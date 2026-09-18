package stepfunctions

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// This file is the JSONPath engine behind ASL "Paths". AWS evaluates Paths with
// Jayway JsonPath semantics, so this follows Jayway where the two differ from
// the IETF draft:
//
//   - A definite path (only member names and single indexes) returns the value
//     it names, and a missing member is an error.
//   - An indefinite path (`*`, `..`, slices, unions, filters) returns a JSON
//     array of every match, which is empty when nothing matches.
//   - A filter on an object tests the object itself; on an array it tests
//     each element.
//
// Wildcards and recursive descent visit object members in sorted key order so
// results are deterministic (decoded JSON objects carry no key order).

type pathRootKind int

const (
	rootInput    pathRootKind = iota // $
	rootContext                      // $$
	rootVariable                     // $name
	rootCurrent                      // @ (filter expressions only)
)

type pathStepKind int

const (
	stepName     pathStepKind = iota // .name or ['name']
	stepIndex                        // [n]
	stepWildcard                     // .* or [*]
	stepNames                        // ['a','b']
	stepIndexes                      // [0,1]
	stepSlice                        // [start:end:step]
	stepFilter                       // [?(expr)]
)

type pathStep struct {
	kind    pathStepKind
	descent bool // reached through `..`
	names   []string
	indexes []int
	slice   sliceSpec
	filter  filterExpr
}

type sliceSpec struct {
	start, end, step int
	hasStart, hasEnd bool
}

type jsonPath struct {
	raw      string
	root     pathRootKind
	variable string
	steps    []pathStep
}

// definite reports whether the path names at most one node.
func (p *jsonPath) definite() bool {
	for _, s := range p.steps {
		if s.descent || (s.kind != stepName && s.kind != stepIndex) {
			return false
		}
	}
	return true
}

func (p *jsonPath) rootLabel() string {
	switch p.root {
	case rootContext:
		return "$$"
	case rootVariable:
		return "$" + p.variable
	case rootCurrent:
		return "@"
	case rootInput:
	}
	return "$"
}

// ─── Parsing ──────────────────────────────────────────────────────────────────

type pathParser struct {
	src    string
	pos    int
	filter bool // inside a filter expression: names end at operators and spaces
}

// parseJSONPath parses a complete ASL path. The whole string must be consumed.
func parseJSONPath(path string) (*jsonPath, error) {
	raw := strings.TrimSpace(path)
	if raw == "" {
		return nil, pathErrorf("path is empty")
	}
	if raw[0] != '$' {
		return nil, pathErrorf("path %q must start with $ or $$", path)
	}
	p := &pathParser{src: raw}
	parsed, err := p.parsePath()
	if err != nil {
		return nil, pathErrorf("path %q: %s", path, err.Error())
	}
	if p.pos != len(raw) {
		return nil, pathErrorf("path %q is malformed at %q", path, raw[p.pos:])
	}
	parsed.raw = raw
	return parsed, nil
}

func (p *pathParser) errorf(format string, args ...any) error {
	return fmt.Errorf(format, args...)
}

func (p *pathParser) peek() byte {
	if p.pos < len(p.src) {
		return p.src[p.pos]
	}
	return 0
}

func (p *pathParser) skipSpace() {
	for p.pos < len(p.src) && (p.src[p.pos] == ' ' || p.src[p.pos] == '\t' || p.src[p.pos] == '\n' || p.src[p.pos] == '\r') {
		p.pos++
	}
}

func isIdentStart(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func isIdentChar(c byte) bool {
	return isIdentStart(c) || (c >= '0' && c <= '9')
}

// parsePath parses a root followed by its steps, starting at `$` or `@`.
func (p *pathParser) parsePath() (*jsonPath, error) {
	out := &jsonPath{}
	switch {
	case p.peek() == '@':
		p.pos++
		out.root = rootCurrent
	case strings.HasPrefix(p.src[p.pos:], "$$"):
		p.pos += 2
		out.root = rootContext
	case p.peek() == '$':
		p.pos++
		if p.pos < len(p.src) && isIdentStart(p.src[p.pos]) {
			start := p.pos
			for p.pos < len(p.src) && isIdentChar(p.src[p.pos]) {
				p.pos++
			}
			out.root = rootVariable
			out.variable = p.src[start:p.pos]
		}
	default:
		return nil, p.errorf("expected $ or @")
	}
	for {
		switch {
		case strings.HasPrefix(p.src[p.pos:], ".."):
			p.pos += 2
			step, err := p.parseDotOrBracket(true)
			if err != nil {
				return nil, err
			}
			out.steps = append(out.steps, step)
		case p.peek() == '.':
			p.pos++
			step, err := p.parseDotOrBracket(false)
			if err != nil {
				return nil, err
			}
			out.steps = append(out.steps, step)
		case p.peek() == '[':
			step, err := p.parseBracket()
			if err != nil {
				return nil, err
			}
			out.steps = append(out.steps, step)
		default:
			return out, nil
		}
	}
}

// parseDotOrBracket parses what follows `.` or `..`: `*`, a member name, or
// (after `..` only) a bracket subscript.
func (p *pathParser) parseDotOrBracket(descent bool) (pathStep, error) {
	if descent && p.peek() == '[' {
		step, err := p.parseBracket()
		step.descent = true
		return step, err
	}
	if p.peek() == '*' {
		p.pos++
		return pathStep{kind: stepWildcard, descent: descent}, nil
	}
	start := p.pos
	for p.pos < len(p.src) && !p.nameTerminator(p.src[p.pos]) {
		p.pos++
	}
	name := p.src[start:p.pos]
	if name == "" {
		return pathStep{}, p.errorf("empty field name")
	}
	return pathStep{kind: stepName, names: []string{name}, descent: descent}, nil
}

func (p *pathParser) nameTerminator(c byte) bool {
	if c == '.' || c == '[' {
		return true
	}
	if !p.filter {
		return false
	}
	return strings.IndexByte(" \t\r\n]()=!<>&|,", c) >= 0
}

// parseBracket parses a `[...]` subscript.
func (p *pathParser) parseBracket() (pathStep, error) {
	p.pos++ // [
	p.skipSpace()
	switch c := p.peek(); {
	case c == '*':
		p.pos++
		if err := p.closeBracket(); err != nil {
			return pathStep{}, err
		}
		return pathStep{kind: stepWildcard}, nil
	case c == '?':
		p.pos++
		p.skipSpace()
		if p.peek() != '(' {
			return pathStep{}, p.errorf("filter must be written [?(...)]")
		}
		p.pos++
		wasFilter := p.filter
		p.filter = true
		expr, err := p.parseOr()
		p.filter = wasFilter
		if err != nil {
			return pathStep{}, err
		}
		p.skipSpace()
		if p.peek() != ')' {
			return pathStep{}, p.errorf("filter is missing its closing )")
		}
		p.pos++
		if err := p.closeBracket(); err != nil {
			return pathStep{}, err
		}
		return pathStep{kind: stepFilter, filter: expr}, nil
	case c == '\'' || c == '"':
		var names []string
		for {
			name, err := p.parseQuoted()
			if err != nil {
				return pathStep{}, err
			}
			names = append(names, name)
			p.skipSpace()
			if p.peek() == ',' {
				p.pos++
				p.skipSpace()
				continue
			}
			if err := p.closeBracket(); err != nil {
				return pathStep{}, err
			}
			break
		}
		if len(names) == 1 {
			return pathStep{kind: stepName, names: names}, nil
		}
		return pathStep{kind: stepNames, names: names}, nil
	}
	end := strings.IndexByte(p.src[p.pos:], ']')
	if end < 0 {
		return pathStep{}, p.errorf("unterminated [")
	}
	inner := strings.TrimSpace(p.src[p.pos : p.pos+end])
	p.pos += end + 1
	if strings.Contains(inner, ":") {
		return parseSlice(inner)
	}
	parts := strings.Split(inner, ",")
	indexes := make([]int, 0, len(parts))
	for _, part := range parts {
		idx, err := strconv.Atoi(strings.TrimSpace(part))
		if err != nil {
			return pathStep{}, p.errorf("unsupported subscript %q", inner)
		}
		indexes = append(indexes, idx)
	}
	if len(indexes) == 1 {
		return pathStep{kind: stepIndex, indexes: indexes}, nil
	}
	return pathStep{kind: stepIndexes, indexes: indexes}, nil
}

func (p *pathParser) closeBracket() error {
	p.skipSpace()
	if p.peek() != ']' {
		return p.errorf("expected ] at %q", p.src[p.pos:])
	}
	p.pos++
	return nil
}

func parseSlice(inner string) (pathStep, error) {
	parts := strings.Split(inner, ":")
	if len(parts) > 3 {
		return pathStep{}, fmt.Errorf("slice %q has too many parts", inner)
	}
	spec := sliceSpec{step: 1}
	for i, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		n, err := strconv.Atoi(part)
		if err != nil {
			return pathStep{}, fmt.Errorf("slice %q has a non-integer bound", inner)
		}
		switch i {
		case 0:
			spec.start, spec.hasStart = n, true
		case 1:
			spec.end, spec.hasEnd = n, true
		case 2:
			if n == 0 {
				return pathStep{}, fmt.Errorf("slice %q has a zero step", inner)
			}
			spec.step = n
		}
	}
	return pathStep{kind: stepSlice, slice: spec}, nil
}

// parseQuoted reads a '...' or "..." literal, honouring backslash escapes.
func (p *pathParser) parseQuoted() (string, error) {
	quote := p.peek()
	p.pos++
	var b strings.Builder
	for p.pos < len(p.src) {
		c := p.src[p.pos]
		switch {
		case c == '\\' && p.pos+1 < len(p.src):
			b.WriteByte(p.src[p.pos+1])
			p.pos += 2
		case c == quote:
			p.pos++
			return b.String(), nil
		default:
			b.WriteByte(c)
			p.pos++
		}
	}
	return "", p.errorf("unterminated string literal")
}

// ─── Filter expressions ───────────────────────────────────────────────────────

type filterExpr interface {
	test(env *pathEnv, current any) bool
}

type filterOr struct{ left, right filterExpr }
type filterAnd struct{ left, right filterExpr }
type filterNot struct{ inner filterExpr }
type filterExists struct{ operand filterOperand }
type filterCompare struct {
	op          string
	left, right filterOperand
}

// filterOperand is a literal or a path (relative `@...` or absolute `$...`).
type filterOperand struct {
	path    *jsonPath
	literal any
}

func (f filterOr) test(env *pathEnv, cur any) bool {
	return f.left.test(env, cur) || f.right.test(env, cur)
}

func (f filterAnd) test(env *pathEnv, cur any) bool {
	return f.left.test(env, cur) && f.right.test(env, cur)
}

func (f filterNot) test(env *pathEnv, cur any) bool { return !f.inner.test(env, cur) }

func (f filterExists) test(env *pathEnv, cur any) bool {
	_, ok := f.operand.resolve(env, cur)
	return ok
}

func (f filterCompare) test(env *pathEnv, cur any) bool {
	left, lok := f.left.resolve(env, cur)
	right, rok := f.right.resolve(env, cur)
	if !lok || !rok {
		// Jayway: an undefined operand equals nothing, so only != holds.
		return f.op == "!="
	}
	switch f.op {
	case "==":
		return jsonEqual(left, right)
	case "!=":
		return !jsonEqual(left, right)
	}
	if ln, ok := toNumber(left); ok {
		rn, ok := toNumber(right)
		if !ok {
			return false
		}
		return compareOrdered(f.op, ln, rn)
	}
	ls, lok := left.(string)
	rs, rok := right.(string)
	if !lok || !rok {
		return false
	}
	return compareOrdered(f.op, ls, rs)
}

func compareOrdered[T float64 | string](op string, a, b T) bool {
	switch op {
	case "<":
		return a < b
	case "<=":
		return a <= b
	case ">":
		return a > b
	case ">=":
		return a >= b
	}
	return false
}

// resolve evaluates the operand; ok is false when a path names nothing.
func (o filterOperand) resolve(env *pathEnv, cur any) (any, bool) {
	if o.path == nil {
		return o.literal, true
	}
	root, err := env.root(o.path, cur)
	if err != nil {
		return nil, false
	}
	if o.path.definite() {
		value, found, _ := walkDefinite(o.path, root)
		return value, found
	}
	matches := walkIndefinite(env, o.path, root)
	return matches, len(matches) > 0
}

func (p *pathParser) parseOr() (filterExpr, error) {
	left, err := p.parseAnd()
	if err != nil {
		return nil, err
	}
	for {
		p.skipSpace()
		if !strings.HasPrefix(p.src[p.pos:], "||") {
			return left, nil
		}
		p.pos += 2
		right, err := p.parseAnd()
		if err != nil {
			return nil, err
		}
		left = filterOr{left: left, right: right}
	}
}

func (p *pathParser) parseAnd() (filterExpr, error) {
	left, err := p.parseUnary()
	if err != nil {
		return nil, err
	}
	for {
		p.skipSpace()
		if !strings.HasPrefix(p.src[p.pos:], "&&") {
			return left, nil
		}
		p.pos += 2
		right, err := p.parseUnary()
		if err != nil {
			return nil, err
		}
		left = filterAnd{left: left, right: right}
	}
}

func (p *pathParser) parseUnary() (filterExpr, error) {
	p.skipSpace()
	switch {
	case p.peek() == '!' && !strings.HasPrefix(p.src[p.pos:], "!="):
		p.pos++
		inner, err := p.parseUnary()
		if err != nil {
			return nil, err
		}
		return filterNot{inner: inner}, nil
	case p.peek() == '(':
		p.pos++
		inner, err := p.parseOr()
		if err != nil {
			return nil, err
		}
		p.skipSpace()
		if p.peek() != ')' {
			return nil, p.errorf("filter has an unbalanced (")
		}
		p.pos++
		return inner, nil
	}
	left, err := p.parseOperand()
	if err != nil {
		return nil, err
	}
	p.skipSpace()
	for _, op := range []string{"==", "!=", "<=", ">=", "<", ">"} {
		if strings.HasPrefix(p.src[p.pos:], op) {
			p.pos += len(op)
			right, err := p.parseOperand()
			if err != nil {
				return nil, err
			}
			return filterCompare{op: op, left: left, right: right}, nil
		}
	}
	if left.path == nil {
		return nil, p.errorf("filter literal must be compared with something")
	}
	return filterExists{operand: left}, nil
}

func (p *pathParser) parseOperand() (filterOperand, error) {
	p.skipSpace()
	c := p.peek()
	switch {
	case c == '@' || c == '$':
		sub, err := p.parsePath()
		if err != nil {
			return filterOperand{}, err
		}
		return filterOperand{path: sub}, nil
	case c == '\'' || c == '"':
		s, err := p.parseQuoted()
		return filterOperand{literal: s}, err
	}
	start := p.pos
	for p.pos < len(p.src) && strings.IndexByte("+-.0123456789eEtruefalsn", p.src[p.pos]) >= 0 {
		p.pos++
	}
	token := p.src[start:p.pos]
	switch token {
	case "true":
		return filterOperand{literal: true}, nil
	case "false":
		return filterOperand{literal: false}, nil
	case "null":
		return filterOperand{literal: nil}, nil
	}
	n, err := strconv.ParseFloat(token, 64)
	if err != nil || token == "" {
		return filterOperand{}, p.errorf("filter operand %q is not a path or a literal", p.src[start:])
	}
	return filterOperand{literal: n}, nil
}

// ─── Evaluation ───────────────────────────────────────────────────────────────

// pathEnv is what a path's roots resolve against.
type pathEnv struct {
	doc    any
	ctxObj map[string]any
}

// root returns the node a path starts from. `cur` is the filter's `@`.
func (e *pathEnv) root(p *jsonPath, cur any) (any, error) {
	switch p.root {
	case rootContext:
		return visibleContext(e.ctxObj), nil
	case rootVariable:
		vars, _ := e.ctxObj[variablesContextKey].(map[string]any)
		value, ok := vars[p.variable]
		if !ok {
			return nil, pathErrorf("path %q: variable $%s is not defined", p.raw, p.variable)
		}
		return value, nil
	case rootCurrent:
		return cur, nil
	case rootInput:
	}
	return e.doc, nil
}

// visibleContext is the context object as a path sees it: without the
// reserved key that carries workflow variables.
func visibleContext(ctxObj map[string]any) any {
	if _, ok := ctxObj[variablesContextKey]; !ok {
		return ctxObj
	}
	out := make(map[string]any, len(ctxObj)-1)
	for k, v := range ctxObj {
		if k != variablesContextKey {
			out[k] = v
		}
	}
	return out
}

// evaluate resolves a parsed path: the value for a definite path (a missing
// node is an error), or the array of matches for an indefinite one.
func (e *pathEnv) evaluate(p *jsonPath) (any, error) {
	root, err := e.root(p, nil)
	if err != nil {
		return nil, err
	}
	if p.definite() {
		value, found, missErr := walkDefinite(p, root)
		if !found {
			return nil, missErr
		}
		return value, nil
	}
	return walkIndefinite(e, p, root), nil
}

// walkDefinite follows a definite path. When it does not resolve, found is
// false and err explains why in the historical message style.
func walkDefinite(p *jsonPath, root any) (value any, found bool, err error) {
	current := root
	for i, step := range p.steps {
		if step.kind == stepIndex {
			arr, ok := current.([]any)
			if !ok {
				return nil, false, pathErrorf("path %q: %s is not an array", p.raw, definitePrefix(p, i))
			}
			idx := step.indexes[0]
			if idx < 0 {
				idx += len(arr)
			}
			if idx < 0 || idx >= len(arr) {
				return nil, false, pathErrorf("path %q: index %d is out of range", p.raw, step.indexes[0])
			}
			current = arr[idx]
			continue
		}
		obj, ok := current.(map[string]any)
		if !ok {
			return nil, false, pathErrorf("path %q: %s is not an object", p.raw, definitePrefix(p, i))
		}
		next, ok := obj[step.names[0]]
		if !ok {
			return nil, false, pathErrorf("path %q: field %q is not present", p.raw, step.names[0])
		}
		current = next
	}
	return current, true, nil
}

func definitePrefix(p *jsonPath, upto int) string {
	var b strings.Builder
	b.WriteString(p.rootLabel())
	for _, step := range p.steps[:upto] {
		if step.kind == stepIndex {
			fmt.Fprintf(&b, "[%d]", step.indexes[0])
			continue
		}
		b.WriteString("." + step.names[0])
	}
	return b.String()
}

func walkIndefinite(env *pathEnv, p *jsonPath, root any) []any {
	nodes := []any{root}
	for _, step := range p.steps {
		next := []any{}
		for _, node := range nodes {
			if step.descent {
				next = descend(env, step, node, next)
			} else {
				next = applyStep(env, step, node, next)
			}
		}
		nodes = next
	}
	return nodes
}

// descend applies step to node and then to every descendant, pre-order, which
// is the order Jayway's deep scan produces.
func descend(env *pathEnv, step pathStep, node any, out []any) []any {
	if step.kind == stepFilter {
		// A deep-scan filter tests every object in the tree once.
		if obj, ok := node.(map[string]any); ok && step.filter.test(env, obj) {
			out = append(out, obj)
		}
	} else {
		out = applyStep(env, step, node, out)
	}
	for _, child := range children(node) {
		out = descend(env, step, child, out)
	}
	return out
}

func children(node any) []any {
	switch x := node.(type) {
	case map[string]any:
		out := make([]any, 0, len(x))
		for _, k := range sortedKeys(x) {
			out = append(out, x[k])
		}
		return out
	case []any:
		return x
	}
	return nil
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func applyStep(env *pathEnv, step pathStep, node any, out []any) []any {
	switch step.kind {
	case stepName, stepNames:
		if obj, ok := node.(map[string]any); ok {
			for _, name := range step.names {
				if v, ok := obj[name]; ok {
					out = append(out, v)
				}
			}
		}
	case stepIndex, stepIndexes:
		if arr, ok := node.([]any); ok {
			for _, idx := range step.indexes {
				if idx < 0 {
					idx += len(arr)
				}
				if idx >= 0 && idx < len(arr) {
					out = append(out, arr[idx])
				}
			}
		}
	case stepWildcard:
		out = append(out, children(node)...)
	case stepSlice:
		if arr, ok := node.([]any); ok {
			out = appendSlice(arr, step.slice, out)
		}
	case stepFilter:
		switch x := node.(type) {
		case []any:
			for _, item := range x {
				if step.filter.test(env, item) {
					out = append(out, item)
				}
			}
		case map[string]any:
			if step.filter.test(env, x) {
				out = append(out, x)
			}
		}
	}
	return out
}

// appendSlice applies Python slice semantics: negative bounds count from the
// end and out-of-range bounds clamp.
func appendSlice(arr []any, s sliceSpec, out []any) []any {
	n := len(arr)
	norm := func(v, lo, hi int) int {
		if v < 0 {
			v += n
		}
		return min(max(v, lo), hi)
	}
	if s.step > 0 {
		start, end := 0, n
		if s.hasStart {
			start = norm(s.start, 0, n)
		}
		if s.hasEnd {
			end = norm(s.end, 0, n)
		}
		for i := start; i < end; i += s.step {
			out = append(out, arr[i])
		}
		return out
	}
	start, end := n-1, -1
	if s.hasStart {
		start = norm(s.start, -1, n-1)
	}
	if s.hasEnd {
		end = norm(s.end, -1, n-1)
	}
	for i := start; i > end; i += s.step {
		out = append(out, arr[i])
	}
	return out
}

// jsonEqual compares two decoded JSON values by value.
func jsonEqual(a, b any) bool {
	if an, ok := toNumber(a); ok {
		bn, ok := toNumber(b)
		return ok && an == bn
	}
	switch x := a.(type) {
	case string:
		y, ok := b.(string)
		return ok && x == y
	case bool:
		y, ok := b.(bool)
		return ok && x == y
	case nil:
		return b == nil
	}
	return canonicalJSON(a) == canonicalJSON(b)
}

// canonicalJSON is a comparison key: encoding/json sorts object keys.
func canonicalJSON(v any) string {
	encoded, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("%#v", v)
	}
	return string(encoded)
}
