package ecs

// debugger_test.go — the ECS side of internal/debugger, touch point by touch
// point (docs/plans/compute-debugger.md § 5 and § 9), against the fake daemon
// so the request shapes are the real ones.

import (
	"context"
	"encoding/json"
	"net"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"

	"github.com/overcast-sh/overcast/internal/clock"
	"github.com/overcast-sh/overcast/internal/config"
	"github.com/overcast-sh/overcast/internal/debugger"
	"github.com/overcast-sh/overcast/internal/debugger/debuggertest"
	"github.com/overcast-sh/overcast/internal/events"
	"github.com/overcast-sh/overcast/internal/middleware"
	"github.com/overcast-sh/overcast/internal/state"
)

// debugTestHandler is newECSDockerTestHandler with a debugger manager wired,
// the ECS flag as asked, and every WARN the handler logs captured.
type debugTestHandler struct {
	h    *Handler
	fd   *fakeECSDockerDaemon
	m    *debugger.Manager
	logs *observer.ObservedLogs

	// registered records that runTask has registered its task definition, so
	// a second run of the same test reuses the revision — and its ARN, which
	// is what a debug port's ownership is keyed by.
	registered bool
}

func newDebugTestHandler(t *testing.T, flagOn bool) *debugTestHandler {
	t.Helper()
	core, logs := observer.New(zap.WarnLevel)
	clk := clock.NewMock()
	m := debuggertest.NewManager(t, clk, config.DebuggerTimeoutAttached)
	svc := New(&config.Config{Region: "us-east-1", AccountID: "123456789012", ECSDebugger: flagOn, ECSHotReload: true},
		state.NewMemoryStore(), zap.New(core), clk, m)
	h := svc.handler
	fd := wireFakeDocker(t, h)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		h.scheduler.Stop(ctx)
	})
	return &debugTestHandler{h: h, fd: fd, m: m, logs: logs}
}

// warned reports whether a WARN naming text was logged.
func (d *debugTestHandler) warned(text string) bool {
	for _, entry := range d.logs.All() {
		if strings.Contains(entry.Message, text) {
			return true
		}
		for _, f := range entry.Context {
			if strings.Contains(f.String, text) {
				return true
			}
		}
	}
	return false
}

// taskDefinitionWith is a bridge-mode task definition of the given
// containers, tagged as given.
func taskDefinitionWith(tags map[string]string, containers ...map[string]any) map[string]any {
	td := map[string]any{"family": "debugged", "containerDefinitions": containers}
	if len(tags) > 0 {
		list := make([]map[string]string, 0, len(tags))
		for k, v := range tags {
			list = append(list, map[string]string{"key": k, "value": v})
		}
		td["tags"] = list
	}
	return td
}

// runTask registers td on its first call, runs one task of it in cluster c1
// and returns the task's id. Every container the daemon creates for it is
// then in fd.
func (d *debugTestHandler) runTask(t *testing.T, td map[string]any, run map[string]any) string {
	t.Helper()
	ctx := context.Background()
	if !d.registered {
		if w := postJSON(t, ctx, d.h.CreateCluster, map[string]any{"clusterName": "c1"}); w.Code != 200 {
			t.Fatalf("CreateCluster: HTTP %d: %s", w.Code, w.Body.String())
		}
		if w := postJSON(t, ctx, d.h.RegisterTaskDefinition, td); w.Code != 200 {
			t.Fatalf("RegisterTaskDefinition: HTTP %d: %s", w.Code, w.Body.String())
		}
		d.registered = true
	}
	if run == nil {
		run = map[string]any{}
	}
	run["cluster"], run["taskDefinition"] = "c1", td["family"]
	w := postJSON(t, ctx, d.h.RunTask, run)
	if w.Code != 200 {
		t.Fatalf("RunTask: HTTP %d: %s", w.Code, w.Body.String())
	}
	var out struct {
		Tasks []struct {
			TaskArn       string `json:"taskArn"`
			LastStatus    string `json:"lastStatus"`
			StoppedReason string `json:"stoppedReason"`
		} `json:"tasks"`
	}
	decodeBody(t, w, &out)
	if len(out.Tasks) != 1 {
		t.Fatalf("RunTask placed %d tasks, want 1", len(out.Tasks))
	}
	if out.Tasks[0].LastStatus == "STOPPED" {
		t.Fatalf("task failed to start: %s", out.Tasks[0].StoppedReason)
	}
	return extractTaskID(out.Tasks[0].TaskArn)
}

// decodeBody reads a recorded response's JSON body into v.
func decodeBody(t *testing.T, w *httptest.ResponseRecorder, v any) {
	t.Helper()
	if err := json.Unmarshal(w.Body.Bytes(), v); err != nil {
		t.Fatalf("decode response: %v: %s", err, w.Body.String())
	}
}

// created returns the container the daemon created for the named definition.
func (d *debugTestHandler) created(t *testing.T, name string) createdContainer {
	t.Helper()
	for _, c := range d.fd.createdContainers() {
		if c.placedAs(name) {
			return c
		}
	}
	t.Fatalf("no container created for %q", name)
	return createdContainer{}
}

// publishedHostPort is the ephemeral host port the fake daemon reports for
// the target's debug port on the container that carries the binding.
func (d *debugTestHandler) publishedHostPort(t *testing.T, target *debugger.Target, containerID string) string {
	t.Helper()
	bindings := d.fd.publishedPorts(containerID)[strconv.Itoa(target.Port())+"/tcp"]
	if len(bindings) == 0 {
		t.Fatalf("container %s publishes no host port for %d", containerID, target.Port())
	}
	return bindings[0].HostPort
}

func assertNoDebugSurface(t *testing.T, c createdContainer) {
	t.Helper()
	if len(c.req.ExposedPorts) != 0 || len(c.req.HostConfig.PortBindings) != 0 {
		t.Errorf("container %q exposes %v / publishes %v, want nothing", c.name, c.req.ExposedPorts, c.req.HostConfig.PortBindings)
	}
	if _, leaked := envMap(c.req.Env)[debugger.DebugPortEnv]; leaked {
		t.Errorf("container %q carries %s", c.name, debugger.DebugPortEnv)
	}
}

func assertDebugSurface(t *testing.T, target *debugger.Target, c createdContainer) {
	t.Helper()
	key := strconv.Itoa(target.Port()) + "/tcp"
	if _, ok := c.req.ExposedPorts[key]; !ok {
		t.Errorf("container %q exposes %v, want %s", c.name, c.req.ExposedPorts, key)
	}
	if got := c.req.HostConfig.PortBindings[key]; len(got) != 1 || got[0].HostIP != "127.0.0.1" || got[0].HostPort != "0" {
		t.Errorf("container %q publishes %v for %s, want an ephemeral loopback binding", c.name, got, key)
	}
}

// ─── resolution per container ────────────────────────────────────────────────

func TestRunTask_debugger_suffixedTagsResolvePerContainer(t *testing.T) {
	// Given: the debugger on, and a two-container bridge task whose tags name
	// the app container alone — with its own NODE_OPTIONS and an image that
	// declares a working directory
	d := newDebugTestHandler(t, true)
	d.fd.setImageWorkingDir("node:20-alpine", "/app")
	td := taskDefinitionWith(map[string]string{
		debugger.TagDebug + "/app":    "true",
		debugger.TagProtocol + "/app": "inspector",
	},
		map[string]any{"name": "app", "image": "node:20-alpine",
			"environment": []map[string]string{{"name": "NODE_OPTIONS", "value": "--enable-source-maps"}}},
		map[string]any{"name": "sidecar", "image": "busybox"},
	)

	// When: the task runs
	taskID := d.runTask(t, td, nil)
	app, sidecar := d.created(t, "app"), d.created(t, "sidecar")

	// Then: only the app container has a target, and only its environment
	// and its create request carry the debugger
	target, ok := d.m.Get(debugger.TargetID(debugger.ServiceECS, taskID, "app"))
	if !ok {
		t.Fatal("no target registered for the app container")
	}
	if _, stray := d.m.Get(debugger.TargetID(debugger.ServiceECS, taskID, "sidecar")); stray {
		t.Error("a target was registered for the untagged sidecar")
	}
	port := strconv.Itoa(target.Port())
	env := envMap(app.req.Env)
	if got, want := env["NODE_OPTIONS"], "--enable-source-maps --inspect=0.0.0.0:"+port; got != want {
		t.Errorf("app NODE_OPTIONS = %q, want %q", got, want)
	}
	if got := env[debugger.DebugPortEnv]; got != port {
		t.Errorf("app %s = %q, want %s", debugger.DebugPortEnv, got, port)
	}
	assertDebugSurface(t, target, app)
	assertNoDebugSurface(t, sidecar)
	if _, has := envMap(sidecar.req.Env)["NODE_OPTIONS"]; has {
		t.Error("the sidecar was given NODE_OPTIONS")
	}

	// And: the started container is behind the port, described for the
	// console with the image's working directory and the definition's ARN
	desc := target.Descriptor()
	if desc.Protocol != "inspector" || desc.ProtocolSource != string(debugger.SourceTag) {
		t.Errorf("protocol = %s from %s, want inspector from the tag", desc.Protocol, desc.ProtocolSource)
	}
	if want := "127.0.0.1:" + d.publishedHostPort(t, target, app.id); desc.Upstream != want {
		t.Errorf("upstream = %q, want %q", desc.Upstream, want)
	}
	if desc.ContainerID != app.id || desc.State != string(debugger.StateListening) {
		t.Errorf("container = %q state = %s, want %q listening", desc.ContainerID, desc.State, app.id)
	}
	if desc.RemoteRoot != "/app" {
		t.Errorf("remoteRoot = %q, want the image's /app", desc.RemoteRoot)
	}
	if desc.Resource != taskID || desc.Container != "app" {
		t.Errorf("resource/container = %q/%q, want %q/app", desc.Resource, desc.Container, taskID)
	}
	if !strings.Contains(desc.Setup.TagCLI, ":task-definition/debugged:1") {
		t.Errorf("setup.tagCli = %q, want the task definition ARN", desc.Setup.TagCLI)
	}
	if d.logs.Len() != 0 {
		t.Errorf("unexpected warnings: %v", d.logs.All())
	}
}

func TestRunTask_debugger_bareTagIsAmbiguousAcrossContainers(t *testing.T) {
	// Given: a two-container task with the bare tag
	d := newDebugTestHandler(t, true)
	td := taskDefinitionWith(map[string]string{debugger.TagDebug: "true"},
		map[string]any{"name": "app", "image": "busybox"},
		map[string]any{"name": "sidecar", "image": "busybox"},
	)

	// When: the task runs
	d.runTask(t, td, nil)

	// Then: nothing is debugged, the containers are created as untagged ones
	// would be, and the warning names the suffixed spelling
	if targets := d.m.List(); len(targets) != 0 {
		t.Errorf("targets = %v, want none", targets)
	}
	assertNoDebugSurface(t, d.created(t, "app"))
	assertNoDebugSurface(t, d.created(t, "sidecar"))
	if !d.warned("ambiguous") || !d.warned(debugger.TagDebug+"/app") {
		t.Errorf("warnings = %v, want one calling the bare tag ambiguous and naming %s/app", d.logs.All(), debugger.TagDebug)
	}
}

func TestRunTask_debugger_hotReloadTagsAreNotRefusedAsContainers(t *testing.T) {
	// Given: a hot-reload tag naming a volume beside a debug tag naming the
	// one container, and the source path falling back to that volume's path
	d := newDebugTestHandler(t, true)
	td := taskDefinitionWith(map[string]string{
		debugger.TagDebug:             "true",
		hotReloadTagPrefix + "src":    "/host/app",
		hotReloadTagPrefix + "vendor": "/host/vendor",
	},
		map[string]any{"name": "app", "image": "busybox",
			"mountPoints": []map[string]any{{"sourceVolume": "src", "containerPath": "/app"}}},
	)
	td["volumes"] = []map[string]any{{"name": "src"}, {"name": "vendor"}}

	// When: the task runs
	taskID := d.runTask(t, td, nil)

	// Then: the debugger does not mistake the volume names for containers, and
	// the local root is the mount the container actually has
	target, ok := d.m.Get(debugger.TargetID(debugger.ServiceECS, taskID, "app"))
	if !ok {
		t.Fatal("no target registered")
	}
	if got := target.Descriptor().LocalRoot; got != "/host/app" {
		t.Errorf("localRoot = %q, want the redirected mount's /host/app", got)
	}
	if d.logs.Len() != 0 {
		t.Errorf("unexpected warnings: %v", d.logs.All())
	}
}

func TestRunTask_debugger_sourcePathTagWinsOverTheRedirectedMount(t *testing.T) {
	// Given: both a source-path tag and a redirected mount
	d := newDebugTestHandler(t, true)
	td := taskDefinitionWith(map[string]string{
		debugger.TagDebug:          "true",
		debugger.TagSourcePath:     "/host/src",
		hotReloadTagPrefix + "src": "/host/app",
	},
		map[string]any{"name": "app", "image": "busybox",
			"mountPoints": []map[string]any{{"sourceVolume": "src", "containerPath": "/app"}}},
	)
	td["volumes"] = []map[string]any{{"name": "src"}}

	// When: the task runs
	taskID := d.runTask(t, td, nil)

	// Then: the explicit tag is the local root
	target, _ := d.m.Get(debugger.TargetID(debugger.ServiceECS, taskID, "app"))
	if got := target.Descriptor().LocalRoot; got != "/host/src" {
		t.Errorf("localRoot = %q, want the source-path tag's /host/src", got)
	}
}

func TestRunTask_debugger_flagOffRegistersAnInertTarget(t *testing.T) {
	// Given: a tagged task on a server whose ECS debugger flag is off
	d := newDebugTestHandler(t, false)
	td := taskDefinitionWith(map[string]string{debugger.TagDebug: "true"},
		map[string]any{"name": "app", "image": "busybox"})

	// When: the task runs
	taskID := d.runTask(t, td, nil)

	// Then: the console can explain why the debugger is off, and the container
	// was created exactly as an untagged one would be
	target, ok := d.m.Get(debugger.TargetID(debugger.ServiceECS, taskID, "app"))
	if !ok {
		t.Fatal("no inert target registered for the tagged task")
	}
	if target.State() != debugger.StateInert || !strings.Contains(target.Reason(), "OVERCAST_ECS_DEBUGGER") {
		t.Errorf("state = %s reason = %q, want inert naming the flag", target.State(), target.Reason())
	}
	assertNoDebugSurface(t, d.created(t, "app"))
	if !d.warned("OVERCAST_ECS_DEBUGGER=true") {
		t.Errorf("warnings = %v, want one naming the flag", d.logs.All())
	}
}

// ─── the port's owner ────────────────────────────────────────────────────────

func TestRunTask_debugger_awsvpcPublishesThePortOnTheNamespaceContainer(t *testing.T) {
	// Given: a Fargate task whose one container is tagged
	d := newDebugTestHandler(t, true)
	td := taskDefinitionWith(map[string]string{debugger.TagDebug: "true", debugger.TagProtocol: "inspector"},
		map[string]any{"name": "app", "image": "node:20-alpine"})
	td["networkMode"], td["requiresCompatibilities"], td["cpu"], td["memory"] = "awsvpc", []string{"FARGATE"}, "256", "512"

	// When: the task runs
	taskID := d.runTask(t, td, map[string]any{
		"launchType":           "FARGATE",
		"networkConfiguration": map[string]any{"awsvpcConfiguration": map[string]any{"subnets": []string{"subnet-1"}}},
	})
	created := d.fd.createdContainers()
	if len(created) != 2 || !strings.HasSuffix(created[0].name, taskNamespaceContainerSuffix) {
		t.Fatalf("created %d containers, want the namespace container and the app", len(created))
	}
	namespace, app := created[0], created[1]

	// Then: the port is published on the namespace container the app runs
	// inside — the only container Docker lets publish anything — while the
	// app carries the flag, and the proxy dials the namespace's host port
	target, ok := d.m.Get(debugger.TargetID(debugger.ServiceECS, taskID, "app"))
	if !ok {
		t.Fatal("no target registered")
	}
	assertDebugSurface(t, target, namespace)
	if len(app.req.ExposedPorts) != 0 || len(app.req.HostConfig.PortBindings) != 0 {
		t.Errorf("the app container publishes %v of its own; Docker rejects that inside a shared namespace", app.req.HostConfig.PortBindings)
	}
	if got := envMap(app.req.Env)[debugger.DebugPortEnv]; got != strconv.Itoa(target.Port()) {
		t.Errorf("app %s = %q, want %d", debugger.DebugPortEnv, got, target.Port())
	}
	desc := target.Descriptor()
	if want := "127.0.0.1:" + d.publishedHostPort(t, target, namespace.id); desc.Upstream != want {
		t.Errorf("upstream = %q, want the namespace container's %q", desc.Upstream, want)
	}
	if desc.ContainerID != app.id {
		t.Errorf("containerId = %q, want the app container %q", desc.ContainerID, app.id)
	}
}

func TestRunTask_debugger_secondTaskOfTheDefinitionRunsUndebugged(t *testing.T) {
	// Given: a debugged task already running
	d := newDebugTestHandler(t, true)
	td := taskDefinitionWith(map[string]string{debugger.TagDebug: "true"},
		map[string]any{"name": "app", "image": "busybox"})
	first := d.runTask(t, td, nil)
	firstTarget, ok := d.m.Get(debugger.TargetID(debugger.ServiceECS, first, "app"))
	if !ok {
		t.Fatal("the first task has no target")
	}

	// When: a second task of the same definition runs
	second := d.runTask(t, td, nil)

	// Then: it runs undebugged, says so naming the first, and the first keeps
	// its port
	if _, stray := d.m.Get(debugger.TargetID(debugger.ServiceECS, second, "app")); stray {
		t.Error("the second task was given a target of its own")
	}
	assertNoDebugSurface(t, d.fd.createdContainers()[1])
	if !d.warned("already in use by task " + first) {
		t.Errorf("warnings = %v, want one naming task %s", d.logs.All(), first)
	}
	if still, _ := d.m.Get(firstTarget.ID()); still != firstTarget {
		t.Error("the first task's target was replaced")
	}

	// When: the first task stops and a third runs
	if w := postJSON(t, context.Background(), d.h.StopTask, map[string]any{"cluster": "c1", "task": first}); w.Code != 200 {
		t.Fatalf("StopTask: HTTP %d: %s", w.Code, w.Body.String())
	}
	third := d.runTask(t, td, nil)

	// Then: the port moves to it
	if _, ok := d.m.Get(debugger.TargetID(debugger.ServiceECS, third, "app")); !ok {
		t.Error("the third task got no target once the first had stopped")
	}
}

// ─── release ─────────────────────────────────────────────────────────────────

func TestStopTask_debugger_releasesTheTargetAndItsPort(t *testing.T) {
	// Given: a debugged task
	d := newDebugTestHandler(t, true)
	taskID := d.runTask(t, taskDefinitionWith(map[string]string{debugger.TagDebug: "true"},
		map[string]any{"name": "app", "image": "busybox"}), nil)
	target, ok := d.m.Get(debugger.TargetID(debugger.ServiceECS, taskID, "app"))
	if !ok {
		t.Fatal("no target registered")
	}
	port := target.Port()

	// When: it is stopped
	if w := postJSON(t, context.Background(), d.h.StopTask, map[string]any{"cluster": "c1", "task": taskID}); w.Code != 200 {
		t.Fatalf("StopTask: HTTP %d: %s", w.Code, w.Body.String())
	}

	// Then: the target is gone and its port is free again
	if _, still := d.m.Get(target.ID()); still {
		t.Fatal("target still registered after the task stopped")
	}
	ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		t.Fatalf("debug port %d still held after release: %v", port, err)
	}
	_ = ln.Close()
}

func TestContainerDied_debugger_clearsTheContainerThenReleasesOnTheLastExit(t *testing.T) {
	// Given: a two-container task with the app debugged and bound
	d := newDebugTestHandler(t, true)
	taskID := d.runTask(t, taskDefinitionWith(map[string]string{debugger.TagDebug + "/app": "true"},
		map[string]any{"name": "app", "image": "busybox"},
		map[string]any{"name": "sidecar", "image": "busybox"}), nil)
	target, ok := d.m.Get(debugger.TargetID(debugger.ServiceECS, taskID, "app"))
	if !ok || target.Upstream() == "" {
		t.Fatalf("target = %v, want one bound to the app container", target)
	}
	app, sidecar := d.created(t, "app"), d.created(t, "sidecar")
	died := func(containerID string) {
		d.h.handleContainerDied(context.Background(), events.Event{
			Type: events.DockerContainerDied, Time: time.Now(),
			Payload: events.DockerContainerPayload{
				ContainerID: containerID, Action: "die", ExitCode: "0",
				Service: serviceName, ResourceID: "c1/" + taskID,
			},
		})
	}

	// When: the app container exits while the sidecar runs on
	died(app.id)

	// Then: nothing is dialled behind the port, which stays registered
	if got := target.Upstream(); got != "" {
		t.Errorf("upstream after the app exited = %q, want none", got)
	}
	if _, still := d.m.Get(target.ID()); !still {
		t.Fatal("target released while the task still ran")
	}

	// When: the last container exits
	died(sidecar.id)

	// Then: the task's target is released
	if _, still := d.m.Get(target.ID()); still {
		t.Error("target still registered after the task stopped")
	}
}

func TestLaunchTask_debugger_failedStartReleasesTheTarget(t *testing.T) {
	// Given: a debugged task whose container the daemon refuses to start
	d := newDebugTestHandler(t, true)
	d.fd.failStartOf("app")
	ctx := context.Background()
	if w := postJSON(t, ctx, d.h.CreateCluster, map[string]any{"clusterName": "c1"}); w.Code != 200 {
		t.Fatalf("CreateCluster: HTTP %d", w.Code)
	}
	if w := postJSON(t, ctx, d.h.RegisterTaskDefinition, taskDefinitionWith(map[string]string{debugger.TagDebug: "true"},
		map[string]any{"name": "app", "image": "busybox"})); w.Code != 200 {
		t.Fatalf("RegisterTaskDefinition: HTTP %d", w.Code)
	}

	// When: it is run
	w := postJSON(t, ctx, d.h.RunTask, map[string]any{"cluster": "c1", "taskDefinition": "debugged"})
	if w.Code != 200 {
		t.Fatalf("RunTask: HTTP %d: %s", w.Code, w.Body.String())
	}

	// Then: the task is STOPPED and no target outlives the placement
	var out struct {
		Tasks []struct {
			LastStatus string `json:"lastStatus"`
		} `json:"tasks"`
	}
	decodeBody(t, w, &out)
	if len(out.Tasks) != 1 || out.Tasks[0].LastStatus != "STOPPED" {
		t.Fatalf("tasks = %+v, want one STOPPED task", out.Tasks)
	}
	if targets := d.m.List(); len(targets) != 0 {
		t.Errorf("targets after a failed start = %v, want none", targets)
	}
}

// ─── the describer ───────────────────────────────────────────────────────────

func TestService_describeUntagged(t *testing.T) {
	// Given: a service holding one task of two containers
	h, _ := newECSRegionTestHandler(t)
	svc := &Service{handler: h}
	ctx := middleware.ContextWithRegion(context.Background(), "us-east-1")
	task := &Task{
		TaskArn: h.taskARN(ctx, "demo", "task-1"), ClusterArn: h.clusterARN(ctx, "demo"),
		TaskDefinitionArn: h.taskDefinitionARN(ctx, "plain", 3),
		LastStatus:        "RUNNING", DesiredStatus: "RUNNING",
		Containers: []Container{{Name: "app"}, {Name: "sidecar"}},
	}
	if aerr := h.store.putTask(ctx, task); aerr != nil {
		t.Fatalf("putTask: %s", aerr.Message)
	}

	t.Run("an existing task is synthesised for its first container", func(t *testing.T) {
		// When: the untagged task is described by id alone
		d, ok := svc.DescribeUntagged(ctx, debugger.ServiceECS, "task-1")

		// Then: the entry is off, says why, names the first container, and the
		// setup command tags the task definition
		if !ok {
			t.Fatal("existing task not described")
		}
		if d.ID != "ecs/task-1/app" || d.Enabled || d.Reason != debugger.ReasonNotTagged {
			t.Errorf("descriptor = %+v, want ecs/task-1/app, off, %q", d, debugger.ReasonNotTagged)
		}
		if !strings.Contains(d.Setup.TagCLI, task.TaskDefinitionArn) {
			t.Errorf("setup.tagCli = %q, want the task definition ARN", d.Setup.TagCLI)
		}
	})

	t.Run("a missing task is not", func(t *testing.T) {
		// When/Then: nothing is invented for an id the region does not hold
		if _, ok := svc.DescribeUntagged(ctx, debugger.ServiceECS, "ghost"); ok {
			t.Fatal("described a task that does not exist")
		}
	})

	t.Run("another service's resource is not ours", func(t *testing.T) {
		// When/Then: a function is left to Lambda, even under a task's id
		if _, ok := svc.DescribeUntagged(ctx, debugger.ServiceLambda, "task-1"); ok {
			t.Fatal("described a Lambda resource")
		}
	})
}
