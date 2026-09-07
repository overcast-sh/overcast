package debugger

import (
	"context"
	"sync"
	"time"

	"github.com/overcast-sh/overcast/internal/clock"
	"github.com/overcast-sh/overcast/internal/config"
)

// WithDeadline bounds ctx by timeout of *running* time: time during which the
// policy says the target is suspended — a client attached, or the program
// paused — does not count. Deadline() still reports the nominal deadline,
// start + timeout, because the Lambda Runtime API derives the deadline header
// it sends once from it, and that header must stay what AWS would send.
//
// With no target, a target that is not listening, or the strict policy, it
// is exactly context.WithTimeout: nothing here runs, and the invoke hot path
// for an undebugged function gains only the nil check.
func WithDeadline(ctx context.Context, clk clock.Clock, timeout time.Duration, t *Target, policy TimeoutPolicy) (context.Context, context.CancelFunc) {
	if t == nil || policy == config.DebuggerTimeoutStrict || !t.Bound() {
		return context.WithTimeout(ctx, timeout)
	}
	// paused needs the protocol to say when it is paused; otherwise the
	// attached policy is the closest thing that is still true.
	if policy == config.DebuggerTimeoutPaused {
		if _, ok := t.res.Protocol.(PauseObserver); !ok {
			policy = config.DebuggerTimeoutAttached
		}
	}

	now := clk.Now()
	c := &suspendCtx{
		parent:    ctx,
		clk:       clk,
		nominal:   now.Add(timeout),
		done:      make(chan struct{}),
		remaining: timeout,
		suspended: true, // the seed below starts the clock if the target is idle
	}
	// The starting state and the subscription are taken together under the
	// target's transition lock, so the clock starts from exactly the state the
	// first event follows — never from a read a transition already overtook.
	unsubscribe := t.subscribe(func(attached, paused bool) {
		if policy == config.DebuggerTimeoutPaused {
			c.setSuspended(paused)
			return
		}
		c.setSuspended(attached)
	}, func(ev Event) {
		switch ev.Kind {
		case EventAttach, EventDetach:
			if policy == config.DebuggerTimeoutAttached {
				c.setSuspended(ev.Kind == EventAttach)
			}
		case EventPause, EventResume:
			if policy == config.DebuggerTimeoutPaused {
				c.setSuspended(ev.Kind == EventPause)
			}
		}
	})
	stopParent := context.AfterFunc(ctx, func() { c.finish(ctx.Err()) })
	// The budget may already have run out (a zero timeout) or the parent may
	// already be done; the cleanups are handed over under the lock, and run
	// here if the context finished before they arrived.
	c.mu.Lock()
	c.unsubscribe, c.stopParent = unsubscribe, stopParent
	finished := c.err != nil
	c.mu.Unlock()
	if finished {
		unsubscribe()
		stopParent()
		return c, func() {}
	}
	return c, func() { c.finish(context.Canceled) }
}

// suspendCtx is a context whose timer can be stopped and restarted. It has
// its own Done channel rather than wrapping a cancelCtx so that a child
// context derived from it reports its Err — DeadlineExceeded when the budget
// ran out — which is what the invoke path checks to tell a timeout from a
// cancellation.
type suspendCtx struct {
	parent  context.Context
	clk     clock.Clock
	nominal time.Time
	done    chan struct{}

	mu          sync.Mutex
	err         error
	remaining   time.Duration // budget left, valid while suspended
	runStart    time.Time     // when the budget last started draining
	suspended   bool
	timer       *clock.Timer
	generation  int // identifies the live timer; a stale fire is ignored
	unsubscribe func()
	stopParent  func() bool
}

func (c *suspendCtx) Deadline() (time.Time, bool) { return c.nominal, true }
func (c *suspendCtx) Done() <-chan struct{}       { return c.done }
func (c *suspendCtx) Value(key any) any           { return c.parent.Value(key) }

func (c *suspendCtx) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.err
}

// setSuspended stops the clock or restarts it. Stopping records the budget
// left; restarting arms a fresh timer for exactly that, or fires at once
// when nothing is left — a client that detaches after the budget ran out
// during an earlier running stretch is a timeout, not a reprieve.
func (c *suspendCtx) setSuspended(suspended bool) {
	c.mu.Lock()
	if c.err != nil || c.suspended == suspended {
		c.mu.Unlock()
		return
	}
	c.suspended = suspended
	now := c.clk.Now()
	if suspended {
		c.remaining -= now.Sub(c.runStart)
		if c.timer != nil {
			c.timer.Stop()
			c.timer = nil
		}
		c.mu.Unlock()
		return
	}
	if c.remaining <= 0 {
		c.finishLocked(context.DeadlineExceeded)
		return
	}
	c.runStart = now
	c.generation++
	generation := c.generation
	c.timer = c.clk.AfterFunc(c.remaining, func() { c.fire(generation) })
	c.mu.Unlock()
}

func (c *suspendCtx) fire(generation int) {
	c.mu.Lock()
	if c.err != nil || c.suspended || generation != c.generation {
		c.mu.Unlock()
		return
	}
	c.finishLocked(context.DeadlineExceeded)
}

func (c *suspendCtx) finish(err error) {
	c.mu.Lock()
	if c.err != nil {
		c.mu.Unlock()
		return
	}
	c.finishLocked(err)
}

// finishLocked ends the context. It is entered with mu held and releases it
// before unsubscribing, since a subscriber callback may be the caller.
func (c *suspendCtx) finishLocked(err error) {
	c.err = err
	if c.timer != nil {
		c.timer.Stop()
		c.timer = nil
	}
	close(c.done)
	unsubscribe, stopParent := c.unsubscribe, c.stopParent
	c.mu.Unlock()
	if unsubscribe != nil {
		unsubscribe()
	}
	if stopParent != nil {
		stopParent()
	}
}
