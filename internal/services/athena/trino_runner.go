package athena

import (
	"context"
	"errors"
	"fmt"

	"github.com/overcast-sh/overcast/internal/clock"
)

// trino_runner.go — one statement run on the engine: started when the
// engine is, followed page by page, and turned into Athena's result.

// errBytesScannedCutoff stops a query that has read past its workgroup's
// BytesScannedCutoffPerQuery.
var errBytesScannedCutoff = errors.New("athena: bytes scanned cutoff exceeded")

// bytesCutoffReason is the StateChangeReason of a query the cutoff stopped.
const bytesCutoffReason = "Query cancelled! : Bytes scanned limit was exceeded"

type trinoRunner struct {
	engine  *engineManager
	sql     string
	session trinoSession
	// header puts the column names first, as a SELECT's result has them.
	header bool
	// cutoff is the workgroup's BytesScannedCutoffPerQuery; 0 is none.
	cutoff int64
	clk    clock.Clock
}

func (trinoRunner) background() bool { return true }

func (r trinoRunner) run(ctx context.Context, running func()) (*queryResult, *queryFailure) {
	queued := r.clk.Now()
	endpoint, release, err := r.engine.acquire(ctx)
	if errors.Is(err, errEngineUnavailable) { // no Docker after all: the query runs inert
		return inertRunner{}.run(ctx, running)
	}
	if err != nil {
		return nil, failure(errorCategorySystem, errorTypeEngineInternal, err.Error())
	}
	defer release()
	booted := r.clk.Since(queued).Milliseconds()

	var cols []trinoColumn
	res := &queryResult{Columns: []ColumnInfo{}}
	final, err := r.engine.client.execute(ctx, endpoint, r.sql, r.session, func(page *trinoResponse) error {
		if cols == nil {
			running()
			cols = []trinoColumn{}
		}
		if len(cols) == 0 && len(page.Columns) > 0 {
			cols = page.Columns
		}
		for _, row := range page.Data {
			res.Rows = append(res.Rows, formatRow(row, cols))
		}
		if r.cutoff > 0 && page.Stats.PhysicalInputBytes > r.cutoff {
			return errBytesScannedCutoff
		}
		return nil
	})
	if fail := r.failure(endpoint, err); fail != nil {
		return nil, fail
	}
	r.finish(res, final, cols)
	res.timing = engineTiming{QueueMillis: booted + final.Stats.QueuedTimeMillis, PlanningMillis: final.Stats.PlanningTimeMillis}
	return res, nil
}

// failure is how a query that did not finish ended.
func (r trinoRunner) failure(endpoint string, err error) *queryFailure {
	var te *trinoError
	switch {
	case err == nil:
		return nil
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

// finish shapes a finished query's result the way Athena returns it: a
// statement that changed rows reports how many as UpdateCount and has no
// rows; a SELECT has its header first.
func (r trinoRunner) finish(res *queryResult, final *trinoResponse, cols []trinoColumn) {
	st := final.Stats
	res.Runtime = QueryRuntimeStatisticsRows{InputBytes: st.PhysicalInputBytes, InputRows: st.ProcessedRows}
	if final.UpdateType != "" {
		res.Rows = nil
		res.UpdateCount = final.UpdateCount
		res.Runtime.OutputBytes = st.PhysicalWrittenBytes
		if final.UpdateCount != nil {
			res.Runtime.OutputRows = *final.UpdateCount
		}
		return
	}
	res.Runtime.OutputRows = int64(len(res.Rows))
	for _, row := range res.Rows {
		for _, cell := range row {
			if cell != nil {
				res.Runtime.OutputBytes += int64(len(*cell))
			}
		}
	}
	for _, c := range cols {
		res.Columns = append(res.Columns, columnInfo(c))
	}
	if r.header && len(cols) > 0 {
		header := make([]string, len(cols))
		for i, c := range cols {
			header[i] = c.Name
		}
		res.Rows = append([][]*string{textRow(header...)}, res.Rows...)
	}
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
