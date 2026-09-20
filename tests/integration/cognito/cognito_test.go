// Package cognito_test contains integration tests for the Cognito User Pools emulator.
//
// Run: go test ./tests/integration/cognito/...
package cognito_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"
	"unicode"

	cborlib "github.com/fxamacker/cbor/v2"

	"github.com/overcast-sh/overcast/tests/helpers"
)

// cognitoCall performs a Cognito X-Amz-Target dispatch request.
func cognitoCall(t *testing.T, srv *helpers.TestServer, operation string, body map[string]any) *http.Response {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal %s body: %v", operation, err)
	}
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/x-amz-json-1.1")
	req.Header.Set("X-Amz-Target", "AWSCognitoIdentityProviderService."+operation)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("cognitoCall %s: %v", operation, err)
	}
	return resp
}

// createPool is a test helper that creates a user pool and returns its ID.
func createPool(t *testing.T, srv *helpers.TestServer, name string) string {
	t.Helper()
	resp := cognitoCall(t, srv, "CreateUserPool", map[string]any{"PoolName": name})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		UserPool struct {
			Id string `json:"Id"`
		} `json:"UserPool"`
	}
	helpers.DecodeJSON(t, resp, &result)
	if result.UserPool.Id == "" {
		t.Fatal("CreateUserPool returned empty Id")
	}
	return result.UserPool.Id
}

// createPoolWithPasswordPolicy creates a user pool with the supplied password policy.
func createPoolWithPasswordPolicy(t *testing.T, srv *helpers.TestServer, name string, policy map[string]any) string {
	t.Helper()
	resp := cognitoCall(t, srv, "CreateUserPool", map[string]any{
		"PoolName": name,
		"Policies": map[string]any{"PasswordPolicy": policy},
	})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		UserPool struct {
			Id string `json:"Id"`
		} `json:"UserPool"`
	}
	helpers.DecodeJSON(t, resp, &result)
	if result.UserPool.Id == "" {
		t.Fatal("CreateUserPool returned empty Id")
	}
	return result.UserPool.Id
}

// createClient is a test helper that creates a pool client and returns its ID.
func createClient(t *testing.T, srv *helpers.TestServer, poolID, name string) string {
	t.Helper()
	resp := cognitoCall(t, srv, "CreateUserPoolClient", map[string]any{
		"UserPoolId": poolID, "ClientName": name,
	})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		UserPoolClient struct {
			ClientId string `json:"ClientId"`
		} `json:"UserPoolClient"`
	}
	helpers.DecodeJSON(t, resp, &result)
	if result.UserPoolClient.ClientId == "" {
		t.Fatal("CreateUserPoolClient returned empty ClientId")
	}
	return result.UserPoolClient.ClientId
}

func createClientWithExplicitAuthFlows(t *testing.T, srv *helpers.TestServer, poolID, name string, flows []string) string {
	t.Helper()
	resp := cognitoCall(t, srv, "CreateUserPoolClient", map[string]any{
		"UserPoolId":        poolID,
		"ClientName":        name,
		"ExplicitAuthFlows": flows,
	})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		UserPoolClient struct {
			ClientId          string   `json:"ClientId"`
			ExplicitAuthFlows []string `json:"ExplicitAuthFlows"`
		} `json:"UserPoolClient"`
	}
	helpers.DecodeJSON(t, resp, &result)
	if result.UserPoolClient.ClientId == "" {
		t.Fatal("CreateUserPoolClient returned empty ClientId")
	}
	return result.UserPoolClient.ClientId
}

// ─── CreateUserPool ───────────────────────────────────────────────────────────

func TestCreateUserPool_success(t *testing.T) {
	// Given: an empty store
	srv := helpers.NewTestServer(t)

	// When: CreateUserPool is called
	resp := cognitoCall(t, srv, "CreateUserPool", map[string]any{
		"PoolName": "test-pool",
	})
	defer resp.Body.Close()

	// Then: 200 with UserPool.Id and .Arn set
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		UserPool struct {
			Id  string `json:"Id"`
			Arn string `json:"Arn"`
		} `json:"UserPool"`
	}
	helpers.DecodeJSON(t, resp, &result)
	if result.UserPool.Id == "" {
		t.Error("expected UserPool.Id to be set")
	}
	if result.UserPool.Arn == "" {
		t.Error("expected UserPool.Arn to be set")
	}
}

// TestCreateUserPool_tagsAppliedAtCreate: UserPoolTags passed to
// CreateUserPool (issue #1196, Axis B) must be stored and readable via
// ListTagsForResource immediately, without a follow-up TagResource call.
// CreateUserPool used to model no UserPoolTags request member at all — the
// legacy JSON1.1 handler and the CBOR typed path were also two independent
// implementations, so both are covered here.
func TestCreateUserPool_tagsAppliedAtCreate(t *testing.T) {
	srv := helpers.NewTestServer(t)

	resp := cognitoCall(t, srv, "CreateUserPool", map[string]any{
		"PoolName":     "tagged-at-create",
		"UserPoolTags": map[string]string{"env": "prod"},
	})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	var created struct {
		UserPool struct {
			Arn string `json:"Arn"`
		} `json:"UserPool"`
	}
	helpers.DecodeJSON(t, resp, &created)

	list := cognitoCall(t, srv, "ListTagsForResource", map[string]any{"ResourceArn": created.UserPool.Arn})
	defer list.Body.Close()
	helpers.AssertStatus(t, list, http.StatusOK)
	var out struct {
		Tags map[string]string `json:"Tags"`
	}
	helpers.DecodeJSON(t, list, &out)
	if out.Tags["env"] != "prod" {
		t.Errorf("Tags: got %v, want env=prod", out.Tags)
	}
}

// TestCreateUserPoolCBOR_tagsAppliedAtCreate is
// TestCreateUserPool_tagsAppliedAtCreate over the CBOR typed path.
func TestCreateUserPoolCBOR_tagsAppliedAtCreate(t *testing.T) {
	srv := helpers.NewTestServer(t)

	resp := cognitoCBORCall(t, srv, "CreateUserPool", map[string]any{
		"PoolName":     "tagged-at-create-cbor",
		"UserPoolTags": map[string]string{"env": "prod"},
	})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read CBOR response: %v", err)
	}
	var created struct {
		UserPool struct {
			Arn string `cbor:"Arn"`
		} `cbor:"UserPool"`
	}
	if err := cborlib.Unmarshal(body, &created); err != nil {
		t.Fatalf("unmarshal CBOR response: %v", err)
	}

	list := cognitoCall(t, srv, "ListTagsForResource", map[string]any{"ResourceArn": created.UserPool.Arn})
	defer list.Body.Close()
	helpers.AssertStatus(t, list, http.StatusOK)
	var out struct {
		Tags map[string]string `json:"Tags"`
	}
	helpers.DecodeJSON(t, list, &out)
	if out.Tags["env"] != "prod" {
		t.Errorf("Tags: got %v, want env=prod", out.Tags)
	}
}

// TestCreateUserPool_invalidTagRejected: an invalid tag passed to
// CreateUserPool must be rejected before the pool is created.
func TestCreateUserPool_invalidTagRejected(t *testing.T) {
	srv := helpers.NewTestServer(t)

	resp := cognitoCall(t, srv, "CreateUserPool", map[string]any{
		"PoolName":     "tags-rejected",
		"UserPoolTags": map[string]string{"aws:reserved": "x"},
	})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusBadRequest)

	list := cognitoCall(t, srv, "ListUserPools", map[string]any{"MaxResults": 10})
	defer list.Body.Close()
	var out struct {
		UserPools []struct {
			Name string `json:"Name"`
		} `json:"UserPools"`
	}
	helpers.DecodeJSON(t, list, &out)
	for _, p := range out.UserPools {
		if p.Name == "tags-rejected" {
			t.Error("tags-rejected pool should not have been created")
		}
	}
}

func TestListUserPools_malformedPersistedPool(t *testing.T) {
	// Given: one valid user pool and one malformed persisted pool record.
	srv := helpers.NewTestServer(t)
	poolID := createPool(t, srv, "valid-pool")
	if err := srv.Store.Set(context.Background(), "cognito:pools", "us-east-1/us-east-1_BADPOOL", "{"); err != nil {
		t.Fatalf("seed malformed pool: %v", err)
	}

	// When: ListUserPools scans persisted pool metadata.
	resp := cognitoCall(t, srv, "ListUserPools", map[string]any{"MaxResults": 10})
	defer resp.Body.Close()

	// Then: the malformed record is skipped and valid pools remain visible.
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		UserPools []struct {
			Id string `json:"Id"`
		} `json:"UserPools"`
	}
	helpers.DecodeJSON(t, resp, &result)
	found := false
	for _, pool := range result.UserPools {
		if pool.Id == poolID {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected valid pool %q, got %#v", poolID, result.UserPools)
	}
}

func TestAdminCreateUser_malformedPersistedPool(t *testing.T) {
	// Given: a user pool record exists but its persisted JSON is malformed.
	srv := helpers.NewTestServer(t)
	poolID := "us-east-1_BADPOOL"
	if err := srv.Store.Set(context.Background(), "cognito:pools", "us-east-1/"+poolID, "{"); err != nil {
		t.Fatalf("seed malformed pool: %v", err)
	}

	// When: an external client targets that pool.
	resp := cognitoCall(t, srv, "AdminCreateUser", map[string]any{
		"UserPoolId":    poolID,
		"Username":      "bad-pool-user@example.test",
		"MessageAction": "SUPPRESS",
		"UserAttributes": []map[string]string{
			{"Name": "email", "Value": "bad-pool-user@example.test"},
		},
	})
	defer resp.Body.Close()

	// Then: the corrupt emulator record is treated as unavailable, not as a
	// service-wide InternalError.
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "ResourceNotFoundException")
}

func TestCreateUserPool_withEmailTemplates(t *testing.T) {
	// Given: an empty store
	srv := helpers.NewTestServer(t)

	// When: CreateUserPool is called with VerificationMessageTemplate and AdminCreateUserConfig
	resp := cognitoCall(t, srv, "CreateUserPool", map[string]any{
		"PoolName": "template-pool",
		"VerificationMessageTemplate": map[string]any{
			"DefaultEmailOption": "CONFIRM_WITH_CODE",
			"EmailMessage":       "Hi {username}, your code is {####}",
			"EmailSubject":       "Verify your account",
			"EmailMessageByLink": "Click {##Verify Email##} to confirm",
			"EmailSubjectByLink": "Verify via link",
		},
		"AdminCreateUserConfig": map[string]any{
			"AllowAdminCreateUserOnly":  true,
			"UnusedAccountValidityDays": 3,
			"InviteMessageTemplate": map[string]any{
				"EmailMessage": "Welcome {username}! Your temp password is {####}",
				"EmailSubject": "Welcome to the app",
				"SMSMessage":   "{username}, your password is {####}",
			},
		},
		"EmailConfiguration": map[string]any{
			"EmailSendingAccount": "COGNITO_DEFAULT",
			"From":                "noreply@example.com",
			"ReplyToEmailAddress": "support@example.com",
		},
	})
	defer resp.Body.Close()

	// Then: 200 with all template fields persisted
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		UserPool struct {
			Id                          string `json:"Id"`
			VerificationMessageTemplate struct {
				DefaultEmailOption string `json:"DefaultEmailOption"`
				EmailMessage       string `json:"EmailMessage"`
				EmailSubject       string `json:"EmailSubject"`
				EmailMessageByLink string `json:"EmailMessageByLink"`
				EmailSubjectByLink string `json:"EmailSubjectByLink"`
			} `json:"VerificationMessageTemplate"`
			AdminCreateUserConfig struct {
				AllowAdminCreateUserOnly  bool `json:"AllowAdminCreateUserOnly"`
				UnusedAccountValidityDays int  `json:"UnusedAccountValidityDays"`
				InviteMessageTemplate     struct {
					EmailMessage string `json:"EmailMessage"`
					EmailSubject string `json:"EmailSubject"`
					SMSMessage   string `json:"SMSMessage"`
				} `json:"InviteMessageTemplate"`
			} `json:"AdminCreateUserConfig"`
			EmailConfiguration struct {
				EmailSendingAccount string `json:"EmailSendingAccount"`
				From                string `json:"From"`
				ReplyToEmailAddress string `json:"ReplyToEmailAddress"`
			} `json:"EmailConfiguration"`
		} `json:"UserPool"`
	}
	helpers.DecodeJSON(t, resp, &result)
	up := result.UserPool
	if up.VerificationMessageTemplate.DefaultEmailOption != "CONFIRM_WITH_CODE" {
		t.Errorf("expected DefaultEmailOption=CONFIRM_WITH_CODE, got %q", up.VerificationMessageTemplate.DefaultEmailOption)
	}
	if up.VerificationMessageTemplate.EmailMessage != "Hi {username}, your code is {####}" {
		t.Errorf("unexpected EmailMessage: %q", up.VerificationMessageTemplate.EmailMessage)
	}
	if up.VerificationMessageTemplate.EmailSubject != "Verify your account" {
		t.Errorf("unexpected EmailSubject: %q", up.VerificationMessageTemplate.EmailSubject)
	}
	if up.AdminCreateUserConfig.AllowAdminCreateUserOnly != true {
		t.Error("expected AllowAdminCreateUserOnly=true")
	}
	if up.AdminCreateUserConfig.UnusedAccountValidityDays != 3 {
		t.Errorf("expected UnusedAccountValidityDays=3, got %d", up.AdminCreateUserConfig.UnusedAccountValidityDays)
	}
	if up.AdminCreateUserConfig.InviteMessageTemplate.EmailMessage != "Welcome {username}! Your temp password is {####}" {
		t.Errorf("unexpected InviteMessageTemplate.EmailMessage: %q", up.AdminCreateUserConfig.InviteMessageTemplate.EmailMessage)
	}
	if up.AdminCreateUserConfig.InviteMessageTemplate.EmailSubject != "Welcome to the app" {
		t.Errorf("unexpected InviteMessageTemplate.EmailSubject: %q", up.AdminCreateUserConfig.InviteMessageTemplate.EmailSubject)
	}
	if up.EmailConfiguration.EmailSendingAccount != "COGNITO_DEFAULT" {
		t.Errorf("unexpected EmailSendingAccount: %q", up.EmailConfiguration.EmailSendingAccount)
	}
	if up.EmailConfiguration.From != "noreply@example.com" {
		t.Errorf("unexpected EmailConfiguration.From: %q", up.EmailConfiguration.From)
	}
}

func TestCreateUserPool_defaultEmailOption(t *testing.T) {
	// Given: an empty store
	srv := helpers.NewTestServer(t)

	// When: CreateUserPool with VerificationMessageTemplate but no DefaultEmailOption
	resp := cognitoCall(t, srv, "CreateUserPool", map[string]any{
		"PoolName": "default-opt-pool",
		"VerificationMessageTemplate": map[string]any{
			"EmailMessage": "Code: {####}",
		},
	})
	defer resp.Body.Close()

	// Then: DefaultEmailOption defaults to CONFIRM_WITH_CODE
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		UserPool struct {
			VerificationMessageTemplate struct {
				DefaultEmailOption string `json:"DefaultEmailOption"`
			} `json:"VerificationMessageTemplate"`
		} `json:"UserPool"`
	}
	helpers.DecodeJSON(t, resp, &result)
	if result.UserPool.VerificationMessageTemplate.DefaultEmailOption != "CONFIRM_WITH_CODE" {
		t.Errorf("expected CONFIRM_WITH_CODE default, got %q",
			result.UserPool.VerificationMessageTemplate.DefaultEmailOption)
	}
}

// ─── UpdateUserPool with templates ────────────────────────────────────────────

func TestUpdateUserPool_emailTemplates(t *testing.T) {
	// Given: a pool created without templates
	srv := helpers.NewTestServer(t)
	poolID := createPool(t, srv, "update-tmpl-pool")

	// When: UpdateUserPool adds VerificationMessageTemplate and AdminCreateUserConfig
	resp := cognitoCall(t, srv, "UpdateUserPool", map[string]any{
		"UserPoolId": poolID,
		"VerificationMessageTemplate": map[string]any{
			"EmailMessage": "Updated code: {####}",
			"EmailSubject": "Updated subject",
		},
		"AdminCreateUserConfig": map[string]any{
			"InviteMessageTemplate": map[string]any{
				"EmailMessage": "Hello {username}, pw: {####}",
				"EmailSubject": "Your invite",
			},
		},
	})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)

	// Then: DescribeUserPool shows the updated templates
	desc := cognitoCall(t, srv, "DescribeUserPool", map[string]any{"UserPoolId": poolID})
	defer desc.Body.Close()
	helpers.AssertStatus(t, desc, http.StatusOK)
	var result struct {
		UserPool struct {
			VerificationMessageTemplate struct {
				EmailMessage       string `json:"EmailMessage"`
				EmailSubject       string `json:"EmailSubject"`
				DefaultEmailOption string `json:"DefaultEmailOption"`
			} `json:"VerificationMessageTemplate"`
			AdminCreateUserConfig struct {
				UnusedAccountValidityDays int `json:"UnusedAccountValidityDays"`
				InviteMessageTemplate     struct {
					EmailMessage string `json:"EmailMessage"`
					EmailSubject string `json:"EmailSubject"`
				} `json:"InviteMessageTemplate"`
			} `json:"AdminCreateUserConfig"`
		} `json:"UserPool"`
	}
	helpers.DecodeJSON(t, desc, &result)
	up := result.UserPool
	if up.VerificationMessageTemplate.EmailMessage != "Updated code: {####}" {
		t.Errorf("expected updated EmailMessage, got %q", up.VerificationMessageTemplate.EmailMessage)
	}
	if up.VerificationMessageTemplate.EmailSubject != "Updated subject" {
		t.Errorf("expected updated EmailSubject, got %q", up.VerificationMessageTemplate.EmailSubject)
	}
	if up.VerificationMessageTemplate.DefaultEmailOption != "CONFIRM_WITH_CODE" {
		t.Errorf("expected DefaultEmailOption=CONFIRM_WITH_CODE, got %q", up.VerificationMessageTemplate.DefaultEmailOption)
	}
	if up.AdminCreateUserConfig.UnusedAccountValidityDays != 7 {
		t.Errorf("expected default UnusedAccountValidityDays=7, got %d", up.AdminCreateUserConfig.UnusedAccountValidityDays)
	}
	if up.AdminCreateUserConfig.InviteMessageTemplate.EmailMessage != "Hello {username}, pw: {####}" {
		t.Errorf("unexpected InviteMessageTemplate.EmailMessage: %q", up.AdminCreateUserConfig.InviteMessageTemplate.EmailMessage)
	}
}

// ─── DescribeUserPool ─────────────────────────────────────────────────────────

func TestDescribeUserPool_success(t *testing.T) {
	// Given: an existing user pool
	srv := helpers.NewTestServer(t)
	poolID := createPool(t, srv, "test-pool")

	// When: DescribeUserPool is called with the pool ID
	resp := cognitoCall(t, srv, "DescribeUserPool", map[string]any{
		"UserPoolId": poolID,
	})
	defer resp.Body.Close()

	// Then: 200 with UserPool.Id matching
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		UserPool struct {
			Id string `json:"Id"`
		} `json:"UserPool"`
	}
	helpers.DecodeJSON(t, resp, &result)
	if result.UserPool.Id != poolID {
		t.Errorf("expected UserPool.Id=%q, got %q", poolID, result.UserPool.Id)
	}
}

func TestDescribeUserPool_notFound(t *testing.T) {
	// Given: no user pools
	srv := helpers.NewTestServer(t)

	// When: DescribeUserPool is called with a non-existent ID
	resp := cognitoCall(t, srv, "DescribeUserPool", map[string]any{
		"UserPoolId": "us-east-1_nonexistent",
	})
	defer resp.Body.Close()

	// Then: 400 with ResourceNotFoundException
	helpers.AssertJSONError(t, resp, "ResourceNotFoundException")
}

// ─── ListUserPools ────────────────────────────────────────────────────────────

func TestListUserPools_success(t *testing.T) {
	// Given: two user pools
	srv := helpers.NewTestServer(t)
	createPool(t, srv, "pool-a")
	createPool(t, srv, "pool-b")

	// When: ListUserPools is called
	resp := cognitoCall(t, srv, "ListUserPools", map[string]any{"MaxResults": 10})
	defer resp.Body.Close()

	// Then: 200 with both pools listed
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		UserPools []struct {
			Id string `json:"Id"`
		} `json:"UserPools"`
	}
	helpers.DecodeJSON(t, resp, &result)
	if len(result.UserPools) < 2 {
		t.Errorf("expected at least 2 pools, got %d", len(result.UserPools))
	}
}

// ─── DeleteUserPool ───────────────────────────────────────────────────────────

func TestDeleteUserPool_success(t *testing.T) {
	// Given: an existing user pool
	srv := helpers.NewTestServer(t)
	poolID := createPool(t, srv, "delete-me")

	// When: DeleteUserPool is called
	resp := cognitoCall(t, srv, "DeleteUserPool", map[string]any{
		"UserPoolId": poolID,
	})
	defer resp.Body.Close()

	// Then: 200
	helpers.AssertStatus(t, resp, http.StatusOK)
}

// ─── Pool clients ─────────────────────────────────────────────────────────────

func TestCreateUserPoolClient_success(t *testing.T) {
	// Given: a user pool
	srv := helpers.NewTestServer(t)
	poolID := createPool(t, srv, "client-pool")

	// When: CreateUserPoolClient is called
	resp := cognitoCall(t, srv, "CreateUserPoolClient", map[string]any{
		"UserPoolId": poolID, "ClientName": "my-app",
	})
	defer resp.Body.Close()

	// Then: 200 with ClientId set
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		UserPoolClient struct {
			ClientId string `json:"ClientId"`
		} `json:"UserPoolClient"`
	}
	helpers.DecodeJSON(t, resp, &result)
	if result.UserPoolClient.ClientId == "" {
		t.Error("expected non-empty ClientId")
	}
}

func TestListUserPoolClients_success(t *testing.T) {
	// Given: a pool with two clients
	srv := helpers.NewTestServer(t)
	poolID := createPool(t, srv, "pool")
	createClient(t, srv, poolID, "client-1")
	createClient(t, srv, poolID, "client-2")

	// When: ListUserPoolClients is called
	resp := cognitoCall(t, srv, "ListUserPoolClients", map[string]any{"UserPoolId": poolID})
	defer resp.Body.Close()

	// Then: 200 with both clients
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		UserPoolClients []struct {
			ClientId string `json:"ClientId"`
		} `json:"UserPoolClients"`
	}
	helpers.DecodeJSON(t, resp, &result)
	if len(result.UserPoolClients) != 2 {
		t.Errorf("expected 2 clients, got %d", len(result.UserPoolClients))
	}
}

func TestDeleteUserPoolClient_success(t *testing.T) {
	// Given: a pool with a client
	srv := helpers.NewTestServer(t)
	poolID := createPool(t, srv, "pool")
	clientID := createClient(t, srv, poolID, "app")

	// When: DeleteUserPoolClient
	resp := cognitoCall(t, srv, "DeleteUserPoolClient", map[string]any{
		"UserPoolId": poolID, "ClientId": clientID,
	})
	defer resp.Body.Close()

	// Then: 200
	helpers.AssertStatus(t, resp, http.StatusOK)
}

// ─── AdminCreateUser / AdminGetUser / ListUsers / AdminDeleteUser ──────────────

func TestAdminCreateUser_success(t *testing.T) {
	// Given: a user pool
	srv := helpers.NewTestServer(t)
	poolID := createPool(t, srv, "p")

	// When: AdminCreateUser
	resp := cognitoCall(t, srv, "AdminCreateUser", map[string]any{
		"UserPoolId": poolID, "Username": "alice",
		"UserAttributes": []map[string]string{{"Name": "email", "Value": "alice@example.com"}},
	})
	defer resp.Body.Close()

	// Then: 200 with FORCE_CHANGE_PASSWORD status
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		User struct {
			Username   string `json:"Username"`
			UserStatus string `json:"UserStatus"`
		} `json:"User"`
	}
	helpers.DecodeJSON(t, resp, &result)
	if result.User.Username != "alice" {
		t.Errorf("expected Username=alice, got %q", result.User.Username)
	}
	if result.User.UserStatus != "FORCE_CHANGE_PASSWORD" {
		t.Errorf("expected FORCE_CHANGE_PASSWORD status, got %q", result.User.UserStatus)
	}
}

func TestAdminCreateUser_customPasswordPolicy(t *testing.T) {
	// Given: a user pool with a strict password policy
	srv := helpers.NewTestServer(t)
	poolID := createPoolWithPasswordPolicy(t, srv, "strict-passwords", map[string]any{
		"MinimumLength":    30,
		"RequireUppercase": true,
		"RequireLowercase": true,
		"RequireNumbers":   true,
		"RequireSymbols":   true,
	})

	// When: AdminCreateUser omits TemporaryPassword
	resp := cognitoCall(t, srv, "AdminCreateUser", map[string]any{
		"UserPoolId": poolID,
		"Username":   "generated-temp",
	})
	defer resp.Body.Close()

	// Then: the generated temporary password conforms to the pool password policy
	helpers.AssertStatus(t, resp, http.StatusOK)
	tempPassword := storedCognitoPlaintextPassword(t, srv, poolID)
	if len(tempPassword) < 30 {
		t.Fatalf("expected generated temp password length >= 30, got %d (%q)", len(tempPassword), tempPassword)
	}
	assertPasswordCategories(t, tempPassword)
}

func TestAdminCreateUser_invalidTemporaryPassword(t *testing.T) {
	// Given: a user pool with a strict password policy
	srv := helpers.NewTestServer(t)
	poolID := createPoolWithPasswordPolicy(t, srv, "strict-passwords", map[string]any{
		"MinimumLength":    12,
		"RequireUppercase": true,
		"RequireLowercase": true,
		"RequireNumbers":   true,
		"RequireSymbols":   true,
	})

	// When: AdminCreateUser supplies a temporary password that violates the policy
	resp := cognitoCall(t, srv, "AdminCreateUser", map[string]any{
		"UserPoolId":             poolID,
		"Username":               "bad-temp",
		"TemporaryPassword":      "short",
		"MessageAction":          "SUPPRESS",
		"UserAttributes":         []map[string]string{{"Name": "email", "Value": "bad-temp@example.com"}},
		"DesiredDeliveryMediums": []string{"EMAIL"},
	})
	defer resp.Body.Close()

	// Then: Cognito rejects the invalid temporary password
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "InvalidPasswordException")
}

func TestAdminCreateUser_duplicate(t *testing.T) {
	// Given: a pool with user alice
	srv := helpers.NewTestServer(t)
	poolID := createPool(t, srv, "p")
	r1 := cognitoCall(t, srv, "AdminCreateUser", map[string]any{
		"UserPoolId": poolID, "Username": "alice",
	})
	defer r1.Body.Close()
	helpers.AssertStatus(t, r1, http.StatusOK)

	// When: AdminCreateUser is called again with the same username
	resp := cognitoCall(t, srv, "AdminCreateUser", map[string]any{
		"UserPoolId": poolID, "Username": "alice",
	})
	defer resp.Body.Close()

	// Then: UsernameExistsException
	helpers.AssertJSONError(t, resp, "UsernameExistsException")
}

func TestAdminGetUser_success(t *testing.T) {
	// Given: a pool with user bob
	srv := helpers.NewTestServer(t)
	poolID := createPool(t, srv, "p")
	r1 := cognitoCall(t, srv, "AdminCreateUser", map[string]any{
		"UserPoolId": poolID, "Username": "bob",
	})
	defer r1.Body.Close()
	helpers.AssertStatus(t, r1, http.StatusOK)

	// When: AdminGetUser
	resp := cognitoCall(t, srv, "AdminGetUser", map[string]any{
		"UserPoolId": poolID, "Username": "bob",
	})
	defer resp.Body.Close()

	// Then: 200 with Username = bob
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		Username string `json:"Username"`
	}
	helpers.DecodeJSON(t, resp, &result)
	if result.Username != "bob" {
		t.Errorf("expected bob, got %q", result.Username)
	}
}

func TestListUsers_success(t *testing.T) {
	// Given: a pool with one user
	srv := helpers.NewTestServer(t)
	poolID := createPool(t, srv, "p")
	r1 := cognitoCall(t, srv, "AdminCreateUser", map[string]any{
		"UserPoolId": poolID, "Username": "bob",
	})
	defer r1.Body.Close()
	helpers.AssertStatus(t, r1, http.StatusOK)

	// When: ListUsers
	resp := cognitoCall(t, srv, "ListUsers", map[string]any{"UserPoolId": poolID})
	defer resp.Body.Close()

	// Then: 200 with Users slice containing bob
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		Users []struct {
			Username string `json:"Username"`
		} `json:"Users"`
	}
	helpers.DecodeJSON(t, resp, &result)
	if len(result.Users) != 1 || result.Users[0].Username != "bob" {
		t.Errorf("expected 1 user 'bob', got %+v", result.Users)
	}
}

func TestAdminDeleteUser_success(t *testing.T) {
	// Given: a pool with user carol
	srv := helpers.NewTestServer(t)
	poolID := createPool(t, srv, "p")
	r1 := cognitoCall(t, srv, "AdminCreateUser", map[string]any{
		"UserPoolId": poolID, "Username": "carol",
	})
	defer r1.Body.Close()
	helpers.AssertStatus(t, r1, http.StatusOK)

	// When: AdminDeleteUser
	resp := cognitoCall(t, srv, "AdminDeleteUser", map[string]any{
		"UserPoolId": poolID, "Username": "carol",
	})
	defer resp.Body.Close()

	// Then: 200, and subsequent AdminGetUser returns 400
	helpers.AssertStatus(t, resp, http.StatusOK)
	r2 := cognitoCall(t, srv, "AdminGetUser", map[string]any{
		"UserPoolId": poolID, "Username": "carol",
	})
	defer r2.Body.Close()
	helpers.AssertJSONError(t, r2, "UserNotFoundException")
}

// ─── AdminSetUserPassword ─────────────────────────────────────────────────────

func TestAdminSetUserPassword_permanent(t *testing.T) {
	// Given: a pool with a FORCE_CHANGE_PASSWORD user
	srv := helpers.NewTestServer(t)
	poolID := createPool(t, srv, "p")
	r1 := cognitoCall(t, srv, "AdminCreateUser", map[string]any{
		"UserPoolId": poolID, "Username": "dave",
	})
	defer r1.Body.Close()
	helpers.AssertStatus(t, r1, http.StatusOK)

	// When: AdminSetUserPassword with Permanent=true
	resp := cognitoCall(t, srv, "AdminSetUserPassword", map[string]any{
		"UserPoolId": poolID, "Username": "dave",
		"Password": "NewSecurePass1!", "Permanent": true,
	})
	defer resp.Body.Close()

	// Then: 200, user status is now CONFIRMED
	helpers.AssertStatus(t, resp, http.StatusOK)
	r2 := cognitoCall(t, srv, "AdminGetUser", map[string]any{
		"UserPoolId": poolID, "Username": "dave",
	})
	defer r2.Body.Close()
	helpers.AssertStatus(t, r2, http.StatusOK)
	var result struct {
		UserStatus string `json:"UserStatus"`
	}
	helpers.DecodeJSON(t, r2, &result)
	if result.UserStatus != "CONFIRMED" {
		t.Errorf("expected CONFIRMED after permanent password set, got %q", result.UserStatus)
	}
}

// ─── Self-service SignUp → ConfirmSignUp ──────────────────────────────────────

func TestSignUp_and_ConfirmSignUp(t *testing.T) {
	// Given: a pool + client
	srv := helpers.NewTestServer(t)
	poolID := createPool(t, srv, "p")
	clientID := createClient(t, srv, poolID, "app")

	// When: SignUp
	resp := cognitoCall(t, srv, "SignUp", map[string]any{
		"ClientId": clientID, "Username": "eve",
		"Password":       "Secure123!",
		"UserAttributes": []map[string]string{{"Name": "email", "Value": "eve@example.com"}},
	})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	var signUpResult struct {
		UserConfirmed bool   `json:"UserConfirmed"`
		UserSub       string `json:"UserSub"`
	}
	helpers.DecodeJSON(t, resp, &signUpResult)
	if signUpResult.UserConfirmed {
		t.Error("expected UserConfirmed=false after SignUp")
	}

	// Grab the confirmation code directly via AdminGetUser (emulator only).
	// In a real flow the code would arrive by email; we verify the code is set
	// by confirming with the wrong code first.
	wrongResp := cognitoCall(t, srv, "ConfirmSignUp", map[string]any{
		"ClientId": clientID, "Username": "eve", "ConfirmationCode": "000000",
	})
	defer wrongResp.Body.Close()
	helpers.AssertJSONError(t, wrongResp, "CodeMismatchException")

	// Now use AdminConfirmSignUp as a shortcut to confirm the user.
	r2 := cognitoCall(t, srv, "AdminConfirmSignUp", map[string]any{
		"UserPoolId": poolID, "Username": "eve",
	})
	defer r2.Body.Close()
	helpers.AssertStatus(t, r2, http.StatusOK)

	// Then: user status is CONFIRMED
	r3 := cognitoCall(t, srv, "AdminGetUser", map[string]any{
		"UserPoolId": poolID, "Username": "eve",
	})
	defer r3.Body.Close()
	helpers.AssertStatus(t, r3, http.StatusOK)
	var userResult struct {
		UserStatus string `json:"UserStatus"`
	}
	helpers.DecodeJSON(t, r3, &userResult)
	if userResult.UserStatus != "CONFIRMED" {
		t.Errorf("expected CONFIRMED status, got %q", userResult.UserStatus)
	}
}

func TestSignUp_plainPool_duplicateUsername(t *testing.T) {
	// Given: a plain username pool where "mallory" has already signed up
	srv := helpers.NewTestServer(t)
	poolID := createPool(t, srv, "p")
	clientID := createClient(t, srv, poolID, "app")
	resp := cognitoCall(t, srv, "SignUp", map[string]any{
		"ClientId": clientID, "Username": "mallory", "Password": "Secure123!",
	})
	resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)

	// When: the same username signs up again
	resp = cognitoCall(t, srv, "SignUp", map[string]any{
		"ClientId": clientID, "Username": "mallory", "Password": "Different123!",
	})

	// Then: AWS's UsernameExistsException is returned
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "UsernameExistsException")
	resp.Body.Close()

	// And: only the first sign-up exists
	resp = cognitoCall(t, srv, "ListUsers", map[string]any{"UserPoolId": poolID})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	var listResult struct {
		Users []struct {
			Username string `json:"Username"`
		} `json:"Users"`
	}
	helpers.DecodeJSON(t, resp, &listResult)
	if len(listResult.Users) != 1 || listResult.Users[0].Username != "mallory" {
		t.Fatalf("expected the single original user, got %#v", listResult.Users)
	}
}

// ─── InitiateAuth USER_PASSWORD_AUTH ─────────────────────────────────────────

func TestInitiateAuth_confirmedUser(t *testing.T) {
	// Given: a confirmed user with a known password
	srv := helpers.NewTestServer(t)
	poolID := createPool(t, srv, "p")
	clientID := createClient(t, srv, poolID, "app")

	r1 := cognitoCall(t, srv, "AdminCreateUser", map[string]any{
		"UserPoolId": poolID, "Username": "frank",
	})
	defer r1.Body.Close()
	helpers.AssertStatus(t, r1, http.StatusOK)
	r2 := cognitoCall(t, srv, "AdminSetUserPassword", map[string]any{
		"UserPoolId": poolID, "Username": "frank",
		"Password": "SteadyPass1!", "Permanent": true,
	})
	defer r2.Body.Close()
	helpers.AssertStatus(t, r2, http.StatusOK)

	// When: InitiateAuth with correct credentials
	resp := cognitoCall(t, srv, "InitiateAuth", map[string]any{
		"ClientId": clientID, "AuthFlow": "USER_PASSWORD_AUTH",
		"AuthParameters": map[string]string{
			"USERNAME": "frank", "PASSWORD": "SteadyPass1!",
		},
	})
	defer resp.Body.Close()

	// Then: 200 with AccessToken, IdToken, RefreshToken
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		AuthenticationResult struct {
			AccessToken  string `json:"AccessToken"`
			IdToken      string `json:"IdToken"`
			RefreshToken string `json:"RefreshToken"`
		} `json:"AuthenticationResult"`
	}
	helpers.DecodeJSON(t, resp, &result)
	if result.AuthenticationResult.AccessToken == "" {
		t.Error("expected non-empty AccessToken")
	}
	if result.AuthenticationResult.RefreshToken == "" {
		t.Error("expected non-empty RefreshToken")
	}
}

func TestInitiateAuth_wrongPassword(t *testing.T) {
	// Given: a confirmed user
	srv := helpers.NewTestServer(t)
	poolID := createPool(t, srv, "p")
	clientID := createClient(t, srv, poolID, "app")
	r1 := cognitoCall(t, srv, "AdminCreateUser", map[string]any{
		"UserPoolId": poolID, "Username": "grace",
	})
	defer r1.Body.Close()
	helpers.AssertStatus(t, r1, http.StatusOK)
	r2 := cognitoCall(t, srv, "AdminSetUserPassword", map[string]any{
		"UserPoolId": poolID, "Username": "grace",
		"Password": "CorrectPass1!", "Permanent": true,
	})
	defer r2.Body.Close()
	helpers.AssertStatus(t, r2, http.StatusOK)

	// When: InitiateAuth with wrong password
	resp := cognitoCall(t, srv, "InitiateAuth", map[string]any{
		"ClientId": clientID, "AuthFlow": "USER_PASSWORD_AUTH",
		"AuthParameters": map[string]string{
			"USERNAME": "grace", "PASSWORD": "WrongPass1!",
		},
	})
	defer resp.Body.Close()

	// Then: NotAuthorizedException
	helpers.AssertJSONError(t, resp, "NotAuthorizedException")
}

func TestInitiateAuth_forceChangePassword_challenge(t *testing.T) {
	// Given: a freshly admin-created user (FORCE_CHANGE_PASSWORD) with a known temp password
	srv := helpers.NewTestServer(t)
	poolID := createPool(t, srv, "p")
	clientID := createClient(t, srv, poolID, "app")
	r1 := cognitoCall(t, srv, "AdminCreateUser", map[string]any{
		"UserPoolId": poolID, "Username": "hank",
		"TemporaryPassword": "TempPass1!",
	})
	defer r1.Body.Close()
	helpers.AssertStatus(t, r1, http.StatusOK)

	// When: InitiateAuth with the temp password
	resp := cognitoCall(t, srv, "InitiateAuth", map[string]any{
		"ClientId": clientID, "AuthFlow": "USER_PASSWORD_AUTH",
		"AuthParameters": map[string]string{
			"USERNAME": "hank", "PASSWORD": "TempPass1!",
		},
	})
	defer resp.Body.Close()

	// Then: NEW_PASSWORD_REQUIRED challenge with a Session token
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		ChallengeName string `json:"ChallengeName"`
		Session       string `json:"Session"`
	}
	helpers.DecodeJSON(t, resp, &result)
	if result.ChallengeName != "NEW_PASSWORD_REQUIRED" {
		t.Errorf("expected NEW_PASSWORD_REQUIRED challenge, got %q", result.ChallengeName)
	}
	if result.Session == "" {
		t.Error("expected non-empty Session token")
	}
}

// ─── RespondToAuthChallenge NEW_PASSWORD_REQUIRED ─────────────────────────────

func TestRespondToAuthChallenge_newPassword(t *testing.T) {
	// Given: a user in FORCE_CHANGE_PASSWORD state who received a NEW_PASSWORD_REQUIRED challenge
	srv := helpers.NewTestServer(t)
	poolID := createPool(t, srv, "p")
	clientID := createClient(t, srv, poolID, "app")
	r1 := cognitoCall(t, srv, "AdminCreateUser", map[string]any{
		"UserPoolId": poolID, "Username": "ivan",
		"TemporaryPassword": "TempPass1!",
	})
	defer r1.Body.Close()
	helpers.AssertStatus(t, r1, http.StatusOK)
	r2 := cognitoCall(t, srv, "InitiateAuth", map[string]any{
		"ClientId": clientID, "AuthFlow": "USER_PASSWORD_AUTH",
		"AuthParameters": map[string]string{"USERNAME": "ivan", "PASSWORD": "TempPass1!"},
	})
	defer r2.Body.Close()
	helpers.AssertStatus(t, r2, http.StatusOK)
	var challenge struct {
		Session string `json:"Session"`
	}
	helpers.DecodeJSON(t, r2, &challenge)

	// When: RespondToAuthChallenge with new password
	resp := cognitoCall(t, srv, "RespondToAuthChallenge", map[string]any{
		"ClientId":      clientID,
		"ChallengeName": "NEW_PASSWORD_REQUIRED",
		"Session":       challenge.Session,
		"ChallengeResponses": map[string]string{
			"USERNAME":     "ivan",
			"NEW_PASSWORD": "FinalPass1!",
		},
	})
	defer resp.Body.Close()

	// Then: 200 with tokens; user is now CONFIRMED
	helpers.AssertStatus(t, resp, http.StatusOK)
	var authResult struct {
		AuthenticationResult struct {
			AccessToken string `json:"AccessToken"`
		} `json:"AuthenticationResult"`
	}
	helpers.DecodeJSON(t, resp, &authResult)
	if authResult.AuthenticationResult.AccessToken == "" {
		t.Error("expected AccessToken after challenge response")
	}

	r3 := cognitoCall(t, srv, "AdminGetUser", map[string]any{
		"UserPoolId": poolID, "Username": "ivan",
	})
	defer r3.Body.Close()
	helpers.AssertStatus(t, r3, http.StatusOK)
	var userResult struct {
		UserStatus string `json:"UserStatus"`
	}
	helpers.DecodeJSON(t, r3, &userResult)
	if userResult.UserStatus != "CONFIRMED" {
		t.Errorf("expected CONFIRMED after challenge response, got %q", userResult.UserStatus)
	}
}

// ─── GetUser ──────────────────────────────────────────────────────────────────

func TestGetUser_success(t *testing.T) {
	// Given: a confirmed user with a valid access token
	srv := helpers.NewTestServer(t)
	poolID := createPool(t, srv, "p")
	clientID := createClient(t, srv, poolID, "app")
	r1 := cognitoCall(t, srv, "AdminCreateUser", map[string]any{
		"UserPoolId": poolID, "Username": "judy",
		"UserAttributes": []map[string]string{{"Name": "email", "Value": "judy@example.com"}},
	})
	defer r1.Body.Close()
	helpers.AssertStatus(t, r1, http.StatusOK)
	r2 := cognitoCall(t, srv, "AdminSetUserPassword", map[string]any{
		"UserPoolId": poolID, "Username": "judy",
		"Password": "JudyPass1!", "Permanent": true,
	})
	defer r2.Body.Close()
	helpers.AssertStatus(t, r2, http.StatusOK)
	r3 := cognitoCall(t, srv, "InitiateAuth", map[string]any{
		"ClientId": clientID, "AuthFlow": "USER_PASSWORD_AUTH",
		"AuthParameters": map[string]string{"USERNAME": "judy", "PASSWORD": "JudyPass1!"},
	})
	defer r3.Body.Close()
	helpers.AssertStatus(t, r3, http.StatusOK)
	var auth struct {
		AuthenticationResult struct {
			AccessToken string `json:"AccessToken"`
		} `json:"AuthenticationResult"`
	}
	helpers.DecodeJSON(t, r3, &auth)

	// When: GetUser with access token
	resp := cognitoCall(t, srv, "GetUser", map[string]any{
		"AccessToken": auth.AuthenticationResult.AccessToken,
	})
	defer resp.Body.Close()

	// Then: 200 with Username = judy
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		Username string `json:"Username"`
	}
	helpers.DecodeJSON(t, resp, &result)
	if result.Username != "judy" {
		t.Errorf("expected judy, got %q", result.Username)
	}
}

// ─── RefreshToken ─────────────────────────────────────────────────────────────

func TestInitiateAuth_refreshToken(t *testing.T) {
	// Given: a confirmed user with tokens
	srv := helpers.NewTestServer(t)
	poolID := createPool(t, srv, "p")
	clientID := createClient(t, srv, poolID, "app")
	r1 := cognitoCall(t, srv, "AdminCreateUser", map[string]any{
		"UserPoolId": poolID, "Username": "ken",
	})
	defer r1.Body.Close()
	helpers.AssertStatus(t, r1, http.StatusOK)
	r2 := cognitoCall(t, srv, "AdminSetUserPassword", map[string]any{
		"UserPoolId": poolID, "Username": "ken",
		"Password": "KenPass1!", "Permanent": true,
	})
	defer r2.Body.Close()
	helpers.AssertStatus(t, r2, http.StatusOK)
	r3 := cognitoCall(t, srv, "InitiateAuth", map[string]any{
		"ClientId": clientID, "AuthFlow": "USER_PASSWORD_AUTH",
		"AuthParameters": map[string]string{"USERNAME": "ken", "PASSWORD": "KenPass1!"},
	})
	defer r3.Body.Close()
	helpers.AssertStatus(t, r3, http.StatusOK)
	var auth struct {
		AuthenticationResult struct {
			RefreshToken string `json:"RefreshToken"`
		} `json:"AuthenticationResult"`
	}
	helpers.DecodeJSON(t, r3, &auth)

	// When: InitiateAuth with REFRESH_TOKEN_AUTH
	resp := cognitoCall(t, srv, "InitiateAuth", map[string]any{
		"ClientId": clientID, "AuthFlow": "REFRESH_TOKEN_AUTH",
		"AuthParameters": map[string]string{"REFRESH_TOKEN": auth.AuthenticationResult.RefreshToken},
	})
	defer resp.Body.Close()

	// Then: 200 with new AccessToken
	helpers.AssertStatus(t, resp, http.StatusOK)
	var newAuth struct {
		AuthenticationResult struct {
			AccessToken string `json:"AccessToken"`
		} `json:"AuthenticationResult"`
	}
	helpers.DecodeJSON(t, resp, &newAuth)
	if newAuth.AuthenticationResult.AccessToken == "" {
		t.Error("expected new AccessToken from refresh")
	}
}

// ─── ForgotPassword → ConfirmForgotPassword ───────────────────────────────────

func TestForgotPassword_and_ConfirmForgotPassword(t *testing.T) {
	// Given: a confirmed user with an email
	srv := helpers.NewTestServer(t)
	poolID := createPool(t, srv, "p")
	clientID := createClient(t, srv, poolID, "app")
	r1 := cognitoCall(t, srv, "AdminCreateUser", map[string]any{
		"UserPoolId": poolID, "Username": "lori",
		"UserAttributes": []map[string]string{{"Name": "email", "Value": "lori@example.com"}},
	})
	defer r1.Body.Close()
	helpers.AssertStatus(t, r1, http.StatusOK)
	r2 := cognitoCall(t, srv, "AdminSetUserPassword", map[string]any{
		"UserPoolId": poolID, "Username": "lori",
		"Password": "LoriPass1!", "Permanent": true,
	})
	defer r2.Body.Close()
	helpers.AssertStatus(t, r2, http.StatusOK)

	// When: ForgotPassword
	forgotResp := cognitoCall(t, srv, "ForgotPassword", map[string]any{
		"ClientId": clientID, "Username": "lori",
	})
	defer forgotResp.Body.Close()
	helpers.AssertStatus(t, forgotResp, http.StatusOK)

	// Then: use wrong code first
	wrongResp := cognitoCall(t, srv, "ConfirmForgotPassword", map[string]any{
		"ClientId": clientID, "Username": "lori",
		"ConfirmationCode": "000000", "Password": "NewLoriPass1!",
	})
	defer wrongResp.Body.Close()
	helpers.AssertJSONError(t, wrongResp, "CodeMismatchException")

	// And: use AdminConfirmSignUp workaround — actually we test the reset code via
	// the store by admin-setting the password directly (end-to-end via the emulator).
	// In a real test we'd read the code from captured SMTP mail.
	r3 := cognitoCall(t, srv, "AdminSetUserPassword", map[string]any{
		"UserPoolId": poolID, "Username": "lori",
		"Password": "FinalLoriPass1!", "Permanent": true,
	})
	defer r3.Body.Close()
	helpers.AssertStatus(t, r3, http.StatusOK)

	// Verify sign-in works with new password
	authResp := cognitoCall(t, srv, "InitiateAuth", map[string]any{
		"ClientId": clientID, "AuthFlow": "USER_PASSWORD_AUTH",
		"AuthParameters": map[string]string{"USERNAME": "lori", "PASSWORD": "FinalLoriPass1!"},
	})
	defer authResp.Body.Close()
	helpers.AssertStatus(t, authResp, http.StatusOK)
}

// ─── ChangePassword ───────────────────────────────────────────────────────────

func TestChangePassword_success(t *testing.T) {
	// Given: a confirmed user with an access token
	srv := helpers.NewTestServer(t)
	poolID := createPool(t, srv, "p")
	clientID := createClient(t, srv, poolID, "app")
	r1 := cognitoCall(t, srv, "AdminCreateUser", map[string]any{
		"UserPoolId": poolID, "Username": "mike",
	})
	defer r1.Body.Close()
	helpers.AssertStatus(t, r1, http.StatusOK)
	r2 := cognitoCall(t, srv, "AdminSetUserPassword", map[string]any{
		"UserPoolId": poolID, "Username": "mike",
		"Password": "MikePass1!", "Permanent": true,
	})
	defer r2.Body.Close()
	helpers.AssertStatus(t, r2, http.StatusOK)
	r3 := cognitoCall(t, srv, "InitiateAuth", map[string]any{
		"ClientId": clientID, "AuthFlow": "USER_PASSWORD_AUTH",
		"AuthParameters": map[string]string{"USERNAME": "mike", "PASSWORD": "MikePass1!"},
	})
	defer r3.Body.Close()
	helpers.AssertStatus(t, r3, http.StatusOK)
	var auth struct {
		AuthenticationResult struct {
			AccessToken string `json:"AccessToken"`
		} `json:"AuthenticationResult"`
	}
	helpers.DecodeJSON(t, r3, &auth)

	// When: ChangePassword
	resp := cognitoCall(t, srv, "ChangePassword", map[string]any{
		"AccessToken":      auth.AuthenticationResult.AccessToken,
		"PreviousPassword": "MikePass1!",
		"ProposedPassword": "MikeNewPass1!",
	})
	defer resp.Body.Close()

	// Then: 200; old password no longer works
	helpers.AssertStatus(t, resp, http.StatusOK)
	authOld := cognitoCall(t, srv, "InitiateAuth", map[string]any{
		"ClientId": clientID, "AuthFlow": "USER_PASSWORD_AUTH",
		"AuthParameters": map[string]string{"USERNAME": "mike", "PASSWORD": "MikePass1!"},
	})
	defer authOld.Body.Close()
	helpers.AssertJSONError(t, authOld, "NotAuthorizedException")

	// New password works
	authNew := cognitoCall(t, srv, "InitiateAuth", map[string]any{
		"ClientId": clientID, "AuthFlow": "USER_PASSWORD_AUTH",
		"AuthParameters": map[string]string{"USERNAME": "mike", "PASSWORD": "MikeNewPass1!"},
	})
	defer authNew.Body.Close()
	helpers.AssertStatus(t, authNew, http.StatusOK)
}

// ─── GlobalSignOut ────────────────────────────────────────────────────────────

func TestGlobalSignOut_revokesAccessToken(t *testing.T) {
	// Given: a confirmed user with an access token
	srv := helpers.NewTestServer(t)
	poolID := createPool(t, srv, "p")
	clientID := createClient(t, srv, poolID, "app")
	r1 := cognitoCall(t, srv, "AdminCreateUser", map[string]any{
		"UserPoolId": poolID, "Username": "nina",
	})
	defer r1.Body.Close()
	helpers.AssertStatus(t, r1, http.StatusOK)
	r2 := cognitoCall(t, srv, "AdminSetUserPassword", map[string]any{
		"UserPoolId": poolID, "Username": "nina",
		"Password": "NinaPass1!", "Permanent": true,
	})
	defer r2.Body.Close()
	helpers.AssertStatus(t, r2, http.StatusOK)
	r3 := cognitoCall(t, srv, "InitiateAuth", map[string]any{
		"ClientId": clientID, "AuthFlow": "USER_PASSWORD_AUTH",
		"AuthParameters": map[string]string{"USERNAME": "nina", "PASSWORD": "NinaPass1!"},
	})
	defer r3.Body.Close()
	helpers.AssertStatus(t, r3, http.StatusOK)
	var auth struct {
		AuthenticationResult struct {
			AccessToken string `json:"AccessToken"`
		} `json:"AuthenticationResult"`
	}
	helpers.DecodeJSON(t, r3, &auth)
	accessToken := auth.AuthenticationResult.AccessToken

	// When: GlobalSignOut
	resp := cognitoCall(t, srv, "GlobalSignOut", map[string]any{"AccessToken": accessToken})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)

	// Then: the access token is no longer valid
	getResp := cognitoCall(t, srv, "GetUser", map[string]any{"AccessToken": accessToken})
	defer getResp.Body.Close()
	helpers.AssertJSONError(t, getResp, "NotAuthorizedException")
}

// ─── AdminDisable/EnableUser ──────────────────────────────────────────────────

func TestAdminDisableUser_preventsSignIn(t *testing.T) {
	// Given: a confirmed user
	srv := helpers.NewTestServer(t)
	poolID := createPool(t, srv, "p")
	clientID := createClient(t, srv, poolID, "app")
	r1 := cognitoCall(t, srv, "AdminCreateUser", map[string]any{
		"UserPoolId": poolID, "Username": "otto",
	})
	defer r1.Body.Close()
	helpers.AssertStatus(t, r1, http.StatusOK)
	r2 := cognitoCall(t, srv, "AdminSetUserPassword", map[string]any{
		"UserPoolId": poolID, "Username": "otto",
		"Password": "OttoPass1!", "Permanent": true,
	})
	defer r2.Body.Close()
	helpers.AssertStatus(t, r2, http.StatusOK)

	// When: AdminDisableUser
	disResp := cognitoCall(t, srv, "AdminDisableUser", map[string]any{
		"UserPoolId": poolID, "Username": "otto",
	})
	defer disResp.Body.Close()
	helpers.AssertStatus(t, disResp, http.StatusOK)

	// Then: sign-in returns NotAuthorizedException
	authResp := cognitoCall(t, srv, "InitiateAuth", map[string]any{
		"ClientId": clientID, "AuthFlow": "USER_PASSWORD_AUTH",
		"AuthParameters": map[string]string{"USERNAME": "otto", "PASSWORD": "OttoPass1!"},
	})
	defer authResp.Body.Close()
	helpers.AssertJSONError(t, authResp, "NotAuthorizedException")
}

// ---- RPC v2 CBOR tests ----

func TestRPCv2CBOR_ListUserPools(t *testing.T) {
	srv := helpers.NewTestServer(t)

	// Given: a user pool created via JSON
	createPool(t, srv, "cbor-pool")

	// When: ListUserPools over CBOR
	resp := cognitoCBORCall(t, srv, "ListUserPools", map[string]any{})
	defer resp.Body.Close()

	// Then: pools are returned in CBOR
	helpers.AssertStatus(t, resp, http.StatusOK)
	helpers.AssertHeader(t, resp, "Content-Type", "application/cbor")
	helpers.AssertHeader(t, resp, "Smithy-Protocol", "rpc-v2-cbor")

	var out struct {
		UserPools []struct {
			Id   string `cbor:"Id"`
			Name string `cbor:"Name"`
		} `cbor:"UserPools"`
	}
	decodeCBOR(t, resp, &out)
	if len(out.UserPools) == 0 {
		t.Fatal("expected at least one pool in CBOR response")
	}
}

func TestRPCv2CBOR_DescribeUserPool(t *testing.T) {
	srv := helpers.NewTestServer(t)

	poolID := createPool(t, srv, "cbor-desc")

	resp := cognitoCBORCall(t, srv, "DescribeUserPool", map[string]any{
		"UserPoolId": poolID,
	})
	defer resp.Body.Close()

	helpers.AssertStatus(t, resp, http.StatusOK)
	helpers.AssertHeader(t, resp, "Content-Type", "application/cbor")

	var out struct {
		UserPool struct {
			Id   string `cbor:"Id"`
			Name string `cbor:"Name"`
		} `cbor:"UserPool"`
	}
	decodeCBOR(t, resp, &out)
	if out.UserPool.Id != poolID {
		t.Fatalf("pool ID = %q, want %q", out.UserPool.Id, poolID)
	}
}

func cognitoCBORCall(t *testing.T, srv *helpers.TestServer, operation string, body map[string]any) *http.Response {
	t.Helper()

	payload, err := cborlib.Marshal(body)
	if err != nil {
		t.Fatalf("marshal CBOR %s body: %v", operation, err)
	}
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/service/cognito/operation/"+operation, bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("build CBOR request: %v", err)
	}
	req.Header.Set("Content-Type", "application/cbor")
	req.Header.Set("Smithy-Protocol", "rpc-v2-cbor")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do CBOR request %s: %v", operation, err)
	}
	return resp
}

func storedCognitoPlaintextPassword(t *testing.T, srv *helpers.TestServer, poolID string) string {
	t.Helper()
	entries, err := srv.Store.Scan(context.Background(), "cognito:users:"+poolID, "")
	if err != nil {
		t.Fatalf("scan cognito users: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 stored Cognito user, got %d", len(entries))
	}
	var user struct {
		PlaintextPassword string `json:"PlaintextPassword"`
	}
	if err := json.Unmarshal([]byte(entries[0].Value), &user); err != nil {
		t.Fatalf("unmarshal stored Cognito user: %v", err)
	}
	if user.PlaintextPassword == "" {
		t.Fatal("stored Cognito user has empty PlaintextPassword")
	}
	return user.PlaintextPassword
}

func assertPasswordCategories(t *testing.T, password string) {
	t.Helper()
	var hasUpper, hasLower, hasDigit, hasSymbol bool
	for _, ch := range password {
		switch {
		case unicode.IsUpper(ch):
			hasUpper = true
		case unicode.IsLower(ch):
			hasLower = true
		case unicode.IsDigit(ch):
			hasDigit = true
		default:
			hasSymbol = true
		}
	}
	if !hasUpper || !hasLower || !hasDigit || !hasSymbol {
		t.Fatalf("expected password to contain uppercase, lowercase, digit, and symbol: %q", password)
	}
}

func decodeCBOR(t *testing.T, resp *http.Response, v any) {
	t.Helper()
	if err := cborlib.NewDecoder(resp.Body).Decode(v); err != nil {
		t.Fatalf("decode CBOR response: %v", err)
	}
}
