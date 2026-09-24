//go:build linux

package lambdainit

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// telemetry_linux_test.go — the sandbox end of the Telemetry API.
//
// The relay's whole job is to be uninteresting: take the batch the host hands
// down its poll channel, POST it to the destination the extension subscribed —
// from in here, where the destination actually is — and say what happened. So
// what these tests pin is that nothing is added, dropped or reshaped on the way
// through, and that a destination which is not listening is reported rather
// than swallowed.

// startTelemetryRelay runs a relay against the fake host until the test ends.
func startTelemetryRelay(t *testing.T, h *fakeHost) {
	t.Helper()
	var diag lockedBuffer
	relay := newTelemetryRelay(h.addr(), &diagLog{w: &diag})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		relay.run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
		if t.Failed() {
			t.Logf("telemetry relay diagnostics:\n%s", diag.String())
		}
	})
}

// TestTelemetryRelayPostsTheBatchToTheDestination is the property the whole
// change exists for: the batch reaches the extension's own listener, POSTed
// from inside the sandbox, carrying exactly the bytes the host cut.
func TestTelemetryRelayPostsTheBatchToTheDestination(t *testing.T) {
	var mu sync.Mutex
	var bodies []string
	var contentTypes []string
	var methods []string
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, string(body))
		contentTypes = append(contentTypes, r.Header.Get("Content-Type"))
		methods = append(methods, r.Method)
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(destination.Close)

	h := newFakeHost(t)
	startTelemetryRelay(t, h)

	// The exact bytes a cut batch is: a JSON array of Telemetry API events,
	// with the member order the host's marshaller produced.
	const batch = `[{"time":"2026-09-06T00:00:00.000Z","type":"platform.initStart","record":{"initializationType":"on-demand","phase":"init"}},` +
		`{"time":"2026-09-06T00:00:00.010Z","type":"platform.initReport","record":{"status":"success"}}]`

	id := h.enqueueDelivery(destination.URL, batch)
	if res := h.awaitTelemetryResult(id); res.Error != "" {
		t.Fatalf("the relay reported delivery %d as failed: %s", id, res.Error)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(bodies) != 1 {
		t.Fatalf("the destination received %d POSTs, want 1: %q", len(bodies), bodies)
	}
	if bodies[0] != batch {
		t.Errorf("the destination received\n%s\nwant\n%s", bodies[0], batch)
	}
	if methods[0] != http.MethodPost {
		t.Errorf("the destination was called with %s, want POST", methods[0])
	}
	if contentTypes[0] != "application/json" {
		t.Errorf("the destination received Content-Type %q, want application/json", contentTypes[0])
	}
}

// TestTelemetryRelayDeliversInOrder pins the ordering the single poll loop
// buys: one batch is carried at a time, so a subscriber's batches reach it in
// the order the host cut them. The pool of host workers this replaces shared no
// ordering at all.
func TestTelemetryRelayDeliversInOrder(t *testing.T) {
	var mu sync.Mutex
	var bodies []string
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, string(body))
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(destination.Close)

	h := newFakeHost(t)
	startTelemetryRelay(t, h)

	var last uint64
	want := []string{`["one"]`, `["two"]`, `["three"]`}
	for _, batch := range want {
		last = h.enqueueDelivery(destination.URL, batch)
	}
	if res := h.awaitTelemetryResult(last); res.Error != "" {
		t.Fatalf("the relay reported the last delivery as failed: %s", res.Error)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(bodies) != len(want) {
		t.Fatalf("the destination received %d batches, want %d: %q", len(bodies), len(want), bodies)
	}
	for i, batch := range want {
		if bodies[i] != batch {
			t.Errorf("batch %d = %s, want %s (order: %q)", i, bodies[i], batch, bodies)
		}
	}
}

// TestTelemetryRelayReportsAnUnreachableDestination keeps a failed delivery
// visible to the host, which owns the retry and the platform.logsDropped
// accounting. A relay that answered "delivered" for a POST that never
// connected would turn a retryable hiccup into a silently lost record.
func TestTelemetryRelayReportsAnUnreachableDestination(t *testing.T) {
	dead := unansweringDestination(t)

	h := newFakeHost(t)
	startTelemetryRelay(t, h)

	id := h.enqueueDelivery(dead, `[{"type":"platform.initStart"}]`)
	if res := h.awaitTelemetryResult(id); res.Error == "" {
		t.Fatalf("the relay reported delivery %d to a dead destination as delivered", id)
	}

	// And it keeps the channel: the next batch, to a destination that is
	// listening, still arrives.
	delivered := make(chan string, 1)
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		select {
		case delivered <- string(body):
		default:
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(destination.Close)

	h.enqueueDelivery(destination.URL, `["after"]`)
	select {
	case got := <-delivered:
		if got != `["after"]` {
			t.Errorf("the destination received %s", got)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the relay stopped delivering after a failed destination")
	}
}

// unansweringDestination returns the URL of a destination that can never
// deliver: it accepts each connection and resets it without reading or
// answering, so every POST to it fails in transport.
//
// It holds its port for the whole test. Binding a port and closing it to get
// an address "nothing listens on" does not: the port is free again at once,
// and the fake host started next — or a test in a package running in parallel
// — can be handed it, turning the dead destination into one that answers
// (#2143).
func unansweringDestination(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return // the listener closed with the test
			}
			if tcp, ok := conn.(*net.TCPConn); ok {
				_ = tcp.SetLinger(0) // close with a reset, not an orderly FIN
			}
			_ = conn.Close()
		}
	}()
	return "http://" + ln.Addr().String()
}

// TestTelemetryRelayTreatsAnyResponseAsDelivered matches the host's own reading
// of an attempt it used to make itself: a response, whatever its status, means
// the bytes reached the destination. Only a transport failure is the host's to
// retry, and an extension answering 500 is not one.
func TestTelemetryRelayTreatsAnyResponseAsDelivered(t *testing.T) {
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		http.Error(w, "the extension is unhappy", http.StatusInternalServerError)
	}))
	t.Cleanup(destination.Close)

	h := newFakeHost(t)
	startTelemetryRelay(t, h)

	id := h.enqueueDelivery(destination.URL, `["x"]`)
	if res := h.awaitTelemetryResult(id); res.Error != "" {
		t.Errorf("a 500 from the destination was reported as a failure: %s", res.Error)
	}
}
