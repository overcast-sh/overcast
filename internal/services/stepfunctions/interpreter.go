package stepfunctions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// The Amazon States Language interpreter.
//
// Executions run in-process, matching the single-node deterministic-clock
// architecture the rest of Overcast uses. StartExecution answers as soon as
// the RUNNING record is persisted and interprets on a tracked goroutine, as
// AWS does; only StartSyncExecution and a `states:startExecution.sync` child
// wait for the terminal state (see executionMode in execution_ops.go). All
// eight ASL state types are interpreted, Parallel branches and Map iterations
// concurrently. Everything Overcast cannot interpret fails the execution
// loudly with an AWS-shaped error surfaced through DescribeExecution and
// GetExecutionHistory. There is no silent pass-through; see
// docs/plans/full-emulation-priority.md §2.1.

// Standard ASL error names.
const (
	errAll                    = "States.ALL"
	errTimeout                = "States.Timeout"
	errHeartbeatTimeout       = "States.HeartbeatTimeout"
	errTaskFailed             = "States.TaskFailed"
	errRuntime                = "States.Runtime"
	errNoChoiceMatched        = "States.NoChoiceMatched"
	errParameterPathFailure   = "States.ParameterPathFailure"
	errResultPathMatchFailure = "States.ResultPathMatchFailure"
	errExceedToleratedFailure = "States.ExceedToleratedFailureThreshold"
	errItemReaderFailed       = "States.ItemReaderFailed"
	errResultWriterFailed     = "States.ResultWriterFailed"
	errQueryEvaluationError   = "States.QueryEvaluationError"
)

// Execution status values, as DescribeExecution reports them.
const (
	statusRunning   = "RUNNING"
	statusSucceeded = "SUCCEEDED"
	statusFailed    = "FAILED"
	statusTimedOut  = "TIMED_OUT"
	statusAborted   = "ABORTED"
)

const (
	// maxHistoryEvents matches AWS's 25,000-event cap on a Standard
	// execution's history. Reaching it fails the execution loudly, which is
	// also what stops a runaway Choice loop.
	maxHistoryEvents = 25000

	// maxNestedExecutionDepth bounds `states:startExecution.sync` recursion so
	// a state machine that starts itself terminates with an honest error
	// instead of exhausting the stack.
	maxNestedExecutionDepth = 10

	// defaultExecutionTimeout is used when the config carries no budget (a
	// hand-built *config.Config rather than one from config.Load).
	defaultExecutionTimeout = 30 * time.Second
)

// stateError is an ASL failure: an error name plus a human-readable cause.
// It is what DescribeExecution's error/cause fields and the *Failed history
// events carry.
type stateError struct {
	name  string
	cause string
	// aborted marks an unwind that StopExecution asked for, so the execution
	// ends ABORTED rather than being reported as a failure or a timeout.
	aborted bool
	// budgetExpired marks the execution's own wall-clock budget running out,
	// which is the only States.Timeout that ends an execution TIMED_OUT. A
	// Task's TimeoutSeconds and a Fail state spelling States.Timeout are both
	// ordinary failures of a still-healthy execution, exactly as on AWS.
	budgetExpired bool
	// unwound marks an error produced because the context was cancelled — a
	// stop, the budget, or a failed sibling — rather than by the state itself.
	// Retry and Catch never act on it, and runState records the state's
	// `<Type>StateAborted` event for it.
	unwound bool
}

func (e *stateError) Error() string { return e.name + ": " + e.cause }

func newStateError(name, format string, args ...any) *stateError {
	return &stateError{name: name, cause: fmt.Sprintf(format, args...)}
}

// unsupportedError is the loud failure Overcast raises for valid ASL it does
// not interpret. States.Runtime is deliberate: AWS documents it as neither
// retriable nor catchable, so a `Catch` on States.ALL cannot swallow an
// Overcast gap and turn it back into a silent pass-through.
func unsupportedError(format string, args ...any) *stateError {
	return &stateError{name: errRuntime, cause: "Overcast does not support " + fmt.Sprintf(format, args...)}
}

// interpreter carries everything one execution needs. One is built per
// execution (and per nested `.sync` child execution); it owns no goroutines,
// timers or tickers of its own.
//
// It is also a frame: fork returns a shallow copy for one Parallel branch or
// Map iteration that shares the execution-wide fields but carries its own
// history cursor and Map item. Frames never share mutable state, which is
// what lets branches and iterations run concurrently.
type interpreter struct {
	handler *Handler
	region  string
	hist    *historyRecorder
	// run is the live registration for this execution. It is how an unwind
	// caused by StopExecution is told apart from the budget running out.
	run *executionRun
	// exec is the execution being interpreted, and sm the state machine it
	// belongs to.
	exec *Execution
	sm   *StateMachine
	// baseCtx is the part of the context object ($$) that does not change
	// during the execution. It is never mutated after buildBaseContext.
	baseCtx map[string]any
	// depth counts nested `states:startExecution.sync` levels so recursion
	// terminates with an honest error rather than a stack overflow.
	depth int

	// cursor is this frame's causal predecessor: the id the next recorded
	// event links to through previousEventId.
	cursor *int64
	// mapItem is $$.Map.Item inside a Map iteration, nil elsewhere.
	mapItem map[string]any
	// topLevel is true only for the frame running the definition's own
	// States, which is the only frame a redrive can resume in.
	topLevel bool
	// queryLanguage is what this frame's states default to.
	queryLanguage string
	// vars is this frame's variable scope.
	vars *varScope
	// testState is set when TestState is running a single state.
	testState *testStateRun
}

// fork returns a frame for a Parallel branch or Map iteration whose first
// event links to after.
func (in *interpreter) fork(after int64) *interpreter {
	child := *in
	cursor := after
	child.cursor = &cursor
	child.topLevel = false
	child.vars = newVarScope(in.vars)
	return &child
}

// record appends an event linked to this frame's previous event and advances
// the frame's cursor to it.
func (in *interpreter) record(event HistoryEvent) int64 {
	return in.recordAt(in.handler.clk.Now(), event)
}

func (in *interpreter) recordAt(now time.Time, event HistoryEvent) int64 {
	id := in.hist.addAfter(now, event, *in.cursor)
	*in.cursor = id
	return id
}

// rejoin moves this frame's cursor to the latest event in the history, which
// is where a Parallel or Map continues once all its children have finished.
func (in *interpreter) rejoin() { *in.cursor = in.hist.lastID() }

// executionOutcome is the terminal result of one execution.
type executionOutcome struct {
	status string
	output string
	err    *stateError
	events []HistoryEvent
}

// executionTimeout returns the wall-clock budget for one execution: the
// configured ceiling, lowered (never raised) by the definition's own
// top-level TimeoutSeconds.
func (h *Handler) executionTimeout(def *aslBranch) time.Duration {
	budget := h.cfg.StepFunctionsExecutionTimeout
	if budget <= 0 {
		budget = defaultExecutionTimeout
	}
	if def != nil && def.TimeoutSeconds > 0 {
		if declared := time.Duration(def.TimeoutSeconds) * time.Second; declared < budget {
			budget = declared
		}
	}
	return budget
}

// newInterpreter builds the top-level frame for one execution.
func (h *Handler) newInterpreter(sm *StateMachine, exec *Execution, region string, depth int, run *executionRun) *interpreter {
	cursor := run.hist.lastID()
	in := &interpreter{
		handler:  h,
		region:   region,
		hist:     run.hist,
		run:      run,
		exec:     exec,
		sm:       sm,
		depth:    depth,
		cursor:   &cursor,
		topLevel: true,
		vars:     newVarScope(nil),
	}
	in.baseCtx = in.buildBaseContext(sm, exec)
	return in
}

// runExecution interprets one state machine to completion and returns the
// terminal status, output and history. The caller persists them.
func (h *Handler) runExecution(ctx context.Context, sm *StateMachine, exec *Execution, def *aslBranch, region string, depth int, run *executionRun) executionOutcome {
	// A hard wall-clock bound on the run. It is a runaway guard rather than a
	// request timeout: StartExecution has already answered by the time this
	// runs, and only the synchronous callers still hold a request open. The
	// cancel is deferred so no timer outlives the execution.
	budget := h.executionTimeout(def)
	runCtx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()

	in := h.newInterpreter(sm, exec, region, depth, run)
	in.queryLanguage = def.QueryLanguage
	if in.queryLanguage == "" {
		in.queryLanguage = run.queryLanguage
	}
	if run.resumeState != "" {
		in.vars = restoreVarScope(run.resumeVariables)
	}

	start, rawInput := def.StartAt, exec.Input
	if run.resumeState != "" {
		start, rawInput = run.resumeState, run.resumeInput
	}
	input, err := decodeExecutionInput(rawInput)
	if err != nil {
		return in.finish(statusFailed, "", newStateError(errRuntime, "%s", err.Error()))
	}

	output, serr := in.runBranchFrom(runCtx, def, start, input)
	if serr != nil {
		return in.finish(statusForError(serr), "", serr)
	}
	encoded, encErr := encodeJSON(output)
	if encErr != nil {
		return in.finish(statusFailed, "", newStateError(errRuntime, "%s", encErr.Error()))
	}
	return in.finish(statusSucceeded, encoded, nil)
}

// executionStartedEvent builds the ExecutionStarted event every execution
// opens with. startExecution records it before the interpreter is handed the
// run, so an execution that has been acknowledged always has a history — even
// one that fails before the interpreter can run.
func executionStartedEvent(sm *StateMachine, exec *Execution) HistoryEvent {
	return HistoryEvent{
		Type: evtExecutionStarted,
		ExecutionStarted: &executionStartedDetails{
			Input:        exec.Input,
			InputDetails: &executionDataDetails{},
			RoleArn:      sm.RoleArn,

			StateMachineVersionArn: exec.StateMachineVersionArn,
			StateMachineAliasArn:   exec.StateMachineAliasArn,
		},
	}
}

// statusForError maps a terminal ASL error to an execution status. AWS
// reports a timed-out execution as TIMED_OUT and a stopped one as ABORTED,
// neither of which is a FAILED execution.
func statusForError(serr *stateError) string {
	switch {
	case serr.aborted:
		return statusAborted
	case serr.budgetExpired:
		return statusTimedOut
	default:
		return statusFailed
	}
}

// finish records the terminal history event and returns the outcome.
func (in *interpreter) finish(status, output string, serr *stateError) executionOutcome {
	switch status {
	case statusSucceeded:
		in.record(HistoryEvent{
			Type: evtExecutionSucceeded,
			ExecutionSucceeded: &executionSucceededDetails{
				Output:        output,
				OutputDetails: &executionDataDetails{},
			},
		})
	case statusTimedOut:
		in.record(HistoryEvent{
			Type:              evtExecutionTimedOut,
			ExecutionTimedOut: &errorCauseDetails{Error: serr.name, Cause: serr.cause},
		})
	case statusAborted:
		in.record(HistoryEvent{
			Type:             evtExecutionAborted,
			ExecutionAborted: &errorCauseDetails{Error: serr.name, Cause: serr.cause},
		})
	default:
		details := &errorCauseDetails{}
		if serr != nil {
			details.Error = serr.name
			details.Cause = serr.cause
		}
		in.record(HistoryEvent{Type: evtExecutionFailed, ExecutionFailed: details})
	}
	return executionOutcome{status: status, output: output, err: serr, events: in.hist.snapshot()}
}

// buildBaseContext assembles the parts of the ASL context object ($$) that do
// not change during the execution.
func (in *interpreter) buildBaseContext(sm *StateMachine, exec *Execution) map[string]any {
	var parsedInput any
	if exec.Input != "" {
		_ = json.Unmarshal([]byte(exec.Input), &parsedInput)
	}
	execCtx := map[string]any{
		"Id":        exec.ExecutionArn,
		"Name":      exec.Name,
		"Input":     parsedInput,
		"RoleArn":   sm.RoleArn,
		"StartTime": exec.StartDate.UTC().Format(time.RFC3339Nano),
	}
	if exec.RedriveCount > 0 {
		execCtx["RedriveCount"] = float64(exec.RedriveCount)
		if exec.RedriveDate != nil {
			execCtx["RedriveTime"] = exec.RedriveDate.UTC().Format(time.RFC3339Nano)
		}
	}
	return map[string]any{
		"Execution": execCtx,
		"StateMachine": map[string]any{
			"Id":   sm.ARN,
			"Name": sm.Name,
		},
	}
}

// stateContext returns the context object for one state evaluation.
func (in *interpreter) stateContext(name string, entered time.Time, retryCount int) map[string]any {
	ctxObj := make(map[string]any, len(in.baseCtx)+3)
	for key, value := range in.baseCtx {
		ctxObj[key] = value
	}
	ctxObj["State"] = map[string]any{
		"Name":        name,
		"EnteredTime": entered.UTC().Format(time.RFC3339Nano),
		"RetryCount":  float64(retryCount),
	}
	if in.mapItem != nil {
		ctxObj["Map"] = map[string]any{"Item": in.mapItem}
	}
	if vars := in.vars.all(); len(vars) > 0 {
		ctxObj[variablesContextKey] = vars
	}
	return ctxObj
}

// ─── Branch execution ─────────────────────────────────────────────────────────

// runBranch interprets one state machine body from its StartAt state until a
// state ends the branch. It is used for the top-level definition, for every
// Parallel branch and for every Map iteration.
func (in *interpreter) runBranch(ctx context.Context, branch *aslBranch, input any) (any, *stateError) {
	return in.runBranchFrom(ctx, branch, branch.StartAt, input)
}

// runBranchFrom is runBranch starting at a named state — which is how a
// redriven execution resumes at the state that failed.
func (in *interpreter) runBranchFrom(ctx context.Context, branch *aslBranch, start string, input any) (any, *stateError) {
	current := start
	data := input
	for {
		if ctx.Err() != nil {
			return nil, in.unwindReason(ctx, "")
		}
		if in.hist.full() {
			return nil, newStateError(errRuntime, "the execution exceeded the maximum of %d history events", maxHistoryEvents)
		}
		state := branch.States[current]
		if state == nil {
			return nil, newStateError(errRuntime, "state %q is not defined", current)
		}
		output, next, done, serr := in.runState(ctx, current, state, data)
		if serr != nil {
			return nil, serr
		}
		data = output
		if done {
			return data, nil
		}
		current = next
	}
}

// errSiblingFailed is the cancellation cause a Parallel or Map puts on its
// children's context when one of them fails, so the others unwind.
var errSiblingFailed = errors.New("a sibling Parallel branch or Map iteration failed")

// unwindReason explains why the interpreter is unwinding after its context was
// cancelled: StopExecution asked for it, a sibling Parallel branch or Map
// iteration failed, or the runaway guard fired. state names the state it was
// in, when known. The result is always marked unwound.
func (in *interpreter) unwindReason(ctx context.Context, state string) *stateError {
	if in.run != nil {
		if stopped, errName, cause := in.run.abortReason(); stopped {
			return &stateError{name: errName, cause: cause, aborted: true, unwound: true}
		}
	}
	if ctx != nil && errors.Is(context.Cause(ctx), errSiblingFailed) {
		return &stateError{name: errRuntime, cause: errSiblingFailed.Error(), unwound: true}
	}
	where := ""
	if state != "" {
		where = fmt.Sprintf(" in state %q", state)
	}
	budget := newStateError(errTimeout,
		"the execution exceeded Overcast's execution budget%s — raise OVERCAST_STEPFUNCTIONS_EXECUTION_TIMEOUT (currently %s) if the workflow legitimately takes this long",
		where, in.handler.executionTimeout(nil))
	budget.budgetExpired = true
	budget.unwound = true
	return budget
}

// runState interprets one state and reports the next transition. done means
// the branch ends here (End: true, or a Succeed state).
func (in *interpreter) runState(ctx context.Context, name string, state *aslState, raw any) (any, string, bool, *stateError) {
	entered := in.handler.clk.Now()
	f, serr := in.newFlow(name, state, raw, in.stateContext(name, entered, 0))
	if serr != nil {
		in.noteFailure(name, raw)
		return nil, "", false, serr
	}

	enteredJSON, encErr := encodeJSON(f.effective)
	if encErr != nil {
		return nil, "", false, newStateError(errRuntime, "%s", encErr.Error())
	}
	in.recordAt(entered, HistoryEvent{
		Type: stateEnteredEventType(state.Type),
		StateEntered: &stateEnteredDetails{
			Name:         name,
			Input:        enteredJSON,
			InputDetails: &executionDataDetails{},
		},
	})

	output, next, done, serr := in.dispatchState(ctx, f)
	if serr != nil && serr.unwound {
		if aborted := stateAbortedEventType(state.Type); aborted != "" {
			in.record(HistoryEvent{Type: aborted})
		}
	}
	if serr != nil {
		in.noteFailure(name, raw)
	}
	return output, next, done, serr
}

// dispatchState runs the type-specific part of a state once it is entered.
func (in *interpreter) dispatchState(ctx context.Context, f *flow) (any, string, bool, *stateError) {
	switch f.state.Type {
	case stateTypeFail:
		return nil, "", false, in.runFail(f)
	case stateTypeSucceed:
		return in.runSucceed(f)
	case stateTypeChoice:
		return in.runChoice(f)
	case stateTypeWait:
		return in.runWait(ctx, f)
	case stateTypePass:
		return in.runPass(f)
	case stateTypeTask, stateTypeParallel, stateTypeMap:
		return in.runRetryable(ctx, f)
	}
	// parseDefinition rejects unknown state types, so this is unreachable in
	// practice; keeping it loud rather than falling through is the point.
	return nil, "", false, unsupportedError("state type %q", f.state.Type)
}

// noteFailure remembers the top-level state the run ended in without
// succeeding — failed, timed out or stopped — and the raw input it was
// entered with, so RedriveExecution can resume there. Only the top-level frame
// records it: a failing Parallel branch or Map iteration is redriven by
// re-running the Parallel or Map state that contains it.
func (in *interpreter) noteFailure(name string, raw any) {
	if in.run == nil || !in.topLevel {
		return
	}
	encoded, err := encodeJSON(raw)
	if err != nil {
		return
	}
	in.run.noteFailure(name, encoded, in.vars.snapshot())
}

// recordExit emits the `<Type>StateExited` event for a state.
func (in *interpreter) recordExit(name, stateType string, output any, assigned map[string]any) {
	encoded, err := encodeJSON(output)
	if err != nil {
		encoded = ""
	}
	details := &stateExitedDetails{
		Name:          name,
		Output:        encoded,
		OutputDetails: &executionDataDetails{},
	}
	if vars := assignedVariables(assigned); vars != nil {
		details.AssignedVariables = vars
		details.AssignedVariablesDetails = &executionDataDetails{}
	}
	in.record(HistoryEvent{Type: stateExitedEventType(stateType), StateExited: details})
}

// ─── Input / output processing ────────────────────────────────────────────────

// applyInputPath narrows the raw state input. An absent InputPath means `$`;
// an explicit null means the effective input is an empty object.
func applyInputPath(state *aslState, raw any, ctxObj map[string]any) (any, *stateError) {
	if !state.InputPath.Set || state.InputPath.Value == "$" {
		return raw, nil
	}
	if state.InputPath.Null {
		return map[string]any{}, nil
	}
	value, err := selectPath(raw, ctxObj, state.InputPath.Value)
	if err != nil {
		return nil, newStateError(errRuntime, "%s", err.Error())
	}
	return value, nil
}

// applyOutputPath narrows a state's output. An absent OutputPath means `$`; an
// explicit null means an empty object.
func applyOutputPath(state *aslState, value any, ctxObj map[string]any) (any, *stateError) {
	if !state.OutputPath.Set || state.OutputPath.Value == "$" {
		return value, nil
	}
	if state.OutputPath.Null {
		return map[string]any{}, nil
	}
	selected, err := selectPath(value, ctxObj, state.OutputPath.Value)
	if err != nil {
		return nil, newStateError(errRuntime, "%s", err.Error())
	}
	return selected, nil
}

// templateStateError turns a payload-template failure into its ASL error.
func templateStateError(err error) *stateError {
	return newStateError(templateErrorName(err), "%s", err.Error())
}

// applyResultPath places a result into the raw state input. An absent
// ResultPath means `$` (the result replaces the input); an explicit null
// discards the result and passes the input through unchanged.
func applyResultPath(resultPath aslPath, raw, result any) (any, *stateError) {
	switch {
	case !resultPath.Set, resultPath.Value == "$":
		return result, nil
	case resultPath.Null:
		return raw, nil
	}
	combined, err := insertPath(raw, resultPath.Value, result)
	if err != nil {
		return nil, newStateError(errResultPathMatchFailure, "%s", err.Error())
	}
	return combined, nil
}

// ─── JSON helpers ─────────────────────────────────────────────────────────────

// decodeExecutionInput parses an execution's input document. An empty input is
// the empty object, matching AWS's default.
func decodeExecutionInput(input string) (any, error) {
	if strings.TrimSpace(input) == "" {
		return map[string]any{}, nil
	}
	var decoded any
	if err := json.Unmarshal([]byte(input), &decoded); err != nil {
		return nil, fmt.Errorf("the execution input is not valid JSON: %w", err)
	}
	return decoded, nil
}

func encodeJSON(v any) (string, error) {
	encoded, err := json.Marshal(v)
	if err != nil {
		return "", fmt.Errorf("state output could not be encoded as JSON: %w", err)
	}
	return string(encoded), nil
}
