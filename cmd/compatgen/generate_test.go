//go:build dev

package main

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/overcast-sh/overcast/internal/capabilities"
)

// The fixture: testdata/shapes/widgets.json models a service whose operations
// cover every emitter decision, testdata/recipes/widgets.json spreads every
// recipe role over eight resources, and the capability table below decides
// which operations count as implemented.
//
//	CreateWidget … PolishWidget     implemented or in the recipe → lifecycle tests
//	DescribeGauge, CalibrateGauge   a pre-existing resource      → read plus authored test
//	TagGauge, ListGaugeTags,
//	UntagGauge                      {TagKey, TagValue} tags      → lifecycle tags trio
//	CreateSprocket … DeleteSprocket async, list-shaped tags      → lifecycle tests
//	DescribeValve                   a second pre-existing resource → read
//	TagValve, ListValveTags,
//	UntagValve                      key-only untag list          → lifecycle tags trio
//	DescribeHub                     a third pre-existing resource → read
//	TagHub, DescribeHubTags,
//	UntagHub                        a list-shaped identifier     → lifecycle tags trio
//	SetWidgetSize                   implemented, update family   → update-without-mutable
//	ArchiveWidget                   implemented, no role         → probe-of-implemented-op
//	CreateWidget (as `spare`)       requires an unbindable cog   → setup-refused:cog
//	CreateCog                       Name unbound, but a write    → never-probe (by verb)
//	GetWidgetHistory                would bind widget.id         → probe-binds-live-resource
//	PingWidgets                     a write verb under allowProbe → probe with a responseField
//	ListCogs                        a page and a NextToken       → probe with isList on the page
//	ListGauges                      @paginated names the token   → probe with isList on the page
//	ScanWidgets                     allowProbe, token-only output → no-output-to-assert
//	GetWidgetAck                    a read that returns nothing  → no-output-to-assert
//	ListWidgetsAndCogs              two lists, no @paginated     → no-output-to-assert
//	PurgeWidgets                    a write, curated sentence    → never-probe (curated)
//	GetWidgetSecret                 a read verb that mutates     → never-probe (curated)
//	FreezeWidget                    a write, returns nothing     → never-probe (by verb, before the output check)
//	RotateWidget                    Unsupported, and a write     → never-probe (by verb)
//	SyncWidgets                     a write, nothing to see      → never-probe (by verb)
//	DescribeGizmo                   undeclared, GizmoArn unbound → unbound-required-member

func fixtureCaps() capabilityTable {
	table := capabilityTable{}
	for _, op := range []string{"CreateWidget", "GetWidget", "ListWidgets", "UpdateWidget", "DeleteWidget", "TagWidget", "UntagWidget", "ListWidgetTags", "SetWidgetSize", "ArchiveWidget"} {
		table[op] = capabilities.StatusSupported
	}
	table["RotateWidget"] = capabilities.StatusUnsupported
	return table
}

var fixtureClient = clientInfo{SDKID: "Widgets", EndpointPrefix: "widgets", SigningName: "widgets", Protocol: "awsJson1_1", APIVersion: "2026-01-01", TargetPrefix: "WidgetService"}

func modelSchemaDir(t *testing.T) string {
	t.Helper()
	return filepath.Join("..", "..", "compat", "model")
}

type fixture struct {
	schemas *schemaSet
	model   *serviceModel
	recipe  recipe
	values  *valuesTable
}

func loadFixture(t *testing.T) fixture {
	t.Helper()
	schemas, err := loadSchemas(modelSchemaDir(t))
	if err != nil {
		t.Fatalf("load schemas: %v", err)
	}
	model, err := loadModel(filepath.Join("testdata", "shapes"), "widgets")
	if err != nil {
		t.Fatalf("load fixture model: %v", err)
	}
	recipes, err := loadRecipes(filepath.Join("testdata", "recipes"), schemas)
	if err != nil {
		t.Fatalf("load fixture recipe: %v", err)
	}
	values, err := loadValues(filepath.Join("testdata", "values.json"), schemas)
	if err != nil {
		t.Fatalf("load fixture values: %v", err)
	}
	return fixture{schemas: schemas, model: model, recipe: recipes[0], values: values}
}

func generateFixture(t *testing.T) (fixture, *generation) {
	t.Helper()
	f := loadFixture(t)
	gen, err := generate(f.model, f.recipe, f.values, fixtureCaps(), fixtureClient)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	return f, gen
}

func TestGenerate_lifecycleGroupCoversEveryRole(t *testing.T) {
	// Given: the fixture recipe.
	// When: it is generated.
	_, gen := generateFixture(t)

	// Then: a lifecycle group per resource with the tests in lifecycle order
	// (create, reads, mutations, tags, authored, list, delete), and one probe
	// group with only the unimplemented operations. `spare` gets no group at
	// all: the resource it requires cannot be set up.
	want := map[string][]string{
		"widgets-gen-widget":   {"CreateWidget", "GetWidget", "DescribeWidget", "UpdateWidget", "TagWidget", "ListWidgetTags", "UntagWidget", "PolishWidget", "ListWidgets", "DeleteWidget"},
		"widgets-gen-gauge":    {"DescribeGauge", "TagGauge", "ListGaugeTags", "UntagGauge", "CalibrateGauge"},
		"widgets-gen-sprocket": {"CreateSprocket", "GetSprocket", "TagSprocket", "ListSprocketTags", "UntagSprocket", "DeleteSprocket"},
		"widgets-gen-valve":    {"DescribeValve", "TagValve", "ListValveTags", "UntagValve"},
		"widgets-gen-hub":      {"DescribeHub", "TagHub", "DescribeHubTags", "UntagHub"},
		"widgets-gen-probe":    {"ListCogs", "ListGauges", "PingWidgets"},
	}
	if len(gen.scenario.Groups) != len(want) {
		t.Fatalf("got %d groups, want %d", len(gen.scenario.Groups), len(want))
	}
	for _, g := range gen.scenario.Groups {
		var names []string
		for _, tc := range g.Tests {
			names = append(names, tc.Name)
		}
		if strings.Join(names, ",") != strings.Join(want[g.Name], ",") {
			t.Errorf("group %s tests = %v, want %v", g.Name, names, want[g.Name])
		}
	}
}

func TestGenerate_refusesWithMachineReadableReasons(t *testing.T) {
	_, gen := generateFixture(t)
	want := map[string]string{
		"ArchiveWidget":      reasonProbeOfImplementedOp,
		"CreateCog":          reasonNeverProbe,
		"CreateWidget":       reasonSetupRefused + ":cog",
		"DescribeGizmo":      reasonUnboundRequiredMember + ":GizmoArn",
		"FreezeWidget":       reasonNeverProbe,
		"GetWidgetAck":       reasonNoOutputToAssert,
		"GetWidgetHistory":   reasonProbeBindsLiveResource + ":WidgetId",
		"GetWidgetSecret":    reasonNeverProbe,
		"ListWidgetsAndCogs": reasonNoOutputToAssert,
		"PurgeWidgets":       reasonNeverProbe,
		"RotateWidget":       reasonNeverProbe,
		"ScanWidgets":        reasonNoOutputToAssert,
		"SetWidgetSize":      reasonUpdateWithoutMutable,
		"SyncWidgets":        reasonNeverProbe,
	}
	got := map[string]string{}
	for _, gp := range gen.gaps {
		got[gp.Operation] = gp.Reason
		if gp.Detail == "" || gp.Service != "widgets" {
			t.Errorf("gap %+v is missing detail or service", gp)
		}
	}
	for op, reason := range want {
		if got[op] != reason {
			t.Errorf("%s: reason %q, want %q", op, got[op], reason)
		}
	}
	if len(got) != len(want) {
		t.Errorf("gaps = %v, want exactly %v", got, want)
	}
}

func TestGenerate_probeGroupHoldsNoImplementedOperation(t *testing.T) {
	_, gen := generateFixture(t)
	caps := fixtureCaps()
	for _, g := range gen.scenario.Groups {
		if g.Kind != groupProbe {
			continue
		}
		for _, tc := range g.Tests {
			if caps.implemented(tc.Op) {
				t.Errorf("implemented operation %s sits in probe group %s", tc.Op, g.Name)
			}
		}
		// A probe asserts on the operation's own output; one that returns
		// nothing is refused rather than given a vacuous read-back. Asserted
		// on the group rather than inside a name match, so a probe that
		// stopped being emitted fails here instead of passing silently.
		var names []string
		for _, tc := range g.Tests {
			names = append(names, tc.Name)
		}
		if strings.Join(names, ",") != "ListCogs,ListGauges,PingWidgets" {
			t.Fatalf("probe group holds %v, want ListCogs, ListGauges and PingWidgets", names)
		}
		_, ping, _ := gen.scenario.findTest(g.Name, "PingWidgets")
		if a := ping.Assert[0]; a.Kind != assertResponseField || !a.Checks["$.Status"].NonEmpty {
			t.Errorf("PingWidgets probe asserts %+v, want $.Status non-empty", a)
		}
		// A probe group has no setup and no teardown, and no probe may touch
		// a resource the run owns: nothing it binds is a $ref, so there is
		// nothing for a teardown to undo.
		if len(g.Setup) != 0 || len(g.Teardown) != 0 {
			t.Errorf("probe setup/teardown = %v / %v, want neither", g.Setup, g.Teardown)
		}
		for _, tc := range g.Tests {
			if refs := refsInTest(tc); len(refs) != 0 {
				t.Errorf("probe %s binds %v, which is a live resource", tc.Name, refs)
			}
		}
	}
}

// TestGenerate_onlyProbeGroupsAreParallel pins the flag to the kind in both
// directions, in the scenario and in the registry projection an interpreter
// actually schedules from (#1801).
//
// The two halves fail differently and both matter. A lifecycle group that
// acquired the flag would have its tests raced against the exports they
// consume — a real corruption, and one that would show up as an unrelated
// flake. A probe group that lost it would still pass; it would just quietly
// give back the wall clock the flag exists to buy, which is the failure nobody
// notices.
func TestGenerate_onlyProbeGroupsAreParallel(t *testing.T) {
	_, gen := generateFixture(t)

	probes, lifecycles := 0, 0
	for _, g := range gen.scenario.Groups {
		switch g.Kind {
		case groupProbe:
			probes++
			if !g.Parallel {
				t.Errorf("probe group %s is not parallel", g.Name)
			}
		case groupLifecycle:
			lifecycles++
			if g.Parallel {
				t.Errorf("lifecycle group %s is parallel; its tests consume one another's exports", g.Name)
			}
		}
	}
	if probes == 0 || lifecycles == 0 {
		t.Fatalf("the fixture has %d probe and %d lifecycle groups; this test needs both", probes, lifecycles)
	}

	// The registry is what a loader reads, so the flag has to survive the
	// projection — a scenario-only flag would be invisible to every suite.
	fromScenario := map[string]bool{}
	for _, g := range gen.scenario.Groups {
		fromScenario[g.Name] = g.Parallel
	}
	registered := buildRegistry([]*generation{gen}, []string{"cli"}, nil, nil).Groups
	if len(registered) != len(gen.scenario.Groups) {
		t.Fatalf("registry holds %d groups, scenario holds %d", len(registered), len(gen.scenario.Groups))
	}
	for _, g := range registered {
		if g.Parallel != fromScenario[g.Name] {
			t.Errorf("registry group %s: parallel=%t, scenario says %t", g.Name, g.Parallel, fromScenario[g.Name])
		}
	}
}

// TestValidateScenario_rejectsAMisplacedParallelFlag is the belt to the
// constructor's braces: newGroupBuilder derives the flag from the kind, and a
// hand-built literal is the only way to get the two out of step.
func TestValidateScenario_rejectsAMisplacedParallelFlag(t *testing.T) {
	probe := test{Name: "ListCogs", Op: "ListCogs",
		Call:   call{Op: "ListCogs", Params: map[string]any{}},
		Assert: []assertion{responseField(checks("$.Cogs", isList()))}}

	for _, tc := range []struct {
		name     string
		kind     string
		parallel bool
	}{
		{"a lifecycle group claiming parallel", groupLifecycle, true},
		{"a probe group that lost it", groupProbe, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &scenario{Version: scenarioVersion, Service: "widgets", Groups: []group{{
				Name: "widgets-gen-probe", Kind: tc.kind, Parallel: tc.parallel, Tests: []test{probe},
			}}}
			err := validateScenario(s)
			if err == nil || !strings.Contains(err.Error(), "parallel") {
				t.Fatalf("validateScenario = %v, want a complaint about parallel", err)
			}
		})
	}
}

func TestGenerate_probeOfAnOperationThatWouldTouchALiveResourceIsRefused(t *testing.T) {
	// Given: GetWidgetHistory is a read the emulator does not implement, and
	// it takes the WidgetId the recipe binds to widget.id.
	// When: the fixture is generated.
	_, gen := generateFixture(t)

	// Then: it is refused rather than probed, and the refusal names the
	// member so a curated literal can fix it.
	got := gapIn(gen, "widgets-gen-probe", "GetWidgetHistory")
	if got.Reason != reasonProbeBindsLiveResource+":WidgetId" {
		t.Fatalf("GetWidgetHistory refusal = %q, want %s:WidgetId", got.Reason, reasonProbeBindsLiveResource)
	}
	if !strings.Contains(got.Detail, "widget.id") {
		t.Errorf("the refusal does not name the export it would have bound: %q", got.Detail)
	}
	// And an operation the recipe forbids probing outright never reaches the
	// binder: its refusal carries the curated reason.
	purge := gapIn(gen, "widgets-gen-probe", "PurgeWidgets")
	if purge.Reason != reasonNeverProbe {
		t.Fatalf("PurgeWidgets, which the recipe lists under neverProbe, was refused with %q", purge.Reason)
	}
	if !strings.Contains(purge.Detail, "cannot be undone") {
		t.Errorf("never-probe detail = %q, want the recipe's own reason", purge.Detail)
	}
}

func TestGenerate_everyTestCarriesAnAssertionAndValidates(t *testing.T) {
	f, gen := generateFixture(t)
	if err := validateScenario(gen.scenario); err != nil {
		t.Fatalf("scenario invariants: %v", err)
	}
	contents, err := encodeDocument(gen.scenario)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.schemas.validate(schemaScenario, contents); err != nil {
		t.Fatalf("generated scenario does not satisfy its schema: %v", err)
	}
	doc := gapsDocument{Version: gapsVersion, Gaps: gen.gaps}
	contents, err = encodeDocument(doc)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.schemas.validate(schemaGaps, contents); err != nil {
		t.Fatalf("generated gaps do not satisfy their schema: %v", err)
	}
}

func TestValidateScenario_rejectsAVacuousTest(t *testing.T) {
	// The constructors cannot build one; a hand-built literal can, and the
	// belt catches it.
	s := &scenario{Version: scenarioVersion, Service: "widgets", Groups: []group{{
		Name: "widgets-gen-widget", Kind: groupLifecycle,
		Tests: []test{{Name: "CreateWidget", Op: "CreateWidget", Call: call{Op: "CreateWidget", Params: map[string]any{}}}},
	}}}
	if err := validateScenario(s); err == nil || !strings.Contains(err.Error(), "no assertion clause") {
		t.Fatalf("vacuous test passed validation: %v", err)
	}
}

func TestGenerate_assertionsFollowTheContract(t *testing.T) {
	_, gen := generateFixture(t)
	_, create, _ := gen.scenario.findTest("widgets-gen-widget", "CreateWidget")
	kinds := assertionKinds(create.Assert)
	if strings.Join(kinds, ",") != "responseField,readback,listContains" {
		t.Errorf("CreateWidget asserts %v", kinds)
	}
	// Identity fields carry the model's pattern, and the read-back asserts
	// the mutable member's initial value.
	if create.Assert[0].Checks["$.WidgetId"].Matches != "^w-[0-9a-f]{8}$" {
		t.Errorf("CreateWidget identity check = %+v", create.Assert[0].Checks["$.WidgetId"])
	}
	if create.Assert[1].Checks["$.Widget.Description"].Equals != "one" {
		t.Errorf("CreateWidget read-back does not assert the initial description: %+v", create.Assert[1].Checks)
	}
	if create.Call.Params["Description"] != "one" {
		t.Errorf("mutable.from was not merged into the create params: %v", create.Call.Params)
	}

	_, update, _ := gen.scenario.findTest("widgets-gen-widget", "UpdateWidget")
	if update.Call.Params["Description"] != "two" || update.Assert[0].Kind != assertReadback || update.Assert[0].Checks["$.Widget.Description"].Equals != "two" {
		t.Errorf("UpdateWidget = %+v", update)
	}

	_, del, _ := gen.scenario.findTest("widgets-gen-widget", "DeleteWidget")
	if del.Assert[0].Kind != assertAbsent || del.Assert[0].Error == nil || del.Assert[0].Error.Code != "Widget.NotFound" || del.Assert[0].Error.Shape != "WidgetNotFound" {
		t.Errorf("DeleteWidget absence = %+v", del.Assert[0])
	}

	_, untag, _ := gen.scenario.findTest("widgets-gen-widget", "UntagWidget")
	if !untag.Assert[0].Checks["$.Tags.compat"].Missing {
		t.Errorf("UntagWidget read-back = %+v", untag.Assert[0])
	}

	// Dependencies follow exports: everything after CreateWidget consumes
	// widget.id.
	_, get, _ := gen.scenario.findTest("widgets-gen-widget", "GetWidget")
	if strings.Join(get.Depends, ",") != "CreateWidget" {
		t.Errorf("GetWidget depends = %v", get.Depends)
	}
	if len(create.Depends) != 0 {
		t.Errorf("CreateWidget depends = %v", create.Depends)
	}
}

// TestGenerate_tagShapeVariants proves detectTagShape's widening (#1909)
// directly against the values a) and b) it produces, alongside c) the
// unchanged {Key, Value} + string-untag shape `sprocket` already exercised
// before this change:
//
//	a) `gauge`'s {TagKey, TagValue} tag structure (KMS's spelling) — tag,
//	   list and untag emitted exactly as for {Key, Value}, with the list
//	   read-back path spelled in the service's own field names.
//	b) `valve`'s untag member: a list of key-only structures (ELB Classic's
//	   TagKeyOnly) instead of bare tag-key strings.
//	c) `sprocket`'s ordinary {Key, Value} tags with a plain string-list
//	   untag, unaffected by either widening.
func TestGenerate_tagShapeVariants(t *testing.T) {
	_, gen := generateFixture(t)

	// a) gauge: {TagKey, TagValue} tag structure, plain string-list untag.
	_, tag, ok := gen.scenario.findTest("widgets-gen-gauge", "TagGauge")
	if !ok {
		t.Fatal("TagGauge was not generated")
	}
	tags, _ := tag.Call.Params["Tags"].([]any)
	if len(tags) != 1 {
		t.Fatalf("TagGauge Tags = %+v, want one entry", tag.Call.Params["Tags"])
	}
	if entry, ok := tags[0].(map[string]any); !ok || entry["TagKey"] != "compat" || entry["TagValue"] != "scenario" {
		t.Errorf("TagGauge Tags[0] = %+v, want {TagKey: compat, TagValue: scenario}", tags[0])
	}
	if tag.Assert[0].Kind != assertListContains || tag.Assert[0].Where["$.TagKey"] != "compat" || tag.Assert[0].Where["$.TagValue"] != "scenario" {
		t.Errorf("TagGauge asserts %+v, want a listContains on {$.TagKey, $.TagValue}", tag.Assert[0])
	}

	_, list, ok := gen.scenario.findTest("widgets-gen-gauge", "ListGaugeTags")
	if !ok {
		t.Fatal("ListGaugeTags was not generated")
	}
	if list.Assert[0].Kind != assertListContains || list.Assert[0].ItemsPath != "$.Tags" {
		t.Errorf("ListGaugeTags asserts %+v, want listContains on $.Tags", list.Assert[0])
	}

	_, untag, ok := gen.scenario.findTest("widgets-gen-gauge", "UntagGauge")
	if !ok {
		t.Fatal("UntagGauge was not generated")
	}
	if got := untag.Call.Params["TagKeys"]; !equalJSON(got, []any{"compat"}) {
		t.Errorf("UntagGauge TagKeys = %+v, want a plain string list [\"compat\"]", got)
	}
	if untag.Assert[0].Kind != assertAbsent || untag.Assert[0].ItemsPath != "$.Tags" || untag.Assert[0].Where["$.TagKey"] != "compat" {
		t.Errorf("UntagGauge asserts %+v, want absentFromList on $.Tags where $.TagKey", untag.Assert[0])
	}

	// b) valve: ordinary {Key, Value} tags, but the untag member takes a
	// list of key-only structures instead of bare strings.
	_, valveTag, ok := gen.scenario.findTest("widgets-gen-valve", "TagValve")
	if !ok {
		t.Fatal("TagValve was not generated")
	}
	valveTags, _ := valveTag.Call.Params["Tags"].([]any)
	if entry, ok := valveTags[0].(map[string]any); !ok || entry["Key"] != "compat" || entry["Value"] != "scenario" {
		t.Errorf("TagValve Tags[0] = %+v, want {Key: compat, Value: scenario}", valveTags[0])
	}

	_, valveUntag, ok := gen.scenario.findTest("widgets-gen-valve", "UntagValve")
	if !ok {
		t.Fatal("UntagValve was not generated")
	}
	if got := valveUntag.Call.Params["TagKeys"]; !equalJSON(got, []any{map[string]any{"Key": "compat"}}) {
		t.Errorf("UntagValve TagKeys = %+v, want a key-only structure list [{Key: compat}]", got)
	}
	if valveUntag.Assert[0].Kind != assertAbsent || valveUntag.Assert[0].Where["$.Key"] != "compat" {
		t.Errorf("UntagValve asserts %+v, want absentFromList where $.Key", valveUntag.Assert[0])
	}

	// c) sprocket: the pre-existing {Key, Value} tag structure paired with a
	// plain string-list untag, unaffected by either widening.
	_, sprocketTag, ok := gen.scenario.findTest("widgets-gen-sprocket", "TagSprocket")
	if !ok {
		t.Fatal("TagSprocket was not generated")
	}
	sprocketTags, _ := sprocketTag.Call.Params["Tags"].([]any)
	if entry, ok := sprocketTags[0].(map[string]any); !ok || entry["Key"] != "compat" || entry["Value"] != "scenario" {
		t.Errorf("TagSprocket Tags[0] = %+v, want {Key: compat, Value: scenario}", sprocketTags[0])
	}
	_, sprocketUntag, ok := gen.scenario.findTest("widgets-gen-sprocket", "UntagSprocket")
	if !ok {
		t.Fatal("UntagSprocket was not generated")
	}
	if got := sprocketUntag.Call.Params["TagKeys"]; !equalJSON(got, []any{"compat"}) {
		t.Errorf("UntagSprocket TagKeys = %+v, want a plain string list [\"compat\"]", got)
	}
}

// TestGenerate_listShapedIdentifierIsBoundByWrapping proves binding rule 1's
// one level of list wrapping (#1923) on `hub`, whose TagHub, DescribeHubTags
// and UntagHub all take the identifier as HubNames — the plural list, as ELB
// Classic's AddTags, DescribeTags and RemoveTags do — while the resource
// exports only the singular name. Every one of the three carries the same
// one-element list holding the resource's own $ref, and the tag role's three
// tests are generated rather than refused unbound-required-member:HubNames.
func TestGenerate_listShapedIdentifierIsBoundByWrapping(t *testing.T) {
	_, gen := generateFixture(t)

	wrapped := []any{map[string]any{"$ref": "hub.name"}}
	for _, op := range []string{"TagHub", "DescribeHubTags", "UntagHub"} {
		_, tc, ok := gen.scenario.findTest("widgets-gen-hub", op)
		if !ok {
			t.Fatalf("%s was not generated", op)
		}
		if got := tc.Call.Params["HubNames"]; !equalJSON(got, wrapped) {
			t.Errorf("%s HubNames = %+v, want %+v", op, got, wrapped)
		}
	}

	// The tag clause reads the listing back through the same wrapped bind,
	// at the indexed path the recipe names.
	_, tag, _ := gen.scenario.findTest("widgets-gen-hub", "TagHub")
	if tag.Assert[0].Kind != assertListContains || tag.Assert[0].ItemsPath != "$.TagDescriptions[0].Tags" {
		t.Errorf("TagHub asserts %+v, want listContains on $.TagDescriptions[0].Tags", tag.Assert[0])
	}
	if got := tag.Assert[0].Call.Params["HubNames"]; !equalJSON(got, wrapped) {
		t.Errorf("TagHub read-back HubNames = %+v, want %+v", got, wrapped)
	}

	// Nothing about the wrap changes the tag structure or the untag list:
	// `hub` moves only the identifier axis.
	if got := tag.Call.Params["Tags"]; !equalJSON(got, []any{map[string]any{"Key": "compat", "Value": "scenario"}}) {
		t.Errorf("TagHub Tags = %+v, want [{Key: compat, Value: scenario}]", got)
	}
	_, untag, _ := gen.scenario.findTest("widgets-gen-hub", "UntagHub")
	if got := untag.Call.Params["TagKeys"]; !equalJSON(got, []any{"compat"}) {
		t.Errorf("UntagHub TagKeys = %+v, want a plain string list [\"compat\"]", got)
	}
}

// TestBinder_listWrapIsCheckedAgainstTheModel proves the two halves of the
// wrap's contract: a wrapped bind whose element kind matches the export is
// bound, and one the model contradicts is an error naming the member rather
// than a refusal — the same treatment a mistyped literal in `params` gets.
func TestBinder_listWrapIsCheckedAgainstTheModel(t *testing.T) {
	f := loadFixture(t)
	hub := resourceByID(t, f.recipe, "hub")
	b := &binder{model: f.model, service: "widgets", values: f.values}
	scope := bindScope{resources: []resource{hub}, exports: exportKinds{"hub.name": "string"}}

	params, ref, err := b.bind("widgets-gen-hub", "DescribeHubTags", nil, scope)
	if err != nil || ref != nil {
		t.Fatalf("bind: ref=%v err=%v", ref, err)
	}
	if got := params["HubNames"]; !equalJSON(got, []any{map[string]any{"$ref": "hub.name"}}) {
		t.Fatalf("HubNames = %+v, want a one-element list holding $ref hub.name", got)
	}

	// The same wrap aimed at a member the model calls a plain string. It is
	// the recipe contradicting the model, so it is an error naming the
	// member, raised where every other value is checked.
	scalar := hub
	scalar.Binds = map[string]bindRef{"WidgetId": {Ref: "hub.name", List: true}}
	scope.resources = []resource{scalar}
	scope.exports = exportKinds{"hub.name": "string"}
	if _, _, err := b.bind("widgets-gen-hub", "GetWidget", nil, scope); err == nil || !strings.Contains(err.Error(), "wants a string, got a list") {
		t.Fatalf("err = %v, want it to say the member wants a string", err)
	}
}

// TestBindRef_decodesBothForms covers the decoder directly, including the two
// shapes the schema already refuses. Both gates are wanted: the schema is what
// a recipe author sees, and the decoder is what protects a recipe reached any
// other way.
func TestBindRef_decodesBothForms(t *testing.T) {
	cases := []struct {
		name, body string
		want       bindRef
		errText    string
	}{
		{name: "a bare path", body: `"widget.id"`, want: bindRef{Ref: "widget.id"}},
		{name: "a wrapped path", body: `["widget.id"]`, want: bindRef{Ref: "widget.id", List: true}},
		{name: "two paths", body: `["a.id","b.id"]`, errText: "exactly one context path, not 2"},
		{name: "neither", body: `7`, errText: "a context path, or a one-element list"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got bindRef
			err := json.Unmarshal([]byte(tc.body), &got)
			if tc.errText != "" {
				if err == nil || !strings.Contains(err.Error(), tc.errText) {
					t.Fatalf("err = %v, want it to mention %q", err, tc.errText)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("got %+v (err %v), want %+v", got, err, tc.want)
			}
		})
	}
}

func resourceByID(t *testing.T, r recipe, id string) resource {
	t.Helper()
	for _, res := range r.Resources {
		if res.ID == id {
			return res
		}
	}
	t.Fatalf("fixture recipe has no resource %q", id)
	return resource{}
}

// equalJSON compares two decoded-JSON values (map[string]any / []any /
// scalars) for equality by round-tripping both through json.Marshal, which
// is simpler and just as exact as a hand-rolled deep-equal for this shape.
func equalJSON(a, b any) bool {
	ab, aerr := json.Marshal(a)
	bb, berr := json.Marshal(b)
	return aerr == nil && berr == nil && string(ab) == string(bb)
}

func assertionKinds(clauses []assertion) []string {
	var kinds []string
	for _, a := range clauses {
		kinds = append(kinds, a.Kind)
	}
	return kinds
}

func TestGenerate_isByteDeterministic(t *testing.T) {
	f := loadFixture(t)
	var renderings [][]byte
	for i := 0; i < 3; i++ {
		gen, err := generate(f.model, f.recipe, f.values, fixtureCaps(), fixtureClient)
		if err != nil {
			t.Fatal(err)
		}
		contents, err := encodeDocument(gen.scenario)
		if err != nil {
			t.Fatal(err)
		}
		renderings = append(renderings, contents)
	}
	for i := 1; i < len(renderings); i++ {
		if !bytes.Equal(renderings[0], renderings[i]) {
			t.Fatalf("run %d differs from run 0", i)
		}
	}
	if !bytes.HasSuffix(renderings[0], []byte("\n")) || bytes.Contains(renderings[0], []byte("\r")) {
		t.Fatal("output must end in one LF and contain no CR")
	}
}

func TestBinder_rules(t *testing.T) {
	f := loadFixture(t)
	widget := f.recipe.Resources[0]
	b := &binder{model: f.model, service: "widgets", values: f.values}
	exports := exportKinds{"widget.id": "string"}
	scope := bindScope{resources: []resource{widget}, exports: exports}

	cases := []struct {
		name     string
		op       string
		explicit map[string]any
		values   *valuesTable
		wantKey  string
		want     any
		refusal  string
		errText  string
	}{
		{name: "rule 1: explicit bind", op: "GetWidget", wantKey: "WidgetId", want: map[string]any{"$ref": "widget.id"}},
		{name: "rule 3: curated literal", op: "RotateWidget", values: &valuesTable{Services: map[string]serviceValues{"widgets": {Operations: map[string]map[string]any{"RotateWidget": {"Angle": json.Number("90")}}}}}, wantKey: "Angle", want: json.Number("90")},
		{name: "rule 4: range minimum", op: "SetWidgetSize", wantKey: "Size", want: json.Number("1")},
		{name: "rule 5: refuse an unconstrained string", op: "DescribeGizmo", refusal: reasonUnboundRequiredMember + ":GizmoArn"},
		{name: "explicit params are checked against the model", op: "CreateWidget", explicit: map[string]any{"Name": "Not Legal!"}, errText: "does not match"},
		{name: "an unknown member is an error, not a refusal", op: "CreateWidget", explicit: map[string]any{"Title": "x"}, errText: "no input member"},
		{name: "a literal of the wrong kind is an error", op: "SetWidgetSize", explicit: map[string]any{"Size": "big"}, errText: "wants a number"},
		{name: "a literal outside the range is an error", op: "SetWidgetSize", explicit: map[string]any{"Size": json.Number("500")}, errText: "above"},
		{name: "an enum value is checked", op: "CreateWidget", explicit: map[string]any{"Name": "ok", "Color": "green"}, errText: "not one of"},
		{name: "a $name that cannot fit is an error", op: "CreateWidget", explicit: map[string]any{"Name": map[string]any{"$name": strings.Repeat("x", 60)}}, errText: "modeled maximum"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b.values = f.values
			if tc.values != nil {
				b.values = tc.values
			}
			params, ref, err := b.bind("widgets-gen-widget", tc.op, tc.explicit, scope)
			switch {
			case tc.errText != "":
				if err == nil || !strings.Contains(err.Error(), tc.errText) {
					t.Fatalf("err = %v, want it to mention %q", err, tc.errText)
				}
			case tc.refusal != "":
				if err != nil || ref == nil || ref.Reason != tc.refusal {
					t.Fatalf("got params=%v ref=%v err=%v, want refusal %s", params, ref, err, tc.refusal)
				}
			default:
				if err != nil || ref != nil {
					t.Fatalf("bind: ref=%v err=%v", ref, err)
				}
				got, _ := json.Marshal(params[tc.wantKey])
				want, _ := json.Marshal(tc.want)
				if string(got) != string(want) {
					t.Fatalf("%s = %s, want %s", tc.wantKey, got, want)
				}
			}
		})
	}
}

func TestBinder_recordsAutomaticNameMatch(t *testing.T) {
	f := loadFixture(t)
	widget := f.recipe.Resources[0]
	widget.Binds = nil                                           // no rule-1 bind…
	widget.Exports = map[string]string{"WidgetId": "$.WidgetId"} // …but an export named like the member
	b := &binder{model: f.model, service: "widgets", values: f.values}
	scope := bindScope{resources: []resource{widget}, exports: exportKinds{"widget.WidgetId": "string"}}
	params, ref, err := b.bind("g", "GetWidget", nil, scope)
	if err != nil || ref != nil {
		t.Fatalf("bind: %v %v", ref, err)
	}
	if got := params["WidgetId"].(map[string]any)["$ref"]; got != "widget.WidgetId" {
		t.Fatalf("bound to %v", got)
	}
	if len(b.auto) != 1 || b.auto[0].Member != "WidgetId" {
		t.Fatalf("automatic binding not recorded: %+v", b.auto)
	}
}

func TestValues_rejectsAnExpression(t *testing.T) {
	f := loadFixture(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "values.json")
	writeFile(t, path, `{"version":1,"services":{"widgets":{"members":{"Angle":{"$ref":"widget.id"}}}}}`)
	if _, err := loadValues(path, f.schemas); err == nil || !strings.Contains(err.Error(), "values.schema.json") {
		t.Fatalf("an expression in values.json loaded: %v", err)
	}
}

func TestRecipe_rejectsUnknownFieldsAndBadReferences(t *testing.T) {
	f := loadFixture(t)
	cases := []struct{ name, body, want string }{
		{"unknown field", `{"service":"w","resources":[{"id":"a","create":{"op":"CreateWidget"},"colour":"blue"}]}`, "recipe.schema.json"},
		{"unknown expression", `{"service":"w","resources":[{"id":"a","create":{"op":"CreateWidget","params":{"Name":{"$todo":"x"}}}}]}`, "recipe.schema.json"},
		{"bad path", `{"service":"w","resources":[{"id":"a","create":{"op":"CreateWidget"},"exports":{"id":"WidgetId"}}]}`, "recipe.schema.json"},
		{"unknown requirement", `{"service":"w","resources":[{"id":"a","requires":["b"],"create":{"op":"CreateWidget"}}]}`, "unknown resource"},
		{"identity not exported", `{"service":"w","resources":[{"id":"a","create":{"op":"CreateWidget"},"read":{"op":"GetWidget","identityPath":"$.Widget.WidgetId","identity":"nope"}}]}`, "not an export"},
		{"authored op without assertion", `{"service":"w","resources":[{"id":"a","create":{"op":"CreateWidget"},"operations":[{"op":"PingWidgets","assert":[]}]}]}`, "recipe.schema.json"},
		{"bind that is neither a path nor a list", `{"service":"w","resources":[{"id":"a","create":{"op":"CreateWidget"},"binds":{"WidgetId":7}}]}`, "recipe.schema.json"},
		{"list bind holding more than one path", `{"service":"w","resources":[{"id":"a","create":{"op":"CreateWidget"},"binds":{"WidgetIds":["a.id","b.id"]}}]}`, "recipe.schema.json"},
		{"list bind holding a non-path", `{"service":"w","resources":[{"id":"a","create":{"op":"CreateWidget"},"binds":{"WidgetIds":["WidgetId"]}}]}`, "recipe.schema.json"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "w.json")
			writeFile(t, path, tc.body)
			_, err := loadRecipe(path, f.schemas)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestGenerate_recipeContradictingTheModelIsAnError(t *testing.T) {
	f := loadFixture(t)
	cases := []struct {
		name   string
		mutate func(r *recipe)
		want   string
	}{
		{"unknown operation", func(r *recipe) { r.Resources[0].Delete = &recipeCall{Op: "DestroyWidget"} }, "does not model"},
		{"unknown export path", func(r *recipe) { r.Resources[0].Exports = map[string]string{"id": "$.Widget.Id"} }, "no member"},
		{"notFound naming an error the read does not raise", func(r *recipe) { r.Resources[0].NotFound = &notFoundSpec{Error: "WidgetExists"} }, "declares exactly one not-found error, WidgetNotFound"},
		{"literal of the wrong kind for the read path", func(r *recipe) { r.Resources[0].Mutable[0].To = json.Number("2") }, "wants a string"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := f.recipe
			r.Resources = []resource{f.recipe.Resources[0]}
			tc.mutate(&r)
			_, err := generate(f.model, r, f.values, fixtureCaps(), fixtureClient)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
}

// resourceNamed is the fixture recipe's resource with that id.
func (f fixture) resourceNamed(t *testing.T, id string) resource {
	t.Helper()
	for _, res := range f.recipe.Resources {
		if res.ID == id {
			return res
		}
	}
	t.Fatalf("the fixture recipe has no resource %q", id)
	return resource{}
}

// gapIn is the refusal recorded for op in one group, or the zero gap. The
// group matters: an operation refused a role in its lifecycle group is
// uncovered, so the probe group refuses it a second time for its own reason.
func gapIn(gen *generation, group, op string) gap {
	for _, gp := range gen.gaps {
		if gp.Group == group && gp.Operation == op {
			return gp
		}
	}
	return gap{}
}

func TestGenerate_authoredCreateAssertionMustCallTheServiceAgain(t *testing.T) {
	// Given: sprocket's authored create assertion, which normally reads the
	// sprocket back, reduced to a clause that only inspects the create's own
	// response.
	f := loadFixture(t)
	gauge, sprocket := f.resourceNamed(t, "gauge"), f.resourceNamed(t, "sprocket")
	create := *sprocket.Create
	create.Assert = []assertion{responseField(checks("$.SprocketId", nonEmpty()))}
	sprocket.Create = &create
	r := f.recipe
	r.Resources = []resource{gauge, sprocket}

	// When: it is generated.
	gen, err := generate(f.model, r, f.values, fixtureCaps(), fixtureClient)
	if err != nil {
		t.Fatal(err)
	}

	// Then: the create is refused. Restating the create's own response is
	// not a read-back, however many clauses say it.
	if got := gapIn(gen, "widgets-gen-sprocket", "CreateSprocket"); got.Reason != reasonNoReadbackPath {
		t.Fatalf("CreateSprocket refusal = %q, want %s", got.Reason, reasonNoReadbackPath)
	}
	// And the committed fixture, whose clause does call the service again,
	// is generated — with the clause wrapped in the resource's async budget,
	// and the response-field clause beside it left alone.
	_, full := generateFixture(t)
	_, createTest, ok := full.scenario.findTest("widgets-gen-sprocket", "CreateSprocket")
	if !ok {
		t.Fatal("the authored create assertion did not produce a test")
	}
	if kinds := assertionKinds(createTest.Assert); strings.Join(kinds, ",") != "responseField,eventually" {
		t.Fatalf("CreateSprocket asserts %v, want the authored read-back wrapped in eventually", kinds)
	}
	inner := createTest.Assert[1]
	if inner.MaxAttempts != 3 || inner.Assert == nil || inner.Assert.Kind != assertReadback || inner.Assert.Call.Op != "GetSprocket" {
		t.Errorf("the authored clause was wrapped as %+v", inner)
	}
	// A resource that declares no async keeps its authored clause verbatim.
	_, polish, _ := full.scenario.findTest("widgets-gen-widget", "PolishWidget")
	if polish.Assert[0].Kind != assertReadback {
		t.Errorf("PolishWidget asserts %v, want the authored read-back unwrapped", assertionKinds(polish.Assert))
	}
}

func TestGenerate_authoredUpdateMustCallTheServiceAgain(t *testing.T) {
	// Given: SetWidgetSize — an update-family operation with no `mutable`
	// entry — authored with a clause that reads only its own response.
	f := loadFixture(t)
	widget := f.resourceNamed(t, "widget")
	echo := authoredOp{Op: "SetWidgetSize", Params: map[string]any{"Size": json.Number("50")},
		Assert: []assertion{responseField(checks("$.WidgetId", nonEmpty()))}}
	widget.Operations = append(append([]authoredOp(nil), widget.Operations...), echo)
	r := f.recipe
	r.Resources = []resource{widget}

	// When: it is generated.
	gen, err := generate(f.model, r, f.values, fixtureCaps(), fixtureClient)
	if err != nil {
		t.Fatal(err)
	}

	// Then: authored coverage does not buy its way past guard 3.
	if got := gapIn(gen, "widgets-gen-widget", "SetWidgetSize"); got.Reason != reasonUpdateWithoutReadback {
		t.Fatalf("SetWidgetSize refusal = %q, want %s", got.Reason, reasonUpdateWithoutReadback)
	}
	if _, _, found := gen.scenario.findTest("widgets-gen-widget", "SetWidgetSize"); found {
		t.Error("the refused operation was emitted anyway")
	}

	// And: the same operation with a read-back is generated, so the guard
	// refuses the anti-pattern rather than authored updates as such.
	real := echo
	real.Assert = []assertion{readback(call{Op: "GetWidget"}, checks("$.Widget.Size", equals(json.Number("50"))))}
	widget.Operations[len(widget.Operations)-1] = real
	r.Resources = []resource{widget}
	gen, err = generate(f.model, r, f.values, fixtureCaps(), fixtureClient)
	if err != nil {
		t.Fatal(err)
	}
	if _, tc, found := gen.scenario.findTest("widgets-gen-widget", "SetWidgetSize"); !found {
		t.Fatal("an authored update with a read-back was refused")
	} else if tc.Assert[0].Kind != assertReadback || tc.Assert[0].Call.Params["WidgetId"] == nil {
		t.Errorf("SetWidgetSize asserts %+v", tc.Assert[0])
	}
}

func TestGenerate_unsupportedTagShapeNamesTheShapeTheSchemaAllows(t *testing.T) {
	// Given: a tag member whose target is neither a string map nor a list of
	// {Key, Value} — here a plain string, whose Smithy id is qualified.
	f := loadFixture(t)
	widget := f.resourceNamed(t, "widget")
	tags := *widget.Tags
	tags.Tag = tagCall{Op: "UpdateWidget", Member: "Description"}
	widget.Tags = &tags
	r := f.recipe
	r.Resources = []resource{widget}

	// When: it is generated.
	gen, err := generate(f.model, r, f.values, fixtureCaps(), fixtureClient)
	if err != nil {
		t.Fatal(err)
	}

	// Then: the reason carries the bare shape name, the detail carries the
	// qualified id, and the gap report still satisfies its schema — the
	// reason pattern has no room for a '#'.
	got := gapIn(gen, "widgets-gen-widget", "ListWidgetTags")
	if got.Reason != reasonUnsupportedTagShape+":String" {
		t.Fatalf("refusal reason = %q, want %s:String", got.Reason, reasonUnsupportedTagShape)
	}
	if !strings.Contains(got.Detail, "smithy.api#String") {
		t.Errorf("the detail drops the qualified id: %q", got.Detail)
	}
	contents, err := encodeDocument(gapsDocument{Version: gapsVersion, Gaps: gen.gaps})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.schemas.validate(schemaGaps, contents); err != nil {
		t.Fatalf("the refusal does not satisfy gaps.schema.json: %v", err)
	}
}

func TestGenerate_refusesUpdateWhoseReadConsumes(t *testing.T) {
	f := loadFixture(t)
	r := f.recipe
	res := f.recipe.Resources[0]
	read := *res.Read
	read.Consuming = true
	res.Read = &read
	res.NotFound = nil
	r.Resources = []resource{res}
	gen, err := generate(f.model, r, f.values, fixtureCaps(), fixtureClient)
	if err != nil {
		t.Fatal(err)
	}
	var reasons []string
	for _, gp := range gen.gaps {
		if gp.Operation == "UpdateWidget" {
			reasons = append(reasons, gp.Reason)
		}
	}
	if strings.Join(reasons, ",") != reasonNoReadbackPath {
		t.Fatalf("UpdateWidget reasons = %v, want %s", reasons, reasonNoReadbackPath)
	}
}

// TestGenerate_probeNeverAssertsAPaginationToken pins the rule that a probe of
// a list operation asserts the page and never the token. A `NextToken` is
// absent from a single-page answer — the answer real AWS gives most of the
// time — so `nonEmpty` on it is false by construction, which §3.10 does not
// allow. The fixture covers both ways a token is recognised and the case where
// excluding it leaves nothing to assert.
func TestGenerate_probeNeverAssertsAPaginationToken(t *testing.T) {
	f, gen := generateFixture(t)

	// ListCogs returns {Cogs, NextToken} and declares no @paginated trait:
	// the member name alone rules the token out, and the sole list is what is
	// left to assert.
	cogsGroup, cogs, ok := gen.scenario.findTest("widgets-gen-probe", "ListCogs")
	if !ok {
		t.Fatal("ListCogs was not probed")
	}
	if len(cogs.Assert[0].Checks) != 1 || !cogs.Assert[0].Checks["$.Cogs"].IsList {
		t.Errorf("ListCogs probe asserts %+v, want isList on $.Cogs", cogs.Assert[0].Checks)
	}
	// isList holds for an omitted page as well as an empty one — several AWS
	// services (SQS's ListQueues among them) omit an empty list member
	// instead of serializing [] — so -explain must render it that way rather
	// than requiring the member to be present.
	if rendered := renderPython(renderEnv{}, gen.scenario, cogsGroup, cogs); !strings.Contains(rendered, "is absent or a list") {
		t.Errorf("explain rendering of ListCogs does not accept an absent page:\n%s", rendered)
	}

	// ListGauges names its token `Cursor`, which no name rule would catch:
	// the @paginated trait is what rules it out, and the trait's `items` is
	// what names the page.
	_, gauges, ok := gen.scenario.findTest("widgets-gen-probe", "ListGauges")
	if !ok {
		t.Fatal("ListGauges was not probed")
	}
	if len(gauges.Assert[0].Checks) != 1 || !gauges.Assert[0].Checks["$.Gauges"].IsList {
		t.Errorf("ListGauges probe asserts %+v, want isList on $.Gauges", gauges.Assert[0].Checks)
	}
	if identityMember(f.model, "ListGauges", "ListGaugesResponse") != "" {
		t.Error("the trait-named token Cursor was offered as an identity")
	}

	// An output that is nothing but a token has nothing left once the token
	// is excluded, so the operation is refused rather than probed.
	scan := gapIn(gen, "widgets-gen-probe", "ScanWidgets")
	if scan.Reason != reasonNoOutputToAssert {
		t.Fatalf("ScanWidgets refusal = %q, want %s", scan.Reason, reasonNoOutputToAssert)
	}
	if !strings.Contains(scan.Detail, "pagination token") {
		t.Errorf("the refusal does not say why the token was not enough: %q", scan.Detail)
	}
	if _, _, found := gen.scenario.findTest("widgets-gen-probe", "ScanWidgets"); found {
		t.Error("a token-only output was probed anyway")
	}

	// And nothing anywhere in the generated scenario asserts a token.
	for _, g := range gen.scenario.Groups {
		for _, tc := range g.Tests {
			for i, a := range tc.Assert {
				for path := range a.Checks {
					if strings.HasSuffix(path, "Token") || strings.HasSuffix(path, "Marker") || strings.HasSuffix(path, "Cursor") {
						t.Errorf("%s/%s assert[%d] checks %s, which is a pagination token", g.Name, tc.Name, i, path)
					}
				}
			}
		}
	}
}

// TestIdentityMember_skipsTokensAndLists is the unit-level half: the same rule
// stated over the fixture model, including the fallbacks, which used to return
// whatever member sorted first.
func TestIdentityMember_skipsTokensAndLists(t *testing.T) {
	f := loadFixture(t)
	cases := []struct{ op, output, want string }{
		{"PingWidgets", "PingWidgetsResponse", "Status"},
		{"ListWidgets", "ListWidgetsResponse", ""},             // one list, no token
		{"ListCogs", "ListCogsResponse", ""},                   // token by name
		{"ListGauges", "ListGaugesResponse", ""},               // token by @paginated
		{"ScanWidgets", "ScanWidgetsResponse", ""},             // nothing but a token
		{"CreateWidget", "CreateWidgetResponse", "Arn"},        // suffix preference is unchanged
		{"DescribeWidget", "DescribeWidgetResponse", "Widget"}, // the last-resort member is still offered
	}
	for _, tc := range cases {
		if got := identityMember(f.model, tc.op, tc.output); got != tc.want {
			t.Errorf("identityMember(%s) = %q, want %q", tc.op, got, tc.want)
		}
	}
	if got := listMember(f.model, "ListGauges", "ListGaugesResponse"); got != "Gauges" {
		t.Errorf("listMember(ListGauges) = %q, want Gauges (the @paginated items)", got)
	}
	if got := listMember(f.model, "ListCogs", "ListCogsResponse"); got != "Cogs" {
		t.Errorf("listMember(ListCogs) = %q, want Cogs (the sole list member)", got)
	}
	if got := listMember(f.model, "ScanWidgets", "ScanWidgetsResponse"); got != "" {
		t.Errorf("listMember(ScanWidgets) = %q, want none", got)
	}
}

// ---------------------------------------------------------------------------
// Model-derived recipe fields (#1795 B1)
// ---------------------------------------------------------------------------

// TestGenerate_derivesNotFoundAndItemsPathFromTheModel is the whole claim in
// one assertion: strip both fields from every resource in the fixture recipe
// and the generated scenario is byte-for-byte the one the explicit values
// produce. Derivation and override are therefore the same answer, which is
// what let the pilot recipes drop the lines.
func TestGenerate_derivesNotFoundAndItemsPathFromTheModel(t *testing.T) {
	// Given: the fixture recipe, which spells both fields out.
	f := loadFixture(t)
	authored, err := generate(f.model, f.recipe, f.values, fixtureCaps(), fixtureClient)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}

	// When: every derivable value is removed.
	stripped := f.recipe
	stripped.Resources = nil
	for _, res := range f.recipe.Resources {
		res.NotFound = nil
		if res.List != nil {
			list := *res.List
			list.ItemsPath = ""
			res.List = &list
		}
		stripped.Resources = append(stripped.Resources, res)
	}
	derived, err := generate(f.model, stripped, f.values, fixtureCaps(), fixtureClient)
	if err != nil {
		t.Fatalf("generate from the stripped recipe: %v", err)
	}

	// Then: the same scenario, and the same gaps.
	if a, b := encoded(t, authored.scenario), encoded(t, derived.scenario); !bytes.Equal(a, b) {
		t.Errorf("the derived scenario differs from the authored one:\n%s\n%s", a, b)
	}
	if a, b := encoded(t, authored.gaps), encoded(t, derived.gaps); !bytes.Equal(a, b) {
		t.Errorf("the derived gaps differ from the authored ones:\n%s\n%s", a, b)
	}

	// And: the derivation reached the fields, rather than the two runs
	// agreeing because neither emitted a delete or a list.
	_, del, ok := derived.scenario.findTest("widgets-gen-widget", "DeleteWidget")
	if !ok || del.Assert[0].Error == nil || del.Assert[0].Error.Shape != "WidgetNotFound" {
		t.Errorf("DeleteWidget asserts %+v, want absence by WidgetNotFound", del.Assert)
	}
	_, list, ok := derived.scenario.findTest("widgets-gen-widget", "ListWidgets")
	if !ok || list.Assert[0].ItemsPath != "$.Widgets" {
		t.Errorf("ListWidgets searches %q, want $.Widgets", list.Assert[0].ItemsPath)
	}
}

func TestGenerate_refusesAListWhosePageTheModelDoesNotSettle(t *testing.T) {
	cases := []struct {
		name string
		op   string
		want string
	}{
		{"two lists and no items trait", "ListWidgetsAndCogs", "2 list members (Cogs, Widgets) and no @paginated `items` trait"},
		{"no list at all", "ScanWidgets", "no list member at all"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Given: the widget's list pointed at that operation, with no
			// itemsPath for the recipe to fall back on.
			f := loadFixture(t)
			widget := f.resourceNamed(t, "widget")
			widget.List = &listSpec{Op: tc.op, IdentityPath: "$.WidgetId", Identity: "id"}
			r := f.recipe
			r.Resources = []resource{widget}

			// When: it is generated.
			gen, err := generate(f.model, r, f.values, fixtureCaps(), fixtureClient)
			if err != nil {
				t.Fatalf("generate: %v", err)
			}

			// Then: the list is refused, naming what the model does say, and
			// no test searches a page the generator had to guess at.
			got := gapIn(gen, "widgets-gen-widget", tc.op)
			if got.Reason != reasonAmbiguousListPage || !strings.Contains(got.Detail, tc.want) {
				t.Fatalf("gap = %+v, want %s naming %q", got, reasonAmbiguousListPage, tc.want)
			}
			if !strings.Contains(got.Detail, "list.itemsPath") {
				t.Errorf("detail %q does not say how to fix it", got.Detail)
			}
			if _, _, ok := gen.scenario.findTest("widgets-gen-widget", tc.op); ok {
				t.Errorf("%s got a test anyway", tc.op)
			}
		})
	}
}

// TestModelDerivations_readTheModelAndNothingElse pins the two rules against
// the fixture model directly, so a change to either is visible as itself
// rather than as a scenario diff.
func TestModelDerivations_readTheModelAndNothingElse(t *testing.T) {
	f := loadFixture(t)

	t.Run("not-found errors", func(t *testing.T) {
		cases := []struct {
			op   string
			want []string
		}{
			{"GetWidget", []string{"WidgetNotFound"}},
			{"GetSprocket", []string{"SprocketNotFound"}},
			{"CreateWidget", nil},  // WidgetExists is not not-found-shaped
			{"DescribeGauge", nil}, // declares no errors at all
		}
		for _, tc := range cases {
			if got := derivableNotFoundErrors(f.model, tc.op); strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Errorf("%s: %v, want %v", tc.op, got, tc.want)
			}
		}
	})

	t.Run("list page", func(t *testing.T) {
		cases := []struct {
			op   string
			want string
		}{
			{"ListWidgets", "Widgets"}, // the sole list member
			{"ListCogs", "Cogs"},       // the sole list beside a token
			{"ListGauges", "Gauges"},   // @paginated names it
			{"ListWidgetsAndCogs", ""}, // two lists, no trait
			{"ScanWidgets", ""},        // no list at all
		}
		for _, tc := range cases {
			got, _ := listPageMember(f.model, tc.op, f.model.OutputShape(tc.op))
			if got != tc.want {
				t.Errorf("%s: %q, want %q", tc.op, got, tc.want)
			}
		}
	})
}

// encoded renders a generated document the way the generator writes it, so a
// test can compare two of them byte for byte.
func encoded(t *testing.T, value any) []byte {
	t.Helper()
	contents, err := encodeDocument(value)
	if err != nil {
		t.Fatal(err)
	}
	return contents
}

// ---------------------------------------------------------------------------
// Probes are default-deny by verb (#1795 B2)
// ---------------------------------------------------------------------------

// TestProbeDecision_isDefaultDenyByVerb states the rule on its own: a probe
// calls an operation the emulator does not implement, so against a real
// account nothing undoes it, and only a read is safe to make. The recipe's
// two exception maps are the only way past it.
func TestProbeDecision_isDefaultDenyByVerb(t *testing.T) {
	r := recipe{
		NeverProbe: map[string]string{"GetWidgetSecret": "rotates the secret it returns"},
		AllowProbe: map[string]string{"ScanWidgets": "a read spelled with another verb"},
	}
	cases := []struct {
		op        string
		probeable bool
		detail    string
	}{
		{op: "DescribeWidget", probeable: true},
		{op: "ListWidgets", probeable: true},
		{op: "GetWidget", probeable: true},
		{op: "List", probeable: true}, // the verb alone is still a read
		{op: "Get2Widgets", probeable: true},
		// A word boundary, not a prefix: these only begin with the letters.
		{op: "Listen", detail: notAReadOperation("Listen")},
		{op: "Getaway", detail: notAReadOperation("Getaway")},
		{op: "Describes", detail: notAReadOperation("Describes")},
		// Every other verb, whatever it does.
		{op: "CreateWidget", detail: notAReadOperation("CreateWidget")},
		{op: "DeleteWidget", detail: notAReadOperation("DeleteWidget")},
		{op: "PurgeWidgets", detail: notAReadOperation("PurgeWidgets")},
		// The exceptions, in both directions.
		{op: "GetWidgetSecret", detail: "rotates the secret it returns"},
		{op: "ScanWidgets", probeable: true, detail: "a read spelled with another verb"},
	}
	for _, tc := range cases {
		probeable, detail := r.probeDecision(tc.op)
		if probeable != tc.probeable {
			t.Errorf("%s: probeable = %v, want %v", tc.op, probeable, tc.probeable)
		}
		if tc.detail != "" && detail != tc.detail {
			t.Errorf("%s: detail = %q, want %q", tc.op, detail, tc.detail)
		}
	}
}

// TestGenerate_neverProbeReasonsComeFromTheRecipeWhereItHasOne is the
// migration the organizations recipe depends on: the curated sentence is
// still what gaps.json reports for an operation the recipe names, and the
// generated one covers everything else, so the 29 sentences did not have to
// become 29 restatements of "not a read".
func TestGenerate_neverProbeReasonsComeFromTheRecipeWhereItHasOne(t *testing.T) {
	_, gen := generateFixture(t)
	cases := []struct{ op, want string }{
		// A read verb the recipe denies: only the recipe could know.
		{"GetWidgetSecret", "the read verb in its name is a lie"},
		// A write the recipe denies anyway, because the prose says more.
		{"PurgeWidgets", "cannot be undone"},
		// A write the recipe says nothing about.
		{"FreezeWidget", notAReadOperation("FreezeWidget")},
		{"CreateCog", notAReadOperation("CreateCog")},
	}
	for _, tc := range cases {
		got := gapIn(gen, "widgets-gen-probe", tc.op)
		if got.Reason != reasonNeverProbe || !strings.Contains(got.Detail, tc.want) {
			t.Errorf("%s: gap = %+v, want %s saying %q", tc.op, got, reasonNeverProbe, tc.want)
		}
	}
	// And the two allowProbe operations are probed: one gets a test, the
	// other is refused for what its own output cannot support — which is the
	// point, since the verb rule would have hidden both answers.
	if _, _, ok := gen.scenario.findTest("widgets-gen-probe", "PingWidgets"); !ok {
		t.Error("PingWidgets, which allowProbe names, was not probed")
	}
	if got := gapIn(gen, "widgets-gen-probe", "ScanWidgets"); got.Reason != reasonNoOutputToAssert {
		t.Errorf("ScanWidgets, which allowProbe names, was refused %q", got.Reason)
	}
}

func TestRecipe_rejectsAProbeExceptionThatSaysNothing(t *testing.T) {
	f := loadFixture(t)
	const resources = `,"resources":[{"id":"a","create":{"op":"CreateWidget"},"read":{"op":"GetWidget","identityPath":"$.Widget.WidgetId"}}]}`
	cases := []struct{ name, body, want string }{
		{"allowProbe with no reason", `{"service":"w","allowProbe":{"PurgeWidgets":"  "}` + resources, "say why calling the operation"},
		{"neverProbe with no reason", `{"service":"w","neverProbe":{"PurgeWidgets":""}` + resources, "recipe.schema.json"},
		{"both at once", `{"service":"w","neverProbe":{"PurgeWidgets":"no"},"allowProbe":{"PurgeWidgets":"yes"}` + resources, "decide which"},
		{"allowProbe on a read verb", `{"service":"w","allowProbe":{"ListWidgets":"safe"}` + resources, "already probeable"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "w.json")
			writeFile(t, path, tc.body)
			_, err := loadRecipe(path, f.schemas)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
}
