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

// stateOrder ranks each state along the only path an execution takes:
// QUEUED, then RUNNING, then one terminal state.
var stateOrder = map[string]int{
	stateQueued: 0, stateRunning: 1,
	stateSucceeded: 2, stateFailed: 2, stateCancelled: 2,
}

func isTerminal(state string) bool { return stateOrder[state] == stateOrder[stateSucceeded] }

// advances reports whether moving from one state to another goes forward.
func advances(from, to string) bool {
	rank, known := stateOrder[to]
	return known && rank > stateOrder[from]
}

// queryExecutor runs the queries StartQueryExecution accepts.
//
// The lifecycle is split so that the service owns the record and the
// executor owns only the running: StartQueryExecution stores the execution
// QUEUED and hands it to Submit; the executor reports each state change
// through report, which the service validates and persists
// (applyTransition); GetQueryResults asks the executor for the rows once the
// record says SUCCEEDED. StopQueryExecution marks the record CANCELLED itself,
// and a recursive DeleteWorkGroup removes it, and each tells the executor
// through Cancel, so a stop is final whatever the engine does afterwards.
//
// No engine resumes a query across a restart. Executions a previous process
// left QUEUED or RUNNING are failed by the service the first time this one
// touches its queries (reapInterrupted), and those still running at shutdown
// are failed by the executor's Stop.
//
// statementExecutor is the one implementation: it routes each statement to
// the Glue catalog (DDL), the Trino engine or the inert engine.
type queryExecutor interface {
	// Submit starts qe. It may report transitions before it returns, or
	// later from its own goroutine; ctx outlives the request.
	Submit(ctx context.Context, qe QueryExecution, report transitionReporter)
	// Cancel asks the engine to abandon a query the service has already
	// marked CANCELLED or deleted, and to forget its results.
	Cancel(ctx context.Context, id string)
	// Results returns one page of a SUCCEEDED query's results, and is where
	// a NextToken is checked, since only the executor knows the pages.
	Results(ctx context.Context, qe QueryExecution, req *getQueryResultsReq) (*getQueryResultsResp, *protocol.AWSError)
	// RuntimeStatistics reports what a finished query read and produced,
	// which only the engine that ran it knows.
	RuntimeStatistics(ctx context.Context, qe QueryExecution) (*QueryRuntimeStatistics, *protocol.AWSError)
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

// applyTransition moves an execution to t.State. Only a move forward along
// QUEUED, RUNNING, terminal is applied: a report that arrives after a stop,
// arrives twice, goes backwards or names no state is dropped.
// Reaching a terminal state stamps CompletionDateTime.
func (s *Service) applyTransition(ctx context.Context, id string, t queryTransition) {
	log := s.log.WithRecorder(ctx)
	defer s.lock(queryLockKey(id))()
	qe, err := s.store.getQuery(ctx, id)
	if err != nil || qe == nil {
		log.Warn("query transition for an unreadable execution dropped", zap.String("queryExecutionId", id), zap.Error(err))
		return
	}
	if !advances(qe.Status.State, t.State) {
		log.Debug("query transition that does not move forward dropped",
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
