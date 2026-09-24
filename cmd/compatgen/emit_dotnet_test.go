//go:build dev

package main

import (
	"maps"
	"strings"
	"sync"
	"testing"
)

// The .NET emitter, over the same hermetic fixture the rest of the generator's
// tests use (testdata/shapes/widgets.json + testdata/recipes/widgets.json).
//
// Like the Go emitter's tests these read a stand-in SDK: the emitter spells
// each member against the type AWSSDK gives its property, which it reads from
// the SDK type table the dotnet-sdk suite reflects (dotnetsdktypes.go), and
// testdata/dotnet-sdk-types is that table for the fixture service — every
// member spelled the way AWSSDK v4 spells its modeled kind. A test that needs
// the SDK to disagree with the model edits a copy (withDotnetProperty) rather
// than the committed fixture. The proof that real emitted source *compiles* is
// still the dotnet-sdk suite's own build.

// fixtureDotnetTypes is the stand-in table, read once per test binary.
var fixtureDotnetTypes = sync.OnceValue(func() *dotnetSDKTypes {
	types, err := loadDotnetSDKTypes("testdata/dotnet-sdk-types")
	if err != nil {
		panic(err)
	}
	return types
})

// fixtureDotnetSpeller is the speller emission uses for the fixture service.
func fixtureDotnetSpeller(t *testing.T, gen *generation) *dotnetSpeller {
	t.Helper()
	pkg, err := fixtureDotnetTypes().service(gen.scenario.Client.SDKID)
	if err != nil {
		t.Fatal(err)
	}
	return newDotnetSpeller(gen.model, pkg, gen.scenario.Client.SDKID)
}

// withDotnetProperties returns a copy of the fixture table with some
// properties retyped or added — class → property → type, in the table's own
// grammar — creating a class that is not there. It is how a test makes the SDK
// disagree with the model, the case the table exists for, without touching the
// shared fixture.
func withDotnetProperties(t *testing.T, edits map[string]map[string]string) *dotnetSDKTypes {
	t.Helper()
	src := fixtureDotnetTypes()
	out := &dotnetSDKTypes{byNamespace: map[string]*dotnetSDKPackage{}}
	for ns, pkg := range src.byNamespace {
		clone := *pkg
		clone.classes = maps.Clone(pkg.classes)
		for class, properties := range edits {
			props := maps.Clone(clone.classes[class])
			if props == nil {
				props = map[string]dotnetType{}
			}
			for property, raw := range properties {
				parsed, err := parseDotnetType(raw)
				if err != nil {
					t.Fatal(err)
				}
				props[property] = parsed
			}
			clone.classes[class] = props
		}
		out.byNamespace[ns] = &clone
		out.packages = append(out.packages, &clone)
	}
	return out
}

// spellDotnetMember spells one request member the way emission does: the
// modeled target, the SDK's property type for it, then the value.
func spellDotnetMember(t *testing.T, sp *dotnetSpeller, op, member, value string) (string, error) {
	t.Helper()
	target, err := sp.target(sp.model.InputShape(op), op, member)
	if err != nil {
		return "", err
	}
	class, err := sp.requestClass(op)
	if err != nil {
		return "", err
	}
	property, err := sp.property(class, op+"Request", member)
	if err != nil {
		return "", err
	}
	var v any
	if err := decodeStrict([]byte(value), &v); err != nil {
		t.Fatal(err)
	}
	return sp.value(target, property, v, member, "")
}

// dotnetGoldenPath is the emitted source for the fixture service. It carries a
// .golden suffix so no C# project ever compiles it: it names a service that
// does not exist.
const dotnetGoldenPath = "testdata/golden/ScenariosWidgetsGen.cs.golden"

func TestEmitDotnet_matchesTheGoldenSource(t *testing.T) {
	// Given: the fixture service, generated.
	_, gen := generateFixture(t)

	// When: it is emitted as C#.
	emission, err := emitDotnet(gen, fixtureDotnetTypes())
	if err != nil {
		t.Fatalf("emitDotnet: %v", err)
	}

	// Then: byte for byte what is committed.
	if emission.Path != "compat/suites/dotnet-sdk/Groups/ScenariosWidgetsGen.cs" {
		t.Errorf("emitted path = %s", emission.Path)
	}
	assertGolden(t, dotnetGoldenPath, emission.Contents)
}

func TestEmitDotnet_isDeterministic(t *testing.T) {
	_, gen := generateFixture(t)

	first, err := emitDotnet(gen, fixtureDotnetTypes())
	if err != nil {
		t.Fatalf("emitDotnet: %v", err)
	}
	second, err := emitDotnet(gen, fixtureDotnetTypes())
	if err != nil {
		t.Fatalf("emitDotnet: %v", err)
	}
	if string(first.Contents) != string(second.Contents) {
		t.Error("two emissions of one scenario differ; the byte-identical regeneration gate would fail at random")
	}
	// There is no gofmt for C# and the suite runs no formatter, so the layout
	// this writes is the layout committed. The one property worth pinning
	// mechanically is that nothing trails whitespace, which a diff shows and a
	// reviewer should not have to.
	for i, line := range strings.Split(strings.TrimSuffix(string(first.Contents), "\n"), "\n") {
		if line != strings.TrimRight(line, " \t") {
			t.Errorf("line %d has trailing whitespace: %q", i+1, line)
		}
	}
}

// TestEmitDotnet_emitsEveryGroupAndTest keeps the emitted registrations in step
// with the scenario. A test the emitter silently dropped would report as a
// hard failure in the suite (the loader's generated-no-backend rule), which is
// loud but late.
func TestEmitDotnet_emitsEveryGroupAndTest(t *testing.T) {
	_, gen := generateFixture(t)
	emission, err := emitDotnet(gen, fixtureDotnetTypes())
	if err != nil {
		t.Fatalf("emitDotnet: %v", err)
	}
	source := string(emission.Contents)
	for _, g := range gen.scenario.Groups {
		for _, tc := range g.Tests {
			key := `"` + g.Name + ":" + tc.Name + `"`
			if !strings.Contains(source, key) {
				t.Errorf("no impl registered under %s", key)
			}
			if !strings.Contains(source, "private Task "+dotnetNameTestMethod(g.Name, tc.Name)+"(") {
				t.Errorf("no method emitted for %s/%s", g.Name, tc.Name)
			}
		}
		// Every group registers both hooks, even the probe group whose two
		// lists are empty: an empty phase is a no-op, not a missing one.
		for _, method := range []string{dotnetNameSetupMethod(g.Name), dotnetNameTeardownMethod(g.Name)} {
			if !strings.Contains(source, "private Task "+method+"(") {
				t.Errorf("no %s emitted for %s", method, g.Name)
			}
		}
	}
	if !strings.Contains(source, "// Code generated by cmd/compatgen; DO NOT EDIT.") {
		t.Error("the generated-file marker is missing")
	}
}

// TestEmitDotnet_refusesWhatItCannotSpell pins every refusal this backend
// introduces, and what each costs: the group leaves the dotnet-sdk column
// rather than being emitted as a guess or dropped silently.
//
// None arises from a committed scenario — the recipes and the upstream
// refusals see to that — so each is constructed against the fixture model.
// The three families the emitter's header names are all here: a modeled kind
// with no C# literal, a deferred expression with no scalar slot to land in,
// and an integral literal C# would refuse to compile. The other two rows are
// the backstops either side of them.
func TestEmitDotnet_refusesWhatItCannotSpell(t *testing.T) {
	for _, tc := range []struct {
		name       string
		op         string
		params     map[string]any
		wantMember string
		wantDetail string
	}{
		{
			// The fixture models ListWidgets with an optional CreatedAfter,
			// and a timestamp has no literal in the IR's value grammar at all.
			name:       "a member whose modeled kind has no C# literal",
			op:         "ListWidgets",
			params:     map[string]any{"CreatedAfter": "2026-09-06T00:00:00Z"},
			wantMember: "CreatedAfter",
			wantDetail: "no C# literal for a timestamp member",
		},
		{
			// A deferred expression resolves into one scalar slot. A whole
			// list from a $ref has none, and inventing one would mean
			// converting an object[] to a List<string> at run time —
			// reflection, by another name.
			name:       "an expression bound to a composite member",
			op:         "UntagWidget",
			params:     map[string]any{"TagKeys": map[string]any{"$ref": "tags.keys"}},
			wantMember: "TagKeys",
			wantDetail: "can only be bound to a scalar member",
		},
		{
			// A literal of the wrong kind is refused rather than coerced:
			// "45" is not 45 anywhere else in the IR, and accepting it here
			// would let a wrong literal through into source that compiles.
			name:       "a literal of the wrong JSON type",
			op:         "RotateWidget",
			params:     map[string]any{"Angle": "45", "WidgetId": "w-1"},
			wantMember: "Angle",
			wantDetail: "wants a number, got a string",
		},
		{
			// C# range-checks an integral literal at compile time, and this
			// backend's compile errors are suite-wide rather than scoped to one
			// group. So the value is refused here, where the cost is one group
			// leaving the dotnet-sdk column.
			name:       "an integer literal outside the C# type's range",
			op:         "RotateWidget",
			params:     map[string]any{"Angle": 3000000000, "WidgetId": "w-1"},
			wantMember: "Angle",
			wantDetail: "is out of range for an integer member",
		},
		{
			// The upstream validation refuses an unknown member long before
			// this, so it is a backstop — but a backstop that names the member
			// rather than emitting a property nothing declares.
			name:       "a member the model does not declare",
			op:         "GetWidget",
			params:     map[string]any{"Sprocket": "s-1"},
			wantMember: "Sprocket",
			wantDetail: `has no member "Sprocket"`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Given: a group whose call carries that member.
			_, gen := generateFixture(t)
			const name = "widgets-gen-refused"
			gen.scenario.Groups = append(gen.scenario.Groups, group{
				Name: name,
				Kind: groupLifecycle,
				Tests: []test{newTest(tc.op, tc.op, call{Op: tc.op, Params: tc.params},
					responseField(checks("$.Widgets", isList())))},
			})

			// When: the service is emitted.
			emission, err := emitDotnet(gen, fixtureDotnetTypes())
			if err != nil {
				t.Fatalf("emitDotnet: %v", err)
			}

			// Then: the group is not in the emitted source, it is reported as
			// unable so the registry scopes it away from dotnet-sdk, and the
			// refusal names the member and says why.
			if strings.Contains(string(emission.Contents), name) {
				t.Error("a group the emitter cannot spell was emitted anyway")
			}
			if !emission.Refused[name] {
				t.Fatal("the refused group was not reported as unable")
			}
			if len(emission.Gaps) != 1 {
				t.Fatalf("gaps = %+v, want one", emission.Gaps)
			}
			got := emission.Gaps[0]
			if got.Reason != dotnetEmitReason+":"+tc.wantMember || got.Operation != tc.op || got.Group != name {
				t.Errorf("gap = %+v", got)
			}
			if !strings.Contains(got.Detail, tc.wantDetail) {
				t.Errorf("gap detail does not say why: %q, want it to mention %q", got.Detail, tc.wantDetail)
			}
		})
	}
}

// TestDotnetSpeller_spellsEveryShapeOfMember is the type-spelling table read
// back, one row at a time, against the fixture's own model. The golden file
// proves what a whole service comes out as; this says which rule produced each
// piece of it, and it is where a new rule is added with its own row.
//
// Every row is a value with no SDK type named anywhere in it. That is the
// property the backend rests on: a collection expression, a target-typed
// `new()` and ConstantClass's implicit conversion from string between them
// mean the property being assigned supplies every type.
func TestDotnetSpeller_spellsEveryShapeOfMember(t *testing.T) {
	_, gen := generateFixture(t)
	sp := fixtureDotnetSpeller(t, gen)
	for _, tc := range []struct {
		name   string
		op     string
		member string
		value  string
		want   string
	}{
		{"a string", "CreateWidget", "Description", `"one"`, `"one"`},
		{"an enum, as the string its ConstantClass converts from", "CreateWidget", "Color", `"blue"`, `"blue"`},
		{"a number", "RotateWidget", "Angle", `45`, `45`},
		{"a string map", "TagWidget", "Tags", `{"compat":"scenario"}`, `new() { ["compat"] = "scenario" }`},
		{"a list of strings", "UntagWidget", "TagKeys", `["compat"]`, `["compat"]`},
		{
			// The element type is stated by the property, so the structure
			// element is a target-typed new() — as a human would write it.
			"a list of structures", "TagSprocket", "Tags", `[{"Key":"k","Value":"v"}]`,
			`[new() { Key = "k", Value = "v" }]`,
		},
		{
			"an expression into a string", "GetWidget", "WidgetId", `{"$ref":"widget.id"}`,
			`b.Bind<string>("WidgetId", Val.Ref("widget.id"))`,
		},
		{
			// The Bind result is a string, and the implicit conversion the
			// enum's ConstantClass declares is what makes the assignment
			// compile — so no cast is written.
			"an expression into an enum", "CreateWidget", "Color", `{"$name":"c"}`,
			`b.Bind<string>("Color", Val.Name("c"))`,
		},
		{
			"an expression inside a composite", "TagWidget", "Tags", `{"compat":{"$ref":"t"}}`,
			`new() { ["compat"] = b.Bind<string>("Tags", Val.Ref("t")) }`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := spellDotnetMember(t, sp, tc.op, tc.member, tc.value)
			if err != nil {
				t.Fatalf("spelling %s.%s: %v", tc.op, tc.member, err)
			}
			if got != tc.want {
				t.Errorf("%s.%s = %s, want %s", tc.op, tc.member, got, tc.want)
			}
			if strings.Contains(got, "Amazon.") || strings.Contains(got, "List<") {
				t.Errorf("%s.%s names an SDK type: %s", tc.op, tc.member, got)
			}
		})
	}
}

// TestDotnetValue_coversTheIRsValueGrammar keeps the naming table total: every
// form the IR admits has a C# expression, and nothing else does.
func TestDotnetValue_coversTheIRsValueGrammar(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   string
		want string
	}{
		{"a string", `"hello"`, `"hello"`},
		{"a number", `30`, `30`},
		{"a boolean", `false`, `false`},
		{"null", `null`, `null`},
		{"a list", `["a","b"]`, `new object?[] { "a", "b" }`},
		{"an object", `{"K":"v"}`, `new Dictionary<string, object?> { ["K"] = "v" }`},
		{"$name", `{"$name":"q"}`, `Val.Name("q")`},
		{"$ref", `{"$ref":"queue.url"}`, `Val.Ref("queue.url")`},
		{"$lit", `{"$lit":{"$weird":1}}`, `Val.Lit(new Dictionary<string, object?> { ["$weird"] = 1 })`},
		{"$concat", `{"$concat":["a",{"$ref":"q.u"}]}`, `Val.Concat("a", Val.Ref("q.u"))`},
		{"$index", `{"$index":[{"$ref":"q.urls"},1]}`, `Val.Index(Val.Ref("q.urls"), 1)`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var v any
			if err := decodeStrict([]byte(tc.in), &v); err != nil {
				t.Fatal(err)
			}
			got, err := dotnetValue(v)
			if err != nil {
				t.Fatalf("dotnetValue(%s): %v", tc.in, err)
			}
			if got != tc.want {
				t.Errorf("dotnetValue(%s) = %s, want %s", tc.in, got, tc.want)
			}
		})
	}

	// A value outside the grammar has no expression, and says so rather than
	// rendering something that would not compile.
	if _, err := dotnetValue(struct{}{}); err == nil {
		t.Error("a value outside the IR's grammar was rendered")
	}
}

// TestCsString_escapesWhatCSharpWouldMisread pins the one place the emitter
// writes arbitrary text into source: a scenario's params JSON, quoted whole
// into failure-message field 3.
func TestCsString_escapesWhatCSharpWouldMisread(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{`plain`, `"plain"`},
		{`a "quoted" word`, `"a \"quoted\" word"`},
		{`back\slash`, `"back\\slash"`},
		{"tab\tand\nnewline", `"tab\tand\nnewline"`},
		{"caf\u00e9", `"caf\u00E9"`},
		// Above the BMP the four-digit form is not merely ugly, it is wrong:
		// C# reads exactly four hex digits, so \u1F600 is U+1F60 followed by
		// a literal "0".
		{"\U0001F600", `"\U0001F600"`},
	} {
		if got := csString(tc.in); got != tc.want {
			t.Errorf("csString(%q) = %s, want %s", tc.in, got, tc.want)
		}
	}
}

// TestDotnetServiceNameOverridesTheLongNames pins the one derivation the AWS
// SDK for .NET does not follow. Its packages keep several services under the
// long names they launched with, so the rule "Amazon. plus the SDK id" names a
// namespace that does not exist for them — AWSSDK.SimpleNotificationService has
// no Amazon.SNS — and the emitted file then fails the whole dotnet-sdk suite's
// build rather than one group. Everything else still derives.
func TestDotnetServiceNameOverridesTheLongNames(t *testing.T) {
	for _, tc := range []struct{ sdkID, namespace, client, config string }{
		{"SNS", "Amazon.SimpleNotificationService", "AmazonSimpleNotificationServiceClient", "AmazonSimpleNotificationServiceConfig"},
		{"SQS", "Amazon.SQS", "AmazonSQSClient", "AmazonSQSConfig"},
		{"Organizations", "Amazon.Organizations", "AmazonOrganizationsClient", "AmazonOrganizationsConfig"},
		{"Elastic Load Balancing", "Amazon.ElasticLoadBalancing", "AmazonElasticLoadBalancingClient", "AmazonElasticLoadBalancingConfig"},
	} {
		if got := dotnetNameNamespace(tc.sdkID); got != tc.namespace {
			t.Errorf("dotnetNameNamespace(%q) = %s, want %s", tc.sdkID, got, tc.namespace)
		}
		if got := dotnetNameClientClass(tc.sdkID); got != tc.client {
			t.Errorf("dotnetNameClientClass(%q) = %s, want %s", tc.sdkID, got, tc.client)
		}
		if got := dotnetNameConfigClass(tc.sdkID); got != tc.config {
			t.Errorf("dotnetNameConfigClass(%q) = %s, want %s", tc.sdkID, got, tc.config)
		}
	}
}

// TestDotnetMethodNamesAreUnique refuses two names that fold to one C#
// identifier before the suite fails to build with no indication of which pair
// caused it.
func TestDotnetMethodNamesAreUnique(t *testing.T) {
	groups := []group{
		{Name: "svc-gen-a-b", Tests: []test{{Name: "C"}}},
		{Name: "svc-gen-a", Tests: []test{{Name: "BC"}}},
	}
	err := dotnetMethodNamesAreUnique("svc", groups)
	if err == nil {
		t.Fatal("two colliding method names were accepted")
	}
	for _, want := range []string{"svc-gen-a-b/C", "svc-gen-a/BC", "TestSvcGenABC"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the collision message lacks %q: %v", want, err)
		}
	}
}

// TestEmitDotnetIndex_listsEveryEmittedServiceAndCompilesWhenEmpty pins both
// halves of the index: it lists what was emitted, and it is still valid C#
// when nothing was.
func TestEmitDotnetIndex_listsEveryEmittedServiceAndCompilesWhenEmpty(t *testing.T) {
	source := string(emitDotnetIndex([]string{"sqs", "organizations"}))
	if !strings.Contains(source, "new ScenariosOrganizations(clients),") || !strings.Contains(source, "new ScenariosSqs(clients),") {
		t.Errorf("index does not name both services:\n%s", source)
	}
	if strings.Index(source, "ScenariosOrganizations") > strings.Index(source, "ScenariosSqs") {
		t.Error("the index is not sorted; regeneration would depend on recipe read order")
	}

	// The remark explaining an empty list is printed only where the list is
	// empty: above two entries it contradicts what the reader can see.
	if strings.Contains(source, "The list is empty") {
		t.Errorf("the populated index claims to be empty:\n%s", source)
	}

	empty := string(emitDotnetIndex(nil))
	if !strings.Contains(empty, "The list is empty because") {
		t.Errorf("the empty index does not say why it is empty:\n%s", empty)
	}
	if !strings.Contains(empty, "All(AwsClients clients) => [];") {
		t.Errorf("the empty index does not compile to an empty list:\n%s", empty)
	}
}

// TestExplainDotnetRendersTheEmittedCall is the definition-of-done item that
// there is one naming table: `-explain -lang dotnet` must print the statements
// the emitter writes, not a second description of them. Both go through
// dotnetInputLines, over the same shape model.
func TestExplainDotnetRendersTheEmittedCall(t *testing.T) {
	_, gen := generateFixture(t)
	g, tc, ok := gen.scenario.findTest("widgets-gen-widget", "CreateWidget")
	if !ok {
		t.Fatal("fixture has no CreateWidget")
	}
	explained := renderDotnet(fixtureRenderEnv(gen), gen.scenario, g, tc)

	emission, err := emitDotnet(gen, fixtureDotnetTypes())
	if err != nil {
		t.Fatalf("emitDotnet: %v", err)
	}
	emitted := string(emission.Contents)

	lines, err := dotnetInputLines(fixtureDotnetSpeller(t, gen), tc.Call.Op, tc.Call.Params, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) < 2 {
		t.Fatalf("CreateWidget renders %d line(s); the fixture no longer exercises member binding", len(lines))
	}
	for _, line := range lines {
		if !strings.Contains(explained, line) {
			t.Errorf("-explain -lang dotnet does not render %q:\n%s", line, explained)
		}
		if !strings.Contains(emitted, line) {
			t.Errorf("the emitted source does not contain %q", line)
		}
	}
}

// TestExplainDotnetSaysSoWhenTheModelCannotBeRead keeps `-explain` a reader's
// tool on a partial checkout: it prints why it cannot spell the call rather
// than a spelling that would be a guess.
func TestExplainDotnetSaysSoWhenTheModelCannotBeRead(t *testing.T) {
	_, gen := generateFixture(t)
	g, tc, ok := gen.scenario.findTest("widgets-gen-widget", "CreateWidget")
	if !ok {
		t.Fatal("fixture has no CreateWidget")
	}
	out := renderDotnet(renderEnv{}, gen.scenario, g, tc)
	if !strings.Contains(out, "could not be read") {
		t.Errorf("the rendering does not say the model was unreadable:\n%s", out)
	}
}
