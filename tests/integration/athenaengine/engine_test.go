package athenaengine_test

// engine_test.go — Athena queries on the real Trino engine, through the AWS
// SDK for Go v2. One server, and so one engine, serves every subtest: the
// engine is a gigabyte image with a seconds-long start, paid once per run.

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/athena"
	"github.com/aws/aws-sdk-go-v2/service/athena/types"
	"github.com/aws/aws-sdk-go-v2/service/glue"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/overcast-sh/overcast/internal/config"
	"github.com/overcast-sh/overcast/internal/docker"
	"github.com/overcast-sh/overcast/tests/helpers"
)

const (
	bucket  = "athena-engine-data"
	results = "s3://athena-engine-results/q/"
)

func TestAthenaEngine(t *testing.T) {
	helpers.SkipWithoutDocker(t)
	helpers.PullOrSkip(t, docker.NewClient(helpers.TestDockerSocket(), nil), config.DefaultAthenaEngineImage)
	srv := helpers.NewTestServer(t, helpers.WithAthenaEngine())
	e := newEnv(t, srv)
	e.waitForDocker()
	e.createBucket("athena-engine-results")
	e.createBucket(bucket)
	e.mustQuery("CREATE DATABASE IF NOT EXISTS demo")

	for _, c := range []struct {
		name string
		run  func(*env)
	}{
		{"first query starts the engine", (*env).testColdStart},
		{"CSV table", (*env).testCSVTable},
		{"large result", (*env).testLargeResult},
		{"CTAS writes a Parquet table", (*env).testCTASParquet},
		{"partitions", (*env).testPartitions},
		{"Iceberg INSERT and MERGE", (*env).testIceberg},
		{"failure", (*env).testFailure},
		{"stop", (*env).testStop},
	} {
		t.Run(c.name, func(t *testing.T) { c.run(e.in(t)) })
	}
}

// testColdStart runs the first query, which waits for the engine to start.
func (e *env) testColdStart() {
	t := e.t
	started := time.Now()
	id := e.mustQuery("SELECT 1 AS one, 'a' AS letter")
	st := e.engineStatus()
	t.Logf("first query, including the engine's start: %s (pull %dms, start %dms)", time.Since(started), st.PullMillis, st.StartMillis)
	e.logMemory("after the first query")

	res := e.results(id, 0, "")
	if got := rowsOf(res); len(got) != 2 || got[0][0] != "one" || got[1][0] != "1" || got[1][1] != "a" {
		t.Fatalf("rows = %v, want the header then 1, a", got)
	}
	cols := res.ResultSet.ResultSetMetadata.ColumnInfo
	if len(cols) != 2 || aws.ToString(cols[0].Type) != "integer" || aws.ToString(cols[1].Type) != "varchar" {
		t.Fatalf("columns = %+v", cols)
	}
	if st.State != "ready" || st.ContainerID == "" {
		t.Fatalf("engine status = %+v, want ready", st)
	}
}

func (e *env) testCSVTable() {
	t := e.t
	e.put("people/part-1.csv", "id,name\n1,alice\n2,\"bob, jr\"\n")
	e.put("people/part-2.csv", "id,name\n3,carol\n")
	e.mustQuery(`CREATE EXTERNAL TABLE demo.people (id string, name string)
		ROW FORMAT SERDE 'org.apache.hadoop.hive.serde2.OpenCSVSerde'
		WITH SERDEPROPERTIES ('separatorChar' = ',')
		LOCATION 's3://` + bucket + `/people/'
		TBLPROPERTIES ('skip.header.line.count' = '1')`)

	id := e.mustQuery("SELECT id, name FROM demo.people ORDER BY id")
	page := e.results(id, 2, "")
	if got := rowsOf(page); len(got) != 2 || got[1][1] != "alice" || aws.ToString(page.NextToken) == "" {
		t.Fatalf("first page = %v (next %q), want the header and alice, and more", got, aws.ToString(page.NextToken))
	}
	rest := rowsOf(e.results(id, 0, aws.ToString(page.NextToken)))
	if len(rest) != 2 || rest[0][1] != "bob, jr" || rest[1][1] != "carol" {
		t.Fatalf("second page = %v", rest)
	}

	qe := e.execution(id)
	if qe.Statistics == nil || aws.ToInt64(qe.Statistics.DataScannedInBytes) == 0 {
		t.Fatalf("statistics = %+v, want bytes scanned", qe.Statistics)
	}
	stats := must[*athena.GetQueryRuntimeStatisticsOutput](t, "GetQueryRuntimeStatistics")(e.athena.GetQueryRuntimeStatistics(e.ctx,
		&athena.GetQueryRuntimeStatisticsInput{QueryExecutionId: aws.String(id)}))
	if rows := stats.QueryRuntimeStatistics.Rows; rows == nil || aws.ToInt64(rows.OutputRows) != 3 {
		t.Fatalf("runtime rows = %+v, want 3 output rows", rows)
	}
	csv := e.get(strings.TrimPrefix(aws.ToString(qe.ResultConfiguration.OutputLocation), "s3://athena-engine-results/"), "athena-engine-results")
	if want := "\"id\",\"name\"\n\"1\",\"alice\"\n\"2\",\"bob, jr\"\n\"3\",\"carol\"\n"; csv != want {
		t.Fatalf("result object = %q, want %q", csv, want)
	}
}

// testLargeResult reads a result the engine returns over many pages: every
// row reaches both the result object and the result GetQueryResults pages.
func (e *env) testLargeResult() {
	t := e.t
	const rows = 200_000
	id := e.mustQuery(`SELECT a.n * 1000 + b.n AS n, 'row "' || CAST(a.n * 1000 + b.n AS varchar) || '"' AS label
		FROM UNNEST(sequence(0, 199)) a(n) CROSS JOIN UNNEST(sequence(0, 999)) b(n)`)

	location := aws.ToString(e.execution(id).ResultConfiguration.OutputLocation)
	csv := e.get(strings.TrimPrefix(location, "s3://athena-engine-results/"), "athena-engine-results")
	if lines := strings.Count(csv, "\n"); lines != rows+1 || !strings.HasPrefix(csv, "\"n\",\"label\"\n") ||
		!strings.Contains(csv, "\n\"123456\",\"row \"\"123456\"\"\"\n") {
		t.Fatalf("result object has %d lines and starts %.40q, want the header and %d rows", lines, csv, rows)
	}
	last := e.results(id, 0, strconv.Itoa(rows))
	if got := rowsOf(last); len(got) != 1 || last.NextToken != nil {
		t.Fatalf("last page = %v (next %q), want the one row left", got, aws.ToString(last.NextToken))
	}
	stats := must[*athena.GetQueryRuntimeStatisticsOutput](t, "GetQueryRuntimeStatistics")(e.athena.GetQueryRuntimeStatistics(e.ctx,
		&athena.GetQueryRuntimeStatisticsInput{QueryExecutionId: aws.String(id)}))
	if r := stats.QueryRuntimeStatistics.Rows; r == nil || aws.ToInt64(r.OutputRows) != rows {
		t.Fatalf("runtime rows = %+v, want %d output rows", r, rows)
	}
}

func (e *env) testCTASParquet() {
	t := e.t
	id := e.mustQuery(`CREATE TABLE demo.people_parquet
		WITH (format = 'PARQUET', external_location = 's3://` + bucket + `/people_parquet/')
		AS SELECT CAST(id AS integer) AS id, name FROM demo.people`)
	if got := aws.ToInt64(e.results(id, 0, "").UpdateCount); got != 3 {
		t.Fatalf("CTAS UpdateCount = %d, want 3", got)
	}

	// A table of its own over the files the CTAS wrote: Parquet read from S3.
	e.mustQuery(`CREATE EXTERNAL TABLE demo.people_files (id int, name string)
		STORED AS PARQUET LOCATION 's3://` + bucket + `/people_parquet/'`)
	got := rowsOf(e.results(e.mustQuery("SELECT sum(id), count(*) FROM demo.people_files"), 0, ""))
	if len(got) != 2 || got[1][0] != "6" || got[1][1] != "3" {
		t.Fatalf("rows = %v, want sum 6 over 3 rows", got)
	}
}

func (e *env) testPartitions() {
	t := e.t
	e.put("sales/year=2025/a.csv", "1,10\n2,20\n")
	e.put("sales/year=2026/a.csv", "3,30\n")
	e.mustQuery(`CREATE EXTERNAL TABLE demo.sales (id int, amount int)
		PARTITIONED BY (year string)
		ROW FORMAT DELIMITED FIELDS TERMINATED BY ','
		LOCATION 's3://` + bucket + `/sales/'`)
	repaired := rowsOf(e.results(e.mustQuery("MSCK REPAIR TABLE demo.sales"), 0, ""))
	if len(repaired) != 3 {
		t.Fatalf("MSCK REPAIR TABLE reported %v, want two partitions added", repaired)
	}

	e.put("elsewhere/2027.csv", "4,40\n")
	e.mustQuery("ALTER TABLE demo.sales ADD PARTITION (year = '2027') LOCATION 's3://" + bucket + "/elsewhere/'")
	parts := rowsOf(e.results(e.mustQuery("SHOW PARTITIONS demo.sales"), 0, ""))
	if len(parts) != 3 || parts[0][0] != "year=2025" || parts[2][0] != "year=2027" {
		t.Fatalf("partitions = %v", parts)
	}
	got := rowsOf(e.results(e.mustQuery("SELECT year, sum(amount) FROM demo.sales GROUP BY year ORDER BY year"), 0, ""))
	if len(got) != 4 || got[1][1] != "30" || got[3][1] != "40" {
		t.Fatalf("rows = %v", got)
	}

	e.mustQuery("ALTER TABLE demo.sales DROP PARTITION (year = '2025')")
	got = rowsOf(e.results(e.mustQuery("SELECT count(*) FROM demo.sales"), 0, ""))
	if got[1][0] != "2" {
		t.Fatalf("count after dropping a partition = %v, want 2", got)
	}
}

func (e *env) testIceberg() {
	t := e.t
	e.mustQuery(`CREATE TABLE demo.orders (id int, status string)
		LOCATION 's3://` + bucket + `/orders/'
		TBLPROPERTIES ('table_type' = 'ICEBERG')`)
	inserted := e.mustQuery("INSERT INTO demo.orders VALUES (1, 'new'), (2, 'new')")
	if got := aws.ToInt64(e.results(inserted, 0, "").UpdateCount); got != 2 {
		t.Fatalf("INSERT UpdateCount = %d, want 2", got)
	}
	before := e.metadataLocation("orders")

	e.mustQuery(`MERGE INTO demo.orders t USING (VALUES (2, 'shipped'), (3, 'new')) AS s(id, status)
		ON t.id = s.id
		WHEN MATCHED THEN UPDATE SET status = s.status
		WHEN NOT MATCHED THEN INSERT VALUES (s.id, s.status)`)
	after := e.metadataLocation("orders")
	if before == "" || after == before {
		t.Fatalf("metadata_location %q -> %q, want the MERGE to commit a new one", before, after)
	}
	got := rowsOf(e.results(e.mustQuery("SELECT id, status FROM demo.orders ORDER BY id"), 0, ""))
	if len(got) != 4 || got[2][1] != "shipped" || got[3][0] != "3" {
		t.Fatalf("rows = %v", got)
	}
	e.logMemory("after the Iceberg MERGE")
}

func (e *env) testFailure() {
	t := e.t
	id := e.start("SELECT * FROM demo.no_such_table")
	qe := e.wait(id)
	st := qe.Status
	if st.State != types.QueryExecutionStateFailed || st.AthenaError == nil ||
		aws.ToInt32(st.AthenaError.ErrorCategory) != 2 || aws.ToInt32(st.AthenaError.ErrorType) != 1110 ||
		!strings.HasPrefix(aws.ToString(st.StateChangeReason), "TABLE_NOT_FOUND: ") {
		t.Fatalf("status = %+v / %+v", st, st.AthenaError)
	}
}

func (e *env) testStop() {
	t := e.t
	id := e.start(`SELECT count(*) FROM UNNEST(sequence(1, 10000)) a(x)
		CROSS JOIN UNNEST(sequence(1, 10000)) b(y) CROSS JOIN UNNEST(sequence(1, 10000)) c(z)`)
	helpers.Eventually(t, 30*time.Second, 100*time.Millisecond, func() bool {
		return e.execution(id).Status.State == types.QueryExecutionStateRunning
	}, "the query never started running")
	must[*athena.StopQueryExecutionOutput](t, "StopQueryExecution")(e.athena.StopQueryExecution(e.ctx,
		&athena.StopQueryExecutionInput{QueryExecutionId: aws.String(id)}))
	if st := e.wait(id).Status.State; st != types.QueryExecutionStateCancelled {
		t.Fatalf("state = %s, want CANCELLED", st)
	}
	// The engine is still there for the next query.
	e.mustQuery("SELECT 1")
}

// ─── Harness ─────────────────────────────────────────────────────────────────

type env struct {
	t      *testing.T
	ctx    context.Context
	srv    *helpers.TestServer
	athena *athena.Client
	glue   *glue.Client
	s3     *s3.Client
}

func newEnv(t *testing.T, srv *helpers.TestServer) *env {
	creds := aws.Credentials{AccessKeyID: "test", SecretAccessKey: "test"}
	provider := aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) { return creds, nil })
	return &env{t: t, ctx: t.Context(), srv: srv,
		athena: athena.New(athena.Options{Region: "us-east-1", Credentials: provider, BaseEndpoint: aws.String(srv.URL)}),
		glue:   glue.New(glue.Options{Region: "us-east-1", Credentials: provider, BaseEndpoint: aws.String(srv.URL)}),
		s3:     s3.New(s3.Options{Region: "us-east-1", Credentials: provider, BaseEndpoint: aws.String(srv.URL), UsePathStyle: true}),
	}
}

// in is e reporting to t, a subtest's own.
func (e *env) in(t *testing.T) *env {
	c := *e
	c.t, c.ctx = t, t.Context()
	return &c
}

// waitForDocker waits for the Docker probe to wire the engine, which
// router.New does not await.
func (e *env) waitForDocker() {
	helpers.Eventually(e.t, 60*time.Second, 50*time.Millisecond, func() bool {
		return e.engineStatus().State != "off"
	}, "the Docker probe never wired Athena's engine")
}

// engineStatus is the emulator-only engine status endpoint's report.
type engineStatus struct {
	State       string `json:"state"`
	ContainerID string `json:"containerId"`
	PullMillis  int64  `json:"pullMillis"`
	StartMillis int64  `json:"startMillis"`
}

func (e *env) engineStatus() engineStatus {
	resp, err := http.Get(e.srv.URL + "/_overcast/athena/engine")
	if err != nil {
		e.t.Fatalf("engine status: %v", err)
	}
	defer resp.Body.Close()
	var st engineStatus
	if err := json.NewDecoder(resp.Body).Decode(&st); err != nil {
		e.t.Fatalf("engine status: %v", err)
	}
	return st
}

// logMemory reports the engine container's memory, which the engine's docs
// quote.
func (e *env) logMemory(when string) {
	id := e.engineStatus().ContainerID
	if used, err := docker.NewClient(helpers.TestDockerSocket(), nil).ContainerMemoryUsage(e.ctx, id); err == nil {
		e.t.Logf("engine memory %s: %d MiB", when, used>>20)
	}
}

func (e *env) createBucket(name string) {
	must[*s3.CreateBucketOutput](e.t, "CreateBucket")(e.s3.CreateBucket(e.ctx, &s3.CreateBucketInput{Bucket: aws.String(name)}))
}

func (e *env) put(key, body string) {
	must[*s3.PutObjectOutput](e.t, "PutObject")(e.s3.PutObject(e.ctx, &s3.PutObjectInput{
		Bucket: aws.String(bucket), Key: aws.String(key), Body: strings.NewReader(body)}))
}

func (e *env) get(key, from string) string {
	out := must[*s3.GetObjectOutput](e.t, "GetObject "+key)(e.s3.GetObject(e.ctx, &s3.GetObjectInput{Bucket: aws.String(from), Key: aws.String(key)}))
	defer out.Body.Close()
	body, _ := io.ReadAll(out.Body)
	return string(body)
}

func (e *env) start(query string) string {
	out := must[*athena.StartQueryExecutionOutput](e.t, "StartQueryExecution")(e.athena.StartQueryExecution(e.ctx, &athena.StartQueryExecutionInput{
		QueryString:         aws.String(query),
		ResultConfiguration: &types.ResultConfiguration{OutputLocation: aws.String(results)},
	}))
	return aws.ToString(out.QueryExecutionId)
}

func (e *env) execution(id string) *types.QueryExecution {
	out := must[*athena.GetQueryExecutionOutput](e.t, "GetQueryExecution")(e.athena.GetQueryExecution(e.ctx,
		&athena.GetQueryExecutionInput{QueryExecutionId: aws.String(id)}))
	return out.QueryExecution
}

// wait polls an execution until it finishes. The first query's budget
// includes pulling nothing (PullOrSkip warmed the image) and starting the
// engine, which takes tens of seconds on a loaded machine.
func (e *env) wait(id string) *types.QueryExecution {
	var qe *types.QueryExecution
	helpers.Eventually(e.t, 5*time.Minute, 200*time.Millisecond, func() bool {
		qe = e.execution(id)
		switch qe.Status.State {
		case types.QueryExecutionStateSucceeded, types.QueryExecutionStateFailed, types.QueryExecutionStateCancelled:
			return true
		case types.QueryExecutionStateQueued, types.QueryExecutionStateRunning:
		}
		return false
	}, "query "+id+" never finished")
	return qe
}

// mustQuery runs a query that has to succeed.
func (e *env) mustQuery(query string) string {
	e.t.Helper()
	id := e.start(query)
	if qe := e.wait(id); qe.Status.State != types.QueryExecutionStateSucceeded {
		e.t.Fatalf("%s: %s: %s", query, qe.Status.State, aws.ToString(qe.Status.StateChangeReason))
	}
	return id
}

func (e *env) results(id string, maxResults int32, next string) *athena.GetQueryResultsOutput {
	in := &athena.GetQueryResultsInput{QueryExecutionId: aws.String(id)}
	if maxResults > 0 {
		in.MaxResults = aws.Int32(maxResults)
	}
	if next != "" {
		in.NextToken = aws.String(next)
	}
	return must[*athena.GetQueryResultsOutput](e.t, "GetQueryResults")(e.athena.GetQueryResults(e.ctx, in))
}

// metadataLocation is an Iceberg table's current metadata file, from Glue.
func (e *env) metadataLocation(table string) string {
	out := must[*glue.GetTableOutput](e.t, "GetTable")(e.glue.GetTable(e.ctx, &glue.GetTableInput{
		DatabaseName: aws.String("demo"), Name: aws.String(table)}))
	return out.Table.Parameters["metadata_location"]
}

func rowsOf(out *athena.GetQueryResultsOutput) [][]string {
	rows := make([][]string, len(out.ResultSet.Rows))
	for i, r := range out.ResultSet.Rows {
		for _, d := range r.Data {
			rows[i] = append(rows[i], aws.ToString(d.VarCharValue))
		}
	}
	return rows
}

func must[T any](t *testing.T, what string) func(T, error) T {
	return func(v T, err error) T {
		t.Helper()
		if err != nil {
			t.Fatalf("%s: %v", what, err)
		}
		return v
	}
}
