package dynamodb_test

// The two secondary-index read-path properties the GSI suite in
// gsi_read_path_test.go leaves open (issue #135): a Projection of ALL, which
// is the only one of the three that must return base-table attributes rather
// than withhold them, and the sparse rule applied to a *composite* index key,
// where the attribute an item may be missing is the index's sort key rather
// than its partition key.
//
// AWS evidence:
//   - "If you query or scan a global secondary index, you can only request
//     attributes that are projected into the index" — and ALL projects every
//     attribute of the base table.
//     https://docs.aws.amazon.com/amazondynamodb/latest/developerguide/GSI.html
//   - "if the sort key doesn't appear in every table item, the index is said
//     to be sparse" — an item missing an index key attribute is not written to
//     the index at all, so no query of that index can return it.
//     https://docs.aws.amazon.com/amazondynamodb/latest/developerguide/GSI.html
//     and the same rule for local indexes,
//     https://docs.aws.amazon.com/amazondynamodb/latest/developerguide/LSI.html

import (
	"net/http"
	"testing"

	"github.com/overcast-sh/overcast/tests/helpers"
)

// A Projection of ALL is the contrast case to the KEYS_ONLY and INCLUDE tests
// in gsi_read_path_test.go: the index row carries the whole base item, so a
// query of it returns attributes that are neither table keys nor index keys.
func TestQuery_GSI_AllProjection_ReturnsEveryBaseAttribute(t *testing.T) {
	// Given: a GSI on "category" projecting ALL, and an item with two
	// attributes outside both key schemas
	srv := helpers.NewTestServer(t)
	createTableWithGSI(t, srv, "gsi-all", "cat-index", "category", "ALL", nil)
	putItem(t, srv, "gsi-all", map[string]any{
		"id":       map[string]any{"S": "i1"},
		"category": map[string]any{"S": "books"},
		"title":    map[string]any{"S": "Dune"},
		"pages":    map[string]any{"N": "412"},
	})

	// When: the index is queried
	resp := ddbCall(t, srv, "Query", map[string]any{
		"TableName":              "gsi-all",
		"IndexName":              "cat-index",
		"KeyConditionExpression": "category = :c",
		"ExpressionAttributeValues": map[string]any{
			":c": map[string]any{"S": "books"},
		},
	})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)

	var out struct {
		Items []map[string]map[string]any `json:"Items"`
	}
	helpers.DecodeJSON(t, resp, &out)
	if len(out.Items) != 1 {
		t.Fatalf("got %d items, want 1", len(out.Items))
	}

	// Then: every base-table attribute comes back, not just the keys
	for _, attr := range []string{"id", "category", "title", "pages"} {
		if _, ok := out.Items[0][attr]; !ok {
			t.Errorf("item %v is missing attribute %q projected by ALL", out.Items[0], attr)
		}
	}
}

// An item that has the index's partition key but not its sort key is not in a
// composite-key index, so a query that constrains only the partition key must
// still skip it. The GSI suite pins the sparse rule for Scan; the Query path
// reaches the index through a different primitive.
func TestQuery_GSI_CompositeKeySparse_ExcludesItemMissingIndexSortKey(t *testing.T) {
	// Given: a GSI keyed (gsipk, gsisk), and two items sharing gsipk of which
	// only one carries gsisk
	srv := helpers.NewTestServer(t)
	createGSIKeyCondTable(t, srv, "gsi-sparse-sort")
	putItem(t, srv, "gsi-sparse-sort", map[string]any{
		"pk":    map[string]any{"S": "P"},
		"sk":    map[string]any{"S": "indexed"},
		"gsipk": map[string]any{"S": "G"},
		"gsisk": map[string]any{"S": "g1"},
	})
	putItem(t, srv, "gsi-sparse-sort", map[string]any{
		"pk":    map[string]any{"S": "P"},
		"sk":    map[string]any{"S": "not-indexed"},
		"gsipk": map[string]any{"S": "G"},
	})

	// When: the index is queried on its partition key alone
	resp := ddbCall(t, srv, "Query", map[string]any{
		"TableName":              "gsi-sparse-sort",
		"IndexName":              "gsi1",
		"KeyConditionExpression": "gsipk = :g",
		"ExpressionAttributeValues": map[string]any{
			":g": map[string]any{"S": "G"},
		},
	})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)

	var out struct {
		Items []map[string]map[string]any `json:"Items"`
	}
	helpers.DecodeJSON(t, resp, &out)

	// Then: only the item that satisfies the whole index key is in the index
	if len(out.Items) != 1 {
		t.Fatalf("got %d items %v, want 1 (the item without gsisk is not in the index)", len(out.Items), out.Items)
	}
	if got, _ := out.Items[0]["sk"]["S"].(string); got != "indexed" {
		t.Errorf("returned item sk = %q, want %q", got, "indexed")
	}
}
