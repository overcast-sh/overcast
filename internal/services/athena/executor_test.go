package athena

import (
	"context"
	"testing"

	"github.com/overcast-sh/overcast/internal/protocol"
)

// fakeExecutor stands in for a real engine: it records what it was handed and
// lets the test report transitions when it chooses, as an asynchronous engine
// would.
type fakeExecutor struct {
	submitted []QueryExecution
	report    transitionReporter
	cancelled []string
}

func (f *fakeExecutor) Submit(_ context.Context, qe QueryExecution, report transitionReporter) {
	f.submitted = append(f.submitted, qe)
	f.report = report
}

func (f *fakeExecutor) Cancel(_ context.Context, id string) { f.cancelled = append(f.cancelled, id) }

func (f *fakeExecutor) Results(context.Context, QueryExecution, *getQueryResultsReq) (*getQueryResultsResp, *protocol.AWSError) {
	return &getQueryResultsResp{ResultSet: ResultSet{Rows: []Row{{Data: []Datum{{VarCharValue: ptr("1")}}}}}}, nil
}

func newServiceWithExecutor(t *testing.T) (*Service, *fakeExecutor) {
	t.Helper()
	s, _ := newTestService(t)
	f := &fakeExecutor{}
	s.executor = f
	return s, f
}

func queryState(t *testing.T, s *Service, id string) QueryExecution {
	t.Helper()
	out, aerr := s.getQueryExecutionTyped(context.Background(), &queryIDReq{QueryExecutionId: id})
	mustOK(t, "GetQueryExecution", aerr)
	return out.QueryExecution
}

func TestExecutor_receivesQueuedRecordAndDrivesTransitions(t *testing.T) {
	// Given: a query submitted to an engine that has not reported yet
	ctx := context.Background()
	s, f := newServiceWithExecutor(t)
	out, aerr := s.startQueryExecutionTyped(ctx, startReq("SELECT 1"))
	mustOK(t, "StartQueryExecution", aerr)
	id := out.QueryExecutionId

	// Then: the engine was handed the stored, QUEUED record, and results are
	// refused until it finishes
	if len(f.submitted) != 1 || f.submitted[0].QueryExecutionId != id || f.submitted[0].Status.State != stateQueued {
		t.Fatalf("submitted = %+v", f.submitted)
	}
	if got := queryState(t, s, id).Status.State; got != stateQueued {
		t.Fatalf("state = %s, want QUEUED", got)
	}
	_, aerr = s.getQueryResultsTyped(ctx, &getQueryResultsReq{QueryExecutionId: id})
	wantCode(t, "GetQueryResults while QUEUED", aerr, codeInvalidRequest)

	// When: the engine reports RUNNING, then SUCCEEDED with statistics
	f.report(ctx, id, queryTransition{State: stateRunning})
	if got := queryState(t, s, id); got.Status.State != stateRunning || got.Status.CompletionDateTime != 0 {
		t.Fatalf("after RUNNING: %+v", got.Status)
	}
	f.report(ctx, id, queryTransition{State: stateSucceeded, Statistics: &QueryExecutionStatistics{DataScannedInBytes: 42}})

	// Then: the record is complete and results come from the engine
	qe := queryState(t, s, id)
	if qe.Status.State != stateSucceeded || qe.Status.CompletionDateTime == 0 || qe.Statistics.DataScannedInBytes != 42 {
		t.Fatalf("after SUCCEEDED: %+v / %+v", qe.Status, qe.Statistics)
	}
	res, aerr := s.getQueryResultsTyped(ctx, &getQueryResultsReq{QueryExecutionId: id})
	mustOK(t, "GetQueryResults", aerr)
	if len(res.ResultSet.Rows) != 1 {
		t.Fatalf("Rows = %+v", res.ResultSet.Rows)
	}
}

func TestExecutor_stopIsFinal(t *testing.T) {
	// Given: a running query
	ctx := context.Background()
	s, f := newServiceWithExecutor(t)
	out, aerr := s.startQueryExecutionTyped(ctx, startReq("SELECT 1"))
	mustOK(t, "StartQueryExecution", aerr)
	id := out.QueryExecutionId
	f.report(ctx, id, queryTransition{State: stateRunning})

	// When: it is stopped, and the engine then reports success anyway
	_, aerr = s.stopQueryExecutionTyped(ctx, &queryIDReq{QueryExecutionId: id})
	mustOK(t, "StopQueryExecution", aerr)
	f.report(ctx, id, queryTransition{State: stateSucceeded})

	// Then: the engine was told, the query stays CANCELLED, and its results
	// are refused as a query that did not finish successfully
	if len(f.cancelled) != 1 || f.cancelled[0] != id {
		t.Fatalf("cancelled = %v", f.cancelled)
	}
	if got := queryState(t, s, id).Status; got.State != stateCancelled || got.CompletionDateTime == 0 {
		t.Fatalf("status = %+v, want CANCELLED with a completion time", got)
	}
	_, aerr = s.getQueryResultsTyped(ctx, &getQueryResultsReq{QueryExecutionId: id})
	wantCode(t, "GetQueryResults after cancel", aerr, codeInvalidRequest)
}

func TestExecutor_failureCarriesAthenaError(t *testing.T) {
	// Given: a submitted query
	ctx := context.Background()
	s, f := newServiceWithExecutor(t)
	out, aerr := s.startQueryExecutionTyped(ctx, startReq("SELECT nope"))
	mustOK(t, "StartQueryExecution", aerr)

	// When: the engine reports a failure
	f.report(ctx, out.QueryExecutionId, queryTransition{
		State: stateFailed, StateChangeReason: "COLUMN_NOT_FOUND",
		AthenaError: &AthenaError{ErrorCategory: 2, ErrorType: 1006, ErrorMessage: "Column 'nope' cannot be resolved"},
	})

	// Then: the record carries the reason and the error
	st := queryState(t, s, out.QueryExecutionId).Status
	if st.State != stateFailed || st.StateChangeReason != "COLUMN_NOT_FOUND" || st.AthenaError == nil || st.AthenaError.ErrorType != 1006 {
		t.Fatalf("status = %+v", st)
	}
}

func TestExecutor_backwardAndUnknownReportsAreDropped(t *testing.T) {
	// Given: a running query
	ctx := context.Background()
	s, f := newServiceWithExecutor(t)
	out, aerr := s.startQueryExecutionTyped(ctx, startReq("SELECT 1"))
	mustOK(t, "StartQueryExecution", aerr)
	f.report(ctx, out.QueryExecutionId, queryTransition{State: stateRunning})

	// When: the engine reports QUEUED again, then a state that does not exist
	f.report(ctx, out.QueryExecutionId, queryTransition{State: stateQueued})
	f.report(ctx, out.QueryExecutionId, queryTransition{State: "PAUSED"})

	// Then: the query is still RUNNING
	if got := queryState(t, s, out.QueryExecutionId).Status.State; got != stateRunning {
		t.Fatalf("state = %s, want RUNNING", got)
	}
}

func TestDeleteWorkGroup_recursiveCancelsRunningQueries(t *testing.T) {
	// Given: a workgroup with a running query and a finished one
	ctx := context.Background()
	s, f := newServiceWithExecutor(t)
	_, aerr := s.createWorkGroupTyped(ctx, &createWorkGroupReq{Name: "wg"})
	mustOK(t, "CreateWorkGroup", aerr)
	start := func() string {
		req := startReq("SELECT 1")
		req.WorkGroup = "wg"
		out, aerr := s.startQueryExecutionTyped(ctx, req)
		mustOK(t, "StartQueryExecution", aerr)
		return out.QueryExecutionId
	}
	running, finished := start(), start()
	f.report(ctx, finished, queryTransition{State: stateSucceeded})

	// When: a plain delete, then a recursive one
	_, aerr = s.deleteWorkGroupTyped(ctx, &deleteWorkGroupReq{WorkGroup: "wg"})
	mustOK(t, "DeleteWorkGroup", aerr)
	if _, aerr := s.getQueryExecutionTyped(ctx, &queryIDReq{QueryExecutionId: running}); aerr != nil {
		t.Fatalf("a non-recursive delete removed the workgroup's executions: %s", aerr.Message)
	}
	_, aerr = s.createWorkGroupTyped(ctx, &createWorkGroupReq{Name: "wg"})
	mustOK(t, "CreateWorkGroup again", aerr)
	_, aerr = s.deleteWorkGroupTyped(ctx, &deleteWorkGroupReq{WorkGroup: "wg", RecursiveDeleteOption: ptr(true)})
	mustOK(t, "DeleteWorkGroup recursive", aerr)

	// Then: only the running query was cancelled, and both are gone
	if len(f.cancelled) != 1 || f.cancelled[0] != running {
		t.Fatalf("cancelled = %v, want [%s]", f.cancelled, running)
	}
	_, aerr = s.getQueryExecutionTyped(ctx, &queryIDReq{QueryExecutionId: running})
	wantCode(t, "GetQueryExecution after recursive delete", aerr, codeInvalidRequest)
}

func TestInertExecutor_resultsHaveOnePage(t *testing.T) {
	ctx := context.Background()
	s, _ := newTestService(t)
	out, aerr := s.startQueryExecutionTyped(ctx, startReq("SELECT 1"))
	mustOK(t, "StartQueryExecution", aerr)
	_, aerr = s.getQueryResultsTyped(ctx, &getQueryResultsReq{QueryExecutionId: out.QueryExecutionId, NextToken: "bogus"})
	wantCode(t, "GetQueryResults with a NextToken", aerr, codeInvalidRequest)
}
