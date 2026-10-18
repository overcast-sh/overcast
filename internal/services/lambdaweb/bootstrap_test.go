package lambdaweb

// bootstrap_test.go — bootstrap.mjs run for real under a local node (skipped
// when there is none) against small fixture apps, the way the container runs
// it: node bootstrap.mjs with the app in LAMBDA_TASK_ROOT.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const fixtureServerCJS = `
const http = require('node:http');
const [host, port] = process.env.AWS_LAMBDA_HTTP_ENDPOINT.split(':');
http.createServer(async (req, res) => {
  if (req.url === '/log') {
    console.log('plain');
    console.log({ answer: 42, nested: { ok: true } });
    console.log('fmt %s %d', 'x', 7);
    console.info('info');
    console.debug('debug');
    console.trace('traced');
    console.warn('warn');
    console.error('error');
    console.error(new Error('with stack'));
    process.stdout.write('raw out\n');
    process.stderr.write('raw err\n');
    await new Promise((r) => setTimeout(r, 5));
    console.log('after await');
    return res.end('ok');
  }
  console.log('served /');
  res.end('hello');
}).listen(Number(port), host);
console.log('init log, no request');
`

const fixtureServerESM = `
import http from 'node:http';
const [host, port] = process.env.AWS_LAMBDA_HTTP_ENDPOINT.split(':');
http.createServer((req, res) => res.end('esm')).listen(Number(port), host);
`

type nodeApp struct {
	cmd    *exec.Cmd
	addr   string
	out    *syncBuffer
	exited chan struct{}
	code   int
}

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) lines() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	sc := bufio.NewScanner(bytes.NewReader(s.b.Bytes()))
	for sc.Scan() {
		out = append(out, sc.Text())
	}
	return out
}

// runBootstrap starts node on the embedded bootstrap with the given app files.
func runBootstrap(t *testing.T, files map[string]string, env map[string]string) *nodeApp {
	t.Helper()
	name := "node"
	if runtime.GOOS == "windows" {
		name = "node.exe" // a .cmd shim would leave the real process behind when killed
	}
	node, err := exec.LookPath(name)
	if err != nil {
		t.Skip("node is not installed")
	}
	root := t.TempDir()
	task := filepath.Join(root, "task")
	require.NoError(t, os.MkdirAll(task, 0o755))
	for name, content := range files {
		require.NoError(t, os.WriteFile(filepath.Join(task, name), []byte(content), 0o644))
	}
	bootstrap := filepath.Join(root, "bootstrap", "bootstrap.mjs")
	require.NoError(t, os.MkdirAll(filepath.Dir(bootstrap), 0o755))
	require.NoError(t, os.WriteFile(bootstrap, bootstrapSource, 0o644))

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := ln.Addr().String()
	require.NoError(t, ln.Close())

	cmd := exec.Command(node, bootstrap)
	cmd.Dir = task
	cmd.Env = append(os.Environ(),
		"LAMBDA_TASK_ROOT="+task,
		"AWS_LAMBDA_HTTP_ENDPOINT="+addr,
		"AWS_REGION=us-east-1",
		"AWS_LAMBDA_NODEJS_ENTRYPOINT=",
	)
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	cmd.WaitDelay = 2 * time.Second
	app := &nodeApp{cmd: cmd, addr: addr, out: &syncBuffer{}, exited: make(chan struct{})}
	cmd.Stdout, cmd.Stderr = app.out, app.out
	require.NoError(t, cmd.Start())
	go func() {
		err := cmd.Wait()
		app.code = cmd.ProcessState.ExitCode()
		_ = err
		close(app.exited)
	}()
	t.Cleanup(func() { _ = cmd.Process.Kill(); <-app.exited })
	return app
}

func (a *nodeApp) waitListening(t *testing.T) {
	t.Helper()
	require.Eventually(t, func() bool {
		c, err := net.DialTimeout("tcp", a.addr, 100*time.Millisecond)
		if err != nil {
			return false
		}
		_ = c.Close()
		return true
	}, 10*time.Second, 20*time.Millisecond, "app never listened; output: %v", a.out.lines())
}

func (a *nodeApp) waitExit(t *testing.T) int {
	t.Helper()
	select {
	case <-a.exited:
		return a.code
	case <-time.After(10 * time.Second):
		t.Fatalf("process did not exit; output: %v", a.out.lines())
		return -1
	}
}

func (a *nodeApp) get(t *testing.T, path string, header map[string]string) string {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://"+a.addr+path, nil)
	require.NoError(t, err)
	for k, v := range header {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return string(b)
}

// logRecords are the output lines that are JSON application logs.
func (a *nodeApp) logRecords(t *testing.T) []map[string]any {
	t.Helper()
	var recs []map[string]any
	for _, l := range a.out.lines() {
		var m map[string]any
		if json.Unmarshal([]byte(l), &m) == nil {
			recs = append(recs, m)
		}
	}
	return recs
}

func findLog(recs []map[string]any, message string) map[string]any {
	for _, r := range recs {
		if fmt.Sprint(r["message"]) == message {
			return r
		}
	}
	return nil
}

func TestBootstrap_cjsServer(t *testing.T) {
	// Given: a CommonJS app at the default entry file
	app := runBootstrap(t, map[string]string{"index.js": fixtureServerCJS}, nil)

	// When: it is asked
	app.waitListening(t)

	// Then: it answers
	assert.Equal(t, "hello", app.get(t, "/", nil))
}

func TestBootstrap_esmServerWithNamedEntrypoint(t *testing.T) {
	// Given: an ES module named by AWS_LAMBDA_NODEJS_ENTRYPOINT
	app := runBootstrap(t, map[string]string{"main.mjs": fixtureServerESM},
		map[string]string{"AWS_LAMBDA_NODEJS_ENTRYPOINT": "main.mjs"})

	// When: it is asked
	app.waitListening(t)

	// Then: it answers
	assert.Equal(t, "esm", app.get(t, "/", nil))
}

func TestBootstrap_failedImports(t *testing.T) {
	cases := []struct {
		name   string
		files  map[string]string
		env    map[string]string
		code   int
		marker string
	}{
		{"missing entry file", map[string]string{"other.js": ""}, nil, exitCodeImportError, "OVERCAST_WEB_IMPORT_ERROR"},
		{"missing named entry", map[string]string{"index.js": ""}, map[string]string{"AWS_LAMBDA_NODEJS_ENTRYPOINT": "nope.mjs"}, exitCodeImportError, "OVERCAST_WEB_IMPORT_ERROR"},
		{"missing dependency", map[string]string{"index.js": "require('./gone')"}, nil, exitCodeImportError, "OVERCAST_WEB_IMPORT_ERROR"},
		{"syntax error", map[string]string{"index.js": "const = 1;"}, nil, exitCodeImportError, "OVERCAST_WEB_IMPORT_ERROR"},
		{"throws on init", map[string]string{"index.js": "throw new TypeError('init failed');"}, nil, exitCodeInitError, "OVERCAST_WEB_INIT_ERROR"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Given: an app that cannot start
			app := runBootstrap(t, tc.files, tc.env)

			// When: the process ends
			code := app.waitExit(t)

			// Then: it exits with the distinctive code after a one-line marker
			assert.Equal(t, tc.code, code)
			var markers []string
			for _, l := range app.out.lines() {
				if strings.HasPrefix(l, tc.marker) {
					markers = append(markers, l)
				}
			}
			assert.Len(t, markers, 1, "output: %v", app.out.lines())
			// and the provider classifies it from the exit code and the tail alike
			f := classifyExit(code, false, strings.Join(app.out.lines(), "\n"))
			if tc.code == exitCodeImportError {
				assert.Equal(t, errTypeImportCode, f.Type)
			} else {
				assert.Equal(t, errTypeInitCode, f.Type)
			}
		})
	}
}

func TestBootstrap_neverListensKeepsRunning(t *testing.T) {
	// Given: an app that starts and does not listen
	app := runBootstrap(t, map[string]string{"index.js": "setInterval(() => {}, 1000);"}, nil)

	// When: we wait a moment
	select {
	case <-app.exited:
		t.Fatalf("exited: %v", app.out.lines())
	case <-time.After(700 * time.Millisecond):
	}

	// Then: it is alive and nothing listens (the provider times it out)
	_, err := net.DialTimeout("tcp", app.addr, 100*time.Millisecond)
	assert.Error(t, err)
}

func TestBootstrap_consoleBecomesJSONWithRequestIdentity(t *testing.T) {
	// Given: an app logging every way, at threshold DEBUG
	app := runBootstrap(t, map[string]string{"index.js": fixtureServerCJS},
		map[string]string{"OVERCAST_WEB_APP_LOG_LEVEL": "DEBUG"})
	app.waitListening(t)

	// When: a request arrives with the platform's identity headers
	app.get(t, "/log", map[string]string{"lambda-web-request-id": "req-1", "lambda-web-endpoint-name": "my-ep"})
	app.get(t, "/", map[string]string{"lambda-web-request-id": "req-2"})
	// the output pipe is read on another goroutine: wait for the last line
	require.Eventually(t, func() bool { return findLog(app.logRecords(t), "served /") != nil },
		5*time.Second, 10*time.Millisecond, "output: %v", app.out.lines())
	recs := app.logRecords(t)

	// Then: every call is one JSON line in the application-log shape
	plain := findLog(recs, "plain")
	require.NotNil(t, plain, "output: %v", app.out.lines())
	assert.Equal(t, "INFO", plain["level"])
	assert.Equal(t, "req-1", plain["requestId"])
	assert.Equal(t, "my-ep", plain["endpointName"])
	assert.Equal(t, "us-east-1", plain["region"])
	ts, err := time.Parse(time.RFC3339Nano, plain["timestamp"].(string))
	require.NoError(t, err)
	assert.WithinDuration(t, time.Now(), ts, time.Minute)
	assert.True(t, strings.HasPrefix(app.out.lines()[0], `{"timestamp":"`), "events start with the timestamp, which the CloudWatch shipper reads")

	// objects stay objects, formats are applied, levels map
	assert.Equal(t, map[string]any{"answer": float64(42), "nested": map[string]any{"ok": true}},
		recs[indexOfObject(recs)]["message"])
	assert.NotNil(t, findLog(recs, "fmt x 7"))
	assert.Equal(t, "INFO", findLog(recs, "info")["level"])
	assert.Equal(t, "DEBUG", findLog(recs, "debug")["level"])
	assert.Equal(t, "WARN", findLog(recs, "warn")["level"])
	assert.Equal(t, "ERROR", findLog(recs, "error")["level"])
	// the request id survives an await
	assert.Equal(t, "req-1", findLog(recs, "after await")["requestId"])
	// raw writes are wrapped by stream
	assert.Equal(t, "INFO", findLog(recs, "raw out")["level"])
	assert.Equal(t, "ERROR", findLog(recs, "raw err")["level"])
	// a thrown error is logged with its stack
	var stack map[string]any
	for _, r := range recs {
		if strings.Contains(fmt.Sprint(r["message"]), "with stack") {
			stack = r
		}
	}
	require.NotNil(t, stack)
	assert.Contains(t, fmt.Sprint(stack["message"]), "    at ")
	// logging outside a request carries no request id
	initLog := findLog(recs, "init log, no request")
	require.NotNil(t, initLog)
	assert.NotContains(t, initLog, "requestId")
	// and the next request is its own
	served := findLog(recs, "served /")
	require.NotNil(t, served)
	assert.Equal(t, "req-2", served["requestId"])
	assert.NotContains(t, served, "endpointName")
}

func indexOfObject(recs []map[string]any) int {
	for i, r := range recs {
		if _, ok := r["message"].(map[string]any); ok {
			return i
		}
	}
	return 0
}

func TestBootstrap_logLevelFilters(t *testing.T) {
	cases := []struct {
		level   string
		present []string
		absent  []string
	}{
		{"INFO", []string{"plain", "info", "warn", "error", "raw out", "raw err"}, []string{"debug"}},
		{"WARN", []string{"warn", "error", "raw err"}, []string{"plain", "info", "debug", "raw out"}},
		{"ERROR", []string{"error", "raw err"}, []string{"warn", "info", "raw out"}},
		{"FATAL", []string{"error"}, []string{"warn", "info"}}, // FATAL acts as ERROR
		{"TRACE", []string{"plain", "debug", "warn", "traced"}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.level, func(t *testing.T) {
			// Given: an app logging at every level, with a threshold
			app := runBootstrap(t, map[string]string{"index.js": fixtureServerCJS},
				map[string]string{"OVERCAST_WEB_APP_LOG_LEVEL": tc.level})
			app.waitListening(t)

			// When: a request logs
			app.get(t, "/log", map[string]string{"lambda-web-request-id": "r"})
			recs := app.logRecords(t)

			// Then: only lines at or above the threshold are written
			for _, m := range tc.present {
				if m == "traced" {
					found := false
					for _, r := range recs {
						found = found || (r["level"] == "TRACE" && strings.HasPrefix(fmt.Sprint(r["message"]), "traced"))
					}
					assert.True(t, found, "console.trace should be logged at TRACE")
					continue
				}
				assert.NotNil(t, findLog(recs, m), "%s should be logged at %s", m, tc.level)
			}
			for _, m := range tc.absent {
				assert.Nil(t, findLog(recs, m), "%s should be filtered at %s", m, tc.level)
			}
		})
	}
}

func TestBootstrap_sigtermRunsTheApplicationsHandler(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("a signal cannot be sent to a process on Windows; the Docker integration tests cover SIGTERM")
	}
	// Given: an app with its own SIGTERM handler
	app := runBootstrap(t, map[string]string{"index.js": fixtureServerCJS + `
process.on('SIGTERM', () => { console.log('app handler ran'); process.exit(5); });`}, nil)
	app.waitListening(t)

	// When: it is sent SIGTERM
	require.NoError(t, app.cmd.Process.Signal(syscall.SIGTERM))

	// Then: its handler decides the exit
	assert.Equal(t, 5, app.waitExit(t))
	assert.NotNil(t, findLog(app.logRecords(t), "app handler ran"))
}

func TestBootstrap_sigtermWithoutHandlerExitsZero(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("a signal cannot be sent to a process on Windows; the Docker integration tests cover SIGTERM")
	}
	// Given: an app with no handler
	app := runBootstrap(t, map[string]string{"index.js": fixtureServerCJS}, nil)
	app.waitListening(t)

	// When: it is sent SIGTERM
	require.NoError(t, app.cmd.Process.Signal(syscall.SIGTERM))

	// Then: it exits 0
	assert.Equal(t, 0, app.waitExit(t))
}
