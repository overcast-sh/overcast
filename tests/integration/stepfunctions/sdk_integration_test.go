// Package stepfunctions_test — AWS SDK service integrations and the optimized
// integrations added alongside them.
//
// Run: go test ./tests/integration/stepfunctions/...
package stepfunctions_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/overcast-sh/overcast/tests/helpers"
)

func createBucket(t *testing.T, srv *helpers.TestServer, bucket string) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/"+bucket, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("CreateBucket: %v", err)
	}
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
}

func putS3Object(t *testing.T, srv *helpers.TestServer, bucket, key, body string) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/"+bucket+"/"+key, bytes.NewReader([]byte(body)))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PutObject: %v", err)
	}
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
}

func getS3Object(t *testing.T, srv *helpers.TestServer, bucket, key string) string {
	t.Helper()
	resp, err := http.Get(srv.URL + "/" + bucket + "/" + key)
	if err != nil {
		t.Fatalf("GetObject: %v", err)
	}
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	b, _ := io.ReadAll(resp.Body)
	return string(b)
}

func TestStartExecution_dynamoDBDeleteItemIntegration(t *testing.T) {
	// Given: a table holding one item and a state machine that deletes it
	srv := helpers.NewTestServer(t)
	createTable(t, srv, "del-table")
	resp := awsJSONCall(t, srv, "DynamoDB_20120810.PutItem", map[string]any{
		"TableName": "del-table", "Item": map[string]any{"id": map[string]string{"S": "a"}},
	})
	resp.Body.Close()
	def := `{"StartAt":"Del","States":{"Del":{"Type":"Task","Resource":"arn:aws:states:::dynamodb:deleteItem",
	  "Parameters":{"TableName":"del-table","Key":{"id":{"S":"a"}}},"End":true}}}`

	// When: it runs
	got, _ := runToEnd(t, srv, "ddb-delete", def, `{}`)

	// Then: the item is gone
	if got.Status != "SUCCEEDED" {
		t.Fatalf("status = %q (%s: %s)", got.Status, got.Error, got.Cause)
	}
	resp = awsJSONCall(t, srv, "DynamoDB_20120810.GetItem", map[string]any{
		"TableName": "del-table", "Key": map[string]any{"id": map[string]string{"S": "a"}},
	})
	defer resp.Body.Close()
	if body := helpers.ReadBody(t, resp); strings.Contains(body, `"Item"`) {
		t.Errorf("item still present: %s", body)
	}
}

func TestStartExecution_eventsPutEventsIntegration(t *testing.T) {
	// Given: a state machine that puts an event with an object Detail
	srv := helpers.NewTestServer(t)
	def := `{"StartAt":"Put","States":{"Put":{"Type":"Task","Resource":"arn:aws:states:::events:putEvents",
	  "Parameters":{"Entries":[{"Source":"app.orders","DetailType":"Placed","Detail":{"id.$":"$.id"}}]},"End":true}}}`

	// When: it runs
	got, _ := runToEnd(t, srv, "events-put", def, `{"id":"o-1"}`)

	// Then: EventBridge accepted the entry
	if got.Status != "SUCCEEDED" {
		t.Fatalf("status = %q (%s: %s)", got.Status, got.Error, got.Cause)
	}
	var out struct {
		FailedEntryCount float64
		Entries          []struct{ EventId string }
	}
	if err := json.Unmarshal([]byte(got.Output), &out); err != nil || len(out.Entries) != 1 || out.Entries[0].EventId == "" {
		t.Errorf("output = %s", got.Output)
	}
}

func TestStartExecution_awsSDKDynamoDBGetItem(t *testing.T) {
	// Given: a table with one item, read through the aws-sdk integration
	srv := helpers.NewTestServer(t)
	createTable(t, srv, "sdk-table")
	resp := awsJSONCall(t, srv, "DynamoDB_20120810.PutItem", map[string]any{
		"TableName": "sdk-table", "Item": map[string]any{"id": map[string]string{"S": "k"}, "colour": map[string]string{"S": "blue"}},
	})
	resp.Body.Close()
	def := `{"StartAt":"Get","States":{"Get":{"Type":"Task","Resource":"arn:aws:states:::aws-sdk:dynamodb:getItem",
	  "Parameters":{"TableName":"sdk-table","Key":{"id":{"S":"k"}}},"ResultSelector":{"colour.$":"$.Item.colour.S"},"End":true}}}`

	// When: it runs
	got, execARN := runToEnd(t, srv, "sdk-ddb", def, `{}`)

	// Then: the item's own attribute names are untouched and the history names the SDK integration
	if got.Status != "SUCCEEDED" || got.Output != `{"colour":"blue"}` {
		t.Fatalf("status=%q output=%q (%s: %s)", got.Status, got.Output, got.Error, got.Cause)
	}
	scheduled := findEvents(rawHistory(t, srv, execARN), "TaskScheduled")[0].detail("taskScheduledEventDetails")
	if scheduled["resourceType"] != "aws-sdk:dynamodb" || scheduled["resource"] != "getItem" {
		t.Errorf("taskScheduledEventDetails = %v", scheduled)
	}
}

func TestStartExecution_awsSDKErrorsAreNamedByService(t *testing.T) {
	// Given: an aws-sdk call against a table that does not exist, caught by name
	srv := helpers.NewTestServer(t)
	def := `{"StartAt":"Get","States":{
	  "Get":{"Type":"Task","Resource":"arn:aws:states:::aws-sdk:dynamodb:getItem",
	    "Parameters":{"TableName":"missing","Key":{"id":{"S":"k"}}},
	    "Catch":[{"ErrorEquals":["DynamoDb.ResourceNotFoundException"],"Next":"Caught"}],"End":true},
	  "Caught":{"Type":"Succeed"}}}`

	// When: it runs
	got, _ := runToEnd(t, srv, "sdk-err", def, `{}`)

	// Then: the error was DynamoDb.ResourceNotFoundException
	if got.Status != "SUCCEEDED" {
		t.Fatalf("status = %q (%s: %s)", got.Status, got.Error, got.Cause)
	}
}

func TestStartExecution_awsSDKCamelCaseServiceResultIsPascalCase(t *testing.T) {
	// Given: a state machine that describes itself through aws-sdk:sfn
	srv := helpers.NewTestServer(t)
	target := createSM(t, srv, "described", `{"StartAt":"P","States":{"P":{"Type":"Pass","End":true}}}`)
	def := `{"StartAt":"D","States":{"D":{"Type":"Task","Resource":"arn:aws:states:::aws-sdk:sfn:describeStateMachine",
	  "Parameters":{"StateMachineArn":"` + target + `"},"ResultSelector":{"name.$":"$.Name","type.$":"$.Type"},"End":true}}}`

	// When: it runs
	got, _ := runToEnd(t, srv, "sdk-sfn", def, `{}`)

	// Then: Step Functions' camelCase members arrive PascalCase, as on AWS
	if got.Status != "SUCCEEDED" || got.Output != `{"name":"described","type":"STANDARD"}` {
		t.Fatalf("status=%q output=%q (%s: %s)", got.Status, got.Output, got.Error, got.Cause)
	}
}

func TestStartExecution_awsSDKS3PutAndGetObject(t *testing.T) {
	// Given: a bucket, and a machine that writes an object then reads it back
	srv := helpers.NewTestServer(t)
	createBucket(t, srv, "sdk-bucket")
	def := `{"StartAt":"Put","States":{
	  "Put":{"Type":"Task","Resource":"arn:aws:states:::aws-sdk:s3:putObject",
	    "Parameters":{"Bucket":"sdk-bucket","Key":"out/data.json","Body.$":"$.doc"},"ResultPath":null,"Next":"Get"},
	  "Get":{"Type":"Task","Resource":"arn:aws:states:::aws-sdk:s3:getObject",
	    "Parameters":{"Bucket":"sdk-bucket","Key":"out/data.json"},"ResultSelector":{"body.$":"$.Body"},"End":true}}}`

	// When: it runs
	got, _ := runToEnd(t, srv, "sdk-s3", def, `{"doc":{"a":1}}`)

	// Then: the object round-trips
	if got.Status != "SUCCEEDED" || got.Output != `{"body":"{\"a\":1}"}` {
		t.Fatalf("status=%q output=%q (%s: %s)", got.Status, got.Output, got.Error, got.Cause)
	}
	if body := getS3Object(t, srv, "sdk-bucket", "out/data.json"); body != `{"a":1}` {
		t.Errorf("object = %q", body)
	}
}
