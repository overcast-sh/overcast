//go:build dev

package main

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// Parameter binding — docs/plans/compat-coverage-modelgen.md §3.3.
//
// For each modeled required input member the recipe did not supply, in
// member-name order:
//
//  1. an explicit `binds` entry on an in-scope resource → bind;
//  2. an exact name match against an in-scope export → bind, recorded as an
//     automatic binding for the review report;
//  3. a curated literal in values.json;
//  4. a constraint-derived synthetic value, scalars only: the first enum
//     value, false for a boolean, the range minimum for a number;
//  5. otherwise refuse, with the member named in the reason.
//
// Optional members are never bound. Guessing is not an option at any step: a
// string with no enum is refused rather than invented, because an invented
// string is legal on the emulator far more often than on AWS. §3.3 rule 4 also
// lists "the shortest legal string for a pattern"; that one is deliberately
// not implemented — see synthesize.
//
// Rules 1 and 2 are switched off inside a probe group. A probe calls an
// operation the emulator does not implement, so it is the one generated call
// whose effect no create/delete pair contains; pointing it at a resource the
// run actually owns is how an irreversible operation (CloseAccount,
// MoveAccount) ends up aimed at real infrastructure the moment a suite runs
// against AWS. A probe therefore binds only curated or synthetic literals —
// syntactically valid and deliberately nonexistent — and is refused
// (probe-binds-live-resource) when a live export is the only thing that would
// have supplied a member.

// refusal is why an operation was not generated. Reason is machine-readable
// and stable; Detail is for the human reading gaps.json.
type refusal struct {
	Reason string
	Detail string
}

func refuse(reason, detail string) *refusal { return &refusal{Reason: reason, Detail: detail} }

// Refusal reasons.
const (
	reasonUnboundRequiredMember  = "unbound-required-member"
	reasonUpdateWithoutMutable   = "update-without-mutable"
	reasonUpdateWithoutReadback  = "update-without-readback"
	reasonNoReadbackPath         = "no-readback-path"
	reasonProbeOfImplementedOp   = "probe-of-implemented-op"
	reasonProbeBindsLiveResource = "probe-binds-live-resource"
	reasonNeverProbe             = "never-probe"
	reasonNoOutputToAssert       = "no-output-to-assert"
	reasonAmbiguousListPage      = "ambiguous-list-page"
	reasonSetupRefused           = "setup-refused"
	reasonUnsupportedTagShape    = "unsupported-tag-shape"
)

// autoBinding records a rule-2 binding, the riskiest inference the generator
// makes, so review sees every one of them.
type autoBinding struct {
	Group, Op, Member, Ref string
}

// valueUse records a rule-3 or rule-4 binding for the report.
type valueUse struct {
	Group, Op, Member string
	Source            valueSource
	Value             any
}

const valueSynthetic valueSource = "synthetic"

// bindScope is what a call may draw on: the resources whose binds and exports
// are in play, nearest first, and the context paths exported so far. probe
// marks a probe group, where rules 1 and 2 are switched off.
type bindScope struct {
	resources []resource
	exports   exportKinds
	probe     bool
}

// binder binds one service's calls. It is shared across groups; per-group
// state lives in bindScope.
type binder struct {
	model   *serviceModel
	service string
	values  *valuesTable
	auto    []autoBinding
	uses    []valueUse
}

// bind produces the params for op: the explicit params, checked against the
// model, plus a binding for every required member they leave out.
func (b *binder) bind(group, op string, explicit map[string]any, scope bindScope) (map[string]any, *refusal, error) {
	if !b.model.HasOperation(op) {
		return nil, nil, fmt.Errorf("operation %q is not modeled for %s", op, b.service)
	}
	input := b.model.InputShape(op)
	params := make(map[string]any, len(explicit))
	for member, value := range explicit {
		params[member] = cloneValue(value)
	}
	if input == "" {
		if len(params) > 0 {
			return nil, nil, fmt.Errorf("%s takes no input, but the recipe supplies %s", op, sortedKeys(params))
		}
		return params, nil, nil
	}
	for _, member := range sortedKeys(params) {
		target, ok := b.model.MemberTarget(input, member)
		if !ok {
			return nil, nil, fmt.Errorf("%s has no input member %q", op, member)
		}
		if err := b.checkValue(params[member], target, scope.exports, op+"."+member, group); err != nil {
			return nil, nil, err
		}
	}
	for _, member := range b.model.RequiredMembers(input) {
		if _, present := params[member]; present {
			continue
		}
		target, _ := b.model.MemberTarget(input, member)
		value, ref := b.bindMember(group, op, member, target, scope)
		if ref != nil {
			return nil, ref, nil
		}
		if err := b.checkValue(value, target, scope.exports, op+"."+member, group); err != nil {
			return nil, nil, err
		}
		params[member] = value
	}
	return params, nil, nil
}

func (b *binder) bindMember(group, op, member, target string, scope bindScope) (any, *refusal) {
	// Rule 1: an explicit bind, nearest resource first. A bind whose export
	// is not in scope yet falls through to the later rules; if nothing else
	// supplies the member, the refusal names the bind. live records what a
	// rule-1 or rule-2 binding would have been inside a probe group, where
	// both rules are off.
	//
	// A bind the recipe wrapped in a list supplies the same reference inside
	// a one-element list, for a member the service models as a list of what
	// the export names (ELB Classic's LoadBalancerNames). The element's kind
	// is not checked here: bind hands every value it produces to checkValue,
	// which walks a list target into its member shape and compares the $ref's
	// exported kind there. A wrap that contradicts the model — a list bind on
	// a scalar member, or an export of the wrong kind — is therefore an error
	// naming the member, exactly as a mistyped literal in `params` is, and
	// not a refusal.
	var unavailable, live string
	for _, res := range scope.resources {
		bind, ok := res.Binds[member]
		if !ok {
			continue
		}
		if scope.probe {
			if live == "" {
				live = bind.String()
			}
			continue
		}
		if _, available := scope.exports[bind.Ref]; available {
			return bind.value(), nil
		}
		if unavailable == "" {
			unavailable = bind.String()
		}
	}
	// Rule 2: an export with exactly the member's name.
	for _, res := range scope.resources {
		ref := res.ID + "." + member
		if !res.exportsName(member) {
			continue
		}
		if scope.probe {
			if live == "" {
				live = ref
			}
			continue
		}
		if _, available := scope.exports[ref]; !available {
			continue
		}
		b.auto = append(b.auto, autoBinding{Group: group, Op: op, Member: member, Ref: ref})
		return map[string]any{"$ref": ref}, nil
	}
	// Rule 3: a curated literal.
	if value, source, ok := b.values.lookup(b.service, op, member, target); ok {
		b.uses = append(b.uses, valueUse{Group: group, Op: op, Member: member, Source: source, Value: value})
		return value, nil
	}
	// Rule 4: a constraint-derived scalar.
	if value, ok := b.synthesize(target); ok {
		b.uses = append(b.uses, valueUse{Group: group, Op: op, Member: member, Source: valueSynthetic, Value: value})
		return value, nil
	}
	// Rule 5: refuse.
	if live != "" {
		return nil, refuse(reasonProbeBindsLiveResource+":"+member,
			fmt.Sprintf("%s.%s would bind to %s, a value exported from a resource the run owns; a probe calls an operation the emulator does not implement, so against real AWS it would act on that resource for real. Add a curated literal for %s to values.json, or leave the operation refused", op, member, live, member))
	}
	if unavailable != "" {
		return nil, refuse(reasonUnboundRequiredMember+":"+member,
			fmt.Sprintf("%s.%s is bound to %s, which nothing exports before this call, and no curated value stands in", op, member, unavailable))
	}
	return nil, refuse(reasonUnboundRequiredMember+":"+member,
		fmt.Sprintf("%s.%s (%s) has no bind, no matching export, no curated value and no derivable literal", op, member, b.describeShape(target)))
}

// synthesize derives a legal literal from constraints alone. Only shapes whose
// legal values are enumerable or bounded qualify.
//
// Two decisions are deliberate. A required boolean synthesises to false: the
// shape has exactly two legal values, both model-valid, and false is the one
// that asks the service to do less (no DryRun, no force, no cascade), so it is
// the conservative half of an exhaustive choice rather than a guess. And §3.3
// rule 4's "shortest legal string for a pattern" is not implemented: a pattern
// constrains a string's *syntax*, never its *reference*, so the shortest match
// for ^arn:aws:.* is a well-formed ARN of something that does not exist and
// may not even be the right service — the emulator accepts far more of those
// than AWS does, which is exactly the class of value §3.10 says belongs in the
// gap report. Such a member is refused and a human writes the literal.
func (b *binder) synthesize(target string) (any, bool) {
	switch b.model.Kind(target) {
	case "enum":
		values := b.model.EnumValues(target)
		if len(values) > 0 {
			return values[0], true
		}
	case "boolean":
		return false, true
	case "integer", "float":
		if c := b.model.Constraints(target); c.RangeMin != nil {
			return json.Number(c.RangeMin.String()), true
		}
	}
	return nil, false
}

func (b *binder) describeShape(target string) string {
	kind := b.model.Kind(target)
	if strings.HasPrefix(target, "smithy.api#") {
		return kind
	}
	return fmt.Sprintf("%s, a %s", target, kind)
}

// checkValue proves a value is legal for the shape it is sent as: the right
// JSON kind, inside the model's constraints, and — for a $ref — exported with
// a compatible kind. A violation is an error, not a refusal: the value came
// from a curated file, so the file is wrong.
func (b *binder) checkValue(v any, target string, exports exportKinds, where, group string) error {
	kind := b.model.Kind(target)
	if key, arg, isExpr := exprOf(v); isExpr {
		switch key {
		case "$lit":
			return b.checkValue(arg, target, exports, where, group)
		case "$ref":
			ref := arg.(string)
			refKind, known := exports[ref]
			if !known {
				return fmt.Errorf("%s: $ref %s is not exported before this call in group %s", where, ref, group)
			}
			if !kindsCompatible(refKind, kind) {
				return fmt.Errorf("%s: $ref %s is a %s but the member is a %s", where, ref, refKind, kind)
			}
			return nil
		case "$name":
			if kind != "string" {
				return fmt.Errorf("%s: $name yields a string but the member is a %s", where, kind)
			}
			return b.checkName(arg.(string), target, where, group)
		case "$concat":
			if kind != "string" {
				return fmt.Errorf("%s: $concat yields a string but the member is a %s", where, kind)
			}
			for i, part := range arg.([]any) {
				if _, isString := part.(string); isString {
					continue
				}
				if err := b.checkValue(part, "smithy.api#String", exports, fmt.Sprintf("%s.$concat[%d]", where, i), group); err != nil {
					return err
				}
			}
			return nil
		case "$index":
			return nil
		}
	}
	switch kind {
	case "timestamp", "blob", "document":
		// The SDKs disagree on how these are passed (a Date, a datetime, a
		// Buffer, a string), and an interpreter has no model at run time to
		// convert with, so the IR carries no literal of these kinds at all.
		return fmt.Errorf("%s: %s is a %s, for which the IR has no portable literal; leave the member unbound so the operation is refused", where, target, kind)
	case "string", "enum":
		s, ok := v.(string)
		if !ok {
			return fmt.Errorf("%s: %s wants a string, got %s", where, b.describeShape(target), describeJSON(v))
		}
		return b.checkString(s, target, where)
	case "integer", "float":
		if literalKind(v, nil) != "integer" && !(kind == "float" && literalKind(v, nil) == "float") {
			return fmt.Errorf("%s: %s wants a number, got %s", where, b.describeShape(target), describeJSON(v))
		}
		// literalKind also classifies a float64 as a number, but every literal
		// the generator handles is decoded with UseNumber, so anything else
		// here is a value that came in some other way and must not be checked
		// as if it had.
		number, ok := v.(json.Number)
		if !ok {
			return fmt.Errorf("%s: %s wants a number decoded as json.Number, got %T; decode the document with decodeStrict", where, b.describeShape(target), v)
		}
		return b.checkNumber(number, target, where)
	case "boolean":
		if _, ok := v.(bool); !ok {
			return fmt.Errorf("%s: %s wants a boolean, got %s", where, b.describeShape(target), describeJSON(v))
		}
	case "list":
		items, ok := v.([]any)
		if !ok {
			return fmt.Errorf("%s: %s wants a list, got %s", where, b.describeShape(target), describeJSON(v))
		}
		c := b.model.Constraints(target)
		if c.LengthMin != nil && int64(len(items)) < *c.LengthMin || c.LengthMax != nil && int64(len(items)) > *c.LengthMax {
			return fmt.Errorf("%s: %s has %d items, outside the modeled length", where, target, len(items))
		}
		for i, item := range items {
			if err := b.checkValue(item, b.model.Shapes[target].Member, exports, fmt.Sprintf("%s[%d]", where, i), group); err != nil {
				return err
			}
		}
	case "map":
		object, ok := v.(map[string]any)
		if !ok {
			return fmt.Errorf("%s: %s wants an object, got %s", where, b.describeShape(target), describeJSON(v))
		}
		shape := b.model.Shapes[target]
		for _, key := range sortedKeys(object) {
			if err := b.checkString(key, shape.Key, where+"."+key+" (key)"); err != nil {
				return err
			}
			if err := b.checkValue(object[key], shape.Value, exports, where+"."+key, group); err != nil {
				return err
			}
		}
	case "structure", "union":
		object, ok := v.(map[string]any)
		if !ok {
			return fmt.Errorf("%s: %s wants an object, got %s", where, b.describeShape(target), describeJSON(v))
		}
		for _, member := range sortedKeys(object) {
			memberTarget, ok := b.model.MemberTarget(target, member)
			if !ok {
				return fmt.Errorf("%s: %s has no member %q", where, target, member)
			}
			if err := b.checkValue(object[member], memberTarget, exports, where+"."+member, group); err != nil {
				return err
			}
		}
		if kind == "structure" {
			for _, required := range b.model.RequiredMembers(target) {
				if _, present := object[required]; !present {
					return fmt.Errorf("%s: %s requires member %q; nested structures are written out in full, not bound", where, target, required)
				}
			}
		}
	}
	return nil
}

func (b *binder) checkString(s, target, where string) error {
	c := b.model.Constraints(target)
	if c.LengthMin != nil && int64(len(s)) < *c.LengthMin || c.LengthMax != nil && int64(len(s)) > *c.LengthMax {
		return fmt.Errorf("%s: %q has length %d, outside %s's modeled length", where, s, len(s), target)
	}
	if b.model.Kind(target) == "enum" {
		values := b.model.EnumValuesSorted(target)
		if i := sort.SearchStrings(values, s); i >= len(values) || values[i] != s {
			return fmt.Errorf("%s: %q is not one of %s's values %s", where, s, target, values)
		}
	}
	if c.Pattern != "" {
		if matched, verifiable := patternMatches(c.Pattern, s); verifiable && !matched {
			return fmt.Errorf("%s: %q does not match %s's pattern %s", where, s, target, c.Pattern)
		}
	}
	return nil
}

func (b *binder) checkNumber(n json.Number, target, where string) error {
	c := b.model.Constraints(target)
	value, err := n.Float64()
	if err != nil {
		return fmt.Errorf("%s: %s is not a number", where, n)
	}
	if c.RangeMin != nil {
		if min, err := c.RangeMin.Float64(); err == nil && value < min {
			return fmt.Errorf("%s: %s is below %s's modeled minimum %s", where, n, target, c.RangeMin)
		}
	}
	if c.RangeMax != nil {
		if max, err := c.RangeMax.Float64(); err == nil && value > max {
			return fmt.Errorf("%s: %s is above %s's modeled maximum %s", where, n, target, c.RangeMax)
		}
	}
	return nil
}

// runIDBudget is the longest run id a suite is handed: "oc-" + 8 hex + "-" +
// a two-letter suite abbreviation (compat/runner.go).
const runIDBudget = len("oc-00000000-xx")

// checkName proves a $name will be legal wherever it is sent: its longest
// possible rendering fits the member's length, and a representative rendering
// matches the member's pattern.
func (b *binder) checkName(suffix, target, where, group string) error {
	sample := strings.Repeat("x", runIDBudget) + "-" + group + "-" + suffix
	c := b.model.Constraints(target)
	if c.LengthMax != nil && int64(len(sample)) > *c.LengthMax {
		return fmt.Errorf("%s: a $name here renders as up to %d characters (%s), over %s's modeled maximum of %d — shorten the suffix or the group name", where, len(sample), sample, target, *c.LengthMax)
	}
	if c.Pattern != "" {
		if matched, verifiable := patternMatches(c.Pattern, sample); verifiable && !matched {
			return fmt.Errorf("%s: a $name renders like %s, which does not match %s's pattern %s", where, sample, target, c.Pattern)
		}
	}
	return nil
}

// kindsCompatible says whether a value of one model kind may be sent as
// another: identical kinds, or an enum carried as a string.
func kindsCompatible(have, want string) bool {
	if have == "" || have == want {
		return true
	}
	stringy := map[string]bool{"string": true, "enum": true}
	return stringy[have] && stringy[want]
}

func describeJSON(v any) string {
	switch v.(type) {
	case string:
		return "a string"
	case bool:
		return "a boolean"
	case json.Number, float64:
		return "a number"
	case []any:
		return "a list"
	case map[string]any:
		return "an object"
	case nil:
		return "null"
	}
	return fmt.Sprintf("%T", v)
}
