//go:build dev

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// The committed corpus — compat/model and compat/suites/registry.generated.json.
//
// These are the offline analogue of the shape snapshot's sha gate: an
// ordinary pull request cannot run the AWS models, but it can prove the
// committed scenarios, gaps and registry are byte-for-byte what the generator
// produces from the committed recipes, values and shapes.

var repoRoot = filepath.Join("..", "..")

func TestCommittedCorpus_isInSyncWithTheGenerator(t *testing.T) {
	skipWithoutVendoredSDK(t)
	c, err := loadCorpus(repoRoot)
	if err != nil {
		t.Fatalf("load corpus: %v", err)
	}
	_, outputs, err := generateAll(repoRoot, c)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if err := outputs.check(repoRoot); err != nil {
		t.Fatal(err)
	}
	if err := checkStaleScenarios(repoRoot, outputs, true); err != nil {
		t.Fatal(err)
	}
}

func TestCommittedCorpus_validatesAgainstItsSchemas(t *testing.T) {
	model, err := loadSchemas(filepath.Join(repoRoot, filepath.FromSlash(modelDir)))
	if err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(repoRoot, filepath.FromSlash(scenarioDir)))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		contents := readFile(t, filepath.Join(repoRoot, filepath.FromSlash(scenarioDir), entry.Name()))
		if err := model.validate(schemaScenario, contents); err != nil {
			t.Errorf("%s: %v", entry.Name(), err)
		}
		var s scenario
		if err := decodeStrict(contents, &s); err != nil {
			t.Errorf("%s: %v", entry.Name(), err)
		} else if err := validateScenario(&s); err != nil {
			t.Errorf("%s: %v", entry.Name(), err)
		}
	}
	if err := model.validate(schemaGaps, readFile(t, filepath.Join(repoRoot, filepath.FromSlash(gapsPath)))); err != nil {
		t.Errorf("gaps.json: %v", err)
	}
	// The soak ledger is a curated input, and the schema is the only thing
	// standing between a hand edit and a group gated on no evidence.
	if err := model.validate(schemaPromotions, readFile(t, filepath.Join(repoRoot, filepath.FromSlash(promotionsPath)))); err != nil {
		t.Errorf("promotions.json: %v", err)
	}

	// The registry validates against the compat suite schemas, which are the
	// loaders' contract, not this package's. The generator holds it to the
	// same schema before it writes, so this is the belt to that braces.
	suites, err := loadRegistrySchemas(filepath.Join(repoRoot, "compat", "suites"))
	if err != nil {
		t.Fatal(err)
	}
	if err := suites.validate(schemaGeneratedRegistry, readFile(t, filepath.Join(repoRoot, filepath.FromSlash(registryPath)))); err != nil {
		t.Errorf("registry.generated.json: %v", err)
	}
}

// TestRegistryIsEmptyExactlyWhileNoBackendExists pins the tie between the
// backend table and the registry, not the file's current emptiness: G0's
// "an empty generated registry behaves as today" gate holds until a suite
// gains a scenario backend, and the moment one does every group is
// registered with exactly that suite list.
func TestRegistryIsEmptyExactlyWhileNoBackendExists(t *testing.T) {
	_, gen := generateFixture(t)
	units := []*generation{gen}

	empty := buildRegistry(units, nil, nil, nil)
	if len(empty.Groups) != 0 {
		t.Fatalf("with no backend the registry must be empty, got %d groups", len(empty.Groups))
	}

	full := buildRegistry(units, []string{"python-sdk", "cli"}, nil, nil)
	if len(full.Groups) != len(gen.scenario.Groups) {
		t.Fatalf("with a backend every group is registered: got %d, want %d", len(full.Groups), len(gen.scenario.Groups))
	}
	for _, g := range full.Groups {
		if strings.Join(g.Suites, ",") != "cli,python-sdk" || !g.Generated || g.State != generatedStateCandidate || g.Scenario != scenarioPath("widgets") {
			t.Errorf("registered group %+v", g)
		}
		for _, tc := range g.Tests {
			if tc.Name == "" {
				t.Errorf("group %s has a nameless test", g.Name)
			}
		}
	}

	// The committed table decides the committed file.
	committed := buildRegistry(units, scenarioBackends, nil, nil)
	if (len(committed.Groups) == 0) != (len(scenarioBackends) == 0) {
		t.Fatalf("scenarioBackends=%v but the registry has %d groups", scenarioBackends, len(committed.Groups))
	}
}

func TestCheck_detectsAHandEdit(t *testing.T) {
	skipWithoutVendoredSDK(t)
	// Given: a copy of the corpus with one byte changed in a generated file.
	root := copyCorpus(t)
	path := filepath.Join(root, filepath.FromSlash(scenarioPath("sqs")))
	contents := readFile(t, path)
	edited := bytes.Replace(contents, []byte(`"maxAttempts": 30`), []byte(`"maxAttempts": 31`), 1)
	if bytes.Equal(edited, contents) {
		t.Fatal("the fixture edit did not apply")
	}
	writeFile(t, path, string(edited))

	// When: -check runs against it.
	var stdout, stderr bytes.Buffer
	code := run([]string{"-root", root, "-check"}, &stdout, &stderr)

	// Then: it fails naming the file, and writes nothing.
	if code != 1 || !strings.Contains(stderr.String(), scenarioPath("sqs")) {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	if !bytes.Equal(readFile(t, path), edited) {
		t.Fatal("-check overwrote the file")
	}

	// And: a plain run repairs it.
	if code := run([]string{"-root", root}, &stdout, &stderr); code != 0 {
		t.Fatalf("regenerate: code=%d stderr=%s", code, stderr.String())
	}
	if !bytes.Equal(readFile(t, path), contents) {
		t.Fatal("regeneration did not restore the committed bytes")
	}
}

func TestRun_removesAScenarioWhoseRecipeIsGone(t *testing.T) {
	skipWithoutVendoredSDK(t)
	root := copyCorpus(t)
	stale := filepath.Join(root, filepath.FromSlash(scenarioDir), "gone.json")
	writeFile(t, stale, "{}")
	var stdout, stderr bytes.Buffer
	if code := run([]string{"-root", root, "-check"}, &stdout, &stderr); code != 1 || !strings.Contains(stderr.String(), "gone.json") {
		t.Fatalf("-check accepted a stale scenario: code=%d stderr=%s", code, stderr.String())
	}
	if code := run([]string{"-root", root}, &stdout, &stderr); code != 0 {
		t.Fatalf("regenerate: %s", stderr.String())
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatal("stale scenario survived regeneration")
	}
}

// TestRun_refusesAServiceOutsideTheSnapshot names a service deliberately absent
// from models/aws/shapes-services.txt. It was kinesis until G6's kinesis-streams
// port (#1116) put that service in the snapshot; athena is the replacement, and
// a later wave adding it has to move this fixture again rather than delete the
// case — the rule it pins is that a recipe for an unlisted service stops the run
// naming the list, instead of reading as "this service has no operations".
func TestRun_refusesAServiceOutsideTheSnapshot(t *testing.T) {
	root := copyCorpus(t)
	writeFile(t, filepath.Join(root, filepath.FromSlash(recipesDir), "athena.json"),
		`{"service":"athena","resources":[{"id":"workgroup","create":{"op":"CreateWorkGroup"}}]}`)
	var stdout, stderr bytes.Buffer
	if code := run([]string{"-root", root}, &stdout, &stderr); code != 1 || !strings.Contains(stderr.String(), "shapes-services.txt") {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
}

// skipWithoutVendoredSDK skips a test that regenerates the *committed* corpus
// when the go-sdk suite module's dependencies cannot be resolved.
//
// Those tests run the Go emitter over the real services, so they need the real
// vendored SDK — the emitter spells each member as that SDK declares it — and
// a checkout that has never fetched the suite module's dependencies has no way
// to answer. Skipping there rather than failing is what keeps `go test`
// runnable offline; the unconditional gate is `make compat-model-check`, whose
// second command is `go run -tags dev ./cmd/compatgen -check` and which fails
// outright. Everything about the emitter that can be proved without the real
// SDK is proved against testdata/awssdk instead, and always runs.
func skipWithoutVendoredSDK(t *testing.T) {
	t.Helper()
	if err := vendoredSDKAvailable(); err != nil {
		t.Skipf("the go-sdk suite module's dependencies are not available: %v", err)
	}
}

var vendoredSDKAvailable = sync.OnceValue(func() error {
	_, err := newGoSDKTypes(filepath.Join(repoRoot, filepath.FromSlash(goSDKModuleDir))).service("SQS")
	return err
})

// copyCorpus copies the inputs and outputs the generator reads and writes
// into a temporary root, so a test can edit them.
//
// The go-sdk suite's go.mod and go.sum come with it: they are what pins the
// SDK the emitter resolves field types from, so a copy without them would
// generate a corpus the real run could not reproduce.
func copyCorpus(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, rel := range []string{modelDir, "compat/suites", shapesDir} {
		src := filepath.Join(repoRoot, filepath.FromSlash(rel))
		if err := filepath.WalkDir(src, func(path string, entry os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() {
				return nil
			}
			name := entry.Name()
			if !strings.HasSuffix(path, ".json") && !strings.HasSuffix(path, ".txt") && name != "go.mod" && name != "go.sum" {
				return nil
			}
			relPath, err := filepath.Rel(repoRoot, path)
			if err != nil {
				return err
			}
			dest := filepath.Join(root, relPath)
			if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
				return err
			}
			return os.WriteFile(dest, readFile(t, path), 0o644)
		}); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return contents
}

func writeFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestOrganizationsProbeGroupIsExactlyItsReads is the #1795 B2 claim, held
// over the real corpus rather than the fixture: making probes default-deny by
// verb left the organizations probe group exactly as the 29 curated
// `neverProbe` sentences had it.
//
// Two committed facts prove it without a "before" to compare against. Every
// operation the probe group holds is a read verb, so the verb rule admitted
// all of them; and every one of organizations' refusals is a curated
// `neverProbe` sentence, so the verb rule refused nothing the recipe had not
// already refused. Between them the set cannot have moved in either
// direction.
//
// It reads the committed files once. There is deliberately no loop that
// re-parses the corpus per operation.
func TestOrganizationsProbeGroupIsExactlyItsReads(t *testing.T) {
	schemas, err := loadSchemas(filepath.Join(repoRoot, filepath.FromSlash(modelDir)))
	if err != nil {
		t.Fatal(err)
	}
	recipes, err := loadRecipes(filepath.Join(repoRoot, filepath.FromSlash(recipesDir)), schemas)
	if err != nil {
		t.Fatal(err)
	}
	var organizations recipe
	for _, r := range recipes {
		if r.Service == "organizations" {
			organizations = r
		}
	}
	if len(organizations.NeverProbe) == 0 {
		t.Fatal("the organizations recipe has no neverProbe entries; this test is about them")
	}

	var s scenario
	if err := decodeStrict(readFile(t, filepath.Join(repoRoot, filepath.FromSlash(scenarioPath("organizations")))), &s); err != nil {
		t.Fatal(err)
	}
	probes := 0
	for _, g := range s.Groups {
		if g.Kind != groupProbe {
			continue
		}
		for _, tc := range g.Tests {
			probes++
			if !isReadVerb(tc.Op) {
				t.Errorf("%s sits in the probe group but is not a read verb", tc.Op)
			}
		}
	}
	if probes == 0 {
		t.Fatal("organizations has no probe group; this test is about it")
	}

	var gaps gapsDocument
	if err := decodeStrict(readFile(t, filepath.Join(repoRoot, filepath.FromSlash(gapsPath))), &gaps); err != nil {
		t.Fatal(err)
	}
	curated := 0
	for _, gp := range gaps.Gaps {
		if gp.Service != "organizations" {
			continue
		}
		why, named := organizations.NeverProbe[gp.Operation]
		if gp.Reason != reasonNeverProbe || !named {
			t.Errorf("organizations/%s is refused %q, which the recipe did not ask for", gp.Operation, gp.Reason)
			continue
		}
		if gp.Detail != why {
			t.Errorf("organizations/%s reports %q, not the recipe's own sentence", gp.Operation, gp.Detail)
		}
		curated++
	}
	if curated != len(organizations.NeverProbe) {
		t.Errorf("%d of the recipe's %d neverProbe sentences reached gaps.json", curated, len(organizations.NeverProbe))
	}
}

// TestClientInfoFor_prefersTheCanonicalModelIdentityOfAnAliasedService pins the
// one place a service key with two modeled identities could pick the wrong one.
//
// awsapi.Operations answers on the Overcast key, and "cloudwatch-events" is an
// alias of "eventbridge": both identities declare every EventBridge operation,
// so a lookup returns two manifest entries and the first in name order is the
// former name. Only SDKID differs, and SDKID is the field every backend derives
// a package and a client class from — "CloudWatch Events" spells
// aws_sdk_cloudwatchevents, AmazonCloudWatchEventsClient and
// @aws-sdk/client-cloudwatch-events, none of which any suite depends on. Taking
// entries[0] made the eventbridge-rules port fail to generate at all, which is
// the loud half; a service whose two identities differed only in a name a suite
// happened to have would have compiled and talked to the wrong package.
func TestClientInfoFor_prefersTheCanonicalModelIdentityOfAnAliasedService(t *testing.T) {
	// Given: the committed snapshot of a service the manifest models twice.
	model, err := loadModel(filepath.Join(repoRoot, filepath.FromSlash(shapesDir)), "eventbridge")
	if err != nil {
		t.Fatalf("load eventbridge model: %v", err)
	}

	// When: the naming header is assembled for the Overcast service key.
	client, err := clientInfoFor(model, "eventbridge")
	if err != nil {
		t.Fatalf("clientInfoFor: %v", err)
	}

	// Then: it carries the SDK id of the identity that *is* the key.
	if client.SDKID != "EventBridge" {
		t.Errorf("SDKID = %q, want %q — the cloudwatch-events alias was preferred over the canonical entry", client.SDKID, "EventBridge")
	}
	if client.EndpointPrefix != "events" || client.SigningName != "events" {
		t.Errorf("endpointPrefix/signingName = %q/%q, want events/events", client.EndpointPrefix, client.SigningName)
	}
	if client.TargetPrefix != "AWSEvents" {
		t.Errorf("targetPrefix = %q, want AWSEvents", client.TargetPrefix)
	}
}
