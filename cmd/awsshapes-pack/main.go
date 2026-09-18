// Command awsshapes-pack compresses the committed aws-sdk shape tables
// (internal/awsshapes/tables) into internal/awsshapes/dist, which the awsshapes
// package embeds. It is the build step behind `make aws-sdk-shapes` / `task
// aws-sdk-shapes`, and every build target depends on it.
//
// The text tables are committed because a model refresh should review as a
// readable diff; the compressed copies are build output, not committed,
// because the binary only needs the bytes — the same split the SPA and the
// Lambda init already make. Output is deterministic (see awsshapes.Pack) and a
// file is only rewritten when its bytes change, so an unchanged table never
// invalidates a build cache. Files in dist/ that no table produces are removed;
// the committed .gitkeep is kept.
package main

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/overcast-sh/overcast/internal/awsshapes"
)

func main() {
	src := flag.String("src", filepath.FromSlash("internal/awsshapes/tables"), "directory holding the committed text tables")
	dst := flag.String("dst", filepath.FromSlash("internal/awsshapes/dist"), "directory the awsshapes package embeds")
	flag.Parse()
	written, err := pack(*src, *dst)
	if err != nil {
		fmt.Fprintf(os.Stderr, "awsshapes-pack: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("awsshapes-pack: %d tables in %s (%d rewritten)\n", len(awsshapes.TableFiles()), *dst, written)
}

// pack writes every table's compressed copy into dst and removes any that no
// table produces. It returns how many files it had to (re)write.
func pack(src, dst string) (int, error) {
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return 0, err
	}
	wanted := map[string]bool{}
	written := 0
	for _, file := range awsshapes.TableFiles() {
		text, err := os.ReadFile(filepath.Join(src, file))
		if err != nil {
			return written, fmt.Errorf("read table: %w", err)
		}
		packed, err := awsshapes.Pack(text)
		if err != nil {
			return written, fmt.Errorf("pack %s: %w", file, err)
		}
		name := file + awsshapes.PackedSuffix
		wanted[name] = true
		target := filepath.Join(dst, name)
		if current, err := os.ReadFile(target); err == nil && bytes.Equal(current, packed) {
			continue
		}
		if err := os.WriteFile(target, packed, 0o644); err != nil {
			return written, fmt.Errorf("write %s: %w", target, err)
		}
		written++
	}
	entries, err := os.ReadDir(dst)
	if err != nil {
		return written, err
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || wanted[name] || !strings.HasSuffix(name, awsshapes.PackedSuffix) {
			continue
		}
		if err := os.Remove(filepath.Join(dst, name)); err != nil {
			return written, fmt.Errorf("remove stale %s: %w", name, err)
		}
	}
	return written, nil
}
