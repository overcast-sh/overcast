// Package stepfunctions_test — ASL error-handling integration tests: what
// Retry/Catch match, and what the interpreter refuses out loud rather than
// dropping.
//
// The fixtures (createSM, startExec, waitForTerminal, execHistory, eventTypes)
// live in execution_test.go alongside the rest of the execution-engine tests.
//
// Run: go test ./tests/integration/stepfunctions/...
package stepfunctions_test

import (
	"testing"

	"github.com/overcast-sh/overcast/tests/helpers"
)

// countEvents returns how many history events have the given type.
func countEvents(events []historyEvent, want string) int {
	n := 0
	for _, e := range events {
		if e.Type == want {
			n++
		}
	}
	return n
}

// ─── Retry / Catch: States.TaskFailed is a wildcard ───────────────────────────

func TestStartExecution_catchStatesTaskFailedRoutesTaskFailure(t *testing.T) {
	// Given: the same Task-against-a-missing-Lambda shape as the States.ALL
	// case in execution_test.go, but caught the way real state machines are
	// written. AWS treats States.TaskFailed as a wildcard over the error the
	// Task actually raised — here Lambda.ResourceNotFoundException.
	srv := helpers.NewTestServer(t)
	def := `{
	  "StartAt": "T",
	  "States": {
	    "T": {
	      "Type": "Task",
	      "Resource": "arn:aws:states:::lambda:invoke",
	      "Parameters": {"FunctionName": "does-not-exist"},
	      "Catch": [{"ErrorEquals": ["States.TaskFailed"], "ResultPath": "$.err", "Next": "Handled"}],
	      "End": true
	    },
	    "Handled": {"Type": "Pass", "Result": "caught", "End": true}
	  }
	}`
	smARN := createSM(t, srv, "catch-taskfailed-sm", def)

	// When: we run it
	execARN := startExec(t, srv, smARN, `{}`)

	// Then: the Catch fired, exactly as it does against real AWS
	got := waitForTerminal(t, srv, execARN)
	if got.Status != "SUCCEEDED" {
		t.Fatalf("status = %q (error=%q cause=%q), want SUCCEEDED — States.TaskFailed must catch a Task error",
			got.Status, got.Error, got.Cause)
	}
	if got.Output != `"caught"` {
		t.Errorf("output = %s, want \"caught\"", got.Output)
	}
}

func TestStartExecution_retryStatesTaskFailedRetriesTheTask(t *testing.T) {
	// Given: a failing Task with the Retry every AWS example opens with
	srv := helpers.NewTestServer(t)
	def := `{
	  "StartAt": "T",
	  "States": {
	    "T": {
	      "Type": "Task",
	      "Resource": "arn:aws:states:::lambda:invoke",
	      "Parameters": {"FunctionName": "does-not-exist"},
	      "Retry": [{"ErrorEquals": ["States.TaskFailed"], "IntervalSeconds": 0, "MaxAttempts": 2}],
	      "End": true
	    }
	  }
	}`
	smARN := createSM(t, srv, "retry-taskfailed-sm", def)

	// When: we run it
	execARN := startExec(t, srv, smARN, `{}`)

	// Then: it really retried — one attempt plus two retries — before failing
	got := waitForTerminal(t, srv, execARN)
	if got.Status != "FAILED" {
		t.Fatalf("status = %q, want FAILED once the retries are exhausted", got.Status)
	}
	events := execHistory(t, srv, execARN)
	if attempts := countEvents(events, "TaskFailed"); attempts != 3 {
		t.Errorf("TaskFailed events = %d, want 3 (1 attempt + 2 retries); history = %v", attempts, eventTypes(events))
	}
}

func TestStartExecution_catchStatesTaskFailedDoesNotCatchStatesRuntime(t *testing.T) {
	// Given: a Catch on States.TaskFailed over a feature Overcast cannot run
	srv := helpers.NewTestServer(t)
	def := `{
	  "StartAt": "T",
	  "States": {
	    "T": {
	      "Type": "Task",
	      "Resource": "arn:aws:states:::aws-sdk:iam:listRoles",
	      "Catch": [{"ErrorEquals": ["States.TaskFailed"], "Next": "Handled"}],
	      "End": true
	    },
	    "Handled": {"Type": "Pass", "Result": "caught", "End": true}
	  }
	}`
	smARN := createSM(t, srv, "runtime-not-caught-sm", def)

	// When: we run it
	execARN := startExec(t, srv, smARN, `{}`)

	// Then: the wildcard still does not swallow an Overcast gap
	got := waitForTerminal(t, srv, execARN)
	if got.Status != "FAILED" {
		t.Fatalf("status = %q, want FAILED — States.TaskFailed must not catch States.Runtime", got.Status)
	}
	if got.Error != "States.Runtime" {
		t.Errorf("error = %q, want States.Runtime", got.Error)
	}
}

// ─── Task TimeoutSeconds is enforced, not just recorded ───────────────────────

// slowChildTaskDefinition builds a parent whose single Task blocks on a child
// execution that waits far longer than the Task's declared TimeoutSeconds. A
// `.sync` child is the long-running Task an integration test can build without
// Docker, and it honours context cancellation the way a Lambda invoke does.
func slowChildTaskDefinition(childARN, catchBlock string) string {
	return `{
	  "StartAt": "T",
	  "States": {
	    "T": {
	      "Type": "Task",
	      "Resource": "arn:aws:states:::states:startExecution.sync",
	      "Parameters": {"StateMachineArn": "` + childARN + `"},
	      "TimeoutSeconds": 1,` + catchBlock + `
	      "End": true
	    },
	    "Fallback": {"Type": "Pass", "Result": "timed out", "End": true}
	  }
	}`
}

const slowChildDefinition = `{"StartAt":"W","States":{"W":{"Type":"Wait","Seconds":30,"Next":"D"},"D":{"Type":"Pass","End":true}}}`

func TestStartExecution_taskTimeoutSecondsRaisesStatesTimeout(t *testing.T) {
	// Given: a Task that runs far longer than the TimeoutSeconds it declares
	srv := helpers.NewTestServer(t)
	childARN := createSM(t, srv, "slow-child-sm", slowChildDefinition)
	smARN := createSM(t, srv, "task-timeout-sm", slowChildTaskDefinition(childARN, ""))

	// When: we run it
	execARN := startExec(t, srv, smARN, `{}`)

	// Then: the Task's own budget really interrupts it, with the error name
	// AWS raises — not a success 30 seconds later
	got := waitForTerminal(t, srv, execARN)
	if got.Status != "FAILED" {
		t.Fatalf("status = %q (error=%q cause=%q), want FAILED", got.Status, got.Error, got.Cause)
	}
	if got.Error != "States.Timeout" {
		t.Errorf("error = %q, want States.Timeout", got.Error)
	}
	events := execHistory(t, srv, execARN)
	if countEvents(events, "TaskTimedOut") != 1 {
		t.Errorf("history = %v, want a TaskTimedOut event", eventTypes(events))
	}
}

func TestStartExecution_taskTimeoutSecondsIsCatchable(t *testing.T) {
	// Given: the same over-running Task, with the Catch a workflow would use
	// to route a timeout to a fallback
	srv := helpers.NewTestServer(t)
	childARN := createSM(t, srv, "slow-child-caught-sm", slowChildDefinition)
	catch := `
	      "Catch": [{"ErrorEquals": ["States.Timeout"], "Next": "Fallback"}],`
	smARN := createSM(t, srv, "task-timeout-catch-sm", slowChildTaskDefinition(childARN, catch))

	// When: we run it
	execARN := startExec(t, srv, smARN, `{}`)

	// Then: the Catch routed it — a Task timeout is an ordinary catchable
	// failure of a healthy execution, not the execution timing out
	got := waitForTerminal(t, srv, execARN)
	if got.Status != "SUCCEEDED" {
		t.Fatalf("status = %q (error=%q cause=%q), want SUCCEEDED", got.Status, got.Error, got.Cause)
	}
	if got.Output != `"timed out"` {
		t.Errorf("output = %s, want \"timed out\"", got.Output)
	}
}
