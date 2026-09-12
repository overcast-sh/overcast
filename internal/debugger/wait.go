package debugger

import (
	"context"
	"time"

	"github.com/overcast-sh/overcast/internal/config"
)

// SettleAfterAttach is how long an invocation held for a client keeps waiting
// after one attaches, so the client's first messages — enabling the debugger
// domain, setting its breakpoints — reach the runtime before the event does.
// A client that has been attached longer than this when the invocation
// arrives is not waited on at all.
const SettleAfterAttach = 750 * time.Millisecond

// WaitResult says how AwaitClient ended.
type WaitResult string

const (
	// WaitSkipped is an invocation that was never held: the target does not
	// ask for it (no TagWait, not bound, or the strict policy), or a client
	// was already attached for longer than the settle.
	WaitSkipped WaitResult = "skipped"
	// WaitAttached is a client attaching while held, plus the settle.
	WaitAttached WaitResult = "attached"
	// WaitExpired is the timeout passing with nobody attached. The caller
	// proceeds as if untagged, and says so.
	WaitExpired WaitResult = "expired"
	// WaitCancelled is the caller's context ending during the hold.
	WaitCancelled WaitResult = "cancelled"
)

// AwaitClient holds the caller until a client is attached to the target, for
// an invocation whose TagWait asks for it: an editor or the console attaching
// on the cold start then sees its breakpoints bind before the handler runs
// (docs/plans/compute-debugger-console.md § 6). It returns at once, WaitSkipped,
// when there is nothing to wait for; otherwise it waits for the attach event
// — subscribed, not polled — then for SettleAfterAttach measured from the
// moment of the attach, whether that attach happened during the hold or
// shortly before it.
//
// The hold is bounded by timeout, after which the invocation proceeds
// undebugged (WaitExpired), and ends with ctx (WaitCancelled): a caller that
// disconnects releases the environment it was holding. Under the strict
// timeout policy nothing may hold an invocation, so the tag is ignored —
// said once when the target is registered, not here.
//
// The invocation clock is not running during the hold: a caller bounds its
// invocation only after AwaitClient returns, so the budget starts, and the
// nominal deadline the Runtime API reports is computed, at dispatch.
func (t *Target) AwaitClient(ctx context.Context, timeout time.Duration) WaitResult {
	if t == nil || !t.Bound() || !t.Wait() || t.policy == config.DebuggerTimeoutStrict {
		return WaitSkipped
	}

	// The seed and the subscription are taken together under the transition
	// lock, so an attach between "not attached" and "subscribed" is seen
	// exactly once, as the seed or as the event — never missed.
	attached := make(chan time.Time, 1)
	unsubscribe := t.subscribe(func(isAttached, _ bool) {
		if isAttached {
			t.mu.Lock()
			since := t.attachedSince
			t.mu.Unlock()
			attached <- since
		}
	}, func(ev Event) {
		if ev.Kind == EventAttach {
			select {
			case attached <- ev.At:
			default:
			}
		}
	})
	defer unsubscribe()

	var at time.Time
	select {
	case at = <-attached:
	default:
		expiry := t.clk.Timer(timeout)
		defer expiry.Stop()
		select {
		case at = <-attached:
		case <-expiry.C:
			return WaitExpired
		case <-ctx.Done():
			return WaitCancelled
		}
	}

	settle := SettleAfterAttach - t.clk.Now().Sub(at)
	if settle <= 0 {
		// Attached long enough already: nothing was waited for.
		return WaitSkipped
	}
	settled := t.clk.Timer(settle)
	defer settled.Stop()
	select {
	case <-settled.C:
		return WaitAttached
	case <-ctx.Done():
		return WaitCancelled
	}
}
