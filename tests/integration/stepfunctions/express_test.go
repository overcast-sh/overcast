// Package stepfunctions_test — what an EXPRESS state machine may contain.
//
// Run: go test ./tests/integration/stepfunctions/...
package stepfunctions_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/overcast-sh/overcast/tests/helpers"
)

func TestCreateStateMachine_expressRejectsCallbacksAndSync(t *testing.T) {
	cases := []struct{ name, resource string }{
		{"waitForTaskToken", "arn:aws:states:::sqs:sendMessage.waitForTaskToken"},
		{"sync", "arn:aws:states:::states:startExecution.sync:2"},
		{"activity", "arn:aws:states:us-east-1:000000000000:activity:a"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Given: an EXPRESS definition using a pattern Express cannot run
			srv := helpers.NewTestServer(t)
			def := `{"StartAt":"T","States":{"T":{"Type":"Task","Resource":"` + tc.resource + `","End":true}}}`

			// When: we create it
			resp := sfnCall(t, srv, "CreateStateMachine", map[string]any{
				"name": "express-" + tc.name, "definition": def, "type": "EXPRESS", "roleArn": "arn:aws:iam::000000000000:role/r",
			})
			defer resp.Body.Close()

			// Then: InvalidDefinition, as on AWS
			helpers.AssertStatus(t, resp, http.StatusBadRequest)
			if body := helpers.ReadBody(t, resp); !strings.Contains(body, "InvalidDefinition") {
				t.Errorf("body = %s", body)
			}
		})
	}
}
