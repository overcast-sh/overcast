package athena

import (
	"context"
	"errors"
	"maps"
	"regexp"

	"github.com/overcast-sh/overcast/internal/protocol"
	"github.com/overcast-sh/overcast/internal/services/glue"
)

// InitGlueCatalog wires the Glue Data Catalog that AwsDataCatalog — and any
// GLUE catalog registered for this account — reads its databases and tables
// from, and that Athena's DDL writes to.
func (s *Service) InitGlueCatalog(c glue.Catalog, w glue.CatalogWriter) {
	s.catalog, s.catalogWriter = c, w
}

var errCatalogNotWired = errors.New("athena: glue catalog not wired")

type catalogDatabaseReq struct {
	CatalogName  string `json:"CatalogName"`
	DatabaseName string `json:"DatabaseName"`
	WorkGroup    string `json:"WorkGroup"`
}

type getDatabaseResp struct {
	Database Database `json:"Database"`
}

type listDatabasesReq struct {
	CatalogName string `json:"CatalogName"`
	MaxResults  int32  `json:"MaxResults"`
	NextToken   string `json:"NextToken"`
	WorkGroup   string `json:"WorkGroup"`
}

type listDatabasesResp struct {
	DatabaseList []Database `json:"DatabaseList"`
	NextToken    string     `json:"NextToken,omitempty"`
}

type getTableMetadataReq struct {
	CatalogName  string `json:"CatalogName"`
	DatabaseName string `json:"DatabaseName"`
	TableName    string `json:"TableName"`
	WorkGroup    string `json:"WorkGroup"`
}

type getTableMetadataResp struct {
	TableMetadata TableMetadata `json:"TableMetadata"`
}

type listTableMetadataReq struct {
	CatalogName  string `json:"CatalogName"`
	DatabaseName string `json:"DatabaseName"`
	Expression   string `json:"Expression"`
	MaxResults   int32  `json:"MaxResults"`
	NextToken    string `json:"NextToken"`
	WorkGroup    string `json:"WorkGroup"`
}

type listTableMetadataResp struct {
	TableMetadataList []TableMetadata `json:"TableMetadataList"`
	NextToken         string          `json:"NextToken,omitempty"`
}

// glueCatalog resolves a catalog name to the Glue catalog it reads. Only
// GLUE catalogs for this account are readable: the others are backed by a
// Lambda connector or a Hive metastore Overcast does not run.
func (s *Service) glueCatalog(ctx context.Context, name string) (glue.Catalog, *protocol.AWSError) {
	if name == "" {
		return nil, errRequired("CatalogName")
	}
	c, aerr := s.requireDataCatalog(ctx, name)
	if aerr != nil {
		return nil, aerr
	}
	if c.Type != catalogTypeGlue {
		return nil, errNotEmulated("Reading metadata from a %s data catalog needs its connector, which Overcast does not run.", c.Type)
	}
	if id := c.Parameters["catalog-id"]; id != s.cfg.AccountID {
		return nil, errNotEmulated("Data catalog %s reads account %s's Glue catalog; Overcast emulates only account %s.", c.Name, id, s.cfg.AccountID)
	}
	if s.catalog == nil {
		return nil, errInternal(errCatalogNotWired)
	}
	return s.catalog, nil
}

func errDatabaseNotFound(name string) *protocol.AWSError {
	return athenaError(codeMetadata, "Database %s not found.", name)
}

func errTableNotFound(name string) *protocol.AWSError {
	return athenaError(codeMetadata, "Table %s not found.", name)
}

// requireDatabase loads a database from the catalog, answering the modeled
// MetadataException when it is not there.
func requireDatabase(ctx context.Context, cat glue.Catalog, name string) (glue.Database, *protocol.AWSError) {
	if name == "" {
		return glue.Database{}, errRequired("DatabaseName")
	}
	db, found, err := cat.GetDatabase(ctx, name)
	if err != nil {
		return db, errInternal(err)
	}
	if !found {
		return db, errDatabaseNotFound(name)
	}
	return db, nil
}

func databaseFromGlue(db glue.Database) Database {
	return Database{Name: db.Name, Description: db.Description, Parameters: db.Parameters}
}

func (s *Service) getDatabaseTyped(ctx context.Context, req *catalogDatabaseReq) (*getDatabaseResp, *protocol.AWSError) {
	cat, aerr := s.glueCatalog(ctx, req.CatalogName)
	if aerr != nil {
		return nil, aerr
	}
	db, aerr := requireDatabase(ctx, cat, req.DatabaseName)
	if aerr != nil {
		return nil, aerr
	}
	return &getDatabaseResp{Database: databaseFromGlue(db)}, nil
}

func (s *Service) listDatabasesTyped(ctx context.Context, req *listDatabasesReq) (*listDatabasesResp, *protocol.AWSError) {
	cat, aerr := s.glueCatalog(ctx, req.CatalogName)
	if aerr != nil {
		return nil, aerr
	}
	dbs, err := cat.ListDatabases(ctx)
	if err != nil {
		return nil, errInternal(err)
	}
	out := make([]Database, len(dbs))
	for i, db := range dbs {
		out[i] = databaseFromGlue(db)
	}
	page, aerr := paginate(out, req.MaxResults, req.NextToken, databasesPageSize)
	if aerr != nil {
		return nil, aerr
	}
	return &listDatabasesResp{DatabaseList: page.Items, NextToken: page.NextToken}, nil
}

func (s *Service) getTableMetadataTyped(ctx context.Context, req *getTableMetadataReq) (*getTableMetadataResp, *protocol.AWSError) {
	cat, aerr := s.glueCatalog(ctx, req.CatalogName)
	if aerr != nil {
		return nil, aerr
	}
	if _, aerr := requireDatabase(ctx, cat, req.DatabaseName); aerr != nil {
		return nil, aerr
	}
	if req.TableName == "" {
		return nil, errRequired("TableName")
	}
	t, found, err := cat.GetTable(ctx, req.DatabaseName, req.TableName)
	if err != nil {
		return nil, errInternal(err)
	}
	if !found {
		return nil, errTableNotFound(req.TableName)
	}
	return &getTableMetadataResp{TableMetadata: tableMetadataFromGlue(t)}, nil
}

// listTableMetadataTyped lists a database's tables. Expression is "a regex
// filter that pattern-matches table names"; like Glue's GetTables, it must
// match the whole name.
func (s *Service) listTableMetadataTyped(ctx context.Context, req *listTableMetadataReq) (*listTableMetadataResp, *protocol.AWSError) {
	cat, aerr := s.glueCatalog(ctx, req.CatalogName)
	if aerr != nil {
		return nil, aerr
	}
	if _, aerr := requireDatabase(ctx, cat, req.DatabaseName); aerr != nil {
		return nil, aerr
	}
	var re *regexp.Regexp
	if req.Expression != "" {
		var err error
		if re, err = regexp.Compile("^(?:" + req.Expression + ")$"); err != nil {
			return nil, errInvalidRequest("Invalid Expression %q: %v", req.Expression, err)
		}
	}
	tables, err := cat.ListTables(ctx, req.DatabaseName)
	if err != nil {
		return nil, errInternal(err)
	}
	out := make([]TableMetadata, 0, len(tables))
	for _, t := range tables {
		if re == nil || re.MatchString(t.Name) {
			out = append(out, tableMetadataFromGlue(t))
		}
	}
	page, aerr := paginate(out, req.MaxResults, req.NextToken, tableMetadataPageSize)
	if aerr != nil {
		return nil, aerr
	}
	return &listTableMetadataResp{TableMetadataList: page.Items, NextToken: page.NextToken}, nil
}

// tableMetadataFromGlue is a Glue table as Athena describes it. Athena has
// no storage descriptor member, so it folds the descriptor's location,
// formats and SerDe into Parameters, beside the table's own.
func tableMetadataFromGlue(t glue.Table) TableMetadata {
	md := TableMetadata{
		Name: t.Name, TableType: t.TableType, CreateTime: t.CreateTime, LastAccessTime: t.LastAccessTime,
		Columns: []Column{}, PartitionKeys: columnsFromGlue(t.PartitionKeys),
	}
	params := maps.Clone(t.Parameters)
	if params == nil {
		params = map[string]string{}
	}
	if sd := t.StorageDescriptor; sd != nil {
		md.Columns = columnsFromGlue(sd.Columns)
		setIfPresent(params, "location", sd.Location)
		setIfPresent(params, "inputformat", sd.InputFormat)
		setIfPresent(params, "outputformat", sd.OutputFormat)
		if serde := sd.SerdeInfo; serde != nil {
			setIfPresent(params, "serde.serialization.lib", serde.SerializationLibrary)
			for k, v := range serde.Parameters {
				params["serde.param."+k] = v
			}
		}
	}
	if len(params) > 0 {
		md.Parameters = params
	}
	return md
}

func setIfPresent(m map[string]string, k, v string) {
	if v != "" {
		m[k] = v
	}
}

func columnsFromGlue(cols []glue.Column) []Column {
	out := make([]Column, len(cols))
	for i, c := range cols {
		out[i] = Column{Name: c.Name, Type: c.Type, Comment: c.Comment}
	}
	return out
}
