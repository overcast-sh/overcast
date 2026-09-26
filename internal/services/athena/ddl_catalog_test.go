package athena

import (
	"context"
	"io"
	"sort"
	"strings"
	"sync"
	"testing"

	"go.uber.org/zap"

	"github.com/overcast-sh/overcast/internal/clock"
	"github.com/overcast-sh/overcast/internal/config"
	"github.com/overcast-sh/overcast/internal/events"
	"github.com/overcast-sh/overcast/internal/protocol"
	"github.com/overcast-sh/overcast/internal/services/glue"
	"github.com/overcast-sh/overcast/internal/state"
)

// fakeS3 is the in-process S3 accessor: puts land in objects, and listing
// pages through them two keys at a time, so a listing that needs a
// continuation token is exercised.
type fakeS3 struct {
	mu      sync.Mutex
	objects map[string]string // "bucket/key" → body
}

func (f *fakeS3) put(_ context.Context, bucket, key string, body io.Reader, _ events.S3PutObjectOptions) (events.S3PutObjectResult, *protocol.AWSError) {
	if bucket == "missing" {
		return events.S3PutObjectResult{}, &protocol.AWSError{Code: "NoSuchBucket", Message: "The specified bucket does not exist"}
	}
	b, err := io.ReadAll(body)
	if err != nil { // the writer gave up: nothing is stored
		return events.S3PutObjectResult{}, protocol.Wrap(protocol.ErrInternalError, err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.objects[bucket+"/"+key] = string(b)
	return events.S3PutObjectResult{}, nil
}

func (f *fakeS3) list(_ context.Context, bucket, prefix, token string, _ int) (events.S3ObjectListPage, *protocol.AWSError) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var keys []string
	for k := range f.objects {
		if rest, ok := strings.CutPrefix(k, bucket+"/"); ok && strings.HasPrefix(rest, prefix) && rest > token {
			keys = append(keys, rest)
		}
	}
	sort.Strings(keys)
	page := events.S3ObjectListPage{}
	for i, k := range keys {
		if i == 2 {
			page.NextContinuationToken = keys[i-1]
			break
		}
		page.Objects = append(page.Objects, events.S3ObjectSummary{Key: k})
	}
	return page, nil
}

// newCatalogService is an inert-engine Athena wired to a real Glue catalog
// and a fake S3, as router.go wires them.
func newCatalogService(t *testing.T) (*Service, *fakeS3) {
	t.Helper()
	s, _ := newTestService(t)
	g := glue.New(&config.Config{Region: "us-east-1", AccountID: "123456789012"}, state.NewMemoryStore(), zap.NewNop(), clock.New())
	s3 := &fakeS3{objects: map[string]string{}}
	s.InitGlueCatalog(g.Catalogs(), g.CatalogWriter())
	s.InitS3Access(s3.put, s3.list)
	return s, s3
}

// run starts a statement and returns its finished execution.
func run(t *testing.T, s *Service, query string) QueryExecution {
	t.Helper()
	out, aerr := s.startQueryExecutionTyped(context.Background(), startReq(query))
	mustOK(t, "StartQueryExecution "+query, aerr)
	return queryState(t, s, out.QueryExecutionId)
}

// mustRun runs a statement that has to succeed and returns its rows.
func mustRun(t *testing.T, s *Service, query string) [][]string {
	t.Helper()
	qe := run(t, s, query)
	if qe.Status.State != stateSucceeded {
		t.Fatalf("%s: %s: %s", query, qe.Status.State, qe.Status.StateChangeReason)
	}
	res, aerr := s.getQueryResultsTyped(context.Background(), &getQueryResultsReq{QueryExecutionId: qe.QueryExecutionId})
	mustOK(t, "GetQueryResults", aerr)
	rows := make([][]string, len(res.ResultSet.Rows))
	for i, r := range res.ResultSet.Rows {
		for _, d := range r.Data {
			v := ""
			if d.VarCharValue != nil {
				v = *d.VarCharValue
			}
			rows[i] = append(rows[i], v)
		}
	}
	return rows
}

func TestDDL_databasesAndTablesReachGlue(t *testing.T) {
	// Given: Athena with the inert engine and a Glue catalog
	ctx := context.Background()
	s, _ := newCatalogService(t)

	// When: a database and a table are created through DDL
	mustRun(t, s, "CREATE DATABASE sales COMMENT 'the sales'")
	mustRun(t, s, `CREATE EXTERNAL TABLE sales.orders (id bigint, item string COMMENT 'what')
		PARTITIONED BY (dt string) STORED AS PARQUET LOCATION 's3://data/orders/'
		TBLPROPERTIES ('classification'='parquet')`)

	// Then: Glue holds them as Athena would have written them
	table, found, err := s.catalog.GetTable(ctx, "sales", "orders")
	if err != nil || !found {
		t.Fatalf("GetTable: %v, %v", found, err)
	}
	sd := table.StorageDescriptor
	if table.TableType != "EXTERNAL_TABLE" || table.Parameters["EXTERNAL"] != "TRUE" || table.Parameters["classification"] != "parquet" ||
		sd.Location != "s3://data/orders/" || sd.SerdeInfo.SerializationLibrary != hiveFormats["parquet"].SerDe ||
		len(sd.Columns) != 2 || sd.Columns[1].Comment != "what" || len(table.PartitionKeys) != 1 {
		t.Fatalf("table = %+v / %+v", table, sd)
	}

	// And: SHOW and DESCRIBE read them back, with no header row
	if got := mustRun(t, s, "SHOW DATABASES LIKE 'sal*'"); len(got) != 1 || got[0][0] != "sales" {
		t.Fatalf("SHOW DATABASES = %v", got)
	}
	if got := mustRun(t, s, "SHOW TABLES IN sales"); len(got) != 1 || got[0][0] != "orders" {
		t.Fatalf("SHOW TABLES = %v", got)
	}
	for pattern, n := range map[string]int{"'ord*'": 1, "'.*ers'": 1, "'o.d.rs|x'": 1, "'.rders'": 1, "'rders'": 0, "'('": 0} {
		if got := mustRun(t, s, "SHOW TABLES IN sales "+pattern); len(got) != n {
			t.Errorf("SHOW TABLES %s = %v, want %d", pattern, got, n)
		}
	}
	if got := mustRun(t, s, "SHOW COLUMNS IN sales.orders"); len(got) != 3 || got[2][0] != "dt" {
		t.Fatalf("SHOW COLUMNS = %v", got)
	}
	if got := mustRun(t, s, "SHOW TBLPROPERTIES sales.orders('classification')"); len(got) != 1 || got[0][1] != "parquet" {
		t.Fatalf("SHOW TBLPROPERTIES = %v", got)
	}
	if got := mustRun(t, s, "DESCRIBE sales.orders"); len(got) != 8 || strings.TrimSpace(got[4][0]) != "# Partition Information" {
		t.Fatalf("DESCRIBE = %v", got)
	}

	// And: DROP DATABASE refuses a database with tables unless CASCADE
	if qe := run(t, s, "DROP DATABASE sales"); qe.Status.State != stateFailed || qe.Status.AthenaError.ErrorCategory != errorCategoryUser {
		t.Fatalf("DROP DATABASE of a non-empty database = %+v", qe.Status)
	}
	mustRun(t, s, "DROP TABLE sales.orders")
	mustRun(t, s, "DROP TABLE IF EXISTS sales.orders")
	mustRun(t, s, "DROP DATABASE sales")
	if _, found, _ := s.catalog.GetDatabase(ctx, "sales"); found {
		t.Fatal("database survived DROP DATABASE")
	}
}

func TestDDL_failuresCarryAthenaErrors(t *testing.T) {
	s, _ := newCatalogService(t)
	cases := map[string]int32{
		"CREATE EXTERNAL TABLE nowhere.t (a int) LOCATION 's3://b/'":             errorTypeNotFound,
		"CREATE EXTERNAL TABLE t (a int) STORED AS NOTAFORMAT":                   errorTypeSyntax,
		"CREATE EXTERNAL TABLE t (a int) TBLPROPERTIES ('table_type'='ICEBERG')": errorTypeNotSupported,
		"CREATE EXTERNAL TABLE t (a int":                                         errorTypeSyntax,
		"SHOW PARTITIONS default.nothing":                                        errorTypeNotFound,
		"MSCK REPAIR TABLE nothing":                                              errorTypeNotFound,
	}
	for query, errorType := range cases {
		st := run(t, s, query).Status
		if st.State != stateFailed || st.AthenaError == nil || st.AthenaError.ErrorType != errorType || st.StateChangeReason == "" {
			t.Errorf("%s: status = %+v / %+v, want FAILED with error type %d", query, st, st.AthenaError, errorType)
		}
	}
}

func TestDDL_partitionsAndRepair(t *testing.T) {
	// Given: a partitioned table, and data under its location for three
	// partitions, one of them escaped and one beside a stray object
	ctx := context.Background()
	s, s3 := newCatalogService(t)
	mustRun(t, s, "CREATE DATABASE logs")
	mustRun(t, s, `CREATE EXTERNAL TABLE logs.hits (path string) PARTITIONED BY (year string, day string)
		ROW FORMAT DELIMITED FIELDS TERMINATED BY ',' LOCATION 's3://data/hits'`)
	for _, key := range []string{"hits/year=2026/day=01/a.csv", "hits/year=2026/day=02/a.csv", "hits/year=2026/day=02/b.csv",
		"hits/year=2025/day=a%2Fb/x.csv", "hits/year=2024/day=10%3A00 am/x.csv", "hits/_SUCCESS", "hits/year=2026/stray.csv"} {
		s3.objects["data/"+key] = "x"
	}

	// When: the table is repaired, then repaired again
	repaired := mustRun(t, s, "MSCK REPAIR TABLE logs.hits")
	again := mustRun(t, s, "MSCK REPAIR TABLE logs.hits")

	// Then: each partition directory became a partition once, at its
	// location and stored as the table is
	if len(repaired) != 5 || !strings.HasPrefix(repaired[0][0], "Partitions not in metastore:\thits:year=2024/day=10%3A00 am\t") ||
		repaired[1][0] != "Repair: Added partition to metastore hits:year=2024/day=10%3A00 am" || len(again) != 0 {
		t.Fatalf("repair reported %v, then %v", repaired, again)
	}
	parts, _ := s.catalog.ListPartitions(ctx, "logs", "hits")
	if len(parts) != 4 {
		t.Fatalf("partitions = %+v", parts)
	}
	byValues := map[string]glue.Partition{}
	for _, p := range parts {
		byValues[strings.Join(p.Values, "/")] = p
	}
	if p := byValues["2025/a/b"]; p.StorageDescriptor == nil || p.StorageDescriptor.Location != "s3://data/hits/year=2025/day=a%2Fb/" ||
		p.StorageDescriptor.SerdeInfo.Parameters["field.delim"] != "," {
		t.Fatalf("escaped partition = %+v", p)
	}
	if p := byValues["2024/10:00 am"]; p.StorageDescriptor == nil || p.StorageDescriptor.Location != "s3://data/hits/year=2024/day=10%3A00 am/" {
		t.Fatalf("partition with a colon and a space = %+v", p)
	}

	// When: partitions are added — one at a location of its own — and dropped
	mustRun(t, s, "ALTER TABLE logs.hits ADD PARTITION (day='03', year='2026') PARTITION (year='2027', day='01') LOCATION 's3://elsewhere/'")
	if qe := run(t, s, "ALTER TABLE logs.hits ADD PARTITION (year='2027', day='01')"); qe.Status.State != stateFailed {
		t.Fatalf("adding an existing partition = %+v", qe.Status)
	}
	mustRun(t, s, "ALTER TABLE logs.hits ADD IF NOT EXISTS PARTITION (year='2027', day='01')")
	mustRun(t, s, "ALTER TABLE logs.hits DROP PARTITION (year='2026', day='01')")

	// Then: SHOW PARTITIONS lists what is left, sorted, as key=value paths
	got := mustRun(t, s, "SHOW PARTITIONS logs.hits")
	want := []string{"year=2024/day=10%3A00 am", "year=2025/day=a%2Fb", "year=2026/day=02", "year=2026/day=03", "year=2027/day=01"}
	if len(got) != len(want) {
		t.Fatalf("SHOW PARTITIONS = %v", got)
	}
	for i := range want {
		if got[i][0] != want[i] {
			t.Fatalf("SHOW PARTITIONS = %v, want %v", got, want)
		}
	}
	if p := byValuesAfter(t, s, "2026/03"); p.StorageDescriptor.Location != "s3://data/hits/year=2026/day=03/" {
		t.Fatalf("default partition location = %q", p.StorageDescriptor.Location)
	}

	// When: a partition with no LOCATION is added to a table with none
	mustRun(t, s, "CREATE EXTERNAL TABLE logs.nowhere (a int) PARTITIONED BY (day string)")
	qe := run(t, s, "ALTER TABLE logs.nowhere ADD PARTITION (day='01')")

	// Then: it fails, as there is nowhere to put it
	if qe.Status.State != stateFailed {
		t.Fatalf("adding a partition with no location = %+v", qe.Status)
	}
}

func TestHivePathEscaping(t *testing.T) {
	for value, want := range map[string]string{"10:00 am": "10%3A00 am", "a/b": "a%2Fb", "x=y#z": "x%3Dy%23z", "tab\t{}": "tab%09%7B}", "plain-_.": "plain-_."} {
		// When: a value is escaped as a directory name, and read back
		got := hiveEscapePath(value)
		// Then: it is escaped as Hive escapes it, and reads back as itself
		if got != want || hiveUnescapePath(got) != value {
			t.Errorf("hiveEscapePath(%q) = %q (back: %q), want %q", value, got, hiveUnescapePath(got), want)
		}
	}
}

func byValuesAfter(t *testing.T, s *Service, values string) glue.Partition {
	t.Helper()
	parts, _ := s.catalog.ListPartitions(context.Background(), "logs", "hits")
	for _, p := range parts {
		if strings.Join(p.Values, "/") == values {
			return p
		}
	}
	t.Fatalf("no partition %s", values)
	return glue.Partition{}
}

func TestDDL_resultsAreWrittenAsText(t *testing.T) {
	// Given: a DDL statement's result location
	s, s3 := newCatalogService(t)
	mustRun(t, s, "CREATE DATABASE a")
	mustRun(t, s, "CREATE DATABASE b")

	// When: a UTILITY statement runs
	qe := run(t, s, "SHOW DATABASES")

	// Then: its result is a .txt of lines, with the column metadata beside it
	key := strings.TrimPrefix(qe.ResultConfiguration.OutputLocation, "s3://")
	if !strings.HasSuffix(key, ".txt") || s3.objects[key] != "a\nb\n" {
		t.Fatalf("result %s = %q", key, s3.objects[key])
	}
	if meta := s3.objects[key+".metadata"]; !strings.Contains(meta, `"Name":"database_name"`) {
		t.Fatalf("metadata = %q", meta)
	}
}

func TestPrepareAndDeallocate(t *testing.T) {
	// Given: an inert service
	ctx := context.Background()
	s, _ := newCatalogService(t)
	stored := func(name string) string {
		ps, _ := s.store.getPreparedStatement(ctx, "primary", name)
		if ps == nil {
			return ""
		}
		return ps.QueryStatement
	}

	// When: a statement is prepared, then prepared again under its name
	mustRun(t, s, "PREPARE My@Q:1 FROM SELECT * FROM t WHERE id = ?;")
	twice := run(t, s, "PREPARE My@Q:1 FROM SELECT 2")

	// Then: the workgroup keeps the first, with its name's case, and the
	// second fails as the name is taken
	if got := stored("My@Q:1"); got != "SELECT * FROM t WHERE id = ?" || twice.Status.State != stateFailed {
		t.Fatalf("prepared statement = %q, second PREPARE = %+v", got, twice.Status)
	}

	// When: it is deallocated, twice
	mustRun(t, s, "DEALLOCATE PREPARE My@Q:1")
	again := run(t, s, "DEALLOCATE PREPARE My@Q:1")

	// Then: it is gone, and the second fails as there is nothing to drop
	if stored("My@Q:1") != "" || again.Status.State != stateFailed {
		t.Fatalf("after DEALLOCATE: %q, second = %+v", stored("My@Q:1"), again.Status)
	}
}
