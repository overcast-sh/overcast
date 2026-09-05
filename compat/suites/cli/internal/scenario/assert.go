package scenario

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

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
	return last
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
			return e.fail(obs, step, kind+" matches", path, "a compilable pattern", quote(err.Error()))
		}
		s, isStr := got.(string)
		if !resolved || !isStr || !re.MatchString(s) {
			return fail(fmt.Sprintf("a string matching %q", pattern))
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

// renderWhereExpected prints a where map for a failure message, in path order.
func renderWhereExpected(where map[string]any) string {
	parts := make([]string, 0, len(where))
	for _, k := range sortedKeys(where) {
		parts = append(parts, fmt.Sprintf("%s=%s", k, render(where[k])))
	}
	return "{" + strings.Join(parts, ", ") + "}"
}

// renderList prints the list a membership check searched. It is the actual
// value of the failure, so it is printed whole rather than truncated: a
// generated failure that says only "no match" cannot be diagnosed without
// re-running.
func renderList(list []any) string {
	if len(list) == 0 {
		return "an empty list"
	}
	return render(list)
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
