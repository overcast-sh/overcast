package stepfunctions

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// jsonataTestNow is the clock reading $now and $millis see in these tests.
var jsonataTestNow = time.Date(2026, 1, 2, 3, 4, 5, 678_000_000, time.UTC)

func evalJSONataJSON(t *testing.T, expr, input string, vars map[string]any) (string, bool, error) {
	t.Helper()
	var doc any
	if input != "" {
		doc = decodeTestJSON(t, input)
	}
	got, defined, err := evaluateJSONata(expr, doc, vars, jsonataTestNow)
	if err != nil || !defined {
		return "", defined, err
	}
	encoded, mErr := json.Marshal(got)
	if mErr != nil {
		t.Fatalf("marshal %v: %v", got, mErr)
	}
	return string(encoded), true, nil
}

// TestJSONata_results runs the JSONata 2.x documentation examples, AWS's
// added functions and the language features 1.x engines lack.
func TestJSONata_results(t *testing.T) {
	// Given: an input shaped like the jsonata.org examples
	input := `{
	  "Account": {"Order": [
	    {"OrderID": "order103", "Product": [{"Product Name": "Bowler Hat", "Price": 34.45}, {"Product Name": "Trilby hat", "Price": 21.67}]},
	    {"OrderID": "order104", "Product": [{"Product Name": "Cloak", "Price": 107.99}]}
	  ]},
	  "library": {
	    "books": [{"isbn": "1", "title": "Structure"}, {"isbn": "2", "title": "Compilers"}],
	    "loans": [{"isbn": "2", "customer": "c1"}]
	  },
	  "nothing": null,
	  "seed": 7
	}`

	cases := []struct {
		name string
		expr string
		want string
	}{
		// ── String functions new in 2.x ──
		{"formatInteger words", `$formatInteger(2789, 'w')`, `"two thousand, seven hundred and eighty-nine"`},
		{"formatInteger roman", `$formatInteger(1999, 'I')`, `"MCMXCIX"`},
		{"formatInteger picture", `$formatInteger(12345678, '#,##0')`, `"12,345,678"`},
		{"formatInteger ordinal", `$formatInteger(2, '1;o')`, `"2nd"`},
		{"parseInteger words", `$parseInteger("twelve thousand, four hundred and seventy-six", 'w')`, `12476`},
		{"parseInteger picture", `$parseInteger('12,345,678', '#,##0')`, `12345678`},
		{"parseInteger roman", `$parseInteger('MCMXCIX', 'I')`, `1999`},
		{"formatNumber grouping", `$formatNumber(1234.5678, "#,##0.00")`, `"1,234.57"`},
		{"formatNumber percent", `$formatNumber(0.14, "01%")`, `"14%"`},
		{"formatNumber exponent", `$formatNumber(1234.5678, "00.000e0")`, `"12.346e2"`},
		{"formatBase", `$formatBase(100, 2)`, `"1100100"`},
		{"formatBase hex", `$formatBase(2555, 16)`, `"9fb"`},
		{"pad right", `$pad("foo", 5)`, `"foo  "`},
		{"pad left with char", `$pad("5", -3, "0")`, `"005"`},
		{"match regex", `$match("ababbabbcc", /a(b+)/).match`, `["ab","abb","abb"]`},
		{"match groups", `$match("ababbabbcc", /a(b+)/, 1).groups`, `["b"]`},
		{"replace string", `$replace("John Smith and John Jones", "John", "Mr")`, `"Mr Smith and Mr Jones"`},
		{"replace regex backreference", `$replace("abracadabra", /a(.)/, "$1")`, `"brcdbra"`},
		{"replace with function", `$replace("temperature = 68F", /(\d+)F/, function($m) { $round(($number($m.groups[0]) - 32) * 5/9) & "C" })`, `"temperature = 20C"`},
		{"contains regex", `$contains("abracadabra", /a.*a/)`, `true`},
		{"split regex", `$split("a1b22c", /\d+/)`, `["a","b","c"]`},
		{"encodeUrl", `$encodeUrl("https://mozilla.org/?x=шеллы")`, `"https://mozilla.org/?x=%D1%88%D0%B5%D0%BB%D0%BB%D1%8B"`},
		{"encodeUrlComponent", `$encodeUrlComponent("?x=test")`, `"%3Fx%3Dtest"`},
		{"decodeUrlComponent", `$decodeUrlComponent("%3Fx%3Dtest")`, `"?x=test"`},
		{"base64 round trip", `$base64decode($base64encode("myuser:mypass"))`, `"myuser:mypass"`},

		// ── Array, object and higher-order functions ──
		{"distinct", `$distinct([1,2,3,3,4,3,5])`, `[1,2,3,4,5]`},
		{"type", `[$type(null), $type([]), $type({}), $type("s"), $type(1), $type(true), $type(function(){1})]`, `["null","array","object","string","number","boolean","function"]`},
		{"zip", `$zip([1,2,3],[4,5,6])`, `[[1,4],[2,5],[3,6]]`},
		{"each", `$each({"a": 1, "b": 2}, function($v, $k) { $k & "=" & $v })`, `["a=1","b=2"]`},
		{"sift", `$sift({"one": 1, "two": 2, "three": 3}, function($v) { $v > 1 })`, `{"three":3,"two":2}`},
		{"single", `$single([1,2,3], function($v) { $v = 2 })`, `2`},
		{"reduce", `$reduce([1..5], function($i, $j) { $i * $j })`, `120`},
		{"sort with function", `$sort([3,1,2], function($a, $b) { $a < $b })`, `[3,2,1]`},
		{"spread and merge", `$merge($spread({"a": 1, "b": 2}))`, `{"a":1,"b":2}`},
		{"lookup", `$lookup({"a": 1}, "a")`, `1`},
		{"shuffle keeps members", `$sort($shuffle([3,1,2]))`, `[1,2,3]`},

		// ── Date/time with the injected clock ──
		{"now", `$now()`, `"2026-01-02T03:04:05.678Z"`},
		{"now is stable within an expression", `$now() = $now()`, `true`},
		{"now with picture", `$now('[Y0001]-[M01]-[D01]')`, `"2026-01-02"`},
		{"now with timezone", `$now('[H01]:[m01]', '+0100')`, `"04:04"`},
		{"millis", `$millis()`, `1767323045678`},
		{"toMillis ISO", `$toMillis("2017-11-07T15:07:54.972Z")`, `1510067274972`},
		{"toMillis picture", `$toMillis("2018-10-21", "[Y0001]-[M01]-[D01]")`, `1540080000000`},
		{"fromMillis default", `$fromMillis(1510067597121)`, `"2017-11-07T15:13:17.121Z"`},
		{"fromMillis picture", `$fromMillis(1510067597121, '[M01]/[D01]/[Y0001] [h#1]:[m01][P]')`, `"11/07/2017 3:13pm"`},
		{"fromMillis timezone", `$fromMillis(1510067597121, '[H01]:[m01]:[s01] [z]', '-0500')`, `"10:13:17 GMT-05:00"`},
		{"fromMillis day name", `$fromMillis(1510067597121, '[FNn], [D1o] [MNn] [Y]')`, `"Tuesday, 7th November 2017"`},

		// ── Language features ──
		{"parent operator", `Account.Order.Product.{"name": ` + "`Product Name`" + `, "order": %.OrderID}`, `[{"name":"Bowler Hat","order":"order103"},{"name":"Trilby hat","order":"order103"},{"name":"Cloak","order":"order104"}]`},
		{"focus binding", `library.loans@$l.books@$b[$l.isbn = $b.isbn].{"title": $b.title, "customer": $l.customer}`, `{"customer":"c1","title":"Compilers"}`},
		{"index binding", `["a","b"]#$i.{"v": $, "i": $i}`, `[{"i":0,"v":"a"},{"i":1,"v":"b"}]`},
		{"transform", `{"a": 1, "b": 2} ~> |$|{"c": 3}, ["b"]|`, `{"a":1,"c":3}`},
		{"null is a value", `nothing = null`, `true`},
		{"null result", `nothing`, `null`},
		{"order-by", `Account.Order.Product^(>Price)."Product Name"`, `["Cloak","Bowler Hat","Trilby hat"]`},
		{"group-by", `Account.Order{OrderID: $sum(Product.Price)}`, `{"order103":56.120000000000005,"order104":107.99}`},

		// ── Functions Step Functions adds ──
		{"partition", `$partition([1,2,3,4,5], 2)`, `[[1,2],[3,4],[5]]`},
		{"partition floors the size", `$partition([1,2,3], 2.7)`, `[[1,2],[3]]`},
		{"range", `$range(0, 10, 5)`, `[0,5,10]`},
		{"range descending", `$range(3, 1, -1)`, `[3,2,1]`},
		{"hash", `$hash('abc', 'SHA-256')`, `"ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"`},
		{"hash MD5", `$hash('abc', 'MD5')`, `"900150983cd24fb0d6963f7d28e17f72"`},
		{"parse", `$parse('{"a": [1, null]}')`, `{"a":[1,null]}`},
		{"random seeded", `$random(seed) = $random(7)`, `true`},
		{"random range", `$random() >= 0 and $random() < 1`, `true`},
		{"uuid", `$length($uuid())`, `36`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// When: the expression is evaluated
			got, defined, err := evalJSONataJSON(t, tc.expr, input, nil)

			// Then: it produces what JSONata 2.x on AWS produces
			if err != nil {
				t.Fatalf("%s: %v", tc.expr, err)
			}
			if !defined {
				t.Fatalf("%s: undefined, want %s", tc.expr, tc.want)
			}
			if !sameJSON(t, got, tc.want) {
				t.Errorf("%s = %s, want %s", tc.expr, got, tc.want)
			}
		})
	}
}

func TestJSONata_variables(t *testing.T) {
	// Given: workflow variables of every JSON type, including null
	vars := map[string]any{
		"states":  map[string]any{"input": map[string]any{"n": 2.0}},
		"name":    "María",
		"nothing": nil,
		"list":    []any{1.0, 2.0},
	}

	// When: an expression reads them
	got, _, err := evalJSONataJSON(t, `[$states.input.n + 1, $name, $nothing = null, $sum($list)]`, "", vars)

	// Then: each is bound with its JSON meaning
	if err != nil {
		t.Fatal(err)
	}
	if !sameJSON(t, got, `[3,"María",true,3]`) {
		t.Errorf("got %s", got)
	}
}

func TestJSONata_undefined(t *testing.T) {
	// Given: an expression selecting a field that is not there
	// When: it is evaluated
	_, defined, err := evalJSONataJSON(t, `missing.field`, `{"a":1}`, nil)

	// Then: the result is JSONata's undefined, not null
	if err != nil || defined {
		t.Fatalf("defined=%v err=%v, want undefined", defined, err)
	}
}

func TestJSONata_errors(t *testing.T) {
	cases := []struct {
		name, expr, wantInError string
	}{
		{"eval is not offered on AWS", `$eval("1 + 1")`, "$eval"},
		{"error", `$error("boom")`, "boom"},
		{"assert", `$assert(1 = 2, "not equal")`, "not equal"},
		{"single with many matches", `$single([1,2,3])`, ""},
		{"type error", `"a" + 1`, ""},
		{"syntax error", `$states.input.`, ""},
		{"unknown function", `$noSuchFunction()`, ""},
		{"partition size below one", `$partition([1,2], 0)`, "$partition"},
		{"range step zero", `$range(1, 2, 0)`, "$range"},
		{"range too long", `$range(0, 2000, 1)`, "1000"},
		{"hash algorithm", `$hash("a", "SHA-3")`, "$hash"},
		{"parse invalid", `$parse("{")`, "$parse"},
		{"recursion without end", `($f := function($n) { $f($n + 1) }; $f(0))`, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// When: the expression is evaluated
			_, _, err := evalJSONataJSON(t, tc.expr, `{}`, nil)

			// Then: it fails, which the interpreter reports as States.QueryEvaluationError
			if err == nil {
				t.Fatalf("%s: no error", tc.expr)
			}
			if !strings.Contains(err.Error(), tc.wantInError) {
				t.Errorf("%s: error %q does not mention %q", tc.expr, err, tc.wantInError)
			}
		})
	}
}

func sameJSON(t *testing.T, got, want string) bool {
	t.Helper()
	var g, w any
	if err := json.Unmarshal([]byte(got), &g); err != nil {
		t.Fatalf("got is not JSON: %s", got)
	}
	if err := json.Unmarshal([]byte(want), &w); err != nil {
		t.Fatalf("want is not JSON: %s", want)
	}
	ge, _ := json.Marshal(g)
	we, _ := json.Marshal(w)
	return string(ge) == string(we)
}
