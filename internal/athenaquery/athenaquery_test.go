package athenaquery

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/athena"
	"github.com/aws/aws-sdk-go-v2/service/athena/types"

	"github.com/overcast-sh/overcast/internal/clock"
)

// fakeAthena answers with scripted states and result pages.
type fakeAthena struct {
	started *athena.StartQueryExecutionInput
	states  []types.QueryExecutionState // one per poll; the last repeats
	polls   int
	failure string
	stmt    types.StatementType
	pages   []*athena.GetQueryResultsOutput
	// polled, when set, hears each poll.
	polled chan struct{}
}

func (f *fakeAthena) StartQueryExecution(_ context.Context, in *athena.StartQueryExecutionInput, _ ...func(*athena.Options)) (*athena.StartQueryExecutionOutput, error) {
	f.started = in
	return &athena.StartQueryExecutionOutput{QueryExecutionId: aws.String("q-1")}, nil
}

func (f *fakeAthena) GetQueryExecution(context.Context, *athena.GetQueryExecutionInput, ...func(*athena.Options)) (*athena.GetQueryExecutionOutput, error) {
	st := f.states[min(f.polls, len(f.states)-1)]
	f.polls++
	if f.polled != nil {
		select {
		case f.polled <- struct{}{}:
		default:
		}
	}
	status := &types.QueryExecutionStatus{State: st}
	if f.failure != "" {
		status.StateChangeReason = aws.String(f.failure)
		status.AthenaError = &types.AthenaError{ErrorCategory: aws.Int32(2), ErrorType: aws.Int32(1000), ErrorMessage: aws.String(f.failure)}
	}
	return &athena.GetQueryExecutionOutput{QueryExecution: &types.QueryExecution{
		QueryExecutionId: aws.String("q-1"), Status: status, StatementType: f.stmt,
		Statistics: &types.QueryExecutionStatistics{DataScannedInBytes: aws.Int64(42)},
	}}, nil
}

func (f *fakeAthena) GetQueryResults(_ context.Context, in *athena.GetQueryResultsInput, _ ...func(*athena.Options)) (*athena.GetQueryResultsOutput, error) {
	i := 0
	if in.NextToken != nil {
		i = int(aws.ToString(in.NextToken)[0] - '0')
	}
	return f.pages[i], nil
}

func row(vals ...*string) types.Row {
	r := types.Row{}
	for _, v := range vals {
		r.Data = append(r.Data, types.Datum{VarCharValue: v})
	}
	return r
}

func selectPages() []*athena.GetQueryResultsOutput {
	meta := &types.ResultSetMetadata{ColumnInfo: []types.ColumnInfo{{Name: aws.String("id"), Type: aws.String("integer")}, {Name: aws.String("name"), Type: aws.String("varchar")}}}
	return []*athena.GetQueryResultsOutput{
		{ResultSet: &types.ResultSet{ResultSetMetadata: meta, Rows: []types.Row{row(aws.String("id"), aws.String("name")), row(aws.String("1"), aws.String("a"))}}, NextToken: aws.String("1")},
		{ResultSet: &types.ResultSet{ResultSetMetadata: meta, Rows: []types.Row{row(aws.String("2"), nil), row(aws.String("3"), aws.String("c"))}}},
	}
}

func TestRun_succeededReadsTypedRowsAcrossPages(t *testing.T) {
	// Given: a SELECT that runs, then succeeds with two pages of rows
	api := &fakeAthena{states: []types.QueryExecutionState{types.QueryExecutionStateQueued, types.QueryExecutionStateSucceeded},
		stmt: types.StatementTypeDml, pages: selectPages()}
	var polled []types.QueryExecutionState

	// When: it is run in a workgroup and database
	res, err := Run(context.Background(), api, Request{SQL: "SELECT 1", WorkGroup: "wg", Database: "db"}, Options{
		PollInterval: time.Millisecond,
		OnPoll:       func(_ context.Context, qe types.QueryExecution) { polled = append(polled, qe.Status.State) },
	})

	// Then: the rows arrive without the header, NULL as nil, with the types
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if aws.ToString(api.started.WorkGroup) != "wg" || aws.ToString(api.started.QueryExecutionContext.Database) != "db" || api.started.QueryExecutionContext.Catalog != nil {
		t.Fatalf("started = %+v", api.started)
	}
	if !res.Succeeded() || res.TimedOut || res.Failure() != nil {
		t.Fatalf("result = %+v", res)
	}
	if len(res.Columns) != 2 || res.Columns[0] != (Column{"id", "integer"}) {
		t.Fatalf("columns = %+v", res.Columns)
	}
	if len(res.Rows) != 3 || *res.Rows[0][0] != "1" || res.Rows[1][1] != nil || *res.Rows[2][1] != "c" {
		t.Fatalf("rows = %v", res.Rows)
	}
	if aws.ToInt64(res.Statistics.DataScannedInBytes) != 42 || len(polled) != 1 || polled[0] != types.QueryExecutionStateQueued {
		t.Fatalf("statistics = %+v, polled = %v", res.Statistics, polled)
	}
}

func TestRun_maxRowsTruncates(t *testing.T) {
	api := &fakeAthena{states: []types.QueryExecutionState{types.QueryExecutionStateSucceeded}, stmt: types.StatementTypeDml, pages: selectPages()}

	res, err := Run(context.Background(), api, Request{SQL: "SELECT 1"}, Options{MaxRows: 2})

	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(res.Rows) != 2 || !res.Truncated {
		t.Fatalf("rows = %d, truncated = %v, want 2 and truncated", len(res.Rows), res.Truncated)
	}
}

func TestRun_failedQueryIsAResult(t *testing.T) {
	api := &fakeAthena{states: []types.QueryExecutionState{types.QueryExecutionStateFailed}, failure: "TABLE_NOT_FOUND: line 1:15: Table 'x' does not exist"}

	res, err := Run(context.Background(), api, Request{SQL: "SELECT * FROM x"}, Options{})

	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.State != types.QueryExecutionStateFailed || res.Error == nil || len(res.Rows) != 0 {
		t.Fatalf("result = %+v", res)
	}
	if err := res.Failure(); err == nil || !strings.Contains(err.Error(), "FAILED: TABLE_NOT_FOUND") {
		t.Fatalf("Failure() = %v", err)
	}
}

func TestRun_timeoutLeavesTheQueryRunning(t *testing.T) {
	// Given: a query that stays RUNNING, waited on for a minute
	api := &fakeAthena{states: []types.QueryExecutionState{types.QueryExecutionStateRunning}, polled: make(chan struct{}, 1)}
	clk := clock.NewMock()
	type outcome struct {
		res *Result
		err error
	}
	done := make(chan outcome)
	go func() {
		res, err := Run(context.Background(), api, Request{SQL: "SELECT 1"}, Options{Timeout: time.Minute, PollInterval: time.Hour, Clock: clk})
		done <- outcome{res, err}
	}()

	// When: the minute passes after the first poll
	<-api.polled
	clk.Add(time.Minute)
	got := <-done

	// Then: the wait ends with the query still running
	res, err := got.res, got.err
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.TimedOut || res.State != types.QueryExecutionStateRunning || res.Finished() {
		t.Fatalf("result = %+v, want timed out while RUNNING", res)
	}
}

func TestRun_nonSelectKeepsItsFirstRow(t *testing.T) {
	// Given: a SHOW statement, whose results have no header row
	pages := []*athena.GetQueryResultsOutput{{ResultSet: &types.ResultSet{
		ResultSetMetadata: &types.ResultSetMetadata{ColumnInfo: []types.ColumnInfo{{Name: aws.String("tab_name"), Type: aws.String("string")}}},
		Rows:              []types.Row{row(aws.String("orders"))},
	}}}
	api := &fakeAthena{states: []types.QueryExecutionState{types.QueryExecutionStateSucceeded}, stmt: types.StatementTypeUtility, pages: pages}

	res, err := Run(context.Background(), api, Request{SQL: "SHOW TABLES"}, Options{})

	if err != nil || len(res.Rows) != 1 || *res.Rows[0][0] != "orders" {
		t.Fatalf("rows = %v, err = %v", res.Rows, err)
	}
}
