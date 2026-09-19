package stepfunctions

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// ─── parseDefinition ──────────────────────────────────────────────────────────

func TestParseDefinition_valid(t *testing.T) {
	// Given: a definition using every state type
	definition := `{
	  "StartAt": "Choose",
	  "States": {
	    "Choose": {"Type": "Choice", "Choices": [{"Variable": "$.a", "StringEquals": "x", "Next": "Wait"}], "Default": "Done"},
	    "Wait":   {"Type": "Wait", "Seconds": 0, "Next": "Work"},
	    "Work":   {"Type": "Task", "Resource": "arn:aws:states:::sqs:sendMessage", "Next": "Fan"},
	    "Fan":    {"Type": "Parallel", "Branches": [{"StartAt": "P", "States": {"P": {"Type": "Pass", "End": true}}}], "Next": "Each"},
	    "Each":   {"Type": "Map", "ItemProcessor": {"StartAt": "I", "States": {"I": {"Type": "Pass", "End": true}}}, "Next": "Done"},
	    "Done":   {"Type": "Succeed"},
	    "Nope":   {"Type": "Fail", "Error": "E"}
	  }
	}`

	// When: we parse it
	branch, err := parseDefinition(definition)

	// Then: it is accepted
	if err != nil {
		t.Fatalf("parseDefinition: %v", err)
	}
	if branch.StartAt != "Choose" || len(branch.States) != 7 {
		t.Fatalf("parsed StartAt=%q states=%d", branch.StartAt, len(branch.States))
	}
}

func TestParseDefinition_rejects(t *testing.T) {
	cases := []struct {
		name       string
		definition string
		wantSubstr string
	}{
		{"empty", "", "empty"},
		{"not JSON", "{", "not valid JSON"},
		{"no StartAt", `{"States":{"P":{"Type":"Pass","End":true}}}`, "StartAt is required"},
		{"no States", `{"StartAt":"P"}`, "States is required"},
		{"dangling StartAt", `{"StartAt":"Q","States":{"P":{"Type":"Pass","End":true}}}`, "does not name a state"},
		{"unknown type", `{"StartAt":"P","States":{"P":{"Type":"Frobnicate","End":true}}}`, "unknown state Type"},
		{"no transition", `{"StartAt":"P","States":{"P":{"Type":"Pass"}}}`, "requires either Next or End"},
		{"both Next and End", `{"StartAt":"P","States":{"P":{"Type":"Pass","End":true,"Next":"P"}}}`, "declares both Next and End"},
		{"dangling Next", `{"StartAt":"P","States":{"P":{"Type":"Pass","Next":"Q"}}}`, "does not name a state"},
		{"Task without Resource", `{"StartAt":"T","States":{"T":{"Type":"Task","End":true}}}`, "Task requires Resource"},
		{"Choice without Choices", `{"StartAt":"C","States":{"C":{"Type":"Choice"}}}`, "non-empty Choices"},
		{"Choice rule without Next", `{"StartAt":"C","States":{"C":{"Type":"Choice","Choices":[{"Variable":"$.a","StringEquals":"x"}]}}}`, "requires Next"},
		{"unknown comparison", `{"StartAt":"C","States":{"C":{"Type":"Choice","Choices":[{"Variable":"$.a","StringSmells":"x","Next":"C"}]},"P":{"Type":"Pass","End":true}}}`, "unknown comparison operator"},
		{"Map without processor", `{"StartAt":"M","States":{"M":{"Type":"Map","End":true}}}`, "Map requires ItemProcessor"},
		{"Parallel without branches", `{"StartAt":"P","States":{"P":{"Type":"Parallel","End":true}}}`, "non-empty Branches"},
		{"Wait without a duration", `{"StartAt":"W","States":{"W":{"Type":"Wait","End":true}}}`, "Wait requires one of"},
		{"Succeed with Next", `{"StartAt":"S","States":{"S":{"Type":"Succeed","Next":"S"}}}`, "must not declare Next or End"},
		{"Catch without Next", `{"StartAt":"T","States":{"T":{"Type":"Task","Resource":"arn:aws:states:::sns:publish","End":true,"Catch":[{"ErrorEquals":["States.ALL"]}]}}}`, "Catch[0] requires Next"},
		{"nested branch invalid", `{"StartAt":"P","States":{"P":{"Type":"Parallel","End":true,"Branches":[{"StartAt":"X","States":{"Y":{"Type":"Pass","End":true}}}]}}}`, "does not name a state"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// When: we parse an invalid definition
			_, err := parseDefinition(tc.definition)

			// Then: it is rejected with a message naming the problem
			if err == nil {
				t.Fatalf("expected an error for %s", tc.definition)
			}
			if !strings.Contains(err.Error(), tc.wantSubstr) {
				t.Fatalf("error = %q, want it to contain %q", err.Error(), tc.wantSubstr)
			}
		})
	}
}

func TestParseDefinition_pathFieldsDistinguishAbsentFromNull(t *testing.T) {
	// Given: one state that omits ResultPath and one that sets it to null
	branch, err := parseDefinition(`{
	  "StartAt": "A",
	  "States": {
	    "A": {"Type": "Pass", "Next": "B"},
	    "B": {"Type": "Pass", "ResultPath": null, "Next": "C"},
	    "C": {"Type": "Pass", "ResultPath": "$.x", "End": true}
	  }
	}`)
	if err != nil {
		t.Fatalf("parseDefinition: %v", err)
	}

	// Then: absent, null and set are three distinguishable states
	if branch.States["A"].ResultPath.Set {
		t.Error("A: ResultPath should be absent")
	}
	if !branch.States["B"].ResultPath.Set || !branch.States["B"].ResultPath.Null {
		t.Error("B: ResultPath should be present and null")
	}
	if !branch.States["C"].ResultPath.Set || branch.States["C"].ResultPath.Null || branch.States["C"].ResultPath.Value != "$.x" {
		t.Error("C: ResultPath should be present with value $.x")
	}
}

// ─── Reference paths ──────────────────────────────────────────────────────────

func TestSelectPath(t *testing.T) {
	doc := decodeTestJSON(t, `{"a":{"b":[10,20]},"s":"hi","n":null}`)
	ctxObj := map[string]any{"Execution": map[string]any{"Name": "run-1"}}

	cases := []struct {
		name    string
		path    string
		want    string
		wantErr bool
	}{
		{"root", "$", `{"a":{"b":[10,20]},"n":null,"s":"hi"}`, false},
		{"field", "$.s", `"hi"`, false},
		{"nested", "$.a.b", `[10,20]`, false},
		{"index", "$.a.b[1]", `20`, false},
		{"bracket field", "$['a']['b'][0]", `10`, false},
		{"explicit null", "$.n", `null`, false},
		{"context object", "$$.Execution.Name", `"run-1"`, false},
		{"missing field", "$.nope", "", true},
		{"index out of range", "$.a.b[9]", "", true},
		{"wildcard", "$.a.b[*]", `[10,20]`, false},
		{"descendant", "$..b", `[[10,20]]`, false},
		{"filter", "$.a.b[?(@.x)]", `[]`, false},
		{"no root", "a.b", "", true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := selectPath(doc, ctxObj, tc.path)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected an error for %q", tc.path)
				}
				return
			}
			if err != nil {
				t.Fatalf("selectPath(%q): %v", tc.path, err)
			}
			encoded, _ := json.Marshal(got)
			if string(encoded) != tc.want {
				t.Fatalf("selectPath(%q) = %s, want %s", tc.path, encoded, tc.want)
			}
		})
	}
}

func TestInsertPath_createsIntermediateObjects(t *testing.T) {
	// Given: a document without the target branch
	doc := decodeTestJSON(t, `{"keep":1}`)

	// When: we insert at a nested path
	got, err := insertPath(doc, "$.a.b", "value")
	if err != nil {
		t.Fatalf("insertPath: %v", err)
	}

	// Then: the branch is created and the original is untouched
	encoded, _ := json.Marshal(got)
	if string(encoded) != `{"a":{"b":"value"},"keep":1}` {
		t.Fatalf("insertPath = %s", encoded)
	}
	original, _ := json.Marshal(doc)
	if string(original) != `{"keep":1}` {
		t.Fatalf("insertPath mutated its input: %s", original)
	}
}

// ─── Payload templates and intrinsics ─────────────────────────────────────────

func TestRenderPayloadTemplate(t *testing.T) {
	doc := decodeTestJSON(t, `{"name":"ada","count":2,"nested":{"v":true}}`)
	ctxObj := map[string]any{"Execution": map[string]any{"Name": "run-7"}}

	cases := []struct {
		name     string
		template string
		want     string
		wantErr  bool
	}{
		{"literal passthrough", `{"a":1,"b":"x"}`, `{"a":1,"b":"x"}`, false},
		{"path substitution", `{"who.$":"$.name"}`, `{"who":"ada"}`, false},
		{"context substitution", `{"run.$":"$$.Execution.Name"}`, `{"run":"run-7"}`, false},
		{"nested object", `{"o":{"v.$":"$.nested.v"}}`, `{"o":{"v":true}}`, false},
		{"array element", `{"l":[{"n.$":"$.count"}]}`, `{"l":[{"n":2}]}`, false},
		{"States.Format", `{"m.$":"States.Format('hi {}, {} times', $.name, $.count)"}`, `{"m":"hi ada, 2 times"}`, false},
		{"States.Array", `{"l.$":"States.Array($.name, $.count)"}`, `{"l":["ada",2]}`, false},
		{"States.JsonToString", `{"s.$":"States.JsonToString($.nested)"}`, `{"s":"{\"v\":true}"}`, false},
		{"States.StringToJson", `{"o.$":"States.StringToJson('{\"k\":1}')"}`, `{"o":{"k":1}}`, false},
		{"States.ArrayLength", `{"n.$":"States.ArrayLength(States.Array($.name))"}`, `{"n":1}`, false},
		{"States.Hash", `{"u.$":"States.Hash($.name, 'MD5')"}`, `{"u":"8c8d357b5e872bbacd45197626bd5759"}`, false},
		{"unknown intrinsic", `{"u.$":"States.NoSuchFunction($.name)"}`, "", true},
		{"missing path", `{"x.$":"$.nope"}`, "", true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := renderPayloadTemplate(json.RawMessage(tc.template), doc, ctxObj)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected an error for %s", tc.template)
				}
				return
			}
			if err != nil {
				t.Fatalf("renderPayloadTemplate(%s): %v", tc.template, err)
			}
			encoded, _ := json.Marshal(got)
			if string(encoded) != tc.want {
				t.Fatalf("renderPayloadTemplate(%s) = %s, want %s", tc.template, encoded, tc.want)
			}
		})
	}
}

// ─── Choice rules ─────────────────────────────────────────────────────────────

func TestEvaluateChoiceRule(t *testing.T) {
	doc := decodeTestJSON(t, `{
	  "s": "banana", "n": 7, "b": true, "nul": null,
	  "t": "2026-08-03T10:00:00Z", "other": 3
	}`)

	cases := []struct {
		name string
		rule string
		want bool
	}{
		{"StringEquals hit", `{"Variable":"$.s","StringEquals":"banana","Next":"X"}`, true},
		{"StringEquals miss", `{"Variable":"$.s","StringEquals":"apple","Next":"X"}`, false},
		{"StringMatches prefix", `{"Variable":"$.s","StringMatches":"ban*","Next":"X"}`, true},
		{"StringMatches escaped star", `{"Variable":"$.s","StringMatches":"ban\\*na","Next":"X"}`, false},
		{"StringLessThan", `{"Variable":"$.s","StringLessThan":"cherry","Next":"X"}`, true},
		{"NumericGreaterThan", `{"Variable":"$.n","NumericGreaterThan":5,"Next":"X"}`, true},
		{"NumericLessThanEquals", `{"Variable":"$.n","NumericLessThanEquals":7,"Next":"X"}`, true},
		{"NumericEqualsPath", `{"Variable":"$.n","NumericEqualsPath":"$.other","Next":"X"}`, false},
		{"BooleanEquals", `{"Variable":"$.b","BooleanEquals":true,"Next":"X"}`, true},
		{"TimestampLessThan", `{"Variable":"$.t","TimestampLessThan":"2027-01-01T00:00:00Z","Next":"X"}`, true},
		{"IsPresent hit", `{"Variable":"$.s","IsPresent":true,"Next":"X"}`, true},
		{"IsPresent miss", `{"Variable":"$.absent","IsPresent":true,"Next":"X"}`, false},
		{"IsNull", `{"Variable":"$.nul","IsNull":true,"Next":"X"}`, true},
		{"IsNumeric", `{"Variable":"$.n","IsNumeric":true,"Next":"X"}`, true},
		{"IsString on a number", `{"Variable":"$.n","IsString":true,"Next":"X"}`, false},
		{"IsTimestamp", `{"Variable":"$.t","IsTimestamp":true,"Next":"X"}`, true},
		{"And", `{"And":[{"Variable":"$.n","NumericGreaterThan":1},{"Variable":"$.s","StringEquals":"banana"}],"Next":"X"}`, true},
		{"And short-circuits false", `{"And":[{"Variable":"$.n","NumericGreaterThan":100},{"Variable":"$.s","StringEquals":"banana"}],"Next":"X"}`, false},
		{"Or", `{"Or":[{"Variable":"$.n","NumericGreaterThan":100},{"Variable":"$.b","BooleanEquals":true}],"Next":"X"}`, true},
		{"Not", `{"Not":{"Variable":"$.s","StringEquals":"apple"},"Next":"X"}`, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var rule aslChoiceRule
			if err := json.Unmarshal([]byte(tc.rule), &rule); err != nil {
				t.Fatalf("unmarshal rule: %v", err)
			}
			got, err := evaluateChoiceRule(&rule, doc, nil)
			if err != nil {
				t.Fatalf("evaluateChoiceRule: %v", err)
			}
			if got != tc.want {
				t.Fatalf("evaluateChoiceRule(%s) = %v, want %v", tc.rule, got, tc.want)
			}
		})
	}
}

// ─── Retry / Catch matching ───────────────────────────────────────────────────

func TestErrorMatches_statesALLNeverMatchesStatesRuntime(t *testing.T) {
	// Given: a wildcard ErrorEquals and an Overcast "not supported" failure
	wildcard := []string{errAll}

	// Then: States.ALL catches ordinary failures …
	if !errorMatches(wildcard, &stateError{name: errTaskFailed}) {
		t.Error("States.ALL should match States.TaskFailed")
	}
	if !errorMatches(wildcard, &stateError{name: "Lambda.ResourceNotFoundException"}) {
		t.Error("States.ALL should match a service error name")
	}
	// … but never States.Runtime, which is how an unsupported ASL feature stays
	// a loud, uncatchable execution failure.
	if errorMatches(wildcard, unsupportedError("the frobnicate integration")) {
		t.Error("States.ALL must not match States.Runtime")
	}
	if !errorMatches([]string{errRuntime}, &stateError{name: errRuntime}) {
		t.Error("an explicit States.Runtime ErrorEquals should still match")
	}
}

// predefinedErrorNamesForTest is the reserved ASL error-name vocabulary, as
// AWS's "Error names" table spells it. Two of these are wildcards in an
// ErrorEquals; the rest match only themselves.
var predefinedErrorNamesForTest = []string{
	"States.ALL",
	"States.BranchFailed",
	"States.DataLimitExceeded",
	"States.ExceedToleratedFailureThreshold",
	"States.HeartbeatTimeout",
	"States.IntrinsicFailure",
	"States.ItemReaderFailed",
	"States.NoChoiceMatched",
	"States.ParameterPathFailure",
	"States.Permissions",
	"States.ResultPathMatchFailure",
	"States.ResultWriterFailed",
	"States.Runtime",
	"States.TaskFailed",
	"States.Timeout",
}

func TestErrorMatches_statesTaskFailedIsAWildcard(t *testing.T) {
	// Given: the ErrorEquals real state machines reach for first
	wildcard := []string{errTaskFailed}

	// Then: it matches whatever the Task actually raised, not just the literal
	// name — this is the matcher that made Retry/Catch look like a no-op
	for _, name := range []string{
		"Lambda.ResourceNotFoundException",
		"Lambda.Unknown",
		"CustomFunctionError",
		errTimeout,
		errTaskFailed,
	} {
		if !errorMatches(wildcard, &stateError{name: name}) {
			t.Errorf("States.TaskFailed should match %q", name)
		}
	}

	// … but never States.Runtime, so an Overcast gap stays uncatchable
	if errorMatches(wildcard, unsupportedError("the frobnicate integration")) {
		t.Error("States.TaskFailed must not match States.Runtime")
	}
}

func TestErrorMatches_predefinedNames(t *testing.T) {
	// Given: every reserved error name in the language
	for _, name := range predefinedErrorNamesForTest {
		t.Run(name, func(t *testing.T) {
			failure := &stateError{name: name}

			// Then: naming it explicitly always matches it
			if !errorMatches([]string{name}, failure) {
				t.Errorf("ErrorEquals [%q] should match the error %q", name, name)
			}

			// And: both wildcards cover it, except States.Runtime
			wantWildcard := name != errRuntime
			for _, wildcard := range []string{errAll, errTaskFailed} {
				if got := errorMatches([]string{wildcard}, failure); got != wantWildcard {
					t.Errorf("ErrorEquals [%q] matching %q = %v, want %v", wildcard, name, got, wantWildcard)
				}
			}

			// And: it is not a wildcard itself unless it is one of the two —
			// States.Timeout must not catch a States.Permissions failure
			if name == errAll || name == errTaskFailed {
				return
			}
			if errorMatches([]string{name}, &stateError{name: "SomeOtherError"}) {
				t.Errorf("ErrorEquals [%q] must match literally, not as a wildcard", name)
			}
		})
	}
}

func TestMatchRetrier_statesTaskFailedRetriesAServiceError(t *testing.T) {
	// Given: the retrier shape AWS's own documentation suggests, against the
	// error a Lambda Task really raises
	two := 2
	retriers := []aslRetrier{{ErrorEquals: []string{errTaskFailed}, MaxAttempts: &two}}
	attempts := []int{0}
	failure := &stateError{name: "Lambda.ResourceNotFoundException"}

	// When/Then: it retries until its attempts run out
	for i := 0; i < 2; i++ {
		if got := matchRetrier(retriers, attempts, failure); got != 0 {
			t.Fatalf("attempt %d: matchRetrier = %d, want 0", i, got)
		}
		attempts[0]++
	}
	if got := matchRetrier(retriers, attempts, failure); got != -1 {
		t.Fatalf("matchRetrier after exhaustion = %d, want -1", got)
	}
}

func TestMatchCatcher_statesTaskFailedCatchesAServiceError(t *testing.T) {
	// Given: the Catch shape real state machines are written with
	catchers := []aslCatcher{{ErrorEquals: []string{errTaskFailed}, Next: "Handled"}}

	// When: a Task fails with a service error name
	got := matchCatcher(catchers, &stateError{name: "Lambda.ResourceNotFoundException"})

	// Then: it is caught
	if got == nil {
		t.Fatal("States.TaskFailed did not catch Lambda.ResourceNotFoundException")
	}
	if got.Next != "Handled" {
		t.Fatalf("catcher Next = %q, want Handled", got.Next)
	}

	// And: an Overcast gap still is not
	if matchCatcher(catchers, unsupportedError("distributed Map")) != nil {
		t.Error("States.TaskFailed must not catch States.Runtime")
	}
}

func TestPositiveSeconds_resolvesSecondsAndPath(t *testing.T) {
	input := map[string]any{"budget": float64(7)}
	timeout := func(state aslState) (*int64, *stateError) {
		f := &flow{state: &state, raw: input, effective: input}
		return f.positiveSeconds(state.TimeoutSeconds, state.TimeoutSecondsPath, "TimeoutSeconds")
	}

	// Given: no timeout declared — AWS leaves only the execution's own
	got, serr := timeout(aslState{})
	if serr != nil || got != nil {
		t.Fatalf("no fields = %v, %v; want nil, nil", got, serr)
	}

	// Given: a literal TimeoutSeconds
	three := aslNumber{Set: true, Value: 3}
	got, serr = timeout(aslState{TimeoutSeconds: three})
	if serr != nil || got == nil || *got != 3 {
		t.Fatalf("TimeoutSeconds: 3 = %v, %v", got, serr)
	}

	// Given: a TimeoutSecondsPath, which takes precedence
	got, serr = timeout(aslState{TimeoutSeconds: three, TimeoutSecondsPath: "$.budget"})
	if serr != nil || got == nil || *got != 7 {
		t.Fatalf("TimeoutSecondsPath = %v, %v", got, serr)
	}

	// Given: a path that does not resolve to a positive number
	if _, serr = timeout(aslState{TimeoutSecondsPath: "$.missing"}); serr == nil {
		t.Fatal("an unresolvable TimeoutSecondsPath should fail the state, not be ignored")
	}
}

func TestMatchRetrier_exhaustsAttempts(t *testing.T) {
	// Given: a retrier with two attempts
	two := 2
	retriers := []aslRetrier{{ErrorEquals: []string{errAll}, MaxAttempts: &two}}
	attempts := []int{0}
	failure := &stateError{name: errTaskFailed}

	// When/Then: it matches until its attempts run out
	for i := 0; i < 2; i++ {
		if got := matchRetrier(retriers, attempts, failure); got != 0 {
			t.Fatalf("attempt %d: matchRetrier = %d, want 0", i, got)
		}
		attempts[0]++
	}
	if got := matchRetrier(retriers, attempts, failure); got != -1 {
		t.Fatalf("matchRetrier after exhaustion = %d, want -1", got)
	}
}

func TestRetryDelay_appliesBackoffAndCap(t *testing.T) {
	interval := 2.0
	backoff := 3.0
	maxDelay := 10.0
	retrier := aslRetrier{IntervalSeconds: &interval, BackoffRate: &backoff, MaxDelaySeconds: &maxDelay}

	if got := retryDelay(retrier, 0).Seconds(); got != 2 {
		t.Errorf("first delay = %v, want 2", got)
	}
	if got := retryDelay(retrier, 1).Seconds(); got != 6 {
		t.Errorf("second delay = %v, want 6", got)
	}
	if got := retryDelay(retrier, 5).Seconds(); got != 10 {
		t.Errorf("capped delay = %v, want 10", got)
	}
}

// ─── Task resource classification ─────────────────────────────────────────────

func TestParseTaskResource(t *testing.T) {
	cases := []struct {
		name        string
		resource    string
		wantService string
		wantAction  string
		wantPattern string
		wantFn      string
		wantErr     bool
	}{
		{name: "function ARN", resource: "arn:aws:lambda:us-east-1:000000000000:function:hello", wantService: "lambda", wantAction: "invoke", wantFn: "hello"},
		{name: "function ARN with qualifier", resource: "arn:aws:lambda:us-east-1:000000000000:function:hello:PROD", wantService: "lambda", wantAction: "invoke", wantFn: "hello:PROD"},
		{name: "optimized lambda", resource: "arn:aws:states:::lambda:invoke", wantService: "lambda", wantAction: "invoke"},
		{name: "waitForTaskToken", resource: "arn:aws:states:::lambda:invoke.waitForTaskToken", wantService: "lambda", wantAction: "invoke", wantPattern: "waitForTaskToken"},
		{name: "sqs", resource: "arn:aws:states:::sqs:sendMessage", wantService: "sqs", wantAction: "sendMessage"},
		{name: "sns", resource: "arn:aws:states:::sns:publish", wantService: "sns", wantAction: "publish"},
		{name: "dynamodb", resource: "arn:aws:states:::dynamodb:putItem", wantService: "dynamodb", wantAction: "putItem"},
		{name: "nested sync", resource: "arn:aws:states:::states:startExecution.sync", wantService: "states", wantAction: "startExecution", wantPattern: "sync"},
		{name: "nested sync 2", resource: "arn:aws:states:::states:startExecution.sync:2", wantService: "states", wantAction: "startExecution", wantPattern: "sync:2"},
		{name: "aws-sdk integration", resource: "arn:aws:states:::aws-sdk:s3:getObject", wantService: "aws-sdk:s3", wantAction: "getObject"},
		{name: "activity", resource: "arn:aws:states:us-east-1:000000000000:activity:my-activity", wantService: "activity"},
		{name: "not an ARN", resource: "hello", wantErr: true},
		{name: "unrelated service", resource: "arn:aws:ecs:us-east-1:000000000000:task-definition/x", wantErr: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, serr := parseTaskResource(tc.resource)
			if tc.wantErr {
				if serr == nil {
					t.Fatalf("expected an error for %q", tc.resource)
				}
				if serr.name != errRuntime {
					t.Fatalf("error name = %q, want %s", serr.name, errRuntime)
				}
				return
			}
			if serr != nil {
				t.Fatalf("parseTaskResource(%q): %v", tc.resource, serr)
			}
			if got.service != tc.wantService || got.action != tc.wantAction || got.pattern != tc.wantPattern || got.functionName != tc.wantFn {
				t.Fatalf("parseTaskResource(%q) = %+v", tc.resource, got)
			}
		})
	}
}

// ─── History recorder ─────────────────────────────────────────────────────────

func TestHistoryRecorder_linksAndCaps(t *testing.T) {
	// Given: a recorder with a small cap
	recorder := newHistoryRecorder(3)
	now := testTime()

	// When: we add events
	for i := 0; i < 3; i++ {
		recorder.add(now, HistoryEvent{Type: evtExecutionStarted})
	}

	// Then: ids are 1-based, previousEventId links back, and the cap trips
	for i, event := range recorder.events {
		if event.ID != int64(i+1) {
			t.Errorf("event[%d].ID = %d", i, event.ID)
		}
		if event.PreviousEventID != int64(i) {
			t.Errorf("event[%d].PreviousEventID = %d", i, event.PreviousEventID)
		}
	}
	if !recorder.full() {
		t.Error("recorder should report full at its cap")
	}
}

// ─── Local helpers ────────────────────────────────────────────────────────────

func decodeTestJSON(t *testing.T, raw string) any {
	t.Helper()
	var decoded any
	if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
		t.Fatalf("decode %q: %v", raw, err)
	}
	return decoded
}

// testTime returns a fixed timestamp so history assertions never depend on the
// wall clock.
func testTime() time.Time { return time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC) }

// ─── Terminal status mapping ──────────────────────────────────────────────────

func TestStatusForError_distinguishesAbortFromTimeoutAndFailure(t *testing.T) {
	// Given: the three shapes of unwind the interpreter can report
	cases := []struct {
		name string
		serr *stateError
		want string
	}{
		{"stopped by StopExecution", &stateError{name: "OperatorStop", cause: "asked", aborted: true}, statusAborted},
		{"stopped with no reason given", &stateError{aborted: true}, statusAborted},
		{"runaway guard fired", &stateError{name: errTimeout, cause: "budget", budgetExpired: true}, statusTimedOut},
		{"ordinary failure", &stateError{name: errTaskFailed}, statusFailed},
		{"unsupported ASL", unsupportedError("something"), statusFailed},
		// Only the execution's own budget ends an execution TIMED_OUT. A Task
		// that blew its TimeoutSeconds, and a Fail state that happens to spell
		// States.Timeout, are ordinary failures on AWS.
		{"task-level timeout", &stateError{name: errTimeout, cause: "the Task state \"T\" exceeded its TimeoutSeconds of 1"}, statusFailed},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Then: each maps to the status AWS would report
			if got := statusForError(tc.serr); got != tc.want {
				t.Fatalf("statusForError = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestExecutionRun_abortReasonCarriesStopDetails(t *testing.T) {
	// Given: a registered run
	cancelled := false
	run := &executionRun{cancel: func() { cancelled = true }, hist: newHistoryRecorder(10)}

	// When: nothing has stopped it
	if stopped, _, _ := run.abortReason(); stopped {
		t.Fatal("a fresh run should not report an abort")
	}

	// And: StopExecution stops it
	at := testTime()
	run.stop(at, "OperatorStop", "asked to")

	// Then: the reason and the stop time are carried to the terminal record,
	// and the interpreter has been cancelled
	stopped, errName, cause := run.abortReason()
	if !stopped || errName != "OperatorStop" || cause != "asked to" {
		t.Fatalf("abortReason = %v/%q/%q", stopped, errName, cause)
	}
	if recorded, ok := run.stopTime(); !ok || !recorded.Equal(at) {
		t.Fatalf("stopTime = %v/%v, want %v", recorded, ok, at)
	}
	if !cancelled {
		t.Error("stop should cancel the execution context")
	}

	// And: a second stop does not overwrite the first reason
	run.stop(at.Add(1), "Later", "later")
	if _, errName, _ := run.abortReason(); errName != "OperatorStop" {
		t.Errorf("second stop overwrote the reason: %q", errName)
	}
}

func TestHistoryRecorder_snapshotIsACopy(t *testing.T) {
	// Given: a recorder with one event — the shape GetExecutionHistory reads
	// while an execution is still running
	recorder := newHistoryRecorder(10)
	recorder.add(testTime(), HistoryEvent{Type: evtExecutionStarted})

	// When: we snapshot it and the execution records more
	snapshot := recorder.snapshot()
	recorder.add(testTime(), HistoryEvent{Type: evtExecutionSucceeded})

	// Then: the snapshot is not aliased to the live slice
	if len(snapshot) != 1 {
		t.Fatalf("snapshot grew to %d events after the recorder advanced", len(snapshot))
	}
	if len(recorder.snapshot()) != 2 {
		t.Fatal("recorder did not advance")
	}
}
