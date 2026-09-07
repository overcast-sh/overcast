package ecs

// handler_tasks.go — RunTask, StopTask, DescribeTasks, ListTasks handlers, and
// the locked read-modify-write of a single task record every writer goes
// through (lockTask).

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"go.uber.org/zap"

	"github.com/overcast-sh/overcast/internal/config"
	"github.com/overcast-sh/overcast/internal/containerendpoint"
	"github.com/overcast-sh/overcast/internal/dataplane"
	"github.com/overcast-sh/overcast/internal/docker"
	"github.com/overcast-sh/overcast/internal/events"
	"github.com/overcast-sh/overcast/internal/protocol"
	"github.com/overcast-sh/overcast/internal/serviceutil"
)

// taskLockStripes is how many mutexes lockTask spreads task records over.
const taskLockStripes = 64

// stoppedTaskRetention is the deterministic end of ECS's documented minimum
// visibility window. AWS guarantees that stopped tasks remain describable for
// at least one hour, while its console displays them for one hour:
// https://docs.aws.amazon.com/AmazonECS/latest/APIReference/API_DescribeTasks.html
const stoppedTaskRetention = time.Hour

// AWS's StopTask response example uses this reason when the optional caller
// reason is omitted:
// https://docs.aws.amazon.com/AmazonECS/latest/APIReference/API_StopTask.html
const defaultUserStoppedReason = "Task stopped by user"

// lockTask serialises the read-modify-write of one task's record, returning the
// function that releases it. Same shape and same reason as lockService: a task
// is stored as one JSON blob, so every writer reads the whole record, edits it
// and writes the whole record back, and two of them overlapping means the
// second silently discards the first's edit.
//
// The pairing that made this necessary is a stop landing on a task that is
// still coming up. Every stop path cancels the task's pending PROVISIONING →
// RUNNING transition before it writes STOPPED, but cancelling only stops a
// timer that has not fired: a transition already inside its callback has read
// the task as PROVISIONING, and its write of RUNNING lands after the STOPPED
// one and resurrects the task. The transition's LastStatus guard narrows that
// window and cannot close it, because the read and the write are not one step.
// Holding this lock across both is what closes it.
//
// Striped, where lockService keeps one mutex per service, because the two have
// different lifetimes: services are few and long-lived, while a service in a
// crash loop replaces its tasks for as long as it runs, so a mutex per task ID
// would grow for the life of the process. Two tasks that share a stripe
// serialise needlessly; a critical section here is one read and one write of a
// single record, with container teardown deliberately left outside it.
//
// Region is not part of the key. Task IDs are UUIDs, so cluster and ID identify
// a task on their own, and leaving the region out lets the Docker exit notifier
// take the lock before it has scanned for the region the task is stored under.
//
// Lock ordering is service-then-task, never the reverse: stopServiceTasks takes
// a task lock while reconcile holds the service lock, so anything holding a
// task lock must release it before it locks a service.
func (h *Handler) lockTask(clusterName, taskID string) func() {
	mu := &h.taskLocks[taskLockStripe(clusterName+"/"+taskID)]
	mu.Lock()
	return mu.Unlock
}

// taskLockStripe picks a task's stripe by FNV-1a. Hand-rolled rather than
// seeded (hash/maphash) so a zero-value Handler — which several tests build —
// needs no initialisation to be safe to lock.
func taskLockStripe(key string) uint32 {
	const (
		offset32 = 2166136261
		prime32  = 16777619
	)
	sum := uint32(offset32)
	for i := 0; i < len(key); i++ {
		sum ^= uint32(key[i])
		sum *= prime32
	}
	return sum % taskLockStripes
}

// taskStop is what a stop path records on a task's record.
type taskStop struct {
	reason string
	code   string
}

// stopTaskRecord writes a stop onto one task's record under lockTask, and
// returns the stored task so the caller can tear its containers down outside
// the lock. changed is false when there was nothing left to stop.
//
// It re-reads the task inside the lock rather than writing back the copy the
// caller already had: that copy predates the lock, so it can be missing a
// transition to RUNNING that landed in between, and writing it back would undo
// more than the status.
//
// A task that is already STOPPED is left exactly as it is. One that died on its
// own between the caller's read and this write keeps the stop code and reason
// it died with — overwriting them would file a container that exited on its own
// under the stopper's name and take a deployment failure off the books, which
// is the same mistake the exit notifier avoids in the other direction.
func (h *Handler) stopTaskRecord(ctx context.Context, clusterName, taskID string, stop taskStop) (task *Task, changed bool, aerr *protocol.AWSError) {
	defer h.lockTask(clusterName, taskID)()

	task, aerr = h.store.getTask(ctx, clusterName, taskID)
	if aerr != nil || task == nil {
		return nil, false, aerr
	}
	if task.LastStatus == "STOPPED" {
		h.deleteStoppedTaskTags(ctx, task)
		return task, false, nil
	}
	if stop.code == "UserInitiated" && stop.reason == "" {
		stop.reason = defaultUserStoppedReason
	}

	task.LastStatus = "STOPPED"
	task.DesiredStatus = "STOPPED"
	task.StoppedReason = stop.reason
	task.StopCode = stop.code
	stoppedAt := h.clk.Now().Unix()
	task.StoppedAt = &stoppedAt
	task.StoppingAt = &stoppedAt
	for i := range task.Containers {
		task.Containers[i].LastStatus = "STOPPED"
	}
	if aerr := h.store.putTask(ctx, task); aerr != nil {
		return nil, false, aerr
	}
	h.deleteStoppedTaskTags(ctx, task)
	return task, true, nil
}

func (h *Handler) deleteStoppedTaskTags(ctx context.Context, task *Task) {
	if err := h.store.store.Delete(ctx, nsTags, serviceutil.RegionKey(h.store.region(ctx), task.TaskArn)); err != nil && h.log != nil {
		h.log.Warn("ecs: failed to delete stopped task tags",
			zap.String("task", task.TaskArn),
			zap.String("error", err.Error()))
	}
}

func (h *Handler) stoppedTaskExpired(task *Task) bool {
	if task == nil || task.LastStatus != "STOPPED" || task.StoppedAt == nil {
		return false
	}
	return !h.clk.Now().Before(time.Unix(*task.StoppedAt, 0).Add(stoppedTaskRetention))
}

// getVisibleTask applies ECS's stopped-task retention contract and lazily
// removes expired records so both memory and persistent stores stay bounded.
func (h *Handler) getVisibleTask(ctx context.Context, clusterName, taskID string) (*Task, *protocol.AWSError) {
	task, aerr := h.store.getTask(ctx, clusterName, taskID)
	if aerr != nil || !h.stoppedTaskExpired(task) {
		return task, aerr
	}
	if aerr := h.reapTaskCompanions(ctx, task); aerr != nil {
		return nil, aerr
	}
	if aerr := h.store.deleteTask(ctx, clusterName, taskID); aerr != nil {
		return nil, aerr
	}
	return nil, nil
}

func (h *Handler) listVisibleTasks(ctx context.Context, clusterName string) ([]Task, *protocol.AWSError) {
	tasks, aerr := h.store.listTasks(ctx, clusterName)
	if aerr != nil {
		return nil, aerr
	}
	visible := make([]Task, 0, len(tasks))
	for i := range tasks {
		if !h.stoppedTaskExpired(&tasks[i]) {
			visible = append(visible, tasks[i])
			continue
		}
		if aerr := h.reapTaskCompanions(ctx, &tasks[i]); aerr != nil {
			return nil, aerr
		}
		if aerr := h.store.deleteTask(ctx, clusterName, extractTaskID(tasks[i].TaskArn)); aerr != nil {
			return nil, aerr
		}
	}
	return visible, nil
}

// reapTaskCompanions deletes the keys that hang off a task record without being
// part of it: its tags, and its containers' retained log tails.
//
// The logs are the reason this is a function rather than the one line it was.
// Nothing else expires that namespace — no TTL, no reaper anywhere — so left
// alone it grows one entry per container of every task a service ever placed,
// for the life of the process. They are unreachable well before that in any
// case: GetTaskContainerLogs resolves the task first, so an entry outliving its
// record can only be read as a 404.
func (h *Handler) reapTaskCompanions(ctx context.Context, task *Task) *protocol.AWSError {
	if err := h.store.store.Delete(ctx, nsTags, serviceutil.RegionKey(h.store.region(ctx), task.TaskArn)); err != nil {
		return protocol.Wrap(protocol.ErrInternalError, err)
	}
	return h.store.deleteTaskContainerLogs(ctx, task)
}

// RunTask handles AmazonEC2ContainerServiceV20141113.RunTask.
func (h *Handler) RunTask(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Cluster              string                `json:"cluster"`
		TaskDefinition       string                `json:"taskDefinition"`
		Count                int                   `json:"count"`
		LaunchType           string                `json:"launchType"`
		NetworkConfiguration *NetworkConfiguration `json:"networkConfiguration"`
		PlatformVersion      string                `json:"platformVersion"`
		Overrides            *TaskOverride         `json:"overrides"`
		Group                string                `json:"group"`
		StartedBy            string                `json:"startedBy"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}

	if req.Cluster == "" {
		req.Cluster = "default"
	}
	if req.Count < 1 {
		req.Count = 1
	}

	clusterName := extractClusterName(req.Cluster)

	// Verify cluster exists.
	cluster, aerr := h.store.getCluster(r.Context(), clusterName)
	if aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}

	// Resolve task definition.
	family, revision, hasRevision := parseTaskDefRef(req.TaskDefinition)
	var td *TaskDefinition
	if hasRevision {
		td, aerr = h.store.getTaskDefinition(r.Context(), family, revision)
	} else {
		td, aerr = h.store.getLatestTaskDefinition(r.Context(), family)
	}
	if aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}

	// awsvpc networking is required by the task definition's networkMode, so
	// this can only be checked once the task definition has been resolved.
	if aerr := validateAwsvpcNetworkConfiguration(td, req.LaunchType, req.NetworkConfiguration); aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}

	// Determine platform version for Fargate.
	platformVersion := req.PlatformVersion
	if platformVersion == "" && req.LaunchType == "FARGATE" {
		platformVersion = "LATEST"
	}

	placement, placementErr := h.resolveAwsvpcPlacement(r.Context(), req.NetworkConfiguration, "awsvpc tasks")
	if placementErr != nil {
		protocol.WriteJSONError(w, r, placementErr)
		return
	}

	tasks := make([]Task, 0, req.Count)
	for i := 0; i < req.Count; i++ {
		// A task that fails to start is returned like any other: AWS reports
		// the ARN from RunTask and the failure through the task's own STOPPED
		// state, reserving the failures array for placement rejections.
		task, aerr, _ := h.launchTask(r.Context(), taskLaunchSpec{
			clusterName:     clusterName,
			clusterArn:      cluster.ClusterArn,
			td:              td,
			launchType:      req.LaunchType,
			platformVersion: platformVersion,
			group:           req.Group,
			startedBy:       req.StartedBy,
			overrides:       req.Overrides,
			netCfg:          req.NetworkConfiguration,
			placement:       placement,
			ordinal:         i,
		})
		if aerr != nil {
			protocol.WriteJSONError(w, r, aerr)
			return
		}
		tasks = append(tasks, task.forWire())
	}

	protocol.WriteAWSJSON(w, r, http.StatusOK, map[string]any{
		"tasks":    tasks,
		"failures": []any{},
	}, "application/x-amz-json-1.1")
}

// startTaskContainers creates and starts Docker containers for all container
// definitions in a task. On success, task containers are updated with
// DockerIDs. beforeStart closes the publication race immediately before the
// first container can emit a die event; launchTask releases that guard only
// after the task record is stored. The RUNNING transition is queued there too.
//
// An awsvpc task gets one container more than it declares: the namespace its
// containers share, started first and recorded on the task. See task_netns.go.
//
// Placement is all-or-nothing: a task whose third container cannot be pulled
// has the two already running torn down before the error returns. Nothing
// downstream would do it otherwise — launchTask persists the task STOPPED, so
// no later pass walks its containers, and they would hold their ports and
// memory until the GC's shutdown sweep. A service crash-looping on a bad image
// leaks a fresh set on every replacement attempt.
func (h *Handler) startTaskContainers(ctx context.Context, task *Task, td *TaskDefinition, clusterName, taskID string, placement awsvpcPlacement, beforeStart func()) (retErr error) {
	// Resolve how task containers reach Overcast. The planes themselves are
	// created once by the Docker supervisor, before any client is handed to a
	// service, so there is nothing to ensure here.
	endpoint := h.containerEndpoint(ctx)

	// Build an override index by container name.
	overrides := make(map[string]*ContainerOverride)
	if task.Overrides != nil {
		for i := range task.Overrides.ContainerOverrides {
			co := &task.Overrides.ContainerOverrides[i]
			overrides[co.Name] = co
		}
	}

	// Hot-reload redirects and debug targets are resolved once for the task:
	// the tags belong to the task definition, so asking per container would
	// repeat the same store read and the same warnings for every container in
	// it. Both come before the namespace container below, which is where an
	// awsvpc task's debug ports are published.
	tags := h.taskDefinitionTags(ctx, td.TaskDefinitionArn)
	hotReload := h.hotReloadPaths(td, tags)
	debug := h.debugTargets(td, taskID, tags, overrides, hotReload)

	// An awsvpc task's containers all run in one network namespace, so it is
	// built before any of them — the task's whole networking hangs off it. See
	// task_netns.go.
	namespaceID := ""
	if taskSharesNetworkNamespace(td, task.LaunchType) {
		id, err := h.startTaskNamespaceContainer(ctx, task, td, clusterName, taskID, placement, endpoint, debug)
		if err != nil {
			return err
		}
		task.NetworkNamespaceID, namespaceID = id, id
		defer func() {
			// A task that could not be placed leaves nothing of itself running —
			// by the time the failure is returned, not at the GC's leisure,
			// which is also how unwindTaskContainers treats the application
			// containers. The namespace container outlives them by design, so
			// unwinding a failed start is the one path that has to take it down
			// explicitly.
			if retErr != nil {
				h.retireTaskNamespaceContainerNow(ctx, task)
				task.NetworkNamespaceID = ""
			}
		}()
	}

	// Resource ID for Docker labels: "clusterName/taskID" so the exit notifier
	// can look up the task.
	resourceID := clusterName + "/" + taskID

	// Volumes before containers: a mount naming a volume that does not exist
	// fails container creation, and provisioning once here rather than per
	// container keeps the daemon calls to a single pass. The failure is
	// attributed to the first container, which is the one whose creation would
	// have hit it.
	if err := h.provisionTaskVolumes(ctx, td, clusterName, taskID, hotReload); err != nil {
		name := ""
		if len(td.ContainerDefinitions) > 0 {
			name = td.ContainerDefinitions[0].Name
		}
		return containerFailure(name, "ecs: %w", err)
	}

	// Containers this call has put on the daemon, in the order they were put
	// there. A failure anywhere below unwinds all of them — including the one
	// being placed when it happened, which is why the index is recorded as soon
	// as the create returns rather than once the container is fully placed.
	//
	// Registered after the namespace container's own unwind above, so it runs
	// before it: the application containers are torn down first, and then the
	// namespace they were running inside.
	var placed []int
	defer func() {
		if retErr != nil {
			h.unwindTaskContainers(ctx, task, placed)
		}
	}()

	// Every failure below belongs to the container being placed when it happens,
	// and is returned naming it: the reason is recorded against that container
	// alone, as ECS does, not against every container in the task.
	for i, cd := range td.ContainerDefinitions {
		image := cd.Image

		// Pull the image (deduplicated). The puller's error already names the
		// image and the failure, so it is not wrapped again — this message
		// reaches the user as a task's stoppedReason.
		if err := h.puller.Ensure(ctx, image); err != nil {
			return containerFailure(cd.Name, "ecs: %w", err)
		}
		debug.setRemoteRoot(ctx, h, &td.ContainerDefinitions[i])

		// Build environment variables, including any resolved from Secrets
		// Manager or SSM via the definition's secrets.
		env := buildContainerEnv(cd, overrides[cd.Name], endpoint)
		env = append(env, h.secretEnv(ctx, cd)...)
		// Last, so the debugger appends to a NODE_OPTIONS or JAVA_TOOL_OPTIONS
		// the definition set rather than replacing it.
		env = debug.inject(cd.Name, env)

		// Build command.
		var cmd []string
		if co, ok := overrides[cd.Name]; ok && len(co.Command) > 0 {
			cmd = co.Command
		} else if len(cd.Command) > 0 {
			cmd = cd.Command
		}

		containerName := fmt.Sprintf("overcast-ecs-%s-%s-%s", clusterName, taskID[:8], cd.Name)

		mounts := h.containerMounts(ctx, td, &td.ContainerDefinitions[i], taskID, hotReload)

		ccfg := &docker.CreateContainerRequest{
			ContainerConfig: &docker.ContainerConfig{
				Image: image,
				Env:   env,
				Cmd:   cmd,
				// entryPoint and workingDirectory override the image's own, as
				// they do on ECS. A task definition that sets either and has it
				// dropped runs something other than what it asked for, silently.
				Entrypoint: cd.EntryPoint,
				WorkingDir: cd.WorkingDirectory,
				User:       cd.User,
				Labels:     mergeDockerLabels(h.instances.ManagedLabels(ctx, serviceName, resourceID), cd.DockerLabels),
			},
			// Keep the container through Docker's die event so the final bounded
			// log tail can be retained for post-mortem diagnostics. The exit
			// notifier schedules removal immediately after capturing it.
			HostConfig: &docker.HostConfig{AutoRemove: false,
				Privileged: cd.Privileged != nil && *cd.Privileged,
				Mounts:     mounts,
			},
		}
		h.applyTaskNetwork(ccfg, namespaceID, endpoint, portSurfaceFor(cd))
		if namespaceID == "" {
			// A container with a namespace of its own publishes its own debug
			// port; in a shared one it was published on the namespace container.
			debug.applyPortBinding(ccfg, cd.Name)
		}

		// The puller retries once when the image was removed behind our back
		// (docker rmi after the recorded pull) instead of failing until restart.
		dockerID, err := h.puller.CreateContainerWithRetry(ctx, containerName, ccfg)
		if err != nil {
			return containerFailure(cd.Name, "ecs: create container %s: %w", cd.Name, decorateBindMountError(err, mounts))
		}

		// Claim it on the task record straight away: the unwind resolves a
		// container's name through this to file its final output under, and a
		// container that has to be torn down before it is fully placed is
		// exactly the one whose output is worth keeping. RuntimeId, which is
		// AWS's own field, still waits for a container that is actually running.
		task.Containers[i].DockerID = dockerID
		placed = append(placed, i)

		// With TLS on, the task must trust the CA that minted Overcast's
		// certificate before its first SDK call. Same mechanism as function
		// code: CopyToContainer, because a dockerized Overcast has no host
		// path to bind-mount from.
		if caTar, caErr := endpoint.CABundleTar(); caErr != nil {
			h.log.ZapLogger().Warn("ecs: CA bundle unavailable; task TLS calls to Overcast will fail verification", zap.Error(caErr))
		} else if caTar != nil {
			if err := h.docker.CopyToContainer(ctx, dockerID, "/", bytes.NewReader(caTar)); err != nil {
				return containerFailure(cd.Name, "ecs: inject CA bundle into %s: %w", cd.Name, err)
			}
		}

		// A container in the task's shared namespace has no network attachments
		// of its own to make: the namespace container made every one of them
		// before this loop began, and this container inherits them by running
		// inside it. Everything below is for a task whose containers each have a
		// namespace of their own.
		//
		// Joining the data plane comes before the start, because an application
		// that dials a database in its first breath would otherwise race the
		// attachment; the VPC attachment comes after it, because it reads back
		// the address Docker assigned and that is not fixed until the container
		// runs.
		if namespaceID == "" {
			if err := h.joinTaskDataPlane(ctx, placement, dockerID); err != nil {
				return containerFailure(cd.Name, "ecs: container %s: %w", cd.Name, err)
			}
		}

		beforeStart()
		if err := h.docker.StartContainer(ctx, dockerID); err != nil {
			return containerFailure(cd.Name, "ecs: start container %s: %w", cd.Name, decorateBindMountError(err, mounts))
		}
		if namespaceID == "" && placement.networkID != "" {
			// Only the first container carries the task's ENI address: AWS
			// reports one privateIPv4Address per task, and Docker has given each
			// of these containers an address of its own.
			if err := h.attachTaskENI(ctx, task, placement, dockerID, i == 0); err != nil {
				return containerFailure(cd.Name, "ecs: connect container %s to VPC network %s: %w", cd.Name, placement.networkID, err)
			}
		}

		task.Containers[i].RuntimeId = dockerID

		// The debug port is reachable now the container runs. It is read from
		// whichever container owns the binding — this one, or the namespace
		// container it runs inside.
		if target, ok := debug[cd.Name]; ok {
			owner := namespaceID
			if owner == "" {
				owner = dockerID
			}
			h.bindDebugTarget(ctx, target, owner, dockerID)
		}

		// Ship this container's output to CloudWatch Logs when the task
		// definition asked for the awslogs driver. Started after the container
		// is running so the stream exists to attach to.
		h.startLogStreaming(ctx, dockerID, taskID, cd)
	}

	return nil
}

// stopTaskContainers tears down the Docker containers of a task a caller has
// stopped: every application container, and then the namespace container they
// shared, which nothing else removes.
//
// Both StopTask paths come through here — the JSON one and the typed one — so a
// task stopped over CBOR is torn down exactly as one stopped over JSON is.
func (h *Handler) stopTaskContainers(ctx context.Context, task *Task) {
	// A stopped task's debug ports go with it, before its containers do, so
	// nothing is dialled behind them meanwhile.
	h.releaseDebugTargets(task)
	if !h.dockerReady.Load() {
		return
	}
	for _, c := range task.Containers {
		if c.DockerID == "" {
			continue
		}
		if h.gc != nil {
			// The GC captures the container's output before it removes it — see
			// SetBeforeRemove, wired in SetDocker.
			h.gc.StopNow(c.DockerID)
			h.gc.ScheduleRemove(c.DockerID)
			continue
		}
		// Without one, this path owns the ordering itself, as
		// retireTaskContainers does: stop, capture, then remove.
		_ = h.docker.StopContainer(ctx, c.DockerID, 10)
		h.captureContainerLogs(ctx, task, c.DockerID)
		if !h.cfg.ECSKeepContainers {
			_ = h.docker.RemoveContainerForce(c.DockerID)
		}
	}
	// Last: it is the namespace the containers above were running in.
	h.retireTaskNamespaceContainer(ctx, task)
}

// unwindTaskContainers tears down the containers a placement had already put on
// the daemon when it failed, named by their index into the task's containers in
// the order they were placed. It is the launch-time counterpart of
// retireTaskContainers: a task that could not be placed leaves nothing running.
// An awsvpc task's namespace container is not among them — it is retired by its
// own defer in startTaskContainers, which runs after this one so the containers
// come down before the namespace they were running inside.
//
// The ordering is that function's, for that function's reason — stop, capture,
// then remove — and see there for why the capture cannot sit on either side of
// the pair instead. This path has the same thing to lose, and loses it to the
// same reader: an image that will not pull, or a port already taken, is exactly
// what somebody is trying to explain when they go looking for the output of the
// sidecar that did come up.
//
// Teardown runs in reverse, so a container is gone before whatever it was
// started alongside. Nothing here depends on that today — Overcast does not yet
// order startup by dependsOn — but a partial placement unwound in placement
// order tears down the containers a later one may still be talking to first.
//
// The Docker IDs stay on the task record afterwards, pointing at containers
// that are gone. That is deliberate: the emulator's task-log endpoint gates on
// a container having an ID before it will serve anything, retained copy
// included, so clearing them would 404 the very output this just captured.
// Nothing else is misled by them — every stop path tolerates a container Docker
// answers 404 for, which is also what it answers for one that exited an hour
// ago and has since been swept.
func (h *Handler) unwindTaskContainers(ctx context.Context, task *Task, placed []int) {
	if !h.dockerReady.Load() || h.docker == nil {
		return
	}
	log := h.log.WithRecorder(ctx)
	for n := len(placed) - 1; n >= 0; n-- {
		i := placed[n]
		dockerID := task.Containers[i].DockerID
		if dockerID == "" {
			continue
		}
		if err := h.docker.StopContainer(ctx, dockerID, 5); err != nil {
			log.Warn("ecs: unwind failed task: could not stop container",
				zap.String("container", dockerID), zap.Error(err))
		}
		h.captureContainerLogs(ctx, task, dockerID)
		if h.cfg != nil && h.cfg.ECSKeepContainers {
			// Kept deliberately for post-mortem inspection, and a launch that
			// failed is the post-mortem this flag is usually set for.
			continue
		}
		if err := h.docker.RemoveContainerForce(dockerID); err != nil {
			log.Warn("ecs: unwind failed task: could not remove container",
				zap.String("container", dockerID), zap.Error(err))
		}
	}
}

// mergeDockerLabels adds a container definition's dockerLabels to Overcast's
// own. Overcast's win: the GC and the exit notifier find containers by them.
func mergeDockerLabels(managed, extra map[string]string) map[string]string {
	if len(extra) == 0 {
		return managed
	}
	merged := make(map[string]string, len(managed)+len(extra))
	for k, v := range extra {
		merged[k] = v
	}
	for k, v := range managed {
		merged[k] = v
	}
	return merged
}

// efsMountSkipReason explains why an EFS volume could not be resolved, which
// is almost always the mode EFS is running in rather than anything wrong with
// the task definition. The three causes need different things from the reader,
// so they get different messages instead of one that lists all three.
func (h *Handler) efsMountSkipReason() (msg, hint string) {
	switch {
	case h.cfg != nil && h.cfg.EFSMode != config.EFSModeLive:
		return "ecs: EFS mount skipped — EFS is in mock mode, so file systems have no storage behind them",
			"the EFS control plane is fully emulated in mock mode (file systems, mount targets, access points, tags) — only the data plane is absent. Live mode is the default and backs each file system with a Docker volume tasks can actually read and write; drop OVERCAST_EFS_MODE=mock from this instance's environment to get it."
	case !h.dockerReady.Load():
		return "ecs: EFS mount skipped — no container runtime, so there is no volume to mount",
			"EFS live mode backs file systems with Docker volumes; start Overcast with a Docker socket available."
	default:
		return "ecs: EFS mount skipped — the file system or access point in the task definition was not found",
			"check that the efsVolumeConfiguration references a file system (and access point) that exists in this region."
	}
}

// volumeScopeLabel records whether an Overcast-created ECS volume lives for the
// task or outlives it. The sweep reads it: a shared-scope volume must survive
// the task that first mounted it, exactly as it does on a container instance,
// and deleting a developer's data because a task stopped is the one mistake
// here that cannot be undone.
const volumeScopeLabel = "overcast.ecs.volume-scope"

// volumeRegionLabel records the region the owning task is stored under, as
// provenance for anyone reading `docker volume inspect`: the managed resource
// ID is "cluster/taskID", and the same cluster and task ID in another region is
// a different task.
//
// Nothing in Overcast reads it. It was added when the orphan sweep resolved
// volumes back to task records and needed a region to do the lookup in; the
// sweep now asks the daemon which volumes are referenced instead, which is
// both simpler and correct across instances. Kept because the label is free
// and the resource ID alone is ambiguous to a human.
const volumeRegionLabel = "overcast.ecs.region"

// taskVolumeName names the Docker volume backing a task-lifetime volume — a
// scratch volume or a dockerVolumeConfiguration with task scope, which are the
// same object with different amounts of configuration.
func taskVolumeName(taskID, volumeName string) string {
	id := taskID
	if len(id) > 8 {
		id = id[:8]
	}
	return "overcast-ecs-task-" + id + "-" + volumeName
}

// dockerVolumeScope reads a dockerVolumeConfiguration's scope, defaulting to
// AWS's default of "task".
func dockerVolumeScope(dvc *DockerVolumeConfiguration) string {
	if dvc != nil && strings.EqualFold(dvc.Scope, "shared") {
		return "shared"
	}
	return "task"
}

// mountedVolumes returns the task definition's volumes that at least one
// container actually mounts, in declaration order. A declared but unmounted
// volume is not provisioned: it would be a Docker volume nothing can reach.
//
// Order matters for the caller: provisioning stops at the first failure, and
// iterating a map would make *which* failure a task reports vary between runs.
func mountedVolumes(td *TaskDefinition) []*TaskVolume {
	wanted := make(map[string]struct{})
	for i := range td.ContainerDefinitions {
		for _, mp := range td.ContainerDefinitions[i].MountPoints {
			wanted[mp.SourceVolume] = struct{}{}
		}
	}
	out := make([]*TaskVolume, 0, len(wanted))
	for i := range td.Volumes {
		v := &td.Volumes[i]
		if _, ok := wanted[v.Name]; ok {
			out = append(out, v)
		}
	}
	return out
}

// provisionTaskVolumes creates the Docker volumes a task's mounts need, before
// any container is created. Doing it here rather than per container keeps the
// daemon calls to one pass and leaves containerMounts a pure function.
//
// EFS volumes are not provisioned here — the EFS service owns their lifetime.
// A failure is returned rather than warned about: unlike a missing EFS mount,
// which degrades to a container writing into its own layer, a volume the task
// definition asked for and did not get is a task that will not do what it says.
func (h *Handler) provisionTaskVolumes(ctx context.Context, td *TaskDefinition, clusterName, taskID string, hotReload map[string]string) error {
	if h.docker == nil {
		return nil
	}
	for _, v := range mountedVolumes(td) {
		dvc := v.DockerVolumeConfiguration
		if v.EFSVolumeConfiguration != nil || (v.Host != nil && v.Host.SourcePath != "") {
			continue
		}
		// A redirected volume is a bind to a directory that already exists on
		// the host; there is no Docker volume to create for it.
		if hotReload[v.Name] != "" {
			continue
		}
		if dvc != nil && dockerVolumeScope(dvc) == "shared" {
			if err := h.provisionSharedVolume(ctx, v.Name, dvc); err != nil {
				return err
			}
			continue
		}
		opts := docker.VolumeOptions{
			Labels: h.volumeLabels(ctx, clusterName+"/"+taskID, "task", dvc),
		}
		if dvc != nil {
			opts.Driver = dvc.Driver
			opts.DriverOpts = dvc.DriverOpts
		}
		if err := h.docker.CreateVolumeWithOptions(ctx, taskVolumeName(taskID, v.Name), opts); err != nil {
			return fmt.Errorf("volume %s: %w", v.Name, err)
		}
	}
	return nil
}

// volumeLabels builds the labels an Overcast-created ECS volume carries: the
// managed set the sweep finds it by, the scope that decides whether it may ever
// be removed, the region that identifies its owner, and last the task
// definition's own labels — which are additive and never override Overcast's,
// since the sweep depends on those meaning what they say.
func (h *Handler) volumeLabels(ctx context.Context, resourceID, scope string, dvc *DockerVolumeConfiguration) map[string]string {
	managed := docker.ManagedLabels(serviceName, resourceID)
	managed[volumeScopeLabel] = scope
	managed[volumeRegionLabel] = h.store.region(ctx)
	var extra map[string]string
	if dvc != nil {
		extra = dvc.Labels
	}
	// Same precedence as a container's labels, and the same helper: Overcast's
	// win over the definition's.
	return mergeDockerLabels(managed, extra)
}

// provisionSharedVolume handles a shared-scope dockerVolumeConfiguration: the
// volume is named as the task definition names it, because on a container
// instance that is the name it has, and a pre-created volume is meant to be
// found by it. autoprovision=false requires it to already exist — AWS fails the
// placement otherwise, and so does this.
func (h *Handler) provisionSharedVolume(ctx context.Context, name string, dvc *DockerVolumeConfiguration) error {
	if !dvc.Autoprovision {
		exists, err := h.docker.VolumeExists(ctx, name)
		if err != nil {
			return fmt.Errorf("volume %s: %w", name, err)
		}
		if !exists {
			return fmt.Errorf("volume %s: shared volume does not exist and autoprovision is false; create it with `docker volume create %s` or set autoprovision", name, name)
		}
		// Adopted as found: not labelled, not owned, never swept.
		return nil
	}
	if err := h.docker.CreateVolumeWithOptions(ctx, name, docker.VolumeOptions{
		Driver:     dvc.Driver,
		DriverOpts: dvc.DriverOpts,
		Labels:     h.volumeLabels(ctx, name, "shared", dvc),
	}); err != nil {
		return fmt.Errorf("volume %s: %w", name, err)
	}
	return nil
}

// removeTaskVolumes removes the task-lifetime volumes placed for a task.
//
// The volumes are found by their labels rather than by re-reading the task
// definition: a definition can be deregistered while its tasks still run, and
// the labels are on the objects being removed. Shared-scope volumes carry a
// different scope label and are deliberately left — ECS never removes them
// either, and on a container instance they are exactly the volumes that are
// meant to outlive the task.
//
// Best-effort and retried: Docker refuses to remove a volume any container
// still references, including a stopped one, and container removal is itself
// asynchronous (the GC schedules it). So the first attempt commonly races the
// container's removal and fails; a few spaced retries cover the normal window,
// and the startup sweep remains the backstop.
func (h *Handler) removeTaskVolumes(ctx context.Context, clusterName, taskID string) {
	h.removeTaskVolumesAttempt(ctx, clusterName, taskID, 0)
}

// Volume removal retry policy, mirroring EFS's: a volume still held by a
// container that has not finished dying fails with 409, and the container GC's
// own backoff runs on a similar timescale.
const (
	taskVolumeRemoveRetries    = 3
	taskVolumeRemoveRetryDelay = 15 * time.Second
)

func (h *Handler) removeTaskVolumesAttempt(ctx context.Context, clusterName, taskID string, attempt int) {
	if h.docker == nil || clusterName == "" || taskID == "" {
		return
	}
	if h.cfg != nil && h.cfg.ECSKeepContainers {
		// A container kept for post-mortem inspection with its volumes deleted
		// cannot answer the question it was kept for.
		return
	}
	volumes, err := h.docker.ListVolumes(ctx, serviceName)
	if err != nil {
		h.log.Warn("ecs: list volumes for task cleanup", zap.String("task", taskID), zap.Error(err))
		return
	}
	resourceID := clusterName + "/" + taskID
	remaining := false
	for _, v := range volumes {
		if v.Labels[volumeScopeLabel] != "task" || v.ResourceID() != resourceID {
			continue
		}
		if err := h.docker.RemoveVolume(ctx, v.Name, true); err != nil {
			remaining = true
			h.log.Debug("ecs: remove task volume failed — will retry",
				zap.String("task", taskID), zap.String("volume", v.Name),
				zap.Int("attempt", attempt+1), zap.Error(err))
		}
	}
	if !remaining {
		return
	}
	if attempt >= taskVolumeRemoveRetries {
		h.log.Warn("ecs: task volumes still present after retries — orphaned until the next startup sweep",
			zap.String("task", taskID))
		return
	}
	h.scheduler.AfterScoped(h.store.region(ctx), taskID, "volume-remove", taskVolumeRemoveRetryDelay,
		func(bgCtx context.Context) {
			h.removeTaskVolumesAttempt(bgCtx, clusterName, taskID, attempt+1)
		})
}

// sweepOrphanedTaskVolumes removes task-scoped volumes whose task is gone —
// the backstop for a process killed between a task stopping and its volumes
// being removed. Shared-scope volumes are never considered.
func (h *Handler) sweepOrphanedTaskVolumes(ctx context.Context) {
	if h.docker == nil {
		return
	}
	// Only volumes no container references. Whether the owning task is still
	// in *this* instance's records is the wrong question: a second Overcast on
	// the same daemon keeps its own records, so each instance would see the
	// other's live task volumes as abandoned and delete them out from under a
	// running task. The daemon's own reference count is the one answer every
	// instance agrees on, and it is also correct for the case this sweep
	// exists for — a process killed before it could clean up leaves containers
	// that the container sweep has already removed by the time this runs.
	//
	// It subsumes the ECS_KEEP_CONTAINERS case for free: a container kept for
	// post-mortem inspection still references its volumes, so they are not
	// dangling and survive with it. That took an explicit exemption before.
	volumes, err := h.docker.ListUnusedVolumes(ctx, serviceName)
	if err != nil {
		h.log.Warn("ecs: list volumes for orphan sweep", zap.Error(err))
		return
	}
	for _, v := range volumes {
		// Shared-scope volumes outlive every task by design and are never
		// swept, however long they sit unreferenced.
		if v.Labels[volumeScopeLabel] != "task" {
			continue
		}
		h.log.Info("ecs: removing orphaned task volume", zap.String("volume", v.Name))
		if err := h.docker.RemoveVolume(ctx, v.Name, true); err != nil {
			h.log.Warn("ecs: remove orphaned task volume failed", zap.String("volume", v.Name), zap.Error(err))
		}
	}
}

// containerMounts resolves a container's mount points against the task
// definition's volumes, in mountPoints order, returning the Docker mounts to
// attach. It is pure: every volume it names has already been provisioned by
// provisionTaskVolumes.
//
// Each volume shape maps to what it means on AWS:
//
//   - efsVolumeConfiguration — the Docker volume backing the file system,
//     scoped by a Subpath from the access point's root directory or the
//     configuration's rootDirectory. Unresolvable references (EFS mock mode,
//     Docker down, unknown IDs) are skipped with a warning so the task still
//     starts, mirroring the best-effort posture of EFS live mode.
//   - host.sourcePath — a bind to a path on the container instance. Only
//     reachable under the EC2 launch type; Fargate is rejected at registration.
//   - anything else (name only, or an empty host block, or a
//     dockerVolumeConfiguration) — a Docker volume, per-task or shared.
//
// A mount point naming a volume the definition does not declare is skipped;
// registration rejects that, so reaching it means a task definition stored
// before the rule existed.
func (h *Handler) containerMounts(ctx context.Context, td *TaskDefinition, cd *ContainerDefinition, taskID string, hotReload map[string]string) []docker.Mount {
	if len(cd.MountPoints) == 0 {
		return nil
	}
	volumesByName := make(map[string]*TaskVolume, len(td.Volumes))
	for i := range td.Volumes {
		volumesByName[td.Volumes[i].Name] = &td.Volumes[i]
	}
	mounts := make([]docker.Mount, 0, len(cd.MountPoints))
	for _, mp := range cd.MountPoints {
		v := volumesByName[mp.SourceVolume]
		if v == nil {
			h.log.Warn("ecs: mount point skipped — the task definition declares no such volume",
				zap.String("container", cd.Name), zap.String("volume", mp.SourceVolume))
			continue
		}
		switch {
		// A hot-reload redirect wins over the scratch volume it replaces, and
		// only ever applies to a volume that had no configuration of its own —
		// hotReloadPaths refuses to redirect anything else.
		case hotReload[mp.SourceVolume] != "":
			mounts = append(mounts, docker.Mount{
				Type:     "bind",
				Source:   hotReload[mp.SourceVolume],
				Target:   mp.ContainerPath,
				ReadOnly: mp.ReadOnly,
			})
		case v.EFSVolumeConfiguration != nil:
			m, ok := h.efsMount(ctx, v.EFSVolumeConfiguration, cd, mp)
			if !ok {
				continue
			}
			mounts = append(mounts, m)
		case v.Host != nil && v.Host.SourcePath != "":
			mounts = append(mounts, docker.Mount{
				Type:     "bind",
				Source:   v.Host.SourcePath,
				Target:   mp.ContainerPath,
				ReadOnly: mp.ReadOnly,
			})
		default:
			source := taskVolumeName(taskID, v.Name)
			if v.DockerVolumeConfiguration != nil && dockerVolumeScope(v.DockerVolumeConfiguration) == "shared" {
				source = v.Name
			}
			mounts = append(mounts, docker.Mount{
				Type:     "volume",
				Source:   source,
				Target:   mp.ContainerPath,
				ReadOnly: mp.ReadOnly,
			})
		}
	}
	if len(mounts) == 0 {
		return nil
	}
	return mounts
}

// efsMount resolves one EFS-backed mount point. ok is false when the file
// system cannot be resolved, which is a warning rather than a failure.
func (h *Handler) efsMount(ctx context.Context, cfg *EFSVolumeConfiguration, cd *ContainerDefinition, mp MountPoint) (docker.Mount, bool) {
	if h.efsResolver == nil {
		return docker.Mount{}, false
	}
	var volume, subpath string
	var ok bool
	if cfg.AuthorizationConfig != nil && cfg.AuthorizationConfig.AccessPointId != "" {
		volume, subpath, ok = h.efsResolver.EFSVolumeForAccessPointID(ctx, cfg.AuthorizationConfig.AccessPointId)
	} else {
		volume, ok = h.efsResolver.EFSVolumeForFileSystem(ctx, cfg.FileSystemId)
		subpath = strings.Trim(cfg.RootDirectory, "/")
	}
	if !ok {
		msg, hint := h.efsMountSkipReason()
		h.log.Warn(msg,
			zap.String("container", cd.Name),
			zap.String("file_system", cfg.FileSystemId),
			zap.String("container_path", mp.ContainerPath),
			zap.String("consequence", "the container starts without the mount: writes to "+mp.ContainerPath+" go to its own writable layer and are lost when the task stops, and nothing is shared with other tasks"),
			zap.String("hint", hint))
		return docker.Mount{}, false
	}
	m := docker.Mount{Type: "volume", Source: volume, Target: mp.ContainerPath, ReadOnly: mp.ReadOnly}
	if subpath != "" {
		m.VolumeOptions = &docker.MountVolumeOptions{Subpath: subpath}
	}
	return m, true
}

// containerEndpoint returns the endpoint mapper for ECS task containers,
// resolving Overcast's container-reachable address once on first use. Resolving
// lazily rather than at SetDocker time keeps the ordering honest: the address
// may be Overcast's own IP on the ECS network, which only exists once the
// network has been created.

func (h *Handler) containerEndpoint(ctx context.Context) *containerendpoint.Mapper {
	h.endpointOnce.Do(func() {
		// ResolveHost + BaseURL rather than Resolve: the endpoint scheme must
		// follow the listener (https under OVERCAST_TLS), which only config knows.
		host := containerendpoint.ResolveHost(ctx, h.docker, dataplane.Primary(h.cfg), h.log.ZapLogger())
		h.endpoint = containerendpoint.New(h.cfg, containerendpoint.BaseURL(h.cfg, host)).WithPublishedPort(h.cfg.PublishedPort)
	})
	return h.endpoint
}

// buildContainerEnv builds the Docker environment for one task container:
// task-definition environment, then any RunTask container override (applied
// last so it wins, as in ECS), then Overcast's endpoint.
//
// Values are passed through the endpoint mapper because AWS SDKs resolve the
// SQS endpoint from the QueueUrl rather than from AWS_ENDPOINT_URL, so a queue
// URL baked in by a host-side deploy would otherwise point the task's SQS
// client at the task's own loopback. See internal/containerendpoint.
func buildContainerEnv(cd ContainerDefinition, co *ContainerOverride, endpoint *containerendpoint.Mapper) []string {
	env := make([]string, 0, len(cd.Environment)+2)
	appendKV := func(kvs []KeyValuePair) {
		for _, kv := range kvs {
			env = append(env, kv.Name+"="+endpoint.RewriteURLs(kv.Value))
		}
	}

	appendKV(cd.Environment)
	if co != nil {
		appendKV(co.Environment)
	}

	// Add the Overcast endpoint so containers can call back into the emulator.
	// Named rather than numbered: it survives Overcast being recreated on a
	// different address, and it lets an SDK derive virtual-hosted URLs from it.
	if addr := endpoint.ClientEndpoint(); addr != "" {
		env = append(env, "AWS_ENDPOINT_URL="+addr)
	}
	// With TLS on, point every SDK TLS stack at the injected CA bundle.
	for k, v := range endpoint.CABundleEnv() {
		env = append(env, k+"="+v)
	}
	return env
}

// scheduleRunningTransition sets up a task's PROVISIONING → RUNNING transition.
// Both placement paths use it: Docker-backed tasks, whose containers are
// already started by the time it runs, and metadata-only tasks placed while no
// container runtime is wired. region is the region the task is stored under,
// resolved from the request context at schedule time.
//
// A task owned by a service reconciles that service when it starts, which is
// what moves the service to its running count and, at the desired count, into
// a steady state — and, mid-rollout, what retires the task the new one has
// just replaced.
func (h *Handler) scheduleRunningTransition(region, clusterName, taskID string) {
	capturedCluster := clusterName
	capturedTaskID := taskID
	h.scheduler.AfterScoped(region, taskID, "pending", 200*time.Millisecond, func(ctx context.Context) {
		started := h.applyRunningTransition(ctx, capturedCluster, capturedTaskID)
		if started == nil {
			return
		}
		if h.bus != nil {
			h.bus.Publish(ctx, events.Event{Type: events.ECSTaskStarted, Payload: events.ResourcePayload{Name: capturedTaskID}})
		}
		if serviceName, ok := serviceNameFromGroup(started.Group); ok {
			h.reconcile(ctx, capturedCluster, serviceName)
		}
	})
}

// applyRunningTransition moves one task from PROVISIONING/PENDING to RUNNING,
// returning the stored task, or nil when the task is gone or has already left
// those states — a task stopped while it was coming up must stay stopped.
//
// Under lockTask, and the lock is released before the caller reconciles the
// owning service: the read and the write are one step, and the ordering stays
// service-then-task. See lockTask.
func (h *Handler) applyRunningTransition(ctx context.Context, clusterName, taskID string) *Task {
	defer h.lockTask(clusterName, taskID)()

	got, aerr := h.store.getTask(ctx, clusterName, taskID)
	if aerr != nil || got == nil {
		return nil
	}
	if got.LastStatus != "PROVISIONING" && got.LastStatus != "PENDING" {
		return nil
	}
	got.LastStatus = "RUNNING"
	startedAt := h.clk.Now().Unix()
	got.StartedAt = &startedAt
	for i := range got.Containers {
		got.Containers[i].LastStatus = "RUNNING"
	}
	// A running awsvpc task's ENI is ATTACHED, not still ATTACHING — that
	// is the status callers wait on before treating the address as usable.
	for i := range got.Attachments {
		if got.Attachments[i].Type == "ElasticNetworkInterface" && got.Attachments[i].Status == "ATTACHING" {
			got.Attachments[i].Status = "ATTACHED"
		}
	}
	if aerr := h.store.putTask(ctx, got); aerr != nil {
		return nil
	}
	return got
}

// StopTask handles AmazonEC2ContainerServiceV20141113.StopTask.
func (h *Handler) StopTask(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Cluster string `json:"cluster"`
		Task    string `json:"task"`
		Reason  string `json:"reason"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}

	task, aerr := h.stopTaskCore(r.Context(), req.Cluster, req.Task, req.Reason)
	if aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}

	protocol.WriteAWSJSON(w, r, http.StatusOK, map[string]any{"task": task.forWire()}, "application/x-amz-json-1.1")
}

// stopTaskCore is what StopTask does, for both the JSON 1.1 handler above and
// the RPC v2 CBOR one in typed_logic.go.
//
// It is one function because it was two, and the two drifted. The CBOR copy
// tore the containers down before it recorded the stop — the ordering the
// comment below exists to prevent — and never did the service handling at all,
// so a task stopped over CBOR could come back as EssentialContainerExited,
// count against its deployment, and leave its targets registered. Newer SDK
// releases put ECS on CBOR, which makes a divergence here something a user hits
// by upgrading rather than by changing their code. Sharing stopTaskContainers
// was not enough to keep the two in step; sharing the order they call it in is.
// See wire_parity_test.go.
func (h *Handler) stopTaskCore(ctx context.Context, cluster, taskRef, reason string) (*Task, *protocol.AWSError) {
	if cluster == "" {
		cluster = "default"
	}
	clusterName := extractClusterName(cluster)
	taskID := extractTaskID(taskRef)

	task, aerr := h.store.getTask(ctx, clusterName, taskID)
	if aerr != nil {
		return nil, aerr
	}
	if task == nil {
		return nil, errTaskNotFound()
	}

	// Cancel any pending scheduler transition.
	h.scheduler.CancelScoped(h.store.region(ctx), taskID, "pending")

	// A task a service owns leaves that service's target groups before it is
	// torn down, and the service then replaces it — its desired count has not
	// changed just because one task was stopped by hand. Both used to fall out
	// of the container's die event; they belong here now that the exit notifier
	// leaves a task the caller already stopped alone.
	serviceName, ownedByService := serviceNameFromGroup(task.Group)
	if ownedByService {
		if svc, aerr := h.store.getService(ctx, clusterName, serviceName); aerr == nil && svc != nil {
			h.deregisterTaskTargets(ctx, svc, task)
		}
	}

	// Record the stop before touching the containers: killing them raises a
	// Docker die event whose handler races this write, and a caller-initiated
	// stop that loses the race is reported as a task that died on its own.
	task, _, aerr = h.stopTaskRecord(ctx, clusterName, taskID, taskStop{
		reason: reason, code: "UserInitiated",
	})
	if aerr != nil {
		return nil, aerr
	}
	if task == nil {
		return nil, errTaskNotFound()
	}

	h.stopTaskContainers(ctx, task)

	if h.bus != nil {
		h.bus.Publish(ctx, events.Event{Type: events.ECSTaskStopped, Payload: events.ResourcePayload{Name: taskID}})
	}
	if ownedByService {
		h.scheduleServiceReplacement(ctx, h.store.region(ctx), clusterName, serviceName)
	}
	return task, nil
}

// errTaskNotFound is what both StopTask paths answer a task reference that
// resolves to nothing.
func errTaskNotFound() *protocol.AWSError {
	return &protocol.AWSError{
		Code:       "InvalidParameterException",
		Message:    "The referenced task was not found.",
		HTTPStatus: http.StatusBadRequest,
	}
}

// DescribeTasks handles AmazonEC2ContainerServiceV20141113.DescribeTasks.
func (h *Handler) DescribeTasks(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Cluster string   `json:"cluster"`
		Tasks   []string `json:"tasks"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}

	if req.Cluster == "" {
		req.Cluster = "default"
	}
	clusterName := extractClusterName(req.Cluster)

	type failure struct {
		Arn    string `json:"arn"`
		Reason string `json:"reason"`
	}
	found := make([]Task, 0, len(req.Tasks))
	failures := make([]failure, 0)

	for _, ref := range req.Tasks {
		taskID := extractTaskID(ref)
		task, aerr := h.getVisibleTask(r.Context(), clusterName, taskID)
		if aerr != nil {
			protocol.WriteJSONError(w, r, aerr)
			return
		}
		if task == nil {
			arn := ref
			if !strings.HasPrefix(arn, "arn:") {
				arn = h.taskARN(r.Context(), clusterName, taskID)
			}
			failures = append(failures, failure{Arn: arn, Reason: "MISSING"})
			continue
		}
		found = append(found, task.forWire())
	}

	// Headers must be set before the body is written; late mutation only
	// reaches the wire while DrainBody happens to buffer the response.
	docker.SetBackingHeaders(w, h.dockerReady.Load(), docker.ContainerHealthUnknown)
	protocol.WriteAWSJSON(w, r, http.StatusOK, map[string]any{
		"tasks":    found,
		"failures": failures,
	}, "application/x-amz-json-1.1")
}

// ListTasks handles AmazonEC2ContainerServiceV20141113.ListTasks.
func (h *Handler) ListTasks(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Cluster       string `json:"cluster"`
		DesiredStatus string `json:"desiredStatus"`
		Family        string `json:"family"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}

	if req.Cluster == "" {
		req.Cluster = "default"
	}
	clusterName := extractClusterName(req.Cluster)

	tasks, aerr := h.listVisibleTasks(r.Context(), clusterName)
	if aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}

	desiredStatus := req.DesiredStatus
	if desiredStatus == "" {
		desiredStatus = "RUNNING"
	}
	arns := make([]string, 0, len(tasks))
	for _, t := range tasks {
		if t.DesiredStatus != desiredStatus {
			continue
		}
		if req.Family != "" && !strings.Contains(t.TaskDefinitionArn, "/"+req.Family+":") {
			continue
		}
		arns = append(arns, t.TaskArn)
	}

	protocol.WriteAWSJSON(w, r, http.StatusOK, map[string]any{"taskArns": arns}, "application/x-amz-json-1.1")
}

// firstOrEmpty extracts a string from a pointer value using a getter, returning "" if nil.
func firstOrEmpty[T any](v *T, fn func(*T) string) string {
	if v == nil {
		return ""
	}
	return fn(v)
}
