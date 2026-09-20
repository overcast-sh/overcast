package acm_test

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/overcast-sh/overcast/tests/helpers"
)

// assertACMError reads the whole error body once and checks both the code and
// a fragment of the message. helpers.AssertJSONError consumes the body, so a
// test that wants both has to do its own decode.
func assertACMError(t *testing.T, resp *http.Response, code, messageFragment string) {
	t.Helper()
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read error body: %v", err)
	}
	var errResp struct {
		Type    string `json:"__type"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(raw, &errResp); err != nil {
		t.Fatalf("decode error body: %v\nbody: %s", err, raw)
	}
	if errResp.Type != code {
		t.Errorf("expected error code %q, got %q (message: %s)", code, errResp.Type, errResp.Message)
	}
	if !strings.Contains(errResp.Message, messageFragment) {
		t.Errorf("expected message to contain %q, got %q", messageFragment, errResp.Message)
	}
}

// ─── RequestCertificate — DomainName validation ──────────────────────────────

// TestRequestCertificate_malformedDomainName covers the DomainNameString
// pattern AWS pins on every domain member
// (^(\*\.)?(((?!-)[A-Za-z0-9-]{0,62}[A-Za-z0-9])\.)+((?!-)[A-Za-z0-9-]{1,62}[A-Za-z0-9])$,
// acm-2015-12-08.json). Overcast used to accept anything non-empty, so a
// typo'd domain produced a certificate AWS would have refused (#1994).
func TestRequestCertificate_malformedDomainName(t *testing.T) {
	// Given: an empty store
	srv := helpers.NewTestServer(t)

	cases := map[string]string{
		"no dot":                    "bad_domain",
		"underscore":                "bad_domain.com",
		"leading hyphen":            "-leading.example.com",
		"trailing hyphen in label":  "trailing-.example.com",
		"wildcard not leftmost":     "www.*.example.com",
		"bare wildcard":             "*",
		"wildcard mid-label":        "*example.com",
		"empty label":               "example..com",
		"trailing dot":              "example.com.",
		"single character TLD":      "example.c",
		"space":                     "exa mple.com",
		"label longer than 63 char": strings.Repeat("a", 64) + ".com",
	}

	for name, domain := range cases {
		t.Run(name, func(t *testing.T) {
			// When: RequestCertificate is called with that DomainName
			resp := acmCall(t, srv, "RequestCertificate", map[string]any{"DomainName": domain})

			// Then: the front-end constraint validator's ValidationException,
			// naming the member and the pattern it failed
			helpers.AssertStatus(t, resp, http.StatusBadRequest)
			assertACMError(t, resp, "ValidationException",
				"failed to satisfy constraint: Member must satisfy regular expression pattern:")
		})
	}
}

// TestRequestCertificate_wellFormedDomainNamesAccepted pins the other side of
// the pattern: a leftmost wildcard, a deep subdomain and a 63-character label
// are all legal and must keep working.
func TestRequestCertificate_wellFormedDomainNamesAccepted(t *testing.T) {
	// Given: an empty store
	srv := helpers.NewTestServer(t)

	for _, domain := range []string{
		"example.com",
		"*.example.com",
		"a.b.c.example.co.uk",
		"xn--80ak6aa92e.com",
		strings.Repeat("a", 63) + ".example.com",
		"1.example.com",
	} {
		t.Run(domain, func(t *testing.T) {
			// When: RequestCertificate is called
			resp := acmCall(t, srv, "RequestCertificate", map[string]any{"DomainName": domain})
			defer resp.Body.Close()

			// Then: it is issued
			helpers.AssertStatus(t, resp, http.StatusOK)
		})
	}
}

// TestRequestCertificate_domainNameTooLong covers DomainNameString's length
// trait (1..253), which AWS reports separately from the pattern.
func TestRequestCertificate_domainNameTooLong(t *testing.T) {
	// Given: a syntactically valid domain of 254 characters
	srv := helpers.NewTestServer(t)
	// Five 49-character labels with their dots are 250 characters; a
	// four-character final label takes it one past the 253 maximum.
	long := strings.Repeat(strings.Repeat("a", 49)+".", 5) + "acom"
	if len(long) != 254 {
		t.Fatalf("test setup: domain is %d characters, want 254", len(long))
	}

	// When: RequestCertificate is called with it
	resp := acmCall(t, srv, "RequestCertificate", map[string]any{"DomainName": long})

	// Then: the length constraint is reported, not the pattern
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	assertACMError(t, resp, "ValidationException",
		"Member must have length less than or equal to 253")
}

// TestRequestCertificate_malformedSubjectAlternativeName pins that SANs carry
// the same DomainNameString constraint as DomainName (DomainList's member
// targets it), and that the message names the offending list entry.
func TestRequestCertificate_malformedSubjectAlternativeName(t *testing.T) {
	// Given: a valid DomainName and one malformed SAN
	srv := helpers.NewTestServer(t)

	// When: RequestCertificate is called
	resp := acmCall(t, srv, "RequestCertificate", map[string]any{
		"DomainName":              "example.com",
		"SubjectAlternativeNames": []string{"www.example.com", "bad_domain"},
	})

	// Then: the second entry is named in the constraint violation
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	assertACMError(t, resp, "ValidationException", "at 'subjectAlternativeNames.2.member'")
}

// TestRequestCertificate_rejectedDomainStrandsNothing pins that a rejected
// request leaves no certificate behind, the same ordering the inline-tag
// rejection already keeps (#1052).
func TestRequestCertificate_rejectedDomainStrandsNothing(t *testing.T) {
	// Given: an empty store
	srv := helpers.NewTestServer(t)

	// When: a malformed request is rejected
	bad := acmCall(t, srv, "RequestCertificate", map[string]any{"DomainName": "bad_domain"})
	bad.Body.Close()

	// Then: nothing was stored
	resp := acmCall(t, srv, "ListCertificates", map[string]any{})
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		CertificateSummaryList []any `json:"CertificateSummaryList"`
	}
	helpers.DecodeJSON(t, resp, &result)
	if len(result.CertificateSummaryList) != 0 {
		t.Errorf("expected 0 certificates after a rejected request, got %d", len(result.CertificateSummaryList))
	}
}

// ─── DescribeCertificate — DomainValidationOptions ───────────────────────────

type domainValidation struct {
	DomainName       string   `json:"DomainName"`
	ValidationDomain string   `json:"ValidationDomain"`
	ValidationEmails []string `json:"ValidationEmails"`
	ValidationStatus string   `json:"ValidationStatus"`
	ValidationMethod string   `json:"ValidationMethod"`
	ResourceRecord   *struct {
		Name  string `json:"Name"`
		Type  string `json:"Type"`
		Value string `json:"Value"`
	} `json:"ResourceRecord"`
}

// describeCert returns the certificate detail for arn.
func describeCert(t *testing.T, srv *helpers.TestServer, arn string) struct {
	DomainValidationOptions []domainValidation `json:"DomainValidationOptions"`
} {
	t.Helper()
	resp := acmCall(t, srv, "DescribeCertificate", map[string]any{"CertificateArn": arn})
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		Certificate struct {
			DomainValidationOptions []domainValidation `json:"DomainValidationOptions"`
		} `json:"Certificate"`
	}
	helpers.DecodeJSON(t, resp, &result)
	return result.Certificate
}

// TestDescribeCertificate_dnsValidationOptions pins CertificateDetail's
// DomainValidationOptions member: one entry per domain on the certificate,
// each echoing the requested ValidationMethod and — for DNS — carrying the
// CNAME record a caller publishes to validate. Terraform's
// aws_acm_certificate_validation and the CDK's Route 53 validation construct
// both read this member; without it they have nothing to act on (#1994).
func TestDescribeCertificate_dnsValidationOptions(t *testing.T) {
	// Given: a DNS-validated certificate with a SAN
	srv := helpers.NewTestServer(t)
	resp := acmCall(t, srv, "RequestCertificate", map[string]any{
		"DomainName":              "example.com",
		"SubjectAlternativeNames": []string{"example.com", "www.example.com"},
		"ValidationMethod":        "DNS",
	})
	helpers.AssertStatus(t, resp, http.StatusOK)
	var requested struct {
		CertificateArn string `json:"CertificateArn"`
	}
	helpers.DecodeJSON(t, resp, &requested)

	// When: DescribeCertificate is called
	cert := describeCert(t, srv, requested.CertificateArn)

	// Then: one entry per unique domain, in order, each SUCCESS/DNS with a
	// CNAME record under the domain it validates
	opts := cert.DomainValidationOptions
	if len(opts) != 2 {
		t.Fatalf("expected 2 DomainValidationOptions, got %d: %+v", len(opts), opts)
	}
	for i, want := range []string{"example.com", "www.example.com"} {
		if opts[i].DomainName != want {
			t.Errorf("entry %d: DomainName = %q, want %q", i, opts[i].DomainName, want)
		}
		if opts[i].ValidationMethod != "DNS" {
			t.Errorf("entry %d: ValidationMethod = %q, want DNS", i, opts[i].ValidationMethod)
		}
		// The certificate is ISSUED on return, so every domain on it is
		// honestly reported as already validated.
		if opts[i].ValidationStatus != "SUCCESS" {
			t.Errorf("entry %d: ValidationStatus = %q, want SUCCESS", i, opts[i].ValidationStatus)
		}
		rr := opts[i].ResourceRecord
		if rr == nil {
			t.Fatalf("entry %d: expected a ResourceRecord for DNS validation", i)
		}
		if rr.Type != "CNAME" {
			t.Errorf("entry %d: ResourceRecord.Type = %q, want CNAME", i, rr.Type)
		}
		if !strings.HasPrefix(rr.Name, "_") || !strings.HasSuffix(rr.Name, "."+want+".") {
			t.Errorf("entry %d: ResourceRecord.Name = %q, want _<token>.%s.", i, rr.Name, want)
		}
		if !strings.HasPrefix(rr.Value, "_") || !strings.HasSuffix(rr.Value, ".acm-validations.aws.") {
			t.Errorf("entry %d: ResourceRecord.Value = %q, want _<token>.acm-validations.aws.", i, rr.Value)
		}
	}
	if opts[0].ResourceRecord.Name == opts[1].ResourceRecord.Name {
		t.Error("expected a distinct validation record per domain")
	}

	// And: the record is stable across calls — a caller that publishes it and
	// describes again must not be handed a different name to publish.
	again := describeCert(t, srv, requested.CertificateArn)
	for i := range opts {
		if again.DomainValidationOptions[i].ResourceRecord.Name != opts[i].ResourceRecord.Name ||
			again.DomainValidationOptions[i].ResourceRecord.Value != opts[i].ResourceRecord.Value {
			t.Errorf("entry %d: validation record changed between DescribeCertificate calls", i)
		}
	}
}

// TestDescribeCertificate_emailValidationOptions pins the EMAIL branch:
// ValidationDomain is reported and no ResourceRecord is invented, because a
// CNAME is not how email validation works. ACM defaults to email validation
// when RequestCertificate names no method, so an unset ValidationMethod lands
// here too.
func TestDescribeCertificate_emailValidationOptions(t *testing.T) {
	// Given: a certificate requested without a ValidationMethod
	srv := helpers.NewTestServer(t)
	arn := requestCert(t, srv, "example.com")

	// When: DescribeCertificate is called
	cert := describeCert(t, srv, arn)

	// Then: one EMAIL entry naming the validation domain, with no CNAME
	opts := cert.DomainValidationOptions
	if len(opts) != 1 {
		t.Fatalf("expected 1 DomainValidationOption, got %d: %+v", len(opts), opts)
	}
	if opts[0].ValidationMethod != "EMAIL" {
		t.Errorf("ValidationMethod = %q, want EMAIL", opts[0].ValidationMethod)
	}
	if opts[0].ValidationDomain != "example.com" {
		t.Errorf("ValidationDomain = %q, want example.com", opts[0].ValidationDomain)
	}
	if opts[0].ResourceRecord != nil {
		t.Errorf("expected no ResourceRecord for EMAIL validation, got %+v", opts[0].ResourceRecord)
	}
	// No mail is sent, so no addresses are reported — AWS's ValidationEmails
	// lists the addresses it actually wrote to.
	if len(opts[0].ValidationEmails) != 0 {
		t.Errorf("expected no ValidationEmails, got %v", opts[0].ValidationEmails)
	}
}

// TestRequestCertificate_unknownValidationMethod pins the ValidationMethod
// enum (EMAIL, DNS, HTTP).
func TestRequestCertificate_unknownValidationMethod(t *testing.T) {
	// Given: an empty store
	srv := helpers.NewTestServer(t)

	// When: RequestCertificate names a method the enum does not carry
	resp := acmCall(t, srv, "RequestCertificate", map[string]any{
		"DomainName": "example.com", "ValidationMethod": "CARRIER_PIGEON",
	})

	// Then: the enum constraint is reported
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	assertACMError(t, resp, "ValidationException", "Member must satisfy enum value set:")
}

// TestListCertificateDomainValidations_reportsTheRequestedMethod pins that the
// summary list echoes the method the request asked for, now that
// RequestCertificate records it.
func TestListCertificateDomainValidations_reportsTheRequestedMethod(t *testing.T) {
	// Given: a DNS-validated certificate
	srv := helpers.NewTestServer(t)
	resp := acmCall(t, srv, "RequestCertificate", map[string]any{
		"DomainName": "example.com", "ValidationMethod": "DNS",
	})
	helpers.AssertStatus(t, resp, http.StatusOK)
	var requested struct {
		CertificateArn string `json:"CertificateArn"`
	}
	helpers.DecodeJSON(t, resp, &requested)

	// When: ListCertificateDomainValidations is called
	list := acmCall(t, srv, "ListCertificateDomainValidations", map[string]any{
		"CertificateArn": requested.CertificateArn,
	})
	helpers.AssertStatus(t, list, http.StatusOK)
	var result struct {
		DomainValidationSummaryList []struct {
			DomainName                    string `json:"DomainName"`
			ActiveValidationConfiguration struct {
				ValidationMethod string `json:"ValidationMethod"`
				ValidationStatus string `json:"ValidationStatus"`
			} `json:"ActiveValidationConfiguration"`
		} `json:"DomainValidationSummaryList"`
	}
	helpers.DecodeJSON(t, list, &result)

	// Then: the active configuration names DNS
	if len(result.DomainValidationSummaryList) != 1 {
		t.Fatalf("expected 1 summary, got %d", len(result.DomainValidationSummaryList))
	}
	got := result.DomainValidationSummaryList[0].ActiveValidationConfiguration
	if got.ValidationMethod != "DNS" {
		t.Errorf("ValidationMethod = %q, want DNS", got.ValidationMethod)
	}
	if got.ValidationStatus != "SUCCESS" {
		t.Errorf("ValidationStatus = %q, want SUCCESS", got.ValidationStatus)
	}
}

// ─── ListCertificates — CertificateStatuses ──────────────────────────────────

// listCertificateDomains returns the DomainName of every certificate the given
// ListCertificates request returns.
func listCertificateDomains(t *testing.T, srv *helpers.TestServer, body map[string]any) []string {
	t.Helper()
	resp := acmCall(t, srv, "ListCertificates", body)
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		CertificateSummaryList []struct {
			DomainName string `json:"DomainName"`
		} `json:"CertificateSummaryList"`
	}
	helpers.DecodeJSON(t, resp, &result)
	domains := make([]string, 0, len(result.CertificateSummaryList))
	for _, s := range result.CertificateSummaryList {
		domains = append(domains, s.DomainName)
	}
	return domains
}

// TestListCertificates_filtersByCertificateStatuses pins the documented
// CertificateStatuses filter. Overcast issues every certificate immediately,
// so a PENDING_VALIDATION filter must come back empty rather than returning
// everything — a caller polling for a pending certificate would otherwise see
// its own ISSUED certificate as still pending (#1994).
func TestListCertificates_filtersByCertificateStatuses(t *testing.T) {
	// Given: one ISSUED certificate
	srv := helpers.NewTestServer(t)
	requestCert(t, srv, "example.com")

	// When/Then: the status filter selects on Status
	if got := listCertificateDomains(t, srv, map[string]any{
		"CertificateStatuses": []string{"PENDING_VALIDATION"},
	}); len(got) != 0 {
		t.Errorf("PENDING_VALIDATION filter returned %v, want none", got)
	}
	if got := listCertificateDomains(t, srv, map[string]any{
		"CertificateStatuses": []string{"ISSUED"},
	}); len(got) != 1 || got[0] != "example.com" {
		t.Errorf("ISSUED filter returned %v, want [example.com]", got)
	}
	// A multi-value filter matches any of the named statuses.
	if got := listCertificateDomains(t, srv, map[string]any{
		"CertificateStatuses": []string{"EXPIRED", "ISSUED"},
	}); len(got) != 1 {
		t.Errorf("EXPIRED+ISSUED filter returned %v, want [example.com]", got)
	}
	// An absent filter still returns everything.
	if got := listCertificateDomains(t, srv, map[string]any{}); len(got) != 1 {
		t.Errorf("unfiltered list returned %v, want [example.com]", got)
	}
}

// TestListCertificates_unknownStatusRejected pins CertificateStatus's enum
// constraint: a typo'd status must not silently widen the filter back to
// everything, which is the failure this whole finding is about.
func TestListCertificates_unknownStatusRejected(t *testing.T) {
	// Given: one certificate
	srv := helpers.NewTestServer(t)
	requestCert(t, srv, "example.com")

	// When: ListCertificates names a status the enum does not carry
	resp := acmCall(t, srv, "ListCertificates", map[string]any{
		"CertificateStatuses": []string{"ISSUEED"},
	})

	// Then: the enum constraint is reported
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	assertACMError(t, resp, "ValidationException",
		"at 'certificateStatuses.1.member' failed to satisfy constraint: Member must satisfy enum value set:")
}
