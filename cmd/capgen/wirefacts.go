//go:build dev

package main

import (
	"encoding/json"
	"fmt"
	"go/ast"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/overcast-sh/overcast/internal/awsapi"
)

// This file is the second half of #864: the manifest as the source of truth for
// the wire facts a service states about itself, rather than only for the routes
// it registers.
//
// #876 made the *route table* answerable to the model — a REST binding has to be
// registered at the method and URI AWS binds it to, and every registered path has
// to be one AWS models or live under /_overcast/. What it left untouched is the
// set of hand-typed strings a service package writes down that restate the same
// modeled facts in a different place:
//
//   - TargetPrefix() — the X-Amz-Target prefix a JSON service dispatches on.
//     #815 is what an invented one costs: AWS Backup registered no chi routes at
//     all and answered only on an `AWSBackup.` prefix the models never mention,
//     so every SDK got a 501 and every Overcast-internal test passed.
//   - PathPrefixes() — the path space a service claims. Every one of #793, #854,
//     #855, #856 and #857 served a real service under an invented prefix
//     (/_scheduler, /_appconfig, /_appconfigdata, /_opensearch, /_bedrock), and
//     #859 under a plausible-but-wrong one (/v2/clusters where AWS models
//     /api/v2/clusters).
//   - DocOnly rows — #862's mechanism. A DocOnly row is exempt from the model
//     name check, so an operation name that AWS does not model, or models under
//     a different service, is invisible. #863 made DocOnly an assertion about
//     dispatch; this makes it an assertion about the *name* too.
//   - The exemption table itself. #864 states the rule this enforces: "an
//     exemption added to make a gate green is precisely how the current
//     situation arose". An exemption whose reason has stopped being true is not
//     neutral — it silently pre-approves whatever is declared under that key
//     next.
//
// Every check here reads the pinned manifest. None of them needs a network, a
// running emulator or a route table, so they cost a directory walk and a scan.

// unmodeledTargetPrefixes is the ratchet for services that dispatch on an
// X-Amz-Target prefix the pinned models do not give them.
//
// A REST-modeled service — AWS declares restJson1 for it and no `awsJson1_x`
// target prefix anywhere — that nevertheless answered `POST /` for
// `<Prefix><Operation>` was an accepted wire real AWS rejects, and the exact
// mechanism of #815: a service whose only reachable dispatch was a target
// prefix nothing modeled, with its modeled REST paths unregistered.
//
// #1226 closed the four entries this ledger used to carry (AppRegistry, EFS,
// EKS, Scheduler): each registered its modeled REST routes already, so the
// prefix was redundant surface rather than the whole service, and each has
// been retired along with the internal callers (CloudFormation's own
// provisioners) that used to speak it. The ledger is empty rather than
// deleted so a new unmodeled prefix still has somewhere to be recorded on its
// way to being fixed, and so the ratchet's stale-entry direction keeps
// proving the four that are gone stay gone.
//
// Like every ledger in this repo it is a ratchet in both directions: a prefix
// absent from here fails the build, and so does an entry whose service has
// stopped declaring an unmodeled prefix.
var unmodeledTargetPrefixes = map[string]string{}

// docOnlyRowsOutsideTheModel records the DocOnly rows whose Operation reads like
// an AWS operation name and is not one for that service.
//
// It is not a fault list — it is the honest margin of the check, the same role
// weaklyServedBindings plays for the binding gate. DocOnly exists so the support
// matrix can document behaviour that is not an operation, and most such rows say
// so in their own spelling (`Fn::ImportValue`, `{{resolve:ssm}}`, `SMS publish`,
// `AWS::ServiceCatalogAppRegistry::Application`). The rows here are the ones a
// reader — or a checker — would take for an operation name, so each states what
// it really is.
//
// The check exists because DocOnly's other use is the one #862 was hiding in:
// six SESv2 rows carried the flag purely to silence a name mismatch, which also
// removed them from the dispatch cross-check and the reachability probe. A row
// added here has to say why the name is not an operation; it cannot simply be a
// name nobody checked.
var docOnlyRowsOutsideTheModel = map[string]string{
	// AppSync's VTL resolver operations. These are the `operation` field of a
	// DynamoDB request-mapping template, not AppSync API operations — AppSync
	// interprets them and issues the DynamoDB call itself.
	"appsync/BatchGetItem":       "AppSync DynamoDB resolver template operation, not an AppSync API operation",
	"appsync/BatchWriteItem":     "AppSync DynamoDB resolver template operation, not an AppSync API operation",
	"appsync/ConditionCheck":     "AppSync DynamoDB resolver template operation, not an AppSync API operation",
	"appsync/DeleteItem":         "AppSync DynamoDB resolver template operation, not an AppSync API operation",
	"appsync/GetItem":            "AppSync DynamoDB resolver template operation, not an AppSync API operation",
	"appsync/PutItem":            "AppSync DynamoDB resolver template operation, not an AppSync API operation",
	"appsync/Query":              "AppSync DynamoDB resolver template operation, not an AppSync API operation",
	"appsync/Scan":               "AppSync DynamoDB resolver template operation, not an AppSync API operation",
	"appsync/TransactGetItems":   "AppSync DynamoDB resolver template operation, not an AppSync API operation",
	"appsync/TransactWriteItems": "AppSync DynamoDB resolver template operation, not an AppSync API operation",
	"appsync/UpdateItem":         "AppSync DynamoDB resolver template operation, not an AppSync API operation",
	"dynamodb/GetShardIterator":  "a DynamoDB Streams operation (dynamodbstreams/GetShardIterator), documented on the DynamoDB page because that is where a reader looks for it",
	"ses/V2Other":                "a catch-all row for the SESv2 operations with no stub, not an operation name",
	"sns/PublishToEndpoint":      "documents Publish addressed to a platform endpoint ARN; the operation is sns/Publish",
}

// operationShapedName matches the spelling of an AWS operation name: one
// PascalCase identifier and nothing else. A DocOnly row that does not match is
// documenting something that is plainly not an operation, and holding it to the
// model would reject rows doing exactly their job.
var operationShapedName = regexp.MustCompile(`^[A-Z][A-Za-z0-9]*$`)

// checkWireFactsAgainstTheModel runs the whole-repo half of --check-model.
//
// complete says whether services and caps describe the entire tree. Every check
// here is a ratchet, and a ratchet's second direction — "this ledger entry names
// a fault that no longer exists" — can only be judged against the whole set. Run
// under --service, the reverse direction would report every other service's
// ledger row as stale, so it is skipped and the forward direction still holds.
func checkWireFactsAgainstTheModel(root string, services []string, caps []CapabilityDecl, complete bool) int {
	violations := 0
	violations += checkTargetPrefixesAgainstTheModel(root, services, complete)
	violations += checkPathPrefixesAgainstTheModel(root, services)
	violations += checkDocOnlyRowsNameRealOperations(caps, complete)
	if complete {
		violations += checkModelExemptionsAreStillNeeded(root, caps)
	}
	return violations
}

// checkTargetPrefixesAgainstTheModel holds every TargetPrefix() to the prefix
// the pinned models give that service.
//
// The fault it answers is #815. AWS Backup is a restJson1 service; Overcast
// dispatched it from an `AWSBackup.` X-Amz-Target prefix that appears in no
// model, registered no chi routes at all, and so answered every real SDK with a
// 501 while its own tests — which spoke the invented wire — passed. Nothing in
// the repo compared the string to the manifest, because nothing read the
// manifest's TargetPrefix column at all outside awsmodelgen's own tests.
func checkTargetPrefixesAgainstTheModel(root string, services []string, complete bool) int {
	modeled := modeledTargetPrefixes()

	violations := 0
	unmodeled := map[string]bool{}
	for _, service := range services {
		declared, ok, err := declaredWireStrings(serviceSourceDir(root, service), "TargetPrefix")
		if err != nil {
			violations += reportSourceReadFailure(service, "TargetPrefix", err)
			continue
		}
		if !ok {
			// The service has no target dispatch and states nothing here.
			continue
		}
		for _, prefix := range declared {
			// Pipes deliberately returns "" so the router does not claim
			// POST / on its behalf.
			if prefix == "" || modeled[service][prefix] {
				continue
			}
			unmodeled[service] = true
			if _, recorded := unmodeledTargetPrefixes[service]; recorded {
				continue
			}
			want := "the models give this service no X-Amz-Target prefix at all"
			if names := sortedSetKeys(modeled[service]); len(names) > 0 {
				want = "AWS models " + strings.Join(names, ", ")
			}
			fmt.Printf("TARGET_PREFIX_NOT_MODELED %s  (TargetPrefix() returns %q; %s — this is #815's shape: a wire AWS does not answer on)\n",
				service, prefix, want)
			violations++
		}
	}

	for service, reason := range unmodeledTargetPrefixes {
		if unmodeled[service] || !complete {
			continue
		}
		fmt.Printf("TARGET_PREFIX_LEDGER_STALE %s  (%s — the prefix now matches the model, or the service no longer declares one; delete the entry)\n",
			service, reason)
		violations++
	}
	return violations
}

// modeledTargetPrefixes indexes every X-Amz-Target prefix the manifest declares,
// by Overcast service key.
func modeledTargetPrefixes() map[string]map[string]bool {
	out := map[string]map[string]bool{}
	awsapi.WalkOperations(func(op awsapi.Operation) bool {
		if op.TargetPrefix == "" {
			return true
		}
		key := awsapi.ServiceKey(op.Service)
		if out[key] == nil {
			out[key] = map[string]bool{}
		}
		out[key][op.TargetPrefix] = true
		return true
	})
	return out
}

// checkPathPrefixesAgainstTheModel holds every PathPrefixes() to the URIs the
// pinned models bind for the same service.
//
// PathPrefixes is a service's own statement of the path space it owns: the
// router registers a 501 on each prefix when a subset test excludes the service,
// so a wrong prefix leaves the real paths falling into S3's wildcard and claims
// a path space that is not the service's. It is the same statement RegisterRoutes
// makes, written a second time by hand.
//
// The whole fault class is a service claiming a path space AWS does not give it:
// /_scheduler (#793), /_appconfig (#854), /_appconfigdata (#855), /_opensearch
// (#856), /_bedrock (#857) and /v2/clusters (#859). The route-side gates in
// internal/router catch a registered route; this catches the declaration, which
// is written before the routes are and is what a subset-registered router
// answers on.
func checkPathPrefixesAgainstTheModel(root string, services []string) int {
	modeled := modeledURIsByService()

	violations := 0
	for _, service := range services {
		declared, ok, err := declaredWireStrings(serviceSourceDir(root, service), "PathPrefixes")
		if err != nil {
			violations += reportSourceReadFailure(service, "PathPrefixes", err)
			continue
		}
		if !ok {
			continue
		}
		for _, prefix := range declared {
			if prefix == "" || prefixIsModeled(prefix, modeled[service]) {
				continue
			}
			fmt.Printf("PATH_PREFIX_NOT_MODELED %s  (PathPrefixes() claims %q; no URI the models bind for this service starts there — serve the operations where AWS binds them, or drop the claim)\n",
				service, prefix)
			violations++
		}
	}
	return violations
}

// reportSourceReadFailure turns an unreadable service package into a counted
// failure rather than a silent pass.
//
// A source-reading gate's worst failure is finding nothing: it looks exactly
// like a clean run. A directory that is not there is the one benign case —
// config.AllServices carries keys ahead of their package — and every other read
// error means the check did not run.
func reportSourceReadFailure(service, method string, err error) int {
	if os.IsNotExist(err) {
		return 0
	}
	fmt.Fprintf(os.Stderr, "capgen: %s: read %s: %v\n", service, method, err)
	return 1
}

// prefixIsModeled reports whether some modeled URI for the service lives under
// the claimed prefix. A prefix claims a subtree, so an exact match and a deeper
// path both count; "/2021-01-01" must not be satisfied by "/2021-01-0199".
func prefixIsModeled(prefix string, uris []string) bool {
	for _, uri := range uris {
		if uri == prefix || strings.HasPrefix(uri, prefix+"/") || strings.HasPrefix(uri, prefix+"?") {
			return true
		}
	}
	return false
}

func modeledURIsByService() map[string][]string {
	out := map[string][]string{}
	awsapi.WalkOperations(func(op awsapi.Operation) bool {
		if op.URI != "" {
			key := awsapi.ServiceKey(op.Service)
			out[key] = append(out[key], op.URI)
		}
		return true
	})
	return out
}

// checkDocOnlyRowsNameRealOperations closes the last thing DocOnly still
// silences.
//
// #863 made DocOnly an assertion about dispatch (checkDocOnlyRowsAreNotDispatched);
// it stayed an exemption from the *name* check, which is the half #862 used.
// SESv2 carried six rows as DocOnly purely because their declared names did not
// resolve in the manifest, and that removed them from every other cross-check
// at the same time. A DocOnly row named like an operation is making a claim
// about AWS, and this holds it to the same model as every other row.
func checkDocOnlyRowsNameRealOperations(caps []CapabilityDecl, complete bool) int {
	violations := 0
	seen := map[string]bool{}
	for _, cap := range caps {
		if !cap.DocOnly || !operationShapedName.MatchString(cap.Operation) {
			continue
		}
		key := cap.Service + "/" + cap.Operation
		if awsapi.HasOperation(cap.Service, modeledOperationName(cap)) {
			if _, recorded := docOnlyRowsOutsideTheModel[key]; recorded {
				fmt.Printf("DOCONLY_LEDGER_STALE %s  (recorded as not an operation of this service, but the model now has it — delete the entry)\n", key)
				violations++
			}
			continue
		}
		seen[key] = true
		if _, recorded := docOnlyRowsOutsideTheModel[key]; recorded {
			continue
		}
		fmt.Printf("DOCONLY_UNKNOWN_MODEL_OPERATION %s  (a DocOnly row named like an AWS operation that this service does not model — correct the name, set DisplayName, or record in docOnlyRowsOutsideTheModel what it really documents)\n", key)
		violations++
	}

	for key := range docOnlyRowsOutsideTheModel {
		if seen[key] || !complete {
			continue
		}
		fmt.Printf("DOCONLY_LEDGER_STALE %s  (no DocOnly row with this key is outside the model any more — delete the entry)\n", key)
		violations++
	}
	return violations
}

// checkModelExemptionsAreStillNeeded is the rule #864 states about itself,
// enforced: "an exemption added to make a gate green is precisely how the
// current situation arose".
//
// An exemption that has stopped being true is worse than clutter. It reads as a
// statement about AWS — capabilityManifestExemptions says in prose that "API
// Gateway v2 has no GetIntegration operation" — and it silences the model check
// for whatever is declared under that key next. Nine of the fourteen entries
// were stale when this check was written, all of them asserting that
// apigatewayv2 does not model an operation it has modeled all along.
func checkModelExemptionsAreStillNeeded(root string, caps []CapabilityDecl) int {
	rows := map[string]CapabilityDecl{}
	for _, cap := range caps {
		rows[cap.Service+"/"+cap.Operation] = cap
	}

	violations := 0
	for _, key := range sortedSetKeys(toSet(capabilityManifestExemptions)) {
		cap, ok := rows[key]
		switch {
		case !ok:
			fmt.Printf("MODEL_EXEMPTION_STALE %s  (%s — no capability row declares this operation; delete the exemption)\n",
				key, capabilityManifestExemptions[key])
			violations++
		case awsapi.HasOperation(cap.Service, modeledOperationName(cap)):
			fmt.Printf("MODEL_EXEMPTION_STALE %s  (%s — but AWS models %s for this service; delete the exemption and let the model check hold the row)\n",
				key, capabilityManifestExemptions[key], modeledOperationName(cap))
			violations++
		case cap.EmulatorOnly:
			fmt.Printf("MODEL_EXEMPTION_STALE %s  (%s — the row carries EmulatorOnly now, which states the same thing and is checked; delete the exemption)\n",
				key, capabilityManifestExemptions[key])
			violations++
		}
	}

	for _, key := range sortedSetKeys(toSet(capabilityOperationAliases)) {
		cap, ok := rows[key]
		if !ok {
			fmt.Printf("OPERATION_ALIAS_STALE %s  (aliased to %s; no capability row declares it — delete the alias)\n",
				key, capabilityOperationAliases[key])
			violations++
			continue
		}
		if awsapi.HasOperation(cap.Service, cap.Operation) {
			fmt.Printf("OPERATION_ALIAS_STALE %s  (aliased to %s, but the declared name resolves in the model on its own — delete the alias)\n",
				key, capabilityOperationAliases[key])
			violations++
		}
	}

	violations += checkCompatExemptionsAreStillNeeded(root, caps)
	return violations
}

// checkCompatExemptionsAreStillNeeded holds the compat registry's service
// exemptions to the same rule: an exemption for a group that no longer exists,
// or whose service has since become a capability key, is pre-approval nobody
// asked for.
func checkCompatExemptionsAreStillNeeded(root string, caps []CapabilityDecl) int {
	groups, err := compatRegistryServices(root)
	if err != nil {
		// A missing registry is not this check's business; the group check
		// already reports a read failure.
		return 0
	}

	capabilityKeys := map[string]bool{}
	for _, cap := range caps {
		capabilityKeys[cap.Service] = true
	}

	violations := 0
	for _, service := range sortedSetKeys(toSet(compatRegistryServiceExemptions)) {
		switch {
		case !groups[service]:
			fmt.Printf("COMPAT_EXEMPTION_STALE %s  (%s — no compat group declares this service; delete the exemption)\n",
				service, compatRegistryServiceExemptions[service])
			violations++
		case capabilityKeys[service]:
			fmt.Printf("COMPAT_EXEMPTION_STALE %s  (%s — but it is a capability service key now; delete the exemption)\n",
				service, compatRegistryServiceExemptions[service])
			violations++
		}
	}
	return violations
}

// compatRegistryServices returns the set of services the compat registry's
// groups declare. It reads the same file checkCompatRegistryServiceKeys does,
// deliberately without sharing its parse: this check runs once per invocation
// and a second decode of a 100 KB file is cheaper than threading state through
// a per-service loop.
func compatRegistryServices(root string) (map[string]bool, error) {
	raw, err := os.ReadFile(filepath.Join(root, "compat", "suites", "registry.json"))
	if err != nil {
		return nil, err
	}
	var registry struct {
		Groups []struct {
			Service string `json:"service"`
		} `json:"groups"`
	}
	if err := json.Unmarshal(raw, &registry); err != nil {
		return nil, err
	}
	out := map[string]bool{}
	for _, group := range registry.Groups {
		out[group.Service] = true
	}
	return out, nil
}

// declaredWireStrings returns the string values a service's method returns,
// resolving package-level string constants, and whether the method exists.
//
// It reads source rather than calling the method because capgen is a build tool
// that must not construct a service: constructing one wires state backends,
// background goroutines and Docker probes, and capgen's whole design is that it
// can answer questions about a package it does not run.
func declaredWireStrings(svcDir, method string) ([]string, bool, error) {
	entries, err := os.ReadDir(svcDir)
	if err != nil {
		return nil, false, err
	}

	fset := token.NewFileSet()
	files := make([]*ast.File, 0, len(entries))
	constants := map[string]string{}
	for _, e := range entries {
		if shouldSkipFile(e) {
			continue
		}
		f, parseErr := parseGoFile(fset, filepath.Join(svcDir, e.Name()))
		if parseErr != nil {
			continue
		}
		files = append(files, f)
		collectStringConstants(f, constants)
	}

	var values []string
	found := false
	for _, f := range files {
		ast.Inspect(f, func(n ast.Node) bool {
			fd, ok := n.(*ast.FuncDecl)
			if !ok || fd.Recv == nil || fd.Name.Name != method || fd.Body == nil {
				return true
			}
			found = true
			ast.Inspect(fd.Body, func(inner ast.Node) bool {
				ret, ok := inner.(*ast.ReturnStmt)
				if !ok {
					return true
				}
				for _, result := range ret.Results {
					values = append(values, resolveStringExpr(result, constants)...)
				}
				return true
			})
			return true
		})
	}
	sort.Strings(values)
	return values, found, nil
}

// collectStringConstants records every package-level string constant or
// variable initialised from a literal, so `return []string{apiPrefix}` resolves
// to the path it names.
func collectStringConstants(f *ast.File, into map[string]string) {
	for _, decl := range f.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || (gen.Tok != token.CONST && gen.Tok != token.VAR) {
			continue
		}
		for _, spec := range gen.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for i, name := range vs.Names {
				if i >= len(vs.Values) {
					continue
				}
				if lit, ok := vs.Values[i].(*ast.BasicLit); ok && lit.Kind == token.STRING {
					if s, err := strconv.Unquote(lit.Value); err == nil {
						into[name.Name] = s
					}
				}
			}
		}
	}
}

// resolveStringExpr flattens a returned expression to the strings it names:
// a literal, a constant, or a []string composite of either.
func resolveStringExpr(expr ast.Expr, constants map[string]string) []string {
	switch v := expr.(type) {
	case *ast.BasicLit:
		if v.Kind != token.STRING {
			return nil
		}
		if s, err := strconv.Unquote(v.Value); err == nil {
			return []string{s}
		}
	case *ast.Ident:
		if s, ok := constants[v.Name]; ok {
			return []string{s}
		}
	case *ast.CompositeLit:
		var out []string
		for _, elt := range v.Elts {
			out = append(out, resolveStringExpr(elt, constants)...)
		}
		return out
	}
	return nil
}

// serviceSourceDir is serviceDir joined to the workspace, so a caller that has
// only a service key does not have to know about subServices.
func serviceSourceDir(root, service string) string {
	return filepath.Join(root, "internal", "services", serviceDir(service))
}

func toSet[V any](m map[string]V) map[string]bool {
	out := make(map[string]bool, len(m))
	for k := range m {
		out[k] = true
	}
	return out
}

func sortedSetKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
