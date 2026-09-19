package stepfunctions

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
)

// aslChoiceRule is one entry of a Choice state's Choices array, or a nested
// rule inside And/Or/Not. Comparison operators are captured generically so an
// operator Overcast does not implement can be named in the failure rather than
// silently evaluating to false.
type aslChoiceRule struct {
	Variable string
	Next     string
	And      []*aslChoiceRule
	Or       []*aslChoiceRule
	Not      *aslChoiceRule

	// Condition is a JSONata rule's "{% boolean expression %}".
	Condition string
	// Assign and Output apply when this top-level rule matches.
	Assign json.RawMessage
	Output json.RawMessage

	comparisons []choiceComparison
}

type choiceComparison struct {
	op      string
	operand json.RawMessage
}

// choiceOperators is the set of comparison operators the interpreter
// evaluates. Every one of them is an Amazon States Language operator; the map
// is also used to reject anything outside it loudly.
var choiceOperators = map[string]bool{
	"StringEquals": true, "StringEqualsPath": true,
	"StringLessThan": true, "StringLessThanPath": true,
	"StringGreaterThan": true, "StringGreaterThanPath": true,
	"StringLessThanEquals": true, "StringLessThanEqualsPath": true,
	"StringGreaterThanEquals": true, "StringGreaterThanEqualsPath": true,
	"StringMatches": true,

	"NumericEquals": true, "NumericEqualsPath": true,
	"NumericLessThan": true, "NumericLessThanPath": true,
	"NumericGreaterThan": true, "NumericGreaterThanPath": true,
	"NumericLessThanEquals": true, "NumericLessThanEqualsPath": true,
	"NumericGreaterThanEquals": true, "NumericGreaterThanEqualsPath": true,

	"BooleanEquals": true, "BooleanEqualsPath": true,

	"TimestampEquals": true, "TimestampEqualsPath": true,
	"TimestampLessThan": true, "TimestampLessThanPath": true,
	"TimestampGreaterThan": true, "TimestampGreaterThanPath": true,
	"TimestampLessThanEquals": true, "TimestampLessThanEqualsPath": true,
	"TimestampGreaterThanEquals": true, "TimestampGreaterThanEqualsPath": true,

	"IsNull": true, "IsPresent": true, "IsNumeric": true,
	"IsString": true, "IsBoolean": true, "IsTimestamp": true,
}

// choiceStructuralKeys are the rule fields that are not comparison operators.
var choiceStructuralKeys = map[string]bool{
	"Variable": true, "Next": true, "And": true, "Or": true, "Not": true, "Comment": true,
	"Condition": true, "Assign": true, "Output": true,
}

// UnmarshalJSON decodes a choice rule, keeping unknown keys so validation can
// name them instead of ignoring them.
func (r *aslChoiceRule) UnmarshalJSON(data []byte) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	*r = aslChoiceRule{}
	keys := make([]string, 0, len(fields))
	for key := range fields {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	for _, key := range keys {
		raw := fields[key]
		switch key {
		case "Variable":
			if err := json.Unmarshal(raw, &r.Variable); err != nil {
				return err
			}
		case "Next":
			if err := json.Unmarshal(raw, &r.Next); err != nil {
				return err
			}
		case "Comment":
			// Ignored, as on AWS.
		case "Condition":
			if err := json.Unmarshal(raw, &r.Condition); err != nil {
				return err
			}
		case "Assign":
			r.Assign = raw
		case "Output":
			r.Output = raw
		case "And":
			if err := json.Unmarshal(raw, &r.And); err != nil {
				return err
			}
		case "Or":
			if err := json.Unmarshal(raw, &r.Or); err != nil {
				return err
			}
		case "Not":
			if err := json.Unmarshal(raw, &r.Not); err != nil {
				return err
			}
		default:
			r.comparisons = append(r.comparisons, choiceComparison{op: key, operand: raw})
		}
	}
	return nil
}

// validateChoiceRule rejects rule shapes AWS rejects and, separately, names
// any comparison operator that is not part of the language.
func validateChoiceRule(rule *aslChoiceRule, loc, field string, jsonataMode bool) error {
	if jsonataMode {
		if _, ok := isJSONataExpression(rule.Condition); !ok {
			return invalidDefinitionf("%s: %s in a JSONata Choice needs a Condition of the form {%% expression %%}", loc, field)
		}
		if rule.Variable != "" || len(rule.comparisons) > 0 || len(rule.And)+len(rule.Or) > 0 || rule.Not != nil {
			return invalidDefinitionf("%s: %s — a JSONata Choice rule takes only Condition, Next, Assign and Output", loc, field)
		}
		return nil
	}
	if rule.Condition != "" || len(rule.Output) > 0 {
		return invalidDefinitionf("%s: %s — Condition and Output are only valid in a JSONata Choice", loc, field)
	}
	nested := len(rule.And) + len(rule.Or)
	if rule.Not != nil {
		nested++
	}
	if nested > 0 && len(rule.comparisons) > 0 {
		return invalidDefinitionf("%s: %s combines a boolean operator with a comparison", loc, field)
	}
	if nested == 0 && len(rule.comparisons) == 0 {
		return invalidDefinitionf("%s: %s has no comparison", loc, field)
	}
	if len(rule.comparisons) > 1 {
		return invalidDefinitionf("%s: %s declares more than one comparison operator", loc, field)
	}
	for _, cmp := range rule.comparisons {
		if choiceStructuralKeys[cmp.op] {
			continue
		}
		if !choiceOperators[cmp.op] {
			return invalidDefinitionf("%s: %s uses unknown comparison operator %q", loc, field, cmp.op)
		}
		if rule.Variable == "" {
			return invalidDefinitionf("%s: %s requires Variable alongside %s", loc, field, cmp.op)
		}
	}
	for i, sub := range rule.And {
		if sub == nil {
			return invalidDefinitionf("%s: %s.And[%d] is null", loc, field, i)
		}
		if err := validateChoiceRule(sub, loc, fmt.Sprintf("%s.And[%d]", field, i), false); err != nil {
			return err
		}
	}
	for i, sub := range rule.Or {
		if sub == nil {
			return invalidDefinitionf("%s: %s.Or[%d] is null", loc, field, i)
		}
		if err := validateChoiceRule(sub, loc, fmt.Sprintf("%s.Or[%d]", field, i), false); err != nil {
			return err
		}
	}
	if rule.Not != nil {
		if err := validateChoiceRule(rule.Not, loc, field+".Not", false); err != nil {
			return err
		}
	}
	return nil
}

// evaluateChoiceRule evaluates a rule against the effective input.
func evaluateChoiceRule(rule *aslChoiceRule, doc any, ctxObj map[string]any) (bool, error) {
	if len(rule.And) > 0 {
		for _, sub := range rule.And {
			ok, err := evaluateChoiceRule(sub, doc, ctxObj)
			if err != nil || !ok {
				return false, err
			}
		}
		return true, nil
	}
	if len(rule.Or) > 0 {
		for _, sub := range rule.Or {
			ok, err := evaluateChoiceRule(sub, doc, ctxObj)
			if err != nil {
				return false, err
			}
			if ok {
				return true, nil
			}
		}
		return false, nil
	}
	if rule.Not != nil {
		ok, err := evaluateChoiceRule(rule.Not, doc, ctxObj)
		if err != nil {
			return false, err
		}
		return !ok, nil
	}
	if len(rule.comparisons) != 1 {
		return false, pathErrorf("choice rule has %d comparisons, expected exactly 1", len(rule.comparisons))
	}
	return evaluateComparison(rule.Variable, rule.comparisons[0], doc, ctxObj)
}

// evaluateComparison applies one comparison operator. The spec is explicit
// that a type mismatch is not an error: "if the values are not both of the
// appropriate type, the comparison will return false". A Variable that does
// not resolve at all still fails the state with States.Runtime, as on AWS.
func evaluateComparison(variable string, cmp choiceComparison, doc any, ctxObj map[string]any) (bool, error) {
	if !choiceOperators[cmp.op] {
		return false, pathErrorf("comparison operator %q is not supported", cmp.op)
	}

	// The Is* family reports on presence and type, so a missing variable is a
	// legitimate answer rather than a fault.
	if strings.HasPrefix(cmp.op, "Is") {
		value, present := selectPathOptional(doc, ctxObj, variable)
		var want bool
		if err := json.Unmarshal(cmp.operand, &want); err != nil {
			return false, pathErrorf("%s expects a boolean operand", cmp.op)
		}
		return isCheck(cmp.op, value, present) == want, nil
	}

	left, err := selectPath(doc, ctxObj, variable)
	if err != nil {
		return false, err
	}

	op := cmp.op
	var right any
	if strings.HasSuffix(op, "Path") {
		op = strings.TrimSuffix(op, "Path")
		var rightPath string
		if err := json.Unmarshal(cmp.operand, &rightPath); err != nil {
			return false, pathErrorf("%s expects a string path operand", cmp.op)
		}
		right, err = selectPath(doc, ctxObj, rightPath)
		if err != nil {
			return false, err
		}
	} else {
		if err := json.Unmarshal(cmp.operand, &right); err != nil {
			return false, pathErrorf("%s operand is not valid JSON", cmp.op)
		}
	}

	switch {
	case strings.HasPrefix(op, "String"):
		return compareStrings(op, left, right)
	case strings.HasPrefix(op, "Numeric"):
		return compareNumbers(op, left, right)
	case op == "BooleanEquals":
		lb, lok := left.(bool)
		rb, rok := right.(bool)
		if !lok || !rok {
			return false, nil
		}
		return lb == rb, nil
	case strings.HasPrefix(op, "Timestamp"):
		return compareTimestamps(op, left, right)
	}
	return false, pathErrorf("comparison operator %q is not supported", cmp.op)
}

func isCheck(op string, value any, present bool) bool {
	switch op {
	case "IsPresent":
		return present
	case "IsNull":
		return present && value == nil
	case "IsNumeric":
		_, ok := toNumber(value)
		return present && ok
	case "IsString":
		_, ok := value.(string)
		return present && ok
	case "IsBoolean":
		_, ok := value.(bool)
		return present && ok
	case "IsTimestamp":
		s, ok := value.(string)
		if !present || !ok {
			return false
		}
		_, err := parseASLTimestamp(s)
		return err == nil
	}
	return false
}

func compareStrings(op string, left, right any) (bool, error) {
	ls, lok := left.(string)
	rs, rok := right.(string)
	if !lok || !rok {
		return false, nil
	}
	switch op {
	case "StringEquals":
		return ls == rs, nil
	case "StringLessThan":
		return ls < rs, nil
	case "StringGreaterThan":
		return ls > rs, nil
	case "StringLessThanEquals":
		return ls <= rs, nil
	case "StringGreaterThanEquals":
		return ls >= rs, nil
	case "StringMatches":
		return matchStringPattern(ls, rs)
	}
	return false, pathErrorf("comparison operator %q is not supported", op)
}

func compareNumbers(op string, left, right any) (bool, error) {
	ln, lok := toNumber(left)
	rn, rok := toNumber(right)
	if !lok || !rok {
		return false, nil
	}
	switch op {
	case "NumericEquals":
		return ln == rn, nil
	case "NumericLessThan":
		return ln < rn, nil
	case "NumericGreaterThan":
		return ln > rn, nil
	case "NumericLessThanEquals":
		return ln <= rn, nil
	case "NumericGreaterThanEquals":
		return ln >= rn, nil
	}
	return false, pathErrorf("comparison operator %q is not supported", op)
}

func compareTimestamps(op string, left, right any) (bool, error) {
	ls, lok := left.(string)
	rs, rok := right.(string)
	if !lok || !rok {
		return false, nil
	}
	lt, lerr := parseASLTimestamp(ls)
	rt, rerr := parseASLTimestamp(rs)
	if lerr != nil || rerr != nil {
		return false, nil
	}
	switch op {
	case "TimestampEquals":
		return lt.Equal(rt), nil
	case "TimestampLessThan":
		return lt.Before(rt), nil
	case "TimestampGreaterThan":
		return lt.After(rt), nil
	case "TimestampLessThanEquals":
		return !lt.After(rt), nil
	case "TimestampGreaterThanEquals":
		return !lt.Before(rt), nil
	}
	return false, pathErrorf("comparison operator %q is not supported", op)
}

func parseASLTimestamp(s string) (time.Time, error) {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, pathErrorf("%q is not an RFC3339 timestamp", s)
	}
	return t, nil
}

// matchStringPattern implements StringMatches: `*` matches any sequence of
// characters and `\\*` matches a literal asterisk. Everything else is literal.
func matchStringPattern(value, pattern string) (bool, error) {
	var expr strings.Builder
	expr.WriteString("^")
	for i := 0; i < len(pattern); i++ {
		switch {
		case pattern[i] == '\\' && i+1 < len(pattern):
			expr.WriteString(regexp.QuoteMeta(string(pattern[i+1])))
			i++
		case pattern[i] == '*':
			expr.WriteString("(?s).*")
		default:
			expr.WriteString(regexp.QuoteMeta(string(pattern[i])))
		}
	}
	expr.WriteString("$")
	re, err := regexp.Compile(expr.String())
	if err != nil {
		return false, pathErrorf("StringMatches pattern %q is invalid", pattern)
	}
	return re.MatchString(value), nil
}
