package athena

import (
	"context"
	"encoding/json"
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

func testRunner(m *engineManager, sql string) trinoRunner {
	return trinoRunner{engine: m, sql: sql, session: trinoSession{Catalog: hiveCatalog, Schema: "demo"}, header: true, clk: clock.New()}
}

func TestTrinoRunner_selectHasItsHeaderFirst(t *testing.T) {
	// Given: an engine that answers SELECT 1
	f := newFakeTrino(t)
	r := testRunner(readyEngine(t, f.srv.URL), "SELECT 1")

	// When: it runs
	ran := false
	res, fail := r.run(context.Background(), func() { ran = true })

	// Then: RUNNING was reported, and the result is the header, then the row
	if fail != nil || !ran {
		t.Fatalf("fail = %+v, running reported = %v", fail, ran)
	}
	if len(res.Rows) != 2 || *res.Rows[0][0] != "_col0" || *res.Rows[1][0] != "1" || res.Columns[0].Type != "integer" {
		t.Fatalf("result = %+v", res)
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

	res, fail := testRunner(readyEngine(t, f.srv.URL), "INSERT INTO t VALUES (1), (2)").run(context.Background(), func() {})

	if fail != nil || res.UpdateCount == nil || *res.UpdateCount != 2 || len(res.Rows) != 0 || len(res.Columns) != 0 ||
		res.Runtime.OutputRows != 2 || res.Runtime.OutputBytes != 316 {
		t.Fatalf("result = %+v, fail = %+v", res, fail)
	}
}

func TestTrinoRunner_engineFailureIsMapped(t *testing.T) {
	f := newFakeTrino(t)
	f.script("SELECT * FROM nope", trinoResponse{Error: &trinoError{Message: "line 1:15: Table 'awsdatacatalog.demo.nope' does not exist",
		ErrorName: "TABLE_NOT_FOUND", ErrorType: "USER_ERROR"}})

	_, fail := testRunner(readyEngine(t, f.srv.URL), "SELECT * FROM nope").run(context.Background(), func() {})

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
	_, fail := r.run(context.Background(), func() {})

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
	_, fail := testRunner(m, "SELECT 1").run(context.Background(), func() {})

	// Then: it fails as a system error and the next query starts a new engine
	if fail == nil || fail.Error.ErrorCategory != errorCategorySystem {
		t.Fatalf("fail = %+v", fail)
	}
	if m.boot != nil {
		t.Fatal("the dead engine is still the one queries are sent to")
	}
}
