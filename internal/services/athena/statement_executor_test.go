package athena

import (
	"context"
	"strconv"
	"testing"
	"time"
)

// funcRunner is a queryRunner a test writes inline.
type funcRunner struct {
	fn    func(ctx context.Context, running func()) (*queryResult, *queryFailure)
	async bool
}

func (r funcRunner) run(ctx context.Context, running func()) (*queryResult, *queryFailure) {
	return r.fn(ctx, running)
}

func (r funcRunner) background() bool { return r.async }

// routeAll sends every statement to r.
func routeAll(s *Service, r queryRunner) {
	s.statements.route = func(context.Context, QueryExecution) queryRunner { return r }
}

// waitState waits for an execution to reach state.
func waitState(t *testing.T, s *Service, id, state string) QueryExecution {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		qe := queryState(t, s, id)
		if qe.Status.State == state {
			return qe
		}
		if time.Now().After(deadline) {
			t.Fatalf("state = %s (%s), want %s", qe.Status.State, qe.Status.StateChangeReason, state)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// rowsResult is a SELECT's result of n numbered rows, the header first.
func rowsResult(n int) *queryResult {
	res := &queryResult{Columns: textColumns("n"), Rows: [][]*string{textRow("n")}}
	for i := range n {
		res.Rows = append(res.Rows, textRow(strconv.Itoa(i)))
	}
	res.Runtime = QueryRuntimeStatisticsRows{InputBytes: 99, OutputRows: int64(n)}
	res.timing = engineTiming{QueueMillis: 3, PlanningMillis: 4}
	return res
}

func TestStatementExecutor_backgroundQueryMovesThroughItsStates(t *testing.T) {
	// Given: an engine query that runs until the test lets it finish
	ctx := context.Background()
	s, s3 := newCatalogService(t)
	release := make(chan struct{})
	routeAll(s, funcRunner{async: true, fn: func(_ context.Context, running func()) (*queryResult, *queryFailure) {
		running()
		<-release
		return rowsResult(2500), nil
	}})

	// When: it is started
	out, aerr := s.startQueryExecutionTyped(ctx, startReq("SELECT n"))
	mustOK(t, "StartQueryExecution", aerr)
	id := out.QueryExecutionId

	// Then: it is RUNNING until the engine finishes, then SUCCEEDED with
	// statistics, and its result is written to S3
	waitState(t, s, id, stateRunning)
	close(release)
	qe := waitState(t, s, id, stateSucceeded)
	if st := qe.Statistics; st == nil || st.DataScannedInBytes != 99 || st.QueryQueueTimeInMillis != 3 || st.QueryPlanningTimeInMillis != 4 {
		t.Fatalf("statistics = %+v", qe.Statistics)
	}
	if body := s3.objects["results/"+id+".csv"]; len(body) == 0 || body[:4] != `"n"`+"\n" {
		t.Fatalf("result object = %.20q", body)
	}

	// And: its 2,501 rows page across the stored chunks
	var rows []Row
	token := ""
	for pages := 0; ; pages++ {
		res, aerr := s.getQueryResultsTyped(ctx, &getQueryResultsReq{QueryExecutionId: id, MaxResults: 999, NextToken: token})
		mustOK(t, "GetQueryResults", aerr)
		rows = append(rows, res.ResultSet.Rows...)
		if token = res.NextToken; token == "" {
			if pages != 2 {
				t.Fatalf("read %d pages, want 3", pages+1)
			}
			break
		}
	}
	if len(rows) != 2501 || *rows[0].Data[0].VarCharValue != "n" || *rows[2500].Data[0].VarCharValue != "2499" {
		t.Fatalf("read %d rows", len(rows))
	}
	_, aerr = s.getQueryResultsTyped(ctx, &getQueryResultsReq{QueryExecutionId: id, NextToken: "2501"})
	wantCode(t, "GetQueryResults past the end", aerr, codeInvalidRequest)

	// And: the runtime statistics come from the engine
	stats, aerr := s.getQueryRuntimeStatisticsTyped(ctx, &queryIDReq{QueryExecutionId: id})
	mustOK(t, "GetQueryRuntimeStatistics", aerr)
	if r := stats.QueryRuntimeStatistics; r.Rows == nil || r.Rows.OutputRows != 2500 || r.Timeline == nil || r.Timeline.QueryQueueTimeInMillis != 3 {
		t.Fatalf("runtime statistics = %+v", r)
	}
}

func TestStatementExecutor_stopCancelsTheEngineQuery(t *testing.T) {
	// Given: an engine query that runs until it is cancelled
	ctx := context.Background()
	s, s3 := newCatalogService(t)
	finished := make(chan struct{})
	routeAll(s, funcRunner{async: true, fn: func(ctx context.Context, running func()) (*queryResult, *queryFailure) {
		defer close(finished)
		running()
		<-ctx.Done()
		return rowsResult(1), nil // what it had when it was stopped
	}})
	out, aerr := s.startQueryExecutionTyped(ctx, startReq("SELECT n"))
	mustOK(t, "StartQueryExecution", aerr)
	waitState(t, s, out.QueryExecutionId, stateRunning)

	// When: it is stopped
	_, aerr = s.stopQueryExecutionTyped(ctx, &queryIDReq{QueryExecutionId: out.QueryExecutionId})
	mustOK(t, "StopQueryExecution", aerr)

	// Then: the engine saw the cancellation, the query stays CANCELLED and
	// nothing was kept or written
	<-finished
	s.statements.stop(ctx)
	if st := queryState(t, s, out.QueryExecutionId).Status.State; st != stateCancelled {
		t.Fatalf("state = %s, want CANCELLED", st)
	}
	if res, _ := s.statements.results.header(ctx, out.QueryExecutionId); res != nil || len(s3.objects) != 0 {
		t.Fatalf("a cancelled query kept %+v and wrote %v", res, s3.objects)
	}
}

func TestStatementExecutor_shutdownFailsRunningQueries(t *testing.T) {
	// Given: an engine query in flight
	ctx := context.Background()
	s, _ := newCatalogService(t)
	routeAll(s, funcRunner{async: true, fn: func(ctx context.Context, running func()) (*queryResult, *queryFailure) {
		running()
		<-ctx.Done()
		return nil, failure(errorCategorySystem, errorTypeEngineInternal, ctx.Err().Error())
	}})
	out, aerr := s.startQueryExecutionTyped(ctx, startReq("SELECT n"))
	mustOK(t, "StartQueryExecution", aerr)
	waitState(t, s, out.QueryExecutionId, stateRunning)

	// When: Overcast shuts down
	s.Stop(ctx)

	// Then: the query failed, saying why, as a retryable system error; and a
	// query started after the stop fails the same way
	st := queryState(t, s, out.QueryExecutionId).Status
	if st.State != stateFailed || st.StateChangeReason != shutdownReason || st.AthenaError == nil || !st.AthenaError.Retryable {
		t.Fatalf("status = %+v / %+v", st, st.AthenaError)
	}
	late, aerr := s.startQueryExecutionTyped(ctx, startReq("SELECT n"))
	mustOK(t, "StartQueryExecution after Stop", aerr)
	if st := queryState(t, s, late.QueryExecutionId).Status; st.State != stateFailed || st.StateChangeReason != shutdownReason {
		t.Fatalf("late status = %+v", st)
	}
}

func TestStatementExecutor_unwritableResultLocationFails(t *testing.T) {
	// Given: a result location in a bucket that does not exist
	ctx := context.Background()
	s, _ := newCatalogService(t)
	routeAll(s, funcRunner{fn: func(context.Context, func()) (*queryResult, *queryFailure) { return rowsResult(1), nil }})
	req := startReq("SELECT n")
	req.ResultConfiguration.OutputLocation = "s3://missing/"

	// When: a query runs
	out, aerr := s.startQueryExecutionTyped(ctx, req)
	mustOK(t, "StartQueryExecution", aerr)

	// Then: it fails as a user error naming the missing bucket
	st := queryState(t, s, out.QueryExecutionId).Status
	if st.State != stateFailed || st.AthenaError == nil || st.AthenaError.ErrorCategory != errorCategoryUser || st.AthenaError.ErrorType != errorTypeBucketNotFound {
		t.Fatalf("status = %+v / %+v", st, st.AthenaError)
	}
}

func TestReapInterrupted_failsWhatAPreviousProcessLeftRunning(t *testing.T) {
	// Given: executions a previous process left QUEUED, RUNNING and finished
	ctx := context.Background()
	s, _ := newTestService(t)
	for id, state := range map[string]string{"queued": stateQueued, "running": stateRunning, "done": stateSucceeded} {
		if err := s.store.putQuery(ctx, &QueryExecution{QueryExecutionId: id, Status: QueryExecutionStatus{State: state}}); err != nil {
			t.Fatal(err)
		}
	}

	// When: this process first reads its queries
	for id, want := range map[string]string{"queued": stateFailed, "running": stateFailed, "done": stateSucceeded} {
		// Then: the unfinished ones failed, saying why, and the rest are untouched
		qe := queryState(t, s, id)
		if qe.Status.State != want || (want == stateFailed && qe.Status.StateChangeReason != restartReason) {
			t.Errorf("%s: status = %+v", id, qe.Status)
		}
	}
}
