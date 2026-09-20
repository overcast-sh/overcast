package kinesis

// handler.go — HTTP handlers for the Kinesis emulator.
// Implements stream lifecycle, record put/get, shard management, and tagging.

import (
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"

	"github.com/overcast-sh/overcast/internal/clock"
	"github.com/overcast-sh/overcast/internal/config"
	"github.com/overcast-sh/overcast/internal/events"
	"github.com/overcast-sh/overcast/internal/middleware"
	"github.com/overcast-sh/overcast/internal/protocol"
	"github.com/overcast-sh/overcast/internal/protocol/op"
	"github.com/overcast-sh/overcast/internal/serviceutil"
)

// Handler holds all dependencies for Kinesis HTTP handlers.
type Handler struct {
	ops     map[string]http.HandlerFunc
	typedOp map[string]op.Operation
	cfg     *config.Config
	store   *kinesisStore
	log     *serviceutil.ServiceLogger
	clk     clock.Clock
	bus     *events.Bus
}

func newHandler(cfg *config.Config, store *kinesisStore, log *serviceutil.ServiceLogger, clk clock.Clock) *Handler {
	h := &Handler{cfg: cfg, store: store, log: log, clk: clk}
	h.initOps()
	return h
}

// initOps registers every known Kinesis operation to its handler.
// Implemented operations point to their handler method.
// Adding a new operation: add an entry here and implement it.
func (h *Handler) initOps() {
	h.ops = map[string]http.HandlerFunc{
		"CreateStream":                  h.CreateStream,
		"DeleteStream":                  h.DeleteStream,
		"DescribeStream":                h.DescribeStream,
		"DescribeStreamSummary":         h.DescribeStreamSummary,
		"ListStreams":                   h.ListStreams,
		"PutRecord":                     h.PutRecord,
		"PutRecords":                    h.PutRecords,
		"GetShardIterator":              h.GetShardIterator,
		"GetRecords":                    h.GetRecords,
		"ListShards":                    h.ListShards,
		"SplitShard":                    h.SplitShard,
		"AddTagsToStream":               h.AddTagsToStream,
		"ListTagsForStream":             h.ListTagsForStream,
		"RemoveTagsFromStream":          h.RemoveTagsFromStream,
		"TagResource":                   h.TagResource,
		"UntagResource":                 h.UntagResource,
		"ListTagsForResource":           h.ListTagsForResource,
		"MergeShards":                   h.MergeShards,
		"IncreaseStreamRetentionPeriod": h.IncreaseStreamRetentionPeriod,
		"DecreaseStreamRetentionPeriod": h.DecreaseStreamRetentionPeriod,
		"UpdateStreamMode":              h.UpdateStreamMode,
		"StartStreamEncryption":         h.StartStreamEncryption,
		"StopStreamEncryption":          h.StopStreamEncryption,
	}
	h.typedOp = h.typedOps()
}

// decodeJSON decodes the request body into v. Returns false and writes an error if it fails.
func decodeJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		protocol.WriteJSONError(w, r, &protocol.AWSError{
			Code:       "SerializationException",
			Message:    "Failed to deserialize the message: " + err.Error(),
			HTTPStatus: http.StatusBadRequest,
		})
		return false
	}
	return true
}

// ---- Stream lifecycle --------------------------------------------------------

// publish emits an event if the bus is wired.
func (h *Handler) publish(r *http.Request, t events.Type, payload any) {
	if h.bus != nil {
		h.bus.Publish(r.Context(), events.Event{Type: t, Payload: payload})
	}
}

// CreateStream handles Kinesis_20131202.CreateStream.
// AWS docs: https://docs.aws.amazon.com/kinesis/latest/APIReference/API_CreateStream.html
//
// Delegates to createStreamTyped (typed_logic.go) — see PutRecord's doc
// comment about the JSON1.1/legacy and CBOR/typed-dispatch paths sharing one
// implementation. This also fixes a divergence between the two: the typed
// path built the stream ARN from h.cfg.Region regardless of the request's
// resolved region, while this handler used middleware.RegionFromContext;
// createStreamTyped now does the same, so both paths agree.
func (h *Handler) CreateStream(w http.ResponseWriter, r *http.Request) {
	var req createStreamRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if _, aerr := h.createStreamTyped(r.Context(), &req); aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// DeleteStream handles Kinesis_20131202.DeleteStream.
// AWS docs: https://docs.aws.amazon.com/kinesis/latest/APIReference/API_DeleteStream.html
func (h *Handler) DeleteStream(w http.ResponseWriter, r *http.Request) {
	var req struct {
		StreamName string `json:"StreamName"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.StreamName == "" {
		protocol.WriteJSONError(w, r, errMissingParameter("StreamName"))
		return
	}
	if _, aerr := h.store.getStream(r.Context(), req.StreamName); aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}
	if aerr := h.store.deleteStream(r.Context(), req.StreamName); aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}
	arn := streamARN(h.cfg.AccountID, middleware.RegionFromContext(r.Context(), h.cfg.Region), req.StreamName)
	h.publish(r, events.KinesisStreamDeleted, events.ResourcePayload{Name: req.StreamName, ARN: arn})
	w.WriteHeader(http.StatusOK)
}

// DescribeStream handles Kinesis_20131202.DescribeStream.
// AWS docs: https://docs.aws.amazon.com/kinesis/latest/APIReference/API_DescribeStream.html
//
// Delegates to describeStreamTyped (typed_logic.go) — see PutRecord's doc
// comment. Its Shards list is paginated (Limit/ExclusiveStartShardId, with
// HasMoreShards in the description), so the two wire paths must not keep
// separate copies of that arithmetic.
func (h *Handler) DescribeStream(w http.ResponseWriter, r *http.Request) {
	var req describeStreamRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	out, aerr := h.describeStreamTyped(r.Context(), &req)
	if aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}
	protocol.WriteAWSJSON(w, r, http.StatusOK, out, "application/x-amz-json-1.1")
}

// DescribeStreamSummary handles Kinesis_20131202.DescribeStreamSummary.
// AWS docs: https://docs.aws.amazon.com/kinesis/latest/APIReference/API_DescribeStreamSummary.html
func (h *Handler) DescribeStreamSummary(w http.ResponseWriter, r *http.Request) {
	var req struct {
		StreamName string `json:"StreamName"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.StreamName == "" {
		protocol.WriteJSONError(w, r, errMissingParameter("StreamName"))
		return
	}
	st, aerr := h.store.getStream(r.Context(), req.StreamName)
	if aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}
	protocol.WriteAWSJSON(w, r, http.StatusOK, map[string]any{
		"StreamDescriptionSummary": toStreamDescriptionSummary(st),
	}, "application/x-amz-json-1.1")
}

// ListStreams handles Kinesis_20131202.ListStreams.
// AWS docs: https://docs.aws.amazon.com/kinesis/latest/APIReference/API_ListStreams.html
//
// Delegates to listStreamsTyped (typed_logic.go) — see PutRecord's doc
// comment. ListStreams takes no required member, so an entirely empty body
// is a legitimate request and a decode failure is not reported here.
func (h *Handler) ListStreams(w http.ResponseWriter, r *http.Request) {
	var req listStreamsRequest
	// Body may be empty — ignore decode errors here.
	_ = json.NewDecoder(r.Body).Decode(&req)

	out, aerr := h.listStreamsTyped(r.Context(), &req)
	if aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}
	protocol.WriteAWSJSON(w, r, http.StatusOK, out, "application/x-amz-json-1.1")
}

// ---- Records -----------------------------------------------------------------

// PutRecord handles Kinesis_20131202.PutRecord.
// AWS docs: https://docs.aws.amazon.com/kinesis/latest/APIReference/API_PutRecord.html
//
// Delegates to putRecordTyped (typed_logic.go) — the JSON1.1/legacy wire
// path (this file) and the CBOR/typed-dispatch path (typed_ops.go) share
// one implementation so the A1 sequence-counter fix (storage-access-plan.md)
// applies to both instead of being duplicated and drifting.
func (h *Handler) PutRecord(w http.ResponseWriter, r *http.Request) {
	var req putRecordRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	resp, aerr := h.putRecordTyped(r.Context(), &req)
	if aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}
	protocol.WriteAWSJSON(w, r, http.StatusOK, resp, "application/x-amz-json-1.1")
}

// PutRecords handles Kinesis_20131202.PutRecords.
// AWS docs: https://docs.aws.amazon.com/kinesis/latest/APIReference/API_PutRecords.html
//
// Delegates to putRecordsTyped (typed_logic.go) — see PutRecord's doc
// comment.
func (h *Handler) PutRecords(w http.ResponseWriter, r *http.Request) {
	var req putRecordsRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	resp, aerr := h.putRecordsTyped(r.Context(), &req)
	if aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}
	protocol.WriteAWSJSON(w, r, http.StatusOK, resp, "application/x-amz-json-1.1")
}

// GetShardIterator handles Kinesis_20131202.GetShardIterator.
// AWS docs: https://docs.aws.amazon.com/kinesis/latest/APIReference/API_GetShardIterator.html
func (h *Handler) GetShardIterator(w http.ResponseWriter, r *http.Request) {
	var req struct {
		StreamName             string `json:"StreamName"`
		ShardId                string `json:"ShardId"`
		ShardIteratorType      string `json:"ShardIteratorType"`
		StartingSequenceNumber string `json:"StartingSequenceNumber"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.StreamName == "" {
		protocol.WriteJSONError(w, r, errMissingParameter("StreamName"))
		return
	}
	if _, aerr := h.store.getStream(r.Context(), req.StreamName); aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}

	// afterSeqNo: the iterator will return records AFTER this sequence number.
	// "" means start from the very beginning (TRIM_HORIZON).
	var afterSeqNo string
	switch req.ShardIteratorType {
	case "AT_SEQUENCE_NUMBER":
		// return records starting AT this sequence number — so afterSeqNo is one before
		afterSeqNo = "before:" + req.StartingSequenceNumber
	case "AFTER_SEQUENCE_NUMBER":
		afterSeqNo = req.StartingSequenceNumber
	case "LATEST":
		// Encode a magic value so GetRecords returns nothing but a valid iterator.
		afterSeqNo = "LATEST"
	default: // TRIM_HORIZON and anything else: start from beginning
		afterSeqNo = ""
	}

	iter := encodeShardIterator(req.StreamName, req.ShardId, afterSeqNo)
	encoded := base64.StdEncoding.EncodeToString([]byte(iter))
	protocol.WriteAWSJSON(w, r, http.StatusOK, map[string]any{
		"ShardIterator": encoded,
	}, "application/x-amz-json-1.1")
}

// GetRecords handles Kinesis_20131202.GetRecords.
// AWS docs: https://docs.aws.amazon.com/kinesis/latest/APIReference/API_GetRecords.html
//
// Delegates to getRecordsTyped (typed_logic.go) — see PutRecord's doc
// comment; this also carries the A2 ScanPage range-read fix
// (storage-access-plan.md).
func (h *Handler) GetRecords(w http.ResponseWriter, r *http.Request) {
	var req getRecordsRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	resp, aerr := h.getRecordsTyped(r.Context(), &req)
	if aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}
	protocol.WriteAWSJSON(w, r, http.StatusOK, resp, "application/x-amz-json-1.1")
}

// ---- Shards ------------------------------------------------------------------

// ListShards handles Kinesis_20131202.ListShards.
// AWS docs: https://docs.aws.amazon.com/kinesis/latest/APIReference/API_ListShards.html
//
// Delegates to listShardsTyped (typed_logic.go) — see PutRecord's doc
// comment. NextToken minting, its exclusions and its expiry all live there.
func (h *Handler) ListShards(w http.ResponseWriter, r *http.Request) {
	var req listShardsRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	out, aerr := h.listShardsTyped(r.Context(), &req)
	if aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}
	protocol.WriteAWSJSON(w, r, http.StatusOK, out, "application/x-amz-json-1.1")
}

// SplitShard handles Kinesis_20131202.SplitShard.
// AWS docs: https://docs.aws.amazon.com/kinesis/latest/APIReference/API_SplitShard.html
//
// Delegates to splitShardTyped (typed_logic.go) — see PutRecord's doc
// comment. Success has an empty body on the JSON1.1 wire (w.WriteHeader
// only, no writeJSON call) to match this handler's pre-existing behavior
// for void operations; splitShardTyped's *struct{} return is only used to
// check for an error.
func (h *Handler) SplitShard(w http.ResponseWriter, r *http.Request) {
	var req splitShardRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if _, aerr := h.splitShardTyped(r.Context(), &req); aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// MergeShards handles Kinesis_20131202.MergeShards (basic implementation).
//
// Delegates to mergeShardsTyped (typed_logic.go) — see SplitShard's doc
// comment about the empty-body convention.
func (h *Handler) MergeShards(w http.ResponseWriter, r *http.Request) {
	var req mergeShardsRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if _, aerr := h.mergeShardsTyped(r.Context(), &req); aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// ---- Tags --------------------------------------------------------------------

// AddTagsToStream handles Kinesis_20131202.AddTagsToStream.
// AWS docs: https://docs.aws.amazon.com/kinesis/latest/APIReference/API_AddTagsToStream.html
//
// Delegates to addTagsToStreamTyped (typed_logic.go) rather than repeating
// it — this used to be a standalone duplicate that skipped the tag
// validation added there for #1052, exactly the drift PutRecord's doc
// comment above warns about.
func (h *Handler) AddTagsToStream(w http.ResponseWriter, r *http.Request) {
	var req addTagsToStreamRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if _, aerr := h.addTagsToStreamTyped(r.Context(), &req); aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// ListTagsForStream handles Kinesis_20131202.ListTagsForStream.
// AWS docs: https://docs.aws.amazon.com/kinesis/latest/APIReference/API_ListTagsForStream.html
//
// Delegates to listTagsForStreamTyped (typed_logic.go) — see PutRecord's doc
// comment; the tag list pages on ExclusiveStartTagKey/Limit.
func (h *Handler) ListTagsForStream(w http.ResponseWriter, r *http.Request) {
	var req listTagsForStreamRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	out, aerr := h.listTagsForStreamTyped(r.Context(), &req)
	if aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}
	protocol.WriteAWSJSON(w, r, http.StatusOK, out, "application/x-amz-json-1.1")
}

// RemoveTagsFromStream handles Kinesis_20131202.RemoveTagsFromStream.
func (h *Handler) RemoveTagsFromStream(w http.ResponseWriter, r *http.Request) {
	var req struct {
		StreamName string   `json:"StreamName"`
		TagKeys    []string `json:"TagKeys"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.StreamName == "" {
		protocol.WriteJSONError(w, r, errMissingParameter("StreamName"))
		return
	}
	st, aerr := h.store.getStream(r.Context(), req.StreamName)
	if aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}
	for _, k := range req.TagKeys {
		delete(st.Tags, k)
	}
	if aerr := h.store.putStream(r.Context(), st); aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// TagResource handles Kinesis_20131202.TagResource — the ARN-addressed
// equivalent of AddTagsToStream.
// AWS docs: https://docs.aws.amazon.com/kinesis/latest/APIReference/API_TagResource.html
//
// This and the two below delegate to the typed implementation rather than
// repeating it: the JSON 1.1 path only differs in how the request arrives and
// how a void success is written (an empty body, which Kinesis' existing
// operations also produce).
func (h *Handler) TagResource(w http.ResponseWriter, r *http.Request) {
	var req tagResourceRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if _, aerr := h.tagResourceTyped(r.Context(), &req); aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// UntagResource handles Kinesis_20131202.UntagResource.
// AWS docs: https://docs.aws.amazon.com/kinesis/latest/APIReference/API_UntagResource.html
func (h *Handler) UntagResource(w http.ResponseWriter, r *http.Request) {
	var req untagResourceRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if _, aerr := h.untagResourceTyped(r.Context(), &req); aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// ListTagsForResource handles Kinesis_20131202.ListTagsForResource.
// AWS docs: https://docs.aws.amazon.com/kinesis/latest/APIReference/API_ListTagsForResource.html
func (h *Handler) ListTagsForResource(w http.ResponseWriter, r *http.Request) {
	var req listTagsForResourceRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	out, aerr := h.listTagsForResourceTyped(r.Context(), &req)
	if aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}
	protocol.WriteAWSJSON(w, r, http.StatusOK, out, "application/x-amz-json-1.1")
}

// ---- Retention ---------------------------------------------------------------

// IncreaseStreamRetentionPeriod handles Kinesis_20131202.IncreaseStreamRetentionPeriod.
func (h *Handler) IncreaseStreamRetentionPeriod(w http.ResponseWriter, r *http.Request) {
	var req struct {
		StreamName           string `json:"StreamName"`
		RetentionPeriodHours int    `json:"RetentionPeriodHours"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.StreamName == "" {
		protocol.WriteJSONError(w, r, errMissingParameter("StreamName"))
		return
	}
	st, aerr := h.store.getStream(r.Context(), req.StreamName)
	if aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}
	st.RetentionPeriodHours = req.RetentionPeriodHours
	if aerr := h.store.putStream(r.Context(), st); aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// DecreaseStreamRetentionPeriod handles Kinesis_20131202.DecreaseStreamRetentionPeriod.
func (h *Handler) DecreaseStreamRetentionPeriod(w http.ResponseWriter, r *http.Request) {
	var req struct {
		StreamName           string `json:"StreamName"`
		RetentionPeriodHours int    `json:"RetentionPeriodHours"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.StreamName == "" {
		protocol.WriteJSONError(w, r, errMissingParameter("StreamName"))
		return
	}
	st, aerr := h.store.getStream(r.Context(), req.StreamName)
	if aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}
	st.RetentionPeriodHours = req.RetentionPeriodHours
	if aerr := h.store.putStream(r.Context(), st); aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// UpdateStreamMode handles Kinesis_20131202.UpdateStreamMode.
// AWS docs: https://docs.aws.amazon.com/kinesis/latest/APIReference/API_UpdateStreamMode.html
//
// Delegates to updateStreamModeTyped (typed_logic.go) — see TagResource's
// doc comment about the empty-body void-response convention.
func (h *Handler) UpdateStreamMode(w http.ResponseWriter, r *http.Request) {
	var req updateStreamModeRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if _, aerr := h.updateStreamModeTyped(r.Context(), &req); aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// StartStreamEncryption handles Kinesis_20131202.StartStreamEncryption.
// AWS docs: https://docs.aws.amazon.com/kinesis/latest/APIReference/API_StartStreamEncryption.html
//
// Delegates to startStreamEncryptionTyped (typed_logic.go) — see
// TagResource's doc comment.
func (h *Handler) StartStreamEncryption(w http.ResponseWriter, r *http.Request) {
	var req streamEncryptionRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if _, aerr := h.startStreamEncryptionTyped(r.Context(), &req); aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// StopStreamEncryption handles Kinesis_20131202.StopStreamEncryption.
// AWS docs: https://docs.aws.amazon.com/kinesis/latest/APIReference/API_StopStreamEncryption.html
//
// Delegates to stopStreamEncryptionTyped (typed_logic.go) — see TagResource's
// doc comment.
func (h *Handler) StopStreamEncryption(w http.ResponseWriter, r *http.Request) {
	var req streamEncryptionRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if _, aerr := h.stopStreamEncryptionTyped(r.Context(), &req); aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// ---- Response helpers --------------------------------------------------------

// toStreamDescription renders DescribeStream's response around one page of
// shards. The page and its HasMoreShards flag are computed by the caller
// (describeStreamTyped), which owns the Limit/ExclusiveStartShardId
// arithmetic.
func toStreamDescription(st *Stream, shards []map[string]any, hasMoreShards bool) map[string]any {
	return map[string]any{
		"StreamName":              st.StreamName,
		"StreamARN":               st.StreamARN,
		"StreamStatus":            st.StreamStatus,
		"Shards":                  shards,
		"HasMoreShards":           hasMoreShards,
		"RetentionPeriodHours":    st.RetentionPeriodHours,
		"StreamCreationTimestamp": st.CreatedAt.Unix(),
		"EnhancedMonitoring":      []any{},
		"StreamModeDetails":       streamModeDetailsToMap(st.effectiveStreamModeDetails()),
		"EncryptionType":          st.effectiveEncryptionType(),
		"KeyId":                   st.KeyId,
	}
}

// toStreamDescriptionSummary is DescribeStreamSummary's response shape,
// shared by the JSON1.1 handler (DescribeStreamSummary above) and the typed
// CBOR path (describeStreamSummaryTyped, typed_logic.go) so the two field
// lists cannot drift apart.
func toStreamDescriptionSummary(st *Stream) map[string]any {
	return map[string]any{
		"StreamName":              st.StreamName,
		"StreamARN":               st.StreamARN,
		"StreamStatus":            st.StreamStatus,
		"OpenShardCount":          activeShardCount(st),
		"RetentionPeriodHours":    st.RetentionPeriodHours,
		"StreamCreationTimestamp": st.CreatedAt.Unix(),
		"EnhancedMonitoring":      []any{},
		"StreamModeDetails":       streamModeDetailsToMap(st.effectiveStreamModeDetails()),
		"EncryptionType":          st.effectiveEncryptionType(),
		"KeyId":                   st.KeyId,
	}
}

func streamModeDetailsToMap(smd *StreamModeDetails) map[string]any {
	return map[string]any{"StreamMode": smd.StreamMode}
}

func shardToMap(shard Shard) map[string]any {
	m := map[string]any{
		"ShardId": shard.ShardId,
		"HashKeyRange": map[string]any{
			"StartingHashKey": shard.HashKeyRange.StartingHashKey,
			"EndingHashKey":   shard.HashKeyRange.EndingHashKey,
		},
		"SequenceNumberRange": map[string]any{
			"StartingSequenceNumber": shard.SequenceNumberRange.StartingSequenceNumber,
		},
	}
	if shard.SequenceNumberRange.EndingSequenceNumber != "" {
		m["SequenceNumberRange"].(map[string]any)["EndingSequenceNumber"] = shard.SequenceNumberRange.EndingSequenceNumber
	}
	return m
}

// allShards renders every shard of the stream, open or closed, in shard-ID
// order (Shards is append-only and IDs are fixed-width, so slice order is ID
// order). DescribeStream answers from this: a split or merge parent stays in
// its response, carrying the EndingSequenceNumber that closed it, which is
// how a consumer follows shard lineage.
func allShards(st *Stream) []map[string]any {
	shards := make([]map[string]any, 0, len(st.Shards))
	for _, shard := range st.Shards {
		shards = append(shards, shardToMap(shard))
	}
	return shards
}

// openShards is allShards without the closed ones, which is what ListShards
// answers from — see the "Closed shards" row in docs/services/kinesis.md for
// the divergence that leaves.
func openShards(st *Stream) []map[string]any {
	shards := make([]map[string]any, 0, len(st.Shards))
	for _, shard := range st.Shards {
		if shard.SequenceNumberRange.EndingSequenceNumber != "" {
			continue
		}
		shards = append(shards, shardToMap(shard))
	}
	return shards
}

// shardIDOf reads a rendered shard back, for minting a cursor from the last
// shard on a page.
func shardIDOf(shard map[string]any) string {
	id, _ := shard["ShardId"].(string)
	return id
}

// shardsAfter drops the shards up to and including afterID. An afterID that
// names no shard is not an error: the listing simply resumes at the first
// shard whose ID sorts after it, which is what an exclusive cursor means.
func shardsAfter(shards []map[string]any, afterID string) []map[string]any {
	return itemsAfter(shards, afterID, shardIDOf)
}

func activeShardCount(st *Stream) int {
	count := 0
	for _, shard := range st.Shards {
		if shard.SequenceNumberRange.EndingSequenceNumber == "" {
			count++
		}
	}
	return count
}

// cmpHashKey compares two decimal hash key strings as big integers.
// Returns -1 if a < b, 0 if a == b, +1 if a > b.
func cmpHashKey(a, b string) int {
	ai, _ := new(big.Int).SetString(a, 10)
	bi, _ := new(big.Int).SetString(b, 10)
	if ai == nil || bi == nil {
		return 0
	}
	return ai.Cmp(bi)
}
