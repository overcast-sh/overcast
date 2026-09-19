package dynamodb_test

// CreateTable must be given AttributeDefinitions that describe exactly the
// key attributes of the table and its indexes (issue #1707, rules 1 and 2).
// AWS has four wordings for the ways the two can disagree; the decision
// table is moto's _throw_attr_error (moto/dynamodb/responses.py), and three
// of the messages are quoted verbatim from real AWS elsewhere:
//   - "Some AttributeDefinitions are not used. AttributeDefinitions: [...],
//     keys used: [...]" in aws-cloudformation/cfn-lint#1037,
//   - "Some index key attributes are not defined in AttributeDefinitions.
//     Keys: [...], AttributeDefinitions: [...]" in getmoto/moto#2445,
//   - "Number of attributes in KeySchema does not exactly match number of
//     attributes defined in AttributeDefinitions" in
//     https://rory.horse/posts/dynamo-dissected-schema/.
// A definition used only by a GSI or LSI is legal and is the regression
// risk of the unused-definition check.

import (
	"net/http"
	"testing"

	"github.com/overcast-sh/overcast/tests/helpers"
)

// ---- CreateTable: AttributeDefinitions vs KeySchema -------------------------

func TestCreateTable_unusedAttributeDefinition(t *testing.T) {
	// Given: a request defining an attribute no key or index uses, on a
	// table with no indexes
	srv := helpers.NewTestServer(t)

	// When: the table is created
	resp := ddbCall(t, srv, "CreateTable", map[string]any{
		"TableName": "rv-unused-def",
		"KeySchema": []map[string]any{{"AttributeName": "pk", "KeyType": "HASH"}},
		"AttributeDefinitions": []map[string]any{
			{"AttributeName": "pk", "AttributeType": "S"},
			{"AttributeName": "unused", "AttributeType": "S"},
		},
		"BillingMode": "PAY_PER_REQUEST",
	})
	defer resp.Body.Close()

	// Then: AWS's count-mismatch wording for an index-less table
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	assertValidationMessage(t, resp,
		"One or more parameter values were invalid: Number of attributes in KeySchema does not exactly match number of attributes defined in AttributeDefinitions")
	helpers.AssertRequestID(t, resp)
}

func TestCreateTable_unusedAttributeDefinitionWithIndex(t *testing.T) {
	// Given: a table with a GSI and one definition nothing uses
	srv := helpers.NewTestServer(t)

	// When: the table is created
	resp := ddbCall(t, srv, "CreateTable", map[string]any{
		"TableName": "rv-unused-def-gsi",
		"KeySchema": []map[string]any{{"AttributeName": "pk", "KeyType": "HASH"}},
		"AttributeDefinitions": []map[string]any{
			{"AttributeName": "pk", "AttributeType": "S"},
			{"AttributeName": "gk", "AttributeType": "S"},
			{"AttributeName": "unused", "AttributeType": "N"},
		},
		"GlobalSecondaryIndexes": []map[string]any{{
			"IndexName":  "by-gk",
			"KeySchema":  []map[string]any{{"AttributeName": "gk", "KeyType": "HASH"}},
			"Projection": map[string]any{"ProjectionType": "ALL"},
		}},
		"BillingMode": "PAY_PER_REQUEST",
	})
	defer resp.Body.Close()

	// Then: AWS names the definitions and the keys actually used
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	assertValidationMessage(t, resp,
		"One or more parameter values were invalid: Some AttributeDefinitions are not used. AttributeDefinitions: [pk, gk, unused], keys used: [pk, gk]")
}

func TestCreateTable_keySchemaAttributeWithoutDefinition(t *testing.T) {
	// Given: a composite key whose sort key has no AttributeDefinition
	srv := helpers.NewTestServer(t)

	// When: the table is created
	resp := ddbCall(t, srv, "CreateTable", map[string]any{
		"TableName": "rv-undefined-key",
		"KeySchema": []map[string]any{
			{"AttributeName": "pk", "KeyType": "HASH"},
			{"AttributeName": "sk", "KeyType": "RANGE"},
		},
		"AttributeDefinitions": []map[string]any{{"AttributeName": "pk", "AttributeType": "S"}},
		"BillingMode":          "PAY_PER_REQUEST",
	})
	defer resp.Body.Close()

	// Then: AWS's wording for an index-less table short of a definition
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	assertValidationMessage(t, resp, "Invalid KeySchema: Some index key attribute have no definition")
}

func TestCreateTable_keySchemaAttributeMisnamedDefinition(t *testing.T) {
	// Given: as many definitions as keys, but one names the wrong attribute
	srv := helpers.NewTestServer(t)

	// When: the table is created
	resp := ddbCall(t, srv, "CreateTable", map[string]any{
		"TableName": "rv-misnamed-key",
		"KeySchema": []map[string]any{
			{"AttributeName": "pk", "KeyType": "HASH"},
			{"AttributeName": "sk", "KeyType": "RANGE"},
		},
		"AttributeDefinitions": []map[string]any{
			{"AttributeName": "pk", "AttributeType": "S"},
			{"AttributeName": "typo", "AttributeType": "S"},
		},
		"BillingMode": "PAY_PER_REQUEST",
	})
	defer resp.Body.Close()

	// Then: AWS lists the key attributes against the definitions supplied
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	assertValidationMessage(t, resp,
		"One or more parameter values were invalid: Some index key attributes are not defined in AttributeDefinitions. Keys: [pk, sk], AttributeDefinitions: [pk, typo]")
}

func TestCreateTable_indexKeyAttributeWithoutDefinition(t *testing.T) {
	// Given: a GSI keyed on an attribute with no AttributeDefinition
	srv := helpers.NewTestServer(t)

	// When: the table is created
	resp := ddbCall(t, srv, "CreateTable", map[string]any{
		"TableName":            "rv-undefined-gsi-key",
		"KeySchema":            []map[string]any{{"AttributeName": "pk", "KeyType": "HASH"}},
		"AttributeDefinitions": []map[string]any{{"AttributeName": "pk", "AttributeType": "S"}},
		"GlobalSecondaryIndexes": []map[string]any{{
			"IndexName":  "by-gk",
			"KeySchema":  []map[string]any{{"AttributeName": "gk", "KeyType": "HASH"}},
			"Projection": map[string]any{"ProjectionType": "ALL"},
		}},
		"BillingMode": "PAY_PER_REQUEST",
	})
	defer resp.Body.Close()

	// Then: AWS names the undefined key attribute
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	assertValidationMessage(t, resp,
		"One or more parameter values were invalid: Some index key attributes are not defined in AttributeDefinitions. Keys: [gk], AttributeDefinitions: [pk]")
}

func TestCreateTable_attributeDefinitionUsedOnlyByIndexes(t *testing.T) {
	// Given: definitions used only by a GSI and only by an LSI — legal, and
	// the regression risk of the unused-definition check
	srv := helpers.NewTestServer(t)

	// When: the table is created
	resp := ddbCall(t, srv, "CreateTable", map[string]any{
		"TableName": "rv-index-only-defs",
		"KeySchema": []map[string]any{
			{"AttributeName": "pk", "KeyType": "HASH"},
			{"AttributeName": "sk", "KeyType": "RANGE"},
		},
		"AttributeDefinitions": []map[string]any{
			{"AttributeName": "pk", "AttributeType": "S"},
			{"AttributeName": "sk", "AttributeType": "N"},
			{"AttributeName": "gk", "AttributeType": "S"},
			{"AttributeName": "lk", "AttributeType": "S"},
		},
		"GlobalSecondaryIndexes": []map[string]any{{
			"IndexName":  "by-gk",
			"KeySchema":  []map[string]any{{"AttributeName": "gk", "KeyType": "HASH"}},
			"Projection": map[string]any{"ProjectionType": "ALL"},
		}},
		"LocalSecondaryIndexes": []map[string]any{{
			"IndexName": "by-lk",
			"KeySchema": []map[string]any{
				{"AttributeName": "pk", "KeyType": "HASH"},
				{"AttributeName": "lk", "KeyType": "RANGE"},
			},
			"Projection": map[string]any{"ProjectionType": "KEYS_ONLY"},
		}},
		"BillingMode": "PAY_PER_REQUEST",
	})
	defer resp.Body.Close()

	// Then: accepted
	helpers.AssertStatus(t, resp, http.StatusOK)
}
