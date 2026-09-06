// cmd/compat/generatedregistry.go — the generated half of the test registry.
//
// compat/suites/registry.json is hand-written and reviewable; its sibling
// compat/suites/registry.generated.json is machine output that cmd/compatgen
// rewrites wholly. Every loader concatenates the two. Splitting them keeps a
// five-thousand-entry diff out of the file humans edit, lets the generator
// rebuild its own file without merge conflicts, and makes "generated vs
// hand-written" an explicit fact rather than something inferred from a naming
// convention.
//
// Two invariants make the split safe, and both are enforced here:
//
//   - The join key is shared. baseline.json, flaky.json and parity-debt.json
//     all key on suite/group/test and are indifferent to which file a group
//     came from, so a generated name that collides with a hand-written one
//     silently merges two different tests into one gate entry.
//     lintGeneratedRegistry rejects that.
//
//   - Generated groups start out unable to break anything. A group in state
//     "candidate" runs everywhere and reports everywhere but gates nothing:
//     it is excluded from --compare-baseline and --max-failures in both
//     directions until a soak promotes it to "gated".
//
// See docs/plans/compat-coverage-modelgen.md § 3.6.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
)

const generatedRegistryVersion = 1

// Generated group lifecycle states.
//
// This is the inverse of compat/flaky.json, and the two must not be confused.
// A flaky test escaped a gate it was already under, with a reviewer's explicit
// approval and a tracking issue. A candidate has not entered the gate yet: it
// was produced mechanically from the AWS model, nobody has watched it run, and
// gating on it before the soak would mean a model-refresh PR could red every
// build until someone quarantined the new tests — which would make flaky.json
// the dumping ground the quarantine lint exists to prevent.
const (
	generatedStateCandidate = "candidate"
	generatedStateGated     = "gated"
)

// ---------------------------------------------------------------------------
// registry.generated.json
// ---------------------------------------------------------------------------

type generatedRegistry struct {
	Version int    `json:"version"`
	Comment string `json:"comment,omitempty"`
	// Ported indexes the hand-written groups an authored scenario now
	// resolves. See portedGroup.
	Ported []portedGroup    `json:"ported,omitempty"`
	Groups []generatedGroup `json:"groups"`
}

// portedGroup is one hand-written registry group whose tests are resolved by
// an authored IR scenario instead of by per-language implementations — step 3
// of docs/plans/compat-coverage-modelgen.md §3.11.
//
// A ported group is *not* a generated group and does not appear in `groups`:
// its entry stays in the hand-written compat/suites/registry.json, which is
// where the names baseline.json, flaky.json and parity-debt.json key on have
// always lived and where they must stay for the port to be invisible to them.
// The group there says only *that* it is ported, by carrying `scenario`.
//
// Everything a human cannot know — which suites can actually execute it — is
// derived, and this index is where cmd/compatgen writes that derivation down.
// Why an index rather than a `suites` list on the hand-written group: `suites`
// on a ported group is mechanically derived from backend availability exactly
// as it is on a generated one, and widens on its own as backends land. A
// hand-written copy would be a human maintaining generator output, stale the
// first time an emitter refused a member; the alternative — the generator
// editing registry.json to keep it fresh — would put a machine in charge of
// the file review assumes a human owns. So the derived fact lives here, beside
// every other mechanically-derived scoping decision, and registry.json stays
// hand-written.
type portedGroup struct {
	// Group is the hand-written group this indexes. It names a group in
	// compat/suites/registry.json, never one in `groups` above.
	Group string `json:"group"`
	// Scenario is the authored IR file that resolves the group, and is the
	// same path the hand-written group carries. The two are written from one
	// run and cross-checked below, so a half-applied edit cannot survive.
	Scenario string `json:"scenario"`
	// Suites lists the backends that can execute the group. A suite absent
	// from it is out of scope, not indebted — the same rule a generated
	// group's `suites` states.
	Suites []string `json:"suites"`
}

// generatedGroup models only what the gate path reads. The full group shape —
// `slow`, and the per-test `op`/`depends`/`requires`/`skip` — is the shared
// TestGroup's, defined once in registry.schema.json and consumed by the suite
// loaders; unknown fields simply pass through here.
type generatedGroup struct {
	Service string `json:"service"`
	Name    string `json:"name"`
	// Generated is required and always true. It is redundant with the file the
	// group was read from, and deliberately so: the dashboard, the report and
	// the lint all see groups after concatenation, by which point the file of
	// origin is gone.
	Generated bool `json:"generated"`
	// Scenario is the IR file this group was generated from, so a failing test
	// leads back to the recipe rather than to a dead end.
	Scenario string `json:"scenario,omitempty"`
	// State is generatedStateCandidate or generatedStateGated.
	State string `json:"state"`
	// ShadowOf names the hand-written group this one shadows, on a group
	// produced from an authored scenario that is being compared against the
	// native implementations it will replace (§3.11 step 2). It is what
	// --compare-shadow joins the two halves on, and why --promote-generated
	// leaves the group alone: a shadow is collecting evidence of agreement
	// with another group, not with itself, and it is deleted when that
	// evidence is in.
	ShadowOf string `json:"shadowOf,omitempty"`
	// Suites lists the backends that can execute the group. Always present and
	// mechanically derived: a suite absent from it is out of scope, not
	// indebted.
	Suites []string        `json:"suites"`
	Tests  []generatedTest `json:"tests"`
}

// generatedTest carries only the join key. The full test shape (op, depends,
// requires, skip) lives in the JSON and is read by the suite loaders; nothing
// on the gate path needs more than the name.
//
// An alias rather than a second struct: the two registries are concatenated and
// matched on exactly these names, so a copy that could drift from parityTest
// would be a copy of the one field that must never differ.
type generatedTest = parityTest

// readGeneratedRegistry loads the generated sibling, treating a missing file as
// an empty registry.
//
// Tolerating absence is not laziness: suite images, CI artifacts and checkouts
// that predate the file all have to keep working, and "the file is not there"
// must produce the same verdict as "the file is there and empty" — that
// equivalence is phase G0's whole acceptance gate.
func readGeneratedRegistry(path string) (*generatedRegistry, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return &generatedRegistry{Version: generatedRegistryVersion}, nil
		}
		return nil, fmt.Errorf("read generated registry %s: %w", path, err)
	}
	var reg generatedRegistry
	if err := json.Unmarshal(b, &reg); err != nil {
		return nil, fmt.Errorf("parse generated registry %s: %w", path, err)
	}
	return &reg, nil
}

// parityGroups projects generated groups onto the shape the parity checker
// already understands, so scoping, debt and the unregistered-result check all
// work with zero new concepts.
func (r *generatedRegistry) parityGroups() []parityGroup {
	out := make([]parityGroup, 0, len(r.Groups))
	for _, g := range r.Groups {
		out = append(out, parityGroup{
			Service:   g.Service,
			Name:      g.Name,
			Suites:    g.Suites,
			Generated: g.Generated,
			State:     g.State,
			Scenario:  g.Scenario,
			ShadowOf:  g.ShadowOf,
			Tests:     g.Tests,
		})
	}
	return out
}

// ---------------------------------------------------------------------------
// The candidate exemption
// ---------------------------------------------------------------------------

// candidateSet is the set of group names in state "candidate".
//
// It mirrors flakySet, but keys on the group rather than on suite/group/test:
// candidacy is a property of the generated group as a whole, and applies to
// every suite that runs it. Keep the two sets distinct — merging them would
// make an untried generated test indistinguishable from a reviewer-approved
// quarantine, and the flaky lint's "this list only shrinks" promise depends on
// nothing else being hidden in it.
type candidateSet map[string]bool

// candidateGroups returns the groups that gate nothing yet.
func (r *generatedRegistry) candidateGroups() candidateSet {
	set := make(candidateSet)
	for _, g := range r.Groups {
		if g.State == generatedStateCandidate {
			set[g.Name] = true
		}
	}
	return set
}

// exempt reports whether a result belongs to a candidate group, and so is
// excluded from the baseline gate and the failure gate in both directions.
func (c candidateSet) exempt(group string) bool { return c[group] }

// loadCandidateGroups reads the candidate set from --generated-registry-file.
// Mirrors readFlakyFile(*flakyFilePath): the gate entry points each load their
// own exemptions rather than threading a pre-built set through.
//
// It lints before returning, and that is not belt-and-braces. The exemption
// this set grants is the ability to silence the baseline gate for a group, so
// the collision check has to hold on the path that grants it: a generated
// candidate group that reused a hand-written group's name would exempt the
// hand-written group from --compare-baseline and --max-failures, hiding a real
// regression. --check-parity runs the same lint, but compat.yml runs it after
// both baseline gates, so relying on it alone means the build reds for a
// confusing reason after the wrong verdict has already been reported.
func loadCandidateGroups() (candidateSet, error) {
	gen, err := readGeneratedRegistry(*generatedRegistryFile)
	if err != nil {
		return nil, err
	}
	// An empty generated registry needs no hand-written registry to check
	// against, and must not require one: --compare-baseline is run against
	// artifacts in contexts where compat/suites/registry.json may not be at
	// the default path.
	if gen.empty() {
		return candidateSet{}, nil
	}
	hand, err := readParityRegistry(*registryFile)
	if err != nil {
		return nil, err
	}
	if issues := lintGeneratedRegistry(hand, gen); len(issues) > 0 {
		return nil, generatedRegistryIssueError(*generatedRegistryFile, issues)
	}
	return gen.candidateGroups(), nil
}

// ---------------------------------------------------------------------------
// Collision and shape lint
// ---------------------------------------------------------------------------

// lintGeneratedRegistry checks the invariants that make concatenation safe.
//
// The name checks are the load-bearing ones. Every gate file keys on
// suite/group/test with no notion of which registry a group came from, so a
// generated group that reuses a hand-written name does not conflict — it
// merges. The baseline would record one status for two different tests, the
// candidate exemption would silently disable the gate on a hand-written group,
// and parity would count the pair once. None of that surfaces as an error
// anywhere downstream, which is why it has to be caught at load time.
//
// The shape checks mirror registry.generated.schema.json so the invariants hold
// wherever the Go loader runs, including the suite images that never invoke the
// Python validator.
func lintGeneratedRegistry(hand *parityRegistry, gen *generatedRegistry) []string {
	var issues []string

	handGroups := make(map[string]bool)
	handKeys := make(map[string]bool)
	if hand != nil {
		for _, g := range hand.Groups {
			handGroups[g.Name] = true
			for _, t := range g.Tests {
				handKeys[g.Name+"/"+t.Name] = true
			}
			// The shared schema has to permit these for
			// registry.generated.schema.json to extend its TestGroup by $ref,
			// so the ban on hand-written groups carrying them is enforced here
			// instead of by the schema.
			//
			// `scenario` is deliberately not in this list (#1903). It is the
			// one field of the five that states something a human can know and
			// must decide: that this group's tests are resolved by an authored
			// IR scenario rather than by seven per-language implementations.
			// The other four are facts about generator output — which file
			// wrote the group, where it is in its soak, what it shadows, how it
			// may be scheduled — and a hand-written copy of any of them could
			// only disagree with the generator.
			if g.Generated || g.State != "" || g.Parallel || g.ShadowOf != "" {
				issues = append(issues, fmt.Sprintf(
					"hand-written group %q carries a generated-only field (generated/state/shadowOf/parallel) — those belong in compat/suites/registry.generated.json, which cmd/compatgen owns",
					g.Name))
			}
		}
	}

	seenGroups := make(map[string]bool)
	seenKeys := make(map[string]bool)
	for _, g := range gen.Groups {
		switch {
		case handGroups[g.Name]:
			issues = append(issues, fmt.Sprintf(
				"generated group %q collides with a hand-written group of the same name — the two registries are concatenated and every gate file keys on suite/group/test, so the entries would merge rather than conflict",
				g.Name))
		case seenGroups[g.Name]:
			issues = append(issues, fmt.Sprintf(
				"generated group %q is declared twice in compat/suites/registry.generated.json",
				g.Name))
		}
		seenGroups[g.Name] = true

		if !g.Generated {
			issues = append(issues, fmt.Sprintf(
				"generated group %q does not set \"generated\": true — the dashboard facet, the report and this lint all read the flag, not the file it came from",
				g.Name))
		}
		switch g.State {
		case generatedStateCandidate, generatedStateGated:
		case "":
			issues = append(issues, fmt.Sprintf(
				"generated group %q has no \"state\" — it must be %q (gates nothing yet) or %q (soaked and enforced)",
				g.Name, generatedStateCandidate, generatedStateGated))
		default:
			issues = append(issues, fmt.Sprintf(
				"generated group %q has state %q, want %q or %q",
				g.Name, g.State, generatedStateCandidate, generatedStateGated))
		}
		if len(g.Suites) == 0 {
			issues = append(issues, fmt.Sprintf(
				"generated group %q has no \"suites\" — a generated group must name the backends that can execute it, or every suite without a backend inherits parity debt for tests it was never asked to run",
				g.Name))
		}
		if len(g.Tests) == 0 {
			issues = append(issues, fmt.Sprintf(
				"generated group %q has no tests", g.Name))
		}
		issues = append(issues, shadowIssues(g, hand)...)

		for _, t := range g.Tests {
			key := g.Name + "/" + t.Name
			if handKeys[key] {
				issues = append(issues, fmt.Sprintf(
					"generated test key %q duplicates a hand-written one — baseline.json, flaky.json and parity-debt.json cannot tell the two apart",
					key))
			}
			if seenKeys[key] {
				issues = append(issues, fmt.Sprintf(
					"generated test key %q is declared twice", key))
			}
			seenKeys[key] = true
		}
	}

	issues = append(issues, portedIssues(hand, gen, seenGroups)...)

	sort.Strings(issues)
	return issues
}

// ---------------------------------------------------------------------------
// Ported groups
// ---------------------------------------------------------------------------

// empty reports whether the generated sibling carries nothing at all — no
// groups and no ported index.
//
// "Absent" and "present but empty" have to produce the same verdict
// everywhere; that equivalence is phase G0's acceptance gate, and it is why
// readGeneratedRegistry tolerates a missing file. Asking it through one method
// keeps the two halves of the file from drifting apart the way they would if
// each caller spelled out `len(gen.Groups) == 0` and forgot the other half.
func (r *generatedRegistry) empty() bool { return len(r.Groups) == 0 && len(r.Ported) == 0 }

// portedIssues checks the join between a hand-written group that carries
// `scenario` and the generated sibling's index of it, in both directions.
//
// The two files are written from one run — cmd/compatgen reads registry.json
// and rewrites registry.generated.json — so any disagreement between them is a
// half-applied edit. It matters because the index is the *only* place a ported
// group's `suites` is stated: a group with `scenario` and no entry falls back
// to "every uniform suite", which is right today by coincidence and wrong the
// first time an emitter refuses it; an entry naming a group that is not ported
// derives scoping for nothing at all.
//
// The forward direction is skipped against an empty sibling, and deliberately.
// An absent generated registry and an empty one must reach the same verdict,
// and neither can index anything, so demanding an entry there would make the
// gate depend on a file this command is documented to run without.
// cmd/compatgen owns the reverse of that case: it always has both files, and
// checkPortedGroupsHaveAuthoredScenarios refuses a `scenario` that no
// authored file backs.
func portedIssues(hand *parityRegistry, gen *generatedRegistry, generatedGroups map[string]bool) []string {
	var issues []string

	indexed := make(map[string]bool, len(gen.Ported))
	for _, p := range gen.Ported {
		if indexed[p.Group] {
			issues = append(issues, fmt.Sprintf(
				"ported group %q is indexed twice in compat/suites/registry.generated.json — two entries could scope one group two ways, and which won would depend on iteration order",
				p.Group))
			continue
		}
		indexed[p.Group] = true
		if generatedGroups[p.Group] {
			issues = append(issues, fmt.Sprintf(
				"ported group %q is also a generated group — a port replaces a hand-written group, it does not stand beside a generated one of the same name",
				p.Group))
		}
		if len(p.Suites) == 0 {
			issues = append(issues, fmt.Sprintf(
				"ported group %q has no \"suites\" — the index exists to say which backends can execute the group, and an empty list scopes it to none",
				p.Group))
		}
	}

	handByName := make(map[string]parityGroup)
	if hand != nil {
		for _, g := range hand.Groups {
			handByName[g.Name] = g
		}
	}
	for _, p := range gen.Ported {
		g, ok := handByName[p.Group]
		if !ok {
			issues = append(issues, fmt.Sprintf(
				"ported group %q is not a group in the hand-written registry — the index scopes a group compat/suites/registry.json declares, and this one scopes nothing",
				p.Group))
			continue
		}
		switch {
		case g.Scenario == "":
			issues = append(issues, fmt.Sprintf(
				"ported group %q carries no \"scenario\" in the hand-written registry — the index says an authored scenario resolves it and the group says nothing does",
				p.Group))
		case g.Scenario != p.Scenario:
			issues = append(issues, fmt.Sprintf(
				"ported group %q is indexed against scenario %q but the hand-written registry names %q — one of the two files was edited without the other",
				p.Group, p.Scenario, g.Scenario))
		}
	}

	if gen.empty() {
		return issues
	}
	for _, name := range handHavingScenario(hand) {
		if !indexed[name] {
			issues = append(issues, fmt.Sprintf(
				"hand-written group %q carries \"scenario\" but is not in the \"ported\" index of compat/suites/registry.generated.json — that index is where its `suites` comes from, so without it the group is scoped to every uniform suite whether or not they can run it; regenerate with `make generate-compat-model`",
				name))
		}
	}
	return issues
}

// handHavingScenario names the hand-written groups that declare themselves
// ported, in registry order.
func handHavingScenario(hand *parityRegistry) []string {
	if hand == nil {
		return nil
	}
	var out []string
	for _, g := range hand.Groups {
		if g.Scenario != "" {
			out = append(out, g.Name)
		}
	}
	return out
}

// applyPortedSuites scopes each ported hand-written group to the suites the
// index derived for it.
//
// This is the whole of #1903 item 2 on the reading side. A ported group is
// executed by whichever suites have a scenario backend for it, exactly as a
// generated group is, so parityGroup.expects has to answer for it exactly as
// it answers for a generated group — otherwise a suite with no backend is
// counted as owing tests it was never asked to run, which is the debt
// inflation `suites` was introduced to prevent.
//
// Assigning into Suites rather than teaching expects a second source keeps
// there being one answer to "which suites run this group", whatever produced
// it. An explicit `suites` on the hand-written group is not merged with the
// derived list: the two would be two answers, and the hand-written one is
// reserved for cdk-lifecycle, which carries no scenario.
func applyPortedSuites(reg *parityRegistry, gen *generatedRegistry) {
	if len(gen.Ported) == 0 {
		return
	}
	byName := make(map[string][]string, len(gen.Ported))
	for _, p := range gen.Ported {
		byName[p.Group] = p.Suites
	}
	for i := range reg.Groups {
		if suites, ok := byName[reg.Groups[i].Name]; ok {
			reg.Groups[i].Suites = suites
		}
	}
}

// generatedRegistryIssueError renders lint issues as a single error, one per
// line, so a --check-parity run names every collision rather than the first.
func generatedRegistryIssueError(path string, issues []string) error {
	return fmt.Errorf("%d problem(s) in %s:\n  %s", len(issues), path, strings.Join(issues, "\n  "))
}

// ---------------------------------------------------------------------------
// Shadow groups
// ---------------------------------------------------------------------------

// shadowIssues holds a shadow group to the two things --compare-shadow needs
// of it, and neither is decorative.
//
// The comparison joins shadow to native on the test name, per suite. A shadow
// naming a group that does not exist compares against nothing and reports a
// clean run; one whose test names have drifted compares eight of nine and says
// nothing about the ninth. Both read as "the port agrees with the natives",
// which is the one conclusion the soak exists to earn rather than assume — and
// the flip that follows deletes working code on the strength of it.
//
// A shadow must also stay a candidate. Gating a group that is scheduled for
// deletion would put the deletion behind a baseline update; more to the point,
// the promotion soak asks whether a group agrees with itself, which is not the
// question a shadow is being asked.
func shadowIssues(g generatedGroup, hand *parityRegistry) []string {
	if g.ShadowOf == "" {
		return nil
	}
	var issues []string
	if g.State != generatedStateCandidate {
		issues = append(issues, fmt.Sprintf(
			"generated group %q shadows %q but is in state %q — a shadow gates nothing and is deleted when the port lands, so it stays %q",
			g.Name, g.ShadowOf, g.State, generatedStateCandidate))
	}
	if hand == nil {
		return issues
	}
	var native *parityGroup
	for i := range hand.Groups {
		if hand.Groups[i].Name == g.ShadowOf {
			native = &hand.Groups[i]
			break
		}
	}
	if native == nil {
		return append(issues, fmt.Sprintf(
			"generated group %q shadows %q, which is not a group in the hand-written registry — --compare-shadow would join it against nothing and report agreement",
			g.Name, g.ShadowOf))
	}
	shadowTests := make(map[string]bool, len(g.Tests))
	for _, t := range g.Tests {
		shadowTests[t.Name] = true
	}
	nativeTests := make(map[string]bool, len(native.Tests))
	for _, t := range native.Tests {
		nativeTests[t.Name] = true
	}
	var missing, extra []string
	for name := range nativeTests {
		if !shadowTests[name] {
			missing = append(missing, name)
		}
	}
	for name := range shadowTests {
		if !nativeTests[name] {
			extra = append(extra, name)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)
	if len(missing) > 0 {
		issues = append(issues, fmt.Sprintf(
			"generated group %q shadows %q but does not declare %s — the comparison joins on the test name, so a test the shadow is missing is a test nobody proved the port reproduces",
			g.Name, g.ShadowOf, strings.Join(missing, ", ")))
	}
	if len(extra) > 0 {
		issues = append(issues, fmt.Sprintf(
			"generated group %q shadows %q and declares %s, which %q does not — a shadow reproduces the native group's tests, it does not add to them",
			g.Name, g.ShadowOf, strings.Join(extra, ", "), g.ShadowOf))
	}
	return issues
}
