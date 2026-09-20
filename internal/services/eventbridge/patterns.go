package eventbridge

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"
)

// patternCacheMax bounds how many distinct event patterns are kept compiled.
// PutEvents evaluates every rule on the bus against every entry, so parsing
// the pattern JSON per (event, rule) pair is the one avoidable cost on that
// path; a rule's pattern is immutable for the life of that rule revision, so
// the parsed form is cached by pattern text. The cache is cleared wholesale
// when it reaches the limit rather than evicted entry by entry — patterns are
// small, the limit is far above any realistic rule count, and a clear is a
// handful of re-parses rather than a correctness problem.
const patternCacheMax = 512

// numericMatchLimit is the magnitude AWS documents numeric matching over:
// "limited to values between -5.0e9 and +5.0e9 inclusive".
const numericMatchLimit = 5.0e9

// The content-filter match types AWS defines for event patterns. Inside a
// field's candidate array, every one listed here is either evaluated by
// matchOperator below or refused by validateOperator — deliberately with no
// third category, because a match type that is neither evaluated nor refused
// is a rule that provisions and then never fires (#484).
//
// Outside such an array these names carry no meaning, to AWS or here: a
// pattern that writes {"count": {"numeric": [">", 0]}} without the enclosing
// array is matching a nested field literally named "numeric", and is treated
// that way rather than second-guessed.
const (
	opPrefix           = "prefix"
	opSuffix           = "suffix"
	opExists           = "exists"
	opNumeric          = "numeric"
	opAnythingBut      = "anything-but"
	opEqualsIgnoreCase = "equals-ignore-case"
	opCIDR             = "cidr"
	opWildcard         = "wildcard"
	opOr               = "$or"
)

// patternCache holds parsed event patterns keyed by their JSON text.
type patternCache struct {
	mu       sync.RWMutex
	compiled map[string]map[string]any
}

func newPatternCache() *patternCache {
	return &patternCache{compiled: make(map[string]map[string]any)}
}

// matches reports whether event satisfies pattern, parsing and caching the
// pattern on first use. An unparseable pattern never matches.
func (c *patternCache) matches(pattern string, event map[string]any) bool {
	if c == nil {
		return eventPatternMatches(pattern, event)
	}
	c.mu.RLock()
	parsed, ok := c.compiled[pattern]
	c.mu.RUnlock()
	if !ok {
		if json.Unmarshal([]byte(pattern), &parsed) != nil {
			parsed = nil
		}
		c.mu.Lock()
		if len(c.compiled) >= patternCacheMax {
			c.compiled = make(map[string]map[string]any, patternCacheMax)
		}
		c.compiled[pattern] = parsed
		c.mu.Unlock()
	}
	if parsed == nil {
		return false
	}
	return matchPatternMap(parsed, event)
}

// eventPatternMatches parses and evaluates a pattern without the cache. It is
// the uncached reference the cache is tested against.
func eventPatternMatches(pattern string, event map[string]any) bool {
	p, err := parseEventPattern(pattern)
	if err != nil {
		return false
	}
	return matchPatternMap(p, event)
}

// parseEventPattern decodes a pattern document, reporting why it is not one.
// Rule delivery treats an unparseable pattern as never matching; PutRule and
// TestEventPattern surface the reason as InvalidEventPatternException instead.
func parseEventPattern(pattern string) (map[string]any, error) {
	var p map[string]any
	if err := json.Unmarshal([]byte(pattern), &p); err != nil {
		return nil, err
	}
	if p == nil {
		return nil, errors.New("pattern must be a JSON object")
	}
	return p, nil
}

// validateEventPatternDocument parses and validates a pattern string, so every
// operation that must refuse a bad pattern shares one entry point with the
// matcher that evaluates a good one.
func validateEventPatternDocument(pattern string) error {
	p, err := parseEventPattern(pattern)
	if err != nil {
		return err
	}
	return validateEventPattern(p)
}

// ── Validation ───────────────────────────────────────────────────────────────

// validateEventPattern reports why p is not an event pattern AWS would accept,
// or nil when it is one.
//
// Two classes of rejection live here. The first is AWS's own: a field's value
// must be a nested pattern object or an array of candidate values, and a match
// expression inside such an array must be one of the documented match types
// carrying the argument shape that type takes. The second is Overcast's: a
// match type AWS defines but this emulator does not evaluate is refused by
// name, because storing it would mint a rule that looks right in DescribeRule
// and silently never fires.
//
// The AWS API reference documents InvalidEventPatternException only as "The
// event pattern is not valid" and publishes none of the reason strings, so the
// reasons below are Overcast's own wording inside AWS's documented
// "Event pattern is not valid. Reason: …" message shape.
//
// Keys are walked in sorted order so a pattern with more than one fault always
// reports the same reason; Go's map iteration order would otherwise let the
// error message vary between identical requests.
func validateEventPattern(p map[string]any) error {
	for _, key := range slices.Sorted(maps.Keys(p)) {
		if key == opOr {
			return unsupportedOperatorError(opOr)
		}
		switch value := p[key].(type) {
		case []any:
			if err := validatePatternLeaf(key, value); err != nil {
				return err
			}
		case map[string]any:
			if err := validateEventPattern(value); err != nil {
				return err
			}
		default:
			// A bare scalar is the mistake behind most patterns that
			// provision and then match nothing.
			return fmt.Errorf("%q must be an object or an array", key)
		}
	}
	return nil
}

// validatePatternLeaf checks one field's array of candidate values. Each entry
// is either an exact value (any JSON scalar, including the null and empty
// string AWS documents) or a single match expression.
func validatePatternLeaf(field string, items []any) error {
	if len(items) == 0 {
		return fmt.Errorf("%q: an empty array matches nothing and is not a valid pattern value", field)
	}
	for _, item := range items {
		switch value := item.(type) {
		case map[string]any:
			if err := validateOperator(field, value); err != nil {
				return err
			}
		case []any:
			return fmt.Errorf("%q: nested arrays are not valid pattern values", field)
		}
	}
	return nil
}

// validateOperator checks a single match expression, e.g. {"prefix": "us-"}.
func validateOperator(field string, expr map[string]any) error {
	name, arg, ok := singleEntry(expr)
	if !ok {
		return fmt.Errorf("%q: a match expression takes exactly one match type, got %d", field, len(expr))
	}
	switch name {
	case opExists:
		if _, ok := arg.(bool); !ok {
			return fmt.Errorf("%q: %q takes true or false", field, opExists)
		}
	case opPrefix, opSuffix:
		if !isAffixArgument(arg) {
			return fmt.Errorf("%q: %q takes a string or {%q: string}", field, name, opEqualsIgnoreCase)
		}
	case opEqualsIgnoreCase:
		if _, ok := arg.(string); !ok {
			return fmt.Errorf("%q: %q takes a string", field, opEqualsIgnoreCase)
		}
	case opNumeric:
		return validateNumericArgument(field, arg)
	case opAnythingBut:
		return validateAnythingButArgument(field, arg)
	case opCIDR, opWildcard, opOr:
		return unsupportedOperatorError(name)
	default:
		return fmt.Errorf("%q: unrecognized match type %q", field, name)
	}
	return nil
}

// validateNumericArgument checks {"numeric": [">", 0, "<=", 5]} — pairs of a
// comparison and a JSON number, at least one pair.
func validateNumericArgument(field string, arg any) error {
	args, ok := arg.([]any)
	if !ok || len(args) == 0 || len(args)%2 != 0 {
		return fmt.Errorf("%q: %q takes pairs of a comparison and a number", field, opNumeric)
	}
	for i := 0; i < len(args); i += 2 {
		cmp, ok := args[i].(string)
		if !ok || !isNumericComparison(cmp) {
			return fmt.Errorf("%q: %q comparison must be one of =, !=, <, <=, >, >=", field, opNumeric)
		}
		bound, ok := args[i+1].(float64)
		if !ok {
			return fmt.Errorf("%q: %q bound must be a number", field, opNumeric)
		}
		if bound < -numericMatchLimit || bound > numericMatchLimit {
			return fmt.Errorf("%q: %q is limited to values between -5.0e9 and +5.0e9", field, opNumeric)
		}
	}
	return nil
}

// validateAnythingButArgument checks the value, list of values, or nested
// prefix/suffix/equals-ignore-case expression that anything-but negates.
func validateAnythingButArgument(field string, arg any) error {
	switch value := arg.(type) {
	case string, float64, bool, nil:
		return nil
	case []any:
		// AWS documents lists "that contain only strings, or only numbers".
		// An empty one would negate into "matches every present value", which
		// is the one way this match type can go badly wrong.
		if len(value) == 0 {
			return fmt.Errorf("%q: %q takes at least one value", field, opAnythingBut)
		}
		for _, item := range value {
			switch item.(type) {
			case string, float64:
			default:
				return fmt.Errorf("%q: %q takes a list of strings or a list of numbers", field, opAnythingBut)
			}
		}
		return nil
	case map[string]any:
		name, inner, ok := singleEntry(value)
		if !ok {
			return fmt.Errorf("%q: %q takes exactly one nested match type", field, opAnythingBut)
		}
		switch name {
		case opPrefix, opSuffix, opEqualsIgnoreCase:
			if !isStringOrStringList(inner) {
				return fmt.Errorf("%q: %q %q takes a string or a list of strings", field, opAnythingBut, name)
			}
			return nil
		case opWildcard:
			return unsupportedOperatorError(opWildcard)
		default:
			return fmt.Errorf("%q: unrecognized match type %q inside %q", field, name, opAnythingBut)
		}
	default:
		return fmt.Errorf("%q: %q takes a value, a list of values, or a nested match expression", field, opAnythingBut)
	}
}

// unsupportedOperatorError names a match type AWS defines and Overcast does
// not evaluate. It is reported as InvalidEventPatternException — a modeled
// error code clients already handle — with a reason that says plainly this is
// the emulator's limit, not a mistake in the caller's pattern.
func unsupportedOperatorError(name string) error {
	return fmt.Errorf("the %q match type is valid on AWS but is not supported by Overcast", name)
}

func isNumericComparison(cmp string) bool {
	switch cmp {
	case "=", "!=", "<", "<=", ">", ">=":
		return true
	}
	return false
}

// isAffixArgument reports whether arg is what prefix and suffix accept: a
// string, or {"equals-ignore-case": string} for the case-insensitive form.
func isAffixArgument(arg any) bool {
	switch value := arg.(type) {
	case string:
		return true
	case map[string]any:
		name, inner, ok := singleEntry(value)
		if !ok || name != opEqualsIgnoreCase {
			return false
		}
		_, ok = inner.(string)
		return ok
	}
	return false
}

func isStringOrStringList(arg any) bool {
	switch value := arg.(type) {
	case string:
		return true
	case []any:
		for _, item := range value {
			if _, ok := item.(string); !ok {
				return false
			}
		}
		return len(value) > 0
	}
	return false
}

// singleEntry returns the sole key and value of m, reporting false when m does
// not hold exactly one.
func singleEntry(m map[string]any) (string, any, bool) {
	if len(m) != 1 {
		return "", nil, false
	}
	for k, v := range m {
		return k, v, true
	}
	return "", nil, false
}

// ── Matching ─────────────────────────────────────────────────────────────────

func matchPatternMap(pattern, event map[string]any) bool {
	for key, want := range pattern {
		got, present := event[key]
		switch wantTyped := want.(type) {
		case []any:
			if !matchLeaf(wantTyped, got, present) {
				return false
			}
		case map[string]any:
			gotMap, ok := got.(map[string]any)
			if !ok || !matchPatternMap(wantTyped, gotMap) {
				return false
			}
		default:
			// Not a shape PutRule or TestEventPattern accepts any more;
			// reached only by a rule stored before that validation existed,
			// which keeps matching the way it always did.
			if !present || !scalarEquals(wantTyped, got) {
				return false
			}
		}
	}
	return true
}

// matchLeaf evaluates one field's array of candidates against the event value,
// which may be absent — presence is the whole question for {"exists": false}.
func matchLeaf(want []any, got any, present bool) bool {
	for _, candidate := range want {
		if expr, ok := candidate.(map[string]any); ok {
			if matchOperator(expr, got, present) {
				return true
			}
			continue
		}
		if present && scalarEquals(candidate, got) {
			return true
		}
	}
	return false
}

// matchOperator evaluates a single match expression. A match type this
// emulator does not implement never matches here; PutRule refuses it outright,
// so that is reached only by a rule stored before the check existed.
func matchOperator(expr map[string]any, got any, present bool) bool {
	name, arg, ok := singleEntry(expr)
	if !ok {
		return false
	}
	if name == opExists {
		want, ok := arg.(bool)
		if !ok {
			return false
		}
		// AWS: "Exists matching only works on leaf nodes. It does not work on
		// intermediate nodes." An object or an array under the key is an
		// intermediate node, so neither true nor false is satisfied there.
		// The docs spell out only the true case; refusing both is the reading
		// that cannot over-match.
		if present && !isLeafValue(got) {
			return false
		}
		return want == present
	}
	// Every other match type asks a question about a value, so an absent
	// field answers no.
	if !present {
		return false
	}
	switch name {
	case opPrefix:
		return matchAffix(arg, got, strings.HasPrefix)
	case opSuffix:
		return matchAffix(arg, got, strings.HasSuffix)
	case opEqualsIgnoreCase:
		return matchEqualsIgnoreCase(arg, got)
	case opNumeric:
		return matchNumeric(arg, got)
	case opAnythingBut:
		inner, supported := matchAnythingBut(arg, got)
		return supported && !inner
	default:
		return false
	}
}

// matchAffix evaluates prefix and suffix, including their
// {"equals-ignore-case": …} case-insensitive forms.
func matchAffix(arg, got any, has func(string, string) bool) bool {
	value, ok := got.(string)
	if !ok {
		return false
	}
	switch want := arg.(type) {
	case string:
		return has(value, want)
	case map[string]any:
		name, inner, ok := singleEntry(want)
		if !ok || name != opEqualsIgnoreCase {
			return false
		}
		text, ok := inner.(string)
		return ok && has(strings.ToLower(value), strings.ToLower(text))
	}
	return false
}

func matchEqualsIgnoreCase(arg, got any) bool {
	want, ok := arg.(string)
	if !ok {
		return false
	}
	value, ok := got.(string)
	return ok && strings.EqualFold(value, want)
}

// matchNumeric evaluates {"numeric": [">", 0, "<=", 5]}. Every pair must hold,
// and the event value must be a JSON number.
func matchNumeric(arg, got any) bool {
	value, ok := got.(float64)
	if !ok {
		return false
	}
	args, ok := arg.([]any)
	if !ok || len(args) == 0 || len(args)%2 != 0 {
		return false
	}
	for i := 0; i < len(args); i += 2 {
		cmp, ok := args[i].(string)
		if !ok {
			return false
		}
		bound, ok := args[i+1].(float64)
		if !ok {
			return false
		}
		if !compareNumeric(value, cmp, bound) {
			return false
		}
	}
	return true
}

func compareNumeric(value float64, cmp string, bound float64) bool {
	switch cmp {
	case "=":
		return value == bound
	case "!=":
		return value != bound
	case "<":
		return value < bound
	case "<=":
		return value <= bound
	case ">":
		return value > bound
	case ">=":
		return value >= bound
	}
	return false
}

// matchAnythingBut reports whether got satisfies the expression anything-but
// negates, and whether that expression is one Overcast evaluates at all. The
// second return matters: a nested match type this emulator does not implement
// must not be read as "did not match" and so negate into "matches everything".
func matchAnythingBut(arg, got any) (matched, supported bool) {
	switch want := arg.(type) {
	case string, float64, bool, nil:
		return scalarEquals(want, got), true
	case []any:
		if len(want) == 0 {
			return false, false
		}
		for _, item := range want {
			if scalarEquals(item, got) {
				return true, true
			}
		}
		return false, true
	case map[string]any:
		name, inner, ok := singleEntry(want)
		if !ok {
			return false, false
		}
		switch name {
		case opPrefix, opSuffix, opEqualsIgnoreCase:
			// The argument shape is checked with the validator's own
			// predicate rather than inferred from whether a match was found.
			// "Could not read this clause" and "read it and it did not match"
			// negate to opposite answers, and getting that wrong turns
			// anything-but into a rule that matches every event.
			if !isStringOrStringList(inner) {
				return false, false
			}
			value, isString := got.(string)
			if !isString {
				// None of the three string clauses can hold for a non-string
				// event value, so anything-but holds.
				return false, true
			}
			return anyString(inner, func(candidate string) bool {
				return matchStringClause(name, value, candidate)
			}), true
		default:
			return false, false
		}
	}
	return false, false
}

// matchStringClause applies one of the three string match types anything-but
// can nest to a single candidate.
func matchStringClause(name, value, want string) bool {
	switch name {
	case opPrefix:
		return strings.HasPrefix(value, want)
	case opSuffix:
		return strings.HasSuffix(value, want)
	case opEqualsIgnoreCase:
		return strings.EqualFold(value, want)
	}
	return false
}

// isLeafValue reports whether v is a JSON scalar — the only shape AWS applies
// exists matching to.
func isLeafValue(v any) bool {
	switch v.(type) {
	case map[string]any, []any:
		return false
	}
	return true
}

// anyString applies fn to a string argument, or to each member of a list of
// strings — the "this includes single values, or a list of values" shape every
// nested anything-but match type takes.
func anyString(arg any, fn func(string) bool) bool {
	switch value := arg.(type) {
	case string:
		return fn(value)
	case []any:
		for _, item := range value {
			if s, ok := item.(string); ok && fn(s) {
				return true
			}
		}
	}
	return false
}

// scalarEquals compares an exact pattern value with an event value. The
// comparison is textual, which is what this matcher has always done and what
// keeps a pattern's 100 equal to an event's 100 across JSON's single number
// type.
func scalarEquals(want, got any) bool {
	return fmt.Sprint(want) == fmt.Sprint(got)
}
