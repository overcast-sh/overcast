package debugger

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/overcast-sh/overcast/internal/clock"
	"github.com/overcast-sh/overcast/internal/config"
)

// freePortRange finds a range of ports starting at one the OS just handed
// out, so tests never name a fixed port. The range is only a scan window: a
// neighbour may be busy, and allocation is expected to skip it.
func freePortRange(t *testing.T) [2]int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := ln.Addr().(*net.TCPAddr).Port
	require.NoError(t, ln.Close())
	return [2]int{port, port + 50}
}

func newTestManager(t *testing.T, clk clock.Clock, policy TimeoutPolicy) *Manager {
	t.Helper()
	m := NewManager(clk, nil, "127.0.0.1", freePortRange(t), policy)
	t.Cleanup(m.Close)
	return m
}

// boundTarget registers an enabled target speaking protocol on an auto port.
func boundTarget(t *testing.T, m *Manager, id string, protocol Protocol) *Target {
	t.Helper()
	spec := Spec{Service: ServiceLambda, Tagged: true, FlagOn: true}
	tgt, err := m.Ensure(id, spec, Resolution{Protocol: protocol, Source: SourceRuntime})
	require.NoError(t, err)
	require.Equal(t, StateUnbound, tgt.State(), tgt.Reason())
	return tgt
}

// The mock clock runs an AfterFunc callback on its own goroutine, so a fired
// deadline is observed by waiting on Done, never by reading Err right away.
func waitDone(t *testing.T, ctx context.Context) {
	t.Helper()
	select {
	case <-ctx.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("context did not finish")
	}
}

func assertRunning(t *testing.T, ctx context.Context) {
	t.Helper()
	select {
	case <-ctx.Done():
		t.Fatalf("context finished early: %v", ctx.Err())
	case <-time.After(20 * time.Millisecond):
	}
}

func TestWithDeadline_noTargetIsPlainTimeout(t *testing.T) {
	// Given: no target
	// When: a zero timeout is applied
	ctx, cancel := WithDeadline(context.Background(), clock.NewMock(), 0, nil, config.DebuggerTimeoutAttached)
	defer cancel()

	// Then: it behaves as context.WithTimeout — already exceeded
	waitDone(t, ctx)
	assert.ErrorIs(t, ctx.Err(), context.DeadlineExceeded)
}

func TestWithDeadline_strictIgnoresAttachedClient(t *testing.T) {
	// Given: an attached client and the strict policy
	mock := clock.NewMock()
	m := newTestManager(t, mock, config.DebuggerTimeoutStrict)
	tgt := boundTarget(t, m, "lambda/fn", inspector{})
	tgt.attach()

	// When: a zero timeout is applied
	ctx, cancel := WithDeadline(context.Background(), mock, 0, tgt, config.DebuggerTimeoutStrict)
	defer cancel()

	// Then: the real timeout applies regardless
	waitDone(t, ctx)
	assert.ErrorIs(t, ctx.Err(), context.DeadlineExceeded)
}

func TestWithDeadline_inertTargetIsPlainTimeout(t *testing.T) {
	// Given: a tagged target the flag keeps inert
	mock := clock.NewMock()
	m := newTestManager(t, mock, config.DebuggerTimeoutAttached)
	tgt, err := m.Ensure("lambda/fn", Spec{Service: ServiceLambda, Tagged: true}, Resolution{Protocol: inspector{}})
	require.NoError(t, err)

	// When: a zero timeout is applied
	ctx, cancel := WithDeadline(context.Background(), mock, 0, tgt, config.DebuggerTimeoutAttached)
	defer cancel()

	// Then: nothing suspends it
	waitDone(t, ctx)
	assert.ErrorIs(t, ctx.Err(), context.DeadlineExceeded)
}

func TestWithDeadline_runsOutWithNoClient(t *testing.T) {
	// Given: a listening target nobody is attached to
	mock := clock.NewMock()
	m := newTestManager(t, mock, config.DebuggerTimeoutAttached)
	tgt := boundTarget(t, m, "lambda/fn", inspector{})
	start := mock.Now()

	// When: three seconds pass
	ctx, cancel := WithDeadline(context.Background(), mock, 3*time.Second, tgt, config.DebuggerTimeoutAttached)
	defer cancel()
	deadline, ok := ctx.Deadline()
	mock.Add(2 * time.Second)
	assertRunning(t, ctx)
	mock.Add(time.Second)

	// Then: the deadline fires on time and reports the nominal deadline
	waitDone(t, ctx)
	assert.ErrorIs(t, ctx.Err(), context.DeadlineExceeded)
	assert.True(t, ok)
	assert.Equal(t, start.Add(3*time.Second), deadline)
}

func TestWithDeadline_suspendedBeforeStart(t *testing.T) {
	// Given: a client attached before the invocation starts
	mock := clock.NewMock()
	m := newTestManager(t, mock, config.DebuggerTimeoutAttached)
	tgt := boundTarget(t, m, "lambda/fn", inspector{})
	tgt.attach()

	// When: far more than the timeout passes while attached, then the client
	// detaches and the timeout passes again
	ctx, cancel := WithDeadline(context.Background(), mock, 3*time.Second, tgt, config.DebuggerTimeoutAttached)
	defer cancel()
	mock.Add(time.Minute)
	assertRunning(t, ctx)
	tgt.detach(&connection{target: tgt})
	mock.Add(2 * time.Second)
	assertRunning(t, ctx)
	mock.Add(time.Second)

	// Then: only running time counted, and Deadline still says start+timeout
	waitDone(t, ctx)
	assert.ErrorIs(t, ctx.Err(), context.DeadlineExceeded)
	deadline, _ := ctx.Deadline()
	assert.Equal(t, time.Unix(0, 0).Add(3*time.Second), deadline)
}

func TestWithDeadline_suspendedMidRunAndSeveralCycles(t *testing.T) {
	// Given: a running invocation with a 3 s budget
	mock := clock.NewMock()
	m := newTestManager(t, mock, config.DebuggerTimeoutAttached)
	tgt := boundTarget(t, m, "lambda/fn", inspector{})
	ctx, cancel := WithDeadline(context.Background(), mock, 3*time.Second, tgt, config.DebuggerTimeoutAttached)
	defer cancel()
	conn := &connection{target: tgt}

	// When: it runs 1 s, pauses under a client for an hour, runs 1 s, pauses
	// again, then runs the last second
	mock.Add(time.Second)
	tgt.attach()
	mock.Add(time.Hour)
	assertRunning(t, ctx)
	tgt.detach(conn)
	mock.Add(time.Second)
	assertRunning(t, ctx)
	tgt.attach()
	mock.Add(time.Hour)
	assertRunning(t, ctx)
	tgt.detach(conn)
	mock.Add(999 * time.Millisecond)
	assertRunning(t, ctx)
	mock.Add(time.Millisecond)

	// Then: the budget is exhausted exactly when 3 s of running time elapsed
	waitDone(t, ctx)
	assert.ErrorIs(t, ctx.Err(), context.DeadlineExceeded)
}

func TestWithDeadline_secondClientKeepsItSuspended(t *testing.T) {
	// Given: two clients attached
	mock := clock.NewMock()
	m := newTestManager(t, mock, config.DebuggerTimeoutAttached)
	tgt := boundTarget(t, m, "lambda/fn", inspector{})
	ctx, cancel := WithDeadline(context.Background(), mock, time.Second, tgt, config.DebuggerTimeoutAttached)
	defer cancel()
	tgt.attach()
	tgt.attach()

	// When: one detaches and time passes
	tgt.detach(&connection{target: tgt})
	mock.Add(time.Hour)

	// Then: the other still holds the clock
	assertRunning(t, ctx)
	tgt.detach(&connection{target: tgt})
	mock.Add(time.Second)
	waitDone(t, ctx)
}

func TestWithDeadline_pausedPolicyFollowsObserver(t *testing.T) {
	// Given: the paused policy and a protocol with an observer
	mock := clock.NewMock()
	m := newTestManager(t, mock, config.DebuggerTimeoutPaused)
	tgt := boundTarget(t, m, "lambda/fn", inspector{})
	ctx, cancel := WithDeadline(context.Background(), mock, 3*time.Second, tgt, config.DebuggerTimeoutPaused)
	defer cancel()
	conn := &connection{target: tgt}

	// When: a client attaches but the program runs, then pauses, then resumes
	tgt.attach()
	mock.Add(2 * time.Second)
	assertRunning(t, ctx)
	tgt.pause(conn)
	mock.Add(time.Hour)
	assertRunning(t, ctx)
	tgt.resume(conn)
	mock.Add(time.Second)

	// Then: attached-but-running time counted, paused time did not
	waitDone(t, ctx)
	assert.ErrorIs(t, ctx.Err(), context.DeadlineExceeded)
}

func TestWithDeadline_pausedPolicyFallsBackForBlindProtocols(t *testing.T) {
	// Given: the paused policy and jdwp, which has no observer
	mock := clock.NewMock()
	m := newTestManager(t, mock, config.DebuggerTimeoutPaused)
	tgt := boundTarget(t, m, "lambda/fn", jdwp{})
	tgt.attach()
	ctx, cancel := WithDeadline(context.Background(), mock, time.Second, tgt, config.DebuggerTimeoutPaused)
	defer cancel()

	// When: an hour passes attached
	mock.Add(time.Hour)

	// Then: attachment alone suspends the clock
	assertRunning(t, ctx)
	tgt.detach(&connection{target: tgt})
	mock.Add(time.Second)
	waitDone(t, ctx)
}

func TestWithDeadline_droppedPausedClientResumesTheClock(t *testing.T) {
	// Given: a client paused under the paused policy
	mock := clock.NewMock()
	m := newTestManager(t, mock, config.DebuggerTimeoutPaused)
	tgt := boundTarget(t, m, "lambda/fn", inspector{})
	ctx, cancel := WithDeadline(context.Background(), mock, time.Second, tgt, config.DebuggerTimeoutPaused)
	defer cancel()
	conn := &connection{target: tgt}
	tgt.attach()
	tgt.pause(conn)
	mock.Add(time.Hour)
	assertRunning(t, ctx)

	// When: the client drops without resuming
	tgt.detach(conn)
	mock.Add(time.Second)

	// Then: the clock resumed on its own
	waitDone(t, ctx)
	assert.False(t, tgt.Paused())
}

func TestWithDeadline_childContextSeesDeadlineExceeded(t *testing.T) {
	// Given: a child derived from the suspendable context
	mock := clock.NewMock()
	m := newTestManager(t, mock, config.DebuggerTimeoutAttached)
	tgt := boundTarget(t, m, "lambda/fn", inspector{})
	ctx, cancel := WithDeadline(context.Background(), mock, time.Second, tgt, config.DebuggerTimeoutAttached)
	defer cancel()
	child, childCancel := context.WithCancel(ctx)
	defer childCancel()

	// When: the budget runs out
	mock.Add(time.Second)

	// Then: the child reports a timeout, not a cancellation — the invoke
	// path tells the two apart
	waitDone(t, child)
	assert.True(t, errors.Is(child.Err(), context.DeadlineExceeded))
}

func TestWithDeadline_cancelAndParent(t *testing.T) {
	mock := clock.NewMock()
	m := newTestManager(t, mock, config.DebuggerTimeoutAttached)
	tgt := boundTarget(t, m, "lambda/fn", inspector{})

	t.Run("cancel func", func(t *testing.T) {
		// Given: a suspendable context
		ctx, cancel := WithDeadline(context.Background(), mock, time.Hour, tgt, config.DebuggerTimeoutAttached)

		// When: it is cancelled
		cancel()

		// Then: it is Canceled and its subscription is gone
		waitDone(t, ctx)
		assert.ErrorIs(t, ctx.Err(), context.Canceled)
		tgt.subMu.Lock()
		assert.Empty(t, tgt.subs)
		tgt.subMu.Unlock()
	})

	t.Run("parent cancelled", func(t *testing.T) {
		// Given: a suspendable context under a cancellable parent
		parent, cancelParent := context.WithCancel(context.Background())
		ctx, cancel := WithDeadline(parent, mock, time.Hour, tgt, config.DebuggerTimeoutAttached)
		defer cancel()

		// When: the parent is cancelled
		cancelParent()

		// Then: the child follows with the parent's error
		waitDone(t, ctx)
		assert.ErrorIs(t, ctx.Err(), context.Canceled)
		tgt.subMu.Lock()
		assert.Empty(t, tgt.subs)
		tgt.subMu.Unlock()
	})

	t.Run("value passes through", func(t *testing.T) {
		// Given: a parent carrying a value
		type key struct{}
		parent := context.WithValue(context.Background(), key{}, "v")
		ctx, cancel := WithDeadline(parent, mock, time.Hour, tgt, config.DebuggerTimeoutAttached)
		defer cancel()

		// Then: the value is visible through the suspendable context
		assert.Equal(t, "v", ctx.Value(key{}))
	})
}
