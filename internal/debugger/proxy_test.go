package debugger

import (
	"bufio"
	"io"
	"net"
	"runtime"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/overcast-sh/overcast/internal/clock"
	"github.com/overcast-sh/overcast/internal/config"
	"github.com/overcast-sh/overcast/internal/docker"
)

// echoServer is a fake container debug port: it echoes every line it gets.
// prefix is echoed with each line so a test can tell two servers apart.
func echoServer(t *testing.T, prefix string) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				r := bufio.NewReader(c)
				for {
					line, err := r.ReadString('\n')
					if err != nil {
						return
					}
					if _, err := io.WriteString(c, prefix+line); err != nil {
						return
					}
				}
			}()
		}
	}()
	return ln.Addr().String()
}

func dialTarget(t *testing.T, tgt *Target) net.Conn {
	t.Helper()
	c, err := net.DialTimeout("tcp", net.JoinHostPort(tgt.Host(), strconv.Itoa(tgt.Port())), 2*time.Second)
	require.NoError(t, err)
	t.Cleanup(func() { c.Close() })
	return c
}

func roundTrip(t *testing.T, c net.Conn, line string) string {
	t.Helper()
	require.NoError(t, c.SetDeadline(time.Now().Add(2*time.Second)))
	_, err := io.WriteString(c, line+"\n")
	require.NoError(t, err)
	got, err := bufio.NewReader(c).ReadString('\n')
	require.NoError(t, err)
	return got
}

// events subscribes and returns a channel of event kinds.
func events(tgt *Target) <-chan EventKind {
	ch := make(chan EventKind, 16)
	tgt.Subscribe(func(ev Event) { ch <- ev.Kind })
	return ch
}

func expectEvent(t *testing.T, ch <-chan EventKind, want EventKind) {
	t.Helper()
	select {
	case got := <-ch:
		assert.Equal(t, want, got)
	case <-time.After(5 * time.Second):
		t.Fatalf("no %s event", want)
	}
}

func TestManager_ensureBindsAutoPortAndProxies(t *testing.T) {
	// Given: a manager and an echo "container"
	m := newTestManager(t, clock.NewMock(), config.DebuggerTimeoutAttached)
	tgt := boundTarget(t, m, "lambda/fn", passthrough{})
	upstream := echoServer(t, "echo:")
	ch := events(tgt)

	// When: the upstream is set and a client attaches and talks
	tgt.SetUpstream(upstream)
	assert.Equal(t, StateListening, tgt.State())
	c := dialTarget(t, tgt)
	got := roundTrip(t, c, "hello")

	// Then: bytes flow both ways, the port is in range and the target
	// reports attached with an attach event
	assert.Equal(t, "echo:hello\n", got)
	assert.GreaterOrEqual(t, tgt.Port(), m.ports[0])
	assert.LessOrEqual(t, tgt.Port(), m.ports[1])
	expectEvent(t, ch, EventAttach)
	assert.Equal(t, StateAttached, tgt.State())
	assert.NotEmpty(t, tgt.Descriptor().AttachedSince)

	// When: the client leaves
	c.Close()

	// Then: a detach event follows and the state drops back
	expectEvent(t, ch, EventDetach)
	assert.Eventually(t, func() bool { return tgt.State() == StateListening }, 5*time.Second, 5*time.Millisecond)
}

func TestManager_ensureIsIdempotentPerID(t *testing.T) {
	// Given: a registered target
	m := newTestManager(t, clock.NewMock(), config.DebuggerTimeoutAttached)
	spec := Spec{Service: ServiceLambda, Tagged: true, FlagOn: true}
	res := Resolution{Protocol: inspector{}, Source: SourceRuntime}
	first, err := m.Ensure("lambda/fn", spec, res)
	require.NoError(t, err)

	// When: the same request is made again
	again, err := m.Ensure("lambda/fn", spec, res)
	require.NoError(t, err)

	// Then: it is the same target on the same port
	assert.Same(t, first, again)
	assert.Len(t, m.List(), 1)
}

func TestManager_ensureReplacesOnChangedRequest(t *testing.T) {
	// Given: a registered target on an auto port
	m := newTestManager(t, clock.NewMock(), config.DebuggerTimeoutAttached)
	spec := Spec{Service: ServiceLambda, Tagged: true, FlagOn: true}
	first, err := m.Ensure("lambda/fn", spec, Resolution{Protocol: inspector{}, Source: SourceRuntime})
	require.NoError(t, err)
	oldPort := first.Port()

	// When: the tags now name a protocol
	replaced, err := m.Ensure("lambda/fn", spec, Resolution{Protocol: jdwp{}, Source: SourceTag})
	require.NoError(t, err)

	// Then: a new target replaced the old, which no longer listens
	assert.NotSame(t, first, replaced)
	assert.Equal(t, "jdwp", replaced.Protocol().Name())
	assert.Len(t, m.List(), 1)
	assert.Nil(t, first.listener)
	_, err = net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(oldPort)), 200*time.Millisecond)
	if err == nil {
		// The port may have been reused by the replacement, which is fine.
		assert.Equal(t, oldPort, replaced.Port())
	}
}

func TestManager_noUpstreamClosesAtOnce(t *testing.T) {
	// Given: a listening target with no container behind it
	m := newTestManager(t, clock.NewMock(), config.DebuggerTimeoutAttached)
	tgt := boundTarget(t, m, "lambda/fn", passthrough{})
	ch := events(tgt)

	// When: a client connects
	c := dialTarget(t, tgt)
	require.NoError(t, c.SetReadDeadline(time.Now().Add(2*time.Second)))
	_, err := c.Read(make([]byte, 1))

	// Then: the connection is closed without ever counting as attached
	assert.ErrorIs(t, err, io.EOF)
	select {
	case ev := <-ch:
		t.Fatalf("unexpected event %s", ev)
	default:
	}
	assert.Equal(t, StateUnbound, tgt.State())
}

func TestManager_setUpstreamMidLife(t *testing.T) {
	// Given: a target forwarding to one container, with a client on it
	m := newTestManager(t, clock.NewMock(), config.DebuggerTimeoutAttached)
	tgt := boundTarget(t, m, "lambda/fn", passthrough{})
	tgt.SetUpstream(echoServer(t, "one:"))
	old := dialTarget(t, tgt)
	assert.Equal(t, "one:a\n", roundTrip(t, old, "a"))

	// When: the container is replaced
	tgt.SetUpstream(echoServer(t, "two:"))

	// Then: the old connection keeps working, and a new one reaches the
	// new container on the same port
	assert.Equal(t, "one:b\n", roundTrip(t, old, "b"))
	assert.Equal(t, "two:c\n", roundTrip(t, dialTarget(t, tgt), "c"))

	// When: the upstream is cleared
	tgt.ClearUpstream()

	// Then: new connections are refused, the old stays
	c := dialTarget(t, tgt)
	require.NoError(t, c.SetReadDeadline(time.Now().Add(2*time.Second)))
	_, err := c.Read(make([]byte, 1))
	assert.ErrorIs(t, err, io.EOF)
	assert.Equal(t, "one:d\n", roundTrip(t, old, "d"))
}

func TestManager_deadUpstreamClosesClient(t *testing.T) {
	// Given: an upstream address nothing listens on
	m := newTestManager(t, clock.NewMock(), config.DebuggerTimeoutAttached)
	tgt := boundTarget(t, m, "lambda/fn", passthrough{})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	tgt.SetUpstream(ln.Addr().String())
	require.NoError(t, ln.Close())

	// When: a client connects
	c := dialTarget(t, tgt)
	require.NoError(t, c.SetReadDeadline(time.Now().Add(5*time.Second)))
	_, err = c.Read(make([]byte, 1))

	// Then: it is closed once the dial fails, and nothing counted as attached
	assert.ErrorIs(t, err, io.EOF)
	assert.False(t, tgt.Attached())
}

func TestManager_explicitPortInUseIsError(t *testing.T) {
	// Given: a port something else already holds
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer ln.Close()
	busy := ln.Addr().(*net.TCPAddr).Port
	m := newTestManager(t, clock.NewMock(), config.DebuggerTimeoutAttached)

	// When: a target asks for exactly that port
	tgt, err := m.Ensure("lambda/fn", Spec{Service: ServiceLambda, Tagged: true, FlagOn: true, Port: busy},
		Resolution{Protocol: inspector{}, Port: busy, Source: SourceRuntime})
	require.NoError(t, err)

	// Then: the target is in error naming the port, and injects nothing —
	// never a silent fallback to another port
	assert.Equal(t, StateError, tgt.State())
	assert.Contains(t, tgt.Reason(), strconv.Itoa(busy))
	assert.Equal(t, busy, tgt.Port())
	env := map[string]string{}
	tgt.Inject(env)
	assert.Empty(t, env)
	cfg, hc := &docker.ContainerConfig{}, &docker.HostConfig{}
	tgt.ApplyPortBinding(cfg, hc)
	assert.Empty(t, cfg.ExposedPorts)
	d := tgt.Descriptor()
	assert.Equal(t, "error", d.State)
	assert.True(t, d.Enabled)
}

func TestManager_autoAllocationSkipsBoundPort(t *testing.T) {
	// Given: the first port of the range is held by something else
	ports := freePortRange(t)
	ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(ports[0])))
	require.NoError(t, err)
	defer ln.Close()
	m := NewManager(clock.NewMock(), nil, "127.0.0.1", ports, config.DebuggerTimeoutAttached)
	t.Cleanup(m.Close)
	spec := Spec{Service: ServiceLambda, Tagged: true, FlagOn: true}

	// When: two targets are allocated
	a, err := m.Ensure("lambda/a", spec, Resolution{Protocol: inspector{}, Source: SourceRuntime})
	require.NoError(t, err)
	b, err := m.Ensure("lambda/b", spec, Resolution{Protocol: inspector{}, Source: SourceRuntime})
	require.NoError(t, err)

	// Then: neither took the held port, both are in range and distinct,
	// and the lower one came first
	assert.NotEqual(t, ports[0], a.Port())
	assert.NotEqual(t, ports[0], b.Port())
	assert.NotEqual(t, a.Port(), b.Port())
	assert.Less(t, a.Port(), b.Port())
	assert.Equal(t, StateUnbound, a.State())
	assert.Equal(t, StateUnbound, b.State())
}

func TestManager_rangeExhaustedIsError(t *testing.T) {
	// Given: a range of one port, held by another target
	ports := freePortRange(t)
	ports[1] = ports[0]
	m := NewManager(clock.NewMock(), nil, "127.0.0.1", ports, config.DebuggerTimeoutAttached)
	t.Cleanup(m.Close)
	spec := Spec{Service: ServiceLambda, Tagged: true, FlagOn: true}
	first, err := m.Ensure("lambda/a", spec, Resolution{Protocol: inspector{}, Source: SourceRuntime})
	require.NoError(t, err)
	if first.State() == StateError {
		t.Skip("the probed port was taken between probe and bind")
	}

	// When: a second target needs a port
	second, err := m.Ensure("lambda/b", spec, Resolution{Protocol: inspector{}, Source: SourceRuntime})
	require.NoError(t, err)

	// Then: it is in error naming the range
	assert.Equal(t, StateError, second.State())
	assert.Contains(t, second.Reason(), "OVERCAST_DEBUGGER_PORTS")
	assert.Zero(t, second.Port())
}

func TestManager_inertTargetHasNoListener(t *testing.T) {
	// Given: a tag with the flag off
	m := newTestManager(t, clock.NewMock(), config.DebuggerTimeoutAttached)
	before := runtime.NumGoroutine()

	// When: it is registered
	tgt, err := m.Ensure("ecs/task-1/app", Spec{Service: ServiceECS, Container: "app", Tagged: true},
		Resolution{Protocol: passthrough{}, Source: SourceFallback})
	require.NoError(t, err)

	// Then: it is inert, injects nothing, binds nothing, and started no
	// goroutine
	assert.Equal(t, StateInert, tgt.State())
	assert.Contains(t, tgt.Reason(), "OVERCAST_ECS_DEBUGGER=true")
	assert.Zero(t, tgt.Port())
	env := map[string]string{}
	tgt.Inject(env)
	assert.Empty(t, env)
	assert.Equal(t, before, runtime.NumGoroutine())
	d := tgt.Descriptor()
	assert.False(t, d.Enabled)
	assert.Equal(t, "ecs", d.Service)
	assert.Equal(t, "task-1", d.Resource)
	assert.Equal(t, "app", d.Container)
	assert.Equal(t, "passthrough", d.Protocol)
}

func TestManager_ensureRejectsBadRequests(t *testing.T) {
	m := newTestManager(t, clock.NewMock(), config.DebuggerTimeoutAttached)

	// Given: an id that is not service/resource
	_, err := m.Ensure("fn", Spec{Tagged: true}, Resolution{Protocol: inspector{}})
	assert.Error(t, err)

	// Given: nothing to debug
	_, err = m.Ensure("lambda/fn", Spec{}, Resolution{})
	assert.ErrorIs(t, err, ErrNothingToDebug)

	// Given: a closed manager
	m.Close()
	_, err = m.Ensure("lambda/fn", Spec{Tagged: true, FlagOn: true}, Resolution{Protocol: inspector{}})
	assert.ErrorIs(t, err, ErrClosed)
}

func TestManager_listIsStableAndGetFinds(t *testing.T) {
	// Given: targets registered out of order
	m := newTestManager(t, clock.NewMock(), config.DebuggerTimeoutAttached)
	for _, id := range []string{"lambda/zeta", "ecs/task/app", "lambda/alpha"} {
		_, err := m.Ensure(id, Spec{Tagged: true}, Resolution{Protocol: passthrough{}})
		require.NoError(t, err)
	}

	// When: listed and looked up
	ids := make([]string, 0, 3)
	for _, tgt := range m.List() {
		ids = append(ids, tgt.ID())
	}
	_, found := m.Get("lambda/alpha")
	_, missing := m.Get("lambda/nope")

	// Then: the list is ordered by id
	assert.Equal(t, []string{"ecs/task/app", "lambda/alpha", "lambda/zeta"}, ids)
	assert.True(t, found)
	assert.False(t, missing)
}

func TestManager_releaseClosesEverythingAndLeaksNothing(t *testing.T) {
	// Given: a target with a live client connection and a dial in flight
	m := newTestManager(t, clock.NewMock(), config.DebuggerTimeoutAttached)
	upstream := echoServer(t, "e:")
	before := runtime.NumGoroutine()
	tgt := boundTarget(t, m, "lambda/fn", inspector{})
	tgt.SetUpstream(upstream)
	c := dialTarget(t, tgt)
	assert.Equal(t, "e:x\n", roundTrip(t, c, "x"))
	port := tgt.Port()

	// When: the target is released
	m.Release("lambda/fn")
	m.Release("lambda/fn") // a second release is a no-op

	// Then: the client is cut, the port is free again, the manager forgot
	// it, and every goroutine the target started has exited
	require.NoError(t, c.SetReadDeadline(time.Now().Add(2*time.Second)))
	_, err := c.Read(make([]byte, 1))
	assert.Error(t, err)
	ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if assert.NoError(t, err, "port still held after release") {
		ln.Close()
	}
	_, ok := m.Get("lambda/fn")
	assert.False(t, ok)
	// The echo server's connection goroutine is the test's, and exits when
	// its peer closes; allow it a moment. Polled inline rather than with
	// assert.Eventually, which runs the condition on a goroutine of its own.
	deadline := time.Now().Add(5 * time.Second)
	for runtime.NumGoroutine() > before && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	assert.LessOrEqual(t, runtime.NumGoroutine(), before, "goroutines leaked by the released target")
}

func TestManager_closeReleasesAll(t *testing.T) {
	// Given: two targets
	m := newTestManager(t, clock.NewMock(), config.DebuggerTimeoutAttached)
	a := boundTarget(t, m, "lambda/a", inspector{})
	b := boundTarget(t, m, "lambda/b", jdwp{})

	// When: the manager closes
	m.Close()

	// Then: both listeners are gone and the list is empty
	assert.Empty(t, m.List())
	for _, tgt := range []*Target{a, b} {
		_, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(tgt.Port())), 200*time.Millisecond)
		assert.Error(t, err)
	}
}

func TestTarget_injectAndPortBinding(t *testing.T) {
	// Given: a bound inspector target
	m := newTestManager(t, clock.NewMock(), config.DebuggerTimeoutAttached)
	tgt := boundTarget(t, m, "lambda/fn", inspector{})
	port := strconv.Itoa(tgt.Port())

	// When: the environment and the create request are prepared
	env := map[string]string{nodeOptionsEnv: "--enable-source-maps"}
	tgt.Inject(env)
	cfg, hc := &docker.ContainerConfig{}, &docker.HostConfig{}
	tgt.ApplyPortBinding(cfg, hc)

	// Then: the protocol flag and OVERCAST_DEBUG_PORT name the same port,
	// and the container exposes it on an ephemeral loopback host port
	assert.Equal(t, "--enable-source-maps --inspect=0.0.0.0:"+port, env[nodeOptionsEnv])
	assert.Equal(t, port, env[DebugPortEnv])
	assert.Contains(t, cfg.ExposedPorts, port+"/tcp")
	assert.Equal(t, []docker.PortBinding{{HostIP: "127.0.0.1", HostPort: "0"}}, hc.PortBindings[port+"/tcp"])

	// When: the container's inspect reports the published port
	inspect := &docker.ContainerInspect{}
	inspect.NetworkSettings.Ports = map[string][]docker.PortBinding{
		port + "/tcp": {{HostIP: "127.0.0.1", HostPort: "55012"}},
	}
	hostPort, ok := tgt.HostPortFrom(inspect)

	// Then: it is read back
	assert.True(t, ok)
	assert.Equal(t, 55012, hostPort)
	_, ok = tgt.HostPortFrom(&docker.ContainerInspect{})
	assert.False(t, ok)
	_, ok = tgt.HostPortFrom(nil)
	assert.False(t, ok)
}

func TestTarget_clearContainerOnlyForgetsTheCurrentContainer(t *testing.T) {
	// Given: a bound target whose container was replaced — hot reload retired
	// the old one and the new one has already been bound
	m := newTestManager(t, clock.NewMock(), config.DebuggerTimeoutAttached)
	tgt := boundTarget(t, m, "lambda/fn", inspector{})
	tgt.SetUpstream("127.0.0.1:55001")
	tgt.SetContainerID("old")
	tgt.SetUpstream("127.0.0.1:55002")
	tgt.SetContainerID("new")

	// When: the old container's Close reports it gone
	tgt.ClearContainer("old")

	// Then: the replacement stays reachable
	assert.Equal(t, "127.0.0.1:55002", tgt.Upstream())
	assert.Equal(t, "new", tgt.Descriptor().ContainerID)
	assert.Equal(t, StateListening, tgt.State())

	// When: the current container goes
	tgt.ClearContainer("new")

	// Then: the port has nothing behind it
	assert.Equal(t, "", tgt.Upstream())
	assert.Equal(t, "", tgt.Descriptor().ContainerID)
	assert.Equal(t, StateUnbound, tgt.State())
}

func TestTarget_observerDrivesPauseState(t *testing.T) {
	// Given: an inspector target whose "container" replays a CDP session
	m := newTestManager(t, clock.NewMock(), config.DebuggerTimeoutPaused)
	tgt := boundTarget(t, m, "lambda/fn", inspector{})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer ln.Close()
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		r := bufio.NewReader(c)
		c.Write([]byte(handshake101))
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			switch line {
			case "pause\n":
				c.Write(textFrame(pausedMsg))
			case "resume\n":
				c.Write(textFrame(resumedMsg))
			}
		}
	}()
	tgt.SetUpstream(ln.Addr().String())
	ch := events(tgt)
	c := dialTarget(t, tgt)
	expectEvent(t, ch, EventAttach)

	// When: the container reports a pause, then a resume
	_, err = io.WriteString(c, "pause\n")
	require.NoError(t, err)
	buf := make([]byte, 4096)
	require.NoError(t, c.SetReadDeadline(time.Now().Add(2*time.Second)))
	_, err = c.Read(buf)
	require.NoError(t, err)

	// Then: the target is paused, with a pause event and a timestamp
	expectEvent(t, ch, EventPause)
	assert.Equal(t, StatePaused, tgt.State())
	assert.NotEmpty(t, tgt.Descriptor().PausedSince)

	_, err = io.WriteString(c, "resume\n")
	require.NoError(t, err)
	expectEvent(t, ch, EventResume)
	assert.Equal(t, StateAttached, tgt.State())

	// When: the client drops while paused
	_, err = io.WriteString(c, "pause\n")
	require.NoError(t, err)
	expectEvent(t, ch, EventPause)
	c.Close()

	// Then: the pause is released before the detach
	expectEvent(t, ch, EventResume)
	expectEvent(t, ch, EventDetach)
}

func TestTarget_subscribeUnsubscribe(t *testing.T) {
	// Given: a subscriber that has been removed
	m := newTestManager(t, clock.NewMock(), config.DebuggerTimeoutAttached)
	tgt := boundTarget(t, m, "lambda/fn", passthrough{})
	called := false
	unsubscribe := tgt.Subscribe(func(Event) { called = true })
	unsubscribe()

	// When: a transition happens
	tgt.attach()
	tgt.detach(&connection{target: tgt})

	// Then: it is not called
	assert.False(t, called)
}

func TestTarget_subscribeSeedsTheStateTheFirstEventFollows(t *testing.T) {
	// Given: a target with one client attached
	m := newTestManager(t, clock.NewMock(), config.DebuggerTimeoutAttached)
	tgt := boundTarget(t, m, "lambda/fn", passthrough{})
	tgt.attach()

	// When: a subscriber registers, and the client then detaches
	var seededAttached, seededPaused bool
	var got []EventKind
	unsubscribe := tgt.subscribe(
		func(attached, paused bool) { seededAttached, seededPaused = attached, paused },
		func(ev Event) { got = append(got, ev.Kind) })
	defer unsubscribe()
	tgt.detach(&connection{target: tgt})

	// Then: the seed saw the attachment and the only event is the one after it
	assert.True(t, seededAttached)
	assert.False(t, seededPaused)
	assert.Equal(t, []EventKind{EventDetach}, got)
}

func TestManager_ensureReleasesTheTargetWhenNothingAsksAnyMore(t *testing.T) {
	// Given: a bound target for a function whose tag has since been removed
	m := newTestManager(t, clock.NewMock(), config.DebuggerTimeoutAttached)
	tgt := boundTarget(t, m, "lambda/fn", inspector{})
	port := tgt.Port()

	// When: the next cold start resolves nothing to debug
	_, err := m.Ensure("lambda/fn", Spec{Service: ServiceLambda, FlagOn: true}, Resolution{})

	// Then: the stale target is gone with its port, not left pinning the
	// function to one instance
	assert.ErrorIs(t, err, ErrNothingToDebug)
	_, ok := m.Get("lambda/fn")
	assert.False(t, ok)
	assert.False(t, tgt.Bound())
	ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if assert.NoError(t, err, "port still held after the tag was removed") {
		ln.Close()
	}
}
