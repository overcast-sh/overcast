package sqs

// handler_queue.go contains handlers for SQS queue lifecycle operations:
// CreateQueue, GetQueueURL, GetQueueAttributes, SetQueueAttributes,
// DeleteQueue, ListQueues, PurgeQueue.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/overcast-sh/overcast/internal/events"
	"github.com/overcast-sh/overcast/internal/middleware"
	"github.com/overcast-sh/overcast/internal/protocol"
	"github.com/overcast-sh/overcast/internal/serviceutil"
)

// ---- Request / response types ----------------------------------------------

type createQueueRequest struct {
	QueueName  string            `json:"QueueName"`
	Attributes map[string]string `json:"Attributes,omitempty"`
	Tags       map[string]string `json:"tags,omitempty"`
}

type createQueueResponse struct {
	QueueUrl string `json:"QueueUrl"`
}

type getQueueURLRequest struct {
	QueueName              string `json:"QueueName"`
	QueueOwnerAWSAccountId string `json:"QueueOwnerAWSAccountId,omitempty"`
}

type getQueueURLResponse struct {
	QueueUrl string `json:"QueueUrl"`
}

type getQueueAttributesRequest struct {
	QueueUrl       string   `json:"QueueUrl"`
	AttributeNames []string `json:"AttributeNames"`
}

type getQueueAttributesResponse struct {
	Attributes map[string]string `json:"Attributes"`
}

type setQueueAttributesRequest struct {
	QueueUrl   string            `json:"QueueUrl"`
	Attributes map[string]string `json:"Attributes"`
}

type deleteQueueRequest struct {
	QueueUrl string `json:"QueueUrl"`
}

type listQueuesRequest struct {
	QueueNamePrefix string `json:"QueueNamePrefix,omitempty"`
	MaxResults      *int   `json:"MaxResults,omitempty"`
	NextToken       string `json:"NextToken,omitempty"`
}

type listQueuesResponse struct {
	NextToken string   `json:"NextToken,omitempty"`
	QueueUrls []string `json:"QueueUrls"`
}

type purgeQueueRequest struct {
	QueueUrl string `json:"QueueUrl"`
}

// ---- Typed operations ------------------------------------------------------

func (h *Handler) createQueueTyped(ctx context.Context, in *createQueueRequest) (*createQueueResponse, *protocol.AWSError) {
	if in.QueueName == "" {
		return nil, protocol.ErrMissingParameter("QueueName")
	}
	if aerr := serviceutil.QueueName(in.QueueName); aerr != nil {
		return nil, aerr
	}

	isFifo := strings.HasSuffix(in.QueueName, ".fifo") || in.Attributes["FifoQueue"] == "true"
	if in.Attributes["FifoQueue"] == "true" && !strings.HasSuffix(in.QueueName, ".fifo") {
		return nil, &protocol.AWSError{
			Code:       "InvalidParameterValue",
			Message:    "The queue name must end in .fifo for FIFO queues.",
			HTTPStatus: http.StatusBadRequest,
		}
	}

	existing, _ := h.store.getQueue(ctx, in.QueueName)
	if existing != nil {
		for k, v := range in.Attributes {
			if existing.Attributes[k] != v {
				return nil, errQueueNameExists(k)
			}
		}
		return &createQueueResponse{QueueUrl: h.queueURL(ctx, existing.Name)}, nil
	}

	canonicalURL := h.canonicalQueueURL(in.QueueName)
	attrs := defaultQueueAttributes()
	for k, v := range in.Attributes {
		attrs[k] = v
	}

	if isFifo {
		attrs["FifoQueue"] = "true"
		if _, ok := attrs["ContentBasedDeduplication"]; !ok {
			attrs["ContentBasedDeduplication"] = "false"
		}
	}

	if aerr := validateCreateQueueAttributes(in.Attributes); aerr != nil {
		return nil, aerr
	}
	if aerr := validateQueueAttributes(attrs); aerr != nil {
		return nil, aerr
	}

	if aerr := h.validateRedrivePolicyContext(ctx, attrs); aerr != nil {
		return nil, aerr
	}

	q := &Queue{
		Name:                  in.QueueName,
		URL:                   canonicalURL,
		ARN:                   protocol.QueueARN(middleware.RegionFromContext(ctx, h.cfg.Region), h.cfg.AccountID, in.QueueName),
		Attributes:            attrs,
		CreatedTimestamp:      h.clk.Now().Unix(),
		LastModifiedTimestamp: h.clk.Now().Unix(),
		Tags:                  in.Tags,
	}

	if aerr := h.store.putQueue(ctx, q); aerr != nil {
		return nil, aerr
	}

	if h.bus != nil {
		h.bus.Publish(ctx, events.Event{
			Type:    events.SQSQueueCreated,
			Time:    h.clk.Now(),
			Source:  "sqs",
			Payload: events.ResourcePayload{Name: in.QueueName, ARN: q.ARN},
		})
	}
	return &createQueueResponse{QueueUrl: h.queueURL(ctx, in.QueueName)}, nil
}

func (h *Handler) getQueueURLTyped(ctx context.Context, in *getQueueURLRequest) (*getQueueURLResponse, *protocol.AWSError) {
	if in.QueueName == "" {
		return nil, protocol.ErrMissingParameter("QueueName")
	}
	q, aerr := h.store.getQueue(ctx, in.QueueName)
	if aerr != nil {
		return nil, aerr
	}
	return &getQueueURLResponse{QueueUrl: h.queueURL(ctx, q.Name)}, nil
}

func (h *Handler) getQueueAttributesTyped(ctx context.Context, in *getQueueAttributesRequest) (*getQueueAttributesResponse, *protocol.AWSError) {
	if aerr := validateQueueAttributeNames(in.AttributeNames); aerr != nil {
		return nil, aerr
	}

	queueName := queueNameFromURL(in.QueueUrl)
	q, aerr := h.store.getQueue(ctx, queueName)
	if aerr != nil {
		return nil, aerr
	}

	counts, aerr := h.store.countMessages(ctx, queueName, h.clk.Now())
	if aerr != nil {
		return nil, aerr
	}
	// The derived attributes: computed per request rather than stored, so they
	// go onto the copy this call answers with and are never persisted.
	q.Attributes["ApproximateNumberOfMessages"] = strconv.Itoa(counts.Visible)
	q.Attributes["ApproximateNumberOfMessagesNotVisible"] = strconv.Itoa(counts.NotVisible())
	q.Attributes["ApproximateNumberOfMessagesDelayed"] = strconv.Itoa(counts.Delayed)
	q.Attributes["QueueArn"] = q.ARN
	q.Attributes["CreatedTimestamp"] = strconv.FormatInt(q.CreatedTimestamp, 10)
	q.Attributes["LastModifiedTimestamp"] = strconv.FormatInt(q.LastModified(), 10)

	attrs := q.Attributes
	// "All" wins from anywhere in the list, as on AWS — it is one QueueAttributeName
	// enum value among the names, not a distinguished first element. (Message
	// attributes' ".*" selector has no meaning here: it is not in the enum, so
	// validateQueueAttributeNames has already rejected it.)
	if len(in.AttributeNames) > 0 && !slices.Contains(in.AttributeNames, "All") {
		filtered := make(map[string]string, len(in.AttributeNames))
		for _, name := range in.AttributeNames {
			if v, ok := attrs[name]; ok {
				filtered[name] = v
			}
		}
		attrs = filtered
	}

	return &getQueueAttributesResponse{Attributes: attrs}, nil
}

func (h *Handler) setQueueAttributesTyped(ctx context.Context, in *setQueueAttributesRequest) (*struct{}, *protocol.AWSError) {
	queueName := queueNameFromURL(in.QueueUrl)
	q, aerr := h.store.getQueue(ctx, queueName)
	if aerr != nil {
		return nil, aerr
	}

	attrs := make(map[string]string, len(q.Attributes)+len(in.Attributes))
	for k, v := range q.Attributes {
		attrs[k] = v
	}
	for k, v := range in.Attributes {
		attrs[k] = v
	}

	if aerr := validateSetQueueAttributes(in.Attributes); aerr != nil {
		return nil, aerr
	}
	if aerr := validateQueueAttributes(attrs); aerr != nil {
		return nil, aerr
	}

	if aerr := h.validateRedrivePolicyContext(ctx, attrs); aerr != nil {
		return nil, aerr
	}
	q.Attributes = attrs
	q.LastModifiedTimestamp = h.clk.Now().Unix()

	if aerr := h.store.putQueue(ctx, q); aerr != nil {
		return nil, aerr
	}

	return &struct{}{}, nil
}

func (h *Handler) deleteQueueTyped(ctx context.Context, in *deleteQueueRequest) (*struct{}, *protocol.AWSError) {
	queueName := queueNameFromURL(in.QueueUrl)
	if aerr := h.store.deleteQueue(ctx, queueName); aerr != nil {
		return nil, aerr
	}

	if h.bus != nil {
		arn := protocol.QueueARN(middleware.RegionFromContext(ctx, h.cfg.Region), h.cfg.AccountID, queueName)
		h.bus.Publish(ctx, events.Event{
			Type:    events.SQSQueueDeleted,
			Time:    h.clk.Now(),
			Source:  "sqs",
			Payload: events.ResourcePayload{Name: queueName, ARN: arn},
		})
	}
	return &struct{}{}, nil
}

// listQueuesTyped is the typed implementation of SQS:ListQueues.
// The legacy h.ListQueues remains below and is removed once the dispatcher path
// is the default (Phase 3).
func (h *Handler) listQueuesTyped(ctx context.Context, in *listQueuesRequest) (*listQueuesResponse, *protocol.AWSError) {
	queues, aerr := h.store.listQueues(ctx, in.QueueNamePrefix)
	if aerr != nil {
		return nil, aerr
	}
	urls := make([]string, len(queues))
	for i, q := range queues {
		urls[i] = h.queueURL(ctx, q.Name)
	}
	urls, token, aerr := paginateQueueURLs(urls, in.MaxResults, in.NextToken)
	if aerr != nil {
		return nil, aerr
	}
	return &listQueuesResponse{QueueUrls: urls, NextToken: token}, nil
}

// maxListResults is the documented ceiling for MaxResults, and the page size
// when MaxResults is omitted, on ListQueues and ListDeadLetterSourceQueues.
const maxListResults = 1000

// paginateQueueURLs pages a queue-URL list the way ListQueues and
// ListDeadLetterSourceQueues do. The input is sorted first so a token keeps
// meaning the same position from one page to the next.
//
// Per AWS docs (API_ListQueues.html, API_ListDeadLetterSourceQueues.html,
// checked 2026-10-17): MaxResults is 1 to 1000; without it the response holds
// up to 1,000 results and NextToken is null ("Token value is null if ... you
// did not set MaxResults"); with it, NextToken appears while results remain.
// A NextToken sent without MaxResults is not an error. The docs and the pinned
// model name no error for a malformed token or an out-of-range MaxResults, so
// both answer InvalidParameterValue, SQS's generic bad-argument code.
func paginateQueueURLs(urls []string, maxResults *int, nextToken string) ([]string, string, *protocol.AWSError) {
	size := 0
	if maxResults != nil {
		size = *maxResults
		if size < 1 || size > maxListResults {
			return nil, "", &protocol.AWSError{
				Code:       "InvalidParameterValue",
				Message:    fmt.Sprintf("Value %d for parameter MaxResults is invalid. Reason: MaxResults must be an integer between 1 and %d.", size, maxListResults),
				HTTPStatus: http.StatusBadRequest,
			}
		}
	}
	slices.Sort(urls)
	page, err := serviceutil.Paginate(urls, size, nextToken, serviceutil.PaginateOptions{DefaultLimit: maxListResults, MaxLimit: maxListResults})
	if err != nil {
		return nil, "", &protocol.AWSError{
			Code:       "InvalidParameterValue",
			Message:    "Value for parameter NextToken is invalid. Reason: the token is not valid.",
			HTTPStatus: http.StatusBadRequest,
		}
	}
	if maxResults == nil {
		return page.Items, "", nil
	}
	return page.Items, page.NextToken, nil
}

func (h *Handler) purgeQueueTyped(ctx context.Context, in *purgeQueueRequest) (*struct{}, *protocol.AWSError) {
	if in.QueueUrl == "" {
		return nil, protocol.ErrMissingParameter("QueueUrl")
	}
	queueName := queueNameFromURL(in.QueueUrl)
	if aerr := h.purgeQueue(ctx, queueName); aerr != nil {
		return nil, aerr
	}
	return &struct{}{}, nil
}

func (h *Handler) purgeQueue(ctx context.Context, queueName string) *protocol.AWSError {
	if _, aerr := h.store.getQueue(ctx, queueName); aerr != nil {
		return aerr
	}

	// Verified against AWS docs (2026-05-16): PurgeQueue enters a 60-second
	// in-progress window. AWS accepts the purge quickly, and messages sent during
	// the window may be deleted while purging.
	if aerr := h.store.startPurge(ctx, queueName); aerr != nil {
		return aerr
	}
	if aerr := h.store.deleteMessagesByQueuePrefix(ctx, queueName); aerr != nil {
		return aerr
	}

	if h.bus != nil {
		arn := protocol.QueueARN(middleware.RegionFromContext(ctx, h.cfg.Region), h.cfg.AccountID, queueName)
		h.bus.Publish(ctx, events.Event{
			Type:    events.SQSQueuePurged,
			Source:  "sqs",
			Payload: events.ResourcePayload{Name: queueName, ARN: arn},
		})
	}

	return nil
}

// ---- Handlers --------------------------------------------------------------

func (h *Handler) CreateQueue(w http.ResponseWriter, r *http.Request) {
	var req createQueueRequest
	if !serviceutil.DecodeJSON(w, r, &req) {
		return
	}
	if !serviceutil.RequireString(w, r, req.QueueName, "QueueName") {
		return
	}

	isFifo := strings.HasSuffix(req.QueueName, ".fifo") || req.Attributes["FifoQueue"] == "true"

	// Validate FIFO naming rules: FifoQueue=true requires .fifo suffix and vice versa.
	if req.Attributes["FifoQueue"] == "true" && !strings.HasSuffix(req.QueueName, ".fifo") {
		writeJSONError(w, r, &protocol.AWSError{
			Code:       "InvalidParameterValue",
			Message:    "The queue name must end in .fifo for FIFO queues.",
			HTTPStatus: http.StatusBadRequest,
		})
		return
	}

	// Check for existing queue.
	existing, _ := h.store.getQueue(r.Context(), req.QueueName)
	if existing != nil {
		for k, v := range req.Attributes {
			if existing.Attributes[k] != v {
				writeJSONError(w, r, errQueueNameExists(k))
				return
			}
		}
		protocol.WriteJSON(w, r, http.StatusOK, &createQueueResponse{QueueUrl: h.queueURL(r.Context(), existing.Name)})
		return
	}

	canonicalURL := h.canonicalQueueURL(req.QueueName)
	attrs := defaultQueueAttributes()
	for k, v := range req.Attributes {
		attrs[k] = v
	}

	// If .fifo suffix, ensure FifoQueue attribute is set.
	if isFifo {
		attrs["FifoQueue"] = "true"
		// Default ContentBasedDeduplication to false if not set.
		if _, ok := attrs["ContentBasedDeduplication"]; !ok {
			attrs["ContentBasedDeduplication"] = "false"
		}
	}

	if aerr := validateCreateQueueAttributes(req.Attributes); aerr != nil {
		writeJSONError(w, r, aerr)
		return
	}
	if aerr := validateQueueAttributes(attrs); aerr != nil {
		writeJSONError(w, r, aerr)
		return
	}

	// Validate RedrivePolicy if provided.
	if aerr := h.validateRedrivePolicy(r, attrs); aerr != nil {
		writeJSONError(w, r, aerr)
		return
	}

	q := &Queue{
		Name:                  req.QueueName,
		URL:                   canonicalURL,
		ARN:                   protocol.QueueARN(middleware.RegionFromContext(r.Context(), h.cfg.Region), h.cfg.AccountID, req.QueueName),
		Attributes:            attrs,
		CreatedTimestamp:      h.clk.Now().Unix(),
		LastModifiedTimestamp: h.clk.Now().Unix(),
		Tags:                  req.Tags,
	}

	if aerr := h.store.putQueue(r.Context(), q); aerr != nil {
		writeJSONError(w, r, aerr)
		return
	}

	if h.bus != nil {
		h.bus.Publish(r.Context(), events.Event{
			Type:    events.SQSQueueCreated,
			Time:    h.clk.Now(),
			Source:  "sqs",
			Payload: events.ResourcePayload{Name: req.QueueName, ARN: q.ARN},
		})
	}
	protocol.WriteJSON(w, r, http.StatusOK, &createQueueResponse{QueueUrl: h.queueURL(r.Context(), req.QueueName)})
}

func (h *Handler) GetQueueURL(w http.ResponseWriter, r *http.Request) {
	var req getQueueURLRequest
	if !serviceutil.DecodeJSON(w, r, &req) {
		return
	}
	if !serviceutil.RequireString(w, r, req.QueueName, "QueueName") {
		return
	}

	q, aerr := h.store.getQueue(r.Context(), req.QueueName)
	if aerr != nil {
		writeJSONError(w, r, aerr)
		return
	}

	protocol.WriteJSON(w, r, http.StatusOK, &getQueueURLResponse{QueueUrl: h.queueURL(r.Context(), q.Name)})
}

// GetQueueAttributes and SetQueueAttributes are the legacy (Query-protocol)
// dispatch path for the two attribute operations. Each decodes the request and
// shares its typed implementation, so the attribute-name validation, the
// derived Approximate*/timestamp attributes and the LastModifiedTimestamp bump
// are defined once and the two dispatch paths cannot drift.
func (h *Handler) GetQueueAttributes(w http.ResponseWriter, r *http.Request) {
	var req getQueueAttributesRequest
	if !serviceutil.DecodeJSON(w, r, &req) {
		return
	}
	resp, aerr := h.getQueueAttributesTyped(r.Context(), &req)
	if aerr != nil {
		writeJSONError(w, r, aerr)
		return
	}
	protocol.WriteJSON(w, r, http.StatusOK, resp)
}

func (h *Handler) SetQueueAttributes(w http.ResponseWriter, r *http.Request) {
	var req setQueueAttributesRequest
	if !serviceutil.DecodeJSON(w, r, &req) {
		return
	}
	resp, aerr := h.setQueueAttributesTyped(r.Context(), &req)
	if aerr != nil {
		writeJSONError(w, r, aerr)
		return
	}
	protocol.WriteJSON(w, r, http.StatusOK, resp)
}

func (h *Handler) DeleteQueue(w http.ResponseWriter, r *http.Request) {
	var req deleteQueueRequest
	if !serviceutil.DecodeJSON(w, r, &req) {
		return
	}

	queueName := queueNameFromURL(req.QueueUrl)
	if aerr := h.store.deleteQueue(r.Context(), queueName); aerr != nil {
		writeJSONError(w, r, aerr)
		return
	}

	if h.bus != nil {
		arn := protocol.QueueARN(middleware.RegionFromContext(r.Context(), h.cfg.Region), h.cfg.AccountID, queueName)
		h.bus.Publish(r.Context(), events.Event{
			Type:    events.SQSQueueDeleted,
			Time:    h.clk.Now(),
			Source:  "sqs",
			Payload: events.ResourcePayload{Name: queueName, ARN: arn},
		})
	}
	protocol.WriteJSON(w, r, http.StatusOK, struct{}{})
}

func (h *Handler) ListQueues(w http.ResponseWriter, r *http.Request) {
	var req listQueuesRequest
	if !serviceutil.DecodeJSON(w, r, &req) {
		return
	}

	queues, aerr := h.store.listQueues(r.Context(), req.QueueNamePrefix)
	if aerr != nil {
		writeJSONError(w, r, aerr)
		return
	}

	urls := make([]string, len(queues))
	for i, q := range queues {
		urls[i] = h.queueURL(r.Context(), q.Name)
	}
	urls, token, aerr := paginateQueueURLs(urls, req.MaxResults, req.NextToken)
	if aerr != nil {
		writeJSONError(w, r, aerr)
		return
	}

	protocol.WriteJSON(w, r, http.StatusOK, &listQueuesResponse{QueueUrls: urls, NextToken: token})
}

func (h *Handler) PurgeQueue(w http.ResponseWriter, r *http.Request) {
	var req purgeQueueRequest
	if !serviceutil.DecodeJSON(w, r, &req) {
		return
	}
	if !serviceutil.RequireString(w, r, req.QueueUrl, "QueueUrl") {
		return
	}

	queueName := queueNameFromURL(req.QueueUrl)
	if aerr := h.purgeQueue(r.Context(), queueName); aerr != nil {
		writeJSONError(w, r, aerr)
		return
	}

	protocol.WriteJSON(w, r, http.StatusOK, struct{}{})
}

// ---- Queue tags ------------------------------------------------------------

type tagQueueRequest struct {
	QueueUrl string            `json:"QueueUrl"`
	Tags     map[string]string `json:"Tags"`
}

type untagQueueRequest struct {
	QueueUrl string   `json:"QueueUrl"`
	TagKeys  []string `json:"TagKeys"`
}

type listQueueTagsRequest struct {
	QueueUrl string `json:"QueueUrl"`
}

type listQueueTagsResponse struct {
	Tags map[string]string `json:"Tags"`
}

func (h *Handler) tagQueueTyped(ctx context.Context, in *tagQueueRequest) (*struct{}, *protocol.AWSError) {
	queueName := queueNameFromURL(in.QueueUrl)
	q, aerr := h.store.getQueue(ctx, queueName)
	if aerr != nil {
		return nil, aerr
	}

	tags := q.GetTags()
	if tags == nil {
		tags = make(map[string]string)
	}
	for k, v := range in.Tags {
		tags[k] = v
	}
	q.SetTags(tags)

	if aerr := h.store.putQueue(ctx, q); aerr != nil {
		return nil, aerr
	}

	return &struct{}{}, nil
}

func (h *Handler) untagQueueTyped(ctx context.Context, in *untagQueueRequest) (*struct{}, *protocol.AWSError) {
	queueName := queueNameFromURL(in.QueueUrl)
	q, aerr := h.store.getQueue(ctx, queueName)
	if aerr != nil {
		return nil, aerr
	}

	tags := q.GetTags()
	for _, k := range in.TagKeys {
		delete(tags, k)
	}
	q.SetTags(tags)

	if aerr := h.store.putQueue(ctx, q); aerr != nil {
		return nil, aerr
	}

	return &struct{}{}, nil
}

func (h *Handler) listQueueTagsTyped(ctx context.Context, in *listQueueTagsRequest) (*listQueueTagsResponse, *protocol.AWSError) {
	queueName := queueNameFromURL(in.QueueUrl)
	q, aerr := h.store.getQueue(ctx, queueName)
	if aerr != nil {
		return nil, aerr
	}

	tags := q.GetTags()
	if tags == nil {
		tags = map[string]string{}
	}

	return &listQueueTagsResponse{Tags: tags}, nil
}

// TagQueue handles the SQS TagQueue operation.
// AWS docs: https://docs.aws.amazon.com/AWSSimpleQueueService/latest/APIReference/API_TagQueue.html
func (h *Handler) TagQueue(w http.ResponseWriter, r *http.Request) {
	var req tagQueueRequest
	if !serviceutil.DecodeJSON(w, r, &req) {
		return
	}

	queueName := queueNameFromURL(req.QueueUrl)
	q, aerr := h.store.getQueue(r.Context(), queueName)
	if aerr != nil {
		writeJSONError(w, r, aerr)
		return
	}

	tags := q.GetTags()
	if tags == nil {
		tags = make(map[string]string)
	}
	for k, v := range req.Tags {
		tags[k] = v
	}
	q.SetTags(tags)

	if aerr := h.store.putQueue(r.Context(), q); aerr != nil {
		writeJSONError(w, r, aerr)
		return
	}

	protocol.WriteJSON(w, r, http.StatusOK, struct{}{})
}

// UntagQueue handles the SQS UntagQueue operation.
// AWS docs: https://docs.aws.amazon.com/AWSSimpleQueueService/latest/APIReference/API_UntagQueue.html
func (h *Handler) UntagQueue(w http.ResponseWriter, r *http.Request) {
	var req untagQueueRequest
	if !serviceutil.DecodeJSON(w, r, &req) {
		return
	}

	queueName := queueNameFromURL(req.QueueUrl)
	q, aerr := h.store.getQueue(r.Context(), queueName)
	if aerr != nil {
		writeJSONError(w, r, aerr)
		return
	}

	tags := q.GetTags()
	for _, k := range req.TagKeys {
		delete(tags, k)
	}
	q.SetTags(tags)

	if aerr := h.store.putQueue(r.Context(), q); aerr != nil {
		writeJSONError(w, r, aerr)
		return
	}

	protocol.WriteJSON(w, r, http.StatusOK, struct{}{})
}

// ListQueueTags handles the SQS ListQueueTags operation.
// AWS docs: https://docs.aws.amazon.com/AWSSimpleQueueService/latest/APIReference/API_ListQueueTags.html
func (h *Handler) ListQueueTags(w http.ResponseWriter, r *http.Request) {
	var req listQueueTagsRequest
	if !serviceutil.DecodeJSON(w, r, &req) {
		return
	}

	queueName := queueNameFromURL(req.QueueUrl)
	q, aerr := h.store.getQueue(r.Context(), queueName)
	if aerr != nil {
		writeJSONError(w, r, aerr)
		return
	}

	tags := q.GetTags()
	if tags == nil {
		tags = map[string]string{}
	}

	protocol.WriteJSON(w, r, http.StatusOK, &listQueueTagsResponse{Tags: tags})
}

// ---- Helpers ---------------------------------------------------------------

// defaultQueueAttributes returns the AWS default queue attribute values.
func defaultQueueAttributes() map[string]string {
	return map[string]string{
		"VisibilityTimeout":             "30",
		"MaximumMessageSize":            "262144",
		"MessageRetentionPeriod":        "345600",
		"DelaySeconds":                  "0",
		"ReceiveMessageWaitTimeSeconds": "0",
	}
}

func validateQueueAttributes(attrs map[string]string) *protocol.AWSError {
	if err := validateQueueIntegerAttribute(attrs, "DelaySeconds", 0, 900); err != nil {
		return err
	}
	if err := validateQueueIntegerAttribute(attrs, "MaximumMessageSize", 1024, 1048576); err != nil {
		return err
	}
	if err := validateQueueIntegerAttribute(attrs, "MessageRetentionPeriod", 60, 1209600); err != nil {
		return err
	}
	if err := validateQueueIntegerAttribute(attrs, "KmsDataKeyReusePeriodSeconds", 60, 86400); err != nil {
		return err
	}
	if err := validateQueueIntegerAttribute(attrs, "ReceiveMessageWaitTimeSeconds", 0, 20); err != nil {
		return err
	}
	if err := validateQueueIntegerAttribute(attrs, "VisibilityTimeout", 0, 43200); err != nil {
		return err
	}
	if err := validateQueueBoolAttribute(attrs, "FifoQueue"); err != nil {
		return err
	}
	if err := validateQueueBoolAttribute(attrs, "ContentBasedDeduplication"); err != nil {
		return err
	}
	if err := validateQueueBoolAttribute(attrs, "SqsManagedSseEnabled"); err != nil {
		return err
	}
	if err := validateQueueEnumAttribute(attrs, "DeduplicationScope", "messageGroup", "queue"); err != nil {
		return err
	}
	if err := validateQueueEnumAttribute(attrs, "FifoThroughputLimit", "perQueue", "perMessageGroupId"); err != nil {
		return err
	}
	if attrs["FifoThroughputLimit"] == "perMessageGroupId" && attrs["DeduplicationScope"] != "messageGroup" {
		return errInvalidQueueAttributeValue("FifoThroughputLimit")
	}
	if attrs["SqsManagedSseEnabled"] == "true" && attrs["KmsMasterKeyId"] != "" {
		return errInvalidQueueAttributeValue("SqsManagedSseEnabled")
	}
	// Reject FIFO-only attributes on standard queues.
	if attrs["FifoQueue"] != "true" {
		if _, ok := attrs["ContentBasedDeduplication"]; ok {
			return errInvalidQueueAttributeValue("ContentBasedDeduplication")
		}
		if _, ok := attrs["DeduplicationScope"]; ok {
			return errInvalidQueueAttributeValue("DeduplicationScope")
		}
		if _, ok := attrs["FifoThroughputLimit"]; ok {
			return errInvalidQueueAttributeValue("FifoThroughputLimit")
		}
	}
	if err := validateQueuePolicyAttribute(attrs, "Policy"); err != nil {
		return err
	}
	return validateRedriveAllowPolicyAttribute(attrs)
}

func validateCreateQueueAttributes(attrs map[string]string) *protocol.AWSError {
	return validateRequestedQueueAttributes(attrs, queueAttributeCreate)
}

func validateSetQueueAttributes(attrs map[string]string) *protocol.AWSError {
	return validateRequestedQueueAttributes(attrs, queueAttributeSet)
}

type queueAttributeValidationContext int

const (
	queueAttributeCreate queueAttributeValidationContext = iota
	queueAttributeSet
)

func validateRequestedQueueAttributes(attrs map[string]string, validationContext queueAttributeValidationContext) *protocol.AWSError {
	for name := range attrs {
		if _, ok := settableQueueAttributes[name]; !ok {
			return &protocol.AWSError{Code: "InvalidAttributeName", Message: "Unknown Attribute " + name + ".", HTTPStatus: http.StatusBadRequest}
		}
		if validationContext == queueAttributeSet && name == "FifoQueue" {
			return &protocol.AWSError{Code: "InvalidAttributeName", Message: "Unknown Attribute " + name + ".", HTTPStatus: http.StatusBadRequest}
		}
	}
	return nil
}

var settableQueueAttributes = map[string]struct{}{
	"ContentBasedDeduplication":     {},
	"DeduplicationScope":            {},
	"DelaySeconds":                  {},
	"FifoQueue":                     {},
	"FifoThroughputLimit":           {},
	"KmsDataKeyReusePeriodSeconds":  {},
	"KmsMasterKeyId":                {},
	"MaximumMessageSize":            {},
	"MessageRetentionPeriod":        {},
	"Policy":                        {},
	"ReceiveMessageWaitTimeSeconds": {},
	"RedriveAllowPolicy":            {},
	"RedrivePolicy":                 {},
	"SqsManagedSseEnabled":          {},
	"VisibilityTimeout":             {},
}

func validateQueueIntegerAttribute(attrs map[string]string, name string, minValue, maxValue int) *protocol.AWSError {
	value, ok := attrs[name]
	if !ok {
		return nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < minValue || parsed > maxValue {
		return errInvalidQueueAttributeValue(name)
	}
	return nil
}

func validateQueueBoolAttribute(attrs map[string]string, name string) *protocol.AWSError {
	value, ok := attrs[name]
	if !ok {
		return nil
	}
	if value != "true" && value != "false" {
		return errInvalidQueueAttributeValue(name)
	}
	return nil
}

func validateQueueEnumAttribute(attrs map[string]string, name string, allowed ...string) *protocol.AWSError {
	value, ok := attrs[name]
	if !ok {
		return nil
	}
	for _, allowedValue := range allowed {
		if value == allowedValue {
			return nil
		}
	}
	return errInvalidQueueAttributeValue(name)
}

func validateQueuePolicyAttribute(attrs map[string]string, name string) *protocol.AWSError {
	value, ok := attrs[name]
	if !ok || strings.TrimSpace(value) == "" {
		return nil
	}
	var policy map[string]any
	if err := json.Unmarshal([]byte(value), &policy); err != nil {
		return errInvalidQueueAttributeValue(name)
	}
	return nil
}

func validateRedriveAllowPolicyAttribute(attrs map[string]string) *protocol.AWSError {
	value, ok := attrs["RedriveAllowPolicy"]
	if !ok || strings.TrimSpace(value) == "" {
		return nil
	}
	var policy struct {
		RedrivePermission string   `json:"redrivePermission"`
		SourceQueueARNs   []string `json:"sourceQueueArns"`
	}
	if err := json.Unmarshal([]byte(value), &policy); err != nil {
		return errInvalidQueueAttributeValue("RedriveAllowPolicy")
	}
	switch policy.RedrivePermission {
	case "", "allowAll", "denyAll":
		if len(policy.SourceQueueARNs) > 0 {
			return errInvalidQueueAttributeValue("RedriveAllowPolicy")
		}
	case "byQueue":
		if len(policy.SourceQueueARNs) > 10 {
			return errInvalidQueueAttributeValue("RedriveAllowPolicy")
		}
	default:
		return errInvalidQueueAttributeValue("RedriveAllowPolicy")
	}
	return nil
}

func errInvalidQueueAttributeValue(name string) *protocol.AWSError {
	return &protocol.AWSError{
		Code:       "InvalidAttributeValue",
		Message:    "Invalid value for the parameter " + name + ".",
		HTTPStatus: http.StatusBadRequest,
	}
}

func errQueueNameExists(attribute string) *protocol.AWSError {
	return &protocol.AWSError{
		Code:       "QueueNameExists",
		Message:    "A queue already exists with the same name and a different value for attribute " + attribute + ".",
		HTTPStatus: http.StatusBadRequest,
	}
}
