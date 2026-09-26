package serviceutil_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws/protocol/ec2query"
	"github.com/aws/aws-sdk-go-v2/aws/protocol/restjson"
	awsxml "github.com/aws/aws-sdk-go-v2/aws/protocol/xml"
	cborlib "github.com/fxamacker/cbor/v2"

	"github.com/overcast-sh/overcast/internal/awsapi"
	"github.com/overcast-sh/overcast/internal/protocol"
	"github.com/overcast-sh/overcast/internal/serviceutil"
)

// These tests read every envelope WriteError writes back with the decoder the
// AWS SDK for Go v2 uses for that protocol. An envelope the SDK cannot decode
// reaches the caller as a deserialization failure, or as an error with no
// code, instead of the error Overcast meant (#2265).

// sdkErrorCode decodes body the way the SDK decodes profile's errors and
// returns the error code it reads.
func sdkErrorCode(t *testing.T, profile awsapi.ErrorProfile, body []byte) string {
	t.Helper()
	switch profile {
	case awsapi.ErrorProfileJSON:
		code, _, err := restjson.GetErrorInfo(json.NewDecoder(bytes.NewReader(body)))
		if err != nil {
			t.Fatalf("restjson.GetErrorInfo: %v; body %s", err, body)
		}
		return code
	case awsapi.ErrorProfileQueryXML, awsapi.ErrorProfileRESTXML:
		got, err := awsxml.GetErrorResponseComponents(bytes.NewReader(body), false)
		if err != nil {
			t.Fatalf("wrapped xml decoder: %v; body %s", err, body)
		}
		return got.Code
	case awsapi.ErrorProfileBareXML:
		got, err := awsxml.GetErrorResponseComponents(bytes.NewReader(body), true)
		if err != nil {
			t.Fatalf("noErrorWrapping xml decoder: %v; body %s", err, body)
		}
		return got.Code
	case awsapi.ErrorProfileEC2QueryXML:
		got, err := ec2query.GetErrorResponseComponents(bytes.NewReader(body))
		if err != nil {
			t.Fatalf("ec2query decoder: %v; body %s", err, body)
		}
		return got.Code
	case awsapi.ErrorProfileRPCV2CBOR:
		var decoded map[string]string
		if err := cborlib.Unmarshal(body, &decoded); err != nil {
			t.Fatalf("cbor: %v", err)
		}
		return decoded["__type"]
	case awsapi.ErrorProfileRPCV2JSON:
		var decoded map[string]string
		if err := json.Unmarshal(body, &decoded); err != nil {
			t.Fatalf("json: %v; body %s", err, body)
		}
		return decoded["__type"]
	}
	t.Fatalf("no SDK decoder for profile %v", profile)
	return ""
}

var everyErrorProfile = []awsapi.ErrorProfile{
	awsapi.ErrorProfileJSON,
	awsapi.ErrorProfileQueryXML,
	awsapi.ErrorProfileEC2QueryXML,
	awsapi.ErrorProfileBareXML,
	awsapi.ErrorProfileRPCV2CBOR,
	awsapi.ErrorProfileRPCV2JSON,
	awsapi.ErrorProfileRESTXML,
}

func TestWriteError_sdkReadsTheCodeInEveryProfile(t *testing.T) {
	aerr := &protocol.AWSError{Code: "InvalidSignatureException", Message: "bad", HTTPStatus: http.StatusForbidden}
	for _, profile := range everyErrorProfile {
		// Given: an error, and a caller that reads profile's envelope
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		w := httptest.NewRecorder()

		// When: it is written in that profile
		serviceutil.WriteError(w, r, profile, aerr)

		// Then: the SDK's own decoder reads its code, at its status
		if w.Code != http.StatusForbidden {
			t.Errorf("profile %v: status = %d, want 403", profile, w.Code)
		}
		if got := sdkErrorCode(t, profile, w.Body.Bytes()); got != aerr.Code {
			t.Errorf("profile %v: SDK read code %q, want %q; body %s", profile, got, aerr.Code, w.Body.String())
		}
	}
}

// TestWriteNotImplemented_restXMLFollowsNoErrorWrapping is the 501 half of
// #2265: CloudFront and Route 53 claims get the wrapped envelope
// their SDKs decode, where they used to get S3's bare <Error>.
func TestWriteNotImplemented_restXMLFollowsNoErrorWrapping(t *testing.T) {
	registry := awsapi.NewRegistry()
	for _, path := range []string{"/2020-05-31/distribution", "/2013-04-01/hostedzone"} {
		// Given: a modeled CloudFront or Route 53 binding
		claim, ok := registry.ClaimREST(http.MethodGet, path)
		if !ok {
			t.Fatalf("ClaimREST(GET %s) matched nothing", path)
		}
		r := httptest.NewRequest(http.MethodGet, path, nil)
		w := httptest.NewRecorder()

		// When: nothing implements it
		serviceutil.WriteNotImplemented(w, r, claim)

		// Then: the SDK's wrapped rest-xml decoder reads NotImplemented
		if w.Code != http.StatusNotImplemented || w.Header().Get("x-emulator-unsupported") != "true" {
			t.Errorf("%s: status %d, x-emulator-unsupported %q; want 501 and true", path, w.Code, w.Header().Get("x-emulator-unsupported"))
		}
		got, err := awsxml.GetErrorResponseComponents(bytes.NewReader(w.Body.Bytes()), false)
		if err != nil || got.Code != "NotImplemented" {
			t.Errorf("%s: wrapped decoder read %q (%v), want NotImplemented; body %s", path, got.Code, err, w.Body.String())
		}
	}
}
