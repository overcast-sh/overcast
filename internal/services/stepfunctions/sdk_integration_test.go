package stepfunctions

import (
	"context"
	"io"
	"net/url"
	"reflect"
	"testing"
	"time"

	cborlib "github.com/fxamacker/cbor/v2"

	"github.com/overcast-sh/overcast/internal/awsapi"
)

func mustResolve(t *testing.T, service, action string) *sdkCall {
	t.Helper()
	call, serr := resolveSDKCall(service, action)
	if serr != nil {
		t.Fatalf("resolveSDKCall(%s, %s): %s: %s", service, action, serr.name, serr.cause)
	}
	return call
}

func TestResolveSDKCall_protocolPreference(t *testing.T) {
	cases := []struct {
		service, action string
		want            awsapi.Protocol
	}{
		{"cloudwatch", "putMetricData", awsapi.ProtocolRPCV2CBOR},
		{"dynamodb", "getItem", awsapi.ProtocolAWSJSON10},
		{"iam", "getRole", awsapi.ProtocolAWSQuery},
		{"ec2", "describeVpcs", awsapi.ProtocolEC2Query},
		{"lambda", "getFunction", awsapi.ProtocolRESTJSON},
		{"s3", "getObject", awsapi.ProtocolRESTXML},
	}
	for _, tc := range cases {
		// Given: an aws-sdk service and action
		// When: it is resolved
		call := mustResolve(t, tc.service, tc.action)

		// Then: the protocol is the one an AWS SDK would pick
		if call.protocol != tc.want {
			t.Errorf("%s:%s protocol = %s, want %s", tc.service, tc.action, call.protocol, tc.want)
		}
	}
}

func TestBuildRequest_rpcv2CBOR(t *testing.T) {
	// Given: a CloudWatch call, whose SDKs speak Smithy RPC v2 CBOR
	call := mustResolve(t, "cloudwatch", "listMetrics")

	// When: its request is built
	req, err := call.buildRequest(context.Background(), map[string]any{"Namespace": "App", "RecentlyActive": "PT3H"})
	if err != nil {
		t.Fatal(err)
	}

	// Then: it is an RPC v2 request with a CBOR body keyed by member name
	if req.URL.Path != "/service/GraniteServiceVersion20100801/operation/ListMetrics" || req.Header.Get("Smithy-Protocol") != "rpc-v2-cbor" {
		t.Fatalf("request = %s %s %v", req.Method, req.URL.Path, req.Header)
	}
	raw, _ := io.ReadAll(req.Body)
	var body map[string]any
	if err := cborlib.Unmarshal(raw, &body); err != nil || body["Namespace"] != "App" {
		t.Errorf("body = %v (%v)", body, err)
	}
}

func TestBuildRequest_queryFlattening(t *testing.T) {
	cases := []struct {
		name            string
		service, action string
		params          map[string]any
		want            url.Values
	}{
		{
			name: "awsQuery list and map", service: "sns", action: "createTopic",
			params: map[string]any{"Name": "t", "Attributes": map[string]any{"DisplayName": "D"}, "Tags": []any{map[string]any{"Key": "k", "Value": "v"}}},
			want: url.Values{
				"Action": {"CreateTopic"}, "Version": {"2010-03-31"}, "Name": {"t"},
				"Attributes.entry.1.key": {"DisplayName"}, "Attributes.entry.1.value": {"D"},
				"Tags.member.1.Key": {"k"}, "Tags.member.1.Value": {"v"},
			},
		},
		{
			name: "ec2Query lists use the capitalised xmlName and no member segment", service: "ec2", action: "describeVpcs",
			params: map[string]any{"VpcIds": []any{"vpc-1", "vpc-2"}, "Filters": []any{map[string]any{"Name": "cidr", "Values": []any{"10.0.0.0/16"}}}},
			want: url.Values{
				"Action": {"DescribeVpcs"}, "Version": {"2016-11-15"},
				"VpcId.1": {"vpc-1"}, "VpcId.2": {"vpc-2"},
				"Filter.1.Name": {"cidr"}, "Filter.1.Value.1": {"10.0.0.0/16"},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Given: a Query-protocol call
			call := mustResolve(t, tc.service, tc.action)

			// When: its request is built
			req, err := call.buildRequest(context.Background(), tc.params)
			if err != nil {
				t.Fatal(err)
			}

			// Then: the form carries the protocol's member keys
			raw, _ := io.ReadAll(req.Body)
			got, _ := url.ParseQuery(string(raw))
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("form = %v\nwant   %v", got, tc.want)
			}
		})
	}
}

func TestBuildRequest_restBindings(t *testing.T) {
	// Given: an S3 putObject with a greedy key label, headers, metadata and a body
	call := mustResolve(t, "s3", "putObject")

	// When: its request is built
	req, err := call.buildRequest(context.Background(), map[string]any{
		"Bucket": "b", "Key": "a dir/x+y.txt", "Body": map[string]any{"a": 1.0},
		"ContentType": "application/json", "Metadata": map[string]any{"colour": "blue"},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Then: labels, query, headers and payload land where the model binds them
	if req.Method != "PUT" || req.URL.EscapedPath() != "/b/a%20dir/x%2By.txt" || req.URL.RawQuery != "x-id=PutObject" {
		t.Errorf("request = %s %s ? %s", req.Method, req.URL.EscapedPath(), req.URL.RawQuery)
	}
	if req.Header.Get("Content-Type") != "application/json" || req.Header.Get("X-Amz-Meta-Colour") != "blue" {
		t.Errorf("headers = %v", req.Header)
	}
	if raw, _ := io.ReadAll(req.Body); string(raw) != `{"a":1}` {
		t.Errorf("body = %s", raw)
	}
}

func TestBuildRequest_missingLabelIsAnError(t *testing.T) {
	// Given: a Lambda getFunction without its FunctionName label
	call := mustResolve(t, "lambda", "getFunction")

	// When: its request is built
	_, err := call.buildRequest(context.Background(), map[string]any{})

	// Then: the missing parameter is named
	if err == nil || err.Error() != "Parameters.FunctionName is required" {
		t.Errorf("err = %v", err)
	}
}

func TestSDKErrorNames(t *testing.T) {
	prefixes := map[string]string{
		"DynamoDB": "DynamoDb", "EC2": "Ec2", "SFN": "Sfn", "IAM": "Iam", "S3": "S3",
		"CloudWatch Logs": "CloudWatchLogs", "API Gateway": "ApiGateway", "ApiGatewayV2": "ApiGatewayV2",
		"SESv2": "SesV2", "WAFV2": "Wafv2", "Elastic Load Balancing v2": "ElasticLoadBalancingV2",
		"Route 53": "Route53", "Secrets Manager": "SecretsManager", "ElastiCache": "ElastiCache",
	}
	for sdkID, want := range prefixes {
		if got := sdkErrorPrefix(sdkID); got != want {
			t.Errorf("sdkErrorPrefix(%q) = %q, want %q", sdkID, got, want)
		}
	}
	exceptions := map[string]string{
		"NoSuchKey": "NoSuchKeyException", "ResourceNotFoundException": "ResourceNotFoundException",
		"DBInstanceNotFoundFault": "DbInstanceNotFoundException", "KMSInvalidStateException": "KmsInvalidStateException",
		"EC2ThrottledException": "Ec2ThrottledException", "QueueDoesNotExist": "QueueDoesNotExistException",
	}
	for shape, want := range exceptions {
		if got := sdkExceptionName(shape); got != want {
			t.Errorf("sdkExceptionName(%q) = %q, want %q", shape, got, want)
		}
	}
}

func TestJavaInstant(t *testing.T) {
	cases := map[time.Time]string{
		time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC):           "2026-01-02T03:04:05Z",
		time.Date(2026, 1, 2, 3, 4, 5, 120_000_000, time.UTC): "2026-01-02T03:04:05.120Z",
		time.Date(2026, 1, 2, 3, 4, 5, 123_456_000, time.UTC): "2026-01-02T03:04:05.123456Z",
	}
	for in, want := range cases {
		if got := javaInstant(in); got != want {
			t.Errorf("javaInstant(%v) = %q, want %q", in, got, want)
		}
	}
}

func TestFindSDKOperation_requiresCamelCaseAction(t *testing.T) {
	if _, ok := findSDKOperation("s3", "getObject"); !ok {
		t.Error("s3:getObject not found")
	}
	if _, ok := findSDKOperation("s3", "GetObject"); ok {
		t.Error("s3:GetObject resolved; AWS rejects the PascalCase spelling")
	}
}
