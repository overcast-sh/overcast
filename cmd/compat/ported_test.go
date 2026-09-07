package main

import (
	"strings"
	"testing"
)

// A ported group is a hand-written registry group whose tests are resolved by
// an authored IR scenario rather than by seven per-language implementations —
// step 3 of docs/plans/compat-coverage-modelgen.md §3.11, and what #1903's two
// prerequisites make possible.
//
// Two facts have to hold for the flip to be safe, and both are asserted here:
// `scenario` is legal on a hand-written group (and the other generated-only
// fields still are not), and the group's `suites` is derived from the
// generated sibling's ported index rather than written by hand — so the parity
// checker asks of a ported group exactly what it asks of a generated one.

// portedHandRegistry is testRegistry() with s3-crud ported: it carries the
// authored scenario that resolves its tests, and no per-suite implementation
// backs them any more.
func portedHandRegistry() *parityRegistry {
	reg := testRegistry()
	reg.Groups[0].Scenario = "compat/model/authored/s3-crud.json"
	return reg
}

// portedIndexFixture is a generated sibling with content, indexing s3-crud as
// ported and scoped to the suites named.
func portedIndexFixture(suites ...string) *generatedRegistry {
	gen := generatedFixture()
	gen.Ported = []portedGroup{{
		Group:    "s3-crud",
		Scenario: "compat/model/authored/s3-crud.json",
		Suites:   suites,
	}}
	return gen
}

// TestLintAcceptsAHandWrittenGroupCarryingScenario is the rule #1903 item 1
// adopts. `scenario` stops being generated-only: it is how a hand-written
// group says its tests are resolved by the scenario backend, which is the
// whole of the flip.
func TestLintAcceptsAHandWrittenGroupCarryingScenario(t *testing.T) {
	// Given: a hand-written group ported to an authored scenario, and the
	// generated sibling's index of it.
	// When: the two registries are linted together.
	issues := lintGeneratedRegistry(portedHandRegistry(), portedIndexFixture("go-sdk", "python-sdk"))

	// Then: nothing is flagged.
	if len(issues) != 0 {
		t.Fatalf("issues = %#v, want none — `scenario` is legal on a hand-written group", issues)
	}
}

// TestLintStillRejectsTheOtherGeneratedOnlyFields pins the half of the rule
// that does not move. `generated`, `state`, `shadowOf` and `parallel` are
// still cmd/compatgen's alone: each is a fact about generator output, and a
// hand-written copy of one could only disagree with it.
func TestLintStillRejectsTheOtherGeneratedOnlyFields(t *testing.T) {
	for _, tc := range []struct {
		field string
		set   func(*parityGroup)
	}{
		{"generated", func(g *parityGroup) { g.Generated = true }},
		{"state", func(g *parityGroup) { g.State = generatedStateCandidate }},
		{"shadowOf", func(g *parityGroup) { g.ShadowOf = "s3-other" }},
		{"parallel", func(g *parityGroup) { g.Parallel = true }},
	} {
		t.Run(tc.field, func(t *testing.T) {
			// Given: a hand-written group carrying one generated-only field.
			hand := testRegistry()
			tc.set(&hand.Groups[0])

			// When: the two registries are linted together.
			issues := lintGeneratedRegistry(hand, &generatedRegistry{Version: generatedRegistryVersion})

			// Then: it is refused, naming the group and the rule.
			if len(issues) != 1 || !strings.Contains(issues[0], "s3-crud") {
				t.Fatalf("issues = %#v, want one naming s3-crud", issues)
			}
			if !strings.Contains(issues[0], "generated-only field") {
				t.Errorf("issue = %q, want it to say the field is generated-only", issues[0])
			}
		})
	}
}

// TestLintRejectsAPortedGroupWithNoIndexEntry is the first half of the join.
// The ported index is where a ported group's `suites` comes from; without one
// the group falls back to "every uniform suite", which is right today only by
// coincidence and wrong the moment a suite has no backend for it.
func TestLintRejectsAPortedGroupWithNoIndexEntry(t *testing.T) {
	// Given: a hand-written group carrying `scenario`, and a generated sibling
	// that has content but does not index it.
	// When: the two are linted together.
	issues := lintGeneratedRegistry(portedHandRegistry(), generatedFixture())

	// Then: refused, naming the group and the index that must carry it.
	if len(issues) != 1 {
		t.Fatalf("issues = %#v, want one", issues)
	}
	if !strings.Contains(issues[0], "s3-crud") || !strings.Contains(issues[0], `"ported"`) {
		t.Errorf("issue = %q, want it to name s3-crud and the ported index", issues[0])
	}
}

// TestLintRejectsAnIndexEntryThatDoesNotJoin is the other half. An entry
// naming a group that does not exist, or one that is not ported, or one whose
// scenario path disagrees, derives `suites` for nothing — and means the
// generator and the registry a human owns have drifted apart.
func TestLintRejectsAnIndexEntryThatDoesNotJoin(t *testing.T) {
	for _, tc := range []struct {
		name  string
		hand  *parityRegistry
		entry portedGroup
		want  string
	}{
		{
			name:  "group does not exist",
			hand:  testRegistry(),
			entry: portedGroup{Group: "s3-absent", Scenario: "compat/model/authored/s3-absent.json", Suites: []string{"go-sdk"}},
			want:  "s3-absent",
		},
		{
			name:  "group carries no scenario",
			hand:  testRegistry(),
			entry: portedGroup{Group: "s3-crud", Scenario: "compat/model/authored/s3-crud.json", Suites: []string{"go-sdk"}},
			want:  "s3-crud",
		},
		{
			name:  "scenario paths disagree",
			hand:  portedHandRegistry(),
			entry: portedGroup{Group: "s3-crud", Scenario: "compat/model/authored/other.json", Suites: []string{"go-sdk"}},
			want:  "other.json",
		},
		{
			name:  "no suites",
			hand:  portedHandRegistry(),
			entry: portedGroup{Group: "s3-crud", Scenario: "compat/model/authored/s3-crud.json"},
			want:  "suites",
		},
		{
			name:  "collides with a generated group",
			hand:  portedHandRegistry(),
			entry: portedGroup{Group: "sqs-generated-gated", Scenario: "compat/model/authored/sqs-generated-gated.json", Suites: []string{"go-sdk"}},
			want:  "sqs-generated-gated",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Given: a generated sibling whose ported index does not join.
			gen := generatedFixture()
			gen.Ported = []portedGroup{tc.entry}

			// When: the two registries are linted together.
			issues := lintGeneratedRegistry(tc.hand, gen)

			// Then: refused, naming what failed to join.
			if len(issues) == 0 {
				t.Fatalf("issues = none, want a refusal")
			}
			if !strings.Contains(strings.Join(issues, "\n"), tc.want) {
				t.Errorf("issues = %#v, want one naming %q", issues, tc.want)
			}
		})
	}
}

// TestLintRejectsADuplicateIndexEntry — two entries for one group could scope
// it two ways, and which one won would depend on iteration order.
func TestLintRejectsADuplicateIndexEntry(t *testing.T) {
	// Given: the same group indexed twice.
	gen := portedIndexFixture("go-sdk")
	gen.Ported = append(gen.Ported, gen.Ported[0])

	// When/Then: refused.
	issues := lintGeneratedRegistry(portedHandRegistry(), gen)
	if len(issues) != 1 || !strings.Contains(issues[0], "twice") {
		t.Fatalf("issues = %#v, want one about a duplicate entry", issues)
	}
}

// TestPortedGroupDerivesItsSuites is #1903 item 2. A ported group is executed
// by whichever suites have a scenario backend, exactly as a generated group
// is, so the parity checker must ask the same question of it.
//
// The derivation happens on load, from the generated sibling's index:
// compat/suites/registry.json stays hand-written and says only *that* the
// group is ported, never *where* it runs.
func TestPortedGroupDerivesItsSuites(t *testing.T) {
	// Given: files on disk — a ported hand-written group, and a sibling
	// scoping it to go-sdk alone.
	handPath := writeTempJSON(t, "registry.json", portedHandRegistry())
	genPath := writeTempJSON(t, "registry.generated.json", portedIndexFixture("go-sdk"))

	// When: they are loaded together.
	reg, err := readParityRegistries(handPath, genPath)
	if err != nil {
		t.Fatalf("readParityRegistries: %v", err)
	}

	// Then: the group is scoped to the backend suites and to no others.
	var group *parityGroup
	for i := range reg.Groups {
		if reg.Groups[i].Name == "s3-crud" {
			group = &reg.Groups[i]
		}
	}
	if group == nil {
		t.Fatal("s3-crud is missing from the concatenated registry")
	}
	if !equalStrings(group.Suites, []string{"go-sdk"}) {
		t.Fatalf("suites = %#v, want the ported index's", group.Suites)
	}
	if !group.expects("go-sdk") {
		t.Error("go-sdk has the backend and must be expected to run the group")
	}
	if group.expects("python-sdk") {
		t.Error("python-sdk has no backend for the group: out of scope, not indebted")
	}
}

// TestPortedGroupIsHeldToParityLikeAGeneratedOne is the no-debt half of the
// same rule, through the checker rather than through the group's fields: a
// suite the index names owes the group exactly as it owes a generated one, and
// a suite it does not name owes nothing.
func TestPortedGroupIsHeldToParityLikeAGeneratedOne(t *testing.T) {
	// Given: a run where the in-scope suite skips the ported group with the
	// shared sentinel, and the out-of-scope suite reports nothing at all.
	report := reportWithResults()
	addSkip(report, "go-sdk", "s3", "s3-crud", "CreateBucket", notImplementedSentinel("go-sdk"))
	addSkip(report, "go-sdk", "s3", "s3-crud", "DeleteBucket", notImplementedSentinel("go-sdk"))

	reg, err := readParityRegistries(
		writeTempJSON(t, "registry.json", portedHandRegistry()),
		writeTempJSON(t, "registry.generated.json", &generatedRegistry{
			Version: generatedRegistryVersion,
			Ported: []portedGroup{{
				Group:    "s3-crud",
				Scenario: "compat/model/authored/s3-crud.json",
				Suites:   []string{"go-sdk"},
			}},
		}))
	if err != nil {
		t.Fatalf("readParityRegistries: %v", err)
	}

	// When: parity is computed for both suites.
	got := computeParity(reg, report, []string{"go-sdk", "python-sdk"})

	// Then: go-sdk carries the debt; python-sdk carries none.
	if len(got.Debt) != 1 || got.Debt[0].Suite != "go-sdk" || got.Debt[0].Tests != 2 {
		t.Fatalf("debt = %#v, want go-sdk/s3-crud with 2 test(s)", got.Debt)
	}
	if got.Missing != 0 {
		t.Errorf("missing = %d, want 0 — python-sdk was never asked for the group", got.Missing)
	}
}

// TestPortedIndexIsNotCheckedAgainstAnEmptySibling keeps phase G0's
// equivalence intact: an absent generated registry and an empty one produce
// the same verdict, and neither can index anything. cmd/compatgen owns the
// reverse direction, where both files are always in hand.
func TestPortedIndexIsNotCheckedAgainstAnEmptySibling(t *testing.T) {
	// Given: a ported hand-written group and an empty generated sibling.
	// When: the two are linted together.
	issues := lintGeneratedRegistry(portedHandRegistry(), &generatedRegistry{Version: generatedRegistryVersion})

	// Then: nothing is flagged.
	if len(issues) != 0 {
		t.Fatalf("issues = %#v, want none", issues)
	}
}

// TestCheckedInRegistryPortsSqsQueues is the corpus half of #1903 after the
// flip: sqs-queues resolves through its authored scenario, the shadow it
// soaked as is gone, and the ported index carries the derived suites the
// parity checker scopes it by. The three move together — a hand-written group
// carrying `scenario` with no index entry, or an index entry with no such
// group, is what lintGeneratedRegistry refuses.
func TestCheckedInRegistryPortsSqsQueues(t *testing.T) {
	hand, err := readParityRegistry(repoPath(t, "compat", "suites", "registry.json"))
	if err != nil {
		t.Fatalf("readParityRegistry: %v", err)
	}
	var scenario string
	for _, g := range hand.Groups {
		if g.Name == "sqs-queues" {
			scenario = g.Scenario
		}
	}
	if want := "compat/model/authored/sqs-queues.json"; scenario != want {
		t.Errorf("sqs-queues scenario = %q, want %q", scenario, want)
	}
	gen, err := readGeneratedRegistry(repoPath(t, "compat", "suites", "registry.generated.json"))
	if err != nil {
		t.Fatalf("readGeneratedRegistry: %v", err)
	}
	for _, g := range gen.Groups {
		if g.Name == "sqs-queues-shadow" {
			t.Error("sqs-queues-shadow is still declared — the flip deletes the shadow with the natives")
		}
	}
	var ported *portedGroup
	for i := range gen.Ported {
		if gen.Ported[i].Group == "sqs-queues" {
			ported = &gen.Ported[i]
		}
	}
	if ported == nil {
		t.Fatalf("ported = %#v, want an entry for sqs-queues", gen.Ported)
	}
	if len(ported.Suites) != 7 {
		t.Errorf("sqs-queues suites = %#v, want all seven backends", ported.Suites)
	}
}
