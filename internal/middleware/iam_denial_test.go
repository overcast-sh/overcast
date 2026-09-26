package middleware

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws/protocol/ec2query"
	"github.com/aws/aws-sdk-go-v2/aws/protocol/restjson"
	awsxml "github.com/aws/aws-sdk-go-v2/aws/protocol/xml"
	cborlib "github.com/fxamacker/cbor/v2"
	"go.uber.org/zap"
)

// These tests pin the envelope an IAM denial is written in, one per wire
// protocol family, and read each back with the decoder the AWS SDK for Go v2
// uses for that protocol: a denial an SDK cannot decode reaches the caller as
// "deserialization failed" instead of an access-denied error (#2259).

// denyUnsigned sends r unsigned through IAMEnforce, which refuses it before
// any policy is read, and returns the refusal.
func denyUnsigned(t *testing.T, r *http.Request, queries QueryRouter) *httptest.ResponseRecorder {
	t.Helper()
	served := false
	h := IAMEnforce(true, nil, zap.NewNop(), queries)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		served = true
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	if served {
		t.Fatal("an unsigned request was served; want it denied")
	}
	return rec
}

// jsonTargetRequest is an unsigned awsJson call to target.
func jsonTargetRequest(contentType, target string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{}`))
	r.Header.Set("Content-Type", contentType)
	r.Header.Set("X-Amz-Target", target)
	return r
}

// assertStatus fails t unless rec answered want.
func assertStatus(t *testing.T, rec *httptest.ResponseRecorder, want int) {
	t.Helper()
	if rec.Code != want {
		t.Fatalf("status = %d, want %d; body %q", rec.Code, want, rec.Body.String())
	}
}

func TestIAMDenial_awsJSON(t *testing.T) {
	// Athena, Glue, KMS and Step Functions were denied in Query XML before
	// #2259; DynamoDB and SQS were not. SQS answers Query too (below).
	cases := []struct{ name, contentType, target string }{
		{"athena", "application/x-amz-json-1.1", "AmazonAthena.StartQueryExecution"},
		{"glue", "application/x-amz-json-1.1", "AWSGlue.GetDatabases"},
		{"kms", "application/x-amz-json-1.1", "TrentService.Encrypt"},
		{"stepfunctions", "application/x-amz-json-1.0", "AWSStepFunctions.StartExecution"},
		{"dynamodb", "application/x-amz-json-1.0", "DynamoDB_20120810.PutItem"},
		{"sqs", "application/x-amz-json-1.0", "AmazonSQS.ListQueues"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Given: an awsJson call
			r := jsonTargetRequest(tc.contentType, tc.target)

			// When: IAM denies it
			rec := denyUnsigned(t, r, nil)

			// Then: it is a 400 AccessDeniedException in the JSON envelope
			assertStatus(t, rec, http.StatusBadRequest)
			code, _, err := restjson.GetErrorInfo(json.NewDecoder(rec.Body))
			if err != nil || code != "AccessDeniedException" {
				t.Fatalf("error code = %q (%v), want AccessDeniedException", code, err)
			}
		})
	}
}

func TestIAMDenial_awsQuery(t *testing.T) {
	// CloudWatch's and SQS's canonical protocols are not awsQuery, but a
	// Query call to either still reads a Query error.
	cases := []struct{ service, form, action string }{
		{"iam", "Action=ListUsers&Version=2010-05-08", "ListUsers"},
		{"cloudwatch", "Action=ListMetrics&Version=2010-08-01", "ListMetrics"},
		{"sqs", "Action=ListQueues&Version=2012-11-05", "ListQueues"},
	}
	for _, tc := range cases {
		t.Run(tc.service, func(t *testing.T) {
			// Given: a call the router serves as Query
			r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(tc.form))
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			if err := r.ParseForm(); err != nil {
				t.Fatal(err)
			}
			queries := stubQueryRouter{route: QueryRoute{Service: tc.service, Action: tc.action}, isQuery: true}

			// When: IAM denies it
			rec := denyUnsigned(t, r, queries)

			// Then: it is a 403 AccessDenied wrapped in <ErrorResponse>
			assertStatus(t, rec, http.StatusForbidden)
			got, err := awsxml.GetErrorResponseComponents(rec.Body, false)
			if err != nil || got.Code != "AccessDenied" {
				t.Fatalf("error code = %q (%v), want AccessDenied", got.Code, err)
			}
		})
	}
}

func TestIAMDenial_awsQueryNoRouterResolved(t *testing.T) {
	// Given: an SNS Query call, and no QueryRouter to resolve it
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("Action=ListTopics&Version=2010-03-31"))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	// When: IAM denies it
	rec := denyUnsigned(t, r, nil)

	// Then: its modeled Action still makes it a Query denial
	assertStatus(t, rec, http.StatusForbidden)
	got, err := awsxml.GetErrorResponseComponents(rec.Body, false)
	if err != nil || got.Code != "AccessDenied" {
		t.Fatalf("error code = %q (%v), want AccessDenied", got.Code, err)
	}
}

func TestIAMDenial_ec2Query(t *testing.T) {
	// Given: an EC2 DescribeInstances call the router serves as Query
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("Action=DescribeInstances&Version=2016-11-15"))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if err := r.ParseForm(); err != nil {
		t.Fatal(err)
	}
	queries := stubQueryRouter{route: QueryRoute{Service: "ec2", Action: "DescribeInstances"}, isQuery: true}

	// When: IAM denies it
	rec := denyUnsigned(t, r, queries)

	// Then: it is EC2's 403 UnauthorizedOperation in <Response><Errors>
	assertStatus(t, rec, http.StatusForbidden)
	got, err := ec2query.GetErrorResponseComponents(rec.Body)
	if err != nil || got.Code != "UnauthorizedOperation" {
		t.Fatalf("error code = %q (%v), want UnauthorizedOperation", got.Code, err)
	}
}

func TestIAMDenial_restXML(t *testing.T) {
	cases := []struct {
		name, path      string
		noErrorWrapping bool
	}{
		// S3 is modeled with noErrorWrapping: a bare <Error>.
		{"s3", "/bucket/key", true},
		// CloudFront and Route 53 wrap theirs in <ErrorResponse>.
		{"cloudfront", "/2020-05-31/distribution", false},
		{"route53", "/2013-04-01/hostedzone", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Given: a REST-XML call
			r := httptest.NewRequest(http.MethodGet, tc.path, nil)

			// When: IAM denies it
			rec := denyUnsigned(t, r, nil)

			// Then: it is a 403 AccessDenied in the service's XML envelope
			assertStatus(t, rec, http.StatusForbidden)
			got, err := awsxml.GetErrorResponseComponents(rec.Body, tc.noErrorWrapping)
			if err != nil || got.Code != "AccessDenied" {
				t.Fatalf("error code = %q (%v), want AccessDenied", got.Code, err)
			}
		})
	}
}

func TestIAMDenial_restJSON(t *testing.T) {
	// SES v2 was denied in Query XML before #2259, for sharing SES v1's key.
	cases := []struct{ name, path string }{
		{"lambda", "/2015-03-31/functions/"},
		{"sesv2", "/v2/email/identities"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Given: a REST-JSON call
			r := httptest.NewRequest(http.MethodGet, tc.path, nil)

			// When: IAM denies it
			rec := denyUnsigned(t, r, nil)

			// Then: it is a 403 AccessDeniedException in the JSON envelope
			assertStatus(t, rec, http.StatusForbidden)
			code, _, err := restjson.GetErrorInfo(json.NewDecoder(rec.Body))
			if err != nil || code != "AccessDeniedException" {
				t.Fatalf("error code = %q (%v), want AccessDeniedException", code, err)
			}
		})
	}
}

func TestIAMDenial_rpcV2(t *testing.T) {
	// The models bind CloudWatch's shape to CBOR only; over JSON the router
	// resolves the label as a service key.
	cases := []struct {
		marker, contentType, label string
		decode                     func([]byte, any) error
	}{
		{"rpc-v2-cbor", "application/cbor", "GraniteServiceVersion20100801", cborlib.Unmarshal},
		{"rpc-v2-json", "application/json", "cloudwatch", json.Unmarshal},
	}
	for _, tc := range cases {
		t.Run(tc.marker, func(t *testing.T) {
			// Given: a CloudWatch ListMetrics call over Smithy RPC v2
			r := httptest.NewRequest(http.MethodPost, "/service/"+tc.label+"/operation/ListMetrics", nil)
			r.Header.Set("Smithy-Protocol", tc.marker)
			r.Header.Set("Content-Type", tc.contentType)

			// When: IAM denies it
			rec := denyUnsigned(t, r, nil)

			// Then: it is a 400 AccessDeniedException in that protocol's envelope
			assertStatus(t, rec, http.StatusBadRequest)
			var body map[string]string
			if err := tc.decode(rec.Body.Bytes(), &body); err != nil || body["__type"] != "AccessDeniedException" {
				t.Fatalf("error = %v (%v), want __type AccessDeniedException", body, err)
			}
		})
	}
}
