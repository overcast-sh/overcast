// Package stepfunctions_test — distributed Map and the map-run API.
//
// Run: go test ./tests/integration/stepfunctions/...
package stepfunctions_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/overcast-sh/overcast/tests/helpers"
)

// distributedMap wraps a processor in a DISTRIBUTED Map state named Fan.
func distributedMap(extra, processorStates string) string {
	return `{
	  "StartAt": "Fan",
	  "States": {
	    "Fan": {
	      "Type": "Map"` + extra + `,
	      "ItemProcessor": {
	        "ProcessorConfig": {"Mode": "DISTRIBUTED", "ExecutionType": "STANDARD"},
	        "StartAt": "Work",
	        "States": ` + processorStates + `
	      },
	      "End": true
	    }
	  }
	}`
}

const doubleProcessor = `{"Work": {"Type": "Pass", "Parameters": {"v.$": "$.v"}, "End": true}}`

func TestStartExecution_distributedMapRunsChildExecutions(t *testing.T) {
	// Given: a distributed Map over three items
	srv := helpers.NewTestServer(t)
	def := distributedMap(`, "ItemsPath": "$.items", "Label": "fanout"`, doubleProcessor)

	// When: it runs
	got, execARN := runToEnd(t, srv, "dmap", def, `{"items":[{"v":1},{"v":2},{"v":3}]}`)

	// Then: the output is the children's outputs in item order
	if got.Status != "SUCCEEDED" || got.Output != `[{"v":1},{"v":2},{"v":3}]` {
		t.Fatalf("status=%q output=%q (%s: %s)", got.Status, got.Output, got.Error, got.Cause)
	}

	// And: the parent history names the map run
	events := rawHistory(t, srv, execARN)
	started := findEvents(events, "MapRunStarted")
	if len(started) != 1 {
		t.Fatalf("MapRunStarted count = %d; types %v", len(started), rawTypes(events))
	}
	mapRunArn, _ := started[0].detail("mapRunStartedEventDetails")["mapRunArn"].(string)
	if !strings.Contains(mapRunArn, ":mapRun:dmap/fanout:") {
		t.Errorf("mapRunArn = %q", mapRunArn)
	}
	if len(findEvents(events, "MapRunSucceeded")) != 1 {
		t.Errorf("types = %v", rawTypes(events))
	}

	// And: the map run and its children are observable
	runs := sfnOK(t, srv, "ListMapRuns", map[string]any{"executionArn": execARN})
	if list, _ := runs["mapRuns"].([]any); len(list) != 1 {
		t.Fatalf("ListMapRuns = %v", runs)
	}
	described := sfnOK(t, srv, "DescribeMapRun", map[string]any{"mapRunArn": mapRunArn})
	counts, _ := described["itemCounts"].(map[string]any)
	if described["status"] != "SUCCEEDED" || counts["succeeded"] != float64(3) || counts["total"] != float64(3) {
		t.Errorf("DescribeMapRun = %v", described)
	}
	children := sfnOK(t, srv, "ListExecutions", map[string]any{"mapRunArn": mapRunArn})
	list, _ := children["executions"].([]any)
	if len(list) != 3 {
		t.Fatalf("children = %v", children)
	}
	child := list[0].(map[string]any)
	if child["mapRunArn"] != mapRunArn || child["status"] != "SUCCEEDED" {
		t.Errorf("child = %v", child)
	}
	childHistory := rawTypes(rawHistory(t, srv, child["executionArn"].(string)))
	if strings.Join(childHistory, ",") != "ExecutionStarted,PassStateEntered,PassStateExited,ExecutionSucceeded" {
		t.Errorf("child history = %v", childHistory)
	}
}

func TestStartExecution_distributedMapReadsBatchesAndWritesS3(t *testing.T) {
	// Given: items in an S3 JSON object, batched in twos, results written to S3
	srv := helpers.NewTestServer(t)
	createBucket(t, srv, "dmap-bucket")
	putS3Object(t, srv, "dmap-bucket", "in/items.json", `[{"n":1},{"n":2},{"n":3}]`)
	extra := `,
	  "ItemReader": {"Resource": "arn:aws:states:::s3:getObject", "ReaderConfig": {"InputType": "JSON"},
	                 "Parameters": {"Bucket": "dmap-bucket", "Key.$": "$.key"}},
	  "ItemBatcher": {"MaxItemsPerBatch": 2, "BatchInput": {"tag": "b"}},
	  "ResultWriter": {"Resource": "arn:aws:states:::s3:putObject", "Parameters": {"Bucket": "dmap-bucket", "Prefix": "out"}}`
	def := distributedMap(extra, `{"Work": {"Type": "Pass", "End": true}}`)

	// When: it runs
	got, _ := runToEnd(t, srv, "dmap-s3", def, `{"key":"in/items.json"}`)

	// Then: the result points at a manifest describing the written results
	if got.Status != "SUCCEEDED" {
		t.Fatalf("status=%q (%s: %s)", got.Status, got.Error, got.Cause)
	}
	var out struct {
		MapRunArn           string
		ResultWriterDetails struct{ Bucket, Key string }
	}
	if err := json.Unmarshal([]byte(got.Output), &out); err != nil || out.ResultWriterDetails.Bucket != "dmap-bucket" {
		t.Fatalf("output = %s", got.Output)
	}
	var manifest struct {
		MapRunArn   string
		ResultFiles map[string][]struct{ Key string }
	}
	if err := json.Unmarshal([]byte(getS3Object(t, srv, "dmap-bucket", out.ResultWriterDetails.Key)), &manifest); err != nil {
		t.Fatalf("manifest: %v", err)
	}
	if manifest.MapRunArn != out.MapRunArn || len(manifest.ResultFiles["SUCCEEDED"]) != 1 {
		t.Fatalf("manifest = %+v", manifest)
	}
	var entries []struct{ Input, Output, Status string }
	if err := json.Unmarshal([]byte(getS3Object(t, srv, "dmap-bucket", manifest.ResultFiles["SUCCEEDED"][0].Key)), &entries); err != nil {
		t.Fatalf("results: %v", err)
	}
	// Two batches: [1,2] and [3], each carrying BatchInput.
	if len(entries) != 2 || entries[0].Input != `{"BatchInput":{"tag":"b"},"Items":[{"n":1},{"n":2}]}` {
		t.Errorf("entries = %+v", entries)
	}
}

func TestStartExecution_distributedMapReadsCSV(t *testing.T) {
	// Given: a CSV object with a header row
	srv := helpers.NewTestServer(t)
	createBucket(t, srv, "dmap-csv")
	putS3Object(t, srv, "dmap-csv", "rows.csv", "name,age\nada,36\nalan,41\n")
	extra := `,
	  "ItemReader": {"Resource": "arn:aws:states:::s3:getObject",
	                 "ReaderConfig": {"InputType": "CSV", "CSVHeaderLocation": "FIRST_ROW"},
	                 "Parameters": {"Bucket": "dmap-csv", "Key": "rows.csv"}}`
	def := distributedMap(extra, `{"Work": {"Type": "Pass", "End": true}}`)

	// When: it runs
	got, _ := runToEnd(t, srv, "dmap-csv", def, `{}`)

	// Then: each row is an object keyed by the header, values as strings
	if got.Status != "SUCCEEDED" || got.Output != `[{"age":"36","name":"ada"},{"age":"41","name":"alan"}]` {
		t.Fatalf("status=%q output=%q (%s: %s)", got.Status, got.Output, got.Error, got.Cause)
	}
}

// failOnTwo fails the child whose item is 2.
const failOnTwo = `{
  "Work": {"Type": "Choice", "Choices": [{"Variable": "$", "NumericEquals": 2, "Next": "Bad"}], "Default": "Good"},
  "Bad": {"Type": "Fail", "Error": "Item.Bad"},
  "Good": {"Type": "Pass", "End": true}
}`

func TestStartExecution_distributedMapToleratesFailures(t *testing.T) {
	// Given: one failing item in four, with one failure tolerated
	srv := helpers.NewTestServer(t)
	def := distributedMap(`, "ToleratedFailureCount": 1`, failOnTwo)

	// When: it runs
	got, _ := runToEnd(t, srv, "dmap-tolerate", def, `[1,2,3,4]`)

	// Then: the Map succeeds; the failed child's slot is null
	if got.Status != "SUCCEEDED" || got.Output != `[1,null,3,4]` {
		t.Fatalf("status=%q output=%q (%s: %s)", got.Status, got.Output, got.Error, got.Cause)
	}
}

func TestStartExecution_distributedMapFailsPastTolerance(t *testing.T) {
	// Given: one failing item and no tolerance
	srv := helpers.NewTestServer(t)
	def := distributedMap(`, "MaxConcurrency": 1`, failOnTwo)

	// When: it runs
	got, execARN := runToEnd(t, srv, "dmap-intolerant", def, `[1,2,3,4]`)

	// Then: AWS's States.ExceedToleratedFailureThreshold, and the map run is FAILED
	if got.Status != "FAILED" || got.Error != "States.ExceedToleratedFailureThreshold" {
		t.Fatalf("status=%q error=%q", got.Status, got.Error)
	}
	events := rawHistory(t, srv, execARN)
	if len(findEvents(events, "MapRunFailed")) != 1 {
		t.Errorf("types = %v", rawTypes(events))
	}
	mapRunArn, _ := findEvents(events, "MapRunStarted")[0].detail("mapRunStartedEventDetails")["mapRunArn"].(string)
	described := sfnOK(t, srv, "DescribeMapRun", map[string]any{"mapRunArn": mapRunArn})
	if described["status"] != "FAILED" {
		t.Errorf("DescribeMapRun = %v", described)
	}

	// And: UpdateMapRun adjusts the recorded limits
	sfnOK(t, srv, "UpdateMapRun", map[string]any{"mapRunArn": mapRunArn, "maxConcurrency": 5, "toleratedFailureCount": 2})
	described = sfnOK(t, srv, "DescribeMapRun", map[string]any{"mapRunArn": mapRunArn})
	if described["maxConcurrency"] != float64(5) || described["toleratedFailureCount"] != float64(2) {
		t.Errorf("after UpdateMapRun = %v", described)
	}
}
