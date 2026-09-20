// Package eventbridge_test contains integration tests for the EventBridge emulator.
//
// Run: go test ./tests/integration/eventbridge/...
package eventbridge_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/fxamacker/cbor/v2"

	"github.com/overcast-sh/overcast/tests/helpers"
)

// ebCall performs an EventBridge X-Amz-Target dispatch request.
func ebCall(t *testing.T, srv *helpers.TestServer, operation string, body map[string]any) *http.Response {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal %s body: %v", operation, err)
	}
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/x-amz-json-1.1")
	req.Header.Set("X-Amz-Target", "AWSEvents."+operation)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("ebCall %s: %v", operation, err)
	}
	return resp
}

func sqsCallForEventBridge(t *testing.T, srv *helpers.TestServer, operation string, body map[string]any) *http.Response {
	t.Helper()
	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal SQS %s body: %v", operation, err)
	}
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/", bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("build SQS %s request: %v", operation, err)
	}
	req.Header.Set("Content-Type", "application/x-amz-json-1.0")
	req.Header.Set("X-Amz-Target", "AmazonSQS."+operation)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("SQS %s: %v", operation, err)
	}
	return resp
}

func ecsCallForEventBridge(t *testing.T, srv *helpers.TestServer, operation string, body map[string]any) *http.Response {
	t.Helper()
	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal ECS %s body: %v", operation, err)
	}
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/", bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("build ECS %s request: %v", operation, err)
	}
	req.Header.Set("Content-Type", "application/x-amz-json-1.1")
	req.Header.Set("X-Amz-Target", "AmazonEC2ContainerServiceV20141113."+operation)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("ECS %s: %v", operation, err)
	}
	return resp
}

func ebCBORCall(t *testing.T, srv *helpers.TestServer, operation string, body map[string]any) *http.Response {
	t.Helper()
	payload, err := cbor.Marshal(body)
	if err != nil {
		t.Fatalf("marshal CBOR %s body: %v", operation, err)
	}
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/service/EventBridge/operation/"+operation, bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("build CBOR %s request: %v", operation, err)
	}
	req.Header.Set("Content-Type", "application/cbor")
	req.Header.Set("Smithy-Protocol", "rpc-v2-cbor")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("ebCBORCall %s: %v", operation, err)
	}
	return resp
}

// ─── CreateEventBus ───────────────────────────────────────────────────────────

func TestCreateEventBus_success(t *testing.T) {
	// Given: an empty store
	srv := helpers.NewTestServer(t)

	// When: CreateEventBus is called
	resp := ebCall(t, srv, "CreateEventBus", map[string]any{
		"Name": "test-bus",
	})
	defer resp.Body.Close()

	// Then: 200 with EventBusArn
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		EventBusArn string `json:"EventBusArn"`
	}
	helpers.DecodeJSON(t, resp, &result)
	if result.EventBusArn == "" {
		t.Error("expected EventBusArn to be set")
	}
}

func TestRPCv2CBOR_EventBusAndRuleLifecycle(t *testing.T) {
	srv := helpers.NewTestServer(t)

	resp := ebCBORCall(t, srv, "CreateEventBus", map[string]any{
		"Name": "cbor-bus",
	})
	helpers.AssertStatus(t, resp, http.StatusOK)
	var created struct {
		EventBusArn string `cbor:"EventBusArn"`
	}
	if err := cbor.NewDecoder(resp.Body).Decode(&created); err != nil {
		t.Fatalf("decode CBOR CreateEventBus response: %v", err)
	}
	resp.Body.Close()
	if created.EventBusArn == "" {
		t.Fatal("expected EventBusArn")
	}

	resp = ebCBORCall(t, srv, "PutRule", map[string]any{
		"Name":         "cbor-rule",
		"EventBusName": "cbor-bus",
		"EventPattern": `{"source":["overcast.test"]}`,
		"State":        "ENABLED",
	})
	helpers.AssertStatus(t, resp, http.StatusOK)
	var ruleOut struct {
		RuleArn string `cbor:"RuleArn"`
	}
	if err := cbor.NewDecoder(resp.Body).Decode(&ruleOut); err != nil {
		t.Fatalf("decode CBOR PutRule response: %v", err)
	}
	resp.Body.Close()
	if ruleOut.RuleArn == "" {
		t.Fatal("expected RuleArn")
	}

	resp = ebCBORCall(t, srv, "ListRules", map[string]any{
		"EventBusName": "cbor-bus",
	})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	helpers.AssertHeader(t, resp, "Content-Type", "application/cbor")

	var rules struct {
		Rules []struct {
			Name  string `cbor:"Name"`
			State string `cbor:"State"`
		} `cbor:"Rules"`
	}
	if err := cbor.NewDecoder(resp.Body).Decode(&rules); err != nil {
		t.Fatalf("decode CBOR ListRules response: %v", err)
	}
	if len(rules.Rules) != 1 {
		t.Fatalf("expected 1 rule, got %d", len(rules.Rules))
	}
	if rules.Rules[0].Name != "cbor-rule" {
		t.Fatalf("rule name = %q", rules.Rules[0].Name)
	}
}

func TestRPCv2CBOR_TargetsRoundTrip(t *testing.T) {
	srv := helpers.NewTestServer(t)

	resp := ebCBORCall(t, srv, "PutRule", map[string]any{
		"Name":         "cbor-target-rule",
		"EventPattern": `{"source":["overcast.test"]}`,
	})
	helpers.AssertStatus(t, resp, http.StatusOK)
	resp.Body.Close()

	resp = ebCBORCall(t, srv, "PutTargets", map[string]any{
		"Rule": "cbor-target-rule",
		"Targets": []map[string]any{
			{"Id": "lambda", "Arn": "arn:aws:lambda:us-east-1:000000000000:function:test"},
		},
	})
	helpers.AssertStatus(t, resp, http.StatusOK)
	var putOut struct {
		FailedEntryCount int   `cbor:"FailedEntryCount"`
		FailedEntries    []any `cbor:"FailedEntries"`
	}
	if err := cbor.NewDecoder(resp.Body).Decode(&putOut); err != nil {
		t.Fatalf("decode CBOR PutTargets response: %v", err)
	}
	resp.Body.Close()
	if putOut.FailedEntryCount != 0 {
		t.Fatalf("FailedEntryCount = %d", putOut.FailedEntryCount)
	}
	if len(putOut.FailedEntries) != 0 {
		t.Fatalf("FailedEntries = %#v, want empty", putOut.FailedEntries)
	}

	resp = ebCBORCall(t, srv, "ListTargetsByRule", map[string]any{
		"Rule": "cbor-target-rule",
	})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	var targets struct {
		Targets []struct {
			ID  string `cbor:"Id"`
			ARN string `cbor:"Arn"`
		} `cbor:"Targets"`
	}
	if err := cbor.NewDecoder(resp.Body).Decode(&targets); err != nil {
		t.Fatalf("decode CBOR ListTargetsByRule response: %v", err)
	}
	if len(targets.Targets) != 1 {
		t.Fatalf("expected 1 target, got %d", len(targets.Targets))
	}
	if targets.Targets[0].ID != "lambda" {
		t.Fatalf("target ID = %q", targets.Targets[0].ID)
	}
}

func TestPutTargets_ecsFargateTargetRoundTrip(t *testing.T) {
	// Given: a rule exists for a scheduled ECS task target
	srv := helpers.NewTestServer(t)
	resp := ebCall(t, srv, "PutRule", map[string]any{
		"Name":               "scheduled-fargate",
		"ScheduleExpression": "rate(5 minutes)",
	})
	resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)

	target := map[string]any{
		"Id":      "ecs-task",
		"Arn":     "arn:aws:ecs:us-east-1:000000000000:cluster/app",
		"RoleArn": "arn:aws:iam::000000000000:role/events-run-task",
		"Input":   `{"source":"scheduled"}`,
		"EcsParameters": map[string]any{
			"TaskDefinitionArn": "arn:aws:ecs:us-east-1:000000000000:task-definition/app:1",
			"LaunchType":        "FARGATE",
			"PlatformVersion":   "LATEST",
			"TaskCount":         float64(1),
			"NetworkConfiguration": map[string]any{
				"awsvpcConfiguration": map[string]any{
					"Subnets":        []any{"subnet-private-a", "subnet-private-b"},
					"SecurityGroups": []any{"sg-app"},
					"AssignPublicIp": "DISABLED",
				},
			},
		},
	}

	// When: PutTargets stores the ECS target payload
	resp = ebCall(t, srv, "PutTargets", map[string]any{
		"Rule":    "scheduled-fargate",
		"Targets": []any{target},
	})
	resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)

	// Then: ListTargetsByRule returns the ECS-specific target fields unchanged
	resp = ebCall(t, srv, "ListTargetsByRule", map[string]any{"Rule": "scheduled-fargate"})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	var out struct {
		Targets []map[string]any `json:"Targets"`
	}
	helpers.DecodeJSON(t, resp, &out)
	if len(out.Targets) != 1 {
		t.Fatalf("expected 1 target, got %d", len(out.Targets))
	}
	got := out.Targets[0]
	if got["RoleArn"] != target["RoleArn"] {
		t.Fatalf("RoleArn = %#v, want %#v", got["RoleArn"], target["RoleArn"])
	}
	ecsParams, ok := got["EcsParameters"].(map[string]any)
	if !ok {
		t.Fatalf("EcsParameters missing or wrong type: %#v", got["EcsParameters"])
	}
	if ecsParams["LaunchType"] != "FARGATE" {
		t.Fatalf("LaunchType = %#v, want FARGATE", ecsParams["LaunchType"])
	}
	if ecsParams["TaskDefinitionArn"] != "arn:aws:ecs:us-east-1:000000000000:task-definition/app:1" {
		t.Fatalf("TaskDefinitionArn = %#v", ecsParams["TaskDefinitionArn"])
	}
}

func TestPutEvents_matchingRuleDeliversToSQSTarget(t *testing.T) {
	// Given: an SQS queue is configured as a target for a matching EventBridge rule
	srv := helpers.NewTestServer(t)
	queueResp := sqsCallForEventBridge(t, srv, "CreateQueue", map[string]any{"QueueName": "orders-events"})
	defer queueResp.Body.Close()
	helpers.AssertStatus(t, queueResp, http.StatusOK)
	var queueOut struct {
		QueueURL string `json:"QueueUrl"`
	}
	helpers.DecodeJSON(t, queueResp, &queueOut)

	ruleResp := ebCall(t, srv, "PutRule", map[string]any{
		"Name":         "orders-created",
		"EventPattern": `{"source":["com.example.orders"],"detail-type":["OrderCreated"],"detail":{"state":["created"]}}`,
	})
	ruleResp.Body.Close()
	helpers.AssertStatus(t, ruleResp, http.StatusOK)

	targetArn := "arn:aws:sqs:us-east-1:000000000000:orders-events"
	targetResp := ebCall(t, srv, "PutTargets", map[string]any{
		"Rule": "orders-created",
		"Targets": []any{map[string]any{
			"Id":  "queue",
			"Arn": targetArn,
		}},
	})
	targetResp.Body.Close()
	helpers.AssertStatus(t, targetResp, http.StatusOK)

	// When: a matching event is put on the default bus
	putResp := ebCall(t, srv, "PutEvents", map[string]any{
		"Entries": []any{map[string]any{
			"Source":     "com.example.orders",
			"DetailType": "OrderCreated",
			"Detail":     `{"state":"created","orderId":"o-123"}`,
		}},
	})
	putResp.Body.Close()
	helpers.AssertStatus(t, putResp, http.StatusOK)

	// Then: the target queue receives the full EventBridge event envelope
	recvResp := sqsCallForEventBridge(t, srv, "ReceiveMessage", map[string]any{
		"QueueUrl":            queueOut.QueueURL,
		"MaxNumberOfMessages": 1,
	})
	defer recvResp.Body.Close()
	helpers.AssertStatus(t, recvResp, http.StatusOK)
	var recvOut struct {
		Messages []struct {
			Body string `json:"Body"`
		} `json:"Messages"`
	}
	helpers.DecodeJSON(t, recvResp, &recvOut)
	if len(recvOut.Messages) != 1 {
		t.Fatalf("expected 1 delivered message, got %d", len(recvOut.Messages))
	}
	if !strings.Contains(recvOut.Messages[0].Body, `"source":"com.example.orders"`) || !strings.Contains(recvOut.Messages[0].Body, `"orderId":"o-123"`) {
		t.Fatalf("unexpected delivered event body: %s", recvOut.Messages[0].Body)
	}
}

func TestScheduledRule_ecsFargateTargetRunsTask(t *testing.T) {
	// Given: a scheduled EventBridge rule targets an ECS Fargate task
	srv := helpers.NewTestServer(t, helpers.WithMockClock())

	resp := ecsCallForEventBridge(t, srv, "CreateCluster", map[string]any{"clusterName": "scheduled"})
	resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	resp = ecsCallForEventBridge(t, srv, "RegisterTaskDefinition", map[string]any{
		"family":                  "scheduled-task",
		"cpu":                     "256",
		"memory":                  "512",
		"networkMode":             "awsvpc",
		"requiresCompatibilities": []any{"FARGATE"},
		"containerDefinitions":    []any{map[string]any{"name": "app", "image": "public.ecr.aws/nginx/nginx:latest"}},
	})
	resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)

	resp = ebCall(t, srv, "PutRule", map[string]any{
		"Name":               "run-scheduled-task",
		"ScheduleExpression": "rate(1 minute)",
		"State":              "ENABLED",
	})
	resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	resp = ebCall(t, srv, "PutTargets", map[string]any{
		"Rule": "run-scheduled-task",
		"Targets": []any{map[string]any{
			"Id":      "ecs",
			"Arn":     "arn:aws:ecs:us-east-1:000000000000:cluster/scheduled",
			"RoleArn": "arn:aws:iam::000000000000:role/events-run-task",
			"EcsParameters": map[string]any{
				"TaskDefinitionArn": "scheduled-task",
				"LaunchType":        "FARGATE",
				"TaskCount":         float64(1),
				"NetworkConfiguration": map[string]any{
					"awsvpcConfiguration": map[string]any{
						"Subnets":        []any{"subnet-scheduled"},
						"SecurityGroups": []any{"sg-scheduled"},
						"AssignPublicIp": "DISABLED",
					},
				},
			},
		}},
	})
	resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)

	// When: time advances past the first scheduled rate interval
	srv.Clock.Add(61 * time.Second)

	// Then: EventBridge invokes ECS RunTask and a task is listed for the cluster
	waitForECSTaskCountForEventBridge(t, srv, "scheduled", 1)
}

func waitForECSTaskCountForEventBridge(t *testing.T, srv *helpers.TestServer, cluster string, want int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	var last []string
	for time.Now().Before(deadline) {
		resp := ecsCallForEventBridge(t, srv, "ListTasks", map[string]any{"cluster": cluster})
		helpers.AssertStatus(t, resp, http.StatusOK)
		var out struct {
			TaskArns []string `json:"taskArns"`
		}
		helpers.DecodeJSON(t, resp, &out)
		resp.Body.Close()
		last = out.TaskArns
		if len(out.TaskArns) == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("expected %d scheduled tasks, got %d (%#v)", want, len(last), last)
}

// ─── ListEventBuses ───────────────────────────────────────────────────────────

func TestListEventBuses_success(t *testing.T) {
	// Given: two event buses
	srv := helpers.NewTestServer(t)
	for _, name := range []string{"bus-a", "bus-b"} {
		r := ebCall(t, srv, "CreateEventBus", map[string]any{"Name": name})
		r.Body.Close()
	}

	// When: ListEventBuses is called
	resp := ebCall(t, srv, "ListEventBuses", map[string]any{})
	defer resp.Body.Close()

	// Then: 200 with EventBuses list (includes default + 2 created = 3)
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		EventBuses []struct {
			Name string `json:"Name"`
			Arn  string `json:"Arn"`
		} `json:"EventBuses"`
	}
	helpers.DecodeJSON(t, resp, &result)
	if len(result.EventBuses) < 2 {
		t.Errorf("expected at least 2 event buses, got %d", len(result.EventBuses))
	}
}

// ─── PutRule ──────────────────────────────────────────────────────────────────

func TestPutRule_success(t *testing.T) {
	// Given: an event bus
	srv := helpers.NewTestServer(t)
	cr := ebCall(t, srv, "CreateEventBus", map[string]any{"Name": "my-bus"})
	cr.Body.Close()

	// When: PutRule is called
	resp := ebCall(t, srv, "PutRule", map[string]any{
		"Name":         "test-rule",
		"EventBusName": "my-bus",
		"EventPattern": `{"source":["aws.ec2"]}`,
		"State":        "ENABLED",
	})
	defer resp.Body.Close()

	// Then: 200 with RuleArn
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		RuleArn string `json:"RuleArn"`
	}
	helpers.DecodeJSON(t, resp, &result)
	if result.RuleArn == "" {
		t.Error("expected RuleArn to be set")
	}
}

// ─── ListRules ────────────────────────────────────────────────────────────────

func TestListRules_success(t *testing.T) {
	// Given: a rule in a bus
	srv := helpers.NewTestServer(t)
	cr := ebCall(t, srv, "CreateEventBus", map[string]any{"Name": "my-bus"})
	cr.Body.Close()
	pr := ebCall(t, srv, "PutRule", map[string]any{
		"Name":         "my-rule",
		"EventBusName": "my-bus",
		"EventPattern": `{"source":["aws.ec2"]}`,
		"State":        "ENABLED",
	})
	pr.Body.Close()

	// When: ListRules is called
	resp := ebCall(t, srv, "ListRules", map[string]any{
		"EventBusName": "my-bus",
	})
	defer resp.Body.Close()

	// Then: 200 with Rules including my-rule
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		Rules []struct {
			Name string `json:"Name"`
		} `json:"Rules"`
	}
	helpers.DecodeJSON(t, resp, &result)
	if len(result.Rules) == 0 {
		t.Error("expected at least one rule")
	}
}

// ─── DeleteEventBus ───────────────────────────────────────────────────────────

func TestDeleteEventBus_success(t *testing.T) {
	// Given: an event bus
	srv := helpers.NewTestServer(t)
	cr := ebCall(t, srv, "CreateEventBus", map[string]any{"Name": "to-delete"})
	cr.Body.Close()

	// When: DeleteEventBus is called
	resp := ebCall(t, srv, "DeleteEventBus", map[string]any{"Name": "to-delete"})
	defer resp.Body.Close()

	// Then: 200
	helpers.AssertStatus(t, resp, http.StatusOK)
}
