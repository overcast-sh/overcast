// Package stepfunctions_test — TestState and ValidateStateMachineDefinition's
// neighbour in the "try a state without deploying it" workflow.
//
// Run: go test ./tests/integration/stepfunctions/...
package stepfunctions_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/overcast-sh/overcast/tests/helpers"
)

func TestTestState_passStateWithInspection(t *testing.T) {
	// Given: a lone Pass state with the full JSONPath pipeline
	srv := helpers.NewTestServer(t)
	def := `{"Type":"Pass","InputPath":"$.order","Parameters":{"id.$":"$.id"},"ResultPath":"$.picked","Next":"Ship"}`

	// When: we test it at DEBUG
	out := sfnOK(t, srv, "TestState", map[string]any{
		"definition": def, "input": `{"order":{"id":7,"qty":2}}`, "inspectionLevel": "DEBUG",
	})

	// Then: its output, next state and every stage of the data flow are reported
	if out["status"] != "SUCCEEDED" || out["nextState"] != "Ship" || out["output"] != `{"order":{"id":7,"qty":2},"picked":{"id":7}}` {
		t.Fatalf("TestState = %v", out)
	}
	data, _ := out["inspectionData"].(map[string]any)
	if data["afterInputPath"] != `{"id":7,"qty":2}` || data["afterParameters"] != `{"id":7}` || data["afterResultPath"] != `{"order":{"id":7,"qty":2},"picked":{"id":7}}` {
		t.Errorf("inspectionData = %v", data)
	}
}

func TestTestState_taskWithMockedResult(t *testing.T) {
	// Given: a Task whose integration result is mocked
	srv := helpers.NewTestServer(t)
	def := `{"Type":"Task","Resource":"arn:aws:states:::lambda:invoke","Parameters":{"FunctionName":"f"},"ResultSelector":{"v.$":"$.Payload.v"},"End":true}`

	// When: we test it
	out := sfnOK(t, srv, "TestState", map[string]any{
		"definition": def, "input": `{}`, "mock": map[string]any{"result": `{"Payload":{"v":42}}`},
	})

	// Then: the mock stood in for the call, and the state ends
	if out["status"] != "SUCCEEDED" || out["output"] != `{"v":42}` || out["nextState"] != nil {
		t.Fatalf("TestState = %v", out)
	}
}

func TestTestState_retriableAndCaughtErrors(t *testing.T) {
	srv := helpers.NewTestServer(t)
	mockFailure := map[string]any{"errorOutput": map[string]any{"error": "Custom.Busy", "cause": "try later"}}

	// Given: a failing Task with a matching retrier
	retrying := `{"Type":"Task","Resource":"arn:aws:states:::lambda:invoke","Parameters":{"FunctionName":"f"},
	  "Retry":[{"ErrorEquals":["Custom.Busy"]}],"End":true}`
	// When: we test it — Then: TestState reports RETRIABLE rather than retrying
	out := sfnOK(t, srv, "TestState", map[string]any{"definition": retrying, "input": `{}`, "mock": mockFailure})
	if out["status"] != "RETRIABLE" || out["error"] != "Custom.Busy" {
		t.Errorf("retrier case = %v", out)
	}

	// Given: the same failure with a catcher
	catching := `{"Type":"Task","Resource":"arn:aws:states:::lambda:invoke","Parameters":{"FunctionName":"f"},
	  "Catch":[{"ErrorEquals":["Custom.Busy"],"ResultPath":"$.err","Next":"Recover"}],"End":true}`
	// When: we test it — Then: CAUGHT_ERROR, routed to the catcher's Next
	out = sfnOK(t, srv, "TestState", map[string]any{"definition": catching, "input": `{}`, "mock": mockFailure})
	if out["status"] != "CAUGHT_ERROR" || out["nextState"] != "Recover" || !strings.Contains(out["output"].(string), "Custom.Busy") {
		t.Errorf("catcher case = %v", out)
	}
}

func TestTestState_jsonataStateInAWholeMachine(t *testing.T) {
	// Given: a whole JSONata machine, a variable supplied by the caller, and a stateName
	srv := helpers.NewTestServer(t)
	def := `{"QueryLanguage":"JSONata","StartAt":"A","States":{
	  "A":{"Type":"Pass","Next":"B"},
	  "B":{"Type":"Pass","Output":"{% $states.input.n * $factor %}","End":true}}}`

	// When: we test state B
	out := sfnOK(t, srv, "TestState", map[string]any{
		"definition": def, "stateName": "B", "input": `{"n":4}`, "variables": `{"factor":3}`,
	})

	// Then: the variable was in scope
	if out["status"] != "SUCCEEDED" || out["output"] != `12` {
		t.Fatalf("TestState = %v", out)
	}
}

func TestTestState_invalidDefinitionIsRejected(t *testing.T) {
	// Given: a state with no Type
	srv := helpers.NewTestServer(t)

	// When: we test it
	resp := sfnCall(t, srv, "TestState", map[string]any{"definition": `{"End":true}`})
	defer resp.Body.Close()

	// Then: InvalidDefinition
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	if body := helpers.ReadBody(t, resp); !strings.Contains(body, "InvalidDefinition") {
		t.Errorf("body = %s", body)
	}
}
