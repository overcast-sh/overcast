package stepfunctions

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"github.com/overcast-sh/overcast/internal/clock"
	"github.com/overcast-sh/overcast/internal/config"
	"github.com/overcast-sh/overcast/internal/events"
	"github.com/overcast-sh/overcast/internal/protocol"
	"github.com/overcast-sh/overcast/internal/protocol/codec"
	"github.com/overcast-sh/overcast/internal/protocol/op"
	"github.com/overcast-sh/overcast/internal/serviceutil"
)

// Handler holds Step Functions handler dependencies.
type Handler struct {
	cfg   *config.Config
	store *Store
	log   *serviceutil.ServiceLogger
	clk   clock.Clock
	bus   *events.Bus
	// router is Overcast's own root handler. Task states dispatch through it
	// so a workflow step runs exactly the handler an SDK call would, rather
	// than a second, divergent code path. Nil until InitRouter is called, and
	// a Task state then fails loudly rather than pretending to succeed.
	router  http.Handler
	ops     map[string]http.HandlerFunc
	typedOp map[string]op.Operation

	// pickWeight draws the random number in [0, n) that routes an alias
	// execution to one of its versions by weight (pickRoute,
	// execution_target.go). Nil means math/rand/v2's IntN; tests pin it.
	pickWeight func(n int) int

	// Executions run on their own goroutines so StartExecution can accept and
	// return while the execution is still RUNNING, as AWS does. runs tracks
	// the live ones (for DescribeExecution, GetExecutionHistory and
	// StopExecution), wg lets shutdown drain them, and shutdownCancel unwinds
	// an execution parked in a Wait so it cannot hold shutdown open.
	//
	// stopping is guarded by runsMu, the same lock reserveRun (runs.go) takes
	// to register a run and Add to wg. Stop sets it inside that critical
	// section before it ever calls wg.Wait, so a StartExecution/
	// StartSyncExecution/nested-startExecution arriving concurrently either
	// completes its reserveRun call — Add and all — strictly before Stop's
	// critical section runs, or observes stopping and refuses before
	// touching wg at all. Either way, wg.Add can never race wg.Wait (issue
	// #1315, the same shape #1290 fixed in lifecycle.Scheduler and #1298
	// fixed in EKS).
	runs           map[string]*executionRun
	runsMu         sync.Mutex
	stopping       bool
	wg             sync.WaitGroup
	shutdown       context.Context
	shutdownCancel context.CancelFunc

	// tasks holds the task tokens of every Task waiting on a callback
	// (.waitForTaskToken or an activity).
	tasks *taskRegistry

	// mapRuns holds the distributed Map runs in progress, so DescribeMapRun
	// reads live counts and UpdateMapRun reaches a running Map.
	mapRuns   map[string]*liveMapRun
	mapRunsMu sync.Mutex
}

func newHandler(cfg *config.Config, store *Store, log *serviceutil.ServiceLogger, clk clock.Clock) *Handler {
	h := &Handler{cfg: cfg, store: store, log: log, clk: clk, runs: map[string]*executionRun{}, tasks: newTaskRegistry()}
	// Parent of every execution context. Creating it does no I/O, so this is
	// safe in a constructor called from router.New.
	h.shutdown, h.shutdownCancel = context.WithCancel(context.Background())
	h.initOps()
	return h
}

// initOps registers every known StepFunctions operation to its handler.
// Adding a new operation: add an entry here, implement in handler.go.
func (h *Handler) initOps() {
	h.typedOp = h.typedOps()
	h.ops = map[string]http.HandlerFunc{
		"StartExecution": h.StartExecution,
		// The state machine control plane, its versions and aliases, tagging
		// and the execution plane each have a single, typed implementation.
		// These entries route the legacy X-Amz-Target path to it rather than
		// duplicating the logic in a second handler that could drift.
		"CreateStateMachine":               h.typedJSONHandler("CreateStateMachine"),
		"DescribeStateMachine":             h.typedJSONHandler("DescribeStateMachine"),
		"ListStateMachines":                h.typedJSONHandler("ListStateMachines"),
		"DeleteStateMachine":               h.typedJSONHandler("DeleteStateMachine"),
		"UpdateStateMachine":               h.typedJSONHandler("UpdateStateMachine"),
		"ValidateStateMachineDefinition":   h.typedJSONHandler("ValidateStateMachineDefinition"),
		"TagResource":                      h.typedJSONHandler("TagResource"),
		"UntagResource":                    h.typedJSONHandler("UntagResource"),
		"ListTagsForResource":              h.typedJSONHandler("ListTagsForResource"),
		"PublishStateMachineVersion":       h.typedJSONHandler("PublishStateMachineVersion"),
		"ListStateMachineVersions":         h.typedJSONHandler("ListStateMachineVersions"),
		"DeleteStateMachineVersion":        h.typedJSONHandler("DeleteStateMachineVersion"),
		"CreateStateMachineAlias":          h.typedJSONHandler("CreateStateMachineAlias"),
		"DescribeStateMachineAlias":        h.typedJSONHandler("DescribeStateMachineAlias"),
		"UpdateStateMachineAlias":          h.typedJSONHandler("UpdateStateMachineAlias"),
		"DeleteStateMachineAlias":          h.typedJSONHandler("DeleteStateMachineAlias"),
		"ListStateMachineAliases":          h.typedJSONHandler("ListStateMachineAliases"),
		"StartSyncExecution":               h.typedJSONHandler("StartSyncExecution"),
		"DescribeExecution":                h.typedJSONHandler("DescribeExecution"),
		"GetExecutionHistory":              h.typedJSONHandler("GetExecutionHistory"),
		"ListExecutions":                   h.typedJSONHandler("ListExecutions"),
		"StopExecution":                    h.typedJSONHandler("StopExecution"),
		"DescribeStateMachineForExecution": h.typedJSONHandler("DescribeStateMachineForExecution"),
	}
	// Every operation added since the typed path became the single
	// implementation reaches it the same way on the legacy X-Amz-Target path.
	for name := range h.typedOp {
		if _, ok := h.ops[name]; !ok {
			h.ops[name] = h.typedJSONHandler(name)
		}
	}
}

// typedJSONHandler adapts a typed operation to the legacy http.HandlerFunc
// dispatch table, so a caller arriving on the X-Amz-Target path reaches exactly
// the implementation the codec path uses.
func (h *Handler) typedJSONHandler(name string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		operation, ok := h.typedOp[name]
		if !ok {
			protocol.NotImplementedJSON(w, r)
			return
		}
		// Step Functions' own wire protocol is AWS JSON 1.0; prefer whatever
		// the router already resolved for this request.
		wire := codec.JSON10
		if resolved, resolvedOp := codec.FromContext(r.Context()); resolved != nil && resolvedOp == name {
			wire = resolved
		}
		operation.Invoke(w, r, wire)
	}
}

// sfnLoggingConfigOrDefault and sfnTracingConfigOrDefault return AWS's
// documented defaults — logging OFF, tracing disabled — when the state
// machine did not configure either, so DescribeStateMachine always echoes
// the shape real AWS returns instead of omitting the field.
func sfnLoggingConfigOrDefault(v map[string]any) map[string]any {
	if v != nil {
		return v
	}
	return map[string]any{
		"level":                "OFF",
		"includeExecutionData": false,
		"destinations":         []any{},
	}
}

func sfnTracingConfigOrDefault(v map[string]any) map[string]any {
	if v != nil {
		return v
	}
	return map[string]any{"enabled": false}
}

// ── StartExecution ────────────────────────────────────────────────────────────

func (h *Handler) StartExecution(w http.ResponseWriter, r *http.Request) {
	var req struct {
		StateMachineArn string `json:"stateMachineArn"`
		Input           string `json:"input"`
		Name            string `json:"name"`
	}
	if !serviceutil.DecodeJSON(w, r, &req) {
		return
	}

	exec, aerr := h.startExecution(r.Context(), req.StateMachineArn, req.Name, req.Input, 0, executionAsync)
	if aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}

	protocol.WriteJSON(w, r, http.StatusOK, map[string]any{
		"executionArn": exec.ExecutionArn,
		"startDate":    epochSeconds(exec.StartDate),
	})
}

// ── Helpers ───────────────────────────────────────────────────────────────────

// extractSMName extracts the state machine name from an ARN.
// ARN format: arn:aws:states:region:account:stateMachine:name.
func extractSMName(arn string) string {
	// Split: [arn, aws, states, region, account, stateMachine, name...]
	parts := strings.SplitN(arn, ":", 7)
	if len(parts) == 7 {
		return parts[6]
	}
	// Fallback: return the last segment
	if i := strings.LastIndex(arn, ":"); i >= 0 {
		return arn[i+1:]
	}
	return arn
}

// validateDefinitionForType rejects a definition that is not valid Amazon
// States Language, the way AWS's CreateStateMachine does. Definitions that are
// valid ASL but use features Overcast cannot interpret are accepted here — so
// CDK and CloudFormation deploys keep working — and fail the execution loudly
// when they actually run.
//
// smType adds the rules an EXPRESS state machine carries on top.
func validateDefinitionForType(definition, smType string) *protocol.AWSError {
	def, err := parseDefinition(definition)
	if err == nil && strings.EqualFold(smType, "EXPRESS") {
		err = checkExpressDefinition(def)
	}
	if err != nil {
		return &protocol.AWSError{
			Code:       "InvalidDefinition",
			Message:    fmt.Sprintf("Invalid State Machine Definition: '%s'", err.Error()),
			HTTPStatus: http.StatusBadRequest,
		}
	}
	return nil
}

func errSMNotFound(arn string) *protocol.AWSError {
	return &protocol.AWSError{
		Code:       "StateMachineDoesNotExist",
		Message:    fmt.Sprintf("State Machine Does Not Exist: '%s'", arn),
		HTTPStatus: http.StatusBadRequest,
	}
}

// ── Tags ──────────────────────────────────────────────────────────────────────

var sfnTagCfg = serviceutil.TagValidationConfig{
	ExceededCode:    "TooManyTags",
	InvalidCode:     "InvalidParameter",
	ExceededMessage: "Tag count exceeded the maximum of 50 tags per resource.",
}

type tagResourceRequest struct {
	ResourceArn string `json:"resourceArn"`
	Tags        []struct {
		Key   string `json:"key"`
		Value string `json:"value"`
	} `json:"tags"`
}

type untagResourceRequest struct {
	ResourceArn string   `json:"resourceArn"`
	TagKeys     []string `json:"tagKeys"`
}

type listTagsForResourceRequest struct {
	ResourceArn string `json:"resourceArn"`
}
