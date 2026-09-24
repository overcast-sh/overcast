//go:build dev

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/overcast-sh/overcast/internal/awsmodel"
)

// The .NET SDK type table (dotnetsdktypes.go), and the emitter reading it: the
// dotnet counterpart of #1831's emit-time SDK type resolution. The disagreement
// every row below is built around is the real one — AWSSDK.CloudWatchLogs types
// InputLogEvent.Timestamp as DateTime? where the model says long — acted out on
// the fixture service by adding an operation to its model and a class to a copy
// of its table.

// withDotnetStampShapes adds StampWidget and ReadStamp to the fixture model:
// epoch-millisecond longs at the top level, inside a structure inside a list,
// and in a response. Added to the loaded model, as withJavaTypeShapes does,
// because no recipe names them and the goldens should not grow for members no
// scenario sends.
func withDotnetStampShapes(model *serviceModel) *serviceModel {
	member := func(target string) awsmodel.SnapshotMember { return awsmodel.SnapshotMember{Target: target} }
	model.Shapes["StampWidget"] = awsmodel.SnapshotShape{Type: "operation", Input: "StampWidgetRequest", Output: "StampWidgetResponse"}
	model.Shapes["StampWidgetRequest"] = awsmodel.SnapshotShape{Type: "structure", Members: map[string]awsmodel.SnapshotMember{
		"Stamp":  member("smithy.api#Long"),
		"Events": member("StampEventList"),
	}}
	model.Shapes["StampWidgetResponse"] = awsmodel.SnapshotShape{Type: "structure", Members: map[string]awsmodel.SnapshotMember{
		"Stamped": member("smithy.api#Long"),
	}}
	model.Shapes["StampEventList"] = awsmodel.SnapshotShape{Type: "list", Member: "StampEvent"}
	model.Shapes["StampEvent"] = awsmodel.SnapshotShape{Type: "structure", Members: map[string]awsmodel.SnapshotMember{
		"At":   member("smithy.api#Long"),
		"Note": member("smithy.api#String"),
	}}
	model.Shapes["ReadStamp"] = awsmodel.SnapshotShape{Type: "operation", Input: "ReadStampRequest", Output: "ReadStampResponse"}
	model.Shapes["ReadStampRequest"] = awsmodel.SnapshotShape{Type: "structure", Members: map[string]awsmodel.SnapshotMember{
		"WidgetId": member("WidgetId"),
	}}
	model.Shapes["ReadStampResponse"] = awsmodel.SnapshotShape{Type: "structure", Members: map[string]awsmodel.SnapshotMember{
		"History": member("StampEventList"),
	}}
	return model
}

// dotnetStampTable is the fixture table with the classes AWSSDK would declare
// for withDotnetStampShapes, every epoch a DateTime? over the model's long.
func dotnetStampTable(t *testing.T) *dotnetSDKTypes {
	return withDotnetProperties(t, map[string]map[string]string{
		"StampWidgetRequest":  {"Stamp": "DateTime?", "Events": "List<class:StampEvent>"},
		"StampWidgetResponse": {"Stamped": "DateTime?"},
		"StampEvent":          {"At": "DateTime?", "Note": "string"},
		"ReadStampRequest":    {"WidgetId": "string"},
		"ReadStampResponse":   {"History": "List<class:StampEvent>"},
	})
}

// measureFixtureEpochMilliseconds records the fixture service as measured for
// the duration of a test, as a real service is recorded once its wire unit has
// been measured.
func measureFixtureEpochMilliseconds(t *testing.T, gen *generation) {
	t.Helper()
	id := gen.scenario.Client.SDKID
	dotnetEpochMilliseconds[id] = "AWSSDK.Widgets"
	t.Cleanup(func() { delete(dotnetEpochMilliseconds, id) })
}

func stampSpeller(t *testing.T, gen *generation, measured bool) *dotnetSpeller {
	t.Helper()
	pkg, err := dotnetStampTable(t).service(gen.scenario.Client.SDKID)
	if err != nil {
		t.Fatal(err)
	}
	return &dotnetSpeller{model: withDotnetStampShapes(gen.model), sdk: pkg, epochMilliseconds: measured}
}

func TestDotnetSpeller_spellsAMeasuredEpochMillisecondsDateTime(t *testing.T) {
	_, gen := generateFixture(t)
	sp := stampSpeller(t, gen, true)
	for _, tc := range []struct{ name, member, value, want string }{
		{
			// The literal is the model's number, which is what every other
			// backend sends; the property wants the DateTime it names.
			"a literal", "Stamp", `1726000000123`,
			"System.DateTimeOffset.FromUnixTimeMilliseconds(1726000000123L).UtcDateTime",
		},
		{
			// An epoch exported from an earlier response — the logs-events
			// port stamps every event with its stream's creationTime.
			"an exported epoch", "Stamp", `{"$ref":"stream.created"}`,
			`b.EpochMilliseconds("Stamp", Val.Ref("stream.created"))`,
		},
		{
			// The client's clock (`$now`), which is what the logs-events port
			// stamps each event with: the same number every other backend
			// sends, made into the DateTime the property wants.
			"the client's clock", "Stamp", `{"$now":{"unit":"epochMillis","offsetMillis":-1}}`,
			`b.EpochMilliseconds("Stamp", Val.Now("epochMillis", -1L))`,
		},
		{
			// InputLogEvent's own shape: a structure inside a list, so the
			// SDK's class is followed down from the request's property.
			"inside a structure inside a list", "Events", `[{"At":{"$ref":"s"},"Note":"n"}]`,
			`[new() { At = b.EpochMilliseconds("Events", Val.Ref("s")), Note = "n" }]`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := spellDotnetMember(t, sp, "StampWidget", tc.member, tc.value)
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Errorf("StampWidget.%s = %s, want %s", tc.member, got, tc.want)
			}
		})
	}
}

func TestDotnetSpeller_refusesWhatTheSDKsTypeCannotTake(t *testing.T) {
	_, gen := generateFixture(t)
	for _, tc := range []struct {
		name     string
		measured bool
		op       string
		member   string
		value    string
		// edits retypes properties of the fixture table; drop removes one.
		edits map[string]map[string]string
		drop  [2]string
		want  string
	}{
		{
			// The disagreement alone is not enough: which unit the SDK puts on
			// the wire is a measurement, and a guess would compile.
			name:   "a DateTime over a long in a service whose unit is not measured",
			op:     "StampWidget",
			member: "Stamp",
			value:  `1726000000123`,
			want:   "has not been for this service",
		},
		{
			name:     "an epoch no DateTime can hold",
			measured: true,
			op:       "StampWidget",
			member:   "Stamp",
			value:    `300000000000000`,
			want:     "is out of range for a long member",
		},
		{
			// Any other disagreement has no spelling at all.
			name:   "an SDK type the model's kind does not convert to",
			op:     "RotateWidget",
			member: "Angle",
			value:  `45`,
			edits:  map[string]map[string]string{"RotateWidgetRequest": {"Angle": "string"}},
			want:   "the model says integer and AWSSDK.Widgets 4.0.0 types it string",
		},
		{
			// Binder.Bind converts to byte, short, int and long; an unsigned
			// property would be a run-time "no conversion" rather than a
			// compile error, so it is refused here.
			name:   "an expression into a property Bind cannot produce",
			op:     "RotateWidget",
			member: "Angle",
			value:  `{"$ref":"a"}`,
			edits:  map[string]map[string]string{"RotateWidgetRequest": {"Angle": "uint?"}},
			want:   "cannot be bound to a uint property",
		},
		{
			// A member the SDK renamed or dropped: a suite-wide compile error
			// before the table, a refusal naming the property now.
			name:   "a property the pinned package does not declare",
			op:     "RotateWidget",
			member: "Angle",
			value:  `45`,
			edits:  map[string]map[string]string{"RotateWidgetRequest": {"Heading": "int?"}},
			drop:   [2]string{"RotateWidgetRequest", "Angle"},
			want:   "declares no property RotateWidgetRequest.Angle",
		},
		{
			// The generator holds a $now to a modeled long; an SDK that
			// narrowed that long would overflow at run time, and is refused.
			name:   "the client's clock into a property narrower than a long",
			op:     "RotateWidget",
			member: "Angle",
			value:  `{"$now":{"unit":"epochMillis"}}`,
			edits:  map[string]map[string]string{"RotateWidgetRequest": {"Angle": "int?"}},
			want:   "a $now is epoch milliseconds",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			table := dotnetStampTable(t)
			if tc.edits != nil {
				table = withDotnetProperties(t, tc.edits)
			}
			pkg, err := table.service(gen.scenario.Client.SDKID)
			if err != nil {
				t.Fatal(err)
			}
			if tc.drop[0] != "" {
				// Only a class edits cloned may be changed in place.
				delete(pkg.classes[tc.drop[0]], tc.drop[1])
			}
			sp := &dotnetSpeller{model: withDotnetStampShapes(gen.model), sdk: pkg, epochMilliseconds: tc.measured}
			got, err := spellDotnetMember(t, sp, tc.op, tc.member, tc.value)
			if err == nil {
				t.Fatalf("%s.%s was spelled %s; want a refusal", tc.op, tc.member, got)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("refusal %q does not say %q", err, tc.want)
			}
		})
	}
}

// TestDotnetSpeller_followsTheSDKsWidthOverTheModels is the point of reading
// the SDK at all: where the two agree on the kind but not the width, the
// literal and Bind follow the property the source is compiled against.
func TestDotnetSpeller_followsTheSDKsWidthOverTheModels(t *testing.T) {
	_, gen := generateFixture(t)
	table := withDotnetProperties(t, map[string]map[string]string{"RotateWidgetRequest": {"Angle": "long?"}})
	pkg, err := table.service(gen.scenario.Client.SDKID)
	if err != nil {
		t.Fatal(err)
	}
	sp := newDotnetSpeller(gen.model, pkg, gen.scenario.Client.SDKID)
	for value, want := range map[string]string{
		`45`:           "45L",
		`{"$ref":"a"}`: `b.Bind<long>("Angle", Val.Ref("a"))`,
		// A long property takes what an int one would refuse.
		`3000000000`: "3000000000L",
	} {
		got, err := spellDotnetMember(t, sp, "RotateWidget", "Angle", value)
		if err != nil {
			t.Fatalf("%s: %v", value, err)
		}
		if got != want {
			t.Errorf("RotateWidget.Angle = %s from %s, want %s", got, value, want)
		}
	}
}

// TestEmitDotnet_registersEveryEpochMillisecondsPropertyItTouches pins the
// response half: Documents renders a DateTime as ISO text unless told the
// model calls it a long, so the emitted constructor tells it — for requests,
// which failure-message field 3 renders, and for responses, which the clauses
// read.
func TestEmitDotnet_registersEveryEpochMillisecondsPropertyItTouches(t *testing.T) {
	_, gen := generateFixture(t)
	withDotnetStampShapes(gen.model)
	measureFixtureEpochMilliseconds(t, gen)
	gen.scenario.Groups = append(gen.scenario.Groups, group{
		Name: "widgets-gen-stamp",
		Kind: groupLifecycle,
		Tests: []test{
			newTest("StampWidget", "StampWidget",
				call{Op: "StampWidget", Params: map[string]any{"Stamp": map[string]any{"$ref": "s"}}},
				responseField(checks("$.Stamped", nonEmpty()))),
			newTest("ReadStamp", "ReadStamp",
				call{Op: "ReadStamp", Params: map[string]any{"WidgetId": "w-1"}},
				responseField(checks("$.History", isList()))),
		},
	})

	emission, err := emitDotnet(gen, dotnetStampTable(t))
	if err != nil {
		t.Fatalf("emitDotnet: %v", err)
	}
	source := string(emission.Contents)
	if emission.Refused["widgets-gen-stamp"] {
		t.Fatalf("the group was refused: %+v", emission.Gaps)
	}
	for _, want := range []string{
		"request.Stamp = b.EpochMilliseconds(\"Stamp\", Val.Ref(\"s\"));",
		"typeof(Amazon.Widgets.Model.StampEvent),\n            nameof(Amazon.Widgets.Model.StampEvent.At));",
		"typeof(Amazon.Widgets.Model.StampWidgetRequest),\n            nameof(Amazon.Widgets.Model.StampWidgetRequest.Stamp));",
		"typeof(Amazon.Widgets.Model.StampWidgetResponse),\n            nameof(Amazon.Widgets.Model.StampWidgetResponse.Stamped));",
	} {
		if !strings.Contains(source, want) {
			t.Errorf("the emitted source lacks %q:\n%s", want, source)
		}
	}
	// Once per class, however many calls reach it: StampEvent is reached from
	// both operations.
	if n := strings.Count(source, "typeof(Amazon.Widgets.Model.StampEvent)"); n != 1 {
		t.Errorf("StampEvent is registered %d times, want once", n)
	}
}

// TestEmitDotnet_refusesAResponseItCannotRenderAlike is the refusal the
// document side adds: a group that only *reads* a DateTime-for-long in a
// service whose unit is not measured sets no member the input check could
// refuse, and would still leave dotnet-sdk holding a different document from
// every other backend.
func TestEmitDotnet_refusesAResponseItCannotRenderAlike(t *testing.T) {
	_, gen := generateFixture(t)
	withDotnetStampShapes(gen.model)
	gen.scenario.Groups = append(gen.scenario.Groups, group{
		Name: "widgets-gen-stamp",
		Kind: groupLifecycle,
		Tests: []test{newTest("ReadStamp", "ReadStamp",
			call{Op: "ReadStamp", Params: map[string]any{"WidgetId": "w-1"}},
			responseField(checks("$.History", isList())))},
	})

	emission, err := emitDotnet(gen, dotnetStampTable(t))
	if err != nil {
		t.Fatalf("emitDotnet: %v", err)
	}
	if !emission.Refused["widgets-gen-stamp"] {
		t.Fatal("a group reading an unrenderable response was emitted")
	}
	var found bool
	for _, g := range emission.Gaps {
		if g.Group == "widgets-gen-stamp" {
			found = true
			if g.Reason != dotnetEmitReason+":At" || g.Operation != "ReadStamp" || !strings.Contains(g.Detail, "StampEvent.At") {
				t.Errorf("gap = %+v", g)
			}
		}
	}
	if !found {
		t.Errorf("no gap names the refused group: %+v", emission.Gaps)
	}
}

// TestEmitDotnet_refusesAnOperationThePinnedPackageLacks turns what used to be
// a suite-wide compile error into a refusal scoped to one group, as go-sdk's
// `:<Op>Input` refusal does.
func TestEmitDotnet_refusesAnOperationThePinnedPackageLacks(t *testing.T) {
	_, gen := generateFixture(t)
	table := dotnetStampTable(t)
	delete(table.byNamespace["Amazon.Widgets"].classes, "RotateWidgetRequest")
	gen.scenario.Groups = append(gen.scenario.Groups, group{
		Name: "widgets-gen-rotate",
		Kind: groupLifecycle,
		Tests: []test{newTest("RotateWidget", "RotateWidget",
			call{Op: "RotateWidget", Params: map[string]any{"WidgetId": "w-1", "Angle": 45}},
			responseField(checks("$.Widgets", isList())))},
	})

	emission, err := emitDotnet(gen, table)
	if err != nil {
		t.Fatalf("emitDotnet: %v", err)
	}
	if !emission.Refused["widgets-gen-rotate"] {
		t.Fatal("a group calling an operation the package lacks was emitted")
	}
	for _, g := range emission.Gaps {
		if g.Group == "widgets-gen-rotate" && g.Reason != dotnetEmitReason+":RotateWidgetRequest" {
			t.Errorf("gap = %+v, want reason %s:RotateWidgetRequest", g, dotnetEmitReason)
		}
	}
}

func TestParseDotnetType_readsTheClosedGrammar(t *testing.T) {
	for raw, check := range map[string]func(dotnetType) bool{
		"string":    func(d dotnetType) bool { return d.scalar("string") && !d.Nullable },
		"DateTime?": func(d dotnetType) bool { return d.scalar("DateTime") && d.Nullable },
		"enum:Distribution": func(d dotnetType) bool {
			return d.Form == "enum" && d.Name == "Distribution"
		},
		"List<class:InputLogEvent>": func(d dotnetType) bool {
			return d.Form == "list" && d.Elem.Form == "class" && d.Elem.Name == "InputLogEvent"
		},
		"Dictionary<string,List<Dictionary<string,int?>>>": func(d dotnetType) bool {
			return d.Form == "map" && d.Key.scalar("string") && d.Value.Form == "list" &&
				d.Value.Elem.Form == "map" && d.Value.Elem.Value.scalar("int") && d.Value.Elem.Value.Nullable
		},
		"type:System.EventHandler<type:Amazon.Runtime.StreamTransferProgressArgs>": func(d dotnetType) bool {
			return d.Form == "other"
		},
	} {
		got, err := parseDotnetType(raw)
		if err != nil {
			t.Errorf("%s: %v", raw, err)
			continue
		}
		if !check(got) {
			t.Errorf("%s parsed as %+v", raw, got)
		}
	}
	for _, raw := range []string{"Guid", "List<string", "Dictionary<string>", "List<a,b>"} {
		if _, err := parseDotnetType(raw); err == nil {
			t.Errorf("%s was accepted", raw)
		}
	}
}

func TestParseDotnetSDKPackage_refusesAMalformedFile(t *testing.T) {
	for name, tc := range map[string]struct{ file, text, want string }{
		"no header": {"AWSSDK.X.txt", "namespace Amazon.X.Model\nclass A\n", "no package or namespace header"},
		"misnamed":  {"AWSSDK.Y.txt", "package AWSSDK.X 4.0.0\nnamespace Amazon.X.Model\n", "should be named AWSSDK.X.txt"},
		"orphan":    {"AWSSDK.X.txt", "package AWSSDK.X 4.0.0\nnamespace Amazon.X.Model\n  A string\n", "outside any class"},
		"bad type":  {"AWSSDK.X.txt", "package AWSSDK.X 4.0.0\nnamespace Amazon.X.Model\nclass A\n  B Guid\n", `unknown type "Guid"`},
		"twice":     {"AWSSDK.X.txt", "package AWSSDK.X 4.0.0\nnamespace Amazon.X.Model\nclass A\nclass A\n", "class A twice"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := parseDotnetSDKPackage(tc.file, []byte(tc.text))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want it to mention %q", err, tc.want)
			}
		})
	}
	// A checkout with CRLF endings reads as the LF text.
	pkg, err := parseDotnetSDKPackage("AWSSDK.X.txt", []byte("package AWSSDK.X 4.0.0\r\nnamespace Amazon.X.Model\r\nclass A\r\n  B long?\r\n"))
	if err != nil {
		t.Fatal(err)
	}
	if b := pkg.classes["A"]["B"]; !b.scalar("long") || !b.Nullable {
		t.Errorf("A.B = %+v", b)
	}
}

// TestCheckDotnetSDKPins is the offline half of the table's guard: a pin bump
// that forgot to refresh the table fails generation and -check, naming the
// command, rather than emitting against another version's types.
func TestCheckDotnetSDKPins(t *testing.T) {
	dir := t.TempDir()
	for file, text := range map[string]string{
		"AWSSDK.SQS.txt":           "package AWSSDK.SQS 4.0.0\nnamespace Amazon.SQS.Model\n",
		"AWSSDK.Organizations.txt": "package AWSSDK.Organizations 4.0.101.4\nnamespace Amazon.Organizations.Model\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, file), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	types, err := loadDotnetSDKTypes(dir)
	if err != nil {
		t.Fatal(err)
	}
	csproj := func(refs string) []byte {
		return []byte(`<Project Sdk="Microsoft.NET.Sdk"><ItemGroup>` + refs + `</ItemGroup></Project>`)
	}
	matching := `<PackageReference Include="AWSSDK.Core" Version="4.0.102.3" />` +
		`<PackageReference Include="AWSSDK.Organizations" Version="4.0.101.4" />` +
		`<PackageReference Include="AWSSDK.SQS" Version="4.0.0" />` +
		`<PackageReference Include="xunit" Version="2.9.2" />`
	if err := checkDotnetSDKPins(types, csproj(matching)); err != nil {
		t.Errorf("a table matching its pins was refused: %v", err)
	}
	for name, tc := range map[string]struct{ refs, want string }{
		"a bumped pin": {
			strings.Replace(matching, `"AWSSDK.SQS" Version="4.0.0"`, `"AWSSDK.SQS" Version="4.0.1"`, 1),
			"AWSSDK.SQS is pinned at 4.0.1 but the table was reflected from 4.0.0",
		},
		"a new package": {
			matching + `<PackageReference Include="AWSSDK.Kinesis" Version="4.0.0" />`,
			"AWSSDK.Kinesis 4.0.0 is pinned but has no table file",
		},
		"a dropped package": {
			strings.Replace(matching, `<PackageReference Include="AWSSDK.SQS" Version="4.0.0" />`, "", 1),
			"AWSSDK.SQS is in the table but",
		},
	} {
		t.Run(name, func(t *testing.T) {
			err := checkDotnetSDKPins(types, csproj(tc.refs))
			if err == nil || !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), dotnetSDKTypesRefresh) {
				t.Errorf("err = %v, want it to say %q and name the refresh command", err, tc.want)
			}
		})
	}
}

// TestCommittedDotnetSDKTypesDeclareTheCloudWatchLogsDisagreement pins the
// fact the table was introduced for, read from the committed table rather than
// remembered: if a refresh ever stops showing it, the measured row in
// dotnetEpochMilliseconds is worth re-measuring.
func TestCommittedDotnetSDKTypesDeclareTheCloudWatchLogsDisagreement(t *testing.T) {
	types, err := loadCheckedDotnetSDKTypes(repoRoot)
	if err != nil {
		t.Fatal(err)
	}
	pkg, err := types.service("CloudWatch Logs")
	if err != nil {
		t.Fatal(err)
	}
	class, ok := pkg.class("InputLogEvent")
	if !ok {
		t.Fatal("the committed table has no InputLogEvent")
	}
	if ts := class["Timestamp"]; !ts.scalar("DateTime") {
		t.Errorf("InputLogEvent.Timestamp = %s, want DateTime?", ts.Raw)
	}
	if _, measured := dotnetEpochMilliseconds["CloudWatch Logs"]; !measured {
		t.Error("CloudWatch Logs is not recorded as measured")
	}
}
