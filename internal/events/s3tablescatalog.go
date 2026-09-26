package events

import (
	"context"
	"time"
)

// The in-process read view of S3 Tables: how Glue's s3tablescatalog
// federated catalog lists table buckets, namespaces and tables without
// importing internal/services/s3tables. router.go hands Glue the view
// s3tables.Service.Catalog returns.
//
// Every method reads the Region the context carries, as an S3 Tables request
// in that Region would. A missing bucket, namespace or table is found=false
// with a nil error; an error is a store failure.

// S3TablesCatalog reads S3 Tables' buckets, namespaces and tables.
type S3TablesCatalog interface {
	ListTableBuckets(ctx context.Context) ([]S3TableBucket, error)
	GetTableBucket(ctx context.Context, bucket string) (S3TableBucket, bool, error)
	ListNamespaces(ctx context.Context, bucket string) ([]S3TablesNamespace, error)
	GetNamespace(ctx context.Context, bucket, namespace string) (S3TablesNamespace, bool, error)
	ListTables(ctx context.Context, bucket, namespace string) ([]S3TablesTable, error)
	GetTable(ctx context.Context, bucket, namespace, name string) (S3TablesTable, bool, error)
}

// S3TableBucket is one table bucket.
type S3TableBucket struct {
	Name      string
	ARN       string
	CreatedAt time.Time
}

// S3TablesNamespace is one namespace of a table bucket.
type S3TablesNamespace struct {
	Name      string
	CreatedAt time.Time
}

// S3TablesTable is one Iceberg table. MetadataLocation is empty for a table
// created without a schema that nothing has committed to yet; Columns are
// its current schema's, in Hive type names, and empty when it has none.
type S3TablesTable struct {
	Name              string
	Namespace         string
	ARN               string
	MetadataLocation  string
	WarehouseLocation string
	CreatedAt         time.Time
	ModifiedAt        time.Time
	Columns           []S3TablesColumn
}

// S3TablesColumn is one column of a table's current schema.
type S3TablesColumn struct {
	Name    string
	Type    string
	Comment string
}
