package awsshapes

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

// TestMain reads the committed text tables: a test binary compiled from a bare
// checkout has no packed copies embedded (see EnvTablesDir). The tests below
// that exercise the embedded path unset it for themselves.
func TestMain(m *testing.M) {
	if os.Getenv(EnvTablesDir) == "" {
		dir, err := filepath.Abs("tables")
		if err != nil {
			panic(err)
		}
		_ = os.Setenv(EnvTablesDir, dir)
	}
	os.Exit(m.Run())
}

func TestPack_isDeterministicAndRoundTrips(t *testing.T) {
	// Given: a table's text.
	text := []byte("service AWSJSON11 version=1\nThing structure\n.Name String\n")

	// When: it is packed twice and unpacked.
	first, err := Pack(text)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Pack(text)
	if err != nil {
		t.Fatal(err)
	}
	back, err := unpack(first)

	// Then: the two packings are byte-identical, so a rebuild of an unchanged
	// table changes nothing, and the text comes back exactly.
	if !bytes.Equal(first, second) {
		t.Error("Pack is not deterministic")
	}
	if err != nil || !bytes.Equal(back, text) {
		t.Errorf("unpack = %q, %v", back, err)
	}
}

func TestReadTable_readsTheEmbeddedPackedCopy(t *testing.T) {
	// Given: no override, and an embedded dist/ holding one packed table.
	t.Setenv(EnvTablesDir, "")
	packed, err := Pack([]byte("service RESTXML\n"))
	if err != nil {
		t.Fatal(err)
	}
	fsys := fstest.MapFS{"dist/box.txt.gz": {Data: packed}}

	// When: the table is read.
	text, err := readTable(fsys, "box.txt")

	// Then: it is the unpacked text.
	if err != nil || text != "service RESTXML\n" {
		t.Fatalf("readTable = %q, %v", text, err)
	}
}

func TestReadTable_namesTheFixWhenTheBuildHasNoTables(t *testing.T) {
	// Given: a build made without `make aws-sdk-shapes` — dist/ holds only
	// .gitkeep.
	t.Setenv(EnvTablesDir, "")
	fsys := fstest.MapFS{"dist/.gitkeep": {}}

	// When: a table is read.
	_, err := readTable(fsys, "box.txt")

	// Then: the error names the missing file and the command that builds it.
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, want := range []string{"internal/awsshapes/dist/box.txt.gz", "make aws-sdk-shapes", "task aws-sdk-shapes"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

func TestReadTable_rejectsACorruptPackedCopy(t *testing.T) {
	t.Setenv(EnvTablesDir, "")
	fsys := fstest.MapFS{"dist/box.txt.gz": {Data: []byte("not gzip")}}

	_, err := readTable(fsys, "box.txt")

	if err == nil || !strings.Contains(err.Error(), "corrupt") {
		t.Fatalf("err = %v", err)
	}
}

func TestReadTable_overrideReadsTextOrPackedFiles(t *testing.T) {
	// Given: an override directory with one table as text and one packed.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "plain.txt"), []byte("service A\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	packed, err := Pack([]byte("service B\n"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "packed.txt.gz"), packed, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(EnvTablesDir, dir)

	// When / Then: both read, and a missing one names the override.
	for file, want := range map[string]string{"plain.txt": "service A\n", "packed.txt": "service B\n"} {
		if got, err := readTable(nil, file); err != nil || got != want {
			t.Errorf("readTable(%s) = %q, %v", file, got, err)
		}
	}
	if _, err := readTable(nil, "missing.txt"); err == nil || !strings.Contains(err.Error(), EnvTablesDir) {
		t.Errorf("missing table err = %v", err)
	}
}

// TestDist_matchesTheCommittedTables catches a stale local build: when
// `make aws-sdk-shapes` has run, every packed copy must unpack to exactly the
// committed text. A bare checkout has no packed copies, and passes.
func TestDist_matchesTheCommittedTables(t *testing.T) {
	for _, file := range TableFiles() {
		packed, err := dist.ReadFile("dist/" + file + PackedSuffix)
		if err != nil {
			continue
		}
		text, err := unpack(packed)
		if err != nil {
			t.Errorf("%s: %v", file, err)
			continue
		}
		committed, err := os.ReadFile(filepath.Join("tables", file))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(text, committed) {
			t.Errorf("dist/%s%s is stale: run `make aws-sdk-shapes` and rebuild", file, PackedSuffix)
		}
	}
}
