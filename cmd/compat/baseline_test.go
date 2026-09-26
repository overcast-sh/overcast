package main

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/overcast-sh/overcast/compat"
)

func TestCompareBaseline_currentRegression(t *testing.T) {
	// Given: a baseline with a passing compat test.
	baseline := &compatBaseline{Version: baselineVersion, Entries: []baselineEntry{{
		Suite:  "node-js-sdk",
		Group:  "sqs-basic",
		Test:   "SendMessage",
		Status: compat.StatusPass,
	}}}
	report := reportWithResults(resultSpec{suite: "node-js-sdk", service: "sqs", group: "sqs-basic", test: "SendMessage", status: compat.StatusFail})

	// When: current results are compared to the baseline.
	regressions := compareBaseline(baseline, report)

	// Then: the passing-to-failing change is reported as a regression.
	if len(regressions) != 1 {
		t.Fatalf("regressions len = %d, want 1: %#v", len(regressions), regressions)
	}
	if !strings.Contains(regressions[0], "node-js-sdk/sqs-basic/SendMessage pass -> fail") {
		t.Fatalf("regression message = %q", regressions[0])
	}
}

func TestUpdateBaseline_improvementsOnly(t *testing.T) {
	// Given: a baseline with one passing test and one known failure.
	baseline := &compatBaseline{Version: baselineVersion, Entries: []baselineEntry{
		{Suite: "node-js-sdk", Group: "sqs-basic", Test: "SendMessage", Status: compat.StatusPass},
		{Suite: "node-js-sdk", Group: "sqs-basic", Test: "ReceiveMessage", Status: compat.StatusFail},
	}}
	report := reportWithResults(
		resultSpec{suite: "node-js-sdk", service: "sqs", group: "sqs-basic", test: "SendMessage", status: compat.StatusFail},
		resultSpec{suite: "node-js-sdk", service: "sqs", group: "sqs-basic", test: "ReceiveMessage", status: compat.StatusPass},
	)

	// When: the baseline is updated from current results.
	updated := updateBaseline(baseline, report)
	entries := baselineEntryMap(updated.Entries)

	// Then: improvements are ratcheted forward, but regressions are not accepted.
	if got := entries["node-js-sdk/sqs-basic/SendMessage"].Status; got != compat.StatusPass {
		t.Fatalf("SendMessage status = %s, want pass", got)
	}
	if got := entries["node-js-sdk/sqs-basic/ReceiveMessage"].Status; got != compat.StatusPass {
		t.Fatalf("ReceiveMessage status = %s, want pass", got)
	}
}

func TestLintBaselineChange_downgrade(t *testing.T) {
	// Given: a proposed baseline change that marks a passing test as expected to fail.
	oldBaseline := &compatBaseline{Version: baselineVersion, Entries: []baselineEntry{{
		Suite:  "go-sdk",
		Group:  "s3-crud",
		Test:   "CreateBucket",
		Status: compat.StatusPass,
	}}}
	newBaseline := &compatBaseline{Version: baselineVersion, Entries: []baselineEntry{{
		Suite:  "go-sdk",
		Group:  "s3-crud",
		Test:   "CreateBucket",
		Status: compat.StatusUnimplemented,
	}}}

	// When: the baseline change is linted. No registry is needed: the scope
	// only ever decides whether a *removal* is legitimate.
	issues, notes, _ := lintBaselineChange(oldBaseline, newBaseline, nil, nil)

	// Then: the downgrade is rejected.
	if len(notes) != 0 {
		t.Fatalf("notes = %#v, want none", notes)
	}
	if len(issues) != 1 {
		t.Fatalf("issues len = %d, want 1: %#v", len(issues), issues)
	}
	if !strings.Contains(issues[0], "pass -> unimplemented") {
		t.Fatalf("issue message = %q", issues[0])
	}
}

func TestLintBaselineChange_removal(t *testing.T) {
	// Given: a proposed baseline change that removes an existing expectation.
	oldBaseline := &compatBaseline{Version: baselineVersion, Entries: []baselineEntry{{
		Suite:  "go-sdk",
		Group:  "s3-crud",
		Test:   "CreateBucket",
		Status: compat.StatusPass,
	}}}
	newBaseline := &compatBaseline{Version: baselineVersion}

	// When: the baseline change is linted against a registry that still asks
	// go-sdk to run the test.
	issues, notes, _ := lintBaselineChange(oldBaseline, newBaseline, scopeFor(parityGroup{
		Service: "s3", Name: "s3-crud", Tests: []parityTest{{Name: "CreateBucket"}},
	}), nil)

	// Then: removing the expectation is rejected.
	if len(notes) != 0 {
		t.Fatalf("notes = %#v, want none", notes)
	}
	if len(issues) != 1 {
		t.Fatalf("issues len = %d, want 1: %#v", len(issues), issues)
	}
	if !strings.Contains(issues[0], "removed") {
		t.Fatalf("issue message = %q", issues[0])
	}
}

func TestCompareBaseline_newFailureNotInBaseline(t *testing.T) {
	// Given: a baseline that says nothing about a test — it did not exist when
	// the baseline was taken.
	baseline := &compatBaseline{Version: baselineVersion, Entries: []baselineEntry{{
		Suite:  "node-js-sdk",
		Group:  "sqs-basic",
		Test:   "SendMessage",
		Status: compat.StatusPass,
	}}}
	// When: that test shows up failing.
	report := reportWithResults(
		resultSpec{suite: "node-js-sdk", service: "sqs", group: "sqs-basic", test: "SendMessage", status: compat.StatusPass},
		resultSpec{suite: "node-js-sdk", service: "sqs", group: "sqs-basic", test: "DeleteMessage", status: compat.StatusFail},
	)
	regressions := compareBaseline(baseline, report)

	// Then: it is a regression. Iterating only over baseline entries would miss
	// it entirely, which is how a brand-new failing test used to land on main
	// with a green check.
	if len(regressions) != 1 {
		t.Fatalf("regressions len = %d, want 1: %#v", len(regressions), regressions)
	}
	if !strings.Contains(regressions[0], "node-js-sdk/sqs-basic/DeleteMessage") {
		t.Fatalf("regression message = %q", regressions[0])
	}
	if !strings.Contains(regressions[0], "not in baseline") {
		t.Fatalf("regression message should explain the test is ungrandfathered: %q", regressions[0])
	}
}

func TestCompareBaseline_newPassingTestIsNotARegression(t *testing.T) {
	// Given: a baseline that predates a newly added test
	baseline := &compatBaseline{Version: baselineVersion}
	// When: the new test passes (or is a known gap)
	report := reportWithResults(
		resultSpec{suite: "go-sdk", service: "s3", group: "s3-crud", test: "CreateBucket", status: compat.StatusPass},
		resultSpec{suite: "go-sdk", service: "s3", group: "s3-crud", test: "DeleteBucket", status: compat.StatusUnimplemented},
		resultSpec{suite: "go-sdk", service: "s3", group: "s3-crud", test: "PutObject", status: compat.StatusSkip},
		resultSpec{suite: "go-sdk", service: "s3", group: "s3-crud", test: "GetObject", status: compat.StatusNA},
	)
	// Then: adding tests never blocks a PR on its own — only failures do.
	if regressions := compareBaseline(baseline, report); len(regressions) != 0 {
		t.Fatalf("regressions = %#v, want none", regressions)
	}
}

func TestCompareBaseline_grandfatheredFailureIsAllowed(t *testing.T) {
	// Given: a baseline that already records a failure (burn-down pending)
	baseline := &compatBaseline{Version: baselineVersion, Entries: []baselineEntry{{
		Suite:  "rust-sdk",
		Group:  "s3-copy",
		Test:   "CreateSourceBucket",
		Status: compat.StatusFail,
	}}}
	report := reportWithResults(resultSpec{suite: "rust-sdk", service: "s3", group: "s3-copy", test: "CreateSourceBucket", status: compat.StatusFail})

	// Then: it is not a regression — the ratchet only forbids getting worse.
	if regressions := compareBaseline(baseline, report); len(regressions) != 0 {
		t.Fatalf("regressions = %#v, want none for a grandfathered failure", regressions)
	}
}

func TestLintBaselineChange_newFailEntryRejected(t *testing.T) {
	// Given: an established baseline, and a change that adds a brand-new
	// expectation at `fail`
	oldBaseline := &compatBaseline{Version: baselineVersion, Entries: []baselineEntry{
		{Suite: "go-sdk", Group: "s3-crud", Test: "DeleteBucket", Status: compat.StatusPass},
	}}
	newBaseline := &compatBaseline{Version: baselineVersion, Entries: []baselineEntry{
		{Suite: "go-sdk", Group: "s3-crud", Test: "CreateBucket", Status: compat.StatusFail},
		{Suite: "go-sdk", Group: "s3-crud", Test: "DeleteBucket", Status: compat.StatusPass},
	}}

	// When: the change is linted
	issues, _, _ := lintBaselineChange(oldBaseline, newBaseline, nil, nil)

	// Then: the new fail expectation is rejected. Iterating only the old
	// baseline let contributors grandfather fresh failures by adding them.
	if len(issues) != 1 {
		t.Fatalf("issues len = %d, want 1: %#v", len(issues), issues)
	}
	if !strings.Contains(issues[0], "go-sdk/s3-crud/CreateBucket") {
		t.Fatalf("issue message = %q", issues[0])
	}
}

func TestLintBaselineChange_seedingAnEmptyBaselineIsAllowed(t *testing.T) {
	// Given: an empty baseline being populated for the first time, recording
	// reality — which includes failures that already exist
	oldBaseline := &compatBaseline{Version: baselineVersion}
	newBaseline := &compatBaseline{Version: baselineVersion, Entries: []baselineEntry{
		{Suite: "cli", Group: "lambda-invoke", Test: "InvokeDryRun", Status: compat.StatusFail},
		{Suite: "cli", Group: "s3-crud", Test: "CreateBucket", Status: compat.StatusPass},
	}}

	// When: the seeding change is linted
	issues, _, _ := lintBaselineChange(oldBaseline, newBaseline, nil, nil)

	// Then: it is allowed. There is nothing to regress from, and the burn-down
	// starts from whatever the first run measured.
	if len(issues) != 0 {
		t.Fatalf("issues = %#v, want none when seeding an empty baseline", issues)
	}
}

func TestLintBaselineChange_emptyingAPopulatedBaselineIsRejected(t *testing.T) {
	// Given: someone empties a populated baseline — the move that would
	// otherwise let the next PR re-seed failures freely
	oldBaseline := &compatBaseline{Version: baselineVersion, Entries: []baselineEntry{
		{Suite: "cli", Group: "s3-crud", Test: "CreateBucket", Status: compat.StatusPass},
		{Suite: "cli", Group: "s3-crud", Test: "DeleteBucket", Status: compat.StatusPass},
	}}
	newBaseline := &compatBaseline{Version: baselineVersion}

	// When/Then: every dropped expectation is reported, so the seeding
	// exemption cannot be reached by wiping the file first.
	issues, _, _ := lintBaselineChange(oldBaseline, newBaseline, scopeFor(parityGroup{
		Service: "s3", Name: "s3-crud",
		Tests: []parityTest{{Name: "CreateBucket"}, {Name: "DeleteBucket"}},
	}), nil)
	if len(issues) != 2 {
		t.Fatalf("issues = %#v, want one per removed expectation", issues)
	}
}

// scopeFor builds the registry scope from the groups a test cares about. The
// concatenated hand-written and generated registries are one list by the time
// the lint sees them, so a generated group is just a group carrying
// Generated: true.
func scopeFor(groups ...parityGroup) *registryScope {
	return newRegistryScope(&parityRegistry{Groups: groups})
}

// TestLintBaselineChange_removalAgainstRegistryScope covers the one thing that
// separates a removal that launders a result from a removal that is simply no
// longer measured: whether the registry still asks that suite to run the test.
func TestLintBaselineChange_removalAgainstRegistryScope(t *testing.T) {
	removed := baselineEntry{Suite: "java-sdk", Service: "cdk", Group: "cdk-lifecycle", Test: "DeployStack", Status: compat.StatusSkip}

	tests := []struct {
		name  string
		old   []baselineEntry
		new   []baselineEntry
		scope *registryScope
		// want is "issue" for a lint failure, or "note" for an informational
		// out-of-scope removal.
		want string
	}{
		{
			name:  "still expected is a removed expectation",
			old:   []baselineEntry{removed},
			scope: scopeFor(parityGroup{Service: "cdk", Name: "cdk-lifecycle", Tests: []parityTest{{Name: "DeployStack"}}}),
			want:  "issue",
		},
		{
			name: "group scoped to another suite is out of scope",
			old:  []baselineEntry{removed},
			scope: scopeFor(parityGroup{
				Service: "cdk", Name: "cdk-lifecycle", Suites: []string{"cdk"},
				Tests: []parityTest{{Name: "DeployStack"}},
			}),
			want: "note",
		},
		{
			name:  "test deleted from the group is out of scope",
			old:   []baselineEntry{removed},
			scope: scopeFor(parityGroup{Service: "cdk", Name: "cdk-lifecycle", Tests: []parityTest{{Name: "DestroyStack"}}}),
			want:  "note",
		},
		{
			name:  "group deleted from the registry is out of scope",
			old:   []baselineEntry{removed},
			scope: scopeFor(parityGroup{Service: "s3", Name: "s3-crud", Tests: []parityTest{{Name: "CreateBucket"}}}),
			want:  "note",
		},
		{
			name: "generated group scoped away is out of scope",
			old: []baselineEntry{{
				Suite: "rust-sdk", Service: "sqs", Group: "sqs-queues-generated",
				Test: "CreateQueue", Status: compat.StatusUnimplemented,
			}},
			scope: scopeFor(parityGroup{
				Service: "sqs", Name: "sqs-queues-generated", Generated: true,
				State: "gated", Suites: []string{"node-js-sdk"},
				Tests: []parityTest{{Name: "CreateQueue"}},
			}),
			want: "note",
		},
		{
			name:  "no registry available keeps judging every removal",
			old:   []baselineEntry{removed},
			scope: nil,
			want:  "issue",
		},
		{
			name: "an out-of-scope group still may not be downgraded",
			old:  []baselineEntry{{Suite: "java-sdk", Service: "cdk", Group: "cdk-lifecycle", Test: "DeployStack", Status: compat.StatusPass}},
			new:  []baselineEntry{{Suite: "java-sdk", Service: "cdk", Group: "cdk-lifecycle", Test: "DeployStack", Status: compat.StatusFail}},
			scope: scopeFor(parityGroup{
				Service: "cdk", Name: "cdk-lifecycle", Suites: []string{"cdk"},
				Tests: []parityTest{{Name: "DeployStack"}},
			}),
			want: "issue",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Given/When: the proposed baseline change is linted against the
			// registry the pull request itself carries.
			oldBaseline := &compatBaseline{Version: baselineVersion, Entries: tc.old}
			newBaseline := &compatBaseline{Version: baselineVersion, Entries: tc.new}
			issues, notes, _ := lintBaselineChange(oldBaseline, newBaseline, tc.scope, nil)

			// Then: the removal either fails the gate or is reported for
			// information, never both and never neither.
			switch tc.want {
			case "issue":
				if len(issues) != 1 || len(notes) != 0 {
					t.Fatalf("issues = %#v, notes = %#v; want exactly one issue", issues, notes)
				}
			case "note":
				if len(notes) != 1 || len(issues) != 0 {
					t.Fatalf("issues = %#v, notes = %#v; want exactly one note", issues, notes)
				}
				if !strings.Contains(notes[0], "no longer in scope for") {
					t.Fatalf("note = %q, want it to say the suite no longer runs the test", notes[0])
				}
			default:
				t.Fatalf("unknown want %q", tc.want)
			}
		})
	}
}

// TestLintBaselineChangeFiles_readsTheRegistryFlags is the plumbing test: the
// scope has to come from --registry-file and --generated-registry-file, or the
// lint would answer from a registry nobody passed it.
func TestLintBaselineChangeFiles_readsTheRegistryFlags(t *testing.T) {
	// Given: a baseline that drops a group the registry scopes to cdk alone,
	// and one that drops a test cli is still asked to run.
	oldBaseline := writeTempJSON(t, "old-baseline.json", &compatBaseline{Version: baselineVersion, Entries: []baselineEntry{
		{Suite: "java-sdk", Service: "cdk", Group: "cdk-lifecycle", Test: "DeployStack", Status: compat.StatusSkip},
		{Suite: "cli", Service: "s3", Group: "s3-crud", Test: "CreateBucket", Status: compat.StatusPass},
	}})
	newBaseline := writeTempJSON(t, "new-baseline.json", &compatBaseline{Version: baselineVersion})
	registry := writeTempJSON(t, "registry.json", &parityRegistry{Groups: []parityGroup{
		{Service: "cdk", Name: "cdk-lifecycle", Suites: []string{"cdk"}, Tests: []parityTest{{Name: "DeployStack"}}},
		{Service: "s3", Name: "s3-crud", Tests: []parityTest{{Name: "CreateBucket"}}},
	}})
	defer swapFlag(registryFile, registry)()
	defer swapFlag(generatedRegistryFile, filepath.Join(t.TempDir(), "absent.json"))()

	// When/Then: the cli removal fails the lint and the cdk-scoped one does not.
	err := lintBaselineChangeFiles(oldBaseline, newBaseline)
	if err == nil {
		t.Fatal("lint passed, want the in-scope removal to fail it")
	}
	if !strings.Contains(err.Error(), "1 compat baseline downgrade(s)") {
		t.Fatalf("error = %v, want exactly one issue", err)
	}
}

// debtFile builds a parity-debt file listing the given suite/group pairs.
func debtFile(keys ...[2]string) *parityDebtFile {
	file := &parityDebtFile{Version: parityDebtVersion}
	for _, k := range keys {
		file.Debt = append(file.Debt, parityDebtEntry{Suite: k[0], Service: "appconfig", Group: k[1], Tests: 1})
	}
	return file
}

// TestLintBaselineChange_closedParityDebt covers the one downgrade the lint
// lets through: a parity-debt skip ("not yet implemented in <suite> test
// suite") becoming an honest `unimplemented` in the change that closes that
// debt. The suite never had the test; now it runs it and the emulator answers
// 501, which is exactly "adding a test that is unimplemented".
func TestLintBaselineChange_closedParityDebt(t *testing.T) {
	const suite, group = "python-sdk", "appconfig-deployments"
	skipped := baselineEntry{Suite: suite, Service: "appconfig", Group: group, Test: "StartDeployment", Status: compat.StatusSkip}
	withStatus := func(status compat.Status) baselineEntry {
		e := skipped
		e.Status = status
		return e
	}
	listed := debtFile([2]string{suite, group})
	other := debtFile([2]string{"go-sdk", group})

	tests := []struct {
		name    string
		old     baselineEntry
		new     baselineEntry
		oldDebt *parityDebtFile
		newDebt *parityDebtFile
		// want is "note" for an allowed closed-debt change, or "issue" for a
		// lint failure.
		want string
	}{
		{
			name: "skip to unimplemented is allowed when this change closes the debt",
			old:  skipped, new: withStatus(compat.StatusUnimplemented),
			oldDebt: listed, newDebt: debtFile(),
			want: "note",
		},
		{
			name: "a skip that was not parity debt may not be downgraded",
			old:  skipped, new: withStatus(compat.StatusUnimplemented),
			oldDebt: other, newDebt: debtFile(),
			want: "issue",
		},
		{
			name: "debt still listed afterwards has not been closed",
			old:  skipped, new: withStatus(compat.StatusUnimplemented),
			oldDebt: listed, newDebt: listed,
			want: "issue",
		},
		{
			name: "closing the debt never licenses a fail",
			old:  skipped, new: withStatus(compat.StatusFail),
			oldDebt: listed, newDebt: debtFile(),
			want: "issue",
		},
		{
			name: "closing the debt does not excuse a pass downgrade",
			old:  withStatus(compat.StatusPass), new: withStatus(compat.StatusUnimplemented),
			oldDebt: listed, newDebt: debtFile(),
			want: "issue",
		},
		{
			name: "closing the debt does not excuse an na downgrade",
			old:  withStatus(compat.StatusNA), new: withStatus(compat.StatusUnimplemented),
			oldDebt: listed, newDebt: debtFile(),
			want: "issue",
		},
		{
			name: "no parity-debt files means no allowance",
			old:  skipped, new: withStatus(compat.StatusUnimplemented),
			want: "issue",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Given: a one-row baseline change and the parity-debt files on
			// either side of it.
			oldBaseline := &compatBaseline{Version: baselineVersion, Entries: []baselineEntry{tc.old}}
			newBaseline := &compatBaseline{Version: baselineVersion, Entries: []baselineEntry{tc.new}}
			var closed closedParityDebt
			if tc.oldDebt != nil {
				closed = closedParityDebtBetween(tc.oldDebt, tc.newDebt)
			}

			// When: the change is linted.
			issues, notes, debtNotes := lintBaselineChange(oldBaseline, newBaseline, nil, closed)

			// Then: it is either a named, allowed closure or a failure — never
			// silently accepted.
			if len(notes) != 0 {
				t.Fatalf("out-of-scope notes = %#v, want none", notes)
			}
			switch tc.want {
			case "note":
				if len(issues) != 0 || len(debtNotes) != 1 {
					t.Fatalf("issues = %#v, debt notes = %#v; want exactly one debt note", issues, debtNotes)
				}
				if !strings.Contains(debtNotes[0], "python-sdk/appconfig-deployments/StartDeployment skip -> unimplemented") ||
					!strings.Contains(debtNotes[0], "parity debt closed") {
					t.Fatalf("debt note = %q", debtNotes[0])
				}
			case "issue":
				if len(issues) != 1 || len(debtNotes) != 0 {
					t.Fatalf("issues = %#v, debt notes = %#v; want exactly one issue", issues, debtNotes)
				}
				if !strings.Contains(issues[0], "downgrade") {
					t.Fatalf("issue = %q", issues[0])
				}
			default:
				t.Fatalf("unknown want %q", tc.want)
			}
		})
	}
}

// TestLintBaselineChangeFiles_readsTheParityDebtFlags is the plumbing test for
// the closed-debt allowance: the two files come from --lint-parity-debt-from
// and --lint-parity-debt-to, and a side that is absent grants nothing.
func TestLintBaselineChangeFiles_readsTheParityDebtFlags(t *testing.T) {
	// Given: a baseline change that turns a parity-debt skip into
	// unimplemented, and a debt file that no longer lists the group.
	row := baselineEntry{Suite: "cli", Service: "appconfig", Group: "appconfig-deployments", Test: "StartDeployment", Status: compat.StatusSkip}
	oldBaseline := writeTempJSON(t, "old-baseline.json", &compatBaseline{Version: baselineVersion, Entries: []baselineEntry{row}})
	row.Status = compat.StatusUnimplemented
	newBaseline := writeTempJSON(t, "new-baseline.json", &compatBaseline{Version: baselineVersion, Entries: []baselineEntry{row}})
	oldDebt := writeTempJSON(t, "old-debt.json", debtFile([2]string{"cli", "appconfig-deployments"}))
	newDebt := writeTempJSON(t, "new-debt.json", debtFile())
	absent := filepath.Join(t.TempDir(), "absent.json")
	defer swapFlag(registryFile, absent)()
	defer swapFlag(generatedRegistryFile, absent)()

	cases := []struct {
		name     string
		from, to string
		wantPass bool
	}{
		{name: "both files present and the debt closed", from: oldDebt, to: newDebt, wantPass: true},
		{name: "no old debt file grants nothing", from: absent, to: newDebt},
		{name: "old debt flag unset grants nothing", from: "", to: newDebt},
		{name: "no new debt file grants nothing", from: oldDebt, to: absent},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			defer swapFlag(lintParityDebtFrom, tc.from)()
			defer swapFlag(lintParityDebtTo, tc.to)()

			// When: the files are linted.
			err := lintBaselineChangeFiles(oldBaseline, newBaseline)

			// Then: only a debt closure both files attest to passes.
			if tc.wantPass && err != nil {
				t.Fatalf("lint failed: %v", err)
			}
			if !tc.wantPass && err == nil {
				t.Fatal("lint passed, want the skip -> unimplemented downgrade rejected")
			}
		})
	}
}

func TestLintFlakyChange_newQuarantineRejected(t *testing.T) {
	// Given: a change that adds a test to the flaky list
	tracked := flakyEntry{
		Suite: "dotnet-sdk", Group: "sns-subscriptions", Test: "PublishDeliveredToSQS",
		Reason: "known race", Issue: "https://github.com/overcast-sh/overcast/issues/388",
		Since: refTime().Format(flakyDateLayout),
	}
	oldFlaky := &flakyFile{Version: flakyVersion, Flaky: []flakyEntry{tracked}}
	newFlaky := &flakyFile{Version: flakyVersion, Flaky: []flakyEntry{tracked, {
		Suite: "go-sdk", Group: "s3-crud", Test: "CreateBucket",
		Reason: "sometimes fails", Issue: "https://github.com/overcast-sh/overcast/issues/999",
		Since: refTime().Format(flakyDateLayout),
	}}}

	// When: the change is linted
	issues := lintFlakyChange(oldFlaky, newFlaky, refTime(), false)

	// Then: it is rejected. Without this the flaky list is an amnesty file —
	// any failing test can be silenced by adding a line to it, and the baseline
	// gate quietly stops covering it.
	if len(issues) != 1 {
		t.Fatalf("issues = %#v, want 1", issues)
	}
	if !strings.Contains(issues[0], "go-sdk/s3-crud/CreateBucket") {
		t.Errorf("issue message = %q", issues[0])
	}
}

func TestLintFlakyChange_removingAQuarantineIsTheGoal(t *testing.T) {
	// Given: a change that deletes an entry — the test was fixed
	fixed := flakyEntry{
		Suite: "dotnet-sdk", Group: "sns-subscriptions", Test: "PublishDeliveredToSQS",
		Reason: "known race", Issue: "https://github.com/overcast-sh/overcast/issues/388",
		Since: refTime().Format(flakyDateLayout),
	}
	kept := flakyEntry{
		Suite: "dotnet-sdk", Group: "sns-subscriptions", Test: "Unsubscribe",
		Reason: "cascade", Issue: "https://github.com/overcast-sh/overcast/issues/388",
		Since: refTime().Format(flakyDateLayout),
	}
	oldFlaky := &flakyFile{Version: flakyVersion, Flaky: []flakyEntry{fixed, kept}}
	newFlaky := &flakyFile{Version: flakyVersion, Flaky: []flakyEntry{kept}}

	// When/Then: shrinking is always allowed — that is the whole point
	if issues := lintFlakyChange(oldFlaky, newFlaky, refTime(), false); len(issues) != 0 {
		t.Fatalf("issues = %#v, want none when the list shrinks", issues)
	}
}

func TestLintFlakyChange_entryNeedsAReason(t *testing.T) {
	// Given: an existing entry stripped of its reason
	entry := flakyEntry{
		Suite: "dotnet-sdk", Group: "sns-subscriptions", Test: "PublishDeliveredToSQS",
		Reason: "known race", Issue: "https://github.com/overcast-sh/overcast/issues/388",
		Since: refTime().Format(flakyDateLayout),
	}
	oldFlaky := &flakyFile{Version: flakyVersion, Flaky: []flakyEntry{entry}}
	stripped := entry
	stripped.Reason = "  "
	newFlaky := &flakyFile{Version: flakyVersion, Flaky: []flakyEntry{stripped}}

	// When/Then: rejected. An entry without evidence is untriageable, and the
	// next person cannot tell a real flake from a silenced failure.
	issues := lintFlakyChange(oldFlaky, newFlaky, refTime(), false)
	if len(issues) != 1 || !strings.Contains(issues[0], "reason") {
		t.Fatalf("issues = %#v, want a missing-reason issue", issues)
	}
}

func TestLintFlakyChange_entryNeedsATrackingIssueAndDate(t *testing.T) {
	// Given: an existing entry with no issue and no date
	oldFlaky := &flakyFile{Version: 2, Flaky: []flakyEntry{
		{Suite: "cli", Group: "g", Test: "T", Reason: "intermittent"},
	}}
	newFlaky := &flakyFile{Version: 2, Flaky: []flakyEntry{
		{Suite: "cli", Group: "g", Test: "T", Reason: "intermittent"},
	}}

	// When/Then: both are required. Without a date the entry cannot age out,
	// and without an issue nobody is investigating — which is how a stop-gap
	// quietly becomes the permanent state.
	issues := lintFlakyChange(oldFlaky, newFlaky, refTime(), false)
	joined := strings.Join(issues, "\n")
	if !strings.Contains(joined, "issue") {
		t.Errorf("missing tracking-issue complaint: %q", joined)
	}
	if !strings.Contains(joined, "since") {
		t.Errorf("missing since-date complaint: %q", joined)
	}
}

func TestLintFlakyChange_overdueEntryBlocksPullRequests(t *testing.T) {
	// Given: an entry quarantined longer than the hard deadline
	stale := &flakyFile{Version: 2, Flaky: []flakyEntry{{
		Suite: "cli", Group: "g", Test: "T", Reason: "intermittent",
		Issue: "https://github.com/overcast-sh/overcast/issues/388",
		Since: refTime().AddDate(0, 0, -(flakyHardDeadlineDays + 1)).Format(flakyDateLayout),
	}}}

	// When/Then: it fails the lint. Tolerating it indefinitely would mean the
	// gate has quietly stopped covering that test for good.
	issues := lintFlakyChange(stale, stale, refTime(), false)
	if len(issues) != 1 || !strings.Contains(issues[0], "quarantined for") {
		t.Fatalf("issues = %#v, want an overdue complaint", issues)
	}
	if !strings.Contains(issues[0], "issues/388") {
		t.Errorf("overdue message should point at the tracking issue: %q", issues[0])
	}
}

func TestLintFlakyChange_recentEntryIsFine(t *testing.T) {
	// Given: an entry quarantined yesterday, properly recorded
	fresh := &flakyFile{Version: 2, Flaky: []flakyEntry{{
		Suite: "cli", Group: "g", Test: "T", Reason: "intermittent",
		Issue: "https://github.com/overcast-sh/overcast/issues/388",
		Since: refTime().AddDate(0, 0, -1).Format(flakyDateLayout),
	}}}

	if issues := lintFlakyChange(fresh, fresh, refTime(), false); len(issues) != 0 {
		t.Fatalf("issues = %#v, want none for a fresh, tracked entry", issues)
	}
}

func TestFlakyOverdue_reportsSoftDeadlineSeparately(t *testing.T) {
	// Given: one entry past the soft deadline, one still inside it
	file := &flakyFile{Version: 2, Flaky: []flakyEntry{
		{Suite: "a", Group: "g", Test: "Old", Issue: "i", Reason: "r",
			Since: refTime().AddDate(0, 0, -(flakySoftDeadlineDays + 1)).Format(flakyDateLayout)},
		{Suite: "b", Group: "g", Test: "New", Issue: "i", Reason: "r",
			Since: refTime().AddDate(0, 0, -1).Format(flakyDateLayout)},
	}}

	// When: the nightly job asks what has gone stale
	// Then: only the older one is named. The soft deadline is what makes the
	// nightly job nag before the hard deadline blocks anyone.
	overdue := flakyOverdue(file, refTime(), flakySoftDeadlineDays)
	if len(overdue) != 1 || !strings.Contains(overdue[0], "a/g/Old") {
		t.Fatalf("overdue = %#v, want only the stale entry", overdue)
	}
}

// refTime is a fixed clock so deadline arithmetic in tests cannot drift.
func refTime() time.Time {
	return time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC)
}

func TestLintFlakyChange_seedingAnEmptyListIsAllowed(t *testing.T) {
	// Given: the first population of the list, mirroring the baseline's
	// seeding exemption. The entry still carries a reason, a date and a
	// tracking issue — seeding exempts growth, not accountability.
	oldFlaky := &flakyFile{Version: flakyVersion}
	newFlaky := &flakyFile{Version: flakyVersion, Flaky: []flakyEntry{{
		Suite: "dotnet-sdk", Group: "sns-subscriptions", Test: "PublishDeliveredToSQS",
		Reason: "known race", Issue: "https://github.com/overcast-sh/overcast/issues/388",
		Since: refTime().Format(flakyDateLayout),
	}}}

	if issues := lintFlakyChange(oldFlaky, newFlaky, refTime(), false); len(issues) != 0 {
		t.Fatalf("issues = %#v, want none when seeding", issues)
	}
}

func TestLintFlakyChange_seedingStillRequiresAccountability(t *testing.T) {
	// Given: a first population whose entry has no issue and no date
	oldFlaky := &flakyFile{Version: flakyVersion}
	newFlaky := &flakyFile{Version: flakyVersion, Flaky: []flakyEntry{
		{Suite: "dotnet-sdk", Group: "sns-subscriptions", Test: "PublishDeliveredToSQS", Reason: "known race"},
	}}

	// Then: rejected. The seeding exemption is about not blocking the first
	// commit of the file, not about letting untracked entries in through it.
	if issues := lintFlakyChange(oldFlaky, newFlaky, refTime(), false); len(issues) != 2 {
		t.Fatalf("issues = %#v, want complaints about the missing issue and date", issues)
	}
}

func TestLintFlakyChange_approvedGrowthIsAccepted(t *testing.T) {
	// Given: a new, fully recorded entry, and a reviewer who has agreed to the
	// quarantine (the quarantine-approved label on the PR).
	tracked := flakyEntry{
		Suite: "dotnet-sdk", Group: "sns-subscriptions", Test: "PublishDeliveredToSQS",
		Reason: "known race", Issue: "https://github.com/overcast-sh/overcast/issues/388",
		Since: refTime().Format(flakyDateLayout),
	}
	oldFlaky := &flakyFile{Version: flakyVersion, Flaky: []flakyEntry{tracked}}
	newFlaky := &flakyFile{Version: flakyVersion, Flaky: []flakyEntry{tracked, {
		Suite: "python-sdk", Group: "lambda-crud", Test: "DeleteFunction",
		Reason: "deletion visible late", Issue: "https://github.com/overcast-sh/overcast/issues/414",
		Since: refTime().Format(flakyDateLayout),
	}}}

	// When/Then: growth passes — the agreement the growth lint exists to force
	// has been given, so failing anyway would just teach people to bypass CI.
	if issues := lintFlakyChange(oldFlaky, newFlaky, refTime(), true); len(issues) != 0 {
		t.Fatalf("issues = %#v, want none for approved growth", issues)
	}
}

func TestLintFlakyChange_approvalWaivesGrowthOnly(t *testing.T) {
	// Given: an approved addition that carries no reason, issue, or date
	oldFlaky := &flakyFile{Version: flakyVersion, Flaky: []flakyEntry{{
		Suite: "dotnet-sdk", Group: "sns-subscriptions", Test: "PublishDeliveredToSQS",
		Reason: "known race", Issue: "https://github.com/overcast-sh/overcast/issues/388",
		Since: refTime().Format(flakyDateLayout),
	}}}
	newFlaky := &flakyFile{Version: flakyVersion}
	newFlaky.Flaky = append(newFlaky.Flaky, oldFlaky.Flaky...)
	newFlaky.Flaky = append(newFlaky.Flaky, flakyEntry{
		Suite: "python-sdk", Group: "lambda-crud", Test: "DeleteFunction",
	})

	// Then: still rejected. The label answers "may this test leave the gate?",
	// not "may it do so untracked?" — accountability checks never wave.
	issues := lintFlakyChange(oldFlaky, newFlaky, refTime(), true)
	if len(issues) != 3 {
		t.Fatalf("issues = %#v, want missing reason + issue + date complaints", issues)
	}
	for _, issue := range issues {
		if strings.Contains(issue, "flaky list grew") {
			t.Fatalf("approval should waive the growth complaint, got %q", issue)
		}
	}
}

func TestCompareBaseline_flakyTestIsToleratedInEitherDirection(t *testing.T) {
	// Given: a test known to be intermittent, declared in the flaky list
	baseline := &compatBaseline{Version: baselineVersion, Entries: []baselineEntry{
		{Suite: "dotnet-sdk", Group: "sns-subscriptions", Test: "PublishDeliveredToSQS", Status: compat.StatusFail},
	}}
	flaky := flakySet{"dotnet-sdk/sns-subscriptions/PublishDeliveredToSQS": true}

	// When: it fails on one run and passes on the next
	for _, status := range []compat.Status{compat.StatusFail, compat.StatusPass, compat.StatusSkip} {
		report := reportWithResults(resultSpec{
			suite: "dotnet-sdk", service: "sns", group: "sns-subscriptions",
			test: "PublishDeliveredToSQS", status: status,
		})
		// Then: neither outcome blocks a PR. An intermittent test that
		// randomly reds the build teaches people to ignore the gate.
		if got := compareBaselineWith(baseline, report, flaky, candidateSet{}); len(got) != 0 {
			t.Errorf("status %s produced regressions %#v, want none", status, got)
		}
	}
}

func TestCompareBaseline_flakyListDoesNotExcuseOtherTests(t *testing.T) {
	// Given: one flaky test declared, and a different test that regresses
	baseline := &compatBaseline{Version: baselineVersion, Entries: []baselineEntry{
		{Suite: "dotnet-sdk", Group: "sns-subscriptions", Test: "PublishDeliveredToSQS", Status: compat.StatusFail},
		{Suite: "dotnet-sdk", Group: "sns-subscriptions", Test: "SubscribeSQS", Status: compat.StatusPass},
	}}
	flaky := flakySet{"dotnet-sdk/sns-subscriptions/PublishDeliveredToSQS": true}
	report := reportWithResults(
		resultSpec{suite: "dotnet-sdk", service: "sns", group: "sns-subscriptions", test: "PublishDeliveredToSQS", status: compat.StatusPass},
		resultSpec{suite: "dotnet-sdk", service: "sns", group: "sns-subscriptions", test: "SubscribeSQS", status: compat.StatusFail},
	)

	// Then: the quarantine is per test, not a blanket amnesty
	got := compareBaselineWith(baseline, report, flaky, candidateSet{})
	if len(got) != 1 || !strings.Contains(got[0], "SubscribeSQS") {
		t.Fatalf("regressions = %#v, want only SubscribeSQS", got)
	}
}

func TestUpdateBaseline_doesNotPromoteFlakyTests(t *testing.T) {
	// Given: a flaky test recorded at its worst observed status
	baseline := &compatBaseline{Version: baselineVersion, Entries: []baselineEntry{
		{Suite: "dotnet-sdk", Group: "sns-subscriptions", Test: "PublishDeliveredToSQS", Status: compat.StatusFail},
		{Suite: "go-sdk", Group: "s3-crud", Test: "CreateBucket", Status: compat.StatusFail},
	}}
	flaky := flakySet{"dotnet-sdk/sns-subscriptions/PublishDeliveredToSQS": true}
	report := reportWithResults(
		resultSpec{suite: "dotnet-sdk", service: "sns", group: "sns-subscriptions", test: "PublishDeliveredToSQS", status: compat.StatusPass},
		resultSpec{suite: "go-sdk", service: "s3", group: "s3-crud", test: "CreateBucket", status: compat.StatusPass},
	)

	// When: the baseline is promoted from a run where the flaky test happened
	// to pass
	updated := updateBaselineWith(baseline, report, flaky, candidateSet{})
	entries := baselineEntryMap(updated.Entries)

	// Then: the flaky test keeps its floor — promoting it would make the very
	// next intermittent failure a red build. A genuine fix is promoted by
	// removing it from the flaky list.
	if got := entries["dotnet-sdk/sns-subscriptions/PublishDeliveredToSQS"].Status; got != compat.StatusFail {
		t.Errorf("flaky test promoted to %s, want it held at fail", got)
	}
	// And: everything else still ratchets normally.
	if got := entries["go-sdk/s3-crud/CreateBucket"].Status; got != compat.StatusPass {
		t.Errorf("non-flaky test = %s, want pass", got)
	}
}

func TestBaselineAnnotations_renderGitHubErrorCommands(t *testing.T) {
	// Given: a set of regression messages
	regressions := []string{
		"compat baseline regression: node-js-sdk/sqs-basic/SendMessage pass -> fail",
		"compat baseline new failure, not in baseline: rust-sdk/s3-copy/CreateSourceBucket",
	}

	// When: they are rendered as GitHub workflow commands
	out := baselineAnnotations(regressions)

	// Then: each becomes an ::error line so it lands on the PR checks tab, with
	// newlines escaped — a raw newline would truncate the annotation.
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 2 {
		t.Fatalf("got %d annotation lines, want 2: %q", len(lines), out)
	}
	for i, line := range lines {
		if !strings.HasPrefix(line, "::error title=Compat baseline::") {
			t.Errorf("line %d = %q, want an ::error annotation", i, line)
		}
	}
	if !strings.Contains(lines[0], "SendMessage pass -> fail") {
		t.Errorf("annotation lost its detail: %q", lines[0])
	}
}

func TestBaselineAnnotations_escapesMultilineDetail(t *testing.T) {
	// Given: a regression message carrying a multi-line error body
	out := baselineAnnotations([]string{"compat baseline regression: a/b/c pass -> fail\nsecond line"})

	// When/Then: the annotation stays on one line, with the newline percent-encoded
	// as GitHub's workflow-command format requires.
	if strings.Count(strings.TrimSpace(out), "\n") != 0 {
		t.Fatalf("annotation spans multiple lines: %q", out)
	}
	if !strings.Contains(out, "%0A") {
		t.Fatalf("newline not escaped as %%0A: %q", out)
	}
}

func TestFailuresOverLimit_namesEveryFailure(t *testing.T) {
	// Given: a run in which two tests failed outright.
	report := reportWithResults(
		resultSpec{suite: "go-sdk", service: "s3", group: "s3-crud", test: "CreateBucket", status: compat.StatusPass},
		resultSpec{suite: "go-sdk", service: "rds", group: "rds-subnet-groups", test: "CreateDBSubnetGroup", status: compat.StatusFail},
		resultSpec{suite: "rust-sdk", service: "ssm", group: "ssm-path", test: "GetParametersByPath", status: compat.StatusFail},
	)

	// When: the absolute failure gate runs with no failures allowed.
	failures := failuresOverLimit(report, flakySet{}, candidateSet{}, 0)

	// Then: both are named. This gate consults no baseline, so a failure the
	// baseline happens to grandfather is reported like any other.
	if len(failures) != 2 {
		t.Fatalf("failures = %#v, want 2", failures)
	}
	if !strings.Contains(failures[0], "go-sdk/rds-subnet-groups/CreateDBSubnetGroup") {
		t.Errorf("failures[0] = %q", failures[0])
	}
	if !strings.Contains(failures[1], "rust-sdk/ssm-path/GetParametersByPath") {
		t.Errorf("failures[1] = %q", failures[1])
	}
}

func TestFailuresOverLimit_onlyFailCounts(t *testing.T) {
	// Given: a run whose non-passing results are all legitimate resting states —
	// an emulator gap, an environmental skip, an API the SDK does not have.
	report := reportWithResults(
		resultSpec{suite: "cli", service: "s3", group: "s3-crud", test: "CreateBucket", status: compat.StatusPass},
		resultSpec{suite: "cli", service: "s3", group: "s3-crud", test: "PutBucketAcl", status: compat.StatusUnimplemented},
		resultSpec{suite: "cli", service: "lambda", group: "lambda-invoke", test: "Invoke", status: compat.StatusSkip},
		resultSpec{suite: "cli", service: "sts", group: "sts-identity", test: "AssumeRole", status: compat.StatusNA},
	)

	// When/Then: none of them trips the gate. "No failures" means no wrong
	// answers, not full coverage.
	if failures := failuresOverLimit(report, flakySet{}, candidateSet{}, 0); len(failures) != 0 {
		t.Fatalf("failures = %#v, want none", failures)
	}
}

func TestFailuresOverLimit_ignoresQuarantinedTests(t *testing.T) {
	// Given: a failing test that a reviewer has quarantined as intermittent.
	report := reportWithResults(
		resultSpec{suite: "dotnet-sdk", service: "sns", group: "sns-subscriptions", test: "PublishDeliveredToSQS", status: compat.StatusFail},
		resultSpec{suite: "dotnet-sdk", service: "sqs", group: "sqs-messages", test: "PurgeQueue", status: compat.StatusFail},
	)
	flaky := flakySet{"dotnet-sdk/sns-subscriptions/PublishDeliveredToSQS": true}

	// When/Then: the quarantine holds here too — otherwise this gate would red
	// the build at random and undo the whole point of the flaky list — but it is
	// per test, not a blanket amnesty.
	failures := failuresOverLimit(report, flaky, candidateSet{}, 0)
	if len(failures) != 1 || !strings.Contains(failures[0], "PurgeQueue") {
		t.Fatalf("failures = %#v, want only PurgeQueue", failures)
	}
}

func TestFailuresOverLimit_underLimitPasses(t *testing.T) {
	// Given: one failure and a limit that tolerates it.
	report := reportWithResults(
		resultSpec{suite: "go-sdk", service: "rds", group: "rds-subnet-groups", test: "CreateDBSubnetGroup", status: compat.StatusFail},
	)

	// When/Then: at or under the limit nothing is reported; over it, everything
	// is — a partial list would understate the damage.
	if failures := failuresOverLimit(report, flakySet{}, candidateSet{}, 1); len(failures) != 0 {
		t.Fatalf("failures = %#v, want none at limit 1", failures)
	}
	if failures := failuresOverLimit(report, flakySet{}, candidateSet{}, 0); len(failures) != 1 {
		t.Fatalf("failures = %#v, want 1 at limit 0", failures)
	}
}

type resultSpec struct {
	suite   string
	service string
	group   string
	test    string
	status  compat.Status
}

func reportWithResults(results ...resultSpec) *compat.RunReport {
	suites := make(map[string]*compat.SuiteReport)
	groups := make(map[string]*compat.GroupReport)
	for _, result := range results {
		suite := suites[result.suite]
		if suite == nil {
			suite = &compat.SuiteReport{Suite: result.suite}
			suites[result.suite] = suite
		}
		groupKey := result.suite + "/" + result.group
		group := groups[groupKey]
		if group == nil {
			group = &compat.GroupReport{Suite: result.suite, Service: result.service, Name: result.group}
			groups[groupKey] = group
			suite.Groups = append(suite.Groups, group)
		}
		group.Tests = append(group.Tests, compat.TestResultEvent{
			Suite:   result.suite,
			Service: result.service,
			Group:   result.group,
			Test:    result.test,
			Status:  result.status,
		})
	}
	var report compat.RunReport
	for _, suite := range suites {
		report.Suites = append(report.Suites, suite)
	}
	return &report
}
