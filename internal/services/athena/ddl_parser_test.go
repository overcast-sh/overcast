package athena

import (
	"errors"
	"reflect"
	"testing"
)

func mustParseDDL(t *testing.T, sql string) ddlStatement {
	t.Helper()
	stmt, err := parseDDL(sql)
	if err != nil {
		t.Fatalf("parseDDL(%q): %v", sql, err)
	}
	if stmt == nil {
		t.Fatalf("parseDDL(%q) left the statement to the engine", sql)
	}
	return stmt
}

func TestParseDDL_createExternalTable(t *testing.T) {
	// Given: Athena's documented CREATE EXTERNAL TABLE, every clause used
	sql := "CREATE EXTERNAL TABLE IF NOT EXISTS `logs`.access (\n" +
		"  ts timestamp COMMENT 'when',\n  attrs map<string, array<struct<k:string,v:int>>>,\n  price decimal(10, 2)\n)\n" +
		"COMMENT 'web logs' PARTITIONED BY (year string, month int COMMENT 'm')\n" +
		"CLUSTERED BY (ts) INTO 8 BUCKETS\n" +
		"ROW FORMAT DELIMITED FIELDS TERMINATED BY '\\t' ESCAPED BY '\\\\' NULL DEFINED AS ''\n" +
		"STORED AS TEXTFILE LOCATION 's3://b/logs/' TBLPROPERTIES ('skip.header.line.count'='1');"

	// When: it is parsed
	s := mustParseDDL(t, sql).(*createTableStmt)

	// Then: each clause is read, types as written
	if s.Table != (tableRef{Database: "logs", Table: "access"}) || !s.IfNotExists || s.Comment != "web logs" {
		t.Fatalf("head = %+v", s)
	}
	wantCols := []columnDef{
		{Name: "ts", Type: "timestamp", Comment: "when"},
		{Name: "attrs", Type: "map<string, array<struct<k:string,v:int>>>"},
		{Name: "price", Type: "decimal(10, 2)"},
	}
	if !reflect.DeepEqual(s.Columns, wantCols) {
		t.Fatalf("columns = %+v", s.Columns)
	}
	parts, err := s.partitionColumns()
	if err != nil || !reflect.DeepEqual(parts, []columnDef{{Name: "year", Type: "string"}, {Name: "month", Type: "int", Comment: "m"}}) {
		t.Fatalf("partitions = %+v, %v", parts, err)
	}
	if s.Buckets != 8 || !reflect.DeepEqual(s.BucketBy, []string{"ts"}) {
		t.Fatalf("buckets = %d by %v", s.Buckets, s.BucketBy)
	}
	wantSerde := map[string]string{"field.delim": "\t", "serialization.format": "\t", "escape.delim": "\\", "serialization.null.format": ""}
	if s.RowFormat.SerDe != lazySimpleSerDe || !reflect.DeepEqual(s.RowFormat.Params, wantSerde) {
		t.Fatalf("row format = %+v", s.RowFormat)
	}
	if s.StoredAs.Format != "textfile" || s.Location != "s3://b/logs/" || s.Properties["skip.header.line.count"] != "1" {
		t.Fatalf("storage = %+v %q %v", s.StoredAs, s.Location, s.Properties)
	}
}

func TestParseDDL_serdeAndFormatClasses(t *testing.T) {
	s := mustParseDDL(t, `CREATE EXTERNAL TABLE t (a string)
		ROW FORMAT SERDE 'org.openx.data.jsonserde.JsonSerDe' WITH SERDEPROPERTIES ('ignore.malformed.json' = 'true')
		STORED AS INPUTFORMAT 'in.Format' OUTPUTFORMAT 'out.Format' LOCATION "s3://b/t/"`).(*createTableStmt)
	if s.RowFormat.SerDe != "org.openx.data.jsonserde.JsonSerDe" || s.RowFormat.Params["ignore.malformed.json"] != "true" ||
		s.StoredAs.InputFormat != "in.Format" || s.StoredAs.OutputFormat != "out.Format" || s.Location != "s3://b/t/" {
		t.Fatalf("parsed = %+v / %+v", s.RowFormat, s.StoredAs)
	}
	sd, known := s.storageDescriptor()
	if !known || sd.InputFormat != "in.Format" || sd.SerdeInfo.SerializationLibrary != "org.openx.data.jsonserde.JsonSerDe" {
		t.Fatalf("storage descriptor = %+v", sd)
	}
}

func TestParseDDL_otherStatements(t *testing.T) {
	cases := map[string]ddlStatement{
		"CREATE DATABASE IF NOT EXISTS sales COMMENT 'c' LOCATION 's3://b/' WITH DBPROPERTIES ('k'='v')": &createDatabaseStmt{
			Name: "sales", Comment: "c", Location: "s3://b/", IfNotExists: true, Properties: map[string]string{"k": "v"}},
		"create schema Sales":                         &createDatabaseStmt{Name: "sales"},
		"DROP DATABASE IF EXISTS sales CASCADE":       &dropDatabaseStmt{Name: "sales", IfExists: true, Cascade: true},
		"DROP SCHEMA sales RESTRICT":                  &dropDatabaseStmt{Name: "sales"},
		"DROP TABLE IF EXISTS awsdatacatalog.sales.t": &dropTableStmt{Table: tableRef{Database: "sales", Table: "t"}, IfExists: true},
		"MSCK REPAIR TABLE t":                         &repairTableStmt{Table: tableRef{Table: "t"}},
		"SHOW DATABASES LIKE 'sa*'":                   &showDatabasesStmt{Pattern: "sa*"},
		"SHOW TABLES IN sales 'o*|l*'":                &showTablesStmt{Database: "sales", Pattern: "o*|l*"},
		"SHOW PARTITIONS sales.t":                     &showPartitionsStmt{Table: tableRef{Database: "sales", Table: "t"}},
		"SHOW COLUMNS IN t FROM sales":                &showColumnsStmt{Table: tableRef{Database: "sales", Table: "t"}},
		"SHOW TBLPROPERTIES t('k')":                   &showTablePropertiesStmt{Table: tableRef{Table: "t"}, Property: "k"},
		"DESCRIBE FORMATTED sales.t":                  &describeStmt{Table: tableRef{Database: "sales", Table: "t"}},
		"ALTER TABLE t ADD IF NOT EXISTS PARTITION (year='2026', month=9) LOCATION 's3://b/x/' PARTITION (year='2027', month=1)": &alterPartitionsStmt{
			Table: tableRef{Table: "t"}, IfGuard: true, Partitions: []partitionSpec{
				{Keys: []string{"year", "month"}, Values: []string{"2026", "9"}, Location: "s3://b/x/"},
				{Keys: []string{"year", "month"}, Values: []string{"2027", "1"}},
			}},
		"ALTER TABLE t DROP PARTITION (year='2026'), PARTITION (year='2027')": &alterPartitionsStmt{
			Table: tableRef{Table: "t"}, Drop: true, Partitions: []partitionSpec{
				{Keys: []string{"year"}, Values: []string{"2026"}}, {Keys: []string{"year"}, Values: []string{"2027"}},
			}},
	}
	for sql, want := range cases {
		if got := mustParseDDL(t, sql); !reflect.DeepEqual(got, want) {
			t.Errorf("parseDDL(%q) = %+v, want %+v", sql, got, want)
		}
	}
}

func TestParseDDL_leavesTheEngineItsStatements(t *testing.T) {
	for _, sql := range []string{
		"SELECT 1", "INSERT INTO t VALUES (1)", "CREATE TABLE t AS SELECT 1", "CREATE VIEW v AS SELECT 1",
		"ALTER TABLE t ADD COLUMNS (c int)", "DESCRIBE INPUT stmt", "SHOW CREATE TABLE t", "SELECT 'unterminated",
	} {
		if stmt, err := parseDDL(sql); stmt != nil || err != nil {
			t.Errorf("parseDDL(%q) = %+v, %v; want it left to the engine", sql, stmt, err)
		}
	}
}

func TestParseDDL_malformedStatementsFail(t *testing.T) {
	for _, sql := range []string{
		"CREATE EXTERNAL TABLE t",
		"CREATE EXTERNAL TABLE t (a string) LOCATION",
		"CREATE EXTERNAL TABLE t (a string) STORED WRONGLY",
		"DROP TABLE t extra",
		"ALTER TABLE t ADD PARTITION (year=)",
		"SHOW COLUMNS t",
	} {
		_, err := parseDDL(sql)
		var syntax *ddlSyntaxError
		if !errors.As(err, &syntax) {
			t.Errorf("parseDDL(%q) error = %v, want a syntax error", sql, err)
		}
	}
}

func TestLexSQL_hiveEscapes(t *testing.T) {
	for src, want := range map[string]string{
		`'\u0001'`: "\x01", `'\001'`: "\x01", `'\t|\n'`: "\t|\n", `'\777'`: "777",
		`'\u00e9t\u00e9'`: "\u00e9t\u00e9", `'\Z'`: "\x1a", `'50\%'`: `50\%`, `'it\'s'`: "it's",
	} {
		// When: a Hive string is lexed
		toks, err := lexSQL(src, dialectHive)
		// Then: its escapes are read as Hive reads them
		if err != nil || toks[0].text != want {
			t.Errorf("lexSQL(%s) = %q, %v; want %q", src, toks[0].text, err, want)
		}
	}
	// A Trino string has no escapes, and a double-quoted token is a name.
	toks, err := lexSQL(`'C:\' "Col"`, dialectTrino)
	if err != nil || toks[0].text != `C:\` || toks[1].kind != tokIdent || toks[1].text != "Col" {
		t.Fatalf("Trino tokens = %+v, %v", toks, err)
	}
}
