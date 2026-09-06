//go:build dev

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Recipes — the hand-curated layer.
//
// A recipe says what the model cannot: which operation creates a resource,
// which response field identifies it, how to read it back, what to mutate and
// where the mutation shows up, how to delete it and what not-found looks like.
// Structure is generated from that; semantics stay curated. The schema is
// compat/model/recipe.schema.json and the README documents every field.

type recipe struct {
	Comment string `json:"$comment,omitempty"`
	Service string `json:"service"`
	Model   string `json:"model,omitempty"`
	// NeverProbe and AllowProbe are the recipe's exceptions to the verb rule
	// below, each mapping an operation to the curated sentence saying why.
	// NeverProbe denies one the verb rule would have allowed — a Get* that
	// mutates — and may also restate a denial the verb rule already makes,
	// in which case the sentence replaces the generated one in gaps.json,
	// which is worth doing wherever the prose says more than "not a read".
	// AllowProbe is the other direction: a non-read operation a human has
	// judged safe to call against a live account with a curated,
	// deliberately nonexistent identifier.
	NeverProbe map[string]string `json:"neverProbe,omitempty"`
	AllowProbe map[string]string `json:"allowProbe,omitempty"`
	Resources  []resource        `json:"resources"`
}

// probeVerbs are the operation-name prefixes a probe is allowed to call. The
// list is deliberately short: it is what AWS uses for an operation that only
// reads, and a probe calls an operation the emulator does not implement, so
// against a real account nothing undoes it. `smithy.api#readonly` would say
// this outright, but cmd/awsmodelgen does not keep the trait, so the
// committed snapshots do not carry it; widening the pruner's allowlist is
// the pruner's own change to make (#1795 track A).
var probeVerbs = []string{"Describe", "List", "Get"}

// isReadVerb reports whether an operation's name begins with one of the read
// verbs at a word boundary, so a `Listen*` or `Getaway*` operation is not
// mistaken for a `List` or a `Get`.
func isReadVerb(op string) bool {
	for _, verb := range probeVerbs {
		rest, ok := strings.CutPrefix(op, verb)
		if !ok {
			continue
		}
		if rest == "" {
			return true
		}
		if c := rest[0]; (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') {
			return true
		}
	}
	return false
}

// notAReadOperation is the refusal a non-read operation gets when the recipe
// says nothing about it. It is generated, so a recipe writes a sentence only
// where it has something to add — what the call actually does — rather than
// once per write operation the service models.
func notAReadOperation(op string) string {
	return op + " is not a read operation; probes call unimplemented operations against a live account and must be side-effect free"
}

// probeDecision says whether an operation may be probed and why. Probes are
// default-deny: only a read verb, or an explicit allowProbe, gets through.
func (r recipe) probeDecision(op string) (bool, string) {
	if why, denied := r.NeverProbe[op]; denied {
		return false, why
	}
	if why, allowed := r.AllowProbe[op]; allowed {
		return true, why
	}
	if isReadVerb(op) {
		return true, ""
	}
	return false, notAReadOperation(op)
}

// modelService is the shape-snapshot key: the model service name when it
// differs from the Overcast capability key (cognito-identity-provider vs
// cognito), else the service itself.
func (r recipe) modelService() string {
	if r.Model != "" {
		return r.Model
	}
	return r.Service
}

type resource struct {
	Comment   string   `json:"$comment,omitempty"`
	ID        string   `json:"id"`
	SetupOnly bool     `json:"setupOnly,omitempty"`
	Requires  []string `json:"requires,omitempty"`
	// Create is absent for a pre-existing resource (an organization), whose
	// exports come from its read and which is never set up or torn down.
	Create     *recipeCall        `json:"create,omitempty"`
	Exports    map[string]string  `json:"exports,omitempty"`
	Derived    []derivedExport    `json:"derived,omitempty"`
	Binds      map[string]bindRef `json:"binds,omitempty"`
	Read       *readSpec          `json:"read,omitempty"`
	Reads      []readSpec         `json:"reads,omitempty"`
	List       *listSpec          `json:"list,omitempty"`
	Mutable    []mutation         `json:"mutable,omitempty"`
	Tags       *tagSpec           `json:"tags,omitempty"`
	Delete     *recipeCall        `json:"delete,omitempty"`
	NotFound   *notFoundSpec      `json:"notFound,omitempty"`
	Async      *retrySpec         `json:"async,omitempty"`
	Operations []authoredOp       `json:"operations,omitempty"`
}

// bindRef is one `binds` entry: the context path supplying an input member,
// and whether the recipe wrapped that path in a one-element list.
//
// A recipe writes either the path — `"WidgetId": "widget.id"` — or the path
// inside a list — `"LoadBalancerNames": ["loadbalancer.name"]` — for a member
// the service models as a *list* of the thing the export names. ELB Classic
// is the case that forced it: `AddTags`, `RemoveTags` and `DescribeTags` all
// take `LoadBalancerNames`, while the resource exports the singular name its
// `DescribeLoadBalancers` read needs.
//
// One level of wrapping is all there is, and deliberately: a bind supplies
// one exported value, so the only question a recipe can be asked is whether
// the member takes that value bare or in a list of its own. Anything deeper
// is a literal the recipe writes out in `params`.
type bindRef struct {
	Ref  string
	List bool
}

func (b *bindRef) UnmarshalJSON(contents []byte) error {
	var path string
	if err := json.Unmarshal(contents, &path); err == nil {
		*b = bindRef{Ref: path}
		return nil
	}
	var wrapped []string
	if err := json.Unmarshal(contents, &wrapped); err != nil {
		return fmt.Errorf("a bind is a context path, or a one-element list holding one: %s", contents)
	}
	if len(wrapped) != 1 {
		return fmt.Errorf("a list bind wraps exactly one context path, not %d: %s", len(wrapped), contents)
	}
	*b = bindRef{Ref: wrapped[0], List: true}
	return nil
}

// value is what binding rule 1 supplies for the member: the reference, or a
// one-element list holding it.
func (b bindRef) value() any {
	ref := map[string]any{"$ref": b.Ref}
	if b.List {
		return []any{ref}
	}
	return ref
}

// String renders the bind the way the recipe wrote it, so a refusal detail
// names a wrapped bind as wrapped.
func (b bindRef) String() string {
	if b.List {
		return "[" + b.Ref + "]"
	}
	return b.Ref
}

// recipeCall names an operation and the params the recipe supplies for it.
// Required members the recipe leaves out are bound by the binding algorithm.
type recipeCall struct {
	Op     string         `json:"op"`
	Params map[string]any `json:"params,omitempty"`
	// Assert is authored coverage on the create call, for resources whose
	// read-back cannot be derived from `read` (a data-plane put whose read
	// consumes the item, say).
	Assert []assertion `json:"assert,omitempty"`
}

type derivedExport struct {
	Export string         `json:"export"`
	Op     string         `json:"op"`
	Params map[string]any `json:"params,omitempty"`
	Path   string         `json:"path"`
}

type readSpec struct {
	Op           string            `json:"op"`
	Params       map[string]any    `json:"params,omitempty"`
	IdentityPath string            `json:"identityPath"`
	Identity     string            `json:"identity,omitempty"`
	Consuming    bool              `json:"consuming,omitempty"`
	Exports      map[string]string `json:"exports,omitempty"`
}

type listSpec struct {
	Comment string         `json:"$comment,omitempty"`
	Op      string         `json:"op"`
	Params  map[string]any `json:"params,omitempty"`
	// ItemsPath is an override. Left out, the generator takes the page from
	// the list operation's own output — the member @paginated names as its
	// `items`, else the sole top-level list — and refuses the list
	// (ambiguous-list-page) where the model does not settle it.
	ItemsPath    string `json:"itemsPath,omitempty"`
	IdentityPath string `json:"identityPath"`
	Identity     string `json:"identity"`
	// Exports are taken from the list test's own response. The list test is
	// the last thing to run before delete, so a handle it re-reads is the
	// freshest one the delete can use (SQS asks a delete to carry the most
	// recent receipt handle).
	Exports map[string]string `json:"exports,omitempty"`
}

type mutation struct {
	Op       string         `json:"op"`
	Params   map[string]any `json:"params,omitempty"`
	Member   string         `json:"member"`
	From     any            `json:"from,omitempty"`
	To       any            `json:"to"`
	ReadPath string         `json:"readPath"`
}

type tagSpec struct {
	Tag   tagCall `json:"tag"`
	Untag tagCall `json:"untag"`
	List  tagList `json:"list"`
}

type tagCall struct {
	Op     string `json:"op"`
	Member string `json:"member"`
}

type tagList struct {
	Op     string         `json:"op"`
	Params map[string]any `json:"params,omitempty"`
	Path   string         `json:"path"`
}

// notFoundSpec is an override. Left out, the generator derives the error from
// the read operation's own modeled errors when exactly one of them is
// not-found-shaped; a value that contradicts such a derivation is an error.
type notFoundSpec struct {
	Error string `json:"error"`
}

type retrySpec struct {
	MaxAttempts int `json:"maxAttempts"`
	DelayMs     int `json:"delayMs"`
}

// authoredOp is coverage written by hand in the IR's own vocabulary, for an
// operation outside the lifecycle vocabulary (PurgeQueue, a batch send). The
// generator still binds its unbound required members, checks every path
// against the model and every $ref against the group's exports, and names
// and registers it — only the assertions are authored.
type authoredOp struct {
	Comment string            `json:"$comment,omitempty"`
	Name    string            `json:"name,omitempty"`
	Op      string            `json:"op"`
	Params  map[string]any    `json:"params,omitempty"`
	Export  map[string]string `json:"export,omitempty"`
	Assert  []assertion       `json:"assert"`
}

// loadRecipes reads every recipe under dir, sorted by file name, validating
// each against the schema and the structural rules the schema cannot express.
func loadRecipes(dir string, schema *schemaSet) ([]recipe, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read recipes %s: %w", dir, err)
	}
	var recipes []recipe
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		r, err := loadRecipe(path, schema)
		if err != nil {
			return nil, err
		}
		if want := strings.TrimSuffix(entry.Name(), ".json"); r.Service != want {
			return nil, fmt.Errorf("%s: service %q must match the file name %q", path, r.Service, want)
		}
		recipes = append(recipes, r)
	}
	sort.Slice(recipes, func(i, j int) bool { return recipes[i].Service < recipes[j].Service })
	return recipes, nil
}

func loadRecipe(path string, schema *schemaSet) (recipe, error) {
	contents, err := os.ReadFile(path)
	if err != nil {
		return recipe{}, fmt.Errorf("read recipe %s: %w", path, err)
	}
	if err := schema.validate(schemaRecipe, contents); err != nil {
		return recipe{}, fmt.Errorf("recipe %s: %w", path, err)
	}
	var r recipe
	if err := decodeStrict(contents, &r); err != nil {
		return recipe{}, fmt.Errorf("recipe %s: %w", path, err)
	}
	if err := r.validate(); err != nil {
		return recipe{}, fmt.Errorf("recipe %s: %w", path, err)
	}
	return r, nil
}

// decodeStrict decodes JSON with unknown fields refused and numbers kept as
// json.Number, so a literal is re-emitted exactly as it was written.
func decodeStrict(contents []byte, into any) error {
	decoder := json.NewDecoder(bytes.NewReader(contents))
	decoder.UseNumber()
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(into); err != nil {
		return err
	}
	if decoder.More() {
		return fmt.Errorf("trailing data after the JSON document")
	}
	return nil
}

// validate checks what the schema cannot: cross-references between resources,
// value grammar, and path syntax. Model-dependent checks happen in the
// generator, where the model is to hand.
func (r recipe) validate() error {
	for _, op := range sortedStringKeys(r.NeverProbe) {
		if strings.TrimSpace(r.NeverProbe[op]) == "" {
			return fmt.Errorf("neverProbe.%s: say why the operation may never be probed; the reason is what gaps.json reports", op)
		}
	}
	for _, op := range sortedStringKeys(r.AllowProbe) {
		if strings.TrimSpace(r.AllowProbe[op]) == "" {
			return fmt.Errorf("allowProbe.%s: say why calling the operation against a live account is safe; the reason is the whole of the exception", op)
		}
		if _, denied := r.NeverProbe[op]; denied {
			return fmt.Errorf("%s is in both neverProbe and allowProbe; decide which", op)
		}
		if isReadVerb(op) {
			return fmt.Errorf("allowProbe.%s: %s is already probeable — its name starts with a read verb — so the entry says nothing; delete it, or move it to neverProbe if it is not in fact a read", op, op)
		}
	}
	ids := make(map[string]int, len(r.Resources))
	for i, res := range r.Resources {
		if _, dup := ids[res.ID]; dup {
			return fmt.Errorf("resource %q declared twice", res.ID)
		}
		ids[res.ID] = i
	}
	for _, res := range r.Resources {
		for _, req := range res.Requires {
			if _, ok := ids[req]; !ok {
				return fmt.Errorf("resource %q requires unknown resource %q", res.ID, req)
			}
			if req == res.ID {
				return fmt.Errorf("resource %q requires itself", res.ID)
			}
		}
		if err := res.validate(); err != nil {
			return fmt.Errorf("resource %q: %w", res.ID, err)
		}
	}
	if _, err := r.topological(); err != nil {
		return err
	}
	return nil
}

func (res resource) validate() error {
	if res.Create == nil {
		if res.Read == nil || len(res.Read.Exports) == 0 {
			return fmt.Errorf("a resource without create is pre-existing and needs a read with exports")
		}
		if res.Delete != nil || len(res.Exports) > 0 || len(res.Derived) > 0 || res.Read.Consuming {
			return fmt.Errorf("a pre-existing resource has no delete, no create exports, no derived exports and no consuming read")
		}
	} else {
		if err := validateValue(res.Create.Params, "create.params"); err != nil {
			return err
		}
		for i, a := range res.Create.Assert {
			if err := validateAuthoredAssertion(a, fmt.Sprintf("create.assert[%d]", i)); err != nil {
				return err
			}
		}
	}
	for name, path := range res.Exports {
		if _, err := parsePath(path); err != nil {
			return fmt.Errorf("exports.%s: %w", name, err)
		}
	}
	for i, d := range res.Derived {
		if err := validateValue(d.Params, fmt.Sprintf("derived[%d].params", i)); err != nil {
			return err
		}
		if _, err := parsePath(d.Path); err != nil {
			return fmt.Errorf("derived[%d]: %w", i, err)
		}
		if _, dup := res.Exports[d.Export]; dup {
			return fmt.Errorf("derived[%d] re-exports %q, which create already exports", i, d.Export)
		}
	}
	for _, member := range sortedStringKeys(res.Binds) {
		if ref := res.Binds[member].Ref; !validContextPath(ref) {
			return fmt.Errorf("binds.%s: %q is not a context path", member, ref)
		}
	}
	reads := append([]readSpec(nil), res.Reads...)
	if res.Read != nil {
		reads = append(reads, *res.Read)
	}
	for i, rd := range reads {
		if err := validateValue(rd.Params, fmt.Sprintf("read[%d].params", i)); err != nil {
			return err
		}
		if _, err := parsePath(rd.IdentityPath); err != nil {
			return fmt.Errorf("read %s: %w", rd.Op, err)
		}
		if rd.Identity != "" && !res.exportsName(rd.Identity) {
			return fmt.Errorf("read %s: identity %q is not an export of this resource", rd.Op, rd.Identity)
		}
		for name, path := range rd.Exports {
			if _, err := parsePath(path); err != nil {
				return fmt.Errorf("read %s exports.%s: %w", rd.Op, name, err)
			}
		}
	}
	if res.List != nil {
		if err := validateValue(res.List.Params, "list.params"); err != nil {
			return err
		}
		// itemsPath is optional: an omitted one is derived from the list
		// operation's own output, which needs the model and so happens in
		// the generator.
		if res.List.ItemsPath != "" {
			if _, err := parsePath(res.List.ItemsPath); err != nil {
				return fmt.Errorf("list: %w", err)
			}
		}
		if _, err := parsePath(res.List.IdentityPath); err != nil {
			return fmt.Errorf("list: %w", err)
		}
		if !res.exportsName(res.List.Identity) {
			return fmt.Errorf("list: identity %q is not an export of this resource", res.List.Identity)
		}
		for name, path := range res.List.Exports {
			if _, err := parsePath(path); err != nil {
				return fmt.Errorf("list exports.%s: %w", name, err)
			}
		}
	}
	for i, m := range res.Mutable {
		if err := validateValue(m.Params, fmt.Sprintf("mutable[%d].params", i)); err != nil {
			return err
		}
		if err := validateValue(m.To, fmt.Sprintf("mutable[%d].to", i)); err != nil {
			return err
		}
		if m.From != nil {
			if err := validateValue(m.From, fmt.Sprintf("mutable[%d].from", i)); err != nil {
				return err
			}
		}
		if _, err := parsePath(m.ReadPath); err != nil {
			return fmt.Errorf("mutable[%d]: %w", i, err)
		}
		if m.Member == "" || strings.HasPrefix(m.Member, ".") || strings.HasSuffix(m.Member, ".") {
			return fmt.Errorf("mutable[%d]: member %q is not a dotted member path", i, m.Member)
		}
	}
	if res.Tags != nil {
		if err := validateValue(res.Tags.List.Params, "tags.list.params"); err != nil {
			return err
		}
		if _, err := parsePath(res.Tags.List.Path); err != nil {
			return fmt.Errorf("tags.list: %w", err)
		}
	}
	if res.Delete != nil {
		if err := validateValue(res.Delete.Params, "delete.params"); err != nil {
			return err
		}
		if len(res.Delete.Assert) > 0 {
			return fmt.Errorf("delete: assertions are derived from notFound and list, not authored")
		}
	}
	if res.Async != nil && (res.Async.MaxAttempts < 1 || res.Async.DelayMs < 0) {
		return fmt.Errorf("async: maxAttempts must be >= 1 and delayMs >= 0")
	}
	names := make(map[string]struct{})
	for i, op := range res.Operations {
		if err := validateValue(op.Params, fmt.Sprintf("operations[%d].params", i)); err != nil {
			return err
		}
		for ctx, path := range op.Export {
			if !validContextPath(ctx) || !strings.HasPrefix(ctx, res.ID+".") {
				return fmt.Errorf("operations[%d] export %q must be a context path under %s.", i, ctx, res.ID)
			}
			if _, err := parsePath(path); err != nil {
				return fmt.Errorf("operations[%d] export %s: %w", i, ctx, err)
			}
		}
		if len(op.Assert) == 0 {
			return fmt.Errorf("operations[%d] (%s): an authored operation needs at least one assertion", i, op.Op)
		}
		for j, a := range op.Assert {
			if err := validateAuthoredAssertion(a, fmt.Sprintf("operations[%d].assert[%d]", i, j)); err != nil {
				return err
			}
		}
		name := op.testName()
		if _, dup := names[name]; dup {
			return fmt.Errorf("operations: test name %q used twice; give one a distinct name", name)
		}
		names[name] = struct{}{}
	}
	return nil
}

// exportsName reports whether the resource exports name anywhere: from its
// create, a derived call, a read, or a call inside an authored assertion.
func (res resource) exportsName(name string) bool {
	if _, ok := res.Exports[name]; ok {
		return true
	}
	if res.Create != nil {
		for _, a := range res.Create.Assert {
			if assertionExports(a, res.ID+"."+name) {
				return true
			}
		}
	}
	for _, op := range res.Operations {
		if _, ok := op.Export[res.ID+"."+name]; ok {
			return true
		}
		for _, a := range op.Assert {
			if assertionExports(a, res.ID+"."+name) {
				return true
			}
		}
	}
	for _, d := range res.Derived {
		if d.Export == name {
			return true
		}
	}
	for _, rd := range res.allReads() {
		if _, ok := rd.Exports[name]; ok {
			return true
		}
	}
	if res.List != nil {
		if _, ok := res.List.Exports[name]; ok {
			return true
		}
	}
	return false
}

func assertionExports(a assertion, ctx string) bool {
	if a.Call != nil {
		if _, ok := a.Call.Export[ctx]; ok {
			return true
		}
	}
	return a.Assert != nil && assertionExports(*a.Assert, ctx)
}

// primaryOp is the operation that stands for the resource in a refusal: its
// create, or its read for a pre-existing resource.
func (res resource) primaryOp() string {
	if res.Create != nil {
		return res.Create.Op
	}
	return res.Read.Op
}

// setupOps are the operations that instantiate the resource.
func (res resource) setupOps() []string {
	if res.Create == nil {
		return []string{res.Read.Op}
	}
	ops := []string{res.Create.Op}
	for _, d := range res.Derived {
		ops = append(ops, d.Op)
	}
	return ops
}

func (res resource) allReads() []readSpec {
	var reads []readSpec
	if res.Read != nil {
		reads = append(reads, *res.Read)
	}
	return append(reads, res.Reads...)
}

// testName is the registry name of an authored operation: the operation
// itself unless the recipe gives a variant name.
func (a authoredOp) testName() string {
	if a.Name != "" {
		return a.Name
	}
	return a.Op
}

// validateAuthoredAssertion checks an assertion written in a recipe: the IR
// rules plus the value grammar of every expression inside it.
func validateAuthoredAssertion(a assertion, where string) error {
	if err := validateAssertion(a); err != nil {
		return fmt.Errorf("%s: %w", where, err)
	}
	if a.Call != nil {
		if err := validateValue(a.Call.Params, where+".call.params"); err != nil {
			return err
		}
		for ctx, path := range a.Call.Export {
			if !validContextPath(ctx) {
				return fmt.Errorf("%s: export %q is not a context path", where, ctx)
			}
			if _, err := parsePath(path); err != nil {
				return fmt.Errorf("%s: export %s: %w", where, ctx, err)
			}
		}
	}
	for path, c := range a.Checks {
		if _, err := parsePath(path); err != nil {
			return fmt.Errorf("%s: %w", where, err)
		}
		if c.Equals != nil {
			if err := validateValue(c.Equals, where+".checks."+path); err != nil {
				return err
			}
		}
		if c.Matches != "" {
			if _, ok := patternMatches(c.Matches, ""); !ok {
				return fmt.Errorf("%s: matches %q is not a valid RE2 pattern", where, c.Matches)
			}
		}
	}
	if a.ItemsPath != "" {
		if _, err := parsePath(a.ItemsPath); err != nil {
			return fmt.Errorf("%s: %w", where, err)
		}
	}
	for path, value := range a.Where {
		if _, err := parsePath(path); err != nil {
			return fmt.Errorf("%s: where: %w", where, err)
		}
		if err := validateValue(value, where+".where."+path); err != nil {
			return err
		}
	}
	if a.Assert != nil {
		return validateAuthoredAssertion(*a.Assert, where+".assert")
	}
	return nil
}

// topological orders resources so every resource follows what it requires.
// Ties keep recipe order, so the output is stable under a reorder of
// unrelated resources only when the recipe itself is reordered.
func (r recipe) topological() ([]resource, error) {
	index := make(map[string]resource, len(r.Resources))
	for _, res := range r.Resources {
		index[res.ID] = res
	}
	var order []resource
	state := make(map[string]int) // 1 visiting, 2 done
	var visit func(id string, trail []string) error
	visit = func(id string, trail []string) error {
		switch state[id] {
		case 2:
			return nil
		case 1:
			return fmt.Errorf("resources require each other in a cycle: %s -> %s", strings.Join(trail, " -> "), id)
		}
		state[id] = 1
		for _, req := range index[id].Requires {
			if err := visit(req, append(trail, id)); err != nil {
				return err
			}
		}
		state[id] = 2
		order = append(order, index[id])
		return nil
	}
	for _, res := range r.Resources {
		if err := visit(res.ID, nil); err != nil {
			return nil, err
		}
	}
	return order, nil
}

// closure returns the resource and everything it transitively requires, in
// topological order with the resource itself last.
func (r recipe) closure(id string) []resource {
	order, _ := r.topological()
	wanted := map[string]bool{id: true}
	index := make(map[string]resource, len(r.Resources))
	for _, res := range r.Resources {
		index[res.ID] = res
	}
	var mark func(id string)
	mark = func(id string) {
		for _, req := range index[id].Requires {
			if !wanted[req] {
				wanted[req] = true
				mark(req)
			}
		}
	}
	mark(id)
	var out []resource
	for _, res := range order {
		if wanted[res.ID] {
			out = append(out, res)
		}
	}
	return out
}
