package dynamodb_test

// Request-level validation rules AWS enforces that Overcast used to wave through
// with a 200 (issue #1707). Each rule has its rejection case and the nearest
// legal request beside it, so the fix cannot over-reach.
//
// AWS evidence for the messages asserted below:
//   - Empty-string key attribute — "One or more parameter values are not
//     valid. The AttributeValue for a key attribute cannot contain an empty
//     string value. Key: <name>", AWS's wording since empty non-key strings
//     became legal (May 2020); moto raises it from put_item, update_item and
//     batch_write_item.
//   - Query without the partition key — "Query condition missed key schema
//     element: <name>" (https://dynobase.dev/dynamodb-errors/dynamodb-query-condition-missed/,
//     moto, floci-io/floci#3360).
//   - BatchWriteItem duplicates — "Provided list of item keys contains
//     duplicates" (moto batch_write_item; the API reference lists the rule).
//   - BatchGetItem over 100 keys — "Too many items requested for the
//     BatchGetItem call", quoted in the API reference itself.
//   - TransactWriteItems on one item twice — "Transaction request cannot
//     include multiple operations on one item" (moto's
//     MultipleTransactionsException; the API reference lists the rule).
//   - Item over 400 KB — "Item size has exceeded the maximum allowed size"
//     (PutItem) and "Item size to update has exceeded the maximum allowed
//     size" (UpdateItem); the latter is in the TransactWriteItems API
//     reference's ValidationError list, both are moto's ItemSizeTooLarge /
//     ItemSizeToUpdateTooLarge.
//   - Scan Segment >= TotalSegments — "The Segment parameter is zero-based and
//     must be less than parameter TotalSegments: Segment: N is not less than
//     TotalSegments: M" (moto scan; the API reference states the constraint).

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/overcast-sh/overcast/tests/helpers"
)

// ---- Empty-string key attributes -------------------------------------------

func TestPutItem_emptyStringKeyAttribute(t *testing.T) {
	// Given: a table keyed on a String
	srv := helpers.NewTestServer(t)
	createTableWithSortKey(t, srv, "rv-empty-key", "pk", "S", "sk", "N")

	// When: PutItem supplies the partition key as ""
	resp := ddbCall(t, srv, "PutItem", map[string]any{
		"TableName": "rv-empty-key",
		"Item": map[string]any{
			"pk": map[string]any{"S": ""},
			"sk": map[string]any{"N": "1"},
		},
	})
	defer resp.Body.Close()

	// Then: rejected, naming the key attribute
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	assertValidationMessage(t, resp,
		"One or more parameter values are not valid. The AttributeValue for a key attribute cannot contain an empty string value. Key: pk")
}

func TestPutItem_emptyStringNonKeyAttributeIsLegal(t *testing.T) {
	// Given: a table keyed on a String
	srv := helpers.NewTestServer(t)
	createTable(t, srv, "rv-empty-nonkey")

	// When: a non-key attribute is the empty string
	resp := ddbCall(t, srv, "PutItem", map[string]any{
		"TableName": "rv-empty-nonkey",
		"Item": map[string]any{
			"id":   map[string]any{"S": "a"},
			"note": map[string]any{"S": ""},
		},
	})
	defer resp.Body.Close()

	// Then: accepted — only key attributes must be non-empty
	helpers.AssertStatus(t, resp, http.StatusOK)
}

func TestGetItem_emptyStringKeyAttribute(t *testing.T) {
	// Given: a table keyed on a String
	srv := helpers.NewTestServer(t)
	createTable(t, srv, "rv-empty-get")

	// When: GetItem's Key is the empty string
	resp := ddbCall(t, srv, "GetItem", map[string]any{
		"TableName": "rv-empty-get",
		"Key":       map[string]any{"id": map[string]any{"S": ""}},
	})
	defer resp.Body.Close()

	// Then: rejected the same way a write is
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	assertValidationMessage(t, resp,
		"One or more parameter values are not valid. The AttributeValue for a key attribute cannot contain an empty string value. Key: id")
}

func TestBatchWriteItem_emptyStringKeyAttribute(t *testing.T) {
	// Given: a table keyed on a String
	srv := helpers.NewTestServer(t)
	createTable(t, srv, "rv-empty-batch")

	// When: a PutRequest carries an empty-string key
	resp := ddbCall(t, srv, "BatchWriteItem", map[string]any{
		"RequestItems": map[string]any{
			"rv-empty-batch": []map[string]any{
				{"PutRequest": map[string]any{"Item": map[string]any{"id": map[string]any{"S": ""}}}},
			},
		},
	})
	defer resp.Body.Close()

	// Then: the whole batch is rejected
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	assertValidationMessage(t, resp,
		"One or more parameter values are not valid. The AttributeValue for a key attribute cannot contain an empty string value. Key: id")
}

// ---- Query must constrain the partition key --------------------------------

func TestQuery_sortKeyOnlyCondition(t *testing.T) {
	// Given: a composite-key table with an item
	srv := helpers.NewTestServer(t)
	createTableWithSortKey(t, srv, "rv-query-sk", "pk", "S", "sk", "N")
	putItem(t, srv, "rv-query-sk", map[string]any{
		"pk": map[string]any{"S": "a"},
		"sk": map[string]any{"N": "1"},
	})

	// When: the key condition names only the sort key
	resp := ddbCall(t, srv, "Query", map[string]any{
		"TableName":                 "rv-query-sk",
		"KeyConditionExpression":    "sk = :s",
		"ExpressionAttributeValues": map[string]any{":s": map[string]any{"N": "1"}},
	})
	defer resp.Body.Close()

	// Then: rejected, naming the partition key it missed
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	assertValidationMessage(t, resp, "Query condition missed key schema element: pk")
}

func TestQuery_nonKeyAttributeCondition(t *testing.T) {
	// Given: a hash-only table
	srv := helpers.NewTestServer(t)
	createTable(t, srv, "rv-query-nonkey")

	// When: the key condition names an attribute outside the key schema
	resp := ddbCall(t, srv, "Query", map[string]any{
		"TableName":                 "rv-query-nonkey",
		"KeyConditionExpression":    "colour = :c",
		"ExpressionAttributeValues": map[string]any{":c": map[string]any{"S": "red"}},
	})
	defer resp.Body.Close()

	// Then: rejected, naming the partition key
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	assertValidationMessage(t, resp, "Query condition missed key schema element: id")
}

func TestQuery_indexConditionMissingIndexPartitionKey(t *testing.T) {
	// Given: a table with a GSI keyed on gk
	srv := helpers.NewTestServer(t)
	createTableWithGSI(t, srv, "rv-query-gsi", "by-gk", "gk", "ALL", nil)

	// When: a Query on the index constrains the table's key instead
	resp := ddbCall(t, srv, "Query", map[string]any{
		"TableName":                 "rv-query-gsi",
		"IndexName":                 "by-gk",
		"KeyConditionExpression":    "id = :v",
		"ExpressionAttributeValues": map[string]any{":v": map[string]any{"S": "a"}},
	})
	defer resp.Body.Close()

	// Then: the index's partition key is the one reported missing
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	assertValidationMessage(t, resp, "Query condition missed key schema element: gk")
}

func TestQuery_sortConditionBeforePartitionKey(t *testing.T) {
	// Given: a composite-key table with two items under one partition
	srv := helpers.NewTestServer(t)
	createTableWithSortKey(t, srv, "rv-query-swapped", "pk", "S", "sk", "N")
	for _, n := range []string{"1", "2"} {
		putItem(t, srv, "rv-query-swapped", map[string]any{
			"pk": map[string]any{"S": "a"},
			"sk": map[string]any{"N": n},
		})
	}

	// When: the two equalities are written sort key first — legal on AWS
	resp := ddbCall(t, srv, "Query", map[string]any{
		"TableName":              "rv-query-swapped",
		"KeyConditionExpression": "sk = :s AND pk = :p",
		"ExpressionAttributeValues": map[string]any{
			":s": map[string]any{"N": "2"},
			":p": map[string]any{"S": "a"},
		},
	})
	defer resp.Body.Close()

	// Then: it is answered exactly as the partition-key-first form would be
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		Count int                         `json:"Count"`
		Items []map[string]map[string]any `json:"Items"`
	}
	helpers.DecodeJSON(t, resp, &result)
	if result.Count != 1 || len(result.Items) != 1 || result.Items[0]["sk"]["N"] != "2" {
		t.Errorf("expected exactly the item with sk = 2, got count=%d items=%v", result.Count, result.Items)
	}
}

// ---- BatchWriteItem duplicate keys -----------------------------------------

func TestBatchWriteItem_duplicatePutKeys(t *testing.T) {
	// Given: a composite-key table
	srv := helpers.NewTestServer(t)
	createTableWithSortKey(t, srv, "rv-batch-dup", "pk", "S", "sk", "N")

	// When: two PutRequests target the same primary key
	resp := ddbCall(t, srv, "BatchWriteItem", map[string]any{
		"RequestItems": map[string]any{
			"rv-batch-dup": []map[string]any{
				{"PutRequest": map[string]any{"Item": map[string]any{
					"pk": map[string]any{"S": "dup"}, "sk": map[string]any{"N": "1"}, "v": map[string]any{"S": "first"},
				}}},
				{"PutRequest": map[string]any{"Item": map[string]any{
					"pk": map[string]any{"S": "dup"}, "sk": map[string]any{"N": "1"}, "v": map[string]any{"S": "second"},
				}}},
			},
		},
	})
	defer resp.Body.Close()

	// Then: the whole batch is rejected and nothing was written
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	assertValidationMessage(t, resp, "Provided list of item keys contains duplicates")
	assertItemAbsent(t, srv, "rv-batch-dup", map[string]any{
		"pk": map[string]any{"S": "dup"}, "sk": map[string]any{"N": "1"},
	})
}

func TestBatchWriteItem_putAndDeleteSameKey(t *testing.T) {
	// Given: a hash-only table
	srv := helpers.NewTestServer(t)
	createTable(t, srv, "rv-batch-putdel")

	// When: one item is both put and deleted in the same batch
	resp := ddbCall(t, srv, "BatchWriteItem", map[string]any{
		"RequestItems": map[string]any{
			"rv-batch-putdel": []map[string]any{
				{"PutRequest": map[string]any{"Item": map[string]any{"id": map[string]any{"S": "x"}}}},
				{"DeleteRequest": map[string]any{"Key": map[string]any{"id": map[string]any{"S": "x"}}}},
			},
		},
	})
	defer resp.Body.Close()

	// Then: rejected — a put and a delete on one item are two operations
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	assertValidationMessage(t, resp, "Provided list of item keys contains duplicates")
}

func TestBatchWriteItem_distinctKeysAndSingleItem(t *testing.T) {
	// Given: a composite-key table
	srv := helpers.NewTestServer(t)
	createTableWithSortKey(t, srv, "rv-batch-ok", "pk", "S", "sk", "N")

	// When: a single-item batch, then a batch whose items share a partition
	// key but differ in sort key
	single := ddbCall(t, srv, "BatchWriteItem", map[string]any{
		"RequestItems": map[string]any{
			"rv-batch-ok": []map[string]any{
				{"PutRequest": map[string]any{"Item": map[string]any{"pk": map[string]any{"S": "a"}, "sk": map[string]any{"N": "1"}}}},
			},
		},
	})
	defer single.Body.Close()
	helpers.AssertStatus(t, single, http.StatusOK)

	resp := ddbCall(t, srv, "BatchWriteItem", map[string]any{
		"RequestItems": map[string]any{
			"rv-batch-ok": []map[string]any{
				{"PutRequest": map[string]any{"Item": map[string]any{"pk": map[string]any{"S": "a"}, "sk": map[string]any{"N": "2"}}}},
				{"PutRequest": map[string]any{"Item": map[string]any{"pk": map[string]any{"S": "a"}, "sk": map[string]any{"N": "3"}}}},
				{"DeleteRequest": map[string]any{"Key": map[string]any{"pk": map[string]any{"S": "a"}, "sk": map[string]any{"N": "1"}}}},
			},
		},
	})
	defer resp.Body.Close()

	// Then: both are accepted
	helpers.AssertStatus(t, resp, http.StatusOK)
}

// ---- BatchGetItem key count ------------------------------------------------

func batchGetKeys(prefix string, n int) []map[string]any {
	keys := make([]map[string]any, 0, n)
	for i := 0; i < n; i++ {
		keys = append(keys, map[string]any{"id": map[string]any{"S": fmt.Sprintf("%s-%d", prefix, i)}})
	}
	return keys
}

func TestBatchGetItem_moreThan100KeysAcrossTables(t *testing.T) {
	// Given: two tables
	srv := helpers.NewTestServer(t)
	createTable(t, srv, "rv-bg-a")
	createTable(t, srv, "rv-bg-b")

	// When: 101 keys are requested across them
	resp := ddbCall(t, srv, "BatchGetItem", map[string]any{
		"RequestItems": map[string]any{
			"rv-bg-a": map[string]any{"Keys": batchGetKeys("a", 60)},
			"rv-bg-b": map[string]any{"Keys": batchGetKeys("b", 41)},
		},
	})
	defer resp.Body.Close()

	// Then: rejected with AWS's documented message
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	assertValidationMessage(t, resp, "Too many items requested for the BatchGetItem call")
}

func TestBatchGetItem_exactly100Keys(t *testing.T) {
	// Given: a table
	srv := helpers.NewTestServer(t)
	createTable(t, srv, "rv-bg-100")

	// When: exactly 100 keys are requested
	resp := ddbCall(t, srv, "BatchGetItem", map[string]any{
		"RequestItems": map[string]any{
			"rv-bg-100": map[string]any{"Keys": batchGetKeys("k", 100)},
		},
	})
	defer resp.Body.Close()

	// Then: accepted — the limit is inclusive
	helpers.AssertStatus(t, resp, http.StatusOK)
}

// ---- TransactWriteItems: one operation per item ----------------------------

func TestTransactWriteItems_multipleOperationsOnOneItem(t *testing.T) {
	// Given: a hash-only table
	srv := helpers.NewTestServer(t)
	createTable(t, srv, "rv-tx-dup")

	// When: a Put and a ConditionCheck target the same item
	resp := ddbCall(t, srv, "TransactWriteItems", map[string]any{
		"TransactItems": []map[string]any{
			{"Put": map[string]any{
				"TableName": "rv-tx-dup",
				"Item":      map[string]any{"id": map[string]any{"S": "one"}},
			}},
			{"ConditionCheck": map[string]any{
				"TableName":           "rv-tx-dup",
				"Key":                 map[string]any{"id": map[string]any{"S": "one"}},
				"ConditionExpression": "attribute_not_exists(id)",
			}},
		},
	})
	defer resp.Body.Close()

	// Then: a ValidationException, not a cancellation, and nothing written
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	assertValidationMessage(t, resp, "Transaction request cannot include multiple operations on one item")
	assertItemAbsent(t, srv, "rv-tx-dup", map[string]any{"id": map[string]any{"S": "one"}})
}

func TestTransactWriteItems_sameKeyInDifferentTables(t *testing.T) {
	// Given: two tables
	srv := helpers.NewTestServer(t)
	createTable(t, srv, "rv-tx-a")
	createTable(t, srv, "rv-tx-b")

	// When: the same key value is written in each table
	resp := ddbCall(t, srv, "TransactWriteItems", map[string]any{
		"TransactItems": []map[string]any{
			{"Put": map[string]any{"TableName": "rv-tx-a", "Item": map[string]any{"id": map[string]any{"S": "one"}}}},
			{"Put": map[string]any{"TableName": "rv-tx-b", "Item": map[string]any{"id": map[string]any{"S": "one"}}}},
		},
	})
	defer resp.Body.Close()

	// Then: accepted — they are different items
	helpers.AssertStatus(t, resp, http.StatusOK)
}

// ---- Item size -------------------------------------------------------------

// oversizedItem is an item whose AWS-accounted size is just over 400 KB:
// 2 ("pk") + 1 + 4 ("blob") + 400*1024 bytes of String.
func oversizedItem() map[string]any {
	return map[string]any{
		"pk":   map[string]any{"S": "big"},
		"blob": map[string]any{"S": strings.Repeat("x", 400*1024)},
	}
}

func TestPutItem_itemOver400KB(t *testing.T) {
	// Given: a hash-only table keyed on pk
	srv := helpers.NewTestServer(t)
	createTableWithHashKey(t, srv, "rv-size-put", "pk")

	// When: an item over 400 KB is written
	resp := ddbCall(t, srv, "PutItem", map[string]any{
		"TableName": "rv-size-put",
		"Item":      oversizedItem(),
	})
	defer resp.Body.Close()

	// Then: rejected, and not stored
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	assertValidationMessage(t, resp, "Item size has exceeded the maximum allowed size")
	assertItemAbsent(t, srv, "rv-size-put", map[string]any{"pk": map[string]any{"S": "big"}})
}

func TestPutItem_item399KB(t *testing.T) {
	// Given: a hash-only table keyed on pk
	srv := helpers.NewTestServer(t)
	createTableWithHashKey(t, srv, "rv-size-ok", "pk")

	// When: a 399 KB item is written
	resp := ddbCall(t, srv, "PutItem", map[string]any{
		"TableName": "rv-size-ok",
		"Item": map[string]any{
			"pk":   map[string]any{"S": "big"},
			"blob": map[string]any{"S": strings.Repeat("x", 399*1024)},
		},
	})
	defer resp.Body.Close()

	// Then: accepted
	helpers.AssertStatus(t, resp, http.StatusOK)
}

func TestPutItem_numberSizeIsNotDecimalLength(t *testing.T) {
	// Given: a hash-only table keyed on pk
	srv := helpers.NewTestServer(t)
	createTableWithHashKey(t, srv, "rv-size-numbers", "pk")

	// When: an item carries a number set whose decimal text alone is over
	// 400 KB but whose AWS-accounted size (1 byte per two significant digits
	// plus one) is roughly half that
	numbers := make([]any, 0, 12000)
	for i := 0; i < 12000; i++ {
		numbers = append(numbers, fmt.Sprintf("%036d", i+1)) // 36 chars, no leading zeros counted
	}
	resp := ddbCall(t, srv, "PutItem", map[string]any{
		"TableName": "rv-size-numbers",
		"Item": map[string]any{
			"pk": map[string]any{"S": "nums"},
			"ns": map[string]any{"NS": numbers},
		},
	})
	defer resp.Body.Close()

	// Then: accepted — numbers are not sized by their decimal string length
	helpers.AssertStatus(t, resp, http.StatusOK)
}

func TestBatchWriteItem_itemOver400KB(t *testing.T) {
	// Given: a hash-only table keyed on pk
	srv := helpers.NewTestServer(t)
	createTableWithHashKey(t, srv, "rv-size-batch", "pk")

	// When: one PutRequest in the batch is over 400 KB
	resp := ddbCall(t, srv, "BatchWriteItem", map[string]any{
		"RequestItems": map[string]any{
			"rv-size-batch": []map[string]any{
				{"PutRequest": map[string]any{"Item": map[string]any{"pk": map[string]any{"S": "small"}}}},
				{"PutRequest": map[string]any{"Item": oversizedItem()}},
			},
		},
	})
	defer resp.Body.Close()

	// Then: the whole batch is rejected before anything is written
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	assertValidationMessage(t, resp, "Item size has exceeded the maximum allowed size")
	assertItemAbsent(t, srv, "rv-size-batch", map[string]any{"pk": map[string]any{"S": "small"}})
}

func TestUpdateItem_resultOver400KB(t *testing.T) {
	// Given: a 399 KB item
	srv := helpers.NewTestServer(t)
	createTableWithHashKey(t, srv, "rv-size-update", "pk")
	putItem(t, srv, "rv-size-update", map[string]any{
		"pk":   map[string]any{"S": "big"},
		"blob": map[string]any{"S": strings.Repeat("x", 399*1024)},
	})

	// When: an update would grow it past 400 KB
	resp := ddbCall(t, srv, "UpdateItem", map[string]any{
		"TableName":                 "rv-size-update",
		"Key":                       map[string]any{"pk": map[string]any{"S": "big"}},
		"UpdateExpression":          "SET more = :m",
		"ExpressionAttributeValues": map[string]any{":m": map[string]any{"S": strings.Repeat("y", 2*1024)}},
	})
	defer resp.Body.Close()

	// Then: rejected with the update-specific wording, item unchanged
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	assertValidationMessage(t, resp, "Item size to update has exceeded the maximum allowed size")
	getResp := ddbCall(t, srv, "GetItem", map[string]any{
		"TableName": "rv-size-update",
		"Key":       map[string]any{"pk": map[string]any{"S": "big"}},
	})
	defer getResp.Body.Close()
	var got struct {
		Item map[string]map[string]any `json:"Item"`
	}
	helpers.DecodeJSON(t, getResp, &got)
	if _, ok := got.Item["more"]; ok {
		t.Error("rejected update must not have been applied")
	}
}

func TestTransactWriteItems_putOver400KB(t *testing.T) {
	// Given: a hash-only table keyed on pk
	srv := helpers.NewTestServer(t)
	createTableWithHashKey(t, srv, "rv-size-tx", "pk")

	// When: a transaction Put carries an item over 400 KB
	resp := ddbCall(t, srv, "TransactWriteItems", map[string]any{
		"TransactItems": []map[string]any{
			{"Put": map[string]any{"TableName": "rv-size-tx", "Item": map[string]any{"pk": map[string]any{"S": "small"}}}},
			{"Put": map[string]any{"TableName": "rv-size-tx", "Item": oversizedItem()}},
		},
	})
	defer resp.Body.Close()

	// Then: the transaction is cancelled with a ValidationError reason at
	// that position, as the API reference documents for item-size faults,
	// and nothing was written
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	var errResp struct {
		Type    string `json:"__type"`
		Message string `json:"message"`
	}
	helpers.DecodeJSON(t, resp, &errResp)
	if errResp.Type != "TransactionCanceledException" {
		t.Errorf("expected TransactionCanceledException, got %q", errResp.Type)
	}
	if !strings.HasSuffix(errResp.Message, "[None, ValidationError]") {
		t.Errorf("expected cancellation reasons [None, ValidationError], got %q", errResp.Message)
	}
	assertItemAbsent(t, srv, "rv-size-tx", map[string]any{"pk": map[string]any{"S": "small"}})
}

// ---- Scan segments ---------------------------------------------------------

func TestScan_segmentNotLessThanTotalSegments(t *testing.T) {
	// Given: a table
	srv := helpers.NewTestServer(t)
	createTable(t, srv, "rv-scan-seg")

	// When: Segment equals TotalSegments
	resp := ddbCall(t, srv, "Scan", map[string]any{
		"TableName":     "rv-scan-seg",
		"TotalSegments": 2,
		"Segment":       2,
	})
	defer resp.Body.Close()

	// Then: rejected with AWS's wording
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	assertValidationMessage(t, resp,
		"The Segment parameter is zero-based and must be less than parameter TotalSegments: Segment: 2 is not less than TotalSegments: 2")
}

func TestScan_lastSegmentIsLegal(t *testing.T) {
	// Given: a table with an item
	srv := helpers.NewTestServer(t)
	createTable(t, srv, "rv-scan-seg-ok")
	putItem(t, srv, "rv-scan-seg-ok", map[string]any{"id": map[string]any{"S": "a"}})

	// When: the last segment (TotalSegments-1) is scanned, and a
	// single-segment scan too
	resp := ddbCall(t, srv, "Scan", map[string]any{
		"TableName":     "rv-scan-seg-ok",
		"TotalSegments": 3,
		"Segment":       2,
	})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)

	single := ddbCall(t, srv, "Scan", map[string]any{
		"TableName":     "rv-scan-seg-ok",
		"TotalSegments": 1,
		"Segment":       0,
	})
	defer single.Body.Close()

	// Then: both accepted
	helpers.AssertStatus(t, single, http.StatusOK)
}

// assertItemAbsent fails if GetItem finds an item under key — the check that
// a rejected write landed nothing.
func assertItemAbsent(t *testing.T, srv *helpers.TestServer, tableName string, key map[string]any) {
	t.Helper()
	resp := ddbCall(t, srv, "GetItem", map[string]any{"TableName": tableName, "Key": key})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	var got struct {
		Item map[string]any `json:"Item"`
	}
	helpers.DecodeJSON(t, resp, &got)
	if len(got.Item) != 0 {
		t.Errorf("expected no item under %v in %s after a rejected write, got %v", key, tableName, got.Item)
	}
}
