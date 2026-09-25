package scenario

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// This file's counterpart is
// compat/suites/go-sdk/internal/scenario/assert.go: the two evaluate the same
// closed assertion set (compat/model/README.md § Assertions) against their own
// backend's response shape and are not byte-identical, but a change to how one
// assertion kind is evaluated here usually needs a matching change there —
// change both or neither.

// The closed assertion set (compat/model/README.md § Assertions).
const (
	kindResponseField = "responseField"
	kindReadback      = "readback"
	kindListContains  = "listContains"
	kindAbsent        = "absent"
	kindErrorCode     = "errorCode"
	kindEventually    = "eventually"
)

// errorCodeClause returns the test's errorCode clause, if it has one. Its
// presence means the primary call is expected to fail.
func errorCodeClause(clauses []Assertion) *ErrorClause {
	for i := range clauses {
		if clauses[i].Kind == kindErrorCode {
			return clauses[i].Error
		}
	}
	return nil
}

// assert evaluates one clause. primary is the test's own response, which
// responseField and a call-less listContains/absent read.
func (e *execution) assert(ctx context.Context, a *Assertion, primary observed, step string) error {
	switch a.Kind {
	case kindResponseField:
		return e.checkAll(primary, a.Checks, kindResponseField, step)

	case kindReadback:
		if a.Call == nil {
			return e.fail(primary, step, kindReadback, "", "a call to read back", "<none>")
		}
		obs, err := e.call(ctx, a.Call, step)
		if err != nil {
			return err
		}
		if err := e.checkAll(obs, a.Checks, kindReadback, step); err != nil {
			return err
		}
		// A clause's exports are applied only once the clause holds: inside an
		// eventually, the failing attempts must not leave a half-read response
		// in the context bag for the next clause to $ref.
		return e.applyExports(a.Call, obs, step)

	case kindListContains, kindAbsent:
		return e.assertList(ctx, a, primary, step)

	case kindEventually:
		return e.eventually(ctx, a, primary, step)

	case kindErrorCode:
		// Checked against the primary call in runTest; a nested one is not
		// representable (eventually wraps only readback/listContains/absent).
		return e.fail(primary, step, kindErrorCode, "", "an errorCode clause on the test's own call", "a nested one")

	default:
		return e.fail(primary, step, a.Kind, "", "one of the IR's assertion kinds", quote(a.Kind))
	}
}

// assertList evaluates listContains and both forms of absent.
func (e *execution) assertList(ctx context.Context, a *Assertion, primary observed, step string) error {
	// absent's error form: the call must fail with the named error.
	if a.Kind == kindAbsent && a.Error != nil {
		if a.Call == nil {
			return e.fail(primary, step, kindAbsent, "", "a call to raise the error", "<none>")
		}
		obs, cliErr, err := e.callRaw(ctx, a.Call, step)
		if err != nil {
			return err // a $ref or params problem, already fully described
		}
		if cliErr == nil {
			return e.fail(obs, step, kindAbsent, "", acceptedCodes(a.Error), "<no error>")
		}
		if !matchesError(cliErr, a.Error) {
			return e.fail(obs, step, kindAbsent, "", acceptedCodes(a.Error), quote(cliErr.Error()))
		}
		return nil
	}

	// The list forms read the clause's own call when it has one, else the
	// test's own response.
	obs := primary
	if a.Call != nil {
		var err error
		obs, err = e.call(ctx, a.Call, step)
		if err != nil {
			return err
		}
	}
	if !obs.ok {
		return e.fail(obs, step, a.Kind, a.ItemsPath, "a response to read the list from", "<no response>")
	}

	items, resolved, err := resolvePath(obs.body, a.ItemsPath)
	if err != nil {
		return e.fail(obs, step, a.Kind, a.ItemsPath, "a well-formed items path", quote(err.Error()))
	}
	var list []any
	if resolved {
		l, ok := items.([]any)
		if !ok {
			return e.fail(obs, step, a.Kind, a.ItemsPath, "a list", render(items))
		}
		list = l
	}
	// A missing list counts as empty: several AWS services omit an empty list
	// member rather than serializing [].

	matched, err := e.matchItem(obs, list, a.Where, a.Kind, step)
	if err != nil {
		return err
	}

	if a.Kind == kindListContains {
		if matched < 0 {
			return e.fail(obs, step, kindListContains, a.ItemsPath,
				fmt.Sprintf("an item matching %s", renderWhereExpected(a.Where)), renderList(list))
		}
	} else if matched >= 0 {
		return e.fail(obs, step, kindAbsent, a.ItemsPath,
			fmt.Sprintf("no item matching %s", renderWhereExpected(a.Where)), render(list[matched]))
	}
	// The clause held. A list clause may carry a call with exports of its own —
	// the schema allows it wherever a call is allowed — and they are applied on
	// the same terms as a read-back's: only once the clause holds.
	if a.Call != nil {
		return e.applyExports(a.Call, obs, step)
	}
	return nil
}

// matchItem returns the index of the first item satisfying every where entry,
// or -1. An unevaluatable where value (an unresolvable $ref) is an error for
// the step rather than a non-match.
func (e *execution) matchItem(obs observed, list []any, where map[string]any, kind, step string) (int, error) {
	type criterion struct {
		path string
		want any
	}
	criteria := make([]criterion, 0, len(where))
	for _, path := range sortedKeys(where) {
		want, err := e.eval.eval(where[path])
		if err != nil {
			return -1, e.fail(obs, step, kind, path, "the where value to evaluate", quote(err.Error()))
		}
		criteria = append(criteria, criterion{path: path, want: want})
	}

	for i, item := range list {
		all := true
		for _, c := range criteria {
			// "$" is the item itself, which is how a list of strings is
			// matched: {"$": {"$ref": "queue.url"}}.
			got, ok, err := resolvePath(item, c.path)
			if err != nil {
				return -1, e.fail(obs, step, kind, c.path, "a well-formed where path", quote(err.Error()))
			}
			if !ok || !jsonEqual(got, c.want) {
				all = false
				break
			}
		}
		if all {
			return i, nil
		}
	}
	return -1, nil
}

// eventually retries the inner clause up to maxAttempts times, waiting delayMs
// between attempts and no longer. The last failure is the reported one, and a
// read-back inside applies its exports only on the attempt that passes —
// which assert already guarantees, because it applies them only when the
// checks hold.
//
// That last failure is reported behind the budget that was spent on it. Bare,
// it is indistinguishable from a clause evaluated once, and the two want
// opposite fixes: a real disagreement, or a poll budget too short for how long
// this service takes to settle. The sibling interpreters word the prefix
// identically (compat/suites/python-sdk/lib/scenario/assertions.py), so a
// generated group's failure reads the same whichever suite reports it. It is
// wrapped rather than interpolated so an inner 501 still carries
// harness.ErrUnimplemented out to the harness.
func (e *execution) eventually(ctx context.Context, a *Assertion, primary observed, step string) error {
	if a.Assert == nil {
		return e.fail(primary, step, kindEventually, "", "a clause to retry", "<none>")
	}
	attempts := a.MaxAttempts
	if attempts < 1 {
		attempts = 1
	}
	inner := step + ".assert"
	var last error
	for i := 0; i < attempts; i++ {
		if i > 0 {
			if err := wait(ctx, a.DelayMs); err != nil {
				return fmt.Errorf("%s/%s: %s: %w", e.group.Name, e.test, a.Assert.Kind, err)
			}
		}
		last = e.assert(ctx, a.Assert, primary, inner)
		if last == nil {
			return nil
		}
	}
	return fmt.Errorf("eventually gave up after %d attempt(s) %dms apart; last failure: %w",
		attempts, a.DelayMs, last)
}

// checkAll evaluates every check of a clause against one response, in path
// order so a failure message is the same on every run.
func (e *execution) checkAll(obs observed, checks map[string]Check, kind, step string) error {
	if !obs.ok {
		return e.fail(obs, step, kind, "", "a response to check", "<no response>")
	}
	for _, path := range sortedKeys(checks) {
		if err := e.check(obs, path, checks[path], kind, step); err != nil {
			return err
		}
	}
	return nil
}

// check evaluates one check against one response path.
func (e *execution) check(obs observed, path string, c Check, kind, step string) error {
	got, resolved, err := resolvePath(obs.body, path)
	if err != nil {
		return e.fail(obs, step, kind+" "+string(c.Kind), path, "a well-formed path", quote(err.Error()))
	}
	fail := func(expected string) error {
		return e.fail(obs, step, kind+" "+string(c.Kind), path, expected, renderResolved(got, resolved))
	}

	switch c.Kind {
	case CheckMissing:
		if resolved {
			return fail("the path not to resolve")
		}
		return nil

	case CheckIsList:
		// True of a present list, empty or not, and of an absent member:
		// several AWS services omit an empty list rather than serializing [].
		// A present value that is not a list still fails.
		if !resolved {
			return nil
		}
		if _, ok := got.([]any); !ok {
			return fail("a list, or no such member")
		}
		return nil

	case CheckNonEmpty:
		if !resolved {
			return fail("a non-empty value")
		}
		if isEmpty(got) {
			return fail("a non-empty value")
		}
		return nil

	case CheckEquals:
		want, err := e.eval.eval(c.Value)
		if err != nil {
			return e.fail(obs, step, kind+" equals", path, "the expected value to evaluate", quote(err.Error()))
		}
		if !resolved || !jsonEqual(got, want) {
			return fail(render(want))
		}
		return nil

	case CheckMatches:
		pattern, ok := c.Value.(string)
		if !ok {
			return fail("a string pattern in the scenario file")
		}
		re, err := regexp.Compile(pattern)
		if err != nil {
			// The model states its patterns in RE2, which Go's regexp is, so
			// this is nearly unreachable here — but a pattern the engine will
			// not compile is a normal six-field mismatch in every interpreter
			// (compat/model/README.md § Assertions), never an exception out of
			// the evaluator, and the phrase is the same in all three.
			return e.fail(obs, step, kind+" matches", path,
				fmt.Sprintf("pattern %s", pattern), quote("unsupported pattern: "+err.Error()))
		}
		s, isStr := got.(string)
		if !resolved || !isStr || !re.MatchString(s) {
			return fail(fmt.Sprintf("a string matching %q", pattern))
		}
		return nil

	case CheckEqualsJSON:
		if holds, actual := equalsJSON(got, resolved, c.Value); !holds {
			return e.fail(obs, step, kind+" equalsJSON", path, "equalsJSON "+render(c.Value), actual)
		}
		return nil

	default:
		return fail(fmt.Sprintf("one of the IR's checks, got %q", string(c.Kind)))
	}
}

// isEmpty is the IR's emptiness: null, "", [] or {}. Numbers and booleans are
// never empty, which is what stops nonEmpty failing on a legitimate 0 or false.
func isEmpty(v any) bool {
	switch t := v.(type) {
	case nil:
		return true
	case string:
		return t == ""
	case []any:
		return len(t) == 0
	case map[string]any:
		return len(t) == 0
	default:
		return false
	}
}

// equalsJSON evaluates an equalsJSON check against the value at its path and,
// when it does not hold, says what was there for the failure message: the
// document it decoded to, the value that is not one, or the usual missing
// rendering.
func equalsJSON(got any, resolved bool, want any) (bool, string) {
	if !resolved {
		return false, missingValue
	}
	doc, isDoc := jsonDocument(got)
	if !isDoc {
		return false, "not a JSON document: " + render(got)
	}
	if !jsonValueEqual(doc, want) {
		return false, "document " + render(doc)
	}
	return true, ""
}

// jsonDocument is the document an equalsJSON check compares, and whether the
// value holds one at all.
//
// An object or a list is the document as it stands: botocore decodes an IAM
// policy document before the CLI prints it, so this backend usually sees one
// of those. A string is percent-decoded once and then read as exactly one JSON
// text — the form the wire carries, which the other SDKs hand back as is.
// Anything else (a number, a boolean, null) is not a document.
func jsonDocument(v any) (any, bool) {
	switch t := v.(type) {
	case map[string]any, []any:
		return t, true
	case string:
		// json.Unmarshal is the strict reader the check wants: surrounding
		// whitespace is fine, anything after the one value is an error, and a
		// number decodes to the float64 the comparison needs.
		var doc any
		if err := json.Unmarshal([]byte(percentDecode(t)), &doc); err != nil {
			return nil, false
		}
		return doc, true
	default:
		return nil, false
	}
}

// percentDecode is Python's urllib.parse.unquote, which botocore applies to a
// policy document unconditionally and which python-sdk and cli therefore
// cannot opt out of: `%XX` (either case) becomes that byte, `+` stays `+`, and
// a `%` not followed by two hex digits is kept as written. It runs once, so
// `%257B` is `%7B`. url.PathUnescape and url.QueryUnescape both refuse a
// malformed escape, and the latter turns `+` into a space, so neither will do.
func percentDecode(s string) string {
	if !strings.Contains(s, "%") {
		return s
	}
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == '%' && i+2 < len(s) {
			hi, hiOK := unhex(s[i+1])
			lo, loOK := unhex(s[i+2])
			if hiOK && loOK {
				out = append(out, hi<<4|lo)
				i += 2
				continue
			}
		}
		out = append(out, s[i])
	}
	return string(out)
}

func unhex(c byte) (byte, bool) {
	switch {
	case '0' <= c && c <= '9':
		return c - '0', true
	case 'a' <= c && c <= 'f':
		return c - 'a' + 10, true
	case 'A' <= c && c <= 'F':
		return c - 'A' + 10, true
	}
	return 0, false
}

// jsonValueEqual is equalsJSON's equality: objects by member name whatever
// their order, arrays element by element in order, numbers by numeric value
// (so 1, 1.0 and 1e0 are equal, and so are -0 and 0, which jsonEqual's
// canonical text would tell apart), and strings, booleans and null by type and
// value with no coercion.
func jsonValueEqual(a, b any) bool {
	switch x := a.(type) {
	case map[string]any:
		y, ok := b.(map[string]any)
		if !ok || len(x) != len(y) {
			return false
		}
		for k, xv := range x {
			yv, present := y[k]
			if !present || !jsonValueEqual(xv, yv) {
				return false
			}
		}
		return true
	case []any:
		y, ok := b.([]any)
		if !ok || len(x) != len(y) {
			return false
		}
		for i := range x {
			if !jsonValueEqual(x[i], y[i]) {
				return false
			}
		}
		return true
	case float64:
		y, ok := b.(float64)
		return ok && x == y
	case string:
		y, ok := b.(string)
		return ok && x == y
	case bool:
		y, ok := b.(bool)
		return ok && x == y
	case nil:
		return b == nil
	default:
		return false
	}
}

// renderWhereExpected prints a where map for a failure message, in path order.
func renderWhereExpected(where map[string]any) string {
	parts := make([]string, 0, len(where))
	for _, k := range sortedKeys(where) {
		parts = append(parts, fmt.Sprintf("%s=%s", k, render(where[k])))
	}
	return "{" + strings.Join(parts, ", ") + "}"
}

// renderList prints the list a membership check searched. It is the actual
// value of the failure, so it is printed rather than summarised — a generated
// failure that says only "no match" cannot be diagnosed without re-running —
// but it is capped: a ListObjectsV2 or a ListPolicies against a shared account
// can return thousands of items, and the whole list goes into one NDJSON
// `error` field that the dashboard and the report tooling both read.
func renderList(list []any) string {
	if len(list) == 0 {
		return "an empty list"
	}
	return clip(render(list))
}

// sortedKeys orders a map's keys so failure messages and check order are
// deterministic across runs — three identical runs is an acceptance criterion.
func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
