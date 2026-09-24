//go:build dev

package main

import (
	"bytes"
	"encoding/json"
	"errors"
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
//	value     → the member's modeled kind and the property's AWSSDK type,
//	            spelled by dotnetSpeller
//
// # Why this backend reads the SDK too, and how
//
// emit_go.go loads the vendored SDK at generation time because `aws.String(v)`
// compiles only where smithy-go made that member a pointer, and the pinned
// shape snapshot cannot say whether it did (#1831). This emitter used to answer
// the same question from the model alone, on three measured facts about the
// pinned AWSSDK major, and all three still hold:
//
//  1. **v4 made every value-typed member nullable.** `ReceiveMessageRequest`'s
//     MaxNumberOfMessages, VisibilityTimeout and WaitTimeSeconds are `int?`,
//     and `IsSet*` is now `!= null` rather than `!= 0`. A wire capture against
//     a local sink confirms the consequence: setting VisibilityTimeout and
//     WaitTimeSeconds to 0 sends `"VisibilityTimeout":0,"WaitTimeSeconds":0`,
//     and leaving them unset sends neither. So the zero-value refusal go-sdk
//     needs (compat/model/README.md § Values) has nothing to refuse here.
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
// What they do not cover is a member AWSSDK customizes away from its modeled
// kind. AWSSDK.CloudWatchLogs types InputLogEvent.Timestamp as DateTime? where
// the model says long, so a long written from the model fails the suite's build
// with CS0029 (#2132). So each member is now spelled against the type AWSSDK
// gives its property, read from the SDK type table (dotnetsdktypes.go) — the
// dotnet-sdk suite's own reflection over the assemblies it pins, committed,
// because this program runs where there is no .NET SDK to load them with. Where
// the model and the SDK agree the spelling is what it always was; where they
// disagree the one way that is measured (dotnetEpochMilliseconds) it is a
// conversion; anywhere else it is a refusal.
//
// Reading the SDK also turns two suite-wide compile errors into refusals scoped
// to one group, as go-sdk's lookup does: an operation the pinned package does
// not declare, and a member it renamed or dropped.

// dotnetSuiteDir is where the emitted files live, repository-relative.
const dotnetSuiteDir = "compat/suites/dotnet-sdk/Groups"

// dotnetEmitReason is the refusal a member the emitter cannot express
// produces. Like go-sdk's it does not mean "no test": the operation is
// generated and the interpreters run it. It means this backend cannot compile
// it, so the whole group is scoped away from dotnet-sdk in the generated
// registry — a suite that cannot execute a group must not be listed as able
// to.
//
// What produces it:
//
//	the member's modeled kind has no C# literal   a timestamp, document, union,
//	                                              bigInteger or bigDecimal (a
//	                                              blob is a MemoryStream — see
//	                                              dotnetBlob)
//	a value expression on a composite member      $ref/$name resolve into one
//	                                              scalar slot, never a list
//	an integer literal outside the C# type's      C# checks an integral literal's
//	range                                         range at compile time, and a
//	                                              compile error here is
//	                                              suite-wide
//	the pinned package lacks the operation or     recorded as :<Op>Request, or
//	the member                                    under the member
//	the model and AWSSDK disagree about the       a DateTime over a long in a
//	member's type in a way with no measured       service whose unit is not
//	spelling                                      measured, or any other pair
//	a request or response property whose          recorded under the property:
//	document form would differ from the other     the same DateTime-over-long,
//	backends'                                     read rather than sent
const dotnetEmitReason = "dotnet-emit-unsupported"

// emitDotnet renders one service's generated groups as C# for the dotnet-sdk
// suite.
func emitDotnet(gen *generation, table *dotnetSDKTypes) (*sourceEmission, error) {
	s := gen.scenario
	e := &sourceEmission{
		Path:    dotnetSuiteDir + "/" + dotnetFileName(gen.unit),
		Refused: map[string]bool{},
	}
	pkg, err := table.service(s.Client.SDKID)
	if err != nil {
		return nil, err
	}
	sp := newDotnetSpeller(gen.model, pkg, s.Client.SDKID)

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

	// Every request and response property the emitted groups touch whose
	// document form Documents has to be told (dotnetEpochMillisecondsProperties).
	// The refusals above have already proved each one renderable.
	epochMilliseconds := map[string]map[string]bool{}
	for _, g := range groups {
		for _, c := range callsOf(g) {
			if err := sp.dotnetEpochMillisecondsProperties(c, epochMilliseconds); err != nil {
				return nil, err
			}
		}
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
	dotnetWriteEpochMilliseconds(w, sp.sdk, epochMilliseconds)
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

// dotnetWriteEpochMilliseconds emits the constructor's registrations of the
// properties Documents renders as epoch milliseconds, one call per class, in
// class and property order. Each property is named through nameof, so a table
// that disagreed with the pinned package would be a compile error rather than a
// registration that silently matched nothing. Nothing is written for a service
// that has none, which is every service but one today.
func dotnetWriteEpochMilliseconds(w *sourceWriter, sdk *dotnetSDKPackage, classes map[string]map[string]bool) {
	if len(classes) == 0 {
		return
	}
	w.linef("        // Epoch-millisecond longs in the model, typed as DateTime by")
	w.linef("        // %s. Every other backend reads the number the service sent,", sdk.describe())
	w.linef("        // so that number is their document form here too")
	w.linef("        // (compat/model/README.md § Values).")
	for _, className := range sortedStringKeys(classes) {
		qualified := sdk.Namespace + "." + className
		args := []string{"typeof(" + qualified + ")"}
		for _, property := range sortedStringKeys(classes[className]) {
			args = append(args, "nameof("+qualified+"."+property+")")
		}
		w.linef("        Documents.EpochMilliseconds(")
		for i, arg := range args {
			suffix := ","
			if i == len(args)-1 {
				suffix = ");"
			}
			w.linef("            %s%s", arg, suffix)
		}
	}
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
	class, err := sp.requestClass(op)
	if err != nil {
		return nil, err
	}
	lines := []string{fmt.Sprintf("var request = new %sRequest();", op)}
	input := sp.model.InputShape(op)
	for _, member := range sortedValueKeys(params) {
		target, err := sp.target(input, op, member)
		if err != nil {
			return nil, err
		}
		property, err := sp.property(class, op+"Request", member)
		if err != nil {
			return nil, err
		}
		value, err := sp.value(target, property, params[member], member, indent)
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
// modeled kind *and* the type AWSSDK gives the property it is assigned to. See
// this file's header for why both are read.
type dotnetSpeller struct {
	model *serviceModel
	// sdk is the service's slice of the SDK type table (dotnetsdktypes.go).
	sdk *dotnetSDKPackage
	// epochMilliseconds is set for a service whose AWSSDK package is measured
	// to carry an epoch-milliseconds long as a DateTime — see
	// dotnetEpochMilliseconds.
	epochMilliseconds bool
}

// newDotnetSpeller returns the speller for one service. A measurement counts
// only for the package it was taken on: a row naming another package than the
// one the service resolved to measured something else.
func newDotnetSpeller(model *serviceModel, sdk *dotnetSDKPackage, sdkID string) *dotnetSpeller {
	pkg, measured := dotnetEpochMilliseconds[sdkID]
	return &dotnetSpeller{model: model, sdk: sdk, epochMilliseconds: measured && pkg == sdk.Package}
}

// dotnetEpochMilliseconds names the services whose AWSSDK package types a
// modeled `long` of epoch milliseconds as a DateTime, keyed by SDK id — the one
// disagreement between the model and the SDK this emitter spells rather than
// refuses.
//
// It is a table of measurements, not a rule. The SDK type table says a property
// is a DateTime where the model says long; it cannot say what the SDK puts on
// the wire for it, and a customization that meant seconds would be spelled
// wrongly by a rule that assumed milliseconds and would still compile. So a
// service is added here with the wire test that proves the unit, and anywhere
// else the same disagreement is refused (dotnet-emit-unsupported) rather than
// guessed at.
var dotnetEpochMilliseconds = map[string]string{ // SDK id → the package measured
	// AWSSDK.CloudWatchLogs 4.0.0: InputLogEvent.Timestamp, LogStream's and
	// LogGroup's CreationTime and the other `Timestamp`-shaped members are
	// DateTime? over the model's long. Measured both ways by
	// SdkWireFormTests: PutLogEvents sends the DateTime as its epoch
	// milliseconds, and DescribeLogStreams reads a number back as the DateTime
	// that many milliseconds after the epoch (#1116, #2132).
	"CloudWatch Logs": "AWSSDK.CloudWatchLogs",
}

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

// requestClass returns the properties of an operation's request class, or an
// error saying the pinned package does not declare one — a generation-time
// refusal where it would otherwise be a suite-wide compile error.
func (sp *dotnetSpeller) requestClass(op string) (map[string]dotnetType, error) {
	class, ok := sp.sdk.class(op + "Request")
	if !ok {
		return nil, fmt.Errorf("%s declares no %s.%sRequest; the operation is newer than the pin in %s",
			sp.sdk.describe(), sp.sdk.Namespace, op, dotnetCsprojPath)
	}
	return class, nil
}

// property returns the type AWSSDK gives the property a modeled member is
// assigned through.
func (sp *dotnetSpeller) property(class map[string]dotnetType, className, member string) (dotnetType, error) {
	name := dotnetNameProperty(member)
	t, ok := class[name]
	if !ok {
		return dotnetType{}, fmt.Errorf("%s declares no property %s.%s for member %q", sp.sdk.describe(), className, name, member)
	}
	return t, nil
}

// dotnetNoLiteralKinds are the modeled kinds with no C# literal whatever the
// SDK's type: the IR has no portable value of them (compat/model/README.md
// § Values), so the binder never binds one and this is a backstop.
var dotnetNoLiteralKinds = map[string]bool{"timestamp": true, "document": true, "union": true}

// dotnetSlot is how one member is spelled, decided by the modeled kind and the
// SDK's property type together.
type dotnetSlot struct {
	// form is string, bool, integer, float, epochMilliseconds, blob, list, map
	// or structure.
	form string
	// scalar is the C# type a literal is range-checked against and Bind is
	// instantiated with: string, bool, byte, short, int, long, float, double.
	scalar string
}

// dotnetIntegral is the inclusive range of each C# integral type the table can
// name, and whether Binder.Bind can produce it. `byte` is C#'s unsigned byte;
// ulong's range is cut at int64's, the widest a scenario literal carries.
var dotnetIntegral = map[string]struct {
	min, max int64
	bindable bool
}{
	"byte":   {0, math.MaxUint8, true},
	"sbyte":  {math.MinInt8, math.MaxInt8, false},
	"short":  {math.MinInt16, math.MaxInt16, true},
	"ushort": {0, math.MaxUint16, false},
	"int":    {math.MinInt32, math.MaxInt32, true},
	"uint":   {0, math.MaxUint32, false},
	"long":   {math.MinInt64, math.MaxInt64, true},
	"ulong":  {0, math.MaxInt64, false},
}

// slot matches a member's modeled kind against the SDK's type for it. Where
// the two agree the spelling follows the SDK's type; where they disagree in the
// one measured way (dotnetEpochMilliseconds) it is a conversion; anywhere else
// it is an error naming both, which refuses the group rather than emitting
// source that does not compile.
func (sp *dotnetSpeller) slot(target string, t dotnetType) (dotnetSlot, error) {
	kind := sp.model.Kind(target)
	shapeType := sp.model.ShapeType(target)
	switch kind {
	case "string", "enum":
		if t.scalar("string") || t.Form == "enum" {
			return dotnetSlot{form: "string", scalar: "string"}, nil
		}
	case "boolean":
		if t.scalar("bool") {
			return dotnetSlot{form: "bool", scalar: "bool"}, nil
		}
	case "integer":
		if shapeType == "bigInteger" {
			// The IR has no way to say which precision was meant, and the .NET
			// SDK gives the shape no numeric property a literal builds.
			return dotnetSlot{}, fmt.Errorf("no C# literal builds a %s member", shapeType)
		}
		if _, integral := dotnetIntegral[t.Name]; t.Form == "scalar" && integral {
			return dotnetSlot{form: "integer", scalar: t.Name}, nil
		}
		if t.scalar("DateTime") && shapeType == "long" {
			if !sp.epochMilliseconds {
				return dotnetSlot{}, fmt.Errorf("the model says long and %s types it %s; that is spelled only where the wire unit is measured (dotnetEpochMilliseconds), and it has not been for this service",
					sp.sdk.describe(), t.Raw)
			}
			return dotnetSlot{form: "epochMilliseconds", scalar: "long"}, nil
		}
	case "float":
		if shapeType == "bigDecimal" {
			return dotnetSlot{}, fmt.Errorf("no C# literal builds a %s member", shapeType)
		}
		if t.scalar("float") || t.scalar("double") {
			return dotnetSlot{form: "float", scalar: t.Name}, nil
		}
	case "blob":
		if t.scalar("MemoryStream") {
			return dotnetSlot{form: "blob"}, nil
		}
	case "list":
		if t.Form == "list" {
			return dotnetSlot{form: "list"}, nil
		}
	case "map":
		if t.Form == "map" {
			return dotnetSlot{form: "map"}, nil
		}
	case "structure":
		if t.Form == "class" {
			if _, ok := sp.sdk.class(t.Name); !ok {
				return dotnetSlot{}, fmt.Errorf("%s names class %s, which the table does not declare", sp.sdk.describe(), t.Name)
			}
			return dotnetSlot{form: "structure"}, nil
		}
	}
	return dotnetSlot{}, fmt.Errorf("the model says %s and %s types it %s, and the emitter has no spelling of one as the other",
		shapeType, sp.sdk.describe(), t.Raw)
}

// value renders one IR value as C# source for a member of the given shape,
// assigned to a property of the given SDK type.
//
// Nothing here names an SDK type unless it has to: a composite is written as a
// collection expression or a target-typed `new()`, and an enum as the string
// its ConstantClass converts from, so the property being assigned supplies the
// type. What the SDK's type settles is the scalar underneath — which C# type a
// literal is range-checked against and Bind is instantiated with — and the one
// conversion the model's kind does not predict.
func (sp *dotnetSpeller) value(target string, t dotnetType, v any, member, indent string) (string, error) {
	kind := sp.model.Kind(target)
	if dotnetNoLiteralKinds[kind] {
		return "", fmt.Errorf("the dotnet-sdk emitter has no C# literal for a %s member", kind)
	}
	slot, err := sp.slot(target, t)
	if err != nil {
		return "", err
	}
	if slot.form == "blob" {
		return dotnetBlob(v, member)
	}
	if _, _, isExpr := exprOf(v); isExpr {
		return sp.expr(target, slot, v, member)
	}
	if v == nil {
		switch slot.form {
		case "string", "list", "map", "structure":
			return "null", nil
		}
		return "", fmt.Errorf("null cannot be written into a %s member", kind)
	}
	switch slot.form {
	case "string":
		s, ok := v.(string)
		if !ok {
			return "", fmt.Errorf("a %s member wants a string, got %s", kind, valueKind(v))
		}
		return csString(s), nil
	case "bool":
		out, ok := v.(bool)
		if !ok {
			return "", fmt.Errorf("a boolean member wants a boolean, got %s", valueKind(v))
		}
		return strconv.FormatBool(out), nil
	case "integer", "float", "epochMilliseconds":
		return sp.number(target, slot, v)
	case "list":
		return sp.list(target, *t.Elem, v, member, indent)
	case "map":
		return sp.mapping(target, t, v, member, indent)
	case "structure":
		return sp.structure(target, t.Name, v, member, indent)
	}
	return "", fmt.Errorf("internal: no spelling for a %s slot", slot.form)
}

// dotnetBlob renders a `$base64` value into a blob member, which AWSSDK for
// .NET types as a MemoryStream.
//
// C# has no byte-string literal for arbitrary bytes (a u8 literal is UTF-8
// text only), so a literal is spelled as a MemoryStream over the scenario's
// own base64 text, decoded by the platform — the same text the scenario file
// writes, so the emitted line greps against it — and the generator has already
// proved that text canonical. Both types are written fully qualified rather
// than relying on the project's implicit usings. A `$base64` around a $ref is
// deferred like every other expression, through Binder.Blob.
func dotnetBlob(v any, member string) (string, error) {
	key, arg, isExpr := exprOf(v)
	if !isExpr || key != "$base64" {
		return "", fmt.Errorf("a blob member takes $base64, got %s", valueKind(v))
	}
	if text, literal := arg.(string); literal {
		if _, err := decodeBase64(text); err != nil {
			return "", fmt.Errorf("$base64 %q %w", text, err)
		}
		return "new System.IO.MemoryStream(System.Convert.FromBase64String(" + csString(text) + "))", nil
	}
	rendered, err := dotnetValue(v)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("b.Blob(%s, %s)", csString(member), rendered), nil
}

// dotnetEpochMillisecondsRange is the inclusive range of
// DateTimeOffset.FromUnixTimeMilliseconds: 0001-01-01 to 9999-12-31, the
// range of a DateTime. A literal outside it throws when the suite runs.
var dotnetEpochMillisecondsRange = [2]int64{-62135596800000, 253402300799999}

// number renders an integer, floating-point or epoch-milliseconds literal.
// Its range is the SDK property's C# type's, not the model's: C# checks an
// integral literal against the type it is assigned to at compile time, and a
// compile error in this backend is suite-wide rather than scoped to one group,
// so a literal outside it is refused here, where the cost is one group leaving
// the dotnet-sdk column.
//
// A long is written with an `L` and a float with an `f`: C# widens an int
// literal into a long, a float and a double implicitly, but a `1.5` written
// into a `float?` does not compile without its suffix.
func (sp *dotnetSpeller) number(target string, slot dotnetSlot, v any) (string, error) {
	n, ok := numberOf(v)
	if !ok {
		return "", fmt.Errorf("a numeric member wants a number, got %s", valueKind(v))
	}
	shapeType := sp.model.ShapeType(target)
	switch slot.form {
	case "integer", "epochMilliseconds":
		if !n.Whole {
			return "", fmt.Errorf("a%s member wants a whole number, got %s", integerArticle(shapeType), n.Text)
		}
		r := [2]int64{dotnetIntegral[slot.scalar].min, dotnetIntegral[slot.scalar].max}
		if slot.form == "epochMilliseconds" {
			r = dotnetEpochMillisecondsRange
		}
		// A whole number wider than an int64 is out of range for every C#
		// integer type, and is refused before the type's own range is
		// consulted rather than being folded to one that fits.
		if !n.Fits || n.Int < r[0] || n.Int > r[1] {
			return "", fmt.Errorf("%s is out of range for a%s member", n.Text, integerArticle(shapeType))
		}
		digits := strconv.FormatInt(n.Int, 10)
		if slot.form == "epochMilliseconds" {
			// The SDK's DateTime, built from the model's number: the property
			// is DateTime?, and the literal the scenario wrote is the epoch
			// milliseconds every other backend sends as it stands.
			return "System.DateTimeOffset.FromUnixTimeMilliseconds(" + digits + "L).UtcDateTime", nil
		}
		if slot.scalar == "long" {
			return digits + "L", nil
		}
		return digits, nil
	case "float":
		// javac and Roslyn agree here: a literal a float cannot carry is a
		// compile error rather than an infinity, and a double a float64 cannot
		// carry would be one.
		if !n.Representable || (slot.scalar == "float" && math.Abs(n.Float) > math.MaxFloat32) {
			return "", fmt.Errorf("%s is out of range for a %s member", n.Text, shapeType)
		}
		rendered := strconv.FormatFloat(n.Float, 'g', -1, 64)
		if slot.scalar == "float" {
			return rendered + "f", nil
		}
		return rendered, nil
	}
	return "", fmt.Errorf("internal: no number spelling for a %s slot", slot.form)
}

// integerArticle names an integral shape type in a message, with its article:
// "an integer", "a long".
func integerArticle(shapeType string) string {
	if strings.HasPrefix(shapeType, "i") {
		return "n " + shapeType
	}
	return " " + shapeType
}

func (sp *dotnetSpeller) list(target string, elem dotnetType, v any, member, indent string) (string, error) {
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
		out, err := sp.value(element, elem, item, member, indent+"    ")
		if err != nil {
			return "", err
		}
		rendered = append(rendered, out)
	}
	// A collection expression takes its element type from the property being
	// assigned, so neither List<T> nor T is named here.
	return csList(rendered, indent), nil
}

func (sp *dotnetSpeller) mapping(target string, t dotnetType, v any, member, indent string) (string, error) {
	entries, ok := v.(map[string]any)
	if !ok {
		return "", fmt.Errorf("a map member wants a JSON object, got %s", valueKind(v))
	}
	key := sp.model.KeyTarget(target)
	if kind := sp.model.Kind(key); kind != "string" && kind != "enum" {
		return "", fmt.Errorf("a map member keyed by %s has no IR spelling; the IR's objects have string keys", kind)
	}
	if !t.Key.scalar("string") && t.Key.Form != "enum" {
		return "", fmt.Errorf("%s keys the map by %s, which a string key does not convert to", sp.sdk.describe(), t.Key.Raw)
	}
	value := sp.model.ValueTarget(target)
	if value == "" {
		return "", fmt.Errorf("the model gives map shape %s no value shape", target)
	}
	rendered := make([]string, 0, len(entries))
	for _, k := range sortedKeys(entries) {
		out, err := sp.value(value, *t.Value, entries[k], member, indent+"    ")
		if err != nil {
			return "", err
		}
		rendered = append(rendered, "["+csString(k)+"] = "+out)
	}
	return csInitializer(rendered, indent), nil
}

func (sp *dotnetSpeller) structure(target, className string, v any, member, indent string) (string, error) {
	members, ok := v.(map[string]any)
	if !ok {
		return "", fmt.Errorf("a structure member wants a JSON object, got %s", valueKind(v))
	}
	class, _ := sp.sdk.class(className)
	rendered := make([]string, 0, len(members))
	for _, k := range sortedKeys(members) {
		field, ok := sp.model.MemberTarget(target, k)
		if !ok {
			return "", fmt.Errorf("%s has no member %q", target, k)
		}
		property, err := sp.property(class, className, k)
		if err != nil {
			return "", err
		}
		out, err := sp.value(field, property, members[k], member, indent+"    ")
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
// bag. Binder.Bind converts the result to the C# scalar the SDK's property
// holds; the conversion to a ConstantClass enum, and the widening into a
// nullable property, are the compiler's, not this emitter's. An epoch-
// milliseconds slot goes through Binder.EpochMilliseconds instead, which makes
// the DateTime the property holds out of the number the context carries.
func (sp *dotnetSpeller) expr(target string, slot dotnetSlot, v any, member string) (string, error) {
	switch slot.form {
	case "string", "bool", "integer", "float", "epochMilliseconds":
	default:
		return "", fmt.Errorf("a value expression can only be bound to a scalar member, and this one is a %s", sp.model.Kind(target))
	}
	if slot.form == "integer" && !dotnetIntegral[slot.scalar].bindable {
		return "", fmt.Errorf("a value expression cannot be bound to a %s property; Binder.Bind converts to byte, short, int and long", slot.scalar)
	}
	// A `$now` is epoch milliseconds: a long, or the DateTime AWSSDK makes of
	// one where the unit is measured — EpochMilliseconds builds it from the
	// same number every other backend sends, so the client's clock reaches
	// the wire unchanged. A property AWSSDK narrowed to anything else would
	// overflow at run time, and is refused here instead.
	if key, _, _ := exprOf(v); key == "$now" && slot.form != "epochMilliseconds" && slot.scalar != "long" {
		return "", fmt.Errorf("a $now is epoch milliseconds, which needs a long or a measured DateTime, and %s types this property %s", sp.sdk.describe(), slot.scalar)
	}
	rendered, err := dotnetValue(v)
	if err != nil {
		return "", err
	}
	if slot.form == "epochMilliseconds" {
		return fmt.Sprintf("b.EpochMilliseconds(%s, %s)", csString(member), rendered), nil
	}
	return fmt.Sprintf("b.Bind<%s>(%s, %s)", slot.scalar, csString(member), rendered), nil
}

// ---------------------------------------------------------------------------
// Document forms
// ---------------------------------------------------------------------------

// dotnetEpochMillisecondsProperties collects, for every call a group makes,
// the request and response properties AWSSDK types as a DateTime where the
// model says epoch-milliseconds long, keyed by class name.
//
// The response is read as a document (Scenario/Documents.cs), and a DateTime
// renders there as ISO text — right for a modeled timestamp, which no backend
// compares, and wrong for one of these: every other backend holds the number
// the service sent, so an `equals`, a `where` or an export `$ref`'d into a
// later request would disagree. So the emitted file registers each such
// property with Documents.EpochMilliseconds, which renders it as that number.
// Request classes are walked too, because failure-message field 3 renders the
// request that was sent through the same conversion.
//
// A disagreement it cannot render — the same DateTime-for-long in a service
// whose wire unit is not measured, or one inside a list or a map value, which
// a (class, property) registration cannot name — is an error, and refuses the
// group rather than leaving one backend reading a different document.
func (sp *dotnetSpeller) dotnetEpochMillisecondsProperties(c call, into map[string]map[string]bool) error {
	seen := map[string]bool{}
	if input := sp.model.InputShape(c.Op); input != "" {
		if err := sp.epochMillisecondsIn(input, c.Op+"Request", into, seen); err != nil {
			return err
		}
	}
	if output := sp.model.OutputShape(c.Op); output != "" {
		if err := sp.epochMillisecondsIn(output, c.Op+"Response", into, seen); err != nil {
			return err
		}
	}
	return nil
}

func (sp *dotnetSpeller) epochMillisecondsIn(structure, className string, into map[string]map[string]bool, seen map[string]bool) error {
	if seen[className] {
		return nil
	}
	seen[className] = true
	class, ok := sp.sdk.class(className)
	if !ok {
		// A response class the pinned package does not declare leaves nothing
		// to render; a request class it does not declare is refused by
		// requestClass before this is asked.
		return nil
	}
	for _, member := range sp.model.Members(structure) {
		target, ok := sp.model.MemberTarget(structure, member)
		if !ok {
			continue
		}
		name := dotnetNameProperty(member)
		t, ok := class[name]
		if !ok {
			continue
		}
		if err := sp.epochMillisecondsAt(target, t, className, name, into, seen); err != nil {
			return err
		}
	}
	return nil
}

func (sp *dotnetSpeller) epochMillisecondsAt(target string, t dotnetType, className, property string, into map[string]map[string]bool, seen map[string]bool) error {
	switch kind := sp.model.Kind(target); {
	case kind == "integer" && t.scalar("DateTime"):
		if _, err := sp.slot(target, t); err != nil {
			return &dotnetDocumentError{class: className, property: property, err: err}
		}
		if into[className] == nil {
			into[className] = map[string]bool{}
		}
		into[className][property] = true
	case kind == "structure" && t.Form == "class":
		return sp.epochMillisecondsIn(target, t.Name, into, seen)
	case (kind == "list" && t.Form == "list") || (kind == "map" && t.Form == "map"):
		inner, innerType := sp.model.ElementTarget(target), t.Elem
		if kind == "map" {
			inner, innerType = sp.model.ValueTarget(target), t.Value
		}
		if sp.model.Kind(inner) == "integer" && innerType.scalar("DateTime") {
			return &dotnetDocumentError{class: className, property: property,
				err: fmt.Errorf("it holds a %s of DateTimes where the model says long, and a conversion registered by property cannot reach inside one", kind)}
		}
		return sp.epochMillisecondsAt(inner, *innerType, className, property, into, seen)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Untyped values
// ---------------------------------------------------------------------------

// dotnetValue renders one IR value as an *untyped* C# expression: an object is
// a Dictionary<string, object?>, a list an object?[], a scalar itself, and
// each of the seven expression forms a Val constructor. Nothing else is
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
		case "$base64":
			inner, err := dotnetValue(arg)
			if err != nil {
				return "", err
			}
			return fmt.Sprintf("Val.Base64(%s)", inner), nil
		case "$now":
			unit, offset, err := nowParts(arg)
			if err != nil {
				return "", err
			}
			return fmt.Sprintf("Val.Now(%s, %dL)", csString(unit), offset), nil
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
// Three things are asked, each by attempting what emission would write, so one
// code path answers "can this be emitted" and "how" and the two cannot drift:
// whether the pinned package declares the operation's request class at all,
// whether each input member can be spelled into the property AWSSDK declares
// for it, and whether every request and response the group touches has a
// document form that agrees with the other backends
// (dotnetEpochMillisecondsProperties).
func dotnetRefusals(gen *generation, sp *dotnetSpeller, g group) []gap {
	out := refusals(gen, g, dotnetEmitReason, refusalChecks{
		call: func(op string) (string, error) {
			if _, err := sp.requestClass(op); err != nil {
				return op + "Request", err
			}
			return "", nil
		},
		member: func(op, member string, v any) error {
			target, err := sp.target(gen.model.InputShape(op), op, member)
			if err != nil {
				return err
			}
			class, err := sp.requestClass(op)
			if err != nil {
				return err
			}
			property, err := sp.property(class, op+"Request", member)
			if err != nil {
				return err
			}
			if _, err := sp.value(target, property, v, member, ""); err != nil {
				return fmt.Errorf("%s.%s cannot be spelled as C#: %v", op, member, err)
			}
			return nil
		},
	})
	// An operation already refused for its input is not reported twice: the
	// first reason is the one to fix.
	seen := map[string]bool{}
	for _, refused := range out {
		seen[refused.Operation] = true
	}
	for _, c := range callsOf(g) {
		if seen[c.Op] {
			continue
		}
		seen[c.Op] = true
		var failed *dotnetDocumentError
		if err := sp.dotnetEpochMillisecondsProperties(c, map[string]map[string]bool{}); errors.As(err, &failed) {
			out = append(out, gap{
				Service:   gen.scenario.Service,
				Operation: c.Op,
				Group:     g.Name,
				Reason:    dotnetEmitReason + ":" + failed.property,
				Detail:    fmt.Sprintf("%s has no document form every backend agrees on: %v", c.Op, err),
			})
		}
	}
	sortGaps(out)
	return out
}

// dotnetDocumentError is a request or response property whose document form
// would disagree with the other backends'. It carries the property's name, which
// is what the refusal is recorded under.
type dotnetDocumentError struct {
	class, property string
	err             error
}

func (e *dotnetDocumentError) Error() string {
	return fmt.Sprintf("%s.%s: %v", e.class, e.property, e.err)
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
