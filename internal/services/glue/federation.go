package glue

// federation.go — s3tablescatalog, the federated catalog S3 Tables appears
// in the Data Catalog through: one child catalog per table bucket, whose
// databases are the bucket's namespaces and whose tables are its Iceberg
// tables. It is read live from S3 Tables on every call, so a bucket,
// namespace or table appears the moment it is created, as AWS's
// integration mounts them. Nothing is copied into Glue's own store.
//
// Lake Formation is not modelled: every catalog, database and table is
// readable by every caller, as under AWS's IAM-only access mode.

import (
	"context"
	"time"

	"github.com/overcast-sh/overcast/internal/events"
	"github.com/overcast-sh/overcast/internal/protocol"
)

// InitS3Tables wires the S3 Tables view s3tablescatalog is read from.
func (s *Service) InitS3Tables(src events.S3TablesCatalog) { s.s3tables = src }

// Catalogs resolves a CatalogId to the catalog it names, for other services
// that read the Data Catalog in-process.
type Catalogs interface {
	// Default is the account's own Data Catalog.
	Default() Catalog
	// Resolve returns the catalog catalogID names: a table bucket's catalog
	// for "<account>:s3tablescatalog/<bucket>", and otherwise as a Glue
	// operation's CatalogId is read. found is false for a catalog Overcast
	// does not have, such as a table bucket that does not exist.
	Resolve(ctx context.Context, catalogID string) (cat Catalog, found bool, err error)
}

// Catalogs returns the service's catalog resolver.
func (s *Service) Catalogs() Catalogs { return catalogs{s: s} }

type catalogs struct{ s *Service }

func (c catalogs) Default() Catalog { return c.s.Catalog() }

func (c catalogs) Resolve(ctx context.Context, catalogID string) (Catalog, bool, error) {
	return c.s.resolveCatalog(ctx, c.s.parseCatalogID(catalogID))
}

// resolveCatalog is the catalog p names. found is false for a catalog
// Overcast does not have: one under s3tablescatalog when S3 Tables is not
// wired, a table bucket that does not exist, and any catalogUnknown.
func (s *Service) resolveCatalog(ctx context.Context, p catalogPath) (Catalog, bool, error) {
	switch {
	case p.kind == catalogDefault:
		return s.Catalog(), true, nil
	case s.s3tables == nil || p.kind == catalogUnknown:
		return nil, false, nil
	case p.kind == catalogS3Tables:
		return emptyCatalog{}, true, nil // the parent holds catalogs, not databases
	}
	if _, found, err := s.s3tables.GetTableBucket(ctx, p.bucket); !found || err != nil {
		return nil, false, err
	}
	return tableBucketCatalog{src: s.s3tables, bucket: p.bucket, catalogID: s.catalogIDOf(p)}, true, nil
}

// readCatalog resolves a read operation's CatalogId, answering the modeled
// EntityNotFoundException for a catalog that does not exist.
func (s *Service) readCatalog(ctx context.Context, id string) (Catalog, *protocol.AWSError) {
	cat, found, err := s.resolveCatalog(ctx, s.parseCatalogID(id))
	if err != nil {
		return nil, errInternal(err)
	}
	if !found {
		return nil, errCatalogNotFound(id)
	}
	return cat, nil
}

// emptyCatalog is s3tablescatalog read as a catalog of databases: it has none.
type emptyCatalog struct{}

func (emptyCatalog) GetDatabase(context.Context, string) (Database, bool, error) {
	return Database{}, false, nil
}
func (emptyCatalog) ListDatabases(context.Context) ([]Database, error) { return nil, nil }
func (emptyCatalog) GetTable(context.Context, string, string) (Table, bool, error) {
	return Table{}, false, nil
}
func (emptyCatalog) ListTables(context.Context, string) ([]Table, error) { return nil, nil }
func (emptyCatalog) ListPartitions(context.Context, string, string) ([]Partition, error) {
	return nil, nil
}

// tableBucketCatalog is one table bucket's catalog.
type tableBucketCatalog struct {
	src       events.S3TablesCatalog
	bucket    string
	catalogID string
}

var (
	_ Catalog = emptyCatalog{}
	_ Catalog = tableBucketCatalog{}
)

func (c tableBucketCatalog) GetDatabase(ctx context.Context, name string) (Database, bool, error) {
	ns, found, err := c.src.GetNamespace(ctx, c.bucket, normName(name))
	if !found || err != nil {
		return Database{}, false, err
	}
	return c.database(ns), true, nil
}

func (c tableBucketCatalog) ListDatabases(ctx context.Context) ([]Database, error) {
	namespaces, err := c.src.ListNamespaces(ctx, c.bucket)
	if err != nil {
		return nil, err
	}
	out := make([]Database, len(namespaces))
	for i, ns := range namespaces {
		out[i] = c.database(ns)
	}
	return out, nil
}

func (c tableBucketCatalog) GetTable(ctx context.Context, databaseName, tableName string) (Table, bool, error) {
	t, found, err := c.src.GetTable(ctx, c.bucket, normName(databaseName), normName(tableName))
	if !found || err != nil {
		return Table{}, false, err
	}
	return c.table(t), true, nil
}

func (c tableBucketCatalog) ListTables(ctx context.Context, databaseName string) ([]Table, error) {
	tables, err := c.src.ListTables(ctx, c.bucket, normName(databaseName))
	if err != nil {
		return nil, err
	}
	out := make([]Table, len(tables))
	for i, t := range tables {
		out[i] = c.table(t)
	}
	return out, nil
}

// ListPartitions lists nothing: an Iceberg table's partitions are in its
// metadata, not in the catalog.
func (tableBucketCatalog) ListPartitions(context.Context, string, string) ([]Partition, error) {
	return nil, nil
}

func (c tableBucketCatalog) database(ns events.S3TablesNamespace) Database {
	return Database{
		Name: ns.Name, CatalogId: c.catalogID, CreateTime: epochSeconds(ns.CreatedAt),
		CreateTableDefaultPermissions: iamAllowedPrincipals(),
	}
}

// table is an S3 Tables table as the Data Catalog describes an Iceberg
// table: an external table whose table_type and metadata_location
// parameters point readers at its metadata, with its warehouse as the
// storage location and its current schema's columns.
func (c tableBucketCatalog) table(t events.S3TablesTable) Table {
	params := map[string]string{"table_type": "ICEBERG"}
	if t.MetadataLocation != "" {
		params["metadata_location"] = t.MetadataLocation
	}
	cols := make([]Column, len(t.Columns))
	for i, col := range t.Columns {
		cols[i] = Column{Name: col.Name, Type: col.Type, Comment: col.Comment}
	}
	out := Table{
		Name: t.Name, DatabaseName: t.Namespace, CatalogId: c.catalogID,
		CreateTime: epochSeconds(t.CreatedAt), UpdateTime: epochSeconds(t.ModifiedAt),
		TableType: defaultTableType, Parameters: params,
		StorageDescriptor: &StorageDescriptor{Columns: cols, Location: t.WarehouseLocation},
	}
	out.normalize()
	return out
}

// epochSeconds is t in the epoch seconds Glue's timestamps are written in.
func epochSeconds(t time.Time) float64 { return float64(t.UnixMilli()) / 1000.0 }
