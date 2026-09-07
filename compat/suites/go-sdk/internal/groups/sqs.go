package groups

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	sqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/overcast-sh/overcast-compat-go-sdk/internal/clients"
	"github.com/overcast-sh/overcast-compat-go-sdk/internal/harness"
)

// SQS returns the SQS service group.
func SQS(c *clients.Clients) ServiceGroup {
	s := &sqsGroup{c: c}
	return ServiceGroup{
		Impls: map[string]harness.TestFn{
			"sqs-messages:SendMessage":             s.SendMessage,
			"sqs-messages:ReceiveMessage":          s.ReceiveMessage,
			"sqs-messages:DeleteMessage":           s.DeleteMessage,
			"sqs-messages:SendMessageBatch":        s.SendMessageBatch,
			"sqs-messages:PurgeQueue":              s.PurgeQueue,
			"sqs-messages:ChangeMessageVisibility": s.ChangeMessageVisibility,
			"sqs-messages:DeleteMessageBatch":      s.DeleteMessageBatch,
			"sqs-dlq:CreateDLQ":                    s.CreateDLQ,
			"sqs-dlq:SetRedrivePolicy":             s.SetRedrivePolicy,
			"sqs-dlq:GetRedrivePolicy":             s.GetRedrivePolicy,
			"sqs-fifo:CreateFifoQueue":             s.CreateFifoQueue,
			"sqs-fifo:SendFifoMessage":             s.SendFifoMessage,
			"sqs-fifo:ReceiveFifoMessage":          s.ReceiveFifoMessage,
		},
		Setup: map[string]func(context.Context, *harness.TestContext) error{
			"sqs-messages": s.setupMessages,
			"sqs-dlq":      s.setupDLQ,
			"sqs-fifo":     s.setupFifo,
		},
		Teardown: map[string]func(context.Context, *harness.TestContext) error{
			"sqs-messages": s.teardownMessages,
			"sqs-dlq":      s.teardownDLQ,
			"sqs-fifo":     s.teardownFifo,
		},
	}
}

type sqsGroup struct{ c *clients.Clients }

func (s *sqsGroup) client() *sqs.Client { return s.c.SQS() }

func (s *sqsGroup) deleteQueue(ctx context.Context, url string) {
	s.client().DeleteQueue(ctx, &sqs.DeleteQueueInput{QueueUrl: aws.String(url)}) //nolint:errcheck
}

// ── sqs-messages ─────────────────────────────────────────────────────────────

func (s *sqsGroup) setupMessages(ctx context.Context, t *harness.TestContext) error {
	name := fmt.Sprintf("%s-sqsmsg", t.RunID)
	resp, err := s.client().CreateQueue(ctx, &sqs.CreateQueueInput{QueueName: aws.String(name)})
	if err != nil {
		return err
	}
	t.Set("sqs_msg_queue_url", aws.ToString(resp.QueueUrl))
	return nil
}

func (s *sqsGroup) teardownMessages(ctx context.Context, t *harness.TestContext) error {
	if url := t.GetString("sqs_msg_queue_url"); url != "" {
		s.deleteQueue(ctx, url)
	}
	return nil
}

func (s *sqsGroup) SendMessage(ctx context.Context, t *harness.TestContext) error {
	url := t.GetString("sqs_msg_queue_url")
	resp, err := s.client().SendMessage(ctx, &sqs.SendMessageInput{
		QueueUrl:    aws.String(url),
		MessageBody: aws.String("hello-sqs"),
	})
	if err != nil {
		return err
	}
	t.Set("sqs_msg_id", aws.ToString(resp.MessageId))
	return nil
}

func (s *sqsGroup) ReceiveMessage(ctx context.Context, t *harness.TestContext) error {
	url := t.GetString("sqs_msg_queue_url")
	resp, err := s.client().ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
		QueueUrl:            aws.String(url),
		MaxNumberOfMessages: 10,
		WaitTimeSeconds:     1,
	})
	if err != nil {
		return err
	}
	if len(resp.Messages) == 0 {
		return fmt.Errorf("ReceiveMessage: no messages received")
	}
	t.Set("sqs_receipt_handle", aws.ToString(resp.Messages[0].ReceiptHandle))
	return nil
}

func (s *sqsGroup) DeleteMessage(ctx context.Context, t *harness.TestContext) error {
	url := t.GetString("sqs_msg_queue_url")
	handle := t.GetString("sqs_receipt_handle")
	_, err := s.client().DeleteMessage(ctx, &sqs.DeleteMessageInput{
		QueueUrl:      aws.String(url),
		ReceiptHandle: aws.String(handle),
	})
	if err != nil {
		return err
	}
	// Verify message is gone
	recv, err := s.client().ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
		QueueUrl:            aws.String(url),
		MaxNumberOfMessages: 1,
		WaitTimeSeconds:     0,
	})
	if err != nil {
		return fmt.Errorf("DeleteMessage: ReceiveMessage verify failed: %w", err)
	}
	if len(recv.Messages) > 0 {
		return fmt.Errorf("DeleteMessage: message still visible after delete")
	}
	return nil
}

func (s *sqsGroup) SendMessageBatch(ctx context.Context, t *harness.TestContext) error {
	url := t.GetString("sqs_msg_queue_url")
	entries := make([]sqstypes.SendMessageBatchRequestEntry, 5)
	for i := range entries {
		entries[i] = sqstypes.SendMessageBatchRequestEntry{
			Id:          aws.String(fmt.Sprintf("msg-%d", i)),
			MessageBody: aws.String(fmt.Sprintf("batch-%d", i)),
		}
	}
	resp, err := s.client().SendMessageBatch(ctx, &sqs.SendMessageBatchInput{
		QueueUrl: aws.String(url),
		Entries:  entries,
	})
	if err != nil {
		return err
	}
	if len(resp.Failed) > 0 {
		return fmt.Errorf("SendMessageBatch: %d failed", len(resp.Failed))
	}
	return nil
}

func (s *sqsGroup) DeleteMessageBatch(ctx context.Context, t *harness.TestContext) error {
	url := t.GetString("sqs_msg_queue_url")
	// Send 2 messages
	for i := 0; i < 2; i++ {
		_, err := s.client().SendMessage(ctx, &sqs.SendMessageInput{
			QueueUrl:    aws.String(url),
			MessageBody: aws.String(fmt.Sprintf("batch-del-%d", i)),
		})
		if err != nil {
			return fmt.Errorf("DeleteMessageBatch: send %d: %w", i, err)
		}
	}
	// Receive them
	recv, err := s.client().ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
		QueueUrl:            aws.String(url),
		MaxNumberOfMessages: 10,
		WaitTimeSeconds:     2,
	})
	if err != nil {
		return err
	}
	if len(recv.Messages) < 2 {
		return fmt.Errorf("DeleteMessageBatch: expected at least 2 messages, got %d", len(recv.Messages))
	}
	entries := make([]sqstypes.DeleteMessageBatchRequestEntry, len(recv.Messages))
	for i, m := range recv.Messages {
		entries[i] = sqstypes.DeleteMessageBatchRequestEntry{
			Id:            aws.String(fmt.Sprintf("%d", i)),
			ReceiptHandle: m.ReceiptHandle,
		}
	}
	resp, err := s.client().DeleteMessageBatch(ctx, &sqs.DeleteMessageBatchInput{
		QueueUrl: aws.String(url),
		Entries:  entries,
	})
	if err != nil {
		return err
	}
	if len(resp.Successful) < 2 {
		return fmt.Errorf("DeleteMessageBatch: expected 2 successful, got %d", len(resp.Successful))
	}
	return nil
}

func (s *sqsGroup) PurgeQueue(ctx context.Context, t *harness.TestContext) error {
	url := t.GetString("sqs_msg_queue_url")
	_, err := s.client().PurgeQueue(ctx, &sqs.PurgeQueueInput{QueueUrl: aws.String(url)})
	if err != nil {
		return err
	}
	// Verify queue is empty
	recv, err := s.client().ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
		QueueUrl:            aws.String(url),
		MaxNumberOfMessages: 10,
		WaitTimeSeconds:     0,
	})
	if err != nil {
		return fmt.Errorf("PurgeQueue: ReceiveMessage verify failed: %w", err)
	}
	if len(recv.Messages) > 0 {
		return fmt.Errorf("PurgeQueue: %d messages remain after purge", len(recv.Messages))
	}
	return nil
}

func (s *sqsGroup) ChangeMessageVisibility(ctx context.Context, t *harness.TestContext) error {
	url := t.GetString("sqs_msg_queue_url")
	// Send a message first
	s.client().SendMessage(ctx, &sqs.SendMessageInput{ //nolint:errcheck
		QueueUrl:    aws.String(url),
		MessageBody: aws.String("vis-timeout-test"),
	})
	recv, err := s.client().ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
		QueueUrl: aws.String(url), MaxNumberOfMessages: 1, WaitTimeSeconds: 1,
	})
	if err != nil || len(recv.Messages) == 0 {
		return fmt.Errorf("ChangeMessageVisibility: no messages to change")
	}
	_, err = s.client().ChangeMessageVisibility(ctx, &sqs.ChangeMessageVisibilityInput{
		QueueUrl:          aws.String(url),
		ReceiptHandle:     recv.Messages[0].ReceiptHandle,
		VisibilityTimeout: 0,
	})
	if err != nil {
		return err
	}
	// With timeout=0, message should be immediately re-visible
	recv2, err := s.client().ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
		QueueUrl: aws.String(url), MaxNumberOfMessages: 1, WaitTimeSeconds: 1,
	})
	if err != nil {
		return fmt.Errorf("ChangeMessageVisibility: ReceiveMessage verify failed: %w", err)
	}
	if len(recv2.Messages) == 0 {
		return fmt.Errorf("ChangeMessageVisibility: message not re-visible after setting timeout to 0")
	}
	return nil
}

// ── sqs-dlq ───────────────────────────────────────────────────────────────────

func (s *sqsGroup) setupDLQ(ctx context.Context, t *harness.TestContext) error {
	dlqName := fmt.Sprintf("%s-dlq", t.RunID)
	srcName := fmt.Sprintf("%s-dlqsrc", t.RunID)
	dlqResp, err := s.client().CreateQueue(ctx, &sqs.CreateQueueInput{QueueName: aws.String(dlqName)})
	if err != nil {
		return err
	}
	srcResp, err := s.client().CreateQueue(ctx, &sqs.CreateQueueInput{QueueName: aws.String(srcName)})
	if err != nil {
		return err
	}
	t.Set("sqs_dlq_url", aws.ToString(dlqResp.QueueUrl))
	t.Set("sqs_src_url", aws.ToString(srcResp.QueueUrl))
	// Get DLQ ARN
	attr, err := s.client().GetQueueAttributes(ctx, &sqs.GetQueueAttributesInput{
		QueueUrl:       dlqResp.QueueUrl,
		AttributeNames: []sqstypes.QueueAttributeName{"QueueArn"},
	})
	if err != nil {
		return err
	}
	t.Set("sqs_dlq_arn", attr.Attributes["QueueArn"])
	return nil
}

func (s *sqsGroup) teardownDLQ(ctx context.Context, t *harness.TestContext) error {
	for _, key := range []string{"sqs_dlq_url", "sqs_src_url"} {
		if url := t.GetString(key); url != "" {
			s.deleteQueue(ctx, url)
		}
	}
	return nil
}

func (s *sqsGroup) CreateDLQ(ctx context.Context, t *harness.TestContext) error {
	// DLQ already created in setup
	if t.GetString("sqs_dlq_url") == "" {
		return fmt.Errorf("CreateDLQ: dlq_url not set in context")
	}
	return nil
}

func (s *sqsGroup) SetQueueAttributesDLQ(ctx context.Context, t *harness.TestContext) error {
	srcURL := t.GetString("sqs_src_url")
	dlqARN := t.GetString("sqs_dlq_arn")
	policy := fmt.Sprintf(`{"deadLetterTargetArn":"%s","maxReceiveCount":"1"}`, dlqARN)
	_, err := s.client().SetQueueAttributes(ctx, &sqs.SetQueueAttributesInput{
		QueueUrl:   aws.String(srcURL),
		Attributes: map[string]string{"RedrivePolicy": policy, "VisibilityTimeout": "0"},
	})
	return err
}

func (s *sqsGroup) SetRedrivePolicy(ctx context.Context, t *harness.TestContext) error {
	srcURL := t.GetString("sqs_src_url")
	dlqARN := t.GetString("sqs_dlq_arn")
	policy := fmt.Sprintf(`{"deadLetterTargetArn":"%s","maxReceiveCount":"3"}`, dlqARN)
	_, err := s.client().SetQueueAttributes(ctx, &sqs.SetQueueAttributesInput{
		QueueUrl:   aws.String(srcURL),
		Attributes: map[string]string{"RedrivePolicy": policy},
	})
	return err
}

func (s *sqsGroup) GetRedrivePolicy(ctx context.Context, t *harness.TestContext) error {
	srcURL := t.GetString("sqs_src_url")
	resp, err := s.client().GetQueueAttributes(ctx, &sqs.GetQueueAttributesInput{
		QueueUrl:       aws.String(srcURL),
		AttributeNames: []sqstypes.QueueAttributeName{"RedrivePolicy"},
	})
	if err != nil {
		return err
	}
	policyStr, ok := resp.Attributes["RedrivePolicy"]
	if !ok || policyStr == "" {
		return fmt.Errorf("GetRedrivePolicy: missing RedrivePolicy attribute")
	}
	var policy struct {
		MaxReceiveCount json.Number `json:"maxReceiveCount"`
	}
	if err := json.Unmarshal([]byte(policyStr), &policy); err != nil {
		return fmt.Errorf("GetRedrivePolicy: parse policy: %w", err)
	}
	if policy.MaxReceiveCount.String() != "3" {
		return fmt.Errorf("GetRedrivePolicy: expected maxReceiveCount=3, got %s", policy.MaxReceiveCount)
	}
	return nil
}

func (s *sqsGroup) SendToDLQ(ctx context.Context, t *harness.TestContext) error {
	srcURL := t.GetString("sqs_src_url")
	_, err := s.client().SendMessage(ctx, &sqs.SendMessageInput{
		QueueUrl:    aws.String(srcURL),
		MessageBody: aws.String("dead-letter-test"),
	})
	return err
}

// ── sqs-fifo ──────────────────────────────────────────────────────────────────

func (s *sqsGroup) setupFifo(ctx context.Context, t *harness.TestContext) error {
	name := fmt.Sprintf("%s-fifo.fifo", t.RunID)
	resp, err := s.client().CreateQueue(ctx, &sqs.CreateQueueInput{
		QueueName: aws.String(name),
		Attributes: map[string]string{
			"FifoQueue":                 "true",
			"ContentBasedDeduplication": "true",
		},
	})
	if err != nil {
		return err
	}
	t.Set("sqs_fifo_url", aws.ToString(resp.QueueUrl))
	return nil
}

func (s *sqsGroup) teardownFifo(ctx context.Context, t *harness.TestContext) error {
	if url := t.GetString("sqs_fifo_url"); url != "" {
		s.deleteQueue(ctx, url)
	}
	return nil
}

func (s *sqsGroup) CreateFifoQueue(ctx context.Context, t *harness.TestContext) error {
	// Set up in setup; verify it exists
	if t.GetString("sqs_fifo_url") == "" {
		return fmt.Errorf("CreateFifoQueue: fifo_url not set in context")
	}
	return nil
}

func (s *sqsGroup) SendMessageFifo(ctx context.Context, t *harness.TestContext) error {
	url := t.GetString("sqs_fifo_url")
	resp, err := s.client().SendMessage(ctx, &sqs.SendMessageInput{
		QueueUrl:       aws.String(url),
		MessageBody:    aws.String("fifo-message-1"),
		MessageGroupId: aws.String("group1"),
	})
	if err != nil {
		return err
	}
	if aws.ToString(resp.MessageId) == "" {
		return fmt.Errorf("SendMessageFifo: missing MessageId")
	}
	return nil
}

func (s *sqsGroup) ReceiveMessageFifo(ctx context.Context, t *harness.TestContext) error {
	url := t.GetString("sqs_fifo_url")
	resp, err := s.client().ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
		QueueUrl:            aws.String(url),
		MaxNumberOfMessages: 1,
		WaitTimeSeconds:     1,
	})
	if err != nil {
		return err
	}
	if len(resp.Messages) == 0 {
		return fmt.Errorf("ReceiveMessageFifo: no messages")
	}
	// Delete so it doesn't block dedup
	s.client().DeleteMessage(ctx, &sqs.DeleteMessageInput{ //nolint:errcheck
		QueueUrl:      aws.String(url),
		ReceiptHandle: resp.Messages[0].ReceiptHandle,
	})
	return nil
}

func (s *sqsGroup) SendFifoMessage(ctx context.Context, t *harness.TestContext) error {
	url := t.GetString("sqs_fifo_url")
	resp, err := s.client().SendMessage(ctx, &sqs.SendMessageInput{
		QueueUrl:       aws.String(url),
		MessageBody:    aws.String("fifo-msg"),
		MessageGroupId: aws.String("grp1"),
	})
	if err != nil {
		return err
	}
	if aws.ToString(resp.MessageId) == "" {
		return fmt.Errorf("SendFifoMessage: missing MessageId")
	}
	return nil
}

func (s *sqsGroup) ReceiveFifoMessage(ctx context.Context, t *harness.TestContext) error {
	url := t.GetString("sqs_fifo_url")
	resp, err := s.client().ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
		QueueUrl:            aws.String(url),
		MaxNumberOfMessages: 1,
		WaitTimeSeconds:     1,
	})
	if err != nil {
		return err
	}
	if len(resp.Messages) == 0 {
		return fmt.Errorf("ReceiveFifoMessage: no message received")
	}
	if aws.ToString(resp.Messages[0].Body) != "fifo-msg" {
		return fmt.Errorf("ReceiveFifoMessage: expected \"fifo-msg\", got %q", aws.ToString(resp.Messages[0].Body))
	}
	// Clean up to not block FIFO group
	s.client().DeleteMessage(ctx, &sqs.DeleteMessageInput{ //nolint:errcheck
		QueueUrl:      aws.String(url),
		ReceiptHandle: resp.Messages[0].ReceiptHandle,
	})
	return nil
}

func (s *sqsGroup) SendMessageFifoDedup(ctx context.Context, t *harness.TestContext) error {
	url := t.GetString("sqs_fifo_url")
	// Wait for dedup window to expire or just send with explicit dedup ID
	time.Sleep(100 * time.Millisecond)
	resp, err := s.client().SendMessage(ctx, &sqs.SendMessageInput{
		QueueUrl:               aws.String(url),
		MessageBody:            aws.String("deduplicated"),
		MessageGroupId:         aws.String("group2"),
		MessageDeduplicationId: aws.String("dedup-1"),
	})
	if err != nil {
		return err
	}
	// Send same dedup ID again — should succeed but not enqueue
	resp2, err := s.client().SendMessage(ctx, &sqs.SendMessageInput{
		QueueUrl:               aws.String(url),
		MessageBody:            aws.String("deduplicated"),
		MessageGroupId:         aws.String("group2"),
		MessageDeduplicationId: aws.String("dedup-1"),
	})
	if err != nil {
		return err
	}
	// Both should return the same MessageId
	if aws.ToString(resp.MessageId) != aws.ToString(resp2.MessageId) {
		return fmt.Errorf("SendMessageFifoDedup: expected same MessageId, got %q and %q",
			aws.ToString(resp.MessageId), aws.ToString(resp2.MessageId))
	}
	return nil
}
