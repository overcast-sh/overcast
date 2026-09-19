package dynamodb

import "testing"

func keySchemaOf(names ...string) []KeySchemaElement {
	out := make([]KeySchemaElement, 0, len(names))
	for i, n := range names {
		kt := "HASH"
		if i > 0 {
			kt = "RANGE"
		}
		out = append(out, KeySchemaElement{AttributeName: n, KeyType: kt})
	}
	return out
}

func stringDefsOf(names ...string) []AttributeDef {
	out := make([]AttributeDef, 0, len(names))
	for _, n := range names {
		out = append(out, AttributeDef{AttributeName: n, AttributeType: "S"})
	}
	return out
}

// The decision table in attribute_definitions.go, one row per case.
func TestValidateAttributeDefinitions(t *testing.T) {
	gsi := func(names ...string) []SecondaryIndex {
		return []SecondaryIndex{{IndexName: "idx", KeySchema: keySchemaOf(names...)}}
	}
	cases := []struct {
		name string
		req  createTableRequest
		want string // "" for accepted
	}{
		{"exact match", createTableRequest{KeySchema: keySchemaOf("pk", "sk"), AttributeDefinitions: stringDefsOf("pk", "sk")}, ""},
		{"definition used only by an index", createTableRequest{KeySchema: keySchemaOf("pk"), AttributeDefinitions: stringDefsOf("pk", "gk"), GlobalSecondaryIndexes: gsi("gk")}, ""},
		{"duplicate definitions are one attribute", createTableRequest{KeySchema: keySchemaOf("pk"), AttributeDefinitions: stringDefsOf("pk", "pk")}, ""},
		{"no key schema and no definitions", createTableRequest{}, ""},
		{"unused, no indexes", createTableRequest{KeySchema: keySchemaOf("pk"), AttributeDefinitions: stringDefsOf("pk", "x")},
			"One or more parameter values were invalid: Number of attributes in KeySchema does not exactly match number of attributes defined in AttributeDefinitions"},
		{"unused, with indexes", createTableRequest{KeySchema: keySchemaOf("pk"), AttributeDefinitions: stringDefsOf("pk", "gk", "x"), GlobalSecondaryIndexes: gsi("gk")},
			"One or more parameter values were invalid: Some AttributeDefinitions are not used. AttributeDefinitions: [pk, gk, x], keys used: [pk, gk]"},
		{"missing, no indexes", createTableRequest{KeySchema: keySchemaOf("pk", "sk"), AttributeDefinitions: stringDefsOf("pk")},
			"Invalid KeySchema: Some index key attribute have no definition"},
		{"missing, with indexes", createTableRequest{KeySchema: keySchemaOf("pk"), AttributeDefinitions: stringDefsOf("pk"), GlobalSecondaryIndexes: gsi("gk")},
			"One or more parameter values were invalid: Some index key attributes are not defined in AttributeDefinitions. Keys: [gk], AttributeDefinitions: [pk]"},
		{"same count misnamed, no indexes", createTableRequest{KeySchema: keySchemaOf("pk", "sk"), AttributeDefinitions: stringDefsOf("pk", "typo")},
			"One or more parameter values were invalid: Some index key attributes are not defined in AttributeDefinitions. Keys: [pk, sk], AttributeDefinitions: [pk, typo]"},
		{"same count misnamed, with indexes", createTableRequest{KeySchema: keySchemaOf("pk"), AttributeDefinitions: stringDefsOf("pk", "typo"), GlobalSecondaryIndexes: gsi("gk")},
			"One or more parameter values were invalid: Some index key attributes are not defined in AttributeDefinitions. Keys: [gk], AttributeDefinitions: [pk, typo]"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			aerr := validateAttributeDefinitions(&tc.req)
			switch {
			case tc.want == "" && aerr != nil:
				t.Errorf("unexpected rejection: %s", aerr.Message)
			case tc.want != "" && aerr == nil:
				t.Errorf("expected rejection %q, got none", tc.want)
			case tc.want != "" && aerr.Message != tc.want:
				t.Errorf("message mismatch\n got: %s\nwant: %s", aerr.Message, tc.want)
			}
		})
	}
}
