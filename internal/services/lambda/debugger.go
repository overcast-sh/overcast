package lambda

// debugger.go — the Lambda side of internal/debugger: resolving a function's
// debug target from its tags at cold start, wiring the container to the
// target's port, pinning a debugged function to one execution environment,
// and bounding an invocation by a clock a debugger can stop. See
// docs/plans/compute-debugger.md § 4.
//
// Every entry point here is nil-safe against a service built without a
// Manager, and a function nothing asked to debug costs the invoke path one nil
// check: the target rides on the containerInstance from the cold start that
// registered it, so no lookup happens per invoke.

import (
	"context"
	"time"

	"go.uber.org/zap"

	"github.com/overcast-sh/overcast/internal/clock"
	"github.com/overcast-sh/overcast/internal/config"
	"github.com/overcast-sh/overcast/internal/dataplane"
	"github.com/overcast-sh/overcast/internal/debugger"
	"github.com/overcast-sh/overcast/internal/docker"
)

// lambdaTaskRoot is where a zip deployment's code lands in the container, and
// the remote root editors map a local source tree to when an image declares
// no working directory of its own.
const lambdaTaskRoot = "/var/task"

// debugTagKeys are the tags that reach the container — as an injected flag,
// OVERCAST_DEBUG_PORT and a port binding — so a change to any of them retires
// the execution environment the way an environment-variable edit does.
// overcast:source-path is deliberately absent: it only shapes the descriptor.
var debugTagKeys = [...]string{debugger.TagDebug, debugger.TagPort, debugger.TagProtocol}

// SetDebugger wires the debug-target manager. Without one, functions run
// exactly as they did before the debugger existed.
func (cr *ContainerRuntime) SetDebugger(m *debugger.Manager) { cr.debugger = m }

// debugTarget resolves fn's debug target from its tags and registers it,
// re-resolving when the tags changed since the last cold start (Ensure
// replaces a target whose request differs). Nil when the runtime has no
// manager or nothing asks for a debugger. A tag that cannot be honoured is
// logged with its fix and dropped, so the function runs as it would without
// it — AWS accepts any tag, so no tag may fail a cold start.
//
// resolvedRef and platform name the image whose working directory is the
// remote root of an image function; they are only read for one.
func (cr *ContainerRuntime) debugTarget(ctx context.Context, fn *Function, resolvedRef, platform string) *debugger.Target {
	if cr.debugger == nil {
		return nil
	}
	spec, problems := debugger.SpecFromTags(debugger.ServiceLambda, fn.Tags, cr.cfg.LambdaDebugger)
	for _, p := range problems {
		cr.logger.Warn("debugger: "+p.Reason, append([]zap.Field{zap.String("function", fn.Name)}, p.Fields()...)...)
	}
	res := debugger.Default.Resolve(spec, fn.Runtime, fn.Environment)
	target, err := cr.debugger.Ensure(debugger.TargetID(debugger.ServiceLambda, fn.Name, ""), spec, res)
	if err != nil {
		// ErrNothingToDebug is the untagged, undetected common case and is
		// not worth a line; a closed manager means Overcast is shutting down.
		return nil
	}
	target.SetResourceARN(fn.ARN)
	target.SetRemoteRoot(cr.debugRemoteRoot(ctx, fn, resolvedRef, platform))
	if target.State() == debugger.StateError {
		cr.logger.Warn("debugger: target could not be bound — the function runs undebugged",
			zap.String("function", fn.Name),
			zap.String("reason", target.Reason()),
			zap.String("hint", "free the port named by "+debugger.TagPort+", or drop the tag to auto-allocate one from OVERCAST_DEBUGGER_PORTS"))
	}
	return target
}

// debugRemoteRoot is the container path editors map the local source root
// to: /var/task for a zip, and for an image the ImageConfig working directory,
// else the image's own WORKDIR, else /var/task again.
func (cr *ContainerRuntime) debugRemoteRoot(ctx context.Context, fn *Function, resolvedRef, platform string) string {
	if fn.PackageType != "Image" {
		return lambdaTaskRoot
	}
	if fn.ImageConfig != nil && fn.ImageConfig.WorkingDirectory != "" {
		return fn.ImageConfig.WorkingDirectory
	}
	// Cached beside the entrypoint lookup the cold start already made, so
	// this is a map read, not a daemon round trip.
	if imageCfg, err := cr.imageConfig(ctx, resolvedRef, platform); err == nil && imageCfg.WorkingDir != "" {
		return imageCfg.WorkingDir
	}
	return lambdaTaskRoot
}

// bindDebugTarget points the target's proxy at the started container, per
// docs/plans/compute-debugger.md § 3.6: the container's own IP when Overcast
// can route to it (Overcast in Docker), else the ephemeral loopback port
// Docker published for the debug port. Failure to find either leaves the
// target unbound with a warning — the function still runs.
func (cr *ContainerRuntime) bindDebugTarget(ctx context.Context, target *debugger.Target, containerID string) {
	var inspect *docker.ContainerInspect
	containerAddr := dataplane.ContainerAddr(ctx, cr.docker, cr.cfg, containerID)
	if containerAddr == "" {
		// Host ports are assigned at start, so the create-time inspect the
		// cold start already made cannot have carried this one.
		var err error
		if inspect, err = cr.docker.InspectContainer(ctx, containerID); err != nil {
			cr.logger.Warn("debugger: inspect started container for its published debug port — target left unbound",
				zap.String("container", shortContainerID(containerID)), zap.Error(err))
			return
		}
	}
	upstream, ok := target.UpstreamFor(containerAddr, inspect)
	if !ok {
		cr.logger.Warn("debugger: container published no host port for the debug port — target left unbound",
			zap.String("container", shortContainerID(containerID)),
			zap.Int("port", target.Port()),
			zap.String("hint", "check that the Docker daemon can publish ports on 127.0.0.1 (a rootless or remote daemon may not)"))
		return
	}
	target.SetUpstream(upstream)
	target.SetContainerID(containerID)
	cr.logger.Debug("debugger: target bound", zap.String("target", target.ID()), zap.String("upstream", upstream))
}

// liveDebugTarget is fn's target when an editor could attach to it: enabled
// and bound, so neither inert (flag off) nor in error (port taken). Only such
// a target pins the function to one execution environment — a port belongs
// to one container — and only such a target can stop the invocation clock.
func liveDebugTarget(m *debugger.Manager, functionName string) *debugger.Target {
	if m == nil {
		return nil
	}
	t, ok := m.Get(debugger.TargetID(debugger.ServiceLambda, functionName, ""))
	if !ok || !t.Enabled() || t.State() == debugger.StateError {
		return nil
	}
	return t
}

// releaseDebugTarget forgets a deleted function's target and closes its port.
func (h *Handler) releaseDebugTarget(functionName string) {
	if h.debugger != nil {
		h.debugger.Release(debugger.TargetID(debugger.ServiceLambda, functionName, ""))
	}
}

// debugTargetInstance is implemented by an execution environment that was
// created for a debug target. Every other RuntimeInstance has none.
type debugTargetInstance interface {
	DebugTarget() *debugger.Target
}

// debugTargetOf is the target an instance was created for, or nil.
func debugTargetOf(inst RuntimeInstance) *debugger.Target {
	if d, ok := inst.(debugTargetInstance); ok {
		return d.DebugTarget()
	}
	return nil
}

// boundInvocation bounds ctx by timeout for the invocation about to run on
// inst. With a debug target it is the suspendable deadline the policy
// describes; without one it is exactly context.WithTimeout, so an undebugged
// invocation pays a nil check and nothing else. The context's Deadline() is
// the nominal one either way, which is what Lambda-Runtime-Deadline-Ms
// reports.
func boundInvocation(ctx context.Context, clk clock.Clock, cfg *config.Config, timeout time.Duration, inst RuntimeInstance) (context.Context, context.CancelFunc) {
	policy := config.DebuggerTimeoutAttached
	if cfg != nil {
		policy = cfg.DebuggerTimeout
	}
	return debugger.WithDeadline(ctx, clk, timeout, debugTargetOf(inst), policy)
}
