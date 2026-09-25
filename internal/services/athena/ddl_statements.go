package athena

import (
	"strconv"
	"strings"
)

// ddl_statements.go — the CREATE and DROP statements parseDDL reads, and
// each one's grammar. What each does to the catalog is its run method, in
// ddl_catalog.go. The partition statements are in ddl_partitions.go and
// SHOW and DESCRIBE in ddl_show.go, grammar and run together.

// tableRef names a table; an empty Database is the query's own.
type tableRef struct{ Database, Table string }

// columnDef is one column as a DDL statement declares it, its type as
// written in Hive's notation.
type columnDef struct{ Name, Type, Comment string }

// rowFormat is a table's ROW FORMAT clause: a SerDe, or delimiters, which
// are LazySimpleSerDe's parameters.
type rowFormat struct {
	SerDe  string
	Params map[string]string
}

// storedAs is a table's STORED AS clause: a format name, or its input and
// output format classes.
type storedAs struct{ Format, InputFormat, OutputFormat string }

// createDatabaseStmt is CREATE (DATABASE|SCHEMA) [IF NOT EXISTS] name
// [COMMENT 'c'] [LOCATION 's3://…'] [WITH DBPROPERTIES (…)].
type createDatabaseStmt struct {
	Name, Comment, Location string
	IfNotExists             bool
	Properties              map[string]string
}

func parseCreateDatabase(p *ddlParser) (ddlStatement, error) {
	s := &createDatabaseStmt{IfNotExists: p.ifNotExists()}
	var err error
	if s.Name, err = p.name(); err != nil {
		return nil, err
	}
	for err == nil && p.peek().kind != tokEOF && !p.peek().isSymbol(";") {
		switch {
		case p.accept("COMMENT"):
			s.Comment, err = p.str()
		case p.accept("LOCATION"):
			s.Location, err = p.str()
		case p.accept("WITH", "DBPROPERTIES"):
			s.Properties, err = p.properties()
		default:
			err = p.fail("unexpected input")
		}
	}
	if err != nil {
		return nil, err
	}
	return s, p.end()
}

// dropDatabaseStmt is DROP (DATABASE|SCHEMA) [IF EXISTS] name
// [RESTRICT|CASCADE].
type dropDatabaseStmt struct {
	Name              string
	IfExists, Cascade bool
}

func parseDropDatabase(p *ddlParser) (ddlStatement, error) {
	s := &dropDatabaseStmt{IfExists: p.ifExists()}
	var err error
	if s.Name, err = p.name(); err != nil {
		return nil, err
	}
	s.Cascade = p.accept("CASCADE")
	if !s.Cascade {
		p.accept("RESTRICT")
	}
	return s, p.end()
}

// createTableStmt is CREATE EXTERNAL TABLE [IF NOT EXISTS] name (columns)
// [COMMENT 'c'] [PARTITIONED BY (columns)] [CLUSTERED BY (names) INTO n
// BUCKETS] [ROW FORMAT …] [STORED AS …] [LOCATION 's3://…']
// [TBLPROPERTIES (…)].
type createTableStmt struct {
	Table       tableRef
	IfNotExists bool
	Columns     []columnDef
	// PartitionedBy are PARTITIONED BY's entries as written: "name type"
	// for a Hive table, a column or a transform for an Iceberg one.
	PartitionedBy []string
	Comment       string
	BucketBy      []string
	Buckets       int32
	RowFormat     *rowFormat
	StoredAs      storedAs
	Location      string
	Properties    map[string]string
}

func parseCreateExternalTable(p *ddlParser) (ddlStatement, error) {
	s := &createTableStmt{IfNotExists: p.ifNotExists()}
	var err error
	if s.Table, err = p.tableName(); err != nil {
		return nil, err
	}
	if s.Columns, err = p.columns(); err != nil {
		return nil, err
	}
	for err == nil && p.peek().kind != tokEOF && !p.peek().isSymbol(";") {
		err = s.parseClause(p)
	}
	if err != nil {
		return nil, err
	}
	return s, p.end()
}

// parseClause reads one of the clauses after the column list.
func (s *createTableStmt) parseClause(p *ddlParser) (err error) {
	switch {
	case p.accept("COMMENT"):
		s.Comment, err = p.str()
	case p.accept("PARTITIONED", "BY"):
		s.PartitionedBy, err = p.rawList()
	case p.accept("CLUSTERED", "BY"):
		err = s.parseBuckets(p)
	case p.accept("ROW", "FORMAT"):
		s.RowFormat, err = parseRowFormat(p)
	case p.accept("STORED", "AS"):
		s.StoredAs, err = parseStoredAs(p)
	case p.accept("LOCATION"):
		s.Location, err = p.str()
	case p.accept("TBLPROPERTIES"):
		s.Properties, err = p.properties()
	default:
		err = p.fail("unexpected input")
	}
	return err
}

// partitionColumns reads PARTITIONED BY's entries as a Hive table's
// partition columns.
func (s *createTableStmt) partitionColumns() ([]columnDef, error) {
	cols := make([]columnDef, len(s.PartitionedBy))
	for i, entry := range s.PartitionedBy {
		p, err := newDDLParser(entry)
		if err != nil {
			return nil, err
		}
		if cols[i].Name, err = p.name(); err != nil {
			return nil, err
		}
		if cols[i].Type, err = p.rawUntil(func(t token) bool { return t.is("COMMENT") }); err != nil {
			return nil, err
		}
		cols[i].Type = strings.ToLower(cols[i].Type)
		if p.accept("COMMENT") {
			if cols[i].Comment, err = p.str(); err != nil {
				return nil, err
			}
		}
	}
	return cols, nil
}

// parseBuckets reads (names) [SORTED BY (…)] INTO n BUCKETS.
func (s *createTableStmt) parseBuckets(p *ddlParser) (err error) {
	if s.BucketBy, err = p.nameList(); err != nil {
		return err
	}
	if p.accept("SORTED", "BY") {
		if _, err := p.rawUntil(func(t token) bool { return t.is("INTO") }); err != nil {
			return err
		}
	}
	if err := p.expect("INTO"); err != nil {
		return err
	}
	n, err := strconv.ParseInt(p.peek().text, 10, 32)
	if p.peek().kind != tokNumber || err != nil {
		return p.fail("expected a bucket count")
	}
	p.next()
	s.Buckets = int32(n)
	return p.expect("BUCKETS")
}

// parseRowFormat reads SERDE 'class' [WITH SERDEPROPERTIES (…)], or
// DELIMITED and its delimiter clauses.
func parseRowFormat(p *ddlParser) (*rowFormat, error) {
	if p.accept("SERDE") {
		serde, err := p.str()
		if err != nil {
			return nil, err
		}
		rf := &rowFormat{SerDe: serde, Params: map[string]string{}}
		if p.accept("WITH", "SERDEPROPERTIES") {
			if rf.Params, err = p.properties(); err != nil {
				return nil, err
			}
		}
		return rf, nil
	}
	if err := p.expect("DELIMITED"); err != nil {
		return nil, err
	}
	rf := &rowFormat{SerDe: lazySimpleSerDe, Params: map[string]string{}}
	for {
		param, ok := acceptDelimiter(p)
		if !ok {
			return rf, nil
		}
		v, err := p.str()
		if err != nil {
			return nil, err
		}
		rf.Params[param] = v
		if param == "field.delim" {
			rf.Params["serialization.format"] = v
		}
	}
}

// delimiterClauses are ROW FORMAT DELIMITED's clauses and the SerDe
// parameter each sets.
var delimiterClauses = []struct {
	keywords []string
	param    string
}{
	{[]string{"FIELDS", "TERMINATED", "BY"}, "field.delim"},
	{[]string{"ESCAPED", "BY"}, "escape.delim"},
	{[]string{"COLLECTION", "ITEMS", "TERMINATED", "BY"}, "collection.delim"},
	{[]string{"MAP", "KEYS", "TERMINATED", "BY"}, "mapkey.delim"},
	{[]string{"LINES", "TERMINATED", "BY"}, "line.delim"},
	{[]string{"NULL", "DEFINED", "AS"}, "serialization.null.format"},
}

func acceptDelimiter(p *ddlParser) (string, bool) {
	for _, c := range delimiterClauses {
		if p.accept(c.keywords...) {
			return c.param, true
		}
	}
	return "", false
}

// parseStoredAs reads a format name, or INPUTFORMAT 'x' OUTPUTFORMAT 'y'.
func parseStoredAs(p *ddlParser) (s storedAs, err error) {
	if p.accept("INPUTFORMAT") {
		if s.InputFormat, err = p.str(); err != nil {
			return s, err
		}
		if err = p.expect("OUTPUTFORMAT"); err != nil {
			return s, err
		}
		s.OutputFormat, err = p.str()
		return s, err
	}
	s.Format, err = p.name()
	return s, err
}

// dropTableStmt is DROP TABLE [IF EXISTS] name.
type dropTableStmt struct {
	Table    tableRef
	IfExists bool
}

func parseDropTable(p *ddlParser) (ddlStatement, error) {
	s := &dropTableStmt{IfExists: p.ifExists()}
	var err error
	if s.Table, err = p.tableName(); err != nil {
		return nil, err
	}
	return s, p.end()
}
