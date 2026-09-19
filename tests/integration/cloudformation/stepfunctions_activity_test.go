package cloudformation_test

// Integration coverage for AWS::StepFunctions::Activity: provisioned through
// CloudFormation, read back through Step Functions' own API.

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/overcast-sh/overcast/tests/helpers"
)

const sfnActivityTemplate = `{
  "Resources": {
    "Approvals": {
      "Type": "AWS::StepFunctions::Activity",
      "Properties": {
        "Name": "cfn-approvals",
        "Tags": [{"Key": "team", "Value": "payments"}]
      }
    }
  },
  "Outputs": {
    "Ref": {"Value": {"Ref": "Approvals"}},
    "Arn": {"Value": {"Fn::GetAtt": ["Approvals", "Arn"]}},
    "Name": {"Value": {"Fn::GetAtt": ["Approvals", "Name"]}}
  }
}`

func TestCreateStack_StepFunctionsActivity(t *testing.T) {
	// Given: a template declaring an activity with a tag
	srv := helpers.NewTestServer(t)
	stackName := "sfn-activity"

	// When: the stack is created
	resp := cfnQuery(t, srv, "CreateStack", url.Values{
		"StackName":    {stackName},
		"TemplateBody": {sfnActivityTemplate},
	})
	resp.Body.Close()
	waitForStackStatus(t, srv, stackName, "CREATE_COMPLETE")

	// Then: Ref and GetAtt Arn are the activity ARN, GetAtt Name its name
	outputs := describeStackOutputs(t, srv, stackName)
	arn := outputs["Ref"]
	if !strings.HasSuffix(arn, ":activity:cfn-approvals") || outputs["Arn"] != arn || outputs["Name"] != "cfn-approvals" {
		t.Fatalf("outputs = %v", outputs)
	}
	described := sfnCFNJSONCall(t, srv, "DescribeActivity", map[string]any{"activityArn": arn})
	helpers.AssertStatus(t, described, http.StatusOK)
	described.Body.Close()
	tags := sfnCFNJSONCall(t, srv, "ListTagsForResource", map[string]any{"resourceArn": arn})
	if body := helpers.ReadBody(t, tags); !strings.Contains(body, `"payments"`) {
		t.Errorf("tags = %s", body)
	}
	tags.Body.Close()

	// When: the stack is deleted
	resp = cfnQuery(t, srv, "DeleteStack", url.Values{"StackName": {stackName}})
	resp.Body.Close()
	waitForStackStatus(t, srv, stackName, "DELETE_COMPLETE")

	// Then: the activity is gone
	gone := sfnCFNJSONCall(t, srv, "DescribeActivity", map[string]any{"activityArn": arn})
	defer gone.Body.Close()
	helpers.AssertStatus(t, gone, http.StatusBadRequest)
}
