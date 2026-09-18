// Package stepfunctions_test — RedriveExecution inside Parallel and Map states.
//
// A redrive resumes inside a failed Parallel or Map: branches and iterations
// that succeeded keep their outputs and are not run again, and those that
// failed, were aborted or never started are run — each from the state it
// stopped in. A distributed Map's map run is redriven the same way, child
// execution by child execution.
//
// Every failure below is a DynamoDB table that does not exist yet; the test
// creates it before redriving. Side effects that must not repeat are counted
// with an UpdateItem ADD on a counters table.
//
// Run: go test ./tests/integration/stepfunctions/...
package stepfunctions_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/overcast-sh/overcast/tests/helpers"
)

// counter reads counters[id].n, or "" when the item does not exist.
func counter(t *testing.T, srv *helpers.TestServer, id string) string {
	t.Helper()
	resp := awsJSONCall(t, srv, "DynamoDB_20120810.GetItem", map[string]any{
		"TableName": "counters", "Key": map[string]any{"id": map[string]string{"S": id}},
	})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	var out struct {
		Item map[string]map[string]string `json:"Item"`
	}
	helpers.DecodeJSON(t, resp, &out)
	return out.Item["n"]["N"]
}

// countState counts the times a state was entered in a history.
func countState(events []rawEvent, name string) int {
	n := 0
	for _, e := range events {
		if strings.HasSuffix(e.typ(), "StateEntered") && e.stateName() == name {
			n++
		}
	}
	return n
}

// afterRedrive returns the events recorded after the last ExecutionRedriven.
func afterRedrive(t *testing.T, events []rawEvent) []rawEvent {
	t.Helper()
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].typ() == "ExecutionRedriven" {
			return events[i:]
		}
	}
	t.Fatalf("no ExecutionRedriven event; types %v", rawTypes(events))
	return nil
}

// redriveAndWait redrives an execution and waits for it to finish.
func redriveAndWait(t *testing.T, srv *helpers.TestServer, execARN string) describeExecutionResult {
	t.Helper()
	sfnOK(t, srv, "RedriveExecution", map[string]any{"executionArn": execARN})
	return waitForTerminal(t, srv, execARN)
}

// countTask increments counters[<key>] — the side effect a re-run would repeat.
func countTask(keyTemplate, next string) string {
	return `{"Type": "Task", "Resource": "arn:aws:states:::dynamodb:updateItem",
	  "Parameters": {"TableName": "counters", "Key": {"id": {` + keyTemplate + `}},
	    "UpdateExpression": "ADD n :one", "ExpressionAttributeValues": {":one": {"N": "1"}}, "ReturnValues": "UPDATED_NEW"},
	  "ResultSelector": {"n.$": "$.Attributes.n.N"}, ` + next + `}`
}

// pollStates waits, inside a branch, until counters[<key>] exists and then
// reads the gate table — so the gate fails only after the sibling branch has
// finished counting, whatever the goroutine scheduling.
func pollStates(keyTemplate, gateTable string) string {
	return `
	  "Poll": {"Type": "Task", "Resource": "arn:aws:states:::dynamodb:getItem",
	    "Parameters": {"TableName": "counters", "Key": {"id": {` + keyTemplate + `}}}, "ResultPath": "$.seen", "Next": "Seen"},
	  "Seen": {"Type": "Choice", "Choices": [{"Variable": "$.seen.Item", "IsPresent": true, "Next": "Gate"}], "Default": "Poll"},
	  "Gate": {"Type": "Task", "Resource": "arn:aws:states:::dynamodb:getItem",
	    "Parameters": {"TableName` + gateTable + `, "Key": {"id": {"S": "g"}}}, "ResultPath": null, "OutputPath": "$.id", "End": true}`
}

func TestRedriveExecution_parallelRerunsOnlyTheFailedBranch(t *testing.T) {
	// Given: a Parallel whose counting branch succeeds and whose other branch
	// fails on a table that does not exist
	srv := helpers.NewTestServer(t)
	createTable(t, srv, "counters")
	def := `{
	  "StartAt": "Fan",
	  "States": {
	    "Fan": {"Type": "Parallel", "Next": "Done", "Branches": [
	      {"StartAt": "Count", "States": {"Count": ` + countTask(`"S": "a"`, `"End": true`) + `}},
	      {"StartAt": "Poll", "States": {` + pollStates(`"S": "a"`, `": "gate-p"`) + `}}
	    ]},
	    "Done": {"Type": "Pass", "End": true}
	  }
	}`
	got, execARN := runToEnd(t, srv, "redrive-parallel", def, `{"id":"x"}`)
	if got.Status != "FAILED" || got.Error != "DynamoDB.ResourceNotFoundException" {
		t.Fatalf("first run: status=%q error=%q (%s)", got.Status, got.Error, got.Cause)
	}

	// When: the table appears and the execution is redriven
	createTable(t, srv, "gate-p")
	got = redriveAndWait(t, srv, execARN)

	// Then: it succeeds with both branches' outputs
	if got.Status != "SUCCEEDED" || got.Output != `[{"n":"1"},"x"]` {
		t.Fatalf("after redrive: status=%q output=%s (%s: %s)", got.Status, got.Output, got.Error, got.Cause)
	}
	// And: the branch that succeeded did not run again
	if n := counter(t, srv, "a"); n != "1" {
		t.Errorf("counter = %q, want 1 — the succeeded branch re-ran", n)
	}
	events := rawHistory(t, srv, execARN)
	if countState(events, "Count") != 1 {
		t.Errorf("Count entered %d times, want 1", countState(events, "Count"))
	}
	// And: the redrive re-enters the Parallel and resumes the failed branch at
	// the state that failed, not at its StartAt
	tail := afterRedrive(t, events)
	want := []string{"ExecutionRedriven", "ParallelStateEntered", "ParallelStateStarted", "TaskStateEntered"}
	if types := rawTypes(tail); len(types) < len(want) || strings.Join(types[:len(want)], ",") != strings.Join(want, ",") {
		t.Fatalf("after ExecutionRedriven: %v", types)
	}
	if tail[3].stateName() != "Gate" || countState(tail, "Poll") != 0 || countState(tail, "Count") != 0 {
		t.Errorf("redriven branch events = %v", rawTypes(tail))
	}
	// And: the events link causally — the Parallel follows ExecutionRedriven
	// and the branch follows ParallelStateStarted
	if tail[1].prev() != tail[0].id() || tail[3].prev() != tail[2].id() {
		t.Errorf("previousEventId links: %v→%v, %v→%v", tail[1].prev(), tail[0].id(), tail[3].prev(), tail[2].id())
	}
	if countState(events, "Done") != 1 {
		t.Errorf("Done entered %d times", countState(events, "Done"))
	}
}

// inlineMapDef runs one iteration per item, one at a time: count the item,
// then read the item's table.
func inlineMapDef() string {
	return `{
	  "StartAt": "Each",
	  "States": {
	    "Each": {"Type": "Map", "ItemsPath": "$.items", "MaxConcurrency": 1, "End": true,
	      "ItemProcessor": {"StartAt": "Count", "States": {
	        "Count": ` + countTask(`"S.$": "$.id"`, `"ResultPath": "$.count", "Next": "Read"`) + `,
	        "Read": {"Type": "Task", "Resource": "arn:aws:states:::dynamodb:getItem",
	          "Parameters": {"TableName.$": "$.table", "Key": {"id": {"S": "g"}}}, "ResultPath": null, "OutputPath": "$.id", "End": true}
	      }}
	    }
	  }
	}`
}

func TestRedriveExecution_inlineMapRerunsOnlyUnsuccessfulIterations(t *testing.T) {
	// Given: an inline Map whose second item fails, so the third never starts
	srv := helpers.NewTestServer(t)
	createTable(t, srv, "counters")
	createTable(t, srv, "tbl-ok")
	input := `{"items":[{"id":"a","table":"tbl-ok"},{"id":"b","table":"tbl-late"},{"id":"c","table":"tbl-ok"}]}`
	got, execARN := runToEnd(t, srv, "redrive-map", inlineMapDef(), input)
	if got.Status != "FAILED" {
		t.Fatalf("first run: status=%q (%s: %s)", got.Status, got.Error, got.Cause)
	}
	if counter(t, srv, "c") != "" {
		t.Fatalf("item c ran in the first attempt; the test needs it unstarted")
	}

	// When: the missing table appears and the execution is redriven
	createTable(t, srv, "tbl-late")
	got = redriveAndWait(t, srv, execARN)

	// Then: the output combines every iteration, in item order
	if got.Status != "SUCCEEDED" || got.Output != `["a","b","c"]` {
		t.Fatalf("after redrive: status=%q output=%s (%s: %s)", got.Status, got.Output, got.Error, got.Cause)
	}
	// And: every item was counted exactly once — a did not re-run, b resumed
	// at the state that failed, c ran for the first time
	for _, id := range []string{"a", "b", "c"} {
		if n := counter(t, srv, id); n != "1" {
			t.Errorf("counter %s = %q, want 1", id, n)
		}
	}
	// And: only the unsuccessful iterations were started again
	tail := afterRedrive(t, rawHistory(t, srv, execARN))
	var indexes []float64
	for _, e := range findEvents(tail, "MapIterationStarted") {
		indexes = append(indexes, e.detail("mapIterationStartedEventDetails")["index"].(float64))
	}
	if len(indexes) != 2 || indexes[0] == 0 || indexes[1] == 0 {
		t.Errorf("redriven iterations = %v, want 1 and 2", indexes)
	}
	started := findEvents(tail, "MapStateStarted")
	if len(started) != 1 || started[0].detail("mapStateStartedEventDetails")["length"] != float64(3) {
		t.Errorf("MapStateStarted = %v", started)
	}
}

func TestRedriveExecution_parallelInsideMapResumesRecursively(t *testing.T) {
	// Given: a Map whose iterations each run a Parallel; the second
	// iteration's gate branch fails
	srv := helpers.NewTestServer(t)
	createTable(t, srv, "counters")
	createTable(t, srv, "tbl-ok")
	key := `"S.$": "States.Format('x-{}', $.id)"`
	def := `{
	  "StartAt": "Each",
	  "States": {
	    "Each": {"Type": "Map", "ItemsPath": "$.items", "MaxConcurrency": 1, "End": true,
	      "ItemProcessor": {"StartAt": "Both", "States": {
	        "Both": {"Type": "Parallel", "End": true, "Branches": [
	          {"StartAt": "Count", "States": {"Count": ` + countTask(key, `"End": true`) + `}},
	          {"StartAt": "Poll", "States": {` + pollStates(key, `.$": "$.table"`) + `}}
	        ]}
	      }}
	    }
	  }
	}`
	input := `{"items":[{"id":"p","table":"tbl-ok"},{"id":"q","table":"tbl-late"}]}`
	got, execARN := runToEnd(t, srv, "redrive-nested", def, input)
	if got.Status != "FAILED" {
		t.Fatalf("first run: status=%q (%s: %s)", got.Status, got.Error, got.Cause)
	}

	// When: the table appears and the execution is redriven
	createTable(t, srv, "tbl-late")
	got = redriveAndWait(t, srv, execARN)

	// Then: both iterations' Parallel outputs are there
	if got.Status != "SUCCEEDED" || got.Output != `[[{"n":"1"},"p"],[{"n":"1"},"q"]]` {
		t.Fatalf("after redrive: status=%q output=%s (%s: %s)", got.Status, got.Output, got.Error, got.Cause)
	}
	// And: neither counting branch ran twice — not the succeeded iteration's,
	// nor the succeeded branch of the failed iteration's Parallel
	for _, id := range []string{"x-p", "x-q"} {
		if n := counter(t, srv, id); n != "1" {
			t.Errorf("counter %s = %q, want 1", id, n)
		}
	}
	tail := afterRedrive(t, rawHistory(t, srv, execARN))
	if countState(tail, "Count") != 0 || countState(tail, "Poll") != 0 || countState(tail, "Gate") != 1 || countState(tail, "Both") != 1 {
		t.Errorf("after redrive: %v", rawTypes(tail))
	}
}

// distributedGateDef runs one child per item, one at a time, reading the
// item's table.
func distributedGateDef(executionType string) string {
	return `{
	  "StartAt": "Fan",
	  "States": {
	    "Fan": {"Type": "Map", "ItemsPath": "$.items", "MaxConcurrency": 1, "Label": "gates", "End": true,
	      "ItemProcessor": {
	        "ProcessorConfig": {"Mode": "DISTRIBUTED", "ExecutionType": "` + executionType + `"},
	        "StartAt": "Read",
	        "States": {"Read": {"Type": "Task", "Resource": "arn:aws:states:::dynamodb:getItem",
	          "Parameters": {"TableName.$": "$.table", "Key": {"id": {"S": "g"}}}, "ResultPath": null, "OutputPath": "$.id", "End": true}}
	      }
	    }
	  }
	}`
}

const distributedGateInput = `{"items":[{"id":"a","table":"tbl-ok"},{"id":"b","table":"tbl-late"},{"id":"c","table":"tbl-ok"}]}`

// mapRunChildren lists a map run's children by status.
func mapRunChildren(t *testing.T, srv *helpers.TestServer, mapRunArn string) map[string][]string {
	t.Helper()
	listed := sfnOK(t, srv, "ListExecutions", map[string]any{"mapRunArn": mapRunArn})
	byStatus := map[string][]string{}
	list, _ := listed["executions"].([]any)
	for _, item := range list {
		exec := item.(map[string]any)
		status, _ := exec["status"].(string)
		byStatus[status] = append(byStatus[status], exec["executionArn"].(string))
	}
	return byStatus
}

func firstMapRunArn(t *testing.T, events []rawEvent) string {
	t.Helper()
	started := findEvents(events, "MapRunStarted")
	if len(started) != 1 {
		t.Fatalf("MapRunStarted count = %d; types %v", len(started), rawTypes(events))
	}
	arn, _ := started[0].detail("mapRunStartedEventDetails")["mapRunArn"].(string)
	return arn
}

func TestRedriveExecution_distributedMapRedrivesFailedChildren(t *testing.T) {
	// Given: a distributed Map whose second child fails, which fails the map
	// run before the third child starts
	srv := helpers.NewTestServer(t)
	createTable(t, srv, "tbl-ok")
	got, execARN := runToEnd(t, srv, "redrive-dmap", distributedGateDef("STANDARD"), distributedGateInput)
	if got.Status != "FAILED" || got.Error != "States.ExceedToleratedFailureThreshold" {
		t.Fatalf("first run: status=%q error=%q (%s)", got.Status, got.Error, got.Cause)
	}
	mapRunArn := firstMapRunArn(t, rawHistory(t, srv, execARN))
	before := mapRunChildren(t, srv, mapRunArn)
	if len(before["SUCCEEDED"]) != 1 || len(before["FAILED"]) != 1 || len(before["RUNNING"]) != 0 {
		t.Fatalf("children before redrive = %v", before)
	}
	succeededChild, failedChild := before["SUCCEEDED"][0], before["FAILED"][0]
	succeededHistory := len(rawHistory(t, srv, succeededChild))
	if described := sfnOK(t, srv, "DescribeExecution", map[string]any{"executionArn": failedChild}); described["redriveStatus"] != "REDRIVABLE" {
		t.Errorf("failed child redriveStatus = %v", described["redriveStatus"])
	}

	// When: the table appears and the parent is redriven
	createTable(t, srv, "tbl-late")
	got = redriveAndWait(t, srv, execARN)

	// Then: the parent succeeds with every child's output in item order
	if got.Status != "SUCCEEDED" || got.Output != `["a","b","c"]` {
		t.Fatalf("after redrive: status=%q output=%s (%s: %s)", got.Status, got.Output, got.Error, got.Cause)
	}
	// And: the same map run was redriven rather than a new one started
	events := rawHistory(t, srv, execARN)
	if len(findEvents(events, "MapRunStarted")) != 1 {
		t.Errorf("MapRunStarted recorded again: %v", rawTypes(events))
	}
	redriven := findEvents(afterRedrive(t, events), "MapRunRedriven")
	if len(redriven) != 1 {
		t.Fatalf("MapRunRedriven = %v; types %v", redriven, rawTypes(events))
	}
	if d := redriven[0].detail("mapRunRedrivenEventDetails"); d["mapRunArn"] != mapRunArn || d["redriveCount"] != float64(1) {
		t.Errorf("mapRunRedrivenEventDetails = %v", d)
	}
	if runs := sfnOK(t, srv, "ListMapRuns", map[string]any{"executionArn": execARN}); len(runs["mapRuns"].([]any)) != 1 {
		t.Errorf("ListMapRuns = %v", runs)
	}
	// And: the failed child was redriven under its own ARN, the succeeded one
	// left alone, and the unstarted one started
	after := mapRunChildren(t, srv, mapRunArn)
	if len(after["SUCCEEDED"]) != 3 {
		t.Fatalf("children after redrive = %v", after)
	}
	child := sfnOK(t, srv, "DescribeExecution", map[string]any{"executionArn": failedChild})
	if child["redriveCount"] != float64(1) || child["redriveDate"] == nil {
		t.Errorf("redriven child = %v", child)
	}
	if len(findEvents(rawHistory(t, srv, failedChild), "ExecutionRedriven")) != 1 {
		t.Errorf("redriven child history = %v", rawTypes(rawHistory(t, srv, failedChild)))
	}
	if n := len(rawHistory(t, srv, succeededChild)); n != succeededHistory {
		t.Errorf("succeeded child history grew from %d to %d events", succeededHistory, n)
	}
	// And: the map run reports the redrive and its final counts
	described := sfnOK(t, srv, "DescribeMapRun", map[string]any{"mapRunArn": mapRunArn})
	executions, _ := described["executionCounts"].(map[string]any)
	items, _ := described["itemCounts"].(map[string]any)
	if described["status"] != "SUCCEEDED" || described["redriveCount"] != float64(1) || described["redriveDate"] == nil {
		t.Errorf("DescribeMapRun = %v", described)
	}
	for name, counts := range map[string]map[string]any{"executionCounts": executions, "itemCounts": items} {
		if counts["succeeded"] != float64(3) || counts["failed"] != float64(0) || counts["total"] != float64(3) ||
			counts["pending"] != float64(0) || counts["pendingRedrive"] != float64(0) {
			t.Errorf("%s = %v", name, counts)
		}
	}
}

func TestRedriveExecution_distributedMapRestartsExpressChildren(t *testing.T) {
	// Given: a distributed Map of EXPRESS children, one of which failed
	srv := helpers.NewTestServer(t)
	createTable(t, srv, "tbl-ok")
	got, execARN := runToEnd(t, srv, "redrive-dmap-x", distributedGateDef("EXPRESS"), distributedGateInput)
	if got.Status != "FAILED" {
		t.Fatalf("first run: status=%q (%s: %s)", got.Status, got.Error, got.Cause)
	}
	mapRunArn := firstMapRunArn(t, rawHistory(t, srv, execARN))
	failedChild := mapRunChildren(t, srv, mapRunArn)["FAILED"]
	if len(failedChild) != 1 {
		t.Fatalf("children = %v", mapRunChildren(t, srv, mapRunArn))
	}
	if described := sfnOK(t, srv, "DescribeExecution", map[string]any{"executionArn": failedChild[0]}); described["redriveStatus"] != "REDRIVABLE_BY_MAP_RUN" {
		t.Errorf("EXPRESS child redriveStatus = %v", described["redriveStatus"])
	}

	// When: the table appears and the parent is redriven
	createTable(t, srv, "tbl-late")
	got = redriveAndWait(t, srv, execARN)

	// Then: the child is started again from its first state under the same
	// ARN — an EXPRESS child is re-started, not redriven
	if got.Status != "SUCCEEDED" || got.Output != `["a","b","c"]` {
		t.Fatalf("after redrive: status=%q output=%s (%s: %s)", got.Status, got.Output, got.Error, got.Cause)
	}
	history := rawHistory(t, srv, failedChild[0])
	if types := strings.Join(rawTypes(history), ","); types != "ExecutionStarted,TaskStateEntered,TaskScheduled,TaskStarted,TaskSucceeded,TaskStateExited,ExecutionSucceeded" {
		t.Errorf("restarted child history = %s", types)
	}
	child := sfnOK(t, srv, "DescribeExecution", map[string]any{"executionArn": failedChild[0]})
	if child["status"] != "SUCCEEDED" || child["redriveCount"] != float64(0) {
		t.Errorf("restarted child = %v", child)
	}
	if described := sfnOK(t, srv, "DescribeMapRun", map[string]any{"mapRunArn": mapRunArn}); described["redriveCount"] != float64(1) {
		t.Errorf("DescribeMapRun = %v", described)
	}
}

func TestRedriveExecution_succeededExecutionReportsWhy(t *testing.T) {
	// Given: a succeeded execution
	srv := helpers.NewTestServer(t)
	_, execARN := runToEnd(t, srv, "redrive-why", `{"StartAt":"P","States":{"P":{"Type":"Pass","End":true}}}`, `{}`)

	// When: we describe it and try to redrive it
	described := sfnOK(t, srv, "DescribeExecution", map[string]any{"executionArn": execARN})
	resp := sfnCall(t, srv, "RedriveExecution", map[string]any{"executionArn": execARN})
	defer resp.Body.Close()

	// Then: both give AWS's reason
	const reason = "Execution is SUCCEEDED and cannot be redriven"
	if described["redriveStatus"] != "NOT_REDRIVABLE" || described["redriveStatusReason"] != reason {
		t.Errorf("DescribeExecution = %v", described)
	}
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	if body := helpers.ReadBody(t, resp); !strings.Contains(body, "ExecutionNotRedrivable") || !strings.Contains(body, reason) {
		t.Errorf("body = %s", body)
	}
}
