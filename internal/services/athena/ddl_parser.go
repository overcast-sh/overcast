package athena

import (
	"fmt"
	"strings"
)

// ddl_parser.go — a recursive-descent reader for the statements Athena runs
// against the Glue Data Catalog itself: Hive DDL that Trino cannot parse.
// Each statement's grammar is in its parse function; the statement itself,
// and what it does to the catalog, is in ddl_statements.go.

// ddlParser walks one statement's tokens.
type ddlParser struct {
	src  string
	toks []token
	pos  int
}

// ddlSyntaxError is a statement Overcast recognised as its own but could not
// read. It fails the query as Athena fails one it cannot parse.
type ddlSyntaxError struct{ msg string }

func (e *ddlSyntaxError) Error() string { return e.msg }

// statementError is a statement Overcast could read but will not run, such
// as EXECUTE of a prepared statement that does not exist.
type statementError struct {
	errorType int32
	msg       string
}

func (e *statementError) Error() string { return e.msg }

// newDDLParser reads src as Hive DDL.
func newDDLParser(src string) (*ddlParser, error) { return newParser(src, dialectHive) }

// newEngineParser reads src as the engine's SQL, with no final semicolon
// or trailing comments: Trino rejects the one and has no use for the other.
func newEngineParser(src string) (*ddlParser, error) {
	p, err := newParser(src, dialectTrino)
	if err != nil {
		return nil, err
	}
	p.trimTerminator()
	return p, nil
}

func newParser(src string, dialect sqlDialect) (*ddlParser, error) {
	toks, err := lexSQL(src, dialect)
	if err != nil {
		return nil, &ddlSyntaxError{msg: err.Error()}
	}
	return &ddlParser{src: src, toks: toks}, nil
}

// trimTerminator cuts the statement after its last token, dropping a final
// semicolon and whatever follows it.
func (p *ddlParser) trimTerminator() {
	n := len(p.toks) - 1 // the EOF
	if n > 0 && p.toks[n-1].isSymbol(";") {
		n--
	}
	end := 0
	if n > 0 {
		end = p.toks[n-1].end
	}
	p.src = p.src[:end]
	p.toks = append(p.toks[:n], token{kind: tokEOF, start: end, end: end})
}

// preparedName reads a prepared statement's name as written: its case kept,
// and with the @ and : that StatementName allows.
func (p *ddlParser) preparedName() (string, error) {
	t := p.peek()
	if t.kind == tokIdent || t.kind == tokString {
		p.pos++
		return t.text, nil
	}
	if t.kind != tokWord {
		return "", p.fail("expected a statement name")
	}
	end := p.next().end
	for n := p.peek(); n.start == end && (n.kind == tokWord || n.kind == tokNumber || n.isSymbol("@") || n.isSymbol(":")); n = p.peek() {
		end = p.next().end
	}
	return p.src[t.start:end], nil
}

func (p *ddlParser) peek() token { return p.toks[p.pos] }

func (p *ddlParser) peekAt(n int) token { return p.toks[min(p.pos+n, len(p.toks)-1)] }

func (p *ddlParser) next() token {
	t := p.toks[p.pos]
	if t.kind != tokEOF {
		p.pos++
	}
	return t
}

func (p *ddlParser) fail(format string, args ...any) error {
	t := p.peek()
	near := "end of statement"
	if t.kind != tokEOF {
		near = fmt.Sprintf("%q", p.src[t.start:t.end])
	}
	return &ddlSyntaxError{msg: fmt.Sprintf("line 1:%d: %s near %s", t.start+1, fmt.Sprintf(format, args...), near)}
}

// accept consumes the keywords kws if they come next, in order.
func (p *ddlParser) accept(kws ...string) bool {
	for i, kw := range kws {
		if !p.peekAt(i).is(kw) {
			return false
		}
	}
	p.pos += len(kws)
	return true
}

func (p *ddlParser) expect(kws ...string) error {
	if !p.accept(kws...) {
		return p.fail("expected %s", strings.Join(kws, " "))
	}
	return nil
}

func (p *ddlParser) acceptSymbol(s string) bool {
	if p.peek().isSymbol(s) {
		p.pos++
		return true
	}
	return false
}

func (p *ddlParser) expectSymbol(s string) error {
	if !p.acceptSymbol(s) {
		return p.fail("expected %q", s)
	}
	return nil
}

// end requires the statement to be over, allowing one trailing semicolon.
func (p *ddlParser) end() error {
	p.acceptSymbol(";")
	if p.peek().kind != tokEOF {
		return p.fail("unexpected input")
	}
	return nil
}

// name reads an identifier, bare or `quoted`. Glue folds names to lower
// case, so the result is lower case.
func (p *ddlParser) name() (string, error) {
	t := p.peek()
	if t.kind != tokWord && t.kind != tokIdent && t.kind != tokString {
		return "", p.fail("expected a name")
	}
	p.pos++
	return strings.ToLower(t.text), nil
}

// tableName reads [catalog.][database.]table.
func (p *ddlParser) tableName() (tableRef, error) {
	parts := []string{}
	for {
		n, err := p.name()
		if err != nil {
			return tableRef{}, err
		}
		parts = append(parts, n)
		if !p.acceptSymbol(".") {
			break
		}
	}
	switch len(parts) {
	case 1:
		return tableRef{Table: parts[0]}, nil
	case 2:
		return tableRef{Database: parts[0], Table: parts[1]}, nil
	case 3:
		return tableRef{Catalog: parts[0], Database: parts[1], Table: parts[2]}, nil
	}
	return tableRef{}, p.fail("too many name parts")
}

// str reads a string literal.
func (p *ddlParser) str() (string, error) {
	t := p.peek()
	if t.kind != tokString {
		return "", p.fail("expected a string")
	}
	p.pos++
	return t.text, nil
}

// properties reads ('key'='value', …).
func (p *ddlParser) properties() (map[string]string, error) {
	if err := p.expectSymbol("("); err != nil {
		return nil, err
	}
	props := map[string]string{}
	for !p.acceptSymbol(")") {
		k, err := p.str()
		if err != nil {
			return nil, err
		}
		if err := p.expectSymbol("="); err != nil {
			return nil, err
		}
		v, err := p.str()
		if err != nil {
			return nil, err
		}
		props[k] = v
		if !p.acceptSymbol(",") && !p.peek().isSymbol(")") {
			return nil, p.fail("expected \",\" or \")\"")
		}
	}
	return props, nil
}

// rawUntil returns the source text from the current token up to, not
// including, the first top-level token stop accepts, balancing (), <> and [].
// It is how a column type such as array<struct<a:int>> is read: as written.
func (p *ddlParser) rawUntil(stop func(token) bool) (string, error) {
	start, depth := p.peek().start, 0
	for {
		t := p.peek()
		if t.kind == tokEOF || depth == 0 && stop(t) {
			break
		}
		switch {
		case t.isSymbol("(") || t.isSymbol("<") || t.isSymbol("["):
			depth++
		case t.isSymbol(")") || t.isSymbol(">") || t.isSymbol("]"):
			depth--
		}
		p.pos++
	}
	raw := strings.TrimSpace(p.src[start:p.peek().start])
	if raw == "" {
		return "", p.fail("expected a type")
	}
	return raw, nil
}

// columns reads (name type [COMMENT 'c'], …).
func (p *ddlParser) columns() ([]columnDef, error) {
	if err := p.expectSymbol("("); err != nil {
		return nil, err
	}
	var cols []columnDef
	for {
		n, err := p.name()
		if err != nil {
			return nil, err
		}
		typ, err := p.rawUntil(func(t token) bool { return t.isSymbol(",") || t.isSymbol(")") || t.is("COMMENT") })
		if err != nil {
			return nil, err
		}
		col := columnDef{Name: n, Type: strings.ToLower(typ)}
		if p.accept("COMMENT") {
			if col.Comment, err = p.str(); err != nil {
				return nil, err
			}
		}
		cols = append(cols, col)
		if p.acceptSymbol(")") {
			return cols, nil
		}
		if err := p.expectSymbol(","); err != nil {
			return nil, err
		}
	}
}

// rawList reads (item, …), each item as written.
func (p *ddlParser) rawList() ([]string, error) {
	if err := p.expectSymbol("("); err != nil {
		return nil, err
	}
	var items []string
	for {
		item, err := p.rawUntil(func(t token) bool { return t.isSymbol(",") || t.isSymbol(")") })
		if err != nil {
			return nil, err
		}
		items = append(items, item)
		if p.acceptSymbol(")") {
			return items, nil
		}
		if err := p.expectSymbol(","); err != nil {
			return nil, err
		}
	}
}

// nameList reads (name, …).
func (p *ddlParser) nameList() ([]string, error) {
	if err := p.expectSymbol("("); err != nil {
		return nil, err
	}
	var names []string
	for {
		n, err := p.name()
		if err != nil {
			return nil, err
		}
		names = append(names, n)
		if p.acceptSymbol(")") {
			return names, nil
		}
		if err := p.expectSymbol(","); err != nil {
			return nil, err
		}
	}
}

// ifNotExists and ifExists read the optional guards.
func (p *ddlParser) ifNotExists() bool { return p.accept("IF", "NOT", "EXISTS") }
func (p *ddlParser) ifExists() bool    { return p.accept("IF", "EXISTS") }

// parseDDL reads a statement Athena runs against the catalog itself. It
// returns nil for any other statement, which the engine runs; an error means
// the statement is one of these and is malformed.
func parseDDL(sql string) (ddlStatement, error) {
	p, err := newDDLParser(sql)
	if err != nil { // not Hive's lexically: the engine's to judge
		return nil, nil
	}
	for _, form := range ddlForms {
		if p.accept(form.keywords...) {
			return form.parse(p)
		}
	}
	return nil, nil
}

// ddlForm is one statement's leading keywords and the parser for the rest.
type ddlForm struct {
	keywords []string
	parse    func(*ddlParser) (ddlStatement, error)
}

// ddlForms are tried in order, so a longer lead comes before a shorter one
// it starts with.
var ddlForms = []ddlForm{
	{[]string{"CREATE", "EXTERNAL", "TABLE"}, parseCreateExternalTable},
	{[]string{"CREATE", "DATABASE"}, parseCreateDatabase},
	{[]string{"CREATE", "SCHEMA"}, parseCreateDatabase},
	{[]string{"DROP", "DATABASE"}, parseDropDatabase},
	{[]string{"DROP", "SCHEMA"}, parseDropDatabase},
	{[]string{"DROP", "TABLE"}, parseDropTable},
	{[]string{"MSCK", "REPAIR", "TABLE"}, parseRepairTable},
	{[]string{"SHOW", "DATABASES"}, parseShowDatabases},
	{[]string{"SHOW", "SCHEMAS"}, parseShowDatabases},
	{[]string{"SHOW", "TABLES"}, parseShowTables},
	{[]string{"SHOW", "PARTITIONS"}, parseShowPartitions},
	{[]string{"SHOW", "COLUMNS"}, parseShowColumns},
	{[]string{"SHOW", "TBLPROPERTIES"}, parseShowTableProperties},
	{[]string{"DESCRIBE"}, parseDescribe},
	{[]string{"DESC"}, parseDescribe},
	{[]string{"ALTER", "TABLE"}, parseAlterTable},
}
