// Package stepfunctions_test — callback patterns: `.waitForTaskToken`,
// activities, SendTaskSuccess/SendTaskFailure/SendTaskHeartbeat.
//
// Run: go test ./tests/integration/stepfunctions/...
package stepfunctions_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/overcast-sh/overcast/tests/helpers"
)

// awaitQueueMessage polls an SQS queue until a message arrives.
func awaitQueueMessage(t *testing.T, srv *helpers.TestServer, queueURL string) string {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
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
			t.Fatal("no message arrived on the queue")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// callbackDefinition sends the task token to an SQS queue and waits for it.
func callbackDefinition(queueURL, extra string) string {
	return `{
	  "StartAt": "Ask",
	  "States": {
	    "Ask": {
	      "Type": "Task",
	      "Resource": "arn:aws:states:::sqs:sendMessage.waitForTaskToken",
	      "Parameters": {"QueueUrl": "` + queueURL + `", "MessageBody": {"token.$": "$$.Task.Token", "input.$": "$"}}` + extra + `,
	      "End": true
	    },
	    "Handled": {"Type": "Pass", "End": true}
	  }
	}`
}

func tokenFrom(t *testing.T, body string) string {
	t.Helper()
	var msg struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal([]byte(body), &msg); err != nil || msg.Token == "" {
		t.Fatalf("message %q carries no token (%v)", body, err)
	}
	return msg.Token
}

func sfnOK(t *testing.T, srv *helpers.TestServer, op string, body map[string]any) map[string]any {
	t.Helper()
	resp := sfnCall(t, srv, op, body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("%s: status %d: %s", op, resp.StatusCode, helpers.ReadBody(t, resp))
	}
	var out map[string]any
	helpers.DecodeJSON(t, resp, &out)
	return out
}

func TestSendTaskSuccess_resumesWaitForTaskToken(t *testing.T) {
	// Given: an execution parked on a .waitForTaskToken SQS task
	srv := helpers.NewTestServer(t)
	queueURL := createQueue(t, srv, "callbacks")
	smARN := createSM(t, srv, "callback-ok", callbackDefinition(queueURL, ""))
	execARN := startExec(t, srv, smARN, `{"order":7}`)
	token := tokenFrom(t, awaitQueueMessage(t, srv, queueURL))

	// When: a worker reports success with the token
	sfnOK(t, srv, "SendTaskSuccess", map[string]any{"taskToken": token, "output": `{"approved":true}`})

	// Then: the task's result is the reported output
	got := waitForTerminal(t, srv, execARN)
	if got.Status != "SUCCEEDED" || got.Output != `{"approved":true}` {
		t.Fatalf("status=%q output=%q (%s: %s)", got.Status, got.Output, got.Error, got.Cause)
	}
	types := strings.Join(rawTypes(rawHistory(t, srv, execARN)), ",")
	if !strings.Contains(types, "TaskScheduled,TaskStarted,TaskSubmitted,TaskSucceeded,TaskStateExited") {
		t.Errorf("types = %s", types)
	}
}

func TestSendTaskFailure_failsTheWaitingTask(t *testing.T) {
	// Given: a waiting task with a Catch on the error a worker will report
	srv := helpers.NewTestServer(t)
	queueURL := createQueue(t, srv, "callbacks-fail")
	def := callbackDefinition(queueURL, `, "Catch": [{"ErrorEquals": ["Order.Rejected"], "ResultPath": "$.why", "Next": "Handled"}]`)
	smARN := createSM(t, srv, "callback-fail", def)
	execARN := startExec(t, srv, smARN, `{}`)
	token := tokenFrom(t, awaitQueueMessage(t, srv, queueURL))

	// When: the worker reports failure
	sfnOK(t, srv, "SendTaskFailure", map[string]any{"taskToken": token, "error": "Order.Rejected", "cause": "out of stock"})

	// Then: the error is catchable by name
	got := waitForTerminal(t, srv, execARN)
	if got.Status != "SUCCEEDED" || !strings.Contains(got.Output, `"Error":"Order.Rejected"`) || !strings.Contains(got.Output, "out of stock") {
		t.Fatalf("status=%q output=%q", got.Status, got.Output)
	}
	if n := len(findEvents(rawHistory(t, srv, execARN), "TaskFailed")); n != 1 {
		t.Errorf("TaskFailed events = %d, want 1", n)
	}
}

func TestWaitForTaskToken_missedHeartbeatTimesOut(t *testing.T) {
	// Given: a waiting task that demands a heartbeat every second
	srv := helpers.NewTestServer(t)
	queueURL := createQueue(t, srv, "callbacks-hb")
	smARN := createSM(t, srv, "callback-hb", callbackDefinition(queueURL, `, "HeartbeatSeconds": 1`))

	// When: nobody ever calls back
	execARN := startExec(t, srv, smARN, `{}`)

	// Then: the task fails with States.HeartbeatTimeout, recorded as TaskTimedOut
	got := waitForTerminal(t, srv, execARN)
	if got.Status != "FAILED" || got.Error != "States.HeartbeatTimeout" {
		t.Fatalf("status=%q error=%q", got.Status, got.Error)
	}
	if n := len(findEvents(rawHistory(t, srv, execARN), "TaskTimedOut")); n != 1 {
		t.Errorf("TaskTimedOut events = %d, want 1", n)
	}
}

func TestSendTaskHeartbeat_keepsTheTaskAlive(t *testing.T) {
	// Given: a waiting task with a two-second heartbeat
	srv := helpers.NewTestServer(t)
	queueURL := createQueue(t, srv, "callbacks-hb-ok")
	smARN := createSM(t, srv, "callback-hb-ok", callbackDefinition(queueURL, `, "HeartbeatSeconds": 2`))
	execARN := startExec(t, srv, smARN, `{}`)
	token := tokenFrom(t, awaitQueueMessage(t, srv, queueURL))

	// When: the worker heartbeats past the heartbeat window, then succeeds
	for i := 0; i < 3; i++ {
		time.Sleep(time.Second)
		sfnOK(t, srv, "SendTaskHeartbeat", map[string]any{"taskToken": token})
	}
	sfnOK(t, srv, "SendTaskSuccess", map[string]any{"taskToken": token, "output": `"done"`})

	// Then: the task survived the heartbeats
	got := waitForTerminal(t, srv, execARN)
	if got.Status != "SUCCEEDED" || got.Output != `"done"` {
		t.Fatalf("status=%q output=%q error=%q", got.Status, got.Output, got.Error)
	}
}

func TestSendTaskSuccess_unknownTokenIsRejected(t *testing.T) {
	// Given: no task is waiting
	srv := helpers.NewTestServer(t)

	// When: a worker reports success for a token nobody issued
	resp := sfnCall(t, srv, "SendTaskSuccess", map[string]any{"taskToken": "bm90LWEtdG9rZW4", "output": `{}`})
	defer resp.Body.Close()

	// Then: AWS's TaskDoesNotExist
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	if body := helpers.ReadBody(t, resp); !strings.Contains(body, "TaskDoesNotExist") {
		t.Errorf("body = %s", body)
	}
}

func TestSendTaskSuccess_invalidOutputIsRejected(t *testing.T) {
	// Given: a waiting task
	srv := helpers.NewTestServer(t)
	queueURL := createQueue(t, srv, "callbacks-badout")
	smARN := createSM(t, srv, "callback-badout", callbackDefinition(queueURL, ""))
	execARN := startExec(t, srv, smARN, `{}`)
	token := tokenFrom(t, awaitQueueMessage(t, srv, queueURL))

	// When: the worker reports output that is not JSON
	resp := sfnCall(t, srv, "SendTaskSuccess", map[string]any{"taskToken": token, "output": `not json`})
	defer resp.Body.Close()

	// Then: InvalidOutput, and the task is still waiting
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	if body := helpers.ReadBody(t, resp); !strings.Contains(body, "InvalidOutput") {
		t.Errorf("body = %s", body)
	}
	if got := describeExec(t, srv, execARN); got.Status != "RUNNING" {
		t.Errorf("status = %q, want RUNNING", got.Status)
	}
	sfnOK(t, srv, "StopExecution", map[string]any{"executionArn": execARN})
}

// ─── Activities ───────────────────────────────────────────────────────────────

func TestActivity_workerCompletesTask(t *testing.T) {
	// Given: an activity and a state machine that schedules it
	srv := helpers.NewTestServer(t)
	created := sfnOK(t, srv, "CreateActivity", map[string]any{"name": "approve"})
	activityARN, _ := created["activityArn"].(string)
	if !strings.Contains(activityARN, ":activity:approve") {
		t.Fatalf("activityArn = %q", activityARN)
	}
	def := `{"StartAt":"Work","States":{"Work":{"Type":"Task","Resource":"` + activityARN + `",
	  "Parameters":{"job.$":"$.job"},"HeartbeatSeconds":30,"TimeoutSeconds":60,"End":true}}}`
	smARN := createSM(t, srv, "activity-sm", def)
	execARN := startExec(t, srv, smARN, `{"job":"j-1"}`)

	// When: a worker polls, then reports success
	task := sfnOK(t, srv, "GetActivityTask", map[string]any{"activityArn": activityARN, "workerName": "worker-1"})
	token, _ := task["taskToken"].(string)
	if token == "" || task["input"] != `{"job":"j-1"}` {
		t.Fatalf("GetActivityTask = %v", task)
	}
	sfnOK(t, srv, "SendTaskSuccess", map[string]any{"taskToken": token, "output": `{"result":"ok"}`})

	// Then: the execution finishes with the worker's output and AWS's activity events
	got := waitForTerminal(t, srv, execARN)
	if got.Status != "SUCCEEDED" || got.Output != `{"result":"ok"}` {
		t.Fatalf("status=%q output=%q (%s)", got.Status, got.Output, got.Cause)
	}
	events := rawHistory(t, srv, execARN)
	types := strings.Join(rawTypes(events), ",")
	if !strings.Contains(types, "TaskStateEntered,ActivityScheduled,ActivityStarted,ActivitySucceeded,TaskStateExited") {
		t.Errorf("types = %s", types)
	}
	scheduled := findEvents(events, "ActivityScheduled")[0].detail("activityScheduledEventDetails")
	if scheduled["resource"] != activityARN || scheduled["heartbeatInSeconds"] != float64(30) || scheduled["timeoutInSeconds"] != float64(60) {
		t.Errorf("activityScheduledEventDetails = %v", scheduled)
	}
	started := findEvents(events, "ActivityStarted")[0].detail("activityStartedEventDetails")
	if started["workerName"] != "worker-1" {
		t.Errorf("activityStartedEventDetails = %v", started)
	}
}

func TestGetActivityTask_unknownActivityIsRefused(t *testing.T) {
	// Given: an activity exists, but the poll names a different one
	srv := helpers.NewTestServer(t)
	created := sfnOK(t, srv, "CreateActivity", map[string]any{"name": "idle"})
	activityARN, _ := created["activityArn"].(string)

	// When: a worker polls the unknown activity
	resp := sfnCall(t, srv, "GetActivityTask", map[string]any{"activityArn": activityARN + "-missing"})

	// Then: AWS's ActivityDoesNotExist rather than a long poll
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	if body := helpers.ReadBody(t, resp); !strings.Contains(body, "ActivityDoesNotExist") {
		t.Errorf("body = %s", body)
	}
}

func TestActivity_lifecycleAndTags(t *testing.T) {
	// Given: an activity created with a tag
	srv := helpers.NewTestServer(t)
	created := sfnOK(t, srv, "CreateActivity", map[string]any{
		"name": "lifecycle", "tags": []map[string]string{{"key": "team", "value": "a"}},
	})
	arn, _ := created["activityArn"].(string)

	// When / Then: CreateActivity with the same name is idempotent
	again := sfnOK(t, srv, "CreateActivity", map[string]any{"name": "lifecycle"})
	if again["activityArn"] != arn {
		t.Errorf("second create = %v, want the same ARN", again)
	}
	described := sfnOK(t, srv, "DescribeActivity", map[string]any{"activityArn": arn})
	if described["name"] != "lifecycle" || described["creationDate"] == nil {
		t.Errorf("DescribeActivity = %v", described)
	}
	listed := sfnOK(t, srv, "ListActivities", map[string]any{})
	if acts, _ := listed["activities"].([]any); len(acts) != 1 {
		t.Errorf("ListActivities = %v", listed)
	}
	tags := sfnOK(t, srv, "ListTagsForResource", map[string]any{"resourceArn": arn})
	if list, _ := tags["tags"].([]any); len(list) != 1 {
		t.Errorf("tags = %v", tags)
	}
	sfnOK(t, srv, "DeleteActivity", map[string]any{"activityArn": arn})
	resp := sfnCall(t, srv, "DescribeActivity", map[string]any{"activityArn": arn})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	if body := helpers.ReadBody(t, resp); !strings.Contains(body, "ActivityDoesNotExist") {
		t.Errorf("body = %s", body)
	}
}
