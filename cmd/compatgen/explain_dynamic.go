//go:build dev

package main

import (
	"fmt"
	"strings"
)

// Renderings for the backends that pass parameters as data — boto3 and the
// JS SDK take a map, the CLI takes the params JSON itself — so a call reads
// as the params document with a client method in front of it. The typed SDKs
// are in explain_typed.go; the shared machinery is in explain.go.

func pyStyle() style {
	return style{
		comment: "#",
		ref:     func(ref string) string { return fmt.Sprintf("ctx[%s]", quote(ref)) },
		name:    func(suffix string) string { return fmt.Sprintf("f\"{run_id}-{group}-%s\"", suffix) },
		str:     quote,
		concat:  func(parts []string) string { return strings.Join(parts, " + ") },
		index:   func(list string, i int) string { return fmt.Sprintf("%s[%d]", list, i) },
		now:     func(offset int64) string { return nowPlus("now_ms", offset) },
		object: func(entries [][2]string) string {
			var parts []string
			for _, e := range entries {
				parts = append(parts, e[0]+": "+e[1])
			}
			return "{" + strings.Join(parts, ", ") + "}"
		},
		list:     func(items []string) string { return "[" + strings.Join(items, ", ") + "]" },
		pathExpr: func(root, path string) string { return root + pathAsSubscripts(path) },
		call: func(op string, members [][2]string) string {
			var args []string
			for _, m := range members {
				args = append(args, m[0]+"="+m[1])
			}
			return fmt.Sprintf("client.%s(%s)", snake(op), strings.Join(args, ", "))
		},
		tru: "True", fls: "False",
	}
}

func renderPython(_ renderEnv, s *scenario, g *group, t *test) string {
	e := &explainer{st: pyStyle()}
	return e.test(s, g, t, func() {
		e.linef("client = boto3.client(%s, endpoint_url=endpoint)  # sdkId %s, protocol %s", quote(cliService(s.Client)), s.Client.SDKID, s.Client.Protocol)
		e.linef("group = %s", quote(g.Name))
	})
}

func jsStyle() style {
	st := pyStyle()
	st.comment = "//"
	st.name = func(suffix string) string { return fmt.Sprintf("`${runId}-${group}-%s`", suffix) }
	st.now = func(offset int64) string { return nowPlus("nowMs", offset) }
	st.object = func(entries [][2]string) string {
		var parts []string
		for _, e := range entries {
			parts = append(parts, e[0]+": "+e[1])
		}
		return "{ " + strings.Join(parts, ", ") + " }"
	}
	st.pathExpr = func(root, path string) string { return root + pathAsJS(path) }
	st.call = func(op string, members [][2]string) string {
		return fmt.Sprintf("await client.send(new %sCommand({ %s }))", op, kv(members, ", ", false))
	}
	st.tru, st.fls = "true", "false"
	return st
}

func renderNode(_ renderEnv, s *scenario, g *group, t *test) string {
	e := &explainer{st: jsStyle()}
	return e.test(s, g, t, func() {
		e.linef("const client = new %sClient({ endpoint });  // @aws-sdk/client-%s", pascalSDK(s.Client.SDKID), kebab(s.Client.SDKID))
		e.linef("const group = %s;", quote(g.Name))
	})
}

func cliStyle(client clientInfo) style {
	st := pyStyle()
	st.name = func(suffix string) string { return fmt.Sprintf("\"$RUN_ID-$GROUP-%s\"", suffix) }
	st.ref = func(ref string) string { return "$" + strings.ToUpper(strings.NewReplacer(".", "_").Replace(ref)) }
	st.now = func(offset int64) string { return "$((" + nowPlus("NOW_MS", offset) + "))" }
	st.pathExpr = func(root, path string) string {
		return fmt.Sprintf("jq '%s' <<< \"$%s\"", strings.TrimPrefix(path, "$"), root)
	}
	st.call = func(op string, members [][2]string) string {
		return fmt.Sprintf("$(aws %s %s --cli-input-json '{%s}')", cliService(client), kebab(op), kv(members, ", ", true))
	}
	st.tru, st.fls = "true", "false"
	return st
}

func renderCLI(_ renderEnv, s *scenario, g *group, t *test) string {
	e := &explainer{st: cliStyle(s.Client)}
	return e.test(s, g, t, func() {
		e.linef("export AWS_ENDPOINT_URL=$ENDPOINT  # service command %q from endpointPrefix %q", cliService(s.Client), s.Client.EndpointPrefix)
		e.linef("GROUP=%s", quote(g.Name))
	})
}

// nowPlus spells a `$now` for pseudo-code: the variable holding the clock,
// read once for the call in epoch milliseconds, and the offset added to it.
func nowPlus(variable string, offset int64) string {
	switch {
	case offset > 0:
		return fmt.Sprintf("%s + %d", variable, offset)
	case offset < 0:
		return fmt.Sprintf("%s - %d", variable, -offset)
	}
	return variable
}

// ---------------------------------------------------------------------------
// Path rendering for the data-shaped backends
// ---------------------------------------------------------------------------

// pathAsSubscripts renders $.A.B[0] as ["A"]["B"][0].
func pathAsSubscripts(path string) string {
	var out strings.Builder
	for _, segment := range mustPath(path).segments {
		if segment.index >= 0 {
			fmt.Fprintf(&out, "[%d]", segment.index)
		} else {
			fmt.Fprintf(&out, "[%s]", quote(segment.name))
		}
	}
	return out.String()
}

// pathAsJS renders $.A.B[0] as .A.B[0].
func pathAsJS(path string) string {
	return strings.TrimPrefix(path, "$")
}

// cliService is the aws CLI command name: botocore's service name, which is
// the endpoint prefix for every service in the pilot. The README flags the
// services where that derivation is known to differ.
func cliService(c clientInfo) string {
	if c.EndpointPrefix != "" {
		return c.EndpointPrefix
	}
	return strings.ToLower(c.SDKID)
}
