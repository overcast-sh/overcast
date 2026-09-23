//go:build dev

package main

// Authored scenarios — the middle layer of docs/plans/compat-coverage-modelgen.md
// §3.11.
//
// A recipe says what a service's shapes cannot, and the generator turns it into
// the IR. Some behavioural intent no recipe can reach — send a message and
// receive it, publish to a topic subscribed to a queue, FIFO ordering — and the
// plan's answer is not to guess it from the model and not to write it eight
// times in eight languages, but to write it once **in the same IR**, by hand.
// That is an authored scenario: an input with no recipe, held to exactly the
// contract a generated scenario is held to, and fed through exactly the same
// emitters.
//
// The one thing it needs that a generated scenario does not is a join to the
// registry. An authored scenario exists to replace a hand-written group, so its
// group name and its test names *are* that group's — they are the join keys for
// compat/baseline/, compat/flaky.json, compat/parity-debt.json and the
// dashboard's history, and the migration is worthless if it orphans them.
// checkAuthoredAgainstRegistry is what makes that a fact rather than a hope.
//
// While the port is soaking, the authored copy runs beside the native one under
// a **shadow** name — `<group>-shadow` — because both are live at once and a
// suite may not register two implementations for one `group:test` key. The
// shadow group carries `shadowOf` into the generated registry, which is what
// `cmd/compat --compare-shadow` joins on and what keeps it out of the promotion
// soak. See compat/model/README.md § Authored scenarios.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const (
	authoredDir = "compat/model/authored"
	// shadowSuffix marks a group that runs beside the native implementation it
	// is replacing rather than in place of it. It is a suffix on the group
	// name because that name is what a suite registers an impl key under, what
	// a loader looks up in the scenario file, and what every gate file keys
	// on: one string, and the flip is deleting it.
	shadowSuffix = "-shadow"
	// authoredUnitPrefix distinguishes an authored generation's emitted source
	// from the service's generated source. sqs has both, and a reviewer has to
	// be able to tell the diff of a port from the diff of a model refresh.
	authoredUnitPrefix = "authored-"
)

// authoredPath is where an authored scenario lives, repository-relative with
// '/' separators — the form the registry records and an interpreter opens.
//
// It is deliberately *not* under compat/model/scenarios/: everything in that
// directory is rewritten wholly on every run and must never be edited, and an
// input filed among the outputs is the one mistake this layout exists to make
// impossible.
func authoredPath(name string) string { return authoredDir + "/" + name + ".json" }

// authoredUnit is the emitted-source key for an authored scenario file.
func authoredUnit(name string) string { return authoredUnitPrefix + name }

// describe names a generation the way a reader of the emitted source needs to
// see it: which layer it came from, and which unit. A recipe's is
// "generated <service>"; an authored scenario's is "authored <group>", because
// "generated authored-sqs-queues" answers one question twice.
func (gen *generation) describe() string {
	if unit, ok := strings.CutPrefix(gen.unit, authoredUnitPrefix); ok {
		return "authored " + unit
	}
	return "generated " + gen.unit
}

// isAuthored reports whether this generation came from an authored scenario
// rather than from a recipe.
func (gen *generation) isAuthored() bool {
	return strings.HasPrefix(gen.unit, authoredUnitPrefix)
}

// authored is one authored scenario file as read: the parsed IR, the file it
// came from and the name it was filed under.
type authored struct {
	// name is the file's base name, which is the registry group it ports.
	name     string
	file     string
	scenario *scenario
}

// nativeGroupOf reports the hand-written registry group a scenario group
// stands for, and whether it is running as a shadow of it.
func nativeGroupOf(groupName string) (native string, shadow bool) {
	if rest, ok := strings.CutSuffix(groupName, shadowSuffix); ok {
		return rest, true
	}
	return groupName, false
}

// loadAuthored reads every authored scenario under dir, validating each
// against scenario.schema.json and the IR's own structural rules. Model-
// dependent checks need the model and happen in generateAuthored.
func loadAuthored(dir string, schemas *schemaSet) ([]authored, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read authored scenarios %s: %w", dir, err)
	}
	var out []authored
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		name := strings.TrimSuffix(entry.Name(), ".json")
		path := filepath.Join(dir, entry.Name())
		contents, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read authored scenario %s: %w", path, err)
		}
		if err := schemas.validate(schemaScenario, contents); err != nil {
			return nil, fmt.Errorf("authored scenario %s %w", authoredPath(name), err)
		}
		var s scenario
		if err := decodeStrict(contents, &s); err != nil {
			return nil, fmt.Errorf("parse authored scenario %s: %w", authoredPath(name), err)
		}
		if err := validateScenario(&s); err != nil {
			return nil, fmt.Errorf("authored scenario %s: %w", authoredPath(name), err)
		}
		out = append(out, authored{name: name, file: authoredPath(name), scenario: &s})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out, nil
}

// generateAuthored turns one authored scenario into the generation the
// emitters and the registry builder consume. There is nothing to generate —
// the IR is the input — so this is the model-dependent half of validation plus
// the plumbing that makes an authored scenario indistinguishable downstream
// from a generated one.
func generateAuthored(a authored, model *serviceModel, client clientInfo) (*generation, error) {
	s := a.scenario
	if s.Client != client {
		want, _ := json.Marshal(client)
		got, _ := json.Marshal(s.Client)
		return nil, fmt.Errorf("%s: client block disagrees with the model:\n  want %s\n  got  %s",
			a.file, want, got)
	}
	for _, g := range s.Groups {
		for i, c := range g.Setup {
			if err := checkAuthoredCall(model, c); err != nil {
				return nil, fmt.Errorf("%s: group %s: setup[%d]: %w", a.file, g.Name, i, err)
			}
		}
		for i, c := range g.Teardown {
			if err := checkAuthoredCall(model, c); err != nil {
				return nil, fmt.Errorf("%s: group %s: teardown[%d]: %w", a.file, g.Name, i, err)
			}
		}
		for _, t := range g.Tests {
			if t.Op != t.Call.Op {
				return nil, fmt.Errorf("%s: group %s: test %s declares op %s but calls %s; they are one operation",
					a.file, g.Name, t.Name, t.Op, t.Call.Op)
			}
			if err := checkAuthoredCall(model, t.Call); err != nil {
				return nil, fmt.Errorf("%s: group %s: test %s: call: %w", a.file, g.Name, t.Name, err)
			}
			for i, clause := range t.Assert {
				if err := checkAuthoredAssertion(model, clause); err != nil {
					return nil, fmt.Errorf("%s: group %s: test %s: assert[%d]: %w", a.file, g.Name, t.Name, i, err)
				}
			}
		}
	}
	for _, g := range s.Groups {
		if err := checkAuthoredValues(model, g); err != nil {
			return nil, fmt.Errorf("%s: group %s: %w", a.file, g.Name, err)
		}
	}
	return &generation{
		scenario: s,
		unit:     authoredUnit(a.name),
		file:     a.file,
		covered:  make(map[string][]string),
		caps:     capabilitiesFor(s.Service),
		model:    model,
	}, nil
}

// checkAuthoredCall holds an authored call to the model: the operation has to
// be one the service declares, and every member one that operation takes.
// Wrong here is an error and not a refusal — a refusal is the generator
// declining to write something, and nobody wrote this but a human.
func checkAuthoredCall(model *serviceModel, c call) error {
	if !model.HasOperation(c.Op) {
		return fmt.Errorf("%s is not a modeled operation of this service", c.Op)
	}
	input := model.InputShape(c.Op)
	known := make(map[string]bool)
	for _, member := range model.Members(input) {
		known[member] = true
	}
	for _, member := range sortedKeys(c.Params) {
		if !known[member] {
			return fmt.Errorf("%s has no input member %s", c.Op, member)
		}
	}
	output := model.OutputShape(c.Op)
	for _, ctx := range sortedStringKeys(c.Export) {
		if err := checkAuthoredPath(model, output, c.Export[ctx]); err != nil {
			return fmt.Errorf("export %s: %w", ctx, err)
		}
	}
	return nil
}

// checkAuthoredValues holds an authored group's values to the rules that decide
// whether every backend can send them, which is the half of checkValue a
// generated scenario gets from the binder and a hand-written one would
// otherwise not get at all.
//
// It is the blob rule (checkBlob): a blob member — at any depth, and on the
// expected side of an `equals` or a `where` that resolves to one — takes
// `$base64` and nothing else, and `$base64` goes nowhere else. Without it a
// string literal on a blob member passed this file, and the four source
// emitters were what refused it, each scoping the group away from its suite
// (#1910): the port would have run in three suites and reported no reason why.
// Here it is an error naming the member and pointing at `$base64`.
//
// Exports are tracked in the order the group runs — setup, then each test's
// call and its clauses, then teardown — so a `$base64` around a $ref is checked
// against the kind of what that $ref really names.
func checkAuthoredValues(model *serviceModel, g group) error {
	exports := exportKinds{}
	record := func(c call) {
		output := model.OutputShape(c.Op)
		for _, ctx := range sortedStringKeys(c.Export) {
			path, err := parsePath(c.Export[ctx])
			if err != nil || output == "" {
				continue
			}
			if target, err := model.ResolvePath(output, path); err == nil {
				exports[ctx] = model.Kind(target)
			}
		}
	}
	checkCall := func(c call, where string) error {
		input := model.InputShape(c.Op)
		for _, member := range sortedKeys(c.Params) {
			target, ok := model.MemberTarget(input, member)
			if !ok {
				continue // checkAuthoredCall has already named it
			}
			if err := checkAuthoredValue(model, c.Params[member], target, exports, where+" "+c.Op+"."+member); err != nil {
				return err
			}
		}
		return nil
	}
	var checkClause func(a assertion, own string, where string) error
	checkClause = func(a assertion, own string, where string) error {
		output := model.OutputShape(own)
		if a.Call != nil {
			if err := checkCall(*a.Call, where); err != nil {
				return err
			}
			output = model.OutputShape(a.Call.Op)
		}
		for _, raw := range sortedCheckPaths(a.Checks) {
			c := a.Checks[raw]
			if c.Equals == nil || output == "" {
				continue
			}
			path, err := parsePath(raw)
			if err != nil {
				return err
			}
			target, err := model.ResolvePath(output, path)
			if err != nil {
				continue // an unresolvable path is the check's own failure to report
			}
			if err := checkAuthoredValue(model, c.Equals, target, exports, where+" check "+raw); err != nil {
				return err
			}
		}
		if len(a.Where) > 0 && output != "" {
			if items, err := parsePath(a.ItemsPath); err == nil {
				if list, err := model.ResolvePath(output, items); err == nil && model.Kind(list) == "list" {
					element := model.Shapes[list].Member
					for _, raw := range sortedValueKeys(a.Where) {
						path, err := parsePath(raw)
						if err != nil {
							return err
						}
						target, err := model.ResolvePath(element, path)
						if err != nil {
							continue
						}
						if err := checkAuthoredValue(model, a.Where[raw], target, exports, where+" where "+raw); err != nil {
							return err
						}
					}
				}
			}
		}
		if a.Call != nil {
			record(*a.Call)
		}
		if a.Assert != nil {
			return checkClause(*a.Assert, own, where)
		}
		return nil
	}
	for i, c := range g.Setup {
		if err := checkCall(c, fmt.Sprintf("setup[%d]", i)); err != nil {
			return err
		}
		record(c)
	}
	for _, t := range g.Tests {
		where := "test " + t.Name
		if err := checkCall(t.Call, where+": call"); err != nil {
			return err
		}
		record(t.Call)
		for i, a := range t.Assert {
			if err := checkClause(a, t.Call.Op, fmt.Sprintf("%s: assert[%d]", where, i)); err != nil {
				return err
			}
		}
	}
	for i, c := range g.Teardown {
		if err := checkCall(c, fmt.Sprintf("teardown[%d]", i)); err != nil {
			return err
		}
	}
	return nil
}

// checkAuthoredValue walks one value against the shape it is sent as, or
// compared with, and applies the blob rule wherever the two meet a blob. It
// descends only through structure the value itself spells out; an expression
// other than `$base64` is left alone, because what it evaluates to is the run's
// business and not this check's.
func checkAuthoredValue(model *serviceModel, v any, target string, exports exportKinds, where string) error {
	kind := model.Kind(target)
	if kind == "blob" {
		return checkBlob(model, v, target, exports, where)
	}
	if key, _, isExpr := exprOf(v); isExpr {
		if key == "$base64" {
			return fmt.Errorf("%s: $base64 is bytes, for a blob member, but %s is a %s", where, bareShapeName(target), kind)
		}
		return nil
	}
	switch value := v.(type) {
	case []any:
		if kind != "list" {
			return nil
		}
		for i, item := range value {
			if err := checkAuthoredValue(model, item, model.Shapes[target].Member, exports, fmt.Sprintf("%s[%d]", where, i)); err != nil {
				return err
			}
		}
	case map[string]any:
		for _, k := range sortedKeys(value) {
			var child string
			switch kind {
			case "map":
				child = model.Shapes[target].Value
			case "structure":
				t, ok := model.MemberTarget(target, k)
				if !ok {
					continue
				}
				child = t
			default:
				return nil
			}
			if err := checkAuthoredValue(model, value[k], child, exports, where+"."+k); err != nil {
				return err
			}
		}
	}
	return nil
}

func checkAuthoredAssertion(model *serviceModel, a assertion) error {
	if a.Assert != nil {
		if err := checkAuthoredAssertion(model, *a.Assert); err != nil {
			return err
		}
	}
	if a.Call == nil {
		return nil
	}
	return checkAuthoredCall(model, *a.Call)
}

// checkAuthoredPath parses a response path and, where the shape is known,
// resolves it against the model. A path into a map (SQS's Attributes) resolves
// to the map's value shape, which is what ResolvePath already does for the
// generated side.
func checkAuthoredPath(model *serviceModel, shape, raw string) error {
	path, err := parsePath(raw)
	if err != nil {
		return err
	}
	if shape == "" {
		return fmt.Errorf("path %s: the operation returns nothing", raw)
	}
	if _, err := model.ResolvePath(shape, path); err != nil {
		return err
	}
	return nil
}

// ---------------------------------------------------------------------------
// The join to the registry
// ---------------------------------------------------------------------------

// handRegistry is the little of compat/suites/registry.json this command needs:
// which groups the hand-written half declares and which tests each holds.
// cmd/compat owns the registry's semantics; the generator only has to prove
// that an authored scenario lands on the same names.
type handRegistry struct {
	Groups []handGroup `json:"groups"`
}

type handGroup struct {
	Service string `json:"service"`
	Name    string `json:"name"`
	// Scenario is set once the group has been ported: the authored IR file
	// that resolves its tests, in place of the per-language implementations
	// the flip deletes (#1903). Absent while the port is still soaking under
	// a shadow name.
	Scenario string     `json:"scenario,omitempty"`
	Tests    []handTest `json:"tests"`
}

type handTest struct {
	Name    string   `json:"name"`
	Op      string   `json:"op,omitempty"`
	Depends []string `json:"depends,omitempty"`
}

// find returns the hand-written group of that name.
func (r *handRegistry) find(name string) *handGroup {
	for i := range r.Groups {
		if r.Groups[i].Name == name {
			return &r.Groups[i]
		}
	}
	return nil
}

func loadHandRegistry(path string) (*handRegistry, error) {
	contents, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var reg handRegistry
	// Not strict: this reads four fields of a document another tool owns, and
	// a field added there must not break generation here.
	if err := json.Unmarshal(contents, &reg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return &reg, nil
}

// checkAuthoredAgainstRegistry is the rule that makes a port a port.
//
// §3.11 step 1: "author the IR scenario under the same registry group/test
// names — the names are the join keys, so baseline history, dashboard history,
// and flaky/debt bookkeeping survive untouched". A scenario that quietly
// renamed a test would soak green and then, on the flip, orphan one baseline
// entry per suite and reset that test's history to nothing. Nothing downstream
// reports that as an error; it reports as a gate the entries no longer cover.
//
// So an authored file's base name must be the group it ports; its groups must
// be that group or its shadow; and the test names, their `op` and their
// `depends` must be the native group's, exactly.
func checkAuthoredAgainstRegistry(a authored, hand *handRegistry) error {
	native := a.name
	group := hand.find(native)
	if group == nil {
		return fmt.Errorf("%s: no group %q in compat/suites/registry.json; an authored scenario is named for the hand-written group it ports", a.file, native)
	}
	if group.Service != a.scenario.Service {
		return fmt.Errorf("%s: service %q, but registry group %q is service %q",
			a.file, a.scenario.Service, native, group.Service)
	}
	if len(a.scenario.Groups) != 1 {
		return fmt.Errorf("%s: %d groups; an authored scenario ports one registry group and holds exactly one",
			a.file, len(a.scenario.Groups))
	}
	g := a.scenario.Groups[0]
	got, shadow := nativeGroupOf(g.Name)
	if got != native {
		return fmt.Errorf("%s: group %q; an authored scenario's group is %q while it is live, or %q while it shadows the native one",
			a.file, g.Name, native, native+shadowSuffix)
	}
	if err := checkPortedPairing(a, group, shadow); err != nil {
		return err
	}
	if len(g.Tests) != len(group.Tests) {
		return fmt.Errorf("%s: group %s has %d tests, registry group %s has %d; the names are the join keys and every one must survive the port",
			a.file, g.Name, len(g.Tests), native, len(group.Tests))
	}
	for i, want := range group.Tests {
		got := g.Tests[i]
		wantOp := want.Op
		if wantOp == "" {
			wantOp = want.Name
		}
		if got.Name != want.Name {
			return fmt.Errorf("%s: group %s test[%d] is %q, registry group %s declares %q there; keep the order as well as the names, so the two registries read alike",
				a.file, g.Name, i, got.Name, native, want.Name)
		}
		if got.Op != wantOp {
			return fmt.Errorf("%s: group %s test %s exercises %q, the registry says %q",
				a.file, g.Name, got.Name, got.Op, wantOp)
		}
		if !equalStrings(got.Depends, want.Depends) {
			return fmt.Errorf("%s: group %s test %s depends on %v, the registry says %v; the loader orders and skips on this list, so a shadow that differs is not running the same schedule",
				a.file, g.Name, got.Name, got.Depends, want.Depends)
		}
	}
	return nil
}

// checkPortedPairing holds the two halves of a port to each other.
//
// A group is ported when its hand-written entry carries `scenario` and the
// authored file's group is live rather than a shadow — the two are one
// decision, taken in one PR (§3.11 step 3), and either half alone is a
// half-flipped group whose symptom is silence:
//
//   - `scenario` on the registry group while the authored group is still a
//     shadow means the suites are told to resolve `<group>` through a file
//     that declares `<group>-shadow`. Every loader falls through to "not yet
//     implemented", and the group's implementations may already be deleted.
//   - a live authored group whose registry entry carries no `scenario` means
//     the two files disagree about whether the port happened. The cli and
//     python-sdk interpreters read `scenario` and would resolve nothing; the
//     four typed suites resolve by group name and would run the port anyway,
//     so the suites would silently disagree with each other about what they
//     ran.
func checkPortedPairing(a authored, group *handGroup, shadow bool) error {
	switch {
	case shadow && group.Scenario != "":
		return fmt.Errorf("%s: group %q is still a shadow, but registry group %q already carries \"scenario\": %q — the flip renames the group and adds the field in one change; until then the natives resolve the group",
			a.file, a.scenario.Groups[0].Name, group.Name, group.Scenario)
	case shadow:
		return nil
	case group.Scenario == "":
		return fmt.Errorf("%s: group %q is live, so registry group %q must carry \"scenario\": %q — otherwise the interpreters have nothing to resolve it from while the typed suites resolve it by name, and the suites disagree about what they ran",
			a.file, group.Name, group.Name, a.file)
	case group.Scenario != a.file:
		return fmt.Errorf("%s: registry group %q carries \"scenario\": %q — an authored scenario is named for the group it ports, so the two must be the same file",
			a.file, group.Name, group.Scenario)
	}
	return nil
}

// checkPortedGroupsHaveAuthoredScenarios is the reverse sweep: every
// hand-written group claiming to be ported must be backed by an authored file
// this run actually read.
//
// checkAuthoredAgainstRegistry walks authored files and can only judge the
// groups they name. A registry group carrying a `scenario` that names no file
// at all — a typo, or a file deleted without the entry — would never be
// visited by it, and downstream the failure is a group every suite skips.
func checkPortedGroupsHaveAuthoredScenarios(hand *handRegistry, scenarios []authored) error {
	live := make(map[string]string, len(scenarios))
	for _, a := range scenarios {
		if len(a.scenario.Groups) != 1 {
			continue
		}
		if _, shadow := nativeGroupOf(a.scenario.Groups[0].Name); !shadow {
			live[a.name] = a.file
		}
	}
	for _, g := range hand.Groups {
		if g.Scenario == "" {
			continue
		}
		file, ok := live[g.Name]
		if !ok {
			return fmt.Errorf("%s: group %q carries \"scenario\": %q, but %s holds no live authored scenario for it; a ported group is resolved by an authored file named for it",
				handRegistryPath, g.Name, g.Scenario, authoredDir)
		}
		if file != g.Scenario {
			return fmt.Errorf("%s: group %q carries \"scenario\": %q, but its authored scenario is %s",
				handRegistryPath, g.Name, g.Scenario, file)
		}
	}
	return nil
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
