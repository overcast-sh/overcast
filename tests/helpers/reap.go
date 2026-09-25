package helpers

import (
	"context"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/overcast-sh/overcast/internal/docker"
)

// Orphaned test containers.
//
// A test binary's in-process Overcast instances start real containers — ECS
// tasks and their pause containers, the ECR registry, Lambda functions — and
// t.Cleanup removes them. A binary that is killed never runs t.Cleanup: `go
// test -timeout` panics the process, and so does an interrupt. What it started
// keeps running on a Docker daemon that, on a developer machine, every other
// session shares, and nothing ever claims it, because each test server's
// data directory — the identity Overcast's own sweeps key on
// (docker.LabelInstance) — was a t.TempDir no later run will see again.
//
// This is the problem testcontainers solves with Ryuk, a sidecar that reaps a
// session's containers when its connection drops. The equivalent here needs no
// sidecar: every container a test binary creates carries owner labels naming
// its process (docker.SetOwnerLabels, installed below), and the first
// Docker-backed test server in the next binary removes anything whose owner is
// provably gone. It reaps containers — the running processes an orphan keeps
// burning CPU with — and never volumes, which may be shared (see
// docker.SetOwnerLabels). The cost of not having a sidecar is latency: an
// orphan lives until the next Docker-backed test run on the machine, and on a
// machine where tests run all day that is minutes.

// orphanMaxAge is the backstop for a PID the operating system has reused: a
// resource whose owner started longer ago than this is an orphan whatever the
// PID now names. The longest `go test -timeout` this repo configures is 1200s
// (Makefile, .github/workflows/test.yml); two hours is six times that.
const orphanMaxAge = 2 * time.Hour

// processStarted is this test binary's start, as close as a package variable
// initialiser can get to it.
var processStarted = time.Now()

func init() {
	host, _ := os.Hostname()
	docker.SetOwnerLabels(map[string]string{
		docker.LabelTestPID:     strconv.Itoa(os.Getpid()),
		docker.LabelTestHost:    host,
		docker.LabelTestStarted: strconv.FormatInt(processStarted.Unix(), 10),
	})
}

// owner is who a resource's owner labels say created it, and what this
// process can establish about whether that creator is still running.
type owner struct {
	pid     int
	host    string
	started time.Time
}

// ownerOf reads the owner labels. ok is false when the labels do not identify
// a process — a resource that merely carries the key, or carries a garbled
// value — and such a resource is never reaped: absence of proof is not proof.
func ownerOf(labels map[string]string) (owner, bool) {
	pid, err := strconv.Atoi(labels[docker.LabelTestPID])
	if err != nil || pid <= 0 {
		return owner{}, false
	}
	secs, err := strconv.ParseInt(labels[docker.LabelTestStarted], 10, 64)
	if err != nil {
		return owner{}, false
	}
	return owner{pid: pid, host: labels[docker.LabelTestHost], started: time.Unix(secs, 0)}, true
}

// orphaned decides whether a resource with these labels was left behind by a
// test process that is no longer running.
//
//   - This process's own resources are never orphans; a live test owns them.
//   - A resource older than orphanMaxAge is, whoever made it: no test runs
//     that long, and it is the only answer available for a reused PID or for
//     an owner on another host.
//   - Otherwise liveness decides, but only for an owner on this host. A PID
//     from another host — a WSL test beside a Windows one on the same Docker
//     Desktop, or a test inside a container with the socket mounted — says
//     nothing about any process here.
func orphaned(labels map[string]string, now time.Time, self owner, alive func(pid int) bool) bool {
	o, ok := ownerOf(labels)
	if !ok {
		return false
	}
	if o.pid == self.pid && o.host == self.host && o.started.Equal(self.started) {
		return false
	}
	if now.Sub(o.started) > orphanMaxAge {
		return true
	}
	return o.host == self.host && !alive(o.pid)
}

// ReapOrphanedTestContainers removes every container a test process that is no
// longer running left behind on the daemon, and reports how many it removed.
// Volumes carry no owner labels and are never reaped — see
// docker.SetOwnerLabels for why. It is best-effort: a container that refuses
// removal is left for the next run, and the only error returned is a failure
// to list.
func ReapOrphanedTestContainers(ctx context.Context, dc *docker.Client) (int, error) {
	host, _ := os.Hostname()
	self := owner{pid: os.Getpid(), host: host, started: time.Unix(processStarted.Unix(), 0)}
	now := time.Now()
	removed := 0

	containers, err := dc.ListContainersWithLabel(ctx, docker.LabelTestPID)
	if err != nil {
		return 0, err
	}
	for _, c := range containers {
		if orphaned(c.Labels, now, self, processAlive) && dc.RemoveContainer(ctx, c.ID, true) == nil {
			removed++
		}
	}
	return removed, nil
}

var reapOnce sync.Once

// reapOrphansOnce runs ReapOrphanedTestContainers the first time a test binary
// starts a Docker-backed server. Once per binary is enough — orphans only
// appear when a binary dies — and a daemon that is not reachable is not an
// error here: the test that needs it will skip or fail on its own terms.
func reapOrphansOnce() {
	reapOnce.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if DockerAvailable(ctx) != nil {
			return
		}
		_, _ = ReapOrphanedTestContainers(ctx, docker.NewClient(TestDockerSocket(), nil))
	})
}
