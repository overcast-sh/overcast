package scenario

import (
	"strings"
	"testing"
	"time"

	"github.com/overcast-sh/overcast-compat-cli/internal/registry"
)

// clauseCase runs one test whose single assertion is `clause`, against a
// scripted set of responses.
type clauseCase struct {
	name   string
	clause obj
	// call is the primary call's params; its response is script["get-thing"].
	script  map[string][]fakeResult
	wantErr string // a substring of the failure; empty means the test must pass
}

func runClauseCases(t *testing.T, cases []clauseCase) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b, fake, rg := fixture(t, scenarioFile(lifecycle("widgets-gen-thing", obj{
				"name": "GetThing", "op": "GetThing",
				"call":   obj{"op": "GetThing", "params": obj{}},
				"assert": []any{tc.clause},
			})))
			for op, results := range tc.script {
				fake.script[op] = results
			}
			err := runOneTest(t, b, rg, "GetThing")
			switch {
			case tc.wantErr == "" && err != nil:
				t.Fatalf("want a pass, got: %v", err)
			case tc.wantErr != "" && err == nil:
				t.Fatalf("want a failure containing %q, got a pass", tc.wantErr)
			case tc.wantErr != "" && !strings.Contains(err.Error(), tc.wantErr):
				t.Fatalf("failure %v does not contain %q", err, tc.wantErr)
			}
		})
	}
}

// TestResponseFieldChecks covers every check in the closed set, on the test's
// own response.
func TestResponseFieldChecks(t *testing.T) {
	body := obj{
		"Id":     "t-1",
		"Count":  float64(0),
		"Ready":  false,
		"Empty":  "",
		"Tags":   obj{},
		"Items":  []any{},
		"Nested": obj{"Arn": "arn:aws:widgets::1234:thing/t-1"},
		"Null":   nil,
	}
	script := map[string][]fakeResult{"get-thing": {ok(body)}}
	field := func(path string, check obj) obj {
		return obj{"kind": "responseField", "checks": obj{path: check}}
	}

	runClauseCases(t, []clauseCase{
		{name: "nonEmpty holds on a string", clause: field("$.Id", obj{"nonEmpty": true}), script: script},
		{name: "nonEmpty holds on zero", clause: field("$.Count", obj{"nonEmpty": true}), script: script},
		{name: "nonEmpty holds on false", clause: field("$.Ready", obj{"nonEmpty": true}), script: script},
		{name: "nonEmpty fails on an empty string", clause: field("$.Empty", obj{"nonEmpty": true}), script: script, wantErr: "nonEmpty"},
		{name: "nonEmpty fails on an empty object", clause: field("$.Tags", obj{"nonEmpty": true}), script: script, wantErr: "nonEmpty"},
		{name: "nonEmpty fails on an empty list", clause: field("$.Items", obj{"nonEmpty": true}), script: script, wantErr: "nonEmpty"},
		{name: "nonEmpty fails on null", clause: field("$.Null", obj{"nonEmpty": true}), script: script, wantErr: "nonEmpty"},
		{name: "nonEmpty fails when absent", clause: field("$.Nope", obj{"nonEmpty": true}), script: script, wantErr: missingValue},

		{name: "isList holds on an empty list", clause: field("$.Items", obj{"isList": true}), script: script},
		{name: "isList holds on an omitted member", clause: field("$.Nope", obj{"isList": true}), script: script},
		{name: "isList fails on a present non-list", clause: field("$.Id", obj{"isList": true}), script: script, wantErr: "isList"},

		{name: "equals holds", clause: field("$.Id", obj{"equals": "t-1"}), script: script},
		{name: "equals holds on a number", clause: field("$.Count", obj{"equals": float64(0)}), script: script},
		{name: "equals does not coerce string to number", clause: field("$.Count", obj{"equals": "0"}), script: script, wantErr: "equals"},
		{name: "equals fails when absent", clause: field("$.Nope", obj{"equals": "x"}), script: script, wantErr: missingValue},

		{name: "matches holds", clause: field("$.Nested.Arn", obj{"matches": `^arn:aws:widgets::\d{4}:thing/t-\d+$`}), script: script},
		{name: "matches fails", clause: field("$.Id", obj{"matches": `^p-`}), script: script, wantErr: "matches"},
		{name: "matches fails on a non-string", clause: field("$.Count", obj{"matches": `^0$`}), script: script, wantErr: "matches"},

		{name: "missing holds when absent", clause: field("$.Nope", obj{"missing": true}), script: script},
		{name: "missing holds for a deep absent segment", clause: field("$.Nested.Nope.Deep", obj{"missing": true}), script: script},
		{name: "missing fails when present", clause: field("$.Id", obj{"missing": true}), script: script, wantErr: "missing"},
		{name: "missing holds is not the same as null", clause: field("$.Null", obj{"missing": true}), script: script, wantErr: "missing"},
	})
}

// TestListClauses covers listContains and both forms of absent.
func TestListClauses(t *testing.T) {
	runClauseCases(t, []clauseCase{
		{
			name: "listContains matches a scalar item through $",
			clause: obj{"kind": "listContains", "itemsPath": "$.Urls",
				"where": obj{"$": "https://q/2"}},
			script: map[string][]fakeResult{"get-thing": {ok(obj{"Urls": []any{"https://q/1", "https://q/2"}})}},
		},
		{
			name: "listContains matches on every where entry",
			clause: obj{"kind": "listContains", "itemsPath": "$.Tags",
				"where": obj{"$.Key": "compat", "$.Value": "scenario"}},
			script: map[string][]fakeResult{"get-thing": {ok(obj{"Tags": []any{
				obj{"Key": "compat", "Value": "other"}, obj{"Key": "compat", "Value": "scenario"}}})}},
		},
		{
			name: "listContains fails when only some entries match",
			clause: obj{"kind": "listContains", "itemsPath": "$.Tags",
				"where": obj{"$.Key": "compat", "$.Value": "scenario"}},
			script: map[string][]fakeResult{"get-thing": {ok(obj{"Tags": []any{
				obj{"Key": "compat", "Value": "other"}}})}},
			wantErr: "listContains",
		},
		{
			name:    "listContains fails on a missing list",
			clause:  obj{"kind": "listContains", "itemsPath": "$.Urls", "where": obj{"$": "x"}},
			script:  map[string][]fakeResult{"get-thing": {ok(obj{})}},
			wantErr: "an empty list",
		},
		{
			name: "listContains reads its own call when it has one",
			clause: obj{"kind": "listContains",
				"call":      obj{"op": "ListThings", "params": obj{}},
				"itemsPath": "$.Things", "where": obj{"$": "t-1"}},
			script: map[string][]fakeResult{
				"get-thing":   {ok(obj{})},
				"list-things": {ok(obj{"Things": []any{"t-1"}})},
			},
		},
		{
			name:   "absent holds on a missing list",
			clause: obj{"kind": "absent", "itemsPath": "$.Messages", "where": obj{"$.MessageId": "m-1"}},
			script: map[string][]fakeResult{"get-thing": {ok(obj{})}},
		},
		{
			name:    "absent fails when the item is there",
			clause:  obj{"kind": "absent", "itemsPath": "$.Messages", "where": obj{"$.MessageId": "m-1"}},
			script:  map[string][]fakeResult{"get-thing": {ok(obj{"Messages": []any{obj{"MessageId": "m-1"}}})}},
			wantErr: "no item matching",
		},
		{
			name: "absent error form holds when the call raises the wire code",
			clause: obj{"kind": "absent",
				"call":  obj{"op": "GetQueueAttributes", "params": obj{}},
				"error": obj{"shape": "QueueDoesNotExist", "code": "AWS.SimpleQueueService.NonExistentQueue"}},
			script: map[string][]fakeResult{
				"get-thing":            {ok(obj{})},
				"get-queue-attributes": {awsErr("AWS.SimpleQueueService.NonExistentQueue")},
			},
		},
		{
			name: "absent error form holds when the call raises the shape name",
			clause: obj{"kind": "absent",
				"call":  obj{"op": "GetQueueAttributes", "params": obj{}},
				"error": obj{"shape": "QueueDoesNotExist", "code": "AWS.SimpleQueueService.NonExistentQueue"}},
			script: map[string][]fakeResult{
				"get-thing":            {ok(obj{})},
				"get-queue-attributes": {awsErr("QueueDoesNotExist")},
			},
		},
		{
			name: "absent error form fails on a different error",
			clause: obj{"kind": "absent",
				"call":  obj{"op": "GetQueueAttributes", "params": obj{}},
				"error": obj{"shape": "QueueDoesNotExist", "code": "AWS.SimpleQueueService.NonExistentQueue"}},
			script: map[string][]fakeResult{
				"get-thing":            {ok(obj{})},
				"get-queue-attributes": {awsErr("AccessDenied")},
			},
			wantErr: "AccessDenied",
		},
		{
			name: "absent error form fails when the call succeeds",
			clause: obj{"kind": "absent",
				"call":  obj{"op": "GetQueueAttributes", "params": obj{}},
				"error": obj{"shape": "QueueDoesNotExist", "code": "AWS.SimpleQueueService.NonExistentQueue"}},
			script: map[string][]fakeResult{
				"get-thing":            {ok(obj{})},
				"get-queue-attributes": {ok(obj{"Attributes": obj{}})},
			},
			wantErr: "<no error>",
		},
	})
}

// TestReadbackAppliesExportsOnlyWhenItsChecksHold: a failing read-back must not
// leave a value in the context bag for a later clause to $ref.
func TestReadbackAppliesExportsOnlyWhenItsChecksHold(t *testing.T) {
	b, fake, rg := fixture(t, scenarioFile(lifecycle("widgets-gen-thing", obj{
		"name": "GetThing", "op": "GetThing",
		"call": obj{"op": "GetThing", "params": obj{}},
		"assert": []any{
			obj{"kind": "readback",
				"call":   obj{"op": "Readback", "params": obj{}, "export": obj{"thing.arn": "$.Arn"}},
				"checks": obj{"$.Id": obj{"equals": "wrong"}}},
			obj{"kind": "readback",
				"call":   obj{"op": "Second", "params": obj{"Arn": obj{"$ref": "thing.arn"}}},
				"checks": obj{"$.Id": obj{"nonEmpty": true}}},
		},
	})))
	fake.script["get-thing"] = []fakeResult{ok(obj{})}
	fake.script["readback"] = []fakeResult{ok(obj{"Id": "t-1", "Arn": "arn:thing"})}
	fake.script["second"] = []fakeResult{ok(obj{"Id": "t-1"})}

	err := runOneTest(t, b, rg, "GetThing")
	if err == nil {
		t.Fatal("the first read-back's check fails, so the test fails")
	}
	if got := fake.ops(); len(got) != 2 || got[1] != "readback" {
		t.Errorf("calls = %v, want the test to stop at the failing clause", got)
	}
}

// TestEventuallyRetriesUntilItHolds pins the retry contract: at most
// maxAttempts evaluations in all, the inner exports applied only on the passing
// attempt, and the delay honoured between attempts and nowhere else.
func TestEventuallyRetriesUntilItHolds(t *testing.T) {
	b, fake, rg := fixture(t, scenarioFile(lifecycle("widgets-gen-thing", obj{
		"name": "SetThing", "op": "SetThing",
		"call": obj{"op": "SetThing", "params": obj{}},
		"assert": []any{obj{
			"kind": "eventually", "maxAttempts": float64(5), "delayMs": float64(10),
			"assert": obj{"kind": "readback",
				"call":   obj{"op": "GetThing", "params": obj{}, "export": obj{"thing.state": "$.State"}},
				"checks": obj{"$.State": obj{"equals": "READY"}}},
		}},
	})))
	fake.script["set-thing"] = []fakeResult{ok(obj{})}
	fake.script["get-thing"] = []fakeResult{
		ok(obj{"State": "PENDING"}), ok(obj{"State": "PENDING"}), ok(obj{"State": "READY"}),
	}

	start := time.Now()
	if err := runOneTest(t, b, rg, "SetThing"); err != nil {
		t.Fatalf("want a pass on the third attempt: %v", err)
	}
	if elapsed := time.Since(start); elapsed < 20*time.Millisecond {
		t.Errorf("elapsed %v, want at least the two 10ms delays between three attempts", elapsed)
	}
	if got := fake.ops(); len(got) != 4 {
		t.Errorf("calls = %v, want set-thing plus three attempts", got)
	}
}

func TestEventuallyReportsTheLastFailureAfterMaxAttempts(t *testing.T) {
	b, fake, rg := fixture(t, scenarioFile(lifecycle("widgets-gen-thing", obj{
		"name": "SetThing", "op": "SetThing",
		"call": obj{"op": "SetThing", "params": obj{}},
		"assert": []any{obj{
			"kind": "eventually", "maxAttempts": float64(3), "delayMs": float64(0),
			"assert": obj{"kind": "readback",
				"call":   obj{"op": "GetThing", "params": obj{}},
				"checks": obj{"$.State": obj{"equals": "READY"}}},
		}},
	})))
	fake.script["set-thing"] = []fakeResult{ok(obj{})}
	fake.script["get-thing"] = []fakeResult{ok(obj{"State": "ONE"}), ok(obj{"State": "TWO"}), ok(obj{"State": "THREE"})}

	err := runOneTest(t, b, rg, "SetThing")
	if err == nil {
		t.Fatal("want a failure after three attempts")
	}
	if !strings.Contains(err.Error(), `"THREE"`) {
		t.Errorf("the reported failure must be the last attempt's: %v", err)
	}
	if got := fake.ops(); len(got) != 4 {
		t.Errorf("calls = %v, want exactly maxAttempts attempts", got)
	}
}

// TestErrorCodeExpectsThePrimaryCallToFail covers the kind no derived path
// emits yet, which every interpreter still implements.
func TestErrorCodeExpectsThePrimaryCallToFail(t *testing.T) {
	file := func() obj {
		return scenarioFile(lifecycle("widgets-gen-thing", obj{
			"name": "CreateThingInvalid", "op": "CreateThing",
			"call": obj{"op": "CreateThing", "params": obj{"Name": ""}},
			"assert": []any{
				obj{"kind": "errorCode", "error": obj{"shape": "ValidationException", "code": "ValidationException"}},
			},
		}))
	}

	b, fake, rg := fixture(t, file())
	fake.script["create-thing"] = []fakeResult{awsErr("ValidationException")}
	if err := runOneTest(t, b, rg, "CreateThingInvalid"); err != nil {
		t.Fatalf("the expected error holds: %v", err)
	}

	b, fake, rg = fixture(t, file())
	fake.script["create-thing"] = []fakeResult{ok(obj{"Id": "t-1"})}
	err := runOneTest(t, b, rg, "CreateThingInvalid")
	if err == nil || !strings.Contains(err.Error(), "<no error>") {
		t.Errorf("a call that succeeds fails the clause, got %v", err)
	}

	b, fake, rg = fixture(t, file())
	fake.script["create-thing"] = []fakeResult{awsErr("AccessDenied")}
	err = runOneTest(t, b, rg, "CreateThingInvalid")
	if err == nil || !strings.Contains(err.Error(), "AccessDenied") {
		t.Errorf("a different error fails the clause, got %v", err)
	}
}

// TestFailureCarriesTheSixFields is the contract the interpreter's whole
// debuggability rests on (compat/model/README.md § Failure messages).
func TestFailureCarriesTheSixFields(t *testing.T) {
	b, fake, rg := fixture(t, scenarioFile(lifecycle("widgets-gen-thing", obj{
		"name": "SetThing", "op": "SetThing",
		"call": obj{"op": "SetThing", "params": obj{"Id": obj{"$name": "thing"}}},
		"assert": []any{
			obj{"kind": "responseField", "checks": obj{"$.Ok": obj{"nonEmpty": true}}},
			obj{"kind": "readback",
				"call":   obj{"op": "GetThing", "params": obj{"Id": obj{"$name": "thing"}}},
				"checks": obj{"$.Attributes.Timeout": obj{"equals": "60"}}},
		},
	})))
	fake.script["set-thing"] = []fakeResult{ok(obj{"Ok": true})}
	fake.script["get-thing"] = []fakeResult{ok(obj{"Attributes": obj{"Timeout": "30"}})}

	err := runOneTest(t, b, rg, "SetThing")
	if err == nil {
		t.Fatal("want a failure")
	}
	for _, want := range []string{
		"widgets-gen-thing/SetThing",            // 1 group/test
		"GetThing",                              // 2 the clause's own operation
		`{"Id":"run1-widgets-gen-thing-thing"}`, // 3 the exact params sent
		"readback equals",                       // 4 the assertion kind
		"$.Attributes.Timeout",                  // 4 the path
		`expected "60", actual "30"`,            // 5 expected vs actual
		"compat/model/scenarios/widgets.json",   // 6 the scenario file
		"assert[1]",                             // 6 the step index
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("failure message is missing %q:\n  %v", want, err)
		}
	}
}

// TestFailureLocatesAStepInsideEventually keeps field 6 useful for the clause
// shape the corpus uses most.
func TestFailureLocatesAStepInsideEventually(t *testing.T) {
	b, fake, rg := fixture(t, scenarioFile(lifecycle("widgets-gen-thing", obj{
		"name": "SetThing", "op": "SetThing",
		"call": obj{"op": "SetThing", "params": obj{}},
		"assert": []any{obj{
			"kind": "eventually", "maxAttempts": float64(1), "delayMs": float64(0),
			"assert": obj{"kind": "listContains",
				"call":      obj{"op": "ListThings", "params": obj{}},
				"itemsPath": "$.Things", "where": obj{"$": "t-1"}},
		}},
	})))
	fake.script["set-thing"] = []fakeResult{ok(obj{})}
	fake.script["list-things"] = []fakeResult{ok(obj{"Things": []any{"t-2"}})}

	err := runOneTest(t, b, rg, "SetThing")
	if err == nil || !strings.Contains(err.Error(), "assert[0].assert") {
		t.Errorf("failure = %v, want the nested step index", err)
	}
}

// TestUnresolvableRefFailsTheStepNamingThePath covers the other half of the
// $ref rule: inside a test it is a failure, and it names what was not set.
func TestUnresolvableRefFailsTheStepNamingThePath(t *testing.T) {
	b, fake, rg := fixture(t, scenarioFile(lifecycle("widgets-gen-thing", obj{
		"name": "GetThing", "op": "GetThing",
		"call":   obj{"op": "GetThing", "params": obj{"Id": obj{"$ref": "thing.id"}}},
		"assert": []any{obj{"kind": "responseField", "checks": obj{"$.Id": obj{"nonEmpty": true}}}},
	})))
	fake.script["get-thing"] = []fakeResult{ok(obj{"Id": "t-1"})}

	err := runOneTest(t, b, rg, "GetThing")
	if err == nil {
		t.Fatal("want a failure")
	}
	if !strings.Contains(err.Error(), "thing.id") || !strings.Contains(err.Error(), "<unset>") {
		t.Errorf("failure = %v, want it to name the unresolvable path", err)
	}
	if len(fake.calls) != 0 {
		t.Error("nothing may be sent when a value expression does not evaluate")
	}
}

// TestClausesRunInOrderAndStopAtTheFirstFailure keeps a later clause from
// masking an earlier one.
func TestClausesRunInOrderAndStopAtTheFirstFailure(t *testing.T) {
	b, fake, rg := fixture(t, scenarioFile(lifecycle("widgets-gen-thing", obj{
		"name": "GetThing", "op": "GetThing",
		"call": obj{"op": "GetThing", "params": obj{}},
		"assert": []any{
			obj{"kind": "responseField", "checks": obj{"$.Id": obj{"equals": "wrong"}}},
			obj{"kind": "readback", "call": obj{"op": "Never", "params": obj{}},
				"checks": obj{"$.Id": obj{"nonEmpty": true}}},
		},
	})))
	fake.script["get-thing"] = []fakeResult{ok(obj{"Id": "t-1"})}

	if err := runOneTest(t, b, rg, "GetThing"); err == nil {
		t.Fatal("want a failure")
	}
	if got := fake.ops(); len(got) != 1 {
		t.Errorf("calls = %v, want the run to stop at the first failing clause", got)
	}
}

// TestListClauseAppliesItsCallsExportsOnlyWhenItHolds: the schema allows a
// call's exports wherever a call is allowed, so a list clause follows the same
// rule a read-back does.
func TestListClauseAppliesItsCallsExportsOnlyWhenItHolds(t *testing.T) {
	build := func(want string) (*Backend, *fakeRunner, registry.RegistryGroup) {
		return fixture(t, scenarioFile(lifecycle("widgets-gen-thing", obj{
			"name": "SendThing", "op": "SendThing",
			"call": obj{"op": "SendThing", "params": obj{}},
			"assert": []any{
				obj{"kind": "listContains",
					"call":      obj{"op": "ListThings", "params": obj{}, "export": obj{"thing.id": "$.Things[0].Id"}},
					"itemsPath": "$.Things", "where": obj{"$.Id": want}},
				obj{"kind": "readback",
					"call":   obj{"op": "GetThing", "params": obj{"Id": obj{"$ref": "thing.id"}}},
					"checks": obj{"$.Id": obj{"nonEmpty": true}}},
			},
		})))
	}

	b, fake, rg := build("t-1")
	fake.script["send-thing"] = []fakeResult{ok(obj{})}
	fake.script["list-things"] = []fakeResult{ok(obj{"Things": []any{obj{"Id": "t-1"}}})}
	fake.script["get-thing"] = []fakeResult{ok(obj{"Id": "t-1"})}
	if err := runOneTest(t, b, rg, "SendThing"); err != nil {
		t.Fatalf("want a pass: %v", err)
	}
	if got := fake.calls[2].input; got != `{"Id":"t-1"}` {
		t.Errorf("the read-back did not see the list clause's export: %s", got)
	}

	b, fake, rg = build("t-other")
	fake.script["send-thing"] = []fakeResult{ok(obj{})}
	fake.script["list-things"] = []fakeResult{ok(obj{"Things": []any{obj{"Id": "t-1"}}})}
	err := runOneTest(t, b, rg, "SendThing")
	if err == nil {
		t.Fatal("the list clause does not hold, so the test fails")
	}
	if len(fake.calls) != 2 {
		t.Errorf("calls = %v, want the run to stop at the failing clause", fake.ops())
	}
}
