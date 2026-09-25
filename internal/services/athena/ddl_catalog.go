package athena

import (
	"context"
	"maps"
	"strings"

	"github.com/overcast-sh/overcast/internal/events"
	"github.com/overcast-sh/overcast/internal/protocol"
	"github.com/overcast-sh/overcast/internal/services/glue"
)

// ddl_catalog.go — what each DDL statement does to the Glue Data Catalog.
// These run without the engine, as Athena's own DDL does, so they work with
// the inert engine too.

// ddlStatement is a statement Athena runs against the catalog itself.
type ddlStatement interface {
	run(ctx context.Context, env ddlEnv) (*queryResult, *queryFailure)
}

// ddlEnv is what a DDL statement runs against.
type ddlEnv struct {
	catalog glue.Catalog
	writer  glue.CatalogWriter
	list    events.S3ListObjectsFunc
	// database is the query's QueryExecutionContext.Database.
	database string
}

// defaultDatabase is the database a query that names none runs in.
const defaultDatabase = "default"

func (env ddlEnv) resolve(t tableRef) tableRef {
	if t.Database == "" {
		t.Database = env.database
	}
	return t
}

// requireTable loads a table the statement names.
func (env ddlEnv) requireTable(ctx context.Context, ref tableRef) (glue.Table, *queryFailure) {
	t, found, err := env.catalog.GetTable(ctx, ref.Database, ref.Table)
	if err != nil {
		return t, catalogFailure(protocol.Wrap(protocol.ErrInternalError, err))
	}
	if !found {
		return t, failure(errorCategoryUser, errorTypeNotFound,
			"FAILED: SemanticException [Error 10001]: Table not found "+ref.Database+"."+ref.Table)
	}
	return t, nil
}

// catalogFailure is a failed Glue call as the query's failure.
func catalogFailure(aerr *protocol.AWSError) *queryFailure {
	category, errorType := errorCategoryUser, errorTypeDDLFailed
	switch {
	case aerr.HTTPStatus >= 500:
		category, errorType = errorCategorySystem, errorTypeMetastore
	case aerr.Code == "EntityNotFoundException":
		errorType = errorTypeNotFound
	}
	return failure(category, errorType, "FAILED: "+aerr.Code+": "+aerr.Message)
}

// emptyResult is a DDL statement's result: no columns and no rows.
func emptyResult() *queryResult { return &queryResult{Columns: []ColumnInfo{}} }

func (s *createDatabaseStmt) run(ctx context.Context, env ddlEnv) (*queryResult, *queryFailure) {
	aerr := env.writer.CreateDatabase(ctx, glue.DatabaseInput{
		Name: s.Name, Description: s.Comment, LocationUri: s.Location, Parameters: s.Properties,
	})
	if aerr != nil && !(s.IfNotExists && aerr.Code == "AlreadyExistsException") {
		return nil, catalogFailure(aerr)
	}
	return emptyResult(), nil
}

func (s *dropDatabaseStmt) run(ctx context.Context, env ddlEnv) (*queryResult, *queryFailure) {
	if _, found, err := env.catalog.GetDatabase(ctx, s.Name); err != nil || !found {
		if err == nil && s.IfExists {
			return emptyResult(), nil
		}
		return nil, failure(errorCategoryUser, errorTypeNotFound, "FAILED: SemanticException [Error 10072]: Database does not exist: "+s.Name)
	}
	if !s.Cascade {
		tables, err := env.catalog.ListTables(ctx, s.Name)
		if err != nil {
			return nil, catalogFailure(protocol.Wrap(protocol.ErrInternalError, err))
		}
		if len(tables) > 0 {
			return nil, failure(errorCategoryUser, errorTypeDDLFailed,
				"FAILED: InvalidOperationException(message:Database "+s.Name+" is not empty. One or more tables exist.)")
		}
	}
	if aerr := env.writer.DeleteDatabase(ctx, s.Name); aerr != nil {
		return nil, catalogFailure(aerr)
	}
	return emptyResult(), nil
}

func (s *createTableStmt) run(ctx context.Context, env ddlEnv) (*queryResult, *queryFailure) {
	if isIcebergProperties(s.Properties) {
		return nil, failure(errorCategoryUser, errorTypeNotSupported,
			"CREATE EXTERNAL TABLE is not supported for Iceberg tables. Use CREATE TABLE instead.")
	}
	sd, known := s.storageDescriptor()
	if !known {
		return nil, failure(errorCategoryUser, errorTypeSyntax, "FAILED: SemanticException Unrecognized file format in STORED AS clause: "+s.StoredAs.Format)
	}
	partitions, err := s.partitionColumns()
	if err != nil {
		return nil, failure(errorCategoryUser, errorTypeSyntax, "FAILED: ParseException "+err.Error())
	}
	ref := env.resolve(s.Table)
	params := maps.Clone(s.Properties)
	if params == nil {
		params = map[string]string{}
	}
	params["EXTERNAL"] = "TRUE"
	aerr := env.writer.CreateTable(ctx, ref.Database, glue.TableInput{
		Name: ref.Table, Description: s.Comment, TableType: "EXTERNAL_TABLE",
		StorageDescriptor: sd, PartitionKeys: glueColumns(partitions), Parameters: params,
	})
	if aerr != nil && !(s.IfNotExists && aerr.Code == "AlreadyExistsException") {
		return nil, catalogFailure(aerr)
	}
	return emptyResult(), nil
}

// isIcebergProperties reports whether a table's properties make it Iceberg.
func isIcebergProperties(props map[string]string) bool {
	for k, v := range props {
		if strings.EqualFold(k, "table_type") && strings.EqualFold(v, "ICEBERG") {
			return true
		}
	}
	return false
}

func (s *dropTableStmt) run(ctx context.Context, env ddlEnv) (*queryResult, *queryFailure) {
	ref := env.resolve(s.Table)
	if aerr := env.writer.DeleteTable(ctx, ref.Database, ref.Table); aerr != nil {
		if s.IfExists && aerr.Code == "EntityNotFoundException" {
			return emptyResult(), nil
		}
		return nil, catalogFailure(aerr)
	}
	return emptyResult(), nil
}
