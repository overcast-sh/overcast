package scenario

import (
	"encoding/base64"
	"fmt"
	"strings"
	"time"
)

// Value expressions (compat/model/README.md § Values). A value is JSON: an
// object with exactly one $-prefixed key is an expression, any other object is
// a structure or map whose values are values, an array is a list of values, and
// a scalar is itself. There are no conditionals, no arithmetic and no
// scripting — eight implementations have to agree on every value.

// contextBag is the map from context path ("queue.url") to value that a
// group's exports fill in and its $refs read. It lives on the harness
// TestContext for exactly one group run.
type contextBag struct {
	values map[string]any
}

func newContextBag() *contextBag { return &contextBag{values: map[string]any{}} }

func (c *contextBag) get(path string) (any, bool) {
	v, ok := c.values[path]
	return v, ok
}

func (c *contextBag) set(path string, v any) { c.values[path] = v }

// evaluator turns value expressions into the JSON a call sends. It carries the
// two things $name needs — the run id and the group name — and the context bag
// $ref reads.
type evaluator struct {
	runID string
	group string
	bag   *contextBag
	// clock is the client's clock in epoch milliseconds — what a $now reads.
	// Nil is the real one; a test pins it.
	clock func() int64
	// now is the clock's reading for the call being evaluated, taken once by
	// evalParams so every $now in one call's params sees the same instant. It
	// is nil everywhere else — an expected value, a where — and a $now
	// evaluated there fails.
	now *int64
}

// refError is an unresolvable $ref: an error for the step that carries it, and
// the one failure a teardown step is allowed to be skipped for.
type refError struct {
	path string
}

func (e *refError) Error() string {
	return fmt.Sprintf("context path %q is not set", e.path)
}

// eval evaluates one value.
func (e *evaluator) eval(v any) (any, error) {
	switch t := v.(type) {
	case map[string]any:
		if key, ok := soleExpressionKey(t); ok {
			return e.evalExpr(key, t[key])
		}
		out := make(map[string]any, len(t))
		for k, member := range t {
			ev, err := e.eval(member)
			if err != nil {
				return nil, err
			}
			out[k] = ev
		}
		return out, nil
	case []any:
		out := make([]any, 0, len(t))
		for _, item := range t {
			ev, err := e.eval(item)
			if err != nil {
				return nil, err
			}
			out = append(out, ev)
		}
		return out, nil
	default:
		return v, nil
	}
}

// soleExpressionKey reports the single $-prefixed key of an expression object.
// An object with more than one key, or whose only key is not $-prefixed, is
// structural — the schema forbids a structural object from carrying a
// $-prefixed member name, so the two cases cannot overlap.
func soleExpressionKey(obj map[string]any) (string, bool) {
	if len(obj) != 1 {
		return "", false
	}
	for k := range obj {
		if strings.HasPrefix(k, "$") {
			return k, true
		}
	}
	return "", false
}

func (e *evaluator) evalExpr(key string, arg any) (any, error) {
	switch key {
	case "$lit":
		// Verbatim, never interpreted — this is how an object whose keys start
		// with $ is written.
		return arg, nil
	case "$ref":
		path, ok := arg.(string)
		if !ok {
			return nil, fmt.Errorf("$ref takes a string, got %s", render(arg))
		}
		v, ok := e.bag.get(path)
		if !ok {
			return nil, &refError{path: path}
		}
		return v, nil
	case "$name":
		suffix, ok := arg.(string)
		if !ok {
			return nil, fmt.Errorf("$name takes a string, got %s", render(arg))
		}
		return e.name(suffix), nil
	case "$concat":
		parts, ok := arg.([]any)
		if !ok {
			return nil, fmt.Errorf("$concat takes an array, got %s", render(arg))
		}
		var b strings.Builder
		for _, part := range parts {
			// A bare string part is a literal; anything else is an expression
			// that must evaluate to a string.
			if s, ok := part.(string); ok {
				b.WriteString(s)
				continue
			}
			ev, err := e.eval(part)
			if err != nil {
				return nil, err
			}
			s, ok := ev.(string)
			if !ok {
				return nil, fmt.Errorf("$concat part evaluated to %s, which is not a string", render(ev))
			}
			b.WriteString(s)
		}
		return b.String(), nil
	case "$index":
		args, ok := arg.([]any)
		if !ok || len(args) != 2 {
			return nil, fmt.Errorf("$index takes [value, n], got %s", render(arg))
		}
		n, ok := args[1].(float64)
		if !ok || n < 0 {
			return nil, fmt.Errorf("$index takes a non-negative index, got %s", render(args[1]))
		}
		ev, err := e.eval(args[0])
		if err != nil {
			return nil, err
		}
		list, ok := ev.([]any)
		if !ok {
			return nil, fmt.Errorf("$index applies to a list, got %s", render(ev))
		}
		if int(n) >= len(list) {
			return nil, fmt.Errorf("$index %d is past the end of a list of %d", int(n), len(list))
		}
		return list[int(n)], nil
	case "$base64":
		// A blob. The AWS CLI v2 reads a blob member of --cli-input-json as
		// base64 text, so the text is what goes in the document — which is
		// also a blob's document form in every backend, how `aws --output
		// json` prints one, and so what an exported blob already is in the
		// bag. It is still checked: text that is not canonical standard base64
		// would reach the CLI as a different spelling from the one every other
		// backend decodes, or fail there as "Invalid base64".
		ev, err := e.eval(arg)
		if err != nil {
			return nil, err
		}
		text, ok := ev.(string)
		if !ok {
			return nil, fmt.Errorf("$base64 takes base64 text, got %s", render(ev))
		}
		if _, err := decodeBase64(text); err != nil {
			return nil, err
		}
		return text, nil
	case "$now":
		// The client's clock, as the number of epoch milliseconds the CLI
		// reads a long member of --cli-input-json as.
		offset, err := nowOffset(arg)
		if err != nil {
			return nil, err
		}
		if e.now == nil {
			return nil, fmt.Errorf("$now is read when a call is made, so it can only be a call's param, never an expected value")
		}
		return float64(*e.now + offset), nil
	default:
		return nil, fmt.Errorf("unknown value expression %q", key)
	}
}

// decodeBase64 decodes a blob's document form: standard base64 with padding,
// in its one canonical spelling. compat/model/testdata/blobs pins what it
// accepts and refuses, for every backend at once.
func decodeBase64(text string) ([]byte, error) {
	raw, err := base64.StdEncoding.Strict().DecodeString(text)
	if err != nil {
		return nil, fmt.Errorf("$base64 %q is not standard padded base64: %v", text, err)
	}
	if canonical := base64.StdEncoding.EncodeToString(raw); canonical != text {
		return nil, fmt.Errorf("$base64 %q is not the canonical spelling of its bytes, which is %q", text, canonical)
	}
	return raw, nil
}

// name is $name: "{runId}-{group}-{suffix}", with the group token the whole
// group name and no shortening anywhere. That is what makes the name-hygiene
// convention hold by construction, and it is what lets the orphan sweep find
// anything a crashed run left behind.
func (e *evaluator) name(suffix string) string {
	return e.runID + "-" + e.group + "-" + suffix
}

// nowUnits is $now's closed set of units, and nowMaxOffsetMillis the bound on
// its offset either way: one hour (compat/model/README.md § Values).
var nowUnits = map[string]bool{"epochMillis": true}

const nowMaxOffsetMillis = 3_600_000

// nowOffset validates a $now argument and returns its offset in milliseconds.
// compat/model/testdata/now pins what every backend accepts and refuses.
func nowOffset(arg any) (int64, error) {
	object, ok := arg.(map[string]any)
	if !ok {
		return 0, fmt.Errorf(`$now takes {"unit": "epochMillis"}, got %s`, render(arg))
	}
	for k := range object {
		if k != "unit" && k != "offsetMillis" {
			return 0, fmt.Errorf("$now has no member %q; it takes unit and offsetMillis", k)
		}
	}
	unit, present := object["unit"]
	if !present {
		return 0, fmt.Errorf(`$now needs "unit": "epochMillis"`)
	}
	var offset int64
	if raw, present := object["offsetMillis"]; present {
		n, ok := raw.(float64)
		if !ok || n != float64(int64(n)) {
			return 0, fmt.Errorf("$now offsetMillis must be a whole number of milliseconds, got %s", render(raw))
		}
		if n == 0 {
			return 0, fmt.Errorf("$now offsetMillis is 0; the scenario omits it instead")
		}
		offset = int64(n)
	}
	s, isString := unit.(string)
	if !isString {
		return 0, fmt.Errorf(`$now unit %s is not one the IR has; its one unit is "epochMillis"`, render(unit))
	}
	return checkNowArguments(s, offset)
}

// checkNowArguments holds the two things any $now comes down to — a unit the
// IR has, and an offset inside an hour — to the rule every runtime holds its
// Now(unit, offsetMillis) to.
func checkNowArguments(unit string, offset int64) (int64, error) {
	if !nowUnits[unit] {
		return 0, fmt.Errorf(`$now unit %q is not one the IR has; its one unit is "epochMillis"`, unit)
	}
	if offset < -nowMaxOffsetMillis || offset > nowMaxOffsetMillis {
		return 0, fmt.Errorf("$now offsetMillis %d is outside ±%d (one hour)", offset, nowMaxOffsetMillis)
	}
	return offset, nil
}

// clockMillis is the client's clock in epoch milliseconds.
func (e *evaluator) clockMillis() int64 {
	if e.clock != nil {
		return e.clock()
	}
	return time.Now().UnixMilli()
}

// evalParams evaluates a call's input members. The returned map is what is
// marshalled into --cli-input-json.
//
// The clock is read here, once per call, so every $now in these params sees
// the same instant. It is read on a copy of the evaluator: a group's
// evaluator is shared by the tests of a parallel probe group, and one call's
// reading must not become another's.
func (e *evaluator) evalParams(params map[string]any) (map[string]any, error) {
	call := *e
	now := e.clockMillis()
	call.now = &now
	out := make(map[string]any, len(params))
	for k, v := range params {
		ev, err := call.eval(v)
		if err != nil {
			return nil, err
		}
		out[k] = ev
	}
	return out, nil
}
