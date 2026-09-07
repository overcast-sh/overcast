package lambda

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/overcast-sh/overcast/internal/clock"
	"github.com/overcast-sh/overcast/internal/config"
	"github.com/overcast-sh/overcast/internal/docker"
	"go.uber.org/zap"
)

func TestContainerRuntimeColdStartSlot_boundsConcurrency(t *testing.T) {
	// Given: a runtime with two cold-start slots already occupied.
	runtime := &ContainerRuntime{coldStartSem: make(chan struct{}, 2)}
	releaseFirst, err := runtime.acquireColdStartSlot(context.Background())
	if err != nil {
		t.Fatalf("acquire first slot: %v", err)
	}
	releaseSecond, err := runtime.acquireColdStartSlot(context.Background())
	if err != nil {
		t.Fatalf("acquire second slot: %v", err)
	}
	defer releaseSecond()

	// When: a third caller waits for a slot.
	acquired := make(chan func(), 1)
	go func() {
		release, acquireErr := runtime.acquireColdStartSlot(context.Background())
		if acquireErr != nil {
			t.Errorf("acquire third slot: %v", acquireErr)
			return
		}
		acquired <- release
	}()

	select {
	case release := <-acquired:
		release()
		t.Fatal("third acquire completed before a slot was released")
	case <-time.After(20 * time.Millisecond):
	}

	// Then: releasing a slot lets the queued caller proceed.
	releaseFirst()
	select {
	case release := <-acquired:
		release()
	case <-time.After(time.Second):
		t.Fatal("third acquire did not complete after a slot was released")
	}
}

func TestContainerRuntimeColdStartSlot_contextCancelled(t *testing.T) {
	// Given: a runtime with its only cold-start slot occupied.
	runtime := &ContainerRuntime{coldStartSem: make(chan struct{}, 1)}
	release, err := runtime.acquireColdStartSlot(context.Background())
	if err != nil {
		t.Fatalf("acquire slot: %v", err)
	}
	defer release()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	// When: another caller tries to wait with a cancelled context.
	_, err = runtime.acquireColdStartSlot(ctx)

	// Then: the wait returns promptly with the context error.
	if err != context.Canceled {
		t.Fatalf("acquire error = %v, want context.Canceled", err)
	}
}

func TestContainerInstanceAwaitReady_contextCancelled(t *testing.T) {
	// Given: a container instance whose runtime has not polled /next yet.
	ready := make(chan struct{})
	inst := &containerInstance{readyCh: ready}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	// When: readiness is awaited with a cancelled context.
	err := inst.AwaitReady(ctx)

	// Then: the context error is returned.
	if err != context.Canceled {
		t.Fatalf("AwaitReady error = %v, want context.Canceled", err)
	}
}

func TestContainerInstanceAwaitReady_ready(t *testing.T) {
	// Given: a container instance whose ready channel is already closed.
	ready := make(chan struct{})
	close(ready)
	inst := &containerInstance{readyCh: ready}

	// When: readiness is awaited.
	err := inst.AwaitReady(context.Background())

	// Then: it returns immediately.
	if err != nil {
		t.Fatalf("AwaitReady: %v", err)
	}
}

func TestContainerRuntimeBuildEnv_includesServiceSpecificEndpointURLs(t *testing.T) {
	// Given: a Docker Lambda runtime with an Overcast endpoint reachable from containers.
	runtime := &ContainerRuntime{
		cfg:              &config.Config{Region: "us-east-1", AccountID: "000000000000"},
		overcastEndpoint: "http://172.18.0.1:4566",
	}
	fn := &Function{
		Name:       "demo",
		ARN:        "arn:aws:lambda:us-east-1:000000000000:function:demo",
		Runtime:    "nodejs22.x",
		Handler:    "index.handler",
		MemorySize: 128,
		Timeout:    3,
	}

	// When: Lambda environment variables are built.
	env := envMap(runtime.buildEnv(fn, "stream", initTypeOnDemand, "172.18.0.1:41001", nil))

	// Then: both the global SDK endpoint and Parameters/Secrets backend endpoints target Overcast.
	if env["AWS_ENDPOINT_URL"] != runtime.overcastEndpoint {
		t.Fatalf("AWS_ENDPOINT_URL = %q", env["AWS_ENDPOINT_URL"])
	}
	if env["AWS_ENDPOINT_URL_SSM"] != runtime.overcastEndpoint {
		t.Fatalf("AWS_ENDPOINT_URL_SSM = %q", env["AWS_ENDPOINT_URL_SSM"])
	}
	if env["AWS_ENDPOINT_URL_SECRETS_MANAGER"] != runtime.overcastEndpoint {
		t.Fatalf("AWS_ENDPOINT_URL_SECRETS_MANAGER = %q", env["AWS_ENDPOINT_URL_SECRETS_MANAGER"])
	}
}

func TestContainerRuntimeBuildEnv_runtimeEndpointsOverrideBlankUserEnv(t *testing.T) {
	// Given: a function template includes empty AWS endpoint placeholders.
	runtime := &ContainerRuntime{
		cfg:              &config.Config{Region: "us-east-1", AccountID: "000000000000"},
		overcastEndpoint: "http://172.18.0.1:4566",
	}
	fn := &Function{
		Name:       "demo",
		ARN:        "arn:aws:lambda:us-east-1:000000000000:function:demo",
		Runtime:    "nodejs22.x",
		Handler:    "index.handler",
		MemorySize: 128,
		Timeout:    3,
		Environment: map[string]string{
			"AWS_ENDPOINT_URL":                 "",
			"AWS_ENDPOINT_URL_SSM":             "",
			"AWS_ENDPOINT_URL_SECRETS_MANAGER": "",
			"AWS_ACCESS_KEY_ID":                "",
			"AWS_SECRET_ACCESS_KEY":            "",
			"AWS_SESSION_TOKEN":                "",
		},
	}

	// When: Lambda environment variables are built for the container.
	env := envMap(runtime.buildEnv(fn, "stream", initTypeOnDemand, "172.18.0.1:41001", nil))

	// Then: runtime-provided endpoint and credential values win so extensions inherit usable values.
	if env["AWS_ENDPOINT_URL"] != runtime.overcastEndpoint {
		t.Fatalf("AWS_ENDPOINT_URL = %q", env["AWS_ENDPOINT_URL"])
	}
	if env["AWS_ENDPOINT_URL_SSM"] != runtime.overcastEndpoint {
		t.Fatalf("AWS_ENDPOINT_URL_SSM = %q", env["AWS_ENDPOINT_URL_SSM"])
	}
	if env["AWS_ENDPOINT_URL_SECRETS_MANAGER"] != runtime.overcastEndpoint {
		t.Fatalf("AWS_ENDPOINT_URL_SECRETS_MANAGER = %q", env["AWS_ENDPOINT_URL_SECRETS_MANAGER"])
	}
	if env["AWS_ACCESS_KEY_ID"] != "overcast" || env["AWS_SECRET_ACCESS_KEY"] != "overcast" || env["AWS_SESSION_TOKEN"] != "overcast" {
		t.Fatalf("credentials not overridden: access=%q secret=%q token=%q", env["AWS_ACCESS_KEY_ID"], env["AWS_SECRET_ACCESS_KEY"], env["AWS_SESSION_TOKEN"])
	}
}

func TestDockerPlatformForLambdaArchitectures_defaultsToX8664(t *testing.T) {
	// Given: AWS defaults Lambda functions to x86_64 when Architectures is omitted.
	// When: the container platform is resolved.
	got := dockerPlatformForLambdaArchitectures(nil)

	// Then: Docker runs the matching amd64 Lambda runtime image even on arm64 hosts.
	if got != "linux/amd64" {
		t.Fatalf("platform = %q, want linux/amd64", got)
	}
}

func TestDockerPlatformForLambdaArchitectures_arm64(t *testing.T) {
	// Given: a function explicitly configured for arm64.
	// When: the container platform is resolved.
	got := dockerPlatformForLambdaArchitectures([]string{"arm64"})

	// Then: Docker runs the matching arm64 Lambda runtime image.
	if got != "linux/arm64" {
		t.Fatalf("platform = %q, want linux/arm64", got)
	}
}

func TestContainerRuntimeEnsureImage_pullsRequestedPlatformWhenCachedTagDiffers(t *testing.T) {
	// Given: Docker has the image tag cached for arm64, but Lambda needs amd64.
	var pulledPlatform string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1.45/images/"):
			_ = json.NewEncoder(w).Encode(docker.ImageInspect{OS: "linux", Architecture: "arm64"})
		case r.Method == http.MethodPost && r.URL.Path == "/v1.45/images/create":
			pulledPlatform = r.URL.Query().Get("platform")
			_, _ = w.Write([]byte(`{"status":"done"}`))
		case r.Method == http.MethodPost && r.URL.Path == "/v1.45/images/prune":
			_, _ = w.Write([]byte(`{"ImagesDeleted":null,"SpaceReclaimed":0}`))
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.String())
		}
	}))
	defer server.Close()
	runtime := &ContainerRuntime{docker: docker.NewClient("tcp://"+server.Listener.Addr().String(), zap.NewNop()), logger: zap.NewNop()}

	// When: Lambda ensures the amd64 variant.
	ctx := context.Background()
	err := runtime.ensureImage(ctx, runtime.resolveImage(ctx, "public.ecr.aws/lambda/nodejs:22"), "linux/amd64")

	// Then: it does not trust the wrong-architecture cached tag and pulls amd64.
	if err != nil {
		t.Fatalf("ensureImage: %v", err)
	}
	if pulledPlatform != "linux/amd64" {
		t.Fatalf("pulled platform = %q, want linux/amd64", pulledPlatform)
	}
}

func TestZipToTar_preservesExtensions(t *testing.T) {
	// Given: a layer zip containing an external Lambda extension.
	var zipBuf bytes.Buffer
	zw := zip.NewWriter(&zipBuf)
	fh := &zip.FileHeader{Name: "extensions/parameters-secrets-extension", Method: zip.Deflate}
	fh.SetMode(0o755)
	w, err := zw.CreateHeader(fh)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("#!/bin/sh\n")); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}

	// When: the zip is converted to a Docker tar archive for /opt injection.
	tarData, err := zipToTar(zipBuf.Bytes())
	if err != nil {
		t.Fatal(err)
	}

	// Then: the extension executable is still present.
	mode, ok := tarEntryMode(t, tarData, "extensions/parameters-secrets-extension")
	if !ok {
		t.Fatal("extension entry was stripped from layer tar")
	}
	if mode&0o111 == 0 {
		t.Fatalf("extension mode = %#o, want executable bit", mode)
	}
}

func TestDiscoverExternalExtensions_executableRootFiles(t *testing.T) {
	// Given: a layer zip with one executable external extension and nearby non-extension files.
	zipData := testLayerZip(t, map[string]struct {
		mode int
		body string
	}{
		"extensions/parameters-secrets-extension":       {mode: 0o755, body: "#!/bin/sh\n"},
		"extensions/not-executable":                     {mode: 0o644, body: "#!/bin/sh\n"},
		"extensions/nested/ignored":                     {mode: 0o755, body: "#!/bin/sh\n"},
		"python/lib/python3.12/site-packages/helper.py": {mode: 0o644, body: ""},
	})

	// When: the layer is scanned for external extensions.
	extensions := discoverExternalExtensions(zipData)

	// Then: only executable files directly under /opt/extensions are expected to register.
	if len(extensions) != 1 || extensions[0] != "parameters-secrets-extension" {
		t.Fatalf("extensions = %#v", extensions)
	}
}

func TestContainerRuntimeCopyLayersToContainer_missingRemoteLayerReturnsLambdaLogWarning(t *testing.T) {
	// Given: a function references an AWS-managed layer that is not published locally,
	// and remote layer fetching has no AWS credentials configured.
	runtime := &ContainerRuntime{
		cfg:    &config.Config{DataDir: t.TempDir(), LambdaFetchRemoteLayers: true},
		clk:    clock.NewMock(),
		logger: zap.NewNop(),
	}
	runtime.SetLayerContentFetcher(func(context.Context, string) ([]byte, error) {
		return nil, fs.ErrNotExist
	})
	runtime.SetRemoteLayerFetcher(NewRemoteLayerFetcher(runtime.cfg, zap.NewNop(), runtime.clk))
	fn := &Function{Layers: []LayerVersionLink{{ARN: "arn:aws:lambda:us-east-1:177933569100:layer:AWS-Parameters-and-Secrets-Lambda-Extension:11"}}}

	// When: layers are resolved for the provisioning archive.
	tars, extensions, logLines, err := runtime.resolveLayerTars(context.Background(), fn)

	// Then: the missing layer is skipped and a warning line is available for the Lambda logs.
	if err != nil {
		t.Fatalf("resolveLayerTars: %v", err)
	}
	if len(tars) != 0 {
		t.Fatalf("tars = %d, want none for a skipped layer", len(tars))
	}
	if len(extensions) != 0 {
		t.Fatalf("extensions = %#v, want none", extensions)
	}
	if len(logLines) != 1 {
		t.Fatalf("log lines = %#v, want one warning", logLines)
	}
	logLine := logLines[0]
	for _, want := range []string{
		"[Overcast Lambda] WARNING",
		"AWS-Parameters-and-Secrets-Lambda-Extension:11",
		"was not loaded because it is not available locally",
		"remote AWS layer fetching is not configured or failed",
	} {
		if !strings.Contains(logLine, want) {
			t.Fatalf("log line %q does not contain %q", logLine, want)
		}
	}
}

// AWS names a Lambda log stream after the date and the execution environment's
// GUID: YYYY/MM/DD/[$LATEST] followed by 32 hex characters, as in
// 2019/07/12/[$LATEST]312c2d81e2e64af58dbe557754f9aa13. Anything shorter fails
// a caller matching stream names against that shape.
func TestLambdaLogStreamName_matchesAWSShape(t *testing.T) {
	shape := regexp.MustCompile(`^\d{4}/\d{2}/\d{2}/\[\$LATEST\][0-9a-f]{32}$`)
	clk := clock.NewMock()
	clk.Set(time.Date(2026, 8, 10, 2, 27, 36, 0, time.UTC))

	for name, got := range map[string]string{
		"container runtime": lambdaLogStreamName(clk),
		"node runtime":      newNodeRuntime(clk, zap.NewNop()).lambdaLogStreamName(),
	} {
		t.Run(name, func(t *testing.T) {
			if !shape.MatchString(got) {
				t.Errorf("log stream name %q does not match %v", got, shape)
			}
		})
	}
}

func tarEntryMode(t *testing.T, tarData []byte, name string) (int64, bool) {
	t.Helper()
	tr := tar.NewReader(bytes.NewReader(tarData))
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return 0, false
		}
		if err != nil {
			t.Fatal(err)
		}
		if h.Name == name {
			return h.Mode, true
		}
	}
}

func testLayerZip(t *testing.T, files map[string]struct {
	mode int
	body string
}) []byte {
	t.Helper()
	var zipBuf bytes.Buffer
	zw := zip.NewWriter(&zipBuf)
	for name, file := range files {
		fh := &zip.FileHeader{Name: name, Method: zip.Deflate}
		fh.SetMode(fs.FileMode(file.mode))
		w, err := zw.CreateHeader(fh)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(file.body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return zipBuf.Bytes()
}

func envMap(env []string) map[string]string {
	out := make(map[string]string, len(env))
	for _, item := range env {
		key, value, ok := strings.Cut(item, "=")
		if ok {
			out[key] = value
		}
	}
	return out
}
