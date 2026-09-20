// Package cognito_test: AutoVerifiedAttributes storage and the code a change
// to an auto-verified attribute sends.
package cognito_test

import (
	"net/http"
	"testing"

	"github.com/overcast-sh/overcast/tests/helpers"
)

// createPoolWithAutoVerified creates a user pool whose AutoVerifiedAttributes
// are set, and returns its ID.
func createPoolWithAutoVerified(t *testing.T, srv *helpers.TestServer, name string, attrs []string) string {
	t.Helper()
	resp := cognitoCall(t, srv, "CreateUserPool", map[string]any{
		"PoolName":               name,
		"AutoVerifiedAttributes": attrs,
	})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		UserPool struct {
			Id                     string   `json:"Id"`
			AutoVerifiedAttributes []string `json:"AutoVerifiedAttributes"`
		} `json:"UserPool"`
	}
	helpers.DecodeJSON(t, resp, &result)
	if result.UserPool.Id == "" {
		t.Fatal("CreateUserPool returned empty Id")
	}
	if len(result.UserPool.AutoVerifiedAttributes) != len(attrs) {
		t.Fatalf("CreateUserPool echoed AutoVerifiedAttributes %#v, want %#v", result.UserPool.AutoVerifiedAttributes, attrs)
	}
	return result.UserPool.Id
}

func userAttr(t *testing.T, srv *helpers.TestServer, poolID, username, name string) string {
	t.Helper()
	resp := cognitoCall(t, srv, "AdminGetUser", map[string]any{"UserPoolId": poolID, "Username": username})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		UserAttributes []struct {
			Name  string `json:"Name"`
			Value string `json:"Value"`
		} `json:"UserAttributes"`
	}
	helpers.DecodeJSON(t, resp, &result)
	for _, a := range result.UserAttributes {
		if a.Name == name {
			return a.Value
		}
	}
	return ""
}

func TestUserPool_autoVerifiedAttributesRoundTrip(t *testing.T) {
	// Given: a pool created with AutoVerifiedAttributes
	srv := helpers.NewTestServer(t)
	poolID := createPoolWithAutoVerified(t, srv, "p", []string{"email"})

	// When: the pool is described
	resp := cognitoCall(t, srv, "DescribeUserPool", map[string]any{"UserPoolId": poolID})
	helpers.AssertStatus(t, resp, http.StatusOK)
	var described struct {
		UserPool struct {
			AutoVerifiedAttributes []string `json:"AutoVerifiedAttributes"`
		} `json:"UserPool"`
	}
	helpers.DecodeJSON(t, resp, &described)
	resp.Body.Close()

	// Then: the setting is stored, not dropped
	if len(described.UserPool.AutoVerifiedAttributes) != 1 || described.UserPool.AutoVerifiedAttributes[0] != "email" {
		t.Fatalf("DescribeUserPool AutoVerifiedAttributes = %#v, want [email]", described.UserPool.AutoVerifiedAttributes)
	}

	// And: UpdateUserPool replaces it
	resp = cognitoCall(t, srv, "UpdateUserPool", map[string]any{
		"UserPoolId":             poolID,
		"AutoVerifiedAttributes": []string{"phone_number"},
	})
	helpers.AssertStatus(t, resp, http.StatusOK)
	resp.Body.Close()
	resp = cognitoCall(t, srv, "DescribeUserPool", map[string]any{"UserPoolId": poolID})
	helpers.AssertStatus(t, resp, http.StatusOK)
	helpers.DecodeJSON(t, resp, &described)
	resp.Body.Close()
	if len(described.UserPool.AutoVerifiedAttributes) != 1 || described.UserPool.AutoVerifiedAttributes[0] != "phone_number" {
		t.Fatalf("after UpdateUserPool AutoVerifiedAttributes = %#v, want [phone_number]", described.UserPool.AutoVerifiedAttributes)
	}
}

func TestCreateUserPool_rejectsInvalidAutoVerifiedAttribute(t *testing.T) {
	// Given/When: a pool asks to auto-verify something that is not an
	// email address or a phone number
	srv := helpers.NewTestServer(t)
	resp := cognitoCall(t, srv, "CreateUserPool", map[string]any{
		"PoolName":               "bad",
		"AutoVerifiedAttributes": []string{"nickname"},
	})
	defer resp.Body.Close()

	// Then: InvalidParameterException
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "InvalidParameterException")
}

func TestUpdateUserAttributes_autoVerifiedEmailSendsCode(t *testing.T) {
	// Given: a pool that auto-verifies email, with verification-before-update
	// off, and a signed-in user with a verified address
	srv := helpers.NewTestServer(t)
	poolID := createPoolWithAutoVerified(t, srv, "p", []string{"email"})
	clientID := createClient(t, srv, poolID, "app")
	cognitoCall(t, srv, "AdminCreateUser", map[string]any{
		"UserPoolId": poolID, "Username": "autoverify",
		"UserAttributes": []map[string]string{
			{"Name": "email", "Value": "old@example.com"},
			{"Name": "email_verified", "Value": "true"},
		},
	}).Body.Close()
	cognitoCall(t, srv, "AdminSetUserPassword", map[string]any{
		"UserPoolId": poolID, "Username": "autoverify",
		"Password": "AutoPass1!", "Permanent": true,
	}).Body.Close()
	resp := cognitoCall(t, srv, "InitiateAuth", map[string]any{
		"ClientId": clientID, "AuthFlow": "USER_PASSWORD_AUTH",
		"AuthParameters": map[string]string{"USERNAME": "autoverify", "PASSWORD": "AutoPass1!"},
	})
	signedIn := decodeChallenge(t, resp)
	resp.Body.Close()
	accessToken := signedIn.AuthenticationResult.AccessToken

	// When: the user changes their email address
	resp = cognitoCall(t, srv, "UpdateUserAttributes", map[string]any{
		"AccessToken": accessToken,
		"UserAttributes": []map[string]string{
			{"Name": "email", "Value": "new@example.com"},
		},
	})
	helpers.AssertStatus(t, resp, http.StatusOK)
	var updated struct {
		CodeDeliveryDetailsList []struct {
			DeliveryMedium string `json:"DeliveryMedium"`
			Destination    string `json:"Destination"`
			AttributeName  string `json:"AttributeName"`
		} `json:"CodeDeliveryDetailsList"`
	}
	helpers.DecodeJSON(t, resp, &updated)
	resp.Body.Close()

	// Then: a code was sent to the new address, masked as AWS masks it
	if len(updated.CodeDeliveryDetailsList) != 1 {
		t.Fatalf("CodeDeliveryDetailsList = %#v, want one entry", updated.CodeDeliveryDetailsList)
	}
	got := updated.CodeDeliveryDetailsList[0]
	if got.AttributeName != "email" || got.DeliveryMedium != "EMAIL" || got.Destination != "n***@example.com" {
		t.Fatalf("CodeDeliveryDetails = %#v, want email/EMAIL/n***@example.com", got)
	}

	// And: the new value is live but no longer verified
	if v := userAttr(t, srv, poolID, "autoverify", "email"); v != "new@example.com" {
		t.Errorf("email = %q, want new@example.com (an auto-verified change applies immediately)", v)
	}
	if v := userAttr(t, srv, poolID, "autoverify", "email_verified"); v != "false" {
		t.Errorf("email_verified = %q, want false", v)
	}

	// And: VerifyUserAttribute with the delivered code marks it verified again
	code := pendingAttributeCode(t, srv, poolID, "autoverify", "email")
	resp = cognitoCall(t, srv, "VerifyUserAttribute", map[string]any{
		"AccessToken": accessToken, "AttributeName": "email", "Code": code,
	})
	helpers.AssertStatus(t, resp, http.StatusOK)
	resp.Body.Close()
	if v := userAttr(t, srv, poolID, "autoverify", "email_verified"); v != "true" {
		t.Errorf("after VerifyUserAttribute email_verified = %q, want true", v)
	}
}

func TestUpdateUserAttributes_withoutAutoVerifiedAttributeSendsNoCode(t *testing.T) {
	// Given: a pool that auto-verifies nothing
	srv := helpers.NewTestServer(t)
	poolID := createPool(t, srv, "p")
	clientID := createClient(t, srv, poolID, "app")
	createConfirmedUser(t, srv, poolID, "nocode", "AutoPass1!")
	resp := cognitoCall(t, srv, "InitiateAuth", map[string]any{
		"ClientId": clientID, "AuthFlow": "USER_PASSWORD_AUTH",
		"AuthParameters": map[string]string{"USERNAME": "nocode", "PASSWORD": "AutoPass1!"},
	})
	signedIn := decodeChallenge(t, resp)
	resp.Body.Close()

	// When: the user changes their email address
	resp = cognitoCall(t, srv, "UpdateUserAttributes", map[string]any{
		"AccessToken": signedIn.AuthenticationResult.AccessToken,
		"UserAttributes": []map[string]string{
			{"Name": "email", "Value": "plain@example.com"},
		},
	})
	helpers.AssertStatus(t, resp, http.StatusOK)
	var updated struct {
		CodeDeliveryDetailsList []struct {
			AttributeName string `json:"AttributeName"`
		} `json:"CodeDeliveryDetailsList"`
	}
	helpers.DecodeJSON(t, resp, &updated)
	resp.Body.Close()

	// Then: nothing is sent and the value is applied
	if len(updated.CodeDeliveryDetailsList) != 0 {
		t.Fatalf("CodeDeliveryDetailsList = %#v, want empty", updated.CodeDeliveryDetailsList)
	}
	if v := userAttr(t, srv, poolID, "nocode", "email"); v != "plain@example.com" {
		t.Errorf("email = %q, want plain@example.com", v)
	}
}

func TestUpdateUserAttributes_verificationBeforeUpdateWinsOverAutoVerified(t *testing.T) {
	// Given: a pool that both auto-verifies email and requires verification
	// before an email change takes effect
	srv := helpers.NewTestServer(t)
	resp := cognitoCall(t, srv, "CreateUserPool", map[string]any{
		"PoolName":               "gated",
		"AutoVerifiedAttributes": []string{"email"},
		"UserAttributeUpdateSettings": map[string]any{
			"AttributesRequireVerificationBeforeUpdate": []string{"email"},
		},
	})
	helpers.AssertStatus(t, resp, http.StatusOK)
	var created struct {
		UserPool struct {
			Id string `json:"Id"`
		} `json:"UserPool"`
	}
	helpers.DecodeJSON(t, resp, &created)
	resp.Body.Close()
	poolID := created.UserPool.Id
	clientID := createClient(t, srv, poolID, "app")
	cognitoCall(t, srv, "AdminCreateUser", map[string]any{
		"UserPoolId": poolID, "Username": "gateduser",
		"UserAttributes": []map[string]string{
			{"Name": "email", "Value": "old@example.com"},
			{"Name": "email_verified", "Value": "true"},
		},
	}).Body.Close()
	cognitoCall(t, srv, "AdminSetUserPassword", map[string]any{
		"UserPoolId": poolID, "Username": "gateduser",
		"Password": "AutoPass1!", "Permanent": true,
	}).Body.Close()
	resp = cognitoCall(t, srv, "InitiateAuth", map[string]any{
		"ClientId": clientID, "AuthFlow": "USER_PASSWORD_AUTH",
		"AuthParameters": map[string]string{"USERNAME": "gateduser", "PASSWORD": "AutoPass1!"},
	})
	signedIn := decodeChallenge(t, resp)
	resp.Body.Close()

	// When: the user changes their email address
	resp = cognitoCall(t, srv, "UpdateUserAttributes", map[string]any{
		"AccessToken": signedIn.AuthenticationResult.AccessToken,
		"UserAttributes": []map[string]string{
			{"Name": "email", "Value": "new@example.com"},
		},
	})
	helpers.AssertStatus(t, resp, http.StatusOK)
	resp.Body.Close()

	// Then: the stored value is untouched until the code is verified
	if v := userAttr(t, srv, poolID, "gateduser", "email"); v != "old@example.com" {
		t.Errorf("email = %q, want old@example.com (verification before update still gates the change)", v)
	}
	if v := userAttr(t, srv, poolID, "gateduser", "email_verified"); v != "true" {
		t.Errorf("email_verified = %q, want true", v)
	}
}

func TestSignUp_autoVerifiedEmailReturnsCodeDeliveryDetails(t *testing.T) {
	// Given: a pool that auto-verifies email
	srv := helpers.NewTestServer(t)
	poolID := createPoolWithAutoVerified(t, srv, "p", []string{"email"})
	clientID := createClient(t, srv, poolID, "app")

	// When: a user signs themselves up
	resp := cognitoCall(t, srv, "SignUp", map[string]any{
		"ClientId": clientID, "Username": "signupuser", "Password": "SignPass1!",
		"UserAttributes": []map[string]string{{"Name": "email", "Value": "signup@example.com"}},
	})
	helpers.AssertStatus(t, resp, http.StatusOK)
	var signedUp struct {
		UserConfirmed       bool `json:"UserConfirmed"`
		CodeDeliveryDetails struct {
			DeliveryMedium string `json:"DeliveryMedium"`
			Destination    string `json:"Destination"`
			AttributeName  string `json:"AttributeName"`
		} `json:"CodeDeliveryDetails"`
	}
	helpers.DecodeJSON(t, resp, &signedUp)
	resp.Body.Close()

	// Then: the user is unconfirmed and told where the code went
	if signedUp.UserConfirmed {
		t.Error("SignUp UserConfirmed = true, want false")
	}
	got := signedUp.CodeDeliveryDetails
	if got.AttributeName != "email" || got.DeliveryMedium != "EMAIL" || got.Destination != "s***@example.com" {
		t.Fatalf("SignUp CodeDeliveryDetails = %#v, want email/EMAIL/s***@example.com", got)
	}
}
