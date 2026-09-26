package athena

import (
	"strings"
	"testing"
)

func testRewrite() engineRewrite {
	return engineRewrite{
		database:       "demo",
		tablesLocation: "s3://results/q/tables/abc/",
		prepared: func(name string) (string, bool) {
			switch name {
			case "by_id":
				return "SELECT * FROM t WHERE id = ?", true
			case "My@Query:1":
				return "SELECT 1; -- the one", true
			}
			return "", false
		},
	}
}

func TestRewriteForEngine(t *testing.T) {
	cases := map[string]string{
		// Passed through as written.
		"SELECT 1": "SELECT 1",
		// A final semicolon, and comments after it, are dropped: Trino
		// rejects the one. A backslash is no escape in Trino's strings.
		"SELECT 1;":            "SELECT 1",
		"SELECT 1 ; -- done\n": "SELECT 1",
		`SELECT 'C:\' AS p;`:   `SELECT 'C:\' AS p`,
		"MERGE INTO t USING s ON t.id = s.id WHEN MATCHED THEN DELETE": "MERGE INTO t USING s ON t.id = s.id WHEN MATCHED THEN DELETE",
		// An Iceberg table: Trino's types, WITH, the Iceberg catalog, and
		// partition transforms with the column first.
		"CREATE TABLE IF NOT EXISTS orders (id bigint, name string, tags array<string>, ts timestamp) " +
			"PARTITIONED BY (bucket(16, id), day(ts)) LOCATION 's3://b/orders/' " +
			"TBLPROPERTIES ('table_type'='ICEBERG', 'format'='orc')": `CREATE TABLE IF NOT EXISTS "awsdatacatalog_iceberg"."demo"."orders" ` +
			`("id" bigint, "name" varchar, "tags" array(varchar), "ts" timestamp(6)) ` +
			`WITH (location = 's3://b/orders/', format = 'ORC', partitioning = ARRAY['bucket(id, 16)', 'day(ts)'])`,
		// A Hive CTAS: defaults to Parquet under the result location.
		"CREATE TABLE sales.copy AS SELECT * FROM t": `CREATE TABLE "awsdatacatalog"."sales"."copy" ` +
			`WITH (format = 'PARQUET', external_location = 's3://results/q/tables/abc/') AS SELECT * FROM t`,
		// Athena's CTAS properties become Trino's; ones it has no use for go.
		"CREATE TABLE copy WITH (format = 'textfile', field_delimiter = ',', write_compression = 'GZIP', " +
			"partitioned_by = ARRAY['year']) AS SELECT a, year FROM t": `CREATE TABLE "awsdatacatalog"."demo"."copy" ` +
			`WITH (format = 'TEXTFILE', textfile_field_separator = ',', partitioned_by = ARRAY['year'], ` +
			`external_location = 's3://results/q/tables/abc/') AS SELECT a, year FROM t`,
		// An Iceberg CTAS lands on the Iceberg catalog.
		"CREATE TABLE ice WITH (table_type = 'ICEBERG', is_external = false, location = 's3://b/ice/') AS SELECT 1 AS a": `CREATE TABLE "awsdatacatalog_iceberg"."demo"."ice" ` +
			`WITH (location = 's3://b/ice/', format = 'PARQUET') AS SELECT 1 AS a`,
		// A prepared statement, with and without USING.
		"EXECUTE by_id USING 42": `EXECUTE IMMEDIATE 'SELECT * FROM t WHERE id = ?' USING 42`,
		// A prepared statement's name keeps its case, its @ and its :.
		"EXECUTE My@Query:1;": `EXECUTE IMMEDIATE 'SELECT 1'`,
		// A CTAS's query goes without the final semicolon too.
		"CREATE TABLE sales.copy AS SELECT * FROM t;": `CREATE TABLE "awsdatacatalog"."sales"."copy" ` +
			`WITH (format = 'PARQUET', external_location = 's3://results/q/tables/abc/') AS SELECT * FROM t`,
	}
	for sql, want := range cases {
		got, err := rewriteForEngine(sql, testRewrite())
		if err != nil || got != want {
			t.Errorf("rewriteForEngine(%q)\n got %q, %v\nwant %q", sql, got, err, want)
		}
	}
}

func TestRewriteForEngine_executionParameters(t *testing.T) {
	rw := testRewrite()
	rw.parameters = []string{"'it''s'", "7"}
	got, err := rewriteForEngine("SELECT * FROM t WHERE name = ? AND id = ?", rw)
	if want := `EXECUTE IMMEDIATE 'SELECT * FROM t WHERE name = ? AND id = ?' USING 'it''s', 7`; err != nil || got != want {
		t.Fatalf("got %q, %v; want %q", got, err, want)
	}
	if got, _ := rewriteForEngine("EXECUTE by_id", rw); !strings.HasSuffix(got, "USING 'it''s', 7") {
		t.Fatalf("EXECUTE with ExecutionParameters = %q", got)
	}
	// Parameters are bound even to SQL Overcast cannot lex.
	if got, _ := rewriteForEngine("SELECT ? FROM 'unterminated;", rw); !strings.HasPrefix(got, "EXECUTE IMMEDIATE ") {
		t.Fatalf("unlexable SQL with ExecutionParameters = %q", got)
	}
}

func TestRewriteForEngine_refusals(t *testing.T) {
	noLocation := testRewrite()
	noLocation.tablesLocation = ""
	for sql, rw := range map[string]engineRewrite{
		"CREATE TABLE t (a int)": testRewrite(),
		"CREATE TABLE t (a int) TBLPROPERTIES ('table_type'='ICEBERG')":                            testRewrite(),
		"CREATE TABLE t (a unknown<int) LOCATION 's3://b/' TBLPROPERTIES ('table_type'='ICEBERG')": testRewrite(),
		"EXECUTE missing":            testRewrite(),
		"CREATE TABLE t AS SELECT 1": noLocation,
		// Trino's catalog statements, which Athena has not, and which would
		// change the catalogs Overcast manages.
		"DROP CATALOG awsdatacatalog":                   testRewrite(),
		"create catalog x using iceberg":                testRewrite(),
		`ALTER CATALOG "s3tablescatalog/b" RENAME TO y`: testRewrite(),
	} {
		if got, err := rewriteForEngine(sql, rw); err == nil {
			t.Errorf("rewriteForEngine(%q) = %q, want an error", sql, got)
		}
	}
}

func TestTrinoType(t *testing.T) {
	for hive, want := range map[string]string{
		"STRING": "varchar", "int": "integer", "float": "real", "binary": "varbinary", "timestamp": "timestamp(6)",
		"decimal(10, 2)": "decimal(10,2)", "varchar(5)": "varchar(5)", "bigint": "bigint", "decimal": "decimal(10,0)",
		"array<struct<a:int, b:map<string,double>>>": `array(row("a" integer, "b" map(varchar, double)))`,
	} {
		if got, err := trinoType(hive); err != nil || got != want {
			t.Errorf("trinoType(%q) = %q, %v; want %q", hive, got, err, want)
		}
	}
	for _, bad := range []string{"", "array<int", "struct<a int>", "map<string>"} {
		if got, err := trinoType(bad); err == nil {
			t.Errorf("trinoType(%q) = %q, want an error", bad, got)
		}
	}
}

func TestRewriteForEngine_s3Tables(t *testing.T) {
	inBucket := testRewrite()
	inBucket.catalog = "s3tablescatalog/sales-data"
	cases := []struct {
		name, sql string
		rw        engineRewrite
		want      string
	}{
		{"a query names a table bucket's catalog as Athena does",
			`SELECT * FROM "s3tablescatalog/sales-data"."ns"."t"`, testRewrite(),
			`SELECT * FROM "s3tablescatalog/sales-data"."ns"."t"`},
		{"an INSERT in the bucket's own catalog",
			"INSERT INTO daily VALUES (1)", inBucket,
			"INSERT INTO daily VALUES (1)"},
		{"an Iceberg table in the query's bucket has no location",
			"CREATE TABLE `daily_sales` (sale_date date, amount double) PARTITIONED BY (month(sale_date)) TBLPROPERTIES ('table_type' = 'iceberg')", inBucket,
			`CREATE TABLE "s3tablescatalog/sales-data"."demo"."daily_sales" ("sale_date" date, "amount" double) ` +
				`WITH (format = 'PARQUET', partitioning = ARRAY['month(sale_date)'])`},
		{"an Iceberg table qualified with a bucket's catalog",
			"CREATE TABLE `s3tablescatalog/Sales-Data`.ns.t (a int) TBLPROPERTIES ('table_type' = 'ICEBERG')", testRewrite(),
			`CREATE TABLE "s3tablescatalog/sales-data"."ns"."t" ("a" integer) WITH (format = 'PARQUET')`},
		{"a CTAS into a bucket is Iceberg without saying so, and has no location",
			`CREATE TABLE "s3tablescatalog/sales-data"."ns"."copy" WITH (format = 'orc') AS SELECT * FROM src`, testRewrite(),
			`CREATE TABLE "s3tablescatalog/sales-data"."ns"."copy" WITH (format = 'ORC') AS SELECT * FROM src`},
		{"a CTAS into a bucket may say it is Iceberg",
			"CREATE TABLE copy WITH (table_type = 'ICEBERG', format = 'AVRO') AS SELECT 1 AS a", inBucket,
			`CREATE TABLE "s3tablescatalog/sales-data"."demo"."copy" WITH (format = 'AVRO') AS SELECT 1 AS a`},
		{"a CTAS in the query's bucket defaults to Parquet",
			"CREATE TABLE copy AS SELECT 1 AS a", inBucket,
			`CREATE TABLE "s3tablescatalog/sales-data"."demo"."copy" WITH (format = 'PARQUET') AS SELECT 1 AS a`},
		{"a table qualified with AwsDataCatalog stays there from a bucket's catalog",
			"CREATE TABLE awsdatacatalog.demo.copy AS SELECT 1 AS a", inBucket,
			`CREATE TABLE "awsdatacatalog"."demo"."copy" WITH (format = 'PARQUET', external_location = 's3://results/q/tables/abc/') AS SELECT 1 AS a`},
	}
	for _, c := range cases {
		got, err := rewriteForEngine(c.sql, c.rw)
		if err != nil || got != c.want {
			t.Errorf("%s:\n got %q, %v\nwant %q", c.name, got, err, c.want)
		}
	}
}

func TestRewriteForEngine_s3TablesRefusesALocation(t *testing.T) {
	inBucket := testRewrite()
	inBucket.catalog = "s3tablescatalog/sales-data"
	for _, sql := range []string{
		"CREATE TABLE t (a int) LOCATION 's3://b/t/' TBLPROPERTIES ('table_type'='ICEBERG')",
		"CREATE TABLE t WITH (location = 's3://b/t/') AS SELECT 1 AS a",
		"CREATE TABLE t WITH (external_location = 's3://b/t/') AS SELECT 1 AS a",
		"CREATE TABLE t (a int)",
		"CREATE TABLE t WITH (table_type = 'HIVE') AS SELECT 1 AS a",
	} {
		if got, err := rewriteForEngine(sql, inBucket); err == nil {
			t.Errorf("rewriteForEngine(%q) = %q, want an error", sql, got)
		}
	}
}

func TestS3TablesCatalogOf(t *testing.T) {
	for _, c := range []struct {
		ref         tableRef
		query, want string
	}{
		{tableRef{Table: "t"}, "s3tablescatalog/b", "s3tablescatalog/b"},
		{tableRef{Table: "t"}, "S3TablesCatalog/B", "s3tablescatalog/b"},
		{tableRef{Catalog: "s3tablescatalog/b", Table: "t"}, "AwsDataCatalog", "s3tablescatalog/b"},
		{tableRef{Catalog: "awsdatacatalog", Table: "t"}, "s3tablescatalog/b", ""},
		{tableRef{Table: "t"}, "AwsDataCatalog", ""},
		{tableRef{Table: "t"}, "", ""},
		{tableRef{Table: "t"}, "s3tablescatalog", ""},
		{tableRef{Table: "t"}, "s3tablescatalogx/b", ""},
	} {
		if got := s3TablesCatalogOf(c.ref, c.query); got != c.want {
			t.Errorf("s3TablesCatalogOf(%+v, %q) = %q, want %q", c.ref, c.query, got, c.want)
		}
	}
}
