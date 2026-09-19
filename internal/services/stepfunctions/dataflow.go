package stepfunctions

import (
	"encoding/json"
	"math"
	"time"
)

// flow is one state's data flow, in whichever query language the state uses.
//
// JSONPath: InputPath → Parameters → (the work) → ResultSelector → ResultPath
// → OutputPath, with Assign reading the result. JSONata: Arguments → (the
// work) → Output, with Assign and Output both reading $states. Everything
// below that differs between the two languages goes through here, so the
// state types themselves are written once.
type flow struct {
	in      *interpreter
	name    string
	state   *aslState
	jsonata bool
	// raw is the state's input as it arrived; effective is that input after
	// InputPath (JSONPath) — the same value for JSONata.
	raw       any
	effective any
	ctxObj    map[string]any
}

// newFlow resolves a state's input processing.
func (in *interpreter) newFlow(name string, state *aslState, raw any, ctxObj map[string]any) (*flow, *stateError) {
	f := &flow{in: in, name: name, state: state, jsonata: in.isJSONata(state), raw: raw, effective: raw, ctxObj: ctxObj}
	f.inspect(func(d *inspectionData, v string) { d.Input = v }, raw)
	if !f.jsonata {
		effective, serr := applyInputPath(state, raw, ctxObj)
		if serr != nil {
			return nil, serr
		}
		f.effective = effective
		f.inspect(func(d *inspectionData, v string) { d.AfterInputPath = v }, effective)
	}
	return f, nil
}

// isJSONata reports whether a state evaluates JSONata.
func (in *interpreter) isJSONata(state *aslState) bool {
	if state.QueryLanguage != "" {
		return state.QueryLanguage == queryLanguageJSONata
	}
	return in.queryLanguage == queryLanguageJSONata
}

// scope is the JSONata evaluation scope for one field of this state.
func (f *flow) scope(field string) jsonataScope {
	return jsonataScope{state: f.name, field: field, input: f.raw, ctxObj: f.ctxObj}
}

// withContext returns a copy of the flow evaluating against ctxObj — a Task
// retry's RetryCount, or $$.Task.Token for a callback.
func (f *flow) withContext(ctxObj map[string]any) *flow {
	copied := *f
	copied.ctxObj = ctxObj
	return &copied
}

// render evaluates a payload template: `.$` fields in JSONPath, "{% %}"
// expressions in JSONata. An absent template yields fallback.
func (f *flow) render(field string, tmpl json.RawMessage, doc any, fallback any) (any, *stateError) {
	if len(tmpl) == 0 {
		return fallback, nil
	}
	if f.jsonata {
		value, defined, serr := f.in.renderJSONata(tmpl, f.scope(field))
		if serr != nil {
			return nil, serr
		}
		if !defined {
			return nil, f.in.queryError(f.scope(field), "the %s expression evaluated to undefined", field)
		}
		return value, nil
	}
	value, err := renderPayloadTemplate(tmpl, doc, f.ctxObj)
	if err != nil {
		return nil, templateStateError(err)
	}
	return value, nil
}

// arguments is a Task's (or a Parallel's) payload: Parameters over the
// effective input in JSONPath, Arguments in JSONata, the input itself when
// neither is set.
func (f *flow) arguments() (any, *stateError) {
	if f.jsonata {
		args, serr := f.render("Arguments", f.state.Arguments, nil, f.raw)
		f.inspect(func(d *inspectionData, v string) { d.AfterArguments = v }, args)
		return args, serr
	}
	params, serr := f.render("Parameters", f.state.Parameters, f.effective, f.effective)
	f.inspect(func(d *inspectionData, v string) { d.AfterParameters = v }, params)
	return params, serr
}

// number resolves a numeric field that JSONPath may give as a `...Path` and
// JSONata as an expression. set is false when the state declares neither.
func (f *flow) number(n aslNumber, path, field string) (float64, bool, *stateError) {
	switch {
	case path != "":
		value, err := selectPath(f.effective, f.ctxObj, path)
		if err != nil {
			return 0, false, newStateError(errRuntime, "%s", err.Error())
		}
		number, ok := toNumber(value)
		if !ok {
			return 0, false, newStateError(errRuntime, "%sPath %q did not resolve to a number", field, path)
		}
		return number, true, nil
	case n.Expr != "":
		value, defined, serr := f.in.evalJSONata(n.Expr, f.scope(field))
		if serr != nil {
			return 0, false, serr
		}
		number, ok := toNumber(value)
		if !defined || !ok {
			return 0, false, f.in.queryError(f.scope(field), "%s must evaluate to a number", field)
		}
		return number, true, nil
	case n.Set:
		return n.Value, true, nil
	}
	return 0, false, nil
}

// positiveSeconds resolves TimeoutSeconds/HeartbeatSeconds into a whole
// number of seconds, nil when the state declared none.
func (f *flow) positiveSeconds(n aslNumber, path, field string) (*int64, *stateError) {
	value, set, serr := f.number(n, path, field)
	if serr != nil || !set {
		return nil, serr
	}
	if value <= 0 || value != math.Trunc(value) {
		return nil, newStateError(errRuntime, "%s must be a positive integer", field)
	}
	seconds := int64(value)
	return &seconds, nil
}

// nonNegativeInt resolves an integer field such as MaxConcurrency.
func (f *flow) nonNegativeInt(n aslNumber, path, field string) (int, bool, *stateError) {
	value, set, serr := f.number(n, path, field)
	if serr != nil || !set {
		return 0, set, serr
	}
	if value < 0 || value != math.Trunc(value) {
		return 0, false, newStateError(errRuntime, "%s must be a non-negative integer", field)
	}
	return int(value), true, nil
}

// finishResult turns a Task/Parallel/Map/Pass result into the state's output
// and the variables it assigns.
func (f *flow) finishResult(result any) (any, map[string]any, *stateError) {
	f.inspect(func(d *inspectionData, v string) { d.Result = v }, result)
	if f.jsonata {
		scope := f.scope("Output")
		scope.result, scope.hasResult = result, true
		return f.jsonataOutput(f.state.Output, f.state.Assign, scope, result)
	}
	if len(f.state.ResultSelector) > 0 {
		selected, err := renderPayloadTemplate(f.state.ResultSelector, result, f.ctxObj)
		if err != nil {
			return nil, nil, templateStateError(err)
		}
		result = selected
		f.inspect(func(d *inspectionData, v string) { d.AfterResultSelector = v }, result)
	}
	assigned, serr := f.in.evaluateAssign(f.state.Assign, false, result, f.ctxObj, jsonataScope{})
	if serr != nil {
		return nil, nil, serr
	}
	combined, serr := applyResultPath(f.state.ResultPath, f.raw, result)
	if serr != nil {
		return nil, nil, serr
	}
	f.inspect(func(d *inspectionData, v string) { d.AfterResultPath = v }, combined)
	output, serr := applyOutputPath(f.state, combined, f.ctxObj)
	return output, assigned, serr
}

// finishPassthrough is finishResult for the states whose output is their
// input — Choice, Wait, Succeed: OutputPath in JSONPath, Output (defaulting to
// the input) in JSONata. assign and output are the Assign/Output that apply:
// a Choice rule's own, or the state's.
func (f *flow) finishPassthrough(assign, output json.RawMessage) (any, map[string]any, *stateError) {
	if f.jsonata {
		return f.jsonataOutput(output, assign, f.scope("Output"), f.raw)
	}
	assigned, serr := f.in.evaluateAssign(assign, false, f.effective, f.ctxObj, jsonataScope{})
	if serr != nil {
		return nil, nil, serr
	}
	out, serr := applyOutputPath(f.state, f.effective, f.ctxObj)
	return out, assigned, serr
}

// finishCatch builds the output of a state whose error a catcher handled.
func (f *flow) finishCatch(catcher *aslCatcher, errorOutput map[string]any) (any, map[string]any, *stateError) {
	if f.jsonata {
		scope := f.scope("Catch.Output")
		scope.errorOutput, scope.hasError = errorOutput, true
		return f.jsonataOutput(catcher.Output, catcher.Assign, scope, f.raw)
	}
	assigned, serr := f.in.evaluateAssign(catcher.Assign, false, errorOutput, f.ctxObj, jsonataScope{})
	if serr != nil {
		return nil, nil, serr
	}
	output, serr := applyResultPath(catcher.ResultPath, f.raw, errorOutput)
	return output, assigned, serr
}

// jsonataOutput evaluates a JSONata Output (defaulting to fallback) and
// Assign against the same scope — both see the variables from before the
// state, as on AWS.
func (f *flow) jsonataOutput(output, assign json.RawMessage, scope jsonataScope, fallback any) (any, map[string]any, *stateError) {
	assigned, serr := f.in.evaluateAssign(assign, true, nil, f.ctxObj, scope)
	if serr != nil {
		return nil, nil, serr
	}
	if len(output) == 0 {
		return fallback, assigned, nil
	}
	scope.field = "Output"
	value, defined, serr := f.in.renderJSONata(output, scope)
	if serr != nil {
		return nil, nil, serr
	}
	if !defined {
		return nil, nil, f.in.queryError(scope, "the Output expression evaluated to undefined")
	}
	return value, assigned, nil
}

// text resolves a string field — a Fail's Error or Cause — that JSONPath may
// give through a `...Path` (or an intrinsic) and JSONata as an expression.
func (f *flow) text(value, path, field string) (string, *stateError) {
	if path != "" {
		resolved, err := evaluateTemplateExpression(path, f.effective, f.ctxObj)
		if err != nil {
			return "", newStateError(errRuntime, "%s", err.Error())
		}
		text, ok := resolved.(string)
		if !ok {
			return "", newStateError(errRuntime, "%sPath must resolve to a string", field)
		}
		return text, nil
	}
	if expr, ok := isJSONataExpression(value); ok && f.jsonata {
		resolved, defined, serr := f.in.evalJSONata(expr, f.scope(field))
		if serr != nil {
			return "", serr
		}
		text, isString := resolved.(string)
		if !defined || !isString {
			return "", f.in.queryError(f.scope(field), "%s must evaluate to a string", field)
		}
		return text, nil
	}
	return value, nil
}

// waitUntil resolves a Wait state's target into a delay from now.
func (f *flow) waitDuration(now time.Time) (time.Duration, *stateError) {
	state := f.state
	if seconds, set, serr := f.number(state.Seconds, state.SecondsPath, "Seconds"); serr != nil || set {
		if serr != nil {
			return 0, serr
		}
		if seconds < 0 {
			return 0, newStateError(errRuntime, "Seconds must not be negative")
		}
		return time.Duration(seconds * float64(time.Second)), nil
	}
	var timestamp string
	if state.TimestampPath != "" {
		value, err := selectPath(f.effective, f.ctxObj, state.TimestampPath)
		if err != nil {
			return 0, newStateError(errRuntime, "%s", err.Error())
		}
		text, ok := value.(string)
		if !ok {
			return 0, newStateError(errRuntime, "TimestampPath %q did not resolve to a string", state.TimestampPath)
		}
		timestamp = text
	} else {
		text, serr := f.text(state.Timestamp, "", "Timestamp")
		if serr != nil {
			return 0, serr
		}
		timestamp = text
	}
	if timestamp == "" {
		return 0, nil
	}
	target, err := parseASLTimestamp(timestamp)
	if err != nil {
		return 0, newStateError(errRuntime, "%s", err.Error())
	}
	return maxDuration(target.Sub(now), 0), nil
}
