package eventbridge

import (
	"context"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/overcast-sh/overcast/internal/clock"
	"github.com/overcast-sh/overcast/internal/config"
	"github.com/overcast-sh/overcast/internal/middleware"
	"github.com/overcast-sh/overcast/internal/protocol/op"
	"github.com/overcast-sh/overcast/internal/state"
)

func TestTypedOps_matchDispatchSurface(t *testing.T) {
	s := &Service{}
	ops := s.typedOps()
	expected := []string{
		"CreateEventBus",
		"DescribeEventBus",
		"ListEventBuses",
		"TagResource",
		"ListTagsForResource",
		"UntagResource",
		"DeleteEventBus",
		"PutRule",
		"DescribeRule",
		"ListRules",
		"PutTargets",
		"ListTargetsByRule",
		"RemoveTargets",
		"DisableRule",
		"EnableRule",
		"DeleteRule",
		"PutEvents",
		"TestEventPattern",
		"PutPermission",
		"RemovePermission",
	}

	if len(ops) != len(expected) {
		t.Fatalf("typed ops len = %d, expected %d", len(ops), len(expected))
	}
	for _, name := range expected {
		operation, ok := ops[name]
		if !ok {
			t.Fatalf("missing typed op %q", name)
		}
		if operation.Name() != name {
			t.Fatalf("typed op %q has Name() %q", name, operation.Name())
		}
	}
	for name, operation := range ops {
		if _, ok := operation.(*op.Raw); ok {
			t.Fatalf("%s registered as raw operation", name)
		}
	}
}

func TestPutEventsEnvelope_usesRequestRegion(t *testing.T) {
	// Given: EventBridge has a default region but the request carries another region.
	s := New(&config.Config{Region: "us-east-1", AccountID: "000000000000"}, state.NewMemoryStore(), zap.NewNop(), clock.NewMock())
	ctx := middleware.ContextWithRegion(context.Background(), "eu-west-1")

	// When: an EventBridge envelope is built.
	event := s.putEventsEnvelope(ctx, "evt-1", map[string]any{
		"Source":     "com.example",
		"DetailType": "Example",
		"Detail":     `{"ok":true}`,
	})

	// Then: the event region matches the request region, not the configured fallback.
	if event["region"] != "eu-west-1" {
		t.Fatalf("region = %#v, want eu-west-1", event["region"])
	}
}

func TestLowerEventBridgeKeys_emptyKey(t *testing.T) {
	// Given: a map contains an empty JSON object key.
	in := map[string]any{"": "kept", "Subnets": []any{"subnet-1"}}

	// When: EventBridge target keys are normalized.
	out, ok := lowerEventBridgeKeys(in).(map[string]any)
	if !ok {
		t.Fatalf("expected map output")
	}

	// Then: no panic occurs and the empty key is preserved.
	if out[""] != "kept" {
		t.Fatalf("empty key value = %#v, want kept", out[""])
	}
	if _, ok := out["subnets"]; !ok {
		t.Fatalf("expected Subnets to be normalized: %#v", out)
	}
}

func TestNextRateFire_missingLastFire(t *testing.T) {
	// Given: a persisted rate rule has no last-fire record.
	now := time.Unix(120, 0).UTC()

	// When: the next fire time is calculated.
	next, err := nextRuleFire("rate(1 minute)", time.Time{}, now)
	if err != nil {
		t.Fatalf("nextRuleFire returned error: %v", err)
	}

	// Then: the rule is due now rather than becoming permanently inert.
	if !next.Equal(now) {
		t.Fatalf("next = %s, want %s", next, now)
	}
}

func TestNextRuleFire_awsSunday(t *testing.T) {
	// Given: a Saturday, and AWS cron day-of-week 1, which means Sunday.
	saturday := time.Date(2026, 7, 18, 0, 0, 0, 0, time.UTC)

	// When: the rule's next firing is computed.
	next, err := nextRuleFire("cron(0 0 ? * 1 *)", time.Time{}, saturday)
	if err != nil {
		t.Fatalf("nextRuleFire: %v", err)
	}

	// Then: it lands on Sunday. Day-of-week 7 is Saturday, not Sunday.
	if got := next.Weekday(); got != time.Sunday {
		t.Fatalf("day-of-week 1 fired on %s, want Sunday", got)
	}
	next, err = nextRuleFire("cron(0 0 ? * 7 *)", time.Time{}, saturday)
	if err != nil {
		t.Fatalf("nextRuleFire: %v", err)
	}
	if got := next.Weekday(); got != time.Saturday {
		t.Fatalf("day-of-week 7 fired on %s, want Saturday", got)
	}
}

func TestTargetPayload_inputPathSelectsNode(t *testing.T) {
	// Given: a target selects the event detail with InputPath.
	target := ebTarget{InputPath: "$.detail"}
	event := map[string]any{"detail": map[string]any{"id": "1"}}

	// When: a target payload is built.
	got, err := targetPayload(ebRule{Name: "r"}, target, event)
	if err != nil {
		t.Fatalf("targetPayload: %v", err)
	}

	// Then: only the selected node is delivered.
	if string(got) != `{"id":"1"}` {
		t.Fatalf("payload = %s, want the selected detail node", got)
	}
}

func TestTargetPayload_inputPathMissingNodeFailsLoudly(t *testing.T) {
	// Given: a target whose InputPath selects nothing in this event.
	target := ebTarget{InputPath: "$.detail.missing"}

	// When: a target payload is built.
	_, err := targetPayload(ebRule{Name: "r"}, target, map[string]any{"detail": map[string]any{}})

	// Then: delivery fails explicitly rather than sending an empty payload.
	if err == nil {
		t.Fatal("expected an error for an InputPath that selects nothing")
	}
}

func TestTargetPayload_inputTransformerRendersTemplate(t *testing.T) {
	// Given: a target rendering a template from named paths, including one of
	// EventBridge's reserved rule variables.
	target := ebTarget{InputTransformer: &ebInputTransformer{
		InputPathsMap: map[string]string{"id": "$.detail.orderId"},
		InputTemplate: `{"order":"<id>","rule":"<aws.events.rule-name>"}`,
	}}
	event := map[string]any{"detail": map[string]any{"orderId": "o-1"}}

	// When: a target payload is built.
	got, err := targetPayload(ebRule{Name: "orders"}, target, event)
	if err != nil {
		t.Fatalf("targetPayload: %v", err)
	}

	// Then: both placeholders are substituted.
	if string(got) != `{"order":"o-1","rule":"orders"}` {
		t.Fatalf("payload = %s", got)
	}
}

func TestValidateTargets_rejectsUnsupportedAndMalformed(t *testing.T) {
	// Given: one deliverable target, one target type the emulator has no sink
	// for, and (separately) a malformed ARN.
	accepted, failed, aerr := validateTargets([]ebTarget{
		{ID: "ok", ARN: "arn:aws:sqs:us-east-1:000000000000:q"},
		{ID: "no", ARN: "arn:aws:logs:us-east-1:000000000000:log-group:/aws/events/x"},
	})

	// Then: the unsupported target is reported, not stored, and the rest is kept.
	if aerr != nil {
		t.Fatalf("unexpected request-level error: %v", aerr)
	}
	if len(accepted) != 1 || accepted[0].ID != "ok" {
		t.Fatalf("accepted = %#v, want only the SQS target", accepted)
	}
	if len(failed) != 1 || failed[0].ErrorCode != unsupportedTargetCode {
		t.Fatalf("failed = %#v, want one %s entry", failed, unsupportedTargetCode)
	}

	// And: a malformed ARN fails the whole call, as AWS's shape check does.
	if _, _, aerr := validateTargets([]ebTarget{{ID: "bad", ARN: "nope"}}); aerr == nil {
		t.Fatal("expected a ValidationException for a malformed target ARN")
	}
}
