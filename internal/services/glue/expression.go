package glue

// expression.go — GetPartitions' Expression filter.
//
// AWS parses Expression with JSQLParser as a SQL WHERE clause over the
// table's partition keys, and documents these operators: =, <>, >, <, >=,
// <=, AND, OR, IN, BETWEEN, LIKE, NOT and IS NULL. This is a small
// recursive-descent parser for exactly that subset, plus parentheses and
// `!=` (JSQLParser's synonym for `<>`).
//
// Anything outside it is an InvalidInputException rather than a filter that
// silently matches everything: a query engine pruning partitions would read
// the wrong data and never know.
//
// Comparison follows the partition key's declared type, as AWS does: the
// integer and decimal types compare numerically, and string, char, varchar,
// date and timestamp compare as strings — which orders ISO-8601 dates and
// timestamps correctly. A key of any other type cannot appear in an
// expression.

import (
	"fmt"
	"math/big"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// partitionFilter is a compiled Expression.
type partitionFilter interface {
	match(values map[string]string) bool
}

// keyKind is how a partition key's values compare.
type keyKind int

const (
	kindString keyKind = iota
	kindNumber
)

// partitionKeyKinds maps each partition key (lowercased) to its comparison
// kind, rejecting a key whose type AWS does not allow in an expression.
func partitionKeyKinds(keys []Column) map[string]keyKind {
	kinds := make(map[string]keyKind, len(keys))
	for _, k := range keys {
		kind, ok := kindOf(k.Type)
		if !ok {
			// Recorded as absent: naming it in an expression is an error,
			// which is what AWS reports for an unsupported key type.
			continue
		}
		kinds[strings.ToLower(k.Name)] = kind
	}
	return kinds
}

func kindOf(colType string) (keyKind, bool) {
	t := strings.ToLower(strings.TrimSpace(colType))
	if i := strings.IndexByte(t, '('); i >= 0 {
		t = t[:i] // decimal(10,2), varchar(20), char(3)
	}
	switch t {
	case "", "string", "char", "varchar", "date", "timestamp":
		return kindString, true
	case "int", "integer", "bigint", "long", "tinyint", "smallint", "decimal":
		return kindNumber, true
	default:
		return 0, false
	}
}

// parsePartitionExpression compiles expr against the table's partition keys.
// An empty expression matches every partition.
func parsePartitionExpression(expr string, keys []Column) (partitionFilter, error) {
	if strings.TrimSpace(expr) == "" {
		return matchAll{}, nil
	}
	toks, err := lexExpression(expr)
	if err != nil {
		return nil, err
	}
	p := &exprParser{toks: toks, kinds: partitionKeyKinds(keys), declared: declaredKeys(keys)}
	f, err := p.parseOr()
	if err != nil {
		return nil, err
	}
	if p.peek().kind != tokEOF {
		return nil, fmt.Errorf("unexpected %q", p.peek().text)
	}
	return f, nil
}

func declaredKeys(keys []Column) map[string]bool {
	out := make(map[string]bool, len(keys))
	for _, k := range keys {
		out[strings.ToLower(k.Name)] = true
	}
	return out
}

// ─── Lexer ─────────────────────────────────────────────────────

type tokKind int

const (
	tokEOF tokKind = iota
	tokIdent
	tokString
	tokNumber
	tokOp // = <> != < <= > >=
	tokLParen
	tokRParen
	tokComma
	tokKeyword // AND OR NOT IN BETWEEN LIKE IS NULL
)

type token struct {
	kind tokKind
	text string // keywords upper-cased; identifiers as written, unquoted
}

var keywords = map[string]bool{
	"AND": true, "OR": true, "NOT": true, "IN": true,
	"BETWEEN": true, "LIKE": true, "IS": true, "NULL": true,
}

func lexExpression(s string) ([]token, error) {
	var toks []token
	for i := 0; i < len(s); {
		c := s[i]
		switch {
		case c == ' ' || c == '\t' || c == '\r' || c == '\n':
			i++
		case c == '(':
			toks = append(toks, token{tokLParen, "("})
			i++
		case c == ')':
			toks = append(toks, token{tokRParen, ")"})
			i++
		case c == ',':
			toks = append(toks, token{tokComma, ","})
			i++
		case c == '=':
			toks = append(toks, token{tokOp, "="})
			i++
		case c == '<' || c == '>' || c == '!':
			op := opSpelling(c, s, i)
			i += len(op)
			switch op {
			case "!":
				return nil, fmt.Errorf("unexpected %q", op)
			case "!=":
				op = "<>"
			}
			toks = append(toks, token{tokOp, op})
		case c == '\'':
			lit, n, err := lexQuoted(s[i:], '\'')
			if err != nil {
				return nil, err
			}
			toks = append(toks, token{tokString, lit})
			i += n
		case c == '"' || c == '`':
			id, n, err := lexQuoted(s[i:], c)
			if err != nil {
				return nil, err
			}
			toks = append(toks, token{tokIdent, id})
			i += n
		case c == '-' || c == '+' || c == '.' || (c >= '0' && c <= '9'):
			j := i + 1
			for j < len(s) && isNumberByte(s[j]) {
				// A sign belongs to the number only straight after an exponent.
				if (s[j] == '+' || s[j] == '-') && s[j-1] != 'e' && s[j-1] != 'E' {
					break
				}
				j++
			}
			num := s[i:j]
			if _, ok := parseDecimal(num); !ok {
				return nil, fmt.Errorf("invalid number %q", num)
			}
			toks = append(toks, token{tokNumber, num})
			i = j
		case isIdentStart(s[i:]):
			j := i
			for j < len(s) {
				r, size := utf8.DecodeRuneInString(s[j:])
				if r != '_' && r != '$' && !unicode.IsLetter(r) && !unicode.IsDigit(r) {
					break
				}
				j += size
			}
			word := s[i:j]
			if up := strings.ToUpper(word); keywords[up] {
				toks = append(toks, token{tokKeyword, up})
			} else {
				toks = append(toks, token{tokIdent, word})
			}
			i = j
		default:
			return nil, fmt.Errorf("unexpected character %q", c)
		}
	}
	return append(toks, token{tokEOF, ""}), nil
}

// isNumberByte reports whether b can continue a numeric literal: digits, a
// decimal point, an exponent marker, or an exponent's sign.
func isNumberByte(b byte) bool {
	return (b >= '0' && b <= '9') || b == '.' || b == 'e' || b == 'E' || b == '+' || b == '-'
}

// isIdentStart reports whether s starts an unquoted identifier: a letter, in
// any script, or an underscore.
func isIdentStart(s string) bool {
	r, _ := utf8.DecodeRuneInString(s)
	return r == '_' || unicode.IsLetter(r)
}

// decimalPattern is a plain decimal number with an optional exponent. It is
// deliberately stricter than big.Rat's own syntax, which also accepts
// fractions ("1/2") and base prefixes ("0x10") that are not SQL numbers.
var decimalPattern = regexp.MustCompile(`^[+-]?([0-9]+\.?[0-9]*|\.[0-9]+)([eE][+-]?[0-9]+)?$`)

// parseDecimal parses a SQL numeric literal or a stored numeric partition
// value.
func parseDecimal(s string) (*big.Rat, bool) {
	if !decimalPattern.MatchString(s) {
		return nil, false
	}
	return new(big.Rat).SetString(s)
}

// opSpelling is the source text of the comparison operator starting at s[i].
func opSpelling(c byte, s string, i int) string {
	if i+1 < len(s) && (s[i+1] == '=' || (c == '<' && s[i+1] == '>')) {
		return s[i : i+2]
	}
	return s[i : i+1]
}

// lexQuoted reads a quote-delimited token starting at s[0], where a doubled
// quote stands for one quote character. It returns the unquoted text and the
// number of bytes consumed.
func lexQuoted(s string, q byte) (string, int, error) {
	var b strings.Builder
	for i := 1; i < len(s); i++ {
		if s[i] != q {
			b.WriteByte(s[i])
			continue
		}
		if i+1 < len(s) && s[i+1] == q {
			b.WriteByte(q)
			i++
			continue
		}
		return b.String(), i + 1, nil
	}
	return "", 0, fmt.Errorf("unterminated %c", q)
}

// ─── Parser ────────────────────────────────────────────────────

type exprParser struct {
	toks     []token
	pos      int
	kinds    map[string]keyKind
	declared map[string]bool
}

func (p *exprParser) peek() token { return p.toks[p.pos] }

func (p *exprParser) next() token {
	t := p.toks[p.pos]
	if t.kind != tokEOF {
		p.pos++
	}
	return t
}

func (p *exprParser) acceptKeyword(kw string) bool {
	if t := p.peek(); t.kind == tokKeyword && t.text == kw {
		p.pos++
		return true
	}
	return false
}

func (p *exprParser) expect(kind tokKind, what string) (token, error) {
	t := p.next()
	if t.kind != kind {
		return t, fmt.Errorf("expected %s, found %q", what, t.text)
	}
	return t, nil
}

func (p *exprParser) parseOr() (partitionFilter, error) {
	left, err := p.parseAnd()
	if err != nil {
		return nil, err
	}
	for p.acceptKeyword("OR") {
		right, err := p.parseAnd()
		if err != nil {
			return nil, err
		}
		left = orFilter{left, right}
	}
	return left, nil
}

func (p *exprParser) parseAnd() (partitionFilter, error) {
	left, err := p.parseNot()
	if err != nil {
		return nil, err
	}
	for p.acceptKeyword("AND") {
		right, err := p.parseNot()
		if err != nil {
			return nil, err
		}
		left = andFilter{left, right}
	}
	return left, nil
}

func (p *exprParser) parseNot() (partitionFilter, error) {
	if p.acceptKeyword("NOT") {
		inner, err := p.parseNot()
		if err != nil {
			return nil, err
		}
		return notFilter{inner}, nil
	}
	return p.parsePredicate()
}

// operand is one side of a predicate: a partition key or a literal.
type operand struct {
	column string // lowercased key name; empty for a literal
	lit    string
}

func (p *exprParser) parseOperand() (operand, error) {
	t := p.next()
	switch t.kind {
	case tokIdent:
		col := strings.ToLower(t.text)
		if !p.declared[col] {
			return operand{}, fmt.Errorf("unknown partition key %q", t.text)
		}
		if _, ok := p.kinds[col]; !ok {
			return operand{}, fmt.Errorf("partition key %q has a type that cannot be filtered on", t.text)
		}
		return operand{column: col}, nil
	case tokString, tokNumber:
		return operand{lit: t.text}, nil
	case tokEOF, tokOp, tokLParen, tokRParen, tokComma, tokKeyword:
	}
	return operand{}, fmt.Errorf("expected a partition key or a literal, found %q", t.text)
}

func (p *exprParser) parsePredicate() (partitionFilter, error) {
	if p.peek().kind == tokLParen {
		p.next()
		inner, err := p.parseOr()
		if err != nil {
			return nil, err
		}
		if _, err := p.expect(tokRParen, ")"); err != nil {
			return nil, err
		}
		return inner, nil
	}

	left, err := p.parseOperand()
	if err != nil {
		return nil, err
	}

	// <operand> <op> <operand>
	if t := p.peek(); t.kind == tokOp {
		p.next()
		right, err := p.parseOperand()
		if err != nil {
			return nil, err
		}
		return p.comparison(left, t.text, right)
	}

	col, err := p.requireColumn(left)
	if err != nil {
		return nil, err
	}

	if p.acceptKeyword("IS") {
		negate := p.acceptKeyword("NOT")
		if !p.acceptKeyword("NULL") {
			return nil, fmt.Errorf("expected NULL after IS, found %q", p.peek().text)
		}
		// A stored partition always has a value for every key.
		return constFilter(negate), nil
	}

	negate := p.acceptKeyword("NOT")
	var f partitionFilter
	switch {
	case p.acceptKeyword("IN"):
		f, err = p.parseIn(col)
	case p.acceptKeyword("BETWEEN"):
		f, err = p.parseBetween(col)
	case p.acceptKeyword("LIKE"):
		f, err = p.parseLike(col)
	default:
		return nil, fmt.Errorf("expected an operator after %q, found %q", col, p.peek().text)
	}
	if err != nil {
		return nil, err
	}
	if negate {
		f = notFilter{f}
	}
	return f, nil
}

func (p *exprParser) requireColumn(o operand) (string, error) {
	if o.column == "" {
		return "", fmt.Errorf("expected a partition key, found literal %q", o.lit)
	}
	return o.column, nil
}

// comparison builds `left op right`, flipping a literal-first comparison so
// the partition key is always on the left.
func (p *exprParser) comparison(left operand, op string, right operand) (partitionFilter, error) {
	switch {
	case left.column != "" && right.column == "":
	case left.column == "" && right.column != "":
		left, right = right, left
		op = flipOp(op)
	case left.column != "" && right.column != "":
		return nil, fmt.Errorf("comparing two partition keys (%q and %q) is not supported", left.column, right.column)
	default:
		return nil, fmt.Errorf("a comparison needs a partition key")
	}
	kind := p.kinds[left.column]
	if err := checkLiteral(kind, left.column, right.lit); err != nil {
		return nil, err
	}
	return cmpFilter{column: left.column, op: op, lit: right.lit, kind: kind}, nil
}

func flipOp(op string) string {
	switch op {
	case "<":
		return ">"
	case "<=":
		return ">="
	case ">":
		return "<"
	case ">=":
		return "<="
	default: // = and <> are symmetric
		return op
	}
}

func checkLiteral(kind keyKind, col, lit string) error {
	if kind == kindNumber {
		if _, ok := parseDecimal(lit); !ok {
			return fmt.Errorf("%q is not a number, but partition key %q is numeric", lit, col)
		}
	}
	return nil
}

func (p *exprParser) literal(col string) (string, error) {
	t := p.next()
	if t.kind != tokString && t.kind != tokNumber {
		return "", fmt.Errorf("expected a literal, found %q", t.text)
	}
	if err := checkLiteral(p.kinds[col], col, t.text); err != nil {
		return "", err
	}
	return t.text, nil
}

func (p *exprParser) parseIn(col string) (partitionFilter, error) {
	if _, err := p.expect(tokLParen, "( after IN"); err != nil {
		return nil, err
	}
	var alts orFilterList
	for {
		lit, err := p.literal(col)
		if err != nil {
			return nil, err
		}
		alts = append(alts, cmpFilter{column: col, op: "=", lit: lit, kind: p.kinds[col]})
		if p.peek().kind == tokComma {
			p.next()
			continue
		}
		if _, err := p.expect(tokRParen, ") closing IN"); err != nil {
			return nil, err
		}
		return alts, nil
	}
}

func (p *exprParser) parseBetween(col string) (partitionFilter, error) {
	lo, err := p.literal(col)
	if err != nil {
		return nil, err
	}
	if !p.acceptKeyword("AND") {
		return nil, fmt.Errorf("expected AND in BETWEEN, found %q", p.peek().text)
	}
	hi, err := p.literal(col)
	if err != nil {
		return nil, err
	}
	kind := p.kinds[col]
	return andFilter{
		cmpFilter{column: col, op: ">=", lit: lo, kind: kind},
		cmpFilter{column: col, op: "<=", lit: hi, kind: kind},
	}, nil
}

func (p *exprParser) parseLike(col string) (partitionFilter, error) {
	t, err := p.expect(tokString, "a string pattern after LIKE")
	if err != nil {
		return nil, err
	}
	var b strings.Builder
	b.WriteString("^")
	for _, r := range t.text {
		switch r {
		case '%':
			b.WriteString(".*")
		case '_':
			b.WriteString(".")
		default:
			b.WriteString(regexp.QuoteMeta(string(r)))
		}
	}
	b.WriteString("$")
	return likeFilter{column: col, re: regexp.MustCompile(b.String())}, nil
}

// ─── Filters ───────────────────────────────────────────────────

type matchAll struct{}

func (matchAll) match(map[string]string) bool { return true }

type constFilter bool

func (c constFilter) match(map[string]string) bool { return bool(c) }

type andFilter struct{ l, r partitionFilter }

func (f andFilter) match(v map[string]string) bool { return f.l.match(v) && f.r.match(v) }

type orFilter struct{ l, r partitionFilter }

func (f orFilter) match(v map[string]string) bool { return f.l.match(v) || f.r.match(v) }

type orFilterList []partitionFilter

func (fs orFilterList) match(v map[string]string) bool {
	for _, f := range fs {
		if f.match(v) {
			return true
		}
	}
	return false
}

type notFilter struct{ inner partitionFilter }

func (f notFilter) match(v map[string]string) bool { return !f.inner.match(v) }

type likeFilter struct {
	column string
	re     *regexp.Regexp
}

func (f likeFilter) match(v map[string]string) bool { return f.re.MatchString(v[f.column]) }

type cmpFilter struct {
	column string
	op     string
	lit    string
	kind   keyKind
}

func (f cmpFilter) match(v map[string]string) bool {
	c, ok := compareValues(f.kind, v[f.column], f.lit)
	if !ok {
		// A stored value that is not a number under a numeric key matches
		// no comparison at all.
		return false
	}
	switch f.op {
	case "=":
		return c == 0
	case "<>":
		return c != 0
	case "<":
		return c < 0
	case "<=":
		return c <= 0
	case ">":
		return c > 0
	case ">=":
		return c >= 0
	}
	return false
}

func compareValues(kind keyKind, value, lit string) (int, bool) {
	if kind == kindString {
		return strings.Compare(value, lit), true
	}
	a, ok := parseDecimal(value)
	if !ok {
		return 0, false
	}
	b, _ := parseDecimal(lit) // validated when the expression was parsed
	return a.Cmp(b), true
}
