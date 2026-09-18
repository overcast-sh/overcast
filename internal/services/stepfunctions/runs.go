package stepfunctions

import (
	"context"
	"net/http"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/overcast-sh/overcast/internal/protocol"
)

// In-flight execution tracking.
//
// StartExecution accepts and returns while the execution is still RUNNING, as
// AWS does, so the interpreter runs on a goroutine rather than on the request.
// This file is what keeps that honest: every run is registered while it is
// alive, so DescribeExecution and GetExecutionHistory can observe it
// progressing, StopExecution can interrupt it, and shutdown can drain it.

// executionRun tracks one execution while it is running.
type executionRun struct {
	// cancel unwinds the interpreter — used by StopExecution and by shutdown.
	cancel context.CancelFunc
	// hist is the live recorder. GetExecutionHistory snapshots it so a running
	// execution reports the states it has already been through.
	hist *historyRecorder

	mu        sync.Mutex
	stopped   bool
	stoppedAt time.Time
	errName   string
	cause     string

	// failedState and failedInput are the top-level state whose failure
	// ended the run and the raw input it was entered with — the point a
	// RedriveExecution resumes from.
	failedState     string
	failedInput     string
	failedVariables string

	// resume, when set, makes the interpreter start at that top-level state
	// with that raw input and those variables instead of at StartAt, and
	// resume inside the Parallel or Map there — a redrive
	// (redrive_checkpoint.go).
	resume *redrivePoint
	// checkpoint is where the top-level frame stopped without succeeding,
	// with everything below it; persistOutcome stores it.
	checkpoint *redrivePoint
	// restarted marks a distributed Map's EXPRESS child being started again
	// from the top under its old ARN, so any checkpoint it left is cleared.
	restarted bool

	// queryLanguage is the default query language when the definition run
	// does not name one: a distributed Map child inherits its Map state's.
	queryLanguage string
}

// noteFailure records the top-level state a failure ended the run in. The
// first one wins; a later unwind cannot overwrite it.
func (r *executionRun) noteFailure(state, input, variables string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.failedState == "" {
		r.failedState = state
		r.failedInput = input
		r.failedVariables = variables
	}
}

// failurePoint returns what noteFailure recorded.
func (r *executionRun) failurePoint() (state, input, variables string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.failedState, r.failedInput, r.failedVariables
}

// stop asks the execution to unwind as ABORTED, recording the error and cause
// StopExecution supplied and the moment it was asked for.
func (r *executionRun) stop(now time.Time, errName, cause string) {
	r.mu.Lock()
	if !r.stopped {
		r.stopped = true
		r.stoppedAt = now
		r.errName = errName
		r.cause = cause
	}
	r.mu.Unlock()
	r.cancel()
}

// abortReason reports whether this unwind was asked for by StopExecution, and
// with what error and cause. It is how the interpreter tells an abort apart
// from the budget running out.
func (r *executionRun) abortReason() (stopped bool, errName, cause string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.stopped, r.errName, r.cause
}

// stopTime returns the time StopExecution was called, if it was, so the
// terminal record carries the same stopDate StopExecution already returned.
func (r *executionRun) stopTime() (time.Time, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.stoppedAt, r.stopped
}

// reserveRun atomically checks that the service is not stopping and, if it
// is not, records the run as in-flight and — when trackWG is true, the
// executionAsync path — reserves a wg slot for it (the run's own goroutine
// calls wg.Done). Returns false if Stop has already begun, in which case the
// caller must not register the run, must not touch wg, and must not launch
// the run goroutine.
//
// The check and the registration/Add happen under the same lock Stop takes
// to set stopping (see the doc comment on Handler.stopping), so every Add
// this can ever produce happens strictly before Stop's critical section
// runs, which happens strictly before Stop calls wg.Wait — closing the
// Add-races-Wait window #1290 fixed in lifecycle.Scheduler and #1298 fixed
// in EKS.
func (h *Handler) reserveRun(execARN string, run *executionRun, trackWG bool) bool {
	h.runsMu.Lock()
	defer h.runsMu.Unlock()
	if h.stopping {
		return false
	}
	if h.runs == nil {
		h.runs = make(map[string]*executionRun)
	}
	h.runs[execARN] = run
	if trackWG {
		h.wg.Add(1)
	}
	return true
}

// lookupRun returns the in-flight run for an execution, or nil once it has
// finished and its terminal state is in the store.
func (h *Handler) lookupRun(execARN string) *executionRun {
	h.runsMu.Lock()
	defer h.runsMu.Unlock()
	return h.runs[execARN]
}

// releaseRun forgets a run once its terminal state has been persisted, so
// readers fall through to the store.
func (h *Handler) releaseRun(execARN string) {
	h.runsMu.Lock()
	defer h.runsMu.Unlock()
	delete(h.runs, execARN)
}

// Stop drains in-flight executions. It first marks the service stopping —
// under the same lock reserveRun uses, so no more executions can reserve a
// wg slot once this returns from the critical section below — then cancels
// them (an execution parked in a Wait would otherwise hold shutdown open for
// its whole budget), then waits for the goroutines to finish writing their
// terminal state, or until ctx expires.
//
// Satisfies router.Stopper.
func (h *Handler) Stop(ctx context.Context) {
	log := h.log.WithRecorder(ctx)
	h.runsMu.Lock()
	h.stopping = true
	h.runsMu.Unlock()
	if h.shutdownCancel != nil {
		h.shutdownCancel()
	}
	done := make(chan struct{})
	go func() {
		h.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
		if h.log != nil {
			log.Logger().Warn("stepfunctions: timed out waiting for in-flight executions to finish")
		}
	}
}

// errServiceStopping is startExecution's (and StartSyncExecution's, and a
// nested states:startExecution's) refusal once Handler.Stop has begun. AWS
// models no such error for Step Functions specifically, but a 503 is the
// generic "retry me" answer several other services already give their own
// callers for the same shutdown condition (EKS's ServiceUnavailableException
// for #1291, apigateway, backup) — SDK retry policies act on the HTTP status
// rather than needing to recognise the exact code.
func errServiceStopping() *protocol.AWSError {
	return &protocol.AWSError{
		Code:       "ServiceUnavailableException",
		Message:    "Overcast Step Functions is shutting down and is not accepting new executions.",
		HTTPStatus: http.StatusServiceUnavailable,
	}
}

// refuseStartAfterShutdown unwinds the RUNNING record startExecution already
// persisted (so the execution is visible from the moment it is accepted,
// same as on AWS) once reserveRun has reported that Stop got there first. No
// goroutine was ever launched for this run — reserveRun refused before
// touching wg — so nothing will otherwise turn this RUNNING record into a
// terminal one: left alone it would stay RUNNING forever. Instead it lands
// ABORTED with a reason, mirroring the "shut down while starting" failure
// EKS's CreateCluster fence (#1291) gives its own already-persisted record.
func (h *Handler) refuseStartAfterShutdown(ctx context.Context, exec *Execution, run *executionRun) *protocol.AWSError {
	stopped := h.clk.Now()
	const cause = "Overcast Step Functions was shutting down when this execution was accepted; it never ran."
	run.hist.add(stopped, HistoryEvent{
		Type:             evtExecutionAborted,
		ExecutionAborted: &errorCauseDetails{Error: errRuntime, Cause: cause},
	})
	exec.Status = statusAborted
	exec.StopDate = &stopped
	exec.Error = errRuntime
	exec.Cause = cause

	log := h.log.WithRecorder(ctx)
	if err := h.store.PutHistory(ctx, exec.ExecutionArn, run.hist.snapshot()); err != nil {
		log.Logger().Error("stepfunctions: could not persist history for an execution refused at shutdown",
			zap.String("execution", exec.ExecutionArn), zap.Error(err))
	}
	if err := h.store.PutExecution(ctx, exec); err != nil {
		log.Logger().Error("stepfunctions: could not persist an execution refused at shutdown",
			zap.String("execution", exec.ExecutionArn), zap.Error(err))
	}
	return errServiceStopping()
}
