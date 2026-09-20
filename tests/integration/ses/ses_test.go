// Package ses_test contains integration tests for the SES service emulator.
//
// It covers both the SES v1 Query-protocol API (used by aws-sdk-go-v2/service/ses,
// boto3 ses, @aws-sdk/client-ses) and the SES v2 REST-JSON API (used by
// aws-sdk-go-v2/service/sesv2, boto3 sesv2, @aws-sdk/client-sesv2).
//
// Run: go test ./tests/integration/ses/...
package ses_test

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/overcast-sh/overcast/internal/awsapi"
	"github.com/overcast-sh/overcast/tests/helpers"
)

// ─── helpers ─────────────────────────────────────────────────────────────────

// modeledBinding returns the HTTP method and URI template the pinned AWS
// Smithy models give an operation, so a route assertion is checked against the
// model rather than against whatever the emulator happens to have registered.
func modeledBinding(t *testing.T, modelService, operation string) (method, uri string) {
	t.Helper()
	awsapi.WalkOperations(func(op awsapi.Operation) bool {
		if op.Service == modelService && op.Name == operation {
			method, uri = op.HTTPMethod, op.URI
			return false
		}
		return true
	})
	if method == "" {
		t.Fatalf("%s/%s is not in the pinned AWS operation manifest", modelService, operation)
	}
	return method, uri
}

// sesCall performs an SES v1 Query-protocol POST / request.
func sesCall(t *testing.T, srv *helpers.TestServer, action string, params url.Values) *http.Response {
	t.Helper()
	params.Set("Action", action)
	params.Set("Version", "2010-12-01")
	body := strings.NewReader(params.Encode())
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/", body)
	if err != nil {
		t.Fatalf("sesCall: new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-Amz-Target", "") // no Target header — SES uses form body
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("sesCall: do: %v", err)
	}
	return resp
}

// v2Call performs an SES v2 REST-JSON request.
func v2Call(t *testing.T, srv *helpers.TestServer, method, path string, body any) *http.Response {
	t.Helper()
	var reqBody io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("v2Call: marshal: %v", err)
		}
		reqBody = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, srv.URL+path, reqBody)
	if err != nil {
		t.Fatalf("v2Call: new request: %v", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("v2Call: do: %v", err)
	}
	return resp
}

func decodeXML(t *testing.T, resp *http.Response, dst any) {
	t.Helper()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("decodeXML: read: %v", err)
	}
	if err := xml.Unmarshal(b, dst); err != nil {
		t.Fatalf("decodeXML: unmarshal: %v\nbody: %s", err, b)
	}
}

func decodeJSON(t *testing.T, resp *http.Response, dst any) {
	t.Helper()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("decodeJSON: read: %v", err)
	}
	if err := json.Unmarshal(b, dst); err != nil {
		t.Fatalf("decodeJSON: unmarshal: %v\nbody: %s", err, b)
	}
}

// ─── SES v1 — VerifyEmailIdentity ────────────────────────────────────────────

func TestSES_VerifyEmailIdentity_success(t *testing.T) {
	// Given a running server
	srv := helpers.NewTestServer(t)

	// When I verify an email address
	resp := sesCall(t, srv, "VerifyEmailIdentity", url.Values{
		"EmailAddress": {"alice@example.com"},
	})
	defer resp.Body.Close()

	// Then the response is 200 OK
	helpers.AssertStatus(t, resp, http.StatusOK)
}

func TestSES_VerifyEmailIdentity_missingParam(t *testing.T) {
	// Given a running server
	srv := helpers.NewTestServer(t)

	// When I call VerifyEmailIdentity without EmailAddress
	resp := sesCall(t, srv, "VerifyEmailIdentity", url.Values{})
	defer resp.Body.Close()

	// Then the response is 400 Bad Request
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
}

// ─── SES v1 — VerifyDomainIdentity ───────────────────────────────────────────

func TestSES_VerifyDomainIdentity_success(t *testing.T) {
	// Given a running server
	srv := helpers.NewTestServer(t)

	// When I verify a domain
	resp := sesCall(t, srv, "VerifyDomainIdentity", url.Values{
		"Domain": {"example.com"},
	})
	defer resp.Body.Close()

	// Then the response is 200 OK with a verification token
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		XMLName xml.Name `xml:"VerifyDomainIdentityResponse"`
		Result  struct {
			VerificationToken string `xml:"VerificationToken"`
		} `xml:"VerifyDomainIdentityResult"`
	}
	decodeXML(t, resp, &result)
	if result.Result.VerificationToken == "" {
		t.Error("expected VerificationToken to be set")
	}
}

// ─── SES v1 — ListIdentities ──────────────────────────────────────────────────

func TestSES_ListIdentities_empty(t *testing.T) {
	// Given a running server with no identities
	srv := helpers.NewTestServer(t)

	// When I list identities
	resp := sesCall(t, srv, "ListIdentities", url.Values{})
	defer resp.Body.Close()

	// Then the response is 200 OK with an empty list
	helpers.AssertStatus(t, resp, http.StatusOK)
}

func TestSES_ListIdentities_returnsVerified(t *testing.T) {
	// Given a server with a verified email
	srv := helpers.NewTestServer(t)
	r := sesCall(t, srv, "VerifyEmailIdentity", url.Values{"EmailAddress": {"bob@example.com"}})
	r.Body.Close()

	// When I list identities
	resp := sesCall(t, srv, "ListIdentities", url.Values{})
	defer resp.Body.Close()

	// Then the email appears in the list
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		XMLName xml.Name `xml:"ListIdentitiesResponse"`
		Result  struct {
			Identities struct {
				Members []string `xml:"member"`
			} `xml:"Identities"`
		} `xml:"ListIdentitiesResult"`
	}
	decodeXML(t, resp, &result)
	found := false
	for _, id := range result.Result.Identities.Members {
		if id == "bob@example.com" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected bob@example.com in identities, got %v", result.Result.Identities.Members)
	}
}

// ─── SES v1 — GetIdentityVerificationAttributes ───────────────────────────────

func TestSES_GetIdentityVerificationAttributes_verified(t *testing.T) {
	// Given a server with a verified email
	srv := helpers.NewTestServer(t)
	r := sesCall(t, srv, "VerifyEmailIdentity", url.Values{"EmailAddress": {"carol@example.com"}})
	r.Body.Close()

	// When I get verification attributes
	resp := sesCall(t, srv, "GetIdentityVerificationAttributes", url.Values{
		"Identities.member.1": {"carol@example.com"},
	})
	defer resp.Body.Close()

	// Then the identity shows as Success
	helpers.AssertStatus(t, resp, http.StatusOK)
	b, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(b), "Success") {
		t.Errorf("expected VerificationStatus=Success in response, got: %s", b)
	}
}

// ─── SES v1 — DeleteIdentity ──────────────────────────────────────────────────

func TestSES_DeleteIdentity_success(t *testing.T) {
	// Given a verified email
	srv := helpers.NewTestServer(t)
	r := sesCall(t, srv, "VerifyEmailIdentity", url.Values{"EmailAddress": {"dave@example.com"}})
	r.Body.Close()

	// When I delete it
	resp := sesCall(t, srv, "DeleteIdentity", url.Values{"Identity": {"dave@example.com"}})
	defer resp.Body.Close()

	// Then 200 OK
	helpers.AssertStatus(t, resp, http.StatusOK)

	// And it no longer appears in the list
	listResp := sesCall(t, srv, "ListIdentities", url.Values{})
	defer listResp.Body.Close()
	b, _ := io.ReadAll(listResp.Body)
	if strings.Contains(string(b), "dave@example.com") {
		t.Error("expected dave@example.com to be deleted, but still in list")
	}
}

// ─── SES v1 — SendEmail (no mailer) ──────────────────────────────────────────

func TestSES_SendEmail_noMailer(t *testing.T) {
	// Given a server with no SMTP configured
	srv := helpers.NewTestServer(t)

	// When I send an email
	resp := sesCall(t, srv, "SendEmail", url.Values{
		"Source":                           {"sender@example.com"},
		"Destination.ToAddresses.member.1": {"recipient@example.com"},
		"Message.Subject.Data":             {"Hello"},
		"Message.Body.Text.Data":           {"Hello world"},
	})
	defer resp.Body.Close()

	// Then it succeeds with a MessageId (delivery is no-op without mailer)
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		XMLName xml.Name `xml:"SendEmailResponse"`
		Result  struct {
			MessageId string `xml:"MessageId"`
		} `xml:"SendEmailResult"`
	}
	decodeXML(t, resp, &result)
	if result.Result.MessageId == "" {
		t.Error("expected MessageId to be set")
	}
}

func TestSES_SendEmail_missingSource(t *testing.T) {
	// Given a server
	srv := helpers.NewTestServer(t)

	// When I send without a Source
	resp := sesCall(t, srv, "SendEmail", url.Values{
		"Destination.ToAddresses.member.1": {"recipient@example.com"},
		"Message.Subject.Data":             {"Hello"},
		"Message.Body.Text.Data":           {"Hello world"},
	})
	defer resp.Body.Close()

	// Then 400
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
}

// ─── SES v1 — GetSendQuota ────────────────────────────────────────────────────

func TestSES_GetSendQuota(t *testing.T) {
	// Given a server
	srv := helpers.NewTestServer(t)

	// When I get the send quota
	resp := sesCall(t, srv, "GetSendQuota", url.Values{})
	defer resp.Body.Close()

	// Then 200 OK with quota fields
	helpers.AssertStatus(t, resp, http.StatusOK)
	b, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(b), "Max24HourSend") {
		t.Errorf("expected Max24HourSend in response, got: %s", b)
	}
}

// ─── SES v1 — Unsupported stub operations (issue #181 / tracker #42) ─────────
//
// internal/services/ses/handler_stubs.go routes every SES v1 Query-protocol
// operation Overcast has not implemented through h.stub, which calls
// protocol.NotImplementedQueryXML. That helper (internal/protocol/errors.go)
// writes the same AWS Query-protocol error envelope SNS and EC2 use:
//
//	<?xml version="1.0" encoding="UTF-8"?>
//	<ErrorResponse><Error><Type>Sender</Type><Code>NotImplemented</Code><Message>...</Message></Error><RequestId>...</RequestId></ErrorResponse>
//
// SES v1 is modeled with aws.protocols#awsQuery (confirmed against the pinned
// model, models/ses/service/2010-12-01/ses-2010-12-01.json — the service
// shape carries "aws.protocols#awsQuery": {}), the same protocol trait SNS
// and EC2 carry, so this is the correct wire shape for any SES v1 error, not
// an emulator invention. NotImplemented itself is an Overcast-only signal
// (real AWS would never return it), which is why the 501 status and the
// x-emulator-unsupported header — rather than the exact error Code — are
// what these tests pin.
//
// The representative stubs below are drawn from the StatusUnsupported rows
// in internal/services/ses/capabilities_dev.go.

// sesStubQueryXMLError is the AWS Query-protocol ErrorResponse envelope.
type sesStubQueryXMLError struct {
	XMLName xml.Name `xml:"ErrorResponse"`
	Error   struct {
		Type    string `xml:"Type"`
		Code    string `xml:"Code"`
		Message string `xml:"Message"`
	} `xml:"Error"`
	RequestID string `xml:"RequestId"`
}

func TestSES_UnsupportedV1Stub_returnsQueryProtocolNotImplemented(t *testing.T) {
	// Given a running server, for each SES v1 operation Overcast has not
	// implemented (routed to h.stub in internal/services/ses/handler.go)
	stubs := []string{
		"GetIdentityDkimAttributes",
		"SetIdentityDkimEnabled",
		"CreateConfigurationSet",
	}
	for _, action := range stubs {
		t.Run(action, func(t *testing.T) {
			srv := helpers.NewTestServer(t)

			// When the stubbed action is called
			resp := sesCall(t, srv, action, url.Values{})
			defer resp.Body.Close()

			// Then it answers 501 with the emulator-unsupported marker, a
			// request ID header, and the Query-protocol ErrorResponse envelope
			helpers.AssertStatus(t, resp, http.StatusNotImplemented)
			helpers.AssertHeader(t, resp, "x-emulator-unsupported", "true")
			helpers.AssertRequestID(t, resp)

			var errResp sesStubQueryXMLError
			decodeXML(t, resp, &errResp)
			if errResp.Error.Type != "Sender" {
				t.Errorf("Error.Type = %q, want %q", errResp.Error.Type, "Sender")
			}
			if errResp.Error.Code != "NotImplemented" {
				t.Errorf("Error.Code = %q, want %q", errResp.Error.Code, "NotImplemented")
			}
			if errResp.Error.Message == "" {
				t.Error("expected Error.Message to be set")
			}
			if errResp.RequestID == "" {
				t.Error("expected RequestId to be set")
			}
		})
	}
}

// TestSES_UnsupportedV1Stub_supportedOperationStillWorks proves the 501s above
// come from per-action stub routing, not from a server that answers every SES
// v1 request with 501 regardless of Action.
func TestSES_UnsupportedV1Stub_supportedOperationStillWorks(t *testing.T) {
	// Given the same kind of server the stub tests above use
	srv := helpers.NewTestServer(t)

	// When a supported v1 operation is called on it
	resp := sesCall(t, srv, "VerifyEmailIdentity", url.Values{
		"EmailAddress": {"stub-routing-check@example.com"},
	})
	defer resp.Body.Close()

	// Then it succeeds normally
	helpers.AssertStatus(t, resp, http.StatusOK)
	helpers.AssertRequestID(t, resp)
}

// ─── SES v2 — POST /v2/email/identities ──────────────────────────────────────

// CreateEmailIdentity is modeled as POST /v2/email/identities, so that is the
// only binding an SDK or the CLI will ever send. Overcast registered it under
// PUT instead, which left a fully implemented operation unreachable to every
// real client (#862). Assert the modeled method reaches the handler and that
// the invented one creates nothing, so the wrong binding cannot come back and
// cannot be papered over with an alias.
func TestSESV2_CreateEmailIdentity_boundToTheModeledMethod(t *testing.T) {
	// Given the pinned model's binding for the operation
	method, uri := modeledBinding(t, "sesv2", "CreateEmailIdentity")
	if method != http.MethodPost || uri != "/v2/email/identities" {
		t.Fatalf("model binds CreateEmailIdentity to %s %s; this test is out of date", method, uri)
	}
	srv := helpers.NewTestServer(t)

	// When I call it the way the model says
	resp := v2Call(t, srv, method, uri, map[string]string{
		"EmailIdentity": "postbound@example.com",
	})
	defer resp.Body.Close()

	// Then it is handled rather than left to the unimplemented-operation path
	helpers.AssertStatus(t, resp, http.StatusOK)

	// And the method AWS does not model creates nothing
	unmodeled := v2Call(t, srv, http.MethodPut, uri, map[string]string{
		"EmailIdentity": "putbound@example.com",
	})
	unmodeled.Body.Close()

	list := v2Call(t, srv, http.MethodGet, uri, nil)
	defer list.Body.Close()
	helpers.AssertStatus(t, list, http.StatusOK)
	b, err := io.ReadAll(list.Body)
	if err != nil {
		t.Fatalf("read identity list: %v", err)
	}
	if !strings.Contains(string(b), "postbound@example.com") {
		t.Errorf("%s %s did not create the identity: %s", method, uri, b)
	}
	if strings.Contains(string(b), "putbound@example.com") {
		t.Errorf("PUT %s created an identity; AWS models no such binding: %s", uri, b)
	}
}

func TestSESV2_CreateEmailIdentity_email(t *testing.T) {
	// Given a running server
	srv := helpers.NewTestServer(t)

	// When I create an email identity via v2
	resp := v2Call(t, srv, http.MethodPost, "/v2/email/identities", map[string]string{
		"EmailIdentity": "test@example.com",
	})
	defer resp.Body.Close()

	// Then 200 OK with identity type
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result map[string]any
	decodeJSON(t, resp, &result)
	if result["IdentityType"] != "EMAIL_ADDRESS" {
		t.Errorf("expected IdentityType=EMAIL_ADDRESS, got %v", result["IdentityType"])
	}
}

func TestSESV2_CreateEmailIdentity_domain(t *testing.T) {
	// Given a running server
	srv := helpers.NewTestServer(t)

	// When I create a domain identity via v2
	resp := v2Call(t, srv, http.MethodPost, "/v2/email/identities", map[string]string{
		"EmailIdentity": "example.com",
	})
	defer resp.Body.Close()

	// Then 200 OK with domain type
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result map[string]any
	decodeJSON(t, resp, &result)
	if result["IdentityType"] != "DOMAIN" {
		t.Errorf("expected IdentityType=DOMAIN, got %v", result["IdentityType"])
	}
}

// ─── SES v2 — GET /v2/email/identities ───────────────────────────────────────

func TestSESV2_ListEmailIdentities(t *testing.T) {
	// Given a server with an identity
	srv := helpers.NewTestServer(t)
	r := v2Call(t, srv, http.MethodPost, "/v2/email/identities", map[string]string{"EmailIdentity": "list@example.com"})
	r.Body.Close()

	// When I list identities
	resp := v2Call(t, srv, http.MethodGet, "/v2/email/identities", nil)
	defer resp.Body.Close()

	// Then the identity appears
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		EmailIdentities []struct {
			IdentityName string `json:"IdentityName"`
		} `json:"EmailIdentities"`
	}
	decodeJSON(t, resp, &result)
	found := false
	for _, id := range result.EmailIdentities {
		if id.IdentityName == "list@example.com" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected list@example.com in identities")
	}
}

// ─── SES v2 — GET /v2/email/identities/{EmailIdentity} ────────────────────────

func TestSESV2_GetEmailIdentity(t *testing.T) {
	// Given a server with an identity
	srv := helpers.NewTestServer(t)
	r := v2Call(t, srv, http.MethodPost, "/v2/email/identities", map[string]string{"EmailIdentity": "get@example.com"})
	r.Body.Close()

	// When I get the identity
	resp := v2Call(t, srv, http.MethodGet, "/v2/email/identities/get@example.com", nil)
	defer resp.Body.Close()

	// Then 200 OK with identity details
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result map[string]any
	decodeJSON(t, resp, &result)
	if result["IdentityType"] != "EMAIL_ADDRESS" {
		t.Errorf("expected IdentityType=EMAIL_ADDRESS, got %v", result["IdentityType"])
	}
}

// ─── SES v2 — DELETE /v2/email/identities/{EmailIdentity} ─────────────────────

func TestSESV2_DeleteEmailIdentity(t *testing.T) {
	// Given a server with an identity
	srv := helpers.NewTestServer(t)
	r := v2Call(t, srv, http.MethodPost, "/v2/email/identities", map[string]string{"EmailIdentity": "delete@example.com"})
	r.Body.Close()

	// When I delete it
	resp := v2Call(t, srv, http.MethodDelete, "/v2/email/identities/delete@example.com", nil)
	defer resp.Body.Close()

	// Then 200 OK
	helpers.AssertStatus(t, resp, http.StatusOK)

	// And it no longer appears in the list
	listResp := v2Call(t, srv, http.MethodGet, "/v2/email/identities", nil)
	defer listResp.Body.Close()
	b, _ := io.ReadAll(listResp.Body)
	if strings.Contains(string(b), "delete@example.com") {
		t.Error("expected delete@example.com to be deleted from list")
	}
}

// ─── SES v2 — POST /v2/email/outbound-emails ─────────────────────────────────

func TestSESV2_SendEmail_simple(t *testing.T) {
	// Given a server with no SMTP configured
	srv := helpers.NewTestServer(t)

	// When I send a simple email via v2
	resp := v2Call(t, srv, http.MethodPost, "/v2/email/outbound-emails", map[string]any{
		"FromEmailAddress": "sender@example.com",
		"Destination": map[string]any{
			"ToAddresses": []string{"recipient@example.com"},
		},
		"Content": map[string]any{
			"Simple": map[string]any{
				"Subject": map[string]string{"Data": "Test Subject"},
				"Body": map[string]any{
					"Text": map[string]string{"Data": "Hello world"},
				},
			},
		},
	})
	defer resp.Body.Close()

	// Then it succeeds with a MessageId
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result map[string]any
	decodeJSON(t, resp, &result)
	if result["MessageId"] == "" {
		t.Error("expected MessageId to be set")
	}
}

func TestSESV2_SendEmail_missingContent(t *testing.T) {
	// Given a server
	srv := helpers.NewTestServer(t)

	// When I send without Content
	resp := v2Call(t, srv, http.MethodPost, "/v2/email/outbound-emails", map[string]any{
		"FromEmailAddress": "sender@example.com",
	})
	defer resp.Body.Close()

	// Then 400
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
}

// ─── Template tests ──────────────────────────────────────────────────────────

func TestCreateTemplate_success(t *testing.T) {
	// Given: a fresh server
	srv := helpers.NewTestServer(t)

	// When: CreateTemplate is called
	resp := sesCall(t, srv, "CreateTemplate", url.Values{
		"Template.TemplateName": {"my-template"},
		"Template.SubjectPart":  {"Hello {{name}}"},
		"Template.TextPart":     {"Hi {{name}}"},
		"Template.HtmlPart":     {"<p>Hi {{name}}</p>"},
	})
	defer resp.Body.Close()

	// Then: 200 OK
	helpers.AssertStatus(t, resp, http.StatusOK)
}

func TestCreateTemplate_duplicate(t *testing.T) {
	// Given: a template already exists
	srv := helpers.NewTestServer(t)
	r := sesCall(t, srv, "CreateTemplate", url.Values{
		"Template.TemplateName": {"dup-template"},
		"Template.SubjectPart":  {"Subject"},
	})
	r.Body.Close()

	// When: CreateTemplate is called again with the same name
	resp := sesCall(t, srv, "CreateTemplate", url.Values{
		"Template.TemplateName": {"dup-template"},
		"Template.SubjectPart":  {"Subject2"},
	})
	defer resp.Body.Close()

	// Then: 400 AlreadyExists error
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
}

func TestGetTemplate_success(t *testing.T) {
	// Given: a template exists
	srv := helpers.NewTestServer(t)
	r := sesCall(t, srv, "CreateTemplate", url.Values{
		"Template.TemplateName": {"get-template"},
		"Template.SubjectPart":  {"Hello {{name}}"},
		"Template.HtmlPart":     {"<p>Hello</p>"},
	})
	r.Body.Close()

	// When: GetTemplate is called
	resp := sesCall(t, srv, "GetTemplate", url.Values{
		"TemplateName": {"get-template"},
	})
	defer resp.Body.Close()

	// Then: 200 with TemplateName in body
	helpers.AssertStatus(t, resp, http.StatusOK)
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "get-template") {
		t.Errorf("expected TemplateName in response, got: %s", body)
	}
}

func TestGetTemplate_notFound(t *testing.T) {
	// Given: no templates
	srv := helpers.NewTestServer(t)

	// When: GetTemplate is called for a non-existent template
	resp := sesCall(t, srv, "GetTemplate", url.Values{
		"TemplateName": {"no-such-template"},
	})
	defer resp.Body.Close()

	// Then: 400 TemplateDoesNotExist
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
}

func TestUpdateTemplate_success(t *testing.T) {
	// Given: a template exists
	srv := helpers.NewTestServer(t)
	r := sesCall(t, srv, "CreateTemplate", url.Values{
		"Template.TemplateName": {"update-template"},
		"Template.SubjectPart":  {"Old subject"},
	})
	r.Body.Close()

	// When: UpdateTemplate is called with a new subject
	resp := sesCall(t, srv, "UpdateTemplate", url.Values{
		"Template.TemplateName": {"update-template"},
		"Template.SubjectPart":  {"New subject"},
	})
	defer resp.Body.Close()

	// Then: 200 OK
	helpers.AssertStatus(t, resp, http.StatusOK)

	// And: GetTemplate reflects the new subject
	getResp := sesCall(t, srv, "GetTemplate", url.Values{"TemplateName": {"update-template"}})
	defer getResp.Body.Close()
	body, _ := io.ReadAll(getResp.Body)
	if !strings.Contains(string(body), "New subject") {
		t.Errorf("expected updated subject in get response, got: %s", body)
	}
}

func TestListTemplates_success(t *testing.T) {
	// Given: two templates exist
	srv := helpers.NewTestServer(t)
	for _, name := range []string{"list-tmpl-1", "list-tmpl-2"} {
		r := sesCall(t, srv, "CreateTemplate", url.Values{
			"Template.TemplateName": {name},
			"Template.SubjectPart":  {"Subject"},
		})
		r.Body.Close()
	}

	// When: ListTemplates is called
	resp := sesCall(t, srv, "ListTemplates", url.Values{})
	defer resp.Body.Close()

	// Then: both templates appear in the response
	helpers.AssertStatus(t, resp, http.StatusOK)
	body, _ := io.ReadAll(resp.Body)
	bs := string(body)
	for _, name := range []string{"list-tmpl-1", "list-tmpl-2"} {
		if !strings.Contains(bs, name) {
			t.Errorf("expected %q in ListTemplates response, got: %s", name, bs)
		}
	}
}

func TestDeleteTemplate_success(t *testing.T) {
	// Given: a template exists
	srv := helpers.NewTestServer(t)
	r := sesCall(t, srv, "CreateTemplate", url.Values{
		"Template.TemplateName": {"del-template"},
		"Template.SubjectPart":  {"Subject"},
	})
	r.Body.Close()

	// When: DeleteTemplate is called
	resp := sesCall(t, srv, "DeleteTemplate", url.Values{
		"TemplateName": {"del-template"},
	})
	defer resp.Body.Close()

	// Then: 200 OK
	helpers.AssertStatus(t, resp, http.StatusOK)

	// And: GetTemplate returns not found
	getResp := sesCall(t, srv, "GetTemplate", url.Values{"TemplateName": {"del-template"}})
	defer getResp.Body.Close()
	helpers.AssertStatus(t, getResp, http.StatusBadRequest)
}

func TestSendTemplatedEmail_success(t *testing.T) {
	// Given: a template and verified identity exist
	srv := helpers.NewTestServer(t)
	c := sesCall(t, srv, "CreateTemplate", url.Values{
		"Template.TemplateName": {"send-template"},
		"Template.SubjectPart":  {"Hello {{name}}"},
		"Template.TextPart":     {"Hi {{name}}"},
		"Template.HtmlPart":     {"<p>Hi {{name}}</p>"},
	})
	c.Body.Close()
	v := sesCall(t, srv, "VerifyEmailIdentity", url.Values{"EmailAddress": {"sender@example.com"}})
	v.Body.Close()

	// When: SendTemplatedEmail is called
	resp := sesCall(t, srv, "SendTemplatedEmail", url.Values{
		"Source":                           {"sender@example.com"},
		"Destination.ToAddresses.member.1": {"recipient@example.com"},
		"Template":                         {"send-template"},
		"TemplateData":                     {`{"name":"World"}`},
	})
	defer resp.Body.Close()

	// Then: 200 with a MessageId
	helpers.AssertStatus(t, resp, http.StatusOK)
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "MessageId") {
		t.Errorf("expected MessageId in response, got: %s", body)
	}
}

// ─── SES v2 — /v2/email/tags ─────────────────────────────────────────────────
//
// Email identities are taggable in real SESv2 through TagResource /
// UntagResource / ListTagsForResource, and CreateEmailIdentity applies inline
// Tags at creation.

// sesIdentityARN builds the ARN SESv2's tag operations address an identity by.
func sesIdentityARN(identity string) string {
	return "arn:aws:ses:us-east-1:000000000000:identity/" + identity
}

// sesListTags reads a resource's tags back through ListTagsForResource.
func sesListTags(t *testing.T, srv *helpers.TestServer, arn string) map[string]string {
	t.Helper()
	resp := v2Call(t, srv, http.MethodGet, "/v2/email/tags?ResourceArn="+url.QueryEscape(arn), nil)
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	var out struct {
		Tags []struct {
			Key   string `json:"Key"`
			Value string `json:"Value"`
		} `json:"Tags"`
	}
	decodeJSON(t, resp, &out)
	got := make(map[string]string, len(out.Tags))
	for _, tag := range out.Tags {
		got[tag.Key] = tag.Value
	}
	return got
}

func TestSESV2_TagResource_roundTripsOnAnIdentity(t *testing.T) {
	srv := helpers.NewTestServer(t)
	resp := v2Call(t, srv, http.MethodPost, "/v2/email/identities", map[string]string{
		"EmailIdentity": "tagged@example.com",
	})
	helpers.AssertStatus(t, resp, http.StatusOK)
	resp.Body.Close()

	arn := sesIdentityARN("tagged@example.com")
	resp = v2Call(t, srv, http.MethodPost, "/v2/email/tags", map[string]any{
		"ResourceArn": arn,
		"Tags":        []map[string]string{{"Key": "env", "Value": "prod"}, {"Key": "team", "Value": "comms"}},
	})
	helpers.AssertStatus(t, resp, http.StatusOK)
	resp.Body.Close()

	if got := sesListTags(t, srv, arn); got["env"] != "prod" || got["team"] != "comms" {
		t.Fatalf("TagResource did not round-trip: got %v", got)
	}
}

func TestSESV2_UntagResource_removesNamedKeysOnly(t *testing.T) {
	srv := helpers.NewTestServer(t)
	resp := v2Call(t, srv, http.MethodPost, "/v2/email/identities", map[string]string{
		"EmailIdentity": "untagged@example.com",
	})
	helpers.AssertStatus(t, resp, http.StatusOK)
	resp.Body.Close()

	arn := sesIdentityARN("untagged@example.com")
	resp = v2Call(t, srv, http.MethodPost, "/v2/email/tags", map[string]any{
		"ResourceArn": arn,
		"Tags":        []map[string]string{{"Key": "env", "Value": "prod"}, {"Key": "keep", "Value": "yes"}},
	})
	helpers.AssertStatus(t, resp, http.StatusOK)
	resp.Body.Close()

	// TagKeys is an httpQuery list member, so it repeats rather than joining.
	resp = v2Call(t, srv, http.MethodDelete,
		"/v2/email/tags?ResourceArn="+url.QueryEscape(arn)+"&TagKeys=env", nil)
	helpers.AssertStatus(t, resp, http.StatusOK)
	resp.Body.Close()

	got := sesListTags(t, srv, arn)
	if _, still := got["env"]; still {
		t.Errorf("UntagResource left env in place: %v", got)
	}
	if got["keep"] != "yes" {
		t.Errorf("UntagResource removed an unrelated tag: %v", got)
	}
}

func TestSESV2_CreateEmailIdentity_appliesTagsAtCreation(t *testing.T) {
	srv := helpers.NewTestServer(t)
	resp := v2Call(t, srv, http.MethodPost, "/v2/email/identities", map[string]any{
		"EmailIdentity": "tagatcreate@example.com",
		"Tags":          []map[string]string{{"Key": "env", "Value": "staging"}},
	})
	helpers.AssertStatus(t, resp, http.StatusOK)
	resp.Body.Close()

	if got := sesListTags(t, srv, sesIdentityARN("tagatcreate@example.com")); got["env"] != "staging" {
		t.Errorf("CreateEmailIdentity tags not applied at creation: got %v", got)
	}
}

// GetEmailIdentity's modeled output has a Tags member, so the console and the
// SDK read an identity's tags without a second call.
func TestSESV2_GetEmailIdentity_reportsTags(t *testing.T) {
	srv := helpers.NewTestServer(t)
	resp := v2Call(t, srv, http.MethodPost, "/v2/email/identities", map[string]any{
		"EmailIdentity": "gettags@example.com",
		"Tags":          []map[string]string{{"Key": "env", "Value": "dev"}},
	})
	helpers.AssertStatus(t, resp, http.StatusOK)
	resp.Body.Close()

	resp = v2Call(t, srv, http.MethodGet, "/v2/email/identities/"+url.PathEscape("gettags@example.com"), nil)
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	var out struct {
		Tags []struct {
			Key   string `json:"Key"`
			Value string `json:"Value"`
		} `json:"Tags"`
	}
	decodeJSON(t, resp, &out)
	if len(out.Tags) != 1 || out.Tags[0].Key != "env" || out.Tags[0].Value != "dev" {
		t.Errorf("GetEmailIdentity Tags = %+v, want one env=dev entry", out.Tags)
	}
}

// Deleting the identity must take its tags with it, so a later identity of the
// same name does not inherit them.
func TestSESV2_DeleteEmailIdentity_dropsItsTags(t *testing.T) {
	srv := helpers.NewTestServer(t)
	resp := v2Call(t, srv, http.MethodPost, "/v2/email/identities", map[string]any{
		"EmailIdentity": "recycled@example.com",
		"Tags":          []map[string]string{{"Key": "env", "Value": "prod"}},
	})
	helpers.AssertStatus(t, resp, http.StatusOK)
	resp.Body.Close()

	resp = v2Call(t, srv, http.MethodDelete, "/v2/email/identities/"+url.PathEscape("recycled@example.com"), nil)
	helpers.AssertStatus(t, resp, http.StatusOK)
	resp.Body.Close()

	resp = v2Call(t, srv, http.MethodPost, "/v2/email/identities", map[string]string{
		"EmailIdentity": "recycled@example.com",
	})
	helpers.AssertStatus(t, resp, http.StatusOK)
	resp.Body.Close()

	if got := sesListTags(t, srv, sesIdentityARN("recycled@example.com")); len(got) != 0 {
		t.Errorf("recreated identity inherited tags from the deleted one: %v", got)
	}
}

func TestSESV2_TagResource_unknownIdentity(t *testing.T) {
	srv := helpers.NewTestServer(t)
	resp := v2Call(t, srv, http.MethodPost, "/v2/email/tags", map[string]any{
		"ResourceArn": sesIdentityARN("nobody@example.com"),
		"Tags":        []map[string]string{{"Key": "env", "Value": "prod"}},
	})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusNotFound)
}
