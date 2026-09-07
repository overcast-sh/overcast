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
//
// TODO(priority:P2): a task that survives an Overcast restart is reconciled
// back to RUNNING (reconcileContainers) but its targets are not re-registered,
// so the console shows it as untagged while its container still listens.

import (
	"context"
	"sort"
	"strings"
	"sync"

	"go.uber.org/zap"

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

// claim takes the port for taskID unless another task still holds it, in
// which case that task is returned and nothing changes. live reports whether
// a recorded holder still has its target: one that lost it without a release,
// however that happened, is displaced rather than honoured. The decision and
// the record are one critical section, so two tasks of one definition
// starting together — a RunTask and the service scheduler's placement —
// cannot both find the port free.
func (o *debugPortOwners) claim(key, taskID string, live func(owner string) bool) (holder string, ok bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if owner := o.byPort[key]; owner != "" && owner != taskID && live(owner) {
		return owner, false
	}
	if o.byPort == nil {
		o.byPort = map[string]string{}
	}
	o.byPort[key] = taskID
	return "", true
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
	debugger.WarnProblems(h.log, problems, zap.String("task_definition", td.TaskDefinitionArn))

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

		// A target that will listen — the flag on and a protocol resolved,
		// whether a tag or the container's own flag asked for it — takes the
		// definition's port, which one task holds at a time.
		portKey := debugPortKey(td.TaskDefinitionArn, cd.Name)
		listens := flagOn && res.Protocol != nil
		if listens {
			holder, ok := h.debugPorts.claim(portKey, taskID, func(owner string) bool {
				_, live := h.debugger.Get(debugger.TargetID(debugger.ServiceECS, owner, cd.Name))
				return live
			})
			if !ok {
				h.log.Warn("ecs: debugger: port already held by another task of this definition — this task runs undebugged",
					zap.String("task_definition", td.TaskDefinitionArn),
					zap.String("task", taskID),
					zap.String("container", cd.Name),
					zap.String("holder", holder),
					zap.String("hint", "a debug port belongs to one container; stop the holder to move the debugger to a newer task, or run one task of this definition while debugging"))
				continue
			}
		}

		target, err := h.debugger.Ensure(debugger.TargetID(debugger.ServiceECS, taskID, cd.Name), spec, res)
		if err != nil || (listens && !target.Bound()) {
			// A closed manager means Overcast is shutting down; a target that
			// could not bind its port is the manager's to warn about, and it
			// holds no claim on the definition's port.
			h.debugPorts.release(portKey, taskID)
		}
		if err != nil {
			continue
		}
		target.SetResourceARN(td.TaskDefinitionArn)
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
	case h.docker != nil && target.Bound():
		// Only for a container an editor can attach to; an inert or errored
		// target is not worth a daemon round trip.
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

// bind points the named container's target at it once it runs (Target.Bind):
// the debug port is read from ownerID, the container whose network the
// debugged one runs in — itself, or the task's namespace container under
// awsvpc — while containerID is what the console names and what a die event
// later clears.
func (d taskDebugTargets) bind(ctx context.Context, h *Handler, container, ownerID, containerID string) {
	if target, ok := d[container]; ok {
		target.Bind(ctx, h.docker, h.cfg, ownerID, containerID)
	}
}

// clearDebugContainer forgets the upstream behind the target of the task
// container that just exited, so nothing is dialled at a port that is gone.
// Keyed by container id, so a stale event about a container already replaced
// leaves the replacement alone. The namespace container an awsvpc task's
// ports are published on is not in task.Containers; when it dies first the
// upstream lingers until the application containers' own die events, which
// follow at once.
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
		h.log.Debug("ecs: debugger: list tasks for the console's target lookup", zap.String("task", taskID), zap.Error(aerr))
		return nil, false
	}
	for i := range tasks {
		if extractTaskID(tasks[i].TaskArn) == taskID {
			return &tasks[i], true
		}
	}
	return nil, false
}
