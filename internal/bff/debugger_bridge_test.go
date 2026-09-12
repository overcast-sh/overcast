package bff

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// The console's debugger session is a WebSocket the BFF has to pass through
// as an upgrade (docs/plans/compute-debugger-console.md § 3.1): frames both
// ways, and the emulator's close code — which the console keys on — as sent.
// The endpoint has to come from the query, since a browser cannot set headers
// on a WebSocket handshake, and the browser's Host and Origin have to reach
// the emulator untouched for its origin check to see the console's own origin.
func TestDebuggerBridgeProxiesTheUpgrade(t *testing.T) {
	type handshake struct{ path, query, host, origin string }
	seen := make(chan handshake, 1)
	_, restore := stubEmulatorForProxyTests(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/_overcast/debugger/targets/lambda/my fn/ws" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"lambda/my fn","consoleDebug":true}`))
			return
		}
		seen <- handshake{path: r.URL.Path, query: r.URL.RawQuery, host: r.Host, origin: r.Header.Get("Origin")}
		c, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			return
		}
		defer c.CloseNow()
		typ, msg, err := c.Read(r.Context())
		if err != nil {
			return
		}
		_ = c.Write(r.Context(), typ, msg)
		_ = c.Close(websocket.StatusServiceRestart, "service restart")
	}))
	defer restore()
	bff := httptest.NewServer(NewHandler(nil, nil, UIConfig{}))
	defer bff.Close()

	// When: the console opens a session through the BFF
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(ctx, "ws://"+bff.Listener.Addr().String()+"/api/debugger/targets/lambda/my%20fn/ws?container=app",
		&websocket.DialOptions{HTTPHeader: http.Header{"Origin": {"http://localhost:4567"}}})
	if err != nil {
		t.Fatalf("dial through the BFF: %v", err)
	}
	defer c.CloseNow()

	// Then: the handshake reached the emulator on the bridge path, with the
	// container query, the browser's Host and its Origin
	var hs handshake
	select {
	case hs = <-seen:
	case <-time.After(5 * time.Second):
		t.Fatal("the emulator never saw the handshake")
	}
	if hs.path != "/_overcast/debugger/targets/lambda/my fn/ws" || hs.query != "container=app" {
		t.Errorf("upstream path %q query %q", hs.path, hs.query)
	}
	if want := bff.Listener.Addr().String(); hs.host != want {
		t.Errorf("upstream Host = %q, want the browser's %q preserved", hs.host, want)
	}
	if hs.origin != "http://localhost:4567" {
		t.Errorf("upstream Origin = %q, want the browser's", hs.origin)
	}

	// And: frames flow both ways
	if err := c.Write(ctx, websocket.MessageText, []byte(`{"id":1}`)); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, echoed, err := c.Read(ctx)
	if err != nil || string(echoed) != `{"id":1}` {
		t.Fatalf("read = %q, %v; want the echo", echoed, err)
	}

	// And: the emulator's close code arrives as sent
	_, _, err = c.Read(ctx)
	if got := websocket.CloseStatus(err); got != websocket.StatusServiceRestart {
		t.Errorf("close status = %v (%v), want 1012", got, err)
	}

	// And: a plain GET beside it still answers
	resp, err := http.Get(bff.URL + "/api/debugger/targets/lambda/my%20fn")
	if err != nil {
		t.Fatalf("plain GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("plain GET status = %d, want 200", resp.StatusCode)
	}
}

// With the emulator down, the handshake fails as the other proxies do — a
// JSON 502 — rather than hanging or panicking in the proxy.
func TestDebuggerBridgeReportsAnUnreachableEmulator(t *testing.T) {
	emulator, restore := stubEmulatorForProxyTests(t, http.NotFoundHandler())
	emulator.Close()
	defer restore()
	bff := httptest.NewServer(NewHandler(nil, nil, UIConfig{}))
	defer bff.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, resp, err := websocket.Dial(ctx, "ws://"+bff.Listener.Addr().String()+"/api/debugger/targets/lambda/fn/ws", nil)
	if err == nil {
		t.Fatal("dial succeeded with no emulator behind the BFF")
	}
	if resp == nil || resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("response = %v (%v), want a 502", resp, err)
	}
}
