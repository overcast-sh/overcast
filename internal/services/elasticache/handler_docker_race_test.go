package elasticache

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/overcast-sh/overcast/internal/clock"
	"github.com/overcast-sh/overcast/internal/config"
	"github.com/overcast-sh/overcast/internal/docker"
	"github.com/overcast-sh/overcast/internal/middleware"
	"github.com/overcast-sh/overcast/internal/state"
)

func testCtx() context.Context {
	return middleware.ContextWithRegion(context.Background(), "us-east-1")
}

// fakeDockerDaemon speaks just enough of the Docker Engine API for the
// ElastiCache container-start path. The first container start request blocks
// until release() is called, so a test can land a Delete while the background
// start is still in flight — the exact interleaving the compat teardowns hit
// on every run, which is why eleven redis containers outlived their deleted
// replication groups. Mirrors the harness in rds/handler_docker_race_test.go.
type fakeDockerDaemon struct {
	srv         *httptest.Server
	containerID string
	started     chan struct{}
	release     func()

	mu         sync.Mutex
	stopped    bool
	removed    bool
	failCreate bool
	// connects records every network the daemon was asked to attach a
	// container to, with the aliases it was asked to advertise there — the
	// placement decision, as the daemon sees it.
	connects []networkConnect
}

// networkConnect is one POST /networks/{id}/connect as the fake daemon saw it.
type networkConnect struct {
	Network string
	Aliases []string
}

// connections returns a copy of every connect recorded so far.
func (fd *fakeDockerDaemon) connections() []networkConnect {
	fd.mu.Lock()
	defer fd.mu.Unlock()
	return append([]networkConnect(nil), fd.connects...)
}

func (fd *fakeDockerDaemon) stoppedOrRemoved() bool {
	fd.mu.Lock()
	defer fd.mu.Unlock()
	return fd.stopped || fd.removed
}

// refuseCreates makes building a container impossible, the way a daemon out of
// disk does. The start then fails outright rather than blocking on release(),
// which is the case container_start_failure_test.go is about. Mirrors
// refuseCreates on rds's lifecycleDaemon.
func (fd *fakeDockerDaemon) refuseCreates() {
	fd.mu.Lock()
	defer fd.mu.Unlock()
	fd.failCreate = true
}

func newFakeDockerDaemon(t *testing.T) *fakeDockerDaemon {
	t.Helper()
	const containerID = "cafecafecafe"
	started := make(chan struct{})
	releaseCh := make(chan struct{})
	var startMu sync.Mutex
	startCount := 0

	fd := &fakeDockerDaemon{containerID: containerID, started: started}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		switch {
		case strings.HasSuffix(p, "/images/create"), strings.HasSuffix(p, "/images/prune"):
			w.WriteHeader(http.StatusOK)

		// GetContainerByName lookup before create — no existing container.
		case strings.Contains(p, "/containers/overcast-elasticache") && strings.HasSuffix(p, "/json"):
			w.WriteHeader(http.StatusNotFound)

		case strings.HasSuffix(p, "/networks/create"):
			w.Write([]byte(`{"Id":"net-1"}`)) //nolint:errcheck

		case strings.HasSuffix(p, "/connect"):
			var body struct {
				EndpointConfig *struct {
					Aliases []string `json:"Aliases"`
				} `json:"EndpointConfig"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			nc := networkConnect{Network: strings.TrimSuffix(p[strings.LastIndex(p, "/networks/")+len("/networks/"):], "/connect")}
			if body.EndpointConfig != nil {
				nc.Aliases = body.EndpointConfig.Aliases
			}
			fd.mu.Lock()
			fd.connects = append(fd.connects, nc)
			fd.mu.Unlock()
			w.WriteHeader(http.StatusOK)

		case strings.HasSuffix(p, "/containers/create"):
			fd.mu.Lock()
			refuse := fd.failCreate
			fd.mu.Unlock()
			if refuse {
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(`{"message":"no space left on device"}`))
				return
			}
			w.Write([]byte(`{"Id":"` + containerID + `"}`)) //nolint:errcheck

		case strings.HasSuffix(p, "/containers/"+containerID+"/start"):
			startMu.Lock()
			startCount++
			first := startCount == 1
			startMu.Unlock()
			if first {
				close(started)
				<-releaseCh
			}
			w.WriteHeader(http.StatusNoContent)

		case strings.HasSuffix(p, "/containers/"+containerID+"/stop"):
			fd.mu.Lock()
			fd.stopped = true
			fd.mu.Unlock()
			w.WriteHeader(http.StatusNoContent)

		case r.Method == http.MethodDelete && strings.Contains(p, "/containers/"+containerID):
			fd.mu.Lock()
			fd.removed = true
			fd.mu.Unlock()
			w.WriteHeader(http.StatusNoContent)

		case strings.HasSuffix(p, "/containers/"+containerID+"/json"):
			w.Write([]byte(`{"Id":"` + containerID + `",` + //nolint:errcheck
				`"State":{"Status":"running","Running":true},` +
				`"NetworkSettings":{"Networks":{"overcast_elasticache":{"IPAddress":"127.0.0.1"}},"Ports":{}}}`))

		default:
			w.WriteHeader(http.StatusNoContent)
		}
	}))

	var once sync.Once
	fd.srv = srv
	fd.release = func() { once.Do(func() { close(releaseCh) }) }
	// Cleanups run LIFO: srv.Close must run after release, or Close hangs on
	// the blocked start request.
	t.Cleanup(srv.Close)
	t.Cleanup(fd.release)
	return fd
}

// newDockerTestHandler wires a Handler against the fake daemon. gc stays nil —
// the code paths under test fall back to direct docker calls.
func newDockerTestHandler(t *testing.T, fd *fakeDockerDaemon) *Handler {
	t.Helper()

	_, h := newDockerTestService(t, fd)
	return h
}

// newDockerTestService is newDockerTestHandler for a test that needs the
// Service as well — Stop lives there, not on Handler.
func newDockerTestService(t *testing.T, fd *fakeDockerDaemon) (*Service, *Handler) {
	t.Helper()
	cfg := &config.Config{Region: "us-east-1", AccountID: "123456789012"}
	s := New(cfg, state.NewMemoryStore(), zap.NewNop(), clock.New())
	h := s.handler
	h.docker = docker.NewClient("tcp://"+fd.srv.Listener.Addr().String(), zap.NewNop())
	h.puller = docker.NewImagePuller(h.docker)
	h.dockerReady.Store(true)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		h.scheduler.Stop(ctx)
		h.dockerWg.Wait()
	})
	return s, h
}

func waitStarted(t *testing.T, fd *fakeDockerDaemon) {
	t.Helper()
	select {
	case <-fd.started:
	case <-time.After(10 * time.Second):
		t.Fatal("container start request never reached the fake Docker daemon")
	}
}

// waitCondition polls until ok returns true or the deadline passes.
func waitCondition(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestDeleteCacheCluster_midStartDoesNotLeakContainer(t *testing.T) {
	// Given: a cache cluster whose redis container is still starting — the
	// container ID has not been persisted yet, so the delete path has nothing
	// to stop.
	fd := newFakeDockerDaemon(t)
	h := newDockerTestHandler(t, fd)
	ctx := testCtx()

	if _, aerr := h.createCacheClusterTyped(ctx, &ecCreateCacheClusterReq{
		CacheClusterId: "compat-leak", Engine: "redis", CacheNodeType: "cache.t3.micro", NumCacheNodes: 1,
	}); aerr != nil {
		t.Fatalf("create: %v", aerr)
	}
	waitStarted(t, fd)

	// When: the cluster is deleted while the start is in flight, and the
	// start then completes.
	if _, aerr := h.deleteCacheClusterTyped(ctx, &ecDeleteCacheClusterReq{CacheClusterId: "compat-leak"}); aerr != nil {
		t.Fatalf("delete: %v", aerr)
	}
	fd.release()

	// Then: the start goroutine tears its own container down. Anything else
	// leaves a running redis container owned by a deleted resource — the
	// compat teardowns hit this window on every run.
	waitCondition(t, "container stop/remove after mid-start delete", fd.stoppedOrRemoved)
}

func TestDeleteReplicationGroup_midStartDoesNotLeakContainer(t *testing.T) {
	// Given: a replication group whose container is still starting
	fd := newFakeDockerDaemon(t)
	h := newDockerTestHandler(t, fd)
	ctx := testCtx()

	if _, aerr := h.createReplicationGroupTyped(ctx, &ecCreateReplicationGroupReq{
		ReplicationGroupId: "compat-rg-leak", ReplicationGroupDescription: "leak test", Engine: "redis",
	}); aerr != nil {
		t.Fatalf("create: %v", aerr)
	}
	waitStarted(t, fd)

	// When: it is deleted mid-start
	if _, aerr := h.deleteReplicationGroupTyped(ctx, &ecDeleteReplicationGroupReq{ReplicationGroupId: "compat-rg-leak"}); aerr != nil {
		t.Fatalf("delete: %v", aerr)
	}
	fd.release()

	// Then: the container is stopped or removed, not left running
	waitCondition(t, "container stop/remove after mid-start delete", fd.stoppedOrRemoved)
}
