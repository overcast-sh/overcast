package debugger

import (
	"io"
	"net"
	"time"

	"go.uber.org/zap"
)

// proxy.go — the per-target loopback listener. Overcast owns the debug port
// rather than publishing the container's: that is what makes attach state
// known at TCP level for any protocol and any editor, and what keeps the
// port stable while hot reload replaces the container underneath it, so an
// editor's reconnect finds the new one without the user doing anything.

// dialTimeout bounds the connect to the container. A container that has
// stopped answers with a refusal well within it; one that is hung should not
// hold an editor's connection attempt for longer.
const dialTimeout = 2 * time.Second

// serve accepts until the listener is closed by Target.close.
func (t *Target) serve(ln net.Listener) {
	defer t.wg.Done()
	for {
		client, err := ln.Accept()
		if err != nil {
			return
		}
		t.wg.Add(1)
		go t.handle(client)
	}
}

// handle forwards one client connection to the upstream. With no upstream
// the connection is closed at once — editors that poll (VS Code's
// restart: true) simply try again — so a client never sits on a half-open
// socket waiting for a container that is not there.
func (t *Target) handle(client net.Conn) {
	defer t.wg.Done()
	if !t.track(client) {
		client.Close()
		return
	}
	defer t.untrack(client)

	upstream := t.Upstream()
	if upstream == "" {
		client.Close()
		return
	}
	dialer := net.Dialer{Timeout: dialTimeout}
	server, err := dialer.DialContext(t.ctx, "tcp", upstream)
	if err != nil {
		t.log.Debug("debugger: upstream dial failed", zap.String("upstream", upstream), zap.Error(err))
		client.Close()
		return
	}
	if !t.track(server) {
		server.Close()
		client.Close()
		return
	}
	defer t.untrack(server)

	conn := t.newConnection()
	defer conn.close()

	// Client→container needs no inspection. io.Copy lets the platform
	// splice it when it can; either side closing ends both copies.
	t.wg.Add(1)
	go func() {
		defer t.wg.Done()
		_, _ = io.Copy(server, client)
		server.Close()
		client.Close()
	}()

	_, _ = io.Copy(client, conn.observedReader(server))
	client.Close()
	server.Close()
}

// track records a connection so Target.close can end it. It refuses once
// the target is closed, which closes the race between an Accept that won and
// a close that already collected the connection set.
func (t *Target) track(c net.Conn) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return false
	}
	t.conns[c] = struct{}{}
	return true
}

func (t *Target) untrack(c net.Conn) {
	t.mu.Lock()
	delete(t.conns, c)
	t.mu.Unlock()
}

// enter counts a goroutine the target must wait for before it is released —
// a bridge session arriving on the HTTP server's goroutine, which serve did
// not spawn. It refuses once the target is closed, so an arrival that lost
// the race with Release does not outlive it; the caller owes a wg.Done.
func (t *Target) enter() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return false
	}
	t.wg.Add(1)
	return true
}

// connection is one attached client, whichever way it arrived — the TCP
// proxy or the console's WebSocket bridge — so the two count and observe
// through one path. It carries the client's own pause state, so a target
// with several clients counts pauses per connection and a dropped paused
// client resumes cleanly, and the protocol's observer when it has one.
type connection struct {
	target   *Target
	paused   bool
	observer Observer // nil when the protocol cannot tell a pause
}

// newConnection counts a client as attached and gives it an observer for
// the container→client direction when the protocol has one. close undoes
// both.
func (t *Target) newConnection() *connection {
	c := &connection{target: t}
	if po, ok := t.res.Protocol.(PauseObserver); ok {
		c.observer = po.NewObserver()
	}
	t.attach()
	return c
}

// close detaches the client, resuming it first if its last word was a pause.
func (c *connection) close() { c.target.detach(c) }

// fromServer feeds container→client bytes, as a socket delivers them, to the
// observer and applies its verdict.
func (c *connection) fromServer(b []byte) {
	if c.observer == nil {
		return
	}
	c.apply(c.observer.FromServer(b))
}

// fromServerMessage feeds one whole container→client message — a WebSocket
// frame the bridge already decoded — to an observer that reads messages.
func (c *connection) fromServerMessage(msg []byte) {
	mo, ok := c.observer.(MessageObserver)
	if !ok {
		return
	}
	c.apply(mo.FromServerMessage(msg))
}

func (c *connection) apply(paused, resumed bool) {
	if paused {
		c.target.pause(c)
	}
	if resumed {
		c.target.resume(c)
	}
}

// observedReader wraps the container side of a byte splice so what is read
// from it passes through the observer; r itself when there is none, so the
// copy stays a plain socket read.
func (c *connection) observedReader(r io.Reader) io.Reader {
	if c.observer == nil {
		return r
	}
	return &observed{r: r, conn: c}
}

// observed feeds the container→client bytes through the connection's
// observer. It sits on the read side so io.Copy's write path stays a plain
// socket write.
type observed struct {
	r    io.Reader
	conn *connection
}

func (o *observed) Read(p []byte) (int, error) {
	n, err := o.r.Read(p)
	if n > 0 {
		o.conn.fromServer(p[:n])
	}
	return n, err
}

// The four transitions below take emitMu for the whole change-and-notify so
// subscribers see attach/detach/pause/resume in the order they happened.

func (t *Target) attach() {
	t.emitMu.Lock()
	defer t.emitMu.Unlock()
	now := t.clk.Now()
	t.mu.Lock()
	t.attached++
	first := t.attached == 1
	if first {
		t.attachedSince = now
	}
	t.mu.Unlock()
	if first {
		t.log.Info("debugger: client attached")
		t.deliver(EventAttach, now)
	}
}

func (t *Target) detach(conn *connection) {
	t.resume(conn)
	t.emitMu.Lock()
	defer t.emitMu.Unlock()
	now := t.clk.Now()
	t.mu.Lock()
	t.attached--
	last := t.attached == 0
	if last {
		t.attachedSince = time.Time{}
	}
	t.mu.Unlock()
	if last {
		t.log.Info("debugger: client detached")
		t.deliver(EventDetach, now)
	}
}

func (t *Target) pause(conn *connection) {
	t.emitMu.Lock()
	defer t.emitMu.Unlock()
	if conn.paused {
		return
	}
	conn.paused = true
	now := t.clk.Now()
	t.mu.Lock()
	t.paused++
	first := t.paused == 1
	if first {
		t.pausedSince = now
	}
	t.mu.Unlock()
	if first {
		t.deliver(EventPause, now)
	}
}

func (t *Target) resume(conn *connection) {
	t.emitMu.Lock()
	defer t.emitMu.Unlock()
	if !conn.paused {
		return
	}
	conn.paused = false
	now := t.clk.Now()
	t.mu.Lock()
	t.paused--
	last := t.paused == 0
	if last {
		t.pausedSince = time.Time{}
	}
	t.mu.Unlock()
	if last {
		t.deliver(EventResume, now)
	}
}
