//go:build dev

package main

import (
	"encoding/json"
	"github.com/overcast-sh/overcast/internal/awsmodel"
	"path/filepath"
	"strings"
	"testing"
)

// The `paginate` field of a list clause (compat/model/README.md § Paginating a
// list): the members come from the operation's @paginated trait and nowhere
// else, a recipe is refused where the model cannot answer, and an authored file
// is held to the trait because it is never rewritten.

func pagedListClause(op string, params map[string]any, p paginate) assertion {
	return paginated(listContains(&call{Op: op, Params: params}, "$.Widgets",
		map[string]any{"$.WidgetId": map[string]any{"$ref": "widget.id"}}), p)
}

func TestPagination_aRecipeClauseIsFilledInFromTheTrait(t *testing.T) {
	// Given: the fixture recipe, whose PolishWidgetListed clause gives only a
	// page size and a floor.
	// When: it is generated.
	_, gen := generateFixture(t)

	// Then: the generated clause names the trait's members, and the call is
	// unchanged — the page size and the token are added by the walk, not stored
	// in the call.
	_, tc, ok := gen.scenario.findTest("widgets-gen-widget", "PolishWidgetListed")
	if !ok {
		t.Fatal("the paginated authored operation produced no test")
	}
	clause := tc.Assert[0]
	want := paginate{PageSize: 1, MinPages: 1, InputToken: "NextToken", OutputToken: "NextToken", PageSizeMember: "MaxResults"}
	if clause.Paginate == nil || *clause.Paginate != want {
		t.Fatalf("paginate = %+v, want %+v", clause.Paginate, want)
	}
	if _, set := clause.Call.Params["MaxResults"]; set {
		t.Errorf("the call carries MaxResults: %v", clause.Call.Params)
	}
}

func TestPagination_pageCallsAddThePageSizeAndTheTokenRef(t *testing.T) {
	// Given: a paginated clause.
	a := pagedListClause("ListWidgets", map[string]any{"NamePrefix": "x"},
		paginate{PageSize: 7, InputToken: "NextToken", OutputToken: "NextToken", PageSizeMember: "MaxResults"})

	// When: its two calls are built.
	first, next := pageCalls(a)

	// Then: page one has the size and no token, every later page has both, and
	// the clause's own call is untouched.
	if first.Params["MaxResults"] != json.Number("7") || first.Params["NextToken"] != nil {
		t.Errorf("first = %v", first.Params)
	}
	token, _ := next.Params["NextToken"].(map[string]any)
	if next.Params["MaxResults"] != json.Number("7") || token["$ref"] != pageTokenRef {
		t.Errorf("next = %v", next.Params)
	}
	if len(a.Call.Params) != 1 {
		t.Errorf("the clause's call was modified: %v", a.Call.Params)
	}
	if !strings.HasPrefix(pageTokenRef, "$") || validContextPath(pageTokenRef) {
		t.Errorf("%q is a valid context path, so a scenario could export over it", pageTokenRef)
	}
}

func TestPagination_everyBackendSeesBothPageCalls(t *testing.T) {
	// Given: a group with one paginated clause.
	_, gen := generateFixture(t)
	g, _, _ := gen.scenario.findTest("widgets-gen-widget", "PolishWidgetListed")

	// When: its calls are collected for the refusal checks and the imports.
	var seen []map[string]any
	for _, c := range callsOf(*g) {
		if c.Op == "ListWidgets" {
			seen = append(seen, c.Params)
		}
	}

	// Then: both pages are there, so a member the pinned SDK lacks refuses the
	// group instead of failing the emitter or the compiler. The derived calls
	// to ListWidgets carry neither member.
	var pageOne, later int
	for _, p := range seen {
		_, size := p["MaxResults"]
		_, token := p["NextToken"]
		switch {
		case size && token:
			later++
		case size:
			pageOne++
		}
	}
	if pageOne != 1 || later != 1 {
		t.Errorf("ListWidgets calls = %v, want one page-one call and one later-page call", seen)
	}
}

func TestPagination_aRecipeIsRefusedWhereTheModelCannotAnswer(t *testing.T) {
	tests := []struct {
		name       string
		listOp     string
		wantReason string
	}{
		{"an operation with no @paginated trait", "ListWidgetsAndCogs", reasonNotPaginated},
		{"a trait with no page size", "ListGauges", reasonPaginatedIncomplete + ":pageSize"},
		{"a trait with no page size, but an items member", "ScanWidgets", reasonPaginatedIncomplete + ":pageSize"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Given: the widget resource with one more authored operation whose
			// clause paginates the list operation under test.
			f := loadFixture(t)
			widget := f.resourceNamed(t, "widget")
			extra := authoredOp{
				Op: "PolishWidget", Name: "PolishWidgetPaged",
				Assert: []assertion{pagedListClause(tc.listOp, nil, paginate{PageSize: 1})},
			}
			widget.Operations = append(append([]authoredOp(nil), widget.Operations...), extra)
			r := f.recipe
			r.Resources = []resource{widget}

			// When: it is generated.
			gen, err := generate(f.model, r, f.values, fixtureCaps(), fixtureClient)
			if err != nil {
				t.Fatal(err)
			}

			// Then: the test is refused with a named reason, not guessed at and
			// not emitted.
			got := gapIn(gen, "widgets-gen-widget", "PolishWidget")
			if got.Reason != tc.wantReason {
				t.Fatalf("refusal = %q (%s), want %s", got.Reason, got.Detail, tc.wantReason)
			}
			if _, _, found := gen.scenario.findTest("widgets-gen-widget", "PolishWidgetPaged"); found {
				t.Error("the refused test was emitted anyway")
			}
		})
	}
}

func TestPagination_aRecipeMayNotNameTheMembersOrSetThem(t *testing.T) {
	for name, clause := range map[string]assertion{
		"names the members": pagedListClause("ListWidgets", nil,
			paginate{PageSize: 1, InputToken: "NextToken", OutputToken: "NextToken", PageSizeMember: "MaxResults"}),
		"sets the page size itself": pagedListClause("ListWidgets", map[string]any{"MaxResults": json.Number("5")},
			paginate{PageSize: 1}),
		"sets the token itself": pagedListClause("ListWidgets", map[string]any{"NextToken": "x"},
			paginate{PageSize: 1}),
	} {
		t.Run(name, func(t *testing.T) {
			f := loadFixture(t)
			widget := f.resourceNamed(t, "widget")
			widget.Operations = append(append([]authoredOp(nil), widget.Operations...),
				authoredOp{Op: "PolishWidget", Name: "PolishWidgetPaged", Assert: []assertion{clause}})
			r := f.recipe
			r.Resources = []resource{widget}
			if _, err := generate(f.model, r, f.values, fixtureCaps(), fixtureClient); err == nil {
				t.Fatal("generation accepted it")
			}
		})
	}
}

func TestResolvePagination_refusesATokenAWalkCannotUse(t *testing.T) {
	// Given: SQS's ListQueues, which paginates cleanly, with its trait edited
	// one way at a time.
	model, err := loadModel(filepath.Join("..", "..", "models", "aws", "shapes"), "sqs")
	if err != nil {
		t.Fatal(err)
	}
	members, ref := resolvePagination(model, "ListQueues")
	if ref != nil {
		t.Fatalf("ListQueues refused: %+v", ref)
	}
	if want := (paginationMembers{InputToken: "NextToken", OutputToken: "NextToken", PageSizeMember: "MaxResults"}); members != want {
		t.Fatalf("members = %+v, want %+v", members, want)
	}

	for name, tc := range map[string]struct {
		trait      string
		wantReason string
	}{
		"a path rather than a member":      {`{"inputToken":"NextToken","outputToken":"Page.NextToken","items":"QueueUrls","pageSize":"MaxResults"}`, reasonUnsupportedPageToken + ":outputToken"},
		"a member the shape does not have": {`{"inputToken":"Cursor","outputToken":"NextToken","items":"QueueUrls","pageSize":"MaxResults"}`, reasonUnsupportedPageToken + ":inputToken"},
		"a page size that is not a number": {`{"inputToken":"NextToken","outputToken":"NextToken","items":"QueueUrls","pageSize":"QueueNamePrefix"}`, reasonUnsupportedPageToken + ":pageSize"},
		"a token that is not a string":     {`{"inputToken":"NextToken","outputToken":"QueueUrls","items":"QueueUrls","pageSize":"MaxResults"}`, reasonUnsupportedPageToken + ":outputToken"},
		"a trait with no input token":      {`{"outputToken":"NextToken","items":"QueueUrls","pageSize":"MaxResults"}`, reasonPaginatedIncomplete + ":inputToken"},
		"a trait with no output token":     {`{"inputToken":"NextToken","items":"QueueUrls","pageSize":"MaxResults"}`, reasonPaginatedIncomplete + ":outputToken"},
		"a trait with an empty page size":  {`{"inputToken":"NextToken","outputToken":"NextToken","items":"QueueUrls"}`, reasonPaginatedIncomplete + ":pageSize"},
	} {
		t.Run(name, func(t *testing.T) {
			edited := *model
			edited.Shapes = make(map[string]awsmodel.SnapshotShape, len(model.Shapes))
			for k, v := range model.Shapes {
				edited.Shapes[k] = v
			}
			shape := edited.Shapes["ListQueues"]
			traits := make(map[string]json.RawMessage, len(shape.Traits))
			for k, v := range shape.Traits {
				traits[k] = v
			}
			traits["smithy.api#paginated"] = json.RawMessage(tc.trait)
			shape.Traits = traits
			edited.Shapes["ListQueues"] = shape

			_, ref := resolvePagination(&edited, "ListQueues")
			if ref == nil || ref.Reason != tc.wantReason {
				t.Fatalf("refusal = %+v, want %s", ref, tc.wantReason)
			}
		})
	}

	// And: an operation with no trait at all.
	if _, ref := resolvePagination(model, "GetQueueUrl"); ref == nil || ref.Reason != reasonNotPaginated {
		t.Errorf("GetQueueUrl refusal = %+v, want %s", ref, reasonNotPaginated)
	}
}

func TestPagination_anAuthoredClauseIsHeldToTheTrait(t *testing.T) {
	model, err := loadModel(filepath.Join("..", "..", "models", "aws", "shapes"), "sqs")
	if err != nil {
		t.Fatal(err)
	}
	right := paginate{PageSize: 1, MinPages: 2, InputToken: "NextToken", OutputToken: "NextToken", PageSizeMember: "MaxResults"}
	clause := func(op string, p paginate) assertion {
		return paginated(listContains(&call{Op: op, Params: map[string]any{}}, "$.QueueUrls", map[string]any{"$": "x"}), p)
	}

	if err := checkAuthoredAssertion(model, clause("ListQueues", right)); err != nil {
		t.Fatalf("a clause naming the trait's members was refused: %v", err)
	}
	// Inside an eventually, as everywhere.
	if err := checkAuthoredAssertion(model, eventually(clause("ListQueues", right), retrySpec{MaxAttempts: 2})); err != nil {
		t.Fatalf("a paginated clause inside eventually was refused: %v", err)
	}

	wrong := right
	wrong.OutputToken = "Marker"
	err = checkAuthoredAssertion(model, clause("ListQueues", wrong))
	if err == nil || !strings.Contains(err.Error(), `outputToken "Marker"`) || !strings.Contains(err.Error(), "@paginated trait says") {
		t.Errorf("a clause naming another token: err = %v, want one saying what the trait says", err)
	}
	err = checkAuthoredAssertion(model, clause("GetQueueUrl", right))
	if err == nil || !strings.Contains(err.Error(), reasonNotPaginated) {
		t.Errorf("a clause on an operation with no trait: err = %v, want %s", err, reasonNotPaginated)
	}
}

func TestPagination_theIRRefusesWhatAWalkCannotRun(t *testing.T) {
	complete := paginate{PageSize: 1, InputToken: "NextToken", OutputToken: "NextToken", PageSizeMember: "MaxResults"}
	list := func(p paginate) assertion {
		return paginated(listContains(&call{Op: "ListQueues", Params: map[string]any{}}, "$.QueueUrls", map[string]any{"$": "x"}), p)
	}
	valid := list(complete)
	if err := validateAssertion(valid); err != nil {
		t.Fatalf("a valid paginated clause: %v", err)
	}

	for name, tc := range map[string]struct {
		a    assertion
		want string
	}{
		"no call": {
			a:    paginated(listContains(nil, "$.QueueUrls", map[string]any{"$": "x"}), complete),
			want: "needs a call of its own",
		},
		"an export": {
			a: paginated(listContains(&call{Op: "ListQueues", Params: map[string]any{}, Export: map[string]string{"a.b": "$.QueueUrls"}},
				"$.QueueUrls", map[string]any{"$": "x"}), complete),
			want: "cannot export",
		},
		"page size zero": {a: list(paginate{PageSize: 0}), want: "pageSize 0"},
		"min pages past the cap": {
			a:    list(paginate{PageSize: 1, MinPages: maxPages + 1}),
			want: "minPages",
		},
		"the call sets the page size": {
			a: paginated(listContains(&call{Op: "ListQueues", Params: map[string]any{"MaxResults": json.Number("3")}},
				"$.QueueUrls", map[string]any{"$": "x"}), complete),
			want: "must not set it",
		},
		"a readback": {
			a:    paginated(readback(call{Op: "ListQueues"}, checks("$.QueueUrls", isList())), complete),
			want: "cannot paginate",
		},
		"the error form of absent": {
			a:    paginated(absentByError(call{Op: "ListQueues", Params: map[string]any{}}, errorSpec{Shape: "S", Code: "C"}), complete),
			want: "list form of absent",
		},
	} {
		t.Run(name, func(t *testing.T) {
			err := validateAssertion(tc.a)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want one containing %q", err, tc.want)
			}
		})
	}

	// A finished scenario names all three members; a recipe's may leave them
	// for the generator.
	if err := validatePaginateComplete(list(paginate{PageSize: 1})); err == nil {
		t.Error("a finished scenario with no member names was accepted")
	}
	// A probe group's tests run concurrently and share a context.
	probe := &scenario{Version: scenarioVersion, Service: "sqs", Groups: []group{{
		Name: "g", Kind: groupProbe, Parallel: true,
		Tests: []test{newTest("T", "ListQueues", call{Op: "ListQueues", Params: map[string]any{}}, valid)},
	}}}
	if err := validateScenario(probe); err == nil || !strings.Contains(err.Error(), "cannot paginate") {
		t.Errorf("a paginated clause in a probe group: err = %v", err)
	}
}

func TestPagination_theSchemaRequiresTheMembersInAFinishedScenario(t *testing.T) {
	// Given: the fixture's generated scenario, which carries a paginated
	// clause.
	f, gen := generateFixture(t)
	contents, err := json.Marshal(gen.scenario)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(contents), `"paginate":{"pageSize":1,"minPages":1,"inputToken":"NextToken","outputToken":"NextToken","pageSizeMember":"MaxResults"}`) {
		t.Fatalf("the scenario does not carry the expected paginate:\n%s", contents)
	}

	// When/Then: it validates, and loses that validity when a member goes.
	if err := f.schemas.validate(schemaScenario, contents); err != nil {
		t.Fatalf("the generated scenario does not validate: %v", err)
	}
	for _, drop := range []string{`"inputToken":"NextToken",`, `"outputToken":"NextToken",`, `"pageSizeMember":"MaxResults"`} {
		broken := strings.Replace(string(contents), drop, "", 1)
		if broken == string(contents) {
			t.Fatalf("%s is not in the scenario", drop)
		}
		if err := f.schemas.validate(schemaScenario, []byte(broken)); err == nil {
			t.Errorf("a scenario without %s validated", drop)
		}
	}
}

func TestPagination_explainShowsTheWalk(t *testing.T) {
	// Given: the fixture's paginated test.
	_, gen := generateFixture(t)
	g, tc, _ := gen.scenario.findTest("widgets-gen-widget", "PolishWidgetListed")

	// When: it is explained in the interpreters' pseudo-code.
	out := renderPython(renderEnv{}, gen.scenario, g, tc)

	// Then: a reader can reproduce it by hand — the page size, the token member
	// to feed back, and the cap are all there.
	for _, want := range []string{"paginated: page size 1 in MaxResults", "send each page's NextToken back as NextToken", "at most 50 pages", "at least 1 pages come back"} {
		if !strings.Contains(out, want) {
			t.Errorf("explain output lacks %q:\n%s", want, out)
		}
	}
}
