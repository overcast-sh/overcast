package athena

import (
	"cmp"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"maps"
	"math"
	"slices"
	"strconv"
	"strings"
)

// trino_types.go — Trino's result columns and values in Athena's terms.
//
// Athena engine version 3 is Trino, so a column's Type is Trino's base type
// name ("varchar", "integer", "decimal", "array", …) and every value is
// rendered as text the way Athena's GetQueryResults renders it: numbers as
// written, arrays as [a, b], maps and rows as {k=v, …}.

// resultCatalogName is the CatalogName Athena reports in every ColumnInfo,
// whatever catalog the query read: the name of its Hive connector.
const resultCatalogName = "hive"

// typePrecision is the Precision Athena reports for a type that has no
// parameter to carry one: the most decimal digits the type holds.
var typePrecision = map[string]int32{
	"tinyint": 3, "smallint": 5, "integer": 10, "bigint": 19,
	"real": 17, "double": 17,
}

// caseSensitiveTypes are the types whose values compare case-sensitively.
var caseSensitiveTypes = map[string]bool{"varchar": true, "char": true, "varbinary": true, "json": true}

// columnInfo maps one Trino result column to Athena's ColumnInfo.
func columnInfo(c trinoColumn) ColumnInfo {
	sig := c.TypeSignature
	if sig.RawType == "" {
		sig.RawType = baseTypeName(c.Type)
	}
	info := ColumnInfo{
		CatalogName:   resultCatalogName,
		Name:          c.Name,
		Label:         c.Name,
		Type:          sig.RawType,
		Nullable:      "UNKNOWN",
		CaseSensitive: caseSensitiveTypes[sig.RawType],
		Precision:     typePrecision[sig.RawType],
	}
	longs := sig.longArguments()
	switch {
	case sig.RawType == "decimal" && len(longs) == 2:
		info.Precision, info.Scale = int32(longs[0]), int32(longs[1])
	case len(longs) == 1:
		// varchar(n), char(n), timestamp(p), time(p): the one parameter.
		info.Precision = int32(min(longs[0], math.MaxInt32))
	}
	return info
}

// baseTypeName is a rendered type's name without its parameters:
// "decimal(10,2)" is "decimal".
func baseTypeName(t string) string {
	if i := strings.IndexByte(t, '('); i >= 0 {
		return t[:i]
	}
	return t
}

// longArguments are the signature's numeric parameters, in order.
func (s trinoTypeSignature) longArguments() []int64 {
	var out []int64
	for _, a := range s.Arguments {
		var n int64
		if a.Kind == "LONG" && json.Unmarshal(a.Value, &n) == nil {
			out = append(out, n)
		}
	}
	return out
}

// typeArgument decodes the i-th TYPE argument: an array's element type, or a
// map's key (0) or value (1) type.
func (s trinoTypeSignature) typeArgument(i int) trinoTypeSignature {
	var out trinoTypeSignature
	if i < len(s.Arguments) {
		_ = json.Unmarshal(s.Arguments[i].Value, &out)
	}
	return out
}

// rowFields decodes a row type's named fields.
func (s trinoTypeSignature) rowFields() []trinoNamedType {
	out := make([]trinoNamedType, 0, len(s.Arguments))
	for _, a := range s.Arguments {
		var f trinoNamedType
		if json.Unmarshal(a.Value, &f) == nil {
			out = append(out, f)
		}
	}
	return out
}

// formatValue renders one value as Athena's VarCharValue. A nil value is a
// SQL NULL, which Athena leaves out of the Datum altogether.
func formatValue(v any, sig trinoTypeSignature) *string {
	if v == nil {
		return nil
	}
	s := renderValue(v, sig)
	return &s
}

func renderValue(v any, sig trinoTypeSignature) string {
	switch val := v.(type) {
	case nil:
		return "null"
	case string:
		if sig.RawType == "varbinary" {
			return hexBytes(val)
		}
		return val
	case json.Number:
		return val.String()
	case bool:
		if val {
			return "true"
		}
		return "false"
	case []any:
		if sig.RawType == "row" {
			return renderRow(val, sig.rowFields())
		}
		elem := sig.typeArgument(0)
		parts := make([]string, len(val))
		for i, e := range val {
			parts[i] = renderValue(e, elem)
		}
		return "[" + strings.Join(parts, ", ") + "]"
	case map[string]any:
		key, value := sig.typeArgument(0), sig.typeArgument(1)
		parts := make([]string, 0, len(val))
		for _, k := range sortedKeys(val, key) {
			parts = append(parts, renderValue(k, key)+"="+renderValue(val[k], value))
		}
		return "{" + strings.Join(parts, ", ") + "}"
	default:
		return fmt.Sprint(val)
	}
}

// hexBytes renders Trino's base64 varbinary as Athena does: each byte as
// two lower-case hex digits, separated by spaces.
func hexBytes(b64 string) string {
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return b64
	}
	parts := make([]string, len(raw))
	for i, c := range raw {
		parts[i] = fmt.Sprintf("%02x", c)
	}
	return strings.Join(parts, " ")
}

// sortedKeys orders a map's keys: numerically when the key type is a
// number, as text otherwise.
func sortedKeys(m map[string]any, key trinoTypeSignature) []string {
	keys := slices.Sorted(maps.Keys(m))
	if !numericTypes[key.RawType] {
		return keys
	}
	slices.SortStableFunc(keys, func(a, b string) int {
		x, errA := strconv.ParseFloat(a, 64)
		y, errB := strconv.ParseFloat(b, 64)
		if errA != nil || errB != nil {
			return strings.Compare(a, b)
		}
		return cmp.Compare(x, y)
	})
	return keys
}

// numericTypes are the Trino types whose values order as numbers.
var numericTypes = map[string]bool{
	"tinyint": true, "smallint": true, "integer": true, "bigint": true, "real": true, "double": true, "decimal": true,
}

// renderRow renders a row as {field=value, …}, the fields named when the
// type says what they are called.
func renderRow(values []any, fields []trinoNamedType) string {
	parts := make([]string, len(values))
	for i, v := range values {
		var sig trinoTypeSignature
		name := fmt.Sprintf("field%d", i)
		if i < len(fields) {
			sig = fields[i].TypeSignature
			if fields[i].FieldName != nil {
				name = fields[i].FieldName.Name
			}
		}
		parts[i] = name + "=" + renderValue(v, sig)
	}
	return "{" + strings.Join(parts, ", ") + "}"
}
