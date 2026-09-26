package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/overcast-sh/overcast/compat"
)

const baselineVersion = 1

type compatBaseline struct {
	Version int             `json:"version"`
	Entries []baselineEntry `json:"entries"`
}

type baselineEntry struct {
	Suite   string        `json:"suite"`
	Service string        `json:"service"`
	Group   string        `json:"group"`
	Test    string        `json:"test"`
	Op      string        `json:"op,omitempty"`
	Status  compat.Status `json:"status"`
}

func compareBaselineFile(baselinePath, resultsPath string) error {
	baseline, err := readBaselineFile(baselinePath)
	if err != nil {
		return err
	}
	report, err := readRunReportFile(resultsPath)
	if err != nil {
		return err
	}
	flaky, err := readFlakyFile(*flakyFilePath)
	if err != nil {
		return err
	}
	candidates, err := loadCandidateGroups()
	if err != nil {
		return err
	}
	regressions := compareBaselineWith(baseline, report, flaky, candidates)
	if len(regressions) > 0 {
		for _, regression := range regressions {
			fmt.Fprintln(os.Stderr, regression)
		}
		if *annotate {
			// stdout so a workflow step can tee it; GitHub picks the commands
			// up from either stream.
			fmt.Print(baselineAnnotations(regressions))
		}
		return fmt.Errorf("%d compat baseline regression(s)", len(regressions))
	}
	fmt.Printf("compat: baseline check passed (%d expected result(s))\n", len(baseline.Entries))
	return nil
}

// enforceMaxFailuresFile is the --max-failures entry point: the absolute
// failure gate, asserted alongside the relative one.
func enforceMaxFailuresFile(resultsPath string, limit int) error {
	report, err := readRunReportFile(resultsPath)
	if err != nil {
		return err
	}
	flaky, err := readFlakyFile(*flakyFilePath)
	if err != nil {
		return err
	}
	candidates, err := loadCandidateGroups()
	if err != nil {
		return err
	}
	failures := failuresOverLimit(report, flaky, candidates, limit)
	if len(failures) > 0 {
		for _, failure := range failures {
			fmt.Fprintln(os.Stderr, failure)
		}
		if *annotate {
			fmt.Print(errorAnnotations("Compat failures", failures))
		}
		return fmt.Errorf("%d compat failure(s), limit %d", len(failures), limit)
	}
	fmt.Printf("compat: failure gate passed (at most %d failure(s) allowed)\n", limit)
	return nil
}

// failuresOverLimit names every test that failed outright, once quarantined
// ones are set aside, when there are more of them than limit allows.
//
// This is deliberately not a baseline comparison. The baseline gate asks a
// relative question — did anything get worse than recorded — which was the
// right shape while the grandfathered fail set was being burned down, but it
// leaves a `fail` the baseline still records passing CI. That set reached zero
// in #462, so the invariant is now absolute: any failure is a regression,
// whatever any file says. Asking it directly also means a stale baseline can no
// longer weaken the gate, which is not hypothetical — promotion could not
// publish for weeks (issue #440) and the recorded fail set sat 26 entries
// behind reality the whole time.
//
// Quarantined tests are exempt here as everywhere else: a gate that reds the
// build at random is one people learn to re-run rather than read. That
// exemption is reviewer-approved per test, and the flaky list is itself linted.
//
// Candidate generated groups are exempt for the opposite reason — see
// candidateSet in generatedregistry.go. A quarantined test escaped a gate it
// was already under; a candidate has not entered it yet.
func failuresOverLimit(report *compat.RunReport, flaky flakySet, candidates candidateSet, limit int) []string {
	var failures []string
	for _, entry := range baselineEntriesFromReport(report).Entries {
		if entry.Status != compat.StatusFail {
			continue
		}
		key := baselineKey(entry)
		if flaky[key] || candidates.exempt(entry.Group) {
			continue
		}
		failures = append(failures, "compat failure: "+key)
	}
	if len(failures) <= limit {
		return nil
	}
	sort.Strings(failures)
	return failures
}

func updateBaselineFile(baselinePath, resultsPath string) error {
	baseline, err := readBaselineFileIfExists(baselinePath)
	if err != nil {
		return err
	}
	report, err := readRunReportFile(resultsPath)
	if err != nil {
		return err
	}
	flaky, err := readFlakyFile(*flakyFilePath)
	if err != nil {
		return err
	}
	candidates, err := loadCandidateGroups()
	if err != nil {
		return err
	}
	updated := updateBaselineWith(baseline, report, flaky, candidates)
	return writeBaselineFile(baselinePath, updated)
}

func lintBaselineChangeFiles(oldPath, newPath string) error {
	oldBaseline, err := readBaselineFile(oldPath)
	if err != nil {
		return err
	}
	newBaseline, err := readBaselineFile(newPath)
	if err != nil {
		return err
	}
	scope, err := loadRegistryScope()
	if err != nil {
		return err
	}
	closedDebt, err := loadClosedParityDebt(*lintParityDebtFrom, *lintParityDebtTo)
	if err != nil {
		return err
	}
	issues, notes, debtNotes := lintBaselineChange(oldBaseline, newBaseline, scope, closedDebt)
	// Notes print whether or not the lint passes. A removal the registry no
	// longer asks for is exactly what a reviewer wants named, and suppressing
	// it because some other entry tripped the gate would hide the half of the
	// diff nothing else reports. The same goes for a downgrade let through
	// because this change closed its parity debt.
	for _, note := range notes {
		fmt.Println(note)
	}
	for _, note := range debtNotes {
		fmt.Println(note)
	}
	if *annotate && len(notes) > 0 {
		fmt.Print(outOfScopeAnnotation(notes))
	}
	if *annotate && len(debtNotes) > 0 {
		fmt.Print(closedDebtAnnotation(debtNotes))
	}
	if len(issues) > 0 {
		for _, issue := range issues {
			fmt.Fprintln(os.Stderr, issue)
		}
		if *annotate {
			fmt.Print(baselineAnnotations(issues))
		}
		return fmt.Errorf("%d compat baseline downgrade(s)", len(issues))
	}
	fmt.Printf("compat: baseline change lint passed (%d expected result(s), %d out-of-scope removal(s), %d closed-debt change(s))\n",
		len(newBaseline.Entries), len(notes), len(debtNotes))
	return nil
}

// closedParityDebt is the set of suite/group keys (parityDebtEntry.key) whose
// parity debt the change under lint closes: listed in the old parity-debt file,
// gone from the new one. A nil set closes nothing.
type closedParityDebt map[string]bool

func (c closedParityDebt) closes(suite, group string) bool {
	return c[parityDebtEntry{Suite: suite, Group: group}.key()]
}

// closedParityDebtBetween is the debt oldDebt records and newDebt no longer
// does.
func closedParityDebtBetween(oldDebt, newDebt *parityDebtFile) closedParityDebt {
	still := make(map[string]bool, len(newDebt.Debt))
	for _, entry := range newDebt.Debt {
		still[entry.key()] = true
	}
	closed := closedParityDebt{}
	for _, entry := range oldDebt.Debt {
		if !still[entry.key()] {
			closed[entry.key()] = true
		}
	}
	return closed
}

// loadClosedParityDebt reads the parity-debt files on either side of the
// change; toPath defaults to --parity-debt-file. An unset or absent file on
// either side yields a nil set — no allowance, the lint as it was before it
// could ask. That is the safe direction for the old side (nothing proves the
// skip was debt) and the only safe one for the new side: readParityDebtFile
// reads an absent file as empty, which would mark every old entry closed.
func loadClosedParityDebt(fromPath, toPath string) (closedParityDebt, error) {
	if fromPath == "" {
		return nil, nil
	}
	if toPath == "" {
		toPath = *parityDebtFilePath
	}
	for _, path := range []string{fromPath, toPath} {
		if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
			fmt.Fprintf(os.Stderr,
				"compat: no parity-debt file at %s — no skip -> unimplemented change counts as closed debt\n", path)
			return nil, nil
		}
	}
	oldDebt, err := readParityDebtFile(fromPath)
	if err != nil {
		return nil, err
	}
	newDebt, err := readParityDebtFile(toPath)
	if err != nil {
		return nil, err
	}
	return closedParityDebtBetween(oldDebt, newDebt), nil
}

// loadRegistryScope reads the registry pair the removal check consults.
//
// A registry that is not there at all yields a nil scope, and a nil scope
// judges every removal exactly as the lint did before it could ask — the safe
// direction, and the one that keeps the lint usable wherever
// compat/suites/registry.json is not at the default path. A registry that is
// there but unreadable, or whose generated sibling collides with it, is an
// error: falling back silently would turn a broken file into a weaker gate.
func loadRegistryScope() (*registryScope, error) {
	if _, err := os.Stat(*registryFile); errors.Is(err, os.ErrNotExist) {
		fmt.Fprintf(os.Stderr,
			"compat: no registry at %s — every removed expectation will be reported\n", *registryFile)
		return nil, nil
	}
	reg, err := readParityRegistries(*registryFile, *generatedRegistryFile)
	if err != nil {
		return nil, err
	}
	return newRegistryScope(reg), nil
}

// flakySet is the set of baseline keys quarantined as intermittent.
type flakySet map[string]bool

// flakyFile is compat/flaky.json — tests known to give different answers on
// identical input. They are bugs, not a resting state; the list only shrinks.
type flakyFile struct {
	Version int          `json:"version"`
	Comment string       `json:"comment,omitempty"`
	Flaky   []flakyEntry `json:"flaky"`
}

type flakyEntry struct {
	Suite string `json:"suite"`
	Group string `json:"group"`
	Test  string `json:"test"`
	// Since is the date the test was quarantined (YYYY-MM-DD). It is what makes
	// an entry age out instead of becoming permanent.
	Since string `json:"since"`
	// Issue is the tracking issue for the investigation. Quarantine is a
	// stop-gap; without somewhere to record the investigation it is just a
	// silenced test.
	Issue  string `json:"issue"`
	Reason string `json:"reason"`
}

func (e flakyEntry) key() string { return e.Suite + "/" + e.Group + "/" + e.Test }

// since parses the quarantine date.
func (e flakyEntry) since() (time.Time, error) {
	return time.Parse(flakyDateLayout, strings.TrimSpace(e.Since))
}

const (
	// flakyVersion is the schema version of compat/flaky.json. v2 added the
	// `since` and `issue` fields that make quarantine a tracked stop-gap
	// rather than an open-ended exemption.
	flakyVersion    = 2
	flakyDateLayout = "2006-01-02"
	// flakySoftDeadlineDays is when the nightly flake-detection job starts
	// reporting an entry as overdue.
	flakySoftDeadlineDays = 14
	// flakyHardDeadlineDays is when the PR lint starts failing on it. Past this
	// point the gate has effectively stopped covering the test, so continuing
	// has to be a conscious, re-argued decision rather than drift.
	flakyHardDeadlineDays = 30
)

// lintFlakyChange rejects growth of the quarantine list.
//
// Quarantine removes a test from the baseline gate in both directions, so an
// unguarded list is an amnesty file: any inconvenient failure can be silenced
// by adding a line, and nothing ever notices the gate stopped covering it.
// Adding an entry is therefore a deliberate act that has to be argued for in
// review, not something CI waves through. growthApproved is how that agreement
// reaches the lint: CI sets it when a reviewer has applied the
// quarantine-approved label to the pull request. It waves through growth and
// nothing else — the per-entry accountability checks (reason, issue, date,
// deadline) apply regardless. Removing an entry — the fix — is always allowed,
// and seeding an empty list mirrors the baseline's own exemption.
func lintFlakyChange(oldFlaky, newFlaky *flakyFile, now time.Time, growthApproved bool) []string {
	var issues []string
	for _, e := range newFlaky.Flaky {
		if strings.TrimSpace(e.Reason) == "" {
			issues = append(issues, fmt.Sprintf(
				"compat flaky entry has no reason: %s — record what was observed, so the next person can tell a real flake from a silenced failure",
				e.key()))
		}
		if strings.TrimSpace(e.Issue) == "" {
			issues = append(issues, fmt.Sprintf(
				"compat flaky entry has no tracking issue: %s — quarantine is a stop-gap, and without an issue nobody is investigating it",
				e.key()))
		}
		since, err := e.since()
		switch {
		case strings.TrimSpace(e.Since) == "":
			issues = append(issues, fmt.Sprintf(
				"compat flaky entry has no since date: %s — without one the entry cannot age out, and a stop-gap becomes permanent",
				e.key()))
		case err != nil:
			issues = append(issues, fmt.Sprintf(
				"compat flaky entry has an unparseable since date: %s (%q, want %s)",
				e.key(), e.Since, flakyDateLayout))
		default:
			if days := int(now.Sub(since).Hours() / 24); days > flakyHardDeadlineDays {
				issues = append(issues, fmt.Sprintf(
					"compat flaky entry is overdue: %s has been quarantined for %d days (limit %d) — fix it, or say in %s why it still cannot be and re-date the entry",
					e.key(), days, flakyHardDeadlineDays, issueOrPlaceholder(e.Issue)))
			}
		}
	}
	if !growthApproved {
		for _, key := range flakyGrowth(oldFlaky, newFlaky) {
			issues = append(issues, fmt.Sprintf(
				"compat flaky list grew: %s — quarantine removes a test from the baseline gate entirely, so adding one needs a reviewer's agreement (the quarantine-approved label on the PR), not just a green build",
				key))
		}
	}
	sort.Strings(issues)
	return issues
}

// flakyGrowth names entries present in newFlaky but absent from oldFlaky.
// Seeding a previously empty list is not growth, mirroring the baseline's
// seeding exemption.
func flakyGrowth(oldFlaky, newFlaky *flakyFile) []string {
	if len(oldFlaky.Flaky) == 0 {
		return nil
	}
	oldByKey := make(map[string]bool, len(oldFlaky.Flaky))
	for _, e := range oldFlaky.Flaky {
		oldByKey[e.key()] = true
	}
	var grown []string
	for _, e := range newFlaky.Flaky {
		if !oldByKey[e.key()] {
			grown = append(grown, e.key())
		}
	}
	sort.Strings(grown)
	return grown
}

// flakyOverdue names entries quarantined longer than days. The nightly job uses
// the soft deadline to nag well before the hard one starts blocking pull
// requests.
func flakyOverdue(file *flakyFile, now time.Time, days int) []string {
	var out []string
	for _, e := range file.Flaky {
		since, err := e.since()
		if err != nil {
			continue
		}
		if age := int(now.Sub(since).Hours() / 24); age > days {
			out = append(out, fmt.Sprintf("%s — quarantined %d days ago, tracked in %s",
				e.key(), age, issueOrPlaceholder(e.Issue)))
		}
	}
	sort.Strings(out)
	return out
}

func issueOrPlaceholder(issue string) string {
	if strings.TrimSpace(issue) == "" {
		return "its tracking issue"
	}
	return issue
}

func lintFlakyChangeFiles(oldPath, newPath string, growthApproved bool) error {
	oldFlaky, err := readFlakyFileRaw(oldPath)
	if err != nil {
		return err
	}
	newFlaky, err := readFlakyFileRaw(newPath)
	if err != nil {
		return err
	}
	issues := lintFlakyChange(oldFlaky, newFlaky, time.Now(), growthApproved)
	if len(issues) > 0 {
		for _, issue := range issues {
			fmt.Fprintln(os.Stderr, issue)
		}
		if *annotate {
			fmt.Print(baselineAnnotations(issues))
		}
		return fmt.Errorf("%d compat flaky list issue(s)", len(issues))
	}
	// Approved growth passes, but never silently: name each new quarantine in
	// the log and as a PR annotation so the decision stays visible.
	if growthApproved {
		for _, key := range flakyGrowth(oldFlaky, newFlaky) {
			fmt.Printf("compat: flaky list grew with reviewer approval: %s\n", key)
			if *annotate {
				fmt.Printf("::notice title=Compat flaky list::%s\n",
					escapeAnnotationData("quarantined with reviewer approval: "+key))
			}
		}
	}
	fmt.Printf("compat: flaky list check passed (%d quarantined test(s))\n", len(newFlaky.Flaky))
	return nil
}

// reportFlakyOverdue is the --report-flaky-overdue entry point: the nightly
// job's nag, well before the PR lint's hard deadline starts blocking people.
func reportFlakyOverdue(path string) error {
	file, err := readFlakyFileRaw(path)
	if err != nil {
		return err
	}
	overdue := flakyOverdue(file, time.Now(), flakySoftDeadlineDays)
	if len(overdue) == 0 {
		fmt.Printf("compat: no quarantine older than %d days (%d entr(ies) total)\n",
			flakySoftDeadlineDays, len(file.Flaky))
		return nil
	}
	fmt.Fprintf(os.Stderr, "compat: %d quarantined test(s) older than %d days:\n",
		len(overdue), flakySoftDeadlineDays)
	for _, line := range overdue {
		fmt.Fprintln(os.Stderr, "  "+line)
		fmt.Printf("::warning title=Compat quarantine overdue::%s\n", escapeAnnotationData(line))
	}
	fmt.Fprintf(os.Stderr,
		"compat: quarantine is a stop-gap — these block pull requests once they pass %d days\n",
		flakyHardDeadlineDays)
	return fmt.Errorf("%d overdue quarantine entr(ies)", len(overdue))
}

func readFlakyFileRaw(path string) (*flakyFile, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return &flakyFile{Version: 1}, nil
		}
		return nil, fmt.Errorf("read flaky list %s: %w", path, err)
	}
	var file flakyFile
	if err := json.Unmarshal(b, &file); err != nil {
		return nil, fmt.Errorf("parse flaky list %s: %w", path, err)
	}
	return &file, nil
}

func readFlakyFile(path string) (flakySet, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return flakySet{}, nil
		}
		return nil, fmt.Errorf("read flaky list %s: %w", path, err)
	}
	var file flakyFile
	if err := json.Unmarshal(b, &file); err != nil {
		return nil, fmt.Errorf("parse flaky list %s: %w", path, err)
	}
	set := make(flakySet, len(file.Flaky))
	for _, e := range file.Flaky {
		set[e.Suite+"/"+e.Group+"/"+e.Test] = true
	}
	return set, nil
}

func compareBaseline(baseline *compatBaseline, report *compat.RunReport) []string {
	return compareBaselineWith(baseline, report, flakySet{}, candidateSet{})
}

// compareBaselineWith reports regressions, ignoring tests quarantined as flaky
// and tests belonging to a candidate generated group.
//
// A quarantined test is exempt in both directions: it may fail where the
// baseline says pass, and it is not promoted when it passes. Anything else
// makes the gate intermittent, and a gate that reds the build at random is one
// people learn to re-run rather than read.
//
// A candidate is exempt in both directions too, but the two are distinct
// concepts and are kept apart deliberately: a flaky test escaped this gate with
// a reviewer's approval and a tracking issue, while a candidate generated group
// has not entered it yet and gates nothing until a soak promotes it. Both arms
// below have to honour the exemption — the new-failure arm especially, since a
// freshly generated candidate is by definition absent from the baseline.
func compareBaselineWith(baseline *compatBaseline, report *compat.RunReport, flaky flakySet, candidates candidateSet) []string {
	current := baselineEntriesFromReport(report)
	currentByKey := baselineEntryMap(current.Entries)
	baselineByKey := baselineEntryMap(baseline.Entries)
	var regressions []string
	for _, expected := range baseline.Entries {
		key := baselineKey(expected)
		if flaky[key] || candidates.exempt(expected.Group) {
			continue
		}
		actual, ok := currentByKey[key]
		if !ok {
			regressions = append(regressions, fmt.Sprintf("compat baseline missing result: %s expected %s", key, expected.Status))
			continue
		}
		if statusRank(actual.Status) < statusRank(expected.Status) {
			regressions = append(regressions, fmt.Sprintf("compat baseline regression: %s %s -> %s", key, expected.Status, actual.Status))
		}
	}
	// A test the baseline says nothing about is new. Adding tests is always
	// welcome — but a new test that fails is a new failure, and iterating only
	// over baseline entries above would let it land on a green check. Only
	// `fail` is blocked: unimplemented/skip/na are legitimate states for a new
	// test (a known SUT gap, an environmental skip, an API the SDK lacks).
	for _, actual := range current.Entries {
		if actual.Status != compat.StatusFail {
			continue
		}
		key := baselineKey(actual)
		if flaky[key] || candidates.exempt(actual.Group) {
			continue
		}
		if _, grandfathered := baselineByKey[key]; grandfathered {
			continue
		}
		regressions = append(regressions, fmt.Sprintf("compat baseline new failure, not in baseline: %s", key))
	}
	sort.Strings(regressions)
	return regressions
}

func updateBaseline(baseline *compatBaseline, report *compat.RunReport) *compatBaseline {
	return updateBaselineWith(baseline, report, flakySet{}, candidateSet{})
}

// updateBaselineWith ratchets the baseline forward, holding quarantined tests
// at their recorded floor. Promoting a flaky test on the run where it happened
// to pass would make its next intermittent failure a red build; a real fix is
// promoted by deleting its entry from compat/flaky.json.
//
// Candidate generated groups are not recorded at all. Writing one into the
// baseline would put it under the gate on the strength of a single run, which
// is precisely the soak that promoting it to "gated" is supposed to require —
// and it would smuggle the candidate past the exemption in
// compareBaselineWith, since that gate reads the group state, not the file.
func updateBaselineWith(baseline *compatBaseline, report *compat.RunReport, flaky flakySet, candidates candidateSet) *compatBaseline {
	current := baselineEntriesFromReport(report)
	merged := baselineEntryMap(baseline.Entries)
	for _, entry := range current.Entries {
		key := baselineKey(entry)
		if candidates.exempt(entry.Group) {
			continue
		}
		old, ok := merged[key]
		if ok && flaky[key] {
			continue
		}
		if !ok || statusRank(entry.Status) > statusRank(old.Status) {
			merged[key] = entry
		}
	}
	return baselineFromMap(merged)
}

// lintBaselineChange rejects a baseline edit that makes the recorded
// expectations weaker: a downgraded status, a removed expectation the registry
// still asks for, or a freshly added `fail`. It returns the issues that fail
// the lint and, separately, informational notes naming removals that are
// legitimate.
//
// A removal is legitimate exactly when the registry — hand-written groups plus
// the generated sibling — no longer asks that suite to run that test: the group
// is gone, the test is gone from the group, or the group's `suites` no longer
// names the suite. Such a row has nothing to compare against on the new side
// because nothing measures it any more, so holding the baseline to it would
// make re-seeding every shard the only way to change what CI measures. This
// does not weaken the gate: a row for a test the suite is still expected to run
// is still a removed expectation, which is the whole anti-laundering property.
//
// One downgrade is legitimate too, and is returned in debtNotes rather than
// issues: `skip` -> `unimplemented` for a (suite, group) whose parity debt this
// change closes — closedDebt, built from the old parity-debt file listing it
// and the new one not. That skip was "not yet implemented in <suite> test
// suite": the suite never ran the test. Closing the debt makes it run, and an
// operation the emulator does not implement answers 501, which is adding an
// `unimplemented` test — and adding tests never blocks. Nothing else is
// excused: a skip that was not debt (an environmental `requires: docker`)
// cannot be downgraded, debt still listed afterwards is not closed, no other
// status pair qualifies, and nothing may become `fail`.
func lintBaselineChange(oldBaseline, newBaseline *compatBaseline, scope *registryScope, closedDebt closedParityDebt) (issues, notes, debtNotes []string) {
	newByKey := baselineEntryMap(newBaseline.Entries)
	oldByKey := baselineEntryMap(oldBaseline.Entries)
	for _, oldEntry := range oldBaseline.Entries {
		newEntry, ok := newByKey[baselineKey(oldEntry)]
		if !ok {
			if !scope.expects(oldEntry.Suite, oldEntry.Group, oldEntry.Test) {
				notes = append(notes, fmt.Sprintf("compat baseline: %s dropped — no longer in scope for %s",
					baselineKey(oldEntry), oldEntry.Suite))
				continue
			}
			issues = append(issues, fmt.Sprintf("compat baseline removed expectation: %s was %s", baselineKey(oldEntry), oldEntry.Status))
			continue
		}
		if statusRank(newEntry.Status) < statusRank(oldEntry.Status) {
			if oldEntry.Status == compat.StatusSkip && newEntry.Status == compat.StatusUnimplemented &&
				closedDebt.closes(oldEntry.Suite, oldEntry.Group) {
				debtNotes = append(debtNotes, fmt.Sprintf("compat baseline: %s skip -> unimplemented — parity debt closed for %s/%s",
					baselineKey(oldEntry), oldEntry.Suite, oldEntry.Group))
				continue
			}
			issues = append(issues, fmt.Sprintf("compat baseline downgrade: %s %s -> %s", baselineKey(oldEntry), oldEntry.Status, newEntry.Status))
		}
	}
	// Iterating only the old baseline above would let a contributor grandfather
	// a fresh failure simply by adding an entry for it. New expectations are
	// fine at any other status — the burn-down shrinks the fail set, never
	// grows it.
	//
	// Seeding is the exception: an empty old baseline means there is nothing to
	// get worse than, and the first population necessarily records reality
	// including its known failures. This cannot be abused to launder failures
	// later — emptying a populated baseline trips the removed-expectation check
	// above for every dropped entry the registry still asks for, which is every
	// entry that measures anything.
	if len(oldBaseline.Entries) == 0 {
		sort.Strings(issues)
		sort.Strings(notes)
		sort.Strings(debtNotes)
		return issues, notes, debtNotes
	}
	for _, newEntry := range newBaseline.Entries {
		if newEntry.Status != compat.StatusFail {
			continue
		}
		if _, existed := oldByKey[baselineKey(newEntry)]; existed {
			continue
		}
		issues = append(issues, fmt.Sprintf("compat baseline new fail expectation: %s may not be added as fail", baselineKey(newEntry)))
	}
	sort.Strings(issues)
	sort.Strings(notes)
	sort.Strings(debtNotes)
	return issues, notes, debtNotes
}

// outOfScopeAnnotationLimit is how many dropped keys the summary annotation
// names before deferring to the step log. A scope change drops a whole group
// across several suites at once — 105 rows in #1737 — and GitHub keeps only
// ten notice annotations per step anyway, so one annotation that says how many
// and shows a sample beats a hundred that are silently truncated.
const outOfScopeAnnotationLimit = 10

// outOfScopeAnnotation renders the legitimate removals as a single ::notice.
// They are not failures, so they must not compete with the ::error annotations
// a reviewer is meant to read first.
func outOfScopeAnnotation(notes []string) string {
	return baselineNoticeAnnotation(notes, "dropped because the registry no longer asks those suites to run them")
}

// closedDebtAnnotation renders the skip -> unimplemented changes let through
// because the change closed their parity debt as a single ::notice, sampled
// the same way: a flipped group closes debt in several suites at once.
func closedDebtAnnotation(notes []string) string {
	return baselineNoticeAnnotation(notes, "moved skip -> unimplemented because this change closes their parity debt")
}

// baselineNoticeAnnotation renders informational lint notes as one ::notice
// headed "<n> baseline expectation(s) <what>:", naming at most
// outOfScopeAnnotationLimit of them.
func baselineNoticeAnnotation(notes []string, what string) string {
	shown := notes
	if len(shown) > outOfScopeAnnotationLimit {
		shown = shown[:outOfScopeAnnotationLimit]
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d baseline expectation(s) %s:\n", len(notes), what)
	for _, note := range shown {
		b.WriteString(strings.TrimPrefix(note, "compat baseline: "))
		b.WriteString("\n")
	}
	if len(notes) > len(shown) {
		fmt.Fprintf(&b, "and %d more — the full list is in the step log\n", len(notes)-len(shown))
	}
	return "::notice title=Compat baseline::" + escapeAnnotationData(strings.TrimRight(b.String(), "\n")) + "\n"
}

// baselineAnnotations renders regression messages as GitHub workflow commands
// so each one becomes an annotation on the PR's checks tab, not just a line in
// a log nobody opens. Newlines and the other reserved characters are
// percent-encoded per the workflow-command format; an unescaped newline would
// silently truncate the annotation.
//
// https://docs.github.com/actions/reference/workflow-commands-for-github-actions
func baselineAnnotations(regressions []string) string {
	return errorAnnotations("Compat baseline", regressions)
}

// errorAnnotations renders messages under a title naming the gate that produced
// them, so a reader on the checks tab can tell a baseline regression from an
// outright failure without opening the log.
func errorAnnotations(title string, messages []string) string {
	var b strings.Builder
	for _, message := range messages {
		b.WriteString("::error title=")
		b.WriteString(title)
		b.WriteString("::")
		b.WriteString(escapeAnnotationData(message))
		b.WriteString("\n")
	}
	return b.String()
}

// escapeAnnotationData percent-encodes the characters GitHub treats as
// structural inside a workflow command's message.
func escapeAnnotationData(s string) string {
	s = strings.ReplaceAll(s, "%", "%25")
	s = strings.ReplaceAll(s, "\r", "%0D")
	s = strings.ReplaceAll(s, "\n", "%0A")
	return s
}

func baselineEntriesFromReport(report *compat.RunReport) *compatBaseline {
	var entries []baselineEntry
	for _, suite := range report.Suites {
		if suite == nil {
			continue
		}
		for _, group := range suite.Groups {
			if group == nil {
				continue
			}
			for _, test := range group.Tests {
				entries = append(entries, baselineEntry{
					Suite:   suite.Suite,
					Service: test.Service,
					Group:   test.Group,
					Test:    test.Test,
					Op:      test.Op,
					Status:  test.Status,
				})
			}
		}
	}
	return &compatBaseline{Version: baselineVersion, Entries: sortBaselineEntries(entries)}
}

// Baseline reading and writing live in baseline_shards.go: the file this
// package reads is really a directory of per-suite shards.

func baselineEntryMap(entries []baselineEntry) map[string]baselineEntry {
	out := make(map[string]baselineEntry, len(entries))
	for _, entry := range entries {
		out[baselineKey(entry)] = entry
	}
	return out
}

func baselineFromMap(entries map[string]baselineEntry) *compatBaseline {
	out := make([]baselineEntry, 0, len(entries))
	for _, entry := range entries {
		out = append(out, entry)
	}
	return &compatBaseline{Version: baselineVersion, Entries: sortBaselineEntries(out)}
}

func sortBaselineEntries(entries []baselineEntry) []baselineEntry {
	sort.SliceStable(entries, func(i, j int) bool {
		return baselineKey(entries[i]) < baselineKey(entries[j])
	})
	return entries
}

func baselineKey(entry baselineEntry) string {
	return entry.Suite + "/" + entry.Group + "/" + entry.Test
}

func statusRank(status compat.Status) int {
	switch status {
	case compat.StatusPass:
		return 4
	case compat.StatusSkip, compat.StatusNA:
		return 3
	case compat.StatusUnimplemented:
		return 2
	case compat.StatusFail:
		return 1
	default:
		return 0
	}
}
