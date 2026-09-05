package scenario

import (
	"fmt"
	"strings"
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
	default:
		return nil, fmt.Errorf("unknown value expression %q", key)
	}
}

// name is $name: "{runId}-{group}-{suffix}", with the group token the whole
// group name and no shortening anywhere. That is what makes the name-hygiene
// convention hold by construction, and it is what lets the orphan sweep find
// anything a crashed run left behind.
func (e *evaluator) name(suffix string) string {
	return e.runID + "-" + e.group + "-" + suffix
}

// evalParams evaluates a call's input members. The returned map is what is
// marshalled into --cli-input-json.
func (e *evaluator) evalParams(params map[string]any) (map[string]any, error) {
	out := make(map[string]any, len(params))
	for k, v := range params {
		ev, err := e.eval(v)
		if err != nil {
			return nil, err
		}
		out[k] = ev
	}
	return out, nil
}
