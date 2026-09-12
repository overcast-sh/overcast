package lambda

// debugger_wait_test.go — the Phase F backend contracts of
// docs/plans/compute-debugger-console.md § 6: an invocation of a function
// tagged overcast:debug-wait=true is held for a client before its event is
// dispatched, and the debug target follows the function's record — create,
// tag, untag, update, the store scan behind the first target list — rather
// than waiting for a cold start.

import (
	"context"
	"testing"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"

	"github.com/overcast-sh/overcast/internal/clock"
	"github.com/overcast-sh/overcast/internal/config"
	"github.com/overcast-sh/overcast/internal/debugger"
	"github.com/overcast-sh/overcast/internal/debugger/debuggertest"
	"github.com/overcast-sh/overcast/internal/middleware"
	"github.com/overcast-sh/overcast/internal/serviceutil"
	"github.com/overcast-sh/overcast/internal/state"
)

// waitTaggedNodeFunction is debugTaggedNodeFunction plus the wait tag.
func waitTaggedNodeFunction(name string) *Function {
	fn := debugTaggedNodeFunction(name)
	fn.Tags[debugger.TagWait] = "true"
	return fn
}

// newDebugHandler builds a handler over a memory store with the Lambda
// debugger on, its own manager on a free port range, and a logger whose
// WARN lines the test can read.
func newDebugHandler(t *testing.T, clk clock.Clock, policy config.DebuggerTimeoutPolicy, waitTimeout time.Duration) (*Handler, *debugger.Manager, *observer.ObservedLogs) {
	t.Helper()
	core, logs := observer.New(zap.DebugLevel)
	m := debuggertest.NewManager(t, clk, policy)
	tracker := newInstanceTracker(clk, zap.NewNop())
	t.Cleanup(tracker.Stop)
	h := &Handler{
		cfg: &config.Config{
			Region:              "us-east-1",
			AccountID:           "000000000000",
			LambdaDebugger:      true,
			DebuggerTimeout:     policy,
			DebuggerWaitTimeout: waitTimeout,
		},
		log:      serviceutil.NewServiceLogger(zap.New(core), "lambda"),
		clk:      clk,
		ls:       newLambdaStore(state.NewMemoryStore(), "us-east-1", clk),
		runtimes: newRuntimeRegistry(nil),
		tracker:  tracker,
		debugger: m,
	}
	return h, m, logs
}

// dispatched is where a background invocationContext lands.
type dispatched struct {
	ctx    context.Context
	cancel context.CancelFunc
}

func invocationContextInBackground(h *Handler, ctx context.Context, fn *Function, inst RuntimeInstance) <-chan dispatched {
	done := make(chan dispatched, 1)
	go func() {
		c, cancel := h.invocationContext(ctx, fn, inst)
		done <- dispatched{ctx: c, cancel: cancel}
	}()
	return done
}

// assertHeld gives the goroutine time to arm its timer on the mock clock and
// asserts the event has not been dispatched.
func assertHeld(t *testing.T, done <-chan dispatched) {
	t.Helper()
	select {
	case d := <-done:
		d.cancel()
		t.Fatal("the invocation was dispatched while it should be held")
	case <-time.After(20 * time.Millisecond):
	}
}

func awaitDispatched(t *testing.T, done <-chan dispatched) dispatched {
	t.Helper()
	select {
	case d := <-done:
		t.Cleanup(d.cancel)
		return d
	case <-time.After(5 * time.Second):
		t.Fatal("the invocation was never dispatched")
		return dispatched{}
	}
}

// ─── the hold ────────────────────────────────────────────────────────────────

func TestInvocationContext_holdsForAClientAndStartsTheClockAtDispatch(t *testing.T) {
	// Given: a function tagged to wait, its target bound and listening with
	// nobody attached, and an environment created for it
	clk := clock.NewMock()
	h, m, _ := newDebugHandler(t, clk, config.DebuggerTimeoutAttached, 2*time.Minute)
	fn := waitTaggedNodeFunction("held")
	target := boundDebugTarget(t, m, fn)
	target.SetUpstream(holdUpstream(t))
	inst := debugInstance{poolTestInstance: newPoolTestInstance(fn.Name), target: target}

	// When: an invocation reaches the dispatch point and far more than the
	// function's 3 s timeout passes with no client
	done := invocationContextInBackground(h, context.Background(), fn, inst)
	assertHeld(t, done)
	clk.Add(time.Minute)

	// Then: it is still held — the function's clock has not started, so
	// nothing has timed out
	assertHeld(t, done)

	// When: a client attaches, and the settle passes
	attachClient(t, target)
	assertHeld(t, done)
	clk.Add(debugger.SettleAfterAttach)

	// Then: the event is dispatched with the full budget, and the nominal
	// deadline the Runtime API will report is measured from the dispatch,
	// not from the arrival
	d := awaitDispatched(t, done)
	deadline, ok := d.ctx.Deadline()
	if !ok || !deadline.Equal(clk.Now().Add(3*time.Second)) {
		t.Fatalf("deadline = %s (%v), want %s (dispatch + 3s)", deadline, ok, clk.Now().Add(3*time.Second))
	}
	// And: with the client attached the clock stays stopped, as for any
	// attached client
	clk.Add(time.Minute)
	select {
	case <-d.ctx.Done():
		t.Fatalf("the invocation ended with a client attached: %v", d.ctx.Err())
	case <-time.After(20 * time.Millisecond):
	}
}

func TestAwaitDebugger_expiryWarnsAndProceeds(t *testing.T) {
	// Given: a held invocation under a 5 s wait timeout
	clk := clock.NewMock()
	h, m, logs := newDebugHandler(t, clk, config.DebuggerTimeoutAttached, 5*time.Second)
	fn := waitTaggedNodeFunction("expired")
	target := boundDebugTarget(t, m, fn)
	inst := debugInstance{poolTestInstance: newPoolTestInstance(fn.Name), target: target}
	done := invocationContextInBackground(h, context.Background(), fn, inst)
	assertHeld(t, done)

	// When: the wait timeout passes with nobody attached
	clk.Add(5 * time.Second)

	// Then: the event is dispatched anyway, the function's clock starting
	// now, and one WARN names the function, the tag and the timeout
	d := awaitDispatched(t, done)
	if deadline, _ := d.ctx.Deadline(); !deadline.Equal(clk.Now().Add(3 * time.Second)) {
		t.Errorf("deadline = %s, want dispatch + 3s", deadline)
	}
	warns := logs.FilterLevelExact(zap.WarnLevel).FilterMessageSnippet("no client attached").All()
	if len(warns) != 1 {
		t.Fatalf("WARN lines = %d, want 1: %+v", len(warns), logs.All())
	}
	fields := warns[0].ContextMap()
	if fields["function"] != fn.Name || fields["tag"] != debugger.TagWait || fields["timeout"] != 5*time.Second {
		t.Errorf("WARN fields = %v, want the function, %s and the timeout", fields, debugger.TagWait)
	}
	// And: the clock now runs — the budget was not spent holding
	clk.Add(3 * time.Second)
	select {
	case <-d.ctx.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("the function's timeout never fired after the hold expired")
	}
	if d.ctx.Err() != context.DeadlineExceeded {
		t.Errorf("err = %v, want DeadlineExceeded", d.ctx.Err())
	}
}

func TestAwaitDebugger_strictPolicyNeverHolds(t *testing.T) {
	// Given: the wait tag under OVERCAST_DEBUGGER_TIMEOUT=strict
	clk := clock.NewMock()
	h, m, _ := newDebugHandler(t, clk, config.DebuggerTimeoutStrict, 2*time.Minute)
	fn := waitTaggedNodeFunction("strict")
	target := boundDebugTarget(t, m, fn)
	inst := debugInstance{poolTestInstance: newPoolTestInstance(fn.Name), target: target}

	// When: an invocation reaches the dispatch point
	done := invocationContextInBackground(h, context.Background(), fn, inst)

	// Then: it is dispatched at once — the mock clock never moved — and on
	// the real timeout, which under strict is a plain wall-clock deadline
	d := awaitDispatched(t, done)
	if deadline, ok := d.ctx.Deadline(); !ok || time.Until(deadline) > 3*time.Second || time.Until(deadline) <= 0 {
		t.Errorf("deadline = %s (%v), want a wall-clock deadline within 3s", deadline, ok)
	}
}

func TestAwaitDebugger_callerCancellationReleasesTheHold(t *testing.T) {
	// Given: a held invocation whose caller is still connected
	clk := clock.NewMock()
	h, m, _ := newDebugHandler(t, clk, config.DebuggerTimeoutAttached, 2*time.Minute)
	fn := waitTaggedNodeFunction("cancelled")
	target := boundDebugTarget(t, m, fn)
	inst := debugInstance{poolTestInstance: newPoolTestInstance(fn.Name), target: target}
	ctx, cancel := context.WithCancel(context.Background())
	done := invocationContextInBackground(h, ctx, fn, inst)
	assertHeld(t, done)

	// When: the caller disconnects
	cancel()

	// Then: the hold ends at once, and the invocation context it hands back
	// follows the cancellation, so nothing runs on the runtime
	d := awaitDispatched(t, done)
	select {
	case <-d.ctx.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("the invocation context stayed live after the caller cancelled")
	}
	if d.ctx.Err() != context.Canceled {
		t.Errorf("err = %v, want Canceled", d.ctx.Err())
	}
}

func TestAwaitDebugger_untaggedFunctionIsNeverHeld(t *testing.T) {
	// Given: a debugged function without the wait tag, nobody attached
	clk := clock.NewMock()
	h, m, _ := newDebugHandler(t, clk, config.DebuggerTimeoutAttached, 2*time.Minute)
	fn := debugTaggedNodeFunction("plain")
	target := boundDebugTarget(t, m, fn)
	inst := debugInstance{poolTestInstance: newPoolTestInstance(fn.Name), target: target}

	// When/Then: the invocation is dispatched at once
	awaitDispatched(t, invocationContextInBackground(h, context.Background(), fn, inst))
}

// ─── registration follows the record ─────────────────────────────────────────

func TestSyncDebugTarget_followsTheTags(t *testing.T) {
	clk := clock.NewMock()
	h, m, _ := newDebugHandler(t, clk, config.DebuggerTimeoutAttached, 2*time.Minute)
	fn := debugTaggedNodeFunction("synced")
	id := debugger.TargetID(debugger.ServiceLambda, fn.Name, "")

	// When: a tagged function is synced
	target := h.syncDebugTarget(context.Background(), fn)

	// Then: it is registered, bound on a port, listening for a container,
	// with the roots and ARN the descriptor needs
	if target == nil {
		t.Fatal("tagged function not registered")
	}
	if target.State() != debugger.StateUnbound || target.Port() == 0 {
		t.Fatalf("state = %s port = %d, want unbound on a port", target.State(), target.Port())
	}
	if d := target.Descriptor(); d.RemoteRoot != lambdaTaskRoot || d.Setup.TagCLI == "" || d.WaitForDebugger {
		t.Errorf("descriptor = %+v, want /var/task, setup, and no wait", d)
	}

	// When: the wait tag is added
	fn.Tags[debugger.TagWait] = "true"
	again := h.syncDebugTarget(context.Background(), fn)

	// Then: the same target carries it — no rebind, same port
	if again != target || !target.Wait() || target.Port() != again.Port() {
		t.Errorf("wait toggle replaced the target or lost the flag")
	}

	// When: the debug tags are removed
	fn.Tags = map[string]string{"team": "platform"}
	if got := h.syncDebugTarget(context.Background(), fn); got != nil {
		t.Errorf("untagged sync returned a target: %+v", got.Descriptor())
	}

	// Then: the target is released and its port is free
	if _, registered := m.Get(id); registered {
		t.Fatal("target still registered after the tags were removed")
	}

	// And: a function that never had a target costs nothing to sync
	if got := h.syncDebugTarget(context.Background(), &Function{Name: "never", Runtime: "nodejs22.x"}); got != nil {
		t.Errorf("an untagged function registered a target: %+v", got.Descriptor())
	}
}

func TestSyncDebugTarget_flagOffRegistersInertWithoutAPort(t *testing.T) {
	// Given: a tagged function on a server whose Lambda debugger flag is off
	clk := clock.NewMock()
	h, m, _ := newDebugHandler(t, clk, config.DebuggerTimeoutAttached, 2*time.Minute)
	h.cfg.LambdaDebugger = false
	fn := waitTaggedNodeFunction("inert")

	// When: it is synced
	target := h.syncDebugTarget(context.Background(), fn)

	// Then: the console can explain why it is off, and no port was bound
	if target == nil {
		t.Fatal("tagged function not registered")
	}
	if target.State() != debugger.StateInert || target.Port() != 0 || target.Bound() {
		t.Errorf("state = %s port = %d bound = %v, want inert with no port", target.State(), target.Port(), target.Bound())
	}
	if _, registered := m.Get(debugger.TargetID(debugger.ServiceLambda, fn.Name, "")); !registered {
		t.Error("inert target not registered")
	}
}

func TestSyncDebugTarget_followsAnEnvironmentUpdate(t *testing.T) {
	// Given: a tagged Node function registered on an auto port
	clk := clock.NewMock()
	h, _, _ := newDebugHandler(t, clk, config.DebuggerTimeoutAttached, 2*time.Minute)
	fn := debugTaggedNodeFunction("env")
	before := h.syncDebugTarget(context.Background(), fn)
	if before == nil || before.Source() != debugger.SourceRuntime {
		t.Fatalf("target = %+v, want one resolved from the runtime", before)
	}

	// When: its environment gains an --inspect flag naming a port, as
	// UpdateFunctionConfiguration would store it
	fn.Environment = map[string]string{"NODE_OPTIONS": "--inspect=0.0.0.0:9251"}
	after := h.syncDebugTarget(context.Background(), fn)

	// Then: the target is re-resolved — a new one, on the port the flag names
	if after == nil || after == before {
		t.Fatal("environment update did not re-resolve the target")
	}
	if after.Port() != 9251 && after.State() != debugger.StateError {
		t.Errorf("port = %d state = %s, want 9251 (or error if it is taken)", after.Port(), after.State())
	}
}

func TestService_scanTagged_registersPersistedFunctionsOnce(t *testing.T) {
	// Given: a service whose store holds two tagged functions in two regions
	// and one untagged one, none ever described or invoked
	clk := clock.NewMock()
	h, m, _ := newDebugHandler(t, clk, config.DebuggerTimeoutAttached, 2*time.Minute)
	svc := &Service{ls: h.ls, handler: h, debugger: m, log: h.log}
	east := debugTaggedNodeFunction("east")
	west := debugTaggedNodeFunction("west")
	west.ARN = "arn:aws:lambda:eu-west-1:000000000000:function:west"
	plain := &Function{Name: "plain", ARN: "arn:aws:lambda:us-east-1:000000000000:function:plain", Runtime: "nodejs22.x"}
	for _, fn := range []*Function{east, plain} {
		if aerr := svc.ls.putFunction(context.Background(), fn); aerr != nil {
			t.Fatalf("put %s: %s", fn.Name, aerr.Message)
		}
	}
	if aerr := svc.ls.putFunction(middleware.ContextWithRegion(context.Background(), "eu-west-1"), west); aerr != nil {
		t.Fatalf("put west: %s", aerr.Message)
	}

	// When: the first target list scans
	svc.ScanTagged(context.Background())

	// Then: both tagged functions are registered, the untagged one is not
	for _, name := range []string{"east", "west"} {
		if _, ok := m.Get(debugger.TargetID(debugger.ServiceLambda, name, "")); !ok {
			t.Errorf("%s not registered by the scan", name)
		}
	}
	if _, ok := m.Get(debugger.TargetID(debugger.ServiceLambda, "plain", "")); ok {
		t.Error("untagged function registered by the scan")
	}

	// When: one is released, as DeleteFunction would, and the list scans again
	m.Release(debugger.TargetID(debugger.ServiceLambda, "east", ""))
	svc.ScanTagged(context.Background())

	// Then: nothing is re-registered — the scan ran once
	if _, ok := m.Get(debugger.TargetID(debugger.ServiceLambda, "east", "")); ok {
		t.Error("a second scan re-registered a released target")
	}
}
