package middleware

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws/protocol/ec2query"
	"github.com/aws/aws-sdk-go-v2/aws/protocol/restjson"
	awsxml "github.com/aws/aws-sdk-go-v2/aws/protocol/xml"
	cborlib "github.com/fxamacker/cbor/v2"
	"go.uber.org/zap"

	"github.com/overcast-sh/overcast/internal/clock"
)

// These tests pin the envelope a SigV4 rejection is written in, one per wire
// protocol family, and read each back with the AWS SDK for Go v2's own
// decoder. SigV4 used to choose the envelope from a hand-kept list of service
// names, which sent SES v2 and CloudWatch-over-CBOR callers Query XML and
// Route 53 callers JSON (#2265).

const sigV4ErrorCode = "InvalidSignatureException"

// rejectSkewed signs r for scope five minutes outside the allowed clock skew,
// sends it through SigV4 with validation on, and returns the rejection.
func rejectSkewed(t *testing.T, r *http.Request, scope string) *httptest.ResponseRecorder {
	t.Helper()
	clk := clock.NewMock()
	now := time.Date(2026, 4, 21, 12, 0, 0, 0, time.UTC)
	clk.Set(now)
	h := SigV4(true, nil, zap.NewNop(), clk)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("a skewed request was served; want it rejected")
	}))
	r.Host = "localhost:4566"
	signHeaderRequest(t, r, now.Add(-10*time.Minute), "us-east-1", scope, []string{"host"})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	assertStatus(t, rec, http.StatusForbidden)
	return rec
}

func TestSigV4Error_awsJSON(t *testing.T) {
	// Given: an awsJson call with a skewed signature
	r := jsonTargetRequest("application/x-amz-json-1.1", "AmazonAthena.StartQueryExecution")

	// When: SigV4 rejects it
	rec := rejectSkewed(t, r, "athena")

	// Then: the SDK's JSON decoder reads the code
	code, _, err := restjson.GetErrorInfo(json.NewDecoder(rec.Body))
	if err != nil || code != sigV4ErrorCode {
		t.Fatalf("error code = %q (%v), want %s", code, err, sigV4ErrorCode)
	}
}

func TestSigV4Error_awsQuery(t *testing.T) {
	// The form is in the body, which SigV4 reads before routing has parsed
	// it. CloudWatch's and SQS's canonical protocols are not awsQuery.
	cases := []struct{ scope, form string }{
		{"sns", "Action=ListTopics&Version=2010-03-31"},
		{"monitoring", "Action=ListMetrics&Version=2010-08-01"},
		{"sqs", "Action=ListQueues&Version=2012-11-05"},
	}
	for _, tc := range cases {
		t.Run(tc.scope, func(t *testing.T) {
			// Given: a Query call with a skewed signature
			r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(tc.form))
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")

			// When: SigV4 rejects it
			rec := rejectSkewed(t, r, tc.scope)

			// Then: the SDK's Query decoder reads the code from <ErrorResponse>
			got, err := awsxml.GetErrorResponseComponents(rec.Body, false)
			if err != nil || got.Code != sigV4ErrorCode {
				t.Fatalf("error code = %q (%v), want %s", got.Code, err, sigV4ErrorCode)
			}
		})
	}
}

func TestSigV4Error_ec2Query(t *testing.T) {
	// Given: an EC2 call with a skewed signature
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("Action=DescribeInstances&Version=2016-11-15"))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	// When: SigV4 rejects it
	rec := rejectSkewed(t, r, "ec2")

	// Then: the SDK's EC2 decoder reads the code from <Response><Errors>
	got, err := ec2query.GetErrorResponseComponents(rec.Body)
	if err != nil || got.Code != sigV4ErrorCode {
		t.Fatalf("error code = %q (%v), want %s", got.Code, err, sigV4ErrorCode)
	}
}

func TestSigV4Error_restXML(t *testing.T) {
	cases := []struct {
		scope, path     string
		noErrorWrapping bool
	}{
		// S3 is modeled with noErrorWrapping: a bare <Error>.
		{"s3", "/bucket/key", true},
		// CloudFront and Route 53 are not: <ErrorResponse>.
		{"cloudfront", "/2020-05-31/distribution", false},
		{"route53", "/2013-04-01/hostedzone", false},
	}
	for _, tc := range cases {
		t.Run(tc.scope, func(t *testing.T) {
			// Given: a REST-XML call with a skewed signature
			r := httptest.NewRequest(http.MethodGet, tc.path, nil)

			// When: SigV4 rejects it
			rec := rejectSkewed(t, r, tc.scope)

			// Then: the SDK's decoder for that service's envelope reads the code
			got, err := awsxml.GetErrorResponseComponents(rec.Body, tc.noErrorWrapping)
			if err != nil || got.Code != sigV4ErrorCode {
				t.Fatalf("error code = %q (%v), want %s", got.Code, err, sigV4ErrorCode)
			}
		})
	}
}

func TestSigV4Error_restJSON(t *testing.T) {
	// Given: an SES v2 call with a skewed signature, which the old list sent
	// Query XML for sharing SES v1's key
	r := httptest.NewRequest(http.MethodGet, "/v2/email/identities", nil)

	// When: SigV4 rejects it
	rec := rejectSkewed(t, r, "ses")

	// Then: the SDK's JSON decoder reads the code
	code, _, err := restjson.GetErrorInfo(json.NewDecoder(rec.Body))
	if err != nil || code != sigV4ErrorCode {
		t.Fatalf("error code = %q (%v), want %s", code, err, sigV4ErrorCode)
	}
}

func TestSigV4Error_rpcV2CBOR(t *testing.T) {
	// Given: a CloudWatch call over Smithy RPC v2 CBOR with a skewed signature
	r := httptest.NewRequest(http.MethodPost, "/service/GraniteServiceVersion20100801/operation/ListMetrics", nil)
	r.Header.Set("Smithy-Protocol", "rpc-v2-cbor")
	r.Header.Set("Content-Type", "application/cbor")

	// When: SigV4 rejects it
	rec := rejectSkewed(t, r, "monitoring")

	// Then: it is a CBOR error, not the Query XML the old list gave CloudWatch
	var body map[string]string
	if err := cborlib.Unmarshal(rec.Body.Bytes(), &body); err != nil || body["__type"] != sigV4ErrorCode {
		t.Fatalf("error = %v (%v), want __type %s", body, err, sigV4ErrorCode)
	}
}
