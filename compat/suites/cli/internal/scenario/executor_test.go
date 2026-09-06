package scenario

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/overcast-sh/overcast-compat-cli/internal/harness"
	"github.com/overcast-sh/overcast-compat-cli/internal/registry"
)

// ─── the in-memory fake ───────────────────────────────────────────────────────

// fakeCall is one recorded `aws` invocation, split the way the interpreter
// builds it: the command name, the kebab-cased operation, and the JSON handed
// to --cli-input-json.
type fakeCall struct {
	command string
	op      string
	input   string
}

// fakeResult is one scripted answer.
type fakeResult struct {
	body map[string]any
	err  error
}

// fakeRunner answers calls from a per-operation script and records every one,
// so a test can assert both what came back and what went out. The last scripted
// answer for an operation repeats once the script is exhausted, which is what
// lets an eventually test say "fails twice, then passes" without counting the
// attempts that follow.
type fakeRunner struct {
	script map[string][]fakeResult
	calls  []fakeCall
}

func (f *fakeRunner) run(_ context.Context, _ *harness.TestContext, args []string) (map[string]any, error) {
	call := fakeCall{command: args[0], op: args[1]}
	if len(args) > 3 {
		call.input = args[3]
	}
	f.calls = append(f.calls, call)

	results := f.script[call.op]
	if len(results) == 0 {
		return nil, fmt.Errorf("an error occurred (NoScriptedResponse) when calling the %s operation", call.op)
	}
	r := results[0]
	if len(results) > 1 {
		f.script[call.op] = results[1:]
	}
	return r.body, r.err
}

func (f *fakeRunner) ops() []string {
	out := make([]string, 0, len(f.calls))
	for _, c := range f.calls {
		out = append(out, c.op)
	}
	return out
}

// ok scripts a successful response.
func ok(body map[string]any) fakeResult { return fakeResult{body: body} }

// awsErr scripts a failure shaped like the AWS CLI's own message.
func awsErr(code string) fakeResult {
	return fakeResult{err: fmt.Errorf(
		"aws sqs get-queue-attributes: exit status 254: An error occurred (%s) when calling the operation", code)}
}

// obj is a shorthand for a decoded JSON object.
type obj = map[string]any

// ─── harness ──────────────────────────────────────────────────────────────────

// fixture writes a scenario file into a throwaway repository root and returns a
// Backend wired to the fake runner, plus the registry group that names it.
func fixture(t *testing.T, file map[string]any) (*Backend, *fakeRunner, registry.RegistryGroup) {
	t.Helper()
	root := t.TempDir()
	rel := "compat/model/scenarios/widgets.json"
	abs := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(abs), 0o750); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(file)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(abs, raw, 0o600); err != nil {
		t.Fatal(err)
	}

	b := New(root)
	fake := &fakeRunner{script: map[string][]fakeResult{}}
	b.run = fake

	groups, _ := file["groups"].([]any)
	name := ""
	if len(groups) > 0 {
		if g, okg := groups[0].(obj); okg {
			name, _ = g["name"].(string)
		}
	}
	return b, fake, registry.RegistryGroup{
		Service: "widgets", Name: name, Generated: true, Scenario: rel,
		Suites: []string{"cli"},
	}
}

// scenarioFile builds the smallest legal scenario file around one group.
func scenarioFile(group obj) obj {
	return obj{
		"version": 1,
		"service": "widgets",
		"client": obj{
			"sdkId": "Widgets", "endpointPrefix": "widgets", "signingName": "widgets",
			"protocol": "awsJson1_0", "apiVersion": "2012-11-05",
		},
		"groups": []any{group},
	}
}

// lifecycle wraps tests into a group with no setup or teardown.
func lifecycle(name string, tests ...obj) obj {
	list := make([]any, 0, len(tests))
	for _, tc := range tests {
		list = append(list, tc)
	}
	return obj{"name": name, "kind": "lifecycle", "setup": []any{}, "tests": list, "teardown": []any{}}
}

// runTest resolves one test through the backend and runs it.
func runOneTest(t *testing.T, b *Backend, rg registry.RegistryGroup, name string) error {
	t.Helper()
	fn, resolved := b.Resolve(rg, registry.RegistryTest{Name: name})
	if !resolved {
		t.Fatalf("backend did not claim %s/%s", rg.Name, name)
	}
	return fn(context.Background(), harness.NewTestContext("http://endpoint", "us-east-1", "run1"))
}

// ─── kebab-casing ─────────────────────────────────────────────────────────────

// TestKebabOpMatchesTheCLI pins the operation → subcommand derivation against
// every operation the pilot corpus uses. The expected values are the AWS CLI's
// own subcommand names (`aws sqs help`, `aws organizations help` under
// aws-cli 2.36), so this is a check against the tool, not against the algorithm
// restated.
// TestAwsCommandUsesTheCLIsOwnNameForTheFourServicesThatDiffer pins
// compat/model/README.md § Naming's cli row. The scenario file carries an
// endpoint prefix and no command name, and for four services the CLI's own
// command is not it — `aws elasticloadbalancing` is not a command at all, so a
// generated group for elastic-load-balancing would report every test as a
// failure of the service rather than of the derivation.
func TestAwsCommandUsesTheCLIsOwnNameForTheFourServicesThatDiffer(t *testing.T) {
	for prefix, want := range map[string]string{
		"elasticloadbalancing": "elb",
		"monitoring":           "cloudwatch",
		"email":                "ses",
		"states":               "stepfunctions",
		// Everything else is the endpoint prefix, unchanged.
		"sqs":           "sqs",
		"organizations": "organizations",
		"batch":         "batch",
	} {
		if got := awsCommand(prefix); got != want {
			t.Errorf("awsCommand(%q) = %q, want %q", prefix, got, want)
		}
	}
}

func TestKebabOpMatchesTheCLI(t *testing.T) {
	cases := map[string]string{
		// sqs
		"CancelMessageMoveTask":        "cancel-message-move-task",
		"ChangeMessageVisibility":      "change-message-visibility",
		"ChangeMessageVisibilityBatch": "change-message-visibility-batch",
		"CreateQueue":                  "create-queue",
		"DeleteMessage":                "delete-message",
		"DeleteMessageBatch":           "delete-message-batch",
		"DeleteQueue":                  "delete-queue",
		"GetQueueAttributes":           "get-queue-attributes",
		"GetQueueUrl":                  "get-queue-url",
		"ListDeadLetterSourceQueues":   "list-dead-letter-source-queues",
		"ListMessageMoveTasks":         "list-message-move-tasks",
		"ListQueueTags":                "list-queue-tags",
		"ListQueues":                   "list-queues",
		"PurgeQueue":                   "purge-queue",
		"ReceiveMessage":               "receive-message",
		"SendMessage":                  "send-message",
		"SendMessageBatch":             "send-message-batch",
		"SetQueueAttributes":           "set-queue-attributes",
		"StartMessageMoveTask":         "start-message-move-task",
		"TagQueue":                     "tag-queue",
		"UntagQueue":                   "untag-queue",
		// organizations
		"CreatePolicy":                           "create-policy",
		"DeletePolicy":                           "delete-policy",
		"DescribeAccount":                        "describe-account",
		"DescribeCreateAccountStatus":            "describe-create-account-status",
		"DescribeEffectivePolicy":                "describe-effective-policy",
		"DescribeHandshake":                      "describe-handshake",
		"DescribeOrganization":                   "describe-organization",
		"DescribeOrganizationalUnit":             "describe-organizational-unit",
		"DescribePolicy":                         "describe-policy",
		"DescribeResourcePolicy":                 "describe-resource-policy",
		"DescribeResponsibilityTransfer":         "describe-responsibility-transfer",
		"ListAWSServiceAccessForOrganization":    "list-aws-service-access-for-organization",
		"ListAccounts":                           "list-accounts",
		"ListAccountsForParent":                  "list-accounts-for-parent",
		"ListAccountsWithInvalidEffectivePolicy": "list-accounts-with-invalid-effective-policy",
		"ListChildren":                           "list-children",
		"ListCreateAccountStatus":                "list-create-account-status",
		"ListDelegatedAdministrators":            "list-delegated-administrators",
		"ListDelegatedServicesForAccount":        "list-delegated-services-for-account",
		"ListEffectivePolicyValidationErrors":    "list-effective-policy-validation-errors",
		"ListHandshakesForAccount":               "list-handshakes-for-account",
		"ListHandshakesForOrganization":          "list-handshakes-for-organization",
		"ListInboundResponsibilityTransfers":     "list-inbound-responsibility-transfers",
		"ListOrganizationalUnitsForParent":       "list-organizational-units-for-parent",
		"ListOutboundResponsibilityTransfers":    "list-outbound-responsibility-transfers",
		"ListParents":                            "list-parents",
		"ListPolicies":                           "list-policies",
		"ListPoliciesForTarget":                  "list-policies-for-target",
		"ListRoots":                              "list-roots",
		"ListTagsForResource":                    "list-tags-for-resource",
		"ListTargetsForPolicy":                   "list-targets-for-policy",
		"TagResource":                            "tag-resource",
		"UntagResource":                          "untag-resource",
		"UpdatePolicy":                           "update-policy",
		// derivations the corpus does not exercise yet, kept because the CLI
		// spells them this way and a naive splitter does not.
		"ListMFADevices":     "list-mfa-devices",
		"CreateSAMLProvider": "create-saml-provider",
		"ListObjectsV2":      "list-objects-v2",
	}
	for op, want := range cases {
		if got := kebabOp(op); got != want {
			t.Errorf("kebabOp(%q) = %q, want %q", op, got, want)
		}
	}
}

// ─── the shape of an invocation ───────────────────────────────────────────────

// TestInvocationIsCommandOpAndInputJSON pins the one thing every generated call
// has in common: `aws <command> <kebab-op> --cli-input-json '<params>'`, with
// the command taken from the scenario header and the params sent verbatim.
func TestInvocationIsCommandOpAndInputJSON(t *testing.T) {
	b, fake, rg := fixture(t, scenarioFile(lifecycle("widgets-gen-thing", obj{
		"name": "CreateThing", "op": "CreateThing",
		"call": obj{"op": "CreateThing", "params": obj{
			"Name": obj{"$name": "thing"}, "Size": float64(3), "Nested": obj{"A": []any{"x"}},
		}},
		"assert": []any{obj{"kind": "responseField", "checks": obj{"$.Id": obj{"nonEmpty": true}}}},
	})))
	fake.script["create-thing"] = []fakeResult{ok(obj{"Id": "t-1"})}

	if err := runOneTest(t, b, rg, "CreateThing"); err != nil {
		t.Fatalf("test failed: %v", err)
	}
	if len(fake.calls) != 1 {
		t.Fatalf("calls = %v, want one", fake.ops())
	}
	got := fake.calls[0]
	if got.command != "widgets" || got.op != "create-thing" {
		t.Errorf("invocation = aws %s %s, want aws widgets create-thing", got.command, got.op)
	}
	want := `{"Name":"run1-widgets-gen-thing-thing","Nested":{"A":["x"]},"Size":3}`
	if got.input != want {
		t.Errorf("--cli-input-json = %s, want %s", got.input, want)
	}
}

// ─── exports ──────────────────────────────────────────────────────────────────

func TestPrimaryCallExportsFeedLaterSteps(t *testing.T) {
	b, fake, rg := fixture(t, scenarioFile(lifecycle("widgets-gen-thing", obj{
		"name": "CreateThing", "op": "CreateThing",
		"call": obj{"op": "CreateThing", "params": obj{}, "export": obj{"thing.id": "$.Thing.Id"}},
		"assert": []any{obj{
			"kind":   "readback",
			"call":   obj{"op": "GetThing", "params": obj{"Id": obj{"$ref": "thing.id"}}},
			"checks": obj{"$.Id": obj{"equals": obj{"$ref": "thing.id"}}},
		}},
	})))
	fake.script["create-thing"] = []fakeResult{ok(obj{"Thing": obj{"Id": "t-7"}})}
	fake.script["get-thing"] = []fakeResult{ok(obj{"Id": "t-7"})}

	if err := runOneTest(t, b, rg, "CreateThing"); err != nil {
		t.Fatalf("test failed: %v", err)
	}
	if got := fake.calls[1].input; got != `{"Id":"t-7"}` {
		t.Errorf("read-back params = %s, want the exported id", got)
	}
}

func TestUnresolvableExportPathFailsNamingThePath(t *testing.T) {
	b, fake, rg := fixture(t, scenarioFile(lifecycle("widgets-gen-thing", obj{
		"name": "CreateThing", "op": "CreateThing",
		"call":   obj{"op": "CreateThing", "params": obj{}, "export": obj{"thing.id": "$.Thing.Id"}},
		"assert": []any{obj{"kind": "responseField", "checks": obj{"$.Thing": obj{"nonEmpty": true}}}},
	})))
	fake.script["create-thing"] = []fakeResult{ok(obj{"Thing": obj{"Other": "x"}})}

	err := runOneTest(t, b, rg, "CreateThing")
	if err == nil {
		t.Fatal("want a failure")
	}
	if !strings.Contains(err.Error(), "$.Thing.Id") || !strings.Contains(err.Error(), "export") {
		t.Errorf("failure does not name the export path: %v", err)
	}
}

// ─── setup and teardown ───────────────────────────────────────────────────────

func TestSetupRunsEveryStepAndExportsIntoTheGroupContext(t *testing.T) {
	b, fake, rg := fixture(t, scenarioFile(obj{
		"name": "widgets-gen-thing", "kind": "lifecycle",
		"setup": []any{
			obj{"op": "CreateDep", "params": obj{"Name": obj{"$name": "dep"}}, "export": obj{"dep.id": "$.Id"}},
			obj{"op": "GetDep", "params": obj{"Id": obj{"$ref": "dep.id"}}, "export": obj{"dep.arn": "$.Arn"}},
		},
		"tests": []any{obj{
			"name": "CreateThing", "op": "CreateThing",
			"call":   obj{"op": "CreateThing", "params": obj{"Dep": obj{"$ref": "dep.arn"}}},
			"assert": []any{obj{"kind": "responseField", "checks": obj{"$.Id": obj{"nonEmpty": true}}}},
		}},
		"teardown": []any{},
	}))
	fake.script["create-dep"] = []fakeResult{ok(obj{"Id": "d-1"})}
	fake.script["get-dep"] = []fakeResult{ok(obj{"Arn": "arn:dep"})}
	fake.script["create-thing"] = []fakeResult{ok(obj{"Id": "t-1"})}

	setup, hasSetup := b.Setup(rg)
	if !hasSetup {
		t.Fatal("the group declares setup steps")
	}
	tc := harness.NewTestContext("http://endpoint", "us-east-1", "run1")
	if err := setup(context.Background(), tc); err != nil {
		t.Fatalf("setup: %v", err)
	}

	fn, _ := b.Resolve(rg, registry.RegistryTest{Name: "CreateThing"})
	if err := fn(context.Background(), tc); err != nil {
		t.Fatalf("test: %v", err)
	}
	if got := fake.calls[2].input; got != `{"Dep":"arn:dep"}` {
		t.Errorf("the test did not see setup's exports: %s", got)
	}
}

func TestSetupFailureIsReportedToTheHarness(t *testing.T) {
	b, fake, rg := fixture(t, scenarioFile(obj{
		"name": "widgets-gen-thing", "kind": "lifecycle",
		"setup": []any{obj{"op": "CreateDep", "params": obj{}}},
		"tests": []any{obj{
			"name": "CreateThing", "op": "CreateThing",
			"call":   obj{"op": "CreateThing", "params": obj{}},
			"assert": []any{obj{"kind": "responseField", "checks": obj{"$.Id": obj{"nonEmpty": true}}}},
		}},
		"teardown": []any{},
	}))
	fake.script["create-dep"] = []fakeResult{awsErr("AccessDenied")}

	setup, _ := b.Setup(rg)
	err := setup(context.Background(), harness.NewTestContext("http://e", "us-east-1", "run1"))
	if err == nil {
		t.Fatal("want a setup failure; the harness turns it into skip for every test in the group")
	}
	if !strings.Contains(err.Error(), "setup[0]") || !strings.Contains(err.Error(), "AccessDenied") {
		t.Errorf("failure does not locate the step: %v", err)
	}
}

// TestTeardownWrapsEveryStepIndividually is the teardown rule of
// compat/AGENTS.md: one failure must not stop the others, and an unresolvable
// $ref skips that step alone.
func TestTeardownWrapsEveryStepIndividually(t *testing.T) {
	b, fake, rg := fixture(t, scenarioFile(obj{
		"name": "widgets-gen-thing", "kind": "lifecycle",
		"setup": []any{},
		"tests": []any{obj{
			"name": "CreateThing", "op": "CreateThing",
			"call":   obj{"op": "CreateThing", "params": obj{}},
			"assert": []any{obj{"kind": "responseField", "checks": obj{"$.Id": obj{"nonEmpty": true}}}},
		}},
		"teardown": []any{
			obj{"op": "DeleteChild", "params": obj{"Id": obj{"$ref": "never.set"}}},
			obj{"op": "DeleteThing", "params": obj{"Id": "t-1"}},
			obj{"op": "DeleteDep", "params": obj{"Id": "d-1"}},
		},
	}))
	fake.script["delete-thing"] = []fakeResult{awsErr("ResourceInUse")}
	fake.script["delete-dep"] = []fakeResult{ok(obj{})}

	teardown, hasTeardown := b.Teardown(rg)
	if !hasTeardown {
		t.Fatal("the group declares teardown steps")
	}
	// Teardown never fails the group: the skips go to stderr, and the orphan
	// sweep is what proves nothing leaked.
	if err := teardown(context.Background(), harness.NewTestContext("http://e", "us-east-1", "run1")); err != nil {
		t.Fatalf("teardown must not throw: %v", err)
	}
	if got := fake.ops(); len(got) != 2 || got[0] != "delete-thing" || got[1] != "delete-dep" {
		t.Errorf("calls = %v, want the unresolvable step skipped and the other two attempted", got)
	}
}

func TestProbeGroupGetsNoSetupOrTeardown(t *testing.T) {
	b, _, rg := fixture(t, scenarioFile(obj{
		"name": "widgets-gen-probe", "kind": "probe",
		"setup": []any{},
		"tests": []any{obj{
			"name": "ListThings", "op": "ListThings",
			"call":   obj{"op": "ListThings", "params": obj{}},
			"assert": []any{obj{"kind": "responseField", "checks": obj{"$.Things": obj{"isList": true}}}},
		}},
		"teardown": []any{},
	}))
	if _, hasSetup := b.Setup(rg); hasSetup {
		t.Error("a probe group has nothing to set up")
	}
	if _, hasTeardown := b.Teardown(rg); hasTeardown {
		t.Error("a probe group has nothing to tear down")
	}
}

// ─── claiming ─────────────────────────────────────────────────────────────────

func TestResolveClaimsOnlyWhatItCanRun(t *testing.T) {
	b, _, rg := fixture(t, scenarioFile(lifecycle("widgets-gen-thing", obj{
		"name": "CreateThing", "op": "CreateThing",
		"call":   obj{"op": "CreateThing", "params": obj{}},
		"assert": []any{obj{"kind": "responseField", "checks": obj{"$.Id": obj{"nonEmpty": true}}}},
	})))

	if _, claimed := b.Resolve(registry.RegistryGroup{Name: "sqs-queues"}, registry.RegistryTest{Name: "CreateQueue"}); claimed {
		t.Error("a group with no scenario is not the backend's")
	}
	if _, claimed := b.Resolve(rg, registry.RegistryTest{Name: "NotInTheFile"}); claimed {
		t.Error("a test the scenario file does not declare is not the backend's")
	}
	if _, claimed := b.Resolve(rg, registry.RegistryTest{Name: "CreateThing"}); !claimed {
		t.Error("a declared test is the backend's")
	}
}

// TestUnreadableScenarioFailsLoudly: the group is scoped to this suite, so the
// only honest result is a failure naming the file — never a skip, and never the
// loader's "no scenario backend", which would blame the wrong thing.
func TestUnreadableScenarioFailsLoudly(t *testing.T) {
	b := New(t.TempDir())
	b.run = &fakeRunner{script: map[string][]fakeResult{}}
	rg := registry.RegistryGroup{Name: "widgets-gen-thing", Scenario: "compat/model/scenarios/missing.json"}

	fn, claimed := b.Resolve(rg, registry.RegistryTest{Name: "CreateThing"})
	if !claimed {
		t.Fatal("a group that names a scenario is the backend's, readable or not")
	}
	err := fn(context.Background(), harness.NewTestContext("http://e", "us-east-1", "run1"))
	if err == nil || !strings.Contains(err.Error(), "missing.json") {
		t.Errorf("failure = %v, want one naming the file", err)
	}
	if setupFn, hasSetup := b.Setup(rg); !hasSetup {
		t.Error("the load error must reach setup too")
	} else if setupFn(context.Background(), harness.NewTestContext("http://e", "us-east-1", "run1")) == nil {
		t.Error("setup must fail when the scenario cannot be read")
	}
}

// ─── unimplemented classification ─────────────────────────────────────────────

// TestUnimplementedSurvivesTheFailureMessage is the probe path: a 501 must
// still read as unimplemented after the interpreter has wrapped it, and an
// ordinary assertion failure must never read as one.
func TestUnimplementedSurvivesTheFailureMessage(t *testing.T) {
	b, fake, rg := fixture(t, scenarioFile(lifecycle("widgets-gen-probe", obj{
		"name": "ProbeThing", "op": "ProbeThing",
		"call":   obj{"op": "ProbeThing", "params": obj{"Id": "nonexistent"}},
		"assert": []any{obj{"kind": "responseField", "checks": obj{"$.Id": obj{"nonEmpty": true}}}},
	})))
	fake.script["probe-thing"] = []fakeResult{{err: errors.New(
		"aws widgets probe-thing: exit status 254: An error occurred (501) when calling the ProbeThing operation")}}

	err := runOneTest(t, b, rg, "ProbeThing")
	if err == nil {
		t.Fatal("want a failure")
	}
	if !harness.IsUnimplemented(err) {
		t.Errorf("a 501 must classify as unimplemented, got %v", err)
	}

	b2, fake2, rg2 := fixture(t, scenarioFile(lifecycle("widgets-gen-thing", obj{
		"name": "GetThing", "op": "GetThing",
		"call":   obj{"op": "GetThing", "params": obj{}},
		"assert": []any{obj{"kind": "responseField", "checks": obj{"$.Id": obj{"nonEmpty": true}}}},
	})))
	fake2.script["get-thing"] = []fakeResult{ok(obj{"Id": ""})}
	err = runOneTest(t, b2, rg2, "GetThing")
	if err == nil {
		t.Fatal("want a failure")
	}
	if harness.IsUnimplemented(err) {
		t.Errorf("an assertion failure must not classify as unimplemented: %v", err)
	}
}

// TestUnimplementedIgnoresA501InsideTheParams is the fault the sentinel exists
// for. Field 3 of every failure message is the params JSON that was sent, so a
// run id, an account number, a port or a resource name carrying "501" used to
// make the substring heuristic report a perfectly ordinary failure — a real
// compatibility gap — as `unimplemented`, where nothing counts it.
func TestUnimplementedIgnoresA501InsideTheParams(t *testing.T) {
	build := func(id string) (*Backend, *fakeRunner, registry.RegistryGroup) {
		return fixture(t, scenarioFile(lifecycle("widgets-gen-thing", obj{
			"name": "GetThing", "op": "GetThing",
			"call":   obj{"op": "GetThing", "params": obj{"Id": id}},
			"assert": []any{obj{"kind": "responseField", "checks": obj{"$.Id": obj{"nonEmpty": true}}}},
		})))
	}

	// A run id with "501" in its hex, and a plain not-found from the service.
	b, fake, rg := build("oc-501abcde-thing")
	fake.script["get-thing"] = []fakeResult{awsErr("ResourceNotFoundException")}
	err := runOneTest(t, b, rg, "GetThing")
	if err == nil {
		t.Fatal("want a failure")
	}
	if !strings.Contains(err.Error(), "oc-501abcde-thing") {
		t.Fatalf("the params must still be in the message — that is why this is a hazard: %v", err)
	}
	if harness.IsUnimplemented(err) {
		t.Errorf("a 501 inside the params says nothing about the status: %v", err)
	}

	// The same params, and this time the CLI really did report a 501.
	b, fake, rg = build("oc-501abcde-thing")
	fake.script["get-thing"] = []fakeResult{{err: errors.New(
		"aws widgets get-thing: exit status 254: An error occurred (501) when calling the GetThing operation")}}
	err = runOneTest(t, b, rg, "GetThing")
	if err == nil {
		t.Fatal("want a failure")
	}
	if !harness.IsUnimplemented(err) {
		t.Errorf("the CLI's own 501 must still classify as unimplemented: %v", err)
	}
	if !errors.Is(err, harness.ErrUnimplemented) {
		t.Errorf("the classification must be the sentinel, not the prose: %v", err)
	}
}

// ─── error-code matching ──────────────────────────────────────────────────────

// TestErrorClauseMatchesTheCodeExactly: containment over the CLI's whole
// message cannot tell a code from a code that ends with it, and the two are
// different errors from different branches of the service — an `absent` clause
// naming NotFoundException was satisfied by a ResourceNotFoundException, so the
// test passed while the emulator answered with something else.
func TestErrorClauseMatchesTheCodeExactly(t *testing.T) {
	cases := []struct {
		name      string
		shape     string
		code      string
		cliErr    error
		wantMatch bool
	}{
		{
			name: "the same code matches", shape: "ResourceNotFoundException", code: "ResourceNotFoundException",
			cliErr: awsErr("ResourceNotFoundException").err, wantMatch: true,
		},
		{
			name: "a longer code is not the one named", shape: "NotFoundException", code: "NotFoundException",
			cliErr: awsErr("ResourceNotFoundException").err, wantMatch: false,
		},
		{
			name: "the wire code matches when the shape does not", shape: "QueueDoesNotExist",
			code:   "AWS.SimpleQueueService.NonExistentQueue",
			cliErr: awsErr("AWS.SimpleQueueService.NonExistentQueue").err, wantMatch: true,
		},
		{
			name: "an unrelated code does not match", shape: "ValidationException", code: "ValidationException",
			cliErr: awsErr("AccessDeniedException").err, wantMatch: false,
		},
		{
			name:  "a __type in a JSON body the CLI echoed, namespace and all",
			shape: "QueueDoesNotExist", code: "QueueDoesNotExist",
			cliErr:    errors.New(`aws sqs get-queue-url: exit status 255: {"__type":"com.amazonaws.sqs#QueueDoesNotExist","message":"…"}`),
			wantMatch: true,
		},
		{
			name: "a Code member in a JSON body", shape: "NoSuchEntity", code: "NoSuchEntity",
			cliErr:    errors.New(`aws iam get-role: exit status 255: {"Code":"NoSuchEntity","Message":"…"}`),
			wantMatch: true,
		},
		{
			// The nested position of compat/model/README.md § Errors, in the
			// spelling a JSON body puts it in.
			name: "a nested Error.Code in a JSON body", shape: "NoSuchEntity", code: "NoSuchEntity",
			cliErr:    errors.New(`aws iam get-role: exit status 255: {"Error":{"Type":"Sender","Code":"NoSuchEntity","Message":"…"}}`),
			wantMatch: true,
		},
		{
			// The same position on the wire an AWS Query service really uses.
			// The old extractor was a regex over the serialized message and
			// could not read this at all.
			name:  "the Code of a Query ErrorResponse envelope the CLI echoed",
			shape: "NoSuchEntity", code: "NoSuchEntity",
			cliErr: errors.New(`aws iam get-group: exit status 255: <ErrorResponse><Error>` +
				`<Type>Sender</Type><Code>NoSuchEntity</Code><Message>…</Message></Error>` +
				`<RequestId>r-1</RequestId></ErrorResponse>`),
			wantMatch: true,
		},
		{
			// REST XML's bare <Error> root, which states the code at the top
			// level of the same body.
			name:  "the Code of a bare REST XML Error the CLI echoed",
			shape: "NoSuchBucket", code: "NoSuchBucket",
			cliErr: errors.New(`aws s3api list-objects-v2: exit status 255: ` +
				`<Error><Code>NoSuchBucket</Code><Message>…</Message></Error>`),
			wantMatch: true,
		},
		{
			// EC2 writes a third dialect, and botocore folds it into the same
			// Error node the other two use.
			name:  "the Code of an EC2 Response envelope the CLI echoed",
			shape: "InvalidVpcID.NotFound", code: "InvalidVpcID.NotFound",
			cliErr: errors.New(`aws ec2 describe-vpcs: exit status 255: <Response><Errors><Error>` +
				`<Code>InvalidVpcID.NotFound</Code><Message>…</Message></Error></Errors>` +
				`<RequestID>r-1</RequestID></Response>`),
			wantMatch: true,
		},
		{
			// The fault an ErrorResponse carries beside the code is not a
			// code, and a structural read is what keeps the two apart.
			name: "the Type of a Query envelope is not its code", shape: "Sender", code: "Sender",
			cliErr: errors.New(`aws iam get-group: exit status 255: <ErrorResponse><Error>` +
				`<Type>Sender</Type><Code>NoSuchEntity</Code></Error></ErrorResponse>`),
			wantMatch: false,
		},
		{
			// XML that is not an error envelope states no code, whatever
			// elements it happens to contain — a proxy's HTML error page is
			// the case this excludes.
			name: "a Code element in XML that is not an error envelope", shape: "NoSuchEntity", code: "NoSuchEntity",
			cliErr:    errors.New(`aws iam get-group: exit status 255: <html><body><Code>NoSuchEntity</Code></body></html>`),
			wantMatch: false,
		},
		{
			name:  "a code named only in the message text is not the error's code",
			shape: "ValidationException", code: "ValidationException",
			cliErr: errors.New(`aws widgets create-thing: exit status 254: An error occurred (AccessDeniedException) ` +
				`when calling the CreateThing operation: not a ValidationException`),
			wantMatch: false,
		},
		{
			// Nothing this parser recognises — a CLI that died before the wire.
			// There is no containment fallback: the message names the code but
			// states none, so the clause fails naming the raw stderr rather
			// than accepting prose as evidence of the error.
			name:  "no code surface at all does not match, however the message reads",
			shape: "ValidationError", code: "ValidationError",
			cliErr:    errors.New(`aws widgets create-thing: exit status 252: Invalid length for parameter Name, ValidationError`),
			wantMatch: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := matchesError(tc.cliErr, &ErrorClause{Shape: tc.shape, Code: tc.code})
			if got != tc.wantMatch {
				t.Errorf("matchesError = %v, want %v for %v", got, tc.wantMatch, tc.cliErr)
			}
		})
	}
}

// TestErrorCodeClauseRejectsANearMissEndToEnd runs the same distinction through
// a whole test: matchesError on its own does not prove the clause uses it.
func TestErrorCodeClauseRejectsANearMissEndToEnd(t *testing.T) {
	b, fake, rg := fixture(t, scenarioFile(lifecycle("widgets-gen-thing", obj{
		"name": "GetThingAbsent", "op": "GetThing",
		"call": obj{"op": "GetThing", "params": obj{"Id": "absent"}},
		"assert": []any{
			obj{"kind": "errorCode", "error": obj{"shape": "NotFoundException", "code": "NotFoundException"}},
		},
	})))
	fake.script["get-thing"] = []fakeResult{awsErr("ResourceNotFoundException")}

	err := runOneTest(t, b, rg, "GetThingAbsent")
	if err == nil {
		t.Fatal("ResourceNotFoundException is not the NotFoundException the clause names")
	}
	if !strings.Contains(err.Error(), "ResourceNotFoundException") {
		t.Errorf("the failure must quote what came back: %v", err)
	}
}

// ─── cancellation ─────────────────────────────────────────────────────────────

func TestCancelledContextStopsBeforeTheNextCall(t *testing.T) {
	b, fake, rg := fixture(t, scenarioFile(lifecycle("widgets-gen-thing", obj{
		"name": "GetThing", "op": "GetThing",
		"call":   obj{"op": "GetThing", "params": obj{}},
		"assert": []any{obj{"kind": "responseField", "checks": obj{"$.Id": obj{"nonEmpty": true}}}},
	})))
	fake.script["get-thing"] = []fakeResult{ok(obj{"Id": "t-1"})}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	fn, _ := b.Resolve(rg, registry.RegistryTest{Name: "GetThing"})
	if err := fn(ctx, harness.NewTestContext("http://e", "us-east-1", "run1")); err == nil {
		t.Fatal("want a cancellation error")
	}
	if len(fake.calls) != 0 {
		t.Errorf("a cancelled context must spawn nothing, got %v", fake.ops())
	}
}
