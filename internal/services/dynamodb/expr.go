package dynamodb

// expr.go implements a DynamoDB expression tokeniser (lexer) and shared value
// helpers.
//
// DynamoDB expressions share a common tokenisation layer:
//   - identifiers (attribute names, function names)
//   - #alias tokens (ExpressionAttributeNames references)
//   - :placeholder tokens (ExpressionAttributeValues references)
//   - operators (=, <>, <, <=, >, >=, comma, dot, open/close paren/bracket)
//   - keywords (AND, OR, NOT, BETWEEN, IN, SET, REMOVE, ADD, DELETE)
//   - numeric literals (list index inside brackets)
//
// The lexer is used by all four expression compilers (filter, update,
// projection, key-condition).

import (
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
)

// tokenKind classifies a lexical token.
type tokenKind int

const (
	tokEOF         tokenKind = iota
	tokIdent                 // attribute name or function name
	tokAlias                 // #name
	tokPlaceholder           // :value
	tokNumber                // integer literal (for list indices)
	tokComma                 // ,
	tokDot                   // .
	tokLParen                // (
	tokRParen                // )
	tokLBracket              // [
	tokRBracket              // ]
	tokEq                    // =
	tokNeq                   // <>
	tokLT                    // <
	tokLE                    // <=
	tokGT                    // >
	tokGE                    // >=
	tokPlus                  // +
	tokMinus                 // -

	// Keywords (case-insensitive, detected during lexing).
	tokAND
	tokOR
	tokNOT
	tokBETWEEN
	tokIN
	tokSET
	tokREMOVE
	tokADD
	tokDELETE
)

// token is a lexical token with its position in the source string.
type token struct {
	kind tokenKind
	val  string // raw text
	pos  int    // byte offset in source
}

func (t token) String() string { return t.val }

// keywords maps upper-case words to their token kinds.
var exprKeywords = map[string]tokenKind{
	"AND":     tokAND,
	"OR":      tokOR,
	"NOT":     tokNOT,
	"BETWEEN": tokBETWEEN,
	"IN":      tokIN,
	"SET":     tokSET,
	"REMOVE":  tokREMOVE,
	"ADD":     tokADD,
	"DELETE":  tokDELETE,
}

// tokenise converts a DynamoDB expression string into a slice of tokens.
func tokenise(src string) ([]token, error) {
	var tokens []token
	i := 0
	for i < len(src) {
		// Skip whitespace.
		if src[i] == ' ' || src[i] == '\t' || src[i] == '\n' || src[i] == '\r' {
			i++
			continue
		}

		switch src[i] {
		case ',':
			tokens = append(tokens, token{tokComma, ",", i})
			i++
		case '.':
			tokens = append(tokens, token{tokDot, ".", i})
			i++
		case '(':
			tokens = append(tokens, token{tokLParen, "(", i})
			i++
		case ')':
			tokens = append(tokens, token{tokRParen, ")", i})
			i++
		case '[':
			tokens = append(tokens, token{tokLBracket, "[", i})
			i++
		case ']':
			tokens = append(tokens, token{tokRBracket, "]", i})
			i++
		case '+':
			tokens = append(tokens, token{tokPlus, "+", i})
			i++
		case '-':
			tokens = append(tokens, token{tokMinus, "-", i})
			i++
		case '=':
			tokens = append(tokens, token{tokEq, "=", i})
			i++
		case '<':
			if i+1 < len(src) && src[i+1] == '>' {
				tokens = append(tokens, token{tokNeq, "<>", i})
				i += 2
			} else if i+1 < len(src) && src[i+1] == '=' {
				tokens = append(tokens, token{tokLE, "<=", i})
				i += 2
			} else {
				tokens = append(tokens, token{tokLT, "<", i})
				i++
			}
		case '>':
			if i+1 < len(src) && src[i+1] == '=' {
				tokens = append(tokens, token{tokGE, ">=", i})
				i += 2
			} else {
				tokens = append(tokens, token{tokGT, ">", i})
				i++
			}
		case '#':
			// #alias — read until non-identifier char.
			start := i
			i++ // skip '#'
			for i < len(src) && isIdentChar(src[i]) {
				i++
			}
			if i == start+1 {
				return nil, fmt.Errorf("empty alias at position %d", start)
			}
			tokens = append(tokens, token{tokAlias, src[start:i], start})
		case ':':
			// :placeholder — read until non-identifier char.
			start := i
			i++ // skip ':'
			for i < len(src) && isIdentChar(src[i]) {
				i++
			}
			if i == start+1 {
				return nil, fmt.Errorf("empty placeholder at position %d", start)
			}
			tokens = append(tokens, token{tokPlaceholder, src[start:i], start})
		default:
			if isDigit(src[i]) {
				start := i
				for i < len(src) && isDigit(src[i]) {
					i++
				}
				tokens = append(tokens, token{tokNumber, src[start:i], start})
			} else if isIdentStart(src[i]) {
				start := i
				for i < len(src) && isIdentChar(src[i]) {
					i++
				}
				word := src[start:i]
				if kw, ok := exprKeywords[strings.ToUpper(word)]; ok {
					tokens = append(tokens, token{kw, word, start})
				} else {
					tokens = append(tokens, token{tokIdent, word, start})
				}
			} else {
				return nil, fmt.Errorf("unexpected character %q at position %d", src[i], i)
			}
		}
	}
	tokens = append(tokens, token{tokEOF, "", len(src)})
	return tokens, nil
}

func isDigit(c byte) bool      { return c >= '0' && c <= '9' }
func isIdentStart(c byte) bool { return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c == '_' }
func isIdentChar(c byte) bool  { return isIdentStart(c) || isDigit(c) }

// tokStream is a position-tracked view over a token slice.
type tokStream struct {
	tokens []token
	pos    int
}

func newTokStream(tokens []token) *tokStream {
	return &tokStream{tokens: tokens}
}

// peek returns the current token without advancing.
func (s *tokStream) peek() token {
	if s.pos >= len(s.tokens) {
		return token{kind: tokEOF}
	}
	return s.tokens[s.pos]
}

// next returns the current token and advances.
func (s *tokStream) next() token {
	t := s.peek()
	if s.pos < len(s.tokens) {
		s.pos++
	}
	return t
}

// expect consumes a token of the given kind or returns an error.
func (s *tokStream) expect(kind tokenKind) (token, error) {
	t := s.next()
	if t.kind != kind {
		return t, fmt.Errorf("expected %s, got %q at position %d", tokenKindName(kind), t.val, t.pos)
	}
	return t, nil
}

// at returns true if the current token is one of the given kinds.
func (s *tokStream) at(kinds ...tokenKind) bool {
	k := s.peek().kind
	for _, want := range kinds {
		if k == want {
			return true
		}
	}
	return false
}

func tokenKindName(k tokenKind) string {
	switch k {
	case tokEOF:
		return "EOF"
	case tokIdent:
		return "identifier"
	case tokAlias:
		return "#alias"
	case tokPlaceholder:
		return ":placeholder"
	case tokNumber:
		return "number"
	case tokComma:
		return "','"
	case tokDot:
		return "'.'"
	case tokLParen:
		return "'('"
	case tokRParen:
		return "')'"
	case tokLBracket:
		return "'['"
	case tokRBracket:
		return "']'"
	case tokEq:
		return "'='"
	case tokNeq:
		return "'<>'"
	case tokLT:
		return "'<'"
	case tokLE:
		return "'<='"
	case tokGT:
		return "'>'"
	case tokGE:
		return "'>='"
	case tokPlus:
		return "'+'"
	case tokMinus:
		return "'-'"
	case tokAND:
		return "AND"
	case tokOR:
		return "OR"
	case tokNOT:
		return "NOT"
	case tokBETWEEN:
		return "BETWEEN"
	case tokIN:
		return "IN"
	case tokSET:
		return "SET"
	case tokREMOVE:
		return "REMOVE"
	case tokADD:
		return "ADD"
	case tokDELETE:
		return "DELETE"
	default:
		return fmt.Sprintf("token(%d)", k)
	}
}

// ---------------------------------------------------------------------------
// Shared: resolve helpers
// ---------------------------------------------------------------------------

// resolveAlias resolves a #name alias using the ExpressionAttributeNames map.
func resolveAlias(alias string, names map[string]string) (string, error) {
	if resolved, ok := names[alias]; ok {
		return resolved, nil
	}
	return "", fmt.Errorf("ExpressionAttributeNames does not contain key: %s", alias)
}

// resolvePlaceholder resolves a :placeholder using ExpressionAttributeValues.
func resolvePlaceholder(placeholder string, values map[string]attrValue) (attrValue, error) {
	if val, ok := values[placeholder]; ok {
		return val, nil
	}
	return nil, fmt.Errorf("ExpressionAttributeValues does not contain key: %s", placeholder)
}

// ---------------------------------------------------------------------------
// Shared: attribute value comparison helpers
// ---------------------------------------------------------------------------

// attrValueEqual reports deep equality of two DynamoDB attribute values.
func attrValueEqual(a, b attrValue) bool {
	if len(a) != len(b) {
		return false
	}
	for k, av := range a {
		bv, ok := b[k]
		if !ok {
			return false
		}
		if fmt.Sprintf("%v", av) != fmt.Sprintf("%v", bv) {
			return false
		}
	}
	return true
}

// attrValueCompare compares two attribute values for ordering.
// Returns -1, 0, or 1. Numeric values are compared numerically;
// string values are compared lexicographically; binary values by bytes.
func attrValueCompare(a, b attrValue) (int, error) {
	aType := attrType(a)
	bType := attrType(b)
	if aType != bType {
		return 0, fmt.Errorf("comparison between %s and %s is not supported", aType, bType)
	}
	switch aType {
	case "N":
		an, err := strconv.ParseFloat(extractScalar(a), 64)
		if err != nil {
			return 0, fmt.Errorf("invalid number: %s", extractScalar(a))
		}
		bn, err := strconv.ParseFloat(extractScalar(b), 64)
		if err != nil {
			return 0, fmt.Errorf("invalid number: %s", extractScalar(b))
		}
		switch {
		case an < bn:
			return -1, nil
		case an > bn:
			return 1, nil
		default:
			return 0, nil
		}
	case "S":
		as := extractScalar(a)
		bs := extractScalar(b)
		switch {
		case as < bs:
			return -1, nil
		case as > bs:
			return 1, nil
		default:
			return 0, nil
		}
	case "B":
		as := extractScalar(a)
		bs := extractScalar(b)
		switch {
		case as < bs:
			return -1, nil
		case as > bs:
			return 1, nil
		default:
			return 0, nil
		}
	default:
		return 0, fmt.Errorf("comparison not supported for type %s", aType)
	}
}

// attrType returns the DynamoDB type code of an attribute value ("S", "N", "B",
// "BOOL", "NULL", "L", "M", "SS", "NS", "BS").
func attrType(v attrValue) string {
	for k := range v {
		return k
	}
	return ""
}

// extractScalar returns the scalar string from an S, N, or B attribute value.
func extractScalar(v attrValue) string {
	for _, val := range v {
		if s, ok := scalarString(val); ok {
			return s
		}
	}
	return ""
}

// scalarString returns the wire-string form of a decoded scalar
// AttributeValue payload (an S, N, or B member's raw value) — the single
// place extractScalar and extractKeyValue (store.go) both go through so a
// Binary attribute resolves to the identical string regardless of which
// protocol decoded the request (issue #1999).
//
// JSON has no binary wire type, so DynamoDB JSON always carries a "B" value
// as base64 text, which encoding/json decodes into a Go string like any
// other JSON string. Smithy RPC v2 CBOR has a genuine byte-string wire type,
// and the CBOR codec (internal/protocol/codec/cbor.go) correctly leaves a
// decoded "B" payload as a raw []byte rather than base64-encoding it, since
// that is the real CBOR wire form a client sent. Before this, extractScalar
// and extractKeyValue recognised only the `string` case: a CBOR-decoded
// []byte read as an empty value, so key_schema.go's empty-key-value rule
// (issue #1707, rule 3) rejected every PutItem/GetItem/Query/DeleteItem/
// BatchGetItem/BatchWriteItem call against a Binary key sent over CBOR,
// although the byte-identical request succeeded over JSON.
//
// A []byte payload is base64-encoded here to the same text an equivalent
// JSON request would have decoded to, so the two protocols agree on the
// stored key string (encodeStorageKeyComponent, key_order.go) and on every
// scalar comparison (equality, BETWEEN, begins_with, sort order) for the
// identical underlying bytes, whichever protocol wrote or reads the item.
func scalarString(raw any) (string, bool) {
	switch v := raw.(type) {
	case string:
		return v, true
	case []byte:
		return base64.StdEncoding.EncodeToString(v), true
	default:
		return "", false
	}
}

// extractList returns the list elements from an L attribute value.
func extractList(v attrValue) []any {
	raw, ok := v["L"]
	if !ok {
		return nil
	}
	list, ok := raw.([]any)
	if !ok {
		return nil
	}
	return list
}

// extractMap returns the map from an M attribute value.
func extractMap(v attrValue) map[string]any {
	raw, ok := v["M"]
	if !ok {
		return nil
	}
	m, ok := raw.(map[string]any)
	if !ok {
		return nil
	}
	return m
}

// attrValueSize returns the "size" of an attribute value as DynamoDB defines it.
func attrValueSize(v attrValue) (int, error) {
	switch attrType(v) {
	case "S":
		return len(extractScalar(v)), nil
	case "B":
		return len(extractScalar(v)), nil
	case "SS", "NS", "BS":
		for _, val := range v {
			if arr, ok := val.([]any); ok {
				return len(arr), nil
			}
		}
		return 0, nil
	case "L":
		return len(extractList(v)), nil
	case "M":
		return len(extractMap(v)), nil
	default:
		return 0, fmt.Errorf("size() is not supported for type %s", attrType(v))
	}
}

// anyToAttrValue converts a raw any (from JSON unmarshalling) to an attrValue.
func anyToAttrValue(raw any) (attrValue, bool) {
	m, ok := raw.(map[string]any)
	if !ok {
		return nil, false
	}
	return attrValue(m), true
}

// numberAttrValue creates a Number attribute value.
func numberAttrValue(n float64) attrValue {
	s := strconv.FormatFloat(n, 'f', -1, 64)
	return attrValue{"N": s}
}

// setContains checks if a set (SS, NS, or BS) contains a given scalar value.
func setContains(set attrValue, val attrValue) bool {
	valScalar := extractScalar(val)
	for _, raw := range set {
		arr, ok := raw.([]any)
		if !ok {
			continue
		}
		for _, elem := range arr {
			if s, ok := elem.(string); ok && s == valScalar {
				return true
			}
		}
	}
	return false
}

// listContains checks if a list contains a given attribute value.
func listContains(list attrValue, val attrValue) bool {
	elems := extractList(list)
	for _, elem := range elems {
		av, ok := anyToAttrValue(elem)
		if ok && attrValueEqual(av, val) {
			return true
		}
	}
	return false
}
