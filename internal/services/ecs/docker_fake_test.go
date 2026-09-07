package ecs

// docker_fake_test.go — a Docker daemon stand-in for the ECS tests whose
// subject only happens behind `dockerReady`.
//
// Since #686 a task is not transitioned to RUNNING unless Docker is wired, so
// tests about the transition itself — region scoping, ordering, deployment
// health — cannot reach their subject on a metadata-only handler. They were
// gated with docker.SkipWithoutDocker instead — a helper that skipped
// unconditionally, because it dialled an empty socket path — so they stopped
// running entirely. That helper has since been deleted; nothing needs a real
// daemon any more.
//
// A real daemon is the wrong dependency for them: none is about containers, and
// requiring one makes the tests unrunnable wherever there is no socket. Point a
// real docker.Client at an httptest server speaking enough of the Engine API
// instead — the same harness elasticache/handler_docker_race_test.go,
// rds/handler_docker_race_test.go and lambda/seed_reconcile_test.go use. It
// keeps the real client, the real HTTP encoding and the real request shapes,
// which a hand-written mock of a Docker interface would all stub out.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/overcast-sh/overcast/internal/clock"
	"github.com/overcast-sh/overcast/internal/config"
	"github.com/overcast-sh/overcast/internal/docker"
	"github.com/overcast-sh/overcast/internal/state"
)

// fakeECSDockerDaemon answers the container-start path RunTask drives. It
// records the containers it was asked to start so a test can assert that a
// task which reports RUNNING has something behind it.
type fakeECSDockerDaemon struct {
	srv *httptest.Server

	mu      sync.Mutex
	started []string
	created []createdContainer
	onStart func(string)
	onStop  func(string)

	// logs and removed model the one thing the retention paths turn on: a
	// container's output is readable right up to the moment it is removed, and
	// not one call afterwards. A daemon that always answers is no test of an
	// ordering bug.
	logs    map[string][]byte
	removed map[string]bool

	// refuseStart holds the container-definition names whose start the daemon
	// rejects, for the tests about a task that is only partly placed.
	refuseStart []string

	// hostPorts models the one thing a real daemon enforces that nothing else
	// here does: a published host port belongs to one live container at a
	// time. Nil until a test asks for it with enforceHostPorts, because most
	// of these place containers that publish nothing and would only pay for
	// the bookkeeping.
	hostPorts map[string]string // host port → the container ID holding it

	// imageWorkingDirs is the WORKDIR an image inspect reports, by image
	// reference, for the tests about a debugged container's remote root.
	imageWorkingDirs map[string]string
}

// setImageWorkingDir makes the daemon report dir as the image's WORKDIR.
func (fd *fakeECSDockerDaemon) setImageWorkingDir(image, dir string) {
	fd.mu.Lock()
	defer fd.mu.Unlock()
	if fd.imageWorkingDirs == nil {
		fd.imageWorkingDirs = map[string]string{}
	}
	fd.imageWorkingDirs[image] = dir
}

// imageWorkingDir is the WORKDIR set for the image an inspect path names, or
// "" for an image no test described.
func (fd *fakeECSDockerDaemon) imageWorkingDir(p string) string {
	const marker = "/images/"
	i := strings.Index(p, marker)
	if i < 0 {
		return ""
	}
	image, _ := url.PathUnescape(strings.TrimSuffix(p[i+len(marker):], "/json"))
	fd.mu.Lock()
	defer fd.mu.Unlock()
	return fd.imageWorkingDirs[image]
}

// publishedPorts is what a container's inspect reports under
// NetworkSettings.Ports: every binding its create asked for, with an
// ephemeral one (host port 0) answered with a port derived from the
// container's sequence number, so two containers never report the same one.
func (fd *fakeECSDockerDaemon) publishedPorts(containerID string) map[string][]docker.PortBinding {
	fd.mu.Lock()
	defer fd.mu.Unlock()
	ports := map[string][]docker.PortBinding{}
	for n, c := range fd.created {
		if c.id != containerID || c.req.HostConfig == nil {
			continue
		}
		for key, bindings := range c.req.HostConfig.PortBindings {
			for i, b := range bindings {
				if b.HostPort == "" || b.HostPort == "0" {
					b.HostPort = strconv.Itoa(40000 + 10*n + i)
				}
				ports[key] = append(ports[key], b)
			}
		}
	}
	return ports
}

// enforceHostPorts makes the daemon refuse to start a container whose
// HostConfig publishes a host port another live container already holds,
// exactly as Docker does. The port is released when the container is stopped
// or removed.
//
// This is what a rolling deployment of an awsvpc service runs into on a shared
// Docker host: two tasks of the same service, both publishing the same port,
// alive at once because the new one is started before the old one is retired.
func (fd *fakeECSDockerDaemon) enforceHostPorts() {
	fd.mu.Lock()
	defer fd.mu.Unlock()
	fd.hostPorts = map[string]string{}
}

// claimHostPorts records the host ports the container publishes, or names the
// one already taken. Callers hold fd.mu.
func (fd *fakeECSDockerDaemon) claimHostPorts(c createdContainer) string {
	if fd.hostPorts == nil || c.req.HostConfig == nil {
		return ""
	}
	for _, bindings := range c.req.HostConfig.PortBindings {
		for _, b := range bindings {
			if b.HostPort == "" || b.HostPort == "0" {
				continue // ephemeral: the daemon picks a free one
			}
			if holder, taken := fd.hostPorts[b.HostPort]; taken && holder != c.id {
				return b.HostPort
			}
		}
	}
	for _, bindings := range c.req.HostConfig.PortBindings {
		for _, b := range bindings {
			if b.HostPort != "" && b.HostPort != "0" {
				fd.hostPorts[b.HostPort] = c.id
			}
		}
	}
	return ""
}

// releaseHostPorts gives back everything the container was holding. Callers
// hold fd.mu.
func (fd *fakeECSDockerDaemon) releaseHostPorts(containerID string) {
	for port, holder := range fd.hostPorts {
		if holder == containerID {
			delete(fd.hostPorts, port)
		}
	}
}

// failStartOf makes the daemon refuse to start the container placed for the
// named container definition, the way it refuses one whose port is already
// taken.
func (fd *fakeECSDockerDaemon) failStartOf(containerName string) {
	fd.mu.Lock()
	defer fd.mu.Unlock()
	fd.refuseStart = append(fd.refuseStart, containerName)
}

// containerIDOf returns the ID the daemon minted for the named container
// definition — the first, should a task hold more than one whose name ends
// that way — or "" if nothing was created for it.
func (fd *fakeECSDockerDaemon) containerIDOf(containerName string) string {
	fd.mu.Lock()
	defer fd.mu.Unlock()
	for _, c := range fd.created {
		if c.placedAs(containerName) {
			return c.id
		}
	}
	return ""
}

// createdContainer is one container create the daemon answered, paired with the
// name it was asked for and the ID it was given — which is how a test tells one
// created container from another, a task's namespace container from the
// application containers that join it most of all.
type createdContainer struct {
	name string
	id   string
	req  docker.CreateContainerRequest
}

// placedAs reports whether this container was created for the named container
// definition. Overcast names a task's containers
// "overcast-ecs-<cluster>-<task>-<definition>", so the definition is the
// suffix.
func (c createdContainer) placedAs(containerName string) bool {
	return strings.HasSuffix(c.name, "-"+containerName)
}

// createdContainers returns the creates the daemon has answered, in order.
func (fd *fakeECSDockerDaemon) createdContainers() []createdContainer {
	fd.mu.Lock()
	defer fd.mu.Unlock()
	return append([]createdContainer(nil), fd.created...)
}

// startedCount reports how many containers have been started.
func (fd *fakeECSDockerDaemon) startedCount() int {
	fd.mu.Lock()
	defer fd.mu.Unlock()
	return len(fd.started)
}

// createdEnvironments returns one environment snapshot per container create.
func (fd *fakeECSDockerDaemon) createdEnvironments() [][]string {
	fd.mu.Lock()
	defer fd.mu.Unlock()
	envs := make([][]string, len(fd.created))
	for i := range fd.created {
		envs[i] = append([]string(nil), fd.created[i].req.Env...)
	}
	return envs
}

func (fd *fakeECSDockerDaemon) setOnStart(fn func(string)) {
	fd.mu.Lock()
	defer fd.mu.Unlock()
	fd.onStart = fn
}

// setOnStop runs fn when a container is stopped, before the daemon answers.
//
// A real daemon raises a die event there, and the handler for it is what the
// stop paths race — so a test about that ordering needs the event delivered at
// the moment of the kill rather than at some point afterwards. Running it
// inline on the caller's own goroutine is the worst case of that race and the
// only version of it that is not a coin toss.
func (fd *fakeECSDockerDaemon) setOnStop(fn func(string)) {
	fd.mu.Lock()
	defer fd.mu.Unlock()
	fd.onStop = fn
}

// setContainerLogs gives a container the bytes its logs endpoint serves, in
// whatever framing the test is about — multiplexed or the raw TTY stream.
func (fd *fakeECSDockerDaemon) setContainerLogs(containerID string, body []byte) {
	fd.mu.Lock()
	defer fd.mu.Unlock()
	fd.logs[containerID] = body
}

// wasRemoved reports whether the container has been deleted from the daemon,
// which is the point after which its logs are unrecoverable.
func (fd *fakeECSDockerDaemon) wasRemoved(containerID string) bool {
	fd.mu.Lock()
	defer fd.mu.Unlock()
	return fd.removed[containerID]
}

func (fd *fakeECSDockerDaemon) latestResourceID() string {
	fd.mu.Lock()
	defer fd.mu.Unlock()
	if len(fd.created) == 0 || fd.created[len(fd.created)-1].req.Labels == nil {
		return ""
	}
	return fd.created[len(fd.created)-1].req.Labels[docker.LabelResourceID]
}

func newFakeECSDockerDaemon(t *testing.T) *fakeECSDockerDaemon {
	t.Helper()
	fd := &fakeECSDockerDaemon{
		logs:    map[string][]byte{},
		removed: map[string]bool{},
	}
	var seq int

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		switch {
		// Image inspect, for an image a test gave a working directory — what
		// the debugger reads for a container's remote root.
		case strings.Contains(p, "/images/") && strings.HasSuffix(p, "/json") && fd.imageWorkingDir(p) != "":
			w.Write([]byte(`{"Config":{"WorkingDir":"` + fd.imageWorkingDir(p) + `"}}`)) //nolint:errcheck

		// Image pull, and the inspect the puller does around it.
		case strings.HasSuffix(p, "/images/create"), strings.Contains(p, "/images/"):
			w.WriteHeader(http.StatusOK)

		// GetContainerByName lookup before create — no container of that name.
		case strings.Contains(p, "/containers/overcast-ecs-") && strings.HasSuffix(p, "/json"):
			w.WriteHeader(http.StatusNotFound)

		case strings.HasSuffix(p, "/containers/create"):
			var create docker.CreateContainerRequest
			if err := json.NewDecoder(r.Body).Decode(&create); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			fd.mu.Lock()
			seq++
			id := fmt.Sprintf("ecsfakecontainer%04d", seq)
			fd.created = append(fd.created, createdContainer{
				name: r.URL.Query().Get("name"), id: id, req: create,
			})
			fd.mu.Unlock()
			w.Write([]byte(`{"Id":"` + id + `"}`)) //nolint:errcheck

		// Logs, until the container is removed. Docker answers a removed one
		// with a JSON 404, which the client turns into an error rather than
		// letting the body reach a caller that would read it as output.
		case strings.HasSuffix(p, "/logs"):
			id := containerIDFromPath(p)
			fd.mu.Lock()
			gone, body := fd.removed[id], fd.logs[id]
			fd.mu.Unlock()
			if gone {
				w.WriteHeader(http.StatusNotFound)
				w.Write([]byte(`{"message":"No such container: ` + id + `"}`)) //nolint:errcheck
				return
			}
			w.Header().Set("Content-Type", "application/vnd.docker.raw-stream")
			w.Write(body) //nolint:errcheck

		case strings.HasSuffix(p, "/stop"):
			fd.mu.Lock()
			onStop := fd.onStop
			fd.releaseHostPorts(containerIDFromPath(p))
			fd.mu.Unlock()
			if onStop != nil {
				onStop(containerIDFromPath(p))
			}
			w.WriteHeader(http.StatusNoContent)

		case strings.HasSuffix(p, "/start"):
			containerID := containerIDFromPath(p)
			fd.mu.Lock()
			refused, portTaken := false, ""
			for _, c := range fd.created {
				if c.id != containerID {
					continue
				}
				for _, name := range fd.refuseStart {
					if c.placedAs(name) {
						refused = true
						break
					}
				}
				if !refused {
					portTaken = fd.claimHostPorts(c)
				}
				break
			}
			if !refused && portTaken == "" {
				fd.started = append(fd.started, containerID)
			}
			onStart := fd.onStart
			fd.mu.Unlock()
			if portTaken != "" {
				w.WriteHeader(http.StatusInternalServerError)
				w.Write([]byte(`{"message":"driver failed programming external connectivity on endpoint ` + //nolint:errcheck
					containerID + `: Bind for 0.0.0.0:` + portTaken + ` failed: port is already allocated"}`))
				return
			}
			if refused {
				w.WriteHeader(http.StatusInternalServerError)
				w.Write([]byte(`{"message":"driver failed programming external connectivity on endpoint ` + containerID + `"}`)) //nolint:errcheck
				return
			}
			if onStart != nil {
				onStart(containerID)
			}
			w.WriteHeader(http.StatusNoContent)

		case r.Method == http.MethodDelete && strings.Contains(p, "/containers/"):
			fd.mu.Lock()
			fd.removed[containerIDFromPath(p)] = true
			fd.releaseHostPorts(containerIDFromPath(p))
			fd.mu.Unlock()
			w.WriteHeader(http.StatusNoContent)

		// Container inspect, once one exists. Its published ports are the
		// ones its create asked for, an ephemeral binding answered with the
		// port the daemon "picked" — see publishedPorts.
		case strings.Contains(p, "/containers/") && strings.HasSuffix(p, "/json"):
			ports, _ := json.Marshal(fd.publishedPorts(containerIDFromPath(p)))
			w.Write([]byte(`{"Id":"` + containerIDFromPath(p) + `",` + //nolint:errcheck
				`"State":{"Status":"running","Running":true},` +
				`"NetworkSettings":{"Networks":{"bridge":{"IPAddress":"172.17.0.2"}},"Ports":` + string(ports) + `}}`))

		case strings.HasSuffix(p, "/networks/create"):
			w.Write([]byte(`{"Id":"net-ecs-fake"}`)) //nolint:errcheck

		case strings.HasSuffix(p, "/networks/prune"):
			w.WriteHeader(http.StatusOK)

		// Network inspect — no containers attached, which is all the data
		// plane needs to decide to connect.
		case strings.Contains(p, "/networks/") && r.Method == http.MethodGet:
			w.Write([]byte(`{"Id":"net-ecs-fake","Name":"bridge","Containers":{}}`)) //nolint:errcheck

		// connect/disconnect, archive (CA bundle), and anything else the path
		// touches but does not read a body from.
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	fd.srv = srv
	t.Cleanup(srv.Close)
	return fd
}

// wireFakeGC gives an already-wired handler a real container GC over the same
// fake daemon, with its remove loop running — for the tests whose subject is
// the GC's own ordering, which a nil gc routes around entirely.
//
// Torn down with DrainAndSweep so the queue is empty and every goroutine has
// finished before the fake daemon closes under them.
func wireFakeGC(t *testing.T, h *Handler, fd *fakeECSDockerDaemon) {
	t.Helper()
	gc := docker.NewGC(docker.NewClient("tcp://"+fd.srv.Listener.Addr().String(), zap.NewNop()),
		zap.NewNop(), h.cfg.ECSKeepContainers, h.instances.Resolve)
	gc.SetBeforeRemove(h.captureContainerLogsByID)
	gc.StartRemoveLoop(context.Background())
	h.gc = gc
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		gc.DrainAndSweep(ctx, serviceName)
	})
}

// wireStalledGC wires a real GC whose remove loop is deliberately not running:
// the state of an emulator whose background queue has not been reached yet,
// which in a fast compat run is every emulator in the seconds after a delete.
// A teardown path that leans on the queue leaves its container on the daemon
// for as long as the test cares to look; one that removes inline does not.
//
// Torn down with DrainAndSweep, which processes the queue inline — so anything
// a test deliberately left in it is cleaned up before the fake daemon closes.
func wireStalledGC(t *testing.T, h *Handler, fd *fakeECSDockerDaemon) {
	t.Helper()
	gc := docker.NewGC(docker.NewClient("tcp://"+fd.srv.Listener.Addr().String(), zap.NewNop()),
		zap.NewNop(), h.cfg.ECSKeepContainers, h.instances.Resolve)
	gc.SetBeforeRemove(h.captureContainerLogsByID)
	h.gc = gc
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		gc.DrainAndSweep(ctx, serviceName)
	})
}

// waitFor blocks until cond holds, for the assertions whose subject completes
// on a goroutine the test does not own.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// containerIDFromPath pulls the container ID out of /v1.43/containers/<id>/...
func containerIDFromPath(p string) string {
	const marker = "/containers/"
	i := strings.Index(p, marker)
	if i < 0 {
		return ""
	}
	rest := p[i+len(marker):]
	if j := strings.Index(rest, "/"); j >= 0 {
		return rest[:j]
	}
	return rest
}

// newECSDockerTestHandler is newECSRegionTestHandler with Docker wired to a
// fake daemon, for tests whose subject is gated on dockerReady.
//
// gc stays nil, as in the elasticache harness: nothing here exercises the
// reaper, and starting its loop would only add traffic to the fake.
func newECSDockerTestHandler(t *testing.T) (*Handler, *clock.Mock, *fakeECSDockerDaemon) {
	t.Helper()
	clk := clock.NewMock()
	svc := New(&config.Config{Region: "us-east-1", AccountID: "123456789012"}, state.NewMemoryStore(), zap.NewNop(), clk, nil)
	h := svc.handler
	fd := wireFakeDocker(t, h)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		h.scheduler.Stop(ctx)
	})
	return h, clk, fd
}

// wireFakeDocker points an already-built handler at a fake daemon, for the
// tests that construct their own — a gated store, a real clock — and so cannot
// take newECSDockerTestHandler wholesale. It wires the same three fields
// Service.SetDocker does, minus the GC.
func wireFakeDocker(t *testing.T, h *Handler) *fakeECSDockerDaemon {
	t.Helper()
	fd := newFakeECSDockerDaemon(t)
	h.docker = docker.NewClient("tcp://"+fd.srv.Listener.Addr().String(), zap.NewNop())
	h.puller = docker.NewImagePuller(h.docker)
	h.dockerReady.Store(true)
	return fd
}
