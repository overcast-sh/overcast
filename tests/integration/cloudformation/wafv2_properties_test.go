package cloudformation_test

// wafv2_properties_test.go — AWS::WAFv2::WebACL property threading (#2060,
// the #540 tail pass).
//
// The handler forwarded only Name, Scope, DefaultAction, VisibilityConfig and
// Rules. Description and Tags were dropped outright, and CreateWebACL's
// response carried an ARN and an Id that Create discarded — so a template
// that referenced `{"Fn::GetAtt": ["WebACL", "Arn"]}` or `"Id"` never
// resolved, even though the underlying service returns both.

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/overcast-sh/overcast/tests/helpers"
)

// wafv2WebACLPropertiesTemplate sets every AWS::WAFv2::WebACL property the
// handler forwards, plus one it does not (CustomResponseBodies) to pin the
// unconsumed-property notice.
const wafv2WebACLPropertiesTemplate = `{
  "AWSTemplateFormatVersion": "2010-09-09",
  "Resources": {
    "WebACL": {
      "Type": "AWS::WAFv2::WebACL",
      "Properties": {
        "Name": "cfn-props-acl",
        "Scope": "REGIONAL",
        "Description": "cfn props test acl",
        "DefaultAction": {"Allow": {}},
        "VisibilityConfig": {
          "CloudWatchMetricsEnabled": false,
          "MetricName": "cfn-props-acl",
          "SampledRequestsEnabled": false
        },
        "Tags": [
          {"Key": "env", "Value": "prod"},
          {"Key": "Owner", "Value": "platform"}
        ],
        "CustomResponseBodies": {
          "custom-body": {"ContentType": "TEXT_PLAIN", "Content": "blocked"}
        }
      }
    }
  },
  "Outputs": {
    "Arn": {"Value": {"Fn::GetAtt": ["WebACL", "Arn"]}},
    "Id": {"Value": {"Fn::GetAtt": ["WebACL", "Id"]}}
  }
}`

// TestCreateStack_WAFv2WebACL_propertiesThreaded is the failing-first case for
// the AWS::WAFv2::WebACL half of #2060. Before the fix, Description and Tags
// were dropped, CreateWebACL's response attributes were discarded so
// Fn::GetAtt Arn/Id never resolved, and the dropped CustomResponseBodies
// property vanished in silence.
func TestCreateStack_WAFv2WebACL_propertiesThreaded(t *testing.T) {
	srv := helpers.NewTestServer(t)
	const stackName = "wafv2-webacl-properties-stack"

	create := cfnQuery(t, srv, "CreateStack", url.Values{
		"StackName":    {stackName},
		"TemplateBody": {wafv2WebACLPropertiesTemplate},
	})
	defer create.Body.Close()
	helpers.AssertStatus(t, create, http.StatusOK)
	waitForStackStatus(t, srv, stackName, "CREATE_COMPLETE")

	// The behavioural one: Fn::GetAtt Arn and Id must resolve to CreateWebACL's
	// own response, not an empty string.
	arn := stackOutput(t, srv, stackName, "Arn")
	if !strings.HasPrefix(arn, "arn:aws:wafv2:") {
		t.Errorf("Fn::GetAtt Arn = %q, want an arn:aws:wafv2:... ARN", arn)
	}
	id := stackOutput(t, srv, stackName, "Id")
	if id == "" {
		t.Errorf("Fn::GetAtt Id = %q, want CreateWebACL's own Id", id)
	}

	// Description forwards straight through and reads back via GetWebACL.
	acl := wafv2GetWebACL(t, srv, id, "cfn-props-acl", "REGIONAL")
	webACL, _ := acl["WebACL"].(map[string]any)
	if got := webACL["Description"]; got != "cfn props test acl" {
		t.Errorf("WebACL.Description = %v, want %q", got, "cfn props test acl")
	}
	if got := webACL["ARN"]; got != arn {
		t.Errorf("GetWebACL ARN = %v, want the stack output's ARN %q", got, arn)
	}

	// Tags do not come back on GetWebACL — they read through
	// ListTagsForResource, keyed by the ARN the Fn::GetAtt output just proved.
	tagsResp := awsJSONCall(t, srv, "AWSWAF_20190729.", "ListTagsForResource",
		"application/x-amz-json-1.1", map[string]any{"ResourceARN": arn})
	defer tagsResp.Body.Close()
	helpers.AssertStatus(t, tagsResp, http.StatusOK)
	var tagsOut struct {
		TagInfoForResource struct {
			TagList []struct {
				Key   string `json:"Key"`
				Value string `json:"Value"`
			} `json:"TagList"`
		} `json:"TagInfoForResource"`
	}
	if err := json.Unmarshal(readBody(t, tagsResp), &tagsOut); err != nil {
		t.Fatalf("decode ListTagsForResource response: %v", err)
	}
	gotTags := map[string]string{}
	for _, tag := range tagsOut.TagInfoForResource.TagList {
		gotTags[tag.Key] = tag.Value
	}
	if gotTags["env"] != "prod" || gotTags["Owner"] != "platform" {
		t.Errorf("ListTagsForResource tags = %v, want the template's two tags with their keys unchanged", gotTags)
	}

	// And the property the handler does not act on is reported rather than
	// dropped in silence — see noteUnconsumedProperties.
	reasons := describeStackResourceReasons(t, srv, stackName)
	if !strings.Contains(reasons, "CustomResponseBodies") {
		t.Errorf("expected the WebACL's ResourceStatusReason to name the unapplied CustomResponseBodies, got: %s", reasons)
	}
}

// wafv2GetWebACL reads a web ACL back through GetWebACL.
func wafv2GetWebACL(t *testing.T, srv *helpers.TestServer, id, name, scope string) map[string]any {
	t.Helper()
	resp := awsJSONCall(t, srv, "AWSWAF_20190729.", "GetWebACL", "application/x-amz-json-1.1", map[string]any{
		"Id":    id,
		"Name":  name,
		"Scope": scope,
	})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	var out map[string]any
	if err := json.Unmarshal(readBody(t, resp), &out); err != nil {
		t.Fatalf("GetWebACL %s: parse response: %v", name, err)
	}
	return out
}
