package lambda

// debugger.go — the Lambda side of internal/debugger: resolving a function's
// debug target from its tags — at create, tag and update time so the console
// sees it before the first invocation, and again at the cold start, which is
// what bakes it into the container — wiring the container to the target's
// port, pinning a debugged function to one execution environment, holding an
// invocation for a client when overcast:debug-wait asks, and bounding an
// invocation by a clock a debugger can stop. See docs/plans/compute-debugger.md
// § 4 and docs/plans/compute-debugger-console.md § 6.
//
// Every entry point here is nil-safe against a service built without a
// Manager. The invoke path costs a function nothing asked to debug one nil
// check for its deadline — the target rides on the containerInstance from the
// cold start that registered it — and, only while OVERCAST_LAMBDA_DEBUGGER is
// on, one registry lookup at admission for the instance pin.

import (
	"context"
	"time"

	"go.uber.org/zap"

	"github.com/overcast-sh/overcast/internal/clock"
	"github.com/overcast-sh/overcast/internal/config"
	"github.com/overcast-sh/overcast/internal/debugger"
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

// ensureDebugTarget resolves fn's debug target from its tags and environment
// and registers it with m, re-resolving when either changed since the last
// call (Ensure replaces a target whose request differs, and applies a changed
// overcast:debug-wait in place). Nil when nothing asks for a debugger. A tag
// that cannot be honoured is logged with its fix and dropped, so the function
// runs as it would without it — AWS accepts any tag, so no tag may fail a
// deploy or a cold start. With the flag off the target is inert: registered
// so the console can say why, with no port bound.
func ensureDebugTarget(m *debugger.Manager, cfg *config.Config, log debugger.Warner, fn *Function) *debugger.Target {
	flagOn := cfg != nil && cfg.LambdaDebugger
	spec, problems := debugger.SpecFromTags(debugger.ServiceLambda, fn.Tags, flagOn)
	debugger.WarnProblems(log, problems, zap.String("function", fn.Name))
	res := debugger.Default.Resolve(spec, fn.Runtime, fn.Environment)
	target, err := m.Ensure(debugger.TargetID(debugger.ServiceLambda, fn.Name, ""), spec, res)
	if err != nil {
		// ErrNothingToDebug is the untagged, undetected common case and is
		// not worth a line; a closed manager means Overcast is shutting down.
		// A target that could not bind its port is the manager's to warn
		// about; it is returned so the console can show the reason.
		return nil
	}
	target.SetResourceARN(fn.ARN)
	return target
}

// debugTarget registers fn's debug target at the cold start, which is where
// it takes effect: the injected flag, OVERCAST_DEBUG_PORT and the port
// binding are all baked into the container being created. Nil when the
// runtime has no manager or nothing asks for a debugger.
//
// resolvedRef and platform name the image whose working directory is the
// remote root of an image function; they are only read for one.
func (cr *ContainerRuntime) debugTarget(ctx context.Context, fn *Function, resolvedRef, platform string) *debugger.Target {
	if cr.debugger == nil {
		return nil
	}
	target := ensureDebugTarget(cr.debugger, cr.cfg, cr.logger, fn)
	if target == nil {
		return nil
	}
	target.SetRemoteRoot(cr.debugRemoteRoot(ctx, fn, resolvedRef, platform))
	return target
}

// syncDebugTarget keeps the Manager in step with fn's record outside the
// cold start — on CreateFunction, TagResource, UntagResource and
// UpdateFunctionConfiguration, and from the store scan the first ListTargets
// makes — so the console lists a tagged function, and offers a session on it,
// before it has ever run (docs/plans/compute-debugger-console.md § 6). It is
// the same Ensure the cold start makes, so the cold start finds the target
// and binds its container to it; with the flag off the target is inert and
// no port is bound.
//
// An untagged function costs a map lookup: only one that still has a target
// — the tag was just removed, or its environment carries a debug flag the
// resolver detects — is re-resolved, which keeps an env-detected target and
// releases a stale one. The remote root is set as far as the record says;
// an image's own working directory is only known to the cold start, which
// sets it then.
func (h *Handler) syncDebugTarget(ctx context.Context, fn *Function) *debugger.Target {
	if h.debugger == nil || fn == nil {
		return nil
	}
	flagOn := h.cfg != nil && h.cfg.LambdaDebugger
	if spec, _ := debugger.SpecFromTags(debugger.ServiceLambda, fn.Tags, flagOn); !spec.Tagged {
		if _, registered := h.debugger.Get(debugger.TargetID(debugger.ServiceLambda, fn.Name, "")); !registered {
			return nil
		}
	}
	target := ensureDebugTarget(h.debugger, h.cfg, h.log.WithRecorder(ctx), fn)
	if target == nil {
		return nil
	}
	if root, known := recordRemoteRoot(fn); known || target.RemoteRoot() == "" {
		target.SetRemoteRoot(root)
	}
	return target
}

// recordRemoteRoot is the remote root a function's record alone determines:
// /var/task for a zip, and an image's configured working directory. known is
// false for an image without one, whose root is the image's own WORKDIR and
// needs the cold start's inspect (debugRemoteRoot); /var/task stands in
// until then.
func recordRemoteRoot(fn *Function) (root string, known bool) {
	if fn.PackageType != "Image" {
		return lambdaTaskRoot, true
	}
	if fn.ImageConfig != nil && fn.ImageConfig.WorkingDirectory != "" {
		return fn.ImageConfig.WorkingDirectory, true
	}
	return lambdaTaskRoot, false
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

// liveDebugTarget is fn's target when an editor could attach to it: bound, so
// neither inert (flag off) nor in error (port taken). Only such a target pins
// the function to one execution environment — a port belongs to one container
// — and only such a target can stop the invocation clock.
func liveDebugTarget(m *debugger.Manager, functionName string) *debugger.Target {
	if m == nil {
		return nil
	}
	t, ok := m.Get(debugger.TargetID(debugger.ServiceLambda, functionName, ""))
	if !ok || !t.Bound() {
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

// defaultDebuggerWaitTimeout stands in for cfg.DebuggerWaitTimeout when a
// handler was built without config or with the field unset; it is
// config.Load's default.
const defaultDebuggerWaitTimeout = 120 * time.Second

// awaitDebugger holds the invocation about to run on inst until a debugger
// client is attached to its target, when the function's overcast:debug-wait
// tag asks for it (docs/plans/compute-debugger-console.md § 6). The
// environment has already been acquired and initialised — INIT cannot pause,
// so a runtime's --inspect-brk would hang there, and the plain --inspect the
// managed runtimes get lets INIT run as usual — and the hold is on the event
// alone: the container is up, its port is bound, and an editor or the
// console can attach to it while the event waits. On expiry the invocation
// proceeds undebugged, and a WARN names the function and the tag. A caller
// that goes away releases the hold; the invocation then fails as any
// cancelled one does. Under the strict policy the target skips the hold
// itself (Target.AwaitClient).
//
// This runs before boundInvocation on purpose: the invocation clock starts,
// and the nominal deadline the Runtime API sends as Lambda-Runtime-Deadline-Ms
// is computed, at dispatch, so the hold neither drains the budget nor leaves
// the function with a deadline already in the past.
func (h *Handler) awaitDebugger(ctx context.Context, fn *Function, inst RuntimeInstance) {
	target := debugTargetOf(inst)
	if target == nil {
		return
	}
	timeout := defaultDebuggerWaitTimeout
	if h.cfg != nil && h.cfg.DebuggerWaitTimeout > 0 {
		timeout = h.cfg.DebuggerWaitTimeout
	}
	switch target.AwaitClient(ctx, timeout) {
	case debugger.WaitAttached:
		h.log.WithRecorder(ctx).Debug("debugger: client attached — dispatching the held invocation",
			zap.String("function", fn.Name))
	case debugger.WaitExpired:
		h.log.WithRecorder(ctx).Warn("debugger: no client attached within the wait timeout — invoking without one",
			zap.String("function", fn.Name),
			zap.String("tag", debugger.TagWait),
			zap.Duration("timeout", timeout),
			zap.String("hint", "attach an editor or open the console's Code tab before invoking, raise OVERCAST_DEBUGGER_WAIT_TIMEOUT, or drop the tag"))
	case debugger.WaitSkipped, debugger.WaitCancelled:
	}
}

// invocationContext is the one seam every invocation mechanism dispatches
// through — sync, async, the console's SSE invoke, and function URLs,
// streaming and the in-process ServiceInvoker, which reach one of those — so
// the hold and the bound are applied in the same order everywhere: hold for
// a debugger first (awaitDebugger), then start the function's clock
// (boundInvocation).
func (h *Handler) invocationContext(ctx context.Context, fn *Function, inst RuntimeInstance) (context.Context, context.CancelFunc) {
	h.awaitDebugger(ctx, fn, inst)
	return boundInvocation(ctx, h.clk, h.cfg, functionTimeout(fn), inst)
}
