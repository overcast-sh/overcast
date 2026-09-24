package kinesis

// storage_bench_test.go benchmarks the two hot paths storage-access-plan.md
// A1 and A2 target: PutRecord's cost as the number of already-written
// records in the target shard grows, and GetRecords' cost as shard depth
// grows (storage-plan.md/storage-test-plan.md conventions: allocs/op and
// B/op are the deterministic signal — wall-clock ns/op is noisy on a shared
// machine).
//
// Before A1: every PutRecord ran a full-shard Scan + JSON-decode of every
// existing record just to compute nextSeqNo via len(records), so ns/op and
// allocs/op grew with preExisting (O(n) per put, O(n^2) to fill a shard).
// After A1 (a persisted per-shard counter, see nextShardSeqBlock in
// store.go), both should be flat.
//
// Before A2: every GetRecords ran a full-shard Scan + slice to honor
// Limit/the shard-iterator cursor, so ns/op and allocs/op grew with shard
// depth even though only `Limit` records were ever returned. After A2 (a
// ScanPage range read, see listRecordsPage in store.go), both should be
// flat for the TRIM_HORIZON/resumed-cursor path this benchmark exercises.
//
// The preload writes records (and seeds the persisted counter) via direct
// kinesisStore calls rather than through PutRecord/PutRecords itself, so
// setup cost stays linear regardless of what the write path does — the
// same convention as cloudwatch's metric_burst_bench_test.go.

import (
	"context"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/overcast-sh/overcast/internal/clock"
	"github.com/overcast-sh/overcast/internal/config"
	"github.com/overcast-sh/overcast/internal/serviceutil"
	"github.com/overcast-sh/overcast/internal/state"
)

// newBenchHandler wires a Handler against a fresh in-memory store with a
// single-shard stream named streamName already created, returning the
// handler, a context, and the stream's one shard ID.
func newBenchHandler(b *testing.B, streamName string) (*Handler, context.Context, string) {
	b.Helper()
	ctx := context.Background()
	cfg := &config.Config{AccountID: "000000000000", Region: "us-east-1"}
	clk := clock.New()
	kStore := newKinesisStore(state.NewMemoryStore(), cfg.Region)
	log := serviceutil.NewServiceLogger(zap.NewNop(), serviceName)
	h := newHandler(cfg, kStore, log, clk)

	st := &Stream{
		StreamName:           streamName,
		StreamARN:            streamARN(cfg.AccountID, cfg.Region, streamName),
		StreamStatus:         "ACTIVE",
		ShardCount:           1,
		Shards:               InitialShards(1, time.Time{}),
		Tags:                 map[string]string{},
		CreatedAt:            clk.Now().UTC(),
		RetentionPeriodHours: 24,
	}
	if aerr := kStore.putStream(ctx, st); aerr != nil {
		b.Fatalf("putStream: %v", aerr)
	}
	return h, ctx, st.Shards[0].ShardId
}

// preloadRecords writes n records directly to shardID (bypassing
// putRecordTyped) and seeds the persisted sequence counter to match, so a
// subsequent putRecordTyped/getRecordsTyped call continues exactly where a
// real sequence of n prior PutRecord calls would have left off.
func preloadRecords(b *testing.B, h *Handler, ctx context.Context, streamName, shardID string, n int) {
	b.Helper()
	clk := h.clk
	for i := 0; i < n; i++ {
		rec := &Record{
			SequenceNumber:              formatSeqNo(0, int64(i)),
			ApproximateArrivalTimestamp: clk.Now().UTC(),
			Data:                        []byte("preloaded-record-payload"),
			PartitionKey:                "pk",
		}
		if aerr := h.store.putRecord(ctx, streamName, shardID, rec); aerr != nil {
			b.Fatalf("preload putRecord: %v", aerr)
		}
	}
	if n > 0 {
		if _, aerr := h.store.nextShardSeqBlock(ctx, streamName, shardID, n); aerr != nil {
			b.Fatalf("seed shard counter: %v", aerr)
		}
	}
}

func benchmarkPutRecord(b *testing.B, preExisting int) {
	b.Helper()
	const streamName = "bench-put-record"
	h, ctx, shardID := newBenchHandler(b, streamName)
	preloadRecords(b, h, ctx, streamName, shardID, preExisting)

	req := &putRecordRequest{StreamName: streamName, Data: []byte("payload"), PartitionKey: "pk"}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, aerr := h.putRecordTyped(ctx, req); aerr != nil {
			b.Fatalf("putRecordTyped: %v", aerr)
		}
	}
}

func BenchmarkKinesis_PutRecord_0Existing(b *testing.B) {
	benchmarkPutRecord(b, 0)
}

func BenchmarkKinesis_PutRecord_1000Existing(b *testing.B) {
	benchmarkPutRecord(b, 1000)
}

func BenchmarkKinesis_PutRecord_10000Existing(b *testing.B) {
	benchmarkPutRecord(b, 10000)
}

func benchmarkGetRecords(b *testing.B, shardDepth int) {
	b.Helper()
	const streamName = "bench-get-records"
	h, ctx, shardID := newBenchHandler(b, streamName)
	preloadRecords(b, h, ctx, streamName, shardID, shardDepth)

	iterResp, aerr := h.getShardIteratorTyped(ctx, &getShardIteratorRequest{
		StreamName:        streamName,
		ShardId:           shardID,
		ShardIteratorType: "TRIM_HORIZON",
	})
	if aerr != nil {
		b.Fatalf("getShardIteratorTyped: %v", aerr)
	}
	req := &getRecordsRequest{ShardIterator: iterResp.ShardIterator, Limit: 100}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, aerr := h.getRecordsTyped(ctx, req); aerr != nil {
			b.Fatalf("getRecordsTyped: %v", aerr)
		}
	}
}

func BenchmarkKinesis_GetRecords_0ShardDepth(b *testing.B) {
	benchmarkGetRecords(b, 0)
}

func BenchmarkKinesis_GetRecords_1000ShardDepth(b *testing.B) {
	benchmarkGetRecords(b, 1000)
}

func BenchmarkKinesis_GetRecords_10000ShardDepth(b *testing.B) {
	benchmarkGetRecords(b, 10000)
}
