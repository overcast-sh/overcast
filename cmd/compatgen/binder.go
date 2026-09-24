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
	reasonNoPortableValue        = "no-portable-value"
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
			if r := b.unportable(op, member, target, bind.String()); r != nil {
				return nil, r
			}
			return b.blobSafe(bind.value(), target), nil
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
		if r := b.unportable(op, member, target, ref); r != nil {
			return nil, r
		}
		b.auto = append(b.auto, autoBinding{Group: group, Op: op, Member: member, Ref: ref})
		return b.blobSafe(map[string]any{"$ref": ref}, target), nil
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

// unportableKinds are the modeled kinds the IR has no value for at all: no
// literal, and no expression a backend can hand its SDK. A timestamp is a Date,
// a datetime, a time.Time or an Instant depending on who is asked, a document
// is an SDK-specific tree, and a union is a tagged variant each typed SDK spells
// as a type of its own; an interpreter has no model at run time to convert a
// JSON value into any of them, and no typed emitter can spell one either.
//
// A blob is not here: it has `$base64` (compat/model/README.md § Values).
var unportableKinds = map[string]bool{"timestamp": true, "document": true, "union": true}

// unportable refuses a rule-1 or rule-2 binding into a member of a kind the IR
// cannot carry. Without it such a $ref passes checkValue — both sides are a
// timestamp, so the kinds agree — and is refused by every source emitter
// instead, which scopes the whole group away from four suites rather than
// recording one gap (#1910). The refusal is the binder's, so it lands in
// gaps.json beside every other reason an operation was not generated.
func (b *binder) unportable(op, member, target, ref string) *refusal {
	kind := b.model.Kind(target)
	if !unportableKinds[kind] {
		return nil
	}
	return refuse(reasonNoPortableValue+":"+member,
		fmt.Sprintf("%s.%s (%s) would bind to %s, but the IR has no %s value every backend can send: an interpreter has no model to convert JSON into one, and no typed emitter can spell one", op, member, b.describeShape(target), ref, kind))
}

// blobSafe wraps a bound $ref in `$base64` where the member — or, for a
// list-wrapped bind, its element — is a blob. An exported blob is its base64
// text in every backend's context bag, so a bare $ref would hand an interpreter
// a string, and boto3 and the JS SDK would send that string's UTF-8 bytes rather
// than the bytes it spells. `$base64` is what says "decode this" — see
// checkBlob.
func (b *binder) blobSafe(v any, target string) any {
	switch b.model.Kind(target) {
	case "blob":
		if key, _, ok := exprOf(v); ok && key == "$ref" {
			return map[string]any{"$base64": v}
		}
	case "list":
		if items, ok := v.([]any); ok {
			element := b.model.Shapes[target].Member
			out := make([]any, len(items))
			for i, item := range items {
				out[i] = b.blobSafe(item, element)
			}
			return out
		}
	}
	return v
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
	if kind == "blob" {
		return checkBlob(b.model, v, target, exports, where)
	}
	if key, arg, isExpr := exprOf(v); isExpr {
		if key == "$base64" {
			return fmt.Errorf("%s: $base64 is bytes, for a blob member, but %s is a %s", where, b.describeShape(target), kind)
		}
		if key == "$ref" && unportableKinds[kind] {
			return fmt.Errorf("%s: %s is a %s, for which the IR has no portable value, not even a $ref; leave the member unbound so the operation is refused", where, target, kind)
		}
		if key == "$now" {
			return checkNowTarget(b.model, target, where)
		}
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
	case "timestamp", "document", "union":
		// The SDKs disagree on how these are passed (a Date, a datetime, a
		// time.Time; an SDK-specific document tree; a variant type of each
		// SDK's own), and an interpreter has no model at run time to convert
		// with, so the IR carries no literal of these kinds at all.
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
	case "structure":
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
		for _, required := range b.model.RequiredMembers(target) {
			if _, present := object[required]; !present {
				return fmt.Errorf("%s: %s requires member %q; nested structures are written out in full, not bound", where, target, required)
			}
		}
	}
	return nil
}

// checkBlob holds a value for a blob member — an input member, a list element
// or map value inside one, or the expected side of an `equals` or `where` that
// resolves to a blob — to the one spelling the IR has for bytes: `$base64`,
// holding either the base64 text of a literal or a $ref to a blob a previous
// call exported (compat/model/README.md § Values).
//
// A plain string is an error rather than something to accept, because the
// backends disagree about what it would mean: aws-cli v2 reads a blob in
// --cli-input-json as base64, so `"record-1"` fails there with "Invalid
// base64", while boto3 and the JS SDK send the string's UTF-8 bytes. No single
// string puts the same bytes on the wire everywhere, and `$base64` does. A bare
// $ref fails for the same reason: an exported blob is base64 text in every
// context bag, so the interpreters would send the text's bytes, not the blob's.
//
// It is a function of the model rather than a binder method because authored
// scenarios, which have no binder, are held to it too (authored.go).
func checkBlob(model *serviceModel, v any, target string, exports exportKinds, where string) error {
	key, arg, isExpr := exprOf(v)
	if !isExpr || key != "$base64" {
		return fmt.Errorf(`%s: %s is a blob; write its bytes as {"$base64": "<standard base64>"}, or {"$base64": {"$ref": "<context path>"}} for a blob a previous call exported — got %s, which the backends do not agree on (the AWS CLI reads a blob string as base64, boto3 and the JS SDK send its UTF-8 bytes)`,
			where, bareShapeName(target), describeBlobValue(v))
	}
	switch inner := arg.(type) {
	case string:
		raw, err := decodeBase64(inner)
		if err != nil {
			return fmt.Errorf("%s: $base64 %q %w", where, inner, err)
		}
		c := model.Constraints(target)
		if c.LengthMin != nil && int64(len(raw)) < *c.LengthMin || c.LengthMax != nil && int64(len(raw)) > *c.LengthMax {
			return fmt.Errorf("%s: $base64 %q is %d bytes, outside %s's modeled length", where, inner, len(raw), bareShapeName(target))
		}
	case map[string]any:
		refKey, refArg, ok := exprOf(inner)
		if !ok || refKey != "$ref" {
			return fmt.Errorf("%s: $base64 takes a base64 string or a $ref, got %s", where, valueKind(inner))
		}
		ref := refArg.(string)
		refKind, known := exports[ref]
		if !known {
			return fmt.Errorf("%s: $ref %s is not exported before this call", where, ref)
		}
		if refKind != "" && refKind != "blob" {
			return fmt.Errorf("%s: $base64 decodes an exported blob, but $ref %s is a %s", where, ref, refKind)
		}
	default:
		return fmt.Errorf("%s: $base64 takes a base64 string or a $ref, got %s", where, valueKind(inner))
	}
	return nil
}

// checkNowTarget holds a `$now` to the one kind of member it may be sent as: a
// `long`, which is how a model spells an epoch it counts itself (CloudWatch
// Logs' InputLogEvent.timestamp). Every backend sends that as a plain integer,
// so the instant reaches the wire unchanged everywhere.
//
// A `timestamp` member is refused, deliberately. Each SDK takes its own type
// for one — a datetime, a Date, a time.Time, an Instant, a DateTime, a smithy
// DateTime, ISO text for the CLI — so lifting `no-portable-value` for it would
// be seven new spellings that no scenario yet needs and none would exercise.
// An int or a short is refused because epoch milliseconds do not fit one.
//
// It is a function of the model rather than a binder method because authored
// scenarios, which have no binder, are held to it too (authored.go).
func checkNowTarget(model *serviceModel, target, where string) error {
	if model.Kind(target) == "integer" && model.ShapeType(target) == "long" {
		return nil
	}
	shape := bareShapeName(target)
	if strings.HasPrefix(target, "smithy.api#") {
		shape = model.ShapeType(target)
	}
	if model.Kind(target) == "timestamp" {
		return fmt.Errorf("%s: $now is epoch milliseconds, for a long member, but %s is a timestamp, which has no portable value — not even the clock (compat/model/README.md § Values)", where, shape)
	}
	return fmt.Errorf("%s: $now is epoch milliseconds, for a long member, but %s is a %s", where, shape, model.ShapeType(target))
}

// describeBlobValue names what a blob member was wrongly given, for checkBlob's
// message.
func describeBlobValue(v any) string {
	if key, _, ok := exprOf(v); ok {
		if key == "$ref" {
			return "a bare $ref, which hands an interpreter the exported blob's base64 text rather than its bytes"
		}
		return "a " + key + " expression"
	}
	if s, ok := v.(string); ok {
		return fmt.Sprintf("the string %q", s)
	}
	return describeJSON(v)
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
