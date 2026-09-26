//go:build !slim

// The runtime MCP endpoint only exists in non-slim builds; see mcp_test.go.
package router_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"testing"

	"github.com/overcast-sh/overcast/internal/mcp"
	"github.com/overcast-sh/overcast/internal/samples"
	"github.com/overcast-sh/overcast/internal/sdkconfig"
	athenasvc "github.com/overcast-sh/overcast/internal/services/athena"
	s3tablessvc "github.com/overcast-sh/overcast/internal/services/s3tables"
	"github.com/overcast-sh/overcast/tests/helpers"
)

// callRuntimeTool calls a runtime MCP tool and returns its structured result,
// failing on a protocol error; a tool error comes back as isError.
func callRuntimeTool(t *testing.T, srv *helpers.TestServer, name string, args map[string]any) map[string]any {
	t.Helper()
	payload, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{"_meta": mcp.NewRequestMeta("router-test", "1.0"), "name": name, "arguments": args},
	})
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, srv.URL+"/_overcast/mcp/", bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("MCP-Protocol-Version", mcp.ProtocolVersion)
	req.Header.Set("Mcp-Method", "tools/call")
	req.Header.Set("Mcp-Name", name)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	var body map[string]any
	helpers.DecodeJSON(t, resp, &body)
	result, ok := body["result"].(map[string]any)
	if !ok {
		t.Fatalf("%s: no result (error: %v)", name, body["error"])
	}
	if isErr, _ := result["isError"].(bool); isErr {
		t.Fatalf("%s: tool error: %v", name, result["content"])
	}
	structured, ok := result["structuredContent"].(map[string]any)
	if !ok {
		t.Fatalf("%s: structuredContent = %T in %v", name, result["structuredContent"], result)
	}
	return structured
}

func loadSampleDataset(t *testing.T, srv *helpers.TestServer) {
	t.Helper()
	engine := func(context.Context) (athenasvc.EngineStatus, error) {
		return athenasvc.EngineStatus{State: athenasvc.EngineOff}, nil
	}
	if _, err := samples.NewLoader(sdkconfig.ForEndpoint(srv.URL, "us-east-1", http.DefaultClient), engine).Load(t.Context(), "analytics"); err != nil {
		t.Fatalf("load samples: %v", err)
	}
}

func TestRuntimeMCPGlueDescribeTable_reportsSchemaFormatAndPartitions(t *testing.T) {
	// Given: the sample dataset
	srv := helpers.NewTestServer(t)
	loadSampleDataset(t, srv)

	// When: an agent describes its partitioned CSV table
	got := callRuntimeTool(t, srv, "runtime_glue_describe_table", map[string]any{"database": "sample_analytics", "table": "orders_csv"})

	// Then: it gets the format, location, columns and partitions
	cols, _ := got["columns"].([]any)
	keys, _ := got["partition_keys"].([]any)
	if got["format"] != "CSV" || got["location"] != "s3://overcast-sample-analytics/csv/orders/" || got["partition_count"] != float64(3) ||
		len(cols) != 6 || len(keys) != 1 || keys[0].(map[string]any)["name"] != "region" {
		t.Fatalf("describe = %v", got)
	}
}

func TestRuntimeMCPAthenaRunQuery_returnsTheOutcome(t *testing.T) {
	// Given: the sample dataset, on the inert engine
	srv := helpers.NewTestServer(t)
	loadSampleDataset(t, srv)

	// When: an agent runs DDL through the tool
	got := callRuntimeTool(t, srv, "runtime_athena_run_query", map[string]any{
		"sql": "SHOW TABLES IN sample_analytics", "output_location": "s3://overcast-sample-analytics/results/",
	})

	// Then: it sees the query succeed, with its rows and statistics
	rows, _ := got["rows"].([]any)
	if got["state"] != "SUCCEEDED" || got["timed_out"] != false || len(rows) != 2 || got["statistics"] == nil {
		t.Fatalf("result = %v", got)
	}

	// When: the query fails
	failed := callRuntimeTool(t, srv, "runtime_athena_run_query", map[string]any{
		"sql": "SHOW TABLES IN no_such_database", "output_location": "s3://overcast-sample-analytics/results/",
	})

	// Then: the failure is the result, with Athena's error
	if failed["state"] != "FAILED" || failed["error"] == nil || failed["state_change_reason"] == "" {
		t.Fatalf("failed result = %v", failed)
	}
}

func TestRuntimeMCPS3TablesTableSnapshots_listsTheCommits(t *testing.T) {
	// Given: an S3 Tables table with one append committed through the
	// Iceberg REST catalog
	srv := helpers.NewTestServer(t)
	bucketARN := helpers.SeedS3Table(t, srv, "lake", "sales", "orders")
	catalog := srv.URL + s3tablessvc.IcebergInternalRoot + "/v1"
	var cfg struct {
		Overrides map[string]string `json:"overrides"`
	}
	icebergREST(t, http.MethodGet, catalog+"/config?warehouse="+url.QueryEscape(bucketARN), nil, &cfg)
	tableURL := catalog + "/" + cfg.Overrides["prefix"] + "/namespaces/sales/tables/orders"
	var loaded struct {
		Metadata struct {
			TableUUID string `json:"table-uuid"`
		} `json:"metadata"`
	}
	icebergREST(t, http.MethodGet, tableURL, nil, &loaded)
	icebergREST(t, http.MethodPost, tableURL, map[string]any{
		"requirements": []any{map[string]any{"type": "assert-table-uuid", "uuid": loaded.Metadata.TableUUID}},
		"updates": []any{
			map[string]any{"action": "add-snapshot", "snapshot": map[string]any{
				"snapshot-id": 7, "sequence-number": 1, "timestamp-ms": 1767225600000, "manifest-list": "s3://lake/snap-7.avro",
				"summary": map[string]string{"operation": "append", "added-records": "2"}, "schema-id": 0,
			}},
			map[string]any{"action": "set-snapshot-ref", "ref-name": "main", "snapshot-id": 7, "type": "branch"},
		},
	}, nil)

	// When: an agent lists its snapshots
	got := callRuntimeTool(t, srv, "runtime_s3tables_table_snapshots", map[string]any{
		"table_bucket_arn": bucketARN, "namespace": "sales", "name": "orders",
	})

	// Then: it sees the append, current
	snapshots, _ := got["snapshots"].([]any)
	if len(snapshots) != 1 || got["current_snapshot_id"] != float64(7) || got["metadata_location"] == "" {
		t.Fatalf("snapshots = %v", got)
	}
	sn := snapshots[0].(map[string]any)
	if sn["operation"] != "append" || sn["current"] != true || sn["timestamp"] != "2026-01-01T00:00:00Z" {
		t.Fatalf("snapshot = %v", sn)
	}
}

// icebergREST sends one request to the unsigned Iceberg REST catalog, which
// must succeed, decoding the answer into out when out is non-nil.
func icebergREST(t *testing.T, method, target string, body, out any) {
	t.Helper()
	payload, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(t.Context(), method, target, bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	if out != nil {
		helpers.DecodeJSON(t, resp, out)
	}
}
