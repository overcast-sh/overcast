//go:build dev

package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// `-explain <group>/<test> -lang <language>` — docs/plans/compat-coverage-modelgen.md §3.2.
//
// Renders one generated test as idiomatic pseudo-code so a failing generated
// test can be reproduced by hand in seconds. It reads the committed scenario
// file, not the recipe, so what it shows is exactly what an interpreter
// executes. The renderings are pseudo-code: readable and faithful to each
// SDK's conventions, not guaranteed to compile.
//
// This file holds the command, the rendering machinery every backend shares
// and the naming derivations compat/model/README.md documents. The
// per-backend styles live beside it, split by how an SDK takes its
// parameters: explain_dynamic.go for the ones that take data (python, node,
// cli), explain_typed.go for the ones that take request types (go, java,
// dotnet, rust).

type renderer func(env renderEnv, s *scenario, g *group, t *test) string

// renderEnv is what a rendering needs from outside the scenario file. Only the
// four source-emitting backends use it: they are the renderings that reproduce
// real emitted source, so each spells a member the way its emitter does and has
// to read what the emitter reads — the vendored SDK's declarations for Go, the
// pinned shape snapshot for Java and Rust, and the snapshot plus the committed
// SDK type table for .NET. The other three are pseudo-code derived from the IR
// alone.
type renderEnv struct {
	goTypes *goSDKTypes
	// dotnet reads the .NET SDK type table (dotnetsdktypes.go) the .NET
	// rendering spells members through, as the emitter does. A func, with its
	// error returned to the rendering, for the reason model's is below: a nil
	// func is "no table was configured", and `-explain` must still say
	// something useful without one.
	dotnet func() (*dotnetSDKTypes, error)
	// model resolves a service's pinned shapes. It is not one backend's own:
	// every emitter that spells a member from the model rather than from its
	// SDK reads it here, which is why it is named after what it returns and not
	// after the first backend to ask for it. runExplain reads the repository;
	// the generator's own tests hand over the fixture model they already hold,
	// which is what keeps them hermetic and off the committed snapshot.
	//
	// The error is returned to the rendering rather than being fatal, because
	// `-explain` is a reader's tool and must still say something useful on a
	// checkout where a snapshot cannot be read — see javaStyle and rustStyle. A
	// nil func is "no snapshot was configured", which is what the renderings
	// that need none are given.
	model func(service string) (*serviceModel, error)
}

// speller resolves one service's SDK types for the Go rendering. The error is
// returned rather than fatal: `-explain` is a reader's tool and must still say
// something useful on a checkout that has never fetched the go-sdk suite
// module's dependencies. See goStyle.
func (env renderEnv) speller(sdkID string) (*goSpeller, error) {
	if env.goTypes == nil {
		return nil, fmt.Errorf("internal: no SDK module directory was configured")
	}
	svc, err := env.goTypes.service(sdkID)
	if err != nil {
		return nil, err
	}
	return &goSpeller{svc: svc}, nil
}

// javaSpeller resolves one service's modeled shapes for the Java rendering. The
// error is returned rather than fatal, for the same reason speller's is:
// `-explain` is a reader's tool and must still say something useful on a
// checkout whose shape snapshot does not cover the service.
func (env renderEnv) javaSpeller(service, sdkID string) (*javaSpeller, error) {
	model, err := env.shapes(service)
	if err != nil {
		return nil, err
	}
	return newJavaSpeller(model, sdkID), nil
}

// shapes resolves one service's pinned shapes for a rendering that spells its
// members from the model rather than from an SDK — Java, .NET and Rust all do.
// The error is returned rather than fatal for the same reason javaSpeller's is.
func (env renderEnv) shapes(service string) (*serviceModel, error) {
	if env.model == nil {
		return nil, fmt.Errorf("internal: no shape source was configured")
	}
	return env.model(service)
}

// dotnetSpeller resolves one service's modeled shapes, off the same shared
// source javaSpeller reads, and its AWSSDK package's slice of the .NET SDK type
// table, for the .NET rendering.
func (env renderEnv) dotnetSpeller(service, sdkID string) (*dotnetSpeller, error) {
	model, err := env.shapes(service)
	if err != nil {
		return nil, err
	}
	if env.dotnet == nil {
		return nil, fmt.Errorf("internal: no .NET SDK type table was configured")
	}
	table, err := env.dotnet()
	if err != nil {
		return nil, err
	}
	pkg, err := table.service(sdkID)
	if err != nil {
		return nil, err
	}
	return newDotnetSpeller(model, pkg, sdkID), nil
}

// repoShapes reads a service's pinned shape snapshot from the repository.
func repoShapes(root string) func(service string) (*serviceModel, error) {
	return func(service string) (*serviceModel, error) {
		return loadModel(filepath.Join(root, filepath.FromSlash(shapesDir)), modelServiceOf(root, service))
	}
}

// modelServiceOf is the shape-snapshot key for an Overcast capability key,
// read from the recipe's own `model` field. `-explain` reads the committed
// scenario file rather than the corpus, and the scenario deliberately carries no
// per-SDK or per-snapshot naming (compat/model/README.md § Naming), so the one
// place that mapping lives is the recipe. A service with no recipe — one only
// an authored scenario covers — resolves the way generation resolved it
// (snapshotServiceFor), and a recipe that cannot be decoded leaves the service
// its own name, which is right for every service that needs no mapping.
func modelServiceOf(root, service string) string {
	contents, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(recipesDir), service+".json"))
	if err != nil {
		if model, err := snapshotServiceFor(filepath.Join(root, filepath.FromSlash(shapesDir)), service); err == nil {
			return model
		}
		return service
	}
	// Decoded into the recipe itself, so the Model-or-service fallback is
	// recipe.modelService's and not a second copy of it. Loosely, not through
	// loadRecipe: this wants one field on a checkout whose schemas may not be
	// readable, and an `-explain` that refused because a recipe failed
	// validation would be no use to the reader who reached for it.
	r := recipe{Service: service}
	if err := json.Unmarshal(contents, &r); err != nil {
		return service
	}
	return r.modelService()
}

var renderers = map[string]renderer{
	"python": renderPython,
	"node":   renderNode,
	"cli":    renderCLI,
	"go":     renderGo,
	"java":   renderJava,
	"dotnet": renderDotnet,
	"rust":   renderRust,
}

func runExplain(opts options, stdout io.Writer) error {
	render, ok := renderers[opts.lang]
	if !ok {
		return fmt.Errorf("unknown -lang %q; want one of %s", opts.lang, strings.Join(rendererNames(), ", "))
	}
	groupName, testName, ok := strings.Cut(opts.explain, "/")
	if !ok || groupName == "" || testName == "" {
		return fmt.Errorf("-explain wants <group>/<test>, got %q", opts.explain)
	}
	file := scenarioFileOfGroup(groupName)
	s, err := readScenario(filepath.Join(opts.root, filepath.FromSlash(file)))
	if err != nil {
		return err
	}
	g, t, ok := s.findTest(groupName, testName)
	if !ok {
		return fmt.Errorf("no test %s in %s", opts.explain, file)
	}
	env := renderEnv{
		goTypes: newGoSDKTypes(filepath.Join(opts.root, filepath.FromSlash(goSDKModuleDir))),
		dotnet: func() (*dotnetSDKTypes, error) {
			return loadDotnetSDKTypes(filepath.Join(opts.root, filepath.FromSlash(dotnetSDKTypesDir)))
		},
		model: repoShapes(opts.root),
	}
	_, err = io.WriteString(stdout, render(env, s, g, t))
	return err
}

// scenarioFileOfGroup is the scenario file a group lives in, derived from its
// name alone.
//
// A generated group says so outright — `<service>-gen-<resource>` — and an
// authored one says so too, once you know the rule: an authored file's base
// name is the registry group it ports, which checkAuthoredAgainstRegistry
// enforces, so `sqs-queues-shadow` is compat/model/authored/sqs-queues.json.
// Every interpreter's failure message ends by naming the file and the step
// index and points the reader at `-explain`, so this has to answer for both
// layers or the advice is wrong for the layer most likely to be under review.
func scenarioFileOfGroup(groupName string) string {
	if service, _, ok := strings.Cut(groupName, "-gen-"); ok {
		return scenarioPath(service)
	}
	native, _ := nativeGroupOf(groupName)
	return authoredPath(native)
}

func rendererNames() []string {
	names := make([]string, 0, len(renderers))
	for name := range renderers {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func readScenario(path string) (*scenario, error) {
	contents, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read scenario %s: %w", path, err)
	}
	var s scenario
	if err := decodeStrict(contents, &s); err != nil {
		return nil, fmt.Errorf("parse scenario %s: %w", path, err)
	}
	return &s, nil
}

// ---------------------------------------------------------------------------
// Shared rendering of values and paths
// ---------------------------------------------------------------------------

// A style says how a language spells the pieces the IR is made of.
type style struct {
	comment string                  // line-comment prefix
	ref     func(ref string) string // a context lookup
	name    func(suffix string) string
	str     func(s string) string // a string literal
	concat  func(parts []string) string
	index   func(list string, i int) string
	// now is a `$now`: the clock read once for the call, plus the offset.
	now func(offsetMillis int64) string
	// blob is a `$base64`: the bytes its text spells, given that text already
	// rendered — a string literal, or the context lookup of an exported blob,
	// which every backend holds as its base64 text.
	blob     func(text string) string
	object   func(entries [][2]string) string // key already rendered as a literal
	list     func(items []string) string
	pathExpr func(root, path string) string // response path access
	// call renders an operation call; the top-level members are rendered
	// individually so builder-style SDKs can spell them as setters.
	call func(op string, members [][2]string) string
	// callLines renders a call as the statements a backend actually writes,
	// for a language whose call is more than one expression. It is set only
	// where an emitter exists, and it is the emitter's own renderer, so
	// `-explain` shows the source that backend compiles rather than a second
	// description of it. The last line is the call itself, which is what the
	// assignment prefix goes in front of.
	callLines func(op string, params map[string]any) []string
	tru, fls  string
}

func (st style) value(v any) string {
	if key, arg, ok := exprOf(v); ok {
		switch key {
		case "$lit":
			return st.value(arg)
		case "$ref":
			return st.ref(arg.(string))
		case "$name":
			return st.name(arg.(string))
		case "$concat":
			var parts []string
			for _, part := range arg.([]any) {
				if s, isString := part.(string); isString {
					parts = append(parts, st.str(s))
				} else {
					parts = append(parts, st.value(part))
				}
			}
			return st.concat(parts)
		case "$index":
			pair := arg.([]any)
			i, _ := integerOf(pair[1])
			return st.index(st.value(pair[0]), i)
		case "$now":
			if offset, err := nowOf(arg); err == nil && st.now != nil {
				return st.now(offset)
			}
		case "$base64":
			if st.blob != nil {
				return st.blob(st.value(arg))
			}
		}
	}
	switch value := v.(type) {
	case string:
		return st.str(value)
	case bool:
		if value {
			return st.tru
		}
		return st.fls
	case json.Number:
		return value.String()
	case float64:
		return fmt.Sprint(value)
	case nil:
		return "null"
	case []any:
		items := make([]string, 0, len(value))
		for _, item := range value {
			items = append(items, st.value(item))
		}
		return st.list(items)
	case map[string]any:
		var entries [][2]string
		for _, k := range sortedKeys(value) {
			entries = append(entries, [2]string{st.str(k), st.value(value[k])})
		}
		return st.object(entries)
	}
	return fmt.Sprint(v)
}

// members renders a params object member by member: [name, rendered value].
func (st style) members(params map[string]any) [][2]string {
	var out [][2]string
	for _, k := range sortedKeys(params) {
		out = append(out, [2]string{k, st.value(params[k])})
	}
	return out
}

// kv renders members as `k: v` pairs joined by sep.
func kv(members [][2]string, sep string, quoteKey bool) string {
	var parts []string
	for _, m := range members {
		key := m[0]
		if quoteKey {
			key = quote(key)
		}
		parts = append(parts, key+": "+m[1])
	}
	return strings.Join(parts, sep)
}

func quote(s string) string {
	contents, _ := json.Marshal(s)
	return string(contents)
}

// explainer assembles a test's steps as language-neutral events which each
// renderer formats: this keeps the seven renderers honest about the IR while
// letting them differ in syntax.
type explainer struct {
	st    style
	lines []string
}

func (e *explainer) linef(format string, args ...any) {
	e.lines = append(e.lines, fmt.Sprintf(format, args...))
}

func (e *explainer) commentf(format string, args ...any) {
	e.lines = append(e.lines, e.st.comment+" "+fmt.Sprintf(format, args...))
}

func (e *explainer) callLine(assign, op string, params map[string]any, export map[string]string) {
	if e.st.callLines != nil {
		lines := e.st.callLines(op, params)
		for _, line := range lines[:len(lines)-1] {
			e.linef("%s", line)
		}
		e.linef("%s%s", assign, lines[len(lines)-1])
	} else {
		e.linef("%s%s", assign, e.st.call(op, e.st.members(params)))
	}
	for _, ctx := range sortedStringKeys(export) {
		e.commentf("export %s = %s", ctx, export[ctx])
	}
}

func (e *explainer) checkLines(root string, checks map[string]check) {
	for _, path := range sortedCheckPaths(checks) {
		c := checks[path]
		access := e.st.pathExpr(root, path)
		switch {
		case c.NonEmpty:
			e.linef("assert %s is present and not empty", access)
		case c.IsList:
			e.linef("assert %s is absent or a list (an omitted or empty page satisfies this)", access)
		case c.Equals != nil:
			e.linef("assert %s == %s", access, e.st.value(c.Equals))
		case c.Matches != "":
			e.linef("assert %s matches %s", access, e.st.str(c.Matches))
		case c.Missing:
			e.linef("assert %s is absent", access)
		}
	}
}

func (e *explainer) whereText(where map[string]any) string {
	var parts []string
	for _, path := range sortedValueKeys(where) {
		parts = append(parts, fmt.Sprintf("item%s == %s", strings.TrimPrefix(path, "$"), e.st.value(where[path])))
	}
	return strings.Join(parts, " and ")
}

func (e *explainer) assertion(a assertion, index int) {
	switch a.Kind {
	case assertResponseField:
		e.commentf("assert[%d] responseField", index)
		e.checkLines("resp", a.Checks)
	case assertReadback:
		e.commentf("assert[%d] readback", index)
		e.callLine("back = ", a.Call.Op, a.Call.Params, a.Call.Export)
		e.checkLines("back", a.Checks)
	case assertListContains:
		e.commentf("assert[%d] listContains", index)
		root := "resp"
		if a.Call != nil {
			e.callLine("listed = ", a.Call.Op, a.Call.Params, a.Call.Export)
			root = "listed"
		}
		e.linef("assert any(%s for item in %s)  %s non-empty, and contains the resource", e.whereText(a.Where), e.st.pathExpr(root, a.ItemsPath), e.st.comment)
	case assertAbsent:
		e.commentf("assert[%d] absent", index)
		if a.Error != nil {
			e.commentf("the call below must fail with %s (or %s)", a.Error.Code, a.Error.Shape)
			e.callLine("", a.Call.Op, a.Call.Params, a.Call.Export)
			return
		}
		root := "resp"
		if a.Call != nil {
			e.callLine("listed = ", a.Call.Op, a.Call.Params, a.Call.Export)
			root = "listed"
		}
		e.linef("assert not any(%s for item in %s)  %s a missing list counts as empty", e.whereText(a.Where), e.st.pathExpr(root, a.ItemsPath), e.st.comment)
	case assertErrorCode:
		e.commentf("assert[%d] errorCode: the call above must fail with %s (or %s)", index, a.Error.Code, a.Error.Shape)
	case assertEventually:
		e.commentf("assert[%d] eventually: retry the clause below up to %d times, %d ms apart, until it passes", index, a.MaxAttempts, a.DelayMs)
		e.assertion(*a.Assert, index)
	}
}

func (e *explainer) test(s *scenario, g *group, t *test, header func()) string {
	e.commentf("%s/%s — op %s (scenario %s)", g.Name, t.Name, t.Op, scenarioFileOfGroup(g.Name))
	if len(t.Depends) > 0 {
		e.commentf("depends on %s", strings.Join(t.Depends, ", "))
	}
	header()
	expectsError := false
	for _, a := range t.Assert {
		if a.Kind == assertErrorCode {
			expectsError = true
		}
	}
	if expectsError {
		e.commentf("the primary call is expected to fail; catch the error and check its code")
	}
	e.callLine("resp = ", t.Call.Op, t.Call.Params, t.Call.Export)
	for i, a := range t.Assert {
		e.assertion(a, i)
	}
	return strings.Join(e.lines, "\n") + "\n"
}

// ---------------------------------------------------------------------------
// Naming helpers shared by every backend
// ---------------------------------------------------------------------------

// snake turns PascalCase into snake_case: SendMessageBatch → send_message_batch.
func snake(name string) string {
	var out strings.Builder
	runes := []rune(name)
	for i, r := range runes {
		upper := r >= 'A' && r <= 'Z'
		if upper && i > 0 {
			prevUpper := runes[i-1] >= 'A' && runes[i-1] <= 'Z'
			nextLower := i+1 < len(runes) && runes[i+1] >= 'a' && runes[i+1] <= 'z'
			if !prevUpper || nextLower {
				out.WriteByte('_')
			}
		}
		if upper {
			r += 'a' - 'A'
		}
		out.WriteRune(r)
	}
	return out.String()
}

// kebab turns PascalCase or an SDK ID into kebab-case: "Cost Explorer" →
// cost-explorer, SendMessageBatch → send-message-batch.
func kebab(name string) string {
	return strings.ReplaceAll(snake(strings.ReplaceAll(name, " ", "")), "_", "-")
}

// pascalSDK renders an SDK ID as a class prefix: "SQS" → SQS, "Cost
// Explorer" → CostExplorer.
func pascalSDK(sdkID string) string { return strings.ReplaceAll(sdkID, " ", "") }

func lowerFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToLower(s[:1]) + s[1:]
}
