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
			if name == "by_id" {
				return "SELECT * FROM t WHERE id = ?", true
			}
			return "", false
		},
	}
}

func TestRewriteForEngine(t *testing.T) {
	cases := map[string]string{
		// Passed through as written.
		"SELECT 1": "SELECT 1",
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
	} {
		if got, err := rewriteForEngine(sql, rw); err == nil {
			t.Errorf("rewriteForEngine(%q) = %q, want an error", sql, got)
		}
	}
}

func TestTrinoType(t *testing.T) {
	for hive, want := range map[string]string{
		"STRING": "varchar", "int": "integer", "float": "real", "binary": "varbinary", "timestamp": "timestamp(6)",
		"decimal(10, 2)": "decimal(10,2)", "varchar(5)": "varchar(5)", "bigint": "bigint",
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
