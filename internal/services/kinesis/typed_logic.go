package kinesis

import (
	"context"
	"encoding/base64"
	"fmt"
	"math/big"
	"net/http"
	"strings"

	"go.uber.org/zap"

	"github.com/overcast-sh/overcast/internal/events"
	"github.com/overcast-sh/overcast/internal/middleware"
	"github.com/overcast-sh/overcast/internal/protocol"
	"github.com/overcast-sh/overcast/internal/serviceutil"
)

type createStreamRequest struct {
	StreamName        string             `json:"StreamName"`
	ShardCount        int                `json:"ShardCount"`
	Tags              map[string]string  `json:"Tags"`
	StreamModeDetails *StreamModeDetails `json:"StreamModeDetails"`
}

type deleteStreamRequest struct {
	StreamName string `json:"StreamName"`
}

// describeStreamRequest pages its Shards list the same way ListShards does,
// with its own documented default and cap (100) and HasMoreShards in place
// of a NextToken.
type describeStreamRequest struct {
	StreamName            string `json:"StreamName"`
	Limit                 *int   `json:"Limit"`
	ExclusiveStartShardId string `json:"ExclusiveStartShardId"`
}

type describeStreamResponse struct {
	StreamDescription map[string]any `json:"StreamDescription"`
}

type describeStreamSummaryRequest struct {
	StreamName string `json:"StreamName"`
}

type describeStreamSummaryResponse struct {
	StreamDescriptionSummary map[string]any `json:"StreamDescriptionSummary"`
}

// listStreamsRequest carries ListStreams' two interchangeable cursors.
// ExclusiveStartStreamName is the one the reference describes in prose ("you
// can request more streams by using the name of the last stream returned ...
// in the ExclusiveStartStreamName parameter"); NextToken is the modeled
// member that means the same thing, and wins when both are given because it
// is the one this service minted.
type listStreamsRequest struct {
	Limit                    *int   `json:"Limit"`
	ExclusiveStartStreamName string `json:"ExclusiveStartStreamName"`
	NextToken                string `json:"NextToken"`
}

// streamSummary is ListStreamsOutput's StreamSummaries element. AWS returns
// it alongside the older StreamNames list, not instead of it.
type streamSummary struct {
	StreamName              string             `json:"StreamName"`
	StreamARN               string             `json:"StreamARN"`
	StreamStatus            string             `json:"StreamStatus"`
	StreamModeDetails       *StreamModeDetails `json:"StreamModeDetails"`
	StreamCreationTimestamp int64              `json:"StreamCreationTimestamp"`
}

type listStreamsResponse struct {
	StreamNames     []string        `json:"StreamNames"`
	HasMoreStreams  bool            `json:"HasMoreStreams"`
	StreamSummaries []streamSummary `json:"StreamSummaries"`
	NextToken       string          `json:"NextToken,omitempty"`
}

type putRecordRequest struct {
	StreamName      string `json:"StreamName"`
	Data            []byte `json:"Data"`
	PartitionKey    string `json:"PartitionKey"`
	ExplicitHashKey string `json:"ExplicitHashKey"`
}

type putRecordResponse struct {
	ShardId        string `json:"ShardId"`
	SequenceNumber string `json:"SequenceNumber"`
}

type putRecordsRequest struct {
	StreamName string           `json:"StreamName"`
	Records    []putRecordsItem `json:"Records"`
}

type putRecordsItem struct {
	Data            []byte `json:"Data"`
	PartitionKey    string `json:"PartitionKey"`
	ExplicitHashKey string `json:"ExplicitHashKey"`
}

type putRecordsEntry struct {
	ShardId        string `json:"ShardId"`
	SequenceNumber string `json:"SequenceNumber"`
}

type putRecordsResponse struct {
	FailedRecordCount int               `json:"FailedRecordCount"`
	Records           []putRecordsEntry `json:"Records"`
}

type getShardIteratorRequest struct {
	StreamName             string `json:"StreamName"`
	ShardId                string `json:"ShardId"`
	ShardIteratorType      string `json:"ShardIteratorType"`
	StartingSequenceNumber string `json:"StartingSequenceNumber"`
}

type getShardIteratorResponse struct {
	ShardIterator string `json:"ShardIterator"`
}

type getRecordsRequest struct {
	ShardIterator string `json:"ShardIterator"`
	Limit         int    `json:"Limit"`
}

type recordResponse struct {
	SequenceNumber              string  `json:"SequenceNumber"`
	ApproximateArrivalTimestamp float64 `json:"ApproximateArrivalTimestamp"`
	Data                        []byte  `json:"Data"`
	PartitionKey                string  `json:"PartitionKey"`
}

type getRecordsResponse struct {
	Records            []recordResponse `json:"Records"`
	NextShardIterator  string           `json:"NextShardIterator"`
	MillisBehindLatest int              `json:"MillisBehindLatest"`
}

// listShardsRequest is the one operation here whose NextToken excludes other
// members: "Don't specify StreamName or StreamCreationTimestamp if you
// specify NextToken because the latter unambiguously identifies the stream",
// and ExclusiveStartShardId carries the same restriction.
//
// StreamARN is accepted because the reference requires one of the two ("you
// must use either the StreamARN or the StreamName parameter, or both"), so
// the error for naming neither would otherwise be a lie. The other
// operations in this package still take StreamName only — see the remaining
// gaps in docs/dev/compatibility/services/kinesis.yaml.
type listShardsRequest struct {
	StreamName              string   `json:"StreamName"`
	StreamARN               string   `json:"StreamARN"`
	NextToken               string   `json:"NextToken"`
	ExclusiveStartShardId   string   `json:"ExclusiveStartShardId"`
	MaxResults              *int     `json:"MaxResults"`
	StreamCreationTimestamp *float64 `json:"StreamCreationTimestamp"`
}

type listShardsResponse struct {
	Shards    []map[string]any `json:"Shards"`
	NextToken string           `json:"NextToken,omitempty"`
}

type splitShardRequest struct {
	StreamName         string `json:"StreamName"`
	ShardToSplit       string `json:"ShardToSplit"`
	NewStartingHashKey string `json:"NewStartingHashKey"`
}

type mergeShardsRequest struct {
	StreamName           string `json:"StreamName"`
	ShardToMerge         string `json:"ShardToMerge"`
	AdjacentShardToMerge string `json:"AdjacentShardToMerge"`
}

type addTagsToStreamRequest struct {
	StreamName string            `json:"StreamName"`
	Tags       map[string]string `json:"Tags"`
}

// listTagsForStreamRequest pages tags by key — AWS: "To list additional tags,
// set ExclusiveStartTagKey to the last key in the response".
type listTagsForStreamRequest struct {
	StreamName           string `json:"StreamName"`
	ExclusiveStartTagKey string `json:"ExclusiveStartTagKey"`
	Limit                *int   `json:"Limit"`
}

type tagEntry struct {
	Key   string `json:"Key"`
	Value string `json:"Value"`
}

// kinesisTagCfg tunes the shared tag validator to Kinesis's error shape
// (#1052). Kinesis reports every other rejected-input case this package
// models (a bad ARN, a missing parameter) as InvalidArgumentException, and
// declares no dedicated tag-count exception, so the 50-tag limit is reported
// the same way.
var kinesisTagCfg = serviceutil.TagValidationConfig{
	ExceededCode:    "InvalidArgumentException",
	InvalidCode:     "InvalidArgumentException",
	ExceededMessage: "Tag limit exceeded for the stream.",
}

// createStreamTags copies CreateStream's inline Tags into the map the stream
// stores. The result is never nil, matching what CreateStream stored before it
// accepted tags; the tag handlers still nil-check, for streams persisted by an
// older build.
func createStreamTags(incoming map[string]string) map[string]string {
	tags := make(map[string]string, len(incoming))
	for k, v := range incoming {
		tags[k] = v
	}
	return tags
}

// sortedTagEntries flattens a stream's tag map into a Key-sorted slice.
// Go map iteration order is randomized per process, so both wire paths
// (the JSON1.1 handler and the CBOR typed dispatch) must serialize through
// this helper to keep the Tags array order deterministic.
func sortedTagEntries(tags map[string]string) []tagEntry {
	return serviceutil.TagElements(tags, func(k, v string) tagEntry {
		return tagEntry{Key: k, Value: v}
	})
}

type listTagsForStreamResponse struct {
	Tags        []tagEntry `json:"Tags"`
	HasMoreTags bool       `json:"HasMoreTags"`
}

type removeTagsFromStreamRequest struct {
	StreamName string   `json:"StreamName"`
	TagKeys    []string `json:"TagKeys"`
}

// The ARN-addressed trio. Kinesis added TagResource/UntagResource/
// ListTagsForResource alongside the older stream-name operations; both
// spellings address the same tag set on the same stream.

type tagResourceRequest struct {
	ResourceARN string            `json:"ResourceARN"`
	Tags        map[string]string `json:"Tags"`
}

type untagResourceRequest struct {
	ResourceARN string   `json:"ResourceARN"`
	TagKeys     []string `json:"TagKeys"`
}

type listTagsForResourceRequest struct {
	ResourceARN string `json:"ResourceARN"`
}

type listTagsForResourceResponse struct {
	Tags []tagEntry `json:"Tags"`
}

// streamNameFromARN extracts the stream name from a Kinesis stream ARN.
// Kinesis' ARN-addressed tag operations accept only stream ARNs here: consumers
// are the other taggable Kinesis resource and Overcast does not implement them.
func streamNameFromARN(arn string) (string, *protocol.AWSError) {
	invalid := invalidArgument(fmt.Sprintf("Invalid stream ARN: %s", arn))
	parts := strings.SplitN(arn, ":", 6)
	if len(parts) < 6 || !strings.HasPrefix(parts[5], "stream/") {
		return "", invalid
	}
	name := strings.TrimPrefix(parts[5], "stream/")
	if name == "" {
		return "", invalid
	}
	return name, nil
}

// streamForResourceARN resolves an ARN-addressed tag request to its stream.
func (h *Handler) streamForResourceARN(ctx context.Context, arn string) (*Stream, *protocol.AWSError) {
	if arn == "" {
		return nil, errMissingParameter("ResourceARN")
	}
	name, aerr := streamNameFromARN(arn)
	if aerr != nil {
		return nil, aerr
	}
	return h.store.getStream(ctx, name)
}

type retentionPeriodRequest struct {
	StreamName           string `json:"StreamName"`
	RetentionPeriodHours int    `json:"RetentionPeriodHours"`
}

func (h *Handler) createStreamTyped(ctx context.Context, req *createStreamRequest) (*struct{}, *protocol.AWSError) {
	if req.StreamName == "" {
		return nil, errMissingParameter("StreamName")
	}
	shardCount := req.ShardCount
	if shardCount <= 0 {
		shardCount = 1
	}
	// Request-shape validation before the duplicate-name check resolves
	// against the store — the same ordering createLogGroupTyped uses
	// (internal/services/cloudwatch/logs/typed_logic.go) — so a rejected
	// create must not depend on whether the name happens to collide (#1052).
	if aerr := serviceutil.ValidateTags(kinesisTagCfg, req.Tags); aerr != nil {
		return nil, aerr
	}
	if _, aerr := h.store.getStream(ctx, req.StreamName); aerr == nil {
		return nil, errStreamAlreadyExists(req.StreamName)
	}
	st := &Stream{
		StreamName:           req.StreamName,
		StreamARN:            streamARN(h.cfg.AccountID, middleware.RegionFromContext(ctx, h.cfg.Region), req.StreamName),
		StreamStatus:         "ACTIVE",
		ShardCount:           shardCount,
		Shards:               buildInitialShards(shardCount),
		Tags:                 createStreamTags(req.Tags),
		CreatedAt:            h.clk.Now().UTC(),
		RetentionPeriodHours: 24,
		StreamModeDetails:    req.StreamModeDetails,
		EncryptionType:       "NONE",
	}
	if aerr := h.store.putStream(ctx, st); aerr != nil {
		return nil, aerr
	}
	h.publishCtx(ctx, events.KinesisStreamCreated, events.ResourcePayload{Name: req.StreamName, ARN: st.StreamARN})
	h.log.Debug("stream created", zap.String("stream", req.StreamName), zap.Int("shards", shardCount))
	return &struct{}{}, nil
}

func (h *Handler) deleteStreamTyped(ctx context.Context, req *deleteStreamRequest) (*struct{}, *protocol.AWSError) {
	if req.StreamName == "" {
		return nil, errMissingParameter("StreamName")
	}
	if _, aerr := h.store.getStream(ctx, req.StreamName); aerr != nil {
		return nil, aerr
	}
	if aerr := h.store.deleteStream(ctx, req.StreamName); aerr != nil {
		return nil, aerr
	}
	h.publishCtx(ctx, events.KinesisStreamDeleted, events.ResourcePayload{Name: req.StreamName, ARN: streamARN(h.cfg.AccountID, h.cfg.Region, req.StreamName)})
	return &struct{}{}, nil
}

func (h *Handler) describeStreamTyped(ctx context.Context, req *describeStreamRequest) (*describeStreamResponse, *protocol.AWSError) {
	if req.StreamName == "" {
		return nil, errMissingParameter("StreamName")
	}
	if aerr := validateLimit(req.Limit, "Limit", describeStreamModelMax); aerr != nil {
		return nil, aerr
	}
	st, aerr := h.store.getStream(ctx, req.StreamName)
	if aerr != nil {
		return nil, aerr
	}
	// Every shard, not just the open ones: AWS DescribeStream reports a
	// closed parent alongside its children (ListShards is the one that
	// omits them here).
	shards := shardsAfter(allShards(st), req.ExclusiveStartShardId)
	page, hasMore := pageOf(shards, req.Limit, describeStreamDefaultLimit, describeStreamMaxLimit)
	return &describeStreamResponse{StreamDescription: toStreamDescription(st, page, hasMore)}, nil
}

func (h *Handler) describeStreamSummaryTyped(ctx context.Context, req *describeStreamSummaryRequest) (*describeStreamSummaryResponse, *protocol.AWSError) {
	if req.StreamName == "" {
		return nil, errMissingParameter("StreamName")
	}
	st, aerr := h.store.getStream(ctx, req.StreamName)
	if aerr != nil {
		return nil, aerr
	}
	return &describeStreamSummaryResponse{StreamDescriptionSummary: toStreamDescriptionSummary(st)}, nil
}

func (h *Handler) listStreamsTyped(ctx context.Context, req *listStreamsRequest) (*listStreamsResponse, *protocol.AWSError) {
	if aerr := validateLimit(req.Limit, "Limit", listStreamsModelMax); aerr != nil {
		return nil, aerr
	}
	after := req.ExclusiveStartStreamName
	if req.NextToken != "" {
		cur, aerr := h.openPageToken(req.NextToken)
		if aerr != nil {
			return nil, aerr
		}
		after = cur.After
	}
	streams, aerr := h.store.listStreams(ctx)
	if aerr != nil {
		return nil, aerr
	}
	// listStreams sorts by name, which is the order the cursor walks.
	if after != "" {
		streams = itemsAfter(streams, after, func(st Stream) string { return st.StreamName })
	}
	page, hasMore := pageOf(streams, req.Limit, listStreamsDefaultLimit, listStreamsMaxLimit)

	names := make([]string, len(page))
	summaries := make([]streamSummary, len(page))
	for i, st := range page {
		names[i] = st.StreamName
		summaries[i] = streamSummary{
			StreamName:              st.StreamName,
			StreamARN:               st.StreamARN,
			StreamStatus:            st.StreamStatus,
			StreamModeDetails:       st.effectiveStreamModeDetails(),
			StreamCreationTimestamp: st.CreatedAt.Unix(),
		}
	}
	out := &listStreamsResponse{StreamNames: names, HasMoreStreams: hasMore, StreamSummaries: summaries}
	if hasMore {
		out.NextToken = h.sealPageToken("", names[len(names)-1])
	}
	return out, nil
}

// shardForRecord resolves the open shard index a PutRecord/PutRecords entry
// lands on: the shard whose HashKeyRange contains the MD5 hash of
// partitionKey, or — when explicitHashKey is given — the shard whose range
// contains that value instead, overriding the partition key entirely (the
// PutRecord/PutRecords reference: "You can override hashing the partition
// key to determine the shard by explicitly specifying a hash value using the
// ExplicitHashKey parameter"). An explicitHashKey that is not a valid
// non-negative integer, or that falls outside every open shard's range, is
// AWS's InvalidArgumentException; neither PutRecord nor PutRecords models a
// per-record error code for it (only ProvisionedThroughputExceededException
// and InternalFailure are), so this fails the whole request.
func shardForRecord(shards []Shard, partitionKey, explicitHashKey string) (int, *protocol.AWSError) {
	hashKey := partitionKeyHashKey(partitionKey)
	if explicitHashKey != "" {
		hk, ok := new(big.Int).SetString(explicitHashKey, 10)
		if !ok || hk.Sign() < 0 {
			return 0, invalidArgument(fmt.Sprintf("Invalid ExplicitHashKey %s", explicitHashKey))
		}
		hashKey = hk
	}
	idx := pickShard(shards, hashKey)
	if idx >= 0 {
		return idx, nil
	}
	if explicitHashKey != "" {
		return 0, invalidArgument(fmt.Sprintf("Invalid ExplicitHashKey %s", explicitHashKey))
	}
	// The computed partition-key hash landed outside every open shard's
	// range. buildInitialShards/splitShardTyped/mergeShardsTyped keep open
	// shards' ranges contiguous across the full 128-bit hash space, so this
	// is an invariant violation rather than a normal request — fall back to
	// the first open shard instead of failing the whole request.
	if idx = firstOpenShard(shards); idx >= 0 {
		return idx, nil
	}
	return 0, nil
}

func (h *Handler) putRecordTyped(ctx context.Context, req *putRecordRequest) (*putRecordResponse, *protocol.AWSError) {
	if req.StreamName == "" {
		return nil, errMissingParameter("StreamName")
	}
	st, aerr := h.store.getStream(ctx, req.StreamName)
	if aerr != nil {
		return nil, aerr
	}
	shardIdx, aerr := shardForRecord(st.Shards, req.PartitionKey, req.ExplicitHashKey)
	if aerr != nil {
		return nil, aerr
	}
	shardID := st.Shards[shardIdx].ShardId
	// Persisted per-shard counter, not len(records) — storage-access-plan.md
	// A1: len() regresses after a deletion and can collide with a
	// surviving record's sequence number.
	base, aerr := h.store.nextShardSeqBlock(ctx, req.StreamName, shardID, 1)
	if aerr != nil {
		return nil, aerr
	}
	seqNo := formatSeqNo(shardIdx, base)
	rec := &Record{
		SequenceNumber:              seqNo,
		ApproximateArrivalTimestamp: h.clk.Now().UTC(),
		Data:                        req.Data,
		PartitionKey:                req.PartitionKey,
	}
	if aerr := h.store.putRecord(ctx, req.StreamName, shardID, rec); aerr != nil {
		return nil, aerr
	}
	return &putRecordResponse{ShardId: shardID, SequenceNumber: seqNo}, nil
}

func (h *Handler) putRecordsTyped(ctx context.Context, req *putRecordsRequest) (*putRecordsResponse, *protocol.AWSError) {
	if req.StreamName == "" {
		return nil, errMissingParameter("StreamName")
	}
	st, aerr := h.store.getStream(ctx, req.StreamName)
	if aerr != nil {
		return nil, aerr
	}

	n := len(req.Records)
	shardIdxs := make([]int, n)
	shardIDs := make([]string, n)
	counts := make(map[string]int, n)
	for i, entry := range req.Records {
		idx, aerr := shardForRecord(st.Shards, entry.PartitionKey, entry.ExplicitHashKey)
		if aerr != nil {
			return nil, aerr
		}
		shardIdxs[i] = idx
		shardIDs[i] = st.Shards[idx].ShardId
		counts[shardIDs[i]]++
	}

	// One contiguous block reservation per shard touched by this batch —
	// not one read-modify-write per record (storage-access-plan.md A1).
	bases := make(map[string]int64, len(counts))
	for shardID, count := range counts {
		base, aerr := h.store.nextShardSeqBlock(ctx, req.StreamName, shardID, count)
		if aerr != nil {
			return nil, aerr
		}
		bases[shardID] = base
	}

	now := h.clk.Now().UTC()
	results := make([]putRecordsEntry, n)
	for i, entry := range req.Records {
		shardID := shardIDs[i]
		seqNo := formatSeqNo(shardIdxs[i], bases[shardID])
		bases[shardID]++
		rec := &Record{
			SequenceNumber:              seqNo,
			ApproximateArrivalTimestamp: now,
			Data:                        entry.Data,
			PartitionKey:                entry.PartitionKey,
		}
		_ = h.store.putRecord(ctx, req.StreamName, shardID, rec)
		results[i] = putRecordsEntry{ShardId: shardID, SequenceNumber: seqNo}
	}
	return &putRecordsResponse{FailedRecordCount: 0, Records: results}, nil
}

func (h *Handler) getShardIteratorTyped(ctx context.Context, req *getShardIteratorRequest) (*getShardIteratorResponse, *protocol.AWSError) {
	if req.StreamName == "" {
		return nil, errMissingParameter("StreamName")
	}
	if _, aerr := h.store.getStream(ctx, req.StreamName); aerr != nil {
		return nil, aerr
	}
	var afterSeqNo string
	switch req.ShardIteratorType {
	case "AT_SEQUENCE_NUMBER":
		afterSeqNo = "before:" + req.StartingSequenceNumber
	case "AFTER_SEQUENCE_NUMBER":
		afterSeqNo = req.StartingSequenceNumber
	case "LATEST":
		afterSeqNo = "LATEST"
	default:
		afterSeqNo = ""
	}
	iter := encodeShardIterator(req.StreamName, req.ShardId, afterSeqNo)
	return &getShardIteratorResponse{ShardIterator: base64.StdEncoding.EncodeToString([]byte(iter))}, nil
}

func (h *Handler) getRecordsTyped(ctx context.Context, req *getRecordsRequest) (*getRecordsResponse, *protocol.AWSError) {
	if req.ShardIterator == "" {
		return nil, errMissingParameter("ShardIterator")
	}
	raw, err := base64.StdEncoding.DecodeString(req.ShardIterator)
	if err != nil {
		return nil, invalidArgument("Invalid ShardIterator")
	}
	streamName, shardID, afterSeqNo, ok := decodeShardIterator(string(raw))
	if !ok {
		return nil, invalidArgument("Invalid ShardIterator format")
	}
	limit := req.Limit
	if limit <= 0 {
		limit = 10000
	}

	var (
		records   []Record
		lastSeqNo string
	)

	switch {
	case afterSeqNo == "LATEST", strings.HasPrefix(afterSeqNo, "before:"):
		// LATEST and AT_SEQUENCE_NUMBER ("before:") iterators only need a
		// full-shard read once, to resolve their starting point on first
		// use — the NextShardIterator this call returns carries a concrete
		// sequence number, so every later poll takes the ScanPage range
		// read below instead. That one-time resolution isn't the hot path
		// A2 targets (repeated polling with a TRIM_HORIZON/
		// AFTER_SEQUENCE_NUMBER-style cursor), so it's deliberately left
		// as a full-shard read (storage-access-plan.md A2).
		allRecords, aerr := h.store.listRecords(ctx, streamName, shardID)
		if aerr != nil {
			return nil, aerr
		}
		if afterSeqNo == "LATEST" {
			if len(allRecords) > 0 {
				lastSeqNo = allRecords[len(allRecords)-1].SequenceNumber
			}
		} else {
			target := afterSeqNo[len("before:"):]
			for _, rec := range allRecords {
				if rec.SequenceNumber >= target {
					records = append(records, rec)
				}
			}
			if len(records) > limit {
				records = records[:limit]
			}
			switch {
			case len(records) > 0:
				lastSeqNo = records[len(records)-1].SequenceNumber
			case len(allRecords) > 0:
				lastSeqNo = allRecords[len(allRecords)-1].SequenceNumber
			default:
				lastSeqNo = afterSeqNo
			}
		}
	default:
		// TRIM_HORIZON ("") or a resolved/resumed cursor: range read via
		// ScanPage instead of a full-shard scan-and-slice
		// (storage-access-plan.md A2, the read-side twin of A1).
		page, aerr := h.store.listRecordsPage(ctx, streamName, shardID, afterSeqNo, limit)
		if aerr != nil {
			return nil, aerr
		}
		records = page
		// No new records past the cursor: keep the cursor exactly where it
		// was (a stable no-op iterator) rather than rewinding to the
		// shard's tail — matches real Kinesis iterators, which never
		// regress or re-deliver already-consumed records.
		lastSeqNo = afterSeqNo
		if len(records) > 0 {
			lastSeqNo = records[len(records)-1].SequenceNumber
		}
	}

	out := make([]recordResponse, len(records))
	for i, rec := range records {
		out[i] = recordResponse{
			SequenceNumber:              rec.SequenceNumber,
			ApproximateArrivalTimestamp: float64(rec.ApproximateArrivalTimestamp.Unix()),
			Data:                        rec.Data,
			PartitionKey:                rec.PartitionKey,
		}
	}
	nextIter := encodeShardIterator(streamName, shardID, lastSeqNo)
	return &getRecordsResponse{
		Records:            out,
		NextShardIterator:  base64.StdEncoding.EncodeToString([]byte(nextIter)),
		MillisBehindLatest: 0,
	}, nil
}

func (h *Handler) listShardsTyped(ctx context.Context, req *listShardsRequest) (*listShardsResponse, *protocol.AWSError) {
	if aerr := validateLimit(req.MaxResults, "MaxResults", listShardsModelMax); aerr != nil {
		return nil, aerr
	}
	streamName, after, aerr := h.listShardsCursor(req)
	if aerr != nil {
		return nil, aerr
	}
	st, aerr := h.store.getStream(ctx, streamName)
	if aerr != nil {
		return nil, aerr
	}
	shards := shardsAfter(openShards(st), after)
	page, hasMore := pageOf(shards, req.MaxResults, listShardsDefaultLimit, listShardsMaxLimit)

	out := &listShardsResponse{Shards: page}
	if hasMore {
		out.NextToken = h.sealPageToken(st.StreamName, shardIDOf(page[len(page)-1]))
	}
	return out, nil
}

// listShardsCursor resolves which stream to list and where to resume, and
// enforces the members NextToken excludes.
func (h *Handler) listShardsCursor(req *listShardsRequest) (streamName, after string, aerr *protocol.AWSError) {
	if req.NextToken != "" {
		switch {
		case req.StreamName != "":
			return "", "", invalidArgument("NextToken and StreamName cannot be provided together.")
		case req.ExclusiveStartShardId != "":
			return "", "", invalidArgument("NextToken and ExclusiveStartShardId cannot be provided together.")
		case req.StreamCreationTimestamp != nil:
			return "", "", invalidArgument("NextToken and StreamCreationTimestamp cannot be provided together.")
		}
		cur, tokenErr := h.openPageToken(req.NextToken)
		if tokenErr != nil {
			return "", "", tokenErr
		}
		return cur.Stream, cur.After, nil
	}
	if req.StreamName != "" {
		return req.StreamName, req.ExclusiveStartShardId, nil
	}
	if req.StreamARN == "" {
		return "", "", invalidArgument("Either StreamName or StreamARN must be provided.")
	}
	name, arnErr := streamNameFromARN(req.StreamARN)
	if arnErr != nil {
		return "", "", arnErr
	}
	return name, req.ExclusiveStartShardId, nil
}

func (h *Handler) splitShardTyped(ctx context.Context, req *splitShardRequest) (*struct{}, *protocol.AWSError) {
	if req.StreamName == "" {
		return nil, errMissingParameter("StreamName")
	}
	st, aerr := h.store.getStream(ctx, req.StreamName)
	if aerr != nil {
		return nil, aerr
	}
	idx := -1
	for i, shard := range st.Shards {
		if shard.ShardId == req.ShardToSplit && shard.SequenceNumberRange.EndingSequenceNumber == "" {
			idx = i
			break
		}
	}
	if idx < 0 {
		return nil, shardNotFound(req.ShardToSplit, req.StreamName)
	}
	orig := st.Shards[idx]
	nowSeq := fmt.Sprintf("49%019d", len(st.Shards))
	st.Shards[idx].SequenceNumberRange.EndingSequenceNumber = nowSeq
	child1 := Shard{
		ShardId: fmt.Sprintf("shardId-%012d", len(st.Shards)),
		HashKeyRange: HashKeyRange{
			StartingHashKey: orig.HashKeyRange.StartingHashKey,
			EndingHashKey:   req.NewStartingHashKey,
		},
		SequenceNumberRange: SequenceNumberRange{StartingSequenceNumber: nowSeq},
	}
	child2 := Shard{
		ShardId: fmt.Sprintf("shardId-%012d", len(st.Shards)+1),
		HashKeyRange: HashKeyRange{
			StartingHashKey: req.NewStartingHashKey,
			EndingHashKey:   orig.HashKeyRange.EndingHashKey,
		},
		SequenceNumberRange: SequenceNumberRange{StartingSequenceNumber: nowSeq},
	}
	st.Shards = append(st.Shards, child1, child2)
	st.ShardCount = activeShardCount(st)
	if aerr := h.store.putStream(ctx, st); aerr != nil {
		return nil, aerr
	}
	return &struct{}{}, nil
}

func (h *Handler) mergeShardsTyped(ctx context.Context, req *mergeShardsRequest) (*struct{}, *protocol.AWSError) {
	if req.StreamName == "" {
		return nil, errMissingParameter("StreamName")
	}
	st, aerr := h.store.getStream(ctx, req.StreamName)
	if aerr != nil {
		return nil, aerr
	}
	mergeIdx, adjIdx := -1, -1
	for i, shard := range st.Shards {
		if shard.SequenceNumberRange.EndingSequenceNumber != "" {
			continue
		}
		if shard.ShardId == req.ShardToMerge {
			mergeIdx = i
		}
		if shard.ShardId == req.AdjacentShardToMerge {
			adjIdx = i
		}
	}
	if mergeIdx < 0 {
		return nil, shardNotFound(req.ShardToMerge, req.StreamName)
	}
	if adjIdx < 0 {
		return nil, shardNotFound(req.AdjacentShardToMerge, req.StreamName)
	}
	nowSeq := fmt.Sprintf("49%019d", len(st.Shards))
	st.Shards[mergeIdx].SequenceNumberRange.EndingSequenceNumber = nowSeq
	st.Shards[adjIdx].SequenceNumberRange.EndingSequenceNumber = nowSeq
	startHash := st.Shards[mergeIdx].HashKeyRange.StartingHashKey
	endHash := st.Shards[mergeIdx].HashKeyRange.EndingHashKey
	adjStart := st.Shards[adjIdx].HashKeyRange.StartingHashKey
	adjEnd := st.Shards[adjIdx].HashKeyRange.EndingHashKey
	if cmpHashKey(adjStart, startHash) < 0 {
		startHash = adjStart
	}
	if cmpHashKey(adjEnd, endHash) > 0 {
		endHash = adjEnd
	}
	merged := Shard{
		ShardId: fmt.Sprintf("shardId-%012d", len(st.Shards)),
		HashKeyRange: HashKeyRange{
			StartingHashKey: startHash,
			EndingHashKey:   endHash,
		},
		SequenceNumberRange: SequenceNumberRange{StartingSequenceNumber: nowSeq},
	}
	st.Shards = append(st.Shards, merged)
	st.ShardCount = activeShardCount(st)
	if aerr := h.store.putStream(ctx, st); aerr != nil {
		return nil, aerr
	}
	return &struct{}{}, nil
}

func (h *Handler) addTagsToStreamTyped(ctx context.Context, req *addTagsToStreamRequest) (*struct{}, *protocol.AWSError) {
	if req.StreamName == "" {
		return nil, errMissingParameter("StreamName")
	}
	st, aerr := h.store.getStream(ctx, req.StreamName)
	if aerr != nil {
		return nil, aerr
	}
	if st.Tags == nil {
		st.Tags = map[string]string{}
	}
	merged := createStreamTags(st.Tags)
	for k, v := range req.Tags {
		merged[k] = v
	}
	// Validated against the merged set, not just the incoming delta, so the
	// tag limit holds across repeated AddTagsToStream calls (#1052).
	if aerr := serviceutil.ValidateTags(kinesisTagCfg, merged); aerr != nil {
		return nil, aerr
	}
	st.Tags = merged
	if aerr := h.store.putStream(ctx, st); aerr != nil {
		return nil, aerr
	}
	return &struct{}{}, nil
}

func (h *Handler) listTagsForStreamTyped(ctx context.Context, req *listTagsForStreamRequest) (*listTagsForStreamResponse, *protocol.AWSError) {
	if req.StreamName == "" {
		return nil, errMissingParameter("StreamName")
	}
	if aerr := validateLimit(req.Limit, "Limit", listTagsModelMax); aerr != nil {
		return nil, aerr
	}
	st, aerr := h.store.getStream(ctx, req.StreamName)
	if aerr != nil {
		return nil, aerr
	}
	// sortedTagEntries is key-ordered, which is the order ExclusiveStartTagKey
	// walks.
	tags := itemsAfter(sortedTagEntries(st.Tags), req.ExclusiveStartTagKey, func(t tagEntry) string { return t.Key })
	page, hasMore := pageOf(tags, req.Limit, listTagsDefaultLimit, listTagsMaxLimit)
	return &listTagsForStreamResponse{Tags: page, HasMoreTags: hasMore}, nil
}

func (h *Handler) removeTagsFromStreamTyped(ctx context.Context, req *removeTagsFromStreamRequest) (*struct{}, *protocol.AWSError) {
	if req.StreamName == "" {
		return nil, errMissingParameter("StreamName")
	}
	st, aerr := h.store.getStream(ctx, req.StreamName)
	if aerr != nil {
		return nil, aerr
	}
	for _, k := range req.TagKeys {
		delete(st.Tags, k)
	}
	if aerr := h.store.putStream(ctx, st); aerr != nil {
		return nil, aerr
	}
	return &struct{}{}, nil
}

func (h *Handler) tagResourceTyped(ctx context.Context, req *tagResourceRequest) (*struct{}, *protocol.AWSError) {
	st, aerr := h.streamForResourceARN(ctx, req.ResourceARN)
	if aerr != nil {
		return nil, aerr
	}
	if st.Tags == nil {
		st.Tags = map[string]string{}
	}
	merged := createStreamTags(st.Tags)
	for k, v := range req.Tags {
		merged[k] = v
	}
	if aerr := serviceutil.ValidateTags(kinesisTagCfg, merged); aerr != nil {
		return nil, aerr
	}
	st.Tags = merged
	if aerr := h.store.putStream(ctx, st); aerr != nil {
		return nil, aerr
	}
	return &struct{}{}, nil
}

func (h *Handler) untagResourceTyped(ctx context.Context, req *untagResourceRequest) (*struct{}, *protocol.AWSError) {
	st, aerr := h.streamForResourceARN(ctx, req.ResourceARN)
	if aerr != nil {
		return nil, aerr
	}
	for _, k := range req.TagKeys {
		delete(st.Tags, k)
	}
	if aerr := h.store.putStream(ctx, st); aerr != nil {
		return nil, aerr
	}
	return &struct{}{}, nil
}

// listTagsForResourceTyped has no HasMoreTags member: that field belongs to
// ListTagsForStream's older output shape, and adding it here would put a
// member on the wire that the modeled response does not have.
func (h *Handler) listTagsForResourceTyped(ctx context.Context, req *listTagsForResourceRequest) (*listTagsForResourceResponse, *protocol.AWSError) {
	st, aerr := h.streamForResourceARN(ctx, req.ResourceARN)
	if aerr != nil {
		return nil, aerr
	}
	return &listTagsForResourceResponse{Tags: sortedTagEntries(st.Tags)}, nil
}

func (h *Handler) increaseStreamRetentionPeriodTyped(ctx context.Context, req *retentionPeriodRequest) (*struct{}, *protocol.AWSError) {
	return h.updateRetentionPeriod(ctx, req)
}

func (h *Handler) decreaseStreamRetentionPeriodTyped(ctx context.Context, req *retentionPeriodRequest) (*struct{}, *protocol.AWSError) {
	return h.updateRetentionPeriod(ctx, req)
}

func (h *Handler) updateRetentionPeriod(ctx context.Context, req *retentionPeriodRequest) (*struct{}, *protocol.AWSError) {
	if req.StreamName == "" {
		return nil, errMissingParameter("StreamName")
	}
	st, aerr := h.store.getStream(ctx, req.StreamName)
	if aerr != nil {
		return nil, aerr
	}
	st.RetentionPeriodHours = req.RetentionPeriodHours
	if aerr := h.store.putStream(ctx, st); aerr != nil {
		return nil, aerr
	}
	return &struct{}{}, nil
}

type updateStreamModeRequest struct {
	StreamARN         string             `json:"StreamARN"`
	StreamModeDetails *StreamModeDetails `json:"StreamModeDetails"`
}

// updateStreamModeTyped handles Kinesis_20131202.UpdateStreamMode. Real
// Kinesis addresses this operation by StreamARN only (no StreamName
// alternative), unlike StartStreamEncryption/StopStreamEncryption below.
func (h *Handler) updateStreamModeTyped(ctx context.Context, req *updateStreamModeRequest) (*struct{}, *protocol.AWSError) {
	if req.StreamModeDetails == nil || req.StreamModeDetails.StreamMode == "" {
		return nil, errMissingParameter("StreamModeDetails")
	}
	st, aerr := h.streamForResourceARN(ctx, req.StreamARN)
	if aerr != nil {
		return nil, aerr
	}
	st.StreamModeDetails = req.StreamModeDetails
	if aerr := h.store.putStream(ctx, st); aerr != nil {
		return nil, aerr
	}
	return &struct{}{}, nil
}

// streamEncryptionRequest is shared by StartStreamEncryption and
// StopStreamEncryption — both accept either StreamName or StreamARN.
type streamEncryptionRequest struct {
	StreamName     string `json:"StreamName"`
	StreamARN      string `json:"StreamARN"`
	EncryptionType string `json:"EncryptionType"`
	KeyId          string `json:"KeyId"`
}

// resolveStream looks a stream up by name, falling back to ARN — the shape
// StartStreamEncryption/StopStreamEncryption's request takes (either
// identifier is accepted on real Kinesis).
func (h *Handler) resolveStream(ctx context.Context, streamName, arn string) (*Stream, *protocol.AWSError) {
	if streamName != "" {
		return h.store.getStream(ctx, streamName)
	}
	if arn != "" {
		return h.streamForResourceARN(ctx, arn)
	}
	return nil, errMissingParameter("StreamName")
}

func (h *Handler) startStreamEncryptionTyped(ctx context.Context, req *streamEncryptionRequest) (*struct{}, *protocol.AWSError) {
	st, aerr := h.resolveStream(ctx, req.StreamName, req.StreamARN)
	if aerr != nil {
		return nil, aerr
	}
	encryptionType := req.EncryptionType
	if encryptionType == "" {
		encryptionType = "KMS"
	}
	st.EncryptionType = encryptionType
	st.KeyId = req.KeyId
	if aerr := h.store.putStream(ctx, st); aerr != nil {
		return nil, aerr
	}
	return &struct{}{}, nil
}

func (h *Handler) stopStreamEncryptionTyped(ctx context.Context, req *streamEncryptionRequest) (*struct{}, *protocol.AWSError) {
	st, aerr := h.resolveStream(ctx, req.StreamName, req.StreamARN)
	if aerr != nil {
		return nil, aerr
	}
	st.EncryptionType = "NONE"
	st.KeyId = ""
	if aerr := h.store.putStream(ctx, st); aerr != nil {
		return nil, aerr
	}
	return &struct{}{}, nil
}

func (h *Handler) publishCtx(ctx context.Context, t events.Type, payload any) {
	if h.bus != nil {
		h.bus.Publish(ctx, events.Event{Type: t, Payload: payload})
	}
}

// shardNotFound answers 400, not 404: every exception in the Kinesis model is
// a client error with no httpError override, and each operation Errors
// section says the same ("ResourceNotFoundException ... HTTP Status Code:
// 400"). See errNoSuchStream in store.go.
func shardNotFound(shardID, streamName string) *protocol.AWSError {
	return &protocol.AWSError{
		Code:       "ResourceNotFoundException",
		Message:    fmt.Sprintf("Could not find shard %s in stream %s", shardID, streamName),
		HTTPStatus: http.StatusBadRequest,
	}
}
