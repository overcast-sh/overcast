//go:build dev

package main

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/overcast-sh/overcast/internal/awsapi"
)

// The emitter — docs/plans/compat-coverage-modelgen.md §3.3–§3.5.
//
// One recipe plus one shape snapshot plus the capability table become one
// scenario file: a lifecycle group per resource (create → read/list → update
// → tag → authored → delete, with setup and teardown for what it requires)
// and one probe group for the operations the emulator does not implement.
// Anything that cannot be expressed is refused into gaps, never guessed.
//
// Two classes of problem are kept apart on purpose. A recipe or values entry
// that contradicts the model (an unknown member, a literal of the wrong kind,
// a path that resolves to nothing) is an error: the curated file is wrong and
// generation stops. A recipe that simply does not cover an operation is a
// refusal: the operation is recorded in gaps.json and generation continues.

// Tag literals used by every generated tag/untag test. Chosen to satisfy
// every AWS tag key/value pattern seen so far (letters only).
const (
	compatTagKey   = "compat"
	compatTagValue = "scenario"
)

// generation is the result of generating one service, or of reading one
// authored scenario.
type generation struct {
	scenario *scenario
	// unit is the emitted-source key: the base name each typed backend builds
	// its file name and its identifiers from, and the key stale-file detection
	// recognises. For a recipe it is the service; for an authored scenario it
	// is authoredUnit(<file base name>), so a ported group's emitted source
	// sits in its own file rather than inside the service's generated one and
	// the diff of a migration is readable.
	unit string
	// file is the scenario file this generation came from, repository-relative
	// with '/' separators — what the registry records, what an interpreter
	// opens, and field 6 of every failure message.
	file string
	gaps []gap
	auto []autoBinding
	uses []valueUse
	// covered maps an operation to the group/test names that exercise it as
	// the primary call.
	covered map[string][]string
	// folded records reads and lists that did not get a test of their own
	// because the group already had one for the operation.
	folded []string
	// noTeardown lists resources that create something and declare no delete.
	noTeardown []string
	caps       capabilityTable
	model      *serviceModel
}

type generator struct {
	model   *serviceModel
	recipe  recipe
	service string
	caps    capabilityTable
	binder  *binder
	out     *generation
}

// generate builds one service's scenario. client is the §7.3 naming header,
// looked up by the caller (clientInfoFor) so a test can supply one for a
// fixture service the routing manifest does not know.
func generate(model *serviceModel, r recipe, values *valuesTable, caps capabilityTable, client clientInfo) (*generation, error) {
	g := &generator{
		model:   model,
		recipe:  r,
		service: r.Service,
		caps:    caps,
		binder:  &binder{model: model, service: r.Service, values: values},
		out: &generation{
			scenario: &scenario{Version: scenarioVersion, Service: r.Service, Client: client, Groups: []group{}},
			unit:     r.Service,
			file:     scenarioPath(r.Service),
			covered:  make(map[string][]string),
			caps:     caps,
			model:    model,
		},
	}
	if err := g.checkRecipeAgainstModel(); err != nil {
		return nil, err
	}
	if err := g.deriveFromModel(); err != nil {
		return nil, err
	}
	for _, res := range g.recipe.Resources {
		if res.SetupOnly {
			continue
		}
		if err := g.lifecycleGroup(res); err != nil {
			return nil, err
		}
	}
	if err := g.probeGroup(); err != nil {
		return nil, err
	}
	g.uncoveredImplemented()
	g.out.auto = g.binder.auto
	g.out.uses = g.binder.uses
	sortGaps(g.out.gaps)
	if err := validateScenario(g.out.scenario); err != nil {
		return nil, fmt.Errorf("generated scenario for %s is malformed: %w", r.Service, err)
	}
	return g.out, nil
}

// clientInfoFor assembles the §7.3 naming header from the manifest (the
// router's own view of the service) and the snapshot's service traits.
func clientInfoFor(model *serviceModel, service string) (clientInfo, error) {
	ops := model.Operations()
	if len(ops) == 0 {
		return clientInfo{}, fmt.Errorf("%s models no operations", service)
	}
	entries := awsapi.Operations(service, ops[0])
	if len(entries) == 0 {
		return clientInfo{}, fmt.Errorf("the routing manifest has no entry for %s/%s; is the capability key right?", service, ops[0])
	}
	op := entries[0]
	return clientInfo{
		SDKID:              op.SDKID,
		EndpointPrefix:     model.EndpointPrefix,
		SigningName:        model.SigningName,
		Protocol:           string(op.Protocol),
		APIVersion:         op.APIVersion,
		TargetPrefix:       strings.TrimSuffix(op.TargetPrefix, "."),
		AWSQueryCompatible: model.QueryCompatible,
	}, nil
}

// checkRecipeAgainstModel verifies every operation the recipe names exists
// and every read/list path resolves, before any group is built, so a typo is
// reported once with its location rather than as a refusal in three groups.
func (g *generator) checkRecipeAgainstModel() error {
	for _, res := range g.recipe.Resources {
		ops := res.setupOps()
		for _, rd := range res.allReads() {
			ops = append(ops, rd.Op)
		}
		if res.List != nil {
			ops = append(ops, res.List.Op)
		}
		for _, m := range res.Mutable {
			ops = append(ops, m.Op)
		}
		if res.Tags != nil {
			ops = append(ops, res.Tags.Tag.Op, res.Tags.Untag.Op, res.Tags.List.Op)
		}
		if res.Delete != nil {
			ops = append(ops, res.Delete.Op)
		}
		for _, a := range res.Operations {
			ops = append(ops, a.Op)
		}
		for _, op := range ops {
			if !g.model.HasOperation(op) {
				return fmt.Errorf("resource %q names operation %q, which %s does not model", res.ID, op, g.recipe.modelService())
			}
		}
		if res.NotFound != nil && !g.model.IsErrorShape(res.NotFound.Error) {
			return fmt.Errorf("resource %q: notFound error %q is not an error shape in the model", res.ID, res.NotFound.Error)
		}
	}
	for _, exceptions := range []struct {
		field string
		ops   map[string]string
	}{{"allowProbe", g.recipe.AllowProbe}, {"neverProbe", g.recipe.NeverProbe}} {
		for _, op := range sortedStringKeys(exceptions.ops) {
			if !g.model.HasOperation(op) {
				return fmt.Errorf("%s names operation %q, which %s does not model", exceptions.field, op, g.recipe.modelService())
			}
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Model-derived recipe fields
// ---------------------------------------------------------------------------

// Two recipe fields the model already determines are derived rather than
// re-typed: the error a read raises once the resource is gone, and the member
// of a list response that carries the page. Both stay writable — a recipe
// value is an override and is used as written — because the derivation covers
// the common shape, not every service. What a recipe may not do is disagree
// with the model about the not-found error while the model names exactly one:
// one of the two is wrong, and quietly preferring either hides it.
//
// Derivation runs after checkRecipeAgainstModel, so every operation it reads
// is known to exist, and before any group is built, so the emitter sees one
// completed recipe rather than a field that is sometimes absent.

// notFoundSuffixes are the shape-name endings AWS gives the error that says
// "what you named is not there". The match is on the suffix, not anywhere in
// the name: `KmsNotFound` is a real not-found error and belongs in the set,
// while a service that spells one of these words in the middle of a longer
// name is left to the recipe. Under-deriving costs a recipe line;
// over-deriving would make a delete assert the wrong error.
var notFoundSuffixes = []string{"NotFound", "NotFoundException", "DoesNotExist", "NonExistent"}

// derivableNotFoundErrors returns the not-found-shaped errors an operation
// declares, sorted, as OperationErrors gives them. It is deliberately
// stricter than the scaffolder's notFoundCandidates, which proposes anything
// with the word anywhere in the name for a human to choose between: this one
// decides on its own, so it matches only the suffix.
func derivableNotFoundErrors(model *serviceModel, op string) []string {
	var out []string
	for _, shape := range model.OperationErrors(op) {
		name := bareShapeName(shape)
		for _, suffix := range notFoundSuffixes {
			if strings.HasSuffix(name, suffix) {
				out = append(out, shape)
				break
			}
		}
	}
	return out
}

// derivedNotFound is the error a resource's read raises after its delete,
// when the model settles it: the read must be one a delete can replay (a
// consuming read never raises it — it is refused later — and deriving from
// one would silently move the delete off its list-membership check), and the
// read must declare exactly one not-found-shaped error.
func (g *generator) derivedNotFound(res resource) (string, bool) {
	if res.Read == nil || res.Read.Consuming {
		return "", false
	}
	candidates := derivableNotFoundErrors(g.model, res.Read.Op)
	if len(candidates) != 1 {
		return "", false
	}
	return candidates[0], true
}

// deriveFromModel fills in what the model determines and the recipe left out,
// and refuses a `list` whose page the model does not settle. It replaces
// g.recipe.Resources rather than editing in place, so a recipe value a caller
// still holds is never rewritten underneath it.
func (g *generator) deriveFromModel() error {
	resources := make([]resource, len(g.recipe.Resources))
	copy(resources, g.recipe.Resources)
	for i := range resources {
		if err := g.deriveNotFound(&resources[i]); err != nil {
			return err
		}
		g.deriveItemsPath(&resources[i])
	}
	g.recipe.Resources = resources
	return nil
}

func (g *generator) deriveNotFound(res *resource) error {
	derived, ok := g.derivedNotFound(*res)
	switch {
	case res.NotFound != nil:
		if ok && derived != res.NotFound.Error {
			return fmt.Errorf("resource %q: notFound.error is %q, but %s declares exactly one not-found error, %s; drop the override or correct it",
				res.ID, res.NotFound.Error, res.Read.Op, derived)
		}
	case ok && res.Delete != nil:
		// notFound exists to prove absence after a delete; a resource with
		// no delete has nothing to prove, so nothing is derived.
		res.NotFound = &notFoundSpec{Error: derived}
	}
	return nil
}

func (g *generator) deriveItemsPath(res *resource) {
	if res.List == nil || res.List.ItemsPath != "" {
		return
	}
	member, candidates := listPageMember(g.model, res.List.Op, g.model.OutputShape(res.List.Op))
	if member == "" {
		// Only 0 or 2-and-more candidates reach here: listPageMember takes a
		// sole one, so the plural below is always right.
		held := "no list member at all"
		if len(candidates) > 0 {
			held = fmt.Sprintf("%d list members (%s) and no @paginated `items` trait to choose between them", len(candidates), strings.Join(candidates, ", "))
		}
		g.refuseOp(g.groupName(res.ID), res.List.Op, refuse(reasonAmbiguousListPage,
			fmt.Sprintf("%s returns %s, so the model does not say which member is the page of items; give resource %q an explicit list.itemsPath",
				res.List.Op, held, res.ID)))
		res.List = nil
		return
	}
	spec := *res.List
	spec.ItemsPath = "$." + member
	res.List = &spec
}

// groupName follows §3.3: <service>-gen-<resource>, <service>-gen-probe.
func (g *generator) groupName(suffix string) string {
	return g.service + "-gen-" + suffix
}

func (g *generator) refuseOp(groupName, op string, r *refusal) {
	g.out.gaps = append(g.out.gaps, gap{Service: g.service, Operation: op, Group: groupName, Reason: r.Reason, Detail: r.Detail})
}

// ---------------------------------------------------------------------------
// Group builder
// ---------------------------------------------------------------------------

type groupBuilder struct {
	g         *generator
	group     group
	scope     []resource
	exports   exportKinds
	producers map[string]string
	names     map[string]string
	// owner is the resource whose call is being bound; its binds take
	// precedence over every other in-scope resource's. nil for probes.
	owner *resource
}

// newGroupBuilder starts a group of the given kind. `parallel` is derived from
// the kind here rather than set by each caller: it is a restatement of what a
// probe group already is (no setup, no teardown, no exports), so a probe that
// had to remember to ask for it would be one refactor away from losing it
// silently.
func (g *generator) newGroupBuilder(name, kind string, scope []resource) *groupBuilder {
	return &groupBuilder{
		g:         g,
		group:     group{Name: name, Kind: kind, Parallel: kind == groupProbe, Setup: []call{}, Tests: []test{}, Teardown: []call{}},
		scope:     scope,
		exports:   make(exportKinds),
		producers: make(map[string]string),
		names:     make(map[string]string),
	}
}

func (gb *groupBuilder) bindScope() bindScope {
	probe := gb.group.Kind == groupProbe
	if gb.owner == nil {
		return bindScope{resources: gb.scope, exports: gb.exports, probe: probe}
	}
	resources := []resource{*gb.owner}
	for _, res := range gb.scope {
		if res.ID != gb.owner.ID {
			resources = append(resources, res)
		}
	}
	return bindScope{resources: resources, exports: gb.exports, probe: probe}
}

// forResource makes res the owner of the calls bound next.
func (gb *groupBuilder) forResource(res resource) {
	owner := res
	gb.owner = &owner
}

// bindCall binds a recipe call for this group.
func (gb *groupBuilder) bindCall(op string, explicit map[string]any) (call, *refusal, error) {
	params, ref, err := gb.g.binder.bind(gb.group.Name, op, explicit, gb.bindScope())
	if err != nil || ref != nil {
		return call{}, ref, err
	}
	return call{Op: op, Params: params}, nil, nil
}

// registerExports resolves each export path against the operation's output
// and records the context path, its kind and its producer.
func (gb *groupBuilder) registerExports(c *call, exports map[string]string, producer string) error {
	if len(exports) == 0 {
		return nil
	}
	output := gb.g.model.OutputShape(c.Op)
	if output == "" {
		return fmt.Errorf("%s returns nothing, so it cannot export %s", c.Op, sortedStringKeys(exports))
	}
	if c.Export == nil {
		c.Export = make(map[string]string, len(exports))
	}
	for _, ctx := range sortedStringKeys(exports) {
		path := mustPath(exports[ctx])
		target, err := gb.g.model.ResolvePath(output, path)
		if err != nil {
			return fmt.Errorf("%s export %s: %w", c.Op, ctx, err)
		}
		c.Export[ctx] = exports[ctx]
		gb.exports[ctx] = gb.g.model.Kind(target)
		gb.producers[ctx] = producer
	}
	return nil
}

// checkNames refuses two resources naming themselves with the same suffix
// inside one group, which would be one AWS resource pretending to be two.
func (gb *groupBuilder) checkNames(res resource, params map[string]any) error {
	for _, suffix := range namesIn(params) {
		if owner, taken := gb.names[suffix]; taken && owner != res.ID {
			return fmt.Errorf("group %s: resources %q and %q both use $name %q", gb.group.Name, owner, res.ID, suffix)
		}
		gb.names[suffix] = res.ID
	}
	return nil
}

// instantiate emits a resource's create and derived calls into setup — or,
// for a pre-existing resource, its read, so its exports are in scope.
func (gb *groupBuilder) instantiate(res resource) (*refusal, error) {
	gb.forResource(res)
	if res.Create == nil {
		rd := res.Read
		c, ref, err := gb.bindCall(rd.Op, rd.Params)
		if err != nil || ref != nil {
			return ref, err
		}
		if err := gb.registerExports(&c, prefixed(res.ID, rd.Exports), ""); err != nil {
			return nil, err
		}
		gb.group.Setup = append(gb.group.Setup, c)
		return nil, nil
	}
	params := cloneValue(res.Create.Params).(map[string]any)
	if err := applyMutableFrom(params, res); err != nil {
		return nil, fmt.Errorf("resource %q: %w", res.ID, err)
	}
	if err := gb.checkNames(res, params); err != nil {
		return nil, err
	}
	c, ref, err := gb.bindCall(res.Create.Op, params)
	if err != nil || ref != nil {
		return ref, err
	}
	if err := gb.registerExports(&c, prefixed(res.ID, res.Exports), ""); err != nil {
		return nil, err
	}
	gb.group.Setup = append(gb.group.Setup, c)
	for _, d := range res.Derived {
		dc, ref, err := gb.bindCall(d.Op, d.Params)
		if err != nil || ref != nil {
			return ref, err
		}
		if err := gb.registerExports(&dc, map[string]string{res.ID + "." + d.Export: d.Path}, ""); err != nil {
			return nil, err
		}
		gb.group.Setup = append(gb.group.Setup, dc)
	}
	return nil, nil
}

// teardown emits delete calls for the given resources, in the order given.
func (gb *groupBuilder) teardown(resources []resource) error {
	for _, res := range resources {
		if res.Delete == nil {
			continue
		}
		gb.forResource(res)
		c, ref, err := gb.bindCall(res.Delete.Op, res.Delete.Params)
		if err != nil {
			return err
		}
		if ref != nil {
			return fmt.Errorf("group %s: teardown of %q cannot be bound: %s", gb.group.Name, res.ID, ref.Detail)
		}
		gb.group.Teardown = append(gb.group.Teardown, c)
	}
	return nil
}

// addTest appends a test, computing its dependencies from the exports it
// consumes and recording coverage.
func (gb *groupBuilder) addTest(t test) error {
	if _, taken := gb.hasTest(t.Name); taken {
		return fmt.Errorf("group %s: test %q would be declared twice", gb.group.Name, t.Name)
	}
	depends := make(map[string]struct{})
	for _, ref := range refsInTest(t) {
		producer, known := gb.producers[ref]
		if !known {
			return fmt.Errorf("group %s: test %s refers to %s, which nothing exports before it", gb.group.Name, t.Name, ref)
		}
		if producer != "" && producer != t.Name {
			depends[producer] = struct{}{}
		}
	}
	t.Depends = sortedSet(depends)
	if len(t.Depends) == 0 {
		t.Depends = nil
	}
	gb.group.Tests = append(gb.group.Tests, t)
	key := gb.group.Name + "/" + t.Name
	gb.g.out.covered[t.Op] = append(gb.g.out.covered[t.Op], key)
	return nil
}

func (gb *groupBuilder) hasTest(name string) (test, bool) {
	for _, t := range gb.group.Tests {
		if t.Name == name {
			return t, true
		}
	}
	return test{}, false
}

// hasTestForOp reports whether an operation is already some test's primary
// call in this group.
func (gb *groupBuilder) hasTestForOp(op string) bool {
	for _, t := range gb.group.Tests {
		if t.Op == op {
			return true
		}
	}
	return false
}

// refsInTest collects every $ref a test consumes, in its call and assertions.
func refsInTest(t test) []string {
	set := make(map[string]struct{})
	add := func(v any) {
		for _, ref := range refsIn(v) {
			set[ref] = struct{}{}
		}
	}
	add(t.Call.Params)
	var walk func(a assertion)
	walk = func(a assertion) {
		if a.Call != nil {
			add(a.Call.Params)
		}
		for _, c := range a.Checks {
			if c.Equals != nil {
				add(c.Equals)
			}
		}
		for _, v := range a.Where {
			add(v)
		}
		if a.Assert != nil {
			walk(*a.Assert)
		}
	}
	for _, a := range t.Assert {
		walk(a)
	}
	return sortedSet(set)
}

// wrap applies the resource's async retry to a read-back style clause.
func wrap(a assertion, res resource) assertion {
	if res.Async == nil {
		return a
	}
	return eventually(a, *res.Async)
}

// wrapAuthored applies the resource's async retry to a clause a human wrote.
// Only a clause that makes a call of its own can be retried usefully — a
// responseField, or a call-less listContains, re-reads the one response the
// primary call already produced — and an author who wrote their own
// `eventually` has already chosen the budget.
func wrapAuthored(a assertion, res resource) assertion {
	if a.Kind == assertEventually || !makesOwnCall(a) {
		return a
	}
	return wrap(a, res)
}

// makesOwnCall reports whether a clause verifies by calling the service again
// rather than by reading the test's own response. It is what counts as a
// read-back path: a clause that only inspects the response the primary call
// returned cannot show that the call changed anything.
func makesOwnCall(a assertion) bool {
	switch a.Kind {
	case assertReadback:
		return a.Call != nil
	case assertListContains, assertAbsent:
		return a.Call != nil
	case assertEventually:
		return a.Assert != nil && makesOwnCall(*a.Assert)
	}
	return false
}

// anyMakesOwnCall reports whether any clause verifies with a call of its own.
func anyMakesOwnCall(clauses []assertion) bool {
	for _, a := range clauses {
		if makesOwnCall(a) {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Assertion completion (authored clauses and derived ones alike)
// ---------------------------------------------------------------------------

// completeAssertion binds the clause's call (if any), resolves every path it
// names against the model, and fills in error codes. ownOutput is the shape
// of the test's own response, for clauses without a call.
func (gb *groupBuilder) completeAssertion(a *assertion, ownOp, producer string) (*refusal, error) {
	output := ""
	if ownOp != "" {
		output = gb.g.model.OutputShape(ownOp)
	}
	if a.Call != nil {
		c, ref, err := gb.bindCall(a.Call.Op, a.Call.Params)
		if err != nil || ref != nil {
			return ref, err
		}
		if err := gb.registerExports(&c, a.Call.Export, producer); err != nil {
			return nil, err
		}
		*a.Call = c
		output = gb.g.model.OutputShape(c.Op)
	}
	switch a.Kind {
	case assertResponseField, assertReadback:
		for _, path := range sortedCheckPaths(a.Checks) {
			if err := gb.checkCheck(output, path, a.Checks[path]); err != nil {
				return nil, err
			}
		}
	case assertListContains, assertAbsent:
		if a.Error != nil {
			if err := gb.fillError(a.Call.Op, a.Error); err != nil {
				return nil, err
			}
			break
		}
		if output == "" {
			subject := ownOp
			if a.Call != nil {
				subject = a.Call.Op
			}
			return nil, fmt.Errorf("%s returns nothing, so there is no %s to search", subject, a.ItemsPath)
		}
		items, err := gb.g.model.ResolvePath(output, mustPath(a.ItemsPath))
		if err != nil {
			return nil, err
		}
		if gb.g.model.Kind(items) != "list" {
			return nil, fmt.Errorf("%s resolves to %s, which is not a list", a.ItemsPath, items)
		}
		item := gb.g.model.Shapes[items].Member
		for _, path := range sortedValueKeys(a.Where) {
			target, err := gb.g.model.ResolvePath(item, mustPath(path))
			if err != nil {
				return nil, fmt.Errorf("where %w", err)
			}
			if err := gb.g.binder.checkValue(a.Where[path], target, gb.exports, "where "+path, gb.group.Name); err != nil {
				return nil, err
			}
		}
	case assertErrorCode:
		if err := gb.fillError(ownOp, a.Error); err != nil {
			return nil, err
		}
	case assertEventually:
		return gb.completeAssertion(a.Assert, ownOp, producer)
	}
	return nil, nil
}

func (gb *groupBuilder) checkCheck(output, path string, c check) error {
	if output == "" {
		return fmt.Errorf("check on %s: the operation returns nothing", path)
	}
	target, err := gb.g.model.ResolvePath(output, mustPath(path))
	if err != nil {
		return err
	}
	if c.Equals != nil {
		return gb.g.binder.checkValue(c.Equals, target, gb.exports, "check "+path, gb.group.Name)
	}
	return nil
}

// fillError resolves an error spec: the shape must be one the operation
// declares, and the code is what the model says the wire carries.
func (gb *groupBuilder) fillError(op string, spec *errorSpec) error {
	declared := gb.g.model.OperationErrors(op)
	if i := sort.SearchStrings(declared, spec.Shape); i >= len(declared) || declared[i] != spec.Shape {
		return fmt.Errorf("%s does not declare error %s (it declares %s)", op, spec.Shape, strings.Join(declared, ", "))
	}
	spec.Code = gb.g.model.ErrorCode(spec.Shape)
	return nil
}

// ---------------------------------------------------------------------------
// Lifecycle groups
// ---------------------------------------------------------------------------

func (g *generator) lifecycleGroup(res resource) error {
	name := g.groupName(res.ID)
	closure := g.recipe.closure(res.ID)
	// Nearest first for binding: the resource itself, then what it requires,
	// most immediate first.
	scope := make([]resource, 0, len(closure))
	for i := len(closure) - 1; i >= 0; i-- {
		scope = append(scope, closure[i])
	}
	gb := g.newGroupBuilder(name, groupLifecycle, scope)
	for _, required := range closure[:len(closure)-1] {
		ref, err := gb.instantiate(required)
		if err != nil {
			return err
		}
		if ref != nil {
			g.refuseOp(name, res.primaryOp(), refuse(reasonSetupRefused+":"+required.ID,
				fmt.Sprintf("required resource %q cannot be created: %s", required.ID, ref.Detail)))
			return nil
		}
	}

	created := res.Create == nil
	if !created {
		if err := gb.createTest(res); err != nil {
			return err
		}
		created = gb.hasTestForOp(res.Create.Op)
	}
	if created {
		for _, rd := range res.allReads() {
			if err := gb.readTest(res, rd); err != nil {
				return err
			}
		}
		if err := gb.mutableTests(res); err != nil {
			return err
		}
		if err := gb.tagTests(res); err != nil {
			return err
		}
		for _, a := range res.Operations {
			if err := gb.authoredTest(res, a); err != nil {
				return err
			}
		}
		// The list test comes last before delete so that an authored
		// operation that changes what a listing shows (a visibility change
		// on a message in flight) has run.
		if err := gb.listTest(res); err != nil {
			return err
		}
		if err := gb.deleteTest(res); err != nil {
			return err
		}
	}
	if err := gb.teardown(reversed(closure)); err != nil {
		return err
	}
	if res.Delete == nil && res.Create != nil {
		g.out.noTeardown = append(g.out.noTeardown, res.ID)
	}
	if len(gb.group.Tests) > 0 {
		g.out.scenario.Groups = append(g.out.scenario.Groups, gb.group)
	}
	return nil
}

func reversed(resources []resource) []resource {
	out := make([]resource, 0, len(resources))
	for i := len(resources) - 1; i >= 0; i-- {
		out = append(out, resources[i])
	}
	return out
}

// applyMutableFrom seeds the create params with every mutation's `from`, so
// the update that follows is a real change and the create read-back can
// assert the initial value.
func applyMutableFrom(params map[string]any, res resource) error {
	for _, m := range res.Mutable {
		if m.From == nil {
			continue
		}
		if err := setMemberPath(params, m.Member, cloneValue(m.From)); err != nil {
			return err
		}
	}
	return nil
}

func prefixed(id string, exports map[string]string) map[string]string {
	out := make(map[string]string, len(exports))
	for name, path := range exports {
		out[id+"."+name] = path
	}
	return out
}

// identityCheck is the check a read applies to its identity path: equal to
// the export it names, else shaped as the model says.
func (gb *groupBuilder) identityCheck(res resource, rd readSpec) check {
	if rd.Identity == "" {
		return gb.shapeCheck(gb.g.model.OutputShape(rd.Op), rd.IdentityPath)
	}
	return equals(map[string]any{"$ref": res.ID + "." + rd.Identity})
}

// shapeCheck is the strongest check the model supports on a response field
// whose value nothing else pins down: it matches the shape's pattern when
// the model declares one RE2 can express, else it is merely present.
func (gb *groupBuilder) shapeCheck(output, path string) check {
	if output == "" {
		return nonEmpty()
	}
	target, err := gb.g.model.ResolvePath(output, mustPath(path))
	if err != nil {
		return nonEmpty()
	}
	if c := gb.g.model.Constraints(target); c.Pattern != "" && gb.g.model.Kind(target) == "string" {
		if _, verifiable := patternMatches(c.Pattern, ""); verifiable {
			return matches(c.Pattern)
		}
	}
	return nonEmpty()
}

// readCall binds the resource's non-consuming read for use as a read-back.
func (gb *groupBuilder) readCall(res resource) (call, *refusal, error) {
	if res.Read == nil || res.Read.Consuming {
		return call{}, nil, nil
	}
	return gb.bindCall(res.Read.Op, res.Read.Params)
}

func (gb *groupBuilder) createTest(res resource) error {
	op := res.Create.Op
	gb.forResource(res)
	params := cloneValue(res.Create.Params).(map[string]any)
	if err := applyMutableFrom(params, res); err != nil {
		return fmt.Errorf("resource %q: %w", res.ID, err)
	}
	if err := gb.checkNames(res, params); err != nil {
		return err
	}
	c, ref, err := gb.bindCall(op, params)
	if err != nil {
		return err
	}
	if ref != nil {
		gb.g.refuseOp(gb.group.Name, op, ref)
		return nil
	}
	if err := gb.registerExports(&c, prefixed(res.ID, res.Exports), op); err != nil {
		return err
	}
	var clauses []assertion
	// readbacks counts the clauses that call the service again; only those
	// can show the create happened.
	readbacks := 0
	if len(res.Exports) > 0 {
		fields := make(map[string]check, len(res.Exports))
		for _, path := range sortedStringValues(res.Exports) {
			fields[path] = gb.shapeCheck(gb.g.model.OutputShape(op), path)
		}
		clauses = append(clauses, responseField(fields))
	}
	for _, d := range res.Derived {
		dc, ref, err := gb.bindCall(d.Op, d.Params)
		if err != nil {
			return err
		}
		if ref != nil {
			gb.g.refuseOp(gb.group.Name, op, refuse(ref.Reason, "derived export "+d.Export+": "+ref.Detail))
			return nil
		}
		if err := gb.registerExports(&dc, map[string]string{res.ID + "." + d.Export: d.Path}, op); err != nil {
			return err
		}
		clauses = append(clauses, wrap(readback(dc, checks(d.Path, gb.shapeCheck(gb.g.model.OutputShape(d.Op), d.Path))), res))
		readbacks++
	}
	// An authored create assertion takes full responsibility for verifying
	// the create; the derived read-back and list-membership clauses are for
	// resources whose read and list can simply be replayed.
	if len(res.Create.Assert) == 0 {
		ref, err := gb.derivedCreateClauses(res, op, &clauses, &readbacks)
		if err != nil {
			return err
		}
		if ref != nil {
			gb.g.refuseOp(gb.group.Name, op, ref)
			return nil
		}
	}
	for _, authored := range res.Create.Assert {
		a := cloneAssertion(authored)
		ref, err := gb.completeAssertion(&a, op, op)
		if err != nil {
			return err
		}
		if ref != nil {
			gb.g.refuseOp(gb.group.Name, op, ref)
			return nil
		}
		if makesOwnCall(a) {
			readbacks++
		}
		clauses = append(clauses, wrapAuthored(a, res))
	}
	// Only a clause that calls the service again proves the create happened.
	// An authored create.assert holding nothing but a responseField restates
	// what the create response already said, which is the vacuous-test shape
	// §3.5 exists to make unrepresentable.
	if readbacks == 0 {
		gb.g.refuseOp(gb.group.Name, op, refuse(reasonNoReadbackPath,
			fmt.Sprintf("resource %q declares no read, no list and no authored create assertion that calls the service again, so the create cannot be verified", res.ID)))
		return nil
	}
	if len(clauses) == 0 {
		return fmt.Errorf("internal: create test for %s has no clauses", op)
	}
	return gb.addTest(newTest(op, op, c, clauses[0], clauses[1:]...))
}

// derivedCreateClauses appends the create test's read-back (via `read`) and
// list-membership (via `list`) clauses.
func (gb *groupBuilder) derivedCreateClauses(res resource, op string, clauses *[]assertion, readbacks *int) (*refusal, error) {
	rc, ref, err := gb.readCall(res)
	if err != nil {
		return nil, err
	}
	if ref != nil {
		return refuse(ref.Reason, "read-back via "+res.Read.Op+": "+ref.Detail), nil
	}
	if rc.Op != "" {
		fields := checks(res.Read.IdentityPath, gb.identityCheck(res, *res.Read))
		for _, m := range res.Mutable {
			if m.From != nil {
				fields[m.ReadPath] = equals(cloneValue(m.From))
			}
		}
		a := readback(rc, fields)
		if ref, err := gb.completeAssertion(&a, "", op); err != nil || ref != nil {
			return ref, err
		}
		*clauses = append(*clauses, wrap(a, res))
		*readbacks++
	}
	if res.List != nil {
		lc, ref, err := gb.bindCall(res.List.Op, res.List.Params)
		if err != nil {
			return nil, err
		}
		if ref != nil {
			return refuse(ref.Reason, "list-membership via "+res.List.Op+": "+ref.Detail), nil
		}
		a := listContains(&lc, res.List.ItemsPath, map[string]any{res.List.IdentityPath: map[string]any{"$ref": res.ID + "." + res.List.Identity}})
		if ref, err := gb.completeAssertion(&a, "", op); err != nil || ref != nil {
			return ref, err
		}
		*clauses = append(*clauses, wrap(a, res))
		*readbacks++
	}
	return nil, nil
}

// firstErr turns a completion refusal into a gap and returns the error, if
// any, for the callers that cannot continue.
func firstErr(ref *refusal, err error, gb *groupBuilder, op string) error {
	if err != nil {
		return err
	}
	gb.g.refuseOp(gb.group.Name, op, ref)
	return nil
}

func (gb *groupBuilder) readTest(res resource, rd readSpec) error {
	if gb.hasTestForOp(rd.Op) {
		gb.g.out.folded = append(gb.g.out.folded, gb.group.Name+"/"+rd.Op+" (read)")
		return nil
	}
	gb.forResource(res)
	c, ref, err := gb.bindCall(rd.Op, rd.Params)
	if err != nil {
		return err
	}
	if ref != nil {
		gb.g.refuseOp(gb.group.Name, rd.Op, ref)
		return nil
	}
	if err := gb.registerExports(&c, prefixed(res.ID, rd.Exports), rd.Op); err != nil {
		return err
	}
	a := responseField(checks(rd.IdentityPath, gb.identityCheck(res, rd)))
	if ref, err := gb.completeAssertion(&a, rd.Op, rd.Op); err != nil || ref != nil {
		return firstErr(ref, err, gb, rd.Op)
	}
	return gb.addTest(newTest(rd.Op, rd.Op, c, a))
}

func (gb *groupBuilder) listTest(res resource) error {
	if res.List == nil {
		return nil
	}
	op := res.List.Op
	if gb.hasTestForOp(op) {
		gb.g.out.folded = append(gb.g.out.folded, gb.group.Name+"/"+op+" (list)")
		return nil
	}
	gb.forResource(res)
	c, ref, err := gb.bindCall(op, res.List.Params)
	if err != nil {
		return err
	}
	if ref != nil {
		gb.g.refuseOp(gb.group.Name, op, ref)
		return nil
	}
	if err := gb.registerExports(&c, prefixed(res.ID, res.List.Exports), op); err != nil {
		return err
	}
	a := listContains(nil, res.List.ItemsPath, map[string]any{res.List.IdentityPath: map[string]any{"$ref": res.ID + "." + res.List.Identity}})
	if ref, err := gb.completeAssertion(&a, op, op); err != nil || ref != nil {
		return firstErr(ref, err, gb, op)
	}
	return gb.addTest(newTest(op, op, c, a))
}

func (gb *groupBuilder) mutableTests(res resource) error {
	perOp := make(map[string]int)
	for _, m := range res.Mutable {
		perOp[m.Op]++
	}
	for _, m := range res.Mutable {
		name := m.Op
		if perOp[m.Op] > 1 {
			name = m.Op + pascal(lastSegment(m.Member))
		}
		gb.forResource(res)
		if res.Read == nil || res.Read.Consuming {
			gb.g.refuseOp(gb.group.Name, m.Op, refuse(reasonNoReadbackPath,
				fmt.Sprintf("mutation of %s needs a non-consuming read on resource %q to read the new value back", m.Member, res.ID)))
			continue
		}
		params := cloneValue(m.Params)
		if params == nil {
			params = map[string]any{}
		}
		object := params.(map[string]any)
		if err := setMemberPath(object, m.Member, cloneValue(m.To)); err != nil {
			return fmt.Errorf("resource %q mutable %s: %w", res.ID, m.Member, err)
		}
		c, ref, err := gb.bindCall(m.Op, object)
		if err != nil {
			return err
		}
		if ref != nil {
			gb.g.refuseOp(gb.group.Name, m.Op, ref)
			continue
		}
		rc, ref, err := gb.readCall(res)
		if err != nil {
			return err
		}
		if ref != nil {
			gb.g.refuseOp(gb.group.Name, m.Op, refuse(ref.Reason, "read-back via "+res.Read.Op+": "+ref.Detail))
			continue
		}
		a := readback(rc, checks(m.ReadPath, equals(cloneValue(m.To))))
		if ref, err := gb.completeAssertion(&a, "", name); err != nil || ref != nil {
			if err := firstErr(ref, err, gb, m.Op); err != nil {
				return err
			}
			continue
		}
		if err := gb.addTest(newTest(name, m.Op, c, wrap(a, res))); err != nil {
			return err
		}
	}
	return nil
}

// tagShape says how a service carries tags: whether the tag member is a
// string map or a list of structures — and, for the list case, which pair of
// member names that structure uses — plus, independently, whether the untag
// member takes bare tag-key strings or a list of key-only structures.
//
// The two questions are independent because AWS answers them independently:
// KMS pairs {TagKey, TagValue} tags (TagResource/ListResourceTags) with a
// plain list of key strings (UntagResource.TagKeys), while ELB Classic pairs
// ordinary {Key, Value} tags (AddTags) with a list of TagKeyOnly structures
// (RemoveTags.Tags) instead of bare strings.
type tagShape struct {
	mode tagMode
	// keyField and valueField name the tag structure's two string members,
	// set only when mode is tagsAsList.
	keyField, valueField string
	// untagKeyOnly is true when the untag member is a list of key-only
	// structures rather than a list of bare tag-key strings; untagKeyField
	// then names that structure's single string member.
	untagKeyOnly  bool
	untagKeyField string
}

type tagMode int

const (
	tagsAsMap tagMode = iota + 1
	tagsAsList
)

// tagFieldSpellings is every {key, value} member-name pair a list-shaped tag
// structure is accepted under. AWS spells the pair {Key, Value} everywhere
// this generator has met except KMS, which spells its Tag structure
// {TagKey, TagValue}.
var tagFieldSpellings = []struct{ key, value string }{
	{"Key", "Value"},
	{"TagKey", "TagValue"},
}

func (gb *groupBuilder) detectTagShape(res resource) (tagShape, *refusal, error) {
	tags := res.Tags
	input := gb.g.model.InputShape(tags.Tag.Op)
	target, ok := gb.g.model.MemberTarget(input, tags.Tag.Member)
	if !ok {
		return tagShape{}, nil, fmt.Errorf("resource %q: %s has no member %q", res.ID, tags.Tag.Op, tags.Tag.Member)
	}
	untagInput := gb.g.model.InputShape(tags.Untag.Op)
	untagTarget, ok := gb.g.model.MemberTarget(untagInput, tags.Untag.Member)
	if !ok {
		return tagShape{}, nil, fmt.Errorf("resource %q: %s has no member %q", res.ID, tags.Untag.Op, tags.Untag.Member)
	}
	untagKeyOnly, untagKeyField, ok := gb.untagListShape(untagTarget)
	if !ok {
		return tagShape{}, refuse(reasonUnsupportedTagShape+":"+bareShapeName(untagTarget),
			fmt.Sprintf("%s.%s targets %s, which is not a list of strings or of key-only structures", tags.Untag.Op, tags.Untag.Member, untagTarget)), nil
	}
	switch gb.g.model.Kind(target) {
	case "map":
		shape := gb.g.model.Shapes[target]
		if gb.g.model.Kind(shape.Key) == "string" && gb.g.model.Kind(shape.Value) == "string" {
			return tagShape{mode: tagsAsMap, untagKeyOnly: untagKeyOnly, untagKeyField: untagKeyField}, nil, nil
		}
	case "list":
		item := gb.g.model.Shapes[target].Member
		for _, spelling := range tagFieldSpellings {
			key, hasKey := gb.g.model.MemberTarget(item, spelling.key)
			value, hasValue := gb.g.model.MemberTarget(item, spelling.value)
			if hasKey && hasValue && gb.g.model.Kind(key) == "string" && gb.g.model.Kind(value) == "string" {
				return tagShape{
					mode:          tagsAsList,
					keyField:      spelling.key,
					valueField:    spelling.value,
					untagKeyOnly:  untagKeyOnly,
					untagKeyField: untagKeyField,
				}, nil, nil
			}
		}
	}
	return tagShape{}, refuse(reasonUnsupportedTagShape+":"+bareShapeName(target),
		fmt.Sprintf("%s.%s targets %s, which is neither a string map nor a list of {Key, Value} or {TagKey, TagValue}", tags.Tag.Op, tags.Tag.Member, target)), nil
}

// untagListShape reports whether an untag member's target is a list this
// generator can bind a tag key into: a list of strings (the key itself), or
// a list of structures carrying exactly one string member (ELB's
// TagKeyOnly). keyField names that structure's member and is empty when the
// list is of bare strings; ok is false when the target is neither.
func (gb *groupBuilder) untagListShape(target string) (keyOnly bool, keyField string, ok bool) {
	if gb.g.model.Kind(target) != "list" {
		return false, "", false
	}
	element := gb.g.model.Shapes[target].Member
	switch gb.g.model.Kind(element) {
	case "string":
		return false, "", true
	case "structure":
		members := gb.g.model.Members(element)
		if len(members) != 1 {
			return false, "", false
		}
		fieldTarget, _ := gb.g.model.MemberTarget(element, members[0])
		if gb.g.model.Kind(fieldTarget) != "string" {
			return false, "", false
		}
		return true, members[0], true
	default:
		return false, "", false
	}
}

// bareShapeName is a Smithy shape id without its namespace. A refusal reason
// is a machine-readable code plus a name, and gaps.schema.json's `reason`
// pattern has no room for the '#' and '.' of a fully-qualified id — the id
// itself belongs in the detail, where a human reads it.
func bareShapeName(id string) string {
	if i := strings.LastIndexByte(id, '#'); i >= 0 {
		return id[i+1:]
	}
	return id
}

func (gb *groupBuilder) tagTests(res resource) error {
	if res.Tags == nil {
		return nil
	}
	tags := res.Tags
	gb.forResource(res)
	shape, ref, err := gb.detectTagShape(res)
	if err != nil {
		return err
	}
	if ref != nil {
		for _, op := range []string{tags.Tag.Op, tags.List.Op, tags.Untag.Op} {
			gb.g.refuseOp(gb.group.Name, op, ref)
		}
		return nil
	}
	var tagValue any
	if shape.mode == tagsAsMap {
		tagValue = map[string]any{compatTagKey: compatTagValue}
	} else {
		tagValue = []any{map[string]any{shape.keyField: compatTagKey, shape.valueField: compatTagValue}}
	}
	// The listing call is bound once; every clause below takes its own copy.
	listing, ref, err := gb.bindCall(tags.List.Op, tags.List.Params)
	if err != nil {
		return err
	}
	if ref != nil {
		for _, op := range []string{tags.Tag.Op, tags.List.Op, tags.Untag.Op} {
			gb.g.refuseOp(gb.group.Name, op, refuse(ref.Reason, "tag listing via "+tags.List.Op+": "+ref.Detail))
		}
		return nil
	}
	var present map[string]any
	if shape.mode == tagsAsList {
		present = map[string]any{"$." + shape.keyField: compatTagKey, "$." + shape.valueField: compatTagValue}
	}

	// Tag: the tag shows up in the listing.
	c, ref, err := gb.bindCall(tags.Tag.Op, map[string]any{tags.Tag.Member: tagValue})
	if err != nil {
		return err
	}
	if ref != nil {
		gb.g.refuseOp(gb.group.Name, tags.Tag.Op, ref)
	} else {
		lc := cloneCall(listing)
		var a assertion
		if shape.mode == tagsAsMap {
			a = readback(lc, checks(joinPath(tags.List.Path, compatTagKey), equals(compatTagValue)))
		} else {
			a = listContains(&lc, tags.List.Path, cloneValue(present).(map[string]any))
		}
		if ref, err := gb.completeAssertion(&a, "", tags.Tag.Op); err != nil || ref != nil {
			if err := firstErr(ref, err, gb, tags.Tag.Op); err != nil {
				return err
			}
		} else if err := gb.addTest(newTest(tags.Tag.Op, tags.Tag.Op, c, wrap(a, res))); err != nil {
			return err
		}
	}

	// List: its own response carries the tag.
	if gb.hasTestForOp(tags.List.Op) {
		gb.g.out.folded = append(gb.g.out.folded, gb.group.Name+"/"+tags.List.Op+" (tags.list)")
	} else {
		lc := cloneCall(listing)
		var a assertion
		if shape.mode == tagsAsMap {
			a = responseField(checks(joinPath(tags.List.Path, compatTagKey), equals(compatTagValue)))
		} else {
			a = listContains(nil, tags.List.Path, cloneValue(present).(map[string]any))
		}
		if ref, err := gb.completeAssertion(&a, tags.List.Op, tags.List.Op); err != nil || ref != nil {
			if err := firstErr(ref, err, gb, tags.List.Op); err != nil {
				return err
			}
		} else if err := gb.addTest(newTest(tags.List.Op, tags.List.Op, lc, a)); err != nil {
			return err
		}
	}

	// Untag: the tag is gone from the listing. The value it takes follows
	// the untag member's own shape, independent of how the tag structure
	// itself is spelled — a bare key string, or a key-only structure whose
	// single member carries the key (ELB's TagKeyOnly).
	var untagValue any
	if shape.untagKeyOnly {
		untagValue = []any{map[string]any{shape.untagKeyField: compatTagKey}}
	} else {
		untagValue = []any{compatTagKey}
	}
	uc, ref, err := gb.bindCall(tags.Untag.Op, map[string]any{tags.Untag.Member: untagValue})
	if err != nil {
		return err
	}
	if ref != nil {
		gb.g.refuseOp(gb.group.Name, tags.Untag.Op, ref)
		return nil
	}
	lc := cloneCall(listing)
	var a assertion
	if shape.mode == tagsAsMap {
		a = readback(lc, checks(joinPath(tags.List.Path, compatTagKey), missing()))
	} else {
		a = absentFromList(&lc, tags.List.Path, map[string]any{"$." + shape.keyField: compatTagKey})
	}
	if ref, err := gb.completeAssertion(&a, "", tags.Untag.Op); err != nil || ref != nil {
		return firstErr(ref, err, gb, tags.Untag.Op)
	}
	return gb.addTest(newTest(tags.Untag.Op, tags.Untag.Op, uc, wrap(a, res)))
}

func (gb *groupBuilder) authoredTest(res resource, a authoredOp) error {
	name := a.testName()
	// Guard 3 (§3.5) applies to authored coverage too: an update-family
	// operation whose clauses only read its own response asserts that the
	// service echoed the request, which is the "the ID is still non-nil"
	// anti-pattern the contract names. The derived path refuses it by
	// requiring a `mutable` entry; this is the same rule for the authored one.
	// Checked before anything is bound so a refusal leaves no export behind.
	if isUpdateFamily(a.Op) && !anyMakesOwnCall(a.Assert) {
		gb.g.refuseOp(gb.group.Name, a.Op, refuse(reasonUpdateWithoutReadback,
			fmt.Sprintf("%s changes state, but every clause of the authored operation %q reads its own response; add a readback, listContains or absent clause that calls the service again, or declare the change as a `mutable` entry", a.Op, name)))
		return nil
	}
	gb.forResource(res)
	c, ref, err := gb.bindCall(a.Op, a.Params)
	if err != nil {
		return err
	}
	if ref != nil {
		gb.g.refuseOp(gb.group.Name, a.Op, ref)
		return nil
	}
	if err := gb.registerExports(&c, a.Export, name); err != nil {
		return err
	}
	clauses := make([]assertion, 0, len(a.Assert))
	for _, authored := range a.Assert {
		clause := cloneAssertion(authored)
		ref, err := gb.completeAssertion(&clause, a.Op, name)
		if err != nil {
			return fmt.Errorf("resource %q operation %s: %w", res.ID, name, err)
		}
		if ref != nil {
			gb.g.refuseOp(gb.group.Name, a.Op, ref)
			return nil
		}
		clauses = append(clauses, wrapAuthored(clause, res))
	}
	return gb.addTest(newTest(name, a.Op, c, clauses[0], clauses[1:]...))
}

func (gb *groupBuilder) deleteTest(res resource) error {
	if res.Delete == nil {
		return nil
	}
	op := res.Delete.Op
	gb.forResource(res)
	c, ref, err := gb.bindCall(op, res.Delete.Params)
	if err != nil {
		return err
	}
	if ref != nil {
		gb.g.refuseOp(gb.group.Name, op, ref)
		return nil
	}
	var a assertion
	switch {
	case res.NotFound != nil:
		rc, ref, err := gb.readCall(res)
		if err != nil {
			return err
		}
		if ref != nil {
			gb.g.refuseOp(gb.group.Name, op, refuse(ref.Reason, "absence via "+res.Read.Op+": "+ref.Detail))
			return nil
		}
		if rc.Op == "" {
			gb.g.refuseOp(gb.group.Name, op, refuse(reasonNoReadbackPath,
				fmt.Sprintf("notFound is declared but resource %q has no non-consuming read to raise it", res.ID)))
			return nil
		}
		a = absentByError(rc, errorSpec{Shape: res.NotFound.Error})
	case res.List != nil:
		lc, ref, err := gb.bindCall(res.List.Op, res.List.Params)
		if err != nil {
			return err
		}
		if ref != nil {
			gb.g.refuseOp(gb.group.Name, op, refuse(ref.Reason, "absence via "+res.List.Op+": "+ref.Detail))
			return nil
		}
		a = absentFromList(&lc, res.List.ItemsPath, map[string]any{res.List.IdentityPath: map[string]any{"$ref": res.ID + "." + res.List.Identity}})
	default:
		gb.g.refuseOp(gb.group.Name, op, refuse(reasonNoReadbackPath,
			fmt.Sprintf("resource %q declares neither notFound nor list, so absence after delete cannot be verified", res.ID)))
		return nil
	}
	if ref, err := gb.completeAssertion(&a, "", op); err != nil || ref != nil {
		return firstErr(ref, err, gb, op)
	}
	return gb.addTest(newTest(op, op, c, wrap(a, res)))
}

// ---------------------------------------------------------------------------
// Probe group
// ---------------------------------------------------------------------------

// probeGroup covers every modeled operation the emulator does not implement
// and no lifecycle test exercises: one call with curated literals, one
// assertion on the modeled output. Against an unimplemented operation the SDK
// raises the 501 and the harness records `unimplemented`; the assertion is
// never reached, and regeneration moves the operation out of this group the
// day it is implemented.
//
// A probe group has no setup and no teardown, and that is the safety rule
// rather than an omission. A probe is the one generated call no create/delete
// pair contains, so it binds only curated or synthetic literals — deliberately
// nonexistent identifiers — and never a value exported from a resource the run
// owns (the probe branch in binder.go). Two refusals follow. An operation
// whose required members only a live export could supply is refused
// (probe-binds-live-resource). And membership is default-deny by verb: only a
// `Describe*`, `List*` or `Get*` — or an operation the recipe explicitly
// allows — is probed at all, and everything else is refused (never-probe)
// before it is bound, with the recipe's curated sentence where it has one and
// a generated one where it does not. See recipe.probeDecision.
func (g *generator) probeGroup() error {
	name := g.groupName("probe")
	// Recipe order, so the first resource a recipe lists is the one a refusal
	// names when several bind the same member.
	gb := g.newGroupBuilder(name, groupProbe, g.recipe.Resources)
	for _, op := range g.model.Operations() {
		if g.caps.implemented(op) {
			continue
		}
		if _, covered := g.out.covered[op]; covered {
			continue
		}
		if probeable, why := g.recipe.probeDecision(op); !probeable {
			g.refuseOp(name, op, refuse(reasonNeverProbe, why))
			continue
		}
		c, ref, err := gb.bindCall(op, nil)
		if err != nil {
			return err
		}
		if ref != nil {
			g.refuseOp(name, op, ref)
			continue
		}
		a, ref := gb.probeAssertion(op)
		if ref != nil {
			g.refuseOp(name, op, ref)
			continue
		}
		if err := gb.addTest(newTest(op, op, c, a)); err != nil {
			return err
		}
	}
	if len(gb.group.Tests) > 0 {
		g.out.scenario.Groups = append(g.out.scenario.Groups, gb.group)
	}
	return nil
}

// probeAssertion is the one clause a probe carries: the modeled output's
// identity member, non-empty — or, for an operation whose only assertable
// output is a page of results, that list's shape.
//
// An operation that returns nothing gets no probe. Reading the resource the
// call was aimed at would assert something that was already true before the
// call, so an operation implemented without a capability row — the one case a
// probe of it could actually run — would pass while proving nothing. Refusing
// is the honest answer, and gaps.json is where it is recorded.
//
// The same honesty rule decides the list case. A `List*` whose output is a
// page and a next-page token has no identity to assert: the token is absent
// on a single-page answer, which is the answer real AWS gives most of the
// time, so `nonEmpty` on it is false by construction (§3.10 wants assertions
// that are AWS-legal), and `nonEmpty` on a list this call did not populate is
// the same mistake one member over. `isList` is what is left that is true of
// a correct response — present or, as several services legally do, omitted
// entirely — and false of a broken one.
func (gb *groupBuilder) probeAssertion(op string) (assertion, *refusal) {
	output := gb.g.model.OutputShape(op)
	if output == "" {
		return assertion{}, refuse(reasonNoOutputToAssert,
			fmt.Sprintf("%s returns nothing, and reading back the resource it names would assert something the call cannot change, so a probe would assert nothing", op))
	}
	if member := identityMember(gb.g.model, op, output); member != "" {
		return responseField(checks("$."+member, nonEmpty())), nil
	}
	if member := listMember(gb.g.model, op, output); member != "" {
		return responseField(checks("$."+member, isList())), nil
	}
	return assertion{}, refuse(reasonNoOutputToAssert,
		fmt.Sprintf("%s returns no identifying member and no single list to check the shape of — a pagination token is never an identity — and reading back the resource it names would assert something the call cannot change, so a probe would assert nothing", op))
}

// identityMember picks the output member a probe asserts: the first member,
// in suffix-preference order, that looks like an identifier; else the first
// required member; else the first member at all. Pagination tokens and lists
// are never eligible, so "" is a real answer and its callers handle it.
//
// A pagination token is excluded because asserting it non-empty asserts that
// more pages follow, which a single-page answer contradicts — the clause
// would be false against real AWS by construction. A list is excluded because
// a probe populates nothing, so its length says nothing; probeAssertion gives
// it a shape check of its own instead.
func identityMember(model *serviceModel, op, output string) string {
	members := model.Members(output)
	if len(members) == 0 {
		return ""
	}
	tokens := paginationTokens(model, op, output)
	eligible := func(member string) bool {
		if tokens[member] {
			return false
		}
		target, _ := model.MemberTarget(output, member)
		return model.Kind(target) != "list"
	}
	for _, suffix := range []string{"Arn", "Id", "Url", "Name", "Handle", "Status", "State"} {
		for _, member := range members {
			if strings.HasSuffix(member, suffix) && isScalar(model, output, member) && eligible(member) {
				return member
			}
		}
	}
	for _, member := range model.RequiredMembers(output) {
		if eligible(member) {
			return member
		}
	}
	for _, member := range members {
		if eligible(member) {
			return member
		}
	}
	return ""
}

// paginationTokenNames are the member names AWS gives a pagination token. The
// suffix rule below covers every one of them and the variants a service
// invents (`NextPageToken`, `NextPageMarker`); the set is written out because
// it is the vocabulary, and a reader should not have to derive it.
var paginationTokenNames = map[string]bool{
	"NextToken":             true,
	"Marker":                true,
	"NextMarker":            true,
	"ContinuationToken":     true,
	"NextContinuationToken": true,
	"PaginationToken":       true,
}

// paginationTokens is the set of output members that carry a next-page token:
// the one @paginated names as its `outputToken` where the operation declares
// the trait, plus every member whose own name — or whose target shape's name —
// follows the convention, which is how the operations that paginate without
// declaring the trait are caught.
func paginationTokens(model *serviceModel, op, output string) map[string]bool {
	tokens := map[string]bool{}
	if outputToken := model.Pagination(op).OutputToken; outputToken != "" {
		tokens[outputToken] = true
	}
	for _, member := range model.Members(output) {
		target, _ := model.MemberTarget(output, member)
		if isTokenName(member) || isTokenName(bareShapeName(target)) {
			tokens[member] = true
		}
	}
	return tokens
}

func isTokenName(name string) bool {
	return paginationTokenNames[name] || strings.HasSuffix(name, "Token") || strings.HasSuffix(name, "Marker")
}

// listPageMember is the output member that carries a page of items: the one
// @paginated names as its `items`, else the output's sole list-typed
// top-level member. It also returns every top-level list member, so a caller
// that has to explain why the model did not settle the question can name the
// candidates. Two lists and no trait to choose between them is an ambiguity
// the generator refuses rather than guesses at, and so is no list at all.
//
// Two callers share it, and they must agree: the list a probe may check the
// shape of, and the page a recipe's `list` searches when it does not spell
// `itemsPath` out.
func listPageMember(model *serviceModel, op, output string) (member string, candidates []string) {
	for _, name := range model.Members(output) {
		if target, ok := model.MemberTarget(output, name); ok && model.Kind(target) == "list" {
			candidates = append(candidates, name)
		}
	}
	if items := model.Pagination(op).Items; items != "" && slices.Contains(candidates, items) {
		return items, candidates
	}
	if len(candidates) == 1 {
		return candidates[0], candidates
	}
	return "", candidates
}

// listMember is the one list a probe may check the shape of.
func listMember(model *serviceModel, op, output string) string {
	member, _ := listPageMember(model, op, output)
	return member
}

func isScalar(model *serviceModel, structure, member string) bool {
	target, _ := model.MemberTarget(structure, member)
	switch model.Kind(target) {
	case "string", "enum", "integer", "float", "boolean", "timestamp":
		return true
	}
	return false
}

// ---------------------------------------------------------------------------
// Implemented operations the recipe gives no role
// ---------------------------------------------------------------------------

func (g *generator) uncoveredImplemented() {
	for _, op := range g.model.Operations() {
		if !g.caps.implemented(op) {
			continue
		}
		if _, covered := g.out.covered[op]; covered {
			continue
		}
		if g.refusedSomewhere(op) {
			continue
		}
		if isUpdateFamily(op) {
			g.refuseOp(g.groupName("probe"), op, refuse(reasonUpdateWithoutMutable,
				fmt.Sprintf("%s is implemented (%s) but no recipe resource declares a mutable member or tags for it", op, g.caps.statusLabel(op))))
			continue
		}
		g.refuseOp(g.groupName("probe"), op, refuse(reasonProbeOfImplementedOp,
			fmt.Sprintf("%s is implemented (%s), so it may not be probed, and no recipe resource gives it a role", op, g.caps.statusLabel(op))))
	}
}

func (g *generator) refusedSomewhere(op string) bool {
	for _, gp := range g.out.gaps {
		if gp.Operation == op {
			return true
		}
	}
	return false
}

// isUpdateFamily classifies an operation as one that changes a resource that
// already exists, which is what guard 3 (§3.5) hangs off: such an operation
// needs either a declared mutation (the derived path) or an authored clause
// that calls the service again (the authored path), or it is refused. Both
// halves of the guard use this one classifier so they cannot drift apart.
func isUpdateFamily(op string) bool {
	for _, prefix := range []string{"Update", "Set", "Put", "Tag", "Untag"} {
		if strings.HasPrefix(op, prefix) {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Small helpers
// ---------------------------------------------------------------------------

// cloneCall copies a bound call so two clauses never share one params map.
func cloneCall(c call) call {
	out := call{Op: c.Op, Params: cloneValue(c.Params).(map[string]any)}
	if c.Export != nil {
		out.Export = make(map[string]string, len(c.Export))
		for k, v := range c.Export {
			out.Export[k] = v
		}
	}
	return out
}

func cloneAssertion(a assertion) assertion {
	out := a
	out.Comment = ""
	if a.Call != nil {
		c := *a.Call
		c.Params = cloneValue(a.Call.Params).(map[string]any)
		if c.Params == nil {
			c.Params = map[string]any{}
		}
		if a.Call.Export != nil {
			c.Export = make(map[string]string, len(a.Call.Export))
			for k, v := range a.Call.Export {
				c.Export[k] = v
			}
		}
		out.Call = &c
	}
	if a.Checks != nil {
		out.Checks = make(map[string]check, len(a.Checks))
		for k, v := range a.Checks {
			v.Equals = cloneValue(v.Equals)
			out.Checks[k] = v
		}
	}
	if a.Where != nil {
		out.Where = cloneValue(a.Where).(map[string]any)
	}
	if a.Error != nil {
		e := *a.Error
		out.Error = &e
	}
	if a.Assert != nil {
		inner := cloneAssertion(*a.Assert)
		out.Assert = &inner
	}
	return out
}

// sortedStringKeys is the deterministic iteration order of a string-keyed
// map. It is generic in the value type so that a map the recipe grows — one
// of bindRef, say — needs no near-identical helper of its own.
func sortedStringKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func sortedStringValues(m map[string]string) []string {
	values := make([]string, 0, len(m))
	for _, v := range m {
		values = append(values, v)
	}
	sort.Strings(values)
	return values
}

func sortedCheckPaths(m map[string]check) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func sortedValueKeys(m map[string]any) []string { return sortedKeys(m) }

func lastSegment(memberPath string) string {
	parts := strings.Split(memberPath, ".")
	return parts[len(parts)-1]
}

// pascal upper-cases the first letter so a variant suffix folds into a
// PascalCase test name.
func pascal(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
