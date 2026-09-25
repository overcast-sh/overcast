package icebergmeta

import (
	"encoding/json"
	"testing"
	"time"
)

func TestHiveTypeName_spellsEachTypeAsIcebergsGlueCatalogDoes(t *testing.T) {
	tests := []struct {
		iceberg string
		want    string
	}{
		{`"boolean"`, "boolean"},
		{`"int"`, "int"},
		{`"long"`, "bigint"},
		{`"float"`, "float"},
		{`"double"`, "double"},
		{`"date"`, "date"},
		{`"time"`, "string"},
		{`"timestamp"`, "timestamp"},
		{`"timestamptz"`, "timestamp"},
		{`"string"`, "string"},
		{`"uuid"`, "string"},
		{`"binary"`, "binary"},
		{`"fixed[16]"`, "binary"},
		{`"decimal(10, 2)"`, "decimal(10,2)"},
		{`{"type": "list", "element-id": 2, "element": "long", "element-required": false}`, "array<bigint>"},
		{`{"type": "map", "key-id": 2, "key": "string", "value-id": 3, "value": "uuid", "value-required": true}`, "map<string,string>"},
		{`{"type": "struct", "fields": [{"id": 2, "name": "x", "required": true, "type": "double"},
			{"id": 3, "name": "at", "required": false, "type": "timestamptz"}]}`, "struct<x:double,at:timestamp>"},
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			// Given: an Iceberg type in the spec's JSON
			var typ Type
			if err := json.Unmarshal([]byte(tt.iceberg), &typ); err != nil {
				t.Fatalf("unmarshal %s: %v", tt.iceberg, err)
			}

			// When / Then: its Hive name is the Glue catalog's
			if got := typ.HiveTypeName(); got != tt.want {
				t.Errorf("HiveTypeName(%s) = %q, want %q", tt.iceberg, got, tt.want)
			}
		})
	}
}

func TestCurrentSchema_isTheSchemaTheMetadataNamesCurrent(t *testing.T) {
	// Given: a new table with one column
	meta, err := New(CreateSpec{TableUUID: "u", Location: "s3://w--table-s3",
		Fields: []Field{{ID: 1, Name: "id", Type: PrimitiveType("long")}}}, time.Unix(0, 0))
	if err != nil {
		t.Fatal(err)
	}

	// When
	schema, ok := meta.CurrentSchema()

	// Then
	if !ok || len(schema.Fields) != 1 || schema.Fields[0].Name != "id" {
		t.Errorf("CurrentSchema = %+v, %v", schema, ok)
	}

	// And: metadata naming a schema it lacks has none
	meta.CurrentSchemaID = 99
	if _, ok := meta.CurrentSchema(); ok {
		t.Error("CurrentSchema found a schema the metadata does not hold")
	}
}
