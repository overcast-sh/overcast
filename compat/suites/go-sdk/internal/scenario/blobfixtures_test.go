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
// Every backend agrees on one document form for a blob — its canonical
// standard base64 text — and this file is where go-sdk proves it, against the
// same cases every other suite's unit tests read: the text decodes to exactly
// the fixture's bytes, the SDK's []byte renders back to exactly that text, a
// `$base64` compares equal to it, and every spelling the fixture calls invalid
// is refused rather than decoded into something else.

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

func loadBlobFixture(t *testing.T) blobFixture {
	t.Helper()
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
	return f
}

func TestSharedBlobFixture(t *testing.T) {
	f := loadBlobFixture(t)
	for _, c := range f.Valid {
		t.Run("valid/"+c.Name, func(t *testing.T) {
			want, err := hex.DecodeString(c.Hex)
			if err != nil {
				t.Fatal(err)
			}

			// A literal $base64 into a blob member, the way the emitted source
			// never does at run time but a $ref does: through Blob.
			b := &Binder{runID: "oc", group: "g", bag: newContextBag()}
			if got := Blob(b, "Data", Base64(c.Base64)); b.err != nil || !bytes.Equal(got, want) {
				t.Fatalf("Blob(Base64(%q)) = %x, %v; want %x", c.Base64, got, b.err, want)
			}

			// An exported blob is its base64 text in the bag, and a $base64
			// around a $ref to it decodes the same bytes.
			bag := newContextBag()
			bag.set("rec.data", c.Base64)
			b = &Binder{runID: "oc", group: "g", bag: bag}
			if got := Blob(b, "Data", Base64(Ref("rec.data"))); b.err != nil || !bytes.Equal(got, want) {
				t.Fatalf("Blob(Base64(Ref)) = %x, %v; want %x", got, b.err, want)
			}

			// The SDK's []byte renders to exactly the fixture's text, and an
			// equals against the $base64 holds.
			doc, ok := toDocument(struct{ Data []byte }{Data: want})
			if !ok {
				t.Fatal("document not present")
			}
			got := doc.(map[string]any)["Data"]
			if got != c.Base64 {
				t.Fatalf("document form %v, want %q", got, c.Base64)
			}
			expected, err := b.eval(Base64(c.Base64))
			if err != nil {
				t.Fatal(err)
			}
			if !jsonEqual(got, expected) {
				t.Fatalf("equals $base64 %q does not hold against %v", c.Base64, got)
			}
		})
	}
	for _, c := range f.Invalid {
		t.Run("invalid/"+c.Name, func(t *testing.T) {
			if raw, err := DecodeBase64(c.Base64); err == nil {
				t.Fatalf("DecodeBase64(%q) accepted it as %x", c.Base64, raw)
			}
			b := &Binder{runID: "oc", group: "g", bag: newContextBag()}
			if got := Blob(b, "Data", Base64(c.Base64)); b.err == nil {
				t.Fatalf("Blob(Base64(%q)) bound %x instead of failing", c.Base64, got)
			}
			if b.member != "Data" {
				t.Fatalf("failure names member %q, want Data", b.member)
			}
		})
	}
}

// A bare string is never decoded: a blob slot takes a $base64 value, and the
// base64 text an equals compares is the document form, not the bytes.
func TestBlobRefusesAValueThatIsNotText(t *testing.T) {
	b := &Binder{runID: "oc", group: "g", bag: newContextBag()}
	if got := Blob(b, "Data", Lit(3.0)); b.err == nil {
		t.Fatalf("a number bound as %x", got)
	}
}
