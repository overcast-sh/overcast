package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/overcast-sh/overcast/compat"
)

// ---------------------------------------------------------------------------
// The G0 acceptance gate: hand-written results and candidate-vs-gated state
// ---------------------------------------------------------------------------

// TestCheckedInGeneratedRegistryGatesByState is the G0 acceptance gate,
// asserted directly against the file that is actually checked in.
//
// It used to pin a temporal fact — every checked-in generated group is
// `candidate` — which held from G0 through G2 and broke the moment the first
// nightly promotion did its job (#1871 flips all nine pilot groups to
// `gated`). That was the point of writing it that way: the test was supposed
// to force this file to be revisited exactly once, at the first promotion.
// From here on the fact is not temporal, so the test pins the invariant
// instead, in two parts:
//
//  1. A hand-written result's gate verdict never depends on the generated
//     registry, whatever state its groups are in — concatenating the sibling
//     must be able to change nothing for a suite/group/test the sibling does
//     not name.
//  2. A generated group's own gate verdict depends only on its state: a
//     `candidate` gates nothing, a `gated` one gates exactly like a
//     hand-written group.
//
// Neither half is allowed to pass vacuously. Part 2 sources one group of each
// state from the checked-in file when both are present, and falls back to a
// fixture group for whichever state the file currently lacks (today: every
// checked-in group is `gated`, so `candidate` is synthesized) — never by
// skipping. It never asserts which specific groups are gated: that is
// `compatgen -check`'s job, since registry.generated.json is regenerated
// wholly from compat/model/promotions.json.
func TestCheckedInGeneratedRegistryGatesByState(t *testing.T) {
	gen, err := readGeneratedRegistry(repoPath(t, "compat", "suites", "registry.generated.json"))
	if err != nil {
		t.Fatalf("readGeneratedRegistry: %v", err)
	}

	// Part 1: a hand-written result is unaffected by the generated registry,
	// whatever it contains.
	report := reportWithResults(
		resultSpec{suite: "go-sdk", service: "s3", group: "s3-crud", test: "CreateBucket", status: compat.StatusPass},
		resultSpec{suite: "go-sdk", service: "s3", group: "s3-crud", test: "DeleteBucket", status: compat.StatusFail},
	)
	baseline := &compatBaseline{Version: baselineVersion, Entries: []baselineEntry{
		{Suite: "go-sdk", Service: "s3", Group: "s3-crud", Test: "CreateBucket", Status: compat.StatusPass},
		{Suite: "go-sdk", Service: "s3", Group: "s3-crud", Test: "DeleteBucket", Status: compat.StatusPass},
	}}

	candidates := gen.candidateGroups()
	none := candidateSet{}

	withFile := compareBaselineWith(baseline, report, flakySet{}, candidates)
	withoutFile := compareBaselineWith(baseline, report, flakySet{}, none)
	if !equalStrings(withFile, withoutFile) {
		t.Errorf("--compare-baseline differs with the checked-in generated registry:\n with = %#v\n without = %#v", withFile, withoutFile)
	}
	if len(withFile) != 1 {
		t.Errorf("regressions = %#v, want the DeleteBucket regression", withFile)
	}

	failWith := failuresOverLimit(report, flakySet{}, candidates, 0)
	failWithout := failuresOverLimit(report, flakySet{}, none, 0)
	if !equalStrings(failWith, failWithout) {
		t.Errorf("--max-failures differs with the checked-in generated registry:\n with = %#v\n without = %#v", failWith, failWithout)
	}
	if len(failWith) != 1 {
		t.Errorf("failures = %#v, want the DeleteBucket failure", failWith)
	}

	updWith := updateBaselineWith(baseline, report, flakySet{}, candidates)
	updWithout := updateBaselineWith(baseline, report, flakySet{}, none)
	if len(updWith.Entries) != len(updWithout.Entries) {
		t.Errorf("--update-baseline differs with the checked-in generated registry: %d vs %d entries",
			len(updWith.Entries), len(updWithout.Entries))
	}

	// Part 2: a candidate group gates nothing; a gated group gates exactly
	// like a hand-written one.
	cand, gated, combined := pickOneOfEachState(gen)
	combinedCandidates := combined.candidateGroups()

	genReport := reportWithResults(
		resultSpec{suite: cand.suite, service: cand.service, group: cand.group, test: cand.test, status: compat.StatusFail},
		resultSpec{suite: gated.suite, service: gated.service, group: gated.group, test: gated.test, status: compat.StatusFail},
	)
	genBaseline := &compatBaseline{Version: baselineVersion, Entries: []baselineEntry{
		{Suite: cand.suite, Service: cand.service, Group: cand.group, Test: cand.test, Status: compat.StatusPass},
		{Suite: gated.suite, Service: gated.service, Group: gated.group, Test: gated.test, Status: compat.StatusPass},
	}}
	candKey := cand.suite + "/" + cand.group + "/" + cand.test
	gatedKey := gated.suite + "/" + gated.group + "/" + gated.test

	failures := failuresOverLimit(genReport, flakySet{}, combinedCandidates, 0)
	if len(failures) != 1 || !strings.Contains(failures[0], gatedKey) {
		t.Errorf("failures = %#v, want only the gated group's failure (%s)", failures, gatedKey)
	}
	for _, f := range failures {
		if strings.Contains(f, candKey) {
			t.Errorf("failures = %#v, candidate group %s must not be counted", failures, candKey)
		}
	}

	regressions := compareBaselineWith(genBaseline, genReport, flakySet{}, combinedCandidates)
	if len(regressions) != 1 || !strings.Contains(regressions[0], gatedKey) {
		t.Errorf("regressions = %#v, want only the gated group's regression (%s)", regressions, gatedKey)
	}
	for _, r := range regressions {
		if strings.Contains(r, candKey) {
			t.Errorf("regressions = %#v, candidate group %s must not be reported", regressions, candKey)
		}
	}

	updated := updateBaselineWith(&compatBaseline{Version: baselineVersion}, genReport, flakySet{}, combinedCandidates)
	entries := baselineEntryMap(updated.Entries)
	if _, ok := entries[candKey]; ok {
		t.Errorf("candidate group %s was recorded in the baseline: %#v", candKey, updated.Entries)
	}
	if _, ok := entries[gatedKey]; !ok {
		t.Errorf("gated group %s is missing from the baseline: %#v", gatedKey, updated.Entries)
	}
}

// generatedGroupSample names one test belonging to one generated group, in
// enough detail to build report and baseline rows for it.
type generatedGroupSample struct {
	service, group, suite, test string
}

func sampleGroup(g generatedGroup) generatedGroupSample {
	return generatedGroupSample{
		service: g.Service,
		group:   g.Name,
		suite:   g.Suites[0],
		test:    g.Tests[0].Name,
	}
}

// pickOneOfEachState returns a sample of one candidate group and one gated
// group, plus the registry they were sourced from. It prefers groups already
// in the checked-in file; whichever state the file currently has none of is
// filled by a fixture group appended to a copy of the registry, so the case
// this feeds never passes vacuously for lack of a state to compare against.
//
// The fixture names are distinctive on purpose (they cannot collide with a
// `*-gen-*` name cmd/compatgen would produce) and are never asserted on by
// name — only by the state they were constructed to hold.
func pickOneOfEachState(gen *generatedRegistry) (cand, gated generatedGroupSample, combined *generatedRegistry) {
	out := &generatedRegistry{Version: gen.Version, Comment: gen.Comment, Groups: append([]generatedGroup(nil), gen.Groups...)}

	var candIdx, gatedIdx = -1, -1
	for i, g := range out.Groups {
		switch g.State {
		case generatedStateCandidate:
			if candIdx == -1 {
				candIdx = i
			}
		case generatedStateGated:
			if gatedIdx == -1 {
				gatedIdx = i
			}
		}
	}
	if candIdx == -1 {
		out.Groups = append(out.Groups, generatedGroup{
			Service: "sqs", Name: "fixture-candidate-for-gate-test", Generated: true,
			State: generatedStateCandidate, Suites: []string{"python-sdk"},
			Tests: []generatedTest{{Name: "SendMessage"}},
		})
		candIdx = len(out.Groups) - 1
	}
	if gatedIdx == -1 {
		out.Groups = append(out.Groups, generatedGroup{
			Service: "sqs", Name: "fixture-gated-for-gate-test", Generated: true,
			State: generatedStateGated, Suites: []string{"python-sdk"},
			Tests: []generatedTest{{Name: "ReceiveMessage"}},
		})
		gatedIdx = len(out.Groups) - 1
	}
	return sampleGroup(out.Groups[candIdx]), sampleGroup(out.Groups[gatedIdx]), out
}

// TestCheckedInGeneratedRegistryLeavesParityUnchanged is the parity half of
// the same gate: concatenating the sibling must leave the checker's verdict
// identical, including the reverse (unregistered-result) direction. It held
// trivially while the file was empty; it holds now because `suites` scopes a
// generated group to the backends that can run it, so a suite none of them
// names sees no change — which is the property worth pinning, and the one
// TestGeneratedSuiteScopingAddsNoParityDebt proves on a fixture.
//
// The suite is chosen from the file rather than named here, because which
// suites have a scenario backend moves as the backends land
// (cmd/compatgen/registry.go's scenarioBackends). Pinning one by name made this
// case fail the day rust-sdk got an emitter, which is a change in the
// generator's coverage and not in the property under test.
func TestCheckedInGeneratedRegistryLeavesParityUnchanged(t *testing.T) {
	suite := suiteWithNoGeneratedGroups(t)

	// Given: the hand-written registry and a run against it.
	report := reportWithResults(
		resultSpec{suite: suite, service: "s3", group: "s3-crud", test: "CreateBucket", status: compat.StatusPass},
	)
	addSkip(report, suite, "s3", "s3-crud", "DeleteBucket", notImplementedSentinel(suite))

	hand := withHandWrittenCounterparts(t, testRegistry())
	handOnly := computeParity(hand, report, []string{suite})

	// When: the checked-in sibling is concatenated in.
	concat, err := readParityRegistries(
		writeTempJSON(t, "registry.json", hand),
		repoPath(t, "compat", "suites", "registry.generated.json"))
	if err != nil {
		t.Fatalf("readParityRegistries: %v", err)
	}
	withSibling := computeParity(concat, report, []string{suite})

	// Then: nothing moves.
	if withSibling.Expected != handOnly.Expected || withSibling.Implemented != handOnly.Implemented {
		t.Errorf("expected/implemented = %d/%d, want %d/%d",
			withSibling.Expected, withSibling.Implemented, handOnly.Expected, handOnly.Implemented)
	}
	if len(withSibling.Debt) != len(handOnly.Debt) {
		t.Errorf("debt = %#v, want %#v", withSibling.Debt, handOnly.Debt)
	}
	if len(withSibling.Unregistered) != 0 {
		t.Errorf("unregistered = %#v, want none", withSibling.Unregistered)
	}
}

// withHandWrittenCounterparts gives a fixture hand-written registry the groups
// the checked-in generated registry names on the other side of the join: the
// native of every shadow, and the ported group of every index entry.
//
// lintGeneratedRegistry requires a shadow group's native to exist and to
// declare the same tests, and a ported index entry's group to exist and to
// carry the same `scenario` — which the real pair of files satisfies by
// construction. A case that pairs the *real* generated registry with a
// *fixture* hand-written one has to satisfy it too, or it fails on the
// fixture's incompleteness rather than on the property it is testing.
func withHandWrittenCounterparts(t *testing.T, hand *parityRegistry) *parityRegistry {
	t.Helper()
	gen, err := readGeneratedRegistry(repoPath(t, "compat", "suites", "registry.generated.json"))
	if err != nil {
		t.Fatalf("readGeneratedRegistry: %v", err)
	}
	for _, g := range gen.Groups {
		if g.ShadowOf == "" {
			continue
		}
		hand.Groups = append(hand.Groups, parityGroup{Service: g.Service, Name: g.ShadowOf, Tests: g.Tests})
	}
	// The index carries no service, and the join does not read one.
	for _, p := range gen.Ported {
		hand.Groups = append(hand.Groups, parityGroup{Name: p.Group, Scenario: p.Scenario})
	}
	return hand
}

// suiteWithNoGeneratedGroups names a suite the checked-in generated registry
// scopes no group to. It is what the case above needs: a suite for which
// concatenating the sibling can change nothing at all.
//
// The candidates are every suite the repository has a baseline shard for, minus
// whichever of them the file names. A checkout where every suite has a scenario
// backend has no such suite, and the case says so rather than passing on a
// vacuous choice.
func suiteWithNoGeneratedGroups(t *testing.T) string {
	t.Helper()
	gen, err := readGeneratedRegistry(repoPath(t, "compat", "suites", "registry.generated.json"))
	if err != nil {
		t.Fatalf("readGeneratedRegistry: %v", err)
	}
	scoped := map[string]bool{}
	for _, g := range gen.Groups {
		for _, suite := range g.Suites {
			scoped[suite] = true
		}
	}
	for _, suite := range []string{"cdk", "java-sdk", "dotnet-sdk", "rust-sdk", "cli", "go-sdk", "node-js-sdk", "python-sdk"} {
		if !scoped[suite] {
			return suite
		}
	}
	t.Fatal("every suite has a scenario backend; this case needs one the generated registry scopes nothing to")
	return ""
}

// TestReadGeneratedRegistryTolueratesMissingFile pins the "missing = empty"
// contract. Every suite image and every CI job that predates the sibling file
// must keep working, and a checkout that has not fetched it must not red the
// gate.
func TestReadGeneratedRegistryToleratesMissingFile(t *testing.T) {
	// Given: a path that does not exist.
	missing := filepath.Join(t.TempDir(), "does-not-exist.json")

	// When: it is read.
	gen, err := readGeneratedRegistry(missing)

	// Then: it reads as an empty registry, not an error.
	if err != nil {
		t.Fatalf("readGeneratedRegistry(missing) error = %v, want nil", err)
	}
	if len(gen.Groups) != 0 {
		t.Fatalf("groups = %d, want 0", len(gen.Groups))
	}
}

// ---------------------------------------------------------------------------
// candidate vs gated: the inverse of flaky.json
// ---------------------------------------------------------------------------

// generatedFixture is a synthetic generated registry: one candidate group and
// one gated group. Deliberately synthetic — the checked-in file stays empty
// through G0, and a fixture is what lets the gate semantics be proven before
// cmd/compatgen can populate anything.
func generatedFixture() *generatedRegistry {
	return &generatedRegistry{
		Version: generatedRegistryVersion,
		Groups: []generatedGroup{
			{
				Service:   "sqs",
				Name:      "sqs-generated-candidate",
				Generated: true,
				Scenario:  "compat/scenarios/sqs/candidate.json",
				State:     generatedStateCandidate,
				Suites:    []string{"python-sdk"},
				Tests:     []generatedTest{{Name: "SendMessage"}},
			},
			{
				Service:   "sqs",
				Name:      "sqs-generated-gated",
				Generated: true,
				Scenario:  "compat/scenarios/sqs/gated.json",
				State:     generatedStateGated,
				Suites:    []string{"python-sdk"},
				Tests:     []generatedTest{{Name: "ReceiveMessage"}},
			},
		},
	}
}

func TestCandidateFailureDoesNotTripMaxFailures(t *testing.T) {
	// Given: a candidate group and a gated group that both fail.
	report := reportWithResults(
		resultSpec{suite: "python-sdk", service: "sqs", group: "sqs-generated-candidate", test: "SendMessage", status: compat.StatusFail},
		resultSpec{suite: "python-sdk", service: "sqs", group: "sqs-generated-gated", test: "ReceiveMessage", status: compat.StatusFail},
	)

	// When: the absolute gate runs with --max-failures 0.
	failures := failuresOverLimit(report, flakySet{}, generatedFixture().candidateGroups(), 0)

	// Then: only the gated group's failure counts. A candidate has not entered
	// the gate yet, so it can never red the build.
	if len(failures) != 1 {
		t.Fatalf("failures = %#v, want only the gated group", failures)
	}
	if !strings.Contains(failures[0], "sqs-generated-gated/ReceiveMessage") {
		t.Errorf("failure = %q, want the gated group", failures[0])
	}
}

func TestCandidateFailureIsNotABaselineRegression(t *testing.T) {
	// Given: a baseline recording both generated groups passing, and a run
	// where both now fail.
	baseline := &compatBaseline{Version: baselineVersion, Entries: []baselineEntry{
		{Suite: "python-sdk", Service: "sqs", Group: "sqs-generated-candidate", Test: "SendMessage", Status: compat.StatusPass},
		{Suite: "python-sdk", Service: "sqs", Group: "sqs-generated-gated", Test: "ReceiveMessage", Status: compat.StatusPass},
	}}
	report := reportWithResults(
		resultSpec{suite: "python-sdk", service: "sqs", group: "sqs-generated-candidate", test: "SendMessage", status: compat.StatusFail},
		resultSpec{suite: "python-sdk", service: "sqs", group: "sqs-generated-gated", test: "ReceiveMessage", status: compat.StatusFail},
	)

	// When: the relative gate runs.
	regressions := compareBaselineWith(baseline, report, flakySet{}, generatedFixture().candidateGroups())

	// Then: only the gated group regresses.
	if len(regressions) != 1 {
		t.Fatalf("regressions = %#v, want only the gated group", regressions)
	}
	if !strings.Contains(regressions[0], "sqs-generated-gated/ReceiveMessage") {
		t.Errorf("regression = %q, want the gated group", regressions[0])
	}
}

func TestCandidateNewFailureIsNotABaselineRegression(t *testing.T) {
	// Given: an empty-of-these baseline — the case that matters most, since a
	// freshly generated candidate is by definition absent from the baseline —
	// and a run where the candidate fails.
	baseline := &compatBaseline{Version: baselineVersion, Entries: []baselineEntry{
		{Suite: "python-sdk", Service: "sqs", Group: "sqs-basic", Test: "SendMessage", Status: compat.StatusPass},
	}}
	report := reportWithResults(
		resultSpec{suite: "python-sdk", service: "sqs", group: "sqs-basic", test: "SendMessage", status: compat.StatusPass},
		resultSpec{suite: "python-sdk", service: "sqs", group: "sqs-generated-candidate", test: "SendMessage", status: compat.StatusFail},
	)

	// When: the relative gate runs.
	regressions := compareBaselineWith(baseline, report, flakySet{}, generatedFixture().candidateGroups())

	// Then: the new-failure arm does not fire either. Both directions of the
	// baseline gate have to skip candidates, or a model refresh that adds a
	// failing candidate reds every PR until someone quarantines it — exactly
	// the outcome candidacy exists to prevent.
	if len(regressions) != 0 {
		t.Fatalf("regressions = %#v, want none", regressions)
	}
}

func TestUpdateBaselineIgnoresCandidates(t *testing.T) {
	// Given: a candidate and a gated group that both pass.
	baseline := &compatBaseline{Version: baselineVersion}
	report := reportWithResults(
		resultSpec{suite: "python-sdk", service: "sqs", group: "sqs-generated-candidate", test: "SendMessage", status: compat.StatusPass},
		resultSpec{suite: "python-sdk", service: "sqs", group: "sqs-generated-gated", test: "ReceiveMessage", status: compat.StatusPass},
	)

	// When: the baseline is ratcheted forward.
	updated := updateBaselineWith(baseline, report, flakySet{}, generatedFixture().candidateGroups())

	// Then: only the gated group is recorded. Promoting a candidate on the run
	// it happened to pass would put it under the gate without the soak that
	// promotion to "gated" is supposed to require.
	entries := baselineEntryMap(updated.Entries)
	if _, ok := entries["python-sdk/sqs-generated-candidate/SendMessage"]; ok {
		t.Errorf("candidate was promoted into the baseline: %#v", updated.Entries)
	}
	if _, ok := entries["python-sdk/sqs-generated-gated/ReceiveMessage"]; !ok {
		t.Errorf("gated group missing from the baseline: %#v", updated.Entries)
	}
}

func TestGatedFailureTripsBothGates(t *testing.T) {
	// Given: a gated generated group that fails and is not in the baseline.
	baseline := &compatBaseline{Version: baselineVersion, Entries: []baselineEntry{
		{Suite: "python-sdk", Service: "sqs", Group: "sqs-basic", Test: "SendMessage", Status: compat.StatusPass},
	}}
	report := reportWithResults(
		resultSpec{suite: "python-sdk", service: "sqs", group: "sqs-basic", test: "SendMessage", status: compat.StatusPass},
		resultSpec{suite: "python-sdk", service: "sqs", group: "sqs-generated-gated", test: "ReceiveMessage", status: compat.StatusFail},
	)
	candidates := generatedFixture().candidateGroups()

	// When: both gates run.
	regressions := compareBaselineWith(baseline, report, flakySet{}, candidates)
	failures := failuresOverLimit(report, flakySet{}, candidates, 0)

	// Then: a promoted group is gated exactly like a hand-written one.
	if len(regressions) != 1 {
		t.Errorf("regressions = %#v, want the new gated failure", regressions)
	}
	if len(failures) != 1 {
		t.Errorf("failures = %#v, want the gated failure", failures)
	}
}

// ---------------------------------------------------------------------------
// suites scoping: generated groups add no parity debt outside their backends
// ---------------------------------------------------------------------------

func TestGeneratedSuiteScopingAddsNoParityDebt(t *testing.T) {
	// Given: a run of two suites where only python-sdk — the one backend the
	// generated groups are scoped to — reports them.
	report := reportWithResults(
		resultSpec{suite: "python-sdk", service: "s3", group: "s3-crud", test: "CreateBucket", status: compat.StatusPass},
		resultSpec{suite: "python-sdk", service: "s3", group: "s3-crud", test: "DeleteBucket", status: compat.StatusPass},
		resultSpec{suite: "python-sdk", service: "sqs", group: "sqs-generated-candidate", test: "SendMessage", status: compat.StatusPass},
		resultSpec{suite: "python-sdk", service: "sqs", group: "sqs-generated-gated", test: "ReceiveMessage", status: compat.StatusPass},
		resultSpec{suite: "go-sdk", service: "s3", group: "s3-crud", test: "CreateBucket", status: compat.StatusPass},
		resultSpec{suite: "go-sdk", service: "s3", group: "s3-crud", test: "DeleteBucket", status: compat.StatusPass},
	)

	reg := testRegistry()
	reg.Groups = append(reg.Groups, generatedFixture().parityGroups()...)

	// When: parity is computed across both suites.
	got := computeParity(reg, report, []string{"go-sdk", "python-sdk"})

	// Then: go-sdk carries no debt for groups it has no backend for. A suite
	// outside a generated group's `suites` list is out of scope, not indebted —
	// otherwise every model refresh would inflate the debt file for six suites
	// that were never asked to run the tests.
	if len(got.Debt) != 0 {
		t.Fatalf("debt = %#v, want none", got.Debt)
	}
	if got.Missing != 0 {
		t.Errorf("missing = %d, want 0", got.Missing)
	}
	// And the generated results are registered, so the reverse direction stays
	// quiet.
	if len(got.Unregistered) != 0 {
		t.Errorf("unregistered = %#v, want none", got.Unregistered)
	}
}

func TestGeneratedGroupInScopeStillCarriesDebt(t *testing.T) {
	// Given: python-sdk, which IS in scope, emitting the shared
	// not-implemented sentinel for a generated group.
	report := reportWithResults()
	addSkip(report, "python-sdk", "sqs", "sqs-generated-gated", "ReceiveMessage", notImplementedSentinel("python-sdk"))

	fixture := generatedFixture()
	fixture.Groups = fixture.Groups[1:] // the gated group alone
	reg := &parityRegistry{Groups: fixture.parityGroups()}

	// When: parity is computed.
	got := computeParity(reg, report, []string{"python-sdk"})

	// Then: scoping narrows who is asked, it does not excuse those who are.
	// Note this is uniform across candidate and gated: `state` governs the two
	// failure gates, not parity. A generated group's `suites` list is derived
	// from backend availability, so an in-scope suite always has the backend
	// and a gap there is a real one whatever the group's state.
	if len(got.Debt) != 1 || got.Debt[0].Group != "sqs-generated-gated" {
		t.Fatalf("debt = %#v, want one entry for sqs-generated-gated", got.Debt)
	}
}

// ---------------------------------------------------------------------------
// Collision lint
// ---------------------------------------------------------------------------

func TestLintGeneratedRegistryRejectsCollidingKey(t *testing.T) {
	// Given: a generated group reusing a hand-written group's name and test.
	hand := testRegistry()
	gen := &generatedRegistry{Version: generatedRegistryVersion, Groups: []generatedGroup{{
		Service:   "s3",
		Name:      "s3-crud",
		Generated: true,
		State:     generatedStateCandidate,
		Suites:    []string{"python-sdk"},
		Tests:     []generatedTest{{Name: "CreateBucket"}},
	}}}

	// When: the two registries are linted together.
	issues := lintGeneratedRegistry(hand, gen)

	// Then: both the group-name and the (group, test) collision are named.
	// baseline.json, flaky.json and parity-debt.json all key on
	// suite/group/test, so a duplicate key silently merges a generated result
	// with a hand-written one and the gate stops meaning anything.
	if len(issues) == 0 {
		t.Fatalf("issues = none, want a collision")
	}
	joined := strings.Join(issues, "\n")
	if !strings.Contains(joined, "s3-crud") {
		t.Errorf("issues do not name the colliding group:\n%s", joined)
	}
}

func TestLintGeneratedRegistryRejectsMalformedGroups(t *testing.T) {
	// Given: groups violating each structural invariant the generated schema
	// states — the Go side mirrors it so the invariant holds even where the
	// Python validator is not run.
	hand := &parityRegistry{}
	gen := &generatedRegistry{Version: generatedRegistryVersion, Groups: []generatedGroup{
		{Service: "sqs", Name: "sqs-no-flag", State: generatedStateCandidate, Suites: []string{"python-sdk"}, Tests: []generatedTest{{Name: "SendMessage"}}},
		{Service: "sqs", Name: "sqs-bad-state", Generated: true, State: "probationary", Suites: []string{"python-sdk"}, Tests: []generatedTest{{Name: "SendMessage"}}},
		{Service: "sqs", Name: "sqs-no-suites", Generated: true, State: generatedStateGated, Tests: []generatedTest{{Name: "SendMessage"}}},
		{Service: "sqs", Name: "sqs-dupe", Generated: true, State: generatedStateGated, Suites: []string{"python-sdk"}, Tests: []generatedTest{{Name: "SendMessage"}}},
		{Service: "sqs", Name: "sqs-dupe", Generated: true, State: generatedStateGated, Suites: []string{"python-sdk"}, Tests: []generatedTest{{Name: "SendMessage"}}},
	}}

	// When: the generated registry is linted.
	issues := lintGeneratedRegistry(hand, gen)

	// Then: every violation is named.
	joined := strings.Join(issues, "\n")
	for _, want := range []string{"sqs-no-flag", "sqs-bad-state", "sqs-no-suites", "sqs-dupe"} {
		if !strings.Contains(joined, want) {
			t.Errorf("lint did not flag %s:\n%s", want, joined)
		}
	}
}

func TestLintGeneratedRegistryAcceptsFixture(t *testing.T) {
	// Given: a well-formed generated registry alongside the hand-written one.
	// When: they are linted together.
	issues := lintGeneratedRegistry(testRegistry(), generatedFixture())

	// Then: nothing is flagged.
	if len(issues) != 0 {
		t.Fatalf("issues = %#v, want none", issues)
	}
}

// TestLintAcceptsAGeneratedGroupReusingATestName pins the deliberate
// non-check. A generated group declaring a test name a hand-written group also
// declares is not a collision: the join key is suite/group/test, the group
// names differ, and every gate file keeps the two apart.
//
// It is also the norm rather than the exception. Generated test names are the
// PascalCase operation name (docs/plans/compat-coverage-modelgen.md §3.3), so
// every generated SQS group declares `CreateQueue`, `SendMessage` and the rest
// beside `sqs-queues` and `sqs-messages`, and a model refresh may add more with
// no human action at all (§3.11). That is safe because impl keys are
// group-qualified `group:test` in every suite, enforced by each suite's own
// registration test (compat/AGENTS.md § Implementation keys) — a shared test
// name never needs disambiguating here.
func TestLintAcceptsAGeneratedGroupReusingATestName(t *testing.T) {
	// Given: a generated group of its own name declaring a test name the
	// hand-written s3-crud group already declares.
	gen := &generatedRegistry{Version: generatedRegistryVersion, Groups: []generatedGroup{{
		Service:   "s3",
		Name:      "s3-generated-crud",
		Generated: true,
		State:     generatedStateCandidate,
		Suites:    []string{"python-sdk"},
		Tests:     []generatedTest{{Name: "CreateBucket"}},
	}}}

	// When: the two registries are linted together.
	issues := lintGeneratedRegistry(testRegistry(), gen)

	// Then: nothing is flagged.
	if len(issues) != 0 {
		t.Fatalf("issues = %#v, want none — a reused test name is not a collision", issues)
	}
}

// TestReadParityRegistriesRejectsCollision proves the lint is enforced at the
// load site, not just available as a function — a collision must stop
// --check-parity rather than quietly produce a merged registry.
func TestReadParityRegistriesRejectsCollision(t *testing.T) {
	// Given: files on disk whose groups collide.
	handPath := writeTempJSON(t, "registry.json", testRegistry())
	genPath := writeTempJSON(t, "registry.generated.json", &generatedRegistry{
		Version: generatedRegistryVersion,
		Groups: []generatedGroup{{
			Service: "s3", Name: "s3-crud", Generated: true,
			State: generatedStateCandidate, Suites: []string{"python-sdk"},
			Tests: []generatedTest{{Name: "CreateBucket"}},
		}},
	})

	// When: they are loaded together.
	_, err := readParityRegistries(handPath, genPath)

	// Then: the load fails.
	if err == nil {
		t.Fatal("readParityRegistries error = nil, want a collision error")
	}
	if !strings.Contains(err.Error(), "s3-crud") {
		t.Errorf("error = %v, want it to name s3-crud", err)
	}
}

// TestLoadCandidateGroupsRejectsCollision covers the gate path, not the parity
// path. A colliding candidate group is the one collision that weakens a gate
// rather than merely confusing a report: reusing a hand-written group's name
// would exempt that hand-written group from --compare-baseline and
// --max-failures, so a real regression would be reported as a pass.
// --check-parity runs the same lint, but compat.yml runs it after both
// baseline gates, so the exemption has to be refused where it is granted.
func TestLoadCandidateGroupsRejectsCollision(t *testing.T) {
	// Given: a candidate group that reuses a hand-written group's name.
	handPath := writeTempJSON(t, "registry.json", testRegistry())
	genPath := writeTempJSON(t, "registry.generated.json", &generatedRegistry{
		Version: generatedRegistryVersion,
		Groups: []generatedGroup{{
			Service: "s3", Name: "s3-crud", Generated: true,
			State: generatedStateCandidate, Suites: []string{"python-sdk"},
			Tests: []generatedTest{{Name: "CreateBucket"}},
		}},
	})
	defer swapFlag(registryFile, handPath)()
	defer swapFlag(generatedRegistryFile, genPath)()

	// When: a gate loads its exemptions.
	_, err := loadCandidateGroups()

	// Then: it refuses rather than handing back an exemption.
	if err == nil {
		t.Fatal("loadCandidateGroups error = nil, want a collision error")
	}
	if !strings.Contains(err.Error(), "s3-crud") {
		t.Errorf("error = %v, want it to name s3-crud", err)
	}
}

// TestLoadCandidateGroupsToleratesMissingHandWrittenRegistry pins the
// short-circuit in loadCandidateGroups: with no generated groups there is
// nothing to collide, and --compare-baseline is run against artifacts in
// contexts where compat/suites/registry.json is not at the default path.
// Demanding it there would break the gate on the empty registry that ships
// today — which is exactly what phase G0 promises cannot happen.
func TestLoadCandidateGroupsToleratesMissingHandWrittenRegistry(t *testing.T) {
	// Given: an empty generated registry and no hand-written registry at all.
	genPath := writeTempJSON(t, "registry.generated.json", &generatedRegistry{
		Version: generatedRegistryVersion,
	})
	defer swapFlag(registryFile, filepath.Join(t.TempDir(), "absent.json"))()
	defer swapFlag(generatedRegistryFile, genPath)()

	// When: a gate loads its exemptions.
	candidates, err := loadCandidateGroups()

	// Then: it succeeds with nothing exempt.
	if err != nil {
		t.Fatalf("loadCandidateGroups: %v", err)
	}
	if len(candidates) != 0 {
		t.Errorf("candidates = %v, want empty", candidates)
	}
}

// swapFlag points a flag at a test value and returns the restore func, so the
// package-level flags the gate entry points read can be driven from a test
// without leaking into the next one.
func swapFlag(target *string, value string) func() {
	previous := *target
	*target = value
	return func() { *target = previous }
}

// ---------------------------------------------------------------------------
// The checked-in files
// ---------------------------------------------------------------------------

// TestCheckedInRegistriesLintClean is the CI enforcement for the collision
// lint: it runs over the real files every time `go test ./cmd/compat/...` does.
func TestCheckedInRegistriesLintClean(t *testing.T) {
	reg, err := readParityRegistry(repoPath(t, "compat", "suites", "registry.json"))
	if err != nil {
		t.Fatalf("readParityRegistry: %v", err)
	}
	gen, err := readGeneratedRegistry(repoPath(t, "compat", "suites", "registry.generated.json"))
	if err != nil {
		t.Fatalf("readGeneratedRegistry: %v", err)
	}
	if issues := lintGeneratedRegistry(reg, gen); len(issues) != 0 {
		t.Fatalf("checked-in registries do not lint clean:\n%s", strings.Join(issues, "\n"))
	}
}

// TestGeneratedSchemaRefsSharedTestGroup keeps the two schemas from drifting.
// The generated schema exists to add three fields to the shared shape, not to
// hold a second copy of it: a duplicated TestGroup would let the generated
// registry accept a test-name convention the hand-written one rejects, and the
// two files are joined on exactly those names.
func TestGeneratedSchemaRefsSharedTestGroup(t *testing.T) {
	genSchema := readJSONFile(t, repoPath(t, "compat", "suites", "registry.generated.schema.json"))
	handSchema := readJSONFile(t, repoPath(t, "compat", "suites", "registry.schema.json"))

	raw, err := json.Marshal(genSchema)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	const ref = "registry.schema.json#/definitions/TestGroup"
	if !strings.Contains(string(raw), ref) {
		t.Errorf("registry.generated.schema.json does not $ref %s — it must extend the shared definition, never copy it", ref)
	}

	defs, ok := handSchema["definitions"].(map[string]any)
	if !ok || defs["TestGroup"] == nil {
		t.Fatalf("registry.schema.json has no definitions/TestGroup for the $ref to target")
	}

	// And the three added fields must all be present, since the Go loader and
	// the dashboard both depend on them.
	for _, field := range []string{"generated", "scenario", "state"} {
		if !strings.Contains(string(raw), `"`+field+`"`) {
			t.Errorf("registry.generated.schema.json does not define %q", field)
		}
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// repoPath resolves a path relative to the repository root. Tests run with the
// package directory as their working directory.
func repoPath(t *testing.T, parts ...string) string {
	t.Helper()
	return filepath.Join(append([]string{"..", ".."}, parts...)...)
}

func readJSONFile(t *testing.T, path string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	return out
}

func writeTempJSON(t *testing.T, name string, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal %s: %v", name, err)
	}
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return path
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
