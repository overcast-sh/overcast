// Package debugger attaches a step debugger to user code running inside
// emulated compute — a Lambda function's handler, an ECS container's process
// — without touching the AWS-facing API (docs/plans/compute-debugger.md).
//
// It exists once, shared by every compute service, because hot reload was
// implemented twice and drifted. A service touches only Spec, Registry.Resolve,
// Manager.Ensure, Target.SetUpstream/ClearUpstream, Manager.Release and
// WithDeadline; everything protocol-specific — the flag to inject, how to
// tell a pause from a running program, what to paste into an editor — lives
// behind Protocol, so adding a runtime is one file that registers itself.
package debugger

import (
	"strconv"
	"strings"
)

// Protocol is one debugger wire protocol. Adding one is a file that
// registers itself in Default; no service, proxy or console code changes.
type Protocol interface {
	Name() string                             // "inspector", "jdwp", "dap", "passthrough"
	Runtimes() []string                       // AWS runtime prefixes: "nodejs", "java", "python"; nil for none
	Inject(env map[string]string, port int)   // append the listen flag; never clobber a user value
	Detect(env map[string]string) (int, bool) // an existing user flag → implicit enable
	Editors() []EditorTemplate                // per-editor templates, rendered by descriptor.go
}

// PauseObserver is optional. A protocol implementing it can tell a pause from
// a running program, which OVERCAST_DEBUGGER_TIMEOUT=paused uses.
type PauseObserver interface {
	NewObserver() Observer
}

// Observer sees one connection's bytes. FromServer is fed the container→client
// direction; it must tolerate arbitrary chunking and never block or fail —
// an unparseable stream simply reports nothing.
type Observer interface {
	FromServer(b []byte) (paused, resumed bool)
}

// Source says how a target's protocol was chosen, so the console can explain
// a surprising choice ("env: NODE_OPTIONS already carried --inspect").
type Source string

const (
	// SourceTag means overcast:debug-protocol named the protocol.
	SourceTag Source = "tag"
	// SourceRuntime means the AWS runtime identifier matched a protocol.
	SourceRuntime Source = "runtime"
	// SourceEnv means a debug flag already in the environment was recognised.
	SourceEnv Source = "env"
	// SourceFallback means nothing matched and passthrough was chosen, which
	// is where images and custom runtimes land.
	SourceFallback Source = "fallback"
)

// Resolution is the protocol a target speaks and the port the container will
// listen on. A nil Protocol means there is nothing to debug.
type Resolution struct {
	Protocol Protocol
	// Port is the container-side port: the tag's fixed port, or the one a
	// pre-existing user flag names, or 0 for auto-allocation.
	Port   int
	Source Source
}

// Registry is the ordered set of protocols resolution consults. Order matters
// only for Detect, where the first protocol to recognise the environment wins.
type Registry struct {
	protocols []Protocol
}

// NewRegistry builds a registry from the given protocols, in order.
func NewRegistry(protocols ...Protocol) *Registry {
	return &Registry{protocols: protocols}
}

// Default is the registry every service uses: the protocols this package
// ships, with passthrough last so it never shadows a real detection.
var Default = NewRegistry(inspector{}, jdwp{}, dap{}, passthrough{})

// Lookup finds a protocol by name.
func (r *Registry) Lookup(name string) (Protocol, bool) {
	for _, p := range r.protocols {
		if p.Name() == name {
			return p, true
		}
	}
	return nil, false
}

// Names lists the registered protocol names, for messages that tell the user
// what overcast:debug-protocol accepts.
func (r *Registry) Names() []string {
	names := make([]string, 0, len(r.protocols))
	for _, p := range r.protocols {
		names = append(names, p.Name())
	}
	return names
}

// Resolve picks the protocol for a resource, in the order the plan fixes:
// the tag, then the runtime, then a flag already present in the environment,
// then passthrough for anything tagged, then nothing.
//
// Only the environment step applies to an untagged resource: a runtime match
// alone would give every Node function a target, and the whole point of the
// tag is that nothing costs anything until someone asks.
//
// Whenever a protocol is chosen by tag or runtime, its Detect still runs:
// Inject never clobbers a user's own flag, so the port that flag names is the
// port the container will actually listen on, and the proxy must dial it.
func (r *Registry) Resolve(spec Spec, runtime string, env map[string]string) Resolution {
	if spec.Tagged {
		if p, ok := r.Lookup(spec.Protocol); ok {
			return r.resolved(p, SourceTag, spec, env)
		}
		if p := r.byRuntime(runtime); p != nil {
			return r.resolved(p, SourceRuntime, spec, env)
		}
	}
	for _, p := range r.protocols {
		if port, ok := p.Detect(env); ok {
			return Resolution{Protocol: p, Port: port, Source: SourceEnv}
		}
	}
	if spec.Tagged {
		if p, ok := r.Lookup(passthroughName); ok {
			return Resolution{Protocol: p, Port: spec.Port, Source: SourceFallback}
		}
	}
	return Resolution{}
}

func (r *Registry) resolved(p Protocol, source Source, spec Spec, env map[string]string) Resolution {
	port := spec.Port
	if detected, ok := p.Detect(env); ok {
		port = detected
	}
	return Resolution{Protocol: p, Port: port, Source: source}
}

// byRuntime matches an AWS runtime identifier ("nodejs20.x") against each
// protocol's prefixes. Prefix rather than exact so a new runtime version
// needs no change here.
func (r *Registry) byRuntime(runtime string) Protocol {
	runtime = strings.ToLower(runtime)
	if runtime == "" {
		return nil
	}
	for _, p := range r.protocols {
		for _, prefix := range p.Runtimes() {
			if strings.HasPrefix(runtime, prefix) {
				return p
			}
		}
	}
	return nil
}

// appendOption adds flag to a space-separated option variable, creating the
// variable when absent and otherwise appending with a single space. It is the
// one place the "never clobber a user value" rule is implemented.
func appendOption(env map[string]string, key, flag string) {
	if existing := strings.TrimSpace(env[key]); existing != "" {
		env[key] = existing + " " + flag
		return
	}
	env[key] = flag
}

// hostPort parses "[host:]port" as debug flags spell it — "9229",
// "0.0.0.0:9229", "*:9229", "[::]:9229" — returning the port. The host is
// discarded: the proxy dials the container by its own address, and the
// container-side listen host is the user's business.
func hostPort(s string) (int, bool) {
	s = strings.TrimSpace(s)
	if i := strings.LastIndexByte(s, ':'); i >= 0 {
		s = s[i+1:]
	}
	port, err := strconv.Atoi(s)
	if err != nil || port < 1 || port > 65535 {
		return 0, false
	}
	return port, true
}
