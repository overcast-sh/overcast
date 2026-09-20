package cognito_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/overcast-sh/overcast/tests/helpers"
)

// createPoolWithUsernameAttributes creates a pool with the given UsernameAttributes and returns its ID.
func createPoolWithUsernameAttributes(t *testing.T, srv *helpers.TestServer, name string, attrs []string) string {
	t.Helper()
	resp := cognitoCall(t, srv, "CreateUserPool", map[string]any{
		"PoolName":           name,
		"UsernameAttributes": attrs,
	})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		UserPool struct {
			Id                 string   `json:"Id"`
			UsernameAttributes []string `json:"UsernameAttributes"`
		} `json:"UserPool"`
	}
	helpers.DecodeJSON(t, resp, &result)
	if result.UserPool.Id == "" {
		t.Fatal("CreateUserPool returned empty Id")
	}
	return result.UserPool.Id
}

// ─── DescribeUserPool returns UsernameAttributes ──────────────────────────────

func TestDescribeUserPool_returnsUsernameAttributes(t *testing.T) {
	// Given: a pool created with email as UsernameAttributes
	srv := helpers.NewTestServer(t)
	poolID := createPoolWithUsernameAttributes(t, srv, "email-pool", []string{"email"})

	// When: DescribeUserPool is called
	resp := cognitoCall(t, srv, "DescribeUserPool", map[string]any{
		"UserPoolId": poolID,
	})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)

	var result struct {
		UserPool struct {
			UsernameAttributes []string `json:"UsernameAttributes"`
		} `json:"UserPool"`
	}
	helpers.DecodeJSON(t, resp, &result)

	// Then: UsernameAttributes should contain "email"
	if len(result.UserPool.UsernameAttributes) != 1 || result.UserPool.UsernameAttributes[0] != "email" {
		t.Errorf("expected UsernameAttributes=[email], got %v", result.UserPool.UsernameAttributes)
	}
}

// ─── AdminCreateUser with email UsernameAttributes generates UUID username ────

func TestAdminCreateUser_emailPool_generatesUUIDUsername(t *testing.T) {
	// Given: a pool using email as the sign-in identifier
	srv := helpers.NewTestServer(t)
	poolID := createPoolWithUsernameAttributes(t, srv, "email-pool", []string{"email"})

	// When: AdminCreateUser is called with an email as Username
	resp := cognitoCall(t, srv, "AdminCreateUser", map[string]any{
		"UserPoolId":    poolID,
		"Username":      "alice@example.com",
		"MessageAction": "SUPPRESS",
	})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)

	var result struct {
		User struct {
			Username   string              `json:"Username"`
			Attributes []map[string]string `json:"Attributes"`
		} `json:"User"`
	}
	helpers.DecodeJSON(t, resp, &result)

	// Then: the internal username should be a UUID, not the email
	if result.User.Username == "alice@example.com" {
		t.Error("username should be auto-generated UUID, not the email address")
	}
	if !isUUID(result.User.Username) {
		t.Errorf("expected UUID username, got %q", result.User.Username)
	}
	if !hasAttr(result.User.Attributes, "sub", result.User.Username) {
		t.Errorf("expected sub attribute to match generated username %q, got %v", result.User.Username, result.User.Attributes)
	}

	// And: the email attribute should be set automatically
	found := false
	for _, a := range result.User.Attributes {
		if a["Name"] == "email" && a["Value"] == "alice@example.com" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected email attribute set to alice@example.com, attributes: %v", result.User.Attributes)
	}
}

// ─── AdminCreateUser with email pool rejects duplicate email ──────────────────

func TestAdminCreateUser_emailPool_rejectsDuplicateEmail(t *testing.T) {
	// Given: a pool with email UsernameAttributes and an existing user
	srv := helpers.NewTestServer(t)
	poolID := createPoolWithUsernameAttributes(t, srv, "email-pool", []string{"email"})
	resp := cognitoCall(t, srv, "AdminCreateUser", map[string]any{
		"UserPoolId":    poolID,
		"Username":      "bob@example.com",
		"MessageAction": "SUPPRESS",
	})
	resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)

	// When: another user is created with the same email
	resp2 := cognitoCall(t, srv, "AdminCreateUser", map[string]any{
		"UserPoolId":    poolID,
		"Username":      "bob@example.com",
		"MessageAction": "SUPPRESS",
	})
	defer resp2.Body.Close()

	// Then: it should be rejected as a duplicate
	helpers.AssertStatus(t, resp2, http.StatusBadRequest)
	var errResult struct {
		Code string `json:"__type"`
	}
	helpers.DecodeJSON(t, resp2, &errResult)
	if !strings.Contains(errResult.Code, "UsernameExistsException") {
		t.Errorf("expected UsernameExistsException, got %q", errResult.Code)
	}
}

// ─── Auth with email pool: sign in using email ────────────────────────────────

func TestAuth_emailPool_signInWithEmail(t *testing.T) {
	// Given: a pool with email sign-in and a confirmed user
	srv := helpers.NewTestServer(t)
	poolID := createPoolWithUsernameAttributes(t, srv, "email-pool", []string{"email"})
	clientID := createClient(t, srv, poolID, "app")

	cognitoCall(t, srv, "AdminCreateUser", map[string]any{
		"UserPoolId":    poolID,
		"Username":      "carol@example.com",
		"MessageAction": "SUPPRESS",
	}).Body.Close()

	// Set a permanent password (use the internal username via ListUsers)
	listResp := cognitoCall(t, srv, "ListUsers", map[string]any{
		"UserPoolId": poolID,
	})
	var listResult struct {
		Users []struct {
			Username string `json:"Username"`
		} `json:"Users"`
	}
	helpers.DecodeJSON(t, listResp, &listResult)
	listResp.Body.Close()
	if len(listResult.Users) == 0 {
		t.Fatal("expected at least one user")
	}
	internalUsername := listResult.Users[0].Username

	cognitoCall(t, srv, "AdminSetUserPassword", map[string]any{
		"UserPoolId": poolID,
		"Username":   internalUsername,
		"Password":   "TestPass1!",
		"Permanent":  true,
	}).Body.Close()

	// When: authenticating using the email address
	resp := cognitoCall(t, srv, "InitiateAuth", map[string]any{
		"ClientId": clientID,
		"AuthFlow": "USER_PASSWORD_AUTH",
		"AuthParameters": map[string]string{
			"USERNAME": "carol@example.com",
			"PASSWORD": "TestPass1!",
		},
	})
	defer resp.Body.Close()

	// Then: authentication should succeed
	helpers.AssertStatus(t, resp, http.StatusOK)
	var authResult struct {
		AuthenticationResult struct {
			AccessToken string `json:"AccessToken"`
		} `json:"AuthenticationResult"`
	}
	helpers.DecodeJSON(t, resp, &authResult)
	if authResult.AuthenticationResult.AccessToken == "" {
		t.Error("expected access token in auth result")
	}
}

func TestSignUp_emailPool_generatesUUIDUsername(t *testing.T) {
	// Given: a pool using email as the sign-in identifier
	srv := helpers.NewTestServer(t)
	poolID := createPoolWithUsernameAttributes(t, srv, "email-pool", []string{"email"})
	clientID := createClient(t, srv, poolID, "app")

	// When: SignUp is called with an email as Username
	resp := cognitoCall(t, srv, "SignUp", map[string]any{
		"ClientId": clientID,
		"Username": "heidi@example.com",
		"Password": "TestPass1!",
	})
	resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)

	// Then: the stored user has a UUID username and the email attribute is set
	resp = cognitoCall(t, srv, "ListUsers", map[string]any{
		"UserPoolId": poolID,
	})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		Users []struct {
			Username   string              `json:"Username"`
			Attributes []map[string]string `json:"Attributes"`
		} `json:"Users"`
	}
	helpers.DecodeJSON(t, resp, &result)
	if len(result.Users) != 1 {
		t.Fatalf("expected 1 user, got %d", len(result.Users))
	}
	if !isUUID(result.Users[0].Username) {
		t.Errorf("expected UUID username, got %q", result.Users[0].Username)
	}
	if !hasAttr(result.Users[0].Attributes, "sub", result.Users[0].Username) {
		t.Errorf("expected sub attribute to match generated username %q, got %v", result.Users[0].Username, result.Users[0].Attributes)
	}
	if !hasAttr(result.Users[0].Attributes, "email", "heidi@example.com") {
		t.Errorf("expected email attribute heidi@example.com, got %v", result.Users[0].Attributes)
	}
}

func TestSignUp_emailPool_userSubMatchesGeneratedUsername(t *testing.T) {
	// Given: a pool using email as the sign-in identifier
	srv := helpers.NewTestServer(t)
	poolID := createPoolWithUsernameAttributes(t, srv, "email-pool", []string{"email"})
	clientID := createClient(t, srv, poolID, "app")

	// When: SignUp is called with an email as Username
	resp := cognitoCall(t, srv, "SignUp", map[string]any{
		"ClientId": clientID,
		"Username": "judy@example.com",
		"Password": "TestPass1!",
	})
	helpers.AssertStatus(t, resp, http.StatusOK)
	var signUpResult struct {
		UserSub string `json:"UserSub"`
	}
	helpers.DecodeJSON(t, resp, &signUpResult)
	resp.Body.Close()

	// Then: the SignUp response's own UserSub is a UUID
	if !isUUID(signUpResult.UserSub) {
		t.Fatalf("expected a UUID UserSub, got %q", signUpResult.UserSub)
	}

	// And: it equals both the generated username and the stored sub attribute
	resp = cognitoCall(t, srv, "ListUsers", map[string]any{"UserPoolId": poolID})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	var listResult struct {
		Users []struct {
			Username   string              `json:"Username"`
			Attributes []map[string]string `json:"Attributes"`
		} `json:"Users"`
	}
	helpers.DecodeJSON(t, resp, &listResult)
	if len(listResult.Users) != 1 {
		t.Fatalf("expected 1 user, got %d", len(listResult.Users))
	}
	if listResult.Users[0].Username != signUpResult.UserSub {
		t.Errorf("expected generated username %q to equal UserSub %q", listResult.Users[0].Username, signUpResult.UserSub)
	}
	if !hasAttr(listResult.Users[0].Attributes, "sub", signUpResult.UserSub) {
		t.Errorf("expected sub attribute to equal UserSub %q, got %v", signUpResult.UserSub, listResult.Users[0].Attributes)
	}
}

func TestSignUp_emailPool_duplicateEmail(t *testing.T) {
	// Given: a pool using email as the sign-in identifier and one sign-up
	srv := helpers.NewTestServer(t)
	poolID := createPoolWithUsernameAttributes(t, srv, "email-pool", []string{"email"})
	clientID := createClient(t, srv, poolID, "app")
	resp := cognitoCall(t, srv, "SignUp", map[string]any{
		"ClientId": clientID,
		"Username": "kim@example.com",
		"Password": "TestPass1!",
	})
	resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)

	// When: the same email signs up again
	resp = cognitoCall(t, srv, "SignUp", map[string]any{
		"ClientId": clientID,
		"Username": "kim@example.com",
		"Password": "Different1!",
	})

	// Then: AWS's UsernameExistsException is returned, even though the stored
	// username is a generated UUID rather than the email itself
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "UsernameExistsException")
	resp.Body.Close()

	// And: no second user was created
	resp = cognitoCall(t, srv, "ListUsers", map[string]any{"UserPoolId": poolID})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	var listResult struct {
		Users []struct {
			Username string `json:"Username"`
		} `json:"Users"`
	}
	helpers.DecodeJSON(t, resp, &listResult)
	if len(listResult.Users) != 1 {
		t.Fatalf("expected 1 user after the duplicate sign-up, got %d", len(listResult.Users))
	}
}

func TestAdminConfirmSignUp_emailPool_usernameAttribute(t *testing.T) {
	// Given: a pool using email as the sign-in identifier and an unconfirmed sign-up.
	srv := helpers.NewTestServer(t)
	poolID := createPoolWithUsernameAttributes(t, srv, "email-confirm-pool", []string{"email"})
	clientID := createClient(t, srv, poolID, "email-confirm-client")
	resp := cognitoCall(t, srv, "SignUp", map[string]any{
		"ClientId": clientID,
		"Username": "ivan@example.com",
		"Password": "Password1!",
	})
	resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)

	// When: AdminConfirmSignUp is called with the email address as Username.
	resp = cognitoCall(t, srv, "AdminConfirmSignUp", map[string]any{
		"UserPoolId": poolID,
		"Username":   "ivan@example.com",
	})
	resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)

	// Then: the confirmed user can authenticate with that email sign-in identifier.
	resp = cognitoCall(t, srv, "InitiateAuth", map[string]any{
		"ClientId": clientID,
		"AuthFlow": "USER_PASSWORD_AUTH",
		"AuthParameters": map[string]string{
			"USERNAME": "ivan@example.com",
			"PASSWORD": "Password1!",
		},
	})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
}

func TestAdminGetUser_emailPool_usernameAttribute(t *testing.T) {
	// Given: a pool with email sign-in and an admin-created user
	srv := helpers.NewTestServer(t)
	poolID := createPoolWithUsernameAttributes(t, srv, "email-pool", []string{"email"})
	resp := cognitoCall(t, srv, "AdminCreateUser", map[string]any{
		"UserPoolId":    poolID,
		"Username":      "dave@example.com",
		"MessageAction": "SUPPRESS",
	})
	resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)

	// When: AdminGetUser is called with the email address as Username
	resp = cognitoCall(t, srv, "AdminGetUser", map[string]any{
		"UserPoolId": poolID,
		"Username":   "dave@example.com",
	})
	defer resp.Body.Close()

	// Then: Cognito resolves the email username attribute to the user
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		Username       string              `json:"Username"`
		UserAttributes []map[string]string `json:"UserAttributes"`
	}
	helpers.DecodeJSON(t, resp, &result)
	if !isUUID(result.Username) {
		t.Errorf("expected internal UUID username, got %q", result.Username)
	}
	if !hasAttr(result.UserAttributes, "email", "dave@example.com") {
		t.Errorf("expected email attribute dave@example.com, got %v", result.UserAttributes)
	}
}

func TestAdminUpdateUserAttributes_emailPool_usernameAttribute(t *testing.T) {
	// Given: a pool with email sign-in and an admin-created user.
	srv := helpers.NewTestServer(t)
	poolID := createPoolWithUsernameAttributes(t, srv, "email-update-attr-pool", []string{"email"})
	resp := cognitoCall(t, srv, "AdminCreateUser", map[string]any{
		"UserPoolId":    poolID,
		"Username":      "kate@example.com",
		"MessageAction": "SUPPRESS",
	})
	resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)

	// When: AdminUpdateUserAttributes is called with the email address as Username.
	resp = cognitoCall(t, srv, "AdminUpdateUserAttributes", map[string]any{
		"UserPoolId": poolID,
		"Username":   "kate@example.com",
		"UserAttributes": []map[string]string{
			{"Name": "custom:role", "Value": "admin"},
		},
	})
	resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)

	// Then: the resolved generated-username user has the updated attribute.
	resp = cognitoCall(t, srv, "AdminGetUser", map[string]any{
		"UserPoolId": poolID,
		"Username":   "kate@example.com",
	})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		UserAttributes []map[string]string `json:"UserAttributes"`
	}
	helpers.DecodeJSON(t, resp, &result)
	if !hasAttr(result.UserAttributes, "custom:role", "admin") {
		t.Fatalf("expected custom:role=admin, got %v", result.UserAttributes)
	}
}

func TestAdminSetUserPassword_emailPool_usernameAttribute(t *testing.T) {
	// Given: a pool with email sign-in and an admin-created user
	srv := helpers.NewTestServer(t)
	poolID := createPoolWithUsernameAttributes(t, srv, "email-pool", []string{"email"})
	clientID := createClient(t, srv, poolID, "app")
	resp := cognitoCall(t, srv, "AdminCreateUser", map[string]any{
		"UserPoolId":    poolID,
		"Username":      "erin@example.com",
		"MessageAction": "SUPPRESS",
	})
	resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)

	// When: AdminSetUserPassword is called with the email address as Username
	resp = cognitoCall(t, srv, "AdminSetUserPassword", map[string]any{
		"UserPoolId": poolID,
		"Username":   "erin@example.com",
		"Password":   "TestPass1!",
		"Permanent":  true,
	})
	resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)

	// Then: the user can authenticate with that email address
	resp = cognitoCall(t, srv, "InitiateAuth", map[string]any{
		"ClientId": clientID,
		"AuthFlow": "USER_PASSWORD_AUTH",
		"AuthParameters": map[string]string{
			"USERNAME": "erin@example.com",
			"PASSWORD": "TestPass1!",
		},
	})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
}

func TestAdminInitiateAuth_emailPool_tokenSubjectMatchesCreatedUsername(t *testing.T) {
	// Given: a pool with email sign-in and an admin-created user
	srv := helpers.NewTestServer(t)
	poolID := createPoolWithUsernameAttributes(t, srv, "email-pool", []string{"email"})
	clientID := createClient(t, srv, poolID, "app")

	createResp := cognitoCall(t, srv, "AdminCreateUser", map[string]any{
		"UserPoolId":    poolID,
		"Username":      "source@example.com",
		"MessageAction": "SUPPRESS",
	})
	var createResult struct {
		User struct {
			Username string `json:"Username"`
		} `json:"User"`
	}
	helpers.DecodeJSON(t, createResp, &createResult)
	createResp.Body.Close()
	helpers.AssertStatus(t, createResp, http.StatusOK)

	setResp := cognitoCall(t, srv, "AdminSetUserPassword", map[string]any{
		"UserPoolId": poolID,
		"Username":   "source@example.com",
		"Password":   "TestPass1!",
		"Permanent":  true,
	})
	setResp.Body.Close()
	helpers.AssertStatus(t, setResp, http.StatusOK)

	// When: the user authenticates with their email sign-in attribute
	resp := cognitoCall(t, srv, "AdminInitiateAuth", map[string]any{
		"UserPoolId": poolID,
		"ClientId":   clientID,
		"AuthFlow":   "ADMIN_USER_PASSWORD_AUTH",
		"AuthParameters": map[string]string{
			"USERNAME": "source@example.com",
			"PASSWORD": "TestPass1!",
		},
	})
	defer resp.Body.Close()

	// Then: the access token subject matches the UserType.Username returned at creation
	helpers.AssertStatus(t, resp, http.StatusOK)
	var authResult struct {
		AuthenticationResult struct {
			AccessToken string `json:"AccessToken"`
		} `json:"AuthenticationResult"`
	}
	helpers.DecodeJSON(t, resp, &authResult)
	claims := decodeJWTPayload(t, authResult.AuthenticationResult.AccessToken)
	if claims["sub"] != createResult.User.Username {
		t.Errorf("expected access token sub %q, got %q", createResult.User.Username, claims["sub"])
	}
}

func TestRespondToAuthChallenge_emailPool_usernameAttribute(t *testing.T) {
	// Given: a pool with email sign-in and a user in FORCE_CHANGE_PASSWORD
	srv := helpers.NewTestServer(t)
	poolID := createPoolWithUsernameAttributes(t, srv, "email-pool", []string{"email"})
	clientID := createClient(t, srv, poolID, "app")
	resp := cognitoCall(t, srv, "AdminCreateUser", map[string]any{
		"UserPoolId":         poolID,
		"Username":           "frank@example.com",
		"TemporaryPassword":  "TempPass1!",
		"MessageAction":      "SUPPRESS",
		"UserAttributes":     []map[string]string{{"Name": "email_verified", "Value": "true"}},
		"ForceAliasCreation": true,
	})
	resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)

	initResp := cognitoCall(t, srv, "InitiateAuth", map[string]any{
		"ClientId": clientID,
		"AuthFlow": "USER_PASSWORD_AUTH",
		"AuthParameters": map[string]string{
			"USERNAME": "frank@example.com",
			"PASSWORD": "TempPass1!",
		},
	})
	var initResult struct {
		ChallengeName string `json:"ChallengeName"`
		Session       string `json:"Session"`
	}
	helpers.DecodeJSON(t, initResp, &initResult)
	initResp.Body.Close()
	if initResult.ChallengeName != "NEW_PASSWORD_REQUIRED" || initResult.Session == "" {
		t.Fatalf("expected NEW_PASSWORD_REQUIRED session, got %#v", initResult)
	}

	// When: RespondToAuthChallenge completes the challenge from the email-based session
	resp = cognitoCall(t, srv, "RespondToAuthChallenge", map[string]any{
		"ClientId":      clientID,
		"ChallengeName": "NEW_PASSWORD_REQUIRED",
		"Session":       initResult.Session,
		"ChallengeResponses": map[string]string{
			"USERNAME":     "frank@example.com",
			"NEW_PASSWORD": "NewPass1!",
		},
	})
	defer resp.Body.Close()

	// Then: the challenge succeeds and returns tokens
	helpers.AssertStatus(t, resp, http.StatusOK)
	var challengeResult struct {
		AuthenticationResult struct {
			AccessToken string `json:"AccessToken"`
		} `json:"AuthenticationResult"`
	}
	helpers.DecodeJSON(t, resp, &challengeResult)
	if challengeResult.AuthenticationResult.AccessToken == "" {
		t.Error("expected access token in challenge result")
	}
}

func TestAdminDeleteUser_emailPool_usernameAttribute(t *testing.T) {
	// Given: a pool with email sign-in and an admin-created user
	srv := helpers.NewTestServer(t)
	poolID := createPoolWithUsernameAttributes(t, srv, "email-pool", []string{"email"})
	resp := cognitoCall(t, srv, "AdminCreateUser", map[string]any{
		"UserPoolId":    poolID,
		"Username":      "grace@example.com",
		"MessageAction": "SUPPRESS",
	})
	resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)

	// When: AdminDeleteUser is called with the email address as Username
	resp = cognitoCall(t, srv, "AdminDeleteUser", map[string]any{
		"UserPoolId": poolID,
		"Username":   "grace@example.com",
	})
	resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)

	// Then: subsequent lookup by email no longer finds the user
	resp = cognitoCall(t, srv, "AdminGetUser", map[string]any{
		"UserPoolId": poolID,
		"Username":   "grace@example.com",
	})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "UserNotFoundException")
}

func TestAdminDisableEnableUser_emailPool_usernameAttribute(t *testing.T) {
	// Given: a pool with email sign-in and a confirmed user with a password.
	srv := helpers.NewTestServer(t)
	poolID := createPoolWithUsernameAttributes(t, srv, "email-disable-pool", []string{"email"})
	clientID := createClient(t, srv, poolID, "email-disable-client")
	resp := cognitoCall(t, srv, "AdminCreateUser", map[string]any{
		"UserPoolId":    poolID,
		"Username":      "heidi@example.com",
		"MessageAction": "SUPPRESS",
	})
	resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	resp = cognitoCall(t, srv, "AdminSetUserPassword", map[string]any{
		"UserPoolId": poolID,
		"Username":   "heidi@example.com",
		"Password":   "Password1!",
		"Permanent":  true,
	})
	resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)

	// When: AdminDisableUser is called with the email address as Username.
	resp = cognitoCall(t, srv, "AdminDisableUser", map[string]any{
		"UserPoolId": poolID,
		"Username":   "heidi@example.com",
	})
	resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)

	// Then: password auth with that email is rejected because the resolved user is disabled.
	resp = cognitoCall(t, srv, "InitiateAuth", map[string]any{
		"ClientId": clientID,
		"AuthFlow": "USER_PASSWORD_AUTH",
		"AuthParameters": map[string]string{
			"USERNAME": "heidi@example.com",
			"PASSWORD": "Password1!",
		},
	})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "NotAuthorizedException")

	// When: AdminEnableUser is called with the email address as Username.
	resp = cognitoCall(t, srv, "AdminEnableUser", map[string]any{
		"UserPoolId": poolID,
		"Username":   "heidi@example.com",
	})
	resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)

	// Then: the same user can authenticate again.
	resp = cognitoCall(t, srv, "InitiateAuth", map[string]any{
		"ClientId": clientID,
		"AuthFlow": "USER_PASSWORD_AUTH",
		"AuthParameters": map[string]string{
			"USERNAME": "heidi@example.com",
			"PASSWORD": "Password1!",
		},
	})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
}

func TestAdminDeleteUserAttributes_emailPool_usernameAttribute(t *testing.T) {
	// Given: a pool with email sign-in and an admin-created user with a custom attribute.
	srv := helpers.NewTestServer(t)
	poolID := createPoolWithUsernameAttributes(t, srv, "email-delete-attr-pool", []string{"email"})
	resp := cognitoCall(t, srv, "AdminCreateUser", map[string]any{
		"UserPoolId":    poolID,
		"Username":      "jane@example.com",
		"MessageAction": "SUPPRESS",
		"UserAttributes": []map[string]string{
			{"Name": "custom:color", "Value": "blue"},
		},
	})
	resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)

	// When: AdminDeleteUserAttributes is called with the email address as Username.
	resp = cognitoCall(t, srv, "AdminDeleteUserAttributes", map[string]any{
		"UserPoolId":         poolID,
		"Username":           "jane@example.com",
		"UserAttributeNames": []string{"custom:color"},
	})
	resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)

	// Then: the resolved generated-username user no longer has the attribute.
	resp = cognitoCall(t, srv, "AdminGetUser", map[string]any{
		"UserPoolId": poolID,
		"Username":   "jane@example.com",
	})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		UserAttributes []map[string]string `json:"UserAttributes"`
	}
	helpers.DecodeJSON(t, resp, &result)
	if hasAttr(result.UserAttributes, "custom:color", "blue") {
		t.Fatalf("expected custom:color to be deleted, got %v", result.UserAttributes)
	}
}

func TestAdminGroupMembership_emailPool_usernameAttribute(t *testing.T) {
	// Given: a pool with email sign-in, a user, and a group.
	srv := helpers.NewTestServer(t)
	poolID := createPoolWithUsernameAttributes(t, srv, "email-group-pool", []string{"email"})
	resp := cognitoCall(t, srv, "AdminCreateUser", map[string]any{
		"UserPoolId":    poolID,
		"Username":      "laura@example.com",
		"MessageAction": "SUPPRESS",
	})
	resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	resp = cognitoCall(t, srv, "CreateGroup", map[string]any{"UserPoolId": poolID, "GroupName": "staff"})
	resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)

	// When: AdminAddUserToGroup is called with the email address as Username.
	resp = cognitoCall(t, srv, "AdminAddUserToGroup", map[string]any{
		"UserPoolId": poolID,
		"Username":   "laura@example.com",
		"GroupName":  "staff",
	})
	resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)

	// Then: AdminListGroupsForUser resolves the same email Username and returns the group.
	resp = cognitoCall(t, srv, "AdminListGroupsForUser", map[string]any{
		"UserPoolId": poolID,
		"Username":   "laura@example.com",
	})
	var result struct {
		Groups []struct{ GroupName string } `json:"Groups"`
	}
	helpers.DecodeJSON(t, resp, &result)
	resp.Body.Close()
	if len(result.Groups) != 1 || result.Groups[0].GroupName != "staff" {
		t.Fatalf("expected staff group for email Username, got %#v", result.Groups)
	}

	// When: AdminRemoveUserFromGroup is called with the email address as Username.
	resp = cognitoCall(t, srv, "AdminRemoveUserFromGroup", map[string]any{
		"UserPoolId": poolID,
		"Username":   "laura@example.com",
		"GroupName":  "staff",
	})
	resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)

	// Then: the resolved user has no group memberships.
	resp = cognitoCall(t, srv, "AdminListGroupsForUser", map[string]any{
		"UserPoolId": poolID,
		"Username":   "laura@example.com",
	})
	defer resp.Body.Close()
	result.Groups = nil
	helpers.DecodeJSON(t, resp, &result)
	if len(result.Groups) != 0 {
		t.Fatalf("expected no groups after removal, got %#v", result.Groups)
	}
}

// ─── AdminCreateUser with phone UsernameAttributes ────────────────────────────

func TestAdminCreateUser_phonePool_generatesUUIDUsername(t *testing.T) {
	// Given: a pool using phone_number as the sign-in identifier
	srv := helpers.NewTestServer(t)
	poolID := createPoolWithUsernameAttributes(t, srv, "phone-pool", []string{"phone_number"})

	// When: AdminCreateUser is called with a phone number as Username
	resp := cognitoCall(t, srv, "AdminCreateUser", map[string]any{
		"UserPoolId":    poolID,
		"Username":      "+15551234567",
		"MessageAction": "SUPPRESS",
	})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)

	var result struct {
		User struct {
			Username   string              `json:"Username"`
			Attributes []map[string]string `json:"Attributes"`
		} `json:"User"`
	}
	helpers.DecodeJSON(t, resp, &result)

	// Then: the internal username should be a UUID
	if result.User.Username == "+15551234567" {
		t.Error("username should be auto-generated UUID, not the phone number")
	}
	if !isUUID(result.User.Username) {
		t.Errorf("expected UUID username, got %q", result.User.Username)
	}
	if !hasAttr(result.User.Attributes, "sub", result.User.Username) {
		t.Errorf("expected sub attribute to match generated username %q, got %v", result.User.Username, result.User.Attributes)
	}

	// And: the phone_number attribute should be set automatically
	found := false
	for _, a := range result.User.Attributes {
		if a["Name"] == "phone_number" && a["Value"] == "+15551234567" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected phone_number attribute set to +15551234567, attributes: %v", result.User.Attributes)
	}
}

func hasAttr(attrs []map[string]string, name, value string) bool {
	for _, attr := range attrs {
		if attr["Name"] == name && attr["Value"] == value {
			return true
		}
	}
	return false
}

// ─── Plain username pool preserves username as-is ─────────────────────────────

func TestAdminCreateUser_plainPool_preservesUsername(t *testing.T) {
	// Given: a pool with NO UsernameAttributes (plain username)
	srv := helpers.NewTestServer(t)
	poolID := createPool(t, srv, "plain-pool")

	// When: AdminCreateUser is called with a plain username
	resp := cognitoCall(t, srv, "AdminCreateUser", map[string]any{
		"UserPoolId":    poolID,
		"Username":      "john.doe",
		"MessageAction": "SUPPRESS",
	})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)

	var result struct {
		User struct {
			Username string `json:"Username"`
		} `json:"User"`
	}
	helpers.DecodeJSON(t, resp, &result)

	// Then: the username should be preserved as-is
	if result.User.Username != "john.doe" {
		t.Errorf("expected username 'john.doe', got %q", result.User.Username)
	}
}
