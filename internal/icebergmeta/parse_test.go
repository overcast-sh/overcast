package icebergmeta

import (
	"encoding/json"
	"errors"
	"testing"
)

// versionOneDoc is a format-version 1 document in the form the spec's
// version 1 writers produce: a single schema and partition-spec, no ids on
// the partition fields, no sort orders, and a current snapshot but no refs.
const versionOneDoc = `{
  "format-version": 1,
  "table-uuid": "d20125c8-7284-442c-9aea-15fee620737c",
  "location": "s3://bucket/test/location",
  "last-updated-ms": 1602638573874,
  "last-column-id": 3,
  "schema": {"type": "struct", "fields": [
    {"id": 1, "name": "x", "required": true, "type": "long"},
    {"id": 2, "name": "y", "required": true, "type": "long", "doc": "comment"},
    {"id": 3, "name": "z", "required": true, "type": "long"}]},
  "partition-spec": [{"name": "x", "transform": "identity", "source-id": 1}],
  "properties": {},
  "current-snapshot-id": 3051729675574597004,
  "snapshots": [{"snapshot-id": 3051729675574597004, "timestamp-ms": 1515100955770,
    "summary": {"operation": "append"}, "manifest-list": "s3://a/b/1.avro"}]
}`

func TestParse_readsAVersionOneDocumentAsTheVersionTwoModel(t *testing.T) {
	// Given: a version 1 document
	// When: it is parsed
	m, err := Parse([]byte(versionOneDoc))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	// Then: its schema, spec and sort order are the lists version 2 keeps,
	// and its current snapshot is on main
	if len(m.Schemas) != 1 || m.CurrentSchemaID != 0 || m.Schemas[0].Fields[1].Doc != "comment" {
		t.Errorf("schemas = %+v", m.Schemas)
	}
	if len(m.PartitionSpecs) != 1 || m.PartitionSpecs[0].Fields[0].FieldID != 1000 || m.LastPartitionID != 1000 {
		t.Errorf("specs = %+v, last = %d", m.PartitionSpecs, m.LastPartitionID)
	}
	if len(m.SortOrders) != 1 || m.DefaultSortOrderID != UnsortedOrderID {
		t.Errorf("sort orders = %+v", m.SortOrders)
	}
	if ref := m.Refs[MainBranch]; ref.SnapshotID != 3051729675574597004 || ref.Type != RefBranch {
		t.Errorf("refs = %+v", m.Refs)
	}

	// And: written again, it is still a version 1 document with its v1 fields
	raw, err := json.Marshal(mustCommit(t, m, "s3://bucket/test/location/metadata/v1.metadata.json", testNow,
		Update{Action: "set-properties", Updates: map[string]string{"k": "v"}}))
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if doc["format-version"] != float64(1) || doc["schema"] == nil || doc["partition-spec"] == nil {
		t.Errorf("rewritten document = %s", raw)
	}
}

func TestParse_refusesWhatItCannotRead(t *testing.T) {
	cases := map[string]string{
		"not JSON":            `{`,
		"format version 3":    `{"format-version": 3, "table-uuid": "u", "location": "s3://w"}`,
		"no location":         `{"format-version": 2, "table-uuid": "u"}`,
		"v2 without a uuid":   `{"format-version": 2, "location": "s3://w"}`,
		"dangling current":    `{"format-version": 2, "table-uuid": "u", "location": "s3://w", "current-schema-id": 0, "schemas": [{"type":"struct","schema-id":0,"fields":[]}], "partition-specs": [{"spec-id":0,"fields":[]}], "current-snapshot-id": 5}`,
		"no current schema":   `{"format-version": 2, "table-uuid": "u", "location": "s3://w", "current-schema-id": 4, "schemas": []}`,
		"unknown nested type": `{"format-version": 2, "table-uuid": "u", "location": "s3://w", "schemas": [{"type":"struct","schema-id":0,"fields":[{"id":1,"name":"a","required":false,"type":{"type":"variant"}}]}]}`,
		"statistics no id":    `{"format-version": 2, "table-uuid": "u", "location": "s3://w", "statistics": [{"statistics-path": "p"}]}`,
		"main is not current": `{"format-version": 2, "table-uuid": "u", "location": "s3://w", "current-schema-id": 0, "schemas": [{"type":"struct","schema-id":0,"fields":[]}], "partition-specs": [{"spec-id":0,"fields":[]}], "current-snapshot-id": 1, "snapshots": [{"snapshot-id": 1, "timestamp-ms": 1}, {"snapshot-id": 2, "timestamp-ms": 2}], "refs": {"main": {"snapshot-id": 2, "type": "branch"}}}`,
	}
	for name, doc := range cases {
		t.Run(name, func(t *testing.T) {
			// Given: a document this package does not accept
			// When: it is parsed
			_, err := Parse([]byte(doc))

			// Then: it is refused as invalid
			if !errors.Is(err, ErrInvalid) {
				t.Errorf("err = %v, want ErrInvalid", err)
			}
		})
	}
}

func TestParse_versionOneWithoutLastPartitionIDKeepsPartitionIDsUnique(t *testing.T) {
	// Given: a version 1 document with partition-specs but no last-partition-id
	doc := `{"format-version": 1, "table-uuid": "u", "location": "s3://w", "last-column-id": 2,
	  "schemas": [{"type": "struct", "schema-id": 0, "fields": [
	    {"id": 1, "name": "a", "required": false, "type": "int"},
	    {"id": 2, "name": "b", "required": false, "type": "int"}]}],
	  "partition-specs": [{"spec-id": 0, "fields": [{"source-id": 1, "field-id": 1000, "name": "a", "transform": "identity"}]}]}`
	m, err := Parse([]byte(doc))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	// When: a spec on another column is added
	m = mustCommit(t, m, "s3://w/metadata/v1.metadata.json", testNow,
		Update{Action: "add-spec", Spec: &PartitionSpec{Fields: []PartitionField{{SourceID: 2, Name: "b", Transform: "identity"}}}})

	// Then: its field gets the next id, not 1000 again
	if got := m.PartitionSpecs[1].Fields[0].FieldID; got != 1001 {
		t.Errorf("new partition field id = %d, want 1001", got)
	}
}
