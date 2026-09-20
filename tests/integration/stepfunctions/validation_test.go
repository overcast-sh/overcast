// Step Functions refuses malformed input rather than treating it as a lookup
// that found nothing: a name outside AWS's charset is InvalidName, and an ARN
// that is not a state machine ARN is InvalidArn. See #2003.
package stepfunctions_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/overcast-sh/overcast/tests/helpers"
)

const (
	testRoleArn = "arn:aws:iam::000000000000:role/sfn"
	// A state machine ARN of the right shape naming something that was never
	// created — the case that stays StateMachineDoesNotExist.
	absentSMArn = "arn:aws:states:us-east-1:000000000000:stateMachine:never-created"
)

// ─── CreateStateMachine: name ─────────────────────────────────────────────────

func TestCreateStateMachine_nameOutsideAWSCharset(t *testing.T) {
	// Given: names AWS documents as invalid for a state machine
	cases := []struct {
		label string
		name  string
	}{
		{"space", "my state machine"},
		{"tab", "my\tmachine"},
		{"brace", "sm{1}"},
		{"bracket", "sm[1]"},
		{"wildcard star", "sm*"},
		{"wildcard question", "sm?"},
		{"quote", `sm"1`},
		{"hash", "sm#1"},
		{"percent", "sm%1"},
		{"backslash", `sm\1`},
		{"caret", "sm^1"},
		{"pipe", "sm|1"},
		{"tilde", "sm~1"},
		{"backtick", "sm`1"},
		{"dollar", "sm$1"},
		{"ampersand", "sm&1"},
		{"comma", "sm,1"},
		{"semicolon", "sm;1"},
		{"colon", "sm:1"},
		{"slash", "sm/1"},
		{"control character", "sm\u0001"},
		{"81 characters", strings.Repeat("a", 81)},
		{"empty", ""},
	}

	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			// Given: a server
			srv := helpers.NewTestServer(t)

			// When: CreateStateMachine is called with that name
			resp := sfnCall(t, srv, "CreateStateMachine", map[string]any{
				"name":       tc.name,
				"definition": testDefinition,
				"roleArn":    testRoleArn,
			})
			defer resp.Body.Close()

			// Then: 400 InvalidName
			helpers.AssertStatus(t, resp, http.StatusBadRequest)
			helpers.AssertJSONError(t, resp, "InvalidName")
		})
	}
}

func TestCreateStateMachine_nameWithinAWSCharset(t *testing.T) {
	// Given: names AWS accepts — the charset rule must not over-reject
	for _, name := range []string{"sm", "my-state_machine.v2", "SM+1", "état", strings.Repeat("a", 80)} {
		t.Run(name, func(t *testing.T) {
			// Given: a server
			srv := helpers.NewTestServer(t)

			// When: CreateStateMachine is called with that name
			resp := sfnCall(t, srv, "CreateStateMachine", map[string]any{
				"name":       name,
				"definition": testDefinition,
				"roleArn":    testRoleArn,
			})
			defer resp.Body.Close()

			// Then: it is created
			helpers.AssertStatus(t, resp, http.StatusOK)
		})
	}
}

// ─── CreateStateMachine: roleArn ──────────────────────────────────────────────

func TestCreateStateMachine_roleArnNotAnIAMRole(t *testing.T) {
	// Given: roleArn values that are not IAM role ARNs
	cases := map[string]string{
		"bare word":       "not-an-arn",
		"wrong service":   "arn:aws:s3:::my-bucket",
		"wrong resource":  "arn:aws:iam::000000000000:user/bob",
		"truncated":       "arn:aws:iam::000000000000",
		"role with no id": "arn:aws:iam::000000000000:role/",
	}

	for label, roleArn := range cases {
		t.Run(label, func(t *testing.T) {
			// Given: a server
			srv := helpers.NewTestServer(t)

			// When: CreateStateMachine is called with that roleArn
			resp := sfnCall(t, srv, "CreateStateMachine", map[string]any{
				"name":       "sm",
				"definition": testDefinition,
				"roleArn":    roleArn,
			})
			defer resp.Body.Close()

			// Then: 400 InvalidArn
			helpers.AssertStatus(t, resp, http.StatusBadRequest)
			helpers.AssertJSONError(t, resp, "InvalidArn")
		})
	}
}

func TestCreateStateMachine_roleArnIsAnIAMRole(t *testing.T) {
	// Given: IAM role ARNs AWS accepts, including a path and another account
	for label, roleArn := range map[string]string{
		"plain":         "arn:aws:iam::000000000000:role/sfn",
		"path":          "arn:aws:iam::123456789012:role/service-role/nested/MyRole",
		"gov partition": "arn:aws-us-gov:iam::123456789012:role/MyRole",
	} {
		t.Run(label, func(t *testing.T) {
			// Given: a server
			srv := helpers.NewTestServer(t)

			// When: CreateStateMachine is called with that roleArn
			resp := sfnCall(t, srv, "CreateStateMachine", map[string]any{
				"name":       "sm",
				"definition": testDefinition,
				"roleArn":    roleArn,
			})
			defer resp.Body.Close()

			// Then: it is created
			helpers.AssertStatus(t, resp, http.StatusOK)
		})
	}
}

// ─── Malformed stateMachineArn ────────────────────────────────────────────────

// malformedSMArns are strings that are not state machine ARNs at all. A
// well-formed ARN naming a state machine that does not exist is a different
// answer and is covered separately below.
var malformedSMArns = map[string]string{
	"bare name":     "my-state-machine",
	"no prefix":     "aws:states:us-east-1:000000000000:stateMachine:sm",
	"wrong service": "arn:aws:lambda:us-east-1:000000000000:function:sm",
	"wrong type":    "arn:aws:states:us-east-1:000000000000:activity:sm",
	"no name":       "arn:aws:states:us-east-1:000000000000:stateMachine:",
	"truncated":     "arn:aws:states:us-east-1:000000000000",
}

func TestStateMachineOperations_malformedArn(t *testing.T) {
	// Given: one operation per caller-supplied stateMachineArn
	ops := map[string]func(arn string) map[string]any{
		"StartExecution":       func(arn string) map[string]any { return map[string]any{"stateMachineArn": arn} },
		"StartSyncExecution":   func(arn string) map[string]any { return map[string]any{"stateMachineArn": arn} },
		"DescribeStateMachine": func(arn string) map[string]any { return map[string]any{"stateMachineArn": arn} },
		"DeleteStateMachine":   func(arn string) map[string]any { return map[string]any{"stateMachineArn": arn} },
		"UpdateStateMachine": func(arn string) map[string]any {
			return map[string]any{"stateMachineArn": arn, "definition": testDefinition}
		},
		"ListExecutions": func(arn string) map[string]any { return map[string]any{"stateMachineArn": arn} },
	}

	// And: a server holding one real state machine, so a refusal cannot be
	// mistaken for an empty emulator. Every case below is refused before it
	// reaches any state, so one server serves them all.
	srv := helpers.NewTestServer(t)
	createTestStateMachine(t, srv, "real-sm")

	for op, body := range ops {
		for label, arn := range malformedSMArns {
			t.Run(op+"/"+label, func(t *testing.T) {
				// When: the operation is called with a malformed ARN
				resp := sfnCall(t, srv, op, body(arn))
				defer resp.Body.Close()

				// Then: 400 InvalidArn — not "that state machine does not exist"
				helpers.AssertStatus(t, resp, http.StatusBadRequest)
				helpers.AssertJSONError(t, resp, "InvalidArn")
			})
		}
	}

	// And: the real state machine is untouched by the refused calls
	resp := sfnCall(t, srv, "DescribeStateMachine", map[string]any{
		"stateMachineArn": "arn:aws:states:us-east-1:000000000000:stateMachine:real-sm",
	})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
}

func TestStateMachineOperations_wellFormedArnForAbsentStateMachine(t *testing.T) {
	// Given: a well-formed state machine ARN naming nothing that exists
	for _, op := range []string{"StartExecution", "DescribeStateMachine", "ListExecutions"} {
		t.Run(op, func(t *testing.T) {
			srv := helpers.NewTestServer(t)

			// When: the operation is called with it
			resp := sfnCall(t, srv, op, map[string]any{"stateMachineArn": absentSMArn})
			defer resp.Body.Close()

			// Then: StateMachineDoesNotExist, which is still the right answer
			helpers.AssertStatus(t, resp, http.StatusBadRequest)
			helpers.AssertJSONError(t, resp, "StateMachineDoesNotExist")
		})
	}
}

// ─── Test helpers ─────────────────────────────────────────────────────────────

func createTestStateMachine(t *testing.T, srv *helpers.TestServer, name string) {
	t.Helper()
	resp := sfnCall(t, srv, "CreateStateMachine", map[string]any{
		"name":       name,
		"definition": testDefinition,
		"roleArn":    testRoleArn,
	})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
}
