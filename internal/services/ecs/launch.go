package ecs

// launch.go — the single path by which a task is placed.
//
// RunTask and the service reconciler both come through here, so a task started
// by a service is the same object, started the same way, as one started
// directly: real containers when Docker is wired, the same awsvpc ENI
// attachment, and the same failure shape when it cannot be placed.

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/overcast-sh/overcast/internal/dataplane"
	"github.com/overcast-sh/overcast/internal/events"
	"github.com/overcast-sh/overcast/internal/protocol"
)

// awsvpcPlacement is the resolved result of an awsvpc networkConfiguration:
// which subnet the task lands in and which Docker network backs its VPC.
type awsvpcPlacement struct {
	subnetID       string
	networkID      string
	subnetResolved bool

	// placement is the dataplane's view of the same decision, carried for
	// the two things the manual ENI attach in attachTaskENI does not do: the
	// egress network the task's subnets earn under OVERCAST_VPC_EGRESS=routed,
	// and recording the placement so a route-table change can revisit it.
	placement dataplane.Placement
	// remapped marks a VPC whose Docker network sits on a shadow CIDR rather
	// than the one the API reports. The address a container really holds is
	// then deliberately not the address AWS callers are shown, so the ENI
	// address must not be reconciled against it.
	remapped bool

	// assignPublicIP mirrors the task's `assignPublicIp: ENABLED`, which on AWS
	// gives an awsvpc task in a public subnet a routable address and so a way
	// out of its VPC. Here it keeps the task on the default data plane as well
	// as its VPC network — the escape hatch from placement, spelled with AWS's
	// own field so the fix that works locally is the fix that works deployed.
	assignPublicIP bool
}

// taskLaunchSpec describes one task about to be placed.
type taskLaunchSpec struct {
	clusterName     string
	clusterArn      string
	td              *TaskDefinition
	launchType      string
	platformVersion string
	group           string
	startedBy       string
	overrides       *TaskOverride
	netCfg          *NetworkConfiguration
	placement       awsvpcPlacement
	// ordinal distinguishes tasks placed in the same batch when EC2 cannot
	// allocate a private IP and the synthetic fallback is used.
	ordinal int
}

// launchTask builds one task from a task definition and places it, starting
// real containers whenever Docker is wired. The task is persisted and then
// transitions to RUNNING only when Docker is available; without Docker the
// task stays at PROVISIONING — it is never misrepresented as RUNNING.
//
// A task whose containers cannot be started is persisted STOPPED with stopCode
// TaskFailedToStart, which is what AWS does — it is never reported RUNNING.
func (h *Handler) launchTask(ctx context.Context, spec taskLaunchSpec) (*Task, *protocol.AWSError, error) {
	taskID := uuid.New().String()
	td := spec.td

	containers := make([]Container, 0, len(td.ContainerDefinitions))
	for _, cd := range td.ContainerDefinitions {
		containers = append(containers, Container{
			ContainerArn: h.containerARN(ctx, uuid.New().String()),
			Name:         cd.Name,
			Image:        cd.Image,
			LastStatus:   "PENDING",
		})
	}

	task := &Task{
		TaskArn:           h.taskARN(ctx, spec.clusterName, taskID),
		TaskDefinitionArn: td.TaskDefinitionArn,
		ClusterArn:        spec.clusterArn,
		LastStatus:        "PROVISIONING",
		DesiredStatus:     "RUNNING",
		LaunchType:        spec.launchType,
		Cpu:               td.Cpu,
		Memory:            td.Memory,
		PlatformVersion:   spec.platformVersion,
		CreatedAt:         h.clk.Now().Unix(),
		Group:             spec.group,
		StartedBy:         spec.startedBy,
		Containers:        containers,
		Overrides:         spec.overrides,
		Attachments:       h.awsvpcAttachment(ctx, spec, taskID),
	}

	startErr := error(nil)
	var unlockTask func()
	lockTaskBeforeStart := func() {
		if unlockTask == nil {
			// Docker can report a fast container exit before StartContainer
			// returns. Keep the notifier out from the first start until the task
			// and its Docker IDs are persisted, otherwise that event is lost.
			// Image pulls and container creation stay outside the critical
			// section; no container can exit before it has started.
			unlockTask = h.lockTask(spec.clusterName, taskID)
		}
	}
	defer func() {
		if unlockTask != nil {
			unlockTask()
		}
	}()
	if h.dockerReady.Load() {
		startErr = h.startTaskContainers(ctx, task, td, spec.clusterName, taskID, spec.placement, lockTaskBeforeStart)
		if startErr != nil {
			h.log.Warn("ecs: task failed to start",
				zap.String("cluster", spec.clusterName),
				zap.String("task", taskID),
				zap.Error(startErr))
			markTaskFailedToStart(task, h.clk.Now().Unix(), startErr)
			// Targets registered before the failure have no container to
			// come; their ports go back for the next attempt.
			h.releaseDebugTargets(task)
		}
	}

	if aerr := h.store.putTask(ctx, task); aerr != nil {
		return nil, aerr, nil
	}
	if startErr != nil {
		if h.bus != nil {
			h.bus.Publish(ctx, events.Event{
				Type:    events.ECSTaskStartFailed,
				Payload: events.ResourcePayload{Name: taskID},
			})
		}
		return task, nil, startErr
	}

	// Only now, with the record persisted. The transition reads the task back
	// and gives up if it is not there, and it is a one-shot: scheduled before
	// this write, it can fire against a task that does not exist yet, be spent,
	// and leave the record that lands a moment later stuck at PROVISIONING for
	// good. Both placement paths converge here so neither can get that wrong.
	//
	// When Docker is unavailable the task is not transitioned to RUNNING: no
	// container is running and it is dishonest to report otherwise.
	if h.dockerReady.Load() {
		h.scheduleRunningTransition(h.store.region(ctx), spec.clusterName, taskID)
	}
	return task, nil, nil
}

// awsvpcAttachment builds the ENI attachment AWS reports on an awsvpc task.
func (h *Handler) awsvpcAttachment(ctx context.Context, spec taskLaunchSpec, taskID string) []Attachment {
	if spec.netCfg == nil {
		return nil
	}
	i := spec.ordinal
	privateIP := "10.0." + fmt.Sprintf("%d.%d", (i+1)/256, (i+1)%256)
	if spec.placement.subnetResolved && h.vpcResolver != nil {
		if translated := h.vpcResolver.AllocatePrivateIPForSubnet(ctx, spec.placement.subnetID); translated != "" {
			privateIP = translated
		}
	}
	return []Attachment{{
		Id:     uuid.New().String(),
		Type:   "ElasticNetworkInterface",
		Status: "ATTACHING",
		Details: []KeyValuePair{
			{Name: "networkInterfaceId", Value: "eni-" + taskID[:8]},
			{Name: "subnetId", Value: spec.placement.subnetID},
			{Name: "privateIPv4Address", Value: privateIP},
		},
	}}
}

// containerStartFailure attributes a task start failure to the container that
// caused it. Every failure raised while placing a task belongs to one container
// definition, and the name has to survive the return from startTaskContainers
// for markTaskFailedToStart to record it where AWS does.
type containerStartFailure struct {
	container string
	err       error
}

func (e *containerStartFailure) Error() string { return e.err.Error() }
func (e *containerStartFailure) Unwrap() error { return e.err }

// containerFailure builds the error a container's start failure is reported as.
func containerFailure(container, format string, args ...any) error {
	return &containerStartFailure{container: container, err: fmt.Errorf(format, args...)}
}

// failedContainer names the container a start failure belongs to, or "" when
// the failure cannot be attributed to one.
func failedContainer(err error) string {
	var cf *containerStartFailure
	if errors.As(err, &cf) {
		return cf.container
	}
	return ""
}

// markTaskFailedToStart records a task that could not be placed in the shape
// AWS uses for one: STOPPED, never RUNNING, carrying the stopCode a caller
// would switch on and a reason naming what actually went wrong.
//
// The reason is recorded on the one container that caused it. ECS attributes a
// launch failure to the container it happened to — a task stopped because its
// application image could not be pulled leaves its sidecars STOPPED with no
// reason of their own. Copying the reason onto every container instead made an
// X-Ray sidecar report a CannotPullContainerError against a CDK container-asset
// image it never names, which reads as the sidecar's own image being wrong.
func markTaskFailedToStart(task *Task, now int64, err error) {
	reason := stoppedReasonFor(err)
	task.LastStatus = "STOPPED"
	task.DesiredStatus = "STOPPED"
	task.StopCode = "TaskFailedToStart"
	task.StoppedReason = reason
	task.StoppedAt = &now
	task.StoppingAt = &now
	// An unattributable failure is left to the task's own stoppedReason rather
	// than blamed on a container that may not have caused it.
	culprit := failedContainer(err)
	for i := range task.Containers {
		task.Containers[i].LastStatus = "STOPPED"
		if culprit != "" && task.Containers[i].Name == culprit {
			task.Containers[i].Reason = reason
		}
	}
}

// stoppedReasonFor prefixes a container start failure with the AWS stopped-task
// error code covering it, so the reason a user reads here names the same
// condition it would on real ECS.
func stoppedReasonFor(err error) string {
	msg := err.Error()
	switch {
	// Before the image and container checks: a task's networking failing to
	// come up is a ResourceInitializationError on AWS whatever the cause, and
	// the cause here can be the utility image failing to pull — which is not
	// the user's own image failing to pull, and must not be reported as it.
	case errors.Is(err, errTaskNetworkNamespace):
		return "ResourceInitializationError: " + msg
	case strings.Contains(msg, "pull image"):
		return "CannotPullContainerError: " + msg
	case strings.Contains(msg, "create container"), strings.Contains(msg, "start container"):
		return "CannotStartContainerError: " + msg
	// Volumes are checked before "network" because a volume failure's message
	// can mention a driver named after a network filesystem (an NFS-backed
	// dockerVolumeConfiguration is the common one), and AWS reports both
	// classes under the same code anyway.
	case strings.Contains(msg, "volume"):
		return "ResourceInitializationError: " + msg
	case strings.Contains(msg, "network"):
		return "ResourceInitializationError: " + msg
	default:
		return msg
	}
}
