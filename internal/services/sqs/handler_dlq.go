package sqs

// handler_dlq.go — Dead Letter Queue support.
//
// Implements:
//   - RedrivePolicy validation (CreateQueue, SetQueueAttributes)
//   - DLQ message move on maxReceiveCount exceeded (called from ReceiveMessage)
//   - ListDeadLetterSourceQueues
//   - StartMessageMoveTask (redrive messages from DLQ back to source)

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/google/uuid"

	"github.com/overcast-sh/overcast/internal/events"
	"github.com/overcast-sh/overcast/internal/middleware"
	"github.com/overcast-sh/overcast/internal/protocol"
	"github.com/overcast-sh/overcast/internal/serviceutil"
)

type listDeadLetterSourceQueuesRequest struct {
	QueueUrl   string `json:"QueueUrl"`
	MaxResults *int   `json:"MaxResults,omitempty"`
	NextToken  string `json:"NextToken,omitempty"`
}

// listDeadLetterSourceQueuesResponse mirrors AWS's response shape.
//
// The JSON member is lower-camel `queueUrls`, unlike every other SQS list
// response (ListQueues returns `QueueUrls`). That inconsistency is real: see
// the Response Syntax in
// https://docs.aws.amazon.com/AWSSimpleQueueService/latest/APIReference/API_ListDeadLetterSourceQueues.html
// The SDKs model it as a member named `queueUrls` carrying an xmlName of
// `QueueUrl`, so the Query-protocol XML still renders repeated <QueueUrl>
// elements — see collectionElements in query.go.
type listDeadLetterSourceQueuesResponse struct {
	NextToken string   `json:"NextToken,omitempty"`
	QueueUrls []string `json:"queueUrls"`
}

// redrivePolicy is the parsed form of the SQS RedrivePolicy queue attribute.
//
// The AWS CLI and some SDK versions serialise maxReceiveCount as a JSON string
// ("3") rather than a JSON number (3).  UnmarshalJSON handles both so that
// parseRedrivePolicy never silently returns nil because of a type mismatch.
type redrivePolicy struct {
	DeadLetterTargetArn string `json:"deadLetterTargetArn"`
	MaxReceiveCount     int    `json:"maxReceiveCount"`
}

func (rp *redrivePolicy) UnmarshalJSON(data []byte) error {
	// Use an alias to avoid infinite recursion.
	type alias struct {
		DeadLetterTargetArn string          `json:"deadLetterTargetArn"`
		MaxReceiveCount     json.RawMessage `json:"maxReceiveCount"`
	}
	var a alias
	if err := json.Unmarshal(data, &a); err != nil {
		return err
	}
	rp.DeadLetterTargetArn = a.DeadLetterTargetArn

	if len(a.MaxReceiveCount) == 0 {
		return nil
	}
	// Try integer first (e.g. 3), then quoted string (e.g. "3").
	if err := json.Unmarshal(a.MaxReceiveCount, &rp.MaxReceiveCount); err != nil {
		var s string
		if err2 := json.Unmarshal(a.MaxReceiveCount, &s); err2 != nil {
			return fmt.Errorf("maxReceiveCount: %w", err)
		}
		n, err2 := strconv.Atoi(s)
		if err2 != nil {
			return fmt.Errorf("maxReceiveCount: cannot parse %q as integer", s)
		}
		rp.MaxReceiveCount = n
	}
	return nil
}

// parseRedrivePolicy decodes the RedrivePolicy JSON string stored in queue
// attributes. Returns nil if the attribute is absent or empty.
func parseRedrivePolicy(attrs map[string]string) (*redrivePolicy, error) {
	raw, ok := attrs["RedrivePolicy"]
	if !ok || raw == "" {
		return nil, nil
	}
	var rp redrivePolicy
	if err := json.Unmarshal([]byte(raw), &rp); err != nil {
		return nil, fmt.Errorf("invalid RedrivePolicy JSON: %w", err)
	}
	return &rp, nil
}

// queueNameFromARN extracts the queue name from an SQS ARN.
// e.g. "arn:aws:sqs:us-east-1:000000000000:my-dlq" → "my-dlq".
func queueNameFromARN(arn string) string {
	parts := strings.Split(arn, ":")
	if len(parts) == 0 {
		return ""
	}
	return parts[len(parts)-1]
}

// validateRedrivePolicy parses and validates a RedrivePolicy attribute value.
// It checks that the JSON is valid, maxReceiveCount is in range, and the
// target DLQ queue exists in the store. Returns an AWSError if invalid.
func (h *Handler) validateRedrivePolicy(r *http.Request, attrs map[string]string) *protocol.AWSError {
	return h.validateRedrivePolicyContext(r.Context(), attrs)
}

func (h *Handler) validateRedrivePolicyContext(ctx context.Context, attrs map[string]string) *protocol.AWSError {
	rp, err := parseRedrivePolicy(attrs)
	if err != nil {
		return &protocol.AWSError{
			Code:       "InvalidParameterValue",
			Message:    err.Error(),
			HTTPStatus: http.StatusBadRequest,
		}
	}
	if rp == nil {
		return nil
	}
	if rp.MaxReceiveCount < 1 || rp.MaxReceiveCount > 1000 {
		return &protocol.AWSError{
			Code:       "InvalidParameterValue",
			Message:    "RedrivePolicy.maxReceiveCount must be between 1 and 1000",
			HTTPStatus: http.StatusBadRequest,
		}
	}
	if rp.DeadLetterTargetArn == "" {
		return &protocol.AWSError{
			Code:       "InvalidParameterValue",
			Message:    "RedrivePolicy.deadLetterTargetArn must not be empty",
			HTTPStatus: http.StatusBadRequest,
		}
	}
	dlqName := queueNameFromARN(rp.DeadLetterTargetArn)
	if dlqName == "" {
		return &protocol.AWSError{
			Code:       "InvalidParameterValue",
			Message:    "RedrivePolicy.deadLetterTargetArn is not a valid SQS ARN",
			HTTPStatus: http.StatusBadRequest,
		}
	}
	// Real AWS requires the DLQ to be in the same region as the source queue.
	if dlqRegion := serviceutil.ARNRegion(rp.DeadLetterTargetArn); dlqRegion != "" {
		if srcRegion := middleware.RegionFromContext(ctx, h.cfg.Region); dlqRegion != srcRegion {
			return &protocol.AWSError{
				Code:       "InvalidParameterValue",
				Message:    fmt.Sprintf("Value %s for parameter deadLetterTargetArn is invalid. Reason: Dead letter target is not in the same region as the source queue.", rp.DeadLetterTargetArn),
				HTTPStatus: http.StatusBadRequest,
			}
		}
	}
	if _, aerr := h.store.getQueue(ctx, dlqName); aerr != nil {
		return &protocol.AWSError{
			Code:       "InvalidParameterValue",
			Message:    fmt.Sprintf("Value %s for parameter deadLetterTargetArn is invalid. Reason: Dead letter queue does not exist.", rp.DeadLetterTargetArn),
			HTTPStatus: http.StatusBadRequest,
		}
	}
	return nil
}

// moveToDLQ moves msg from srcQueueName to the dead letter queue described by
// rp. The message body and user-defined attributes are preserved; system
// attributes are reset for the new queue. The DeadLetterSourceQueueArn system
// attribute is injected so consumers can identify where the message came from.
//
// Called from ReceiveMessage when ApproximateReceiveCount >= maxReceiveCount,
// and from sqsReceiver (Lambda ESM) for the same condition.
func (h *Handler) moveToDLQ(ctx context.Context, srcQueueName string, srcQueue *Queue, rp *redrivePolicy, msg *Message) *protocol.AWSError {
	dlqName := queueNameFromARN(rp.DeadLetterTargetArn)

	newID := uuid.New().String()
	newBody := msg.Body
	bodyMD5 := md5Hex([]byte(newBody))

	dlqMsg := &Message{
		MessageID:     newID,
		ReceiptHandle: encodeReceiptHandle(dlqName, newID),
		Body:          newBody,
		MD5OfBody:     bodyMD5,
		SentTimestamp: h.clk.Now().UnixMilli(),
		VisibleAfter:  h.clk.Now(),
		Attributes: map[string]string{
			"SenderId":                         h.cfg.AccountID,
			"SentTimestamp":                    strconv.FormatInt(h.clk.Now().UnixMilli(), 10),
			"ApproximateReceiveCount":          "0",
			"ApproximateFirstReceiveTimestamp": "0",
			"DeadLetterSourceQueueArn":         srcQueue.ARN,
		},
		MessageAttributes: msg.MessageAttributes,
	}

	if aerr := h.store.putMessage(ctx, dlqName, dlqMsg); aerr != nil {
		return aerr
	}
	if aerr := h.store.deleteMessage(ctx, srcQueueName, msg.MessageID); aerr != nil {
		return aerr
	}

	if h.bus != nil {
		h.bus.Publish(ctx, events.Event{
			Type:   events.SQSMessageDLQ,
			Time:   h.clk.Now(),
			Source: serviceName,
			Payload: events.SQSDLQPayload{
				SourceQueue: srcQueueName,
				DLQQueue:    dlqName,
				MessageID:   dlqMsg.MessageID,
			},
		})
	}

	return nil
}

// ListDeadLetterSourceQueues handles the SQS ListDeadLetterSourceQueues operation.
// It returns all queues whose RedrivePolicy targets the specified DLQ.
// AWS docs: https://docs.aws.amazon.com/AWSSimpleQueueService/latest/APIReference/API_ListDeadLetterSourceQueues.html
func (h *Handler) ListDeadLetterSourceQueues(w http.ResponseWriter, r *http.Request) {
	var req listDeadLetterSourceQueuesRequest
	if !serviceutil.DecodeJSON(w, r, &req) {
		return
	}
	if !serviceutil.RequireString(w, r, req.QueueUrl, "QueueUrl") {
		return
	}

	// Share the typed implementation so the two dispatch paths cannot drift on
	// the response member name.
	resp, aerr := h.listDeadLetterSourceQueuesTyped(r.Context(), &req)
	if aerr != nil {
		writeJSONError(w, r, aerr)
		return
	}
	protocol.WriteJSON(w, r, http.StatusOK, resp)
}

func (h *Handler) listDeadLetterSourceQueuesTyped(ctx context.Context, in *listDeadLetterSourceQueuesRequest) (*listDeadLetterSourceQueuesResponse, *protocol.AWSError) {
	if in.QueueUrl == "" {
		return nil, protocol.ErrMissingParameter("QueueUrl")
	}

	dlqName := queueNameFromURL(in.QueueUrl)
	dlq, aerr := h.store.getQueue(ctx, dlqName)
	if aerr != nil {
		return nil, aerr
	}

	allQueues, aerr := h.store.listQueues(ctx, "")
	if aerr != nil {
		return nil, aerr
	}

	var sourceURLs []string
	for _, q := range allQueues {
		rp, err := parseRedrivePolicy(q.Attributes)
		if err != nil || rp == nil {
			continue
		}
		if rp.DeadLetterTargetArn == dlq.ARN {
			sourceURLs = append(sourceURLs, h.queueURL(ctx, q.Name))
		}
	}
	if sourceURLs == nil {
		sourceURLs = []string{}
	}

	sourceURLs, token, aerr := paginateQueueURLs(sourceURLs, in.MaxResults, in.NextToken)
	if aerr != nil {
		return nil, aerr
	}
	return &listDeadLetterSourceQueuesResponse{QueueUrls: sourceURLs, NextToken: token}, nil
}

// ── StartMessageMoveTask ───────────────────────────────────────────────────

type startMessageMoveTaskRequest struct {
	SourceArn      string `json:"SourceArn"`
	DestinationArn string `json:"DestinationArn"`
}

type startMessageMoveTaskResponse struct {
	TaskHandle string `json:"TaskHandle"`
}

// StartMessageMoveTask redrives messages from a DLQ back to their source queue.
// AWS docs: https://docs.aws.amazon.com/AWSSimpleQueueService/latest/APIReference/API_StartMessageMoveTask.html
//
// In real AWS this is an async task. In Overcast the move is performed
// synchronously since there is no need for background task management in a
// local emulator — the response is returned once all messages are moved.
func (h *Handler) StartMessageMoveTask(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SourceArn      string `json:"SourceArn"`
		DestinationArn string `json:"DestinationArn"`
	}
	if !serviceutil.DecodeJSON(w, r, &req) {
		return
	}
	if !serviceutil.RequireString(w, r, req.SourceArn, "SourceArn") {
		return
	}

	resp, aerr := h.startMessageMoveTaskTyped(r.Context(), &startMessageMoveTaskRequest{
		SourceArn:      req.SourceArn,
		DestinationArn: req.DestinationArn,
	})
	if aerr != nil {
		writeJSONError(w, r, aerr)
		return
	}
	protocol.WriteJSON(w, r, http.StatusOK, resp)
}

func (h *Handler) startMessageMoveTaskTyped(ctx context.Context, in *startMessageMoveTaskRequest) (*startMessageMoveTaskResponse, *protocol.AWSError) {
	if in.SourceArn == "" {
		return nil, protocol.ErrMissingParameter("SourceArn")
	}

	// Resolve the DLQ (source of redrive).
	dlqName := queueNameFromARN(in.SourceArn)
	if dlqName == "" {
		return nil, &protocol.AWSError{
			Code:       "InvalidParameterValue",
			Message:    "SourceArn is not a valid SQS ARN",
			HTTPStatus: http.StatusBadRequest,
		}
	}
	_, aerr := h.store.getQueue(ctx, dlqName)
	if aerr != nil {
		return nil, aerr
	}

	// If an explicit DestinationArn is provided, validate it exists.
	var explicitDest string
	if in.DestinationArn != "" {
		explicitDest = queueNameFromARN(in.DestinationArn)
		if _, aerr := h.store.getQueue(ctx, explicitDest); aerr != nil {
			return nil, &protocol.AWSError{
				Code:       "InvalidParameterValue",
				Message:    fmt.Sprintf("Destination queue %q does not exist", explicitDest),
				HTTPStatus: http.StatusBadRequest,
			}
		}
	}

	// Move all messages from the DLQ back to their source queues.
	messages, aerr := h.store.listMessages(ctx, dlqName)
	if aerr != nil {
		return nil, aerr
	}

	// Track which destination queues received messages (for the event).
	destQueues := map[string]bool{}

	for _, msg := range messages {
		// Determine destination per-message: explicit > per-message attribute > error.
		dest := explicitDest
		if dest == "" {
			// Real AWS uses the DeadLetterSourceQueueArn message attribute to
			// route each message back to its original source queue.
			if srcArn, ok := msg.Attributes["DeadLetterSourceQueueArn"]; ok && srcArn != "" {
				dest = queueNameFromARN(srcArn)
			}
		}
		if dest == "" {
			return nil, &protocol.AWSError{
				Code:       "InvalidParameterValue",
				Message:    "Cannot determine destination for message " + msg.MessageID + ". Specify DestinationArn.",
				HTTPStatus: http.StatusBadRequest,
			}
		}

		// Validate destination exists (cache check via map to avoid repeated lookups).
		if !destQueues[dest] {
			if _, aerr := h.store.getQueue(ctx, dest); aerr != nil {
				return nil, &protocol.AWSError{
					Code:       "InvalidParameterValue",
					Message:    fmt.Sprintf("Destination queue %q does not exist", dest),
					HTTPStatus: http.StatusBadRequest,
				}
			}
			destQueues[dest] = true
		}

		// Redriven messages are new: new ID, new timestamps, receive count = 0.
		newID := uuid.New().String()
		bodyMD5 := md5Hex([]byte(msg.Body))
		newMsg := &Message{
			MessageID:     newID,
			ReceiptHandle: encodeReceiptHandle(dest, newID),
			Body:          msg.Body,
			MD5OfBody:     bodyMD5,
			SentTimestamp: h.clk.Now().UnixMilli(),
			VisibleAfter:  h.clk.Now(),
			Attributes: map[string]string{
				"SenderId":                         h.cfg.AccountID,
				"SentTimestamp":                    strconv.FormatInt(h.clk.Now().UnixMilli(), 10),
				"ApproximateReceiveCount":          "0",
				"ApproximateFirstReceiveTimestamp": "0",
			},
			MessageAttributes: msg.MessageAttributes,
		}
		if aerr := h.store.putMessage(ctx, dest, newMsg); aerr != nil {
			return nil, aerr
		}
		if aerr := h.store.deleteMessage(ctx, dlqName, msg.MessageID); aerr != nil {
			return nil, aerr
		}
	}

	if h.bus != nil {
		// Report the primary destination for the event.
		var primaryDest string
		for d := range destQueues {
			primaryDest = d
			break
		}
		h.bus.Publish(ctx, events.Event{
			Type:   events.SQSMessageRedrive,
			Time:   h.clk.Now(),
			Source: serviceName,
			Payload: events.SQSRedrivePayload{
				SourceQueue:      dlqName,
				DestinationQueue: primaryDest,
				MessageCount:     len(messages),
			},
		})
	}

	taskHandle := uuid.New().String()
	return &startMessageMoveTaskResponse{TaskHandle: taskHandle}, nil
}
