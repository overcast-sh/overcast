//go:build dev

// Command capgen is a developer tool that cross-checks handler operation
// registrations against capabilities_dev.go declarations, and can generate
// the static capability snapshot (all.gen.go) and regenerate docs tables.
//
// Usage:
//
// go run -tags dev ./cmd/capgen [flags]
//
// Flags:
//
// --check        verify handler ops match declared capabilities; exit 1 on mismatch
// --generate     write internal/capabilities/all.gen.go
// --write-docs   regenerate docs/services/<key>/operations.md and the sentinel-
//
//	bracketed Operations stub in docs/services/<key>.md
//
// --service      limit to one service name (default: all)
// --workspace    workspace root (default: directory containing go.mod)
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/overcast-sh/overcast/internal/awsapi"
	"github.com/overcast-sh/overcast/internal/config"
)

// CapabilityDecl is a capability entry parsed from a capabilities_dev.go file.
type CapabilityDecl struct {
	Service      string
	Operation    string
	Category     string
	Status       string // e.g. "StatusSupported"
	Notes        string
	DocsURL      string
	DisplayName  string
	DocOnly      bool
	EmulatorOnly bool
	Since        string
}

// Operation is a handler operation extracted from service source files.
type Operation struct {
	Name   string
	IsStub bool
}

func main() {
	var (
		workspace  = flag.String("workspace", ".", "workspace root (directory with go.mod)")
		check      = flag.Bool("check", false, "check capabilities against handler ops; exit 1 on mismatch")
		checkModel = flag.Bool("check-model", false, "check capabilities, service keys, and compat registry groups against the generated AWS operation corpus")
		generate   = flag.Bool("generate", false, "generate internal/capabilities/all.gen.go")
		initCaps   = flag.Bool("init", false, "generate missing capabilities_dev.go files from detected handler ops")
		writeDocs  = flag.Bool("write-docs", false, "regenerate docs/services/<key>/operations.md and the Operations stub in docs/services/<key>.md")
		initDocs   = flag.Bool("init-docs", false, "add sentinel markers to docs that don't have them yet")
		routes     = flag.Bool("routes", false, "print the chi route skeleton the pinned model gives --service")
		service    = flag.String("service", "", "limit to one service (all if empty)")
	)
	flag.Parse()

	if !*check && !*checkModel && !*generate && !*writeDocs && !*initCaps && !*initDocs && !*routes {
		flag.Usage()
		fmt.Fprintln(os.Stderr, "\ncapgen: no action specified; use --check, --check-model, --generate, --write-docs, --routes, --init, or --init-docs")
		os.Exit(1)
	}
	if *routes && *service == "" {
		fatalf("--routes needs --service: the skeleton is one service's modeled bindings")
	}

	root, err := findWorkspaceRoot(*workspace)
	if err != nil {
		fatalf("workspace root: %v", err)
	}

	services, err := listServices(root)
	if err != nil {
		fatalf("listing services: %v", err)
	}
	if *service != "" {
		services = []string{strings.ToLower(*service)}
	}

	failures := 0
	var allCaps []CapabilityDecl

	// Checked on every run, not just --write-docs, and fatal rather than
	// counted: a service list that has drifted from config.AllServices makes
	// the generated token tables wrong, so carrying on would overwrite correct
	// rows with incorrect ones before anyone reads the error.
	if problems := validateServiceNames(); len(problems) > 0 {
		for _, problem := range problems {
			fmt.Fprintf(os.Stderr, "capgen: service tokens: %s\n", problem)
		}
		fatalf("service list is out of sync with config.AllServices (%d problem(s) above)", len(problems))
	}

	for _, svc := range services {
		svcDir := filepath.Join(root, "internal", "services", serviceDir(svc))
		caps, parseErr := parseCapabilitiesFile(svcDir, svc)
		if parseErr != nil && !os.IsNotExist(parseErr) {
			fmt.Fprintf(os.Stderr, "capgen: %s: parse capabilities_dev.go: %v\n", svc, parseErr)
		}
		// Tag each cap with the service name derived from directory
		for i := range caps {
			if caps[i].Service == "" {
				caps[i].Service = svc
			}
		}
		allCaps = append(allCaps, caps...)

		if *routes {
			printRouteSkeleton(svc, caps)
		}

		if *checkModel {
			failures += checkCapabilitiesInManifest(caps)
			failures += checkEmulatorOnlyRowsAreNotModeled(caps)
			failures += checkDocOnlyRowsAreNotDispatched(svc, svcDir, caps)
			failures += checkNotesBindingsMatchTheModel(caps)
		}

		if *check {
			ops, comprehensive, opsErr := parseHandlerOps(svcDir)
			if opsErr != nil {
				fmt.Fprintf(os.Stderr, "capgen: %s: parse handlers: %v\n", svc, opsErr)
				continue
			}
			handlers, handlerErr := implementedHandlerMethods(svcDir)
			if handlerErr != nil && !os.IsNotExist(handlerErr) {
				fmt.Fprintf(os.Stderr, "capgen: %s: parse handler methods: %v\n", svc, handlerErr)
				continue
			}
			if len(ops) == 0 && len(handlers) == 0 {
				continue
			}
			failures += checkService(svc, ops, handlers, caps, comprehensive)
		}
	}

	if *checkModel {
		failures += checkServiceKeysInManifest(services, allCaps)
		// The compat registry names every service at once, so judging it
		// against one service's capability rows reports the other 49 as
		// unknown. --service narrows what is being checked, not what the
		// registry is allowed to contain.
		if *service == "" {
			failures += checkCompatRegistryServiceKeys(root, allCaps)
		}
		failures += checkWireFactsAgainstTheModel(root, services, allCaps, *service == "")
	}

	if *generate {
		if err := generateAllGenGo(root, allCaps); err != nil {
			fatalf("writing all.gen.go: %v", err)
		}
		fmt.Printf("capgen: wrote internal/capabilities/all.gen.go (%d capabilities)\n", len(allCaps))
	}

	if *writeDocs {
		for _, svc := range services {
			svcCaps := capsByService(allCaps, svc)
			if len(svcCaps) == 0 {
				continue
			}
			docPath := filepath.Join(root, "docs", "services", serviceDocFile(svc)+".md")
			if _, statErr := os.Stat(docPath); os.IsNotExist(statErr) {
				continue
			}
			if writeErr := writeServiceDocs(root, svc, svcCaps); writeErr != nil {
				fmt.Fprintf(os.Stderr, "capgen: %s: write docs: %v\n", svc, writeErr)
			} else {
				fmt.Printf("capgen: updated docs/services/%s.md and docs/services/%s/operations.md\n", serviceDocFile(svc), serviceDocFile(svc))
			}
		}
		if changed, err := updateStatusMd(root, allCaps); err != nil {
			fmt.Fprintf(os.Stderr, "capgen: STATUS.md: %v\n", err)
		} else if changed {
			fmt.Println("capgen: updated STATUS.md op counts")
		}
		if changed, err := updateDocsReadmeServiceIndex(root, allCaps); err != nil {
			fmt.Fprintf(os.Stderr, "capgen: docs/README.md: %v\n", err)
		} else if changed {
			fmt.Println("capgen: updated docs/README.md service index")
		}
		if changed, err := updateConfigurationServiceNames(root); err != nil {
			fmt.Fprintf(os.Stderr, "capgen: docs/configuration.md: %v\n", err)
			failures++
		} else if changed {
			fmt.Println("capgen: updated docs/configuration.md service names")
		}
		if changed, err := updateRootReadmeServiceList(root, allCaps); err != nil {
			fmt.Fprintf(os.Stderr, "capgen: README.md: %v\n", err)
		} else if changed {
			fmt.Println("capgen: updated README.md service list")
		}
		if err := generateServiceSupportJSON(root, allCaps); err != nil {
			fmt.Fprintf(os.Stderr, "capgen: service-support.json: %v\n", err)
		} else {
			fmt.Println("capgen: wrote docs/generated/service-support.json")
		}
	}

	if *initCaps {
		for _, svc := range services {
			svcDir := filepath.Join(root, "internal", "services", serviceDir(svc))
			capsPath := filepath.Join(svcDir, "capabilities_dev.go")
			if _, statErr := os.Stat(capsPath); statErr == nil {
				// Already exists — skip.
				continue
			}
			ops, _, opsErr := parseHandlerOps(svcDir)
			if opsErr != nil {
				fmt.Fprintf(os.Stderr, "capgen: %s: parse handlers: %v\n", svc, opsErr)
				continue
			}
			if len(ops) == 0 {
				fmt.Fprintf(os.Stderr, "capgen: %s: no ops detected; skipping init\n", svc)
				continue
			}
			if writeErr := writeInitialCapabilities(capsPath, svc, ops); writeErr != nil {
				fmt.Fprintf(os.Stderr, "capgen: %s: write capabilities_dev.go: %v\n", svc, writeErr)
			} else {
				fmt.Printf("capgen: created internal/services/%s/capabilities_dev.go (%d ops)\n", svc, len(ops))
			}
		}
	}

	if *initDocs {
		for _, svc := range services {
			docPath := filepath.Join(root, "docs", "services", serviceDocFile(svc)+".md")
			if _, statErr := os.Stat(docPath); os.IsNotExist(statErr) {
				continue
			}
			if err := addSentinelMarkers(docPath, svc); err != nil {
				fmt.Fprintf(os.Stderr, "capgen: %s: add sentinels: %v\n", svc, err)
			} else {
				fmt.Printf("capgen: added sentinels to docs/services/%s.md\n", serviceDocFile(svc))
			}
		}
	}

	if failures > 0 {
		os.Exit(1)
	}
}

// checkCapabilitiesInManifest keeps the implementation/status inventory tied
// to the same generated AWS operation universe used by the router. DocOnly
// entries are intentionally descriptive and may refer to synthetic rows.
func checkCapabilitiesInManifest(caps []CapabilityDecl) int {
	violations := 0
	for _, cap := range caps {
		if cap.DocOnly || cap.EmulatorOnly || capabilityManifestExemption(cap) != "" {
			continue
		}
		operation := modeledOperationName(cap)
		if awsapi.HasOperation(cap.Service, operation) {
			continue
		}
		fmt.Printf("UNKNOWN_MODEL_OPERATION %s/%s  (correct the AWS operation name, mark EmulatorOnly if Overcast invented this operation, or mark DocOnly)\n", cap.Service, cap.Operation)
		violations++
	}
	return violations
}

// checkEmulatorOnlyRowsAreNotModeled is the other half of the EmulatorOnly
// contract, and the reason the flag is safe to honour.
//
// EmulatorOnly takes a row out of every published AWS operation count and out
// of the model check that would otherwise hold its name. Left unguarded that is
// a way to make a failing gate green by typing one word, which is the move #864
// was filed about. So the flag asserts something checkable: AWS models no
// operation of this name for this service. If AWS later models one, the row has
// stopped being an extension and the flag has to go, which is what this reports.
func checkEmulatorOnlyRowsAreNotModeled(caps []CapabilityDecl) int {
	violations := 0
	for _, cap := range caps {
		if !cap.EmulatorOnly {
			continue
		}
		operation := modeledOperationName(cap)
		if !awsapi.HasOperation(cap.Service, operation) {
			continue
		}
		fmt.Printf("EMULATOR_ONLY_IS_MODELED %s/%s  (AWS models %s for this service; drop EmulatorOnly and implement the AWS operation, or rename the extension)\n",
			cap.Service, cap.Operation, operation)
		violations++
	}
	return violations
}

// modeledOperationName resolves a declaration's operation identifier to the
// AWS operation name it stands for.
//
// DisplayName is consulted first because that is precisely what the field
// documents itself as: "the internal operation identifier differs from the AWS
// API name (e.g. V2SendEmail -> SendEmail)". Not reading it here left SESv2's
// rows failing the name check, and six of them carried DocOnly to silence
// that — which also removed them from every other cross-check, and is how a
// route registered on the wrong HTTP method survived 33 releases (#862).
//
// API Gateway records the same idea structurally rather than in a field: v2
// operations are modeled by apigatewayv2 as CreateApi et al., while Overcast's
// established capability names carry V2 (CreateV2Api) to keep them apart from
// the REST API operations.
func modeledOperationName(cap CapabilityDecl) string {
	if cap.DisplayName != "" {
		return cap.DisplayName
	}
	if cap.Service == "apigateway" {
		return strings.Replace(cap.Operation, "V2", "", 1)
	}
	if alias, ok := capabilityOperationAliases[cap.Service+"/"+cap.Operation]; ok {
		return alias
	}
	return cap.Operation
}

var capabilityOperationAliases = map[string]string{
	"cloudfront/DeleteFieldLevelEncryption": "DeleteFieldLevelEncryptionConfig",
}

// capabilityManifestExemptions is deliberately small and names only
// emulator-internal operations that AWS does not model at all. Keep an explicit
// reason here rather than silently weakening the model gate, and expect
// checkModelExemptionsAreStillNeeded to delete an entry whose reason has stopped
// being true.
//
// It held nine more entries until #864, every one of them asserting that API
// Gateway v2 "has no GetIntegration operation" (and DeleteIntegration,
// GetAuthorizer, GetAuthorizers, DeleteAuthorizer, GetDomainNames,
// DeleteDomainName, GetVpcLinks, DeleteVpcLink). apigatewayv2 models all nine,
// and has for as long as the manifest has existed — so nine live capability
// rows were exempt from the model check on the strength of a false statement
// about AWS. That is the shape #864 was filed about, and it is why the staleness
// check now runs alongside the exemption table rather than being trusted to
// review.
var capabilityManifestExemptions = map[string]string{
	"apigateway/ExecuteRestAPI": "emulator invoke-route helper, not an AWS control-plane operation",
	"apigateway/ExecuteV2API":   "emulator invoke-route helper, not an AWS control-plane operation",
	"appsync/ExecuteGraphQL":    "emulator GraphQL execution helper, not an AWS SDK operation",
	"eks/UpdateKubeconfig":      "emulator convenience helper, not an AWS SDK operation",
}

func capabilityManifestExemption(cap CapabilityDecl) string {
	return capabilityManifestExemptions[cap.Service+"/"+cap.Operation]
}

// notesBindingPattern matches an HTTP binding written inside a capability's
// Notes — "`PUT /v2/email/identities`", "`GET /2021-01-01/domain`". Notes are
// rendered verbatim into docs/services/*.md, so this is the form in which the
// published support matrix makes a claim about where an operation lives.
var notesBindingPattern = regexp.MustCompile(`\b(GET|PUT|POST|PATCH|DELETE|HEAD)\s+(/[^\s` + "`" + `,;)]*)`)

// checkNotesBindingsMatchTheModel holds a capability's prose to the same model
// its routes are held to.
//
// #864's fifth enforcement point asks that generated docs cannot claim a path
// Overcast does not serve. Today docs/services/*.md is generated from the
// declarations, and the declarations are typed by hand — so SESv2's
// CreateEmailIdentity published "`PUT /v2/email/identities`" for 33 releases,
// which was an accurate description of the emulator's route and the wrong
// answer about AWS. Anyone reading the matrix to find out where to send a
// request was told the one thing that would not work.
//
// Only the method and path are checked, and only when a Note states one. A
// Note is free to say anything else; what it may not do is name a binding AWS
// does not use.
func checkNotesBindingsMatchTheModel(caps []CapabilityDecl) int {
	violations := 0
	for _, cap := range caps {
		match := notesBindingPattern.FindStringSubmatch(cap.Notes)
		if match == nil {
			continue
		}
		method, path := match[1], match[2]

		matched := false
		var bindings []string
		for _, op := range awsapi.Operations(cap.Service, modeledOperationName(cap)) {
			if op.URI == "" {
				continue
			}
			bindings = append(bindings, op.HTTPMethod+" "+op.URI)
			if op.HTTPMethod == method && comparablePath(op.URI) == comparablePath(path) {
				matched = true
				break
			}
		}
		if matched || len(bindings) == 0 {
			continue
		}
		sort.Strings(bindings)
		fmt.Printf("NOTES_BINDING_MISMATCH %s/%s  (Notes say %s %s; AWS models %s — correct the note, and check the route it describes)\n",
			cap.Service, cap.Operation, method, path, strings.Join(bindings, ", "))
		violations++
	}
	return violations
}

// pathLabel matches a URI template's parameter, on either side of the
// comparison — the model's {ResourceArn} and a note's shorter {arn}.
var pathLabel = regexp.MustCompile(`\{[^}]*\}`)

// comparablePath reduces a URI to what a note and a model row have to agree
// on: the sequence of literal segments and the positions of the parameters
// between them.
//
// A parameter's name carries no meaning here — a note is free to write {arn}
// where the model writes {resourceArn} — and a note that goes on to spell out
// the query parameters an operation takes is documenting the operation, not
// contradicting its binding.
func comparablePath(uri string) string {
	if i := strings.IndexByte(uri, '?'); i >= 0 {
		uri = uri[:i]
	}
	return strings.TrimSuffix(pathLabel.ReplaceAllString(uri, "{}"), "/")
}

// checkDocOnlyRowsAreNotDispatched turns DocOnly from an exemption into a
// checkable claim.
//
// Its contract says the same thing three ways — "documentation metadata only",
// "generic behaviour, unsupported operations without explicit stubs, or other
// non-dispatched rows" — and every clause means *not dispatched*. But because
// the flag only ever suppressed checks, nothing tested the claim, and a row
// that was dispatched could carry it and disappear from the cross-check, the
// model gate and the reachability probe at once. SESv2's CreateEmailIdentity
// did exactly that: DocOnly, a handler, a registered route, and the wrong HTTP
// method on it for 33 releases (#862, #863).
//
// A handler method that only returns 501 is not an implementation — one of the
// three uses the contract names is documenting an unsupported operation that
// does have an explicit stub — so stubs leave the flag intact.
func checkDocOnlyRowsAreNotDispatched(service, svcDir string, caps []CapabilityDecl) int {
	implemented, err := implementedHandlerMethods(svcDir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0
		}
		fmt.Fprintf(os.Stderr, "capgen: %s: parse handler methods: %v\n", service, err)
		return 1
	}

	violations := 0
	for _, cap := range caps {
		if !cap.DocOnly || !implemented[cap.Operation] {
			continue
		}
		fmt.Printf("DOCONLY_DISPATCHED %s/%s  (DocOnly means non-dispatched, but a handler implements it — drop the flag and fix whatever it was hiding)\n",
			service, cap.Operation)
		violations++
	}
	return violations
}

// implementedHandlerMethods returns the names of methods in svcDir that take
// (http.ResponseWriter, *http.Request) and do something other than answer 501.
// A dispatched operation has one; a row documenting generic behaviour does not.
func implementedHandlerMethods(svcDir string) (map[string]bool, error) {
	entries, err := os.ReadDir(svcDir)
	if err != nil {
		return nil, err
	}

	fset := token.NewFileSet()
	methods := map[string]bool{}
	for _, e := range entries {
		if shouldSkipFile(e) {
			continue
		}
		f, err := parseGoFile(fset, filepath.Join(svcDir, e.Name()))
		if err != nil {
			continue
		}
		ast.Inspect(f, func(n ast.Node) bool {
			fd, ok := n.(*ast.FuncDecl)
			if !ok || fd.Recv == nil || fd.Body == nil || !isHTTPHandlerSignature(fd.Type) {
				return true
			}
			// A method name can appear in more than one build-tagged file;
			// an implementation anywhere makes the operation dispatched.
			methods[fd.Name.Name] = methods[fd.Name.Name] || !containsNotImplementedCall(fd.Body)
			return true
		})
	}
	return methods, nil
}

// isHTTPHandlerSignature reports whether a function takes exactly
// (http.ResponseWriter, *http.Request) and returns nothing.
func isHTTPHandlerSignature(ft *ast.FuncType) bool {
	if ft.Results != nil && len(ft.Results.List) > 0 {
		return false
	}
	params := ft.Params.List
	// One field can declare both parameters ("w http.ResponseWriter" and
	// "r *http.Request" are separate fields, but a signature is free to group).
	var types []ast.Expr
	for _, param := range params {
		for range max(len(param.Names), 1) {
			types = append(types, param.Type)
		}
	}
	if len(types) != 2 {
		return false
	}
	return isSelector(types[0], "http", "ResponseWriter") && isPointerToSelector(types[1], "http", "Request")
}

func isSelector(expr ast.Expr, pkg, name string) bool {
	sel, ok := expr.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != name {
		return false
	}
	ident, ok := sel.X.(*ast.Ident)
	return ok && ident.Name == pkg
}

func isPointerToSelector(expr ast.Expr, pkg, name string) bool {
	star, ok := expr.(*ast.StarExpr)
	return ok && isSelector(star.X, pkg, name)
}

// printRouteSkeleton writes the chi route registrations a service's modeled
// operations require, straight from the pinned manifest.
//
// #864's seventh point. EKS has six hand-invented paths, four of them with the
// wrong HTTP method (#858), written by hand beside a file that already had the
// right answers; the same is true of every service in the fault class. Nothing
// stops the next one being typed out too unless there is something easier to
// reach for than typing.
//
// Output goes to stdout for a human to paste and prune, rather than to a
// generated file. A service does not implement every operation AWS models —
// deciding which ones to serve is the work — but where it does serve one, the
// method and URI are not a judgement call, and this is where they come from.
func printRouteSkeleton(service string, caps []CapabilityDecl) {
	for _, line := range routeSkeleton(service, caps) {
		fmt.Println(line)
	}
}

// routeSkeleton builds the skeleton's lines. It is separate from printing so a
// test can assert that the methods and paths come from the model rather than
// from whatever the service happens to register.
func routeSkeleton(service string, caps []CapabilityDecl) []string {
	declared := map[string]CapabilityDecl{}
	for _, cap := range caps {
		declared[modeledOperationName(cap)] = cap
	}

	type route struct{ method, uri, operation, status string }
	var routes []route
	seen := map[string]bool{}
	awsapi.WalkOperations(func(op awsapi.Operation) bool {
		if awsapi.ServiceKey(op.Service) != service || op.URI == "" {
			return true
		}
		if op.Protocol != awsapi.ProtocolRESTJSON && op.Protocol != awsapi.ProtocolRESTXML {
			return true
		}
		if seen[op.Name] {
			return true
		}
		seen[op.Name] = true
		status := "not declared"
		if cap, ok := declared[op.Name]; ok {
			status = strings.TrimPrefix(cap.Status, "Status")
		}
		routes = append(routes, route{method: op.HTTPMethod, uri: op.URI, operation: op.Name, status: status})
		return true
	})
	if len(routes) == 0 {
		return []string{fmt.Sprintf("// %s: the model declares no REST bindings for this service (it is an AWS JSON or Query API).", service)}
	}
	sort.Slice(routes, func(i, j int) bool {
		if routes[i].uri != routes[j].uri {
			return routes[i].uri < routes[j].uri
		}
		return routes[i].method < routes[j].method
	})

	lines := []string{
		fmt.Sprintf("// Route skeleton for %s, generated from internal/awsapi/manifest.gen.go.", service),
		"// Delete the operations this service does not serve; do not edit the methods or paths.",
		"func (s *Service) RegisterRoutes(r chi.Router) {",
	}
	for _, route := range routes {
		lines = append(lines, fmt.Sprintf("\tr.%s(%q, s.handler.%s) // %s",
			chiMethod(route.method), stripQueryBinding(route.uri), route.operation, route.status))
	}
	return append(lines, "}")
}

// chiMethod maps an HTTP method to chi's registration helper.
func chiMethod(method string) string {
	if method == "" {
		return "HandleFunc"
	}
	return strings.ToUpper(method[:1]) + strings.ToLower(method[1:])
}

// stripQueryBinding drops the literal query a URI template may pin
// (/apikeys?mode=import). chi routes on the path, so the handler has to branch
// on the parameter; the skeleton leaves a comment-free path and the two
// operations that share it appear as two lines, which is the prompt to look.
func stripQueryBinding(uri string) string {
	if i := strings.IndexByte(uri, '?'); i >= 0 {
		return uri[:i]
	}
	return uri
}

// checkServiceKeysInManifest enforces that every service key — directory name
// or capability declaration — resolves to at least one modeled AWS identity
// through awsapi's key-or-alias mapping. A key that resolves to nothing means
// the alias table in internal/awsapi/registry_data.go is missing an entry (or
// a key is misspelled), which would silently detach that service's capability
// rows from the model gate — or worse, leave them validating against a
// related-but-wrong service family (WAF Classic vs WAF v2 share operation
// names, for example).
func checkServiceKeysInManifest(services []string, caps []CapabilityDecl) int {
	backed := map[string]bool{}
	awsapi.WalkOperations(func(op awsapi.Operation) bool {
		backed[awsapi.ServiceKey(op.Service)] = true
		return true
	})

	keys := map[string]bool{}
	for _, svc := range services {
		keys[svc] = true
	}
	for _, cap := range caps {
		keys[cap.Service] = true
	}

	sorted := make([]string, 0, len(keys))
	for key := range keys {
		sorted = append(sorted, key)
	}
	sort.Strings(sorted)

	violations := 0
	for _, key := range sorted {
		if !backed[key] {
			fmt.Printf("SERVICE_KEY_NOT_IN_MODEL %s  (no manifest identity resolves to this key; add a serviceAliases entry in internal/awsapi/registry_data.go or fix the key)\n", key)
			violations++
		}
	}
	return violations
}

// compatRegistryServiceExemptions names the compat groups whose "service" is
// deliberately not an AWS service. Keep an explicit reason here rather than
// letting any unrecognized string through: the whole point of the check is
// that a typo cannot quietly invent a service.
var compatRegistryServiceExemptions = map[string]string{
	"cdk": "IaC tool suite, not an AWS service; scoped to the cdk suite via the group's suites field",
}

// checkCompatRegistryServiceKeys holds hand-written compat groups to the same
// service vocabulary the capability table uses. Generated groups will use a
// capability key by construction; nothing stops a hand-written one from
// inventing a key that no service answers to, which silently detaches its
// results from the coverage accounting. Composed with
// checkServiceKeysInManifest, this also transitively guarantees every compat
// group's service resolves to a modeled AWS identity via key-or-alias — which
// is what makes the "cognito" group legitimate (it resolves only through the
// cognito-identity-provider alias).
func checkCompatRegistryServiceKeys(root string, caps []CapabilityDecl) int {
	path := filepath.Join(root, "compat", "suites", "registry.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return 0
		}
		fmt.Fprintf(os.Stderr, "capgen: read compat registry: %v\n", err)
		return 1
	}
	var registry struct {
		Groups []struct {
			Service string `json:"service"`
			Name    string `json:"name"`
			Tests   []struct {
				Name string `json:"name"`
				Op   string `json:"op"`
			} `json:"tests"`
		} `json:"groups"`
	}
	if err := json.Unmarshal(raw, &registry); err != nil {
		fmt.Fprintf(os.Stderr, "capgen: parse compat registry: %v\n", err)
		return 1
	}

	capabilityKeys := map[string]bool{}
	for _, cap := range caps {
		capabilityKeys[cap.Service] = true
	}

	violations := 0
	reported := map[string]bool{}
	for _, group := range registry.Groups {
		if !capabilityKeys[group.Service] && compatRegistryServiceExemptions[group.Service] == "" {
			if !reported[group.Service] {
				reported[group.Service] = true
				fmt.Printf("COMPAT_REGISTRY_UNKNOWN_SERVICE %s  (group %q; not a capability service key — fix the key or add an explicit exemption)\n", group.Service, group.Name)
				violations++
			}
			continue
		}
		// A test's `op` is the registry's own statement of which AWS operation
		// it exercises, and until now nothing read it. `--check-parity`
		// measures the registry against a run of the registry — a uniformity
		// check across the eight suites, not a coverage check — so a typo in
		// an `op` detaches that test from the operation it claims to cover and
		// nothing anywhere notices.
		//
		// Only `op` is validated. A test `name` is a scenario name where it
		// needs to be (PutObjectMultipleKeys, ListObjectsV2Delimiter), so the
		// schema makes `op` the field that names an operation; holding names
		// to the model would reject the ones doing their job.
		for _, test := range group.Tests {
			if test.Op == "" || awsapi.HasOperation(group.Service, test.Op) {
				continue
			}
			fmt.Printf("COMPAT_REGISTRY_UNKNOWN_OPERATION %s/%s  (group %q, test %q; AWS models no such operation for this service)\n",
				group.Service, test.Op, group.Name, test.Name)
			violations++
		}
	}
	return violations
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "capgen: "+format+"\n", args...)
	os.Exit(1)
}

// writeInitialCapabilities generates a capabilities_dev.go from detected handler ops.
// All implemented ops get StatusSupported; all stub ops get StatusUnsupported.
func writeInitialCapabilities(path, svc string, ops []Operation) error {
	var buf bytes.Buffer
	buf.WriteString("//go:build dev\n\n")
	buf.WriteString("package " + svc + "\n\n")
	buf.WriteString("import \"github.com/overcast-sh/overcast/internal/capabilities\"\n\n")
	buf.WriteString("func init() {\n")
	buf.WriteString("\tcapabilities.Default.Register(\n")
	for _, op := range ops {
		status := "capabilities.StatusSupported"
		notes := ""
		if op.IsStub {
			status = "capabilities.StatusUnsupported"
			notes = "stub; returns 501"
		}
		buf.WriteString("\t\t{Service: \"" + svc + "\", Operation: \"" + op.Name + "\", Category: \"General\", Status: " + status)
		if notes != "" {
			buf.WriteString(", Notes: \"" + notes + "\"")
		}
		buf.WriteString("},\n")
	}
	buf.WriteString("\t)\n}\n")
	return os.WriteFile(path, buf.Bytes(), 0o644)
}

// addSentinelMarkers gives a new service doc an empty generated block at the
// position writeLandingStub will keep it at — last, except for a trailing
// "## Related". A doc that already has the markers is left alone.
//
// --write-docs no longer needs this (it appends the block itself when a doc has
// none), but --init-docs stays: it is how a freshly hand-written service page
// is shown the shape it has to end in before any capability rows exist.
func addSentinelMarkers(docPath, svc string) error {
	_ = svc
	data, err := os.ReadFile(docPath)
	if err != nil {
		return err
	}
	content := string(data)
	if strings.Contains(content, capBeginMarker) {
		return nil // already present
	}
	sentinels := capBeginMarker + "\n" + capEndMarker + "\n"
	if idx := findLastTopLevelHeading(content, "## Related"); idx >= 0 {
		content = strings.TrimRight(content[:idx], "\n") + "\n\n" + sentinels + "\n" + content[idx:]
	} else {
		content = strings.TrimRight(content, "\n") + "\n\n" + sentinels
	}
	return os.WriteFile(docPath, []byte(content), 0o644)
}

// findWorkspaceRoot walks up from start until it finds go.mod.
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

func listServices(root string) ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(root, "internal", "services"))
	if err != nil {
		return nil, err
	}
	var services []string
	for _, e := range entries {
		if e.IsDir() {
			services = append(services, e.Name())
		}
	}
	// Include known sub-services that have their own capabilities files.
	for name := range subServices {
		services = append(services, name)
	}
	sort.Strings(services)
	return services, nil
}

// subServices maps virtual service names to their directory path under internal/services/.
var subServices = map[string]string{
	"cloudwatch-logs": "cloudwatch/logs",
}

// serviceDir returns the directory path for a service relative to root/internal/services/.
func serviceDir(svc string) string {
	if sub, ok := subServices[svc]; ok {
		return sub
	}
	return svc
}

// parseHandlerOps extracts operation names from handler source files in svcDir.
// It detects three registration patterns:
//
//  1. Map keys in map[string]http.HandlerFunc{...} literals.
//  2. Map keys in map[string]op.Operation{...} literals (the typed registry a
//     service builds in typedOps()).
//  3. Case strings in switch statements that have 3+ PascalCase operation names.
//
// Stub operations are detected by finding methods that call protocol.NotImplemented*.
// The second return value is true when at least one map[string]http.HandlerFunc
// registration was found (i.e., detection is comprehensive). When false, ops were
// found only via switch dispatch or the typed registry, which means the service uses
// REST routing for its primary dispatch and ORPHAN violations should not be treated
// as failures — see isTypedOperationMap for why a typed registry is not evidence of
// comprehensive detection.
func parseHandlerOps(svcDir string) ([]Operation, bool, error) {
	entries, err := os.ReadDir(svcDir)
	if err != nil {
		return nil, false, err
	}

	fset := token.NewFileSet()
	stubMethods := map[string]struct{}{}

	// Pass 1: collect stub method names.
	for _, e := range entries {
		if shouldSkipFile(e) {
			continue
		}
		absPath := filepath.Join(svcDir, e.Name())
		f, err := parseGoFile(fset, absPath)
		if err != nil {
			continue
		}
		ast.Inspect(f, func(n ast.Node) bool {
			fd, ok := n.(*ast.FuncDecl)
			if !ok || fd.Recv == nil || fd.Body == nil {
				return true
			}
			// A method is a stub only if its body directly calls protocol.NotImplemented*.
			// Do not rely on file name (handler_stubs.go may contain real implementations).
			if containsNotImplementedCall(fd.Body) {
				stubMethods[fd.Name.Name] = struct{}{}
			}
			return true
		})
	}

	// Pass 2: collect operation names from handler registrations.
	seen := map[string]struct{}{}
	var ops []Operation
	hasMap := false // true when a map[string]http.HandlerFunc was detected

	for _, e := range entries {
		if shouldSkipFile(e) {
			continue
		}
		absPath := filepath.Join(svcDir, e.Name())
		f, err := parseGoFile(fset, absPath)
		if err != nil {
			continue
		}

		// Pattern 1: map[string]http.HandlerFunc{...} literals.
		ast.Inspect(f, func(n ast.Node) bool {
			cl, ok := n.(*ast.CompositeLit)
			if !ok || !isHandlerFuncMap(cl) {
				return true
			}
			hasMap = true
			for _, elt := range cl.Elts {
				kv, ok := elt.(*ast.KeyValueExpr)
				if !ok {
					continue
				}
				lit, ok := kv.Key.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					continue
				}
				opName := strings.Trim(lit.Value, `"`)
				if !isAWSOperation(opName) {
					continue
				}
				if _, exists := seen[opName]; exists {
					continue
				}
				seen[opName] = struct{}{}
				isStub := false
				if sel, ok := kv.Value.(*ast.SelectorExpr); ok {
					if _, found := stubMethods[sel.Sel.Name]; found {
						isStub = true
					}
				}
				ops = append(ops, Operation{Name: opName, IsStub: isStub})
			}
			return true
		})

		// Pattern 2: map[string]op.Operation{...} literals — the typed registry.
		ast.Inspect(f, func(n ast.Node) bool {
			cl, ok := n.(*ast.CompositeLit)
			if !ok || !isTypedOperationMap(cl) {
				return true
			}
			for _, elt := range cl.Elts {
				kv, ok := elt.(*ast.KeyValueExpr)
				if !ok {
					continue
				}
				lit, ok := kv.Key.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					continue
				}
				opName := strings.Trim(lit.Value, `"`)
				if !isAWSOperation(opName) {
					continue
				}
				if _, exists := seen[opName]; exists {
					continue
				}
				seen[opName] = struct{}{}
				ops = append(ops, Operation{Name: opName, IsStub: typedOpIsStub(kv.Value, stubMethods)})
			}
			return true
		})

		// Pattern 3: switch statements with PascalCase string cases.
		ast.Inspect(f, func(n ast.Node) bool {
			sw, ok := n.(*ast.SwitchStmt)
			if !ok {
				return true
			}
			if !isOperationSwitch(sw) {
				return true
			}
			awsCases := collectAWSCasesFromSwitch(sw, stubMethods)
			if len(awsCases) < 3 {
				return true
			}
			for _, op := range awsCases {
				if _, exists := seen[op.Name]; exists {
					continue
				}
				seen[op.Name] = struct{}{}
				ops = append(ops, op)
			}
			return true
		})
	}

	sort.Slice(ops, func(i, j int) bool { return ops[i].Name < ops[j].Name })
	return ops, hasMap, nil
}

func isOperationSwitch(sw *ast.SwitchStmt) bool {
	if sw.Tag == nil {
		return false
	}
	name := strings.ToLower(exprName(sw.Tag))
	return name == "action" || name == "operation" || name == "op" || strings.HasSuffix(name, "action") || strings.HasSuffix(name, "operation")
}

func exprName(expr ast.Expr) string {
	switch v := expr.(type) {
	case *ast.Ident:
		return v.Name
	case *ast.SelectorExpr:
		return v.Sel.Name
	case *ast.CallExpr:
		return exprName(v.Fun)
	case *ast.ParenExpr:
		return exprName(v.X)
	default:
		return ""
	}
}

func shouldSkipFile(e os.DirEntry) bool {
	if e.IsDir() {
		return true
	}
	name := e.Name()
	if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
		return true
	}
	if name == "capabilities_dev.go" {
		return true
	}
	return false
}

func parseGoFile(fset *token.FileSet, path string) (*ast.File, error) {
	src, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return parser.ParseFile(fset, path, src, 0)
}

// containsNotImplementedCall reports whether a method's whole behaviour is to
// answer 501 — the shape of a stub.
//
// Only top-level statements count. A NotImplemented call nested in an if or a
// switch is a branch of a working handler, not a stub: Lambda's CreateFunction
// implements the operation and refuses one unsupported package type that way,
// and matching it anywhere in the body reported five working Lambda operations
// as stubs declared Supported. "Directly calls" is what the rule always said;
// this is it enforced.
func containsNotImplementedCall(body *ast.BlockStmt) bool {
	for _, stmt := range body.List {
		var expr ast.Expr
		switch s := stmt.(type) {
		case *ast.ExprStmt:
			expr = s.X
		case *ast.ReturnStmt:
			if len(s.Results) != 1 {
				continue
			}
			expr = s.Results[0]
		default:
			continue
		}
		call, ok := expr.(*ast.CallExpr)
		if !ok {
			continue
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if ok && strings.HasPrefix(sel.Sel.Name, "NotImplemented") {
			return true
		}
	}
	return false
}

func isHandlerFuncMap(cl *ast.CompositeLit) bool {
	mt, ok := cl.Type.(*ast.MapType)
	if !ok {
		return false
	}
	keyIdent, ok := mt.Key.(*ast.Ident)
	if !ok || keyIdent.Name != "string" {
		return false
	}
	valSel, ok := mt.Value.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	return valSel.Sel.Name == "HandlerFunc"
}

// isTypedOperationMap reports whether cl is a map[string]op.Operation literal —
// the Smithy-aligned typed registry a service builds in typedOps().
//
// Operations found here count towards MISSING but deliberately do not set the
// comprehensive flag the way a map[string]http.HandlerFunc registration does. A
// typed registry is a lower bound on a service's dispatch surface, not the whole
// of it: REST-routed services such as route53 and appregistry register only the
// operations that arrive through the protocol dispatcher and serve the rest from
// RegisterRoutes (or, for appregistry's tag APIs, from another service's shared
// routes). Treating the registry as comprehensive would report those as ORPHANs.
func isTypedOperationMap(cl *ast.CompositeLit) bool {
	mt, ok := cl.Type.(*ast.MapType)
	if !ok {
		return false
	}
	keyIdent, ok := mt.Key.(*ast.Ident)
	if !ok || keyIdent.Name != "string" {
		return false
	}
	valSel, ok := mt.Value.(*ast.SelectorExpr)
	if !ok || valSel.Sel.Name != "Operation" {
		return false
	}
	pkg, ok := valSel.X.(*ast.Ident)
	return ok && pkg.Name == "op"
}

// typedOpIsStub reports whether a typed registry entry delegates to a stub. The
// value is a constructor call such as op.NewTyped[in, out]("Name", s.fooTyped),
// so the handler method reference is one of the call's arguments.
func typedOpIsStub(value ast.Expr, stubMethods map[string]struct{}) bool {
	call, ok := value.(*ast.CallExpr)
	if !ok {
		return false
	}
	for _, arg := range call.Args {
		sel, ok := arg.(*ast.SelectorExpr)
		if !ok {
			continue
		}
		if _, found := stubMethods[sel.Sel.Name]; found {
			return true
		}
	}
	return false
}

// collectAWSCasesFromSwitch returns the operations an action switch dispatches,
// each marked according to whether the method its arm calls is a stub.
//
// The stub flag used to be hardcoded false here, so an operation dispatched
// from a switch could never be reported as a 501 however plainly its handler
// said so — while the same operation registered in a map or the typed registry
// would be. That is why ElastiCache advertised DescribeCacheEngineVersions and
// RebootCacheCluster as ✅ Supported while both answered 501 from
// handler_stubs.go under a TODO, recorded in #861 and #864 as the
// status-honesty gap.
func collectAWSCasesFromSwitch(sw *ast.SwitchStmt, stubMethods map[string]struct{}) []Operation {
	var cases []Operation
	for _, s := range sw.Body.List {
		cc, ok := s.(*ast.CaseClause)
		if !ok || len(cc.List) == 0 {
			continue
		}
		lit, ok := cc.List[0].(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			continue
		}
		val := strings.Trim(lit.Value, `"`)
		if isAWSOperation(val) && !isKnownNonOperationCase(val) {
			cases = append(cases, Operation{Name: val, IsStub: switchArmIsStub(cc, stubMethods)})
		}
	}
	return cases
}

// switchArmIsStub reports whether a switch arm's only work is to call a method
// that answers 501 — the `case "X": h.X(w, r)` shape every action switch uses.
func switchArmIsStub(cc *ast.CaseClause, stubMethods map[string]struct{}) bool {
	for _, stmt := range cc.Body {
		expr, ok := stmt.(*ast.ExprStmt)
		if !ok {
			continue
		}
		call, ok := expr.X.(*ast.CallExpr)
		if !ok {
			continue
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			continue
		}
		if _, found := stubMethods[sel.Sel.Name]; found {
			return true
		}
	}
	return false
}

// aslChoiceOperators are the Amazon States Language Choice comparison
// operators. They are PascalCase strings switched on in
// internal/services/stepfunctions, so the switch-case heuristic would
// otherwise read them as Step Functions API operations.
var aslChoiceOperators = map[string]bool{
	"StringEquals": true, "StringEqualsPath": true,
	"StringLessThan": true, "StringLessThanPath": true,
	"StringGreaterThan": true, "StringGreaterThanPath": true,
	"StringLessThanEquals": true, "StringLessThanEqualsPath": true,
	"StringGreaterThanEquals": true, "StringGreaterThanEqualsPath": true,
	"StringMatches": true,

	"NumericEquals": true, "NumericEqualsPath": true,
	"NumericLessThan": true, "NumericLessThanPath": true,
	"NumericGreaterThan": true, "NumericGreaterThanPath": true,
	"NumericLessThanEquals": true, "NumericLessThanEqualsPath": true,
	"NumericGreaterThanEquals": true, "NumericGreaterThanEqualsPath": true,

	"BooleanEquals": true, "BooleanEqualsPath": true,

	"TimestampEquals": true, "TimestampEqualsPath": true,
	"TimestampLessThan": true, "TimestampLessThanPath": true,
	"TimestampGreaterThan": true, "TimestampGreaterThanPath": true,
	"TimestampLessThanEquals": true, "TimestampLessThanEqualsPath": true,
	"TimestampGreaterThanEquals": true, "TimestampGreaterThanEqualsPath": true,

	"IsNull": true, "IsPresent": true, "IsNumeric": true,
	"IsString": true, "IsBoolean": true, "IsTimestamp": true,
}

// isKnownNonOperationCase names PascalCase switch cases that are domain
// vocabulary rather than AWS API operations. Without it the switch-case
// heuristic reports them as operations missing a capability declaration.
func isKnownNonOperationCase(s string) bool {
	// CloudWatch alarm comparison operators.
	switch s {
	case "GreaterThanThreshold", "GreaterThanOrEqualToThreshold", "LessThanThreshold", "LessThanOrEqualToThreshold":
		return true
	}
	return aslChoiceOperators[s]
}

// isAWSOperation returns true if s looks like an AWS API operation name (PascalCase, 3-80 chars).
// All-caps identifiers (e.g. "AWS", "HTTP", "MOCK") are excluded because AWS operation names
// are always mixed-case PascalCase and never consist entirely of uppercase letters.
func isAWSOperation(s string) bool {
	if len(s) < 3 || len(s) > 80 {
		return false
	}
	if !unicode.IsUpper(rune(s[0])) {
		return false
	}
	hasLower := false
	for _, r := range s {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			return false
		}
		if unicode.IsLower(r) {
			hasLower = true
		}
	}
	// Require at least one lowercase letter to distinguish PascalCase from ALL_CAPS constants.
	return hasLower
}

// parseCapabilitiesFile parses capabilities_dev.go in svcDir and extracts Capability literals.
// If a Capability literal omits the Service field (e.g. when using RegisterForService),
// the svc parameter is used as the fallback so the generated docs and all.gen.go stay correct.
func parseCapabilitiesFile(svcDir, svc string) ([]CapabilityDecl, error) {
	path := filepath.Join(svcDir, "capabilities_dev.go")
	src, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, os.ErrNotExist
	}
	if err != nil {
		return nil, err
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, src, 0)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	stringConsts := collectFileStringConsts(f)
	var caps []CapabilityDecl
	ast.Inspect(f, func(n ast.Node) bool {
		cl, ok := n.(*ast.CompositeLit)
		if !ok || !isCapabilityLit(cl) {
			return true
		}
		c := CapabilityDecl{}
		for _, elt := range cl.Elts {
			kv, ok := elt.(*ast.KeyValueExpr)
			if !ok {
				continue
			}
			key, ok := kv.Key.(*ast.Ident)
			if !ok {
				continue
			}
			switch key.Name {
			case "Service":
				c.Service = stringExpr(kv.Value, stringConsts)
			case "Operation":
				c.Operation = stringExpr(kv.Value, stringConsts)
			case "Category":
				c.Category = stringExpr(kv.Value, stringConsts)
			case "Status":
				c.Status = selectorOrIdent(kv.Value)
			case "Notes":
				c.Notes = stringExpr(kv.Value, stringConsts)
			case "DocsURL":
				c.DocsURL = stringExpr(kv.Value, stringConsts)
			case "DisplayName":
				c.DisplayName = stringExpr(kv.Value, stringConsts)
			case "DocOnly":
				c.DocOnly = boolLit(kv.Value)
			case "EmulatorOnly":
				c.EmulatorOnly = boolLit(kv.Value)
			case "Since":
				c.Since = stringExpr(kv.Value, stringConsts)
			}
		}
		if c.Operation != "" {
			if c.Service == "" {
				c.Service = svc // filled in by RegisterForService at runtime; use dir name here
			}
			caps = append(caps, c)
		}
		return true
	})
	return caps, nil
}

func collectFileStringConsts(f *ast.File) map[string]string {
	out := make(map[string]string)
	for _, decl := range f.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.CONST {
			continue
		}
		for _, spec := range gen.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok || len(vs.Names) == 0 || len(vs.Values) == 0 {
				continue
			}
			for i, name := range vs.Names {
				v := vs.Values[0]
				if i < len(vs.Values) {
					v = vs.Values[i]
				}
				if s := stringLit(v); s != "" {
					out[name.Name] = s
				}
			}
		}
	}
	return out
}

func stringExpr(e ast.Expr, consts map[string]string) string {
	if s := stringLit(e); s != "" {
		return s
	}
	id, ok := e.(*ast.Ident)
	if !ok {
		return ""
	}
	return consts[id.Name]
}

func isCapabilityLit(cl *ast.CompositeLit) bool {
	if cl.Type == nil {
		return false
	}
	if sel, ok := cl.Type.(*ast.SelectorExpr); ok {
		return sel.Sel.Name == "Capability"
	}
	if id, ok := cl.Type.(*ast.Ident); ok {
		return id.Name == "Capability"
	}
	return false
}

func stringLit(e ast.Expr) string {
	lit, ok := e.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return ""
	}
	// Parse string literals using Go unquoting semantics so escaped
	// characters are rendered correctly in generated docs.
	v, err := strconv.Unquote(lit.Value)
	if err != nil {
		return strings.Trim(lit.Value, `"`)
	}
	return v
}

func selectorOrIdent(e ast.Expr) string {
	if sel, ok := e.(*ast.SelectorExpr); ok {
		return sel.Sel.Name
	}
	if id, ok := e.(*ast.Ident); ok {
		return id.Name
	}
	return ""
}

func boolLit(e ast.Expr) bool {
	id, ok := e.(*ast.Ident)
	if !ok {
		return false
	}
	return id.Name == "true"
}

// checkService reports missing and orphaned capability entries for a service.
// Returns the number of violations found.
// When comprehensive is false (ops detected only via switch-case, not map),
// ORPHAN entries are printed as warnings but do not count as violations, because
// REST-routed operations cannot be detected by static analysis.
// checkService cross-checks a service's capability declarations against the
// operations its package actually dispatches.
//
// ops are the action-dispatch registrations parseHandlerOps finds — the map or
// switch a Query or AWS-JSON service routes on. handlers are the
// (http.ResponseWriter, *http.Request) methods on the package's types, keyed by
// name and valued by whether they do more than answer 501. A REST-routed
// operation appears only in the second: chi binds it by path, so there is no
// action table to read.
//
// Reading the methods is what removed the last hand-maintained operation
// inventory in the tree. AppSync used to carry an 85-row
// rest_operations_dev.go listing method, path and operation for every REST
// route so that capgen had something to cross-check — every field of it
// already in the pinned manifest, and kept in step by a test asserting it
// matched the routes. The manifest supplies the bindings, the router gate
// (internal/router/modelbinding_dev_test.go) asserts the routes, and the
// handler methods supply dispatch, so all three of those artefacts are now
// derived rather than typed.
func checkService(service string, ops []Operation, handlers map[string]bool, caps []CapabilityDecl, comprehensive bool) int {
	capByOp := make(map[string]CapabilityDecl, len(caps))
	for _, c := range caps {
		capByOp[c.Operation] = c
	}
	opByName := make(map[string]Operation, len(ops))
	for _, op := range ops {
		opByName[op.Name] = op
	}

	violations := 0
	for _, op := range ops {
		cap, declared := capByOp[op.Name]
		if !declared {
			fmt.Printf("MISSING    %s/%s  (add to internal/services/%s/capabilities_dev.go)\n",
				service, op.Name, service)
			violations++
			continue
		}
		if op.IsStub && cap.Status != "StatusUnsupported" && cap.Status != "StatusWIP" {
			fmt.Printf("WRONG_STATUS %s/%s  (stub returns 501 but declared as %s)\n",
				service, op.Name, cap.Status)
			violations++
		}
	}
	for _, cap := range caps {
		if cap.DocOnly || cap.Status == "StatusUnsupported" {
			continue
		}
		if _, found := opByName[cap.Operation]; found {
			continue
		}
		implemented, hasHandler := handlers[cap.Operation]
		switch {
		case implemented:
			// Dispatched by path rather than by action.
		case hasHandler:
			// The only method of that name answers 501, so the row's status
			// describes an operation that cannot behave as advertised. ops
			// carries the same rule for action-dispatched operations above.
			if cap.Status != "StatusWIP" {
				fmt.Printf("WRONG_STATUS %s/%s  (stub returns 501 but declared as %s)\n",
					service, cap.Operation, cap.Status)
				violations++
			}
		case comprehensive:
			fmt.Printf("ORPHAN     %s/%s  (in capabilities_dev.go but not in handler)\n",
				service, cap.Operation)
			violations++
		}
		// A row this pass cannot attribute is not reported. It used to print
		// "REST-routed; not detectable — skipping" twenty times a run, which
		// asserted nothing and trained the reader to scroll past capgen's
		// output. It is not undetectable any more: a REST-bound operation is
		// held to the method and URI the manifest gives it by
		// TestModeledBindings_areServedWhereAWSBindsThem, which reads the real
		// router rather than guessing from a handler's name.
	}
	return violations
}

func capsByService(all []CapabilityDecl, service string) []CapabilityDecl {
	var out []CapabilityDecl
	for _, c := range all {
		if c.Service == service {
			out = append(out, c)
		}
	}
	return out
}

// generateAllGenGo writes internal/capabilities/all.gen.go.
func generateAllGenGo(root string, caps []CapabilityDecl) error {
	sorted := make([]CapabilityDecl, len(caps))
	copy(sorted, caps)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].Service != sorted[j].Service {
			return sorted[i].Service < sorted[j].Service
		}
		return sorted[i].Operation < sorted[j].Operation
	})

	var buf bytes.Buffer
	buf.WriteString("//go:build dev\n\n")
	buf.WriteString("// Code generated by cmd/capgen; DO NOT EDIT.\n")
	buf.WriteString("// Regenerate with: make generate-caps\n\n")
	buf.WriteString("package capabilities\n\n")
	buf.WriteString("// AllCapabilities is the static snapshot of all declared service capabilities.\n")
	buf.WriteString("// Used by tools (e.g. cmd/overcast-mcp) that need capability data without importing\n")
	buf.WriteString("// all service packages. Only included in dev builds.\n")
	buf.WriteString("var AllCapabilities = []Capability{\n")
	for _, c := range sorted {
		buf.WriteString(fmt.Sprintf("\t{Service: %q, Operation: %q, Category: %q, Status: %s, Notes: %q, DocsURL: %q, DisplayName: %q, DocOnly: %t, EmulatorOnly: %t, Since: %q},\n",
			c.Service, c.Operation, c.Category, c.Status, c.Notes, c.DocsURL, c.DisplayName, c.DocOnly, c.EmulatorOnly, c.Since))
	}
	buf.WriteString("}\n")

	out := filepath.Join(root, "internal", "capabilities", "all.gen.go")
	return os.WriteFile(out, buf.Bytes(), 0o644)
}

// capgen owns two files per service, and both are bracketed by the same
// sentinels so `make docs-check`'s regenerate-and-diff gate catches drift in
// either:
//
//   - docs/services/<key>/operations.md — the full per-operation tables. The
//     whole file is generated, frontmatter included; the sentinels sit just
//     inside it so a reader who opens the raw Markdown can see that at a glance.
//   - docs/services/<key>.md — a short Operations section: one coverage
//     sentence and a link to the table. Everything else on the landing page is
//     hand-written.
//
// The tables used to live at the bottom of the landing page, which made every
// service doc a page of prose followed by two hundred rows nobody reads top to
// bottom. Splitting them keeps the landing page answerable in one screen and
// gives the reference its own URL, which is what a reference wants.
const (
	capBeginMarker = "<!-- BEGIN overcast:capabilities -->"
	capEndMarker   = "<!-- END overcast:capabilities -->"

	// operationsDocName is the sub-page capgen writes. docs/dev/service-doc-template.md
	// names the other three sub-pages a service may have, all hand-written.
	operationsDocName = "operations.md"
)

// writeServiceDocs regenerates both of capgen's targets for one service: the
// operations sub-page and the landing page's Operations stub.
func writeServiceDocs(root, service string, caps []CapabilityDecl) error {
	docFile := serviceDocFile(service)
	opsDir := filepath.Join(root, "docs", "services", docFile)
	if err := os.MkdirAll(opsDir, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(opsDir, operationsDocName), []byte(buildOperationsDoc(service, caps)), 0o644); err != nil {
		return err
	}
	return writeLandingStub(filepath.Join(root, "docs", "services", docFile+".md"), service, caps)
}

// coverage counts what the landing page's one-line summary reports. It is the
// same arithmetic docs/generated/service-support.json publishes as total_ops /
// implemented_ops, so the two can never disagree: anything not explicitly
// StatusUnsupported is something a caller can invoke.
//
// An EmulatorOnly row is counted in neither half. The sentence is read as a
// statement about AWS coverage, and an operation AWS does not model cannot
// raise or lower it; CloudFront's ProxyRequest made the service read "all 89
// listed operations" when AWS has 88 of them (#75).
func coverage(caps []CapabilityDecl) (implemented, total int) {
	for _, c := range caps {
		if c.EmulatorOnly {
			continue
		}
		total++
		if c.Status != "StatusUnsupported" {
			implemented++
		}
	}
	return implemented, total
}

// coverageSentence renders the coverage counts as one sentence, in both files.
func coverageSentence(caps []CapabilityDecl) string {
	implemented, total := coverage(caps)
	noun := "operations are"
	if total == 1 {
		noun = "operation is"
	}
	if implemented == total {
		return fmt.Sprintf("All %d listed %s implemented.", total, noun)
	}
	return fmt.Sprintf("%d of %d listed %s implemented.", implemented, total, noun)
}

// writeLandingStub replaces the landing page's generated block with the
// Operations stub, leaving every hand-written section alone.
//
// The block goes last, except that a trailing "## Related" stays last — link
// lists belong at the bottom of a page, and the structural lint in
// internal/docslint enforces exactly that shape, so the two agree by
// construction.
func writeLandingStub(docPath, service string, caps []CapabilityDecl) error {
	existing, err := os.ReadFile(docPath)
	if err != nil {
		return err
	}
	content := stripGeneratedBlock(string(existing))
	block := capBeginMarker + "\n\n" + buildLandingStub(service, caps) + capEndMarker + "\n"

	var out string
	if idx := findLastTopLevelHeading(content, "## Related"); idx >= 0 {
		out = strings.TrimRight(content[:idx], "\n") + "\n\n" + block + "\n" + content[idx:]
	} else {
		out = strings.TrimRight(content, "\n") + "\n\n" + block
	}
	return os.WriteFile(docPath, []byte(out), 0o644)
}

// stripGeneratedBlock removes a BEGIN..END block wherever it currently sits, so
// a rerun re-inserts it at the canonical position rather than compounding a
// past misplacement.
func stripGeneratedBlock(content string) string {
	begin := strings.Index(content, capBeginMarker)
	end := strings.Index(content, capEndMarker)
	if begin < 0 || end < begin {
		return content
	}
	return strings.TrimRight(content[:begin], "\n") + "\n\n" + strings.TrimLeft(content[end+len(capEndMarker):], "\n")
}

// buildLandingStub is the body of the landing page's generated block: the
// coverage sentence and the link to the table. Deliberately two lines — the
// landing page's job is to answer "does this service work", and a reader who
// needs the per-operation detail follows the link.
func buildLandingStub(service string, caps []CapabilityDecl) string {
	docFile := serviceDocFile(service)
	return fmt.Sprintf("## Operations\n\n%s\nPer-operation status, notes and AWS API links: [%s operations](%s/%s).\n\n",
		coverageSentence(caps), serviceDisplayName(service), docFile, operationsDocName)
}

// buildOperationsDoc renders the whole of docs/services/<key>/operations.md.
func buildOperationsDoc(service string, caps []CapabilityDecl) string {
	docFile := serviceDocFile(service)
	name := serviceDisplayName(service)
	title := name + " operations"

	var b strings.Builder
	b.WriteString("---\n")
	fmt.Fprintf(&b, "title: %s\n", quoteYAMLString(title))
	fmt.Fprintf(&b, "description: %s\n", quoteYAMLString(operationsDescription(name, caps)))
	b.WriteString("section: \"Service Reference\"\n")
	b.WriteString("tags:\n")
	for _, tag := range operationsDocTags(docFile) {
		fmt.Fprintf(&b, "  - %s\n", tag)
	}
	b.WriteString("---\n\n")

	b.WriteString(capBeginMarker + "\n\n")
	fmt.Fprintf(&b, "# %s\n\n", title)
	fmt.Fprintf(&b, "%s Back to [%s](../%s.md).\n", coverageSentence(caps), name, docFile)
	b.WriteString(buildDocSection(service, caps))
	b.WriteString(buildOperationsRelated(service))
	b.WriteString(capEndMarker + "\n")
	return b.String()
}

// buildOperationsRelated is the operations page's link footer.
//
// internal/docslint requires one on every published page, and requires this
// file to hold nothing capgen did not write — so the footer is generated too.
// Two links: the landing page, which is where a reader who arrived from search
// on one operation row usually wants to be, and the service index.
func buildOperationsRelated(service string) string {
	return fmt.Sprintf("## Related\n\n- [%s](../%s.md) — quick start, what works, and the differences from AWS\n- [All service pages](../README.md)\n\n",
		serviceDisplayName(service), serviceDocFile(service))
}

// operationsDescription is the frontmatter description, kept well inside the
// 220-character cap internal/docsindex enforces.
func operationsDescription(name string, caps []CapabilityDecl) string {
	implemented, total := coverage(caps)
	return fmt.Sprintf("Every %s operation Overcast declares — %d of %d implemented — with status, behaviour notes and a link to the AWS API reference for each.", name, implemented, total)
}

// operationsDocTags mirrors what internal/docsindex would infer from the
// path, so the generated frontmatter and the inferred metadata agree.
func operationsDocTags(docFile string) []string {
	tags := []string{"docs", "operations", "services"}
	tags = append(tags, strings.FieldsFunc(docFile, func(r rune) bool { return r == '-' || r == '_' })...)
	seen := map[string]bool{}
	out := make([]string, 0, len(tags))
	for _, tag := range tags {
		if tag == "" || seen[tag] {
			continue
		}
		seen[tag] = true
		out = append(out, tag)
	}
	sort.Strings(out)
	return out
}

// serviceDisplayName is the human name for a service key, falling back to the
// key itself for one that predates statusDisplayNames.
func serviceDisplayName(service string) string {
	if name := statusDisplayNames[service]; name != "" {
		return name
	}
	return service
}

// quoteYAMLString renders a value the same way internal/docsindex writes
// frontmatter, so a regenerated file and a --refresh-frontmatter run produce
// the same bytes.
func quoteYAMLString(s string) string {
	raw, _ := json.Marshal(s)
	return string(raw)
}

// findLastTopLevelHeading returns the byte offset of the last line that is
// exactly the given heading. Exact, not prefix: internal/docslint requires the
// literal "## Related", so a near-miss spelling has to fail the lint rather
// than quietly change where capgen writes.
func findLastTopLevelHeading(content, heading string) int {
	found := -1
	offset := 0
	for _, line := range strings.SplitAfter(content, "\n") {
		if strings.TrimRight(line, "\r\n") == heading {
			found = offset
		}
		offset += len(line)
	}
	return found
}

// displayWidth returns the visible display width of a string, matching how
// Prettier (via the string-width npm package) measures markdown table cells.
// Emoji are counted as width 2; variation selectors (U+FE0F) as 0; ASCII as 1.
func displayWidth(s string) int {
	w := 0
	for _, r := range s {
		switch {
		case r == 0xFE0F: // variation selector-16 — zero width
		case r >= 0x1F000: // supplementary emoji (e.g. 🚧)
			w += 2
		case r >= 0x2600 && r <= 0x27FF: // misc symbols & dingbats (e.g. ✅ ⚠ ❌)
			w += 2
		default:
			w++
		}
	}
	return w
}

// escapeMDCell escapes characters that would break a GFM table cell.
// Pipe characters are backslash-escaped so that inline markdown formatting
// (including backtick code spans) can be used freely in notes without
// disrupting the column structure.
func escapeMDCell(s string) string {
	return strings.ReplaceAll(s, "|", `\|`)
}

// formatTable renders a markdown table with Prettier-style column alignment.
// headers is a slice of column header strings; rows is a slice of rows (each a slice of cell strings).
// All columns are padded to the display width of their widest cell.
func formatTable(headers []string, rows [][]string) string {
	widths := make([]int, len(headers))
	for i, h := range headers {
		widths[i] = displayWidth(h)
	}
	for _, row := range rows {
		for i, cell := range row {
			if i < len(widths) {
				if dw := displayWidth(escapeMDCell(cell)); dw > widths[i] {
					widths[i] = dw
				}
			}
		}
	}

	pad := func(s string, w int) string {
		return s + strings.Repeat(" ", w-displayWidth(s))
	}

	var sb strings.Builder

	// Header row.
	sb.WriteString("|")
	for i, h := range headers {
		sb.WriteString(" " + pad(h, widths[i]) + " |")
	}
	sb.WriteString("\n")

	// Separator row.
	sb.WriteString("|")
	for _, w := range widths {
		sb.WriteString(" " + strings.Repeat("-", w) + " |")
	}
	sb.WriteString("\n")

	// Data rows.
	for _, row := range rows {
		sb.WriteString("|")
		for i := range headers {
			cell := ""
			if i < len(row) {
				cell = escapeMDCell(row[i])
			}
			sb.WriteString(" " + pad(cell, widths[i]) + " |")
		}
		sb.WriteString("\n")
	}

	return sb.String()
}

// buildDocSection generates the markdown table section for a service.
func buildDocSection(service string, caps []CapabilityDecl) string {
	docsBase := serviceDocsBase(service)

	// Collect ordered unique categories preserving declaration order.
	catOrder := []string{}
	catSeen := map[string]struct{}{}
	byCat := map[string][]CapabilityDecl{}
	var extensions []CapabilityDecl
	for _, c := range caps {
		if c.EmulatorOnly {
			extensions = append(extensions, c)
			continue
		}
		cat := c.Category
		if cat == "" {
			cat = "Operations"
		}
		if _, ok := catSeen[cat]; !ok {
			catSeen[cat] = struct{}{}
			catOrder = append(catOrder, cat)
		}
		byCat[cat] = append(byCat[cat], c)
	}

	var buf bytes.Buffer

	// Summary table.
	buf.WriteString("\n## Summary\n\n")
	allSummaryHeaders := []string{"Category", "✅ Supported", "🧊 Inert", "⚠️ Partial", "🚧 WIP", "❌ Unsupported"}
	statusKeys := []string{"StatusSupported", "StatusInert", "StatusPartial", "StatusWIP", "StatusUnsupported"}
	// Accumulate raw counts per row so we can detect all-zero columns.
	type summaryCount struct {
		cat    string
		counts [5]int
	}
	rawSummary := make([]summaryCount, 0, len(catOrder))
	colTotals := [5]int{}
	for _, cat := range catOrder {
		var sc summaryCount
		sc.cat = cat
		for _, c := range byCat[cat] {
			for i, k := range statusKeys {
				if c.Status == k {
					sc.counts[i]++
					colTotals[i]++
				}
			}
		}
		rawSummary = append(rawSummary, sc)
	}
	// Build filtered headers and rows, skipping all-zero columns and blank-ing zero cells.
	summaryHeaders := []string{allSummaryHeaders[0]}
	activeColIdx := []int{}
	for i := range statusKeys {
		if colTotals[i] > 0 {
			summaryHeaders = append(summaryHeaders, allSummaryHeaders[i+1])
			activeColIdx = append(activeColIdx, i)
		}
	}
	summaryRows := make([][]string, 0, len(rawSummary))
	for _, sc := range rawSummary {
		row := []string{sc.cat}
		for _, i := range activeColIdx {
			if sc.counts[i] == 0 {
				row = append(row, "")
			} else {
				row = append(row, fmt.Sprintf("%d", sc.counts[i]))
			}
		}
		summaryRows = append(summaryRows, row)
	}
	buf.WriteString(formatTable(summaryHeaders, summaryRows))

	buf.WriteString("\n---\n\n## Endpoints\n")

	// Endpoints tables per category.
	endpointHeaders := []string{"Operation", "Status", "Notes", "AWS Docs"}
	for _, cat := range catOrder {
		buf.WriteString(fmt.Sprintf("\n### %s\n\n", cat))
		rows := make([][]string, 0, len(byCat[cat]))
		for _, c := range byCat[cat] {
			status := statusLabel(c.Status)
			docsURL := c.DocsURL
			if docsURL == "" && docsBase != "" {
				docsURL = fmt.Sprintf("[docs](%s%s.html)", docsBase, c.Operation)
			}
			displayOp := c.Operation
			if c.DisplayName != "" {
				displayOp = c.DisplayName
			}
			rows = append(rows, []string{"`" + displayOp + "`", status, c.Notes, docsURL})
		}
		buf.WriteString(formatTable(endpointHeaders, rows))
	}

	buf.WriteString(buildEmulatorExtensions(extensions))

	buf.WriteString("\n")
	return buf.String()
}

// emulatorSectionHeading names the operations page's second table: the rows
// that are Overcast's own rather than AWS's.
const emulatorSectionHeading = "## Emulator extensions"

// buildEmulatorExtensions renders the EmulatorOnly rows under their own
// heading, or nothing when a service has none.
//
// They keep a table rather than being dropped, because they are behaviour a
// reader of the service page has to be able to find — CloudFront's proxy is
// most of what makes the service useful locally. What they lose is the AWS
// Docs column: the default cell is built from the operation name, so this row
// published a link to API_ProxyRequest.html, a page that has never existed. A
// reference is rendered only when the declaration states one, as EKS's
// UpdateKubeconfig states the CLI command's page.
func buildEmulatorExtensions(extensions []CapabilityDecl) string {
	if len(extensions) == 0 {
		return ""
	}

	var buf bytes.Buffer
	buf.WriteString("\n---\n\n" + emulatorSectionHeading + "\n\n")
	buf.WriteString("Operations Overcast serves that appear in no AWS model, so no SDK calls them and no AWS reference page describes them. They sit outside the operation counts above.\n\n")

	headers := []string{"Operation", "Status", "Notes"}
	referenced := false
	for _, c := range extensions {
		if c.DocsURL != "" {
			referenced = true
			break
		}
	}
	if referenced {
		headers = append(headers, "Reference")
	}

	rows := make([][]string, 0, len(extensions))
	for _, c := range extensions {
		displayOp := c.Operation
		if c.DisplayName != "" {
			displayOp = c.DisplayName
		}
		row := []string{"`" + displayOp + "`", statusLabel(c.Status), c.Notes}
		if referenced {
			row = append(row, c.DocsURL)
		}
		rows = append(rows, row)
	}
	buf.WriteString(formatTable(headers, rows))
	return buf.String()
}

func statusLabel(s string) string {
	switch s {
	case "StatusSupported":
		return "✅ Supported"
	case "StatusInert":
		return "🧊 Inert"
	case "StatusPartial":
		return "⚠️ Partial"
	case "StatusWIP":
		return "🚧 WIP"
	default:
		return "❌ Unsupported"
	}
}

// serviceDocsBase returns the AWS API docs URL base for a service (up to the operation name).
var serviceDocsBaseMap = map[string]string{
	"acm":             "https://docs.aws.amazon.com/acm/latest/APIReference/API_",
	"apigateway":      "https://docs.aws.amazon.com/apigateway/latest/api/API_",
	"appconfig":       "https://docs.aws.amazon.com/appconfig/2019-10-09/APIReference/API_",
	"appconfigdata":   "https://docs.aws.amazon.com/appconfig/2019-10-09/APIReference/API_",
	"appregistry":     "https://docs.aws.amazon.com/servicecatalog/latest/dg/API_app-registry_",
	"appsync":         "https://docs.aws.amazon.com/appsync/latest/APIReference/API_",
	"athena":          "https://docs.aws.amazon.com/athena/latest/APIReference/API_",
	"backup":          "https://docs.aws.amazon.com/aws-backup/latest/devguide/API_",
	"bedrock":         "https://docs.aws.amazon.com/bedrock/latest/APIReference/API_",
	"cloudformation":  "https://docs.aws.amazon.com/AWSCloudFormation/latest/APIReference/API_",
	"cloudfront":      "https://docs.aws.amazon.com/cloudfront/latest/APIReference/API_",
	"cloudtrail":      "https://docs.aws.amazon.com/awscloudtrail/latest/APIReference/API_",
	"cloudwatch":      "https://docs.aws.amazon.com/AmazonCloudWatch/latest/APIReference/API_",
	"cloudwatch-logs": "https://docs.aws.amazon.com/AmazonCloudWatchLogs/latest/APIReference/API_",
	"cognito":         "https://docs.aws.amazon.com/cognito-user-identity-pools/latest/APIReference/API_",
	"dynamodb":        "https://docs.aws.amazon.com/amazondynamodb/latest/APIReference/API_",
	"dynamodbstreams": "https://docs.aws.amazon.com/amazondynamodb/latest/APIReference/API_",
	"ec2":             "https://docs.aws.amazon.com/AWSEC2/latest/APIReference/API_",
	"ecr":             "https://docs.aws.amazon.com/AmazonECR/latest/APIReference/API_",
	"ecs":             "https://docs.aws.amazon.com/AmazonECS/latest/APIReference/API_",
	"efs":             "https://docs.aws.amazon.com/efs/latest/ug/API_",
	"eks":             "https://docs.aws.amazon.com/eks/latest/APIReference/API_",
	"elasticache":     "https://docs.aws.amazon.com/AmazonElastiCache/latest/APIReference/API_",
	"elbv2":           "https://docs.aws.amazon.com/elasticloadbalancing/latest/APIReference/API_",
	"eventbridge":     "https://docs.aws.amazon.com/eventbridge/latest/APIReference/API_",
	"firehose":        "https://docs.aws.amazon.com/firehose/latest/APIReference/API_",
	"glue":            "https://docs.aws.amazon.com/glue/latest/dg/aws-glue-api-",
	"iam":             "https://docs.aws.amazon.com/IAM/latest/APIReference/API_",
	"kinesis":         "https://docs.aws.amazon.com/kinesis/latest/APIReference/API_",
	"kms":             "https://docs.aws.amazon.com/kms/latest/APIReference/API_",
	"lambda":          "https://docs.aws.amazon.com/lambda/latest/dg/API_",
	"msk":             "https://docs.aws.amazon.com/msk/latest/developerguide/API_",
	"opensearch":      "https://docs.aws.amazon.com/opensearch-service/latest/APIReference/API_",
	"organizations":   "https://docs.aws.amazon.com/organizations/latest/APIReference/API_",
	"pipes":           "https://docs.aws.amazon.com/eventbridge/latest/pipes-reference/API_",
	"rds":             "https://docs.aws.amazon.com/AmazonRDS/latest/APIReference/API_",
	"route53":         "https://docs.aws.amazon.com/Route53/latest/APIReference/API_",
	"s3":              "https://docs.aws.amazon.com/AmazonS3/latest/API/API_",
	"scheduler":       "https://docs.aws.amazon.com/scheduler/latest/APIReference/API_",
	"secretsmanager":  "https://docs.aws.amazon.com/secretsmanager/latest/apireference/API_",
	"ses":             "https://docs.aws.amazon.com/ses/latest/APIReference/API_",
	"shield":          "https://docs.aws.amazon.com/waf/latest/DDOSAPIReference/API_",
	"sns":             "https://docs.aws.amazon.com/sns/latest/api/API_",
	"sqs":             "https://docs.aws.amazon.com/AWSSimpleQueueService/latest/APIReference/API_",
	"ssm":             "https://docs.aws.amazon.com/systems-manager/latest/APIReference/API_",
	"stepfunctions":   "https://docs.aws.amazon.com/step-functions/latest/apireference/API_",
	"sts":             "https://docs.aws.amazon.com/STS/latest/APIReference/API_",
	"transfer":        "https://docs.aws.amazon.com/transfer/latest/userguide/API_",
	"waf":             "https://docs.aws.amazon.com/waf/latest/APIReference/API_",
}

func serviceDocsBase(service string) string {
	return serviceDocsBaseMap[service]
}

// statusDisplayNames maps service IDs to their display names as they appear in STATUS.md tables.
var statusDisplayNames = map[string]string{
	"acm":             "ACM",
	"apigateway":      "API Gateway",
	"appconfig":       "AppConfig",
	"appconfigdata":   "AppConfigData",
	"appregistry":     "AppRegistry",
	"appsync":         "AppSync",
	"athena":          "Athena",
	"autoscaling":     "Auto Scaling",
	"backup":          "Backup",
	"bedrock":         "Bedrock",
	"cloudformation":  "CloudFormation",
	"cloudfront":      "CloudFront",
	"cloudtrail":      "CloudTrail",
	"cloudwatch":      "CloudWatch",
	"cloudwatch-logs": "CloudWatch Logs",
	"cognito":         "Cognito",
	"dynamodb":        "DynamoDB",
	"dynamodbstreams": "DynamoDB Streams",
	"ec2":             "EC2 / VPC",
	"ecr":             "ECR",
	"ecs":             "ECS",
	"efs":             "EFS",
	"eks":             "EKS",
	"elasticache":     "ElastiCache",
	"elbv2":           "ELBv2",
	"eventbridge":     "EventBridge",
	"firehose":        "Firehose",
	"glue":            "Glue",
	"iam":             "IAM",
	"kinesis":         "Kinesis",
	"kms":             "KMS",
	"lambda":          "Lambda",
	"msk":             "MSK",
	"opensearch":      "OpenSearch",
	"organizations":   "Organizations",
	"pipes":           "Pipes",
	"rds":             "RDS",
	"route53":         "Route 53",
	"s3":              "S3",
	"scheduler":       "Scheduler",
	"secretsmanager":  "Secrets Manager",
	"ses":             "SES",
	"shield":          "Shield",
	"sns":             "SNS",
	"sqs":             "SQS",
	"ssm":             "SSM",
	"stepfunctions":   "Step Functions",
	"sts":             "STS",
	"transfer":        "Transfer Family",
	"waf":             "WAF v2",
}

// statusTableOrder defines the display order for the sentinel-generated op-count
// table. Mirrors the tier ordering of the hand-maintained STATUS.md tables.
var statusTableOrder = []string{
	"s3", "sqs", "dynamodb", "lambda", "apigateway", "appsync", "cloudfront",
	"cognito", "ec2", "sns", "stepfunctions",
	"iam", "ecs", "ecr", "kms", "kinesis", "eventbridge", "scheduler",
	"cloudformation", "rds", "elasticache", "efs", "appconfig", "appconfigdata",
	"secretsmanager", "ssm", "cloudwatch-logs", "ses", "sts", "route53", "autoscaling",
	"pipes", "waf", "shield", "acm", "athena", "bedrock",
	"cloudwatch", "dynamodbstreams", "firehose", "glue", "opensearch",
	"appregistry", "backup", "cloudtrail", "eks", "elbv2", "msk",
	"organizations", "transfer",
}

var serviceIndexTiers = map[string]string{
	"s3":              "Comprehensive / broad support",
	"sqs":             "Comprehensive / broad support",
	"dynamodb":        "Comprehensive / broad support",
	"lambda":          "Comprehensive / broad support",
	"apigateway":      "Comprehensive / broad support",
	"appsync":         "Comprehensive / broad support",
	"cloudfront":      "Comprehensive / broad support",
	"cognito":         "Comprehensive / broad support",
	"ec2":             "Comprehensive / broad support",
	"sns":             "Comprehensive / broad support",
	"stepfunctions":   "Comprehensive / broad support",
	"iam":             "Core CRUD + common workflows",
	"ecs":             "Core CRUD + common workflows",
	"ecr":             "Core CRUD + common workflows",
	"kms":             "Core CRUD + common workflows",
	"kinesis":         "Core CRUD + common workflows",
	"eventbridge":     "Core CRUD + common workflows",
	"scheduler":       "Core CRUD + common workflows",
	"cloudformation":  "Core CRUD + common workflows",
	"rds":             "Core CRUD + common workflows",
	"elasticache":     "Core CRUD + common workflows",
	"efs":             "Core CRUD + common workflows",
	"appconfig":       "Core CRUD + common workflows",
	"appconfigdata":   "Core CRUD + common workflows",
	"secretsmanager":  "Core CRUD + common workflows",
	"ssm":             "Core CRUD + common workflows",
	"cloudwatch-logs": "Core CRUD + common workflows",
	"ses":             "Core CRUD + common workflows",
	"sts":             "Core CRUD + common workflows",
	"route53":         "Core CRUD + common workflows",
	"autoscaling":     "Core CRUD + common workflows",
	"pipes":           "Minimal / targeted support",
	"waf":             "Minimal / targeted support",
	"shield":          "Minimal / targeted support",
	"acm":             "Minimal / targeted support",
	"athena":          "Minimal / targeted support",
	"bedrock":         "Minimal / targeted support",
	"cloudwatch":      "Minimal / targeted support",
	"dynamodbstreams": "Minimal / targeted support",
	"firehose":        "Minimal / targeted support",
	"glue":            "Minimal / targeted support",
	"opensearch":      "Minimal / targeted support",
	"appregistry":     "IaC/discovery-oriented stub",
	"backup":          "IaC/discovery-oriented stub",
	"cloudtrail":      "IaC/discovery-oriented stub",
	"eks":             "IaC/discovery-oriented stub",
	"elbv2":           "IaC/discovery-oriented stub",
	"msk":             "IaC/discovery-oriented stub",
	"organizations":   "IaC/discovery-oriented stub",
	"transfer":        "IaC/discovery-oriented stub",
}

var serviceDocFileNames = map[string]string{
	"elbv2": "elb",
}

// serviceConfigNames maps a capgen service key to the config service name when
// the two differ. capgen keys CloudWatch Logs by its documentation name;
// config.AllServices calls it "logs". Every other key is its own name, and
// validateServiceNames enforces that this stays true.
var serviceConfigNames = map[string]string{
	"cloudwatch-logs": "logs",
}

// serviceCDK describes a service in CDK terms, for the token tables in
// docs/configuration.md and docs/cdk.md. This is the one part of those tables with no
// derivable source in the repository, so it is declared here and rendered into
// both documents — the two can restate the mapping without drifting apart.
//
// validateServiceNames requires an entry for every service, so adding a
// service fails the generator until its CDK mapping is filled in.
type serviceCDK struct {
	// Modules are aws-cdk-lib submodule names ("aws-events"), most-used
	// first. Empty when the service has no construct module at all.
	Modules []string
	// NoModule explains an empty Modules, e.g. "used by the CDK CLI itself".
	// Rendered in place of the module list.
	NoModule string
}

var serviceCDKInfo = map[string]serviceCDK{
	"s3":              {Modules: []string{"aws-s3"}},
	"sqs":             {Modules: []string{"aws-sqs"}},
	"sns":             {Modules: []string{"aws-sns"}},
	"ses":             {Modules: []string{"aws-ses"}},
	"dynamodb":        {Modules: []string{"aws-dynamodb"}},
	"dynamodbstreams": {NoModule: "enabled by the `stream` prop on `aws-dynamodb`"},
	"lambda":          {Modules: []string{"aws-lambda"}},
	"pipes":           {Modules: []string{"aws-pipes"}},
	"cloudwatch-logs": {Modules: []string{"aws-logs"}},
	"secretsmanager":  {Modules: []string{"aws-secretsmanager"}},
	"sts":             {NoModule: "used by the CDK CLI itself"},
	"ssm":             {Modules: []string{"aws-ssm"}},
	"kms":             {Modules: []string{"aws-kms"}},
	"iam":             {Modules: []string{"aws-iam"}},
	"cloudformation":  {Modules: []string{"aws-cloudformation"}},
	"ec2":             {Modules: []string{"aws-ec2"}},
	"rds":             {Modules: []string{"aws-rds"}},
	"ecs":             {Modules: []string{"aws-ecs"}},
	"ecr":             {Modules: []string{"aws-ecr", "aws-ecr-assets"}},
	"efs":             {Modules: []string{"aws-efs"}},
	"eks":             {Modules: []string{"aws-eks"}},
	"cognito":         {Modules: []string{"aws-cognito"}},
	"stepfunctions":   {Modules: []string{"aws-stepfunctions", "aws-stepfunctions-tasks"}},
	"waf":             {Modules: []string{"aws-wafv2"}},
	"shield":          {Modules: []string{"aws-shield"}},
	"appsync":         {Modules: []string{"aws-appsync"}},
	"apigateway":      {Modules: []string{"aws-apigateway", "aws-apigatewayv2"}},
	"cloudfront":      {Modules: []string{"aws-cloudfront", "aws-cloudfront-origins"}},
	"eventbridge":     {Modules: []string{"aws-events", "aws-events-targets"}},
	"kinesis":         {Modules: []string{"aws-kinesis"}},
	"appregistry":     {Modules: []string{"aws-servicecatalogappregistry"}},
	"cloudwatch":      {Modules: []string{"aws-cloudwatch", "aws-cloudwatch-actions"}},
	"acm":             {Modules: []string{"aws-certificatemanager"}},
	"opensearch":      {Modules: []string{"aws-opensearchservice"}},
	"appconfig":       {Modules: []string{"aws-appconfig"}},
	"appconfigdata":   {NoModule: "runtime data plane; no constructs"},
	"bedrock":         {Modules: []string{"aws-bedrock"}},
	"glue":            {Modules: []string{"aws-glue"}},
	"firehose":        {Modules: []string{"aws-kinesisfirehose"}},
	"athena":          {Modules: []string{"aws-athena"}},
	"elasticache":     {Modules: []string{"aws-elasticache"}},
	"msk":             {Modules: []string{"aws-msk"}},
	"scheduler":       {Modules: []string{"aws-scheduler"}},
	"route53":         {Modules: []string{"aws-route53", "aws-route53-targets"}},
	"elbv2":           {Modules: []string{"aws-elasticloadbalancingv2"}},
	"organizations":   {NoModule: "no constructs"},
	"autoscaling":     {Modules: []string{"aws-autoscaling", "aws-applicationautoscaling"}},
	"cloudtrail":      {Modules: []string{"aws-cloudtrail"}},
	"backup":          {Modules: []string{"aws-backup"}},
	"transfer":        {Modules: []string{"aws-transfer"}},
}

type serviceLink struct {
	name string
	link string
}

// awsOperationCounts counts, per service, the AWS API operations it declares.
//
// It is what every published "Ops" figure is: STATUS.md's table, the docs
// service index, and service-support.json. An EmulatorOnly row is Overcast's
// own operation and is not one of them, but its service still belongs in the
// list — so a service is keyed here whether or not any of its rows count,
// rather than disappearing from the index if all of them are extensions.
func awsOperationCounts(allCaps []CapabilityDecl) map[string]int {
	counts := map[string]int{}
	for _, c := range allCaps {
		if _, seen := counts[c.Service]; !seen {
			counts[c.Service] = 0
		}
		if c.EmulatorOnly {
			continue
		}
		counts[c.Service]++
	}
	return counts
}

// updateStatusMd keeps STATUS.md op counts consistent with the capability
// registry. It does two things:
//
//  1. Inline-patches the "Ops" column in the existing hand-maintained tables
//     so the displayed tier-grouped rows stay current.
//
//  2. Replaces the content between <!-- BEGIN overcast:status --> and
//     <!-- END overcast:status --> with a freshly generated flat table for
//     direct comparison against the hand-maintained counts.
//
// Returns true if the file was modified.
func updateStatusMd(root string, allCaps []CapabilityDecl) (bool, error) {
	const beginMarker = "<!-- BEGIN overcast:status -->"
	const endMarker = "<!-- END overcast:status -->"

	opCounts := awsOperationCounts(allCaps)

	// Build reverse map: lower-cased display name → service ID.
	nameToID := make(map[string]string, len(statusDisplayNames))
	for id, name := range statusDisplayNames {
		nameToID[strings.ToLower(strings.TrimSpace(name))] = id
	}

	statusPath := filepath.Join(root, "STATUS.md")
	raw, err := os.ReadFile(statusPath)
	if err != nil {
		return false, err
	}

	lines := strings.Split(string(raw), "\n")
	changed := false

	// Part 1: inline-patch Ops cells in the existing hand-maintained tables.
	for i, line := range lines {
		if !strings.HasPrefix(line, "|") {
			continue
		}
		// Split on "|" — at least 4 parts for "| svc | ops | highlights |"
		parts := strings.Split(line, "|")
		if len(parts) < 4 {
			continue
		}
		svcCell := strings.TrimSpace(parts[1])
		if svcCell == "" || svcCell == "Service" || strings.Contains(svcCell, "---") {
			continue
		}
		svcID, ok := nameToID[strings.ToLower(svcCell)]
		if !ok {
			continue
		}
		count, ok := opCounts[svcID]
		if !ok {
			continue
		}
		// Replace ops cell (parts[2]) with the new count.
		oldOps := strings.TrimSpace(parts[2])
		newOps := fmt.Sprintf("%d", count)
		if oldOps == newOps {
			continue
		}
		// Preserve original cell width padding.
		parts[2] = fmt.Sprintf(" %-3s ", newOps)
		lines[i] = strings.Join(parts, "|")
		changed = true
	}

	// Part 2: replace sentinel section with a flat generated table.
	content := strings.Join(lines, "\n")
	serviceCountLine := fmt.Sprintf("%d AWS services are registered. Coverage varies from comprehensive to stub.", len(opCounts))
	content = regexpMustReplace(content, `(?m)^\d+ AWS services are registered\. Coverage varies from comprehensive to stub\.$`, serviceCountLine, &changed)
	beginIdx := strings.Index(content, beginMarker)
	endIdx := strings.Index(content, endMarker)
	if beginIdx >= 0 && endIdx > beginIdx {
		var buf strings.Builder
		buf.WriteString(beginMarker + "\n\n")
		buf.WriteString("| Service         | Ops |\n")
		buf.WriteString("| --------------- | --- |\n")
		ordered := orderedServices(opCounts)
		for _, svcID := range ordered {
			name := statusDisplayNames[svcID]
			if name == "" {
				name = svcID
			}
			buf.WriteString(fmt.Sprintf("| %-15s | %-3d |\n", name, opCounts[svcID]))
		}
		buf.WriteString("\n" + endMarker)
		replacement := buf.String()
		oldSection := content[beginIdx : endIdx+len(endMarker)]
		if replacement != oldSection {
			content = content[:beginIdx] + replacement + content[endIdx+len(endMarker):]
			changed = true
		}
	}

	if !changed {
		return false, nil
	}
	return true, os.WriteFile(statusPath, []byte(content), 0o644)
}

// orderedServices returns service IDs in statusTableOrder first, followed by
// any remaining service IDs sorted alphabetically.
func orderedServices(opCounts map[string]int) []string {
	inOrder := make(map[string]bool, len(statusTableOrder))
	for _, s := range statusTableOrder {
		inOrder[s] = true
	}

	ordered := make([]string, 0, len(opCounts))
	for _, s := range statusTableOrder {
		if _, ok := opCounts[s]; ok {
			ordered = append(ordered, s)
		}
	}

	remainder := make([]string, 0, len(opCounts))
	for s := range opCounts {
		if !inOrder[s] {
			remainder = append(remainder, s)
		}
	}
	sort.Strings(remainder)

	return append(ordered, remainder...)
}

func regexpMustReplace(content, pattern, replacement string, changed *bool) string {
	re := regexp.MustCompile(pattern)
	next := re.ReplaceAllString(content, replacement)
	if next != content {
		*changed = true
	}
	return next
}

func updateDocsReadmeServiceIndex(root string, allCaps []CapabilityDecl) (bool, error) {
	opCounts := awsOperationCounts(allCaps)

	rows := make([][]string, 0, len(opCounts))
	for _, svc := range orderedServices(opCounts) {
		name := statusDisplayNames[svc]
		if name == "" {
			name = svc
		}
		docFile := serviceDocFile(svc)
		tier := serviceIndexTiers[svc]
		if tier == "" {
			tier = "See service doc"
		}
		rows = append(rows, []string{
			name,
			fmt.Sprintf("[%s.md](./services/%s.md)", docFile, docFile),
			fmt.Sprintf("%d", opCounts[svc]),
			tier,
		})
	}

	return replaceMarkedSection(
		filepath.Join(root, "docs", "README.md"),
		"<!-- BEGIN overcast:service-index -->",
		"<!-- END overcast:service-index -->",
		formatTable([]string{"Service", "Doc", "Ops", "Coverage tier"}, rows),
	)
}

// replaceMarkedSection rewrites the sentinel-bracketed block in path with body,
// reporting whether the file changed.
func replaceMarkedSection(path, beginMarker, endMarker, body string) (bool, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	content := string(raw)
	beginIdx := strings.Index(content, beginMarker)
	endIdx := strings.Index(content, endMarker)
	if beginIdx < 0 || endIdx <= beginIdx {
		return false, fmt.Errorf("%s: missing %s/%s markers", filepath.Base(path), beginMarker, endMarker)
	}

	replacement := beginMarker + "\n\n" + body + "\n" + endMarker
	if replacement == content[beginIdx:endIdx+len(endMarker)] {
		return false, nil
	}
	content = content[:beginIdx] + replacement + content[endIdx+len(endMarker):]
	return true, os.WriteFile(path, []byte(content), 0o644)
}

// serviceConfigName returns the config service name for a capgen service
// key. The two are the same for all but CloudWatch Logs; see
// serviceConfigNames.
func serviceConfigName(service string) string {
	if token, ok := serviceConfigNames[service]; ok {
		return token
	}
	return service
}

// validateServiceNames checks capgen's service list against the services that
// actually exist, and requires a CDK mapping for each.
//
// This is what makes the generated token tables trustworthy. Without it, a
// service added to config.allServices would simply be absent from the
// documented list, and a service renamed there would leave a stale row behind
// — in both cases silently, because generation would still succeed.
func validateServiceNames() []string {
	var problems []string

	accepted := map[string]bool{}
	for _, name := range config.AllServices() {
		accepted[name] = true
	}

	claimed := map[string]bool{}
	for _, svc := range statusTableOrder {
		name := serviceConfigName(svc)
		if !accepted[name] {
			problems = append(problems, fmt.Sprintf("service %q maps to token %q, which is not a known service", svc, name))
			continue
		}
		if claimed[name] {
			problems = append(problems, fmt.Sprintf("token %q is claimed by more than one service key", name))
		}
		claimed[name] = true

		info, ok := serviceCDKInfo[svc]
		if !ok {
			problems = append(problems, fmt.Sprintf("service %q has no serviceCDKInfo entry; add its aws-cdk-lib module, or a NoModule reason", svc))
			continue
		}
		if len(info.Modules) == 0 && info.NoModule == "" {
			problems = append(problems, fmt.Sprintf("service %q has neither Modules nor a NoModule reason", svc))
		}
		if len(info.Modules) > 0 && info.NoModule != "" {
			problems = append(problems, fmt.Sprintf("service %q sets both Modules and NoModule; NoModule is only for services with no construct module", svc))
		}
	}

	for _, name := range config.AllServices() {
		if !claimed[name] {
			problems = append(problems, fmt.Sprintf("service %q has no capgen service key; add it to statusTableOrder and statusDisplayNames", name))
		}
	}

	sort.Strings(problems)
	return problems
}

// cdkModuleCell renders the CDK module column of the docs/README.md token
// table: a list of modules, or an em-dash and the reason there are none.
func cdkModuleCell(info serviceCDK) string {
	if len(info.Modules) == 0 {
		return "— (" + info.NoModule + ")"
	}
	quoted := make([]string, len(info.Modules))
	for i, m := range info.Modules {
		quoted[i] = "`" + m + "`"
	}
	return strings.Join(quoted, ", ")
}

// updateConfigurationServiceNames regenerates the service-name table in
// docs/configuration.md (moved off docs/README.md when the reference
// monolith was split into standalone pages — see docs/README.md's own
// "Contents" index).
func updateConfigurationServiceNames(root string) (bool, error) {
	rows := make([][]string, 0, len(statusTableOrder))
	for _, svc := range statusTableOrder {
		name := statusDisplayNames[svc]
		if name == "" {
			name = svc
		}
		rows = append(rows, []string{
			"`" + serviceConfigName(svc) + "`",
			name,
			cdkModuleCell(serviceCDKInfo[svc]),
		})
	}

	return replaceMarkedSection(
		filepath.Join(root, "docs", "configuration.md"),
		"<!-- BEGIN overcast:service-names -->",
		"<!-- END overcast:service-names -->",
		formatTable([]string{"Name", "Service", "CDK module (`aws-cdk-lib/…`)"}, rows),
	)
}

func updateRootReadmeServiceList(root string, allCaps []CapabilityDecl) (bool, error) {
	const beginMarker = "<!-- BEGIN overcast:root-service-list -->"
	const endMarker = "<!-- END overcast:root-service-list -->"

	opCounts := awsOperationCounts(allCaps)

	path := filepath.Join(root, "README.md")
	raw, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	content := string(raw)
	beginIdx := strings.Index(content, beginMarker)
	endIdx := strings.Index(content, endMarker)
	if beginIdx < 0 || endIdx <= beginIdx {
		return false, fmt.Errorf("missing %s/%s markers", beginMarker, endMarker)
	}

	links := make([]serviceLink, 0, len(opCounts))
	for svc := range opCounts {
		name := statusDisplayNames[svc]
		if name == "" {
			name = svc
		}
		docFile := serviceDocFile(svc)
		links = append(links, serviceLink{
			name: name,
			link: fmt.Sprintf("[%s](./docs/services/%s.md)", name, docFile),
		})
	}
	sort.Slice(links, func(i, j int) bool {
		return strings.ToLower(links[i].name) < strings.ToLower(links[j].name)
	})

	var buf strings.Builder
	buf.WriteString(beginMarker + "\n\n")
	buf.WriteString(fmt.Sprintf("Overcast currently registers **%d AWS services**. Coverage ranges from broad\n", len(opCounts)))
	buf.WriteString("service emulation to minimal discovery/IaC stubs; check the per-service docs for\n")
	buf.WriteString("exact endpoint support.\n\n")
	buf.WriteString(formatCommaSeparatedLinks(links))
	buf.WriteString(".\n\n")
	buf.WriteString("Some services require Docker socket access for full runtime behaviour:\n\n")
	buf.WriteString("- Lambda, ECS, RDS, EC2/VPC, and ElastiCache can launch sibling containers.\n")
	buf.WriteString("- Without Docker, their metadata/control-plane APIs still work where possible,\n")
	buf.WriteString("  but runtime execution falls back to metadata-only or stub behaviour.\n\n")
	buf.WriteString("IAM is implemented for local development and CloudFormation/CDK compatibility,\n")
	buf.WriteString("but IAM policies are not enforced as an authorization layer.\n\n")
	buf.WriteString("See the [service emulation reference](./docs/services/) for per-endpoint\n")
	buf.WriteString("coverage tables, or browse the generated summary in [STATUS.md](./STATUS.md#service-coverage).\n\n")
	buf.WriteString(endMarker)

	replacement := buf.String()
	oldSection := content[beginIdx : endIdx+len(endMarker)]
	if replacement == oldSection {
		return false, nil
	}
	content = content[:beginIdx] + replacement + content[endIdx+len(endMarker):]
	return true, os.WriteFile(path, []byte(content), 0o644)
}

func formatCommaSeparatedLinks(links []serviceLink) string {
	const perLine = 4
	var buf strings.Builder
	for i, link := range links {
		if i > 0 {
			if i%perLine == 0 {
				buf.WriteString(",\n")
			} else {
				buf.WriteString(", ")
			}
		}
		buf.WriteString(link.link)
	}
	return buf.String()
}

func serviceDocFile(service string) string {
	if override := serviceDocFileNames[service]; override != "" {
		return override
	}
	return service
}

// generateServiceSupportJSON writes docs/generated/service-support.json, a
// machine-readable aggregate of all declared capabilities grouped by service.
// The file is intended for consumption by the web UI, CI checks, and MCP tools.
// It is regenerated by `capgen --write-docs` and checked by `make docs-check`.
func generateServiceSupportJSON(root string, allCaps []CapabilityDecl) error {
	type opEntry struct {
		Operation string `json:"operation"`
		Category  string `json:"category,omitempty"`
		Status    string `json:"status"`
		Notes     string `json:"notes,omitempty"`
		DocsURL   string `json:"docs_url,omitempty"`
		DocOnly   bool   `json:"doc_only,omitempty"`
		// EmulatorOnly marks an Overcast extension. It is excluded from this
		// service's total_ops and implemented_ops, which count AWS operations.
		EmulatorOnly bool `json:"emulator_only,omitempty"`
	}
	type svcEntry struct {
		Service        string    `json:"service"`
		DisplayName    string    `json:"display_name,omitempty"`
		TotalOps       int       `json:"total_ops"`
		ImplementedOps int       `json:"implemented_ops"`
		Operations     []opEntry `json:"operations"`
	}
	type manifest struct {
		GeneratedBy string     `json:"generated_by"`
		TotalOps    int        `json:"total_ops"`
		Services    []svcEntry `json:"services"`
	}

	// Group by service in statusTableOrder, then alphabetical remainder.
	inOrder := make(map[string]bool, len(statusTableOrder))
	for _, s := range statusTableOrder {
		inOrder[s] = true
	}
	// Build unique service list: ordered first, then remainder sorted.
	allSvcs := make(map[string]bool)
	for _, c := range allCaps {
		allSvcs[c.Service] = true
	}
	ordered := make([]string, 0, len(allSvcs))
	for _, s := range statusTableOrder {
		if allSvcs[s] {
			ordered = append(ordered, s)
		}
	}
	prefixLen := len(ordered)
	for svc := range allSvcs {
		if !inOrder[svc] {
			ordered = append(ordered, svc)
		}
	}
	sort.Slice(ordered[prefixLen:], func(i, j int) bool {
		base := prefixLen
		return ordered[base+i] < ordered[base+j]
	})

	svcs := make([]svcEntry, 0, len(ordered))
	for _, svc := range ordered {
		var ops []opEntry
		total, implemented := 0, 0
		for _, c := range allCaps {
			if c.Service != svc {
				continue
			}
			status := statusLabel(c.Status)
			ops = append(ops, opEntry{
				Operation:    c.Operation,
				Category:     c.Category,
				Status:       status,
				Notes:        c.Notes,
				DocsURL:      c.DocsURL,
				DocOnly:      c.DocOnly,
				EmulatorOnly: c.EmulatorOnly,
			})
			if c.EmulatorOnly {
				continue
			}
			total++
			if c.Status != "StatusUnsupported" {
				implemented++
			}
		}
		svcs = append(svcs, svcEntry{
			Service:        svc,
			DisplayName:    statusDisplayNames[svc],
			TotalOps:       total,
			ImplementedOps: implemented,
			Operations:     ops,
		})
	}

	totalOps := 0
	for _, svc := range svcs {
		totalOps += svc.TotalOps
	}

	m := manifest{
		GeneratedBy: "go run -tags dev ./cmd/capgen --write-docs",
		TotalOps:    totalOps,
		Services:    svcs,
	}

	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')

	outDir := filepath.Join(root, "docs", "generated")
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(outDir, "service-support.json"), data, 0o644)
}
