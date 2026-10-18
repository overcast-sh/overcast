package serviceutil

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestTakeCloudFrontSource_trustedStamp(t *testing.T) {
	// Given: a request stamped by this process
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	SetCloudFrontSource(r.Header, "arn:aws:cloudfront::000000000000:distribution/E1")

	// When: the receiver takes the identity
	arn, ok := TakeCloudFrontSource(r)

	// Then: the ARN is returned and both headers are gone
	if !ok || arn != "arn:aws:cloudfront::000000000000:distribution/E1" {
		t.Fatalf("got (%q, %v)", arn, ok)
	}
	if r.Header.Get(CloudFrontSourceARNHeader) != "" || r.Header.Get(InternalTrustHeader) != "" {
		t.Fatalf("headers not stripped: %v", r.Header)
	}
}

func TestTakeCloudFrontSource_spoofedByClient(t *testing.T) {
	// Given: an external request claiming a distribution with a guessed token
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set(CloudFrontSourceARNHeader, "arn:aws:cloudfront::000000000000:distribution/E1")
	r.Header.Set(InternalTrustHeader, "guess")

	// When: the receiver takes the identity
	arn, ok := TakeCloudFrontSource(r)

	// Then: the claim is rejected and the headers are still removed
	if ok || arn != "" {
		t.Fatalf("spoof accepted: (%q, %v)", arn, ok)
	}
	if r.Header.Get(CloudFrontSourceARNHeader) != "" || r.Header.Get(InternalTrustHeader) != "" {
		t.Fatalf("headers not stripped: %v", r.Header)
	}
}

func TestTakeCloudFrontSource_absent(t *testing.T) {
	// Given: a request with no identity
	r := httptest.NewRequest(http.MethodGet, "/", nil)

	// When / Then: nothing is reported
	if arn, ok := TakeCloudFrontSource(r); ok || arn != "" {
		t.Fatalf("got (%q, %v)", arn, ok)
	}
}
