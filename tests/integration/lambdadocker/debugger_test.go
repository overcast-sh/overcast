package lambdadocker_test

// debugger_test.go — the compute debugger against a real Node.js container
// (docs/plans/compute-debugger.md § 9): the inspector answers through
// Overcast's proxy port, and an attached client stops the invocation clock
// under the default policy while the strict policy keeps AWS's timeout.
//
// Same rules as the rest of this package: Docker-gated, never t.Parallel().

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/overcast-sh/overcast/internal/config"
	"github.com/overcast-sh/overcast/internal/debugger"
	"github.com/overcast-sh/overcast/tests/helpers"
)

// debuggedFunctionTimeout is the function's configured timeout: short, so a
// handler that outlives it proves the clock stopped rather than the test
// being patient.
const debuggedFunctionTimeout = 3

// sleepingHandler sleeps for event.sleepMs and answers.
const sleepingHandler = `
exports.handler = async (event) => {
  await new Promise(r => setTimeout(r, event.sleepMs || 0));
  return { slept: event.sleepMs || 0 };
};
`

// createDebuggedFunction deploys the sleeping handler tagged for debugging and
// waits for it to become Active.
func createDebuggedFunction(t *testing.T, srv *helpers.TestServer, name string) {
	t.Helper()
	resp := doJSON(t, http.MethodPost, lambdaURL(srv, "/functions"), createFunctionReq{
		FunctionName: name,
		Runtime:      "nodejs20.x",
		Handler:      "index.handler",
		Role:         "arn:aws:iam::000000000000:role/lambda-role",
		Timeout:      debuggedFunctionTimeout,
		MemorySize:   128,
		Code:         &lambdaCode{ZipFile: makeZip(t, "index.js", sleepingHandler)},
		Tags:         map[string]string{debugger.TagDebug: "true"},
	})
	helpers.AssertStatus(t, resp, http.StatusCreated)
	resp.Body.Close()
	waitForFunctionActive(t, srv, name)
}

// debugTarget reads the function's descriptor from the debugger endpoint.
func debugTarget(t *testing.T, srv *helpers.TestServer, name string) debugger.Descriptor {
	t.Helper()
	resp, err := http.Get(srv.URL + "/_overcast/debugger/targets/lambda/" + name)
	if err != nil {
		t.Fatalf("get target: %v", err)
	}
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	var d debugger.Descriptor
	helpers.DecodeJSON(t, resp, &d)
	return d
}

// waitForTargetState polls the descriptor until the target reports state.
func waitForTargetState(t *testing.T, srv *helpers.TestServer, name string, state debugger.State) debugger.Descriptor {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	var d debugger.Descriptor
	for time.Now().Before(deadline) {
		if d = debugTarget(t, srv, name); d.State == string(state) {
			return d
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("target never reached state %q: %+v", state, d)
	return d
}

// invokeAndWarm runs one quick invocation so the container exists and the
// target is bound, retrying the one transient cold-start failure the other
// invoke tests tolerate under load.
func invokeAndWarm(t *testing.T, srv *helpers.TestServer, name string) {
	t.Helper()
	resp := invokeFunction(t, srv, name, map[string]int{"sleepMs": 0})
	if resp.Header.Get("X-Amz-Function-Error") == "Unhandled" {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if !strings.Contains(string(body), "Runtime.InitError") && !strings.Contains(string(body), "Runtime.ExitError") {
			t.Fatalf("warm-up invoke failed: %s", body)
		}
		resp = invokeFunction(t, srv, name, map[string]int{"sleepMs": 0})
	}
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	if fe := resp.Header.Get("X-Amz-Function-Error"); fe != "" {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("warm-up invoke failed with %s: %s", fe, body)
	}
}

// holdDebugConnection opens a raw TCP connection to the target's port and
// keeps it open, which is what an editor does before its first message and
// what the proxy counts as attached.
func holdDebugConnection(t *testing.T, listen debugger.Listen) net.Conn {
	t.Helper()
	conn, err := net.DialTimeout("tcp", net.JoinHostPort(listen.Host, strconv.Itoa(listen.Port)), 5*time.Second)
	if err != nil {
		t.Fatalf("dial debug port %d: %v", listen.Port, err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func TestInvoke_debugger_inspectorAnswersThroughTheProxy(t *testing.T) {
	helpers.SkipWithoutDocker(t)
	requireLambdaInit(t)

	// Given: the Lambda debugger on, and a Node function tagged for it that
	// has run once — its container is up and bound to the target
	srv := helpers.NewTestServer(t, helpers.WithLambdaDocker(), helpers.WithLambdaDebugger())
	createDebuggedFunction(t, srv, "debug-inspect-fn")
	invokeAndWarm(t, srv, "debug-inspect-fn")
	target := waitForTargetState(t, srv, "debug-inspect-fn", debugger.StateListening)

	// Then: the target resolved to the inspector on a loopback port, with the
	// roots an editor needs
	if !target.Enabled || target.Protocol != "inspector" || target.Listen.Port == 0 {
		t.Fatalf("target = %+v, want an enabled inspector target on a port", target)
	}
	if target.RemoteRoot != "/var/task" || target.Upstream == "" || target.ContainerID == "" {
		t.Errorf("target = %+v, want remoteRoot /var/task with an upstream and a container", target)
	}

	// When: the inspector's discovery endpoint is read through the proxy
	resp, err := http.Get("http://" + net.JoinHostPort(target.Listen.Host, strconv.Itoa(target.Listen.Port)) + "/json/list")
	if err != nil {
		t.Fatalf("GET /json/list through the proxy: %v", err)
	}
	defer resp.Body.Close()

	// Then: Node's inspector answers, naming a WebSocket URL on the same port
	// number the editor asked for
	helpers.AssertStatus(t, resp, http.StatusOK)
	var sessions []struct {
		WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
	}
	helpers.DecodeJSON(t, resp, &sessions)
	if len(sessions) == 0 || sessions[0].WebSocketDebuggerURL == "" {
		t.Fatalf("/json/list = %+v, want at least one session with a webSocketDebuggerUrl", sessions)
	}
	if !strings.Contains(sessions[0].WebSocketDebuggerURL, ":"+strconv.Itoa(target.Listen.Port)+"/") {
		t.Errorf("webSocketDebuggerUrl = %q, want it on port %d", sessions[0].WebSocketDebuggerURL, target.Listen.Port)
	}
}

func TestInvoke_debugger_attachedClientSuspendsTheTimeout(t *testing.T) {
	helpers.SkipWithoutDocker(t)
	requireLambdaInit(t)

	// Given: a debugged function whose container is up, and a client holding
	// a connection to its debug port
	srv := helpers.NewTestServer(t, helpers.WithLambdaDocker(), helpers.WithLambdaDebugger())
	createDebuggedFunction(t, srv, "debug-attached-fn")
	invokeAndWarm(t, srv, "debug-attached-fn")
	target := waitForTargetState(t, srv, "debug-attached-fn", debugger.StateListening)
	holdDebugConnection(t, target.Listen)
	waitForTargetState(t, srv, "debug-attached-fn", debugger.StateAttached)

	// When: the handler sleeps twice the function's timeout
	sleepMs := 2 * debuggedFunctionTimeout * 1000
	start := time.Now()
	resp := invokeFunction(t, srv, "debug-attached-fn", map[string]int{"sleepMs": sleepMs})
	defer resp.Body.Close()
	elapsed := time.Since(start)

	// Then: it completes — the clock stopped while the client was attached
	helpers.AssertStatus(t, resp, http.StatusOK)
	body, _ := io.ReadAll(resp.Body)
	if fe := resp.Header.Get("X-Amz-Function-Error"); fe != "" {
		t.Fatalf("invocation failed with %s after %s: %s", fe, elapsed, body)
	}
	var out struct {
		Slept int `json:"slept"`
	}
	if err := json.Unmarshal(body, &out); err != nil || out.Slept != sleepMs {
		t.Fatalf("response = %s (%v), want the handler's own result", body, err)
	}
	if elapsed < time.Duration(sleepMs)*time.Millisecond {
		t.Errorf("invoke returned after %s, before the handler could have finished sleeping %dms", elapsed, sleepMs)
	}
}

func TestInvoke_debugger_strictPolicyKeepsTheTimeout(t *testing.T) {
	helpers.SkipWithoutDocker(t)
	requireLambdaInit(t)

	// Given: the same attached client under OVERCAST_DEBUGGER_TIMEOUT=strict
	srv := helpers.NewTestServer(t, helpers.WithLambdaDocker(), helpers.WithLambdaDebugger(),
		helpers.WithDebuggerTimeout(config.DebuggerTimeoutStrict))
	createDebuggedFunction(t, srv, "debug-strict-fn")
	invokeAndWarm(t, srv, "debug-strict-fn")
	target := waitForTargetState(t, srv, "debug-strict-fn", debugger.StateListening)
	if target.TimeoutPolicy != string(config.DebuggerTimeoutStrict) {
		t.Fatalf("timeoutPolicy = %q, want strict", target.TimeoutPolicy)
	}
	holdDebugConnection(t, target.Listen)
	waitForTargetState(t, srv, "debug-strict-fn", debugger.StateAttached)

	// When: the handler sleeps twice the function's timeout
	resp := invokeFunction(t, srv, "debug-strict-fn", map[string]int{"sleepMs": 2 * debuggedFunctionTimeout * 1000})
	defer resp.Body.Close()

	// Then: it times out exactly as on AWS, attached client or not
	helpers.AssertStatus(t, resp, http.StatusOK)
	body, _ := io.ReadAll(resp.Body)
	if fe := resp.Header.Get("X-Amz-Function-Error"); fe != "Unhandled" {
		t.Fatalf("X-Amz-Function-Error = %q, want Unhandled: %s", fe, body)
	}
	if !strings.Contains(string(body), "Task timed out after 3.00 seconds") {
		t.Errorf("body = %s, want AWS's timeout message", body)
	}
}
