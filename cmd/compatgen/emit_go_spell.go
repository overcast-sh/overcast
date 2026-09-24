//go:build dev

package main

import (
	"fmt"
	"go/types"
	"math"
	"strconv"
)

// The type-spelling table: one IR value plus the SDK's own field type, as Go
// source.
//
// This is the whole of what the go-sdk backend knows about writing typed Go,
// and both writers go through it — emit_go.go's Build bodies and
// `-explain -lang go` (explain_typed.go), which share goInputLines. There is
// no second description of a call anywhere.
//
//	field type              value          emitted
//	---------------------   ------------   ----------------------------------
//	*string                 "blue"         aws.String("blue")
//	string                  "blue"         "blue"
//	int32                   30             30
//	*int32                  30             aws.Int32(30)
//	types.QueueAttributeName "All"         types.QueueAttributeName("All")
//	[]types.QueueAttributeName ["All"]     []types.QueueAttributeName{"All"}
//	map[string]string       {"a":"b"}      map[string]string{"a": "b"}
//	[]types.Tag             [{"Key":"k"}]  []types.Tag{{Key: aws.String("k")}}
//	*types.Tag              {"Key":"k"}    &types.Tag{Key: aws.String("k")}
//	*string                 {"$ref":"q"}   aws.String(scenario.Bind[string](b, "M", scenario.Ref("q")))
//	types.PolicyType        {"$ref":"t"}   types.PolicyType(scenario.Bind[string](b, "M", scenario.Ref("t")))
//	[]byte                  {"$base64":"cmVjb3JkLTE="}            []byte("record-1")
//	[]byte                  {"$base64":{"$ref":"k"}}  scenario.Blob(b, "M", scenario.Base64(scenario.Ref("k")))
//	*int64                  {"$now":{"unit":"epochMillis","offsetMillis":-1}}
//	                                       aws.Int64(scenario.Bind[int64](b, "M", scenario.Now("epochMillis", -1)))
//
// Two rules explain most of the table:
//
//   - **Inside a composite literal the element type is already written**, so a
//     constant element is spelled bare and a struct element drops its type
//     name — `[]types.Tag{{Key: …}}`, not `[]types.Tag{types.Tag{Key: …}}`.
//     A value that is not a constant does not get that: a Bind result is a
//     `string`, not a `types.QueueAttributeName`, so it keeps its conversion.
//   - **A value expression is deferred, so it cannot be a constant.** It
//     becomes `scenario.Bind[T](b, member, <expr>)` — evaluated when the call
//     is built, converted to the one scalar type the field wants, and
//     recorded on the binder if it cannot be. That is the only run-time step
//     left in building an input, and it does no reflection.
//
// Everything this cannot spell is refused at generation time into gaps.json,
// which is the property #1831 exists to get: a member the SDK does not have,
// or has under a type with no literal, stops the group from being emitted
// instead of compiling and failing on the wire.

// goAWSModule is the SDK's pointer-helper package.
const goAWSModule = "github.com/aws/aws-sdk-go-v2/aws"

// goScalarKinds is the scalar half of the table. A key is a predeclared basic
// type smithy-go gives a modeled scalar member; the value is both the
// `aws.<Fn>` that returns a pointer to it and the type parameter
// `scenario.Bind` is instantiated with — the aws helper set and the Bind
// constraint cover the same ground for the same reason, so one map serves.
//
// smithy-go's mapping is byte→int8, short→int16, integer→int32, long→int64,
// float→float32, double→float64; bigInteger and bigDecimal have no Go scalar
// and are refused, as is a timestamp, whose *time.Time has no IR literal.
var goScalarKinds = map[types.BasicKind]string{
	types.String:  "String",
	types.Bool:    "Bool",
	types.Int8:    "Int8",
	types.Int16:   "Int16",
	types.Int32:   "Int32",
	types.Int64:   "Int64",
	types.Float32: "Float32",
	types.Float64: "Float64",
}

// goSpeller renders one service's values against that service's SDK types.
//
// It records which of the two optional imports the file it is writing turned
// out to need, so an emitted file never carries an unused import. That makes
// an instance single-use: refusal detection runs over a throwaway one, because
// a group that is refused must not leave its import behind.
type goSpeller struct {
	svc       *goSDKService
	usesAWS   bool
	usesTypes bool
}

// field resolves the SDK field a modeled member is assigned through.
func (sp *goSpeller) field(op, member string) (*types.Var, error) {
	input, err := sp.svc.Input(op)
	if err != nil {
		return nil, err
	}
	f, ok := goSDKField(input, member)
	if !ok {
		return nil, fmt.Errorf("%s.%sInput has no field for member %q; smithy-go renamed or dropped it", sp.svc.Name, op, member)
	}
	return f, nil
}

// value renders one IR value as a Go expression of type t.
//
// member is the modeled member name the value belongs to, which a deferred
// expression carries into its failure message. elide says the expression will
// sit inside a composite literal that already states t.
func (sp *goSpeller) value(t types.Type, v any, member, indent string, elide bool) (string, error) {
	t = types.Unalias(t)
	if goIsBytes(t) {
		return sp.blob(v, member, indent)
	}
	if _, _, isExpr := exprOf(v); isExpr {
		return sp.expr(t, v, member, indent)
	}
	if v == nil {
		switch t.Underlying().(type) {
		case *types.Pointer, *types.Slice, *types.Map, *types.Interface:
			return "nil", nil
		}
		name, _ := sp.typeName(t)
		return "", fmt.Errorf("null cannot be written into %s, which has no nil", name)
	}
	switch u := t.Underlying().(type) {
	case *types.Basic:
		return sp.basic(t, u, v, elide)
	case *types.Pointer:
		return sp.pointer(u, v, member, indent, elide)
	case *types.Slice:
		return sp.slice(u, v, member, indent)
	case *types.Map:
		return sp.mapping(u, v, member, indent)
	case *types.Struct:
		return sp.structure(t, u, v, member, indent, elide)
	}
	name, err := sp.typeName(t)
	if err != nil {
		name = t.String()
	}
	return "", fmt.Errorf("no Go literal builds a %s", name)
}

// basic renders a scalar into a value-typed field.
//
// A value-typed member set to its zero value is refused rather than emitted:
// smithy-go serializes such a field only when it differs from the zero value,
// so the request would silently leave the member out and the backends would
// disagree about what the service was told. compat/model/README.md § Values
// states the rule for authors; this is where it is enforced for this backend.
func (sp *goSpeller) basic(t types.Type, u *types.Basic, v any, elide bool) (string, error) {
	lit, err := goBasicLiteral(u, v)
	if err != nil {
		return "", err
	}
	if lit == goZeroLiteral(u) {
		name, nameErr := sp.typeName(t)
		if nameErr != nil {
			name = u.Name()
		}
		return "", fmt.Errorf("the SDK gives this member the value type %s, and serializes it only when it is not %s, so setting it to %s would send nothing (compat/model/README.md § Values)", name, lit, lit)
	}
	if elide {
		return lit, nil
	}
	if _, unnamed := t.(*types.Basic); unnamed {
		return lit, nil
	}
	name, err := sp.typeName(t)
	if err != nil {
		return "", err
	}
	return name + "(" + lit + ")", nil
}

// pointer renders a value into a pointer field: aws.String and friends for a
// scalar, an address-of composite for a nested structure.
func (sp *goSpeller) pointer(u *types.Pointer, v any, member, indent string, elide bool) (string, error) {
	elem := types.Unalias(u.Elem())
	switch eu := elem.Underlying().(type) {
	case *types.Basic:
		if _, unnamed := elem.(*types.Basic); !unnamed {
			// aws.String takes a string, not a named string type, and there is
			// no other one-call spelling. smithy-go does not generate a
			// pointer to an enum — an enum's zero value is the empty string,
			// which already means "unset" — so nothing reaches this in
			// practice; refusing beats emitting source that will not compile.
			name, _ := sp.typeName(elem)
			return "", fmt.Errorf("no aws helper returns a *%s; the table spells a pointer only to a predeclared basic type", name)
		}
		fn, ok := goScalarKinds[eu.Kind()]
		if !ok {
			return "", fmt.Errorf("no aws helper returns a *%s", eu.Name())
		}
		lit, err := goBasicLiteral(eu, v)
		if err != nil {
			return "", err
		}
		sp.usesAWS = true
		return "aws." + fn + "(" + lit + ")", nil
	case *types.Struct:
		inner, err := sp.value(elem, v, member, indent, elide)
		if err != nil {
			return "", err
		}
		if elide {
			// []*types.Tag{{…}}: the composite states the pointer type, and Go
			// takes the address for us.
			return inner, nil
		}
		return "&" + inner, nil
	}
	name, err := sp.typeName(u)
	if err != nil {
		name = u.String()
	}
	return "", fmt.Errorf("no Go literal builds a %s", name)
}

func (sp *goSpeller) slice(u *types.Slice, v any, member, indent string) (string, error) {
	elem := types.Unalias(u.Elem())
	items, ok := v.([]any)
	if !ok {
		return "", fmt.Errorf("a list member wants a JSON array, got %s", valueKind(v))
	}
	name, err := sp.typeName(u)
	if err != nil {
		return "", err
	}
	rendered := make([]string, 0, len(items))
	for _, item := range items {
		out, err := sp.value(elem, item, member, indent+"\t", true)
		if err != nil {
			return "", err
		}
		rendered = append(rendered, out)
	}
	return goComposite(name+"{", rendered, indent), nil
}

func (sp *goSpeller) mapping(u *types.Map, v any, member, indent string) (string, error) {
	key := types.Unalias(u.Key())
	if b, ok := key.Underlying().(*types.Basic); !ok || b.Info()&types.IsString == 0 {
		name, _ := sp.typeName(key)
		return "", fmt.Errorf("a map member keyed by %s has no IR spelling; the IR's objects have string keys", name)
	}
	entries, ok := v.(map[string]any)
	if !ok {
		return "", fmt.Errorf("a map member wants a JSON object, got %s", valueKind(v))
	}
	name, err := sp.typeName(u)
	if err != nil {
		return "", err
	}
	rendered := make([]string, 0, len(entries))
	for _, k := range sortedKeys(entries) {
		out, err := sp.value(types.Unalias(u.Elem()), entries[k], member, indent+"\t", true)
		if err != nil {
			return "", err
		}
		rendered = append(rendered, strconv.Quote(k)+": "+out)
	}
	return goComposite(name+"{", rendered, indent), nil
}

func (sp *goSpeller) structure(t types.Type, u *types.Struct, v any, member, indent string, elide bool) (string, error) {
	members, ok := v.(map[string]any)
	if !ok {
		return "", fmt.Errorf("a structure member wants a JSON object, got %s", valueKind(v))
	}
	name, err := sp.typeName(t)
	if err != nil {
		return "", err
	}
	rendered := make([]string, 0, len(members))
	for _, k := range sortedKeys(members) {
		f, found := goSDKField(u, k)
		if !found {
			return "", fmt.Errorf("%s has no field for member %q; smithy-go renamed or dropped it", name, k)
		}
		out, err := sp.value(f.Type(), members[k], member, indent+"\t", false)
		if err != nil {
			return "", err
		}
		rendered = append(rendered, f.Name()+": "+out)
	}
	open := name + "{"
	if elide {
		open = "{"
	}
	return goComposite(open, rendered, indent), nil
}

// goIsBytes reports whether a field type is smithy-go's blob: an unnamed
// []byte.
func goIsBytes(t types.Type) bool {
	s, ok := types.Unalias(t).(*types.Slice)
	if !ok {
		return false
	}
	b, ok := types.Unalias(s.Elem()).(*types.Basic)
	return ok && b.Kind() == types.Byte
}

// blob renders a `$base64` value into a []byte field.
//
// A literal is decoded here, at generation time, and written as a Go
// conversion of a quoted string — `[]byte("record-1")`, with strconv.Quote's
// \x escapes for anything unprintable — so the bytes are in the source for the
// compiler and a reader alike. A `$base64` around a $ref is deferred like every
// other expression and decoded by scenario.Blob at run time, from the base64
// text an exported blob is in the context bag (compat/model/README.md
// § Values). Anything else in a blob slot was refused upstream; saying so here
// keeps this a total function rather than a silent wrong spelling.
func (sp *goSpeller) blob(v any, member, indent string) (string, error) {
	key, arg, isExpr := exprOf(v)
	if !isExpr || key != "$base64" {
		return "", fmt.Errorf("a blob member takes $base64, got %s", valueKind(v))
	}
	if text, literal := arg.(string); literal {
		raw, err := decodeBase64(text)
		if err != nil {
			return "", fmt.Errorf("$base64 %q %w", text, err)
		}
		return "[]byte(" + strconv.Quote(string(raw)) + ")", nil
	}
	expr, err := goValue(v, indent)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("scenario.Blob(b, %q, %s)", member, expr), nil
}

// expr renders a deferred value expression into a typed slot.
//
// The expression itself is still the IR's — scenario.Ref, scenario.Name and
// the rest, rendered by goValue — and it still resolves through the run's
// context bag. What changes from the reflective binder it replaces is where
// the result lands: scenario.Bind converts it to the one scalar Go type this
// field wants, chosen here from the SDK's declaration, and the conversion to a
// named type or the pointer wrapper is written in the source rather than
// discovered at run time.
func (sp *goSpeller) expr(t types.Type, v any, member, indent string) (string, error) {
	core, pointer := t, false
	if p, ok := t.Underlying().(*types.Pointer); ok {
		core, pointer = types.Unalias(p.Elem()), true
	}
	basic, ok := core.Underlying().(*types.Basic)
	if !ok {
		name, err := sp.typeName(t)
		if err != nil {
			name = t.String()
		}
		return "", fmt.Errorf("a value expression can only be bound to a scalar member, and this one is a %s", name)
	}
	kind, ok := goScalarKinds[basic.Kind()]
	if !ok {
		return "", fmt.Errorf("no scenario.Bind instantiation produces a %s", basic.Name())
	}
	// A `$now` is epoch milliseconds, which only an int64 holds. The generator
	// has already held it to a modeled long; this is where a vendored SDK that
	// typed that long as anything else is refused rather than overflowed at
	// run time.
	if key, _, _ := exprOf(v); key == "$now" && basic.Kind() != types.Int64 {
		return "", fmt.Errorf("a $now is epoch milliseconds, which needs an int64, and the SDK gives this member a %s", basic.Name())
	}
	_, unnamed := core.(*types.Basic)
	if pointer && !unnamed {
		name, _ := sp.typeName(core)
		return "", fmt.Errorf("no aws helper returns a *%s; the table spells a pointer only to a predeclared basic type", name)
	}
	expr, err := goValue(v, indent)
	if err != nil {
		return "", err
	}
	out := fmt.Sprintf("scenario.Bind[%s](b, %q, %s)", basic.Name(), member, expr)
	if !unnamed {
		name, err := sp.typeName(core)
		if err != nil {
			return "", err
		}
		out = name + "(" + out + ")"
	}
	if pointer {
		sp.usesAWS = true
		out = "aws." + kind + "(" + out + ")"
	}
	return out, nil
}

// typeName spells a type as the emitted file must write it. Only the service
// package and its `types` subpackage may be named: a field whose type comes
// from anywhere else is one this backend does not import, and refusing is
// better than emitting an identifier that does not resolve.
func (sp *goSpeller) typeName(t types.Type) (string, error) {
	switch t := types.Unalias(t).(type) {
	case *types.Basic:
		return t.Name(), nil
	case *types.Named:
		return sp.qualify(t)
	case *types.Pointer:
		inner, err := sp.typeName(t.Elem())
		return "*" + inner, err
	case *types.Slice:
		inner, err := sp.typeName(t.Elem())
		return "[]" + inner, err
	case *types.Map:
		key, err := sp.typeName(t.Key())
		if err != nil {
			return "", err
		}
		value, err := sp.typeName(t.Elem())
		return "map[" + key + "]" + value, err
	}
	return "", fmt.Errorf("no Go type name for %s", t)
}

func (sp *goSpeller) qualify(named *types.Named) (string, error) {
	obj := named.Obj()
	if obj.Pkg() == nil {
		return "", fmt.Errorf("%s is a builtin type with no package", obj.Name())
	}
	switch obj.Pkg().Path() {
	case sp.svc.Path:
		return sp.svc.Name + "." + obj.Name(), nil
	case sp.svc.TypesPath:
		sp.usesTypes = true
		return "types." + obj.Name(), nil
	}
	return "", fmt.Errorf("%s.%s is outside %s and its types package, which are the only ones an emitted file imports", obj.Pkg().Path(), obj.Name(), sp.svc.Path)
}

// ---------------------------------------------------------------------------
// Literals
// ---------------------------------------------------------------------------

// goBasicLiteral renders an IR scalar as a Go constant of the given basic
// type. A mismatch is an error rather than a coercion: "30" is not 30 anywhere
// else in the IR, and accepting it here would let a wrong literal through.
func goBasicLiteral(b *types.Basic, v any) (string, error) {
	switch {
	case b.Info()&types.IsString != 0:
		s, ok := v.(string)
		if !ok {
			return "", fmt.Errorf("a %s member wants a string, got %s", b.Name(), valueKind(v))
		}
		return strconv.Quote(s), nil
	case b.Info()&types.IsBoolean != 0:
		out, ok := v.(bool)
		if !ok {
			return "", fmt.Errorf("a %s member wants a boolean, got %s", b.Name(), valueKind(v))
		}
		return strconv.FormatBool(out), nil
	case b.Info()&types.IsInteger != 0:
		n, ok := numberOf(v)
		if !ok {
			return "", fmt.Errorf("a %s member wants a number, got %s", b.Name(), valueKind(v))
		}
		if !n.Whole {
			return "", fmt.Errorf("a %s member wants a whole number, got %s", b.Name(), n.Text)
		}
		// A whole number wider than an int64 is out of range for every Go
		// integer type, so it is refused before the type's own range is
		// consulted rather than being folded to one that fits.
		if !n.Fits {
			return "", fmt.Errorf("%s is out of range for %s", n.Text, b.Name())
		}
		if min, max, known := goIntRange(b.Kind()); known && (n.Int < min || n.Int > max) {
			return "", fmt.Errorf("%s is out of range for %s", n.Text, b.Name())
		}
		return strconv.FormatInt(n.Int, 10), nil
	case b.Info()&types.IsFloat != 0:
		n, ok := numberOf(v)
		if !ok {
			return "", fmt.Errorf("a %s member wants a number, got %s", b.Name(), valueKind(v))
		}
		// A literal a float64 cannot carry, and one a float32 member cannot,
		// are the same fault: the constant would not compile, or would compile
		// to ±Inf. Both are refused rather than emitted.
		if !n.Representable || (b.Kind() == types.Float32 && math.Abs(n.Float) > math.MaxFloat32) {
			return "", fmt.Errorf("%s is out of range for %s", n.Text, b.Name())
		}
		return strconv.FormatFloat(n.Float, 'g', -1, 64), nil
	}
	return "", fmt.Errorf("no IR literal builds a %s", b.Name())
}

// goZeroLiteral is what goBasicLiteral renders for a basic type's zero value,
// which is the value the SDK will not serialize out of a value-typed field.
func goZeroLiteral(b *types.Basic) string {
	switch {
	case b.Info()&types.IsString != 0:
		return `""`
	case b.Info()&types.IsBoolean != 0:
		return "false"
	case b.Info()&types.IsInteger != 0:
		return "0"
	case b.Info()&types.IsFloat != 0:
		return "0"
	}
	return ""
}

func goIntRange(kind types.BasicKind) (min, max int64, known bool) {
	switch kind {
	case types.Int8:
		return math.MinInt8, math.MaxInt8, true
	case types.Int16:
		return math.MinInt16, math.MaxInt16, true
	case types.Int32:
		return math.MinInt32, math.MaxInt32, true
	case types.Int64:
		return math.MinInt64, math.MaxInt64, true
	}
	return 0, 0, false
}
