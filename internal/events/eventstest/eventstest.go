// Package eventstest helps tests assert on what a service published to the
// event bus.
package eventstest

import (
	"context"

	"github.com/overcast-sh/overcast/internal/events"
)

// Published returns the events of type t that b has published so far, in
// publication order. It reads the bus's history, which Publish appends to
// before it returns, so a test can call it straight after the operation it
// is checking without waiting on a subscriber.
func Published(b *events.Bus, t events.Type) []events.Event {
	history, cancel := b.SnapshotAndSubscribeAll(func(context.Context, events.Event) {})
	cancel()
	var out []events.Event
	for _, e := range history {
		if e.Type == t {
			out = append(out, e)
		}
	}
	return out
}
