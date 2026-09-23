package glue

import "context"

// Catalog is the read-only view of the Data Catalog that other services use
// in-process — Athena's metadata operations and query engine wiring read
// databases, tables and partitions through it rather than over HTTP.
//
// Names are folded to lowercase, as the Glue API folds them. A missing
// database or table is (zero, false, nil); an error is a store failure. Each
// call returns freshly decoded values the caller may keep or modify.
type Catalog interface {
	GetDatabase(ctx context.Context, name string) (Database, bool, error)
	ListDatabases(ctx context.Context) ([]Database, error)
	GetTable(ctx context.Context, databaseName, tableName string) (Table, bool, error)
	ListTables(ctx context.Context, databaseName string) ([]Table, error)
	ListPartitions(ctx context.Context, databaseName, tableName string) ([]Partition, error)
}

// Catalog returns the service's read-only catalog view.
func (s *Service) Catalog() Catalog { return catalogReader{store: s.store} }

type catalogReader struct{ store *glueStore }

var _ Catalog = catalogReader{}

func (c catalogReader) GetDatabase(ctx context.Context, name string) (Database, bool, error) {
	db, found, err := c.store.getDatabase(ctx, normName(name))
	if !found || err != nil {
		return Database{}, false, err
	}
	return db.Database, true, nil
}

func (c catalogReader) ListDatabases(ctx context.Context) ([]Database, error) {
	recs, err := c.store.listDatabases(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Database, len(recs))
	for i, r := range recs {
		out[i] = r.Database
	}
	return out, nil
}

func (c catalogReader) GetTable(ctx context.Context, databaseName, tableName string) (Table, bool, error) {
	t, found, err := c.store.getTable(ctx, normName(databaseName), normName(tableName))
	if !found || err != nil {
		return Table{}, false, err
	}
	return t.Table, true, nil
}

func (c catalogReader) ListTables(ctx context.Context, databaseName string) ([]Table, error) {
	recs, err := c.store.listTables(ctx, normName(databaseName))
	if err != nil {
		return nil, err
	}
	out := make([]Table, len(recs))
	for i, r := range recs {
		out[i] = r.Table
	}
	return out, nil
}

func (c catalogReader) ListPartitions(ctx context.Context, databaseName, tableName string) ([]Partition, error) {
	ps, err := c.store.listPartitions(ctx, normName(databaseName), normName(tableName))
	if err != nil {
		return nil, err
	}
	out := make([]Partition, len(ps))
	for i, p := range ps {
		out[i] = *p
	}
	return out, nil
}
