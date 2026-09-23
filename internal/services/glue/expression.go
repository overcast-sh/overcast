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
	// kindUnsupported is a declared key whose type AWS does not allow in an
	// expression: naming it is an error, as AWS reports.
	kindUnsupported
)

// partitionKeyName is how an expression names a partition key: its name,
// lowercased.
func partitionKeyName(k Column) string { return strings.ToLower(k.Name) }

// partitionKeyKinds maps every declared partition key to its comparison kind.
// Of two keys that fold to one name, a filterable one wins.
func partitionKeyKinds(keys []Column) map[string]keyKind {
	kinds := make(map[string]keyKind, len(keys))
	for _, k := range keys {
		name, kind := partitionKeyName(k), kindOf(k.Type)
		if _, seen := kinds[name]; seen && kind == kindUnsupported {
			continue
		}
		kinds[name] = kind
	}
	return kinds
}

func kindOf(colType string) keyKind {
	t := strings.ToLower(strings.TrimSpace(colType))
	if i := strings.IndexByte(t, '('); i >= 0 {
		t = t[:i] // decimal(10,2), varchar(20), char(3)
	}
	switch t {
	case "", "string", "char", "varchar", "date", "timestamp":
		return kindString
	case "int", "integer", "bigint", "long", "tinyint", "smallint", "decimal":
		return kindNumber
	default:
		return kindUnsupported
	}
}

// parsePartitionExpression compiles expr against the table's partition keys.
// An empty expression matches every partition.
func parsePartitionExpression(expr string, keys []Column) (partitionFilter, error) {
	if strings.TrimSpace(expr) == "" {
		return constFilter(true), nil
	}
	toks, err := lexExpression(expr)
	if err != nil {
		return nil, err
	}
	p := &exprParser{toks: toks, kinds: partitionKeyKinds(keys)}
	f, err := p.parseOr()
	if err != nil {
		return nil, err
	}
	if p.peek().kind != tokEOF {
		return nil, fmt.Errorf("unexpected %q", p.peek().text)
	}
	return f, nil
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

// ─── Parser ────────────────────────────────────────────────────

type exprParser struct {
	toks  []token
	pos   int
	kinds map[string]keyKind // every declared partition key; see partitionKeyKinds
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
		kind, declared := p.kinds[col]
		if !declared {
			return operand{}, fmt.Errorf("unknown partition key %q", t.text)
		}
		if kind == kindUnsupported {
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

	col, err := requireColumn(left)
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

func requireColumn(o operand) (string, error) {
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
	if err := checkLiteral(p.kinds[left.column], left.column, right.lit); err != nil {
		return nil, err
	}
	return p.cmp(left.column, op, right.lit), nil
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

// cmp builds the comparison `col op lit`, whose literal checkLiteral has
// accepted for col.
func (p *exprParser) cmp(col, op, lit string) cmpFilter {
	f := cmpFilter{column: col, op: op, lit: lit, kind: p.kinds[col]}
	if f.kind == kindNumber {
		f.num, _ = parseDecimal(lit)
	}
	return f
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
	var alts orFilter
	for {
		lit, err := p.literal(col)
		if err != nil {
			return nil, err
		}
		alts = append(alts, p.cmp(col, "=", lit))
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
	return andFilter{p.cmp(col, ">=", lo), p.cmp(col, "<=", hi)}, nil
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

// constFilter matches every partition or none: an empty expression, or IS
// [NOT] NULL, which a stored partition's values never are.
type constFilter bool

func (c constFilter) match(map[string]string) bool { return bool(c) }

type andFilter struct{ l, r partitionFilter }

func (f andFilter) match(v map[string]string) bool { return f.l.match(v) && f.r.match(v) }

type orFilter []partitionFilter

func (fs orFilter) match(v map[string]string) bool {
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
	num    *big.Rat // lit, parsed, when kind is kindNumber
}

func (f cmpFilter) match(v map[string]string) bool {
	c, ok := f.compare(v[f.column])
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

// compare orders a stored value against the literal under the key's kind.
// It is false when a numeric key's stored value is not a number.
func (f cmpFilter) compare(value string) (int, bool) {
	if f.kind == kindString {
		return strings.Compare(value, f.lit), true
	}
	n, ok := parseDecimal(value)
	if !ok {
		return 0, false
	}
	return n.Cmp(f.num), true
}
