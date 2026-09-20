// Package cognito_test: SMS_MFA, SELECT_MFA_TYPE and MFA_SETUP challenge tests.
package cognito_test

import (
	"net/http"
	"testing"

	"github.com/overcast-sh/overcast/tests/helpers"
)

// ─── helpers ──────────────────────────────────────────────────────────────────

// setPoolMfaConfig configures the pool's MFA factors through SetUserPoolMfaConfig.
func setPoolMfaConfig(t *testing.T, srv *helpers.TestServer, poolID string, body map[string]any) {
	t.Helper()
	req := map[string]any{"UserPoolId": poolID}
	for k, v := range body {
		req[k] = v
	}
	resp := cognitoCall(t, srv, "SetUserPoolMfaConfig", req)
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
}

// createConfirmedUserWithPhone creates a confirmed user carrying a verified
// phone number, which is what makes SMS MFA available to them.
func createConfirmedUserWithPhone(t *testing.T, srv *helpers.TestServer, poolID, username, password, phone string) {
	t.Helper()
	resp := cognitoCall(t, srv, "AdminCreateUser", map[string]any{
		"UserPoolId": poolID,
		"Username":   username,
		"UserAttributes": []map[string]string{
			{"Name": "phone_number", "Value": phone},
			{"Name": "phone_number_verified", "Value": "true"},
		},
	})
	helpers.AssertStatus(t, resp, http.StatusOK)
	resp.Body.Close()
	resp = cognitoCall(t, srv, "AdminSetUserPassword", map[string]any{
		"UserPoolId": poolID, "Username": username,
		"Password": password, "Permanent": true,
	})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
}

type challengeResult struct {
	ChallengeName        string            `json:"ChallengeName"`
	Session              string            `json:"Session"`
	ChallengeParameters  map[string]string `json:"ChallengeParameters"`
	AuthenticationResult struct {
		AccessToken string `json:"AccessToken"`
	} `json:"AuthenticationResult"`
}

func decodeChallenge(t *testing.T, resp *http.Response) challengeResult {
	t.Helper()
	helpers.AssertStatus(t, resp, http.StatusOK)
	var out challengeResult
	helpers.DecodeJSON(t, resp, &out)
	return out
}

// ─── SMS_MFA ──────────────────────────────────────────────────────────────────

func TestInitiateAuth_smsMfaChallengeWhenPoolRequiresMfa(t *testing.T) {
	// Given: a pool that requires MFA with SMS as the only enabled factor, and
	// a user with a verified phone number.
	srv := helpers.NewTestServer(t)
	poolID := createPool(t, srv, "p")
	setPoolMfaConfig(t, srv, poolID, map[string]any{
		"MfaConfiguration":    "ON",
		"SmsMfaConfiguration": map[string]any{"SmsAuthenticationMessage": "Your code is {####}"},
	})
	clientID := createClient(t, srv, poolID, "app")
	createConfirmedUserWithPhone(t, srv, poolID, "smsmfa", "SmsPass1!", "+14155550123")

	// When: the user signs in with a password
	resp := cognitoCall(t, srv, "InitiateAuth", map[string]any{
		"ClientId": clientID, "AuthFlow": "USER_PASSWORD_AUTH",
		"AuthParameters": map[string]string{"USERNAME": "smsmfa", "PASSWORD": "SmsPass1!"},
	})
	challenge := decodeChallenge(t, resp)
	resp.Body.Close()

	// Then: Cognito answers SMS_MFA with masked delivery details
	if challenge.ChallengeName != "SMS_MFA" {
		t.Fatalf("expected SMS_MFA challenge, got %q", challenge.ChallengeName)
	}
	if challenge.Session == "" {
		t.Fatal("SMS_MFA challenge returned no Session")
	}
	if got := challenge.ChallengeParameters["CODE_DELIVERY_DELIVERY_MEDIUM"]; got != "SMS" {
		t.Errorf("CODE_DELIVERY_DELIVERY_MEDIUM = %q, want SMS", got)
	}
	if got := challenge.ChallengeParameters["CODE_DELIVERY_DESTINATION"]; got != "+*******0123" {
		t.Errorf("CODE_DELIVERY_DESTINATION = %q, want +*******0123", got)
	}
	if got := challenge.ChallengeParameters["USER_ID_FOR_SRP"]; got != "smsmfa" {
		t.Errorf("USER_ID_FOR_SRP = %q, want smsmfa", got)
	}

	// And: answering with the delivered code completes sign-in
	code := authChallengeCode(t, srv, poolID, "smsmfa", "SMS_MFA")
	resp = cognitoCall(t, srv, "RespondToAuthChallenge", map[string]any{
		"ClientId": clientID, "ChallengeName": "SMS_MFA", "Session": challenge.Session,
		"ChallengeResponses": map[string]string{"USERNAME": "smsmfa", "SMS_MFA_CODE": code},
	})
	done := decodeChallenge(t, resp)
	resp.Body.Close()
	if done.AuthenticationResult.AccessToken == "" {
		t.Fatal("RespondToAuthChallenge(SMS_MFA): empty AccessToken")
	}
}

func TestRespondToAuthChallenge_smsMfaWrongCodeIsCodeMismatch(t *testing.T) {
	// Given: an SMS_MFA challenge in flight
	srv := helpers.NewTestServer(t)
	poolID := createPool(t, srv, "p")
	setPoolMfaConfig(t, srv, poolID, map[string]any{
		"MfaConfiguration":    "ON",
		"SmsMfaConfiguration": map[string]any{"SmsAuthenticationMessage": "Your code is {####}"},
	})
	clientID := createClient(t, srv, poolID, "app")
	createConfirmedUserWithPhone(t, srv, poolID, "smsbad", "SmsPass1!", "+14155550199")
	resp := cognitoCall(t, srv, "InitiateAuth", map[string]any{
		"ClientId": clientID, "AuthFlow": "USER_PASSWORD_AUTH",
		"AuthParameters": map[string]string{"USERNAME": "smsbad", "PASSWORD": "SmsPass1!"},
	})
	challenge := decodeChallenge(t, resp)
	resp.Body.Close()

	// When: the wrong code is submitted
	resp = cognitoCall(t, srv, "RespondToAuthChallenge", map[string]any{
		"ClientId": clientID, "ChallengeName": "SMS_MFA", "Session": challenge.Session,
		"ChallengeResponses": map[string]string{"USERNAME": "smsbad", "SMS_MFA_CODE": "000000"},
	})
	defer resp.Body.Close()

	// Then: CodeMismatchException
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "CodeMismatchException")
}

func TestInitiateAuth_optionalMfaHonoursSmsPreference(t *testing.T) {
	// Given: an OPTIONAL-MFA pool and a user who opted in to SMS MFA
	srv := helpers.NewTestServer(t)
	poolID := createPool(t, srv, "p")
	setPoolMfaConfig(t, srv, poolID, map[string]any{
		"MfaConfiguration":    "OPTIONAL",
		"SmsMfaConfiguration": map[string]any{"SmsAuthenticationMessage": "Your code is {####}"},
	})
	clientID := createClient(t, srv, poolID, "app")
	createConfirmedUserWithPhone(t, srv, poolID, "optin", "SmsPass1!", "+14155550144")

	// A user who has not opted in signs straight in.
	resp := cognitoCall(t, srv, "InitiateAuth", map[string]any{
		"ClientId": clientID, "AuthFlow": "USER_PASSWORD_AUTH",
		"AuthParameters": map[string]string{"USERNAME": "optin", "PASSWORD": "SmsPass1!"},
	})
	before := decodeChallenge(t, resp)
	resp.Body.Close()
	if before.AuthenticationResult.AccessToken == "" {
		t.Fatalf("OPTIONAL MFA without a preference should issue tokens, got challenge %q", before.ChallengeName)
	}

	// When: the user activates SMS MFA
	resp = cognitoCall(t, srv, "AdminSetUserMFAPreference", map[string]any{
		"UserPoolId": poolID, "Username": "optin",
		"SMSMfaSettings": map[string]any{"Enabled": true, "PreferredMfa": true},
	})
	helpers.AssertStatus(t, resp, http.StatusOK)
	resp.Body.Close()

	// Then: the next sign-in is challenged
	resp = cognitoCall(t, srv, "InitiateAuth", map[string]any{
		"ClientId": clientID, "AuthFlow": "USER_PASSWORD_AUTH",
		"AuthParameters": map[string]string{"USERNAME": "optin", "PASSWORD": "SmsPass1!"},
	})
	after := decodeChallenge(t, resp)
	resp.Body.Close()
	if after.ChallengeName != "SMS_MFA" {
		t.Fatalf("expected SMS_MFA challenge after opting in, got %q", after.ChallengeName)
	}
}

func TestAdminInitiateAuth_smsMfaChallengeAndCompletion(t *testing.T) {
	// Given: a required-MFA pool with SMS and a verified phone number
	srv := helpers.NewTestServer(t)
	poolID := createPool(t, srv, "p")
	setPoolMfaConfig(t, srv, poolID, map[string]any{
		"MfaConfiguration":    "ON",
		"SmsMfaConfiguration": map[string]any{"SmsAuthenticationMessage": "Your code is {####}"},
	})
	clientID := createClient(t, srv, poolID, "app")
	createConfirmedUserWithPhone(t, srv, poolID, "adminsms", "SmsPass1!", "+14155550177")

	// When: the admin auth flow runs
	resp := cognitoCall(t, srv, "AdminInitiateAuth", map[string]any{
		"UserPoolId": poolID, "ClientId": clientID, "AuthFlow": "ADMIN_USER_PASSWORD_AUTH",
		"AuthParameters": map[string]string{"USERNAME": "adminsms", "PASSWORD": "SmsPass1!"},
	})
	challenge := decodeChallenge(t, resp)
	resp.Body.Close()
	if challenge.ChallengeName != "SMS_MFA" {
		t.Fatalf("expected SMS_MFA challenge, got %q", challenge.ChallengeName)
	}

	// Then: AdminRespondToAuthChallenge completes it
	code := authChallengeCode(t, srv, poolID, "adminsms", "SMS_MFA")
	resp = cognitoCall(t, srv, "AdminRespondToAuthChallenge", map[string]any{
		"UserPoolId": poolID, "ClientId": clientID, "ChallengeName": "SMS_MFA", "Session": challenge.Session,
		"ChallengeResponses": map[string]string{"USERNAME": "adminsms", "SMS_MFA_CODE": code},
	})
	done := decodeChallenge(t, resp)
	resp.Body.Close()
	if done.AuthenticationResult.AccessToken == "" {
		t.Fatal("AdminRespondToAuthChallenge(SMS_MFA): empty AccessToken")
	}
}

func TestInitiateAuth_srpFlowIssuesSmsMfaChallenge(t *testing.T) {
	// Given: a required-MFA pool with SMS, signed in over USER_SRP_AUTH
	srv := helpers.NewTestServer(t)
	poolID := createPool(t, srv, "p")
	setPoolMfaConfig(t, srv, poolID, map[string]any{
		"MfaConfiguration":    "ON",
		"SmsMfaConfiguration": map[string]any{"SmsAuthenticationMessage": "Your code is {####}"},
	})
	clientID := createClientWithExplicitAuthFlows(t, srv, poolID, "app", []string{"ALLOW_USER_SRP_AUTH", "ALLOW_REFRESH_TOKEN_AUTH"})
	createConfirmedUserWithPhone(t, srv, poolID, "srpsms", "SmsPass1!", "+14155550188")

	resp := cognitoCall(t, srv, "InitiateAuth", map[string]any{
		"ClientId": clientID, "AuthFlow": "USER_SRP_AUTH",
		"AuthParameters": map[string]string{"USERNAME": "srpsms", "SRP_A": "abc123"},
	})
	srp := decodeChallenge(t, resp)
	resp.Body.Close()
	if srp.ChallengeName != "PASSWORD_VERIFIER" {
		t.Fatalf("expected PASSWORD_VERIFIER, got %q", srp.ChallengeName)
	}

	// When: the SRP proof is submitted
	resp = cognitoCall(t, srv, "RespondToAuthChallenge", map[string]any{
		"ClientId": clientID, "ChallengeName": "PASSWORD_VERIFIER", "Session": srp.Session,
		"ChallengeResponses": map[string]string{
			"USERNAME":                    "srpsms",
			"PASSWORD_CLAIM_SIGNATURE":    "sig",
			"PASSWORD_CLAIM_SECRET_BLOCK": "blk",
			"TIMESTAMP":                   "Mon Jan 1 00:00:00 UTC 2024",
		},
	})
	challenge := decodeChallenge(t, resp)
	resp.Body.Close()

	// Then: the SMS_MFA challenge follows instead of tokens
	if challenge.ChallengeName != "SMS_MFA" {
		t.Fatalf("expected SMS_MFA after PASSWORD_VERIFIER, got %q", challenge.ChallengeName)
	}
	if challenge.AuthenticationResult.AccessToken != "" {
		t.Fatal("PASSWORD_VERIFIER issued tokens although MFA is required")
	}
}

// ─── SELECT_MFA_TYPE ──────────────────────────────────────────────────────────

func TestInitiateAuth_selectMfaTypeWhenTwoFactorsAreActive(t *testing.T) {
	// Given: a user with both TOTP and SMS MFA active and no preference
	srv := helpers.NewTestServer(t)
	poolID := createPool(t, srv, "p")
	setPoolMfaConfig(t, srv, poolID, map[string]any{
		"MfaConfiguration":              "OPTIONAL",
		"SmsMfaConfiguration":           map[string]any{"SmsAuthenticationMessage": "Your code is {####}"},
		"SoftwareTokenMfaConfiguration": map[string]any{"Enabled": true},
	})
	clientID := createClient(t, srv, poolID, "app")
	createConfirmedUserWithPhone(t, srv, poolID, "bothmfa", "SmsPass1!", "+14155550155")
	accessToken := enrollTOTP(t, srv, poolID, clientID, "bothmfa", "SmsPass1!")
	resp := cognitoCall(t, srv, "SetUserMFAPreference", map[string]any{
		"AccessToken":              accessToken,
		"SoftwareTokenMfaSettings": map[string]any{"Enabled": true},
		"SMSMfaSettings":           map[string]any{"Enabled": true},
	})
	helpers.AssertStatus(t, resp, http.StatusOK)
	resp.Body.Close()

	// When: the user signs in
	resp = cognitoCall(t, srv, "InitiateAuth", map[string]any{
		"ClientId": clientID, "AuthFlow": "USER_PASSWORD_AUTH",
		"AuthParameters": map[string]string{"USERNAME": "bothmfa", "PASSWORD": "SmsPass1!"},
	})
	challenge := decodeChallenge(t, resp)
	resp.Body.Close()

	// Then: SELECT_MFA_TYPE lists both factors
	if challenge.ChallengeName != "SELECT_MFA_TYPE" {
		t.Fatalf("expected SELECT_MFA_TYPE, got %q", challenge.ChallengeName)
	}
	if got := challenge.ChallengeParameters["MFAS_CAN_CHOOSE"]; got != `["SMS_MFA","SOFTWARE_TOKEN_MFA"]` {
		t.Errorf("MFAS_CAN_CHOOSE = %q", got)
	}

	// And: choosing SMS_MFA delivers a code and moves to that challenge
	resp = cognitoCall(t, srv, "RespondToAuthChallenge", map[string]any{
		"ClientId": clientID, "ChallengeName": "SELECT_MFA_TYPE", "Session": challenge.Session,
		"ChallengeResponses": map[string]string{"USERNAME": "bothmfa", "ANSWER": "SMS_MFA"},
	})
	selected := decodeChallenge(t, resp)
	resp.Body.Close()
	if selected.ChallengeName != "SMS_MFA" {
		t.Fatalf("expected SMS_MFA after SELECT_MFA_TYPE, got %q", selected.ChallengeName)
	}
	code := authChallengeCode(t, srv, poolID, "bothmfa", "SMS_MFA")
	resp = cognitoCall(t, srv, "RespondToAuthChallenge", map[string]any{
		"ClientId": clientID, "ChallengeName": "SMS_MFA", "Session": selected.Session,
		"ChallengeResponses": map[string]string{"USERNAME": "bothmfa", "SMS_MFA_CODE": code},
	})
	done := decodeChallenge(t, resp)
	resp.Body.Close()
	if done.AuthenticationResult.AccessToken == "" {
		t.Fatal("SELECT_MFA_TYPE -> SMS_MFA did not issue tokens")
	}
}

func TestRespondToAuthChallenge_selectMfaTypeRejectsInactiveFactor(t *testing.T) {
	// Given: a SELECT_MFA_TYPE challenge in flight
	srv := helpers.NewTestServer(t)
	poolID := createPool(t, srv, "p")
	setPoolMfaConfig(t, srv, poolID, map[string]any{
		"MfaConfiguration":              "OPTIONAL",
		"SmsMfaConfiguration":           map[string]any{"SmsAuthenticationMessage": "Your code is {####}"},
		"SoftwareTokenMfaConfiguration": map[string]any{"Enabled": true},
	})
	clientID := createClient(t, srv, poolID, "app")
	createConfirmedUserWithPhone(t, srv, poolID, "choosemfa", "SmsPass1!", "+14155550166")
	accessToken := enrollTOTP(t, srv, poolID, clientID, "choosemfa", "SmsPass1!")
	resp := cognitoCall(t, srv, "SetUserMFAPreference", map[string]any{
		"AccessToken":              accessToken,
		"SoftwareTokenMfaSettings": map[string]any{"Enabled": true},
		"SMSMfaSettings":           map[string]any{"Enabled": true},
	})
	helpers.AssertStatus(t, resp, http.StatusOK)
	resp.Body.Close()
	resp = cognitoCall(t, srv, "InitiateAuth", map[string]any{
		"ClientId": clientID, "AuthFlow": "USER_PASSWORD_AUTH",
		"AuthParameters": map[string]string{"USERNAME": "choosemfa", "PASSWORD": "SmsPass1!"},
	})
	challenge := decodeChallenge(t, resp)
	resp.Body.Close()

	// When: an MFA type the user has not activated is chosen
	resp = cognitoCall(t, srv, "RespondToAuthChallenge", map[string]any{
		"ClientId": clientID, "ChallengeName": "SELECT_MFA_TYPE", "Session": challenge.Session,
		"ChallengeResponses": map[string]string{"USERNAME": "choosemfa", "ANSWER": "EMAIL_MFA"},
	})
	defer resp.Body.Close()

	// Then: MFAMethodNotFoundException
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "MFAMethodNotFoundException")
}

// ─── MFA_SETUP ────────────────────────────────────────────────────────────────

func TestInitiateAuth_mfaSetupChallengeAndCompletion(t *testing.T) {
	// Given: a pool that requires MFA with TOTP enabled, and a user with no
	// MFA factor configured.
	srv := helpers.NewTestServer(t)
	poolID := createPool(t, srv, "p")
	setPoolMfaConfig(t, srv, poolID, map[string]any{
		"MfaConfiguration":              "ON",
		"SoftwareTokenMfaConfiguration": map[string]any{"Enabled": true},
	})
	clientID := createClient(t, srv, poolID, "app")
	createConfirmedUser(t, srv, poolID, "setupuser", "SetupPass1!")

	// When: the user signs in
	resp := cognitoCall(t, srv, "InitiateAuth", map[string]any{
		"ClientId": clientID, "AuthFlow": "USER_PASSWORD_AUTH",
		"AuthParameters": map[string]string{"USERNAME": "setupuser", "PASSWORD": "SetupPass1!"},
	})
	challenge := decodeChallenge(t, resp)
	resp.Body.Close()

	// Then: MFA_SETUP names the factors that can be set up
	if challenge.ChallengeName != "MFA_SETUP" {
		t.Fatalf("expected MFA_SETUP, got %q", challenge.ChallengeName)
	}
	if got := challenge.ChallengeParameters["MFAS_CAN_SETUP"]; got != `["SOFTWARE_TOKEN_MFA"]` {
		t.Errorf("MFAS_CAN_SETUP = %q", got)
	}

	// And: the session drives AssociateSoftwareToken / VerifySoftwareToken
	resp = cognitoCall(t, srv, "AssociateSoftwareToken", map[string]any{"Session": challenge.Session})
	helpers.AssertStatus(t, resp, http.StatusOK)
	var associated struct {
		SecretCode string `json:"SecretCode"`
		Session    string `json:"Session"`
	}
	helpers.DecodeJSON(t, resp, &associated)
	resp.Body.Close()
	if associated.SecretCode == "" || associated.Session == "" {
		t.Fatalf("AssociateSoftwareToken(Session) returned %#v", associated)
	}

	resp = cognitoCall(t, srv, "VerifySoftwareToken", map[string]any{
		"Session":  associated.Session,
		"UserCode": testComputeTOTP(t, associated.SecretCode),
	})
	helpers.AssertStatus(t, resp, http.StatusOK)
	var verified struct {
		Status  string `json:"Status"`
		Session string `json:"Session"`
	}
	helpers.DecodeJSON(t, resp, &verified)
	resp.Body.Close()
	if verified.Status != "SUCCESS" || verified.Session == "" {
		t.Fatalf("VerifySoftwareToken(Session) returned %#v", verified)
	}

	// And: MFA_SETUP completes sign-in with the verified session
	resp = cognitoCall(t, srv, "RespondToAuthChallenge", map[string]any{
		"ClientId": clientID, "ChallengeName": "MFA_SETUP", "Session": verified.Session,
		"ChallengeResponses": map[string]string{"USERNAME": "setupuser"},
	})
	done := decodeChallenge(t, resp)
	resp.Body.Close()
	if done.AuthenticationResult.AccessToken == "" {
		t.Fatal("RespondToAuthChallenge(MFA_SETUP): empty AccessToken")
	}

	// And: the next sign-in asks for the newly activated TOTP factor
	resp = cognitoCall(t, srv, "InitiateAuth", map[string]any{
		"ClientId": clientID, "AuthFlow": "USER_PASSWORD_AUTH",
		"AuthParameters": map[string]string{"USERNAME": "setupuser", "PASSWORD": "SetupPass1!"},
	})
	next := decodeChallenge(t, resp)
	resp.Body.Close()
	if next.ChallengeName != "SOFTWARE_TOKEN_MFA" {
		t.Fatalf("expected SOFTWARE_TOKEN_MFA after MFA_SETUP, got %q", next.ChallengeName)
	}
}

func TestRespondToAuthChallenge_mfaSetupBeforeVerificationIsRejected(t *testing.T) {
	// Given: an MFA_SETUP challenge whose software token was never verified
	srv := helpers.NewTestServer(t)
	poolID := createPool(t, srv, "p")
	setPoolMfaConfig(t, srv, poolID, map[string]any{
		"MfaConfiguration":              "ON",
		"SoftwareTokenMfaConfiguration": map[string]any{"Enabled": true},
	})
	clientID := createClient(t, srv, poolID, "app")
	createConfirmedUser(t, srv, poolID, "unsetup", "SetupPass1!")
	resp := cognitoCall(t, srv, "InitiateAuth", map[string]any{
		"ClientId": clientID, "AuthFlow": "USER_PASSWORD_AUTH",
		"AuthParameters": map[string]string{"USERNAME": "unsetup", "PASSWORD": "SetupPass1!"},
	})
	challenge := decodeChallenge(t, resp)
	resp.Body.Close()

	// When: MFA_SETUP is answered without verifying a factor
	resp = cognitoCall(t, srv, "RespondToAuthChallenge", map[string]any{
		"ClientId": clientID, "ChallengeName": "MFA_SETUP", "Session": challenge.Session,
		"ChallengeResponses": map[string]string{"USERNAME": "unsetup"},
	})
	defer resp.Body.Close()

	// Then: SoftwareTokenMFANotFoundException
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "SoftwareTokenMFANotFoundException")
}

// ─── MFA preferences ──────────────────────────────────────────────────────────

func TestGetUser_reportsMfaSettings(t *testing.T) {
	// Given: a user with SMS MFA activated and preferred
	srv := helpers.NewTestServer(t)
	poolID := createPool(t, srv, "p")
	setPoolMfaConfig(t, srv, poolID, map[string]any{
		"MfaConfiguration":    "OPTIONAL",
		"SmsMfaConfiguration": map[string]any{"SmsAuthenticationMessage": "Your code is {####}"},
	})
	clientID := createClient(t, srv, poolID, "app")
	createConfirmedUserWithPhone(t, srv, poolID, "prefuser", "SmsPass1!", "+14155550133")
	resp := cognitoCall(t, srv, "AdminSetUserMFAPreference", map[string]any{
		"UserPoolId": poolID, "Username": "prefuser",
		"SMSMfaSettings": map[string]any{"Enabled": true, "PreferredMfa": true},
	})
	helpers.AssertStatus(t, resp, http.StatusOK)
	resp.Body.Close()

	// When: AdminGetUser reads the user back
	resp = cognitoCall(t, srv, "AdminGetUser", map[string]any{"UserPoolId": poolID, "Username": "prefuser"})
	helpers.AssertStatus(t, resp, http.StatusOK)
	var admin struct {
		PreferredMfaSetting string   `json:"PreferredMfaSetting"`
		UserMFASettingList  []string `json:"UserMFASettingList"`
	}
	helpers.DecodeJSON(t, resp, &admin)
	resp.Body.Close()

	// Then: both MFA fields are reported
	if admin.PreferredMfaSetting != "SMS_MFA" {
		t.Errorf("AdminGetUser PreferredMfaSetting = %q, want SMS_MFA", admin.PreferredMfaSetting)
	}
	if len(admin.UserMFASettingList) != 1 || admin.UserMFASettingList[0] != "SMS_MFA" {
		t.Errorf("AdminGetUser UserMFASettingList = %#v, want [SMS_MFA]", admin.UserMFASettingList)
	}

	// And: GetUser reports the same for the signed-in user
	challenge := signInWithSmsMfa(t, srv, poolID, clientID, "prefuser", "SmsPass1!")
	resp = cognitoCall(t, srv, "GetUser", map[string]any{"AccessToken": challenge})
	helpers.AssertStatus(t, resp, http.StatusOK)
	var self struct {
		PreferredMfaSetting string   `json:"PreferredMfaSetting"`
		UserMFASettingList  []string `json:"UserMFASettingList"`
	}
	helpers.DecodeJSON(t, resp, &self)
	resp.Body.Close()
	if self.PreferredMfaSetting != "SMS_MFA" {
		t.Errorf("GetUser PreferredMfaSetting = %q, want SMS_MFA", self.PreferredMfaSetting)
	}
	if len(self.UserMFASettingList) != 1 || self.UserMFASettingList[0] != "SMS_MFA" {
		t.Errorf("GetUser UserMFASettingList = %#v, want [SMS_MFA]", self.UserMFASettingList)
	}
}

func TestSetUserMFAPreference_smsWithoutPhoneNumberIsRejected(t *testing.T) {
	// Given: a user with no phone number
	srv := helpers.NewTestServer(t)
	poolID := createPool(t, srv, "p")
	createConfirmedUser(t, srv, poolID, "nophone", "SmsPass1!")

	// When: SMS MFA is activated for them
	resp := cognitoCall(t, srv, "AdminSetUserMFAPreference", map[string]any{
		"UserPoolId": poolID, "Username": "nophone",
		"SMSMfaSettings": map[string]any{"Enabled": true},
	})
	defer resp.Body.Close()

	// Then: InvalidParameterException
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "InvalidParameterException")
}

// signInWithSmsMfa runs a complete password + SMS_MFA sign-in and returns the
// access token.
func signInWithSmsMfa(t *testing.T, srv *helpers.TestServer, poolID, clientID, username, password string) string {
	t.Helper()
	resp := cognitoCall(t, srv, "InitiateAuth", map[string]any{
		"ClientId": clientID, "AuthFlow": "USER_PASSWORD_AUTH",
		"AuthParameters": map[string]string{"USERNAME": username, "PASSWORD": password},
	})
	challenge := decodeChallenge(t, resp)
	resp.Body.Close()
	if challenge.ChallengeName != "SMS_MFA" {
		t.Fatalf("expected SMS_MFA challenge for %s, got %q", username, challenge.ChallengeName)
	}
	code := authChallengeCode(t, srv, poolID, username, "SMS_MFA")
	resp = cognitoCall(t, srv, "RespondToAuthChallenge", map[string]any{
		"ClientId": clientID, "ChallengeName": "SMS_MFA", "Session": challenge.Session,
		"ChallengeResponses": map[string]string{"USERNAME": username, "SMS_MFA_CODE": code},
	})
	done := decodeChallenge(t, resp)
	resp.Body.Close()
	if done.AuthenticationResult.AccessToken == "" {
		t.Fatalf("SMS_MFA sign-in for %s issued no access token", username)
	}
	return done.AuthenticationResult.AccessToken
}

// enrollTOTP signs the user in, associates and verifies a software token, and
// returns an access token for them.
func enrollTOTP(t *testing.T, srv *helpers.TestServer, poolID, clientID, username, password string) string {
	t.Helper()
	resp := cognitoCall(t, srv, "InitiateAuth", map[string]any{
		"ClientId": clientID, "AuthFlow": "USER_PASSWORD_AUTH",
		"AuthParameters": map[string]string{"USERNAME": username, "PASSWORD": password},
	})
	signedIn := decodeChallenge(t, resp)
	resp.Body.Close()
	accessToken := signedIn.AuthenticationResult.AccessToken
	if accessToken == "" {
		t.Fatalf("enrollTOTP: %s was challenged with %q instead of signing in", username, signedIn.ChallengeName)
	}
	resp = cognitoCall(t, srv, "AssociateSoftwareToken", map[string]any{"AccessToken": accessToken})
	helpers.AssertStatus(t, resp, http.StatusOK)
	var associated struct {
		SecretCode string `json:"SecretCode"`
	}
	helpers.DecodeJSON(t, resp, &associated)
	resp.Body.Close()
	resp = cognitoCall(t, srv, "VerifySoftwareToken", map[string]any{
		"AccessToken": accessToken, "UserCode": testComputeTOTP(t, associated.SecretCode),
	})
	helpers.AssertStatus(t, resp, http.StatusOK)
	resp.Body.Close()
	return accessToken
}
