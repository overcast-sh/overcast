package cognito_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/overcast-sh/overcast/tests/helpers"
)

// mfaConfigResult mirrors the AWS SetUserPoolMfaConfig / GetUserPoolMfaConfig
// response shape (cognito-identity-provider-2016-04-18 SetUserPoolMfaConfigResponse
// and GetUserPoolMfaConfigResponse).
type mfaConfigResult struct {
	MfaConfiguration string `json:"MfaConfiguration"`
	SmsMfaConfig     *struct {
		SmsAuthenticationMessage string `json:"SmsAuthenticationMessage"`
		SmsConfiguration         *struct {
			SnsCallerArn string `json:"SnsCallerArn"`
			ExternalId   string `json:"ExternalId"`
			SnsRegion    string `json:"SnsRegion"`
		} `json:"SmsConfiguration"`
	} `json:"SmsMfaConfiguration"`
	SoftwareTokenMfaConfig *struct {
		Enabled bool `json:"Enabled"`
	} `json:"SoftwareTokenMfaConfiguration"`
	EmailMfaConfig *struct {
		Message string `json:"Message"`
		Subject string `json:"Subject"`
	} `json:"EmailMfaConfiguration"`
	WebAuthnConfiguration *struct {
		RelyingPartyID   string `json:"RelyingPartyId"`
		UserVerification string `json:"UserVerification"`
	} `json:"WebAuthnConfiguration"`
}

// setMfaConfig issues SetUserPoolMfaConfig and decodes the response, requiring 200.
func setMfaConfig(t *testing.T, srv *helpers.TestServer, body map[string]any) mfaConfigResult {
	t.Helper()
	resp := cognitoCall(t, srv, "SetUserPoolMfaConfig", body)
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	var out mfaConfigResult
	helpers.DecodeJSON(t, resp, &out)
	return out
}

// getMfaConfig issues GetUserPoolMfaConfig and decodes the response, requiring 200.
func getMfaConfig(t *testing.T, srv *helpers.TestServer, poolID string) mfaConfigResult {
	t.Helper()
	resp := cognitoCall(t, srv, "GetUserPoolMfaConfig", map[string]any{"UserPoolId": poolID})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	var out mfaConfigResult
	helpers.DecodeJSON(t, resp, &out)
	return out
}

// assertJSONErrorMessage checks the JSON error code and the exact AWS message text.
func assertJSONErrorMessage(t *testing.T, resp *http.Response, code, message string) {
	t.Helper()
	var errResp struct {
		Type    string `json:"__type"`
		Message string `json:"message"`
	}
	helpers.DecodeJSON(t, resp, &errResp)
	if errResp.Type != code {
		t.Errorf("expected error code %q, got %q (message: %s)", code, errResp.Type, errResp.Message)
	}
	if errResp.Message != message {
		t.Errorf("expected message %q, got %q", message, errResp.Message)
	}
}

// fullMfaRequest is the request body from the AWS SetUserPoolMfaConfig API
// reference example, minus the UserPoolId.
func fullMfaRequest(poolID string) map[string]any {
	return map[string]any{
		"UserPoolId":       poolID,
		"MfaConfiguration": "OPTIONAL",
		"SmsMfaConfiguration": map[string]any{
			"SmsAuthenticationMessage": "Your OTP for MFA or sign-in: use {####}.",
			"SmsConfiguration": map[string]any{
				"ExternalId":   "a1b2c3d4-5678-90ab-cdef-EXAMPLE11111",
				"SnsCallerArn": "arn:aws:iam::123456789012:role/service-role/test-SMS-Role",
				"SnsRegion":    "us-west-2",
			},
		},
		"SoftwareTokenMfaConfiguration": map[string]any{"Enabled": true},
		"EmailMfaConfiguration": map[string]any{
			"Message": "Your OTP for MFA or sign-in: use {####}",
			"Subject": "OTP test",
		},
	}
}

func assertFullConfig(t *testing.T, got mfaConfigResult, where string) {
	t.Helper()
	if got.MfaConfiguration != "OPTIONAL" {
		t.Errorf("%s: MfaConfiguration = %q, want OPTIONAL", where, got.MfaConfiguration)
	}
	if got.SmsMfaConfig == nil {
		t.Fatalf("%s: SmsMfaConfiguration missing", where)
	}
	if got.SmsMfaConfig.SmsAuthenticationMessage != "Your OTP for MFA or sign-in: use {####}." {
		t.Errorf("%s: SmsAuthenticationMessage = %q", where, got.SmsMfaConfig.SmsAuthenticationMessage)
	}
	if got.SmsMfaConfig.SmsConfiguration == nil {
		t.Fatalf("%s: SmsMfaConfiguration.SmsConfiguration missing", where)
	}
	sc := got.SmsMfaConfig.SmsConfiguration
	if sc.SnsCallerArn != "arn:aws:iam::123456789012:role/service-role/test-SMS-Role" {
		t.Errorf("%s: SnsCallerArn = %q", where, sc.SnsCallerArn)
	}
	if sc.ExternalId != "a1b2c3d4-5678-90ab-cdef-EXAMPLE11111" {
		t.Errorf("%s: ExternalId = %q", where, sc.ExternalId)
	}
	if sc.SnsRegion != "us-west-2" {
		t.Errorf("%s: SnsRegion = %q", where, sc.SnsRegion)
	}
	if got.SoftwareTokenMfaConfig == nil || !got.SoftwareTokenMfaConfig.Enabled {
		t.Errorf("%s: SoftwareTokenMfaConfiguration = %#v, want Enabled true", where, got.SoftwareTokenMfaConfig)
	}
	if got.EmailMfaConfig == nil {
		t.Fatalf("%s: EmailMfaConfiguration missing", where)
	}
	if got.EmailMfaConfig.Message != "Your OTP for MFA or sign-in: use {####}" {
		t.Errorf("%s: EmailMfaConfiguration.Message = %q", where, got.EmailMfaConfig.Message)
	}
	if got.EmailMfaConfig.Subject != "OTP test" {
		t.Errorf("%s: EmailMfaConfiguration.Subject = %q", where, got.EmailMfaConfig.Subject)
	}
}

func TestSetUserPoolMfaConfig_allFactorsRoundTrip(t *testing.T) {
	// Given: a user pool
	srv := helpers.NewTestServer(t)
	poolID := createPool(t, srv, "mfa-roundtrip-pool")

	// When: every MFA factor is configured in one request
	setResp := setMfaConfig(t, srv, fullMfaRequest(poolID))

	// Then: SetUserPoolMfaConfig echoes all three factor configurations,
	// and GetUserPoolMfaConfig returns them from the store
	assertFullConfig(t, setResp, "SetUserPoolMfaConfig response")
	assertFullConfig(t, getMfaConfig(t, srv, poolID), "GetUserPoolMfaConfig response")
}

func TestGetUserPoolMfaConfig_unconfiguredPool(t *testing.T) {
	// Given: a user pool with no MFA configuration
	srv := helpers.NewTestServer(t)
	poolID := createPool(t, srv, "mfa-unconfigured-pool")

	// When: the MFA configuration is read
	got := getMfaConfig(t, srv, poolID)

	// Then: MFA reports OFF with no factor configurations
	if got.MfaConfiguration != "OFF" {
		t.Errorf("MfaConfiguration = %q, want OFF", got.MfaConfiguration)
	}
	if got.SmsMfaConfig != nil || got.SoftwareTokenMfaConfig != nil || got.EmailMfaConfig != nil {
		t.Errorf("expected no factor configurations, got %#v", got)
	}
}

func TestSetUserPoolMfaConfig_replacesOmittedFactors(t *testing.T) {
	// Given: a pool with SMS, TOTP and email MFA all configured
	srv := helpers.NewTestServer(t)
	poolID := createPool(t, srv, "mfa-replace-pool")
	setMfaConfig(t, srv, fullMfaRequest(poolID))

	// When: a later request supplies only the TOTP factor
	got := setMfaConfig(t, srv, map[string]any{
		"UserPoolId":                    poolID,
		"MfaConfiguration":              "ON",
		"SoftwareTokenMfaConfiguration": map[string]any{"Enabled": true},
	})

	// Then: the omitted factors are cleared rather than merged from the store
	if got.SmsMfaConfig != nil || got.EmailMfaConfig != nil {
		t.Errorf("expected omitted factors to be cleared, got %#v", got)
	}
	if got.SoftwareTokenMfaConfig == nil || !got.SoftwareTokenMfaConfig.Enabled {
		t.Errorf("SoftwareTokenMfaConfiguration = %#v, want Enabled true", got.SoftwareTokenMfaConfig)
	}
	stored := getMfaConfig(t, srv, poolID)
	if stored.SmsMfaConfig != nil || stored.EmailMfaConfig != nil {
		t.Errorf("GetUserPoolMfaConfig still returns cleared factors: %#v", stored)
	}
	if stored.MfaConfiguration != "ON" {
		t.Errorf("MfaConfiguration = %q, want ON", stored.MfaConfiguration)
	}
}

func TestSetUserPoolMfaConfig_omittedMfaConfigurationTurnsMfaOff(t *testing.T) {
	// Given: a pool with optional MFA and a TOTP factor
	srv := helpers.NewTestServer(t)
	poolID := createPool(t, srv, "mfa-omitted-mode-pool")
	setMfaConfig(t, srv, fullMfaRequest(poolID))

	// When: a later request omits MfaConfiguration and every factor
	got := setMfaConfig(t, srv, map[string]any{
		"UserPoolId": poolID,
		"WebAuthnConfiguration": map[string]any{
			"RelyingPartyId":   "auth.example.com",
			"UserVerification": "preferred",
		},
	})

	// Then: the whole MFA configuration is replaced, so MFA is off
	if got.MfaConfiguration != "OFF" {
		t.Errorf("MfaConfiguration = %q, want OFF", got.MfaConfiguration)
	}
	if got.SmsMfaConfig != nil || got.SoftwareTokenMfaConfig != nil || got.EmailMfaConfig != nil {
		t.Errorf("expected no factor configurations, got %#v", got)
	}
	// And: WebAuthn configuration is kept, as it is not an MFA factor here
	if got.WebAuthnConfiguration == nil || got.WebAuthnConfiguration.RelyingPartyID != "auth.example.com" {
		t.Errorf("WebAuthnConfiguration = %#v", got.WebAuthnConfiguration)
	}
}

func TestSetUserPoolMfaConfig_requiredWithNoFactorEnabled(t *testing.T) {
	// Given: a user pool
	srv := helpers.NewTestServer(t)
	poolID := createPool(t, srv, "mfa-no-factor-pool")

	// When: MFA is required with no factor enabled
	resp := cognitoCall(t, srv, "SetUserPoolMfaConfig", map[string]any{
		"UserPoolId":       poolID,
		"MfaConfiguration": "ON",
	})
	defer resp.Body.Close()

	// Then: Cognito rejects the combination
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	assertJSONErrorMessage(t, resp, "InvalidParameterException",
		"Invalid MFA configuration given, can't disable all MFAs with a required or optional configuration.")
}

func TestSetUserPoolMfaConfig_optionalWithDisabledSoftwareToken(t *testing.T) {
	// Given: a user pool
	srv := helpers.NewTestServer(t)
	poolID := createPool(t, srv, "mfa-disabled-totp-pool")

	// When: MFA is optional and the only factor supplied is explicitly disabled
	resp := cognitoCall(t, srv, "SetUserPoolMfaConfig", map[string]any{
		"UserPoolId":                    poolID,
		"MfaConfiguration":              "OPTIONAL",
		"SoftwareTokenMfaConfiguration": map[string]any{"Enabled": false},
	})
	defer resp.Body.Close()

	// Then: Cognito rejects it — a disabled factor does not count as enabled
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	assertJSONErrorMessage(t, resp, "InvalidParameterException",
		"Invalid MFA configuration given, can't disable all MFAs with a required or optional configuration.")
}

func TestSetUserPoolMfaConfig_offWithFactorEnabled(t *testing.T) {
	// Given: a user pool
	srv := helpers.NewTestServer(t)
	poolID := createPool(t, srv, "mfa-off-with-factor-pool")

	// When: MFA is turned off while a factor is configured
	resp := cognitoCall(t, srv, "SetUserPoolMfaConfig", map[string]any{
		"UserPoolId":                    poolID,
		"MfaConfiguration":              "OFF",
		"SoftwareTokenMfaConfiguration": map[string]any{"Enabled": true},
	})
	defer resp.Body.Close()

	// Then: Cognito rejects the contradictory request
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	assertJSONErrorMessage(t, resp, "InvalidParameterException",
		"Invalid MFA configuration given, can't turn off MFA and configure an MFA together.")
}

func TestSetUserPoolMfaConfig_offClearsFactors(t *testing.T) {
	// Given: a pool with every factor configured
	srv := helpers.NewTestServer(t)
	poolID := createPool(t, srv, "mfa-off-clears-pool")
	setMfaConfig(t, srv, fullMfaRequest(poolID))

	// When: MFA is turned off with no factor supplied
	got := setMfaConfig(t, srv, map[string]any{
		"UserPoolId":       poolID,
		"MfaConfiguration": "OFF",
	})

	// Then: the stored factor configuration goes with it
	if got.MfaConfiguration != "OFF" {
		t.Errorf("MfaConfiguration = %q, want OFF", got.MfaConfiguration)
	}
	if got.SmsMfaConfig != nil || got.SoftwareTokenMfaConfig != nil || got.EmailMfaConfig != nil {
		t.Errorf("expected no factor configurations, got %#v", got)
	}
}

func TestSetUserPoolMfaConfig_invalidMfaConfiguration(t *testing.T) {
	// Given: a user pool
	srv := helpers.NewTestServer(t)
	poolID := createPool(t, srv, "mfa-bad-enum-pool")

	// When: MfaConfiguration is outside the modeled enum
	resp := cognitoCall(t, srv, "SetUserPoolMfaConfig", map[string]any{
		"UserPoolId":       poolID,
		"MfaConfiguration": "REQUIRED",
	})
	defer resp.Body.Close()

	// Then: Cognito rejects it with the standard constraint message
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	assertJSONErrorMessage(t, resp, "InvalidParameterException",
		"1 validation error detected: Value 'REQUIRED' at 'mfaConfiguration' failed to satisfy constraint: "+
			"Member must satisfy enum value set: [OFF, ON, OPTIONAL]")
}

func TestSetUserPoolMfaConfig_emailMessageWithoutCodePlaceholder(t *testing.T) {
	// Given: a user pool
	srv := helpers.NewTestServer(t)
	poolID := createPool(t, srv, "mfa-email-no-code-pool")

	// When: the email MFA message omits the {####} code placeholder
	resp := cognitoCall(t, srv, "SetUserPoolMfaConfig", map[string]any{
		"UserPoolId":            poolID,
		"MfaConfiguration":      "OPTIONAL",
		"EmailMfaConfiguration": map[string]any{"Message": "Please sign in", "Subject": "OTP"},
	})
	defer resp.Body.Close()

	// Then: Cognito rejects the template
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	var errResp struct {
		Type    string `json:"__type"`
		Message string `json:"message"`
	}
	helpers.DecodeJSON(t, resp, &errResp)
	if errResp.Type != "InvalidParameterException" {
		t.Fatalf("expected InvalidParameterException, got %q (%s)", errResp.Type, errResp.Message)
	}
	if !strings.Contains(errResp.Message, "at 'emailMfaConfiguration.message'") {
		t.Errorf("message does not name the offending member: %q", errResp.Message)
	}
	if !strings.Contains(errResp.Message, `\{####\}`) {
		t.Errorf("message does not name the required placeholder: %q", errResp.Message)
	}
}

func TestSetUserPoolMfaConfig_smsMessageWithoutCodePlaceholder(t *testing.T) {
	// Given: a user pool
	srv := helpers.NewTestServer(t)
	poolID := createPool(t, srv, "mfa-sms-no-code-pool")

	// When: the SMS MFA message omits the {####} code placeholder
	resp := cognitoCall(t, srv, "SetUserPoolMfaConfig", map[string]any{
		"UserPoolId":       poolID,
		"MfaConfiguration": "OPTIONAL",
		"SmsMfaConfiguration": map[string]any{
			"SmsAuthenticationMessage": "Please sign in",
			"SmsConfiguration": map[string]any{
				"SnsCallerArn": "arn:aws:iam::123456789012:role/service-role/test-SMS-Role",
			},
		},
	})
	defer resp.Body.Close()

	// Then: Cognito rejects the template
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	var errResp struct {
		Type    string `json:"__type"`
		Message string `json:"message"`
	}
	helpers.DecodeJSON(t, resp, &errResp)
	if errResp.Type != "InvalidParameterException" {
		t.Fatalf("expected InvalidParameterException, got %q (%s)", errResp.Type, errResp.Message)
	}
	if !strings.Contains(errResp.Message, "at 'smsMfaConfiguration.smsAuthenticationMessage'") {
		t.Errorf("message does not name the offending member: %q", errResp.Message)
	}
}

func TestSetUserPoolMfaConfig_emailMfaRequiresEssentialsTier(t *testing.T) {
	// Given: a LITE-tier user pool
	srv := helpers.NewTestServer(t)
	resp := cognitoCall(t, srv, "CreateUserPool", map[string]any{
		"PoolName":     "mfa-lite-tier-pool",
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

	// When: email MFA is configured on it
	resp = cognitoCall(t, srv, "SetUserPoolMfaConfig", map[string]any{
		"UserPoolId":       poolResult.UserPool.Id,
		"MfaConfiguration": "OPTIONAL",
		"EmailMfaConfiguration": map[string]any{
			"Message": "Your code is {####}",
			"Subject": "OTP",
		},
	})
	defer resp.Body.Close()

	// Then: Cognito reports the feature as unavailable in the tier
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "FeatureUnavailableInTierException")
}

func TestSetUserPoolMfaConfig_overRPCv2CBOR(t *testing.T) {
	// Given: a user pool
	srv := helpers.NewTestServer(t)
	poolID := createPool(t, srv, "mfa-cbor-pool")

	// When: every MFA factor is configured over the Smithy RPC v2 CBOR binding
	resp := cognitoCBORCall(t, srv, "SetUserPoolMfaConfig", fullMfaRequest(poolID))
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)

	// Then: the nested factor configurations survive the CBOR round trip
	var got struct {
		MfaConfiguration string `cbor:"MfaConfiguration"`
		SmsMfaConfig     *struct {
			SmsAuthenticationMessage string `cbor:"SmsAuthenticationMessage"`
			SmsConfiguration         *struct {
				SnsCallerArn string `cbor:"SnsCallerArn"`
			} `cbor:"SmsConfiguration"`
		} `cbor:"SmsMfaConfiguration"`
		SoftwareTokenMfaConfig *struct {
			Enabled bool `cbor:"Enabled"`
		} `cbor:"SoftwareTokenMfaConfiguration"`
		EmailMfaConfig *struct {
			Message string `cbor:"Message"`
			Subject string `cbor:"Subject"`
		} `cbor:"EmailMfaConfiguration"`
	}
	decodeCBOR(t, resp, &got)
	if got.MfaConfiguration != "OPTIONAL" {
		t.Errorf("MfaConfiguration = %q, want OPTIONAL", got.MfaConfiguration)
	}
	if got.SmsMfaConfig == nil || got.SmsMfaConfig.SmsConfiguration == nil ||
		got.SmsMfaConfig.SmsConfiguration.SnsCallerArn != "arn:aws:iam::123456789012:role/service-role/test-SMS-Role" {
		t.Errorf("SmsMfaConfiguration = %#v", got.SmsMfaConfig)
	}
	if got.SoftwareTokenMfaConfig == nil || !got.SoftwareTokenMfaConfig.Enabled {
		t.Errorf("SoftwareTokenMfaConfiguration = %#v", got.SoftwareTokenMfaConfig)
	}
	if got.EmailMfaConfig == nil || got.EmailMfaConfig.Subject != "OTP test" {
		t.Errorf("EmailMfaConfiguration = %#v", got.EmailMfaConfig)
	}

	// And: the JSON binding reads back the same stored configuration
	assertFullConfig(t, getMfaConfig(t, srv, poolID), "GetUserPoolMfaConfig after CBOR set")
}

func TestDescribeUserPool_afterSetUserPoolMfaConfig(t *testing.T) {
	// Given: a pool with every MFA factor configured
	srv := helpers.NewTestServer(t)
	poolID := createPool(t, srv, "mfa-describe-pool")
	setMfaConfig(t, srv, fullMfaRequest(poolID))

	// When: the pool is described
	resp := cognitoCall(t, srv, "DescribeUserPool", map[string]any{"UserPoolId": poolID})
	defer resp.Body.Close()

	// Then: DescribeUserPool still answers with the pool it created
	helpers.AssertStatus(t, resp, http.StatusOK)
	var described struct {
		UserPool struct {
			Id   string `json:"Id"`
			Name string `json:"Name"`
		} `json:"UserPool"`
	}
	helpers.DecodeJSON(t, resp, &described)
	if described.UserPool.Id != poolID || described.UserPool.Name != "mfa-describe-pool" {
		t.Fatalf("unexpected DescribeUserPool result: %#v", described.UserPool)
	}
}
