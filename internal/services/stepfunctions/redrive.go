package stepfunctions

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/overcast-sh/overcast/internal/middleware"
	"github.com/overcast-sh/overcast/internal/protocol"
)

// RedriveExecution restarts an unsuccessful Standard execution from the
// top-level state it failed, timed out or was stopped in, with the input that
// state was entered with. States that had already succeeded are not run
// again, the execution keeps its ARN, and its history continues after an
// ExecutionRedriven event — as on AWS. A failed Parallel or Map is re-run as a
// whole.

// redriveWindow is how long after it stops an execution can be redriven.
const redriveWindow = 14 * 24 * time.Hour

// Redrive status values, as DescribeExecution reports them.
const (
	redriveStatusRedrivable    = "REDRIVABLE"
	redriveStatusNotRedrivable = "NOT_REDRIVABLE"
)

// redriveStatus reports whether an execution can be redriven, and why not.
func (h *Handler) redriveStatus(exec *Execution, sm *StateMachine) (status, reason string) {
	switch {
	case exec.Status == statusRunning:
		return redriveStatusNotRedrivable, "Execution is RUNNING and cannot be redriven"
	case exec.Status == statusSucceeded:
		return redriveStatusNotRedrivable, "Execution is SUCCEEDED and cannot be redriven"
	case sm != nil && strings.EqualFold(sm.Type, "EXPRESS"):
		return redriveStatusNotRedrivable, "Express executions cannot be redriven"
	case exec.RedriveState == "":
		return redriveStatusNotRedrivable, "Execution failed before entering any state and cannot be redriven"
	case exec.StopDate != nil && h.clk.Now().Sub(*exec.StopDate) > redriveWindow:
		return redriveStatusNotRedrivable, "Execution stopped more than 14 days ago and cannot be redriven"
	}
	return redriveStatusRedrivable, ""
}

type redriveExecutionRequest struct {
	ExecutionArn string `json:"executionArn" cbor:"executionArn"`
	ClientToken  string `json:"clientToken" cbor:"clientToken"`
}

type redriveExecutionResponse struct {
	RedriveDate float64 `json:"redriveDate" cbor:"redriveDate"`
}

func errNotRedrivable(arn, reason string) *protocol.AWSError {
	return &protocol.AWSError{
		Code:       "ExecutionNotRedrivable",
		Message:    fmt.Sprintf("Execution Not Redrivable: '%s' — %s", arn, reason),
		HTTPStatus: http.StatusBadRequest,
	}
}

func (h *Handler) redriveExecutionTyped(ctx context.Context, req *redriveExecutionRequest) (*redriveExecutionResponse, *protocol.AWSError) {
	exec, aerr := h.getExecution(ctx, req.ExecutionArn)
	if aerr != nil {
		return nil, aerr
	}
	// A redrive runs the definition the execution ran: the version it was
	// started through, or else the state machine's current one.
	target := exec.StateMachineArn
	if exec.StateMachineVersionArn != "" {
		target = exec.StateMachineVersionArn
	}
	sm, _, aerr := h.resolveExecutionTarget(ctx, target)
	if aerr != nil {
		return nil, aerr
	}
	if status, reason := h.redriveStatus(exec, sm); status != redriveStatusRedrivable {
		return nil, errNotRedrivable(exec.ExecutionArn, reason)
	}
	if h.lookupRun(exec.ExecutionArn) != nil {
		return nil, errNotRedrivable(exec.ExecutionArn, "Execution is already being redriven")
	}

	events, err := h.store.GetHistory(ctx, exec.ExecutionArn)
	if err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}

	now := h.clk.Now()
	exec.Status = statusRunning
	exec.RedriveCount++
	exec.RedriveDate = &now
	exec.StopDate = nil
	exec.Error, exec.Cause, exec.Output = "", "", ""
	run := &executionRun{
		hist:            resumeHistoryRecorder(events, maxHistoryEvents),
		resumeState:     exec.RedriveState,
		resumeInput:     exec.RedriveInput,
		resumeVariables: exec.RedriveVariables,
	}
	run.hist.add(now, HistoryEvent{
		Type:              evtExecutionRedriven,
		ExecutionRedriven: &executionRedrivenDetails{RedriveCount: exec.RedriveCount},
	})
	if err := h.store.PutExecution(ctx, exec); err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	region := middleware.RegionFromContext(ctx, h.cfg.Region)
	if aerr := h.launchAsync(ctx, sm, exec, region, 0, run); aerr != nil {
		return nil, aerr
	}
	return &redriveExecutionResponse{RedriveDate: epochSeconds(now)}, nil
}
