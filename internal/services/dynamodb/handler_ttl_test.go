package dynamodb

// handler_ttl_test.go covers the parts of the asynchronous TTL lifecycle that
// have no client-visible trigger: the sweeper's ENABLED gate (the hourly tick
// is far longer than the transition window, so an integration test can never
// catch a sweep mid-ENABLING) and the startup re-arm of a transition that was
// in flight when the process stopped.

import (
	"context"
	"strconv"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/overcast-sh/overcast/internal/clock"
	"github.com/overcast-sh/overcast/internal/config"
	"github.com/overcast-sh/overcast/internal/events"
	"github.com/overcast-sh/overcast/internal/state"
)

// newTTLTestService builds a DynamoDB service on a mock clock wound to a
// fixed date, plus the store it persists tables into so a restart can be
// simulated against the same data.
func newTTLTestService(t *testing.T, store state.Store, mock *clock.Mock) *Service {
	t.Helper()
	cfg := &config.Config{Region: "us-east-1", AccountID: "000000000000"}
	svc := New(cfg, store, zap.NewNop(), mock, events.NewBus())
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		svc.Stop(ctx)
	})
	return svc
}

func newTTLTestClock() *clock.Mock {
	mock := clock.NewMock()
	mock.Set(time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC))
	return mock
}

func mustUpdateTTL(t *testing.T, svc *Service, table string, enabled bool, attribute string) {
	t.Helper()
	_, aerr := svc.handler.updateTimeToLiveTyped(context.Background(), &updateTimeToLiveRequest{
		TableName: table,
		TimeToLiveSpecification: &timeToLiveSpecificationInput{
			Enabled:       &enabled,
			AttributeName: &attribute,
		},
	})
	if aerr != nil {
		t.Fatalf("UpdateTimeToLive(%q, enabled=%v): %v", table, enabled, aerr)
	}
}

func mustPutTTLItem(t *testing.T, svc *Service, table, id string, expiresAt int64) {
	t.Helper()
	_, aerr := svc.handler.putItemTyped(context.Background(), &putItemRequest{
		TableName: table,
		Item: Item{
			"id":  attrValue{"S": id},
			"ttl": attrValue{"N": strconv.FormatInt(expiresAt, 10)},
		},
	})
	if aerr != nil {
		t.Fatalf("PutItem %q: %v", id, aerr)
	}
}

func itemExists(t *testing.T, svc *Service, table, id string) bool {
	t.Helper()
	resp, aerr := svc.handler.getItemTyped(context.Background(), &getItemRequest{
		TableName: table,
		Key:       Item{"id": attrValue{"S": id}},
	})
	if aerr != nil {
		t.Fatalf("GetItem %q: %v", id, aerr)
	}
	return resp.Item != nil
}

// TestSweepExpiredItems_onlyExpiresWhileEnabled pins that the sweeper leaves
// items alone while the enable is still processing. AWS only starts expiring
// items once TimeToLiveStatus reaches ENABLED, so a sweep that fired during
// ENABLING would delete data the table is not yet expiring.
func TestSweepExpiredItems_onlyExpiresWhileEnabled(t *testing.T) {
	// Given: a table whose TTL enable is still in flight, holding an item
	// whose expiry is already in the past
	mock := newTTLTestClock()
	svc := newTTLTestService(t, state.NewMemoryStore(), mock)
	mustCreateTable(t, svc, "sweep-gate")
	mustUpdateTTL(t, svc, "sweep-gate", true, "ttl")
	mustPutTTLItem(t, svc, "sweep-gate", "expired", mock.Now().Add(-time.Hour).Unix())

	// When: a sweep runs while the table is still ENABLING
	svc.handler.sweepExpiredItems(context.Background())

	// Then: the item survives
	if !itemExists(t, svc, "sweep-gate", "expired") {
		t.Fatal("sweeper expired an item while TimeToLiveStatus was ENABLING")
	}

	// And: once the transition settles to ENABLED, the same sweep deletes it
	mock.Add(ttlTransitionDuration + time.Second)
	svc.handler.sweepExpiredItems(context.Background())
	if itemExists(t, svc, "sweep-gate", "expired") {
		t.Fatal("sweeper did not expire an item while TimeToLiveStatus was ENABLED")
	}
}

// TestSweepExpiredItems_stopsExpiringWhileDisabling pins the other half of the
// gate: a disable that is still processing has already stopped expiry.
func TestSweepExpiredItems_stopsExpiringWhileDisabling(t *testing.T) {
	// Given: a table whose TTL is ENABLED, then disabled
	mock := newTTLTestClock()
	svc := newTTLTestService(t, state.NewMemoryStore(), mock)
	mustCreateTable(t, svc, "sweep-disabling")
	mustUpdateTTL(t, svc, "sweep-disabling", true, "ttl")
	mock.Add(ttlTransitionDuration + time.Second)
	mustUpdateTTL(t, svc, "sweep-disabling", false, "ttl")
	mustPutTTLItem(t, svc, "sweep-disabling", "expired", mock.Now().Add(-time.Hour).Unix())

	// When: a sweep runs while the table is DISABLING
	svc.handler.sweepExpiredItems(context.Background())

	// Then: the item survives
	if !itemExists(t, svc, "sweep-disabling", "expired") {
		t.Fatal("sweeper expired an item while TimeToLiveStatus was DISABLING")
	}
}

// TestRearmTTLTransitions_completesATransitionInterruptedByRestart covers the
// two restart cases: a window that elapsed while the process was down settles
// on startup, and one still open is re-armed for its remaining time.
func TestRearmTTLTransitions_completesATransitionInterruptedByRestart(t *testing.T) {
	// Given: a store holding a table whose TTL enable is still in flight
	store := state.NewMemoryStore()
	mock := newTTLTestClock()
	first := newTTLTestService(t, store, mock)
	mustCreateTable(t, first, "ttl-restart")
	mustUpdateTTL(t, first, "ttl-restart", true, "expiresAt")
	if got := ttlStatusOf(t, first, "ttl-restart"); got != ttlStatusEnabling {
		t.Fatalf("test setup: status = %q, want %q", got, ttlStatusEnabling)
	}

	// When: the process restarts after the window has elapsed
	mock.Add(ttlTransitionDuration + time.Second)
	second := newTTLTestService(t, store, mock)
	second.handler.rearmTTLTransitions(context.Background())

	// Then: the transition has completed and left no pending marker behind
	if got := ttlStatusOf(t, second, "ttl-restart"); got != ttlStatusEnabled {
		t.Errorf("status after restart = %q, want %q", got, ttlStatusEnabled)
	}
	table, aerr := second.handler.store.getTable(context.Background(), "ttl-restart")
	if aerr != nil {
		t.Fatalf("getTable: %v", aerr)
	}
	if table.TTLTransitionAt != 0 {
		t.Errorf("TTLTransitionAt = %d, want it cleared by the settle", table.TTLTransitionAt)
	}
}

func TestRearmTTLTransitions_reArmsATransitionStillInFlight(t *testing.T) {
	// Given: a store holding a table whose TTL disable is still in flight
	store := state.NewMemoryStore()
	mock := newTTLTestClock()
	first := newTTLTestService(t, store, mock)
	mustCreateTable(t, first, "ttl-rearm")
	mustUpdateTTL(t, first, "ttl-rearm", true, "expiresAt")
	mock.Add(ttlTransitionDuration + time.Second)
	mustUpdateTTL(t, first, "ttl-rearm", false, "expiresAt")

	// When: the process restarts while the window is still open
	second := newTTLTestService(t, store, mock)
	second.handler.rearmTTLTransitions(context.Background())

	// Then: the table is still DISABLING, and settles when the window closes
	if got := ttlStatusOf(t, second, "ttl-rearm"); got != ttlStatusDisabling {
		t.Fatalf("status after restart = %q, want %q", got, ttlStatusDisabling)
	}
	second.handler.ttlSched.AdvanceAndSettle(mock, ttlTransitionDuration+time.Second)
	table, aerr := second.handler.store.getTable(context.Background(), "ttl-rearm")
	if aerr != nil {
		t.Fatalf("getTable: %v", aerr)
	}
	if table.TTL != nil || table.TTLTransitionAt != 0 {
		t.Errorf("record after settle = %+v, want the TTL configuration dropped", table)
	}
	if got := ttlStatusOf(t, second, "ttl-rearm"); got != ttlStatusDisabled {
		t.Errorf("status after settle = %q, want %q", got, ttlStatusDisabled)
	}
}

// TestRearmTTLTransitions_armsTheTransitionTheRecordCarries pins that a
// re-arm cannot cancel a transition armed after its own scan.
//
// The scheduler keys one pending transition per table, so a re-arm that arms
// what its snapshot said replaces whatever the table has now — and an entry
// replaced between its callback firing and that callback claiming it stands
// down without running. The transition it belonged to then never settles.
// That is #1962: on CI, a startup re-arm whose scan had been descheduled long
// enough to go stale cancelled the settle a test had just armed, and the test
// spent its whole completion budget waiting for a callback that had already
// been told to stand down.
//
// The gate holds the window open on purpose, so what CI hit by chance is a
// sequence written down here.
func TestRearmTTLTransitions_armsTheTransitionTheRecordCarries(t *testing.T) {
	// Given: a table whose TTL enable is in flight
	mock := newTTLTestClock()
	svc := newTTLTestService(t, newTTLScanGate(t, state.NewMemoryStore()), mock)
	mustCreateTable(t, svc, "ttl-rearm-race")
	mustUpdateTTL(t, svc, "ttl-rearm-race", true, "expiresAt")

	// When: a re-arm runs, and between its scan and its arming the table is
	// re-armed for a later deadline — the tail of an UpdateTimeToLive that was
	// accepted in that window, which is the record write and the arming that
	// updateTimeToLiveTyped does under the table's TTL lock
	rearmed := mock.Now().Add(2 * ttlTransitionDuration).UnixNano()
	ctx := withTTLScanGate(context.Background(), func() {
		table, aerr := svc.handler.store.getTable(context.Background(), "ttl-rearm-race")
		if aerr != nil {
			t.Errorf("getTable in the scan window: %v", aerr)
			return
		}
		table.TTLTransitionAt = rearmed
		if aerr := svc.handler.store.putTable(context.Background(), table); aerr != nil {
			t.Errorf("putTable in the scan window: %v", aerr)
			return
		}
		svc.handler.scheduleTTLTransition("us-east-1", table.TableName, 2*ttlTransitionDuration, rearmed)
	})
	svc.handler.rearmTTLTransitions(ctx)

	// Then: the transition the record carries is the one that is armed, so it
	// settles when its own window closes
	svc.handler.ttlSched.AdvanceAndSettle(mock, 2*ttlTransitionDuration+time.Second)
	table, aerr := svc.handler.store.getTable(context.Background(), "ttl-rearm-race")
	if aerr != nil {
		t.Fatalf("getTable: %v", aerr)
	}
	if table.TTLTransitionAt != 0 {
		t.Errorf("TTLTransitionAt = %d, want 0 — the settle for the re-armed deadline never ran",
			table.TTLTransitionAt)
	}
	if got := ttlStatusOf(t, svc, "ttl-rearm-race"); got != ttlStatusEnabled {
		t.Errorf("status after the settle = %q, want %q", got, ttlStatusEnabled)
	}
}

// ttlScanGateKey carries a test's callback on the context it scans with.
type ttlScanGateKey struct{}

func withTTLScanGate(ctx context.Context, fn func()) context.Context {
	return context.WithValue(ctx, ttlScanGateKey{}, fn)
}

// ttlScanGate is a state.Store that gives a test both halves of the control it
// needs over the whole-namespace table scan — the read behind
// rearmTTLTransitions and sweepExpiredItems, and the only one that passes an
// empty prefix, so ListTables is never affected.
//
// A caller carrying a gate callback has it run between the snapshot being
// taken and the snapshot coming back: that is the window a re-arm goes stale
// in, held open with no sleep and no second goroutine.
//
// Any other caller — in practice the sweeper's own start-up re-arm, which
// runs on a context of its own at a moment no test controls — waits at the
// door until release is closed. Without that, whether the sweeper's scan lands
// before or after the test's writes decides whether it re-arms the transition
// too, which is precisely the nondeterminism these tests are here to remove.
type ttlScanGate struct {
	state.Store
	release chan struct{}
}

func newTTLScanGate(t *testing.T, store state.Store) *ttlScanGate {
	t.Helper()
	g := &ttlScanGate{Store: store, release: make(chan struct{})}
	t.Cleanup(func() { close(g.release) })
	return g
}

func (g *ttlScanGate) Scan(ctx context.Context, namespace, prefix string) ([]state.KV, error) {
	if namespace != nsTables || prefix != "" {
		return g.Store.Scan(ctx, namespace, prefix)
	}
	fn, gated := ctx.Value(ttlScanGateKey{}).(func())
	if !gated {
		<-g.release
		return g.Store.Scan(ctx, namespace, prefix)
	}
	out, err := g.Store.Scan(ctx, namespace, prefix)
	fn()
	return out, err
}

func ttlStatusOf(t *testing.T, svc *Service, table string) string {
	t.Helper()
	resp, aerr := svc.handler.describeTimeToLiveTyped(context.Background(), &describeTimeToLiveRequest{TableName: table})
	if aerr != nil {
		t.Fatalf("DescribeTimeToLive %q: %v", table, aerr)
	}
	return resp.TimeToLiveDescription.TimeToLiveStatus
}
