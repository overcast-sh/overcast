// Package stepfunctions_test — Task resource integrations, data flow and the
// loud failures the interpreter raises for ASL it does not support.
package stepfunctions_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/overcast-sh/overcast/tests/helpers"
)

// ─── Data flow: InputPath / Parameters / ResultSelector / ResultPath ──────────

func TestStartExecution_dataFlowPipeline(t *testing.T) {
	// Given: a Pass state exercising the full input/output pipeline
	srv := helpers.NewTestServer(t)
	def := `{
	  "StartAt": "Shape",
	  "States": {
	    "Shape": {
	      "Type": "Pass",
	      "InputPath": "$.payload",
	      "Parameters": {"who.$": "$.name", "run.$": "$$.Execution.Name", "fixed": 1},
	      "ResultPath": "$.result",
	      "OutputPath": "$.result",
	      "End": true
	    }
	  }
	}`
	smARN := createSM(t, srv, "flow-sm", def)

	// When: we run it with a named execution
	resp := sfnCall(t, srv, "StartExecution", map[string]any{
		"stateMachineArn": smARN,
		"name":            "flow-1",
		"input":           `{"payload":{"name":"ada"},"other":true}`,
	})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	var started struct {
		ExecutionArn string `json:"executionArn"`
	}
	helpers.DecodeJSON(t, resp, &started)

	// Then: each stage applied in ASL's documented order
	got := waitForTerminal(t, srv, started.ExecutionArn)
	if got.Status != "SUCCEEDED" {
		t.Fatalf("status = %q (error=%q cause=%q)", got.Status, got.Error, got.Cause)
	}
	var output map[string]any
	if err := json.Unmarshal([]byte(got.Output), &output); err != nil {
		t.Fatalf("unmarshal output %q: %v", got.Output, err)
	}
	if output["who"] != "ada" || output["fixed"] != float64(1) || output["run"] != "flow-1" {
		t.Fatalf("output = %s, want who=ada fixed=1 run=flow-1", got.Output)
	}
}

func TestStartExecution_resultPathNullDiscardsResult(t *testing.T) {
	// Given: a Pass state whose ResultPath is explicitly null
	srv := helpers.NewTestServer(t)
	def := `{"StartAt":"P","States":{"P":{"Type":"Pass","Result":"ignored","ResultPath":null,"End":true}}}`
	smARN := createSM(t, srv, "discard-sm", def)

	// When: we run it
	execARN := startExec(t, srv, smARN, `{"keep":true}`)

	// Then: the input passes through untouched — null is not the same as absent
	got := waitForTerminal(t, srv, execARN)
	if got.Output != `{"keep":true}` {
		t.Fatalf("output = %s, want the input passed through", got.Output)
	}
}

// ─── Wait ─────────────────────────────────────────────────────────────────────

func TestStartExecution_waitStateContinues(t *testing.T) {
	// Given: a Wait state with a zero-second delay, then a Pass
	srv := helpers.NewTestServer(t)
	def := `{
	  "StartAt": "Hold",
	  "States": {
	    "Hold": {"Type": "Wait", "Seconds": 0, "Next": "Done"},
	    "Done": {"Type": "Pass", "Result": "after", "End": true}
	  }
	}`
	smARN := createSM(t, srv, "wait-sm", def)

	// When: we run it
	execARN := startExec(t, srv, smARN, `{}`)

	// Then: the wait is recorded in the history and the branch continues
	got := waitForTerminal(t, srv, execARN)
	if got.Status != "SUCCEEDED" || got.Output != `"after"` {
		t.Fatalf("status=%q output=%s (error=%q cause=%q)", got.Status, got.Output, got.Error, got.Cause)
	}
	types := eventTypes(execHistory(t, srv, execARN))
	if !containsEvent(types, "WaitStateEntered") || !containsEvent(types, "WaitStateExited") {
		t.Fatalf("history = %v, want WaitStateEntered/WaitStateExited", types)
	}
}

func TestStartExecution_waitBeyondBudgetTimesOut(t *testing.T) {
	// Given: a state machine whose own TimeoutSeconds is shorter than its Wait
	srv := helpers.NewTestServer(t)
	def := `{
	  "TimeoutSeconds": 1,
	  "StartAt": "Hold",
	  "States": {"Hold": {"Type": "Wait", "Seconds": 3600, "End": true}}
	}`
	smARN := createSM(t, srv, "timeout-sm", def)

	// When: we run it
	execARN := startExec(t, srv, smARN, `{}`)

	// Then: it times out loudly rather than blocking or silently skipping
	got := waitForTerminal(t, srv, execARN)
	if got.Status != "TIMED_OUT" {
		t.Fatalf("status = %q, want TIMED_OUT (error=%q cause=%q)", got.Status, got.Error, got.Cause)
	}
	if got.Error != "States.Timeout" {
		t.Errorf("error = %q, want States.Timeout", got.Error)
	}
	types := eventTypes(execHistory(t, srv, execARN))
	if len(types) == 0 || types[len(types)-1] != "ExecutionTimedOut" {
		t.Errorf("history = %v, want it to end with ExecutionTimedOut", types)
	}
}

// ─── Runaway protection ───────────────────────────────────────────────────────

func TestStartExecution_infiniteChoiceLoopFailsLoudly(t *testing.T) {
	// Given: a Choice loop with no exit
	srv := helpers.NewTestServer(t)
	def := `{
	  "TimeoutSeconds": 2,
	  "StartAt": "Spin",
	  "States": {
	    "Spin": {"Type": "Choice", "Choices": [{"Variable": "$.go", "BooleanEquals": true, "Next": "Again"}], "Default": "Stop"},
	    "Again": {"Type": "Pass", "Next": "Spin"},
	    "Stop": {"Type": "Succeed"}
	  }
	}`
	smARN := createSM(t, srv, "loop-sm", def)

	// When: we run it with the looping input
	execARN := startExec(t, srv, smARN, `{"go":true}`)

	// Then: it terminates with an honest failure rather than spinning forever
	got := waitForTerminal(t, srv, execARN)
	if got.Status == "SUCCEEDED" {
		t.Fatalf("status = SUCCEEDED; a non-terminating state machine must fail")
	}
	if got.Error != "States.Timeout" && got.Error != "States.Runtime" {
		t.Fatalf("error = %q, want States.Timeout or States.Runtime", got.Error)
	}
	if got.Cause == "" {
		t.Error("cause is empty; a runaway execution must say why it stopped")
	}
}

// ─── Task integrations ────────────────────────────────────────────────────────

func TestStartExecution_sqsSendMessageIntegration(t *testing.T) {
	// Given: a queue and a state machine that sends to it
	srv := helpers.NewTestServer(t)
	queueURL := createQueue(t, srv, "sfn-target")
	def := `{
	  "StartAt": "Send",
	  "States": {
	    "Send": {
	      "Type": "Task",
	      "Resource": "arn:aws:states:::sqs:sendMessage",
	      "Parameters": {"QueueUrl": "` + queueURL + `", "MessageBody.$": "$.body"},
	      "End": true
	    }
	  }
	}`
	smARN := createSM(t, srv, "sqs-sm", def)

	// When: we run it
	execARN := startExec(t, srv, smARN, `{"body":"hello from the workflow"}`)

	// Then: the state succeeded with SQS's own response, and the message landed
	got := waitForTerminal(t, srv, execARN)
	if got.Status != "SUCCEEDED" {
		t.Fatalf("status = %q (error=%q cause=%q)", got.Status, got.Error, got.Cause)
	}
	var result map[string]any
	if err := json.Unmarshal([]byte(got.Output), &result); err != nil {
		t.Fatalf("unmarshal output %q: %v", got.Output, err)
	}
	if result["MessageId"] == nil {
		t.Errorf("output = %s, want a MessageId from SQS", got.Output)
	}
	if body := receiveMessage(t, srv, queueURL); body != "hello from the workflow" {
		t.Errorf("received %q from the queue", body)
	}
}

func TestStartExecution_snsPublishIntegration(t *testing.T) {
	// Given: a topic and a state machine that publishes to it
	srv := helpers.NewTestServer(t)
	topicARN := createTopic(t, srv, "sfn-topic")
	def := `{
	  "StartAt": "Notify",
	  "States": {
	    "Notify": {
	      "Type": "Task",
	      "Resource": "arn:aws:states:::sns:publish",
	      "Parameters": {"TopicArn": "` + topicARN + `", "Message.$": "$.text"},
	      "End": true
	    }
	  }
	}`
	smARN := createSM(t, srv, "sns-sm", def)

	// When: we run it
	execARN := startExec(t, srv, smARN, `{"text":"published"}`)

	// Then: the state succeeded with SNS's MessageId
	got := waitForTerminal(t, srv, execARN)
	if got.Status != "SUCCEEDED" {
		t.Fatalf("status = %q (error=%q cause=%q)", got.Status, got.Error, got.Cause)
	}
	var result map[string]any
	if err := json.Unmarshal([]byte(got.Output), &result); err != nil {
		t.Fatalf("unmarshal output %q: %v", got.Output, err)
	}
	if id, _ := result["MessageId"].(string); id == "" {
		t.Errorf("output = %s, want a MessageId from SNS", got.Output)
	}
}

func TestStartExecution_dynamoDBPutAndGetIntegration(t *testing.T) {
	// Given: a table and a state machine that writes then reads an item
	srv := helpers.NewTestServer(t)
	createTable(t, srv, "sfn-items")
	def := `{
	  "StartAt": "Put",
	  "States": {
	    "Put": {
	      "Type": "Task",
	      "Resource": "arn:aws:states:::dynamodb:putItem",
	      "Parameters": {"TableName": "sfn-items", "Item": {"id": {"S": "k1"}, "v": {"S": "stored"}}},
	      "ResultPath": null,
	      "Next": "Get"
	    },
	    "Get": {
	      "Type": "Task",
	      "Resource": "arn:aws:states:::dynamodb:getItem",
	      "Parameters": {"TableName": "sfn-items", "Key": {"id": {"S": "k1"}}},
	      "End": true
	    }
	  }
	}`
	smARN := createSM(t, srv, "ddb-sm", def)

	// When: we run it
	execARN := startExec(t, srv, smARN, `{}`)

	// Then: the GetItem response is the state's output
	got := waitForTerminal(t, srv, execARN)
	if got.Status != "SUCCEEDED" {
		t.Fatalf("status = %q (error=%q cause=%q)", got.Status, got.Error, got.Cause)
	}
	var result struct {
		Item map[string]map[string]string `json:"Item"`
	}
	if err := json.Unmarshal([]byte(got.Output), &result); err != nil {
		t.Fatalf("unmarshal output %q: %v", got.Output, err)
	}
	if result.Item["v"]["S"] != "stored" {
		t.Errorf("output = %s, want Item.v.S = stored", got.Output)
	}
}

func TestStartExecution_dynamoDBUnsupportedActionFailsLoudly(t *testing.T) {
	// Given: a DynamoDB integration outside the interpreted set
	srv := helpers.NewTestServer(t)
	def := `{
	  "StartAt": "Del",
	  "States": {
	    "Del": {
	      "Type": "Task",
	      "Resource": "arn:aws:states:::dynamodb:query",
	      "Parameters": {"TableName": "t"},
	      "Catch": [{"ErrorEquals": ["States.ALL"], "Next": "Swallowed"}],
	      "End": true
	    },
	    "Swallowed": {"Type": "Pass", "Result": "caught", "End": true}
	  }
	}`
	smARN := createSM(t, srv, "ddb-unsupported-sm", def)

	// When: we run it
	execARN := startExec(t, srv, smARN, `{}`)

	// Then: it fails loudly — and a Catch on States.ALL cannot swallow it,
	// because an Overcast gap must never become a silent pass-through
	got := waitForTerminal(t, srv, execARN)
	if got.Status != "FAILED" {
		t.Fatalf("status = %q, want FAILED (output=%s)", got.Status, got.Output)
	}
	if got.Error != "States.Runtime" {
		t.Errorf("error = %q, want States.Runtime", got.Error)
	}
}

// ─── Nested executions ────────────────────────────────────────────────────────

func TestStartExecution_nestedSyncExecution(t *testing.T) {
	// Given: a child state machine and a parent that runs it with .sync
	srv := helpers.NewTestServer(t)
	childARN := createSM(t, srv, "child-sm", `{"StartAt":"P","States":{"P":{"Type":"Pass","Result":{"from":"child"},"End":true}}}`)
	parentDef := `{
	  "StartAt": "Call",
	  "States": {
	    "Call": {
	      "Type": "Task",
	      "Resource": "arn:aws:states:::states:startExecution.sync:2",
	      "Parameters": {"StateMachineArn": "` + childARN + `", "Input": {"x": 1}},
	      "OutputPath": "$.Output",
	      "End": true
	    }
	  }
	}`
	parentARN := createSM(t, srv, "parent-sm", parentDef)

	// When: we run the parent
	execARN := startExec(t, srv, parentARN, `{}`)

	// Then: the child really ran and its output came back
	got := waitForTerminal(t, srv, execARN)
	if got.Status != "SUCCEEDED" {
		t.Fatalf("status = %q (error=%q cause=%q)", got.Status, got.Error, got.Cause)
	}
	if got.Output != `{"from":"child"}` {
		t.Fatalf("output = %s, want the child's output", got.Output)
	}
}

func TestStartExecution_nestedSyncPropagatesChildFailure(t *testing.T) {
	// Given: a child that fails, called with .sync
	srv := helpers.NewTestServer(t)
	childARN := createSM(t, srv, "bad-child-sm", `{"StartAt":"F","States":{"F":{"Type":"Fail","Error":"ChildBroke","Cause":"on purpose"}}}`)
	parentDef := `{
	  "StartAt": "Call",
	  "States": {
	    "Call": {
	      "Type": "Task",
	      "Resource": "arn:aws:states:::states:startExecution.sync",
	      "Parameters": {"StateMachineArn": "` + childARN + `"},
	      "End": true
	    }
	  }
	}`
	parentARN := createSM(t, srv, "bad-parent-sm", parentDef)

	// When: we run the parent
	execARN := startExec(t, srv, parentARN, `{}`)

	// Then: the parent fails with States.TaskFailed naming the child
	got := waitForTerminal(t, srv, execARN)
	if got.Status != "FAILED" {
		t.Fatalf("status = %q, want FAILED", got.Status)
	}
	if got.Error != "States.TaskFailed" {
		t.Errorf("error = %q, want States.TaskFailed", got.Error)
	}
}

// ─── Loud failures for unsupported ASL ────────────────────────────────────────

func TestStartExecution_unsupportedAslFeaturesFailLoudly(t *testing.T) {
	cases := []struct {
		name       string
		definition string
	}{
		{
			name: "Map ItemReader over an S3 inventory manifest",
			definition: `{"StartAt":"M","States":{"M":{"Type":"Map","ItemReader":{"Resource":"arn:aws:states:::s3:getObject",` +
				`"ReaderConfig":{"InputType":"MANIFEST"},"Parameters":{"Bucket":"b","Key":"k"}},` +
				`"ItemProcessor":{"ProcessorConfig":{"Mode":"DISTRIBUTED"},"StartAt":"I","States":{"I":{"Type":"Pass","End":true}}},"End":true}}}`,
		},
		{
			name: "AWS SDK integration over the Query protocol",
			definition: `{"StartAt":"T","States":{"T":{"Type":"Task","Resource":"arn:aws:states:::aws-sdk:iam:listRoles",` +
				`"End":true}}}`,
		},
		{
			name: "unknown intrinsic",
			definition: `{"StartAt":"P","States":{"P":{"Type":"Pass","Parameters":{"h.$":"States.NoSuchFunction($.x)"},` +
				`"End":true}}}`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Given: a state machine using an ASL feature Overcast does not interpret
			srv := helpers.NewTestServer(t)
			smARN := createSM(t, srv, "loud-sm", tc.definition)

			// When: we run it
			execARN := startExec(t, srv, smARN, `{"x":"v","items":[]}`)

			// Then: the execution fails and says what is unsupported
			got := waitForTerminal(t, srv, execARN)
			if got.Status == "SUCCEEDED" {
				t.Fatalf("status = SUCCEEDED; an unsupported feature must never pass through silently (output=%s)", got.Output)
			}
			if got.Error == "" || got.Cause == "" {
				t.Fatalf("error=%q cause=%q — both must be set so a user sees why", got.Error, got.Cause)
			}
			types := eventTypes(execHistory(t, srv, execARN))
			if len(types) == 0 || types[len(types)-1] != "ExecutionFailed" {
				t.Errorf("history = %v, want it to end with ExecutionFailed", types)
			}
		})
	}
}

func TestCreateStateMachine_invalidDefinitionRejected(t *testing.T) {
	// Given: a running server
	srv := helpers.NewTestServer(t)

	// When: we create a state machine whose ASL is structurally invalid
	resp := sfnCall(t, srv, "CreateStateMachine", map[string]any{
		"name":       "broken-sm",
		"definition": `{"StartAt":"Nowhere","States":{"P":{"Type":"Pass","End":true}}}`,
		"roleArn":    "arn:aws:iam::000000000000:role/r",
	})
	defer resp.Body.Close()

	// Then: AWS's InvalidDefinition, not a state machine that cannot run
	helpers.AssertJSONError(t, resp, "InvalidDefinition")
}

// ─── Execution-plane operations ───────────────────────────────────────────────

func TestStartSyncExecution_expressReturnsOutput(t *testing.T) {
	// Given: an EXPRESS state machine
	srv := helpers.NewTestServer(t)
	create := sfnCall(t, srv, "CreateStateMachine", map[string]any{
		"name":       "express-sm",
		"type":       "EXPRESS",
		"definition": `{"StartAt":"P","States":{"P":{"Type":"Pass","Result":"sync","End":true}}}`,
		"roleArn":    "arn:aws:iam::000000000000:role/r",
	})
	defer create.Body.Close()
	helpers.AssertStatus(t, create, http.StatusOK)
	var created struct {
		StateMachineArn string `json:"stateMachineArn"`
	}
	helpers.DecodeJSON(t, create, &created)

	// When: we call StartSyncExecution
	resp := sfnCall(t, srv, "StartSyncExecution", map[string]any{
		"stateMachineArn": created.StateMachineArn,
		"input":           `{}`,
	})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	var out struct {
		Status string `json:"status"`
		Output string `json:"output"`
	}
	helpers.DecodeJSON(t, resp, &out)

	// Then: the same interpreter produced the result inline
	if out.Status != "SUCCEEDED" || out.Output != `"sync"` {
		t.Fatalf("status=%q output=%s", out.Status, out.Output)
	}
}

func TestStartSyncExecution_standardIsRejected(t *testing.T) {
	// Given: a STANDARD state machine
	srv := helpers.NewTestServer(t)
	smARN := createSM(t, srv, "standard-sm", `{"StartAt":"P","States":{"P":{"Type":"Pass","End":true}}}`)

	// When: we call StartSyncExecution against it
	resp := sfnCall(t, srv, "StartSyncExecution", map[string]any{"stateMachineArn": smARN})
	defer resp.Body.Close()

	// Then: AWS's StateMachineTypeNotSupported
	helpers.AssertJSONError(t, resp, "StateMachineTypeNotSupported")
}

func TestDescribeExecution_unknownArn(t *testing.T) {
	// Given: a running server
	srv := helpers.NewTestServer(t)

	// When: we describe an execution that does not exist
	resp := sfnCall(t, srv, "DescribeExecution", map[string]any{
		"executionArn": "arn:aws:states:us-east-1:000000000000:execution:nope:nope",
	})
	defer resp.Body.Close()

	// Then: ExecutionDoesNotExist
	helpers.AssertJSONError(t, resp, "ExecutionDoesNotExist")
	helpers.AssertRequestID(t, resp)
}

func TestStartExecution_duplicateNameRejected(t *testing.T) {
	// Given: an execution that already ran under a chosen name
	srv := helpers.NewTestServer(t)
	smARN := createSM(t, srv, "dup-exec-sm", `{"StartAt":"P","States":{"P":{"Type":"Pass","End":true}}}`)
	first := sfnCall(t, srv, "StartExecution", map[string]any{"stateMachineArn": smARN, "name": "only-once"})
	defer first.Body.Close()
	helpers.AssertStatus(t, first, http.StatusOK)

	// When: we start another execution with the same name
	second := sfnCall(t, srv, "StartExecution", map[string]any{"stateMachineArn": smARN, "name": "only-once"})
	defer second.Body.Close()

	// Then: ExecutionAlreadyExists
	helpers.AssertJSONError(t, second, "ExecutionAlreadyExists")
}

func TestDescribeStateMachineForExecution_returnsDefinition(t *testing.T) {
	// Given: an execution of a known state machine
	srv := helpers.NewTestServer(t)
	definition := `{"StartAt":"P","States":{"P":{"Type":"Pass","End":true}}}`
	smARN := createSM(t, srv, "for-exec-sm", definition)
	execARN := startExec(t, srv, smARN, `{}`)

	// When: we ask which state machine ran it
	resp := sfnCall(t, srv, "DescribeStateMachineForExecution", map[string]any{"executionArn": execARN})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	var out struct {
		StateMachineArn string `json:"stateMachineArn"`
		Definition      string `json:"definition"`
	}
	helpers.DecodeJSON(t, resp, &out)

	// Then: the stored definition comes back
	if out.StateMachineArn != smARN || out.Definition != definition {
		t.Fatalf("stateMachineArn=%q definition=%q", out.StateMachineArn, out.Definition)
	}
}

func TestStopExecution_reportsStopTimeOfFinishedExecution(t *testing.T) {
	// Given: an execution that already completed (runs are synchronous)
	srv := helpers.NewTestServer(t)
	smARN := createSM(t, srv, "stop-sm", `{"StartAt":"P","States":{"P":{"Type":"Pass","End":true}}}`)
	execARN := startExec(t, srv, smARN, `{}`)
	waitForTerminal(t, srv, execARN)

	// When: we stop it
	resp := sfnCall(t, srv, "StopExecution", map[string]any{"executionArn": execARN})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	var out struct {
		StopDate float64 `json:"stopDate"`
	}
	helpers.DecodeJSON(t, resp, &out)

	// Then: it reports a stop time and leaves the terminal status alone
	if out.StopDate <= 0 {
		t.Fatalf("stopDate = %v, want a real timestamp", out.StopDate)
	}
	if got := waitForTerminal(t, srv, execARN); got.Status != "SUCCEEDED" {
		t.Fatalf("status = %q, want the completed status preserved", got.Status)
	}
}

func TestGetExecutionHistory_reverseOrderAndMaxResults(t *testing.T) {
	// Given: an execution with four history events
	srv := helpers.NewTestServer(t)
	smARN := createSM(t, srv, "hist-opts-sm", `{"StartAt":"P","States":{"P":{"Type":"Pass","End":true}}}`)
	execARN := startExec(t, srv, smARN, `{}`)
	waitForTerminal(t, srv, execARN)

	// When: we ask for the newest two, newest first
	resp := sfnCall(t, srv, "GetExecutionHistory", map[string]any{
		"executionArn": execARN,
		"reverseOrder": true,
		"maxResults":   2,
	})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	var out struct {
		Events []historyEvent `json:"events"`
	}
	helpers.DecodeJSON(t, resp, &out)

	// Then: exactly two events, newest first
	if len(out.Events) != 2 {
		t.Fatalf("events = %d, want 2", len(out.Events))
	}
	if out.Events[0].Type != "ExecutionSucceeded" {
		t.Fatalf("first event = %q, want ExecutionSucceeded", out.Events[0].Type)
	}
}

// ─── Local helpers ────────────────────────────────────────────────────────────

func containsEvent(types []string, want string) bool {
	for _, ty := range types {
		if ty == want {
			return true
		}
	}
	return false
}

func createQueue(t *testing.T, srv *helpers.TestServer, name string) string {
	t.Helper()
	resp := awsJSONCall(t, srv, "AmazonSQS.CreateQueue", map[string]any{"QueueName": name})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	var out struct {
		QueueURL string `json:"QueueUrl"`
	}
	helpers.DecodeJSON(t, resp, &out)
	if out.QueueURL == "" {
		t.Fatal("CreateQueue: empty QueueUrl")
	}
	return out.QueueURL
}

func receiveMessage(t *testing.T, srv *helpers.TestServer, queueURL string) string {
	t.Helper()
	resp := awsJSONCall(t, srv, "AmazonSQS.ReceiveMessage", map[string]any{
		"QueueUrl":            queueURL,
		"MaxNumberOfMessages": 1,
	})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	var out struct {
		Messages []struct {
			Body string `json:"Body"`
		} `json:"Messages"`
	}
	helpers.DecodeJSON(t, resp, &out)
	if len(out.Messages) == 0 {
		t.Fatal("ReceiveMessage: no messages on the queue")
	}
	return out.Messages[0].Body
}

func createTopic(t *testing.T, srv *helpers.TestServer, name string) string {
	t.Helper()
	resp, err := http.PostForm(srv.URL+"/", map[string][]string{
		"Action":  {"CreateTopic"},
		"Name":    {name},
		"Version": {"2010-03-31"},
	})
	if err != nil {
		t.Fatalf("CreateTopic: %v", err)
	}
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	var out struct {
		TopicARN string `xml:"CreateTopicResult>TopicArn"`
	}
	helpers.DecodeXML(t, resp, &out)
	if out.TopicARN == "" {
		t.Fatal("CreateTopic: empty TopicArn")
	}
	return out.TopicARN
}

func createTable(t *testing.T, srv *helpers.TestServer, name string) {
	t.Helper()
	resp := awsJSONCall(t, srv, "DynamoDB_20120810.CreateTable", map[string]any{
		"TableName":            name,
		"AttributeDefinitions": []map[string]string{{"AttributeName": "id", "AttributeType": "S"}},
		"KeySchema":            []map[string]string{{"AttributeName": "id", "KeyType": "HASH"}},
		"BillingMode":          "PAY_PER_REQUEST",
	})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
}

// awsJSONCall performs an AWS JSON 1.0 target-dispatch request against any
// service on the test server.
func awsJSONCall(t *testing.T, srv *helpers.TestServer, target string, body map[string]any) *http.Response {
	t.Helper()
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal %s body: %v", target, err)
	}
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/", bytes.NewReader(encoded))
	if err != nil {
		t.Fatalf("build %s request: %v", target, err)
	}
	req.Header.Set("Content-Type", "application/x-amz-json-1.0")
	req.Header.Set("X-Amz-Target", target)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s: %v", target, err)
	}
	return resp
}

// ─── Retry ────────────────────────────────────────────────────────────────────

func TestStartExecution_retryExhaustsThenCatches(t *testing.T) {
	// Given: a Task that always fails, with two retries and then a Catch
	srv := helpers.NewTestServer(t)
	def := `{
	  "StartAt": "Flaky",
	  "States": {
	    "Flaky": {
	      "Type": "Task",
	      "Resource": "arn:aws:states:::lambda:invoke",
	      "Parameters": {"FunctionName": "missing-function"},
	      "Retry": [{"ErrorEquals": ["States.ALL"], "IntervalSeconds": 0, "MaxAttempts": 2, "BackoffRate": 1}],
	      "Catch": [{"ErrorEquals": ["States.ALL"], "Next": "Handled"}],
	      "End": true
	    },
	    "Handled": {"Type": "Pass", "Result": "recovered", "End": true}
	  }
	}`
	smARN := createSM(t, srv, "retry-sm", def)

	// When: we run it
	execARN := startExec(t, srv, smARN, `{}`)

	// Then: the task was attempted three times before the Catch took over
	got := waitForTerminal(t, srv, execARN)
	if got.Status != "SUCCEEDED" || got.Output != `"recovered"` {
		t.Fatalf("status=%q output=%s (error=%q cause=%q)", got.Status, got.Output, got.Error, got.Cause)
	}
	failures := 0
	for _, ty := range eventTypes(execHistory(t, srv, execARN)) {
		if ty == "TaskFailed" {
			failures++
		}
	}
	if failures != 3 {
		t.Fatalf("TaskFailed events = %d, want 3 (initial attempt + 2 retries)", failures)
	}
}

func TestStartExecution_resultSelectorReshapesTaskResult(t *testing.T) {
	// Given: a Task whose ResultSelector picks one field out of the response
	srv := helpers.NewTestServer(t)
	queueURL := createQueue(t, srv, "selector-target")
	def := `{
	  "StartAt": "Send",
	  "States": {
	    "Send": {
	      "Type": "Task",
	      "Resource": "arn:aws:states:::sqs:sendMessage",
	      "Parameters": {"QueueUrl": "` + queueURL + `", "MessageBody": "x"},
	      "ResultSelector": {"sent.$": "$.MessageId"},
	      "ResultPath": "$.sqs",
	      "End": true
	    }
	  }
	}`
	smARN := createSM(t, srv, "selector-sm", def)

	// When: we run it
	execARN := startExec(t, srv, smARN, `{"keep":1}`)

	// Then: the reshaped result was merged into the state input
	got := waitForTerminal(t, srv, execARN)
	if got.Status != "SUCCEEDED" {
		t.Fatalf("status = %q (error=%q cause=%q)", got.Status, got.Error, got.Cause)
	}
	var output struct {
		Keep float64 `json:"keep"`
		SQS  struct {
			Sent string `json:"sent"`
		} `json:"sqs"`
	}
	if err := json.Unmarshal([]byte(got.Output), &output); err != nil {
		t.Fatalf("unmarshal output %q: %v", got.Output, err)
	}
	if output.Keep != 1 || output.SQS.Sent == "" {
		t.Fatalf("output = %s, want keep=1 and sqs.sent set", got.Output)
	}
}

// ─── Asynchronous execution ───────────────────────────────────────────────────

func TestStartExecution_returnsWhileExecutionIsRunning(t *testing.T) {
	// Given: a state machine that waits before finishing
	srv := helpers.NewTestServer(t)
	def := `{
	  "StartAt": "Hold",
	  "States": {
	    "Hold": {"Type": "Wait", "Seconds": 2, "Next": "Done"},
	    "Done": {"Type": "Pass", "Result": "finished", "End": true}
	  }
	}`
	smARN := createSM(t, srv, "async-sm", def)

	// When: we start it
	execARN := startExec(t, srv, smARN, `{}`)

	// Then: StartExecution has already answered while the execution runs —
	// it is not held open for the length of the workflow
	running := describeExec(t, srv, execARN)
	if running.Status != "RUNNING" {
		t.Fatalf("status = %q immediately after StartExecution, want RUNNING", running.Status)
	}
	if running.Output != "" {
		t.Errorf("output = %q on a RUNNING execution, want empty", running.Output)
	}
	// … and its history is already readable, without waiting for it to finish
	types := eventTypes(execHistory(t, srv, execARN))
	if len(types) == 0 || types[0] != "ExecutionStarted" {
		t.Fatalf("history = %v, want it to open with ExecutionStarted", types)
	}

	// And: it reaches its terminal state on its own
	done := waitForTerminal(t, srv, execARN)
	if done.Status != "SUCCEEDED" || done.Output != `"finished"` {
		t.Fatalf("status=%q output=%s (error=%q cause=%q)", done.Status, done.Output, done.Error, done.Cause)
	}
}

func TestStopExecution_abortsRunningExecution(t *testing.T) {
	// Given: an execution parked in a long Wait
	srv := helpers.NewTestServer(t)
	def := `{"StartAt":"Hold","States":{"Hold":{"Type":"Wait","Seconds":300,"End":true}}}`
	smARN := createSM(t, srv, "abort-sm", def)
	execARN := startExec(t, srv, smARN, `{}`)

	// When: we stop it
	resp := sfnCall(t, srv, "StopExecution", map[string]any{
		"executionArn": execARN,
		"error":        "OperatorStop",
		"cause":        "stopped by the test",
	})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)

	// Then: it really is interrupted, and reports why
	got := waitForTerminal(t, srv, execARN)
	if got.Status != "ABORTED" {
		t.Fatalf("status = %q, want ABORTED", got.Status)
	}
	if got.Error != "OperatorStop" || got.Cause != "stopped by the test" {
		t.Errorf("error/cause = %q/%q, want OperatorStop/stopped by the test", got.Error, got.Cause)
	}
	types := eventTypes(execHistory(t, srv, execARN))
	if len(types) == 0 || types[len(types)-1] != "ExecutionAborted" {
		t.Errorf("history = %v, want it to end with ExecutionAborted", types)
	}
}

func TestStartExecution_longWaitSucceeds(t *testing.T) {
	// Given: a Wait longer than a request would tolerate — the case the
	// synchronous design could never have completed
	srv := helpers.NewTestServer(t)
	def := `{
	  "StartAt": "Hold",
	  "States": {
	    "Hold": {"Type": "Wait", "Seconds": 3, "Next": "Done"},
	    "Done": {"Type": "Pass", "Result": "waited", "End": true}
	  }
	}`
	smARN := createSM(t, srv, "long-wait-sm", def)

	// When: we run it
	execARN := startExec(t, srv, smARN, `{}`)

	// Then: it waits and then succeeds, rather than timing out
	got := waitForTerminal(t, srv, execARN)
	if got.Status != "SUCCEEDED" {
		t.Fatalf("status = %q, want SUCCEEDED (error=%q cause=%q)", got.Status, got.Error, got.Cause)
	}
	if got.Output != `"waited"` {
		t.Errorf("output = %s, want \"waited\"", got.Output)
	}
}
