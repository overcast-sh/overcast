package debugger

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/overcast-sh/overcast/internal/clock"
	"github.com/overcast-sh/overcast/internal/config"
)

// waitingTarget registers a bound inspector target whose tags ask for the
// hold.
func waitingTarget(t *testing.T, m *Manager, id string) *Target {
	t.Helper()
	spec := Spec{Service: ServiceLambda, Tagged: true, FlagOn: true, Wait: true}
	tgt, err := m.Ensure(id, spec, Resolution{Protocol: inspector{}, Source: SourceRuntime})
	require.NoError(t, err)
	require.Equal(t, StateUnbound, tgt.State(), tgt.Reason())
	return tgt
}

// awaitInBackground runs AwaitClient on its own goroutine and hands back
// where its result will land.
func awaitInBackground(ctx context.Context, tgt *Target, timeout time.Duration) <-chan WaitResult {
	done := make(chan WaitResult, 1)
	go func() { done <- tgt.AwaitClient(ctx, timeout) }()
	return done
}

// assertHolding gives the goroutine time to arm its timers on the mock clock
// and asserts it has not returned — the mock only fires timers that exist
// when it is advanced.
func assertHolding(t *testing.T, done <-chan WaitResult) {
	t.Helper()
	select {
	case result := <-done:
		t.Fatalf("hold ended early: %s", result)
	case <-time.After(20 * time.Millisecond):
	}
}

func awaitResult(t *testing.T, done <-chan WaitResult) WaitResult {
	t.Helper()
	select {
	case result := <-done:
		return result
	case <-time.After(5 * time.Second):
		t.Fatal("hold never ended")
		return ""
	}
}

func TestAwaitClient_releasesOnAttachPlusSettle(t *testing.T) {
	// Given: a target asking for the hold, with nobody attached
	mock := clock.NewMock()
	m := newTestManager(t, mock, config.DebuggerTimeoutAttached)
	tgt := waitingTarget(t, m, "lambda/fn")

	// When: an invocation waits, the timeout is nowhere near, and a client
	// attaches
	done := awaitInBackground(context.Background(), tgt, 2*time.Minute)
	assertHolding(t, done)
	mock.Add(time.Minute)
	assertHolding(t, done)
	tgt.attach()

	// Then: it is still held for the settle, and released once it passes
	assertHolding(t, done)
	mock.Add(SettleAfterAttach)
	assert.Equal(t, WaitAttached, awaitResult(t, done))
}

func TestAwaitClient_expiresAndProceeds(t *testing.T) {
	// Given: a target asking for the hold, with nobody attached
	mock := clock.NewMock()
	m := newTestManager(t, mock, config.DebuggerTimeoutAttached)
	tgt := waitingTarget(t, m, "lambda/fn")

	// When: the wait timeout passes with no client
	done := awaitInBackground(context.Background(), tgt, 5*time.Second)
	assertHolding(t, done)
	mock.Add(5 * time.Second)

	// Then: the hold ends as expired, and its subscription is gone
	assert.Equal(t, WaitExpired, awaitResult(t, done))
	tgt.subMu.Lock()
	assert.Empty(t, tgt.subs)
	tgt.subMu.Unlock()
}

func TestAwaitClient_cancelledCallerReleasesTheHold(t *testing.T) {
	// Given: a held invocation
	mock := clock.NewMock()
	m := newTestManager(t, mock, config.DebuggerTimeoutAttached)
	tgt := waitingTarget(t, m, "lambda/fn")
	ctx, cancel := context.WithCancel(context.Background())
	done := awaitInBackground(ctx, tgt, 2*time.Minute)
	assertHolding(t, done)

	// When: the caller goes away
	cancel()

	// Then: the hold ends at once
	assert.Equal(t, WaitCancelled, awaitResult(t, done))
}

func TestAwaitClient_skipsWhenNothingAsksForIt(t *testing.T) {
	mock := clock.NewMock()

	t.Run("no tag", func(t *testing.T) {
		// Given: a bound target whose tags do not ask for the hold
		m := newTestManager(t, mock, config.DebuggerTimeoutAttached)
		tgt := boundTarget(t, m, "lambda/plain", inspector{})

		// When/Then: nothing is waited for
		assert.Equal(t, WaitSkipped, tgt.AwaitClient(context.Background(), time.Minute))
	})

	t.Run("strict policy", func(t *testing.T) {
		// Given: the tag under the strict policy, where nothing may hold an
		// invocation
		m := newTestManager(t, mock, config.DebuggerTimeoutStrict)
		tgt := waitingTarget(t, m, "lambda/strict")

		// When/Then: the tag is ignored and the invocation is not held
		assert.Equal(t, WaitSkipped, tgt.AwaitClient(context.Background(), time.Minute))
	})

	t.Run("inert target", func(t *testing.T) {
		// Given: the tag on a server whose flag is off
		m := newTestManager(t, mock, config.DebuggerTimeoutAttached)
		tgt, err := m.Ensure("lambda/inert", Spec{Service: ServiceLambda, Tagged: true, Wait: true}, Resolution{Protocol: inspector{}})
		require.NoError(t, err)

		// When/Then: nothing can attach, so nothing is waited for
		assert.Equal(t, WaitSkipped, tgt.AwaitClient(context.Background(), time.Minute))
	})

	t.Run("nil target", func(t *testing.T) {
		var tgt *Target
		assert.Equal(t, WaitSkipped, tgt.AwaitClient(context.Background(), time.Minute))
	})
}

func TestAwaitClient_clientAlreadyAttached(t *testing.T) {
	mock := clock.NewMock()
	m := newTestManager(t, mock, config.DebuggerTimeoutAttached)
	tgt := waitingTarget(t, m, "lambda/fn")
	tgt.attach()

	t.Run("just now still settles", func(t *testing.T) {
		// Given: a client that attached a moment ago
		mock.Add(SettleAfterAttach / 3)

		// When: an invocation arrives
		done := awaitInBackground(context.Background(), tgt, time.Minute)

		// Then: it waits out the rest of the settle, measured from the attach
		assertHolding(t, done)
		mock.Add(SettleAfterAttach - SettleAfterAttach/3)
		assert.Equal(t, WaitAttached, awaitResult(t, done))
	})

	t.Run("long ago is not waited on", func(t *testing.T) {
		// Given: the same client, attached for far longer than the settle
		mock.Add(time.Minute)

		// When/Then: the invocation is dispatched at once
		assert.Equal(t, WaitSkipped, tgt.AwaitClient(context.Background(), time.Minute))
	})
}

func TestEnsure_waitToggleKeepsTheTarget(t *testing.T) {
	// Given: a registered target with a client attached
	mock := clock.NewMock()
	m := newTestManager(t, mock, config.DebuggerTimeoutAttached)
	spec := Spec{Service: ServiceLambda, Tagged: true, FlagOn: true}
	res := Resolution{Protocol: inspector{}, Source: SourceRuntime}
	first, err := m.Ensure("lambda/fn", spec, res)
	require.NoError(t, err)
	first.attach()
	require.False(t, first.Wait())

	// When: the tags gain the wait, then lose it again
	spec.Wait = true
	on, err := m.Ensure("lambda/fn", spec, res)
	require.NoError(t, err)
	spec.Wait = false
	off, err := m.Ensure("lambda/fn", spec, res)
	require.NoError(t, err)

	// Then: it is the same target throughout — same port, client still
	// attached — with the flag applied in place and reported in the
	// descriptor
	assert.Same(t, first, on)
	assert.Same(t, first, off)
	assert.True(t, first.Attached())
	assert.False(t, first.Wait())
	spec.Wait = true
	_, err = m.Ensure("lambda/fn", spec, res)
	require.NoError(t, err)
	assert.True(t, first.Wait())
	assert.True(t, first.Descriptor().WaitForDebugger)
}
