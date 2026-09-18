package stepfunctions

import (
	"sync"
	"time"
)

// This file models AWS's execution-history event vocabulary, and records the
// Task lifecycle events into it. The type names, the per-type detail field
// names and the id/previousEventId linkage all match the Step Functions API so
// that step-functions-local-style assertions work against Overcast unmodified.

// History event type names, spelled exactly as the Step Functions API spells them.
const (
	evtExecutionStarted   = "ExecutionStarted"
	evtExecutionSucceeded = "ExecutionSucceeded"
	evtExecutionFailed    = "ExecutionFailed"
	evtExecutionAborted   = "ExecutionAborted"
	evtExecutionTimedOut  = "ExecutionTimedOut"
	evtExecutionRedriven  = "ExecutionRedriven"
	evtEvaluationFailed   = "EvaluationFailed"

	evtTaskScheduled = "TaskScheduled"
	evtTaskStarted   = "TaskStarted"
	evtTaskSucceeded = "TaskSucceeded"
	evtTaskFailed    = "TaskFailed"
	evtTaskTimedOut  = "TaskTimedOut"
	evtTaskSubmitted = "TaskSubmitted"

	evtActivityScheduled      = "ActivityScheduled"
	evtActivityScheduleFailed = "ActivityScheduleFailed"
	evtActivityStarted        = "ActivityStarted"
	evtActivitySucceeded      = "ActivitySucceeded"
	evtActivityFailed         = "ActivityFailed"
	evtActivityTimedOut       = "ActivityTimedOut"

	evtLambdaFunctionScheduled = "LambdaFunctionScheduled"
	evtLambdaFunctionStarted   = "LambdaFunctionStarted"
	evtLambdaFunctionSucceeded = "LambdaFunctionSucceeded"
	evtLambdaFunctionFailed    = "LambdaFunctionFailed"
	evtLambdaFunctionTimedOut  = "LambdaFunctionTimedOut"

	evtParallelStateStarted   = "ParallelStateStarted"
	evtParallelStateSucceeded = "ParallelStateSucceeded"
	evtParallelStateFailed    = "ParallelStateFailed"

	evtMapStateStarted   = "MapStateStarted"
	evtMapStateSucceeded = "MapStateSucceeded"
	evtMapStateFailed    = "MapStateFailed"

	evtMapIterationStarted   = "MapIterationStarted"
	evtMapIterationSucceeded = "MapIterationSucceeded"
	evtMapIterationFailed    = "MapIterationFailed"
	evtMapIterationAborted   = "MapIterationAborted"

	evtMapRunStarted   = "MapRunStarted"
	evtMapRunSucceeded = "MapRunSucceeded"
	evtMapRunFailed    = "MapRunFailed"
	evtMapRunRedriven  = "MapRunRedriven"
)

// stateAbortedEventType returns the `<Type>StateAborted` event AWS records
// when a state is interrupted mid-flight — by StopExecution, by the execution
// timing out, or by a sibling Parallel branch or Map iteration failing. Only
// the four state types that can be in flight have one.
func stateAbortedEventType(stateType string) string {
	switch stateType {
	case stateTypeTask, stateTypeWait, stateTypeParallel, stateTypeMap:
		return stateType + "StateAborted"
	}
	return ""
}

// stateEnteredEventType returns the `<Type>StateEntered` event name for a
// state type, e.g. Task → TaskStateEntered.
func stateEnteredEventType(stateType string) string { return stateType + "StateEntered" }

// stateExitedEventType returns the `<Type>StateExited` event name for a state
// type. Fail states have no exit event on AWS — the execution ends there.
func stateExitedEventType(stateType string) string { return stateType + "StateExited" }

// executionDataDetails mirrors AWS's HistoryEventExecutionDataDetails. Overcast
// never truncates payloads, so `truncated` is always false — the field is
// present because SDK models and assertions expect it.
type executionDataDetails struct {
	Truncated bool `json:"truncated" cbor:"truncated"`
}

type executionStartedDetails struct {
	Input        string                `json:"input,omitempty" cbor:"input,omitempty"`
	InputDetails *executionDataDetails `json:"inputDetails,omitempty" cbor:"inputDetails,omitempty"`
	RoleArn      string                `json:"roleArn,omitempty" cbor:"roleArn,omitempty"`

	StateMachineVersionArn string `json:"stateMachineVersionArn,omitempty" cbor:"stateMachineVersionArn,omitempty"`
	StateMachineAliasArn   string `json:"stateMachineAliasArn,omitempty" cbor:"stateMachineAliasArn,omitempty"`
}

type executionSucceededDetails struct {
	Output        string                `json:"output,omitempty" cbor:"output,omitempty"`
	OutputDetails *executionDataDetails `json:"outputDetails,omitempty" cbor:"outputDetails,omitempty"`
}

type executionRedrivenDetails struct {
	RedriveCount int `json:"redriveCount" cbor:"redriveCount"`
}

type errorCauseDetails struct {
	Error string `json:"error,omitempty" cbor:"error,omitempty"`
	Cause string `json:"cause,omitempty" cbor:"cause,omitempty"`
}

type stateEnteredDetails struct {
	Name         string                `json:"name" cbor:"name"`
	Input        string                `json:"input,omitempty" cbor:"input,omitempty"`
	InputDetails *executionDataDetails `json:"inputDetails,omitempty" cbor:"inputDetails,omitempty"`
}

type stateExitedDetails struct {
	Name          string                `json:"name" cbor:"name"`
	Output        string                `json:"output,omitempty" cbor:"output,omitempty"`
	OutputDetails *executionDataDetails `json:"outputDetails,omitempty" cbor:"outputDetails,omitempty"`
	// AssignedVariables maps each variable the state assigned to its new
	// value, serialized as JSON.
	AssignedVariables        map[string]string     `json:"assignedVariables,omitempty" cbor:"assignedVariables,omitempty"`
	AssignedVariablesDetails *executionDataDetails `json:"assignedVariablesDetails,omitempty" cbor:"assignedVariablesDetails,omitempty"`
}

type evaluationFailedDetails struct {
	Error    string `json:"error,omitempty" cbor:"error,omitempty"`
	Cause    string `json:"cause,omitempty" cbor:"cause,omitempty"`
	Location string `json:"location,omitempty" cbor:"location,omitempty"`
	State    string `json:"state" cbor:"state"`
}

type taskScheduledDetails struct {
	ResourceType       string `json:"resourceType" cbor:"resourceType"`
	Resource           string `json:"resource" cbor:"resource"`
	Region             string `json:"region,omitempty" cbor:"region,omitempty"`
	Parameters         string `json:"parameters,omitempty" cbor:"parameters,omitempty"`
	TimeoutInSeconds   *int64 `json:"timeoutInSeconds,omitempty" cbor:"timeoutInSeconds,omitempty"`
	HeartbeatInSeconds *int64 `json:"heartbeatInSeconds,omitempty" cbor:"heartbeatInSeconds,omitempty"`
}

type activityScheduledDetails struct {
	Resource           string                `json:"resource" cbor:"resource"`
	Input              string                `json:"input,omitempty" cbor:"input,omitempty"`
	InputDetails       *executionDataDetails `json:"inputDetails,omitempty" cbor:"inputDetails,omitempty"`
	TimeoutInSeconds   *int64                `json:"timeoutInSeconds,omitempty" cbor:"timeoutInSeconds,omitempty"`
	HeartbeatInSeconds *int64                `json:"heartbeatInSeconds,omitempty" cbor:"heartbeatInSeconds,omitempty"`
}

type activityStartedDetails struct {
	WorkerName string `json:"workerName,omitempty" cbor:"workerName,omitempty"`
}

type taskStartedDetails struct {
	ResourceType string `json:"resourceType" cbor:"resourceType"`
	Resource     string `json:"resource" cbor:"resource"`
}

type taskSucceededDetails struct {
	ResourceType  string                `json:"resourceType" cbor:"resourceType"`
	Resource      string                `json:"resource" cbor:"resource"`
	Output        string                `json:"output,omitempty" cbor:"output,omitempty"`
	OutputDetails *executionDataDetails `json:"outputDetails,omitempty" cbor:"outputDetails,omitempty"`
}

type taskErrorDetails struct {
	ResourceType string `json:"resourceType" cbor:"resourceType"`
	Resource     string `json:"resource" cbor:"resource"`
	Error        string `json:"error,omitempty" cbor:"error,omitempty"`
	Cause        string `json:"cause,omitempty" cbor:"cause,omitempty"`
}

type lambdaScheduledDetails struct {
	Resource         string                `json:"resource" cbor:"resource"`
	Input            string                `json:"input,omitempty" cbor:"input,omitempty"`
	InputDetails     *executionDataDetails `json:"inputDetails,omitempty" cbor:"inputDetails,omitempty"`
	TimeoutInSeconds *int64                `json:"timeoutInSeconds,omitempty" cbor:"timeoutInSeconds,omitempty"`
}

type lambdaSucceededDetails struct {
	Output        string                `json:"output,omitempty" cbor:"output,omitempty"`
	OutputDetails *executionDataDetails `json:"outputDetails,omitempty" cbor:"outputDetails,omitempty"`
}

type mapStateStartedDetails struct {
	Length int64 `json:"length" cbor:"length"`
}

type mapRunStartedDetails struct {
	MapRunArn string `json:"mapRunArn" cbor:"mapRunArn"`
}

type mapRunRedrivenDetails struct {
	MapRunArn    string `json:"mapRunArn" cbor:"mapRunArn"`
	RedriveCount int    `json:"redriveCount" cbor:"redriveCount"`
}

type mapIterationDetails struct {
	Name  string `json:"name,omitempty" cbor:"name,omitempty"`
	Index int64  `json:"index" cbor:"index"`
}

// HistoryEvent is one execution-history entry. It is persisted as-is and
// returned by GetExecutionHistory, so the JSON tags are both the storage
// format and the wire format.
type HistoryEvent struct {
	// Timestamp is epoch seconds with millisecond precision — the AWS JSON 1.0
	// and RPC v2 CBOR wire representation of a Step Functions timestamp, and
	// also how the event is persisted.
	Timestamp       float64 `json:"timestamp" cbor:"timestamp"`
	Type            string  `json:"type" cbor:"type"`
	ID              int64   `json:"id" cbor:"id"`
	PreviousEventID int64   `json:"previousEventId" cbor:"previousEventId"`

	ExecutionStarted   *executionStartedDetails   `json:"executionStartedEventDetails,omitempty" cbor:"executionStartedEventDetails,omitempty"`
	ExecutionSucceeded *executionSucceededDetails `json:"executionSucceededEventDetails,omitempty" cbor:"executionSucceededEventDetails,omitempty"`
	ExecutionFailed    *errorCauseDetails         `json:"executionFailedEventDetails,omitempty" cbor:"executionFailedEventDetails,omitempty"`
	ExecutionAborted   *errorCauseDetails         `json:"executionAbortedEventDetails,omitempty" cbor:"executionAbortedEventDetails,omitempty"`
	ExecutionTimedOut  *errorCauseDetails         `json:"executionTimedOutEventDetails,omitempty" cbor:"executionTimedOutEventDetails,omitempty"`
	ExecutionRedriven  *executionRedrivenDetails  `json:"executionRedrivenEventDetails,omitempty" cbor:"executionRedrivenEventDetails,omitempty"`

	StateEntered *stateEnteredDetails `json:"stateEnteredEventDetails,omitempty" cbor:"stateEnteredEventDetails,omitempty"`
	StateExited  *stateExitedDetails  `json:"stateExitedEventDetails,omitempty" cbor:"stateExitedEventDetails,omitempty"`

	EvaluationFailed *evaluationFailedDetails `json:"evaluationFailedEventDetails,omitempty" cbor:"evaluationFailedEventDetails,omitempty"`

	TaskScheduled *taskScheduledDetails `json:"taskScheduledEventDetails,omitempty" cbor:"taskScheduledEventDetails,omitempty"`
	TaskStarted   *taskStartedDetails   `json:"taskStartedEventDetails,omitempty" cbor:"taskStartedEventDetails,omitempty"`
	TaskSucceeded *taskSucceededDetails `json:"taskSucceededEventDetails,omitempty" cbor:"taskSucceededEventDetails,omitempty"`
	TaskFailed    *taskErrorDetails     `json:"taskFailedEventDetails,omitempty" cbor:"taskFailedEventDetails,omitempty"`
	TaskTimedOut  *taskErrorDetails     `json:"taskTimedOutEventDetails,omitempty" cbor:"taskTimedOutEventDetails,omitempty"`
	TaskSubmitted *taskSucceededDetails `json:"taskSubmittedEventDetails,omitempty" cbor:"taskSubmittedEventDetails,omitempty"`

	ActivityScheduled      *activityScheduledDetails `json:"activityScheduledEventDetails,omitempty" cbor:"activityScheduledEventDetails,omitempty"`
	ActivityScheduleFailed *errorCauseDetails        `json:"activityScheduleFailedEventDetails,omitempty" cbor:"activityScheduleFailedEventDetails,omitempty"`
	ActivityStarted        *activityStartedDetails   `json:"activityStartedEventDetails,omitempty" cbor:"activityStartedEventDetails,omitempty"`
	ActivitySucceeded      *lambdaSucceededDetails   `json:"activitySucceededEventDetails,omitempty" cbor:"activitySucceededEventDetails,omitempty"`
	ActivityFailed         *errorCauseDetails        `json:"activityFailedEventDetails,omitempty" cbor:"activityFailedEventDetails,omitempty"`
	ActivityTimedOut       *errorCauseDetails        `json:"activityTimedOutEventDetails,omitempty" cbor:"activityTimedOutEventDetails,omitempty"`

	LambdaFunctionScheduled *lambdaScheduledDetails `json:"lambdaFunctionScheduledEventDetails,omitempty" cbor:"lambdaFunctionScheduledEventDetails,omitempty"`
	LambdaFunctionSucceeded *lambdaSucceededDetails `json:"lambdaFunctionSucceededEventDetails,omitempty" cbor:"lambdaFunctionSucceededEventDetails,omitempty"`
	LambdaFunctionFailed    *errorCauseDetails      `json:"lambdaFunctionFailedEventDetails,omitempty" cbor:"lambdaFunctionFailedEventDetails,omitempty"`
	LambdaFunctionTimedOut  *errorCauseDetails      `json:"lambdaFunctionTimedOutEventDetails,omitempty" cbor:"lambdaFunctionTimedOutEventDetails,omitempty"`

	MapStateStarted *mapStateStartedDetails `json:"mapStateStartedEventDetails,omitempty" cbor:"mapStateStartedEventDetails,omitempty"`

	MapIterationStarted   *mapIterationDetails   `json:"mapIterationStartedEventDetails,omitempty" cbor:"mapIterationStartedEventDetails,omitempty"`
	MapIterationSucceeded *mapIterationDetails   `json:"mapIterationSucceededEventDetails,omitempty" cbor:"mapIterationSucceededEventDetails,omitempty"`
	MapIterationFailed    *mapIterationDetails   `json:"mapIterationFailedEventDetails,omitempty" cbor:"mapIterationFailedEventDetails,omitempty"`
	MapRunStarted         *mapRunStartedDetails  `json:"mapRunStartedEventDetails,omitempty" cbor:"mapRunStartedEventDetails,omitempty"`
	MapRunFailed          *errorCauseDetails     `json:"mapRunFailedEventDetails,omitempty" cbor:"mapRunFailedEventDetails,omitempty"`
	MapRunRedriven        *mapRunRedrivenDetails `json:"mapRunRedrivenEventDetails,omitempty" cbor:"mapRunRedrivenEventDetails,omitempty"`

	MapIterationAborted *mapIterationDetails `json:"mapIterationAbortedEventDetails,omitempty" cbor:"mapIterationAbortedEventDetails,omitempty"`
}

// historyRecorder accumulates the events of one execution in memory and hands
// them to the store in a single write when the execution finishes. Appending
// to a slice keeps a long execution at one store write rather than one per
// state transition.
//
// It is mutated by the execution's own goroutine and read concurrently by
// GetExecutionHistory, which snapshots it so a RUNNING execution reports the
// states it has already been through — hence the mutex, and hence no extra
// store writes to make progress visible.
type historyRecorder struct {
	mu       sync.Mutex
	events   []HistoryEvent
	previous int64
	limit    int
}

func newHistoryRecorder(limit int) *historyRecorder {
	return &historyRecorder{limit: limit}
}

// resumeHistoryRecorder continues an existing history — a redriven execution
// appends to the events of the run it resumes.
func resumeHistoryRecorder(events []HistoryEvent, limit int) *historyRecorder {
	h := &historyRecorder{limit: limit, events: append([]HistoryEvent(nil), events...)}
	if n := len(events); n > 0 {
		h.previous = events[n-1].ID
	}
	return h
}

// add appends an event, links it to the most recently recorded one, stamps
// it and returns its id. The caller supplies the timestamp so it always comes
// from the injected clock. Execution-level events (ExecutionStarted, the
// terminal event of an execution that never reached the interpreter) use
// this; everything the interpreter records goes through addAfter so that a
// Parallel branch or Map iteration links to its own causal predecessor.
func (h *historyRecorder) add(now time.Time, event HistoryEvent) int64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.appendLocked(now, event, h.previous)
}

// addAfter appends an event whose previousEventId is prev rather than the
// last event recorded. This is AWS's causal linkage: events inside a Parallel
// branch or Map iteration chain back through that branch to its
// ParallelStateStarted / MapIterationStarted event, even though concurrent
// branches interleave in id order.
func (h *historyRecorder) addAfter(now time.Time, event HistoryEvent, prev int64) int64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.appendLocked(now, event, prev)
}

func (h *historyRecorder) appendLocked(now time.Time, event HistoryEvent, prev int64) int64 {
	event.ID = int64(len(h.events)) + 1
	event.PreviousEventID = prev
	event.Timestamp = float64(now.UnixMilli()) / 1000.0
	h.previous = event.ID
	h.events = append(h.events, event)
	return event.ID
}

// lastID returns the id of the most recently recorded event.
func (h *historyRecorder) lastID() int64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.previous
}

// snapshot returns a copy of the events recorded so far, safe to hand to a
// reader while the execution is still running.
func (h *historyRecorder) snapshot() []HistoryEvent {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]HistoryEvent, len(h.events))
	copy(out, h.events)
	return out
}

// full reports whether the recorder has reached its event cap. AWS caps a
// Standard execution's history at 25,000 events and fails the execution when
// it is exceeded; Overcast applies the same rule, which is also what stops a
// runaway Choice loop instead of letting it spin.
func (h *historyRecorder) full() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.events) >= h.limit
}

// ─── Task history events ──────────────────────────────────────────────────────

func (in *interpreter) recordTaskScheduled(integration taskIntegration, resource, parameters string, timeout, heartbeat *int64) {
	now := in.handler.clk.Now()
	if integration.direct() {
		in.recordAt(now, HistoryEvent{
			Type: evtLambdaFunctionScheduled,
			LambdaFunctionScheduled: &lambdaScheduledDetails{
				Resource:         resource,
				Input:            parameters,
				InputDetails:     &executionDataDetails{},
				TimeoutInSeconds: timeout,
			},
		})
		return
	}
	in.recordAt(now, HistoryEvent{
		Type: evtTaskScheduled,
		TaskScheduled: &taskScheduledDetails{
			ResourceType:       integration.service,
			Resource:           integration.action + patternSuffix(integration.pattern),
			Region:             in.region,
			Parameters:         parameters,
			TimeoutInSeconds:   timeout,
			HeartbeatInSeconds: heartbeat,
		},
	})
}

// recordTaskSubmitted records the answer of the call that started a callback
// (.waitForTaskToken) task, before the Task waits for its token.
func (in *interpreter) recordTaskSubmitted(integration taskIntegration, output any) {
	encoded, err := encodeJSON(output)
	if err != nil {
		encoded = ""
	}
	in.record(HistoryEvent{
		Type: evtTaskSubmitted,
		TaskSubmitted: &taskSucceededDetails{
			ResourceType:  integration.service,
			Resource:      integration.action + patternSuffix(integration.pattern),
			Output:        encoded,
			OutputDetails: &executionDataDetails{},
		},
	})
}

func (in *interpreter) recordTaskStarted(integration taskIntegration) {
	now := in.handler.clk.Now()
	if integration.direct() {
		in.recordAt(now, HistoryEvent{Type: evtLambdaFunctionStarted})
		return
	}
	in.recordAt(now, HistoryEvent{
		Type: evtTaskStarted,
		TaskStarted: &taskStartedDetails{
			ResourceType: integration.service,
			Resource:     integration.action + patternSuffix(integration.pattern),
		},
	})
}

func (in *interpreter) recordTaskSucceeded(integration taskIntegration, result any) {
	encoded, err := encodeJSON(result)
	if err != nil {
		encoded = ""
	}
	now := in.handler.clk.Now()
	if integration.direct() {
		in.recordAt(now, HistoryEvent{
			Type: evtLambdaFunctionSucceeded,
			LambdaFunctionSucceeded: &lambdaSucceededDetails{
				Output:        encoded,
				OutputDetails: &executionDataDetails{},
			},
		})
		return
	}
	in.recordAt(now, HistoryEvent{
		Type: evtTaskSucceeded,
		TaskSucceeded: &taskSucceededDetails{
			ResourceType:  integration.service,
			Resource:      integration.action + patternSuffix(integration.pattern),
			Output:        encoded,
			OutputDetails: &executionDataDetails{},
		},
	})
}

func (in *interpreter) recordTaskFailed(integration taskIntegration, serr *stateError) {
	now := in.handler.clk.Now()
	timedOut := serr.name == errTimeout || serr.name == errHeartbeatTimeout
	if integration.direct() {
		eventType := evtLambdaFunctionFailed
		if timedOut {
			eventType = evtLambdaFunctionTimedOut
		}
		event := HistoryEvent{Type: eventType}
		details := &errorCauseDetails{Error: serr.name, Cause: serr.cause}
		if timedOut {
			event.LambdaFunctionTimedOut = details
		} else {
			event.LambdaFunctionFailed = details
		}
		in.recordAt(now, event)
		return
	}
	details := &taskErrorDetails{
		ResourceType: integration.service,
		Resource:     integration.action + patternSuffix(integration.pattern),
		Error:        serr.name,
		Cause:        serr.cause,
	}
	if timedOut {
		in.recordAt(now, HistoryEvent{Type: evtTaskTimedOut, TaskTimedOut: details})
		return
	}
	in.recordAt(now, HistoryEvent{Type: evtTaskFailed, TaskFailed: details})
}
