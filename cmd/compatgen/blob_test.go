//go:build dev

package main

import (
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The blob value (#1910): `$base64` is the only value a blob member takes,
// in a recipe, in an authored scenario and on the expected side of a check,
// and a bound $ref into a blob is wrapped rather than handed over bare.

func kinesisModel(t *testing.T) *serviceModel {
	t.Helper()
	model, err := loadModel(filepath.Join("..", "..", "models", "aws", "shapes"), "kinesis")
	if err != nil {
		t.Fatal(err)
	}
	return model
}

// The generator's decoder answers the shared fixture exactly as every
// suite's runtime does.
func TestDecodeBase64_sharedFixture(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "compat", "model", "testdata", "blobs", "blobs.json"))
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		Valid   []struct{ Name, Base64, Hex string } `json:"valid"`
		Invalid []struct{ Name, Base64 string }      `json:"invalid"`
	}
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	for _, c := range f.Valid {
		got, err := decodeBase64(c.Base64)
		if err != nil || hex.EncodeToString(got) != c.Hex {
			t.Errorf("valid/%s: decodeBase64(%q) = %x, %v; want %s", c.Name, c.Base64, got, err, c.Hex)
		}
		if err := validateValue(map[string]any{"$base64": c.Base64}, "v"); err != nil {
			t.Errorf("valid/%s: validateValue: %v", c.Name, err)
		}
	}
	for _, c := range f.Invalid {
		if got, err := decodeBase64(c.Base64); err == nil {
			t.Errorf("invalid/%s: decodeBase64(%q) accepted it as %x", c.Name, c.Base64, got)
		}
		if err := validateValue(map[string]any{"$base64": c.Base64}, "v"); err == nil {
			t.Errorf("invalid/%s: validateValue accepted it", c.Name)
		}
	}
}

func TestCheckBlob(t *testing.T) {
	model := kinesisModel(t)
	input := model.InputShape("PutRecord")
	target, ok := model.MemberTarget(input, "Data")
	if !ok || model.Kind(target) != "blob" {
		t.Fatalf("PutRecord.Data is %q, want a blob", model.Kind(target))
	}
	exports := exportKinds{"rec.data": "blob", "stream.name": "string"}
	cases := []struct {
		name  string
		value any
		want  string
	}{
		{"a literal", map[string]any{"$base64": "cmVjb3JkLTE="}, ""},
		{"a ref to an exported blob", map[string]any{"$base64": map[string]any{"$ref": "rec.data"}}, ""},
		{"a plain string", "record-1", `write its bytes as {"$base64"`},
		{"a bare ref", map[string]any{"$ref": "rec.data"}, "bare $ref"},
		{"a ref to a string", map[string]any{"$base64": map[string]any{"$ref": "stream.name"}}, "is a string"},
		{"a ref nothing exports", map[string]any{"$base64": map[string]any{"$ref": "rec.other"}}, "not exported"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := checkBlob(model, tc.value, target, exports, "PutRecord.Data")
			if tc.want == "" {
				if err != nil {
					t.Fatalf("refused: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want it to mention %q", err, tc.want)
			}
		})
	}
}

// An authored scenario is held to the blob rule at every depth — a record in
// PutRecords is a structure in a list — and in a check that resolves to a
// blob; `$base64` anywhere else is refused too.
func TestCheckAuthoredValues_blobRule(t *testing.T) {
	model := kinesisModel(t)
	put := func(data any) call {
		return call{Op: "PutRecords", Params: map[string]any{
			"StreamName": "s",
			"Records":    []any{map[string]any{"PartitionKey": "p", "Data": data}},
		}}
	}
	ok := group{Name: "kinesis-records", Tests: []test{{
		Name: "PutRecords", Op: "PutRecords", Call: put(map[string]any{"$base64": "cmVjb3JkLTE="}),
		Assert: []assertion{responseField(map[string]check{"$.FailedRecordCount": equals(json.Number("0"))})},
	}}}
	if err := checkAuthoredValues(model, ok); err != nil {
		t.Fatalf("a $base64 record was refused: %v", err)
	}

	bad := ok
	bad.Tests = []test{ok.Tests[0]}
	bad.Tests[0].Call = put("record-1")
	err := checkAuthoredValues(model, bad)
	if err == nil || !strings.Contains(err.Error(), "Records[0].Data") || !strings.Contains(err.Error(), "$base64") {
		t.Fatalf("a string record was not refused naming the member and $base64: %v", err)
	}

	misplaced := ok
	misplaced.Tests = []test{ok.Tests[0]}
	misplaced.Tests[0].Call = call{Op: "PutRecords", Params: map[string]any{
		"StreamName": map[string]any{"$base64": "cw=="},
		"Records":    []any{map[string]any{"PartitionKey": "p", "Data": map[string]any{"$base64": "cw=="}}},
	}}
	if err := checkAuthoredValues(model, misplaced); err == nil || !strings.Contains(err.Error(), "StreamName") {
		t.Fatalf("$base64 on a string member was not refused: %v", err)
	}

	checked := group{Name: "kinesis-records", Tests: []test{{
		Name: "GetRecords", Op: "GetRecords",
		Call: call{Op: "GetRecords", Params: map[string]any{"ShardIterator": "it"}},
		Assert: []assertion{listContains(nil, "$.Records", map[string]any{
			"$.Data": "record-1",
		})},
	}}}
	if err := checkAuthoredValues(model, checked); err == nil || !strings.Contains(err.Error(), "where $.Data") {
		t.Fatalf("a string compared with a blob was not refused: %v", err)
	}
}

// A binding into a blob member carries the bytes: the bound $ref is wrapped in
// $base64, and a binding into a kind the IR cannot carry is a gap of its own
// rather than an emitter refusal.
func TestBinder_blobSafeAndUnportable(t *testing.T) {
	model := kinesisModel(t)
	b := &binder{model: model, service: "kinesis"}
	target, _ := model.MemberTarget(model.InputShape("PutRecord"), "Data")
	got := b.blobSafe(map[string]any{"$ref": "rec.data"}, target)
	want := map[string]any{"$base64": map[string]any{"$ref": "rec.data"}}
	if gotJSON, _ := json.Marshal(got); string(gotJSON) != mustJSON(t, want) {
		t.Fatalf("blobSafe = %s, want %s", gotJSON, mustJSON(t, want))
	}
	stamp, _ := model.MemberTarget(model.InputShape("GetShardIterator"), "Timestamp")
	r := b.unportable("GetShardIterator", "Timestamp", stamp, "stream.created")
	if r == nil || r.Reason != reasonNoPortableValue+":Timestamp" {
		t.Fatalf("unportable = %+v", r)
	}
	if r := b.unportable("PutRecord", "Data", target, "rec.data"); r != nil {
		t.Fatalf("a blob was refused as unportable: %+v", r)
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	out, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}
