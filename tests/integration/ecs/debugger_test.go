package ecs_test

// debugger_test.go — the compute debugger against a real Node.js task
// (docs/plans/compute-debugger.md § 5 and § 9): the inspector inside an awsvpc
// task answers through Overcast's proxy port, published on the namespace
// container the task's containers share, and the port goes away with the task.
//
// Docker-gated like its neighbours, and never t.Parallel().

import (
	"net"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/overcast-sh/overcast/internal/debugger"
	"github.com/overcast-sh/overcast/tests/helpers"
)

// debuggedNodeImage runs the inspector the tag asks for; small, and official.
const debuggedNodeImage = "node:20-alpine"

// ecsDebugTarget reads a task's descriptor from the debugger endpoint —
// its first container, which is the task's only one here.
func ecsDebugTarget(t *testing.T, srv *helpers.TestServer, taskID string) (debugger.Descriptor, int) {
	t.Helper()
	resp, err := http.Get(srv.URL + "/_overcast/debugger/targets/ecs/" + taskID)
	if err != nil {
		t.Fatalf("get target: %v", err)
	}
	defer resp.Body.Close()
	var d debugger.Descriptor
	if resp.StatusCode == http.StatusOK {
		helpers.DecodeJSON(t, resp, &d)
	}
	return d, resp.StatusCode
}

func TestRunTask_withDocker_debuggerInspectorAnswersThroughTheProxy(t *testing.T) {
	dc := skipWithoutDocker(t)
	helpers.PullOrSkip(t, dc, debuggedNodeImage)

	// Given: the ECS debugger on, and an awsvpc task of one Node container
	// tagged for the inspector, running an idle script
	srv := helpers.NewTestServer(t, helpers.WithECSDocker(), helpers.WithECSDebugger())
	helpers.WaitForECSDocker(t, srv)

	create := ecsCall(t, srv, "CreateCluster", map[string]any{"clusterName": "debug"})
	helpers.AssertStatus(t, create, http.StatusOK)
	create.Body.Close()

	reg := ecsCall(t, srv, "RegisterTaskDefinition", map[string]any{
		"family":                  "debugged-node",
		"networkMode":             "awsvpc",
		"requiresCompatibilities": []string{"FARGATE"},
		"cpu":                     "256",
		"memory":                  "512",
		"containerDefinitions": []map[string]any{{
			"name":    "app",
			"image":   debuggedNodeImage,
			"command": []string{"node", "-e", "setInterval(() => {}, 1000)"},
		}},
		"tags": []map[string]string{
			{"key": debugger.TagDebug, "value": "true"},
			{"key": debugger.TagProtocol, "value": "inspector"},
		},
	})
	helpers.AssertStatus(t, reg, http.StatusOK)
	reg.Body.Close()

	// When: the task is placed
	run := ecsCall(t, srv, "RunTask", map[string]any{
		"cluster":        "debug",
		"taskDefinition": "debugged-node",
		"launchType":     "FARGATE",
		"networkConfiguration": map[string]any{
			"awsvpcConfiguration": map[string]any{"subnets": []string{"subnet-debug"}},
		},
	})
	helpers.AssertStatus(t, run, http.StatusOK)
	var placed struct {
		Tasks []struct {
			TaskArn       string `json:"taskArn"`
			LastStatus    string `json:"lastStatus"`
			StoppedReason string `json:"stoppedReason"`
		} `json:"tasks"`
	}
	helpers.DecodeJSON(t, run, &placed)
	run.Body.Close()
	if len(placed.Tasks) != 1 {
		t.Fatalf("expected 1 task, got %d", len(placed.Tasks))
	}
	if placed.Tasks[0].LastStatus == "STOPPED" {
		t.Fatalf("task failed to start: %s", placed.Tasks[0].StoppedReason)
	}
	taskID := placed.Tasks[0].TaskArn[strings.LastIndex(placed.Tasks[0].TaskArn, "/")+1:]

	// Then: the container's target is bound to the inspector on a loopback
	// port, with the task definition in the setup block
	target, status := ecsDebugTarget(t, srv, taskID)
	if status != http.StatusOK || !target.Enabled || target.Protocol != "inspector" || target.Listen.Port == 0 {
		t.Fatalf("target = %+v (HTTP %d), want an enabled inspector target on a port", target, status)
	}
	if target.Container != "app" || target.Upstream == "" || target.ContainerID == "" || target.RemoteRoot == "" {
		t.Errorf("target = %+v, want the app container bound with an upstream, a container and a remote root", target)
	}
	if !strings.Contains(target.Setup.TagCLI, ":task-definition/debugged-node:1") {
		t.Errorf("setup.tagCli = %q, want the task definition ARN", target.Setup.TagCLI)
	}

	// And: the inspector's discovery endpoint answers through the proxy once
	// Node is up, naming a WebSocket URL on the same port number
	proxy := "http://" + net.JoinHostPort(target.Listen.Host, strconv.Itoa(target.Listen.Port))
	var sessions []struct {
		WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
	}
	helpers.Eventually(t, 60*time.Second, 500*time.Millisecond, func() bool {
		resp, err := http.Get(proxy + "/json/list")
		if err != nil {
			return false
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return false
		}
		helpers.DecodeJSON(t, resp, &sessions)
		return len(sessions) > 0
	}, "the inspector never answered /json/list through the proxy")
	if sessions[0].WebSocketDebuggerURL == "" || !strings.Contains(sessions[0].WebSocketDebuggerURL, ":"+strconv.Itoa(target.Listen.Port)+"/") {
		t.Errorf("webSocketDebuggerUrl = %q, want it on port %d", sessions[0].WebSocketDebuggerURL, target.Listen.Port)
	}

	// When: the task is stopped
	stop := ecsCall(t, srv, "StopTask", map[string]any{"cluster": "debug", "task": taskID})
	helpers.AssertStatus(t, stop, http.StatusOK)
	stop.Body.Close()

	// Then: the target is gone — the endpoint falls back to the untagged entry
	// for a task that still exists — and its port is free again
	after, status := ecsDebugTarget(t, srv, taskID)
	if status != http.StatusOK || after.Enabled || after.Reason != debugger.ReasonNotTagged {
		t.Errorf("target after stop = %+v (HTTP %d), want the synthesised untagged entry", after, status)
	}
	ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(target.Listen.Port)))
	if err != nil {
		t.Fatalf("debug port %d still held after the task stopped: %v", target.Listen.Port, err)
	}
	_ = ln.Close()
}
