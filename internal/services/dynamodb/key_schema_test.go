package dynamodb

// Unit coverage for the defensive branches of key_schema.go — the ones a
// well-formed SDK request cannot reach, so the integration suite in
// tests/integration/dynamodb/key_schema_validation_test.go cannot exercise
// them. The AWS-facing behaviour itself is asserted there.

import "testing"

// compositeSchemaTable is a pk (S) / sk (N) table, the shape every case below
// varies from.
func compositeSchemaTable() *Table {
	return &Table{
		TableName: "t",
		KeySchema: []KeySchemaElement{
			{AttributeName: "pk", KeyType: "HASH"},
			{AttributeName: "sk", KeyType: "RANGE"},
		},
		AttributeDefinitions: []AttributeDef{
			{AttributeName: "pk", AttributeType: "S"},
			{AttributeName: "sk", AttributeType: "N"},
		},
	}
}

func TestValidateKeySchema_malformedTableRecordsAreNotRejected(t *testing.T) {
	// Given: table records that cannot be type-checked against
	undeclared := compositeSchemaTable()
	undeclared.AttributeDefinitions = nil // no declarations to compare with

	noHashKey := compositeSchemaTable()
	noHashKey.KeySchema = nil // resolveKeys answers this one, further down

	cases := []struct {
		name  string
		table *Table
		item  Item
	}{
		{"nil table", nil, Item{"pk": {"N": "1"}}},
		{"key attributes with no AttributeDefinitions entry", undeclared, Item{"pk": {"N": "1"}, "sk": {"S": "x"}}},
		{"table with no key schema", noHashKey, Item{"anything": {"S": "x"}}},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// When + Then: validation stands aside rather than inventing a
			// rejection for a record AWS makes impossible at CreateTable
			if aerr := validateKeySchema(c.table, c.item, operandItem); aerr != nil {
				t.Errorf("validateKeySchema = %v, want nil", aerr.Message)
			}
			if aerr := validateKeySchema(c.table, c.item, operandKey); aerr != nil {
				t.Errorf("validateKeySchema(operandKey) = %v, want nil", aerr.Message)
			}
		})
	}
}

func TestValidateKeySchema_attributeValueWithoutExactlyOneTypeTag(t *testing.T) {
	// Given: key values that carry no type tag, or more than one — malformed
	// AttributeValues that no AWS SDK produces and that AWS rejects with a
	// message of its own that Overcast does not model
	table := compositeSchemaTable()
	cases := []struct {
		name string
		item Item
	}{
		{"no type tag", Item{"pk": {}, "sk": {"N": "1"}}},
		{"two type tags", Item{"pk": {"S": "a", "N": "1"}, "sk": {"N": "1"}}},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// When + Then: the type check is skipped rather than reporting a
			// type it cannot name — the attribute is present, which is all
			// this validator claims to check
			if aerr := validateKeySchema(table, c.item, operandItem); aerr != nil {
				t.Errorf("validateKeySchema = %v, want nil", aerr.Message)
			}
		})
	}
}

func TestValidateKeySchema_presenceIsReportedBeforeType(t *testing.T) {
	// Given: an item missing its partition key whose sort key is also mistyped
	table := compositeSchemaTable()

	// When: it is validated as a whole item
	aerr := validateKeySchema(table, Item{"sk": {"S": "x"}}, operandItem)

	// Then: AWS names the missing partition key, not the mistyped sort key
	if aerr == nil {
		t.Fatal("validateKeySchema = nil, want a ValidationException")
	}
	want := "One or more parameter values were invalid: Missing the key pk in the item"
	if aerr.Message != want {
		t.Errorf("message\n got: %s\nwant: %s", aerr.Message, want)
	}
}

func TestValidateKeySchema_hashOnlyTableIgnoresNonKeyAttributes(t *testing.T) {
	// Given: a hash-only table
	table := &Table{
		TableName:            "t",
		KeySchema:            []KeySchemaElement{{AttributeName: "id", KeyType: "HASH"}},
		AttributeDefinitions: []AttributeDef{{AttributeName: "id", AttributeType: "S"}},
	}

	// When: an item carries the key plus attributes of other types
	item := Item{"id": {"S": "a"}, "n": {"N": "1"}, "m": {"M": map[string]any{}}}

	// Then: only the key attribute is constrained
	if aerr := validateKeySchema(table, item, operandItem); aerr != nil {
		t.Errorf("validateKeySchema = %v, want nil", aerr.Message)
	}
	// ...and the same map supplied as a Key is rejected for its extras
	if aerr := validateKeySchema(table, item, operandKey); aerr == nil {
		t.Error("validateKeySchema(operandKey) = nil, want a ValidationException for the extra attributes")
	}
}

func TestValidateKeyConditionTypes_undeclaredOrAbsentValuesAreSkipped(t *testing.T) {
	table := compositeSchemaTable()

	// Given: a condition on an attribute the table declares nothing about
	kc := &keyCond{hashAttr: "other", hashVal: attrValue{"N": "1"}}

	// When + Then: nothing to check it against, so nothing is reported
	if aerr := validateKeyConditionTypes(table, "other", "", kc); aerr != nil {
		t.Errorf("validateKeyConditionTypes = %v, want nil", aerr.Message)
	}

	// Given: a BETWEEN condition whose bounds are both correctly typed, with
	// the unused single-value slot left nil
	kc = &keyCond{
		hashAttr: "pk",
		hashVal:  attrValue{"S": "a"},
		sortCond: &sortKeyCond{attr: "sk", kind: sortKeyBetween, lo: attrValue{"N": "1"}, hi: attrValue{"N": "9"}},
	}

	// When + Then: the nil val slot is skipped rather than treated as a fault
	if aerr := validateKeyConditionTypes(table, "pk", "sk", kc); aerr != nil {
		t.Errorf("validateKeyConditionTypes = %v, want nil", aerr.Message)
	}

	// Given: no compiled condition and no table at all
	if aerr := validateKeyConditionTypes(table, "pk", "sk", nil); aerr != nil {
		t.Errorf("validateKeyConditionTypes(nil cond) = %v, want nil", aerr.Message)
	}
	if aerr := validateKeyConditionTypes(nil, "pk", "sk", kc); aerr != nil {
		t.Errorf("validateKeyConditionTypes(nil table) = %v, want nil", aerr.Message)
	}
}

func TestDeclaredKeyAttrType_reportsWhetherDeclared(t *testing.T) {
	table := compositeSchemaTable()
	cases := []struct {
		name     string
		attr     string
		wantType string
		wantOK   bool
	}{
		{"declared string key", "pk", "S", true},
		{"declared number key", "sk", "N", true},
		{"undeclared attribute", "other", "", false},
		{"empty name", "", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := declaredKeyAttrType(table, c.attr)
			if got != c.wantType || ok != c.wantOK {
				t.Errorf("declaredKeyAttrType(table, %q) = (%q, %v), want (%q, %v)", c.attr, got, ok, c.wantType, c.wantOK)
			}
		})
	}
	if _, ok := declaredKeyAttrType(nil, "pk"); ok {
		t.Error("declaredKeyAttrType(nil, \"pk\") reported a declaration, want none")
	}
}

// validateKeyConditionSchema resolves which parsed term plays which role and
// then checks both against the key schema in play. The AWS-facing messages are
// asserted end to end in tests/integration/dynamodb/key_condition_test.go;
// this pins the resolution table itself, including the swap's effect on the
// compiled condition, which no response body shows.
func TestValidateKeyConditionSchema_rolesAndSchema(t *testing.T) {
	// Helper builders keep each case to the shape under test.
	eq := func(attr, val string) *sortKeyCond {
		return &sortKeyCond{attr: attr, kind: sortKeyEq, val: attrValue{"S": val}}
	}
	gt := func(attr, val string) *sortKeyCond {
		return &sortKeyCond{attr: attr, kind: sortKeyGT, val: attrValue{"S": val}}
	}

	cases := []struct {
		name         string
		hashAttrName string
		sortAttrName string
		kc           *keyCond
		want         string // "" means accepted
	}{
		{
			name: "nil condition is not this check's business",
			// A missing KeyConditionExpression is rejected before compiling.
			hashAttrName: "pk", sortAttrName: "sk", kc: nil,
		},
		{
			name:         "partition key alone",
			hashAttrName: "pk", sortAttrName: "sk",
			kc: &keyCond{hashAttr: "pk", hashVal: attrValue{"S": "a"}},
		},
		{
			name:         "partition key and sort key",
			hashAttrName: "pk", sortAttrName: "sk",
			kc: &keyCond{hashAttr: "pk", hashVal: attrValue{"S": "a"}, sortCond: gt("sk", "b")},
		},
		{
			name:         "non-key second condition leaves the sort key missed",
			hashAttrName: "pk", sortAttrName: "sk",
			kc:   &keyCond{hashAttr: "pk", hashVal: attrValue{"S": "a"}, sortCond: eq("foo", "b")},
			want: "Query condition missed key schema element: sk",
		},
		{
			name:         "second condition with no sort key to miss",
			hashAttrName: "pk", sortAttrName: "",
			kc:   &keyCond{hashAttr: "pk", hashVal: attrValue{"S": "a"}, sortCond: eq("foo", "b")},
			want: msgKeyConditionNotSupported,
		},
		{
			name:         "index key schema, not the table's",
			hashAttrName: "gsipk", sortAttrName: "gsisk",
			kc:   &keyCond{hashAttr: "gsipk", hashVal: attrValue{"S": "a"}, sortCond: eq("sk", "b")},
			want: "Query condition missed key schema element: gsisk",
		},
		{
			name:         "no term could hold the partition-key role",
			hashAttrName: "pk", sortAttrName: "sk",
			kc:   &keyCond{sortCond: gt("sk", "b")},
			want: "Query condition missed key schema element: pk",
		},
		{
			name:         "partition key not constrained at all",
			hashAttrName: "pk", sortAttrName: "sk",
			kc:   &keyCond{hashAttr: "foo", hashVal: attrValue{"S": "a"}},
			want: "Query condition missed key schema element: pk",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			aerr := validateKeyConditionSchema(tc.hashAttrName, tc.sortAttrName, tc.kc)
			switch {
			case tc.want == "" && aerr != nil:
				t.Fatalf("validateKeyConditionSchema = %q, want accepted", aerr.Message)
			case tc.want != "" && aerr == nil:
				t.Fatalf("validateKeyConditionSchema accepted, want %q", tc.want)
			case tc.want != "" && aerr.Message != tc.want:
				t.Fatalf("validateKeyConditionSchema = %q, want %q", aerr.Message, tc.want)
			}
		})
	}
}

// Both terms are equalities, so the parser cannot tell which is the partition
// key's; the schema does, and the swap must move the value across with the
// name or the query reads the wrong partition.
func TestValidateKeyConditionSchema_swapsSortKeyFirstEqualities(t *testing.T) {
	// Given: "sk = :s AND pk = :p" as the parser leaves it
	kc := &keyCond{
		hashAttr: "sk",
		hashVal:  attrValue{"S": "sortval"},
		sortCond: &sortKeyCond{attr: "pk", kind: sortKeyEq, val: attrValue{"S": "hashval"}},
	}

	// When: it is checked against a pk/sk schema
	if aerr := validateKeyConditionSchema("pk", "sk", kc); aerr != nil {
		t.Fatalf("validateKeyConditionSchema = %q, want accepted", aerr.Message)
	}

	// Then: both halves are in canonical order, names and values together
	if kc.hashAttr != "pk" || extractScalar(kc.hashVal) != "hashval" {
		t.Errorf("hash condition = %s/%s, want pk/hashval", kc.hashAttr, extractScalar(kc.hashVal))
	}
	if kc.sortCond.attr != "sk" || extractScalar(kc.sortCond.val) != "sortval" {
		t.Errorf("sort condition = %s/%s, want sk/sortval", kc.sortCond.attr, extractScalar(kc.sortCond.val))
	}
}
