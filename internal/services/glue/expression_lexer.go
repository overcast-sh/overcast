package glue

// expression_lexer.go — tokenises a GetPartitions Expression for the parser
// in expression.go.

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

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
			num, n, err := lexNumber(s[i:])
			if err != nil {
				return nil, err
			}
			toks = append(toks, token{tokNumber, num})
			i += n
		case isIdentStart(s[i:]):
			tok, n := lexWord(s[i:])
			toks = append(toks, tok)
			i += n
		default:
			return nil, fmt.Errorf("unexpected character %q", c)
		}
	}
	return append(toks, token{tokEOF, ""}), nil
}

// lexNumber reads the numeric literal starting at s[0]. It returns the
// literal and the number of bytes consumed.
func lexNumber(s string) (string, int, error) {
	j := 1
	for j < len(s) && isNumberByte(s[j]) {
		// A sign belongs to the number only straight after an exponent.
		if (s[j] == '+' || s[j] == '-') && s[j-1] != 'e' && s[j-1] != 'E' {
			break
		}
		j++
	}
	num := s[:j]
	if _, ok := parseDecimal(num); !ok {
		return "", 0, fmt.Errorf("invalid number %q", num)
	}
	return num, j, nil
}

// lexWord reads the unquoted identifier or keyword starting at s[0]. It
// returns the token and the number of bytes consumed.
func lexWord(s string) (token, int) {
	j := 0
	for j < len(s) {
		r, size := utf8.DecodeRuneInString(s[j:])
		if r != '_' && r != '$' && !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			break
		}
		j += size
	}
	word := s[:j]
	if up := strings.ToUpper(word); keywords[up] {
		return token{tokKeyword, up}, j
	}
	return token{tokIdent, word}, j
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
