package lambda

// debugger_test.go — the Lambda side of internal/debugger, touch point by
// touch point (docs/plans/compute-debugger.md § 4 and § 9).

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/overcast-sh/overcast/internal/clock"
	"github.com/overcast-sh/overcast/internal/config"
	"github.com/overcast-sh/overcast/internal/debugger"
	"github.com/overcast-sh/overcast/internal/debugger/debuggertest"
	"github.com/overcast-sh/overcast/internal/state"
)

// boundDebugTarget registers an enabled, listening target for fn as a cold
// start would, speaking the protocol its runtime resolves to.
func boundDebugTarget(t *testing.T, m *debugger.Manager, fn *Function) *debugger.Target {
	t.Helper()
	spec, problems := debugger.SpecFromTags(debugger.ServiceLambda, fn.Tags, true)
	if len(problems) != 0 {
		t.Fatalf("tag problems: %+v", problems)
	}
	res := debugger.Default.Resolve(spec, fn.Runtime, fn.Environment)
	target, err := m.Ensure(debugger.TargetID(debugger.ServiceLambda, fn.Name, ""), spec, res)
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if target.State() != debugger.StateUnbound {
		t.Fatalf("target state = %s (%s), want unbound", target.State(), target.Reason())
	}
	return target
}

// holdUpstream is a fake container debug port that accepts and holds every
// connection, so a client dialled through the proxy counts as attached.
func holdUpstream(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			t.Cleanup(func() { _ = c.Close() })
		}
	}()
	return ln.Addr().String()
}

// attachClient dials the target's port and waits until the proxy reports the
// client attached.
func attachClient(t *testing.T, target *debugger.Target) net.Conn {
	t.Helper()
	c, err := net.DialTimeout("tcp", net.JoinHostPort(target.Host(), strconv.Itoa(target.Port())), 2*time.Second)
	if err != nil {
		t.Fatalf("dial target: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	deadline := time.Now().Add(5 * time.Second)
	for !target.Attached() {
		if time.Now().After(deadline) {
			t.Fatal("client never counted as attached")
		}
		time.Sleep(5 * time.Millisecond)
	}
	return c
}

func debugTaggedNodeFunction(name string) *Function {
	return &Function{
		Name:       name,
		ARN:        "arn:aws:lambda:us-east-1:000000000000:function:" + name,
		Runtime:    "nodejs22.x",
		Handler:    "index.handler",
		MemorySize: 128,
		Timeout:    3,
		Tags:       map[string]string{debugger.TagDebug: "true"},
	}
}

// ─── env injection ───────────────────────────────────────────────────────────

func TestContainerRuntimeBuildEnv_injectsTheDebuggerFlagsOnlyWithATarget(t *testing.T) {
	// Given: a runtime, a Node function with its own NODE_OPTIONS, and a bound
	// inspector target for it
	runtime := &ContainerRuntime{
		cfg:              &config.Config{Region: "us-east-1", AccountID: "000000000000"},
		overcastEndpoint: "http://172.18.0.1:4566",
	}
	fn := debugTaggedNodeFunction("demo")
	fn.Environment = map[string]string{"NODE_OPTIONS": "--enable-source-maps"}
	target := boundDebugTarget(t, debuggertest.NewManager(t, clock.NewMock(), config.DebuggerTimeoutAttached), fn)
	port := strconv.Itoa(target.Port())

	// When: the container environment is built with and without the target
	with := envMap(runtime.buildEnv(fn, "stream", initTypeOnDemand, "172.18.0.1:41001", target))
	without := envMap(runtime.buildEnv(fn, "stream", initTypeOnDemand, "172.18.0.1:41001", nil))

	// Then: the target appends the inspector flag to the user's value and
	// names the port, and nothing of it exists without the target
	if got, want := with["NODE_OPTIONS"], "--enable-source-maps --inspect=0.0.0.0:"+port; got != want {
		t.Errorf("NODE_OPTIONS = %q, want %q", got, want)
	}
	if got := with[debugger.DebugPortEnv]; got != port {
		t.Errorf("%s = %q, want %q", debugger.DebugPortEnv, got, port)
	}
	if got := without["NODE_OPTIONS"]; got != "--enable-source-maps" {
		t.Errorf("NODE_OPTIONS without a target = %q, want the user's value alone", got)
	}
	if _, leaked := without[debugger.DebugPortEnv]; leaked {
		t.Errorf("%s set without a target", debugger.DebugPortEnv)
	}
	// And: the function record the API answers from is untouched — the
	// injected values live in the container only
	if _, leaked := fn.Environment[debugger.DebugPortEnv]; leaked || fn.Environment["NODE_OPTIONS"] != "--enable-source-maps" {
		t.Errorf("fn.Environment mutated: %v", fn.Environment)
	}
}

// ─── the create request ──────────────────────────────────────────────────────

func TestAcquireContainer_publishesTheDebugPortOnlyForATaggedFunction(t *testing.T) {
	cases := []struct {
		name       string
		tags       map[string]string
		imageCfg   *ImageConfig
		wantTarget bool
		wantRoot   string
	}{
		{name: "untagged", wantTarget: false},
		{name: "tagged", tags: map[string]string{debugger.TagDebug: "true"}, wantTarget: true, wantRoot: lambdaTaskRoot},
		{name: "tagged image with a working directory", tags: map[string]string{debugger.TagDebug: "true"},
			imageCfg: &ImageConfig{WorkingDirectory: "/app"}, wantTarget: true, wantRoot: "/app"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Given: a fake daemon that records creates and fails starts, and a
			// runtime whose manager allows the debugger for tagged functions
			daemon := newRecordingDaemon(t)
			cr := newDaemonContainerRuntime(t, daemon.Server)
			cr.cfg.LambdaDebugger = true
			m := debuggertest.NewManager(t, clock.New(), config.DebuggerTimeoutAttached)
			cr.SetDebugger(m)
			fn := imageFunction()
			fn.Tags = tc.tags
			fn.ImageConfig = tc.imageCfg

			// When: the function is acquired; the fake start ends the acquire
			// just past the create under test
			if _, err := cr.acquireContainer(context.Background(), fn, func(string) {}, initTypeOnDemand, false); err == nil {
				t.Fatal("expected the fake daemon's start failure")
			}
			creates := daemon.recordedCreates()
			if len(creates) != 1 {
				t.Fatalf("container creates = %d, want 1", len(creates))
			}
			req := creates[0]
			env := envMap(req.ContainerConfig.Env)
			target, registered := m.Get(debugger.TargetID(debugger.ServiceLambda, fn.Name, ""))

			// Then: only a tagged function is registered, exposes its port on
			// an ephemeral loopback host port, and carries the port in its env
			if registered != tc.wantTarget {
				t.Fatalf("target registered = %v, want %v", registered, tc.wantTarget)
			}
			if !tc.wantTarget {
				if len(req.ContainerConfig.ExposedPorts) != 0 || len(req.HostConfig.PortBindings) != 0 {
					t.Errorf("untagged function exposed ports: %v / %v", req.ContainerConfig.ExposedPorts, req.HostConfig.PortBindings)
				}
				if _, leaked := env[debugger.DebugPortEnv]; leaked {
					t.Errorf("%s set for an untagged function", debugger.DebugPortEnv)
				}
				return
			}
			key := strconv.Itoa(target.Port()) + "/tcp"
			if _, ok := req.ContainerConfig.ExposedPorts[key]; !ok {
				t.Errorf("ExposedPorts = %v, want %s", req.ContainerConfig.ExposedPorts, key)
			}
			if got := req.HostConfig.PortBindings[key]; len(got) != 1 || got[0].HostIP != "127.0.0.1" || got[0].HostPort != "0" {
				t.Errorf("PortBindings[%s] = %v, want an ephemeral loopback binding", key, got)
			}
			if got := env[debugger.DebugPortEnv]; got != strconv.Itoa(target.Port()) {
				t.Errorf("%s = %q, want %d", debugger.DebugPortEnv, got, target.Port())
			}
			d := target.Descriptor()
			if d.RemoteRoot != tc.wantRoot {
				t.Errorf("remoteRoot = %q, want %q", d.RemoteRoot, tc.wantRoot)
			}
			if !strings.Contains(d.Setup.TagCLI, fn.ARN) {
				t.Errorf("setup.tagCli = %q, want the function ARN", d.Setup.TagCLI)
			}
			// And: the start failed, so nothing is behind the port
			if d.Upstream != "" || d.State != string(debugger.StateUnbound) {
				t.Errorf("state = %s upstream = %q, want unbound with no upstream", d.State, d.Upstream)
			}
		})
	}
}

func TestAcquireContainer_flagOffRegistersAnInertTarget(t *testing.T) {
	// Given: a tagged function on a server whose Lambda debugger flag is off
	daemon := newRecordingDaemon(t)
	cr := newDaemonContainerRuntime(t, daemon.Server)
	m := debuggertest.NewManager(t, clock.New(), config.DebuggerTimeoutAttached)
	cr.SetDebugger(m)
	fn := imageFunction()
	fn.Tags = map[string]string{debugger.TagDebug: "true"}

	// When: the function is acquired
	if _, err := cr.acquireContainer(context.Background(), fn, func(string) {}, initTypeOnDemand, false); err == nil {
		t.Fatal("expected the fake daemon's start failure")
	}

	// Then: the console can explain why the debugger is off, and the container
	// was created exactly as an untagged one would be
	target, ok := m.Get(debugger.TargetID(debugger.ServiceLambda, fn.Name, ""))
	if !ok {
		t.Fatal("no inert target registered for the tagged function")
	}
	if target.State() != debugger.StateInert || !strings.Contains(target.Reason(), "OVERCAST_LAMBDA_DEBUGGER") {
		t.Errorf("state = %s reason = %q, want inert naming the flag", target.State(), target.Reason())
	}
	req := daemon.recordedCreates()[0]
	if len(req.ContainerConfig.ExposedPorts) != 0 || len(req.HostConfig.PortBindings) != 0 {
		t.Errorf("inert target exposed ports: %v / %v", req.ContainerConfig.ExposedPorts, req.HostConfig.PortBindings)
	}
	if _, leaked := envMap(req.ContainerConfig.Env)[debugger.DebugPortEnv]; leaked {
		t.Errorf("%s set for an inert target", debugger.DebugPortEnv)
	}
}

// ─── one execution environment ───────────────────────────────────────────────

func TestAcquire_liveDebugTargetPinsTheFunctionToOneInstance(t *testing.T) {
	// Given: a pool with room for many, a function with a live debug target,
	// and one invocation of it in flight
	rt := &countingColdStartRuntime{}
	pool := NewInstancePool(rt, zap.NewNop(), clock.NewMock(), PoolLimits{MaxInstancesPerFunction: 10})
	defer pool.Stop()
	m := debuggertest.NewManager(t, clock.NewMock(), config.DebuggerTimeoutAttached)
	pool.debugger = m
	fn := debugTaggedNodeFunction("debugged")
	fn.Timeout = 1
	boundDebugTarget(t, m, fn)
	first := mustAcquire(t, pool, fn)

	// When: a second invocation arrives while the first still runs
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	_, err := pool.Acquire(ctx, fn)

	// Then: it queues on the per-function cap — the debug port belongs to one
	// container — and is throttled when its budget runs out, as at the
	// emulator cap
	throttle, ok := asThrottle(err)
	if !ok || throttle.Reason != reasonConcurrencyLimit {
		t.Fatalf("second acquire: err = %v, want a %s throttle", err, reasonConcurrencyLimit)
	}
	if rt.count() != 1 {
		t.Fatalf("cold starts = %d, want 1", rt.count())
	}

	// When: the first invocation finishes
	pool.Release(context.Background(), first, true)

	// Then: the next invocation reuses that one environment
	mustAcquire(t, pool, fn)
	if rt.count() != 1 {
		t.Fatalf("cold starts after release = %d, want 1 (warm reuse)", rt.count())
	}
}

func TestAcquire_inertOrErrorTargetDoesNotPin(t *testing.T) {
	// Given: a function whose tag asks for a debugger the flag keeps off
	rt := &countingColdStartRuntime{}
	pool := NewInstancePool(rt, zap.NewNop(), clock.NewMock(), PoolLimits{MaxInstancesPerFunction: 10})
	defer pool.Stop()
	m := debuggertest.NewManager(t, clock.NewMock(), config.DebuggerTimeoutAttached)
	pool.debugger = m
	fn := debugTaggedNodeFunction("inert")
	spec, _ := debugger.SpecFromTags(debugger.ServiceLambda, fn.Tags, false)
	if _, err := m.Ensure(debugger.TargetID(debugger.ServiceLambda, fn.Name, ""), spec, debugger.Default.Resolve(spec, fn.Runtime, nil)); err != nil {
		t.Fatalf("ensure: %v", err)
	}

	// When: two invocations run at once
	mustAcquire(t, pool, fn)
	mustAcquire(t, pool, fn)

	// Then: both get an environment — nothing can attach, so nothing is pinned
	if rt.count() != 2 {
		t.Fatalf("cold starts = %d, want 2", rt.count())
	}
}

func TestSetProvisionedConcurrency_liveDebugTargetFillsOneEnvironment(t *testing.T) {
	// Given: a function with a live debug target and a reservation of three
	rt := &countingColdStartRuntime{}
	pool := NewInstancePool(rt, zap.NewNop(), clock.NewMock(), PoolLimits{MaxWarmPerFunction: 5})
	defer pool.Stop()
	m := debuggertest.NewManager(t, clock.NewMock(), config.DebuggerTimeoutAttached)
	pool.debugger = m
	fn := debugTaggedNodeFunction("provisioned")
	boundDebugTarget(t, m, fn)

	// When: provisioned concurrency is set
	pool.SetProvisionedConcurrency(fn, 3)
	pool.warmWG.Wait()

	// Then: one environment holds the reservation, not three
	if rt.count() != 1 {
		t.Fatalf("environments created = %d, want 1", rt.count())
	}
	if _, allocated, _, _ := pool.ProvisionedStatus(fn.Name); allocated != 1 {
		t.Fatalf("allocated = %d, want 1", allocated)
	}
}

// ─── the suspendable deadline ────────────────────────────────────────────────

// debugInstance is a stub instance carrying a debug target.
type debugInstance struct {
	*poolTestInstance
	target *debugger.Target
}

func (d debugInstance) DebugTarget() *debugger.Target { return d.target }

func TestBoundInvocation_isAPlainTimeoutWithoutATarget(t *testing.T) {
	// Given: an ordinary instance and a mock clock that never advances
	clk := clock.NewMock()
	inst := newPoolTestInstance("plain")

	// When: an invocation is bounded by a short timeout
	ctx, cancel := boundInvocation(context.Background(), clk, &config.Config{}, 20*time.Millisecond, inst)
	defer cancel()

	// Then: it is context.WithTimeout on the wall clock — the mock clock is
	// never consulted, and the deadline is the nominal one
	select {
	case <-ctx.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("the plain timeout never fired")
	}
	if ctx.Err() != context.DeadlineExceeded {
		t.Fatalf("err = %v, want DeadlineExceeded", ctx.Err())
	}
}

func TestBoundInvocation_stopsTheClockWhileAClientIsAttached(t *testing.T) {
	// Given: an instance created for a live target with an editor attached
	clk := clock.NewMock()
	m := debuggertest.NewManager(t, clk, config.DebuggerTimeoutAttached)
	fn := debugTaggedNodeFunction("paused")
	target := boundDebugTarget(t, m, fn)
	target.SetUpstream(holdUpstream(t))
	attachClient(t, target)
	inst := debugInstance{poolTestInstance: newPoolTestInstance(fn.Name), target: target}

	// When: an invocation is bounded by the function timeout and the clock
	// runs far past it
	ctx, cancel := boundInvocation(context.Background(), clk, &config.Config{DebuggerTimeout: config.DebuggerTimeoutAttached}, 3*time.Second, inst)
	defer cancel()
	nominal, _ := ctx.Deadline()
	clk.Add(time.Minute)

	// Then: the invocation is still running — the clock stopped with the
	// client — while the deadline the Runtime API reports stayed nominal
	select {
	case <-ctx.Done():
		t.Fatal("the invocation timed out with a debugger attached")
	case <-time.After(50 * time.Millisecond):
	}
	if want := clk.Now().Add(-time.Minute).Add(3 * time.Second); !nominal.Equal(want) {
		t.Fatalf("nominal deadline = %s, want %s", nominal, want)
	}
}

func TestBoundInvocation_strictPolicyKeepsTheRealTimeout(t *testing.T) {
	// Given: the same attached client under the strict policy
	clk := clock.NewMock()
	m := debuggertest.NewManager(t, clk, config.DebuggerTimeoutStrict)
	fn := debugTaggedNodeFunction("strict")
	target := boundDebugTarget(t, m, fn)
	target.SetUpstream(holdUpstream(t))
	attachClient(t, target)
	inst := debugInstance{poolTestInstance: newPoolTestInstance(fn.Name), target: target}

	// When: an invocation is bounded and its timeout elapses
	ctx, cancel := boundInvocation(context.Background(), clk, &config.Config{DebuggerTimeout: config.DebuggerTimeoutStrict}, 20*time.Millisecond, inst)
	defer cancel()

	// Then: it times out exactly as AWS would
	select {
	case <-ctx.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("strict policy did not enforce the timeout")
	}
}

// ─── the container going away ────────────────────────────────────────────────

func TestContainerInstanceClose_clearsTheUpstreamOfItsOwnContainerOnly(t *testing.T) {
	// Given: a target bound to a container, and an instance for it
	m := debuggertest.NewManager(t, clock.NewMock(), config.DebuggerTimeoutAttached)
	target := boundDebugTarget(t, m, debugTaggedNodeFunction("closing"))
	newInstance := func(id string) *containerInstance {
		return &containerInstance{id: id, logger: zap.NewNop(), clk: clock.NewMock(), debug: target}
	}
	target.SetUpstream("127.0.0.1:55001")
	target.SetContainerID("container-one")

	// When: a container that was already replaced closes
	target.SetUpstream("127.0.0.1:55002")
	target.SetContainerID("container-two")
	if err := newInstance("container-one").Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// Then: the replacement stays reachable
	if got := target.Upstream(); got != "127.0.0.1:55002" {
		t.Fatalf("upstream after the old container closed = %q, want the replacement's", got)
	}

	// When: the current container closes
	if err := newInstance("container-two").Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// Then: nothing is behind the port, which stays open for the next one
	if got := target.Upstream(); got != "" {
		t.Fatalf("upstream after the current container closed = %q, want none", got)
	}
	if target.State() != debugger.StateUnbound {
		t.Fatalf("state = %s, want unbound", target.State())
	}
}

func TestDeleteFunction_releasesTheDebugTarget(t *testing.T) {
	// Given: a function with a registered target
	h, _ := lifecycleTestHandler(t)
	m := debuggertest.NewManager(t, clock.NewMock(), config.DebuggerTimeoutAttached)
	h.debugger = m
	fn := seedLifecycleFunction(t, h, nil)
	fn.Tags = map[string]string{debugger.TagDebug: "true"}
	target := boundDebugTarget(t, m, fn)
	port := target.Port()

	// When: the function is deleted
	req := withFunctionNameParam(httptest.NewRequest(http.MethodDelete, "/2015-03-31/functions/"+fn.Name, nil), fn.Name)
	rec := httptest.NewRecorder()
	h.DeleteFunction(rec, req)

	// Then: the target is gone and its port is free again
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204: %s", rec.Code, rec.Body.String())
	}
	if _, still := m.Get(target.ID()); still {
		t.Fatal("target still registered after the function was deleted")
	}
	ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		t.Fatalf("debug port %d still held after release: %v", port, err)
	}
	_ = ln.Close()
}

// ─── the describer ───────────────────────────────────────────────────────────

func TestService_describeUntagged(t *testing.T) {
	// Given: a service holding one function
	clk := clock.NewMock()
	svc := &Service{ls: newLambdaStore(state.NewMemoryStore(), "us-east-1", clk)}
	fn := &Function{Name: "plain", ARN: "arn:aws:lambda:us-east-1:000000000000:function:plain", Runtime: "nodejs22.x"}
	if aerr := svc.ls.putFunction(context.Background(), fn); aerr != nil {
		t.Fatalf("put function: %s", aerr.Message)
	}

	t.Run("an existing function is synthesised with its ARN", func(t *testing.T) {
		// When: the untagged function is described
		d, ok := svc.DescribeUntagged(context.Background(), debugger.ServiceLambda, "plain")

		// Then: the entry is off, says why, and the setup command names the ARN
		if !ok {
			t.Fatal("existing function not described")
		}
		if d.ID != "lambda/plain" || d.Enabled || d.Reason != debugger.ReasonNotTagged {
			t.Errorf("descriptor = %+v, want lambda/plain, off, %q", d, debugger.ReasonNotTagged)
		}
		if !strings.Contains(d.Setup.TagCLI, fn.ARN) {
			t.Errorf("setup.tagCli = %q, want the function ARN", d.Setup.TagCLI)
		}
	})

	t.Run("a missing function is not", func(t *testing.T) {
		// When/Then: nothing is invented for a name the store does not hold
		if _, ok := svc.DescribeUntagged(context.Background(), debugger.ServiceLambda, "ghost"); ok {
			t.Fatal("described a function that does not exist")
		}
	})

	t.Run("another service's resource is not ours", func(t *testing.T) {
		// When/Then: an ECS task is left to ECS, even under a function's name
		if _, ok := svc.DescribeUntagged(context.Background(), debugger.ServiceECS, "plain"); ok {
			t.Fatal("described an ECS resource")
		}
	})
}

// ─── identity ────────────────────────────────────────────────────────────────

func TestFunctionInstanceIdentity_changesWithTheDebugTags(t *testing.T) {
	// Given: a function and the same function with a debug tag
	plain := debugTaggedNodeFunction("identity")
	plain.Tags = nil
	tagged := debugTaggedNodeFunction("identity")
	cosmetic := debugTaggedNodeFunction("identity")
	cosmetic.Tags = map[string]string{"team": "platform"}

	// When/Then: a debug tag retires the environment built without it, since
	// the flag and the port binding are baked into the container, while any
	// other tag does not
	if functionInstanceIdentity(plain) == functionInstanceIdentity(tagged) {
		t.Fatal("a debug tag did not change the instance identity")
	}
	if functionInstanceIdentity(plain) != functionInstanceIdentity(cosmetic) {
		t.Fatal("an unrelated tag changed the instance identity")
	}
}
