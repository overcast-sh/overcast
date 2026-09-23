//go:build dev

package main

import (
	"fmt"
	"strings"
)

// Renderings for the backends whose SDKs are typed: a call is a request
// struct or builder, and a response is read through fields or getters rather
// than by subscripting a map. The data-shaped backends are in
// explain_dynamic.go; the shared machinery is in explain.go.

// typedStyle is what the four typed backends share: a request object built
// from named members, and a response read through fields or getters. Each of
// them overrides the call and the object/list syntax; go additionally renders
// through its emitter (goStyle below).
func typedStyle() style {
	st := jsStyle()
	st.name = func(suffix string) string { return fmt.Sprintf("runID + \"-\" + group + \"-%s\"", suffix) }
	st.object = func(entries [][2]string) string {
		var parts []string
		for _, e := range entries {
			parts = append(parts, e[0]+": "+e[1])
		}
		return "map[string]any{" + strings.Join(parts, ", ") + "}"
	}
	st.list = func(items []string) string { return "[]T{" + strings.Join(items, ", ") + "}" }
	st.pathExpr = func(root, path string) string { return root + pathAsGo(path) }
	st.call = func(op string, members [][2]string) string {
		return fmt.Sprintf("client.%s(ctx, &%sInput{%s})", op, op, kv(members, ", ", false))
	}
	return st
}

// goStyle renders a call through the emitter's own spelling table
// (goInputLines, over emit_go_spell.go), so `-explain -lang go` prints the
// statements cmd/compatgen writes into
// compat/suites/go-sdk/internal/groups/scenarios_*_gen.go rather than a second
// description of them. The definition of done for the Go backend asks for one
// naming table; this is how there comes to be only one — and now that the
// spelling is typed, it is also what keeps the explanation honest about which
// members the vendored SDK made pointers.
//
// loadErr is non-nil when the SDK's own types could not be read. That must
// never happen to generation, but it can happen to `-explain`: the loader
// needs the go-sdk suite module's dependencies, and a reader may be on a
// checkout that has never fetched them. Saying so beats printing a spelling
// that would be a guess.
func goStyle(sp *goSpeller, loadErr error) style {
	st := typedStyle()
	st.callLines = func(op string, params map[string]any) []string {
		if loadErr != nil {
			return []string{fmt.Sprintf("// the vendored SDK's field types could not be read: %v", loadErr)}
		}
		lines, err := goInputLines(sp, op, params, "")
		if err != nil {
			// Unreachable for a committed scenario: the emitter refuses at
			// generation time what it cannot render, so a value this cannot
			// spell never reaches a scenario file.
			return []string{fmt.Sprintf("// %v", err)}
		}
		return append(lines, fmt.Sprintf("g.cl().%s(ctx, in)", op))
	}
	return st
}

func renderGo(env renderEnv, s *scenario, g *group, t *test) string {
	sp, loadErr := env.speller(s.Client.SDKID)
	e := &explainer{st: goStyle(sp, loadErr)}
	return e.test(s, g, t, func() {
		e.linef("client := %s.NewFromConfig(cfg)  // %s", goNamePackage(s.Client.SDKID), goNameModule(s.Client.SDKID))
		e.linef("group := %s", quote(g.Name))
		e.commentf("b is the scenario.Binder the generated Build closure receives")
	})
}

// javaStyle renders a call through the emitter's own spelling table
// (javaRequestLines, over emit_java_spell.go), so `-explain -lang java` prints
// the statements cmd/compatgen writes into
// compat/suites/java-sdk/.../Scenarios*Gen.java rather than a second
// description of them. The definition of done for a typed backend asks for one
// naming table; this is how there comes to be only one.
//
// sp is nil when the scenario's own model could not be read, which must never
// happen to generation but can happen to `-explain` on a checkout with no shape
// snapshot for the service. Saying so beats printing a spelling that would be a
// guess.
func javaStyle(sp *javaSpeller, loadErr error) style {
	st := typedStyle()
	st.name = func(suffix string) string { return fmt.Sprintf("runId + \"-\" + group + \"-%s\"", suffix) }
	st.object = func(entries [][2]string) string {
		var parts []string
		for _, e := range entries {
			parts = append(parts, e[0]+", "+e[1])
		}
		return "Map.of(" + strings.Join(parts, ", ") + ")"
	}
	st.list = func(items []string) string { return "List.of(" + strings.Join(items, ", ") + ")" }
	st.pathExpr = func(root, path string) string { return root + pathAsGetters(path, "()") }
	// No st.call override: callLines supersedes it for every call this style
	// renders, and a second spelling of one would be the drift this backend's
	// one-naming-table rule exists to prevent.
	st.callLines = func(op string, params map[string]any) []string {
		if loadErr != nil {
			return []string{fmt.Sprintf("// the service's shape snapshot could not be read: %v", loadErr)}
		}
		lines, err := javaRequestLines(sp, op, params)
		if err != nil {
			// Unreachable for a committed scenario: the emitter refuses at
			// generation time what it cannot render, so a value this cannot
			// spell never reaches a scenario file.
			return []string{fmt.Sprintf("// %v", err)}
		}
		out := []string{javaNameRequest(op) + " request = " + lines[0]}
		for i, line := range lines[1:] {
			end := ""
			if i == len(lines)-2 {
				end = ";"
			}
			out = append(out, "        "+line+end)
		}
		return append(out, fmt.Sprintf("client.%s(request)", javaMethod(op)))
	}
	return st
}

func renderJava(env renderEnv, s *scenario, g *group, t *test) string {
	sp, loadErr := env.javaSpeller(s.Service, s.Client.SDKID)
	e := &explainer{st: javaStyle(sp, loadErr)}
	return e.test(s, g, t, func() {
		client := javaNameClientClass(s.Client.SDKID)
		e.linef("%s client = %s.builder().endpointOverride(endpoint).build();", client, client)
		e.linef("String group = %s;", quote(g.Name))
	})
}

// dotnetStyle renders a call through the emitter's own spelling table
// (dotnetInputLines, over emit_dotnet.go's dotnetSpeller), so
// `-explain -lang dotnet` prints the statements cmd/compatgen writes into
// compat/suites/dotnet-sdk/Groups/Scenarios*Gen.cs rather than a second
// description of them. The definition of done for a typed backend asks for one
// naming table; this is how there comes to be only one.
//
// loadErr is non-nil when the service's shape snapshot or its slice of the .NET
// SDK type table could not be read. That must never happen to generation,
// which has already loaded both, but it can happen to `-explain` on a partial
// checkout. Saying so beats printing a spelling that would be a guess.
func dotnetStyle(sp *dotnetSpeller, loadErr error) style {
	st := typedStyle()
	st.object = func(entries [][2]string) string {
		var parts []string
		for _, e := range entries {
			parts = append(parts, "{ "+e[0]+", "+e[1]+" }")
		}
		return "new Dictionary<string, T> { " + strings.Join(parts, ", ") + " }"
	}
	st.list = func(items []string) string { return "new List<T> { " + strings.Join(items, ", ") + " }" }
	st.pathExpr = func(root, path string) string { return root + pathAsGetters(path, "") }
	// No st.call: callLines below is set unconditionally, and the explainer
	// prefers it, so a second spelling of the same call would only ever be dead.
	st.callLines = func(op string, params map[string]any) []string {
		if loadErr != nil {
			return []string{fmt.Sprintf("// the service's shape snapshot or .NET SDK type table could not be read: %v", loadErr)}
		}
		lines, err := dotnetInputLines(sp, op, params, "")
		if err != nil {
			// Unreachable for a committed scenario: the emitter refuses at
			// generation time what it cannot render, so a value this cannot
			// spell never reaches a scenario file.
			return []string{fmt.Sprintf("// %v", err)}
		}
		return append(lines, fmt.Sprintf("await Cl().%sAsync(request)", op))
	}
	return st
}

func renderDotnet(env renderEnv, s *scenario, g *group, t *test) string {
	sp, loadErr := env.dotnetSpeller(s.Service, s.Client.SDKID)
	e := &explainer{st: dotnetStyle(sp, loadErr)}
	return e.test(s, g, t, func() {
		e.linef("var client = new %s(new %s { ServiceURL = endpoint });",
			dotnetNameClientClass(s.Client.SDKID), dotnetNameConfigClass(s.Client.SDKID))
		e.linef("var group = %s;", quote(g.Name))
		e.commentf("b is the Binder the generated Build lambda receives")
	})
}

// rustStyle renders a call through the emitter's own spelling table
// (rustCallLines), so `-explain -lang rust` prints the statements cmd/compatgen
// writes into compat/suites/rust-sdk/src/groups/scenarios_*_gen.rs rather than a
// second description of them. It is the same one-naming-table rule goStyle
// follows, and the same reason: an explanation that describes the call rather
// than reproducing it drifts from it silently.
//
// A nil model is a snapshot that could not be read, and modelErr says why. That
// must never happen to generation, but it can happen to `-explain`, which is a
// reader's tool run on any checkout. Saying so beats printing a spelling that
// would be a guess.
func rustStyle(model *serviceModel, crate string, modelErr error) style {
	st := typedStyle()
	st.name = func(suffix string) string { return fmt.Sprintf("format!(\"{run_id}-{group}-%s\")", suffix) }
	st.object = func(entries [][2]string) string {
		var parts []string
		for _, e := range entries {
			parts = append(parts, "("+e[0]+", "+e[1]+")")
		}
		return "HashMap::from([" + strings.Join(parts, ", ") + "])"
	}
	st.list = func(items []string) string { return "vec![" + strings.Join(items, ", ") + "]" }
	st.pathExpr = func(root, path string) string { return root + pathAsRustAccessors(path) }
	st.call = func(op string, members [][2]string) string {
		var setters []string
		for _, m := range members {
			setters = append(setters, "."+rustNameMember(m[0])+"("+m[1]+")")
		}
		return fmt.Sprintf("client.%s()%s.send().await?", rustNameOperation(op), strings.Join(setters, ""))
	}
	st.callLines = func(op string, params map[string]any) []string {
		if model == nil {
			return []string{fmt.Sprintf("// the shape snapshot could not be read: %v", modelErr)}
		}
		lines, _, err := rustCallLines(model, crate, op, params)
		if err != nil {
			// Unreachable for a committed scenario: the emitter refuses at
			// generation time what it cannot render, so a value this cannot
			// spell never reaches a scenario file.
			return []string{fmt.Sprintf("// %v", err)}
		}
		return lines
	}
	return st
}

func renderRust(env renderEnv, s *scenario, g *group, t *test) string {
	crate := rustNameCrate(s.Client.SDKID)
	model, loadErr := env.shapes(s.Service)
	e := &explainer{st: rustStyle(model, crate, loadErr)}
	return e.test(s, g, t, func() {
		e.linef("let client = %s::Client::new(&config);  // endpoint_url set on the config", crate)
		e.linef("let group = %s;", quote(g.Name))
		e.commentf("b is the scenario::Binder the generated invoke closure receives")
		e.commentf("capture is the interceptor that keeps the raw response body")
	})
}

// ---------------------------------------------------------------------------
// Path rendering for the typed backends
// ---------------------------------------------------------------------------

// pathAsGo renders $.A.B[0] as .A.B[0].
func pathAsGo(path string) string {
	var out strings.Builder
	for _, segment := range mustPath(path).segments {
		if segment.index >= 0 {
			fmt.Fprintf(&out, "[%d]", segment.index)
		} else {
			fmt.Fprintf(&out, ".%s", segment.name)
		}
	}
	return out.String()
}

// pathAsRustAccessors renders $.A.B[0] as .a().b()[0] — snake_case, because
// that is what the SDK for Rust names a member's accessor, and a bare index,
// because a list member answers with a slice.
//
// It is a rendering of the *modeled* path for a reader, not of what the runtime
// does: crate::scenario walks the response body as a document, by the modeled
// names the scenario file writes (see compat/suites/rust-sdk/src/scenario).
func pathAsRustAccessors(path string) string {
	var out strings.Builder
	for _, segment := range mustPath(path).segments {
		if segment.index >= 0 {
			fmt.Fprintf(&out, "[%d]", segment.index)
		} else {
			fmt.Fprintf(&out, ".%s()", rustNameMember(segment.name))
		}
	}
	return out.String()
}

// pathAsGetters renders $.A.B[0] as .a().b().get(0) (Java) or .A.B[0]
// (.NET, with call="").
func pathAsGetters(path string, call string) string {
	var out strings.Builder
	for _, segment := range mustPath(path).segments {
		switch {
		case segment.index >= 0 && call != "":
			fmt.Fprintf(&out, ".get(%d)", segment.index)
		case segment.index >= 0:
			fmt.Fprintf(&out, "[%d]", segment.index)
		case call != "":
			fmt.Fprintf(&out, ".%s%s", lowerFirst(segment.name), call)
		default:
			fmt.Fprintf(&out, ".%s", segment.name)
		}
	}
	return out.String()
}
