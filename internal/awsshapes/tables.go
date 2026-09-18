package awsshapes

import (
	"bytes"
	"compress/gzip"
	"embed"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// dist holds the compressed tables `make aws-sdk-shapes` builds. `all:` so the
// committed .gitkeep is embedded too: without it the pattern matches nothing in
// a bare checkout and the package fails to compile.
//
//go:embed all:dist
var dist embed.FS

// Where the tables live in the repository. Both appear in error messages, so
// they must stay in step with the Makefile's aws-sdk-shapes target.
const (
	distDirRel   = "internal/awsshapes/dist"
	tablesDirRel = "internal/awsshapes/tables"
)

// PackedSuffix is appended to a table's file name for its compressed copy in
// dist/.
const PackedSuffix = ".gz"

// EnvTablesDir names a directory to read the tables from INSTEAD of the
// embedded copies — either the committed text (`<file>`) or packed ones
// (`<file>.gz`). Two audiences, neither of them production:
//
//   - The repository's own test binaries. An embed is baked at compile time,
//     so a test binary compiled from a bare checkout — before `make
//     aws-sdk-shapes` has ever run — carries no tables. The packages whose
//     tests exercise them point this at the committed text in their TestMain
//     (internal/awsshapes/awsshapestest), so `go test` passes on a fresh clone.
//   - A developer iterating on the generator, who wants a run to read freshly
//     generated tables without repacking and relinking.
//
// When unset — every production deployment — the embedded copies are the only
// source, and a build without them fails loudly as documented on Lookup.
const EnvTablesDir = "OVERCAST_AWS_SDK_SHAPES_DIR"

// TableFiles lists every table's file name, in service order: the name under
// tables/ for the committed text, and — with PackedSuffix — under dist/.
func TableFiles() []string {
	out := make([]string, len(tables))
	for i, t := range tables {
		out[i] = t.file
	}
	return out
}

// Pack compresses a table for dist/. The output depends only on the input —
// no timestamp or name in the gzip header — so rebuilding an unchanged table
// produces identical bytes, and a test can compare a packed copy against the
// committed text byte for byte.
func Pack(text []byte) ([]byte, error) {
	var buf bytes.Buffer
	w, err := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	if err != nil {
		return nil, err
	}
	if _, err := w.Write(text); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// unpack reverses Pack.
func unpack(packed []byte) ([]byte, error) {
	r, err := gzip.NewReader(bytes.NewReader(packed))
	if err != nil {
		return nil, err
	}
	defer r.Close()
	return io.ReadAll(r)
}

// readTable returns one table's text: from EnvTablesDir when it is set,
// otherwise from the embedded, compressed copy.
func readTable(fsys fs.FS, file string) (string, error) {
	if dir := os.Getenv(EnvTablesDir); dir != "" {
		return readTableFromDir(dir, file)
	}
	packed, err := fs.ReadFile(fsys, "dist/"+file+PackedSuffix)
	if err != nil || len(packed) == 0 {
		return "", fmt.Errorf("this overcast build has no aws-sdk shape tables: %s/%s%s is missing — run `make aws-sdk-shapes` (or `task aws-sdk-shapes`) and rebuild", distDirRel, file, PackedSuffix)
	}
	text, err := unpack(packed)
	if err != nil {
		return "", fmt.Errorf("%s/%s%s is corrupt (%w) — run `make aws-sdk-shapes` (or `task aws-sdk-shapes`) and rebuild", distDirRel, file, PackedSuffix, err)
	}
	return string(text), nil
}

func readTableFromDir(dir, file string) (string, error) {
	if text, err := os.ReadFile(filepath.Join(dir, file)); err == nil {
		return string(text), nil
	}
	packed, err := os.ReadFile(filepath.Join(dir, file+PackedSuffix))
	if err != nil {
		return "", fmt.Errorf("%s points at %s, but neither %s nor %s%s is readable there", EnvTablesDir, dir, file, file, PackedSuffix)
	}
	text, err := unpack(packed)
	if err != nil {
		return "", fmt.Errorf("%s points at %s, but %s%s there is corrupt: %w", EnvTablesDir, dir, file, PackedSuffix, err)
	}
	return string(text), nil
}
