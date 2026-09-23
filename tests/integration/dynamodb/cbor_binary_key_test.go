package dynamodb_test

// cbor_binary_key_test.go covers issue #1999: a table keyed (hash or range)
// on a Binary attribute answered every item operation with a 400
// ValidationException ("cannot contain an empty string value") over Smithy
// RPC v2 CBOR, where the byte-identical request succeeded over AWS JSON
// 1.0. Root cause, documented in
// docs/dev/compatibility/services/dynamodb.yaml's cbor_coverage divergence
// finding: extractKeyValue (store.go) and extractScalar (expr.go) recognised
// a scalar attribute's value only when it arrived as a Go string. JSON
// always decodes a "B" AttributeValue as base64 text (a Go string); RPC v2
// CBOR decodes the same member as a genuine CBOR byte string, which the CBOR
// codec (internal/protocol/codec/cbor.go) leaves as a raw []byte. The two
// helpers therefore saw an empty value for a CBOR-written Binary key and
// key_schema.go's empty-key-value rule (issue #1707, rule 3) rejected the
// request before the table was ever looked up.
//
// These tests exercise PutItem, GetItem, DeleteItem, Query and
// BatchGetItem against tables keyed on Binary — a Binary-only hash key and a
// table with both a Binary hash key and a Binary range key — and a
// cross-protocol round trip proving a Binary key written over one protocol
// is readable over the other, because both now resolve to the identical
// stored key string (base64 text) regardless of which protocol wrote it.

import (
	"encoding/base64"
	"net/http"
	"reflect"
	"testing"

	"github.com/overcast-sh/overcast/tests/helpers"
)

// cborCreateTableWithSortKey creates a table with a Binary (or other) hash
// and range key pair — the CBOR-test equivalent of dynamodb_test.go's
// createTableWithSortKey.
func cborCreateTableWithSortKey(t *testing.T, srv *helpers.TestServer, name, hashKey, hashType, sortKey, sortType string) {
	t.Helper()
	resp := ddbCall(t, srv, "CreateTable", map[string]any{
		"TableName": name,
		"AttributeDefinitions": []map[string]any{
			{"AttributeName": hashKey, "AttributeType": hashType},
			{"AttributeName": sortKey, "AttributeType": sortType},
		},
		"KeySchema": []map[string]any{
			{"AttributeName": hashKey, "KeyType": "HASH"},
			{"AttributeName": sortKey, "KeyType": "RANGE"},
		},
		"BillingMode": "PAY_PER_REQUEST",
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("cborCreateTableWithSortKey %q: status %d", name, resp.StatusCode)
	}
}

// TestRPCv2CBOR_BinaryHashKey_PutGetDeleteItem round-trips PutItem, GetItem
// and DeleteItem over rpcv2Cbor against a table whose only key is a Binary
// hash key.
func TestRPCv2CBOR_BinaryHashKey_PutGetDeleteItem(t *testing.T) {
	srv := helpers.NewTestServer(t)
	cborCreateTable(t, srv, "bin-hash", "id", "B")

	idBytes := []byte{0x01, 0x02, 0x03, 0xFF}
	key := map[string]any{"id": map[string]any{"B": idBytes}}

	// When: an item with a Binary hash key is written over rpcv2Cbor.
	putResp := dynamodbCBORCall(t, srv, "PutItem", map[string]any{
		"TableName": "bin-hash",
		"Item": map[string]any{
			"id":    map[string]any{"B": idBytes},
			"label": map[string]any{"S": "first"},
		},
	})
	helpers.AssertStatus(t, putResp, http.StatusOK)
	putResp.Body.Close()

	// Then: GetItem with the identical Binary key finds it.
	getResp := dynamodbCBORCall(t, srv, "GetItem", map[string]any{
		"TableName": "bin-hash",
		"Key":       key,
	})
	helpers.AssertStatus(t, getResp, http.StatusOK)
	var out struct {
		Item map[string]any `cbor:"Item"`
	}
	decodeCBORBody(t, getResp, &out)
	if out.Item == nil {
		t.Fatalf("GetItem found no item for Binary hash key %x", idBytes)
	}
	labelAttr, _ := out.Item["label"].(map[string]any)
	if s, _ := labelAttr["S"].(string); s != "first" {
		t.Fatalf("Item.label = %#v, want S:first", out.Item["label"])
	}

	// When: the item is deleted over rpcv2Cbor.
	delResp := dynamodbCBORCall(t, srv, "DeleteItem", map[string]any{
		"TableName": "bin-hash",
		"Key":       key,
	})
	helpers.AssertStatus(t, delResp, http.StatusOK)
	delResp.Body.Close()

	// Then: it is gone.
	getAfterDelete := dynamodbCBORCall(t, srv, "GetItem", map[string]any{
		"TableName": "bin-hash",
		"Key":       key,
	})
	helpers.AssertStatus(t, getAfterDelete, http.StatusOK)
	var afterOut struct {
		Item map[string]any `cbor:"Item"`
	}
	decodeCBORBody(t, getAfterDelete, &afterOut)
	if afterOut.Item != nil {
		t.Fatalf("GetItem after DeleteItem = %#v, want no item", afterOut.Item)
	}
}

// TestRPCv2CBOR_BinaryHashAndRangeKey_Query writes three items sharing a
// Binary hash key but differing in their Binary range key, over rpcv2Cbor,
// then Queries by the hash key and checks all three come back.
func TestRPCv2CBOR_BinaryHashAndRangeKey_Query(t *testing.T) {
	srv := helpers.NewTestServer(t)
	cborCreateTableWithSortKey(t, srv, "bin-hashrange", "pk", "B", "sk", "B")

	pkBytes := []byte{0xAA, 0xBB}
	skValues := [][]byte{{0x01}, {0x02}, {0x03}}

	for i, sk := range skValues {
		putCBORItem(t, srv, "bin-hashrange", map[string]any{
			"pk":  map[string]any{"B": pkBytes},
			"sk":  map[string]any{"B": sk},
			"seq": map[string]any{"N": string(rune('0' + i))},
		})
	}

	// When: Query filters on the Binary hash key alone.
	queryResp := dynamodbCBORCall(t, srv, "Query", map[string]any{
		"TableName":                 "bin-hashrange",
		"KeyConditionExpression":    "pk = :pk",
		"ExpressionAttributeValues": map[string]any{":pk": map[string]any{"B": pkBytes}},
	})
	helpers.AssertStatus(t, queryResp, http.StatusOK)
	var out struct {
		Items []map[string]any `cbor:"Items"`
		Count int              `cbor:"Count"`
	}
	decodeCBORBody(t, queryResp, &out)

	// Then: all three items sharing the Binary hash key are returned.
	if out.Count != 3 {
		t.Fatalf("Query Count = %d, want 3", out.Count)
	}
	gotSK := map[string]bool{}
	for _, item := range out.Items {
		skAttr, ok := item["sk"].(map[string]any)
		if !ok {
			t.Fatalf("item sk missing or malformed: %#v", item["sk"])
		}
		b, ok := skAttr["B"].([]byte)
		if !ok {
			t.Fatalf("sk.B decoded as %T, want []byte", skAttr["B"])
		}
		gotSK[string(b)] = true
	}
	for _, want := range skValues {
		if !gotSK[string(want)] {
			t.Fatalf("Query results missing sort key %x; got %v", want, gotSK)
		}
	}

	// And: GetItem against a single (hash, range) Binary key pair finds
	// exactly that item.
	getResp := dynamodbCBORCall(t, srv, "GetItem", map[string]any{
		"TableName": "bin-hashrange",
		"Key": map[string]any{
			"pk": map[string]any{"B": pkBytes},
			"sk": map[string]any{"B": skValues[1]},
		},
	})
	helpers.AssertStatus(t, getResp, http.StatusOK)
	var getOut struct {
		Item map[string]any `cbor:"Item"`
	}
	decodeCBORBody(t, getResp, &getOut)
	if getOut.Item == nil {
		t.Fatalf("GetItem found no item for Binary (hash, range) key (%x, %x)", pkBytes, skValues[1])
	}
}

// TestRPCv2CBOR_BinaryKey_BatchGetItem writes items keyed by Binary hash
// keys with BatchWriteItem and reads them back with BatchGetItem, entirely
// over rpcv2Cbor.
func TestRPCv2CBOR_BinaryKey_BatchGetItem(t *testing.T) {
	srv := helpers.NewTestServer(t)
	cborCreateTable(t, srv, "bin-batch", "id", "B")

	idValues := [][]byte{{0x10}, {0x20}, {0x30}}
	reqs := make([]map[string]any, len(idValues))
	keys := make([]map[string]any, len(idValues))
	for i, id := range idValues {
		reqs[i] = map[string]any{"PutRequest": map[string]any{"Item": map[string]any{
			"id":  map[string]any{"B": id},
			"idx": map[string]any{"N": string(rune('0' + i))},
		}}}
		keys[i] = map[string]any{"id": map[string]any{"B": id}}
	}

	// When: BatchWriteItem writes three items keyed by Binary hash keys.
	bwResp := dynamodbCBORCall(t, srv, "BatchWriteItem", map[string]any{
		"RequestItems": map[string]any{"bin-batch": reqs},
	})
	helpers.AssertStatus(t, bwResp, http.StatusOK)
	bwResp.Body.Close()

	// Then: BatchGetItem reads all three back by their Binary keys.
	bgResp := dynamodbCBORCall(t, srv, "BatchGetItem", map[string]any{
		"RequestItems": map[string]any{"bin-batch": map[string]any{"Keys": keys}},
	})
	helpers.AssertStatus(t, bgResp, http.StatusOK)
	var out struct {
		Responses map[string][]map[string]any `cbor:"Responses"`
	}
	decodeCBORBody(t, bgResp, &out)

	got := out.Responses["bin-batch"]
	if len(got) != 3 {
		t.Fatalf("BatchGetItem returned %d items, want 3", len(got))
	}
	seen := map[string]bool{}
	for _, item := range got {
		idAttr, _ := item["id"].(map[string]any)
		b, ok := idAttr["B"].([]byte)
		if !ok {
			t.Fatalf("id.B decoded as %T, want []byte", idAttr["B"])
		}
		seen[string(b)] = true
	}
	for _, want := range idValues {
		if !seen[string(want)] {
			t.Fatalf("BatchGetItem results missing id %x; got %v", want, seen)
		}
	}
}

// TestRPCv2CBOR_BinaryKey_CrossProtocolRoundTrip proves the key encoding is
// identical regardless of which protocol wrote the item: a Binary key
// written over rpcv2Cbor (as a raw CBOR byte string) must be readable over
// awsJson1_0 (as base64 text) for the same underlying bytes, and vice
// versa — because both protocols resolve a Binary key to the same stored
// key string (base64 text; see extractKeyValue/extractScalar and
// scalarString in internal/services/dynamodb).
func TestRPCv2CBOR_BinaryKey_CrossProtocolRoundTrip(t *testing.T) {
	srv := helpers.NewTestServer(t)
	cborCreateTable(t, srv, "bin-crossproto", "id", "B")

	cborWrittenID := []byte{0xC0, 0xFF, 0xEE}
	jsonWrittenID := []byte{0xBA, 0xAD, 0xF0, 0x0D}

	// Given: one item written over rpcv2Cbor (native byte string)...
	putCBORItem(t, srv, "bin-crossproto", map[string]any{
		"id":     map[string]any{"B": cborWrittenID},
		"origin": map[string]any{"S": "cbor"},
	})
	// ...and one item written over awsJson1_0 (base64 text, the only wire
	// form JSON has for Binary).
	jsonPut := ddbCall(t, srv, "PutItem", map[string]any{
		"TableName": "bin-crossproto",
		"Item": map[string]any{
			"id":     map[string]any{"B": base64.StdEncoding.EncodeToString(jsonWrittenID)},
			"origin": map[string]any{"S": "json"},
		},
	})
	helpers.AssertStatus(t, jsonPut, http.StatusOK)
	jsonPut.Body.Close()

	// When: the CBOR-written item is read back over awsJson1_0, keyed by
	// the same bytes expressed as base64 text.
	getCBORItemOverJSON := ddbCall(t, srv, "GetItem", map[string]any{
		"TableName": "bin-crossproto",
		"Key":       map[string]any{"id": map[string]any{"B": base64.StdEncoding.EncodeToString(cborWrittenID)}},
	})
	helpers.AssertStatus(t, getCBORItemOverJSON, http.StatusOK)
	var jsonReadOfCBORItem struct {
		Item map[string]any `json:"Item"`
	}
	helpers.DecodeJSON(t, getCBORItemOverJSON, &jsonReadOfCBORItem)
	originAttr, _ := jsonReadOfCBORItem.Item["origin"].(map[string]any)
	if s, _ := originAttr["S"].(string); s != "cbor" {
		t.Fatalf("GetItem over awsJson1_0 for a rpcv2Cbor-written Binary key: origin = %#v, want S:cbor", jsonReadOfCBORItem.Item["origin"])
	}

	// And: the awsJson1_0-written item is read back over rpcv2Cbor, keyed
	// by the same bytes expressed as a raw CBOR byte string.
	getJSONItemOverCBOR := dynamodbCBORCall(t, srv, "GetItem", map[string]any{
		"TableName": "bin-crossproto",
		"Key":       map[string]any{"id": map[string]any{"B": jsonWrittenID}},
	})
	helpers.AssertStatus(t, getJSONItemOverCBOR, http.StatusOK)
	var cborReadOfJSONItem struct {
		Item map[string]any `cbor:"Item"`
	}
	decodeCBORBody(t, getJSONItemOverCBOR, &cborReadOfJSONItem)
	jsonOriginAttr, _ := cborReadOfJSONItem.Item["origin"].(map[string]any)
	if s, _ := jsonOriginAttr["S"].(string); s != "json" {
		t.Fatalf("GetItem over rpcv2Cbor for an awsJson1_0-written Binary key: origin = %#v, want S:json", cborReadOfJSONItem.Item["origin"])
	}

	// Sanity: the two records are genuinely distinct (different keys), not
	// one item coincidentally matching both reads.
	if reflect.DeepEqual(cborWrittenID, jsonWrittenID) {
		t.Fatalf("test setup bug: cborWrittenID and jsonWrittenID must differ")
	}
}
