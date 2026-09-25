package athena

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"go.uber.org/zap"

	"github.com/overcast-sh/overcast/internal/clock"
	"github.com/overcast-sh/overcast/internal/config"
	"github.com/overcast-sh/overcast/internal/docker"
	"github.com/overcast-sh/overcast/internal/serviceutil"
)

// readyEngine is an engine manager whose engine is already up at endpoint,
// so a runner can be tested without starting one.
func readyEngine(t *testing.T, endpoint string) *engineManager {
	t.Helper()
	m := newEngineManager(&config.Config{}, serviceutil.NewServiceLogger(zap.NewNop(), serviceName), clock.New(), nil)
	t.Cleanup(m.bgCancel)
	done := make(chan struct{})
	close(done)
	m.docker = docker.NewClient("tcp://127.0.0.1:1", zap.NewNop())
	m.boot = &engineBoot{done: done, endpoint: endpoint}
	return m
}

// collectedRows is a rowSink that keeps every row.
type collectedRows [][]*string

func (c *collectedRows) write(row []*string) error {
	*c = append(*c, row)
	return nil
}

// refusingRows is a rowSink that takes after rows and refuses the next.
type refusingRows struct{ after int }

func (r *refusingRows) write([]*string) error {
	if r.after == 0 {
		return &resultError{resultTooLarge(1)}
	}
	r.after--
	return nil
}

func testRunner(m *engineManager, sql string) trinoRunner {
	return trinoRunner{engine: m, sql: sql, session: trinoSession{Catalog: hiveCatalog, Schema: "demo"}, header: true, clk: clock.New()}
}

func TestTrinoRunner_selectHasItsHeaderFirst(t *testing.T) {
	// Given: an engine that answers SELECT 1
	f := newFakeTrino(t)
	r := testRunner(readyEngine(t, f.srv.URL), "SELECT 1")

	// When: it runs
	ran := false
	var rows collectedRows
	res, fail := r.run(context.Background(), func() { ran = true }, &rows)

	// Then: RUNNING was reported, and the rows are the header, then the row
	if fail != nil || !ran {
		t.Fatalf("fail = %+v, running reported = %v", fail, ran)
	}
	if len(rows) != 2 || *rows[0][0] != "_col0" || *rows[1][0] != "1" || len(res.Rows) != 0 || res.Columns[0].Type != "integer" {
		t.Fatalf("rows = %v, result = %+v", rows, res)
	}
	if res.Runtime.InputBytes != 11 || res.Runtime.InputRows != 1 || res.Runtime.OutputRows != 1 ||
		res.timing.PlanningMillis != 7 || res.timing.QueueMillis < 5 {
		t.Fatalf("statistics = %+v / %+v", res.Runtime, res.timing)
	}
	if s := f.sessions[0]; s.Catalog != "awsdatacatalog" || s.Schema != "demo" {
		t.Fatalf("session = %+v", s)
	}
}

func TestTrinoRunner_dmlReportsItsUpdateCount(t *testing.T) {
	f := newFakeTrino(t)
	count := int64(2)
	f.script("INSERT INTO t VALUES (1), (2)", trinoResponse{UpdateType: "INSERT", UpdateCount: &count,
		Columns: []trinoColumn{{Name: "rows", Type: "bigint"}}, Data: [][]any{{json.Number("2")}},
		Stats: trinoStats{State: "FINISHED", PhysicalWrittenBytes: 316}})

	var rows collectedRows
	res, fail := testRunner(readyEngine(t, f.srv.URL), "INSERT INTO t VALUES (1), (2)").run(context.Background(), func() {}, &rows)

	if fail != nil || res.UpdateCount == nil || *res.UpdateCount != 2 || len(rows) != 0 || len(res.Columns) != 0 ||
		res.Runtime.OutputRows != 2 || res.Runtime.OutputBytes != 316 {
		t.Fatalf("result = %+v, fail = %+v", res, fail)
	}
}

func TestTrinoRunner_engineFailureIsMapped(t *testing.T) {
	f := newFakeTrino(t)
	f.script("SELECT * FROM nope", trinoResponse{Error: &trinoError{Message: "line 1:15: Table 'awsdatacatalog.demo.nope' does not exist",
		ErrorName: "TABLE_NOT_FOUND", ErrorType: "USER_ERROR"}})

	_, fail := testRunner(readyEngine(t, f.srv.URL), "SELECT * FROM nope").run(context.Background(), func() {}, &collectedRows{})

	if fail == nil || fail.State != stateFailed || fail.Error.ErrorType != errorTypeNotFound ||
		fail.Reason != "TABLE_NOT_FOUND: line 1:15: Table 'awsdatacatalog.demo.nope' does not exist" {
		t.Fatalf("fail = %+v / %+v", fail, fail.Error)
	}
}

func TestTrinoRunner_bytesScannedCutoffCancels(t *testing.T) {
	// Given: a workgroup cutoff below what the query reads by its second page
	f := newFakeTrino(t)
	f.script("SELECT big", trinoResponse{Stats: trinoStats{State: "RUNNING", PhysicalInputBytes: 10}},
		trinoResponse{Stats: trinoStats{State: "RUNNING", PhysicalInputBytes: 5000}},
		trinoResponse{Stats: trinoStats{State: "FINISHED", PhysicalInputBytes: 9000}})
	r := testRunner(readyEngine(t, f.srv.URL), "SELECT big")
	r.cutoff = 1000

	// When: it runs
	_, fail := r.run(context.Background(), func() {}, &collectedRows{})

	// Then: it was cancelled on the engine, and ends CANCELLED as Athena
	// ends a query that exceeds the cutoff
	if fail == nil || fail.State != stateCancelled || fail.Reason != bytesCutoffReason {
		t.Fatalf("fail = %+v", fail)
	}
	if len(f.deletes()) != 1 {
		t.Fatalf("DELETEs = %v, want the query cancelled", f.deletes())
	}
}

func TestTrinoRunner_unreachableEngineIsForgotten(t *testing.T) {
	// Given: an engine that has gone away
	f := newFakeTrino(t)
	m := readyEngine(t, f.srv.URL)
	m.gc = docker.NewGC(m.docker, zap.NewNop(), true, nil)
	f.srv.Close()

	// When: a query runs on it
	_, fail := testRunner(m, "SELECT 1").run(context.Background(), func() {}, &collectedRows{})

	// Then: it fails as a system error and the next query starts a new engine
	if fail == nil || fail.Error.ErrorCategory != errorCategorySystem {
		t.Fatalf("fail = %+v", fail)
	}
	if m.boot != nil {
		t.Fatal("the dead engine is still the one queries are sent to")
	}
}

// varcharPage is a page of one varchar column, n, holding values.
func varcharPage(state string, values ...string) trinoResponse {
	data := make([][]any, len(values))
	for i, v := range values {
		data[i] = []any{v}
	}
	return trinoResponse{Columns: []trinoColumn{{Name: "n", Type: "varchar", TypeSignature: trinoTypeSignature{RawType: "varchar"}}},
		Data: data, Stats: trinoStats{State: state}}
}

func TestTrinoRunner_rowsArePassedOnPageByPage(t *testing.T) {
	// Given: a SELECT whose rows arrive over two pages
	f := newFakeTrino(t)
	f.script("SELECT n", varcharPage("RUNNING", "a", "b"), varcharPage("FINISHED", "c"))

	// When: it runs
	var rows collectedRows
	res, fail := testRunner(readyEngine(t, f.srv.URL), "SELECT n").run(context.Background(), func() {}, &rows)

	// Then: the sink got the header once, then every row in order, and the
	// result counts the rows without the header
	var got []string
	for _, row := range rows {
		got = append(got, *row[0])
	}
	if fail != nil || strings.Join(got, ",") != "n,a,b,c" || res.Runtime.OutputRows != 3 || res.Runtime.OutputBytes != 3 {
		t.Fatalf("rows = %v, result = %+v, fail = %+v", got, res, fail)
	}
}

func TestTrinoRunner_emptySelectStillHasItsHeader(t *testing.T) {
	f := newFakeTrino(t)
	f.script("SELECT n WHERE false", varcharPage("FINISHED"))

	var rows collectedRows
	_, fail := testRunner(readyEngine(t, f.srv.URL), "SELECT n WHERE false").run(context.Background(), func() {}, &rows)

	if fail != nil || len(rows) != 1 || *rows[0][0] != "n" {
		t.Fatalf("rows = %v, fail = %+v", rows, fail)
	}
}

func TestTrinoRunner_aRefusedRowStopsTheQuery(t *testing.T) {
	// Given: a result that takes the header and one row, and no more
	f := newFakeTrino(t)
	f.script("SELECT n", varcharPage("RUNNING", "a"), varcharPage("RUNNING", "b"), varcharPage("FINISHED", "c"))

	// When: the query runs
	_, fail := testRunner(readyEngine(t, f.srv.URL), "SELECT n").run(context.Background(), func() {}, &refusingRows{after: 2})

	// Then: it fails as the sink said, and is cancelled on the engine
	if fail == nil || fail.Reason != resultTooLarge(1).Reason {
		t.Fatalf("fail = %+v", fail)
	}
	if len(f.deletes()) != 1 {
		t.Fatalf("DELETEs = %v, want the query cancelled", f.deletes())
	}
}
