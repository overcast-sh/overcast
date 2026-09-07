package debugger

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/overcast-sh/overcast/internal/clock"
	"github.com/overcast-sh/overcast/internal/config"
	"github.com/overcast-sh/overcast/internal/docker"
)

// TimeoutPolicy is config.DebuggerTimeoutPolicy under the name this package
// uses; the enum lives in config so it is parsed once and never redeclared.
type TimeoutPolicy = config.DebuggerTimeoutPolicy

// State is where a target is in its life. It is a string so the descriptor
// carries it unchanged.
type State string

const (
	// StateInert is a tagged resource the server flag keeps off: registered
	// so the console can say why, with no listener.
	StateInert State = "inert"
	// StateUnbound is a listener with no container behind it yet.
	StateUnbound State = "unbound"
	// StateListening is a listener with an upstream and no client.
	StateListening State = "listening"
	// StateAttached is at least one client connected.
	StateAttached State = "attached"
	// StatePaused is a connected client whose protocol reports a pause.
	StatePaused State = "paused"
	// StateError is a listener that could not be bound; Reason says why.
	StateError State = "error"
)

// EventKind is what changed on a target.
type EventKind string

const (
	// EventAttach fires when the attach count goes 0→1.
	EventAttach EventKind = "attach"
	// EventDetach fires when the attach count goes 1→0.
	EventDetach EventKind = "detach"
	// EventPause fires when the first connection reports a pause.
	EventPause EventKind = "pause"
	// EventResume fires when the last paused connection resumes or drops.
	EventResume EventKind = "resume"
)

// Event is delivered to subscribers, in order, from the goroutine that
// caused the transition.
type Event struct {
	Kind   EventKind
	Target *Target
	At     time.Time
}

// TargetID builds the id services register under: "lambda/<function>" or
// "ecs/<task id>/<container>". Ensure parses it back, so the id is the one
// place a target's identity is spelled.
func TargetID(service Service, resource, container string) string {
	id := string(service) + "/" + resource
	if container != "" {
		id += "/" + container
	}
	return id
}

func parseTargetID(id string) (service Service, resource, container string, err error) {
	parts := strings.Split(id, "/")
	if len(parts) < 2 || len(parts) > 3 || parts[0] == "" || parts[1] == "" {
		return "", "", "", fmt.Errorf("debugger: target id %q is not service/resource[/container]", id)
	}
	if len(parts) == 3 {
		if parts[2] == "" {
			return "", "", "", fmt.Errorf("debugger: target id %q has an empty container", id)
		}
		container = parts[2]
	}
	return Service(parts[0]), parts[1], container, nil
}

// Target is one debuggable resource: its loopback listener, the container
// behind it, and who is attached. Every method is safe to call from any
// goroutine.
type Target struct {
	id        string
	service   Service
	resource  string
	container string
	spec      Spec
	res       Resolution
	host      string
	policy    TimeoutPolicy
	clk       clock.Clock
	log       *zap.Logger

	// ctx ends every dial in flight when the target is released; conns and
	// the listener are closed explicitly, since io.Copy takes no context.
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	mu            sync.Mutex
	enabled       bool
	reason        string
	port          int
	listener      net.Listener
	closed        bool
	upstream      string
	containerID   string
	remoteRoot    string
	arn           string
	attached      int
	paused        int
	attachedSince time.Time
	pausedSince   time.Time
	conns         map[net.Conn]struct{}

	// emitMu serialises transitions and their delivery so subscribers see
	// events in the order they happened, without holding mu while they run.
	emitMu sync.Mutex
	subMu  sync.Mutex
	subs   map[int]func(Event)
	nextID int
}

// ID is the id the target was registered under.
func (t *Target) ID() string { return t.id }

// Service is the compute service the target belongs to.
func (t *Target) Service() Service { return t.service }

// Resource is the function name or task id.
func (t *Target) Resource() string { return t.resource }

// Container is the ECS container name; empty for Lambda.
func (t *Target) Container() string { return t.container }

// Spec is what the tags asked for.
func (t *Target) Spec() Spec { return t.spec }

// Protocol is the resolved protocol; nil for an inert target with no
// resolution.
func (t *Target) Protocol() Protocol { return t.res.Protocol }

// Source is how the protocol was chosen.
func (t *Target) Source() Source { return t.res.Source }

// Host is the address the listener binds.
func (t *Target) Host() string { return t.host }

// Port is the port editors attach to and the container listens on — the
// same number on both sides, so the inspector's own URLs match what the
// editor asked for. Zero while unbound or inert.
func (t *Target) Port() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.port
}

// Enabled reports whether the target has (or tried to bind) a listener.
func (t *Target) Enabled() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.enabled
}

// Reason says why the target is inert or in error; empty otherwise.
func (t *Target) Reason() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.reason
}

// State is the target's current state.
func (t *Target) State() State {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.stateLocked()
}

func (t *Target) stateLocked() State {
	switch {
	case !t.enabled:
		return StateInert
	case t.listener == nil:
		return StateError
	case t.paused > 0:
		return StatePaused
	case t.attached > 0:
		return StateAttached
	case t.upstream == "":
		return StateUnbound
	default:
		return StateListening
	}
}

// Attached reports whether at least one client is connected.
func (t *Target) Attached() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.attached > 0
}

// Paused reports whether a connected client's protocol reports a pause.
func (t *Target) Paused() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.paused > 0
}

// bound reports whether the listener exists — the precondition for
// injecting, binding ports and suspending a deadline.
func (t *Target) bound() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.listener != nil
}

// SetUpstream points new connections at the container's debug port. It may
// be called again when the container is replaced; connections to the old one
// are left to close on their own, which is what lets an editor's reconnect
// find the new container on the same port.
func (t *Target) SetUpstream(addr string) {
	t.mu.Lock()
	t.upstream = addr
	t.mu.Unlock()
}

// ClearUpstream makes new connections close immediately, which editors that
// poll treat as "not yet", until SetUpstream is called again.
func (t *Target) ClearUpstream() { t.SetUpstream("") }

// SetContainerID records the container behind the upstream, for the console.
func (t *Target) SetContainerID(id string) {
	t.mu.Lock()
	t.containerID = id
	t.mu.Unlock()
}

// ClearContainer forgets the upstream and the container id, but only while
// id is still the container behind the port. A service calls it when a
// container goes away; a retired container closing after its replacement was
// bound must not blind the proxy to the replacement, which is what an
// unconditional ClearUpstream from that container's Close would do.
func (t *Target) ClearContainer(id string) {
	t.mu.Lock()
	if t.containerID == id {
		t.upstream = ""
		t.containerID = ""
	}
	t.mu.Unlock()
}

// SetRemoteRoot records the container path editors map the local root to:
// /var/task for a zip, the image's working directory otherwise.
func (t *Target) SetRemoteRoot(path string) {
	t.mu.Lock()
	t.remoteRoot = path
	t.mu.Unlock()
}

// SetResourceARN records the ARN of the resource that carries the tag, so
// the setup block's CLI command is copy-paste ready.
func (t *Target) SetResourceARN(arn string) {
	t.mu.Lock()
	t.arn = arn
	t.mu.Unlock()
}

// Upstream is the address new connections are forwarded to; empty when none.
func (t *Target) Upstream() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.upstream
}

// Subscribe registers fn for attach, detach, pause and resume events and
// returns a function that removes it. Callbacks run synchronously on the
// goroutine that caused the transition and must return promptly; they must
// not call back into the Manager, whose Release, Close and replacing Ensure
// wait for those goroutines.
func (t *Target) Subscribe(fn func(Event)) (unsubscribe func()) {
	t.subMu.Lock()
	id := t.nextID
	t.nextID++
	if t.subs == nil {
		t.subs = map[int]func(Event){}
	}
	t.subs[id] = fn
	t.subMu.Unlock()
	return func() {
		t.subMu.Lock()
		delete(t.subs, id)
		t.subMu.Unlock()
	}
}

// deliver runs every subscriber with the event. Called under emitMu, never
// under mu, so a subscriber may read the target's state.
func (t *Target) deliver(kind EventKind, at time.Time) {
	t.subMu.Lock()
	subs := make([]func(Event), 0, len(t.subs))
	for _, fn := range t.subs {
		subs = append(subs, fn)
	}
	t.subMu.Unlock()
	ev := Event{Kind: kind, Target: t, At: at}
	for _, fn := range subs {
		fn(ev)
	}
}

// DebugPortEnv is set in every debugged container so user code and images
// can read the port without knowing the protocol.
const DebugPortEnv = "OVERCAST_DEBUG_PORT"

// Inject adds the protocol's listen flag and OVERCAST_DEBUG_PORT to a
// container environment. A target without a listener injects nothing: a
// flag that makes the runtime wait for a debugger nobody can reach would
// hang the function.
func (t *Target) Inject(env map[string]string) {
	if !t.bound() {
		return
	}
	port := t.Port()
	t.res.Protocol.Inject(env, port)
	env[DebugPortEnv] = strconv.Itoa(port)
}

// ApplyPortBinding exposes the debug port on the container and publishes it
// on an ephemeral loopback port of the Docker host. It is added whether or
// not Overcast will use it (a containerised Overcast dials the container's
// IP instead): it is cheap, and it avoids a second decision at create time.
func (t *Target) ApplyPortBinding(cfg *docker.ContainerConfig, hc *docker.HostConfig) {
	if !t.bound() {
		return
	}
	key := t.portKey()
	if cfg.ExposedPorts == nil {
		cfg.ExposedPorts = map[string]struct{}{}
	}
	cfg.ExposedPorts[key] = struct{}{}
	if hc.PortBindings == nil {
		hc.PortBindings = map[string][]docker.PortBinding{}
	}
	hc.PortBindings[key] = append(hc.PortBindings[key], docker.PortBinding{HostIP: "127.0.0.1", HostPort: "0"})
}

// HostPortFrom reads the ephemeral host port ApplyPortBinding asked for from
// an inspect response, for the native case where the container's own IP is
// not routable from Overcast.
func (t *Target) HostPortFrom(inspect *docker.ContainerInspect) (int, bool) {
	if inspect == nil || !t.bound() {
		return 0, false
	}
	for _, b := range inspect.NetworkSettings.Ports[t.portKey()] {
		if port, err := strconv.Atoi(b.HostPort); err == nil && port > 0 {
			return port, true
		}
	}
	return 0, false
}

func (t *Target) portKey() string {
	return strconv.Itoa(t.Port()) + "/tcp"
}

// Manager is the registry of live targets and the owner of their ports.
// Services call Ensure when a resource is about to run and Release when it is
// gone; Close is shutdown.
type Manager struct {
	clk    clock.Clock
	log    *zap.Logger
	host   string
	ports  [2]int
	policy TimeoutPolicy

	mu      sync.Mutex
	targets map[string]*Target
	closed  bool
}

// NewManager builds a manager binding on host, allocating auto ports from
// the inclusive range ports, and applying policy to deadlines.
func NewManager(clk clock.Clock, log *zap.Logger, host string, ports [2]int, policy TimeoutPolicy) *Manager {
	if log == nil {
		log = zap.NewNop()
	}
	return &Manager{
		clk:     clk,
		log:     log,
		host:    host,
		ports:   ports,
		policy:  policy,
		targets: map[string]*Target{},
	}
}

// ErrClosed is returned by Ensure after Close.
var ErrClosed = errors.New("debugger: manager closed")

// ErrNothingToDebug is returned by Ensure when the resolution names no
// protocol and no tag asked for one: there is no target to make.
var ErrNothingToDebug = errors.New("debugger: nothing to debug")

// Ensure returns the target for id, creating it on first call. A second
// call with the same spec and resolution returns the same target; a call
// with a different one — the tags changed — replaces it, which is how a
// port change takes effect without a service tracking the old value.
//
// A spec whose flag is off yields an inert target: no listener, no goroutine,
// a reason the console shows. A spec whose fixed port is taken yields a
// target in StateError with the reason — never a silent fallback port,
// because the user typed that port into an editor.
func (m *Manager) Ensure(id string, spec Spec, res Resolution) (*Target, error) {
	service, resource, container, err := parseTargetID(id)
	if err != nil {
		return nil, err
	}
	if res.Protocol == nil && !spec.Tagged {
		return nil, ErrNothingToDebug
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil, ErrClosed
	}
	if existing, ok := m.targets[id]; ok {
		if existing.sameRequest(spec, res) {
			return existing, nil
		}
		// Closed under the lock so no concurrent Ensure for the same id can
		// slip in between; nothing close waits for takes m.mu.
		delete(m.targets, id)
		existing.close()
	}

	ctx, cancel := context.WithCancel(context.Background())
	t := &Target{
		id:        id,
		service:   service,
		resource:  resource,
		container: container,
		spec:      spec,
		res:       res,
		host:      m.host,
		policy:    m.policy,
		clk:       m.clk,
		log:       m.log.With(zap.String("target", id)),
		ctx:       ctx,
		cancel:    cancel,
		conns:     map[net.Conn]struct{}{},
	}
	switch {
	case !spec.FlagOn:
		t.reason = "debugger not enabled: set " + service.FlagEnv() + "=true (or OVERCAST_DEBUGGER=true)"
	case res.Protocol == nil:
		t.reason = "no protocol resolved"
	default:
		t.enabled = true
		m.bind(t)
	}
	m.targets[id] = t
	return t, nil
}

// sameRequest reports whether a repeated Ensure asks for what the target
// already is, so the common per-invoke call is a map lookup.
func (t *Target) sameRequest(spec Spec, res Resolution) bool {
	sameProtocol := (t.res.Protocol == nil) == (res.Protocol == nil) &&
		(res.Protocol == nil || t.res.Protocol.Name() == res.Protocol.Name())
	return sameProtocol &&
		t.res.Port == res.Port &&
		t.res.Source == res.Source &&
		t.spec == spec
}

// bind opens the target's listener: the fixed port, or the lowest free port
// of the range not already held by another target. Called under m.mu so two
// targets cannot race for the same auto port.
func (m *Manager) bind(t *Target) {
	if t.res.Port != 0 {
		m.listen(t, t.res.Port)
		return
	}
	held := make(map[int]bool, len(m.targets))
	for _, other := range m.targets {
		if p := other.Port(); p != 0 {
			held[p] = true
		}
	}
	for port := m.ports[0]; port <= m.ports[1]; port++ {
		if held[port] {
			continue
		}
		if m.listen(t, port) {
			return
		}
	}
	t.mu.Lock()
	t.reason = fmt.Sprintf("no free port in %d-%d (OVERCAST_DEBUGGER_PORTS)", m.ports[0], m.ports[1])
	t.port = 0
	t.mu.Unlock()
	t.log.Warn("debugger: no free port for target", zap.String("reason", t.reason))
}

// listen binds one port and starts accepting. On failure the target keeps
// the port number and the reason, so the console can say which port was
// wanted and why it is not there.
func (m *Manager) listen(t *Target, port int) bool {
	addr := net.JoinHostPort(m.host, strconv.Itoa(port))
	ln, err := net.Listen("tcp", addr)
	t.mu.Lock()
	t.port = port
	if err != nil {
		t.reason = fmt.Sprintf("cannot listen on %s: %v", addr, err)
		t.mu.Unlock()
		return false
	}
	t.reason = ""
	t.listener = ln
	t.mu.Unlock()
	t.wg.Add(1)
	go t.serve(ln)
	t.log.Info("debugger: listening", zap.String("addr", addr), zap.String("protocol", t.res.Protocol.Name()))
	return true
}

// Get returns the target registered under id.
func (m *Manager) Get(id string) (*Target, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.targets[id]
	return t, ok
}

// List returns every target, ordered by id, so the console's list is stable
// between polls.
func (m *Manager) List() []*Target {
	m.mu.Lock()
	targets := make([]*Target, 0, len(m.targets))
	for _, t := range m.targets {
		targets = append(targets, t)
	}
	m.mu.Unlock()
	sort.Slice(targets, func(i, j int) bool { return targets[i].id < targets[j].id })
	return targets
}

// Release closes the target's listener and every connection through it and
// forgets it. A second Release, or one for an unknown id, does nothing.
func (m *Manager) Release(id string) {
	m.mu.Lock()
	t, ok := m.targets[id]
	delete(m.targets, id)
	m.mu.Unlock()
	if ok {
		t.close()
	}
}

// Close releases every target and refuses further Ensure calls.
func (m *Manager) Close() {
	m.mu.Lock()
	m.closed = true
	targets := make([]*Target, 0, len(m.targets))
	for _, t := range m.targets {
		targets = append(targets, t)
	}
	m.targets = map[string]*Target{}
	m.mu.Unlock()
	for _, t := range targets {
		t.close()
	}
}

// close stops the listener, drops every connection, ends dials in flight and
// waits for the goroutines to exit, so a released target leaves nothing
// behind.
func (t *Target) close() {
	t.cancel()
	t.mu.Lock()
	t.closed = true
	ln := t.listener
	conns := make([]net.Conn, 0, len(t.conns))
	for c := range t.conns {
		conns = append(conns, c)
	}
	t.mu.Unlock()
	if ln != nil {
		ln.Close()
	}
	for _, c := range conns {
		c.Close()
	}
	t.wg.Wait()
	t.mu.Lock()
	t.listener = nil
	t.reason = "released"
	t.mu.Unlock()
}
