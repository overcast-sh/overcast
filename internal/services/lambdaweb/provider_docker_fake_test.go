package lambdaweb

// provider_docker_fake_test.go — a fake Docker Engine API for the provider's
// unit tests: an httptest.Server speaking /v1.45/..., wired to the provider
// through docker.NewClient("tcp://"+addr). It records what the provider asks
// for and plays the container: wait blocks until the test ends the process.

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/overcast-sh/overcast/internal/clock"
	"github.com/overcast-sh/overcast/internal/config"
	"github.com/overcast-sh/overcast/internal/docker"
	"github.com/overcast-sh/overcast/internal/events"
	"github.com/overcast-sh/overcast/internal/protocol"
	"github.com/overcast-sh/overcast/internal/state"
)

type fakeDaemon struct {
	*httptest.Server

	// port is the published port inspect reports; zero means none.
	port int
	// logs is the container's output (what ContainerLogs returns).
	logs string
	// streamLines are served to a following log reader.
	streamLines string
	// imageMissingOnce makes the first create answer "No such image".
	imageMissingOnce bool

	mu       sync.Mutex
	requests []string // "METHOD path"
	creates  []docker.CreateContainerRequest
	names    []string
	archives [][]byte
	pulls    int
	exited   map[string]*fakeExit
	deleted  []string
	stops    []string
	oom      bool
	exitCode int
	exitedAt bool // inspect reports the container not running
}

type fakeExit struct {
	once sync.Once
	ch   chan struct{}
}

func newFakeDaemon(t *testing.T) *fakeDaemon {
	t.Helper()
	d := &fakeDaemon{exited: map[string]*fakeExit{}}
	d.Server = httptest.NewServer(http.HandlerFunc(d.serve(t)))
	t.Cleanup(d.Close)
	return d
}

func (d *fakeDaemon) exit(id string) *fakeExit {
	d.mu.Lock()
	defer d.mu.Unlock()
	e, ok := d.exited[id]
	if !ok {
		e = &fakeExit{ch: make(chan struct{})}
		d.exited[id] = e
	}
	return e
}

// endProcess is the container's process exiting.
func (d *fakeDaemon) endProcess(id string, code int, oom bool) {
	d.mu.Lock()
	d.exitCode, d.oom, d.exitedAt = code, oom, true
	d.mu.Unlock()
	e := d.exit(id)
	e.once.Do(func() { close(e.ch) })
}

func (d *fakeDaemon) serve(t *testing.T) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/v1.45")
		d.mu.Lock()
		d.requests = append(d.requests, r.Method+" "+path)
		d.mu.Unlock()
		jsonOut := func(v any) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(v)
		}
		switch {
		case r.Method == http.MethodGet && strings.HasPrefix(path, "/images/"):
			d.mu.Lock()
			pulled := d.pulls > 0 || !d.imageMissingOnce
			d.mu.Unlock()
			if !pulled {
				http.Error(w, "no such image", http.StatusNotFound)
				return
			}
			jsonOut(map[string]any{"Os": "linux", "Architecture": "amd64"})

		case r.Method == http.MethodPost && path == "/images/create":
			d.mu.Lock()
			d.pulls++
			d.mu.Unlock()
			_, _ = w.Write([]byte(`{"status":"done"}` + "\n"))

		case r.Method == http.MethodPost && path == "/containers/create":
			var req docker.CreateContainerRequest
			require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
			d.mu.Lock()
			if d.imageMissingOnce && len(d.creates) == 0 {
				d.creates = append(d.creates, req)
				d.imageMissingOnce = false
				d.pulls = 0 // the image was "removed": the next ensure pulls
				d.mu.Unlock()
				http.Error(w, `{"message":"No such image: x"}`, http.StatusNotFound)
				return
			}
			d.creates = append(d.creates, req)
			d.names = append(d.names, r.URL.Query().Get("name"))
			id := "ctr" + string(rune('a'+len(d.names)-1))
			d.mu.Unlock()
			w.WriteHeader(http.StatusCreated)
			jsonOut(map[string]any{"Id": id})

		case r.Method == http.MethodPut && strings.HasSuffix(path, "/archive"):
			b, _ := io.ReadAll(r.Body)
			d.mu.Lock()
			d.archives = append(d.archives, b)
			d.mu.Unlock()

		case r.Method == http.MethodPost && strings.Contains(path, "/networks/"):
			// connect

		case r.Method == http.MethodPost && strings.HasSuffix(path, "/start"):
			w.WriteHeader(http.StatusNoContent)

		case r.Method == http.MethodPost && strings.HasSuffix(path, "/wait"):
			id := strings.TrimSuffix(strings.TrimPrefix(path, "/containers/"), "/wait")
			select {
			case <-d.exit(id).ch:
			case <-r.Context().Done():
				return
			}
			d.mu.Lock()
			code := d.exitCode
			d.mu.Unlock()
			jsonOut(map[string]any{"StatusCode": code})

		case r.Method == http.MethodPost && strings.HasSuffix(path, "/stop"):
			id := strings.TrimSuffix(strings.TrimPrefix(path, "/containers/"), "/stop")
			d.mu.Lock()
			d.stops = append(d.stops, id+"?t="+r.URL.Query().Get("t"))
			d.mu.Unlock()
			d.endProcess(id, 0, false)
			w.WriteHeader(http.StatusNoContent)

		case r.Method == http.MethodDelete && strings.HasPrefix(path, "/containers/"):
			id := strings.TrimPrefix(path, "/containers/")
			d.mu.Lock()
			d.deleted = append(d.deleted, id)
			d.mu.Unlock()
			d.endProcess(id, 137, false)
			w.WriteHeader(http.StatusNoContent)

		case r.Method == http.MethodGet && path == "/containers/json":
			jsonOut([]any{})

		case r.Method == http.MethodGet && strings.HasSuffix(path, "/logs"):
			d.serveLogs(w, r)

		case r.Method == http.MethodGet && strings.HasSuffix(path, "/stats"):
			jsonOut(map[string]any{
				"memory_stats": map[string]any{"usage": 100 << 20, "stats": map[string]any{"inactive_file": 20 << 20}},
				"cpu_stats":    map[string]any{"cpu_usage": map[string]any{"total_usage": 1_500_000_000}, "online_cpus": 2},
			})

		case r.Method == http.MethodGet && strings.HasSuffix(path, "/json") && strings.HasPrefix(path, "/containers/"):
			d.mu.Lock()
			running, oom, code, port := !d.exitedAt, d.oom, d.exitCode, d.port
			d.mu.Unlock()
			ports := map[string]any{}
			if port != 0 && running {
				ports["8080/tcp"] = []map[string]string{{"HostIp": "127.0.0.1", "HostPort": itoa(port)}}
			}
			jsonOut(map[string]any{
				"Id":              strings.TrimSuffix(strings.TrimPrefix(path, "/containers/"), "/json"),
				"State":           map[string]any{"Running": running, "OOMKilled": oom, "ExitCode": code},
				"NetworkSettings": map[string]any{"Ports": ports},
			})

		default:
			jsonOut(map[string]any{})
		}
	}
}

func (d *fakeDaemon) serveLogs(w http.ResponseWriter, r *http.Request) {
	frame := func(s string) []byte {
		hdr := make([]byte, 8)
		hdr[0] = 1
		binary.BigEndian.PutUint32(hdr[4:], uint32(len(s)))
		return append(hdr, s...)
	}
	q := r.URL.Query()
	if q.Get("follow") == "true" {
		if d.streamLines != "" {
			_, _ = w.Write(frame(d.streamLines))
			w.(http.Flusher).Flush()
		}
		<-r.Context().Done()
		return
	}
	if q.Get("timestamps") == "true" {
		_, _ = w.Write(frame(d.streamLines))
		return
	}
	_, _ = w.Write(frame(d.logs))
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }

func (d *fakeDaemon) sawRequest(prefix string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, r := range d.requests {
		if strings.HasPrefix(r, prefix) {
			return true
		}
	}
	return false
}

func (d *fakeDaemon) count(prefix string) int {
	d.mu.Lock()
	defer d.mu.Unlock()
	n := 0
	for _, r := range d.requests {
		if strings.HasPrefix(r, prefix) {
			n++
		}
	}
	return n
}

// holdListener is the application: it accepts connections and says nothing, as
// an HTTP server does until it is sent a request. It returns its port.
func holdListener(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	var conns []net.Conn
	var mu sync.Mutex
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			conns = append(conns, c)
			mu.Unlock()
		}
	}()
	t.Cleanup(func() {
		_ = ln.Close()
		mu.Lock()
		for _, c := range conns {
			_ = c.Close()
		}
		mu.Unlock()
	})
	return ln.Addr().(*net.TCPAddr).Port
}

// closedPort is a port nothing listens on.
func closedPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := ln.Addr().(*net.TCPAddr).Port
	require.NoError(t, ln.Close())
	return port
}

// memoryLogs is a events.LogWriter that remembers what it was given.
type memoryLogs struct {
	mu      sync.Mutex
	groups  []string
	streams []string
	entries []events.LogEntry
}

func (m *memoryLogs) EnsureLogGroup(_ context.Context, g string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.groups = append(m.groups, g)
	return nil
}

func (m *memoryLogs) EnsureLogStream(_ context.Context, g, s string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.streams = append(m.streams, g+"|"+s)
	return nil
}

func (m *memoryLogs) WriteLogEvents(_ context.Context, _, _ string, e []events.LogEntry) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.entries = append(m.entries, e...)
	return nil
}

func (m *memoryLogs) snapshot() []events.LogEntry {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]events.LogEntry(nil), m.entries...)
}

// mapSource serves packages from memory and counts fetches.
type mapSource struct {
	mu      sync.Mutex
	pkgs    map[string][]byte
	fetches int
	err     *protocol.AWSError
}

func (s *mapSource) Resolve(_ context.Context, ref codeRef) (codeRef, int64, *protocol.AWSError) {
	return ref, 0, nil
}

func (s *mapSource) Fetch(_ context.Context, ref codeRef) ([]byte, *protocol.AWSError) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fetches++
	if s.err != nil {
		return nil, s.err
	}
	return s.pkgs[ref.Key], nil
}

func (s *mapSource) fetched() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.fetches
}

type providerFixture struct {
	daemon   *fakeDaemon
	provider *DockerProvider
	src      *mapSource
	logs     *memoryLogs
	cfg      *config.Config
}

func newProviderFixture(t *testing.T) *providerFixture {
	t.Helper()
	d := newFakeDaemon(t)
	d.port = holdListener(t)
	cfg := testConfig()
	p := NewDockerProvider(cfg, zap.NewNop(), clock.New())
	src := &mapSource{pkgs: map[string][]byte{"app.zip": zipBytes(t, map[string]string{"index.js": "// app", "lib/util.js": "x"})}}
	logs := &memoryLogs{}
	p.InitCode(src)
	p.InitStore(state.NewMemoryStore())
	p.SetDocker(docker.NewClient("tcp://"+strings.TrimPrefix(d.URL, "http://"), zap.NewNop()))
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		p.Stop(ctx)
	})
	return &providerFixture{daemon: d, provider: p, src: src, logs: logs, cfg: cfg}
}

func (f *providerFixture) spec() envSpec {
	rev := &revision{Function: "my-fn", Region: "us-east-1", ID: "rev1"}
	rev.BuildConfig.RuntimeConfig.Runtime = "nodejs24.x"
	return envSpec{
		Region: "us-east-1", Function: "my-fn", Endpoint: "ep", Revision: rev,
		Code:        codeRef{Bucket: "b", Key: "app.zip", VersionID: "v1"},
		Env:         map[string]string{"GREETING": "hi"},
		MemoryMB:    2048,
		InitTimeout: 2 * time.Second,
		LogGroup:    "/aws/lambda/web/my-fn", LogStream: "2026/01/01/[rev1]abc",
		AppLogLevel: "INFO", SysLogLevel: "INFO",
	}
}

func zipBytes(t *testing.T, files map[string]string) []byte {
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

// tarEntries reads an archive into name → content.
func tarEntries(t *testing.T, data []byte) map[string]string {
	t.Helper()
	out := map[string]string{}
	tr := tar.NewReader(bytes.NewReader(data))
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return out
		}
		require.NoError(t, err)
		b, _ := io.ReadAll(tr)
		out[h.Name] = string(b)
	}
}
