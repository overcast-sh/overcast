package icebergmeta

import (
	"encoding/json"
	"errors"
	"testing"
)

func baseInput() TableInput {
	return TableInput{
		TableUUID:     "5f1a8f36-8b3a-4bcb-9b3a-1c2d3e4f5a6b",
		Location:      "s3://abc--table-s3",
		LastUpdatedMS: 1758628800000,
		Columns: []ColumnInput{
			{Name: "id", Type: "long", Required: true},
			{Name: "name", Type: "string"},
			{Name: "amount", Type: "Decimal( 10 ,2 )"},
		},
	}
}

func TestNew_writesTheSpecsInitialTable(t *testing.T) {
	// Given: a three-column table with no partitioning or sort order
	in := baseInput()
	in.Properties = map[string]string{"write.format.default": "parquet"}

	// When: its metadata is built and serialised
	m, err := New(in)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	// Then: it carries every required v2 field with the initial ids
	want := map[string]any{
		"format-version":        float64(2),
		"table-uuid":            in.TableUUID,
		"location":              in.Location,
		"last-sequence-number":  float64(0),
		"last-updated-ms":       float64(in.LastUpdatedMS),
		"last-column-id":        float64(3),
		"current-schema-id":     float64(0),
		"default-spec-id":       float64(0),
		"last-partition-id":     float64(999),
		"default-sort-order-id": float64(0),
	}
	for k, v := range want {
		if doc[k] != v {
			t.Errorf("%s = %v, want %v", k, doc[k], v)
		}
	}
	fields := m.Schemas[0].Fields
	for i, f := range fields {
		if f.ID != i+1 {
			t.Errorf("field %q id = %d, want %d", f.Name, f.ID, i+1)
		}
	}
	if fields[2].Type != "decimal(10, 2)" || !fields[0].Required {
		t.Errorf("fields = %+v", fields)
	}
	if len(m.PartitionSpecs) != 1 || len(m.PartitionSpecs[0].Fields) != 0 {
		t.Errorf("partition specs = %+v", m.PartitionSpecs)
	}
	if len(m.SortOrders) != 1 || m.SortOrders[0].OrderID != 0 {
		t.Errorf("sort orders = %+v", m.SortOrders)
	}
	if m.Properties["write.format.default"] != "parquet" {
		t.Errorf("properties = %v", m.Properties)
	}
}

func TestNew_partitionAndSortOrder(t *testing.T) {
	// Given: a table partitioned by day(id) and sorted by name
	in := baseInput()
	in.Partition = []PartitionFieldInput{{SourceID: 1, Name: "id_bucket", Transform: "bucket[16]"}}
	in.SortOrderID = 1
	in.SortFields = []SortFieldInput{{SourceID: 2, Transform: "identity", Direction: "asc", NullOrder: "nulls-first"}}

	// When: its metadata is built
	m, err := New(in)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// Then: the partition field gets the first partition id and the sort order is the default
	if got := m.PartitionSpecs[0].Fields[0].FieldID; got != 1000 || m.LastPartitionID != 1000 {
		t.Errorf("partition field id = %d, last = %d", got, m.LastPartitionID)
	}
	if m.DefaultSortOrderID != 1 || len(m.SortOrders) != 2 {
		t.Errorf("sort orders = %+v default %d", m.SortOrders, m.DefaultSortOrderID)
	}
}

func TestNew_rejectsWhatTheSpecDoesNotAllow(t *testing.T) {
	cases := map[string]func(*TableInput){
		"no columns":         func(in *TableInput) { in.Columns = nil },
		"duplicate column":   func(in *TableInput) { in.Columns = append(in.Columns, ColumnInput{Name: "id", Type: "int"}) },
		"unknown type":       func(in *TableInput) { in.Columns[0].Type = "varchar" },
		"nested type string": func(in *TableInput) { in.Columns[0].Type = "list<int>" },
		"bad partition": func(in *TableInput) {
			in.Partition = []PartitionFieldInput{{SourceID: 9, Name: "p", Transform: "identity"}}
		},
		"sort order id zero":  func(in *TableInput) { in.SortFields = []SortFieldInput{{SourceID: 1, Transform: "identity"}} },
		"missing location":    func(in *TableInput) { in.Location = "" },
		"column without name": func(in *TableInput) { in.Columns[1].Name = "" },
		"v3-only type":        func(in *TableInput) { in.Columns[0].Type = "timestamp_ns" },
		"duplicate partition id": func(in *TableInput) {
			in.Partition = []PartitionFieldInput{{SourceID: 1, Name: "a", Transform: "identity", FieldID: 1001}, {SourceID: 2, Name: "b", Transform: "identity", FieldID: 1001}}
		},
		"duplicate partition name": func(in *TableInput) {
			in.Partition = []PartitionFieldInput{{SourceID: 1, Name: "a", Transform: "identity"}, {SourceID: 2, Name: "a", Transform: "identity"}}
		},
		"bad sort direction": func(in *TableInput) {
			in.SortOrderID = 1
			in.SortFields = []SortFieldInput{{SourceID: 1, Transform: "identity", Direction: "up", NullOrder: "nulls-first"}}
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			in := baseInput()
			mutate(&in)
			_, err := New(in)
			var inv *ErrInvalid
			if !errors.As(err, &inv) {
				t.Fatalf("New err = %v, want *ErrInvalid", err)
			}
		})
	}
}

func TestFileName(t *testing.T) {
	if got := FileName(0, "abc"); got != "00000-abc.metadata.json" {
		t.Errorf("FileName = %q", got)
	}
}
