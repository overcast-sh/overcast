//go:build dev

package main

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

// The type-spelling table: one IR value plus the member's modeled shape, as
// Java source.
//
// This is the whole of what the java-sdk backend knows about writing typed
// Java, and both writers go through it — emit_java.go's builder chains and
// `-explain -lang java` (explain_typed.go), which share javaRequestLines. There
// is no second description of a call anywhere.
//
//	modeled member          value              emitted
//	---------------------   ----------------   ------------------------------
//	string                  "blue"             "blue"
//	integer                 30                 30
//	long                    30                 30L
//	byte                    1                  (byte) 1
//	double                  1.5                1.5
//	float                   1.5                1.5f
//	boolean                 false              false
//	enum Color              "blue"             "blue"          → .color(String)
//	list<QueueAttributeName> ["All"]           List.of("All")  → .…WithStrings
//	map<string,string>      {"a":"b"}          Map.of("a", "b")
//	map<QueueAttributeName,string> {"a":"b"}   Map.of("a", "b") → .…WithStrings
//	list<structure Tag>     [{"Key":"k"}]      List.of(Tag.builder().key("k").build())
//	string                  {"$ref":"q"}       b.string("M", Values.ref("q"))
//	enum Color              {"$ref":"c"}       b.string("M", Values.ref("c"))
//	blob                    {"$base64":"cmVj"} SdkBytes.fromByteArray(Base64.getDecoder().decode("cmVj"))
//	blob                    {"$base64":{"$ref":"k"}} b.blob("M", Values.base64(Values.ref("k")))
//
// # An enum is spelled as its wire value, never as the enum class
//
// The AWS SDK for Java v2 gives every enum-typed member a String form: a scalar
// enum's is an overload of the same name, and a list of enums or a map with an
// enum key or value gets a second setter named `<member>WithStrings`, spelled
// with String in the enum's place. Both send the modeled wire value unchanged.
//
// `Enum.fromValue` would not. A value the *pinned* SDK does not know resolves to
// `UNKNOWN_TO_SDK_VERSION`, whose `toString` is the four-character string
// "null" — so the request goes out carrying "null" rather than the value the
// scenario asked for, which JavaSdkWireFactsTest measures on the wire. The
// String form takes that hazard out of the emitter, and with it the enum-class
// import and the pascalCase-of-an-enum-shape rule. It also puts this backend
// where the Go one already is: `types.QueueAttributeName("All")` passes the
// model's own value straight through too.
//
// One list or map down is as far as the String form reaches. An enum nested
// deeper has no String spelling in the SDK at all, and is refused rather than
// emitted through `fromValue`.
//
// # Why the model is the authority here, and the Go emitter's SDK is not
//
// docs/plans/compat-coverage-modelgen.md §3.2 settles it: a typed backend
// resolves its SDK's field types at emit time *wherever the SDK's nullability
// is not derivable from the model*, and for the AWS SDK for Java v2 it is —
// every scalar is boxed, so a builder setter takes the value whatever the
// member's optionality and a boxed 0 really is serialized. That is measured
// rather than assumed: JavaSdkWireFactsTest in the suite sends
// ReceiveMessage's VisibilityTimeout as 0 through a real client and asserts the
// member is on the wire. The consequence is that this file has no counterpart
// to emit_go_spell.go's zero-value refusal, and needs no SDK lookup at
// generation time.
//
// What the model cannot say is whether the *pinned* SDK is new enough to have
// the operation at all. That axis is answered by the suite's own `mvn package`,
// which is a compile error rather than a wrong request — see emit_java.go.

// javaSpeller renders one service's values against that service's modeled
// shapes, recording the model classes it named so the emitted file's import
// block lists exactly what it turned out to need. That makes an instance
// single-use: refusal detection runs over a throwaway one, because a group that
// is refused must not leave its import behind.
type javaSpeller struct {
	model *serviceModel
	// sdkID names the SDK package a spelled class comes from, which is needed
	// in full whenever the class's simple name is one the emitted file has
	// already bound — see className.
	sdkID string
	// modelTypes is the set of `….model.<Name>` classes some spelled value
	// named and the file may import. An emitted file imports each of them, and
	// nothing else.
	modelTypes map[string]bool
}

func newJavaSpeller(model *serviceModel, sdkID string) *javaSpeller {
	return &javaSpeller{model: model, sdkID: sdkID, modelTypes: map[string]bool{}}
}

// javaSlot says where a value sits relative to the builder setter that will
// take it, which is what decides whether an enum in it has a String form.
type javaSlot int

const (
	// slotSetter is a setter's own argument. A scalar enum here is spelled by
	// the String overload of the same name; a structure here opens a builder of
	// its own, whose members are setter slots again.
	slotSetter javaSlot = iota
	// slotElement is one level inside the setter's own list or map, which the
	// SDK's `<member>WithStrings` setter spells with String in the enum's
	// place.
	slotElement
	// slotDeep is anywhere further in, where the SDK offers no String form.
	slotDeep
)

// inside is the slot a value's list elements or map entries sit in.
func (s javaSlot) inside() javaSlot {
	if s == slotSetter {
		return slotElement
	}
	return slotDeep
}

// javaScalarBinders maps a Smithy scalar shape type to the Binder accessor that
// converts a deferred expression into the Java box the SDK gives that member.
// The AWS SDK for Java v2 boxes every scalar, so the list is the box set.
var javaScalarBinders = map[string]string{
	"string":     "string",
	"boolean":    "bool",
	"byte":       "byteValue",
	"short":      "shortValue",
	"integer":    "integer",
	"intEnum":    "integer",
	"long":       "longValue",
	"float":      "floatValue",
	"double":     "doubleValue",
	"bigInteger": "",
	"bigDecimal": "",
}

// javaUnsupportedKinds are the modeled member kinds no value in the IR's
// grammar can carry. Timestamps, documents and unions have no portable value
// and are refused upstream — an error in a recipe or an authored scenario, and
// `no-portable-value` for a binding (binder.go) — so this is a backstop rather
// than a live path. A blob is not here: `$base64` spells it (blob, below).
var javaUnsupportedKinds = map[string]bool{
	"timestamp": true,
	"document":  true,
	"union":     true,
}

// javaFileBoundNames are the simple names an emitted file already binds: the
// scenario vocabulary and java.util types it imports, the harness types it
// names, and the interface it implements — which is in the same package, and a
// single-type import of the same name shadows it rather than overloading it.
//
// A model class of any of these names is written out fully qualified. IAM,
// Resource Groups and Greengrass all model a shape named `Group`, and a second
// `import ….model.Group;` in a file that already imports
// `io.overcast.compat.scenario.Group` does not compile.
var javaFileBoundNames = map[string]bool{
	"AwsClients":   true,
	"ServiceGroup": true,
	"TestContext":  true,
	"TestFn":       true,
	"Call":         true,
	"Check":        true,
	"Clause":       true,
	"ErrorSpec":    true,
	"Group":        true,
	"Values":       true,
	"Where":        true,
	"List":         true,
	"Map":          true,
}

// memberTarget resolves the shape a call's input member targets, refusing a
// member the model does not give the operation. An operation with a unit input
// has no members at all, which is the same refusal with a different sentence.
func (sp *javaSpeller) memberTarget(op, member string) (string, error) {
	input := sp.model.InputShape(op)
	if input == "" {
		return "", fmt.Errorf("the model gives %s no input, so it has no member %q", op, member)
	}
	target, ok := sp.model.MemberTarget(input, member)
	if !ok {
		return "", fmt.Errorf("the model gives %s no member %q (its input is %s)", op, member, bareShapeName(input))
	}
	return target, nil
}

// setterFor is the builder setter a member's value is passed to: javaSetter,
// plus the SDK's `WithStrings` suffix wherever the member is a list of enums or
// a map with an enum key or value.
func (sp *javaSpeller) setterFor(target, member string) string {
	if sp.wantsStringOverload(target) {
		return javaSetter(member) + "WithStrings"
	}
	return javaSetter(member)
}

// wantsStringOverload reports whether the SDK spells this member's setter
// `<member>WithStrings` — which it does for exactly the composites that carry
// an enum directly. A scalar enum's String form is an overload of the setter's
// own name, so it needs no suffix.
func (sp *javaSpeller) wantsStringOverload(target string) bool {
	shape := sp.model.Shapes[target]
	switch sp.shapeType(target) {
	case "list":
		return sp.model.Kind(shape.Member) == "enum"
	case "map":
		return sp.model.Kind(shape.Key) == "enum" || sp.model.Kind(shape.Value) == "enum"
	}
	return false
}

// value renders one IR value as a Java expression of the member's modeled type,
// in the slot a builder setter's own argument sits in.
//
// member is the modeled member name the value belongs to, which a deferred
// expression carries into its failure message.
func (sp *javaSpeller) value(target string, v any, member string) (string, error) {
	return sp.valueIn(target, v, member, slotSetter)
}

func (sp *javaSpeller) valueIn(target string, v any, member string, slot javaSlot) (string, error) {
	kind := sp.model.Kind(target)
	if javaUnsupportedKinds[kind] {
		return "", fmt.Errorf("the java-sdk emitter has no Java value expression for a %s member (%s)", kind, bareShapeName(target))
	}
	if kind == "blob" {
		return javaBlob(v, member)
	}
	if _, _, isExpr := exprOf(v); isExpr {
		return sp.expr(target, kind, v, member, slot)
	}
	if v == nil {
		// The AWS SDK for Java v2 uses null for "unset", so there is no
		// spelling that sends an explicit JSON null; a scenario asking for one
		// would silently omit the member instead.
		return "", fmt.Errorf("a Java builder setter cannot send an explicit null: null means unset")
	}
	switch kind {
	case "string", "boolean", "integer", "float", "enum":
		return sp.scalar(target, kind, v, slot)
	case "list":
		return sp.list(target, v, member, slot)
	case "map":
		return sp.mapping(target, v, member, slot)
	case "structure":
		return sp.structure(target, v, member)
	}
	return "", fmt.Errorf("no Java literal builds a %s member (%s)", kind, bareShapeName(target))
}

// javaBlob renders a `$base64` value into a blob member, whose builder setter
// takes an SdkBytes.
//
// Java has no byte-string literal, so a literal is spelled as the SDK's own
// SdkBytes built from the scenario's base64 text by the platform decoder —
// the same text the scenario file writes, so the emitted line greps against
// it — and the generator has already proved that text canonical. Both classes
// are written fully qualified: an emitted file imports exactly the model
// classes it names, and neither of these is one. A `$base64` around a $ref is
// deferred like every other expression, through Binder.blob.
func javaBlob(v any, member string) (string, error) {
	key, arg, isExpr := exprOf(v)
	if !isExpr || key != "$base64" {
		return "", fmt.Errorf("a blob member takes $base64, got %s", valueKind(v))
	}
	if text, literal := arg.(string); literal {
		if _, err := decodeBase64(text); err != nil {
			return "", fmt.Errorf("$base64 %q %w", text, err)
		}
		return "software.amazon.awssdk.core.SdkBytes.fromByteArray(java.util.Base64.getDecoder().decode(" + javaQuote(text) + "))", nil
	}
	rendered, err := javaValue(v)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("b.blob(%s, %s)", javaQuote(member), rendered), nil
}

// scalar renders a literal into a scalar member. An enum is its wire value: the
// model already checked the literal is one of the shape's values (binder.go's
// checkString), and the SDK's String form sends it unchanged.
func (sp *javaSpeller) scalar(target, kind string, v any, slot javaSlot) (string, error) {
	if kind == "enum" {
		s, ok := v.(string)
		if !ok {
			return "", fmt.Errorf("an enum member wants a string, got %s", valueKind(v))
		}
		if err := sp.enumHasStringForm(target, slot); err != nil {
			return "", err
		}
		return javaQuote(s), nil
	}
	return javaLiteral(sp.shapeType(target), kind, v)
}

// enumHasStringForm refuses an enum the SDK gives no String spelling.
//
// The String form reaches a setter's own argument and one list or map inside
// it, and no further: there is no `WithStrings` for a list of lists of enums or
// a map of string to list of enums. Falling back to `Enum.fromValue` there
// would reintroduce the "null" wire value this backend spells enums as strings
// to avoid, so the group is scoped away from java-sdk instead.
func (sp *javaSpeller) enumHasStringForm(target string, slot javaSlot) error {
	if slot != slotDeep {
		return nil
	}
	return fmt.Errorf("the AWS SDK for Java v2 has no String form for the enum %s nested this deep in a member, "+
		"and this emitter will not spell the enum class: fromValue sends \"null\" for a value the pinned SDK does not know",
		bareShapeName(target))
}

func (sp *javaSpeller) list(target string, v any, member string, slot javaSlot) (string, error) {
	items, ok := v.([]any)
	if !ok {
		return "", fmt.Errorf("a list member wants a JSON array, got %s", valueKind(v))
	}
	elem := sp.model.Shapes[target].Member
	rendered := make([]string, 0, len(items))
	for _, item := range items {
		out, err := sp.valueIn(elem, item, member, slot.inside())
		if err != nil {
			return "", err
		}
		rendered = append(rendered, out)
	}
	return "List.of(" + strings.Join(rendered, ", ") + ")", nil
}

func (sp *javaSpeller) mapping(target string, v any, member string, slot javaSlot) (string, error) {
	entries, ok := v.(map[string]any)
	if !ok {
		return "", fmt.Errorf("a map member wants a JSON object, got %s", valueKind(v))
	}
	shape := sp.model.Shapes[target]
	keyKind := sp.model.Kind(shape.Key)
	if keyKind != "string" && keyKind != "enum" {
		return "", fmt.Errorf("a map member keyed by %s has no IR spelling; the IR's objects have string keys", keyKind)
	}
	if keyKind == "enum" {
		if err := sp.enumHasStringForm(shape.Key, slot.inside()); err != nil {
			return "", err
		}
	}
	pairs := make([][2]string, 0, len(entries))
	for _, k := range sortedKeys(entries) {
		out, err := sp.valueIn(shape.Value, entries[k], member, slot.inside())
		if err != nil {
			return "", err
		}
		pairs = append(pairs, [2]string{javaQuote(k), out})
	}
	return javaMapLiteral(pairs), nil
}

// javaMapLiteral spells a map. Map.of takes at most ten pairs, so a wider one
// goes through Map.ofEntries — which has no limit and is otherwise identical.
func javaMapLiteral(pairs [][2]string) string {
	if len(pairs) <= 10 {
		parts := make([]string, 0, len(pairs)*2)
		for _, pair := range pairs {
			parts = append(parts, pair[0], pair[1])
		}
		return "Map.of(" + strings.Join(parts, ", ") + ")"
	}
	parts := make([]string, 0, len(pairs))
	for _, pair := range pairs {
		parts = append(parts, "Map.entry("+pair[0]+", "+pair[1]+")")
	}
	return "Map.ofEntries(" + strings.Join(parts, ", ") + ")"
}

// structure opens a builder of its own, so its members are setter slots again —
// the nested builder declares the same String forms the outer one does.
func (sp *javaSpeller) structure(target string, v any, member string) (string, error) {
	members, ok := v.(map[string]any)
	if !ok {
		return "", fmt.Errorf("a structure member wants a JSON object, got %s", valueKind(v))
	}
	class := sp.className(target)
	var out strings.Builder
	out.WriteString(class + ".builder()")
	for _, k := range sortedKeys(members) {
		field, ok := sp.model.MemberTarget(target, k)
		if !ok {
			return "", fmt.Errorf("the model gives %s no member %q", bareShapeName(target), k)
		}
		rendered, err := sp.valueIn(field, members[k], member, slotSetter)
		if err != nil {
			return "", err
		}
		out.WriteString("." + sp.setterFor(field, k) + "(" + rendered + ")")
	}
	out.WriteString(".build()")
	return out.String(), nil
}

// expr renders a deferred value expression into a typed slot.
//
// The expression itself is still the IR's — Values.ref, Values.name and the
// rest, rendered by javaValue — and it still resolves through the run's context
// bag. What changes is where the result lands: a Binder accessor converts it to
// the one Java box this member wants, chosen here from the model. An enum takes
// the String box, which is what its String form accepts.
func (sp *javaSpeller) expr(target, kind string, v any, member string, slot javaSlot) (string, error) {
	shapeType := sp.shapeType(target)
	if kind == "enum" {
		if err := sp.enumHasStringForm(target, slot); err != nil {
			return "", err
		}
		shapeType = "string"
	}
	accessor, known := javaScalarBinders[shapeType]
	if !known {
		return "", fmt.Errorf("a value expression can only be bound to a scalar member, and this one is a %s", kind)
	}
	if accessor == "" {
		return "", fmt.Errorf("no Binder accessor produces the Java type for a %s member", shapeType)
	}
	rendered, err := javaValue(v)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("b.%s(%s, %s)", accessor, javaQuote(member), rendered), nil
}

// shapeType is the member's Smithy shape type — the distinction Kind collapses,
// and the one that decides which Java box the SDK gave the member: `integer`
// and `long` are both Kind "integer" and are Integer and Long in Java.
func (sp *javaSpeller) shapeType(target string) string {
	if kind, ok := preludeJavaTypes[target]; ok {
		return kind
	}
	shape, ok := sp.model.Shapes[target]
	if !ok {
		return "unknown"
	}
	if shape.Type == "set" {
		return "list"
	}
	return shape.Type
}

// className is the Java class the SDK generates for a modeled shape, as the
// emitted file must write it: the simple name, recorded so the file imports it,
// or the fully qualified name when the simple one is already bound.
func (sp *javaSpeller) className(target string) string {
	name := javaClassOf(target)
	if javaFileBoundNames[name] || name == javaNameClientClass(sp.sdkID) {
		return javaNameModelPackage(sp.sdkID) + "." + name
	}
	sp.modelTypes[name] = true
	return name
}

// preludeJavaTypes names the Smithy prelude targets, which the snapshot never
// emits as shapes of their own. It mirrors model.go's preludeKinds but keeps the
// width the Java box depends on.
var preludeJavaTypes = map[string]string{
	"smithy.api#String":           "string",
	"smithy.api#Blob":             "blob",
	"smithy.api#Boolean":          "boolean",
	"smithy.api#PrimitiveBoolean": "boolean",
	"smithy.api#Byte":             "byte",
	"smithy.api#Short":            "short",
	"smithy.api#Integer":          "integer",
	"smithy.api#PrimitiveInteger": "integer",
	"smithy.api#Long":             "long",
	"smithy.api#PrimitiveLong":    "long",
	"smithy.api#Float":            "float",
	"smithy.api#Double":           "double",
	"smithy.api#BigInteger":       "bigInteger",
	"smithy.api#BigDecimal":       "bigDecimal",
	"smithy.api#Timestamp":        "timestamp",
	"smithy.api#Document":         "document",
	"smithy.api#Unit":             "unit",
}

// modelImports is the sorted set of `….model.<Name>` classes the spelled values
// named, for the emitted file's import block.
func (sp *javaSpeller) modelImports() []string {
	out := make([]string, 0, len(sp.modelTypes))
	for name := range sp.modelTypes {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// ---------------------------------------------------------------------------
// Literals
// ---------------------------------------------------------------------------

// javaLiteral renders an IR scalar as a Java constant of the member's own type.
// A mismatch is an error rather than a coercion: "30" is not 30 anywhere else in
// the IR, and accepting it here would let a wrong literal through.
func javaLiteral(shapeType, kind string, v any) (string, error) {
	switch kind {
	case "string":
		s, ok := v.(string)
		if !ok {
			return "", fmt.Errorf("a string member wants a string, got %s", valueKind(v))
		}
		return javaQuote(s), nil
	case "boolean":
		b, ok := v.(bool)
		if !ok {
			return "", fmt.Errorf("a boolean member wants a boolean, got %s", valueKind(v))
		}
		return strconv.FormatBool(b), nil
	case "integer":
		return javaIntegerLiteral(shapeType, v)
	case "float":
		return javaFloatLiteral(shapeType, v)
	}
	return "", fmt.Errorf("no Java literal builds a %s member", kind)
}

// javaIntegerLiteral renders a whole number as a constant of the member's own
// Java integer type.
//
// The cast on a byte or a short is not decoration: an `int` literal does not
// convert to a `Byte` or a `Short` on the way into a builder setter — narrowing
// happens only in an assignment context, not in an invocation one — so
// `.foo(1)` against a `Byte` member is a compile error and `.foo((byte) 1)` is
// the spelling that boxes.
func javaIntegerLiteral(shapeType string, v any) (string, error) {
	n, ok := numberOf(v)
	if !ok {
		return "", fmt.Errorf("a %s member wants a number, got %s", shapeType, valueKind(v))
	}
	if !n.Whole {
		return "", fmt.Errorf("a %s member wants a whole number, got %s", shapeType, n.Text)
	}
	min, max, known := javaIntRange(shapeType)
	if !known {
		return "", fmt.Errorf("no Java literal builds a %s member", shapeType)
	}
	// A whole number wider than an int64 is out of range for every Java integer
	// type, and is refused before the type's own range is consulted rather than
	// being folded to one that fits.
	if !n.Fits || n.Int < min || n.Int > max {
		return "", fmt.Errorf("%s is out of range for %s", n.Text, shapeType)
	}
	digits := strconv.FormatInt(n.Int, 10)
	switch shapeType {
	case "byte":
		return "(byte) " + digits, nil
	case "short":
		return "(short) " + digits, nil
	case "long":
		return digits + "L", nil
	}
	return digits, nil
}

// javaFloatLiteral renders a number as a constant of the member's own Java
// floating-point type. A literal outside that type's range is refused: javac
// rejects `1e+300f` outright, and a double a float64 cannot carry would compile
// to infinity.
func javaFloatLiteral(shapeType string, v any) (string, error) {
	n, ok := numberOf(v)
	if !ok {
		return "", fmt.Errorf("a %s member wants a number, got %s", shapeType, valueKind(v))
	}
	suffix, known := javaFloatSuffix(shapeType)
	if !known {
		return "", fmt.Errorf("no Java literal builds a %s member", shapeType)
	}
	if !n.Representable || (shapeType == "float" && math.Abs(n.Float) > math.MaxFloat32) {
		return "", fmt.Errorf("%s is out of range for %s", n.Text, shapeType)
	}
	rendered := strconv.FormatFloat(n.Float, 'g', -1, 64)
	if !strings.ContainsAny(rendered, ".eE") {
		rendered += ".0"
	}
	return rendered + suffix, nil
}

// javaIntRange is the range of the Java integer type a Smithy shape type maps
// to, and whether one exists: bigInteger is java.math.BigInteger, which has no
// literal at all.
func javaIntRange(shapeType string) (min, max int64, known bool) {
	switch shapeType {
	case "byte":
		return math.MinInt8, math.MaxInt8, true
	case "short":
		return math.MinInt16, math.MaxInt16, true
	case "integer", "intEnum":
		return math.MinInt32, math.MaxInt32, true
	case "long":
		return math.MinInt64, math.MaxInt64, true
	}
	return 0, 0, false
}

// javaFloatSuffix is javaIntRange's mirror for the floating-point types: the
// suffix the literal carries, and whether the shape type has a Java literal at
// all. A float takes `f` and a double takes nothing; bigDecimal is
// java.math.BigDecimal and is refused for the same reason bigInteger is — a
// double standing in for one would silently change the value it was chosen to
// carry exactly.
func javaFloatSuffix(shapeType string) (suffix string, known bool) {
	switch shapeType {
	case "float":
		return "f", true
	case "double":
		return "", true
	}
	return "", false
}

// javaQuote renders a Go string as a Java string literal. Java accepts \uXXXX
// but not Go's \xNN, so control characters go through the unicode form and
// everything printable stays as itself — the emitted file is UTF-8.
func javaQuote(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if r < 0x20 || r == utf8.RuneError {
				fmt.Fprintf(&b, `\u%04x`, r)
				continue
			}
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// javaClassOf is the Java class the AWS SDK for Java v2 generates for a modeled
// shape: its bare name run through the code generator's pascalCase, which is
// what turns AWSOrganizationsNotInUse into AwsOrganizationsNotInUse.
func javaClassOf(shapeID string) string { return javaPascal(bareShapeName(shapeID)) }
