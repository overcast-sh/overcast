package s3tables

// The read view Glue's s3tablescatalog federated catalog serves S3 Tables
// through: table buckets as child catalogs, namespaces as databases and
// tables as tables. It reads the store directly, in the Region the context
// carries, and takes no lock: like any S3 Tables read, it sees each record as
// last written.

import (
	"context"

	"go.uber.org/zap"

	"github.com/overcast-sh/overcast/internal/events"
	"github.com/overcast-sh/overcast/internal/protocol"
)

// Catalog returns the read view of S3 Tables that Glue federates.
func (s *Service) Catalog() events.S3TablesCatalog { return catalogView{s: s} }

type catalogView struct{ s *Service }

var _ events.S3TablesCatalog = catalogView{}

func (v catalogView) ListTableBuckets(ctx context.Context) ([]events.S3TableBucket, error) {
	buckets, aerr := v.s.listBuckets(ctx, v.s.regionOf(ctx))
	if aerr != nil {
		return nil, aerr
	}
	out := make([]events.S3TableBucket, len(buckets))
	for i, b := range buckets {
		out[i] = bucketView(b)
	}
	return out, nil
}

func (v catalogView) GetTableBucket(ctx context.Context, bucket string) (events.S3TableBucket, bool, error) {
	b, found, aerr := v.s.loadBucket(ctx, v.s.regionOf(ctx), bucket)
	if !found || aerr != nil {
		return events.S3TableBucket{}, false, asError(aerr)
	}
	return bucketView(b), true, nil
}

func (v catalogView) ListNamespaces(ctx context.Context, bucket string) ([]events.S3TablesNamespace, error) {
	namespaces, aerr := v.s.listNamespaces(ctx, v.s.regionOf(ctx), bucket)
	if aerr != nil {
		return nil, aerr
	}
	out := make([]events.S3TablesNamespace, len(namespaces))
	for i, n := range namespaces {
		out[i] = namespaceView(n)
	}
	return out, nil
}

func (v catalogView) GetNamespace(ctx context.Context, bucket, namespace string) (events.S3TablesNamespace, bool, error) {
	n, found, aerr := v.s.loadNamespace(ctx, v.s.regionOf(ctx), bucket, namespace)
	if !found || aerr != nil {
		return events.S3TablesNamespace{}, false, asError(aerr)
	}
	return namespaceView(n), true, nil
}

// ListTables lists one namespace's tables. An empty namespace would list the
// whole bucket, which no database is, so it lists nothing.
func (v catalogView) ListTables(ctx context.Context, bucket, namespace string) ([]events.S3TablesTable, error) {
	if namespace == "" {
		return nil, nil
	}
	tables, aerr := v.s.listTables(ctx, v.s.regionOf(ctx), bucket, namespace)
	if aerr != nil {
		return nil, aerr
	}
	out := make([]events.S3TablesTable, len(tables))
	for i, t := range tables {
		out[i] = v.tableView(ctx, t)
	}
	return out, nil
}

func (v catalogView) GetTable(ctx context.Context, bucket, namespace, name string) (events.S3TablesTable, bool, error) {
	t, found, aerr := v.s.loadTable(ctx, v.s.regionOf(ctx), bucket, namespace, name)
	if !found || aerr != nil {
		return events.S3TablesTable{}, false, asError(aerr)
	}
	return v.tableView(ctx, t), true, nil
}

func bucketView(b *tableBucket) events.S3TableBucket {
	return events.S3TableBucket{Name: b.Name, ARN: b.ARN, CreatedAt: b.CreatedAt}
}

func namespaceView(n *namespaceRecord) events.S3TablesNamespace {
	return events.S3TablesNamespace{Name: n.Name, CreatedAt: n.CreatedAt}
}

// tableView describes a table with its current schema's columns. A table
// whose metadata cannot be read is still listed, without columns: the
// metadata file is the caller's data, and one bad file must not hide the
// table or its neighbours.
func (v catalogView) tableView(ctx context.Context, t *tableRecord) events.S3TablesTable {
	out := events.S3TablesTable{
		Name: t.Name, Namespace: t.Namespace, ARN: t.ARN,
		MetadataLocation: t.MetadataLocation, WarehouseLocation: t.WarehouseLocation,
		CreatedAt: t.CreatedAt, ModifiedAt: t.ModifiedAt,
	}
	if t.MetadataLocation == "" || v.s.getObject == nil {
		return out
	}
	meta, aerr := v.s.readMetadata(ctx, t.MetadataLocation)
	if aerr != nil {
		v.s.log.Debug("s3tables: table listed without columns", zap.String("table", t.ARN), zap.String("reason", aerr.Message))
		return out
	}
	if schema, ok := meta.CurrentSchema(); ok {
		out.Columns = make([]events.S3TablesColumn, len(schema.Fields))
		for i, f := range schema.Fields {
			out.Columns[i] = events.S3TablesColumn{Name: f.Name, Type: f.Type.HiveTypeName(), Comment: f.Doc}
		}
	}
	return out
}

// asError is aerr as an error, nil when it is: a nil *protocol.AWSError in
// an error interface is not a nil error.
func asError(aerr *protocol.AWSError) error {
	if aerr == nil {
		return nil
	}
	return aerr
}
