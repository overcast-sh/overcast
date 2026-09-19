package main

import (
	"strings"
	"testing"

	"github.com/overcast-sh/overcast/compat"
)

// testRegistry builds a registry covering two groups, one of them scoped to a
// single suite.
func testRegistry() *parityRegistry {
	return &parityRegistry{Groups: []parityGroup{
		{Service: "s3", Name: "s3-crud", Tests: []parityTest{{Name: "CreateBucket"}, {Name: "DeleteBucket"}}},
		{Service: "cdk", Name: "cdk-lifecycle", Suites: []string{"cdk"}, Tests: []parityTest{{Name: "Deploy"}}},
	}}
}

func TestParityClassifiesRegistryGapsAsDebt(t *testing.T) {
	// Given: a suite that implements one test and emits the shared
	// not-implemented sentinel for the other
	report := reportWithResults(
		resultSpec{suite: "rust-sdk", service: "s3", group: "s3-crud", test: "CreateBucket", status: compat.StatusPass},
	)
	addSkip(report, "rust-sdk", "s3", "s3-crud", "DeleteBucket", notImplementedSentinel("rust-sdk"))

	// When: parity is computed for that suite
	got := computeParity(testRegistry(), report, []string{"rust-sdk"})

	// Then: only the sentinel-skipped test counts as parity debt
	if len(got.Debt) != 1 {
		t.Fatalf("debt = %#v, want 1 entry", got.Debt)
	}
	d := got.Debt[0]
	if d.Suite != "rust-sdk" || d.Group != "s3-crud" || d.Tests != 1 {
		t.Fatalf("debt entry = %+v, want rust-sdk/s3-crud with 1 test", d)
	}
}

func TestParityFlagsResultsAbsentFromRegistry(t *testing.T) {
	// Given: a suite emitting a test the registry has never heard of. This is
	// not hypothetical — cdk shipped VerifyFilteredEsmDelivery and
	// VerifyFilteredDdbEsm outside the registry and nothing noticed: the
	// checker only walked registry->suites, so a test invented by one suite
	// never joined the cross-suite matrix and uniformity silently eroded.
	report := reportWithResults(
		resultSpec{suite: "rust-sdk", service: "s3", group: "s3-crud", test: "CreateBucket", status: compat.StatusPass},
		resultSpec{suite: "rust-sdk", service: "s3", group: "s3-crud", test: "DeleteBucket", status: compat.StatusPass},
		resultSpec{suite: "rust-sdk", service: "s3", group: "s3-crud", test: "FrobnicateBucket", status: compat.StatusPass},
	)

	// When: parity is computed
	got := computeParity(testRegistry(), report, []string{"rust-sdk"})

	// Then: the invented test is named. The registry is the single source of
	// truth in both directions — a suite neither under- nor over-implements it.
	if len(got.Unregistered) != 1 {
		t.Fatalf("unregistered = %#v, want 1 entry", got.Unregistered)
	}
	if !strings.Contains(got.Unregistered[0], "rust-sdk/s3-crud/FrobnicateBucket") {
		t.Errorf("unregistered entry = %q", got.Unregistered[0])
	}
}

func TestParityUnregisteredHonoursSuiteScope(t *testing.T) {
	// Given: cdk emitting a test from a group scoped to cdk, plus a suite
	// filter that excludes the result's suite entirely
	report := reportWithResults(
		resultSpec{suite: "cdk", service: "cdk", group: "cdk-lifecycle", test: "Deploy", status: compat.StatusPass},
		resultSpec{suite: "go-sdk", service: "s3", group: "s3-crud", test: "NotInRegistry", status: compat.StatusPass},
	)

	// When: parity is computed for cdk only
	got := computeParity(testRegistry(), report, []string{"cdk"})

	// Then: cdk's scoped test is fine, and go-sdk's stray result is not
	// reported because that suite was not asked about
	if len(got.Unregistered) != 0 {
		t.Fatalf("unregistered = %#v, want none", got.Unregistered)
	}
}

func TestParityIgnoresEnvironmentalAndCascadeSkips(t *testing.T) {
	// Given: skips that are not registry gaps — an environmental skip and a
	// cascade from a failed dependency
	report := reportWithResults()
	addSkip(report, "go-sdk", "s3", "s3-crud", "CreateBucket", "requires: docker")
	addSkip(report, "go-sdk", "s3", "s3-crud", "DeleteBucket", "dependency failed: CreateBucket")

	// When: parity is computed
	got := computeParity(testRegistry(), report, []string{"go-sdk"})

	// Then: neither is parity debt — the suite implements both, they just did
	// not execute on this run
	if len(got.Debt) != 0 {
		t.Fatalf("debt = %#v, want none for environmental/cascade skips", got.Debt)
	}
	if got.Cascades != 1 {
		t.Errorf("cascades = %d, want 1", got.Cascades)
	}
}

func TestParityTreatsNAAsLegitimate(t *testing.T) {
	// Given: a test the SDK has no API for
	report := reportWithResults(
		resultSpec{suite: "cli", service: "s3", group: "s3-crud", test: "CreateBucket", status: compat.StatusPass},
		resultSpec{suite: "cli", service: "s3", group: "s3-crud", test: "DeleteBucket", status: compat.StatusNA},
	)

	// When/Then: N/A is a documented divergence, never debt
	if got := computeParity(testRegistry(), report, []string{"cli"}); len(got.Debt) != 0 {
		t.Fatalf("debt = %#v, want none for an na result", got.Debt)
	}
}

func TestParityFlagsMissingResultsAsDebt(t *testing.T) {
	// Given: a suite whose output omits a registry test entirely — worse than a
	// skip, because the dashboard matrix silently loses the cell
	report := reportWithResults(
		resultSpec{suite: "dotnet-sdk", service: "s3", group: "s3-crud", test: "CreateBucket", status: compat.StatusPass},
	)

	// When: parity is computed
	got := computeParity(testRegistry(), report, []string{"dotnet-sdk"})

	// Then: the absent test is debt, and it is reported as missing
	if len(got.Debt) != 1 || got.Debt[0].Tests != 1 {
		t.Fatalf("debt = %#v, want 1 missing test", got.Debt)
	}
	if got.Missing != 1 {
		t.Errorf("missing = %d, want 1", got.Missing)
	}
}

func TestParityHonoursSuiteScoping(t *testing.T) {
	// Given: cdk-lifecycle is scoped to the cdk suite
	report := reportWithResults(
		resultSpec{suite: "rust-sdk", service: "s3", group: "s3-crud", test: "CreateBucket", status: compat.StatusPass},
		resultSpec{suite: "rust-sdk", service: "s3", group: "s3-crud", test: "DeleteBucket", status: compat.StatusPass},
	)

	// When: parity is computed for a non-cdk suite that never reports it
	got := computeParity(testRegistry(), report, []string{"rust-sdk"})

	// Then: the out-of-scope group is not expected and carries no debt
	if len(got.Debt) != 0 {
		t.Fatalf("debt = %#v, want none — cdk-lifecycle is cdk-only", got.Debt)
	}
}

func TestParityExemptsCDKFromUnscopedGroups(t *testing.T) {
	// Given: the cdk suite, which reports only its own scoped group
	report := reportWithResults(
		resultSpec{suite: "cdk", service: "cdk", group: "cdk-lifecycle", test: "Deploy", status: compat.StatusPass},
	)

	// When: parity is computed for it
	got := computeParity(testRegistry(), report, []string{"cdk"})

	// Then: it owes nothing for s3-crud. CDK deploys whole stacks rather than
	// calling operations one at a time, so registry-wide uniformity does not
	// apply — it runs only what the registry scopes to it.
	if len(got.Debt) != 0 {
		t.Fatalf("debt = %#v, want none — cdk is exempt from unscoped groups", got.Debt)
	}
	if got.Expected != 1 {
		t.Errorf("expected = %d, want 1 (only the cdk-scoped test)", got.Expected)
	}
}

func TestParityAcceptsLegacyNotImplementedWording(t *testing.T) {
	// Given: results from a suite binary predating the shared sentinel wording
	report := reportWithResults(
		resultSpec{suite: "java-sdk", service: "s3", group: "s3-crud", test: "CreateBucket", status: compat.StatusPass},
	)
	addSkip(report, "java-sdk", "s3", "s3-crud", "DeleteBucket", "not implemented")

	// When: parity is computed
	got := computeParity(testRegistry(), report, []string{"java-sdk"})

	// Then: it is classified as debt, not as an unclassified reason — otherwise
	// an artifact from an older build looks like wording drift.
	if len(got.Debt) != 1 {
		t.Fatalf("debt = %#v, want 1", got.Debt)
	}
	if len(got.Unclassified) != 0 {
		t.Errorf("unclassified = %#v, want none", got.Unclassified)
	}
}

func TestParityUnclassifiedSkipReasonSurfaces(t *testing.T) {
	// Given: a skip whose reason matches no known category — e.g. a suite that
	// drifted from the shared sentinel wording
	report := reportWithResults(
		resultSpec{suite: "go-sdk", service: "s3", group: "s3-crud", test: "CreateBucket", status: compat.StatusPass},
	)
	addSkip(report, "go-sdk", "s3", "s3-crud", "DeleteBucket", "todo later")

	// When: parity is computed
	got := computeParity(testRegistry(), report, []string{"go-sdk"})

	// Then: it is reported rather than silently counted as implemented, so
	// wording drift cannot hide a gap
	if len(got.Unclassified) != 1 {
		t.Fatalf("unclassified = %#v, want 1", got.Unclassified)
	}
	if !strings.Contains(got.Unclassified[0], "go-sdk/s3-crud/DeleteBucket") {
		t.Errorf("unclassified message = %q", got.Unclassified[0])
	}
}

func TestParityDiffRejectsUnrecordedAndStaleDebt(t *testing.T) {
	// Given: actual debt in one group, and a debt file that records a different
	// group instead
	actual := []parityDebtEntry{{Suite: "rust-sdk", Service: "s3", Group: "s3-crud", Tests: 1}}
	recorded := &parityDebtFile{Version: 1, Debt: []parityDebtEntry{
		{Suite: "rust-sdk", Service: "iam", Group: "iam-users", Tests: 11},
	}}

	// When: the two are diffed
	issues := diffParityDebt(recorded, actual)

	// Then: both directions are errors — unrecorded debt would let a PR add
	// gaps silently, stale debt would let the file rot and stop shrinking
	joined := strings.Join(issues, "\n")
	if !strings.Contains(joined, "unrecorded parity debt: rust-sdk/s3-crud") {
		t.Errorf("missing unrecorded-debt issue: %q", joined)
	}
	if !strings.Contains(joined, "stale parity debt: rust-sdk/iam-users") {
		t.Errorf("missing stale-debt issue: %q", joined)
	}
}

func TestParityDiffAcceptsExactMatch(t *testing.T) {
	// Given: a debt file that matches reality, including the test count
	actual := []parityDebtEntry{{Suite: "rust-sdk", Service: "s3", Group: "s3-crud", Tests: 2}}
	recorded := &parityDebtFile{Version: 1, Debt: []parityDebtEntry{
		{Suite: "rust-sdk", Service: "s3", Group: "s3-crud", Tests: 2},
	}}

	if issues := diffParityDebt(recorded, actual); len(issues) != 0 {
		t.Fatalf("issues = %#v, want none", issues)
	}
}

func TestParityDiffFlagsCountGrowthWithinAGroup(t *testing.T) {
	// Given: a group whose debt grew — e.g. new registry tests added without
	// implementing them anywhere
	actual := []parityDebtEntry{{Suite: "rust-sdk", Service: "s3", Group: "s3-crud", Tests: 5}}
	recorded := &parityDebtFile{Version: 1, Debt: []parityDebtEntry{
		{Suite: "rust-sdk", Service: "s3", Group: "s3-crud", Tests: 2},
	}}

	// When/Then: growth is rejected; the debt file only ever shrinks
	issues := diffParityDebt(recorded, actual)
	if len(issues) != 1 || !strings.Contains(issues[0], "grew") {
		t.Fatalf("issues = %#v, want a growth issue", issues)
	}
}

func TestParityDiffAllowsShrinkWithFileUpdate(t *testing.T) {
	// Given: debt that shrank but whose file was not updated
	actual := []parityDebtEntry{{Suite: "rust-sdk", Service: "s3", Group: "s3-crud", Tests: 1}}
	recorded := &parityDebtFile{Version: 1, Debt: []parityDebtEntry{
		{Suite: "rust-sdk", Service: "s3", Group: "s3-crud", Tests: 3},
	}}

	// When/Then: this is an error too — the file must be regenerated so
	// progress is visible in the diff, not implied
	issues := diffParityDebt(recorded, actual)
	if len(issues) != 1 || !strings.Contains(issues[0], "shrank") {
		t.Fatalf("issues = %#v, want a shrink issue telling the author to regenerate", issues)
	}
}

// addSkip appends a skipped result with a reason to an existing report.
func addSkip(report *compat.RunReport, suite, service, group, test, reason string) {
	for _, sr := range report.Suites {
		if sr.Suite != suite {
			continue
		}
		for _, gr := range sr.Groups {
			if gr.Name == group {
				gr.Tests = append(gr.Tests, compat.TestResultEvent{
					Suite: suite, Service: service, Group: group, Test: test,
					Status: compat.StatusSkip, Error: reason,
				})
				return
			}
		}
		gr := &compat.GroupReport{Suite: suite, Service: service, Name: group}
		gr.Tests = append(gr.Tests, compat.TestResultEvent{
			Suite: suite, Service: service, Group: group, Test: test,
			Status: compat.StatusSkip, Error: reason,
		})
		sr.Groups = append(sr.Groups, gr)
		return
	}
	sr := &compat.SuiteReport{Suite: suite}
	gr := &compat.GroupReport{Suite: suite, Service: service, Name: group}
	gr.Tests = append(gr.Tests, compat.TestResultEvent{
		Suite: suite, Service: service, Group: group, Test: test,
		Status: compat.StatusSkip, Error: reason,
	})
	sr.Groups = append(sr.Groups, gr)
	report.Suites = append(report.Suites, sr)
}

// TestParityCountsGroupTimeoutSkipsAsCascades is the aggregate half of #1966:
// the cli harness reports the tests a group ran out of budget before reaching
// as skips with this prefix, and they are cascades of whatever consumed the
// budget, never parity debt. Before the harness reported them at all, they
// were Missing here, and the gate failed a pull request that had touched
// nothing near KMS for four tests of unrecorded debt.
func TestParityCountsGroupTimeoutSkipsAsCascades(t *testing.T) {
	// Given: a group whose budget ran out under CreateBucket, leaving
	// DeleteBucket reported as a timed-out skip
	report := reportWithResults(
		resultSpec{suite: "cli", service: "s3", group: "s3-crud", test: "CreateBucket", status: compat.StatusFail},
	)
	addSkip(report, "cli", "s3", "s3-crud", "DeleteBucket", "group timed out after 16m3s before this test ran (last test to run: CreateBucket)")

	// When: parity is computed
	got := computeParity(testRegistry(), report, []string{"cli"})

	// Then: no debt, nothing missing, nothing unclassified — one cascade
	if len(got.Debt) != 0 {
		t.Fatalf("debt = %#v, want none for a timed-out skip", got.Debt)
	}
	if got.Missing != 0 {
		t.Errorf("missing = %d, want 0: the test was reported", got.Missing)
	}
	if len(got.Unclassified) != 0 {
		t.Errorf("unclassified = %#v, want none", got.Unclassified)
	}
	if got.Cascades != 1 {
		t.Errorf("cascades = %d, want 1", got.Cascades)
	}
}
