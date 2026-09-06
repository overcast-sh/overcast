//go:build dev

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The authored layer's whole risk is the join. A generated scenario cannot name
// a group that does not exist, because the generator invents the name; an
// authored one is a human writing the names of a group whose baseline, flaky
// and parity-debt history hang off them, and a typo there soaks green and
// orphans one baseline entry per suite on the flip. Every rule that stops that
// is asserted here.

// authoredFixture is a minimal authored scenario over the widgets fixture
// model, in the shape a file on disk has.
func authoredFixture() map[string]any {
	return map[string]any{
		"version": 1,
		"service": "widgets",
		"client": map[string]any{
			"sdkId":              fixtureClient.SDKID,
			"endpointPrefix":     fixtureClient.EndpointPrefix,
			"signingName":        fixtureClient.SigningName,
			"protocol":           fixtureClient.Protocol,
			"apiVersion":         fixtureClient.APIVersion,
			"targetPrefix":       fixtureClient.TargetPrefix,
			"awsQueryCompatible": false,
		},
		"groups": []any{map[string]any{
			"name":  "widgets-hand-shadow",
			"kind":  "lifecycle",
			"setup": []any{},
			"tests": []any{
				map[string]any{
					"name": "CreateWidget",
					"op":   "CreateWidget",
					"call": map[string]any{
						"op":     "CreateWidget",
						"params": map[string]any{"Name": map[string]any{"$name": "w"}},
						"export": map[string]any{"widget.id": "$.WidgetId"},
					},
					"assert": []any{map[string]any{
						"kind":   "responseField",
						"checks": map[string]any{"$.WidgetId": map[string]any{"nonEmpty": true}},
					}},
				},
				map[string]any{
					"name": "GetWidget",
					"op":   "GetWidget",
					"call": map[string]any{
						"op":     "GetWidget",
						"params": map[string]any{"WidgetId": map[string]any{"$ref": "widget.id"}},
					},
					"assert": []any{map[string]any{
						"kind":   "responseField",
						"checks": map[string]any{"$.Widget.Name": map[string]any{"nonEmpty": true}},
					}},
					"depends": []any{"CreateWidget"},
				},
			},
			"teardown": []any{},
		}},
	}
}

// authoredHandRegistry is the hand-written registry the fixture ports from.
func authoredHandRegistry() *handRegistry {
	return &handRegistry{Groups: []handGroup{{
		Service: "widgets",
		Name:    "widgets-hand",
		Tests: []handTest{
			{Name: "CreateWidget"},
			{Name: "GetWidget", Depends: []string{"CreateWidget"}},
		},
	}}}
}

// writeAuthored writes one authored scenario document to a temp directory and
// loads it back, returning what loadAuthored made of it.
func writeAuthored(t *testing.T, name string, doc map[string]any) ([]authored, error) {
	t.Helper()
	dir := t.TempDir()
	contents, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name+".json"), contents, 0o644); err != nil {
		t.Fatal(err)
	}
	schemas, err := loadSchemas(modelSchemaDir(t))
	if err != nil {
		t.Fatal(err)
	}
	return loadAuthored(dir, schemas)
}

func loadAuthoredFixture(t *testing.T, name string, doc map[string]any) authored {
	t.Helper()
	list, err := writeAuthored(t, name, doc)
	if err != nil {
		t.Fatalf("loadAuthored: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("loaded %d authored scenarios, want 1", len(list))
	}
	return list[0]
}

func TestLoadAuthored_acceptsAWellFormedScenario(t *testing.T) {
	// Given/When: a scenario that satisfies the schema and the IR's rules.
	a := loadAuthoredFixture(t, "widgets-hand", authoredFixture())

	// Then: it is filed under its own name, with the path an interpreter opens.
	if a.name != "widgets-hand" {
		t.Errorf("name = %q", a.name)
	}
	if a.file != "compat/model/authored/widgets-hand.json" {
		t.Errorf("file = %q", a.file)
	}
	if err := checkAuthoredAgainstRegistry(a, authoredHandRegistry()); err != nil {
		t.Errorf("checkAuthoredAgainstRegistry: %v", err)
	}
}

func TestLoadAuthored_refusesAScenarioTheSchemaRejects(t *testing.T) {
	// Given: a test with no assertion clause — the one thing the IR says is
	// not representable.
	doc := authoredFixture()
	group := doc["groups"].([]any)[0].(map[string]any)
	group["tests"].([]any)[0].(map[string]any)["assert"] = []any{}

	// When/Then: the file is refused on load, naming its path.
	_, err := writeAuthored(t, "widgets-hand", doc)
	if err == nil || !strings.Contains(err.Error(), "compat/model/authored/widgets-hand.json") {
		t.Fatalf("err = %v, want a refusal naming the file", err)
	}
}

func TestCheckAuthoredAgainstRegistry_refusesAFileNamedForNoGroup(t *testing.T) {
	// Given: a file whose base name is not a hand-written registry group.
	a := loadAuthoredFixture(t, "widgets-nothing", authoredFixture())

	// When/Then: refused. An authored scenario ports a group; a file that
	// ports nothing would soak against nothing.
	err := checkAuthoredAgainstRegistry(a, authoredHandRegistry())
	if err == nil || !strings.Contains(err.Error(), "no group") {
		t.Fatalf("err = %v", err)
	}
}

func TestCheckAuthoredAgainstRegistry_refusesADriftedTestName(t *testing.T) {
	// Given: a scenario that renamed one of the native group's tests.
	doc := authoredFixture()
	group := doc["groups"].([]any)[0].(map[string]any)
	group["tests"].([]any)[1].(map[string]any)["name"] = "ReadWidget"

	// When/Then: refused. The name is the join key for compat/baseline/,
	// compat/flaky.json and compat/parity-debt.json.
	a := loadAuthoredFixture(t, "widgets-hand", doc)
	err := checkAuthoredAgainstRegistry(a, authoredHandRegistry())
	if err == nil || !strings.Contains(err.Error(), `is "ReadWidget", registry group widgets-hand declares "GetWidget"`) {
		t.Fatalf("err = %v", err)
	}
}

func TestCheckAuthoredAgainstRegistry_refusesADriftedDependsList(t *testing.T) {
	// Given: a scenario that dropped a dependency the registry declares.
	doc := authoredFixture()
	group := doc["groups"].([]any)[0].(map[string]any)
	delete(group["tests"].([]any)[1].(map[string]any), "depends")

	// When/Then: refused. The loader orders and auto-skips on that list, so a
	// shadow with a different one is not running the native group's schedule.
	a := loadAuthoredFixture(t, "widgets-hand", doc)
	err := checkAuthoredAgainstRegistry(a, authoredHandRegistry())
	if err == nil || !strings.Contains(err.Error(), "depends on") {
		t.Fatalf("err = %v", err)
	}
}

func TestCheckAuthoredAgainstRegistry_acceptsEitherTheGroupOrItsShadow(t *testing.T) {
	// Given: the same scenario named for the live group rather than its shadow
	// — which is what the flip PR renames it to.
	doc := authoredFixture()
	doc["groups"].([]any)[0].(map[string]any)["name"] = "widgets-hand"

	// When/Then: accepted, and the shadow suffix is what tells the two apart.
	// The registry entry given here is the flipped one as well: since #1903 the
	// two halves of a port are one change, and ported_test.go pins what each
	// half alone is refused for.
	a := loadAuthoredFixture(t, "widgets-hand", doc)
	if err := checkAuthoredAgainstRegistry(a, portedHandRegistry()); err != nil {
		t.Fatalf("the flipped name must be accepted: %v", err)
	}
	if native, shadow := nativeGroupOf("widgets-hand"); native != "widgets-hand" || shadow {
		t.Errorf("nativeGroupOf(live) = %q, %t", native, shadow)
	}
	if native, shadow := nativeGroupOf("widgets-hand-shadow"); native != "widgets-hand" || !shadow {
		t.Errorf("nativeGroupOf(shadow) = %q, %t", native, shadow)
	}
}

func TestGenerateAuthored_refusesAnOperationTheModelDoesNotDeclare(t *testing.T) {
	// Given: a call to an operation the service does not have.
	doc := authoredFixture()
	group := doc["groups"].([]any)[0].(map[string]any)
	test := group["tests"].([]any)[1].(map[string]any)
	test["op"] = "FetchWidget"
	test["call"].(map[string]any)["op"] = "FetchWidget"

	// When/Then: an error, not a refusal. A refusal is the generator declining
	// to write something; nobody wrote this but a human.
	a := loadAuthoredFixture(t, "widgets-hand", doc)
	f := loadFixture(t)
	_, err := generateAuthored(a, f.model, fixtureClient)
	if err == nil || !strings.Contains(err.Error(), "not a modeled operation") {
		t.Fatalf("err = %v", err)
	}
}

func TestGenerateAuthored_refusesAnInputMemberTheOperationDoesNotTake(t *testing.T) {
	// Given: a member CreateWidget does not declare.
	doc := authoredFixture()
	group := doc["groups"].([]any)[0].(map[string]any)
	call := group["tests"].([]any)[0].(map[string]any)["call"].(map[string]any)
	call["params"].(map[string]any)["Colour"] = "red"

	// When/Then: named and refused.
	a := loadAuthoredFixture(t, "widgets-hand", doc)
	f := loadFixture(t)
	_, err := generateAuthored(a, f.model, fixtureClient)
	if err == nil || !strings.Contains(err.Error(), "no input member Colour") {
		t.Fatalf("err = %v", err)
	}
}

func TestGenerateAuthored_refusesAClientBlockThatDisagreesWithTheModel(t *testing.T) {
	// Given: a hand-written client block with the wrong protocol.
	doc := authoredFixture()
	doc["client"].(map[string]any)["protocol"] = "restJson1"

	// When/Then: refused. The interpreters build their client from this block,
	// so a stale copy of it is a scenario that talks to the wrong wire format.
	a := loadAuthoredFixture(t, "widgets-hand", doc)
	f := loadFixture(t)
	_, err := generateAuthored(a, f.model, fixtureClient)
	if err == nil || !strings.Contains(err.Error(), "client block disagrees with the model") {
		t.Fatalf("err = %v", err)
	}
}

func TestGenerateAuthored_producesAGenerationTheEmittersCanUse(t *testing.T) {
	// Given: a well-formed authored scenario.
	a := loadAuthoredFixture(t, "widgets-hand", authoredFixture())
	f := loadFixture(t)

	// When: it is turned into a generation.
	gen, err := generateAuthored(a, f.model, fixtureClient)
	if err != nil {
		t.Fatalf("generateAuthored: %v", err)
	}

	// Then: the emit key keeps it out of the service's own generated file, and
	// the scenario file is the authored one — which is field 6 of every
	// failure message the emitted source produces.
	if gen.unit != "authored-widgets-hand" {
		t.Errorf("unit = %q", gen.unit)
	}
	if gen.file != "compat/model/authored/widgets-hand.json" {
		t.Errorf("file = %q", gen.file)
	}
	if !gen.isAuthored() || gen.describe() != "authored widgets-hand" {
		t.Errorf("describe = %q, isAuthored = %t", gen.describe(), gen.isAuthored())
	}
	if goFileName(gen.unit) != "scenarios_authored_widgets_hand_gen.go" {
		t.Errorf("go file name = %q", goFileName(gen.unit))
	}
}

func TestBuildRegistry_marksAShadowGroupAndKeepsItACandidate(t *testing.T) {
	// Given: an authored scenario whose group carries the shadow suffix.
	a := loadAuthoredFixture(t, "widgets-hand", authoredFixture())
	f := loadFixture(t)
	gen, err := generateAuthored(a, f.model, fixtureClient)
	if err != nil {
		t.Fatalf("generateAuthored: %v", err)
	}

	// When: it is projected onto the generated registry.
	reg := buildRegistry([]*generation{gen}, []string{"cli", "python-sdk"}, nil, nil)

	// Then: the entry names the native group, points at the authored file, and
	// is a candidate — a shadow gates nothing and is deleted by the flip PR.
	if len(reg.Groups) != 1 {
		t.Fatalf("groups = %d, want 1", len(reg.Groups))
	}
	g := reg.Groups[0]
	if g.Name != "widgets-hand-shadow" || g.ShadowOf != "widgets-hand" {
		t.Errorf("group = %+v", g)
	}
	if g.Scenario != "compat/model/authored/widgets-hand.json" {
		t.Errorf("scenario = %q", g.Scenario)
	}
	if g.State != generatedStateCandidate {
		t.Errorf("state = %q, want %q", g.State, generatedStateCandidate)
	}
	if g.Service != "widgets" {
		t.Errorf("service = %q — the registry records the capability key, not the emit unit", g.Service)
	}
}

// The committed authored corpus has to satisfy every rule above, and its
// registry join has to hold against the real registry.json. loadCorpus runs
// both; this is the case that fails when a hand edit breaks one.
func TestCommittedAuthoredCorpus_joinsTheHandWrittenRegistry(t *testing.T) {
	schemas, err := loadSchemas(filepath.Join(repoRoot, filepath.FromSlash(modelDir)))
	if err != nil {
		t.Fatal(err)
	}
	list, err := loadAuthored(filepath.Join(repoRoot, filepath.FromSlash(authoredDir)), schemas)
	if err != nil {
		t.Fatalf("loadAuthored: %v", err)
	}
	hand, err := loadHandRegistry(filepath.Join(repoRoot, filepath.FromSlash(handRegistryPath)))
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range list {
		if err := checkAuthoredAgainstRegistry(a, hand); err != nil {
			t.Errorf("%s: %v", a.file, err)
		}
	}
}
