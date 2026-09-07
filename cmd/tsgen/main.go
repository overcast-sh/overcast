// Command tsgen renders the Go response structs the web UI consumes into
// TypeScript, so the types the console is written against are generated from
// the structs the server marshals instead of being mirrored by hand.
//
// The alternative — a hand-written copy in web/src/types — drifts silently: a
// Go field rename does not fail tsc, it ships a UI reading `undefined`. With
// this tool the Go side is the only source of truth, and the CI gate
// (`make check-ts`, part of `make docs-check`) fails the build when the
// checked-in web/src/types/api.gen.ts no longer matches the Go source.
//
// Usage:
//
//	go run ./cmd/tsgen --write       # regenerate web/src/types/api.gen.ts
//	go run ./cmd/tsgen --check       # exit 1 with a "run make generate-ts" message on drift
//	go run ./cmd/tsgen --workspace . # workspace root (default: the directory containing go.mod)
//
// What gets generated is exactly the manifest below — the structs the web UI
// actually reads — never "every type with a json tag". Adding a type is one
// manifest line; the renderer refuses (with the field named) to reference a
// type the manifest does not list, so the set cannot grow by accident.
//
// Rendering rules, which together define the TypeScript contract:
//
//   - A struct becomes `export interface`; its fields are named by their `json`
//     tag (falling back to the Go name, as encoding/json does), in source order.
//     Unexported fields and `json:"-"` fields are skipped.
//   - `omitempty` or `omitzero` makes the field optional (`name?:`). A pointer
//     field WITHOUT either renders as `T | null`, because that is what a nil
//     pointer marshals to. Nil slices and maps also marshal as null, but the
//     generator deliberately renders them as plain arrays/records: every
//     handler in the manifest sends empty collections rather than nil (several
//     say so in their comments), and `| null` on every list would cost every
//     consumer a guard for a value the server never sends.
//   - Numbers (all int/uint/float widths) → `number`; `string` → `string`;
//     `bool` → `boolean`; `time.Time` → `string` (RFC 3339); `[]byte` →
//     `string` (base64); `json.RawMessage`, `any` → `unknown`; `[]T` → `T[]`;
//     `map[string]V` → `Record<string, V>`; a `json:",string"` option → `string`.
//   - A type whose underlying type is `string` (alias or defined) becomes a
//     union of the string constants declared with that type in the same
//     package, in declaration order — `type EmulationTier = string` plus
//     `TierFull EmulationTier = "full"` renders as `"full" | …`. With no such
//     constants it is `string`. An empty-string constant (the type's zero
//     value, which omitempty elides) is not a union member.
//   - `net/http.Header` → `Record<string, string[]>`.
//   - Go doc comments on the type and on each field (including a trailing
//     line comment) are carried over verbatim as JSDoc, so the reasoning that
//     lives on the Go side is visible from the editor on the TypeScript side.
//
// Embedded fields, anonymous struct fields, generics and non-string map keys
// are rejected rather than guessed at — none of the consumed structs use them,
// and an error names the field so the rule can be added deliberately when one
// does. A type with its own MarshalJSON is rejected too: its wire shape is
// whatever that method writes, so the manifest must name the package-level
// struct the method encodes (trace.Entry → trace.entryJSON is the pattern).
//
// Files are read through go/build pinned to linux/amd64 (the platform the
// shipped binary runs on), so the output does not depend on the host that
// regenerated it.
package main

import (
	"bytes"
	"flag"
	"fmt"
	"go/ast"
	"go/build"
	"go/parser"
	"go/token"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
)

// outputPath is where the rendered TypeScript lives, relative to the workspace
// root. web/src/types/common.ts re-exports from it.
const outputPath = "web/src/types/api.gen.ts"

// printWidth mirrors web/prettier.config.mjs so a union that fits on one line
// is emitted the way prettier would format it, and one that does not is broken
// one member per line — the committed file is expected to pass
// `prettier --check` untouched.
const printWidth = 100

// target is one Go type the web UI consumes and the TypeScript name it is
// rendered under. Unexported Go names are fine: the tool reads source, not
// reflection, and several wire structs are unexported handler-local types.
type target struct {
	pkg    string // package directory, slash-separated, relative to the workspace root
	goName string // Go type name
	tsName string // exported TypeScript name
}

// manifest lists exactly the types the web UI reads, grouped by the endpoint
// that serves them. Order here is output order — keep related types together.
var manifest = []target{
	// GET /_overcast/events — one SSE frame (internal/router/events.go).
	{"internal/router", "sseEnvelope", "StreamEvent"},

	// GET /_overcast/metrics — runtime stats + startup timeline.
	{"internal/router", "metricsSnapshot", "MetricsSnapshot"},
	{"internal/router", "StartupPhase", "StartupPhase"},

	// GET /_overcast/health.
	{"internal/router", "healthResponse", "HealthResponse"},
	{"internal/router", "healthStorage", "HealthStorage"},
	{"internal/router", "persistentHealth", "PersistentHealth"},
	{"internal/router", "EmulationTier", "EmulationTier"},
	{"internal/docker", "Status", "DockerHealth"},
	{"internal/docker", "ServiceHealth", "DockerServiceHealth"},
	{"internal/docker", "NetworkStatus", "DockerNetworkStatus"},
	{"internal/docker", "NetworkFieldDiff", "DockerNetworkFieldDiff"},
	{"internal/listenstatus", "Status", "ListenerStatus"},
	{"internal/listenstatus", "State", "ListenerState"},

	// GET /_overcast/debug/metrics — storage diagnostics + advisories.
	{"internal/router", "debugMetricsResponse", "DebugMetricsResponse"},
	{"internal/state", "DebugMetrics", "DebugMetrics"},
	{"internal/state", "DebugFlushRecord", "DebugFlushRecord"},
	{"internal/state", "StoreCounters", "StoreCounters"},
	{"internal/state", "DataDirProbeResult", "DataDirProbeResult"},
	{"internal/router", "Advisory", "Advisory"},
	{"internal/router", "AdvisorySeverity", "AdvisorySeverity"},

	// CloudFormation stack diagnostics — the provenance tier each evidence
	// section carries (web/src/services/api/cloudformation.ts).
	{"internal/services/cloudformation", "DiagnosticProvenance", "DiagnosticProvenance"},

	// GET /_overcast/topology — the Map page's graph.
	{"internal/router", "topologyResponse", "TopologyResponse"},
	{"internal/router", "topologyNode", "TopologyNode"},
	{"internal/router", "topologyEdge", "TopologyEdge"},
	{"internal/router", "topologyECSResourceType", "TopologyECSResourceType"},

	// GET /_overcast/ses/inbox/messages — the Inbox page.
	{"internal/smtp", "CapturedMessage", "CapturedMessage"},
	{"internal/smtp", "MessageKind", "MessageKind"},

	// GET /_overcast/debug/trace{,s}{,/count,/search} — the request-tracing UI.
	// Entry and Hop marshal through package-level wire structs (their bodies
	// are []byte in memory and strings on the wire), so the manifest names
	// those, not the in-memory types.
	{"internal/trace", "entryJSON", "TraceEntry"},
	{"internal/trace", "hopJSON", "TraceHop"},
	{"internal/trace", "LogEntry", "TraceLogEntry"},
	{"internal/trace", "OmitReason", "TraceOmitReason"},
	{"internal/trace", "Summary", "TraceSummary"},
	{"internal/router", "debugTraceListResponse", "TraceListResponse"},
	{"internal/trace", "RetentionStats", "TraceCountResponse"},
	{"internal/trace", "DroppedCounts", "TraceDroppedCounts"},
	{"internal/trace", "Match", "TraceMatch"},
	{"internal/trace", "MatchField", "TraceMatchField"},
	{"internal/trace", "DeepResult", "TraceSearchResponse"},

	// GET /_overcast/lambda/functions/{name}/metrics and
	// GET /_overcast/sqs/queues/{name}/metrics — the web Monitor tab's
	// read-through into the shared service-metrics repository
	// (docs/plans/service-metrics-platform.md phase 3). Shared by every
	// service's Monitor endpoint (internal/metrics/monitor.go).
	{"internal/metrics", "MonitorResponse", "MonitorResponse"},
	{"internal/metrics", "MonitorSeries", "MonitorSeries"},
	{"internal/metrics", "ChartPoint", "ChartPoint"},

	// GET /_overcast/debugger/targets and
	// GET /_overcast/debugger/targets/{service}/{resource} — the compute
	// debugger's target list and one target's descriptor, which the Debug tab
	// renders (docs/plans/compute-debugger.md § 6).
	{"internal/debugger", "TargetList", "DebuggerTargetList"},
	{"internal/debugger", "Descriptor", "DebuggerTarget"},
	{"internal/debugger", "Listen", "DebuggerListen"},
	{"internal/debugger", "Setup", "DebuggerSetup"},
	{"internal/debugger", "Editor", "DebuggerEditor"},

	// GET /api/docs/nav — the console's docs sidebar and "On this page" list.
	// The console used to import this data as a generated TypeScript module
	// committed to the repository; it fetches it now (internal/docsindex).
	{"internal/docsindex", "Entry", "DocsNavEntry"},
	{"internal/docsindex", "Heading", "DocsHeading"},
}

// basicTS maps Go predeclared types to TypeScript.
var basicTS = map[string]string{
	"string": "string",
	"bool":   "boolean",
	"int":    "number", "int8": "number", "int16": "number", "int32": "number", "int64": "number",
	"uint": "number", "uint8": "number", "uint16": "number", "uint32": "number", "uint64": "number",
	"uintptr": "number", "float32": "number", "float64": "number",
	"byte": "number", "rune": "number",
	"any": "unknown",
}

// externalTS maps types from outside the module that the consumed structs use,
// keyed by "<import path>.<name>", to the TypeScript they marshal as.
var externalTS = map[string]string{
	"time.Time":                "string",                   // RFC 3339
	"time.Duration":            "number",                   // nanoseconds
	"encoding/json.RawMessage": "unknown",                  // opaque JSON
	"net/http.Header":          "Record<string, string[]>", // map[string][]string
}

func main() {
	var (
		workspace = flag.String("workspace", ".", "workspace root (directory with go.mod)")
		write     = flag.Bool("write", false, "write "+outputPath)
		check     = flag.Bool("check", false, "exit 1 if "+outputPath+" is not what --write would produce")
	)
	flag.Parse()

	if !*write && !*check {
		flag.Usage()
		fmt.Fprintln(os.Stderr, "\ntsgen: no action specified; use --write or --check")
		os.Exit(1)
	}

	root, err := findWorkspaceRoot(*workspace)
	if err != nil {
		fatalf("workspace root: %v", err)
	}

	got, err := render(root, manifest)
	if err != nil {
		fatalf("%v", err)
	}

	out := filepath.Join(root, filepath.FromSlash(outputPath))
	if *check {
		want, readErr := os.ReadFile(out)
		if readErr != nil {
			fatalf("%s: %v; run: make generate-ts", outputPath, readErr)
		}
		if !bytes.Equal(want, got) {
			fmt.Fprintf(os.Stderr, "tsgen: %s is stale (%s); run: make generate-ts\n", outputPath, firstDifference(want, got))
			os.Exit(1)
		}
		fmt.Printf("tsgen: %s is up to date\n", outputPath)
	}
	if *write {
		if err := os.WriteFile(out, got, 0o644); err != nil {
			fatalf("writing %s: %v", outputPath, err)
		}
		fmt.Printf("tsgen: wrote %s (%d types)\n", outputPath, len(manifest))
	}
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "tsgen: "+format+"\n", args...)
	os.Exit(1)
}

// firstDifference describes where the committed file and the freshly rendered
// one diverge, so a CI log says which type moved without anyone having to
// regenerate locally to find out.
func firstDifference(committed, rendered []byte) string {
	a := strings.Split(string(committed), "\n")
	b := strings.Split(string(rendered), "\n")
	for i := 0; i < len(a) || i < len(b); i++ {
		var la, lb string
		if i < len(a) {
			la = a[i]
		}
		if i < len(b) {
			lb = b[i]
		}
		if la != lb {
			return fmt.Sprintf("first difference at line %d: committed %q, Go source now gives %q", i+1, la, lb)
		}
	}
	return "files differ"
}

func findWorkspaceRoot(start string) (string, error) {
	abs, err := filepath.Abs(start)
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(abs, "go.mod")); err == nil {
			return abs, nil
		}
		parent := filepath.Dir(abs)
		if parent == abs {
			return "", fmt.Errorf("go.mod not found in %s or any parent", start)
		}
		abs = parent
	}
}

// modulePath reads the module directive from the workspace's go.mod, which is
// what turns an import path in a source file back into a package directory.
func modulePath(root string) (string, error) {
	data, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(data), "\n") {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(line), "module "); ok {
			return strings.TrimSpace(rest), nil
		}
	}
	return "", fmt.Errorf("%s: no module directive", filepath.Join(root, "go.mod"))
}

// ---- Source model -----------------------------------------------------------

// fileInfo is what a type declaration needs from the file it lives in: the
// import table, to resolve `pkg.Name` references in field types.
type fileInfo struct {
	pkg     string            // package directory (slash-separated, root-relative)
	name    string            // root-relative path, for "generated from" notes
	imports map[string]string // local name → import path
}

// typeDecl is one `type X …` declaration with its documentation.
type typeDecl struct {
	spec *ast.TypeSpec
	doc  *ast.CommentGroup
	file *fileInfo
}

// stringConst is a string constant declared with an explicit named type —
// `TierFull EmulationTier = "full"` — which is how a string-typed alias gets
// its union members.
type stringConst struct {
	name  string
	value string
}

type pkgInfo struct {
	types  map[string]*typeDecl
	consts map[string][]stringConst // type name → its string constants, in declaration order
	// marshalers is the set of type names with a MarshalJSON method. Such a
	// type's wire shape is whatever that method writes, not its struct, so
	// rendering the struct would be silently wrong — the manifest has to name
	// the struct the method encodes instead.
	marshalers map[string]bool
}

type generator struct {
	root    string
	module  string
	byName  map[string]target // "<pkg>.<goName>" → target, for reference resolution
	pkgs    map[string]*pkgInfo
	context build.Context
}

// render produces the full contents of api.gen.ts for the given manifest.
func render(root string, targets []target) ([]byte, error) {
	module, err := modulePath(root)
	if err != nil {
		return nil, err
	}
	g := &generator{
		root:    root,
		module:  module,
		byName:  make(map[string]target, len(targets)),
		pkgs:    map[string]*pkgInfo{},
		context: build.Default,
	}
	// Pin the platform so the output is the same whichever host regenerates
	// it; linux/amd64 is what the shipped binary runs as.
	g.context.GOOS = "linux"
	g.context.GOARCH = "amd64"
	g.context.CgoEnabled = false

	seen := map[string]bool{}
	for _, t := range targets {
		key := t.pkg + "." + t.goName
		if _, dup := g.byName[key]; dup {
			return nil, fmt.Errorf("manifest lists %s twice", key)
		}
		if seen[t.tsName] {
			return nil, fmt.Errorf("manifest renders two types as %s", t.tsName)
		}
		seen[t.tsName] = true
		g.byName[key] = t
	}

	var b bytes.Buffer
	b.WriteString(header)
	for _, t := range targets {
		decl, err := g.lookup(t.pkg, t.goName)
		if err != nil {
			return nil, err
		}
		b.WriteString("\n")
		if err := g.renderType(&b, t, decl); err != nil {
			return nil, err
		}
	}
	return b.Bytes(), nil
}

const header = `// Code generated by go run ./cmd/tsgen; DO NOT EDIT.
// Regenerate with: make generate-ts
//
// TypeScript mirrors of the Go response structs the web UI consumes — one per
// entry in cmd/tsgen's manifest, generated from the Go source so the two
// cannot drift (` + "`make check-ts`" + ` fails CI when they do). Field names come from
// the ` + "`json`" + ` tags; ` + "`omitempty`/`omitzero`" + ` makes a field optional; a pointer
// without either is ` + "`| null`" + `; Go doc comments are carried over verbatim.
// See cmd/tsgen/main.go for the full rendering rules and their limits.
`

// loadPackage parses every buildable Go file in a package directory once and
// indexes its type declarations and typed string constants.
func (g *generator) loadPackage(dir string) (*pkgInfo, error) {
	if p, ok := g.pkgs[dir]; ok {
		return p, nil
	}
	abs := filepath.Join(g.root, filepath.FromSlash(dir))
	bp, err := g.context.ImportDir(abs, 0)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", dir, err)
	}
	p := &pkgInfo{types: map[string]*typeDecl{}, consts: map[string][]stringConst{}, marshalers: map[string]bool{}}
	fset := token.NewFileSet()
	for _, name := range bp.GoFiles {
		f, err := parser.ParseFile(fset, filepath.Join(abs, name), nil, parser.ParseComments)
		if err != nil {
			return nil, err
		}
		fi := &fileInfo{pkg: dir, name: dir + "/" + name, imports: map[string]string{}}
		for _, imp := range f.Imports {
			ip, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				return nil, fmt.Errorf("%s: import %s: %w", fi.name, imp.Path.Value, err)
			}
			local := path.Base(ip)
			if imp.Name != nil {
				local = imp.Name.Name
			}
			fi.imports[local] = ip
		}
		for _, decl := range f.Decls {
			if fd, ok := decl.(*ast.FuncDecl); ok {
				if fd.Recv != nil && fd.Name.Name == "MarshalJSON" && len(fd.Recv.List) == 1 {
					recv := fd.Recv.List[0].Type
					if star, ok := recv.(*ast.StarExpr); ok {
						recv = star.X
					}
					if id, ok := recv.(*ast.Ident); ok {
						p.marshalers[id.Name] = true
					}
				}
				continue
			}
			gd, ok := decl.(*ast.GenDecl)
			if !ok {
				continue
			}
			switch gd.Tok {
			case token.TYPE:
				for _, spec := range gd.Specs {
					ts := spec.(*ast.TypeSpec)
					doc := ts.Doc
					if doc == nil {
						// `type X struct{…}` outside a group carries its doc
						// on the GenDecl, not the TypeSpec.
						doc = gd.Doc
					}
					p.types[ts.Name.Name] = &typeDecl{spec: ts, doc: doc, file: fi}
				}
			case token.CONST:
				for _, spec := range gd.Specs {
					vs := spec.(*ast.ValueSpec)
					typ, ok := vs.Type.(*ast.Ident)
					if !ok {
						continue
					}
					for i, n := range vs.Names {
						if i >= len(vs.Values) {
							break
						}
						lit, ok := vs.Values[i].(*ast.BasicLit)
						if !ok || lit.Kind != token.STRING {
							continue
						}
						v, err := strconv.Unquote(lit.Value)
						if err != nil {
							return nil, fmt.Errorf("%s: const %s: %w", fi.name, n.Name, err)
						}
						p.consts[typ.Name] = append(p.consts[typ.Name], stringConst{name: n.Name, value: v})
					}
				}
			}
		}
	}
	g.pkgs[dir] = p
	return p, nil
}

func (g *generator) lookup(pkg, goName string) (*typeDecl, error) {
	p, err := g.loadPackage(pkg)
	if err != nil {
		return nil, err
	}
	decl, ok := p.types[goName]
	if !ok {
		return nil, fmt.Errorf("%s: no type named %s (renamed or removed? update the manifest in cmd/tsgen)", pkg, goName)
	}
	return decl, nil
}

// ---- Rendering --------------------------------------------------------------

func (g *generator) renderType(b *bytes.Buffer, t target, decl *typeDecl) error {
	if decl.spec.TypeParams != nil {
		return fmt.Errorf("%s.%s: generic types are not supported", t.pkg, t.goName)
	}
	if g.pkgs[t.pkg].marshalers[t.goName] {
		return fmt.Errorf("%s.%s has a custom MarshalJSON, so its wire shape is not its struct; hoist the struct that method encodes to package level and name that in the manifest instead", t.pkg, t.goName)
	}
	origin := fmt.Sprintf("Generated from Go `%s.%s` (%s).", path.Base(t.pkg), t.goName, decl.file.name)
	writeJSDoc(b, "", append(commentLines(decl.doc), "", origin))

	switch typ := decl.spec.Type.(type) {
	case *ast.StructType:
		return g.renderInterface(b, t, decl, typ)
	case *ast.Ident:
		if typ.Name != "string" {
			return fmt.Errorf("%s.%s: only string-based named types can be rendered as unions, not %s", t.pkg, t.goName, typ.Name)
		}
		return g.renderStringType(b, t)
	default:
		return fmt.Errorf("%s.%s: unsupported declaration kind %T (a struct or a string-based type was expected)", t.pkg, t.goName, decl.spec.Type)
	}
}

// renderStringType renders `type X string` / `type X = string` as the union of
// its typed string constants, or as `string` when it has none.
func (g *generator) renderStringType(b *bytes.Buffer, t target) error {
	p := g.pkgs[t.pkg]
	consts := p.consts[t.goName]
	if len(consts) == 0 {
		fmt.Fprintf(b, "export type %s = string\n", t.tsName)
		return nil
	}
	members := make([]string, 0, len(consts))
	seen := map[string]bool{}
	for _, c := range consts {
		if c.value == "" {
			// The type's zero value — what omitempty elides. Every such
			// field in the manifest is omitempty, so "" never reaches the
			// wire and a `""` union member would describe a field that is
			// absent instead. A non-omitempty field of such a type would
			// need this rule revisited, not silently widened.
			continue
		}
		if seen[c.value] {
			continue // two names for one wire value are one union member
		}
		seen[c.value] = true
		members = append(members, strconv.Quote(c.value))
	}
	oneLine := fmt.Sprintf("export type %s = %s", t.tsName, strings.Join(members, " | "))
	if len(oneLine) <= printWidth {
		b.WriteString(oneLine + "\n")
		return nil
	}
	fmt.Fprintf(b, "export type %s =\n", t.tsName)
	for _, m := range members {
		fmt.Fprintf(b, "  | %s\n", m)
	}
	return nil
}

func (g *generator) renderInterface(b *bytes.Buffer, t target, decl *typeDecl, st *ast.StructType) error {
	var fields bytes.Buffer
	for _, f := range st.Fields.List {
		if len(f.Names) == 0 {
			return fmt.Errorf("%s.%s: embedded field %s is not supported (encoding/json flattens it; name the field or add a rule)", t.pkg, t.goName, exprString(f.Type))
		}
		for _, name := range f.Names {
			if !ast.IsExported(name.Name) {
				continue // encoding/json ignores unexported fields
			}
			jsonName, optional, asString, skip := parseJSONTag(f.Tag, name.Name)
			if skip {
				continue
			}
			tsType, err := g.fieldType(f.Type, decl.file, optional)
			if err != nil {
				return fmt.Errorf("%s.%s.%s: %w", t.pkg, t.goName, name.Name, err)
			}
			if asString {
				tsType = "string"
			}
			doc := commentLines(f.Doc)
			if trailing := commentLines(f.Comment); len(trailing) > 0 {
				doc = append(doc, trailing...)
			}
			writeJSDoc(&fields, "  ", doc)
			opt := ""
			if optional {
				opt = "?"
			}
			fmt.Fprintf(&fields, "  %s%s: %s\n", tsPropertyName(jsonName), opt, tsType)
		}
	}
	if fields.Len() == 0 {
		fmt.Fprintf(b, "export interface %s {}\n", t.tsName)
		return nil
	}
	fmt.Fprintf(b, "export interface %s {\n", t.tsName)
	b.Write(fields.Bytes())
	b.WriteString("}\n")
	return nil
}

// parseJSONTag applies encoding/json's tag rules: the name (defaulting to the
// Go field name), "-" to skip, and the omitempty/omitzero/string options.
func parseJSONTag(tag *ast.BasicLit, goName string) (name string, optional, asString, skip bool) {
	name = goName
	if tag == nil {
		return name, false, false, false
	}
	raw, err := strconv.Unquote(tag.Value)
	if err != nil {
		return name, false, false, false
	}
	value, ok := reflect.StructTag(raw).Lookup("json")
	if !ok {
		return name, false, false, false
	}
	if value == "-" {
		return "", false, false, true
	}
	parts := strings.Split(value, ",")
	if parts[0] != "" {
		name = parts[0]
	}
	for _, opt := range parts[1:] {
		switch opt {
		case "omitempty", "omitzero":
			optional = true
		case "string":
			asString = true
		}
	}
	return name, optional, asString, false
}

// tsPropertyName quotes a property name that is not a valid identifier
// (`"content-type"`), and leaves the rest bare — which is how prettier prints
// them.
func tsPropertyName(name string) string {
	for i, r := range name {
		if r == '_' || r == '$' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (i > 0 && r >= '0' && r <= '9') {
			continue
		}
		return strconv.Quote(name)
	}
	if name == "" {
		return `""`
	}
	return name
}

// fieldType renders a field's type. A pointer at the top level is `T | null`
// unless the field is optional, in which case the pointer only exists to make
// omitempty work and the TypeScript side sees `T`.
func (g *generator) fieldType(expr ast.Expr, file *fileInfo, optional bool) (string, error) {
	if star, ok := expr.(*ast.StarExpr); ok {
		inner, err := g.tsType(star.X, file)
		if err != nil {
			return "", err
		}
		if optional {
			return inner, nil
		}
		return inner + " | null", nil
	}
	return g.tsType(expr, file)
}

// tsType renders a Go type expression used inside a field type.
func (g *generator) tsType(expr ast.Expr, file *fileInfo) (string, error) {
	switch e := expr.(type) {
	case *ast.Ident:
		if ts, ok := basicTS[e.Name]; ok {
			return ts, nil
		}
		return g.ref(file.pkg, e.Name)
	case *ast.SelectorExpr:
		x, ok := e.X.(*ast.Ident)
		if !ok {
			return "", fmt.Errorf("unsupported selector %s", exprString(e))
		}
		ip, ok := file.imports[x.Name]
		if !ok {
			return "", fmt.Errorf("%s: package %s is not imported by %s", exprString(e), x.Name, file.name)
		}
		if ts, ok := externalTS[ip+"."+e.Sel.Name]; ok {
			return ts, nil
		}
		dir, ok := strings.CutPrefix(ip, g.module+"/")
		if !ok {
			return "", fmt.Errorf("%s.%s is outside the module and has no entry in externalTS", ip, e.Sel.Name)
		}
		return g.ref(dir, e.Sel.Name)
	case *ast.StarExpr:
		inner, err := g.tsType(e.X, file)
		if err != nil {
			return "", err
		}
		return "(" + inner + " | null)", nil
	case *ast.ArrayType:
		if id, ok := e.Elt.(*ast.Ident); ok && (id.Name == "byte" || id.Name == "uint8") {
			return "string", nil // encoding/json base64-encodes byte slices
		}
		elt, err := g.tsType(e.Elt, file)
		if err != nil {
			return "", err
		}
		if strings.ContainsAny(elt, " |") && !strings.HasPrefix(elt, "(") {
			elt = "(" + elt + ")"
		}
		return elt + "[]", nil
	case *ast.MapType:
		keyType, err := g.tsType(e.Key, file)
		if err != nil {
			return "", err
		}
		if keyType != "string" {
			// A string-based named key (a union) would make the Record
			// partial; a non-string key marshals under rules not modelled
			// here. Both are deliberate gaps until a consumed struct needs one.
			if _, isUnion := g.unionKey(e.Key, file); !isUnion {
				return "", fmt.Errorf("map key %s is not a string", exprString(e.Key))
			}
			keyType = "string"
		}
		val, err := g.tsType(e.Value, file)
		if err != nil {
			return "", err
		}
		return "Record<" + keyType + ", " + val + ">", nil
	case *ast.InterfaceType:
		if e.Methods == nil || len(e.Methods.List) == 0 {
			return "unknown", nil
		}
		return "", fmt.Errorf("non-empty interface %s cannot be rendered", exprString(e))
	default:
		return "", fmt.Errorf("unsupported type %s", exprString(expr))
	}
}

// unionKey reports whether a map key expression is a manifest type rendered
// as a string union (which encoding/json marshals as a plain string key).
func (g *generator) unionKey(expr ast.Expr, file *fileInfo) (target, bool) {
	id, ok := expr.(*ast.Ident)
	if !ok {
		return target{}, false
	}
	t, ok := g.byName[file.pkg+"."+id.Name]
	if !ok {
		return target{}, false
	}
	decl, err := g.lookup(t.pkg, t.goName)
	if err != nil {
		return target{}, false
	}
	under, ok := decl.spec.Type.(*ast.Ident)
	return t, ok && under.Name == "string"
}

// ref resolves a named type to its TypeScript name. Only manifest entries
// resolve: a struct that refers to a type the manifest does not list is an
// error naming it, so the generated set grows only on purpose.
func (g *generator) ref(pkg, goName string) (string, error) {
	if t, ok := g.byName[pkg+"."+goName]; ok {
		return t.tsName, nil
	}
	return "", fmt.Errorf("refers to %s.%s, which is not in tsgen's manifest — add it to cmd/tsgen/main.go if the web UI consumes it", pkg, goName)
}

// ---- Comments ---------------------------------------------------------------

// commentLines returns a comment group's text as lines with the comment
// markers stripped, trailing blank lines removed, and nothing reflowed.
func commentLines(cg *ast.CommentGroup) []string {
	if cg == nil {
		return nil
	}
	var lines []string
	for _, c := range cg.List {
		text := c.Text
		switch {
		case strings.HasPrefix(text, "//"):
			lines = append(lines, strings.TrimPrefix(strings.TrimPrefix(text, "//"), " "))
		case strings.HasPrefix(text, "/*"):
			text = strings.TrimSuffix(strings.TrimPrefix(text, "/*"), "*/")
			for _, l := range strings.Split(text, "\n") {
				lines = append(lines, strings.TrimSpace(l))
			}
		}
	}
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	for len(lines) > 0 && strings.TrimSpace(lines[0]) == "" {
		lines = lines[1:]
	}
	return lines
}

// writeJSDoc writes lines as a JSDoc block at the given indent: one line as
// `/** … */`, more as a starred block. A `*/` inside the text would end the
// comment early, so it is defused.
func writeJSDoc(b *bytes.Buffer, indent string, lines []string) {
	// Drop leading/trailing blanks (a type with no doc still gets its origin
	// line, preceded by a blank we do not want).
	for len(lines) > 0 && strings.TrimSpace(lines[0]) == "" {
		lines = lines[1:]
	}
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	if len(lines) == 0 {
		return
	}
	for i, l := range lines {
		lines[i] = strings.ReplaceAll(l, "*/", "*\\/")
	}
	if len(lines) == 1 {
		fmt.Fprintf(b, "%s/** %s */\n", indent, lines[0])
		return
	}
	fmt.Fprintf(b, "%s/**\n", indent)
	for _, l := range lines {
		if strings.TrimSpace(l) == "" {
			fmt.Fprintf(b, "%s *\n", indent)
		} else {
			fmt.Fprintf(b, "%s * %s\n", indent, strings.TrimRight(l, " \t"))
		}
	}
	fmt.Fprintf(b, "%s */\n", indent)
}

// exprString prints a type expression for error messages.
func exprString(e ast.Expr) string {
	switch v := e.(type) {
	case *ast.Ident:
		return v.Name
	case *ast.SelectorExpr:
		return exprString(v.X) + "." + v.Sel.Name
	case *ast.StarExpr:
		return "*" + exprString(v.X)
	case *ast.ArrayType:
		return "[]" + exprString(v.Elt)
	case *ast.MapType:
		return "map[" + exprString(v.Key) + "]" + exprString(v.Value)
	case *ast.InterfaceType:
		return "interface{}"
	case *ast.StructType:
		return "struct{…}"
	default:
		return fmt.Sprintf("%T", e)
	}
}
