package middleware

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	awsxml "github.com/aws/aws-sdk-go-v2/aws/protocol/xml"

	"github.com/overcast-sh/overcast/internal/protocol"
)

// These tests pin the edges of writeUnroutedError's classification that the
// per-protocol SigV4 tests do not reach (#2265).

func TestNotReady_Route53ReadsTheWrappedEnvelope(t *testing.T) {
	// Given: a store still migrating, and a Route 53 call
	next, _ := passThroughHandler()
	handler := NotReady(&fakeNotReadyStore{notReady: true})(next)
	r := httptest.NewRequest(http.MethodGet, "/2013-04-01/hostedzone", nil)
	rec := httptest.NewRecorder()

	// When: NotReady refuses it
	handler.ServeHTTP(rec, r)

	// Then: the SDK's wrapped rest-xml decoder reads the code
	assertStatus(t, rec, http.StatusServiceUnavailable)
	got, err := awsxml.GetErrorResponseComponents(rec.Body, false)
	if err != nil || got.Code != protocol.ErrStorageMigrating.Code {
		t.Fatalf("error code = %q (%v), want %s", got.Code, err, protocol.ErrStorageMigrating.Code)
	}
}

func TestSigV4Error_unboundPathFollowsTheCredentialScope(t *testing.T) {
	// Given: a Route 53-signed GET on a path no Route 53 binding declares,
	// which only MediaStore Data's root catch-all "/{Path+}" matches
	r := httptest.NewRequest(http.MethodGet, "/2013-04-01/no-such-resource", nil)

	// When: SigV4 rejects it
	rec := rejectSkewed(t, r, "route53")

	// Then: the catch-all does not make it a MediaStore JSON error; the
	// caller's scope names Route 53, whose SDK reads <ErrorResponse>
	got, err := awsxml.GetErrorResponseComponents(rec.Body, false)
	if err != nil || got.Code != sigV4ErrorCode {
		t.Fatalf("error code = %q (%v), want %s; body %s", got.Code, err, sigV4ErrorCode, rec.Body.String())
	}
}

func TestSigV4Error_formEncodedPutIsNotQuery(t *testing.T) {
	// Given: an S3 PutObject sent with curl's default form content type,
	// whose object body happens to look like a Query form
	r := httptest.NewRequest(http.MethodPut, "/bucket/key", strings.NewReader("Action=DescribeInstances&Version=2016-11-15"))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	// When: SigV4 rejects it
	rec := rejectSkewed(t, r, "s3")

	// Then: it is S3's bare <Error>, not a Query or EC2 envelope
	got, err := awsxml.GetErrorResponseComponents(bytes.NewReader(rec.Body.Bytes()), true)
	if err != nil || got.Code != sigV4ErrorCode {
		t.Fatalf("error code = %q (%v), want %s; body %s", got.Code, err, sigV4ErrorCode, rec.Body.String())
	}
}
