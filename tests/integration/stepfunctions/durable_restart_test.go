//go:build !nosqlite

// Step Functions executions across an emulator restart. Under -tags nosqlite,
// state.NewSQLiteStore is a stub that always errors (see
// internal/state/sqlite_hybrid_nosqlite.go), so there is no durable store and
// a restart cannot preserve anything; guarding the file mirrors
// tests/integration/lambda/tags_restart_test.go.
//
// Every test here restarts the whole server over the same SQLite file: the
// first server is shut down the way overcast shuts down (service Stop, then
// the store is closed), and a second one is built over a reopened store.
package stepfunctions_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/overcast-sh/overcast/internal/state"
	"github.com/overcast-sh/overcast/tests/helpers"
)

// durableServer is one incarnation of the emulator over a data directory.
type durableServer struct {
	*helpers.TestServer
	store *state.SQLiteStore
}

// startDurableServer opens (or reopens) the SQLite store in dataDir and
// builds a server with a mock clock over it.
func startDurableServer(t *testing.T, dataDir string) *durableServer {
	t.Helper()
	store, err := state.NewSQLiteStore(dataDir)
	if err != nil {
		t.Fatalf("open SQLite store in %s: %v", dataDir, err)
	}
	// Any real operation blocks on the background migration; synchronize past
	// it so the first request is not answered "still migrating".
	if _, _, err := store.Get(context.Background(), "warmup", "warmup"); err != nil {
		t.Fatalf("waiting for SQLite migration: %v", err)
	}
	srv := helpers.NewTestServer(t, helpers.WithStore(store), helpers.WithMockClock())
	t.Cleanup(func() { _ = store.Close() })
	return &durableServer{TestServer: srv, store: store}
}

// restart shuts this server down, closes its store and starts a new server
// over the same data directory.
func (d *durableServer) restart(t *testing.T, dataDir string) *durableServer {
	t.Helper()
	d.Shutdown()
	if err := d.store.Close(); err != nil {
		t.Fatalf("close SQLite store: %v", err)
	}
	return startDurableServer(t, dataDir)
}

// waitForEvent polls the live history until an event of the given type is
// recorded, and returns the history at that point.
func waitForEvent(t *testing.T, srv *helpers.TestServer, execARN, eventType string) []historyEvent {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		events := execHistory(t, srv, execARN)
		for _, e := range events {
			if e.Type == eventType {
				return events
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("no %s event after 10s; history: %v", eventType, eventTypes(events))
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// assertContinuousHistory checks the id / previousEventId linkage the console
// and SDK assertions rely on: ids run 1..n without a gap, every event links
// to an earlier one, and the history recorded before the restart is an
// unchanged prefix of the history after it.
func assertContinuousHistory(t *testing.T, before, after []historyEvent) {
	t.Helper()
	for i, e := range after {
		if e.ID != int64(i+1) {
			t.Fatalf("event %d has id %d; ids must run 1..n: %v", i, e.ID, eventTypes(after))
		}
		if i > 0 && (e.PreviousEventID < 1 || e.PreviousEventID >= e.ID) {
			t.Fatalf("event %d (%s) links to %d", e.ID, e.Type, e.PreviousEventID)
		}
	}
	if len(after) < len(before) {
		t.Fatalf("history shrank across the restart: %d → %d events", len(before), len(after))
	}
	for i := range before {
		if before[i].ID != after[i].ID || before[i].Type != after[i].Type || before[i].PreviousEventID != after[i].PreviousEventID {
			t.Fatalf("event %d changed across the restart: %+v → %+v", i+1, before[i], after[i])
		}
	}
}

// eventOf returns the first event of a type, failing the test if none.
func eventOf(t *testing.T, events []historyEvent, eventType string) historyEvent {
	t.Helper()
	for _, e := range events {
		if e.Type == eventType {
			return e
		}
	}
	t.Fatalf("no %s event in %v", eventType, eventTypes(events))
	return historyEvent{}
}

// durableCallbackDefinition parks on a .waitForTaskToken SQS task, places the
// worker's answer under $.answer, and continues to a Pass state.
func durableCallbackDefinition(queueURL, extra string) string {
	return `{
	  "StartAt": "Ask",
	  "States": {
	    "Ask": {
	      "Type": "Task",
	      "Resource": "arn:aws:states:::sqs:sendMessage.waitForTaskToken",
	      "Parameters": {"QueueUrl": "` + queueURL + `", "MessageBody": {"token.$": "$$.Task.Token"}},
	      "ResultPath": "$.answer"` + extra + `,
	      "Next": "After"
	    },
	    "After": {"Type": "Pass", "End": true},
	    "Handled": {"Type": "Pass", "End": true}
	  }
	}`
}

func TestRestart_sendTaskSuccessResumesParkedExecution(t *testing.T) {
	// Given: an execution parked on a .waitForTaskToken task
	dataDir := t.TempDir()
	first := startDurableServer(t, dataDir)
	queueURL := createQueue(t, first.TestServer, "durable-ok")
	smARN := createSM(t, first.TestServer, "durable-ok", durableCallbackDefinition(queueURL, ""))
	execARN := startExec(t, first.TestServer, smARN, `{"order":7}`)
	token := tokenFrom(t, awaitQueueMessage(t, first.TestServer, queueURL))
	before := waitForEvent(t, first.TestServer, execARN, "TaskSubmitted")

	// When: the emulator restarts and the worker then reports success
	second := first.restart(t, dataDir)
	if got := describeExec(t, second.TestServer, execARN); got.Status != "RUNNING" {
		t.Fatalf("after restart status=%q (%s: %s), want RUNNING", got.Status, got.Error, got.Cause)
	}
	sfnOK(t, second.TestServer, "SendTaskSuccess", map[string]any{"taskToken": token, "output": `{"approved":true}`})

	// Then: the execution resumes where it parked and finishes with the
	// worker's output placed by ResultPath
	got := waitForTerminal(t, second.TestServer, execARN)
	if got.Status != "SUCCEEDED" || got.Output != `{"answer":{"approved":true},"order":7}` {
		t.Fatalf("status=%q output=%q (%s: %s)", got.Status, got.Output, got.Error, got.Cause)
	}
	after := execHistory(t, second.TestServer, execARN)
	assertContinuousHistory(t, before, after)
	succeeded := eventOf(t, after, "TaskSucceeded")
	if submitted := eventOf(t, after, "TaskSubmitted"); succeeded.PreviousEventID != submitted.ID {
		t.Errorf("TaskSucceeded links to %d, want the TaskSubmitted event %d", succeeded.PreviousEventID, submitted.ID)
	}
	types := strings.Join(eventTypes(after), ",")
	if !strings.Contains(types, "TaskSubmitted,TaskSucceeded,TaskStateExited,PassStateEntered,PassStateExited,ExecutionSucceeded") {
		t.Errorf("types = %s", types)
	}
	if n := strings.Count(types, "TaskStateEntered"); n != 1 {
		t.Errorf("TaskStateEntered recorded %d times, want 1: %s", n, types)
	}
}

func TestRestart_sendTaskFailureIsCatchable(t *testing.T) {
	// Given: a parked task with a Catch on the error a worker will report
	dataDir := t.TempDir()
	first := startDurableServer(t, dataDir)
	queueURL := createQueue(t, first.TestServer, "durable-fail")
	def := durableCallbackDefinition(queueURL, `, "Catch": [{"ErrorEquals": ["Order.Rejected"], "ResultPath": "$.why", "Next": "Handled"}]`)
	smARN := createSM(t, first.TestServer, "durable-fail", def)
	execARN := startExec(t, first.TestServer, smARN, `{}`)
	token := tokenFrom(t, awaitQueueMessage(t, first.TestServer, queueURL))
	waitForEvent(t, first.TestServer, execARN, "TaskSubmitted")

	// When: the emulator restarts and the worker reports failure
	second := first.restart(t, dataDir)
	sfnOK(t, second.TestServer, "SendTaskFailure", map[string]any{"taskToken": token, "error": "Order.Rejected", "cause": "out of stock"})

	// Then: the Catch handles it by name
	got := waitForTerminal(t, second.TestServer, execARN)
	if got.Status != "SUCCEEDED" || !strings.Contains(got.Output, `"Error":"Order.Rejected"`) || !strings.Contains(got.Output, "out of stock") {
		t.Fatalf("status=%q output=%q (%s: %s)", got.Status, got.Output, got.Error, got.Cause)
	}
	if !containsEvent(eventTypes(execHistory(t, second.TestServer, execARN)), "TaskFailed") {
		t.Error("no TaskFailed event after the restart")
	}
}

func TestRestart_retryPositionSurvives(t *testing.T) {
	// Given: a task allowed one retry, whose first attempt already failed
	// and whose retry is parked on its token
	dataDir := t.TempDir()
	first := startDurableServer(t, dataDir)
	queueURL := createQueue(t, first.TestServer, "durable-retry")
	def := durableCallbackDefinition(queueURL, `,
	      "Retry": [{"ErrorEquals": ["Worker.Flaky"], "MaxAttempts": 1, "IntervalSeconds": 1}],
	      "Catch": [{"ErrorEquals": ["States.ALL"], "ResultPath": "$.why", "Next": "Handled"}]`)
	smARN := createSM(t, first.TestServer, "durable-retry", def)
	execARN := startExec(t, first.TestServer, smARN, `{}`)
	firstToken := tokenFrom(t, awaitQueueMessage(t, first.TestServer, queueURL))
	sfnOK(t, first.TestServer, "SendTaskFailure", map[string]any{"taskToken": firstToken, "error": "Worker.Flaky"})
	retryToken := tokenFrom(t, awaitRetryMessage(t, first.TestServer, queueURL))
	deadline := time.Now().Add(10 * time.Second)
	for len(findEvents(rawHistory(t, first.TestServer, execARN), "TaskSubmitted")) < 2 {
		if time.Now().After(deadline) {
			t.Fatal("the retry never parked")
		}
		time.Sleep(5 * time.Millisecond)
	}

	// When: the emulator restarts and the retry fails the same way
	second := first.restart(t, dataDir)
	sfnOK(t, second.TestServer, "SendTaskFailure", map[string]any{"taskToken": retryToken, "error": "Worker.Flaky", "cause": "again"})

	// Then: the retrier is already spent, so the Catch takes over instead of
	// a third attempt
	got := waitForTerminal(t, second.TestServer, execARN)
	if got.Status != "SUCCEEDED" || !strings.Contains(got.Output, `"Error":"Worker.Flaky"`) {
		t.Fatalf("status=%q output=%q (%s: %s)", got.Status, got.Output, got.Error, got.Cause)
	}
	if n := len(findEvents(rawHistory(t, second.TestServer, execARN), "TaskScheduled")); n != 2 {
		t.Errorf("TaskScheduled events = %d, want 2", n)
	}
}

// awaitRetryMessage advances the mock clock past a retry's interval until the
// retried task's message arrives. The retry arms its timer on its own
// goroutine, so a single advance could land before the timer exists.
func awaitRetryMessage(t *testing.T, srv *helpers.TestServer, queueURL string) string {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		srv.AdvanceClock(time.Second)
		resp := awsJSONCall(t, srv, "AmazonSQS.ReceiveMessage", map[string]any{"QueueUrl": queueURL, "MaxNumberOfMessages": 1})
		var out struct {
			Messages []struct {
				Body string `json:"Body"`
			} `json:"Messages"`
		}
		helpers.DecodeJSON(t, resp, &out)
		resp.Body.Close()
		if len(out.Messages) > 0 {
			return out.Messages[0].Body
		}
		if time.Now().After(deadline) {
			t.Fatal("the retried task never sent its message")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestRestart_heartbeatTimeoutFiresFromPersistedDeadline(t *testing.T) {
	// Given: a parked task that demands a heartbeat every 60 seconds
	dataDir := t.TempDir()
	first := startDurableServer(t, dataDir)
	queueURL := createQueue(t, first.TestServer, "durable-hb")
	smARN := createSM(t, first.TestServer, "durable-hb", durableCallbackDefinition(queueURL, `, "HeartbeatSeconds": 60`))
	execARN := startExec(t, first.TestServer, smARN, `{}`)
	waitForEvent(t, first.TestServer, execARN, "TaskSubmitted")

	// When: the emulator restarts and nobody heartbeats past the deadline
	second := first.restart(t, dataDir)
	if got := describeExec(t, second.TestServer, execARN); got.Status != "RUNNING" {
		t.Fatalf("after restart status=%q (%s: %s), want RUNNING", got.Status, got.Error, got.Cause)
	}
	second.AdvanceClock(61 * time.Second)

	// Then: the task times out with States.HeartbeatTimeout
	got := waitForTerminal(t, second.TestServer, execARN)
	if got.Status != "FAILED" || got.Error != "States.HeartbeatTimeout" {
		t.Fatalf("status=%q error=%q cause=%q", got.Status, got.Error, got.Cause)
	}
	if !containsEvent(eventTypes(execHistory(t, second.TestServer, execARN)), "TaskTimedOut") {
		t.Error("no TaskTimedOut event")
	}
}

func TestRestart_activityTaskIsAvailableAgain(t *testing.T) {
	// Given: an activity task scheduled but not yet picked up by any worker
	dataDir := t.TempDir()
	first := startDurableServer(t, dataDir)
	activityARN, _ := sfnOK(t, first.TestServer, "CreateActivity", map[string]any{"name": "durable-approve"})["activityArn"].(string)
	def := `{"StartAt":"Approve","States":{"Approve":{"Type":"Task","Resource":"` + activityARN + `","End":true}}}`
	smARN := createSM(t, first.TestServer, "durable-activity", def)
	execARN := startExec(t, first.TestServer, smARN, `{"doc":"a"}`)
	waitForEvent(t, first.TestServer, execARN, "ActivityScheduled")

	// When: the emulator restarts and a worker polls for the activity
	second := first.restart(t, dataDir)
	task := sfnOK(t, second.TestServer, "GetActivityTask", map[string]any{"activityArn": activityARN, "workerName": "after-restart"})

	// Then: the parked task is handed out, and answering it completes the
	// execution
	token, _ := task["taskToken"].(string)
	if token == "" || task["input"] != `{"doc":"a"}` {
		t.Fatalf("GetActivityTask after restart = %v", task)
	}
	sfnOK(t, second.TestServer, "SendTaskSuccess", map[string]any{"taskToken": token, "output": `{"ok":1}`})
	got := waitForTerminal(t, second.TestServer, execARN)
	if got.Status != "SUCCEEDED" || got.Output != `{"ok":1}` {
		t.Fatalf("status=%q output=%q (%s: %s)", got.Status, got.Output, got.Error, got.Cause)
	}
	types := strings.Join(eventTypes(execHistory(t, second.TestServer, execARN)), ",")
	if !strings.Contains(types, "ActivityScheduled,ActivityStarted,ActivitySucceeded") {
		t.Errorf("types = %s", types)
	}
}

func TestRestart_pickedUpActivityKeepsItsWorker(t *testing.T) {
	// Given: an activity task a worker has already picked up
	dataDir := t.TempDir()
	first := startDurableServer(t, dataDir)
	activityARN, _ := sfnOK(t, first.TestServer, "CreateActivity", map[string]any{"name": "durable-picked"})["activityArn"].(string)
	def := `{"StartAt":"Approve","States":{"Approve":{"Type":"Task","Resource":"` + activityARN + `","End":true}}}`
	smARN := createSM(t, first.TestServer, "durable-picked", def)
	execARN := startExec(t, first.TestServer, smARN, `{}`)
	waitForEvent(t, first.TestServer, execARN, "ActivityScheduled")
	token, _ := sfnOK(t, first.TestServer, "GetActivityTask", map[string]any{"activityArn": activityARN, "workerName": "w1"})["taskToken"].(string)
	waitForEvent(t, first.TestServer, execARN, "ActivityStarted")

	// When: the emulator restarts and the same worker answers
	second := first.restart(t, dataDir)
	sfnOK(t, second.TestServer, "SendTaskSuccess", map[string]any{"taskToken": token, "output": `{"done":true}`})

	// Then: the execution completes and the task was started exactly once
	got := waitForTerminal(t, second.TestServer, execARN)
	if got.Status != "SUCCEEDED" {
		t.Fatalf("status=%q (%s: %s)", got.Status, got.Error, got.Cause)
	}
	types := strings.Join(eventTypes(execHistory(t, second.TestServer, execARN)), ",")
	if n := strings.Count(types, "ActivityStarted"); n != 1 {
		t.Errorf("ActivityStarted recorded %d times, want 1: %s", n, types)
	}
}

func TestRestart_waitStateResumesWithRemainingTime(t *testing.T) {
	// Given: an execution in a 120-second Wait
	dataDir := t.TempDir()
	first := startDurableServer(t, dataDir)
	def := `{"StartAt":"Hold","States":{"Hold":{"Type":"Wait","Seconds":120,"Next":"Done"},"Done":{"Type":"Pass","Result":{"done":true},"End":true}}}`
	smARN := createSM(t, first.TestServer, "durable-wait", def)
	execARN := startExec(t, first.TestServer, smARN, `{}`)
	before := waitForEvent(t, first.TestServer, execARN, "WaitStateEntered")

	// When: the emulator restarts 100 seconds into the wait
	second := first.restart(t, dataDir)
	second.AdvanceClock(100 * time.Second)
	if got := describeExec(t, second.TestServer, execARN); got.Status != "RUNNING" {
		t.Fatalf("after restart status=%q (%s: %s), want RUNNING", got.Status, got.Error, got.Cause)
	}

	// Then: the Wait finishes once the remaining 20 seconds have passed, not
	// a fresh 120
	second.AdvanceClock(10 * time.Second)
	if got := describeExec(t, second.TestServer, execARN); got.Status != "RUNNING" {
		t.Fatalf("10s later status=%q, want still RUNNING", got.Status)
	}
	second.AdvanceClock(11 * time.Second)
	got := waitForTerminal(t, second.TestServer, execARN)
	if got.Status != "SUCCEEDED" || got.Output != `{"done":true}` {
		t.Fatalf("status=%q output=%q (%s: %s)", got.Status, got.Output, got.Error, got.Cause)
	}
	after := execHistory(t, second.TestServer, execARN)
	assertContinuousHistory(t, before, after)
	if exited := eventOf(t, after, "WaitStateExited"); exited.PreviousEventID != eventOf(t, after, "WaitStateEntered").ID {
		t.Errorf("WaitStateExited links to %d", exited.PreviousEventID)
	}
}

func TestRestart_executionNotParkedIsNotLeftRunning(t *testing.T) {
	// Given: an execution waiting on a token inside a Parallel branch — a
	// point Overcast cannot resume from
	dataDir := t.TempDir()
	first := startDurableServer(t, dataDir)
	queueURL := createQueue(t, first.TestServer, "durable-branch")
	def := `{"StartAt":"Fan","States":{"Fan":{"Type":"Parallel","End":true,"Branches":[{"StartAt":"Ask","States":{"Ask":{
	  "Type":"Task","Resource":"arn:aws:states:::sqs:sendMessage.waitForTaskToken",
	  "Parameters":{"QueueUrl":"` + queueURL + `","MessageBody":{"token.$":"$$.Task.Token"}},"End":true}}}]}}}`
	smARN := createSM(t, first.TestServer, "durable-branch", def)
	execARN := startExec(t, first.TestServer, smARN, `{}`)
	waitForEvent(t, first.TestServer, execARN, "TaskSubmitted")

	// When: the emulator restarts
	second := first.restart(t, dataDir)

	// Then: the execution has ended FAILED with a States.Runtime cause naming
	// the restart, and it can be redriven from the state it was in
	got := describeExec(t, second.TestServer, execARN)
	if got.Status != "FAILED" || got.Error != "States.Runtime" || !strings.Contains(got.Cause, "restart") {
		t.Fatalf("status=%q error=%q cause=%q", got.Status, got.Error, got.Cause)
	}
	described := sfnOK(t, second.TestServer, "DescribeExecution", map[string]any{"executionArn": execARN})
	if described["redriveStatus"] != "REDRIVABLE" {
		t.Errorf("redriveStatus = %v (%v)", described["redriveStatus"], described["redriveStatusReason"])
	}
	events := execHistory(t, second.TestServer, execARN)
	if last := events[len(events)-1]; last.Type != "ExecutionFailed" {
		t.Errorf("last event = %s, want ExecutionFailed", last.Type)
	}
	assertContinuousHistory(t, nil, events)
}
