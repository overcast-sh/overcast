package stepfunctions

import (
	"crypto/md5"  //nolint:gosec // $hash offers MD5 because AWS does; it is not used for security.
	"crypto/sha1" //nolint:gosec // $hash offers SHA-1 because AWS does; it is not used for security.
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"math"
	"math/rand/v2"
	"strings"

	jsonata "github.com/blues/jsonata-go"
	"github.com/blues/jsonata-go/jtypes"
	"github.com/google/uuid"
)

// JSONata: `"QueryLanguage": "JSONata"` on the definition or on one state.
//
// A JSONata state reads its input and writes its output through JSONata
// expressions — strings of the form "{% expression %}" anywhere inside
// Arguments, Output, Assign, Items, ItemSelector, a Choice rule's Condition,
// a Wait's Seconds or Timestamp, a Fail's Error or Cause, and the numeric
// fields TimeoutSeconds, HeartbeatSeconds, MaxConcurrency and the tolerated
// failure thresholds. Every expression sees the reserved $states variable —
// $states.input, $states.context, and $states.result or $states.errorOutput
// where they exist — plus the workflow's variables.
//
// The engine is github.com/blues/jsonata-go, which implements JSONata 1.5.
// AWS runs JSONata 2.x: the 1.x function library is all there, and the
// functions AWS adds ($partition, $range, $hash, $random, $uuid, $parse) are
// registered below, but JSONata-2-only functions such as $formatInteger,
// $parseInteger and $eval fail the evaluation with States.QueryEvaluationError.

// isJSONataExpression reports whether s is a "{% … %}" expression and returns
// the expression inside.
func isJSONataExpression(s string) (string, bool) {
	trimmed := strings.TrimSpace(s)
	if len(trimmed) >= 4 && strings.HasPrefix(trimmed, "{%") && strings.HasSuffix(trimmed, "%}") {
		return strings.TrimSpace(trimmed[2 : len(trimmed)-2]), true
	}
	return "", false
}

// jsonataScope is what one evaluation can see.
type jsonataScope struct {
	// state and field name where an expression failed, for the error.
	state string
	field string

	input  any
	ctxObj map[string]any

	result      any
	hasResult   bool
	errorOutput any
	hasError    bool
}

// statesVariable builds $states.
func (s jsonataScope) statesVariable() map[string]any {
	states := map[string]any{
		"input":   s.input,
		"context": withoutVariables(s.ctxObj),
	}
	if s.hasResult {
		states["result"] = s.result
	}
	if s.hasError {
		states["errorOutput"] = s.errorOutput
	}
	return states
}

// withoutVariables returns the context object without the variables Overcast
// carries alongside it.
func withoutVariables(ctxObj map[string]any) map[string]any {
	if _, ok := ctxObj[variablesContextKey]; !ok {
		return ctxObj
	}
	out := make(map[string]any, len(ctxObj))
	for k, v := range ctxObj {
		if k != variablesContextKey {
			out[k] = v
		}
	}
	return out
}

// queryError is a JSONata evaluation failure: States.QueryEvaluationError,
// which Retry and Catch can handle, recorded as an EvaluationFailed event.
func (in *interpreter) queryError(scope jsonataScope, format string, args ...any) *stateError {
	serr := newStateError(errQueryEvaluationError, format, args...)
	in.record(HistoryEvent{
		Type: evtEvaluationFailed,
		EvaluationFailed: &evaluationFailedDetails{
			Error:    serr.name,
			Cause:    serr.cause,
			Location: scope.field,
			State:    scope.state,
		},
	})
	return serr
}

// evalJSONata evaluates one expression. defined is false when the expression
// produced no value (JSONata's undefined).
func (in *interpreter) evalJSONata(expr string, scope jsonataScope) (value any, defined bool, serr *stateError) {
	compiled, err := jsonata.Compile(expr)
	if err != nil {
		return nil, false, in.queryError(scope, "the JSONata expression %q in %s is not valid: %v", expr, scope.field, err)
	}
	vars := map[string]any{}
	if all, ok := scope.ctxObj[variablesContextKey].(map[string]any); ok {
		for k, v := range all {
			vars[k] = v
		}
	}
	vars["states"] = scope.statesVariable()
	if err := compiled.RegisterVars(vars); err != nil {
		return nil, false, in.queryError(scope, "the variables for %s could not be bound: %v", scope.field, err)
	}
	if err := compiled.RegisterExts(in.jsonataFunctions()); err != nil {
		return nil, false, in.queryError(scope, "%v", err)
	}
	result, err := compiled.Eval(scope.input)
	if errors.Is(err, jsonata.ErrUndefined) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, in.queryError(scope, "the JSONata expression %q in %s failed: %v", expr, scope.field, err)
	}
	normalized, err := normalizeJSON(result)
	if err != nil {
		return nil, false, in.queryError(scope, "the JSONata expression %q in %s returned a value that is not JSON: %v", expr, scope.field, err)
	}
	return normalized, true, nil
}

// normalizeJSON round-trips a value through encoding/json so the rest of the
// interpreter only ever sees the types json.Unmarshal produces.
func normalizeJSON(v any) (any, error) {
	encoded, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var out any
	if err := json.Unmarshal(encoded, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// renderJSONata evaluates a JSONata-mode field: a "{% %}" string is an
// expression, objects and arrays are walked and every expression string in
// them evaluated, and anything else is a literal. An object member whose
// expression is undefined is left out, as on AWS.
func (in *interpreter) renderJSONata(raw json.RawMessage, scope jsonataScope) (any, bool, *stateError) {
	var node any
	if err := json.Unmarshal(raw, &node); err != nil {
		return nil, false, newStateError(errRuntime, "%s is not valid JSON: %v", scope.field, err)
	}
	return in.renderJSONataValue(node, scope)
}

func (in *interpreter) renderJSONataValue(node any, scope jsonataScope) (any, bool, *stateError) {
	switch x := node.(type) {
	case string:
		if expr, ok := isJSONataExpression(x); ok {
			return in.evalJSONata(expr, scope)
		}
		return x, true, nil
	case map[string]any:
		out := make(map[string]any, len(x))
		for key, value := range x {
			rendered, defined, serr := in.renderJSONataValue(value, scope)
			if serr != nil {
				return nil, false, serr
			}
			if defined {
				out[key] = rendered
			}
		}
		return out, true, nil
	case []any:
		out := make([]any, 0, len(x))
		for _, value := range x {
			rendered, defined, serr := in.renderJSONataValue(value, scope)
			if serr != nil {
				return nil, false, serr
			}
			if defined {
				out = append(out, rendered)
			}
		}
		return out, true, nil
	}
	return node, true, nil
}

// ─── AWS's JSONata functions ──────────────────────────────────────────────────

// jsonataFunctions are the functions Step Functions adds to JSONata, plus
// $now and $millis read from the injected clock rather than the host's.
func (in *interpreter) jsonataFunctions() map[string]jsonata.Extension {
	now := in.handler.clk.Now()
	return map[string]jsonata.Extension{
		"partition": {Func: jsonataPartition},
		"range":     {Func: jsonataRange},
		"hash":      {Func: jsonataHash},
		"random":    {Func: jsonataRandom},
		"uuid":      {Func: func() string { return uuid.NewString() }},
		"parse":     {Func: jsonataParse},
		"millis":    {Func: func() float64 { return float64(now.UnixMilli()) }},
		"now":       {Func: func() string { return now.UTC().Format("2006-01-02T15:04:05.000Z") }},
	}
}

func jsonataPartition(array []any, size float64) ([]any, error) {
	if size < 1 || size != math.Trunc(size) {
		return nil, fmt.Errorf("$partition: the chunk size must be a positive integer")
	}
	n := int(size)
	out := make([]any, 0, (len(array)+n-1)/n)
	for i := 0; i < len(array); i += n {
		end := min(i+n, len(array))
		out = append(out, append([]any(nil), array[i:end]...))
	}
	return out, nil
}

func jsonataRange(start, end, step float64) ([]any, error) {
	if step == 0 {
		return nil, fmt.Errorf("$range: the step must not be zero")
	}
	var out []any
	for v := start; (step > 0 && v <= end) || (step < 0 && v >= end); v += step {
		out = append(out, v)
		if len(out) > 1000 {
			return nil, fmt.Errorf("$range: the result exceeds 1000 items")
		}
	}
	if out == nil {
		out = []any{}
	}
	return out, nil
}

func jsonataHash(value any, algorithm string) (string, error) {
	var text string
	switch v := value.(type) {
	case string:
		text = v
	default:
		encoded, err := json.Marshal(v)
		if err != nil {
			return "", err
		}
		text = string(encoded)
	}
	var h hash.Hash
	switch algorithm {
	case "MD5":
		h = md5.New() //nolint:gosec // see the import
	case "SHA-1":
		h = sha1.New() //nolint:gosec // see the import
	case "SHA-256":
		h = sha256.New()
	case "SHA-384":
		h = sha512.New384()
	case "SHA-512":
		h = sha512.New()
	default:
		return "", fmt.Errorf("$hash: algorithm %q is not one of MD5, SHA-1, SHA-256, SHA-384, SHA-512", algorithm)
	}
	h.Write([]byte(text))
	return hex.EncodeToString(h.Sum(nil)), nil
}

// jsonataRandom is $random([seed]): a number in [0, 1), deterministic for a
// given seed.
func jsonataRandom(seed jtypes.OptionalFloat64) float64 {
	if seed.IsSet() {
		s := uint64(int64(seed.Float64))
		return rand.New(rand.NewPCG(s, s^0x9e3779b97f4a7c15)).Float64() //nolint:gosec // not for security
	}
	return rand.Float64() //nolint:gosec // not for security
}

func jsonataParse(text string) (any, error) {
	var out any
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		return nil, fmt.Errorf("$parse: %v", err)
	}
	return out, nil
}
