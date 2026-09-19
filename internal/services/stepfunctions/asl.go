package stepfunctions

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// State type names, as spelled in the Amazon States Language.
const (
	stateTypePass     = "Pass"
	stateTypeTask     = "Task"
	stateTypeChoice   = "Choice"
	stateTypeWait     = "Wait"
	stateTypeSucceed  = "Succeed"
	stateTypeFail     = "Fail"
	stateTypeParallel = "Parallel"
	stateTypeMap      = "Map"
)

// aslStateTypes is the complete set of Amazon States Language state types.
// Anything outside it is an invalid definition, exactly as on AWS — not an
// Overcast gap. Overcast interprets all eight; the gaps are in individual
// fields and Task resource integrations, which fail loudly at run time.
var aslStateTypes = map[string]bool{
	stateTypePass:     true,
	stateTypeTask:     true,
	stateTypeChoice:   true,
	stateTypeWait:     true,
	stateTypeSucceed:  true,
	stateTypeFail:     true,
	stateTypeParallel: true,
	stateTypeMap:      true,
}

// aslBranch is a state machine body: a start state plus a named state map.
// The top-level definition, every Parallel branch and every Map processor
// share this shape.
type aslBranch struct {
	Comment        string               `json:"Comment"`
	StartAt        string               `json:"StartAt"`
	States         map[string]*aslState `json:"States"`
	TimeoutSeconds int                  `json:"TimeoutSeconds"`
	Version        string               `json:"Version"`
	QueryLanguage  string               `json:"QueryLanguage"`

	// ProcessorConfig appears on a Map ItemProcessor: Mode INLINE or
	// DISTRIBUTED, and a distributed Map's ExecutionType.
	ProcessorConfig json.RawMessage `json:"ProcessorConfig"`

	// order is the deterministic iteration order of States, used only for
	// error messages so that a definition with several problems reports the
	// same one on every run.
	order []string
}

// aslState is one state in a branch. Fields not valid for a state's Type are
// simply unread; validation rejects the combinations AWS rejects.
type aslState struct {
	Type    string `json:"Type"`
	Comment string `json:"Comment"`
	Next    string `json:"Next"`
	End     bool   `json:"End"`

	// QueryLanguage may be set per state as well as on the whole definition:
	// a JSONata state inside a JSONPath state machine is legal ASL.
	QueryLanguage string `json:"QueryLanguage"`

	// JSONata data flow: Arguments replaces Parameters, Output replaces
	// ResultSelector/ResultPath/OutputPath, Items replaces ItemsPath. Assign
	// (variables) is valid in both query languages.
	Arguments json.RawMessage `json:"Arguments"`
	Output    json.RawMessage `json:"Output"`
	Assign    json.RawMessage `json:"Assign"`
	Items     json.RawMessage `json:"Items"`

	// Input/output processing. aslPath distinguishes "absent" from an explicit
	// JSON null, which has its own meaning for all three paths.
	InputPath      aslPath         `json:"InputPath"`
	OutputPath     aslPath         `json:"OutputPath"`
	ResultPath     aslPath         `json:"ResultPath"`
	Parameters     json.RawMessage `json:"Parameters"`
	ResultSelector json.RawMessage `json:"ResultSelector"`

	// Pass
	Result json.RawMessage `json:"Result"`

	// Task. HeartbeatSeconds governs activity tasks and `.waitForTaskToken`.
	// Credentials is deliberately unread — Overcast has a single account, so
	// assuming a cross-account role is a no-op rather than a behaviour that
	// could silently differ.
	Resource             string          `json:"Resource"`
	TimeoutSeconds       aslNumber       `json:"TimeoutSeconds"`
	TimeoutSecondsPath   string          `json:"TimeoutSecondsPath"`
	HeartbeatSeconds     aslNumber       `json:"HeartbeatSeconds"`
	HeartbeatSecondsPath string          `json:"HeartbeatSecondsPath"`
	Credentials          json.RawMessage `json:"Credentials"`
	Retry                []aslRetrier    `json:"Retry"`
	Catch                []aslCatcher    `json:"Catch"`

	// Choice
	Choices []*aslChoiceRule `json:"Choices"`
	Default string           `json:"Default"`

	// Wait
	Seconds       aslNumber `json:"Seconds"`
	SecondsPath   string    `json:"SecondsPath"`
	Timestamp     string    `json:"Timestamp"`
	TimestampPath string    `json:"TimestampPath"`

	// Fail
	Error     string `json:"Error"`
	ErrorPath string `json:"ErrorPath"`
	Cause     string `json:"Cause"`
	CausePath string `json:"CausePath"`

	// Parallel
	Branches []*aslBranch `json:"Branches"`

	// Map
	Iterator           *aslBranch      `json:"Iterator"`
	ItemProcessor      *aslBranch      `json:"ItemProcessor"`
	ItemsPath          string          `json:"ItemsPath"`
	ItemSelector       json.RawMessage `json:"ItemSelector"`
	MaxConcurrency     aslNumber       `json:"MaxConcurrency"`
	MaxConcurrencyPath string          `json:"MaxConcurrencyPath"`
	ItemReader         json.RawMessage `json:"ItemReader"`
	Label              string          `json:"Label"`

	ToleratedFailureCount          aslNumber       `json:"ToleratedFailureCount"`
	ToleratedFailureCountPath      string          `json:"ToleratedFailureCountPath"`
	ToleratedFailurePercentage     aslNumber       `json:"ToleratedFailurePercentage"`
	ToleratedFailurePercentagePath string          `json:"ToleratedFailurePercentagePath"`
	ItemBatcher                    json.RawMessage `json:"ItemBatcher"`
	ResultWriter                   json.RawMessage `json:"ResultWriter"`
}

// aslNumber is a numeric field that a JSONata state may instead give as a
// "{% expression %}" string: TimeoutSeconds, HeartbeatSeconds, a Wait's
// Seconds, MaxConcurrency and the tolerated failure thresholds.
type aslNumber struct {
	Set   bool
	Value float64
	Expr  string
}

// UnmarshalJSON accepts a JSON number or a JSONata expression string.
func (n *aslNumber) UnmarshalJSON(data []byte) error {
	n.Set = true
	var text string
	if json.Unmarshal(data, &text) == nil {
		expr, ok := isJSONataExpression(text)
		if !ok {
			return fmt.Errorf("expected a number or a JSONata expression, got %q", text)
		}
		n.Expr = expr
		return nil
	}
	return json.Unmarshal(data, &n.Value)
}

// aslRetrier is a Retry entry on a Task/Parallel/Map state.
type aslRetrier struct {
	ErrorEquals     []string `json:"ErrorEquals"`
	IntervalSeconds *float64 `json:"IntervalSeconds"`
	MaxAttempts     *int     `json:"MaxAttempts"`
	BackoffRate     *float64 `json:"BackoffRate"`
	MaxDelaySeconds *float64 `json:"MaxDelaySeconds"`
	JitterStrategy  string   `json:"JitterStrategy"`
}

// interval returns the retrier's first-attempt delay in seconds (AWS default 1).
func (r aslRetrier) interval() float64 {
	if r.IntervalSeconds == nil {
		return 1
	}
	return *r.IntervalSeconds
}

// maxAttempts returns the retrier's attempt cap (AWS default 3).
func (r aslRetrier) maxAttempts() int {
	if r.MaxAttempts == nil {
		return 3
	}
	return *r.MaxAttempts
}

// backoffRate returns the retrier's multiplier (AWS default 2.0).
func (r aslRetrier) backoffRate() float64 {
	if r.BackoffRate == nil {
		return 2.0
	}
	return *r.BackoffRate
}

// aslCatcher is a Catch entry on a Task/Parallel/Map state. Assign and Output
// carry the same meaning here as on a state, with the error output as the
// data they read.
type aslCatcher struct {
	ErrorEquals []string        `json:"ErrorEquals"`
	Next        string          `json:"Next"`
	ResultPath  aslPath         `json:"ResultPath"`
	Assign      json.RawMessage `json:"Assign"`
	Output      json.RawMessage `json:"Output"`
}

// aslPath is an optional reference-path field. ASL gives "absent" and an
// explicit JSON null different meanings for InputPath, OutputPath and
// ResultPath, so a plain *string (which decodes null as nil) cannot represent
// them — this type keeps the two apart.
type aslPath struct {
	Set   bool
	Null  bool
	Value string
}

// UnmarshalJSON records whether the field was present and whether it was null.
func (p *aslPath) UnmarshalJSON(data []byte) error {
	p.Set = true
	if string(data) == "null" {
		p.Null = true
		p.Value = ""
		return nil
	}
	return json.Unmarshal(data, &p.Value)
}

// invalidDefinitionError marks a definition as structurally invalid. It is
// surfaced to the caller as AWS's InvalidDefinition at CreateStateMachine time.
type invalidDefinitionError struct{ msg string }

func (e *invalidDefinitionError) Error() string { return e.msg }

func invalidDefinitionf(format string, args ...any) error {
	return &invalidDefinitionError{msg: fmt.Sprintf(format, args...)}
}

// parseDefinition parses and structurally validates an ASL document.
//
// It rejects exactly what AWS rejects at CreateStateMachine time — malformed
// JSON, a missing or dangling StartAt, an unknown state Type, a state that
// neither ends nor names a Next, a dangling transition. It deliberately does
// not reject definitions that are valid ASL but use features Overcast cannot
// interpret: those provision (so CDK/CloudFormation deploys keep working) and
// then fail the execution loudly, which is what docs/plans/
// full-emulation-priority.md §2.1 asks for.
func parseDefinition(definition string) (*aslBranch, error) {
	if strings.TrimSpace(definition) == "" {
		return nil, invalidDefinitionf("definition is empty")
	}
	var branch aslBranch
	dec := json.NewDecoder(strings.NewReader(definition))
	dec.UseNumber()
	if err := dec.Decode(&branch); err != nil {
		return nil, invalidDefinitionf("definition is not valid JSON: %v", err)
	}
	switch {
	case branch.QueryLanguage == "", branch.QueryLanguage == queryLanguageJSONPath, branch.QueryLanguage == queryLanguageJSONata:
	default:
		return nil, invalidDefinitionf("QueryLanguage %q must be JSONPath or JSONata", branch.QueryLanguage)
	}
	if err := validateBranch(&branch, "", branch.QueryLanguage); err != nil {
		return nil, err
	}
	if err := checkUniqueStateNames(&branch, "", map[string]string{}); err != nil {
		return nil, err
	}
	return &branch, nil
}

// checkUniqueStateNames enforces that every state name is unique across the
// whole state machine, nested Parallel branches and Map processors included.
// AWS rejects a duplicate with DUPLICATE_STATE_NAME, and the rule is what
// lets a history reader attribute an event to a state by name alone.
func checkUniqueStateNames(branch *aslBranch, where string, seen map[string]string) error {
	for _, name := range branch.order {
		loc := "/States/" + name
		if where != "" {
			loc = where + loc
		}
		if first, dup := seen[name]; dup {
			return invalidDefinitionf("DUPLICATE_STATE_NAME: state name %q is used at both %s and %s — state names must be unique across the whole state machine", name, first, loc)
		}
		seen[name] = loc
	}
	for _, name := range branch.order {
		state := branch.States[name]
		loc := where + "/States/" + name
		for i, sub := range state.Branches {
			if err := checkUniqueStateNames(sub, fmt.Sprintf("%s/Branches[%d]", loc, i), seen); err != nil {
				return err
			}
		}
		if processor := state.processor(); state.Type == stateTypeMap && processor != nil {
			if err := checkUniqueStateNames(processor, loc+"/ItemProcessor", seen); err != nil {
				return err
			}
		}
	}
	return nil
}

// checkStatesAllLast enforces the spec's rule for Retry and Catch: States.ALL
// must appear alone in its ErrorEquals and only in the last entry.
func checkStatesAllLast(loc, field string, errorEquals [][]string) error {
	for i, names := range errorEquals {
		for _, name := range names {
			if name != errAll {
				continue
			}
			if len(names) != 1 {
				return invalidDefinitionf("%s: %s[%d] — States.ALL must appear alone in ErrorEquals", loc, field, i)
			}
			if i != len(errorEquals)-1 {
				return invalidDefinitionf("%s: %s[%d] — States.ALL must be in the last %s entry", loc, field, i, field)
			}
		}
	}
	return nil
}

// validateBranch checks one branch (top level, Parallel branch or Map
// processor). where is a human-readable prefix naming the nesting position.
//
// queryLanguage is the query language the branch's states default to.
func validateBranch(branch *aslBranch, where, queryLanguage string) error {
	prefix := ""
	if where != "" {
		prefix = where + ": "
	}
	if branch == nil {
		return invalidDefinitionf("%smissing state machine body", prefix)
	}
	if branch.StartAt == "" {
		return invalidDefinitionf("%sStartAt is required", prefix)
	}
	if len(branch.States) == 0 {
		return invalidDefinitionf("%sStates is required and must not be empty", prefix)
	}
	if _, ok := branch.States[branch.StartAt]; !ok {
		return invalidDefinitionf("%sStartAt %q does not name a state in States", prefix, branch.StartAt)
	}
	branch.order = make([]string, 0, len(branch.States))
	for name := range branch.States {
		branch.order = append(branch.order, name)
	}
	sort.Strings(branch.order)

	for _, name := range branch.order {
		state := branch.States[name]
		if state == nil {
			return invalidDefinitionf("%sstate %q is null", prefix, name)
		}
		if err := validateState(branch, name, state, where, queryLanguage); err != nil {
			return err
		}
	}
	return nil
}

// terminalStateTypes never carry Next/End — reaching one ends the branch.
var terminalStateTypes = map[string]bool{stateTypeSucceed: true, stateTypeFail: true}

func validateState(branch *aslBranch, name string, state *aslState, where, defaultQL string) error {
	loc := "state " + name
	if where != "" {
		loc = where + " state " + name
	}
	if state.Type == "" {
		return invalidDefinitionf("%s: Type is required", loc)
	}
	if !aslStateTypes[state.Type] {
		return invalidDefinitionf("%s: unknown state Type %q", loc, state.Type)
	}
	ql, err := stateQueryLanguage(state, defaultQL, loc)
	if err != nil {
		return err
	}
	if err := validateQueryLanguageFields(state, ql, loc); err != nil {
		return err
	}

	switch state.Type {
	case stateTypeChoice:
		if len(state.Choices) == 0 {
			return invalidDefinitionf("%s: Choice requires a non-empty Choices array", loc)
		}
		for i, rule := range state.Choices {
			if rule == nil {
				return invalidDefinitionf("%s: Choices[%d] is null", loc, i)
			}
			if rule.Next == "" {
				return invalidDefinitionf("%s: Choices[%d] requires Next", loc, i)
			}
			if err := checkTransition(branch, loc, fmt.Sprintf("Choices[%d].Next", i), rule.Next); err != nil {
				return err
			}
			if err := validateChoiceRule(rule, loc, fmt.Sprintf("Choices[%d]", i), ql == queryLanguageJSONata); err != nil {
				return err
			}
		}
		if state.Default != "" {
			if err := checkTransition(branch, loc, "Default", state.Default); err != nil {
				return err
			}
		}
	case stateTypeTask:
		if state.Resource == "" {
			return invalidDefinitionf("%s: Task requires Resource", loc)
		}
		if err := validateSDKTask(state, loc); err != nil {
			return err
		}
	case stateTypeParallel:
		if len(state.Branches) == 0 {
			return invalidDefinitionf("%s: Parallel requires a non-empty Branches array", loc)
		}
		for i, sub := range state.Branches {
			if err := validateBranch(sub, fmt.Sprintf("%s Branches[%d]", loc, i), ql); err != nil {
				return err
			}
		}
	case stateTypeMap:
		processor := state.ItemProcessor
		if processor == nil {
			processor = state.Iterator
		}
		if processor == nil {
			return invalidDefinitionf("%s: Map requires ItemProcessor (or the legacy Iterator)", loc)
		}
		if err := validateBranch(processor, loc+" ItemProcessor", ql); err != nil {
			return err
		}
	case stateTypeWait:
		if !state.Seconds.Set && state.SecondsPath == "" && state.Timestamp == "" && state.TimestampPath == "" {
			return invalidDefinitionf("%s: Wait requires one of Seconds, SecondsPath, Timestamp or TimestampPath", loc)
		}
	}

	// Retry/Catch are only meaningful on the three states that can fail.
	if len(state.Catch) > 0 {
		for i, catcher := range state.Catch {
			if len(catcher.ErrorEquals) == 0 {
				return invalidDefinitionf("%s: Catch[%d] requires a non-empty ErrorEquals", loc, i)
			}
			if catcher.Next == "" {
				return invalidDefinitionf("%s: Catch[%d] requires Next", loc, i)
			}
			if err := checkTransition(branch, loc, fmt.Sprintf("Catch[%d].Next", i), catcher.Next); err != nil {
				return err
			}
		}
	}
	retryNames := make([][]string, 0, len(state.Retry))
	for i, retrier := range state.Retry {
		if len(retrier.ErrorEquals) == 0 {
			return invalidDefinitionf("%s: Retry[%d] requires a non-empty ErrorEquals", loc, i)
		}
		if retrier.BackoffRate != nil && *retrier.BackoffRate < 1 {
			return invalidDefinitionf("%s: Retry[%d].BackoffRate must be at least 1.0", loc, i)
		}
		if retrier.MaxAttempts != nil && *retrier.MaxAttempts < 0 {
			return invalidDefinitionf("%s: Retry[%d].MaxAttempts must not be negative", loc, i)
		}
		switch strings.ToUpper(retrier.JitterStrategy) {
		case "", "FULL", "NONE":
		default:
			return invalidDefinitionf("%s: Retry[%d].JitterStrategy must be FULL or NONE", loc, i)
		}
		retryNames = append(retryNames, retrier.ErrorEquals)
	}
	if err := checkStatesAllLast(loc, "Retry", retryNames); err != nil {
		return err
	}
	catchNames := make([][]string, 0, len(state.Catch))
	for _, catcher := range state.Catch {
		catchNames = append(catchNames, catcher.ErrorEquals)
	}
	if err := checkStatesAllLast(loc, "Catch", catchNames); err != nil {
		return err
	}

	// Transition shape. Choice states route through Choices/Default only.
	switch {
	case terminalStateTypes[state.Type]:
		if state.Next != "" || state.End {
			return invalidDefinitionf("%s: a %s state must not declare Next or End", loc, state.Type)
		}
	case state.Type == stateTypeChoice:
		if state.Next != "" || state.End {
			return invalidDefinitionf("%s: a Choice state must not declare Next or End", loc)
		}
	default:
		if state.End && state.Next != "" {
			return invalidDefinitionf("%s: declares both Next and End", loc)
		}
		if !state.End && state.Next == "" {
			return invalidDefinitionf("%s: requires either Next or End", loc)
		}
		if state.Next != "" {
			if err := checkTransition(branch, loc, "Next", state.Next); err != nil {
				return err
			}
		}
	}
	return nil
}

// Query language names.
const (
	queryLanguageJSONPath = "JSONPath"
	queryLanguageJSONata  = "JSONata"
)

// stateQueryLanguage resolves a state's query language. A JSONata state
// machine cannot contain a JSONPath state; the reverse is allowed.
func stateQueryLanguage(state *aslState, defaultQL, loc string) (string, error) {
	if defaultQL == "" {
		defaultQL = queryLanguageJSONPath
	}
	switch state.QueryLanguage {
	case "":
		return defaultQL, nil
	case queryLanguageJSONPath:
		if defaultQL == queryLanguageJSONata {
			return "", invalidDefinitionf("%s: a JSONPath state cannot appear where JSONata is the query language", loc)
		}
		return queryLanguageJSONPath, nil
	case queryLanguageJSONata:
		return queryLanguageJSONata, nil
	}
	return "", invalidDefinitionf("%s: QueryLanguage %q must be JSONPath or JSONata", loc, state.QueryLanguage)
}

// validateQueryLanguageFields rejects the fields that belong to the other
// query language, as AWS does: JSONata states take Arguments, Output and
// Items and never a Path field; JSONPath states the reverse.
func validateQueryLanguageFields(state *aslState, ql, loc string) error {
	if ql == queryLanguageJSONata {
		pathFields := map[string]bool{
			"InputPath": state.InputPath.Set, "OutputPath": state.OutputPath.Set, "ResultPath": state.ResultPath.Set,
			"Parameters": len(state.Parameters) > 0, "ResultSelector": len(state.ResultSelector) > 0,
			"ItemsPath": state.ItemsPath != "", "SecondsPath": state.SecondsPath != "", "TimestampPath": state.TimestampPath != "",
			"TimeoutSecondsPath": state.TimeoutSecondsPath != "", "HeartbeatSecondsPath": state.HeartbeatSecondsPath != "",
			"ErrorPath": state.ErrorPath != "", "CausePath": state.CausePath != "", "MaxConcurrencyPath": state.MaxConcurrencyPath != "",
			"ToleratedFailureCountPath": state.ToleratedFailureCountPath != "", "ToleratedFailurePercentagePath": state.ToleratedFailurePercentagePath != "",
			"Result": len(state.Result) > 0,
		}
		if fields := sortedTrueKeys(pathFields); len(fields) > 0 {
			return invalidDefinitionf("%s: %s is a JSONPath field and is not valid in a JSONata state", loc, fields[0])
		}
		for i, catcher := range state.Catch {
			if catcher.ResultPath.Set {
				return invalidDefinitionf("%s: Catch[%d].ResultPath is not valid in a JSONata state — use Output", loc, i)
			}
		}
		return nil
	}
	jsonataFields := map[string]bool{
		"Arguments": len(state.Arguments) > 0, "Output": len(state.Output) > 0, "Items": len(state.Items) > 0,
		"TimeoutSeconds": state.TimeoutSeconds.Expr != "", "HeartbeatSeconds": state.HeartbeatSeconds.Expr != "",
		"Seconds": state.Seconds.Expr != "", "MaxConcurrency": state.MaxConcurrency.Expr != "",
		"ToleratedFailureCount": state.ToleratedFailureCount.Expr != "", "ToleratedFailurePercentage": state.ToleratedFailurePercentage.Expr != "",
	}
	if fields := sortedTrueKeys(jsonataFields); len(fields) > 0 {
		return invalidDefinitionf("%s: %s (or a JSONata expression in it) is only valid in a JSONata state", loc, fields[0])
	}
	for i, catcher := range state.Catch {
		if len(catcher.Output) > 0 {
			return invalidDefinitionf("%s: Catch[%d].Output is only valid in a JSONata state", loc, i)
		}
		if catcher.ResultPath.Set && !catcher.ResultPath.Null && !isDefinitePath(catcher.ResultPath.Value) {
			return invalidDefinitionf("%s: Catch[%d].ResultPath must be a Reference Path", loc, i)
		}
	}
	if state.ResultPath.Set && !state.ResultPath.Null && !isDefinitePath(state.ResultPath.Value) {
		return invalidDefinitionf("%s: ResultPath %q must be a Reference Path (no wildcards, filters or slices)", loc, state.ResultPath.Value)
	}
	return nil
}

func sortedTrueKeys(m map[string]bool) []string {
	var keys []string
	for k, v := range m {
		if v {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	return keys
}

func checkTransition(branch *aslBranch, loc, field, target string) error {
	if _, ok := branch.States[target]; !ok {
		return invalidDefinitionf("%s: %s %q does not name a state in the same States block", loc, field, target)
	}
	return nil
}

// processor returns the branch a Map state iterates with, preferring the
// current ItemProcessor field over the deprecated Iterator.
func (s *aslState) processor() *aslBranch {
	if s.ItemProcessor != nil {
		return s.ItemProcessor
	}
	return s.Iterator
}

// checkExpressDefinition rejects what an EXPRESS state machine cannot run, as
// AWS does at CreateStateMachine: the `.sync` and `.waitForTaskToken`
// integration patterns, activity tasks and distributed Map.
func checkExpressDefinition(branch *aslBranch) error {
	for _, name := range branch.order {
		state := branch.States[name]
		switch {
		case state.Type == stateTypeTask && strings.Contains(state.Resource, ".sync"):
			return invalidDefinitionf("state %s: Express state machines do not support the '.sync' service integration pattern", name)
		case state.Type == stateTypeTask && strings.Contains(state.Resource, ".waitForTaskToken"):
			return invalidDefinitionf("state %s: Express state machines do not support the '.waitForTaskToken' service integration pattern", name)
		case state.Type == stateTypeTask && strings.Contains(state.Resource, ":activity:"):
			return invalidDefinitionf("state %s: Express state machines do not support activities", name)
		case state.Type == stateTypeMap && strings.EqualFold(processorMode(state.processor()), "DISTRIBUTED"):
			return invalidDefinitionf("state %s: Express state machines do not support distributed Map", name)
		}
		for _, sub := range state.Branches {
			if err := checkExpressDefinition(sub); err != nil {
				return err
			}
		}
		if processor := state.processor(); state.Type == stateTypeMap && processor != nil {
			if err := checkExpressDefinition(processor); err != nil {
				return err
			}
		}
	}
	return nil
}
