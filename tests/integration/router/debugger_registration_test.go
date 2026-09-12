package router_test

// debugger_registration_test.go — a debug target follows the function's
// record rather than its first cold start (docs/plans/compute-debugger-console.md
// § 6): CreateFunction, TagResource, UntagResource and DeleteFunction keep
// the target list in step, a server whose flag is off registers an inert
// target with no port, and the first list after a restart scans the store
// for functions tagged before it.

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/url"
	"testing"

	"github.com/overcast-sh/overcast/internal/debugger"
	"github.com/overcast-sh/overcast/internal/state"
	"github.com/overcast-sh/overcast/tests/helpers"
)

// createTaggedDebuggerTestFunction is createDebuggerTestFunction with tags
// on the create request.
func createTaggedDebuggerTestFunction(t *testing.T, srv *helpers.TestServer, name string, tags map[string]string) string {
	t.Helper()
	body, _ := json.Marshal(map[string]any{
		"FunctionName": name,
		"Runtime":      "nodejs22.x",
		"Handler":      "index.handler",
		"Role":         "arn:aws:iam::000000000000:role/lambda-role",
		"Code":         map[string]any{"ZipFile": []byte("PK\x05\x06" + string(make([]byte, 18)))},
		"Tags":         tags,
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

func tagDebuggerTestFunction(t *testing.T, srv *helpers.TestServer, arn string, tags map[string]string) {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"Tags": tags})
	resp, err := http.Post(srv.URL+"/2017-03-31/tags/"+url.PathEscape(arn), "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("tag resource: %v", err)
	}
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusNoContent)
}

func untagDebuggerTestFunction(t *testing.T, srv *helpers.TestServer, arn string, keys ...string) {
	t.Helper()
	q := url.Values{}
	for _, k := range keys {
		q.Add("tagKeys", k)
	}
	req, _ := http.NewRequest(http.MethodDelete, srv.URL+"/2017-03-31/tags/"+url.PathEscape(arn)+"?"+q.Encode(), nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("untag resource: %v", err)
	}
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusNoContent)
}

func deleteDebuggerTestFunction(t *testing.T, srv *helpers.TestServer, name string) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodDelete, srv.URL+"/2015-03-31/functions/"+name, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("delete function: %v", err)
	}
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusNoContent)
}

// listDebuggerTargets reads the target list as the console does.
func listDebuggerTargets(t *testing.T, srv *helpers.TestServer) map[string]debugger.Descriptor {
	t.Helper()
	var list debugger.TargetList
	resp := getDebuggerJSON(t, srv, "/_overcast/debugger/targets", &list)
	helpers.AssertStatus(t, resp, http.StatusOK)
	byID := make(map[string]debugger.Descriptor, len(list.Targets))
	for _, d := range list.Targets {
		byID[d.ID] = d
	}
	return byID
}

func TestDebuggerTargets_followTheFunctionRecord(t *testing.T) {
	// Given: the Lambda debugger on, and no Docker — registration needs the
	// record and the manager, not a container
	srv := helpers.NewTestServer(t, helpers.WithLambdaDebugger())

	// When: a function is created tagged for the debugger, with the wait
	// tag, and an untagged one beside it
	arn := createTaggedDebuggerTestFunction(t, srv, "created-tagged", map[string]string{
		debugger.TagDebug: "true", debugger.TagWait: "true",
	})
	createDebuggerTestFunction(t, srv, "created-plain")

	// Then: the tagged one is listed before it has ever run — enabled,
	// bound on a port, waiting for a container, asking to wait for a client —
	// and the untagged one is not listed at all
	targets := listDebuggerTargets(t, srv)
	d, ok := targets["lambda/created-tagged"]
	if !ok {
		t.Fatalf("targets = %v, want lambda/created-tagged", targets)
	}
	if !d.Enabled || d.State != string(debugger.StateUnbound) || d.Listen.Port == 0 || !d.WaitForDebugger {
		t.Errorf("descriptor = enabled %v state %s port %d wait %v, want enabled, unbound, on a port, waiting",
			d.Enabled, d.State, d.Listen.Port, d.WaitForDebugger)
	}
	if _, listed := targets["lambda/created-plain"]; listed {
		t.Error("an untagged function was listed")
	}

	// When: the wait tag is removed, and the untagged function is tagged
	untagDebuggerTestFunction(t, srv, arn, debugger.TagWait)
	plainARN := "arn:aws:lambda:us-east-1:000000000000:function:created-plain"
	tagDebuggerTestFunction(t, srv, plainARN, map[string]string{debugger.TagDebug: "true"})

	// Then: the first keeps its port and no longer waits; the second is now
	// listed
	after := listDebuggerTargets(t, srv)
	if got := after["lambda/created-tagged"]; got.WaitForDebugger || got.Listen.Port != d.Listen.Port {
		t.Errorf("after untagging the wait: wait %v port %d, want no wait on port %d", got.WaitForDebugger, got.Listen.Port, d.Listen.Port)
	}
	if got, listed := after["lambda/created-plain"]; !listed || !got.Enabled {
		t.Errorf("tagged function not listed after TagResource: %+v", got)
	}

	// When: the debug tag itself is removed from one, and the other is deleted
	untagDebuggerTestFunction(t, srv, plainARN, debugger.TagDebug)
	deleteDebuggerTestFunction(t, srv, "created-tagged")

	// Then: both targets are gone
	if gone := listDebuggerTargets(t, srv); len(gone) != 0 {
		t.Errorf("targets after untag and delete = %v, want none", gone)
	}
}

func TestDebuggerTargets_flagOffRegistersInertWithoutAPort(t *testing.T) {
	// Given: a default server — the debugger flag is off
	srv := helpers.NewTestServer(t)

	// When: a function is created tagged for the debugger
	createTaggedDebuggerTestFunction(t, srv, "inert-tagged", map[string]string{debugger.TagDebug: "true"})

	// Then: it is listed so the console can say why it is off, and no port
	// was bound
	targets := listDebuggerTargets(t, srv)
	d, ok := targets["lambda/inert-tagged"]
	if !ok {
		t.Fatalf("targets = %v, want lambda/inert-tagged", targets)
	}
	if d.Enabled || d.State != string(debugger.StateInert) || d.Listen.Port != 0 {
		t.Errorf("descriptor = enabled %v state %s port %d, want inert with no port", d.Enabled, d.State, d.Listen.Port)
	}
	if !bytes.Contains([]byte(d.Reason), []byte("OVERCAST_LAMBDA_DEBUGGER")) {
		t.Errorf("reason = %q, want it to name the flag", d.Reason)
	}
}

func TestDebuggerTargets_firstListAfterARestartScansTheStore(t *testing.T) {
	// Given: a store in which a function was tagged before a restart — a
	// first server writes it, and a second server starts over the same store
	store := state.NewMemoryStore()
	before := helpers.NewTestServer(t, helpers.WithLambdaDebugger(), helpers.WithStore(store))
	createTaggedDebuggerTestFunction(t, before, "persisted-tagged", map[string]string{debugger.TagDebug: "true"})
	createDebuggerTestFunction(t, before, "persisted-plain")
	before.Close()
	after := helpers.NewTestServer(t, helpers.WithLambdaDebugger(), helpers.WithStore(store))

	// When: the console asks for the target list, before any page visit or
	// invocation
	targets := listDebuggerTargets(t, after)

	// Then: the tagged function is listed, bound on a port, and the untagged
	// one is not
	d, ok := targets["lambda/persisted-tagged"]
	if !ok {
		t.Fatalf("targets = %v, want lambda/persisted-tagged", targets)
	}
	if !d.Enabled || d.State != string(debugger.StateUnbound) || d.Listen.Port == 0 {
		t.Errorf("descriptor = enabled %v state %s port %d, want enabled and unbound on a port", d.Enabled, d.State, d.Listen.Port)
	}
	if _, listed := targets["lambda/persisted-plain"]; listed {
		t.Error("an untagged function was listed by the scan")
	}
}
