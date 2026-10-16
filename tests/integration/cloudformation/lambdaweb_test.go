package cloudformation_test

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/lambdaweb"
	"github.com/aws/aws-sdk-go-v2/service/lambdaweb/types"

	"github.com/overcast-sh/overcast/tests/helpers"
)

// lambdaWebStackTemplate is the shape a CDK Web Function stack produces: the
// container, an immutable revision built from an S3 package, and an endpoint
// that routes all traffic to the revision.
const lambdaWebStackTemplate = `{
  "AWSTemplateFormatVersion": "2010-09-09",
  "Resources": {
    "App": {
      "Type": "AWS::Lambda::WebFunction",
      "Properties": {
        "FunctionName": "cfn-web-app",
        "Tags": [{"Key": "team", "Value": "web"}]
      }
    },
    "AppRevision": {
      "Type": "AWS::Lambda::WebFunctionRevision",
      "Properties": {
        "FunctionName": {"Ref": "App"},
        "Description": "first",
        "BuildConfig": {
          "CodeConfig": {"S3Object": {"Bucket": "code-bucket", "Key": "app.zip"}},
          "RuntimeConfig": {"Runtime": "nodejs24.x"}
        },
        "ServiceConfig": {
          "ExecutionRoleArn": "arn:aws:iam::000000000000:role/web-role",
          "TimeoutSeconds": 45,
          "EnvironmentVariables": {"LogLevel": "debug"}
        }
      }
    },
    "AppEndpoint": {
      "Type": "AWS::Lambda::WebFunctionEndpoint",
      "Properties": {
        "FunctionName": "cfn-web-app",
        "EndpointName": "prod",
        "EndpointType": "HomeRegion",
        "AuthType": "IamAuth",
        "RevisionWeights": [{"RevisionId": {"Fn::GetAtt": ["AppRevision", "RevisionId"]}, "Weight": 100}],
        "ThrottleConfig": {"RateLimit": 1000}
      }
    }
  },
  "Outputs": {
    "FunctionRef": {"Value": {"Ref": "App"}},
    "RevisionRef": {"Value": {"Ref": "AppRevision"}},
    "RevisionId": {"Value": {"Fn::GetAtt": ["AppRevision", "RevisionId"]}},
    "EndpointRef": {"Value": {"Ref": "AppEndpoint"}},
    "EndpointDomain": {"Value": {"Fn::GetAtt": ["AppEndpoint", "DomainName"]}}
  }
}`

func lambdaWebClient(srv *helpers.TestServer) *lambdaweb.Client {
	return lambdaweb.New(lambdaweb.Options{
		Region:       "us-east-1",
		Credentials:  credentials.NewStaticCredentialsProvider("test", "test", ""),
		BaseEndpoint: aws.String(srv.URL),
		HTTPClient:   http.DefaultClient,
	})
}

func TestCreateStack_LambdaWebFunction(t *testing.T) {
	// Given: a stack of every Web Function resource type
	srv := helpers.NewTestServer(t)
	resp := cfnQuery(t, srv, "CreateStack", url.Values{
		"StackName":    []string{"web-stack"},
		"TemplateBody": []string{lambdaWebStackTemplate},
	})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)

	// Then: it completes, and Ref is each resource's ARN
	waitForStackStatus(t, srv, "web-stack", "CREATE_COMPLETE")
	out := describeStackOutputs(t, srv, "web-stack")
	fnARN := "arn:aws:lambda:us-east-1:000000000000:web-function/cfn-web-app"
	if out["FunctionRef"] != fnARN {
		t.Errorf("FunctionRef = %q", out["FunctionRef"])
	}
	if out["RevisionRef"] != fnARN+"/revision/"+out["RevisionId"] || !strings.HasPrefix(out["RevisionId"], "rev-") {
		t.Errorf("RevisionRef = %q, RevisionId = %q", out["RevisionRef"], out["RevisionId"])
	}
	if out["EndpointRef"] != fnARN+"/endpoint/prod" || !strings.HasSuffix(out["EndpointDomain"], ".lambda-web.us-east-1.on.aws") {
		t.Errorf("EndpointRef = %q, EndpointDomain = %q", out["EndpointRef"], out["EndpointDomain"])
	}

	// And: the resources exist through the service, configured as declared
	c := lambdaWebClient(srv)
	ctx := context.Background()
	tags, err := c.ListTags(ctx, &lambdaweb.ListTagsInput{Resource: aws.String(fnARN)})
	if err != nil || tags.Tags["team"] != "web" {
		t.Errorf("tags = %+v, %v", tags, err)
	}
	rev, err := c.GetWebFunctionRevision(ctx, &lambdaweb.GetWebFunctionRevisionInput{FunctionName: aws.String("cfn-web-app"), RevisionId: aws.String(out["RevisionId"])})
	if err != nil {
		t.Fatalf("GetWebFunctionRevision: %v", err)
	}
	// The environment variable keeps its case: it is data, not a shape.
	if aws.ToInt32(rev.ServiceConfig.TimeoutSeconds) != 45 || rev.ServiceConfig.EnvironmentVariables["LogLevel"] != "debug" ||
		aws.ToString(rev.Description) != "first" {
		t.Errorf("revision = %+v", rev)
	}
	ep, err := c.GetWebFunctionEndpoint(ctx, &lambdaweb.GetWebFunctionEndpointInput{FunctionName: aws.String("cfn-web-app"), EndpointName: aws.String("prod")})
	if err != nil {
		t.Fatalf("GetWebFunctionEndpoint: %v", err)
	}
	if ep.AuthType != types.AuthTypeIamAuth || len(ep.RevisionWeights) != 1 || aws.ToString(ep.RevisionWeights[0].RevisionId) != out["RevisionId"] ||
		aws.ToInt32(ep.ThrottleConfig.RateLimit) != 1000 {
		t.Errorf("endpoint = %+v", ep)
	}

	// When: the stack is deleted
	del := cfnQuery(t, srv, "DeleteStack", url.Values{"StackName": []string{"web-stack"}})
	defer del.Body.Close()
	helpers.AssertStatus(t, del, http.StatusOK)

	// Then: the endpoint, the revision and the function go
	helpers.Eventually(t, 5*time.Second, 20*time.Millisecond, func() bool {
		list, err := c.ListWebFunctions(ctx, &lambdaweb.ListWebFunctionsInput{})
		return err == nil && len(list.Functions) == 0
	}, "timed out waiting for the web function to be deleted with the stack")
}

func TestUpdateStack_LambdaWebFunctionEndpointInPlaceAndRevisionReplaced(t *testing.T) {
	// Given: the stack above
	srv := helpers.NewTestServer(t)
	resp := cfnQuery(t, srv, "CreateStack", url.Values{
		"StackName":    []string{"web-update"},
		"TemplateBody": []string{lambdaWebStackTemplate},
	})
	resp.Body.Close()
	waitForStackStatus(t, srv, "web-update", "CREATE_COMPLETE")
	before := describeStackOutputs(t, srv, "web-update")

	// When: the endpoint's auth type and throttle change, and the revision's code does
	updated := strings.Replace(lambdaWebStackTemplate, `"AuthType": "IamAuth"`, `"AuthType": "ApplicationManaged"`, 1)
	updated = strings.Replace(updated, `"RateLimit": 1000`, `"RateLimit": 2000`, 1)
	updated = strings.Replace(updated, `"Key": "app.zip"`, `"Key": "app-v2.zip"`, 1)
	up := cfnQuery(t, srv, "UpdateStack", url.Values{
		"StackName":    []string{"web-update"},
		"TemplateBody": []string{updated},
	})
	up.Body.Close()
	waitForStackStatus(t, srv, "web-update", "UPDATE_COMPLETE")

	// Then: the endpoint was updated in place and now serves the replacement revision
	after := describeStackOutputs(t, srv, "web-update")
	if after["EndpointRef"] != before["EndpointRef"] || after["RevisionRef"] == before["RevisionRef"] {
		t.Errorf("endpoint %q -> %q, revision %q -> %q", before["EndpointRef"], after["EndpointRef"], before["RevisionRef"], after["RevisionRef"])
	}
	c := lambdaWebClient(srv)
	ep, err := c.GetWebFunctionEndpoint(context.Background(), &lambdaweb.GetWebFunctionEndpointInput{FunctionName: aws.String("cfn-web-app"), EndpointName: aws.String("prod")})
	if err != nil {
		t.Fatalf("GetWebFunctionEndpoint: %v", err)
	}
	if ep.AuthType != types.AuthTypeApplicationManaged || aws.ToInt32(ep.ThrottleConfig.RateLimit) != 2000 ||
		len(ep.RevisionWeights) != 1 || aws.ToString(ep.RevisionWeights[0].RevisionId) != after["RevisionId"] {
		t.Errorf("endpoint = %+v", ep)
	}
}
