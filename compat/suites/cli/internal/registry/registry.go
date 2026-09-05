// Package registry loads the shared registry.json and builds TestGroup lists.
package registry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/overcast-sh/overcast-compat-cli/internal/harness"
)

// RegistryGroup is one group entry from registry.json or registry.generated.json.
type RegistryGroup struct {
	Service string         `json:"service"`
	Name    string         `json:"name"`
	Tests   []RegistryTest `json:"tests"`

	// Suites restricts this group to specific suites — nil/empty means every
	// suite. Reserved for cdk-lifecycle on a hand-written group; required on
	// every generated group, where cmd/compatgen derives it from backend
	// availability. See compat/AGENTS.md § registry.json — canonical test matrix.
	Suites []string `json:"suites,omitempty"`
	// Generated, State and Scenario are only legal in registry.generated.json.
	// BuildGroups parses them so a later interpreter can read Scenario and so
	// a generated group can be told apart from a hand-written one (the interim
	// fail rule below), but nothing here branches on State or Scenario beyond
	// that.
	Generated bool   `json:"generated,omitempty"`
	State     string `json:"state,omitempty"`
	Scenario  string `json:"scenario,omitempty"`
}

// RegistryTest is one test entry within a group.
type RegistryTest struct {
	Name     string   `json:"name"`
	Op       *string  `json:"op"`
	Skip     string   `json:"skip"`
	Requires []string `json:"requires"`
	Depends  []string `json:"depends"`
}

// Registry is the root of registry.json.
type Registry struct {
	Groups []RegistryGroup `json:"groups"`
}

// ImplMap maps test names to TestFn implementations.
type ImplMap map[string]harness.TestFn

var _noop harness.TestFn = func(_ context.Context, _ *harness.TestContext) error { return nil }

// generatedNoBackendFail builds the interim-rule result for a generated test
// with neither a static impl nor a scenario backend (#1393).
//
// `suites` on a generated group is derived from backend availability by
// cmd/compatgen, so a suite named in it that cannot execute the group is a
// generator/loader bug, not an environmental gap — it has to be loud. It must
// never be reported as "not yet implemented" (that sentinel means a suite
// simply hasn't gotten to a hand-written group yet) or as "na" (that means the
// tool has no API for the operation) — either would let a coverage hole report
// as success. Because candidate groups are excluded from --compare-baseline
// and --max-failures by cmd/compat (#1367), this cannot red a build until the
// group is promoted to "gated", at which point it is a real regression and
// should. What it can never do is pass or skip.
func generatedNoBackendFail(rg RegistryGroup, rt RegistryTest, op, suite string) harness.TestCase {
	msg := fmt.Sprintf("generated group %q is scoped to %s but %s has no scenario backend", rg.Name, suite, suite)
	return harness.TestCase{
		Name: rt.Name, Op: op, Depends: rt.Depends,
		Fn: func(_ context.Context, _ *harness.TestContext) error { return errors.New(msg) },
	}
}

// registryPath returns the absolute path to registry.json.
// The suite is run with `go run ./cmd/runner` from compat/suites/cli/,
// so registry.json is one level up at compat/suites/registry.json.
func registryPath() string {
	if p := os.Getenv("OVERCAST_REGISTRY_PATH"); p != "" {
		return p
	}
	return filepath.Join("..", "registry.json")
}

// generatedRegistryPath returns the path to registry.json's machine-written
// sibling, resolved from the same directory as registryPath() — whatever env
// override or relative path that already uses, applied identically. See
// docs/plans/compat-coverage-modelgen.md § 3.6.
func generatedRegistryPath() string {
	return filepath.Join(filepath.Dir(registryPath()), "registry.generated.json")
}

// RepoRoot returns the repository root, derived from registryPath() rather than
// from the working directory, so an OVERCAST_REGISTRY_PATH override moves both
// together. A generated group's `scenario` field is a repository-relative path
// (compat/model/scenarios/<service>.json), and the scenario backend resolves it
// from here — never from CWD, which differs between `go run ./cmd/runner` and
// `go test ./...`.
//
// registry.json always lives at <root>/compat/suites/registry.json, so the root
// is two levels above its directory.
func RepoRoot() string {
	return filepath.Join(filepath.Dir(registryPath()), "..", "..")
}

// generatedRegistryVersion is the only "version" registry.generated.json may
// declare. cmd/compat's own generated-registry reader enforces the same
// constant; kept in step so a schema bump has to be a deliberate multi-file
// change, not a loader that silently accepts anything.
const generatedRegistryVersion = 1

// generatedFile is the root shape of registry.generated.json.
type generatedFile struct {
	Version int             `json:"version"`
	Groups  []RegistryGroup `json:"groups"`
}

// Load reads registry.json and concatenates its generated sibling,
// registry.generated.json, hand-written groups first (see #1393).
//
// A missing generated file is not an error — it is treated exactly like a
// present-but-empty one, so suite images, CI artifacts and branches cut before
// the file existed keep working. A present-but-broken file is a load error,
// the same posture as a malformed registry.json: unparsable JSON, a version
// other than 1, or a group missing "generated", "state" or "suites" — the
// three fields cmd/compatgen always writes — is refused rather than silently
// accepted. A generated group name colliding with a hand-written one is also
// refused, since every downstream consumer (baseline, flaky list, parity
// debt) keys on suite/group/test with no notion of which file a group came
// from — a collision would merge two different tests into one entry instead
// of conflicting.
func Load() (*Registry, error) {
	hand, err := loadHandWritten(registryPath())
	if err != nil {
		return nil, err
	}
	gen, err := loadGenerated(generatedRegistryPath())
	if err != nil {
		return nil, err
	}
	handNames := make(map[string]bool, len(hand.Groups))
	for _, g := range hand.Groups {
		handNames[g.Name] = true
	}
	for _, g := range gen.Groups {
		if handNames[g.Name] {
			return nil, fmt.Errorf("registry: generated group %q collides with a hand-written group of the same name", g.Name)
		}
	}
	hand.Groups = append(hand.Groups, gen.Groups...)
	return hand, nil
}

// loadHandWritten reads and parses registry.json. Split out from Load so the
// error messages it has always produced are unchanged by the concatenation
// this PR adds.
func loadHandWritten(p string) (*Registry, error) {
	b, err := os.ReadFile(p)
	if err != nil {
		return nil, fmt.Errorf("registry: read %s: %w", p, err)
	}
	var reg Registry
	if err := json.Unmarshal(b, &reg); err != nil {
		return nil, fmt.Errorf("registry: parse: %w", err)
	}
	return &reg, nil
}

// loadGenerated reads registry.generated.json, treating a missing file as an
// empty registry and validating a present one against the shape
// registry.generated.schema.json requires.
func loadGenerated(p string) (*Registry, error) {
	b, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			return &Registry{}, nil
		}
		return nil, fmt.Errorf("registry: read %s: %w", p, err)
	}
	var gen generatedFile
	if err := json.Unmarshal(b, &gen); err != nil {
		return nil, fmt.Errorf("registry: parse %s: %w", p, err)
	}
	if gen.Version != generatedRegistryVersion {
		return nil, fmt.Errorf("registry: %s: version %d, want %d", p, gen.Version, generatedRegistryVersion)
	}
	for _, g := range gen.Groups {
		if !g.Generated {
			return nil, fmt.Errorf("registry: %s: group %q is missing \"generated\": true", p, g.Name)
		}
		if g.State == "" {
			return nil, fmt.Errorf("registry: %s: group %q is missing \"state\"", p, g.Name)
		}
		if len(g.Suites) == 0 {
			return nil, fmt.Errorf("registry: %s: group %q is missing \"suites\"", p, g.Name)
		}
	}
	return &Registry{Groups: gen.Groups}, nil
}

// BuildGroupsOptions controls how groups are assembled from the registry.
type BuildGroupsOptions struct {
	Suite        string
	Capabilities map[string]bool
	Setup        map[string]func(context.Context, *harness.TestContext) error
	Teardown     map[string]func(context.Context, *harness.TestContext) error
	// Scenario resolves a test to an implementation when no static impl
	// claims it, given the test's group (carrying its `scenario` path) and
	// the test itself. Consulted for any test with no static impl — a
	// generated group's, or a hand-written group ported to an authored
	// scenario under the same registry name (G6, see ScenarioBackend below) —
	// not only a generated group's. Nil until a G2 interpreter is wired up;
	// every such test currently falls through to the interim fail rule
	// (generated groups) or the not-yet-implemented sentinel (hand-written
	// groups) in BuildGroups. See docs/plans/compat-coverage-modelgen.md § 3.6.
	Scenario ScenarioBackend
}

// ScenarioBackend is the extension point a scenario-driven interpreter (G2)
// registers to execute a test that has no static impl. BuildGroups calls it
// after the static impl lookup ("<group>:<test>" then bare "<test>") comes up
// empty, for any such test — not only a generated group's. A hand-written
// group ported to an authored scenario keeps its registry group and test
// names (G6, docs/plans/compat-coverage-modelgen.md § 3.11), so it resolves
// through this same hook once a backend claims it, with no loader change.
type ScenarioBackend func(group RegistryGroup, test RegistryTest) (fn harness.TestFn, ok bool)

// topoSort topologically sorts tests within a group using their declared dependencies.
// Tests with no dependencies come first; tests whose deps are all resolved come next.
// Falls back to declaration order for tests at the same dependency depth.
func topoSort(tests []RegistryTest) []RegistryTest {
	byName := make(map[string]*RegistryTest, len(tests))
	for i := range tests {
		byName[tests[i].Name] = &tests[i]
	}

	sorted := make([]RegistryTest, 0, len(tests))
	visited := make(map[string]bool)
	visiting := make(map[string]bool) // cycle detection

	var visit func(t *RegistryTest)
	visit = func(t *RegistryTest) {
		if visited[t.Name] || visiting[t.Name] {
			return
		}
		visiting[t.Name] = true
		for _, dep := range t.Depends {
			if dt, ok := byName[dep]; ok {
				visit(dt)
			}
		}
		delete(visiting, t.Name)
		visited[t.Name] = true
		sorted = append(sorted, *t)
	}

	for i := range tests {
		visit(&tests[i])
	}
	return sorted
}

// BuildGroups creates a []harness.TestGroup from the registry, auto-skipping
// tests whose impls are absent or whose requirements are unmet.
func BuildGroups(reg *Registry, impls ImplMap, opts BuildGroupsOptions) []harness.TestGroup {
	caps := opts.Capabilities
	if caps == nil {
		caps = map[string]bool{}
	}
	ambiguous := AmbiguousTestNames(reg)
	var groups []harness.TestGroup

	for _, rg := range reg.Groups {
		// A group scoped to specific suites (`suites` in the registry) is out
		// of scope for every other suite: no tests, no skips, no results. This
		// generalises the old `rg.Service == "cdk"` special case — cdk-lifecycle
		// is the only hand-written group that declares `suites` (["cdk"]), and
		// it is the only group with service "cdk", so filtering on `suites`
		// here is behaviour-identical to the service check it replaces. Every
		// generated group must declare `suites` too (enforced in loadGenerated).
		if len(rg.Suites) > 0 && !slices.Contains(rg.Suites, opts.Suite) {
			continue
		}
		var tests []harness.TestCase

		// Topologically sort tests by their declared dependencies.
		sortedTests := topoSort(rg.Tests)

		for _, rt := range sortedTests {
			op := ""
			if rt.Op != nil {
				op = *rt.Op
			}

			if rt.Skip != "" {
				tests = append(tests, harness.TestCase{Name: rt.Name, Fn: _noop, Op: op, Skip: rt.Skip, Depends: rt.Depends})
				continue
			}

			// Capability gate
			var missing []string
			for _, req := range rt.Requires {
				if !caps[req] {
					missing = append(missing, req)
				}
			}
			if len(missing) > 0 {
				tests = append(tests, harness.TestCase{
					Name: rt.Name, Fn: _noop, Op: op,
					Skip:    fmt.Sprintf("requires capabilities: %v", missing),
					Depends: rt.Depends,
				})
				continue
			}

			// Look up by group-qualified key ("groupName:testName") first, then
			// fall back to the bare test name.  The bare fallback is refused for
			// a name claimed by more than one group: it would bind this group to
			// another group's implementation and report its result as ours.
			// ValidateImpls rejects such a registration outright; this is the
			// second line of defence, so a mis-bind cannot occur even if
			// validation is bypassed.
			qualifiedKey := rg.Name + ":" + rt.Name
			fn, ok := impls[qualifiedKey]
			if !ok && !ambiguous[rt.Name] {
				fn, ok = impls[rt.Name]
			}
			if !ok {
				// Extension point (#1393, G6): any test with no static impl —
				// a generated group's, or a hand-written group ported to an
				// authored scenario under the same registry group/test names
				// — is tried against the scenario backend before falling back
				// to the generated-fail rule or the hand-written sentinel
				// below. This is not limited to generated groups: a G6-ported
				// hand-written group resolves to its scenario here with no
				// loader change.
				if opts.Scenario != nil {
					if bfn, resolved := opts.Scenario(rg, rt); resolved {
						tests = append(tests, harness.TestCase{Name: rt.Name, Fn: bfn, Op: op, Depends: rt.Depends})
						continue
					}
				}
				if rg.Generated {
					tests = append(tests, generatedNoBackendFail(rg, rt, op, opts.Suite))
					continue
				}
				tests = append(tests, harness.TestCase{
					Name: rt.Name, Fn: _noop, Op: op,
					// Sentinel wording is shared by every suite loader: the
					// parity checker (cmd/compat --check-parity) classifies
					// registry gaps by this exact phrasing, so it must not
					// drift. See compat/AGENTS.md § Baseline & uniformity.
					Skip:    fmt.Sprintf("not yet implemented in %s test suite", opts.Suite),
					Depends: rt.Depends,
				})
				continue
			}
			if fn == nil {
				// Explicitly registered as nil → the AWS CLI does not yet
				// expose this operation.  Emit as N/A, not as a suite gap.
				tests = append(tests, harness.TestCase{
					Name: rt.Name, Fn: _noop, Op: op,
					NA:      "not yet supported by the AWS CLI",
					Depends: rt.Depends,
				})
				continue
			}
			tests = append(tests, harness.TestCase{Name: rt.Name, Fn: fn, Op: op, Depends: rt.Depends})
		}

		groupName := rg.Name // capture for closures
		g := harness.TestGroup{
			Suite:   opts.Suite,
			Service: rg.Service,
			Name:    groupName,
			Tests:   tests,
		}

		if fn, ok := opts.Setup[groupName]; ok {
			g.Setup = func(ctx context.Context, t *harness.TestContext) error {
				return fn(ctx, t)
			}
		}
		if fn, ok := opts.Teardown[groupName]; ok {
			g.Teardown = func(ctx context.Context, t *harness.TestContext) error {
				return fn(ctx, t)
			}
		}

		groups = append(groups, g)
	}
	return groups
}

// AmbiguousTestNames returns the test names that more than one registry group
// declares.
//
// A bare-name implementation cannot serve these. `ListUsers` belongs to both
// `iam-users` and `cognito-userpools`, so a bare `ListUsers` impl binds
// whichever group happens to resolve it — and the loser silently runs the
// other service's test and reports the result as its own. Suites must register
// the group-qualified key for an ambiguous name.
func AmbiguousTestNames(reg *Registry) map[string]bool {
	owners := TestNameOwners(reg)
	ambiguous := map[string]bool{}
	for name, groups := range owners {
		if len(groups) > 1 {
			ambiguous[name] = true
		}
	}
	return ambiguous
}

// TestNameOwners maps each registry test name to the sorted names of the groups
// that declare it.
func TestNameOwners(reg *Registry) map[string][]string {
	owners := map[string][]string{}
	for _, rg := range reg.Groups {
		for _, rt := range rg.Tests {
			if !slices.Contains(owners[rt.Name], rg.Name) {
				owners[rt.Name] = append(owners[rt.Name], rg.Name)
			}
		}
	}
	for name := range owners {
		slices.Sort(owners[name])
	}
	return owners
}

// ImplSource is one service group's contribution to the suite's impl map,
// labelled with the group it came from so a collision can name both sides.
type ImplSource struct {
	Name  string
	Impls ImplMap
}

// MergeImpls flattens the per-service impl maps into the single map the loader
// resolves against, refusing any key that two sources both register.
//
// The merge used to be `for k, v := range sg.Impls { impls[k] = v }` — last
// writer wins, and silently. Two service files both registering
// "lambda-crud:CreateFunction" left one implementation unreachable with nothing
// said about it, and the run reported a result for whichever one survived.
// ValidateImpls cannot catch this: by the time it sees the flattened map the
// discarded implementation is already gone, and the surviving key resolves
// perfectly well.
func MergeImpls(sources []ImplSource, suite string) (ImplMap, error) {
	merged := make(ImplMap)
	owner := make(map[string]string) // key → the source that registered it first

	var problems []string
	for _, src := range sources {
		for key, fn := range src.Impls {
			if first, dup := owner[key]; dup {
				problems = append(problems, duplicateProblem(key, first, src.Name))
				continue
			}
			owner[key] = src.Name
			merged[key] = fn
		}
	}
	if len(problems) == 0 {
		return merged, nil
	}
	// Map iteration order is random, so sort for a stable message. Every
	// problem starts with the key, which is what a reader scans for.
	slices.Sort(problems)
	return nil, fmt.Errorf("[%s] %d duplicate impl registration(s):\n  - %s",
		suite, len(problems), strings.Join(problems, "\n  - "))
}

// duplicateProblem describes one collision. The two sources are the same when a
// single service group registers the key twice.
func duplicateProblem(key, first, second string) string {
	if first == second {
		return fmt.Sprintf("impl %q is registered twice by %q"+
			" — one of the two would be silently discarded; remove or re-key one", key, first)
	}
	return fmt.Sprintf("impl %q is registered by both %q and %q"+
		" — one of the two would be silently discarded; remove or re-key one", key, first, second)
}

// ValidateImpls rejects impl keys that cannot be bound to exactly one registry
// test. It returns an error rather than warning: an unresolvable key used to be
// a stderr line nobody read, while the test it was meant to implement quietly
// fell back to another group's implementation and reported a pass.
//
// Two registrations are refused:
//
//   - a key matching no registry entry — a typo, a stale name, or the wrong
//     separator (every suite uses "group:test"; "group/test" is not accepted);
//   - a bare key for a test name that several groups declare, which cannot say
//     which group it implements.
func ValidateImpls(reg *Registry, impls ImplMap, suite string) error {
	owners := TestNameOwners(reg)
	known := map[string]bool{}
	for _, rg := range reg.Groups {
		for _, rt := range rg.Tests {
			known[rt.Name] = true
			known[rg.Name+":"+rt.Name] = true
		}
	}

	names := make([]string, 0, len(impls))
	for name := range impls {
		names = append(names, name)
	}
	slices.Sort(names)

	var problems []string
	for _, name := range names {
		switch {
		case !known[name]:
			msg := fmt.Sprintf("impl %q matches no registry entry", name)
			if strings.Contains(name, "/") {
				// The Java suite used "group/test" until the separator was
				// unified; a key copied from it resolves to nothing here.
				msg += fmt.Sprintf(` (group-qualified keys use ":", not "/" — did you mean %q?)`,
					strings.Replace(name, "/", ":", 1))
			}
			problems = append(problems, msg)
		case len(owners[name]) > 1:
			// Naming every candidate rather than guessing one: only the author
			// knows which group this implementation is for, and binding it to
			// the wrong one is the failure this check exists to prevent.
			qualified := make([]string, 0, len(owners[name]))
			for _, group := range owners[name] {
				qualified = append(qualified, fmt.Sprintf("%q", group+":"+name))
			}
			problems = append(problems, fmt.Sprintf(
				"impl %q is ambiguous: groups %v all declare a test named %q — qualify it with the group it implements, one of: %s",
				name, owners[name], name, strings.Join(qualified, ", ")))
		}
	}
	if len(problems) == 0 {
		return nil
	}
	return fmt.Errorf("[%s] %d unusable impl registration(s):\n  - %s",
		suite, len(problems), strings.Join(problems, "\n  - "))
}

// EmitAllNA writes NDJSON run_start + na result for every test in the registry +
// run_end to stdout. Used when a hard prerequisite (e.g. the aws CLI binary) is
// missing and no test can possibly run.
func EmitAllNA(reg *Registry, suiteName, runID, reason string) {
	emit := func(obj any) {
		b, _ := json.Marshal(obj)
		fmt.Println(string(b))
	}
	emit(map[string]any{"event": "run_start", "suite": suiteName, "run_id": runID})
	for _, rg := range reg.Groups {
		for _, rt := range rg.Tests {
			op := ""
			if rt.Op != nil {
				op = *rt.Op
			}
			emit(map[string]any{
				"event":       "test_result",
				"suite":       suiteName,
				"service":     rg.Service,
				"group":       rg.Name,
				"test":        rt.Name,
				"op":          op,
				"status":      "na",
				"na":          reason,
				"duration_ms": 0,
			})
		}
	}
	emit(map[string]any{"event": "run_end", "suite": suiteName, "run_id": runID})
}
