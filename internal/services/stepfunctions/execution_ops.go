package stepfunctions

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/overcast-sh/overcast/internal/events"
	"github.com/overcast-sh/overcast/internal/middleware"
	"github.com/overcast-sh/overcast/internal/protocol"
)

// Execution-plane operations: starting an execution actually interprets the
// state machine, and DescribeExecution / GetExecutionHistory / ListExecutions
// / StopExecution / DescribeStateMachineForExecution report what really ran.

// ─── Shared start path ────────────────────────────────────────────────────────

// executionMode selects whether the caller waits for the execution.
type executionMode int

const (
	// executionAsync accepts the execution and returns while it is still
	// RUNNING, as AWS's StartExecution does. The interpreter runs on a tracked
	// goroutine, so the caller's request — and any service dispatching to
	// Step Functions, such as an EventBridge target — is never held open for
	// the length of the workflow.
	executionAsync executionMode = iota
	// executionSync runs the execution to completion before returning. This is
	// StartSyncExecution's express-workflow semantic, and what the
	// `states:startExecution.sync` integration needs in order to block on its
	// child.
	executionSync
)

// startExecution records an execution and runs the state machine.
//
// In executionAsync mode it returns as soon as the RUNNING record is
// persisted; the interpreter continues on a goroutine tracked by h.wg, so
// DescribeExecution and GetExecutionHistory observe it progressing and
// StopExecution can interrupt it. In executionSync mode it returns the
// terminal record.
//
// Either way the run is bounded by OVERCAST_STEPFUNCTIONS_EXECUTION_TIMEOUT,
// which is a runaway guard rather than a request timeout: it no longer sits on
// the wire, so it is generous enough for ordinary Wait states.
//
// depth is the nesting level for `states:startExecution` children.
func (h *Handler) startExecution(ctx context.Context, smARN, execName, input string, depth int, mode executionMode) (*Execution, *protocol.AWSError) {
	region := middleware.RegionFromContext(ctx, h.cfg.Region)
	// smARN may be a version or alias ARN; sm then carries the version's
	// definition and role, under the base state machine's ARN and name.
	sm, target, aerr := h.resolveExecutionTarget(ctx, smARN)
	if aerr != nil {
		return nil, aerr
	}
	smName := sm.Name
	if input != "" && !json.Valid([]byte(input)) {
		return nil, &protocol.AWSError{
			Code:       "InvalidExecutionInput",
			Message:    "Invalid State Machine Execution Input: the input is not valid JSON",
			HTTPStatus: http.StatusBadRequest,
		}
	}
	if execName == "" {
		execName = uuid.NewString()
	}
	execARN := protocol.ARN(region, h.cfg.AccountID, "states", "execution:"+smName+":"+execName)
	existing, err := h.store.GetExecution(ctx, execARN)
	if err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	if existing != nil {
		return nil, &protocol.AWSError{
			Code:       "ExecutionAlreadyExists",
			Message:    fmt.Sprintf("Execution Already Exists: '%s'", execARN),
			HTTPStatus: http.StatusBadRequest,
		}
	}

	started := h.clk.Now()
	exec := &Execution{
		ExecutionArn:           execARN,
		StateMachineArn:        sm.ARN,
		Name:                   execName,
		Input:                  input,
		Status:                 statusRunning,
		StartDate:              started,
		StateMachineVersionArn: target.versionArn,
		StateMachineAliasArn:   target.aliasArn,
	}
	// Persist the RUNNING record before interpreting so the execution is
	// visible from the moment StartExecution answers.
	if err := h.store.PutExecution(ctx, exec); err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	h.publishCtx(ctx, events.SFNExecutionStarted, events.ResourcePayload{Name: execName})

	// Every run is registered while it is alive, in both modes, so
	// StopExecution and the live-history read work uniformly. ExecutionStarted
	// is recorded here rather than on the goroutine so GetExecutionHistory
	// never sees an empty history for an execution StartExecution has already
	// acknowledged.
	run := &executionRun{hist: newHistoryRecorder(maxHistoryEvents)}
	run.hist.add(started, executionStartedEvent(sm, exec))

	if mode == executionSync {
		runCtx, cancel := context.WithCancel(ctx)
		defer cancel()
		run.cancel = cancel
		// reserveRun (runs.go) is the fenced choke point every launcher in
		// this package funnels through — StartExecution, StartSyncExecution
		// and a nested states:startExecution all call startExecution, and
		// startExecution never registers a run any other way. Once Stop has
		// begun, this refuses rather than run a state machine shutdown will
		// never wait for (issue #1315).
		if !h.reserveRun(execARN, run, false) {
			return nil, h.refuseStartAfterShutdown(ctx, exec, run)
		}
		defer h.releaseRun(execARN)
		if err := h.completeExecution(runCtx, sm, exec, region, depth, run); err != nil {
			return nil, protocol.Wrap(protocol.ErrInternalError, err)
		}
		return exec, nil
	}

	if aerr := h.launchAsync(ctx, sm, exec, region, depth, run); aerr != nil {
		return nil, aerr
	}
	return exec, nil
}

// launchAsync runs an execution on a tracked goroutine and returns at once.
// StartExecution and RedriveExecution both come through here.
func (h *Handler) launchAsync(ctx context.Context, sm *StateMachine, exec *Execution, region string, depth int, run *executionRun) *protocol.AWSError {
	log := h.log.WithRecorder(ctx)
	execARN := exec.ExecutionArn
	// The run outlives this request, so it hangs off the service's own
	// context rather than the caller's, and carries the region the request
	// resolved (the store is region-scoped and the background context has no
	// request to read it from).
	runCtx, cancel := context.WithCancel(middleware.ContextWithRegion(h.shutdown, region))
	run.cancel = cancel

	// The goroutine mutates its own copy: the caller still holds exec for the
	// response body.
	running := *exec
	// reserveRun registers the run and Adds to wg atomically with the
	// stopping check, under the same lock Stop uses to set it — so this Add
	// can never race Stop's wg.Wait (issue #1315; the #1290/#1298 pattern).
	// Refused, no goroutine is launched and wg is never touched.
	if !h.reserveRun(execARN, run, true) {
		cancel()
		return h.refuseStartAfterShutdown(ctx, exec, run)
	}
	go func() {
		defer h.wg.Done()
		defer cancel()
		defer h.releaseRun(execARN)
		defer h.recoverExecution(runCtx, &running)
		if err := h.completeExecution(runCtx, sm, &running, region, depth, run); err != nil {
			log.Logger().Error("stepfunctions: could not persist execution result",
				zap.String("execution", execARN), zap.Error(err))
		}
	}()
	return nil
}

// completeExecution runs the interpreter and persists the terminal state and
// history. It is the body of both the async goroutine and the synchronous
// path, so the two can never drift.
func (h *Handler) completeExecution(ctx context.Context, sm *StateMachine, exec *Execution, region string, depth int, run *executionRun) error {
	return h.persistOutcome(ctx, exec, run, h.interpret(ctx, sm, exec, region, depth, run))
}

// persistOutcome applies a run's terminal outcome to its execution record and
// writes the history and the record.
func (h *Handler) persistOutcome(ctx context.Context, exec *Execution, run *executionRun, outcome executionOutcome) error {
	exec.Status = outcome.status
	exec.Output = outcome.output
	// An aborted execution reports the stop time StopExecution already handed
	// back to its caller, rather than whenever the unwind happened to land.
	stopped, aborted := run.stopTime()
	if !aborted {
		stopped = h.clk.Now()
	}
	exec.StopDate = &stopped
	exec.Error, exec.Cause = "", ""
	if outcome.err != nil {
		exec.Error = outcome.err.name
		exec.Cause = outcome.err.cause
	}
	// Where a redrive would resume. Cleared on success, so only an
	// unsuccessful run is redrivable.
	exec.RedriveState, exec.RedriveInput, exec.RedriveVariables = "", "", ""
	if outcome.status != statusSucceeded {
		exec.RedriveState, exec.RedriveInput, exec.RedriveVariables = run.failurePoint()
	}
	if err := h.store.PutHistory(ctx, exec.ExecutionArn, outcome.events); err != nil {
		return err
	}
	// The execution record lands after the history, so an execution that reads
	// as terminal always has its history alongside it.
	return h.store.PutExecution(ctx, exec)
}

// recoverExecution keeps a panic inside the interpreter from taking the
// process down — the HTTP recovery middleware does not cover a goroutine — and
// leaves the execution FAILED rather than RUNNING forever.
func (h *Handler) recoverExecution(ctx context.Context, exec *Execution) {
	log := h.log.WithRecorder(ctx)
	r := recover()
	if r == nil {
		return
	}
	log.Logger().Error("stepfunctions: execution panicked",
		zap.String("execution", exec.ExecutionArn), zap.Any("panic", r))
	stopped := h.clk.Now()
	exec.Status = statusFailed
	exec.StopDate = &stopped
	exec.Error = errRuntime
	exec.Cause = fmt.Sprintf("the execution panicked inside Overcast's interpreter: %v", r)
	if err := h.store.PutExecution(ctx, exec); err != nil {
		log.Logger().Error("stepfunctions: could not persist panicked execution",
			zap.String("execution", exec.ExecutionArn), zap.Error(err))
	}
}

// interpret parses the stored definition and runs it. A definition that no
// longer parses fails the execution loudly rather than reporting SUCCEEDED —
// CreateStateMachine rejects invalid ASL, so this only happens for a state
// machine stored before that validation existed.
func (h *Handler) interpret(ctx context.Context, sm *StateMachine, exec *Execution, region string, depth int, run *executionRun) executionOutcome {
	def, err := parseDefinition(sm.Definition)
	if err != nil {
		serr := newStateError(errRuntime, "the state machine definition is not valid ASL: %s", err.Error())
		run.hist.add(h.clk.Now(), HistoryEvent{
			Type:            evtExecutionFailed,
			ExecutionFailed: &errorCauseDetails{Error: serr.name, Cause: serr.cause},
		})
		return executionOutcome{status: statusFailed, err: serr, events: run.hist.snapshot()}
	}
	return h.runExecution(ctx, sm, exec, def, region, depth, run)
}

// ─── StartExecution ───────────────────────────────────────────────────────────

func (h *Handler) startExecutionTyped(ctx context.Context, req *startExecutionRequest) (*startExecutionResponse, *protocol.AWSError) {
	exec, aerr := h.startExecution(ctx, req.StateMachineArn, req.Name, req.Input, 0, executionAsync)
	if aerr != nil {
		return nil, aerr
	}
	return &startExecutionResponse{
		ExecutionArn: exec.ExecutionArn,
		StartDate:    epochSeconds(exec.StartDate),
	}, nil
}

// ─── StartSyncExecution ───────────────────────────────────────────────────────

type startSyncExecutionRequest struct {
	StateMachineArn string `json:"stateMachineArn" cbor:"stateMachineArn"`
	Input           string `json:"input" cbor:"input"`
	Name            string `json:"name" cbor:"name"`
	TraceHeader     string `json:"traceHeader" cbor:"traceHeader"`
}

type startSyncExecutionResponse struct {
	ExecutionArn    string                `json:"executionArn" cbor:"executionArn"`
	StateMachineArn string                `json:"stateMachineArn" cbor:"stateMachineArn"`
	Name            string                `json:"name" cbor:"name"`
	StartDate       float64               `json:"startDate" cbor:"startDate"`
	StopDate        float64               `json:"stopDate" cbor:"stopDate"`
	Status          string                `json:"status" cbor:"status"`
	Input           string                `json:"input,omitempty" cbor:"input,omitempty"`
	InputDetails    *executionDataDetails `json:"inputDetails,omitempty" cbor:"inputDetails,omitempty"`
	Output          string                `json:"output,omitempty" cbor:"output,omitempty"`
	OutputDetails   *executionDataDetails `json:"outputDetails,omitempty" cbor:"outputDetails,omitempty"`
	Error           string                `json:"error,omitempty" cbor:"error,omitempty"`
	Cause           string                `json:"cause,omitempty" cbor:"cause,omitempty"`
}

func (h *Handler) startSyncExecutionTyped(ctx context.Context, req *startSyncExecutionRequest) (*startSyncExecutionResponse, *protocol.AWSError) {
	sm, _, aerr := h.resolveExecutionTarget(ctx, req.StateMachineArn)
	if aerr != nil {
		return nil, aerr
	}
	// AWS serves StartSyncExecution only for EXPRESS state machines.
	if !strings.EqualFold(sm.Type, "EXPRESS") {
		return nil, &protocol.AWSError{
			Code:       "StateMachineTypeNotSupported",
			Message:    "StartSyncExecution is not supported for STANDARD workflows",
			HTTPStatus: http.StatusBadRequest,
		}
	}
	exec, aerr := h.startExecution(ctx, req.StateMachineArn, req.Name, req.Input, 0, executionSync)
	if aerr != nil {
		return nil, aerr
	}
	resp := &startSyncExecutionResponse{
		ExecutionArn:    exec.ExecutionArn,
		StateMachineArn: exec.StateMachineArn,
		Name:            exec.Name,
		StartDate:       epochSeconds(exec.StartDate),
		Status:          exec.Status,
		Input:           exec.Input,
		InputDetails:    &executionDataDetails{},
		Output:          exec.Output,
		Error:           exec.Error,
		Cause:           exec.Cause,
	}
	if exec.StopDate != nil {
		resp.StopDate = epochSeconds(*exec.StopDate)
	}
	if exec.Output != "" {
		resp.OutputDetails = &executionDataDetails{}
	}
	return resp, nil
}

// ─── DescribeExecution ────────────────────────────────────────────────────────

type describeExecutionRequest struct {
	ExecutionArn string `json:"executionArn" cbor:"executionArn"`
}

type describeExecutionResponse struct {
	ExecutionArn    string                `json:"executionArn" cbor:"executionArn"`
	StateMachineArn string                `json:"stateMachineArn" cbor:"stateMachineArn"`
	Name            string                `json:"name" cbor:"name"`
	Status          string                `json:"status" cbor:"status"`
	StartDate       float64               `json:"startDate" cbor:"startDate"`
	StopDate        float64               `json:"stopDate,omitempty" cbor:"stopDate,omitempty"`
	Input           string                `json:"input,omitempty" cbor:"input,omitempty"`
	InputDetails    *executionDataDetails `json:"inputDetails,omitempty" cbor:"inputDetails,omitempty"`
	Output          string                `json:"output,omitempty" cbor:"output,omitempty"`
	OutputDetails   *executionDataDetails `json:"outputDetails,omitempty" cbor:"outputDetails,omitempty"`
	Error           string                `json:"error,omitempty" cbor:"error,omitempty"`
	Cause           string                `json:"cause,omitempty" cbor:"cause,omitempty"`

	MapRunArn           string  `json:"mapRunArn,omitempty" cbor:"mapRunArn,omitempty"`
	RedriveCount        int     `json:"redriveCount" cbor:"redriveCount"`
	RedriveDate         float64 `json:"redriveDate,omitempty" cbor:"redriveDate,omitempty"`
	RedriveStatus       string  `json:"redriveStatus,omitempty" cbor:"redriveStatus,omitempty"`
	RedriveStatusReason string  `json:"redriveStatusReason,omitempty" cbor:"redriveStatusReason,omitempty"`
	// Set when the execution was started through a version or alias ARN.
	StateMachineVersionArn string `json:"stateMachineVersionArn,omitempty" cbor:"stateMachineVersionArn,omitempty"`
	StateMachineAliasArn   string `json:"stateMachineAliasArn,omitempty" cbor:"stateMachineAliasArn,omitempty"`
}

func (h *Handler) describeExecutionTyped(ctx context.Context, req *describeExecutionRequest) (*describeExecutionResponse, *protocol.AWSError) {
	exec, aerr := h.getExecution(ctx, req.ExecutionArn)
	if aerr != nil {
		return nil, aerr
	}
	resp := &describeExecutionResponse{
		ExecutionArn:    exec.ExecutionArn,
		StateMachineArn: exec.StateMachineArn,
		Name:            exec.Name,
		Status:          exec.Status,
		StartDate:       epochSeconds(exec.StartDate),
		Input:           exec.Input,
		InputDetails:    &executionDataDetails{},
		Output:          exec.Output,
		Error:           exec.Error,
		Cause:           exec.Cause,

		StateMachineVersionArn: exec.StateMachineVersionArn,
		StateMachineAliasArn:   exec.StateMachineAliasArn,
	}
	if exec.StopDate != nil {
		resp.StopDate = epochSeconds(*exec.StopDate)
	}
	if exec.Output != "" {
		resp.OutputDetails = &executionDataDetails{}
	}
	resp.MapRunArn = exec.MapRunArn
	resp.RedriveCount = exec.RedriveCount
	if exec.RedriveDate != nil {
		resp.RedriveDate = epochSeconds(*exec.RedriveDate)
	}
	sm, err := h.store.GetStateMachine(ctx, extractSMName(exec.StateMachineArn))
	if err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	resp.RedriveStatus, resp.RedriveStatusReason = h.redriveStatus(exec, sm)
	return resp, nil
}

// ─── GetExecutionHistory ──────────────────────────────────────────────────────

type getExecutionHistoryRequest struct {
	ExecutionArn         string `json:"executionArn" cbor:"executionArn"`
	MaxResults           int    `json:"maxResults" cbor:"maxResults"`
	ReverseOrder         bool   `json:"reverseOrder" cbor:"reverseOrder"`
	NextToken            string `json:"nextToken" cbor:"nextToken"`
	IncludeExecutionData *bool  `json:"includeExecutionData" cbor:"includeExecutionData"`
}

type getExecutionHistoryResponse struct {
	Events    []HistoryEvent `json:"events" cbor:"events"`
	NextToken string         `json:"nextToken,omitempty" cbor:"nextToken,omitempty"`
}

func (h *Handler) getExecutionHistoryTyped(ctx context.Context, req *getExecutionHistoryRequest) (*getExecutionHistoryResponse, *protocol.AWSError) {
	if _, aerr := h.getExecution(ctx, req.ExecutionArn); aerr != nil {
		return nil, aerr
	}
	// A running execution's history lives in its recorder, not the store —
	// events are persisted once, at the end. Reading the live recorder is what
	// lets a caller watch an execution progress without costing a store write
	// per state transition.
	var events []HistoryEvent
	if run := h.lookupRun(req.ExecutionArn); run != nil {
		events = run.hist.snapshot()
	} else {
		stored, err := h.store.GetHistory(ctx, req.ExecutionArn)
		if err != nil {
			return nil, protocol.Wrap(protocol.ErrInternalError, err)
		}
		events = make([]HistoryEvent, len(stored))
		copy(events, stored)
	}
	if req.IncludeExecutionData != nil && !*req.IncludeExecutionData {
		for i := range events {
			stripExecutionData(&events[i])
		}
	}
	if req.ReverseOrder {
		for i, j := 0, len(events)-1; i < j; i, j = i+1, j-1 {
			events[i], events[j] = events[j], events[i]
		}
	}
	page, pageErr := paginate(events, req.MaxResults, req.NextToken)
	if pageErr != nil {
		return nil, pageErr
	}
	return &getExecutionHistoryResponse{Events: page.Items, NextToken: page.NextToken}, nil
}

// stripExecutionData blanks the payload fields GetExecutionHistory omits when
// includeExecutionData is false.
func stripExecutionData(event *HistoryEvent) {
	// Every details block is copied before it is blanked: the events share
	// their details with the live recorder (and the store's decoded copy), so
	// blanking in place would erase the payloads from the history itself.
	if d := event.ExecutionStarted; d != nil {
		c := *d
		c.Input = ""
		event.ExecutionStarted = &c
	}
	if d := event.ExecutionSucceeded; d != nil {
		c := *d
		c.Output = ""
		event.ExecutionSucceeded = &c
	}
	if d := event.StateEntered; d != nil {
		c := *d
		c.Input = ""
		event.StateEntered = &c
	}
	if d := event.StateExited; d != nil {
		c := *d
		c.Output = ""
		c.AssignedVariables = nil
		event.StateExited = &c
	}
	if d := event.TaskScheduled; d != nil {
		c := *d
		c.Parameters = ""
		event.TaskScheduled = &c
	}
	if d := event.TaskSucceeded; d != nil {
		c := *d
		c.Output = ""
		event.TaskSucceeded = &c
	}
	if d := event.TaskSubmitted; d != nil {
		c := *d
		c.Output = ""
		event.TaskSubmitted = &c
	}
	if d := event.LambdaFunctionScheduled; d != nil {
		c := *d
		c.Input = ""
		event.LambdaFunctionScheduled = &c
	}
	if d := event.LambdaFunctionSucceeded; d != nil {
		c := *d
		c.Output = ""
		event.LambdaFunctionSucceeded = &c
	}
	if d := event.ActivityScheduled; d != nil {
		c := *d
		c.Input = ""
		event.ActivityScheduled = &c
	}
	if d := event.ActivitySucceeded; d != nil {
		c := *d
		c.Output = ""
		event.ActivitySucceeded = &c
	}
}

// ─── ListExecutions ───────────────────────────────────────────────────────────

type listExecutionsRequest struct {
	StateMachineArn string `json:"stateMachineArn" cbor:"stateMachineArn"`
	MapRunArn       string `json:"mapRunArn" cbor:"mapRunArn"`
	StatusFilter    string `json:"statusFilter" cbor:"statusFilter"`
	RedriveFilter   string `json:"redriveFilter" cbor:"redriveFilter"`
	MaxResults      int    `json:"maxResults" cbor:"maxResults"`
	NextToken       string `json:"nextToken" cbor:"nextToken"`
}

type executionListItem struct {
	ExecutionArn    string  `json:"executionArn" cbor:"executionArn"`
	StateMachineArn string  `json:"stateMachineArn" cbor:"stateMachineArn"`
	Name            string  `json:"name" cbor:"name"`
	Status          string  `json:"status" cbor:"status"`
	StartDate       float64 `json:"startDate" cbor:"startDate"`
	StopDate        float64 `json:"stopDate,omitempty" cbor:"stopDate,omitempty"`
	MapRunArn       string  `json:"mapRunArn,omitempty" cbor:"mapRunArn,omitempty"`
	RedriveCount    int     `json:"redriveCount,omitempty" cbor:"redriveCount,omitempty"`
	RedriveDate     float64 `json:"redriveDate,omitempty" cbor:"redriveDate,omitempty"`

	StateMachineVersionArn string `json:"stateMachineVersionArn,omitempty" cbor:"stateMachineVersionArn,omitempty"`
	StateMachineAliasArn   string `json:"stateMachineAliasArn,omitempty" cbor:"stateMachineAliasArn,omitempty"`
}

type listExecutionsResponse struct {
	Executions []executionListItem `json:"executions" cbor:"executions"`
	NextToken  string              `json:"nextToken,omitempty" cbor:"nextToken,omitempty"`
}

// executionStatusValues is the complete ExecutionStatus enum ListExecutions'
// statusFilter draws from.
//
// https://docs.aws.amazon.com/step-functions/latest/apireference/API_ListExecutions.html
var executionStatusValues = []string{
	"RUNNING", "SUCCEEDED", "FAILED", "TIMED_OUT", "ABORTED", "PENDING_REDRIVE",
}

var executionStatusSet = func() map[string]bool {
	set := make(map[string]bool, len(executionStatusValues))
	for _, s := range executionStatusValues {
		set[s] = true
	}
	return set
}()

// errInvalidStatusFilter refuses a statusFilter value outside AWS's
// ExecutionStatus enum. ListExecutions declares ValidationException as one of
// its own errors for exactly this — an empty page would look like "no
// executions in that state" rather than "I do not recognise that state".
func errInvalidStatusFilter(status string) *protocol.AWSError {
	return &protocol.AWSError{
		Code: "ValidationException",
		Message: fmt.Sprintf(
			"1 validation error detected: Value '%s' at 'statusFilter' failed to satisfy constraint: Member must satisfy enum value set: [%s]",
			status, strings.Join(executionStatusValues, ", ")),
		HTTPStatus: http.StatusBadRequest,
	}
}

func (h *Handler) listExecutionsTyped(ctx context.Context, req *listExecutionsRequest) (*listExecutionsResponse, *protocol.AWSError) {
	if req.StatusFilter != "" && !executionStatusSet[req.StatusFilter] {
		return nil, errInvalidStatusFilter(req.StatusFilter)
	}
	switch req.RedriveFilter {
	case "", "REDRIVEN", "NOT_REDRIVEN":
	default:
		return nil, &protocol.AWSError{
			Code:       "ValidationException",
			Message:    fmt.Sprintf("1 validation error detected: Value '%s' at 'redriveFilter' failed to satisfy constraint: Member must satisfy enum value set: [REDRIVEN, NOT_REDRIVEN]", req.RedriveFilter),
			HTTPStatus: http.StatusBadRequest,
		}
	}
	var (
		execs   []*Execution
		err     error
		matches = func(*Execution) bool { return true }
	)
	if req.MapRunArn != "" {
		// A distributed Map's child executions, which do not belong to the
		// state machine's own listing.
		if _, aerr := h.getMapRun(ctx, req.MapRunArn); aerr != nil {
			return nil, aerr
		}
		execs, err = h.store.ListExecutionsWithPrefix(ctx, mapRunExecutionPrefix(req.MapRunArn))
		kept := execs[:0]
		for _, exec := range execs {
			if exec.MapRunArn == req.MapRunArn {
				kept = append(kept, exec)
			}
		}
		execs = kept
	} else {
		// stateMachineArn may be a version or alias ARN, which narrows the
		// list to the executions started through it (execution_target.go).
		sm, filter, aerr := h.executionFilter(ctx, req.StateMachineArn)
		if aerr != nil {
			return nil, aerr
		}
		matches = filter
		execs, err = h.store.ListExecutions(ctx, sm.ARN)
	}
	if err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	items := make([]executionListItem, 0, len(execs))
	for _, exec := range execs {
		if (req.StatusFilter != "" && exec.Status != req.StatusFilter) || !matches(exec) {
			continue
		}
		if (req.RedriveFilter == "REDRIVEN" && exec.RedriveCount == 0) || (req.RedriveFilter == "NOT_REDRIVEN" && exec.RedriveCount > 0) {
			continue
		}
		item := executionListItem{
			ExecutionArn:           exec.ExecutionArn,
			StateMachineArn:        exec.StateMachineArn,
			Name:                   exec.Name,
			Status:                 exec.Status,
			StartDate:              epochSeconds(exec.StartDate),
			StateMachineVersionArn: exec.StateMachineVersionArn,
			StateMachineAliasArn:   exec.StateMachineAliasArn,
			MapRunArn:              exec.MapRunArn,
			RedriveCount:           exec.RedriveCount,
		}
		if exec.StopDate != nil {
			item.StopDate = epochSeconds(*exec.StopDate)
		}
		if exec.RedriveDate != nil {
			item.RedriveDate = epochSeconds(*exec.RedriveDate)
		}
		items = append(items, item)
	}
	page, pageErr := paginate(items, req.MaxResults, req.NextToken)
	if pageErr != nil {
		return nil, pageErr
	}
	return &listExecutionsResponse{Executions: page.Items, NextToken: page.NextToken}, nil
}

// ─── StopExecution ────────────────────────────────────────────────────────────

type stopExecutionRequest struct {
	ExecutionArn string `json:"executionArn" cbor:"executionArn"`
	Error        string `json:"error" cbor:"error"`
	Cause        string `json:"cause" cbor:"cause"`
}

type stopExecutionResponse struct {
	StopDate float64 `json:"stopDate" cbor:"stopDate"`
}

// stopExecutionTyped aborts an execution that is still RUNNING.
//
// A live execution is asked to unwind: the interpreter observes the cancelled
// context, stops between states, and its own goroutine writes the ABORTED
// record with the error and cause supplied here. As on AWS the stop is
// therefore asynchronous — this returns the stop time, and the execution
// reaches ABORTED a moment later.
//
// An execution that is already terminal reports its recorded stop time
// unchanged, and a RUNNING record with no live run (left behind by a process
// that exited mid-execution) is transitioned here directly.
func (h *Handler) stopExecutionTyped(ctx context.Context, req *stopExecutionRequest) (*stopExecutionResponse, *protocol.AWSError) {
	exec, aerr := h.getExecution(ctx, req.ExecutionArn)
	if aerr != nil {
		return nil, aerr
	}
	if exec.Status != statusRunning {
		stopped := exec.StartDate
		if exec.StopDate != nil {
			stopped = *exec.StopDate
		}
		return &stopExecutionResponse{StopDate: epochSeconds(stopped)}, nil
	}

	stopped := h.clk.Now()
	if run := h.lookupRun(req.ExecutionArn); run != nil {
		run.stop(stopped, req.Error, req.Cause)
		return &stopExecutionResponse{StopDate: epochSeconds(stopped)}, nil
	}

	exec.Status = statusAborted
	exec.StopDate = &stopped
	exec.Error = req.Error
	exec.Cause = req.Cause
	if err := h.store.PutExecution(ctx, exec); err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	return &stopExecutionResponse{StopDate: epochSeconds(stopped)}, nil
}

// ─── DescribeStateMachineForExecution ─────────────────────────────────────────

type describeStateMachineForExecutionRequest struct {
	ExecutionArn string `json:"executionArn" cbor:"executionArn"`
}

type describeStateMachineForExecutionResponse struct {
	StateMachineArn string  `json:"stateMachineArn" cbor:"stateMachineArn"`
	Name            string  `json:"name" cbor:"name"`
	Definition      string  `json:"definition" cbor:"definition"`
	RoleArn         string  `json:"roleArn" cbor:"roleArn"`
	UpdateDate      float64 `json:"updateDate" cbor:"updateDate"`
}

func (h *Handler) describeStateMachineForExecutionTyped(ctx context.Context, req *describeStateMachineForExecutionRequest) (*describeStateMachineForExecutionResponse, *protocol.AWSError) {
	exec, aerr := h.getExecution(ctx, req.ExecutionArn)
	if aerr != nil {
		return nil, aerr
	}
	// The definition the execution ran: its version's, when it ran one.
	sm, aerr := h.stateMachineForExecution(ctx, exec)
	if aerr != nil {
		return nil, aerr
	}
	return &describeStateMachineForExecutionResponse{
		StateMachineArn: sm.ARN,
		Name:            sm.Name,
		Definition:      sm.Definition,
		RoleArn:         sm.RoleArn,
		UpdateDate:      epochSeconds(sm.CreatedAt),
	}, nil
}

// ─── Shared helpers ───────────────────────────────────────────────────────────

func (h *Handler) getExecution(ctx context.Context, arn string) (*Execution, *protocol.AWSError) {
	if strings.TrimSpace(arn) == "" {
		return nil, &protocol.AWSError{
			Code:       "InvalidArn",
			Message:    "Invalid Arn: 'executionArn is required'",
			HTTPStatus: http.StatusBadRequest,
		}
	}
	exec, err := h.store.GetExecution(ctx, arn)
	if err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	if exec == nil {
		return nil, &protocol.AWSError{
			Code:       "ExecutionDoesNotExist",
			Message:    fmt.Sprintf("Execution Does Not Exist: '%s'", arn),
			HTTPStatus: http.StatusBadRequest,
		}
	}
	return exec, nil
}

// epochSeconds renders a timestamp the way the Step Functions wire protocol
// does: epoch seconds carrying millisecond precision.
func epochSeconds(t time.Time) float64 { return float64(t.UnixMilli()) / 1000.0 }
