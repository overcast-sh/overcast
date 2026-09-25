package scenario

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadRejectsAMalformedScenario(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		wantErr string
	}{
		{name: "unparsable", body: `{`, wantErr: "parse"},
		{name: "the wrong version", body: `{"version":2,"service":"widgets","client":{"endpointPrefix":"widgets"},"groups":[]}`, wantErr: "version 2, want 1"},
		{name: "no command name", body: `{"version":1,"service":"widgets","client":{},"groups":[]}`, wantErr: "endpointPrefix"},
		{
			// A test has one primary call, so it has one outcome to check
			// against an errorCode clause. errorCodeClause takes the first and
			// runTest skips the rest, so a second clause naming a different
			// error would never be evaluated and the test would pass on the
			// strength of a check nobody made. Refusing the file is the honest
			// outcome; the fix belongs in the recipe or the generator.
			name: "two errorCode clauses on one test",
			body: `{"version":1,"service":"widgets","client":{"endpointPrefix":"widgets"},"groups":[
				{"name":"widgets-gen-thing","kind":"lifecycle","setup":[],"teardown":[],"tests":[
					{"name":"GetThingAbsent","op":"GetThing","call":{"op":"GetThing","params":{}},"assert":[
						{"kind":"errorCode","error":{"shape":"NotFoundException","code":"NotFoundException"}},
						{"kind":"errorCode","error":{"shape":"AccessDenied","code":"AccessDenied"}}
					]}
				]}
			]}`,
			wantErr: "widgets-gen-thing/GetThingAbsent carries 2 errorCode clauses",
		},
		{
			// An equalsJSON operand is a literal document and is never
			// evaluated, so JSON text in a string is not one.
			name: "an equalsJSON operand that is a string",
			body: `{"version":1,"service":"widgets","client":{"endpointPrefix":"widgets"},"groups":[
				{"name":"widgets-gen-thing","kind":"probe","setup":[],"teardown":[],"tests":[
					{"name":"GetThing","op":"GetThing","call":{"op":"GetThing","params":{}},"assert":[
						{"kind":"responseField","checks":{"$.Policy":{"equalsJSON":"{\"a\":1}"}}}
					]}
				]}
			]}`,
			wantErr: "equalsJSON: the operand must be a JSON object or array",
		},
		{
			// A $-key inside the operand would be read as an expression
			// anywhere else in the IR; here it is refused rather than read
			// either way.
			name: "an equalsJSON operand with a $-key",
			body: `{"version":1,"service":"widgets","client":{"endpointPrefix":"widgets"},"groups":[
				{"name":"widgets-gen-thing","kind":"probe","setup":[],"teardown":[],"tests":[
					{"name":"GetThing","op":"GetThing","call":{"op":"GetThing","params":{}},"assert":[
						{"kind":"responseField","checks":{"$.Policy":{"equalsJSON":{"Statement":[{"Resource":{"$ref":"q.arn"}}]}}}}
					]}
				]}
			]}`,
			wantErr: `its key "$ref" may not start with $`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			rel := "compat/model/scenarios/widgets.json"
			abs := filepath.Join(root, filepath.FromSlash(rel))
			if err := os.MkdirAll(filepath.Dir(abs), 0o750); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(abs, []byte(tc.body), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := New(root).load(rel)
			if err == nil {
				t.Fatal("want a load error")
			}
			if !strings.Contains(err.Error(), tc.wantErr) || !strings.Contains(err.Error(), rel) {
				t.Errorf("error %v, want one naming %q and containing %q", err, rel, tc.wantErr)
			}
		})
	}
}

// TestLoadReadsEachFileOnce: the loader is consulted once per test at build
// time and again for setup and teardown, and the harness builds every suite's
// groups up front, so a scenario file must not be re-read per test.
func TestLoadReadsEachFileOnce(t *testing.T) {
	root := t.TempDir()
	rel := "compat/model/scenarios/widgets.json"
	abs := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(abs), 0o750); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(scenarioFile(lifecycle("widgets-gen-thing", obj{
		"name": "GetThing", "op": "GetThing",
		"call":   obj{"op": "GetThing", "params": obj{}},
		"assert": []any{obj{"kind": "responseField", "checks": obj{"$.Id": obj{"nonEmpty": true}}}},
	})))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(abs, raw, 0o600); err != nil {
		t.Fatal(err)
	}

	b := New(root)
	first, err := b.load(rel)
	if err != nil {
		t.Fatal(err)
	}
	// Deleting the file proves the second read came from the cache.
	if err := os.Remove(abs); err != nil {
		t.Fatal(err)
	}
	second, err := b.load(rel)
	if err != nil {
		t.Fatalf("the second load must come from the cache: %v", err)
	}
	if first != second {
		t.Error("load returned two different files for one path")
	}
	if first.Path != rel {
		t.Errorf("Path = %q, want the registry-relative path %q", first.Path, rel)
	}
}

// TestCheckDecoding pins the hand-written Check decoder, whose whole reason to
// exist is telling {"equals": null} from an absent key.
func TestCheckDecoding(t *testing.T) {
	cases := []struct {
		body     string
		wantKind CheckKind
		wantVal  any
		wantErr  bool
	}{
		{body: `{"nonEmpty":true}`, wantKind: CheckNonEmpty},
		{body: `{"isList":true}`, wantKind: CheckIsList},
		{body: `{"missing":true}`, wantKind: CheckMissing},
		{body: `{"equals":"60"}`, wantKind: CheckEquals, wantVal: "60"},
		{body: `{"equals":null}`, wantKind: CheckEquals, wantVal: nil},
		{body: `{"matches":"^p-"}`, wantKind: CheckMatches, wantVal: "^p-"},
		{body: `{"equalsJSON":{"Version":"2012-10-17","Statement":[]}}`, wantKind: CheckEqualsJSON, wantVal: map[string]any{"Version": "2012-10-17", "Statement": []any{}}},
		{body: `{"equalsJSON":["a",{"b":null}]}`, wantKind: CheckEqualsJSON, wantVal: []any{"a", map[string]any{"b": nil}}},
		// The operand is a literal document, never a value expression: JSON
		// text in a string, a scalar, and a $-key at any depth are refused
		// when the file loads.
		{body: `{"equalsJSON":"{\"a\":1}"}`, wantErr: true},
		{body: `{"equalsJSON":1}`, wantErr: true},
		{body: `{"equalsJSON":null}`, wantErr: true},
		{body: `{"equalsJSON":{"$ref":"role.policy"}}`, wantErr: true},
		{body: `{"equalsJSON":{"Statement":[{"Resource":{"$name":"q"}}]}}`, wantErr: true},
		{body: `{}`, wantErr: true},
		{body: `{"nonEmpty":true,"isList":true}`, wantErr: true},
		{body: `{"nope":true}`, wantErr: true},
	}
	for _, tc := range cases {
		var c Check
		err := json.Unmarshal([]byte(tc.body), &c)
		if tc.wantErr {
			if err == nil {
				t.Errorf("%s: want a decode error", tc.body)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: %v", tc.body, err)
			continue
		}
		if c.Kind != tc.wantKind || !jsonEqual(c.Value, tc.wantVal) {
			t.Errorf("%s decoded to %s %s, want %s %s", tc.body, c.Kind, render(c.Value), tc.wantKind, render(tc.wantVal))
		}
	}
}

// TestPilotCorpusLoads reads the committed scenario files — the corpus this
// interpreter exists to run — and holds every construct in them to the closed
// sets this package implements. It is the check that catches an IR construct
// added upstream that the interpreter has no branch for, without a live
// emulator or the aws binary.
func TestPilotCorpusLoads(t *testing.T) {
	root := repoRootFromTest(t)
	b := New(root)
	for _, service := range []string{"sqs", "organizations"} {
		rel := "compat/model/scenarios/" + service + ".json"
		f, err := b.load(rel)
		if err != nil {
			t.Fatalf("%s: %v", rel, err)
		}
		if len(f.Groups) == 0 {
			t.Fatalf("%s declares no groups", rel)
		}
		for _, g := range f.Groups {
			if g.Kind == "probe" && (len(g.Setup) > 0 || len(g.Teardown) > 0) {
				t.Errorf("%s: probe group %s carries setup or teardown", rel, g.Name)
			}
			for _, c := range g.Setup {
				checkCall(t, rel, g.Name, c)
			}
			for _, c := range g.Teardown {
				checkCall(t, rel, g.Name, c)
			}
			for _, tc := range g.Tests {
				if len(tc.Assert) == 0 {
					t.Errorf("%s: %s/%s has no assertion clause", rel, g.Name, tc.Name)
				}
				checkCall(t, rel, g.Name, tc.Call)
				for i := range tc.Assert {
					checkClause(t, rel, g.Name+"/"+tc.Name, &tc.Assert[i])
				}
			}
		}
	}
}

func checkCall(t *testing.T, file, where string, c Call) {
	t.Helper()
	if c.Op == "" {
		t.Errorf("%s: %s has a call with no operation", file, where)
	}
	for ctxPath, respPath := range c.Export {
		if _, err := parsePath(respPath); err != nil {
			t.Errorf("%s: %s: export %s: %v", file, where, ctxPath, err)
		}
	}
}

func checkClause(t *testing.T, file, where string, a *Assertion) {
	t.Helper()
	switch a.Kind {
	case kindResponseField, kindReadback:
		for path, check := range a.Checks {
			if _, err := parsePath(path); err != nil {
				t.Errorf("%s: %s: %v", file, where, err)
			}
			switch check.Kind {
			case CheckNonEmpty, CheckIsList, CheckEquals, CheckMatches, CheckEqualsJSON, CheckMissing:
			default:
				t.Errorf("%s: %s: unknown check %q at %s", file, where, check.Kind, path)
			}
		}
	case kindListContains, kindAbsent:
		if a.Error == nil {
			if _, err := parsePath(a.ItemsPath); err != nil {
				t.Errorf("%s: %s: %v", file, where, err)
			}
			for path := range a.Where {
				if _, err := parsePath(path); err != nil {
					t.Errorf("%s: %s: %v", file, where, err)
				}
			}
		}
	case kindErrorCode:
		if a.Error == nil {
			t.Errorf("%s: %s: errorCode with no error", file, where)
		}
	case kindEventually:
		if a.MaxAttempts < 1 || a.Assert == nil {
			t.Errorf("%s: %s: eventually with no budget or no inner clause", file, where)
		}
		checkClause(t, file, where, a.Assert)
	default:
		t.Errorf("%s: %s: unknown assertion kind %q", file, where, a.Kind)
	}
	if a.Call != nil {
		checkCall(t, file, where, *a.Call)
	}
}

// fixturesRequiredEnvVar is set to "1" only by test.yml's
// compat-suite-unit-tests job, which runs this test from a full checkout
// where the corpus is always reachable. Its absence there would mean the
// shared conformance set silently stopped being checked anywhere — see
// compat/AGENTS.md § Where the shared error corpus runs.
const fixturesRequiredEnvVar = "OVERCAST_COMPAT_FIXTURES_REQUIRED"

// repoRootFromTest walks up from the test's working directory to the directory
// holding compat/model. The test skips rather than fails when there is none:
// the suite module is also built in images that carry no model directory, and
// a corpus check has nothing to say there — unless fixturesRequiredEnvVar says
// this run is expected to see it, in which case that silence would mean the
// shared conformance set was not checked anywhere, and the test fails loudly.
func repoRootFromTest(t *testing.T) string {
	t.Helper()
	start, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	dir := start
	for i := 0; i < 8; i++ {
		if _, err := os.Stat(filepath.Join(dir, "compat", "model", "scenarios")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	if os.Getenv(fixturesRequiredEnvVar) == "1" {
		t.Fatalf("%s=1 but no compat/model found walking up from %s — this suite's fixture test must run from a full checkout (test.yml's compat-suite-unit-tests job)",
			fixturesRequiredEnvVar, start)
	}
	t.Skipf("no compat/model above %s; nothing to check (set %s=1 to make this fatal)", start, fixturesRequiredEnvVar)
	return ""
}
