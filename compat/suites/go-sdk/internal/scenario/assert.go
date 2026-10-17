package scenario

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// This file's counterpart is compat/suites/cli/internal/scenario/assert.go:
// the two evaluate the same closed assertion set
// (compat/model/README.md § Assertions) against their own backend's response
// shape and are not byte-identical, but a change to how one assertion kind is
// evaluated here usually needs a matching change there — change both or
// neither.
//
// The closed assertion set (compat/model/README.md § Assertions), evaluated
// over the document form of a response.

// errorCodeClause returns the test's errorCode clause, if it has one. Its
// presence means the primary call is expected to fail.
func errorCodeClause(clauses []Clause) *ErrorSpec {
	for i := range clauses {
		if clauses[i].Kind == KindErrorCode {
			return clauses[i].Error
		}
	}
	return nil
}

// assert evaluates one clause. primary is the test's own response, which
// responseField and a call-less listContains/absent read.
func (e *execution) assert(ctx context.Context, a *Clause, primary observed, step string) error {
	switch a.Kind {
	case KindResponseField:
		return e.checkAll(primary, a.Checks, KindResponseField, step)

	case KindReadback:
		if a.Call == nil {
			return e.fail(primary, step, KindReadback, "", "a call to read back", "<none>")
		}
		obs, err := e.call(ctx, a.Call, step)
		if err != nil {
			return err
		}
		if err := e.checkAll(obs, a.Checks, KindReadback, step); err != nil {
			return err
		}
		// A clause's exports are applied only once the clause holds: inside an
		// eventually, the failing attempts must not leave a half-read response
		// in the context bag for the next clause to reference.
		return e.applyExports(a.Call, obs, step)

	case KindListContains, KindAbsent:
		return e.assertList(ctx, a, primary, step)

	case KindEventually:
		return e.eventually(ctx, a, primary, step)

	case KindErrorCode:
		// Checked against the primary call in RunTest; a nested one is not
		// representable (eventually wraps only readback/listContains/absent).
		return e.fail(primary, step, KindErrorCode, "", "an errorCode clause on the test's own call", "a nested one")

	default:
		return e.fail(primary, step, a.Kind, "", "one of the IR's assertion kinds", fmt.Sprintf("%q", a.Kind))
	}
}

// assertList evaluates listContains and both forms of absent.
func (e *execution) assertList(ctx context.Context, a *Clause, primary observed, step string) error {
	// absent's error form: the call must fail with the named error.
	if a.Kind == KindAbsent && a.Error != nil {
		if a.Call == nil {
			return e.fail(primary, step, KindAbsent, "", "a call to raise the error", "<none>")
		}
		obs, sdkErr, err := e.callRaw(ctx, a.Call, step)
		if err != nil {
			return err // a ref or params problem, already fully described
		}
		if sdkErr == nil {
			return e.fail(obs, step, KindAbsent, "", acceptedCodes(a.Error), "<no error>")
		}
		if !matchesError(sdkErr, a.Error) {
			return e.fail(obs, step, KindAbsent, "", acceptedCodes(a.Error), quote(sdkErr.Error()))
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

	// walked prefixes the "actual" half of a failure with how far a paginated
	// clause got; a single-call clause has no pages to count.
	walked := func(rest string) string { return rest }
	var list []any
	if a.Page != nil && a.Call != nil {
		var pages int
		var err error
		list, pages, err = e.walkPages(ctx, a, obs, step)
		if err != nil {
			return err
		}
		walked = func(rest string) string {
			return fmt.Sprintf("walked %d page(s), %d item(s): %s", pages, len(list), rest)
		}
		if a.Page.MinPages > pages {
			return e.fail(obs, step, a.Kind, a.ItemsPath,
				fmt.Sprintf("at least %d page(s) at a page size of %d", a.Page.MinPages, a.Page.Size),
				fmt.Sprintf("walked %d page(s), %d item(s)", pages, len(list)))
		}
	} else {
		items, resolved, err := resolvePath(obs.body, a.ItemsPath)
		if err != nil {
			return e.fail(obs, step, a.Kind, a.ItemsPath, "a well-formed items path", quote(err.Error()))
		}
		if resolved {
			l, ok := items.([]any)
			if !ok {
				return e.fail(obs, step, a.Kind, a.ItemsPath, "a list", render(items))
			}
			list = l
		}
		// A missing list counts as empty: several AWS services omit an empty
		// list member rather than serializing [].
	}

	matched, wanted, err := e.matchItem(obs, list, a.Where, a.Kind, step)
	if err != nil {
		return err
	}

	if a.Kind == KindListContains {
		if matched < 0 {
			return e.fail(obs, step, KindListContains, a.ItemsPath,
				fmt.Sprintf("an item matching %s", renderWhereExpected(a.Where, wanted)), walked(renderList(list)))
		}
	} else if matched >= 0 {
		return e.fail(obs, step, KindAbsent, a.ItemsPath,
			fmt.Sprintf("no item matching %s", renderWhereExpected(a.Where, wanted)), walked(render(list[matched])))
	}
	// The clause held. A list clause may carry a call with exports of its own,
	// and they are applied on the same terms as a read-back's: only once the
	// clause holds.
	if a.Call != nil {
		return e.applyExports(a.Call, obs, step)
	}
	return nil
}

// walkPages follows a paginated list clause to its last page and returns the
// items of every page as one list, and how many pages that took. first is
// page one, already fetched; every failure quotes its params, so a message
// names the clause's own call rather than whichever page it stopped on.
//
// A page whose items are missing counts as empty. The walk ends at the first
// page that carries no token — absent, null or "" — and fails the clause,
// rather than looping, once MaxPages pages have all carried one. Each later
// page is the clause's Next call with PageTokenKey set to the token the
// previous page returned.
func (e *execution) walkPages(ctx context.Context, a *Clause, first observed, step string) ([]any, int, error) {
	var all []any
	cur := first
	for pages := 1; ; pages++ {
		items, resolved, err := resolvePath(cur.body, a.ItemsPath)
		if err != nil {
			return nil, pages, e.fail(first, step, a.Kind, a.ItemsPath, "a well-formed items path", quote(err.Error()))
		}
		if resolved {
			l, ok := items.([]any)
			if !ok {
				return nil, pages, e.fail(first, step, a.Kind, a.ItemsPath, "a list",
					fmt.Sprintf("walked %d page(s): %s", pages, render(items)))
			}
			all = append(all, l...)
		}

		tokenPath := "$." + a.Page.OutputToken
		raw, has, err := resolvePath(cur.body, tokenPath)
		if err != nil {
			return nil, pages, e.fail(first, step, a.Kind, tokenPath, "a well-formed token path", quote(err.Error()))
		}
		token := ""
		if has && raw != nil {
			s, ok := raw.(string)
			if !ok {
				return nil, pages, e.fail(first, step, a.Kind, tokenPath, "a string token, or none",
					fmt.Sprintf("walked %d page(s): %s", pages, render(raw)))
			}
			token = s
		}
		if token == "" {
			return all, pages, nil
		}
		if pages >= MaxPages {
			return nil, pages, e.fail(first, step, a.Kind, tokenPath,
				fmt.Sprintf("no %s on a page within %d pages", a.Page.OutputToken, MaxPages),
				fmt.Sprintf("walked %d page(s), %d item(s), and the last still returned a %s", pages, len(all), a.Page.OutputToken))
		}

		e.bag.set(PageTokenKey, token)
		cur, err = e.call(ctx, &a.Page.Next, step)
		if err != nil {
			return nil, pages, err
		}
	}
}

// matchItem returns the index of the first item satisfying every where entry,
// or -1, together with the evaluated expected values so a failure message can
// print them. An unevaluatable where value (an unresolvable ref) is an error
// for the step rather than a non-match.
func (e *execution) matchItem(obs observed, list []any, where []WhereEntry, kind, step string) (int, []any, error) {
	b := e.binder()
	wanted := make([]any, len(where))
	for i, entry := range where {
		want, err := b.eval(entry.Value)
		if err != nil {
			return -1, wanted, e.fail(obs, step, kind, entry.Path, "the where value to evaluate", quote(err.Error()))
		}
		wanted[i] = want
	}

	for i, item := range list {
		all := true
		for j, entry := range where {
			// "$" is the item itself, which is how a list of strings is
			// matched: Where("$", Ref("queue.url")).
			got, ok, err := resolvePath(item, entry.Path)
			if err != nil {
				return -1, wanted, e.fail(obs, step, kind, entry.Path, "a well-formed where path", quote(err.Error()))
			}
			if !ok || !jsonEqual(got, wanted[j]) {
				all = false
				break
			}
		}
		if all {
			return i, wanted, nil
		}
	}
	return -1, wanted, nil
}

// eventually retries the inner clause up to maxAttempts times, waiting delayMs
// between attempts and no longer. The last failure is the reported one, and a
// read-back inside applies its exports only on the attempt that passes — which
// assert already guarantees, because it applies them only when the checks hold.
//
// That last failure is reported behind the budget that was spent on it. Bare,
// it is indistinguishable from a clause evaluated once, and the two want
// opposite fixes: a real disagreement, or a poll budget too short for how long
// this service takes to settle. The three interpreters word the prefix
// identically, so a generated group's give-up reads the same whichever suite
// reports it. It is wrapped rather than interpolated so an inner 501 still
// carries harness.ErrUnimplemented out to the harness.
func (e *execution) eventually(ctx context.Context, a *Clause, primary observed, step string) error {
	if a.Assert == nil {
		return e.fail(primary, step, KindEventually, "", "a clause to retry", "<none>")
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

// wait sleeps between eventually attempts for exactly the delay the IR asks
// for, and returns early if the run is cancelled.
func wait(ctx context.Context, delayMs int) error {
	if delayMs <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(time.Duration(delayMs) * time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// checkAll evaluates every check of a clause against one response, in the
// order the emitter wrote them — which is path order, so a failure message is
// the same on every run.
func (e *execution) checkAll(obs observed, checks []Check, kind, step string) error {
	if !obs.ok {
		return e.fail(obs, step, kind, "", "a response to check", "<no response>")
	}
	for _, c := range checks {
		if err := e.check(obs, c, kind, step); err != nil {
			return err
		}
	}
	return nil
}

// check evaluates one check against one response path.
func (e *execution) check(obs observed, c Check, kind, step string) error {
	got, resolved, err := resolvePath(obs.body, c.Path)
	if err != nil {
		return e.fail(obs, step, kind+" "+string(c.Kind), c.Path, "a well-formed path", quote(err.Error()))
	}
	fail := func(expected string) error {
		return e.fail(obs, step, kind+" "+string(c.Kind), c.Path, expected, renderResolved(got, resolved))
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
		if !resolved || isEmpty(got) {
			return fail("a non-empty value")
		}
		return nil

	case CheckEquals:
		want, err := e.binder().eval(c.Value)
		if err != nil {
			return e.fail(obs, step, kind+" equals", c.Path, "the expected value to evaluate", quote(err.Error()))
		}
		if !resolved || !jsonEqual(got, want) {
			return fail(render(want))
		}
		return nil

	case CheckMatches:
		pattern, ok := c.Value.(string)
		if !ok {
			return fail("a string pattern in the generated source")
		}
		re, err := regexp.Compile(pattern)
		if err != nil {
			// The model states its patterns in RE2, which Go's regexp is, so
			// this is nearly unreachable here — but a pattern the engine will
			// not compile is a normal six-field mismatch in every backend
			// (compat/model/README.md § Assertions), never an exception out of
			// the evaluator, and the phrase is the same in all of them.
			return e.fail(obs, step, kind+" matches", c.Path,
				fmt.Sprintf("pattern %s", pattern), quote("unsupported pattern: "+err.Error()))
		}
		s, isStr := got.(string)
		if !resolved || !isStr || !re.MatchString(s) {
			return fail(fmt.Sprintf("a string matching %q", pattern))
		}
		return nil

	case CheckEndsWith, CheckStartsWith, CheckContains:
		operand, err := e.binder().eval(c.Value)
		if err != nil {
			return e.fail(obs, step, kind+" "+string(c.Kind), c.Path, "the operand to evaluate", quote(err.Error()))
		}
		affix, isStr := operand.(string)
		if !isStr || affix == "" {
			return e.fail(obs, step, kind+" "+string(c.Kind), c.Path,
				"a non-empty string operand", quote("the operand evaluated to "+render(operand)))
		}
		s, gotStr := got.(string)
		how, holds := "", false
		switch c.Kind {
		case CheckEndsWith:
			how, holds = "ending with", resolved && gotStr && strings.HasSuffix(s, affix)
		case CheckStartsWith:
			how, holds = "starting with", resolved && gotStr && strings.HasPrefix(s, affix)
		default:
			how, holds = "containing", resolved && gotStr && strings.Contains(s, affix)
		}
		if !holds {
			return fail(fmt.Sprintf("a string %s %q", how, affix))
		}
		return nil

	case CheckEqualsJSON:
		if c.operandErr != nil {
			return e.fail(obs, step, kind+" equalsJSON", c.Path,
				"a JSON object or array operand in the generated source", quote(c.operandErr.Error()))
		}
		if holds, actual := equalsJSON(got, resolved, c.Value); !holds {
			return e.fail(obs, step, kind+" equalsJSON", c.Path, "equalsJSON "+render(c.Value), actual)
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
