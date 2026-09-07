//go:build dev

package main

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// A port is finished when the authored file's group loses its `-shadow` suffix
// and the hand-written registry group gains a `scenario` — one change, in one
// PR (docs/plans/compat-coverage-modelgen.md §3.11 step 3, #1903). These cases
// pin what the generator does with a group in that state, and what it refuses
// when only one of the two halves has been applied.

// portedHandRegistry is authoredHandRegistry() with the group ported.
func portedHandRegistry() *handRegistry {
	hand := authoredHandRegistry()
	hand.Groups[0].Scenario = authoredPath("widgets-hand")
	return hand
}

// livePortedFixture is the authored fixture with its group flipped live — the
// name the flip PR renames it to.
func livePortedFixture() map[string]any {
	doc := authoredFixture()
	doc["groups"].([]any)[0].(map[string]any)["name"] = "widgets-hand"
	return doc
}

func TestCheckAuthoredAgainstRegistry_acceptsALivePortPairedWithItsScenario(t *testing.T) {
	// Given: the authored group is live and the registry group names it.
	a := loadAuthoredFixture(t, "widgets-hand", livePortedFixture())

	// When/Then: accepted. This is the flipped state, and the only state in
	// which a hand-written group may carry `scenario` at all.
	if err := checkAuthoredAgainstRegistry(a, portedHandRegistry()); err != nil {
		t.Fatalf("the flipped pair must be accepted: %v", err)
	}
}

func TestCheckAuthoredAgainstRegistry_refusesALivePortWithNoScenarioOnTheGroup(t *testing.T) {
	// Given: the authored group is live but the registry entry still says
	// nothing about it.
	a := loadAuthoredFixture(t, "widgets-hand", livePortedFixture())

	// When/Then: refused. cli and python-sdk resolve from `scenario` and would
	// find nothing; the four typed suites resolve by group name and would run
	// the port — so the suites would silently disagree about what they ran.
	err := checkAuthoredAgainstRegistry(a, authoredHandRegistry())
	if err == nil || !strings.Contains(err.Error(), "must carry \"scenario\"") {
		t.Fatalf("err = %v, want a refusal naming the missing field", err)
	}
}

func TestCheckAuthoredAgainstRegistry_refusesAScenarioOnAGroupStillShadowed(t *testing.T) {
	// Given: the registry entry claims the port is done while the authored
	// group is still soaking as a shadow.
	a := loadAuthoredFixture(t, "widgets-hand", authoredFixture())

	// When/Then: refused. The suites would be told to resolve `widgets-hand`
	// through a file that declares `widgets-hand-shadow`, and every loader
	// would fall through to "not yet implemented" — with the natives possibly
	// already deleted.
	err := checkAuthoredAgainstRegistry(a, portedHandRegistry())
	if err == nil || !strings.Contains(err.Error(), "still a shadow") {
		t.Fatalf("err = %v, want a refusal naming the shadow", err)
	}
}

func TestCheckAuthoredAgainstRegistry_refusesAScenarioPointingAtAnotherFile(t *testing.T) {
	// Given: the registry group names a different authored file.
	hand := portedHandRegistry()
	hand.Groups[0].Scenario = authoredPath("widgets-other")
	a := loadAuthoredFixture(t, "widgets-hand", livePortedFixture())

	// When/Then: refused — an authored scenario is named for the group it
	// ports, so the two spellings are one file or a mistake.
	err := checkAuthoredAgainstRegistry(a, hand)
	if err == nil || !strings.Contains(err.Error(), "widgets-other") {
		t.Fatalf("err = %v", err)
	}
}

func TestCheckPortedGroupsHaveAuthoredScenarios_refusesAScenarioNoFileBacks(t *testing.T) {
	// Given: a registry group claiming to be ported, and no authored scenario
	// for it at all — a typo, or a file deleted without its entry.
	// When/Then: refused. checkAuthoredAgainstRegistry walks authored files
	// and would never visit this group, so the sweep is the only thing that
	// sees it.
	err := checkPortedGroupsHaveAuthoredScenarios(portedHandRegistry(), nil)
	if err == nil || !strings.Contains(err.Error(), "widgets-hand") {
		t.Fatalf("err = %v, want a refusal naming the group", err)
	}
}

func TestCheckPortedGroupsHaveAuthoredScenarios_ignoresAShadowedGroup(t *testing.T) {
	// Given: a group being ported under a shadow name, its registry entry
	// untouched.
	a := loadAuthoredFixture(t, "widgets-hand", authoredFixture())

	// When/Then: nothing to check — the group is not ported yet.
	if err := checkPortedGroupsHaveAuthoredScenarios(authoredHandRegistry(), []authored{a}); err != nil {
		t.Fatalf("err = %v, want none", err)
	}
}

func TestCheckPortedGroupsHaveAuthoredScenarios_acceptsALivePort(t *testing.T) {
	// Given: the flipped pair.
	a := loadAuthoredFixture(t, "widgets-hand", livePortedFixture())

	// When/Then: accepted.
	if err := checkPortedGroupsHaveAuthoredScenarios(portedHandRegistry(), []authored{a}); err != nil {
		t.Fatalf("err = %v, want none", err)
	}
}

// TestBuildRegistryIndexesAPortedGroupRatherThanDeclaringIt is #1903 item 2 on
// the writing side.
//
// A ported group's entry stays in the hand-written registry — that is where
// the names compat/baseline/, compat/flaky.json and compat/parity-debt.json key
// on live, and moving them is what a port must never do. Writing a *group*
// here would not move them either: it would collide with the hand-written
// entry, which the two registries being concatenated makes a merge rather than
// a clash. So only the derived scoping is written, into `ported`.
func TestBuildRegistryIndexesAPortedGroupRatherThanDeclaringIt(t *testing.T) {
	// Given: a live authored generation.
	a := loadAuthoredFixture(t, "widgets-hand", livePortedFixture())
	f := loadFixture(t)
	gen, err := generateAuthored(a, f.model, fixtureClient)
	if err != nil {
		t.Fatalf("generateAuthored: %v", err)
	}

	// When: the registry is built over two backends.
	reg := buildRegistry([]*generation{gen}, []string{"cli", "python-sdk"}, nil, nil)

	// Then: no group is declared for it, and the ported index carries the
	// group, its scenario and the suites derived from backend availability.
	for _, g := range reg.Groups {
		if g.Name == "widgets-hand" {
			t.Fatalf("groups declare %q — a ported group's entry is the hand-written one", g.Name)
		}
	}
	if len(reg.Ported) != 1 {
		t.Fatalf("ported = %#v, want one entry", reg.Ported)
	}
	got := reg.Ported[0]
	if got.Group != "widgets-hand" {
		t.Errorf("group = %q, want widgets-hand", got.Group)
	}
	if got.Scenario != authoredPath("widgets-hand") {
		t.Errorf("scenario = %q, want the authored file", got.Scenario)
	}
	if !equalStrings(got.Suites, []string{"cli", "python-sdk"}) {
		t.Errorf("suites = %#v, want both backends", got.Suites)
	}
}

// TestBuildRegistryScopesAPortedGroupAwayFromAnUnableEmitter pins that a
// ported group's `suites` is derived from exactly what a generated group's is:
// the backend table minus the emitters that refused the group. Nothing about
// the derivation changes because a human wrote the scenario.
func TestBuildRegistryScopesAPortedGroupAwayFromAnUnableEmitter(t *testing.T) {
	// Given: a live authored generation the go-sdk emitter cannot express.
	a := loadAuthoredFixture(t, "widgets-hand", livePortedFixture())
	f := loadFixture(t)
	gen, err := generateAuthored(a, f.model, fixtureClient)
	if err != nil {
		t.Fatalf("generateAuthored: %v", err)
	}

	// When: the registry is built with that refusal recorded.
	reg := buildRegistry([]*generation{gen}, []string{"cli", goSDKSuite},
		nil, unableSuites{"widgets-hand": {goSDKSuite: true}})

	// Then: the index scopes it to the suite that can run it, and to no other.
	if len(reg.Ported) != 1 {
		t.Fatalf("ported = %#v, want one entry", reg.Ported)
	}
	if !equalStrings(reg.Ported[0].Suites, []string{"cli"}) {
		t.Errorf("suites = %#v, want cli alone", reg.Ported[0].Suites)
	}
}

// TestBuildRegistryStillDeclaresAShadowAsAGroup is the control: while the port
// is soaking there is no hand-written entry to collide with, so a shadow is an
// ordinary generated group and stays out of the ported index.
func TestBuildRegistryStillDeclaresAShadowAsAGroup(t *testing.T) {
	// Given: the authored fixture under its shadow name.
	a := loadAuthoredFixture(t, "widgets-hand", authoredFixture())
	f := loadFixture(t)
	gen, err := generateAuthored(a, f.model, fixtureClient)
	if err != nil {
		t.Fatalf("generateAuthored: %v", err)
	}

	// When: the registry is built.
	reg := buildRegistry([]*generation{gen}, []string{"cli"}, nil, nil)

	// Then: it is a group, shadowing the native, and nothing is ported.
	if len(reg.Groups) != 1 || reg.Groups[0].Name != "widgets-hand-shadow" {
		t.Fatalf("groups = %#v, want the shadow group", reg.Groups)
	}
	if reg.Groups[0].ShadowOf != "widgets-hand" {
		t.Errorf("shadowOf = %q, want widgets-hand", reg.Groups[0].ShadowOf)
	}
	if len(reg.Ported) != 0 {
		t.Errorf("ported = %#v, want none while the port soaks", reg.Ported)
	}
}

// TestCommittedRegistryPortsSqsQueues is the corpus half of #1903 after the
// flip: the hand-written sqs-queues entry names the authored scenario that
// replaced its seven native implementations, and it is the only ported group
// so far. Every consequence of the field — the group leaving the generated
// groups list, the ported index, the emitted source — follows from the pair
// of names, and the generator refuses either half without the other.
func TestCommittedRegistryPortsSqsQueues(t *testing.T) {
	hand, err := loadHandRegistry(filepath.Join(repoRoot, filepath.FromSlash(handRegistryPath)))
	if err != nil {
		t.Fatalf("loadHandRegistry: %v", err)
	}
	ported := map[string]string{}
	for _, g := range hand.Groups {
		if g.Scenario != "" {
			ported[g.Name] = g.Scenario
		}
	}
	want := map[string]string{"sqs-queues": "compat/model/authored/sqs-queues.json"}
	if !reflect.DeepEqual(ported, want) {
		t.Errorf("ported hand-written groups = %#v, want %#v", ported, want)
	}
}
