package dynamodb

// expr_key.go implements DynamoDB KeyConditionExpression parsing.
//
// A KeyConditionExpression must always include an equality condition on the
// partition key, and may optionally include a condition on the sort key:
//
//   hashAttr = :h
//   hashAttr = :h AND sortAttr = :s
//   hashAttr = :h AND sortAttr < :s
//   hashAttr = :h AND sortAttr <= :s
//   hashAttr = :h AND sortAttr > :s
//   hashAttr = :h AND sortAttr >= :s
//   hashAttr = :h AND sortAttr BETWEEN :lo AND :hi
//   hashAttr = :h AND begins_with(sortAttr, :prefix)
//
// The two conditions may be written in either order. AWS's rules constrain
// which operator each key may use — "You must specify the partition key name
// and value as an equality condition", and the seven forms above for the sort
// key — never the position the condition is written in
// (https://docs.aws.amazon.com/amazondynamodb/latest/developerguide/Query.KeyConditionExpressions.html).
// So "sortAttr > :s AND hashAttr = :h" and "begins_with(sortAttr, :p) AND
// hashAttr = :h" are as legal as the hash-first spellings, which is what
// moto's parser covers in test_reverse_keys
// (tests/test_dynamodb/models/test_key_condition_expression_parser.py).
// This file therefore parses two positionally interchangeable *terms* and
// assigns their roles afterwards: the partition-key condition is the
// equality, and when both are equalities the tie is broken against the key
// schema by validateKeyConditionSchema (key_schema.go), which is the only
// place that knows the schema.

import (
	"errors"
	"fmt"
	"strings"
)

// keyCond is a parsed key condition.
type keyCond struct {
	hashAttr string
	hashVal  attrValue

	// Sort key condition (nil when no sort key condition).
	sortCond *sortKeyCond
}

// sortKeyCondKind classifies the sort key condition type.
type sortKeyCondKind int

const (
	sortKeyEq sortKeyCondKind = iota
	sortKeyLT
	sortKeyLE
	sortKeyGT
	sortKeyGE
	sortKeyBetween
	sortKeyBeginsWith
)

// sortKeyCond is a parsed sort key condition within a KeyConditionExpression.
type sortKeyCond struct {
	attr string
	kind sortKeyCondKind
	val  attrValue // for single-value conditions
	lo   attrValue // for BETWEEN
	hi   attrValue // for BETWEEN
}

// matchItem returns true if an item's sort key matches this condition.
func (c *sortKeyCond) matchItem(item Item) bool {
	av, ok := item[c.attr]
	if !ok {
		return false
	}
	switch c.kind {
	case sortKeyEq:
		return attrValueEqual(av, c.val)
	case sortKeyLT:
		cmp, err := attrValueCompare(av, c.val)
		return err == nil && cmp < 0
	case sortKeyLE:
		cmp, err := attrValueCompare(av, c.val)
		return err == nil && cmp <= 0
	case sortKeyGT:
		cmp, err := attrValueCompare(av, c.val)
		return err == nil && cmp > 0
	case sortKeyGE:
		cmp, err := attrValueCompare(av, c.val)
		return err == nil && cmp >= 0
	case sortKeyBetween:
		cmpLo, err1 := attrValueCompare(av, c.lo)
		cmpHi, err2 := attrValueCompare(av, c.hi)
		return err1 == nil && err2 == nil && cmpLo >= 0 && cmpHi <= 0
	case sortKeyBeginsWith:
		return strings.HasPrefix(extractScalar(av), extractScalar(c.val))
	}
	return false
}

// compileKeyCondition parses a KeyConditionExpression.
func compileKeyCondition(
	expr string,
	names map[string]string,
	values map[string]attrValue,
) (*keyCond, error) {
	kc, err := parseKeyCondition(expr, names, values)
	if err != nil {
		return nil, exprValidationError(exprKeyCondition, err)
	}
	return kc, nil
}

func parseKeyCondition(
	expr string,
	names map[string]string,
	values map[string]attrValue,
) (*keyCond, error) {
	tokens, err := tokenise(expr)
	if err != nil {
		return nil, err
	}
	s := newTokStream(tokens)

	var terms []*sortKeyCond
	if err := collectKeyCondTerms(s, names, values, &terms); err != nil {
		return nil, err
	}
	if len(terms) == 0 {
		// Unreachable while collectKeyCondTerms either appends a term or
		// fails, which an empty or bracket-only expression does; kept so a
		// future change to it cannot turn into an index panic here.
		return nil, fmt.Errorf("KeyConditionExpression: no condition found")
	}

	if !s.at(tokEOF) {
		return nil, fmt.Errorf("KeyConditionExpression: unexpected token %q at position %d", s.peek().val, s.peek().pos)
	}

	// Two conditions on one attribute is a fault of its own, whichever
	// attribute it is — see msgOneConditionPerKey (key_schema.go). It is
	// caught here rather than in the schema check because assignKeyCondRoles
	// has only one slot for a sort-key condition, so a same-attribute pair
	// with no equality in it ("sk >= :lo AND sk <= :hi") is no longer visible
	// downstream.
	for i, term := range terms {
		for _, earlier := range terms[:i] {
			if earlier.attr == term.attr {
				return nil, errors.New(msgOneConditionPerKey)
			}
		}
	}

	// A key schema has at most two attributes, so a third distinct condition
	// is necessarily on something that is not one of them, with both keys
	// already constrained and nothing left to name as missed.
	if len(terms) > 2 {
		return nil, errors.New(msgKeyConditionNotSupported)
	}

	var second *sortKeyCond
	if len(terms) == 2 {
		second = terms[1]
	}
	return assignKeyCondRoles(terms[0], second), nil
}

// collectKeyCondTerms parses the conjunction of conditions, flattening any
// parentheses, and appends each condition it finds to terms.
//
// Brackets carry no meaning here — a KeyConditionExpression is a conjunction
// and nothing else, so there is no precedence for them to change — but they
// are common on the wire because expression builders emit them, and AWS parses
// them: planetlabs/datalake-api#21 records a real account answering the
// generated "(#n0 = :v0 AND begins_with(#n0, :v0))" with a verdict on the
// conditions inside it rather than with a syntax error. Overcast used to
// refuse the brackets themselves.
//
// BETWEEN's own AND is consumed by parseKeyCondTerm before this loop sees it,
// so a bracketed BETWEEN needs no special case.
func collectKeyCondTerms(s *tokStream, names map[string]string, values map[string]attrValue, terms *[]*sortKeyCond) error {
	for {
		if s.at(tokLParen) {
			s.next()
			if err := collectKeyCondTerms(s, names, values, terms); err != nil {
				return err
			}
			if _, err := s.expect(tokRParen); err != nil {
				return fmt.Errorf("KeyConditionExpression: %w", err)
			}
		} else {
			term, err := parseKeyCondTerm(s, names, values)
			if err != nil {
				return err
			}
			*terms = append(*terms, term)
		}
		if !s.at(tokAND) {
			return nil
		}
		s.next()
	}
}

// assignKeyCondRoles decides which of the parsed terms is the partition-key
// condition and which is the sort-key condition. The partition key must be
// compared with "=", so an equality is the only term that can hold that role.
//
// When both terms are equalities the first is provisionally taken as the
// partition key's and validateKeyConditionSchema swaps them if the schema says
// otherwise; when neither is, no term can be the partition-key condition and
// the returned keyCond carries an empty hashAttr, which that same check turns
// into AWS's "Query condition missed key schema element: <partition key>" —
// the message needs the schema's name for the key, which this parser does not
// have.
func assignKeyCondRoles(first, second *sortKeyCond) *keyCond {
	switch {
	case first.kind == sortKeyEq:
		return &keyCond{hashAttr: first.attr, hashVal: first.val, sortCond: second}
	case second != nil && second.kind == sortKeyEq:
		return &keyCond{hashAttr: second.attr, hashVal: second.val, sortCond: first}
	default:
		// Neither term can be the partition-key condition. Only first is
		// carried: the result is rejected outright, so the second term has
		// nothing left to affect.
		return &keyCond{sortCond: first}
	}
}

// parseKeyCondTerm parses one condition of a KeyConditionExpression. Either
// position may hold either role, so this parses the union of the two forms —
// an equality (which the partition key requires and the sort key also allows)
// and the sort-key-only comparisons, BETWEEN and begins_with — and leaves the
// role to assignKeyCondRoles. A term that is legal here but not in the role it
// ends up in is rejected by the schema check, with AWS's message for that
// case.
func parseKeyCondTerm(s *tokStream, names map[string]string, values map[string]attrValue) (*sortKeyCond, error) {
	// begins_with(attr, :prefix) — function form
	if s.at(tokIdent) && strings.ToLower(s.peek().val) == "begins_with" {
		s.next()
		if _, err := s.expect(tokLParen); err != nil {
			return nil, err
		}
		sortPath, err := parsePath(s, names)
		if err != nil {
			return nil, err
		}
		if !sortPath.isSimple() {
			return nil, fmt.Errorf("KeyConditionExpression: key must be a simple attribute name")
		}
		if _, err := s.expect(tokComma); err != nil {
			return nil, err
		}
		if !s.at(tokPlaceholder) {
			return nil, fmt.Errorf("KeyConditionExpression: expected :placeholder for begins_with prefix")
		}
		prefixTok := s.next()
		prefixVal, err := resolvePlaceholder(prefixTok.val, values)
		if err != nil {
			return nil, err
		}
		if _, err := s.expect(tokRParen); err != nil {
			return nil, err
		}
		return &sortKeyCond{attr: sortPath.topLevel(), kind: sortKeyBeginsWith, val: prefixVal}, nil
	}

	// attr op :val | attr BETWEEN :lo AND :hi
	sortPath, err := parsePath(s, names)
	if err != nil {
		return nil, fmt.Errorf("KeyConditionExpression: %w", err)
	}
	if !sortPath.isSimple() {
		return nil, fmt.Errorf("KeyConditionExpression: key must be a simple attribute name")
	}

	// BETWEEN
	if s.at(tokBETWEEN) {
		s.next()
		if !s.at(tokPlaceholder) {
			return nil, fmt.Errorf("KeyConditionExpression: expected :placeholder for BETWEEN low value")
		}
		loTok := s.next()
		loVal, err := resolvePlaceholder(loTok.val, values)
		if err != nil {
			return nil, err
		}
		if _, err := s.expect(tokAND); err != nil {
			return nil, fmt.Errorf("KeyConditionExpression: BETWEEN requires AND")
		}
		if !s.at(tokPlaceholder) {
			return nil, fmt.Errorf("KeyConditionExpression: expected :placeholder for BETWEEN high value")
		}
		hiTok := s.next()
		hiVal, err := resolvePlaceholder(hiTok.val, values)
		if err != nil {
			return nil, err
		}
		return &sortKeyCond{attr: sortPath.topLevel(), kind: sortKeyBetween, lo: loVal, hi: hiVal}, nil
	}

	// Comparison operator
	if !s.at(tokEq, tokLT, tokLE, tokGT, tokGE) {
		return nil, fmt.Errorf("KeyConditionExpression: expected comparison operator, got %q", s.peek().val)
	}
	opTok := s.next()
	if !s.at(tokPlaceholder) {
		return nil, fmt.Errorf("KeyConditionExpression: expected :placeholder for key condition value")
	}
	valTok := s.next()
	sortVal, err := resolvePlaceholder(valTok.val, values)
	if err != nil {
		return nil, err
	}

	var kind sortKeyCondKind
	//exhaustive:ignore
	switch opTok.kind {
	case tokEq:
		kind = sortKeyEq
	case tokLT:
		kind = sortKeyLT
	case tokLE:
		kind = sortKeyLE
	case tokGT:
		kind = sortKeyGT
	case tokGE:
		kind = sortKeyGE
	default:
		return nil, fmt.Errorf("KeyConditionExpression: unsupported key condition operator %q", opTok.val)
	}
	return &sortKeyCond{attr: sortPath.topLevel(), kind: kind, val: sortVal}, nil
}
