package events

// noiseTypes is the set of event Types classified as "noise" for the
// history ring buffer's eviction policy and for the web UI's default
// filters.
//
// Intent: keep what someone debugging yesterday's incident actually wants
// to see — resource lifecycle changes, message flows, delivery outcomes,
// errors — and let the highest-volume, lowest-signal category be the first
// thing sacrificed when the buffer is full or the first thing hidden by
// default in the Events page.
//
// Today that is exactly per-request HTTP telemetry (RequestReceived):
// every incoming request — including the web UI's own polling and any
// Docker/load-balancer health checks — publishes one, and it dwarfs every
// other event type by volume. There is no separate synthetic "heartbeat" or
// "health" Bus event to classify: the SSE endpoint's periodic heartbeat
// comment is written directly to the response and never published on the
// Bus (see internal/router/events.go), and container health-check
// transitions (DockerContainerHealthStatus) are meaningful, low-frequency
// lifecycle signals rather than noise, so they are intentionally excluded
// from this set.
var noiseTypes = map[Type]struct{}{
	RequestReceived: {},
	// Per-request web function telemetry, coalesced by the publisher but
	// still one event per endpoint per second under load.
	LambdaWebRequestCompleted: {},
}

// IsNoise reports whether t is classified as noise. See noiseTypes for the
// rationale. Both the history ring buffer (internal/events/history.go) and
// the web UI's default Events-page filter derive their behaviour from this
// single classification so the two stay in lockstep as event types evolve.
func IsNoise(t Type) bool {
	_, ok := noiseTypes[t]
	return ok
}
