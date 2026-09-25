package athena

import (
	"encoding/json"
	"testing"
)

// sig decodes a type signature written as Trino sends it.
func sig(t *testing.T, raw string) trinoTypeSignature {
	t.Helper()
	var s trinoTypeSignature
	if err := json.Unmarshal([]byte(raw), &s); err != nil {
		t.Fatalf("signature %s: %v", raw, err)
	}
	return s
}

func TestColumnInfo_mapsTrinoTypes(t *testing.T) {
	cases := []struct {
		col              trinoColumn
		typ              string
		precision, scale int32
		caseSensitive    bool
	}{
		{trinoColumn{Name: "name", Type: "varchar", TypeSignature: sig(t, `{"rawType":"varchar","arguments":[{"kind":"LONG","value":2147483647}]}`)}, "varchar", 2147483647, 0, true},
		{trinoColumn{Name: "price", Type: "decimal(10,2)", TypeSignature: sig(t, `{"rawType":"decimal","arguments":[{"kind":"LONG","value":10},{"kind":"LONG","value":2}]}`)}, "decimal", 10, 2, false},
		{trinoColumn{Name: "id", Type: "bigint", TypeSignature: sig(t, `{"rawType":"bigint","arguments":[]}`)}, "bigint", 19, 0, false},
		{trinoColumn{Name: "tags", Type: "array(varchar)", TypeSignature: sig(t, `{"rawType":"array","arguments":[{"kind":"TYPE","value":{"rawType":"varchar","arguments":[]}}]}`)}, "array", 0, 0, false},
		{trinoColumn{Name: "legacy", Type: "timestamp(3)"}, "timestamp", 0, 0, false},
	}
	for _, c := range cases {
		// When: a result column is described
		got := columnInfo(c.col)
		// Then: it carries Athena's type name, precision and scale
		if got.Type != c.typ || got.Precision != c.precision || got.Scale != c.scale || got.CaseSensitive != c.caseSensitive ||
			got.Name != c.col.Name || got.Label != c.col.Name || got.CatalogName != "hive" || got.Nullable != "UNKNOWN" {
			t.Errorf("columnInfo(%s) = %+v", c.col.Type, got)
		}
	}
}

func TestFormatValue_rendersAsAthenaDoes(t *testing.T) {
	arrayOfInt := sig(t, `{"rawType":"array","arguments":[{"kind":"TYPE","value":{"rawType":"integer","arguments":[]}}]}`)
	mapSig := sig(t, `{"rawType":"map","arguments":[{"kind":"TYPE","value":{"rawType":"varchar"}},{"kind":"TYPE","value":{"rawType":"integer"}}]}`)
	rowSig := sig(t, `{"rawType":"row","arguments":[{"kind":"NAMED_TYPE","value":{"fieldName":{"name":"a"},"typeSignature":{"rawType":"integer"}}},{"kind":"NAMED_TYPE","value":{"fieldName":{"name":"b"},"typeSignature":{"rawType":"varchar"}}}]}`)
	cases := []struct {
		v    any
		sig  trinoTypeSignature
		want string
	}{
		{json.Number("12345678901234567890"), trinoTypeSignature{RawType: "decimal"}, "12345678901234567890"},
		{"hello", trinoTypeSignature{RawType: "varchar"}, "hello"},
		{true, trinoTypeSignature{RawType: "boolean"}, "true"},
		{[]any{json.Number("1"), nil}, arrayOfInt, "[1, null]"},
		{map[string]any{"b": json.Number("2"), "a": json.Number("1")}, mapSig, "{a=1, b=2}"},
		{[]any{json.Number("1"), "x"}, rowSig, "{a=1, b=x}"},
	}
	for _, c := range cases {
		if got := formatValue(c.v, c.sig); got == nil || *got != c.want {
			t.Errorf("formatValue(%v) = %v, want %q", c.v, got, c.want)
		}
	}
	// A SQL NULL is absent from the Datum rather than rendered.
	if got := formatValue(nil, trinoTypeSignature{RawType: "varchar"}); got != nil {
		t.Errorf("formatValue(nil) = %q, want no value", *got)
	}
}

func TestAthenaErrorFor_classifiesTrinoErrors(t *testing.T) {
	cases := []struct {
		err                 trinoError
		category, errorType int32
		retryable           bool
	}{
		{trinoError{ErrorName: "TABLE_NOT_FOUND", ErrorType: "USER_ERROR"}, 2, 1110, false},
		{trinoError{ErrorName: "SYNTAX_ERROR", ErrorType: "USER_ERROR"}, 2, 1006, false},
		{trinoError{ErrorName: "SOMETHING_NEW", ErrorType: "USER_ERROR"}, 2, 1000, false},
		{trinoError{ErrorName: "HIVE_METASTORE_ERROR", ErrorType: "EXTERNAL"}, 1, 204, false},
		{trinoError{ErrorName: "ICEBERG_COMMIT_ERROR", ErrorType: "EXTERNAL"}, 1, 233, false},
		{trinoError{ErrorName: "EXCEEDED_LOCAL_MEMORY_LIMIT", ErrorType: "INSUFFICIENT_RESOURCES"}, 1, 0, true},
		{trinoError{ErrorName: "GENERIC_INTERNAL_ERROR", ErrorType: "INTERNAL_ERROR"}, 1, 200, false},
	}
	for _, c := range cases {
		c.err.Message = "boom"
		got := athenaErrorFor(&c.err)
		if got.ErrorCategory != c.category || got.ErrorType != c.errorType || got.Retryable != c.retryable ||
			got.ErrorMessage != c.err.ErrorName+": boom" {
			t.Errorf("athenaErrorFor(%s) = %+v", c.err.ErrorName, got)
		}
	}
}
