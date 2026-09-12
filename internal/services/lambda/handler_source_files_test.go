package lambda

// handler_source_files_test.go — the per-file half of GET .../source, which
// the console's debugger reads source maps and their sources through
// (docs/plans/compute-debugger-console.md § 3.3): any file in the deployment
// by its path under /var/task, from the zip or from a hot-reload mount.

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"go.uber.org/zap"

	"github.com/overcast-sh/overcast/internal/clock"
	"github.com/overcast-sh/overcast/internal/config"
	"github.com/overcast-sh/overcast/internal/serviceutil"
	"github.com/overcast-sh/overcast/internal/state"
)

// sourceHandler is a handler over a memory store, hot reload on or off.
func sourceHandler(t *testing.T, hotReload bool) *Handler {
	t.Helper()
	clk := clock.NewMock()
	tracker := newInstanceTracker(clk, zap.NewNop())
	t.Cleanup(tracker.Stop)
	return &Handler{
		cfg:      &config.Config{Region: "us-east-1", LambdaHotReload: hotReload},
		log:      serviceutil.NewServiceLogger(zap.NewNop(), "lambda"),
		clk:      clk,
		ls:       newLambdaStore(state.NewMemoryStore(), "us-east-1", clk),
		runtimes: newRuntimeRegistry(nil),
		tracker:  tracker,
	}
}

func seedSourceFunction(t *testing.T, h *Handler, fn *Function) {
	t.Helper()
	fn.Name = "src-fn"
	fn.ARN = "arn:aws:lambda:us-east-1:000000000000:function:src-fn"
	fn.State = "Active"
	if aerr := h.ls.putFunction(context.Background(), fn); aerr != nil {
		t.Fatalf("seed function: %s", aerr.Message)
	}
}

func zipOf(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, content := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("zip entry %s: %v", name, err)
		}
		if _, err := w.Write([]byte(content)); err != nil {
			t.Fatalf("zip write %s: %v", name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zip close: %v", err)
	}
	return buf.Bytes()
}

func getSource(t *testing.T, h *Handler, file string) (*httptest.ResponseRecorder, sourceResponse) {
	t.Helper()
	target := "/_overcast/lambda/functions/src-fn/source"
	if file != "" {
		target += "?file=" + url.QueryEscape(file)
	}
	rec := httptest.NewRecorder()
	h.GetFunctionSource(rec, withFunctionNameParam(httptest.NewRequest(http.MethodGet, target, nil), "src-fn"))
	var body sourceResponse
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode %s: %v", rec.Body.String(), err)
		}
	}
	return rec, body
}

func fileNames(files []sourceFile) []string {
	names := make([]string, 0, len(files))
	for _, f := range files {
		names = append(names, f.Name)
	}
	return names
}

func TestGetFunctionSource_readsAnyFileInThePackageByPath(t *testing.T) {
	// Given: a bundled deployment with a nested entry, its source map and
	// the original beside it
	h := sourceHandler(t, false)
	fn := &Function{Runtime: "nodejs22.x", Handler: "dist/index.handler"}
	fn.setCode(zipOf(t, map[string]string{
		"dist/index.js":     `exports.handler = async () => 1; //# sourceMappingURL=index.js.map`,
		"dist/index.js.map": `{"version":3,"sources":["../src/index.ts"],"mappings":"AAAA"}`,
		"src/index.ts":      `export const handler = async () => 1;`,
	}))
	seedSourceFunction(t, h, fn)

	// When: the console asks for the source map by its path under /var/task
	rec, body := getSource(t, h, "dist/index.js.map")

	// Then: it is returned as JSON, with the whole deployment listed
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if body.Filename != "dist/index.js.map" || body.Language != "json" || !bytes.Contains([]byte(body.Source), []byte(`"sources"`)) {
		t.Errorf("body = %+v, want the map as json", body)
	}
	if got := fileNames(body.Files); len(got) != 3 {
		t.Errorf("files = %v, want all three", got)
	}

	// When: the original TypeScript is asked for
	rec, body = getSource(t, h, "src/index.ts")

	// Then: it comes back with its own language
	if rec.Code != http.StatusOK || body.Language != "typescript" || body.Source != `export const handler = async () => 1;` {
		t.Errorf("status %d body %+v, want the typescript source", rec.Code, body)
	}

	// When: a file the package does not hold is asked for
	rec, _ = getSource(t, h, "dist/missing.js")

	// Then: 404
	if rec.Code != http.StatusNotFound {
		t.Errorf("status %d, want 404", rec.Code)
	}
}

func TestGetFunctionSource_readsTheHotReloadMount(t *testing.T) {
	// Given: a hot-reload function whose tag mounts a host directory holding
	// the handler, a nested source map, and a dependency tree
	root := t.TempDir()
	write := func(rel, content string) {
		t.Helper()
		full := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("index.js", `exports.handler = async () => "mounted";`)
	write("dist/app.js.map", `{"version":3,"sources":["../src/app.ts"]}`)
	write("node_modules/dep/index.js", `module.exports = 1;`)
	if err := os.WriteFile(filepath.Join(root, "..", "outside.txt"), []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	h := sourceHandler(t, true)
	seedSourceFunction(t, h, &Function{
		Runtime: "nodejs22.x", Handler: "index.handler",
		Tags: map[string]string{hotReloadTagKey: root},
	})

	// When: the console opens the Code tab
	rec, body := getSource(t, h, "")

	// Then: the handler is read live from the mount, never a placeholder,
	// and the listing is the mount minus its dependency tree
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if body.Placeholder || body.Filename != "index.js" || body.Source != `exports.handler = async () => "mounted";` {
		t.Errorf("body = %+v, want the mounted handler", body)
	}
	if got := fileNames(body.Files); len(got) != 2 || got[0] != "dist/app.js.map" || got[1] != "index.js" {
		t.Errorf("files = %v, want [dist/app.js.map index.js]", got)
	}

	// When: the nested map, and a file under the skipped tree, are asked for
	// by path
	for _, want := range []string{"dist/app.js.map", "node_modules/dep/index.js"} {
		rec, body := getSource(t, h, want)
		if rec.Code != http.StatusOK || body.Filename != want || body.Source == "" {
			t.Errorf("%s: status %d body %+v, want the mounted file", want, rec.Code, body)
		}
	}

	// When: a path tries to climb out of the mount
	rec, _ = getSource(t, h, "../outside.txt")

	// Then: 404 — it is resolved inside the mount, where nothing is there
	if rec.Code != http.StatusNotFound {
		t.Errorf("status %d, want 404 for a path outside the mount", rec.Code)
	}
}

func TestGetFunctionSource_ignoresTheMountWhenHotReloadIsOff(t *testing.T) {
	// Given: the same tag, with OVERCAST_LAMBDA_HOT_RELOAD off — the function
	// runs its package
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "index.js"), []byte("mounted"), 0o644); err != nil {
		t.Fatal(err)
	}
	h := sourceHandler(t, false)
	fn := &Function{Runtime: "nodejs22.x", Handler: "index.handler", Tags: map[string]string{hotReloadTagKey: root}}
	fn.setCode(zipOf(t, map[string]string{"index.js": "packaged"}))
	seedSourceFunction(t, h, fn)

	// When: the source is read
	rec, body := getSource(t, h, "")

	// Then: it is the package's
	if rec.Code != http.StatusOK || body.Source != "packaged" {
		t.Errorf("status %d body %+v, want the packaged handler", rec.Code, body)
	}
}

func TestLanguageForFilename_sourceMapsAndTSX(t *testing.T) {
	for file, want := range map[string]string{
		"dist/index.js.map": "json",
		"app.tsx":           "typescript",
		"app.jsx":           "javascript",
		"lib.cts":           "typescript",
	} {
		if got := languageForFilename(file, "nodejs22.x"); got != want {
			t.Errorf("%s = %q, want %q", file, got, want)
		}
	}
}
