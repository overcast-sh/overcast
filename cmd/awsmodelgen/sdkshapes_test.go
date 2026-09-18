package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// sdkShapeFixture adds what the SDK tables render beyond the snapshot: a list
// member's @xmlName, HTTP bindings, a streaming blob and a Query error code.
const sdkShapeFixture = `{
  "smithy":"2.0",
  "shapes":{
    "example.box#BoxService":{"type":"service","version":"2026-03-03","operations":[{"target":"example.box#PutBox"}],
      "traits":{"aws.api#service":{"sdkId":"Box"},"aws.protocols#restXml":{},"smithy.api#xmlNamespace":{"uri":"https://box.example/doc/"}}},
    "example.box#PutBox":{"type":"operation","input":{"target":"example.box#PutBoxRequest"},"output":{"target":"smithy.api#Unit"},
      "errors":[{"target":"example.box#NoSuchBox"}],
      "traits":{"smithy.api#http":{"method":"PUT","uri":"/{Name}?box","code":200},"smithy.api#documentation":"dropped"}},
    "example.box#PutBoxRequest":{"type":"structure","members":{
      "Name":{"target":"smithy.api#String","traits":{"smithy.api#httpLabel":{},"smithy.api#required":{}}},
      "Labels":{"target":"example.box#LabelList","traits":{"smithy.api#httpHeader":"x-box-labels"}},
      "Colour":{"target":"example.box#Colour","traits":{"smithy.api#httpQuery":"colour"}},
      "Body":{"target":"example.box#Stream","traits":{"smithy.api#httpPayload":{}}}}},
    "example.box#LabelList":{"type":"list","member":{"target":"smithy.api#String","traits":{"smithy.api#xmlName":"Label"}}},
    "example.box#Stream":{"type":"blob","traits":{"smithy.api#streaming":{}}},
    "example.box#NoSuchBox":{"type":"structure","members":{},"traits":{"smithy.api#error":"client","smithy.api#httpError":404,"aws.protocols#awsQueryError":{"code":"Box.Missing","httpResponseCode":404}}},
    "example.box#Colour":{"type":"enum","members":{"RED":{"target":"smithy.api#Unit","traits":{"smithy.api#enumValue":"red"}}}}
  }
}`

func TestBuildSDKShapes_rendersTheTranslatorsFacts(t *testing.T) {
	// Given: a corpus with one service.
	dir := t.TempDir()
	writeModel(t, filepath.Join(dir, "box.json"), sdkShapeFixture)

	// When: its SDK shape table is generated.
	files, err := buildSDKShapes(dir, []string{"box"})
	if err != nil {
		t.Fatalf("build sdk shapes: %v", err)
	}

	// Then: there is one table and an index naming it.
	table := string(files["shapes_box.gen.go"])
	if !strings.Contains(string(files["index.gen.go"]), `{service: "box", encoded: shapesBox}`) {
		t.Errorf("index:\n%s", files["index.gen.go"])
	}
	// ... carrying the header, the bindings, the list member's element name,
	// streaming and the error's Query code, with prelude targets shortened.
	for _, want := range []string{
		"const shapesBox = `service RESTXML version=2026-03-03 ns=https://box.example/doc/\n",
		"PutBox operation in=PutBoxRequest out=~Unit err=NoSuchBox method=PUT uri=/{Name}?box code=200\n",
		".Name ~String label\n",
		".Labels LabelList header=x-box-labels\n",
		".Body Stream payload\n",
		".Colour Colour query=colour\n",
		"LabelList list m=~String mxml=Label\n",
		"Stream blob stream\n",
		"NoSuchBox structure qerr=Box.Missing error=client code=404\n",
		"Colour enum\n",
	} {
		if !strings.Contains(table, want) {
			t.Errorf("table is missing %q:\n%s", want, table)
		}
	}
	// ... and nothing the translator does not read.
	for _, unwanted := range []string{"dropped", "required", "RED", "red"} {
		if strings.Contains(table, unwanted) {
			t.Errorf("table still carries %q:\n%s", unwanted, table)
		}
	}
}

func TestWriteOrCheckSDKShapes_leavesHandWrittenFilesAlone(t *testing.T) {
	// Given: a package directory holding a hand-written file and a stale table.
	dir := t.TempDir()
	for name, contents := range map[string]string{"awsshapes.go": "package awsshapes\n", "shapes_gone.gen.go": "stale"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	files := map[string][]byte{"index.gen.go": []byte("new")}

	// When: check mode runs, then write mode.
	checkErr := writeOrCheckSDKShapes(dir, files, true)
	writeErr := writeOrCheckSDKShapes(dir, files, false)

	// Then: check reports the stale table, write removes it, and the
	// hand-written decoder survives both.
	if checkErr == nil || !strings.Contains(checkErr.Error(), "shapes_gone.gen.go") {
		t.Errorf("check error = %v", checkErr)
	}
	if writeErr != nil {
		t.Fatal(writeErr)
	}
	if _, err := os.Stat(filepath.Join(dir, "shapes_gone.gen.go")); !os.IsNotExist(err) {
		t.Errorf("stale table survived: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "awsshapes.go")); err != nil {
		t.Errorf("hand-written file removed: %v", err)
	}
}
