package athena

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/overcast-sh/overcast/internal/clock"
	"github.com/overcast-sh/overcast/internal/config"
	"github.com/overcast-sh/overcast/internal/docker"
	"github.com/overcast-sh/overcast/internal/serviceutil"
	"github.com/overcast-sh/overcast/internal/state"
)

// engine_manager_test.go — the engine's container lifecycle against a Docker
// daemon stand-in (the harness msk/docker_fake_test.go describes) whose
// "engine" is a fake Trino.

type fakeEngineDaemon struct {
	srv   *httptest.Server
	trino *fakeTrino

	mu       sync.Mutex
	created  []map[string]any
	archives map[string][]byte
	removed  []string
	exited   bool // the next container exits instead of answering
	seq      int
}

func newFakeEngineDaemon(t *testing.T) *fakeEngineDaemon {
	t.Helper()
	d := &fakeEngineDaemon{trino: newFakeTrino(t), archives: map[string][]byte{}}
	d.srv = httptest.NewServer(http.HandlerFunc(d.serve))
	t.Cleanup(d.srv.Close)
	return d
}

func (d *fakeEngineDaemon) serve(w http.ResponseWriter, r *http.Request) {
	d.mu.Lock()
	defer d.mu.Unlock()
	p := r.URL.Path
	id := containerIDOf(p)
	switch {
	case strings.HasSuffix(p, "/images/create") || strings.Contains(p, "/images/"):
		w.WriteHeader(http.StatusOK)
	case strings.HasSuffix(p, "/containers/create"):
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		body["name"] = r.URL.Query().Get("name")
		d.created = append(d.created, body)
		d.seq++
		_, _ = fmt.Fprintf(w, `{"Id":"engine%d"}`, d.seq)
	case strings.HasSuffix(p, "/archive") && r.Method == http.MethodPut:
		d.archives[id], _ = io.ReadAll(r.Body)
	case strings.HasSuffix(p, "/start"), strings.HasSuffix(p, "/stop"):
		w.WriteHeader(http.StatusNoContent)
	case strings.HasSuffix(p, "/logs"):
		_, _ = w.Write([]byte("Configuration is invalid\n"))
	case strings.HasSuffix(p, "/containers/json"): // the GC's sweep lists nothing
		_, _ = w.Write([]byte(`[]`))
	case strings.HasSuffix(p, "/json"):
		d.inspect(w, id)
	case r.Method == http.MethodDelete:
		d.removed = append(d.removed, id)
	default:
		w.WriteHeader(http.StatusOK)
	}
}

// inspect answers a container inspect: running and publishing the fake
// Trino's port, or exited.
func (d *fakeEngineDaemon) inspect(w http.ResponseWriter, id string) {
	u, _ := url.Parse(d.trino.srv.URL)
	state := `{"Status":"running","Running":true}`
	if d.exited {
		state = `{"Status":"exited","Running":false,"ExitCode":100}`
	}
	_, _ = fmt.Fprintf(w, `{"Id":%q,"State":%s,"NetworkSettings":{"Networks":{},"Ports":{"8080/tcp":[{"HostIp":"127.0.0.1","HostPort":%q}]}}}`,
		id, state, u.Port())
}

func containerIDOf(p string) string {
	_, rest, found := strings.Cut(p, "/containers/")
	if !found {
		return ""
	}
	id, _, _ := strings.Cut(rest, "/")
	return id
}

func (d *fakeEngineDaemon) snapshot() (created []map[string]any, removed []string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]map[string]any(nil), d.created...), append([]string(nil), d.removed...)
}

// newTestEngine is an engine manager on the fake daemon, its gateway
// already open so no reachability probe runs.
func newTestEngine(t *testing.T, clk clock.Clock) (*engineManager, *fakeEngineDaemon) {
	t.Helper()
	d := newFakeEngineDaemon(t)
	cfg := &config.Config{Region: "us-east-1", AccountID: "123456789012", AthenaEngineImage: "trino:test",
		AthenaEngineMemory: config.DefaultAthenaEngineMemory, Network: "overcast"}
	m := newEngineManager(cfg, serviceutil.NewServiceLogger(zap.NewNop(), serviceName), clk,
		serviceutil.NewInstanceDomain(state.NewMemoryStore(), nsInstance))
	m.gateway.endpoint = "http://gateway.test:1"
	m.setDocker(docker.NewClient("tcp://"+d.srv.Listener.Addr().String(), zap.NewNop()))
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		m.stop(ctx)
	})
	return m, d
}

func TestEngineManager_startsOneEngineForConcurrentQueries(t *testing.T) {
	// Given: an engine that is not running
	m, d := newTestEngine(t, clock.New())

	// When: several queries need it at once
	var wg sync.WaitGroup
	endpoints := make([]string, 4)
	for i := range endpoints {
		wg.Add(1)
		go func() {
			defer wg.Done()
			endpoint, release, err := m.acquire(context.Background())
			if err != nil {
				t.Errorf("acquire: %v", err)
				return
			}
			defer release()
			endpoints[i] = endpoint
		}()
	}
	wg.Wait()

	// Then: one container was started, and every query got its endpoint
	created, _ := d.snapshot()
	if len(created) != 1 {
		t.Fatalf("created %d containers, want 1", len(created))
	}
	for _, e := range endpoints {
		if e != d.trino.srv.URL {
			t.Fatalf("endpoints = %v, want %s", endpoints, d.trino.srv.URL)
		}
	}
	if st := m.snapshot(); st.State != engineReady || st.ContainerID != "engine1" || st.Engine != "trino" {
		t.Fatalf("status = %+v", st)
	}

	// And: the container is the tuned engine, bound to loopback, labelled
	// for the sweep, and configured with the rendered files
	req := created[0]
	host := req["HostConfig"].(map[string]any)
	binding := host["PortBindings"].(map[string]any)["8080/tcp"].([]any)[0].(map[string]any)
	labels := req["Labels"].(map[string]any)
	if req["Image"] != "trino:test" || host["Memory"] != float64(config.DefaultAthenaEngineMemory) || binding["HostIp"] != "127.0.0.1" ||
		labels[docker.LabelService] != serviceName || labels[docker.LabelInstance] == nil {
		t.Fatalf("create request = %+v", req)
	}
	files := untar(t, d.archives["engine1"])
	if !strings.Contains(files["etc/athena/jvm.config"], "-Xmx512m") ||
		!strings.Contains(files["etc/athena/catalog/awsdatacatalog.properties"], "hive.metastore.glue.endpoint-url=http://gateway.test:1") {
		t.Fatalf("configuration = %v", files)
	}
}

func TestEngineManager_failedStartIsReportedAndRetried(t *testing.T) {
	// Given: an engine whose container exits as it starts
	m, d := newTestEngine(t, clock.New())
	d.exited, d.trino.starting = true, true

	// When: a query needs it
	_, _, err := m.acquire(context.Background())

	// Then: the query learns why, with the container's last words, and the
	// failed container is removed
	if err == nil || !strings.Contains(err.Error(), "exited") || !strings.Contains(err.Error(), "Configuration is invalid") {
		t.Fatalf("err = %v", err)
	}
	if st := m.snapshot(); st.State != engineFailed || st.LastError == "" {
		t.Fatalf("status = %+v", st)
	}
	waitFor(t, func() bool { _, removed := d.snapshot(); return len(removed) == 1 })

	// When: the next query comes, with the daemon healthy again
	d.mu.Lock()
	d.exited = false
	d.mu.Unlock()
	d.trino.mu.Lock()
	d.trino.starting = false
	d.trino.mu.Unlock()
	_, release, err := m.acquire(context.Background())

	// Then: it starts a new engine
	if err != nil {
		t.Fatalf("second acquire: %v", err)
	}
	release()
	if created, _ := d.snapshot(); len(created) != 2 {
		t.Fatalf("created %d containers, want a second start", len(created))
	}
}

func TestEngineManager_stopsWhenIdleAndRestartsOnDemand(t *testing.T) {
	// Given: a running engine nothing holds
	clk := clock.NewMock()
	m, d := newTestEngine(t, clk)
	_, release, err := m.acquire(context.Background())
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	release()

	// When: it has been idle for the idle timeout
	clk.Add(engineIdleTimeout)

	// Then: it is stopped and removed
	waitFor(t, func() bool { _, removed := d.snapshot(); return len(removed) == 1 && removed[0] == "engine1" })
	if st := m.snapshot(); st.State != engineStopped {
		t.Fatalf("status = %+v, want stopped", st)
	}

	// When: another query needs it
	_, release, err = m.acquire(context.Background())
	if err != nil {
		t.Fatalf("acquire after idle stop: %v", err)
	}
	release()

	// Then: a new engine is started
	if created, _ := d.snapshot(); len(created) != 2 {
		t.Fatalf("created %d containers, want 2", len(created))
	}
}

func TestEngineManager_unavailableWithoutDocker(t *testing.T) {
	m := newEngineManager(&config.Config{}, serviceutil.NewServiceLogger(zap.NewNop(), serviceName), clock.New(), nil)
	if m.available() {
		t.Fatal("available with no Docker")
	}
	if _, _, err := m.acquire(context.Background()); err != errEngineUnavailable {
		t.Fatalf("acquire = %v, want errEngineUnavailable", err)
	}
	if st := m.snapshot(); st.State != engineOff || st.Reason == "" {
		t.Fatalf("status = %+v", st)
	}
}

// untar reads an archive of regular files into name → content, and fails
// the test on any other kind of entry.
func untar(t *testing.T, archive []byte) map[string]string {
	t.Helper()
	out := map[string]string{}
	tr := tar.NewReader(bytes.NewReader(archive))
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return out
		}
		if err != nil {
			t.Fatalf("archive: %v", err)
		}
		if hdr.Typeflag != tar.TypeReg {
			t.Fatalf("archive: %q is not a regular file (type %c)", hdr.Name, hdr.Typeflag)
		}
		body, _ := io.ReadAll(tr)
		out[hdr.Name] = string(body)
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition never held")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestEngineManager_queriesWaitForTheDockerProbe(t *testing.T) {
	// Given: an engine whose Docker probe has not finished
	cfg := &config.Config{AthenaDockerSocket: "unix:///probe.sock"}
	m := newEngineManager(cfg, serviceutil.NewServiceLogger(zap.NewNop(), serviceName), clock.New(), nil)

	// Then: queries are routed to it, and the status says it is probing
	if !m.available() || m.snapshot().State != engineProbing {
		t.Fatalf("available = %v, status = %+v", m.available(), m.snapshot())
	}

	// When: a query needs it, and the probe then fails
	got := make(chan error, 1)
	go func() {
		_, _, err := m.acquire(context.Background())
		got <- err
	}()
	select {
	case err := <-got:
		t.Fatalf("acquire returned %v before the probe finished", err)
	case <-time.After(50 * time.Millisecond):
	}
	m.dockerUnavailable()

	// Then: the waiting query learns there is no engine, and later ones
	// run inert
	if err := <-got; err != errEngineUnavailable {
		t.Fatalf("acquire = %v, want errEngineUnavailable", err)
	}
	if m.available() || m.snapshot().State != engineOff {
		t.Fatalf("after the probe failed: available = %v, status = %+v", m.available(), m.snapshot())
	}
}

func TestEngineManager_idleStopArmsWhenAnAbandonedBootFinishes(t *testing.T) {
	// Given: an engine that is slow to start, and a query that gives up
	// waiting for it
	clk := clock.NewMock()
	m, d := newTestEngine(t, clk)
	d.trino.mu.Lock()
	d.trino.starting = true
	d.trino.mu.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	gaveUp := make(chan error, 1)
	go func() {
		_, _, err := m.acquire(ctx)
		gaveUp <- err
	}()
	waitFor(t, func() bool { return m.snapshot().State == engineStarting })
	cancel()
	if err := <-gaveUp; err != context.Canceled {
		t.Fatalf("acquire = %v, want context.Canceled", err)
	}

	// When: the engine finishes starting with nothing holding it, and then
	// sits idle for the idle timeout
	d.trino.mu.Lock()
	d.trino.starting = false
	d.trino.mu.Unlock()
	waitFor(t, func() bool {
		clk.Add(engineReadyInterval)
		return m.snapshot().State == engineReady
	})
	clk.Add(engineIdleTimeout)

	// Then: it is stopped and removed
	waitFor(t, func() bool { _, removed := d.snapshot(); return len(removed) == 1 })
}
