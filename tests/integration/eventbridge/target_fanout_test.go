// Target fan-out integration tests (issue #467).
//
// PutEvents used to deliver to SQS targets and scheduled ECS task targets only:
// a Lambda/SNS/Step Functions/Kinesis/Firehose target was accepted by
// PutTargets and then silently dropped at delivery time. These tests pin the
// delivery behaviour for every target type EventBridge now fans out to, the
// input transformation applied before delivery, the retry/DLQ handling of a
// failed delivery, and the loud PutTargets rejection of a target type the
// emulator cannot deliver to (docs/plans/full-emulation-priority.md §2.1).
package eventbridge_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/overcast-sh/overcast/tests/helpers"
)

// ─── helpers ──────────────────────────────────────────────────────────────────

// jsonCallForEventBridge performs an AWS JSON-protocol X-Amz-Target request.
func jsonCallForEventBridge(t *testing.T, srv *helpers.TestServer, target string, body map[string]any) *http.Response {
	t.Helper()
	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal %s body: %v", target, err)
	}
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/", strings.NewReader(string(payload)))
	if err != nil {
		t.Fatalf("build %s request: %v", target, err)
	}
	req.Header.Set("Content-Type", "application/x-amz-json-1.1")
	req.Header.Set("X-Amz-Target", target)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s: %v", target, err)
	}
	return resp
}

// snsCallForEventBridge performs an AWS Query-protocol SNS request.
func snsCallForEventBridge(t *testing.T, srv *helpers.TestServer, action string, params url.Values) *http.Response {
	t.Helper()
	params.Set("Action", action)
	params.Set("Version", "2010-03-31")
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/", strings.NewReader(params.Encode()))
	if err != nil {
		t.Fatalf("build SNS %s request: %v", action, err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("SNS %s: %v", action, err)
	}
	return resp
}

// putRuleWithTarget creates an ENABLED rule matching source com.example.fanout
// and attaches a single target to it. It fails the test if PutTargets reports
// any failed entry.
func putRuleWithTarget(t *testing.T, srv *helpers.TestServer, rule string, target map[string]any) {
	t.Helper()
	resp := ebCall(t, srv, "PutRule", map[string]any{
		"Name":         rule,
		"EventPattern": `{"source":["com.example.fanout"]}`,
		"State":        "ENABLED",
	})
	resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)

	resp = ebCall(t, srv, "PutTargets", map[string]any{
		"Rule":    rule,
		"Targets": []any{target},
	})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	var out struct {
		FailedEntryCount int              `json:"FailedEntryCount"`
		FailedEntries    []map[string]any `json:"FailedEntries"`
	}
	helpers.DecodeJSON(t, resp, &out)
	if out.FailedEntryCount != 0 {
		t.Fatalf("PutTargets rejected the target: %#v", out.FailedEntries)
	}
}

// putFanoutEvent puts one matching event on the default bus.
func putFanoutEvent(t *testing.T, srv *helpers.TestServer, detail string) {
	t.Helper()
	resp := ebCall(t, srv, "PutEvents", map[string]any{
		"Entries": []any{map[string]any{
			"Source":     "com.example.fanout",
			"DetailType": "FanoutTest",
			"Detail":     detail,
		}},
	})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
}

// receiveOneMessage polls an SQS queue until a message arrives (or fails).
func receiveOneMessage(t *testing.T, srv *helpers.TestServer, queueURL string) string {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		resp := jsonCallForEventBridge(t, srv, "AmazonSQS.ReceiveMessage", map[string]any{
			"QueueUrl":            queueURL,
			"MaxNumberOfMessages": 1,
		})
		helpers.AssertStatus(t, resp, http.StatusOK)
		var out struct {
			Messages []struct {
				Body string `json:"Body"`
			} `json:"Messages"`
		}
		helpers.DecodeJSON(t, resp, &out)
		resp.Body.Close()
		if len(out.Messages) == 1 {
			return out.Messages[0].Body
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("no message delivered to %s within the timeout", queueURL)
	return ""
}

func createQueueForFanout(t *testing.T, srv *helpers.TestServer, name string) string {
	t.Helper()
	resp := jsonCallForEventBridge(t, srv, "AmazonSQS.CreateQueue", map[string]any{"QueueName": name})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	var out struct {
		QueueURL string `json:"QueueUrl"`
	}
	helpers.DecodeJSON(t, resp, &out)
	return out.QueueURL
}

// ─── Lambda target ────────────────────────────────────────────────────────────

func TestPutEvents_deliversToLambdaTarget(t *testing.T) {
	// Given: a Lambda function and a rule targeting it
	srv := helpers.NewTestServer(t)
	fn := map[string]any{
		"name":        "fanout-handler",
		"arn":         "arn:aws:lambda:us-east-1:000000000000:function:fanout-handler",
		"runtime":     "nodejs20.x",
		"handler":     "index.handler",
		"state":       "Active",
		"timeout":     30,
		"memory_size": 128,
	}
	fnJSON, _ := json.Marshal(fn)
	if err := srv.Store.Set(context.Background(), "lambda:functions", "us-east-1/fanout-handler", string(fnJSON)); err != nil {
		t.Fatalf("seed function: %v", err)
	}

	putRuleWithTarget(t, srv, "fanout-lambda", map[string]any{
		"Id":  "fn",
		"Arn": "arn:aws:lambda:us-east-1:000000000000:function:fanout-handler",
	})

	// When: a matching event is published
	putFanoutEvent(t, srv, `{"orderId":"o-1"}`)

	// Then: the function is invoked (the invocation record carries metadata
	// only, so the envelope itself is asserted on the sinks that keep bodies —
	// SQS, SNS and Step Functions below)
	deadline := time.Now().Add(2 * time.Second)
	invoked := false
	for time.Now().Before(deadline) {
		kvs, err := srv.Store.Scan(context.Background(), "lambda:invocations", "us-east-1/fanout-handler:")
		if err == nil && len(kvs) > 0 {
			if !strings.Contains(kvs[0].Value, `"payload_size"`) || strings.Contains(kvs[0].Value, `"payload_size":0`) {
				t.Fatalf("lambda invocation carried no payload: %s", kvs[0].Value)
			}
			invoked = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !invoked {
		t.Fatal("no Lambda invocation recorded — the Lambda target never fired")
	}

	// And: the delivery is reported against the resolved Lambda target type
	rec := waitForDelivery(t, srv, "default", "fanout-lambda", "fn")
	if rec.TargetType != "Lambda" || rec.Outcome != "delivered" {
		t.Fatalf("delivery record = %+v, want a delivered Lambda target", rec)
	}
}

// ─── SNS target ───────────────────────────────────────────────────────────────

func TestPutEvents_deliversToSNSTarget(t *testing.T) {
	// Given: an SNS topic with an SQS subscription, targeted by a rule
	srv := helpers.NewTestServer(t)
	queueURL := createQueueForFanout(t, srv, "fanout-sns-queue")

	resp := snsCallForEventBridge(t, srv, "CreateTopic", url.Values{"Name": {"fanout-topic"}})
	resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	topicARN := "arn:aws:sns:us-east-1:000000000000:fanout-topic"

	resp = snsCallForEventBridge(t, srv, "Subscribe", url.Values{
		"TopicArn": {topicARN},
		"Protocol": {"sqs"},
		"Endpoint": {"arn:aws:sqs:us-east-1:000000000000:fanout-sns-queue"},
	})
	resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)

	putRuleWithTarget(t, srv, "fanout-sns", map[string]any{"Id": "topic", "Arn": topicARN})

	// When: a matching event is published
	putFanoutEvent(t, srv, `{"orderId":"o-2"}`)

	// Then: the subscribed queue receives the event envelope through SNS
	body := receiveOneMessage(t, srv, queueURL)
	if !strings.Contains(body, "com.example.fanout") || !strings.Contains(body, "o-2") {
		t.Fatalf("SNS-delivered message did not carry the event: %s", body)
	}
}

// ─── Step Functions target ────────────────────────────────────────────────────

func TestPutEvents_deliversToStepFunctionsTarget(t *testing.T) {
	// Given: a state machine targeted by a rule
	srv := helpers.NewTestServer(t)
	resp := jsonCallForEventBridge(t, srv, "AWSStepFunctions.CreateStateMachine", map[string]any{
		"name":       "fanout-machine",
		"definition": `{"StartAt":"Done","States":{"Done":{"Type":"Succeed"}}}`,
		"roleArn":    "arn:aws:iam::000000000000:role/sfn",
	})
	resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	smARN := "arn:aws:states:us-east-1:000000000000:stateMachine:fanout-machine"

	putRuleWithTarget(t, srv, "fanout-sfn", map[string]any{"Id": "sm", "Arn": smARN})

	// When: a matching event is published
	putFanoutEvent(t, srv, `{"orderId":"o-3"}`)

	// Then: an execution is started with the event as its input
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		kvs, err := srv.Store.Scan(context.Background(), "stepfunctions", "us-east-1/exec:")
		if err == nil && len(kvs) > 0 {
			if !strings.Contains(kvs[0].Value, "o-3") {
				t.Fatalf("execution input missing the event detail: %s", kvs[0].Value)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("no Step Functions execution started — the Step Functions target never fired")
}

// ─── Kinesis target ───────────────────────────────────────────────────────────

func TestPutEvents_deliversToKinesisTarget(t *testing.T) {
	// Given: a Kinesis stream targeted by a rule, with a partition key path
	srv := helpers.NewTestServer(t)
	resp := jsonCallForEventBridge(t, srv, "Kinesis_20131202.CreateStream", map[string]any{
		"StreamName": "fanout-stream",
		"ShardCount": 1,
	})
	resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)

	putRuleWithTarget(t, srv, "fanout-kinesis", map[string]any{
		"Id":                "stream",
		"Arn":               "arn:aws:kinesis:us-east-1:000000000000:stream/fanout-stream",
		"KinesisParameters": map[string]any{"PartitionKeyPath": "$.detail.orderId"},
	})

	// When: a matching event is published
	putFanoutEvent(t, srv, `{"orderId":"o-4"}`)

	// Then: the stream holds one record carrying the event, keyed by the path
	resp = jsonCallForEventBridge(t, srv, "Kinesis_20131202.GetShardIterator", map[string]any{
		"StreamName":        "fanout-stream",
		"ShardId":           "shardId-000000000000",
		"ShardIteratorType": "TRIM_HORIZON",
	})
	helpers.AssertStatus(t, resp, http.StatusOK)
	var iter struct {
		ShardIterator string `json:"ShardIterator"`
	}
	helpers.DecodeJSON(t, resp, &iter)
	resp.Body.Close()

	resp = jsonCallForEventBridge(t, srv, "Kinesis_20131202.GetRecords", map[string]any{
		"ShardIterator": iter.ShardIterator,
	})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	var records struct {
		Records []struct {
			Data         []byte `json:"Data"`
			PartitionKey string `json:"PartitionKey"`
		} `json:"Records"`
	}
	helpers.DecodeJSON(t, resp, &records)
	if len(records.Records) != 1 {
		t.Fatalf("expected 1 Kinesis record, got %d — the Kinesis target never fired", len(records.Records))
	}
	if !strings.Contains(string(records.Records[0].Data), "com.example.fanout") {
		t.Fatalf("Kinesis record data missing the event: %s", records.Records[0].Data)
	}
	if records.Records[0].PartitionKey != "o-4" {
		t.Fatalf("PartitionKey = %q, want o-4 (resolved from PartitionKeyPath)", records.Records[0].PartitionKey)
	}
}

// ─── Firehose target ──────────────────────────────────────────────────────────

func TestPutEvents_deliversToFirehoseTarget(t *testing.T) {
	// Given: a Firehose delivery stream targeted by a rule
	srv := helpers.NewTestServer(t)
	resp := jsonCallForEventBridge(t, srv, "Firehose_20150804.CreateDeliveryStream", map[string]any{
		"DeliveryStreamName": "fanout-firehose",
	})
	resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)

	putRuleWithTarget(t, srv, "fanout-firehose", map[string]any{
		"Id":  "hose",
		"Arn": "arn:aws:firehose:us-east-1:000000000000:deliverystream/fanout-firehose",
	})

	// When: a matching event is published
	putFanoutEvent(t, srv, `{"orderId":"o-5"}`)

	// Then: the delivery is recorded as delivered
	rec := waitForDelivery(t, srv, "default", "fanout-firehose", "hose")
	if rec.Outcome != "delivered" {
		t.Fatalf("Firehose delivery outcome = %q (%s), want delivered", rec.Outcome, rec.Error)
	}
	if rec.TargetType != "Firehose" {
		t.Fatalf("TargetType = %q, want Firehose", rec.TargetType)
	}
}

// ─── PutTargets validation (§2.1 fidelity rule) ───────────────────────────────

func TestPutTargets_rejectsUnsupportedTargetType(t *testing.T) {
	// Given: a rule
	srv := helpers.NewTestServer(t)
	resp := ebCall(t, srv, "PutRule", map[string]any{
		"Name": "unsupported-target", "EventPattern": `{"source":["com.example.targets"]}`,
	})
	resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)

	// When: a target type the emulator cannot deliver to is added
	resp = ebCall(t, srv, "PutTargets", map[string]any{
		"Rule": "unsupported-target",
		"Targets": []any{map[string]any{
			"Id":  "logs",
			"Arn": "arn:aws:logs:us-east-1:000000000000:log-group:/aws/events/demo",
		}},
	})
	defer resp.Body.Close()

	// Then: it fails loudly rather than being accepted and silently dropped
	helpers.AssertStatus(t, resp, http.StatusOK)
	var out struct {
		FailedEntryCount int `json:"FailedEntryCount"`
		FailedEntries    []struct {
			TargetID     string `json:"TargetId"`
			ErrorCode    string `json:"ErrorCode"`
			ErrorMessage string `json:"ErrorMessage"`
		} `json:"FailedEntries"`
	}
	helpers.DecodeJSON(t, resp, &out)
	if out.FailedEntryCount != 1 || len(out.FailedEntries) != 1 {
		t.Fatalf("FailedEntryCount = %d, FailedEntries = %#v; want one rejected entry", out.FailedEntryCount, out.FailedEntries)
	}
	if out.FailedEntries[0].TargetID != "logs" {
		t.Fatalf("FailedEntries[0].TargetId = %q, want logs", out.FailedEntries[0].TargetID)
	}
	if out.FailedEntries[0].ErrorCode != "UnsupportedTargetType" {
		t.Fatalf("ErrorCode = %q, want UnsupportedTargetType", out.FailedEntries[0].ErrorCode)
	}

	// And: the rejected target is not stored
	resp2 := ebCall(t, srv, "ListTargetsByRule", map[string]any{"Rule": "unsupported-target"})
	defer resp2.Body.Close()
	var listed struct {
		Targets []map[string]any `json:"Targets"`
	}
	helpers.DecodeJSON(t, resp2, &listed)
	if len(listed.Targets) != 0 {
		t.Fatalf("rejected target was stored anyway: %#v", listed.Targets)
	}
}

func TestPutTargets_rejectsMalformedTargetARN(t *testing.T) {
	// Given: a rule
	srv := helpers.NewTestServer(t)
	resp := ebCall(t, srv, "PutRule", map[string]any{
		"Name": "bad-arn", "EventPattern": `{"source":["com.example.targets"]}`,
	})
	resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)

	// When: a target with a malformed ARN is added
	resp = ebCall(t, srv, "PutTargets", map[string]any{
		"Rule":    "bad-arn",
		"Targets": []any{map[string]any{"Id": "t1", "Arn": "not-an-arn"}},
	})
	defer resp.Body.Close()

	// Then: AWS's synchronous ARN-shape check rejects the whole call
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	var errOut struct {
		Type    string `json:"__type"`
		Message string `json:"message"`
	}
	helpers.DecodeJSON(t, resp, &errOut)
	if !strings.Contains(errOut.Type, "ValidationException") {
		t.Fatalf("error type = %q, want ValidationException", errOut.Type)
	}
}

func TestPutTargets_rejectsMultipleInputSpecifications(t *testing.T) {
	// Given: a rule
	srv := helpers.NewTestServer(t)
	resp := ebCall(t, srv, "PutRule", map[string]any{
		"Name": "double-input", "EventPattern": `{"source":["com.example.targets"]}`,
	})
	resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)

	// When: a target sets both Input and InputPath
	resp = ebCall(t, srv, "PutTargets", map[string]any{
		"Rule": "double-input",
		"Targets": []any{map[string]any{
			"Id":        "t1",
			"Arn":       "arn:aws:sqs:us-east-1:000000000000:q",
			"Input":     `{"a":1}`,
			"InputPath": "$.detail",
		}},
	})
	defer resp.Body.Close()

	// Then: AWS rejects the call outright
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
}

// ─── Input transformation ─────────────────────────────────────────────────────

func TestPutEvents_inputPathAppliesBeforeDelivery(t *testing.T) {
	// Given: an SQS target with an InputPath selecting the detail
	srv := helpers.NewTestServer(t)
	queueURL := createQueueForFanout(t, srv, "fanout-inputpath")
	putRuleWithTarget(t, srv, "fanout-inputpath", map[string]any{
		"Id":        "q",
		"Arn":       "arn:aws:sqs:us-east-1:000000000000:fanout-inputpath",
		"InputPath": "$.detail",
	})

	// When: a matching event is published
	putFanoutEvent(t, srv, `{"orderId":"o-6"}`)

	// Then: only the selected node is delivered
	body := receiveOneMessage(t, srv, queueURL)
	var got map[string]any
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("delivered body is not JSON: %s", body)
	}
	if got["orderId"] != "o-6" || len(got) != 1 {
		t.Fatalf("InputPath did not select $.detail: %s", body)
	}
}

func TestPutEvents_inputTransformerAppliesBeforeDelivery(t *testing.T) {
	// Given: an SQS target with an InputTransformer
	srv := helpers.NewTestServer(t)
	queueURL := createQueueForFanout(t, srv, "fanout-transformer")
	putRuleWithTarget(t, srv, "fanout-transformer", map[string]any{
		"Id":  "q",
		"Arn": "arn:aws:sqs:us-east-1:000000000000:fanout-transformer",
		"InputTransformer": map[string]any{
			"InputPathsMap": map[string]any{
				"order":  "$.detail.orderId",
				"origin": "$.source",
			},
			"InputTemplate": `{"id":"<order>","from":"<origin>"}`,
		},
	})

	// When: a matching event is published
	putFanoutEvent(t, srv, `{"orderId":"o-7"}`)

	// Then: the rendered template is delivered
	body := receiveOneMessage(t, srv, queueURL)
	var got map[string]any
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("delivered body is not JSON: %s", body)
	}
	if got["id"] != "o-7" || got["from"] != "com.example.fanout" {
		t.Fatalf("InputTransformer output = %s", body)
	}
}

// ─── Retry / DLQ ──────────────────────────────────────────────────────────────

func TestPutEvents_failedDeliveryGoesToDeadLetterQueue(t *testing.T) {
	// Given: a Lambda target that does not exist, with a DLQ configured
	srv := helpers.NewTestServer(t)
	dlqURL := createQueueForFanout(t, srv, "fanout-dlq")

	putRuleWithTarget(t, srv, "fanout-dlq-rule", map[string]any{
		"Id":               "missing",
		"Arn":              "arn:aws:lambda:us-east-1:000000000000:function:does-not-exist",
		"RetryPolicy":      map[string]any{"MaximumRetryAttempts": 2},
		"DeadLetterConfig": map[string]any{"Arn": "arn:aws:sqs:us-east-1:000000000000:fanout-dlq"},
	})

	// When: a matching event is published
	putFanoutEvent(t, srv, `{"orderId":"o-8"}`)

	// Then: the undeliverable event lands on the dead-letter queue
	body := receiveOneMessage(t, srv, dlqURL)
	if !strings.Contains(body, "o-8") {
		t.Fatalf("DLQ message did not carry the event: %s", body)
	}

	// And: the delivery is reported as DLQ'd after the configured retries
	rec := waitForDelivery(t, srv, "default", "fanout-dlq-rule", "missing")
	if rec.Outcome != "dlq" {
		t.Fatalf("outcome = %q, want dlq", rec.Outcome)
	}
	if rec.Attempts != 3 {
		t.Fatalf("attempts = %d, want 3 (initial + MaximumRetryAttempts)", rec.Attempts)
	}
}

func TestPutEvents_failedDeliveryWithoutDLQIsDropped(t *testing.T) {
	// Given: a rule whose only target is a queue that does not exist
	srv := helpers.NewTestServer(t)
	putRuleWithTarget(t, srv, "fanout-drop", map[string]any{
		"Id":  "gone",
		"Arn": "arn:aws:sqs:us-east-1:000000000000:no-such-queue",
	})

	// When: a matching event is published
	putFanoutEvent(t, srv, `{"orderId":"o-9"}`)

	// Then: PutEvents still succeeds and the delivery is reported as dropped
	rec := waitForDelivery(t, srv, "default", "fanout-drop", "gone")
	if rec.Outcome != "dropped" {
		t.Fatalf("outcome = %q, want dropped", rec.Outcome)
	}
	if rec.Error == "" {
		t.Fatal("expected the dropped delivery to record why it failed")
	}
}

// ─── Delivery visibility endpoint (web UI) ────────────────────────────────────

type fanoutDelivery struct {
	Rule       string `json:"Rule"`
	TargetID   string `json:"TargetId"`
	TargetARN  string `json:"TargetArn"`
	TargetType string `json:"TargetType"`
	Outcome    string `json:"Outcome"`
	Attempts   int    `json:"Attempts"`
	Error      string `json:"Error"`
	EventID    string `json:"EventId"`
}

// waitForDelivery polls the delivery-visibility endpoint until an outcome for
// the given rule/target appears.
func waitForDelivery(t *testing.T, srv *helpers.TestServer, bus, rule, targetID string) fanoutDelivery {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(srv.URL + "/_overcast/eventbridge/deliveries?bus=" + url.QueryEscape(bus))
		if err != nil {
			t.Fatalf("GET deliveries: %v", err)
		}
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			t.Fatalf("GET deliveries: HTTP %d", resp.StatusCode)
		}
		var out struct {
			Deliveries []fanoutDelivery `json:"Deliveries"`
		}
		helpers.DecodeJSON(t, resp, &out)
		resp.Body.Close()
		for _, d := range out.Deliveries {
			if d.Rule == rule && d.TargetID == targetID {
				return d
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("no delivery outcome recorded for rule %q target %q", rule, targetID)
	return fanoutDelivery{}
}

func TestDeliveriesEndpoint_reportsResolvedTargetTypes(t *testing.T) {
	// Given: an SQS target that delivers successfully
	srv := helpers.NewTestServer(t)
	createQueueForFanout(t, srv, "fanout-visible")
	putRuleWithTarget(t, srv, "fanout-visible-rule", map[string]any{
		"Id":  "q",
		"Arn": "arn:aws:sqs:us-east-1:000000000000:fanout-visible",
	})

	// When: a matching event is published
	putFanoutEvent(t, srv, `{"orderId":"o-10"}`)

	// Then: the delivery record names the resolved target type and outcome
	rec := waitForDelivery(t, srv, "default", "fanout-visible-rule", "q")
	if rec.TargetType != "SQS" {
		t.Fatalf("TargetType = %q, want SQS", rec.TargetType)
	}
	if rec.Outcome != "delivered" {
		t.Fatalf("Outcome = %q (%s), want delivered", rec.Outcome, rec.Error)
	}
	if rec.Attempts != 1 {
		t.Fatalf("Attempts = %d, want 1", rec.Attempts)
	}
	if rec.EventID == "" {
		t.Fatal("expected the delivery record to carry the EventId")
	}
	if !strings.HasPrefix(rec.TargetARN, "arn:aws:sqs:") {
		t.Fatalf("TargetArn = %q", rec.TargetARN)
	}
}
