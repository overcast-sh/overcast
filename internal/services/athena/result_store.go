package athena

import (
	"context"
	"fmt"
	"strconv"
)

// result_store.go — a finished query's result, kept for GetQueryResults and
// GetQueryRuntimeStatistics.
//
// A result is stored as one header record ("<id>") and its rows in chunks of
// resultChunkRows ("<id>/<n>"), so a page reads only the chunks it covers
// rather than the whole result. The namespace is Cached-tier: results are
// read back rarely and can be large.

const (
	nsResults       = "athena:results"
	resultChunkRows = 1000
)

// queryResult is what a statement produced: its columns and rows, as text,
// and what it wrote or read.
type queryResult struct {
	Columns []ColumnInfo `json:"columns"`
	// Rows are the result rows. For a SELECT the first is the header row,
	// the column names, as Athena returns it.
	Rows        [][]*string                `json:"-"`
	RowCount    int                        `json:"rowCount"`
	UpdateCount *int64                     `json:"updateCount,omitempty"`
	Runtime     QueryRuntimeStatisticsRows `json:"runtime"`
	// timing is how long the engine queued and planned the statement,
	// reported in the execution's Statistics rather than stored here.
	timing engineTiming
}

// engineTiming is the part of a statement's time only its engine can see.
// Queueing includes starting the engine.
type engineTiming struct {
	QueueMillis    int64
	PlanningMillis int64
}

type resultStore struct{ store *athenaStore }

func resultChunkKey(id string, chunk int) string { return id + "/" + strconv.Itoa(chunk) }

// put stores res under the query's id.
func (s resultStore) put(ctx context.Context, id string, res *queryResult) error {
	res.RowCount = len(res.Rows)
	for start := 0; start < len(res.Rows); start += resultChunkRows {
		chunk := res.Rows[start:min(start+resultChunkRows, len(res.Rows))]
		if err := s.store.put(ctx, nsResults, resultChunkKey(id, start/resultChunkRows), chunk); err != nil {
			return err
		}
	}
	return s.store.put(ctx, nsResults, id, res)
}

// header reads a result without its rows, or nil when there is none.
func (s resultStore) header(ctx context.Context, id string) (*queryResult, error) {
	return getRecord[queryResult](ctx, s.store, nsResults, id)
}

// rows reads rows [offset, offset+n) of a result whose header is res.
func (s resultStore) rows(ctx context.Context, id string, res *queryResult, offset, n int) ([][]*string, error) {
	end := min(offset+n, res.RowCount)
	out := make([][]*string, 0, max(end-offset, 0))
	for chunk := offset / resultChunkRows; offset+len(out) < end; chunk++ {
		rows, err := getRecord[[][]*string](ctx, s.store, nsResults, resultChunkKey(id, chunk))
		if err != nil {
			return nil, err
		}
		if rows == nil {
			return nil, fmt.Errorf("athena: result %s is missing rows from %d", id, chunk*resultChunkRows)
		}
		from := offset + len(out) - chunk*resultChunkRows
		to := min(len(*rows), end-chunk*resultChunkRows)
		out = append(out, (*rows)[from:to]...)
	}
	return out, nil
}

// delete forgets a result.
func (s resultStore) delete(ctx context.Context, id string) error {
	res, err := s.header(ctx, id)
	if err != nil || res == nil {
		return err
	}
	for chunk := 0; chunk*resultChunkRows < res.RowCount; chunk++ {
		if err := s.store.delete(ctx, nsResults, resultChunkKey(id, chunk)); err != nil {
			return err
		}
	}
	return s.store.delete(ctx, nsResults, id)
}
