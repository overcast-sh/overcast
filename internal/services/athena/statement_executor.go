package athena

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/overcast-sh/overcast/internal/clock"
	"github.com/overcast-sh/overcast/internal/protocol"
	"github.com/overcast-sh/overcast/internal/serviceutil"
)

// statement_executor.go — the queryExecutor: it hands each statement to the
// runner that owns it (statementRouter), reports the query's progress, and
// keeps and writes what the statement produced.

// queryRunner runs one statement to its end.
type queryRunner interface {
	// run executes the statement, calling running once it starts executing
	// rather than waiting. A runner that finishes at once need not call it.
	// Rows go to out as the statement produces them, or, for a result
	// Overcast makes itself, in the result's Rows.
	run(ctx context.Context, running func(), out rowSink) (*queryResult, *queryFailure)
	// background reports whether the statement runs on a goroutine of its
	// own rather than before Submit returns: true for the query engine,
	// whose queries take as long as they take.
	background() bool
}

// queryFailure is why a statement did not succeed.
type queryFailure struct {
	// State is FAILED, or CANCELLED when the engine itself stopped the query
	// (a workgroup's bytes-scanned cutoff).
	State  string
	Reason string
	Error  *AthenaError
}

func failure(category, errorType int32, reason string) *queryFailure {
	return &queryFailure{State: stateFailed, Reason: reason,
		Error: &AthenaError{ErrorCategory: category, ErrorType: errorType, ErrorMessage: reason}}
}

// Why a query's context ended, when it was not the query finishing.
var (
	errQueryCancelled = errors.New("athena: query cancelled")
	errShuttingDown   = errors.New("athena: shutting down")
)

// The StateChangeReason of a query Overcast stopped running because it shut
// down, and of one a previous process left running.
const (
	shutdownReason = "Overcast shut down while this query was running."
	restartReason  = "Overcast restarted while this query was running."
)

// interrupted is the transition that fails a query Overcast stopped running.
func interrupted(reason string) queryTransition {
	return queryTransition{State: stateFailed, StateChangeReason: reason,
		AthenaError: &AthenaError{ErrorCategory: errorCategorySystem, ErrorType: errorTypeInternal, ErrorMessage: reason, Retryable: true}}
}

type statementExecutor struct {
	route   func(ctx context.Context, qe QueryExecution) queryRunner
	results resultStore
	output  *resultWriter // nil until S3 is wired: results are not written
	// maxResultBytes is ATHENA_MAX_RESULT_BYTES; 0, which only a Config
	// built in a test holds, is no limit.
	maxResultBytes int64
	clk            clock.Clock
	log            *serviceutil.ServiceLogger

	mu       sync.Mutex
	running  map[string]context.CancelCauseFunc
	stopping bool
	wg       sync.WaitGroup
}

var _ queryExecutor = (*statementExecutor)(nil)

func (e *statementExecutor) Submit(ctx context.Context, qe QueryExecution, report transitionReporter) {
	r := e.route(ctx, qe)
	if !r.background() {
		e.execute(ctx, qe, r, report)
		return
	}
	runCtx, cancel := context.WithCancelCause(ctx)
	if !e.track(qe.QueryExecutionId, cancel) {
		cancel(errShuttingDown)
		report(ctx, qe.QueryExecutionId, interrupted(shutdownReason))
		return
	}
	go func() {
		defer e.untrack(qe.QueryExecutionId)
		e.execute(runCtx, qe, r, report)
	}()
}

// track records a background query's cancel, unless the executor is
// stopping.
func (e *statementExecutor) track(id string, cancel context.CancelCauseFunc) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.stopping {
		return false
	}
	if e.running == nil {
		e.running = map[string]context.CancelCauseFunc{}
	}
	e.running[id] = cancel
	e.wg.Add(1)
	return true
}

func (e *statementExecutor) untrack(id string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if cancel, ok := e.running[id]; ok {
		cancel(nil)
		delete(e.running, id)
	}
	e.wg.Done()
}

// execute runs one statement and reports how it ended.
func (e *statementExecutor) execute(ctx context.Context, qe QueryExecution, r queryRunner, report transitionReporter) {
	id := qe.QueryExecutionId
	reportCtx := context.WithoutCancel(ctx)
	out := e.stream(reportCtx, qe)
	started := e.clk.Now()
	res, fail := r.run(ctx, func() { report(reportCtx, id, queryTransition{State: stateRunning}) }, out)
	finished := e.clk.Now()
	switch {
	case fail == nil && ctx.Err() != nil:
		// Stopped as it finished: a cancelled query keeps no result.
		fail = &queryFailure{State: stateCancelled, Reason: "Query was cancelled."}
	case fail == nil && res != nil: // the inert engine produces nothing to keep
		fail = keep(res, out)
		if fail == nil && ctx.Err() != nil { // cancelled while it was kept
			e.forget(reportCtx, id)
		}
	}
	out.abort()
	if fail == nil {
		report(reportCtx, id, queryTransition{State: stateSucceeded, Statistics: executionStatistics(res, started, finished, e.clk.Now())})
		return
	}
	t := queryTransition{State: fail.State, StateChangeReason: fail.Reason, AthenaError: fail.Error}
	if context.Cause(ctx) == errShuttingDown {
		t = interrupted(shutdownReason)
	}
	t.Statistics = executionStatistics(res, started, finished, finished)
	report(reportCtx, id, t)
}

// stream is where qe's rows go: its stored result, and its OutputLocation
// when there is one to write to.
func (e *statementExecutor) stream(ctx context.Context, qe QueryExecution) *resultStream {
	s := &resultStream{ctx: ctx, id: qe.QueryExecutionId, chunks: e.results.writer(qe.QueryExecutionId), limit: e.maxResultBytes, log: e.log}
	if location := qe.ResultConfiguration.OutputLocation; location != "" && e.output != nil {
		s.output, s.location = e.output, location
	}
	return s
}

// keep writes out the rows a statement returned rather than streamed, then
// commits its result.
func keep(res *queryResult, out *resultStream) *queryFailure {
	for _, row := range res.Rows {
		if fail := out.add(row); fail != nil {
			return fail
		}
	}
	res.Rows = nil
	return out.commit(res)
}

// executionStatistics fills QueryExecutionStatistics: what the engine said
// about queueing, planning and scanning, and the wall clock around it. Rows
// are written as the engine returns them, so their writing is engine time;
// committing the result is the service's processing time.
func executionStatistics(res *queryResult, started, finished, written time.Time) *QueryExecutionStatistics {
	stats := &QueryExecutionStatistics{
		ResultReuseInformation:        &ResultReuseInformation{},
		TotalExecutionTimeInMillis:    written.Sub(started).Milliseconds(),
		ServiceProcessingTimeInMillis: written.Sub(finished).Milliseconds(),
	}
	engine := finished.Sub(started).Milliseconds()
	if res != nil {
		t := res.timing
		stats.QueryQueueTimeInMillis = t.QueueMillis
		stats.QueryPlanningTimeInMillis = t.PlanningMillis
		stats.DataScannedInBytes = res.Runtime.InputBytes
		engine -= t.QueueMillis
	}
	stats.EngineExecutionTimeInMillis = max(engine, 0)
	return stats
}

// Cancel abandons a running query and forgets its result.
func (e *statementExecutor) Cancel(ctx context.Context, id string) {
	e.mu.Lock()
	if cancel, ok := e.running[id]; ok {
		cancel(errQueryCancelled)
	}
	e.mu.Unlock()
	e.forget(ctx, id)
}

// forget deletes a query's stored result.
func (e *statementExecutor) forget(ctx context.Context, id string) {
	if err := e.results.delete(ctx, id); err != nil {
		e.log.WithRecorder(ctx).Warn("query result not deleted", zap.String("queryExecutionId", id), zap.Error(err))
	}
}

// stop fails every query still running and waits, within ctx, for them to
// report it.
func (e *statementExecutor) stop(ctx context.Context) {
	e.mu.Lock()
	e.stopping = true
	for _, cancel := range e.running {
		cancel(errShuttingDown)
	}
	e.mu.Unlock()
	done := make(chan struct{})
	go func() { e.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-ctx.Done():
	}
}

func (e *statementExecutor) Results(ctx context.Context, qe QueryExecution, req *getQueryResultsReq) (*getQueryResultsResp, *protocol.AWSError) {
	res, err := e.results.header(ctx, qe.QueryExecutionId)
	if err != nil {
		return nil, errInternal(err)
	}
	if res == nil { // the inert engine, or DDL with nothing to report
		res = &queryResult{Columns: []ColumnInfo{}}
	}
	offset := 0
	if req.NextToken != "" {
		n, err := strconv.Atoi(req.NextToken)
		if err != nil || n <= 0 || n >= res.RowCount {
			return nil, errInvalidRequest("Invalid NextToken.")
		}
		offset = n
	}
	limit := int(req.MaxResults)
	if limit == 0 {
		limit = maxQueryResults
	}
	rows, err := e.results.rows(ctx, qe.QueryExecutionId, res, offset, limit)
	if err != nil {
		return nil, errInternal(err)
	}
	resp := &getQueryResultsResp{
		ResultSet:   ResultSet{Rows: toRows(rows), ResultSetMetadata: ResultSetMetadata{ColumnInfo: res.Columns}},
		UpdateCount: res.UpdateCount,
	}
	if next := offset + len(rows); next < res.RowCount {
		resp.NextToken = strconv.Itoa(next)
	}
	return resp, nil
}

func toRows(rows [][]*string) []Row {
	out := make([]Row, len(rows))
	for i, row := range rows {
		data := make([]Datum, len(row))
		for j, cell := range row {
			data[j] = Datum{VarCharValue: cell}
		}
		out[i] = Row{Data: data}
	}
	return out
}

func (e *statementExecutor) RuntimeStatistics(ctx context.Context, qe QueryExecution) (*QueryRuntimeStatistics, *protocol.AWSError) {
	res, err := e.results.header(ctx, qe.QueryExecutionId)
	if err != nil {
		return nil, errInternal(err)
	}
	rows := QueryRuntimeStatisticsRows{}
	if res != nil {
		rows = res.Runtime
	}
	out := &QueryRuntimeStatistics{Rows: &rows}
	if st := qe.Statistics; st != nil {
		out.Timeline = &QueryRuntimeStatisticsTimeline{
			EngineExecutionTimeInMillis:      st.EngineExecutionTimeInMillis,
			QueryPlanningTimeInMillis:        st.QueryPlanningTimeInMillis,
			QueryQueueTimeInMillis:           st.QueryQueueTimeInMillis,
			ServicePreProcessingTimeInMillis: st.ServicePreProcessingTimeInMillis,
			ServiceProcessingTimeInMillis:    st.ServiceProcessingTimeInMillis,
			TotalExecutionTimeInMillis:       st.TotalExecutionTimeInMillis,
		}
	}
	return out, nil
}
