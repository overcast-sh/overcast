package debugger

// bridge.go — the console's way onto a target: a WebSocket on Overcast's own
// port, joined to the container exactly as the TCP proxy joins an editor
// (docs/plans/compute-debugger-console.md § 3.1). Browsers cannot open raw
// TCP, and a WebSocket through the BFF keeps the console same-origin. A
// bridge session is one more attached client of the Target — it counts, it
// observes, and it suspends the invocation clock — through the same
// connection bookkeeping as a TCP client, so the Debug tab reads attached and
// paused for console sessions too.
//
// A WebSocket-framed protocol (the inspector) is relayed message by message,
// each container→client message read by the observer whole; anything else is
// a byte splice over binary frames.

import (
	"context"
	"io"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/coder/websocket"
	"go.uber.org/zap"
)

const (
	// bridgeKeepalive is how often the bridge pings the console, and how
	// long a pong may take: a ping still unanswered when the next is due is
	// a missed one, and bridgeMissedPongs misses in a row close the session.
	bridgeKeepalive   = 20 * time.Second
	bridgeMissedPongs = 2

	// bridgeMaxMessage bounds one message either way. Debugger.paused with
	// its call frames and Debugger.getScriptSource of a bundled file run to
	// megabytes, and the library's 32 KiB default would cut the first pause
	// off mid-frame.
	bridgeMaxMessage = 64 << 20

	// closeReasonMax is RFC 6455 § 5.5.1's room for a close frame's reason.
	closeReasonMax = 123
)

// Close codes and reasons the console keys on (§ 3.1).
const (
	// bridgeNoContainer: no container is behind the target yet, or it went
	// away. The console says "waiting for a container — invoke once".
	bridgeNoContainer = websocket.StatusInternalError // 1011
	// bridgeRestart: the container was replaced while the session was open
	// (hot reload retired it). The console reconnects and re-applies its
	// breakpoints on the next scriptParsed.
	bridgeRestart = websocket.StatusServiceRestart // 1012
	// bridgeStale: the console missed two keepalive pongs in a row.
	bridgeStale = websocket.StatusGoingAway // 1001
)

// bridgeOriginPatterns are the origins accepted besides the request's own
// host, which websocket.Accept always allows: the console served from any
// loopback port — the UI port, a published container port, the Vite dev
// server. Through the BFF the browser's Host and Origin arrive as they were
// sent, so a console on some other host matches by being the request host.
// Anything else is a 403 from Accept.
var bridgeOriginPatterns = []string{"localhost", "localhost:*", "127.0.0.1", "127.0.0.1:*"}

// webSocketDiscoverer is optional on a Protocol. A protocol implementing it is
// itself WebSocket-framed on the container's port and names the session URL
// to dial — the inspector's /json/list — so the bridge relays it message by
// message instead of splicing bytes.
type webSocketDiscoverer interface {
	discoverWebSocket(ctx context.Context, upstream string) (string, error)
}

// ServeWebSocket is the bridge for one console session on the target:
// GET /_overcast/debugger/targets/{service}/{resource}/ws, upgraded. The
// session lasts until the console closes it, the container goes away
// (1011), the container is replaced (1012), the console stops answering
// pings (1001), or the target is released — and every goroutine it started
// has exited when it returns.
func (t *Target) ServeWebSocket(w http.ResponseWriter, r *http.Request) {
	if !t.enter() {
		writeJSON(w, http.StatusNotFound, errorBody{Error: "target released: " + t.id})
		return
	}
	defer t.wg.Done()

	client, err := websocket.Accept(w, r, &websocket.AcceptOptions{OriginPatterns: bridgeOriginPatterns})
	if err != nil {
		// Accept answered the request itself: 403 for an origin it refused,
		// 400 for a handshake that was not one.
		t.log.Debug("debugger: bridge handshake refused",
			zap.String("origin", r.Header.Get("Origin")), zap.String("host", r.Host), zap.Error(err))
		return
	}
	client.SetReadLimit(bridgeMaxMessage)

	ctx, cancel := context.WithCancel(t.ctx)
	defer cancel()
	s := &bridgeSession{
		target:  t,
		client:  client,
		log:     t.log.With(zap.String("console", r.RemoteAddr)),
		ctx:     ctx,
		cancel:  cancel,
		ends:    make(chan relayEnd, 2),
		changed: make(chan struct{}, 1),
		stale:   make(chan struct{}),
	}
	s.run()
}

// bridgeSession is one console session: the accepted client, the relays
// either way, and the signals that end it.
type bridgeSession struct {
	target *Target
	client *websocket.Conn
	log    *zap.Logger

	// ctx bounds every read and write; cancelling it is the last step of a
	// teardown, after the close frame the console should see has gone out,
	// because a cancelled read tears the connection down without one.
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	ends    chan relayEnd // the first relay to finish says which side left
	changed chan struct{} // the target's upstream moved; compare before acting
	stale   chan struct{} // closed when the console misses its pongs
}

// relayEnd is one relay direction finishing.
type relayEnd struct {
	fromClient bool // the client→container relay ended: the console left
	err        error
}

// run attaches, relays until something ends the session, and closes both
// sides in the order that lets the console see why.
func (s *bridgeSession) run() {
	t := s.target
	if !t.Bound() {
		s.closeClient(bridgeNoContainer, "not listening: "+t.Reason())
		return
	}
	// Subscribed before the upstream is read, so a replacement between the
	// two is a signal the loop below compares against what it dialled rather
	// than a change it never hears of.
	unsubscribe := t.Subscribe(func(ev Event) {
		if ev.Kind != EventUpstream {
			return
		}
		select {
		case s.changed <- struct{}{}:
		default:
		}
	})
	defer unsubscribe()

	upstream := t.Upstream()
	if upstream == "" {
		s.closeClient(bridgeNoContainer, "no container")
		return
	}
	if d, ok := t.res.Protocol.(webSocketDiscoverer); ok {
		s.relayMessages(d, upstream)
		return
	}
	s.relayBytes(upstream)
}

// relayMessages joins the console to a WebSocket-framed container protocol:
// the session URL is discovered, dialled at the upstream, and messages go
// across unchanged, the container's through the observer.
func (s *bridgeSession) relayMessages(d webSocketDiscoverer, upstream string) {
	sessionURL, err := d.discoverWebSocket(s.ctx, upstream)
	if err != nil {
		s.log.Warn("debugger: bridge could not discover the container's debug session — session refused",
			zap.String("upstream", upstream), zap.Error(err),
			zap.String("hint", "the container may still be starting, or its debugger is not on the port the target injected; invoke once and open the session again"))
		s.closeClient(bridgeNoContainer, "container unreachable")
		return
	}
	dialCtx, cancel := context.WithTimeout(s.ctx, dialTimeout)
	server, resp, err := websocket.Dial(dialCtx, sessionURL, nil)
	cancel()
	if resp != nil && resp.Body != nil {
		// Dial has already consumed the handshake body — it is nil on
		// success and a drained buffer on failure — so this is only the
		// close the linter wants to see.
		_ = resp.Body.Close()
	}
	if err != nil {
		s.log.Warn("debugger: bridge could not dial the container's debug session — session refused",
			zap.String("url", sessionURL), zap.Error(err),
			zap.String("hint", "the container's debugger may already hold its one client; detach the editor, or invoke once to get a fresh container"))
		s.closeClient(bridgeNoContainer, "container unreachable")
		return
	}
	server.SetReadLimit(bridgeMaxMessage)

	conn := s.target.newConnection()
	defer conn.close()
	s.relay(true, func() error { return copyMessages(s.ctx, s.client, server, nil) })
	s.relay(false, func() error { return copyMessages(s.ctx, server, s.client, conn.fromServerMessage) })
	s.await(upstream, func(graceful bool) {
		if graceful {
			_ = server.Close(websocket.StatusNormalClosure, "")
		}
		_ = server.CloseNow()
	})
}

// copyMessages relays from one WebSocket to the other until either fails,
// handing each message to observe first when there is one.
func copyMessages(ctx context.Context, from, to *websocket.Conn, observe func([]byte)) error {
	for {
		typ, msg, err := from.Read(ctx)
		if err != nil {
			return err
		}
		if observe != nil {
			observe(msg)
		}
		if err := to.Write(ctx, typ, msg); err != nil {
			return err
		}
	}
}

// relayBytes joins the console to a byte-oriented container protocol: the
// client's binary frames are one stream, spliced to a TCP dial of the
// upstream, the container's bytes through the observer as in the TCP proxy.
func (s *bridgeSession) relayBytes(upstream string) {
	dialer := net.Dialer{Timeout: dialTimeout}
	server, err := dialer.DialContext(s.ctx, "tcp", upstream)
	if err != nil {
		s.log.Warn("debugger: bridge could not dial the container's debug port — session refused",
			zap.String("upstream", upstream), zap.Error(err),
			zap.String("hint", "the container may have stopped; invoke once and open the session again"))
		s.closeClient(bridgeNoContainer, "container unreachable")
		return
	}
	if !s.target.track(server) {
		server.Close()
		return
	}
	defer s.target.untrack(server)

	conn := s.target.newConnection()
	defer conn.close()
	clientStream := websocket.NetConn(s.ctx, s.client, websocket.MessageBinary)
	s.relay(true, func() error {
		_, err := io.Copy(server, clientStream)
		return err
	})
	s.relay(false, func() error {
		_, err := io.Copy(clientStream, conn.observedReader(server))
		return err
	})
	s.await(upstream, func(bool) { server.Close() })
}

// relay runs one direction until it ends and reports which.
func (s *bridgeSession) relay(fromClient bool, run func() error) {
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.ends <- relayEnd{fromClient: fromClient, err: run()}
	}()
}

// await pings the console and waits for whatever ends the session, then
// tears it down: the close frame the console should see first, the
// container next, and only then the context, so no read is cancelled before
// the close frame is on the wire. closeUpstream is told whether the console
// left of its own accord, which is the one case worth a close handshake with
// the container rather than a drop.
func (s *bridgeSession) await(upstream string, closeUpstream func(graceful bool)) {
	s.keepalive()
	defer s.wg.Wait()
	defer s.cancel()
	for {
		select {
		case end := <-s.ends:
			if end.fromClient {
				s.log.Debug("debugger: console session closed by the console", zap.Error(end.err))
				_ = s.client.CloseNow()
				closeUpstream(true)
				return
			}
			s.log.Debug("debugger: console session closed by the container", zap.Error(end.err))
			s.closeClient(bridgeNoContainer, "container closed")
			closeUpstream(false)
			return
		case <-s.changed:
			if s.target.Upstream() == upstream {
				continue
			}
			s.log.Info("debugger: container replaced under a console session — asking the console to reconnect")
			s.closeClient(bridgeRestart, "service restart")
			closeUpstream(false)
			return
		case <-s.stale:
			s.log.Info("debugger: console session missed its keepalive — closed",
				zap.Duration("interval", bridgeKeepalive), zap.Int("missed", bridgeMissedPongs))
			s.closeClient(bridgeStale, "keepalive timeout")
			closeUpstream(false)
			return
		case <-s.ctx.Done():
			// The target was released: its context ended every read
			// already, and there is no connection left to say goodbye on.
			s.log.Debug("debugger: console session ended with the target")
			_ = s.client.CloseNow()
			closeUpstream(false)
			return
		}
	}
}

// keepalive pings the console every bridgeKeepalive on the target's clock. A
// ping still unanswered when the next is due counts as missed; the second
// consecutive miss closes the session. Pings wait for their pong on their
// own goroutine, so an unanswered one never stalls the count.
func (s *bridgeSession) keepalive() {
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		ticker := s.target.clk.Ticker(bridgeKeepalive)
		defer ticker.Stop()
		var pending atomic.Int32
		missed := 0
		for {
			select {
			case <-s.ctx.Done():
				return
			case <-ticker.C:
			}
			if pending.Load() > 0 {
				missed++
			} else {
				missed = 0
			}
			if missed >= bridgeMissedPongs {
				close(s.stale)
				return
			}
			pending.Add(1)
			s.wg.Add(1)
			go func() {
				defer s.wg.Done()
				defer pending.Add(-1)
				_ = s.client.Ping(s.ctx)
			}()
		}
	}()
}

// closeClient sends the console a close frame it can act on, then makes sure
// the connection is gone whether or not the console answered the handshake.
func (s *bridgeSession) closeClient(code websocket.StatusCode, reason string) {
	_ = s.client.Close(code, closeReason(reason))
	_ = s.client.CloseNow()
}

// closeReason fits a reason into a close frame: the limit is in bytes, the
// ellipsis is three of them, and the cut lands on a rune boundary, so the
// frame is valid UTF-8 and websocket.Close does not refuse it — which would
// leave the console with a 1006 and no reason at all.
func closeReason(reason string) string {
	if len(reason) <= closeReasonMax {
		return reason
	}
	const ellipsis = "…"
	cut := closeReasonMax - len(ellipsis)
	for cut > 0 && !utf8.RuneStart(reason[cut]) {
		cut--
	}
	return reason[:cut] + ellipsis
}
