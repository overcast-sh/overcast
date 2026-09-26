package athena

import (
	"context"
	"errors"
	"fmt"

	"github.com/overcast-sh/overcast/internal/clock"
)

// trino_runner.go — one statement run on the engine: started when the
// engine is, followed page by page, and turned into Athena's result. Each
// page's rows are passed on as it arrives, so the result is never held
// whole.

// errBytesScannedCutoff stops a query that has read past its workgroup's
// BytesScannedCutoffPerQuery.
var errBytesScannedCutoff = errors.New("athena: bytes scanned cutoff exceeded")

// bytesCutoffReason is the StateChangeReason of a query the cutoff stopped.
const bytesCutoffReason = "Query cancelled! : Bytes scanned limit was exceeded"

type trinoRunner struct {
	engine  *engineManager
	sql     string
	session trinoSession
	// s3Tables is whether the statement runs in, or names, a table bucket's
	// catalog, which the engine is then given first.
	s3Tables bool
	// header puts the column names first, as a SELECT's result has them.
	header bool
	// cutoff is the workgroup's BytesScannedCutoffPerQuery; 0 is none.
	cutoff int64
	clk    clock.Clock
}

func (trinoRunner) background() bool { return true }

func (r trinoRunner) run(ctx context.Context, running func(), out rowSink) (*queryResult, *queryFailure) {
	queued := r.clk.Now()
	endpoint, release, err := r.engine.acquire(ctx)
	if errors.Is(err, errEngineUnavailable) { // no Docker after all: the query runs inert
		return inertRunner{}.run(ctx, running, out)
	}
	if err != nil {
		return nil, failure(errorCategorySystem, errorTypeEngineInternal, err.Error())
	}
	defer release()
	if r.s3Tables {
		r.engine.syncS3TablesCatalogs(ctx, endpoint)
	}
	booted := r.clk.Since(queued).Milliseconds()

	rows := &trinoRows{out: out, header: r.header}
	started := false
	final, err := r.engine.client.execute(ctx, endpoint, r.sql, r.session, func(page *trinoResponse) error {
		if !started {
			started = true
			running()
		}
		if err := rows.page(page); err != nil {
			return err
		}
		if r.cutoff > 0 && page.Stats.PhysicalInputBytes > r.cutoff {
			return errBytesScannedCutoff
		}
		return nil
	})
	if fail := r.failure(endpoint, err); fail != nil {
		return nil, fail
	}
	res, err := rows.finish(final)
	if fail := r.failure(endpoint, err); fail != nil {
		return nil, fail
	}
	res.timing = engineTiming{QueueMillis: booted + final.Stats.QueuedTimeMillis, PlanningMillis: final.Stats.PlanningTimeMillis}
	return res, nil
}

// failure is how a query that did not finish ended.
func (r trinoRunner) failure(endpoint string, err error) *queryFailure {
	var te *trinoError
	var re *resultError
	switch {
	case err == nil:
		return nil
	case errors.As(err, &re): // the result could not take a row
		return re.fail
	case errors.As(err, &te):
		ae := athenaErrorFor(te)
		return &queryFailure{State: stateFailed, Reason: ae.ErrorMessage, Error: ae}
	case errors.Is(err, errBytesScannedCutoff):
		return &queryFailure{State: stateCancelled, Reason: bytesCutoffReason,
			Error: &AthenaError{ErrorCategory: errorCategoryUser, ErrorType: errorTypeRejected, ErrorMessage: bytesCutoffReason}}
	case errors.Is(err, errTrinoUnreachable):
		r.engine.invalidate(endpoint)
	}
	return failure(errorCategorySystem, errorTypeEngineInternal, fmt.Sprintf("The query engine failed: %v", err))
}

// trinoRows passes a query's rows on as each page brings them, and counts
// what it passed.
type trinoRows struct {
	out rowSink
	// header is whether the column names are still to be written first.
	header bool
	cols   []trinoColumn
	// count and bytes are the rows passed on, without the header, and the
	// total length of their values.
	count, bytes int64
}

// page passes on one page's rows. A statement that changed rows has one row,
// the count, which Athena reports as UpdateCount instead.
func (t *trinoRows) page(p *trinoResponse) error {
	if len(t.cols) == 0 {
		t.cols = p.Columns
	}
	if p.UpdateType != "" {
		return nil
	}
	for _, values := range p.Data {
		if err := t.writeHeader(); err != nil {
			return err
		}
		row := formatRow(values, t.cols)
		t.count++
		t.bytes += textLength(row)
		if err := t.out.write(row); err != nil {
			return err
		}
	}
	return nil
}

// writeHeader writes the column names, once, when the result has them.
func (t *trinoRows) writeHeader() error {
	if !t.header || len(t.cols) == 0 {
		return nil
	}
	t.header = false
	names := make([]string, len(t.cols))
	for i, c := range t.cols {
		names[i] = c.Name
	}
	return t.out.write(textRow(names...))
}

// finish shapes a finished query's result the way Athena returns it: a
// statement that changed rows reports how many as UpdateCount and has no
// rows; a SELECT has its header first, even with no rows.
func (t *trinoRows) finish(final *trinoResponse) (*queryResult, error) {
	st := final.Stats
	res := &queryResult{Columns: []ColumnInfo{}, Runtime: QueryRuntimeStatisticsRows{InputBytes: st.PhysicalInputBytes, InputRows: st.ProcessedRows}}
	if final.UpdateType != "" {
		res.UpdateCount = final.UpdateCount
		res.Runtime.OutputBytes = st.PhysicalWrittenBytes
		if final.UpdateCount != nil {
			res.Runtime.OutputRows = *final.UpdateCount
		}
		return res, nil
	}
	if err := t.writeHeader(); err != nil {
		return nil, err
	}
	res.Runtime.OutputRows, res.Runtime.OutputBytes = t.count, t.bytes
	for _, c := range t.cols {
		res.Columns = append(res.Columns, columnInfo(c))
	}
	return res, nil
}

// formatRow renders one row of values as text.
func formatRow(row []any, cols []trinoColumn) []*string {
	out := make([]*string, len(row))
	for i, v := range row {
		var sig trinoTypeSignature
		if i < len(cols) {
			sig = cols[i].TypeSignature
		}
		out[i] = formatValue(v, sig)
	}
	return out
}
