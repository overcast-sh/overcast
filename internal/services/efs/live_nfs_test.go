package efs

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
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
	"github.com/overcast-sh/overcast/internal/state"
)

// ─── Fake Docker daemon with container lifecycle ──────────────────────────────

// createdContainer records one POST /containers/create.
type createdContainer struct {
	Name         string
	Image        string
	Cmd          []string
	Labels       map[string]string
	Mounts       []docker.Mount
	PortBindings map[string][]docker.PortBinding
	CapAdd       []string
	Privileged   bool
}

// fakeNFSDaemon speaks enough of the Engine API for export containers, and
// lets a test block inside StartContainer to reproduce the delete-mid-start
// interleaving deterministically.
type fakeNFSDaemon struct {
	mu         sync.Mutex
	created    []createdContainer
	started    []string
	stopped    []string
	removed    []string
	listed     []docker.ContainerSummary
	inspectRun map[string]bool // container ID → State.Running (default true)
	// exists holds every name and ID the daemon knows about, so inspecting an
	// unknown container 404s the way a real daemon does.
	exists map[string]bool
	// idByName maps a pre-seeded container's name onto its ID, and labels
	// holds what inspect reports for it, so GetContainerByName finds a
	// container "left over from an earlier run" that carries our labels.
	idByName map[string]string
	labels   map[string]map[string]string
	// connected records every POST /networks/{name}/connect: which network,
	// which container, and the aliases it advertised there.
	connected []networkConnect

	// startGate, when non-nil, blocks every StartContainer until closed.
	startGate chan struct{}

	srv *httptest.Server
}

// networkConnect is one container joining one network.
type networkConnect struct {
	Network   string
	Container string
	Aliases   []string
}

func newFakeNFSDaemon(t *testing.T) *fakeNFSDaemon {
	t.Helper()
	fd := &fakeNFSDaemon{
		inspectRun: map[string]bool{}, exists: map[string]bool{},
		idByName: map[string]string{}, labels: map[string]map[string]string{},
	}
	fd.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(path, "/volumes/create"):
			w.Write([]byte(`{"Name":"v"}`)) //nolint:errcheck

		case r.Method == http.MethodGet && strings.HasSuffix(path, "/volumes"):
			_ = json.NewEncoder(w).Encode(struct {
				Volumes []docker.VolumeSummary `json:"Volumes"`
			}{})

		case r.Method == http.MethodPost && strings.HasSuffix(path, "/images/create"):
			w.WriteHeader(http.StatusOK)

		case r.Method == http.MethodPost && strings.HasSuffix(path, "/containers/create"):
			var req struct {
				Image      string            `json:"Image"`
				Cmd        []string          `json:"Cmd"`
				Labels     map[string]string `json:"Labels"`
				HostConfig struct {
					Mounts       []docker.Mount                  `json:"Mounts"`
					PortBindings map[string][]docker.PortBinding `json:"PortBindings"`
					CapAdd       []string                        `json:"CapAdd"`
					Privileged   bool                            `json:"Privileged"`
				} `json:"HostConfig"`
			}
			_ = json.NewDecoder(r.Body).Decode(&req)
			name := r.URL.Query().Get("name")
			fd.mu.Lock()
			fd.created = append(fd.created, createdContainer{
				Name: name, Image: req.Image, Cmd: req.Cmd, Labels: req.Labels,
				Mounts: req.HostConfig.Mounts, PortBindings: req.HostConfig.PortBindings,
				CapAdd: req.HostConfig.CapAdd, Privileged: req.HostConfig.Privileged,
			})
			id := "ctr-" + name
			fd.exists[name], fd.exists[id] = true, true
			// Root-directory helpers are one-shot: report them exited so the
			// caller's wait loop finishes. Exports keep running.
			fd.inspectRun[id] = !strings.HasPrefix(name, "overcast-efs-mkdir-")
			fd.mu.Unlock()
			w.Write([]byte(`{"Id":"` + id + `"}`)) //nolint:errcheck

		case r.Method == http.MethodPost && strings.HasSuffix(path, "/start"):
			fd.mu.Lock()
			gate := fd.startGate
			fd.mu.Unlock()
			if gate != nil {
				<-gate
			}
			fd.mu.Lock()
			fd.started = append(fd.started, containerIDFromPath(path))
			fd.mu.Unlock()
			w.WriteHeader(http.StatusNoContent)

		case r.Method == http.MethodPost && strings.HasSuffix(path, "/stop"):
			fd.mu.Lock()
			fd.stopped = append(fd.stopped, containerIDFromPath(path))
			fd.mu.Unlock()
			w.WriteHeader(http.StatusNoContent)

		case r.Method == http.MethodDelete && strings.Contains(path, "/containers/"):
			id := containerIDFromPath(path)
			fd.mu.Lock()
			fd.removed = append(fd.removed, id)
			delete(fd.exists, id)
			fd.mu.Unlock()
			w.WriteHeader(http.StatusNoContent)

		case r.Method == http.MethodGet && strings.HasSuffix(path, "/containers/json"):
			fd.mu.Lock()
			out := append([]docker.ContainerSummary(nil), fd.listed...)
			fd.mu.Unlock()
			_ = json.NewEncoder(w).Encode(out)

		case r.Method == http.MethodPost && strings.Contains(path, "/networks/") && strings.HasSuffix(path, "/connect"):
			var req struct {
				Container      string `json:"Container"`
				EndpointConfig *struct {
					Aliases []string `json:"Aliases"`
				} `json:"EndpointConfig"`
			}
			_ = json.NewDecoder(r.Body).Decode(&req)
			nc := networkConnect{Network: networkFromPath(path), Container: req.Container}
			if req.EndpointConfig != nil {
				nc.Aliases = req.EndpointConfig.Aliases
			}
			fd.mu.Lock()
			fd.connected = append(fd.connected, nc)
			fd.mu.Unlock()
			w.WriteHeader(http.StatusOK)

		case r.Method == http.MethodGet && strings.HasSuffix(path, "/json"):
			id := containerIDFromPath(path)
			fd.mu.Lock()
			if byName, ok := fd.idByName[id]; ok {
				id = byName
			}
			known := fd.exists[id]
			running, ok := fd.inspectRun[id]
			if !ok {
				running = true
			}
			labels := fd.labels[id]
			fd.mu.Unlock()
			if !known {
				w.WriteHeader(http.StatusNotFound)
				w.Write([]byte(`{"message":"No such container"}`)) //nolint:errcheck
				return
			}
			status := "running"
			if !running {
				status = "exited"
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"Id":     id,
				"Config": map[string]any{"Labels": labels},
				"State":  map[string]any{"Status": status, "Running": running, "ExitCode": 0},
			})

		default:
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	t.Cleanup(fd.srv.Close)
	return fd
}

// containerIDFromPath pulls the container ID out of "/v1.x/containers/<id>/<verb>".
func containerIDFromPath(path string) string {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	for i, p := range parts {
		if p == "containers" && i+1 < len(parts) {
			return parts[i+1]
		}
	}
	return ""
}

// networkFromPath pulls the network out of "/v1.x/networks/<name>/connect".
func networkFromPath(path string) string {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	for i, p := range parts {
		if p == "networks" && i+1 < len(parts) {
			return parts[i+1]
		}
	}
	return ""
}

func (fd *fakeNFSDaemon) createdContainers() []createdContainer {
	fd.mu.Lock()
	defer fd.mu.Unlock()
	return append([]createdContainer(nil), fd.created...)
}

// connections returns every network connect the daemon saw, in order.
func (fd *fakeNFSDaemon) connections() []networkConnect {
	fd.mu.Lock()
	defer fd.mu.Unlock()
	return append([]networkConnect(nil), fd.connected...)
}

// seedContainer makes the daemon know a running container by name and ID —
// one an earlier Overcast run left behind — carrying the given labels.
func (fd *fakeNFSDaemon) seedContainer(name, id string, labels map[string]string) {
	fd.mu.Lock()
	defer fd.mu.Unlock()
	fd.exists[name], fd.exists[id] = true, true
	fd.idByName[name] = id
	fd.labels[id] = labels
}

func (fd *fakeNFSDaemon) removedContainers() []string {
	fd.mu.Lock()
	defer fd.mu.Unlock()
	return append([]string(nil), fd.removed...)
}

// newNFSTestService builds a live-mode Service with the NFS data plane on,
// wired to the fake daemon. Readiness probing is disabled by default so unit
// tests never dial a real socket; TestNFSReadiness covers the probe itself.
func newNFSTestService(t *testing.T, fd *fakeNFSDaemon, nfs bool) *Service {
	t.Helper()
	svc := newNFSTestServiceWithClock(t, fd, nfs, clock.New(), zap.NewNop())
	svc.skipNFSReadiness = true
	return svc
}

// newNFSTestServiceWithClock is newNFSTestService with the clock and logger
// left to the caller, for the readiness test — it drives the retry budget on a
// mock clock instead of waiting out 30 seconds of real retries.
func newNFSTestServiceWithClock(t *testing.T, fd *fakeNFSDaemon, nfs bool, clk clock.Clock, log *zap.Logger) *Service {
	t.Helper()
	cfg := &config.Config{
		Region: "us-east-1", AccountID: "000000000000",
		EFSMode: config.EFSModeLive, EFSNFSExport: nfs,
		EFSNFSPortBase: 22049, EFSNFSImage: "ganesha:test",
	}
	svc := New(cfg, state.NewMemoryStore(), log, clk)
	if fd != nil {
		dc := docker.NewClient("tcp://"+fd.srv.Listener.Addr().String(), zap.NewNop())
		svc.docker = dc
		svc.puller = docker.NewImagePuller(dc)
		svc.dockerReady.Store(true)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		svc.Stop(ctx)
	})
	return svc
}

// seedFileSystem creates an available file system for mount-target tests.
func seedFileSystem(t *testing.T, svc *Service, ctx context.Context, token string) string {
	t.Helper()
	fs, aerr := svc.createFileSystemTyped(ctx, &createFileSystemRequest{CreationToken: token})
	if aerr != nil {
		t.Fatalf("CreateFileSystem: %v", aerr)
	}
	return fs.FileSystemId
}

// eventually polls cond until it holds, failing the test after 3s. Export
// starts run off the request path, so assertions on them poll rather than
// sleep for a fixed interval.
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// awaitExport waits for a mount target's export container to be recorded and
// returns the record.
func awaitExport(t *testing.T, svc *Service, ctx context.Context, region, mtID string) *mountTargetRecord {
	t.Helper()
	var rec *mountTargetRecord
	eventually(t, "export container recorded on "+mtID, func() bool {
		got, found, err := svc.getMountTarget(ctx, region, mtID)
		if err != nil || !found || got.NFSContainerId == "" {
			return false
		}
		rec = got
		return true
	})
	return rec
}

// ─── Export lifecycle ─────────────────────────────────────────────────────────

func TestNFSExport_mountTargetStartsAndStopsGanesha(t *testing.T) {
	fd := newFakeNFSDaemon(t)
	svc := newNFSTestService(t, fd, true)
	ctx := context.Background()

	fsID := seedFileSystem(t, svc, ctx, "nfs-1")
	mt, aerr := svc.createMountTargetTyped(ctx, &createMountTargetRequest{
		FileSystemId: fsID, SubnetId: "subnet-1111",
	})
	if aerr != nil {
		t.Fatalf("CreateMountTarget: %v", aerr)
	}

	rec := awaitExport(t, svc, ctx, "us-east-1", mt.MountTargetId)

	created := fd.createdContainers()
	if len(created) != 1 {
		t.Fatalf("expected 1 export container, got %d: %+v", len(created), created)
	}
	c := created[0]
	if want := nfsContainerName(mt.MountTargetId); c.Name != want {
		t.Fatalf("container name: want %q, got %q", want, c.Name)
	}
	if c.Image != "ganesha:test" {
		t.Fatalf("image: want the configured image, got %q", c.Image)
	}
	if c.Labels[docker.LabelResourceID] != mt.MountTargetId || c.Labels[docker.LabelService] != serviceName {
		t.Fatalf("managed labels missing/wrong: %v", c.Labels)
	}
	if len(c.Mounts) != 1 || c.Mounts[0].Source != volumeName(fsID) || c.Mounts[0].Target != nfsExportRoot {
		t.Fatalf("expected the file system volume mounted at %s, got %+v", nfsExportRoot, c.Mounts)
	}
	if bindings := c.PortBindings[nfsContainerPort]; len(bindings) != 1 || bindings[0].HostPort == "" {
		t.Fatalf("expected 2049 published on a host port, got %+v", c.PortBindings)
	}

	// The mount target records the container and port so delete can reclaim
	// them, and reaches "available" once the export is up.
	if rec.NFSHostPort == 0 {
		t.Fatalf("expected a host port persisted, got %+v", rec)
	}
	eventually(t, "mount target available", func() bool {
		got, found, err := svc.getMountTarget(ctx, "us-east-1", mt.MountTargetId)
		return err == nil && found && got.LifeCycleState == stateAvailable
	})

	// Delete tears the container down.
	if _, aerr := svc.deleteMountTargetTyped(ctx, &deleteMountTargetRequest{MountTargetId: mt.MountTargetId}); aerr != nil {
		t.Fatalf("DeleteMountTarget: %v", aerr)
	}
	// Teardown runs off the request path, so poll for it.
	eventually(t, "export container removed", func() bool {
		for _, id := range fd.removedContainers() {
			if id == rec.NFSContainerId {
				return true
			}
		}
		return false
	})
	// The host port is released back to the pool.
	eventually(t, "host port released", func() bool {
		used, err := svc.nfsPortInUse(ctx, rec.NFSHostPort)
		return err == nil && !used
	})
}

// TestNFSExport_grantsDacReadSearch pins the one capability the export needs.
// Ganesha's VFS FSAL resolves file handles with open_by_handle_at(), which the
// kernel gates on CAP_DAC_READ_SEARCH. It is not in Docker's default set, so
// without it the export mounts once and then serves nothing: readdir returns
// an empty directory, writes fail with EPERM, and every later mount is
// refused ("Failed to get attrs for referral ... Forbidden action").
func TestNFSExport_grantsDacReadSearch(t *testing.T) {
	fd := newFakeNFSDaemon(t)
	svc := newNFSTestService(t, fd, true)
	ctx := context.Background()

	fsID := seedFileSystem(t, svc, ctx, "nfs-caps")
	mt, aerr := svc.createMountTargetTyped(ctx, &createMountTargetRequest{
		FileSystemId: fsID, SubnetId: "subnet-caps",
	})
	if aerr != nil {
		t.Fatalf("CreateMountTarget: %v", aerr)
	}
	awaitExport(t, svc, ctx, "us-east-1", mt.MountTargetId)

	created := fd.createdContainers()
	if len(created) != 1 {
		t.Fatalf("expected 1 export container, got %d: %+v", len(created), created)
	}
	c := created[0]
	if len(c.CapAdd) != 1 || c.CapAdd[0] != "DAC_READ_SEARCH" {
		t.Fatalf("export container must add exactly CAP_DAC_READ_SEARCH, got %v", c.CapAdd)
	}
	// One capability, not a blanket escalation: the export stays unprivileged.
	if c.Privileged {
		t.Fatal("export container must not be privileged")
	}
}

func TestNFSExport_offByDefaultInLiveMode(t *testing.T) {
	fd := newFakeNFSDaemon(t)
	svc := newNFSTestService(t, fd, false)
	ctx := context.Background()

	fsID := seedFileSystem(t, svc, ctx, "nfs-off")
	mt, aerr := svc.createMountTargetTyped(ctx, &createMountTargetRequest{
		FileSystemId: fsID, SubnetId: "subnet-2222",
	})
	if aerr != nil {
		t.Fatalf("CreateMountTarget: %v", aerr)
	}
	if created := fd.createdContainers(); len(created) != 0 {
		t.Fatalf("NFS export must be opt-in; got containers %+v", created)
	}
	// The mount target still reaches "available" inline, as before step 4.
	got, found, err := svc.getMountTarget(ctx, "us-east-1", mt.MountTargetId)
	if err != nil || !found || got.LifeCycleState != stateAvailable {
		t.Fatalf("expected available mount target, got %+v (found=%v err=%v)", got, found, err)
	}
}

// TestNFSExport_deletedMidStartTearsDownContainer reproduces the leak class
// fixed for RDS (#412), ElastiCache (#459) and MSK/Lambda (#461): the delete
// lands while the container is still starting, so the record it reads carries
// no container ID and the start goroutine is the only party that can clean up.
func TestNFSExport_deletedMidStartTearsDownContainer(t *testing.T) {
	fd := newFakeNFSDaemon(t)
	fd.mu.Lock()
	fd.startGate = make(chan struct{})
	fd.mu.Unlock()

	svc := newNFSTestService(t, fd, true)
	ctx := context.Background()

	fsID := seedFileSystem(t, svc, ctx, "nfs-race")
	mt, aerr := svc.createMountTargetTyped(ctx, &createMountTargetRequest{
		FileSystemId: fsID, SubnetId: "subnet-3333",
	})
	if aerr != nil {
		t.Fatalf("CreateMountTarget: %v", aerr)
	}

	// Wait until the container exists and the start goroutine is parked inside
	// StartContainer, then delete: the stored record has no container ID yet,
	// so delete cannot reclaim it.
	eventually(t, "export container created", func() bool {
		return len(fd.createdContainers()) == 1
	})
	if _, aerr := svc.deleteMountTargetTyped(ctx, &deleteMountTargetRequest{MountTargetId: mt.MountTargetId}); aerr != nil {
		t.Fatalf("DeleteMountTarget: %v", aerr)
	}
	if removed := fd.removedContainers(); len(removed) != 0 {
		t.Fatalf("delete found a container ID it should not have had: %v", removed)
	}

	// Release the start; the goroutine must notice the record is gone and
	// remove the container it owns.
	fd.mu.Lock()
	close(fd.startGate)
	fd.startGate = nil
	fd.mu.Unlock()

	wantID := "ctr-" + nfsContainerName(mt.MountTargetId)
	deadline := time.Now().Add(3 * time.Second)
	for {
		removed := fd.removedContainers()
		for _, id := range removed {
			if id == wantID {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("start goroutine leaked container %q; removed=%v", wantID, removed)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// ─── Reconciliation ───────────────────────────────────────────────────────────

func TestReconcileExports_adoptsKnownAndSweepsOrphans(t *testing.T) {
	fd := newFakeNFSDaemon(t)
	svc := newNFSTestService(t, fd, true)
	ctx := context.Background()

	// A mount target persisted across a restart, with its container running.
	if err := svc.putMountTarget(ctx, "eu-west-1", &mountTargetRecord{
		MountTargetId: "fsmt-aaaaaaaaaaaaaaaaa", FileSystemId: "fs-aaaaaaaaaaaaaaaaa",
		LifeCycleState: stateAvailable,
	}); err != nil {
		t.Fatalf("seed mount target: %v", err)
	}

	fd.mu.Lock()
	fd.listed = []docker.ContainerSummary{
		{
			ID:     "ctr-adopt",
			Names:  []string{"/" + nfsContainerName("fsmt-aaaaaaaaaaaaaaaaa")},
			State:  "running",
			Labels: docker.ManagedLabels(serviceName, "fsmt-aaaaaaaaaaaaaaaaa"),
			Ports: []struct {
				HostPort      int    `json:"PublicPort"`
				ContainerPort int    `json:"PrivatePort"`
				Type          string `json:"Type"`
			}{{HostPort: 22050, ContainerPort: 2049, Type: "tcp"}},
		},
		{
			ID:     "ctr-orphan",
			Names:  []string{"/" + nfsContainerName("fsmt-zzzzzzzzzzzzzzzzz")},
			State:  "running",
			Labels: svc.managedLabels(ctx, "fsmt-zzzzzzzzzzzzzzzzz"),
		},
		{
			// A one-shot root-directory helper that outlived its process.
			ID:     "ctr-helper",
			Names:  []string{"/overcast-efs-mkdir-abc"},
			State:  "exited",
			Labels: svc.managedLabels(ctx, volumeName("fs-aaaaaaaaaaaaaaaaa")),
		},
	}
	fd.mu.Unlock()

	svc.reconcileExports(ctx)

	// The surviving mount target adopts its container and port…
	rec, found, err := svc.getMountTarget(ctx, "eu-west-1", "fsmt-aaaaaaaaaaaaaaaaa")
	if err != nil || !found {
		t.Fatalf("get mount target: found=%v err=%v", found, err)
	}
	if rec.NFSContainerId != "ctr-adopt" || rec.NFSHostPort != 22050 {
		t.Fatalf("expected adoption of ctr-adopt on 22050, got %+v", rec)
	}
	if used, err := svc.nfsPortInUse(ctx, 22050); err != nil || !used {
		t.Fatalf("expected adopted port reserved, inUse=%v err=%v", used, err)
	}

	// …and both the orphaned export and the stale helper are removed.
	removed := fd.removedContainers()
	wantRemoved := map[string]bool{"ctr-orphan": false, "ctr-helper": false}
	for _, id := range removed {
		if _, ok := wantRemoved[id]; ok {
			wantRemoved[id] = true
		}
		if id == "ctr-adopt" {
			t.Fatalf("reconciliation removed a live export container")
		}
	}
	for id, got := range wantRemoved {
		if !got {
			t.Fatalf("expected %q swept, removed=%v", id, removed)
		}
	}
}

func TestReconcileContainersAdoptsExportFromCentralSnapshot(t *testing.T) {
	fd := newFakeNFSDaemon(t)
	svc := newNFSTestService(t, fd, true)
	ctx := context.Background()
	const mountTargetID = "fsmt-bbbbbbbbbbbbbbbbb"
	if err := svc.putMountTarget(ctx, "eu-west-1", &mountTargetRecord{
		MountTargetId: mountTargetID, FileSystemId: "fs-bbbbbbbbbbbbbbbbb", LifeCycleState: stateAvailable,
	}); err != nil {
		t.Fatalf("seed mount target: %v", err)
	}
	containers := []docker.ContainerSummary{{
		ID: "ctr-central", Names: []string{"/" + nfsContainerName(mountTargetID)}, State: "running",
		Labels: docker.ManagedLabels(serviceName, mountTargetID),
		Ports: []struct {
			HostPort      int    `json:"PublicPort"`
			ContainerPort int    `json:"PrivatePort"`
			Type          string `json:"Type"`
		}{{HostPort: 22051, ContainerPort: 2049, Type: "tcp"}},
	}}

	svc.ReconcileContainers(ctx, containers)
	svc.nfsWg.Wait()

	rec, found, err := svc.getMountTarget(ctx, "eu-west-1", mountTargetID)
	if err != nil || !found {
		t.Fatalf("get mount target: found=%v err=%v", found, err)
	}
	if rec.NFSContainerId != "ctr-central" || rec.NFSHostPort != 22051 {
		t.Fatalf("central snapshot was not adopted: %+v", rec)
	}
}

// ─── Ganesha configuration ────────────────────────────────────────────────────

func TestGaneshaConfig_rootAndAccessPointPseudoPaths(t *testing.T) {
	uid, gid := int64(1234), int64(5678)
	cfg := buildGaneshaConfig([]ganeshaExport{
		{ID: 1, Path: nfsExportRoot, Pseudo: "/"},
		{ID: 2, Path: nfsExportRoot + "/app/data", Pseudo: "/fsap-1111", AnonUID: &uid, AnonGID: &gid},
	})

	for _, want := range []string{
		"Export_Id = 1;",
		"Path = " + nfsExportRoot + ";",
		"Pseudo = /;",
		"Squash = No_Root_Squash;",
		"Export_Id = 2;",
		"Path = " + nfsExportRoot + "/app/data;",
		"Pseudo = /fsap-1111;",
		"Squash = All_Squash;",
		"Anonymous_Uid = 1234;",
		"Anonymous_Gid = 5678;",
		"Protocols = 4;",
	} {
		if !strings.Contains(cfg, want) {
			t.Fatalf("ganesha config missing %q:\n%s", want, cfg)
		}
	}
}

func TestGaneshaExports_accessPointsBecomePseudoPaths(t *testing.T) {
	fd := newFakeNFSDaemon(t)
	svc := newNFSTestService(t, fd, true)
	ctx := context.Background()

	fsID := seedFileSystem(t, svc, ctx, "nfs-ap")
	uid, gid := int64(1000), int64(1000)
	ap, aerr := svc.createAccessPointTyped(ctx, &createAccessPointRequest{
		ClientToken: "ap-1", FileSystemId: fsID,
		PosixUser:     &PosixUser{Uid: &uid, Gid: &gid},
		RootDirectory: &RootDirectory{Path: "/app/data", CreationInfo: &CreationInfo{OwnerUid: &uid, OwnerGid: &gid, Permissions: "0755"}},
	})
	if aerr != nil {
		t.Fatalf("CreateAccessPoint: %v", aerr)
	}

	exports, err := svc.ganeshaExportsFor(ctx, "us-east-1", fsID)
	if err != nil {
		t.Fatalf("ganeshaExportsFor: %v", err)
	}
	if len(exports) != 2 {
		t.Fatalf("expected root export plus one access point, got %+v", exports)
	}
	if exports[0].Pseudo != "/" || exports[0].Path != nfsExportRoot {
		t.Fatalf("first export must be the volume root, got %+v", exports[0])
	}
	got := exports[1]
	if got.Pseudo != "/"+ap.AccessPointId {
		t.Fatalf("access point pseudo path: want /%s, got %q", ap.AccessPointId, got.Pseudo)
	}
	if got.Path != nfsExportRoot+"/app/data" {
		t.Fatalf("access point path: want the root directory, got %q", got.Path)
	}
	if got.AnonUID == nil || *got.AnonUID != uid || got.AnonGID == nil || *got.AnonGID != gid {
		t.Fatalf("PosixUser must become Anonymous_Uid/Gid, got %+v", got)
	}
}

// TestGaneshaExports_unsafeRootDirectoryIsSkipped guards the config generator
// against an access point whose root directory cannot be embedded in
// ganesha.conf. CreateAccessPoint does not constrain the path, so a newline
// could otherwise terminate the heredoc that carries the config into the
// container and leave the rest of it running as shell.
func TestGaneshaExports_unsafeRootDirectoryIsSkipped(t *testing.T) {
	fd := newFakeNFSDaemon(t)
	svc := newNFSTestService(t, fd, true)
	ctx := context.Background()

	fsID := seedFileSystem(t, svc, ctx, "nfs-unsafe")
	for i, path := range []string{
		"/app\nOVERCAST_GANESHA_EOF\nrm -rf /",
		"/app; Export_Id = 99;",
		"/app dir",
		"/app'quote",
		"/app#comment",
	} {
		if _, aerr := svc.createAccessPointTyped(ctx, &createAccessPointRequest{
			ClientToken:   fmt.Sprintf("unsafe-%d", i),
			FileSystemId:  fsID,
			RootDirectory: &RootDirectory{Path: path},
		}); aerr != nil {
			t.Fatalf("CreateAccessPoint(%q): %v", path, aerr)
		}
	}

	exports, err := svc.ganeshaExportsFor(ctx, "us-east-1", fsID)
	if err != nil {
		t.Fatalf("ganeshaExportsFor: %v", err)
	}
	if len(exports) != 1 {
		t.Fatalf("expected only the root export, got %+v", exports)
	}
	// The generated config must contain exactly one export terminator and no
	// trace of the injected text.
	cfg := buildGaneshaConfig(exports)
	for _, banned := range []string{"OVERCAST_GANESHA_EOF", "rm -rf", "Export_Id = 99"} {
		if strings.Contains(cfg, banned) {
			t.Fatalf("generated config leaked %q:\n%s", banned, cfg)
		}
	}
	if got := strings.Count(ganeshaStartScript(exports), "OVERCAST_GANESHA_EOF"); got != 2 {
		t.Fatalf("expected exactly one heredoc open/close pair, got %d markers", got)
	}
}

// TestGaneshaStartScript_anchorsAccessPointPseudoPaths reproduces the defect
// where one access point took the whole export down. Ganesha builds its
// pseudo-filesystem by grafting each export's Pseudo path onto the tree, and
// it cannot create a directory inside a non-PSEUDO export: with the volume
// exported at "/" as VFS, "/fsap-…" made it exit FATAL
// ("can't create directory on non-PSEUDO FSAL"), so the mount target ended up
// with no export at all. The graft succeeds when the name already exists in
// the volume, so the start script must create one anchor directory per access
// point before the daemon starts.
func TestGaneshaStartScript_anchorsAccessPointPseudoPaths(t *testing.T) {
	script := ganeshaStartScript([]ganeshaExport{
		{ID: 1, Path: nfsExportRoot, Pseudo: "/"},
		{ID: 2, Path: nfsExportRoot + "/app/data", Pseudo: "/fsap-1111"},
		{ID: 3, Path: nfsExportRoot, Pseudo: "/fsap-2222"},
	})

	for _, want := range []string{
		shQuote(nfsExportRoot + "/fsap-1111"),
		shQuote(nfsExportRoot + "/fsap-2222"),
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("start script must anchor %s in the volume:\n%s", want, script)
		}
	}
	// The anchors have to exist before ganesha.nfsd parses the config.
	anchor := strings.Index(script, shQuote(nfsExportRoot+"/fsap-1111"))
	start := strings.Index(script, "exec ganesha.nfsd")
	if anchor < 0 || start < 0 || anchor > start {
		t.Fatalf("anchors must be created before the daemon starts:\n%s", script)
	}
	// The root export is the volume itself and needs no anchor.
	if strings.Contains(script, "mkdir -p '/export/'") {
		t.Fatalf("root export must not be anchored:\n%s", script)
	}
}

// ─── Readiness probe ──────────────────────────────────────────────────────────

// TestNFSReadiness_nullRPC checks the probe against a hand-rolled server that
// answers one NFSv4 NULL call, and against one that accepts the connection but
// says nothing — a bare TCP dial cannot tell those apart.
func TestNFSReadiness_nullRPC(t *testing.T) {
	t.Run("answers", func(t *testing.T) {
		ln := nfsNullResponder(t, true)
		if err := nfsNullPing(context.Background(), ln.Addr().String(), time.Second); err != nil {
			t.Fatalf("expected the probe to succeed: %v", err)
		}
	})
	t.Run("silent", func(t *testing.T) {
		ln := nfsNullResponder(t, false)
		if err := nfsNullPing(context.Background(), ln.Addr().String(), 200*time.Millisecond); err == nil {
			t.Fatal("expected the probe to fail against a socket that never replies")
		}
	})
	t.Run("closed", func(t *testing.T) {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("listen: %v", err)
		}
		addr := ln.Addr().String()
		ln.Close()
		if err := nfsNullPing(context.Background(), addr, 200*time.Millisecond); err == nil {
			t.Fatal("expected the probe to fail against a closed port")
		}
	})
}

// TestNFSExport_wedgedExportMarksMountTargetError covers the reporting side of
// a broken export: a container that started and then died (a FATAL config
// error, say) must not leave the mount target claiming "available". The
// warning alone is too easy to miss — DescribeMountTargets has to say "error".
func TestNFSExport_wedgedExportMarksMountTargetError(t *testing.T) {
	fd := newFakeNFSDaemon(t)
	mock := clock.NewMock()
	svc := newNFSTestServiceWithClock(t, fd, true, mock, zap.NewNop())
	ctx := context.Background()

	// A mock clock leaves zero-delay lifecycle timers pending, so nudge it
	// until the file system is available and can take a mount target.
	fsID := seedFileSystem(t, svc, ctx, "nfs-wedged")
	eventually(t, "file system available", func() bool {
		mock.Add(time.Millisecond)
		fs, found, err := svc.getFileSystem(ctx, "us-east-1", fsID)
		return err == nil && found && fs.LifeCycleState == stateAvailable
	})

	mt, aerr := svc.createMountTargetTyped(ctx, &createMountTargetRequest{
		FileSystemId: fsID, SubnetId: "subnet-wedged",
	})
	if aerr != nil {
		t.Fatalf("CreateMountTarget: %v", aerr)
	}
	awaitExport(t, svc, ctx, "us-east-1", mt.MountTargetId)

	// Nothing is listening on the published port, so every probe fails. Burn
	// the retry budget on the mock clock rather than in real time.
	var final string
	eventually(t, "readiness budget exhausted", func() bool {
		if svc.scheduler.PendingCount() > 0 {
			mock.Add(nfsReadyInterval)
		}
		rec, found, err := svc.getMountTarget(ctx, "us-east-1", mt.MountTargetId)
		if err != nil || !found || rec.LifeCycleState == stateCreating {
			return false
		}
		final = rec.LifeCycleState
		return true
	})
	if final != stateError {
		t.Fatalf("an export that never answered must leave the mount target in %q, got %q", stateError, final)
	}
}

// TestSettleMountTarget_transitions pins the lifecycle moves the export
// plumbing may make. "error" has to be recoverable: reconciliation starts a
// fresh export for a mount target whose container is gone, and a mount target
// that failed before a restart must be able to come back.
func TestSettleMountTarget_transitions(t *testing.T) {
	cases := []struct{ name, from, to, want string }{
		{"creating settles available", stateCreating, stateAvailable, stateAvailable},
		{"creating settles error", stateCreating, stateError, stateError},
		{"error recovers when an export finally answers", stateError, stateAvailable, stateAvailable},
		{"available is not downgraded by a straggler", stateAvailable, stateError, stateAvailable},
		{"deleting is left alone", stateDeleting, stateAvailable, stateDeleting},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Given: a mount target in the starting state
			svc := newNFSTestService(t, nil, true)
			ctx := context.Background()
			rec := &mountTargetRecord{
				MountTargetId: "fsmt-1111111111111111a", FileSystemId: "fs-1111111111111111a",
				LifeCycleState: tc.from,
			}
			if err := svc.putMountTarget(ctx, "us-east-1", rec); err != nil {
				t.Fatalf("seed mount target: %v", err)
			}

			// When: the export plumbing settles it
			svc.settleMountTarget(ctx, "us-east-1", rec.MountTargetId, tc.to)

			// Then: only the permitted transitions took effect
			got, found, err := svc.getMountTarget(ctx, "us-east-1", rec.MountTargetId)
			if err != nil || !found {
				t.Fatalf("get mount target: found=%v err=%v", found, err)
			}
			if got.LifeCycleState != tc.want {
				t.Fatalf("%s → %s: want %q, got %q", tc.from, tc.to, tc.want, got.LifeCycleState)
			}
		})
	}
}

// nfsNullResponder listens on a random port and, when reply is true, answers
// the first record-marked RPC call with a MSG_ACCEPTED/SUCCESS reply echoing
// the caller's XID.
func nfsNullResponder(t *testing.T, reply bool) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		hdr := make([]byte, 4)
		if _, err := io.ReadFull(conn, hdr); err != nil {
			return
		}
		n := int(be32(hdr) &^ 0x80000000)
		body := make([]byte, n)
		if _, err := io.ReadFull(conn, body); err != nil {
			return
		}
		if !reply {
			<-time.After(time.Second)
			return
		}
		out := make([]byte, 0, 24)
		out = append(out, body[:4]...)            // xid, echoed
		out = append(out, 0, 0, 0, 1)             // msg_type: REPLY
		out = append(out, 0, 0, 0, 0)             // reply_stat: MSG_ACCEPTED
		out = append(out, 0, 0, 0, 0, 0, 0, 0, 0) // verf: AUTH_NONE, length 0
		out = append(out, 0, 0, 0, 0)             // accept_stat: SUCCESS
		frame := binary.BigEndian.AppendUint32(nil, 0x80000000|uint32(len(out)))
		conn.Write(append(frame, out...)) //nolint:errcheck
	}()
	return ln
}

// The export sweep's scoping, the container-side counterpart of
// TestReconcileVolumes_leavesVolumesOwnedByAnotherInstance. Removing another
// instance's containers does not destroy data the way sweeping its volumes
// would, but it takes a live NFS export out from under whatever is mounting
// it — and the helper branch, which matches any managed EFS container that is
// not an export, would collect its in-flight materialization runs too.
func TestReconcileExports_leavesContainersOwnedByAnotherInstance(t *testing.T) {
	fd := newFakeNFSDaemon(t)
	svc := newNFSTestService(t, fd, true)
	ctx := context.Background()

	foreignExport := docker.ManagedLabels(serviceName, "fsmt-zzzzzzzzzzzzzzzzz")
	foreignExport[docker.LabelInstance] = "11111111-2222-3333-4444-555555555555"
	foreignHelper := docker.ManagedLabels(serviceName, volumeName("fs-zzzzzzzzzzzzzzzzz"))
	foreignHelper[docker.LabelInstance] = "11111111-2222-3333-4444-555555555555"

	fd.mu.Lock()
	fd.listed = []docker.ContainerSummary{
		{
			ID:     "ctr-other-export",
			Names:  []string{"/" + nfsContainerName("fsmt-zzzzzzzzzzzzzzzzz")},
			State:  "running",
			Labels: foreignExport,
		},
		{
			ID:     "ctr-other-helper",
			Names:  []string{"/overcast-efs-mkdir-xyz"},
			State:  "running",
			Labels: foreignHelper,
		},
		{
			// Predates the identity label: owner unestablishable, so untouched.
			ID:     "ctr-unlabelled",
			Names:  []string{"/" + nfsContainerName("fsmt-yyyyyyyyyyyyyyyyy")},
			State:  "running",
			Labels: docker.ManagedLabels(serviceName, "fsmt-yyyyyyyyyyyyyyyyy"),
		},
	}
	fd.mu.Unlock()

	svc.reconcileExports(ctx)

	if removed := fd.removedContainers(); len(removed) != 0 {
		t.Fatalf("swept containers this instance does not own: %v", removed)
	}
}
