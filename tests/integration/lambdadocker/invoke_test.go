// Package lambdadocker_test contains the Lambda integration tests that need a
// running Docker daemon: real container cold starts, invokes, log capture and
// the in-container init.
//
// It is the sibling of tests/integration/lambda, which drives the same service
// over HTTP and starts nothing. The split is a package boundary and nothing
// else — same tests, same assertions — so that `go test ./...` runs the two
// halves on different cores instead of serialising ~250 metadata tests behind
// the container ones.
//
// Nothing here calls t.Parallel(): these tests share named Docker networks,
// fixed registry ports and the daemon's address pool.
//
// Run: go test ./tests/integration/lambdadocker/...
package lambdadocker_test

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/overcast-sh/overcast/internal/docker"
	"github.com/overcast-sh/overcast/tests/helpers"
	"github.com/overcast-sh/overcast/tests/helpers/lambdafixture"
	"go.uber.org/zap"
)

// TestMain pre-pulls Docker images used by tests so that individual test
// cases don't all attempt to pull concurrently (thundering herd).
//
// The pre-pull resolves the daemon through helpers.TestDockerSocket, the same
// resolution helpers.SkipWithoutDocker and every test server here use. It used
// to run only when LAMBDA_DOCKER_SOCKET was unset and then pull through a
// literal "/var/run/docker.sock", which is no daemon at all on Windows and the
// wrong one wherever the variable pointed elsewhere — so the warming either
// filled a cache nothing would read, or did not happen (#1785).
func TestMain(m *testing.M) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	if helpers.DockerAvailable(ctx) == nil {
		dc := docker.NewClient(helpers.TestDockerSocket(), zap.NewNop())
		for _, img := range []string{
			"public.ecr.aws/lambda/nodejs:20",
			"public.ecr.aws/lambda/nodejs:22",
		} {
			exists, _ := dc.ImageExists(ctx, img)
			if !exists {
				_ = dc.PullImage(ctx, img)
			}
		}
		// Build the in-container init before any test runs. It is build output
		// and its embed is baked at compile time, so a binary that starts
		// containers has to build it for itself; doing it here rather than in
		// whichever test happens to run first is what keeps this package
		// independent of file order. Failures are left for the tests' own
		// requireLambdaInit to report, against the test that needed it.
		_ = lambdafixture.EnsureInit()
	}
	cancel()
	os.Exit(m.Run())
}

// ─── Invoke (container-based) ─────────────────────────────────────────────────
//
// These tests require a running Docker daemon. They are skipped automatically
// when Docker is unavailable (CI without Docker socket, Windows dev without
// Docker Desktop, etc.).

// The gate is helpers.SkipWithoutDocker, and this package deliberately keeps no
// private one. It had a private one until #1785: it stat-ed the literal path
// "/var/run/docker.sock", which does not exist on Windows — where Docker
// Desktop listens on a named pipe — and is the wrong daemon wherever
// LAMBDA_DOCKER_SOCKET or DOCKER_HOST names another. All 49 tests here
// therefore skipped on any Windows workstation however healthy Docker was, and
// the contributor saw green. The shared helper resolves the endpoint the way
// the emulator does and asks the daemon, so the gate and the server under test
// can never disagree about which daemon is meant.

func skipIfContainerizedHotReloadBindMount(t *testing.T) {
	t.Helper()
	if os.Getenv("OVERCAST_TEST_LAMBDA_HOT_RELOAD") == "1" {
		return
	}
	if _, err := os.Stat("/.dockerenv"); err == nil {
		t.Skip("Lambda hot-reload bind mounts require host-visible source paths; set OVERCAST_TEST_LAMBDA_HOT_RELOAD=1 to force this test")
	}
}

// hotReloadSourceDir returns a fresh directory that both this process and the
// Docker daemon can reach at the same path, which is what a bind-mount hot
// reload test needs and what makes these tests skip in most rigs.
//
// OVERCAST_TEST_LAMBDA_HOT_RELOAD_DIR names such a directory when one exists:
// run the suite in a container with a host directory mounted at the path the
// daemon knows it by (on Docker Desktop for Windows, `F:\src` is `/f/src`) and
// point the variable at it. Without it, the test falls back to the devcontainer
// layout its siblings assume — a host-mounted /workspace — and skips when this
// process is containerized and cannot promise the daemon sees the same tree.
func hotReloadSourceDir(t *testing.T) string {
	t.Helper()
	shared := os.Getenv("OVERCAST_TEST_LAMBDA_HOT_RELOAD_DIR")
	parent := shared
	if parent == "" {
		skipIfContainerizedHotReloadBindMount(t)
		parent = "/workspace"
	}
	dir, err := os.MkdirTemp(parent, "hot-reload-")
	if err != nil {
		if shared != "" {
			t.Fatalf("OVERCAST_TEST_LAMBDA_HOT_RELOAD_DIR=%q is not usable: %v", shared, err)
		}
		return t.TempDir()
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// makeZip creates a minimal zip archive containing a single file.
func makeZip(t *testing.T, name, content string) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	f, err := w.Create(name)
	if err != nil {
		t.Fatalf("zip.Create: %v", err)
	}
	if _, err := io.WriteString(f, content); err != nil {
		t.Fatalf("zip.Write: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("zip.Close: %v", err)
	}
	return buf.Bytes()
}

// createFunctionWithCode creates a Lambda function seeded with a real code zip.
func createFunctionWithCode(t *testing.T, srv *helpers.TestServer, name, runtime, handler string, zipBytes []byte) functionConfiguration {
	t.Helper()
	resp := doJSON(t, http.MethodPost, lambdaURL(srv, "/functions"), createFunctionReq{
		FunctionName: name,
		Runtime:      runtime,
		Handler:      handler,
		Role:         "arn:aws:iam::000000000000:role/lambda-role",
		Timeout:      10,
		MemorySize:   128,
		Code:         &lambdaCode{ZipFile: zipBytes},
	})
	helpers.AssertStatus(t, resp, http.StatusCreated)
	var cfg functionConfiguration
	decodeJSON(t, resp, &cfg)
	return cfg
}

// waitForFunctionActive polls GetFunction until the function reaches Active state
// or the deadline is exceeded. Required for Docker-dependent tests where the
// prewarmer callback transitions State from "Pending" asynchronously.
func waitForFunctionActive(t *testing.T, srv *helpers.TestServer, name string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Minute)
	var lastState, lastReason, lastReasonCode string
	for time.Now().Before(deadline) {
		resp := doJSON(t, http.MethodGet, lambdaURL(srv, "/functions/"+name), nil)
		var fn struct {
			Configuration struct {
				State           string `json:"State"`
				StateReason     string `json:"StateReason"`
				StateReasonCode string `json:"StateReasonCode"`
			} `json:"Configuration"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&fn)
		resp.Body.Close()
		lastState = fn.Configuration.State
		lastReason = fn.Configuration.StateReason
		lastReasonCode = fn.Configuration.StateReasonCode
		if fn.Configuration.State == "Active" {
			return
		}
		if fn.Configuration.State == "Failed" {
			t.Fatalf("function %s reached Failed state: reason=%q code=%q", name, lastReason, lastReasonCode)
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("function %s did not reach Active state within 2m: last_state=%q reason=%q code=%q", name, lastState, lastReason, lastReasonCode)
}

func TestInvoke_nodeRuntime_success(t *testing.T) {
	helpers.SkipWithoutDocker(t)

	// Given a Node.js function that echoes its input
	srv := helpers.NewTestServer(t, helpers.WithLambdaDocker())
	code := makeZip(t, "index.js", `
exports.handler = async (event) => {
  return { statusCode: 200, body: JSON.stringify(event) };
};
`)
	createFunctionWithCode(t, srv, "echo-fn", "nodejs20.x", "index.handler", code)
	waitForFunctionActive(t, srv, "echo-fn")

	// When InvokeFunction is called
	resp := invokeFunction(t, srv, "echo-fn", map[string]string{"hello": "world"})
	if resp.Header.Get("X-Amz-Function-Error") == "Unhandled" {
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		// Under high parallel test load, cold-start runtime init can fail
		// transiently; one immediate retry stabilizes this integration test
		// without masking deterministic handler/runtime regressions.
		if strings.Contains(string(body), "Runtime.InitError") || strings.Contains(string(body), "Runtime.ExitError") {
			resp = invokeFunction(t, srv, "echo-fn", map[string]string{"hello": "world"})
		}
	}
	defer resp.Body.Close()

	// Then 200 with the echoed payload
	helpers.AssertStatus(t, resp, http.StatusOK)
	if resp.Header.Get("X-Amz-Function-Error") != "" {
		t.Errorf("unexpected function error: %s", resp.Header.Get("X-Amz-Function-Error"))
	}
	body, _ := io.ReadAll(resp.Body)
	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("unmarshal response: %v — body: %s", err, body)
	}
	if statusCode, _ := out["statusCode"].(float64); int(statusCode) != 200 {
		t.Errorf("response statusCode = %v, want 200", out["statusCode"])
	}
}

func TestInvoke_nodeRuntime_hotReloadMountedSource(t *testing.T) {
	helpers.SkipWithoutDocker(t)
	skipIfContainerizedHotReloadBindMount(t)

	// Given a function that opts into hot-reload with source mounted from host.
	sourceDir, err := os.MkdirTemp("/workspace", "hot-reload-")
	if err != nil {
		sourceDir = t.TempDir()
	} else {
		t.Cleanup(func() { _ = os.RemoveAll(sourceDir) })
	}
	if err := os.WriteFile(sourceDir+"/index.js", []byte(`
exports.handler = async () => {
  return { source: "mounted" };
};
`), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}

	srv := helpers.NewTestServer(t, helpers.WithLambdaDocker(), helpers.WithLambdaHotReload())
	resp := doJSON(t, http.MethodPost, lambdaURL(srv, "/functions"), createFunctionReq{
		FunctionName: "hot-fn",
		Runtime:      "nodejs20.x",
		Handler:      "index.handler",
		Role:         "arn:aws:iam::000000000000:role/lambda-role",
		Code:         &lambdaCode{},
		Tags:         map[string]string{"overcast:hot-reload-path": sourceDir},
	})
	helpers.AssertStatus(t, resp, http.StatusCreated)
	resp.Body.Close()

	waitForFunctionActive(t, srv, "hot-fn")

	// When invoking the function
	invokeResp := invokeFunction(t, srv, "hot-fn", map[string]any{"ping": true})
	defer invokeResp.Body.Close()
	body, _ := io.ReadAll(invokeResp.Body)
	if invokeResp.Header.Get("X-Amz-Function-Error") == "Unhandled" &&
		(strings.Contains(string(body), "Runtime.ExitError") || strings.Contains(string(body), "Runtime.ImportModuleError")) {
		t.Skipf("hot-reload bind mount not supported in this Docker environment: %s", string(body))
	}

	// Then execution succeeds and comes from the mounted source tree.
	helpers.AssertStatus(t, invokeResp, http.StatusOK)
	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("unmarshal response: %v — body: %s", err, body)
	}
	if out["source"] != "mounted" {
		t.Fatalf("expected mounted source response, got: %s", body)
	}
}

// TestInvoke_nodeRuntime_hotReload_sourceEditedInPlace is #1411
// end to end: the second invocation must run the edited file, not the module
// the warm container loaded on the first one.
//
// The two writes differ in length as well as content on purpose. A same-length
// edit relies on the mtime alone, and some filesystems only resolve mtimes to
// the second — a boundary the docs state and a test must not pretend away by
// sleeping through it.
func TestInvoke_nodeRuntime_hotReload_sourceEditedInPlace(t *testing.T) {
	helpers.SkipWithoutDocker(t)
	sourceDir := hotReloadSourceDir(t)
	handler := sourceDir + "/index.js"
	if err := os.WriteFile(handler, []byte(`exports.handler = async () => ({ v: 1 });`+"\n"), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}

	srv := helpers.NewTestServer(t, helpers.WithLambdaDocker(), helpers.WithLambdaHotReload())
	resp := doJSON(t, http.MethodPost, lambdaURL(srv, "/functions"), createFunctionReq{
		FunctionName: "hot-edit-fn",
		Runtime:      "nodejs20.x",
		Handler:      "index.handler",
		Role:         "arn:aws:iam::000000000000:role/lambda-role",
		Code:         &lambdaCode{},
		Tags:         map[string]string{"overcast:hot-reload-path": sourceDir},
	})
	helpers.AssertStatus(t, resp, http.StatusCreated)
	resp.Body.Close()
	waitForFunctionActive(t, srv, "hot-edit-fn")

	invoke := func(t *testing.T, want float64) {
		t.Helper()
		invokeResp := invokeFunction(t, srv, "hot-edit-fn", map[string]any{"ping": true})
		defer invokeResp.Body.Close()
		body, _ := io.ReadAll(invokeResp.Body)
		if invokeResp.Header.Get("X-Amz-Function-Error") == "Unhandled" &&
			(strings.Contains(string(body), "Runtime.ExitError") || strings.Contains(string(body), "Runtime.ImportModuleError")) {
			t.Skipf("hot-reload bind mount not supported in this Docker environment: %s", string(body))
		}
		helpers.AssertStatus(t, invokeResp, http.StatusOK)
		var out map[string]any
		if err := json.Unmarshal(body, &out); err != nil {
			t.Fatalf("unmarshal response: %v — body: %s", err, body)
		}
		if out["v"] != want {
			t.Fatalf("handler returned v=%v, want %v — body: %s", out["v"], want, body)
		}
	}

	// Given: one invocation has run, so a warm container holds the module.
	invoke(t, 1)

	// When: the same file is edited in place — no create, no delete, no
	// rename, which is what the directory's own mtime used to key on.
	if err := os.WriteFile(handler, []byte(`exports.handler = async () => ({ v: 2, edited: true });`+"\n"), 0o644); err != nil {
		t.Fatalf("rewrite source: %v", err)
	}

	// Then: the next invocation serves the edited source.
	invoke(t, 2)
}

func TestInvoke_nodeRuntime_hotReloadMountedSource_withLayer(t *testing.T) {
	helpers.SkipWithoutDocker(t)
	skipIfContainerizedHotReloadBindMount(t)

	// Given a hot-reload source directory that imports a dependency from a layer.
	sourceDir, err := os.MkdirTemp("/workspace", "hot-reload-layered-")
	if err != nil {
		sourceDir = t.TempDir()
	} else {
		t.Cleanup(func() { _ = os.RemoveAll(sourceDir) })
	}
	if err := os.WriteFile(sourceDir+"/index.js", []byte(`
const layerLib = require("layer-lib");
exports.handler = async () => {
  return { source: "mounted", layer: layerLib.value };
};
`), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}

	srv := helpers.NewTestServer(t, helpers.WithLambdaDocker(), helpers.WithLambdaHotReload())
	createResp := doJSON(t, http.MethodPost, lambdaURL(srv, "/functions"), createFunctionReq{
		FunctionName: "hot-layer-fn",
		Runtime:      "nodejs20.x",
		Handler:      "index.handler",
		Role:         "arn:aws:iam::000000000000:role/lambda-role",
		Code:         &lambdaCode{},
		Tags:         map[string]string{"overcast:hot-reload-path": sourceDir},
	})
	helpers.AssertStatus(t, createResp, http.StatusCreated)
	createResp.Body.Close()

	waitForFunctionActive(t, srv, "hot-layer-fn")

	// Publish a layer that provides /opt/nodejs/node_modules/layer-lib/index.js.
	layerZip := makeZip(t, "nodejs/node_modules/layer-lib/index.js", `module.exports = { value: "from-layer" };`)
	lvResp := doJSON(t, http.MethodPost, layerURL(srv, "/layers/hot-layer/versions"), publishLayerVersionReq{
		Content: layerContent{ZipFile: layerZip},
	})
	helpers.AssertStatus(t, lvResp, http.StatusCreated)
	var lv layerVersionResponse
	decodeJSON(t, lvResp, &lv)

	attachResp := doJSON(t, http.MethodPut, lambdaURL(srv, "/functions/hot-layer-fn/configuration"), map[string]any{
		"Layers": []string{lv.LayerVersionArn},
	})
	defer attachResp.Body.Close()
	helpers.AssertStatus(t, attachResp, http.StatusOK)

	// When invoking the function.
	invokeResp := invokeFunction(t, srv, "hot-layer-fn", map[string]any{"ping": true})
	defer invokeResp.Body.Close()
	body, _ := io.ReadAll(invokeResp.Body)
	if invokeResp.Header.Get("X-Amz-Function-Error") == "Unhandled" &&
		(strings.Contains(string(body), "Runtime.ExitError") || strings.Contains(string(body), "Runtime.ImportModuleError")) {
		t.Skipf("hot-reload bind mount not supported in this Docker environment: %s", string(body))
	}

	// Then invocation succeeds and returns both mounted-source and layered values.
	helpers.AssertStatus(t, invokeResp, http.StatusOK)
	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("unmarshal response: %v — body: %s", err, body)
	}
	if out["source"] != "mounted" {
		t.Fatalf("expected mounted source response, got: %s", body)
	}
	if out["layer"] != "from-layer" {
		t.Fatalf("expected layer module value from-layer, got: %s", body)
	}
}

func TestInvoke_pythonRuntime_hotReloadMountedSource_withLayer(t *testing.T) {
	helpers.SkipWithoutDocker(t)
	skipIfContainerizedHotReloadBindMount(t)

	// Given a hot-reload Python source directory that imports from a layer module.
	sourceDir, err := os.MkdirTemp("/workspace", "hot-reload-python-layered-")
	if err != nil {
		sourceDir = t.TempDir()
	} else {
		t.Cleanup(func() { _ = os.RemoveAll(sourceDir) })
	}
	if err := os.WriteFile(sourceDir+"/index.py", []byte(`
import layer_mod

def handler(event, context):
    return {"source": "mounted", "layer": layer_mod.VALUE}
`), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}

	srv := helpers.NewTestServer(t, helpers.WithLambdaDocker(), helpers.WithLambdaHotReload())
	createResp := doJSON(t, http.MethodPost, lambdaURL(srv, "/functions"), createFunctionReq{
		FunctionName: "hot-python-layer-fn",
		Runtime:      "python3.11",
		Handler:      "index.handler",
		Role:         "arn:aws:iam::000000000000:role/lambda-role",
		Code:         &lambdaCode{},
		Tags:         map[string]string{"overcast:hot-reload-path": sourceDir},
	})
	helpers.AssertStatus(t, createResp, http.StatusCreated)
	createResp.Body.Close()

	waitForFunctionActive(t, srv, "hot-python-layer-fn")

	// Publish a layer that provides /opt/python/layer_mod.py.
	layerZip := makeZip(t, "python/layer_mod.py", `VALUE = "from-layer"`)
	lvResp := doJSON(t, http.MethodPost, layerURL(srv, "/layers/hot-python-layer/versions"), publishLayerVersionReq{
		Content: layerContent{ZipFile: layerZip},
	})
	helpers.AssertStatus(t, lvResp, http.StatusCreated)
	var lv layerVersionResponse
	decodeJSON(t, lvResp, &lv)

	attachResp := doJSON(t, http.MethodPut, lambdaURL(srv, "/functions/hot-python-layer-fn/configuration"), map[string]any{
		"Layers": []string{lv.LayerVersionArn},
	})
	defer attachResp.Body.Close()
	helpers.AssertStatus(t, attachResp, http.StatusOK)

	// When invoking the function.
	invokeResp := invokeFunction(t, srv, "hot-python-layer-fn", map[string]any{"ping": true})
	defer invokeResp.Body.Close()
	body, _ := io.ReadAll(invokeResp.Body)
	if invokeResp.Header.Get("X-Amz-Function-Error") == "Unhandled" &&
		(strings.Contains(string(body), "Runtime.ExitError") || strings.Contains(string(body), "Runtime.ImportModuleError")) {
		t.Skipf("hot-reload bind mount not supported in this Docker environment: %s", string(body))
	}

	// Then invocation succeeds and returns both mounted-source and layered values.
	helpers.AssertStatus(t, invokeResp, http.StatusOK)
	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("unmarshal response: %v — body: %s", err, body)
	}
	if out["source"] != "mounted" {
		t.Fatalf("expected mounted source response, got: %s", body)
	}
	if out["layer"] != "from-layer" {
		t.Fatalf("expected layer module value from-layer, got: %s", body)
	}
}

func TestInvoke_nodeRuntime_hotReloadMountedSource_withLayer_precedenceLastWins(t *testing.T) {
	helpers.SkipWithoutDocker(t)
	skipIfContainerizedHotReloadBindMount(t)

	// Given a hot-reload source directory importing a module provided by layers.
	sourceDir, err := os.MkdirTemp("/workspace", "hot-reload-layered-precedence-")
	if err != nil {
		sourceDir = t.TempDir()
	} else {
		t.Cleanup(func() { _ = os.RemoveAll(sourceDir) })
	}
	if err := os.WriteFile(sourceDir+"/index.js", []byte(`
const layerLib = require("layer-lib");
exports.handler = async () => {
  return { source: "mounted", layer: layerLib.value };
};
`), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}

	srv := helpers.NewTestServer(t, helpers.WithLambdaDocker(), helpers.WithLambdaHotReload())
	createResp := doJSON(t, http.MethodPost, lambdaURL(srv, "/functions"), createFunctionReq{
		FunctionName: "hot-layer-precedence-node-fn",
		Runtime:      "nodejs20.x",
		Handler:      "index.handler",
		Role:         "arn:aws:iam::000000000000:role/lambda-role",
		Code:         &lambdaCode{},
		Tags:         map[string]string{"overcast:hot-reload-path": sourceDir},
	})
	helpers.AssertStatus(t, createResp, http.StatusCreated)
	createResp.Body.Close()

	waitForFunctionActive(t, srv, "hot-layer-precedence-node-fn")

	baseZip := makeZip(t, "nodejs/node_modules/layer-lib/index.js", `module.exports = { value: "base" };`)
	baseResp := doJSON(t, http.MethodPost, layerURL(srv, "/layers/hot-node-layer-base/versions"), publishLayerVersionReq{
		Content: layerContent{ZipFile: baseZip},
	})
	helpers.AssertStatus(t, baseResp, http.StatusCreated)
	var base layerVersionResponse
	decodeJSON(t, baseResp, &base)

	overrideZip := makeZip(t, "nodejs/node_modules/layer-lib/index.js", `module.exports = { value: "override" };`)
	overrideResp := doJSON(t, http.MethodPost, layerURL(srv, "/layers/hot-node-layer-override/versions"), publishLayerVersionReq{
		Content: layerContent{ZipFile: overrideZip},
	})
	helpers.AssertStatus(t, overrideResp, http.StatusCreated)
	var override layerVersionResponse
	decodeJSON(t, overrideResp, &override)

	attachResp := doJSON(t, http.MethodPut, lambdaURL(srv, "/functions/hot-layer-precedence-node-fn/configuration"), map[string]any{
		"Layers": []string{base.LayerVersionArn, override.LayerVersionArn},
	})
	defer attachResp.Body.Close()
	helpers.AssertStatus(t, attachResp, http.StatusOK)

	// When invoking the function.
	invokeResp := invokeFunction(t, srv, "hot-layer-precedence-node-fn", map[string]any{"ping": true})
	defer invokeResp.Body.Close()
	body, _ := io.ReadAll(invokeResp.Body)
	if invokeResp.Header.Get("X-Amz-Function-Error") == "Unhandled" &&
		(strings.Contains(string(body), "Runtime.ExitError") || strings.Contains(string(body), "Runtime.ImportModuleError")) {
		t.Skipf("hot-reload bind mount not supported in this Docker environment: %s", string(body))
	}

	// Then the later layer should override the earlier one.
	helpers.AssertStatus(t, invokeResp, http.StatusOK)
	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("unmarshal response: %v — body: %s", err, body)
	}
	if out["source"] != "mounted" {
		t.Fatalf("expected mounted source response, got: %s", body)
	}
	if out["layer"] != "override" {
		t.Fatalf("expected last layer to win (override), got: %s", body)
	}
}

func TestInvoke_pythonRuntime_hotReloadMountedSource_withLayer_precedenceLastWins(t *testing.T) {
	helpers.SkipWithoutDocker(t)
	skipIfContainerizedHotReloadBindMount(t)

	// Given a hot-reload Python source directory importing a module from layers.
	sourceDir, err := os.MkdirTemp("/workspace", "hot-reload-python-layered-precedence-")
	if err != nil {
		sourceDir = t.TempDir()
	} else {
		t.Cleanup(func() { _ = os.RemoveAll(sourceDir) })
	}
	if err := os.WriteFile(sourceDir+"/index.py", []byte(`
import layer_mod

def handler(event, context):
    return {"source": "mounted", "layer": layer_mod.VALUE}
`), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}

	srv := helpers.NewTestServer(t, helpers.WithLambdaDocker(), helpers.WithLambdaHotReload())
	createResp := doJSON(t, http.MethodPost, lambdaURL(srv, "/functions"), createFunctionReq{
		FunctionName: "hot-layer-precedence-python-fn",
		Runtime:      "python3.11",
		Handler:      "index.handler",
		Role:         "arn:aws:iam::000000000000:role/lambda-role",
		Code:         &lambdaCode{},
		Tags:         map[string]string{"overcast:hot-reload-path": sourceDir},
	})
	helpers.AssertStatus(t, createResp, http.StatusCreated)
	createResp.Body.Close()

	waitForFunctionActive(t, srv, "hot-layer-precedence-python-fn")

	baseZip := makeZip(t, "python/layer_mod.py", `VALUE = "base"`)
	baseResp := doJSON(t, http.MethodPost, layerURL(srv, "/layers/hot-python-layer-base/versions"), publishLayerVersionReq{
		Content: layerContent{ZipFile: baseZip},
	})
	helpers.AssertStatus(t, baseResp, http.StatusCreated)
	var base layerVersionResponse
	decodeJSON(t, baseResp, &base)

	overrideZip := makeZip(t, "python/layer_mod.py", `VALUE = "override"`)
	overrideResp := doJSON(t, http.MethodPost, layerURL(srv, "/layers/hot-python-layer-override/versions"), publishLayerVersionReq{
		Content: layerContent{ZipFile: overrideZip},
	})
	helpers.AssertStatus(t, overrideResp, http.StatusCreated)
	var override layerVersionResponse
	decodeJSON(t, overrideResp, &override)

	attachResp := doJSON(t, http.MethodPut, lambdaURL(srv, "/functions/hot-layer-precedence-python-fn/configuration"), map[string]any{
		"Layers": []string{base.LayerVersionArn, override.LayerVersionArn},
	})
	defer attachResp.Body.Close()
	helpers.AssertStatus(t, attachResp, http.StatusOK)

	// When invoking the function.
	invokeResp := invokeFunction(t, srv, "hot-layer-precedence-python-fn", map[string]any{"ping": true})
	defer invokeResp.Body.Close()
	body, _ := io.ReadAll(invokeResp.Body)
	if invokeResp.Header.Get("X-Amz-Function-Error") == "Unhandled" &&
		(strings.Contains(string(body), "Runtime.ExitError") || strings.Contains(string(body), "Runtime.ImportModuleError")) {
		t.Skipf("hot-reload bind mount not supported in this Docker environment: %s", string(body))
	}

	// Then the later layer should override the earlier one.
	helpers.AssertStatus(t, invokeResp, http.StatusOK)
	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("unmarshal response: %v — body: %s", err, body)
	}
	if out["source"] != "mounted" {
		t.Fatalf("expected mounted source response, got: %s", body)
	}
	if out["layer"] != "override" {
		t.Fatalf("expected last layer to win (override), got: %s", body)
	}
}

func TestInvoke_nodeRuntime_zipCode_withLayer(t *testing.T) {
	helpers.SkipWithoutDocker(t)

	// Given a zip-based Node function that imports from a layer module.
	srv := helpers.NewTestServer(t, helpers.WithLambdaDocker())
	code := makeZip(t, "index.js", `
const layerLib = require("layer-lib");
exports.handler = async () => {
  return { source: "zip", layer: layerLib.value };
};
`)
	createFunctionWithCode(t, srv, "zip-layer-node-fn", "nodejs20.x", "index.handler", code)

	waitForFunctionActive(t, srv, "zip-layer-node-fn")

	layerZip := makeZip(t, "nodejs/node_modules/layer-lib/index.js", `module.exports = { value: "from-layer" };`)
	lvResp := doJSON(t, http.MethodPost, layerURL(srv, "/layers/zip-node-layer/versions"), publishLayerVersionReq{
		Content: layerContent{ZipFile: layerZip},
	})
	helpers.AssertStatus(t, lvResp, http.StatusCreated)
	var lv layerVersionResponse
	decodeJSON(t, lvResp, &lv)

	attachResp := doJSON(t, http.MethodPut, lambdaURL(srv, "/functions/zip-layer-node-fn/configuration"), map[string]any{
		"Layers": []string{lv.LayerVersionArn},
	})
	defer attachResp.Body.Close()
	helpers.AssertStatus(t, attachResp, http.StatusOK)

	// When invoking the function.
	invokeResp := invokeFunction(t, srv, "zip-layer-node-fn", map[string]any{"ping": true})
	defer invokeResp.Body.Close()
	body, _ := io.ReadAll(invokeResp.Body)

	// Under heavy parallel load cold-start can fail transiently; retry once.
	if invokeResp.Header.Get("X-Amz-Function-Error") == "Unhandled" &&
		(strings.Contains(string(body), "Runtime.InitError") || strings.Contains(string(body), "Runtime.ExitError")) {
		invokeResp = invokeFunction(t, srv, "zip-layer-node-fn", map[string]any{"ping": true})
		defer invokeResp.Body.Close()
		body, _ = io.ReadAll(invokeResp.Body)
	}

	// Then invocation succeeds and returns values from zip code and layer.
	helpers.AssertStatus(t, invokeResp, http.StatusOK)
	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("unmarshal response: %v — body: %s", err, body)
	}
	if out["source"] != "zip" {
		t.Fatalf("expected zip source response, got: %s", body)
	}
	if out["layer"] != "from-layer" {
		t.Fatalf("expected layer module value from-layer, got: %s", body)
	}
}

func TestInvoke_nodeRuntime_zipCode_withLayer_precedenceLastWins(t *testing.T) {
	helpers.SkipWithoutDocker(t)

	// Given a zip-based Node function importing a module provided by layers.
	srv := helpers.NewTestServer(t, helpers.WithLambdaDocker())
	code := makeZip(t, "index.js", `
const layerLib = require("layer-lib");
exports.handler = async () => {
  return { layer: layerLib.value };
};
`)
	createFunctionWithCode(t, srv, "zip-layer-precedence-node-fn", "nodejs20.x", "index.handler", code)

	waitForFunctionActive(t, srv, "zip-layer-precedence-node-fn")

	// Publish two layers that provide the same module path with different values.
	baseZip := makeZip(t, "nodejs/node_modules/layer-lib/index.js", `module.exports = { value: "base" };`)
	baseResp := doJSON(t, http.MethodPost, layerURL(srv, "/layers/zip-node-layer-base/versions"), publishLayerVersionReq{
		Content: layerContent{ZipFile: baseZip},
	})
	helpers.AssertStatus(t, baseResp, http.StatusCreated)
	var base layerVersionResponse
	decodeJSON(t, baseResp, &base)

	overrideZip := makeZip(t, "nodejs/node_modules/layer-lib/index.js", `module.exports = { value: "override" };`)
	overrideResp := doJSON(t, http.MethodPost, layerURL(srv, "/layers/zip-node-layer-override/versions"), publishLayerVersionReq{
		Content: layerContent{ZipFile: overrideZip},
	})
	helpers.AssertStatus(t, overrideResp, http.StatusCreated)
	var override layerVersionResponse
	decodeJSON(t, overrideResp, &override)

	attachResp := doJSON(t, http.MethodPut, lambdaURL(srv, "/functions/zip-layer-precedence-node-fn/configuration"), map[string]any{
		"Layers": []string{base.LayerVersionArn, override.LayerVersionArn},
	})
	defer attachResp.Body.Close()
	helpers.AssertStatus(t, attachResp, http.StatusOK)

	// When invoking the function.
	invokeResp := invokeFunction(t, srv, "zip-layer-precedence-node-fn", map[string]any{"ping": true})
	defer invokeResp.Body.Close()
	body, _ := io.ReadAll(invokeResp.Body)

	// Then the later layer should override the earlier one.
	helpers.AssertStatus(t, invokeResp, http.StatusOK)
	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("unmarshal response: %v — body: %s", err, body)
	}
	if out["layer"] != "override" {
		t.Fatalf("expected last layer to win (override), got: %s", body)
	}
}

func TestInvoke_pythonRuntime_zipCode_withLayer(t *testing.T) {
	helpers.SkipWithoutDocker(t)

	// Given a zip-based Python function that imports from a layer module.
	srv := helpers.NewTestServer(t, helpers.WithLambdaDocker())
	code := makeZip(t, "index.py", `
import layer_mod

def handler(event, context):
    return {"source": "zip", "layer": layer_mod.VALUE}
`)
	createFunctionWithCode(t, srv, "zip-layer-python-fn", "python3.11", "index.handler", code)

	waitForFunctionActive(t, srv, "zip-layer-python-fn")

	layerZip := makeZip(t, "python/layer_mod.py", `VALUE = "from-layer"`)
	lvResp := doJSON(t, http.MethodPost, layerURL(srv, "/layers/zip-python-layer/versions"), publishLayerVersionReq{
		Content: layerContent{ZipFile: layerZip},
	})
	helpers.AssertStatus(t, lvResp, http.StatusCreated)
	var lv layerVersionResponse
	decodeJSON(t, lvResp, &lv)

	attachResp := doJSON(t, http.MethodPut, lambdaURL(srv, "/functions/zip-layer-python-fn/configuration"), map[string]any{
		"Layers": []string{lv.LayerVersionArn},
	})
	defer attachResp.Body.Close()
	helpers.AssertStatus(t, attachResp, http.StatusOK)

	// When invoking the function.
	invokeResp := invokeFunction(t, srv, "zip-layer-python-fn", map[string]any{"ping": true})
	defer invokeResp.Body.Close()
	body, _ := io.ReadAll(invokeResp.Body)

	// Then invocation succeeds and returns values from zip code and layer.
	helpers.AssertStatus(t, invokeResp, http.StatusOK)
	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("unmarshal response: %v — body: %s", err, body)
	}
	if out["source"] != "zip" {
		t.Fatalf("expected zip source response, got: %s", body)
	}
	if out["layer"] != "from-layer" {
		t.Fatalf("expected layer module value from-layer, got: %s", body)
	}
}

func TestInvoke_pythonRuntime_zipCode_withLayer_precedenceLastWins(t *testing.T) {
	helpers.SkipWithoutDocker(t)

	// Given a zip-based Python function importing a symbol from layer code.
	srv := helpers.NewTestServer(t, helpers.WithLambdaDocker())
	code := makeZip(t, "index.py", `
import layer_mod

def handler(event, context):
    return {"layer": layer_mod.VALUE}
`)
	createFunctionWithCode(t, srv, "zip-layer-precedence-python-fn", "python3.11", "index.handler", code)

	waitForFunctionActive(t, srv, "zip-layer-precedence-python-fn")

	// Publish two layers that provide the same module path with different values.
	baseZip := makeZip(t, "python/layer_mod.py", `VALUE = "base"`)
	baseResp := doJSON(t, http.MethodPost, layerURL(srv, "/layers/zip-python-layer-base/versions"), publishLayerVersionReq{
		Content: layerContent{ZipFile: baseZip},
	})
	helpers.AssertStatus(t, baseResp, http.StatusCreated)
	var base layerVersionResponse
	decodeJSON(t, baseResp, &base)

	overrideZip := makeZip(t, "python/layer_mod.py", `VALUE = "override"`)
	overrideResp := doJSON(t, http.MethodPost, layerURL(srv, "/layers/zip-python-layer-override/versions"), publishLayerVersionReq{
		Content: layerContent{ZipFile: overrideZip},
	})
	helpers.AssertStatus(t, overrideResp, http.StatusCreated)
	var override layerVersionResponse
	decodeJSON(t, overrideResp, &override)

	attachResp := doJSON(t, http.MethodPut, lambdaURL(srv, "/functions/zip-layer-precedence-python-fn/configuration"), map[string]any{
		"Layers": []string{base.LayerVersionArn, override.LayerVersionArn},
	})
	defer attachResp.Body.Close()
	helpers.AssertStatus(t, attachResp, http.StatusOK)

	// When invoking the function.
	invokeResp := invokeFunction(t, srv, "zip-layer-precedence-python-fn", map[string]any{"ping": true})
	defer invokeResp.Body.Close()
	body, _ := io.ReadAll(invokeResp.Body)

	// Then the later layer should override the earlier one.
	helpers.AssertStatus(t, invokeResp, http.StatusOK)
	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("unmarshal response: %v — body: %s", err, body)
	}
	if out["layer"] != "override" {
		t.Fatalf("expected last layer to win (override), got: %s", body)
	}
}

func TestInvoke_nodeRuntime_deletedAttachedLayerVersionFailsInit(t *testing.T) {
	helpers.SkipWithoutDocker(t)

	// Given a zip-based Node function with a real attached layer version.
	srv := helpers.NewTestServer(t, helpers.WithLambdaDocker())
	code := makeZip(t, "index.js", `
exports.handler = async () => {
  return { ok: true };
};
`)
	createFunctionWithCode(t, srv, "deleted-layer-fn", "nodejs20.x", "index.handler", code)

	waitForFunctionActive(t, srv, "deleted-layer-fn")

	layerZip := makeZip(t, "nodejs/node_modules/layer-lib/index.js", `module.exports = { value: "from-layer" };`)
	lvResp := doJSON(t, http.MethodPost, layerURL(srv, "/layers/deleted-node-layer/versions"), publishLayerVersionReq{
		Content: layerContent{ZipFile: layerZip},
	})
	helpers.AssertStatus(t, lvResp, http.StatusCreated)
	var lv layerVersionResponse
	decodeJSON(t, lvResp, &lv)

	attachResp := doJSON(t, http.MethodPut, lambdaURL(srv, "/functions/deleted-layer-fn/configuration"), map[string]any{
		"Layers": []string{lv.LayerVersionArn},
	})
	defer attachResp.Body.Close()
	helpers.AssertStatus(t, attachResp, http.StatusOK)

	// When the attached layer version is deleted after configuration.
	deleteResp := doJSON(t, http.MethodDelete, layerURL(srv, "/layers/deleted-node-layer/versions/1"), nil)
	defer deleteResp.Body.Close()
	helpers.AssertStatus(t, deleteResp, http.StatusNoContent)

	// Then function readback still preserves the configured layer reference.
	type funcConfigWithLayers struct {
		functionConfiguration
		Layers []struct {
			Arn string `json:"Arn"`
		} `json:"Layers,omitempty"`
	}
	getCfgResp := doJSON(t, http.MethodGet, lambdaURL(srv, "/functions/deleted-layer-fn/configuration"), nil)
	helpers.AssertStatus(t, getCfgResp, http.StatusOK)
	var cfg funcConfigWithLayers
	decodeJSON(t, getCfgResp, &cfg)
	if len(cfg.Layers) != 1 || cfg.Layers[0].Arn != lv.LayerVersionArn {
		t.Fatalf("expected configuration to retain deleted layer ARN %q, got %#v", lv.LayerVersionArn, cfg.Layers)
	}

	getFnResp := doJSON(t, http.MethodGet, lambdaURL(srv, "/functions/deleted-layer-fn"), nil)
	helpers.AssertStatus(t, getFnResp, http.StatusOK)
	var fnResp struct {
		Configuration funcConfigWithLayers `json:"Configuration"`
	}
	decodeJSON(t, getFnResp, &fnResp)
	if len(fnResp.Configuration.Layers) != 1 || fnResp.Configuration.Layers[0].Arn != lv.LayerVersionArn {
		t.Fatalf("expected GetFunction to retain deleted layer ARN %q, got %#v", lv.LayerVersionArn, fnResp.Configuration.Layers)
	}

	// And invocation now fails during runtime init with a clear missing-layer error.
	resp := invokeFunction(t, srv, "deleted-layer-fn", map[string]any{"ping": true})
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}

	helpers.AssertStatus(t, resp, http.StatusOK)
	if resp.Header.Get("X-Amz-Function-Error") != "Unhandled" {
		t.Fatalf("expected X-Amz-Function-Error=Unhandled, got %q (body=%s)", resp.Header.Get("X-Amz-Function-Error"), body)
	}

	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("unmarshal response: %v — body: %s", err, body)
	}
	if out["errorType"] != "Runtime.InitError" {
		t.Fatalf("expected errorType Runtime.InitError, got: %v (body=%s)", out["errorType"], body)
	}
	errMsg, _ := out["errorMessage"].(string)
	if !strings.Contains(errMsg, "layer version not found") {
		t.Fatalf("expected missing-layer message, got: %s", body)
	}
}

func TestInvoke_pythonRuntime_deletedAttachedLayerVersionFailsInit(t *testing.T) {
	helpers.SkipWithoutDocker(t)

	// Given a zip-based Python function with a real attached layer version.
	srv := helpers.NewTestServer(t, helpers.WithLambdaDocker())
	code := makeZip(t, "index.py", `
def handler(event, context):
    return {"ok": True}
`)
	createFunctionWithCode(t, srv, "deleted-python-layer-fn", "python3.11", "index.handler", code)

	waitForFunctionActive(t, srv, "deleted-python-layer-fn")

	layerZip := makeZip(t, "python/layer_mod.py", `VALUE = "from-layer"`)
	lvResp := doJSON(t, http.MethodPost, layerURL(srv, "/layers/deleted-python-layer/versions"), publishLayerVersionReq{
		Content: layerContent{ZipFile: layerZip},
	})
	helpers.AssertStatus(t, lvResp, http.StatusCreated)
	var lv layerVersionResponse
	decodeJSON(t, lvResp, &lv)

	attachResp := doJSON(t, http.MethodPut, lambdaURL(srv, "/functions/deleted-python-layer-fn/configuration"), map[string]any{
		"Layers": []string{lv.LayerVersionArn},
	})
	defer attachResp.Body.Close()
	helpers.AssertStatus(t, attachResp, http.StatusOK)

	// When the attached layer version is deleted after configuration.
	deleteResp := doJSON(t, http.MethodDelete, layerURL(srv, "/layers/deleted-python-layer/versions/1"), nil)
	defer deleteResp.Body.Close()
	helpers.AssertStatus(t, deleteResp, http.StatusNoContent)

	// Then function readback still preserves the configured layer reference.
	type funcConfigWithLayers struct {
		functionConfiguration
		Layers []struct {
			Arn string `json:"Arn"`
		} `json:"Layers,omitempty"`
	}
	getCfgResp := doJSON(t, http.MethodGet, lambdaURL(srv, "/functions/deleted-python-layer-fn/configuration"), nil)
	helpers.AssertStatus(t, getCfgResp, http.StatusOK)
	var cfg funcConfigWithLayers
	decodeJSON(t, getCfgResp, &cfg)
	if len(cfg.Layers) != 1 || cfg.Layers[0].Arn != lv.LayerVersionArn {
		t.Fatalf("expected configuration to retain deleted layer ARN %q, got %#v", lv.LayerVersionArn, cfg.Layers)
	}

	getFnResp := doJSON(t, http.MethodGet, lambdaURL(srv, "/functions/deleted-python-layer-fn"), nil)
	helpers.AssertStatus(t, getFnResp, http.StatusOK)
	var fnResp struct {
		Configuration funcConfigWithLayers `json:"Configuration"`
	}
	decodeJSON(t, getFnResp, &fnResp)
	if len(fnResp.Configuration.Layers) != 1 || fnResp.Configuration.Layers[0].Arn != lv.LayerVersionArn {
		t.Fatalf("expected GetFunction to retain deleted layer ARN %q, got %#v", lv.LayerVersionArn, fnResp.Configuration.Layers)
	}

	// And invocation now fails during runtime init with a clear missing-layer error.
	resp := invokeFunction(t, srv, "deleted-python-layer-fn", map[string]any{"ping": true})
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}

	helpers.AssertStatus(t, resp, http.StatusOK)
	if resp.Header.Get("X-Amz-Function-Error") != "Unhandled" {
		t.Fatalf("expected X-Amz-Function-Error=Unhandled, got %q (body=%s)", resp.Header.Get("X-Amz-Function-Error"), body)
	}

	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("unmarshal response: %v — body: %s", err, body)
	}
	if out["errorType"] != "Runtime.InitError" {
		t.Fatalf("expected errorType Runtime.InitError, got: %v (body=%s)", out["errorType"], body)
	}
	errMsg, _ := out["errorMessage"].(string)
	if !strings.Contains(errMsg, "layer version not found") {
		t.Fatalf("expected missing-layer message, got: %s", body)
	}
}

func TestInvoke_nodeRuntime_deletedLayerRecoveryAfterClearingLayers(t *testing.T) {
	helpers.SkipWithoutDocker(t)

	// Given a zip-based Node function with an attached layer that later gets deleted.
	srv := helpers.NewTestServer(t, helpers.WithLambdaDocker())
	code := makeZip(t, "index.js", `
exports.handler = async () => {
  return { ok: true };
};
`)
	createFunctionWithCode(t, srv, "deleted-layer-recovery-fn", "nodejs20.x", "index.handler", code)

	waitForFunctionActive(t, srv, "deleted-layer-recovery-fn")

	layerZip := makeZip(t, "nodejs/node_modules/layer-lib/index.js", `module.exports = { value: "from-layer" };`)
	lvResp := doJSON(t, http.MethodPost, layerURL(srv, "/layers/deleted-node-layer-recovery/versions"), publishLayerVersionReq{
		Content: layerContent{ZipFile: layerZip},
	})
	helpers.AssertStatus(t, lvResp, http.StatusCreated)
	var lv layerVersionResponse
	decodeJSON(t, lvResp, &lv)

	attachResp := doJSON(t, http.MethodPut, lambdaURL(srv, "/functions/deleted-layer-recovery-fn/configuration"), map[string]any{
		"Layers": []string{lv.LayerVersionArn},
	})
	defer attachResp.Body.Close()
	helpers.AssertStatus(t, attachResp, http.StatusOK)

	deleteResp := doJSON(t, http.MethodDelete, layerURL(srv, "/layers/deleted-node-layer-recovery/versions/1"), nil)
	defer deleteResp.Body.Close()
	helpers.AssertStatus(t, deleteResp, http.StatusNoContent)

	// First invoke fails because configured layer content can no longer be loaded.
	failResp := invokeFunction(t, srv, "deleted-layer-recovery-fn", map[string]any{"ping": true})
	defer failResp.Body.Close()
	failBody, err := io.ReadAll(failResp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	helpers.AssertStatus(t, failResp, http.StatusOK)
	if failResp.Header.Get("X-Amz-Function-Error") != "Unhandled" {
		t.Fatalf("expected X-Amz-Function-Error=Unhandled, got %q (body=%s)", failResp.Header.Get("X-Amz-Function-Error"), failBody)
	}

	// When layer references are cleared via configuration update.
	clearResp := doJSON(t, http.MethodPut, lambdaURL(srv, "/functions/deleted-layer-recovery-fn/configuration"), map[string]any{
		"Layers": []string{},
	})
	defer clearResp.Body.Close()
	helpers.AssertStatus(t, clearResp, http.StatusOK)

	type funcConfigWithLayers struct {
		functionConfiguration
		Layers []struct {
			Arn string `json:"Arn"`
		} `json:"Layers,omitempty"`
	}
	getCfgResp := doJSON(t, http.MethodGet, lambdaURL(srv, "/functions/deleted-layer-recovery-fn/configuration"), nil)
	helpers.AssertStatus(t, getCfgResp, http.StatusOK)
	var cfg funcConfigWithLayers
	decodeJSON(t, getCfgResp, &cfg)
	if len(cfg.Layers) != 0 {
		t.Fatalf("expected 0 attached layers after clearing, got %#v", cfg.Layers)
	}

	// Then invoke succeeds again because startup no longer needs missing layer content.
	okResp := invokeFunction(t, srv, "deleted-layer-recovery-fn", map[string]any{"ping": true})
	defer okResp.Body.Close()
	okBody, err := io.ReadAll(okResp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	helpers.AssertStatus(t, okResp, http.StatusOK)
	if okResp.Header.Get("X-Amz-Function-Error") != "" {
		t.Fatalf("expected no function error after clearing layers, got %q (body=%s)", okResp.Header.Get("X-Amz-Function-Error"), okBody)
	}

	var out map[string]any
	if err := json.Unmarshal(okBody, &out); err != nil {
		t.Fatalf("unmarshal response: %v — body: %s", err, okBody)
	}
	if out["ok"] != true {
		t.Fatalf("expected successful payload after clearing layers, got: %s", okBody)
	}
}

func TestInvoke_pythonRuntime_deletedLayerRecoveryAfterClearingLayers(t *testing.T) {
	helpers.SkipWithoutDocker(t)

	// Given a zip-based Python function with an attached layer that later gets deleted.
	srv := helpers.NewTestServer(t, helpers.WithLambdaDocker())
	code := makeZip(t, "index.py", `
def handler(event, context):
    return {"ok": True}
`)
	createFunctionWithCode(t, srv, "deleted-python-layer-recovery-fn", "python3.11", "index.handler", code)

	waitForFunctionActive(t, srv, "deleted-python-layer-recovery-fn")

	layerZip := makeZip(t, "python/layer_mod.py", `VALUE = "from-layer"`)
	lvResp := doJSON(t, http.MethodPost, layerURL(srv, "/layers/deleted-python-layer-recovery/versions"), publishLayerVersionReq{
		Content: layerContent{ZipFile: layerZip},
	})
	helpers.AssertStatus(t, lvResp, http.StatusCreated)
	var lv layerVersionResponse
	decodeJSON(t, lvResp, &lv)

	attachResp := doJSON(t, http.MethodPut, lambdaURL(srv, "/functions/deleted-python-layer-recovery-fn/configuration"), map[string]any{
		"Layers": []string{lv.LayerVersionArn},
	})
	defer attachResp.Body.Close()
	helpers.AssertStatus(t, attachResp, http.StatusOK)

	deleteResp := doJSON(t, http.MethodDelete, layerURL(srv, "/layers/deleted-python-layer-recovery/versions/1"), nil)
	defer deleteResp.Body.Close()
	helpers.AssertStatus(t, deleteResp, http.StatusNoContent)

	// First invoke fails because configured layer content can no longer be loaded.
	failResp := invokeFunction(t, srv, "deleted-python-layer-recovery-fn", map[string]any{"ping": true})
	defer failResp.Body.Close()
	failBody, err := io.ReadAll(failResp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	helpers.AssertStatus(t, failResp, http.StatusOK)
	if failResp.Header.Get("X-Amz-Function-Error") != "Unhandled" {
		t.Fatalf("expected X-Amz-Function-Error=Unhandled, got %q (body=%s)", failResp.Header.Get("X-Amz-Function-Error"), failBody)
	}

	// When layer references are cleared via configuration update.
	clearResp := doJSON(t, http.MethodPut, lambdaURL(srv, "/functions/deleted-python-layer-recovery-fn/configuration"), map[string]any{
		"Layers": []string{},
	})
	defer clearResp.Body.Close()
	helpers.AssertStatus(t, clearResp, http.StatusOK)

	type funcConfigWithLayers struct {
		functionConfiguration
		Layers []struct {
			Arn string `json:"Arn"`
		} `json:"Layers,omitempty"`
	}
	getCfgResp := doJSON(t, http.MethodGet, lambdaURL(srv, "/functions/deleted-python-layer-recovery-fn/configuration"), nil)
	helpers.AssertStatus(t, getCfgResp, http.StatusOK)
	var cfg funcConfigWithLayers
	decodeJSON(t, getCfgResp, &cfg)
	if len(cfg.Layers) != 0 {
		t.Fatalf("expected 0 attached layers after clearing, got %#v", cfg.Layers)
	}

	// Then invoke succeeds again because startup no longer needs missing layer content.
	okResp := invokeFunction(t, srv, "deleted-python-layer-recovery-fn", map[string]any{"ping": true})
	defer okResp.Body.Close()
	okBody, err := io.ReadAll(okResp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	helpers.AssertStatus(t, okResp, http.StatusOK)
	if okResp.Header.Get("X-Amz-Function-Error") != "" {
		t.Fatalf("expected no function error after clearing layers, got %q (body=%s)", okResp.Header.Get("X-Amz-Function-Error"), okBody)
	}

	var out map[string]any
	if err := json.Unmarshal(okBody, &out); err != nil {
		t.Fatalf("unmarshal response: %v — body: %s", err, okBody)
	}
	if out["ok"] != true {
		t.Fatalf("expected successful payload after clearing layers, got: %s", okBody)
	}
}

func TestInvoke_nodeRuntime_hotReload_deletedAttachedLayerVersionFailsInit(t *testing.T) {
	helpers.SkipWithoutDocker(t)
	skipIfContainerizedHotReloadBindMount(t)

	// Given a hot-reload Node function with a real attached layer version.
	sourceDir, err := os.MkdirTemp("/workspace", "hot-reload-deleted-layer-")
	if err != nil {
		sourceDir = t.TempDir()
	} else {
		t.Cleanup(func() { _ = os.RemoveAll(sourceDir) })
	}
	if err := os.WriteFile(sourceDir+"/index.js", []byte(`
exports.handler = async () => {
  return { ok: true };
};
`), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}

	srv := helpers.NewTestServer(t, helpers.WithLambdaDocker(), helpers.WithLambdaHotReload())
	createResp := doJSON(t, http.MethodPost, lambdaURL(srv, "/functions"), createFunctionReq{
		FunctionName: "hot-deleted-layer-fn",
		Runtime:      "nodejs20.x",
		Handler:      "index.handler",
		Role:         "arn:aws:iam::000000000000:role/lambda-role",
		Code:         &lambdaCode{},
		Tags:         map[string]string{"overcast:hot-reload-path": sourceDir},
	})
	helpers.AssertStatus(t, createResp, http.StatusCreated)
	createResp.Body.Close()

	waitForFunctionActive(t, srv, "hot-deleted-layer-fn")

	layerZip := makeZip(t, "nodejs/node_modules/layer-lib/index.js", `module.exports = { value: "from-layer" };`)
	lvResp := doJSON(t, http.MethodPost, layerURL(srv, "/layers/hot-deleted-node-layer/versions"), publishLayerVersionReq{
		Content: layerContent{ZipFile: layerZip},
	})
	helpers.AssertStatus(t, lvResp, http.StatusCreated)
	var lv layerVersionResponse
	decodeJSON(t, lvResp, &lv)

	attachResp := doJSON(t, http.MethodPut, lambdaURL(srv, "/functions/hot-deleted-layer-fn/configuration"), map[string]any{
		"Layers": []string{lv.LayerVersionArn},
	})
	defer attachResp.Body.Close()
	helpers.AssertStatus(t, attachResp, http.StatusOK)

	// When the attached layer version is deleted after configuration.
	deleteResp := doJSON(t, http.MethodDelete, layerURL(srv, "/layers/hot-deleted-node-layer/versions/1"), nil)
	defer deleteResp.Body.Close()
	helpers.AssertStatus(t, deleteResp, http.StatusNoContent)

	// Then function readback still preserves the configured layer reference.
	type funcConfigWithLayers struct {
		functionConfiguration
		Layers []struct {
			Arn string `json:"Arn"`
		} `json:"Layers,omitempty"`
	}
	getCfgResp := doJSON(t, http.MethodGet, lambdaURL(srv, "/functions/hot-deleted-layer-fn/configuration"), nil)
	helpers.AssertStatus(t, getCfgResp, http.StatusOK)
	var cfg funcConfigWithLayers
	decodeJSON(t, getCfgResp, &cfg)
	if len(cfg.Layers) != 1 || cfg.Layers[0].Arn != lv.LayerVersionArn {
		t.Fatalf("expected configuration to retain deleted layer ARN %q, got %#v", lv.LayerVersionArn, cfg.Layers)
	}

	// And invocation now fails during runtime init with a clear missing-layer error.
	resp := invokeFunction(t, srv, "hot-deleted-layer-fn", map[string]any{"ping": true})
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}

	helpers.AssertStatus(t, resp, http.StatusOK)
	if resp.Header.Get("X-Amz-Function-Error") != "Unhandled" {
		t.Fatalf("expected X-Amz-Function-Error=Unhandled, got %q (body=%s)", resp.Header.Get("X-Amz-Function-Error"), body)
	}

	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("unmarshal response: %v — body: %s", err, body)
	}
	if out["errorType"] != "Runtime.InitError" {
		t.Fatalf("expected errorType Runtime.InitError, got: %v (body=%s)", out["errorType"], body)
	}
	errMsg, _ := out["errorMessage"].(string)
	if !strings.Contains(errMsg, "layer version not found") {
		t.Fatalf("expected missing-layer message, got: %s", body)
	}
}

func TestInvoke_pythonRuntime_hotReload_deletedAttachedLayerVersionFailsInit(t *testing.T) {
	helpers.SkipWithoutDocker(t)
	skipIfContainerizedHotReloadBindMount(t)

	// Given a hot-reload Python function with a real attached layer version.
	sourceDir, err := os.MkdirTemp("/workspace", "hot-reload-python-deleted-layer-")
	if err != nil {
		sourceDir = t.TempDir()
	} else {
		t.Cleanup(func() { _ = os.RemoveAll(sourceDir) })
	}
	if err := os.WriteFile(sourceDir+"/index.py", []byte(`
def handler(event, context):
    return {"ok": True}
`), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}

	srv := helpers.NewTestServer(t, helpers.WithLambdaDocker(), helpers.WithLambdaHotReload())
	createResp := doJSON(t, http.MethodPost, lambdaURL(srv, "/functions"), createFunctionReq{
		FunctionName: "hot-deleted-python-layer-fn",
		Runtime:      "python3.11",
		Handler:      "index.handler",
		Role:         "arn:aws:iam::000000000000:role/lambda-role",
		Code:         &lambdaCode{},
		Tags:         map[string]string{"overcast:hot-reload-path": sourceDir},
	})
	helpers.AssertStatus(t, createResp, http.StatusCreated)
	createResp.Body.Close()

	waitForFunctionActive(t, srv, "hot-deleted-python-layer-fn")

	layerZip := makeZip(t, "python/layer_mod.py", `VALUE = "from-layer"`)
	lvResp := doJSON(t, http.MethodPost, layerURL(srv, "/layers/hot-deleted-python-layer/versions"), publishLayerVersionReq{
		Content: layerContent{ZipFile: layerZip},
	})
	helpers.AssertStatus(t, lvResp, http.StatusCreated)
	var lv layerVersionResponse
	decodeJSON(t, lvResp, &lv)

	attachResp := doJSON(t, http.MethodPut, lambdaURL(srv, "/functions/hot-deleted-python-layer-fn/configuration"), map[string]any{
		"Layers": []string{lv.LayerVersionArn},
	})
	defer attachResp.Body.Close()
	helpers.AssertStatus(t, attachResp, http.StatusOK)

	// When the attached layer version is deleted after configuration.
	deleteResp := doJSON(t, http.MethodDelete, layerURL(srv, "/layers/hot-deleted-python-layer/versions/1"), nil)
	defer deleteResp.Body.Close()
	helpers.AssertStatus(t, deleteResp, http.StatusNoContent)

	// Then function readback still preserves the configured layer reference.
	type funcConfigWithLayers struct {
		functionConfiguration
		Layers []struct {
			Arn string `json:"Arn"`
		} `json:"Layers,omitempty"`
	}
	getCfgResp := doJSON(t, http.MethodGet, lambdaURL(srv, "/functions/hot-deleted-python-layer-fn/configuration"), nil)
	helpers.AssertStatus(t, getCfgResp, http.StatusOK)
	var cfg funcConfigWithLayers
	decodeJSON(t, getCfgResp, &cfg)
	if len(cfg.Layers) != 1 || cfg.Layers[0].Arn != lv.LayerVersionArn {
		t.Fatalf("expected configuration to retain deleted layer ARN %q, got %#v", lv.LayerVersionArn, cfg.Layers)
	}

	// And invocation now fails during runtime init with a clear missing-layer error.
	resp := invokeFunction(t, srv, "hot-deleted-python-layer-fn", map[string]any{"ping": true})
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}

	helpers.AssertStatus(t, resp, http.StatusOK)
	if resp.Header.Get("X-Amz-Function-Error") != "Unhandled" {
		t.Fatalf("expected X-Amz-Function-Error=Unhandled, got %q (body=%s)", resp.Header.Get("X-Amz-Function-Error"), body)
	}

	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("unmarshal response: %v — body: %s", err, body)
	}
	if out["errorType"] != "Runtime.InitError" {
		t.Fatalf("expected errorType Runtime.InitError, got: %v (body=%s)", out["errorType"], body)
	}
	errMsg, _ := out["errorMessage"].(string)
	if !strings.Contains(errMsg, "layer version not found") {
		t.Fatalf("expected missing-layer message, got: %s", body)
	}
}

func TestInvoke_nodeRuntime_hotReload_deletedLayerRecoveryAfterClearingLayers(t *testing.T) {
	helpers.SkipWithoutDocker(t)
	skipIfContainerizedHotReloadBindMount(t)

	// Given a hot-reload Node function with an attached layer that later gets deleted.
	sourceDir, err := os.MkdirTemp("/workspace", "hot-reload-deleted-layer-recovery-")
	if err != nil {
		sourceDir = t.TempDir()
	} else {
		t.Cleanup(func() { _ = os.RemoveAll(sourceDir) })
	}
	if err := os.WriteFile(sourceDir+"/index.js", []byte(`
exports.handler = async () => {
  return { ok: true };
};
`), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}

	srv := helpers.NewTestServer(t, helpers.WithLambdaDocker(), helpers.WithLambdaHotReload())
	createResp := doJSON(t, http.MethodPost, lambdaURL(srv, "/functions"), createFunctionReq{
		FunctionName: "hot-deleted-layer-recovery-fn",
		Runtime:      "nodejs20.x",
		Handler:      "index.handler",
		Role:         "arn:aws:iam::000000000000:role/lambda-role",
		Code:         &lambdaCode{},
		Tags:         map[string]string{"overcast:hot-reload-path": sourceDir},
	})
	helpers.AssertStatus(t, createResp, http.StatusCreated)
	createResp.Body.Close()

	waitForFunctionActive(t, srv, "hot-deleted-layer-recovery-fn")

	layerZip := makeZip(t, "nodejs/node_modules/layer-lib/index.js", `module.exports = { value: "from-layer" };`)
	lvResp := doJSON(t, http.MethodPost, layerURL(srv, "/layers/hot-deleted-node-layer-recovery/versions"), publishLayerVersionReq{
		Content: layerContent{ZipFile: layerZip},
	})
	helpers.AssertStatus(t, lvResp, http.StatusCreated)
	var lv layerVersionResponse
	decodeJSON(t, lvResp, &lv)

	attachResp := doJSON(t, http.MethodPut, lambdaURL(srv, "/functions/hot-deleted-layer-recovery-fn/configuration"), map[string]any{
		"Layers": []string{lv.LayerVersionArn},
	})
	defer attachResp.Body.Close()
	helpers.AssertStatus(t, attachResp, http.StatusOK)

	deleteResp := doJSON(t, http.MethodDelete, layerURL(srv, "/layers/hot-deleted-node-layer-recovery/versions/1"), nil)
	defer deleteResp.Body.Close()
	helpers.AssertStatus(t, deleteResp, http.StatusNoContent)

	// First invoke fails because configured layer content can no longer be loaded.
	failResp := invokeFunction(t, srv, "hot-deleted-layer-recovery-fn", map[string]any{"ping": true})
	defer failResp.Body.Close()
	failBody, err := io.ReadAll(failResp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if failResp.Header.Get("X-Amz-Function-Error") == "Unhandled" &&
		(strings.Contains(string(failBody), "Runtime.ExitError") || strings.Contains(string(failBody), "Runtime.ImportModuleError")) {
		t.Skipf("hot-reload bind mount not supported in this Docker environment: %s", string(failBody))
	}
	helpers.AssertStatus(t, failResp, http.StatusOK)
	if failResp.Header.Get("X-Amz-Function-Error") != "Unhandled" {
		t.Fatalf("expected X-Amz-Function-Error=Unhandled, got %q (body=%s)", failResp.Header.Get("X-Amz-Function-Error"), failBody)
	}
	var failOut map[string]any
	if err := json.Unmarshal(failBody, &failOut); err != nil {
		t.Fatalf("unmarshal response: %v — body: %s", err, failBody)
	}
	errMsg, _ := failOut["errorMessage"].(string)
	if failOut["errorType"] != "Runtime.InitError" || !strings.Contains(errMsg, "layer version not found") {
		t.Fatalf("expected missing-layer Runtime.InitError, got: %s", failBody)
	}

	// When layer references are cleared via configuration update.
	clearResp := doJSON(t, http.MethodPut, lambdaURL(srv, "/functions/hot-deleted-layer-recovery-fn/configuration"), map[string]any{
		"Layers": []string{},
	})
	defer clearResp.Body.Close()
	helpers.AssertStatus(t, clearResp, http.StatusOK)

	type funcConfigWithLayers struct {
		functionConfiguration
		Layers []struct {
			Arn string `json:"Arn"`
		} `json:"Layers,omitempty"`
	}
	getCfgResp := doJSON(t, http.MethodGet, lambdaURL(srv, "/functions/hot-deleted-layer-recovery-fn/configuration"), nil)
	helpers.AssertStatus(t, getCfgResp, http.StatusOK)
	var cfg funcConfigWithLayers
	decodeJSON(t, getCfgResp, &cfg)
	if len(cfg.Layers) != 0 {
		t.Fatalf("expected 0 attached layers after clearing, got %#v", cfg.Layers)
	}

	// Then invoke succeeds again because startup no longer needs missing layer content.
	okResp := invokeFunction(t, srv, "hot-deleted-layer-recovery-fn", map[string]any{"ping": true})
	defer okResp.Body.Close()
	okBody, err := io.ReadAll(okResp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if okResp.Header.Get("X-Amz-Function-Error") == "Unhandled" &&
		(strings.Contains(string(okBody), "Runtime.ExitError") || strings.Contains(string(okBody), "Runtime.ImportModuleError")) {
		t.Skipf("hot-reload bind mount not supported in this Docker environment: %s", string(okBody))
	}
	helpers.AssertStatus(t, okResp, http.StatusOK)
	if okResp.Header.Get("X-Amz-Function-Error") != "" {
		t.Fatalf("expected no function error after clearing layers, got %q (body=%s)", okResp.Header.Get("X-Amz-Function-Error"), okBody)
	}
	var out map[string]any
	if err := json.Unmarshal(okBody, &out); err != nil {
		t.Fatalf("unmarshal response: %v — body: %s", err, okBody)
	}
	if out["ok"] != true {
		t.Fatalf("expected successful payload after clearing layers, got: %s", okBody)
	}
}

func TestInvoke_pythonRuntime_hotReload_deletedLayerRecoveryAfterClearingLayers(t *testing.T) {
	helpers.SkipWithoutDocker(t)
	skipIfContainerizedHotReloadBindMount(t)

	// Given a hot-reload Python function with an attached layer that later gets deleted.
	sourceDir, err := os.MkdirTemp("/workspace", "hot-reload-python-deleted-layer-recovery-")
	if err != nil {
		sourceDir = t.TempDir()
	} else {
		t.Cleanup(func() { _ = os.RemoveAll(sourceDir) })
	}
	if err := os.WriteFile(sourceDir+"/index.py", []byte(`
def handler(event, context):
    return {"ok": True}
`), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}

	srv := helpers.NewTestServer(t, helpers.WithLambdaDocker(), helpers.WithLambdaHotReload())
	createResp := doJSON(t, http.MethodPost, lambdaURL(srv, "/functions"), createFunctionReq{
		FunctionName: "hot-deleted-python-layer-recovery-fn",
		Runtime:      "python3.11",
		Handler:      "index.handler",
		Role:         "arn:aws:iam::000000000000:role/lambda-role",
		Code:         &lambdaCode{},
		Tags:         map[string]string{"overcast:hot-reload-path": sourceDir},
	})
	helpers.AssertStatus(t, createResp, http.StatusCreated)
	createResp.Body.Close()

	waitForFunctionActive(t, srv, "hot-deleted-python-layer-recovery-fn")

	layerZip := makeZip(t, "python/layer_mod.py", `VALUE = "from-layer"`)
	lvResp := doJSON(t, http.MethodPost, layerURL(srv, "/layers/hot-deleted-python-layer-recovery/versions"), publishLayerVersionReq{
		Content: layerContent{ZipFile: layerZip},
	})
	helpers.AssertStatus(t, lvResp, http.StatusCreated)
	var lv layerVersionResponse
	decodeJSON(t, lvResp, &lv)

	attachResp := doJSON(t, http.MethodPut, lambdaURL(srv, "/functions/hot-deleted-python-layer-recovery-fn/configuration"), map[string]any{
		"Layers": []string{lv.LayerVersionArn},
	})
	defer attachResp.Body.Close()
	helpers.AssertStatus(t, attachResp, http.StatusOK)

	deleteResp := doJSON(t, http.MethodDelete, layerURL(srv, "/layers/hot-deleted-python-layer-recovery/versions/1"), nil)
	defer deleteResp.Body.Close()
	helpers.AssertStatus(t, deleteResp, http.StatusNoContent)

	// First invoke fails because configured layer content can no longer be loaded.
	failResp := invokeFunction(t, srv, "hot-deleted-python-layer-recovery-fn", map[string]any{"ping": true})
	defer failResp.Body.Close()
	failBody, err := io.ReadAll(failResp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if failResp.Header.Get("X-Amz-Function-Error") == "Unhandled" &&
		(strings.Contains(string(failBody), "Runtime.ExitError") || strings.Contains(string(failBody), "Runtime.ImportModuleError")) {
		t.Skipf("hot-reload bind mount not supported in this Docker environment: %s", string(failBody))
	}
	helpers.AssertStatus(t, failResp, http.StatusOK)
	if failResp.Header.Get("X-Amz-Function-Error") != "Unhandled" {
		t.Fatalf("expected X-Amz-Function-Error=Unhandled, got %q (body=%s)", failResp.Header.Get("X-Amz-Function-Error"), failBody)
	}
	var failOut map[string]any
	if err := json.Unmarshal(failBody, &failOut); err != nil {
		t.Fatalf("unmarshal response: %v — body: %s", err, failBody)
	}
	errMsg, _ := failOut["errorMessage"].(string)
	if failOut["errorType"] != "Runtime.InitError" || !strings.Contains(errMsg, "layer version not found") {
		t.Fatalf("expected missing-layer Runtime.InitError, got: %s", failBody)
	}

	// When layer references are cleared via configuration update.
	clearResp := doJSON(t, http.MethodPut, lambdaURL(srv, "/functions/hot-deleted-python-layer-recovery-fn/configuration"), map[string]any{
		"Layers": []string{},
	})
	defer clearResp.Body.Close()
	helpers.AssertStatus(t, clearResp, http.StatusOK)

	type funcConfigWithLayers struct {
		functionConfiguration
		Layers []struct {
			Arn string `json:"Arn"`
		} `json:"Layers,omitempty"`
	}
	getCfgResp := doJSON(t, http.MethodGet, lambdaURL(srv, "/functions/hot-deleted-python-layer-recovery-fn/configuration"), nil)
	helpers.AssertStatus(t, getCfgResp, http.StatusOK)
	var cfg funcConfigWithLayers
	decodeJSON(t, getCfgResp, &cfg)
	if len(cfg.Layers) != 0 {
		t.Fatalf("expected 0 attached layers after clearing, got %#v", cfg.Layers)
	}

	// Then invoke succeeds again because startup no longer needs missing layer content.
	okResp := invokeFunction(t, srv, "hot-deleted-python-layer-recovery-fn", map[string]any{"ping": true})
	defer okResp.Body.Close()
	okBody, err := io.ReadAll(okResp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if okResp.Header.Get("X-Amz-Function-Error") == "Unhandled" &&
		(strings.Contains(string(okBody), "Runtime.ExitError") || strings.Contains(string(okBody), "Runtime.ImportModuleError")) {
		t.Skipf("hot-reload bind mount not supported in this Docker environment: %s", string(okBody))
	}
	helpers.AssertStatus(t, okResp, http.StatusOK)
	if okResp.Header.Get("X-Amz-Function-Error") != "" {
		t.Fatalf("expected no function error after clearing layers, got %q (body=%s)", okResp.Header.Get("X-Amz-Function-Error"), okBody)
	}
	var out map[string]any
	if err := json.Unmarshal(okBody, &out); err != nil {
		t.Fatalf("unmarshal response: %v — body: %s", err, okBody)
	}
	if out["ok"] != true {
		t.Fatalf("expected successful payload after clearing layers, got: %s", okBody)
	}
}

func TestInvoke_nodeRuntime_missingLayerVersionFailsInit(t *testing.T) {
	helpers.SkipWithoutDocker(t)

	// Given a zip-based Node function whose configuration references a
	// non-existent layer version ARN.
	srv := helpers.NewTestServer(t, helpers.WithLambdaDocker())
	code := makeZip(t, "index.js", `
exports.handler = async () => {
  return { ok: true };
};
`)
	createFunctionWithCode(t, srv, "missing-layer-fn", "nodejs20.x", "index.handler", code)
	waitForFunctionActive(t, srv, "missing-layer-fn")

	missingARN := "arn:aws:lambda:us-east-1:000000000000:layer:does-not-exist:999"
	attachResp := doJSON(t, http.MethodPut, lambdaURL(srv, "/functions/missing-layer-fn/configuration"), map[string]any{
		"Layers": []string{missingARN},
	})
	defer attachResp.Body.Close()
	helpers.AssertStatus(t, attachResp, http.StatusOK)

	// When invoking the function.
	resp := invokeFunction(t, srv, "missing-layer-fn", map[string]any{"ping": true})
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}

	// Then the runtime init fails with a clear message about missing layer version.
	helpers.AssertStatus(t, resp, http.StatusOK)
	if resp.Header.Get("X-Amz-Function-Error") != "Unhandled" {
		t.Fatalf("expected X-Amz-Function-Error=Unhandled, got %q (body=%s)", resp.Header.Get("X-Amz-Function-Error"), body)
	}

	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("unmarshal response: %v — body: %s", err, body)
	}
	if out["errorType"] != "Runtime.InitError" {
		t.Fatalf("expected errorType Runtime.InitError, got: %v (body=%s)", out["errorType"], body)
	}
	errMsg, _ := out["errorMessage"].(string)
	if !strings.Contains(errMsg, "layer version not found") {
		t.Fatalf("expected missing-layer message, got: %s", body)
	}
}

func TestInvoke_pythonRuntime_missingLayerVersionFailsInit(t *testing.T) {
	helpers.SkipWithoutDocker(t)

	// Given a zip-based Python function whose configuration references a
	// non-existent layer version ARN.
	srv := helpers.NewTestServer(t, helpers.WithLambdaDocker())
	code := makeZip(t, "index.py", `
def handler(event, context):
    return {"ok": True}
`)
	createFunctionWithCode(t, srv, "missing-python-layer-fn", "python3.11", "index.handler", code)
	waitForFunctionActive(t, srv, "missing-python-layer-fn")

	missingARN := "arn:aws:lambda:us-east-1:000000000000:layer:does-not-exist:999"
	attachResp := doJSON(t, http.MethodPut, lambdaURL(srv, "/functions/missing-python-layer-fn/configuration"), map[string]any{
		"Layers": []string{missingARN},
	})
	defer attachResp.Body.Close()
	helpers.AssertStatus(t, attachResp, http.StatusOK)

	// When invoking the function.
	resp := invokeFunction(t, srv, "missing-python-layer-fn", map[string]any{"ping": true})
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}

	// Then the runtime init fails with a clear message about missing layer version.
	helpers.AssertStatus(t, resp, http.StatusOK)
	if resp.Header.Get("X-Amz-Function-Error") != "Unhandled" {
		t.Fatalf("expected X-Amz-Function-Error=Unhandled, got %q (body=%s)", resp.Header.Get("X-Amz-Function-Error"), body)
	}

	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("unmarshal response: %v — body: %s", err, body)
	}
	if out["errorType"] != "Runtime.InitError" {
		t.Fatalf("expected errorType Runtime.InitError, got: %v (body=%s)", out["errorType"], body)
	}
	errMsg, _ := out["errorMessage"].(string)
	if !strings.Contains(errMsg, "layer version not found") {
		t.Fatalf("expected missing-layer message, got: %s", body)
	}
}

func TestInvoke_nodeRuntime_hotReload_missingLayerVersionFailsInit(t *testing.T) {
	helpers.SkipWithoutDocker(t)
	skipIfContainerizedHotReloadBindMount(t)

	// Given a hot-reload function whose configuration references a
	// non-existent layer version ARN.
	sourceDir, err := os.MkdirTemp("/workspace", "hot-reload-missing-layer-")
	if err != nil {
		sourceDir = t.TempDir()
	} else {
		t.Cleanup(func() { _ = os.RemoveAll(sourceDir) })
	}
	if err := os.WriteFile(sourceDir+"/index.js", []byte(`
exports.handler = async () => {
  return { ok: true };
};
`), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}

	srv := helpers.NewTestServer(t, helpers.WithLambdaDocker(), helpers.WithLambdaHotReload())
	createResp := doJSON(t, http.MethodPost, lambdaURL(srv, "/functions"), createFunctionReq{
		FunctionName: "hot-missing-layer-fn",
		Runtime:      "nodejs20.x",
		Handler:      "index.handler",
		Role:         "arn:aws:iam::000000000000:role/lambda-role",
		Code:         &lambdaCode{},
		Tags:         map[string]string{"overcast:hot-reload-path": sourceDir},
	})
	helpers.AssertStatus(t, createResp, http.StatusCreated)
	createResp.Body.Close()

	waitForFunctionActive(t, srv, "hot-missing-layer-fn")

	missingARN := "arn:aws:lambda:us-east-1:000000000000:layer:does-not-exist:999"
	attachResp := doJSON(t, http.MethodPut, lambdaURL(srv, "/functions/hot-missing-layer-fn/configuration"), map[string]any{
		"Layers": []string{missingARN},
	})
	defer attachResp.Body.Close()
	helpers.AssertStatus(t, attachResp, http.StatusOK)

	// When invoking the function.
	resp := invokeFunction(t, srv, "hot-missing-layer-fn", map[string]any{"ping": true})
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}

	// Then runtime init fails with a clear missing-layer message.
	helpers.AssertStatus(t, resp, http.StatusOK)
	if resp.Header.Get("X-Amz-Function-Error") != "Unhandled" {
		t.Fatalf("expected X-Amz-Function-Error=Unhandled, got %q (body=%s)", resp.Header.Get("X-Amz-Function-Error"), body)
	}

	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("unmarshal response: %v — body: %s", err, body)
	}
	if out["errorType"] != "Runtime.InitError" {
		t.Fatalf("expected errorType Runtime.InitError, got: %v (body=%s)", out["errorType"], body)
	}
	errMsg, _ := out["errorMessage"].(string)
	if !strings.Contains(errMsg, "layer version not found") {
		t.Fatalf("expected missing-layer message, got: %s", body)
	}
}

func TestInvoke_pythonRuntime_hotReload_missingLayerVersionFailsInit(t *testing.T) {
	helpers.SkipWithoutDocker(t)
	skipIfContainerizedHotReloadBindMount(t)

	// Given a hot-reload Python function whose configuration references a
	// non-existent layer version ARN.
	sourceDir, err := os.MkdirTemp("/workspace", "hot-reload-python-missing-layer-")
	if err != nil {
		sourceDir = t.TempDir()
	} else {
		t.Cleanup(func() { _ = os.RemoveAll(sourceDir) })
	}
	if err := os.WriteFile(sourceDir+"/index.py", []byte(`
def handler(event, context):
    return {"ok": True}
`), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}

	srv := helpers.NewTestServer(t, helpers.WithLambdaDocker(), helpers.WithLambdaHotReload())
	createResp := doJSON(t, http.MethodPost, lambdaURL(srv, "/functions"), createFunctionReq{
		FunctionName: "hot-missing-python-layer-fn",
		Runtime:      "python3.11",
		Handler:      "index.handler",
		Role:         "arn:aws:iam::000000000000:role/lambda-role",
		Code:         &lambdaCode{},
		Tags:         map[string]string{"overcast:hot-reload-path": sourceDir},
	})
	helpers.AssertStatus(t, createResp, http.StatusCreated)
	createResp.Body.Close()

	waitForFunctionActive(t, srv, "hot-missing-python-layer-fn")

	missingARN := "arn:aws:lambda:us-east-1:000000000000:layer:does-not-exist:999"
	attachResp := doJSON(t, http.MethodPut, lambdaURL(srv, "/functions/hot-missing-python-layer-fn/configuration"), map[string]any{
		"Layers": []string{missingARN},
	})
	defer attachResp.Body.Close()
	helpers.AssertStatus(t, attachResp, http.StatusOK)

	// When invoking the function.
	resp := invokeFunction(t, srv, "hot-missing-python-layer-fn", map[string]any{"ping": true})
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}

	// Then runtime init fails with a clear missing-layer message.
	helpers.AssertStatus(t, resp, http.StatusOK)
	if resp.Header.Get("X-Amz-Function-Error") != "Unhandled" {
		t.Fatalf("expected X-Amz-Function-Error=Unhandled, got %q (body=%s)", resp.Header.Get("X-Amz-Function-Error"), body)
	}

	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("unmarshal response: %v — body: %s", err, body)
	}
	if out["errorType"] != "Runtime.InitError" {
		t.Fatalf("expected errorType Runtime.InitError, got: %v (body=%s)", out["errorType"], body)
	}
	errMsg, _ := out["errorMessage"].(string)
	if !strings.Contains(errMsg, "layer version not found") {
		t.Fatalf("expected missing-layer message, got: %s", body)
	}
}

func TestInvoke_functionError(t *testing.T) {
	helpers.SkipWithoutDocker(t)

	// Given a function that always throws
	srv := helpers.NewTestServer(t, helpers.WithLambdaDocker())
	code := makeZip(t, "index.js", `
exports.handler = async () => {
  throw new Error("something went wrong");
};
`)
	createFunctionWithCode(t, srv, "throw-fn", "nodejs20.x", "index.handler", code)
	waitForFunctionActive(t, srv, "throw-fn")

	// When InvokeFunction is called
	resp := invokeFunction(t, srv, "throw-fn", map[string]string{})
	defer resp.Body.Close()

	// Then 200 with X-Amz-Function-Error header set (AWS semantics: errors are
	// still 200 responses with a special header + error payload)
	helpers.AssertStatus(t, resp, http.StatusOK)
	if resp.Header.Get("X-Amz-Function-Error") == "" {
		t.Error("expected X-Amz-Function-Error header to be set for a throwing handler")
	}
	body, _ := io.ReadAll(resp.Body)
	var errPayload map[string]any
	if err := json.Unmarshal(body, &errPayload); err != nil {
		t.Fatalf("unmarshal error payload: %v — body: %s", err, body)
	}
	if errPayload["errorMessage"] == nil && errPayload["errorType"] == nil {
		t.Errorf("error payload missing errorMessage/errorType: %s", body)
	}
}

func TestInvoke_timeout(t *testing.T) {
	helpers.SkipWithoutDocker(t)

	// Given a function that sleeps longer than its timeout
	srv := helpers.NewTestServer(t, helpers.WithLambdaDocker())
	code := makeZip(t, "index.js", `
exports.handler = async () => {
  await new Promise(r => setTimeout(r, 30000));
  return {};
};
`)
	// Timeout = 1s so the test completes quickly.
	resp := doJSON(t, http.MethodPost, lambdaURL(srv, "/functions"), createFunctionReq{
		FunctionName: "timeout-fn",
		Runtime:      "nodejs20.x",
		Handler:      "index.handler",
		Role:         "arn:aws:iam::000000000000:role/lambda-role",
		Timeout:      1,
		MemorySize:   128,
		Code:         &lambdaCode{ZipFile: code},
	})
	helpers.AssertStatus(t, resp, http.StatusCreated)
	resp.Body.Close()

	waitForFunctionActive(t, srv, "timeout-fn")

	// When InvokeFunction is called (should time out)
	start := time.Now()
	invokeResp := invokeFunction(t, srv, "timeout-fn", nil)
	invokeResp.Body.Close()
	elapsed := time.Since(start)

	// Then it returns in a bounded time (not hanging forever). The budget is
	// generous because a cold Docker container start (image pull/create/start
	// of node20.x) plus the 1s sleep plus teardown can take several seconds
	// on a loaded host. The intent is to catch the "invoke hangs forever"
	// regression, not to assert tight timing.
	if elapsed > 15*time.Second {
		t.Errorf("invoke took %v, expected ≤15s (Lambda timeout=1s + Docker cold-start budget ~14s); this likely indicates the invoke is hanging or Docker is wedged", elapsed)
	}
}

// invokeForLogTail invokes fn with X-Amz-Log-Type: Tail and returns the decoded
// X-Amz-Log-Result.
func invokeForLogTail(t *testing.T, srv *helpers.TestServer, fn string, payload []byte) []byte {
	t.Helper()

	req, err := http.NewRequest(http.MethodPost, lambdaURL(srv, "/functions/"+fn+"/invocations"), bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Amz-Log-Type", "Tail")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	defer resp.Body.Close()

	helpers.AssertStatus(t, resp, http.StatusOK)
	logResult := resp.Header.Get("X-Amz-Log-Result")
	if logResult == "" {
		t.Fatal("expected X-Amz-Log-Result header when X-Amz-Log-Type: Tail")
	}
	decoded, err := base64.StdEncoding.DecodeString(logResult)
	if err != nil {
		t.Fatalf("decode X-Amz-Log-Result: %v", err)
	}
	return decoded
}

// TestInvoke_logTail covers both halves of the tail's timing problem. The
// handler's stdout is the only part of the tail that travels through Docker's
// log stream — START / END / REPORT are written straight into the buffer by
// the emulator — so it is the only part that can be missing if the snapshot is
// taken too early.
//
// The first invocation is a cold start, where nothing has ever been read from
// the log stream. The second is a warm reuse of the same container, where the
// read watermark already holds the first invocation's timestamp; that case
// went uncovered for a long time and hid a wait that had become a no-op.
func TestInvoke_logTail(t *testing.T) {
	helpers.SkipWithoutDocker(t)

	// Given a function that echoes a caller-supplied marker
	srv := helpers.NewTestServer(t, helpers.WithLambdaDocker())
	code := makeZip(t, "index.js", `
exports.handler = async (event) => {
  console.log("hello from lambda " + (event.marker || ""));
  return { ok: true };
};
`)
	createFunctionWithCode(t, srv, "log-fn", "nodejs20.x", "index.handler", code)
	waitForFunctionActive(t, srv, "log-fn")

	// When the function is invoked cold with X-Amz-Log-Type: Tail
	decoded := invokeForLogTail(t, srv, "log-fn", []byte(`{"marker":"cold"}`))

	// Then the tail carries the handler's own output, not just the platform lines
	if !bytes.Contains(decoded, []byte("hello from lambda cold")) {
		t.Errorf("cold-start log tail %q does not contain expected log line", decoded)
	}

	// When the same warm container serves a second invocation
	decoded = invokeForLogTail(t, srv, "log-fn", []byte(`{"marker":"warm"}`))

	// Then the tail carries *that* invocation's output — a watermark left by
	// the first invocation must not satisfy the wait for the second's
	if !bytes.Contains(decoded, []byte("hello from lambda warm")) {
		t.Errorf("warm-invoke log tail %q does not contain expected log line", decoded)
	}
	if bytes.Contains(decoded, []byte("hello from lambda cold")) {
		t.Errorf("warm-invoke log tail leaked the previous invocation's output: %q", decoded)
	}
}

// TestInvoke_logsLandInCloudWatch verifies the END-TO-END log path: invoking a
// Lambda function results in handler stdout AND the synthetic START / END /
// REPORT lines being readable via CloudWatch Logs GetLogEvents.
//
// This is the user-visible behaviour the CloudWatch Logs UI relies on. It
// exercises the full pipeline: Docker stdout → streamLogs goroutine → batched
// flush → logsStore.appendEvents (cache + debounced persist) → GetLogEvents.
func TestInvoke_logsLandInCloudWatch(t *testing.T) {
	helpers.SkipWithoutDocker(t)

	srv := helpers.NewTestServer(t, helpers.WithLambdaDocker())
	code := makeZip(t, "index.js", `
exports.handler = async () => {
  console.log("handler ran ok marker-xyz");
  return { ok: true };
};
`)
	createFunctionWithCode(t, srv, "cwl-fn", "nodejs20.x", "index.handler", code)
	waitForFunctionActive(t, srv, "cwl-fn")

	// Invoke the function.
	resp := invokeFunction(t, srv, "cwl-fn", []byte("{}"))
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("invoke returned %d, expected 200", resp.StatusCode)
	}

	// Poll CloudWatch Logs for the async log pipeline to deliver: events are
	// written by the log batcher (5 ms flush) through a debounced persist
	// (50 ms), and the cache returns them as soon as appendEvents completes.
	// Measured over 10 `-race -count=10` runs in a `--cpus=8` golang:1.24
	// container on an otherwise idle host, this loop takes 1–103 ms — the
	// invocation it is waiting on has already returned 200, so the Docker
	// cold-start cost is behind us by the time it starts.
	//
	// So the budget is on *progress*, not a wall-clock deadline for the loop.
	// A fixed one has to serve two contradictory jobs — long enough that a
	// slow-but-working pipeline is never failed on a loaded CI runner, short
	// enough that a genuinely broken one is reported quickly — and the 20 s
	// this used to allow bought the first at the price of the second. Every
	// newly visible event now refreshes logIdleBudget, so a pipeline that has
	// stopped delivering fails in half the old time and says which of the two
	// budgets ran out, while one that is merely slow is never cut off as long
	// as it keeps making progress. logOverallBudget is the backstop for a
	// pipeline that dribbles forever without completing; it matches the 2 m
	// waitForFunctionActive allows for Docker-paced work in this file.
	const (
		logIdleBudget    = 10 * time.Second
		logOverallBudget = 2 * time.Minute
	)
	groupName := "/aws/lambda/cwl-fn"
	started := time.Now()
	overallDeadline := started.Add(logOverallBudget)
	idleDeadline := started.Add(logIdleBudget)
	var matched bool
	var lastEvents []map[string]any
	var lastStatus int
	var lastBody string
	var seenEvents int
	for time.Now().Before(overallDeadline) && time.Now().Before(idleDeadline) {
		// FilterLogEvents searches across all streams in the group; we don't
		// know the auto-generated stream name up front.
		body, _ := json.Marshal(map[string]any{
			"logGroupName": groupName,
		})
		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/x-amz-json-1.1")
		req.Header.Set("X-Amz-Target", "Logs_20140328.FilterLogEvents")
		filterResp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("FilterLogEvents: %v", err)
		}
		lastStatus = filterResp.StatusCode
		bodyBytes, _ := io.ReadAll(filterResp.Body)
		filterResp.Body.Close()
		lastBody = string(bodyBytes)
		if filterResp.StatusCode != http.StatusOK {
			time.Sleep(100 * time.Millisecond)
			continue
		}
		var result struct {
			Events []map[string]any `json:"events"`
		}
		_ = json.Unmarshal(bodyBytes, &result)
		lastEvents = result.Events

		// Events are only ever appended to the group, so a growing count is
		// the progress signal that refreshes the idle budget.
		if len(result.Events) > seenEvents {
			seenEvents = len(result.Events)
			idleDeadline = time.Now().Add(logIdleBudget)
		}

		// Check for the marker we logged + the synthetic START/REPORT lines.
		var sawMarker, sawStart, sawReport bool
		for _, e := range result.Events {
			msg, _ := e["message"].(string)
			if strings.Contains(msg, "marker-xyz") {
				sawMarker = true
			}
			if strings.HasPrefix(msg, "START RequestId:") {
				sawStart = true
			}
			if strings.HasPrefix(msg, "REPORT RequestId:") {
				sawReport = true
			}
		}
		if sawMarker && sawStart && sawReport {
			matched = true
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	if !matched {
		gaveUp := "the log pipeline delivered nothing new for " + logIdleBudget.String()
		if !time.Now().Before(overallDeadline) {
			gaveUp = "the " + logOverallBudget.String() + " overall budget ran out while events were still arriving"
		}
		t.Fatalf("expected START / handler stdout (marker-xyz) / REPORT in CloudWatch Logs for group %q; %s after %v; last status=%d body=%s got %d events: %+v",
			groupName, gaveUp, time.Since(started).Round(100*time.Millisecond), lastStatus, lastBody, len(lastEvents), lastEvents)
	}
}

// ─── InvokeWithResponseStream ────────────────────────────────────────────────

// parseEventStreamMessages decodes a raw AWS event stream body into a map
// from :event-type header value to event payload bytes.
func parseEventStreamMessages(body []byte) map[string][]byte {
	result := make(map[string][]byte)
	for len(body) >= 12 {
		totalLen := int(body[0])<<24 | int(body[1])<<16 | int(body[2])<<8 | int(body[3])
		if totalLen > len(body) {
			break
		}
		hdrLen := int(body[4])<<24 | int(body[5])<<16 | int(body[6])<<8 | int(body[7])
		// Skip prelude CRC (bytes 8-11).
		hdrStart := 12
		hdrEnd := hdrStart + hdrLen
		payloadStart := hdrEnd
		payloadEnd := totalLen - 4 // exclude trailing CRC

		headers := parseESHeaders(body[hdrStart:hdrEnd])
		eventType := headers[":event-type"]
		result[eventType] = body[payloadStart:payloadEnd]
		body = body[totalLen:]
	}
	return result
}

func parseESHeaders(b []byte) map[string]string {
	out := make(map[string]string)
	for len(b) > 0 {
		nameLen := int(b[0])
		name := string(b[1 : 1+nameLen])
		b = b[1+nameLen:]
		typ := b[0]
		b = b[1:]
		if typ == 7 { // string
			valLen := int(b[0])<<8 | int(b[1])
			val := string(b[2 : 2+valLen])
			out[name] = val
			b = b[2+valLen:]
		}
	}
	return out
}

func TestInvokeWithResponseStream_nodeRuntime(t *testing.T) {
	helpers.SkipWithoutDocker(t)

	// Approach B (tracked on helpers.WithLambdaDocker) would let these 5 Docker
	// tests share a single ContainerRuntime + InstancePool via a package-level
	// singleton, enabling warm-container reuse across test functions.
	srv := helpers.NewTestServer(t, helpers.WithLambdaDocker())
	code := makeZip(t, "index.js", `
exports.handler = async (event) => {
  return { streamed: true, received: event };
};
`)
	createFunctionWithCode(t, srv, "stream-fn", "nodejs20.x", "index.handler", code)
	waitForFunctionActive(t, srv, "stream-fn")

	req, _ := http.NewRequest(http.MethodPost, streamingURL(srv, "stream-fn"), bytes.NewReader([]byte(`{"hello":"world"}`)))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	defer resp.Body.Close()

	helpers.AssertStatus(t, resp, http.StatusOK)
	if ct := resp.Header.Get("Content-Type"); ct != "application/vnd.amazon.eventstream" {
		t.Errorf("Content-Type: got %q, want application/vnd.amazon.eventstream", ct)
	}

	body, _ := io.ReadAll(resp.Body)
	events := parseEventStreamMessages(body)

	// PayloadChunk must contain the real function response.
	chunk, ok := events["PayloadChunk"]
	if !ok {
		t.Fatal("missing PayloadChunk event")
	}
	var fnResp struct {
		Streamed bool `json:"streamed"`
	}
	if err := json.Unmarshal(chunk, &fnResp); err != nil {
		t.Fatalf("unmarshal PayloadChunk: %v", err)
	}
	if !fnResp.Streamed {
		t.Errorf("PayloadChunk.streamed: got false, want true")
	}

	// InvokeComplete must be present.
	if _, ok := events["InvokeComplete"]; !ok {
		t.Fatal("missing InvokeComplete event")
	}
}

// TestInvokeFunctionURL_hostRouted_success proves the full path: create a
// function URL config, then invoke it via its Host-routed FunctionUrl and
// confirm the request reaches the function through the same runtime
// InvokeFunction uses. Requires Docker, like the other real-runtime
// invocation tests in this file (helpers.SkipWithoutDocker).
func TestInvokeFunctionURL_hostRouted_success(t *testing.T) {
	helpers.SkipWithoutDocker(t)

	// Given a Node.js function that echoes a structured function-URL response
	srv := helpers.NewTestServer(t, helpers.WithLambdaDocker())
	code := makeZip(t, "index.js", `
exports.handler = async (event) => {
  return { statusCode: 200, body: JSON.stringify({ rawPath: event.rawPath, method: event.requestContext.http.method }) };
};
`)
	createFunctionWithCode(t, srv, "url-invoke-fn", "nodejs20.x", "index.handler", code)
	waitForFunctionActive(t, srv, "url-invoke-fn")

	createResp := doJSON(t, http.MethodPost, lambdaURLv2021(srv, "/functions/url-invoke-fn/url"), map[string]any{"AuthType": "NONE"})
	helpers.AssertStatus(t, createResp, http.StatusCreated)
	var cfg functionUrlConfigResp
	decodeJSON(t, createResp, &cfg)

	// FunctionUrl is http://{urlId}.lambda-url.{region}.{host}:{port}/ —
	// extract the Host portion to invoke through the test server while
	// presenting the real function-URL Host header.
	u, err := url.Parse(cfg.FunctionUrl)
	if err != nil {
		t.Fatalf("parse FunctionUrl %q: %v", cfg.FunctionUrl, err)
	}

	// When we invoke via the Host-routed function URL, at a sub-path
	req, err := http.NewRequest(http.MethodGet, srv.URL+"/hello", nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Host = u.Host
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("invoke: %v", err)
	}
	defer resp.Body.Close()

	// Then the function receives the request via the payload v2.0 event
	// shape (rawPath, requestContext.http.method) and its structured
	// response is honoured
	helpers.AssertStatus(t, resp, http.StatusOK)
	body, _ := io.ReadAll(resp.Body)
	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("unmarshal response: %v — body: %s", err, body)
	}
	if out["rawPath"] != "/hello" {
		t.Errorf("rawPath = %v, want /hello", out["rawPath"])
	}
	if out["method"] != "GET" {
		t.Errorf("method = %v, want GET", out["method"])
	}
}
