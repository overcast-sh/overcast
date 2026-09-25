//go:build dev

package main

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// The rust-sdk source emitter — docs/plans/compat-coverage-modelgen.md §3.2 D1,
// phase G3.
//
// It is the second source-emitting backend, after emit_go.go, and it is here
// for the same reason: the AWS SDK for Rust takes typed request builders, not a
// map of parameters, so a scenario cannot be interpreted at run time without
// reaching under the SDK — which the plan rejects, because the whole value of
// running eight suites is that each exercises its own real typed serialization
// path. So this file emits Rust — one function per scenario test, each building
// a real fluent builder chain and calling a real client method — which the
// rust-sdk suite's ordinary `cargo build` compiles.
//
// The split is emit_go.go's: what is emitted is the *data* plus the typed
// calls. Semantics — the context bag, value expressions, the closed check set,
// error matching, `eventually`, the six-field failure message — live once in
// the suite's hand-written src/scenario module and are never re-emitted.
//
// # The naming table
//
// Everything this emitter knows about spelling Rust is in the rustName*
// functions below, and `-explain -lang rust` (explain_typed.go) renders through
// rustCallLines, so the pseudo-code a reader reproduces a failure with is the
// source the emitter wrote:
//
//	service   → aws_sdk_<sdkId lower-cased with its spaces removed>
//	operation → client.<snake(op)>()
//	member    → .<snake(member)>(…), raw-escaped where snake_case lands on a
//	            Rust keyword (Organizations' `Type` is `r#type`)
//	value     → the member's own modeled kind, spelled by rustMemberLines
//
// # Why this emitter reads the model and emit_go.go reads the SDK
//
// emit_go.go loads `aws-sdk-go-v2/service/<pkg>` at generation time because
// smithy-go's choice between a pointer field and a value field is not derivable
// from the model, and getting it wrong is a compile error or — worse — a member
// the SDK silently drops. Rust has neither problem, and it is the *builder*
// that removes them: a fluent setter takes the value itself
// (`.queue_name(impl Into<String>)`, `.max_number_of_messages(i32)`), never an
// Option, so a member's optionality never reaches the call site and a value set
// to its type's zero is sent exactly as any other value is. Everything else the
// spelling needs — string, enum, integer width, list, map, structure — is the
// modeled kind, which the pinned snapshot carries.
//
// What that leaves is the one thing the model cannot answer: whether the
// vendored crate has the operation at all. A crate older than the pinned model
// is a *compile failure of the suite* here rather than a generation-time
// refusal, because cmd/compatgen has no Rust toolchain to ask. It is loud —
// the Dockerfile builds before it runs — and the fix is the same either way:
// move the pin in compat/suites/rust-sdk/Cargo.toml forward. See
// compat/suites/rust-sdk/AGENTS.md § Generated groups.

// rustSuiteDir is where the emitted files live, repository-relative.
const rustSuiteDir = "compat/suites/rust-sdk/src/groups"

// rustEmitReason is the refusal a member the emitter cannot express produces.
// Like go-emit-unsupported it does not mean "no test": the operation is
// generated and the interpreters run it. It means this backend cannot compile
// it, so the whole group is scoped away from rust-sdk in the generated
// registry — a suite that cannot execute a group must not be listed as able to.
//
// Two things produce it:
//
//	the member's modeled kind has no IR literal    a timestamp, blob, document, union
//	an expression is bound to a composite member   it has no scalar slot to land in
const rustEmitReason = "rust-emit-unsupported"

// rustUnsupportedKinds are the modeled member kinds no value in the IR's
// grammar can carry. Timestamps, documents and unions have no portable value
// and are refused upstream — an error in a recipe or an authored scenario, and
// `no-portable-value` for a binding (binder.go) — so this is a backstop rather
// than a live path. A blob is not here: `$base64` spells it (rustBlob).
var rustUnsupportedKinds = map[string]bool{
	"timestamp": true,
	"document":  true,
	"union":     true,
}

// rustBindings records whether spelling one call reached the Binder at all.
// It is threaded down the value-rendering chain so that the single branch that
// writes a Binder call is also what decides the closure's parameter name — see
// rustBinderParam.
type rustBindings struct{ used bool }

// emitRust renders one service's generated groups as Rust for the rust-sdk
// suite.
func emitRust(gen *generation) (*sourceEmission, error) {
	s := gen.scenario
	e := &sourceEmission{
		Path:    rustSuiteDir + "/" + rustFileName(gen.unit),
		Refused: map[string]bool{},
	}

	groups := make([]group, 0, len(s.Groups))
	for _, g := range s.Groups {
		if refusals := rustRefusals(gen, g); len(refusals) > 0 {
			e.Refused[g.Name] = true
			e.Gaps = append(e.Gaps, refusals...)
			continue
		}
		groups = append(groups, g)
	}

	if err := rustItemNamesAreUnique(gen.unit, groups); err != nil {
		return nil, err
	}

	crate := rustNameCrate(s.Client.SDKID)
	structName := rustNameStruct(gen.unit)
	w := &rustWriter{}
	w.linef("// Code generated by cmd/compatgen; DO NOT EDIT.")
	w.linef("//")
	w.linef("// Generated from %s by cmd/compatgen.", gen.file)
	w.linef("// The semantics live in crate::scenario; this file is the data and the typed")
	w.linef("// SDK calls.")
	w.linef("")
	w.linef("use std::collections::HashMap;")
	w.linef("use std::sync::Arc;")
	w.linef("")
	w.linef("use crate::clients::AwsClients;")
	w.linef("use crate::groups::ServiceGroup;")
	w.linef("use crate::harness::{TestContext, TestFn};")
	w.linef("use crate::scenario::{self, Call, Group, Test};")
	w.linef("")
	w.linef("/// The scenario file every group in this file was generated from.")
	w.linef("const SCENARIO_FILE: &str = %s;", rustString(gen.file))
	for _, g := range groups {
		w.linef("")
		w.linef("const %s: Group = Group {", rustNameGroupConst(g.Name))
		w.linef("    name: %s,", rustString(g.Name))
		w.linef("    file: SCENARIO_FILE,")
		w.linef("};")
	}
	w.linef("")
	w.linef("/// The %s groups.", gen.describe())
	w.linef("///")
	w.linef("/// The client is built once, from the config the suite's hand-written groups")
	w.linef("/// share, and cloned into every test — a generated group builds its own rather")
	w.linef("/// than adding an accessor to AwsClients for every service the generator learns")
	w.linef("/// to cover; nothing else about the client differs.")
	w.linef("pub struct %s {", structName)
	w.linef("    client: %s::Client,", crate)
	w.linef("}")
	w.linef("")
	w.linef("impl %s {", structName)
	w.linef("    pub fn new(clients: &Arc<AwsClients>) -> Self {")
	w.linef("        let config = %s::config::Builder::from(clients.config())", crate)
	w.linef("            .endpoint_url(clients.endpoint())")
	w.linef("            .build();")
	w.linef("        Self {")
	w.linef("            client: %s::Client::from_conf(config),", crate)
	w.linef("        }")
	w.linef("    }")
	w.linef("}")
	w.linef("")
	w.linef("impl ServiceGroup for %s {", structName)
	w.linef("    fn name(&self) -> &'static str {")
	w.linef("        %s", rustString("scenarios/"+gen.unit))
	w.linef("    }")
	w.linef("")
	rustWriteRegistrations(w, groups)
	w.linef("}")

	for _, g := range groups {
		if err := rustWriteGroup(w, gen, crate, g); err != nil {
			return nil, err
		}
	}

	e.Contents = []byte(w.String())
	sortGaps(e.Gaps)
	return e, nil
}

// rustWriteRegistrations emits the three ServiceGroup maps. Every group
// registers both hooks even when its two lists are empty, because an empty
// phase is a no-op and not a missing one — which is what makes "a probe creates
// nothing" visible in the emitted source rather than a convention to remember.
func rustWriteRegistrations(w *rustWriter, groups []group) {
	w.linef("    fn impls(&self) -> HashMap<String, TestFn> {")
	w.linef("        let mut impls: HashMap<String, TestFn> = HashMap::new();")
	for _, g := range groups {
		for _, t := range g.Tests {
			w.linef("        {")
			w.linef("            let client = self.client.clone();")
			w.linef("            impls.insert(")
			w.linef("                %s.to_string(),", rustString(g.Name+":"+t.Name))
			w.linef("                Arc::new(move |ctx: TestContext| {")
			w.linef("                    let client = client.clone();")
			w.linef("                    Box::pin(async move {")
			w.linef("                        %s", rustNameGroupConst(g.Name))
			w.linef("                            .run_test(&ctx, %s, %s(&client))", rustString(t.Name), rustNameTestFn(g.Name, t.Name))
			w.linef("                            .await")
			w.linef("                    })")
			w.linef("                }),")
			w.linef("            );")
			w.linef("        }")
		}
	}
	w.linef("        impls")
	w.linef("    }")
	w.linef("")
	rustWriteHookMap(w, groups, "setups", "run_setup", rustNameSetupFn)
	w.linef("")
	rustWriteHookMap(w, groups, "teardowns", "run_teardown", rustNameTeardownFn)
}

func rustWriteHookMap(w *rustWriter, groups []group, method, run string, name func(string) string) {
	w.linef("    fn %s(&self) -> HashMap<String, TestFn> {", method)
	w.linef("        let mut %s: HashMap<String, TestFn> = HashMap::new();", method)
	for _, g := range groups {
		w.linef("        {")
		w.linef("            let client = self.client.clone();")
		w.linef("            %s.insert(", method)
		w.linef("                %s.to_string(),", rustString(g.Name))
		w.linef("                Arc::new(move |ctx: TestContext| {")
		w.linef("                    let client = client.clone();")
		w.linef("                    Box::pin(async move {")
		w.linef("                        %s.%s(&ctx, %s(&client)).await", rustNameGroupConst(g.Name), run, name(g.Name))
		w.linef("                    })")
		w.linef("                }),")
		w.linef("            );")
		w.linef("        }")
	}
	w.linef("        %s", method)
	w.linef("    }")
}

// rustWriteGroup emits one group: its setup and teardown call lists and one
// function per test.
func rustWriteGroup(w *rustWriter, gen *generation, crate string, g group) error {
	if err := rustWriteCallList(w, gen, crate, rustNameSetupFn(g.Name), g.Setup); err != nil {
		return err
	}
	if err := rustWriteCallList(w, gen, crate, rustNameTeardownFn(g.Name), g.Teardown); err != nil {
		return err
	}
	for _, t := range g.Tests {
		w.linef("")
		w.linef("fn %s(client: &%s::Client) -> Test {", rustNameTestFn(g.Name, t.Name), crate)
		w.linef("    Test {")
		if err := rustWriteCall(w, gen, crate, t.Call, "        ", "call: "); err != nil {
			return err
		}
		w.linef("        assert: vec![")
		for _, a := range t.Assert {
			if err := rustWriteClause(w, gen, crate, a, "            "); err != nil {
				return err
			}
		}
		w.linef("        ],")
		w.linef("    }")
		w.linef("}")
	}
	return nil
}

func rustWriteCallList(w *rustWriter, gen *generation, crate, name string, calls []call) error {
	w.linef("")
	if len(calls) == 0 {
		w.linef("fn %s(_client: &%s::Client) -> Vec<Call> {", name, crate)
		w.linef("    // An empty phase is a no-op, not a missing one.")
		w.linef("    Vec::new()")
		w.linef("}")
		return nil
	}
	w.linef("fn %s(client: &%s::Client) -> Vec<Call> {", name, crate)
	w.linef("    vec![")
	for _, c := range calls {
		if err := rustWriteCall(w, gen, crate, c, "        ", ""); err != nil {
			return err
		}
	}
	w.linef("    ]")
	w.linef("}")
	return nil
}

// rustWriteCall emits one scenario::Call: the operation, the params as the
// scenario file writes them, the exports, and the typed builder chain.
//
// The params tree is emitted once and serves two purposes: the runtime
// evaluates it to produce failure-message field 3, and the typed call reads its
// deferred leaves back by path through the Binder. That is why an expression
// appears in the emitted source only once, as data — `b.string("QueueUrl")`
// names the member it is filling rather than repeating the expression.
func rustWriteCall(w *rustWriter, gen *generation, crate string, c call, indent, prefix string) error {
	w.linef("%s%sCall {", indent, prefix)
	w.linef("%s    op: %s,", indent, rustString(c.Op))
	params, err := rustValue(c.Params, indent+"    ")
	if err != nil {
		return fmt.Errorf("%s params: %w", c.Op, err)
	}
	w.linef("%s    params: %s,", indent, params)
	if len(c.Export) == 0 {
		w.linef("%s    export: Vec::new(),", indent)
	} else {
		w.linef("%s    export: vec![", indent)
		for _, path := range sortedStringKeys(c.Export) {
			w.linef("%s        (%s, %s),", indent, rustString(path), rustString(c.Export[path]))
		}
		w.linef("%s    ],", indent)
	}
	lines, bound, err := rustCallLines(gen.model, crate, c.Op, c.Params)
	if err != nil {
		return err
	}
	w.linef("%s    invoke: {", indent)
	w.linef("%s        let client = client.clone();", indent)
	w.linef("%s        scenario::invoker(move |%s| {", indent, rustBinderParam(bound))
	w.linef("%s            let client = client.clone();", indent)
	w.linef("%s            Box::pin(async move {", indent)
	w.linef("%s                let capture = scenario::Capture::new();", indent)
	for _, line := range lines[:len(lines)-1] {
		w.linef("%s                %s", indent, line)
	}
	w.linef("%s                Ok(scenario::observe(%s, &capture))", indent, lines[len(lines)-1])
	w.linef("%s            })", indent)
	w.linef("%s        })", indent)
	w.linef("%s    },", indent)
	w.linef("%s},", indent)
	return nil
}

// rustBinderParam names the Binder parameter of an emitted invoke closure. A
// call whose every member is a literal never asks the Binder for anything, and
// an unused binding is a warning the suite's build would carry for every such
// call — which is most of a probe group.
//
// The answer comes from the spelling itself (rustBindings, threaded down to the
// one branch that writes a Binder call) rather than from reading the rendered
// lines back: a string literal that happens to contain "b." would otherwise
// name the parameter `b` and leave it unused, which is the warning this exists
// to avoid.
func rustBinderParam(bound bool) string {
	if bound {
		return "b"
	}
	return "_b"
}

// rustWriteClause emits one assertion clause through the constructors in
// crate::scenario, which are the same closed set ir.go builds.
func rustWriteClause(w *rustWriter, gen *generation, crate string, a assertion, indent string) error {
	switch a.Kind {
	case assertResponseField:
		w.linef("%sscenario::response_field(vec![", indent)
		if err := rustWriteChecks(w, a.Checks, indent+"    "); err != nil {
			return err
		}
		w.linef("%s]),", indent)
	case assertReadback:
		w.linef("%sscenario::readback(", indent)
		if err := rustWriteCall(w, gen, crate, *a.Call, indent+"    ", ""); err != nil {
			return err
		}
		w.linef("%s    vec![", indent)
		if err := rustWriteChecks(w, a.Checks, indent+"        "); err != nil {
			return err
		}
		w.linef("%s    ],", indent)
		w.linef("%s),", indent)
	case assertListContains, assertAbsent:
		if a.Kind == assertAbsent && a.Error != nil {
			w.linef("%sscenario::absent_by_error(", indent)
			if err := rustWriteCall(w, gen, crate, *a.Call, indent+"    ", ""); err != nil {
				return err
			}
			w.linef("%s    scenario::error(%s, %s),", indent, rustString(a.Error.Shape), rustString(a.Error.Code))
			w.linef("%s),", indent)
			return nil
		}
		name := "list_contains"
		if a.Kind == assertAbsent {
			name = "absent_from_list"
		}
		w.linef("%sscenario::%s(", indent, name)
		if a.Call == nil {
			w.linef("%s    None,", indent)
		} else {
			if err := rustWriteCall(w, gen, crate, *a.Call, indent+"    ", "Some("); err != nil {
				return err
			}
			// rustWriteCall closed the struct with "}," — reopen it as the
			// Some(...) argument it is.
			w.closeSome(indent + "    ")
		}
		w.linef("%s    %s,", indent, rustString(a.ItemsPath))
		w.linef("%s    vec![", indent)
		for _, path := range sortedValueKeys(a.Where) {
			value, err := rustValue(a.Where[path], indent+"        ")
			if err != nil {
				return err
			}
			w.linef("%s        scenario::where_entry(%s, %s),", indent, rustString(path), value)
		}
		w.linef("%s    ],", indent)
		w.linef("%s),", indent)
	case assertErrorCode:
		w.linef("%sscenario::error_code(scenario::error(%s, %s)),", indent, rustString(a.Error.Shape), rustString(a.Error.Code))
	case assertEventually:
		w.linef("%sscenario::eventually(%d, %d,", indent, a.MaxAttempts, a.DelayMs)
		if err := rustWriteClause(w, gen, crate, *a.Assert, indent+"    "); err != nil {
			return err
		}
		w.linef("%s),", indent)
	default:
		return fmt.Errorf("cannot emit assertion kind %q", a.Kind)
	}
	return nil
}

// rustWriteChecks emits a clause's checks in path order, so a failure message
// is the same on every run and in every backend.
func rustWriteChecks(w *rustWriter, checks map[string]check, indent string) error {
	for _, path := range sortedCheckPaths(checks) {
		rendered, err := rustCheck(path, checks[path], indent)
		if err != nil {
			return err
		}
		w.linef("%s%s,", indent, rendered)
	}
	return nil
}

func rustCheck(path string, c check, indent string) (string, error) {
	switch {
	case c.NonEmpty:
		return fmt.Sprintf("scenario::non_empty(%s)", rustString(path)), nil
	case c.IsList:
		return fmt.Sprintf("scenario::is_list(%s)", rustString(path)), nil
	case c.Missing:
		return fmt.Sprintf("scenario::missing(%s)", rustString(path)), nil
	case c.Matches != "":
		return fmt.Sprintf("scenario::matches(%s, %s)", rustString(path), rustString(c.Matches)), nil
	case c.EqualsJSON != nil:
		text, err := equalsJSONText(c.EqualsJSON)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("scenario::equals_json(%s, %s)", rustString(path), rustString(text)), nil
	default:
		value, err := rustValue(c.Equals, indent)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("scenario::equals(%s, %s)", rustString(path), value), nil
	}
}

// ---------------------------------------------------------------------------
// The typed call
// ---------------------------------------------------------------------------

// rustCallLines renders the statements that build and send one call. It is the
// emitter's invoke body and, line for line, what `-explain -lang rust` prints,
// which is what keeps the two from drifting.
//
// The last line is the call itself, which is what the assignment prefix goes in
// front of in `-explain` and what the emitter wraps in scenario::observe.
func rustCallLines(model *serviceModel, crate, op string, params map[string]any) ([]string, bool, error) {
	input := model.InputShape(op)
	bind := &rustBindings{}
	setters, err := rustMemberLines(model, crate, input, params, "", bind)
	if err != nil {
		return nil, false, fmt.Errorf("%s: %w", op, err)
	}
	lines := []string{"let request = client"}
	lines = append(lines, "    ."+rustNameOperation(op)+"()")
	lines = append(lines, setters...)
	lines = append(lines, "    .customize()")
	lines = append(lines, "    .interceptor(capture.clone());")
	return append(lines, "request.send().await"), bind.used, nil
}

// rustCallIndent is the indent of an operation builder's own setter lines. It
// is the top of the ladder rustSetterCalls and rustScalarOrComposite walk down
// together: the members of a structure literal are indented two steps further
// in than the setter that takes it, whatever depth that setter is itself at.
const rustCallIndent = "    "

// rustMemberLines renders one params object as fluent-builder setter lines, in
// member order. A list or map member is several setter calls, because that is
// what the SDK's builder takes: `.entries(entry)` appends one element and
// `.attributes(key, value)` inserts one entry.
//
// prefix is the member's path inside the params document, which is the name the
// Binder is asked for and the name a binding failure reports.
func rustMemberLines(model *serviceModel, crate, structure string, params map[string]any, prefix string, bind *rustBindings) ([]string, error) {
	var lines []string
	for _, member := range sortedValueKeys(params) {
		target, ok := model.MemberTarget(structure, member)
		if !ok {
			return nil, fmt.Errorf("%s has no member %s in the model", structure, member)
		}
		path := member
		if prefix != "" {
			path = prefix + "." + member
		}
		calls, err := rustSetterCalls(model, crate, rustNameMember(member), target, params[member], path, rustCallIndent, bind)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", member, err)
		}
		lines = append(lines, calls...)
	}
	return lines, nil
}

// rustSetterCalls renders every setter call one member needs, at indent.
//
// indent is the column the setter lines sit in, and it is what makes this one
// function serve both an operation's own builder and a structure literal nested
// inside one: a builder chain is a builder chain at every depth, and only the
// indent differs.
func rustSetterCalls(model *serviceModel, crate, setter, target string, value any, path, indent string, bind *rustBindings) ([]string, error) {
	kind := model.Kind(target)
	// A deferred expression fills one slot, whatever the member's kind, so it
	// goes straight to rustValueOfKind — which is where the refusal for a
	// composite member lives. Reaching the list and map branches below with an
	// expression would report "the scenario gives it a map", which describes
	// the JSON rather than the reason.
	if _, _, isExpr := exprOf(value); isExpr {
		rendered, err := rustValueOfKind(model, crate, target, value, path, bind)
		if err != nil {
			return nil, err
		}
		return rustSetterLine(indent, setter, rendered), nil
	}
	switch kind {
	case "list":
		items, ok := value.([]any)
		if !ok {
			return nil, fmt.Errorf("%s is a list member but the scenario gives it %T", path, value)
		}
		element := model.Shapes[target].Member
		var lines []string
		for i, item := range items {
			rendered, err := rustScalarOrComposite(model, crate, element, item, fmt.Sprintf("%s[%d]", path, i), indent, bind)
			if err != nil {
				return nil, err
			}
			lines = append(lines, rustSetterLine(indent, setter, rendered)...)
		}
		return lines, nil
	case "map":
		entries, ok := value.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("%s is a map member but the scenario gives it %T", path, value)
		}
		shape := model.Shapes[target]
		var lines []string
		for _, key := range sortedValueKeys(entries) {
			renderedKey, err := rustLiteralOfKind(model, crate, shape.Key, key)
			if err != nil {
				return nil, err
			}
			renderedValue, err := rustScalarOrComposite(model, crate, shape.Value, entries[key], path+"."+key, indent, bind)
			if err != nil {
				return nil, err
			}
			lines = append(lines, rustSetterLine(indent, setter, renderedKey+", "+renderedValue)...)
		}
		return lines, nil
	default:
		rendered, err := rustScalarOrComposite(model, crate, target, value, path, indent, bind)
		if err != nil {
			return nil, err
		}
		return rustSetterLine(indent, setter, rendered), nil
	}
}

// rustSetterLine wraps one setter call, on one line while that is readable and
// over several when the argument is itself multi-line.
func rustSetterLine(indent, setter, argument string) []string {
	if !strings.Contains(argument, "\n") {
		return []string{fmt.Sprintf("%s.%s(%s)", indent, setter, argument)}
	}
	lines := []string{fmt.Sprintf("%s.%s(", indent, setter)}
	lines = append(lines, strings.Split(argument, "\n")...)
	return append(lines, indent+")")
}

// rustScalarOrComposite renders one value of one modeled shape: a scalar, or a
// structure built through the SDK's own builder.
//
// A structure's members go back through rustSetterCalls, which is what makes
// the nesting total: a member that is itself a structure is another builder
// chain, a list member is the same repeated setter smithy-rs appends through at
// the top level, and a map member the same two-argument insert. The only thing
// that changes with depth is the indent, and every other rule — the pascal-cased
// type name, the raw identifier, whether build() is fallible — is asked of the
// nested shape rather than inherited from the shape around it.
//
// The recursion terminates on the scenario's value tree rather than on the
// model, so a shape that refers to itself is only as deep as the params
// document that fills it in.
func rustScalarOrComposite(model *serviceModel, crate, target string, value any, path, indent string, bind *rustBindings) (string, error) {
	if model.Kind(target) != "structure" {
		return rustValueOfKind(model, crate, target, value, path, bind)
	}
	members, ok := value.(map[string]any)
	if !ok {
		return "", fmt.Errorf("%s is a structure member but the scenario gives it %T", path, value)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s    %s::types::%s::builder()", indent, crate, rustNameType(target))
	inner := indent + "        "
	for _, member := range sortedValueKeys(members) {
		memberTarget, ok := model.MemberTarget(target, member)
		if !ok {
			return "", fmt.Errorf("%s has no member %s in the model", target, member)
		}
		calls, err := rustSetterCalls(model, crate, rustNameMember(member), memberTarget, members[member], path+"."+member, inner, bind)
		if err != nil {
			return "", err
		}
		for _, line := range calls {
			b.WriteString("\n" + line)
		}
	}
	fmt.Fprintf(&b, "\n%s        .build()", indent)
	if rustBuilderIsFallible(model, target) {
		fmt.Fprintf(&b, "\n%s        .map_err(|err| scenario::build_error(%s, err))?", indent, rustString(path))
	}
	return b.String(), nil
}

// rustBuilderIsFallible reports whether smithy-rs gives a structure's `build()`
// a Result, which is exactly where some member is required and carries neither
// `@default` nor `@clientOptional`.
//
// The `@default` half is what a reading of "required" alone gets wrong. A member
// that is both required and defaulted has a value whatever the caller says, so
// the generated builder fills it in with `unwrap_or_default` and `build()` stays
// infallible — Elastic Load Balancing's `Listener.LoadBalancerPort` and
// `AccessLog.Enabled` are two of the six such members in the pinned snapshot.
// Writing `?` after one of those `build()` calls does not compile.
//
// `@clientOptional` is the other half, and it is the one place in this program
// where that trait really does mean what it says: it tells a client generator
// to treat the member as optional whatever `@required` says, so smithy-rs
// leaves the builder infallible. Batch's `ComputeEnvironmentOrder`,
// `ResourceRequirement` and `ShareAttributes` all mark every required member
// `@clientOptional`, and aws-sdk-batch 1.126.0 gives each of them a bare
// `build()` — a `?` after any of the three does not compile.
//
// It is a predicate of its own rather than a change to RequiredMembers, because
// that method's other callers ask a different question — which members a caller
// must *send* — and neither a defaulted nor a client-optional member stops
// being one of those.
func rustBuilderIsFallible(model *serviceModel, target string) bool {
	for _, name := range model.RequiredMembers(target) {
		traits := model.Shapes[target].Members[name].Traits
		if hasTrait(traits, "smithy.api#default") || hasTrait(traits, "smithy.api#clientOptional") {
			continue
		}
		return true
	}
	return false
}

// rustValueOfKind renders one scalar value: a literal where the scenario writes
// one, and a Binder lookup where it writes a deferred expression.
//
// The Binder is handed the member's path inside the params document rather than
// the expression itself. The runtime has already evaluated the whole params
// tree by then — that evaluation is failure-message field 3 — so reading the
// leaf back by path is what keeps the typed call and the reported params the
// same values rather than two evaluations of one expression.
func rustValueOfKind(model *serviceModel, crate, target string, value any, path string, bind *rustBindings) (string, error) {
	kind := model.Kind(target)
	if rustUnsupportedKinds[kind] {
		return "", fmt.Errorf("the rust-sdk emitter has no Rust value expression for a %s member (%s)", kind, path)
	}
	if kind == "blob" {
		return rustBlob(crate, value, path, bind)
	}
	if _, _, isExpr := exprOf(value); !isExpr {
		return rustLiteralOfKind(model, crate, target, value)
	}
	bind.used = true
	switch kind {
	case "string":
		return fmt.Sprintf("b.string(%s)?", rustString(path)), nil
	case "enum":
		return fmt.Sprintf("%s::types::%s::from(b.string(%s)?.as_str())", crate, rustNameType(target), rustString(path)), nil
	case "integer":
		width := rustIntWidth(model, target)
		if key, _, _ := exprOf(value); key == "$now" && width != "i64" {
			return "", fmt.Errorf("a $now is epoch milliseconds, which needs an i64, and %s is an %s", path, width)
		}
		return fmt.Sprintf("b.%s(%s)?", width, rustString(path)), nil
	case "float":
		return fmt.Sprintf("b.%s(%s)?", rustFloatWidth(model, target), rustString(path)), nil
	case "boolean":
		return fmt.Sprintf("b.boolean(%s)?", rustString(path)), nil
	}
	// A $ref that names a whole list, map or structure has no scalar slot to
	// land in: the SDK's setter takes one typed element at a time, and building
	// the composite at run time would mean converting a document into typed
	// values without a type to convert it to — reflection, by another name.
	return "", fmt.Errorf("a value expression can only be bound to a scalar member; %s is a %s", path, kind)
}

// rustBlob renders a `$base64` value into a blob member, whose setter takes the
// crate's re-exported `primitives::Blob`.
//
// A literal is decoded at generation time and written as a Rust byte string —
// printable ASCII as itself, everything else as \xNN — so the bytes are in the
// source for the compiler and a reader alike. A `$base64` around a $ref is
// deferred: the runtime has already evaluated it into the params document as
// base64 text, and Binder::blob reads that leaf back and decodes it.
func rustBlob(crate string, value any, path string, bind *rustBindings) (string, error) {
	key, arg, isExpr := exprOf(value)
	if !isExpr || key != "$base64" {
		return "", fmt.Errorf("%s is a blob member, which takes $base64, but the scenario gives it %s", path, valueKind(value))
	}
	if text, literal := arg.(string); literal {
		raw, err := decodeBase64(text)
		if err != nil {
			return "", fmt.Errorf("%s: $base64 %q %w", path, text, err)
		}
		return fmt.Sprintf("%s::primitives::Blob::new(%s.to_vec())", crate, rustByteString(raw)), nil
	}
	bind.used = true
	return fmt.Sprintf("%s::primitives::Blob::new(b.blob(%s)?)", crate, rustString(path)), nil
}

// rustByteString renders bytes as a Rust byte-string literal.
func rustByteString(raw []byte) string {
	var b strings.Builder
	b.WriteString(`b"`)
	for _, c := range raw {
		switch {
		case c == '"':
			b.WriteString(`\"`)
		case c == '\\':
			b.WriteString(`\\`)
		case c >= 0x20 && c < 0x7f:
			b.WriteByte(c)
		default:
			fmt.Fprintf(&b, `\x%02x`, c)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// rustLiteralOfKind renders a literal the scenario file states outright.
func rustLiteralOfKind(model *serviceModel, crate, target string, value any) (string, error) {
	kind := model.Kind(target)
	if rustUnsupportedKinds[kind] {
		return "", fmt.Errorf("the rust-sdk emitter has no Rust value expression for a %s member", kind)
	}
	switch kind {
	case "string":
		s, ok := value.(string)
		if !ok {
			return "", fmt.Errorf("wanted a string literal, got %T", value)
		}
		return rustString(s), nil
	case "enum":
		s, ok := value.(string)
		if !ok {
			return "", fmt.Errorf("wanted an enum literal, got %T", value)
		}
		// From<&str> rather than the named variant: an enum the pinned model
		// knows and the vendored crate does not still compiles, as the SDK's
		// own Unknown variant.
		return fmt.Sprintf("%s::types::%s::from(%s)", crate, rustNameType(target), rustString(s)), nil
	case "integer":
		n, err := integerOf(value)
		if err != nil {
			return "", err
		}
		return strconv.Itoa(n), nil
	case "float":
		n, ok := numberOf(value)
		if !ok {
			return "", fmt.Errorf("wanted a number, got %s", valueKind(value))
		}
		// A literal a float64 cannot carry would compile to ±Inf rather than to
		// the number the scenario asked for, so it is refused rather than emitted
		// — the same rule emit_go.go and emit_java.go apply.
		if !n.Representable {
			return "", fmt.Errorf("%s is out of range for an %s member", n.Text, rustFloatWidth(model, target))
		}
		return rustFloatLiteral(n.Float), nil
	case "boolean":
		b, ok := value.(bool)
		if !ok {
			return "", fmt.Errorf("wanted a boolean literal, got %T", value)
		}
		return strconv.FormatBool(b), nil
	}
	return "", fmt.Errorf("no Rust literal for a %s member", kind)
}

// rustIntWidth is smithy-rs's integer mapping: byte→i8, short→i16, integer→i32,
// long→i64. It decides which Binder accessor an expression goes through; a
// literal needs none, because the setter's own parameter type infers it.
func rustIntWidth(model *serviceModel, target string) string {
	switch rustShapeType(model, target) {
	case "byte":
		return "i8"
	case "short":
		return "i16"
	case "long", "bigInteger":
		return "i64"
	default:
		return "i32"
	}
}

// rustFloatWidth is the same for float→f32 and double→f64.
func rustFloatWidth(model *serviceModel, target string) string {
	if rustShapeType(model, target) == "float" {
		return "f32"
	}
	return "f64"
}

// rustShapeType is the shape's own Smithy type, prelude targets included —
// Kind() collapses every width into "integer", and the width is what decides
// the Rust type.
func rustShapeType(model *serviceModel, target string) string {
	switch target {
	case "smithy.api#Byte":
		return "byte"
	case "smithy.api#Short":
		return "short"
	case "smithy.api#Long", "smithy.api#PrimitiveLong", "smithy.api#BigInteger":
		return "long"
	case "smithy.api#Float":
		return "float"
	case "smithy.api#Double", "smithy.api#BigDecimal":
		return "double"
	}
	return model.Shapes[target].Type
}

// rustFloatLiteral keeps a whole number a float: Rust reads `1` as an integer,
// and the setter wants an f32 or an f64.
func rustFloatLiteral(f float64) string {
	s := strconv.FormatFloat(f, 'g', -1, 64)
	if !strings.ContainsAny(s, ".eE") {
		s += ".0"
	}
	return s
}

// ---------------------------------------------------------------------------
// Values, as data
// ---------------------------------------------------------------------------

// rustValueWidth is how wide a composite may be before it is written one entry
// per line. A generated file nobody can read in a diff is a generated file
// nobody reviews.
const rustValueWidth = 76

// rustValue renders one IR value as a scenario::Value — the data half of a
// call, which the runtime evaluates for failure-message field 3 and reads the
// deferred leaves of back by path.
//
// A subtree with no expression anywhere in it is one serde_json literal, which
// is both shorter and exactly the JSON the scenario file writes. Only a subtree
// that carries an expression is broken open into scenario::map / scenario::list.
func rustValue(v any, indent string) (string, error) {
	if !containsExpression(v) {
		literal, err := rustJSONLiteral(v)
		if err != nil {
			return "", err
		}
		return "scenario::lit(::serde_json::json!(" + literal + "))", nil
	}
	if key, arg, ok := exprOf(v); ok {
		switch key {
		case "$lit":
			literal, err := rustJSONLiteral(arg)
			if err != nil {
				return "", err
			}
			return "scenario::lit(::serde_json::json!(" + literal + "))", nil
		case "$ref":
			return fmt.Sprintf("scenario::context(%s)", rustString(arg.(string))), nil
		case "$name":
			return fmt.Sprintf("scenario::name(%s)", rustString(arg.(string))), nil
		case "$concat":
			var parts []string
			for _, part := range arg.([]any) {
				rendered, err := rustValue(part, indent+"    ")
				if err != nil {
					return "", err
				}
				parts = append(parts, rendered)
			}
			return rustComposite("scenario::concat(vec![", parts, "])", indent), nil
		case "$index":
			pair := arg.([]any)
			inner, err := rustValue(pair[0], indent)
			if err != nil {
				return "", err
			}
			n, err := integerOf(pair[1])
			if err != nil {
				return "", err
			}
			return fmt.Sprintf("scenario::index(%s, %d)", inner, n), nil
		case "$base64":
			inner, err := rustValue(arg, indent)
			if err != nil {
				return "", err
			}
			return fmt.Sprintf("scenario::base64(%s)", inner), nil
		case "$now":
			unit, offset, err := nowParts(arg)
			if err != nil {
				return "", err
			}
			return fmt.Sprintf("scenario::now(%s, %d)", rustString(unit), offset), nil
		}
	}
	switch value := v.(type) {
	case []any:
		items := make([]string, 0, len(value))
		for _, item := range value {
			rendered, err := rustValue(item, indent+"    ")
			if err != nil {
				return "", err
			}
			items = append(items, rendered)
		}
		return rustComposite("scenario::list(vec![", items, "])", indent), nil
	case map[string]any:
		var entries []string
		for _, k := range sortedValueKeys(value) {
			rendered, err := rustValue(value[k], indent+"    ")
			if err != nil {
				return "", err
			}
			entries = append(entries, "("+rustString(k)+", "+rendered+")")
		}
		return rustComposite("scenario::map(vec![", entries, "])", indent), nil
	}
	return "", fmt.Errorf("no Rust expression for %T", v)
}

// rustComposite joins a composite's entries, on one line while that is readable
// and one per line when it is not.
func rustComposite(open string, entries []string, close, indent string) string {
	inline := open + strings.Join(entries, ", ") + close
	if len(inline)+len(indent) <= rustValueWidth && !strings.Contains(inline, "\n") {
		return inline
	}
	var b strings.Builder
	b.WriteString(open)
	for _, entry := range entries {
		b.WriteString("\n" + indent + "    " + entry + ",")
	}
	b.WriteString("\n" + indent + close)
	return b.String()
}

// containsExpression reports whether a value carries a deferred expression
// anywhere inside it.
func containsExpression(v any) bool {
	if _, _, ok := exprOf(v); ok {
		return true
	}
	switch value := v.(type) {
	case []any:
		for _, item := range value {
			if containsExpression(item) {
				return true
			}
		}
	case map[string]any:
		for _, item := range value {
			if containsExpression(item) {
				return true
			}
		}
	}
	return false
}

// rustJSONLiteral renders a value as the JSON text serde_json's json! macro
// takes. It is not encoding/json's output: the macro reads Rust tokens, so
// every string is a Rust string literal and an escape encoding/json would write
// as \uXXXX has to be Rust's \u{XX}.
func rustJSONLiteral(v any) (string, error) {
	var b strings.Builder
	if err := writeRustJSON(&b, v); err != nil {
		return "", err
	}
	return b.String(), nil
}

func writeRustJSON(b *strings.Builder, v any) error {
	switch value := v.(type) {
	case nil:
		b.WriteString("null")
		return nil
	case bool:
		b.WriteString(strconv.FormatBool(value))
		return nil
	case string:
		b.WriteString(rustString(value))
		return nil
	case json.Number:
		b.WriteString(value.String())
		return nil
	case float64:
		b.WriteString(strconv.FormatFloat(value, 'g', -1, 64))
		return nil
	case []any:
		b.WriteString("[")
		for i, item := range value {
			if i > 0 {
				b.WriteString(", ")
			}
			if err := writeRustJSON(b, item); err != nil {
				return err
			}
		}
		b.WriteString("]")
		return nil
	case map[string]any:
		b.WriteString("{")
		for i, k := range sortedValueKeys(value) {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString(rustString(k))
			b.WriteString(": ")
			if err := writeRustJSON(b, value[k]); err != nil {
				return err
			}
		}
		b.WriteString("}")
		return nil
	}
	return fmt.Errorf("no JSON literal for %T", v)
}

// rustString renders a Rust string literal. Rust spells a non-printable escape
// \u{XX} rather than JSON's \uXXXX, so this is written rather than borrowed
// from encoding/json.
func rustString(s string) string {
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
			if r < 0x20 || r == 0x7f {
				fmt.Fprintf(&b, `\u{%x}`, r)
				continue
			}
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// ---------------------------------------------------------------------------
// The naming table — shared with -explain -lang rust (explain_typed.go)
// ---------------------------------------------------------------------------

// rustNameCrate is the aws-sdk-rust module for a service: `aws_sdk_` plus the
// SDK id lower-cased with its spaces removed. SQS → aws_sdk_sqs, DynamoDB →
// aws_sdk_dynamodb, Cost Explorer → aws_sdk_costexplorer.
//
// It is deliberately not snake_case of the SDK id: aws-sdk-rust names its
// crates after the id's letters, not its word boundaries, so DynamoDB is
// aws-sdk-dynamodb and not aws-sdk-dynamo-db. compat/model/README.md § Naming
// records the derivation and where it is known to break.
func rustNameCrate(sdkID string) string {
	var b strings.Builder
	b.WriteString("aws_sdk_")
	for _, r := range strings.ToLower(sdkID) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// rustNameOperation is the client method for an operation.
func rustNameOperation(op string) string { return rustIdent(snake(op)) }

// rustNameMember is the builder setter for a modeled member.
func rustNameMember(member string) string { return rustIdent(snake(member)) }

// rustNameType is the crate::types name of a modeled shape.
//
// It is *not* the shape name verbatim, which is what this returned until a
// `batch` recipe needed one. smithy-rs runs every structure and enum name
// through its own `toPascalCase` — `CaseUtils.toSnakeCase` followed by
// `CaseUtils.toCamelCase` — so an acronym run is normalised to a single
// capital. Batch models `CEState`, `CEType` and `JQState`, and the crate
// declares `CeState`, `CeType` and `JqState` (verified against
// aws-sdk-batch 1.126.0's `src/types/_ce_state.rs` and `_jq_state.rs`); the
// verbatim name does not resolve. Every shape in the pilot corpus was already
// its own pascal case, which is why nothing caught this earlier.
//
// `snake` is the same normalisation the operation and member names go through,
// so one rule spells all three.
func rustNameType(target string) string {
	bare := target
	if i := strings.LastIndex(target, "#"); i >= 0 {
		bare = target[i+1:]
	}
	var out strings.Builder
	upperNext := true
	for _, r := range snake(bare) {
		if r == '_' {
			upperNext = true
			continue
		}
		if upperNext && r >= 'a' && r <= 'z' {
			r -= 'a' - 'A'
		}
		upperNext = false
		out.WriteRune(r)
	}
	return out.String()
}

// rustKeywords are the Rust keywords a generated identifier may collide with,
// and that a raw identifier can escape. `self`, `Self`, `super` and `crate`
// cannot be raw identifiers at all; no AWS member name snake_cases onto one, and
// rustIdent leaves them alone rather than emitting an `r#` Rust would reject.
var rustKeywords = map[string]bool{
	"as": true, "async": true, "await": true, "break": true, "const": true,
	"continue": true, "dyn": true, "else": true, "enum": true, "extern": true,
	"false": true, "fn": true, "for": true, "if": true, "impl": true,
	"in": true, "let": true, "loop": true, "match": true, "mod": true,
	"move": true, "mut": true, "pub": true, "ref": true, "return": true,
	"static": true, "struct": true, "trait": true, "true": true, "type": true,
	"unsafe": true, "use": true, "where": true, "while": true,
	// Reserved for future use; smithy-rs escapes these too.
	"abstract": true, "become": true, "box": true, "do": true, "final": true,
	"macro": true, "override": true, "priv": true, "try": true, "typeof": true,
	"unsized": true, "virtual": true, "yield": true,
}

// rustIdent escapes a Rust keyword as a raw identifier, which is what smithy-rs
// does: Organizations models a member called `Type` and the SDK spells its
// setter `r#type`.
func rustIdent(name string) string {
	if rustKeywords[name] {
		return "r#" + name
	}
	return name
}

// ---------------------------------------------------------------------------
// Identifiers
// ---------------------------------------------------------------------------

func rustFileName(service string) string {
	return "scenarios_" + strings.ReplaceAll(service, "-", "_") + "_gen.rs"
}

// rustNameStruct is the exported entry point the index file constructs.
func rustNameStruct(service string) string { return "Scenarios" + camel(service) }

func rustNameGroupConst(group string) string {
	return "GROUP_" + strings.ToUpper(strings.ReplaceAll(group, "-", "_"))
}

func rustNameSetupFn(group string) string { return "setup_" + rustSnakeName(group) }

func rustNameTeardownFn(group string) string { return "teardown_" + rustSnakeName(group) }

func rustNameTestFn(group, test string) string {
	return "test_" + rustSnakeName(group) + "_" + snake(test)
}

// rustSnakeName turns a kebab-case registry name into a snake_case identifier
// fragment: sqs-gen-queue → sqs_gen_queue.
func rustSnakeName(name string) string { return strings.ReplaceAll(name, "-", "_") }

// rustItemNamesAreUnique refuses a service whose group and test names collide
// once folded into Rust identifiers. Two names differing only in where their
// hyphens fall would otherwise emit two items of the same name, and the suite
// would fail to build with no indication of which pair caused it.
//
// Detecting the collision is uniqueNames (emit_shared.go); what is Rust's own
// is which identifiers a group claims — the group's `const` included, because
// a module's functions and consts share one namespace.
func rustItemNamesAreUnique(service string, groups []group) error {
	var claims []nameClaim
	for _, g := range groups {
		claims = append(claims,
			nameClaim{rustNameGroupConst(g.Name), g.Name + " group"},
			nameClaim{rustNameSetupFn(g.Name), g.Name + " setup"},
			nameClaim{rustNameTeardownFn(g.Name), g.Name + " teardown"})
		for _, t := range g.Tests {
			claims = append(claims, nameClaim{rustNameTestFn(g.Name, t.Name), g.Name + "/" + t.Name})
		}
	}
	return uniqueNames(service, "Rust", claims)
}

// ---------------------------------------------------------------------------
// Refusals
// ---------------------------------------------------------------------------

// rustRefusals reports the members of a group's calls this backend cannot
// express. A group with any is not emitted and is scoped away from rust-sdk.
//
// The check is the very spelling emission would write, so one code path decides
// "can this be emitted" and "how", and the two cannot drift.
func rustRefusals(gen *generation, g group) []gap {
	crate := rustNameCrate(gen.scenario.Client.SDKID)
	return refusals(gen, g, rustEmitReason, refusalChecks{
		member: func(op, member string, v any) error {
			input := gen.model.InputShape(op)
			if input == "" {
				return fmt.Errorf("%s takes no input in the model, so it has no member %s", op, member)
			}
			target, ok := gen.model.MemberTarget(input, member)
			if !ok {
				return fmt.Errorf("%s has no member %s in the model", input, member)
			}
			if _, err := rustSetterCalls(gen.model, crate, rustNameMember(member), target, v, member, rustCallIndent, &rustBindings{}); err != nil {
				return fmt.Errorf("%s.%s cannot be spelled as Rust: %v", op, member, err)
			}
			return nil
		},
	})
}

// ---------------------------------------------------------------------------
// The index file
// ---------------------------------------------------------------------------

// rustIndexPath is the module the suite's groups module declares. It is emitted
// whether or not rust-sdk is a scenario backend, so the crate compiles either
// way.
var rustIndexPath = rustSuiteDir + "/scenarios_gen.rs"

// emitRustIndex renders the list of generated service groups.
//
// The per-service modules are declared here rather than in the hand-written
// groups/mod.rs, so adding a service to the corpus needs no hand edit. A module
// declared in a file that is not mod.rs looks for its source in a directory
// named after that file, so each declaration carries the #[path] that puts it
// back beside this one.
func emitRustIndex(services []string) ([]byte, error) {
	sorted := append([]string(nil), services...)
	sort.Strings(sorted)
	w := &rustWriter{}
	w.linef("// Code generated by cmd/compatgen; DO NOT EDIT.")
	w.linef("//")
	w.linef("// The generated service groups, which main merges with the hand-written ones.")
	w.linef("// The list is empty until cmd/compatgen's scenarioBackends table names rust-sdk.")
	w.linef("")
	w.linef("use std::sync::Arc;")
	w.linef("")
	w.linef("use crate::clients::AwsClients;")
	w.linef("use crate::groups::ServiceGroup;")
	for _, service := range sorted {
		w.linef("")
		w.linef("#[path = %s]", rustString(rustFileName(service)))
		w.linef("mod %s;", strings.TrimSuffix(rustFileName(service), ".rs"))
	}
	w.linef("")
	if len(sorted) == 0 {
		w.linef("pub fn scenario_groups(_clients: &Arc<AwsClients>) -> Vec<Box<dyn ServiceGroup>> {")
		w.linef("    Vec::new()")
		w.linef("}")
	} else {
		w.linef("pub fn scenario_groups(clients: &Arc<AwsClients>) -> Vec<Box<dyn ServiceGroup>> {")
		w.linef("    vec![")
		for _, service := range sorted {
			w.linef("        Box::new(%s::%s::new(clients)),",
				strings.TrimSuffix(rustFileName(service), ".rs"), rustNameStruct(service))
		}
		w.linef("    ]")
		w.linef("}")
	}
	return []byte(w.String()), nil
}

// ---------------------------------------------------------------------------
// Writing
// ---------------------------------------------------------------------------

// rustWriter is sourceWriter with the two things Rust needs and no other
// backend does. There is no formatter to run afterwards — cmd/compatgen has no
// Rust toolchain and CI's docs job has only Go — so the indentation written
// here is the output's actual layout.
type rustWriter struct{ sourceWriter }

// linef splits on newlines, so a multi-line template writes one entry per line
// and closeSome below can address the last of them.
func (w *rustWriter) linef(format string, args ...any) {
	for _, line := range strings.Split(fmt.Sprintf(format, args...), "\n") {
		w.sourceWriter.linef("%s", line)
	}
}

// closeSome turns the "}," a nested call struct ends with into the "})," that
// closes the Some(...) it was written inside.
func (w *rustWriter) closeSome(indent string) {
	last := len(w.lines) - 1
	if w.lines[last] == indent+"}," {
		w.lines[last] = indent + "}),"
	}
}
