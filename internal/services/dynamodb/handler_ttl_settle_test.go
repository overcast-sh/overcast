package dynamodb

import (
	"context"
	"testing"
	"time"

	"github.com/overcast-sh/overcast/internal/state"
)

// The settle callback for a TTL transition must only ever settle the
// transition it was armed for. The window closes at the very instant a new
// UpdateTimeToLive becomes acceptable, so the old transition's settle and the
// new update can run at the same moment; before #1868 a settle that had read
// the table just before the update wrote the old specification back over it,
// deadline cleared, and the table a caller had just disabled read ENABLED.
//
// Two guards close that, and each has a test here. The deadline guard is
// deterministic: a settle armed for one deadline finds the table re-armed for
// another and leaves it alone. The lock is exercised by holding it while the
// scheduled settle comes due, which pins that the settle's read-modify-write
// and UpdateTimeToLive's cannot interleave.

func TestSettleTTLTransition_ignoresARecordReArmedForAnotherTransition(t *testing.T) {
	// Given: a table whose enable has settled, and a disable now in flight
	mock := newTTLTestClock()
	svc := newTTLTestService(t, newTTLScanGate(t, state.NewMemoryStore()), mock)
	mustCreateTable(t, svc, "ttl-stale-settle")
	mustUpdateTTL(t, svc, "ttl-stale-settle", true, "expiresAt")
	enableDeadline := ttlDeadlineOf(t, svc, "ttl-stale-settle")
	svc.handler.ttlSched.AdvanceAndSettle(mock, ttlTransitionDuration+time.Second)
	mustUpdateTTL(t, svc, "ttl-stale-settle", false, "expiresAt")
	disableDeadline := ttlDeadlineOf(t, svc, "ttl-stale-settle")
	if disableDeadline == enableDeadline {
		t.Fatalf("test setup: the disable re-used the enable's deadline %d", disableDeadline)
	}

	// When: a settle armed for the enable runs late, after the disable
	// has been accepted
	svc.handler.settleTTLTransition(context.Background(), "ttl-stale-settle", enableDeadline)

	// Then: the disable is untouched — still in flight, attribute intact,
	// deadline as the disable set it
	if got := ttlStatusOf(t, svc, "ttl-stale-settle"); got != ttlStatusDisabling {
		t.Errorf("status after the stale settle = %q, want %q", got, ttlStatusDisabling)
	}
	if got := ttlDeadlineOf(t, svc, "ttl-stale-settle"); got != disableDeadline {
		t.Errorf("TTLTransitionAt after the stale settle = %d, want the disable's %d", got, disableDeadline)
	}

	// And: the disable's own settle still completes it
	svc.handler.ttlSched.AdvanceAndSettle(mock, ttlTransitionDuration+time.Second)
	if got := ttlStatusOf(t, svc, "ttl-stale-settle"); got != ttlStatusDisabled {
		t.Errorf("status after the disable settled = %q, want %q", got, ttlStatusDisabled)
	}
}

func TestSettleTTLTransition_waitsForTheTableLock(t *testing.T) {
	// Given: a table whose enable is in flight
	mock := newTTLTestClock()
	svc := newTTLTestService(t, newTTLScanGate(t, state.NewMemoryStore()), mock)
	mustCreateTable(t, svc, "ttl-locked-settle")
	mustUpdateTTL(t, svc, "ttl-locked-settle", true, "expiresAt")
	deadline := ttlDeadlineOf(t, svc, "ttl-locked-settle")

	// And: the table's TTL lock held as UpdateTimeToLive holds it across its
	// read and its write, with the scheduled settle re-armed through a wrapper
	// the test can watch. The settle still runs on the scheduler's own
	// goroutine when the window closes — that is the shape of the race — and
	// the wrapper only reports where it has got to, which is what this test
	// used to spend a 100 ms sleep and a 5 s completion budget guessing at
	// (#1962).
	//
	// Arming under the lock is deliberate: it leaves the whole window in which
	// the entry could still be replaced — which closes when the callback
	// claims itself, just before `running` — inside a section any concurrent
	// re-arm has to wait for.
	unlock := svc.handler.ttlLocks.Lock(ttlLockKey("us-east-1", "ttl-locked-settle"))
	running, settled := make(chan struct{}), make(chan struct{})
	svc.handler.ttlSched.AfterScoped("us-east-1", "ttl-locked-settle", ttlTransitionKey,
		ttlTransitionDuration, func(ctx context.Context) {
			close(running)
			svc.handler.settleTTLTransition(ctx, "ttl-locked-settle", deadline)
			close(settled)
		})

	// When: the window closes, so the settle fires and finds the lock held. A
	// bare Add is what the scheduler offers here: AdvanceAndSettle waits for
	// the callback to finish, and this one cannot finish until the lock this
	// goroutine holds is released.
	mock.Add(ttlTransitionDuration + time.Second)
	<-running

	// Then: the settle is at the lock and has written nothing. Both checks are
	// deliberately generous — `running` is reported just before the settle
	// takes the lock, so a settle that ignored the lock might not have written
	// yet either — but neither can fail an implementation that does take it,
	// which is what makes them safe to make without a window to wait out.
	select {
	case <-settled:
		t.Fatal("the settle completed while the table's TTL lock was held")
	default:
	}
	if got := ttlDeadlineOf(t, svc, "ttl-locked-settle"); got != deadline {
		t.Fatalf("TTLTransitionAt changed to %d under the lock, want %d untouched", got, deadline)
	}

	// And: it completes once the lock is released. There is no budget on that
	// wait on purpose: the settle is parked on a mutex the line above
	// released, so a deadline here could only be a guess at how long a loaded
	// runner takes to schedule a goroutine — the guess that failed two
	// unrelated pull requests.
	unlock()
	<-settled
	if got := ttlDeadlineOf(t, svc, "ttl-locked-settle"); got != 0 {
		t.Errorf("TTLTransitionAt after the settle = %d, want it cleared", got)
	}
	if got := ttlStatusOf(t, svc, "ttl-locked-settle"); got != ttlStatusEnabled {
		t.Errorf("status after the settle = %q, want %q", got, ttlStatusEnabled)
	}
}

func ttlDeadlineOf(t *testing.T, svc *Service, table string) int64 {
	t.Helper()
	rec, aerr := svc.handler.store.getTable(context.Background(), table)
	if aerr != nil {
		t.Fatalf("getTable(%q): %v", table, aerr)
	}
	return rec.TTLTransitionAt
}
