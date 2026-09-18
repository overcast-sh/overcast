package stepfunctions

import (
	"encoding/json"
	"strings"
	"testing"
)

// storeDoc is the Jayway JsonPath README document, the reference most JSONPath
// examples (and AWS's own) are written against.
const storeDoc = `{
  "store": {
    "book": [
      {"category": "reference", "author": "Nigel Rees", "title": "Sayings of the Century", "price": 8.95},
      {"category": "fiction", "author": "Evelyn Waugh", "title": "Sword of Honour", "price": 12.99},
      {"category": "fiction", "author": "Herman Melville", "title": "Moby Dick", "isbn": "0-553-21311-3", "price": 8.99},
      {"category": "fiction", "author": "J. R. R. Tolkien", "title": "The Lord of the Rings", "isbn": "0-395-19395-8", "price": 22.99}
    ],
    "bicycle": {"color": "red", "price": 19.95}
  },
  "expensive": 10
}`

func assertSelects(t *testing.T, doc any, ctxObj map[string]any, path, want string) {
	t.Helper()
	got, err := selectPath(doc, ctxObj, path)
	if err != nil {
		t.Fatalf("selectPath(%q): %v", path, err)
	}
	encoded, _ := json.Marshal(got)
	if string(encoded) != want {
		t.Fatalf("selectPath(%q) = %s, want %s", path, encoded, want)
	}
}

func TestSelectPath_jsonPathFeatures(t *testing.T) {
	// Given: the Jayway store document
	doc := decodeTestJSON(t, storeDoc)

	cases := []struct {
		name string
		path string
		want string
	}{
		{"authors via wildcard", "$.store.book[*].author", `["Nigel Rees","Evelyn Waugh","Herman Melville","J. R. R. Tolkien"]`},
		{"authors via descent", "$..author", `["Nigel Rees","Evelyn Waugh","Herman Melville","J. R. R. Tolkien"]`},
		{"object wildcard iterates sorted keys", "$.store.*", `[{"color":"red","price":19.95},[{"author":"Nigel Rees","category":"reference","price":8.95,"title":"Sayings of the Century"},{"author":"Evelyn Waugh","category":"fiction","price":12.99,"title":"Sword of Honour"},{"author":"Herman Melville","category":"fiction","isbn":"0-553-21311-3","price":8.99,"title":"Moby Dick"},{"author":"J. R. R. Tolkien","category":"fiction","isbn":"0-395-19395-8","price":22.99,"title":"The Lord of the Rings"}]]`},
		{"prices under store via descent", "$.store..price", `[19.95,8.95,12.99,8.99,22.99]`},
		{"descent then index", "$..book[2].title", `["Moby Dick"]`},
		{"descent bracket index", "$..book[-1].title", `["The Lord of the Rings"]`},
		{"negative index is definite", "$.store.book[-1].title", `"The Lord of the Rings"`},
		{"index union", "$.store.book[0,1].title", `["Sayings of the Century","Sword of Honour"]`},
		{"name union", "$.store.bicycle['color','price']", `["red",19.95]`},
		{"slice start:end", "$.store.book[:2].price", `[8.95,12.99]`},
		{"slice from", "$.store.book[2:].price", `[8.99,22.99]`},
		{"slice negative", "$.store.book[-2:].price", `[8.99,22.99]`},
		{"slice with step", "$.store.book[0:4:2].price", `[8.95,8.99]`},
		{"slice with negative step", "$.store.book[::-1].price", `[22.99,8.99,12.99,8.95]`},
		{"slice out of range clamps", "$.store.book[1:99].price", `[12.99,8.99,22.99]`},
		{"filter existence", "$.store.book[?(@.isbn)].title", `["Moby Dick","The Lord of the Rings"]`},
		{"filter negated existence", "$.store.book[?(!@.isbn)].title", `["Sayings of the Century","Sword of Honour"]`},
		{"filter numeric <", "$.store.book[?(@.price < 10)].title", `["Sayings of the Century","Moby Dick"]`},
		{"filter <=", "$.store.book[?(@.price <= 8.99)].price", `[8.95,8.99]`},
		{"filter >", "$.store.book[?(@.price > 20)].title", `["The Lord of the Rings"]`},
		{"filter >=", "$.store.book[?(@.price >= 12.99)].price", `[12.99,22.99]`},
		{"filter string ==", "$.store.book[?(@.category == 'reference')].author", `["Nigel Rees"]`},
		{"filter double-quoted string", `$.store.book[?(@.category == "reference")].author`, `["Nigel Rees"]`},
		{"filter !=", "$.store.book[?(@.category != 'fiction')].author", `["Nigel Rees"]`},
		{"filter against root path", "$.store.book[?(@.price <= $.expensive)].price", `[8.95,8.99]`},
		{"filter &&", "$.store.book[?(@.category == 'fiction' && @.price < 10)].title", `["Moby Dick"]`},
		{"filter ||", "$.store.book[?(@.price < 9 || @.price > 20)].price", `[8.95,8.99,22.99]`},
		{"filter parentheses", "$.store.book[?((@.price < 9 || @.price > 20) && @.isbn)].price", `[8.99,22.99]`},
		{"filter under descent", "$..book[?(@.author == 'Herman Melville')].price", `[8.99]`},
		{"filter no match is empty array", "$.store.book[?(@.price > 1000)]", `[]`},
		{"wildcard on missing is empty array", "$.nothing[*]", `[]`},
		{"descent no match is empty array", "$..nothing", `[]`},
		{"descent into indexes", "$..[0].author", `["Nigel Rees"]`},
		{"bracket wildcard on object", "$.store.bicycle[*]", `["red",19.95]`},
		{"length of wildcard via path on scalars is empty", "$.expensive[*]", `[]`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// When / Then: the path yields Jayway's result
			assertSelects(t, doc, nil, tc.path, tc.want)
		})
	}
}

func TestSelectPath_filterLiterals(t *testing.T) {
	// Given: items carrying every JSON scalar type
	doc := decodeTestJSON(t, `{"items":[
	  {"id":1,"flag":true,"v":null},
	  {"id":2,"flag":false,"v":"x"},
	  {"id":3,"other":1}
	]}`)

	cases := []struct {
		name string
		path string
		want string
	}{
		{"true literal", "$.items[?(@.flag == true)].id", `[1]`},
		{"false literal", "$.items[?(@.flag == false)].id", `[2]`},
		{"null literal", "$.items[?(@.v == null)].id", `[1]`},
		{"existence of a false value", "$.items[?(@.flag)].id", `[1,2]`},
		{"missing field is never equal", "$.items[?(@.flag == 'x')].id", `[]`},
		{"missing field is not-equal", "$.items[?(@.v != null)].id", `[2,3]`},
		{"compare two relative paths", "$.items[?(@.id == @.other)].id", `[]`},
		{"type mismatch is not equal", "$.items[?(@.id == '1')].id", `[]`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertSelects(t, doc, nil, tc.path, tc.want)
		})
	}
}

func TestSelectPath_definiteMissingStillErrors(t *testing.T) {
	// Given: a document
	doc := decodeTestJSON(t, `{"a":{"b":[1,2]}}`)

	for _, path := range []string{"$.a.c", "$.a.b[5]", "$.a.b[-3]", "$.a.b.c", "$['x']"} {
		// When: a definite path does not resolve
		_, err := selectPath(doc, nil, path)

		// Then: it is an error, as before
		if err == nil {
			t.Errorf("selectPath(%q): expected an error", path)
		}
	}
}

func TestSelectPath_malformedPaths(t *testing.T) {
	// Given: a document
	doc := decodeTestJSON(t, `{"a":[1]}`)

	for _, path := range []string{"", "a", "$.", "$.a[", "$.a[?(@.x ==)]", "$.a[1:2:0]", "$.a[x]", "$.a[?(@.x == 'y)]", "$..", "$.a b["} {
		// When: the path is not valid JSONPath
		_, err := selectPath(doc, nil, path)

		// Then: it is rejected
		if err == nil {
			t.Errorf("selectPath(%q): expected an error", path)
		}
	}
}

func TestIsDefinitePath(t *testing.T) {
	cases := map[string]bool{
		"$":                    true,
		"$$":                   true,
		"$.a.b[0]":             true,
		"$['a'][-1]":           true,
		"$$.Execution.Input":   true,
		"$var.x":               true,
		"$.a[*]":               false,
		"$.*":                  false,
		"$..a":                 false,
		"$.a[0,1]":             false,
		"$.a['x','y']":         false,
		"$.a[1:]":              false,
		"$.a[?(@.x)]":          false,
		"not a path":           false,
		"$.a[":                 false,
		"$.a[?(@.x == 'y')].b": false,
	}
	for path, want := range cases {
		// When / Then: definiteness matches Jayway's notion
		if got := isDefinitePath(path); got != want {
			t.Errorf("isDefinitePath(%q) = %v, want %v", path, got, want)
		}
	}
}

func TestInsertPath_rejectsIndefinitePaths(t *testing.T) {
	// Given: a document
	doc := decodeTestJSON(t, `{"a":[{"b":1}]}`)

	for _, path := range []string{"$.a[*].b", "$..b", "$.a[?(@.b)]", "$.a[0:1]"} {
		// When: we try to write through an indefinite path
		_, err := insertPath(doc, path, "v")

		// Then: it is refused
		if err == nil {
			t.Errorf("insertPath(%q): expected an error", path)
		}
	}
}

// ─── Variables ────────────────────────────────────────────────────────────────

func variablesCtx(vars map[string]any) map[string]any {
	return map[string]any{
		"Execution":         map[string]any{"Name": "run-1"},
		variablesContextKey: vars,
	}
}

func TestSelectPath_variableRoots(t *testing.T) {
	// Given: a context carrying workflow variables
	doc := decodeTestJSON(t, `{"input":true}`)
	vars := decodeTestJSON(t, `{"order":{"items":[{"sku":"a"},{"sku":"b"}]},"count":3,"_x1":"u"}`).(map[string]any)
	ctxObj := variablesCtx(vars)

	cases := []struct {
		name string
		path string
		want string
	}{
		{"bare variable", "$count", `3`},
		{"underscore name", "$_x1", `"u"`},
		{"field of variable", "$order.items[1].sku", `"b"`},
		{"wildcard over variable", "$order.items[*].sku", `["a","b"]`},
		{"input root unaffected", "$.input", `true`},
		{"context root unaffected", "$$.Execution.Name", `"run-1"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertSelects(t, doc, ctxObj, tc.path, tc.want)
		})
	}
}

func TestSelectPath_undefinedVariable(t *testing.T) {
	// Given: a context with one variable, and one with none at all
	withVars := variablesCtx(map[string]any{"a": 1.0})

	for _, ctxObj := range []map[string]any{withVars, nil} {
		// When: a path names a variable that was never assigned
		_, err := selectPath(map[string]any{}, ctxObj, "$missing.field")

		// Then: the error names the variable
		if err == nil || !strings.Contains(err.Error(), "$missing") {
			t.Errorf("err = %v, want one naming $missing", err)
		}
	}
}

func TestSelectPath_contextObjectHidesVariables(t *testing.T) {
	// Given: a context object that carries the reserved variables key
	ctxObj := variablesCtx(map[string]any{"secret": "s"})

	for _, path := range []string{"$$", "$$.*", "$$..*"} {
		// When: we select the whole context object
		got, err := selectPath(nil, ctxObj, path)
		if err != nil {
			t.Fatalf("selectPath(%q): %v", path, err)
		}

		// Then: the reserved key never leaks into the result
		encoded, _ := json.Marshal(got)
		if strings.Contains(string(encoded), variablesContextKey) || strings.Contains(string(encoded), `"s"`) {
			t.Errorf("selectPath(%q) = %s leaks the variables", path, encoded)
		}
	}
	if _, err := selectPath(nil, ctxObj, "$$."+variablesContextKey); err == nil {
		t.Error("the reserved key must not be addressable through $$")
	}
	if _, ok := ctxObj[variablesContextKey]; !ok {
		t.Error("selecting $$ must not mutate the context object")
	}
}

func TestInsertPath_variableRootRejected(t *testing.T) {
	// Given: a document
	doc := decodeTestJSON(t, `{}`)

	// When: we try to write into a variable
	_, err := insertPath(doc, "$myVar.x", 1.0)

	// Then: it is refused — variables are only written by Assign
	if err == nil {
		t.Fatal("expected an error")
	}
}
