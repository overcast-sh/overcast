package athena

import (
	"fmt"
	"strings"
)

// hive_types.go — a Hive column type, as Athena's DDL writes it, in Trino's
// spelling: string is varchar, struct<a:int> is row(a integer), and so on.
// Used where Overcast hands a DDL statement to the engine (Iceberg CREATE
// TABLE); the catalog itself keeps Hive's spelling, as Athena's does.

// hiveScalarTypes are the Hive type names Trino spells differently.
var hiveScalarTypes = map[string]string{
	"string": "varchar", "int": "integer", "float": "real",
	"binary": "varbinary", "timestamp": "timestamp(6)", "decimal": "decimal(10,0)",
}

// trinoType converts one Hive type.
func trinoType(hive string) (string, error) {
	c := &typeCursor{s: strings.ToLower(strings.TrimSpace(hive))}
	out, err := c.parse()
	if err == nil && c.i < len(c.s) {
		err = fmt.Errorf("unexpected %q", c.s[c.i:])
	}
	if err != nil {
		return "", fmt.Errorf("unsupported column type %q: %w", hive, err)
	}
	return out, nil
}

type typeCursor struct {
	s string
	i int
}

func (c *typeCursor) skipSpace() {
	for c.i < len(c.s) && c.s[c.i] == ' ' {
		c.i++
	}
}

// word reads a type or field name.
func (c *typeCursor) word() string {
	c.skipSpace()
	start := c.i
	for c.i < len(c.s) && isWordByte(c.s[c.i]) {
		c.i++
	}
	return c.s[start:c.i]
}

func (c *typeCursor) consume(b byte) bool {
	c.skipSpace()
	if c.i < len(c.s) && c.s[c.i] == b {
		c.i++
		return true
	}
	return false
}

func (c *typeCursor) expect(b byte) error {
	if !c.consume(b) {
		return fmt.Errorf("expected %q", b)
	}
	return nil
}

func (c *typeCursor) parse() (string, error) {
	name := c.word()
	switch name {
	case "":
		return "", fmt.Errorf("expected a type")
	case "array":
		return c.generic("array", 1)
	case "map":
		return c.generic("map", 2)
	case "struct":
		return c.row()
	}
	if c.consume('(') { // decimal(p,s), varchar(n), char(n)
		end := strings.IndexByte(c.s[c.i:], ')')
		if end < 0 {
			return "", fmt.Errorf("unclosed (")
		}
		args := strings.ReplaceAll(c.s[c.i:c.i+end], " ", "")
		c.i += end + 1
		return name + "(" + args + ")", nil
	}
	if t, ok := hiveScalarTypes[name]; ok {
		return t, nil
	}
	return name, nil
}

// generic reads <t, …> with n element types.
func (c *typeCursor) generic(name string, n int) (string, error) {
	if err := c.expect('<'); err != nil {
		return "", err
	}
	args := make([]string, n)
	for i := range args {
		if i > 0 {
			if err := c.expect(','); err != nil {
				return "", err
			}
		}
		t, err := c.parse()
		if err != nil {
			return "", err
		}
		args[i] = t
	}
	if err := c.expect('>'); err != nil {
		return "", err
	}
	return name + "(" + strings.Join(args, ", ") + ")", nil
}

// row reads struct<name:t, …>.
func (c *typeCursor) row() (string, error) {
	if err := c.expect('<'); err != nil {
		return "", err
	}
	var fields []string
	for {
		name := c.word()
		if err := c.expect(':'); err != nil {
			return "", err
		}
		t, err := c.parse()
		if err != nil {
			return "", err
		}
		fields = append(fields, quoteIdent(name)+" "+t)
		if c.consume('>') {
			return "row(" + strings.Join(fields, ", ") + ")", nil
		}
		if err := c.expect(','); err != nil {
			return "", err
		}
	}
}

// quoteIdent quotes a Trino identifier.
func quoteIdent(name string) string { return `"` + strings.ReplaceAll(name, `"`, `""`) + `"` }

// quoteString quotes a Trino string literal.
func quoteString(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }
