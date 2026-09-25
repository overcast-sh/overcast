package helpers

// Internal because the decision is the thing worth testing: which resources a
// dead test binary left behind, and — more important — which ones a live test
// still owns and must never lose to another binary's sweep.

import (
	"context"
	"os"
	"os/exec"
	"strconv"
	"testing"
	"time"

	"github.com/overcast-sh/overcast/internal/docker"
)

func ownerLabels(pid int, host string, started time.Time) map[string]string {
	return map[string]string{
		docker.LabelTestPID:     strconv.Itoa(pid),
		docker.LabelTestHost:    host,
		docker.LabelTestStarted: strconv.FormatInt(started.Unix(), 10),
	}
}

func TestOrphaned(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	self := owner{pid: 100, host: "here", started: now.Add(-time.Minute)}
	alive := func(pid int) bool { return pid == 100 || pid == 200 }

	tests := []struct {
		name   string
		labels map[string]string
		want   bool
	}{
		{"this process's own resource", ownerLabels(100, "here", self.started), false},
		{"a dead process on this host", ownerLabels(300, "here", now.Add(-5*time.Minute)), true},
		{"a live process on this host", ownerLabels(200, "here", now.Add(-5*time.Minute)), false},
		{"another host, recent: liveness is unknowable", ownerLabels(300, "elsewhere", now.Add(-5*time.Minute)), false},
		{"another host, past the age cap", ownerLabels(300, "elsewhere", now.Add(-orphanMaxAge-time.Minute)), true},
		{"a live PID past the age cap is a reused PID", ownerLabels(200, "here", now.Add(-orphanMaxAge-time.Minute)), true},
		{"this PID from an earlier process is not this process", ownerLabels(100, "here", now.Add(-orphanMaxAge-time.Minute)), true},
		{"a garbled PID is never reaped", map[string]string{docker.LabelTestPID: "x", docker.LabelTestStarted: "1"}, false},
		{"a missing start time is never reaped", map[string]string{docker.LabelTestPID: "300", docker.LabelTestHost: "here"}, false},
		{"no owner labels at all", map[string]string{docker.LabelManaged: "true"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// When: the reaper judges a resource with these labels.
			got := orphaned(tt.labels, now, self, alive)

			// Then: only a provably abandoned resource is an orphan.
			if got != tt.want {
				t.Fatalf("orphaned(%v) = %v, want %v", tt.labels, got, tt.want)
			}
		})
	}
}

// exitedPID returns the PID of a process that has already exited.
func exitedPID(t *testing.T) int {
	t.Helper()
	// The test binary itself, told to run no tests: it exists on every
	// platform and exits at once.
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	if err := cmd.Run(); err != nil {
		t.Fatalf("run helper process: %v", err)
	}
	return cmd.Process.Pid
}

func TestProcessAlive(t *testing.T) {
	// Given: this process, and one that has exited.
	dead := exitedPID(t)

	// Then: liveness tells them apart.
	if !processAlive(os.Getpid()) {
		t.Fatal("processAlive(self) = false")
	}
	if processAlive(dead) {
		t.Fatalf("processAlive(%d) = true for an exited process", dead)
	}
}

func TestReapOrphanedTestContainers_removesOnlyWhatADeadProcessLeft(t *testing.T) {
	SkipWithoutDocker(t)
	ctx := context.Background()
	dc := docker.NewClient(TestDockerSocket(), nil)
	const image = "busybox:1.36"
	PullOrSkip(t, dc, image)

	host, _ := os.Hostname()
	run := strconv.FormatInt(time.Now().UnixNano(), 36)

	// create starts a container carrying exactly these labels. The owner
	// labels installed by init are cleared for the create, so the container
	// says only what the test says about it.
	create := func(name string, labels map[string]string) string {
		t.Helper()
		docker.SetOwnerLabels(nil)
		defer docker.SetOwnerLabels(ownerLabels(os.Getpid(), host, processStarted))
		id, err := dc.CreateContainer(ctx, name, &docker.CreateContainerRequest{
			ContainerConfig: &docker.ContainerConfig{Image: image, Cmd: []string{"sleep", "300"}, Labels: labels},
		})
		if err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
		t.Cleanup(func() { _ = dc.RemoveContainer(context.Background(), id, true) })
		if err := dc.StartContainer(ctx, id); err != nil {
			t.Fatalf("start %s: %v", name, err)
		}
		return id
	}

	// Given: a running container a dead test process left behind, and one
	// this process owns.
	orphan := create("overcast-reap-orphan-"+run, ownerLabels(exitedPID(t), host, time.Now().Add(-time.Minute)))
	mine := create("overcast-reap-mine-"+run, ownerLabels(os.Getpid(), host, processStarted))

	// When: the reaper runs.
	if _, err := ReapOrphanedTestContainers(ctx, dc); err != nil {
		t.Fatalf("ReapOrphanedTestContainers: %v", err)
	}

	// Then: the orphan is gone and this process's container is untouched.
	if info, _ := dc.GetContainerByName(ctx, "overcast-reap-orphan-"+run); info != nil {
		t.Fatalf("orphan %s was not reaped", orphan)
	}
	if info, err := dc.GetContainerByName(ctx, "overcast-reap-mine-"+run); err != nil || info == nil {
		t.Fatalf("live owner's container %s was reaped (err %v)", mine, err)
	}
}
