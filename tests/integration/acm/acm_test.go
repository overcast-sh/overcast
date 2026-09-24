// Package acm_test contains integration tests for the ACM emulator.
//
// Run: go test ./tests/integration/acm/...
package acm_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/overcast-sh/overcast/tests/helpers"
)

// acmCall performs an ACM JSON 1.1 dispatch request.
func acmCall(t *testing.T, srv *helpers.TestServer, operation string, body map[string]any) *http.Response {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal %s body: %v", operation, err)
	}
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/x-amz-json-1.1")
	req.Header.Set("X-Amz-Target", "CertificateManager."+operation)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("acmCall %s: %v", operation, err)
	}
	return resp
}

func requestCert(t *testing.T, srv *helpers.TestServer, domain string) string {
	t.Helper()
	resp := acmCall(t, srv, "RequestCertificate", map[string]any{
		"DomainName": domain,
	})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		CertificateArn string `json:"CertificateArn"`
	}
	helpers.DecodeJSON(t, resp, &result)
	if result.CertificateArn == "" {
		t.Fatal("expected CertificateArn to be set")
	}
	return result.CertificateArn
}

// ─── RequestCertificate ───────────────────────────────────────────────────────

func TestRequestCertificate_success(t *testing.T) {
	// Given: an empty store
	srv := helpers.NewTestServer(t)

	// When: RequestCertificate is called
	arn := requestCert(t, srv, "example.com")

	// Then: a valid ARN is returned
	if arn == "" {
		t.Error("expected CertificateArn to be non-empty")
	}
}

// TestRequestCertificate_emptyDomainNameAnswersValidationException pins that
// a missing DomainName is AWS's front-end "required member absent" rejection
// (ValidationException, "Member must not be null") rather than the service's
// own InvalidParameterException — the wrong exception #2030 reported this
// handler answering before this fix. AWS gives the same treatment to any
// other required member absent from the request; DomainName is
// RequestCertificate's only required one.
func TestRequestCertificate_emptyDomainNameAnswersValidationException(t *testing.T) {
	// Given: an empty store
	srv := helpers.NewTestServer(t)

	// When: RequestCertificate is called with no DomainName at all
	resp := acmCall(t, srv, "RequestCertificate", map[string]any{})
	defer resp.Body.Close()

	// Then: AWS's ValidationException for a missing required member, not
	// ACM's own InvalidParameterException
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "ValidationException")
}

// ─── DescribeCertificate ──────────────────────────────────────────────────────

func TestDescribeCertificate_success(t *testing.T) {
	// Given: a certificate exists
	srv := helpers.NewTestServer(t)
	arn := requestCert(t, srv, "example.com")

	// When: DescribeCertificate is called
	resp := acmCall(t, srv, "DescribeCertificate", map[string]any{
		"CertificateArn": arn,
	})
	defer resp.Body.Close()

	// Then: 200 with Status "ISSUED"
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		Certificate struct {
			Status string `json:"Status"`
		} `json:"Certificate"`
	}
	helpers.DecodeJSON(t, resp, &result)
	if result.Certificate.Status != "ISSUED" {
		t.Errorf("expected Status=ISSUED, got %q", result.Certificate.Status)
	}
}

// AWS documents ResourceNotFoundException as HTTP 400 for DescribeCertificate
// (https://docs.aws.amazon.com/acm/latest/APIReference/API_DescribeCertificate.html#API_DescribeCertificate_Errors),
// not 404 — see https://github.com/overcast-sh/overcast/issues/1729.
func TestDescribeCertificate_notFound(t *testing.T) {
	// Given: an empty store
	srv := helpers.NewTestServer(t)
	missing := "arn:aws:acm:us-east-1:000000000000:certificate/00000000-0000-0000-0000-000000000000"

	// When: DescribeCertificate is called for an ARN that does not exist
	resp := acmCall(t, srv, "DescribeCertificate", map[string]any{
		"CertificateArn": missing,
	})
	defer resp.Body.Close()

	// Then: ResourceNotFoundException, HTTP 400 (not 404)
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "ResourceNotFoundException")
}

// ─── ListCertificates ─────────────────────────────────────────────────────────

func TestListCertificates_success(t *testing.T) {
	// Given: a certificate exists
	srv := helpers.NewTestServer(t)
	requestCert(t, srv, "example.com")

	// When: ListCertificates is called
	resp := acmCall(t, srv, "ListCertificates", map[string]any{})
	defer resp.Body.Close()

	// Then: 200 with the certificate listed
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		CertificateSummaryList []struct {
			CertificateArn string `json:"CertificateArn"`
			DomainName     string `json:"DomainName"`
		} `json:"CertificateSummaryList"`
	}
	helpers.DecodeJSON(t, resp, &result)
	if len(result.CertificateSummaryList) < 1 {
		t.Error("expected at least 1 certificate in list")
	}
}

// ─── DeleteCertificate ────────────────────────────────────────────────────────

func TestDeleteCertificate_success(t *testing.T) {
	// Given: a certificate exists
	srv := helpers.NewTestServer(t)
	arn := requestCert(t, srv, "example.com")

	// When: DeleteCertificate is called
	del := acmCall(t, srv, "DeleteCertificate", map[string]any{
		"CertificateArn": arn,
	})
	defer del.Body.Close()
	helpers.AssertStatus(t, del, http.StatusOK)

	// Then: ListCertificates returns empty
	resp := acmCall(t, srv, "ListCertificates", map[string]any{})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		CertificateSummaryList []any `json:"CertificateSummaryList"`
	}
	helpers.DecodeJSON(t, resp, &result)
	if len(result.CertificateSummaryList) != 0 {
		t.Errorf("expected 0 certificates after delete, got %d", len(result.CertificateSummaryList))
	}
}

// AWS documents ResourceNotFoundException as HTTP 400 for DeleteCertificate
// (https://docs.aws.amazon.com/acm/latest/APIReference/API_DeleteCertificate.html#API_DeleteCertificate_Errors),
// not 404 — see https://github.com/overcast-sh/overcast/issues/1729.
func TestDeleteCertificate_notFound(t *testing.T) {
	// Given: an empty store
	srv := helpers.NewTestServer(t)
	missing := "arn:aws:acm:us-east-1:000000000000:certificate/00000000-0000-0000-0000-000000000000"

	// When: DeleteCertificate is called for an ARN that does not exist
	resp := acmCall(t, srv, "DeleteCertificate", map[string]any{
		"CertificateArn": missing,
	})
	defer resp.Body.Close()

	// Then: ResourceNotFoundException, HTTP 400 (not 404)
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "ResourceNotFoundException")
}
