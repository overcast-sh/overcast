package stepfunctions

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/overcast-sh/overcast/internal/middleware"
	"github.com/overcast-sh/overcast/internal/protocol"
)

// TestState runs one state in isolation — the Task really calls its
// integration unless a mock result is supplied — and reports its output, the
// state it would transition to, and (at DEBUG) the data at each step of its
// input/output processing. As on AWS it runs a single attempt: a failure a
// retrier would handle is reported RETRIABLE rather than retried, and one a
// catcher handles is CAUGHT_ERROR with the catcher's Next.

// TestState outcome statuses.
const (
	testStateSucceeded   = "SUCCEEDED"
	testStateFailed      = "FAILED"
	testStateRetriable   = "RETRIABLE"
	testStateCaughtError = "CAUGHT_ERROR"
)

type testStateMock struct {
	Result      string `json:"result" cbor:"result"`
	ErrorOutput *struct {
		Error string `json:"error" cbor:"error"`
		Cause string `json:"cause" cbor:"cause"`
	} `json:"errorOutput" cbor:"errorOutput"`
}

type testStateRequest struct {
	Definition      string         `json:"definition" cbor:"definition"`
	RoleArn         string         `json:"roleArn" cbor:"roleArn"`
	Input           string         `json:"input" cbor:"input"`
	InspectionLevel string         `json:"inspectionLevel" cbor:"inspectionLevel"`
	RevealSecrets   bool           `json:"revealSecrets" cbor:"revealSecrets"`
	Variables       string         `json:"variables" cbor:"variables"`
	StateName       string         `json:"stateName" cbor:"stateName"`
	Mock            *testStateMock `json:"mock" cbor:"mock"`
	Context         string         `json:"context" cbor:"context"`
}

// inspectionData is AWS's InspectionData: each stage of the state's data flow
// as a JSON string.
type inspectionData struct {
	Input               string `json:"input,omitempty" cbor:"input,omitempty"`
	AfterArguments      string `json:"afterArguments,omitempty" cbor:"afterArguments,omitempty"`
	AfterInputPath      string `json:"afterInputPath,omitempty" cbor:"afterInputPath,omitempty"`
	AfterParameters     string `json:"afterParameters,omitempty" cbor:"afterParameters,omitempty"`
	Result              string `json:"result,omitempty" cbor:"result,omitempty"`
	AfterResultSelector string `json:"afterResultSelector,omitempty" cbor:"afterResultSelector,omitempty"`
	AfterResultPath     string `json:"afterResultPath,omitempty" cbor:"afterResultPath,omitempty"`
	Variables           string `json:"variables,omitempty" cbor:"variables,omitempty"`
}

type testStateResponse struct {
	Output         string          `json:"output,omitempty" cbor:"output,omitempty"`
	Error          string          `json:"error,omitempty" cbor:"error,omitempty"`
	Cause          string          `json:"cause,omitempty" cbor:"cause,omitempty"`
	InspectionData *inspectionData `json:"inspectionData,omitempty" cbor:"inspectionData,omitempty"`
	NextState      string          `json:"nextState,omitempty" cbor:"nextState,omitempty"`
	Status         string          `json:"status" cbor:"status"`
}

// testStateRun is what a TestState evaluation records about its one state.
type testStateRun struct {
	mock    *testStateMock
	inspect *inspectionData
	status  string
}

func errTestStateInvalidDefinition(err error) *protocol.AWSError {
	return &protocol.AWSError{
		Code:       "InvalidDefinition",
		Message:    fmt.Sprintf("Invalid State Machine Definition: '%s'", err.Error()),
		HTTPStatus: http.StatusBadRequest,
	}
}

// parseTestStateDefinition returns the state to test and a branch holding it.
// definition is either one state, or a whole state machine with stateName
// naming the state.
func parseTestStateDefinition(definition, stateName string) (*aslBranch, string, *protocol.AWSError) {
	var probe struct {
		Type   string          `json:"Type"`
		States json.RawMessage `json:"States"`
	}
	if err := json.Unmarshal([]byte(definition), &probe); err != nil {
		return nil, "", errTestStateInvalidDefinition(fmt.Errorf("definition is not valid JSON: %v", err))
	}
	if len(probe.States) > 0 {
		def, err := parseDefinition(definition)
		if err != nil {
			return nil, "", errTestStateInvalidDefinition(err)
		}
		if stateName == "" {
			stateName = def.StartAt
		}
		if def.States[stateName] == nil {
			return nil, "", errTestStateInvalidDefinition(fmt.Errorf("stateName %q does not name a top-level state", stateName))
		}
		return def, stateName, nil
	}

	// A lone state: wrap it in a branch, with a placeholder for every state
	// it can transition to, so the ordinary validation applies.
	var state aslState
	if err := json.Unmarshal([]byte(definition), &state); err != nil {
		return nil, "", errTestStateInvalidDefinition(err)
	}
	name := stateName
	if name == "" {
		name = "TestState"
	}
	states := map[string]json.RawMessage{name: json.RawMessage(definition)}
	placeholder := json.RawMessage(`{"Type":"Succeed"}`)
	targets := []string{state.Next, state.Default}
	for _, rule := range state.Choices {
		if rule != nil {
			targets = append(targets, rule.Next)
		}
	}
	for _, catcher := range state.Catch {
		targets = append(targets, catcher.Next)
	}
	for _, target := range targets {
		if _, taken := states[target]; target != "" && !taken {
			states[target] = placeholder
		}
	}
	wrapped, _ := json.Marshal(map[string]any{"StartAt": name, "States": states, "QueryLanguage": state.QueryLanguage})
	def, err := parseDefinition(string(wrapped))
	if err != nil {
		return nil, "", errTestStateInvalidDefinition(err)
	}
	return def, name, nil
}

func (h *Handler) testStateTyped(ctx context.Context, req *testStateRequest) (*testStateResponse, *protocol.AWSError) {
	if strings.TrimSpace(req.Definition) == "" {
		return nil, errValidation("1 validation error detected: Value null at 'definition' failed to satisfy constraint: Member must not be null")
	}
	switch req.InspectionLevel {
	case "", "INFO", "DEBUG", "TRACE":
	default:
		return nil, errValidation(fmt.Sprintf("1 validation error detected: Value '%s' at 'inspectionLevel' failed to satisfy constraint: Member must satisfy enum value set: [INFO, DEBUG, TRACE]", req.InspectionLevel))
	}
	def, name, aerr := parseTestStateDefinition(req.Definition, req.StateName)
	if aerr != nil {
		return nil, aerr
	}
	input, err := decodeExecutionInput(req.Input)
	if err != nil {
		return nil, &protocol.AWSError{Code: "InvalidExecutionInput", Message: err.Error(), HTTPStatus: http.StatusBadRequest}
	}

	region := middleware.RegionFromContext(ctx, h.cfg.Region)
	sm := &StateMachine{
		Name:    "TestState",
		ARN:     protocol.ARN(region, h.cfg.AccountID, "states", "stateMachine:TestState"),
		RoleArn: req.RoleArn,
		Type:    "STANDARD",
	}
	exec := &Execution{
		ExecutionArn:    protocol.ARN(region, h.cfg.AccountID, "states", "execution:TestState:test"),
		StateMachineArn: sm.ARN,
		Name:            "test",
		Input:           req.Input,
		StartDate:       h.clk.Now(),
	}
	runCtx, cancel := context.WithTimeout(ctx, h.executionTimeout(def))
	defer cancel()
	run := &executionRun{hist: newHistoryRecorder(maxHistoryEvents), cancel: cancel}
	in := h.newInterpreter(sm, exec, region, 0, run)
	in.queryLanguage = def.QueryLanguage
	if req.Variables != "" {
		in.vars = restoreVarScope(req.Variables)
	}
	if req.Context != "" {
		var overlay map[string]any
		if err := json.Unmarshal([]byte(req.Context), &overlay); err != nil {
			return nil, errValidation("context is not a valid JSON object")
		}
		for key, value := range overlay {
			in.baseCtx[key] = value
		}
	}
	probe := &testStateRun{mock: req.Mock}
	if req.InspectionLevel == "DEBUG" || req.InspectionLevel == "TRACE" {
		probe.inspect = &inspectionData{}
	}
	in.testState = probe

	output, next, done, serr := in.runState(runCtx, name, def.States[name], input)
	resp := &testStateResponse{InspectionData: probe.inspect}
	if probe.inspect != nil {
		probe.inspect.Variables = in.vars.snapshot()
	}
	switch {
	case serr != nil:
		resp.Error, resp.Cause = serr.name, serr.cause
		resp.Status = testStateFailed
		if probe.status == testStateRetriable {
			resp.Status = testStateRetriable
		}
	default:
		encoded, _ := encodeJSON(output)
		resp.Output = encoded
		resp.Status = testStateSucceeded
		if probe.status == testStateCaughtError {
			resp.Status = testStateCaughtError
		}
		if !done {
			resp.NextState = next
		}
	}
	return resp, nil
}

// inspect records one stage of the tested state's data flow.
func (f *flow) inspect(set func(*inspectionData, string), value any) {
	probe := f.in.testState
	if probe == nil || probe.inspect == nil || !f.in.topLevel {
		return
	}
	encoded, err := encodeJSON(value)
	if err != nil {
		return
	}
	set(probe.inspect, encoded)
}

// taskMock returns the mock TestState supplied for the state under test.
func (in *interpreter) taskMock() *testStateMock {
	if in.testState == nil || !in.topLevel {
		return nil
	}
	return in.testState.mock
}

// mockedResult is the Task result (or failure) a TestState mock stands in for.
func mockedResult(mock *testStateMock) (any, *stateError) {
	if mock.ErrorOutput != nil {
		return nil, &stateError{name: mock.ErrorOutput.Error, cause: mock.ErrorOutput.Cause}
	}
	var result any
	if err := json.Unmarshal([]byte(mock.Result), &result); err != nil {
		return nil, newStateError(errRuntime, "the mock result is not valid JSON: %v", err)
	}
	return result, nil
}
