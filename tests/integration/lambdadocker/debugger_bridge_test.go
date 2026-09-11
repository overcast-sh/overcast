package lambdadocker_test

// debugger_bridge_test.go — the console's WebSocket bridge against a real
// Node.js container (docs/plans/compute-debugger-console.md § 3.1): opened
// through the router at the descriptor's bridgePath, it carries CDP both
// ways, and the session counts as an attached client.
//
// Same rules as the rest of this package: Docker-gated, never t.Parallel().

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/overcast-sh/overcast/internal/debugger"
	"github.com/overcast-sh/overcast/tests/helpers"
)

func TestInvoke_debugger_consoleBridgeSpeaksCDPToTheContainer(t *testing.T) {
	helpers.SkipWithoutDocker(t)
	requireLambdaInit(t)

	// Given: a debugged Node function whose container is up, and a
	// descriptor that says the console can debug it and where
	srv := helpers.NewTestServer(t, helpers.WithLambdaDocker(), helpers.WithLambdaDebugger())
	createDebuggedFunction(t, srv, "debug-bridge-fn")
	invokeAndWarm(t, srv, "debug-bridge-fn")
	target := waitForTargetState(t, srv, "debug-bridge-fn", debugger.StateListening)
	if !target.ConsoleDebug || target.BridgePath != "/_overcast/debugger/targets/lambda/debug-bridge-fn/ws" {
		t.Fatalf("target = %+v, want consoleDebug with the bridge path", target)
	}

	// When: the console opens the bridge through the router and enables the
	// debugger domain, as its CDP client does first
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + target.BridgePath
	conn, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{
		HTTPHeader: http.Header{"Origin": {"http://localhost:4567"}},
	})
	if err != nil {
		t.Fatalf("dial %s: %v", wsURL, err)
	}
	defer conn.CloseNow()
	conn.SetReadLimit(-1)
	if err := conn.Write(ctx, websocket.MessageText, []byte(`{"id":1,"method":"Debugger.enable"}`)); err != nil {
		t.Fatalf("write Debugger.enable: %v", err)
	}

	// Then: the inspector answers with the scripts it has parsed — the
	// handler among them, by its path under /var/task — and the enable's
	// own result
	var sawHandler, sawResult bool
	for !(sawHandler && sawResult) {
		_, msg, err := conn.Read(ctx)
		if err != nil {
			t.Fatalf("read from the bridge (handler seen: %v, result seen: %v): %v", sawHandler, sawResult, err)
		}
		var m struct {
			ID     int    `json:"id"`
			Method string `json:"method"`
			Params struct {
				URL string `json:"url"`
			} `json:"params"`
		}
		if err := json.Unmarshal(msg, &m); err != nil {
			t.Fatalf("not a CDP message: %s", msg)
		}
		switch {
		case m.Method == "Debugger.scriptParsed" && m.Params.URL == "file:///var/task/index.js":
			sawHandler = true
		case m.ID == 1:
			sawResult = true
		}
	}

	// And: the session is an attached client of the target
	waitForTargetState(t, srv, "debug-bridge-fn", debugger.StateAttached)

	// When: the console closes the session
	if err := conn.Close(websocket.StatusNormalClosure, ""); err != nil {
		t.Fatalf("close: %v", err)
	}

	// Then: the target is listening again
	waitForTargetState(t, srv, "debug-bridge-fn", debugger.StateListening)
}
