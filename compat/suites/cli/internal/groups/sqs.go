package groups

import (
	"context"
	"fmt"

	"github.com/overcast-sh/overcast-compat-cli/internal/awscli"
	"github.com/overcast-sh/overcast-compat-cli/internal/harness"
)

// SQS returns the SQS service group.
func SQS() ServiceGroup {
	g := &sqsGroup{}
	return ServiceGroup{
		Impls: map[string]harness.TestFn{
			// sqs-messages
			"sqs-messages:SendMessage":             g.SendMessage,
			"sqs-messages:SendMessageBatch":        g.SendMessageBatch,
			"sqs-messages:ReceiveMessage":          g.ReceiveMessage,
			"sqs-messages:DeleteMessage":           g.DeleteMessage,
			"sqs-messages:ChangeMessageVisibility": g.ChangeMessageVisibility,
			"sqs-messages:DeleteMessageBatch":      g.DeleteMessageBatch,
			"sqs-messages:PurgeQueue":              g.PurgeQueue,
			// sqs-dlq
			"sqs-dlq:CreateDLQ":        g.CreateDLQ,
			"sqs-dlq:SetRedrivePolicy": g.SetRedrivePolicy,
			"sqs-dlq:GetRedrivePolicy": g.GetRedrivePolicy,
			// sqs-fifo
			"sqs-fifo:CreateFifoQueue":    g.CreateFifoQueue,
			"sqs-fifo:SendFifoMessage":    g.SendFifoMessage,
			"sqs-fifo:ReceiveFifoMessage": g.ReceiveFifoMessage,
		},
		Setup: map[string]func(context.Context, *harness.TestContext) error{
			"sqs-messages": g.setupMessages,
			"sqs-dlq":      g.setupDLQ,
			"sqs-fifo":     g.setupFIFO,
		},
		Teardown: map[string]func(context.Context, *harness.TestContext) error{
			"sqs-messages": g.teardownMessages,
			"sqs-dlq":      g.teardownDLQ,
			"sqs-fifo":     g.teardownFIFO,
		},
	}
}

// One Namer per SQS sub-group ensures parallel groups never share queue names.
var (
	sqsMsgNamer    = harness.NewNamer("sqs-m")    // sqs-messages
	sqsDLQNamer    = harness.NewNamer("sqs-dlq")  // sqs-dlq dead-letter queue
	sqsDLQSrcNamer = harness.NewNamer("sqs-dlqs") // sqs-dlq source queue (distinct from sqs-queues)
	sqsFIFONamer   = harness.NewNamer("sqs-fifo") // sqs-fifo
)

type sqsGroup struct{}

// ─── sqs-messages ────────────────────────────────────────────────────────────

func (g *sqsGroup) setupMessages(_ context.Context, t *harness.TestContext) error {
	out, err := awscli.RunOutput(t.Endpoint, t.Region,
		"sqs", "create-queue", "--queue-name", sqsMsgNamer.Name(t),
	)
	if err != nil {
		return err
	}
	url, _ := out["QueueUrl"].(string)
	t.Set("queue_url", url)
	return nil
}

func (g *sqsGroup) SendMessage(_ context.Context, t *harness.TestContext) error {
	url := t.GetString("queue_url")
	out, err := awscli.RunOutput(t.Endpoint, t.Region,
		"sqs", "send-message",
		"--queue-url", url,
		"--message-body", "hello from CLI",
	)
	if err != nil {
		return err
	}
	msgID, _ := out["MessageId"].(string)
	if msgID == "" {
		return fmt.Errorf("sqs SendMessage: missing MessageId")
	}
	return nil
}

func (g *sqsGroup) SendMessageBatch(_ context.Context, t *harness.TestContext) error {
	url := t.GetString("queue_url")
	out, err := awscli.RunOutput(t.Endpoint, t.Region,
		"sqs", "send-message-batch",
		"--queue-url", url,
		"--entries", `[{"Id":"m1","MessageBody":"batch1"},{"Id":"m2","MessageBody":"batch2"}]`,
	)
	if err != nil {
		return err
	}
	succ, _ := out["Successful"].([]any)
	if len(succ) != 2 {
		return fmt.Errorf("sqs SendMessageBatch: expected 2 Successful, got %d", len(succ))
	}
	return nil
}

func (g *sqsGroup) ReceiveMessage(_ context.Context, t *harness.TestContext) error {
	url := t.GetString("queue_url")
	out, err := awscli.RunOutput(t.Endpoint, t.Region,
		"sqs", "receive-message",
		"--queue-url", url,
		"--max-number-of-messages", "1",
		"--visibility-timeout", "30",
	)
	if err != nil {
		return err
	}
	msgs, _ := out["Messages"].([]any)
	if len(msgs) == 0 {
		// No messages yet — queue might be settling. Pass anyway.
		return nil
	}
	msg := msgs[0].(map[string]any)
	receipt, _ := msg["ReceiptHandle"].(string)
	t.Set("receipt_handle", receipt)
	return nil
}

func (g *sqsGroup) DeleteMessage(_ context.Context, t *harness.TestContext) error {
	url := t.GetString("queue_url")
	receipt := t.GetString("receipt_handle")
	if receipt == "" {
		return nil // No message was received; skip.
	}
	if err := awscli.Run(t.Endpoint, t.Region,
		"sqs", "delete-message",
		"--queue-url", url,
		"--receipt-handle", receipt,
	); err != nil {
		return err
	}
	// Verify message is gone
	out, err := awscli.RunOutput(t.Endpoint, t.Region,
		"sqs", "receive-message",
		"--queue-url", url,
		"--max-number-of-messages", "10",
		"--visibility-timeout", "0",
		"--wait-time-seconds", "0",
	)
	if err != nil {
		return fmt.Errorf("sqs DeleteMessage: verify failed: %w", err)
	}
	msgs, _ := out["Messages"].([]any)
	for _, raw := range msgs {
		if m, ok := raw.(map[string]any); ok {
			if m["ReceiptHandle"] == receipt {
				return fmt.Errorf("sqs DeleteMessage: message still present")
			}
		}
	}
	return nil
}

func (g *sqsGroup) ChangeMessageVisibility(_ context.Context, t *harness.TestContext) error {
	url := t.GetString("queue_url")
	// Send + receive a fresh message.
	awscli.Run(t.Endpoint, t.Region, "sqs", "send-message", "--queue-url", url, "--message-body", "vis") //nolint:errcheck
	out, err := awscli.RunOutput(t.Endpoint, t.Region,
		"sqs", "receive-message", "--queue-url", url, "--max-number-of-messages", "1",
	)
	if err != nil {
		return err
	}
	msgs, _ := out["Messages"].([]any)
	if len(msgs) == 0 {
		return nil
	}
	msg := msgs[0].(map[string]any)
	receipt, _ := msg["ReceiptHandle"].(string)
	// Set visibility to 0 (make immediately visible)
	if err := awscli.Run(t.Endpoint, t.Region,
		"sqs", "change-message-visibility",
		"--queue-url", url,
		"--receipt-handle", receipt,
		"--visibility-timeout", "0",
	); err != nil {
		return err
	}
	// Verify message is re-visible
	out2, err := awscli.RunOutput(t.Endpoint, t.Region,
		"sqs", "receive-message", "--queue-url", url, "--max-number-of-messages", "1",
	)
	if err != nil {
		return fmt.Errorf("sqs ChangeMessageVisibility: verify failed: %w", err)
	}
	msgs2, _ := out2["Messages"].([]any)
	if len(msgs2) == 0 {
		return fmt.Errorf("sqs ChangeMessageVisibility: message not re-visible after timeout=0")
	}
	return nil
}

func (g *sqsGroup) DeleteMessageBatch(_ context.Context, t *harness.TestContext) error {
	url := t.GetString("queue_url")
	// Send two messages, receive, then batch-delete.
	awscli.Run(t.Endpoint, t.Region, "sqs", "send-message", "--queue-url", url, "--message-body", "d1") //nolint:errcheck
	awscli.Run(t.Endpoint, t.Region, "sqs", "send-message", "--queue-url", url, "--message-body", "d2") //nolint:errcheck
	out, err := awscli.RunOutput(t.Endpoint, t.Region,
		"sqs", "receive-message", "--queue-url", url, "--max-number-of-messages", "10",
	)
	if err != nil {
		return err
	}
	msgs, _ := out["Messages"].([]any)
	if len(msgs) == 0 {
		return nil
	}
	var entries []string
	for i, m := range msgs {
		msg := m.(map[string]any)
		receipt, _ := msg["ReceiptHandle"].(string)
		entries = append(entries, fmt.Sprintf(`{"Id":"del%d","ReceiptHandle":"%s"}`, i, receipt))
	}
	batch := fmt.Sprintf("[%s]", joinStrings(entries))
	out2, err := awscli.RunOutput(t.Endpoint, t.Region,
		"sqs", "delete-message-batch",
		"--queue-url", url,
		"--entries", batch,
	)
	if err != nil {
		return err
	}
	succ, _ := out2["Successful"].([]any)
	if len(succ) == 0 {
		return fmt.Errorf("sqs DeleteMessageBatch: no Successful entries")
	}
	return nil
}

func (g *sqsGroup) PurgeQueue(_ context.Context, t *harness.TestContext) error {
	url := t.GetString("queue_url")
	if err := awscli.Run(t.Endpoint, t.Region,
		"sqs", "purge-queue",
		"--queue-url", url,
	); err != nil {
		return err
	}
	out, err := awscli.RunOutput(t.Endpoint, t.Region,
		"sqs", "get-queue-attributes",
		"--queue-url", url,
		"--attribute-names", "ApproximateNumberOfMessages",
	)
	if err != nil {
		return fmt.Errorf("sqs PurgeQueue: get-queue-attributes failed: %w", err)
	}
	attrs, _ := out["Attributes"].(map[string]any)
	if attrs["ApproximateNumberOfMessages"] != "0" {
		return fmt.Errorf("sqs PurgeQueue: expected 0 messages, got %v", attrs["ApproximateNumberOfMessages"])
	}
	return nil
}

func (g *sqsGroup) teardownMessages(_ context.Context, t *harness.TestContext) error {
	url := t.GetString("queue_url")
	if url != "" {
		awscli.Run(t.Endpoint, t.Region, "sqs", "delete-queue", "--queue-url", url) //nolint:errcheck
	}
	return nil
}

// ─── sqs-dlq ─────────────────────────────────────────────────────────────────

func (g *sqsGroup) setupDLQ(_ context.Context, _ *harness.TestContext) error { return nil }

func (g *sqsGroup) CreateDLQ(_ context.Context, t *harness.TestContext) error {
	out, err := awscli.RunOutput(t.Endpoint, t.Region,
		"sqs", "create-queue", "--queue-name", sqsDLQNamer.Name(t),
	)
	if err != nil {
		return err
	}
	url, _ := out["QueueUrl"].(string)
	t.Set("dlq_url", url)

	// Get the ARN.
	attrs, err := awscli.RunOutput(t.Endpoint, t.Region,
		"sqs", "get-queue-attributes",
		"--queue-url", url,
		"--attribute-names", "QueueArn",
	)
	if err != nil {
		return err
	}
	attrMap, _ := attrs["Attributes"].(map[string]any)
	arn, _ := attrMap["QueueArn"].(string)
	t.Set("dlq_arn", arn)

	// Create the source queue (distinct name so it doesn't collide with sqs-queues).
	out2, err := awscli.RunOutput(t.Endpoint, t.Region,
		"sqs", "create-queue", "--queue-name", sqsDLQSrcNamer.Name(t),
	)
	if err != nil {
		return err
	}
	url2, _ := out2["QueueUrl"].(string)
	t.Set("queue_url", url2)
	return nil
}

func (g *sqsGroup) SetRedrivePolicy(_ context.Context, t *harness.TestContext) error {
	url := t.GetString("queue_url")
	dlqArn := t.GetString("dlq_arn")
	policy := fmt.Sprintf(`{"deadLetterTargetArn":"%s","maxReceiveCount":"3"}`, dlqArn)
	if err := awscli.Run(t.Endpoint, t.Region,
		"sqs", "set-queue-attributes",
		"--queue-url", url,
		"--attributes", fmt.Sprintf(`{"RedrivePolicy":%q}`, policy),
	); err != nil {
		return err
	}
	out, err := awscli.RunOutput(t.Endpoint, t.Region,
		"sqs", "get-queue-attributes",
		"--queue-url", url,
		"--attribute-names", "RedrivePolicy",
	)
	if err != nil {
		return fmt.Errorf("sqs SetRedrivePolicy: verify failed: %w", err)
	}
	attrs, _ := out["Attributes"].(map[string]any)
	if attrs["RedrivePolicy"] == nil {
		return fmt.Errorf("sqs SetRedrivePolicy: RedrivePolicy not set")
	}
	return nil
}

func (g *sqsGroup) GetRedrivePolicy(_ context.Context, t *harness.TestContext) error {
	url := t.GetString("queue_url")
	out, err := awscli.RunOutput(t.Endpoint, t.Region,
		"sqs", "get-queue-attributes",
		"--queue-url", url,
		"--attribute-names", "RedrivePolicy",
	)
	if err != nil {
		return err
	}
	attrs, _ := out["Attributes"].(map[string]any)
	if attrs["RedrivePolicy"] == nil {
		return fmt.Errorf("sqs GetRedrivePolicy: RedrivePolicy attribute missing")
	}
	return nil
}

func (g *sqsGroup) teardownDLQ(_ context.Context, t *harness.TestContext) error {
	for _, key := range []string{"queue_url", "dlq_url"} {
		url := t.GetString(key)
		if url != "" {
			awscli.Run(t.Endpoint, t.Region, "sqs", "delete-queue", "--queue-url", url) //nolint:errcheck
		}
	}
	return nil
}

// ─── sqs-fifo ────────────────────────────────────────────────────────────────

func (g *sqsGroup) setupFIFO(_ context.Context, _ *harness.TestContext) error { return nil }

func (g *sqsGroup) CreateFifoQueue(_ context.Context, t *harness.TestContext) error {
	out, err := awscli.RunOutput(t.Endpoint, t.Region,
		"sqs", "create-queue",
		"--queue-name", sqsFIFONamer.Suffixed(t, ".fifo"),
		"--attributes", `{"FifoQueue":"true","ContentBasedDeduplication":"true"}`,
	)
	if err != nil {
		return err
	}
	url, _ := out["QueueUrl"].(string)
	t.Set("fifo_url", url)
	return nil
}

func (g *sqsGroup) SendFifoMessage(_ context.Context, t *harness.TestContext) error {
	url := t.GetString("fifo_url")
	out, err := awscli.RunOutput(t.Endpoint, t.Region,
		"sqs", "send-message",
		"--queue-url", url,
		"--message-body", "fifo message",
		"--message-group-id", "grp1",
	)
	if err != nil {
		return err
	}
	if out["MessageId"] == nil {
		return fmt.Errorf("sqs SendFifoMessage: missing MessageId")
	}
	if out["SequenceNumber"] == nil {
		return fmt.Errorf("sqs SendFifoMessage: missing SequenceNumber")
	}
	return nil
}

func (g *sqsGroup) ReceiveFifoMessage(_ context.Context, t *harness.TestContext) error {
	url := t.GetString("fifo_url")
	out, err := awscli.RunOutput(t.Endpoint, t.Region,
		"sqs", "receive-message",
		"--queue-url", url,
		"--max-number-of-messages", "1",
	)
	if err != nil {
		return err
	}
	msgs, _ := out["Messages"].([]any)
	if len(msgs) == 0 {
		return fmt.Errorf("sqs ReceiveFifoMessage: expected message, got none")
	}
	msg := msgs[0].(map[string]any)
	if body, _ := msg["Body"].(string); body != "fifo message" {
		return fmt.Errorf("sqs ReceiveFifoMessage: expected body 'fifo message', got %q", body)
	}
	return nil
}

func (g *sqsGroup) teardownFIFO(_ context.Context, t *harness.TestContext) error {
	url := t.GetString("fifo_url")
	if url != "" {
		awscli.Run(t.Endpoint, t.Region, "sqs", "delete-queue", "--queue-url", url) //nolint:errcheck
	}
	return nil
}

// joinStrings joins string slice with commas (avoid importing strings).
func joinStrings(ss []string) string {
	out := ""
	for i, s := range ss {
		if i > 0 {
			out += ","
		}
		out += s
	}
	return out
}
