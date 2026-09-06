// cmd/compat/parity.go — cross-suite uniformity enforcement.
//
// compat/suites/registry.json is the single source of truth for what every
// SDK/CLI suite should test. Nothing used to check that suites actually
// implement it: a suite could quietly emit "not yet implemented" forever, and
// a new registry test could land implemented in one suite only.
//
// The parity checker closes that. It classifies every (suite, registry test)
// pair from a real run's results — the same data the dashboard shows — and
// compares the gaps against compat/parity-debt.json. Debt must be recorded,
// must match reality exactly, and only ever shrinks.
//
// See compat/AGENTS.md § Baseline & uniformity policy.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/overcast-sh/overcast/compat"
)

const parityDebtVersion = 1

// ---------------------------------------------------------------------------
// registry.json (the subset parity cares about)
// ---------------------------------------------------------------------------

type parityRegistry struct {
	Groups []parityGroup `json:"groups"`
}

type parityGroup struct {
	Service string `json:"service"`
	Name    string `json:"name"`
	// Suites restricts the group to specific suites. Empty = every suite.
	Suites []string `json:"suites"`
	// Generated, State and Parallel are set only on groups from
	// registry.generated.json. They are parsed from the hand-written registry
	// too so lintGeneratedRegistry can reject a hand-written group that carries
	// them — the shared schema has to permit the properties for the generated
	// schema to extend its TestGroup by $ref. See generatedregistry.go.
	Generated bool   `json:"generated"`
	State     string `json:"state"`
	Parallel  bool   `json:"parallel"`
	// Scenario is the IR file that resolves the group's tests. A generated
	// group always carries it; a hand-written group carries it once it has
	// been ported (#1903), which is how it says the scenario backend resolves
	// its tests and no suite implements them by hand any more.
	Scenario string `json:"scenario"`
	// ShadowOf is the same: only a generated group carries it, and it names the
	// hand-written group the authored scenario behind it is being compared
	// against before the native implementations are deleted.
	ShadowOf string       `json:"shadowOf"`
	Tests    []parityTest `json:"tests"`
}

type parityTest struct {
	Name string `json:"name"`
}

// nonUniformSuites are exempt from registry-wide uniformity. CDK drives
// CloudFormation deploys of whole stacks rather than calling operations one at a
// time, so "implement every registry operation" is meaningless for it; it runs
// only the groups a registry entry scopes to it explicitly. Every other suite
// is an SDK or the CLI, where uniformity is the whole point.
var nonUniformSuites = map[string]bool{"cdk": true}

// expects reports whether a suite is required to implement this group.
func (g parityGroup) expects(suite string) bool {
	if len(g.Suites) > 0 {
		for _, s := range g.Suites {
			if s == suite {
				return true
			}
		}
		return false
	}
	// Unscoped groups are expected of every uniform (SDK/CLI) suite.
	return !nonUniformSuites[suite]
}

// registryScope answers, for one (suite, group, test) cell, whether the
// registry still asks that suite to run that test. It is the concatenated
// registry — hand-written plus generated — indexed for lookup.
//
// The baseline change lint is its only caller, and the question it answers
// there is what separates a removed expectation that launders a result from
// one that is simply no longer measured: a group deleted from the registry, a
// test dropped from a group, or a group scoped away from the suite.
type registryScope struct {
	groups map[string]parityGroup
	tests  map[string]map[string]bool
}

func newRegistryScope(reg *parityRegistry) *registryScope {
	scope := &registryScope{
		groups: make(map[string]parityGroup, len(reg.Groups)),
		tests:  make(map[string]map[string]bool, len(reg.Groups)),
	}
	// Two groups sharing a name is a load error upstream — lintGeneratedRegistry
	// refuses it, and that refusal is what makes concatenation safe at all.
	// Unioning their tests rather than letting the later one win is the
	// fail-closed reading if one ever slipped through: a removal is then judged
	// against more tests, not fewer.
	for _, group := range reg.Groups {
		scope.groups[group.Name] = group
		tests := scope.tests[group.Name]
		if tests == nil {
			tests = make(map[string]bool, len(group.Tests))
			scope.tests[group.Name] = tests
		}
		for _, test := range group.Tests {
			tests[test.Name] = true
		}
	}
	return scope
}

// expects reports whether the registry requires suite to run group/test.
//
// A nil scope — no registry was available to the caller — answers yes to
// everything. That is the fail-closed direction: without a registry to consult,
// the lint keeps judging every removal as it did before it could ask.
func (s *registryScope) expects(suite, group, test string) bool {
	if s == nil {
		return true
	}
	g, ok := s.groups[group]
	if !ok {
		return false
	}
	if !s.tests[group][test] {
		return false
	}
	return g.expects(suite)
}

// ---------------------------------------------------------------------------
// parity-debt.json
// ---------------------------------------------------------------------------

type parityDebtFile struct {
	Version int               `json:"version"`
	Comment string            `json:"comment,omitempty"`
	Debt    []parityDebtEntry `json:"debt"`
}

type parityDebtEntry struct {
	Suite   string `json:"suite"`
	Service string `json:"service"`
	Group   string `json:"group"`
	Tests   int    `json:"tests"`
}

func (e parityDebtEntry) key() string { return e.Suite + "/" + e.Group }

// ---------------------------------------------------------------------------
// Classification
// ---------------------------------------------------------------------------

// notImplementedSentinel is the skip reason every suite loader emits for a
// registry group it has not implemented. The wording is shared deliberately —
// it is how parity debt is told apart from an environmental skip — so all eight
// loaders must keep producing exactly this.
func notImplementedSentinel(suite string) string {
	return fmt.Sprintf("not yet implemented in %s test suite", suite)
}

// isNotImplementedSkip matches the shared sentinel for any suite. The bare
// "not implemented" form is the legacy wording java-sdk and cli emitted before
// they were normalised; it is still accepted so the checker can classify
// results produced by an older binary (a CI artifact from before the change, or
// a suite image that has not been rebuilt).
func isNotImplementedSkip(reason string) bool {
	if reason == "not implemented" || reason == "not implemented in cli suite" {
		return true
	}
	return strings.HasPrefix(reason, "not yet implemented in ") &&
		strings.HasSuffix(reason, " test suite")
}

// isEnvironmentalSkip matches skips caused by the environment rather than by a
// missing implementation — the test exists and would have run elsewhere.
func isEnvironmentalSkip(reason string) bool {
	return strings.HasPrefix(reason, "requires:") ||
		strings.Contains(reason, "Docker") ||
		strings.Contains(reason, "docker")
}

// isCascadeSkip matches skips caused by an earlier failure in the same group.
// Not parity debt, but worth counting: cascades mask real results and should
// disappear when their root cause is fixed.
func isCascadeSkip(reason string) bool {
	return strings.HasPrefix(reason, "setup failed") ||
		strings.HasPrefix(reason, "dependency failed")
}

// parityResult is the outcome of a parity computation.
type parityResult struct {
	// Debt is one entry per (suite, group) with unimplemented registry tests.
	Debt []parityDebtEntry
	// Missing counts registry tests a suite did not report at all.
	Missing int
	// Cascades counts skips caused by an earlier failure.
	Cascades int
	// Unclassified lists skips whose reason matches no known category.
	Unclassified []string
	// Unregistered lists results whose (group, test) the registry does not
	// contain. The registry is the source of truth in both directions: a test
	// invented by one suite never joins the cross-suite matrix, is invisible
	// to the uniformity policy, and erodes exactly the guarantee the registry
	// exists to give — cdk shipped two such tests before this was checked.
	Unregistered []string
	// Implemented counts (suite, test) pairs the suite actually exercises.
	Implemented int
	// Expected counts (suite, test) pairs the registry requires.
	Expected int
}

// computeParity classifies every registry test for every suite against a run's
// results.
func computeParity(reg *parityRegistry, report *compat.RunReport, suites []string) *parityResult {
	// Index results by suite/group/test.
	type resultKey struct{ suite, group, test string }
	results := make(map[resultKey]compat.TestResultEvent)
	for _, sr := range report.Suites {
		if sr == nil {
			continue
		}
		for _, gr := range sr.Groups {
			if gr == nil {
				continue
			}
			for _, tr := range gr.Tests {
				results[resultKey{sr.Suite, gr.Name, tr.Test}] = tr
			}
		}
	}

	out := &parityResult{}
	debtByKey := make(map[string]*parityDebtEntry)

	for _, suite := range suites {
		for _, group := range reg.Groups {
			if !group.expects(suite) {
				continue
			}
			for _, test := range group.Tests {
				out.Expected++
				result, found := results[resultKey{suite, group.Name, test.Name}]
				gap := false
				switch {
				case !found:
					// The suite never reported this cell at all.
					out.Missing++
					gap = true
				case result.Status == compat.StatusSkip && isNotImplementedSkip(result.Error):
					gap = true
				case result.Status == compat.StatusSkip && isCascadeSkip(result.Error):
					out.Cascades++
				case result.Status == compat.StatusSkip && isEnvironmentalSkip(result.Error):
					// Implemented; the environment could not run it.
				case result.Status == compat.StatusSkip:
					out.Unclassified = append(out.Unclassified, fmt.Sprintf(
						"unclassified skip reason: %s/%s/%s: %q",
						suite, group.Name, test.Name, result.Error))
				}
				if gap {
					key := suite + "/" + group.Name
					entry := debtByKey[key]
					if entry == nil {
						entry = &parityDebtEntry{Suite: suite, Service: group.Service, Group: group.Name}
						debtByKey[key] = entry
					}
					entry.Tests++
					continue
				}
				out.Implemented++
			}
		}
	}

	for _, entry := range debtByKey {
		out.Debt = append(out.Debt, *entry)
	}

	// The reverse direction: every result must be a registry test. Walk the
	// asked-about suites' results and flag any (group, test) the registry does
	// not contain — respecting group scoping, so a cdk-only group does not make
	// cdk's results look foreign.
	registered := make(map[string]map[string]bool, len(reg.Groups))
	for _, group := range reg.Groups {
		tests := make(map[string]bool, len(group.Tests))
		for _, test := range group.Tests {
			tests[test.Name] = true
		}
		registered[group.Name] = tests
	}
	asked := make(map[string]bool, len(suites))
	for _, s := range suites {
		asked[s] = true
	}
	for key := range results {
		if !asked[key.suite] {
			continue
		}
		if !registered[key.group][key.test] {
			out.Unregistered = append(out.Unregistered, fmt.Sprintf(
				"result not in registry: %s/%s/%s — add it to compat/suites/registry.json first, so every suite implements it (or records debt), or rename it to the registry entry it corresponds to",
				key.suite, key.group, key.test))
		}
	}

	sortParityDebt(out.Debt)
	sort.Strings(out.Unclassified)
	sort.Strings(out.Unregistered)
	return out
}

// diffParityDebt compares recorded debt against reality. Any difference is an
// error: unrecorded debt means a PR added gaps without declaring them, stale or
// mismatched entries mean the file has rotted and can no longer prove progress.
func diffParityDebt(recorded *parityDebtFile, actual []parityDebtEntry) []string {
	recordedByKey := make(map[string]parityDebtEntry, len(recorded.Debt))
	for _, entry := range recorded.Debt {
		recordedByKey[entry.key()] = entry
	}
	actualByKey := make(map[string]parityDebtEntry, len(actual))
	for _, entry := range actual {
		actualByKey[entry.key()] = entry
	}

	var issues []string
	for _, entry := range actual {
		rec, ok := recordedByKey[entry.key()]
		if !ok {
			issues = append(issues, fmt.Sprintf(
				"unrecorded parity debt: %s (%d test(s)) — implement the group in this suite, mark the tests na, or record the debt in compat/parity-debt.json",
				entry.key(), entry.Tests))
			continue
		}
		switch {
		case entry.Tests > rec.Tests:
			issues = append(issues, fmt.Sprintf(
				"parity debt grew: %s %d -> %d test(s) — new registry tests must be implemented in every suite",
				entry.key(), rec.Tests, entry.Tests))
		case entry.Tests < rec.Tests:
			issues = append(issues, fmt.Sprintf(
				"parity debt shrank: %s %d -> %d test(s) — regenerate compat/parity-debt.json so the win is recorded",
				entry.key(), rec.Tests, entry.Tests))
		}
	}
	for _, entry := range recorded.Debt {
		if _, ok := actualByKey[entry.key()]; !ok {
			issues = append(issues, fmt.Sprintf(
				"stale parity debt: %s is fully implemented — remove it from compat/parity-debt.json",
				entry.key()))
		}
	}
	sort.Strings(issues)
	return issues
}

func sortParityDebt(entries []parityDebtEntry) {
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].key() < entries[j].key() })
}

// ---------------------------------------------------------------------------
// File IO and CLI entry points
// ---------------------------------------------------------------------------

func readParityRegistry(path string) (*parityRegistry, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read registry %s: %w", path, err)
	}
	var reg parityRegistry
	if err := json.Unmarshal(b, &reg); err != nil {
		return nil, fmt.Errorf("parse registry %s: %w", path, err)
	}
	return &reg, nil
}

// readParityRegistries loads the hand-written registry and concatenates the
// generated sibling onto it, refusing the load if the two collide.
//
// Concatenation is all the parity checker needs: generated groups carry an
// explicit `suites` list, so parityGroup.expects already scopes them correctly
// and a suite without a backend for a generated group neither implements it nor
// carries debt for it. That is deliberate — a model refresh that adds a
// thousand tests must not add a thousand entries of debt to six suites that
// were never asked to run them.
//
// A *ported* hand-written group — one carrying `scenario`, resolved by an
// authored scenario rather than by per-language implementations — is executed
// by the same backends and so needs the same scoping. It cannot carry `suites`
// itself, because that list is derived from backend availability rather than
// decided by a human, so it is applied here from the generated sibling's
// `ported` index (applyPortedSuites). Downstream nothing knows the difference:
// expects() sees one `suites` list whatever produced it.
func readParityRegistries(registryPath, generatedPath string) (*parityRegistry, error) {
	reg, err := readParityRegistry(registryPath)
	if err != nil {
		return nil, err
	}
	gen, err := readGeneratedRegistry(generatedPath)
	if err != nil {
		return nil, err
	}
	if issues := lintGeneratedRegistry(reg, gen); len(issues) > 0 {
		return nil, generatedRegistryIssueError(generatedPath, issues)
	}
	applyPortedSuites(reg, gen)
	reg.Groups = append(reg.Groups, gen.parityGroups()...)
	return reg, nil
}

func readParityDebtFile(path string) (*parityDebtFile, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &parityDebtFile{Version: parityDebtVersion}, nil
		}
		return nil, fmt.Errorf("read parity debt %s: %w", path, err)
	}
	var file parityDebtFile
	if err := json.Unmarshal(b, &file); err != nil {
		return nil, fmt.Errorf("parse parity debt %s: %w", path, err)
	}
	return &file, nil
}

const parityDebtComment = "Cross-suite parity debt: registry groups a suite has not implemented yet. " +
	"Generated by `go run ./cmd/compat --update-parity-debt`. This file only ever shrinks — " +
	"see compat/AGENTS.md § Baseline & uniformity policy."

func writeParityDebtFile(path string, entries []parityDebtEntry) error {
	sortParityDebt(entries)
	if entries == nil {
		entries = []parityDebtEntry{}
	}
	file := parityDebtFile{Version: parityDebtVersion, Comment: parityDebtComment, Debt: entries}
	b, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal parity debt: %w", err)
	}
	b = append(b, '\n')
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return fmt.Errorf("write parity debt: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp) //nolint:errcheck
		return fmt.Errorf("write parity debt: %w", err)
	}
	return nil
}

// paritySuites returns the suites present in a run. Which groups each is held
// to is decided by parityGroup.expects — a cdk-only group is enforced for cdk
// alone, and cdk is exempt from unscoped groups.
func paritySuites(report *compat.RunReport) []string {
	seen := make(map[string]bool)
	var out []string
	for _, sr := range report.Suites {
		if sr == nil || seen[sr.Suite] {
			continue
		}
		seen[sr.Suite] = true
		out = append(out, sr.Suite)
	}
	sort.Strings(out)
	return out
}

// checkParityFiles is the --check-parity entry point.
func checkParityFiles(registryPath, generatedPath, debtPath, resultsPath string, annotateOut bool) error {
	reg, err := readParityRegistries(registryPath, generatedPath)
	if err != nil {
		return err
	}
	report, err := readRunReportFile(resultsPath)
	if err != nil {
		return err
	}
	recorded, err := readParityDebtFile(debtPath)
	if err != nil {
		return err
	}

	suites := paritySuites(report)
	if len(suites) == 0 {
		return fmt.Errorf("no suites in %s — parity cannot be checked", resultsPath)
	}
	result := computeParity(reg, report, suites)
	issues := diffParityDebt(recorded, result.Debt)
	issues = append(issues, result.Unclassified...)
	issues = append(issues, result.Unregistered...)
	sort.Strings(issues)

	printParitySummary(result, suites)

	if len(issues) > 0 {
		for _, issue := range issues {
			fmt.Fprintln(os.Stderr, issue)
		}
		if annotateOut {
			fmt.Print(parityAnnotations(issues))
		}
		return fmt.Errorf("%d cross-suite parity issue(s)", len(issues))
	}
	fmt.Printf("compat: parity check passed (%d/%d registry test(s) implemented across %d suite(s))\n",
		result.Implemented, result.Expected, len(suites))
	return nil
}

// updateParityDebtFile is the --update-parity-debt entry point.
func updateParityDebtFile(registryPath, generatedPath, debtPath, resultsPath string) error {
	reg, err := readParityRegistries(registryPath, generatedPath)
	if err != nil {
		return err
	}
	report, err := readRunReportFile(resultsPath)
	if err != nil {
		return err
	}
	suites := paritySuites(report)
	if len(suites) == 0 {
		return fmt.Errorf("no suites in %s — refusing to rewrite parity debt from an empty run", resultsPath)
	}
	result := computeParity(reg, report, suites)
	if err := writeParityDebtFile(debtPath, result.Debt); err != nil {
		return err
	}
	total := 0
	for _, entry := range result.Debt {
		total += entry.Tests
	}
	fmt.Printf("compat: parity debt written to %s (%d group(s), %d test(s))\n", debtPath, len(result.Debt), total)
	return nil
}

func printParitySummary(result *parityResult, suites []string) {
	total := 0
	perSuite := make(map[string]int)
	for _, entry := range result.Debt {
		total += entry.Tests
		perSuite[entry.Suite] += entry.Tests
	}
	fmt.Printf("compat: parity — %d/%d registry test(s) implemented across %d suite(s); %d test(s) of debt in %d group(s)\n",
		result.Implemented, result.Expected, len(suites), total, len(result.Debt))
	for _, suite := range suites {
		if perSuite[suite] > 0 {
			fmt.Printf("  %-14s %d test(s) of debt\n", suite, perSuite[suite])
		}
	}
	if result.Missing > 0 {
		fmt.Printf("  %d registry test(s) were not reported by any suite that should run them\n", result.Missing)
	}
	if result.Cascades > 0 {
		fmt.Printf("  %d skip(s) cascaded from an earlier failure (not parity debt; fix the root cause)\n", result.Cascades)
	}
}

func parityAnnotations(issues []string) string {
	var b strings.Builder
	for _, issue := range issues {
		b.WriteString("::error title=Compat parity::")
		b.WriteString(escapeAnnotationData(issue))
		b.WriteString("\n")
	}
	return b.String()
}
