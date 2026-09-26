package s3tables

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/overcast-sh/overcast/internal/icebergmeta"
)

func intp(v int) *int { return &v }

func TestBuildInitialMetadata_declaredIDsAddressTheirOwnColumn(t *testing.T) {
	// Given: "a" declares id 2 and "b" declares nothing, so position 2 is taken
	in := &icebergMetadata{
		Schema: &icebergSchema{Fields: []icebergSchemaField{
			{ID: intp(2), Name: "a", Type: "int"},
			{Name: "b", Type: "int"},
		}},
		PartitionSpec: &icebergPartitionSpec{Fields: []icebergPartitionField{{SourceID: 2, Transform: "identity", Name: "a_part"}}},
	}

	// When: the first metadata document is built
	raw, aerr := buildInitialMetadata("uuid", "s3://w--table-s3", time.Unix(0, 0), in)
	if aerr != nil {
		t.Fatalf("buildInitialMetadata: %v", aerr)
	}

	// Then: the partition refers to "a", now column 1, not to "b"
	var doc struct {
		PartitionSpecs []struct {
			Fields []struct {
				SourceID int `json:"source-id"`
			} `json:"fields"`
		} `json:"partition-specs"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if got := doc.PartitionSpecs[0].Fields[0].SourceID; got != 1 {
		t.Errorf("source-id = %d, want 1", got)
	}
}

func TestBuildInitialMetadata_duplicateDeclaredIDIsRefused(t *testing.T) {
	// Given: two columns that declare the same id
	// When: the first metadata document is built
	// Then: the request is refused as a bad request
	in := &icebergMetadata{Schema: &icebergSchema{Fields: []icebergSchemaField{
		{ID: intp(1), Name: "a", Type: "int"},
		{ID: intp(1), Name: "b", Type: "int"},
	}}}
	if _, aerr := buildInitialMetadata("uuid", "s3://w--table-s3", time.Unix(0, 0), in); aerr == nil || aerr.Code != "BadRequestException" {
		t.Errorf("aerr = %v", aerr)
	}
}

// schemaV2Request is a schemaV2 metadata.iceberg as an SDK sends it: nested
// types as documents, ids in the caller's own numbering, a partition on a
// field of a struct and a sort on a top-level column.
const schemaV2Request = `{
  "schemaV2": {
    "type": "struct", "schema-id": 7, "identifier-field-ids": [10],
    "fields": [
      {"id": 10, "name": "id", "required": true, "type": "long", "doc": "row id"},
      {"id": 20, "name": "tags", "required": false, "type": {"type": "list", "element-id": 21, "element": "string", "element-required": false}},
      {"id": 30, "name": "point", "required": false, "type": {"type": "struct", "fields": [
        {"id": 31, "name": "x", "required": true, "type": "double"}]}}
    ]
  },
  "partitionSpec": {"fields": [{"source-id": 31, "transform": "identity", "name": "x"}]},
  "writeOrder": {"order-id": 1, "fields": [{"source-id": 10, "transform": "identity", "direction": "asc", "null-order": "nulls-first"}]}
}`

func TestBuildInitialMetadata_schemaV2WritesNestedTypesWithFreshIDs(t *testing.T) {
	// Given: a schemaV2 request
	var in icebergMetadata
	if err := json.Unmarshal([]byte(schemaV2Request), &in); err != nil {
		t.Fatal(err)
	}

	// When: the first metadata document is built
	raw, aerr := buildInitialMetadata("uuid", "s3://w--table-s3", time.Unix(0, 0), &in)
	if aerr != nil {
		t.Fatalf("buildInitialMetadata: %v", aerr)
	}
	meta, err := icebergmeta.Parse(raw)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	// Then: the schema is id 0 with Iceberg's fresh ids, depth-first
	s := meta.Schemas[0]
	if s.SchemaID != 0 || s.Fields[0].ID != 1 || s.Fields[1].Type.List.ElementID != 4 || s.Fields[2].Type.Struct.Fields[0].ID != 5 {
		t.Errorf("schema = %s", raw)
	}
	if s.Fields[0].Doc != "row id" || s.IdentifierFieldIDs[0] != 1 || meta.LastColumnID != 5 {
		t.Errorf("doc %q, identifier ids %v, last column %d", s.Fields[0].Doc, s.IdentifierFieldIDs, meta.LastColumnID)
	}

	// And: the partition and sort fields follow their columns
	if got := meta.PartitionSpecs[0].Fields[0].SourceID; got != 5 {
		t.Errorf("partition source-id = %d, want 5 (point.x)", got)
	}
	if got := meta.SortOrders[0].Fields[0].SourceID; got != 1 {
		t.Errorf("sort source-id = %d, want 1 (id)", got)
	}
}

func TestValidateCreateTable_schemaChoice(t *testing.T) {
	v2 := &icebergSchemaV2{Type: "struct", Fields: []icebergmeta.Field{{ID: 1, Name: "id", Type: icebergmeta.PrimitiveType("long")}}}
	v1 := &icebergSchema{Fields: []icebergSchemaField{{Name: "id", Type: "long"}}}
	cases := map[string]struct {
		iceberg *icebergMetadata
		ok      bool
	}{
		"schema":              {&icebergMetadata{Schema: v1}, true},
		"schemaV2":            {&icebergMetadata{SchemaV2: v2}, true},
		"both":                {&icebergMetadata{Schema: v1, SchemaV2: v2}, false},
		"neither":             {&icebergMetadata{}, false},
		"schemaV2 not struct": {&icebergMetadata{SchemaV2: &icebergSchemaV2{Type: "list", Fields: v2.Fields}}, false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			// Given: a CreateTable request whose metadata.iceberg declares its columns this way
			req := &createTableRequest{Name: "t", Format: formatIceberg, Metadata: &tableMetadata{Iceberg: tc.iceberg}}

			// When: it is validated
			_, aerr := validateCreateTable(req)

			// Then: only exactly one struct schema is accepted, and the rest are bad requests
			if tc.ok != (aerr == nil) || (aerr != nil && aerr.Code != "BadRequestException") {
				t.Errorf("aerr = %v, want ok=%v", aerr, tc.ok)
			}
		})
	}
}

func TestBuildInitialMetadata_nestedTypeInSchemaPointsAtSchemaV2(t *testing.T) {
	// Given: a flat schema whose column is typed like a nested type
	in := &icebergMetadata{Schema: &icebergSchema{Fields: []icebergSchemaField{{Name: "tags", Type: "list<string>"}}}}

	// When: the first metadata document is built
	_, aerr := buildInitialMetadata("uuid", "s3://w--table-s3", time.Unix(0, 0), in)

	// Then: it is refused, naming schemaV2 as the way to declare it
	if aerr == nil || aerr.Code != "BadRequestException" || !strings.Contains(aerr.Message, "schemaV2") {
		t.Errorf("aerr = %v", aerr)
	}
}
