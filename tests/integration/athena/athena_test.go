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
		"QueryString": query,
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

// ─── Unknown-resource error shapes (issue #66 / tracker #9) ──────────────────
//
// AWS ground truth, established against the pinned Athena Smithy model
// (models/athena/service/2017-05-18/athena-2017-05-18.json) and the API
// reference before writing these tests:
//
//   - GetQueryExecution, GetQueryResults, GetWorkGroup and DeleteWorkGroup all
//     declare only InvalidRequestException (plus InternalServerException, and
//     for GetQueryResults, TooManyRequestsException) for an unknown
//     identifier — see each operation's "errors" list in the model.
//     InvalidRequestException carries no @httpError trait, and the API
//     reference is explicit about the default that implies: "HTTP Status
//     Code: 400" (confirmed on
//     https://docs.aws.amazon.com/athena/latest/APIReference/API_GetQueryExecution.html#API_GetQueryExecution_Errors
//     and the GetQueryResults equivalent). DynamoDB's own ResourceNotFoundException
//     (internal/services/dynamodb/store.go's errTableNotFound) already follows
//     this awsJson convention of answering "not found" with 400, not 404.
//   - Overcast's Athena handlers (typed_logic.go and service.go) answer all
//     four of these with HTTPStatus: http.StatusNotFound (404) instead — the
//     Code value ("InvalidRequestException") and Message are otherwise
//     correct. This is a confirmed divergence, reported separately; per this
//     review's brief, a test must not pin the wrong status as if it were
//     correct, so HTTP status is deliberately not asserted below.
//   - StopQueryExecution is not implemented at all: it has no entry in
//     Service.ops (service.go), so any call — regardless of QueryExecutionId
//     — falls through to protocol.NotImplementedJSON (501). AWS models the
//     same InvalidRequestException/400 pair for this operation's unknown-id
//     case, so today's 501/NotImplemented response is itself the compat gap
//     for this operation, not a narrower not-found-shape bug. See this
//     package's test report for the full finding.

func TestGetQueryExecution_unknownId(t *testing.T) {
	// Given: an empty store (no query with this ID was ever started)
	srv := helpers.NewTestServer(t)

	// When: GetQueryExecution is called with an unknown QueryExecutionId
	resp := athenaCall(t, srv, "GetQueryExecution", map[string]any{
		"QueryExecutionId": "does-not-exist",
	})
	defer resp.Body.Close()

	// Then: the JSON error names InvalidRequestException and the offending ID
	// (HTTP status intentionally not asserted — see package comment above:
	// AWS documents 400, Overcast answers 404)
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

	// Then: the JSON error names InvalidRequestException (status not
	// asserted — see package comment above)
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

	// Then: the JSON error names InvalidRequestException (status not
	// asserted — see package comment above)
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

	// Then: the JSON error names InvalidRequestException (status not
	// asserted — see package comment above)
	helpers.AssertRequestID(t, resp)
	assertAthenaJSONError(t, resp, "InvalidRequestException", "")
}

// TestStopQueryExecution_unimplemented documents today's actual behavior for
// an operation the AWS model declares (with InvalidRequestException/400 for
// an unknown id) but that Overcast has not implemented at all. See the
// package comment above for the AWS-side evidence and why this is reported
// as a bigger gap than the not-found-shape divergence the other tests here
// cover.
func TestStopQueryExecution_unimplemented(t *testing.T) {
	// Given: an empty store
	srv := helpers.NewTestServer(t)

	// When: StopQueryExecution is called at all (the QueryExecutionId does
	// not matter — the operation has no handler registered)
	resp := athenaCall(t, srv, "StopQueryExecution", map[string]any{
		"QueryExecutionId": "does-not-exist",
	})
	defer resp.Body.Close()

	// Then: Overcast's generic unimplemented-operation fallback answers,
	// not the AWS-modeled InvalidRequestException
	helpers.AssertStatus(t, resp, http.StatusNotImplemented)
	helpers.AssertHeader(t, resp, "x-emulator-unsupported", "true")
	helpers.AssertJSONError(t, resp, "NotImplemented")
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
