package athena

import (
	"context"
	"errors"
	"strings"

	"github.com/overcast-sh/overcast/internal/protocol"
)

// statement_router.go — which runner owns a statement (Decision 2 of
// docs/plans/athena-s3tables-iceberg.md): Overcast runs Athena's Hive DDL
// against the Glue Data Catalog itself, and everything else goes to the
// engine — Trino when it can run, the inert engine when it cannot.

// runnerFor picks the runner for qe's statement.
func (s *Service) runnerFor(ctx context.Context, qe QueryExecution) queryRunner {
	database := orDefault(qe.QueryExecutionContext.Database, defaultDatabase)
	stmt, err := parseDDL(qe.Query)
	if err != nil {
		return failedRunner{syntaxFailure(err)}
	}
	if stmt != nil {
		env := ddlEnv{catalog: s.catalog, writer: s.catalogWriter, list: s.listObjects, database: database}
		if engineSQL, ok := s.icebergDrop(ctx, stmt, env); ok {
			return s.trinoRunnerFor(ctx, qe, engineSQL, database)
		}
		return ddlRunner{stmt: stmt, env: env}
	}
	if s.engine == nil || !s.engine.available() {
		return inertRunner{}
	}
	sql, err := rewriteForEngine(qe.Query, engineRewrite{
		database:       database,
		tablesLocation: tablesLocation(qe),
		prepared:       func(name string) (string, bool) { return s.preparedQuery(ctx, qe.WorkGroup, name) },
		parameters:     qe.ExecutionParameters,
	})
	if err != nil {
		return failedRunner{syntaxFailure(err)}
	}
	return s.trinoRunnerFor(ctx, qe, sql, database)
}

// icebergDrop routes DROP TABLE of an Iceberg table to the engine, which
// removes its data as Athena does; the catalog alone would only forget it.
func (s *Service) icebergDrop(ctx context.Context, stmt ddlStatement, env ddlEnv) (string, bool) {
	drop, ok := stmt.(*dropTableStmt)
	if !ok || s.engine == nil || !s.engine.available() {
		return "", false
	}
	ref := env.resolve(drop.Table)
	t, found, err := env.catalog.GetTable(ctx, ref.Database, ref.Table)
	if err != nil || !found || !isIcebergProperties(t.Parameters) {
		return "", false
	}
	return "DROP TABLE " + qualified(icebergCatalog, ref, ref.Database), true
}

func (s *Service) trinoRunnerFor(ctx context.Context, qe QueryExecution, sql, database string) trinoRunner {
	return trinoRunner{
		engine: s.engine, sql: sql,
		session: trinoSession{Catalog: hiveCatalog, Schema: database},
		header:  qe.StatementType == statementDML,
		cutoff:  s.bytesScannedCutoff(ctx, qe.WorkGroup),
		clk:     s.clk,
	}
}

// tablesLocation is where a CTAS that names no location puts its table, as
// Athena does: "tables/<id>/" under the query's result location.
func tablesLocation(qe QueryExecution) string {
	out := qe.ResultConfiguration.OutputLocation
	if out == "" {
		return ""
	}
	return out[:strings.LastIndexByte(out, '/')+1] + "tables/" + qe.QueryExecutionId + "/"
}

// preparedQuery looks up a prepared statement's query in a workgroup.
func (s *Service) preparedQuery(ctx context.Context, workGroup, name string) (string, bool) {
	ps, err := s.store.getPreparedStatement(ctx, workGroup, name)
	if err != nil || ps == nil {
		return "", false
	}
	return ps.QueryStatement, true
}

// bytesScannedCutoff is the workgroup's BytesScannedCutoffPerQuery, or 0.
func (s *Service) bytesScannedCutoff(ctx context.Context, workGroup string) int64 {
	wg, err := s.store.getWorkGroup(ctx, workGroup)
	if err != nil || wg == nil || wg.Configuration == nil || wg.Configuration.BytesScannedCutoffPerQuery == nil {
		return 0
	}
	return *wg.Configuration.BytesScannedCutoffPerQuery
}

func orDefault(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return strings.ToLower(v)
}

func syntaxFailure(err error) *queryFailure {
	var syntax *ddlSyntaxError
	if errors.As(err, &syntax) {
		return failure(errorCategoryUser, errorTypeSyntax, "FAILED: ParseException "+syntax.msg)
	}
	return failure(errorCategorySystem, errorTypeInternal, err.Error())
}

// ddlRunner runs a DDL statement against the catalog, before Submit returns.
type ddlRunner struct {
	stmt ddlStatement
	env  ddlEnv
}

func (r ddlRunner) run(ctx context.Context, _ func()) (*queryResult, *queryFailure) {
	if r.env.catalog == nil || r.env.writer == nil {
		return nil, catalogFailure(protocol.Wrap(protocol.ErrInternalError, errCatalogNotWired))
	}
	return r.stmt.run(ctx, r.env)
}

func (ddlRunner) background() bool { return false }

// inertRunner runs nothing: the statement succeeds at once, having scanned
// nothing and returned no rows.
type inertRunner struct{}

func (inertRunner) run(context.Context, func()) (*queryResult, *queryFailure) { return nil, nil }

func (inertRunner) background() bool { return false }

// failedRunner is a statement that fails before it runs.
type failedRunner struct{ fail *queryFailure }

func (r failedRunner) run(context.Context, func()) (*queryResult, *queryFailure) { return nil, r.fail }

func (failedRunner) background() bool { return false }
