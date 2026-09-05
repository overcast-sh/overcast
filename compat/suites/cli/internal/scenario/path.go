package scenario

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// Paths (compat/model/README.md § Paths): `$` is the response, `.Name` selects
// a structure member or map key, `[n]` selects a list element. Nothing else —
// no wildcards, filters, quoting or recursive descent.

// pathSegment is one step of a path: a member name or a list index.
type pathSegment struct {
	member string
	index  int
	isIdx  bool
}

// parsePath splits a path into its segments. It rejects anything the IR's Path
// pattern does not admit, so a malformed path fails the step rather than
// silently resolving to nothing — the two are very different bugs.
func parsePath(p string) ([]pathSegment, error) {
	if p == "" || p[0] != '$' {
		return nil, fmt.Errorf("path %q does not start with $", p)
	}
	var segs []pathSegment
	rest := p[1:]
	for rest != "" {
		switch rest[0] {
		case '.':
			rest = rest[1:]
			end := strings.IndexAny(rest, ".[")
			if end < 0 {
				end = len(rest)
			}
			if end == 0 {
				return nil, fmt.Errorf("path %q has an empty member name", p)
			}
			segs = append(segs, pathSegment{member: rest[:end]})
			rest = rest[end:]
		case '[':
			end := strings.IndexByte(rest, ']')
			if end < 0 {
				return nil, fmt.Errorf("path %q has an unterminated index", p)
			}
			n, err := strconv.Atoi(rest[1:end])
			if err != nil || n < 0 {
				return nil, fmt.Errorf("path %q has a non-numeric index %q", p, rest[1:end])
			}
			segs = append(segs, pathSegment{index: n, isIdx: true})
			rest = rest[end+1:]
		default:
			return nil, fmt.Errorf("path %q has an unexpected character %q", p, rest[0])
		}
	}
	return segs, nil
}

// resolvePath walks a path over a decoded JSON document. ok is false when any
// segment is absent — which is what `missing` tests for, and what makes an
// absent list count as empty for listContains and absent.
//
// A JSON null that is present resolves: it is a value the service sent, not a
// missing member, and only nonEmpty distinguishes the two.
func resolvePath(doc any, p string) (any, bool, error) {
	segs, err := parsePath(p)
	if err != nil {
		return nil, false, err
	}
	cur := doc
	for _, s := range segs {
		if s.isIdx {
			list, ok := cur.([]any)
			if !ok || s.index >= len(list) {
				return nil, false, nil
			}
			cur = list[s.index]
			continue
		}
		obj, ok := cur.(map[string]any)
		if !ok {
			return nil, false, nil
		}
		v, ok := obj[s.member]
		if !ok {
			return nil, false, nil
		}
		cur = v
	}
	return cur, true, nil
}

// canonicalJSON renders a decoded JSON value in a stable form: object keys
// sorted (encoding/json does that for a map), no HTML escaping, no trailing
// newline. It is both how values are compared and how they are printed in a
// failure message, so "expected X, actual Y" reads in the same notation the
// scenario file is written in.
func canonicalJSON(v any) (string, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return "", err
	}
	return strings.TrimRight(buf.String(), "\n"), nil
}

// jsonEqual is the IR's "equal, as JSON" (compat/model/README.md § Assertions).
//
// The coercion rule: both sides are already Go values decoded from JSON by
// encoding/json — the response by internal/awscli, the expected value by this
// package's loader — so a JSON number is a float64 on both sides, a JSON string
// a string, an object a map[string]any. Comparing their canonical encodings is
// therefore JSON equality with no coercion of one type to another: "30" never
// equals 30, and 30 never equals 30.0000001. The generator only ever emits an
// equals literal of the member's modeled kind, so nothing wider is needed.
func jsonEqual(a, b any) bool {
	as, aerr := canonicalJSON(a)
	bs, berr := canonicalJSON(b)
	return aerr == nil && berr == nil && as == bs
}

// render prints a value for a failure message.
func render(v any) string {
	s, err := canonicalJSON(v)
	if err != nil {
		return fmt.Sprintf("%v", v)
	}
	return s
}

// missingValue is what a failure message prints where a path did not resolve.
const missingValue = "<missing>"

// renderResolved prints a resolved-or-not value for a failure message.
func renderResolved(v any, ok bool) string {
	if !ok {
		return missingValue
	}
	return render(v)
}
