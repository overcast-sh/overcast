package athena

import (
	"context"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"

	"github.com/overcast-sh/overcast/internal/protocol"
	"github.com/overcast-sh/overcast/internal/services/glue"
)

// ddl_show.go — SHOW and DESCRIBE, answered from the catalog. Their results
// are UTILITY output: string columns, no header row.

// textColumns describes string result columns.
func textColumns(names ...string) []ColumnInfo {
	cols := make([]ColumnInfo, len(names))
	for i, n := range names {
		cols[i] = ColumnInfo{CatalogName: resultCatalogName, Name: n, Label: n, Type: "string", Nullable: "UNKNOWN"}
	}
	return cols
}

func textRow(cells ...string) []*string {
	row := make([]*string, len(cells))
	for i := range cells {
		row[i] = &cells[i]
	}
	return row
}

// textResult is a one-column result holding rows.
func textResult(column string, rows ...string) *queryResult {
	res := &queryResult{Columns: textColumns(column), Rows: make([][]*string, len(rows))}
	for i, r := range rows {
		res.Rows[i] = textRow(r)
	}
	return res
}

// hivePattern compiles a SHOW pattern as Hive reads one: a regular
// expression in which a '*' not following a '.' matches anything and '|'
// separates alternatives, case-insensitively. An empty pattern matches all,
// and one that does not compile matches nothing.
func hivePattern(pattern string) *regexp.Regexp {
	if pattern == "" {
		return nil
	}
	alts := strings.Split(pattern, "|")
	for i, a := range alts {
		alts[i] = loneStar.ReplaceAllString(strings.TrimSpace(a), "$1.*")
	}
	re, err := regexp.Compile("(?i)^(?:" + strings.Join(alts, "|") + ")$")
	if err != nil {
		return matchNothing
	}
	return re
}

var (
	// loneStar is a '*' that is not already a regular expression's '.*'.
	loneStar = regexp.MustCompile(`(^|[^.])\*`)
	// matchNothing is the pattern a malformed one becomes.
	matchNothing = regexp.MustCompile(`a^`)
)

// matching returns the names re matches, sorted.
func matching(names []string, re *regexp.Regexp) []string {
	out := names[:0]
	for _, n := range names {
		if re == nil || re.MatchString(n) {
			out = append(out, n)
		}
	}
	slices.Sort(out)
	return out
}

func (s *showDatabasesStmt) run(ctx context.Context, env ddlEnv) (*queryResult, *queryFailure) {
	dbs, err := env.catalog.ListDatabases(ctx)
	if err != nil {
		return nil, catalogFailure(protocol.Wrap(protocol.ErrInternalError, err))
	}
	names := make([]string, len(dbs))
	for i, db := range dbs {
		names[i] = db.Name
	}
	return textResult("database_name", matching(names, hivePattern(s.Pattern))...), nil
}

func (s *showTablesStmt) run(ctx context.Context, env ddlEnv) (*queryResult, *queryFailure) {
	database := s.Database
	if database == "" {
		database = env.database
	}
	if _, found, err := env.catalog.GetDatabase(ctx, database); err != nil {
		return nil, catalogFailure(protocol.Wrap(protocol.ErrInternalError, err))
	} else if !found {
		return nil, failure(errorCategoryUser, errorTypeNotFound, "FAILED: SemanticException [Error 10072]: Database does not exist: "+database)
	}
	tables, err := env.catalog.ListTables(ctx, database)
	if err != nil {
		return nil, catalogFailure(protocol.Wrap(protocol.ErrInternalError, err))
	}
	names := make([]string, len(tables))
	for i, t := range tables {
		names[i] = t.Name
	}
	return textResult("tab_name", matching(names, hivePattern(s.Pattern))...), nil
}

// allColumns are a table's columns followed by its partition keys.
func allColumns(t glue.Table) []glue.Column {
	var cols []glue.Column
	if t.StorageDescriptor != nil {
		cols = append(cols, t.StorageDescriptor.Columns...)
	}
	return append(cols, t.PartitionKeys...)
}

func (s *showColumnsStmt) run(ctx context.Context, env ddlEnv) (*queryResult, *queryFailure) {
	t, fail := env.requireTable(ctx, env.resolve(s.Table))
	if fail != nil {
		return nil, fail
	}
	cols := allColumns(t)
	names := make([]string, len(cols))
	for i, c := range cols {
		names[i] = c.Name
	}
	return textResult("field", names...), nil
}

func (s *showTablePropertiesStmt) run(ctx context.Context, env ddlEnv) (*queryResult, *queryFailure) {
	t, fail := env.requireTable(ctx, env.resolve(s.Table))
	if fail != nil {
		return nil, fail
	}
	res := &queryResult{Columns: textColumns("prpt_name", "prpt_value")}
	for _, k := range slices.Sorted(maps.Keys(t.Parameters)) {
		if s.Property == "" || s.Property == k {
			res.Rows = append(res.Rows, textRow(k, t.Parameters[k]))
		}
	}
	return res, nil
}

// describeWidth is the width Hive pads DESCRIBE's name and type columns to.
const describeWidth = 20

func (s *describeStmt) run(ctx context.Context, env ddlEnv) (*queryResult, *queryFailure) {
	t, fail := env.requireTable(ctx, env.resolve(s.Table))
	if fail != nil {
		return nil, fail
	}
	res := &queryResult{Columns: textColumns("col_name", "data_type", "comment")}
	for _, c := range allColumns(t) {
		res.Rows = append(res.Rows, describeRow(c.Name, c.Type, c.Comment))
	}
	if len(t.PartitionKeys) > 0 {
		res.Rows = append(res.Rows, textRow("", "", ""), describeRow("# Partition Information", "", ""),
			describeRow("# col_name", "data_type", "comment"), textRow("", "", ""))
		for _, c := range t.PartitionKeys {
			res.Rows = append(res.Rows, describeRow(c.Name, c.Type, c.Comment))
		}
	}
	return res, nil
}

func describeRow(name, typ, comment string) []*string {
	return textRow(fmt.Sprintf("%-*s", describeWidth, name), fmt.Sprintf("%-*s", describeWidth, typ), comment)
}

// ─── Grammar ───────────────────────────────────────────────────

// showDatabasesStmt is SHOW (DATABASES|SCHEMAS) [LIKE 'pattern'].
type showDatabasesStmt struct{ Pattern string }

func parseShowDatabases(p *ddlParser) (ddlStatement, error) {
	s := &showDatabasesStmt{}
	if p.accept("LIKE") {
		var err error
		if s.Pattern, err = p.str(); err != nil {
			return nil, err
		}
	}
	return s, p.end()
}

// showTablesStmt is SHOW TABLES [IN database] ['pattern'].
type showTablesStmt struct{ Database, Pattern string }

func parseShowTables(p *ddlParser) (ddlStatement, error) {
	s := &showTablesStmt{}
	var err error
	if p.accept("IN") || p.accept("FROM") {
		if s.Database, err = p.name(); err != nil {
			return nil, err
		}
	}
	if p.accept("LIKE") || p.peek().kind == tokString {
		if s.Pattern, err = p.str(); err != nil {
			return nil, err
		}
	}
	return s, p.end()
}

// showPartitionsStmt is SHOW PARTITIONS name.
type showPartitionsStmt struct{ Table tableRef }

func (s *showPartitionsStmt) target() tableRef { return s.Table }

func parseShowPartitions(p *ddlParser) (ddlStatement, error) {
	table, err := p.tableName()
	if err != nil {
		return nil, err
	}
	return &showPartitionsStmt{Table: table}, p.end()
}

// showColumnsStmt is SHOW COLUMNS (IN|FROM) name [(IN|FROM) database].
type showColumnsStmt struct{ Table tableRef }

func (s *showColumnsStmt) target() tableRef { return s.Table }

func parseShowColumns(p *ddlParser) (ddlStatement, error) {
	if !p.accept("IN") && !p.accept("FROM") {
		return nil, p.fail("expected IN or FROM")
	}
	table, err := p.tableName()
	if err != nil {
		return nil, err
	}
	if p.accept("IN") || p.accept("FROM") {
		if table.Database, err = p.name(); err != nil {
			return nil, err
		}
	}
	return &showColumnsStmt{Table: table}, p.end()
}

// showTablePropertiesStmt is SHOW TBLPROPERTIES name [('property')].
type showTablePropertiesStmt struct {
	Table    tableRef
	Property string
}

func (s *showTablePropertiesStmt) target() tableRef { return s.Table }

func parseShowTableProperties(p *ddlParser) (ddlStatement, error) {
	table, err := p.tableName()
	if err != nil {
		return nil, err
	}
	s := &showTablePropertiesStmt{Table: table}
	if p.acceptSymbol("(") {
		if s.Property, err = p.str(); err != nil {
			return nil, err
		}
		if err := p.expectSymbol(")"); err != nil {
			return nil, err
		}
	}
	return s, p.end()
}

// describeStmt is DESCRIBE [EXTENDED|FORMATTED] name. DESCRIBE INPUT and
// DESCRIBE OUTPUT, which describe a prepared statement, are the engine's.
type describeStmt struct{ Table tableRef }

func (s *describeStmt) target() tableRef { return s.Table }

func parseDescribe(p *ddlParser) (ddlStatement, error) {
	if p.peek().is("INPUT") || p.peek().is("OUTPUT") {
		return nil, nil
	}
	if !p.accept("EXTENDED") {
		p.accept("FORMATTED")
	}
	table, err := p.tableName()
	if err != nil {
		return nil, err
	}
	return &describeStmt{Table: table}, p.end()
}
