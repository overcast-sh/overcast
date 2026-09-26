package athena

import (
	"context"
	"strings"
	"testing"

	"go.uber.org/zap"

	"github.com/overcast-sh/overcast/internal/clock"
	"github.com/overcast-sh/overcast/internal/config"
	"github.com/overcast-sh/overcast/internal/events"
	"github.com/overcast-sh/overcast/internal/protocol"
	"github.com/overcast-sh/overcast/internal/services/glue"
	"github.com/overcast-sh/overcast/internal/state"
)

// oneTableBucket is S3 Tables with one bucket, "sales", holding namespace
// "ns" and its table "t".
type oneTableBucket struct{}

var _ events.S3TablesCatalog = oneTableBucket{}

func (oneTableBucket) ListTableBuckets(context.Context) ([]events.S3TableBucket, error) {
	return []events.S3TableBucket{{Name: "sales", ARN: "arn:aws:s3tables:us-east-1:123456789012:bucket/sales"}}, nil
}

func (b oneTableBucket) GetTableBucket(ctx context.Context, name string) (events.S3TableBucket, bool, error) {
	buckets, _ := b.ListTableBuckets(ctx)
	return buckets[0], name == buckets[0].Name, nil
}

func (oneTableBucket) ListNamespaces(context.Context, string) ([]events.S3TablesNamespace, error) {
	return []events.S3TablesNamespace{{Name: "ns"}}, nil
}

func (oneTableBucket) GetNamespace(_ context.Context, _, namespace string) (events.S3TablesNamespace, bool, error) {
	return events.S3TablesNamespace{Name: namespace}, namespace == "ns", nil
}

func (oneTableBucket) ListTables(_ context.Context, _, namespace string) ([]events.S3TablesTable, error) {
	if namespace != "ns" {
		return nil, nil
	}
	return []events.S3TablesTable{{Name: "t", Namespace: "ns", Columns: []events.S3TablesColumn{{Name: "a", Type: "int"}}}}, nil
}

func (b oneTableBucket) GetTable(ctx context.Context, bucket, namespace, name string) (events.S3TablesTable, bool, error) {
	tables, _ := b.ListTables(ctx, bucket, namespace)
	for _, t := range tables {
		if t.Name == name {
			return t, true, nil
		}
	}
	return events.S3TablesTable{}, false, nil
}

// newS3TablesService is Athena on a Glue catalog that federates
// oneTableBucket, with an engine.
func newS3TablesService(t *testing.T) *Service {
	t.Helper()
	s, _ := newTestService(t)
	g := glue.New(&config.Config{Region: "us-east-1", AccountID: "123456789012"}, state.NewMemoryStore(), zap.NewNop(), clock.New())
	g.InitS3Tables(oneTableBucket{})
	s.InitGlueCatalog(g.Catalogs(), g.CatalogWriter())
	s.engine = readyEngine(t, "http://engine.test:1")
	return s
}

func inCatalog(catalog, query string) QueryExecution {
	return QueryExecution{QueryExecutionId: "id", Query: query, StatementType: statementType(query),
		QueryExecutionContext: QueryExecutionContext{Catalog: catalog, Database: "ns"}}
}

func TestRunnerFor_runsATableBucketsStatementsOnItsCatalog(t *testing.T) {
	// Given: Athena with an engine, and a table bucket "sales"
	s := newS3TablesService(t)
	ctx := context.Background()
	const bucket = "s3tablescatalog/sales"

	// Then: a query in the bucket's catalog runs there, on the engine, and a
	// DDL statement that changes the bucket runs as Trino's DDL on it
	for query, want := range map[string]string{
		"SELECT * FROM t":                    "SELECT * FROM t",
		"INSERT INTO t VALUES (1)":           "INSERT INTO t VALUES (1)",
		"CREATE DATABASE `ns2`":              `CREATE SCHEMA "s3tablescatalog/sales"."ns2"`,
		"CREATE SCHEMA IF NOT EXISTS ns2":    `CREATE SCHEMA IF NOT EXISTS "s3tablescatalog/sales"."ns2"`,
		"DROP DATABASE IF EXISTS ns CASCADE": `DROP SCHEMA IF EXISTS "s3tablescatalog/sales"."ns" CASCADE`,
		"DROP TABLE IF EXISTS t":             `DROP TABLE IF EXISTS "s3tablescatalog/sales"."ns"."t"`,
	} {
		r, ok := s.runnerFor(ctx, inCatalog("S3TablesCatalog/sales", query)).(trinoRunner)
		if !ok || r.sql != want || r.session.Catalog != bucket || r.session.Schema != "ns" {
			t.Errorf("%s: runner = %+v, want %q in %s", query, r, want, bucket)
		}
	}

	// And: one naming the bucket's table from AwsDataCatalog runs there too
	r, ok := s.runnerFor(ctx, inCatalog("AwsDataCatalog", "DROP TABLE `s3tablescatalog/sales`.ns.t")).(trinoRunner)
	if !ok || r.sql != `DROP TABLE "s3tablescatalog/sales"."ns"."t"` || r.session.Catalog != hiveCatalog {
		t.Errorf("qualified DROP TABLE = %+v", r)
	}
}

func TestRunnerFor_readsATableBucketThroughGlue(t *testing.T) {
	// Given: Athena and a table bucket "sales" with table ns.t
	s := newS3TablesService(t)
	ctx := context.Background()

	// When: SHOW TABLES and DESCRIBE run in the bucket's catalog
	for query, want := range map[string]string{"SHOW TABLES": "t", "DESCRIBE t": "a"} {
		r, ok := s.runnerFor(ctx, inCatalog("s3tablescatalog/sales", query)).(ddlRunner)
		if !ok {
			t.Fatalf("%s did not go to the catalog", query)
		}
		res, fail := r.run(ctx, func() {}, nil)

		// Then: they read the bucket, not AwsDataCatalog
		if fail != nil || len(res.Rows) == 0 || !strings.HasPrefix(*res.Rows[0][0], want) {
			t.Errorf("%s = %+v, %+v; want %q first", query, res, fail, want)
		}
	}
}

func TestRunnerFor_refusesWhatATableBucketCannotHold(t *testing.T) {
	// Given: Athena and a table bucket "sales"
	s := newS3TablesService(t)
	ctx := context.Background()

	// Then: Hive's table statements, and a namespace with a location, fail
	for _, query := range []string{
		"CREATE EXTERNAL TABLE x (a int) LOCATION 's3://b/x/'",
		"MSCK REPAIR TABLE t",
		"ALTER TABLE t ADD PARTITION (p = '1')",
		"CREATE DATABASE ns2 LOCATION 's3://b/ns2/'",
	} {
		r, ok := s.runnerFor(ctx, inCatalog("s3tablescatalog/sales", query)).(failedRunner)
		if !ok || r.fail.Error.ErrorType != errorTypeNotSupported || !strings.HasPrefix(r.fail.Reason, "NOT_SUPPORTED: ") {
			t.Errorf("%s: runner = %+v", query, r)
		}
	}

	// And: a bucket that does not exist is not found
	r, ok := s.runnerFor(ctx, inCatalog("s3tablescatalog/nope", "SHOW TABLES")).(failedRunner)
	if !ok || !strings.Contains(r.fail.Reason, "s3tablescatalog/nope was not found") {
		t.Errorf("missing bucket: runner = %+v", r)
	}

	// And: with no engine, a statement that changes the bucket fails rather
	// than succeed having changed nothing
	s.engine = nil
	for _, query := range []string{"CREATE DATABASE ns2", "DROP DATABASE ns", "DROP TABLE t"} {
		r, ok := s.runnerFor(ctx, inCatalog("s3tablescatalog/sales", query)).(failedRunner)
		if !ok || r.fail.Error.ErrorType != errorTypeNotSupported {
			t.Errorf("%s without an engine: runner = %+v", query, r)
		}
	}
}

func TestReadOnlyCatalog_refusesEveryWrite(t *testing.T) {
	// Given: the writer a table bucket's catalog is read with
	w, ctx := readOnlyCatalog{}, context.Background()

	// Then: every write fails, rather than reach a writer that is not there
	_, createParts := w.CreatePartitions(ctx, "d", "t", nil)
	_, deleteParts := w.DeletePartitions(ctx, "d", "t", nil)
	for name, aerr := range map[string]*protocol.AWSError{
		"CreateDatabase": w.CreateDatabase(ctx, glue.DatabaseInput{}), "DeleteDatabase": w.DeleteDatabase(ctx, "d"),
		"CreateTable": w.CreateTable(ctx, "d", glue.TableInput{}), "DeleteTable": w.DeleteTable(ctx, "d", "t"),
		"CreatePartitions": createParts, "DeletePartitions": deleteParts,
	} {
		if aerr == nil {
			t.Errorf("%s succeeded", name)
		}
	}
}
