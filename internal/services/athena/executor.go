package athena

import (
	"context"

	"go.uber.org/zap"

	"github.com/overcast-sh/overcast/internal/protocol"
)

// Query execution states (QueryExecutionState).
const (
	stateQueued    = "QUEUED"
	stateRunning   = "RUNNING"
	stateSucceeded = "SUCCEEDED"
	stateFailed    = "FAILED"
	stateCancelled = "CANCELLED"
)

func isTerminal(state string) bool {
	return state == stateSucceeded || state == stateFailed || state == stateCancelled
}

// queryExecutor runs the queries StartQueryExecution accepts.
//
// The lifecycle is split so that the service owns the record and the
// executor owns only the running: StartQueryExecution stores the execution
// QUEUED and hands it to Submit; the executor reports each state change
// through report, which the service validates and persists
// (applyTransition); GetQueryResults asks the executor for the rows once the
// record says SUCCEEDED. StopQueryExecution marks the record CANCELLED itself
// and tells the executor through Cancel, so a stop is final whatever the
// engine does afterwards.
//
// inertExecutor is the implementation wired today. A real engine replaces it
// without touching the operations.
type queryExecutor interface {
	// Submit starts qe. It may report transitions before it returns, or
	// later from its own goroutine, with a context of its own.
	Submit(ctx context.Context, qe QueryExecution, report transitionReporter)
	// Cancel asks the engine to abandon a query the service has already
	// marked CANCELLED.
	Cancel(ctx context.Context, id string)
	// Results returns one page of a SUCCEEDED query's results.
	Results(ctx context.Context, qe QueryExecution, req *getQueryResultsReq) (*getQueryResultsResp, *protocol.AWSError)
}

// queryTransition is one state change an executor reports.
type queryTransition struct {
	State             string
	StateChangeReason string
	AthenaError       *AthenaError
	Statistics        *QueryExecutionStatistics
}

// transitionReporter applies a transition to the execution with id.
type transitionReporter func(ctx context.Context, id string, t queryTransition)

// inertExecutor runs nothing: every query succeeds the moment it is
// submitted, having scanned nothing and returned no rows.
type inertExecutor struct{}

func (inertExecutor) Submit(ctx context.Context, qe QueryExecution, report transitionReporter) {
	report(ctx, qe.QueryExecutionId, queryTransition{
		State: stateSucceeded,
		Statistics: &QueryExecutionStatistics{
			ResultReuseInformation: &ResultReuseInformation{},
		},
	})
}

func (inertExecutor) Cancel(context.Context, string) {}

func (inertExecutor) Results(context.Context, QueryExecution, *getQueryResultsReq) (*getQueryResultsResp, *protocol.AWSError) {
	return &getQueryResultsResp{ResultSet: ResultSet{
		Rows:              []Row{},
		ResultSetMetadata: ResultSetMetadata{ColumnInfo: []ColumnInfo{}},
	}}, nil
}

// applyTransition moves an execution to t.State. A terminal execution never
// moves again, so a report that arrives after a stop, or twice, is dropped.
// Reaching a terminal state stamps CompletionDateTime.
func (s *Service) applyTransition(ctx context.Context, id string, t queryTransition) {
	log := s.log.WithRecorder(ctx)
	defer s.lock(queryLockKey(id))()
	qe, err := s.store.getQuery(ctx, id)
	if err != nil || qe == nil {
		log.Warn("query transition for an unreadable execution dropped", zap.String("queryExecutionId", id), zap.Error(err))
		return
	}
	if isTerminal(qe.Status.State) {
		log.Debug("query transition after a terminal state dropped",
			zap.String("queryExecutionId", id), zap.String("state", qe.Status.State), zap.String("reported", t.State))
		return
	}
	qe.Status.State = t.State
	qe.Status.StateChangeReason = t.StateChangeReason
	qe.Status.AthenaError = t.AthenaError
	if t.Statistics != nil {
		qe.Statistics = t.Statistics
	}
	if isTerminal(t.State) {
		qe.Status.CompletionDateTime = s.now()
	}
	if err := s.store.putQuery(ctx, qe); err != nil {
		log.Error("query transition not persisted", zap.String("queryExecutionId", id), zap.Error(err))
	}
}

func queryLockKey(id string) string { return "query:" + id }
