//go:build dev

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheckCapabilitiesInManifest_allowsDocOnlyAndRejectsUnknown(t *testing.T) {
	// Given: one modeled capability, one DocOnly row, one EmulatorOnly row, and one typo.
	caps := []CapabilityDecl{
		{Service: "secretsmanager", Operation: "ListSecrets"},
		{Service: "cognito", Operation: "ListUsers"},
		{Service: "apigateway", Operation: "CreateV2Api"},
		{Service: "cloudfront", Operation: "DeleteFieldLevelEncryption"},
		{Service: "appsync", Operation: "ExecuteGraphQL", EmulatorOnly: true},
		{Service: "secretsmanager", Operation: "ConsoleOnly", DocOnly: true},
		{Service: "secretsmanager", Operation: "NotAnAWSOperation"},
	}

	// When: capgen validates the declarations against the generated corpus.
	violations := checkCapabilitiesInManifest(caps)

	// Then: only the undeclared AWS operation is rejected.
	if violations != 1 {
		t.Errorf("checkCapabilitiesInManifest() = %d violations, want 1", violations)
	}
}

func TestModeledOperationName_resolvesDisplayNameToTheAWSName(t *testing.T) {
	// Given: SESv2 rows, whose internal identifiers carry a V2 prefix to keep
	// them apart from the v1 operations of the same name, and whose DisplayName
	// records the AWS name — which is exactly what DisplayName is documented
	// to be for.
	caps := []CapabilityDecl{
		{Service: "ses", Operation: "V2SendEmail", DisplayName: "SendEmail"},
		{Service: "ses", Operation: "V2CreateEmailIdentity", DisplayName: "CreateEmailIdentity"},
	}

	// When: capgen validates them against the corpus.
	violations := checkCapabilitiesInManifest(caps)

	// Then: they resolve. Before this, they did not, and six SESv2 rows carried
	// DocOnly to silence the resulting UNKNOWN_MODEL_OPERATION — which also
	// removed them from every cross-check, and is how #862 stayed hidden.
	if violations != 0 {
		t.Errorf("checkCapabilitiesInManifest() = %d violations, want 0", violations)
	}
}

func TestModeledOperationName_prosaicDisplayNameIsNotAnAWSName(t *testing.T) {
	// Given: a DocOnly row whose DisplayName is prose describing a group of
	// operations rather than naming one — the shape SES uses for "All other v2
	// operations".
	caps := []CapabilityDecl{
		{Service: "ses", Operation: "V2Other", DisplayName: "All other v2 operations", DocOnly: true},
	}

	// When: capgen validates it against the corpus.
	violations := checkCapabilitiesInManifest(caps)

	// Then: DocOnly still exempts a genuinely non-dispatched row from the
	// name check, so honouring DisplayName does not fail the rows the flag was
	// designed for.
	if violations != 0 {
		t.Errorf("checkCapabilitiesInManifest() = %d violations, want 0", violations)
	}
}

func TestCheckDocOnlyRowsAreNotDispatched_rejectsAnImplementedRow(t *testing.T) {
	// Given: a DocOnly capability with an HTTP handler method of its own name,
	// and one without. DocOnly's contract is "non-dispatched rows"; the first
	// is dispatched and so the flag is a false claim.
	svcDir := t.TempDir()
	writeGoFile(t, svcDir, "handler.go", `package widget

import "net/http"

func (h *Handler) V2CreateEmailIdentity(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
}
`)
	caps := []CapabilityDecl{
		{Service: "widget", Operation: "V2CreateEmailIdentity", DocOnly: true},
		{Service: "widget", Operation: "V2Other", DocOnly: true},
		// Not DocOnly, so having a handler is exactly what is expected.
		{Service: "widget", Operation: "V2CreateEmailIdentity"},
	}

	// When: capgen checks the DocOnly claims against the package.
	violations := checkDocOnlyRowsAreNotDispatched("widget", svcDir, caps)

	// Then: only the dispatched DocOnly row is rejected.
	if violations != 1 {
		t.Errorf("checkDocOnlyRowsAreNotDispatched() = %d violations, want 1", violations)
	}
}

func TestCheckDocOnlyRowsAreNotDispatched_stubHandlerIsNotAnImplementation(t *testing.T) {
	// Given: a DocOnly row whose only method returns 501. Documenting an
	// unsupported operation that has an explicit stub is one of the three uses
	// DocOnly's contract names.
	svcDir := t.TempDir()
	writeGoFile(t, svcDir, "handler_stubs.go", `package widget

import "net/http"

func (h *Handler) ArchiveWidget(w http.ResponseWriter, r *http.Request) {
	protocol.NotImplementedJSON(w, r)
}
`)
	caps := []CapabilityDecl{{Service: "widget", Operation: "ArchiveWidget", DocOnly: true}}

	// When: capgen checks the DocOnly claims against the package.
	violations := checkDocOnlyRowsAreNotDispatched("widget", svcDir, caps)

	// Then: a 501 is not an implementation, so the flag stands.
	if violations != 0 {
		t.Errorf("checkDocOnlyRowsAreNotDispatched() = %d violations, want 0", violations)
	}
}

func TestImplementedHandlerMethods_conditional501IsNotAStub(t *testing.T) {
	// Given: a handler that implements its operation and refuses one
	// unsupported case with a 501 — Lambda's CreateFunction shape — alongside a
	// method whose whole body is the 501.
	svcDir := t.TempDir()
	writeGoFile(t, svcDir, "handler.go", `package widget

import "net/http"

func (h *Handler) CreateWidget(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("X-Package-Type") == "Image" {
		protocol.NotImplementedJSON(w, r)
		return
	}
	h.create(w, r)
}

func (h *Handler) ArchiveWidget(w http.ResponseWriter, r *http.Request) {
	protocol.NotImplementedJSON(w, r)
}

// Not a handler: the signature does not match, so it must not be offered as
// evidence that an operation of this name is dispatched.
func (h *Handler) DescribeWidget(id string) error { return nil }
`)

	// When: capgen reads the package's handler methods.
	methods, err := implementedHandlerMethods(svcDir)
	if err != nil {
		t.Fatalf("implementedHandlerMethods() error = %v", err)
	}

	// Then: only the whole-body 501 is a stub, and the non-handler is absent.
	if !methods["CreateWidget"] {
		t.Error("CreateWidget = stub, want implemented: a 501 inside a branch is a refusal, not a stub")
	}
	if implemented, ok := methods["ArchiveWidget"]; !ok || implemented {
		t.Errorf("ArchiveWidget = (%v, %v), want present and not implemented", implemented, ok)
	}
	if _, ok := methods["DescribeWidget"]; ok {
		t.Error("DescribeWidget was reported as a handler method; its signature is not (ResponseWriter, *Request)")
	}
}

func TestRouteSkeleton_bindsOperationsWhereTheModelDoes(t *testing.T) {
	// Given: EKS, whose six hand-invented paths — four with the wrong HTTP
	// method — were typed out beside a manifest that already had the answers
	// (#858).
	caps := []CapabilityDecl{{Service: "eks", Operation: "UpdateAddon", Status: "StatusWIP"}}

	// When: the skeleton is generated from the model.
	lines := strings.Join(routeSkeleton("eks", caps), "\n")

	// Then: it gives the modeled method and path, not the registered ones —
	// EKS serves UpdateAddon at POST /clusters/{name}/addons/{addonName}/updates.
	want := `r.Post("/clusters/{clusterName}/addons/{addonName}/update", s.handler.UpdateAddon) // WIP`
	if !strings.Contains(lines, want) {
		t.Errorf("routeSkeleton() did not contain\n\t%s\ngot:\n%s", want, lines)
	}
	// And an operation with no capability row is offered, marked so the
	// reader decides rather than the generator.
	if !strings.Contains(lines, "s.handler.RegisterCluster) // not declared") {
		t.Error("routeSkeleton() omitted an undeclared modeled operation; the skeleton is the model's whole REST surface")
	}
}

func TestRouteSkeleton_nonRESTServiceHasNoBindings(t *testing.T) {
	// Given: SQS, an AWS JSON API dispatched from X-Amz-Target rather than
	// from a path.

	// When: the skeleton is generated.
	lines := routeSkeleton("sqs", nil)

	// Then: it says so rather than emitting a route table that would be wrong.
	if len(lines) != 1 || !strings.Contains(lines[0], "no REST bindings") {
		t.Errorf("routeSkeleton() = %v, want a single explanatory line", lines)
	}
}

func TestCheckServiceKeysInManifest_requiresKeyOrAliasResolution(t *testing.T) {
	// Given: service keys that resolve directly, via an alias, and not at all.
	services := []string{"sqs", "stepfunctions", "cloudwatch-logs"}
	caps := []CapabilityDecl{
		{Service: "waf", Operation: "CreateWebACL"},
		{Service: "not-an-aws-service", Operation: "DoThing"},
	}

	// When: capgen validates every key against the generated corpus.
	violations := checkServiceKeysInManifest(services, caps)

	// Then: only the key no manifest identity resolves to is rejected.
	if violations != 1 {
		t.Errorf("checkServiceKeysInManifest() = %d violations, want 1", violations)
	}
}

func TestCheckCompatRegistryServiceKeys_requiresCapabilityServiceKeys(t *testing.T) {
	// Given: compat groups keyed by a capability service, the exempt IaC tool
	// grouping, and a service no capability table declares.
	root := t.TempDir()
	writeCompatRegistry(t, root, `{
	  "version": 1,
	  "groups": [
	    {"service": "sqs", "name": "sqs-crud", "tests": [{"name": "SendMessage"}]},
	    {"service": "cognito", "name": "cognito-pools", "tests": [{"name": "ListUsers"}]},
	    {"service": "cdk", "name": "cdk-lifecycle", "tests": [{"name": "Deploy"}]},
	    {"service": "sqsx", "name": "sqsx-typo", "tests": [{"name": "SendMessage"}]}
	  ]
	}`)
	caps := []CapabilityDecl{
		{Service: "sqs", Operation: "SendMessage"},
		{Service: "cognito", Operation: "ListUsers"},
	}

	// When: capgen validates the compat registry against capability keys.
	violations := checkCompatRegistryServiceKeys(root, caps)

	// Then: only the undeclared service key is rejected.
	if violations != 1 {
		t.Errorf("checkCompatRegistryServiceKeys() = %d violations, want 1", violations)
	}
}

func TestCheckCompatRegistryServiceKeys_rejectsAnUnmodeledTestOperation(t *testing.T) {
	// Given: compat tests naming the operation they exercise — one real, one a
	// typo, one belonging to a different service, and one scenario test with no
	// `op` at all, which is the shape most registry entries have.
	root := t.TempDir()
	writeCompatRegistry(t, root, `{
	  "version": 1,
	  "groups": [
	    {"service": "s3", "name": "s3-objects", "tests": [
	      {"name": "PutObjectMultipleKeys", "op": "PutObject"},
	      {"name": "ListObjectsV2Delimiter"},
	      {"name": "PutObjectTypo", "op": "PutObjcet"},
	      {"name": "SendMessage", "op": "SendMessage"}
	    ]}
	  ]
	}`)
	caps := []CapabilityDecl{{Service: "s3", Operation: "PutObject"}}

	// When: capgen validates the registry against the corpus.
	violations := checkCompatRegistryServiceKeys(root, caps)

	// Then: the typo and the other service's operation are rejected, and the
	// scenario test without an `op` is left alone — a test name is a scenario
	// name where it needs to be.
	if violations != 2 {
		t.Errorf("checkCompatRegistryServiceKeys() = %d violations, want 2", violations)
	}
}

func TestCheckCompatRegistryServiceKeys_absentRegistryIsNotAViolation(t *testing.T) {
	// Given: a workspace with no compat registry (capgen runs outside it too).

	// When: the check runs.
	violations := checkCompatRegistryServiceKeys(t.TempDir(), nil)

	// Then: a missing registry is silent rather than a spurious failure.
	if violations != 0 {
		t.Errorf("checkCompatRegistryServiceKeys() = %d violations, want 0", violations)
	}
}

func TestParseHandlerOps_detectsTypedOperationRegistry(t *testing.T) {
	// Given: a REST-routed service whose only operation registration is the
	// typed registry built by typedOps().
	svcDir := t.TempDir()
	writeGoFile(t, svcDir, "typed_ops.go", `package widget

import "github.com/overcast-sh/overcast/internal/protocol/op"

func (s *Service) typedOps() map[string]op.Operation {
	return map[string]op.Operation{
		"CreateWidget": op.NewTyped[createWidgetRequest, createWidgetResponse]("CreateWidget", s.createWidgetTyped),
		"ListWidgets":  op.NewTypedAny[listWidgetsRequest]("ListWidgets", s.listWidgetsTyped),
	}
}
`)

	// When: capgen parses the package.
	ops, comprehensive, err := parseHandlerOps(svcDir)
	if err != nil {
		t.Fatalf("parseHandlerOps() error = %v", err)
	}

	// Then: both typed operations are detected, but the registry does not claim
	// comprehensive detection — REST-routed services register only a subset here.
	if got := opNames(ops); len(got) != 2 || got[0] != "CreateWidget" || got[1] != "ListWidgets" {
		t.Errorf("parseHandlerOps() ops = %v, want [CreateWidget ListWidgets]", got)
	}
	if comprehensive {
		t.Error("parseHandlerOps() comprehensive = true, want false for a typed-registry-only service")
	}
}

func TestParseHandlerOps_typedRegistryDoesNotWeakenHandlerFuncMap(t *testing.T) {
	// Given: a service that dispatches through map[string]http.HandlerFunc and
	// registers an extra operation only in the typed registry.
	svcDir := t.TempDir()
	writeGoFile(t, svcDir, "handler.go", `package widget

import "net/http"

func (h *Handler) initOps() {
	h.ops = map[string]http.HandlerFunc{
		"CreateWidget": h.CreateWidget,
		"ArchiveWidget": h.ArchiveWidget,
	}
}

func (h *Handler) ArchiveWidget(w http.ResponseWriter, r *http.Request) {
	protocol.NotImplementedJSON(w, r)
}
`)
	writeGoFile(t, svcDir, "typed_ops.go", `package widget

import "github.com/overcast-sh/overcast/internal/protocol/op"

func (h *Handler) typedOps() map[string]op.Operation {
	return map[string]op.Operation{
		"CreateWidget": op.NewTyped[createWidgetRequest, createWidgetResponse]("CreateWidget", h.createWidgetTyped),
		"ListWidgets":  op.NewTyped[listWidgetsRequest, listWidgetsResponse]("ListWidgets", h.listWidgetsTyped),
	}
}
`)

	// When: capgen parses the package.
	ops, comprehensive, err := parseHandlerOps(svcDir)
	if err != nil {
		t.Fatalf("parseHandlerOps() error = %v", err)
	}

	// Then: the typed-only operation joins the set and the HandlerFunc map still
	// makes detection comprehensive, so ORPHANs remain violations.
	if got := opNames(ops); len(got) != 3 {
		t.Errorf("parseHandlerOps() ops = %v, want 3 operations", got)
	}
	if !comprehensive {
		t.Error("parseHandlerOps() comprehensive = false, want true when a HandlerFunc map is present")
	}
}

func TestParseHandlerOps_typedRegistryEntryDelegatingToAStubIsAStub(t *testing.T) {
	// Given: a typed registry that wraps a raw handler method returning 501 —
	// the op.NewRaw shape services use to expose an existing HandlerFunc.
	svcDir := t.TempDir()
	writeGoFile(t, svcDir, "typed_ops.go", `package widget

import "github.com/overcast-sh/overcast/internal/protocol/op"

func (h *Handler) typedOps() map[string]op.Operation {
	return map[string]op.Operation{
		"ArchiveWidget": op.NewRaw("ArchiveWidget", h.ArchiveWidget),
	}
}

func (h *Handler) ArchiveWidget(w http.ResponseWriter, r *http.Request) {
	protocol.NotImplementedJSON(w, r)
}
`)

	// When: capgen parses the package.
	ops, _, err := parseHandlerOps(svcDir)
	if err != nil {
		t.Fatalf("parseHandlerOps() error = %v", err)
	}

	// Then: the operation is reported as a stub, so a Supported declaration is
	// caught by the WRONG_STATUS check.
	if len(ops) != 1 {
		t.Fatalf("parseHandlerOps() ops = %v, want 1 operation", opNames(ops))
	}
	if !ops[0].IsStub {
		t.Error("parseHandlerOps() IsStub = false, want true for an operation delegating to a 501 method")
	}
}

func opNames(ops []Operation) []string {
	names := make([]string, 0, len(ops))
	for _, op := range ops {
		names = append(names, op.Name)
	}
	return names
}

func writeGoFile(t *testing.T, dir, name, contents string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeCompatRegistry(t *testing.T, root, contents string) {
	t.Helper()
	dir := filepath.Join(root, "compat", "suites")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "registry.json"), []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestCheckCapabilitiesInManifest_emulatorOnlyRowNeedsNoExemption(t *testing.T) {
	// Given: an Overcast extension that AWS models nowhere, carrying the flag
	// rather than an entry in capabilityManifestExemptions.
	caps := []CapabilityDecl{
		{Service: "cloudfront", Operation: "ProxyRequest", EmulatorOnly: true},
	}

	// When: capgen validates the declarations against the generated corpus.
	violations := checkCapabilitiesInManifest(caps)

	// Then: the flag carries it, as the deleted exemption used to.
	if violations != 0 {
		t.Errorf("checkCapabilitiesInManifest() = %d violations, want 0", violations)
	}
}

func TestCheckEmulatorOnlyRowsAreNotModeled_rejectsAnOperationAWSModels(t *testing.T) {
	// Given: one genuine extension and one real AWS operation wearing the flag.
	caps := []CapabilityDecl{
		{Service: "cloudfront", Operation: "ProxyRequest", EmulatorOnly: true},
		{Service: "cloudfront", Operation: "CreateInvalidation", EmulatorOnly: true},
		{Service: "cloudfront", Operation: "GetDistribution"},
	}

	// When: capgen checks that the flag never covers a modeled operation.
	violations := checkEmulatorOnlyRowsAreNotModeled(caps)

	// Then: only the modeled one is rejected — the flag may not hide an
	// operation an SDK can call, which is the whole reason it excuses a row
	// from the counts.
	if violations != 1 {
		t.Errorf("checkEmulatorOnlyRowsAreNotModeled() = %d violations, want 1", violations)
	}
}

func TestCoverage_excludesEmulatorOnlyRowsFromTheAWSCount(t *testing.T) {
	// Given: two AWS operations and one Overcast extension.
	caps := []CapabilityDecl{
		{Service: "cloudfront", Operation: "GetDistribution", Status: "StatusSupported"},
		{Service: "cloudfront", Operation: "GetFunction", Status: "StatusSupported"},
		{Service: "cloudfront", Operation: "ProxyRequest", Status: "StatusSupported", EmulatorOnly: true},
	}

	// When: the coverage arithmetic behind every published count runs.
	implemented, total := coverage(caps)
	sentence := coverageSentence(caps)

	// Then: the extension is in neither half of "N of M AWS operations".
	if implemented != 2 || total != 2 {
		t.Errorf("coverage() = %d of %d, want 2 of 2", implemented, total)
	}
	if want := "All 2 listed operations are implemented."; sentence != want {
		t.Errorf("coverageSentence() = %q, want %q", sentence, want)
	}
}

func TestBuildDocSection_emulatorOnlyRowIsSeparatedAndUnlinked(t *testing.T) {
	// Given: an AWS operation and an Overcast extension in the same category.
	caps := []CapabilityDecl{
		{Service: "cloudfront", Operation: "GetDistribution", Category: "Distributions", Status: "StatusSupported"},
		{Service: "cloudfront", Operation: "ProxyRequest", Category: "Proxy", Status: "StatusSupported", EmulatorOnly: true},
	}

	// When: the operations page is rendered.
	doc := buildDocSection("cloudfront", caps)

	// Then: the extension is discoverable under its own heading, and never
	// carries a fabricated AWS docs URL — there is no API_ProxyRequest.html.
	if strings.Contains(doc, "API_ProxyRequest.html") {
		t.Error("buildDocSection() links an AWS docs page for an emulator-only operation")
	}
	if !strings.Contains(doc, emulatorSectionHeading) {
		t.Errorf("buildDocSection() has no %q section:\n%s", emulatorSectionHeading, doc)
	}
	if !strings.Contains(doc, "`ProxyRequest`") {
		t.Error("buildDocSection() dropped the emulator-only row entirely")
	}

	// And: it is out of the Endpoints tables and the Summary counts, which are
	// what "N of M AWS operations" is read off.
	endpoints := doc[strings.Index(doc, "## Endpoints"):strings.Index(doc, emulatorSectionHeading)]
	if strings.Contains(endpoints, "ProxyRequest") {
		t.Error("buildDocSection() still lists the emulator-only row under ## Endpoints")
	}
	if strings.Contains(doc[:strings.Index(doc, "## Endpoints")], "Proxy") {
		t.Error("buildDocSection() still counts the emulator-only row's category in ## Summary")
	}
}
