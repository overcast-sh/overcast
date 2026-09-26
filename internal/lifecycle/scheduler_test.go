package lifecycle

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/overcast-sh/overcast/internal/clock"
)

func TestScheduler_After_fires(t *testing.T) {
	// Given a scheduler with a mock clock
	mock := clock.NewMock()
	s := NewScheduler(mock)

	// When we schedule a callback after 1s
	var fired atomic.Bool
	s.After("test-key", 1*time.Second, func() {
		fired.Store(true)
	})

	// Then the callback has not fired yet
	if fired.Load() {
		t.Fatal("expected callback not to fire immediately")
	}
	if s.PendingCount() != 1 {
		t.Fatalf("expected 1 pending, got %d", s.PendingCount())
	}

	// When we advance the clock past the delay
	mock.Add(1*time.Second + time.Millisecond)

	// Then the callback fires (give goroutine a moment)
	time.Sleep(10 * time.Millisecond)
	if !fired.Load() {
		t.Fatal("expected callback to fire after delay")
	}
	if s.PendingCount() != 0 {
		t.Fatalf("expected 0 pending, got %d", s.PendingCount())
	}
}

func TestScheduler_Cancel(t *testing.T) {
	// Given a scheduler with a pending transition
	mock := clock.NewMock()
	s := NewScheduler(mock)

	var fired atomic.Bool
	s.After("cancel-me", 5*time.Second, func() {
		fired.Store(true)
	})

	// When we cancel it
	ok := s.Cancel("cancel-me")
	if !ok {
		t.Fatal("expected Cancel to return true")
	}

	// Then advancing the clock does not fire it
	mock.Add(10 * time.Second)
	time.Sleep(10 * time.Millisecond)
	if fired.Load() {
		t.Fatal("expected cancelled callback not to fire")
	}
	if s.PendingCount() != 0 {
		t.Fatalf("expected 0 pending, got %d", s.PendingCount())
	}
}

func TestScheduler_Cancel_notFound(t *testing.T) {
	mock := clock.NewMock()
	s := NewScheduler(mock)

	if s.Cancel("nonexistent") {
		t.Fatal("expected Cancel to return false for nonexistent key")
	}
}

func TestScheduler_After_replaces(t *testing.T) {
	// Given a scheduler with a pending transition
	mock := clock.NewMock()
	s := NewScheduler(mock)

	var firstFired, secondFired atomic.Bool
	s.After("same-key", 5*time.Second, func() {
		firstFired.Store(true)
	})

	// When we schedule a new one with the same key
	s.After("same-key", 3*time.Second, func() {
		secondFired.Store(true)
	})

	// Then only one is pending
	if s.PendingCount() != 1 {
		t.Fatalf("expected 1 pending, got %d", s.PendingCount())
	}

	// When we advance past the new delay
	mock.Add(3*time.Second + time.Millisecond)
	time.Sleep(10 * time.Millisecond)

	// Then only the second callback fires
	if firstFired.Load() {
		t.Fatal("expected first callback to be cancelled")
	}
	if !secondFired.Load() {
		t.Fatal("expected second callback to fire")
	}
}

func TestScheduler_Stop(t *testing.T) {
	// Given a scheduler with multiple pending transitions
	mock := clock.NewMock()
	s := NewScheduler(mock)

	var count atomic.Int32
	s.After("a", 1*time.Second, func() { count.Add(1) })
	s.After("b", 2*time.Second, func() { count.Add(1) })
	s.After("c", 3*time.Second, func() { count.Add(1) })

	// When we stop the scheduler
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	s.Stop(ctx)

	// Then no callbacks fire even after advancing
	mock.Add(10 * time.Second)
	time.Sleep(10 * time.Millisecond)
	if count.Load() != 0 {
		t.Fatalf("expected 0 callbacks to fire, got %d", count.Load())
	}
	if s.PendingCount() != 0 {
		t.Fatalf("expected 0 pending, got %d", s.PendingCount())
	}
}

func TestScheduler_After_replace_does_not_hang_Stop(t *testing.T) {
	// Given a scheduler with a pending transition
	mock := clock.NewMock()
	s := NewScheduler(mock)

	s.After("x", 5*time.Second, func() {})

	// When we replace that key with a new transition
	s.After("x", 3*time.Second, func() {})

	// Then Stop completes without hanging (the replaced entry's WaitGroup is balanced)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	s.Stop(ctx)

	if ctx.Err() != nil {
		t.Fatal("Stop() hung — WaitGroup imbalance when replacing a pending transition")
	}
}

func TestScheduler_AdvanceAndSettle_waitsForTheCallbackToFinish(t *testing.T) {
	// Given a transition whose callback takes longer than the millisecond a
	// bare mock.Add yields for
	mock := clock.NewMock()
	s := NewScheduler(mock)

	var fired atomic.Bool
	s.After("slow", 1*time.Second, func() {
		time.Sleep(50 * time.Millisecond)
		fired.Store(true)
	})

	// When we advance the clock through it and settle
	s.AdvanceAndSettle(mock, 1*time.Second)

	// Then the callback has finished, not merely started
	if !fired.Load() {
		t.Fatal("AdvanceAndSettle returned while the callback was still running")
	}
	if s.PendingCount() != 0 {
		t.Fatalf("expected 0 pending, got %d", s.PendingCount())
	}
}

func TestScheduler_AdvanceAndSettle_transitionNotYetDue(t *testing.T) {
	// Given one transition inside the advance and one well beyond it
	mock := clock.NewMock()
	s := NewScheduler(mock)

	var due, later atomic.Bool
	s.After("due", 1*time.Second, func() { due.Store(true) })
	s.After("later", 1*time.Hour, func() { later.Store(true) })

	// When we advance past the first only
	s.AdvanceAndSettle(mock, 2*time.Second)

	// Then it settled the one that came due and did not wait for the other
	if !due.Load() {
		t.Fatal("expected the due transition to have run")
	}
	if later.Load() {
		t.Fatal("expected the later transition not to have run")
	}
	if s.PendingCount() != 1 {
		t.Fatalf("expected 1 pending, got %d", s.PendingCount())
	}
}

func TestScheduler_Settle_realClock(t *testing.T) {
	// Given a transition on a real clock
	s := NewScheduler(clock.New())

	var fired atomic.Bool
	s.After("real", 10*time.Millisecond, func() { fired.Store(true) })

	// When we settle, which waits the delay out
	s.Settle()

	// Then the callback has run
	if !fired.Load() {
		t.Fatal("Settle returned before the transition ran")
	}
}

func TestScheduler_Settle_callbackAlreadyRunning(t *testing.T) {
	// Given a transition that has already fired and is still in its callback —
	// so it has left the pending map, which is where a settle looks
	s := NewScheduler(clock.New())

	started := make(chan struct{})
	var finished atomic.Bool
	s.After("inflight", time.Millisecond, func() {
		close(started)
		time.Sleep(100 * time.Millisecond)
		finished.Store(true)
	})
	<-started

	// When we settle
	s.Settle()

	// Then it waited for the callback in flight rather than finding nothing
	// pending and returning straight away
	if !finished.Load() {
		t.Fatal("Settle returned while a fired callback was still running")
	}
}

func TestScheduler_Settle_transitionCancelled(t *testing.T) {
	// Given a waiter on a transition that is never going to come due
	mock := clock.NewMock()
	s := NewScheduler(mock)
	s.After("never", 1*time.Hour, func() {})

	settled := make(chan struct{})
	go func() {
		s.Settle()
		close(settled)
	}()

	// When the scheduler is stopped, cancelling it
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	s.Stop(ctx)

	// Then the waiter is released rather than waiting for a callback that will
	// never run
	select {
	case <-settled:
	case <-time.After(5 * time.Second):
		t.Fatal("Settle did not return after the transition was cancelled")
	}
}

// TestScheduler_StopRacesSelfReschedulingAfter reproduces the shape of the
// race from issue #1282: readiness.Watch's health-check loop reschedules
// itself from inside its own callback (the callback calls Scheduler.After
// again, for the next probe attempt) for as long as its probe keeps
// answering Waiting — here modelled directly, since nothing bounds how long
// that can go on for. Against the unfixed scheduler this hangs: Stop's
// wg.Wait has nothing to fence the chain, so a callback can always land one
// more After — including, on an unlucky interleaving, one whose wg.Add races
// Stop's wg.Wait exactly as reported (scheduler.go:81 vs :211 at the time).
// Fixed, After after Stop is a no-op, so the chain breaks on the first After
// call that lands once Stop's critical section has run, and Stop returns
// promptly. Run under `go test -race -count=20`.
func TestScheduler_StopRacesSelfReschedulingAfter(t *testing.T) {
	s := NewScheduler(clock.New())

	var hammer func()
	hammer = func() {
		s.After("hammer", time.Microsecond, func() {
			hammer()
		})
	}
	hammer()

	// Give the chain a moment to actually be alternating between an in-flight
	// callback and a freshly scheduled one — the window Stop must race
	// against — before racing Stop against it.
	time.Sleep(time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	s.Stop(ctx)

	if ctx.Err() != nil {
		t.Fatal("Stop() did not complete before its deadline — a callback kept rescheduling past Stop")
	}
	if s.PendingCount() != 0 {
		t.Fatalf("expected 0 pending after Stop, got %d", s.PendingCount())
	}
}

// TestScheduler_After_isANoOpAfterStop is the deterministic half of the
// #1282 fix: DoD requires that After after Stop be a defined no-op rather
// than touching torn-down state. This nails that contract down directly,
// with no reliance on winning a race window — After must neither run fn
// inline (the 0-delay fast path) nor ever schedule it.
func TestScheduler_After_isANoOpAfterStop(t *testing.T) {
	mock := clock.NewMock()
	s := NewScheduler(mock)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	s.Stop(ctx)

	var ran atomic.Bool
	s.After("late", 0, func() { ran.Store(true) })
	if ran.Load() {
		t.Fatal("After ran its callback inline after Stop — the 0-delay fast path must be a no-op too")
	}

	s.After("late-delayed", time.Hour, func() { ran.Store(true) })
	if s.PendingCount() != 0 {
		t.Fatalf("PendingCount = %d after a post-Stop After, want 0 — it must not schedule", s.PendingCount())
	}

	mock.Add(2 * time.Hour)
	time.Sleep(10 * time.Millisecond)
	if ran.Load() {
		t.Fatal("a transition scheduled after Stop ran anyway")
	}
}

func TestScheduler_multipleKeys(t *testing.T) {
	mock := clock.NewMock()
	s := NewScheduler(mock)

	var a, b atomic.Bool
	s.After("key-a", 1*time.Second, func() { a.Store(true) })
	s.After("key-b", 2*time.Second, func() { b.Store(true) })

	if s.PendingCount() != 2 {
		t.Fatalf("expected 2 pending, got %d", s.PendingCount())
	}

	// Advance past first but not second
	mock.Add(1*time.Second + time.Millisecond)
	time.Sleep(10 * time.Millisecond)

	if !a.Load() {
		t.Fatal("expected key-a to fire")
	}
	if b.Load() {
		t.Fatal("expected key-b not to fire yet")
	}
	if s.PendingCount() != 1 {
		t.Fatalf("expected 1 pending, got %d", s.PendingCount())
	}

	// Advance past second
	mock.Add(1*time.Second + time.Millisecond)
	time.Sleep(10 * time.Millisecond)

	if !b.Load() {
		t.Fatal("expected key-b to fire")
	}
	if s.PendingCount() != 0 {
		t.Fatalf("expected 0 pending, got %d", s.PendingCount())
	}
}

// fireRace drives one scheduler key as hard as a mock clock allows: the test
// advances the clock through a stream of transitions on that key while a
// contending goroutine keeps arming it — and, depending on the test, cancels
// it, re-arms it, or stops the scheduler underneath it.
//
// The window all three aim at is the one the mock clock opens between choosing
// a timer and running it: Mock.runNextTimer picks the next timer, releases the
// clock's lock, and only then calls Tick, which re-takes that lock before it
// launches the callback goroutine. A Timer.Stop landing in between reports
// true — the timer is still registered and not yet marked stopped — for a timer
// that is nevertheless about to run its function. Every cancelling path in the
// Scheduler calls Timer.Stop while holding s.mu, so a contending goroutine
// waiting on the clock's lock is handed it exactly when that window opens.
type fireRace struct {
	s    *Scheduler
	mock *clock.Mock
	// witnessRan is closed by an uncontended transition halfway through the
	// advance, so a test can say the advance really did drive the scheduler
	// rather than finding nothing due and returning at once.
	witnessRan chan struct{}
	// ranAfterCancel counts callbacks that ran even though a Cancel had
	// already reported cancelling that very transition.
	ranAfterCancel atomic.Int64
}

// armedTransition is one scheduled transition, so its callback can catch
// itself running after the Cancel that claimed to have stopped it.
type armedTransition struct{ cancelled atomic.Bool }

const (
	fireRaceKey        = "fire-race"
	fireRaceWitnessKey = "fire-race-witness"
	// fireRaceMillis is how many mock milliseconds each race advances
	// through, and so roughly how many fire windows it opens.
	fireRaceMillis = 50
)

// newFireRace builds the scheduler and paces the advance. The pacing timers
// belong to the clock rather than the scheduler and do nothing: they are there
// because a mock advance stops as soon as nothing is due before its target,
// which — with a contended key that is unarmed for an instant on every round —
// can happen before the key has fired even once.
func newFireRace() *fireRace {
	mock := clock.NewMock()
	for i := 1; i <= fireRaceMillis; i++ {
		mock.AfterFunc(time.Duration(i)*time.Millisecond, func() {})
	}
	r := &fireRace{s: NewScheduler(mock), mock: mock, witnessRan: make(chan struct{})}
	r.s.After(fireRaceWitnessKey, fireRaceMillis/2*time.Millisecond, func() {
		close(r.witnessRan)
	})
	return r
}

// arm schedules one transition on the contended key, one mock millisecond out.
func (r *fireRace) arm() *armedTransition {
	a := &armedTransition{}
	r.s.After(fireRaceKey, time.Millisecond, func() {
		if a.cancelled.Load() {
			r.ranAfterCancel.Add(1)
		}
	})
	return a
}

// contendUntilStopped runs contend on another goroutine until the returned
// function is called. It returns only once contend has run at least once, so
// an advance started straight after it has something to fire.
func (r *fireRace) contendUntilStopped(contend func()) (stop func()) {
	var stopped atomic.Bool
	var wg sync.WaitGroup
	ready := make(chan struct{})

	wg.Add(1)
	go func() {
		defer wg.Done()
		contend()
		close(ready)
		for !stopped.Load() {
			contend()
		}
	}()

	<-ready
	return func() {
		stopped.Store(true)
		wg.Wait()
	}
}

// advanceWhileContending advances the mock clock through the whole race while
// contend runs alongside it.
func (r *fireRace) advanceWhileContending(contend func()) {
	stop := r.contendUntilStopped(contend)
	r.mock.Add(fireRaceMillis * time.Millisecond)
	stop()
}

// awaitExercised fails if the uncontended witness transition does not run,
// which would mean the advance drove no transition at all and the rest of the
// test is vacuous. It must be called after the advance and before Stop.
//
// It waits for the witness rather than checking whether it has already run,
// because the advance returning does not mean the witness's callback has run.
// The mock clock launches each callback on a goroutine of its own and yields
// for one millisecond, and that callback still has to take s.mu to claim its
// entry — the mutex the contending goroutine is hammering. On a loaded runner
// the test used to reach Stop first: Stop claimed the still-pending witness
// and cancelled it, the callback correctly stood down, and the test reported
// "the advance drove no transitions" about an advance that had driven one
// (CI run 36200038193). A Stop that wins the claim is meant to cancel, so the
// fault was in the test's timing, never the scheduler.
//
// The deadline is generous so that a witness that genuinely never fires still
// fails the test rather than hanging it.
//
// The contended key cannot serve as the witness: a cancel that wins the claim
// stands its callback down, so on a correct scheduler that key can legitimately
// never run fn at all.
func (r *fireRace) awaitExercised(t *testing.T) {
	t.Helper()
	select {
	case <-r.witnessRan:
	case <-time.After(10 * time.Second):
		t.Fatal("the witness transition never ran — the advance drove no transitions")
	}
}

// assertStoppedCleanly fails if Stop did not finish before its deadline —
// which is what an entry dropped without balancing its wg.Add looks like from
// the outside — or if it left work behind.
func (r *fireRace) assertStoppedCleanly(t *testing.T, timedOut bool) {
	t.Helper()
	if timedOut {
		t.Fatal("Stop() did not complete before its deadline — an entry was dropped without balancing its wg.Add")
	}
	if n := r.s.PendingCount(); n != 0 {
		t.Fatalf("PendingCount = %d after Stop, want 0", n)
	}
}

// stop stops the scheduler and asserts it did so cleanly.
func (r *fireRace) stop(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	r.s.Stop(ctx)
	r.assertStoppedCleanly(t, ctx.Err() != nil)
}

// TestScheduler_Cancel_racesTheFiringTimer reproduces the crash in CI run
// 34003824983 (job 101407402629, `Test suite (-tags slim)`), where the
// dynamodb package's test binary died with `panic: close of closed channel` in
// Scheduler.After's callback — with no `--- FAIL` line, because a panic takes
// the binary down rather than failing a test.
//
// A Cancel that lands in the mock clock's fire window (see fireRace) sees
// Timer.Stop report true and takes that as proof the callback will never run,
// so it closes the entry's done channel and balances the WaitGroup — while the
// callback, already launched, closes the same channel again on its way out.
// Under the fix the two cannot both settle one entry: whichever claims it first
// finishes it, and a Cancel that wins the claim really does cancel, so the
// callback stands down instead of running fn.
func TestScheduler_Cancel_racesTheFiringTimer(t *testing.T) {
	// Given a key that is armed a mock millisecond out over and over
	r := newFireRace()

	// When the clock is advanced through that stream of transitions while
	// another goroutine cancels the same key
	r.advanceWhileContending(func() {
		a := r.arm()
		if r.s.Cancel(fireRaceKey) {
			a.cancelled.Store(true)
		}
	})

	// Then the advance really drove the scheduler, it stops cleanly, and no
	// transition ran after a Cancel reported cancelling it
	r.awaitExercised(t)
	r.stop(t)
	if n := r.ranAfterCancel.Load(); n != 0 {
		t.Fatalf("%d transition(s) ran after Cancel reported cancelling them", n)
	}
}

// TestScheduler_After_reArmRacesTheFiringTimer is the same race reached
// through After's cancel-the-existing-entry path: a key rescheduled at the
// moment its current transition fires. A callback that finds itself superseded
// must not settle — or take out of pending — the entry that replaced it, or the
// replacement is orphaned: still armed, no longer cancellable, and holding a
// wg.Add that Stop then waits on forever.
func TestScheduler_After_reArmRacesTheFiringTimer(t *testing.T) {
	// Given a key that is armed a mock millisecond out over and over
	r := newFireRace()

	// When the clock is advanced through that stream of transitions while
	// another goroutine keeps rescheduling the same key
	r.advanceWhileContending(func() { r.arm() })

	// Then the advance really drove the scheduler, and nothing was orphaned or
	// settled twice
	r.awaitExercised(t)
	r.stop(t)
}

// TestScheduler_Stop_racesTheFiringTimer is the same race reached through
// Stop, which cancels every pending entry at once.
func TestScheduler_Stop_racesTheFiringTimer(t *testing.T) {
	// Given a key that is armed a mock millisecond out over and over
	r := newFireRace()
	stopContending := r.contendUntilStopped(func() { r.arm() })

	// When the clock is advanced through that stream of transitions while the
	// scheduler is stopped underneath it
	advanced := make(chan struct{})
	go func() {
		defer close(advanced)
		r.mock.Add(fireRaceMillis * time.Millisecond)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	r.s.Stop(ctx)
	timedOut := ctx.Err() != nil
	<-advanced
	stopContending()

	// Then Stop completed rather than waiting on an entry nobody will finish,
	// and left nothing pending
	r.assertStoppedCleanly(t, timedOut)
}
