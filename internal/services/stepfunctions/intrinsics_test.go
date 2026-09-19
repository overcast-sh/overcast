package stepfunctions

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"testing"
)

func evalIntrinsicJSON(t *testing.T, expr string, doc any, ctxObj map[string]any) (string, error) {
	t.Helper()
	got, err := evaluateTemplateExpression(expr, doc, ctxObj)
	if err != nil {
		return "", err
	}
	encoded, mErr := json.Marshal(got)
	if mErr != nil {
		t.Fatalf("marshal %v: %v", got, mErr)
	}
	return string(encoded), nil
}

func TestIntrinsics_results(t *testing.T) {
	// Given: an input carrying the values the AWS documentation examples use
	doc := decodeTestJSON(t, `{
	  "Id": 123456,
	  "inputArray": [1,2,3,4,5,6,7,8,9],
	  "dupes": [1,2,3,3,3,3,3,3,4],
	  "mixedDupes": [{"a":1},{"a":1},"x",1,"x",[1]],
	  "lookingFor": 5,
	  "index": 5,
	  "input": "Data to encode",
	  "base64": "RGF0YSB0byBlbmNvZGU=",
	  "Data": "input data",
	  "json1": {"a": {"a1": 1, "a2": 2}, "b": 2},
	  "json2": {"a": {"a3": 1, "a4": 2}, "c": 3},
	  "value1": 111,
	  "step": -1,
	  "inputString": "1,2,3,4,5",
	  "multi": "This.is+a,test=string",
	  "splitter": ".+,=",
	  "name": "Arnav",
	  "template": "Hello, my name is {}.",
	  "escapedJsonString": "{\"foo\": \"bar\"}",
	  "unescapedJson": {"foo": "bar"},
	  "items": [{"id":"a"},{"id":"b"}]
	}`)
	vars := map[string]any{"greeting": "hey", "nums": []any{2.0, 4.0}}
	ctxObj := map[string]any{"Execution": map[string]any{"Name": "run-9"}, variablesContextKey: vars}

	cases := []struct {
		name string
		expr string
		want string
	}{
		{"Array", "States.Array($.Id)", `[123456]`},
		{"Array empty", "States.Array()", `[]`},
		{"Array literals", "States.Array('a', 1, 2.5, true, false, null)", `["a",1,2.5,true,false,null]`},
		{"ArrayPartition", "States.ArrayPartition($.inputArray,4)", `[[1,2,3,4],[5,6,7,8],[9]]`},
		{"ArrayPartition rounds chunk", "States.ArrayPartition($.inputArray, 4.6)", `[[1,2,3,4,5],[6,7,8,9]]`},
		{"ArrayPartition empty", "States.ArrayPartition(States.Array(), 2)", `[]`},
		{"ArrayContains hit", "States.ArrayContains($.inputArray, $.lookingFor)", `true`},
		{"ArrayContains miss", "States.ArrayContains($.inputArray, 42)", `false`},
		{"ArrayContains object", "States.ArrayContains($.mixedDupes, $.json1.b)", `false`},
		{"ArrayContains deep", "States.ArrayContains($.items, States.StringToJson('{\"id\":\"b\"}'))", `true`},
		{"ArrayRange", "States.ArrayRange(1, 9, 2)", `[1,3,5,7,9]`},
		{"ArrayRange descending", "States.ArrayRange(5, 1, -2)", `[5,3,1]`},
		{"ArrayRange wrong direction", "States.ArrayRange(1, 5, -1)", `[]`},
		{"ArrayRange rounds", "States.ArrayRange(0.6, 3.4, 1)", `[1,2,3]`},
		{"ArrayRange 1000 items", "States.ArrayLength(States.ArrayRange(1, 1000, 1))", `1000`},
		{"ArrayGetItem", "States.ArrayGetItem($.inputArray, $.index)", `6`},
		{"ArrayLength", "States.ArrayLength($.inputArray)", `9`},
		{"ArrayUnique", "States.ArrayUnique($.dupes)", `[1,2,3,4]`},
		{"ArrayUnique mixed", "States.ArrayUnique($.mixedDupes)", `[{"a":1},"x",1,[1]]`},
		{"Base64Encode", "States.Base64Encode($.input)", `"RGF0YSB0byBlbmNvZGU="`},
		{"Base64Decode", "States.Base64Decode($.base64)", `"Data to encode"`},
		{"Hash SHA-1", "States.Hash($.Data, 'SHA-1')", `"aaff4a450a104cd177d28d18d74485e8cae074b7"`},
		{"Hash MD5", "States.Hash('input data', 'MD5')", `"812f45842bc6d66ee14572ce20db8e86"`},
		{"Hash SHA-256", "States.Hash('abc', 'SHA-256')", `"ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"`},
		{"Hash SHA-384", "States.Hash('abc', 'SHA-384')", `"cb00753f45a35e8bb5a03d699ac65007272c32ab0eded1631a8b605a43ff5bed8086072ba1e7cc2358baeca134c825a7"`},
		{"Hash SHA-512", "States.Hash('abc', 'SHA-512')", `"ddaf35a193617abacc417349ae20413112e6fa4e89a97ea20a9eeee64b55d39a2192992a274fc1a836ba3c23a3feebbd454d4423643ce80e2a9ac94fa54ca49f"`},
		{"JsonMerge", "States.JsonMerge($.json1, $.json2, false)", `{"a":{"a3":1,"a4":2},"b":2,"c":3}`},
		{"StringToJson", "States.StringToJson($.escapedJsonString)", `{"foo":"bar"}`},
		{"JsonToString", "States.JsonToString($.unescapedJson)", `"{\"foo\":\"bar\"}"`},
		{"MathAdd", "States.MathAdd($.value1, $.step)", `110`},
		{"MathAdd rounds", "States.MathAdd(1.6, 1)", `3`},
		{"StringSplit", "States.StringSplit($.inputString, ',')", `["1","2","3","4","5"]`},
		{"StringSplit multi-delimiter", "States.StringSplit($.multi, $.splitter)", `["This","is","a","test","string"]`},
		{"StringSplit drops empty tokens", "States.StringSplit(',a,,b,', ',')", `["a","b"]`},
		{"Format literal template", "States.Format('Hello, my name is {}.', $.name)", `"Hello, my name is Arnav."`},
		{"Format path template", "States.Format($.template, $.name)", `"Hello, my name is Arnav."`},
		{"Format numbers and booleans", "States.Format('{} {} {} {}', 1, 2.5, true, null)", `"1 2.5 true null"`},
		{"Format escaped braces are literal", "States.Format('\\{\\} {}', 'x')", `"{} x"`},
		{"Format escaped quote and backslash", "States.Format('it\\'s a \\\\ {}', 'x')", `"it's a \\ x"`},
		{"Format with comma and paren in literal", "States.Format('a, b) {}', 'c')", `"a, b) c"`},
		{"Format variable and context", "States.Format('{} {}', $greeting, $$.Execution.Name)", `"hey run-9"`},
		{"nested", "States.ArrayGetItem(States.StringSplit(States.ArrayGetItem(States.StringSplit('arn:aws:x/a.b/c', '/'), 1), '.'), 0)", `"a"`},
		{"indefinite path argument", "States.ArrayLength($.items[*].id)", `2`},
		{"filter path argument", "States.ArrayGetItem($.items[?(@.id == 'b')].id, 0)", `"b"`},
		{"variable argument", "States.ArrayLength($nums)", `2`},
		{"whitespace tolerated", "  States.MathAdd( 1 , 2 )  ", `3`},
		{"negative literal", "States.MathAdd(-5, 2)", `-3`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// When: the intrinsic is evaluated
			got, err := evalIntrinsicJSON(t, tc.expr, doc, ctxObj)

			// Then: it yields the documented result
			if err != nil {
				t.Fatalf("%s: %v", tc.expr, err)
			}
			if got != tc.want {
				t.Fatalf("%s = %s, want %s", tc.expr, got, tc.want)
			}
		})
	}
}

func TestIntrinsics_jsonToStringDoesNotEscapeHTML(t *testing.T) {
	// Given / When: a value with HTML-significant characters is stringified
	got, err := evaluateTemplateExpression("States.JsonToString('<a&b>')", nil, nil)

	// Then: the characters are kept verbatim, as AWS does
	if err != nil {
		t.Fatal(err)
	}
	if got != `"<a&b>"` {
		t.Fatalf("JsonToString = %v", got)
	}
}

func TestIntrinsics_numbersAreFloat64(t *testing.T) {
	// Given: intrinsics that produce numbers
	for _, expr := range []string{"States.MathAdd(1, 2)", "States.ArrayLength(States.Array(1))", "States.MathRandom(1, 5)"} {
		// When: evaluated
		got, err := evaluateTemplateExpression(expr, nil, nil)
		if err != nil {
			t.Fatalf("%s: %v", expr, err)
		}

		// Then: the value has the type json.Unmarshal would produce
		if _, ok := got.(float64); !ok {
			t.Errorf("%s = %T, want float64", expr, got)
		}
	}
	got, err := evaluateTemplateExpression("States.ArrayRange(1, 2, 1)", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := got.([]any)[0].(float64); !ok {
		t.Errorf("ArrayRange items are %T, want float64", got.([]any)[0])
	}
}

func TestIntrinsics_mathRandom(t *testing.T) {
	// Given: a range and a seed
	// When: MathRandom is evaluated repeatedly
	for i := 0; i < 200; i++ {
		got, err := evaluateTemplateExpression("States.MathRandom(3, 6)", nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		// Then: start is inclusive and end exclusive
		if n := got.(float64); n < 3 || n >= 6 || n != float64(int(n)) {
			t.Fatalf("MathRandom(3, 6) = %v", n)
		}
	}
	first, err := evaluateTemplateExpression("States.MathRandom(1, 1000000, 42)", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		again, _ := evaluateTemplateExpression("States.MathRandom(1, 1000000, 42)", nil, nil)
		// Then: a seeded call is deterministic
		if again != first {
			t.Fatalf("seeded MathRandom returned %v then %v", first, again)
		}
	}
	other, _ := evaluateTemplateExpression("States.MathRandom(1, 1000000, 43)", nil, nil)
	if other == first {
		t.Errorf("different seeds returned the same value %v", first)
	}
}

func TestIntrinsics_uuid(t *testing.T) {
	// Given / When: two UUIDs are generated
	a, err := evaluateTemplateExpression("States.UUID()", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := evaluateTemplateExpression("States.UUID()", nil, nil)

	// Then: each is a lowercase v4 UUID and they differ
	v4 := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	if !v4.MatchString(a.(string)) {
		t.Errorf("UUID %q is not a v4 UUID", a)
	}
	if a == b {
		t.Error("two UUIDs were equal")
	}
}

func TestIntrinsics_failuresAreIntrinsicFailures(t *testing.T) {
	// Given: an input with values of each type
	doc := decodeTestJSON(t, `{"arr":[1,2,3],"s":"str","obj":{"a":1},"n":1}`)
	long := strings.Repeat("a", 10001)

	cases := []struct {
		name string
		expr string
	}{
		{"Array unaffected, ArrayPartition non-array", "States.ArrayPartition($.s, 2)"},
		{"ArrayPartition zero chunk", "States.ArrayPartition($.arr, 0)"},
		{"ArrayPartition negative chunk", "States.ArrayPartition($.arr, -1)"},
		{"ArrayPartition arg count", "States.ArrayPartition($.arr)"},
		{"ArrayContains non-array", "States.ArrayContains($.obj, 1)"},
		{"ArrayContains arg count", "States.ArrayContains($.arr)"},
		{"ArrayRange zero step", "States.ArrayRange(1, 5, 0)"},
		{"ArrayRange over 1000", "States.ArrayRange(1, 1001, 1)"},
		{"ArrayRange string arg", "States.ArrayRange('1', 5, 1)"},
		{"ArrayGetItem out of range", "States.ArrayGetItem($.arr, 3)"},
		{"ArrayGetItem negative", "States.ArrayGetItem($.arr, -1)"},
		{"ArrayGetItem non-number index", "States.ArrayGetItem($.arr, 'x')"},
		{"ArrayLength non-array", "States.ArrayLength($.s)"},
		{"ArrayUnique non-array", "States.ArrayUnique($.s)"},
		{"Base64Encode non-string", "States.Base64Encode($.n)"},
		{"Base64Encode too long", fmt.Sprintf("States.Base64Encode('%s')", long)},
		{"Base64Decode invalid", "States.Base64Decode('not base64!')"},
		{"Base64Decode too long", fmt.Sprintf("States.Base64Decode('%s')", long)},
		{"Hash unknown algorithm", "States.Hash('x', 'SHA-3')"},
		{"Hash lowercase algorithm", "States.Hash('x', 'sha-256')"},
		{"Hash too long", fmt.Sprintf("States.Hash('%s', 'MD5')", long)},
		{"JsonMerge deep", "States.JsonMerge($.obj, $.obj, true)"},
		{"JsonMerge missing flag", "States.JsonMerge($.obj, $.obj)"},
		{"JsonMerge non-object", "States.JsonMerge($.obj, $.arr, false)"},
		{"StringToJson invalid", "States.StringToJson('{nope')"},
		{"StringToJson non-string", "States.StringToJson($.obj)"},
		{"JsonToString arg count", "States.JsonToString()"},
		{"MathRandom start not below end", "States.MathRandom(5, 5)"},
		{"MathRandom non-number", "States.MathRandom('a', 5)"},
		{"MathRandom arg count", "States.MathRandom(1)"},
		{"MathAdd out of int32 range", "States.MathAdd(2147483648, 1)"},
		{"MathAdd string", "States.MathAdd('1', 1)"},
		{"MathAdd arg count", "States.MathAdd(1, 2, 3)"},
		{"StringSplit non-string", "States.StringSplit($.arr, ',')"},
		{"UUID with args", "States.UUID(1)"},
		{"Format too few args", "States.Format('{} {}', 'a')"},
		{"Format too many args", "States.Format('{}', 'a', 'b')"},
		{"Format non-string template", "States.Format(1)"},
		{"unknown function", "States.NoSuchThing($.s)"},
		{"open escape", "States.Format('bad \\q {}', 'x')"},
		{"unterminated literal", "States.Format('abc)"},
		{"missing close paren", "States.Array(1, 2"},
		{"trailing garbage", "States.Array(1) x"},
		{"bad literal token", "States.Array(bogus)"},
		{"nesting deeper than 10", strings.Repeat("States.Array(", 11) + "1" + strings.Repeat(")", 11)},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// When: the call is evaluated
			_, err := evaluateTemplateExpression(tc.expr, doc, nil)

			// Then: it fails with States.IntrinsicFailure
			if err == nil {
				t.Fatalf("%s: expected an error", tc.expr)
			}
			if name := templateErrorName(err); name != "States.IntrinsicFailure" {
				t.Fatalf("%s: templateErrorName = %s (%v), want States.IntrinsicFailure", tc.expr, name, err)
			}
		})
	}
}

func TestIntrinsics_tenNestedLevelsAllowed(t *testing.T) {
	// Given: exactly ten nested intrinsic calls
	expr := strings.Repeat("States.Array(", 10) + "1" + strings.Repeat(")", 10)

	// When: evaluated
	_, err := evaluateTemplateExpression(expr, nil, nil)

	// Then: it succeeds
	if err != nil {
		t.Fatalf("%s: %v", expr, err)
	}
}

func TestTemplateErrorName(t *testing.T) {
	// Given: a document without the referenced field
	doc := decodeTestJSON(t, `{"a":1}`)

	// When: a plain path, and a path inside an intrinsic, fail to resolve
	_, pathErr := evaluateTemplateExpression("$.missing", doc, nil)
	_, argErr := evaluateTemplateExpression("States.Array($.missing)", doc, nil)
	_, undefinedVar := evaluateTemplateExpression("$nope", doc, nil)

	// Then: they are parameter path failures, not intrinsic failures
	for _, err := range []error{pathErr, argErr, undefinedVar} {
		if err == nil {
			t.Fatal("expected an error")
		}
		if got := templateErrorName(err); got != "States.ParameterPathFailure" {
			t.Errorf("templateErrorName(%v) = %s", err, got)
		}
	}

	// And: an intrinsic error is recognised through wrapping
	wrapped := fmt.Errorf("rendering: %w", &intrinsicError{msg: "boom"})
	if got := templateErrorName(wrapped); got != "States.IntrinsicFailure" {
		t.Errorf("templateErrorName(wrapped) = %s", got)
	}
	var ie *intrinsicError
	if !errors.As(wrapped, &ie) || ie.Error() != "boom" {
		t.Errorf("intrinsicError does not unwrap: %v", ie)
	}
}

func TestRenderPayloadTemplate_intrinsicFailurePropagates(t *testing.T) {
	// Given: a template calling an intrinsic with a bad argument
	tmpl := json.RawMessage(`{"x":{"y.$":"States.ArrayGetItem(States.Array(1), 5)"}}`)

	// When: the template is rendered
	_, err := renderPayloadTemplate(tmpl, map[string]any{}, nil)

	// Then: the failure keeps its intrinsic identity
	if templateErrorName(err) != "States.IntrinsicFailure" {
		t.Fatalf("err = %v, want an intrinsic failure", err)
	}
}
