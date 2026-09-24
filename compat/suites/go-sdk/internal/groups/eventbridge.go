package groups

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eventbridge"
	"github.com/aws/aws-sdk-go-v2/service/eventbridge/types"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	sqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/overcast-sh/overcast-compat-go-sdk/internal/clients"
	"github.com/overcast-sh/overcast-compat-go-sdk/internal/harness"
)

func EventBridge(c *clients.Clients) ServiceGroup {
	g := &ebGroup{c: c}
	return ServiceGroup{
		Impls: map[string]harness.TestFn{
			"eventbridge-target-fanout:PutFanoutTargets":              g.PutFanoutTargets,
			"eventbridge-target-fanout:PutEventsToQueueTarget":        g.PutEventsToQueueTarget,
			"eventbridge-target-fanout:PutEventsWithInputTransformer": g.PutEventsWithInputTransformer,
			"eventbridge-patterns:TestEventPattern":                   g.TestEventPattern,
			"eventbridge-patterns:TestEventPatternNoMatch":            g.TestEventPatternNoMatch,
		},
		Setup: map[string]func(context.Context, *harness.TestContext) error{
			"eventbridge-target-fanout": g.setupFanout,
		},
		Teardown: map[string]func(context.Context, *harness.TestContext) error{
			"eventbridge-target-fanout": g.teardownFanout,
		},
	}
}

type ebGroup struct{ c *clients.Clients }

func (g *ebGroup) cl() *eventbridge.Client { return g.c.EventBridge() }

// ── eventbridge-patterns ──────────────────────────────────────────────────────

// patternsEvent is the envelope both pattern tests evaluate: every field AWS
// documents as mandatory for TestEventPattern is present.
func (g *ebGroup) patternsEvent(t *harness.TestContext) string {
	return fmt.Sprintf(`{"id":%q,"detail-type":"order.created","source":"compat.eventbridge-patterns",`+
		`"account":"000000000000","time":"2026-01-01T00:00:00Z","region":%q,"resources":[],"detail":{"orderId":"1"}}`,
		t.RunID, t.Region)
}

func (g *ebGroup) testEventPattern(ctx context.Context, t *harness.TestContext, pattern string) (bool, error) {
	resp, err := g.cl().TestEventPattern(ctx, &eventbridge.TestEventPatternInput{
		EventPattern: aws.String(pattern),
		Event:        aws.String(g.patternsEvent(t)),
	})
	if err != nil {
		return false, err
	}
	return resp.Result, nil
}

func (g *ebGroup) TestEventPattern(ctx context.Context, t *harness.TestContext) error {
	matched, err := g.testEventPattern(ctx, t, `{"source":["compat.eventbridge-patterns"],"detail-type":["order.created"]}`)
	if err != nil {
		return err
	}
	if !matched {
		return fmt.Errorf("TestEventPattern: expected Result=true for a matching pattern, got false")
	}
	return nil
}

func (g *ebGroup) TestEventPatternNoMatch(ctx context.Context, t *harness.TestContext) error {
	matched, err := g.testEventPattern(ctx, t, `{"source":["compat.eventbridge-patterns.other"]}`)
	if err != nil {
		return err
	}
	if matched {
		return fmt.Errorf("TestEventPatternNoMatch: expected Result=false for a non-matching pattern, got true")
	}
	return nil
}

// ── eventbridge-target-fanout ─────────────────────────────────────────────────
//
// Target fan-out: an event put on a bus reaches the rule's targets, with the
// target's input transformation applied first. The group provisions its own
// queues and rule so it can run independently of the other EventBridge groups.

func (g *ebGroup) setupFanout(ctx context.Context, t *harness.TestContext) error {
	sq := g.c.SQS()
	plain := fmt.Sprintf("oc-%s-fanout-plain", t.RunID)
	shaped := fmt.Sprintf("oc-%s-fanout-shaped", t.RunID)

	for key, name := range map[string]string{"eb_fanout_plain": plain, "eb_fanout_shaped": shaped} {
		created, err := sq.CreateQueue(ctx, &sqs.CreateQueueInput{QueueName: aws.String(name)})
		if err != nil {
			return err
		}
		attrs, err := sq.GetQueueAttributes(ctx, &sqs.GetQueueAttributesInput{
			QueueUrl:       created.QueueUrl,
			AttributeNames: []sqstypes.QueueAttributeName{"QueueArn"},
		})
		if err != nil {
			return err
		}
		t.Set(key+"_url", aws.ToString(created.QueueUrl))
		t.Set(key+"_arn", attrs.Attributes["QueueArn"])
	}

	source := fmt.Sprintf("oc.fanout.%s", t.RunID)
	rule := fmt.Sprintf("oc-%s-fanout", t.RunID)
	if _, err := g.cl().PutRule(ctx, &eventbridge.PutRuleInput{
		Name:         aws.String(rule),
		EventPattern: aws.String(fmt.Sprintf(`{"source":["%s"]}`, source)),
		State:        types.RuleStateEnabled,
	}); err != nil {
		return err
	}
	t.Set("eb_fanout_rule", rule)
	t.Set("eb_fanout_source", source)
	return nil
}

func (g *ebGroup) teardownFanout(ctx context.Context, t *harness.TestContext) error {
	if rule := t.GetString("eb_fanout_rule"); rule != "" {
		g.cl().RemoveTargets(ctx, &eventbridge.RemoveTargetsInput{ //nolint:errcheck
			Rule: aws.String(rule), Ids: []string{"plain", "shaped"},
		})
		g.cl().DeleteRule(ctx, &eventbridge.DeleteRuleInput{Name: aws.String(rule)}) //nolint:errcheck
	}
	for _, key := range []string{"eb_fanout_plain_url", "eb_fanout_shaped_url"} {
		if url := t.GetString(key); url != "" {
			g.c.SQS().DeleteQueue(ctx, &sqs.DeleteQueueInput{QueueUrl: aws.String(url)}) //nolint:errcheck
		}
	}
	return nil
}

func (g *ebGroup) PutFanoutTargets(ctx context.Context, t *harness.TestContext) error {
	rule := t.GetString("eb_fanout_rule")
	resp, err := g.cl().PutTargets(ctx, &eventbridge.PutTargetsInput{
		Rule: aws.String(rule),
		Targets: []types.Target{
			{Id: aws.String("plain"), Arn: aws.String(t.GetString("eb_fanout_plain_arn"))},
			{
				Id:  aws.String("shaped"),
				Arn: aws.String(t.GetString("eb_fanout_shaped_arn")),
				InputTransformer: &types.InputTransformer{
					InputPathsMap: map[string]string{"order": "$.detail.orderId"},
					InputTemplate: aws.String(`{"order":"<order>"}`),
				},
			},
		},
	})
	if err != nil {
		return err
	}
	if resp.FailedEntryCount > 0 {
		return fmt.Errorf("PutFanoutTargets: %d failed entries", resp.FailedEntryCount)
	}

	listed, err := g.cl().ListTargetsByRule(ctx, &eventbridge.ListTargetsByRuleInput{Rule: aws.String(rule)})
	if err != nil {
		return err
	}
	if len(listed.Targets) != 2 {
		return fmt.Errorf("PutFanoutTargets: rule has %d targets, want 2", len(listed.Targets))
	}
	for _, target := range listed.Targets {
		if aws.ToString(target.Id) != "shaped" {
			continue
		}
		if target.InputTransformer == nil || aws.ToString(target.InputTransformer.InputTemplate) == "" {
			return fmt.Errorf("PutFanoutTargets: InputTransformer did not round-trip")
		}
	}
	return nil
}

func (g *ebGroup) PutEventsToQueueTarget(ctx context.Context, t *harness.TestContext) error {
	if err := g.putFanoutEvent(ctx, t, "queue-target"); err != nil {
		return err
	}
	body, err := g.awaitFanoutMessage(ctx, t.GetString("eb_fanout_plain_url"), "queue-target")
	if err != nil {
		return fmt.Errorf("PutEventsToQueueTarget: %w", err)
	}
	if !strings.Contains(body, t.GetString("eb_fanout_source")) || !strings.Contains(body, "queue-target") {
		return fmt.Errorf("PutEventsToQueueTarget: delivered body missing the event envelope: %s", body)
	}
	return nil
}

func (g *ebGroup) PutEventsWithInputTransformer(ctx context.Context, t *harness.TestContext) error {
	if err := g.putFanoutEvent(ctx, t, "transformed"); err != nil {
		return err
	}
	body, err := g.awaitFanoutMessage(ctx, t.GetString("eb_fanout_shaped_url"), "transformed")
	if err != nil {
		return fmt.Errorf("PutEventsWithInputTransformer: %w", err)
	}
	if body != `{"order":"transformed"}` {
		return fmt.Errorf("PutEventsWithInputTransformer: delivered body = %s, want the rendered template", body)
	}
	return nil
}

func (g *ebGroup) putFanoutEvent(ctx context.Context, t *harness.TestContext, orderID string) error {
	resp, err := g.cl().PutEvents(ctx, &eventbridge.PutEventsInput{
		Entries: []types.PutEventsRequestEntry{{
			Source:     aws.String(t.GetString("eb_fanout_source")),
			DetailType: aws.String("FanoutTest"),
			Detail:     aws.String(fmt.Sprintf(`{"orderId":%q}`, orderID)),
		}},
	})
	if err != nil {
		return err
	}
	if resp.FailedEntryCount > 0 {
		return fmt.Errorf("PutEvents: %d failed entries", resp.FailedEntryCount)
	}
	return nil
}

// awaitFanoutMessage polls a queue for a delivered message carrying want.
// Both targets hang off one rule, so every event reaches both queues and a
// queue may hold an earlier test's message; matching on want (and consuming
// what does not match) keeps the tests order-independent. Delivery is
// asynchronous, so a bounded poll replaces a fixed sleep.
func (g *ebGroup) awaitFanoutMessage(ctx context.Context, queueURL, want string) (string, error) {
	for attempt := 0; attempt < 15; attempt++ {
		resp, err := g.c.SQS().ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
			QueueUrl:            aws.String(queueURL),
			MaxNumberOfMessages: 10,
			WaitTimeSeconds:     1,
		})
		if err != nil {
			return "", err
		}
		var matched string
		for _, message := range resp.Messages {
			g.c.SQS().DeleteMessage(ctx, &sqs.DeleteMessageInput{ //nolint:errcheck
				QueueUrl: aws.String(queueURL), ReceiptHandle: message.ReceiptHandle,
			})
			if body := aws.ToString(message.Body); strings.Contains(body, want) {
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
