package glue

import (
	"sort"
	"strings"
	"testing"
)

var exprTestKeys = []Column{
	{Name: "year", Type: "int"},
	{Name: "month", Type: "string"},
	{Name: "dt", Type: "date"},
	{Name: "Region", Type: "varchar(10)"},
	{Name: "amount", Type: "decimal(10,2)"},
}

// exprTestPartitions are keyed by a label so a case can name what it expects.
var exprTestPartitions = map[string]map[string]string{
	"a": {"year": "2023", "month": "12", "dt": "2023-12-31", "region": "eu-west-1", "amount": "9.50"},
	"b": {"year": "2024", "month": "01", "dt": "2024-01-01", "region": "us-east-1", "amount": "10"},
	"c": {"year": "2024", "month": "02", "dt": "2024-02-15", "region": "us-west-2", "amount": "100.25"},
	"d": {"year": "999", "month": "02", "dt": "0999-02-01", "region": "ap-south-1", "amount": "-1"},
}

func matching(t *testing.T, expr string) string {
	t.Helper()
	f, err := parsePartitionExpression(expr, exprTestKeys)
	if err != nil {
		t.Fatalf("parsePartitionExpression(%q) error = %v", expr, err)
	}
	var got []string
	for label, values := range exprTestPartitions {
		if f.match(values) {
			got = append(got, label)
		}
	}
	sort.Strings(got)
	return strings.Join(got, ",")
}

func TestParsePartitionExpression_matches(t *testing.T) {
	cases := []struct {
		name, expr, want string
	}{
		{"empty matches all", "", "a,b,c,d"},
		{"blank matches all", "   ", "a,b,c,d"},
		{"string equality", "month = '02'", "c,d"},
		{"numeric equality ignores formatting", "amount = 10.00", "b"},
		{"numeric greater-than is numeric, not lexical", "year > 1000", "a,b,c"},
		{"literal first is flipped", "1000 < year", "a,b,c"},
		{"not equal", "year <> 2024", "a,d"},
		{"bang-equals is not-equal", "year != 2024", "a,d"},
		{"less-or-equal", "year <= 2023", "a,d"},
		{"greater-or-equal", "amount >= 10", "b,c"},
		{"negative literal", "amount < 0", "d"},
		{"string numeric literal under numeric key", "year = '2024'", "b,c"},
		{"date range", "dt >= '2024-01-01' AND dt < '2024-02-01'", "b"},
		{"and binds tighter than or", "year = 2023 OR year = 2024 AND month = '02'", "a,c"},
		{"parentheses override precedence", "(year = 2023 OR year = 2024) AND month = '02'", "c"},
		{"in list", "month IN ('01', '12')", "a,b"},
		{"numeric in list", "year IN (2023, 999)", "a,d"},
		{"not in", "month NOT IN ('01', '12')", "c,d"},
		{"between is inclusive", "year BETWEEN 2023 AND 2024", "a,b,c"},
		{"not between", "year NOT BETWEEN 2023 AND 2024", "d"},
		{"like", "region LIKE 'us-%'", "b,c"},
		{"like single char", "region LIKE 'us-_ast-1'", "b"},
		{"not like", "region NOT LIKE 'us-%'", "a,d"},
		{"not prefix", "NOT (year = 2024)", "a,d"},
		{"is null never matches", "month IS NULL", ""},
		{"is not null always matches", "month IS NOT NULL", "a,b,c,d"},
		{"keywords are case-insensitive", "year between 2023 and 2023 or month in ('02')", "a,c,d"},
		{"column names are case-insensitive", "REGION = 'us-east-1'", "b"},
		{"backtick-quoted key", "`year` = 2023", "a"},
		{"double-quoted key", `"month" = '12'`, "a"},
		{"escaped quote in literal", "region = 'it''s'", ""},
		{"trino-style nesting", "((year = 2024)) AND ((month IN ('01','02')))", "b,c"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Given: four partitions of a table keyed by five typed keys
			// When: the expression is compiled and applied
			got := matching(t, tc.expr)
			// Then: exactly the expected partitions match
			if got != tc.want {
				t.Errorf("%q matched [%s], want [%s]", tc.expr, got, tc.want)
			}
		})
	}
}

func TestParsePartitionExpression_rejectsUnsupportedSyntax(t *testing.T) {
	keys := append([]Column{{Name: "flag", Type: "boolean"}}, exprTestKeys...)
	cases := []struct{ name, expr string }{
		{"unknown key", "nope = 1"},
		{"unsupported key type", "flag = 'true'"},
		{"non-numeric literal for numeric key", "year = 'abc'"},
		{"two keys compared", "year = month"},
		{"two literals compared", "1 = 1"},
		{"dangling operator", "year ="},
		{"missing operator", "year 2024"},
		{"unbalanced parenthesis", "(year = 2024"},
		{"trailing tokens", "year = 2024 2025"},
		{"unterminated string", "month = '01"},
		{"function call", "lower(month) = '01'"},
		{"arithmetic", "year + 1 = 2025"},
		{"bare bang", "year ! 2024"},
		{"in without parentheses", "month IN '01'"},
		{"between without and", "year BETWEEN 1 2"},
		{"like with a number", "region LIKE 5"},
		{"is without null", "month IS '01'"},
		{"unknown character", "month = '01' ; DROP"},
		{"invalid number", "year = 1.2.3"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Given/When: an expression outside the supported subset
			_, err := parsePartitionExpression(tc.expr, keys)
			// Then: it is refused rather than matching everything
			if err == nil {
				t.Fatalf("parsePartitionExpression(%q) succeeded, want an error", tc.expr)
			}
		})
	}
}

func TestParsePartitionExpression_nonNumericStoredValueMatchesNothing(t *testing.T) {
	// Given: a numeric key whose stored value is not a number
	f, err := parsePartitionExpression("year <> 1", exprTestKeys)
	if err != nil {
		t.Fatal(err)
	}
	// When/Then: no comparison matches it, including inequality — nor does
	// a value only big.Rat's wider syntax reads as a number
	for _, stored := range []string{"unknown", "0x10", "1/2"} {
		if f.match(map[string]string{"year": stored}) {
			t.Errorf("stored value %q matched a numeric comparison", stored)
		}
	}
}

func TestParsePartitionExpression_numbersAndIdentifiers(t *testing.T) {
	keys := []Column{{Name: "année", Type: "int"}, {Name: "n", Type: "decimal"}}
	cases := []struct {
		expr   string
		values map[string]string
		want   bool
	}{
		{"année = 2024", map[string]string{"année": "2024"}, true},
		{"n = 1e3", map[string]string{"n": "1000"}, true},
		{"n > 1.5E-1", map[string]string{"n": "0.2"}, true},
		{"n >= -2.5e+1", map[string]string{"n": "-25"}, true},
		{"n = 1e3", map[string]string{"n": "1e3"}, true},
		{"n=-1", map[string]string{"n": "-1"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.expr, func(t *testing.T) {
			// Given/When: an expression with a non-ASCII key or an exponent
			f, err := parsePartitionExpression(tc.expr, keys)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			// Then: it compares as the typed value
			if got := f.match(tc.values); got != tc.want {
				t.Errorf("match(%v) = %v, want %v", tc.values, got, tc.want)
			}
		})
	}
	for _, bad := range []string{"n = 0x10", "n = 1/2", "n = 1e", "n = 1e+", "n = 1.2.3"} {
		if _, err := parsePartitionExpression(bad, keys); err == nil {
			t.Errorf("parsePartitionExpression(%q) succeeded, want an invalid number", bad)
		}
	}
}

func TestParsePartitionExpression_foldedDuplicateKeyStaysFilterable(t *testing.T) {
	cases := []struct {
		name string
		keys []Column
	}{
		{"unsupported first", []Column{{Name: "Flag", Type: "boolean"}, {Name: "flag", Type: "int"}}},
		{"unsupported last", []Column{{Name: "flag", Type: "int"}, {Name: "Flag", Type: "boolean"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Given: two keys whose names fold to one, only one of them filterable
			// When: an expression names it
			f, err := parsePartitionExpression("flag = 1", tc.keys)
			// Then: it compiles against the filterable key and compares numerically
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if !f.match(map[string]string{"flag": "1.0"}) {
				t.Error("flag = 1 did not match a stored 1.0")
			}
		})
	}
}
