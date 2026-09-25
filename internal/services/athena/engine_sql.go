package athena

import (
	"fmt"
	"slices"
	"strings"
)

// engine_sql.go — the statement the engine runs for an Athena statement.
//
// Almost everything goes to Trino as written: Athena engine version 3 is
// Trino. The exceptions are the statements whose Athena form Trino cannot
// parse or would place wrongly:
//
//   - an Iceberg CREATE TABLE, written with Hive's column types and
//     TBLPROPERTIES ('table_type'='ICEBERG'), becomes Trino's CREATE TABLE …
//     WITH (…) on the Iceberg catalog;
//   - a CREATE TABLE AS SELECT has its Athena table properties translated,
//     and lands on the Iceberg catalog when table_type is ICEBERG — and, when
//     it names no location, under the query's result location, as Athena
//     puts it;
//   - EXECUTE of a prepared statement, and any query given
//     ExecutionParameters, becomes EXECUTE IMMEDIATE … USING.

// engineRewrite is what a rewrite knows beyond the statement itself.
type engineRewrite struct {
	// database is the query's database, which names an unqualified table.
	database string
	// tablesLocation is where a CTAS that names no location writes its
	// data: "<result location>/tables/<query id>/". Empty for managed
	// results, which have no location.
	tablesLocation string
	// prepared looks up a prepared statement in the query's workgroup.
	prepared func(name string) (string, bool)
	// parameters are the query's ExecutionParameters.
	parameters []string
}

// rewriteForEngine returns the SQL the engine runs for sql. It is read as
// Trino's SQL, except that a CREATE TABLE is Hive DDL when it lexes as such.
func rewriteForEngine(sql string, rw engineRewrite) (string, error) {
	p, err := newEngineParser(sql)
	if err != nil { // not lexically SQL: the engine's to judge
		return bindParameters(engineStatement(sql), rw.parameters), nil
	}
	switch {
	case p.peek().is("CREATE") && p.peekAt(1).is("TABLE"):
		if hive, err := newDDLParser(p.src); err == nil {
			p = hive
		}
		p.accept("CREATE", "TABLE")
		if sql, err = rewriteCreateTable(p, rw); err != nil {
			return "", err
		}
	case p.peek().is("EXECUTE") && !p.peekAt(1).is("IMMEDIATE"):
		p.next()
		return rewriteExecute(p, rw)
	default:
		sql = p.src
	}
	return bindParameters(sql, rw.parameters), nil
}

// engineStatement is sql without a final semicolon or trailing comments.
func engineStatement(sql string) string {
	if p, err := newEngineParser(sql); err == nil {
		return p.src
	}
	return strings.TrimSuffix(strings.TrimSpace(sql), ";")
}

// bindParameters runs sql with its ? placeholders bound to parameters, when
// it is given any.
func bindParameters(sql string, parameters []string) string {
	if len(parameters) == 0 {
		return sql
	}
	return executeImmediate(sql, strings.Join(parameters, ", "))
}

// executeImmediate runs sql with its ? placeholders bound to using.
func executeImmediate(sql, using string) string {
	out := "EXECUTE IMMEDIATE " + quoteString(engineStatement(sql))
	if using != "" {
		out += " USING " + using
	}
	return out
}

// rewriteExecute resolves EXECUTE name [USING values] against the
// workgroup's prepared statements.
func rewriteExecute(p *ddlParser, rw engineRewrite) (string, error) {
	name, err := p.preparedName()
	if err != nil {
		return "", err
	}
	query, found := rw.prepared(name)
	if !found {
		return "", &statementError{errorType: errorTypeUser, msg: "NOT_FOUND: Prepared statement not found: " + name}
	}
	using := strings.Join(rw.parameters, ", ")
	if p.accept("USING") {
		using = strings.TrimSpace(p.src[p.peek().start:])
	} else if err := p.end(); err != nil {
		return "", err
	}
	return executeImmediate(query, using), nil
}

// qualified is a table's name on a catalog, quoted for Trino.
func qualified(catalog string, t tableRef, database string) string {
	if t.Database == "" {
		t.Database = database
	}
	return quoteIdent(catalog) + "." + quoteIdent(t.Database) + "." + quoteIdent(t.Table)
}

// rewriteCreateTable handles CREATE TABLE after its two keywords: a CTAS, or
// an Iceberg table's DDL. Athena creates no other table without EXTERNAL.
func rewriteCreateTable(p *ddlParser, rw engineRewrite) (string, error) {
	ifNotExists := p.ifNotExists()
	table, err := p.tableName()
	if err != nil {
		return "", err
	}
	if !p.peek().isSymbol("(") {
		return rewriteCTAS(p, rw, table, ifNotExists)
	}
	s := &createTableStmt{Table: table, IfNotExists: ifNotExists}
	if s.Columns, err = p.columns(); err != nil {
		return "", err
	}
	for err == nil && p.peek().kind != tokEOF && !p.peek().isSymbol(";") {
		err = s.parseClause(p)
	}
	if err == nil {
		err = p.end()
	}
	if err != nil {
		return "", err
	}
	if !isIcebergProperties(s.Properties) {
		return "", &ddlSyntaxError{msg: "Only external table creation is supported. Use CREATE EXTERNAL TABLE, or CREATE TABLE with TBLPROPERTIES ('table_type'='ICEBERG')."}
	}
	return icebergCreateTable(s, rw.database)
}

// icebergCreateTable is an Iceberg table's DDL in Trino's form.
func icebergCreateTable(s *createTableStmt, database string) (string, error) {
	if s.Location == "" {
		return "", &statementError{errorType: errorTypeUser, msg: "LOCATION is required for an Iceberg table."}
	}
	cols := make([]string, len(s.Columns))
	for i, c := range s.Columns {
		t, err := trinoType(c.Type)
		if err != nil {
			return "", &ddlSyntaxError{msg: err.Error()}
		}
		cols[i] = quoteIdent(c.Name) + " " + t
		if c.Comment != "" {
			cols[i] += " COMMENT " + quoteString(c.Comment)
		}
	}
	props := []string{"location = " + quoteString(s.Location), "format = " + quoteString(icebergFormat(s.Properties))}
	if len(s.PartitionedBy) > 0 {
		parts := make([]string, len(s.PartitionedBy))
		for i, entry := range s.PartitionedBy {
			parts[i] = quoteString(icebergTransform(entry))
		}
		props = append(props, "partitioning = ARRAY["+strings.Join(parts, ", ")+"]")
	}
	var b strings.Builder
	b.WriteString("CREATE TABLE ")
	if s.IfNotExists {
		b.WriteString("IF NOT EXISTS ")
	}
	b.WriteString(qualified(icebergCatalog, s.Table, database) + " (" + strings.Join(cols, ", ") + ")")
	if s.Comment != "" {
		b.WriteString(" COMMENT " + quoteString(s.Comment))
	}
	b.WriteString(" WITH (" + strings.Join(props, ", ") + ")")
	return b.String(), nil
}

// icebergFormat is an Iceberg table's file format: TBLPROPERTIES' format,
// Parquet by default as on Athena.
func icebergFormat(props map[string]string) string {
	for k, v := range props {
		if strings.EqualFold(k, "format") {
			return strings.ToUpper(v)
		}
	}
	return "PARQUET"
}

// icebergTransform turns an Athena partition transform into Trino's:
// Athena writes bucket(16, id) and truncate(4, name) with the width first,
// Trino with the column first. A plain column, or year(ts), is unchanged.
func icebergTransform(entry string) string {
	name, args, found := strings.Cut(entry, "(")
	fn := strings.ToLower(strings.TrimSpace(name))
	if !found || (fn != "bucket" && fn != "truncate") {
		return entry
	}
	width, column, ok := strings.Cut(strings.TrimSuffix(strings.TrimSpace(args), ")"), ",")
	if !ok {
		return entry
	}
	return fmt.Sprintf("%s(%s, %s)", fn, strings.TrimSpace(column), strings.TrimSpace(width))
}

// ctasProperty is one table property of a CTAS, as written.
type ctasProperty struct{ Key, Value string }

// rewriteCTAS handles CREATE TABLE name [WITH (…)] AS query.
func rewriteCTAS(p *ddlParser, rw engineRewrite, table tableRef, ifNotExists bool) (string, error) {
	var props []ctasProperty
	if p.accept("WITH") {
		var err error
		if props, err = parseCTASProperties(p); err != nil {
			return "", err
		}
	}
	if !p.peek().is("AS") {
		return "", p.fail("expected AS")
	}
	query := p.src[p.peek().start:]
	catalog, with, err := ctasTarget(props, rw.tablesLocation)
	if err != nil {
		return "", err
	}
	head := "CREATE TABLE "
	if ifNotExists {
		head += "IF NOT EXISTS "
	}
	return head + qualified(catalog, table, rw.database) + " WITH (" + strings.Join(with, ", ") + ") " + query, nil
}

// parseCTASProperties reads (name = value, …), each value as written.
func parseCTASProperties(p *ddlParser) ([]ctasProperty, error) {
	if err := p.expectSymbol("("); err != nil {
		return nil, err
	}
	var props []ctasProperty
	for !p.acceptSymbol(")") {
		key, err := p.name()
		if err != nil {
			return nil, err
		}
		if err := p.expectSymbol("="); err != nil {
			return nil, err
		}
		value, err := p.rawUntil(func(t token) bool { return t.isSymbol(",") || t.isSymbol(")") })
		if err != nil {
			return nil, err
		}
		props = append(props, ctasProperty{Key: key, Value: value})
		p.acceptSymbol(",")
	}
	return props, nil
}

// Athena's CTAS properties, by the Trino property each becomes, per table
// type. A property with no Trino counterpart (write_compression, is_external,
// vacuum_*) is dropped.
var (
	hiveCTASProperties = map[string]string{
		"format": "format", "external_location": "external_location", "partitioned_by": "partitioned_by",
		"bucketed_by": "bucketed_by", "bucket_count": "bucket_count", "field_delimiter": "textfile_field_separator",
	}
	icebergCTASProperties = map[string]string{
		"format": "format", "location": "location", "external_location": "location", "partitioning": "partitioning",
	}
)

// ctasTarget decides a CTAS's catalog and its Trino table properties.
func ctasTarget(props []ctasProperty, tablesLocation string) (catalog string, with []string, err error) {
	catalog, names, locationKey := hiveCatalog, hiveCTASProperties, "external_location"
	if i := slices.IndexFunc(props, func(p ctasProperty) bool { return p.Key == "table_type" }); i >= 0 &&
		strings.EqualFold(strings.Trim(props[i].Value, `'`), "ICEBERG") {
		catalog, names, locationKey = icebergCatalog, icebergCTASProperties, "location"
	}
	hasFormat, hasLocation := false, false
	for _, prop := range props {
		name, ok := names[prop.Key]
		if !ok {
			continue
		}
		value := prop.Value
		switch name {
		case "format":
			hasFormat, value = true, strings.ToUpper(value)
		case locationKey:
			hasLocation = true
		}
		with = append(with, name+" = "+value)
	}
	if !hasFormat {
		with = append(with, "format = 'PARQUET'")
	}
	if !hasLocation {
		if tablesLocation == "" {
			return "", nil, &statementError{errorType: errorTypeUser, msg: "CREATE TABLE AS needs " + locationKey + " when the workgroup keeps no result location."}
		}
		with = append(with, locationKey+" = "+quoteString(tablesLocation))
	}
	return catalog, with, nil
}
