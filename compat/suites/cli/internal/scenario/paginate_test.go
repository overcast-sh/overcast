package scenario

import (
	"fmt"
	"strings"
	"testing"
)

// The paginated list clause (compat/model/README.md § Paginating a list): a
// walk to the last page, a match against the union of the pages, a floor on how
// many pages came back, and a cap so a service that never stops fails instead
// of hanging.

// pagedClause is a list clause that walks ListThings a page at a time.
func pagedClause(kind string, extra obj) obj {
	clause := obj{
		"kind":      kind,
		"call":      obj{"op": "ListThings", "params": obj{"Prefix": "oc-"}},
		"itemsPath": "$.Things",
		"where":     obj{"$": "t-want"},
		"paginate": obj{
			"pageSize": float64(1), "inputToken": "NextToken", "outputToken": "NextToken",
			"pageSizeMember": "MaxResults",
		},
	}
	for k, v := range extra {
		clause[k] = v
	}
	return clause
}

func thingsPage(things []any, token any) fakeResult {
	body := obj{}
	if things != nil {
		body["Things"] = things
	}
	if token != nil {
		body["NextToken"] = token
	}
	return ok(body)
}

func pagedCases(clause obj, answers ...fakeResult) clauseCase {
	return clauseCase{clause: clause, script: map[string][]fakeResult{
		"get-thing":   {ok(obj{})},
		"list-things": answers,
	}}
}

func TestPaginatedClauseWalksEveryPageFeedingTheTokenBack(t *testing.T) {
	b, fake, rg := fixture(t, scenarioFile(lifecycle("widgets-gen-thing", obj{
		"name": "GetThing", "op": "GetThing",
		"call":   obj{"op": "GetThing", "params": obj{}},
		"assert": []any{pagedClause("listContains", nil)},
	})))
	fake.script["get-thing"] = []fakeResult{ok(obj{})}
	fake.script["list-things"] = []fakeResult{
		thingsPage([]any{"t-a"}, "tok1"), thingsPage([]any{"t-b"}, "tok2"), thingsPage([]any{"t-want"}, nil),
	}
	if err := runOneTest(t, b, rg, "GetThing"); err != nil {
		t.Fatalf("test failed: %v", err)
	}
	var sent []string
	for _, c := range fake.calls {
		if c.op == "list-things" {
			sent = append(sent, c.input)
			if len(c.extra) != 1 || c.extra[0] != "--no-paginate" {
				t.Errorf("list-things args after the input = %v, want [--no-paginate]: the CLI would otherwise merge the pages itself", c.extra)
			}
		}
	}
	want := []string{
		`{"MaxResults":1,"Prefix":"oc-"}`,
		`{"MaxResults":1,"NextToken":"tok1","Prefix":"oc-"}`,
		`{"MaxResults":1,"NextToken":"tok2","Prefix":"oc-"}`,
	}
	if strings.Join(sent, "\n") != strings.Join(want, "\n") {
		t.Errorf("list-things inputs =\n%s\nwant\n%s", strings.Join(sent, "\n"), strings.Join(want, "\n"))
	}
}

func TestPaginatedClauseOutcomes(t *testing.T) {
	minPages := func(n int) obj {
		p := pagedClause("listContains", nil)["paginate"].(obj)
		p["minPages"] = float64(n)
		return obj{"paginate": p}
	}
	runClauseCases(t, []clauseCase{
		func() clauseCase {
			c := pagedCases(pagedClause("listContains", nil), thingsPage([]any{"t-a"}, "tok"), thingsPage([]any{"t-want"}, nil))
			c.name = "an item on a later page is found"
			return c
		}(),
		func() clauseCase {
			c := pagedCases(pagedClause("listContains", nil), thingsPage([]any{"t-want"}, ""))
			c.name = "an empty token ends the walk"
			return c
		}(),
		func() clauseCase {
			c := pagedCases(pagedClause("listContains", nil), thingsPage([]any{"t-want"}, nil))
			c.script["list-things"][0].body["NextToken"] = nil
			c.name = "a null token ends the walk"
			return c
		}(),
		func() clauseCase {
			c := pagedCases(pagedClause("listContains", nil), thingsPage(nil, "tok"), thingsPage([]any{"t-want"}, nil))
			c.name = "a page without the list counts as empty"
			return c
		}(),
		func() clauseCase {
			c := pagedCases(pagedClause("listContains", nil), thingsPage([]any{"t-a"}, "t1"), thingsPage([]any{"t-b"}, "t2"), thingsPage([]any{"t-c"}, nil))
			c.name = "a miss says how many pages were walked and what was found"
			c.wantErr = `walked 3 page(s), 3 item(s): ["t-a","t-b","t-c"]`
			return c
		}(),
		func() clauseCase {
			c := pagedCases(pagedClause("listContains", nil), thingsPage([]any{}, nil))
			c.name = "an empty list still fails listContains"
			c.wantErr = "walked 1 page(s), 0 item(s)"
			return c
		}(),
		func() clauseCase {
			c := pagedCases(pagedClause("listContains", minPages(2)), thingsPage([]any{"t-want"}, nil))
			c.name = "minPages fails a single page that holds everything"
			c.wantErr = "at least 2 page(s) at a page size of 1"
			return c
		}(),
		func() clauseCase {
			c := pagedCases(pagedClause("listContains", minPages(2)), thingsPage([]any{"t-a"}, "tok"), thingsPage([]any{"t-want"}, nil))
			c.name = "minPages passes when the service paginated"
			return c
		}(),
		func() clauseCase {
			c := pagedCases(pagedClause("absent", nil), thingsPage([]any{"t-a"}, "tok"), thingsPage([]any{"t-b"}, nil))
			c.name = "absent passes when no page holds the item"
			return c
		}(),
		func() clauseCase {
			c := pagedCases(pagedClause("absent", nil), thingsPage([]any{"t-a"}, "tok"), thingsPage([]any{"t-want"}, nil))
			c.name = "absent fails on an item on a later page"
			c.wantErr = "walked 2 page(s), 2 item(s): "
			return c
		}(),
		func() clauseCase {
			c := pagedCases(pagedClause("listContains", nil), thingsPage([]any{"t-want"}, float64(7)))
			c.name = "a non-string token fails the clause"
			c.wantErr = "a string token, or none"
			return c
		}(),
		func() clauseCase {
			c := pagedCases(pagedClause("listContains", nil), thingsPage([]any{"t-a"}, "tok1"), fakeResult{err: fmt.Errorf("boom")})
			c.name = "a call failing on a later page is a call failure"
			c.wantErr = `"NextToken":"tok1"`
			return c
		}(),
		func() clauseCase {
			c := pagedCases(pagedClause("listContains", nil), thingsPage([]any{"t-a"}, "again"))
			c.name = "a service that never stops fails at the page cap"
			c.wantErr = fmt.Sprintf("walked %d page(s), %d item(s), and the last still returned a NextToken", maxPages, maxPages)
			return c
		}(),
	})
}

func TestPaginatedClauseStopsAtThePageCap(t *testing.T) {
	b, fake, rg := fixture(t, scenarioFile(lifecycle("widgets-gen-thing", obj{
		"name": "GetThing", "op": "GetThing",
		"call":   obj{"op": "GetThing", "params": obj{}},
		"assert": []any{pagedClause("listContains", nil)},
	})))
	fake.script["get-thing"] = []fakeResult{ok(obj{})}
	fake.script["list-things"] = []fakeResult{thingsPage([]any{"t-a"}, "again")}
	if err := runOneTest(t, b, rg, "GetThing"); err == nil {
		t.Fatal("a service that always returns a token passed")
	}
	n := 0
	for _, c := range fake.calls {
		if c.op == "list-things" {
			n++
		}
	}
	if n != maxPages {
		t.Errorf("list-things was called %d times, want exactly %d", n, maxPages)
	}
}

func TestPaginatedClauseQuotesPageOnesParamsAndEventuallyWalksAgain(t *testing.T) {
	// A miss quotes page one's params, not the page it stopped on.
	b, fake, rg := fixture(t, scenarioFile(lifecycle("widgets-gen-thing", obj{
		"name": "GetThing", "op": "GetThing",
		"call":   obj{"op": "GetThing", "params": obj{}},
		"assert": []any{pagedClause("listContains", nil)},
	})))
	fake.script["get-thing"] = []fakeResult{ok(obj{})}
	fake.script["list-things"] = []fakeResult{thingsPage([]any{"t-a"}, "tok1"), thingsPage([]any{"t-b"}, nil)}
	err := runOneTest(t, b, rg, "GetThing")
	if err == nil {
		t.Fatal("a miss passed")
	}
	if !strings.Contains(err.Error(), `{"MaxResults":1,"Prefix":"oc-"}`) || strings.Contains(strings.Split(err.Error(), "expected")[0], "NextToken") {
		t.Errorf("failure %v does not quote page one's params", err)
	}

	// Inside an eventually every attempt walks again from the first page.
	b, fake, rg = fixture(t, scenarioFile(lifecycle("widgets-gen-thing", obj{
		"name": "GetThing", "op": "GetThing",
		"call": obj{"op": "GetThing", "params": obj{}},
		"assert": []any{obj{
			"kind": "eventually", "maxAttempts": float64(3), "delayMs": float64(0),
			"assert": pagedClause("listContains", nil),
		}},
	})))
	fake.script["get-thing"] = []fakeResult{ok(obj{})}
	fake.script["list-things"] = []fakeResult{
		thingsPage([]any{"t-a"}, "tok1"), thingsPage([]any{"t-b"}, nil),
		thingsPage([]any{"t-a"}, "tok1"), thingsPage([]any{"t-want"}, nil),
	}
	if err := runOneTest(t, b, rg, "GetThing"); err != nil {
		t.Fatalf("eventually did not retry the walk: %v", err)
	}
	var inputs []string
	for _, c := range fake.calls {
		if c.op == "list-things" {
			inputs = append(inputs, c.input)
		}
	}
	if len(inputs) != 4 || strings.Contains(inputs[2], "NextToken") {
		t.Errorf("inputs = %v, want four, the third starting over without a token", inputs)
	}
}

func TestLoaderRefusesAPaginateTheWalkCannotRun(t *testing.T) {
	for name, clause := range map[string]obj{
		"no call":       {"kind": "listContains", "itemsPath": "$.Things", "where": obj{"$": "x"}, "paginate": pagedClause("listContains", nil)["paginate"]},
		"no page size":  pagedClause("listContains", obj{"paginate": obj{"inputToken": "NextToken", "outputToken": "NextToken", "pageSizeMember": "MaxResults"}}),
		"no token name": pagedClause("listContains", obj{"paginate": obj{"pageSize": float64(1), "pageSizeMember": "MaxResults"}}),
	} {
		t.Run(name, func(t *testing.T) {
			b, _, rg := fixture(t, scenarioFile(lifecycle("widgets-gen-thing", obj{
				"name": "GetThing", "op": "GetThing",
				"call":   obj{"op": "GetThing", "params": obj{}},
				"assert": []any{clause},
			})))
			err := runOneTest(t, b, rg, "GetThing")
			if err == nil || !strings.Contains(err.Error(), "paginate needs") {
				t.Fatalf("err = %v, want a load error naming paginate", err)
			}
		})
	}
}
