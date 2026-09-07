package ecs

// debugger.go — the ECS side of internal/debugger: resolving a task's debug
// targets from its task definition's tags at task start, one per container
// the tags name, wiring each container to its target's port, and letting the
// targets go when the task stops. See docs/plans/compute-debugger.md § 5.
//
// Every entry point here is nil-safe against a handler built without a
// Manager, and a task nothing asked to debug costs the start path one nil
// check per container: the targets are resolved once, before any container is
// created, and ride through startTaskContainers as a map that is nil for the
// undebugged majority.
//
// Tasks have no deadline to suspend and are not recycled the way Lambda
// environments are: a stopped task's targets are released outright, and a
// task definition is not a target — it is the tag carrier the setup block
// names.

import (
	"context"
	"sort"
	"strings"
	"sync"

	"go.uber.org/zap"

	"github.com/overcast-sh/overcast/internal/dataplane"
	"github.com/overcast-sh/overcast/internal/debugger"
	"github.com/overcast-sh/overcast/internal/docker"
)

// taskDebugTargets is the debug target per container name for one task,
// resolved once at task start. Nil for a task nothing asked to debug; every
// method tolerates a nil map, so the start path needs no guard of its own.
type taskDebugTargets map[string]*debugger.Target

// debugPortOwners records which task holds the port a task definition's tags
// asked for, keyed by task definition ARN and container. A port belongs to one
// container, so a second task from the same definition — a service scaled to
// two, or a task rerun before the first was stopped — runs undebugged with a
// warning naming the task that has it, and the target stays with the first
// until that task stops.
type debugPortOwners struct {
	mu     sync.Mutex
	byPort map[string]string // taskDefinitionArn + "/" + container → task ID
}

func debugPortKey(taskDefinitionArn, container string) string {
	return taskDefinitionArn + "/" + container
}

// holder is the task that owns the port, or "" when it is free.
func (o *debugPortOwners) holder(key string) string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.byPort[key]
}

func (o *debugPortOwners) claim(key, taskID string) {
	o.mu.Lock()
	if o.byPort == nil {
		o.byPort = map[string]string{}
	}
	o.byPort[key] = taskID
	o.mu.Unlock()
}

// release frees the port only if taskID still holds it.
func (o *debugPortOwners) release(key, taskID string) {
	o.mu.Lock()
	if o.byPort[key] == taskID {
		delete(o.byPort, key)
	}
	o.mu.Unlock()
}

// debugTargets resolves and registers task's debug targets from the task
// definition's tags: a suffixed key names its container, the bare key the
// single container (debugger.SpecsFromTaskTags). Nil when the handler has no
// manager or nothing asks for a debugger. A tag that cannot be honoured is
// logged with its fix and dropped, so the task runs as it would without it —
// AWS accepts any tag, so no tag may fail a placement. The remote root is
// not set here: it needs the image, which the container loop pulls.
//
// Resolution has no runtime string to go on, so it is the protocol tag, then a
// debug flag already in the container's own environment, then passthrough.
// A container tagged for nothing but carrying such a flag is proxied too, when
// the flag is on: the tag's job is to ask for a port, and that container has
// already asked.
func (h *Handler) debugTargets(td *TaskDefinition, taskID string, tags map[string]string, overrides map[string]*ContainerOverride, hotReload map[string]string) taskDebugTargets {
	if h.debugger == nil || h.cfg == nil {
		return nil
	}
	flagOn := h.cfg.ECSDebugger
	if !flagOn && len(tags) == 0 {
		return nil
	}
	names := make([]string, 0, len(td.ContainerDefinitions))
	for _, cd := range td.ContainerDefinitions {
		names = append(names, cd.Name)
	}
	specs, problems := debugger.SpecsFromTaskTags(tags, names, flagOn)
	for _, p := range problems {
		h.log.Warn("ecs: debugger: "+p.Reason,
			append([]zap.Field{zap.String("task_definition", td.TaskDefinitionArn)}, p.Fields()...)...)
	}

	var targets taskDebugTargets
	for i := range td.ContainerDefinitions {
		cd := &td.ContainerDefinitions[i]
		spec, tagged := specs[cd.Name]
		if !tagged {
			if !flagOn {
				continue
			}
			spec = debugger.Spec{Service: debugger.ServiceECS, Container: cd.Name, FlagOn: flagOn}
		}
		if spec.SourcePathRaw == "" {
			spec.SourcePathRaw, spec.SourcePath = hotReloadRoot(cd, tags, hotReload)
		}
		res := debugger.Default.Resolve(spec, "", containerEnvMap(cd, overrides[cd.Name]))
		if res.Protocol == nil && !spec.Tagged {
			continue
		}

		portKey := debugPortKey(td.TaskDefinitionArn, cd.Name)
		if spec.Enabled() {
			if owner := h.debugPorts.holder(portKey); owner != "" && owner != taskID {
				if _, live := h.debugger.Get(debugger.TargetID(debugger.ServiceECS, owner, cd.Name)); live {
					h.log.Warn("ecs: debugger: port already in use by task "+owner+" — this task runs undebugged",
						zap.String("task_definition", td.TaskDefinitionArn),
						zap.String("task", taskID),
						zap.String("container", cd.Name),
						zap.String("hint", "a debug port belongs to one container; stop task "+owner+" to move the debugger to a newer task, or run one task of this definition while debugging"))
					continue
				}
				h.debugPorts.release(portKey, owner)
			}
		}

		target, err := h.debugger.Ensure(debugger.TargetID(debugger.ServiceECS, taskID, cd.Name), spec, res)
		if err != nil {
			// ErrNothingToDebug cannot happen past the check above; a closed
			// manager means Overcast is shutting down.
			continue
		}
		target.SetResourceARN(td.TaskDefinitionArn)
		if spec.Enabled() && target.State() != debugger.StateError {
			h.debugPorts.claim(portKey, taskID)
		}
		if target.State() == debugger.StateError {
			h.log.Warn("ecs: debugger: target could not be bound — the container runs undebugged",
				zap.String("task_definition", td.TaskDefinitionArn),
				zap.String("task", taskID),
				zap.String("container", cd.Name),
				zap.String("reason", target.Reason()),
				zap.String("hint", "free the port named by "+debugger.TagPort+", or drop the tag to auto-allocate one from OVERCAST_DEBUGGER_PORTS"))
		}
		if targets == nil {
			targets = taskDebugTargets{}
		}
		targets[cd.Name] = target
	}
	return targets
}

// hotReloadRoot is the local root a container's redirected mount implies: the
// hot-reload path of the first of its mount points that a tag redirected, as
// the user wrote it and as normalised, or empty when none is. It is the ECS
// spelling of the Lambda fallback from overcast:source-path to
// overcast:hot-reload-path — here the tag names a volume, so the package
// cannot resolve it and the mount points decide.
func hotReloadRoot(cd *ContainerDefinition, tags, hotReload map[string]string) (raw, normalised string) {
	for _, mp := range cd.MountPoints {
		path := hotReload[mp.SourceVolume]
		if path == "" {
			continue
		}
		written, ok := tags[hotReloadTagPrefix+mp.SourceVolume]
		if !ok {
			written = tags[hotReloadTagBare]
		}
		return strings.TrimSpace(written), path
	}
	return "", ""
}

// envMap turns "K=V" entries into a map, last value winning — matching how
// Docker resolves duplicate environment keys.
func envMap(env []string) map[string]string {
	out := make(map[string]string, len(env))
	for _, kv := range env {
		if k, v, ok := strings.Cut(kv, "="); ok {
			out[k] = v
		}
	}
	return out
}

// containerEnvMap is the container's own environment — the definition's, with
// a RunTask override applied last as ECS does — for protocol detection. The
// values Overcast adds (the endpoint, secrets) carry no debug flag, so they
// are not built here.
func containerEnvMap(cd *ContainerDefinition, co *ContainerOverride) map[string]string {
	env := make(map[string]string, len(cd.Environment))
	for _, kv := range cd.Environment {
		env[kv.Name] = kv.Value
	}
	if co != nil {
		for _, kv := range co.Environment {
			env[kv.Name] = kv.Value
		}
	}
	return env
}

// setRemoteRoot records the container path editors map the local source
// root to, once the named container's image is present: the definition's
// workingDirectory override, else the image's own WORKDIR, else the root. The
// image inspect is one local daemon call, made only for a debugged container.
func (d taskDebugTargets) setRemoteRoot(ctx context.Context, h *Handler, cd *ContainerDefinition) {
	target, ok := d[cd.Name]
	if !ok {
		return
	}
	root := "/"
	switch {
	case cd.WorkingDirectory != "":
		root = cd.WorkingDirectory
	case h.docker != nil:
		inspect, err := h.docker.InspectImage(ctx, cd.Image)
		switch {
		case err != nil:
			h.log.Debug("ecs: debugger: inspect image for its working directory — remote root defaults to /",
				zap.String("container", cd.Name), zap.String("image", cd.Image), zap.Error(err))
		case inspect.Config.WorkingDir != "":
			root = inspect.Config.WorkingDir
		}
	}
	target.SetRemoteRoot(root)
}

// inject adds the named container's protocol flag and OVERCAST_DEBUG_PORT to
// its Docker environment, appending to a value the definition already set
// rather than replacing it. An unknown container's environment is returned
// unchanged.
func (d taskDebugTargets) inject(container string, env []string) []string {
	target, ok := d[container]
	if !ok {
		return env
	}
	before := envMap(env)
	after := make(map[string]string, len(before)+2)
	for k, v := range before {
		after[k] = v
	}
	target.Inject(after)

	// Changed keys are rewritten in place; new ones are appended in a fixed
	// order, so the create request reads the same on every run.
	out := make([]string, 0, len(env)+2)
	seen := make(map[string]struct{}, len(after))
	for _, kv := range env {
		k, _, _ := strings.Cut(kv, "=")
		if _, dup := seen[k]; dup {
			continue
		}
		seen[k] = struct{}{}
		out = append(out, k+"="+after[k])
	}
	added := make([]string, 0, 2)
	for k := range after {
		if _, had := seen[k]; !had {
			added = append(added, k)
		}
	}
	sort.Strings(added)
	for _, k := range added {
		out = append(out, k+"="+after[k])
	}
	return out
}

// applyPortBinding exposes the named container's debug port on ccfg —
// the container itself, or the namespace container an awsvpc task's
// containers share, which is the one whose HostConfig carries the task's
// port bindings. Called after applyTaskNetwork, which replaces the port
// maps wholesale.
func (d taskDebugTargets) applyPortBinding(ccfg *docker.CreateContainerRequest, container string) {
	if target, ok := d[container]; ok {
		target.ApplyPortBinding(ccfg.ContainerConfig, ccfg.HostConfig)
	}
}

// bindDebugTarget points the target's proxy at the started container, per
// docs/plans/compute-debugger.md § 3.6: the address Overcast can route to
// when it is itself in Docker, else the ephemeral loopback port Docker
// published for the debug port. Both are read from ownerID, the container
// whose network the debugged one runs in — itself, or the task's namespace
// container under awsvpc — while containerID is what the console names and
// what a die event later clears. Failure leaves the target unbound with a
// warning; the task still runs.
func (h *Handler) bindDebugTarget(ctx context.Context, target *debugger.Target, ownerID, containerID string) {
	var inspect *docker.ContainerInspect
	containerAddr := dataplane.ContainerAddr(ctx, h.docker, h.cfg, ownerID)
	if containerAddr == "" {
		// Host ports are assigned at start, so nothing read before it could
		// have carried this one.
		var err error
		if inspect, err = h.docker.InspectContainer(ctx, ownerID); err != nil {
			h.log.Warn("ecs: debugger: inspect started container for its published debug port — target left unbound",
				zap.String("target", target.ID()), zap.String("container", ownerID), zap.Error(err))
			return
		}
	}
	upstream, ok := target.UpstreamFor(containerAddr, inspect)
	if !ok {
		h.log.Warn("ecs: debugger: container published no host port for the debug port — target left unbound",
			zap.String("target", target.ID()),
			zap.String("container", ownerID),
			zap.Int("port", target.Port()),
			zap.String("hint", "check that the Docker daemon can publish ports on 127.0.0.1 (a rootless or remote daemon may not)"))
		return
	}
	target.SetUpstream(upstream)
	target.SetContainerID(containerID)
	h.log.Debug("ecs: debugger: target bound", zap.String("target", target.ID()), zap.String("upstream", upstream))
}

// clearDebugContainer forgets the upstream behind the target of the task
// container that just exited, so nothing is dialled at a port that is gone.
// Keyed by container id, so a stale event about a container already replaced
// leaves the replacement alone.
func (h *Handler) clearDebugContainer(task *Task, dockerID string) {
	if h.debugger == nil {
		return
	}
	taskID := extractTaskID(task.TaskArn)
	for _, c := range task.Containers {
		if c.DockerID != dockerID {
			continue
		}
		if target, ok := h.debugger.Get(debugger.TargetID(debugger.ServiceECS, taskID, c.Name)); ok {
			target.ClearContainer(dockerID)
		}
		return
	}
}

// releaseDebugTargets closes the ports of a task that has stopped — however
// it stopped — and gives them back to the next task of its definition.
func (h *Handler) releaseDebugTargets(task *Task) {
	if h.debugger == nil || task == nil {
		return
	}
	taskID := extractTaskID(task.TaskArn)
	for _, c := range task.Containers {
		id := debugger.TargetID(debugger.ServiceECS, taskID, c.Name)
		if _, ok := h.debugger.Get(id); !ok {
			continue
		}
		h.debugger.Release(id)
		h.debugPorts.release(debugPortKey(task.TaskDefinitionArn, c.Name), taskID)
	}
}

// DescribeUntagged implements debugger.Describer for tasks: a task no tag
// mentions gets the synthesised "not tagged" entry for its first container
// (the endpoint's ?container= names another), with the task definition's ARN
// in the setup block, since that is the resource the tag goes on. A task the
// caller's region does not hold, or another service's resource, is not ours
// to describe.
func (s *Service) DescribeUntagged(ctx context.Context, service debugger.Service, resource string) (debugger.Descriptor, bool) {
	if service != debugger.ServiceECS {
		return debugger.Descriptor{}, false
	}
	task, ok := s.handler.findTask(ctx, resource)
	if !ok || len(task.Containers) == 0 {
		return debugger.Descriptor{}, false
	}
	return debugger.UntaggedDescriptor(debugger.ServiceECS, resource, task.Containers[0].Name, task.TaskDefinitionArn), true
}

// findTask locates a task by id alone, across the region's clusters — the
// console's debugger endpoint names a task without its cluster.
func (h *Handler) findTask(ctx context.Context, taskID string) (*Task, bool) {
	tasks, aerr := h.store.listAllTasks(ctx)
	if aerr != nil {
		return nil, false
	}
	for i := range tasks {
		if extractTaskID(tasks[i].TaskArn) == taskID {
			return &tasks[i], true
		}
	}
	return nil, false
}
