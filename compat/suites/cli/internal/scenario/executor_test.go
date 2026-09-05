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
