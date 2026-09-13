package logs

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

// ---------- Public API -------------------------------------------------------

// Matcher tests whether a log event message satisfies a compiled filter pattern.
type Matcher func(message string) bool

// CompileFilter parses a CloudWatch Logs filter pattern and returns a compiled
// Matcher. An empty pattern matches all messages.
//
// Supported syntax:
//   - Text patterns: term (AND), "quoted phrase", ?term (OR across ? terms)
//   - JSON patterns: { $.field op value }, with &&/|| combinators,
//     EXISTS, NOT EXISTS, IS NULL, IS NOT NULL
//   - Space-delimited patterns: [col1, col2 = value, col3 = 4*, ...]
//     with wildcard glob, numeric comparison, and ellipsis
func CompileFilter(pattern string) (Matcher, error) {
	f, err := compileFilterPattern(pattern)
	if err != nil {
		return nil, err
	}
	return f.matcher(), nil
}

// compiledFilter is CompileFilter's extraction-aware form, shared by
// FilterLogEvents (which only needs the match) and metric filters (which also
// need the fields a `metricValue` or dimension names). One evaluation does
// both, so a metric filter never parses a JSON event or splits a
// space-delimited one twice.
type compiledFilter struct {
	// eval reports whether msg matches. On a match, fields is the message's
	// extracted fields for a JSON or space-delimited pattern and nil for a
	// text pattern, which names no fields.
	eval func(msg string) (fields matchedFields, ok bool)
	// extractsFields is true for the pattern kinds that can name fields —
	// JSON and space-delimited — and false for text patterns.
	extractsFields bool
}

func (f *compiledFilter) matcher() Matcher {
	return func(msg string) bool {
		_, ok := f.eval(msg)
		return ok
	}
}

// matchedFields is what one matched message exposes to a metric filter.
type matchedFields interface {
	// lookup resolves one field reference as a metric filter writes it —
	// `$name` for a space-delimited column, `$.path` for a JSON property —
	// to its string value. ok is false for a reference the message does not
	// carry, and for a JSON value that is not a scalar (an object, an array
	// or null), since none of those can be a metric value or a dimension.
	lookup(ref string) (value string, ok bool)
	// named returns the fields TestMetricFilter reports as extractedValues:
	// every column of a space-delimited pattern (unnamed ones as `$<position>`,
	// 1-based), or the properties a JSON pattern's selectors name.
	named() map[string]string
}

// compileFilterPattern parses a filter pattern into its match-and-extract
// form. An empty pattern matches every message and extracts nothing.
func compileFilterPattern(pattern string) (*compiledFilter, error) {
	pattern = strings.TrimSpace(pattern)
	if pattern == "" {
		return matchAllFilter, nil
	}
	if len(pattern) >= 2 && pattern[0] == '{' && pattern[len(pattern)-1] == '}' {
		return compileJSONFilter(pattern)
	}
	if len(pattern) >= 2 && pattern[0] == '[' && pattern[len(pattern)-1] == ']' {
		return compileColumnarFilter(pattern)
	}
	return matcherFilter(compileTextFilter(pattern)), nil
}

func matchAll(string) bool { return true }

// matchAllFilter is the compiled form of the empty pattern.
var matchAllFilter = matcherFilter(matchAll)

// matcherFilter wraps a plain Matcher — a text pattern, or match-all — as a
// compiledFilter that extracts nothing.
func matcherFilter(m Matcher) *compiledFilter {
	return &compiledFilter{eval: func(msg string) (matchedFields, bool) {
		return nil, m(msg)
	}}
}

// ---------- Text filter ------------------------------------------------------

func compileTextFilter(pattern string) Matcher {
	tokens := tokenizeText(pattern)
	if len(tokens) == 0 {
		return matchAll
	}

	var required, included []string
	for _, tok := range tokens {
		if strings.HasPrefix(tok, "?") {
			if v := tok[1:]; v != "" {
				included = append(included, v)
			}
		} else {
			required = append(required, tok)
		}
	}

	return func(msg string) bool {
		for _, t := range required {
			if !strings.Contains(msg, t) {
				return false
			}
		}
		if len(included) > 0 {
			for _, t := range included {
				if strings.Contains(msg, t) {
					return true
				}
			}
			return false
		}
		return true
	}
}

// tokenizeText splits a text pattern into tokens, handling quoted phrases.
// The ? prefix is preserved so the caller can identify OR terms.
func tokenizeText(pattern string) []string {
	var tokens []string
	s := strings.TrimSpace(pattern)
	for s != "" {
		// Handle ?-prefixed quoted string: ?"phrase"
		prefix := ""
		if len(s) > 1 && s[0] == '?' && s[1] == '"' {
			prefix = "?"
			s = s[1:]
		}
		if s[0] == '"' {
			end := strings.Index(s[1:], `"`)
			if end >= 0 {
				tokens = append(tokens, prefix+s[1:1+end])
				s = strings.TrimSpace(s[2+end:])
			} else {
				tokens = append(tokens, prefix+s[1:])
				s = ""
			}
		} else {
			idx := strings.IndexFunc(s, unicode.IsSpace)
			if idx >= 0 {
				tokens = append(tokens, s[:idx])
				s = strings.TrimSpace(s[idx:])
			} else {
				tokens = append(tokens, s)
				s = ""
			}
		}
	}
	return tokens
}

// ---------- JSON filter expression tree --------------------------------------

// jsonExpr is a node in the parsed JSON filter expression tree.
type jsonExpr interface {
	eval(data any) bool
	// selectors appends every $.path the node names, in pattern order —
	// the set TestMetricFilter reports for a JSON pattern.
	selectors(acc [][]string) [][]string
}

type cmpOp int8

const (
	opEq cmpOp = iota
	opNe
	opGt
	opGe
	opLt
	opLe
)

// jsonCmp: $.path op value.
type jsonCmp struct {
	path []string
	op   cmpOp
	val  any // string, float64, or bool
}

func (c *jsonCmp) eval(data any) bool {
	v, ok := resolvePath(data, c.path)
	if !ok {
		return false
	}
	return cmpValues(v, c.val, c.op)
}

func (c *jsonCmp) selectors(acc [][]string) [][]string { return append(acc, c.path) }

// jsonAnd / jsonOr: boolean combinators.
type jsonAnd struct{ left, right jsonExpr }
type jsonOr struct{ left, right jsonExpr }

func (e *jsonAnd) eval(data any) bool { return e.left.eval(data) && e.right.eval(data) }
func (e *jsonOr) eval(data any) bool  { return e.left.eval(data) || e.right.eval(data) }

func (e *jsonAnd) selectors(acc [][]string) [][]string {
	return e.right.selectors(e.left.selectors(acc))
}

func (e *jsonOr) selectors(acc [][]string) [][]string {
	return e.right.selectors(e.left.selectors(acc))
}

// jsonExists: $.path EXISTS / NOT EXISTS.
type jsonExists struct {
	path   []string
	negate bool
}

func (e *jsonExists) eval(data any) bool {
	_, ok := resolvePath(data, e.path)
	if e.negate {
		return !ok
	}
	return ok
}

func (e *jsonExists) selectors(acc [][]string) [][]string { return append(acc, e.path) }

// jsonIsNull: $.path IS NULL / IS NOT NULL.
type jsonIsNull struct {
	path   []string
	negate bool
}

func (e *jsonIsNull) eval(data any) bool {
	v, ok := resolvePath(data, e.path)
	if !ok {
		// Missing field: IS NULL → true, IS NOT NULL → false.
		return !e.negate
	}
	if e.negate {
		return v != nil
	}
	return v == nil
}

func (e *jsonIsNull) selectors(acc [][]string) [][]string { return append(acc, e.path) }

// ---------- JSON parser (recursive descent) ----------------------------------

func compileJSONFilter(pattern string) (*compiledFilter, error) {
	inner := strings.TrimSpace(pattern[1 : len(pattern)-1])
	if inner == "" {
		return matchAllFilter, nil
	}
	expr, rest, err := parseOr(inner)
	if err != nil {
		return nil, err
	}
	if rest = strings.TrimSpace(rest); rest != "" {
		return nil, fmt.Errorf("unexpected trailing text: %q", truncStr(rest, 30))
	}
	selectors := expr.selectors(nil)
	return &compiledFilter{
		extractsFields: true,
		eval: func(msg string) (matchedFields, bool) {
			if len(msg) == 0 || msg[0] != '{' {
				return nil, false
			}
			var data any
			if err := json.Unmarshal([]byte(msg), &data); err != nil {
				return nil, false
			}
			if !expr.eval(data) {
				return nil, false
			}
			return &jsonFields{data: data, selectors: selectors}, true
		},
	}, nil
}

// jsonFields is a matched JSON event: lookup walks the parsed document with
// the same resolvePath the pattern evaluated with, so `$.a.b` in a metricValue
// means exactly what it means in a pattern.
type jsonFields struct {
	data      any
	selectors [][]string
}

func (f *jsonFields) lookup(ref string) (string, bool) {
	if !strings.HasPrefix(ref, "$.") || len(ref) == 2 {
		return "", false
	}
	v, ok := resolvePath(f.data, strings.Split(ref[2:], "."))
	if !ok {
		return "", false
	}
	return jsonScalarString(v)
}

// named reports the properties the pattern's own selectors name, in the
// pattern's order, omitting any the event does not carry. The AWS reference
// documents extractedValues by example only for space-delimited and text
// patterns (API_TestMetricFilter.html); this is the JSON analogue of the
// space-delimited rule — the fields the pattern declares, with their values.
func (f *jsonFields) named() map[string]string {
	out := make(map[string]string, len(f.selectors))
	for _, path := range f.selectors {
		v, ok := resolvePath(f.data, path)
		if !ok {
			continue
		}
		if s, ok := jsonScalarString(v); ok {
			out["$."+strings.Join(path, ".")] = s
		}
	}
	return out
}

// jsonScalarString renders a JSON scalar the way a log field reads: numbers
// in their shortest exact form, booleans as true/false. Objects, arrays and
// null are not values a metric filter can use.
func jsonScalarString(v any) (string, bool) {
	switch t := v.(type) {
	case string:
		return t, true
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64), true
	case bool:
		return strconv.FormatBool(t), true
	default:
		return "", false
	}
}

// parseOr: andExpr ("||" andExpr)* .
func parseOr(s string) (jsonExpr, string, error) {
	left, rest, err := parseAnd(s)
	if err != nil {
		return nil, "", err
	}
	for {
		rest = strings.TrimSpace(rest)
		if !strings.HasPrefix(rest, "||") {
			return left, rest, nil
		}
		rest = strings.TrimSpace(rest[2:])
		var right jsonExpr
		right, rest, err = parseAnd(rest)
		if err != nil {
			return nil, "", err
		}
		left = &jsonOr{left: left, right: right}
	}
}

// parseAnd: atom ("&&" atom)* .
func parseAnd(s string) (jsonExpr, string, error) {
	left, rest, err := parseAtom(s)
	if err != nil {
		return nil, "", err
	}
	for {
		rest = strings.TrimSpace(rest)
		if !strings.HasPrefix(rest, "&&") {
			return left, rest, nil
		}
		rest = strings.TrimSpace(rest[2:])
		var right jsonExpr
		right, rest, err = parseAtom(rest)
		if err != nil {
			return nil, "", err
		}
		left = &jsonAnd{left: left, right: right}
	}
}

// parseAtom: $.path (op value | EXISTS | NOT EXISTS | IS [NOT] NULL).
func parseAtom(s string) (jsonExpr, string, error) {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "$.") {
		return nil, "", fmt.Errorf("expected $.path, got %q", truncStr(s, 30))
	}

	// Scan path characters: letters, digits, underscore, hyphen, dot.
	i := 2
	for i < len(s) && isPathChar(s[i]) {
		i++
	}
	if i == 2 {
		return nil, "", fmt.Errorf("empty path in filter expression")
	}
	path := strings.Split(s[2:i], ".")
	rest := strings.TrimSpace(s[i:])

	// Try keyword matches (longest first).
	if r, ok := consumeKeywords(rest, "IS", "NOT", "NULL"); ok {
		return &jsonIsNull{path: path, negate: true}, r, nil
	}
	if r, ok := consumeKeywords(rest, "IS", "NULL"); ok {
		return &jsonIsNull{path: path}, r, nil
	}
	if r, ok := consumeKeywords(rest, "NOT", "EXISTS"); ok {
		return &jsonExists{path: path, negate: true}, r, nil
	}
	if r, ok := consumeKeywords(rest, "EXISTS"); ok {
		return &jsonExists{path: path}, r, nil
	}

	// Comparison operator.
	op, rest, err := parseCmpOp(rest)
	if err != nil {
		return nil, "", err
	}
	val, rest, err := parseValue(rest)
	if err != nil {
		return nil, "", err
	}
	return &jsonCmp{path: path, op: op, val: val}, rest, nil
}

func isPathChar(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
		(c >= '0' && c <= '9') || c == '_' || c == '-' || c == '.'
}

// consumeKeywords checks whether s starts with a sequence of case-insensitive
// keywords separated by whitespace, each followed by a word boundary.
func consumeKeywords(s string, keywords ...string) (string, bool) {
	rest := s
	for _, kw := range keywords {
		rest = strings.TrimSpace(rest)
		if len(rest) < len(kw) {
			return s, false
		}
		if !strings.EqualFold(rest[:len(kw)], kw) {
			return s, false
		}
		rest = rest[len(kw):]
		if rest != "" && !isWordBoundary(rest[0]) {
			return s, false
		}
	}
	return strings.TrimSpace(rest), true
}

func isWordBoundary(c byte) bool {
	return c == ' ' || c == '\t' || c == '&' || c == '|' || c == '}'
}

func parseCmpOp(s string) (cmpOp, string, error) {
	s = strings.TrimSpace(s)
	switch {
	case strings.HasPrefix(s, "!="):
		return opNe, strings.TrimSpace(s[2:]), nil
	case strings.HasPrefix(s, ">="):
		return opGe, strings.TrimSpace(s[2:]), nil
	case strings.HasPrefix(s, "<="):
		return opLe, strings.TrimSpace(s[2:]), nil
	case strings.HasPrefix(s, "="):
		return opEq, strings.TrimSpace(s[1:]), nil
	case strings.HasPrefix(s, ">"):
		return opGt, strings.TrimSpace(s[1:]), nil
	case strings.HasPrefix(s, "<"):
		return opLt, strings.TrimSpace(s[1:]), nil
	default:
		return 0, "", fmt.Errorf("expected operator, got %q", truncStr(s, 20))
	}
}

func parseValue(s string) (any, string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, "", fmt.Errorf("expected value, got end of input")
	}
	// String literal.
	if s[0] == '"' {
		end := strings.Index(s[1:], `"`)
		if end < 0 {
			return nil, "", fmt.Errorf("unterminated string in filter pattern")
		}
		return s[1 : 1+end], s[2+end:], nil
	}
	// Numeric or boolean token.
	end := strings.IndexFunc(s, func(r rune) bool {
		return unicode.IsSpace(r) || r == '&' || r == '|' || r == '}'
	})
	var tok string
	if end < 0 {
		tok = s
		s = ""
	} else {
		tok = s[:end]
		s = s[end:]
	}
	if f, err := strconv.ParseFloat(tok, 64); err == nil {
		return f, s, nil
	}
	switch strings.ToLower(tok) {
	case "true":
		return true, s, nil
	case "false":
		return false, s, nil
	}
	return nil, "", fmt.Errorf("invalid value: %q", tok)
}

// ---------- Helpers ----------------------------------------------------------

// resolvePath walks a JSON value along the given dot-separated path segments.
func resolvePath(data any, path []string) (any, bool) {
	cur := data
	for _, key := range path {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		v, exists := m[key]
		if !exists {
			return nil, false
		}
		cur = v
	}
	return cur, true
}

// cmpValues compares two values using the given operator.
func cmpValues(a, b any, op cmpOp) bool {
	// Boolean comparison.
	if ab, ok := a.(bool); ok {
		if bb, ok := b.(bool); ok {
			switch op { //nolint:exhaustive // booleans only support equality
			case opEq:
				return ab == bb
			case opNe:
				return ab != bb
			default:
				return false
			}
		}
	}
	// Numeric comparison.
	if na, ok := toFloat(a); ok {
		if nb, ok := toFloat(b); ok {
			switch op {
			case opEq:
				return na == nb
			case opNe:
				return na != nb
			case opGt:
				return na > nb
			case opGe:
				return na >= nb
			case opLt:
				return na < nb
			case opLe:
				return na <= nb
			}
		}
	}
	// String comparison.
	sa, sb := fmt.Sprint(a), fmt.Sprint(b)
	switch op {
	case opEq:
		return sa == sb
	case opNe:
		return sa != sb
	case opGt:
		return sa > sb
	case opGe:
		return sa >= sb
	case opLt:
		return sa < sb
	case opLe:
		return sa <= sb
	}
	return false
}

func toFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	}
	return 0, false
}

func truncStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// ---------- Space-delimited (columnar) filter --------------------------------

// colMatcher tests whether a single field value matches a column constraint.
// A nil colMatcher means the column is unconstrained (matches anything).
type colMatcher func(field string) bool

// compileColumnarFilter compiles a space-delimited pattern. An empty `[]`
// matches every message and still names each field by position, which is
// what AWS's third TestMetricFilter example reports for it.
func compileColumnarFilter(pattern string) (*compiledFilter, error) {
	inner := strings.TrimSpace(pattern[1 : len(pattern)-1])

	var parts []string
	if inner != "" {
		parts = splitColumns(inner)
	}

	// Detect leading ellipsis.
	hasEllipsis := false
	if len(parts) > 0 && strings.TrimSpace(parts[0]) == "..." {
		hasEllipsis = true
		parts = parts[1:]
	}

	cols := make([]colMatcher, 0, len(parts))
	names := make([]string, 0, len(parts))
	for _, p := range parts {
		name, cm, err := compileColumnDef(strings.TrimSpace(p))
		if err != nil {
			return nil, err
		}
		cols = append(cols, cm)
		names = append(names, name)
	}

	return &compiledFilter{
		extractsFields: true,
		eval: func(msg string) (matchedFields, bool) {
			fields := splitLogFields(msg)
			if len(fields) < len(cols) {
				return nil, false
			}
			// With an ellipsis the columns align to the tail of the fields
			// (the last column matches the last field); without one, to the
			// head.
			offset := 0
			if hasEllipsis {
				offset = len(fields) - len(cols)
			}
			for i, cm := range cols {
				if cm != nil && !cm(fields[offset+i]) {
					return nil, false
				}
			}
			return &columnFields{fields: fields, names: names, offset: offset}, true
		},
	}, nil
}

// columnFields is a matched space-delimited event: the split fields plus the
// pattern's column names, aligned at offset.
type columnFields struct {
	fields []string
	names  []string // "" for an unnamed column
	offset int
}

// nameOf is the key AWS reports a field under: `$<name>` for a column the
// pattern names, else `$<position>`, 1-based — including every field an
// ellipsis absorbed (API_TestMetricFilter.html, examples 2, 3 and 5).
func (f *columnFields) nameOf(i int) string {
	if col := i - f.offset; col >= 0 && col < len(f.names) && f.names[col] != "" {
		return "$" + f.names[col]
	}
	return "$" + strconv.Itoa(i+1)
}

func (f *columnFields) lookup(ref string) (string, bool) {
	if len(ref) < 2 || ref[0] != '$' {
		return "", false
	}
	for i := range f.fields {
		if f.nameOf(i) == ref {
			return unwrapLogField(f.fields[i]), true
		}
	}
	return "", false
}

func (f *columnFields) named() map[string]string {
	out := make(map[string]string, len(f.fields))
	for i, v := range f.fields {
		out[f.nameOf(i)] = unwrapLogField(v)
	}
	return out
}

// unwrapLogField strips the brackets or quotes splitLogFields keeps around a
// bracketed or quoted field: the extracted value AWS reports is the text
// inside them (`$timestamp` is `10/Oct/2000:13:25:15 -0700`, not the
// bracketed form — API_TestMetricFilter.html, example 1).
func unwrapLogField(field string) string {
	if n := len(field); n >= 2 {
		switch {
		case field[0] == '[' && field[n-1] == ']', field[0] == '"' && field[n-1] == '"':
			return field[1 : n-1]
		}
	}
	return field
}

// splitColumns splits on commas, but respects quoted strings.
func splitColumns(s string) []string {
	var parts []string
	var cur strings.Builder
	inQuote := false
	for i := 0; i < len(s); i++ {
		ch := s[i]
		if ch == '"' {
			inQuote = !inQuote
			cur.WriteByte(ch)
		} else if ch == ',' && !inQuote {
			parts = append(parts, cur.String())
			cur.Reset()
		} else {
			cur.WriteByte(ch)
		}
	}
	parts = append(parts, cur.String())
	return parts
}

// splitLogFields splits a log message into space-delimited fields.
// Characters between brackets ([...]) and double quotes ("...") are treated
// as a single field, per the AWS CloudWatch Logs specification.
func splitLogFields(s string) []string {
	var fields []string
	s = strings.TrimSpace(s)
	for s != "" {
		switch s[0] {
		case '[':
			// Bracketed field — find closing ']'.
			end := strings.IndexByte(s, ']')
			if end < 0 {
				fields = append(fields, s)
				s = ""
			} else {
				fields = append(fields, s[:end+1])
				s = strings.TrimLeft(s[end+1:], " \t")
			}
		case '"':
			// Quoted field — find closing '"'.
			end := strings.IndexByte(s[1:], '"')
			if end < 0 {
				fields = append(fields, s)
				s = ""
			} else {
				fields = append(fields, s[:end+2])
				s = strings.TrimLeft(s[end+2:], " \t")
			}
		default:
			idx := strings.IndexAny(s, " \t")
			if idx < 0 {
				fields = append(fields, s)
				s = ""
			} else {
				fields = append(fields, s[:idx])
				s = strings.TrimLeft(s[idx:], " \t")
			}
		}
	}
	return fields
}

// compileColumnDef compiles a single column definition into its name and a
// colMatcher. Supports:
//
//	(empty)                     → "", nil (unnamed, unconstrained)
//	name                        → name, nil (bare name, no constraint)
//	name = value                → equality (string/glob/regex)
//	name != value               → not-equals
//	name > 100                  → numeric comparison
//	name = 404 || name = 410    → compound OR
//	name != X && name != Y      → compound AND
//
// The name is what a metric filter's `$name` reference and TestMetricFilter's
// extractedValues key on; a compound definition takes the name of its first
// term, which is the column every term constrains.
func compileColumnDef(s string) (string, colMatcher, error) {
	if s == "" {
		return "", nil, nil
	}

	// Check for compound expressions (|| then &&).
	if name, cm, ok, err := tryCompoundColumn(s); ok {
		return name, cm, err
	}

	return compileSingleColumn(s)
}

// tryCompoundColumn checks for || or && in the column definition.
// Returns (name, matcher, true, nil/err) if a compound expression was found,
// or ("", nil, false, nil) if no compound operator exists.
func tryCompoundColumn(s string) (string, colMatcher, bool, error) {
	// Check for || first (lower precedence).
	if parts := splitCompound(s, "||"); len(parts) > 1 {
		name, matchers, err := compileCompoundTerms(parts)
		if err != nil {
			return "", nil, true, err
		}
		if len(matchers) == 0 {
			return name, nil, true, nil
		}
		return name, func(field string) bool {
			for _, m := range matchers {
				if m(field) {
					return true
				}
			}
			return false
		}, true, nil
	}
	// Check for &&.
	if parts := splitCompound(s, "&&"); len(parts) > 1 {
		name, matchers, err := compileCompoundTerms(parts)
		if err != nil {
			return "", nil, true, err
		}
		if len(matchers) == 0 {
			return name, nil, true, nil
		}
		return name, func(field string) bool {
			for _, m := range matchers {
				if !m(field) {
					return false
				}
			}
			return true
		}, true, nil
	}
	return "", nil, false, nil
}

// compileCompoundTerms compiles each term of a compound column definition,
// keeping the constrained ones and the first term's column name.
func compileCompoundTerms(parts []string) (string, []colMatcher, error) {
	var name string
	var matchers []colMatcher
	for i, part := range parts {
		termName, cm, err := compileSingleColumn(strings.TrimSpace(part))
		if err != nil {
			return "", nil, err
		}
		if i == 0 {
			name = termName
		}
		if cm != nil {
			matchers = append(matchers, cm)
		}
	}
	return name, matchers, nil
}

// splitCompound splits s on a compound operator, respecting quoted strings.
func splitCompound(s, op string) []string {
	var parts []string
	var cur strings.Builder
	inQuote := false
	inPercent := false
	for i := 0; i < len(s); i++ {
		ch := s[i]
		if ch == '"' {
			inQuote = !inQuote
		}
		if ch == '%' {
			inPercent = !inPercent
		}
		if !inQuote && !inPercent && i+len(op) <= len(s) && s[i:i+len(op)] == op {
			parts = append(parts, cur.String())
			cur.Reset()
			i += len(op) - 1
			continue
		}
		cur.WriteByte(ch)
	}
	parts = append(parts, cur.String())
	return parts
}

// compileSingleColumn compiles one simple constraint: "name op value" or bare
// "name", returning the column's name alongside its matcher.
func compileSingleColumn(s string) (string, colMatcher, error) {
	if s == "" {
		return "", nil, nil
	}

	op, nameRaw, valRaw, found := splitColumnOp(s)
	if !found {
		// Bare name — no constraint.
		return s, nil, nil
	}

	name := strings.TrimSpace(nameRaw)
	val := strings.TrimSpace(valRaw)
	if name == "" || val == "" {
		return "", nil, fmt.Errorf("invalid column definition: %q", s)
	}
	cm, err := compileColumnConstraint(name, op, val)
	if err != nil {
		return "", nil, err
	}
	return name, cm, nil
}

// compileColumnConstraint compiles one `name op value` constraint's matcher.
func compileColumnConstraint(name string, op cmpOp, val string) (colMatcher, error) {

	// Strip quotes from value.
	if len(val) >= 2 && val[0] == '"' && val[len(val)-1] == '"' {
		val = val[1 : len(val)-1]
	}

	// Regex: %pattern%
	if len(val) >= 2 && val[0] == '%' && val[len(val)-1] == '%' {
		re, err := regexp.Compile(val[1 : len(val)-1])
		if err != nil {
			return nil, fmt.Errorf("invalid regex in column %q: %w", name, err)
		}
		if op == opNe {
			return func(field string) bool { return !re.MatchString(field) }, nil
		}
		return func(field string) bool { return re.MatchString(field) }, nil
	}

	// Glob: contains '*'
	if strings.Contains(val, "*") {
		gm := compileGlob(val)
		if op == opNe {
			return func(field string) bool { return !gm(field) }, nil
		}
		return func(field string) bool { return gm(field) }, nil
	}

	// Numeric comparison if value parses as a number.
	if numVal, err := strconv.ParseFloat(val, 64); err == nil {
		return func(field string) bool {
			if fv, ferr := strconv.ParseFloat(field, 64); ferr == nil {
				switch op {
				case opEq:
					return fv == numVal
				case opNe:
					return fv != numVal
				case opGt:
					return fv > numVal
				case opGe:
					return fv >= numVal
				case opLt:
					return fv < numVal
				case opLe:
					return fv <= numVal
				}
			}
			// Fall through to string comparison for non-numeric field values.
			switch op { //nolint:exhaustive // non-numeric fields only support equality
			case opEq:
				return field == val
			case opNe:
				return field != val
			default:
				return false
			}
		}, nil
	}

	// String comparison.
	return func(field string) bool {
		switch op { //nolint:exhaustive // strings only support equality
		case opEq:
			return field == val
		case opNe:
			return field != val
		default:
			return false
		}
	}, nil
}

// splitColumnOp finds the first operator in a column definition.
// It returns the operator, left side, right side, and whether an operator was found.
// When multiple operators appear (e.g. inside a %regex%), the one at the
// earliest position wins.
func splitColumnOp(s string) (cmpOp, string, string, bool) {
	type match struct {
		op  cmpOp
		idx int
		len int
	}
	best := match{idx: len(s)}
	found := false
	for _, pair := range []struct {
		lit string
		op  cmpOp
	}{
		{"!=", opNe},
		{">=", opGe},
		{"<=", opLe},
		{">", opGt},
		{"<", opLt},
		{"=", opEq},
	} {
		if idx := strings.Index(s, pair.lit); idx >= 0 && idx < best.idx {
			best = match{op: pair.op, idx: idx, len: len(pair.lit)}
			found = true
		}
	}
	if !found {
		return 0, "", "", false
	}
	// If a single-char operator sits at the same position as a two-char
	// operator (e.g. "=" at 4 vs ">=" at 4), prefer the longer match.
	for _, pair := range []struct {
		lit string
		op  cmpOp
	}{
		{"!=", opNe},
		{">=", opGe},
		{"<=", opLe},
	} {
		if idx := strings.Index(s, pair.lit); idx == best.idx && len(pair.lit) > best.len {
			best = match{op: pair.op, idx: idx, len: len(pair.lit)}
		}
	}
	return best.op, s[:best.idx], s[best.idx+best.len:], true
}

// compileGlob returns a match function for a '*' wildcard pattern.
// Common shapes are compiled to strings.HasPrefix/HasSuffix/Contains
// to avoid the general-purpose DP matcher.
func compileGlob(pattern string) func(string) bool {
	if pattern == "*" {
		return func(string) bool { return true }
	}
	if !strings.Contains(pattern, "*") {
		return func(s string) bool { return s == pattern }
	}

	// Count stars to detect simple patterns.
	starCount := strings.Count(pattern, "*")

	if starCount == 1 {
		if pattern[len(pattern)-1] == '*' {
			// "prefix*"
			prefix := pattern[:len(pattern)-1]
			return func(s string) bool { return strings.HasPrefix(s, prefix) }
		}
		if pattern[0] == '*' {
			// "*suffix"
			suffix := pattern[1:]
			return func(s string) bool { return strings.HasSuffix(s, suffix) }
		}
		// "prefix*suffix"
		parts := strings.SplitN(pattern, "*", 2)
		prefix, suffix := parts[0], parts[1]
		minLen := len(prefix) + len(suffix)
		return func(s string) bool {
			return len(s) >= minLen && strings.HasPrefix(s, prefix) && strings.HasSuffix(s, suffix)
		}
	}

	if starCount == 2 && pattern[0] == '*' && pattern[len(pattern)-1] == '*' {
		// "*infix*"
		infix := pattern[1 : len(pattern)-1]
		if !strings.Contains(infix, "*") {
			return func(s string) bool { return strings.Contains(s, infix) }
		}
	}

	// General case: two-row DP.
	return func(s string) bool { return globMatchDP(pattern, s) }
}

// globMatchDP uses a two-row DP to match pattern (with '*' wildcards) against s.
func globMatchDP(pattern, s string) bool {
	pn, sn := len(pattern), len(s)
	// Two rows — swap prev/cur to avoid allocating per-character.
	prev := make([]bool, sn+1)
	cur := make([]bool, sn+1)
	prev[0] = true

	for i := 1; i <= pn; i++ {
		// Reset cur row.
		for j := range cur {
			cur[j] = false
		}
		pc := pattern[i-1]
		if pc == '*' {
			cur[0] = prev[0]
			for j := 1; j <= sn; j++ {
				cur[j] = prev[j] || cur[j-1]
			}
		} else {
			for j := 1; j <= sn; j++ {
				if pc == s[j-1] {
					cur[j] = prev[j-1]
				}
			}
		}
		prev, cur = cur, prev
	}
	return prev[sn]
}
