package stepfunctions

import (
	"encoding/json"
	"sort"
)

// Workflow variables — the Assign field, in both query languages.
//
// A state's Assign is evaluated against the variable values from before the
// state ran (every Assign in one state sees the same snapshot) and the new
// values take effect once the state's output is computed. A Parallel branch
// or Map iteration reads the variables of the scope around it but assigns
// into its own, so nothing it assigns is visible after the Parallel or Map —
// as on AWS, where data leaves a branch only through its output.
//
// In JSONPath states a variable is read as `$name` (or `$name.field`) in any
// path or intrinsic argument; in JSONata states as `$name`.

// varScope is one level of variable scope.
type varScope struct {
	parent *varScope
	vars   map[string]any
}

func newVarScope(parent *varScope) *varScope {
	return &varScope{parent: parent, vars: map[string]any{}}
}

// all flattens the scope chain, inner values shadowing outer ones.
func (s *varScope) all() map[string]any {
	if s == nil {
		return map[string]any{}
	}
	out := s.parent.all()
	for k, v := range s.vars {
		out[k] = v
	}
	return out
}

// assign sets variables in this scope.
func (s *varScope) assign(values map[string]any) {
	for k, v := range values {
		s.vars[k] = v
	}
}

// snapshot encodes this scope chain's values, for a redrive to restore.
func (s *varScope) snapshot() string {
	all := s.all()
	if len(all) == 0 {
		return ""
	}
	encoded, err := json.Marshal(all)
	if err != nil {
		return ""
	}
	return string(encoded)
}

// restoreVarScope rebuilds a top-level scope from snapshot.
func restoreVarScope(snapshot string) *varScope {
	scope := newVarScope(nil)
	if snapshot == "" {
		return scope
	}
	var values map[string]any
	if json.Unmarshal([]byte(snapshot), &values) == nil {
		scope.assign(values)
	}
	return scope
}

// assignedVariables renders assigned values the way stateExitedEventDetails
// reports them: each variable's value as a JSON string.
func assignedVariables(values map[string]any) map[string]string {
	if len(values) == 0 {
		return nil
	}
	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make(map[string]string, len(values))
	for _, k := range keys {
		encoded, err := json.Marshal(values[k])
		if err != nil {
			continue
		}
		out[k] = string(encoded)
	}
	return out
}

// evaluateAssign evaluates a state's (or a Choice rule's, or a catcher's)
// Assign. In JSONPath states `$` is doc — the state's result, its input for a
// Choice or Wait, the error output in a Catch; in JSONata states the
// expressions read $states.
func (in *interpreter) evaluateAssign(raw json.RawMessage, jsonataMode bool, doc any, ctxObj map[string]any, scope jsonataScope) (map[string]any, *stateError) {
	if len(raw) == 0 {
		return nil, nil
	}
	var rendered any
	if jsonataMode {
		scope.field = "Assign"
		value, _, serr := in.renderJSONata(raw, scope)
		if serr != nil {
			return nil, serr
		}
		rendered = value
	} else {
		value, err := renderPayloadTemplate(raw, doc, ctxObj)
		if err != nil {
			return nil, templateStateError(err)
		}
		rendered = value
	}
	values, ok := rendered.(map[string]any)
	if !ok {
		return nil, newStateError(errRuntime, "Assign must be a JSON object")
	}
	return values, nil
}
