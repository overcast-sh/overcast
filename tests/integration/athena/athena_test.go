// Package athena_test contains integration tests for the Amazon Athena emulator.
//
// Run: go test ./tests/integration/athena/...
package athena_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/overcast-sh/overcast/tests/helpers"
)

// athenaCall performs an Athena JSON 1.1 dispatch request.
func athenaCall(t *testing.T, srv *helpers.TestServer, operation string, body map[string]any) *http.Response {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal %s body: %v", operation, err)
	}
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/x-amz-json-1.1")
	req.Header.Set("X-Amz-Target", "AmazonAthena."+operation)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("athenaCall %s: %v", operation, err)
	}
	return resp
}

func startQuery(t *testing.T, srv *helpers.TestServer, query string) string {
	t.Helper()
	resp := athenaCall(t, srv, "StartQueryExecution", map[string]any{
		"QueryString":         query,
		"ResultConfiguration": map[string]any{"OutputLocation": "s3://athena-results/"},
	})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		QueryExecutionId string `json:"QueryExecutionId"`
	}
	helpers.DecodeJSON(t, resp, &result)
	if result.QueryExecutionId == "" {
		t.Fatal("expected QueryExecutionId to be set")
	}
	return result.QueryExecutionId
}

// ─── StartQueryExecution ──────────────────────────────────────────────────────

func TestStartQueryExecution_success(t *testing.T) {
	// Given: an empty store
	srv := helpers.NewTestServer(t)

	// When: StartQueryExecution is called
	qid := startQuery(t, srv, "SELECT 1")

	// Then: a QueryExecutionId is returned
	if qid == "" {
		t.Error("expected QueryExecutionId to be non-empty")
	}
}

// ─── GetQueryExecution ────────────────────────────────────────────────────────

func TestGetQueryExecution_success(t *testing.T) {
	// Given: a query has been started
	srv := helpers.NewTestServer(t)
	qid := startQuery(t, srv, "SELECT 1")

	// When: GetQueryExecution is called
	resp := athenaCall(t, srv, "GetQueryExecution", map[string]any{
		"QueryExecutionId": qid,
	})
	defer resp.Body.Close()

	// Then: 200 with State "SUCCEEDED"
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		QueryExecution struct {
			Status struct {
				State string `json:"State"`
			} `json:"Status"`
		} `json:"QueryExecution"`
	}
	helpers.DecodeJSON(t, resp, &result)
	if result.QueryExecution.Status.State != "SUCCEEDED" {
		t.Errorf("expected State=SUCCEEDED, got %q", result.QueryExecution.Status.State)
	}
}

// ─── GetQueryResults ──────────────────────────────────────────────────────────

func TestGetQueryResults_success(t *testing.T) {
	// Given: a query has been started
	srv := helpers.NewTestServer(t)
	qid := startQuery(t, srv, "SELECT 1")

	// When: GetQueryResults is called
	resp := athenaCall(t, srv, "GetQueryResults", map[string]any{
		"QueryExecutionId": qid,
	})
	defer resp.Body.Close()

	// Then: 200 with ResultSet
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		ResultSet struct {
			Rows []any `json:"Rows"`
		} `json:"ResultSet"`
	}
	helpers.DecodeJSON(t, resp, &result)
}

// ─── CreateWorkGroup ──────────────────────────────────────────────────────────

func TestCreateWorkGroup_success(t *testing.T) {
	// Given: an empty store
	srv := helpers.NewTestServer(t)

	// When: CreateWorkGroup is called
	resp := athenaCall(t, srv, "CreateWorkGroup", map[string]any{
		"Name": "test-wg",
	})
	defer resp.Body.Close()

	// Then: 200 OK
	helpers.AssertStatus(t, resp, http.StatusOK)
}

// ─── Unknown-resource error shapes (issue #66, status fixed in #2009) ────────
//
// AWS ground truth, established against the pinned Athena Smithy model
// (models/athena/service/2017-05-18/athena-2017-05-18.json) and the API
// reference before writing these tests:
//
//   - GetQueryExecution, GetQueryResults, GetWorkGroup, DeleteWorkGroup and
//     StopQueryExecution all declare only InvalidRequestException (plus
//     InternalServerException, and for GetQueryResults,
//     TooManyRequestsException) for an unknown identifier — see each
//     operation's "errors" list in the model. InvalidRequestException carries
//     smithy.api#error: "client" and no @httpError trait, so the awsJson1_1
//     default applies, and the API reference is explicit about it: "HTTP
//     Status Code: 400" (confirmed on
//     https://docs.aws.amazon.com/athena/latest/APIReference/API_GetQueryExecution.html#API_GetQueryExecution_Errors
//     and the GetQueryResults equivalent). Kinesis's errNoSuchStream and
//     Firehose's errStreamNotFound answer 400 for the same reason.
//   - InvalidRequestException also models an optional AthenaErrorCode member,
//     documented as "the error code returned when the query execution failed
//     to process". AWS documents no value for it on an unknown-identifier
//     request, so Overcast neither emits nor asserts it here.

func TestGetQueryExecution_unknownId(t *testing.T) {
	// Given: an empty store (no query with this ID was ever started)
	srv := helpers.NewTestServer(t)

	// When: GetQueryExecution is called with an unknown QueryExecutionId
	resp := athenaCall(t, srv, "GetQueryExecution", map[string]any{
		"QueryExecutionId": "does-not-exist",
	})
	defer resp.Body.Close()

	// Then: HTTP 400 and a JSON error naming InvalidRequestException and the
	// offending ID
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertRequestID(t, resp)
	assertAthenaJSONError(t, resp, "InvalidRequestException", "does-not-exist")
}

func TestGetQueryResults_unknownId(t *testing.T) {
	// Given: an empty store
	srv := helpers.NewTestServer(t)

	// When: GetQueryResults is called with an unknown QueryExecutionId
	resp := athenaCall(t, srv, "GetQueryResults", map[string]any{
		"QueryExecutionId": "does-not-exist",
	})
	defer resp.Body.Close()

	// Then: HTTP 400 and a JSON error naming InvalidRequestException
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertRequestID(t, resp)
	assertAthenaJSONError(t, resp, "InvalidRequestException", "")
}

func TestGetWorkGroup_unknownName(t *testing.T) {
	// Given: no workgroup named "does-not-exist"
	srv := helpers.NewTestServer(t)

	// When: GetWorkGroup is called with an unknown name
	resp := athenaCall(t, srv, "GetWorkGroup", map[string]any{
		"WorkGroup": "does-not-exist",
	})
	defer resp.Body.Close()

	// Then: HTTP 400 and a JSON error naming InvalidRequestException
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertRequestID(t, resp)
	assertAthenaJSONError(t, resp, "InvalidRequestException", "does-not-exist")
}

func TestDeleteWorkGroup_unknownName(t *testing.T) {
	// Given: no workgroup named "does-not-exist"
	srv := helpers.NewTestServer(t)

	// When: DeleteWorkGroup is called with an unknown name
	resp := athenaCall(t, srv, "DeleteWorkGroup", map[string]any{
		"WorkGroup": "does-not-exist",
	})
	defer resp.Body.Close()

	// Then: HTTP 400 and a JSON error naming InvalidRequestException
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertRequestID(t, resp)
	assertAthenaJSONError(t, resp, "InvalidRequestException", "")
}

// ─── StopQueryExecution ───────────────────────────────────────────────────────

// TestStopQueryExecution_alreadyFinished covers the only query state Overcast
// can reach: StartQueryExecution completes synchronously, so a query is
// already SUCCEEDED by the time anyone can stop it.
//
// The pinned model gives StopQueryExecution an empty StopQueryExecutionOutput,
// only InternalServerException and InvalidRequestException for errors — no
// "wrong state" exception — and marks the operation smithy.api#idempotent, so
// a repeat call on a query that is already in a terminal state must answer the
// same way rather than failing. A terminal query's state is AWS's to keep:
// StopQueryExecution is documented as interrupting execution, and a SUCCEEDED
// query has none left to interrupt, so nothing about it changes.
func TestStopQueryExecution_alreadyFinished(t *testing.T) {
	// Given: a query that has been started (and so has already SUCCEEDED)
	srv := helpers.NewTestServer(t)
	qid := startQuery(t, srv, "SELECT 1")

	// When: StopQueryExecution is called for it, twice
	resp := athenaCall(t, srv, "StopQueryExecution", map[string]any{
		"QueryExecutionId": qid,
	})
	defer resp.Body.Close()

	// Then: 200 with an empty document, per StopQueryExecutionOutput
	helpers.AssertStatus(t, resp, http.StatusOK)
	if body := helpers.ReadBody(t, resp); strings.TrimSpace(body) != "{}" {
		t.Errorf("expected an empty StopQueryExecutionOutput document, got: %s", body)
	}

	// And: the repeat call is accepted too (the operation is @idempotent)
	again := athenaCall(t, srv, "StopQueryExecution", map[string]any{
		"QueryExecutionId": qid,
	})
	defer again.Body.Close()
	helpers.AssertStatus(t, again, http.StatusOK)

	// And: the finished query keeps the state and completion time it had
	get := athenaCall(t, srv, "GetQueryExecution", map[string]any{
		"QueryExecutionId": qid,
	})
	defer get.Body.Close()
	helpers.AssertStatus(t, get, http.StatusOK)
	var result struct {
		QueryExecution struct {
			Status struct {
				State              string  `json:"State"`
				CompletionDateTime float64 `json:"CompletionDateTime"`
			} `json:"Status"`
		} `json:"QueryExecution"`
	}
	helpers.DecodeJSON(t, get, &result)
	if result.QueryExecution.Status.State != "SUCCEEDED" {
		t.Errorf("expected a stopped-but-already-finished query to stay SUCCEEDED, got %q", result.QueryExecution.Status.State)
	}
	if result.QueryExecution.Status.CompletionDateTime == 0 {
		t.Error("expected CompletionDateTime to remain set after StopQueryExecution")
	}
}

func TestStopQueryExecution_unknownId(t *testing.T) {
	// Given: an empty store (no query with this ID was ever started)
	srv := helpers.NewTestServer(t)

	// When: StopQueryExecution is called with an unknown QueryExecutionId
	resp := athenaCall(t, srv, "StopQueryExecution", map[string]any{
		"QueryExecutionId": "does-not-exist",
	})
	defer resp.Body.Close()

	// Then: HTTP 400 and a JSON error naming InvalidRequestException and the
	// offending ID — the only client error the operation models
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertRequestID(t, resp)
	assertAthenaJSONError(t, resp, "InvalidRequestException", "does-not-exist")
}

// ─── GetQueryResults response shape ───────────────────────────────────────────

// TestGetQueryResults_succeededQuery_matchesAWSResponseShape checks the
// response against API_GetQueryResults.html's documented Response Syntax:
// a top-level ResultSet containing ResultSetMetadata.ColumnInfo and Rows
// (both arrays; empty is fine since Overcast queries always complete with no
// rows). UpdateCount is deliberately not asserted: it is an optional member
// with no default (see the GetQueryResultsOutput shape in the pinned model)
// documented as populated only for CREATE TABLE AS SELECT/INSERT INTO/UPDATE
// statements, so its absence for a plain query is consistent with the model,
// not a divergence.
func TestGetQueryResults_succeededQuery_matchesAWSResponseShape(t *testing.T) {
	// Given: a query that has run to completion
	srv := helpers.NewTestServer(t)
	qid := startQuery(t, srv, "SELECT 1")

	// When: GetQueryResults is called
	resp := athenaCall(t, srv, "GetQueryResults", map[string]any{
		"QueryExecutionId": qid,
	})
	defer resp.Body.Close()

	// Then: ResultSet.ResultSetMetadata.ColumnInfo and ResultSet.Rows are
	// both present as arrays
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		ResultSet struct {
			Rows              []any `json:"Rows"`
			ResultSetMetadata struct {
				ColumnInfo []any `json:"ColumnInfo"`
			} `json:"ResultSetMetadata"`
		} `json:"ResultSet"`
	}
	helpers.DecodeJSON(t, resp, &result)
	if result.ResultSet.Rows == nil {
		t.Error("expected ResultSet.Rows to be present (an array, empty is fine)")
	}
	if result.ResultSet.ResultSetMetadata.ColumnInfo == nil {
		t.Error("expected ResultSet.ResultSetMetadata.ColumnInfo to be present (an array, empty is fine)")
	}
}

// ─── ListWorkGroups ───────────────────────────────────────────────────────────

func TestListWorkGroups_success(t *testing.T) {
	// Given: a work group exists
	srv := helpers.NewTestServer(t)
	cr := athenaCall(t, srv, "CreateWorkGroup", map[string]any{
		"Name": "test-wg",
	})
	defer cr.Body.Close()
	helpers.AssertStatus(t, cr, http.StatusOK)

	// When: ListWorkGroups is called
	resp := athenaCall(t, srv, "ListWorkGroups", map[string]any{})
	defer resp.Body.Close()

	// Then: 200 with at least 1 work group
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		WorkGroups []struct {
			Name string `json:"Name"`
		} `json:"WorkGroups"`
	}
	helpers.DecodeJSON(t, resp, &result)
	if len(result.WorkGroups) < 1 {
		t.Error("expected at least 1 work group")
	}
}

// ─── Test helpers ──────────────────────────────────────────────────────────

// assertAthenaJSONError reads resp's body once and checks the JSON error's
// __type against wantCode, failing if wantContains is non-empty and absent
// from the message. It exists so a single test can check both the error code
// and a message substring without reading resp.Body twice
// (helpers.AssertJSONError and helpers.ReadBody each consume the body
// independently).
func assertAthenaJSONError(t *testing.T, resp *http.Response, wantCode, wantContains string) {
	t.Helper()
	body := helpers.ReadBody(t, resp)
	var errResp struct {
		Type    string `json:"__type"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal([]byte(body), &errResp); err != nil {
		t.Fatalf("failed to decode JSON error: %v\nbody: %s", err, body)
	}
	if errResp.Type != wantCode {
		t.Errorf("expected error code %q, got %q (message: %s)", wantCode, errResp.Type, errResp.Message)
	}
	if wantContains != "" && !strings.Contains(body, wantContains) {
		t.Errorf("expected error message to contain %q, got: %s", wantContains, body)
	}
}
