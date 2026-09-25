package icebergmeta

import (
	"encoding/json"
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
		SortOrderID:     1,
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
	if got := m.SortOrders[1].Fields[0].SourceID; got != 1 {
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
