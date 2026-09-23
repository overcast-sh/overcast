package groups

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/overcast-sh/overcast-compat-cli/internal/awscli"
	"github.com/overcast-sh/overcast-compat-cli/internal/harness"
)

// EventBridge returns the EventBridge service group.
func EventBridge() ServiceGroup {
	g := &ebGroup{}
	return ServiceGroup{
		Impls: map[string]harness.TestFn{
			// eventbridge-buses
			"eventbridge-buses:CreateEventBus":                 g.CreateEventBus,
			"eventbridge-buses:DescribeEventBus":               g.DescribeEventBus,
			"eventbridge-buses:ListEventBuses":                 g.ListEventBuses,
			"eventbridge-buses:TagEventBus":                    g.TagEventBus,
			"eventbridge-buses:ListEventBridgeTagsForResource": g.ListTagsForResource,
			"eventbridge-buses:DeleteEventBus":                 g.DeleteEventBus,
			// eventbridge-events
			"eventbridge-events:PutEvents":      g.PutEvents,
			"eventbridge-events:PutEventsBatch": g.PutEventsBatch,
			// eventbridge-target-fanout
			"eventbridge-target-fanout:PutFanoutTargets":              g.PutFanoutTargets,
			"eventbridge-target-fanout:PutEventsToQueueTarget":        g.PutEventsToQueueTarget,
			"eventbridge-target-fanout:PutEventsWithInputTransformer": g.PutEventsWithInputTransformer,
			// eventbridge-patterns
			"eventbridge-patterns:TestEventPattern":        g.TestEventPattern,
			"eventbridge-patterns:TestEventPatternNoMatch": g.TestEventPatternNoMatch,
		},
		Setup: map[string]func(context.Context, *harness.TestContext) error{
			"eventbridge-buses":         g.setupBuses,
			"eventbridge-events":        g.setupEvents,
			"eventbridge-target-fanout": g.setupFanout,
		},
		Teardown: map[string]func(context.Context, *harness.TestContext) error{
			"eventbridge-buses":         g.teardownBus,
			"eventbridge-events":        g.teardownEvents,
			"eventbridge-target-fanout": g.teardownFanout,
		},
	}
}

type ebGroup struct{}

// busName returns one group's event bus. Each group gets its OWN bus: groups
// run in parallel (8 slots), and when the three EventBridge groups shared one
// name, setupRules/setupEvents re-created the bus the buses group was testing,
// DeleteEventBus deleted it from under the rules/events groups, and every
// teardown deleted it from under everyone — the "bus not found" failures
// behind plan item R7 and the DeleteEventBus quarantine (issue #388).
func (g *ebGroup) busName(t *harness.TestContext, group string) string {
	return fmt.Sprintf("%s-eb-%s", t.RunID, group)
}

// ─── eventbridge-buses ───────────────────────────────────────────────────────

func (g *ebGroup) setupBuses(_ context.Context, _ *harness.TestContext) error { return nil }

func (g *ebGroup) CreateEventBus(_ context.Context, t *harness.TestContext) error {
	out, err := awscli.RunOutput(t.Endpoint, t.Region,
		"events", "create-event-bus",
		"--name", g.busName(t, "buses"),
	)
	if err != nil {
		return err
	}
	arn, _ := out["EventBusArn"].(string)
	if arn == "" {
		return fmt.Errorf("eventbridge CreateEventBus: missing EventBusArn")
	}
	t.Set("bus_arn", arn)
	return nil
}

func (g *ebGroup) DescribeEventBus(_ context.Context, t *harness.TestContext) error {
	out, err := awscli.RunOutput(t.Endpoint, t.Region,
		"events", "describe-event-bus",
		"--name", g.busName(t, "buses"),
	)
	if err != nil {
		return err
	}
	if name, _ := out["Name"].(string); name != g.busName(t, "buses") {
		return fmt.Errorf("eb DescribeEventBus: expected Name=%q, got %q", g.busName(t, "buses"), name)
	}
	return nil
}

func (g *ebGroup) ListEventBuses(_ context.Context, t *harness.TestContext) error {
	out, err := awscli.RunOutput(t.Endpoint, t.Region, "events", "list-event-buses")
	if err != nil {
		return err
	}
	buses, _ := out["EventBuses"].([]any)
	for _, raw := range buses {
		if m, ok := raw.(map[string]any); ok && m["Name"] == g.busName(t, "buses") {
			return nil
		}
	}
	return fmt.Errorf("eb ListEventBuses: bus %q not found", g.busName(t, "buses"))
}

func (g *ebGroup) TagEventBus(_ context.Context, t *harness.TestContext) error {
	arn := t.GetString("bus_arn")
	if err := awscli.Run(t.Endpoint, t.Region,
		"events", "tag-resource",
		"--resource-arn", arn,
		"--tags", `[{"Key":"env","Value":"test"}]`,
	); err != nil {
		return err
	}
	out, err := awscli.RunOutput(t.Endpoint, t.Region,
		"events", "list-tags-for-resource",
		"--resource-arn", arn,
	)
	if err != nil {
		return fmt.Errorf("eb TagEventBus: list-tags failed: %w", err)
	}
	tags, _ := out["Tags"].([]any)
	for _, raw := range tags {
		if m, ok := raw.(map[string]any); ok && m["Key"] == "env" && m["Value"] == "test" {
			return nil
		}
	}
	return fmt.Errorf("eb TagEventBus: env=test tag not found")
}

func (g *ebGroup) ListTagsForResource(_ context.Context, t *harness.TestContext) error {
	arn := t.GetString("bus_arn")
	out, err := awscli.RunOutput(t.Endpoint, t.Region,
		"events", "list-tags-for-resource",
		"--resource-arn", arn,
	)
	if err != nil {
		return err
	}
	tags, _ := out["Tags"].([]any)
	if len(tags) == 0 {
		return fmt.Errorf("eb ListTagsForResource: expected tags, got none")
	}
	return nil
}

func (g *ebGroup) DeleteEventBus(_ context.Context, t *harness.TestContext) error {
	name := g.busName(t, "buses")
	if err := awscli.Run(t.Endpoint, t.Region,
		"events", "delete-event-bus",
		"--name", name,
	); err != nil {
		return err
	}
	out, err := awscli.RunOutput(t.Endpoint, t.Region, "events", "list-event-buses")
	if err != nil {
		return fmt.Errorf("eb DeleteEventBus: list-event-buses failed: %w", err)
	}
	buses, _ := out["EventBuses"].([]any)
	for _, raw := range buses {
		if m, ok := raw.(map[string]any); ok && m["Name"] == name {
			return fmt.Errorf("eb DeleteEventBus: bus %q still present", name)
		}
	}
	return nil
}

func (g *ebGroup) teardownBus(_ context.Context, t *harness.TestContext) error {
	awscli.Run(t.Endpoint, t.Region, "events", "delete-event-bus", "--name", g.busName(t, "buses")) //nolint:errcheck
	return nil
}

// ─── eventbridge-events ──────────────────────────────────────────────────────

func (g *ebGroup) setupEvents(_ context.Context, t *harness.TestContext) error {
	out, err := awscli.RunOutput(t.Endpoint, t.Region,
		"events", "create-event-bus",
		"--name", g.busName(t, "events"),
	)
	if err != nil {
		return err
	}
	arn, _ := out["EventBusArn"].(string)
	t.Set("bus_arn", arn)
	return nil
}

func (g *ebGroup) PutEvents(_ context.Context, t *harness.TestContext) error {
	entries := fmt.Sprintf(
		`[{"Source":"oc.cli","DetailType":"TestEvent","Detail":"{\"runId\":\"%s\"}","EventBusName":"%s"}]`,
		t.RunID, g.busName(t, "events"),
	)
	out, err := awscli.RunOutput(t.Endpoint, t.Region,
		"events", "put-events",
		"--entries", entries,
	)
	if err != nil {
		return err
	}
	if fc, _ := out["FailedEntryCount"].(float64); fc > 0 {
		return fmt.Errorf("eb PutEvents: FailedEntryCount=%v", fc)
	}
	return nil
}

func (g *ebGroup) PutEventsBatch(_ context.Context, t *harness.TestContext) error {
	entries := fmt.Sprintf(
		`[`+
			`{"Source":"oc.cli","DetailType":"Event1","Detail":"{\"n\":1}","EventBusName":"%s"},`+
			`{"Source":"oc.cli","DetailType":"Event2","Detail":"{\"n\":2}","EventBusName":"%s"}`+
			`]`,
		g.busName(t, "events"), g.busName(t, "events"),
	)
	out, err := awscli.RunOutput(t.Endpoint, t.Region,
		"events", "put-events",
		"--entries", entries,
	)
	if err != nil {
		return err
	}
	if fc, _ := out["FailedEntryCount"].(float64); fc > 0 {
		return fmt.Errorf("eb PutEventsBatch: FailedEntryCount=%v", fc)
	}
	resEntries, _ := out["Entries"].([]any)
	if len(resEntries) != 2 {
		return fmt.Errorf("eb PutEventsBatch: expected 2 Entries, got %d", len(resEntries))
	}
	return nil
}

func (g *ebGroup) teardownEvents(_ context.Context, t *harness.TestContext) error {
	awscli.Run(t.Endpoint, t.Region, "events", "delete-event-bus", "--name", g.busName(t, "events")) //nolint:errcheck
	return nil
}

// ─── eventbridge-target-fanout ───────────────────────────────────────────────
//
// Target fan-out: an event put on a bus reaches the rule's targets, with the
// target's input transformation applied first. The group provisions its own
// queues and rule (issue #388) so it never races the other EventBridge groups.

var (
	ebFanoutPlainNamer  = harness.NewNamer("eb-fo-plain")
	ebFanoutShapedNamer = harness.NewNamer("eb-fo-shaped")
)

func (g *ebGroup) fanoutRuleName(t *harness.TestContext) string {
	return fmt.Sprintf("%s-eb-fanout", t.RunID)
}

func (g *ebGroup) fanoutSource(t *harness.TestContext) string {
	return fmt.Sprintf("oc.fanout.%s", t.RunID)
}

func (g *ebGroup) setupFanout(_ context.Context, t *harness.TestContext) error {
	for key, name := range map[string]string{
		"eb_fanout_plain":  ebFanoutPlainNamer.Name(t),
		"eb_fanout_shaped": ebFanoutShapedNamer.Name(t),
	} {
		out, err := awscli.RunOutput(t.Endpoint, t.Region, "sqs", "create-queue", "--queue-name", name)
		if err != nil {
			return err
		}
		url, _ := out["QueueUrl"].(string)
		if url == "" {
			return fmt.Errorf("eb fanout setup: missing QueueUrl for %s", name)
		}
		attrs, err := awscli.RunOutput(t.Endpoint, t.Region,
			"sqs", "get-queue-attributes", "--queue-url", url, "--attribute-names", "QueueArn")
		if err != nil {
			return err
		}
		attrMap, _ := attrs["Attributes"].(map[string]any)
		arn, _ := attrMap["QueueArn"].(string)
		if arn == "" {
			return fmt.Errorf("eb fanout setup: missing QueueArn for %s", name)
		}
		t.Set(key+"_url", url)
		t.Set(key+"_arn", arn)
	}

	return awscli.Run(t.Endpoint, t.Region,
		"events", "put-rule",
		"--name", g.fanoutRuleName(t),
		"--event-pattern", fmt.Sprintf(`{"source":["%s"]}`, g.fanoutSource(t)),
		"--state", "ENABLED",
	)
}

func (g *ebGroup) teardownFanout(_ context.Context, t *harness.TestContext) error {
	awscli.Run(t.Endpoint, t.Region, //nolint:errcheck
		"events", "remove-targets", "--rule", g.fanoutRuleName(t), "--ids", "plain", "shaped")
	awscli.Run(t.Endpoint, t.Region, //nolint:errcheck
		"events", "delete-rule", "--name", g.fanoutRuleName(t))
	for _, key := range []string{"eb_fanout_plain_url", "eb_fanout_shaped_url"} {
		if url := t.GetString(key); url != "" {
			awscli.Run(t.Endpoint, t.Region, "sqs", "delete-queue", "--queue-url", url) //nolint:errcheck
		}
	}
	return nil
}

func (g *ebGroup) PutFanoutTargets(_ context.Context, t *harness.TestContext) error {
	targets := fmt.Sprintf(
		`[{"Id":"plain","Arn":"%s"},{"Id":"shaped","Arn":"%s","InputTransformer":{"InputPathsMap":{"order":"$.detail.orderId"},"InputTemplate":"{\"order\":\"<order>\"}"}}]`,
		t.GetString("eb_fanout_plain_arn"), t.GetString("eb_fanout_shaped_arn"),
	)
	out, err := awscli.RunOutput(t.Endpoint, t.Region,
		"events", "put-targets", "--rule", g.fanoutRuleName(t), "--targets", targets)
	if err != nil {
		return err
	}
	if count, ok := out["FailedEntryCount"].(float64); ok && count > 0 {
		return fmt.Errorf("eb PutFanoutTargets: %v failed entries: %v", count, out["FailedEntries"])
	}

	listed, err := awscli.RunOutput(t.Endpoint, t.Region,
		"events", "list-targets-by-rule", "--rule", g.fanoutRuleName(t))
	if err != nil {
		return err
	}
	tgts, _ := listed["Targets"].([]any)
	if len(tgts) != 2 {
		return fmt.Errorf("eb PutFanoutTargets: rule has %d targets, want 2", len(tgts))
	}
	for _, raw := range tgts {
		m, _ := raw.(map[string]any)
		if m["Id"] != "shaped" {
			continue
		}
		if _, ok := m["InputTransformer"].(map[string]any); !ok {
			return fmt.Errorf("eb PutFanoutTargets: InputTransformer did not round-trip")
		}
	}
	return nil
}

func (g *ebGroup) PutEventsToQueueTarget(_ context.Context, t *harness.TestContext) error {
	if err := g.putFanoutEvent(t, "queue-target"); err != nil {
		return err
	}
	body, err := g.awaitFanoutMessage(t, t.GetString("eb_fanout_plain_url"), "queue-target")
	if err != nil {
		return fmt.Errorf("eb PutEventsToQueueTarget: %w", err)
	}
	if !strings.Contains(body, g.fanoutSource(t)) || !strings.Contains(body, "queue-target") {
		return fmt.Errorf("eb PutEventsToQueueTarget: delivered body missing the event envelope: %s", body)
	}
	return nil
}

func (g *ebGroup) PutEventsWithInputTransformer(_ context.Context, t *harness.TestContext) error {
	if err := g.putFanoutEvent(t, "transformed"); err != nil {
		return err
	}
	body, err := g.awaitFanoutMessage(t, t.GetString("eb_fanout_shaped_url"), "transformed")
	if err != nil {
		return fmt.Errorf("eb PutEventsWithInputTransformer: %w", err)
	}
	if body != `{"order":"transformed"}` {
		return fmt.Errorf("eb PutEventsWithInputTransformer: delivered body = %s, want the rendered template", body)
	}
	return nil
}

func (g *ebGroup) putFanoutEvent(t *harness.TestContext, orderID string) error {
	entries := fmt.Sprintf(
		`[{"Source":"%s","DetailType":"FanoutTest","Detail":"{\"orderId\":\"%s\"}"}]`,
		g.fanoutSource(t), orderID,
	)
	out, err := awscli.RunOutput(t.Endpoint, t.Region, "events", "put-events", "--entries", entries)
	if err != nil {
		return err
	}
	if count, ok := out["FailedEntryCount"].(float64); ok && count > 0 {
		return fmt.Errorf("put-events: %v failed entries", count)
	}
	return nil
}

// awaitFanoutMessage polls a queue for a delivered message carrying want.
// Both targets hang off one rule, so every event reaches both queues and a
// queue may hold an earlier test's message; matching on want (and consuming
// what does not match) keeps the tests order-independent. Delivery is
// asynchronous, so a bounded poll replaces a fixed sleep.
func (g *ebGroup) awaitFanoutMessage(t *harness.TestContext, queueURL, want string) (string, error) {
	for attempt := 0; attempt < 15; attempt++ {
		out, err := awscli.RunOutput(t.Endpoint, t.Region,
			"sqs", "receive-message", "--queue-url", queueURL,
			"--max-number-of-messages", "10", "--wait-time-seconds", "1")
		if err != nil {
			return "", err
		}
		messages, _ := out["Messages"].([]any)
		matched := ""
		for _, raw := range messages {
			m, _ := raw.(map[string]any)
			body, _ := m["Body"].(string)
			if handle, _ := m["ReceiptHandle"].(string); handle != "" {
				awscli.Run(t.Endpoint, t.Region, //nolint:errcheck
					"sqs", "delete-message", "--queue-url", queueURL, "--receipt-handle", handle)
			}
			if strings.Contains(body, want) {
				matched = body
			}
		}
		if matched != "" {
			return matched, nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return "", fmt.Errorf("no message containing %q delivered to the target queue", want)
}

// ─── eventbridge-patterns ────────────────────────────────────────────────────

// patternsEvent is the envelope both pattern tests evaluate: every field AWS
// documents as mandatory for TestEventPattern is present.
func (g *ebGroup) patternsEvent(t *harness.TestContext) string {
	return fmt.Sprintf(`{"id":%q,"detail-type":"order.created","source":"compat.eventbridge-patterns",`+
		`"account":"000000000000","time":"2026-01-01T00:00:00Z","region":%q,"resources":[],"detail":{"orderId":"1"}}`,
		t.RunID, t.Region)
}

func (g *ebGroup) testEventPattern(t *harness.TestContext, pattern string) (bool, error) {
	out, err := awscli.RunOutput(t.Endpoint, t.Region,
		"events", "test-event-pattern",
		"--event-pattern", pattern,
		"--event", g.patternsEvent(t),
	)
	if err != nil {
		return false, err
	}
	result, ok := out["Result"].(bool)
	if !ok {
		return false, fmt.Errorf("test-event-pattern: Result missing or not a boolean in %v", out)
	}
	return result, nil
}

func (g *ebGroup) TestEventPattern(_ context.Context, t *harness.TestContext) error {
	matched, err := g.testEventPattern(t, `{"source":["compat.eventbridge-patterns"],"detail-type":["order.created"]}`)
	if err != nil {
		return err
	}
	if !matched {
		return fmt.Errorf("TestEventPattern: expected Result=true for a matching pattern, got false")
	}
	return nil
}

func (g *ebGroup) TestEventPatternNoMatch(_ context.Context, t *harness.TestContext) error {
	matched, err := g.testEventPattern(t, `{"source":["compat.eventbridge-patterns.other"]}`)
	if err != nil {
		return err
	}
	if matched {
		return fmt.Errorf("TestEventPatternNoMatch: expected Result=false for a non-matching pattern, got true")
	}
	return nil
}
