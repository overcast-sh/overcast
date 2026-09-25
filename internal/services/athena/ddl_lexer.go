package athena

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"
)

// ddl_lexer.go — tokens of Athena's SQL, in either of its two dialects.
//
// Hive DDL quotes identifiers with backticks and strings with either quote,
// and reads backslash escapes in a string. Trino's SQL quotes identifiers
// with double quotes and has no backslash escapes, so 'C:\' is a whole
// string there. Comments are skipped. Each token remembers where it came
// from, so a column type or a CTAS query can be carried over as written.

// sqlDialect is which of Athena's two SQL dialects a statement is lexed in.
type sqlDialect int

const (
	dialectHive sqlDialect = iota
	dialectTrino
)

type tokenKind int

const (
	tokWord   tokenKind = iota // a keyword or a bare identifier
	tokIdent                   // a `quoted` identifier
	tokString                  // a '...' or "..." literal, unquoted
	tokNumber                  // an unsigned integer
	tokSymbol                  // one character of punctuation
	tokEOF
)

type token struct {
	kind       tokenKind
	text       string
	start, end int // byte offsets in the statement
}

// is reports whether t is the keyword kw, in any case.
func (t token) is(kw string) bool { return t.kind == tokWord && strings.EqualFold(t.text, kw) }

// isSymbol reports whether t is the punctuation s.
func (t token) isSymbol(s string) bool { return t.kind == tokSymbol && t.text == s }

// lexSQL splits a statement into tokens, ending with tokEOF.
func lexSQL(src string, dialect sqlDialect) ([]token, error) {
	var toks []token
	for i := 0; i < len(src); {
		c := src[i]
		switch {
		case unicode.IsSpace(rune(c)):
			i++
		case strings.HasPrefix(src[i:], "--"):
			i = skipTo(src, i, "\n")
		case strings.HasPrefix(src[i:], "/*"):
			i = skipTo(src, i+2, "*/")
		case c == '\'' || c == '"' || c == '`':
			text, end, err := lexQuoted(src, i, dialect == dialectHive && c != '`')
			if err != nil {
				return nil, err
			}
			kind := tokString
			if c == '`' || c == '"' && dialect == dialectTrino {
				kind = tokIdent
			}
			toks = append(toks, token{kind: kind, text: text, start: i, end: end})
			i = end
		case isWordByte(c):
			end := i
			for end < len(src) && isWordByte(src[end]) {
				end++
			}
			kind := tokWord
			if strings.Trim(src[i:end], "0123456789") == "" {
				kind = tokNumber
			}
			toks = append(toks, token{kind: kind, text: src[i:end], start: i, end: end})
			i = end
		default:
			toks = append(toks, token{kind: tokSymbol, text: string(c), start: i, end: i + 1})
			i++
		}
	}
	return append(toks, token{kind: tokEOF, start: len(src), end: len(src)}), nil
}

func isWordByte(c byte) bool {
	return c == '_' || c == '$' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= 0x80
}

// skipTo returns the offset just past the next end at or after i, or the end
// of src when there is none.
func skipTo(src string, i int, end string) int {
	if j := strings.Index(src[i:], end); j >= 0 {
		return i + j + len(end)
	}
	return len(src)
}

// lexQuoted reads the quoted token at src[i], where a doubled quote stands
// for itself, and returns its text and the offset just past it. With escapes,
// a backslash escapes the next character as Hive reads it: '\t' is a tab and
// '\001' the byte 1, which is how a delimiter is written.
func lexQuoted(src string, i int, escapes bool) (string, int, error) {
	q := src[i]
	var b strings.Builder
	for j := i + 1; j < len(src); j++ {
		if escapes && src[j] == '\\' && j+1 < len(src) {
			j += writeEscape(&b, src[j+1:])
			continue
		}
		if src[j] != q {
			b.WriteByte(src[j])
			continue
		}
		if j+1 < len(src) && src[j+1] == q {
			b.WriteByte(q)
			j++
			continue
		}
		return b.String(), j + 1, nil
	}
	return "", 0, fmt.Errorf("unterminated %c at offset %d", q, i)
}

// hiveEscapes are the single-character escapes Hive's unescapeSQLString
// reads in a string; any other escaped character stands for itself.
var hiveEscapes = map[byte]string{
	'0': "\x00", 'b': "\b", 'n': "\n", 'r': "\r", 't': "\t", 'Z': "\x1a", '%': "\\%", '_': "\\_",
}

// writeEscape writes what the escape at the start of rest stands for, and
// returns how many bytes of rest it used: \uXXXX is a code point, three
// octal digits from \000 to \177 a byte, and the rest one character each.
func writeEscape(b *strings.Builder, rest string) int {
	if len(rest) >= 5 && rest[0] == 'u' {
		if v, err := strconv.ParseUint(rest[1:5], 16, 16); err == nil {
			b.WriteRune(rune(v))
			return 5
		}
	}
	if len(rest) >= 3 && rest[0] >= '0' && rest[0] <= '1' && isOctal(rest[1]) && isOctal(rest[2]) {
		v, _ := strconv.ParseUint(rest[:3], 8, 8)
		b.WriteByte(byte(v))
		return 3
	}
	if e, ok := hiveEscapes[rest[0]]; ok {
		b.WriteString(e)
	} else {
		b.WriteByte(rest[0])
	}
	return 1
}

func isOctal(c byte) bool { return c >= '0' && c <= '7' }
