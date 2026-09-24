package kinesis

// shard_filter.go — ListShards' ShardFilter (#2112).
//
// Per the ShardFilter reference
// (https://docs.aws.amazon.com/kinesis/latest/APIReference/API_ShardFilter.html):
//
//	AFTER_SHARD_ID    all shards whose ID follows ShardId
//	AT_TRIM_HORIZON   the shards that were open at TRIM_HORIZON
//	FROM_TRIM_HORIZON (default) all shards within the retention period
//	AT_LATEST         only the currently open shards
//	AT_TIMESTAMP      shards whose start is <= Timestamp and whose end is
//	                  >= Timestamp or that are still open
//	FROM_TIMESTAMP    closed shards whose end is >= Timestamp, and all open
//	                  shards; a Timestamp before TRIM_HORIZON is corrected to it
//
// Overcast never expires records (see GetRecords' capability note), so no
// shard ever reaches AWS's EXPIRED state and TRIM_HORIZON is always the
// stream's creation: FROM_TRIM_HORIZON is every shard, and AT_TRIM_HORIZON is
// the shards CreateStream made — the ones with no parent.

import (
	"math"
	"time"

	"github.com/overcast-sh/overcast/internal/protocol"
)

const (
	shardFilterAfterShardID    = "AFTER_SHARD_ID"
	shardFilterAtTrimHorizon   = "AT_TRIM_HORIZON"
	shardFilterFromTrimHorizon = "FROM_TRIM_HORIZON"
	shardFilterAtLatest        = "AT_LATEST"
	shardFilterAtTimestamp     = "AT_TIMESTAMP"
	shardFilterFromTimestamp   = "FROM_TIMESTAMP"
)

// shardFilter is ListShards' ShardFilter member. It is also carried inside a
// ListShards NextToken (pageCursor.Filter), because a request that passes a
// NextToken may not repeat it and the next page must still be filtered.
type shardFilter struct {
	Type      string   `json:"Type"`
	ShardId   string   `json:"ShardId,omitempty"`
	Timestamp *float64 `json:"Timestamp,omitempty"`
}

// validate enforces the reference's rules for which properties go with which
// Type. The reference says ShardId "can only be used if the AFTER_SHARD_ID
// shard type is specified" and Timestamp "can only be used if FROM_TIMESTAMP
// or AT_TIMESTAMP shard types are specified"; a property given where it
// cannot be used is refused rather than silently ignored.
func (f *shardFilter) validate() *protocol.AWSError {
	switch f.Type {
	case "":
		return invalidArgument("ShardFilter must specify a Type.")
	case shardFilterAfterShardID:
		if f.ShardId == "" {
			return invalidArgument("ShardId must be supplied in a ShardFilter with a Type of AFTER_SHARD_ID.")
		}
	case shardFilterAtTimestamp, shardFilterFromTimestamp:
		if f.Timestamp == nil {
			return invalidArgument("Timestamp must be supplied in a ShardFilter with a Type of AT_TIMESTAMP or FROM_TIMESTAMP.")
		}
	case shardFilterAtTrimHorizon, shardFilterFromTrimHorizon, shardFilterAtLatest:
	default:
		return invalidArgument("ShardFilter Type " + f.Type + " is not one of AFTER_SHARD_ID, AT_TRIM_HORIZON, FROM_TRIM_HORIZON, AT_LATEST, AT_TIMESTAMP, FROM_TIMESTAMP.")
	}
	if f.ShardId != "" && f.Type != shardFilterAfterShardID {
		return invalidArgument("ShardId can only be used in a ShardFilter with a Type of AFTER_SHARD_ID.")
	}
	if f.Timestamp != nil && f.Type != shardFilterAtTimestamp && f.Type != shardFilterFromTimestamp {
		return invalidArgument("Timestamp can only be used in a ShardFilter with a Type of AT_TIMESTAMP or FROM_TIMESTAMP.")
	}
	return nil
}

// filterShards returns the stream's shards, in shard-ID order, that f admits.
// A nil filter is FROM_TRIM_HORIZON, the documented default.
func filterShards(st *Stream, f *shardFilter) []Shard {
	if f == nil {
		return st.Shards
	}
	var keep func(Shard) bool
	switch f.Type {
	case shardFilterAfterShardID:
		keep = func(s Shard) bool { return s.ShardId > f.ShardId }
	case shardFilterAtTrimHorizon:
		keep = func(s Shard) bool { return s.ParentShardId == "" && s.AdjacentParentShardId == "" }
	case shardFilterAtLatest:
		keep = func(s Shard) bool { return s.isOpen() }
	case shardFilterAtTimestamp:
		at := st.clampToTrimHorizon(epochSecondsToTime(*f.Timestamp))
		keep = func(s Shard) bool {
			return !st.shardOpenedAt(s).After(at) && (s.isOpen() || !st.shardClosedAt(s).Before(at))
		}
	case shardFilterFromTimestamp:
		from := st.clampToTrimHorizon(epochSecondsToTime(*f.Timestamp))
		keep = func(s Shard) bool { return s.isOpen() || !st.shardClosedAt(s).Before(from) }
	default: // FROM_TRIM_HORIZON
		return st.Shards
	}
	out := make([]Shard, 0, len(st.Shards))
	for _, s := range st.Shards {
		if keep(s) {
			out = append(out, s)
		}
	}
	return out
}

// clampToTrimHorizon corrects a timestamp from before the stream's
// TRIM_HORIZON up to it. The reference documents the correction for
// FROM_TIMESTAMP; it is applied to AT_TIMESTAMP as well, because the shards
// "open at" a moment before the stream existed can only sensibly be the ones
// it was created with.
func (st *Stream) clampToTrimHorizon(t time.Time) time.Time {
	if t.Before(st.CreatedAt) {
		return st.CreatedAt
	}
	return t
}

// shardOpenedAt is when a shard was created. A shard persisted before
// Shard.CreatedAt existed reads as created with its stream.
func (st *Stream) shardOpenedAt(s Shard) time.Time {
	if s.CreatedAt.IsZero() {
		return st.CreatedAt
	}
	return s.CreatedAt
}

// shardClosedAt is when a closed shard was closed. A shard closed before
// Shard.ClosedAt existed has no record of when, and reads as closed when its
// stream was created.
func (st *Stream) shardClosedAt(s Shard) time.Time {
	if s.ClosedAt == nil {
		return st.CreatedAt
	}
	return *s.ClosedAt
}

// epochSecondsToTime converts a wire timestamp (epoch seconds, with the
// millisecond precision the reference documents) to a time.Time.
func epochSecondsToTime(seconds float64) time.Time {
	return time.UnixMilli(int64(math.Round(seconds * 1000))).UTC()
}
