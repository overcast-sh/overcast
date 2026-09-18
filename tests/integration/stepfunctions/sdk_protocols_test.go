// Package stepfunctions_test — `aws-sdk:<service>:<action>` integrations over
// every wire protocol Overcast serves.
//
// Each test runs a state machine whose Task calls a real service through the
// SDK integration and asserts on the result exactly as a workflow sees it:
// PascalCase parameter and result member names, user-data map keys left as
// written, and errors named `<Service>.<Exception>`.
//
// Run: go test ./tests/integration/stepfunctions/...
package stepfunctions_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/overcast-sh/overcast/tests/helpers"
)

// runSDK runs a definition to completion and decodes its JSON output.
func runSDK(t *testing.T, srv *helpers.TestServer, name, def, input string) map[string]any {
	t.Helper()
	got, _ := runToEnd(t, srv, name, def, input)
	if got.Status != "SUCCEEDED" {
		t.Fatalf("status = %q (%s: %s)", got.Status, got.Error, got.Cause)
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(got.Output), &out); err != nil {
		t.Fatalf("output %q: %v", got.Output, err)
	}
	return out
}

func TestStartExecution_awsSDKQueryProtocolIAM(t *testing.T) {
	// Given: a machine that creates an IAM role (AWS Query) and reads it back
	srv := helpers.NewTestServer(t)
	def := `{"StartAt":"Create","States":{
	  "Create":{"Type":"Task","Resource":"arn:aws:states:::aws-sdk:iam:createRole",
	    "Parameters":{"RoleName":"sdk-role","AssumeRolePolicyDocument":"{\"Version\":\"2012-10-17\",\"Statement\":[{\"Effect\":\"Allow\",\"Principal\":{\"Service\":\"states.amazonaws.com\"},\"Action\":\"sts:AssumeRole\"}]}",
	      "Tags":[{"Key":"team","Value":"blue"}]},
	    "ResultPath":"$.created","Next":"Get"},
	  "Get":{"Type":"Task","Resource":"arn:aws:states:::aws-sdk:iam:getRole",
	    "Parameters":{"RoleName":"sdk-role"},"ResultPath":"$.got","Next":"List"},
	  "List":{"Type":"Task","Resource":"arn:aws:states:::aws-sdk:iam:listRoles",
	    "ResultSelector":{"count.$":"States.ArrayLength($.Roles)"},"ResultPath":"$.listed","End":true}}}`

	// When: it runs
	out := runSDK(t, srv, "sdk-iam", def, `{}`)

	// Then: Query results come back as PascalCase JSON with lists as arrays
	created := out["created"].(map[string]any)["Role"].(map[string]any)
	if created["RoleName"] != "sdk-role" || !strings.HasPrefix(created["Arn"].(string), "arn:aws:iam::") {
		t.Errorf("created = %v", created)
	}
	if _, isString := created["CreateDate"].(string); !isString {
		t.Errorf("CreateDate = %#v, want an ISO-8601 string", created["CreateDate"])
	}
	got := out["got"].(map[string]any)["Role"].(map[string]any)
	tags, _ := got["Tags"].([]any)
	if got["RoleName"] != "sdk-role" || len(tags) != 1 || tags[0].(map[string]any)["Key"] != "team" {
		t.Errorf("got = %v", got)
	}
	if n, _ := out["listed"].(map[string]any)["count"].(float64); n < 1 {
		t.Errorf("listed = %v", out["listed"])
	}
}

func TestStartExecution_awsSDKQueryErrorIsNamedByService(t *testing.T) {
	// Given: a getRole for a role that does not exist, caught by its SDK name
	srv := helpers.NewTestServer(t)
	def := `{"StartAt":"Get","States":{
	  "Get":{"Type":"Task","Resource":"arn:aws:states:::aws-sdk:iam:getRole",
	    "Parameters":{"RoleName":"nobody"},
	    "Catch":[{"ErrorEquals":["Iam.NoSuchEntityException"],"ResultPath":"$.err","Next":"Caught"}],"End":true},
	  "Caught":{"Type":"Pass","End":true}}}`

	// When: it runs
	out := runSDK(t, srv, "sdk-iam-err", def, `{}`)

	// Then: the Catch matched, and the cause names the service and status
	cause, _ := out["err"].(map[string]any)["Cause"].(string)
	if !strings.Contains(cause, "nobody") || !strings.Contains(cause, "Service: Iam") || !strings.Contains(cause, "Status Code: 404") {
		t.Errorf("cause = %q", cause)
	}
}

func TestStartExecution_awsSDKQueryProtocolSNSAttributesMap(t *testing.T) {
	// Given: an SNS topic, a display name set on it, and its attributes read back
	srv := helpers.NewTestServer(t)
	def := `{"StartAt":"Create","States":{
	  "Create":{"Type":"Task","Resource":"arn:aws:states:::aws-sdk:sns:createTopic",
	    "Parameters":{"Name":"sdk-topic","Tags":[{"Key":"team","Value":"blue"}]},"ResultPath":"$.topic","Next":"Set"},
	  "Set":{"Type":"Task","Resource":"arn:aws:states:::aws-sdk:sns:setTopicAttributes",
	    "Parameters":{"TopicArn.$":"$.topic.TopicArn","AttributeName":"DisplayName","AttributeValue":"Orders"},
	    "ResultPath":null,"Next":"Get"},
	  "Get":{"Type":"Task","Resource":"arn:aws:states:::aws-sdk:sns:getTopicAttributes",
	    "Parameters":{"TopicArn.$":"$.topic.TopicArn"},
	    "ResultSelector":{"display.$":"$.Attributes.DisplayName","arn.$":"$.Attributes.TopicArn"},"End":true}}}`

	// When: it runs
	out := runSDK(t, srv, "sdk-sns", def, `{}`)

	// Then: the Query map round-trips with its keys as written
	if out["display"] != "Orders" || !strings.HasSuffix(out["arn"].(string), ":sdk-topic") {
		t.Errorf("output = %v", out)
	}
}

func TestStartExecution_awsSDKEC2QueryProtocol(t *testing.T) {
	// Given: a machine that creates a VPC (EC2 Query) and describes it by ID
	srv := helpers.NewTestServer(t)
	def := `{"StartAt":"Create","States":{
	  "Create":{"Type":"Task","Resource":"arn:aws:states:::aws-sdk:ec2:createVpc",
	    "Parameters":{"CidrBlock":"10.42.0.0/16"},"ResultSelector":{"id.$":"$.Vpc.VpcId"},"ResultPath":"$.vpc","Next":"Describe"},
	  "Describe":{"Type":"Task","Resource":"arn:aws:states:::aws-sdk:ec2:describeVpcs",
	    "Parameters":{"VpcIds.$":"States.Array($.vpc.id)"},
	    "ResultSelector":{"cidr.$":"$.Vpcs[0].CidrBlock","count.$":"States.ArrayLength($.Vpcs)"},"End":true}}}`

	// When: it runs
	out := runSDK(t, srv, "sdk-ec2", def, `{}`)

	// Then: EC2's <item> lists arrive as arrays with PascalCase members
	if out["cidr"] != "10.42.0.0/16" || out["count"] != float64(1) {
		t.Errorf("output = %v", out)
	}
}

func TestStartExecution_awsSDKEC2UnmodeledErrorIsServiceException(t *testing.T) {
	// Given: a describe of an instance that does not exist — EC2 models no errors
	srv := helpers.NewTestServer(t)
	def := `{"StartAt":"D","States":{
	  "D":{"Type":"Task","Resource":"arn:aws:states:::aws-sdk:ec2:describeInstances",
	    "Parameters":{"InstanceIds":["i-0123456789abcdef0"]},
	    "Catch":[{"ErrorEquals":["Ec2.Ec2Exception"],"ResultPath":"$.err","Next":"Caught"}],"End":true},
	  "Caught":{"Type":"Pass","End":true}}}`

	// When: it runs
	out := runSDK(t, srv, "sdk-ec2-err", def, `{}`)

	// Then: the SDK's service exception is caught and the cause keeps the code
	if cause, _ := out["err"].(map[string]any)["Cause"].(string); !strings.Contains(cause, "InvalidInstanceID.NotFound") {
		t.Errorf("cause = %q", cause)
	}
}

func TestStartExecution_awsSDKRESTJSONKeepsMapKeys(t *testing.T) {
	// Given: API Gateway (REST-JSON, camelCase on the wire) with a tags map
	srv := helpers.NewTestServer(t)
	def := `{"StartAt":"Create","States":{
	  "Create":{"Type":"Task","Resource":"arn:aws:states:::aws-sdk:apigateway:createRestApi",
	    "Parameters":{"Name":"sdk-api","Description":"from a workflow","Tags":{"team":"blue","cost-centre":"42"}},
	    "ResultPath":"$.api","Next":"Get"},
	  "Get":{"Type":"Task","Resource":"arn:aws:states:::aws-sdk:apigateway:getRestApi",
	    "Parameters":{"RestApiId.$":"$.api.Id"},"End":true}}}`

	// When: it runs
	out := runSDK(t, srv, "sdk-apigw", def, `{}`)

	// Then: members are PascalCase and the user's tag keys are exactly as written
	if out["Name"] != "sdk-api" || out["Description"] != "from a workflow" {
		t.Errorf("output = %v", out)
	}
	tags, _ := out["Tags"].(map[string]any)
	if tags["team"] != "blue" || tags["cost-centre"] != "42" || tags["Team"] != nil {
		t.Errorf("Tags = %v, want the keys untouched", out["Tags"])
	}
	if _, isString := out["CreatedDate"].(string); !isString {
		t.Errorf("CreatedDate = %#v, want an ISO-8601 string", out["CreatedDate"])
	}
}

func TestStartExecution_awsSDKRESTJSONLambdaNotFound(t *testing.T) {
	// Given: a Lambda getFunction (REST-JSON, label-bound) for a missing function
	srv := helpers.NewTestServer(t)
	def := `{"StartAt":"G","States":{
	  "G":{"Type":"Task","Resource":"arn:aws:states:::aws-sdk:lambda:getFunction",
	    "Parameters":{"FunctionName":"absent"},
	    "Catch":[{"ErrorEquals":["Lambda.ResourceNotFoundException"],"Next":"Caught"}],"End":true},
	  "Caught":{"Type":"Pass","Result":"caught","End":true}}}`

	// When: it runs
	got, _ := runToEnd(t, srv, "sdk-lambda-err", def, `{}`)

	// Then: the modeled Lambda error is catchable by its SDK name
	if got.Status != "SUCCEEDED" || got.Output != `"caught"` {
		t.Fatalf("status=%q output=%q (%s: %s)", got.Status, got.Output, got.Error, got.Cause)
	}
}

func TestStartExecution_awsSDKRESTXMLS3ObjectWithMetadata(t *testing.T) {
	// Given: a bucket, and a machine that writes an object with user metadata,
	// reads it back, and lists the bucket
	srv := helpers.NewTestServer(t)
	createBucket(t, srv, "sdk-meta")
	def := `{"StartAt":"Put","States":{
	  "Put":{"Type":"Task","Resource":"arn:aws:states:::aws-sdk:s3:putObject",
	    "Parameters":{"Bucket":"sdk-meta","Key":"docs/a.txt","Body":"hello world","ContentType":"text/plain",
	      "Metadata":{"colour":"blue","owner-id":"u-1"}},
	    "ResultPath":"$.put","Next":"Get"},
	  "Get":{"Type":"Task","Resource":"arn:aws:states:::aws-sdk:s3:getObject",
	    "Parameters":{"Bucket":"sdk-meta","Key":"docs/a.txt"},"ResultPath":"$.get","Next":"List"},
	  "List":{"Type":"Task","Resource":"arn:aws:states:::aws-sdk:s3:listObjectsV2",
	    "Parameters":{"Bucket":"sdk-meta","Prefix":"docs/"},"ResultPath":"$.list","End":true}}}`

	// When: it runs
	out := runSDK(t, srv, "sdk-s3-meta", def, `{}`)

	// Then: the body, headers and metadata map round-trip with keys untouched
	if etag, _ := out["put"].(map[string]any)["ETag"].(string); etag == "" {
		t.Errorf("put = %v", out["put"])
	}
	get := out["get"].(map[string]any)
	if get["Body"] != "hello world" || get["ContentType"] != "text/plain" || get["ContentLength"] != float64(11) {
		t.Errorf("get = %v", get)
	}
	metadata, _ := get["Metadata"].(map[string]any)
	if metadata["colour"] != "blue" || metadata["owner-id"] != "u-1" || metadata["Colour"] != nil {
		t.Errorf("Metadata = %v, want the keys untouched", get["Metadata"])
	}
	list := out["list"].(map[string]any)
	contents, _ := list["Contents"].([]any)
	if list["KeyCount"] != float64(1) || len(contents) != 1 || contents[0].(map[string]any)["Key"] != "docs/a.txt" {
		t.Errorf("list = %v", list)
	}
	if body := getS3Object(t, srv, "sdk-meta", "docs/a.txt"); body != "hello world" {
		t.Errorf("object = %q", body)
	}
}

func TestStartExecution_awsSDKRESTXMLNoSuchKeyIsCatchable(t *testing.T) {
	// Given: a getObject for a key that does not exist
	srv := helpers.NewTestServer(t)
	createBucket(t, srv, "sdk-missing")
	def := `{"StartAt":"Get","States":{
	  "Get":{"Type":"Task","Resource":"arn:aws:states:::aws-sdk:s3:getObject",
	    "Parameters":{"Bucket":"sdk-missing","Key":"nope"},
	    "Catch":[{"ErrorEquals":["S3.NoSuchKeyException"],"Next":"Caught"}],"End":true},
	  "Caught":{"Type":"Pass","Result":"caught","End":true}}}`

	// When: it runs
	got, _ := runToEnd(t, srv, "sdk-s3-nokey", def, `{}`)

	// Then: the S3 error is caught by its SDK exception name
	if got.Status != "SUCCEEDED" || got.Output != `"caught"` {
		t.Fatalf("status=%q output=%q (%s: %s)", got.Status, got.Output, got.Error, got.Cause)
	}
}

func TestStartExecution_awsSDKJSONProtocolSQSMessageAttributes(t *testing.T) {
	// Given: a queue and a machine that sends a message with attributes and receives it
	srv := helpers.NewTestServer(t)
	queueURL := createQueue(t, srv, "sdk-queue")
	def := `{"StartAt":"Send","States":{
	  "Send":{"Type":"Task","Resource":"arn:aws:states:::aws-sdk:sqs:sendMessage",
	    "Parameters":{"QueueUrl":"` + queueURL + `","MessageBody":"hi",
	      "MessageAttributes":{"trace-id":{"DataType":"String","StringValue":"t-1"}}},"ResultPath":"$.sent","Next":"Receive"},
	  "Receive":{"Type":"Task","Resource":"arn:aws:states:::aws-sdk:sqs:receiveMessage",
	    "Parameters":{"QueueUrl":"` + queueURL + `","MessageAttributeNames":["All"]},
	    "ResultSelector":{"body.$":"$.Messages[0].Body","attr.$":"$.Messages[0].MessageAttributes.trace-id.StringValue"},"End":true}}}`

	// When: it runs
	out := runSDK(t, srv, "sdk-sqs", def, `{}`)

	// Then: the attribute map key is as written
	if out["body"] != "hi" || out["attr"] != "t-1" {
		t.Errorf("output = %v", out)
	}
}

func TestStartExecution_awsSDKRPCv2CBORCloudWatch(t *testing.T) {
	// Given: CloudWatch, whose SDKs speak Smithy RPC v2 CBOR, fed a metric
	srv := helpers.NewTestServer(t)
	def := `{"StartAt":"Put","States":{
	  "Put":{"Type":"Task","Resource":"arn:aws:states:::aws-sdk:cloudwatch:putMetricData",
	    "Parameters":{"Namespace":"Sdk/Test","MetricData":[{"MetricName":"Orders","Value":3,"Unit":"Count",
	      "Dimensions":[{"Name":"shop","Value":"north"}]}]},"ResultPath":null,"Next":"List"},
	  "List":{"Type":"Task","Resource":"arn:aws:states:::aws-sdk:cloudwatch:listMetrics",
	    "Parameters":{"Namespace":"Sdk/Test"},
	    "ResultSelector":{"name.$":"$.Metrics[0].MetricName","dim.$":"$.Metrics[0].Dimensions[0].Value"},"End":true}}}`

	// When: it runs
	out := runSDK(t, srv, "sdk-cw", def, `{}`)

	// Then: the metric is listed
	if out["name"] != "Orders" || out["dim"] != "north" {
		t.Errorf("output = %v", out)
	}
}

func TestCreateStateMachine_awsSDKUnknownActionIsInvalid(t *testing.T) {
	cases := map[string]string{
		"unknown action":    `arn:aws:states:::aws-sdk:s3:frobnicateObject`,
		"unknown service":   `arn:aws:states:::aws-sdk:nosuchservice:getThing`,
		"PascalCase action": `arn:aws:states:::aws-sdk:s3:GetObject`,
		"sync pattern":      `arn:aws:states:::aws-sdk:s3:getObject.sync`,
	}
	for name, resource := range cases {
		t.Run(name, func(t *testing.T) {
			// Given: a definition naming an aws-sdk resource AWS does not recognise
			srv := helpers.NewTestServer(t)
			def := `{"StartAt":"T","States":{"T":{"Type":"Task","Resource":"` + resource + `","End":true}}}`

			// When: it is created
			resp := sfnCall(t, srv, "CreateStateMachine", map[string]any{
				"name": "bad-sdk", "definition": def, "roleArn": "arn:aws:iam::000000000000:role/r",
			})
			defer resp.Body.Close()

			// Then: it is rejected as an invalid definition
			helpers.AssertStatus(t, resp, http.StatusBadRequest)
			if body := helpers.ReadBody(t, resp); !strings.Contains(body, "InvalidDefinition") {
				t.Errorf("body = %s", body)
			}
		})
	}
}

func TestCreateStateMachine_awsSDKUnknownParameterIsInvalid(t *testing.T) {
	// Given: an aws-sdk Task whose Parameters name a member the action does not have
	srv := helpers.NewTestServer(t)
	def := `{"StartAt":"T","States":{"T":{"Type":"Task","Resource":"arn:aws:states:::aws-sdk:s3:getObject",
	  "Parameters":{"Bucket":"b","Key":"k","Colour":"blue"},"End":true}}}`

	// When: it is created
	resp := sfnCall(t, srv, "CreateStateMachine", map[string]any{
		"name": "bad-param", "definition": def, "roleArn": "arn:aws:iam::000000000000:role/r",
	})
	defer resp.Body.Close()

	// Then: it is rejected naming the field
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	if body := helpers.ReadBody(t, resp); !strings.Contains(body, "Colour") {
		t.Errorf("body = %s", body)
	}
}
