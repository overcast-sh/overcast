package dynamodb_test

// cbor_test.go covers DynamoDB's core item operations over Smithy RPC v2 CBOR
// (issue #136). Before this file, DynamoDB's only CBOR coverage was
// TestRPCv2CBOR_DynamoDBListTables (protocol_dispatch_test.go),
// TestRPCv2CBOR_StreamListStreams (streams_test.go) and
// TestRPCv2CBOR_UpdateTimeToLive (ttl_transitions_test.go) — three smoke
// tests naming no item operation and no AttributeValue type. Every test here
// runs the identical request over rpcv2Cbor and over awsJson1_0 and compares
// the two answers field-by-field (see normaliseAttrShape), the same
// technique cloudwatch_cbor_test.go's TestCBOR_MatchesJSON uses.
//
// One genuine divergence was found while writing this coverage — see
// docs/dev/compatibility/services/dynamodb.yaml's cbor_coverage finding: a
// Binary (S or B) key attribute sent over rpcv2Cbor decodes to a raw []byte,
// and extractKeyValue/extractScalar (expr.go) only recognise a key's scalar
// value when it arrives as a Go string (always true for JSON, never true for
// a CBOR byte string). Every operation that takes a Key or an Item on a
// table whose hash or range key is Binary therefore answers a 400
// ValidationException ("cannot contain an empty string value") over CBOR
// where the identical request succeeds over JSON. This file's coverage is
// deliberately restricted to Binary as a non-key attribute, where the
// AWS-correct shape (a CBOR byte string, not base64 text) actually round
// -trips — the failing key case is intentionally not included as a passing
// test, per this review's brief, and is documented instead.

import (
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"sort"
	"strconv"
	"testing"

	cborlib "github.com/fxamacker/cbor/v2"

	"github.com/overcast-sh/overcast/tests/helpers"
)

// ---- shared test infrastructure --------------------------------------------

// ddbCBORDecMode mirrors the decode mode internal/protocol/codec/cbor.go
// builds for the production RPCv2CBOR codec: CBOR maps decoded into an `any`
// slot (a nested List/Map AttributeValue member, or an open document) come
// back as map[string]any rather than the library's default map[any]any, so
// they compare against a JSON-decoded response — which is always
// map[string]any — without a second normalisation step for keys.
var ddbCBORDecMode = func() cborlib.DecMode {
	mode, err := cborlib.DecOptions{
		DefaultMapType: reflect.TypeOf(map[string]any(nil)),
	}.DecMode()
	if err != nil {
		panic("cbor_test: building decode mode: " + err.Error())
	}
	return mode
}()

// decodeCBORBody reads and closes resp.Body, decoding it into out with
// ddbCBORDecMode.
func decodeCBORBody(t *testing.T, resp *http.Response, out any) {
	t.Helper()
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read CBOR body: %v", err)
	}
	if err := ddbCBORDecMode.Unmarshal(body, out); err != nil {
		t.Fatalf("decode CBOR body: %v\nbody: %x", err, body)
	}
}

// cborCreateTable creates a hash-key-only table, the CBOR-test equivalent of
// dynamodb_test.go's createTable (which hardcodes the key name "id").
func cborCreateTable(t *testing.T, srv *helpers.TestServer, name, hashKey, hashType string) {
	t.Helper()
	resp := ddbCall(t, srv, "CreateTable", map[string]any{
		"TableName":            name,
		"AttributeDefinitions": []map[string]any{{"AttributeName": hashKey, "AttributeType": hashType}},
		"KeySchema":            []map[string]any{{"AttributeName": hashKey, "KeyType": "HASH"}},
		"BillingMode":          "PAY_PER_REQUEST",
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("createTable %q: status %d", name, resp.StatusCode)
	}
}

// putCBORItem is putItem's CBOR twin.
func putCBORItem(t *testing.T, srv *helpers.TestServer, tableName string, item map[string]any) {
	t.Helper()
	resp := dynamodbCBORCall(t, srv, "PutItem", map[string]any{
		"TableName": tableName,
		"Item":      item,
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("putCBORItem into %q: status %d", tableName, resp.StatusCode)
	}
}

// mergeMap returns a new map holding every key of a and then of b — b's
// values win on a shared key. Used to build a Query/UpdateItem body shared
// between the CBOR and JSON legs of a test, differing only in TableName.
func mergeMap(a, b map[string]any) map[string]any {
	out := make(map[string]any, len(a)+len(b))
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		out[k] = v
	}
	return out
}

// normaliseAttrShape rewrites a decoded DynamoDB response so a CBOR answer
// and a JSON answer to the identical request compare equal:
//
//   - CBOR decodes a whole number as int64/uint64 where JSON has only
//     float64 — this affects plain response members like Count and
//     ScannedCount, never an AttributeValue's own N (which is always a wire
//     string on both protocols).
//   - CBOR decodes a Binary (B) or Binary Set (BS) AttributeValue payload as
//     a raw []byte; JSON has no binary wire type, so DynamoDB JSON always
//     carries the same bytes as base64 text. Both are canonicalised to
//     base64 text here so the two protocols' equally-correct encodings of
//     the same bytes compare equal by value — this is the expected
//     difference the CBOR codec exists to produce, not a bug. See
//     TestRPCv2CBOR_PutGetItem_AttributeTypes's direct assertion that CBOR's
//     own wire form really is a byte string, not text, before this
//     normalisation hides that fact.
func normaliseAttrShape(v any) any {
	switch value := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(value))
		for key, member := range value {
			out[key] = normaliseAttrShape(member)
		}
		return out
	case map[any]any:
		out := make(map[string]any, len(value))
		keys := make([]string, 0, len(value))
		for key := range value {
			name, ok := key.(string)
			if !ok {
				continue
			}
			keys = append(keys, name)
		}
		sort.Strings(keys)
		for _, key := range keys {
			out[key] = normaliseAttrShape(value[key])
		}
		return out
	case []any:
		out := make([]any, len(value))
		for i, member := range value {
			out[i] = normaliseAttrShape(member)
		}
		return out
	case []map[string]any:
		out := make([]any, len(value))
		for i, member := range value {
			out[i] = normaliseAttrShape(member)
		}
		return out
	case []byte:
		return base64.StdEncoding.EncodeToString(value)
	case int64:
		return float64(value)
	case uint64:
		return float64(value)
	case int:
		return float64(value)
	case float32:
		return float64(value)
	default:
		return v
	}
}

// cborErrorEnvelope is the __type/message shape both WriteJSONError and
// RPCv2CBOR.WriteError produce (internal/protocol/errors.go,
// internal/protocol/codec/cbor.go).
type cborErrorEnvelope struct {
	Type    string `cbor:"__type" json:"__type"`
	Message string `cbor:"message" json:"message"`
}

// ---- PutItem / GetItem: every AttributeValue type --------------------------

// TestRPCv2CBOR_PutGetItem_AttributeTypes round-trips one item carrying every
// documented AttributeValue type (S, N, B, BOOL, NULL, L, M, SS, NS, BS)
// through PutItem and GetItem over rpcv2Cbor, and checks the result against
// the identical item over awsJson1_0. Binary is exercised as a plain
// attribute, not a key — see the file header for why a Binary key is left
// out of this file's coverage.
func TestRPCv2CBOR_PutGetItem_AttributeTypes(t *testing.T) {
	srv := helpers.NewTestServer(t)
	// Given: two identically-shaped tables, one exercised per protocol, so a
	// difference in the two answers cannot be explained by shared state.
	cborCreateTable(t, srv, "cbor-types", "pk", "S")
	cborCreateTable(t, srv, "json-types", "pk", "S")

	binBytes := []byte{0xDE, 0xAD, 0xBE, 0xEF, 0x00, 0x01}
	binSetA := []byte{0x01, 0x02}
	binSetB := []byte{0x03, 0x04}

	item := map[string]any{
		"str":        map[string]any{"S": "hello world"},
		"numInt":     map[string]any{"N": "42"},
		"numNeg":     map[string]any{"N": "-17"},
		"numDecimal": map[string]any{"N": "3.14159"},
		"numLarge":   map[string]any{"N": "123456789012345678901234567890"},
		"bin":        map[string]any{"B": binBytes},
		"flag":       map[string]any{"BOOL": true},
		"nothing":    map[string]any{"NULL": true},
		"list": map[string]any{"L": []any{
			map[string]any{"S": "a"},
			map[string]any{"N": "1"},
			map[string]any{"BOOL": false},
		}},
		"nested": map[string]any{"M": map[string]any{
			"inner":  map[string]any{"S": "v"},
			"amount": map[string]any{"N": "2"},
			"deeper": map[string]any{"M": map[string]any{"x": map[string]any{"N": "9"}}},
		}},
		"strSet": map[string]any{"SS": []any{"a", "b", "c"}},
		"numSet": map[string]any{"NS": []any{"1", "2", "3"}},
		"binSet": map[string]any{"BS": []any{binSetA, binSetB}},
	}

	// When: the item is written and read back over rpcv2Cbor...
	cborItem := mergeMap(item, map[string]any{"pk": map[string]any{"S": "item"}})
	putCBOR := dynamodbCBORCall(t, srv, "PutItem", map[string]any{"TableName": "cbor-types", "Item": cborItem})
	helpers.AssertStatus(t, putCBOR, http.StatusOK)
	putCBOR.Body.Close()

	getCBOR := dynamodbCBORCall(t, srv, "GetItem", map[string]any{
		"TableName": "cbor-types",
		"Key":       map[string]any{"pk": map[string]any{"S": "item"}},
	})
	helpers.AssertStatus(t, getCBOR, http.StatusOK)
	var cborOut struct {
		Item map[string]any `cbor:"Item"`
	}
	decodeCBORBody(t, getCBOR, &cborOut)

	// ...and the same item over awsJson1_0.
	jsonItem := mergeMap(item, map[string]any{"pk": map[string]any{"S": "item"}})
	putJSON := ddbCall(t, srv, "PutItem", map[string]any{"TableName": "json-types", "Item": jsonItem})
	helpers.AssertStatus(t, putJSON, http.StatusOK)
	putJSON.Body.Close()

	getJSON := ddbCall(t, srv, "GetItem", map[string]any{
		"TableName": "json-types",
		"Key":       map[string]any{"pk": map[string]any{"S": "item"}},
	})
	helpers.AssertStatus(t, getJSON, http.StatusOK)
	var jsonOut struct {
		Item map[string]any `json:"Item"`
	}
	helpers.DecodeJSON(t, getJSON, &jsonOut)

	// Then: the two items are the same, attribute for attribute, once run
	// through the same canonical form.
	normCBOR := normaliseAttrShape(cborOut.Item)
	normJSON := normaliseAttrShape(jsonOut.Item)
	if !reflect.DeepEqual(normCBOR, normJSON) {
		t.Fatalf("rpcv2Cbor and awsJson1_0 GetItem answers differ:\n cbor = %#v\n json = %#v", normCBOR, normJSON)
	}

	// And: CBOR's own wire form for Binary really is a byte string, not
	// base64 text — the raw bytes decode straight into []byte with no
	// base64 step, exactly the efficiency RPC v2 CBOR exists to offer a
	// binary-heavy item. Checked before normaliseAttrShape's base64
	// canonicalisation above hides the difference.
	binAttr, ok := cborOut.Item["bin"].(map[string]any)
	if !ok {
		t.Fatalf("bin attribute missing or malformed: %#v", cborOut.Item["bin"])
	}
	gotBin, ok := binAttr["B"].([]byte)
	if !ok {
		t.Fatalf("B value decoded as %T, want []byte (a CBOR byte string)", binAttr["B"])
	}
	if string(gotBin) != string(binBytes) {
		t.Fatalf("B value = %x, want %x", gotBin, binBytes)
	}

	binSetAttr, ok := cborOut.Item["binSet"].(map[string]any)
	if !ok {
		t.Fatalf("binSet attribute missing or malformed: %#v", cborOut.Item["binSet"])
	}
	binSetElems, ok := binSetAttr["BS"].([]any)
	if !ok || len(binSetElems) != 2 {
		t.Fatalf("BS = %#v, want 2 elements", binSetAttr["BS"])
	}
	for i, want := range [][]byte{binSetA, binSetB} {
		got, ok := binSetElems[i].([]byte)
		if !ok {
			t.Fatalf("BS[%d] decoded as %T, want []byte", i, binSetElems[i])
		}
		if string(got) != string(want) {
			t.Fatalf("BS[%d] = %x, want %x", i, got, want)
		}
	}
}

// ---- UpdateItem: SET/ADD on a number and a set, ReturnValues=ALL_NEW ------

// TestRPCv2CBOR_UpdateItem_SetAddReturnValues issues the identical
// UpdateExpression (a SET creating a new attribute, an ADD incrementing a
// Number and unioning a String Set) with ReturnValues=ALL_NEW over rpcv2Cbor
// and over awsJson1_0, and checks the two Attributes answers match.
func TestRPCv2CBOR_UpdateItem_SetAddReturnValues(t *testing.T) {
	srv := helpers.NewTestServer(t)
	cborCreateTable(t, srv, "upd-cbor", "pk", "S")
	cborCreateTable(t, srv, "upd-json", "pk", "S")

	base := map[string]any{
		"pk":   map[string]any{"S": "item"},
		"ctr":  map[string]any{"N": "10"},
		"tags": map[string]any{"SS": []any{"a", "b"}},
	}

	// Given: the same base item on both tables.
	putCBORItem(t, srv, "upd-cbor", base)
	putItem(t, srv, "upd-json", base)

	update := map[string]any{
		"Key":              map[string]any{"pk": map[string]any{"S": "item"}},
		"UpdateExpression": "SET note = :n ADD ctr :inc, tags :newtag",
		"ExpressionAttributeValues": map[string]any{
			":n":      map[string]any{"S": "updated"},
			":inc":    map[string]any{"N": "5"},
			":newtag": map[string]any{"SS": []any{"c"}},
		},
		"ReturnValues": "ALL_NEW",
	}

	// When: the same UpdateItem is issued over rpcv2Cbor and over
	// awsJson1_0.
	updCBOR := dynamodbCBORCall(t, srv, "UpdateItem", mergeMap(update, map[string]any{"TableName": "upd-cbor"}))
	helpers.AssertStatus(t, updCBOR, http.StatusOK)
	var cborOut struct {
		Attributes map[string]any `cbor:"Attributes"`
	}
	decodeCBORBody(t, updCBOR, &cborOut)

	updJSON := ddbCall(t, srv, "UpdateItem", mergeMap(update, map[string]any{"TableName": "upd-json"}))
	helpers.AssertStatus(t, updJSON, http.StatusOK)
	var jsonOut struct {
		Attributes map[string]any `json:"Attributes"`
	}
	helpers.DecodeJSON(t, updJSON, &jsonOut)

	// Then: both answers agree once normalised...
	normCBOR := normaliseAttrShape(cborOut.Attributes)
	normJSON := normaliseAttrShape(jsonOut.Attributes)
	if !reflect.DeepEqual(normCBOR, normJSON) {
		t.Fatalf("UpdateItem rpcv2Cbor/awsJson1_0 mismatch:\n cbor = %#v\n json = %#v", normCBOR, normJSON)
	}

	// ...and are what SET/ADD actually imply, not two protocols agreeing on
	// nothing.
	ctr, ok := cborOut.Attributes["ctr"].(map[string]any)
	if !ok || ctr["N"] != "15" {
		t.Fatalf("ctr = %#v, want N:15", cborOut.Attributes["ctr"])
	}
	note, ok := cborOut.Attributes["note"].(map[string]any)
	if !ok || note["S"] != "updated" {
		t.Fatalf("note = %#v, want S:updated", cborOut.Attributes["note"])
	}
	tags, ok := cborOut.Attributes["tags"].(map[string]any)
	if !ok {
		t.Fatalf("tags missing: %#v", cborOut.Attributes["tags"])
	}
	tagList, ok := tags["SS"].([]any)
	if !ok || len(tagList) != 3 {
		t.Fatalf("tags SS = %#v, want 3 elements (a, b, c)", tags["SS"])
	}
}

// ---- Query: KeyConditionExpression, LastEvaluatedKey, ExclusiveStartKey ---

// TestRPCv2CBOR_Query_KeyConditionAndPagination pins Query's
// KeyConditionExpression, LastEvaluatedKey shape and ExclusiveStartKey
// pagination over rpcv2Cbor against the identical awsJson1_0 request, then
// fetches a second page entirely within CBOR using the first page's own
// LastEvaluatedKey — the shape a real SDK would feed back in unmodified.
func TestRPCv2CBOR_Query_KeyConditionAndPagination(t *testing.T) {
	srv := helpers.NewTestServer(t)
	for _, name := range []string{"q-cbor", "q-json"} {
		resp := ddbCall(t, srv, "CreateTable", map[string]any{
			"TableName": name,
			"AttributeDefinitions": []map[string]any{
				{"AttributeName": "pk", "AttributeType": "S"},
				{"AttributeName": "sk", "AttributeType": "S"},
			},
			"KeySchema": []map[string]any{
				{"AttributeName": "pk", "KeyType": "HASH"},
				{"AttributeName": "sk", "KeyType": "RANGE"},
			},
			"BillingMode": "PAY_PER_REQUEST",
		})
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("CreateTable %q: status %d", name, resp.StatusCode)
		}
		resp.Body.Close()
	}
	for _, sk := range []string{"item#1", "item#2", "item#3", "item#4"} {
		putItem(t, srv, "q-json", map[string]any{"pk": map[string]any{"S": "user#1"}, "sk": map[string]any{"S": sk}})
		putCBORItem(t, srv, "q-cbor", map[string]any{"pk": map[string]any{"S": "user#1"}, "sk": map[string]any{"S": sk}})
	}

	request := map[string]any{
		"KeyConditionExpression":    "pk = :pk",
		"ExpressionAttributeValues": map[string]any{":pk": map[string]any{"S": "user#1"}},
		"Limit":                     2,
	}

	// When: page 1 is fetched over rpcv2Cbor and over awsJson1_0.
	page1CBOR := dynamodbCBORCall(t, srv, "Query", mergeMap(request, map[string]any{"TableName": "q-cbor"}))
	helpers.AssertStatus(t, page1CBOR, http.StatusOK)
	var cborPage1 struct {
		Items            []map[string]any `cbor:"Items"`
		Count            int              `cbor:"Count"`
		LastEvaluatedKey map[string]any   `cbor:"LastEvaluatedKey"`
	}
	decodeCBORBody(t, page1CBOR, &cborPage1)

	page1JSON := ddbCall(t, srv, "Query", mergeMap(request, map[string]any{"TableName": "q-json"}))
	helpers.AssertStatus(t, page1JSON, http.StatusOK)
	var jsonPage1 struct {
		Items            []map[string]any `json:"Items"`
		Count            int              `json:"Count"`
		LastEvaluatedKey map[string]any   `json:"LastEvaluatedKey"`
	}
	helpers.DecodeJSON(t, page1JSON, &jsonPage1)

	// Then: both pages agree, and both paginate.
	if cborPage1.Count != 2 || jsonPage1.Count != 2 {
		t.Fatalf("Count = cbor:%d json:%d, want 2 for both", cborPage1.Count, jsonPage1.Count)
	}
	if cborPage1.LastEvaluatedKey == nil || jsonPage1.LastEvaluatedKey == nil {
		t.Fatalf("expected LastEvaluatedKey on both protocols' first page")
	}
	if !reflect.DeepEqual(normaliseAttrShape(cborPage1.Items), normaliseAttrShape(jsonPage1.Items)) {
		t.Fatalf("page 1 items differ:\n cbor = %#v\n json = %#v",
			normaliseAttrShape(cborPage1.Items), normaliseAttrShape(jsonPage1.Items))
	}
	if !reflect.DeepEqual(normaliseAttrShape(cborPage1.LastEvaluatedKey), normaliseAttrShape(jsonPage1.LastEvaluatedKey)) {
		t.Fatalf("LastEvaluatedKey shape differs:\n cbor = %#v\n json = %#v",
			normaliseAttrShape(cborPage1.LastEvaluatedKey), normaliseAttrShape(jsonPage1.LastEvaluatedKey))
	}

	// When: page 2 is fetched using page 1's own (CBOR-decoded)
	// LastEvaluatedKey as ExclusiveStartKey, entirely within rpcv2Cbor.
	page2CBOR := dynamodbCBORCall(t, srv, "Query", map[string]any{
		"TableName":                 "q-cbor",
		"KeyConditionExpression":    "pk = :pk",
		"ExpressionAttributeValues": map[string]any{":pk": map[string]any{"S": "user#1"}},
		"ExclusiveStartKey":         cborPage1.LastEvaluatedKey,
	})
	helpers.AssertStatus(t, page2CBOR, http.StatusOK)
	var cborPage2 struct {
		Items            []map[string]any `cbor:"Items"`
		Count            int              `cbor:"Count"`
		LastEvaluatedKey map[string]any   `cbor:"LastEvaluatedKey"`
	}
	decodeCBORBody(t, page2CBOR, &cborPage2)

	// Then: the two pages together cover all 4 items exactly once, and the
	// table is now exhausted.
	if cborPage2.Count != 2 {
		t.Fatalf("page 2 Count = %d, want 2", cborPage2.Count)
	}
	if cborPage2.LastEvaluatedKey != nil {
		t.Fatalf("expected no LastEvaluatedKey once the table is exhausted, got %#v", cborPage2.LastEvaluatedKey)
	}
	seen := map[string]bool{}
	for _, page := range [][]map[string]any{cborPage1.Items, cborPage2.Items} {
		for _, it := range page {
			skAttr, _ := it["sk"].(map[string]any)
			sk, _ := skAttr["S"].(string)
			seen[sk] = true
		}
	}
	if len(seen) != 4 {
		t.Fatalf("expected 4 distinct sort keys across both pages, got %v", seen)
	}
}

// ---- Query: a Global Secondary Index --------------------------------------

// TestRPCv2CBOR_Query_GSI queries a GSI over rpcv2Cbor and checks the result
// against the identical query over awsJson1_0.
func TestRPCv2CBOR_Query_GSI(t *testing.T) {
	srv := helpers.NewTestServer(t)
	for _, name := range []string{"gsi-cbor", "gsi-json"} {
		resp := ddbCall(t, srv, "CreateTable", map[string]any{
			"TableName": name,
			"AttributeDefinitions": []map[string]any{
				{"AttributeName": "id", "AttributeType": "S"},
				{"AttributeName": "email", "AttributeType": "S"},
			},
			"KeySchema": []map[string]any{{"AttributeName": "id", "KeyType": "HASH"}},
			"GlobalSecondaryIndexes": []map[string]any{
				{
					"IndexName":  "email-index",
					"KeySchema":  []map[string]any{{"AttributeName": "email", "KeyType": "HASH"}},
					"Projection": map[string]any{"ProjectionType": "ALL"},
				},
			},
			"BillingMode": "PAY_PER_REQUEST",
		})
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("CreateTable %q: status %d", name, resp.StatusCode)
		}
		resp.Body.Close()
	}

	seedUsers := func(put func(item map[string]any), tableName string) {
		put(map[string]any{"id": map[string]any{"S": "u1"}, "email": map[string]any{"S": "alice@example.com"}, "name": map[string]any{"S": "Alice"}})
		put(map[string]any{"id": map[string]any{"S": "u2"}, "email": map[string]any{"S": "bob@example.com"}, "name": map[string]any{"S": "Bob"}})
		put(map[string]any{"id": map[string]any{"S": "u3"}, "email": map[string]any{"S": "alice@example.com"}, "name": map[string]any{"S": "Alice2"}})
	}
	seedUsers(func(item map[string]any) { putCBORItem(t, srv, "gsi-cbor", item) }, "gsi-cbor")
	seedUsers(func(item map[string]any) { putItem(t, srv, "gsi-json", item) }, "gsi-json")

	request := map[string]any{
		"IndexName":                 "email-index",
		"KeyConditionExpression":    "email = :e",
		"ExpressionAttributeValues": map[string]any{":e": map[string]any{"S": "alice@example.com"}},
	}

	// When: the GSI is queried over rpcv2Cbor and over awsJson1_0.
	cborResp := dynamodbCBORCall(t, srv, "Query", mergeMap(request, map[string]any{"TableName": "gsi-cbor"}))
	helpers.AssertStatus(t, cborResp, http.StatusOK)
	var cborOut struct {
		Items []map[string]any `cbor:"Items"`
		Count int              `cbor:"Count"`
	}
	decodeCBORBody(t, cborResp, &cborOut)

	jsonResp := ddbCall(t, srv, "Query", mergeMap(request, map[string]any{"TableName": "gsi-json"}))
	helpers.AssertStatus(t, jsonResp, http.StatusOK)
	var jsonOut struct {
		Items []map[string]any `json:"Items"`
		Count int              `json:"Count"`
	}
	helpers.DecodeJSON(t, jsonResp, &jsonOut)

	// Then: both protocols find the same 2 items via the index.
	if cborOut.Count != 2 || jsonOut.Count != 2 {
		t.Fatalf("Count = cbor:%d json:%d, want 2 for both", cborOut.Count, jsonOut.Count)
	}
	sortByID := func(items []map[string]any) {
		sort.Slice(items, func(a, b int) bool {
			ai, _ := items[a]["id"].(map[string]any)
			bi, _ := items[b]["id"].(map[string]any)
			as, _ := ai["S"].(string)
			bs, _ := bi["S"].(string)
			return as < bs
		})
	}
	sortByID(cborOut.Items)
	sortByID(jsonOut.Items)
	if !reflect.DeepEqual(normaliseAttrShape(cborOut.Items), normaliseAttrShape(jsonOut.Items)) {
		t.Fatalf("GSI query results differ:\n cbor = %#v\n json = %#v",
			normaliseAttrShape(cborOut.Items), normaliseAttrShape(jsonOut.Items))
	}
}

// ---- BatchWriteItem / BatchGetItem -----------------------------------------

// TestRPCv2CBOR_BatchWriteAndBatchGetItem writes 3 items with BatchWriteItem
// and reads them back with BatchGetItem, over rpcv2Cbor and over
// awsJson1_0, into separate tables so the two runs cannot interfere.
func TestRPCv2CBOR_BatchWriteAndBatchGetItem(t *testing.T) {
	srv := helpers.NewTestServer(t)
	cborCreateTable(t, srv, "batch-cbor", "pk", "S")
	cborCreateTable(t, srv, "batch-json", "pk", "S")

	items := make([]map[string]any, 3)
	for i := range items {
		items[i] = map[string]any{
			"pk":  map[string]any{"S": fmt.Sprintf("item#%d", i)},
			"val": map[string]any{"N": strconv.Itoa(i * 10)},
		}
	}

	writeReq := func(tableName string) map[string]any {
		reqs := make([]map[string]any, len(items))
		for i, it := range items {
			reqs[i] = map[string]any{"PutRequest": map[string]any{"Item": it}}
		}
		return map[string]any{"RequestItems": map[string]any{tableName: reqs}}
	}
	keys := func() []map[string]any {
		out := make([]map[string]any, len(items))
		for i, it := range items {
			out[i] = map[string]any{"pk": it["pk"]}
		}
		return out
	}

	// When: BatchWriteItem writes the same 3 items over rpcv2Cbor and over
	// awsJson1_0.
	bwCBOR := dynamodbCBORCall(t, srv, "BatchWriteItem", writeReq("batch-cbor"))
	helpers.AssertStatus(t, bwCBOR, http.StatusOK)
	bwCBOR.Body.Close()

	bwJSON := ddbCall(t, srv, "BatchWriteItem", writeReq("batch-json"))
	helpers.AssertStatus(t, bwJSON, http.StatusOK)
	bwJSON.Body.Close()

	// Then: BatchGetItem reads all 3 back, identically shaped on both
	// sides.
	bgCBOR := dynamodbCBORCall(t, srv, "BatchGetItem", map[string]any{
		"RequestItems": map[string]any{"batch-cbor": map[string]any{"Keys": keys()}},
	})
	helpers.AssertStatus(t, bgCBOR, http.StatusOK)
	var cborOut struct {
		Responses map[string][]map[string]any `cbor:"Responses"`
	}
	decodeCBORBody(t, bgCBOR, &cborOut)

	bgJSON := ddbCall(t, srv, "BatchGetItem", map[string]any{
		"RequestItems": map[string]any{"batch-json": map[string]any{"Keys": keys()}},
	})
	helpers.AssertStatus(t, bgJSON, http.StatusOK)
	var jsonOut struct {
		Responses map[string][]map[string]any `json:"Responses"`
	}
	helpers.DecodeJSON(t, bgJSON, &jsonOut)

	cborGot := cborOut.Responses["batch-cbor"]
	jsonGot := jsonOut.Responses["batch-json"]
	if len(cborGot) != 3 || len(jsonGot) != 3 {
		t.Fatalf("BatchGetItem returned cbor:%d json:%d items, want 3 each", len(cborGot), len(jsonGot))
	}
	sortByPK := func(its []map[string]any) {
		sort.Slice(its, func(a, b int) bool {
			ai, _ := its[a]["pk"].(map[string]any)
			bi, _ := its[b]["pk"].(map[string]any)
			as, _ := ai["S"].(string)
			bs, _ := bi["S"].(string)
			return as < bs
		})
	}
	sortByPK(cborGot)
	sortByPK(jsonGot)
	if !reflect.DeepEqual(normaliseAttrShape(cborGot), normaliseAttrShape(jsonGot)) {
		t.Fatalf("BatchGetItem shapes differ:\n cbor = %#v\n json = %#v",
			normaliseAttrShape(cborGot), normaliseAttrShape(jsonGot))
	}
}

// ---- TransactWriteItems with a ConditionCheck ------------------------------

// TestRPCv2CBOR_TransactWriteItems_ConditionCheck runs a transaction pairing
// a ConditionCheck with a Put over rpcv2Cbor (proving the check gates the
// write), then checks that a failing ConditionCheck is cancelled the same
// way — same code, same message, same HTTP status — as the identical
// transaction over awsJson1_0.
func TestRPCv2CBOR_TransactWriteItems_ConditionCheck(t *testing.T) {
	srv := helpers.NewTestServer(t)
	cborCreateTable(t, srv, "tx-cbor", "pk", "S")
	cborCreateTable(t, srv, "tx-json", "pk", "S")

	seed := map[string]any{
		"pk":     map[string]any{"S": "acct#1"},
		"status": map[string]any{"S": "active"},
	}
	putCBORItem(t, srv, "tx-cbor", seed)
	putItem(t, srv, "tx-json", seed)

	// Given/When: a transaction whose ConditionCheck passes also writes a
	// new item, over rpcv2Cbor.
	tx := dynamodbCBORCall(t, srv, "TransactWriteItems", map[string]any{
		"TransactItems": []map[string]any{
			{
				"ConditionCheck": map[string]any{
					"TableName":                "tx-cbor",
					"Key":                      map[string]any{"pk": map[string]any{"S": "acct#1"}},
					"ConditionExpression":      "#s = :want",
					"ExpressionAttributeNames": map[string]any{"#s": "status"},
					"ExpressionAttributeValues": map[string]any{
						":want": map[string]any{"S": "active"},
					},
				},
			},
			{
				"Put": map[string]any{
					"TableName": "tx-cbor",
					"Item": map[string]any{
						"pk":   map[string]any{"S": "order#1"},
						"acct": map[string]any{"S": "acct#1"},
					},
				},
			},
		},
	})
	helpers.AssertStatus(t, tx, http.StatusOK)
	tx.Body.Close()

	// Then: the Put half actually landed.
	get := dynamodbCBORCall(t, srv, "GetItem", map[string]any{
		"TableName": "tx-cbor",
		"Key":       map[string]any{"pk": map[string]any{"S": "order#1"}},
	})
	helpers.AssertStatus(t, get, http.StatusOK)
	var out struct {
		Item map[string]any `cbor:"Item"`
	}
	decodeCBORBody(t, get, &out)
	acctAttr, _ := out.Item["acct"].(map[string]any)
	if s, _ := acctAttr["S"].(string); s != "acct#1" {
		t.Fatalf("Item.acct = %#v, want S:acct#1", out.Item["acct"])
	}

	// When: a transaction whose ConditionCheck fails is issued over
	// rpcv2Cbor and over awsJson1_0.
	failingTx := func(tableName string) map[string]any {
		return map[string]any{
			"TransactItems": []map[string]any{
				{
					"ConditionCheck": map[string]any{
						"TableName":                tableName,
						"Key":                      map[string]any{"pk": map[string]any{"S": "acct#1"}},
						"ConditionExpression":      "#s = :want",
						"ExpressionAttributeNames": map[string]any{"#s": "status"},
						"ExpressionAttributeValues": map[string]any{
							":want": map[string]any{"S": "closed"},
						},
					},
				},
			},
		}
	}

	failCBOR := dynamodbCBORCall(t, srv, "TransactWriteItems", failingTx("tx-cbor"))
	helpers.AssertStatus(t, failCBOR, http.StatusBadRequest)
	var cborErr cborErrorEnvelope
	decodeCBORBody(t, failCBOR, &cborErr)

	failJSON := ddbCall(t, srv, "TransactWriteItems", failingTx("tx-json"))
	helpers.AssertStatus(t, failJSON, http.StatusBadRequest)
	var jsonErr cborErrorEnvelope
	helpers.DecodeJSON(t, failJSON, &jsonErr)

	// Then: the same cancellation, field for field.
	if cborErr != jsonErr {
		t.Fatalf("TransactWriteItems cancellation differs:\n cbor = %+v\n json = %+v", cborErr, jsonErr)
	}
	if cborErr.Type != "TransactionCanceledException" {
		t.Fatalf("Type = %q, want TransactionCanceledException", cborErr.Type)
	}
}

// ---- Errors: ValidationException and ResourceNotFoundException ------------

// TestRPCv2CBOR_ValidationException_MatchesJSON triggers AWS's documented
// "empty key value" ValidationException (issue #1707, rule 3) over
// rpcv2Cbor and checks the error envelope — __type, message and HTTP status
// — matches the identical request over awsJson1_0.
func TestRPCv2CBOR_ValidationException_MatchesJSON(t *testing.T) {
	srv := helpers.NewTestServer(t)
	cborCreateTable(t, srv, "val-cbor", "pk", "S")
	cborCreateTable(t, srv, "val-json", "pk", "S")

	badItem := func(tableName string) map[string]any {
		return map[string]any{
			"TableName": tableName,
			"Item":      map[string]any{"pk": map[string]any{"S": ""}},
		}
	}

	// When: PutItem is given an empty String hash key over both protocols.
	cborResp := dynamodbCBORCall(t, srv, "PutItem", badItem("val-cbor"))
	helpers.AssertStatus(t, cborResp, http.StatusBadRequest)
	var cborErr cborErrorEnvelope
	decodeCBORBody(t, cborResp, &cborErr)

	jsonResp := ddbCall(t, srv, "PutItem", badItem("val-json"))
	helpers.AssertStatus(t, jsonResp, http.StatusBadRequest)
	var jsonErr cborErrorEnvelope
	helpers.DecodeJSON(t, jsonResp, &jsonErr)

	// Then: the error envelope matches field for field.
	if cborErr != jsonErr {
		t.Fatalf("ValidationException envelope differs:\n cbor = %+v\n json = %+v", cborErr, jsonErr)
	}
	if cborErr.Type != "ValidationException" {
		t.Fatalf("Type = %q, want ValidationException", cborErr.Type)
	}
}

// TestRPCv2CBOR_ResourceNotFoundException_MatchesJSON triggers
// ResourceNotFoundException (GetItem against a table that does not exist)
// over rpcv2Cbor and checks the error envelope matches awsJson1_0.
func TestRPCv2CBOR_ResourceNotFoundException_MatchesJSON(t *testing.T) {
	srv := helpers.NewTestServer(t)

	req := map[string]any{
		"TableName": "does-not-exist",
		"Key":       map[string]any{"pk": map[string]any{"S": "x"}},
	}

	// When: GetItem names a table that was never created, over both
	// protocols.
	cborResp := dynamodbCBORCall(t, srv, "GetItem", req)
	helpers.AssertStatus(t, cborResp, http.StatusBadRequest)
	var cborErr cborErrorEnvelope
	decodeCBORBody(t, cborResp, &cborErr)

	jsonResp := ddbCall(t, srv, "GetItem", req)
	helpers.AssertStatus(t, jsonResp, http.StatusBadRequest)
	var jsonErr cborErrorEnvelope
	helpers.DecodeJSON(t, jsonResp, &jsonErr)

	// Then: the error envelope matches field for field.
	if cborErr != jsonErr {
		t.Fatalf("ResourceNotFoundException envelope differs:\n cbor = %+v\n json = %+v", cborErr, jsonErr)
	}
	if cborErr.Type != "ResourceNotFoundException" {
		t.Fatalf("Type = %q, want ResourceNotFoundException", cborErr.Type)
	}
}
