package kinesis

// seq_counter_concurrency_test.go exercises nextShardSeqBlock's
// Get-then-Set critical section (storage-access-plan.md A1) under
// concurrent PutRecord calls. state.Store has no compare-and-swap
// primitive, so correctness here depends entirely on kinesisStore.mu
// serializing the read-modify-write — run with -race to catch a missing or
// misplaced lock.

import (
	"context"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/overcast-sh/overcast/internal/clock"
	"github.com/overcast-sh/overcast/internal/config"
	"github.com/overcast-sh/overcast/internal/serviceutil"
	"github.com/overcast-sh/overcast/internal/state"
)

func TestPutRecord_concurrentCallsProduceUniqueSequenceNumbers(t *testing.T) {
	// Given: a stream with one shard
	ctx := context.Background()
	cfg := &config.Config{AccountID: "000000000000", Region: "us-east-1"}
	clk := clock.New()
	kStore := newKinesisStore(state.NewMemoryStore(), cfg.Region)
	log := serviceutil.NewServiceLogger(zap.NewNop(), serviceName)
	h := newHandler(cfg, kStore, log, clk)

	const streamName = "concurrent-put"
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
		t.Fatalf("putStream: %v", aerr)
	}

	// When: many goroutines call PutRecord concurrently against the same
	// shard.
	const n = 200
	seqNos := make([]string, n)
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			resp, aerr := h.putRecordTyped(ctx, &putRecordRequest{
				StreamName:   streamName,
				Data:         []byte("payload"),
				PartitionKey: "pk",
			})
			if aerr != nil {
				t.Errorf("putRecordTyped: %v", aerr)
				return
			}
			seqNos[i] = resp.SequenceNumber
		}(i)
	}
	wg.Wait()

	// Then: every sequence number is unique — a lost update in
	// nextShardSeqBlock's read-modify-write would hand out the same value
	// to two callers.
	seen := make(map[string]int, n)
	for _, s := range seqNos {
		if s == "" {
			t.Fatal("empty SequenceNumber recorded (see prior t.Errorf for the cause)")
		}
		seen[s]++
	}
	for s, count := range seen {
		if count > 1 {
			t.Fatalf("sequence number %q was assigned %d times, want 1", s, count)
		}
	}
	if len(seen) != n {
		t.Fatalf("got %d unique sequence numbers, want %d", len(seen), n)
	}
}
