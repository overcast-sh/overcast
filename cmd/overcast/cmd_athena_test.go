package main

import (
	"bytes"
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/athena/types"
	"github.com/spf13/cobra"
	"go.uber.org/zap"

	"github.com/overcast-sh/overcast/internal/athenaquery"
	"github.com/overcast-sh/overcast/internal/clock"
	"github.com/overcast-sh/overcast/internal/config"
	"github.com/overcast-sh/overcast/internal/router"
	athenasvc "github.com/overcast-sh/overcast/internal/services/athena"
	"github.com/overcast-sh/overcast/internal/state"
)

// newDaemon serves the real router, on the inert Athena engine, keeping S3
// object bodies in a temporary data directory.
func newDaemon(t *testing.T) string {
	t.Helper()
	cfg := &config.Config{Host: "127.0.0.1", Region: "us-east-1", AccountID: "000000000000", DataDir: t.TempDir(),
		State: config.StateBackendMemory, LogLevel: "error", AthenaEngine: config.AthenaEngineInert}
	handler, preShutdown, cleanup, _ := router.New(cfg, state.NewMemoryStore(), zap.NewNop(), clock.New())
	t.Cleanup(func() {
		preShutdown()
		cleanup(context.Background())
	})
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return srv.URL
}

// runCLI runs the overcast command tree with args and returns stdout,
// stderr and the error.
func runCLI(t *testing.T, stdin string, args ...string) (string, string, error) {
	t.Helper()
	root := &cobra.Command{Use: "overcast", SilenceUsage: true, SilenceErrors: true}
	root.PersistentFlags().String("endpoint", "http://localhost:4566", "")
	root.AddCommand(newAthenaCmd(), newSamplesCmd())
	var out, errOut bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errOut)
	root.SetIn(strings.NewReader(stdin))
	root.SetArgs(args)
	err := root.ExecuteContext(t.Context())
	return out.String(), errOut.String(), err
}

// sampleResults is a result location in the sample dataset's bucket.
const sampleResults = "s3://overcast-sample-analytics/results/"

// newDaemonWithSamples is newDaemon with the analytics dataset loaded.
func newDaemonWithSamples(t *testing.T) string {
	t.Helper()
	endpoint := newDaemon(t)
	if _, _, err := runCLI(t, "", "samples", "load", "analytics", "--endpoint", endpoint); err != nil {
		t.Fatalf("samples load: %v", err)
	}
	return endpoint
}

func TestAthenaQueryCmd_printsTheResultInEachFormat(t *testing.T) {
	// Given: a daemon with the sample dataset
	endpoint := newDaemonWithSamples(t)
	query := []string{"athena", "query", "SHOW TABLES", "--database", "sample_analytics",
		"--output-location", sampleResults, "--endpoint", endpoint}

	// When/Then: each format prints the two tables
	for format, want := range map[string]string{
		"table": "tab_name\norders_csv\norders_parquet\n(2 rows)\n",
		"csv":   "tab_name\norders_csv\norders_parquet\n",
		"json":  "[\n  {\n    \"tab_name\": \"orders_csv\"\n  },\n  {\n    \"tab_name\": \"orders_parquet\"\n  }\n]\n",
	} {
		out, _, err := runCLI(t, "", append(query, "--output", format)...)
		if err != nil || out != want {
			t.Errorf("--output %s = %q, %v; want %q", format, out, err, want)
		}
	}
}

func TestAthenaQueryCmd_readsSQLFromStdin(t *testing.T) {
	endpoint := newDaemonWithSamples(t)

	out, _, err := runCLI(t, "SHOW DATABASES\n", "athena", "query", "-", "--output", "csv",
		"--output-location", sampleResults, "--endpoint", endpoint)

	if err != nil || !strings.Contains(out, "sample_analytics") {
		t.Fatalf("out = %q, err = %v", out, err)
	}
}

func TestAthenaQueryCmd_failedQueryIsAnError(t *testing.T) {
	endpoint := newDaemonWithSamples(t)

	out, _, err := runCLI(t, "", "athena", "query", "SHOW TABLES IN no_such_database",
		"--output-location", sampleResults, "--endpoint", endpoint)

	if err == nil || !strings.Contains(err.Error(), "query FAILED") || !strings.Contains(err.Error(), "no_such_database") || out != "" {
		t.Fatalf("out = %q, err = %v; want a FAILED error and no output", out, err)
	}
}

func TestAthenaQueryCmd_unknownOutputFormat(t *testing.T) {
	_, _, err := runCLI(t, "", "athena", "query", "SELECT 1", "--output", "yaml")
	if err == nil || !strings.Contains(err.Error(), "table, json or csv") {
		t.Fatalf("err = %v", err)
	}
}

func TestSamplesLoadCmd_reportsWhatItLoaded(t *testing.T) {
	endpoint := newDaemon(t)

	out, progress, err := runCLI(t, "", "samples", "load", "analytics", "--endpoint", endpoint)

	if err != nil {
		t.Fatalf("samples load: %v", err)
	}
	for _, want := range []string{"sample_analytics.orders_csv", "CSV", "sample_analytics.orders_parquet", "No Iceberg copy: the Athena engine is off"} {
		if !strings.Contains(out, want) {
			t.Errorf("output %q does not mention %q", out, want)
		}
	}
	if !strings.Contains(progress, "samples: creating bucket overcast-sample-analytics") {
		t.Errorf("progress %q", progress)
	}
}

func TestEngineProgress_reportsTheStartUpOnce(t *testing.T) {
	// Given: an engine that pulls, starts, then is ready, polled twice in
	// each state
	states := []athenasvc.EngineStatus{
		{State: athenasvc.EnginePulling, Image: "trino:483@sha256:15ff23b9"}, {State: athenasvc.EnginePulling},
		{State: athenasvc.EngineStarting}, {State: athenasvc.EngineStarting},
		{State: athenasvc.EngineReady, PullMillis: 12340, StartMillis: 8060},
	}
	var out bytes.Buffer
	i := 0
	p := &engineProgress{out: &out, status: func(context.Context) (athenasvc.EngineStatus, error) {
		st := states[min(i, len(states)-1)]
		i++
		return st, nil
	}}
	queued := types.QueryExecution{Status: &types.QueryExecutionStatus{State: types.QueryExecutionStateQueued}}
	running := types.QueryExecution{Status: &types.QueryExecutionStatus{State: types.QueryExecutionStateRunning}}

	// When: the query waits, then runs
	for range 4 {
		p.poll(context.Background(), queued)
	}
	p.poll(context.Background(), running)
	p.poll(context.Background(), running)

	// Then: each step is reported once, ending with the timings
	want := "athena: starting the query engine: pulling trino:483\n" +
		"athena: starting the query engine: waiting for it to answer\n" +
		"athena: query engine ready (pull 12.3s, start 8.1s)\n"
	if out.String() != want {
		t.Fatalf("progress = %q, want %q", out.String(), want)
	}
}

func TestEngineProgress_silentWhenTheEngineIsAlreadyUp(t *testing.T) {
	var out bytes.Buffer
	p := &engineProgress{out: &out, status: func(context.Context) (athenasvc.EngineStatus, error) {
		return athenasvc.EngineStatus{State: athenasvc.EngineReady}, nil
	}}
	p.poll(context.Background(), types.QueryExecution{Status: &types.QueryExecutionStatus{State: types.QueryExecutionStateQueued}})
	if out.Len() != 0 {
		t.Fatalf("progress = %q, want nothing", out.String())
	}
}

func TestWriteResultTable_nullsAndUpdateCounts(t *testing.T) {
	v := "a"
	var out bytes.Buffer
	res := &athenaquery.Result{Columns: []athenaquery.Column{{Name: "x"}, {Name: "y"}}, Rows: [][]*string{{&v, nil}}}
	if err := writeResultTable(&out, res); err != nil || out.String() != "x  y\na  NULL\n(1 row)\n" {
		t.Fatalf("table = %q, %v", out.String(), err)
	}

	out.Reset()
	n := int64(3)
	if err := writeResultTable(&out, &athenaquery.Result{UpdateCount: &n}); err != nil || out.String() != "3 rows affected\n" {
		t.Fatalf("update count = %q, %v", out.String(), err)
	}
}
