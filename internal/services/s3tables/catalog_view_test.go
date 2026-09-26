package s3tables

import (
	"context"
	"testing"

	"github.com/overcast-sh/overcast/internal/events"
)

// createSchemaTable creates table name in namespace "ns" with two columns.
func createSchemaTable(t *testing.T, s *Service, bucketARN, name string) {
	t.Helper()
	_, aerr := s.createTableTyped(context.Background(), &createTableRequest{
		TableBucketARN: bucketARN, Namespace: "ns", Name: name, Format: formatIceberg,
		Metadata: &tableMetadata{Iceberg: &icebergMetadata{Schema: &icebergSchema{Fields: []icebergSchemaField{
			{Name: "id", Type: "long", Required: true},
			{Name: "amount", Type: "decimal(10,2)"},
		}}}},
	})
	if aerr != nil {
		t.Fatalf("createTable %s: %v", name, aerr)
	}
}

func TestCatalogView_readsBucketsNamespacesAndTables(t *testing.T) {
	// Given: a bucket "seed" with namespace "ns" holding a table with a schema
	// and one without
	s, _ := newTestService(t)
	withMemoryS3(s)
	arn := seed(t, s)
	createSchemaTable(t, s, arn, "orders")
	if _, aerr := s.createTableTyped(context.Background(), &createTableRequest{
		TableBucketARN: arn, Namespace: "ns", Name: "empty", Format: formatIceberg,
	}); aerr != nil {
		t.Fatal(aerr)
	}
	v, ctx := s.Catalog(), context.Background()

	// When / Then: the bucket is listed and found by name
	buckets, err := v.ListTableBuckets(ctx)
	if err != nil || len(buckets) != 1 || buckets[0].Name != "seed" || buckets[0].ARN != arn {
		t.Fatalf("ListTableBuckets = %+v, %v", buckets, err)
	}
	if _, found, err := v.GetTableBucket(ctx, "absent"); found || err != nil {
		t.Errorf("GetTableBucket(absent) = %v, %v", found, err)
	}

	// And: the namespace
	namespaces, err := v.ListNamespaces(ctx, "seed")
	if err != nil || len(namespaces) != 1 || namespaces[0].Name != "ns" {
		t.Fatalf("ListNamespaces = %+v, %v", namespaces, err)
	}
	if _, found, err := v.GetNamespace(ctx, "seed", "ns"); !found || err != nil {
		t.Errorf("GetNamespace(ns) = %v, %v", found, err)
	}

	// And: the tables, the one with a schema carrying its columns in Hive names
	tables, err := v.ListTables(ctx, "seed", "ns")
	if err != nil || len(tables) != 2 {
		t.Fatalf("ListTables = %+v, %v", tables, err)
	}
	orders, found, err := v.GetTable(ctx, "seed", "ns", "orders")
	if !found || err != nil {
		t.Fatalf("GetTable(orders) = %v, %v", found, err)
	}
	want := []events.S3TablesColumn{{Name: "id", Type: "bigint"}, {Name: "amount", Type: "decimal(10,2)"}}
	if len(orders.Columns) != len(want) || orders.Columns[0] != want[0] || orders.Columns[1] != want[1] {
		t.Errorf("columns = %+v, want %+v", orders.Columns, want)
	}
	if orders.MetadataLocation == "" || orders.WarehouseLocation == "" {
		t.Errorf("locations missing: %+v", orders)
	}
	empty, _, _ := v.GetTable(ctx, "seed", "ns", "empty")
	if empty.MetadataLocation != "" || len(empty.Columns) != 0 {
		t.Errorf("a table without metadata = %+v", empty)
	}
}

func TestCatalogView_listsATableWhoseMetadataIsUnreadable(t *testing.T) {
	// Given: a table whose metadata file has gone
	s, _ := newTestService(t)
	s3 := withMemoryS3(s)
	arn := seed(t, s)
	createSchemaTable(t, s, arn, "orders")
	clear(s3.objects)

	// When
	tables, err := s.Catalog().ListTables(context.Background(), "seed", "ns")

	// Then: the table is still listed, without columns
	if err != nil || len(tables) != 1 || len(tables[0].Columns) != 0 {
		t.Errorf("ListTables = %+v, %v", tables, err)
	}
}

func TestCatalogView_listsNoTablesForAnEmptyNamespace(t *testing.T) {
	// Given: a bucket with a table
	s, _ := newTestService(t)
	withMemoryS3(s)
	arn := seed(t, s)
	createSchemaTable(t, s, arn, "orders")

	// When: tables are listed with no namespace
	tables, err := s.Catalog().ListTables(context.Background(), "seed", "")

	// Then: none are, rather than the whole bucket's
	if err != nil || len(tables) != 0 {
		t.Errorf("ListTables = %+v, %v", tables, err)
	}
}
