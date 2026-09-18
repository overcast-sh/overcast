package stepfunctions

import (
	"encoding/json"
	"fmt"
	"strings"
)

// This file implements the Amazon States Language data-flow primitives: paths
// (`$.a.b[0]`, `$..x`, `$.l[?(@.n > 1)]`), the context object (`$$`),
// workflow variables (`$name`) and payload templates (objects whose keys end
// in `.$`). The JSONPath engine lives in jsonpath.go and the intrinsic
// functions templates may call live in intrinsics.go.

// pathError is a data-flow failure. It maps to States.Runtime, which is what
// AWS raises when a path cannot be resolved against the effective input.
type pathError struct{ msg string }

func (e *pathError) Error() string { return e.msg }

func pathErrorf(format string, args ...any) error {
	return &pathError{msg: fmt.Sprintf(format, args...)}
}

// variablesContextKey is the reserved context-object key under which the
// interpreter keeps workflow variables (a map[string]any of name to value).
// `$name` paths resolve against it; `$$` paths never see it.
const variablesContextKey = "__overcast_variables"

// selectPath resolves an ASL path against the effective input (doc), the
// execution context object (`$$`, ctxObj) or a workflow variable (`$name`).
//
// A definite path returns the node it names and fails when that node does not
// exist, matching AWS's behaviour for InputPath/ItemsPath and for `.$`
// fields. An indefinite path (wildcards, `..`, slices, unions, filters)
// returns a JSON array of its matches — empty when nothing matches — which is
// how AWS's Jayway-based evaluator behaves.
func selectPath(doc any, ctxObj map[string]any, path string) (any, error) {
	parsed, err := parseJSONPath(path)
	if err != nil {
		return nil, err
	}
	env := &pathEnv{doc: doc, ctxObj: ctxObj}
	return env.evaluate(parsed)
}

// selectPathOptional is selectPath but reports absence instead of erroring.
// Choice's Is* operators and IsPresent need "missing" as a value, not a fault.
func selectPathOptional(doc any, ctxObj map[string]any, path string) (any, bool) {
	value, err := selectPath(doc, ctxObj, path)
	if err != nil {
		return nil, false
	}
	return value, true
}

// isDefinitePath reports whether path is a valid ASL "Reference Path": one
// that names a single node (no wildcards, descent, slices, unions or
// filters). ResultPath and a Choice rule's Variable must be definite.
func isDefinitePath(path string) bool {
	parsed, err := parseJSONPath(path)
	return err == nil && parsed.definite()
}

// insertPath returns doc with value placed at the reference path. A `$` path
// replaces the document outright; missing intermediate objects are created,
// which is what ResultPath does on AWS. Only definite, field-only paths rooted
// at `$` can be written.
func insertPath(doc any, path string, value any) (any, error) {
	parsed, err := parseJSONPath(path)
	if err != nil {
		return nil, err
	}
	switch parsed.root {
	case rootContext:
		return nil, pathErrorf("path %q: the context object is read-only", path)
	case rootVariable:
		return nil, pathErrorf("path %q: variables can only be written by Assign", path)
	case rootInput, rootCurrent:
	}
	if !parsed.definite() {
		return nil, pathErrorf("path %q is not a reference path: wildcards, descendants, slices, unions and filters cannot be written to", path)
	}
	if len(parsed.steps) == 0 {
		return value, nil
	}
	for _, step := range parsed.steps {
		if step.kind == stepIndex {
			return nil, pathErrorf("path %q: array subscripts are not supported when writing a result", path)
		}
	}
	root, ok := doc.(map[string]any)
	if !ok {
		if doc == nil {
			root = map[string]any{}
		} else {
			return nil, pathErrorf("path %q: cannot write into a non-object payload", path)
		}
	}
	cloned := cloneJSON(root).(map[string]any)
	cursor := cloned
	last := len(parsed.steps) - 1
	for _, step := range parsed.steps[:last] {
		next, ok := cursor[step.names[0]].(map[string]any)
		if !ok {
			next = map[string]any{}
			cursor[step.names[0]] = next
		}
		cursor = next
	}
	cursor[parsed.steps[last].names[0]] = value
	return cloned, nil
}

// cloneJSON deep-copies a decoded JSON value so that a mutation on one state's
// data cannot alias another's.
func cloneJSON(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, item := range x {
			out[k] = cloneJSON(item)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, item := range x {
			out[i] = cloneJSON(item)
		}
		return out
	default:
		return v
	}
}

// ─── Payload templates ────────────────────────────────────────────────────────

// renderPayloadTemplate evaluates a Parameters/ResultSelector/ItemSelector
// template: every object key ending in `.$` is replaced by the value its path
// or intrinsic function resolves to, and the `.$` suffix is dropped.
func renderPayloadTemplate(tmpl json.RawMessage, doc any, ctxObj map[string]any) (any, error) {
	if len(tmpl) == 0 {
		return nil, nil
	}
	var decoded any
	if err := json.Unmarshal(tmpl, &decoded); err != nil {
		return nil, pathErrorf("payload template is not valid JSON: %v", err)
	}
	return renderTemplateValue(decoded, doc, ctxObj)
}

func renderTemplateValue(node any, doc any, ctxObj map[string]any) (any, error) {
	switch x := node.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for key, value := range x {
			if !strings.HasSuffix(key, ".$") {
				rendered, err := renderTemplateValue(value, doc, ctxObj)
				if err != nil {
					return nil, err
				}
				out[key] = rendered
				continue
			}
			expr, ok := value.(string)
			if !ok {
				return nil, pathErrorf("payload template key %q must have a string value", key)
			}
			resolved, err := evaluateTemplateExpression(expr, doc, ctxObj)
			if err != nil {
				return nil, err
			}
			out[strings.TrimSuffix(key, ".$")] = resolved
		}
		return out, nil
	case []any:
		out := make([]any, len(x))
		for i, item := range x {
			rendered, err := renderTemplateValue(item, doc, ctxObj)
			if err != nil {
				return nil, err
			}
			out[i] = rendered
		}
		return out, nil
	default:
		return node, nil
	}
}

// evaluateTemplateExpression resolves the right-hand side of a `.$` key:
// either a reference path or an intrinsic function call.
func evaluateTemplateExpression(expr string, doc any, ctxObj map[string]any) (any, error) {
	trimmed := strings.TrimSpace(expr)
	if strings.HasPrefix(trimmed, "States.") {
		return evaluateIntrinsic(trimmed, doc, ctxObj)
	}
	return selectPath(doc, ctxObj, trimmed)
}

func toNumber(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case int:
		return float64(x), true
	case json.Number:
		f, err := x.Float64()
		return f, err == nil
	}
	return 0, false
}
