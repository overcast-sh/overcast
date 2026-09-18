package stepfunctions

import (
	"context"
	"crypto/md5"  //nolint:gosec // $hash offers MD5 because AWS does; it is not used for security.
	"crypto/sha1" //nolint:gosec // $hash offers SHA-1 because AWS does; it is not used for security.
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash"
	"math"
	"math/rand/v2"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/recolabs/gnata"
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
// The engine is github.com/recolabs/gnata, a JSONata 2.x implementation that
// passes the jsonata-js 2.2 test suite. AWS runs JSONata 2.0.6, so the whole
// 2.x function library and language (parent operator, @/# bindings,
// transforms, regex functions, date pictures) is available. On top of it:
//
//   - the functions AWS adds — $partition, $range, $hash, $random (with an
//     optional seed), $uuid and $parse — are registered below;
//   - $now and $millis read the injected clock, fixed for one evaluation;
//   - $eval is removed, because Step Functions does not offer it;
//   - an evaluation is cut off after one second, AWS's expression timeout.

// jsonataTimeout is AWS's limit on one expression's evaluation.
const jsonataTimeout = time.Second

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
	vars := map[string]any{}
	if all, ok := scope.ctxObj[variablesContextKey].(map[string]any); ok {
		for k, v := range all {
			vars[k] = v
		}
	}
	vars["states"] = scope.statesVariable()
	value, defined, err := evaluateJSONata(expr, scope.input, vars, in.handler.clk.Now())
	if err != nil {
		return nil, false, in.queryError(scope, "the JSONata expression %q in %s failed: %v", expr, scope.field, err)
	}
	return value, defined, nil
}

// evaluateJSONata evaluates expr against input with vars bound as $name
// variables and $now/$millis reading now. The result has the types
// json.Unmarshal produces; defined is false for JSONata's undefined.
func evaluateJSONata(expr string, input any, vars map[string]any, now time.Time) (value any, defined bool, err error) {
	compiled, err := gnata.Compile(expr, gnata.WithTimeout(jsonataTimeout))
	if err != nil {
		return nil, false, fmt.Errorf("the expression is not valid: %w", err)
	}
	data, err := toJSONataValue(input)
	if err != nil {
		return nil, false, err
	}
	bound := make(map[string]any, len(vars))
	for name, v := range vars {
		if bound[name], err = toJSONataValue(v); err != nil {
			return nil, false, fmt.Errorf("variable $%s: %w", name, err)
		}
	}
	env := gnata.NewCustomEnvironment(jsonataFunctions(now))
	result, err := compiled.EvalWithCustomEnvironmentAndVars(context.Background(), data, env, bound)
	if err != nil {
		return nil, false, err
	}
	if gnata.IsNull(result) {
		return nil, true, nil
	}
	if result == nil {
		return nil, false, nil
	}
	normalized, err := normalizeJSON(gnata.NormalizeValue(result))
	if err != nil {
		return nil, false, fmt.Errorf("the result is not JSON: %w", err)
	}
	return normalized, true, nil
}

// toJSONataValue converts a decoded JSON value to the evaluator's own
// representation, which keeps JSON null distinct from undefined.
func toJSONataValue(v any) (any, error) {
	encoded, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return gnata.DecodeJSON(encoded)
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

// jsonataFromMillis formats $now's picture and timezone with the standard
// library's $fromMillis, so $now(picture, tz) formats exactly as it does.
var jsonataFromMillis = func() *gnata.Expression {
	expr, err := gnata.Compile(`$fromMillis($ms, $picture, $tz)`)
	if err != nil {
		panic(err)
	}
	return expr
}()

// jsonataFunctions are the functions Step Functions adds to or changes in
// JSONata: AWS's additions, $random with a seed, $now and $millis read from
// the injected clock rather than the host's, and $eval withdrawn.
func jsonataFunctions(now time.Time) map[string]gnata.CustomFunc {
	millis := float64(now.UnixMilli())
	return map[string]gnata.CustomFunc{
		"partition": jsonataPartition,
		"range":     jsonataRange,
		"hash":      jsonataHash,
		"random":    jsonataRandom,
		"uuid":      func([]any, any) (any, error) { return uuid.NewString(), nil },
		"parse":     jsonataParse,
		"millis":    func([]any, any) (any, error) { return millis, nil },
		"now": func(args []any, _ any) (any, error) {
			if len(args) == 0 || args[0] == nil {
				return now.UTC().Format("2006-01-02T15:04:05.000Z"), nil
			}
			vars := map[string]any{"ms": millis, "picture": args[0]}
			if len(args) > 1 {
				vars["tz"] = args[1]
			}
			return jsonataFromMillis.EvalWithVars(context.Background(), nil, vars)
		},
		"eval": func([]any, any) (any, error) {
			return nil, fmt.Errorf("$eval is not available in Step Functions; use $parse to read JSON text")
		},
	}
}

// jsonataNumber reads a numeric argument.
func jsonataNumber(fn string, args []any, i int) (float64, error) {
	if i >= len(args) || args[i] == nil {
		return 0, fmt.Errorf("%s: argument %d is required", fn, i+1)
	}
	switch v := args[i].(type) {
	case float64:
		return v, nil
	case int:
		return float64(v), nil
	case int64:
		return float64(v), nil
	case json.Number:
		return v.Float64()
	}
	return 0, fmt.Errorf("%s: argument %d must be a number", fn, i+1)
}

// jsonataInteger reads an integer argument, rounding down as AWS does for
// the functions it adds.
func jsonataInteger(fn string, args []any, i int) (int, error) {
	f, err := jsonataNumber(fn, args, i)
	if err != nil {
		return 0, err
	}
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return 0, fmt.Errorf("%s: argument %d must be finite", fn, i+1)
	}
	return int(math.Floor(f)), nil
}

func jsonataPartition(args []any, _ any) (any, error) {
	if len(args) == 0 || args[0] == nil {
		return nil, fmt.Errorf("$partition: the array to partition is required")
	}
	array, ok := args[0].([]any)
	if !ok {
		array = []any{args[0]}
	}
	n, err := jsonataInteger("$partition", args, 1)
	if err != nil {
		return nil, err
	}
	if n < 1 {
		return nil, fmt.Errorf("$partition: the chunk size must be a positive integer")
	}
	out := make([]any, 0, (len(array)+n-1)/n)
	for i := 0; i < len(array); i += n {
		end := min(i+n, len(array))
		out = append(out, append([]any(nil), array[i:end]...))
	}
	return out, nil
}

func jsonataRange(args []any, _ any) (any, error) {
	var bounds [3]int
	for i := range bounds {
		v, err := jsonataInteger("$range", args, i)
		if err != nil {
			return nil, err
		}
		bounds[i] = v
	}
	start, end, step := bounds[0], bounds[1], bounds[2]
	if step == 0 {
		return nil, fmt.Errorf("$range: the step must not be zero")
	}
	out := []any{}
	for v := start; (step > 0 && v <= end) || (step < 0 && v >= end); v += step {
		out = append(out, float64(v))
		if len(out) > 1000 {
			return nil, fmt.Errorf("$range: the result exceeds 1000 items")
		}
	}
	return out, nil
}

func jsonataHash(args []any, _ any) (any, error) {
	if len(args) < 2 || args[0] == nil {
		return nil, fmt.Errorf("$hash: the value and the algorithm are required")
	}
	var text string
	switch v := args[0].(type) {
	case string:
		text = v
	default:
		encoded, err := json.Marshal(v)
		if err != nil {
			return nil, err
		}
		text = string(encoded)
	}
	algorithm, _ := args[1].(string)
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
		return nil, fmt.Errorf("$hash: algorithm %v is not one of MD5, SHA-1, SHA-256, SHA-384, SHA-512", args[1])
	}
	h.Write([]byte(text))
	return hex.EncodeToString(h.Sum(nil)), nil
}

// jsonataRandom is $random([seed]): a number in [0, 1), deterministic for a
// given seed.
func jsonataRandom(args []any, _ any) (any, error) {
	if len(args) == 0 || args[0] == nil {
		return rand.Float64(), nil //nolint:gosec // not for security
	}
	seed, err := jsonataInteger("$random", args, 0)
	if err != nil {
		return nil, err
	}
	s := uint64(int64(seed))
	return rand.New(rand.NewPCG(s, s^0x9e3779b97f4a7c15)).Float64(), nil //nolint:gosec // not for security
}

func jsonataParse(args []any, _ any) (any, error) {
	if len(args) == 0 || args[0] == nil {
		return nil, fmt.Errorf("$parse: the JSON text is required")
	}
	text, ok := args[0].(string)
	if !ok {
		return nil, fmt.Errorf("$parse: the argument must be a string")
	}
	if !json.Valid([]byte(text)) {
		var probe any
		err := json.Unmarshal([]byte(text), &probe)
		return nil, fmt.Errorf("$parse: %v", err)
	}
	return gnata.DecodeJSON(json.RawMessage(text))
}
