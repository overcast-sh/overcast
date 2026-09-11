package debugger

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/overcast-sh/overcast/internal/clock"
	"github.com/overcast-sh/overcast/internal/config"
)

// The bridge closes a console session with 1012 when the container behind
// the port changes, which it learns from EventUpstream — fired for a change
// and only a change, so a repeated bind or a stale container's late clear
// does not cost a reconnect.
func TestTarget_upstreamChangesAreEvents(t *testing.T) {
	// Given: a bound target and a subscriber to every event
	m := newTestManager(t, clock.NewMock(), config.DebuggerTimeoutAttached)
	tgt := boundTarget(t, m, "lambda/fn", inspector{})
	var kinds []EventKind
	tgt.Subscribe(func(ev Event) { kinds = append(kinds, ev.Kind) })

	// When: the container is bound, bound again to the same address,
	// replaced, and a retired container's clear arrives after the
	// replacement
	tgt.SetUpstream("127.0.0.1:55001")
	tgt.SetContainerID("c1")
	tgt.SetUpstream("127.0.0.1:55001")
	tgt.SetUpstream("127.0.0.1:55002")
	tgt.SetContainerID("c2")
	tgt.ClearContainer("c1")

	// Then: only the two changes were events, and the replacement stands
	assert.Equal(t, []EventKind{EventUpstream, EventUpstream}, kinds)
	assert.Equal(t, "127.0.0.1:55002", tgt.Upstream())

	// When: the live container is cleared
	tgt.ClearContainer("c2")

	// Then: that is a change too
	assert.Equal(t, []EventKind{EventUpstream, EventUpstream, EventUpstream}, kinds)
	assert.Equal(t, "", tgt.Upstream())
}

// A decoded message reaches the same state as the framed stream, so a pause
// the bridge relays is the pause the proxy would have seen.
func TestCDPObserver_wholeMessagesShareTheFramedState(t *testing.T) {
	o := newCDPObserver()

	paused, resumed := o.FromServerMessage([]byte(pausedMsg))
	assert.True(t, paused)
	assert.False(t, resumed)

	paused, resumed = o.FromServerMessage([]byte(otherMsg))
	assert.False(t, paused)
	assert.False(t, resumed)

	paused, resumed = o.FromServerMessage([]byte(resumedMsg))
	assert.False(t, paused)
	assert.True(t, resumed)

	paused, resumed = o.FromServerMessage([]byte("not json"))
	assert.False(t, paused)
	assert.False(t, resumed)
}
