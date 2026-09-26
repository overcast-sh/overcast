package athena

import (
	"context"
	"strings"

	"github.com/overcast-sh/overcast/internal/protocol"
	"github.com/overcast-sh/overcast/internal/services/glue"
)

// s3tables_statements.go — statements run in a table bucket's catalog,
// "s3tablescatalog/<bucket>": the query's own catalog, or the one a table
// name is qualified with.
//
// The engine queries each bucket as a catalog of that name
// (engine_catalogs.go), so a query or DML statement runs there as written,
// and an Iceberg CREATE TABLE or a CTAS is rewritten onto it (engine_sql.go).
// Athena's own DDL splits three ways. S3 Tables holds only Iceberg tables,
// so a statement that changes one of its namespaces or tables runs on the
// engine, as Trino's DDL, and commits through the REST catalog as any other
// Iceberg client's would. One that only reads runs against Glue's
// s3tablescatalog, as the metadata operations do. Hive's table statements —
// external tables, partitions and MSCK REPAIR — have nothing in S3 Tables to
// act on, and fail.
//
// AWS documents the statements, and that a table's LOCATION is left out:
// https://docs.aws.amazon.com/athena/latest/ug/gdc-register-s3-table-bucket-cat.html

// errS3TablesLocation refuses a location for a table S3 Tables places.
var errS3TablesLocation = &statementError{errorType: errorTypeUser,
	msg: "S3 Tables chooses where a table's data lives: omit LOCATION and location for a table in an s3tablescatalog/<bucket> catalog."}

// asS3TablesCatalog is name, lower case as the engine names it, when
// it is a table bucket's, and "" otherwise.
func asS3TablesCatalog(name string) string {
	if !isS3TablesCatalog(name) {
		return ""
	}
	return strings.ToLower(name)
}

// s3TablesCatalogOf is the table bucket catalog a table named t is in: t's
// own catalog when it names one, else the query's, and "" when that is not
// a table bucket's.
func s3TablesCatalogOf(t tableRef, queryCatalog string) string {
	if t.Catalog != "" {
		return asS3TablesCatalog(t.Catalog)
	}
	return asS3TablesCatalog(queryCatalog)
}

// ddlS3TablesCatalog is the table bucket catalog a DDL statement acts in,
// or "".
func ddlS3TablesCatalog(stmt ddlStatement, queryCatalog string) string {
	var ref tableRef
	if t, ok := stmt.(tableTarget); ok {
		ref = t.target()
	}
	return s3TablesCatalogOf(ref, queryCatalog)
}

// s3TablesDDLRunner runs one of Athena's DDL statements in catalog, a table
// bucket's.
func (s *Service) s3TablesDDLRunner(ctx context.Context, qe QueryExecution, stmt ddlStatement, catalog, database string) queryRunner {
	switch st := stmt.(type) {
	case *createDatabaseStmt:
		if st.Location != "" || st.Comment != "" || len(st.Properties) > 0 {
			return failedRunner{s3TablesUnsupported("An S3 Tables namespace has no location, comment or properties")}
		}
		return s.s3TablesEngineRunner(ctx, qe, "CREATE SCHEMA "+ifClause(st.IfNotExists, "IF NOT EXISTS ")+
			quoteIdent(catalog)+"."+quoteIdent(st.Name), database)
	case *dropDatabaseStmt:
		return s.s3TablesEngineRunner(ctx, qe, "DROP SCHEMA "+ifClause(st.IfExists, "IF EXISTS ")+
			quoteIdent(catalog)+"."+quoteIdent(st.Name)+ifClause(st.Cascade, " CASCADE"), database)
	case *dropTableStmt:
		return s.s3TablesEngineRunner(ctx, qe, "DROP TABLE "+ifClause(st.IfExists, "IF EXISTS ")+qualified(catalog, st.Table, database), database)
	case *showDatabasesStmt, *showTablesStmt, *showColumnsStmt, *showTablePropertiesStmt, *describeStmt:
		return s.s3TablesReadRunner(ctx, stmt, catalog, database)
	}
	return failedRunner{s3TablesUnsupported("S3 Tables holds only Iceberg tables, which this statement does not apply to")}
}

// s3TablesReadRunner runs a statement that only reads the catalog against
// the table bucket's Glue catalog, which it cannot write.
func (s *Service) s3TablesReadRunner(ctx context.Context, stmt ddlStatement, catalog, database string) queryRunner {
	cat, aerr := s.glueCatalog(ctx, catalog)
	if aerr != nil {
		return failedRunner{catalogFailure(aerr)}
	}
	return ddlRunner{stmt: stmt, env: ddlEnv{catalog: cat, writer: readOnlyCatalog{}, database: database}}
}

// s3TablesEngineRunner runs sql, a change to a table bucket, on the engine.
// Only the engine writes S3 Tables, so without one it fails rather than
// succeed having changed nothing.
func (s *Service) s3TablesEngineRunner(ctx context.Context, qe QueryExecution, sql, database string) queryRunner {
	if !s.engineAvailable() {
		return failedRunner{s3TablesUnsupported("Changing a table bucket needs the query engine, which is off,")}
	}
	return s.trinoRunnerFor(ctx, qe, sql, database)
}

// s3TablesUnsupported fails a DDL statement that has no meaning in S3
// Tables.
func s3TablesUnsupported(why string) *queryFailure {
	return failure(errorCategoryUser, errorTypeNotSupported, "NOT_SUPPORTED: "+why+" in an s3tablescatalog/<bucket> catalog.")
}

// ifClause is clause when on is set, for an optional keyword of a statement.
func ifClause(on bool, clause string) string {
	if on {
		return clause
	}
	return ""
}

// readOnlyCatalog is the writer of a catalog the DDL layer only reads: a
// table bucket's, which only the engine writes. s3TablesDDLRunner sends it
// no statement that writes, so each method answers only a routing mistake.
type readOnlyCatalog struct{}

var _ glue.CatalogWriter = readOnlyCatalog{}

var errReadOnlyCatalog = &protocol.AWSError{Code: "InvalidRequestException",
	Message: "A table bucket's catalog is changed through the query engine, not written directly.", HTTPStatus: 400}

func (readOnlyCatalog) CreateDatabase(context.Context, glue.DatabaseInput) *protocol.AWSError {
	return errReadOnlyCatalog
}

func (readOnlyCatalog) DeleteDatabase(context.Context, string) *protocol.AWSError {
	return errReadOnlyCatalog
}

func (readOnlyCatalog) CreateTable(context.Context, string, glue.TableInput) *protocol.AWSError {
	return errReadOnlyCatalog
}

func (readOnlyCatalog) DeleteTable(context.Context, string, string) *protocol.AWSError {
	return errReadOnlyCatalog
}

func (readOnlyCatalog) CreatePartitions(context.Context, string, string, []glue.PartitionInput) ([]glue.PartitionError, *protocol.AWSError) {
	return nil, errReadOnlyCatalog
}

func (readOnlyCatalog) DeletePartitions(context.Context, string, string, [][]string) ([]glue.PartitionError, *protocol.AWSError) {
	return nil, errReadOnlyCatalog
}
