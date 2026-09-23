package scenario

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// The shared blob-value conformance fixture, compat/model/testdata/blobs.
//
// The cli backend never holds a blob's bytes: the AWS CLI v2 reads a blob in
// --cli-input-json as base64 and prints one in --output json the same way, so
// `$base64` evaluates to the text itself and an exported blob already is that
// text. What this suite owes the fixture is that the text it passes through is
// the text every other backend decodes — each valid case goes through
// unchanged, and decodes to exactly the fixture's bytes — and that every
// spelling the fixture calls invalid is refused before the CLI sees it.

type blobFixture struct {
	Comment string     `json:"$comment"`
	Valid   []blobCase `json:"valid"`
	Invalid []blobCase `json:"invalid"`
}

type blobCase struct {
	Name   string `json:"name"`
	Base64 string `json:"base64"`
	Hex    string `json:"hex,omitempty"`
}

func TestSharedBlobFixture(t *testing.T) {
	root := repoRootFromTest(t)
	raw, err := os.ReadFile(filepath.Join(root, "compat", "model", "testdata", "blobs", "blobs.json"))
	if err != nil {
		t.Fatal(err)
	}
	var f blobFixture
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&f); err != nil {
		t.Fatal(err)
	}
	if len(f.Valid) == 0 || len(f.Invalid) == 0 {
		t.Fatal("the blob fixture has no valid or no invalid cases: the shared set may not be skipped by emptying it")
	}
	type obj = map[string]any
	for _, c := range f.Valid {
		t.Run("valid/"+c.Name, func(t *testing.T) {
			want, err := hex.DecodeString(c.Hex)
			if err != nil {
				t.Fatal(err)
			}
			e := testEvaluator(map[string]any{"rec.data": c.Base64})
			for _, in := range []any{obj{"$base64": c.Base64}, obj{"$base64": obj{"$ref": "rec.data"}}} {
				got, err := e.eval(in)
				if err != nil {
					t.Fatalf("eval %v: %v", in, err)
				}
				if got != c.Base64 {
					t.Fatalf("eval %v = %v, want the text %q passed through", in, got, c.Base64)
				}
			}
			bytesOut, err := decodeBase64(c.Base64)
			if err != nil || !bytes.Equal(bytesOut, want) {
				t.Fatalf("decode %q = %x, %v; want %x", c.Base64, bytesOut, err, want)
			}
			// A response blob is already the document form, so an equals
			// against a $base64 is a comparison of two equal strings.
			if !jsonEqual(c.Base64, mustEval(t, e, obj{"$base64": c.Base64})) {
				t.Fatalf("equals $base64 %q does not hold", c.Base64)
			}
		})
	}
	for _, c := range f.Invalid {
		t.Run("invalid/"+c.Name, func(t *testing.T) {
			e := testEvaluator(map[string]any{"rec.data": c.Base64})
			for _, in := range []any{obj{"$base64": c.Base64}, obj{"$base64": obj{"$ref": "rec.data"}}} {
				if got, err := e.eval(in); err == nil {
					t.Fatalf("eval %v accepted %q as %v", in, c.Base64, got)
				}
			}
		})
	}
}

func mustEval(t *testing.T, e *evaluator, v any) any {
	t.Helper()
	out, err := e.eval(v)
	if err != nil {
		t.Fatal(err)
	}
	return out
}
