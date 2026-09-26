package main

import (
	"strings"
	"testing"
	"time"

	"github.com/overcast-sh/overcast/compat"
	compatmodel "github.com/overcast-sh/overcast/compat/model"
)

// shadowRegistry is one shadow group and the native it shadows, in the shape
// --compare-shadow reads.
func shadowRegistry(tests ...string) *generatedRegistry {
	var entries []parityTest
	for _, name := range tests {
		entries = append(entries, parityTest{Name: name})
	}
	return &generatedRegistry{
		Version: generatedRegistryVersion,
		Groups: []generatedGroup{{
			Service:   "sqs",
			Name:      "sqs-queues-shadow",
			Generated: true,
			State:     generatedStateCandidate,
			ShadowOf:  "sqs-queues",
			Suites:    []string{"cli", "go-sdk"},
			Tests:     entries,
		}},
	}
}

func TestCompareShadow_agreesWhenBothHalvesAnswerTheSame(t *testing.T) {
	// Given: two suites running the native group and its shadow, agreeing.
	report := reportWithResults(
		resultSpec{suite: "cli", service: "sqs", group: "sqs-queues", test: "CreateQueue", status: compat.StatusPass},
		resultSpec{suite: "cli", service: "sqs", group: "sqs-queues-shadow", test: "CreateQueue", status: compat.StatusPass},
		resultSpec{suite: "go-sdk", service: "sqs", group: "sqs-queues", test: "CreateQueue", status: compat.StatusPass},
		resultSpec{suite: "go-sdk", service: "sqs", group: "sqs-queues-shadow", test: "CreateQueue", status: compat.StatusPass},
	)

	// When: the two are compared.
	comparisons := compareShadows(shadowRegistry("CreateQueue"), report)

	// Then: every pair agrees, and both suites were considered.
	if len(comparisons) != 1 {
		t.Fatalf("comparisons = %d, want 1", len(comparisons))
	}
	if got := len(comparisons[0].Pairs); got != 2 {
		t.Fatalf("pairs = %d, want 2 (one per suite)", got)
	}
	if diverged := comparisons[0].divergences(); len(diverged) != 0 {
		t.Fatalf("divergences = %+v, want none", diverged)
	}
	var out strings.Builder
	if !writeShadowReport(&out, comparisons, false) {
		t.Fatalf("report says the halves disagreed:\n%s", out.String())
	}
}

func TestCompareShadow_reportsAStatusFlipPerSuiteAndTest(t *testing.T) {
	// Given: the shadow fails one test the native passes, in one suite only.
	report := reportWithResults(
		resultSpec{suite: "cli", service: "sqs", group: "sqs-queues", test: "TagQueue", status: compat.StatusPass},
		resultSpec{suite: "cli", service: "sqs", group: "sqs-queues-shadow", test: "TagQueue", status: compat.StatusFail},
		resultSpec{suite: "go-sdk", service: "sqs", group: "sqs-queues", test: "TagQueue", status: compat.StatusPass},
		resultSpec{suite: "go-sdk", service: "sqs", group: "sqs-queues-shadow", test: "TagQueue", status: compat.StatusPass},
	)

	// When: the two are compared.
	comparisons := compareShadows(shadowRegistry("TagQueue"), report)
	diverged := comparisons[0].divergences()

	// Then: exactly the one (suite, test) is named, with both statuses.
	if len(diverged) != 1 {
		t.Fatalf("divergences = %+v, want 1", diverged)
	}
	if diverged[0].Suite != "cli" || diverged[0].Test != "TagQueue" {
		t.Errorf("divergence = %+v, want cli/TagQueue", diverged[0])
	}
	var out strings.Builder
	if writeShadowReport(&out, comparisons, false) {
		t.Fatal("report says the halves agreed")
	}
	for _, want := range []string{"cli", "TagQueue", "native=pass", "shadow=fail"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("report does not mention %q:\n%s", want, out.String())
		}
	}
}

// A suite that ran one half and not the other has proved nothing about the
// port, and the pair it did not run is exactly the one a reader would assume
// was fine. It is reported as a divergence with <no result> on the missing
// side.
func TestCompareShadow_treatsAMissingHalfAsADivergence(t *testing.T) {
	// Given: cli ran only the native group.
	report := reportWithResults(
		resultSpec{suite: "cli", service: "sqs", group: "sqs-queues", test: "DeleteQueue", status: compat.StatusPass},
	)

	// When: the two are compared.
	comparisons := compareShadows(shadowRegistry("DeleteQueue"), report)

	// Then: the pair is reported, naming the side that answered nothing.
	diverged := comparisons[0].divergences()
	if len(diverged) != 1 || diverged[0].Shadow != "" {
		t.Fatalf("divergences = %+v, want one with no shadow status", diverged)
	}
	var out strings.Builder
	if writeShadowReport(&out, comparisons, false) {
		t.Fatal("report says the halves agreed")
	}
	if !strings.Contains(out.String(), "<no result>") {
		t.Errorf("report does not name the missing half:\n%s", out.String())
	}
}

// A run that exercised neither half is not agreement. Reporting it as such is
// how a flip PR ends up citing a soak that never happened.
func TestCompareShadow_refusesARunThatExercisedNeitherHalf(t *testing.T) {
	// Given: a report with nothing for either group.
	report := reportWithResults(
		resultSpec{suite: "cli", service: "s3", group: "s3-crud", test: "CreateBucket", status: compat.StatusPass},
	)

	// When: the two are compared.
	comparisons := compareShadows(shadowRegistry("CreateQueue"), report)

	// Then: the report says so and does not call it agreement.
	var out strings.Builder
	if writeShadowReport(&out, comparisons, false) {
		t.Fatal("a run that exercised neither half must not read as agreement")
	}
	if !strings.Contains(out.String(), "proves nothing") {
		t.Errorf("report does not say why:\n%s", out.String())
	}
}

func TestCompareShadow_saysSoWhenThereAreNoShadowGroups(t *testing.T) {
	// Given: a generated registry with no shadow group in it.
	gen := &generatedRegistry{Version: generatedRegistryVersion, Groups: []generatedGroup{{
		Name: "sqs-gen-queue", Generated: true, State: generatedStateCandidate,
		Suites: []string{"cli"}, Tests: []parityTest{{Name: "CreateQueue"}},
	}}}

	// When/Then: the comparison is empty and passes, rather than failing for
	// want of anything to compare.
	var out strings.Builder
	if !writeShadowReport(&out, compareShadows(gen, reportWithResults()), false) {
		t.Fatalf("no shadow groups must not fail:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "No shadow groups") {
		t.Errorf("report = %q", out.String())
	}
}

// ---------------------------------------------------------------------------
// The lint
// ---------------------------------------------------------------------------

func TestLintGeneratedRegistry_refusesAShadowOfANonexistentGroup(t *testing.T) {
	// Given: a shadow naming a group the hand-written registry does not have.
	hand := &parityRegistry{Groups: []parityGroup{
		{Service: "s3", Name: "s3-crud", Tests: []parityTest{{Name: "CreateBucket"}}},
	}}

	// When: the pair is linted.
	issues := lintGeneratedRegistry(hand, shadowRegistry("CreateQueue"))

	// Then: it is refused, because the comparison would join against nothing.
	if len(issues) != 1 || !strings.Contains(issues[0], "not a group in the hand-written registry") {
		t.Fatalf("issues = %#v", issues)
	}
}

func TestLintGeneratedRegistry_refusesAShadowWhoseTestNamesHaveDrifted(t *testing.T) {
	// Given: a native group with a test the shadow does not declare, and a
	// shadow test the native does not.
	hand := &parityRegistry{Groups: []parityGroup{
		{Service: "sqs", Name: "sqs-queues", Tests: []parityTest{{Name: "CreateQueue"}, {Name: "DeleteQueue"}}},
	}}

	// When: the pair is linted.
	issues := lintGeneratedRegistry(hand, shadowRegistry("CreateQueue", "PurgeQueue"))

	// Then: both directions are named — the comparison joins on the test name,
	// so either way a test goes uncompared.
	joined := strings.Join(issues, "\n")
	if !strings.Contains(joined, "does not declare DeleteQueue") {
		t.Errorf("missing test not reported: %#v", issues)
	}
	if !strings.Contains(joined, "declares PurgeQueue") {
		t.Errorf("extra test not reported: %#v", issues)
	}
}

func TestLintGeneratedRegistry_refusesAGatedShadow(t *testing.T) {
	// Given: a shadow group somebody promoted into the gate.
	hand := &parityRegistry{Groups: []parityGroup{
		{Service: "sqs", Name: "sqs-queues", Tests: []parityTest{{Name: "CreateQueue"}}},
	}}
	gen := shadowRegistry("CreateQueue")
	gen.Groups[0].State = generatedStateGated

	// When: the pair is linted.
	issues := lintGeneratedRegistry(hand, gen)

	// Then: it is refused. A shadow is deleted when the port lands.
	if len(issues) != 1 || !strings.Contains(issues[0], "a shadow gates nothing") {
		t.Fatalf("issues = %#v", issues)
	}
}

func TestLintGeneratedRegistry_refusesAHandWrittenGroupCarryingShadowOf(t *testing.T) {
	// Given: shadowOf hand-written into registry.json.
	hand := &parityRegistry{Groups: []parityGroup{
		{Service: "sqs", Name: "sqs-queues", ShadowOf: "something", Tests: []parityTest{{Name: "CreateQueue"}}},
	}}

	// When: the pair is linted.
	issues := lintGeneratedRegistry(hand, &generatedRegistry{Version: generatedRegistryVersion, Groups: []generatedGroup{{
		Name: "sqs-gen-queue", Generated: true, State: generatedStateCandidate,
		Suites: []string{"cli"}, Tests: []parityTest{{Name: "CreateQueue"}},
	}}})

	// Then: it is refused — cmd/compatgen owns that field.
	if len(issues) != 1 || !strings.Contains(issues[0], "generated-only field") {
		t.Fatalf("issues = %#v", issues)
	}
}

// A shadow group runs the migration soak, not the promotion soak, and would
// otherwise promote on three agreeing runs into a gate it is scheduled to be
// deleted from — and then be reported overdue at thirty days for failing to
// promote when it already had.
func TestApplyPromotions_leavesAShadowGroupAlone(t *testing.T) {
	// Given: a shadow group that answered identically in three runs.
	gen := candidateFixture()
	gen.Groups[0].Name = "sqs-queues-shadow"
	gen.Groups[0].ShadowOf = "sqs-queues"
	runs := soakRuns(3, compat.StatusPass)
	for _, run := range runs {
		for _, suite := range run.Report.Suites {
			for _, group := range suite.Groups {
				group.Name = "sqs-queues-shadow"
				for i := range group.Tests {
					group.Tests[i].Group = "sqs-queues-shadow"
				}
			}
		}
	}
	ledger := &compatmodel.Promotions{Version: compatmodel.PromotionsVersion, Groups: map[string]compatmodel.Promotion{}}

	// When: the soak runs.
	outcome := applyPromotions(gen, ledger, runs, 3, time.Date(2026, 9, 7, 0, 0, 0, 0, time.UTC))

	// Then: nothing is promoted, and no ledger entry is opened for it.
	if len(outcome.Promoted) != 0 {
		t.Errorf("promoted = %v, want none", outcome.Promoted)
	}
	if _, recorded := outcome.File.Groups["sqs-queues-shadow"]; recorded {
		t.Error("a shadow group must not enter the promotion ledger")
	}
	if len(outcome.Blocked) != 0 {
		t.Errorf("blocked = %+v, want none — a shadow is not a candidate at all", outcome.Blocked)
	}
}

// skipWithReason adds a skip carrying its reason, which is what tells parity
// debt apart from a setup that fell over.
func skipWithReason(report *compat.RunReport, suite, group, test, reason string) {
	for _, s := range report.Suites {
		if s.Suite != suite {
			continue
		}
		for _, g := range s.Groups {
			if g.Name != group {
				continue
			}
			g.Tests = append(g.Tests, compat.TestResultEvent{
				Suite: suite, Service: "sqs", Group: group, Test: test,
				Status: compat.StatusSkip, Error: reason,
			})
			return
		}
		s.Groups = append(s.Groups, &compat.GroupReport{
			Suite: suite, Service: "sqs", Name: group,
			Tests: []compat.TestResultEvent{{
				Suite: suite, Service: "sqs", Group: group, Test: test,
				Status: compat.StatusSkip, Error: reason,
			}},
		})
		return
	}
	report.Suites = append(report.Suites, &compat.SuiteReport{
		Suite: suite,
		Groups: []*compat.GroupReport{{
			Suite: suite, Service: "sqs", Name: group,
			Tests: []compat.TestResultEvent{{
				Suite: suite, Service: "sqs", Group: group, Test: test,
				Status: compat.StatusSkip, Error: reason,
			}},
		}},
	})
}

// The migration's own purpose: a suite that never implemented the native group
// gains the test from the port. There is no predecessor to match, so this is
// not a divergence and must not block the flip (§3.11 step 4).
func TestCompareShadow_countsParityDebtClosedRatherThanDiverged(t *testing.T) {
	// Given: rust-sdk never implemented the native group; the shadow passes.
	report := reportWithResults(
		resultSpec{suite: "go-sdk", service: "sqs", group: "sqs-queues-shadow", test: "CreateQueue", status: compat.StatusPass},
	)
	skipWithReason(report, "go-sdk", "sqs-queues", "CreateQueue", notImplementedSentinel("go-sdk"))

	// When: the two are compared.
	comparisons := compareShadows(shadowRegistry("CreateQueue"), report)

	// Then: it is debt closed, not a divergence, and the port is still clear.
	if got := len(comparisons[0].of(shadowDebtClosed)); got != 1 {
		t.Fatalf("debt closed = %d, want 1 (verdicts %+v)", got, comparisons[0].Pairs)
	}
	var out strings.Builder
	if !writeShadowReport(&out, comparisons, false) {
		t.Fatalf("closing parity debt must not block the flip:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "debt closed") {
		t.Errorf("report does not name it:\n%s", out.String())
	}
}

// Two halves that both skipped agree on their status and prove nothing. They
// are reported apart from agreement — promote.go's reasoning about an all-skip
// candidate, applied to the other soak.
func TestCompareShadow_separatesPairsNeitherHalfExercised(t *testing.T) {
	// Given: a setup that failed the same way for both halves.
	report := reportWithResults()
	skipWithReason(report, "java-sdk", "sqs-queues", "CreateQueue", "setup failed: CreateQueue: connection refused")
	skipWithReason(report, "java-sdk", "sqs-queues-shadow", "CreateQueue", "setup failed: CreateQueue: connection refused")

	// When: the two are compared.
	comparisons := compareShadows(shadowRegistry("CreateQueue"), report)

	// Then: it is neither agreement nor divergence, and the report says why.
	if got := len(comparisons[0].of(shadowUnexercised)); got != 1 {
		t.Fatalf("unexercised = %d, want 1 (pairs %+v)", got, comparisons[0].Pairs)
	}
	if got := len(comparisons[0].of(shadowAgree)); got != 0 {
		t.Errorf("agreed = %d, want 0 — two skips are not evidence", got)
	}
	var out strings.Builder
	writeShadowReport(&out, comparisons, false)
	for _, want := range []string{"not run", "0 agreed", "1 not exercised", "connection refused"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("report does not mention %q:\n%s", want, out.String())
		}
	}
}

// Debt is closed by a shadow that works, not by one that merely ran. A shadow
// failing in a suite that never implemented the native group used to count as
// "parity debt closed", which hid a real node-js-sdk interpreter bug in #2240
// (#2246): nonEmpty failed on every timestamp, and only in suites with no
// native to diverge from. The shadow answering "unimplemented" is not a fault
// of the port — the natives elsewhere report the same 501 — so that still
// closes the debt; a shadow that fails is a divergence and blocks the flip.
func TestCompareShadow_debtClosesOnlyWhenTheShadowWorks(t *testing.T) {
	for _, tt := range []struct {
		shadow compat.Status
		want   shadowVerdict
	}{
		{compat.StatusPass, shadowDebtClosed},
		{compat.StatusUnimplemented, shadowDebtClosed},
		{compat.StatusFail, shadowDiverge},
	} {
		t.Run(string(tt.shadow), func(t *testing.T) {
			// Given: rust-sdk never implemented the native group, and the shadow
			// answered tt.shadow there.
			report := reportWithResults(
				resultSpec{suite: "rust-sdk", service: "sqs", group: "sqs-queues-shadow", test: "CreateQueue", status: tt.shadow},
			)
			skipWithReason(report, "rust-sdk", "sqs-queues", "CreateQueue", notImplementedSentinel("rust-sdk"))

			// When: the two are compared.
			comparisons := compareShadows(shadowRegistry("CreateQueue"), report)

			// Then: only a shadow that works closes the debt, and a failing one
			// blocks the flip.
			if got := comparisons[0].Pairs[0].verdict(); got != tt.want {
				t.Fatalf("verdict = %v, want %v (pair %+v)", got, tt.want, comparisons[0].Pairs[0])
			}
			var out strings.Builder
			clear := writeShadowReport(&out, comparisons, false)
			if blocks := tt.want == shadowDiverge; clear == blocks {
				t.Fatalf("flip clear = %v, want %v:\n%s", clear, !blocks, out.String())
			}
		})
	}
}
