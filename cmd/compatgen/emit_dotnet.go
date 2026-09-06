//go:build dev

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

// The dotnet-sdk source emitter — docs/plans/compat-coverage-modelgen.md §3.2
// D1, phase G3.
//
// The three interpreters execute the scenario IR at run time. The AWS SDK for
// .NET has no public dynamic-dispatch API, and the plan rejects reaching into
// its marshaller layer to fake one: the whole value of running eight suites is
// that each exercises its own real typed serialization path. So this file
// emits C# — one method per scenario test, each building a real typed request
// object and calling a real client method — which the dotnet-sdk suite's
// ordinary build compiles.
//
// What is emitted is the *data* plus the typed calls. Semantics — the context
// bag, value expressions, the closed check set, error matching, `eventually`,
// the six-field failure message — live once in the suite's hand-written
// Scenario/ namespace and are never re-emitted.
//
// # The naming table
//
// Everything this emitter knows about spelling C# is in the dotnetName*
// functions below, and `-explain -lang dotnet` (explain_typed.go) renders
// through the same dotnetInputLines, so the pseudo-code a reader reproduces a
// failure with is the source the emitter wrote. The table is deliberately
// tiny:
//
//	service   → namespace Amazon.<stem>, client Amazon<stem>Client (dotnetSDKStems)
//	operation → new <Op>Request() and client.<Op>Async(request)
//	member    → the property named <Member> with its first letter capitalized
//	value     → the member's *modeled* kind, spelled by dotnetSpeller
//
// # Why this backend reads the model where go-sdk reads the SDK
//
// emit_go.go loads the vendored SDK at generation time because `aws.String(v)`
// compiles only where smithy-go made that member a pointer, and the pinned
// shape snapshot cannot say whether it did. The .NET emitter faces the same
// question and answers it from the model, because three measured facts about
// the pinned AWSSDK major make the SDK's own declarations unnecessary:
//
//  1. **v4 made every value-typed member nullable.** `ReceiveMessageRequest`'s
//     MaxNumberOfMessages, VisibilityTimeout and WaitTimeSeconds are `int?`,
//     and `IsSet*` is now `!= null` rather than `!= 0`. A wire capture against
//     a local sink confirms the consequence: setting VisibilityTimeout and
//     WaitTimeSeconds to 0 sends `"VisibilityTimeout":0,"WaitTimeSeconds":0`,
//     and leaving them unset sends neither. So the zero-value refusal go-sdk
//     needs (compat/model/README.md § Values) has nothing to refuse here, and
//     the nullability that made the Go lookup necessary is uniform.
//  2. **C# target-typing spells the composites.** A collection expression
//     (`["All"]`), a target-typed `new()` and a target-typed `new() { ["k"] =
//     "v" }` take their element, structure and value types from the property
//     being assigned, so a list, a map and a nested structure are written
//     without naming a single SDK type.
//  3. **An enum is a ConstantClass with an implicit conversion from string.**
//     `request.Type = "SERVICE_CONTROL_POLICY"` and
//     `request.ChildType = b.Bind<string>(…)` both compile, for a constant and
//     for a deferred expression alike, so an enum member needs no type name
//     either.
//
// What that costs is stated rather than hidden: this backend cannot refuse an
// operation the vendored SDK does not declare, or a member it renamed, because
// it never asks the SDK. Those become a compile error in the suite's own build
// — loud, but suite-wide rather than scoped to one group. The pinned package
// versions in OvercastCompat.csproj are what keep them from arising; see that
// file's comment and cmd/compatgen/README.md § Source emitters.

// dotnetSuiteDir is where the emitted files live, repository-relative.
const dotnetSuiteDir = "compat/suites/dotnet-sdk/Groups"

// dotnetEmitReason is the refusal a member the emitter cannot express
// produces. Like go-sdk's it does not mean "no test": the operation is
// generated and the interpreters run it. It means this backend cannot compile
// it, so the whole group is scoped away from dotnet-sdk in the generated
// registry — a suite that cannot execute a group must not be listed as able
// to.
//
// Three things produce it, and all three are read off the model:
//
//	the member's modeled kind has no C# literal   a timestamp, blob, document,
//	                                              union, bigInteger or bigDecimal
//	a value expression on a composite member      $ref/$name resolve into one
//	                                              scalar slot, never a list
//	an integer literal outside the C# type's      C# checks an integral literal's
//	range                                         range at compile time, and a
//	                                              compile error here is
//	                                              suite-wide
const dotnetEmitReason = "dotnet-emit-unsupported"

// emitDotnet renders one service's generated groups as C# for the dotnet-sdk
// suite.
func emitDotnet(gen *generation) (*sourceEmission, error) {
	s := gen.scenario
	e := &sourceEmission{
		Path:    dotnetSuiteDir + "/" + dotnetFileName(gen.unit),
		Refused: map[string]bool{},
	}
	sp := &dotnetSpeller{model: gen.model}

	groups := make([]group, 0, len(s.Groups))
	for _, g := range s.Groups {
		if refusals := dotnetRefusals(gen, sp, g); len(refusals) > 0 {
			e.Refused[g.Name] = true
			e.Gaps = append(e.Gaps, refusals...)
			continue
		}
		groups = append(groups, g)
	}

	if err := dotnetMethodNamesAreUnique(gen.unit, groups); err != nil {
		return nil, err
	}

	class := dotnetNameClass(gen.unit)
	ns := dotnetNameNamespace(s.Client.SDKID)
	w := &sourceWriter{}
	w.linef("// Code generated by cmd/compatgen; DO NOT EDIT.")
	w.linef("")
	for _, using := range dotnetUsings(ns) {
		w.linef("using %s;", using)
	}
	w.linef("")
	w.linef("namespace OvercastCompat.Groups;")
	w.linef("")
	w.linef("/// <summary>The %s groups.</summary>", gen.describe())
	w.linef("/// <remarks>")
	w.linef("/// Generated from %s by cmd/compatgen. The semantics live in", gen.file)
	w.linef("/// OvercastCompat.Scenario; this file is the data and the typed SDK calls.")
	w.linef("/// </remarks>")
	w.linef("internal sealed class %s : IServiceGroup", class)
	w.linef("{")
	for _, g := range groups {
		w.linef("    private static readonly ScenarioGroup %s = new(%s, %s);",
			dotnetNameGroupField(g.Name), csString(g.Name), csString(gen.file))
	}
	if len(groups) > 0 {
		w.linef("")
	}
	w.linef("    private readonly Lazy<%s> _client;", dotnetNameClientClass(s.Client.SDKID))
	w.linef("")
	w.linef("    internal %s(AwsClients clients)", class)
	w.linef("    {")
	w.linef("        // A generated group builds its own client from the configuration the")
	w.linef("        // suite's hand-written groups share, rather than adding an accessor to")
	w.linef("        // AwsClients for every service the generator learns to cover. Nothing")
	w.linef("        // else about the client differs, and Lazy<T> builds it once however")
	w.linef("        // many of the group's tests run concurrently.")
	w.linef("        _client = new Lazy<%s>(() => clients.CreateClient(", dotnetNameClientClass(s.Client.SDKID))
	w.linef("            (credentials, configuration) => new %s(credentials, (%s)configuration),",
		dotnetNameClientClass(s.Client.SDKID), dotnetNameConfigClass(s.Client.SDKID))
	w.linef("            new %s()));", dotnetNameConfigClass(s.Client.SDKID))
	w.linef("    }")
	w.linef("")
	w.linef("    public string SourceName => %s;", csString(class))
	w.linef("")
	dotnetWriteMap(w, "TestFn", "Impls", func(w *sourceWriter) {
		for _, g := range groups {
			for _, t := range g.Tests {
				w.linef("        [%s] = %s,", csString(g.Name+":"+t.Name), dotnetNameTestMethod(g.Name, t.Name))
			}
		}
	})
	w.linef("")
	dotnetWriteMap(w, "SetupFn", "Setups", func(w *sourceWriter) {
		for _, g := range groups {
			w.linef("        [%s] = %s,", csString(g.Name), dotnetNameSetupMethod(g.Name))
		}
	})
	w.linef("")
	dotnetWriteMap(w, "SetupFn", "Teardowns", func(w *sourceWriter) {
		for _, g := range groups {
			w.linef("        [%s] = %s,", csString(g.Name), dotnetNameTeardownMethod(g.Name))
		}
	})
	w.linef("")
	w.linef("    private %s Cl() => _client.Value;", dotnetNameClientClass(s.Client.SDKID))

	for _, g := range groups {
		if err := dotnetWriteGroup(w, sp, g); err != nil {
			return nil, err
		}
	}
	w.linef("}")

	e.Contents = []byte(w.String())
	sortGaps(e.Gaps)
	return e, nil
}

// dotnetUsings is the emitted file's using block, sorted. The suite enables
// ImplicitUsings, so System, System.Collections.Generic, System.Threading and
// System.Threading.Tasks are already in scope and naming them again would be
// noise in every generated file.
func dotnetUsings(serviceNamespace string) []string {
	usings := []string{
		serviceNamespace,
		serviceNamespace + ".Model",
		"OvercastCompat.Clients",
		"OvercastCompat.Harness",
		"OvercastCompat.Scenario",
	}
	sort.Strings(usings)
	return usings
}

// dotnetWriteMap emits one of IServiceGroup's three registration maps.
func dotnetWriteMap(w *sourceWriter, value, name string, entries func(*sourceWriter)) {
	w.linef("    public IReadOnlyDictionary<string, %s> %s() => new Dictionary<string, %s>(StringComparer.Ordinal)", value, name, value)
	w.linef("    {")
	entries(w)
	w.linef("    };")
}

// dotnetWriteGroup emits one group: its setup and teardown hooks — registered
// even when empty, because an empty phase is a no-op and not a missing one —
// and one method per test.
func dotnetWriteGroup(w *sourceWriter, sp *dotnetSpeller, g group) error {
	field := dotnetNameGroupField(g.Name)

	w.linef("")
	w.linef("    private Task %s(TestContext t) =>", dotnetNameSetupMethod(g.Name))
	if err := dotnetWriteHookBody(w, sp, g, "RunSetupAsync", g.Setup); err != nil {
		return err
	}

	w.linef("")
	w.linef("    private Task %s(TestContext t) =>", dotnetNameTeardownMethod(g.Name))
	if err := dotnetWriteHookBody(w, sp, g, "RunTeardownAsync", g.Teardown); err != nil {
		return err
	}

	for _, t := range g.Tests {
		w.linef("")
		w.linef("    private Task %s(TestContext t) => %s.RunTestAsync(t, %s, new ScenarioTest",
			dotnetNameTestMethod(g.Name, t.Name), field, csString(t.Name))
		w.linef("    {")
		if err := dotnetWriteCall(w, sp, t.Call, "        ", "Call = "); err != nil {
			return err
		}
		w.linef("        Assert =")
		w.linef("        [")
		for i, a := range t.Assert {
			suffix := ","
			if i == len(t.Assert)-1 {
				suffix = ""
			}
			if err := dotnetWriteClause(w, sp, a, "            ", suffix); err != nil {
				return err
			}
		}
		w.linef("        ],")
		w.linef("    });")
	}
	return nil
}

func dotnetWriteHookBody(w *sourceWriter, sp *dotnetSpeller, g group, method string, calls []call) error {
	field := dotnetNameGroupField(g.Name)
	if len(calls) == 0 {
		phase := strings.ToLower(strings.TrimSuffix(strings.TrimPrefix(method, "Run"), "Async"))
		w.linef("        // No %s steps: an empty phase is a no-op, not a missing one.", phase)
		w.linef("        %s.%s(t);", field, method)
		return nil
	}
	w.linef("        %s.%s(t,", field, method)
	for i, c := range calls {
		suffix := ","
		if i == len(calls)-1 {
			suffix = ""
		}
		if err := dotnetWriteCallTerminated(w, sp, c, "            ", "", suffix); err != nil {
			return err
		}
	}
	w.linef("        );")
	return nil
}

// dotnetWriteCall emits one ScenarioCall: the operation, the params as the
// scenario file writes them, the typed request build, the client method and
// the exports.
func dotnetWriteCall(w *sourceWriter, sp *dotnetSpeller, c call, indent, prefix string) error {
	return dotnetWriteCallTerminated(w, sp, c, indent, prefix, ",")
}

func dotnetWriteCallTerminated(w *sourceWriter, sp *dotnetSpeller, c call, indent, prefix, suffix string) error {
	w.linef("%s%snew ScenarioCall", indent, prefix)
	w.linef("%s{", indent)
	w.linef("%s    Op = %s,", indent, csString(c.Op))
	raw, err := dotnetRawParams(c.Params)
	if err != nil {
		return err
	}
	w.linef("%s    Params = %s,", indent, raw)
	w.linef("%s    Build = b =>", indent)
	w.linef("%s    {", indent)
	lines, err := dotnetInputLines(sp, c.Op, c.Params, indent+"        ")
	if err != nil {
		return err
	}
	for _, line := range lines {
		w.linef("%s        %s", indent, line)
	}
	w.linef("%s        return request;", indent)
	w.linef("%s    },", indent)
	w.linef("%s    SendAsync = async request =>", indent)
	w.linef("%s        await Cl().%sAsync((%sRequest)request),", indent, c.Op, c.Op)
	if len(c.Export) > 0 {
		w.linef("%s    Export = new()", indent)
		w.linef("%s    {", indent)
		for _, path := range sortedStringKeys(c.Export) {
			w.linef("%s        [%s] = %s,", indent, csString(path), csString(c.Export[path]))
		}
		w.linef("%s    },", indent)
	}
	w.linef("%s}%s", indent, suffix)
	return nil
}

// dotnetWriteClause emits one assertion clause through the factories in the
// Scenario namespace, which are the same closed set ir.go builds.
//
// suffix is what follows the closing bracket — a comma between clauses, and
// nothing on the last one. C# has no trailing comma in an argument list, so
// every list of arguments this writes has to know which entry is last.
func dotnetWriteClause(w *sourceWriter, sp *dotnetSpeller, a assertion, indent, suffix string) error {
	switch a.Kind {
	case assertResponseField:
		w.linef("%sClause.ResponseField(", indent)
		dotnetWriteChecks(w, a.Checks, indent+"    ")
		w.linef("%s)%s", indent, suffix)
	case assertReadback:
		w.linef("%sClause.Readback(", indent)
		callSuffix := ","
		if len(a.Checks) == 0 {
			callSuffix = ""
		}
		if err := dotnetWriteCallTerminated(w, sp, *a.Call, indent+"    ", "", callSuffix); err != nil {
			return err
		}
		dotnetWriteChecks(w, a.Checks, indent+"    ")
		w.linef("%s)%s", indent, suffix)
	case assertListContains, assertAbsent:
		if a.Kind == assertAbsent && a.Error != nil {
			w.linef("%sClause.AbsentByError(", indent)
			if err := dotnetWriteCallTerminated(w, sp, *a.Call, indent+"    ", "", ","); err != nil {
				return err
			}
			w.linef("%s    new ErrorSpec(%s, %s))%s", indent, csString(a.Error.Shape), csString(a.Error.Code), suffix)
			return nil
		}
		name := "ListContains"
		if a.Kind == assertAbsent {
			name = "AbsentFromList"
		}
		w.linef("%sClause.%s(", indent, name)
		if a.Call == nil {
			w.linef("%s    null,", indent)
		} else if err := dotnetWriteCallTerminated(w, sp, *a.Call, indent+"    ", "", ","); err != nil {
			return err
		}
		paths := sortedValueKeys(a.Where)
		itemsSuffix := ""
		if len(paths) > 0 {
			itemsSuffix = ","
		}
		w.linef("%s    %s%s", indent, csString(a.ItemsPath), itemsSuffix)
		for i, path := range paths {
			value, err := dotnetValue(a.Where[path])
			if err != nil {
				return err
			}
			entrySuffix := ","
			if i == len(paths)-1 {
				entrySuffix = ""
			}
			w.linef("%s    new WhereEntry(%s, %s)%s", indent, csString(path), value, entrySuffix)
		}
		w.linef("%s)%s", indent, suffix)
	case assertErrorCode:
		w.linef("%sClause.ErrorCode(new ErrorSpec(%s, %s))%s", indent, csString(a.Error.Shape), csString(a.Error.Code), suffix)
	case assertEventually:
		w.linef("%sClause.Eventually(%d, %d,", indent, a.MaxAttempts, a.DelayMs)
		if err := dotnetWriteClause(w, sp, *a.Assert, indent+"    ", ""); err != nil {
			return err
		}
		w.linef("%s)%s", indent, suffix)
	default:
		return fmt.Errorf("cannot emit assertion kind %q", a.Kind)
	}
	return nil
}

// dotnetWriteChecks emits a clause's checks in path order, so a failure
// message is the same on every run and in every backend.
func dotnetWriteChecks(w *sourceWriter, checks map[string]check, indent string) {
	paths := sortedCheckPaths(checks)
	for i, path := range paths {
		suffix := ","
		if i == len(paths)-1 {
			suffix = ""
		}
		w.linef("%s%s%s", indent, dotnetCheck(path, checks[path]), suffix)
	}
}

func dotnetCheck(path string, c check) string {
	switch {
	case c.NonEmpty:
		return fmt.Sprintf("Check.NonEmpty(%s)", csString(path))
	case c.IsList:
		return fmt.Sprintf("Check.IsList(%s)", csString(path))
	case c.Missing:
		return fmt.Sprintf("Check.Missing(%s)", csString(path))
	case c.Matches != "":
		return fmt.Sprintf("Check.Matches(%s, %s)", csString(path), csString(c.Matches))
	default:
		value, err := dotnetValue(c.Equals)
		if err != nil {
			// Unreachable: dotnetValue is total over the IR's value grammar,
			// and validateAssertion has already rejected anything else.
			value = fmt.Sprintf("%#v", c.Equals)
		}
		// EqualTo rather than Equals: object declares a static
		// Equals(object, object), and an overload beside it reads as a
		// mistake even where the compiler resolves it correctly.
		return fmt.Sprintf("Check.EqualTo(%s, %s)", csString(path), value)
	}
}

// ---------------------------------------------------------------------------
// The naming table — shared with -explain -lang dotnet (explain_typed.go)
// ---------------------------------------------------------------------------

// dotnetSDKName overrides the SDK id where the AWS SDK for .NET spells a
// service by an older, longer name than its SDK id. compat/model/README.md
// § Naming lists the six known to break; this table carries the ones a
// scenario has actually needed, which is the plan's rule for every backend's
// override table (§3.2: added with the first scenario that names one).
//
// It has two fields rather than one because the two spellings diverge for
// three of the six: the STS namespace is Amazon.SecurityToken while its client
// is AmazonSecurityTokenServiceClient, IAM's are Amazon.IdentityManagement and
// AmazonIdentityManagementServiceClient, and DynamoDB's are Amazon.DynamoDBv2
// and AmazonDynamoDBClient. A single-stem table would name three clients that
// do not exist, so the remaining rows — STS, SSM and DynamoDB — are left to the
// scenario that needs them and to the `dotnet publish` that proves each
// spelling, rather than written blind from the README's summary.
var dotnetSDKName = map[string]struct{ namespace, client string }{
	// IAM: namespace Amazon.IdentityManagement, client
	// AmazonIdentityManagementServiceClient, config
	// AmazonIdentityManagementServiceConfig — the first row whose two stems
	// actually diverge, which is the reason the table carries both. Verified
	// by the dotnet-sdk suite's own image build against
	// AWSSDK.IdentityManagement 4.0.103.4 (#1883).
	"IAM": {namespace: "IdentityManagement", client: "IdentityManagementService"},
	// KMS: namespace Amazon.KeyManagementService, client
	// AmazonKeyManagementServiceClient, config AmazonKeyManagementServiceConfig.
	// Verified by the dotnet-sdk suite's own image build against
	// AWSSDK.KeyManagementService 4.0.0.
	"KMS": {namespace: "KeyManagementService", client: "KeyManagementService"},
	// SNS: namespace Amazon.SimpleNotificationService, client
	// AmazonSimpleNotificationServiceClient, config …Config. Verified by the
	// dotnet-sdk suite's image build against AWSSDK.SimpleNotificationService
	// 4.0.0 (#1900).
	"SNS": {namespace: "SimpleNotificationService", client: "SimpleNotificationService"},
}

// dotnetSDKStems returns the namespace and client stems for an SDK id: the id
// with spaces removed, unless dotnetSDKName overrides it.
func dotnetSDKStems(sdkID string) (namespace, client string) {
	if override, ok := dotnetSDKName[sdkID]; ok {
		return override.namespace, override.client
	}
	stem := pascalSDK(sdkID)
	return stem, stem
}

// dotnetNameNamespace is the AWS SDK for .NET namespace for a service: Amazon.
// plus the SDK id with spaces removed. SQS → Amazon.SQS, Organizations →
// Amazon.Organizations, KMS → Amazon.KeyManagementService, SNS →
// Amazon.SimpleNotificationService.
func dotnetNameNamespace(sdkID string) string {
	namespace, _ := dotnetSDKStems(sdkID)
	return "Amazon." + namespace
}

// dotnetNameClientClass is the service client: AmazonSQSClient.
func dotnetNameClientClass(sdkID string) string {
	_, client := dotnetSDKStems(sdkID)
	return "Amazon" + client + "Client"
}

// dotnetNameConfigClass is that client's configuration: AmazonSQSConfig.
func dotnetNameConfigClass(sdkID string) string {
	_, client := dotnetSDKStems(sdkID)
	return "Amazon" + client + "Config"
}

// dotnetNameProperty is the request property a modeled member is assigned
// through: the member name with its first letter capitalized. Almost every AWS
// member is already PascalCase, but not all — SQS models CreateQueue's tags as
// `tags` and ListDeadLetterSourceQueues' page as `queueUrls`, and the .NET SDK
// spells both with a capital.
func dotnetNameProperty(member string) string {
	if member == "" {
		return member
	}
	r := []rune(member)
	if !unicode.IsLower(r[0]) {
		return member
	}
	r[0] = unicode.ToUpper(r[0])
	return string(r)
}

// dotnetInputLines renders the statements that build one call's typed request.
// It is the emitter's Build body and, line for line, what `-explain -lang
// dotnet` prints, which is what keeps the two from drifting.
func dotnetInputLines(sp *dotnetSpeller, op string, params map[string]any, indent string) ([]string, error) {
	lines := []string{fmt.Sprintf("var request = new %sRequest();", op)}
	input := sp.model.InputShape(op)
	for _, member := range sortedValueKeys(params) {
		target, err := sp.target(input, op, member)
		if err != nil {
			return nil, err
		}
		value, err := sp.value(target, params[member], member, indent)
		if err != nil {
			return nil, fmt.Errorf("%s.%s: %w", op, member, err)
		}
		lines = append(lines, fmt.Sprintf("request.%s = %s;", dotnetNameProperty(member), value))
	}
	return lines, nil
}

// ---------------------------------------------------------------------------
// The type-spelling table
// ---------------------------------------------------------------------------

// dotnetSpeller renders one IR value as C# source, against the member's
// modeled kind. See this file's header for why the model is the authority here
// and the vendored SDK is the authority in emit_go.go.
type dotnetSpeller struct{ model *serviceModel }

// target resolves the shape a modeled member points at.
func (sp *dotnetSpeller) target(input, op, member string) (string, error) {
	if input == "" {
		return "", fmt.Errorf("%s has no modeled input, so it cannot carry member %q", op, member)
	}
	target, ok := sp.model.MemberTarget(input, member)
	if !ok {
		return "", fmt.Errorf("%s has no member %q", input, member)
	}
	return target, nil
}

// value renders one IR value as C# source for a member of the given shape.
//
// Nothing here names an SDK type: a composite is written as a collection
// expression or a target-typed `new()`, and an enum as the string its
// ConstantClass converts from. The property being assigned supplies the type,
// which is what makes the emitted source depend on the model alone.
func (sp *dotnetSpeller) value(target string, v any, member, indent string) (string, error) {
	if _, _, isExpr := exprOf(v); isExpr {
		return sp.expr(target, v, member)
	}
	kind := sp.model.Kind(target)
	if v == nil {
		switch kind {
		case "string", "enum", "list", "map", "structure":
			return "null", nil
		}
		return "", fmt.Errorf("null cannot be written into a %s member", kind)
	}
	switch kind {
	case "string", "enum":
		s, ok := v.(string)
		if !ok {
			return "", fmt.Errorf("a %s member wants a string, got %s", kind, valueKind(v))
		}
		return csString(s), nil
	case "boolean":
		out, ok := v.(bool)
		if !ok {
			return "", fmt.Errorf("a boolean member wants a boolean, got %s", valueKind(v))
		}
		return strconv.FormatBool(out), nil
	case "integer", "float":
		return sp.number(target, v)
	case "list":
		return sp.list(target, v, member, indent)
	case "map":
		return sp.mapping(target, v, member, indent)
	case "structure":
		return sp.structure(target, v, member, indent)
	}
	return "", fmt.Errorf("the dotnet-sdk emitter has no C# literal for a %s member", kind)
}

// number renders an integer or floating-point literal. The suffix is chosen
// from the shape's own Smithy type rather than from the value: C# widens an
// int literal to long, float and double implicitly, so only the narrower
// floating-point targets need one — and a `1.5` written into a `float?`
// property does not compile without it.
func (sp *dotnetSpeller) number(target string, v any) (string, error) {
	n, ok := numberOf(v)
	if !ok {
		return "", fmt.Errorf("a numeric member wants a number, got %s", valueKind(v))
	}
	shapeType := sp.model.ShapeType(target)
	switch shapeType {
	case "byte", "short", "integer", "intEnum", "long":
		if !n.Whole {
			return "", fmt.Errorf("a%s member wants a whole number, got %s", integerArticle(shapeType), n.Text)
		}
		r, known := csIntegralRange(shapeType)
		if !known {
			return "", fmt.Errorf("no C# literal builds a %s member", shapeType)
		}
		// A whole number wider than an int64 is out of range for every C#
		// integer type, and is refused before the type's own range is
		// consulted rather than being folded to one that fits. A literal
		// outside the property's range is a *compile* error in C#, and this
		// backend's compile errors are suite-wide rather than scoped to one
		// group — so it is refused here, where the cost is one group leaving
		// the dotnet-sdk column.
		if !n.Fits || n.Int < r[0] || n.Int > r[1] {
			return "", fmt.Errorf("%s is out of range for a%s member", n.Text, integerArticle(shapeType))
		}
		digits := strconv.FormatInt(n.Int, 10)
		if shapeType == "long" {
			return digits + "L", nil
		}
		return digits, nil
	case "float", "double":
		// javac and Roslyn agree here: a literal a float cannot carry is a
		// compile error rather than an infinity, and a double a float64 cannot
		// carry would be one.
		if !n.Representable || (shapeType == "float" && math.Abs(n.Float) > math.MaxFloat32) {
			return "", fmt.Errorf("%s is out of range for a %s member", n.Text, shapeType)
		}
		rendered := strconv.FormatFloat(n.Float, 'g', -1, 64)
		if shapeType == "float" {
			return rendered + "f", nil
		}
		return rendered, nil
	}
	// bigInteger and bigDecimal: the .NET SDK gives them no numeric property
	// type a C# literal builds, and the IR has no way to say which precision
	// was meant. Refuse rather than round.
	return "", fmt.Errorf("no C# literal builds a %s member", shapeType)
}

// csIntegralRange is the inclusive range of the C# type scalarType names for an
// integral shape type. Both are keyed by the shape type, so they cannot come to
// disagree about which C# type a member has — and note that C#'s `byte` is the
// unsigned one, which is why its range starts at zero.
func csIntegralRange(shapeType string) (r [2]int64, known bool) {
	switch shapeType {
	case "byte":
		return [2]int64{0, math.MaxUint8}, true
	case "short":
		return [2]int64{math.MinInt16, math.MaxInt16}, true
	case "integer", "intEnum":
		return [2]int64{math.MinInt32, math.MaxInt32}, true
	case "long":
		return [2]int64{math.MinInt64, math.MaxInt64}, true
	}
	return [2]int64{}, false
}

// integerArticle names an integral shape type in a message, with its article:
// "an integer", "a long".
func integerArticle(shapeType string) string {
	if strings.HasPrefix(shapeType, "i") {
		return "n " + shapeType
	}
	return " " + shapeType
}

func (sp *dotnetSpeller) list(target string, v any, member, indent string) (string, error) {
	items, ok := v.([]any)
	if !ok {
		return "", fmt.Errorf("a list member wants a JSON array, got %s", valueKind(v))
	}
	element := sp.model.ElementTarget(target)
	if element == "" {
		return "", fmt.Errorf("the model gives list shape %s no member shape", target)
	}
	rendered := make([]string, 0, len(items))
	for _, item := range items {
		out, err := sp.value(element, item, member, indent+"    ")
		if err != nil {
			return "", err
		}
		rendered = append(rendered, out)
	}
	// A collection expression takes its element type from the property being
	// assigned, so neither List<T> nor T is named here.
	return csList(rendered, indent), nil
}

func (sp *dotnetSpeller) mapping(target string, v any, member, indent string) (string, error) {
	entries, ok := v.(map[string]any)
	if !ok {
		return "", fmt.Errorf("a map member wants a JSON object, got %s", valueKind(v))
	}
	key := sp.model.KeyTarget(target)
	if kind := sp.model.Kind(key); kind != "string" && kind != "enum" {
		return "", fmt.Errorf("a map member keyed by %s has no IR spelling; the IR's objects have string keys", kind)
	}
	value := sp.model.ValueTarget(target)
	if value == "" {
		return "", fmt.Errorf("the model gives map shape %s no value shape", target)
	}
	rendered := make([]string, 0, len(entries))
	for _, k := range sortedKeys(entries) {
		out, err := sp.value(value, entries[k], member, indent+"    ")
		if err != nil {
			return "", err
		}
		rendered = append(rendered, "["+csString(k)+"] = "+out)
	}
	return csInitializer(rendered, indent), nil
}

func (sp *dotnetSpeller) structure(target string, v any, member, indent string) (string, error) {
	members, ok := v.(map[string]any)
	if !ok {
		return "", fmt.Errorf("a structure member wants a JSON object, got %s", valueKind(v))
	}
	rendered := make([]string, 0, len(members))
	for _, k := range sortedKeys(members) {
		field, ok := sp.model.MemberTarget(target, k)
		if !ok {
			return "", fmt.Errorf("%s has no member %q", target, k)
		}
		out, err := sp.value(field, members[k], member, indent+"    ")
		if err != nil {
			return "", err
		}
		rendered = append(rendered, dotnetNameProperty(k)+" = "+out)
	}
	return csInitializer(rendered, indent), nil
}

// expr renders a deferred value expression into a typed slot.
//
// The expression itself is still the IR's — Val.Ref, Val.Name and the rest,
// rendered by dotnetValue — and it still resolves through the run's context
// bag. Binder.Bind converts the result to the one C# scalar type the member's
// modeled kind names; the conversion to a ConstantClass enum, and the widening
// into a nullable property, are the compiler's, not this emitter's.
func (sp *dotnetSpeller) expr(target string, v any, member string) (string, error) {
	scalar, err := sp.scalarType(target)
	if err != nil {
		return "", err
	}
	rendered, err := dotnetValue(v)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("b.Bind<%s>(%s, %s)", scalar, csString(member), rendered), nil
}

// scalarType is the C# type Bind is instantiated with for a member's modeled
// kind. It is the .NET counterpart of smithy-go's scalar mapping, and an enum
// reaches it as its underlying string: ConstantClass declares an implicit
// conversion from string, so the emitted assignment needs no cast.
func (sp *dotnetSpeller) scalarType(target string) (string, error) {
	switch sp.model.ShapeType(target) {
	case "string":
		return "string", nil
	case "enum":
		return "string", nil
	case "boolean":
		return "bool", nil
	case "byte":
		return "byte", nil
	case "short":
		return "short", nil
	case "integer", "intEnum":
		return "int", nil
	case "long":
		return "long", nil
	case "float":
		return "float", nil
	case "double":
		return "double", nil
	}
	return "", fmt.Errorf("a value expression can only be bound to a scalar member, and this one is a %s", sp.model.Kind(target))
}

// ---------------------------------------------------------------------------
// Untyped values
// ---------------------------------------------------------------------------

// dotnetValue renders one IR value as an *untyped* C# expression: an object is
// a Dictionary<string, object?>, a list an object?[], a scalar itself, and
// each of the five expression forms a Val constructor. Nothing else is
// representable, which is what makes this total.
//
// Untyped is right in the two places it is used. An assertion's expected value
// is compared in the IR's own type system against a response read back as a
// document, so it is data on both sides. And the argument of a value
// expression — a $concat part, the list a $index takes — is evaluated at run
// time, where there is no property type to spell it against; only the
// expression's *result* has one, which is what Binder.Bind converts it to.
// Request members themselves go through dotnetSpeller instead.
func dotnetValue(v any) (string, error) {
	if key, arg, ok := exprOf(v); ok {
		switch key {
		case "$lit":
			inner, err := dotnetValue(arg)
			if err != nil {
				return "", err
			}
			return fmt.Sprintf("Val.Lit(%s)", inner), nil
		case "$ref":
			return fmt.Sprintf("Val.Ref(%s)", csString(arg.(string))), nil
		case "$name":
			return fmt.Sprintf("Val.Name(%s)", csString(arg.(string))), nil
		case "$concat":
			var parts []string
			for _, part := range arg.([]any) {
				rendered, err := dotnetValue(part)
				if err != nil {
					return "", err
				}
				parts = append(parts, rendered)
			}
			return fmt.Sprintf("Val.Concat(%s)", strings.Join(parts, ", ")), nil
		case "$index":
			pair := arg.([]any)
			inner, err := dotnetValue(pair[0])
			if err != nil {
				return "", err
			}
			n, err := integerOf(pair[1])
			if err != nil {
				return "", err
			}
			return fmt.Sprintf("Val.Index(%s, %d)", inner, n), nil
		}
	}
	switch value := v.(type) {
	case nil:
		return "null", nil
	case string:
		return csString(value), nil
	case bool:
		return strconv.FormatBool(value), nil
	case json.Number:
		return value.String(), nil
	case float64:
		return strconv.FormatFloat(value, 'g', -1, 64), nil
	case []any:
		items := make([]string, 0, len(value))
		for _, item := range value {
			rendered, err := dotnetValue(item)
			if err != nil {
				return "", err
			}
			items = append(items, rendered)
		}
		return "new object?[] { " + strings.Join(items, ", ") + " }", nil
	case map[string]any:
		var entries []string
		for _, k := range sortedKeys(value) {
			rendered, err := dotnetValue(value[k])
			if err != nil {
				return "", err
			}
			entries = append(entries, "["+csString(k)+"] = "+rendered)
		}
		return "new Dictionary<string, object?> { " + strings.Join(entries, ", ") + " }", nil
	}
	return "", fmt.Errorf("no C# expression for %T", v)
}

// dotnetRawParams renders a call's params as the scenario file writes them —
// expressions unevaluated — for failure-message field 3 when a value could not
// be evaluated and nothing was sent. It is the same canonical JSON the
// interpreters print in that case.
func dotnetRawParams(params map[string]any) (string, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if params == nil {
		params = map[string]any{}
	}
	if err := enc.Encode(params); err != nil {
		return "", err
	}
	return csString(strings.TrimRight(buf.String(), "\n")), nil
}

// ---------------------------------------------------------------------------
// Identifiers
// ---------------------------------------------------------------------------

func dotnetFileName(service string) string { return dotnetNameClass(service) + "Gen.cs" }

func dotnetNameClass(service string) string { return "Scenarios" + camel(service) }

func dotnetNameGroupField(group string) string { return "Group" + camel(group) }

func dotnetNameSetupMethod(group string) string { return "Setup" + camel(group) }

func dotnetNameTeardownMethod(group string) string { return "Teardown" + camel(group) }

func dotnetNameTestMethod(group, test string) string {
	return "Test" + camel(group) + test
}

// dotnetMethodNamesAreUnique refuses a service whose group and test names
// collide once folded into C# identifiers. Two names differing only in where
// their hyphens fall would otherwise emit two methods of the same name, and the
// suite would fail to build with no indication of which pair caused it.
//
// The collision itself is uniqueNames (emit_shared.go); what is C#'s own is
// which identifiers a group claims.
func dotnetMethodNamesAreUnique(service string, groups []group) error {
	var claims []nameClaim
	for _, g := range groups {
		claims = append(claims,
			nameClaim{dotnetNameSetupMethod(g.Name), g.Name + " setup"},
			nameClaim{dotnetNameTeardownMethod(g.Name), g.Name + " teardown"})
		for _, t := range g.Tests {
			claims = append(claims, nameClaim{dotnetNameTestMethod(g.Name, t.Name), g.Name + "/" + t.Name})
		}
	}
	return uniqueNames(service, "C#", claims)
}

// ---------------------------------------------------------------------------
// Refusals
// ---------------------------------------------------------------------------

// dotnetRefusals reports the members of a group's calls this backend cannot
// express. A group with any is not emitted and is scoped away from dotnet-sdk.
//
// It decides by attempting the very spelling emission would write, so one code
// path answers "can this be emitted" and "how" and the two cannot drift.
func dotnetRefusals(gen *generation, sp *dotnetSpeller, g group) []gap {
	return refusals(gen, g, dotnetEmitReason, refusalChecks{
		member: func(op, member string, v any) error {
			target, err := sp.target(gen.model.InputShape(op), op, member)
			if err != nil {
				return err
			}
			if _, err := sp.value(target, v, member, ""); err != nil {
				return fmt.Errorf("%s.%s cannot be spelled as C#: %v", op, member, err)
			}
			return nil
		},
	})
}

// ---------------------------------------------------------------------------
// The index file
// ---------------------------------------------------------------------------

// dotnetIndexPath is the file Program.cs builds the scenario backend from. It
// is emitted whether or not dotnet-sdk is a scenario backend, so the project
// compiles either way.
var dotnetIndexPath = dotnetSuiteDir + "/ScenariosGen.cs"

// emitDotnetIndex renders the list of generated service group classes.
func emitDotnetIndex(services []string) []byte {
	sorted := append([]string(nil), services...)
	sort.Strings(sorted)
	w := &sourceWriter{}
	w.linef("// Code generated by cmd/compatgen; DO NOT EDIT.")
	w.linef("")
	w.linef("using OvercastCompat.Clients;")
	w.linef("")
	w.linef("namespace OvercastCompat.Groups;")
	w.linef("")
	w.linef("/// <summary>Every generated service group, in service order.</summary>")
	w.linef("/// <remarks>")
	w.linef("/// Program.cs registers these as the suite's ScenarioBackend rather than")
	w.linef("/// merging them into the hand-written impl map: a generated group is resolved")
	w.linef("/// by the loader's backend hook, which is the last step before the")
	w.linef("/// no-backend sentinel.")
	if len(sorted) == 0 {
		// Said only where it is true: a remark claiming the list is empty,
		// printed above a list of two entries, is worse than no remark.
		w.linef("/// <para>The list is empty because cmd/compatgen's scenarioBackends")
		w.linef("/// table does not name dotnet-sdk.</para>")
	}
	w.linef("/// </remarks>")
	w.linef("internal static class ScenarioGroups")
	w.linef("{")
	if len(sorted) == 0 {
		w.linef("    internal static IServiceGroup[] All(AwsClients clients) => [];")
	} else {
		w.linef("    internal static IServiceGroup[] All(AwsClients clients) =>")
		w.linef("    [")
		for _, service := range sorted {
			w.linef("        new %s(clients),", dotnetNameClass(service))
		}
		w.linef("    ];")
	}
	w.linef("}")
	return []byte(w.String())
}

// ---------------------------------------------------------------------------
// Writing
// ---------------------------------------------------------------------------

// There is no gofmt for C# and the suite runs no formatter, so the layout the
// functions above write is the layout committed — which is why each carries
// its own indent rather than leaving it to a tool.

// csValueWidth is how wide a composite may be before it is written one entry
// per line. A generated file nobody can read in a diff is a generated file
// nobody reviews.
const csValueWidth = 80

// csList renders a collection expression, on one line while that is readable
// and one element per line when it is not. The element type is never named:
// the property being assigned supplies it.
func csList(entries []string, indent string) string {
	if len(entries) == 0 {
		return "[]"
	}
	inline := "[" + strings.Join(entries, ", ") + "]"
	if len(indent)+len(inline) <= csValueWidth {
		return inline
	}
	var b strings.Builder
	b.WriteString("[")
	for _, entry := range entries {
		b.WriteString("\n" + indent + "    " + entry + ",")
	}
	b.WriteString("\n" + indent + "]")
	return b.String()
}

// csInitializer renders a target-typed object or collection initializer, which
// is how a map and a nested structure are written without naming their types.
func csInitializer(entries []string, indent string) string {
	if len(entries) == 0 {
		return "new()"
	}
	inline := "new() { " + strings.Join(entries, ", ") + " }"
	if len(indent)+len(inline) <= csValueWidth {
		return inline
	}
	var b strings.Builder
	b.WriteString("new()\n" + indent + "{")
	for _, entry := range entries {
		b.WriteString("\n" + indent + "    " + entry + ",")
	}
	b.WriteString("\n" + indent + "}")
	return b.String()
}

// csString renders a Go string as a C# string literal. Everything outside the
// printable ASCII range is escaped rather than written through: a generated
// file is read in diffs and reviewed on terminals, and the IR's own values are
// ASCII.
func csString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			switch {
			// C# reads exactly four hex digits after \u, so a rune above the
			// BMP needs the eight-digit form: \u1F600 would be read as U+1F60
			// followed by a literal "0".
			case r > 0xFFFF:
				fmt.Fprintf(&b, `\U%08X`, r)
			case r < 0x20 || r > 0x7e:
				fmt.Fprintf(&b, `\u%04X`, r)
			default:
				b.WriteRune(r)
			}
			continue
		}
	}
	b.WriteByte('"')
	return b.String()
}
