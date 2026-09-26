package docker

import (
	"maps"
	"sync/atomic"
)

// Owner labels identify the *process* that created a Docker resource, as
// opposed to the Overcast instance that manages it (LabelInstance). They exist
// for one caller: a test binary, whose in-process Overcast instances each get a
// fresh data directory and so a fresh LabelInstance. When such a binary is
// killed — most often by `go test -timeout` — its t.Cleanup never runs, no
// later instance ever resolves to the same identity, and the containers it
// started (ECS tasks, the ECR registry, Lambda functions) keep running on a
// daemon shared by every other session on the machine. The owner labels let
// the next test binary on that machine tell such an orphan from a resource a
// live test is still using: see tests/helpers.ReapOrphanedTestContainers.
//
// Production never sets them, so a resource that carries them was made by a
// test, and a reaper keyed on them can never touch anything a user owns.
const (
	// LabelTestPID is the process ID of the test binary that created the
	// resource.
	LabelTestPID = "overcast.test.pid"
	// LabelTestHost is the hostname that process ran on. A PID only means
	// something on the host that issued it: a daemon can be shared by
	// processes on different hosts (Docker Desktop serves Windows and WSL
	// alike), and a PID from one says nothing about liveness on the other.
	LabelTestHost = "overcast.test.host"
	// LabelTestStarted is when the owning process started, in Unix seconds —
	// the backstop for a PID the operating system has since reused.
	LabelTestStarted = "overcast.test.started"
)

var ownerLabels atomic.Pointer[map[string]string]

// SetOwnerLabels installs labels that every container this process creates
// carries from now on, in addition to the labels the creating service sets.
//
// Containers only, deliberately. A volume outlives the process that made it by
// design — the Lambda init volume is shared by every process running the same
// init, and a registry's data volume by every run on its port — so an owner
// label on a volume would hand it to the next test binary's reaper while other
// processes still mount it. And an idle orphaned volume costs disk, not the CPU
// an orphaned running container burns on a daemon every session shares.
//
// Passing nil clears them. It is process-wide by design — a test binary
// declares its ownership once, for every Overcast instance it starts — so a
// test that changes it must not run in parallel with one that creates.
//
// Owner labels only ever add: a key the creating service sets keeps the
// service's value.
func SetOwnerLabels(labels map[string]string) {
	if len(labels) == 0 {
		ownerLabels.Store(nil)
		return
	}
	cp := maps.Clone(labels)
	ownerLabels.Store(&cp)
}

// withOwnerLabels returns labels plus the owner labels, as a new map. The
// caller's map is never modified, and is returned as-is when no owner labels
// are set, so production pays nothing.
func withOwnerLabels(labels map[string]string) map[string]string {
	owner := ownerLabels.Load()
	if owner == nil {
		return labels
	}
	out := make(map[string]string, len(labels)+len(*owner))
	maps.Copy(out, *owner)
	maps.Copy(out, labels)
	return out
}

// withOwnerLabelsRequest returns req with the owner labels merged into its
// container labels. It copies what it changes — the request and its embedded
// config — so the caller's request, which some services keep and reuse, is
// never modified.
func withOwnerLabelsRequest(req *CreateContainerRequest) *CreateContainerRequest {
	if ownerLabels.Load() == nil {
		return req
	}
	out := CreateContainerRequest{}
	if req != nil {
		out = *req
	}
	cfg := ContainerConfig{}
	if out.ContainerConfig != nil {
		cfg = *out.ContainerConfig
	}
	cfg.Labels = withOwnerLabels(cfg.Labels)
	out.ContainerConfig = &cfg
	return &out
}
