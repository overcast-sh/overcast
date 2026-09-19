// Package stepfunctions_test — execution-engine (ASL interpreter) integration tests.
//
// Run: go test ./tests/integration/stepfunctions/...
package stepfunctions_test

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/overcast-sh/overcast/tests/helpers"
)

// ─── Helpers ──────────────────────────────────────────────────────────────────

// createSM creates a state machine and returns its ARN.
func createSM(t *testing.T, srv *helpers.TestServer, name, definition string) string {
	t.Helper()
	resp := sfnCall(t, srv, "CreateStateMachine", map[string]any{
		"name":       name,
		"definition": definition,
		"roleArn":    "arn:aws:iam::000000000000:role/compat",
	})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	var out struct {
		StateMachineArn string `json:"stateMachineArn"`
	}
	helpers.DecodeJSON(t, resp, &out)
	if out.StateMachineArn == "" {
		t.Fatal("CreateStateMachine: empty stateMachineArn")
	}
	return out.StateMachineArn
}

// startExec starts an execution and returns its ARN.
func startExec(t *testing.T, srv *helpers.TestServer, smARN, input string) string {
	t.Helper()
	body := map[string]any{"stateMachineArn": smARN}
	if input != "" {
		body["input"] = input
	}
	resp := sfnCall(t, srv, "StartExecution", body)
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	var out struct {
		ExecutionArn string `json:"executionArn"`
	}
	helpers.DecodeJSON(t, resp, &out)
	if out.ExecutionArn == "" {
		t.Fatal("StartExecution: empty executionArn")
	}
	return out.ExecutionArn
}

// waitForTerminal polls DescribeExecution until the execution leaves RUNNING.
//
// StartExecution accepts and returns while the execution is still running, as
// AWS does, so a test that asserts on the outcome has to wait for it. Polling
// the real API is deliberate: it exercises the same RUNNING → terminal
// transition an application would observe.
func waitForTerminal(t *testing.T, srv *helpers.TestServer, execARN string) describeExecutionResult {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		got := describeExec(t, srv, execARN)
		if got.Status != "RUNNING" {
			return got
		}
		if time.Now().After(deadline) {
			t.Fatalf("execution %s was still RUNNING after 20s", execARN)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

type describeExecutionResult struct {
	ExecutionArn string `json:"executionArn"`
	Status       string `json:"status"`
	Input        string `json:"input"`
	Output       string `json:"output"`
	Error        string `json:"error"`
	Cause        string `json:"cause"`
	Name         string `json:"name"`
}

func describeExec(t *testing.T, srv *helpers.TestServer, execARN string) describeExecutionResult {
	t.Helper()
	resp := sfnCall(t, srv, "DescribeExecution", map[string]any{"executionArn": execARN})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	var out describeExecutionResult
	helpers.DecodeJSON(t, resp, &out)
	return out
}

type historyEvent struct {
	ID              int64  `json:"id"`
	PreviousEventID int64  `json:"previousEventId"`
	Type            string `json:"type"`

	StateEnteredEventDetails *struct {
		Name  string `json:"name"`
		Input string `json:"input"`
	} `json:"stateEnteredEventDetails"`
	StateExitedEventDetails *struct {
		Name   string `json:"name"`
		Output string `json:"output"`
	} `json:"stateExitedEventDetails"`
	ExecutionSucceededEventDetails *struct {
		Output string `json:"output"`
	} `json:"executionSucceededEventDetails"`
	ExecutionFailedEventDetails *struct {
		Error string `json:"error"`
		Cause string `json:"cause"`
	} `json:"executionFailedEventDetails"`
	TaskFailedEventDetails *struct {
		ResourceType string `json:"resourceType"`
		Resource     string `json:"resource"`
		Error        string `json:"error"`
		Cause        string `json:"cause"`
	} `json:"taskFailedEventDetails"`
}

func execHistory(t *testing.T, srv *helpers.TestServer, execARN string) []historyEvent {
	t.Helper()
	return historyPages[historyEvent](t, srv, execARN)
}

func eventTypes(events []historyEvent) []string {
	types := make([]string, 0, len(events))
	for _, e := range events {
		types = append(types, e.Type)
	}
	return types
}

// ─── Pass / Succeed — the interpreter actually runs ───────────────────────────

func TestStartExecution_passStateProducesOutput(t *testing.T) {
	// Given: a state machine whose Pass state rewrites the input
	srv := helpers.NewTestServer(t)
	def := `{
	  "StartAt": "Hello",
	  "States": {
	    "Hello": {
	      "Type": "Pass",
	      "Result": {"greeting": "hello"},
	      "ResultPath": "$.msg",
	      "End": true
	    }
	  }
	}`
	smARN := createSM(t, srv, "pass-sm", def)

	// When: we start an execution with an input document
	execARN := startExec(t, srv, smARN, `{"name":"world"}`)

	// Then: DescribeExecution reports the interpreted output, not a bare SUCCEEDED
	got := waitForTerminal(t, srv, execARN)
	if got.Status != "SUCCEEDED" {
		t.Fatalf("status = %q, want SUCCEEDED (error=%q cause=%q)", got.Status, got.Error, got.Cause)
	}
	var output map[string]any
	if err := json.Unmarshal([]byte(got.Output), &output); err != nil {
		t.Fatalf("unmarshal output %q: %v", got.Output, err)
	}
	msg, ok := output["msg"].(map[string]any)
	if !ok || msg["greeting"] != "hello" {
		t.Fatalf("output = %s, want $.msg.greeting = hello", got.Output)
	}
	if output["name"] != "world" {
		t.Fatalf("output = %s, want the original $.name preserved", got.Output)
	}
}

func TestGetExecutionHistory_passStateEmitsAWSEventVocabulary(t *testing.T) {
	// Given: a single-Pass state machine that has run
	srv := helpers.NewTestServer(t)
	smARN := createSM(t, srv, "hist-sm", `{"StartAt":"P","States":{"P":{"Type":"Pass","End":true}}}`)
	execARN := startExec(t, srv, smARN, `{}`)

	// When: we read the execution history once the run is done
	waitForTerminal(t, srv, execARN)
	events := execHistory(t, srv, execARN)

	// Then: it is the real AWS event vocabulary, in order, with linked IDs
	want := []string{"ExecutionStarted", "PassStateEntered", "PassStateExited", "ExecutionSucceeded"}
	got := eventTypes(events)
	if len(got) != len(want) {
		t.Fatalf("event types = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("event types = %v, want %v", got, want)
		}
	}
	for i, e := range events {
		if e.ID != int64(i+1) {
			t.Errorf("event[%d].id = %d, want %d", i, e.ID, i+1)
		}
		wantPrev := int64(i)
		if e.PreviousEventID != wantPrev {
			t.Errorf("event[%d].previousEventId = %d, want %d", i, e.PreviousEventID, wantPrev)
		}
	}
	if events[1].StateEnteredEventDetails == nil || events[1].StateEnteredEventDetails.Name != "P" {
		t.Errorf("PassStateEntered missing stateEnteredEventDetails.name = P")
	}
	if events[2].StateExitedEventDetails == nil || events[2].StateExitedEventDetails.Name != "P" {
		t.Errorf("PassStateExited missing stateExitedEventDetails.name = P")
	}
}

// ─── Choice ───────────────────────────────────────────────────────────────────

func TestStartExecution_choiceStateBranches(t *testing.T) {
	// Given: a Choice state routing on a numeric comparison
	srv := helpers.NewTestServer(t)
	def := `{
	  "StartAt": "Route",
	  "States": {
	    "Route": {
	      "Type": "Choice",
	      "Choices": [{"Variable": "$.n", "NumericGreaterThan": 10, "Next": "Big"}],
	      "Default": "Small"
	    },
	    "Big":   {"Type": "Pass", "Result": "big",   "End": true},
	    "Small": {"Type": "Pass", "Result": "small", "End": true}
	  }
	}`
	smARN := createSM(t, srv, "choice-sm", def)

	// When/Then: each input takes its branch
	for _, tc := range []struct{ input, want string }{
		{`{"n": 42}`, `"big"`},
		{`{"n": 1}`, `"small"`},
	} {
		execARN := startExec(t, srv, smARN, tc.input)
		got := waitForTerminal(t, srv, execARN)
		if got.Status != "SUCCEEDED" {
			t.Fatalf("input %s: status = %q (error=%q cause=%q)", tc.input, got.Status, got.Error, got.Cause)
		}
		if got.Output != tc.want {
			t.Errorf("input %s: output = %s, want %s", tc.input, got.Output, tc.want)
		}
	}
}

// ─── Fail ─────────────────────────────────────────────────────────────────────

func TestStartExecution_failStateFailsExecution(t *testing.T) {
	// Given: a state machine that fails
	srv := helpers.NewTestServer(t)
	def := `{"StartAt":"Boom","States":{"Boom":{"Type":"Fail","Error":"MyError","Cause":"it broke"}}}`
	smARN := createSM(t, srv, "fail-sm", def)

	// When: we run it
	execARN := startExec(t, srv, smARN, `{}`)

	// Then: the execution is FAILED with the declared error/cause
	got := waitForTerminal(t, srv, execARN)
	if got.Status != "FAILED" {
		t.Fatalf("status = %q, want FAILED", got.Status)
	}
	if got.Error != "MyError" || got.Cause != "it broke" {
		t.Errorf("error/cause = %q/%q, want MyError/it broke", got.Error, got.Cause)
	}
	types := eventTypes(execHistory(t, srv, execARN))
	if len(types) == 0 || types[len(types)-1] != "ExecutionFailed" {
		t.Errorf("history = %v, want it to end with ExecutionFailed", types)
	}
}

// ─── The §2.1 fidelity rule: unsupported features fail loudly ─────────────────

func TestStartExecution_unsupportedResourceFailsLoudly(t *testing.T) {
	// Given: a Task against a service integration Overcast does not interpret
	srv := helpers.NewTestServer(t)
	def := `{
	  "StartAt": "T",
	  "States": {
	    "T": {
	      "Type": "Task",
	      "Resource": "arn:aws:states:::glue:startJobRun.sync",
	      "Parameters": {"JobName": "j"},
	      "End": true
	    }
	  }
	}`
	smARN := createSM(t, srv, "glue-sm", def)

	// When: we run it
	execARN := startExec(t, srv, smARN, `{}`)

	// Then: it fails loudly rather than skipping the state
	got := waitForTerminal(t, srv, execARN)
	if got.Status != "FAILED" {
		t.Fatalf("status = %q, want FAILED for an unsupported resource integration", got.Status)
	}
	if got.Error != "States.Runtime" {
		t.Errorf("error = %q, want States.Runtime", got.Error)
	}
}

// ─── Map / Parallel actually iterate ──────────────────────────────────────────

func TestStartExecution_mapStateIteratesItems(t *testing.T) {
	// Given: an inline Map over ItemsPath
	srv := helpers.NewTestServer(t)
	def := `{
	  "StartAt": "M",
	  "States": {
	    "M": {
	      "Type": "Map",
	      "ItemsPath": "$.items",
	      "ItemProcessor": {
	        "StartAt": "Double",
	        "States": {"Double": {"Type": "Pass", "Parameters": {"v.$": "$.v"}, "End": true}}
	      },
	      "End": true
	    }
	  }
	}`
	smARN := createSM(t, srv, "map-sm", def)

	// When: we run it over three items
	execARN := startExec(t, srv, smARN, `{"items":[{"v":1},{"v":2},{"v":3}]}`)

	// Then: the result is the per-item outputs, in order
	got := waitForTerminal(t, srv, execARN)
	if got.Status != "SUCCEEDED" {
		t.Fatalf("status = %q (error=%q cause=%q)", got.Status, got.Error, got.Cause)
	}
	var items []map[string]any
	if err := json.Unmarshal([]byte(got.Output), &items); err != nil {
		t.Fatalf("unmarshal output %q: %v", got.Output, err)
	}
	if len(items) != 3 {
		t.Fatalf("output = %s, want 3 items", got.Output)
	}
	for i, want := range []float64{1, 2, 3} {
		if items[i]["v"] != want {
			t.Errorf("item[%d].v = %v, want %v", i, items[i]["v"], want)
		}
	}
}

func TestStartExecution_parallelStateRunsBranches(t *testing.T) {
	// Given: a Parallel state with two branches
	srv := helpers.NewTestServer(t)
	def := `{
	  "StartAt": "P",
	  "States": {
	    "P": {
	      "Type": "Parallel",
	      "Branches": [
	        {"StartAt": "A", "States": {"A": {"Type": "Pass", "Result": "a", "End": true}}},
	        {"StartAt": "B", "States": {"B": {"Type": "Pass", "Result": "b", "End": true}}}
	      ],
	      "End": true
	    }
	  }
	}`
	smARN := createSM(t, srv, "parallel-sm", def)

	// When: we run it
	execARN := startExec(t, srv, smARN, `{}`)

	// Then: the output is the ordered array of branch outputs
	got := waitForTerminal(t, srv, execARN)
	if got.Status != "SUCCEEDED" {
		t.Fatalf("status = %q (error=%q cause=%q)", got.Status, got.Error, got.Cause)
	}
	if got.Output != `["a","b"]` {
		t.Errorf("output = %s, want [\"a\",\"b\"]", got.Output)
	}
}

// ─── Retry / Catch ────────────────────────────────────────────────────────────

func TestStartExecution_catchRoutesTaskFailure(t *testing.T) {
	// Given: a Task whose Lambda does not exist, with a Catch
	srv := helpers.NewTestServer(t)
	def := `{
	  "StartAt": "T",
	  "States": {
	    "T": {
	      "Type": "Task",
	      "Resource": "arn:aws:states:::lambda:invoke",
	      "Parameters": {"FunctionName": "does-not-exist"},
	      "Catch": [{"ErrorEquals": ["States.ALL"], "ResultPath": "$.err", "Next": "Handled"}],
	      "End": true
	    },
	    "Handled": {"Type": "Pass", "Result": "caught", "End": true}
	  }
	}`
	smARN := createSM(t, srv, "catch-sm", def)

	// When: we run it
	execARN := startExec(t, srv, smARN, `{}`)

	// Then: the Catch routed to the handler and the execution succeeded
	got := waitForTerminal(t, srv, execARN)
	if got.Status != "SUCCEEDED" {
		t.Fatalf("status = %q (error=%q cause=%q)", got.Status, got.Error, got.Cause)
	}
	if got.Output != `"caught"` {
		t.Errorf("output = %s, want \"caught\"", got.Output)
	}
	types := eventTypes(execHistory(t, srv, execARN))
	found := false
	for _, ty := range types {
		if ty == "TaskFailed" {
			found = true
		}
	}
	if !found {
		t.Errorf("history = %v, want a TaskFailed event before the catch", types)
	}
}

// ─── ListExecutions / StopExecution ───────────────────────────────────────────

func TestListExecutions_returnsFinishedExecutions(t *testing.T) {
	// Given: a state machine with two executions
	srv := helpers.NewTestServer(t)
	smARN := createSM(t, srv, "list-sm", `{"StartAt":"P","States":{"P":{"Type":"Pass","End":true}}}`)
	first := startExec(t, srv, smARN, `{}`)
	second := startExec(t, srv, smARN, `{}`)
	waitForTerminal(t, srv, first)
	waitForTerminal(t, srv, second)

	// When: we list them
	resp := sfnCall(t, srv, "ListExecutions", map[string]any{"stateMachineArn": smARN})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	var out struct {
		Executions []struct {
			ExecutionArn string `json:"executionArn"`
			Status       string `json:"status"`
		} `json:"executions"`
	}
	helpers.DecodeJSON(t, resp, &out)

	// Then: both are present and SUCCEEDED
	seen := map[string]string{}
	for _, e := range out.Executions {
		seen[e.ExecutionArn] = e.Status
	}
	if seen[first] != "SUCCEEDED" || seen[second] != "SUCCEEDED" {
		t.Fatalf("executions = %+v, want both %q and %q SUCCEEDED", out.Executions, first, second)
	}
}

// An unrecognised statusFilter must be refused, matching real Step Functions'
// ValidationException — one of ListExecutions' own declared errors — rather
// than silently matching nothing. See
// docs/plans/ec2-filter-rule-cross-service.md, finding 5, and #1188.
func TestListExecutions_unrecognisedStatusFilterIsRefused(t *testing.T) {
	// Given: a state machine with one finished execution
	srv := helpers.NewTestServer(t)
	smARN := createSM(t, srv, "bogus-status-sm", `{"StartAt":"P","States":{"P":{"Type":"Pass","End":true}}}`)
	startExec(t, srv, smARN, `{}`)

	// When: ListExecutions is called with a statusFilter outside the enum
	resp := sfnCall(t, srv, "ListExecutions", map[string]any{
		"stateMachineArn": smARN,
		"statusFilter":    "BOGUS_STATUS",
	})
	defer resp.Body.Close()

	// Then: ValidationException, not an empty (or full) page
	helpers.AssertJSONError(t, resp, "ValidationException")
}

// A recognised statusFilter value that no execution currently holds matches
// nothing — that direction is unchanged by this fix.
func TestListExecutions_recognisedStatusFilterThatMatchesNothingStaysEmpty(t *testing.T) {
	// Given: a state machine with one finished (SUCCEEDED) execution
	srv := helpers.NewTestServer(t)
	smARN := createSM(t, srv, "empty-status-sm", `{"StartAt":"P","States":{"P":{"Type":"Pass","End":true}}}`)
	exec := startExec(t, srv, smARN, `{}`)
	waitForTerminal(t, srv, exec)

	// When: ListExecutions filters for a real status nothing is in
	resp := sfnCall(t, srv, "ListExecutions", map[string]any{
		"stateMachineArn": smARN,
		"statusFilter":    "FAILED",
	})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	var out struct {
		Executions []struct {
			ExecutionArn string `json:"executionArn"`
		} `json:"executions"`
	}
	helpers.DecodeJSON(t, resp, &out)

	// Then: an empty page, not an error
	if len(out.Executions) != 0 {
		t.Fatalf("executions = %+v, want empty — a recognised status with no matches is not an error", out.Executions)
	}
}
