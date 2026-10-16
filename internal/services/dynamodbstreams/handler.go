package dynamodbstreams

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"time"

	"go.uber.org/zap"

	"github.com/overcast-sh/overcast/internal/middleware"
	"github.com/overcast-sh/overcast/internal/protocol"
	"github.com/overcast-sh/overcast/internal/services/dynamodb"
	"github.com/overcast-sh/overcast/internal/serviceutil"
)

// ---- Request / response types ----------------------------------------------

type listStreamsRequest struct {
	TableName               string `json:"TableName,omitempty"`
	Limit                   int    `json:"Limit,omitempty"`
	ExclusiveStartStreamArn string `json:"ExclusiveStartStreamArn,omitempty"`
}

type streamSummary struct {
	StreamArn   string `json:"StreamArn"`
	StreamLabel string `json:"StreamLabel"`
	TableName   string `json:"TableName"`
}

type listStreamsResponse struct {
	Streams []streamSummary `json:"Streams"`
}

type describeStreamRequest struct {
	StreamArn             string `json:"StreamArn"`
	Limit                 int    `json:"Limit,omitempty"`
	ExclusiveStartShardId string `json:"ExclusiveStartShardId,omitempty"`
}

type seqRange struct {
	StartingSequenceNumber string `json:"StartingSequenceNumber"`
}

type shardDesc struct {
	ShardId             string   `json:"ShardId"`
	SequenceNumberRange seqRange `json:"SequenceNumberRange"`
}

type streamDescription struct {
	StreamArn               string      `json:"StreamArn"`
	StreamLabel             string      `json:"StreamLabel"`
	StreamStatus            string      `json:"StreamStatus"`
	StreamViewType          string      `json:"StreamViewType"`
	CreationRequestDateTime float64     `json:"CreationRequestDateTime,omitempty"`
	TableName               string      `json:"TableName"`
	KeySchema               any         `json:"KeySchema"`
	Shards                  []shardDesc `json:"Shards"`
}

type describeStreamResponse struct {
	StreamDescription streamDescription `json:"StreamDescription"`
}

type getShardIteratorRequest struct {
	StreamArn         string `json:"StreamArn"`
	ShardId           string `json:"ShardId"`
	ShardIteratorType string `json:"ShardIteratorType"`
	SequenceNumber    string `json:"SequenceNumber,omitempty"`
	// Timestamp is Unix epoch seconds (fractional, like every AWS JSON
	// timestamp); required when ShardIteratorType is AT_TIMESTAMP.
	Timestamp *float64 `json:"Timestamp,omitempty"`
}

type getShardIteratorResponse struct {
	ShardIterator string `json:"ShardIterator"`
}

type getRecordsRequest struct {
	ShardIterator string `json:"ShardIterator"`
	Limit         int    `json:"Limit,omitempty"`
}

// streamRecord mirrors the DynamoDB Streams API wire format.
type streamRecord struct {
	EventID      string     `json:"eventID"`
	EventVersion string     `json:"eventVersion"`
	EventSource  string     `json:"eventSource"`
	AWSRegion    string     `json:"awsRegion,omitempty"`
	EventName    string     `json:"eventName"`
	Dynamodb     recordBody `json:"dynamodb"`
}

type recordBody struct {
	ApproximateCreationDateTime float64       `json:"ApproximateCreationDateTime"`
	Keys                        dynamodb.Item `json:"Keys"`
	NewImage                    dynamodb.Item `json:"NewImage,omitempty"`
	OldImage                    dynamodb.Item `json:"OldImage,omitempty"`
	SequenceNumber              string        `json:"SequenceNumber"`
	StreamViewType              string        `json:"StreamViewType"`
}

type getRecordsResponse struct {
	Records           []streamRecord `json:"Records"`
	NextShardIterator string         `json:"NextShardIterator"`
}

// ---- shardIterator token ---------------------------------------------------

// shardToken is the opaque cursor encoded as base64(JSON).
//
// Region is carried alongside the table name because a DynamoDB stream is a
// regional resource and stream records are stored under a region-qualified
// table key (dynamodb's dynamoStore.tableKey — issue #673). Without it,
// GetRecords would resolve the table name against whatever region the
// GetRecords call itself happened to be signed for, which for two same-named
// tables is the wrong stream. It is minted from the table's own stream ARN, so
// it names the region the stream actually lives in rather than the region the
// GetShardIterator caller claimed.
//
// A token minted before this field existed decodes with Region == "", which
// getRecordsTyped treats as "use the request's region" — the pre-#673
// behaviour — so iterators handed out before an upgrade keep working.
type shardToken struct {
	Table    string `json:"t"`
	Region   string `json:"r,omitempty"`
	AfterSeq int64  `json:"s"`
}

func encodeIterator(region, tableName string, afterSeq int64) string {
	tok, _ := json.Marshal(shardToken{Table: tableName, Region: region, AfterSeq: afterSeq})
	return base64.StdEncoding.EncodeToString(tok)
}

func decodeIterator(raw string) (shardToken, error) {
	b, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		return shardToken{}, fmt.Errorf("invalid shard iterator (base64): %w", err)
	}
	var tok shardToken
	if err := json.Unmarshal(b, &tok); err != nil {
		return shardToken{}, fmt.Errorf("invalid shard iterator (json): %w", err)
	}
	return tok, nil
}

// shardID returns the canonical (single) shard ID for a table's stream.
func shardID(tableName string) string {
	return "shardId-00000000000000000001-" + tableName
}

// ---- Handlers --------------------------------------------------------------

// ListStreams handles DynamoDBStreams_20120810.ListStreams.
func (h *handler) ListStreams(w http.ResponseWriter, r *http.Request) {
	var req listStreamsRequest
	if !serviceutil.DecodeJSON(w, r, &req) {
		return
	}
	resp, aerr := h.listStreamsTyped(r.Context(), &req)
	if aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}
	protocol.WriteJSON(w, r, http.StatusOK, resp)
}

func (h *handler) listStreamsTyped(ctx context.Context, req *listStreamsRequest) (*listStreamsResponse, *protocol.AWSError) {
	tables, err := h.ddb.ListStreamEnabledTables(ctx)
	if err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}

	summaries := make([]streamSummary, 0, len(tables))
	for _, t := range tables {
		if req.TableName != "" && t.TableName != req.TableName {
			continue
		}
		summaries = append(summaries, streamSummary{
			StreamArn:   t.LatestStreamArn,
			StreamLabel: t.LatestStreamLabel,
			TableName:   t.TableName,
		})
	}

	return &listStreamsResponse{Streams: summaries}, nil
}

// DescribeStream handles DynamoDBStreams_20120810.DescribeStream.
func (h *handler) DescribeStream(w http.ResponseWriter, r *http.Request) {
	var req describeStreamRequest
	if !serviceutil.DecodeJSON(w, r, &req) {
		return
	}
	if !serviceutil.RequireString(w, r, req.StreamArn, "StreamArn") {
		return
	}
	resp, aerr := h.describeStreamTyped(r.Context(), &req)
	if aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}
	protocol.WriteJSON(w, r, http.StatusOK, resp)
}

func (h *handler) describeStreamTyped(ctx context.Context, req *describeStreamRequest) (*describeStreamResponse, *protocol.AWSError) {
	if req.StreamArn == "" {
		return nil, validationError("StreamArn is required")
	}

	t, err := h.ddb.GetStreamTable(ctx, req.StreamArn)
	if err != nil {
		return nil, &protocol.AWSError{
			Code:       "ResourceNotFoundException",
			Message:    "Stream not found: " + req.StreamArn,
			HTTPStatus: http.StatusBadRequest,
		}
	}

	status := "ENABLED"
	if !t.StreamSpecification.StreamEnabled {
		status = "DISABLED"
	}

	desc := streamDescription{
		StreamArn:      t.LatestStreamArn,
		StreamLabel:    t.LatestStreamLabel,
		StreamStatus:   status,
		StreamViewType: t.StreamSpecification.StreamViewType,
		TableName:      t.TableName,
		KeySchema:      t.KeySchema,
		Shards: []shardDesc{
			{
				ShardId:             shardID(t.TableName),
				SequenceNumberRange: seqRange{StartingSequenceNumber: "000000000000000000001"},
			},
		},
	}

	return &describeStreamResponse{StreamDescription: desc}, nil
}

// GetShardIterator handles DynamoDBStreams_20120810.GetShardIterator.
func (h *handler) GetShardIterator(w http.ResponseWriter, r *http.Request) {
	var req getShardIteratorRequest
	if !serviceutil.DecodeJSON(w, r, &req) {
		return
	}
	if !serviceutil.RequireString(w, r, req.StreamArn, "StreamArn") {
		return
	}
	if !serviceutil.RequireString(w, r, req.ShardId, "ShardId") {
		return
	}
	resp, aerr := h.getShardIteratorTyped(r.Context(), &req)
	if aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}
	protocol.WriteJSON(w, r, http.StatusOK, resp)
}

func (h *handler) getShardIteratorTyped(ctx context.Context, req *getShardIteratorRequest) (*getShardIteratorResponse, *protocol.AWSError) {
	if req.StreamArn == "" {
		return nil, validationError("StreamArn is required")
	}
	if req.ShardId == "" {
		return nil, validationError("ShardId is required")
	}

	t, err := h.ddb.GetStreamTable(ctx, req.StreamArn)
	if err != nil {
		return nil, &protocol.AWSError{
			Code:       "ResourceNotFoundException",
			Message:    "Stream not found: " + req.StreamArn,
			HTTPStatus: http.StatusBadRequest,
		}
	}

	// The stream's own region, read back from the ARN GetStreamTable just
	// matched. GetStreamTable only searches the caller's region, so this is
	// always the caller's region too — reading it from the ARN rather than the
	// context simply means the iterator names a region that a stream provably
	// exists in.
	region := serviceutil.ARNRegion(t.LatestStreamArn)
	streamCtx := ctx
	if region != "" {
		streamCtx = middleware.ContextWithRegion(ctx, region)
	}

	var afterSeq int64
	switch req.ShardIteratorType {
	case "TRIM_HORIZON":
		afterSeq = 0
	case "LATEST":
		// Position at the current end — records written after this iterator
		// will be visible, records written before will not.
		_, latest, sErr := h.ddb.GetStreamRecordsSince(streamCtx, t.TableName, 0, 0)
		if sErr != nil {
			return nil, protocol.Wrap(protocol.ErrInternalError, sErr)
		}
		afterSeq = latest
	case "AT_SEQUENCE_NUMBER":
		if req.SequenceNumber == "" {
			return nil, &protocol.AWSError{
				Code:       "ValidationException",
				Message:    "SequenceNumber is required for AT_SEQUENCE_NUMBER",
				HTTPStatus: http.StatusBadRequest,
			}
		}
		var seq int64
		if _, err := fmt.Sscanf(req.SequenceNumber, "%d", &seq); err != nil {
			seq = 0
		}
		afterSeq = seq - 1 // include the record AT this sequence number
		if afterSeq < 0 {
			afterSeq = 0
		}
	case "AFTER_SEQUENCE_NUMBER":
		if req.SequenceNumber == "" {
			return nil, &protocol.AWSError{
				Code:       "ValidationException",
				Message:    "SequenceNumber is required for AFTER_SEQUENCE_NUMBER",
				HTTPStatus: http.StatusBadRequest,
			}
		}
		if _, err := fmt.Sscanf(req.SequenceNumber, "%d", &afterSeq); err != nil {
			afterSeq = 0
		}
	case "AT_TIMESTAMP":
		if req.Timestamp == nil {
			return nil, validationError("Timestamp is required for AT_TIMESTAMP")
		}
		tsMillis := int64(math.Round(*req.Timestamp * 1000))
		if tsMillis > time.Now().UnixMilli() {
			return nil, validationError("Timestamp must not be in the future")
		}
		// Point at the first record created at or after the timestamp. A
		// timestamp older than every retained record resolves to the first
		// record (TRIM_HORIZON); one newer than every record resolves to the
		// end, so only later writes are returned.
		recs, latest, sErr := h.ddb.GetStreamRecordsSince(streamCtx, t.TableName, 0, 0)
		if sErr != nil {
			return nil, protocol.Wrap(protocol.ErrInternalError, sErr)
		}
		afterSeq = latest
		for _, rec := range recs {
			if rec.CreatedAt >= tsMillis {
				afterSeq = rec.SequenceNumber - 1
				if afterSeq < 0 {
					afterSeq = 0
				}
				break
			}
		}
	default:
		return nil, &protocol.AWSError{
			Code:       "ValidationException",
			Message:    "Invalid ShardIteratorType: " + req.ShardIteratorType,
			HTTPStatus: http.StatusBadRequest,
		}
	}

	iter := encodeIterator(region, t.TableName, afterSeq)
	h.log.Debug("shard iterator created",
		zap.String("table", t.TableName),
		zap.String("region", region),
		zap.String("type", req.ShardIteratorType),
		zap.Int64("afterSeq", afterSeq),
	)
	return &getShardIteratorResponse{ShardIterator: iter}, nil
}

// GetRecords handles DynamoDBStreams_20120810.GetRecords.
func (h *handler) GetRecords(w http.ResponseWriter, r *http.Request) {
	var req getRecordsRequest
	if !serviceutil.DecodeJSON(w, r, &req) {
		return
	}
	if !serviceutil.RequireString(w, r, req.ShardIterator, "ShardIterator") {
		return
	}
	resp, aerr := h.getRecordsTyped(r.Context(), &req)
	if aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}
	protocol.WriteJSON(w, r, http.StatusOK, resp)
}

func (h *handler) getRecordsTyped(ctx context.Context, req *getRecordsRequest) (*getRecordsResponse, *protocol.AWSError) {
	if req.ShardIterator == "" {
		return nil, validationError("ShardIterator is required")
	}
	tok, err := decodeIterator(req.ShardIterator)
	if err != nil {
		return nil, &protocol.AWSError{
			Code:       "ValidationException",
			Message:    "Invalid ShardIterator: " + err.Error(),
			HTTPStatus: http.StatusBadRequest,
		}
	}
	tableName, afterSeq := tok.Table, tok.AfterSeq

	limit := req.Limit
	if limit <= 0 {
		limit = 1000
	}

	// Read the stream in the region its iterator names, not the region this
	// GetRecords call was signed for. The iterator could only have been minted
	// by a GetShardIterator that found the stream in that region, so this
	// resolves the same stream the caller paged from — for two same-named
	// tables in two regions, the region is the only thing that tells them
	// apart (issue #673). A legacy token carries no region and keeps the old
	// context-derived behaviour.
	streamCtx := ctx
	if tok.Region != "" {
		streamCtx = middleware.ContextWithRegion(ctx, tok.Region)
	}

	recs, latestSeq, sErr := h.ddb.GetStreamRecordsSince(streamCtx, tableName, afterSeq, limit)
	if sErr != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, sErr)
	}

	// Convert internal StreamRecord to wire format.
	wireRecs := make([]streamRecord, 0, len(recs))
	for i, rec := range recs {
		body := recordBody{
			ApproximateCreationDateTime: float64(rec.CreatedAt) / 1000.0,
			Keys:                        rec.Keys,
			SequenceNumber:              fmt.Sprintf("%021d", rec.SequenceNumber),
		}
		// Only include images if non-empty (they're nil for KEYS_ONLY).
		if len(rec.NewImage) > 0 {
			body.NewImage = rec.NewImage
		}
		if len(rec.OldImage) > 0 {
			body.OldImage = rec.OldImage
		}
		wireRecs = append(wireRecs, streamRecord{
			EventID:      fmt.Sprintf("%s-%d", tableName, rec.SequenceNumber),
			EventVersion: "1.1",
			EventSource:  "aws:dynamodb",
			// AWS stamps every stream record with the region it came from;
			// with streams now region-scoped there is a real region to report,
			// and a consumer can tell two same-named tables' records apart.
			// Omitted for a legacy region-less iterator (omitempty).
			AWSRegion: tok.Region,
			EventName: rec.EventName,
			Dynamodb:  body,
		})
		_ = i
	}

	nextIter := encodeIterator(tok.Region, tableName, latestSeq)
	return &getRecordsResponse{
		Records:           wireRecs,
		NextShardIterator: nextIter,
	}, nil
}

func validationError(message string) *protocol.AWSError {
	return &protocol.AWSError{
		Code:       "ValidationException",
		Message:    message,
		HTTPStatus: http.StatusBadRequest,
	}
}
