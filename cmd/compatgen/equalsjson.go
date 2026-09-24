//go:build dev

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// The `equalsJSON` check (compat/model/README.md § Assertions): a string
// member holding a JSON document, compared by value rather than by text.
//
// IAM sends its five policyDocumentType members as percent-encoded JSON text;
// botocore's json_decode_policies decodes and parses them, so python-sdk and cli
// hold an object where go-sdk, java-sdk, dotnet-sdk, node-js-sdk and rust-sdk
// hold the string. `equals` is strict JSON-type equality and cannot hold in
// both, so the document check is its own kind. What the generator owns is the
// operand's grammar and where the check may go; the comparison itself is each
// backend's, pinned by compat/model/testdata/equalsjson/equalsjson.json.

// validateEqualsJSON holds an operand to the grammar every backend reads: a
// literal JSON object or array, with no `$`-prefixed key anywhere inside it.
//
// It is an object or an array because that is what a document is, and because
// a string operand would be ambiguous — the JSON text of a document, or a
// document that is itself a string — in exactly the way the check exists to
// remove. It is never evaluated, so an object whose key starts with `$` would
// be read as an expression by a reader that forgot that and as a literal by
// one that remembered; no policy grammar spells a key that way, so it is
// refused rather than read either way.
func validateEqualsJSON(v any, where string) error {
	switch v.(type) {
	case map[string]any, []any:
	default:
		return fmt.Errorf("%s: equalsJSON takes a JSON object or array, the document itself, not %s", where, jsonKind(v))
	}
	return walkEqualsJSON(v, where)
}

func walkEqualsJSON(v any, where string) error {
	switch x := v.(type) {
	case map[string]any:
		for _, k := range sortedValueKeys(x) {
			if strings.HasPrefix(k, "$") {
				return fmt.Errorf("%s: equalsJSON's document is a literal and is never evaluated, so it may not hold a key starting with $ (%q)", where, k)
			}
			if err := walkEqualsJSON(x[k], where); err != nil {
				return err
			}
		}
	case []any:
		for _, e := range x {
			if err := walkEqualsJSON(e, where); err != nil {
				return err
			}
		}
	}
	return nil
}

// jsonKind names a decoded JSON value's type for an error message.
func jsonKind(v any) string {
	switch v.(type) {
	case nil:
		return "null"
	case bool:
		return "a boolean"
	case json.Number, float64:
		return "a number"
	case string:
		return "a string"
	case map[string]any:
		return "an object"
	case []any:
		return "an array"
	}
	return fmt.Sprintf("%T", v)
}

// checkEqualsJSONTarget refuses the check on a member that is not a string.
// The check is author-asserted: nothing in the model says a string holds a
// document (IAM's policyDocumentType is a plain string with a pattern), so a
// human says so by writing it. What the model can say is that a member is not
// a string at all, and a document check on a number, a list or a structure is
// a mistake rather than a comparison.
func checkEqualsJSONTarget(model *serviceModel, target, where string) error {
	if kind := model.Kind(target); kind != "string" {
		return fmt.Errorf("%s: equalsJSON compares a string member holding a JSON document, and %s is %s", where, target, kind)
	}
	return nil
}

// equalsJSONText is the operand as compact JSON text, which is how the four
// typed emitters hand it to their runtimes: object members in sorted order,
// numbers exactly as the scenario spells them, and no HTML escaping, so that
// the text is the same on every run.
func equalsJSONText(v any) (string, error) {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(v); err != nil {
		return "", err
	}
	return strings.TrimSuffix(buffer.String(), "\n"), nil
}
