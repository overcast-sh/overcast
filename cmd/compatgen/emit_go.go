//go:build dev

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"go/format"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

// The go-sdk source emitter — docs/plans/compat-coverage-modelgen.md §3.2 D1,
// phase G3.
//
// The three interpreters execute the scenario IR at run time. The AWS SDK for
// Go v2 has no public dynamic-dispatch API, and the plan rejects reaching into
// smithy-go's marshaller layer to fake one: the whole value of running eight
// suites is that each exercises its own real typed serialization path. So this
// file emits Go source — one function per scenario test, each building a real
// typed input struct and calling a real client method — which the go-sdk
// suite's ordinary build compiles.
//
// What is emitted is the *data* plus the typed calls. Semantics — the context
// bag, value expressions, the closed check set, error matching, `eventually`,
// the six-field failure message — live once in the suite's hand-written
// internal/scenario package and are never re-emitted.
//
// # The naming table
//
// Everything this emitter knows about spelling Go is in the goName* functions
// below, and `-explain -lang go` (explain_typed.go) renders through the same
// ones, so the pseudo-code a reader reproduces a failure with is the source the
// emitter wrote. The table is deliberately tiny:
//
//	service → github.com/aws/aws-sdk-go-v2/service/<lower(sdkId), spaces removed>
//	operation → &<pkg>.<Op>Input{} and client.<Op>(ctx, in)
//	member → the field the SDK declares for it, looked up by the modeled name
//	         with its first letter capitalized
//	value → that field's own type, spelled by emit_go_spell.go
//
// # Why the SDK's own types are read at emit time
//
// `in.QueueUrl = aws.String(v)` compiles only where smithy-go made that member
// a pointer, and the pinned shape snapshot cannot say whether it did: the
// snapshot and the vendored SDK are generated from different revisions of the
// same AWS model, and for the pilot service they already disagree —
// ReceiveMessage's MaxNumberOfMessages, VisibilityTimeout and WaitTimeSeconds
// target NullableInteger in models/aws/shapes/sqs.json, which says pointer,
// and are plain int32 fields in aws-sdk-go-v2/service/sqs.
//
// So the emitter asks the SDK rather than the model. gosdktypes.go loads
// `github.com/aws/aws-sdk-go-v2/service/<pkg>` from the go-sdk suite module —
// the same module the emitted source is compiled in — and emit_go_spell.go
// turns `<Op>Input`'s declared field types into source: aws.String for a
// pointer, a bare literal for a value, types.<Enum>(…) for a named string,
// []types.<Struct>{…} and map[string]string{…} recursively. What that buys is
// compile-time evidence that the call is well-typed, which is the typed
// backends' whole marginal value over the three interpreters (plan §3.2), and
// a member the SDK has no field for becomes a refusal here instead of a red
// compat result on the wire.
//
// The java, dotnet and rust emitters face the same question. dotnet answers it
// the same way — nullable value types are not derivable from the model —
// while java's builders and Rust's Option<T> are, and need no lookup.

// goSuiteDir is where the emitted files live, repository-relative.
const goSuiteDir = "compat/suites/go-sdk/internal/groups"

// goEmitReason is the refusal a member the emitter cannot express produces.
// Unlike every other reason in gaps.json it does not mean "no test": the
// operation is generated and the interpreters run it. It means this backend
// cannot compile it, so the whole group is scoped away from go-sdk in the
// generated registry — a suite that cannot execute a group must not be listed
// as able to.
//
// Five things produce it, and every one of them used to be either a run-time
// failure or a request the SDK quietly dropped:
//
//	the member's modeled kind has no IR literal    a timestamp, blob, document, union
//	the SDK's <Op>Input does not exist             the vendored SDK is older than the model
//	the SDK has no field for the member            smithy-go renamed or dropped it
//	the field's type has no Go literal             a union, or a type from a third package
//	a value-typed member is set to its zero value  the SDK would not serialize it
const goEmitReason = "go-emit-unsupported"

// goUnsupportedKinds are the modeled member kinds no value in the IR's grammar
// can carry. Timestamps, documents and unions have no portable value and are
// refused upstream — an error in a recipe or an authored scenario, and
// `no-portable-value` for a binding (binder.go) — so this is a backstop rather
// than a live path. A blob is not here: `$base64` spells it, as a []byte
// literal or a scenario.Blob binding (emit_go_spell.go).
var goUnsupportedKinds = map[string]bool{
	"timestamp": true,
	"document":  true,
	"union":     true,
}

// emitGo renders one service's generated groups as Go source for the go-sdk
// suite. loader resolves the SDK's own field types, from the go-sdk suite
// module for a real run and from testdata/awssdk for the generator's tests.
func emitGo(gen *generation, loader *goSDKTypes) (*sourceEmission, error) {
	e := &sourceEmission{
		Path:    goSuiteDir + "/" + goFileName(gen.unit),
		Refused: map[string]bool{},
	}
	s := gen.scenario
	svc, err := loader.service(s.Client.SDKID)
	if err != nil {
		return nil, err
	}

	groups := make([]group, 0, len(s.Groups))
	for _, g := range s.Groups {
		if refusals := goRefusals(gen, svc, g); len(refusals) > 0 {
			e.Refused[g.Name] = true
			e.Gaps = append(e.Gaps, refusals...)
			continue
		}
		groups = append(groups, g)
	}

	if err := goMethodNamesAreUnique(gen.unit, groups); err != nil {
		return nil, err
	}

	// The body is written first so the import block can name exactly what it
	// turned out to need: whether a service reaches for aws.String or the
	// types package depends on the field types its calls touch, and an unused
	// import does not compile.
	sp := &goSpeller{svc: svc}
	recv := goNameReceiver(gen.unit)
	body := &sourceWriter{}
	goWriteHeaderComment(body, gen)
	body.linef("func %s(c *clients.Clients) ServiceGroup {", goNameConstructor(gen.unit))
	body.linef("\tg := &%s{c: c}", recv)
	body.linef("\treturn ServiceGroup{")
	body.linef("\t\tName: %q,", "scenarios/"+gen.unit)
	body.linef("\t\tImpls: map[string]harness.TestFn{")
	for _, g := range groups {
		for _, t := range g.Tests {
			body.linef("\t\t\t%q: g.%s,", g.Name+":"+t.Name, goNameTestMethod(g.Name, t.Name))
		}
	}
	body.linef("\t\t},")
	body.linef("\t\tSetup: map[string]func(context.Context, *harness.TestContext) error{")
	for _, g := range groups {
		body.linef("\t\t\t%q: g.%s,", g.Name, goNameSetupMethod(g.Name))
	}
	body.linef("\t\t},")
	body.linef("\t\tTeardown: map[string]func(context.Context, *harness.TestContext) error{")
	for _, g := range groups {
		body.linef("\t\t\t%q: g.%s,", g.Name, goNameTeardownMethod(g.Name))
	}
	body.linef("\t\t},")
	body.linef("\t}")
	body.linef("}")
	body.linef("")
	body.linef("type %s struct {", recv)
	body.linef("\tc      *clients.Clients")
	body.linef("\tonce   sync.Once")
	body.linef("\tclient *%s.Client", svc.Name)
	body.linef("}")
	body.linef("")
	body.linef("// cl builds this service's client once, from the config the suite's")
	body.linef("// hand-written groups share. A generated group builds its own rather than")
	body.linef("// adding an accessor to internal/clients for every service the generator")
	body.linef("// learns to cover; nothing else about the client differs.")
	body.linef("func (g *%s) cl() *%s.Client {", recv, svc.Name)
	body.linef("\tg.once.Do(func() { g.client = %s.NewFromConfig(g.c.Config()) })", svc.Name)
	body.linef("\treturn g.client")
	body.linef("}")

	for _, g := range groups {
		if err := goWriteGroup(body, gen, sp, recv, g); err != nil {
			return nil, err
		}
	}

	w := &sourceWriter{}
	w.linef("// Code generated by cmd/compatgen; DO NOT EDIT.")
	w.linef("")
	w.linef("package groups")
	w.linef("")
	w.linef("import (")
	w.linef("\t%q", "context")
	w.linef("\t%q", "sync")
	w.linef("")
	for _, path := range goImports(sp) {
		w.linef("\t%q", path)
	}
	w.linef(")")
	w.linef("")
	w.lines = append(w.lines, body.lines...)

	contents, err := format.Source([]byte(w.String()))
	if err != nil {
		// The emitter produced source Go cannot parse. That is a generator
		// bug, and it must stop generation rather than land a file the suite
		// cannot build; the offending source is printed so the fault is
		// diagnosable from the failure alone.
		return nil, fmt.Errorf("emitted Go for %s does not parse: %w\n%s", gen.unit, err, w.String())
	}
	e.Contents = contents
	sortGaps(e.Gaps)
	return e, nil
}

// goImports is the emitted file's second import group, sorted. gofmt does not
// reorder imports — only goimports does, and generated output must not depend
// on a tool the generator does not run — so the order is produced rather than
// corrected.
func goImports(sp *goSpeller) []string {
	paths := []string{
		sp.svc.Path,
		"github.com/overcast-sh/overcast-compat-go-sdk/internal/clients",
		"github.com/overcast-sh/overcast-compat-go-sdk/internal/harness",
		"github.com/overcast-sh/overcast-compat-go-sdk/internal/scenario",
	}
	if sp.usesAWS {
		paths = append(paths, goAWSModule)
	}
	if sp.usesTypes {
		paths = append(paths, sp.svc.TypesPath)
	}
	sort.Strings(paths)
	return paths
}

func goWriteHeaderComment(w *sourceWriter, gen *generation) {
	w.linef("// %s returns the %s groups.", goNameConstructor(gen.unit), gen.describe())
	w.linef("//")
	w.linef("// Generated from %s by cmd/compatgen.", gen.file)
	w.linef("// The semantics live in internal/scenario; this file is the data and the")
	w.linef("// typed SDK calls.")
}

// goWriteGroup emits one group: its identity, its setup and teardown hooks —
// registered even when empty, because an empty phase is a no-op and not a
// missing one — and one function per test.
func goWriteGroup(w *sourceWriter, gen *generation, sp *goSpeller, recv string, g group) error {
	file := gen.file
	w.linef("")
	w.linef("var %s = scenario.Group{Name: %q, File: %q}", goNameGroupVar(g.Name), g.Name, file)

	w.linef("")
	w.linef("func (g *%s) %s(ctx context.Context, t *harness.TestContext) error {", recv, goNameSetupMethod(g.Name))
	if err := goWriteHookBody(w, sp, g, "RunSetup", g.Setup); err != nil {
		return err
	}
	w.linef("}")

	w.linef("")
	w.linef("func (g *%s) %s(ctx context.Context, t *harness.TestContext) error {", recv, goNameTeardownMethod(g.Name))
	if err := goWriteHookBody(w, sp, g, "RunTeardown", g.Teardown); err != nil {
		return err
	}
	w.linef("}")

	for _, t := range g.Tests {
		w.linef("")
		w.linef("func (g *%s) %s(ctx context.Context, t *harness.TestContext) error {", recv, goNameTestMethod(g.Name, t.Name))
		w.linef("\treturn %s.RunTest(ctx, t, %q, scenario.Test{", goNameGroupVar(g.Name), t.Name)
		if err := goWriteCall(w, sp, t.Call, "\t\t", "Call: "); err != nil {
			return err
		}
		w.linef("\t\tAssert: []scenario.Clause{")
		for _, a := range t.Assert {
			if err := goWriteClause(w, sp, a, "\t\t\t"); err != nil {
				return err
			}
		}
		w.linef("\t\t},")
		w.linef("\t})")
		w.linef("}")
	}
	return nil
}

func goWriteHookBody(w *sourceWriter, sp *goSpeller, g group, method string, calls []call) error {
	if len(calls) == 0 {
		w.linef("\t// No %s steps: an empty phase is a no-op, not a missing one.",
			strings.ToLower(strings.TrimPrefix(method, "Run")))
		w.linef("\treturn %s.%s(ctx, t)", goNameGroupVar(g.Name), method)
		return nil
	}
	w.linef("\treturn %s.%s(ctx, t,", goNameGroupVar(g.Name), method)
	for _, c := range calls {
		if err := goWriteCall(w, sp, c, "\t\t", ""); err != nil {
			return err
		}
	}
	w.linef("\t)")
	return nil
}

// goWriteCall emits one scenario.Call: the operation, the params as the
// scenario file writes them, the typed input build, the client method and the
// exports. prefix carries whatever the call's position needs in front of it —
// a struct field name, or the "&" the list clauses want, since those take a
// pointer so that "no call of its own" can be nil.
func goWriteCall(w *sourceWriter, sp *goSpeller, c call, indent, prefix string) error {
	w.linef("%s%sscenario.Call{", indent, prefix)
	w.linef("%s\tOp: %q,", indent, c.Op)
	raw, err := goRawParams(c.Params)
	if err != nil {
		return err
	}
	w.linef("%s\tParams: %s,", indent, raw)
	w.linef("%s\tBuild: func(b *scenario.Binder) any {", indent)
	lines, err := goInputLines(sp, c.Op, c.Params, indent+"\t\t")
	if err != nil {
		return err
	}
	for _, line := range lines {
		w.linef("%s\t\t%s", indent, line)
	}
	w.linef("%s\t\treturn in", indent)
	w.linef("%s\t},", indent)
	w.linef("%s\tSend: func(ctx context.Context, in any) (any, error) {", indent)
	w.linef("%s\t\treturn g.cl().%s", indent, goNameClientCall(sp.svc.Name, c.Op))
	w.linef("%s\t},", indent)
	if len(c.Export) > 0 {
		w.linef("%s\tExport: map[string]string{", indent)
		for _, path := range sortedStringKeys(c.Export) {
			w.linef("%s\t\t%q: %q,", indent, path, c.Export[path])
		}
		w.linef("%s\t},", indent)
	}
	w.linef("%s},", indent)
	return nil
}

// goWriteClause emits one assertion clause through the constructors in
// internal/scenario, which are the same closed set ir.go builds.
func goWriteClause(w *sourceWriter, sp *goSpeller, a assertion, indent string) error {
	switch a.Kind {
	case assertResponseField:
		w.linef("%sscenario.ResponseField(", indent)
		goWriteChecks(w, a.Checks, indent+"\t")
		w.linef("%s),", indent)
	case assertReadback:
		w.linef("%sscenario.Readback(", indent)
		if err := goWriteCall(w, sp, *a.Call, indent+"\t", ""); err != nil {
			return err
		}
		goWriteChecks(w, a.Checks, indent+"\t")
		w.linef("%s),", indent)
	case assertListContains, assertAbsent:
		if a.Kind == assertAbsent && a.Error != nil {
			w.linef("%sscenario.AbsentByError(", indent)
			if err := goWriteCall(w, sp, *a.Call, indent+"\t", ""); err != nil {
				return err
			}
			w.linef("%s\tscenario.Error(%q, %q),", indent, a.Error.Shape, a.Error.Code)
			w.linef("%s),", indent)
			return nil
		}
		name := "ListContains"
		if a.Kind == assertAbsent {
			name = "AbsentFromList"
		}
		w.linef("%sscenario.%s(", indent, name)
		if a.Call == nil {
			w.linef("%s\tnil,", indent)
		} else if err := goWriteCall(w, sp, *a.Call, indent+"\t", "&"); err != nil {
			return err
		}
		w.linef("%s\t%q,", indent, a.ItemsPath)
		for _, path := range sortedValueKeys(a.Where) {
			value, err := goValue(a.Where[path], indent+"\t")
			if err != nil {
				return err
			}
			w.linef("%s\tscenario.Where(%q, %s),", indent, path, value)
		}
		w.linef("%s),", indent)
	case assertErrorCode:
		w.linef("%sscenario.ErrorCode(scenario.Error(%q, %q)),", indent, a.Error.Shape, a.Error.Code)
	case assertEventually:
		w.linef("%sscenario.Eventually(%d, %d,", indent, a.MaxAttempts, a.DelayMs)
		if err := goWriteClause(w, sp, *a.Assert, indent+"\t"); err != nil {
			return err
		}
		w.linef("%s),", indent)
	default:
		return fmt.Errorf("cannot emit assertion kind %q", a.Kind)
	}
	return nil
}

// goWriteChecks emits a clause's checks in path order, so a failure message is
// the same on every run and in every backend.
func goWriteChecks(w *sourceWriter, checks map[string]check, indent string) {
	for _, path := range sortedCheckPaths(checks) {
		w.linef("%s%s,", indent, goCheck(path, checks[path], indent))
	}
}

func goCheck(path string, c check, indent string) string {
	switch {
	case c.NonEmpty:
		return fmt.Sprintf("scenario.NonEmpty(%q)", path)
	case c.IsList:
		return fmt.Sprintf("scenario.IsList(%q)", path)
	case c.Missing:
		return fmt.Sprintf("scenario.Missing(%q)", path)
	case c.Matches != "":
		return fmt.Sprintf("scenario.Matches(%q, %q)", path, c.Matches)
	case c.EqualsJSON != nil:
		// The document travels as its compact JSON text, which the runtime
		// parses once; validateAssertion has already held it to the grammar.
		text, _ := equalsJSONText(c.EqualsJSON)
		return fmt.Sprintf("scenario.EqualsJSON(%q, %q)", path, text)
	default:
		value, err := goValue(c.Equals, indent)
		if err != nil {
			// Unreachable: goValue is total over the IR's value grammar, and
			// validateAssertion has already rejected anything else.
			value = fmt.Sprintf("%#v", c.Equals)
		}
		return fmt.Sprintf("scenario.Equals(%q, %s)", path, value)
	}
}

// ---------------------------------------------------------------------------
// The naming table — shared with -explain -lang go (explain_typed.go)
// ---------------------------------------------------------------------------

// goNamePackage is the Go SDK v2 package for a service: the SDK id lower-cased
// with spaces removed. Cost Explorer → costexplorer.
//
// compat/model/README.md § Naming records the two derivations known to break
// (SFN → sfn, ELB → elasticloadbalancing); neither service has a scenario, and
// the override table belongs here when one does.
func goNamePackage(sdkID string) string {
	return strings.ToLower(strings.ReplaceAll(sdkID, " ", ""))
}

// goNameModule is the import path of that package.
func goNameModule(sdkID string) string {
	return "github.com/aws/aws-sdk-go-v2/service/" + goNamePackage(sdkID)
}

// goNameInput is the operation's input struct: &sqs.CreateQueueInput{}.
func goNameInput(pkg, op string) string { return fmt.Sprintf("&%s.%sInput{}", pkg, op) }

// goNameInputType is that struct's type, for the assertion Send does.
func goNameInputType(pkg, op string) string { return fmt.Sprintf("*%s.%sInput", pkg, op) }

// goNameClientCall is the client method call, given an input already built.
func goNameClientCall(pkg, op string) string {
	return fmt.Sprintf("%s(ctx, in.(%s))", op, goNameInputType(pkg, op))
}

// goNameField is where the emitter *looks* for the Go field of a modeled
// member: the member name with its first letter capitalized. Almost every AWS
// member is already PascalCase, but not all — SQS models CreateQueue's tags as
// `tags` and ListDeadLetterSourceQueues' page as `queueUrls`, and the Go SDK
// spells both with a capital.
//
// It is a lookup key, not the answer. smithy-go's rule is capitalization plus
// reserved-word and collision handling, and reproducing only half of it is
// what made a renamed member compile and then fail at run time. So the field
// the SDK actually declares is what gets written (goSDKField), and a member
// with no field at all is refused. The member's own name stays the label the
// emitted expression carries, because that is what a failure message must
// name.
func goNameField(member string) string {
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

// goInputLines renders the statements that build one call's typed input. It is
// the emitter's Build body and, line for line, what `-explain -lang go` prints,
// which is what keeps the two from drifting.
//
// Each member is one assignment to the field the SDK declares, spelled as that
// field's own type: the compiler then checks the call, which is the property
// the reflective binder this replaced could not offer.
func goInputLines(sp *goSpeller, op string, params map[string]any, indent string) ([]string, error) {
	lines := []string{fmt.Sprintf("in := %s", goNameInput(sp.svc.Name, op))}
	for _, member := range sortedValueKeys(params) {
		field, err := sp.field(op, member)
		if err != nil {
			return nil, fmt.Errorf("%s.%s: %w", op, member, err)
		}
		value, err := sp.value(field.Type(), params[member], member, indent, false)
		if err != nil {
			return nil, fmt.Errorf("%s.%s: %w", op, member, err)
		}
		lines = append(lines, fmt.Sprintf("in.%s = %s", field.Name(), value))
	}
	return lines, nil
}

// goValueWidth is how wide a composite may be before it is written one entry
// per line. gofmt does not break a long literal, so a params map rendered
// inline stays inline however wide it gets, and a generated file nobody can
// read in a diff is a generated file nobody reviews.
const goValueWidth = 80

// goValue renders one IR value as an *untyped* Go expression, indented for
// the line it will sit on: an object is a map[string]any, a list a []any, a
// scalar itself, and each of the seven expression forms is a scenario.Value
// constructor. Nothing else is representable, which is what makes this total.
//
// Untyped is right in the two places it is still used. An assertion's expected
// value is compared in the IR's own type system against a response read back
// as a document, so it is data on both sides. And the argument of a value
// expression — a $concat part, the list a $index takes — is evaluated at run
// time, where there is no field type to spell it against; only the
// expression's *result* has one, which is what scenario.Bind converts it to.
// Input members themselves go through goSpeller instead (emit_go_spell.go).
func goValue(v any, indent string) (string, error) {
	if key, arg, ok := exprOf(v); ok {
		switch key {
		case "$lit":
			inner, err := goValue(arg, indent)
			if err != nil {
				return "", err
			}
			return fmt.Sprintf("scenario.Lit(%s)", inner), nil
		case "$ref":
			return fmt.Sprintf("scenario.Ref(%q)", arg.(string)), nil
		case "$name":
			return fmt.Sprintf("scenario.Name(%q)", arg.(string)), nil
		case "$concat":
			var parts []string
			for _, part := range arg.([]any) {
				rendered, err := goValue(part, indent)
				if err != nil {
					return "", err
				}
				parts = append(parts, rendered)
			}
			return fmt.Sprintf("scenario.Concat(%s)", strings.Join(parts, ", ")), nil
		case "$index":
			pair := arg.([]any)
			inner, err := goValue(pair[0], indent)
			if err != nil {
				return "", err
			}
			n, err := integerOf(pair[1])
			if err != nil {
				return "", err
			}
			return fmt.Sprintf("scenario.Index(%s, %d)", inner, n), nil
		case "$base64":
			inner, err := goValue(arg, indent)
			if err != nil {
				return "", err
			}
			return fmt.Sprintf("scenario.Base64(%s)", inner), nil
		case "$now":
			unit, offset, err := nowParts(arg)
			if err != nil {
				return "", err
			}
			return fmt.Sprintf("scenario.Now(%q, %d)", unit, offset), nil
		}
	}
	switch value := v.(type) {
	case nil:
		return "nil", nil
	case string:
		return strconv.Quote(value), nil
	case bool:
		return strconv.FormatBool(value), nil
	case json.Number:
		return value.String(), nil
	case float64:
		return strconv.FormatFloat(value, 'g', -1, 64), nil
	case []any:
		items := make([]string, 0, len(value))
		for _, item := range value {
			rendered, err := goValue(item, indent+"\t")
			if err != nil {
				return "", err
			}
			items = append(items, rendered)
		}
		return goComposite("[]any{", items, indent), nil
	case map[string]any:
		var entries []string
		for _, k := range sortedKeys(value) {
			rendered, err := goValue(value[k], indent+"\t")
			if err != nil {
				return "", err
			}
			entries = append(entries, fmt.Sprintf("%q: %s", k, rendered))
		}
		return goComposite("map[string]any{", entries, indent), nil
	}
	return "", fmt.Errorf("no Go expression for %T", v)
}

// goComposite joins a composite literal's entries, on one line while that is
// readable and one per line when it is not.
func goComposite(open string, entries []string, indent string) string {
	inline := open + strings.Join(entries, ", ") + "}"
	if len(inline) <= goValueWidth {
		return inline
	}
	var b strings.Builder
	b.WriteString(open)
	for _, entry := range entries {
		b.WriteString("\n" + indent + "\t" + entry + ",")
	}
	b.WriteString("\n" + indent + "}")
	return b.String()
}

// goRawParams renders a call's params as the scenario file writes them —
// expressions unevaluated — for failure-message field 3 when a value could not
// be evaluated and nothing was sent. It is the same canonical JSON the
// interpreters print in that case.
func goRawParams(params map[string]any) (string, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if params == nil {
		params = map[string]any{}
	}
	if err := enc.Encode(params); err != nil {
		return "", err
	}
	raw := strings.TrimRight(buf.String(), "\n")
	if !strings.ContainsAny(raw, "`\r\n") {
		return "`" + raw + "`", nil
	}
	return strconv.Quote(raw), nil
}

// ---------------------------------------------------------------------------
// Identifiers
// ---------------------------------------------------------------------------

func goFileName(service string) string {
	return "scenarios_" + strings.ReplaceAll(service, "-", "_") + "_gen.go"
}

// goNameConstructor is the exported entry point the index file calls.
func goNameConstructor(service string) string { return "Scenarios" + camel(service) }

func goNameReceiver(service string) string { return goLowerCamel(service) + "Scenarios" }

func goNameGroupVar(group string) string { return "group" + camel(group) }

func goNameSetupMethod(group string) string { return "setup" + camel(group) }

func goNameTeardownMethod(group string) string { return "teardown" + camel(group) }

func goNameTestMethod(group, test string) string { return "test" + camel(group) + test }

func goLowerCamel(name string) string {
	upper := camel(name)
	if upper == "" {
		return upper
	}
	return strings.ToLower(upper[:1]) + upper[1:]
}

// goMethodNamesAreUnique refuses a service whose group and test names collide
// once folded into Go identifiers. Two names differing only in where their
// hyphens fall would otherwise emit two methods of the same name, and the
// suite would fail to build with no indication of which pair caused it.
//
// The collision itself is uniqueNames (emit_shared.go); what is Go's own is
// which identifiers a group claims.
func goMethodNamesAreUnique(service string, groups []group) error {
	var claims []nameClaim
	for _, g := range groups {
		claims = append(claims,
			nameClaim{goNameSetupMethod(g.Name), g.Name + " setup"},
			nameClaim{goNameTeardownMethod(g.Name), g.Name + " teardown"})
		for _, t := range g.Tests {
			claims = append(claims, nameClaim{goNameTestMethod(g.Name, t.Name), g.Name + "/" + t.Name})
		}
	}
	return uniqueNames(service, "Go", claims)
}

// ---------------------------------------------------------------------------
// Refusals
// ---------------------------------------------------------------------------

// goRefusals reports the members of a group's calls this backend cannot
// express. A group with any is not emitted and is scoped away from go-sdk.
//
// The SDK answers most of it, and it does so by attempting the very spelling
// emission would write, through a throwaway speller: one code path decides
// "can this be emitted" and "how", so the two cannot drift, and a group that
// is refused leaves no import behind.
//
// The model's kind is consulted in between, and takes precedence over the type
// for the kinds it knows about: a timestamp, blob, document or union has no
// literal in the IR's value grammar at all, and saying that in the model's own
// vocabulary is more use to a recipe author than naming the Go type it happens
// to have.
func goRefusals(gen *generation, svc *goSDKService, g group) []gap {
	probe := &goSpeller{svc: svc}
	return refusals(gen, g, goEmitReason, refusalChecks{
		call: func(op string) (string, error) {
			if _, err := svc.Input(op); err != nil {
				return op + "Input", err
			}
			return "", nil
		},
		member: func(op, member string, v any) error {
			if input := gen.model.InputShape(op); input != "" {
				if target, ok := gen.model.MemberTarget(input, member); ok {
					if kind := gen.model.Kind(target); goUnsupportedKinds[kind] {
						return fmt.Errorf("the go-sdk emitter has no Go value expression for a %s member (%s.%s)", kind, input, member)
					}
				}
			}
			field, err := probe.field(op, member)
			if err != nil {
				return err
			}
			if _, err := probe.value(field.Type(), v, member, "", false); err != nil {
				return fmt.Errorf("%s.%s cannot be spelled as Go: %v", op, member, err)
			}
			return nil
		},
	})
}

// ---------------------------------------------------------------------------
// The index file
// ---------------------------------------------------------------------------

// goIndexPath is the file groups.All() calls into. It is emitted whether or
// not go-sdk is a scenario backend, so the package compiles either way.
var goIndexPath = goSuiteDir + "/scenarios_gen.go"

// emitGoIndex renders the list of generated service constructors.
func emitGoIndex(services []string) ([]byte, error) {
	sorted := append([]string(nil), services...)
	sort.Strings(sorted)
	w := &sourceWriter{}
	w.linef("// Code generated by cmd/compatgen; DO NOT EDIT.")
	w.linef("")
	w.linef("package groups")
	w.linef("")
	w.linef("import %q", "github.com/overcast-sh/overcast-compat-go-sdk/internal/clients")
	w.linef("")
	w.linef("// scenarioGroups returns every generated service group, which All appends")
	w.linef("// to the hand-written ones. The list is empty until cmd/compatgen's")
	w.linef("// scenarioBackends table names go-sdk.")
	if len(sorted) == 0 {
		w.linef("func scenarioGroups(_ *clients.Clients) []ServiceGroup { return nil }")
	} else {
		w.linef("func scenarioGroups(c *clients.Clients) []ServiceGroup {")
		w.linef("\treturn []ServiceGroup{")
		for _, service := range sorted {
			w.linef("\t\t%s(c),", goNameConstructor(service))
		}
		w.linef("\t}")
		w.linef("}")
	}
	contents, err := format.Source([]byte(w.String()))
	if err != nil {
		return nil, fmt.Errorf("emitted Go index does not parse: %w\n%s", err, w.String())
	}
	return contents, nil
}

// Everything this emitter writes goes through go/format before it lands (the
// sourceWriter it writes into is emit_shared.go's), so the indentation the
// linef calls above carry is a readability aid for whoever reads this file
// rather than the committed output's actual layout.
