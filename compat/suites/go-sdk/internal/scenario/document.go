package scenario

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"
	"unicode"
)

// An AWS SDK response, as one of the IR's documents.
//
// This is the one direction that still needs a conversion, and the only place
// in this package that reflects. It has to: a response is an arbitrary SDK
// output struct and the assertions walk it by path, so nothing is known about
// its shape until it arrives. The other direction — a value into an input
// field — needs no conversion any more: cmd/compatgen resolves each field's
// type from the vendored SDK and writes the spelling into the emitted source,
// so only a deferred expression reaches run time, through Bind in binder.go.
//
// Every rule the IR states about a response — a path resolves or it does not,
// an absent list reads like an empty one, `equals` compares in the JSON type
// system — is stated over JSON. The three interpreters get that for free: they
// hold the parsed response. A typed SDK hands us a struct of pointers, enums
// and slices instead, so one conversion stands between the two, and its
// choices are the ones that make this suite agree with the others.
//
// Two of those choices are load-bearing:
//
//   - **nil is absence, not null.** The Go SDK deserializes an omitted member
//     and a JSON null to the same nil pointer, and compat/model/README.md
//     § Paths settles which of the two that is: "`undefined` in an SDK's object
//     model is absence, not a value". So a nil field is left out of the
//     document rather than written as null. Encoding the struct with
//     encoding/json instead would write `"NextToken": null` for every unset
//     member, which resolves — and would fail `missing` on an absent token and
//     `isList` on an omitted page, both of which are correct AWS answers.
//   - **every number becomes a float64.** That is what encoding/json produces
//     on the interpreters' side, so an `equals` on an int32 member compares
//     the same way here as it does there.

// toDocument converts an SDK value to the IR's document form: map[string]any,
// []any, string, float64, bool, or nil.
//
// present is false when the value is absent — a nil pointer, slice, map or
// interface — and the caller then omits the member entirely.
func toDocument(v any) (doc any, present bool) {
	if v == nil {
		return nil, false
	}
	return fromValue(reflect.ValueOf(v))
}

// resultMetadataField is the one member of every SDK output struct that is not
// part of the modeled response: middleware.Metadata, whose contents are the
// SDK's own bookkeeping. It carries no exported fields, so it would render as
// an empty object shadowing nothing — but a scenario path can only ever mean
// modeled members, so it is dropped rather than surfaced.
const resultMetadataField = "ResultMetadata"

func fromValue(rv reflect.Value) (any, bool) {
	// The kinds not named here are handled by the second switch below, which
	// ends in a catch-all: this one only peels off absence and indirection.
	//exhaustive:ignore
	switch rv.Kind() {
	case reflect.Invalid:
		return nil, false
	case reflect.Pointer, reflect.Interface:
		if rv.IsNil() {
			return nil, false
		}
		return fromValue(rv.Elem())
	}

	// A timestamp is never compared by the IR (compat/model/README.md
	// § Assertions), but it can sit on a response a path walks past, so it is
	// rendered rather than dropped.
	if t, ok := rv.Interface().(time.Time); ok {
		return t.UTC().Format(time.RFC3339Nano), true
	}

	// Every kind an SDK response can hold is named; anything else falls through
	// to the catch-all after the switch rather than being enumerated here.
	//exhaustive:ignore
	switch rv.Kind() {
	case reflect.String:
		return rv.String(), true
	case reflect.Bool:
		return rv.Bool(), true
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return float64(rv.Int()), true
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return float64(rv.Uint()), true
	case reflect.Float32, reflect.Float64:
		return rv.Float(), true
	case reflect.Slice, reflect.Array:
		if rv.Kind() == reflect.Slice && rv.IsNil() {
			return nil, false
		}
		// A blob is []byte, and its document form in every backend is its
		// standard base64 text (compat/model/README.md § Values): what an
		// `equals` against a `$base64` compares, and what an export of a blob
		// puts in the context bag for a later `$base64` around a $ref to decode.
		if rv.Type().Elem().Kind() == reflect.Uint8 && rv.Type().Elem().PkgPath() == "" {
			return base64.StdEncoding.EncodeToString(rv.Bytes()), true
		}
		out := make([]any, 0, rv.Len())
		for i := 0; i < rv.Len(); i++ {
			item, ok := fromValue(rv.Index(i))
			if !ok {
				// A nil element of a list is a null the service sent, not an
				// absent member: dropping it would renumber every index after
				// it, which a path can address.
				item = nil
			}
			out = append(out, item)
		}
		return out, true
	case reflect.Map:
		if rv.IsNil() {
			return nil, false
		}
		out := make(map[string]any, rv.Len())
		for _, key := range rv.MapKeys() {
			name, ok := mapKeyString(key)
			if !ok {
				continue
			}
			if value, ok := fromValue(rv.MapIndex(key)); ok {
				out[name] = value
			}
		}
		return out, true
	case reflect.Struct:
		out := make(map[string]any)
		rt := rv.Type()
		for i := 0; i < rt.NumField(); i++ {
			field := rt.Field(i)
			if !field.IsExported() || field.Name == resultMetadataField {
				continue
			}
			if value, ok := fromValue(rv.Field(i)); ok {
				out[field.Name] = value
			}
		}
		return out, true
	}
	return fmt.Sprint(rv.Interface()), true
}

// exportedName is the Go field smithy-go generates for a modeled member: the
// member name with its first letter capitalized. Almost every AWS member is
// already PascalCase, but not all — SQS models
// ListDeadLetterSourceQueues' page as `queueUrls` and CreateQueue's tags as
// `tags`, and the Go SDK spells both with a capital.
//
// This is the one place the Go object model and the IR's member names differ,
// and it is bridged here rather than by rewriting paths at emit time: a
// failure message has to quote the path the scenario file writes, or field 4
// stops matching the other backends'.
//
// smithy-go's own rule is capitalization plus reserved-word handling; only the
// capitalization is reproduced, because a member that needed the second half
// would not resolve and would fail the check that names it rather than passing
// wrongly.
func exportedName(member string) string {
	if member == "" {
		return member
	}
	r := []rune(member)
	if !unicode.IsLower(r[0]) {
		return member
	}
	r[0] = unicode.ToUpper(r[0])
	return string(r)
}

// mapKeyString renders a map key. SDK map keys are strings or named string
// types (an enum-keyed attribute map); anything else has no place in a
// document and is dropped rather than stringified into a member name a path
// could accidentally address.
func mapKeyString(key reflect.Value) (string, bool) {
	if key.Kind() == reflect.String {
		return key.String(), true
	}
	return "", false
}

// ---------------------------------------------------------------------------
// equalsJSON — a member holding a JSON document, compared by value
// ---------------------------------------------------------------------------
//
// IAM sends a policy document as percent-encoded JSON text and this SDK hands
// that string back untouched, while botocore decodes it for python-sdk and
// cli. equalsJSON is how one scenario check answers the same on both sides of
// that split (compat/model/README.md § Assertions); its conformance fixture is
// compat/model/testdata/equalsjson.

// parseEqualsJSONOperand reads the operand cmd/compatgen writes into the
// emitted source: exactly one JSON text holding an object or an array, with no
// `$`-prefixed key at any depth. The operand is a literal and never
// evaluated, so a key the rest of the IR would read as an expression —
// {"$ref": ...} — is refused rather than read either way.
func parseEqualsJSONOperand(text string) (any, error) {
	var doc any
	if err := json.Unmarshal([]byte(text), &doc); err != nil {
		return nil, fmt.Errorf("equalsJSON operand is not one JSON text: %w", err)
	}
	switch doc.(type) {
	case map[string]any, []any:
	default:
		return nil, fmt.Errorf("equalsJSON operand must be a JSON object or array, got %s", render(doc))
	}
	if err := refuseDollarKeys(doc); err != nil {
		return nil, err
	}
	return doc, nil
}

// refuseDollarKeys walks a decoded JSON value for an object key starting with
// `$`, in key order so the error names the same key on every run.
func refuseDollarKeys(v any) error {
	switch t := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if strings.HasPrefix(k, "$") {
				return fmt.Errorf("equalsJSON operand is a literal document and is never evaluated, so its key %q may not start with $", k)
			}
			if err := refuseDollarKeys(t[k]); err != nil {
				return err
			}
		}
	case []any:
		for _, item := range t {
			if err := refuseDollarKeys(item); err != nil {
				return err
			}
		}
	}
	return nil
}

// equalsJSON evaluates an equalsJSON check against the value at its path and,
// when it does not hold, says what was there for the failure message: the
// document it decoded to, the value that is not one, or the usual missing
// rendering.
func equalsJSON(got any, resolved bool, want any) (bool, string) {
	if !resolved {
		return false, missingValue
	}
	doc, isDoc := jsonDocument(got)
	if !isDoc {
		return false, "not a JSON document: " + render(got)
	}
	if !jsonValueEqual(doc, want) {
		return false, "document " + render(doc)
	}
	return true, ""
}

// jsonDocument is the document an equalsJSON check compares, and whether the
// value holds one at all. A string is percent-decoded once and then read as
// exactly one JSON text; an object or a list is the document as it stands;
// anything else (a number, a boolean, null) is not a document.
func jsonDocument(v any) (any, bool) {
	switch t := v.(type) {
	case map[string]any, []any:
		return t, true
	case string:
		// json.Unmarshal is the strict reader the check wants: surrounding
		// whitespace is fine, anything after the one value is an error, and a
		// number decodes to the float64 the comparison needs.
		var doc any
		if err := json.Unmarshal([]byte(percentDecode(t)), &doc); err != nil {
			return nil, false
		}
		return doc, true
	default:
		return nil, false
	}
}

// percentDecode is Python's urllib.parse.unquote, which botocore applies to a
// policy document unconditionally and python-sdk and cli therefore cannot opt
// out of, so every backend decodes the same way: `%XX` (either case) becomes
// that byte, `+` stays `+`, and a `%` not followed by two hex digits is kept
// as written. It runs once, so `%257B` is `%7B`. url.PathUnescape and
// url.QueryUnescape both refuse a malformed escape, and the latter turns `+`
// into a space, so neither will do.
func percentDecode(s string) string {
	if !strings.Contains(s, "%") {
		return s
	}
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == '%' && i+2 < len(s) {
			hi, hiOK := unhex(s[i+1])
			lo, loOK := unhex(s[i+2])
			if hiOK && loOK {
				out = append(out, hi<<4|lo)
				i += 2
				continue
			}
		}
		out = append(out, s[i])
	}
	return string(out)
}

func unhex(c byte) (byte, bool) {
	switch {
	case '0' <= c && c <= '9':
		return c - '0', true
	case 'a' <= c && c <= 'f':
		return c - 'a' + 10, true
	case 'A' <= c && c <= 'F':
		return c - 'A' + 10, true
	}
	return 0, false
}

// jsonValueEqual is equalsJSON's equality: objects by member name whatever
// their order, arrays element by element in order, numbers by numeric value
// (so 1, 1.0 and 1e0 are equal, and so are -0 and 0, which jsonEqual's
// canonical text would tell apart), and strings, booleans and null by type and
// value with no coercion.
func jsonValueEqual(a, b any) bool {
	switch x := a.(type) {
	case map[string]any:
		y, ok := b.(map[string]any)
		if !ok || len(x) != len(y) {
			return false
		}
		for k, xv := range x {
			yv, present := y[k]
			if !present || !jsonValueEqual(xv, yv) {
				return false
			}
		}
		return true
	case []any:
		y, ok := b.([]any)
		if !ok || len(x) != len(y) {
			return false
		}
		for i := range x {
			if !jsonValueEqual(x[i], y[i]) {
				return false
			}
		}
		return true
	case float64:
		y, ok := b.(float64)
		return ok && x == y
	case string:
		y, ok := b.(string)
		return ok && x == y
	case bool:
		y, ok := b.(bool)
		return ok && x == y
	case nil:
		return b == nil
	default:
		return false
	}
}
