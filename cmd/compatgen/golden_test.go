//go:build dev

package main

import (
	"os"
	"path/filepath"
	"testing"
)

// The golden files under testdata/golden, shared by every source emitter.
//
// A golden file is the review artifact for what an emitter writes: the diff is
// how a change to the emitted shape is reviewed, rather than being inferred
// from the generator's code. That is why updating one is a deliberate act with
// its own environment variable, and why the update path fails the test rather
// than passing quietly.

// updateGolden rewrites the golden files instead of comparing against them:
//
//	OVERCAST_UPDATE_GOLDEN=1 go test -tags dev -run TestEmit ./cmd/compatgen
//
// Read the diff before committing the result — a golden regenerated without
// being read proves nothing.
func updateGolden() bool { return os.Getenv("OVERCAST_UPDATE_GOLDEN") == "1" }

// assertGolden compares emitted bytes with the committed golden file, or
// rewrites it under OVERCAST_UPDATE_GOLDEN.
func assertGolden(t *testing.T, path string, got []byte) {
	t.Helper()
	if updateGolden() {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
		t.Fatalf("golden file rewritten; re-run without OVERCAST_UPDATE_GOLDEN and read the diff")
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden: %v (set OVERCAST_UPDATE_GOLDEN=1 to write it)", err)
	}
	if string(got) != string(want) {
		t.Errorf("emitted source differs from %s; set OVERCAST_UPDATE_GOLDEN=1 to update it after reading the diff\n--- got ---\n%s", path, got)
	}
}

// fixtureRenderEnv is the `-explain` environment for the fixture service: the
// stand-in Go SDK's types for the Go rendering, the stand-in .NET SDK type
// table for the .NET one, and the fixture's own shape model for every
// rendering that reads it. The fixture has no
// recipe under compat/model/recipes, which is why renderEnv takes a resolver
// rather than a directory.
//
// It lives here rather than beside one emitter's tests because every typed
// backend asserts that `-explain` renders the source it emits, and each needs
// this to do it.
func fixtureRenderEnv(gen *generation) renderEnv {
	return renderEnv{
		goTypes: fixtureGoTypes(),
		dotnet:  func() (*dotnetSDKTypes, error) { return fixtureDotnetTypes(), nil },
		model:   staticModel(gen.model),
	}
}
