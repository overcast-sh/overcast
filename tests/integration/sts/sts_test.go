// Package sts_test contains integration tests for the STS service emulator.
//
// Run: go test ./tests/integration/sts/...
package sts_test

import (
	"encoding/xml"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/overcast-sh/overcast/tests/helpers"
)

// ─── Helpers ─────────────────────────────────────────────────────────────────

func stsCall(t *testing.T, srv *helpers.TestServer, action string, params url.Values) *http.Response {
	t.Helper()
	if params == nil {
		params = url.Values{}
	}
	params.Set("Action", action)
	params.Set("Version", "2011-06-15")
	body := strings.NewReader(params.Encode())
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/", body)
	if err != nil {
		t.Fatalf("stsCall: new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("stsCall: do: %v", err)
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
		t.Fatalf("decodeXML: unmarshal %T: %v\nbody: %s", dst, err, b)
	}
}

// ─── GetCallerIdentity ───────────────────────────────────────────────────────

func TestGetCallerIdentity_success(t *testing.T) {
	// Given: an STS service
	srv := helpers.NewTestServer(t)

	// When: calling GetCallerIdentity
	resp := stsCall(t, srv, "GetCallerIdentity", nil)
	defer resp.Body.Close()

	// Then: response has Account, UserId, and Arn
	helpers.AssertStatus(t, resp, http.StatusOK)

	var result struct {
		XMLName xml.Name `xml:"GetCallerIdentityResponse"`
		Result  struct {
			Account string `xml:"Account"`
			UserId  string `xml:"UserId"`
			Arn     string `xml:"Arn"`
		} `xml:"GetCallerIdentityResult"`
	}
	decodeXML(t, resp, &result)
	if result.Result.Account == "" {
		t.Error("expected Account to be set")
	}
	if result.Result.UserId == "" {
		t.Error("expected UserId to be set")
	}
	if result.Result.Arn == "" {
		t.Error("expected Arn to be set")
	}
}

// ─── GetSessionToken ─────────────────────────────────────────────────────────

func TestGetSessionToken_success(t *testing.T) {
	// Given: an STS service
	srv := helpers.NewTestServer(t)

	// When: calling GetSessionToken
	resp := stsCall(t, srv, "GetSessionToken", nil)
	defer resp.Body.Close()

	// Then: response has Credentials with AccessKeyId, SecretAccessKey, SessionToken, Expiration
	helpers.AssertStatus(t, resp, http.StatusOK)

	var result struct {
		XMLName xml.Name `xml:"GetSessionTokenResponse"`
		Result  struct {
			Credentials struct {
				AccessKeyId     string `xml:"AccessKeyId"`
				SecretAccessKey string `xml:"SecretAccessKey"`
				SessionToken    string `xml:"SessionToken"`
				Expiration      string `xml:"Expiration"`
			} `xml:"Credentials"`
		} `xml:"GetSessionTokenResult"`
	}
	decodeXML(t, resp, &result)
	creds := result.Result.Credentials
	if !strings.HasPrefix(creds.AccessKeyId, "ASIA") {
		t.Errorf("expected AccessKeyId to start with ASIA, got %q", creds.AccessKeyId)
	}
	if creds.SecretAccessKey == "" {
		t.Error("expected SecretAccessKey to be set")
	}
	if creds.SessionToken == "" {
		t.Error("expected SessionToken to be set")
	}
	if creds.Expiration == "" {
		t.Error("expected Expiration to be set")
	}
}

// ─── GetFederationToken ──────────────────────────────────────────────────────

func TestGetFederationToken_success(t *testing.T) {
	// Given: an STS service
	srv := helpers.NewTestServer(t)
	params := url.Values{"Name": {"Jane"}}

	// When: calling GetFederationToken
	resp := stsCall(t, srv, "GetFederationToken", params)
	defer resp.Body.Close()

	// Then: response has Credentials and FederatedUser
	helpers.AssertStatus(t, resp, http.StatusOK)

	var result struct {
		XMLName xml.Name `xml:"GetFederationTokenResponse"`
		Result  struct {
			Credentials struct {
				AccessKeyId string `xml:"AccessKeyId"`
			} `xml:"Credentials"`
			FederatedUser struct {
				Arn             string `xml:"Arn"`
				FederatedUserId string `xml:"FederatedUserId"`
			} `xml:"FederatedUser"`
		} `xml:"GetFederationTokenResult"`
	}
	decodeXML(t, resp, &result)
	if !strings.HasPrefix(result.Result.Credentials.AccessKeyId, "ASIA") {
		t.Errorf("expected ASIA prefix, got %q", result.Result.Credentials.AccessKeyId)
	}
	if result.Result.FederatedUser.Arn == "" {
		t.Error("expected FederatedUser.Arn to be set")
	}
}

func TestGetFederationToken_missingName(t *testing.T) {
	// Given: an STS service
	srv := helpers.NewTestServer(t)

	// When: calling GetFederationToken without Name
	resp := stsCall(t, srv, "GetFederationToken", nil)
	defer resp.Body.Close()

	// Then: 400 error
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
}

// ─── AssumeRole ──────────────────────────────────────────────────────────────

func TestAssumeRole_success(t *testing.T) {
	// Given: an STS service
	srv := helpers.NewTestServer(t)
	params := url.Values{
		"RoleArn":         {"arn:aws:iam::000000000000:role/MyRole"},
		"RoleSessionName": {"test-session"},
	}

	// When: calling AssumeRole
	resp := stsCall(t, srv, "AssumeRole", params)
	defer resp.Body.Close()

	// Then: response has Credentials and AssumedRoleUser
	helpers.AssertStatus(t, resp, http.StatusOK)

	var result struct {
		XMLName xml.Name `xml:"AssumeRoleResponse"`
		Result  struct {
			Credentials struct {
				AccessKeyId string `xml:"AccessKeyId"`
			} `xml:"Credentials"`
			AssumedRoleUser struct {
				Arn           string `xml:"Arn"`
				AssumedRoleId string `xml:"AssumedRoleId"`
			} `xml:"AssumedRoleUser"`
		} `xml:"AssumeRoleResult"`
	}
	decodeXML(t, resp, &result)
	if !strings.HasPrefix(result.Result.Credentials.AccessKeyId, "ASIA") {
		t.Errorf("expected ASIA prefix, got %q", result.Result.Credentials.AccessKeyId)
	}
	const want = "arn:aws:sts::000000000000:assumed-role/MyRole/test-session"
	if result.Result.AssumedRoleUser.Arn != want {
		t.Errorf("AssumedRoleUser.Arn = %q, want %q", result.Result.AssumedRoleUser.Arn, want)
	}
}

// TestAssumeRole_arnWithPath verifies the role name is parsed correctly out
// of a RoleArn that carries a path component (e.g. arn:aws:iam::acct:role/path/Name),
// per AWS's documented AssumedRoleUser.Arn shape.
func TestAssumeRole_arnWithPath(t *testing.T) {
	// Given: an STS service
	srv := helpers.NewTestServer(t)
	params := url.Values{
		"RoleArn":         {"arn:aws:iam::000000000000:role/some/nested/path/MyRole"},
		"RoleSessionName": {"test-session"},
	}

	// When: calling AssumeRole with a path-qualified RoleArn
	resp := stsCall(t, srv, "AssumeRole", params)
	defer resp.Body.Close()

	// Then: AssumedRoleUser.Arn uses only the role name (the final path
	// segment), not the full path, and not the session name repeated.
	helpers.AssertStatus(t, resp, http.StatusOK)

	var result struct {
		XMLName xml.Name `xml:"AssumeRoleResponse"`
		Result  struct {
			AssumedRoleUser struct {
				Arn string `xml:"Arn"`
			} `xml:"AssumedRoleUser"`
		} `xml:"AssumeRoleResult"`
	}
	decodeXML(t, resp, &result)
	const want = "arn:aws:sts::000000000000:assumed-role/MyRole/test-session"
	if result.Result.AssumedRoleUser.Arn != want {
		t.Errorf("AssumedRoleUser.Arn = %q, want %q", result.Result.AssumedRoleUser.Arn, want)
	}
}

// ─── AssumeRoleWithWebIdentity ────────────────────────────────────────────────

func TestAssumeRoleWithWebIdentity_success(t *testing.T) {
	// Given: an STS service
	srv := helpers.NewTestServer(t)
	params := url.Values{
		"RoleArn":          {"arn:aws:iam::000000000000:role/WebRole"},
		"RoleSessionName":  {"web-session"},
		"WebIdentityToken": {"some.jwt.token"},
	}

	// When: calling AssumeRoleWithWebIdentity
	resp := stsCall(t, srv, "AssumeRoleWithWebIdentity", params)
	defer resp.Body.Close()

	// Then: response has valid Credentials
	helpers.AssertStatus(t, resp, http.StatusOK)

	var result struct {
		XMLName xml.Name `xml:"AssumeRoleWithWebIdentityResponse"`
		Result  struct {
			Credentials struct {
				AccessKeyId string `xml:"AccessKeyId"`
			} `xml:"Credentials"`
			AssumedRoleUser struct {
				Arn string `xml:"Arn"`
			} `xml:"AssumedRoleUser"`
		} `xml:"AssumeRoleWithWebIdentityResult"`
	}
	decodeXML(t, resp, &result)
	if !strings.HasPrefix(result.Result.Credentials.AccessKeyId, "ASIA") {
		t.Errorf("expected ASIA prefix, got %q", result.Result.Credentials.AccessKeyId)
	}
	const want = "arn:aws:sts::000000000000:assumed-role/WebRole/web-session"
	if result.Result.AssumedRoleUser.Arn != want {
		t.Errorf("AssumedRoleUser.Arn = %q, want %q", result.Result.AssumedRoleUser.Arn, want)
	}
}

func TestAssumeRoleWithWebIdentity_missingTokenIsMissingParameter(t *testing.T) {
	// Given: an STS service
	srv := helpers.NewTestServer(t)

	// When: AssumeRoleWithWebIdentity is called without the required token
	resp := stsCall(t, srv, "AssumeRoleWithWebIdentity", url.Values{
		"RoleArn":         {"arn:aws:iam::000000000000:role/WebRole"},
		"RoleSessionName": {"web-session"},
	})
	defer resp.Body.Close()

	// Then: it is refused, as the model requires WebIdentityToken
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	if body := helpers.ReadBody(t, resp); !strings.Contains(body, "<Code>MissingParameter</Code>") || !strings.Contains(body, "WebIdentityToken") {
		t.Fatalf("body = %s, want MissingParameter naming WebIdentityToken", body)
	}
}

// ─── Unsupported operations ───────────────────────────────────────────────────

// TestUnsupportedOperations_notImplemented documents that STS operations
// Overcast does not emulate return a NotImplemented error in AWS Query XML
// form. These operations are tracked as DocOnly unsupported capabilities.
func TestUnsupportedOperations_notImplemented(t *testing.T) {
	// Given: an STS service
	srv := helpers.NewTestServer(t)

	unsupported := []string{
		"AssumeRoleWithSAML",
		"AssumeRoot",
		"DecodeAuthorizationMessage",
		"GetAccessKeyInfo",
		"GetDelegatedAccessToken",
		"GetWebIdentityToken",
	}

	for _, action := range unsupported {
		t.Run(action, func(t *testing.T) {
			// When: calling an unsupported action
			resp := stsCall(t, srv, action, nil)
			defer resp.Body.Close()

			// Then: a NotImplemented XML error is returned with the standard
			// unsupported-operation marker used by every protocol helper.
			helpers.AssertStatus(t, resp, http.StatusNotImplemented)
			helpers.AssertHeader(t, resp, "x-emulator-unsupported", "true")
			helpers.AssertRequestID(t, resp)
			var result struct {
				XMLName xml.Name `xml:"ErrorResponse"`
				Error   struct {
					Code string `xml:"Code"`
				} `xml:"Error"`
			}
			decodeXML(t, resp, &result)
			if result.Error.Code != "NotImplemented" {
				t.Errorf("expected NotImplemented, got %q", result.Error.Code)
			}
		})
	}
}
