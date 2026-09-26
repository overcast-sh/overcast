// Package athenaquery runs one Athena query through the AWS SDK and collects
// its outcome: it starts the query, waits for a final state, and reads the
// result rows with their column types. `overcast athena query`, the runtime
// MCP tool and the sample dataset all run their SQL through it.
package athenaquery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/athena"
	"github.com/aws/aws-sdk-go-v2/service/athena/types"

	"github.com/overcast-sh/overcast/internal/clock"
	athenasvc "github.com/overcast-sh/overcast/internal/services/athena"
)

// DefaultPollInterval is how often a running query is asked for its state.
const DefaultPollInterval = 250 * time.Millisecond

// API is the part of the Athena client a query needs.
type API interface {
	StartQueryExecution(context.Context, *athena.StartQueryExecutionInput, ...func(*athena.Options)) (*athena.StartQueryExecutionOutput, error)
	GetQueryExecution(context.Context, *athena.GetQueryExecutionInput, ...func(*athena.Options)) (*athena.GetQueryExecutionOutput, error)
	GetQueryResults(context.Context, *athena.GetQueryResultsInput, ...func(*athena.Options)) (*athena.GetQueryResultsOutput, error)
}

// Request is the query to run and where. Empty fields take Athena's defaults:
// the primary workgroup, AwsDataCatalog, and the workgroup's result location.
type Request struct {
	SQL            string
	WorkGroup      string
	Catalog        string
	Database       string
	OutputLocation string
}

// Options tune how a query is waited for and read.
type Options struct {
	// Timeout bounds the wait; when it passes, Run returns the state the
	// query is in, marked TimedOut, and leaves it running. Zero waits for as
	// long as ctx allows.
	Timeout time.Duration
	// MaxRows caps the rows read; zero reads them all.
	MaxRows int
	// PollInterval defaults to DefaultPollInterval.
	PollInterval time.Duration
	// Clock defaults to the wall clock.
	Clock clock.Clock
	// OnPoll sees the execution after each poll that finds it still running.
	OnPoll func(context.Context, types.QueryExecution)
}

// Column is one result column.
type Column struct {
	Name string
	Type string
}

// Result is a query's outcome.
type Result struct {
	QueryExecutionID string
	State            types.QueryExecutionState
	StateReason      string
	Error            *types.AthenaError
	StatementType    types.StatementType
	Statistics       *types.QueryExecutionStatistics
	Columns          []Column
	// Rows are the values in column order; nil is NULL.
	Rows [][]*string
	// Truncated says there were more rows than MaxRows.
	Truncated   bool
	UpdateCount *int64
	// TimedOut says the wait ended before the query did.
	TimedOut bool
}

// Finished reports whether the query reached a final state.
func (r *Result) Finished() bool { return IsFinal(r.State) }

// Succeeded reports whether the query succeeded.
func (r *Result) Succeeded() bool { return r.State == types.QueryExecutionStateSucceeded }

// IsFinal reports whether a query in state s will not change again.
func IsFinal(s types.QueryExecutionState) bool {
	switch s {
	case types.QueryExecutionStateSucceeded, types.QueryExecutionStateFailed, types.QueryExecutionStateCancelled:
		return true
	case types.QueryExecutionStateQueued, types.QueryExecutionStateRunning:
		return false
	}
	return false
}

// Run starts req, waits for it within opts.Timeout, and reads its results
// when it succeeded. An error means the query could not be started or
// followed; a query that failed is a Result in state FAILED.
func Run(ctx context.Context, api API, req Request, opts Options) (*Result, error) {
	id, err := Start(ctx, api, req)
	if err != nil {
		return nil, err
	}
	return Follow(ctx, api, id, opts)
}

// Start starts req and returns its query execution id.
func Start(ctx context.Context, api API, req Request) (string, error) {
	in := &athena.StartQueryExecutionInput{QueryString: aws.String(req.SQL)}
	if req.WorkGroup != "" {
		in.WorkGroup = aws.String(req.WorkGroup)
	}
	if req.Catalog != "" || req.Database != "" {
		in.QueryExecutionContext = &types.QueryExecutionContext{Catalog: optional(req.Catalog), Database: optional(req.Database)}
	}
	if req.OutputLocation != "" {
		in.ResultConfiguration = &types.ResultConfiguration{OutputLocation: aws.String(req.OutputLocation)}
	}
	out, err := api.StartQueryExecution(ctx, in)
	if err != nil {
		return "", fmt.Errorf("start query: %w", err)
	}
	return aws.ToString(out.QueryExecutionId), nil
}

// Follow waits for the query id within opts.Timeout and reads its results
// when it succeeded.
func Follow(ctx context.Context, api API, id string, opts Options) (*Result, error) {
	qe, err := wait(ctx, api, id, opts)
	if err != nil {
		return nil, err
	}
	res := describe(id, qe)
	if !IsFinal(res.State) {
		res.TimedOut = true
		return res, nil
	}
	if res.Succeeded() {
		if err := readResults(ctx, api, res, opts.MaxRows); err != nil {
			return nil, err
		}
	}
	return res, nil
}

// wait polls the query until it is final or opts.Timeout passes, and
// returns its last known execution.
func wait(ctx context.Context, api API, id string, opts Options) (types.QueryExecution, error) {
	clk := opts.Clock
	if clk == nil {
		clk = clock.New()
	}
	interval := opts.PollInterval
	if interval <= 0 {
		interval = DefaultPollInterval
	}
	var deadline <-chan time.Time
	if opts.Timeout > 0 {
		timer := clk.Timer(opts.Timeout)
		defer timer.Stop()
		deadline = timer.C
	}
	tick := clk.Ticker(interval)
	defer tick.Stop()
	for {
		out, err := api.GetQueryExecution(ctx, &athena.GetQueryExecutionInput{QueryExecutionId: aws.String(id)})
		if err != nil {
			return types.QueryExecution{}, fmt.Errorf("get query execution %s: %w", id, err)
		}
		if out.QueryExecution == nil {
			return types.QueryExecution{}, fmt.Errorf("get query execution %s: no execution in the response", id)
		}
		qe := *out.QueryExecution
		if IsFinal(StateOf(qe)) {
			return qe, nil
		}
		if opts.OnPoll != nil {
			opts.OnPoll(ctx, qe)
		}
		select {
		case <-ctx.Done():
			return types.QueryExecution{}, fmt.Errorf("query %s is still %s: %w", id, StateOf(qe), ctx.Err())
		case <-deadline:
			return qe, nil
		case <-tick.C:
		}
	}
}

func describe(id string, qe types.QueryExecution) *Result {
	res := &Result{QueryExecutionID: id, StatementType: qe.StatementType, Statistics: qe.Statistics}
	if qe.Status != nil {
		res.State, res.Error = qe.Status.State, qe.Status.AthenaError
		res.StateReason = aws.ToString(qe.Status.StateChangeReason)
	}
	return res
}

// StateOf is an execution's state, or "" when it reports none.
func StateOf(qe types.QueryExecution) types.QueryExecutionState {
	if qe.Status == nil {
		return ""
	}
	return qe.Status.State
}

// readResults pages through the query's results into res, up to maxRows.
func readResults(ctx context.Context, api API, res *Result, maxRows int) error {
	var token *string
	first := true
	for {
		out, err := api.GetQueryResults(ctx, &athena.GetQueryResultsInput{QueryExecutionId: aws.String(res.QueryExecutionID), NextToken: token})
		if err != nil {
			return fmt.Errorf("get query results %s: %w", res.QueryExecutionID, err)
		}
		res.UpdateCount = out.UpdateCount
		rows := out.ResultSet.Rows
		if first {
			res.Columns = columns(out.ResultSet.ResultSetMetadata)
			rows = withoutHeader(rows, res.Columns, res.StatementType)
			first = false
		}
		for _, row := range rows {
			if maxRows > 0 && len(res.Rows) == maxRows {
				res.Truncated = true
				return nil
			}
			res.Rows = append(res.Rows, values(row))
		}
		if token = out.NextToken; token == nil {
			return nil
		}
	}
}

func columns(meta *types.ResultSetMetadata) []Column {
	if meta == nil {
		return nil
	}
	cols := make([]Column, len(meta.ColumnInfo))
	for i, c := range meta.ColumnInfo {
		cols[i] = Column{Name: aws.ToString(c.Name), Type: aws.ToString(c.Type)}
	}
	return cols
}

// withoutHeader drops the row of column labels Athena puts first in a
// SELECT's results.
func withoutHeader(rows []types.Row, cols []Column, st types.StatementType) []types.Row {
	if st != types.StatementTypeDml || len(rows) == 0 || len(rows[0].Data) != len(cols) {
		return rows
	}
	for i, d := range rows[0].Data {
		if aws.ToString(d.VarCharValue) != cols[i].Name {
			return rows
		}
	}
	return rows[1:]
}

func values(row types.Row) []*string {
	out := make([]*string, len(row.Data))
	for i, d := range row.Data {
		out[i] = d.VarCharValue
	}
	return out
}

func optional(s string) *string {
	if s == "" {
		return nil
	}
	return aws.String(s)
}

// Failure is the error a query that did not succeed stands for.
func (r *Result) Failure() error {
	if r.Succeeded() {
		return nil
	}
	msg := r.StateReason
	if msg == "" && r.Error != nil {
		msg = aws.ToString(r.Error.ErrorMessage)
	}
	if msg == "" {
		return fmt.Errorf("query %s", r.State)
	}
	return errors.New("query " + string(r.State) + ": " + msg)
}

// FetchEngineStatus reads the query engine's status from the Overcast at
// endpoint.
func FetchEngineStatus(ctx context.Context, client *http.Client, endpoint string) (athenasvc.EngineStatus, error) {
	var st athenasvc.EngineStatus
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(endpoint, "/")+athenasvc.EngineStatusPath, nil)
	if err != nil {
		return st, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return st, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return st, fmt.Errorf("engine status: %s", resp.Status)
	}
	return st, json.NewDecoder(resp.Body).Decode(&st)
}
