// Package stepfunctions_test — RedriveExecution and execution listing.
//
// Run: go test ./tests/integration/stepfunctions/...
package stepfunctions_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/overcast-sh/overcast/tests/helpers"
)

// redriveDefinition reads a DynamoDB table that the test creates only after
// the first run has failed on it.
func redriveDefinition() string {
	return `{
	  "StartAt": "Before",
	  "States": {
	    "Before": {"Type": "Pass", "Result": "ran", "ResultPath": "$.before", "Next": "Read"},
	    "Read": {"Type": "Task", "Resource": "arn:aws:states:::dynamodb:getItem",
	      "Parameters": {"TableName": "gate", "Key": {"id": {"S": "g"}}}, "ResultSelector": {"ok.$": "$.Item.ok.BOOL"}, "ResultPath": "$.gate", "Next": "Done"},
	    "Done": {"Type": "Succeed"}
	  }
	}`
}

func openGate(t *testing.T, srv *helpers.TestServer) {
	t.Helper()
	createTable(t, srv, "gate")
	resp := awsJSONCall(t, srv, "DynamoDB_20120810.PutItem", map[string]any{
		"TableName": "gate", "Item": map[string]any{"id": map[string]string{"S": "g"}, "ok": map[string]bool{"BOOL": true}},
	})
	resp.Body.Close()
}

func TestRedriveExecution_resumesAtTheFailedState(t *testing.T) {
	// Given: an execution that failed because the table it reads was missing
	srv := helpers.NewTestServer(t)
	got, execARN := runToEnd(t, srv, "redrive-sm", redriveDefinition(), `{"order":1}`)
	if got.Status != "FAILED" || got.Error != "DynamoDB.ResourceNotFoundException" {
		t.Fatalf("first run: status=%q error=%q", got.Status, got.Error)
	}
	described := sfnOK(t, srv, "DescribeExecution", map[string]any{"executionArn": execARN})
	if described["redriveStatus"] != "REDRIVABLE" {
		t.Errorf("redriveStatus = %v, want REDRIVABLE", described["redriveStatus"])
	}

	// When: the table appears and the execution is redriven
	openGate(t, srv)
	redriven := sfnOK(t, srv, "RedriveExecution", map[string]any{"executionArn": execARN})
	if redriven["redriveDate"] == nil {
		t.Errorf("RedriveExecution = %v, want a redriveDate", redriven)
	}

	// Then: it succeeds without re-running the states that had succeeded
	got = waitForTerminal(t, srv, execARN)
	if got.Status != "SUCCEEDED" {
		t.Fatalf("after redrive: status=%q (%s: %s)", got.Status, got.Error, got.Cause)
	}
	events := rawHistory(t, srv, execARN)
	redrivenEvents := findEvents(events, "ExecutionRedriven")
	if len(redrivenEvents) != 1 || redrivenEvents[0].detail("executionRedrivenEventDetails")["redriveCount"] != float64(1) {
		t.Fatalf("ExecutionRedriven = %v; types %v", redrivenEvents, rawTypes(events))
	}
	befores := 0
	for _, e := range findEvents(events, "PassStateEntered") {
		if e.stateName() == "Before" {
			befores++
		}
	}
	if befores != 1 {
		t.Errorf("Before ran %d times, want 1", befores)
	}
	described = sfnOK(t, srv, "DescribeExecution", map[string]any{"executionArn": execARN})
	if described["redriveCount"] != float64(1) || described["redriveStatus"] != "NOT_REDRIVABLE" {
		t.Errorf("DescribeExecution after redrive = %v", described)
	}
	listed := sfnOK(t, srv, "ListExecutions", map[string]any{"stateMachineArn": described["stateMachineArn"], "redriveFilter": "REDRIVEN"})
	if execs, _ := listed["executions"].([]any); len(execs) != 1 {
		t.Errorf("ListExecutions(REDRIVEN) = %v", listed)
	}
}

func TestRedriveExecution_succeededExecutionIsNotRedrivable(t *testing.T) {
	// Given: a succeeded execution
	srv := helpers.NewTestServer(t)
	_, execARN := runToEnd(t, srv, "redrive-ok", `{"StartAt":"P","States":{"P":{"Type":"Pass","End":true}}}`, `{}`)

	// When: we try to redrive it
	resp := sfnCall(t, srv, "RedriveExecution", map[string]any{"executionArn": execARN})
	defer resp.Body.Close()

	// Then: ExecutionNotRedrivable
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	if body := helpers.ReadBody(t, resp); !strings.Contains(body, "ExecutionNotRedrivable") {
		t.Errorf("body = %s", body)
	}
}

func TestListExecutions_paginates(t *testing.T) {
	// Given: three finished executions
	srv := helpers.NewTestServer(t)
	smARN := createSM(t, srv, "paged", `{"StartAt":"P","States":{"P":{"Type":"Pass","End":true}}}`)
	for i := 0; i < 3; i++ {
		waitForTerminal(t, srv, startExec(t, srv, smARN, `{}`))
	}

	// When: we page through them two at a time
	first := sfnOK(t, srv, "ListExecutions", map[string]any{"stateMachineArn": smARN, "maxResults": 2})
	token, _ := first["nextToken"].(string)
	second := sfnOK(t, srv, "ListExecutions", map[string]any{"stateMachineArn": smARN, "maxResults": 2, "nextToken": token})

	// Then: two pages cover all three with no token on the last
	a, _ := first["executions"].([]any)
	b, _ := second["executions"].([]any)
	if len(a) != 2 || token == "" || len(b) != 1 || second["nextToken"] != nil {
		t.Errorf("pages = %d (token %q), %d (token %v)", len(a), token, len(b), second["nextToken"])
	}
}

func TestGetExecutionHistory_paginates(t *testing.T) {
	// Given: an execution with six history events
	srv := helpers.NewTestServer(t)
	_, execARN := runToEnd(t, srv, "hist-paged", `{"StartAt":"P","States":{"P":{"Type":"Pass","Next":"Q"},"Q":{"Type":"Succeed"}}}`, `{}`)

	// When: we read it three events at a time
	first := sfnOK(t, srv, "GetExecutionHistory", map[string]any{"executionArn": execARN, "maxResults": 3})
	token, _ := first["nextToken"].(string)
	second := sfnOK(t, srv, "GetExecutionHistory", map[string]any{"executionArn": execARN, "maxResults": 3, "nextToken": token})

	// Then: the second page continues where the first stopped
	a, _ := first["events"].([]any)
	b, _ := second["events"].([]any)
	if len(a) != 3 || token == "" || len(b) != 3 {
		t.Fatalf("pages = %d (token %q), %d", len(a), token, len(b))
	}
	if id, _ := b[0].(map[string]any)["id"].(float64); id != 4 {
		t.Errorf("second page starts at id %v, want 4", id)
	}
}
