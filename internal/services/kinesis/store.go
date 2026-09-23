package kinesis

// store.go — state access for the Kinesis emulator.
// All domain types and serialisation live here.

import (
	"context"
	"crypto/md5" //nolint:gosec // reproduces AWS's partition-key hashing, not used for security
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/overcast-sh/overcast/internal/middleware"
	"github.com/overcast-sh/overcast/internal/protocol"
	"github.com/overcast-sh/overcast/internal/serviceutil"
	"github.com/overcast-sh/overcast/internal/state"
)

const (
	nsStreams = "kinesis:streams"
	// nsRecords key: "<region>/<streamName>/<shardId>/<seqNo>" (see
	// serviceutil.RegionKey at every call site) — records from two regions
	// sharing a stream name never collide.
	nsRecords = "kinesis:records"
	// nsShardSeq holds the persisted per-shard sequence-number counter (see
	// nextShardSeqBlock, storage-access-plan.md A1). Key:
	// "<region>/<streamName>/<shardId>" — deliberately a separate namespace
	// from nsRecords so incrementing the counter never touches (or is
	// affected by) actual record storage.
	nsShardSeq = "kinesis:shardseq"
)

// Stream represents a Kinesis Data Stream.
type Stream struct {
	StreamName           string             `json:"StreamName"`
	StreamARN            string             `json:"StreamARN"`
	StreamStatus         string             `json:"StreamStatus"` // CREATING, ACTIVE, UPDATING, DELETING
	ShardCount           int                `json:"ShardCount"`
	Shards               []Shard            `json:"Shards"`
	Tags                 map[string]string  `json:"Tags,omitempty"`
	CreatedAt            time.Time          `json:"CreatedAt"`
	RetentionPeriodHours int                `json:"RetentionPeriodHours"`
	StreamModeDetails    *StreamModeDetails `json:"StreamModeDetails,omitempty"`
	EncryptionType       string             `json:"EncryptionType,omitempty"`
	KeyId                string             `json:"KeyId,omitempty"`
}

// StreamModeDetails mirrors AWS's StreamModeDetails: whether a stream is
// capacity-managed (PROVISIONED, the historical default) or scales
// automatically (ON_DEMAND). Set at CreateStream and changed in place via
// UpdateStreamMode.
type StreamModeDetails struct {
	StreamMode string `json:"StreamMode"`
}

// effectiveStreamModeDetails defaults a stream's mode to PROVISIONED — both
// for a stream created before this field existed (persisted JSON with no
// StreamModeDetails key unmarshals to nil) and for CreateStream requests
// that omit it, matching real Kinesis' default.
func (st *Stream) effectiveStreamModeDetails() *StreamModeDetails {
	if st.StreamModeDetails != nil && st.StreamModeDetails.StreamMode != "" {
		return st.StreamModeDetails
	}
	return &StreamModeDetails{StreamMode: "PROVISIONED"}
}

// effectiveEncryptionType defaults an unset EncryptionType to "NONE", the
// same way a pre-existing stream (persisted before this field existed) and a
// stream that never called StartStreamEncryption both read on real Kinesis.
func (st *Stream) effectiveEncryptionType() string {
	if st.EncryptionType == "" {
		return "NONE"
	}
	return st.EncryptionType
}

// Shard represents a Kinesis shard.
type Shard struct {
	ShardId             string              `json:"ShardId"`
	HashKeyRange        HashKeyRange        `json:"HashKeyRange"`
	SequenceNumberRange SequenceNumberRange `json:"SequenceNumberRange"`
}

// HashKeyRange defines the hash key range of a shard.
type HashKeyRange struct {
	StartingHashKey string `json:"StartingHashKey"`
	EndingHashKey   string `json:"EndingHashKey"`
}

// SequenceNumberRange holds sequence numbers for a shard.
type SequenceNumberRange struct {
	StartingSequenceNumber string `json:"StartingSequenceNumber"`
	EndingSequenceNumber   string `json:"EndingSequenceNumber,omitempty"`
}

// Record is a stored Kinesis record.
type Record struct {
	SequenceNumber              string    `json:"SequenceNumber"`
	ApproximateArrivalTimestamp time.Time `json:"ApproximateArrivalTimestamp"`
	Data                        []byte    `json:"Data"`
	PartitionKey                string    `json:"PartitionKey"`
}

// kinesisStore wraps state.Store with Kinesis-specific helpers.
type kinesisStore struct {
	// mu guards nextShardSeqBlock's read-modify-write of the persisted
	// per-shard counter — the same single-mutex-around-a-counter-key
	// pattern as ecsStore.nextRevision (internal/services/ecs/store.go).
	mu            sync.Mutex
	store         state.Store
	defaultRegion string
}

func newKinesisStore(store state.Store, defaultRegion string) *kinesisStore {
	return &kinesisStore{store: store, defaultRegion: defaultRegion}
}

// region extracts the per-request region from context, falling back to the default.
func (s *kinesisStore) region(ctx context.Context) string {
	return middleware.RegionFromContext(ctx, s.defaultRegion)
}

// ---- Stream operations -------------------------------------------------------

func (s *kinesisStore) getStream(ctx context.Context, name string) (*Stream, *protocol.AWSError) {
	raw, found, err := s.store.Get(ctx, nsStreams, serviceutil.RegionKey(s.region(ctx), name))
	if err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	if !found {
		return nil, errNoSuchStream(name)
	}
	var st Stream
	if err := json.Unmarshal([]byte(raw), &st); err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	return &st, nil
}

func (s *kinesisStore) putStream(ctx context.Context, st *Stream) *protocol.AWSError {
	raw, err := json.Marshal(st)
	if err != nil {
		return protocol.Wrap(protocol.ErrInternalError, err)
	}
	if err := s.store.Set(ctx, nsStreams, serviceutil.RegionKey(s.region(ctx), st.StreamName), string(raw)); err != nil {
		return protocol.Wrap(protocol.ErrInternalError, err)
	}
	return nil
}

func (s *kinesisStore) deleteStream(ctx context.Context, name string) *protocol.AWSError {
	// Delete all records for each shard
	region := s.region(ctx)
	keys, err := s.store.List(ctx, nsRecords, serviceutil.RegionKey(region, name+"/"))
	if err == nil {
		for _, k := range keys {
			_ = s.store.Delete(ctx, nsRecords, k)
		}
	}
	if err := s.store.Delete(ctx, nsStreams, serviceutil.RegionKey(region, name)); err != nil {
		return protocol.Wrap(protocol.ErrInternalError, err)
	}
	// Deliberately NOT deleting nsShardSeq counters here. The record
	// deletion loop above ignores per-key errors, so it cannot guarantee
	// every record was actually removed; if a stream with the same name is
	// recreated later, leaving the counter in place guarantees the next
	// PutRecord still allocates a sequence number strictly after anything
	// that survived, instead of resetting to 0 and risking exactly the
	// collision A1 exists to prevent. The cost is that a recreated stream's
	// sequence numbers won't restart at a low value — harmless, since
	// clients only ever compare/sort them, never assume a starting point.
	return nil
}

func (s *kinesisStore) listStreams(ctx context.Context) ([]Stream, *protocol.AWSError) {
	pairs, err := s.store.Scan(ctx, nsStreams, serviceutil.RegionKey(s.region(ctx), ""))
	if err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	streams := make([]Stream, 0, len(pairs))
	for _, p := range pairs {
		var st Stream
		if err := json.Unmarshal([]byte(p.Value), &st); err != nil {
			continue
		}
		streams = append(streams, st)
	}
	sort.Slice(streams, func(i, j int) bool { return streams[i].StreamName < streams[j].StreamName })
	return streams, nil
}

// ---- Record operations -------------------------------------------------------

func recordStoreKey(streamName, shardID, seqNo string) string {
	return streamName + "/" + shardID + "/" + seqNo
}

func (s *kinesisStore) putRecord(ctx context.Context, streamName, shardID string, rec *Record) *protocol.AWSError {
	raw, err := json.Marshal(rec)
	if err != nil {
		return protocol.Wrap(protocol.ErrInternalError, err)
	}
	key := serviceutil.RegionKey(s.region(ctx), recordStoreKey(streamName, shardID, rec.SequenceNumber))
	if err := s.store.Set(ctx, nsRecords, key, string(raw)); err != nil {
		return protocol.Wrap(protocol.ErrInternalError, err)
	}
	return nil
}

// listRecords uses Scan instead of List+per-key Get (storage-plan.md item
// 3.1) — one store round trip instead of N+1.
func (s *kinesisStore) listRecords(ctx context.Context, streamName, shardID string) ([]Record, *protocol.AWSError) {
	prefix := serviceutil.RegionKey(s.region(ctx), streamName+"/"+shardID+"/")
	pairs, err := s.store.Scan(ctx, nsRecords, prefix)
	if err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	records := make([]Record, 0, len(pairs))
	for _, kv := range pairs {
		var rec Record
		if err := json.Unmarshal([]byte(kv.Value), &rec); err != nil {
			// One malformed persisted record must not fail the whole list
			// (CLAUDE.md malformed-persisted-state rule).
			continue
		}
		records = append(records, rec)
	}
	sort.Slice(records, func(i, j int) bool {
		return records[i].SequenceNumber < records[j].SequenceNumber
	})
	return records, nil
}

// listRecordsPage returns up to limit records for shardID whose sequence
// number sorts strictly after afterSeqNo (pass "" for the start of the
// shard), reading via ScanPage's key range instead of a full-shard Scan +
// slice (storage-access-plan.md A2 — the read-side twin of A1). Keys are
// "<region>/<streamName>/<shardId>/<seqNo>" with a fixed-width sortable
// seqNo (see formatSeqNo), so ScanPage's startAfter is just the shard's
// prefix plus the raw seqNo — copies the shape of DynamoDB Streams'
// GetRecords (internal/services/dynamodb/stream_store.go sqlStreamBackend.since).
func (s *kinesisStore) listRecordsPage(ctx context.Context, streamName, shardID, afterSeqNo string, limit int) ([]Record, *protocol.AWSError) {
	prefix := serviceutil.RegionKey(s.region(ctx), streamName+"/"+shardID+"/")
	startAfter := ""
	if afterSeqNo != "" {
		startAfter = prefix + afterSeqNo
	}
	pairs, _, err := s.store.ScanPage(ctx, nsRecords, prefix, startAfter, limit)
	if err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	records := make([]Record, 0, len(pairs))
	for _, kv := range pairs {
		var rec Record
		if err := json.Unmarshal([]byte(kv.Value), &rec); err != nil {
			// One malformed persisted record must not fail the whole page
			// (CLAUDE.md malformed-persisted-state rule).
			continue
		}
		records = append(records, rec)
	}
	// Defensive re-sort: pre-A1 data could have non-contiguous or
	// out-of-order sequence numbers for a shard (the old counter derived
	// from len(records), which regressed after a deletion — see A1).
	// ScanPage already returns rows in key order, so this is a no-op once
	// no pre-A1 data remains. Drop it then (storage-access-plan.md A2).
	sort.Slice(records, func(i, j int) bool {
		return records[i].SequenceNumber < records[j].SequenceNumber
	})
	return records, nil
}

// ---- Sequence counter ---------------------------------------------------------

// nextShardSeqBlock reserves a contiguous block of `count` sequence-number
// slots for shardID and persists the updated counter, returning the first
// (lowest) value in the block. The caller assigns base, base+1, ...,
// base+count-1 (via formatSeqNo) to the count records it is about to write,
// in that order.
//
// This replaces deriving the next sequence number from
// len(listRecords(...)) (storage-access-plan.md A1): that count regresses
// whenever records are removed (retention trim, a stream recreated with
// residual data), so a freshly computed "next" number could collide with a
// sequence number a surviving record still holds — silently overwriting it.
// The counter here is monotonic and independent of how many records
// currently exist for the shard, so it never regresses.
//
// Guarded by s.mu (mirrors ecsStore.nextRevision): Get+increment+Set is not
// atomic on its own against a concurrent caller, and state.Store has no
// compare-and-swap primitive to make it so.
func (s *kinesisStore) nextShardSeqBlock(ctx context.Context, streamName, shardID string, count int) (int64, *protocol.AWSError) {
	if count <= 0 {
		return 0, nil
	}
	key := serviceutil.RegionKey(s.region(ctx), streamName+"/"+shardID)

	s.mu.Lock()
	defer s.mu.Unlock()

	raw, found, err := s.store.Get(ctx, nsShardSeq, key)
	if err != nil {
		return 0, protocol.Wrap(protocol.ErrInternalError, err)
	}
	var base int64
	if found {
		// A corrupt counter value must not crash the write path or wedge
		// the shard (CLAUDE.md malformed-persisted-state rule); treat it
		// as an unset counter instead of failing the request. This method
		// always writes a valid decimal integer, so this only matters if
		// something else corrupted the persisted value.
		if parsed, perr := strconv.ParseInt(raw, 10, 64); perr == nil {
			base = parsed
		}
	}
	next := base + int64(count)
	if err := s.store.Set(ctx, nsShardSeq, key, strconv.FormatInt(next, 10)); err != nil {
		return 0, protocol.Wrap(protocol.ErrInternalError, err)
	}
	return base, nil
}

// ---- Helpers ----------------------------------------------------------------

// buildInitialShards returns evenly distributed shards for a new stream.
// The 128-bit hash space (0 to 2^128-1) is divided equally among shardCount shards.
func buildInitialShards(shardCount int) []Shard {
	maxHash := new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 128), big.NewInt(1))
	shards := make([]Shard, shardCount)
	for i := 0; i < shardCount; i++ {
		start := new(big.Int).Div(new(big.Int).Mul(maxHash, big.NewInt(int64(i))), big.NewInt(int64(shardCount)))
		var end *big.Int
		if i == shardCount-1 {
			end = new(big.Int).Set(maxHash)
		} else {
			end = new(big.Int).Div(new(big.Int).Mul(maxHash, big.NewInt(int64(i+1))), big.NewInt(int64(shardCount)))
			end.Sub(end, big.NewInt(1))
		}
		shards[i] = Shard{
			ShardId: fmt.Sprintf("shardId-%012d", i),
			HashKeyRange: HashKeyRange{
				StartingHashKey: start.String(),
				EndingHashKey:   end.String(),
			},
			SequenceNumberRange: SequenceNumberRange{
				StartingSequenceNumber: fmt.Sprintf("49%019d", i),
			},
		}
	}
	return shards
}

// partitionKeyHashKey returns the 128-bit integer AWS maps a partition key
// to: "An MD5 hash function is used to map partition keys to 128-bit integer
// values and to map associated data records to shards using the hash key
// ranges of the shards" (PutRecord/PutRecords API reference).
func partitionKeyHashKey(partitionKey string) *big.Int {
	sum := md5.Sum([]byte(partitionKey)) //nolint:gosec // reproduces AWS's partition-key hashing, not used for security
	return new(big.Int).SetBytes(sum[:])
}

// pickShard selects the open shard whose HashKeyRange contains hashKey — the
// same placement AWS uses for a record's MD5-hashed partition key, or for an
// ExplicitHashKey supplied directly. A closed shard (one with an
// EndingSequenceNumber, superseded by SplitShard/MergeShards children) is
// never selected. Returns -1 if no open shard's range contains hashKey.
func pickShard(shards []Shard, hashKey *big.Int) int {
	for i, shard := range shards {
		if shard.SequenceNumberRange.EndingSequenceNumber != "" {
			continue
		}
		start, ok1 := new(big.Int).SetString(shard.HashKeyRange.StartingHashKey, 10)
		end, ok2 := new(big.Int).SetString(shard.HashKeyRange.EndingHashKey, 10)
		if !ok1 || !ok2 {
			continue
		}
		if hashKey.Cmp(start) >= 0 && hashKey.Cmp(end) <= 0 {
			return i
		}
	}
	return -1
}

// firstOpenShard returns the index of the first shard with no
// EndingSequenceNumber, or -1 if every shard is closed.
func firstOpenShard(shards []Shard) int {
	for i, shard := range shards {
		if shard.SequenceNumberRange.EndingSequenceNumber == "" {
			return i
		}
	}
	return -1
}

// formatSeqNo renders a Kinesis-style sequence number: fixed-width and
// lexicographically sortable ("49" + 19-digit shard index + 10-digit
// per-shard counter) — unchanged from the pre-A1 wire format. shardIdx is
// stable for the lifetime of a shard (Shards is append-only: see
// buildInitialShards, splitShardTyped, mergeShardsTyped). n is the
// persisted per-shard counter value from nextShardSeqBlock — never derived
// from how many records currently exist for the shard.
func formatSeqNo(shardIdx int, n int64) string {
	return fmt.Sprintf("49%019d%010d", shardIdx, n)
}

func streamARN(accountID, region, name string) string {
	return fmt.Sprintf("arn:aws:kinesis:%s:%s:stream/%s", region, accountID, name)
}

// errNoSuchStream returns an AWS-flavoured error for a missing stream.
//
// 400, not 404. Every exception in the Kinesis model is a client error with
// no httpError override, so the protocol default applies, and each
// operation Errors section states it outright:
// "ResourceNotFoundException ... HTTP Status Code: 400".
func errNoSuchStream(name string) *protocol.AWSError {
	return &protocol.AWSError{
		Code:       "ResourceNotFoundException",
		Message:    fmt.Sprintf("Stream %s under account not found.", name),
		HTTPStatus: http.StatusBadRequest,
	}
}

// errStreamAlreadyExists returns an AWS-flavoured error for a duplicate stream.
func errStreamAlreadyExists(name string) *protocol.AWSError {
	return &protocol.AWSError{
		Code:       "ResourceInUseException",
		Message:    fmt.Sprintf("Stream %s under account already exists.", name),
		HTTPStatus: http.StatusBadRequest,
	}
}

// iteratorKey encodes a shard iterator as a stable, opaque string.
// Format: "<streamName>|<shardId>|<nextSeqNo>".
func encodeShardIterator(streamName, shardID, afterSeqNo string) string {
	return streamName + "|" + shardID + "|" + afterSeqNo
}

func decodeShardIterator(iter string) (streamName, shardID, afterSeqNo string, ok bool) {
	parts := strings.SplitN(iter, "|", 3)
	if len(parts) != 3 {
		return "", "", "", false
	}
	return parts[0], parts[1], parts[2], true
}
