// Package cognito_test: extends the resolveUser username-attribute/alias/sub
// sweep (started for AdminEnableUser/AdminDisableUser, AdminConfirmSignUp,
// AdminDeleteUserAttributes, AdminUpdateUserAttributes and the group operations
// in cognito_sub_lookup_test.go and cognito_username_attributes_test.go) to the
// remaining admin operations that take a Username: AdminInitiateAuth,
// AdminRespondToAuthChallenge, the admin device operations, and
// AdminSetUserMFAPreference/AdminSetUserPassword.
//
// AdminUserGlobalSignOut, AdminResetUserPassword and AdminListUserAuthEvents are
// modeled by AWS but not implemented in internal/services/cognito (no
// capabilities_dev.go entry, no Typed handler), so they are intentionally not
// covered here.
package cognito_test

import (
	"net/http"
	"testing"

	"github.com/overcast-sh/overcast/tests/helpers"
)

func createAliasPoolWithDeviceConfiguration(t *testing.T, srv *helpers.TestServer, name string, aliasAttrs []string, deviceConfig map[string]any) string {
	t.Helper()
	resp := cognitoCall(t, srv, "CreateUserPool", map[string]any{
		"PoolName":            name,
		"AliasAttributes":     aliasAttrs,
		"DeviceConfiguration": deviceConfig,
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

func createConfirmedAliasUser(t *testing.T, srv *helpers.TestServer, poolID, username, email, password string) {
	t.Helper()
	resp := cognitoCall(t, srv, "AdminCreateUser", map[string]any{
		"UserPoolId":    poolID,
		"Username":      username,
		"MessageAction": "SUPPRESS",
		"UserAttributes": []map[string]string{
			{"Name": "email", "Value": email},
			{"Name": "email_verified", "Value": "true"},
		},
	})
	resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	resp = cognitoCall(t, srv, "AdminSetUserPassword", map[string]any{
		"UserPoolId": poolID,
		"Username":   username,
		"Password":   password,
		"Permanent":  true,
	})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
}

// ─── AdminInitiateAuth ─────────────────────────────────────────────────────

func TestAdminInitiateAuth_aliasPool_emailAlias(t *testing.T) {
	// Given: an AliasAttributes:[email] pool with a confirmed user
	srv := helpers.NewTestServer(t)
	poolID := createPoolWithAliasAttributes(t, srv, "p", []string{"email"})
	clientID := createClient(t, srv, poolID, "app")
	createConfirmedAliasUser(t, srv, poolID, "ivan", "ivan@example.com", "IvanPass1!")

	// When: AdminInitiateAuth signs in with the verified email alias as Username
	resp := cognitoCall(t, srv, "AdminInitiateAuth", map[string]any{
		"UserPoolId": poolID,
		"ClientId":   clientID,
		"AuthFlow":   "ADMIN_USER_PASSWORD_AUTH",
		"AuthParameters": map[string]string{
			"USERNAME": "ivan@example.com",
			"PASSWORD": "IvanPass1!",
		},
	})
	defer resp.Body.Close()

	// Then: Cognito resolves the alias and returns tokens
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		AuthenticationResult struct {
			AccessToken string `json:"AccessToken"`
		} `json:"AuthenticationResult"`
	}
	helpers.DecodeJSON(t, resp, &result)
	if result.AuthenticationResult.AccessToken == "" {
		t.Fatal("expected AccessToken, got none")
	}
}

func TestAdminInitiateAuth_plainPool_subUsername(t *testing.T) {
	// Given: a plain username pool user with a sub attribute
	srv := helpers.NewTestServer(t)
	poolID := createPool(t, srv, "p")
	clientID := createClient(t, srv, poolID, "app")
	sub := createUserAndReturnSub(t, srv, poolID, "jane")
	cognitoCall(t, srv, "AdminSetUserPassword", map[string]any{
		"UserPoolId": poolID, "Username": "jane", "Password": "JanePass1!", "Permanent": true,
	}).Body.Close()

	// When: AdminInitiateAuth signs in with sub as Username
	resp := cognitoCall(t, srv, "AdminInitiateAuth", map[string]any{
		"UserPoolId": poolID,
		"ClientId":   clientID,
		"AuthFlow":   "ADMIN_USER_PASSWORD_AUTH",
		"AuthParameters": map[string]string{
			"USERNAME": sub,
			"PASSWORD": "JanePass1!",
		},
	})
	defer resp.Body.Close()

	// Then: Cognito resolves sub to the user and returns tokens
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		AuthenticationResult struct {
			AccessToken string `json:"AccessToken"`
		} `json:"AuthenticationResult"`
	}
	helpers.DecodeJSON(t, resp, &result)
	if result.AuthenticationResult.AccessToken == "" {
		t.Fatal("expected AccessToken, got none")
	}
}

func TestAdminInitiateAuth_userNotFound(t *testing.T) {
	// Given: a pool with no matching user
	srv := helpers.NewTestServer(t)
	poolID := createPool(t, srv, "p")
	clientID := createClient(t, srv, poolID, "app")

	// When: AdminInitiateAuth targets a username that resolves to nobody
	resp := cognitoCall(t, srv, "AdminInitiateAuth", map[string]any{
		"UserPoolId": poolID,
		"ClientId":   clientID,
		"AuthFlow":   "ADMIN_USER_PASSWORD_AUTH",
		"AuthParameters": map[string]string{
			"USERNAME": "no-such-user",
			"PASSWORD": "Whatever1!",
		},
	})
	defer resp.Body.Close()

	// Then: Cognito reports the user as not found
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "UserNotFoundException")
}

// ─── AdminRespondToAuthChallenge ───────────────────────────────────────────
//
// Only the positive (alias/sub resolve) path is covered here. An unresolved
// username on this call lands in completeChoicePasswordChallengeTyped's
// session-identity check (`u == nil || u.Username != st.Username`), which
// answers NotAuthorizedException ("Invalid session") rather than
// UserNotFoundException. That reads as deliberate session-identity masking
// consistent with how AWS treats a second auth-challenge step (the same
// class of behavior as InitiateAuth's "Incorrect username or password"
// masking a nonexistent user), not as a confirmed divergence, so it isn't
// asserted either way here.

func TestAdminRespondToAuthChallenge_aliasPool_emailAlias(t *testing.T) {
	// Given: a USER_AUTH SELECT_CHALLENGE session for a user found by email alias
	srv := helpers.NewTestServer(t)
	poolID := createPoolWithAliasAttributes(t, srv, "p", []string{"email"})
	clientID := createClientWithExplicitAuthFlows(t, srv, poolID, "app", []string{"ALLOW_USER_AUTH", "ALLOW_REFRESH_TOKEN_AUTH"})
	createConfirmedAliasUser(t, srv, poolID, "karen", "karen@example.com", "KarenPass1!")
	resp := cognitoCall(t, srv, "AdminInitiateAuth", map[string]any{
		"UserPoolId": poolID,
		"ClientId":   clientID,
		"AuthFlow":   "USER_AUTH",
		"AuthParameters": map[string]string{
			"USERNAME": "karen@example.com",
		},
	})
	helpers.AssertStatus(t, resp, http.StatusOK)
	var initResult struct {
		Session string `json:"Session"`
	}
	helpers.DecodeJSON(t, resp, &initResult)
	resp.Body.Close()

	// When: AdminRespondToAuthChallenge selects PASSWORD using the same email alias
	resp = cognitoCall(t, srv, "AdminRespondToAuthChallenge", map[string]any{
		"UserPoolId":    poolID,
		"ClientId":      clientID,
		"ChallengeName": "SELECT_CHALLENGE",
		"Session":       initResult.Session,
		"ChallengeResponses": map[string]string{
			"USERNAME": "karen@example.com",
			"ANSWER":   "PASSWORD",
			"PASSWORD": "KarenPass1!",
		},
	})
	defer resp.Body.Close()

	// Then: authentication completes for the alias-resolved user
	helpers.AssertStatus(t, resp, http.StatusOK)
	var authResult struct {
		AuthenticationResult struct {
			AccessToken string `json:"AccessToken"`
		} `json:"AuthenticationResult"`
	}
	helpers.DecodeJSON(t, resp, &authResult)
	if authResult.AuthenticationResult.AccessToken == "" {
		t.Fatal("expected AccessToken, got none")
	}
}

func TestAdminRespondToAuthChallenge_plainPool_subUsername(t *testing.T) {
	// Given: a USER_AUTH SELECT_CHALLENGE session for a user found by sub
	srv := helpers.NewTestServer(t)
	poolID := createPool(t, srv, "p")
	clientID := createClientWithExplicitAuthFlows(t, srv, poolID, "app", []string{"ALLOW_USER_AUTH", "ALLOW_REFRESH_TOKEN_AUTH"})
	sub := createUserAndReturnSub(t, srv, poolID, "leo")
	cognitoCall(t, srv, "AdminSetUserPassword", map[string]any{
		"UserPoolId": poolID, "Username": "leo", "Password": "LeoPass1!", "Permanent": true,
	}).Body.Close()
	resp := cognitoCall(t, srv, "AdminInitiateAuth", map[string]any{
		"UserPoolId": poolID,
		"ClientId":   clientID,
		"AuthFlow":   "USER_AUTH",
		"AuthParameters": map[string]string{
			"USERNAME": sub,
		},
	})
	helpers.AssertStatus(t, resp, http.StatusOK)
	var initResult struct {
		Session string `json:"Session"`
	}
	helpers.DecodeJSON(t, resp, &initResult)
	resp.Body.Close()

	// When: AdminRespondToAuthChallenge selects PASSWORD using the same sub
	resp = cognitoCall(t, srv, "AdminRespondToAuthChallenge", map[string]any{
		"UserPoolId":    poolID,
		"ClientId":      clientID,
		"ChallengeName": "SELECT_CHALLENGE",
		"Session":       initResult.Session,
		"ChallengeResponses": map[string]string{
			"USERNAME": sub,
			"ANSWER":   "PASSWORD",
			"PASSWORD": "LeoPass1!",
		},
	})
	defer resp.Body.Close()

	// Then: authentication completes for the sub-resolved user
	helpers.AssertStatus(t, resp, http.StatusOK)
	var authResult struct {
		AuthenticationResult struct {
			AccessToken string `json:"AccessToken"`
		} `json:"AuthenticationResult"`
	}
	helpers.DecodeJSON(t, resp, &authResult)
	if authResult.AuthenticationResult.AccessToken == "" {
		t.Fatal("expected AccessToken, got none")
	}
}

// ─── Admin device operations ───────────────────────────────────────────────

func TestAdminDeviceOperations_aliasPool_emailAlias(t *testing.T) {
	// Given: a user found by email alias with one confirmed device
	srv := helpers.NewTestServer(t)
	poolID := createAliasPoolWithDeviceConfiguration(t, srv, "p", []string{"email"}, map[string]any{
		"ChallengeRequiredOnNewDevice":     true,
		"DeviceOnlyRememberedOnUserPrompt": false,
	})
	clientID := createClientWithExplicitAuthFlows(t, srv, poolID, "app", []string{"ALLOW_USER_PASSWORD_AUTH", "ALLOW_REFRESH_TOKEN_AUTH"})
	createConfirmedAliasUser(t, srv, poolID, "gina", "gina@example.com", "GinaPass1!")
	_, deviceKey := signInAndConfirmDevice(t, srv, clientID, "gina", "GinaPass1!", "gina's device")

	// When/Then: AdminGetDevice resolves the user by email alias
	resp := cognitoCall(t, srv, "AdminGetDevice", map[string]any{
		"UserPoolId": poolID,
		"Username":   "gina@example.com",
		"DeviceKey":  deviceKey,
	})
	helpers.AssertStatus(t, resp, http.StatusOK)
	var getResult struct {
		Device struct {
			DeviceKey string `json:"DeviceKey"`
		} `json:"Device"`
	}
	helpers.DecodeJSON(t, resp, &getResult)
	resp.Body.Close()
	if getResult.Device.DeviceKey != deviceKey {
		t.Fatalf("expected device %q, got %#v", deviceKey, getResult.Device)
	}

	// And: AdminListDevices resolves the user by email alias
	resp = cognitoCall(t, srv, "AdminListDevices", map[string]any{
		"UserPoolId": poolID,
		"Username":   "gina@example.com",
	})
	helpers.AssertStatus(t, resp, http.StatusOK)
	var listResult struct {
		Devices []struct {
			DeviceKey string `json:"DeviceKey"`
		} `json:"Devices"`
	}
	helpers.DecodeJSON(t, resp, &listResult)
	resp.Body.Close()
	if len(listResult.Devices) != 1 || listResult.Devices[0].DeviceKey != deviceKey {
		t.Fatalf("expected one listed device %q, got %#v", deviceKey, listResult.Devices)
	}

	// And: AdminUpdateDeviceStatus resolves the user by email alias
	resp = cognitoCall(t, srv, "AdminUpdateDeviceStatus", map[string]any{
		"UserPoolId":             poolID,
		"Username":               "gina@example.com",
		"DeviceKey":              deviceKey,
		"DeviceRememberedStatus": "not_remembered",
	})
	resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)

	// And: AdminForgetDevice resolves the user by email alias
	resp = cognitoCall(t, srv, "AdminForgetDevice", map[string]any{
		"UserPoolId": poolID,
		"Username":   "gina@example.com",
		"DeviceKey":  deviceKey,
	})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
}

func TestAdminDeviceOperations_plainPool_subUsername(t *testing.T) {
	// Given: a user found by sub with one confirmed device
	srv := helpers.NewTestServer(t)
	poolID := createPoolWithDeviceConfiguration(t, srv, "p", map[string]any{
		"ChallengeRequiredOnNewDevice":     true,
		"DeviceOnlyRememberedOnUserPrompt": false,
	})
	clientID := createClientWithExplicitAuthFlows(t, srv, poolID, "app", []string{"ALLOW_USER_PASSWORD_AUTH", "ALLOW_REFRESH_TOKEN_AUTH"})
	sub := createUserAndReturnSub(t, srv, poolID, "henry")
	cognitoCall(t, srv, "AdminSetUserPassword", map[string]any{
		"UserPoolId": poolID, "Username": "henry", "Password": "HenryPass1!", "Permanent": true,
	}).Body.Close()
	_, deviceKey := signInAndConfirmDevice(t, srv, clientID, "henry", "HenryPass1!", "henry's device")

	// When/Then: AdminGetDevice resolves the user by sub
	resp := cognitoCall(t, srv, "AdminGetDevice", map[string]any{
		"UserPoolId": poolID,
		"Username":   sub,
		"DeviceKey":  deviceKey,
	})
	helpers.AssertStatus(t, resp, http.StatusOK)
	var getResult struct {
		Device struct {
			DeviceKey string `json:"DeviceKey"`
		} `json:"Device"`
	}
	helpers.DecodeJSON(t, resp, &getResult)
	resp.Body.Close()
	if getResult.Device.DeviceKey != deviceKey {
		t.Fatalf("expected device %q, got %#v", deviceKey, getResult.Device)
	}

	// And: AdminListDevices resolves the user by sub
	resp = cognitoCall(t, srv, "AdminListDevices", map[string]any{
		"UserPoolId": poolID,
		"Username":   sub,
	})
	helpers.AssertStatus(t, resp, http.StatusOK)
	var listResult struct {
		Devices []struct {
			DeviceKey string `json:"DeviceKey"`
		} `json:"Devices"`
	}
	helpers.DecodeJSON(t, resp, &listResult)
	resp.Body.Close()
	if len(listResult.Devices) != 1 || listResult.Devices[0].DeviceKey != deviceKey {
		t.Fatalf("expected one listed device %q, got %#v", deviceKey, listResult.Devices)
	}

	// And: AdminUpdateDeviceStatus resolves the user by sub
	resp = cognitoCall(t, srv, "AdminUpdateDeviceStatus", map[string]any{
		"UserPoolId":             poolID,
		"Username":               sub,
		"DeviceKey":              deviceKey,
		"DeviceRememberedStatus": "not_remembered",
	})
	resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)

	// And: AdminForgetDevice resolves the user by sub
	resp = cognitoCall(t, srv, "AdminForgetDevice", map[string]any{
		"UserPoolId": poolID,
		"Username":   sub,
		"DeviceKey":  deviceKey,
	})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
}

func TestAdminDeviceOperations_userNotFound(t *testing.T) {
	// Given: a pool with no matching user
	srv := helpers.NewTestServer(t)
	poolID := createPool(t, srv, "p")
	const unknownDeviceKey = "us-east-1_11111111-1111-1111-1111-111111111111"

	cases := []struct {
		name      string
		operation string
		body      map[string]any
	}{
		{"AdminGetDevice", "AdminGetDevice", map[string]any{"UserPoolId": poolID, "Username": "no-such-user", "DeviceKey": unknownDeviceKey}},
		{"AdminListDevices", "AdminListDevices", map[string]any{"UserPoolId": poolID, "Username": "no-such-user"}},
		{"AdminUpdateDeviceStatus", "AdminUpdateDeviceStatus", map[string]any{"UserPoolId": poolID, "Username": "no-such-user", "DeviceKey": unknownDeviceKey, "DeviceRememberedStatus": "remembered"}},
		{"AdminForgetDevice", "AdminForgetDevice", map[string]any{"UserPoolId": poolID, "Username": "no-such-user", "DeviceKey": unknownDeviceKey}},
	}
	for _, tc := range cases {
		// When: the operation targets a username that resolves to nobody
		resp := cognitoCall(t, srv, tc.operation, tc.body)

		// Then: Cognito reports the user as not found, not the device
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: expected status 400, got %d", tc.name, resp.StatusCode)
		}
		helpers.AssertJSONError(t, resp, "UserNotFoundException")
		resp.Body.Close()
	}
}

// ─── AdminSetUserMFAPreference ─────────────────────────────────────────────

func TestAdminSetUserMFAPreference_aliasPool_emailAlias(t *testing.T) {
	// Given: a user found by email alias
	srv := helpers.NewTestServer(t)
	poolID := createPoolWithAliasAttributes(t, srv, "p", []string{"email"})
	createConfirmedAliasUser(t, srv, poolID, "mia", "mia@example.com", "MiaPass1!")

	// When: AdminSetUserMFAPreference targets the user by email alias
	resp := cognitoCall(t, srv, "AdminSetUserMFAPreference", map[string]any{
		"UserPoolId":               poolID,
		"Username":                 "mia@example.com",
		"SoftwareTokenMfaSettings": map[string]any{"Enabled": false},
	})
	defer resp.Body.Close()

	// Then: Cognito resolves the alias and applies the preference
	helpers.AssertStatus(t, resp, http.StatusOK)
}

func TestAdminSetUserMFAPreference_plainPool_subUsername(t *testing.T) {
	// Given: a user found by sub
	srv := helpers.NewTestServer(t)
	poolID := createPool(t, srv, "p")
	sub := createUserAndReturnSub(t, srv, poolID, "nina")

	// When: AdminSetUserMFAPreference targets the user by sub
	resp := cognitoCall(t, srv, "AdminSetUserMFAPreference", map[string]any{
		"UserPoolId":               poolID,
		"Username":                 sub,
		"SoftwareTokenMfaSettings": map[string]any{"Enabled": false},
	})
	defer resp.Body.Close()

	// Then: Cognito resolves sub and applies the preference
	helpers.AssertStatus(t, resp, http.StatusOK)
}

func TestAdminSetUserMFAPreference_userNotFound(t *testing.T) {
	// Given: a pool with no matching user
	srv := helpers.NewTestServer(t)
	poolID := createPool(t, srv, "p")

	// When: AdminSetUserMFAPreference targets a username that resolves to nobody
	resp := cognitoCall(t, srv, "AdminSetUserMFAPreference", map[string]any{
		"UserPoolId":               poolID,
		"Username":                 "no-such-user",
		"SoftwareTokenMfaSettings": map[string]any{"Enabled": false},
	})
	defer resp.Body.Close()

	// Then: Cognito reports the user as not found
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "UserNotFoundException")
}

// ─── AdminSetUserPassword ──────────────────────────────────────────────────
//
// TestAdminSetUserPassword_plainPool_subUsername (sub, plain pool) and
// TestAdminSetUserPassword_emailPool_usernameAttribute (UsernameAttributes)
// already cover those two resolution modes; this adds the AliasAttributes
// case and the not-found case.

func TestAdminSetUserPassword_aliasPool_emailAlias(t *testing.T) {
	// Given: an AliasAttributes:[email] pool with a user created under a fixed username
	srv := helpers.NewTestServer(t)
	poolID := createPoolWithAliasAttributes(t, srv, "p", []string{"email"})
	clientID := createClient(t, srv, poolID, "app")
	resp := cognitoCall(t, srv, "AdminCreateUser", map[string]any{
		"UserPoolId":    poolID,
		"Username":      "olga",
		"MessageAction": "SUPPRESS",
		"UserAttributes": []map[string]string{
			{"Name": "email", "Value": "olga@example.com"},
			{"Name": "email_verified", "Value": "true"},
		},
	})
	resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)

	// When: AdminSetUserPassword is called with the email alias as Username
	resp = cognitoCall(t, srv, "AdminSetUserPassword", map[string]any{
		"UserPoolId": poolID,
		"Username":   "olga@example.com",
		"Password":   "OlgaPass1!",
		"Permanent":  true,
	})
	resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)

	// Then: the password was set on the alias-resolved user
	resp = cognitoCall(t, srv, "InitiateAuth", map[string]any{
		"ClientId": clientID,
		"AuthFlow": "USER_PASSWORD_AUTH",
		"AuthParameters": map[string]string{
			"USERNAME": "olga",
			"PASSWORD": "OlgaPass1!",
		},
	})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
}

func TestAdminSetUserPassword_userNotFound(t *testing.T) {
	// Given: a pool with no matching user
	srv := helpers.NewTestServer(t)
	poolID := createPool(t, srv, "p")

	// When: AdminSetUserPassword targets a username that resolves to nobody
	resp := cognitoCall(t, srv, "AdminSetUserPassword", map[string]any{
		"UserPoolId": poolID,
		"Username":   "no-such-user",
		"Password":   "Whatever1!",
		"Permanent":  true,
	})
	defer resp.Body.Close()

	// Then: Cognito reports the user as not found
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "UserNotFoundException")
}
