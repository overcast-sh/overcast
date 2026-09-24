package cloudformation_test

// pipes_pipe_test.go — AWS::Pipes::Pipe forwards the properties CDK/CloudFormation
// templates set (#533). pipesPipeHandler.Create used to send only Source,
// Target and RoleArn to CreatePipe, dropping SourceParameters, TargetParameters,
// Enrichment, EnrichmentParameters, Description, DesiredState and Tags — so a
// pipe configured with, say, a DynamoDB Streams batch size provisioned clean
// and ran with defaults instead. Update forwarded even less: only Description
// and DesiredState, and never RoleArn, which UpdatePipe requires on every call
// — so a template that changed only Description hit UpdatePipe's own
// "RoleArn is required" rejection.

import (
	"encoding/json"
	"net/http"
	"net/url"
	"reflect"
	"testing"

	"github.com/overcast-sh/overcast/tests/helpers"
)

// describePipe reads a pipe back through Pipes' own DescribePipe.
func describePipe(t *testing.T, srv *helpers.TestServer, name string) map[string]any {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, srv.URL+"/v1/pipes/"+name, nil)
	if err != nil {
		t.Fatalf("new DescribePipe request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("DescribePipe %s: %v", name, err)
	}
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	var out map[string]any
	helpers.DecodeJSON(t, resp, &out)
	return out
}

const pipesPipeTemplate = `{
  "Resources": {
    "Pipe": {
      "Type": "AWS::Pipes::Pipe",
      "Properties": {
        "Name": "cfn-forwarded-pipe",
        "Source": "arn:aws:sqs:us-east-1:000000000000:src",
        "Target": "arn:aws:sqs:us-east-1:000000000000:dst",
        "RoleArn": "arn:aws:iam::000000000000:role/pipes",
        "Description": "created by cloudformation",
        "DesiredState": "STOPPED",
        "SourceParameters": {
          "SqsQueueParameters": {"BatchSize": 5}
        },
        "TargetParameters": {
          "InputTemplate": "<$.body>"
        },
        "Enrichment": "arn:aws:lambda:us-east-1:000000000000:function:enrich",
        "EnrichmentParameters": {
          "InputTemplate": "{\"enriched\": true}"
        },
        "Tags": {"environment": "development"}
      }
    }
  }
}`

func TestCreateStack_PipesPipe_propertiesForwarded(t *testing.T) {
	// Given: a stack carrying a pipe with every forwardable property set
	srv := helpers.NewTestServer(t)
	resp := cfnQuery(t, srv, "CreateStack", url.Values{
		"StackName":    {"pipes-forward-create-stack"},
		"TemplateBody": {pipesPipeTemplate},
	})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	waitForStackStatus(t, srv, "pipes-forward-create-stack", "CREATE_COMPLETE")

	// Then: DescribePipe reports every one of them, not just source/target/roleArn
	pipe := describePipe(t, srv, "cfn-forwarded-pipe")
	if got := pipe["Description"]; got != "created by cloudformation" {
		t.Errorf("Description = %v, want %q", got, "created by cloudformation")
	}
	if got := pipe["DesiredState"]; got != "STOPPED" {
		t.Errorf("DesiredState = %v, want STOPPED", got)
	}
	if got := pipe["Enrichment"]; got != "arn:aws:lambda:us-east-1:000000000000:function:enrich" {
		t.Errorf("Enrichment = %v", got)
	}
	sourceParams, _ := pipe["SourceParameters"].(map[string]any)
	sqsParams, _ := sourceParams["SqsQueueParameters"].(map[string]any)
	if batchSize, _ := sqsParams["BatchSize"].(float64); batchSize != 5 {
		t.Errorf("SourceParameters.SqsQueueParameters.BatchSize = %v, want 5: %#v", sqsParams, pipe)
	}
	targetParams, _ := pipe["TargetParameters"].(map[string]any)
	if got := targetParams["InputTemplate"]; got != "<$.body>" {
		t.Errorf("TargetParameters.InputTemplate = %v", got)
	}
	enrichParams, _ := pipe["EnrichmentParameters"].(map[string]any)
	if got := enrichParams["InputTemplate"]; got != `{"enriched": true}` {
		t.Errorf("EnrichmentParameters.InputTemplate = %v", got)
	}
	tags, _ := pipe["Tags"].(map[string]any)
	if !reflect.DeepEqual(tags, map[string]any{"environment": "development"}) {
		t.Errorf("Tags = %#v, want {environment: development}", tags)
	}
}

// TestUpdateStack_PipesPipe_descriptionOnlyChangeSucceeds pins the regression
// #533 found: UpdatePipe requires RoleArn on every call, and the emulated CFN
// handler used to omit it whenever the changed property was Description or
// DesiredState — so this exact update used to fail with UpdatePipe's own
// "RoleArn is required" ValidationException.
func TestUpdateStack_PipesPipe_descriptionOnlyChangeSucceeds(t *testing.T) {
	srv := helpers.NewTestServer(t)
	create := cfnQuery(t, srv, "CreateStack", url.Values{
		"StackName":    {"pipes-update-desc-stack"},
		"TemplateBody": {pipesPipeTemplate},
	})
	defer create.Body.Close()
	helpers.AssertStatus(t, create, http.StatusOK)
	waitForStackStatus(t, srv, "pipes-update-desc-stack", "CREATE_COMPLETE")

	updated := setJSONString(t, pipesPipeTemplate,
		"$.Resources.Pipe.Properties.Description", "updated by cloudformation")
	update := cfnQuery(t, srv, "UpdateStack", url.Values{
		"StackName":    {"pipes-update-desc-stack"},
		"TemplateBody": {updated},
	})
	defer update.Body.Close()
	helpers.AssertStatus(t, update, http.StatusOK)
	waitForStackStatus(t, srv, "pipes-update-desc-stack", "UPDATE_COMPLETE")

	pipe := describePipe(t, srv, "cfn-forwarded-pipe")
	if got := pipe["Description"]; got != "updated by cloudformation" {
		t.Errorf("Description = %v, want %q", got, "updated by cloudformation")
	}
	// RoleArn must still be there — UpdatePipe requires it on every call, and
	// this is what would have 400'd before the fix.
	if got := pipe["RoleArn"]; got != "arn:aws:iam::000000000000:role/pipes" {
		t.Errorf("RoleArn = %v, want it to survive the update", got)
	}
}

// TestCreateStack_PipesPipe_dynamoDBStreamsBatchSizeForwarded pins #533's
// definition of done literally: a pipe with a DynamoDB Streams source and
// SourceParameters.DynamoDBStreamParameters.BatchSize set describes back with
// that batch size, rather than the emulated pipe running with the poller's
// default because the property never reached CreatePipe.
func TestCreateStack_PipesPipe_dynamoDBStreamsBatchSizeForwarded(t *testing.T) {
	srv := helpers.NewTestServer(t)
	const template = `{
  "Resources": {
    "Pipe": {
      "Type": "AWS::Pipes::Pipe",
      "Properties": {
        "Name": "cfn-ddb-batchsize-pipe",
        "Source": "arn:aws:dynamodb:us-east-1:000000000000:table/orders/stream/2024-01-01T00:00:00.000",
        "Target": "arn:aws:sqs:us-east-1:000000000000:dst",
        "RoleArn": "arn:aws:iam::000000000000:role/pipes",
        "SourceParameters": {
          "DynamoDBStreamParameters": {
            "StartingPosition": "LATEST",
            "BatchSize": 42
          }
        }
      }
    }
  }
}`
	resp := cfnQuery(t, srv, "CreateStack", url.Values{
		"StackName":    {"pipes-ddb-batchsize-stack"},
		"TemplateBody": {template},
	})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	waitForStackStatus(t, srv, "pipes-ddb-batchsize-stack", "CREATE_COMPLETE")

	pipe := describePipe(t, srv, "cfn-ddb-batchsize-pipe")
	sourceParams, _ := pipe["SourceParameters"].(map[string]any)
	ddbParams, _ := sourceParams["DynamoDBStreamParameters"].(map[string]any)
	if batchSize, _ := ddbParams["BatchSize"].(float64); batchSize != 42 {
		t.Errorf("SourceParameters.DynamoDBStreamParameters.BatchSize = %v, want 42: %#v", ddbParams, pipe)
	}
	if got := ddbParams["StartingPosition"]; got != "LATEST" {
		t.Errorf("SourceParameters.DynamoDBStreamParameters.StartingPosition = %v, want LATEST", got)
	}
}

// TestUpdateStack_PipesPipe_tagsOnlyChangeReconciles pins that a Tags-only
// change goes through TagResource/UntagResource rather than being dropped or
// forcing replacement — Pipes' UpdatePipe carries no Tags member of its own.
func TestUpdateStack_PipesPipe_tagsOnlyChangeReconciles(t *testing.T) {
	srv := helpers.NewTestServer(t)
	create := cfnQuery(t, srv, "CreateStack", url.Values{
		"StackName":    {"pipes-update-tags-stack"},
		"TemplateBody": {pipesPipeTemplate},
	})
	defer create.Body.Close()
	helpers.AssertStatus(t, create, http.StatusOK)
	waitForStackStatus(t, srv, "pipes-update-tags-stack", "CREATE_COMPLETE")

	updated := setJSONString(t, pipesPipeTemplate,
		"$.Resources.Pipe.Properties.Tags.environment", "production")
	update := cfnQuery(t, srv, "UpdateStack", url.Values{
		"StackName":    {"pipes-update-tags-stack"},
		"TemplateBody": {updated},
	})
	defer update.Body.Close()
	helpers.AssertStatus(t, update, http.StatusOK)
	waitForStackStatus(t, srv, "pipes-update-tags-stack", "UPDATE_COMPLETE")

	pipe := describePipe(t, srv, "cfn-forwarded-pipe")
	tags, _ := pipe["Tags"].(map[string]any)
	if !reflect.DeepEqual(tags, map[string]any{"environment": "production"}) {
		t.Errorf("Tags = %#v, want {environment: production}", tags)
	}
}

// setJSONString parses template, sets the string value at the given dotted
// path (a small subset of a JSONPath — object field traversal only, no
// arrays or wildcards) to value, and returns the re-marshalled template.
func setJSONString(t *testing.T, template, path, value string) string {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal([]byte(template), &doc); err != nil {
		t.Fatalf("setJSONString: unmarshal template: %v", err)
	}
	segments := parseDottedPath(t, path)
	cur := doc
	for _, seg := range segments[:len(segments)-1] {
		next, ok := cur[seg].(map[string]any)
		if !ok {
			t.Fatalf("setJSONString: %q is not an object in the template at %q", seg, path)
		}
		cur = next
	}
	cur[segments[len(segments)-1]] = value
	out, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("setJSONString: marshal template: %v", err)
	}
	return string(out)
}

func parseDottedPath(t *testing.T, path string) []string {
	t.Helper()
	var segments []string
	var cur []byte
	for i := 0; i < len(path); i++ {
		switch path[i] {
		case '$':
			continue
		case '.':
			if len(cur) > 0 {
				segments = append(segments, string(cur))
				cur = nil
			}
		default:
			cur = append(cur, path[i])
		}
	}
	if len(cur) > 0 {
		segments = append(segments, string(cur))
	}
	if len(segments) == 0 {
		t.Fatalf("parseDottedPath: empty path %q", path)
	}
	return segments
}
