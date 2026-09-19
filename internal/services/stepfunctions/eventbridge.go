package stepfunctions

// eventbridge.go — bridges Step Functions execution status transitions onto
// the default EventBridge bus, the way real Step Functions publishes a
// `Step Functions Execution Status Change` event every time a standard
// workflow execution's status changes. This is the remainder of #758 after
// EC2 and ECS shipped in #1225 — see #1221.
//
// The hook lives at the store layer (Store.PutExecution, in store.go) so it
// fires for every call site that commits a status — StartExecution's initial
// RUNNING write, completeExecution's terminal write (SUCCEEDED, FAILED,
// TIMED_OUT, ABORTED), the panic-recovery FAILED write, and
// StopExecution's direct ABORTED write for a run with no live goroutine —
// with no per-call-site wiring and no risk of a new one forgetting to
// notify. Mirrors ec2Store.notifyInstanceStateChange /
// ecsStore.notifyTaskStateChange (internal/services/ec2/eventbridge.go /
// internal/services/ecs/eventbridge.go).
//
// Reference: https://docs.aws.amazon.com/step-functions/latest/dg/eventbridge-integration.html#event-detail-execution-status-change
//
//	{
//	   . . .,
//	   "detail-type": "Step Functions Execution Status Change",
//	   "source": "aws.states",
//	   . . .,
//	   "detail": {
//	     "executionArn" : "string",
//	     "input" : "string",
//	     "inputDetails" : { "included" : "boolean" },
//	     "name" : "string",
//	     "output" : "string",
//	     "outputDetails" : { "included" : "boolean" },
//	     "startDate" : "integer",
//	     "stateMachineArn" : "string",
//	     "stopDate" : "integer",
//	     "status" : "RUNNING" | "SUCCEEDED" | "FAILED" | "TIMED_OUT" | "ABORTED" | "PENDING_REDRIVE",
//	     "stateMachineVersionArn" : "string",
//	     "stateMachineAliasArn" : "string",
//	     "redriveCount" : "integer",
//	     "redriveDate" : "string",
//	     "redriveStatus" : "NOT_REDRIVABLE" | "REDRIVABLE" | "REDRIVE_IN_PROGRESS",
//	     "redriveStatusReason" : "string",
//	     "error" : "string",
//	     "cause" : "string"
//	   }
//	}
//
// stateMachineVersionArn and stateMachineAliasArn are present when the
// execution was started through a version or alias ARN, and omitted
// otherwise. redriveCount is always present; redriveStatus, redriveDate and
// redriveStatusReason are omitted — DescribeExecution reports them — rather
// than computed here from a state machine lookup on every status change. AWS's
// own docs tell consumers de-serializing this event to tolerate unknown or
// absent properties, which is exactly what an omission is from the other side.
//
// version, id, account, time and region are filled in by the EventBridge
// service itself (internal/services/eventbridge/delivery.go putEventsEnvelope) —
// producers only supply source, detail-type, detail and resources.

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/overcast-sh/overcast/internal/events"
)

const (
	eventBridgeSFNSource               = "aws.states"
	eventBridgeSFNExecutionChangedType = "Step Functions Execution Status Change"
)

// sfnExecutionDataDetails is AWS's CloudWatchEventsExecutionDataDetails
// shape: whether the accompanying input/output was included verbatim (real
// AWS omits it above a 248 KiB combined size; Overcast never truncates, so
// this is always true when the payload itself is present).
type sfnExecutionDataDetails struct {
	Included bool `json:"included"`
}

// sfnExecutionStatusChangeDetail is the detail document AWS documents for
// this event, restricted to the fields Overcast actually tracks (see the
// package doc comment above for the fields omitted rather than fabricated).
// stopDate, output, outputDetails, error and cause are pointers so they
// marshal as explicit JSON nulls when not yet applicable, matching AWS's own
// documented examples for a RUNNING execution rather than dropping the key.
type sfnExecutionStatusChangeDetail struct {
	ExecutionArn    string                   `json:"executionArn"`
	Input           string                   `json:"input"`
	InputDetails    sfnExecutionDataDetails  `json:"inputDetails"`
	Name            string                   `json:"name"`
	Output          *string                  `json:"output"`
	OutputDetails   *sfnExecutionDataDetails `json:"outputDetails"`
	StartDate       int64                    `json:"startDate"`
	StateMachineArn string                   `json:"stateMachineArn"`
	StopDate        *int64                   `json:"stopDate"`
	Status          string                   `json:"status"`
	Error           *string                  `json:"error"`
	Cause           *string                  `json:"cause"`
	// Present only for an execution started through a version or alias ARN.
	StateMachineVersionArn string `json:"stateMachineVersionArn,omitempty"`
	StateMachineAliasArn   string `json:"stateMachineAliasArn,omitempty"`
	RedriveCount           int    `json:"redriveCount"`
}

// InitEventBridge wires the EventBridge bus publisher so Step Functions
// execution status transitions emit onto the default bus the way real Step
// Functions does. Called once during router construction, after both
// services exist; nil (untested/wired) leaves notifyExecutionStatusChange a
// no-op.
func (s *Service) InitEventBridge(bus events.BusPublisher) {
	s.handler.store.eventBridge = bus
}

// buildExecutionStatusChangeEntry renders one execution as an EventBridge
// entry, AWS-shaped per the reference at the top of this file.
func buildExecutionStatusChangeEntry(exec *Execution) (events.BusEntry, bool) {
	if exec == nil {
		return events.BusEntry{}, false
	}

	detail := sfnExecutionStatusChangeDetail{
		ExecutionArn:    exec.ExecutionArn,
		Input:           exec.Input,
		InputDetails:    sfnExecutionDataDetails{Included: true},
		Name:            exec.Name,
		StartDate:       exec.StartDate.UnixMilli(),
		StateMachineArn: exec.StateMachineArn,
		Status:          exec.Status,

		StateMachineVersionArn: exec.StateMachineVersionArn,
		StateMachineAliasArn:   exec.StateMachineAliasArn,
		RedriveCount:           exec.RedriveCount,
	}
	if exec.StopDate != nil {
		stopMillis := exec.StopDate.UnixMilli()
		detail.StopDate = &stopMillis
	}
	if exec.Output != "" {
		output := exec.Output
		detail.Output = &output
		detail.OutputDetails = &sfnExecutionDataDetails{Included: true}
	}
	if exec.Error != "" {
		errName := exec.Error
		detail.Error = &errName
	}
	if exec.Cause != "" {
		cause := exec.Cause
		detail.Cause = &cause
	}

	raw, err := json.Marshal(detail)
	if err != nil {
		return events.BusEntry{}, false
	}
	return events.BusEntry{
		Source:     eventBridgeSFNSource,
		DetailType: eventBridgeSFNExecutionChangedType,
		Detail:     string(raw),
		Resources:  []string{exec.ExecutionArn},
	}, true
}

// notifyExecutionStatusChange emits the Step Functions Execution Status
// Change event whenever an execution's status actually changed between prev
// and cur. prev is nil for the first write of a given execution ARN — the
// execution entering RUNNING for the first time — which is itself a real
// transition AWS notifies on, so it is treated as a change rather than
// skipped.
//
// Per AWS's own docs (https://docs.aws.amazon.com/step-functions/latest/dg/eventbridge-integration.html#supported-events),
// "Only standard workflows emit events to EventBridge" — an EXPRESS state
// machine's executions are monitored through CloudWatch Logs instead, so this
// looks up the owning state machine's Type and stays silent for EXPRESS.
//
// It is a no-op when EventBridge is not wired (most unit tests), the status
// did not change, or the owning state machine is EXPRESS.
func (st *Store) notifyExecutionStatusChange(ctx context.Context, prev, cur *Execution) {
	if st.eventBridge == nil || cur == nil {
		return
	}
	if prev != nil && prev.Status == cur.Status {
		return
	}
	sm, err := st.GetStateMachine(ctx, extractSMName(cur.StateMachineArn))
	if err != nil || sm == nil {
		return
	}
	if strings.EqualFold(sm.Type, "EXPRESS") {
		return
	}

	entry, ok := buildExecutionStatusChangeEntry(cur)
	if !ok {
		return
	}
	// PublishBusEvent is best-effort by contract (internal/events/sink.go):
	// an error means EventBridge itself could not accept the entry, not that
	// a matching rule failed, and there is nothing more useful to do with it
	// here than the alarmaction dispatcher does with its own PutEvents call.
	_ = st.eventBridge.PublishBusEvent(ctx, entry)
}
