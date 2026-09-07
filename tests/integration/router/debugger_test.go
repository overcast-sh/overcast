package router_test

// debugger_test.go — the two emulator-only debugger endpoints under
// /_overcast/debugger (docs/plans/compute-debugger.md § 6), as the console
// reads them: the target list, and one function's entry whether or not a tag
// ever mentioned it.

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/overcast-sh/overcast/internal/debugger"
	"github.com/overcast-sh/overcast/tests/helpers"
)

func getDebuggerJSON(t *testing.T, srv *helpers.TestServer, path string, into any) *http.Response {
	t.Helper()
	resp, err := http.Get(srv.URL + path)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	if got := resp.Header.Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", got)
	}
	helpers.DecodeJSON(t, resp, into)
	return resp
}

func createDebuggerTestFunction(t *testing.T, srv *helpers.TestServer, name string) string {
	t.Helper()
	body, _ := json.Marshal(map[string]any{
		"FunctionName": name,
		"Runtime":      "nodejs22.x",
		"Handler":      "index.handler",
		"Role":         "arn:aws:iam::000000000000:role/lambda-role",
		"Code":         map[string]any{"ZipFile": []byte("PK\x05\x06" + string(make([]byte, 18)))},
	})
	resp, err := http.Post(srv.URL+"/2015-03-31/functions", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("create function: %v", err)
	}
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusCreated)
	var created struct {
		FunctionArn string `json:"FunctionArn"`
	}
	helpers.DecodeJSON(t, resp, &created)
	return created.FunctionArn
}

func TestDebuggerTargets_listIsEmptyOnAFreshServer(t *testing.T) {
	// Given: a default server — the debugger is off and nothing is tagged
	srv := helpers.NewTestServer(t)

	// When: the console asks for every target
	var list debugger.TargetList
	resp := getDebuggerJSON(t, srv, "/_overcast/debugger/targets", &list)

	// Then: the endpoint is mounted regardless of OVERCAST_DEBUG and answers
	// an empty array, not null
	helpers.AssertStatus(t, resp, http.StatusOK)
	if list.Targets == nil || len(list.Targets) != 0 {
		t.Fatalf("targets = %#v, want an empty array", list.Targets)
	}
}

func TestDebuggerTargets_untaggedFunctionIsSynthesised(t *testing.T) {
	// Given: a function no tag mentions
	srv := helpers.NewTestServer(t)
	arn := createDebuggerTestFunction(t, srv, "plain-fn")

	// When: the console opens its Debug tab
	var d debugger.Descriptor
	resp := getDebuggerJSON(t, srv, "/_overcast/debugger/targets/lambda/plain-fn", &d)

	// Then: the entry says it is off, why, and how to turn it on — with the
	// function's ARN in the tagging command
	helpers.AssertStatus(t, resp, http.StatusOK)
	if d.ID != "lambda/plain-fn" || d.Enabled || d.Reason != debugger.ReasonNotTagged {
		t.Errorf("descriptor = %+v, want lambda/plain-fn, off, %q", d, debugger.ReasonNotTagged)
	}
	if want := "--resource " + arn; !bytes.Contains([]byte(d.Setup.TagCLI), []byte(want)) {
		t.Errorf("setup.tagCli = %q, want it to name %q", d.Setup.TagCLI, want)
	}
	if d.Editors == nil {
		t.Error("editors = null, want an empty array")
	}
}

func TestDebuggerTargets_untaggedTaskIsSynthesisedPerContainer(t *testing.T) {
	// Given: a task placed from a task definition no tag mentions — metadata
	// only, since this server has no Docker, which is enough for it to exist
	srv := helpers.NewTestServer(t)
	ecsCall := func(operation string, body map[string]any, into any) {
		t.Helper()
		raw, _ := json.Marshal(body)
		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/", bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/x-amz-json-1.1")
		req.Header.Set("X-Amz-Target", "AmazonEC2ContainerServiceV20141113."+operation)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s: %v", operation, err)
		}
		defer resp.Body.Close()
		helpers.AssertStatus(t, resp, http.StatusOK)
		if into != nil {
			helpers.DecodeJSON(t, resp, into)
		}
	}
	ecsCall("CreateCluster", map[string]any{"clusterName": "plain"}, nil)
	var registered struct {
		TaskDefinition struct {
			TaskDefinitionArn string `json:"taskDefinitionArn"`
		} `json:"taskDefinition"`
	}
	ecsCall("RegisterTaskDefinition", map[string]any{
		"family": "plain-td",
		"containerDefinitions": []map[string]any{
			{"name": "app", "image": "busybox"},
			{"name": "sidecar", "image": "busybox"},
		},
	}, &registered)
	var placed struct {
		Tasks []struct {
			TaskArn string `json:"taskArn"`
		} `json:"tasks"`
	}
	ecsCall("RunTask", map[string]any{"cluster": "plain", "taskDefinition": "plain-td"}, &placed)
	if len(placed.Tasks) != 1 {
		t.Fatalf("placed %d tasks, want 1", len(placed.Tasks))
	}
	taskID := placed.Tasks[0].TaskArn[strings.LastIndex(placed.Tasks[0].TaskArn, "/")+1:]

	// When: the console opens the task's Debug panel for its sidecar
	var d debugger.Descriptor
	resp := getDebuggerJSON(t, srv, "/_overcast/debugger/targets/ecs/"+taskID+"?container=sidecar", &d)

	// Then: the entry is off, why, and how to turn it on — tagging the task
	// definition, which is where ECS reads the tag from
	helpers.AssertStatus(t, resp, http.StatusOK)
	if d.ID != "ecs/"+taskID+"/sidecar" || d.Container != "sidecar" || d.Enabled || d.Reason != debugger.ReasonNotTagged {
		t.Errorf("descriptor = %+v, want ecs/%s/sidecar, off, %q", d, taskID, debugger.ReasonNotTagged)
	}
	if want := "--resource-arn " + registered.TaskDefinition.TaskDefinitionArn; !strings.Contains(d.Setup.TagCLI, want) {
		t.Errorf("setup.tagCli = %q, want it to name %q", d.Setup.TagCLI, want)
	}

	// And: without a container named, the first one answers
	resp = getDebuggerJSON(t, srv, "/_overcast/debugger/targets/ecs/"+taskID, &d)
	helpers.AssertStatus(t, resp, http.StatusOK)
	if d.Container != "app" {
		t.Errorf("default container = %q, want the first, app", d.Container)
	}
}

func TestDebuggerTargets_unknownResourceIs404(t *testing.T) {
	// Given: a server holding no function of that name
	srv := helpers.NewTestServer(t)

	// When: an entry is requested for it
	var body map[string]string
	resp := getDebuggerJSON(t, srv, "/_overcast/debugger/targets/lambda/ghost", &body)

	// Then: 404 with a JSON error naming the target
	helpers.AssertStatus(t, resp, http.StatusNotFound)
	if body["error"] == "" || !bytes.Contains([]byte(body["error"]), []byte("lambda/ghost")) {
		t.Errorf("error = %q, want it to name lambda/ghost", body["error"])
	}
}
