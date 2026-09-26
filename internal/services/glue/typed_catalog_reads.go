package glue

// typed_catalog_reads.go — the database and table reads. Each resolves its
// CatalogId (federation.go), so it serves the account's own catalog and the
// federated S3 Tables catalogs alike.

import (
	"context"
	"regexp"

	"github.com/overcast-sh/overcast/internal/protocol"
)

// requireCatalogDatabase loads a database from a resolved catalog, answering
// EntityNotFoundException when it is not there.
func requireCatalogDatabase(ctx context.Context, cat Catalog, name string) (Database, *protocol.AWSError) {
	if name == "" {
		return Database{}, errInvalidInput("DatabaseName is required.")
	}
	db, found, err := cat.GetDatabase(ctx, name)
	if err != nil {
		return db, errInternal(err)
	}
	if !found {
		return db, errDatabaseNotFound(normName(name))
	}
	return db, nil
}

// requireCatalogTable loads a table from a resolved catalog, checking its
// database first as AWS does.
func requireCatalogTable(ctx context.Context, cat Catalog, dbName, name string) (Table, *protocol.AWSError) {
	if _, aerr := requireCatalogDatabase(ctx, cat, dbName); aerr != nil {
		return Table{}, aerr
	}
	if name == "" {
		return Table{}, errInvalidInput("Table name is required.")
	}
	t, found, err := cat.GetTable(ctx, dbName, name)
	if err != nil {
		return t, errInternal(err)
	}
	if !found {
		return t, errTableNotFound(normName(name))
	}
	return t, nil
}

func (s *Service) getDatabaseTyped(ctx context.Context, req *getDatabaseReq) (*getDatabaseResp, *protocol.AWSError) {
	cat, aerr := s.readCatalog(ctx, req.CatalogId)
	if aerr != nil {
		return nil, aerr
	}
	db, aerr := requireCatalogDatabase(ctx, cat, req.Name)
	if aerr != nil {
		return nil, aerr
	}
	return &getDatabaseResp{Database: &db}, nil
}

func (s *Service) getDatabasesTyped(ctx context.Context, req *getDatabasesReq) (*getDatabasesResp, *protocol.AWSError) {
	cat, aerr := s.readCatalog(ctx, req.CatalogId)
	if aerr != nil {
		return nil, aerr
	}
	dbs, err := cat.ListDatabases(ctx)
	if err != nil {
		return nil, errInternal(err)
	}
	page, aerr := paginate(dbs, req.MaxResults, req.NextToken, catalogPageSize)
	if aerr != nil {
		return nil, aerr
	}
	return &getDatabasesResp{DatabaseList: page.Items, NextToken: page.NextToken}, nil
}

func (s *Service) getTableTyped(ctx context.Context, req *getTableReq) (*getTableResp, *protocol.AWSError) {
	cat, aerr := s.readCatalog(ctx, req.CatalogId)
	if aerr != nil {
		return nil, aerr
	}
	t, aerr := requireCatalogTable(ctx, cat, req.DatabaseName, req.Name)
	if aerr != nil {
		return nil, aerr
	}
	return &getTableResp{Table: &t}, nil
}

func (s *Service) getTablesTyped(ctx context.Context, req *getTablesReq) (*getTablesResp, *protocol.AWSError) {
	cat, aerr := s.readCatalog(ctx, req.CatalogId)
	if aerr != nil {
		return nil, aerr
	}
	if _, aerr := requireCatalogDatabase(ctx, cat, req.DatabaseName); aerr != nil {
		return nil, aerr
	}
	// Expression is "a regular expression pattern. If present, only those
	// tables whose names match the pattern are returned." It must match the
	// whole name.
	var re *regexp.Regexp
	if req.Expression != "" {
		var err error
		if re, err = regexp.Compile("^(?:" + req.Expression + ")$"); err != nil {
			return nil, errInvalidInput("Invalid Expression %q: %v", req.Expression, err)
		}
	}
	all, err := cat.ListTables(ctx, req.DatabaseName)
	if err != nil {
		return nil, errInternal(err)
	}
	tables := make([]*Table, 0, len(all))
	for i := range all {
		if re == nil || re.MatchString(all[i].Name) {
			tables = append(tables, &all[i])
		}
	}
	page, aerr := paginate(tables, req.MaxResults, req.NextToken, catalogPageSize)
	if aerr != nil {
		return nil, aerr
	}
	return &getTablesResp{TableList: page.Items, NextToken: page.NextToken}, nil
}
