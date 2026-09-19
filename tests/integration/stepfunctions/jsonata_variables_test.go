// Package stepfunctions_test — the JSONata query language and workflow
// variables (Assign) in both query languages.
//
// Run: go test ./tests/integration/stepfunctions/...
package stepfunctions_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/overcast-sh/overcast/tests/helpers"
)

func TestStartExecution_perStateJSONataIsEvaluated(t *testing.T) {
	cases := []struct {
		name, definition, input, want string
	}{
		{
			name:       "state only",
			definition: `{"StartAt":"P","States":{"P":{"Type":"Pass","QueryLanguage":"JSONata","Output":"{% 1+1 %}","End":true}}}`,
			input:      `{}`,
			want:       `2`,
		},
		{
			name: "mixed with a JSONPath machine",
			definition: `{"QueryLanguage":"JSONPath","StartAt":"First","States":{
			  "First": {"Type":"Pass","Result":{"n":1},"Next":"P"},
			  "P":     {"Type":"Pass","QueryLanguage":"JSONata","Output":"{% $states.input.n + 1 %}","End":true}
			}}`,
			input: `{}`,
			want:  `2`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Given: a definition using a JSONata state
			srv := helpers.NewTestServer(t)

			// When: it runs
			got, _ := runToEnd(t, srv, "jsonata-sm", tc.definition, tc.input)

			// Then: the expression was evaluated
			if got.Status != "SUCCEEDED" || got.Output != tc.want {
				t.Fatalf("status=%q output=%q (%s: %s), want %s", got.Status, got.Output, got.Error, got.Cause, tc.want)
			}
		})
	}
}

func TestCreateStateMachine_queryLanguageFieldsAreChecked(t *testing.T) {
	cases := []struct{ name, definition string }{
		{"Output in a JSONPath state", `{"StartAt":"P","States":{"P":{"Type":"Pass","Output":"{% 1+1 %}","End":true}}}`},
		{"InputPath in a JSONata state", `{"QueryLanguage":"JSONata","StartAt":"P","States":{"P":{"Type":"Pass","InputPath":"$.a","End":true}}}`},
		{"JSONPath state in a JSONata machine", `{"QueryLanguage":"JSONata","StartAt":"P","States":{"P":{"Type":"Pass","QueryLanguage":"JSONPath","End":true}}}`},
		{"JSONata Choice rule without a Condition", `{"QueryLanguage":"JSONata","StartAt":"C","States":{"C":{"Type":"Choice","Choices":[{"Variable":"$.a","IsPresent":true,"Next":"D"}]},"D":{"Type":"Succeed"}}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Given: a definition mixing up the two query languages
			srv := helpers.NewTestServer(t)

			// When: we create it
			resp := sfnCall(t, srv, "CreateStateMachine", map[string]any{
				"name": "ql-fields", "definition": tc.definition, "roleArn": "arn:aws:iam::000000000000:role/r",
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

func TestStartExecution_jsonPathAssignAndVariableReads(t *testing.T) {
	// Given: a state that assigns a variable and a later one that reads it
	srv := helpers.NewTestServer(t)
	def := `{"StartAt":"Keep","States":{
	  "Keep": {"Type":"Pass","Result":{"n":5},"Assign":{"total.$":"$.n","label":"fixed"},"Next":"Use"},
	  "Use":  {"Type":"Pass","Parameters":{"t.$":"$total","l.$":"$label","f.$":"States.Format('{}-{}', $label, $total)"},"End":true}
	}}`

	// When: it runs
	got, execARN := runToEnd(t, srv, "vars-jsonpath", def, `{}`)

	// Then: the variable reads see the assigned values
	if got.Status != "SUCCEEDED" || got.Output != `{"f":"fixed-5","l":"fixed","t":5}` {
		t.Fatalf("status=%q output=%q (%s: %s)", got.Status, got.Output, got.Error, got.Cause)
	}
	// And: the exit event reports what was assigned
	for _, e := range findEvents(rawHistory(t, srv, execARN), "PassStateExited") {
		if e.stateName() != "Keep" {
			continue
		}
		assigned, _ := e.detail("stateExitedEventDetails")["assignedVariables"].(map[string]any)
		if assigned["total"] != "5" || assigned["label"] != `"fixed"` {
			t.Errorf("assignedVariables = %v", assigned)
		}
	}
}

func TestStartExecution_catchAssignRecordsTheError(t *testing.T) {
	// Given: a failing Task whose Catch assigns from the error output
	srv := helpers.NewTestServer(t)
	def := `{"StartAt":"T","States":{
	  "T": {"Type":"Task","Resource":"arn:aws:states:::lambda:invoke","Parameters":{"FunctionName":"does-not-exist"},
	        "Catch":[{"ErrorEquals":["States.TaskFailed"],"Assign":{"failedWith.$":"$.Error"},"ResultPath":null,"Next":"Handled"}],
	        "End":true},
	  "Handled": {"Type":"Pass","Parameters":{"e.$":"$failedWith"},"End":true}
	}}`

	// When: it runs
	got, _ := runToEnd(t, srv, "vars-catch", def, `{}`)

	// Then: the catcher's Assign saw the error
	if got.Status != "SUCCEEDED" || !strings.Contains(got.Output, `"e":"Lambda.`) {
		t.Fatalf("status=%q output=%q (%s: %s)", got.Status, got.Output, got.Error, got.Cause)
	}
}

func TestStartExecution_branchVariablesStayInTheBranch(t *testing.T) {
	// Given: a Parallel branch that reads an outer variable and assigns its own
	srv := helpers.NewTestServer(t)
	def := `{"StartAt":"Init","States":{
	  "Init": {"Type":"Pass","Assign":{"outer":1},"Next":"P"},
	  "P": {"Type":"Parallel","Branches":[{"StartAt":"B","States":{
	          "B":{"Type":"Pass","Parameters":{"seen.$":"$outer"},"Assign":{"inner":2},"End":true}}}],
	        "Next":"After"},
	  "After": {"Type":"Pass","Parameters":{"inner.$":"$inner"},"End":true}
	}}`

	// When: it runs
	got, _ := runToEnd(t, srv, "vars-scope", def, `{}`)

	// Then: the branch read the outer variable, and its own is gone afterwards
	if got.Status != "FAILED" || !strings.Contains(got.Cause, "inner") {
		t.Fatalf("status=%q output=%q error=%q cause=%q, want a failure naming $inner", got.Status, got.Output, got.Error, got.Cause)
	}
}

func TestStartExecution_jsonataStateMachine(t *testing.T) {
	// Given: a JSONata machine using variables, Items, Condition and Output
	srv := helpers.NewTestServer(t)
	def := `{"QueryLanguage":"JSONata","StartAt":"Init","States":{
	  "Init":  {"Type":"Pass","Assign":{"factor":"{% $states.input.n %}"},"Output":{"items":[1,2,3]},"Next":"Each"},
	  "Each":  {"Type":"Map","Items":"{% $states.input.items %}",
	            "ItemProcessor":{"StartAt":"Scale","States":{"Scale":{"Type":"Pass","Output":"{% $states.input * $factor %}","End":true}}},
	            "Output":"{% $sum($states.result) %}","Next":"Check"},
	  "Check": {"Type":"Choice","Choices":[{"Condition":"{% $states.input > 10 %}","Next":"Big"}],"Default":"Small"},
	  "Big":   {"Type":"Succeed","Output":"{% 'big:' & $string($states.input) %}"},
	  "Small": {"Type":"Succeed","Output":"small"}
	}}`

	// When: it runs with n = 2
	got, _ := runToEnd(t, srv, "jsonata-machine", def, `{"n":2}`)

	// Then: (1+2+3) × 2 = 12 took the Big branch
	if got.Status != "SUCCEEDED" || got.Output != `"big:12"` {
		t.Fatalf("status=%q output=%q (%s: %s)", got.Status, got.Output, got.Error, got.Cause)
	}
}

func TestStartExecution_jsonataCatchAndFailExpressions(t *testing.T) {
	// Given: a Parallel whose branch fails with a computed error, caught with Output
	srv := helpers.NewTestServer(t)
	def := `{"QueryLanguage":"JSONata","StartAt":"P","States":{
	  "P": {"Type":"Parallel","Arguments":{"code":"{% $states.input.code %}"},
	        "Branches":[{"StartAt":"F","States":{"F":{"Type":"Fail","Error":"{% 'Order.' & $states.input.code %}","Cause":"computed"}}}],
	        "Catch":[{"ErrorEquals":["Order.E42"],"Output":{"caught":"{% $states.errorOutput.Error %}","was":"{% $states.input.code %}"},"Next":"Done"}],
	        "End":true},
	  "Done": {"Type":"Succeed"}
	}}`

	// When: it runs
	got, _ := runToEnd(t, srv, "jsonata-catch", def, `{"code":"E42"}`)

	// Then: the Fail's Error expression and the catcher's Output both evaluated
	if got.Status != "SUCCEEDED" || got.Output != `{"caught":"Order.E42","was":"E42"}` {
		t.Fatalf("status=%q output=%q (%s: %s)", got.Status, got.Output, got.Error, got.Cause)
	}
}

func TestStartExecution_jsonataEvaluationErrorIsCatchable(t *testing.T) {
	// Given: an Output expression that fails, and a Catch for it
	srv := helpers.NewTestServer(t)
	def := `{"QueryLanguage":"JSONata","StartAt":"Bad","States":{
	  "Bad":  {"Type":"Task","Resource":"arn:aws:states:::aws-sdk:sfn:listStateMachines","Output":"{% $undefinedFunction() %}",
	           "Catch":[{"ErrorEquals":["States.QueryEvaluationError"],"Output":"recovered","Next":"Done"}],"End":true},
	  "Done": {"Type":"Succeed"}
	}}`

	// When: it runs
	got, execARN := runToEnd(t, srv, "jsonata-error", def, `{}`)

	// Then: States.QueryEvaluationError was caught and recorded as EvaluationFailed
	if got.Status != "SUCCEEDED" || got.Output != `"recovered"` {
		t.Fatalf("status=%q output=%q (%s: %s)", got.Status, got.Output, got.Error, got.Cause)
	}
	failed := findEvents(rawHistory(t, srv, execARN), "EvaluationFailed")
	if len(failed) != 1 || failed[0].detail("evaluationFailedEventDetails")["state"] != "Bad" {
		t.Errorf("EvaluationFailed = %v", failed)
	}
}

func TestStartExecution_jsonataAWSFunctions(t *testing.T) {
	// Given: the functions Step Functions adds to JSONata
	srv := helpers.NewTestServer(t)
	def := `{"QueryLanguage":"JSONata","StartAt":"F","States":{"F":{"Type":"Pass","Output":{
	  "partition":"{% $partition([1,2,3,4,5], 2) %}",
	  "range":"{% $range(0, 10, 5) %}",
	  "hash":"{% $hash('abc', 'SHA-256') %}",
	  "parsed":"{% $parse('{\"a\":1}').a %}",
	  "uuidLength":"{% $length($uuid()) %}",
	  "seeded":"{% $random(7) = $random(7) %}"
	},"End":true}}}`

	// When: it runs
	got, _ := runToEnd(t, srv, "jsonata-functions", def, `{}`)

	// Then: each behaves as AWS documents
	if got.Status != "SUCCEEDED" {
		t.Fatalf("status=%q (%s: %s)", got.Status, got.Error, got.Cause)
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(got.Output), &out); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"partition":  `[[1,2],[3,4],[5]]`,
		"range":      `[0,5,10]`,
		"hash":       `"ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"`,
		"parsed":     `1`,
		"uuidLength": `36`,
		"seeded":     `true`,
	}
	for key, expected := range want {
		encoded, _ := json.Marshal(out[key])
		if string(encoded) != expected {
			t.Errorf("%s = %s, want %s", key, encoded, expected)
		}
	}
}

func TestStartExecution_jsonataTaskArgumentsAndResult(t *testing.T) {
	// Given: a JSONata Task that builds its Arguments and shapes $states.result
	srv := helpers.NewTestServer(t)
	createTable(t, srv, "jsonata-table")
	resp := awsJSONCall(t, srv, "DynamoDB_20120810.PutItem", map[string]any{
		"TableName": "jsonata-table", "Item": map[string]any{"id": map[string]string{"S": "k1"}, "v": map[string]string{"S": "hello"}},
	})
	resp.Body.Close()
	def := `{"QueryLanguage":"JSONata","StartAt":"Get","States":{"Get":{"Type":"Task",
	  "Resource":"arn:aws:states:::dynamodb:getItem",
	  "Arguments":{"TableName":"jsonata-table","Key":{"id":{"S":"{% $states.input.key %}"}}},
	  "Output":"{% $states.result.Item.v.S %}","TimeoutSeconds":"{% 30 %}","End":true}}}`

	// When: it runs
	got, _ := runToEnd(t, srv, "jsonata-task", def, `{"key":"k1"}`)

	// Then: the item's value is the output
	if got.Status != "SUCCEEDED" || got.Output != `"hello"` {
		t.Fatalf("status=%q output=%q (%s: %s)", got.Status, got.Output, got.Error, got.Cause)
	}
}
