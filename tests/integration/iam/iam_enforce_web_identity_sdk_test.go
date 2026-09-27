package iam_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials/stscreds"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/aws/smithy-go"
	smithyhttp "github.com/aws/smithy-go/transport/http"

	"github.com/overcast-sh/overcast/tests/helpers"
)

// The tests in this file pin #2273: under enforcement an SDK's
// AssumeRoleWithWebIdentity, which it sends unsigned, is decided by the
// role's trust policy, as on AWS.

const webIdentityRoleARN = "arn:aws:iam::000000000000:role/ci"

// staticIDToken is a web identity token the SDK's provider reads.
type staticIDToken string

func (t staticIDToken) GetIdentityToken() ([]byte, error) { return []byte(t), nil }

// oidcIDToken is an oidc.example.com ID token for subject sub. Its signature
// is a placeholder: Overcast reads the claims without verifying them.
func oidcIDToken(t *testing.T, sub string) staticIDToken {
	t.Helper()
	claims, err := json.Marshal(map[string]string{"iss": "https://oidc.example.com", "sub": sub, "aud": "sts.amazonaws.com"})
	if err != nil {
		t.Fatal(err)
	}
	enc := base64.RawURLEncoding
	return staticIDToken(enc.EncodeToString([]byte(`{"alg":"RS256"}`)) + "." + enc.EncodeToString(claims) + ".sig")
}

// createWebIdentityRole creates the ci role, trusting oidc.example.com tokens
// for the ci subject, through the IAM API as an administrator.
func createWebIdentityRole(t *testing.T, srv *helpers.TestServer) {
	t.Helper()
	seedIAMPrincipal(t, srv, "test", `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"iam:CreateRole","Resource":"*"}]}`)
	resp := iamCallWithAuth(t, srv, "CreateRole", url.Values{
		"RoleName": {"ci"},
		"AssumeRolePolicyDocument": {`{"Version":"2012-10-17","Statement":[{"Effect":"Allow",
			"Principal":{"Federated":"arn:aws:iam::000000000000:oidc-provider/oidc.example.com"},
			"Action":"sts:AssumeRoleWithWebIdentity",
			"Condition":{"StringEquals":{"oidc.example.com:sub":"ci","oidc.example.com:aud":"sts.amazonaws.com"}}}]}`},
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("CreateRole status = %d", resp.StatusCode)
	}
}

// anonymousSTS is an STS client with no credentials, as the SDK's web
// identity provider uses one.
func anonymousSTS(srv *helpers.TestServer) *sts.Client {
	return sts.NewFromConfig(aws.Config{
		Region:       "us-east-1",
		Credentials:  aws.AnonymousCredentials{},
		BaseEndpoint: aws.String(srv.URL),
		HTTPClient:   http.DefaultClient,
	})
}

func TestIAMEnforceWebIdentity_trustPolicyAllowsTheToken(t *testing.T) {
	// Given: enforcement on, and a role trusting the ci subject's tokens
	srv := helpers.NewTestServer(t, helpers.WithEnforceIAM(true))
	createWebIdentityRole(t, srv)
	provider := stscreds.NewWebIdentityRoleProvider(anonymousSTS(srv), webIdentityRoleARN, oidcIDToken(t, "ci"))

	// When: the SDK assumes the role with a token for that subject
	creds, err := provider.Retrieve(context.Background())

	// Then: it gets the role's session credentials, which name the role
	if err != nil {
		t.Fatalf("Retrieve: %v", err)
	}
	if !strings.HasPrefix(creds.AccessKeyID, "ASIA") {
		t.Fatalf("AccessKeyID = %q, want a session key", creds.AccessKeyID)
	}
	cfg := sdkConfigFor(srv, creds.AccessKeyID)
	out, err := sts.NewFromConfig(cfg).GetCallerIdentity(context.Background(), &sts.GetCallerIdentityInput{})
	if err != nil {
		t.Fatalf("GetCallerIdentity with the session: %v", err)
	}
	if aws.ToString(out.Account) != "000000000000" {
		t.Fatalf("Account = %q, want 000000000000", aws.ToString(out.Account))
	}
}

func TestIAMEnforceWebIdentity_trustPolicyRefusesTheToken(t *testing.T) {
	// Given: enforcement on, and a role trusting only the ci subject's tokens
	srv := helpers.NewTestServer(t, helpers.WithEnforceIAM(true))
	createWebIdentityRole(t, srv)
	provider := stscreds.NewWebIdentityRoleProvider(anonymousSTS(srv), webIdentityRoleARN, oidcIDToken(t, "dev"))

	// When: the SDK assumes the role with a token for another subject
	_, err := provider.Retrieve(context.Background())

	// Then: STS refuses it with AWS's 403 AccessDenied for the call
	var apiErr smithy.APIError
	if !errors.As(err, &apiErr) || apiErr.ErrorCode() != "AccessDenied" {
		t.Fatalf("err = %v, want API error AccessDenied", err)
	}
	if want := "Not authorized to perform sts:AssumeRoleWithWebIdentity"; apiErr.ErrorMessage() != want {
		t.Fatalf("message = %q, want %q", apiErr.ErrorMessage(), want)
	}
	var respErr *smithyhttp.ResponseError
	if !errors.As(err, &respErr) || respErr.HTTPStatusCode() != http.StatusForbidden {
		t.Fatalf("err = %v, want HTTP 403", err)
	}
}
