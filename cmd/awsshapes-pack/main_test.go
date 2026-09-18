package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/overcast-sh/overcast/internal/awsshapes"
)

func TestPack_buildsEveryTableOnceAndTidiesDist(t *testing.T) {
	// Given: the committed tables, and a dist/ holding the .gitkeep and a
	// packed table no longer generated.
	src := filepath.Join("..", "..", "internal", "awsshapes", "tables")
	dst := t.TempDir()
	for name, contents := range map[string]string{".gitkeep": "", "gone.txt.gz": "stale"} {
		if err := os.WriteFile(filepath.Join(dst, name), []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	// When: it packs twice.
	first, err := pack(src, dst)
	if err != nil {
		t.Fatal(err)
	}
	second, err := pack(src, dst)
	if err != nil {
		t.Fatal(err)
	}

	// Then: every table is written the first time and none the second, so an
	// unchanged table never invalidates a build cache.
	if want := len(awsshapes.TableFiles()); first != want || second != 0 {
		t.Errorf("rewrote %d then %d tables, want %d then 0", first, second, want)
	}
	// And: each packed copy is Pack of the committed text.
	for _, file := range awsshapes.TableFiles() {
		text, err := os.ReadFile(filepath.Join(src, file))
		if err != nil {
			t.Fatal(err)
		}
		want, err := awsshapes.Pack(text)
		if err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(filepath.Join(dst, file+awsshapes.PackedSuffix))
		if err != nil || !bytes.Equal(got, want) {
			t.Errorf("%s: packed copy differs (%v)", file, err)
		}
	}
	// And: the stale table is gone and the .gitkeep is kept.
	if _, err := os.Stat(filepath.Join(dst, "gone.txt.gz")); !os.IsNotExist(err) {
		t.Errorf("stale table survived: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dst, ".gitkeep")); err != nil {
		t.Errorf(".gitkeep removed: %v", err)
	}
}
