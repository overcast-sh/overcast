// Step Functions Execution Status Change, delivered onto the default
// EventBridge bus the way real Step Functions does. See:
// https://docs.aws.amazon.com/step-functions/latest/dg/eventbridge-integration.html#event-detail-execution-status-change
//
// AWS's own detail also carries the redrive* fields — omitted here rather
// than invented, per internal/services/stepfunctions/eventbridge.go's doc
// comment, since Overcast does not implement execution redrive. These tests
// only assert on the fields Overcast does track; stateMachineVersionArn and
// stateMachineAliasArn are covered by that package's unit tests. This is the remainder of #758 (#1221) after EC2 and ECS shipped in
// #1225.
package stepfunctions_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/overcast-sh/overcast/tests/helpers"
)

// ---- EventBridge/SQS wire helpers (package-local; see the same pattern in
// tests/integration/ecs/ecs_eventbridge_test.go,
// tests/integration/s3/s3_eventbridge_notification_test.go and
// tests/integration/eventbridge/eventbridge_test.go) --------------------------

func eventBridgeCallForSFN(t *testing.T, srv *helpers.TestServer, operation string, body map[string]any) *http.Response {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal EventBridge %s body: %v", operation, err)
	}
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/x-amz-json-1.1")
	req.Header.Set("X-Amz-Target", "AWSEvents."+operation)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("EventBridge %s: %v", operation, err)
	}
	return resp
}

func sqsCallForSFNEventBridge(t *testing.T, srv *helpers.TestServer, operation string, body map[string]any) *http.Response {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal SQS %s body: %v", operation, err)
	}
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/x-amz-json-1.0")
	req.Header.Set("X-Amz-Target", "AmazonSQS."+operation)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("SQS %s: %v", operation, err)
	}
	return resp
}

func createQueueForSFNEventBridge(t *testing.T, srv *helpers.TestServer, name string) string {
	t.Helper()
	resp := sqsCallForSFNEventBridge(t, srv, "CreateQueue", map[string]any{"QueueName": name})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	var out struct {
		QueueURL string `json:"QueueUrl"`
	}
	helpers.DecodeJSON(t, resp, &out)
	return out.QueueURL
}

func putSFNEventBridgeRule(t *testing.T, srv *helpers.TestServer, rule, pattern, targetARN string) {
	t.Helper()
	ruleResp := eventBridgeCallForSFN(t, srv, "PutRule", map[string]any{
		"Name":         rule,
		"EventPattern": pattern,
	})
	ruleResp.Body.Close()
	helpers.AssertStatus(t, ruleResp, http.StatusOK)

	targetResp := eventBridgeCallForSFN(t, srv, "PutTargets", map[string]any{
		"Rule":    rule,
		"Targets": []any{map[string]any{"Id": "queue", "Arn": targetARN}},
	})
	targetResp.Body.Close()
	helpers.AssertStatus(t, targetResp, http.StatusOK)
}

type sfnEventBridgeEnvelope struct {
	Source     string   `json:"source"`
	DetailType string   `json:"detail-type"`
	Resources  []string `json:"resources"`
	Detail     struct {
		ExecutionArn    string `json:"executionArn"`
		StateMachineArn string `json:"stateMachineArn"`
		Name            string `json:"name"`
		Status          string `json:"status"`
		StartDate       int64  `json:"startDate"`
		StopDate        *int64 `json:"stopDate"`
		Input           string `json:"input"`
		InputDetails    struct {
			Included bool `json:"included"`
		} `json:"inputDetails"`
		Output        *string `json:"output"`
		OutputDetails *struct {
			Included bool `json:"included"`
		} `json:"outputDetails"`
		Error *string `json:"error"`
		Cause *string `json:"cause"`
	} `json:"detail"`
}

// receiveSFNEventBridgeMessages polls the queue a few times for up to
// maxCount currently-available messages. Execution completion happens on a
// background goroutine (StartExecution returns while RUNNING, as AWS does),
// so — unlike ECS's synchronous store writes — delivery is not guaranteed to
// have landed the instant the HTTP call before it returns; the retries here
// give the goroutine's terminal PutExecution a chance to land without a long
// fixed sleep.
func receiveSFNEventBridgeMessages(t *testing.T, srv *helpers.TestServer, queueURL string, want int) []sfnEventBridgeEnvelope {
	t.Helper()
	var envelopes []sfnEventBridgeEnvelope
	deadline := time.Now().Add(20 * time.Second)
	for {
		resp := sqsCallForSFNEventBridge(t, srv, "ReceiveMessage", map[string]any{
			"QueueUrl":            queueURL,
			"MaxNumberOfMessages": 10,
		})
		helpers.AssertStatus(t, resp, http.StatusOK)
		var out struct {
			Messages []struct {
				Body string `json:"Body"`
			} `json:"Messages"`
		}
		helpers.DecodeJSON(t, resp, &out)
		resp.Body.Close()

		for _, m := range out.Messages {
			var e sfnEventBridgeEnvelope
			if err := json.Unmarshal([]byte(m.Body), &e); err != nil {
				t.Fatalf("delivered event is not valid JSON (%q): %v", m.Body, err)
			}
			envelopes = append(envelopes, e)
		}
		if len(envelopes) >= want {
			return envelopes
		}
		if time.Now().After(deadline) {
			t.Fatalf("expected %d delivered event(s), got %d after 20s: %+v", want, len(envelopes), envelopes)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// findByStatus returns the delivered envelope whose detail.status matches,
// and fails the test if it is missing or duplicated.
//
// This must not assume delivery order: AWS's own docs for this event say
// rule matches are delivered "on a best-effort basis" and "events might be
// delivered out of order"
// (https://docs.aws.amazon.com/step-functions/latest/dg/eventbridge-integration.html#supported-events).
// Overcast's SQS emulator is faithful to that for a standard (non-FIFO)
// queue — receiveCandidates deliberately does not sort standard-queue
// messages (see internal/services/sqs/message_backend.go), so ReceiveMessage
// can return them in either order.
func findByStatus(t *testing.T, envelopes []sfnEventBridgeEnvelope, status string) sfnEventBridgeEnvelope {
	t.Helper()
	var found []sfnEventBridgeEnvelope
	for _, e := range envelopes {
		if e.Detail.Status == status {
			found = append(found, e)
		}
	}
	if len(found) != 1 {
		t.Fatalf("expected exactly 1 delivered event with status %q, got %d: %+v", status, len(found), envelopes)
	}
	return found[0]
}

func TestStartExecution_deliversRunningThenSucceededToEventBridge(t *testing.T) {
	// Given: a rule matching Step Functions' execution status-change event,
	// targeting an SQS queue, and a state machine that succeeds
	srv := helpers.NewTestServer(t)
	queueURL := createQueueForSFNEventBridge(t, srv, "sfn-run-queue")
	putSFNEventBridgeRule(t, srv, "sfn-execution-status-change",
		`{"source":["aws.states"],"detail-type":["Step Functions Execution Status Change"]}`,
		"arn:aws:sqs:us-east-1:000000000000:sfn-run-queue")

	def := `{"StartAt":"Hello","States":{"Hello":{"Type":"Pass","Result":"world","End":true}}}`
	smARN := createSM(t, srv, "eb-succeed-sm", def)

	// When: StartExecution runs the machine to completion
	execARN := startExec(t, srv, smARN, `{"key":"value"}`)
	got := waitForTerminal(t, srv, execARN)
	if got.Status != "SUCCEEDED" {
		t.Fatalf("status = %q, want SUCCEEDED (error=%q cause=%q)", got.Status, got.Error, got.Cause)
	}

	// Then: EventBridge delivers exactly two events — the initial RUNNING
	// transition and the terminal SUCCEEDED one. Delivery order is not
	// guaranteed (see findByStatus), so each is located by its own
	// detail.status rather than by position.
	envelopes := receiveSFNEventBridgeMessages(t, srv, queueURL, 2)
	if len(envelopes) != 2 {
		t.Fatalf("expected 2 delivered events, got %d: %+v", len(envelopes), envelopes)
	}
	running := findByStatus(t, envelopes, "RUNNING")
	succeeded := findByStatus(t, envelopes, "SUCCEEDED")

	for _, e := range []sfnEventBridgeEnvelope{running, succeeded} {
		if e.Source != "aws.states" {
			t.Errorf("source = %q, want aws.states", e.Source)
		}
		if e.DetailType != "Step Functions Execution Status Change" {
			t.Errorf("detail-type = %q, want Step Functions Execution Status Change", e.DetailType)
		}
		if len(e.Resources) != 1 || e.Resources[0] != execARN {
			t.Errorf("resources = %v, want [%s]", e.Resources, execARN)
		}
		if e.Detail.ExecutionArn != execARN {
			t.Errorf("detail.executionArn = %q, want %q", e.Detail.ExecutionArn, execARN)
		}
		if e.Detail.StateMachineArn != smARN {
			t.Errorf("detail.stateMachineArn = %q, want %q", e.Detail.StateMachineArn, smARN)
		}
		if e.Detail.Input != `{"key":"value"}` {
			t.Errorf("detail.input = %q, want the original input", e.Detail.Input)
		}
		if !e.Detail.InputDetails.Included {
			t.Errorf("detail.inputDetails.included = false, want true")
		}
		if e.Detail.StartDate <= 0 {
			t.Errorf("detail.startDate = %d, want > 0", e.Detail.StartDate)
		}
	}

	if running.Detail.StopDate != nil {
		t.Errorf("RUNNING event stopDate = %v, want nil", running.Detail.StopDate)
	}
	if running.Detail.Output != nil || running.Detail.OutputDetails != nil {
		t.Errorf("RUNNING event output/outputDetails = %v/%v, want nil/nil", running.Detail.Output, running.Detail.OutputDetails)
	}

	if succeeded.Detail.StopDate == nil || *succeeded.Detail.StopDate <= 0 {
		t.Errorf("SUCCEEDED event stopDate = %v, want a positive timestamp", succeeded.Detail.StopDate)
	}
	if succeeded.Detail.Output == nil || *succeeded.Detail.Output != `"world"` {
		t.Errorf("SUCCEEDED event output = %v, want \"world\"", succeeded.Detail.Output)
	}
	if succeeded.Detail.OutputDetails == nil || !succeeded.Detail.OutputDetails.Included {
		t.Errorf("SUCCEEDED event outputDetails = %v, want {included:true}", succeeded.Detail.OutputDetails)
	}
	if succeeded.Detail.Error != nil || succeeded.Detail.Cause != nil {
		t.Errorf("SUCCEEDED event error/cause = %v/%v, want nil/nil", succeeded.Detail.Error, succeeded.Detail.Cause)
	}
}

func TestStartExecution_deliversFailedWithErrorAndCauseToEventBridge(t *testing.T) {
	// Given: a rule matching the event, and a state machine that always fails
	srv := helpers.NewTestServer(t)
	queueURL := createQueueForSFNEventBridge(t, srv, "sfn-fail-queue")
	putSFNEventBridgeRule(t, srv, "sfn-execution-status-change-fail",
		`{"source":["aws.states"],"detail-type":["Step Functions Execution Status Change"]}`,
		"arn:aws:sqs:us-east-1:000000000000:sfn-fail-queue")

	def := `{"StartAt":"Boom","States":{"Boom":{"Type":"Fail","Error":"MyError","Cause":"it broke"}}}`
	smARN := createSM(t, srv, "eb-fail-sm", def)

	// When: the execution runs to its terminal FAILED state
	execARN := startExec(t, srv, smARN, `{}`)
	got := waitForTerminal(t, srv, execARN)
	if got.Status != "FAILED" {
		t.Fatalf("status = %q, want FAILED", got.Status)
	}

	// Then: EventBridge delivers a RUNNING and a FAILED event (delivery order
	// is not guaranteed — see findByStatus), and the FAILED event carries the
	// declared error/cause — AWS emits on every status, not just SUCCEEDED.
	envelopes := receiveSFNEventBridgeMessages(t, srv, queueURL, 2)
	if len(envelopes) != 2 {
		t.Fatalf("expected 2 delivered events, got %d: %+v", len(envelopes), envelopes)
	}
	findByStatus(t, envelopes, "RUNNING")
	failed := findByStatus(t, envelopes, "FAILED")
	if failed.Detail.ExecutionArn != execARN {
		t.Errorf("detail.executionArn = %q, want %q", failed.Detail.ExecutionArn, execARN)
	}
	if failed.Detail.Error == nil || *failed.Detail.Error != "MyError" {
		t.Errorf("detail.error = %v, want MyError", failed.Detail.Error)
	}
	if failed.Detail.Cause == nil || *failed.Detail.Cause != "it broke" {
		t.Errorf("detail.cause = %v, want %q", failed.Detail.Cause, "it broke")
	}
	if failed.Detail.Output != nil || failed.Detail.OutputDetails != nil {
		t.Errorf("FAILED event output/outputDetails = %v/%v, want nil/nil", failed.Detail.Output, failed.Detail.OutputDetails)
	}
	if failed.Detail.StopDate == nil || *failed.Detail.StopDate <= 0 {
		t.Errorf("FAILED event stopDate = %v, want a positive timestamp", failed.Detail.StopDate)
	}
}

func TestSFNExecutionStatusChangeRule_filtersOnDetailStatus(t *testing.T) {
	// Given: a rule matching only status=FAILED — proving the delivered
	// detail is real enough for EventBridge's own content filtering to match
	// on it, not just detail-type
	srv := helpers.NewTestServer(t)
	queueURL := createQueueForSFNEventBridge(t, srv, "sfn-failed-only-queue")
	putSFNEventBridgeRule(t, srv, "sfn-failed-only",
		`{"source":["aws.states"],"detail-type":["Step Functions Execution Status Change"],"detail":{"status":["FAILED"]}}`,
		"arn:aws:sqs:us-east-1:000000000000:sfn-failed-only-queue")

	def := `{"StartAt":"Boom","States":{"Boom":{"Type":"Fail","Error":"E","Cause":"C"}}}`
	smARN := createSM(t, srv, "eb-filter-sm", def)

	// When: the execution runs (RUNNING then FAILED occur; only FAILED
	// matches the rule's pattern)
	execARN := startExec(t, srv, smARN, `{}`)
	waitForTerminal(t, srv, execARN)

	// Then: exactly one delivery, and it is the FAILED status
	envelopes := receiveSFNEventBridgeMessages(t, srv, queueURL, 1)
	if len(envelopes) != 1 {
		t.Fatalf("expected exactly 1 delivered event (FAILED only), got %d: %+v", len(envelopes), envelopes)
	}
	if envelopes[0].Detail.Status != "FAILED" {
		t.Errorf("delivered status = %q, want FAILED", envelopes[0].Detail.Status)
	}
}
