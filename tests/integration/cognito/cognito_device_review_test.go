// Package cognito_test: device-management error and opt-in-confirmation
// coverage that the happy-path device tests in cognito_user_auth_test.go and
// the admin Username sweep in cognito_admin_username_sweep_test.go leave open.
//
// The AWS source for each expectation is the operation's modeled error set in
// the pinned cognito-identity-provider Smithy model (models/aws/VERSION):
// ForgetDevice/AdminForgetDevice/UpdateDeviceStatus/AdminUpdateDeviceStatus all
// list ResourceNotFoundException, ConfirmDevice lists DeviceKeyExistsException,
// and DeviceRememberedStatusType is a closed enum of remembered/not_remembered
// so any other value is an InvalidParameterException. Overcast answers HTTP 400
// for these client errors, as it does for every other modeled Cognito client
// error (see errDeviceNotFound/errUserNotFound in internal/services/cognito).
package cognito_test

import (
	"net/http"
	"testing"

	"github.com/overcast-sh/overcast/tests/helpers"
)

// unregisteredDeviceKey is a syntactically valid DeviceKeyType value
// (^[\w-]+_[0-9a-f-]+$) that no test user ever confirms.
const unregisteredDeviceKey = "us-east-1_22222222-2222-2222-2222-222222222222"

// newDeviceTrackingPool creates an always-remember device-tracking pool with a
// password client, and returns (poolID, clientID).
func newDeviceTrackingPool(t *testing.T, srv *helpers.TestServer, name string) (string, string) {
	t.Helper()
	poolID := createPoolWithDeviceConfiguration(t, srv, name, map[string]any{
		"ChallengeRequiredOnNewDevice":     true,
		"DeviceOnlyRememberedOnUserPrompt": false,
	})
	clientID := createClientWithExplicitAuthFlows(t, srv, poolID, "app", []string{"ALLOW_USER_PASSWORD_AUTH", "ALLOW_REFRESH_TOKEN_AUTH"})
	return poolID, clientID
}

// ─── ForgetDevice / AdminForgetDevice not-found ────────────────────────────

func TestForgetDevice_unknownDeviceKey(t *testing.T) {
	// Given: a signed-in user who has confirmed no device
	srv := helpers.NewTestServer(t)
	poolID, clientID := newDeviceTrackingPool(t, srv, "p")
	createConfirmedUser(t, srv, poolID, "forget-device-user", "ChoicePass1!")
	accessToken := signInForAccessToken(t, srv, clientID, "forget-device-user", "ChoicePass1!")

	// When: ForgetDevice targets a device key the user never registered
	resp := cognitoCall(t, srv, "ForgetDevice", map[string]any{
		"AccessToken": accessToken,
		"DeviceKey":   unregisteredDeviceKey,
	})
	defer resp.Body.Close()

	// Then: Cognito reports the device as not found rather than succeeding
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "ResourceNotFoundException")
}

func TestAdminForgetDevice_unknownDeviceKey(t *testing.T) {
	// Given: a real user in the pool who has confirmed no device
	srv := helpers.NewTestServer(t)
	poolID, _ := newDeviceTrackingPool(t, srv, "p")
	createConfirmedUser(t, srv, poolID, "admin-forget-user", "ChoicePass1!")

	// When: AdminForgetDevice targets a device key that user never registered
	resp := cognitoCall(t, srv, "AdminForgetDevice", map[string]any{
		"UserPoolId": poolID,
		"Username":   "admin-forget-user",
		"DeviceKey":  unregisteredDeviceKey,
	})
	defer resp.Body.Close()

	// Then: Cognito reports the device as not found rather than succeeding
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "ResourceNotFoundException")
}

// ─── ConfirmDevice ─────────────────────────────────────────────────────────

func TestConfirmDevice_duplicateDeviceKey(t *testing.T) {
	// Given: a user who has already confirmed a device
	srv := helpers.NewTestServer(t)
	poolID, clientID := newDeviceTrackingPool(t, srv, "p")
	createConfirmedUser(t, srv, poolID, "confirm-twice-user", "ChoicePass1!")
	accessToken, deviceKey := signInAndConfirmDevice(t, srv, clientID, "confirm-twice-user", "ChoicePass1!", "primary laptop")

	// When: ConfirmDevice is called again with the same DeviceKey
	resp := cognitoCall(t, srv, "ConfirmDevice", map[string]any{
		"AccessToken": accessToken,
		"DeviceKey":   deviceKey,
		"DeviceName":  "primary laptop",
		"DeviceSecretVerifierConfig": map[string]string{
			"PasswordVerifier": "verifier",
			"Salt":             "salt",
		},
	})

	// Then: Cognito rejects the duplicate confirmation
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "DeviceKeyExistsException")
	resp.Body.Close()

	// And: the original device is still the only one registered
	resp = cognitoCall(t, srv, "ListDevices", map[string]any{"AccessToken": accessToken})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	var listResult struct {
		Devices []struct {
			DeviceKey string `json:"DeviceKey"`
		} `json:"Devices"`
	}
	helpers.DecodeJSON(t, resp, &listResult)
	if len(listResult.Devices) != 1 || listResult.Devices[0].DeviceKey != deviceKey {
		t.Fatalf("expected the single original device %q, got %#v", deviceKey, listResult.Devices)
	}
}

func TestConfirmDevice_optInPoolRequiresUserConfirmation(t *testing.T) {
	// Given: a pool that only remembers devices on user prompt
	srv := helpers.NewTestServer(t)
	poolID := createPoolWithDeviceConfiguration(t, srv, "p", map[string]any{
		"ChallengeRequiredOnNewDevice":     true,
		"DeviceOnlyRememberedOnUserPrompt": true,
	})
	clientID := createClientWithExplicitAuthFlows(t, srv, poolID, "app", []string{"ALLOW_USER_PASSWORD_AUTH", "ALLOW_REFRESH_TOKEN_AUTH"})
	createConfirmedUser(t, srv, poolID, "opt-in-device-user", "ChoicePass1!")

	// And: a sign-in that returns a new device key
	resp := cognitoCall(t, srv, "InitiateAuth", map[string]any{
		"AuthFlow": "USER_PASSWORD_AUTH",
		"ClientId": clientID,
		"AuthParameters": map[string]string{
			"USERNAME": "opt-in-device-user",
			"PASSWORD": "ChoicePass1!",
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
	accessToken := signIn.AuthenticationResult.AccessToken
	deviceKey := signIn.AuthenticationResult.NewDeviceMetadata.DeviceKey
	if accessToken == "" || deviceKey == "" {
		t.Fatalf("expected access token and NewDeviceMetadata, got %#v", signIn.AuthenticationResult)
	}

	// When: the app confirms that device
	resp = cognitoCall(t, srv, "ConfirmDevice", map[string]any{
		"AccessToken": accessToken,
		"DeviceKey":   deviceKey,
		"DeviceName":  "opt-in laptop",
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

	// Then: Cognito asks the app to prompt the user
	if !confirm.UserConfirmationNecessary {
		t.Fatal("expected UserConfirmationNecessary=true for DeviceOnlyRememberedOnUserPrompt=true")
	}

	// And: the device is listed but not yet remembered
	resp = cognitoCall(t, srv, "ListDevices", map[string]any{"AccessToken": accessToken})
	helpers.AssertStatus(t, resp, http.StatusOK)
	var listResult struct {
		Devices []struct {
			DeviceKey        string `json:"DeviceKey"`
			DeviceAttributes []struct {
				Name  string `json:"Name"`
				Value string `json:"Value"`
			} `json:"DeviceAttributes"`
		} `json:"Devices"`
	}
	helpers.DecodeJSON(t, resp, &listResult)
	resp.Body.Close()
	if len(listResult.Devices) != 1 || listResult.Devices[0].DeviceKey != deviceKey {
		t.Fatalf("expected the confirmed device %q, got %#v", deviceKey, listResult.Devices)
	}
	if !hasDeviceAttribute(listResult.Devices[0].DeviceAttributes, "dev:device_remembered_status", "not_remembered") {
		t.Fatalf("expected not_remembered status before the user opts in, got %#v", listResult.Devices[0].DeviceAttributes)
	}

	// And: signing in with that DEVICE_KEY does not start device auth, because
	// the device is not remembered yet
	resp = cognitoCall(t, srv, "InitiateAuth", map[string]any{
		"AuthFlow": "USER_PASSWORD_AUTH",
		"ClientId": clientID,
		"AuthParameters": map[string]string{
			"USERNAME":   "opt-in-device-user",
			"PASSWORD":   "ChoicePass1!",
			"DEVICE_KEY": deviceKey,
		},
	})
	helpers.AssertStatus(t, resp, http.StatusOK)
	var deviceSignIn struct {
		ChallengeName        string `json:"ChallengeName"`
		AuthenticationResult struct {
			AccessToken string `json:"AccessToken"`
		} `json:"AuthenticationResult"`
	}
	helpers.DecodeJSON(t, resp, &deviceSignIn)
	resp.Body.Close()
	if deviceSignIn.ChallengeName != "" || deviceSignIn.AuthenticationResult.AccessToken == "" {
		t.Fatalf("expected direct tokens for a not-yet-remembered device, got %#v", deviceSignIn)
	}

	// And: once the user opts in, the device becomes remembered
	resp = cognitoCall(t, srv, "UpdateDeviceStatus", map[string]any{
		"AccessToken":            accessToken,
		"DeviceKey":              deviceKey,
		"DeviceRememberedStatus": "remembered",
	})
	resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)

	resp = cognitoCall(t, srv, "InitiateAuth", map[string]any{
		"AuthFlow": "USER_PASSWORD_AUTH",
		"ClientId": clientID,
		"AuthParameters": map[string]string{
			"USERNAME":   "opt-in-device-user",
			"PASSWORD":   "ChoicePass1!",
			"DEVICE_KEY": deviceKey,
		},
	})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	var rememberedSignIn struct {
		ChallengeName string `json:"ChallengeName"`
	}
	helpers.DecodeJSON(t, resp, &rememberedSignIn)
	if rememberedSignIn.ChallengeName != "DEVICE_SRP_AUTH" {
		t.Fatalf("expected DEVICE_SRP_AUTH after the user opts in, got %#v", rememberedSignIn)
	}
}

// ─── UpdateDeviceStatus / AdminUpdateDeviceStatus validation ───────────────

func TestUpdateDeviceStatus_invalidRememberedStatus(t *testing.T) {
	// Given: a signed-in user with a confirmed device
	srv := helpers.NewTestServer(t)
	poolID, clientID := newDeviceTrackingPool(t, srv, "p")
	createConfirmedUser(t, srv, poolID, "status-device-user", "ChoicePass1!")
	accessToken, deviceKey := signInAndConfirmDevice(t, srv, clientID, "status-device-user", "ChoicePass1!", "laptop")

	// When: UpdateDeviceStatus supplies a value outside DeviceRememberedStatusType
	resp := cognitoCall(t, srv, "UpdateDeviceStatus", map[string]any{
		"AccessToken":            accessToken,
		"DeviceKey":              deviceKey,
		"DeviceRememberedStatus": "bogus",
	})

	// Then: Cognito rejects the value
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "InvalidParameterException")
	resp.Body.Close()

	// And: the stored status is unchanged
	resp = cognitoCall(t, srv, "GetDevice", map[string]any{
		"AccessToken": accessToken,
		"DeviceKey":   deviceKey,
	})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	var getResult struct {
		Device struct {
			DeviceAttributes []struct {
				Name  string `json:"Name"`
				Value string `json:"Value"`
			} `json:"DeviceAttributes"`
		} `json:"Device"`
	}
	helpers.DecodeJSON(t, resp, &getResult)
	if !hasDeviceAttribute(getResult.Device.DeviceAttributes, "dev:device_remembered_status", "remembered") {
		t.Fatalf("expected the device to stay remembered, got %#v", getResult.Device.DeviceAttributes)
	}
}

func TestAdminUpdateDeviceStatus_invalidRememberedStatus(t *testing.T) {
	// Given: a user with a confirmed device
	srv := helpers.NewTestServer(t)
	poolID, clientID := newDeviceTrackingPool(t, srv, "p")
	createConfirmedUser(t, srv, poolID, "admin-status-user", "ChoicePass1!")
	_, deviceKey := signInAndConfirmDevice(t, srv, clientID, "admin-status-user", "ChoicePass1!", "laptop")

	// When: AdminUpdateDeviceStatus supplies a value outside DeviceRememberedStatusType
	resp := cognitoCall(t, srv, "AdminUpdateDeviceStatus", map[string]any{
		"UserPoolId":             poolID,
		"Username":               "admin-status-user",
		"DeviceKey":              deviceKey,
		"DeviceRememberedStatus": "bogus",
	})
	defer resp.Body.Close()

	// Then: Cognito rejects the value
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "InvalidParameterException")
}

func TestAdminUpdateDeviceStatus_unknownDeviceKey(t *testing.T) {
	// Given: a real user in the pool who has confirmed no device
	srv := helpers.NewTestServer(t)
	poolID, _ := newDeviceTrackingPool(t, srv, "p")
	createConfirmedUser(t, srv, poolID, "admin-unknown-device-user", "ChoicePass1!")

	// When: AdminUpdateDeviceStatus targets a device key that user never registered
	resp := cognitoCall(t, srv, "AdminUpdateDeviceStatus", map[string]any{
		"UserPoolId":             poolID,
		"Username":               "admin-unknown-device-user",
		"DeviceKey":              unregisteredDeviceKey,
		"DeviceRememberedStatus": "remembered",
	})
	defer resp.Body.Close()

	// Then: Cognito reports the device as not found, not the user
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "ResourceNotFoundException")
}
