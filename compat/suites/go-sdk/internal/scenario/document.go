package scenario

import (
	"encoding/base64"
	"fmt"
	"reflect"
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
