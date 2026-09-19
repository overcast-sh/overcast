package stepfunctions

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"go.uber.org/zap"

	"github.com/overcast-sh/overcast/internal/middleware"
)

// Durable executions: what survives an Overcast restart.
//
// Executions run on in-process goroutines and their history is held in memory
// until they finish (history.go), so without help a restart loses every
// RUNNING execution — including one parked on a task token, which on AWS can
// wait for up to a year. This file closes that gap at the points where an
// execution is actually waiting on the outside world:
//
//   - a top-level Task waiting on its `.waitForTaskToken` token,
//   - a top-level Task waiting on an activity worker, and
//   - a top-level Wait state of at least durableWaitThreshold.
//
// Parking at one of them writes the history so far and a small checkpoint
// record (the state, its raw input, variables, Retry position, token and
// deadlines). On the first Step Functions request after a restart (lazily —
// see Handler.ensureRehydrated) every checkpoint is rehydrated: the token is
// registered again, an unclaimed activity task is queued again, and the
// execution continues from the parked state with its history, Retry/Catch and
// ResultPath handling intact. Heartbeat, TimeoutSeconds and Wait deadlines are
// absolute times on the injected clock, so time spent down counts, as it
// would on AWS.
//
// Everything else — a synchronous Task in flight, a token waited on inside a
// Parallel branch or Map iteration, a distributed Map, an EXPRESS workflow,
// the child a `.sync` parent is blocked on — is not resumable. An execution
// caught in one of those by a shutdown ends FAILED with a States.Runtime cause
// that names the restart, and it is redrivable from that state; one caught by
// a crash (no shutdown ran) is failed the same way the first time it is read
// after the restart (reapIfOrphaned). Nothing is ever left RUNNING forever.
//
// Write cost: nothing extra for an execution that never parks. Parking costs
// two writes (history, checkpoint) and leaving the park one delete; an
// activity pick-up rewrites both, a heartbeat rewrites the checkpoint at most
// once per half HeartbeatSeconds, and a shutdown rewrites each parked
// execution's checkpoint once.

// parkPrefix keys checkpoints. Unlike the other records it is not region
// scoped: rehydration must find every parked execution in one prefix scan,
// and the checkpoint carries its region and the (globally unique) execution
// ARN itself.
const parkPrefix = "park:"

// Park kinds.
const (
	parkCallback = "callback"
	parkActivity = "activity"
	parkWait     = "wait"
)

// durableWaitThreshold is the shortest Wait that is checkpointed. Shorter
// waits are over before a restart could matter, and are not worth two writes.
const durableWaitThreshold = time.Second

// checkpointStateMachine is the definition the execution was running, kept
// with the checkpoint so an UpdateStateMachine after the park does not change
// what a resumed execution runs — as on AWS, where an execution always
// finishes on the definition it started with.
type checkpointStateMachine struct {
	ARN        string `json:"ARN"`
	Name       string `json:"Name"`
	Definition string `json:"Definition"`
	RoleArn    string `json:"RoleArn"`
	Type       string `json:"Type"`
}

// executionCheckpoint is the persisted record of one parked execution.
type executionCheckpoint struct {
	ExecutionArn  string                 `json:"ExecutionArn"`
	Region        string                 `json:"Region"`
	StateMachine  checkpointStateMachine `json:"StateMachine"`
	Depth         int                    `json:"Depth,omitempty"`
	QueryLanguage string                 `json:"QueryLanguage,omitempty"`

	// Point is the top-level state parked in, the raw input it was entered
	// with and the variables in scope — the same resume point a redrive
	// starts from (redrive_checkpoint.go), which is what a rehydrated run is
	// handed. EnteredTime is when the state was entered.
	Point       redrivePoint `json:"Point"`
	EnteredTime time.Time    `json:"EnteredTime"`
	// RetryAttempts and RetryCount are the Task's Retry position: attempts
	// used per retrier and $$.State.RetryCount.
	RetryAttempts []int `json:"RetryAttempts,omitempty"`
	RetryCount    int   `json:"RetryCount,omitempty"`
	// HistoryLength is how many events the history written with this
	// checkpoint holds; rehydration resumes after exactly those.
	HistoryLength int `json:"HistoryLength"`

	Kind string `json:"Kind"`

	// Callback and activity tasks.
	Token             string     `json:"Token,omitempty"`
	ActivityArn       string     `json:"ActivityArn,omitempty"`
	ActivityInput     string     `json:"ActivityInput,omitempty"`
	PickedUp          bool       `json:"PickedUp,omitempty"`
	WorkerName        string     `json:"WorkerName,omitempty"`
	HeartbeatSeconds  *int64     `json:"HeartbeatSeconds,omitempty"`
	HeartbeatDeadline *time.Time `json:"HeartbeatDeadline,omitempty"`
	TimeoutSeconds    *int64     `json:"TimeoutSeconds,omitempty"`
	TimeoutDeadline   *time.Time `json:"TimeoutDeadline,omitempty"`

	// Wait states.
	WaitUntil *time.Time `json:"WaitUntil,omitempty"`
}

// ─── Store ────────────────────────────────────────────────────────────────────

// PutCheckpoint saves an execution's checkpoint.
func (st *Store) PutCheckpoint(ctx context.Context, cp *executionCheckpoint) error {
	raw, err := json.Marshal(cp)
	if err != nil {
		return fmt.Errorf("stepfunctions: marshal checkpoint %q: %w", cp.ExecutionArn, err)
	}
	return st.s.Set(ctx, storeNS, parkPrefix+cp.ExecutionArn, string(raw))
}

// DeleteCheckpoint removes an execution's checkpoint, if it has one.
func (st *Store) DeleteCheckpoint(ctx context.Context, execARN string) error {
	return st.s.Delete(ctx, storeNS, parkPrefix+execARN)
}

// ListCheckpoints returns every checkpoint in every region. A record that
// cannot be decoded is skipped and its key reported in malformed, so one bad
// record never stops the others from resuming.
func (st *Store) ListCheckpoints(ctx context.Context) (checkpoints []*executionCheckpoint, malformed []string, err error) {
	pairs, err := st.s.Scan(ctx, storeNS, parkPrefix)
	if err != nil {
		return nil, nil, fmt.Errorf("stepfunctions: scan checkpoints: %w", err)
	}
	for _, p := range pairs {
		var cp executionCheckpoint
		if json.Unmarshal([]byte(p.Value), &cp) != nil || cp.ExecutionArn == "" || cp.Point.State == "" {
			malformed = append(malformed, p.Key)
			continue
		}
		checkpoints = append(checkpoints, &cp)
	}
	return checkpoints, malformed, nil
}

// ─── Parking ──────────────────────────────────────────────────────────────────

// parkHandle is one live park point. Every method is safe on a nil handle,
// which is what a non-durable execution gets, so callers never branch.
//
// All of its writes happen on the execution's own goroutine — the park, the
// heartbeat and pick-up updates (delivered to that goroutine over the task's
// channels) and the unpark — so a late write can never resurrect a checkpoint
// the unpark already deleted.
type parkHandle struct {
	in *interpreter
	cp *executionCheckpoint
	// persistedHeartbeat is the heartbeat deadline last written.
	persistedHeartbeat time.Time
}

// durable reports whether this frame parks durably: the top-level frame of an
// execution launched asynchronously (not a `.sync` child, a distributed Map
// child or an EXPRESS workflow), outside TestState.
func (in *interpreter) durable() bool {
	return in.run != nil && in.run.durable && in.topLevel && in.testState == nil
}

// park writes a checkpoint for the state f is running and returns its handle,
// or nil when this frame is not durable. cp carries the kind-specific fields;
// park fills in the rest.
func (in *interpreter) park(ctx context.Context, f *flow, cp *executionCheckpoint) *parkHandle {
	if !in.durable() {
		return nil
	}
	input, err := encodeJSON(f.raw)
	if err != nil {
		return nil
	}
	cp.ExecutionArn = in.exec.ExecutionArn
	cp.Region = in.region
	cp.StateMachine = checkpointStateMachine{
		ARN: in.sm.ARN, Name: in.sm.Name, Definition: in.sm.Definition, RoleArn: in.sm.RoleArn, Type: in.sm.Type,
	}
	cp.Depth = in.depth
	cp.QueryLanguage = in.run.queryLanguage
	cp.Point = redrivePoint{State: f.name, Input: input, Variables: in.vars.snapshot()}
	cp.EnteredTime = in.enteredAt
	cp.RetryAttempts = append([]int(nil), in.retryAttempts...)
	cp.RetryCount = in.retryCount
	p := &parkHandle{in: in, cp: cp}
	if cp.HeartbeatDeadline != nil {
		p.persistedHeartbeat = *cp.HeartbeatDeadline
	}
	in.run.setParked(true)
	p.writeWithHistory(ctx)
	return p
}

// resumePark returns the handle for a checkpoint rehydration resumed from.
// Nothing is written: the checkpoint on disk is already this one.
func (in *interpreter) resumePark(cp *executionCheckpoint) *parkHandle {
	p := &parkHandle{in: in, cp: cp}
	if cp.HeartbeatDeadline != nil {
		p.persistedHeartbeat = *cp.HeartbeatDeadline
	}
	in.run.setParked(true)
	return p
}

// unpark ends a park. interrupted means the wait was cut short by the
// execution's context. If that was a shutdown (and not a StopExecution), the
// execution is suspended: the checkpoint is brought up to date and kept, the
// run stays marked parked, and unwindReason turns the unwind into a
// suspension that writes no terminal state. Otherwise the checkpoint is
// deleted — the execution has moved on, or is about to end.
func (in *interpreter) unpark(ctx context.Context, p *parkHandle, interrupted bool) {
	if p == nil {
		return
	}
	if interrupted && in.handler.shuttingDown() {
		if stopped, _, _ := in.run.abortReason(); !stopped {
			p.write(ctx)
			return
		}
	}
	in.run.setParked(false)
	if err := in.handler.store.DeleteCheckpoint(context.WithoutCancel(ctx), p.cp.ExecutionArn); err != nil {
		in.logPersistError("delete the checkpoint of", err)
	}
}

func (p *parkHandle) write(ctx context.Context) {
	if err := p.in.handler.store.PutCheckpoint(context.WithoutCancel(ctx), p.cp); err != nil {
		p.in.logPersistError("checkpoint", err)
	}
}

// writeWithHistory persists the history so far and then the checkpoint that
// resumes after it. The history goes first, so a checkpoint on disk always has
// at least the events it names.
func (p *parkHandle) writeWithHistory(ctx context.Context) {
	events := p.in.hist.snapshot()
	p.cp.HistoryLength = len(events)
	if err := p.in.handler.store.PutHistory(context.WithoutCancel(ctx), p.cp.ExecutionArn, events); err != nil {
		p.in.logPersistError("write the history of", err)
		return
	}
	p.write(ctx)
}

func (p *parkHandle) heartbeatDeadline() *time.Time {
	if p == nil {
		return nil
	}
	return p.cp.HeartbeatDeadline
}

func (p *parkHandle) pickedUp() bool { return p != nil && p.cp.PickedUp }

// noteHeartbeat moves the heartbeat deadline. It is written only once it has
// moved by half HeartbeatSeconds, so a chatty worker cannot turn every
// SendTaskHeartbeat into a store write; the deadline a restart then resumes
// with is at worst half a window early.
func (p *parkHandle) noteHeartbeat(ctx context.Context, deadline time.Time) {
	if p == nil {
		return
	}
	p.cp.HeartbeatDeadline = &deadline
	if p.cp.HeartbeatSeconds == nil {
		return
	}
	half := time.Duration(*p.cp.HeartbeatSeconds) * time.Second / 2
	if deadline.Sub(p.persistedHeartbeat) >= half {
		p.persistedHeartbeat = deadline
		p.write(ctx)
	}
}

// notePickedUp records that a worker took the activity task, so a restart
// neither hands it to a second worker nor records ActivityStarted twice. The
// history is rewritten too: it now ends with that ActivityStarted.
func (p *parkHandle) notePickedUp(ctx context.Context, worker string, heartbeatDeadline *time.Time) {
	if p == nil {
		return
	}
	p.cp.PickedUp = true
	p.cp.WorkerName = worker
	p.cp.HeartbeatDeadline = heartbeatDeadline
	if heartbeatDeadline != nil {
		p.persistedHeartbeat = *heartbeatDeadline
	}
	p.writeWithHistory(ctx)
}

// takeResumed returns the checkpoint the state being dispatched resumes from,
// if it is of one of the given kinds, and consumes it.
func (in *interpreter) takeResumed(kinds ...string) *executionCheckpoint {
	cp := in.parkResumed
	if cp == nil {
		return nil
	}
	for _, kind := range kinds {
		if cp.Kind == kind {
			in.parkResumed = nil
			return cp
		}
	}
	return nil
}

// takeResumeFor returns the checkpoint a resumed run starts from when name is
// the state it parked in, consuming it. Only the first state of the resumed
// top-level frame can match.
func (in *interpreter) takeResumeFor(name string) *executionCheckpoint {
	cp := in.parkResume
	if cp == nil || !in.topLevel || cp.Point.State != name {
		return nil
	}
	in.parkResume = nil
	return cp
}

func (in *interpreter) logPersistError(what string, err error) {
	in.handler.logger().Error("stepfunctions: could not "+what+" a parked execution",
		zap.String("execution", in.exec.ExecutionArn), zap.Error(err))
}

// deadlineAfter is now plus seconds, nil when seconds is.
func deadlineAfter(now time.Time, seconds *int64) *time.Time {
	if seconds == nil {
		return nil
	}
	deadline := now.Add(time.Duration(*seconds) * time.Second)
	return &deadline
}

// shuttingDown reports whether Stop has cancelled the executions.
func (h *Handler) shuttingDown() bool {
	return h.shutdown != nil && h.shutdown.Err() != nil
}

// errSuspended is the unwind of a parked execution at shutdown: nothing is
// recorded and nothing terminal is written; the checkpoint resumes it.
func errSuspended() *stateError {
	return &stateError{name: errRuntime, cause: "the execution was suspended for an Overcast restart", unwound: true, suspended: true}
}

// errInterruptedByShutdown is the unwind of an execution a shutdown caught
// somewhere it cannot resume from.
func errInterruptedByShutdown(state string) *stateError {
	where := "while the execution was running"
	if state != "" {
		where = fmt.Sprintf("while the execution was in state %q", state)
	}
	return &stateError{name: errRuntime, unwound: true, cause: "Overcast shut down " + where +
		", which is not a point an execution can resume from after a restart — only a top-level Task waiting on a task token or an activity, or a Wait state, is. Redrive the execution to run the state again."}
}

// resumedBudget is the execution budget of a run resumed from a checkpoint.
// The definition's own TimeoutSeconds keeps counting from the (re)start of
// the execution on the injected clock, downtime included, as on AWS;
// Overcast's runaway guard, which is not an AWS limit, starts afresh.
func (h *Handler) resumedBudget(def *aslBranch, exec *Execution) time.Duration {
	budget := h.executionTimeout(nil)
	if def == nil || def.TimeoutSeconds <= 0 {
		return budget
	}
	started := exec.StartDate
	if exec.RedriveDate != nil {
		started = *exec.RedriveDate
	}
	remaining := time.Duration(def.TimeoutSeconds)*time.Second - h.clk.Since(started)
	if remaining < budget {
		budget = max(remaining, 0)
	}
	return budget
}

// ─── Rehydration ──────────────────────────────────────────────────────────────

// ensureRehydrated resumes every execution parked by a previous process. It
// runs once, on the first Step Functions request, never from New — reading
// the store at construction would stall every service's startup (see
// docs/dev/performance.md § Startup budget). A store failure leaves it to be
// retried by the next request.
func (h *Handler) ensureRehydrated() {
	_ = h.rehydrated.Do(func() error { return h.rehydrate(context.Background()) })
}

func (h *Handler) rehydrate(ctx context.Context) error {
	h.runsMu.Lock()
	stopping := h.stopping
	h.runsMu.Unlock()
	if stopping {
		return nil
	}
	checkpoints, malformed, err := h.store.ListCheckpoints(ctx)
	if err != nil {
		return err
	}
	for _, key := range malformed {
		// Isolated rather than fatal: its execution is failed like any other
		// that cannot resume, the first time it is read (reapIfOrphaned).
		h.logger().Warn("stepfunctions: skipping a checkpoint that cannot be decoded", zap.String("key", key))
	}
	for _, cp := range checkpoints {
		h.resumeFromCheckpoint(ctx, cp)
	}
	return nil
}

// resumeFromCheckpoint relaunches one parked execution. The token is
// registered, and an unclaimed activity task queued, before this returns, so
// a SendTaskSuccess or GetActivityTask arriving right behind the request that
// triggered rehydration finds it.
func (h *Handler) resumeFromCheckpoint(ctx context.Context, cp *executionCheckpoint) {
	rctx := middleware.ContextWithRegion(ctx, cp.Region)
	exec, err := h.store.GetExecution(rctx, cp.ExecutionArn)
	if err != nil {
		h.logger().Warn("stepfunctions: could not read a parked execution; it resumes on the next restart",
			zap.String("execution", cp.ExecutionArn), zap.Error(err))
		return
	}
	if exec == nil || exec.Status != statusRunning {
		_ = h.store.DeleteCheckpoint(rctx, cp.ExecutionArn)
		return
	}
	fail := func(reason string) {
		h.failInterrupted(rctx, exec, "Overcast restarted while this execution was waiting in state "+
			fmt.Sprintf("%q", cp.Point.State)+", but it could not be resumed: "+reason)
	}
	sm := &StateMachine{
		ARN: cp.StateMachine.ARN, Name: cp.StateMachine.Name, Definition: cp.StateMachine.Definition,
		RoleArn: cp.StateMachine.RoleArn, Type: cp.StateMachine.Type,
	}
	def, err := parseDefinition(sm.Definition)
	if err != nil {
		fail("its definition no longer parses")
		return
	}
	state := def.States[cp.Point.State]
	switch {
	case state == nil:
		fail("the state is not in its definition")
		return
	case cp.Kind == parkWait && state.Type != stateTypeWait,
		(cp.Kind == parkCallback || cp.Kind == parkActivity) && state.Type != stateTypeTask:
		fail("the checkpoint does not match the state's type")
		return
	}
	events, err := h.store.GetHistory(rctx, cp.ExecutionArn)
	if err != nil || len(events) < cp.HistoryLength || cp.HistoryLength == 0 {
		fail("its history was not saved with the checkpoint")
		return
	}
	events = events[:cp.HistoryLength]

	run := &executionRun{
		hist:          resumeHistoryRecorder(events, maxHistoryEvents),
		resume:        &redrivePoint{State: cp.Point.State, Input: cp.Point.Input, Variables: cp.Point.Variables},
		queryLanguage: cp.QueryLanguage,
		parkResume:    cp,
	}
	if cp.Kind == parkCallback || cp.Kind == parkActivity {
		run.resumeTask = h.tasks.restore(cp.Token, cp.ActivityArn, cp.ActivityInput)
		if cp.Kind == parkActivity && !cp.PickedUp {
			h.tasks.enqueue(run.resumeTask)
		}
	}
	if aerr := h.launchAsync(rctx, sm, exec, cp.Region, cp.Depth, run); aerr != nil && run.resumeTask != nil {
		h.tasks.release(run.resumeTask)
	}
}

// ─── Executions no restart can resume ─────────────────────────────────────────

// reapIfOrphaned fails a RUNNING execution that a previous process was
// running when it went away without a shutdown (a crash, a kill) and that had
// no checkpoint to resume from. Such a record would otherwise read RUNNING
// forever. It is recognised by the runner ID PutExecution stamps: a RUNNING
// record from another process with no live run here. Parked executions are
// never mistaken for one — rehydration has relaunched them before any request
// can get here.
func (h *Handler) reapIfOrphaned(ctx context.Context, exec *Execution) *Execution {
	if exec.Status != statusRunning || exec.RunnerID == h.store.runnerID || h.lookupRun(exec.ExecutionArn) != nil {
		return exec
	}
	if !h.rehydrated.Done() {
		return exec
	}
	return h.failInterrupted(ctx, exec, "Overcast restarted while this execution was running. Only an execution "+
		"waiting on a task token, an activity task or a Wait state survives a restart; this one was not, so it could not be resumed.")
}

// failInterrupted ends an execution that cannot resume: FAILED with
// States.Runtime and cause, its history — whatever was last saved, or just
// the ExecutionStarted event — closed with ExecutionFailed.
func (h *Handler) failInterrupted(ctx context.Context, exec *Execution, cause string) *Execution {
	h.reapMu.Lock()
	defer h.reapMu.Unlock()
	// Another request may have got here first.
	if current, err := h.store.GetExecution(ctx, exec.ExecutionArn); err == nil && current != nil {
		if current.Status != statusRunning {
			return current
		}
		exec = current
	}
	events, err := h.store.GetHistory(ctx, exec.ExecutionArn)
	if err != nil {
		events = nil
	}
	hist := resumeHistoryRecorder(events, maxHistoryEvents)
	if len(events) == 0 {
		roleArn := ""
		if sm, _ := h.store.GetStateMachine(ctx, extractSMName(exec.StateMachineArn)); sm != nil {
			roleArn = sm.RoleArn
		}
		hist.add(exec.StartDate, executionStartedEvent(&StateMachine{RoleArn: roleArn}, exec))
	}
	now := h.clk.Now()
	hist.add(now, HistoryEvent{Type: evtExecutionFailed, ExecutionFailed: &errorCauseDetails{Error: errRuntime, Cause: cause}})
	exec.Status = statusFailed
	exec.StopDate = &now
	exec.Error, exec.Cause = errRuntime, cause
	if err := h.store.PutHistory(ctx, exec.ExecutionArn, hist.snapshot()); err != nil {
		h.logger().Error("stepfunctions: could not persist the history of an execution a restart interrupted",
			zap.String("execution", exec.ExecutionArn), zap.Error(err))
		return exec
	}
	if err := h.store.PutExecution(ctx, exec); err != nil {
		h.logger().Error("stepfunctions: could not fail an execution a restart interrupted",
			zap.String("execution", exec.ExecutionArn), zap.Error(err))
		return exec
	}
	_ = h.store.DeleteCheckpoint(ctx, exec.ExecutionArn)
	return exec
}

func (h *Handler) logger() *zap.Logger {
	if h.log == nil {
		return zap.NewNop()
	}
	return h.log.Logger()
}

// isExpress reports whether a state machine is an EXPRESS workflow, whose
// executions AWS never makes durable.
func isExpress(sm *StateMachine) bool { return strings.EqualFold(sm.Type, "EXPRESS") }
