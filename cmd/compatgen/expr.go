//go:build dev

package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// Value expressions.
//
// A value in a recipe or a scenario is ordinary JSON with seven expression
// forms, each an object with exactly one `$`-prefixed key:
//
//	{"$lit": <json>}                 the JSON verbatim, never interpreted
//	{"$ref": "queue.url"}            a value exported earlier in the group
//	{"$name": "q"}                   {runId}-{group}-q, the only way to name a resource
//	{"$concat": [<part>, ...]}       string concatenation; a bare string part is a literal
//	{"$index": [<value>, n]}         element n of a list-valued expression
//	{"$base64": "<base64>"}          these bytes — the only value a blob member takes
//	{"$base64": {"$ref": "k.blob"}}  the bytes of a blob a previous call exported
//	{"$now": {"unit": "epochMillis"}}  the client's clock when the call is made,
//	                                 in epoch milliseconds, optionally plus
//	                                 "offsetMillis": n — for a long member only
//
// Everything else is structural: an object is a structure or map whose values
// are themselves values, an array is a list of values, and a scalar is itself.
// No conditionals, no arithmetic, no scripting — eight implementations have to
// agree on every value, so the grammar is closed and total.

// exprKeys is the closed set of expression forms.
var exprKeys = map[string]struct{}{"$lit": {}, "$ref": {}, "$name": {}, "$concat": {}, "$index": {}, "$base64": {}, "$now": {}}

// exprOf returns the expression form of a value, or "" for a structural value.
func exprOf(v any) (key string, arg any, ok bool) {
	object, isObject := v.(map[string]any)
	if !isObject || len(object) != 1 {
		return "", nil, false
	}
	for k, a := range object {
		if _, known := exprKeys[k]; known {
			return k, a, true
		}
	}
	return "", nil, false
}

// validateValue checks that a value uses the grammar above and nothing else.
// It reports where the fault is in terms of the value's own structure.
func validateValue(v any, where string) error {
	switch value := v.(type) {
	case map[string]any:
		dollar := 0
		for k := range value {
			if strings.HasPrefix(k, "$") {
				dollar++
			}
		}
		if dollar > 0 && (dollar != 1 || len(value) != 1) {
			return fmt.Errorf("%s: an expression is an object with exactly one $-key, got %s", where, sortedKeys(value))
		}
		key, arg, isExpr := exprOf(value)
		if !isExpr {
			if dollar == 1 {
				for k := range value {
					return fmt.Errorf("%s: unknown expression %q (want one of $lit, $ref, $name, $concat, $index, $base64, $now)", where, k)
				}
			}
			for k, child := range value {
				if err := validateValue(child, where+"."+k); err != nil {
					return err
				}
			}
			return nil
		}
		return validateExpr(key, arg, where)
	case []any:
		for i, child := range value {
			if err := validateValue(child, fmt.Sprintf("%s[%d]", where, i)); err != nil {
				return err
			}
		}
		return nil
	case string, bool, json.Number, float64, nil:
		return nil
	}
	return fmt.Errorf("%s: unsupported JSON value %T", where, v)
}

func validateExpr(key string, arg any, where string) error {
	switch key {
	case "$lit":
		return nil
	case "$ref":
		ref, ok := arg.(string)
		if !ok || !validContextPath(ref) {
			return fmt.Errorf("%s: $ref must be a context path like queue.url, got %v", where, arg)
		}
	case "$name":
		suffix, ok := arg.(string)
		if !ok || !validNameSuffix(suffix) {
			return fmt.Errorf("%s: $name must be a kebab-case suffix, got %v", where, arg)
		}
	case "$concat":
		parts, ok := arg.([]any)
		if !ok || len(parts) == 0 {
			return fmt.Errorf("%s: $concat takes a non-empty array of parts", where)
		}
		for i, part := range parts {
			if _, isString := part.(string); isString {
				continue
			}
			partKey, _, isExpr := exprOf(part)
			if !isExpr {
				return fmt.Errorf("%s: $concat part %d must be a string or an expression", where, i)
			}
			if partKey == "$now" {
				return fmt.Errorf("%s: $concat part %d is a $now, which is a number, not a string", where, i)
			}
			if err := validateValue(part, fmt.Sprintf("%s.$concat[%d]", where, i)); err != nil {
				return err
			}
		}
	case "$index":
		pair, ok := arg.([]any)
		if !ok || len(pair) != 2 {
			return fmt.Errorf("%s: $index takes [<value>, <index>]", where)
		}
		if key, _, _ := exprOf(pair[0]); key == "$now" {
			return fmt.Errorf("%s: $index takes a list, and a $now is a number", where)
		}
		if err := validateValue(pair[0], where+".$index[0]"); err != nil {
			return err
		}
		if _, err := integerOf(pair[1]); err != nil {
			return fmt.Errorf("%s: $index position must be a non-negative integer", where)
		}
	case "$base64":
		switch inner := arg.(type) {
		case string:
			if _, err := decodeBase64(inner); err != nil {
				return fmt.Errorf("%s: $base64 %q: %w", where, inner, err)
			}
		case map[string]any:
			key, _, isExpr := exprOf(inner)
			if !isExpr || key != "$ref" {
				return fmt.Errorf("%s: $base64 takes a base64 string or a {\"$ref\": ...} to an exported blob, got %s", where, valueKind(inner))
			}
			if err := validateValue(inner, where+".$base64"); err != nil {
				return err
			}
		default:
			return fmt.Errorf("%s: $base64 takes a base64 string or a {\"$ref\": ...} to an exported blob, got %s", where, valueKind(inner))
		}
	case "$now":
		if _, err := nowOf(arg); err != nil {
			return fmt.Errorf("%s: %w", where, err)
		}
	}
	return nil
}

// The `$now` value (compat/model/README.md § Values) is the client's own clock
// at the moment a call is made, which is what a real caller stamps an event
// with. It is a closed object rather than a bare keyword so that each of its
// two knobs is named where it is used:
//
//   - unit says what the number counts. A model `long` does not say whether it
//     is seconds or milliseconds — CloudWatch Logs' is milliseconds — so the
//     author states it and the generator holds it to the one unit every
//     backend implements. A second unit would be an addition to nowUnits and
//     to seven runtimes, never a reinterpretation of this one.
//   - offsetMillis is added to the instant. Every `$now` in one call's params
//     sees the same instant, so the offset is what makes two values in one
//     call distinct and ordered — two events in one PutLogEvents batch must be
//     chronological. It is bounded to an hour either way, which keeps every
//     value well inside the window services accept a client clock in
//     (CloudWatch Logs refuses an event more than two hours ahead or fourteen
//     days behind), and a zero offset is written by omitting it, so each value
//     has one spelling.
//
// Nothing else is accepted: no arithmetic on other values, no timestamp
// shape, no second clock.

// nowUnits is the closed set of `$now` units.
var nowUnits = map[string]bool{"epochMillis": true}

// nowMaxOffsetMillis bounds `$now`'s offset, in either direction: one hour.
const nowMaxOffsetMillis = 3_600_000

// nowOf validates a `$now` argument and returns its offset in milliseconds.
func nowOf(arg any) (int64, error) {
	object, ok := arg.(map[string]any)
	if !ok {
		return 0, fmt.Errorf(`$now takes {"unit": "epochMillis"} with an optional "offsetMillis", got %s`, valueKind(arg))
	}
	for _, k := range sortedKeys(object) {
		if k != "unit" && k != "offsetMillis" {
			return 0, fmt.Errorf("$now has no member %q; it takes unit and offsetMillis", k)
		}
	}
	unit, ok := object["unit"].(string)
	if !ok {
		return 0, fmt.Errorf(`$now needs "unit": "epochMillis", because a long does not say what it counts`)
	}
	if !nowUnits[unit] {
		return 0, fmt.Errorf(`$now unit %q is not one the IR has; its one unit is "epochMillis"`, unit)
	}
	raw, present := object["offsetMillis"]
	if !present {
		return 0, nil
	}
	var offset int64
	switch n := raw.(type) {
	case json.Number:
		i, err := n.Int64()
		if err != nil {
			return 0, fmt.Errorf("$now offsetMillis must be a whole number of milliseconds, got %s", n)
		}
		offset = i
	case float64:
		if n != float64(int64(n)) {
			return 0, fmt.Errorf("$now offsetMillis must be a whole number of milliseconds, got %v", n)
		}
		offset = int64(n)
	default:
		return 0, fmt.Errorf("$now offsetMillis must be a number, got %s", valueKind(raw))
	}
	if offset == 0 {
		return 0, fmt.Errorf("$now offsetMillis is 0; omit it instead, so each value has one spelling")
	}
	if offset < -nowMaxOffsetMillis || offset > nowMaxOffsetMillis {
		return 0, fmt.Errorf("$now offsetMillis %d is outside ±%d (one hour)", offset, nowMaxOffsetMillis)
	}
	return offset, nil
}

// nowParts is a validated `$now` argument as the two arguments every typed
// runtime's constructor takes — the unit and the offset, zero where the
// scenario omits it — which is how the four emitters spell one.
func nowParts(arg any) (unit string, offset int64, err error) {
	offset, err = nowOf(arg)
	if err != nil {
		return "", 0, err
	}
	return arg.(map[string]any)["unit"].(string), offset, nil
}

// hasNow reports whether a value is, or contains, a `$now` — which the
// expected side of a check may never hold: the instant is taken when a call is
// made, so there is nothing in a response it could be equal to.
func hasNow(v any) bool {
	found := false
	walkValue(v, func(key string, _ any) {
		if key == "$now" {
			found = true
		}
	})
	return found
}

// decodeBase64 decodes the text of a `$base64` literal, which must be
// standard base64 (RFC 4648 §4, the `+/` alphabet) with its padding, and
// canonical: the one spelling of those bytes that re-encodes to itself.
//
// Canonical matters because a blob is compared as its document form — that same
// base64 text — in every backend (compat/model/README.md § Values), so two
// spellings of one byte string would be two values that `equals` tells apart.
// URL-safe base64, missing padding, embedded whitespace and non-zero trailing
// bits are all refused here rather than accepted by one runtime's decoder and
// rejected by another's.
func decodeBase64(text string) ([]byte, error) {
	raw, err := base64.StdEncoding.Strict().DecodeString(text)
	if err != nil {
		return nil, fmt.Errorf("is not standard padded base64: %w", err)
	}
	if base64.StdEncoding.EncodeToString(raw) != text {
		return nil, fmt.Errorf("is not the canonical spelling of its bytes (%q)", base64.StdEncoding.EncodeToString(raw))
	}
	return raw, nil
}

// integerOf accepts a JSON number that is a non-negative integer.
func integerOf(v any) (int, error) {
	switch n := v.(type) {
	case json.Number:
		i, err := n.Int64()
		if err != nil || i < 0 {
			return 0, fmt.Errorf("not a non-negative integer: %v", v)
		}
		return int(i), nil
	case float64:
		if n < 0 || n != float64(int(n)) {
			return 0, fmt.Errorf("not a non-negative integer: %v", v)
		}
		return int(n), nil
	}
	return 0, fmt.Errorf("not a number: %v", v)
}

// refsIn collects every $ref in a value, sorted and deduplicated.
func refsIn(v any) []string {
	set := make(map[string]struct{})
	walkValue(v, func(key string, arg any) {
		if key == "$ref" {
			set[arg.(string)] = struct{}{}
		}
	})
	return sortedSet(set)
}

// namesIn collects every $name suffix in a value, sorted and deduplicated.
func namesIn(v any) []string {
	set := make(map[string]struct{})
	walkValue(v, func(key string, arg any) {
		if key == "$name" {
			set[arg.(string)] = struct{}{}
		}
	})
	return sortedSet(set)
}

// walkValue visits every expression in a validated value.
func walkValue(v any, visit func(key string, arg any)) {
	switch value := v.(type) {
	case map[string]any:
		if key, arg, ok := exprOf(value); ok {
			visit(key, arg)
			switch key {
			case "$concat":
				for _, part := range arg.([]any) {
					walkValue(part, visit)
				}
			case "$index":
				walkValue(arg.([]any)[0], visit)
			case "$base64":
				walkValue(arg, visit)
			}
			return
		}
		for _, child := range value {
			walkValue(child, visit)
		}
	case []any:
		for _, child := range value {
			walkValue(child, visit)
		}
	}
}

// hasExpr reports whether a value is, or contains, a value expression. It is
// walkValue asked the shortest question there is, which is what an emitter
// needs in order to decide whether its file references the run-time value
// vocabulary at all.
func hasExpr(v any) bool {
	found := false
	walkValue(v, func(string, any) { found = true })
	return found
}

// literalKind classifies a structural (non-expression) JSON value the way the
// model classifies shapes, so a literal can be checked against the member it
// is sent as. Expressions report the kind they evaluate to where that is
// knowable ($name and $concat are strings) and "" otherwise.
func literalKind(v any, exports exportKinds) string {
	if key, arg, ok := exprOf(v); ok {
		switch key {
		case "$name", "$concat":
			return "string"
		case "$ref":
			return exports[arg.(string)]
		case "$lit":
			return literalKind(arg, exports)
		case "$base64":
			return "blob"
		case "$now":
			return "integer"
		}
		return ""
	}
	switch value := v.(type) {
	case string:
		return "string"
	case bool:
		return "boolean"
	case json.Number:
		if strings.ContainsAny(value.String(), ".eE") {
			return "float"
		}
		return "integer"
	case float64:
		if value == float64(int64(value)) {
			return "integer"
		}
		return "float"
	case []any:
		return "list"
	case map[string]any:
		return "object"
	}
	return ""
}

// exportKinds records the model kind of every exported context path, so a
// $ref can be type-checked where it is used.
type exportKinds map[string]string

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func sortedSet(set map[string]struct{}) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// cloneValue deep-copies a value so a recipe's params can be extended per
// test without mutating the recipe.
func cloneValue(v any) any {
	switch value := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(value))
		for k, child := range value {
			out[k] = cloneValue(child)
		}
		return out
	case []any:
		out := make([]any, len(value))
		for i, child := range value {
			out[i] = cloneValue(child)
		}
		return out
	}
	return v
}

// setMemberPath sets a dotted member path (`Attributes.VisibilityTimeout`)
// inside a params object, creating intermediate objects. It refuses to
// descend through an expression, since the generator cannot know what an
// expression evaluates to.
func setMemberPath(params map[string]any, memberPath string, value any) error {
	parts := strings.Split(memberPath, ".")
	current := params
	for i, part := range parts[:len(parts)-1] {
		child, exists := current[part]
		if !exists {
			next := make(map[string]any)
			current[part] = next
			current = next
			continue
		}
		object, isObject := child.(map[string]any)
		if !isObject {
			return fmt.Errorf("member path %s: %s is not an object", memberPath, strings.Join(parts[:i+1], "."))
		}
		if _, _, isExpr := exprOf(object); isExpr {
			return fmt.Errorf("member path %s: %s is an expression, so the generator cannot set a field inside it", memberPath, strings.Join(parts[:i+1], "."))
		}
		current = object
	}
	current[parts[len(parts)-1]] = value
	return nil
}
