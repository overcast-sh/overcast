package lambdadocker_test

// debugger_wait_test.go — overcast:debug-wait against a real Node.js container
// (docs/plans/compute-debugger-console.md § 6): an invocation of a function
// tagged to wait is held, after its container has started, until a client
// attaches to the proxied port, and runs anyway once
// OVERCAST_DEBUGGER_WAIT_TIMEOUT passes with nobody there.
//
// Same rules as the rest of this package: Docker-gated, never t.Parallel().

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/overcast-sh/overcast/internal/debugger"
	"github.com/overcast-sh/overcast/tests/helpers"
)

// createWaitDebuggedFunction is createDebuggedFunction with the wait tag.
func createWaitDebuggedFunction(t *testing.T, srv *helpers.TestServer, name string) {
	t.Helper()
	resp := doJSON(t, http.MethodPost, lambdaURL(srv, "/functions"), createFunctionReq{
		FunctionName: name,
		Runtime:      "nodejs20.x",
		Handler:      "index.handler",
		Role:         "arn:aws:iam::000000000000:role/lambda-role",
		Timeout:      debuggedFunctionTimeout,
		MemorySize:   128,
		Code:         &lambdaCode{ZipFile: makeZip(t, "index.js", sleepingHandler)},
		Tags:         map[string]string{debugger.TagDebug: "true", debugger.TagWait: "true"},
	})
	helpers.AssertStatus(t, resp, http.StatusCreated)
	resp.Body.Close()
	waitForFunctionActive(t, srv, name)
}

// invokeOutcome is one invocation's response and how long it took.
type invokeOutcome struct {
	resp    *http.Response
	elapsed time.Duration
}

// invokeInBackground starts one invocation and hands back where its outcome
// will land, so a test can watch what happens on the debug port meanwhile.
func invokeInBackground(t *testing.T, srv *helpers.TestServer, name string) <-chan invokeOutcome {
	t.Helper()
	done := make(chan invokeOutcome, 1)
	go func() {
		start := time.Now()
		resp := invokeFunction(t, srv, name, map[string]int{"sleepMs": 0})
		done <- invokeOutcome{resp: resp, elapsed: time.Since(start)}
	}()
	return done
}

// assertSucceeded reads the handler's own result off the response.
func assertSucceeded(t *testing.T, resp *http.Response) {
	t.Helper()
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	body, _ := io.ReadAll(resp.Body)
	if fe := resp.Header.Get("X-Amz-Function-Error"); fe != "" {
		t.Fatalf("invocation failed with %s: %s", fe, body)
	}
	var out struct {
		Slept int `json:"slept"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("response = %s (%v), want the handler's own result", body, err)
	}
}

func TestInvoke_debugger_waitHoldsTheInvocationUntilAClientAttaches(t *testing.T) {
	helpers.SkipWithoutDocker(t)
	requireLambdaInit(t)

	// Given: the Lambda debugger on, and a Node function tagged to wait for a
	// debugger — registered at create time, so the descriptor already says so
	// before any container exists
	srv := helpers.NewTestServer(t, helpers.WithLambdaDocker(), helpers.WithLambdaDebugger())
	createWaitDebuggedFunction(t, srv, "debug-wait-fn")
	if d := debugTarget(t, srv, "debug-wait-fn"); !d.WaitForDebugger || d.State != string(debugger.StateUnbound) {
		t.Fatalf("target before the first invoke = wait %v state %s, want waiting and unbound", d.WaitForDebugger, d.State)
	}

	// When: the function is invoked with nobody attached
	done := invokeInBackground(t, srv, "debug-wait-fn")

	// Then: its container starts and binds to the port, and the invocation
	// does not complete — far longer than a cold start and the handler take
	target := waitForTargetState(t, srv, "debug-wait-fn", debugger.StateListening)
	select {
	case out := <-done:
		body, _ := io.ReadAll(out.resp.Body)
		out.resp.Body.Close()
		t.Fatalf("the invocation completed after %s with no client attached: %s", out.elapsed, body)
	case <-time.After(8 * time.Second):
	}

	// When: a client attaches to the proxied port
	attachedAt := time.Now()
	holdDebugConnection(t, target.Listen)
	waitForTargetState(t, srv, "debug-wait-fn", debugger.StateAttached)

	// Then: the invocation completes, and only after the settle
	var out invokeOutcome
	select {
	case out = <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("the invocation never completed after a client attached")
	}
	if sinceAttach := time.Since(attachedAt); sinceAttach < debugger.SettleAfterAttach {
		t.Errorf("invocation completed %s after the attach, before the %s settle", sinceAttach, debugger.SettleAfterAttach)
	}
	assertSucceeded(t, out.resp)
}

func TestInvoke_debugger_waitTimeoutInvokesAnyway(t *testing.T) {
	helpers.SkipWithoutDocker(t)
	requireLambdaInit(t)

	// Given: the same function under a 5 s wait timeout
	const waitTimeout = 5 * time.Second
	srv := helpers.NewTestServer(t, helpers.WithLambdaDocker(), helpers.WithLambdaDebugger(),
		helpers.WithDebuggerWaitTimeout(waitTimeout))
	createWaitDebuggedFunction(t, srv, "debug-wait-expiry-fn")

	// When: it is invoked and nobody ever attaches
	start := time.Now()
	resp := invokeFunction(t, srv, "debug-wait-expiry-fn", map[string]int{"sleepMs": 0})
	if resp.Header.Get("X-Amz-Function-Error") == "Unhandled" {
		// The one transient cold-start failure the other invoke tests
		// tolerate under load; the retry is a fresh invocation with its own hold.
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if !strings.Contains(string(body), "Runtime.InitError") && !strings.Contains(string(body), "Runtime.ExitError") {
			t.Fatalf("invoke failed: %s", body)
		}
		start = time.Now()
		resp = invokeFunction(t, srv, "debug-wait-expiry-fn", map[string]int{"sleepMs": 0})
	}
	elapsed := time.Since(start)

	// Then: it ran after the hold expired — held at least the timeout, and
	// not timed out itself, since the function's clock starts at dispatch
	assertSucceeded(t, resp)
	if elapsed < waitTimeout {
		t.Errorf("invocation completed after %s, before the %s wait timeout", elapsed, waitTimeout)
	}
}
