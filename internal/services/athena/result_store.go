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
// rather than the whole result. The chunks are stored as the statement
// produces its rows and the header last, so a result is readable only once
// it is whole. The namespace is Cached-tier: results are read back rarely
// and can be large.

const (
	nsResults       = "athena:results"
	resultChunkRows = 1000
)

// queryResult is what a statement produced: its columns and rows, as text,
// and what it wrote or read.
type queryResult struct {
	Columns []ColumnInfo `json:"columns"`
	// Rows are the rows of a result Overcast makes itself, such as a SHOW
	// statement's, which the executor writes out once the statement ends.
	// The engine writes its rows to the statement's rowSink as they come
	// instead, and leaves Rows empty.
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

// resultRowsPrefix is the prefix of every chunk key of a result's rows.
func resultRowsPrefix(id string) string { return id + "/" }

func resultChunkKey(id string, chunk int) string { return resultRowsPrefix(id) + strconv.Itoa(chunk) }

// writer starts storing the result of the query with id.
func (s resultStore) writer(id string) *resultChunkWriter {
	return &resultChunkWriter{store: s, id: id}
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

// delete forgets a result, finished or not: its rows are found by their
// keys rather than through the header, which an unfinished result lacks.
func (s resultStore) delete(ctx context.Context, id string) error {
	if err := s.deleteRows(ctx, id); err != nil {
		return err
	}
	return s.store.delete(ctx, nsResults, id)
}

// deleteRows deletes every chunk of a result's rows.
func (s resultStore) deleteRows(ctx context.Context, id string) error {
	keys, err := s.store.store.List(ctx, nsResults, resultRowsPrefix(id))
	if err != nil {
		return fmt.Errorf("athena: list result %s: %w", id, err)
	}
	for _, key := range keys {
		if err := s.store.delete(ctx, nsResults, key); err != nil {
			return err
		}
	}
	return nil
}

// resultChunkWriter stores a result's rows as they come, holding no more
// than a chunk of them.
type resultChunkWriter struct {
	store  resultStore
	id     string
	chunk  [][]*string
	stored int // chunks already stored
	rows   int
}

// write takes the next row, storing the chunk it completes.
func (w *resultChunkWriter) write(ctx context.Context, row []*string) error {
	w.chunk = append(w.chunk, row)
	w.rows++
	if len(w.chunk) < resultChunkRows {
		return nil
	}
	return w.flush(ctx)
}

func (w *resultChunkWriter) flush(ctx context.Context) error {
	if len(w.chunk) == 0 {
		return nil
	}
	if err := w.store.store.put(ctx, nsResults, resultChunkKey(w.id, w.stored), w.chunk); err != nil {
		return err
	}
	w.stored++
	clear(w.chunk)
	w.chunk = w.chunk[:0]
	return nil
}

// close stores the last chunk, then res as the result's header, which makes
// the result readable.
func (w *resultChunkWriter) close(ctx context.Context, res *queryResult) error {
	if err := w.flush(ctx); err != nil {
		return err
	}
	res.RowCount = w.rows
	return w.store.store.put(ctx, nsResults, w.id, res)
}

// discard deletes the chunks stored so far of a result that is not kept.
func (w *resultChunkWriter) discard(ctx context.Context) error {
	if w.stored == 0 {
		return nil
	}
	return w.store.deleteRows(ctx, w.id)
}
