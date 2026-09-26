package athenaengine_test

// s3tables_test.go — Athena reading and writing S3 Tables on the real
// engine: a table bucket is queried as "s3tablescatalog/<bucket>", through
// S3 Tables' own Iceberg REST catalog, and every write commits there.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/athena"
	"github.com/aws/aws-sdk-go-v2/service/athena/types"
	"github.com/aws/aws-sdk-go-v2/service/s3tables"

	s3tablessvc "github.com/overcast-sh/overcast/internal/services/s3tables"
	"github.com/overcast-sh/overcast/tests/helpers"
)

const (
	tableBucket   = "athena-tables"
	tablesCatalog = "s3tablescatalog/" + tableBucket
)

func (e *env) testS3Tables() {
	t := e.t
	// Given: a table bucket, namespace and table made through the S3 Tables
	// API — created while the engine is already running
	st := tablesOf(e, helpers.SeedS3Table(t, e.srv, tableBucket, "sales", "orders"))

	// When: rows are inserted in the bucket's catalog
	before := st.metadataLocation("sales", "orders")
	inserted := e.mustQueryIn(tablesCatalog, "sales", "INSERT INTO orders VALUES (1, 9.50), (2, 20.00)")

	// Then: the insert committed through the REST catalog, moving the table
	// on in S3 Tables
	if got := aws.ToInt64(e.results(inserted, 0, "").UpdateCount); got != 2 {
		t.Fatalf("INSERT UpdateCount = %d, want 2", got)
	}
	afterInsert := st.metadataLocation("sales", "orders")
	if afterInsert == before {
		t.Fatalf("metadata location stayed %q after the INSERT", before)
	}

	// And: they read back from AwsDataCatalog by the table's full name
	got := rowsOf(e.results(e.mustQuery(`SELECT id, amount FROM "`+tablesCatalog+`"."sales"."orders" ORDER BY id`), 0, ""))
	if len(got) != 3 || got[1][0] != "1" || got[2][1] != "20.00" {
		t.Fatalf("rows = %v", got)
	}

	// When: a MERGE updates one row and adds another
	e.mustQueryIn(tablesCatalog, "sales", `MERGE INTO orders o USING (VALUES (2, 25.00), (3, 5.00)) AS s(id, amount)
		ON o.id = s.id
		WHEN MATCHED THEN UPDATE SET amount = s.amount
		WHEN NOT MATCHED THEN INSERT VALUES (s.id, s.amount)`)

	// Then: it committed too, and the catalog holds a snapshot per write
	if after := st.metadataLocation("sales", "orders"); after == afterInsert {
		t.Fatalf("metadata location stayed %q after the MERGE", after)
	}
	if snaps := st.snapshots("sales", "orders"); snaps < 2 {
		t.Fatalf("the REST catalog has %d snapshots of orders, want one for the INSERT and more for the MERGE", snaps)
	}

	// When: a CTAS creates a table in the bucket, naming no location
	e.mustQuery(`CREATE TABLE "` + tablesCatalog + `"."sales"."big_orders" WITH (format = 'PARQUET')
		AS SELECT id, amount FROM "` + tablesCatalog + `"."sales"."orders" WHERE amount > 6`)

	// Then: S3 Tables has the table, committed, with the rows
	if loc := st.metadataLocation("sales", "big_orders"); loc == "" {
		t.Fatal("the CTAS table has no metadata location")
	}
	if snaps := st.snapshots("sales", "big_orders"); snaps != 1 {
		t.Fatalf("the CTAS table has %d snapshots, want 1", snaps)
	}
	got = rowsOf(e.results(e.mustQueryIn(tablesCatalog, "sales", "SELECT count(*), sum(amount) FROM big_orders"), 0, ""))
	if got[1][0] != "2" || got[1][1] != "34.50" {
		t.Fatalf("CTAS rows = %v, want 2 rows summing 34.50", got)
	}

	// And: Athena's DDL makes a namespace and an Iceberg table in the bucket
	e.mustQueryIn(tablesCatalog, "sales", "CREATE DATABASE `analytics`")
	e.mustQueryIn(tablesCatalog, "analytics", `CREATE TABLE daily (day date, total double)
		PARTITIONED BY (month(day)) TBLPROPERTIES ('table_type' = 'iceberg')`)
	e.mustQueryIn(tablesCatalog, "analytics", "INSERT INTO daily VALUES (DATE '2026-09-01', 1.5)")
	if loc := st.metadataLocation("analytics", "daily"); loc == "" {
		t.Fatal("the created table has no metadata location")
	}
	tables := rowsOf(e.results(e.mustQueryIn(tablesCatalog, "analytics", "SHOW TABLES"), 0, ""))
	if len(tables) != 1 || tables[0][0] != "daily" {
		t.Fatalf("SHOW TABLES = %v", tables)
	}

	// And: UPDATE and DELETE commit too, and DROP removes the table and then
	// the namespace
	e.mustQueryIn(tablesCatalog, "analytics", "UPDATE daily SET total = 2.5")
	e.mustQueryIn(tablesCatalog, "analytics", "DELETE FROM daily WHERE total > 2")
	if got := rowsOf(e.results(e.mustQueryIn(tablesCatalog, "analytics", "SELECT count(*) FROM daily"), 0, "")); got[1][0] != "0" {
		t.Fatalf("rows left after UPDATE and DELETE = %v, want 0", got)
	}
	e.mustQueryIn(tablesCatalog, "analytics", "DROP TABLE daily")
	e.mustQueryIn(tablesCatalog, "analytics", "DROP DATABASE analytics")
	if _, err := st.client.GetNamespace(e.ctx, &s3tables.GetNamespaceInput{TableBucketARN: aws.String(st.arn), Namespace: aws.String("analytics")}); err == nil {
		t.Fatal("the namespace is still there after DROP DATABASE")
	}
}

// mustQueryIn runs a query that has to succeed in catalog and database.
func (e *env) mustQueryIn(catalog, database, query string) string {
	e.t.Helper()
	out := must[*athena.StartQueryExecutionOutput](e.t, "StartQueryExecution")(e.athena.StartQueryExecution(e.ctx, &athena.StartQueryExecutionInput{
		QueryString:           aws.String(query),
		QueryExecutionContext: &types.QueryExecutionContext{Catalog: aws.String(catalog), Database: aws.String(database)},
		ResultConfiguration:   &types.ResultConfiguration{OutputLocation: aws.String(results)},
	}))
	id := aws.ToString(out.QueryExecutionId)
	if qe := e.wait(id); qe.Status.State != types.QueryExecutionStateSucceeded {
		e.t.Fatalf("%s: %s: %s", query, qe.Status.State, aws.ToString(qe.Status.StateChangeReason))
	}
	return id
}

// tablesEnv reads the table bucket through S3 Tables' API and its REST
// catalog.
type tablesEnv struct {
	*env
	client *s3tables.Client
	arn    string
}

func tablesOf(e *env, arn string) tablesEnv {
	provider := aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
		return aws.Credentials{AccessKeyID: "test", SecretAccessKey: "test"}, nil
	})
	return tablesEnv{env: e, arn: arn,
		client: s3tables.New(s3tables.Options{Region: "us-east-1", Credentials: provider, BaseEndpoint: aws.String(e.srv.URL)})}
}

// metadataLocation is a table's current metadata file, from S3 Tables.
func (e tablesEnv) metadataLocation(namespace, table string) string {
	out := must[*s3tables.GetTableMetadataLocationOutput](e.t, "GetTableMetadataLocation")(e.client.GetTableMetadataLocation(e.ctx,
		&s3tables.GetTableMetadataLocationInput{TableBucketARN: aws.String(e.arn), Namespace: aws.String(namespace), Name: aws.String(table)}))
	return aws.ToString(out.MetadataLocation)
}

// snapshots counts a table's snapshots as the Iceberg REST catalog serves
// them, over its unsigned mount: there is no SDK for the REST catalog.
func (e tablesEnv) snapshots(namespace, table string) int {
	u := e.srv.URL + s3tablessvc.IcebergInternalRoot + "/v1/" + url.QueryEscape(e.arn) + "/namespaces/" + namespace + "/tables/" + table
	resp, err := http.Get(u)
	if err != nil {
		e.t.Fatalf("load table %s: %v", table, err)
	}
	defer resp.Body.Close()
	var loaded struct {
		Metadata struct {
			Snapshots []json.RawMessage `json:"snapshots"`
		} `json:"metadata"`
	}
	if resp.StatusCode != http.StatusOK {
		e.t.Fatalf("load table %s: HTTP %d", table, resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(&loaded); err != nil {
		e.t.Fatalf("load table %s: %v", table, err)
	}
	return len(loaded.Metadata.Snapshots)
}
