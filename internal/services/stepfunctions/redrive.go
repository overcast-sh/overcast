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
// ExecutionRedriven event — as on AWS. Inside a failed Parallel or inline Map
// only the branches and iterations that did not succeed run again, each from
// the state it stopped in; a failed distributed Map redrives its map run
// (redrive_checkpoint.go, distributed_map.go).

const (
	// redriveWindow is how long after it stops an execution can be redriven.
	redriveWindow = 14 * 24 * time.Hour
	// maxExecutionOpenTime is AWS's one-year limit on how long an execution
	// may stay open; one that started longer ago cannot be redriven.
	maxExecutionOpenTime = 365 * 24 * time.Hour
	// maxRedriveHistoryEvents is the history size at which a redrive no
	// longer fits: it needs room for ExecutionRedriven and at least one more
	// event under the 25,000-event cap.
	maxRedriveHistoryEvents = maxHistoryEvents - 1
)

// Redrive status values, as DescribeExecution reports them.
const (
	redriveStatusRedrivable    = "REDRIVABLE"
	redriveStatusNotRedrivable = "NOT_REDRIVABLE"
	// redriveStatusByMapRun is an EXPRESS child of a distributed Map: only a
	// redrive of its map run (which starts it again) can re-run it.
	redriveStatusByMapRun = "REDRIVABLE_BY_MAP_RUN"
)

// Why an execution is not redrivable, in AWS's words.
const (
	reasonRunning          = "Execution is RUNNING and cannot be redriven"
	reasonSucceeded        = "Execution is SUCCEEDED and cannot be redriven"
	reasonHistoryLimit     = "Execution history event limit exceeded"
	reasonMaxExecutionTime = "Execution has exceeded the max execution time"
	reasonPeriodExceeded   = "Execution redrivable period exceeded"
)

// redriveStatus reports whether an execution can be redriven, and why not.
// The history is read only when every cheaper condition has passed, so
// describing a successful or running execution costs nothing extra.
func (h *Handler) redriveStatus(ctx context.Context, exec *Execution, sm *StateMachine) (status, reason string) {
	now := h.clk.Now()
	switch {
	case exec.Status == statusRunning:
		return redriveStatusNotRedrivable, reasonRunning
	case exec.Status == statusSucceeded:
		return redriveStatusNotRedrivable, reasonSucceeded
	case exec.MapRunArn != "" && strings.EqualFold(exec.ExecutionType, "EXPRESS"):
		return redriveStatusByMapRun, ""
	case sm != nil && strings.EqualFold(sm.Type, "EXPRESS"):
		return redriveStatusNotRedrivable, "Express executions cannot be redriven"
	case now.Sub(exec.StartDate) > maxExecutionOpenTime:
		return redriveStatusNotRedrivable, reasonMaxExecutionTime
	case exec.StopDate != nil && now.Sub(*exec.StopDate) > redriveWindow:
		return redriveStatusNotRedrivable, reasonPeriodExceeded
	}
	if events, err := h.store.GetHistory(ctx, exec.ExecutionArn); err == nil && len(events) >= maxRedriveHistoryEvents {
		return redriveStatusNotRedrivable, reasonHistoryLimit
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
	// A distributed Map's children are redriven by redriving the parent,
	// which redrives the map run they belong to.
	if exec.MapRunArn != "" {
		return nil, errNotRedrivable(exec.ExecutionArn, "Execution was started by a Map Run; redrive its parent execution to redrive the Map Run")
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
	if status, reason := h.redriveStatus(ctx, exec, sm); status != redriveStatusRedrivable {
		return nil, errNotRedrivable(exec.ExecutionArn, reason)
	}
	if h.lookupRun(exec.ExecutionArn) != nil {
		return nil, errNotRedrivable(exec.ExecutionArn, "Execution is already being redriven")
	}

	run, err := h.beginRedrive(ctx, exec)
	if err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	if err := h.store.PutExecution(ctx, exec); err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	region := middleware.RegionFromContext(ctx, h.cfg.Region)
	if aerr := h.launchAsync(ctx, sm, exec, region, 0, run); aerr != nil {
		return nil, aerr
	}
	return &redriveExecutionResponse{RedriveDate: epochSeconds(*exec.RedriveDate)}, nil
}

// beginRedrive turns a stopped execution's record into a running one — its
// redrive count and date advanced — and returns the run that resumes it: its
// history continued after an ExecutionRedriven event, and its resume point
// read from the record and the stored checkpoint. The caller persists exec.
func (h *Handler) beginRedrive(ctx context.Context, exec *Execution) (*executionRun, error) {
	events, err := h.store.GetHistory(ctx, exec.ExecutionArn)
	if err != nil {
		return nil, err
	}
	now := h.clk.Now()
	run := &executionRun{
		hist:   resumeHistoryRecorder(events, maxHistoryEvents),
		resume: h.resumePoint(ctx, exec),
	}
	exec.Status = statusRunning
	exec.RedriveCount++
	exec.RedriveDate = &now
	exec.StopDate = nil
	exec.Error, exec.Cause, exec.Output = "", "", ""
	run.hist.add(now, HistoryEvent{
		Type:              evtExecutionRedriven,
		ExecutionRedriven: &executionRedrivenDetails{RedriveCount: exec.RedriveCount},
	})
	return run, nil
}
