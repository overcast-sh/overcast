package eventbridge_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/overcast-sh/overcast/tests/helpers"
)

// TestPutRule_invalidEventPatternIsRefused covers the wire path an SDK takes:
// a rule whose pattern no event could ever satisfy must fail at PutRule, not
// provision cleanly and stay silent forever.
func TestPutRule_invalidEventPatternIsRefused(t *testing.T) {
	// Given: a server with the default bus only.
	srv := helpers.NewTestServer(t)

	cases := map[string]string{
		"not JSON":              `{"source":[`,
		"leaf is not an array":  `{"source":"com.example.orders"}`,
		"unsupported operator":  `{"detail":{"ip":[{"cidr":"10.0.0.0/24"}]}}`,
		"unrecognized operator": `{"source":[{"begins-with":"com."}]}`,
	}
	for name, pattern := range cases {
		t.Run(name, func(t *testing.T) {
			// When: PutRule is called over AWS JSON 1.1.
			resp := ebCall(t, srv, "PutRule", map[string]any{
				"Name":         "bad-pattern",
				"EventPattern": pattern,
			})
			defer resp.Body.Close()

			// Then: the documented 400 InvalidEventPatternException.
			helpers.AssertStatus(t, resp, http.StatusBadRequest)
			helpers.AssertJSONError(t, resp, "InvalidEventPatternException")
		})
	}
}

// TestPutRule_requiresEventPatternOrScheduleExpression pins AWS's documented
// "A rule must contain at least an EventPattern or ScheduleExpression".
func TestPutRule_requiresEventPatternOrScheduleExpression(t *testing.T) {
	// Given: a server.
	srv := helpers.NewTestServer(t)

	// When: a rule is created with neither trigger.
	resp := ebCall(t, srv, "PutRule", map[string]any{"Name": "no-trigger"})
	defer resp.Body.Close()

	// Then: ValidationException, and the rule does not exist afterwards.
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "ValidationException")

	describe := ebCall(t, srv, "DescribeRule", map[string]any{"Name": "no-trigger"})
	defer describe.Body.Close()
	helpers.AssertStatus(t, describe, http.StatusBadRequest)
}

// TestPutRule_unknownEventBusIsResourceNotFound covers a rule aimed at a bus
// that was never created — previously accepted, leaving a rule no PutEvents
// call could ever reach.
func TestPutRule_unknownEventBusIsResourceNotFound(t *testing.T) {
	// Given: a server where "orders-bus" has not been created.
	srv := helpers.NewTestServer(t)

	// When: a rule is put on it.
	resp := ebCall(t, srv, "PutRule", map[string]any{
		"Name":         "orders",
		"EventBusName": "orders-bus",
		"EventPattern": `{"source":["com.example.orders"]}`,
	})
	defer resp.Body.Close()

	// Then: ResourceNotFoundException.
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "ResourceNotFoundException")

	// And: creating the bus first makes the same call succeed.
	created := ebCall(t, srv, "CreateEventBus", map[string]any{"Name": "orders-bus"})
	created.Body.Close()
	helpers.AssertStatus(t, created, http.StatusOK)

	retry := ebCall(t, srv, "PutRule", map[string]any{
		"Name":         "orders",
		"EventBusName": "orders-bus",
		"EventPattern": `{"source":["com.example.orders"]}`,
	})
	defer retry.Body.Close()
	helpers.AssertStatus(t, retry, http.StatusOK)
}

// TestPutEvents_contentFilterDeliversToSQSTarget proves the content-filter
// operators are evaluated by the same matcher rule delivery uses, not only by
// TestEventPattern.
func TestPutEvents_contentFilterDeliversToSQSTarget(t *testing.T) {
	// Given: an SQS queue behind a rule that filters on a prefix and a range.
	srv := helpers.NewTestServer(t)
	queueResp := sqsCallForEventBridge(t, srv, "CreateQueue", map[string]any{"QueueName": "filtered-events"})
	defer queueResp.Body.Close()
	helpers.AssertStatus(t, queueResp, http.StatusOK)
	var queueOut struct {
		QueueURL string `json:"QueueUrl"`
	}
	helpers.DecodeJSON(t, queueResp, &queueOut)

	ruleResp := ebCall(t, srv, "PutRule", map[string]any{
		"Name": "big-orders",
		"EventPattern": `{"source":[{"prefix":"com.example."}],` +
			`"detail":{"amount":[{"numeric":[">",100]}],"state":[{"anything-but":"cancelled"}]}}`,
	})
	ruleResp.Body.Close()
	helpers.AssertStatus(t, ruleResp, http.StatusOK)

	targetResp := ebCall(t, srv, "PutTargets", map[string]any{
		"Rule": "big-orders",
		"Targets": []any{map[string]any{
			"Id":  "queue",
			"Arn": "arn:aws:sqs:us-east-1:000000000000:filtered-events",
		}},
	})
	targetResp.Body.Close()
	helpers.AssertStatus(t, targetResp, http.StatusOK)

	// When: three events are published — one matching, two failing a clause.
	putResp := ebCall(t, srv, "PutEvents", map[string]any{
		"Entries": []any{
			map[string]any{
				"Source": "com.example.orders", "DetailType": "OrderCreated",
				"Detail": `{"amount":250,"state":"placed","orderId":"keep-me"}`,
			},
			map[string]any{
				"Source": "com.example.orders", "DetailType": "OrderCreated",
				"Detail": `{"amount":50,"state":"placed","orderId":"too-small"}`,
			},
			map[string]any{
				"Source": "com.example.orders", "DetailType": "OrderCreated",
				"Detail": `{"amount":900,"state":"cancelled","orderId":"excluded"}`,
			},
		},
	})
	putResp.Body.Close()
	helpers.AssertStatus(t, putResp, http.StatusOK)

	// Then: only the matching event reaches the queue.
	recvResp := sqsCallForEventBridge(t, srv, "ReceiveMessage", map[string]any{
		"QueueUrl":            queueOut.QueueURL,
		"MaxNumberOfMessages": 10,
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
		t.Fatalf("expected exactly the matching event, got %d messages", len(recvOut.Messages))
	}
	if !strings.Contains(recvOut.Messages[0].Body, "keep-me") {
		t.Fatalf("delivered the wrong event: %s", recvOut.Messages[0].Body)
	}
}

// TestDeleteRule_withTargetsAttachedIsRefused pins AWS's documented ordering:
// "Before you can delete the rule, you must remove all targets, using
// RemoveTargets" (API_DeleteRule). A rule deleted out from under its targets
// used to succeed, leaving the targets orphaned in the store.
func TestDeleteRule_withTargetsAttachedIsRefused(t *testing.T) {
	// Given: a rule with one target.
	srv := helpers.NewTestServer(t)
	resp := ebCall(t, srv, "PutRule", map[string]any{
		"Name":         "has-targets",
		"EventPattern": `{"source":["com.example.orders"]}`,
	})
	resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)

	resp = ebCall(t, srv, "PutTargets", map[string]any{
		"Rule": "has-targets",
		"Targets": []any{map[string]any{
			"Id":  "queue",
			"Arn": "arn:aws:sqs:us-east-1:000000000000:orders",
		}},
	})
	resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)

	// When: DeleteRule is called while the target is still attached.
	del := ebCall(t, srv, "DeleteRule", map[string]any{"Name": "has-targets"})
	defer del.Body.Close()

	// Then: the documented ValidationException, and the rule survives.
	helpers.AssertStatus(t, del, http.StatusBadRequest)
	var errOut struct {
		Type    string `json:"__type"`
		Message string `json:"message"`
	}
	helpers.DecodeJSON(t, del, &errOut)
	if !strings.Contains(errOut.Type, "ValidationException") {
		t.Fatalf("error type = %q, want ValidationException", errOut.Type)
	}
	if errOut.Message != "Rule can't be deleted since it has targets." {
		t.Fatalf("message = %q", errOut.Message)
	}

	describe := ebCall(t, srv, "DescribeRule", map[string]any{"Name": "has-targets"})
	describe.Body.Close()
	helpers.AssertStatus(t, describe, http.StatusOK)

	// And: after RemoveTargets the delete succeeds.
	rm := ebCall(t, srv, "RemoveTargets", map[string]any{
		"Rule": "has-targets",
		"Ids":  []any{"queue"},
	})
	rm.Body.Close()
	helpers.AssertStatus(t, rm, http.StatusOK)

	retry := ebCall(t, srv, "DeleteRule", map[string]any{"Name": "has-targets"})
	retry.Body.Close()
	helpers.AssertStatus(t, retry, http.StatusOK)

	gone := ebCall(t, srv, "DescribeRule", map[string]any{"Name": "has-targets"})
	gone.Body.Close()
	helpers.AssertStatus(t, gone, http.StatusBadRequest)
}

// TestDeleteRule_forceDoesNotBypassTheTargetsCheck holds Force to what the API
// reference says it is: the managed-rule escape, "ignored for rules that are
// not managed rules". It is not a way to delete a rule that still has targets.
func TestDeleteRule_forceDoesNotBypassTheTargetsCheck(t *testing.T) {
	// Given: a rule with a target.
	srv := helpers.NewTestServer(t)
	resp := ebCall(t, srv, "PutRule", map[string]any{
		"Name":         "forced",
		"EventPattern": `{"source":["com.example.orders"]}`,
	})
	resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	resp = ebCall(t, srv, "PutTargets", map[string]any{
		"Rule":    "forced",
		"Targets": []any{map[string]any{"Id": "q", "Arn": "arn:aws:sqs:us-east-1:000000000000:orders"}},
	})
	resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)

	// When: DeleteRule is called with Force.
	del := ebCall(t, srv, "DeleteRule", map[string]any{"Name": "forced", "Force": true})
	defer del.Body.Close()

	// Then: it is still refused.
	helpers.AssertStatus(t, del, http.StatusBadRequest)
	helpers.AssertJSONError(t, del, "ValidationException")
}

// TestDeleteRule_withoutTargetsStaysIdempotent keeps AWS's "If you call delete
// rule multiple times for the same rule, all calls will succeed."
func TestDeleteRule_withoutTargetsStaysIdempotent(t *testing.T) {
	// Given: a rule with no targets.
	srv := helpers.NewTestServer(t)
	resp := ebCall(t, srv, "PutRule", map[string]any{
		"Name":         "no-targets",
		"EventPattern": `{"source":["com.example.orders"]}`,
	})
	resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)

	// When/Then: deleting it twice both succeed.
	for i := 0; i < 2; i++ {
		del := ebCall(t, srv, "DeleteRule", map[string]any{"Name": "no-targets"})
		del.Body.Close()
		helpers.AssertStatus(t, del, http.StatusOK)
	}
}
