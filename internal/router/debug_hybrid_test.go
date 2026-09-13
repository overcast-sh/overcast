//go:build !nosqlite

package router

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/overcast-sh/overcast/internal/config"
	"github.com/overcast-sh/overcast/internal/state"
)

// TestDebugMetrics_hybridStoreReportsFlushAndSeed is the integration-shaped
// test for 3.6: a real *state.HybridStore (not a fake), a real Set+Flush,
// and a real HTTP request through debugMetrics, asserting the response
// carries genuine flush-history and seed-duration data rather than the
// zero-value shape MemoryStore produces. Lives in its own !nosqlite-guarded
// file (mirroring internal/state/hybrid_test.go's convention) since
// *state.HybridStore only exists in real form under that build.
func TestDebugMetrics_hybridStoreReportsFlushAndSeed(t *testing.T) {
	dir := t.TempDir()
	store, err := state.NewHybridStore(dir, 20*time.Millisecond)
	if err != nil {
		t.Fatalf("NewHybridStore: %v", err)
	}
	defer store.Close()

	ctx := context.Background()
	if err := store.WaitReady(ctx); err != nil {
		t.Fatalf("WaitReady: %v", err)
	}
	waitForDebugMetricsSeedDuration(t, ctx, store)

	if err := store.Set(ctx, "sqs:messages", "us-east-1/orders/k1", `{"body":"hello"}`); err != nil {
		t.Fatal(err)
	}
	if err := store.Flush(ctx); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/_overcast/debug/metrics", nil)
	rec := httptest.NewRecorder()
	cfg := &config.Config{State: config.StateBackendHybrid}
	debugMetrics(cfg, store, nil, nil, nil, nil).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp debugMetricsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(resp.Stores) != 1 {
		t.Fatalf("expected exactly one reporting store, got %d: %+v", len(resp.Stores), resp.Stores)
	}
	got := resp.Stores[0]
	if got.Mode != "hybrid" {
		t.Errorf("expected mode %q, got %q", "hybrid", got.Mode)
	}
	if got.SeedDurationMillis == nil {
		t.Error("expected seed duration to be recorded after the background seed completed")
	}
	if len(got.FlushHistory) == 0 {
		t.Error("expected at least one flush history entry after an explicit Flush")
	}
	if got.JournalMode != "wal" {
		t.Errorf("expected live journal mode %q, got %q", "wal", got.JournalMode)
	}
	if got.Degraded {
		t.Error("expected Degraded = false for a healthy hybrid store")
	}
	if resp.Advisories == nil {
		t.Fatal("expected a non-nil (possibly empty) advisories list")
	}
	// Deliberately does NOT assert len(resp.Advisories) == 0: this test runs a
	// real startup fsync probe (runDataDirProbe) against t.TempDir(), and on a
	// heavily loaded machine (many concurrent test/build processes) that probe
	// can legitimately exceed dataDirSlowFsyncThreshold and correctly fire the
	// data-dir-slow-filesystem advisory — that would be an accurate reading of
	// a genuinely slow moment, not a bug, and asserting strict emptiness here
	// would make this test flaky under load for a reason unrelated to what it
	// exists to cover (flush history / seed duration). The "no advisories on a
	// healthy store" behavior itself has full, deterministic coverage in
	// advisories_test.go against fake DebugMetrics input instead. This test
	// only checks for the two advisories that must never fire on an
	// undegraded, correctly-WAL-mode store regardless of disk speed.
	for _, a := range resp.Advisories {
		if a.Code == advisoryCodeJournalModeNotWAL || a.Code == advisoryCodeStoreDegradedMemoryOnly {
			t.Errorf("unexpected advisory %q for a healthy, correctly-WAL-mode hybrid store: %+v", a.Code, a)
		}
	}
}

// TestDebugMetrics_flushHistoryIsBounded proves the flush-history ring
// buffer never grows past its configured cap regardless of how many flushes
// happen over the store's lifetime.
func TestDebugMetrics_flushHistoryIsBounded(t *testing.T) {
	dir := t.TempDir()
	store, err := state.NewHybridStore(dir, 20*time.Millisecond)
	if err != nil {
		t.Fatalf("NewHybridStore: %v", err)
	}
	defer store.Close()

	ctx := context.Background()
	if err := store.WaitReady(ctx); err != nil {
		t.Fatalf("WaitReady: %v", err)
	}

	// More flushes than the documented cap (hybridFlushHistoryCap == 20 in
	// internal/state/hybrid.go) — each Set+Flush pair is one attempted flush
	// (it has pending ops to write), so this exercises the ring buffer's
	// eviction, not just its initial fill.
	const attempts = 30
	for i := 0; i < attempts; i++ {
		if err := store.Set(ctx, "sqs:messages", fmt.Sprintf("us-east-1/orders/k%d", i), `{"body":"hello"}`); err != nil {
			t.Fatal(err)
		}
		if err := store.Flush(ctx); err != nil {
			t.Fatalf("Flush: %v", err)
		}
	}

	snapshots, ok := state.DebugMetricsSnapshot(ctx, store, state.DebugMetricsOptions{})
	if !ok || len(snapshots) != 1 {
		t.Fatalf("expected exactly one DebugMetrics snapshot, got ok=%v len=%d", ok, len(snapshots))
	}
	history := snapshots[0].FlushHistory
	if len(history) == 0 {
		t.Fatal("expected non-empty flush history")
	}
	if len(history) > 20 {
		t.Fatalf("expected flush history capped at 20 entries, got %d", len(history))
	}
}

// waitForDebugMetricsSeedDuration blocks until the store's background
// TierHot seed has finished (observable, via the exported
// state.DebugMetricsSnapshot API, as SeedDurationMillis becoming non-nil —
// see HybridStore.seedFromSQLite). Store.WaitReady only waits for the
// SQLite connection itself, not the full memory seed.
func waitForDebugMetricsSeedDuration(t *testing.T, ctx context.Context, store state.Store) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		snapshots, ok := state.DebugMetricsSnapshot(ctx, store, state.DebugMetricsOptions{})
		if ok && len(snapshots) == 1 && snapshots[0].SeedDurationMillis != nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for hybrid seed duration to be recorded")
		}
		time.Sleep(time.Millisecond)
	}
}
