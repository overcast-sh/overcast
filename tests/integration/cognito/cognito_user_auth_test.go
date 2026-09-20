package cognito_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/overcast-sh/overcast/tests/helpers"
)

func TestInitiateAuth_userAuthSelectChallenge(t *testing.T) {
	// Given: a confirmed user in a user pool
	srv := helpers.NewTestServer(t)
	poolID := createPool(t, srv, "p")
	clientID := createClientWithExplicitAuthFlows(t, srv, poolID, "app", []string{"ALLOW_USER_AUTH", "ALLOW_REFRESH_TOKEN_AUTH"})
	createConfirmedUser(t, srv, poolID, "choice-user", "ChoicePass1!")

	// When: USER_AUTH is initiated with only USERNAME
	resp := cognitoCall(t, srv, "InitiateAuth", map[string]any{
		"AuthFlow": "USER_AUTH",
		"ClientId": clientID,
		"AuthParameters": map[string]string{
			"USERNAME": "choice-user",
		},
	})
	defer resp.Body.Close()

	// Then: Cognito returns a SELECT_CHALLENGE response with PASSWORD available
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		ChallengeName       string            `json:"ChallengeName"`
		AvailableChallenges []string          `json:"AvailableChallenges"`
		ChallengeParameters map[string]string `json:"ChallengeParameters"`
		Session             string            `json:"Session"`
	}
	helpers.DecodeJSON(t, resp, &result)
	if result.ChallengeName != "SELECT_CHALLENGE" {
		t.Fatalf("expected SELECT_CHALLENGE, got %q", result.ChallengeName)
	}
	if result.Session == "" {
		t.Fatal("expected non-empty Session")
	}
	if !containsString(result.AvailableChallenges, "PASSWORD") {
		t.Fatalf("expected PASSWORD in AvailableChallenges, got %#v", result.AvailableChallenges)
	}
	if result.ChallengeParameters["USERNAME"] != "choice-user" {
		t.Fatalf("expected challenge USERNAME choice-user, got %q", result.ChallengeParameters["USERNAME"])
	}
}

func TestInitiateAuth_userAuthAllowedFirstAuthFactorsPassword(t *testing.T) {
	// Given: a user pool whose first auth factors allow PASSWORD
	srv := helpers.NewTestServer(t)
	poolID := createPoolWithAllowedFirstAuthFactors(t, srv, "p", []string{"PASSWORD"})
	clientID := createClientWithExplicitAuthFlows(t, srv, poolID, "app", []string{"ALLOW_USER_AUTH", "ALLOW_REFRESH_TOKEN_AUTH"})
	createConfirmedUser(t, srv, poolID, "factor-password-user", "ChoicePass1!")

	// When: USER_AUTH is initiated with only USERNAME
	resp := cognitoCall(t, srv, "InitiateAuth", map[string]any{
		"AuthFlow": "USER_AUTH",
		"ClientId": clientID,
		"AuthParameters": map[string]string{
			"USERNAME": "factor-password-user",
		},
	})
	defer resp.Body.Close()

	// Then: PASSWORD remains available as the first factor
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		ChallengeName       string   `json:"ChallengeName"`
		AvailableChallenges []string `json:"AvailableChallenges"`
	}
	helpers.DecodeJSON(t, resp, &result)
	if result.ChallengeName != "SELECT_CHALLENGE" {
		t.Fatalf("expected SELECT_CHALLENGE, got %q", result.ChallengeName)
	}
	if !containsString(result.AvailableChallenges, "PASSWORD") {
		t.Fatalf("expected PASSWORD in AvailableChallenges, got %#v", result.AvailableChallenges)
	}
}

func TestInitiateAuth_userAuthAllowedFirstAuthFactorsWithoutPassword(t *testing.T) {
	// Given: a user pool whose first auth factors allow EMAIL_OTP but not PASSWORD
	srv := helpers.NewTestServer(t)
	poolID := createPoolWithAllowedFirstAuthFactors(t, srv, "p", []string{"EMAIL_OTP"})
	clientID := createClientWithExplicitAuthFlows(t, srv, poolID, "app", []string{"ALLOW_USER_AUTH", "ALLOW_REFRESH_TOKEN_AUTH"})
	createConfirmedUser(t, srv, poolID, "factor-email-user", "ChoicePass1!")

	// When: USER_AUTH is initiated with only USERNAME
	resp := cognitoCall(t, srv, "InitiateAuth", map[string]any{
		"AuthFlow": "USER_AUTH",
		"ClientId": clientID,
		"AuthParameters": map[string]string{
			"USERNAME": "factor-email-user",
		},
	})
	defer resp.Body.Close()

	// Then: Cognito offers EMAIL_OTP as the available first factor
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		ChallengeName       string   `json:"ChallengeName"`
		AvailableChallenges []string `json:"AvailableChallenges"`
	}
	helpers.DecodeJSON(t, resp, &result)
	if result.ChallengeName != "SELECT_CHALLENGE" {
		t.Fatalf("expected SELECT_CHALLENGE, got %q", result.ChallengeName)
	}
	if !containsString(result.AvailableChallenges, "EMAIL_OTP") || containsString(result.AvailableChallenges, "PASSWORD") {
		t.Fatalf("expected only EMAIL_OTP available, got %#v", result.AvailableChallenges)
	}
}

func TestAdminInitiateAuth_userAuthAllowedFirstAuthFactorsWithoutPassword(t *testing.T) {
	// Given: a user pool whose first auth factors allow EMAIL_OTP but not PASSWORD
	srv := helpers.NewTestServer(t)
	poolID := createPoolWithAllowedFirstAuthFactors(t, srv, "p", []string{"EMAIL_OTP"})
	clientID := createClientWithExplicitAuthFlows(t, srv, poolID, "app", []string{"ALLOW_USER_AUTH", "ALLOW_REFRESH_TOKEN_AUTH"})
	createConfirmedUser(t, srv, poolID, "admin-factor-email-user", "ChoicePass1!")

	// When: admin USER_AUTH is initiated with only USERNAME
	resp := cognitoCall(t, srv, "AdminInitiateAuth", map[string]any{
		"AuthFlow":   "USER_AUTH",
		"UserPoolId": poolID,
		"ClientId":   clientID,
		"AuthParameters": map[string]string{
			"USERNAME": "admin-factor-email-user",
		},
	})
	defer resp.Body.Close()

	// Then: Cognito offers EMAIL_OTP as the available first factor
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		ChallengeName       string   `json:"ChallengeName"`
		AvailableChallenges []string `json:"AvailableChallenges"`
	}
	helpers.DecodeJSON(t, resp, &result)
	if result.ChallengeName != "SELECT_CHALLENGE" {
		t.Fatalf("expected SELECT_CHALLENGE, got %q", result.ChallengeName)
	}
	if !containsString(result.AvailableChallenges, "EMAIL_OTP") || containsString(result.AvailableChallenges, "PASSWORD") {
		t.Fatalf("expected only EMAIL_OTP available, got %#v", result.AvailableChallenges)
	}
}

func TestRespondToAuthChallenge_selectChallengeEmailOTP(t *testing.T) {
	// Given: a USER_AUTH SELECT_CHALLENGE session with EMAIL_OTP available
	srv := helpers.NewTestServer(t)
	poolID := createPoolWithAllowedFirstAuthFactors(t, srv, "p", []string{"EMAIL_OTP"})
	clientID := createClientWithExplicitAuthFlows(t, srv, poolID, "app", []string{"ALLOW_USER_AUTH", "ALLOW_REFRESH_TOKEN_AUTH"})
	createConfirmedUser(t, srv, poolID, "choice-email-otp-user", "ChoicePass1!")
	resp := cognitoCall(t, srv, "InitiateAuth", map[string]any{
		"AuthFlow": "USER_AUTH",
		"ClientId": clientID,
		"AuthParameters": map[string]string{
			"USERNAME": "choice-email-otp-user",
		},
	})
	helpers.AssertStatus(t, resp, http.StatusOK)
	var initResult struct {
		Session string `json:"Session"`
	}
	helpers.DecodeJSON(t, resp, &initResult)
	resp.Body.Close()

	// When: the user selects EMAIL_OTP
	resp = cognitoCall(t, srv, "RespondToAuthChallenge", map[string]any{
		"ClientId":      clientID,
		"ChallengeName": "SELECT_CHALLENGE",
		"Session":       initResult.Session,
		"ChallengeResponses": map[string]string{
			"USERNAME": "choice-email-otp-user",
			"ANSWER":   "EMAIL_OTP",
		},
	})
	helpers.AssertStatus(t, resp, http.StatusOK)
	var otpChallenge struct {
		ChallengeName       string            `json:"ChallengeName"`
		ChallengeParameters map[string]string `json:"ChallengeParameters"`
		Session             string            `json:"Session"`
	}
	helpers.DecodeJSON(t, resp, &otpChallenge)
	resp.Body.Close()
	if otpChallenge.ChallengeName != "EMAIL_OTP" {
		t.Fatalf("expected EMAIL_OTP challenge, got %q", otpChallenge.ChallengeName)
	}
	if otpChallenge.ChallengeParameters["CODE_DELIVERY_DELIVERY_MEDIUM"] != "EMAIL" {
		t.Fatalf("expected email delivery parameters, got %#v", otpChallenge.ChallengeParameters)
	}
	code := authChallengeCode(t, srv, poolID, "choice-email-otp-user", "EMAIL_OTP")

	// Then: submitting the delivered OTP completes authentication
	resp = cognitoCall(t, srv, "RespondToAuthChallenge", map[string]any{
		"ClientId":      clientID,
		"ChallengeName": "EMAIL_OTP",
		"Session":       otpChallenge.Session,
		"ChallengeResponses": map[string]string{
			"USERNAME":       "choice-email-otp-user",
			"EMAIL_OTP_CODE": code,
		},
	})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	var authResult struct {
		AuthenticationResult struct {
			AccessToken string `json:"AccessToken"`
		} `json:"AuthenticationResult"`
	}
	helpers.DecodeJSON(t, resp, &authResult)
	if authResult.AuthenticationResult.AccessToken == "" {
		t.Fatal("expected authentication token")
	}
}

func TestInitiateAuth_userSRPAuth(t *testing.T) {
	// Given: a confirmed user and a client with USER_SRP_AUTH enabled
	srv := helpers.NewTestServer(t)
	poolID := createPool(t, srv, "p")
	clientID := createClientWithExplicitAuthFlows(t, srv, poolID, "app", []string{"ALLOW_USER_SRP_AUTH", "ALLOW_REFRESH_TOKEN_AUTH"})
	createConfirmedUser(t, srv, poolID, "srp-user", "ChoicePass1!")

	// When: USER_SRP_AUTH starts with SRP_A
	resp := cognitoCall(t, srv, "InitiateAuth", map[string]any{
		"AuthFlow": "USER_SRP_AUTH",
		"ClientId": clientID,
		"AuthParameters": map[string]string{
			"USERNAME": "srp-user",
			"SRP_A":    "abcdef1234567890",
		},
	})
	helpers.AssertStatus(t, resp, http.StatusOK)
	var initResult struct {
		ChallengeName       string            `json:"ChallengeName"`
		ChallengeParameters map[string]string `json:"ChallengeParameters"`
		Session             string            `json:"Session"`
	}
	helpers.DecodeJSON(t, resp, &initResult)
	resp.Body.Close()
	if initResult.ChallengeName != "PASSWORD_VERIFIER" || initResult.Session == "" {
		t.Fatalf("expected PASSWORD_VERIFIER with session, got %#v", initResult)
	}
	for _, key := range []string{"SALT", "SRP_B", "SECRET_BLOCK", "USER_ID_FOR_SRP"} {
		if initResult.ChallengeParameters[key] == "" {
			t.Fatalf("expected %s challenge parameter in %#v", key, initResult.ChallengeParameters)
		}
	}

	// Then: responding with SRP verifier fields completes authentication
	resp = cognitoCall(t, srv, "RespondToAuthChallenge", map[string]any{
		"ClientId":      clientID,
		"ChallengeName": "PASSWORD_VERIFIER",
		"Session":       initResult.Session,
		"ChallengeResponses": map[string]string{
			"USERNAME":                    "srp-user",
			"PASSWORD_CLAIM_SIGNATURE":    "signature",
			"PASSWORD_CLAIM_SECRET_BLOCK": initResult.ChallengeParameters["SECRET_BLOCK"],
			"TIMESTAMP":                   "Mon May 18 12:00:00 UTC 2026",
		},
	})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	var authResult struct {
		AuthenticationResult struct {
			AccessToken string `json:"AccessToken"`
		} `json:"AuthenticationResult"`
	}
	helpers.DecodeJSON(t, resp, &authResult)
	if authResult.AuthenticationResult.AccessToken == "" {
		t.Fatal("expected authentication token")
	}
}

func TestRespondToAuthChallenge_selectChallengePasswordSRP(t *testing.T) {
	// Given: a USER_AUTH SELECT_CHALLENGE session with password factors available
	srv := helpers.NewTestServer(t)
	poolID := createPool(t, srv, "p")
	clientID := createClientWithExplicitAuthFlows(t, srv, poolID, "app", []string{"ALLOW_USER_AUTH", "ALLOW_REFRESH_TOKEN_AUTH"})
	createConfirmedUser(t, srv, poolID, "choice-srp-user", "ChoicePass1!")
	resp := cognitoCall(t, srv, "InitiateAuth", map[string]any{
		"AuthFlow": "USER_AUTH",
		"ClientId": clientID,
		"AuthParameters": map[string]string{
			"USERNAME": "choice-srp-user",
		},
	})
	helpers.AssertStatus(t, resp, http.StatusOK)
	var initResult struct {
		AvailableChallenges []string `json:"AvailableChallenges"`
		Session             string   `json:"Session"`
	}
	helpers.DecodeJSON(t, resp, &initResult)
	resp.Body.Close()
	if !containsString(initResult.AvailableChallenges, "PASSWORD_SRP") {
		t.Fatalf("expected PASSWORD_SRP in AvailableChallenges, got %#v", initResult.AvailableChallenges)
	}

	// When: the user selects PASSWORD_SRP and supplies SRP_A
	resp = cognitoCall(t, srv, "RespondToAuthChallenge", map[string]any{
		"ClientId":      clientID,
		"ChallengeName": "SELECT_CHALLENGE",
		"Session":       initResult.Session,
		"ChallengeResponses": map[string]string{
			"USERNAME": "choice-srp-user",
			"ANSWER":   "PASSWORD_SRP",
			"SRP_A":    "abcdef1234567890",
		},
	})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	var srpChallenge struct {
		ChallengeName       string            `json:"ChallengeName"`
		ChallengeParameters map[string]string `json:"ChallengeParameters"`
		Session             string            `json:"Session"`
	}
	helpers.DecodeJSON(t, resp, &srpChallenge)
	if srpChallenge.ChallengeName != "PASSWORD_VERIFIER" || srpChallenge.Session == "" {
		t.Fatalf("expected PASSWORD_VERIFIER challenge, got %#v", srpChallenge)
	}

	// Then: responding to the SRP verifier challenge completes authentication
	resp = cognitoCall(t, srv, "RespondToAuthChallenge", map[string]any{
		"ClientId":      clientID,
		"ChallengeName": "PASSWORD_VERIFIER",
		"Session":       srpChallenge.Session,
		"ChallengeResponses": map[string]string{
			"USERNAME":                    "choice-srp-user",
			"PASSWORD_CLAIM_SIGNATURE":    "signature",
			"PASSWORD_CLAIM_SECRET_BLOCK": srpChallenge.ChallengeParameters["SECRET_BLOCK"],
			"TIMESTAMP":                   "Mon May 18 12:00:00 UTC 2026",
		},
	})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	var authResult struct {
		AuthenticationResult struct {
			AccessToken string `json:"AccessToken"`
		} `json:"AuthenticationResult"`
	}
	helpers.DecodeJSON(t, resp, &authResult)
	if authResult.AuthenticationResult.AccessToken == "" {
		t.Fatal("expected authentication token")
	}

}

func TestAdminInitiateAuth_userSRPAuth(t *testing.T) {
	// Given: a confirmed user and an admin client with USER_SRP_AUTH enabled
	srv := helpers.NewTestServer(t)
	poolID := createPool(t, srv, "p")
	clientID := createClientWithExplicitAuthFlows(t, srv, poolID, "app", []string{"ALLOW_USER_SRP_AUTH", "ALLOW_REFRESH_TOKEN_AUTH"})
	createConfirmedUser(t, srv, poolID, "admin-srp-user", "ChoicePass1!")

	// When: AdminInitiateAuth starts USER_SRP_AUTH with SRP_A
	resp := cognitoCall(t, srv, "AdminInitiateAuth", map[string]any{
		"AuthFlow":   "USER_SRP_AUTH",
		"ClientId":   clientID,
		"UserPoolId": poolID,
		"AuthParameters": map[string]string{
			"USERNAME": "admin-srp-user",
			"SRP_A":    "abcdef1234567890",
		},
	})
	helpers.AssertStatus(t, resp, http.StatusOK)
	var initResult struct {
		ChallengeName       string            `json:"ChallengeName"`
		ChallengeParameters map[string]string `json:"ChallengeParameters"`
		Session             string            `json:"Session"`
	}
	helpers.DecodeJSON(t, resp, &initResult)
	resp.Body.Close()
	if initResult.ChallengeName != "PASSWORD_VERIFIER" || initResult.Session == "" {
		t.Fatalf("expected PASSWORD_VERIFIER with session, got %#v", initResult)
	}
	for _, key := range []string{"SALT", "SRP_B", "SECRET_BLOCK", "USER_ID_FOR_SRP"} {
		if initResult.ChallengeParameters[key] == "" {
			t.Fatalf("expected %s challenge parameter in %#v", key, initResult.ChallengeParameters)
		}
	}

	// Then: AdminRespondToAuthChallenge completes authentication with verifier fields
	resp = cognitoCall(t, srv, "AdminRespondToAuthChallenge", map[string]any{
		"ClientId":      clientID,
		"UserPoolId":    poolID,
		"ChallengeName": "PASSWORD_VERIFIER",
		"Session":       initResult.Session,
		"ChallengeResponses": map[string]string{
			"USERNAME":                    "admin-srp-user",
			"PASSWORD_CLAIM_SIGNATURE":    "signature",
			"PASSWORD_CLAIM_SECRET_BLOCK": initResult.ChallengeParameters["SECRET_BLOCK"],
			"TIMESTAMP":                   "Mon May 18 12:00:00 UTC 2026",
		},
	})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	var authResult struct {
		AuthenticationResult struct {
			AccessToken string `json:"AccessToken"`
		} `json:"AuthenticationResult"`
	}
	helpers.DecodeJSON(t, resp, &authResult)
	if authResult.AuthenticationResult.AccessToken == "" {
		t.Fatal("expected authentication token")
	}
}

func TestInitiateAuth_customAuth(t *testing.T) {
	// Given: a confirmed user and a client with CUSTOM_AUTH enabled
	srv := helpers.NewTestServer(t)
	poolID := createPool(t, srv, "p")
	clientID := createClientWithExplicitAuthFlows(t, srv, poolID, "app", []string{"ALLOW_CUSTOM_AUTH", "ALLOW_REFRESH_TOKEN_AUTH"})
	createConfirmedUser(t, srv, poolID, "custom-user", "ChoicePass1!")

	// When: CUSTOM_AUTH is initiated with a custom challenge
	resp := cognitoCall(t, srv, "InitiateAuth", map[string]any{
		"AuthFlow": "CUSTOM_AUTH",
		"ClientId": clientID,
		"AuthParameters": map[string]string{
			"USERNAME":       "custom-user",
			"CHALLENGE_NAME": "CUSTOM_CHALLENGE",
		},
	})
	helpers.AssertStatus(t, resp, http.StatusOK)
	var initResult struct {
		ChallengeName       string            `json:"ChallengeName"`
		ChallengeParameters map[string]string `json:"ChallengeParameters"`
		Session             string            `json:"Session"`
	}
	helpers.DecodeJSON(t, resp, &initResult)
	resp.Body.Close()
	if initResult.ChallengeName != "CUSTOM_CHALLENGE" || initResult.Session == "" {
		t.Fatalf("expected CUSTOM_CHALLENGE with session, got %#v", initResult)
	}
	if initResult.ChallengeParameters["USERNAME"] != "custom-user" {
		t.Fatalf("expected challenge USERNAME custom-user, got %#v", initResult.ChallengeParameters)
	}

	// Then: answering the custom challenge completes authentication
	resp = cognitoCall(t, srv, "RespondToAuthChallenge", map[string]any{
		"ClientId":      clientID,
		"ChallengeName": "CUSTOM_CHALLENGE",
		"Session":       initResult.Session,
		"ChallengeResponses": map[string]string{
			"USERNAME": "custom-user",
			"ANSWER":   "accepted",
		},
	})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	var authResult struct {
		AuthenticationResult struct {
			AccessToken string `json:"AccessToken"`
		} `json:"AuthenticationResult"`
	}
	helpers.DecodeJSON(t, resp, &authResult)
	if authResult.AuthenticationResult.AccessToken == "" {
		t.Fatal("expected authentication token")
	}
}

func TestInitiateAuth_customAuthSRPPrelude(t *testing.T) {
	// Given: a confirmed user and a client with CUSTOM_AUTH enabled
	srv := helpers.NewTestServer(t)
	poolID := createPool(t, srv, "p")
	clientID := createClientWithExplicitAuthFlows(t, srv, poolID, "app", []string{"ALLOW_CUSTOM_AUTH", "ALLOW_REFRESH_TOKEN_AUTH"})
	createConfirmedUser(t, srv, poolID, "custom-srp-user", "ChoicePass1!")

	// When: CUSTOM_AUTH starts with the documented SRP prelude
	resp := cognitoCall(t, srv, "InitiateAuth", map[string]any{
		"AuthFlow": "CUSTOM_AUTH",
		"ClientId": clientID,
		"AuthParameters": map[string]string{
			"USERNAME":       "custom-srp-user",
			"CHALLENGE_NAME": "SRP_A",
			"SRP_A":          "abcdef1234567890",
		},
	})
	defer resp.Body.Close()

	// Then: Cognito returns an SRP PASSWORD_VERIFIER challenge
	helpers.AssertStatus(t, resp, http.StatusOK)
	var initResult struct {
		ChallengeName       string            `json:"ChallengeName"`
		ChallengeParameters map[string]string `json:"ChallengeParameters"`
		Session             string            `json:"Session"`
	}
	helpers.DecodeJSON(t, resp, &initResult)
	if initResult.ChallengeName != "PASSWORD_VERIFIER" || initResult.Session == "" {
		t.Fatalf("expected PASSWORD_VERIFIER with session, got %#v", initResult)
	}
	for _, key := range []string{"SALT", "SRP_B", "SECRET_BLOCK", "USER_ID_FOR_SRP"} {
		if initResult.ChallengeParameters[key] == "" {
			t.Fatalf("expected %s challenge parameter in %#v", key, initResult.ChallengeParameters)
		}
	}
}

func TestInitiateAuth_customAuthExplicitFlowRequired(t *testing.T) {
	// Given: a confirmed user and a client without CUSTOM_AUTH enabled
	srv := helpers.NewTestServer(t)
	poolID := createPool(t, srv, "p")
	clientID := createClientWithExplicitAuthFlows(t, srv, poolID, "app", []string{"ALLOW_USER_PASSWORD_AUTH", "ALLOW_REFRESH_TOKEN_AUTH"})
	createConfirmedUser(t, srv, poolID, "custom-denied-user", "ChoicePass1!")

	// When: CUSTOM_AUTH is initiated
	resp := cognitoCall(t, srv, "InitiateAuth", map[string]any{
		"AuthFlow": "CUSTOM_AUTH",
		"ClientId": clientID,
		"AuthParameters": map[string]string{
			"USERNAME":       "custom-denied-user",
			"CHALLENGE_NAME": "CUSTOM_CHALLENGE",
		},
	})
	defer resp.Body.Close()

	// Then: Cognito rejects the disabled app-client auth flow
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "UnsupportedOperationException")
}

func TestAdminInitiateAuth_customAuth(t *testing.T) {
	// Given: a confirmed user and an admin client with CUSTOM_AUTH enabled
	srv := helpers.NewTestServer(t)
	poolID := createPool(t, srv, "p")
	clientID := createClientWithExplicitAuthFlows(t, srv, poolID, "app", []string{"ALLOW_CUSTOM_AUTH", "ALLOW_REFRESH_TOKEN_AUTH"})
	createConfirmedUser(t, srv, poolID, "admin-custom-user", "ChoicePass1!")

	// When: AdminInitiateAuth starts CUSTOM_AUTH with a custom challenge
	resp := cognitoCall(t, srv, "AdminInitiateAuth", map[string]any{
		"AuthFlow":   "CUSTOM_AUTH",
		"UserPoolId": poolID,
		"ClientId":   clientID,
		"AuthParameters": map[string]string{
			"USERNAME":       "admin-custom-user",
			"CHALLENGE_NAME": "CUSTOM_CHALLENGE",
		},
	})
	helpers.AssertStatus(t, resp, http.StatusOK)
	var initResult struct {
		ChallengeName string `json:"ChallengeName"`
		Session       string `json:"Session"`
	}
	helpers.DecodeJSON(t, resp, &initResult)
	resp.Body.Close()
	if initResult.ChallengeName != "CUSTOM_CHALLENGE" || initResult.Session == "" {
		t.Fatalf("expected CUSTOM_CHALLENGE with session, got %#v", initResult)
	}

	// Then: AdminRespondToAuthChallenge completes authentication
	resp = cognitoCall(t, srv, "AdminRespondToAuthChallenge", map[string]any{
		"UserPoolId":    poolID,
		"ClientId":      clientID,
		"ChallengeName": "CUSTOM_CHALLENGE",
		"Session":       initResult.Session,
		"ChallengeResponses": map[string]string{
			"USERNAME": "admin-custom-user",
			"ANSWER":   "accepted",
		},
	})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	var authResult struct {
		AuthenticationResult struct {
			AccessToken string `json:"AccessToken"`
		} `json:"AuthenticationResult"`
	}
	helpers.DecodeJSON(t, resp, &authResult)
	if authResult.AuthenticationResult.AccessToken == "" {
		t.Fatal("expected authentication token")
	}
}

func TestInitiateAuth_deviceRememberedFlow(t *testing.T) {
	// Given: a pool with device tracking and a confirmed user
	srv := helpers.NewTestServer(t)
	poolID := createPoolWithDeviceConfiguration(t, srv, "p", map[string]any{
		"ChallengeRequiredOnNewDevice":     true,
		"DeviceOnlyRememberedOnUserPrompt": false,
	})
	clientID := createClientWithExplicitAuthFlows(t, srv, poolID, "app", []string{"ALLOW_USER_PASSWORD_AUTH", "ALLOW_REFRESH_TOKEN_AUTH"})
	createConfirmedUser(t, srv, poolID, "device-user", "ChoicePass1!")

	// When: the user signs in without a DEVICE_KEY
	resp := cognitoCall(t, srv, "InitiateAuth", map[string]any{
		"AuthFlow": "USER_PASSWORD_AUTH",
		"ClientId": clientID,
		"AuthParameters": map[string]string{
			"USERNAME": "device-user",
			"PASSWORD": "ChoicePass1!",
		},
	})
	helpers.AssertStatus(t, resp, http.StatusOK)
	var signIn struct {
		AuthenticationResult struct {
			AccessToken       string `json:"AccessToken"`
			NewDeviceMetadata struct {
				DeviceKey      string `json:"DeviceKey"`
				DeviceGroupKey string `json:"DeviceGroupKey"`
			} `json:"NewDeviceMetadata"`
		} `json:"AuthenticationResult"`
	}
	helpers.DecodeJSON(t, resp, &signIn)
	resp.Body.Close()
	deviceKey := signIn.AuthenticationResult.NewDeviceMetadata.DeviceKey
	if signIn.AuthenticationResult.AccessToken == "" || deviceKey == "" || signIn.AuthenticationResult.NewDeviceMetadata.DeviceGroupKey == "" {
		t.Fatalf("expected tokens and NewDeviceMetadata, got %#v", signIn.AuthenticationResult)
	}

	// And: the app confirms the device
	resp = cognitoCall(t, srv, "ConfirmDevice", map[string]any{
		"AccessToken": signIn.AuthenticationResult.AccessToken,
		"DeviceKey":   deviceKey,
		"DeviceName":  "test laptop",
		"DeviceSecretVerifierConfig": map[string]string{
			"PasswordVerifier": "verifier",
			"Salt":             "salt",
		},
	})
	helpers.AssertStatus(t, resp, http.StatusOK)
	var confirm struct {
		UserConfirmationNecessary bool `json:"UserConfirmationNecessary"`
	}
	helpers.DecodeJSON(t, resp, &confirm)
	resp.Body.Close()
	if confirm.UserConfirmationNecessary {
		t.Fatal("expected always-remember configuration to skip user confirmation")
	}

	// Then: the device is listed for the user
	resp = cognitoCall(t, srv, "ListDevices", map[string]any{
		"AccessToken": signIn.AuthenticationResult.AccessToken,
	})
	helpers.AssertStatus(t, resp, http.StatusOK)
	var devices struct {
		Devices []struct {
			DeviceKey        string `json:"DeviceKey"`
			DeviceAttributes []struct {
				Name  string `json:"Name"`
				Value string `json:"Value"`
			} `json:"DeviceAttributes"`
		} `json:"Devices"`
	}
	helpers.DecodeJSON(t, resp, &devices)
	resp.Body.Close()
	if len(devices.Devices) != 1 || devices.Devices[0].DeviceKey != deviceKey {
		t.Fatalf("expected confirmed device %q, got %#v", deviceKey, devices.Devices)
	}

	// When: the user signs in again with the remembered DEVICE_KEY
	resp = cognitoCall(t, srv, "InitiateAuth", map[string]any{
		"AuthFlow": "USER_PASSWORD_AUTH",
		"ClientId": clientID,
		"AuthParameters": map[string]string{
			"USERNAME":   "device-user",
			"PASSWORD":   "ChoicePass1!",
			"DEVICE_KEY": deviceKey,
		},
	})
	helpers.AssertStatus(t, resp, http.StatusOK)
	var deviceChallenge struct {
		ChallengeName       string            `json:"ChallengeName"`
		ChallengeParameters map[string]string `json:"ChallengeParameters"`
		Session             string            `json:"Session"`
	}
	helpers.DecodeJSON(t, resp, &deviceChallenge)
	resp.Body.Close()
	if deviceChallenge.ChallengeName != "DEVICE_SRP_AUTH" || deviceChallenge.Session == "" || deviceChallenge.ChallengeParameters["DEVICE_KEY"] != deviceKey {
		t.Fatalf("expected DEVICE_SRP_AUTH challenge, got %#v", deviceChallenge)
	}

	// And: the client responds with device SRP_A
	resp = cognitoCall(t, srv, "RespondToAuthChallenge", map[string]any{
		"ClientId":      clientID,
		"ChallengeName": "DEVICE_SRP_AUTH",
		"Session":       deviceChallenge.Session,
		"ChallengeResponses": map[string]string{
			"USERNAME":   "device-user",
			"DEVICE_KEY": deviceKey,
			"SRP_A":      "abcdef1234567890",
		},
	})
	helpers.AssertStatus(t, resp, http.StatusOK)
	var verifier struct {
		ChallengeName       string            `json:"ChallengeName"`
		ChallengeParameters map[string]string `json:"ChallengeParameters"`
		Session             string            `json:"Session"`
	}
	helpers.DecodeJSON(t, resp, &verifier)
	resp.Body.Close()
	if verifier.ChallengeName != "DEVICE_PASSWORD_VERIFIER" || verifier.Session == "" || verifier.ChallengeParameters["SECRET_BLOCK"] == "" {
		t.Fatalf("expected DEVICE_PASSWORD_VERIFIER challenge, got %#v", verifier)
	}

	// Then: the device password verifier response completes authentication
	resp = cognitoCall(t, srv, "RespondToAuthChallenge", map[string]any{
		"ClientId":      clientID,
		"ChallengeName": "DEVICE_PASSWORD_VERIFIER",
		"Session":       verifier.Session,
		"ChallengeResponses": map[string]string{
			"USERNAME":                    "device-user",
			"DEVICE_KEY":                  deviceKey,
			"PASSWORD_CLAIM_SIGNATURE":    "signature",
			"PASSWORD_CLAIM_SECRET_BLOCK": verifier.ChallengeParameters["SECRET_BLOCK"],
			"TIMESTAMP":                   "Tue May 19 12:00:00 UTC 2026",
		},
	})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	var authResult struct {
		AuthenticationResult struct {
			AccessToken string `json:"AccessToken"`
		} `json:"AuthenticationResult"`
	}
	helpers.DecodeJSON(t, resp, &authResult)
	if authResult.AuthenticationResult.AccessToken == "" {
		t.Fatal("expected authentication token")
	}
}

func TestDeviceManagement_userOperations(t *testing.T) {
	// Given: a signed-in user with a confirmed remembered device
	srv := helpers.NewTestServer(t)
	poolID := createPoolWithDeviceConfiguration(t, srv, "p", map[string]any{
		"ChallengeRequiredOnNewDevice":     true,
		"DeviceOnlyRememberedOnUserPrompt": false,
	})
	clientID := createClientWithExplicitAuthFlows(t, srv, poolID, "app", []string{"ALLOW_USER_PASSWORD_AUTH", "ALLOW_REFRESH_TOKEN_AUTH"})
	createConfirmedUser(t, srv, poolID, "device-mgmt-user", "ChoicePass1!")
	accessToken, deviceKey := signInAndConfirmDevice(t, srv, clientID, "device-mgmt-user", "ChoicePass1!", "primary laptop")

	// When: the user retrieves the device
	resp := cognitoCall(t, srv, "GetDevice", map[string]any{
		"AccessToken": accessToken,
		"DeviceKey":   deviceKey,
	})
	helpers.AssertStatus(t, resp, http.StatusOK)
	var getResult struct {
		Device struct {
			DeviceKey        string `json:"DeviceKey"`
			DeviceAttributes []struct {
				Name  string `json:"Name"`
				Value string `json:"Value"`
			} `json:"DeviceAttributes"`
		} `json:"Device"`
	}
	helpers.DecodeJSON(t, resp, &getResult)
	resp.Body.Close()
	if getResult.Device.DeviceKey != deviceKey || !hasDeviceAttribute(getResult.Device.DeviceAttributes, "device_name", "primary laptop") {
		t.Fatalf("expected device %q with name, got %#v", deviceKey, getResult.Device)
	}

	// And: the user marks the device not remembered
	resp = cognitoCall(t, srv, "UpdateDeviceStatus", map[string]any{
		"AccessToken":            accessToken,
		"DeviceKey":              deviceKey,
		"DeviceRememberedStatus": "not_remembered",
	})
	helpers.AssertStatus(t, resp, http.StatusOK)
	resp.Body.Close()

	// Then: sign-in with the device key no longer starts device SRP auth
	resp = cognitoCall(t, srv, "InitiateAuth", map[string]any{
		"AuthFlow": "USER_PASSWORD_AUTH",
		"ClientId": clientID,
		"AuthParameters": map[string]string{
			"USERNAME":   "device-mgmt-user",
			"PASSWORD":   "ChoicePass1!",
			"DEVICE_KEY": deviceKey,
		},
	})
	helpers.AssertStatus(t, resp, http.StatusOK)
	var signIn struct {
		ChallengeName        string `json:"ChallengeName"`
		AuthenticationResult struct {
			AccessToken string `json:"AccessToken"`
		} `json:"AuthenticationResult"`
	}
	helpers.DecodeJSON(t, resp, &signIn)
	resp.Body.Close()
	if signIn.ChallengeName != "" || signIn.AuthenticationResult.AccessToken == "" {
		t.Fatalf("expected direct token response after not_remembered, got %#v", signIn)
	}

	// And: forgetting the device removes it from ListDevices
	resp = cognitoCall(t, srv, "ForgetDevice", map[string]any{
		"AccessToken": accessToken,
		"DeviceKey":   deviceKey,
	})
	helpers.AssertStatus(t, resp, http.StatusOK)
	resp.Body.Close()
	resp = cognitoCall(t, srv, "ListDevices", map[string]any{"AccessToken": accessToken})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	var listResult struct {
		Devices []struct {
			DeviceKey string `json:"DeviceKey"`
		} `json:"Devices"`
	}
	helpers.DecodeJSON(t, resp, &listResult)
	if len(listResult.Devices) != 0 {
		t.Fatalf("expected no devices after ForgetDevice, got %#v", listResult.Devices)
	}
}

func TestDeviceManagement_adminOperations(t *testing.T) {
	// Given: a user with two confirmed devices
	srv := helpers.NewTestServer(t)
	poolID := createPoolWithDeviceConfiguration(t, srv, "p", map[string]any{
		"ChallengeRequiredOnNewDevice":     true,
		"DeviceOnlyRememberedOnUserPrompt": false,
	})
	clientID := createClientWithExplicitAuthFlows(t, srv, poolID, "app", []string{"ALLOW_USER_PASSWORD_AUTH", "ALLOW_REFRESH_TOKEN_AUTH"})
	createConfirmedUser(t, srv, poolID, "admin-device-user", "ChoicePass1!")
	_, firstDeviceKey := signInAndConfirmDevice(t, srv, clientID, "admin-device-user", "ChoicePass1!", "first device")
	_, secondDeviceKey := signInAndConfirmDevice(t, srv, clientID, "admin-device-user", "ChoicePass1!", "second device")

	// When: admin lists devices with a limit
	resp := cognitoCall(t, srv, "AdminListDevices", map[string]any{
		"UserPoolId": poolID,
		"Username":   "admin-device-user",
		"Limit":      1,
	})
	helpers.AssertStatus(t, resp, http.StatusOK)
	var firstPage struct {
		Devices []struct {
			DeviceKey string `json:"DeviceKey"`
		} `json:"Devices"`
		PaginationToken string `json:"PaginationToken"`
	}
	helpers.DecodeJSON(t, resp, &firstPage)
	resp.Body.Close()
	if len(firstPage.Devices) != 1 || firstPage.PaginationToken == "" {
		t.Fatalf("expected one device and pagination token, got %#v", firstPage)
	}

	// Then: the next page returns the other device
	resp = cognitoCall(t, srv, "AdminListDevices", map[string]any{
		"UserPoolId":      poolID,
		"Username":        "admin-device-user",
		"Limit":           1,
		"PaginationToken": firstPage.PaginationToken,
	})
	helpers.AssertStatus(t, resp, http.StatusOK)
	var secondPage struct {
		Devices []struct {
			DeviceKey string `json:"DeviceKey"`
		} `json:"Devices"`
	}
	helpers.DecodeJSON(t, resp, &secondPage)
	resp.Body.Close()
	if len(secondPage.Devices) != 1 || secondPage.Devices[0].DeviceKey == firstPage.Devices[0].DeviceKey {
		t.Fatalf("expected second page with different device, got %#v after %#v", secondPage, firstPage)
	}

	// And: admin can get, update, and forget a device
	resp = cognitoCall(t, srv, "AdminGetDevice", map[string]any{
		"UserPoolId": poolID,
		"Username":   "admin-device-user",
		"DeviceKey":  firstDeviceKey,
	})
	helpers.AssertStatus(t, resp, http.StatusOK)
	var getResult struct {
		Device struct {
			DeviceKey string `json:"DeviceKey"`
		} `json:"Device"`
	}
	helpers.DecodeJSON(t, resp, &getResult)
	resp.Body.Close()
	if getResult.Device.DeviceKey != firstDeviceKey {
		t.Fatalf("expected admin get device %q, got %#v", firstDeviceKey, getResult.Device)
	}

	resp = cognitoCall(t, srv, "AdminUpdateDeviceStatus", map[string]any{
		"UserPoolId":             poolID,
		"Username":               "admin-device-user",
		"DeviceKey":              secondDeviceKey,
		"DeviceRememberedStatus": "not_remembered",
	})
	helpers.AssertStatus(t, resp, http.StatusOK)
	resp.Body.Close()

	resp = cognitoCall(t, srv, "AdminForgetDevice", map[string]any{
		"UserPoolId": poolID,
		"Username":   "admin-device-user",
		"DeviceKey":  firstDeviceKey,
	})
	helpers.AssertStatus(t, resp, http.StatusOK)
	resp.Body.Close()

	resp = cognitoCall(t, srv, "AdminListDevices", map[string]any{
		"UserPoolId": poolID,
		"Username":   "admin-device-user",
	})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	var afterForget struct {
		Devices []struct {
			DeviceKey string `json:"DeviceKey"`
		} `json:"Devices"`
	}
	helpers.DecodeJSON(t, resp, &afterForget)
	if len(afterForget.Devices) != 1 || afterForget.Devices[0].DeviceKey != secondDeviceKey {
		t.Fatalf("expected only second device after admin forget, got %#v", afterForget.Devices)
	}
}

func TestListDevices_limitAboveMaximum(t *testing.T) {
	// Given: a signed-in user with a confirmed device
	srv := helpers.NewTestServer(t)
	poolID := createPoolWithDeviceConfiguration(t, srv, "p", map[string]any{
		"ChallengeRequiredOnNewDevice":     true,
		"DeviceOnlyRememberedOnUserPrompt": false,
	})
	clientID := createClientWithExplicitAuthFlows(t, srv, poolID, "app", []string{"ALLOW_USER_PASSWORD_AUTH", "ALLOW_REFRESH_TOKEN_AUTH"})
	createConfirmedUser(t, srv, poolID, "limit-device-user", "ChoicePass1!")
	accessToken, _ := signInAndConfirmDevice(t, srv, clientID, "limit-device-user", "ChoicePass1!", "device")

	// When: ListDevices is called with a Limit above the documented maximum of 60
	resp := cognitoCall(t, srv, "ListDevices", map[string]any{
		"AccessToken": accessToken,
		"Limit":       61,
	})
	defer resp.Body.Close()

	// Then: Cognito rejects the out-of-range limit
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "InvalidParameterException")
}

func TestListDevices_malformedPaginationToken(t *testing.T) {
	// Given: a signed-in user with a confirmed device
	srv := helpers.NewTestServer(t)
	poolID := createPoolWithDeviceConfiguration(t, srv, "p", map[string]any{
		"ChallengeRequiredOnNewDevice":     true,
		"DeviceOnlyRememberedOnUserPrompt": false,
	})
	clientID := createClientWithExplicitAuthFlows(t, srv, poolID, "app", []string{"ALLOW_USER_PASSWORD_AUTH", "ALLOW_REFRESH_TOKEN_AUTH"})
	createConfirmedUser(t, srv, poolID, "token-device-user", "ChoicePass1!")
	accessToken, _ := signInAndConfirmDevice(t, srv, clientID, "token-device-user", "ChoicePass1!", "device")

	// When: ListDevices is called with a PaginationToken that isn't the internal offset format
	resp := cognitoCall(t, srv, "ListDevices", map[string]any{
		"AccessToken":     accessToken,
		"PaginationToken": "not-a-token",
	})
	defer resp.Body.Close()

	// Then: Cognito rejects the malformed token
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "InvalidParameterException")
}

func TestListDevices_pagesWithLimitOne(t *testing.T) {
	// Given: a user with two confirmed devices
	srv := helpers.NewTestServer(t)
	poolID := createPoolWithDeviceConfiguration(t, srv, "p", map[string]any{
		"ChallengeRequiredOnNewDevice":     true,
		"DeviceOnlyRememberedOnUserPrompt": false,
	})
	clientID := createClientWithExplicitAuthFlows(t, srv, poolID, "app", []string{"ALLOW_USER_PASSWORD_AUTH", "ALLOW_REFRESH_TOKEN_AUTH"})
	createConfirmedUser(t, srv, poolID, "paging-device-user", "ChoicePass1!")
	accessToken, firstDeviceKey := signInAndConfirmDevice(t, srv, clientID, "paging-device-user", "ChoicePass1!", "first device")
	_, secondDeviceKey := signInAndConfirmDevice(t, srv, clientID, "paging-device-user", "ChoicePass1!", "second device")

	// When: ListDevices requests the first page with Limit=1
	resp := cognitoCall(t, srv, "ListDevices", map[string]any{
		"AccessToken": accessToken,
		"Limit":       1,
	})
	helpers.AssertStatus(t, resp, http.StatusOK)
	var firstPage struct {
		Devices []struct {
			DeviceKey string `json:"DeviceKey"`
		} `json:"Devices"`
		PaginationToken string `json:"PaginationToken"`
	}
	helpers.DecodeJSON(t, resp, &firstPage)
	resp.Body.Close()
	if len(firstPage.Devices) != 1 || firstPage.PaginationToken == "" {
		t.Fatalf("expected one device and a pagination token, got %#v", firstPage)
	}

	// Then: the second page returns the other device with no further token
	resp = cognitoCall(t, srv, "ListDevices", map[string]any{
		"AccessToken":     accessToken,
		"Limit":           1,
		"PaginationToken": firstPage.PaginationToken,
	})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	var secondPage struct {
		Devices []struct {
			DeviceKey string `json:"DeviceKey"`
		} `json:"Devices"`
		PaginationToken string `json:"PaginationToken"`
	}
	helpers.DecodeJSON(t, resp, &secondPage)
	if len(secondPage.Devices) != 1 || secondPage.PaginationToken != "" {
		t.Fatalf("expected final page with 1 device and no token, got %#v", secondPage)
	}
	seen := map[string]bool{firstPage.Devices[0].DeviceKey: true, secondPage.Devices[0].DeviceKey: true}
	if !seen[firstDeviceKey] || !seen[secondDeviceKey] {
		t.Fatalf("expected to see both devices across pages, got %#v then %#v", firstPage, secondPage)
	}
}

func TestGetDevice_unknownDeviceKey(t *testing.T) {
	// Given: a signed-in user with no matching device
	srv := helpers.NewTestServer(t)
	poolID := createPoolWithDeviceConfiguration(t, srv, "p", map[string]any{
		"ChallengeRequiredOnNewDevice":     true,
		"DeviceOnlyRememberedOnUserPrompt": false,
	})
	clientID := createClientWithExplicitAuthFlows(t, srv, poolID, "app", []string{"ALLOW_USER_PASSWORD_AUTH", "ALLOW_REFRESH_TOKEN_AUTH"})
	createConfirmedUser(t, srv, poolID, "get-device-user", "ChoicePass1!")
	accessToken := signInForAccessToken(t, srv, clientID, "get-device-user", "ChoicePass1!")

	// When: GetDevice is called with a device key the user never registered
	resp := cognitoCall(t, srv, "GetDevice", map[string]any{
		"AccessToken": accessToken,
		"DeviceKey":   "us-east-1_11111111-1111-1111-1111-111111111111",
	})
	defer resp.Body.Close()

	// Then: Cognito reports the device as not found
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "ResourceNotFoundException")
}

func TestUpdateDeviceStatus_unknownDeviceKey(t *testing.T) {
	// Given: a signed-in user with no matching device
	srv := helpers.NewTestServer(t)
	poolID := createPoolWithDeviceConfiguration(t, srv, "p", map[string]any{
		"ChallengeRequiredOnNewDevice":     true,
		"DeviceOnlyRememberedOnUserPrompt": false,
	})
	clientID := createClientWithExplicitAuthFlows(t, srv, poolID, "app", []string{"ALLOW_USER_PASSWORD_AUTH", "ALLOW_REFRESH_TOKEN_AUTH"})
	createConfirmedUser(t, srv, poolID, "update-device-user", "ChoicePass1!")
	accessToken := signInForAccessToken(t, srv, clientID, "update-device-user", "ChoicePass1!")

	// When: UpdateDeviceStatus targets a device key the user never registered
	resp := cognitoCall(t, srv, "UpdateDeviceStatus", map[string]any{
		"AccessToken":            accessToken,
		"DeviceKey":              "us-east-1_11111111-1111-1111-1111-111111111111",
		"DeviceRememberedStatus": "remembered",
	})
	defer resp.Body.Close()

	// Then: Cognito reports the device as not found
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "ResourceNotFoundException")
}

func TestRespondToAuthChallenge_webAuthn(t *testing.T) {
	// Given: a user pool with passkey sign-in enabled and a user with a registered credential
	srv := helpers.NewTestServer(t)
	poolID := createPoolWithAllowedFirstAuthFactors(t, srv, "p", []string{"PASSWORD", "WEB_AUTHN"})
	clientID := createClientWithExplicitAuthFlows(t, srv, poolID, "app", []string{"ALLOW_USER_AUTH", "ALLOW_USER_PASSWORD_AUTH", "ALLOW_REFRESH_TOKEN_AUTH"})
	createConfirmedUser(t, srv, poolID, "webauthn-user", "ChoicePass1!")
	resp := cognitoCall(t, srv, "SetUserPoolMfaConfig", map[string]any{
		"UserPoolId": poolID,
		"WebAuthnConfiguration": map[string]string{
			"RelyingPartyId":   "auth.example.com",
			"UserVerification": "preferred",
		},
	})
	helpers.AssertStatus(t, resp, http.StatusOK)
	resp.Body.Close()
	resp = cognitoCall(t, srv, "GetUserPoolMfaConfig", map[string]any{"UserPoolId": poolID})
	helpers.AssertStatus(t, resp, http.StatusOK)
	var mfaConfig struct {
		WebAuthnConfiguration struct {
			RelyingPartyID   string `json:"RelyingPartyId"`
			UserVerification string `json:"UserVerification"`
		} `json:"WebAuthnConfiguration"`
	}
	helpers.DecodeJSON(t, resp, &mfaConfig)
	resp.Body.Close()
	if mfaConfig.WebAuthnConfiguration.RelyingPartyID != "auth.example.com" || mfaConfig.WebAuthnConfiguration.UserVerification != "preferred" {
		t.Fatalf("unexpected WebAuthn config: %#v", mfaConfig.WebAuthnConfiguration)
	}
	accessToken := signInForAccessToken(t, srv, clientID, "webauthn-user", "ChoicePass1!")
	resp = cognitoCall(t, srv, "StartWebAuthnRegistration", map[string]any{"AccessToken": accessToken})
	helpers.AssertStatus(t, resp, http.StatusOK)
	var startResult struct {
		CredentialCreationOptions map[string]any `json:"CredentialCreationOptions"`
	}
	helpers.DecodeJSON(t, resp, &startResult)
	resp.Body.Close()
	if startResult.CredentialCreationOptions["challenge"] == "" {
		t.Fatalf("expected credential creation options, got %#v", startResult.CredentialCreationOptions)
	}
	resp = cognitoCall(t, srv, "CompleteWebAuthnRegistration", map[string]any{
		"AccessToken": accessToken,
		"Credential": map[string]any{
			"id":   "credential-id-1",
			"type": "public-key",
			"response": map[string]any{
				"clientDataJSON":    "client-data",
				"attestationObject": "attestation",
			},
		},
	})
	helpers.AssertStatus(t, resp, http.StatusOK)
	resp.Body.Close()

	// When: USER_AUTH is initiated with WEB_AUTHN as the preferred challenge
	resp = cognitoCall(t, srv, "InitiateAuth", map[string]any{
		"AuthFlow": "USER_AUTH",
		"ClientId": clientID,
		"AuthParameters": map[string]string{
			"USERNAME":            "webauthn-user",
			"PREFERRED_CHALLENGE": "WEB_AUTHN",
		},
	})
	helpers.AssertStatus(t, resp, http.StatusOK)
	var initResult struct {
		AvailableChallenges []string          `json:"AvailableChallenges"`
		ChallengeName       string            `json:"ChallengeName"`
		ChallengeParameters map[string]string `json:"ChallengeParameters"`
		Session             string            `json:"Session"`
	}
	helpers.DecodeJSON(t, resp, &initResult)
	resp.Body.Close()
	if initResult.ChallengeName != "WEB_AUTHN" || initResult.Session == "" {
		t.Fatalf("expected WEB_AUTHN challenge, got %#v", initResult)
	}
	if !containsString(initResult.AvailableChallenges, "WEB_AUTHN") {
		t.Fatalf("expected WEB_AUTHN in AvailableChallenges, got %#v", initResult.AvailableChallenges)
	}
	optionsJSON := initResult.ChallengeParameters["CREDENTIAL_REQUEST_OPTIONS"]
	if optionsJSON == "" {
		t.Fatalf("expected CREDENTIAL_REQUEST_OPTIONS in %#v", initResult.ChallengeParameters)
	}
	var options map[string]any
	if err := json.Unmarshal([]byte(optionsJSON), &options); err != nil {
		t.Fatalf("CREDENTIAL_REQUEST_OPTIONS was not JSON: %v", err)
	}
	if options["challenge"] == "" || options["rpId"] != "auth.example.com" {
		t.Fatalf("unexpected request options: %#v", options)
	}

	// Then: responding with a WebAuthn credential completes authentication
	resp = cognitoCall(t, srv, "RespondToAuthChallenge", map[string]any{
		"ClientId":      clientID,
		"ChallengeName": "WEB_AUTHN",
		"Session":       initResult.Session,
		"ChallengeResponses": map[string]string{
			"USERNAME":   "webauthn-user",
			"CREDENTIAL": `{"id":"credential-id-1","type":"public-key","response":{"clientDataJSON":"client-data","authenticatorData":"auth-data","signature":"signature"}}`,
		},
	})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	var authResult struct {
		AuthenticationResult struct {
			AccessToken string `json:"AccessToken"`
		} `json:"AuthenticationResult"`
	}
	helpers.DecodeJSON(t, resp, &authResult)
	if authResult.AuthenticationResult.AccessToken == "" {
		t.Fatal("expected authentication token")
	}
}

func TestInitiateAuth_userAuthPreferredSMSOTP(t *testing.T) {
	// Given: a user with a phone number and SMS_OTP enabled as a first factor
	srv := helpers.NewTestServer(t)
	poolID := createPoolWithAllowedFirstAuthFactors(t, srv, "p", []string{"SMS_OTP"})
	clientID := createClientWithExplicitAuthFlows(t, srv, poolID, "app", []string{"ALLOW_USER_AUTH", "ALLOW_REFRESH_TOKEN_AUTH"})
	createConfirmedUser(t, srv, poolID, "preferred-sms-otp-user", "ChoicePass1!")
	resp := cognitoCall(t, srv, "AdminUpdateUserAttributes", map[string]any{
		"UserPoolId": poolID,
		"Username":   "preferred-sms-otp-user",
		"UserAttributes": []map[string]string{
			{"Name": "phone_number", "Value": "+15555550123"},
			{"Name": "phone_number_verified", "Value": "true"},
		},
	})
	helpers.AssertStatus(t, resp, http.StatusOK)
	resp.Body.Close()

	// When: USER_AUTH requests SMS_OTP as the preferred challenge
	resp = cognitoCall(t, srv, "InitiateAuth", map[string]any{
		"AuthFlow": "USER_AUTH",
		"ClientId": clientID,
		"AuthParameters": map[string]string{
			"USERNAME":            "preferred-sms-otp-user",
			"PREFERRED_CHALLENGE": "SMS_OTP",
		},
	})
	helpers.AssertStatus(t, resp, http.StatusOK)
	var initResult struct {
		ChallengeName       string            `json:"ChallengeName"`
		ChallengeParameters map[string]string `json:"ChallengeParameters"`
		Session             string            `json:"Session"`
	}
	helpers.DecodeJSON(t, resp, &initResult)
	resp.Body.Close()
	if initResult.ChallengeName != "SMS_OTP" {
		t.Fatalf("expected SMS_OTP challenge, got %q", initResult.ChallengeName)
	}
	if initResult.ChallengeParameters["CODE_DELIVERY_DELIVERY_MEDIUM"] != "SMS" {
		t.Fatalf("expected SMS delivery parameters, got %#v", initResult.ChallengeParameters)
	}
	code := authChallengeCode(t, srv, poolID, "preferred-sms-otp-user", "SMS_OTP")

	// Then: submitting the SMS OTP completes authentication
	resp = cognitoCall(t, srv, "RespondToAuthChallenge", map[string]any{
		"ClientId":      clientID,
		"ChallengeName": "SMS_OTP",
		"Session":       initResult.Session,
		"ChallengeResponses": map[string]string{
			"USERNAME":     "preferred-sms-otp-user",
			"SMS_OTP_CODE": code,
		},
	})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
}

func TestRespondToAuthChallenge_selectChallengePassword(t *testing.T) {
	// Given: a USER_AUTH SELECT_CHALLENGE session for a confirmed user
	srv := helpers.NewTestServer(t)
	poolID := createPool(t, srv, "p")
	clientID := createClientWithExplicitAuthFlows(t, srv, poolID, "app", []string{"ALLOW_USER_AUTH", "ALLOW_REFRESH_TOKEN_AUTH"})
	createConfirmedUser(t, srv, poolID, "choice-password-user", "ChoicePass1!")
	resp := cognitoCall(t, srv, "InitiateAuth", map[string]any{
		"AuthFlow": "USER_AUTH",
		"ClientId": clientID,
		"AuthParameters": map[string]string{
			"USERNAME": "choice-password-user",
		},
	})
	helpers.AssertStatus(t, resp, http.StatusOK)
	var initResult struct {
		Session string `json:"Session"`
	}
	helpers.DecodeJSON(t, resp, &initResult)
	resp.Body.Close()

	// When: the user selects PASSWORD and supplies their password in the challenge response
	resp = cognitoCall(t, srv, "RespondToAuthChallenge", map[string]any{
		"ClientId":      clientID,
		"ChallengeName": "SELECT_CHALLENGE",
		"Session":       initResult.Session,
		"ChallengeResponses": map[string]string{
			"USERNAME": "choice-password-user",
			"ANSWER":   "PASSWORD",
			"PASSWORD": "ChoicePass1!",
		},
	})
	defer resp.Body.Close()

	// Then: authentication completes and returns tokens
	helpers.AssertStatus(t, resp, http.StatusOK)
	var authResult struct {
		AuthenticationResult struct {
			AccessToken  string `json:"AccessToken"`
			IdToken      string `json:"IdToken"`
			RefreshToken string `json:"RefreshToken"`
			TokenType    string `json:"TokenType"`
		} `json:"AuthenticationResult"`
	}
	helpers.DecodeJSON(t, resp, &authResult)
	if authResult.AuthenticationResult.AccessToken == "" || authResult.AuthenticationResult.IdToken == "" || authResult.AuthenticationResult.RefreshToken == "" {
		t.Fatal("expected authentication tokens")
	}
	if authResult.AuthenticationResult.TokenType != "Bearer" {
		t.Fatalf("expected Bearer token type, got %q", authResult.AuthenticationResult.TokenType)
	}
}

func TestInitiateAuth_userAuthPreferredPassword(t *testing.T) {
	// Given: a confirmed user and a client with USER_AUTH enabled
	srv := helpers.NewTestServer(t)
	poolID := createPool(t, srv, "p")
	clientID := createClientWithExplicitAuthFlows(t, srv, poolID, "app", []string{"ALLOW_USER_AUTH", "ALLOW_REFRESH_TOKEN_AUTH"})
	createConfirmedUser(t, srv, poolID, "preferred-password-user", "ChoicePass1!")

	// When: USER_AUTH requests PASSWORD as the preferred challenge
	resp := cognitoCall(t, srv, "InitiateAuth", map[string]any{
		"AuthFlow": "USER_AUTH",
		"ClientId": clientID,
		"AuthParameters": map[string]string{
			"USERNAME":            "preferred-password-user",
			"PREFERRED_CHALLENGE": "PASSWORD",
		},
	})
	defer resp.Body.Close()

	// Then: Cognito returns a PASSWORD challenge directly
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		ChallengeName       string            `json:"ChallengeName"`
		AvailableChallenges []string          `json:"AvailableChallenges"`
		ChallengeParameters map[string]string `json:"ChallengeParameters"`
		Session             string            `json:"Session"`
	}
	helpers.DecodeJSON(t, resp, &result)
	if result.ChallengeName != "PASSWORD" {
		t.Fatalf("expected PASSWORD challenge, got %q", result.ChallengeName)
	}
	if result.Session == "" {
		t.Fatal("expected non-empty Session")
	}
	if !containsString(result.AvailableChallenges, "PASSWORD") {
		t.Fatalf("expected PASSWORD in AvailableChallenges, got %#v", result.AvailableChallenges)
	}
	if result.ChallengeParameters["USERNAME"] != "preferred-password-user" {
		t.Fatalf("expected challenge USERNAME preferred-password-user, got %q", result.ChallengeParameters["USERNAME"])
	}
}

func TestRespondToAuthChallenge_passwordChallenge(t *testing.T) {
	// Given: a USER_AUTH PASSWORD challenge session for a confirmed user
	srv := helpers.NewTestServer(t)
	poolID := createPool(t, srv, "p")
	clientID := createClientWithExplicitAuthFlows(t, srv, poolID, "app", []string{"ALLOW_USER_AUTH", "ALLOW_REFRESH_TOKEN_AUTH"})
	createConfirmedUser(t, srv, poolID, "password-challenge-user", "ChoicePass1!")
	resp := cognitoCall(t, srv, "InitiateAuth", map[string]any{
		"AuthFlow": "USER_AUTH",
		"ClientId": clientID,
		"AuthParameters": map[string]string{
			"USERNAME":            "password-challenge-user",
			"PREFERRED_CHALLENGE": "PASSWORD",
		},
	})
	helpers.AssertStatus(t, resp, http.StatusOK)
	var initResult struct {
		Session string `json:"Session"`
	}
	helpers.DecodeJSON(t, resp, &initResult)
	resp.Body.Close()

	// When: the user responds to the PASSWORD challenge
	resp = cognitoCall(t, srv, "RespondToAuthChallenge", map[string]any{
		"ClientId":      clientID,
		"ChallengeName": "PASSWORD",
		"Session":       initResult.Session,
		"ChallengeResponses": map[string]string{
			"USERNAME": "password-challenge-user",
			"PASSWORD": "ChoicePass1!",
		},
	})
	defer resp.Body.Close()

	// Then: authentication completes and returns tokens
	helpers.AssertStatus(t, resp, http.StatusOK)
	var authResult struct {
		AuthenticationResult struct {
			AccessToken string `json:"AccessToken"`
			IdToken     string `json:"IdToken"`
			TokenType   string `json:"TokenType"`
		} `json:"AuthenticationResult"`
	}
	helpers.DecodeJSON(t, resp, &authResult)
	if authResult.AuthenticationResult.AccessToken == "" || authResult.AuthenticationResult.IdToken == "" {
		t.Fatal("expected authentication tokens")
	}
	if authResult.AuthenticationResult.TokenType != "Bearer" {
		t.Fatalf("expected Bearer token type, got %q", authResult.AuthenticationResult.TokenType)
	}
}

func TestInitiateAuth_userAuthPreferredUnavailableChallenge(t *testing.T) {
	// Given: a confirmed user and a client with USER_AUTH enabled
	srv := helpers.NewTestServer(t)
	poolID := createPool(t, srv, "p")
	clientID := createClientWithExplicitAuthFlows(t, srv, poolID, "app", []string{"ALLOW_USER_AUTH", "ALLOW_REFRESH_TOKEN_AUTH"})
	createConfirmedUser(t, srv, poolID, "preferred-unavailable-user", "ChoicePass1!")

	// When: USER_AUTH requests an unavailable preferred challenge
	resp := cognitoCall(t, srv, "InitiateAuth", map[string]any{
		"AuthFlow": "USER_AUTH",
		"ClientId": clientID,
		"AuthParameters": map[string]string{
			"USERNAME":            "preferred-unavailable-user",
			"PREFERRED_CHALLENGE": "WEB_AUTHN",
		},
	})
	defer resp.Body.Close()

	// Then: Cognito returns SELECT_CHALLENGE with PASSWORD as the available local factor
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		ChallengeName       string   `json:"ChallengeName"`
		AvailableChallenges []string `json:"AvailableChallenges"`
		Session             string   `json:"Session"`
	}
	helpers.DecodeJSON(t, resp, &result)
	if result.ChallengeName != "SELECT_CHALLENGE" {
		t.Fatalf("expected SELECT_CHALLENGE, got %q", result.ChallengeName)
	}
	if result.Session == "" {
		t.Fatal("expected non-empty Session")
	}
	if !containsString(result.AvailableChallenges, "PASSWORD") {
		t.Fatalf("expected PASSWORD in AvailableChallenges, got %#v", result.AvailableChallenges)
	}
}

func TestAdminRespondToAuthChallenge_selectChallengePassword(t *testing.T) {
	// Given: an admin USER_AUTH SELECT_CHALLENGE session for a confirmed user
	srv := helpers.NewTestServer(t)
	poolID := createPool(t, srv, "p")
	clientID := createClientWithExplicitAuthFlows(t, srv, poolID, "app", []string{"ALLOW_USER_AUTH", "ALLOW_REFRESH_TOKEN_AUTH"})
	createConfirmedUser(t, srv, poolID, "admin-choice-user", "ChoicePass1!")
	resp := cognitoCall(t, srv, "AdminInitiateAuth", map[string]any{
		"AuthFlow":   "USER_AUTH",
		"UserPoolId": poolID,
		"ClientId":   clientID,
		"AuthParameters": map[string]string{
			"USERNAME": "admin-choice-user",
		},
	})
	helpers.AssertStatus(t, resp, http.StatusOK)
	var initResult struct {
		ChallengeName       string   `json:"ChallengeName"`
		AvailableChallenges []string `json:"AvailableChallenges"`
		Session             string   `json:"Session"`
	}
	helpers.DecodeJSON(t, resp, &initResult)
	resp.Body.Close()
	if initResult.ChallengeName != "SELECT_CHALLENGE" || !containsString(initResult.AvailableChallenges, "PASSWORD") || initResult.Session == "" {
		t.Fatalf("expected SELECT_CHALLENGE with PASSWORD and Session, got %#v", initResult)
	}

	// When: the admin challenge response selects PASSWORD and supplies the password
	resp = cognitoCall(t, srv, "AdminRespondToAuthChallenge", map[string]any{
		"UserPoolId":    poolID,
		"ClientId":      clientID,
		"ChallengeName": "SELECT_CHALLENGE",
		"Session":       initResult.Session,
		"ChallengeResponses": map[string]string{
			"USERNAME": "admin-choice-user",
			"ANSWER":   "PASSWORD",
			"PASSWORD": "ChoicePass1!",
		},
	})
	defer resp.Body.Close()

	// Then: authentication completes and returns tokens
	helpers.AssertStatus(t, resp, http.StatusOK)
	var authResult struct {
		AuthenticationResult struct {
			AccessToken string `json:"AccessToken"`
			IdToken     string `json:"IdToken"`
			TokenType   string `json:"TokenType"`
		} `json:"AuthenticationResult"`
	}
	helpers.DecodeJSON(t, resp, &authResult)
	if authResult.AuthenticationResult.AccessToken == "" || authResult.AuthenticationResult.IdToken == "" {
		t.Fatal("expected authentication tokens")
	}
	if authResult.AuthenticationResult.TokenType != "Bearer" {
		t.Fatalf("expected Bearer token type, got %q", authResult.AuthenticationResult.TokenType)
	}
}

func TestAdminInitiateAuth_userAuthPreferredPassword(t *testing.T) {
	// Given: a confirmed user and a client with USER_AUTH enabled
	srv := helpers.NewTestServer(t)
	poolID := createPool(t, srv, "p")
	clientID := createClientWithExplicitAuthFlows(t, srv, poolID, "app", []string{"ALLOW_USER_AUTH", "ALLOW_REFRESH_TOKEN_AUTH"})
	createConfirmedUser(t, srv, poolID, "admin-preferred-password-user", "ChoicePass1!")

	// When: admin USER_AUTH requests PASSWORD as the preferred challenge
	resp := cognitoCall(t, srv, "AdminInitiateAuth", map[string]any{
		"AuthFlow":   "USER_AUTH",
		"UserPoolId": poolID,
		"ClientId":   clientID,
		"AuthParameters": map[string]string{
			"USERNAME":            "admin-preferred-password-user",
			"PREFERRED_CHALLENGE": "PASSWORD",
		},
	})
	defer resp.Body.Close()

	// Then: Cognito returns a PASSWORD challenge directly
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		ChallengeName       string            `json:"ChallengeName"`
		AvailableChallenges []string          `json:"AvailableChallenges"`
		ChallengeParameters map[string]string `json:"ChallengeParameters"`
		Session             string            `json:"Session"`
	}
	helpers.DecodeJSON(t, resp, &result)
	if result.ChallengeName != "PASSWORD" {
		t.Fatalf("expected PASSWORD challenge, got %q", result.ChallengeName)
	}
	if result.Session == "" {
		t.Fatal("expected non-empty Session")
	}
	if !containsString(result.AvailableChallenges, "PASSWORD") {
		t.Fatalf("expected PASSWORD in AvailableChallenges, got %#v", result.AvailableChallenges)
	}
	if result.ChallengeParameters["USERNAME"] != "admin-preferred-password-user" {
		t.Fatalf("expected challenge USERNAME admin-preferred-password-user, got %q", result.ChallengeParameters["USERNAME"])
	}
}

func TestAdminRespondToAuthChallenge_passwordChallenge(t *testing.T) {
	// Given: an admin USER_AUTH PASSWORD challenge session for a confirmed user
	srv := helpers.NewTestServer(t)
	poolID := createPool(t, srv, "p")
	clientID := createClientWithExplicitAuthFlows(t, srv, poolID, "app", []string{"ALLOW_USER_AUTH", "ALLOW_REFRESH_TOKEN_AUTH"})
	createConfirmedUser(t, srv, poolID, "admin-password-challenge-user", "ChoicePass1!")
	resp := cognitoCall(t, srv, "AdminInitiateAuth", map[string]any{
		"AuthFlow":   "USER_AUTH",
		"UserPoolId": poolID,
		"ClientId":   clientID,
		"AuthParameters": map[string]string{
			"USERNAME":            "admin-password-challenge-user",
			"PREFERRED_CHALLENGE": "PASSWORD",
		},
	})
	helpers.AssertStatus(t, resp, http.StatusOK)
	var initResult struct {
		Session string `json:"Session"`
	}
	helpers.DecodeJSON(t, resp, &initResult)
	resp.Body.Close()

	// When: the admin caller responds to the PASSWORD challenge
	resp = cognitoCall(t, srv, "AdminRespondToAuthChallenge", map[string]any{
		"UserPoolId":    poolID,
		"ClientId":      clientID,
		"ChallengeName": "PASSWORD",
		"Session":       initResult.Session,
		"ChallengeResponses": map[string]string{
			"USERNAME": "admin-password-challenge-user",
			"PASSWORD": "ChoicePass1!",
		},
	})
	defer resp.Body.Close()

	// Then: authentication completes and returns tokens
	helpers.AssertStatus(t, resp, http.StatusOK)
	var authResult struct {
		AuthenticationResult struct {
			AccessToken string `json:"AccessToken"`
			IdToken     string `json:"IdToken"`
			TokenType   string `json:"TokenType"`
		} `json:"AuthenticationResult"`
	}
	helpers.DecodeJSON(t, resp, &authResult)
	if authResult.AuthenticationResult.AccessToken == "" || authResult.AuthenticationResult.IdToken == "" {
		t.Fatal("expected authentication tokens")
	}
	if authResult.AuthenticationResult.TokenType != "Bearer" {
		t.Fatalf("expected Bearer token type, got %q", authResult.AuthenticationResult.TokenType)
	}
}

func TestAdminInitiateAuth_userAuthPreferredUnavailableChallenge(t *testing.T) {
	// Given: a confirmed user and a client with USER_AUTH enabled
	srv := helpers.NewTestServer(t)
	poolID := createPool(t, srv, "p")
	clientID := createClientWithExplicitAuthFlows(t, srv, poolID, "app", []string{"ALLOW_USER_AUTH", "ALLOW_REFRESH_TOKEN_AUTH"})
	createConfirmedUser(t, srv, poolID, "admin-preferred-unavailable-user", "ChoicePass1!")

	// When: admin USER_AUTH requests an unavailable preferred challenge
	resp := cognitoCall(t, srv, "AdminInitiateAuth", map[string]any{
		"AuthFlow":   "USER_AUTH",
		"UserPoolId": poolID,
		"ClientId":   clientID,
		"AuthParameters": map[string]string{
			"USERNAME":            "admin-preferred-unavailable-user",
			"PREFERRED_CHALLENGE": "WEB_AUTHN",
		},
	})
	defer resp.Body.Close()

	// Then: Cognito returns SELECT_CHALLENGE with PASSWORD as the available local factor
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		ChallengeName       string   `json:"ChallengeName"`
		AvailableChallenges []string `json:"AvailableChallenges"`
		Session             string   `json:"Session"`
	}
	helpers.DecodeJSON(t, resp, &result)
	if result.ChallengeName != "SELECT_CHALLENGE" {
		t.Fatalf("expected SELECT_CHALLENGE, got %q", result.ChallengeName)
	}
	if result.Session == "" {
		t.Fatal("expected non-empty Session")
	}
	if !containsString(result.AvailableChallenges, "PASSWORD") {
		t.Fatalf("expected PASSWORD in AvailableChallenges, got %#v", result.AvailableChallenges)
	}
}

func TestInitiateAuth_userAuthDefaultClient(t *testing.T) {
	// Given: a default app client without ALLOW_USER_AUTH
	srv := helpers.NewTestServer(t)
	poolID := createPool(t, srv, "p")
	clientID := createClient(t, srv, poolID, "app")
	createConfirmedUser(t, srv, poolID, "default-choice-user", "ChoicePass1!")

	// When: USER_AUTH is initiated
	resp := cognitoCall(t, srv, "InitiateAuth", map[string]any{
		"AuthFlow": "USER_AUTH",
		"ClientId": clientID,
		"AuthParameters": map[string]string{
			"USERNAME": "default-choice-user",
		},
	})
	defer resp.Body.Close()

	// Then: Cognito rejects the operation because USER_AUTH isn't enabled for the client
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "UnsupportedOperationException")
}

func TestAdminInitiateAuth_userAuthDefaultClient(t *testing.T) {
	// Given: a default app client without ALLOW_USER_AUTH
	srv := helpers.NewTestServer(t)
	poolID := createPool(t, srv, "p")
	clientID := createClient(t, srv, poolID, "app")
	createConfirmedUser(t, srv, poolID, "admin-default-choice-user", "ChoicePass1!")

	// When: admin USER_AUTH is initiated
	resp := cognitoCall(t, srv, "AdminInitiateAuth", map[string]any{
		"AuthFlow":   "USER_AUTH",
		"UserPoolId": poolID,
		"ClientId":   clientID,
		"AuthParameters": map[string]string{
			"USERNAME": "admin-default-choice-user",
		},
	})
	defer resp.Body.Close()

	// Then: Cognito rejects the operation because USER_AUTH isn't enabled for the client
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "UnsupportedOperationException")
}

func TestCreateUserPoolClient_explicitAuthFlows(t *testing.T) {
	// Given: a user pool
	srv := helpers.NewTestServer(t)
	poolID := createPool(t, srv, "p")

	// When: a client is created with explicit auth flows
	resp := cognitoCall(t, srv, "CreateUserPoolClient", map[string]any{
		"UserPoolId": poolID,
		"ClientName": "explicit-app",
		"ExplicitAuthFlows": []string{
			"ALLOW_USER_AUTH",
			"ALLOW_REFRESH_TOKEN_AUTH",
		},
	})
	defer resp.Body.Close()

	// Then: the response includes the configured flows
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		UserPoolClient struct {
			ExplicitAuthFlows []string `json:"ExplicitAuthFlows"`
		} `json:"UserPoolClient"`
	}
	helpers.DecodeJSON(t, resp, &result)
	if !containsString(result.UserPoolClient.ExplicitAuthFlows, "ALLOW_USER_AUTH") || !containsString(result.UserPoolClient.ExplicitAuthFlows, "ALLOW_REFRESH_TOKEN_AUTH") {
		t.Fatalf("expected explicit auth flows in response, got %#v", result.UserPoolClient.ExplicitAuthFlows)
	}
}

func TestCreateUserPool_signInPolicyAllowedFirstAuthFactors(t *testing.T) {
	// Given: an empty store
	srv := helpers.NewTestServer(t)

	// When: a user pool is created with SignInPolicy.AllowedFirstAuthFactors
	resp := cognitoCall(t, srv, "CreateUserPool", map[string]any{
		"PoolName": "signin-policy-pool",
		"Policies": map[string]any{
			"SignInPolicy": map[string]any{
				"AllowedFirstAuthFactors": []string{"PASSWORD", "EMAIL_OTP"},
			},
		},
	})
	defer resp.Body.Close()

	// Then: the response includes the sign-in policy
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		UserPool struct {
			Policies struct {
				SignInPolicy struct {
					AllowedFirstAuthFactors []string `json:"AllowedFirstAuthFactors"`
				} `json:"SignInPolicy"`
			} `json:"Policies"`
		} `json:"UserPool"`
	}
	helpers.DecodeJSON(t, resp, &result)
	if !containsString(result.UserPool.Policies.SignInPolicy.AllowedFirstAuthFactors, "PASSWORD") || !containsString(result.UserPool.Policies.SignInPolicy.AllowedFirstAuthFactors, "EMAIL_OTP") {
		t.Fatalf("expected sign-in factors in response, got %#v", result.UserPool.Policies.SignInPolicy.AllowedFirstAuthFactors)
	}
}

func TestCreateUserPool_liteTierSignInPolicy(t *testing.T) {
	// Given: an empty store
	srv := helpers.NewTestServer(t)

	// When: a LITE-tier pool is created with choice-auth SignInPolicy
	resp := cognitoCall(t, srv, "CreateUserPool", map[string]any{
		"PoolName":     "lite-signin-policy-pool",
		"UserPoolTier": "LITE",
		"Policies": map[string]any{
			"SignInPolicy": map[string]any{
				"AllowedFirstAuthFactors": []string{"PASSWORD", "EMAIL_OTP"},
			},
		},
	})
	defer resp.Body.Close()

	// Then: Cognito rejects the feature as unavailable in the selected tier
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "FeatureUnavailableInTierException")
}

func TestCreateUserPoolClient_liteTierUserAuth(t *testing.T) {
	// Given: a LITE-tier user pool
	srv := helpers.NewTestServer(t)
	resp := cognitoCall(t, srv, "CreateUserPool", map[string]any{
		"PoolName":     "lite-client-pool",
		"UserPoolTier": "LITE",
	})
	helpers.AssertStatus(t, resp, http.StatusOK)
	var poolResult struct {
		UserPool struct {
			Id           string `json:"Id"`
			UserPoolTier string `json:"UserPoolTier"`
		} `json:"UserPool"`
	}
	helpers.DecodeJSON(t, resp, &poolResult)
	resp.Body.Close()
	if poolResult.UserPool.UserPoolTier != "LITE" {
		t.Fatalf("expected LITE tier in response, got %q", poolResult.UserPool.UserPoolTier)
	}

	// When: a client enables ALLOW_USER_AUTH
	resp = cognitoCall(t, srv, "CreateUserPoolClient", map[string]any{
		"UserPoolId": poolResult.UserPool.Id,
		"ClientName": "lite-app",
		"ExplicitAuthFlows": []string{
			"ALLOW_USER_AUTH",
			"ALLOW_REFRESH_TOKEN_AUTH",
		},
	})
	defer resp.Body.Close()

	// Then: Cognito rejects USER_AUTH because it requires Essentials tier or higher
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "FeatureUnavailableInTierException")
}

func TestUpdateUserPoolClient_liteTierUserAuth(t *testing.T) {
	// Given: a LITE-tier pool with a client that doesn't enable USER_AUTH
	srv := helpers.NewTestServer(t)
	resp := cognitoCall(t, srv, "CreateUserPool", map[string]any{
		"PoolName":     "lite-update-client-pool",
		"UserPoolTier": "LITE",
	})
	helpers.AssertStatus(t, resp, http.StatusOK)
	var poolResult struct {
		UserPool struct {
			Id string `json:"Id"`
		} `json:"UserPool"`
	}
	helpers.DecodeJSON(t, resp, &poolResult)
	resp.Body.Close()
	clientID := createClientWithExplicitAuthFlows(t, srv, poolResult.UserPool.Id, "lite-update-app", []string{"ALLOW_REFRESH_TOKEN_AUTH", "ALLOW_USER_PASSWORD_AUTH"})

	// When: UpdateUserPoolClient enables ALLOW_USER_AUTH
	resp = cognitoCall(t, srv, "UpdateUserPoolClient", map[string]any{
		"UserPoolId": poolResult.UserPool.Id,
		"ClientId":   clientID,
		"ExplicitAuthFlows": []string{
			"ALLOW_USER_AUTH",
			"ALLOW_REFRESH_TOKEN_AUTH",
		},
	})
	defer resp.Body.Close()

	// Then: Cognito rejects USER_AUTH because it requires Essentials tier or higher
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "FeatureUnavailableInTierException")
}

func TestUpdateUserPool_liteTierSignInPolicyRejected(t *testing.T) {
	// Given: a LITE-tier user pool
	srv := helpers.NewTestServer(t)
	poolID := createPoolWithTier(t, srv, "lite-update-signin-pool", "LITE")

	// When: UpdateUserPool tries to set a choice-auth SignInPolicy
	resp := cognitoCall(t, srv, "UpdateUserPool", map[string]any{
		"UserPoolId": poolID,
		"Policies": map[string]any{
			"SignInPolicy": map[string]any{
				"AllowedFirstAuthFactors": []string{"PASSWORD", "EMAIL_OTP"},
			},
		},
	})
	defer resp.Body.Close()

	// Then: Cognito rejects the feature as unavailable in the LITE tier. The AWS
	// model documents SignInPolicyType as requiring "the Essentials tier or
	// higher" to "activate this setting" at all, so this mirrors
	// TestCreateUserPool_liteTierSignInPolicy for the update path.
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "FeatureUnavailableInTierException")
}

func TestUpdateUserPool_liteTierUpgradeAllowsSignInPolicy(t *testing.T) {
	// Given: a LITE-tier user pool
	srv := helpers.NewTestServer(t)
	poolID := createPoolWithTier(t, srv, "lite-upgrade-signin-pool", "LITE")

	// When: the same request upgrades the tier to ESSENTIALS and sets a SignInPolicy
	resp := cognitoCall(t, srv, "UpdateUserPool", map[string]any{
		"UserPoolId":   poolID,
		"UserPoolTier": "ESSENTIALS",
		"Policies": map[string]any{
			"SignInPolicy": map[string]any{
				"AllowedFirstAuthFactors": []string{"PASSWORD", "EMAIL_OTP"},
			},
		},
	})
	resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)

	// Then: DescribeUserPool reflects both the new tier and the sign-in policy
	resp = cognitoCall(t, srv, "DescribeUserPool", map[string]any{"UserPoolId": poolID})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		UserPool struct {
			UserPoolTier string `json:"UserPoolTier"`
			Policies     struct {
				SignInPolicy struct {
					AllowedFirstAuthFactors []string `json:"AllowedFirstAuthFactors"`
				} `json:"SignInPolicy"`
			} `json:"Policies"`
		} `json:"UserPool"`
	}
	helpers.DecodeJSON(t, resp, &result)
	if result.UserPool.UserPoolTier != "ESSENTIALS" {
		t.Fatalf("expected ESSENTIALS tier, got %q", result.UserPool.UserPoolTier)
	}
	if !containsString(result.UserPool.Policies.SignInPolicy.AllowedFirstAuthFactors, "EMAIL_OTP") {
		t.Fatalf("expected EMAIL_OTP in sign-in factors, got %#v", result.UserPool.Policies.SignInPolicy.AllowedFirstAuthFactors)
	}
}

func TestUpdateUserPool_signInPolicyAllowedFirstAuthFactors(t *testing.T) {
	// Given: a user pool with default sign-in policy
	srv := helpers.NewTestServer(t)
	poolID := createPool(t, srv, "p")

	// When: the user pool is updated with SignInPolicy.AllowedFirstAuthFactors
	resp := cognitoCall(t, srv, "UpdateUserPool", map[string]any{
		"UserPoolId": poolID,
		"Policies": map[string]any{
			"SignInPolicy": map[string]any{
				"AllowedFirstAuthFactors": []string{"EMAIL_OTP"},
			},
		},
	})
	resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)

	// Then: DescribeUserPool includes the updated sign-in policy
	resp = cognitoCall(t, srv, "DescribeUserPool", map[string]any{
		"UserPoolId": poolID,
	})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		UserPool struct {
			Policies struct {
				SignInPolicy struct {
					AllowedFirstAuthFactors []string `json:"AllowedFirstAuthFactors"`
				} `json:"SignInPolicy"`
			} `json:"Policies"`
		} `json:"UserPool"`
	}
	helpers.DecodeJSON(t, resp, &result)
	if !containsString(result.UserPool.Policies.SignInPolicy.AllowedFirstAuthFactors, "EMAIL_OTP") {
		t.Fatalf("expected EMAIL_OTP in updated sign-in factors, got %#v", result.UserPool.Policies.SignInPolicy.AllowedFirstAuthFactors)
	}
}

func TestCreateUserPool_signInPolicyInvalidFirstAuthFactor(t *testing.T) {
	// Given: an empty store
	srv := helpers.NewTestServer(t)

	// When: a user pool is created with an invalid first auth factor
	resp := cognitoCall(t, srv, "CreateUserPool", map[string]any{
		"PoolName": "invalid-signin-policy-pool",
		"Policies": map[string]any{
			"SignInPolicy": map[string]any{
				"AllowedFirstAuthFactors": []string{"PASSWORD", "NOT_REAL"},
			},
		},
	})
	defer resp.Body.Close()

	// Then: Cognito rejects the invalid factor
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "InvalidParameterException")
}

func TestUpdateUserPool_signInPolicyEmptyFirstAuthFactors(t *testing.T) {
	// Given: a user pool
	srv := helpers.NewTestServer(t)
	poolID := createPool(t, srv, "p")

	// When: the user pool is updated with an empty first-factor list
	resp := cognitoCall(t, srv, "UpdateUserPool", map[string]any{
		"UserPoolId": poolID,
		"Policies": map[string]any{
			"SignInPolicy": map[string]any{
				"AllowedFirstAuthFactors": []string{},
			},
		},
	})
	defer resp.Body.Close()

	// Then: Cognito rejects the empty factor list
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "InvalidParameterException")
}

func TestCreateUserPool_signInPolicyDuplicateFirstAuthFactor(t *testing.T) {
	// Given: an empty store
	srv := helpers.NewTestServer(t)

	// When: a user pool is created with duplicate first auth factors
	resp := cognitoCall(t, srv, "CreateUserPool", map[string]any{
		"PoolName": "duplicate-signin-policy-pool",
		"Policies": map[string]any{
			"SignInPolicy": map[string]any{
				"AllowedFirstAuthFactors": []string{"PASSWORD", "PASSWORD"},
			},
		},
	})
	defer resp.Body.Close()

	// Then: Cognito rejects the duplicate factor
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "InvalidParameterException")
}

func TestCreateUserPool_signInPolicyTooManyFirstAuthFactors(t *testing.T) {
	// Given: an empty store
	srv := helpers.NewTestServer(t)

	// When: a user pool is created with more factors than the AWS shape allows
	resp := cognitoCall(t, srv, "CreateUserPool", map[string]any{
		"PoolName": "too-many-signin-policy-pool",
		"Policies": map[string]any{
			"SignInPolicy": map[string]any{
				"AllowedFirstAuthFactors": []string{"PASSWORD", "EMAIL_OTP", "SMS_OTP", "WEB_AUTHN", "PASSWORD"},
			},
		},
	})
	defer resp.Body.Close()

	// Then: Cognito rejects the overlong factor list
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "InvalidParameterException")
}

func TestUpdateUserPoolClient_explicitAuthFlows(t *testing.T) {
	// Given: a default app client and a confirmed user
	srv := helpers.NewTestServer(t)
	poolID := createPool(t, srv, "p")
	clientID := createClient(t, srv, poolID, "app")
	createConfirmedUser(t, srv, poolID, "updated-choice-user", "ChoicePass1!")

	// When: ExplicitAuthFlows is updated to allow USER_AUTH
	resp := cognitoCall(t, srv, "UpdateUserPoolClient", map[string]any{
		"UserPoolId": poolID,
		"ClientId":   clientID,
		"ExplicitAuthFlows": []string{
			"ALLOW_USER_AUTH",
			"ALLOW_REFRESH_TOKEN_AUTH",
		},
	})
	helpers.AssertStatus(t, resp, http.StatusOK)
	var updateResult struct {
		UserPoolClient struct {
			ExplicitAuthFlows []string `json:"ExplicitAuthFlows"`
		} `json:"UserPoolClient"`
	}
	helpers.DecodeJSON(t, resp, &updateResult)
	resp.Body.Close()
	if !containsString(updateResult.UserPoolClient.ExplicitAuthFlows, "ALLOW_USER_AUTH") {
		t.Fatalf("expected ALLOW_USER_AUTH in updated flows, got %#v", updateResult.UserPoolClient.ExplicitAuthFlows)
	}

	// Then: USER_AUTH is enabled for the updated client
	resp = cognitoCall(t, srv, "InitiateAuth", map[string]any{
		"AuthFlow": "USER_AUTH",
		"ClientId": clientID,
		"AuthParameters": map[string]string{
			"USERNAME": "updated-choice-user",
		},
	})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	var authResult struct {
		ChallengeName string `json:"ChallengeName"`
	}
	helpers.DecodeJSON(t, resp, &authResult)
	if authResult.ChallengeName != "SELECT_CHALLENGE" {
		t.Fatalf("expected SELECT_CHALLENGE, got %q", authResult.ChallengeName)
	}
}

func TestCreateUserPoolClient_explicitAuthFlowsInvalidValue(t *testing.T) {
	// Given: a user pool
	srv := helpers.NewTestServer(t)
	poolID := createPool(t, srv, "p")

	// When: a client is created with an unknown explicit auth flow
	resp := cognitoCall(t, srv, "CreateUserPoolClient", map[string]any{
		"UserPoolId":        poolID,
		"ClientName":        "invalid-flow-app",
		"ExplicitAuthFlows": []string{"ALLOW_USER_AUTH", "ALLOW_NOT_REAL"},
	})
	defer resp.Body.Close()

	// Then: Cognito rejects the invalid flow value
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "InvalidParameterException")
}

func TestCreateUserPoolClient_explicitAuthFlowsLegacyAndAllow(t *testing.T) {
	// Given: a user pool
	srv := helpers.NewTestServer(t)
	poolID := createPool(t, srv, "p")

	// When: a client mixes legacy flow names with ALLOW_ flow names
	resp := cognitoCall(t, srv, "CreateUserPoolClient", map[string]any{
		"UserPoolId":        poolID,
		"ClientName":        "mixed-flow-app",
		"ExplicitAuthFlows": []string{"USER_PASSWORD_AUTH", "ALLOW_USER_AUTH"},
	})
	defer resp.Body.Close()

	// Then: Cognito rejects the incompatible flow combination
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "InvalidParameterException")
}

func TestUpdateUserPoolClient_explicitAuthFlowsInvalidValue(t *testing.T) {
	// Given: an existing app client
	srv := helpers.NewTestServer(t)
	poolID := createPool(t, srv, "p")
	clientID := createClient(t, srv, poolID, "app")

	// When: the client is updated with an unknown explicit auth flow
	resp := cognitoCall(t, srv, "UpdateUserPoolClient", map[string]any{
		"UserPoolId":        poolID,
		"ClientId":          clientID,
		"ExplicitAuthFlows": []string{"ALLOW_NOT_REAL"},
	})
	defer resp.Body.Close()

	// Then: Cognito rejects the invalid flow value
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "InvalidParameterException")
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func hasDeviceAttribute(attrs []struct {
	Name  string `json:"Name"`
	Value string `json:"Value"`
}, name, value string) bool {
	for _, attr := range attrs {
		if attr.Name == name && attr.Value == value {
			return true
		}
	}
	return false
}

func signInAndConfirmDevice(t *testing.T, srv *helpers.TestServer, clientID, username, password, deviceName string) (string, string) {
	t.Helper()
	resp := cognitoCall(t, srv, "InitiateAuth", map[string]any{
		"AuthFlow": "USER_PASSWORD_AUTH",
		"ClientId": clientID,
		"AuthParameters": map[string]string{
			"USERNAME": username,
			"PASSWORD": password,
		},
	})
	helpers.AssertStatus(t, resp, http.StatusOK)
	var signIn struct {
		AuthenticationResult struct {
			AccessToken       string `json:"AccessToken"`
			NewDeviceMetadata struct {
				DeviceKey string `json:"DeviceKey"`
			} `json:"NewDeviceMetadata"`
		} `json:"AuthenticationResult"`
	}
	helpers.DecodeJSON(t, resp, &signIn)
	resp.Body.Close()
	deviceKey := signIn.AuthenticationResult.NewDeviceMetadata.DeviceKey
	if signIn.AuthenticationResult.AccessToken == "" || deviceKey == "" {
		t.Fatalf("expected access token and device key, got %#v", signIn.AuthenticationResult)
	}
	resp = cognitoCall(t, srv, "ConfirmDevice", map[string]any{
		"AccessToken": signIn.AuthenticationResult.AccessToken,
		"DeviceKey":   deviceKey,
		"DeviceName":  deviceName,
		"DeviceSecretVerifierConfig": map[string]string{
			"PasswordVerifier": "verifier",
			"Salt":             "salt",
		},
	})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	return signIn.AuthenticationResult.AccessToken, deviceKey
}

func authChallengeCode(t *testing.T, srv *helpers.TestServer, poolID, username, challengeName string) string {
	t.Helper()
	raw, ok, err := srv.Store.Get(context.Background(), "cognito:users:"+poolID, "us-east-1/"+poolID+"/"+username)
	if err != nil {
		t.Fatalf("get user state: %v", err)
	}
	if !ok {
		t.Fatalf("missing user state for %s", username)
	}
	var user struct {
		AuthChallengeCodes []struct {
			ChallengeName string `json:"ChallengeName"`
			Code          string `json:"Code"`
		} `json:"AuthChallengeCodes"`
	}
	if err := json.Unmarshal([]byte(raw), &user); err != nil {
		t.Fatalf("unmarshal user state: %v", err)
	}
	for _, challenge := range user.AuthChallengeCodes {
		if challenge.ChallengeName == challengeName {
			return challenge.Code
		}
	}
	t.Fatalf("missing auth challenge code for %s in %#v", challengeName, user.AuthChallengeCodes)
	return ""
}

func createConfirmedUser(t *testing.T, srv *helpers.TestServer, poolID, username, password string) {
	t.Helper()
	createCognitoUserWithEmail(t, srv, poolID, username, username+"@example.com")
	resp := cognitoCall(t, srv, "AdminSetUserPassword", map[string]any{
		"UserPoolId": poolID,
		"Username":   username,
		"Password":   password,
		"Permanent":  true,
	})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
}

func signInForAccessToken(t *testing.T, srv *helpers.TestServer, clientID, username, password string) string {
	t.Helper()
	resp := cognitoCall(t, srv, "InitiateAuth", map[string]any{
		"AuthFlow": "USER_PASSWORD_AUTH",
		"ClientId": clientID,
		"AuthParameters": map[string]string{
			"USERNAME": username,
			"PASSWORD": password,
		},
	})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		AuthenticationResult struct {
			AccessToken string `json:"AccessToken"`
		} `json:"AuthenticationResult"`
	}
	helpers.DecodeJSON(t, resp, &result)
	if result.AuthenticationResult.AccessToken == "" {
		t.Fatal("expected access token")
	}
	return result.AuthenticationResult.AccessToken
}

func createPoolWithTier(t *testing.T, srv *helpers.TestServer, name, tier string) string {
	t.Helper()
	resp := cognitoCall(t, srv, "CreateUserPool", map[string]any{
		"PoolName":     name,
		"UserPoolTier": tier,
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

func createPoolWithAllowedFirstAuthFactors(t *testing.T, srv *helpers.TestServer, name string, factors []string) string {
	t.Helper()
	resp := cognitoCall(t, srv, "CreateUserPool", map[string]any{
		"PoolName": name,
		"Policies": map[string]any{
			"SignInPolicy": map[string]any{
				"AllowedFirstAuthFactors": factors,
			},
		},
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

func createPoolWithDeviceConfiguration(t *testing.T, srv *helpers.TestServer, name string, deviceConfig map[string]any) string {
	t.Helper()
	resp := cognitoCall(t, srv, "CreateUserPool", map[string]any{
		"PoolName":            name,
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
