// Package lambdawebdocker_test contains the Lambda Web Functions tests that
// need a running Docker daemon: the Docker environment provider against the
// real nodejs24.x base image.
//
// It is the sibling of the lambdaweb unit tests, which drive the same provider
// against a fake Engine API. Nothing here calls t.Parallel(): these tests share
// the daemon's address pool and run containers that are measured for speed.
//
// Run: go test ./tests/integration/lambdawebdocker/...
package lambdawebdocker_test

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/overcast-sh/overcast/internal/clock"
	"github.com/overcast-sh/overcast/internal/config"
	"github.com/overcast-sh/overcast/internal/docker"
	"github.com/overcast-sh/overcast/internal/docker/dockertest"
	"github.com/overcast-sh/overcast/internal/events"
	"github.com/overcast-sh/overcast/internal/services/lambdaweb"
	"github.com/overcast-sh/overcast/internal/state"
	"github.com/overcast-sh/overcast/tests/helpers"
)

const nodeImage = "public.ecr.aws/lambda/nodejs:24"

// TestMain pre-pulls the base image so that no test pays for it (and the
// cold-start figures below measure starting, not downloading).
func TestMain(m *testing.M) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	if helpers.DockerAvailable(ctx) == nil {
		dc := docker.NewClient(helpers.TestDockerSocket(), zap.NewNop())
		if exists, _ := dc.ImageExists(ctx, nodeImage); !exists {
			_ = dc.PullImage(ctx, nodeImage)
		}
	}
	cancel()
	os.Exit(m.Run())
}

// ─── Harness ─────────────────────────────────────────────────────────────────

// logRecorder is the CloudWatch Logs the provider writes to.
type logRecorder struct {
	mu      sync.Mutex
	groups  map[string]bool
	entries map[string][]events.LogEntry // "group|stream"
}

func (l *logRecorder) EnsureLogGroup(_ context.Context, g string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.groups == nil {
		l.groups = map[string]bool{}
	}
	l.groups[g] = true
	return nil
}

func (l *logRecorder) EnsureLogStream(ctx context.Context, g, s string) error {
	return l.EnsureLogGroup(ctx, g)
}

func (l *logRecorder) WriteLogEvents(_ context.Context, g, s string, e []events.LogEntry) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.entries == nil {
		l.entries = map[string][]events.LogEntry{}
	}
	l.entries[g+"|"+s] = append(l.entries[g+"|"+s], e...)
	return nil
}

func (l *logRecorder) stream(g, s string) []events.LogEntry {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]events.LogEntry(nil), l.entries[g+"|"+s]...)
}

// packages is the S3 stand-in: the zips tests register by key.
type packages struct {
	mu sync.Mutex
	m  map[string][]byte
}

func (p *packages) put(key string, zipData []byte) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.m == nil {
		p.m = map[string][]byte{}
	}
	p.m[key] = zipData
}

func (p *packages) fetch(_ context.Context, _, key, _ string) ([]byte, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if b, ok := p.m[key]; ok {
		return b, nil
	}
	return nil, lambdaweb.ErrNoSuchPackage
}

type harness struct {
	provider *lambdaweb.DockerProvider
	dc       *docker.Client
	logs     *logRecorder
	pkgs     *packages
	bus      *events.Bus
}

// newHarness builds a provider on networks of its own, removed with the test.
func newHarness(t *testing.T) *harness {
	t.Helper()
	helpers.SkipWithoutDocker(t)

	network := fmt.Sprintf("overcast_lwdp_test_%d", time.Now().UnixNano())
	cfg := &config.Config{
		Region:           "us-east-1",
		AccountID:        "000000000000",
		Network:          network,
		Port:             4566,
		LambdaTarCacheMB: 64,
	}
	dc := docker.NewClient(helpers.TestDockerSocket(), zap.NewNop())
	ctx := t.Context()
	for _, n := range []string{cfg.Network, cfg.ControlNetwork()} {
		_, err := dc.CreateNetwork(ctx, n)
		require.NoError(t, err, "create network %s", n)
	}
	t.Cleanup(func() {
		cctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		dockertest.RemoveOwned(cctx, dc, []string{cfg.Network, cfg.ControlNetwork()}, t.Logf)
	})

	h := &harness{dc: dc, logs: &logRecorder{}, pkgs: &packages{}, bus: events.NewBus()}
	h.provider = lambdaweb.NewDockerProvider(cfg, zap.NewNop(), clock.New())
	h.provider.InitStore(state.NewMemoryStore())
	h.provider.InitLogWriter(h.logs)
	h.provider.InitBus(h.bus)
	h.provider.InitCodeFetcher(h.pkgs.fetch)
	h.provider.SetDocker(dc)
	t.Cleanup(func() {
		sctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		h.provider.Stop(sctx)
	})
	return h
}

// zipOf builds a package zip from name → content.
func zipOf(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, content := range files {
		w, err := zw.Create(name)
		require.NoError(t, err)
		_, err = io.WriteString(w, content)
		require.NoError(t, err)
	}
	require.NoError(t, zw.Close())
	return buf.Bytes()
}

// req is a StartRequest for the package registered as key.
func (h *harness) req(key string) lambdaweb.StartRequest {
	return lambdaweb.StartRequest{
		Region:      "us-east-1",
		Function:    "fn-" + strings.ReplaceAll(key, ".", "-"),
		Endpoint:    "ep",
		Bucket:      "code",
		Key:         key,
		VersionID:   "v1",
		InitTimeout: 10 * time.Second,
		LogGroup:    "/aws/lambda/web/" + key,
		LogStream:   "2026/01/01/[rev]" + key,
		AppLogLevel: "INFO",
	}
}

func (h *harness) start(t *testing.T, r lambdaweb.StartRequest) lambdaweb.Environment {
	t.Helper()
	env, fail := h.provider.StartEnvironment(t.Context(), r)
	require.Nil(t, fail, "start failed: %v", fail)
	require.NotNil(t, env)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = env.Stop(ctx, time.Second)
	})
	return env
}

func get(t *testing.T, addr, path string, header map[string]string) (int, string) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://"+addr+path, nil)
	require.NoError(t, err)
	for k, v := range header {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return resp.StatusCode, string(body)
}

// containerExists reports whether the daemon still knows the container.
func (h *harness) containerExists(t *testing.T, id string) bool {
	t.Helper()
	_, err := h.dc.InspectContainer(t.Context(), id)
	if err == nil {
		return true
	}
	require.True(t, docker.IsNotFound(err), "inspect: %v", err)
	return false
}

func (h *harness) eventuallyRemoved(t *testing.T, id string) {
	t.Helper()
	require.Eventually(t, func() bool { return !h.containerExists(t, id) }, 15*time.Second, 50*time.Millisecond,
		"container %s was not removed", id)
}

// ─── Fixtures ────────────────────────────────────────────────────────────────

const serverCJS = `
const http = require('node:http');
const [host, port] = process.env.AWS_LAMBDA_HTTP_ENDPOINT.split(':');
const server = http.createServer((req, res) => {
  if (req.url === '/log') {
    console.log('plain');
    console.log({ answer: 42 });
    console.info('info %s', 'formatted');
    console.warn('careful');
    console.error('broken');
    console.debug('debugging');
    process.stdout.write('raw out\n');
    return res.end('logged');
  }
  if (req.url === '/env') {
    return res.end(JSON.stringify(process.env));
  }
  res.end('hello ' + (req.headers['lambda-web-request-id'] || ''));
});
server.listen(Number(port), host);
`

const serverESM = `
import http from 'node:http';
const [host, port] = process.env.AWS_LAMBDA_HTTP_ENDPOINT.split(':');
http.createServer((req, res) => res.end('esm ok')).listen(Number(port), host);
`

// ─── Tests ───────────────────────────────────────────────────────────────────

func TestDockerProvider_startServeStop(t *testing.T) {
	// Given: a package with a dependency-free HTTP server
	h := newHarness(t)
	h.pkgs.put("app.zip", zipOf(t, map[string]string{"index.js": serverCJS}))

	// When: an environment is started, and then a second one from the cached
	// package, and a request is served
	t0 := time.Now()
	env := h.start(t, h.req("app.zip"))
	firstStart := time.Since(t0)
	status, body := get(t, env.Addr(), "/", map[string]string{"lambda-web-request-id": "req-1"})

	t1 := time.Now()
	second := h.start(t, h.req("app.zip"))
	warmStart := time.Since(t1)

	// Then: it answers, the second environment is a distinct container, and the
	// cold-start timings are on record
	assert.Equal(t, http.StatusOK, status)
	assert.Equal(t, "hello req-1", body)
	assert.NotEqual(t, env.ID(), second.ID())
	t.Logf("cold start (first environment of a revision, image local): %s", firstStart)
	t.Logf("cold start (package cached, image verified):               %s", warmStart)

	// When: it is stopped
	stopStart := time.Now()
	require.NoError(t, env.Stop(t.Context(), time.Second))

	// Then: the process is gone within the grace period, the exit was requested
	// and clean, and the container is removed
	select {
	case <-env.Done():
	default:
		t.Fatal("Done is not closed after Stop returned")
	}
	assert.Less(t, time.Since(stopStart), 2*time.Second)
	assert.True(t, env.ExitReason().Requested)
	assert.False(t, env.ExitReason().OOM)
	h.eventuallyRemoved(t, env.ID())
}

func TestDockerProvider_environmentContract(t *testing.T) {
	// Given: a running environment started with user variables
	h := newHarness(t)
	h.pkgs.put("env.zip", zipOf(t, map[string]string{"index.js": serverCJS}))
	r := h.req("env.zip")
	r.Env = map[string]string{"GREETING": "hi"}
	env := h.start(t, r)

	// When: the server reports its environment
	_, body := get(t, env.Addr(), "/env", nil)
	var got map[string]string
	require.NoError(t, json.Unmarshal([]byte(body), &got))

	// Then: the runtime variables are set next to the user's
	assert.Equal(t, "hi", got["GREETING"])
	assert.Equal(t, "0.0.0.0:8080", got["AWS_LAMBDA_HTTP_ENDPOINT"])
	assert.Equal(t, "us-east-1", got["AWS_REGION"])
	assert.Equal(t, "us-east-1", got["AWS_DEFAULT_REGION"])
	assert.Equal(t, "fn-env-zip", got["AWS_LAMBDA_FUNCTION_NAME"])
	assert.Equal(t, "2048", got["AWS_LAMBDA_FUNCTION_MEMORY_SIZE"])
	assert.Equal(t, "/var/task", got["LAMBDA_TASK_ROOT"])
	assert.Equal(t, "--max-old-space-size=1024", got["NODE_OPTIONS"])
	assert.NotEmpty(t, got["AWS_ENDPOINT_URL"])
	assert.NotEmpty(t, got["AWS_ACCESS_KEY_ID"])

	// And: the container carries the limits and the ownership label
	info, err := h.dc.InspectContainer(t.Context(), env.ID())
	require.NoError(t, err)
	assert.Equal(t, "lambdaweb", info.Config.Labels["overcast.service"])
	assert.Equal(t, "fn-env-zip", info.Config.Labels["overcast.resource-id"])

	// And: Stats answers
	stats := env.Stats(t.Context())
	assert.Greater(t, stats.MaxMemoryMB, 1.0)
}

func TestDockerProvider_esmEntrypoint(t *testing.T) {
	// Given: an ES module entry file named by AWS_LAMBDA_NODEJS_ENTRYPOINT
	h := newHarness(t)
	h.pkgs.put("esm.zip", zipOf(t, map[string]string{"server.mjs": serverESM}))
	r := h.req("esm.zip")
	r.Env = map[string]string{"AWS_LAMBDA_NODEJS_ENTRYPOINT": "server.mjs"}

	// When: it is started and asked
	env := h.start(t, r)
	status, body := get(t, env.Addr(), "/", nil)

	// Then: the module ran
	assert.Equal(t, http.StatusOK, status)
	assert.Equal(t, "esm ok", body)
}

func TestDockerProvider_applicationLogsReachCloudWatch(t *testing.T) {
	// Given: a running environment with application log level INFO
	h := newHarness(t)
	h.pkgs.put("log.zip", zipOf(t, map[string]string{"index.js": serverCJS}))
	r := h.req("log.zip")
	env := h.start(t, r)

	// When: a request that logs at every level is served, then the environment stops
	status, _ := get(t, env.Addr(), "/log", map[string]string{
		"lambda-web-request-id":    "req-42",
		"lambda-web-endpoint-name": "my-endpoint",
	})
	require.Equal(t, http.StatusOK, status)
	require.NoError(t, env.Stop(t.Context(), time.Second))

	// Then: each line is a JSON application log with the request's identity,
	// filtered by level, stamped with the application's own time
	var lines []map[string]any
	var entries []events.LogEntry
	require.Eventually(t, func() bool {
		entries = h.logs.stream(r.LogGroup, r.LogStream)
		lines = lines[:0]
		for _, e := range entries {
			var m map[string]any
			if json.Unmarshal([]byte(e.Message), &m) == nil && m["requestId"] == "req-42" {
				lines = append(lines, m)
			}
		}
		return len(lines) >= 6
	}, 15*time.Second, 50*time.Millisecond, "log lines never arrived: %v", entries)

	byMessage := map[string]map[string]any{}
	for _, m := range lines {
		byMessage[fmt.Sprint(m["message"])] = m
	}
	assert.Equal(t, "INFO", byMessage["plain"]["level"])
	assert.Equal(t, "my-endpoint", byMessage["plain"]["endpointName"])
	assert.Equal(t, "us-east-1", byMessage["plain"]["region"])
	assert.Equal(t, map[string]any{"answer": float64(42)}, byMessage["map[answer:42]"]["message"], "an object stays an object")
	assert.Equal(t, "INFO", byMessage["info formatted"]["level"])
	assert.Equal(t, "WARN", byMessage["careful"]["level"])
	assert.Equal(t, "ERROR", byMessage["broken"]["level"])
	assert.Equal(t, "INFO", byMessage["raw out"]["level"], "a raw stdout write is wrapped as INFO")
	assert.NotContains(t, byMessage, "debugging", "DEBUG is below the INFO threshold")

	for _, e := range entries {
		var m map[string]any
		if json.Unmarshal([]byte(e.Message), &m) != nil {
			continue
		}
		ts, err := time.Parse(time.RFC3339Nano, fmt.Sprint(m["timestamp"]))
		require.NoError(t, err)
		assert.Equal(t, ts.UnixMilli(), e.Timestamp, "the event carries the application's timestamp")
	}
}

func TestDockerProvider_importErrors(t *testing.T) {
	cases := []struct {
		name  string
		files map[string]string
		env   map[string]string
		want  string
	}{
		{"missing dependency", map[string]string{"index.js": "require('./nope');"}, nil, "Runtime.ImportCodeError"},
		{"syntax error", map[string]string{"index.js": "const = ;"}, nil, "Runtime.ImportCodeError"},
		{"missing entry file", map[string]string{"other.js": ""}, nil, "Runtime.ImportCodeError"},
		{"entry named but absent", map[string]string{"index.js": serverCJS}, map[string]string{"AWS_LAMBDA_NODEJS_ENTRYPOINT": "gone.mjs"}, "Runtime.ImportCodeError"},
		{"throws on init", map[string]string{"index.js": "throw new Error('boom at init');"}, nil, "Application.InitCodeError"},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Given: a package whose entry file cannot run
			h := newHarness(t)
			key := fmt.Sprintf("bad%d.zip", i)
			h.pkgs.put(key, zipOf(t, tc.files))
			r := h.req(key)
			r.Env = tc.env

			// When: an environment is started
			env, fail := h.provider.StartEnvironment(t.Context(), r)

			// Then: it fails with the error type, quotes the process output, and
			// leaves no container behind
			require.Nil(t, env)
			require.NotNil(t, fail)
			assert.Equal(t, tc.want, fail.Type, "detail: %s", fail.Detail)
			assert.NotEmpty(t, fail.Detail)
			assert.LessOrEqual(t, len(fail.Detail), 4096)
			containers, err := h.dc.ListContainers(t.Context(), "lambdaweb")
			require.NoError(t, err)
			for _, c := range containers {
				assert.NotEqual(t, r.Function, c.ResourceID(), "container %s left behind", c.ID)
			}
		})
	}
}

func TestDockerProvider_neverListens(t *testing.T) {
	// Given: a package that runs but never opens its port
	h := newHarness(t)
	h.pkgs.put("idle.zip", zipOf(t, map[string]string{"index.js": "console.log('started, not listening'); setInterval(() => {}, 1000);"}))
	r := h.req("idle.zip")
	r.InitTimeout = 1500 * time.Millisecond

	// When: an environment is started
	t0 := time.Now()
	env, fail := h.provider.StartEnvironment(t.Context(), r)

	// Then: the init timeout is reported after about that long, with the output
	require.Nil(t, env)
	require.NotNil(t, fail)
	assert.Equal(t, "Application.TimeoutListenError", fail.Type)
	assert.Contains(t, fail.Detail, "started, not listening")
	assert.GreaterOrEqual(t, time.Since(t0), 1500*time.Millisecond)
	containers, err := h.dc.ListContainers(t.Context(), "lambdaweb")
	require.NoError(t, err)
	assert.Empty(t, containers, "the container must be removed")
}

func TestDockerProvider_sigtermReachesTheApplication(t *testing.T) {
	// Given: one application with a SIGTERM handler and one without
	h := newHarness(t)
	h.pkgs.put("graceful.zip", zipOf(t, map[string]string{"index.js": serverCJS + `
process.on('SIGTERM', () => { console.log('graceful shutdown ran'); process.exit(7); });`}))
	h.pkgs.put("plain.zip", zipOf(t, map[string]string{"index.js": serverCJS}))
	graceful := h.start(t, h.req("graceful.zip"))
	plain := h.start(t, h.req("plain.zip"))

	// When: each is stopped with a one second grace
	for name, env := range map[string]lambdaweb.Environment{"graceful": graceful, "plain": plain} {
		t0 := time.Now()
		require.NoError(t, env.Stop(t.Context(), time.Second), name)

		// Then: both are gone inside the grace period, not killed at its end
		assert.Less(t, time.Since(t0), 1500*time.Millisecond, "%s took too long to stop", name)
	}

	// And: the handler's exit status is the application's, the default is 0
	assert.Equal(t, 7, graceful.ExitReason().Code, "the application's own SIGTERM handler decides the exit")
	assert.Equal(t, 0, plain.ExitReason().Code, "without a handler the bootstrap exits 0")
	assert.True(t, graceful.ExitReason().Requested)
}

func TestDockerProvider_outOfMemoryDuringInit(t *testing.T) {
	// Given: an application that allocates past its environment's memory
	h := newHarness(t)
	h.pkgs.put("oom.zip", zipOf(t, map[string]string{"index.js": `
const hold = [];
for (;;) { hold.push(Buffer.alloc(16 * 1024 * 1024, 1)); }`}))
	r := h.req("oom.zip")
	r.MemoryMB = 128

	// When: an environment is started
	env, fail := h.provider.StartEnvironment(t.Context(), r)

	// Then: the failure says so
	require.Nil(t, env)
	require.NotNil(t, fail)
	assert.Equal(t, "Runtime.OutOfMemory", fail.Type, "detail: %s", fail.Detail)
}

func TestDockerProvider_exitAfterReadyClosesDone(t *testing.T) {
	// Given: a ready environment whose process dies on its first request
	h := newHarness(t)
	h.pkgs.put("crash.zip", zipOf(t, map[string]string{"index.js": `
const http = require('node:http');
const [host, port] = process.env.AWS_LAMBDA_HTTP_ENDPOINT.split(':');
http.createServer(() => process.exit(3)).listen(Number(port), host);`}))
	env := h.start(t, h.req("crash.zip"))

	// When: the request kills it
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://"+env.Addr()+"/", nil)
	if resp, err := http.DefaultClient.Do(req); err == nil {
		resp.Body.Close()
	}

	// Then: Done closes and the exit is reported, not requested
	select {
	case <-env.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("Done never closed")
	}
	assert.Equal(t, 3, env.ExitReason().Code)
	assert.False(t, env.ExitReason().Requested)
	assert.False(t, env.ExitReason().OOM)
}

func TestDockerProvider_stopIsIdempotentAndConcurrent(t *testing.T) {
	// Given: a running environment
	h := newHarness(t)
	h.pkgs.put("stop.zip", zipOf(t, map[string]string{"index.js": serverCJS}))
	env := h.start(t, h.req("stop.zip"))

	// When: it is stopped from several goroutines, twice over
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- env.Stop(t.Context(), time.Second)
		}()
	}
	wg.Wait()
	errs <- env.Stop(t.Context(), time.Second)
	close(errs)

	// Then: every call succeeds and the container is gone
	for err := range errs {
		assert.NoError(t, err)
	}
	h.eventuallyRemoved(t, env.ID())
}

func TestDockerProvider_providerStopSweepsEnvironments(t *testing.T) {
	// Given: two running environments
	h := newHarness(t)
	h.pkgs.put("drain.zip", zipOf(t, map[string]string{"index.js": serverCJS}))
	a := h.start(t, h.req("drain.zip"))
	b := h.start(t, h.req("drain.zip"))

	// When: the provider is stopped, as at shutdown
	h.provider.Stop(t.Context())

	// Then: both are gone
	for _, env := range []lambdaweb.Environment{a, b} {
		select {
		case <-env.Done():
		default:
			t.Fatalf("environment %s still running", env.ID())
		}
		h.eventuallyRemoved(t, env.ID())
	}
}
