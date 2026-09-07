package ecs

// task_netns.go — the network namespace an awsvpc task's containers share.
//
// On AWS, every container in an awsvpc task runs in **one** network namespace:
// one ENI, one address, one set of listening ports, and `127.0.0.1` reaching
// every other container in the same task. That is what makes the sidecar
// pattern work — nginx proxying to php-fpm on `127.0.0.1:9000`, an application
// reaching its X-Ray daemon on `127.0.0.1:2000` — and a task definition written
// against AWS says exactly that, because on Fargate it is the only thing that
// can be said: awsvpc is the only network mode Fargate supports.
//
// Docker gives every container a namespace of its own, so a task whose
// containers were each created normally gets one address per container and
// `127.0.0.1` that reaches nothing but the caller. Overcast builds the shared
// namespace the way the ECS agent does: a container that does nothing but hold
// a namespace open (the agent calls its own `~internal~ecs~pause`) is started
// first, the task's networking is attached to *that*, and every application
// container is created with `NetworkMode: container:<id>` so it runs inside it.
//
// Everything belonging to the namespace rather than to a process therefore
// belongs to that container: the ENI and the address DescribeTasks reports, the
// task's published ports, and the resolvers and hosts entries that point a task
// at Overcast's API. Docker shares its `/etc/hosts`, `/etc/resolv.conf` and
// `/etc/hostname` with every container that joins it, and rejects a container
// that tries to name any of them for itself.

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"go.uber.org/zap"

	"github.com/overcast-sh/overcast/internal/containerendpoint"
	"github.com/overcast-sh/overcast/internal/dataplane"
	"github.com/overcast-sh/overcast/internal/docker"
)

// taskNamespaceContainerSuffix names the namespace container within its task,
// in the same `overcast-ecs-<cluster>-<task>-<name>` shape its application
// containers use.
//
// The ECS agent calls its own `~internal~ecs~pause`, and the tildes are the
// point: they cannot appear in a container definition's name, so the pause
// container can never collide with one. Docker does not accept them in a
// container name, so dots stand in — an AWS container name is `[a-zA-Z0-9-_]+`,
// which makes a dot exactly as impossible to collide with.
const taskNamespaceContainerSuffix = "internal.ecs.pause"

// taskNamespaceStopTimeout bounds the stop of a namespace container, in
// seconds. Short, because it holds no application state — everything that could
// need time to shut down has already exited inside it.
const taskNamespaceStopTimeout = 5

// taskNamespaceEntrypoint holds the namespace open, and gives it back promptly
// when the task stops.
//
// The trap is what makes the second half true. PID 1 has no default signal
// disposition, so a bare `sleep` would ignore the SIGTERM `docker stop` sends
// and every task teardown would wait out the full timeout before the kill.
var taskNamespaceEntrypoint = []string{"/bin/sh", "-c", "trap 'exit 0' TERM INT; sleep 2147483647 & wait"}

// errTaskNetworkNamespace marks a failure to provision the namespace an awsvpc
// task's containers share.
//
// AWS reports everything that goes wrong provisioning a task's networking as
// ResourceInitializationError whatever the underlying cause, so stoppedReasonFor
// classifies this ahead of its image and container checks: the utility image
// failing to pull is a *networking* failure of the task, not the user's own
// image failing to pull, and reporting it as the latter names an image the task
// definition never mentions.
var errTaskNetworkNamespace = errors.New("unable to provision the task's network namespace")

// taskSharesNetworkNamespace reports whether td's containers run in one shared
// network namespace, which is what awsvpc means and what no other ECS network
// mode does: `bridge` gives each container its own namespace on the docker
// bridge, `host` shares the host's, and `none` has none to share.
func taskSharesNetworkNamespace(td *TaskDefinition, launchType string) bool {
	return effectiveNetworkMode(td, launchType) == "awsvpc"
}

// sharedNamespaceMode is the Docker network mode that puts a container inside
// another container's network namespace.
func sharedNamespaceMode(namespaceID string) string {
	return "container:" + namespaceID
}

// portSurface is the ports one or more container definitions put on a network
// namespace, in the shape Docker's container-create call takes them.
type portSurface struct {
	exposed  map[string]struct{}
	bindings map[string][]docker.PortBinding
}

// portSurfaceFor collects the port mappings of every container definition given.
//
// One definition's ports for a task whose containers each have a namespace of
// their own; the whole task's for one where they share it, since the ports are
// then published on the namespace rather than on any container inside it.
//
// Merging by port key is what makes the second case safe. An awsvpc mapping's
// hostPort must be left blank or equal its containerPort
// (https://docs.aws.amazon.com/AmazonECS/latest/APIReference/API_PortMapping.html),
// so a task's ports are one flat set rather than a per-container mapping, and
// two containers naming the same port collapse to one entry instead of a
// duplicate publish the daemon would reject.
func portSurfaceFor(cds ...ContainerDefinition) portSurface {
	var s portSurface
	for _, cd := range cds {
		for _, pm := range cd.PortMappings {
			proto := pm.Protocol
			if proto == "" {
				proto = "tcp"
			}
			if s.exposed == nil {
				s.exposed = make(map[string]struct{}, len(cd.PortMappings))
				s.bindings = make(map[string][]docker.PortBinding, len(cd.PortMappings))
			}
			key := strconv.Itoa(pm.ContainerPort) + "/" + proto
			s.exposed[key] = struct{}{}
			if pm.HostPort > 0 {
				s.bindings[key] = []docker.PortBinding{
					{HostIP: "0.0.0.0", HostPort: strconv.Itoa(pm.HostPort)},
				}
			}
		}
	}
	return s
}

// applyTaskNetwork puts one container of a task on the task's network.
//
// namespaceID names the container holding the task's namespace, and is empty
// for a task whose containers each get their own — which is the same branch the
// namespace container itself is created through, because it is the container
// that *has* the task's networking.
func (h *Handler) applyTaskNetwork(ccfg *docker.CreateContainerRequest, namespaceID string, endpoint *containerendpoint.Mapper, ports portSurface) {
	if namespaceID != "" {
		// Inside the task's namespace a container has no networking of its own:
		// its address, its resolvers, its hosts file and its published ports all
		// belong to the namespace container and are inherited from it. Docker
		// refuses a create that names any of them again here, which is why they
		// are left off rather than set to the same values.
		ccfg.HostConfig.NetworkMode = sharedNamespaceMode(namespaceID)
		return
	}
	ccfg.ExposedPorts = ports.exposed
	ccfg.HostConfig.NetworkMode = dataplane.Primary(h.cfg)
	ccfg.HostConfig.PortBindings = ports.bindings
	ccfg.HostConfig.ExtraHosts = endpoint.ExtraHosts()
	ccfg.HostConfig.Dns = endpoint.DNSServers()
	ccfg.NetworkingConfig = dataplane.PrimaryEndpoints(h.cfg)
}

// joinTaskDataPlane joins a container to the default data plane before it
// starts: it was created on the control plane, and an application that dials a
// database in its first breath would otherwise race the attachment.
//
// A task placed in a VPC does *not* join it — that is the restriction — unless
// it asked for `assignPublicIp: ENABLED`, which on AWS is what gives an awsvpc
// task a way out of its subnet.
func (h *Handler) joinTaskDataPlane(ctx context.Context, placement awsvpcPlacement, dockerID string) error {
	if placement.networkID != "" && !placement.assignPublicIP {
		return nil
	}
	return dataplane.Attach(ctx, h.docker, h.cfg, dockerID, dataplane.Placement{})
}

// startTaskNamespaceContainer starts the container holding an awsvpc task's
// network namespace and puts the whole of the task's networking on it — its
// port mappings and, for the containers a debug tag names, their debug ports.
// It returns the Docker ID the task's application containers join.
//
// Failures here are the task's rather than any one container's, which is what
// AWS does with them too: a task that cannot provision its ENI is STOPPED with
// a reason of its own and no container blamed for it.
func (h *Handler) startTaskNamespaceContainer(ctx context.Context, task *Task, td *TaskDefinition, clusterName, taskID string, placement awsvpcPlacement, endpoint *containerendpoint.Mapper, debug taskDebugTargets) (string, error) {
	if err := h.puller.Ensure(ctx, docker.UtilityImage); err != nil {
		return "", fmt.Errorf("ecs: %w: %w", errTaskNetworkNamespace, err)
	}

	ccfg := &docker.CreateContainerRequest{
		ContainerConfig: &docker.ContainerConfig{
			Image:      docker.UtilityImage,
			Entrypoint: taskNamespaceEntrypoint,
			Labels:     h.instances.ManagedLabels(ctx, serviceName, clusterName+"/"+taskID),
		},
		// Kept through its die event like a task container, so a namespace that
		// went away can still be inspected after the fact.
		HostConfig: &docker.HostConfig{AutoRemove: false},
	}
	h.applyTaskNetwork(ccfg, "", endpoint, portSurfaceFor(td.ContainerDefinitions...))
	for _, cd := range td.ContainerDefinitions {
		debug.applyPortBinding(ccfg, cd.Name)
	}

	name := fmt.Sprintf("overcast-ecs-%s-%s-%s", clusterName, taskID[:8], taskNamespaceContainerSuffix)
	namespaceID, err := h.puller.CreateContainerWithRetry(ctx, name, ccfg)
	if err != nil {
		return "", fmt.Errorf("ecs: %w: create container: %w", errTaskNetworkNamespace, err)
	}

	if err := h.joinTaskDataPlane(ctx, placement, namespaceID); err != nil {
		_ = h.docker.RemoveContainerForce(namespaceID)
		return "", fmt.Errorf("ecs: %w: %w", errTaskNetworkNamespace, err)
	}

	// Started without the caller's publication guard, unlike a task container:
	// this one's exit is not recorded against the task, so there is no event to
	// lose, and taking the guard here would hold the task's lock across every
	// application image pull still to come.
	if err := h.docker.StartContainer(ctx, namespaceID); err != nil {
		_ = h.docker.RemoveContainerForce(namespaceID)
		return "", fmt.Errorf("ecs: %w: start container: %w", errTaskNetworkNamespace, err)
	}

	if placement.networkID != "" {
		// The namespace holds the task's one ENI, so the address it is given is
		// the task's: what DescribeTasks reports and what a target group is
		// registered with is now the address every container in the task really
		// answers on, rather than the first container's alone.
		if err := h.attachTaskENI(ctx, task, placement, namespaceID, true); err != nil {
			_ = h.docker.RemoveContainerForce(namespaceID)
			return "", fmt.Errorf("ecs: %w: connect to VPC network %s: %w", errTaskNetworkNamespace, placement.networkID, err)
		}
	}
	return namespaceID, nil
}

// retireTaskNamespaceContainer tears down the container holding a task's
// network namespace, handing the removal to the container GC when one is
// wired.
//
// It outlives the application containers that ran inside it, exactly as a
// task's ENI outlives any one container on AWS, so nothing removes it as a side
// effect of their exits: every path that ends a task has to call this or its
// synchronous sibling below. A task that never had one — anything but awsvpc —
// is a no-op.
//
// This deferred form is for the paths whose application containers also go to
// the GC: the exit notifier's die-event handling, and StopTask, which answers
// before the teardown lands the way AWS's own asynchronous StopTask does.
func (h *Handler) retireTaskNamespaceContainer(ctx context.Context, task *Task) {
	if task == nil || task.NetworkNamespaceID == "" || !h.dockerReady.Load() {
		return
	}
	id := task.NetworkNamespaceID
	if h.gc != nil {
		h.gc.StopNow(id)
		h.gc.ScheduleRemove(id)
		return
	}
	h.removeTaskNamespaceContainerNow(ctx, id)
}

// retireTaskNamespaceContainerNow tears the namespace container down before
// returning, GC or no GC.
//
// It is for the paths that already remove the task's application containers
// inline — the service scheduler's retireTaskContainers and a failed launch's
// unwind — where the namespace container was the one container still handed to
// the GC's background queue. That asymmetry is what the compat runner's
// post-run audit kept reporting: a suite finishes in seconds, the audit lists
// the daemon before the GC's remove loop reaches the queue, and every service
// task's pause container shows up as leaked while its application containers —
// removed inline — do not. On a path that is already making Docker calls per
// application container, one more round trip buys "the call returned" meaning
// the task is actually gone.
func (h *Handler) retireTaskNamespaceContainerNow(ctx context.Context, task *Task) {
	if task == nil || task.NetworkNamespaceID == "" || !h.dockerReady.Load() {
		return
	}
	h.removeTaskNamespaceContainerNow(ctx, task.NetworkNamespaceID)
}

// removeTaskNamespaceContainerNow stops and removes a namespace container on
// the caller's goroutine, honouring ECSKeepContainers the way every other
// teardown does.
func (h *Handler) removeTaskNamespaceContainerNow(ctx context.Context, id string) {
	if err := h.docker.StopContainer(ctx, id, taskNamespaceStopTimeout); err != nil {
		h.log.WithRecorder(ctx).Debug("ecs: failed to stop a task's network namespace container",
			zap.String("container", id), zap.Error(err))
	}
	if h.cfg == nil || !h.cfg.ECSKeepContainers {
		_ = h.docker.RemoveContainerForce(id)
	}
}
