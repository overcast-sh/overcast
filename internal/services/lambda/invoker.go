package lambda

// invoker.go — ServiceInvoker implements events.FunctionInvoker,
// events.FunctionEventInvoker and events.FunctionSyncInvoker.
//
// InvokeEvent is called by any service that needs to trigger a Lambda function
// asynchronously — the S3 notification dispatcher, EventBridge/Scheduler
// targets, SNS subscription fan-out. It is the in-process spelling of an HTTP
// `InvocationType=Event` invoke and behaves identically: it validates what real
// Lambda validates before answering 202, records the invocation, hands the work
// to the same async machinery the HTTP path uses (Handler.startAsync), and
// returns. It does not wait for the function to run.
//
// Sharing that machinery is the point. It was duplicated here once, and the
// copy diverged in the one place it mattered: a throttle. The HTTP path retries
// a throttled Event invocation internally, because AWS answered 202 long before
// the throttle happened and never reports it to the caller. The copy returned
// it — and SNS reads a returned error as a delivery failure, so a function
// reserved to zero concurrency dead-lettered notifications that the very same
// function, invoked over HTTP, would have retried into.
//
// InvokeAsync is the same call for callers that do not act on the outcome: it
// swallows the error, matching AWS behaviour where a misconfigured notification
// silently fails rather than breaking the originating operation. SNS does act
// on it — an undeliverable notification has to reach the subscription's
// dead-letter queue — so it calls InvokeEvent directly.

import (
	"context"
	"fmt"
	"time"

	"github.com/overcast-sh/overcast/internal/clock"
	"github.com/overcast-sh/overcast/internal/config"
	"github.com/overcast-sh/overcast/internal/events"
	"github.com/overcast-sh/overcast/internal/middleware"
	"go.uber.org/zap"
)

// ServiceInvoker implements events.FunctionInvoker for the Lambda service.
type ServiceInvoker struct {
	// h is the Lambda handler whose async machinery InvokeEvent hands accepted
	// events to, so an in-process event and an HTTP `InvocationType=Event`
	// invoke run on exactly one implementation.
	h         *Handler
	store     *lambdaStore
	runtimes  *runtimeRegistry
	logger    *zap.Logger
	tracker   *instanceTracker
	logWriter events.LogWriter // nil until InitLogWriter is called
	cfg       *config.Config
	bus       *events.Bus // nil until InitBus is called
	clk       clock.Clock // nil until InitBus is called
}

// newServiceInvoker creates a new ServiceInvoker.
func newServiceInvoker(h *Handler, store *lambdaStore, runtimes *runtimeRegistry, logger *zap.Logger, tracker *instanceTracker) *ServiceInvoker {
	return &ServiceInvoker{h: h, store: store, runtimes: runtimes, logger: logger, tracker: tracker}
}

// InitBus wires the event bus and clock so the invoker can publish
// ServiceError events for invocation failures that would otherwise only
// appear in server logs.
func (inv *ServiceInvoker) InitBus(b *events.Bus, clk clock.Clock) {
	inv.bus = b
	inv.clk = clk
}

// publishError emits a ServiceError event onto the bus. It is a no-op when
// the bus has not been wired (e.g. in unit tests that don't need it).
func (inv *ServiceInvoker) publishError(ctx context.Context, operation, message, code string) {
	if inv.bus == nil {
		return
	}
	var t time.Time
	if inv.clk != nil {
		t = inv.clk.Now()
	} else {
		t = time.Now()
	}
	inv.bus.Publish(ctx, events.Event{
		Type:   events.ServiceError,
		Time:   t,
		Source: "lambda",
		Payload: events.ErrorPayload{
			Service:   "lambda",
			Operation: operation,
			Message:   message,
			Code:      code,
		},
	})
}

// InvokeAsync satisfies events.FunctionInvoker.
// It is safe to call from any goroutine, and returns as soon as the event is
// accepted or refused — never after the function has run.
//
// Delivery problems are logged and swallowed: S3 notification configs,
// EventBridge targets and Scheduler targets all treat a misconfigured
// destination the way AWS does — the originating operation still succeeds.
// Callers that need the outcome use InvokeEvent instead.
func (inv *ServiceInvoker) InvokeAsync(ctx context.Context, functionARN string, payload []byte) error {
	if err := inv.InvokeEvent(ctx, functionARN, payload); err != nil {
		inv.logger.Debug("lambda: invokeAsync: event not accepted",
			zap.String("arn", functionARN),
			zap.Error(err),
		)
	}
	return nil
}

// InvokeEvent satisfies events.FunctionEventInvoker.
// It is safe to call from any goroutine.
//
// It accepts or refuses the event and returns; it never waits for the function.
// A non-nil error means the event was refused and never queued, so the caller
// still owns it — SNS dead-letters it against the subscription's RedrivePolicy.
// The refusals are the ones an HTTP `InvocationType=Event` invoke answers before
// its 202: the function does not exist, or is not in an invokable state. Two
// more are Overcast's own, and are refusals rather than silent drops because a
// notification that vanishes is worse than one that fails loudly — a missing
// layer version, and a declared runtime the emulator cannot execute.
//
// A throttle is deliberately *not* among them. AWS answered 202 before any
// concurrency was needed and retries internally; startAsync's acquireForAsync
// does the same. Handing it back would dead-letter an event AWS would have run.
//
// Everything after acceptance — the cold start, the throttle retry, a handler
// that raises — is Lambda's, and is reported to the caller as a successful
// delivery. Overcast has no async retry or DeadLetterConfig yet, so a handler
// exception currently reaches nothing but the server logs; this seam is where
// that machinery will go.
func (inv *ServiceInvoker) InvokeEvent(ctx context.Context, functionARN string, payload []byte) error {
	name := functionNameFromARN(functionARN)

	fn, aerr := inv.store.getFunction(ctx, name)
	if aerr != nil {
		inv.logger.Debug("lambda: invokeEvent: state lookup failed",
			zap.String("arn", functionARN),
			zap.String("error", aerr.Message),
		)
		return fmt.Errorf("lambda %s: %s", name, aerr.Message)
	}
	if fn == nil {
		inv.logger.Debug("lambda: invokeEvent: function not found",
			zap.String("arn", functionARN),
			zap.String("name", name),
		)
		return fmt.Errorf("lambda %s: function not found", name)
	}
	if aerr := checkInvokableState(fn); aerr != nil {
		msg := invokableStateMessage(fn.State)
		inv.logger.Warn("lambda: invokeEvent: function not invokable",
			zap.String("function", name),
			zap.String("state", fn.State),
			zap.String("reason", msg),
		)
		inv.publishError(ctx, "Invoke", name+": "+msg, aerr.Code)
		return fmt.Errorf("lambda %s: %s", name, msg)
	}
	if badLayer := inv.h.checkLayerVersionsExist(ctx, fn); badLayer != "" {
		msg := "layer version not found: " + badLayer
		inv.logger.Warn("lambda: invokeEvent: missing layer version",
			zap.String("function", name),
			zap.String("layer", badLayer),
		)
		inv.publishError(ctx, "Invoke", name+": "+msg, "ResourceNotFoundException")
		return fmt.Errorf("lambda %s: %s", name, msg)
	}

	// Record the invocation before executing. This makes delivery observable
	// in tests even when the runtime cannot run (e.g. missing code zip). The
	// HTTP path records here too — before the 202, not after it.
	if err := inv.store.addInvocation(ctx, fn, payload); err != nil {
		inv.logger.Warn("lambda: invokeEvent: failed to record invocation",
			zap.String("function", name),
			zap.Error(err),
		)
		// Non-fatal: continue to attempt execution.
	}

	// Find a runtime that can handle the function.
	rt := inv.runtimes.runtimeFor(ctx, fn.Runtime)
	if rt == nil {
		msg := "no runtime available for " + fn.Runtime
		inv.logger.Warn("lambda: invokeEvent: no runtime for function",
			zap.String("function", name),
			zap.String("runtime", fn.Runtime),
		)
		inv.publishError(ctx, "Invoke", name+": "+msg, "InvalidRuntimeException")
		return fmt.Errorf("lambda %s: %s", name, msg)
	}

	// Accepted. From here the event belongs to Lambda: startAsync owns the
	// throttle retry, the instance tracking and the shutdown drain, and the
	// caller's goroutine is released rather than parked on a cold start.
	if !inv.h.startAsync(fn, rt, payload) {
		// Lambda has already drained. S3, Scheduler and the event bus all stop
		// after it, so this is reachable, and the event really is undelivered —
		// say so rather than reporting a delivery that will never happen.
		return fmt.Errorf("lambda %s: shutting down, event not accepted", name)
	}
	return nil
}

// Invoke executes the named function synchronously and returns the result.
// Satisfies events.FunctionSyncInvoker.
// If the function is not found, no runtime is available, or the container
// fails to start, (nil, nil) is returned and the issue is logged — consistent
// with InvokeAsync's fail-silent approach for missing configuration.
//
// A non-nil *events.InvokeOutcome with FunctionError != "" means the function ran
// but returned a handled or unhandled error; the caller should decide whether
// to retry or discard the event.
func (inv *ServiceInvoker) Invoke(ctx context.Context, functionName string, payload []byte) (*events.InvokeOutcome, error) {
	fn, aerr := inv.store.getFunction(ctx, functionName)
	if aerr != nil {
		inv.logger.Debug("lambda: invoke: state lookup failed",
			zap.String("function", functionName),
			zap.String("error", aerr.Message),
		)
		return nil, nil
	}
	if fn == nil {
		inv.logger.Debug("lambda: invoke: function not found",
			zap.String("function", functionName),
		)
		return nil, nil
	}
	if aerr := checkInvokableState(fn); aerr != nil {
		msg := invokableStateMessage(fn.State)
		inv.logger.Warn("lambda: invoke: function not invokable",
			zap.String("function", functionName),
			zap.String("state", fn.State),
			zap.String("reason", msg),
		)
		inv.publishError(ctx, "Invoke", functionName+": "+msg, aerr.Code)
		return nil, nil
	}

	rt := inv.runtimes.runtimeFor(ctx, fn.Runtime)
	if rt == nil {
		inv.logger.Debug("lambda: invoke: no runtime",
			zap.String("function", functionName),
			zap.String("runtime", fn.Runtime),
		)
		return nil, nil
	}

	tracked := inv.tracker.Begin(functionName, payload)
	startedAt := inv.h.clk.Now()
	inst, err := rt.Acquire(ctx, fn)
	if err != nil {
		tracked.Abandon(err.Error())
		if _, throttled := asThrottle(err); throttled {
			inv.logger.Warn("lambda: invoke: throttled",
				zap.String("function", functionName), zap.Error(err))
			inv.h.recordInvocationOutcome(ctx, functionName, startedAt, 0, true, false)
			return nil, err
		}
		inv.logger.Error("lambda: invoke: acquire instance failed",
			zap.String("function", functionName),
			zap.Error(err),
		)
		inv.h.recordInvocationOutcome(ctx, functionName, startedAt, 0, false, true)
		return nil, err
	}
	tracked.Bind(inst)
	tracked.Ready()
	if err := awaitRuntimeReady(ctx, inv.h.clk, inv.cfg, inst); err != nil {
		rt.Release(ctx, inst, false)
		tracked.Abandon(err.Error())
		inv.logger.Error("lambda: invoke: runtime init failed",
			zap.String("function", functionName),
			zap.Error(err),
		)
		inv.h.recordInvocationOutcome(ctx, functionName, startedAt, 0, false, true)
		return nil, err
	}
	tracked.Running()

	// Ensure the log stream exists so container logs are captured.
	// Use the function's own region (from its ARN) so the log stream is
	// created in the correct regional log group.
	if inv.logWriter != nil {
		fnRegion := regionFromFunctionARN(fn.ARN)
		if fnRegion == "" {
			fnRegion = inv.cfg.Region
		}
		fnCtx := middleware.ContextWithRegion(ctx, fnRegion)
		tracked.SetLogRefs(fn.logGroupName(), inst.LogStreamName())
		if lsErr := inv.logWriter.EnsureLogStream(fnCtx, fn.logGroupName(), inst.LogStreamName()); lsErr != nil {
			inv.logger.Debug("lambda: invoke: ensure log stream", zap.String("function", functionName), zap.Error(lsErr))
		}
	}

	// No tail: InvokeOutcome carries no log field, so nothing would read it.
	invokeStart := inv.h.clk.Now()
	result, err := inst.Invoke(ctx, payload, InvokeOptions{})
	invokeDuration := inv.h.clk.Now().Sub(invokeStart)
	healthy := err == nil
	rt.Release(ctx, inst, healthy)
	success, failureReason := invocationOutcome(err, result)
	tracked.Finish(success, failureReason)
	inv.h.recordInvocationOutcome(ctx, functionName, startedAt, invokeDuration, false, !success)
	if err != nil {
		return nil, err
	}

	return &events.InvokeOutcome{
		Payload:       result.Payload,
		FunctionError: result.FunctionError,
	}, nil
}
