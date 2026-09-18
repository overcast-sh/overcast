package stepfunctions

import (
	"bytes"
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"math"
	"math/rand/v2"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
)

// The Amazon States Language intrinsic functions, evaluated inside a payload
// template's `.$` fields. Argument rules and limits follow
// https://docs.aws.amazon.com/step-functions/latest/dg/intrinsic-functions.html.
// Arguments may be string/number/boolean/null literals, paths (`$.x`, `$$.x`,
// `$var`) or nested intrinsic calls, up to ten calls deep.

// intrinsicError is an intrinsic function that could not be evaluated: a bad
// argument count or type, a value out of range, malformed syntax. AWS reports
// these as States.IntrinsicFailure, distinct from a path that did not resolve.
type intrinsicError struct{ msg string }

func (e *intrinsicError) Error() string { return e.msg }

func intrinsicErrorf(format string, args ...any) error {
	return &intrinsicError{msg: fmt.Sprintf(format, args...)}
}

// templateErrorName is the ASL error name for a failure while rendering a
// payload template or evaluating a template expression.
func templateErrorName(err error) string {
	var ie *intrinsicError
	if errors.As(err, &ie) {
		return "States.IntrinsicFailure"
	}
	return "States.ParameterPathFailure"
}

const (
	maxIntrinsicNesting = 10
	maxIntrinsicChars   = 10000
	maxArrayRangeItems  = 1000
)

// ─── Syntax ───────────────────────────────────────────────────────────────────

type intrinsicCall struct {
	name string
	args []intrinsicArg
}

// intrinsicArg is exactly one of: a nested call, a path, or a literal.
type intrinsicArg struct {
	call    *intrinsicCall
	path    string
	literal any
	// rawString is a string literal's source text between the quotes, escapes
	// intact. States.Format needs it to tell `{}` from `\{\}`.
	rawString string
	isString  bool
}

type intrinsicParser struct {
	src string
	pos int
}

func parseIntrinsic(expr string) (*intrinsicCall, error) {
	p := &intrinsicParser{src: strings.TrimSpace(expr)}
	call, err := p.parseCall(1)
	if err != nil {
		return nil, err
	}
	p.skipSpace()
	if p.pos != len(p.src) {
		return nil, intrinsicErrorf("intrinsic %q has unexpected text after the call: %q", expr, p.src[p.pos:])
	}
	return call, nil
}

func (p *intrinsicParser) skipSpace() {
	for p.pos < len(p.src) && strings.IndexByte(" \t\r\n", p.src[p.pos]) >= 0 {
		p.pos++
	}
}

func (p *intrinsicParser) parseCall(depth int) (*intrinsicCall, error) {
	if depth > maxIntrinsicNesting {
		return nil, intrinsicErrorf("intrinsic functions can be nested at most %d deep", maxIntrinsicNesting)
	}
	if !strings.HasPrefix(p.src[p.pos:], "States.") {
		return nil, intrinsicErrorf("intrinsic %q is malformed — expected States.Function(args)", p.src)
	}
	start := p.pos
	p.pos += len("States.")
	for p.pos < len(p.src) && isIdentChar(p.src[p.pos]) {
		p.pos++
	}
	call := &intrinsicCall{name: p.src[start:p.pos]}
	p.skipSpace()
	if p.pos >= len(p.src) || p.src[p.pos] != '(' {
		return nil, intrinsicErrorf("intrinsic %q is malformed — expected ( after %s", p.src, call.name)
	}
	p.pos++
	p.skipSpace()
	if p.pos < len(p.src) && p.src[p.pos] == ')' {
		p.pos++
		return call, nil
	}
	for {
		arg, err := p.parseArg(depth)
		if err != nil {
			return nil, err
		}
		call.args = append(call.args, arg)
		p.skipSpace()
		if p.pos >= len(p.src) {
			return nil, intrinsicErrorf("intrinsic %q is missing a closing )", p.src)
		}
		switch p.src[p.pos] {
		case ',':
			p.pos++
		case ')':
			p.pos++
			return call, nil
		default:
			return nil, intrinsicErrorf("intrinsic %q is malformed at %q", p.src, p.src[p.pos:])
		}
	}
}

func (p *intrinsicParser) parseArg(depth int) (intrinsicArg, error) {
	p.skipSpace()
	rest := p.src[p.pos:]
	switch {
	case rest == "":
		return intrinsicArg{}, intrinsicErrorf("intrinsic %q is missing an argument", p.src)
	case strings.HasPrefix(rest, "States."):
		call, err := p.parseCall(depth + 1)
		return intrinsicArg{call: call}, err
	case rest[0] == '\'':
		return p.parseStringLiteral()
	case rest[0] == '$':
		return intrinsicArg{path: p.scanPath()}, nil
	}
	start := p.pos
	for p.pos < len(p.src) && strings.IndexByte(",) \t\r\n", p.src[p.pos]) < 0 {
		p.pos++
	}
	token := p.src[start:p.pos]
	switch token {
	case "true":
		return intrinsicArg{literal: true}, nil
	case "false":
		return intrinsicArg{literal: false}, nil
	case "null":
		return intrinsicArg{literal: nil}, nil
	}
	if n, err := strconv.ParseFloat(token, 64); err == nil && !math.IsInf(n, 0) && !math.IsNaN(n) {
		return intrinsicArg{literal: n}, nil
	}
	return intrinsicArg{}, intrinsicErrorf("intrinsic argument %q is not a literal, a path or a nested intrinsic", token)
}

// parseStringLiteral reads a '...' literal. The only escapes are \', \{, \}
// and \\; any other backslash is an "open escape", which AWS rejects.
func (p *intrinsicParser) parseStringLiteral() (intrinsicArg, error) {
	p.pos++ // opening quote
	start := p.pos
	var b strings.Builder
	for p.pos < len(p.src) {
		c := p.src[p.pos]
		switch c {
		case '\\':
			if p.pos+1 >= len(p.src) || strings.IndexByte(`'{}\`, p.src[p.pos+1]) < 0 {
				return intrinsicArg{}, intrinsicErrorf("intrinsic string literal in %q has an open escape \\", p.src)
			}
			b.WriteByte(p.src[p.pos+1])
			p.pos += 2
		case '\'':
			raw := p.src[start:p.pos]
			p.pos++
			return intrinsicArg{literal: b.String(), rawString: raw, isString: true}, nil
		default:
			b.WriteByte(c)
			p.pos++
		}
	}
	return intrinsicArg{}, intrinsicErrorf("intrinsic %q has an unterminated string literal", p.src)
}

// scanPath consumes a path argument up to the `,` or `)` that ends it,
// skipping over brackets, filter parentheses and quoted names.
func (p *intrinsicParser) scanPath() string {
	start := p.pos
	depth := 0
	var quote byte
	for ; p.pos < len(p.src); p.pos++ {
		c := p.src[p.pos]
		if quote != 0 {
			if c == '\\' {
				p.pos++
			} else if c == quote {
				quote = 0
			}
			continue
		}
		switch c {
		case '\'', '"':
			quote = c
		case '[', '(':
			depth++
		case ']':
			depth--
		case ')':
			if depth == 0 {
				return strings.TrimSpace(p.src[start:p.pos])
			}
			depth--
		case ',':
			if depth == 0 {
				return strings.TrimSpace(p.src[start:p.pos])
			}
		}
	}
	return strings.TrimSpace(p.src[start:])
}

// ─── Evaluation ───────────────────────────────────────────────────────────────

func evaluateIntrinsic(expr string, doc any, ctxObj map[string]any) (any, error) {
	call, err := parseIntrinsic(expr)
	if err != nil {
		return nil, err
	}
	return call.evaluate(doc, ctxObj)
}

type intrinsicFunc func(name string, args []any, raw []intrinsicArg) (any, error)

var intrinsicFuncs = map[string]intrinsicFunc{
	"States.Format":         intrinsicFormat,
	"States.StringToJson":   intrinsicStringToJSON,
	"States.JsonToString":   intrinsicJSONToString,
	"States.Array":          func(_ string, args []any, _ []intrinsicArg) (any, error) { return append([]any{}, args...), nil },
	"States.ArrayPartition": intrinsicArrayPartition,
	"States.ArrayContains":  intrinsicArrayContains,
	"States.ArrayRange":     intrinsicArrayRange,
	"States.ArrayGetItem":   intrinsicArrayGetItem,
	"States.ArrayLength":    intrinsicArrayLength,
	"States.ArrayUnique":    intrinsicArrayUnique,
	"States.Base64Encode":   intrinsicBase64Encode,
	"States.Base64Decode":   intrinsicBase64Decode,
	"States.Hash":           intrinsicHash,
	"States.JsonMerge":      intrinsicJSONMerge,
	"States.MathRandom":     intrinsicMathRandom,
	"States.MathAdd":        intrinsicMathAdd,
	"States.StringSplit":    intrinsicStringSplit,
	"States.UUID":           intrinsicUUID,
}

func (c *intrinsicCall) evaluate(doc any, ctxObj map[string]any) (any, error) {
	fn, ok := intrinsicFuncs[c.name]
	if !ok {
		return nil, intrinsicErrorf("%s is not an intrinsic function", c.name)
	}
	args := make([]any, len(c.args))
	for i, arg := range c.args {
		switch {
		case arg.call != nil:
			value, err := arg.call.evaluate(doc, ctxObj)
			if err != nil {
				return nil, err
			}
			args[i] = value
		case arg.path != "":
			value, err := selectPath(doc, ctxObj, arg.path)
			if err != nil {
				return nil, err
			}
			args[i] = value
		default:
			args[i] = arg.literal
		}
	}
	return fn(c.name, args, c.args)
}

func wantArgs(name string, args []any, n int) error {
	if len(args) != n {
		return intrinsicErrorf("%s expects %d argument(s), got %d", name, n, len(args))
	}
	return nil
}

func arrayArg(name string, v any, position string) ([]any, error) {
	arr, ok := v.([]any)
	if !ok {
		return nil, intrinsicErrorf("%s expects an array as its %s argument, got %s", name, position, jsonTypeName(v))
	}
	return arr, nil
}

func stringArg(name string, v any, position string) (string, error) {
	s, ok := v.(string)
	if !ok {
		return "", intrinsicErrorf("%s expects a string as its %s argument, got %s", name, position, jsonTypeName(v))
	}
	return s, nil
}

// integerArg reads a numeric argument, rounding a non-integer to the nearest
// integer as AWS documents.
func integerArg(name string, v any, position string) (int64, error) {
	n, ok := toNumber(v)
	if !ok {
		return 0, intrinsicErrorf("%s expects a number as its %s argument, got %s", name, position, jsonTypeName(v))
	}
	rounded := math.Round(n)
	if math.IsNaN(rounded) || rounded > math.MaxInt64/2 || rounded < math.MinInt64/2 {
		return 0, intrinsicErrorf("%s: %s argument %v is out of range", name, position, n)
	}
	return int64(rounded), nil
}

func limitedString(name string, s string) error {
	if utf8.RuneCountInString(s) > maxIntrinsicChars {
		return intrinsicErrorf("%s accepts at most %d characters, got %d", name, maxIntrinsicChars, utf8.RuneCountInString(s))
	}
	return nil
}

func jsonTypeName(v any) string {
	switch v.(type) {
	case nil:
		return "null"
	case string:
		return "a string"
	case bool:
		return "a boolean"
	case []any:
		return "an array"
	case map[string]any:
		return "an object"
	}
	if _, ok := toNumber(v); ok {
		return "a number"
	}
	return fmt.Sprintf("%T", v)
}

// ─── Functions ────────────────────────────────────────────────────────────────

func intrinsicFormat(name string, args []any, raw []intrinsicArg) (any, error) {
	if len(args) == 0 {
		return nil, intrinsicErrorf("%s expects at least 1 argument", name)
	}
	template, err := stringArg(name, args[0], "first")
	if err != nil {
		return nil, err
	}
	// A literal template keeps its escapes so `\{\}` stays literal text; a
	// template read from a path has no escapes to honour.
	literal := raw[0].isString
	if literal {
		template = raw[0].rawString
	}
	values := args[1:]
	var b strings.Builder
	used := 0
	for i := 0; i < len(template); i++ {
		if literal && template[i] == '\\' && i+1 < len(template) {
			b.WriteByte(template[i+1])
			i++
			continue
		}
		if strings.HasPrefix(template[i:], "{}") {
			if used >= len(values) {
				return nil, intrinsicErrorf("%s has more {} placeholders than arguments", name)
			}
			b.WriteString(formatIntrinsicValue(values[used]))
			used++
			i++
			continue
		}
		b.WriteByte(template[i])
	}
	if used != len(values) {
		return nil, intrinsicErrorf("%s has %d argument(s) for %d placeholder(s)", name, len(values), used)
	}
	return b.String(), nil
}

func formatIntrinsicValue(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case nil:
		return "null"
	default:
		return marshalNoHTMLEscape(x)
	}
}

// marshalNoHTMLEscape encodes v as compact JSON without escaping <, > and &,
// which AWS leaves alone.
func marshalNoHTMLEscape(v any) string {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return fmt.Sprint(v)
	}
	return strings.TrimSuffix(buf.String(), "\n")
}

func intrinsicStringToJSON(name string, args []any, _ []intrinsicArg) (any, error) {
	if err := wantArgs(name, args, 1); err != nil {
		return nil, err
	}
	s, err := stringArg(name, args[0], "first")
	if err != nil {
		return nil, err
	}
	var decoded any
	if err := json.Unmarshal([]byte(s), &decoded); err != nil {
		return nil, intrinsicErrorf("%s: argument is not valid JSON: %v", name, err)
	}
	return decoded, nil
}

func intrinsicJSONToString(name string, args []any, _ []intrinsicArg) (any, error) {
	if err := wantArgs(name, args, 1); err != nil {
		return nil, err
	}
	return marshalNoHTMLEscape(args[0]), nil
}

func intrinsicArrayPartition(name string, args []any, _ []intrinsicArg) (any, error) {
	if err := wantArgs(name, args, 2); err != nil {
		return nil, err
	}
	arr, err := arrayArg(name, args[0], "first")
	if err != nil {
		return nil, err
	}
	size, err := integerArg(name, args[1], "second")
	if err != nil {
		return nil, err
	}
	if size <= 0 {
		return nil, intrinsicErrorf("%s expects a positive chunk size, got %d", name, size)
	}
	out := []any{}
	for start := 0; start < len(arr); start += int(size) {
		end := min(start+int(size), len(arr))
		out = append(out, append([]any{}, arr[start:end]...))
	}
	return out, nil
}

func intrinsicArrayContains(name string, args []any, _ []intrinsicArg) (any, error) {
	if err := wantArgs(name, args, 2); err != nil {
		return nil, err
	}
	arr, err := arrayArg(name, args[0], "first")
	if err != nil {
		return nil, err
	}
	for _, item := range arr {
		if jsonEqual(item, args[1]) {
			return true, nil
		}
	}
	return false, nil
}

func intrinsicArrayRange(name string, args []any, _ []intrinsicArg) (any, error) {
	if err := wantArgs(name, args, 3); err != nil {
		return nil, err
	}
	var bounds [3]int64
	for i, position := range []string{"first", "second", "third"} {
		n, err := integerArg(name, args[i], position)
		if err != nil {
			return nil, err
		}
		bounds[i] = n
	}
	start, end, step := bounds[0], bounds[1], bounds[2]
	if step == 0 {
		return nil, intrinsicErrorf("%s expects a non-zero step", name)
	}
	count := int64(0)
	if (step > 0 && end >= start) || (step < 0 && end <= start) {
		count = (end-start)/step + 1
	}
	if count > maxArrayRangeItems {
		return nil, intrinsicErrorf("%s would produce %d items; the maximum is %d", name, count, maxArrayRangeItems)
	}
	out := make([]any, 0, count)
	for i := int64(0); i < count; i++ {
		out = append(out, float64(start+i*step))
	}
	return out, nil
}

func intrinsicArrayGetItem(name string, args []any, _ []intrinsicArg) (any, error) {
	if err := wantArgs(name, args, 2); err != nil {
		return nil, err
	}
	arr, err := arrayArg(name, args[0], "first")
	if err != nil {
		return nil, err
	}
	idx, err := integerArg(name, args[1], "second")
	if err != nil {
		return nil, err
	}
	if idx < 0 || idx >= int64(len(arr)) {
		return nil, intrinsicErrorf("%s: index %d is out of range for an array of length %d", name, idx, len(arr))
	}
	return arr[idx], nil
}

func intrinsicArrayLength(name string, args []any, _ []intrinsicArg) (any, error) {
	if err := wantArgs(name, args, 1); err != nil {
		return nil, err
	}
	arr, err := arrayArg(name, args[0], "first")
	if err != nil {
		return nil, err
	}
	return float64(len(arr)), nil
}

func intrinsicArrayUnique(name string, args []any, _ []intrinsicArg) (any, error) {
	if err := wantArgs(name, args, 1); err != nil {
		return nil, err
	}
	arr, err := arrayArg(name, args[0], "first")
	if err != nil {
		return nil, err
	}
	seen := make(map[string]bool, len(arr))
	out := []any{}
	for _, item := range arr {
		key := canonicalJSON(item)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, item)
	}
	return out, nil
}

func intrinsicBase64Encode(name string, args []any, _ []intrinsicArg) (any, error) {
	if err := wantArgs(name, args, 1); err != nil {
		return nil, err
	}
	s, err := stringArg(name, args[0], "first")
	if err != nil {
		return nil, err
	}
	if err := limitedString(name, s); err != nil {
		return nil, err
	}
	return base64.StdEncoding.EncodeToString([]byte(s)), nil
}

func intrinsicBase64Decode(name string, args []any, _ []intrinsicArg) (any, error) {
	if err := wantArgs(name, args, 1); err != nil {
		return nil, err
	}
	s, err := stringArg(name, args[0], "first")
	if err != nil {
		return nil, err
	}
	if err := limitedString(name, s); err != nil {
		return nil, err
	}
	// MIME Base64 tolerates line breaks.
	cleaned := strings.NewReplacer("\r", "", "\n", "").Replace(s)
	decoded, decErr := base64.StdEncoding.DecodeString(cleaned)
	if decErr != nil {
		return nil, intrinsicErrorf("%s: argument is not valid Base64: %v", name, decErr)
	}
	return string(decoded), nil
}

var intrinsicHashes = map[string]func() hash.Hash{
	"MD5":     md5.New,
	"SHA-1":   sha1.New,
	"SHA-256": sha256.New,
	"SHA-384": sha512.New384,
	"SHA-512": sha512.New,
}

func intrinsicHash(name string, args []any, _ []intrinsicArg) (any, error) {
	if err := wantArgs(name, args, 2); err != nil {
		return nil, err
	}
	data, ok := args[0].(string)
	if !ok {
		data = marshalNoHTMLEscape(args[0])
	}
	if err := limitedString(name, data); err != nil {
		return nil, err
	}
	algorithm, err := stringArg(name, args[1], "second")
	if err != nil {
		return nil, err
	}
	newHash, ok := intrinsicHashes[algorithm]
	if !ok {
		return nil, intrinsicErrorf("%s: algorithm %q is not one of MD5, SHA-1, SHA-256, SHA-384, SHA-512", name, algorithm)
	}
	h := newHash()
	h.Write([]byte(data))
	return hex.EncodeToString(h.Sum(nil)), nil
}

func intrinsicJSONMerge(name string, args []any, _ []intrinsicArg) (any, error) {
	if err := wantArgs(name, args, 3); err != nil {
		return nil, err
	}
	left, lok := args[0].(map[string]any)
	right, rok := args[1].(map[string]any)
	if !lok || !rok {
		return nil, intrinsicErrorf("%s expects two JSON objects, got %s and %s", name, jsonTypeName(args[0]), jsonTypeName(args[1]))
	}
	deep, ok := args[2].(bool)
	if !ok {
		return nil, intrinsicErrorf("%s expects a boolean as its third argument, got %s", name, jsonTypeName(args[2]))
	}
	if deep {
		return nil, intrinsicErrorf("%s supports only shallow merging; the third argument must be false", name)
	}
	out := make(map[string]any, len(left)+len(right))
	for k, v := range left {
		out[k] = v
	}
	for k, v := range right {
		out[k] = v
	}
	return out, nil
}

func intrinsicMathRandom(name string, args []any, _ []intrinsicArg) (any, error) {
	if len(args) != 2 && len(args) != 3 {
		return nil, intrinsicErrorf("%s expects 2 or 3 arguments, got %d", name, len(args))
	}
	start, err := integerArg(name, args[0], "first")
	if err != nil {
		return nil, err
	}
	end, err := integerArg(name, args[1], "second")
	if err != nil {
		return nil, err
	}
	if start >= end {
		return nil, intrinsicErrorf("%s expects the start (%d) to be less than the end (%d)", name, start, end)
	}
	if len(args) == 3 {
		seed, err := integerArg(name, args[2], "third")
		if err != nil {
			return nil, err
		}
		rng := rand.New(rand.NewPCG(uint64(seed), 0))
		return float64(start + rng.Int64N(end-start)), nil
	}
	return float64(start + rand.Int64N(end-start)), nil
}

func intrinsicMathAdd(name string, args []any, _ []intrinsicArg) (any, error) {
	if err := wantArgs(name, args, 2); err != nil {
		return nil, err
	}
	var sum int64
	for i, position := range []string{"first", "second"} {
		n, err := integerArg(name, args[i], position)
		if err != nil {
			return nil, err
		}
		if n < math.MinInt32 || n > math.MaxInt32 {
			return nil, intrinsicErrorf("%s: %s argument %d is outside the range %d to %d", name, position, n, math.MinInt32, math.MaxInt32)
		}
		sum += n
	}
	return float64(sum), nil
}

// intrinsicStringSplit splits on any of the delimiter's characters and drops
// empty tokens, as Java's StringTokenizer (and AWS) does.
func intrinsicStringSplit(name string, args []any, _ []intrinsicArg) (any, error) {
	if err := wantArgs(name, args, 2); err != nil {
		return nil, err
	}
	s, err := stringArg(name, args[0], "first")
	if err != nil {
		return nil, err
	}
	delimiters, err := stringArg(name, args[1], "second")
	if err != nil {
		return nil, err
	}
	out := []any{}
	for _, token := range strings.FieldsFunc(s, func(r rune) bool { return strings.ContainsRune(delimiters, r) }) {
		out = append(out, token)
	}
	return out, nil
}

func intrinsicUUID(name string, args []any, _ []intrinsicArg) (any, error) {
	if err := wantArgs(name, args, 0); err != nil {
		return nil, err
	}
	return uuid.NewString(), nil
}
