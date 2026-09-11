package debugger

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/coder/websocket"
	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/overcast-sh/overcast/internal/clock"
	"github.com/overcast-sh/overcast/internal/config"
)

// fakeInspector is a container's inspector port: /json/list names a session
// on a host the bridge has to rewrite, and the session echoes every message,
// answering Debugger.pause and Debugger.resume with the notifications Node
// would send. It returns the address the target's upstream should be set to.
func fakeInspector(t *testing.T) string {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/json/list", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `[{"description":"node.js instance","webSocketDebuggerUrl":"ws://0.0.0.0:1/devtools/page/abc"}]`)
	})
	mux.HandleFunc("/devtools/page/abc", func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			return
		}
		defer c.CloseNow()
		ctx := r.Context()
		for {
			typ, msg, err := c.Read(ctx)
			if err != nil {
				return
			}
			var m struct {
				Method string `json:"method"`
			}
			_ = json.Unmarshal(msg, &m)
			reply := msg
			switch m.Method {
			case "Debugger.pause":
				reply = []byte(pausedMsg)
			case "Debugger.resume":
				reply = []byte(resumedMsg)
			}
			if err := c.Write(ctx, typ, reply); err != nil {
				return
			}
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv.Listener.Addr().String()
}

// bridgeServer mounts the bridge route as the router does.
func bridgeServer(t *testing.T, m *Manager) *httptest.Server {
	t.Helper()
	h := NewHandler(m, nil)
	r := chi.NewRouter()
	r.Get("/_overcast/debugger/targets/{service}/{resource}/ws", h.Bridge)
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	return srv
}

const consoleOrigin = "http://localhost:4567"

// dialBridge opens a console session with the given Origin.
func dialBridge(t *testing.T, srv *httptest.Server, path, origin string, opts *websocket.DialOptions) (*websocket.Conn, *http.Response, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if opts == nil {
		opts = &websocket.DialOptions{}
	}
	opts.HTTPHeader = http.Header{"Origin": {origin}}
	c, resp, err := websocket.Dial(ctx, "ws://"+srv.Listener.Addr().String()+path, opts)
	if c != nil {
		t.Cleanup(func() { _ = c.CloseNow() })
	}
	return c, resp, err
}

func mustDialBridge(t *testing.T, srv *httptest.Server, path string) *websocket.Conn {
	t.Helper()
	c, _, err := dialBridge(t, srv, path, consoleOrigin, nil)
	require.NoError(t, err)
	return c
}

func wsRoundTrip(t *testing.T, c *websocket.Conn, msg string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t, c.Write(ctx, websocket.MessageText, []byte(msg)))
	_, got, err := c.Read(ctx)
	require.NoError(t, err)
	return string(got)
}

// readClose reads until the server closes and returns the close frame.
func readClose(t *testing.T, c *websocket.Conn) websocket.CloseError {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for {
		_, _, err := c.Read(ctx)
		if err == nil {
			continue
		}
		var ce websocket.CloseError
		require.ErrorAs(t, err, &ce, "expected a close frame, got %v", err)
		return ce
	}
}

func TestBridge_inspectorSessionRelaysCountsAndObserves(t *testing.T) {
	// Given: an inspector target bound to a container whose /json/list names
	// its session on a host only it can reach
	m := newTestManager(t, clock.NewMock(), config.DebuggerTimeoutAttached)
	tgt := boundTarget(t, m, "lambda/fn", inspector{})
	tgt.SetUpstream(fakeInspector(t))
	ch := events(tgt)
	srv := bridgeServer(t, m)

	// Then: the descriptor tells the console it can debug here, and where
	d := tgt.Descriptor()
	assert.True(t, d.ConsoleDebug)
	assert.Equal(t, "/_overcast/debugger/targets/lambda/fn/ws", d.BridgePath)

	// When: the console opens the bridge and sends a command
	c := mustDialBridge(t, srv, d.BridgePath)
	got := wsRoundTrip(t, c, `{"id":1,"method":"Runtime.enable"}`)

	// Then: the container answered through the rewritten session URL, and
	// the session counts as one attached client
	assert.Equal(t, `{"id":1,"method":"Runtime.enable"}`, got)
	expectEvent(t, ch, EventAttach)
	assert.Equal(t, StateAttached, tgt.State())

	// When: the program pauses and resumes, as the container reports it
	assert.Equal(t, pausedMsg, wsRoundTrip(t, c, `{"id":2,"method":"Debugger.pause"}`))
	expectEvent(t, ch, EventPause)
	assert.Equal(t, StatePaused, tgt.State())
	assert.Equal(t, resumedMsg, wsRoundTrip(t, c, `{"id":3,"method":"Debugger.resume"}`))
	expectEvent(t, ch, EventResume)
	assert.Equal(t, StateAttached, tgt.State())

	// When: the console closes the session
	require.NoError(t, c.Close(websocket.StatusNormalClosure, ""))

	// Then: it detaches and the target is listening again
	expectEvent(t, ch, EventDetach)
	assert.Eventually(t, func() bool { return tgt.State() == StateListening }, 5*time.Second, 5*time.Millisecond)
}

func TestBridge_otherProtocolsAreSplicedAsBytes(t *testing.T) {
	// Given: a passthrough target bound to a TCP echo "container"
	m := newTestManager(t, clock.NewMock(), config.DebuggerTimeoutAttached)
	tgt := boundTarget(t, m, "lambda/fn", passthrough{})
	tgt.SetUpstream(echoServer(t, "echo:"))
	ch := events(tgt)
	srv := bridgeServer(t, m)
	assert.False(t, tgt.Descriptor().ConsoleDebug, "the console has no client for passthrough")

	// When: the console opens the bridge and streams bytes over binary frames
	c := mustDialBridge(t, srv, tgt.Descriptor().BridgePath)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	stream := websocket.NetConn(ctx, c, websocket.MessageBinary)
	_, err := io.WriteString(stream, "hello\n")
	require.NoError(t, err)
	buf := make([]byte, 64)
	n, err := stream.Read(buf)
	require.NoError(t, err)

	// Then: the container's bytes come back, and the session counted
	assert.Equal(t, "echo:hello\n", string(buf[:n]))
	expectEvent(t, ch, EventAttach)

	// When: the console leaves
	require.NoError(t, stream.Close())

	// Then: it detaches
	expectEvent(t, ch, EventDetach)
}

func TestBridge_noContainerCloses1011(t *testing.T) {
	// Given: a listening target with nothing behind it yet
	m := newTestManager(t, clock.NewMock(), config.DebuggerTimeoutAttached)
	tgt := boundTarget(t, m, "lambda/fn", inspector{})
	ch := events(tgt)
	srv := bridgeServer(t, m)

	// When: the console opens the bridge
	c := mustDialBridge(t, srv, tgt.Descriptor().BridgePath)
	ce := readClose(t, c)

	// Then: it is closed with the code the console keys on, and nothing
	// counted as attached
	assert.Equal(t, websocket.StatusInternalError, ce.Code)
	assert.Equal(t, "no container", ce.Reason)
	select {
	case ev := <-ch:
		t.Fatalf("unexpected event %s", ev)
	default:
	}
	assert.Equal(t, StateUnbound, tgt.State())
}

func TestBridge_inertTargetCloses1011WithTheReason(t *testing.T) {
	// Given: a tagged target the server flag keeps off
	m := newTestManager(t, clock.NewMock(), config.DebuggerTimeoutAttached)
	tgt, err := m.Ensure("lambda/fn", Spec{Service: ServiceLambda, Tagged: true}, Resolution{Protocol: inspector{}, Source: SourceRuntime})
	require.NoError(t, err)
	srv := bridgeServer(t, m)

	// When: the console opens the bridge anyway
	c := mustDialBridge(t, srv, tgt.Descriptor().BridgePath)
	ce := readClose(t, c)

	// Then: the close says why there is nothing to attach to
	assert.Equal(t, websocket.StatusInternalError, ce.Code)
	assert.Contains(t, ce.Reason, "not listening")
	assert.Contains(t, ce.Reason, "OVERCAST_LAMBDA_DEBUGGER=true")
}

func TestBridge_containerReplacedCloses1012(t *testing.T) {
	// Given: a console session on one container
	m := newTestManager(t, clock.NewMock(), config.DebuggerTimeoutAttached)
	tgt := boundTarget(t, m, "lambda/fn", inspector{})
	tgt.SetUpstream(fakeInspector(t))
	ch := events(tgt)
	srv := bridgeServer(t, m)
	c := mustDialBridge(t, srv, tgt.Descriptor().BridgePath)
	assert.Equal(t, `{"id":1}`, wsRoundTrip(t, c, `{"id":1}`))
	expectEvent(t, ch, EventAttach)

	// When: hot reload binds a replacement container
	tgt.SetUpstream(fakeInspector(t))

	// Then: the session is closed with the code that tells the console to
	// reconnect, and the target no longer counts it
	ce := readClose(t, c)
	assert.Equal(t, websocket.StatusServiceRestart, ce.Code)
	assert.Equal(t, "service restart", ce.Reason)
	expectEvent(t, ch, EventDetach)

	// And: a new session reaches the replacement
	again := mustDialBridge(t, srv, tgt.Descriptor().BridgePath)
	assert.Equal(t, `{"id":2}`, wsRoundTrip(t, again, `{"id":2}`))
}

func TestBridge_containerGoneCloses1011(t *testing.T) {
	// Given: a console session on a container
	m := newTestManager(t, clock.NewMock(), config.DebuggerTimeoutAttached)
	tgt := boundTarget(t, m, "lambda/fn", inspector{})
	tgt.SetUpstream(fakeInspector(t))
	tgt.SetContainerID("c1")
	ch := events(tgt)
	srv := bridgeServer(t, m)
	c := mustDialBridge(t, srv, tgt.Descriptor().BridgePath)
	assert.Equal(t, `{"id":1}`, wsRoundTrip(t, c, `{"id":1}`))
	expectEvent(t, ch, EventAttach)

	// When: the container goes away and the service clears it
	tgt.ClearContainer("c1")

	// Then: the console is told to reconnect — its next attempt is the
	// "no container" it can wait on
	ce := readClose(t, c)
	assert.Equal(t, websocket.StatusServiceRestart, ce.Code)
	expectEvent(t, ch, EventDetach)
}

func TestBridge_originIsChecked(t *testing.T) {
	// Given: a bound inspector target
	m := newTestManager(t, clock.NewMock(), config.DebuggerTimeoutAttached)
	tgt := boundTarget(t, m, "lambda/fn", inspector{})
	tgt.SetUpstream(fakeInspector(t))
	srv := bridgeServer(t, m)
	path := tgt.Descriptor().BridgePath

	t.Run("another site is refused", func(t *testing.T) {
		// When: a page on some other origin opens the bridge
		_, resp, err := dialBridge(t, srv, path, "https://evil.example", nil)

		// Then: 403, before any upgrade
		require.Error(t, err)
		require.NotNil(t, resp)
		assert.Equal(t, http.StatusForbidden, resp.StatusCode)
		assert.False(t, tgt.Attached())
	})

	for _, origin := range []string{
		"http://localhost:5173",                  // the Vite dev server
		"http://127.0.0.1:4580",                  // a published container port
		"http://" + srv.Listener.Addr().String(), // the request's own host
	} {
		t.Run(origin+" is accepted", func(t *testing.T) {
			// When: the console on a loopback port, or on the request's own
			// host, opens the bridge
			c, _, err := dialBridge(t, srv, path, origin, nil)

			// Then: it is a session
			require.NoError(t, err)
			assert.Equal(t, `{"id":1}`, wsRoundTrip(t, c, `{"id":1}`))
		})
	}
}

func TestBridge_keepaliveClosesAfterTwoMissedPongs(t *testing.T) {
	// Given: a console session whose client never answers a ping
	clk := clock.NewMock()
	m := newTestManager(t, clk, config.DebuggerTimeoutAttached)
	tgt := boundTarget(t, m, "lambda/fn", inspector{})
	tgt.SetUpstream(fakeInspector(t))
	ch := events(tgt)
	srv := bridgeServer(t, m)
	silent := &websocket.DialOptions{OnPingReceived: func(context.Context, []byte) bool { return false }}
	c, _, err := dialBridge(t, srv, tgt.Descriptor().BridgePath, consoleOrigin, silent)
	require.NoError(t, err)
	expectEvent(t, ch, EventAttach)
	closed := make(chan error, 1)
	go func() {
		for {
			if _, _, err := c.Read(context.Background()); err != nil {
				closed <- err
				return
			}
		}
	}()

	// When: keepalive intervals pass with no pong
	assert.Eventually(t, func() bool {
		clk.Add(bridgeKeepalive)
		return !tgt.Attached()
	}, 10*time.Second, 20*time.Millisecond)

	// Then: the session was closed as gone away, after the second miss
	select {
	case err := <-closed:
		assert.Equal(t, websocket.StatusGoingAway, websocket.CloseStatus(err))
	case <-time.After(5 * time.Second):
		t.Fatal("client never saw the close")
	}
	expectEvent(t, ch, EventDetach)
}

func TestBridge_keepaliveKeepsAnAnsweringConsole(t *testing.T) {
	// Given: a console session whose client answers pings, as browsers do
	clk := clock.NewMock()
	m := newTestManager(t, clk, config.DebuggerTimeoutAttached)
	tgt := boundTarget(t, m, "lambda/fn", inspector{})
	tgt.SetUpstream(fakeInspector(t))
	srv := bridgeServer(t, m)
	c := mustDialBridge(t, srv, tgt.Descriptor().BridgePath)
	assert.Equal(t, `{"id":1}`, wsRoundTrip(t, c, `{"id":1}`))
	ctx := c.CloseRead(context.Background()) // answers pings in the background

	// When: many keepalive intervals pass
	for range 5 {
		clk.Add(bridgeKeepalive)
		time.Sleep(20 * time.Millisecond)
	}

	// Then: the session is still open
	assert.NoError(t, ctx.Err())
	assert.True(t, tgt.Attached())
}

func TestBridge_releaseEndsSessions(t *testing.T) {
	// Given: a console session
	m := newTestManager(t, clock.NewMock(), config.DebuggerTimeoutAttached)
	tgt := boundTarget(t, m, "lambda/fn", inspector{})
	tgt.SetUpstream(fakeInspector(t))
	srv := bridgeServer(t, m)
	c := mustDialBridge(t, srv, tgt.Descriptor().BridgePath)
	assert.Equal(t, `{"id":1}`, wsRoundTrip(t, c, `{"id":1}`))

	// When: the target is released
	released := make(chan struct{})
	go func() {
		m.Release("lambda/fn")
		close(released)
	}()

	// Then: the release completes — every session goroutine exited — and
	// the console's connection is gone
	select {
	case <-released:
	case <-time.After(10 * time.Second):
		t.Fatal("Release did not return: a bridge goroutine is still counted")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _, err := c.Read(ctx)
	assert.Error(t, err)
	assert.False(t, errors.Is(err, context.DeadlineExceeded), "connection still open after release")
}

func TestHandler_bridgeUnknownTargetIs404(t *testing.T) {
	// Given: no target for the resource
	m := newTestManager(t, clock.NewMock(), config.DebuggerTimeoutAttached)
	srv := bridgeServer(t, m)

	// When: the bridge is opened for it
	_, resp, err := dialBridge(t, srv, "/_overcast/debugger/targets/lambda/ghost/ws", consoleOrigin, nil)

	// Then: 404 with a JSON error, before any upgrade
	require.Error(t, err)
	require.NotNil(t, resp)
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
	body, _ := io.ReadAll(resp.Body)
	assert.Contains(t, string(body), "lambda/ghost")
}

func TestBridgePath(t *testing.T) {
	assert.Equal(t, "/_overcast/debugger/targets/lambda/my%20fn/ws", BridgePath(ServiceLambda, "my fn", ""))
	assert.Equal(t, "/_overcast/debugger/targets/ecs/task-1/ws?container=app+1", BridgePath(ServiceECS, "task-1", "app 1"))
	assert.Equal(t, "", UntaggedDescriptor(ServiceLambda, "fn", "", "").BridgePath)
	assert.False(t, strings.Contains(BridgePath(ServiceLambda, "a/b", ""), "a/b"), "a slash in a resource is escaped")
}

func TestCloseReason_fitsAFrameInBytesOnARuneBoundary(t *testing.T) {
	// Given: a reason longer than a close frame holds, ending in multi-byte
	// runes right where the cut lands
	long := strings.Repeat("a", closeReasonMax-4) + "éééé"
	require.Greater(t, len(long), closeReasonMax)

	// When: it is fitted
	got := closeReason(long)

	// Then: it is within the byte limit websocket.Close enforces, valid
	// UTF-8, and says it was cut
	assert.LessOrEqual(t, len(got), closeReasonMax)
	assert.True(t, utf8.ValidString(got), "cut through a rune: %q", got)
	assert.True(t, strings.HasSuffix(got, "…"))
	assert.Equal(t, "short", closeReason("short"))

	// And: a target whose reason is long still closes with the code and a
	// reason the console can read, rather than an abnormal closure
	m := newTestManager(t, clock.NewMock(), config.DebuggerTimeoutAttached)
	tgt, err := m.Ensure("lambda/fn", Spec{Service: ServiceLambda, Tagged: true}, Resolution{Protocol: inspector{}, Source: SourceRuntime})
	require.NoError(t, err)
	tgt.mu.Lock()
	tgt.reason = strings.Repeat("the port is held by something else; ", 6)
	tgt.mu.Unlock()
	srv := bridgeServer(t, m)
	c := mustDialBridge(t, srv, tgt.Descriptor().BridgePath)
	ce := readClose(t, c)
	assert.Equal(t, websocket.StatusInternalError, ce.Code)
	assert.True(t, strings.HasPrefix(ce.Reason, "not listening: the port is held"), ce.Reason)
}
