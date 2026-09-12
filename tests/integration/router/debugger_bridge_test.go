package router_test

// debugger_bridge_test.go — the console's bridge route on the router
// (docs/plans/compute-debugger-console.md § 3.1) without a container: an
// untagged function has no target to bridge to, which is a JSON 404 before
// any upgrade, and the descriptor of one carries no bridge path or console
// flag for the console to act on.

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/overcast-sh/overcast/internal/debugger"
	"github.com/overcast-sh/overcast/tests/helpers"
)

func TestDebuggerBridge_untaggedFunctionHasNoTargetToBridge(t *testing.T) {
	// Given: a function no tag mentions
	srv := helpers.NewTestServer(t)
	createDebuggerTestFunction(t, srv, "plain-fn")

	// Then: its synthesised descriptor offers no console session
	var d debugger.Descriptor
	getDebuggerJSON(t, srv, "/_overcast/debugger/targets/lambda/plain-fn", &d)
	if d.ConsoleDebug || d.BridgePath != "" {
		t.Errorf("descriptor = %+v, want no consoleDebug and no bridgePath for an untagged function", d)
	}

	// When: the console opens the bridge for it anyway
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/_overcast/debugger/targets/lambda/plain-fn/ws"
	_, resp, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{
		HTTPHeader: http.Header{"Origin": {"http://localhost:4567"}},
	})

	// Then: 404 JSON naming the target, no upgrade
	if err == nil {
		t.Fatal("dial succeeded with no target registered")
	}
	if resp == nil {
		t.Fatalf("no response: %v", err)
	}
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusNotFound)
	if got := resp.Header.Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", got)
	}
	var body map[string]string
	helpers.DecodeJSON(t, resp, &body)
	if !strings.Contains(body["error"], "lambda/plain-fn") {
		t.Errorf("error = %q, want it to name lambda/plain-fn", body["error"])
	}
}
