package main

// cmd_status_test.go — pins `overcast status` to the daemon's real health
// route. The command once probed a bare /health, a path the router never
// registers, so every request fell through to the AWS dispatch fallback and
// the command reported an error against a perfectly healthy daemon. Building
// the actual router here keeps both ends of that contract in one test: if
// either the command's URL or the router's route moves, this fails.

import (
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"go.uber.org/zap"

	"github.com/overcast-sh/overcast/internal/clock"
	"github.com/overcast-sh/overcast/internal/config"
	"github.com/overcast-sh/overcast/internal/router"
	"github.com/overcast-sh/overcast/internal/state"
)

// runStatusCmd executes `overcast status` against endpoint and returns its
// stdout. The endpoint flag is registered locally because in production it is
// a persistent flag on the root command (see main.go).
func runStatusCmd(t *testing.T, endpoint string) (string, error) {
	t.Helper()
	cmd := newStatusCmd()
	cmd.Flags().String("endpoint", endpoint, "")
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(nil)
	err := cmd.Execute()
	return out.String(), err
}

func TestStatusCmd_againstRealRouter(t *testing.T) {
	// Given: the real router, exactly as `overcast serve` builds it
	cfg := &config.Config{
		Host:      "127.0.0.1",
		Port:      0,
		Region:    "us-east-1",
		AccountID: "000000000000",
		State:     config.StateBackendMemory,
		LogLevel:  "error",
		Version:   "1.2.3-test",
	}
	handler, preShutdown, cleanup, _ := router.New(cfg, state.NewMemoryStore(), zap.NewNop(), clock.New())
	t.Cleanup(func() {
		preShutdown()
		cleanup(t.Context())
	})
	srv := httptest.NewServer(handler)
	defer srv.Close()

	// When: `overcast status` runs against it
	out, err := runStatusCmd(t, srv.URL)

	// Then: it reports the daemon healthy, in one line, enriched with the
	// version and storage backend the health endpoint already reports, then
	// the Athena engine's line.
	if err != nil {
		t.Fatalf("status against a healthy daemon: %v (output: %q)", err, out)
	}
	if !strings.Contains(out, "overcast OK at "+srv.URL) {
		t.Errorf("output %q does not report OK at %s", out, srv.URL)
	}
	if !strings.Contains(out, "1.2.3-test") {
		t.Errorf("output %q does not include the daemon version", out)
	}
	if !strings.Contains(out, "memory") {
		t.Errorf("output %q does not include the storage backend", out)
	}
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 2 || lines[1] != "athena engine: off, queries run inert" {
		t.Errorf("output = %q, want the status line then the engine's", lines)
	}
}

// TestStatusCmd_reportsARunningAthenaEngine covers the engine line for an
// engine that is up: its memory and how long it has been up.
func TestStatusCmd_reportsARunningAthenaEngine(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"status":"ok","athenaEngine":{"engine":"trino","state":"ready","memoryBytes":2147483648,"uptimeMillis":192400,"runningQueries":1}}`))
	}))
	defer srv.Close()

	out, err := runStatusCmd(t, srv.URL)

	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if !strings.Contains(out, "\nathena engine: ready, memory 2.0 GiB, up 3m12s, 1 query running\n") {
		t.Errorf("output %q does not report the running engine", out)
	}
}

// TestStatusCmd_probesHealthRoute asserts the exact path the command requests,
// so a wrong URL is diagnosed as such rather than as a generic non-200 from
// the full router's fallback.
func TestStatusCmd_probesHealthRoute(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"ok","version":"9.9.9","storage":{"default":"wal"}}`))
	}))
	defer srv.Close()

	out, err := runStatusCmd(t, srv.URL+"/") // trailing slash must not double up
	if err != nil {
		t.Fatalf("status: %v (output: %q)", err, out)
	}
	if gotPath != "/_overcast/health" {
		t.Errorf("status requested %q, want /_overcast/health", gotPath)
	}
}

// TestStatusCmd_non200 keeps the failure path honest: a daemon answering the
// probe with anything but 200 is reported as an error, not swallowed.
func TestStatusCmd_non200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotImplemented)
	}))
	defer srv.Close()

	if _, err := runStatusCmd(t, srv.URL); err == nil {
		t.Fatal("status against a non-200 daemon returned nil error")
	}
}

// TestStatusCmd_plainBody covers a health body the command cannot enrich from
// (older daemon, proxy in the way): a 200 still reports OK, without details.
func TestStatusCmd_plainBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("ok"))
	}))
	defer srv.Close()

	out, err := runStatusCmd(t, srv.URL)
	if err != nil {
		t.Fatalf("status: %v (output: %q)", err, out)
	}
	if !strings.Contains(out, "overcast OK at "+srv.URL) {
		t.Errorf("output %q does not report OK at %s", out, srv.URL)
	}
}

// TestStatusCmd_InstanceTable_Empty pins the exact original one-line
// behavior for a registry that has nothing in it — the case
// TestStatusCmd_againstRealRouter etc. above already implicitly rely on
// (they don't seam instancesBaseDir at all, so they only stay one-liners
// because a fresh checkout's real ~/.overcast/instances doesn't exist).
// This test makes that guarantee explicit and hermetic.
func TestStatusCmd_InstanceTable_Empty(t *testing.T) {
	withTestInstancesDir(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"status":"ok"}`))
	}))
	defer srv.Close()

	out, err := runStatusCmd(t, srv.URL)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if n := strings.Count(strings.TrimRight(out, "\n"), "\n"); n != 0 {
		t.Errorf("output is %d lines with an empty registry, want a one-liner:\n%s", n+1, out)
	}
}

// TestStatusCmd_InstanceTable_ListsRegisteredInstances covers the actual
// listing: every registered instance appears, backend and endpoint verbatim,
// with a running/stopped state derived from its own liveness check.
func TestStatusCmd_InstanceTable_ListsRegisteredInstances(t *testing.T) {
	withTestInstancesDir(t)
	// This test process's own pid, so the record reads as running.
	if err := saveInstance(instanceRecord{Name: "running-one", Backend: "native", PID: os.Getpid(), Endpoint: "http://127.0.0.1:4566"}); err != nil {
		t.Fatalf("saveInstance: %v", err)
	}
	if err := saveInstance(instanceRecord{Name: "stopped-one", Backend: "native", PID: exitedPID(t), Endpoint: "http://127.0.0.1:4570"}); err != nil {
		t.Fatalf("saveInstance: %v", err)
	}

	prevDockerRun := dockerRun
	dockerRun = func(args ...string) (string, error) { return "false", nil }
	t.Cleanup(func() { dockerRun = prevDockerRun })
	if err := saveInstance(instanceRecord{Name: "docker-one", Backend: "docker", ContainerID: "cid123", Endpoint: "http://127.0.0.1:4580"}); err != nil {
		t.Fatalf("saveInstance: %v", err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"status":"ok"}`))
	}))
	defer srv.Close()

	out, err := runStatusCmd(t, srv.URL)
	if err != nil {
		t.Fatalf("status: %v", err)
	}

	for _, want := range []string{
		"running-one", "native", "http://127.0.0.1:4566", "running",
		"stopped-one", "http://127.0.0.1:4570", "stopped",
		"docker-one", "docker", "http://127.0.0.1:4580",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("status output %q does not contain %q", out, want)
		}
	}
}

// TestStatusCmd_InstanceTable_DockerInspectFailureReportsUnknown covers the
// three-valued state a docker instance can be in: a liveness check that
// itself fails to run (docker missing, daemon down, container long gone)
// must show as "unknown", not silently as "stopped".
func TestStatusCmd_InstanceTable_DockerInspectFailureReportsUnknown(t *testing.T) {
	withTestInstancesDir(t)
	prevDockerRun := dockerRun
	dockerRun = func(args ...string) (string, error) { return "", errors.New("no such container") }
	t.Cleanup(func() { dockerRun = prevDockerRun })
	if err := saveInstance(instanceRecord{Name: "gone", Backend: "docker", ContainerID: "cid123", Endpoint: "http://127.0.0.1:4580"}); err != nil {
		t.Fatalf("saveInstance: %v", err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"status":"ok"}`))
	}))
	defer srv.Close()

	out, err := runStatusCmd(t, srv.URL)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if !strings.Contains(out, "unknown") {
		t.Errorf("status output %q does not report the docker instance's state as unknown", out)
	}
}
