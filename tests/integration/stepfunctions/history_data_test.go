// Package stepfunctions_test — GetExecutionHistory's includeExecutionData.
//
// Run: go test ./tests/integration/stepfunctions/...
package stepfunctions_test

import (
	"net/http"
	"testing"
	"time"

	"github.com/overcast-sh/overcast/tests/helpers"
)

func TestGetExecutionHistory_withoutExecutionDataLeavesTheHistoryIntact(t *testing.T) {
	// Given: a running execution parked in a Wait, its input recorded
	srv := helpers.NewTestServer(t)
	smARN := createSM(t, srv, "hist-data", `{"StartAt":"W","States":{"W":{"Type":"Wait","Seconds":60,"End":true}}}`)
	execARN := startExec(t, srv, smARN, `{"secret":"s3cr3t"}`)
	deadline := time.Now().Add(5 * time.Second)
	for len(findEvents(rawHistory(t, srv, execARN), "WaitStateEntered")) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("never entered the Wait")
		}
		time.Sleep(5 * time.Millisecond)
	}

	// When: the history is read once without execution data
	resp := sfnCall(t, srv, "GetExecutionHistory", map[string]any{"executionArn": execARN, "includeExecutionData": false})
	helpers.AssertStatus(t, resp, http.StatusOK)
	resp.Body.Close()

	// Then: a later full read still carries the payloads
	entered := findEvents(rawHistory(t, srv, execARN), "WaitStateEntered")[0]
	if entered.detail("stateEnteredEventDetails")["input"] != `{"secret":"s3cr3t"}` {
		t.Errorf("WaitStateEntered input = %v — stripping execution data erased it from the history", entered.detail("stateEnteredEventDetails"))
	}
	sfnOK(t, srv, "StopExecution", map[string]any{"executionArn": execARN})
}
