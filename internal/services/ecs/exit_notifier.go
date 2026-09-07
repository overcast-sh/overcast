package ecs

import (
	"context"
	"strconv"
	"strings"
	"time"

	"go.uber.org/zap"

	"github.com/overcast-sh/overcast/internal/docker"
	"github.com/overcast-sh/overcast/internal/events"
	"github.com/overcast-sh/overcast/internal/middleware"
	"github.com/overcast-sh/overcast/internal/serviceutil"
)

// reconcileContainers brings stored task state back in line with Docker after
// startup or an event-stream reconnect. It deliberately feeds the same exit
// handler as a live die event so stop metadata, service failure accounting,
// replacement scheduling, log retention, and cleanup have one owner.
func (h *Handler) reconcileContainers(ctx context.Context, containers []docker.ContainerSummary) {
	tasks, err := serviceutil.ScanRegions[Task](ctx, h.store.store, nsTasks, h.store.defaultRegion)
	if err != nil {
		h.log.Warn("ecs: reconcile containers: list tasks", zap.Error(err))
		return
	}
	byResource := docker.ContainersByResource(containers)
	for _, regioned := range tasks {
		task := regioned.Value
		if task.LastStatus == "STOPPED" {
			continue
		}
		rctx := middleware.ContextWithRegion(ctx, regioned.Region)
		resourceID := extractClusterName(task.ClusterArn) + "/" + extractTaskID(task.TaskArn)
		h.reconcileTaskContainers(rctx, task, resourceID, byResource[resourceID])
	}
}

func (h *Handler) reconcileTaskContainers(ctx context.Context, task *Task, resourceID string, candidates []*docker.ContainerSummary) {
	for _, container := range task.Containers {
		if container.DockerID == "" || container.LastStatus == "STOPPED" {
			continue
		}
		actual := h.instances.OwnContainer(ctx, candidates, container.DockerID)
		if actual != nil && !containerHasExited(actual.State) {
			continue
		}
		payload, exitTime := h.reconciledContainerExit(ctx, container.DockerID, resourceID, actual)
		h.handleContainerDied(ctx, events.Event{Type: events.DockerContainerDied, Time: exitTime, Payload: payload})
	}
}

func (h *Handler) reconciledContainerExit(ctx context.Context, recordedID, resourceID string, actual *docker.ContainerSummary) (events.DockerContainerPayload, time.Time) {
	payload := events.DockerContainerPayload{
		ContainerID: recordedID,
		Action:      "die",
		Service:     serviceName,
		ResourceID:  resourceID,
	}
	if actual == nil {
		return payload, time.Time{}
	}
	payload.ContainerID = actual.ID
	payload.Instance = actual.Instance()
	if h.docker == nil {
		return payload, time.Time{}
	}
	info, err := h.docker.InspectContainer(ctx, actual.ID)
	if err != nil {
		h.log.Debug("ecs: reconcile containers: inspect exited container",
			zap.String("container", actual.ID), zap.Error(err))
		return payload, time.Time{}
	}
	payload.ExitCode = strconv.Itoa(info.State.ExitCode)
	payload.Reason = info.ExitReason()
	return payload, info.ExitTime()
}

func containerHasExited(state string) bool {
	switch strings.ToLower(state) {
	case "dead", "exited", "removing":
		return true
	default:
		return false
	}
}

// handleContainerDied is a bus handler for DockerContainerDied events targeting
// ECS containers. When a task container exits, it updates the container status,
// and if all containers in the task are stopped, transitions the task to STOPPED.
func (h *Handler) handleContainerDied(_ context.Context, e events.Event) {
	p, ok := e.Payload.(events.DockerContainerPayload)
	if !ok || p.Service != "ecs" {
		return
	}

	// ResourceID format: "clusterName/taskID"
	parts := strings.SplitN(p.ResourceID, "/", 2)
	if len(parts) != 2 {
		h.log.Warn("ecs: container died with invalid resource ID",
			zap.String("resourceId", p.ResourceID),
			zap.String("containerId", p.ContainerID))
		return
	}
	clusterName, taskID := parts[0], parts[1]

	region, task, allStopped := h.recordContainerExit(clusterName, taskID, p, e.Time)
	if task == nil {
		return
	}
	ctx := middleware.ContextWithRegion(context.Background(), region)
	h.retainContainerLogs(ctx, task, p.ContainerID)
	h.clearDebugContainer(task, p.ContainerID)
	if !allStopped {
		return
	}
	h.releaseDebugTargets(task)

	// The task is fully stopped, so its task-lifetime volumes have no reader
	// left. Shared-scope volumes are left alone; see removeTaskVolumes.
	h.removeTaskVolumes(ctx, clusterName, taskID)

	// Nothing is left inside the task's network namespace either. The container
	// holding it open outlives the application containers by design — it carries
	// the task's ENI, which on AWS outlives any one container — so no container
	// exit takes it down as a side effect and this path has to.
	h.retireTaskNamespaceContainer(ctx, task)

	if h.bus != nil {
		h.bus.Publish(ctx, events.Event{
			Type:    events.ECSTaskStopped,
			Time:    e.Time,
			Payload: events.ResourcePayload{Name: taskID},
		})
	}

	// A service keeps its desired count: a task whose containers exited is
	// replaced. Without this a service drains to zero the first time a task
	// finishes and stays there, which is not what ECS does with one.
	serviceName, ok := serviceNameFromGroup(task.Group)
	if !ok {
		return
	}
	h.recordServiceTaskDeath(ctx, clusterName, serviceName, task, e.Time)
	h.scheduleServiceReplacement(ctx, region, clusterName, serviceName)
}

// maxRetainedLogBytes bounds one container's retained tail. The upstream
// ContainerLogs read is already capped, but only per call: the number of
// entries is bounded by task lifetime (see deleteTaskContainerLogs) and their
// size is bounded here. 16 KiB matches RDS — enough for a stack trace and the
// lines around it, small enough that the record stays a record.
const maxRetainedLogBytes = 16 * 1024

// containerLogCaptureTimeout bounds a capture so that a Docker daemon which has
// stopped answering cannot hold up a task stop behind it.
const containerLogCaptureTimeout = 5 * time.Second

// captureContainerLogs copies the final bounded tail of one container's output
// into the retention namespace, so it survives the container.
//
// This is the only place ECS captures container logs, and it is called from two
// orderings. On the natural-death path — a crash-looping container that exits
// on its own — the Docker die event brings it here asynchronously, which is
// fine because nothing is racing to remove the container. On a teardown the
// scheduler or a caller drives, the container is removed the moment it stops,
// so retireTaskContainers calls this synchronously in between; see there.
//
// Both can therefore run for one container, and the first success wins: a
// capture that has already landed is not read over. The copy taken while the
// container was certainly still there is the better one, and a later read of a
// container mid-removal can succeed and return less than all of it.
//
// That check is a read followed by a write rather than a locked one, so two
// captures interleaving inside the window between them can both proceed, and
// the later write lands. Left alone deliberately: both copies are genuine
// captures of the same container differing at worst in how much they caught,
// ECS keeps no record locks anywhere else to borrow, and new locking machinery
// to guard a rare case with a harmless outcome is the worse trade.
//
// Best-effort by design. A container whose logs cannot be read must still
// complete its stop, so every failure here is logged and swallowed, and the
// read is given its own short deadline rather than the caller's.
func (h *Handler) captureContainerLogs(ctx context.Context, task *Task, dockerID string) {
	if dockerID == "" {
		return
	}
	// Every path that ends a container comes through here, which makes it the
	// one place that knows the container's awslogs follower — if it has one —
	// has nothing left to follow. Stopped first, and unconditionally: a
	// follower left running would keep re-opening a log stream for a container
	// that is about to be removed, and the emulator not being ready to read
	// logs does not make that any less true.
	h.stopLogStreaming(dockerID)
	if !h.dockerReady.Load() || h.docker == nil {
		return
	}

	containerName := ""
	for _, container := range task.Containers {
		if container.DockerID == dockerID {
			containerName = container.Name
			break
		}
	}
	if containerName == "" {
		return
	}

	cluster, taskID := extractClusterName(task.ClusterArn), extractTaskID(task.TaskArn)
	if have, found, aerr := h.store.getTaskContainerLogs(ctx, cluster, taskID, containerName); aerr == nil && found && have.Logs != "" {
		return
	}

	logCtx, cancel := context.WithTimeout(ctx, containerLogCaptureTimeout)
	raw, err := h.docker.ContainerLogs(logCtx, dockerID, "200")
	cancel()
	if err != nil {
		h.log.Debug("ecs: capture stopped container logs",
			zap.String("container", dockerID), zap.Error(err))
		return
	}
	logs := serviceutil.TailBytes(string(docker.DemuxStream(raw)), maxRetainedLogBytes)
	if logs == "" {
		return
	}
	if aerr := h.store.putTaskContainerLogs(ctx, cluster, taskID, containerName, taskContainerLogs{
		Logs:       logs,
		CapturedAt: h.clk.Now().UTC().Format(time.RFC3339),
	}); aerr != nil {
		h.log.Warn("ecs: persist stopped container logs",
			zap.String("container", dockerID), zap.String("error", aerr.Message))
	}
}

// captureContainerLogsByID is captureContainerLogs for a caller that has only a
// container ID — the container GC's before-remove hook, which is handed one
// container at a time and knows nothing of tasks.
//
// The task is found by scanning for the record that claims the container, the
// same way reconcileContainers matches Docker's containers back to tasks. That
// is a scan per removal, which is affordable here and not elsewhere: removals
// are driven by task teardown rather than by request traffic, they run on the
// GC's own loop where nothing is waiting on them, and the alternative — asking
// Docker for the container's resource-id label — is a second round trip to a
// daemon that is about to be told to delete it.
//
// A container with no task record left is not an error: the record expires an
// hour after the task stops, and a removal that arrives after that has nothing
// left to attach output to.
func (h *Handler) captureContainerLogsByID(containerID string) {
	if containerID == "" || !h.dockerReady.Load() {
		return
	}
	ctx := context.Background()
	tasks, err := serviceutil.ScanRegions[Task](ctx, h.store.store, nsTasks, h.store.defaultRegion)
	if err != nil {
		h.log.Debug("ecs: capture container logs before removal: list tasks",
			zap.String("container", containerID), zap.Error(err))
		return
	}
	for _, regioned := range tasks {
		for _, c := range regioned.Value.Containers {
			if c.DockerID != containerID {
				continue
			}
			h.captureContainerLogs(middleware.ContextWithRegion(ctx, regioned.Region), regioned.Value, containerID)
			return
		}
	}
}

// retainContainerLogs captures a dead container's final output and then lets it
// go. Called off the die event, where the container has exited on its own and
// nothing else is removing it.
func (h *Handler) retainContainerLogs(ctx context.Context, task *Task, dockerID string) {
	if !h.dockerReady.Load() || dockerID == "" {
		return
	}
	h.captureContainerLogs(ctx, task, dockerID)

	if h.gc != nil {
		h.gc.ScheduleRemove(dockerID)
		return
	}
	if h.cfg == nil || !h.cfg.ECSKeepContainers {
		_ = h.docker.RemoveContainerForce(dockerID)
	}
}

// containerIsOurs reports whether the container a die event is describing is
// one this Overcast created for task.
//
// A container is matched to a task by the overcast.resource-id label, and that
// label says nothing about which Overcast created it: two of them sharing a
// Docker daemon keep separate state stores, and a store restored or copied
// between them leaves both holding the same task IDs while each creates its
// own containers. The damage is not in the per-container update below, which
// already compares Docker IDs — it is in the stop decision, which asks only
// whether any recorded container is still running. A task still being placed
// has none recorded yet and satisfies that vacuously, so a foreign exit stops
// it, tears down its volumes, counts the death against the deployment and
// schedules a replacement.
//
// A task tracks a container ID per container rather than one on the record, so
// all of them are offered as the fallback ownership evidence — see
// InstanceDomain.ContainerIsOurs for what settles it.
func (h *Handler) containerIsOurs(ctx context.Context, containerID, owner string, task *Task) bool {
	recorded := make([]string, 0, len(task.Containers))
	for _, c := range task.Containers {
		recorded = append(recorded, c.DockerID)
	}
	return h.instances.ContainerIsOurs(ctx, containerID, owner, recorded...)
}

// recordContainerExit records one container's exit on its task's record, and
// stops the task when that was the last container still running. It returns the
// region the task is stored under, the stored task (nil when there is no such
// task), and whether the task itself stopped.
//
// Under lockTask, which is why it takes the lock before it knows the region:
// the key is cluster and task ID alone, so the cross-region scan that finds the
// task happens inside the lock rather than racing ahead of it. The lock is
// released before the caller touches the service, keeping the order
// service-then-task. See lockTask.
func (h *Handler) recordContainerExit(clusterName, taskID string, p events.DockerContainerPayload, occurredAt time.Time) (string, *Task, bool) {
	defer h.lockTask(clusterName, taskID)()

	// The event only carries "cluster/task" — not the region the task was
	// stored under — so locate it with a cross-region scan and pin that
	// region on the context for the write-back.
	task, region, found, err := serviceutil.FindRegioned[Task](context.Background(), h.store.store, nsTasks, p.ResourceID, h.store.defaultRegion)
	if err != nil || !found {
		return "", nil, false
	}
	ctx := middleware.ContextWithRegion(context.Background(), region)
	if !h.containerIsOurs(ctx, p.ContainerID, p.Instance, task) {
		return "", nil, false
	}

	// Update the container that died.
	for i := range task.Containers {
		if task.Containers[i].DockerID == p.ContainerID {
			task.Containers[i].LastStatus = "STOPPED"
			if exitCode, err := strconv.Atoi(p.ExitCode); err == nil {
				task.Containers[i].ExitCode = &exitCode
			}
			if p.Reason != "" {
				task.Containers[i].Reason = p.Reason
			}
			break
		}
	}

	// A task the scheduler or the caller already stopped keeps the stop code and
	// reason it was given: its container exiting is the *consequence* of that
	// decision, not a task dying on its own. Recording the exit code is all
	// that is left to do — rewriting it to EssentialContainerExited would lose
	// why it stopped, count a deliberate scale-in as a deployment failure, and
	// schedule a replacement for a task nothing is missing.
	if task.LastStatus == "STOPPED" {
		h.store.putTask(ctx, task) //nolint:errcheck
		return region, task, false
	}

	// Check if all containers are stopped.
	allStopped := true
	for _, c := range task.Containers {
		if c.LastStatus != "STOPPED" {
			allStopped = false
			break
		}
	}

	if allStopped {
		task.LastStatus = "STOPPED"
		task.DesiredStatus = "STOPPED"
		task.StoppedReason = "Essential container in task exited"
		task.StopCode = "EssentialContainerExited"
		if occurredAt.IsZero() {
			occurredAt = h.clk.Now()
		}
		stoppedAt := occurredAt.Unix()
		task.StoppedAt = &stoppedAt
		task.StoppingAt = &stoppedAt

		// Cancel any pending scheduler transition.
		h.scheduler.CancelScoped(region, taskID, "pending")
	}

	h.store.putTask(ctx, task) //nolint:errcheck
	return region, task, allStopped
}

// recordServiceTaskDeath takes a dead task out of its service's target groups
// and counts it against the deployment that placed it.
//
// Under lockService, because this is a read-modify-write of the whole service
// record and a reconcile of the same service runs concurrently — on the
// scheduler, on the very replacement this death is about to trigger. Unguarded,
// the reconcile's write lands on top of the increment and the failure is lost:
// the count a crash loop is measured by then undercounts, and with it the
// "unable to consistently start tasks" event and the circuit breaker.
func (h *Handler) recordServiceTaskDeath(ctx context.Context, clusterName, serviceName string, task *Task, occurredAt time.Time) {
	defer h.lockService(ctx, clusterName, serviceName)()

	svc, aerr := h.store.getService(ctx, clusterName, serviceName)
	if aerr != nil || svc == nil {
		return
	}
	// Out of the load balancer's rotation first, then replaced.
	h.deregisterTaskTargets(ctx, svc, task)
	// The deployment counts it as a failed task, which is what makes a
	// crash loop visible: failedTasks climbs, the deployment stops
	// reporting a steady state it is not in, and a circuit breaker trips.
	h.recordTaskStopFailureAt(svc, task, occurredAt)
	if aerr := h.store.putService(ctx, clusterName, svc); aerr != nil {
		h.log.Warn("ecs: failed to persist service after a task stopped",
			zap.String("cluster", clusterName),
			zap.String("service", serviceName),
			zap.String("error", aerr.Message))
	}
}
