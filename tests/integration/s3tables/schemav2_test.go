package s3tables_test

import (
	"encoding/json"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3tables"
	"github.com/aws/aws-sdk-go-v2/service/s3tables/document"
	"github.com/aws/aws-sdk-go-v2/service/s3tables/types"

	"github.com/overcast-sh/overcast/internal/icebergmeta"
)

// nestedSchemaV2 is a schemaV2 in the caller's own numbering: a list, a map
// and a struct, with a partition on a field of the struct and the table's
// identifier on a top-level column.
func nestedSchemaV2() types.IcebergMetadata {
	return types.IcebergMetadata{
		SchemaV2: &types.IcebergSchemaV2{
			Type: types.SchemaV2FieldTypeStruct, SchemaId: aws.Int32(3), IdentifierFieldIds: []int32{10},
			Fields: []types.SchemaV2Field{
				{Id: aws.Int32(10), Name: aws.String("id"), Required: aws.Bool(true), Type: document.NewLazyDocument("long"), Doc: aws.String("row id")},
				{Id: aws.Int32(20), Name: aws.String("tags"), Required: aws.Bool(false), Type: document.NewLazyDocument(map[string]any{
					"type": "list", "element-id": 21, "element": "string", "element-required": false,
				})},
				{Id: aws.Int32(30), Name: aws.String("attrs"), Required: aws.Bool(false), Type: document.NewLazyDocument(map[string]any{
					"type": "map", "key-id": 31, "key": "string", "value-id": 32, "value": "double", "value-required": true,
				})},
				{Id: aws.Int32(40), Name: aws.String("customer"), Required: aws.Bool(true), Type: document.NewLazyDocument(map[string]any{
					"type": "struct", "fields": []any{
						map[string]any{"id": 41, "name": "region", "required": true, "type": "string"},
					},
				})},
			},
		},
		PartitionSpec: &types.IcebergPartitionSpec{Fields: []types.IcebergPartitionField{
			{SourceId: aws.Int32(41), Transform: aws.String("identity"), Name: aws.String("region")},
		}},
	}
}

// readMetadata is the Iceberg metadata document the table points at.
func (f *fixture) readMetadata(t *testing.T, name string) *icebergmeta.Metadata {
	t.Helper()
	got, err := f.tables.GetTableMetadataLocation(f.ctx, &s3tables.GetTableMetadataLocationInput{
		TableBucketARN: aws.String(f.bucketARN), Namespace: aws.String(f.namespace), Name: aws.String(name),
	})
	if err != nil {
		t.Fatalf("GetTableMetadataLocation: %v", err)
	}
	raw, err := json.Marshal(f.readS3(t, aws.ToString(got.MetadataLocation)))
	if err != nil {
		t.Fatal(err)
	}
	meta, err := icebergmeta.Parse(raw)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return meta
}

func TestCreateTable_schemaV2WritesNestedTypesWithFreshIDs(t *testing.T) {
	// Given: a bucket and namespace
	f := newFixture(t, "v2-bucket")

	// When: a table is created with a nested schemaV2
	f.createTable(t, "orders", &types.TableMetadataMemberIceberg{Value: nestedSchemaV2()})

	// Then: its first metadata.json is schema 0, numbered depth-first from 1
	meta := f.readMetadata(t, "orders")
	s := meta.Schemas[0]
	if s.SchemaID != 0 || len(s.Fields) != 4 || s.Fields[0].Doc != "row id" {
		t.Fatalf("schema = %+v", s)
	}
	for i, col := range s.Fields {
		if col.ID != i+1 {
			t.Errorf("column %q id = %d, want %d", col.Name, col.ID, i+1)
		}
	}
	list, mp, st := s.Fields[1].Type.List, s.Fields[2].Type.Map, s.Fields[3].Type.Struct
	if list == nil || mp == nil || st == nil {
		t.Fatalf("nested types lost: %+v", s.Fields)
	}
	if list.ElementID != 5 || mp.KeyID != 6 || mp.ValueID != 7 || !mp.ValueRequired || st.Fields[0].ID != 8 {
		t.Errorf("nested ids: element %d, key %d, value %d, customer.region %d", list.ElementID, mp.KeyID, mp.ValueID, st.Fields[0].ID)
	}

	// And: the identifier and the partition follow their fields to the new ids
	if len(s.IdentifierFieldIDs) != 1 || s.IdentifierFieldIDs[0] != 1 || meta.LastColumnID != 8 {
		t.Errorf("identifier ids %v, last column %d", s.IdentifierFieldIDs, meta.LastColumnID)
	}
	if got := meta.PartitionSpecs[0].Fields[0].SourceID; got != 8 {
		t.Errorf("partition source-id = %d, want 8 (customer.region)", got)
	}
}

func TestCreateTable_schemaV2RefusalsLeaveNothingBehind(t *testing.T) {
	cases := map[string]func(*types.IcebergMetadata){
		"schema and schemaV2 together": func(m *types.IcebergMetadata) {
			m.Schema = &types.IcebergSchema{Fields: []types.SchemaField{{Name: aws.String("id"), Type: aws.String("long")}}}
		},
		"partition by a list element": func(m *types.IcebergMetadata) {
			m.PartitionSpec.Fields[0].SourceId = aws.Int32(21)
		},
		"optional identifier field": func(m *types.IcebergMetadata) {
			m.SchemaV2.IdentifierFieldIds = []int32{20}
		},
		"unknown primitive": func(m *types.IcebergMetadata) {
			m.SchemaV2.Fields[0].Type = document.NewLazyDocument("varchar")
		},
		"field id used twice": func(m *types.IcebergMetadata) {
			m.SchemaV2.Fields[1].Id = aws.Int32(10)
		},
	}
	f := newFixture(t, "v2-refusals")
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			// Given: the nested schemaV2, altered in one way AWS or Iceberg refuses
			metadata := nestedSchemaV2()
			mutate(&metadata)

			// When: a table is created with it
			_, err := f.tables.CreateTable(f.ctx, &s3tables.CreateTableInput{
				TableBucketARN: aws.String(f.bucketARN), Namespace: aws.String(f.namespace), Name: aws.String("refused"),
				Format: types.OpenTableFormatIceberg, Metadata: &types.TableMetadataMemberIceberg{Value: metadata},
			})

			// Then: it is a bad request and no table exists
			if code := apiErrorCode(t, err); code != "BadRequestException" {
				t.Errorf("code = %s", code)
			}
		})
	}
	list, err := f.tables.ListTables(f.ctx, &s3tables.ListTablesInput{TableBucketARN: aws.String(f.bucketARN)})
	if err != nil || len(list.Tables) != 0 {
		t.Errorf("ListTables = %+v, %v", list, err)
	}
}

func TestCreateTable_nestedTypeInSchemaIsRefused(t *testing.T) {
	// Given: a bucket and namespace
	f := newFixture(t, "flat-nested")

	// When: a primitive-only schema declares a column typed like a nested type
	_, err := f.tables.CreateTable(f.ctx, &s3tables.CreateTableInput{
		TableBucketARN: aws.String(f.bucketARN), Namespace: aws.String(f.namespace), Name: aws.String("tags"),
		Format: types.OpenTableFormatIceberg,
		Metadata: &types.TableMetadataMemberIceberg{Value: types.IcebergMetadata{Schema: &types.IcebergSchema{
			Fields: []types.SchemaField{{Name: aws.String("tags"), Type: aws.String("list<string>")}},
		}}},
	})

	// Then: it is refused: schema takes primitive types only
	if code := apiErrorCode(t, err); code != "BadRequestException" {
		t.Errorf("code = %s", code)
	}
}
