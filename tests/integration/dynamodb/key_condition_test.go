package dynamodb_test

// KeyConditionExpression semantics for Query — which attributes a condition
// may name, which operators the sort key accepts, and the order the two
// conditions may be written in (issue #135).
//
// AWS evidence for the rules asserted below:
//   - "You must specify the partition key name and value as an equality
//     condition. You cannot use a non-key attribute in a key condition
//     expression." and the seven legal sort-key forms (=, <, <=, >, >=,
//     BETWEEN, begins_with) —
//     https://docs.aws.amazon.com/amazondynamodb/latest/developerguide/Query.KeyConditionExpressions.html
//   - "Query condition missed key schema element: <name>" is what AWS answers
//     when a key attribute of the table or index queried is not constrained by
//     the expression — including when a *non-key* attribute stands where the
//     sort key belongs, which leaves the sort key unconstrained. Reported
//     against a real account, naming the index's sort key, in
//     getmoto/moto#3237; moto raises it from validate_schema
//     (moto/dynamodb/parsing/key_condition_expression.py).
//   - "Query key condition not supported" is the answer when there is no key
//     to name as missing — a hash-only table or index given a second
//     condition, or a partition key compared with something other than "=".
//     https://dynobase.dev/dynamodb-errors/dynamodb-query-key-condition-not-supported/
//     and the same moto validate_schema.
//   - "KeyConditionExpressions must only contain one condition per key" is the
//     answer when both conditions name the same attribute, whether that is the
//     partition key (planetlabs/datalake-api#21, for
//     "(#n0 = :v0 AND begins_with(#n0, :v0))") or the sort key
//     (equaltoai/lesser#1500, for "sk >= :a AND sk <= :b").
//   - The two conditions may be written in either order, with the sort-key
//     condition first, and that holds for the non-equality operators as well
//     as for an equality: AWS's rules constrain the operator each key may use,
//     never the position it is written in, and moto's parser covers
//     "start_date >= :sk and job_id = :id", "start_date>:sk and job_id=:id",
//     "start_date=:sk and job_id = :id" and
//     "begins_with(start_date,:sk) and job_id = :id"
//     (tests/test_dynamodb/models/test_key_condition_expression_parser.py::test_reverse_keys).

import (
	"fmt"
	"net/http"
	"strconv"
	"testing"

	"github.com/overcast-sh/overcast/tests/helpers"
)

const (
	// wantKeyConditionNotSupported is AWS's answer when a key condition names
	// something that cannot be a key condition and no key schema element can
	// be named as the one missed.
	wantKeyConditionNotSupported = "Query key condition not supported"
	// wantOneConditionPerKey is AWS's answer when both conditions constrain
	// the same attribute.
	wantOneConditionPerKey = "KeyConditionExpressions must only contain one condition per key"
)

// createKeyCondTable makes a hash+sort table ("pk" String, "sk" String) and
// fills one partition with the sort keys given, so a condition's effect is
// read off the returned sort keys.
func createKeyCondTable(t *testing.T, srv *helpers.TestServer, name string, sortKeys ...string) {
	t.Helper()
	createTableWithSortKey(t, srv, name, "pk", "S", "sk", "S")
	for _, sk := range sortKeys {
		putItem(t, srv, name, map[string]any{
			"pk": map[string]any{"S": "P"},
			"sk": map[string]any{"S": sk},
		})
	}
}

// queryKeyCond runs a Query with the given KeyConditionExpression and values.
func queryKeyCond(t *testing.T, srv *helpers.TestServer, table, expr string, values map[string]any) *http.Response {
	t.Helper()
	return ddbCall(t, srv, "Query", map[string]any{
		"TableName":                 table,
		"KeyConditionExpression":    expr,
		"ExpressionAttributeValues": values,
	})
}

// querySortKeys runs a Query expected to succeed and returns the sort keys of
// the items it matched, in the order they were returned.
func querySortKeys(t *testing.T, srv *helpers.TestServer, table, expr string, values map[string]any) []string {
	t.Helper()
	resp := queryKeyCond(t, srv, table, expr, values)
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)

	var out struct {
		Items []map[string]map[string]any `json:"Items"`
	}
	helpers.DecodeJSON(t, resp, &out)
	got := make([]string, 0, len(out.Items))
	for _, item := range out.Items {
		s, _ := item["sk"]["S"].(string)
		got = append(got, s)
	}
	return got
}

func assertSortKeys(t *testing.T, got []string, want ...string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("matched sort keys %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("matched sort keys %v, want %v", got, want)
		}
	}
}

// ---- A non-key attribute in the key condition -------------------------------

// A second condition naming an attribute that is neither key is not a sort-key
// condition. Overcast used to treat it as one — every Query branch overwrote
// the parsed attribute name with the sort key's — so "pk = :p AND foo = :f"
// silently answered as if it read "pk = :p AND sk = :f", teaching a data model
// DynamoDB cannot serve.
func TestQuery_nonKeyAttributeInKeyCondition(t *testing.T) {
	// Given: a hash+sort table holding one item
	srv := helpers.NewTestServer(t)
	createKeyCondTable(t, srv, "kc-nonkey", "a")

	// When: the second condition names a non-key attribute
	resp := queryKeyCond(t, srv, "kc-nonkey", "pk = :p AND foo = :f", map[string]any{
		":p": map[string]any{"S": "P"},
		":f": map[string]any{"S": "a"},
	})
	defer resp.Body.Close()

	// Then: the sort key is the schema element left unconstrained, and AWS
	// names it rather than answering with the item the condition would have
	// matched had "foo" been "sk"
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	assertValidationMessage(t, resp, "Query condition missed key schema element: sk")
}

func TestQuery_nonKeyAttributeInKeyCondition_beginsWith(t *testing.T) {
	// Given: a hash+sort table holding one item
	srv := helpers.NewTestServer(t)
	createKeyCondTable(t, srv, "kc-nonkey-bw", "abc")

	// When: begins_with names a non-key attribute
	resp := queryKeyCond(t, srv, "kc-nonkey-bw", "pk = :p AND begins_with(foo, :f)", map[string]any{
		":p": map[string]any{"S": "P"},
		":f": map[string]any{"S": "a"},
	})
	defer resp.Body.Close()

	// Then: the function form is rejected the same way the comparison form is
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	assertValidationMessage(t, resp, "Query condition missed key schema element: sk")
}

// A hash-only table has no sort key to report as missed, so AWS falls back to
// the generic message.
func TestQuery_hashOnlyTable_secondCondition(t *testing.T) {
	// Given: a hash-only table
	srv := helpers.NewTestServer(t)
	createTable(t, srv, "kc-hash-only")
	putItem(t, srv, "kc-hash-only", map[string]any{
		"id":  map[string]any{"S": "x"},
		"foo": map[string]any{"S": "a"},
	})

	// When: a second condition is supplied anyway
	resp := queryKeyCond(t, srv, "kc-hash-only", "id = :i AND foo = :f", map[string]any{
		":i": map[string]any{"S": "x"},
		":f": map[string]any{"S": "a"},
	})
	defer resp.Body.Close()

	// Then: there is no key schema element to name
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	assertValidationMessage(t, resp, wantKeyConditionNotSupported)
}

// An index query is checked against the index's key schema, not the table's,
// so the table's own sort key is a non-key attribute there.
func TestQuery_GSI_tableSortKeyIsNotTheIndexSortKey(t *testing.T) {
	// Given: a table keyed (pk, sk) with a GSI keyed (gsipk, gsisk)
	srv := helpers.NewTestServer(t)
	createGSIKeyCondTable(t, srv, "kc-gsi-schema")

	// When: the index query constrains the table's sort key
	resp := ddbCall(t, srv, "Query", map[string]any{
		"TableName":              "kc-gsi-schema",
		"IndexName":              "gsi1",
		"KeyConditionExpression": "gsipk = :g AND sk = :s",
		"ExpressionAttributeValues": map[string]any{
			":g": map[string]any{"S": "G"},
			":s": map[string]any{"S": "a"},
		},
	})
	defer resp.Body.Close()

	// Then: the index's own sort key is the one reported missed
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	assertValidationMessage(t, resp, "Query condition missed key schema element: gsisk")
}

// ---- Either order -----------------------------------------------------------

// AWS constrains which operator each key may use, never the position the
// condition is written in. Overcast's parser used to require the first
// condition to be an equality, so every one of these was rejected.
func TestQuery_sortKeyConditionWrittenFirst(t *testing.T) {
	cases := []struct {
		name string
		expr string
		want []string
	}{
		{"greaterThan", "sk > :s AND pk = :p", []string{"b", "c"}},
		{"greaterOrEqual", "sk >= :s AND pk = :p", []string{"a", "b", "c"}},
		{"lessThan", "sk < :s AND pk = :p", []string{}},
		{"equal", "sk = :s AND pk = :p", []string{"a"}},
		{"beginsWith", "begins_with(sk, :s) AND pk = :p", []string{"a"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Given: one partition holding sort keys a, b, c
			srv := helpers.NewTestServer(t)
			createKeyCondTable(t, srv, "kc-order", "a", "b", "c")

			// When: the sort-key condition is written before the partition key
			got := querySortKeys(t, srv, "kc-order", tc.expr, map[string]any{
				":p": map[string]any{"S": "P"},
				":s": map[string]any{"S": "a"},
			})

			// Then: it means exactly what the same pair means hash-first
			assertSortKeys(t, got, tc.want...)
		})
	}
}

func TestQuery_betweenWrittenBeforePartitionKey(t *testing.T) {
	// Given: one partition holding sort keys a, b, c
	srv := helpers.NewTestServer(t)
	createKeyCondTable(t, srv, "kc-order-between", "a", "b", "c")

	// When: BETWEEN — whose own AND must not be read as the conjunction —
	// is written before the partition key condition
	got := querySortKeys(t, srv, "kc-order-between", "sk BETWEEN :lo AND :hi AND pk = :p", map[string]any{
		":p":  map[string]any{"S": "P"},
		":lo": map[string]any{"S": "a"},
		":hi": map[string]any{"S": "b"},
	})

	// Then: the bounds are inclusive, as they are hash-first
	assertSortKeys(t, got, "a", "b")
}

// A sort-key condition with no partition-key equality anywhere is the case
// "Query condition missed key schema element" was written for, and writing it
// first must not turn it into a different fault.
func TestQuery_sortKeyConditionAlone(t *testing.T) {
	// Given: a hash+sort table
	srv := helpers.NewTestServer(t)
	createKeyCondTable(t, srv, "kc-sort-alone", "a")

	// When: only the sort key is constrained
	resp := queryKeyCond(t, srv, "kc-sort-alone", "sk > :s", map[string]any{
		":s": map[string]any{"S": "a"},
	})
	defer resp.Body.Close()

	// Then: the partition key is the element reported missed
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	assertValidationMessage(t, resp, "Query condition missed key schema element: pk")
}

// ---- One condition per key ---------------------------------------------------

func TestQuery_twoConditionsOnOneKey(t *testing.T) {
	cases := []struct {
		name   string
		expr   string
		values map[string]any
	}{
		{
			"partitionKeyTwice",
			"pk = :p AND pk = :q",
			map[string]any{":p": map[string]any{"S": "P"}, ":q": map[string]any{"S": "Q"}},
		},
		{
			"partitionKeyEqualityAndBeginsWith",
			"pk = :p AND begins_with(pk, :q)",
			map[string]any{":p": map[string]any{"S": "P"}, ":q": map[string]any{"S": "P"}},
		},
		{
			"sortKeyRangePair",
			"sk >= :lo AND sk <= :hi",
			map[string]any{":lo": map[string]any{"S": "a"}, ":hi": map[string]any{"S": "b"}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Given: a hash+sort table
			srv := helpers.NewTestServer(t)
			createKeyCondTable(t, srv, "kc-dup", "a", "b")

			// When: both conditions constrain the same attribute
			resp := queryKeyCond(t, srv, "kc-dup", tc.expr, tc.values)
			defer resp.Body.Close()

			// Then: AWS rejects the pair rather than honouring one of them
			helpers.AssertStatus(t, resp, http.StatusBadRequest)
			assertValidationMessage(t, resp, wantOneConditionPerKey)
		})
	}
}

// ---- Parentheses ------------------------------------------------------------

// Expression builders parenthesise what they emit, so a key condition reaches
// the service wrapped in brackets far more often than a hand-written one does.
// AWS parses them and then judges the conditions inside: planetlabs/datalake-api#21
// is a real account answering "KeyConditionExpressions must only contain one
// condition per key" — a semantic verdict — for the generated
// "(#n0 = :v0 AND begins_with(#n0, :v0))", which it could not have reached had
// the brackets themselves been a syntax error.
func TestQuery_parenthesisedKeyCondition(t *testing.T) {
	cases := []struct {
		name string
		expr string
		want []string
	}{
		{"aroundEachCondition", "(pk = :p) AND (sk = :s)", []string{"a"}},
		{"aroundTheSortCondition", "pk = :p AND (begins_with(sk, :s))", []string{"a"}},
		{"aroundTheWholeExpression", "(pk = :p AND sk = :s)", []string{"a"}},
		{"aroundTheHashConditionOnly", "(pk = :p)", []string{"a", "b"}},
		{"nested", "((pk = :p) AND (sk = :s))", []string{"a"}},
		{"reversedAndParenthesised", "(sk = :s) AND (pk = :p)", []string{"a"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Given: one partition holding a and b
			srv := helpers.NewTestServer(t)
			createKeyCondTable(t, srv, "kc-parens", "a", "b")

			// When: the condition is written with brackets
			got := querySortKeys(t, srv, "kc-parens", tc.expr, map[string]any{
				":p": map[string]any{"S": "P"},
				":s": map[string]any{"S": "a"},
			})

			// Then: the brackets change nothing about what it means
			assertSortKeys(t, got, tc.want...)
		})
	}
}

// The verdict inside the brackets is still a verdict on the conditions, and
// this is the exact expression a real account answered that way.
func TestQuery_parenthesisedDuplicateKeyCondition(t *testing.T) {
	// Given: a hash+sort table
	srv := helpers.NewTestServer(t)
	createKeyCondTable(t, srv, "kc-parens-dup", "a")

	// When: a generated expression applies two conditions to one key
	resp := ddbCall(t, srv, "Query", map[string]any{
		"TableName":                "kc-parens-dup",
		"KeyConditionExpression":   "(#n0 = :v0 AND begins_with(#n0, :v0))",
		"ExpressionAttributeNames": map[string]any{"#n0": "pk"},
		"ExpressionAttributeValues": map[string]any{
			":v0": map[string]any{"S": "P"},
		},
	})
	defer resp.Body.Close()

	// Then: the brackets are parsed and the conditions inside are rejected
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	assertValidationMessage(t, resp, wantOneConditionPerKey)
}

// Unbalanced brackets are a syntax error, not a condition to evaluate.
func TestQuery_unbalancedParentheses(t *testing.T) {
	// Given: a hash+sort table
	srv := helpers.NewTestServer(t)
	createKeyCondTable(t, srv, "kc-parens-bad", "a")

	// When: a bracket is left open
	resp := queryKeyCond(t, srv, "kc-parens-bad", "(pk = :p AND sk = :s", map[string]any{
		":p": map[string]any{"S": "P"},
		":s": map[string]any{"S": "a"},
	})
	defer resp.Body.Close()

	// Then: the request is rejected rather than read as if the bracket closed
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
}

// ---- The sort-key operator matrix -------------------------------------------

// Every operator the Query API reference lists for the sort key, end to end
// over one partition, so the seven forms are pinned at the wire rather than
// only in the parser's unit tests.
func TestQuery_sortKeyOperatorMatrix(t *testing.T) {
	cases := []struct {
		name   string
		expr   string
		values map[string]any
		want   []string
	}{
		{"equal", "pk = :p AND sk = :s", map[string]any{":s": map[string]any{"S": "b2"}}, []string{"b2"}},
		{"lessThan", "pk = :p AND sk < :s", map[string]any{":s": map[string]any{"S": "b2"}}, []string{"a1", "b1"}},
		{"lessOrEqual", "pk = :p AND sk <= :s", map[string]any{":s": map[string]any{"S": "b2"}}, []string{"a1", "b1", "b2"}},
		{"greaterThan", "pk = :p AND sk > :s", map[string]any{":s": map[string]any{"S": "b2"}}, []string{"c1"}},
		{"greaterOrEqual", "pk = :p AND sk >= :s", map[string]any{":s": map[string]any{"S": "b2"}}, []string{"b2", "c1"}},
		{
			"between",
			"pk = :p AND sk BETWEEN :lo AND :hi",
			map[string]any{":lo": map[string]any{"S": "b1"}, ":hi": map[string]any{"S": "b2"}},
			[]string{"b1", "b2"},
		},
		{"beginsWith", "pk = :p AND begins_with(sk, :s)", map[string]any{":s": map[string]any{"S": "b"}}, []string{"b1", "b2"}},
		{"noSortCondition", "pk = :p", nil, []string{"a1", "b1", "b2", "c1"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Given: one partition holding a1, b1, b2, c1
			srv := helpers.NewTestServer(t)
			createKeyCondTable(t, srv, "kc-ops", "a1", "b1", "b2", "c1")

			values := map[string]any{":p": map[string]any{"S": "P"}}
			for k, v := range tc.values {
				values[k] = v
			}

			// When: the sort key is constrained with that operator
			got := querySortKeys(t, srv, "kc-ops", tc.expr, values)

			// Then: the matched range is the one the API reference defines,
			// returned in ascending sort-key order
			assertSortKeys(t, got, tc.want...)
		})
	}
}

// A reserved word is legal as a key attribute name when it is reached through
// an ExpressionAttributeNames alias — the documented escape hatch — and that
// must hold for a key condition written in either order.
func TestQuery_reservedWordKeyNamesViaAlias(t *testing.T) {
	// Given: a table whose keys are both DynamoDB reserved words
	srv := helpers.NewTestServer(t)
	createTableWithSortKey(t, srv, "kc-reserved", "name", "S", "timestamp", "S")
	for _, ts := range []string{"t1", "t2"} {
		putItem(t, srv, "kc-reserved", map[string]any{
			"name":      map[string]any{"S": "n"},
			"timestamp": map[string]any{"S": ts},
		})
	}

	// When: both are reached through aliases, sort key first
	resp := ddbCall(t, srv, "Query", map[string]any{
		"TableName":                "kc-reserved",
		"KeyConditionExpression":   "begins_with(#ts, :t) AND #n = :n",
		"ExpressionAttributeNames": map[string]any{"#n": "name", "#ts": "timestamp"},
		"ExpressionAttributeValues": map[string]any{
			":n": map[string]any{"S": "n"},
			":t": map[string]any{"S": "t"},
		},
	})
	defer resp.Body.Close()

	// Then: the alias resolves to the key on both sides of the AND
	helpers.AssertStatus(t, resp, http.StatusOK)
	var out struct {
		Count int `json:"Count"`
	}
	helpers.DecodeJSON(t, resp, &out)
	if out.Count != 2 {
		t.Errorf("Count = %d, want 2", out.Count)
	}
}

// ---- ScanIndexForward --------------------------------------------------------

// Query results are sorted by sort key, ascending unless ScanIndexForward is
// false. The LSI read path already pins this; the base table is the more
// commonly exercised one.
func TestQuery_ScanIndexForward_baseTable(t *testing.T) {
	cases := []struct {
		name    string
		forward *bool
		want    []string
	}{
		{"default", nil, []string{"a", "b", "c"}},
		{"forward", boolPtr(true), []string{"a", "b", "c"}},
		{"reverse", boolPtr(false), []string{"c", "b", "a"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Given: one partition holding a, b, c
			srv := helpers.NewTestServer(t)
			createKeyCondTable(t, srv, "kc-order-dir", "a", "b", "c")

			payload := map[string]any{
				"TableName":              "kc-order-dir",
				"KeyConditionExpression": "pk = :p",
				"ExpressionAttributeValues": map[string]any{
					":p": map[string]any{"S": "P"},
				},
			}
			if tc.forward != nil {
				payload["ScanIndexForward"] = *tc.forward
			}

			// When: the query runs with that direction
			resp := ddbCall(t, srv, "Query", payload)
			defer resp.Body.Close()
			helpers.AssertStatus(t, resp, http.StatusOK)

			var out struct {
				Items []map[string]map[string]any `json:"Items"`
			}
			helpers.DecodeJSON(t, resp, &out)
			got := make([]string, 0, len(out.Items))
			for _, item := range out.Items {
				s, _ := item["sk"]["S"].(string)
				got = append(got, s)
			}

			// Then: ascending by default, descending when explicitly reversed
			assertSortKeys(t, got, tc.want...)
		})
	}
}

// Paging backwards must resume from the cursor in the reversed order, not
// restart at the ascending end.
func TestQuery_ScanIndexForward_reversePagination(t *testing.T) {
	// Given: one partition holding a, b, c, d
	srv := helpers.NewTestServer(t)
	createKeyCondTable(t, srv, "kc-rev-page", "a", "b", "c", "d")

	page := func(startKey map[string]any) ([]string, map[string]any) {
		t.Helper()
		payload := map[string]any{
			"TableName":              "kc-rev-page",
			"KeyConditionExpression": "pk = :p",
			"ExpressionAttributeValues": map[string]any{
				":p": map[string]any{"S": "P"},
			},
			"ScanIndexForward": false,
			"Limit":            2,
		}
		if startKey != nil {
			payload["ExclusiveStartKey"] = startKey
		}
		resp := ddbCall(t, srv, "Query", payload)
		defer resp.Body.Close()
		helpers.AssertStatus(t, resp, http.StatusOK)

		var out struct {
			Items            []map[string]map[string]any `json:"Items"`
			LastEvaluatedKey map[string]any              `json:"LastEvaluatedKey"`
		}
		helpers.DecodeJSON(t, resp, &out)
		got := make([]string, 0, len(out.Items))
		for _, item := range out.Items {
			s, _ := item["sk"]["S"].(string)
			got = append(got, s)
		}
		return got, out.LastEvaluatedKey
	}

	// When: the partition is paged two at a time in reverse
	first, cursor := page(nil)
	assertSortKeys(t, first, "d", "c")
	if cursor == nil {
		t.Fatal("expected LastEvaluatedKey after the first reverse page")
	}

	second, tail := page(cursor)

	// Then: the second page continues downwards and the walk terminates
	assertSortKeys(t, second, "b", "a")
	if tail != nil {
		t.Errorf("LastEvaluatedKey = %v, want nil once the partition is exhausted", tail)
	}
}

// ---- Limit, Select=COUNT and the cursor shape --------------------------------

// Limit caps items READ, so a Select=COUNT page reports the filtered Count
// against the unfiltered ScannedCount and still carries the cursor that
// continues the walk.
func TestQuery_SelectCOUNT_withLimitAndFilter(t *testing.T) {
	// Given: one partition of 5 items, only the last two matching the filter
	srv := helpers.NewTestServer(t)
	createTableWithSortKey(t, srv, "kc-count-page", "pk", "S", "sk", "N")
	for i := 1; i <= 5; i++ {
		flag := "skip"
		if i > 3 {
			flag = "keep"
		}
		putItem(t, srv, "kc-count-page", map[string]any{
			"pk":   map[string]any{"S": "P"},
			"sk":   map[string]any{"N": strconv.Itoa(i)},
			"flag": map[string]any{"S": flag},
		})
	}

	// When: Select=COUNT reads a window of 3 and filters it
	resp := ddbCall(t, srv, "Query", map[string]any{
		"TableName":              "kc-count-page",
		"KeyConditionExpression": "pk = :p",
		"FilterExpression":       "flag = :keep",
		"ExpressionAttributeValues": map[string]any{
			":p":    map[string]any{"S": "P"},
			":keep": map[string]any{"S": "keep"},
		},
		"Limit":  3,
		"Select": "COUNT",
	})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)

	var raw map[string]any
	helpers.DecodeJSON(t, resp, &raw)

	// Then: Count is post-filter, ScannedCount is the read window, no Items,
	// and the cursor names the third item so the walk can continue
	if count, _ := raw["Count"].(float64); count != 0 {
		t.Errorf("Count = %v, want 0 (none of the first 3 pass the filter)", raw["Count"])
	}
	if sc, _ := raw["ScannedCount"].(float64); sc != 3 {
		t.Errorf("ScannedCount = %v, want 3 (Limit caps items read)", raw["ScannedCount"])
	}
	if _, hasItems := raw["Items"]; hasItems {
		t.Error("Items must not be present when Select=COUNT")
	}
	lek, _ := raw["LastEvaluatedKey"].(map[string]any)
	if lek == nil {
		t.Fatal("expected LastEvaluatedKey: the read window stopped before the partition did")
	}
	sk, _ := lek["sk"].(map[string]any)
	if got, _ := sk["N"].(string); got != "3" {
		t.Errorf("LastEvaluatedKey sk = %v, want \"3\"", sk)
	}
}

// An LSI query's cursor carries the table's primary key; the index's sort key
// is one of those attributes already, but a client must be able to feed the
// cursor straight back and the walk must not repeat or skip an item.
func TestQuery_LSI_paginationCursorRoundTrips(t *testing.T) {
	// Given: a table with an LSI, one partition of 4 items
	srv := helpers.NewTestServer(t)
	createLSIKeyCondTable(t, srv, "kc-lsi-page")
	for i := 1; i <= 4; i++ {
		putItem(t, srv, "kc-lsi-page", map[string]any{
			"pk":    map[string]any{"S": "P"},
			"sk":    map[string]any{"S": fmt.Sprintf("s%d", i)},
			"lsisk": map[string]any{"S": fmt.Sprintf("l%d", i)},
		})
	}

	seen := make([]string, 0, 4)
	var cursor map[string]any
	for page := 0; page < 4; page++ {
		payload := map[string]any{
			"TableName":              "kc-lsi-page",
			"IndexName":              "lsi1",
			"KeyConditionExpression": "pk = :p",
			"ExpressionAttributeValues": map[string]any{
				":p": map[string]any{"S": "P"},
			},
			"Limit": 2,
		}
		if cursor != nil {
			payload["ExclusiveStartKey"] = cursor
		}
		resp := ddbCall(t, srv, "Query", payload)
		helpers.AssertStatus(t, resp, http.StatusOK)

		var out struct {
			Items            []map[string]map[string]any `json:"Items"`
			LastEvaluatedKey map[string]any              `json:"LastEvaluatedKey"`
		}
		helpers.DecodeJSON(t, resp, &out)
		resp.Body.Close()

		for _, item := range out.Items {
			s, _ := item["lsisk"]["S"].(string)
			seen = append(seen, s)
		}
		if out.LastEvaluatedKey == nil {
			break
		}
		// The cursor must carry the table's own key schema, or a client
		// cannot feed it back.
		for _, attr := range []string{"pk", "sk"} {
			if _, ok := out.LastEvaluatedKey[attr]; !ok {
				t.Fatalf("LastEvaluatedKey %v is missing table key attribute %q", out.LastEvaluatedKey, attr)
			}
		}
		cursor = out.LastEvaluatedKey
	}

	// Then: every item was seen exactly once, in index order
	assertSortKeys(t, seen, "l1", "l2", "l3", "l4")
}

// ---- Test helpers ------------------------------------------------------------

func boolPtr(b bool) *bool { return &b }

// createGSIKeyCondTable makes a table keyed (pk, sk) with a GSI keyed
// (gsipk, gsisk), so an index query has a key schema of its own that differs
// from the table's in both halves.
func createGSIKeyCondTable(t *testing.T, srv *helpers.TestServer, name string) {
	t.Helper()
	resp := ddbCall(t, srv, "CreateTable", map[string]any{
		"TableName": name,
		"AttributeDefinitions": []map[string]any{
			{"AttributeName": "pk", "AttributeType": "S"},
			{"AttributeName": "sk", "AttributeType": "S"},
			{"AttributeName": "gsipk", "AttributeType": "S"},
			{"AttributeName": "gsisk", "AttributeType": "S"},
		},
		"KeySchema": []map[string]any{
			{"AttributeName": "pk", "KeyType": "HASH"},
			{"AttributeName": "sk", "KeyType": "RANGE"},
		},
		"GlobalSecondaryIndexes": []map[string]any{{
			"IndexName": "gsi1",
			"KeySchema": []map[string]any{
				{"AttributeName": "gsipk", "KeyType": "HASH"},
				{"AttributeName": "gsisk", "KeyType": "RANGE"},
			},
			"Projection": map[string]any{"ProjectionType": "ALL"},
		}},
		"BillingMode": "PAY_PER_REQUEST",
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("createGSIKeyCondTable %q: status %d: %s", name, resp.StatusCode, helpers.ReadBody(t, resp))
	}
}

// createLSIKeyCondTable makes a table keyed (pk, sk) with an LSI that reuses
// pk and sorts on lsisk.
func createLSIKeyCondTable(t *testing.T, srv *helpers.TestServer, name string) {
	t.Helper()
	resp := ddbCall(t, srv, "CreateTable", map[string]any{
		"TableName": name,
		"AttributeDefinitions": []map[string]any{
			{"AttributeName": "pk", "AttributeType": "S"},
			{"AttributeName": "sk", "AttributeType": "S"},
			{"AttributeName": "lsisk", "AttributeType": "S"},
		},
		"KeySchema": []map[string]any{
			{"AttributeName": "pk", "KeyType": "HASH"},
			{"AttributeName": "sk", "KeyType": "RANGE"},
		},
		"LocalSecondaryIndexes": []map[string]any{{
			"IndexName": "lsi1",
			"KeySchema": []map[string]any{
				{"AttributeName": "pk", "KeyType": "HASH"},
				{"AttributeName": "lsisk", "KeyType": "RANGE"},
			},
			"Projection": map[string]any{"ProjectionType": "ALL"},
		}},
		"BillingMode": "PAY_PER_REQUEST",
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("createLSIKeyCondTable %q: status %d: %s", name, resp.StatusCode, helpers.ReadBody(t, resp))
	}
}
