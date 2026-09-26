package icebergmeta

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// nestedFields is a schema in the caller's numbering: a list, a map and a
// struct, with ids that are unique but in no particular order.
const nestedFields = `[
  {"id": 10, "name": "id", "required": true, "type": "long"},
  {"id": 20, "name": "tags", "required": false, "type": {"type": "list", "element-id": 21, "element": "string", "element-required": false}},
  {"id": 30, "name": "attrs", "required": false, "type": {"type": "map", "key-id": 31, "key": "string", "value-id": 32, "value": "double", "value-required": true}},
  {"id": 40, "name": "point", "required": false, "type": {"type": "struct", "fields": [
    {"id": 41, "name": "x", "required": true, "type": "double"},
    {"id": 42, "name": "y", "required": true, "type": "double"}]}}
]`

func TestType_roundTripsTheSpecsJSON(t *testing.T) {
	// Given: fields of every nested kind in the spec's JSON
	var fields []Field
	if err := json.Unmarshal([]byte(nestedFields), &fields); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	// When: they are written again
	raw, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}

	// Then: the document is unchanged
	var want, got any
	_ = json.Unmarshal([]byte(nestedFields), &want)
	_ = json.Unmarshal(raw, &got)
	if !equalJSON(want, got) {
		t.Errorf("round trip:\n got  %s\n want %s", raw, nestedFields)
	}
}

func TestNew_assignsFreshIDsTheWayIcebergDoes(t *testing.T) {
	// Given: a nested schema in the caller's numbering, partitioned and sorted
	// by columns named by those ids
	var fields []Field
	if err := json.Unmarshal([]byte(nestedFields), &fields); err != nil {
		t.Fatal(err)
	}
	spec := CreateSpec{
		TableUUID: "u", Location: "s3://w--table-s3", Fields: fields, IdentifierFieldIDs: []int{10},
		PartitionFields: []PartitionField{{SourceID: 41, Name: "x", Transform: "identity"}},
		SortFields:      []SortField{{SourceID: 10, Transform: "identity", Direction: "asc", NullOrder: "nulls-first"}},
	}

	// When: the table is created
	m, err := New(spec, testNow)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// Then: top-level columns are 1..4, then each nested type in turn — the
	// numbering PyIceberg and the Java reference both produce
	s := m.Schemas[0]
	if got := [...]int{s.Fields[0].ID, s.Fields[1].ID, s.Fields[2].ID, s.Fields[3].ID}; got != [...]int{1, 2, 3, 4} {
		t.Errorf("top-level ids = %v", got)
	}
	list, mp, st := s.Fields[1].Type.List, s.Fields[2].Type.Map, s.Fields[3].Type.Struct
	if list.ElementID != 5 || mp.KeyID != 6 || mp.ValueID != 7 || st.Fields[0].ID != 8 || st.Fields[1].ID != 9 {
		t.Errorf("nested ids = %d, %d/%d, %d/%d", list.ElementID, mp.KeyID, mp.ValueID, st.Fields[0].ID, st.Fields[1].ID)
	}
	if m.LastColumnID != 9 || s.IdentifierFieldIDs[0] != 1 {
		t.Errorf("last column = %d, identifier ids = %v", m.LastColumnID, s.IdentifierFieldIDs)
	}

	// And: the partition and sort fields follow their columns to the new ids
	if got := m.PartitionSpecs[0].Fields[0].SourceID; got != 8 {
		t.Errorf("partition source = %d, want 8 (point.x)", got)
	}
	if got := m.SortOrders[0].Fields[0].SourceID; got != 1 {
		t.Errorf("sort source = %d, want 1 (id)", got)
	}
}

func TestNew_versionOneOnRequest(t *testing.T) {
	// Given: a table whose properties ask for format version 1
	in := baseInput()
	in.Properties = map[string]string{propFormatVersion: "1", "k": "v"}

	// When: it is created
	m, err := New(in, testNow)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// Then: it is version 1, carries the v1 fields, and does not store the
	// reserved property
	if m.FormatVersion != 1 || m.Schema == nil || m.PartitionSpec == nil {
		t.Errorf("version = %d, schema = %v, spec = %v", m.FormatVersion, m.Schema, m.PartitionSpec)
	}
	if _, stored := m.Properties[propFormatVersion]; stored || m.Properties["k"] != "v" {
		t.Errorf("properties = %v", m.Properties)
	}
}

// nestedSpec is the nested schema as a new table, with point.y made a long so
// that the "identifier in an optional struct" case is refused for its
// optional parent rather than for being a double.
func nestedSpec(t *testing.T) CreateSpec {
	t.Helper()
	var fields []Field
	if err := json.Unmarshal([]byte(nestedFields), &fields); err != nil {
		t.Fatal(err)
	}
	fields[3].Type.Struct.Fields[1].Type = PrimitiveType("long")
	return CreateSpec{TableUUID: "u", Location: "s3://w--table-s3", Fields: fields}
}

func TestNew_refusesReferencesTheSpecForbidsInANestedSchema(t *testing.T) {
	// Given: the nested schema, with one reference into it the spec forbids;
	// the message must name the id as the caller sent it
	cases := map[string]struct {
		mutate func(*CreateSpec)
		names  string
	}{
		"partition by a struct": {func(in *CreateSpec) {
			in.PartitionFields = []PartitionField{{SourceID: 40, Name: "p", Transform: "identity"}}
		}, "source id 40"},
		"partition by a list element": {func(in *CreateSpec) {
			in.PartitionFields = []PartitionField{{SourceID: 21, Name: "p", Transform: "identity"}}
		}, "source id 21"},
		"partition by an undeclared id": {func(in *CreateSpec) {
			in.PartitionFields = []PartitionField{{SourceID: 99, Name: "p", Transform: "identity"}}
		}, "source id 99"},
		"sort by a struct": {func(in *CreateSpec) {
			in.SortFields = []SortField{{SourceID: 40, Transform: "identity", Direction: "asc", NullOrder: "nulls-first"}}
		}, "source id 40"},
		"identifier that is optional": {func(in *CreateSpec) {
			in.Fields[0].Required = false
			in.IdentifierFieldIDs = []int{10}
		}, `"id"`},
		"identifier that is a list":              {func(in *CreateSpec) { in.IdentifierFieldIDs = []int{20} }, "id 20"},
		"identifier that is a double":            {func(in *CreateSpec) { in.IdentifierFieldIDs = []int{41} }, `"x"`},
		"identifier in an optional struct":       {func(in *CreateSpec) { in.IdentifierFieldIDs = []int{42} }, `"y"`},
		"identifier that is a map key":           {func(in *CreateSpec) { in.IdentifierFieldIDs = []int{31} }, "id 31"},
		"identifier the schema does not declare": {func(in *CreateSpec) { in.IdentifierFieldIDs = []int{99} }, "id 99"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			in := nestedSpec(t)
			tc.mutate(&in)

			// When: the table is created
			_, err := New(in, testNow)

			// Then: it is refused as invalid, naming what the caller sent
			if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), tc.names) {
				t.Fatalf("New err = %v, want ErrInvalid naming %s", err, tc.names)
			}
		})
	}
}

func TestNew_acceptsWhatTheReferenceImplementationAccepts(t *testing.T) {
	// Given: a sort by a map value, which unlike a partition source may sit in
	// a map, and a void partition field, whose source is never type-checked
	in := nestedSpec(t)
	in.SortFields = []SortField{{SourceID: 32, Transform: "identity", Direction: "asc", NullOrder: "nulls-first"}}
	in.PartitionFields = []PartitionField{{SourceID: 40, Name: "dropped", Transform: "void"}}

	// When: the table is created
	m, err := New(in, testNow)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// Then: both follow their sources to the fresh ids: attrs.value is 7, point 4
	if got := m.SortOrders[0].Fields[0].SourceID; got != 7 {
		t.Errorf("sort source = %d, want 7", got)
	}
	if got := m.PartitionSpecs[0].Fields[0].SourceID; got != 4 {
		t.Errorf("partition source = %d, want 4", got)
	}
}

func TestNew_acceptsAFieldOfARequiredStructAsIdentifierAndSource(t *testing.T) {
	// Given: a required struct whose required primitive field is the identifier,
	// the partition source and the sort source
	fields := []Field{{ID: 7, Name: "key", Required: true, Type: Type{Struct: &StructType{Fields: []Field{
		{ID: 3, Name: "tenant", Required: true, Type: PrimitiveType("string")},
	}}}}}
	in := CreateSpec{
		TableUUID: "u", Location: "s3://w--table-s3", Fields: fields, IdentifierFieldIDs: []int{3},
		PartitionFields: []PartitionField{{SourceID: 3, Name: "tenant", Transform: "identity"}},
		SortFields:      []SortField{{SourceID: 3, Transform: "identity", Direction: "asc", NullOrder: "nulls-first"}},
	}

	// When: the table is created
	m, err := New(in, testNow)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// Then: every reference follows the field to its fresh id, 2
	s := m.Schemas[0]
	if s.IdentifierFieldIDs[0] != 2 || m.PartitionSpecs[0].Fields[0].SourceID != 2 || m.SortOrders[0].Fields[0].SourceID != 2 {
		t.Errorf("identifier %v, partition %+v, sort %+v", s.IdentifierFieldIDs, m.PartitionSpecs[0].Fields, m.SortOrders[0].Fields)
	}
}
