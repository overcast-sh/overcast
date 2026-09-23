// Package glue_test contains integration tests for the AWS Glue emulator.
//
// Run: go test ./tests/integration/glue/...
package glue_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/overcast-sh/overcast/internal/protocol"
	"github.com/overcast-sh/overcast/tests/helpers"
)

// glueCall performs a Glue JSON 1.1 dispatch request.
func glueCall(t *testing.T, srv *helpers.TestServer, operation string, body map[string]any) *http.Response {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal %s body: %v", operation, err)
	}
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/x-amz-json-1.1")
	req.Header.Set("X-Amz-Target", "AWSGlue."+operation)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("glueCall %s: %v", operation, err)
	}
	return resp
}

func createDatabase(t *testing.T, srv *helpers.TestServer, name string) {
	t.Helper()
	resp := glueCall(t, srv, "CreateDatabase", map[string]any{
		"DatabaseInput": map[string]any{
			"Name": name,
		},
	})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
}

// ─── CreateDatabase ───────────────────────────────────────────────────────────

func TestCreateDatabase_success(t *testing.T) {
	// Given: an empty store
	srv := helpers.NewTestServer(t)

	// When: CreateDatabase is called
	resp := glueCall(t, srv, "CreateDatabase", map[string]any{
		"DatabaseInput": map[string]any{
			"Name": "testdb",
		},
	})
	defer resp.Body.Close()

	// Then: 200 OK
	helpers.AssertStatus(t, resp, http.StatusOK)
}

// ─── GetDatabase ──────────────────────────────────────────────────────────────

func TestGetDatabase_success(t *testing.T) {
	// Given: a database exists
	srv := helpers.NewTestServer(t)
	createDatabase(t, srv, "testdb")

	// When: GetDatabase is called
	resp := glueCall(t, srv, "GetDatabase", map[string]any{
		"Name": "testdb",
	})
	defer resp.Body.Close()

	// Then: 200 with database info
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		Database struct {
			Name string `json:"Name"`
		} `json:"Database"`
	}
	helpers.DecodeJSON(t, resp, &result)
	if result.Database.Name != "testdb" {
		t.Errorf("expected Name=testdb, got %q", result.Database.Name)
	}
}

// ─── GetDatabases ─────────────────────────────────────────────────────────────

func TestGetDatabases_success(t *testing.T) {
	// Given: a database exists
	srv := helpers.NewTestServer(t)
	createDatabase(t, srv, "testdb")

	// When: GetDatabases is called
	resp := glueCall(t, srv, "GetDatabases", map[string]any{})
	defer resp.Body.Close()

	// Then: 200 with at least 1 database
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		DatabaseList []struct {
			Name string `json:"Name"`
		} `json:"DatabaseList"`
	}
	helpers.DecodeJSON(t, resp, &result)
	if len(result.DatabaseList) < 1 {
		t.Error("expected at least 1 database")
	}
}

// ─── DeleteDatabase ───────────────────────────────────────────────────────────

func TestDeleteDatabase_success(t *testing.T) {
	// Given: a database exists
	srv := helpers.NewTestServer(t)
	createDatabase(t, srv, "testdb")

	// When: DeleteDatabase is called
	del := glueCall(t, srv, "DeleteDatabase", map[string]any{
		"Name": "testdb",
	})
	defer del.Body.Close()
	helpers.AssertStatus(t, del, http.StatusOK)

	// Then: GetDatabase answers EntityNotFoundException, which Glue sends
	// as a 400 like all its client errors
	resp := glueCall(t, srv, "GetDatabase", map[string]any{
		"Name": "testdb",
	})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
}

// ─── CreateTable ──────────────────────────────────────────────────────────────

func TestCreateTable_success(t *testing.T) {
	// Given: a database exists
	srv := helpers.NewTestServer(t)
	createDatabase(t, srv, "testdb")

	// When: CreateTable is called
	resp := glueCall(t, srv, "CreateTable", map[string]any{
		"DatabaseName": "testdb",
		"TableInput": map[string]any{
			"Name": "testtable",
		},
	})
	defer resp.Body.Close()

	// Then: 200 OK
	helpers.AssertStatus(t, resp, http.StatusOK)
}

// ─── GetTable ─────────────────────────────────────────────────────────────────

func TestGetTable_success(t *testing.T) {
	// Given: a table exists in a database
	srv := helpers.NewTestServer(t)
	createDatabase(t, srv, "testdb")
	cr := glueCall(t, srv, "CreateTable", map[string]any{
		"DatabaseName": "testdb",
		"TableInput": map[string]any{
			"Name": "testtable",
		},
	})
	defer cr.Body.Close()
	helpers.AssertStatus(t, cr, http.StatusOK)

	// When: GetTable is called
	resp := glueCall(t, srv, "GetTable", map[string]any{
		"DatabaseName": "testdb",
		"Name":         "testtable",
	})
	defer resp.Body.Close()

	// Then: 200 with table info
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		Table struct {
			Name string `json:"Name"`
		} `json:"Table"`
	}
	helpers.DecodeJSON(t, resp, &result)
	if result.Table.Name != "testtable" {
		t.Errorf("expected Name=testtable, got %q", result.Table.Name)
	}
}

// ─── CreateTable with OpenTableFormatInput ────────────────────────────────────

func TestCreateTable_icebergInputMarksTheMissingMetadataAsALimitation(t *testing.T) {
	// Given: a database
	srv := helpers.NewTestServer(t)
	createDatabase(t, srv, "testdb")

	// When: CreateTable asks Glue to write the table's Iceberg metadata
	resp := glueCall(t, srv, "CreateTable", map[string]any{
		"DatabaseName":         "testdb",
		"TableInput":           map[string]any{"Name": "ice"},
		"OpenTableFormatInput": map[string]any{"IcebergInput": map[string]any{"MetadataOperation": "CREATE"}},
	})
	defer resp.Body.Close()

	// Then: the table is created, and the response says what was not done
	helpers.AssertStatus(t, resp, http.StatusOK)
	if got := resp.Header.Get(protocol.EmulationLimitationHeader); !strings.Contains(got, "IcebergInput") {
		t.Errorf("%s = %q, want the Iceberg metadata limitation", protocol.EmulationLimitationHeader, got)
	}
}
