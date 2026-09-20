// Package cognito_test: SECRET_HASH validation integration tests.
package cognito_test

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"testing"

	"github.com/overcast-sh/overcast/tests/helpers"
)

// createClientWithSecret creates a pool client with a generated secret and returns
// (clientID, clientSecret).
func createClientWithSecret(t *testing.T, srv *helpers.TestServer, poolID, name string) (string, string) {
	t.Helper()
	resp := cognitoCall(t, srv, "CreateUserPoolClient", map[string]any{
		"UserPoolId":     poolID,
		"ClientName":     name,
		"GenerateSecret": true,
	})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		UserPoolClient struct {
			ClientId     string `json:"ClientId"`
			ClientSecret string `json:"ClientSecret"`
		} `json:"UserPoolClient"`
	}
	helpers.DecodeJSON(t, resp, &result)
	if result.UserPoolClient.ClientId == "" {
		t.Fatal("CreateUserPoolClient returned empty ClientId")
	}
	if result.UserPoolClient.ClientSecret == "" {
		t.Fatal("CreateUserPoolClient returned empty ClientSecret")
	}
	return result.UserPoolClient.ClientId, result.UserPoolClient.ClientSecret
}

// secretHash computes the Cognito SECRET_HASH:
//
//	Base64( HMAC-SHA256( username + clientID , clientSecret ) )
func secretHash(username, clientID, clientSecret string) string {
	mac := hmac.New(sha256.New, []byte(clientSecret))
	mac.Write([]byte(username + clientID))
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

// ─── CreateUserPoolClient ─────────────────────────────────────────────────────

func TestCreateUserPoolClient_withSecret(t *testing.T) {
	// Given: a user pool
	srv := helpers.NewTestServer(t)
	poolID := createPool(t, srv, "p")

	// When: CreateUserPoolClient with GenerateSecret=true
	clientID, secret := createClientWithSecret(t, srv, poolID, "app")
	if len(clientID) == 0 {
		t.Error("ClientId is empty")
	}
	if len(secret) < 10 {
		t.Errorf("ClientSecret looks too short: %q", secret)
	}
}

// ─── InitiateAuth SECRET_HASH ─────────────────────────────────────────────────

func TestInitiateAuth_secretHash_valid(t *testing.T) {
	// Given: a confirmed user and a client with a secret
	srv := helpers.NewTestServer(t)
	poolID := createPool(t, srv, "p")
	clientID, clientSecret := createClientWithSecret(t, srv, poolID, "app")
	cognitoCall(t, srv, "AdminCreateUser", map[string]any{"UserPoolId": poolID, "Username": "alice"}).Body.Close()
	cognitoCall(t, srv, "AdminSetUserPassword", map[string]any{
		"UserPoolId": poolID, "Username": "alice", "Password": "AlicePass1!", "Permanent": true,
	}).Body.Close()

	// When: InitiateAuth with correct SECRET_HASH
	resp := cognitoCall(t, srv, "InitiateAuth", map[string]any{
		"ClientId": clientID, "AuthFlow": "USER_PASSWORD_AUTH",
		"AuthParameters": map[string]string{
			"USERNAME":    "alice",
			"PASSWORD":    "AlicePass1!",
			"SECRET_HASH": secretHash("alice", clientID, clientSecret),
		},
	})
	defer resp.Body.Close()

	// Then: 200 with tokens
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		AuthenticationResult struct {
			AccessToken string `json:"AccessToken"`
		} `json:"AuthenticationResult"`
	}
	helpers.DecodeJSON(t, resp, &result)
	if result.AuthenticationResult.AccessToken == "" {
		t.Error("expected AccessToken, got none")
	}
}

func TestInitiateAuth_secretHash_missing(t *testing.T) {
	// Given: a confirmed user and a client with a secret
	srv := helpers.NewTestServer(t)
	poolID := createPool(t, srv, "p")
	clientID, _ := createClientWithSecret(t, srv, poolID, "app")
	cognitoCall(t, srv, "AdminCreateUser", map[string]any{"UserPoolId": poolID, "Username": "bob"}).Body.Close()
	cognitoCall(t, srv, "AdminSetUserPassword", map[string]any{
		"UserPoolId": poolID, "Username": "bob", "Password": "BobPass1!", "Permanent": true,
	}).Body.Close()

	// When: InitiateAuth without SECRET_HASH
	resp := cognitoCall(t, srv, "InitiateAuth", map[string]any{
		"ClientId": clientID, "AuthFlow": "USER_PASSWORD_AUTH",
		"AuthParameters": map[string]string{"USERNAME": "bob", "PASSWORD": "BobPass1!"},
	})
	defer resp.Body.Close()

	// Then: InvalidParameterException
	helpers.AssertJSONError(t, resp, "InvalidParameterException")
}

func TestInitiateAuth_secretHash_wrong(t *testing.T) {
	// Given: a confirmed user and a client with a secret
	srv := helpers.NewTestServer(t)
	poolID := createPool(t, srv, "p")
	clientID, _ := createClientWithSecret(t, srv, poolID, "app")
	cognitoCall(t, srv, "AdminCreateUser", map[string]any{"UserPoolId": poolID, "Username": "carol"}).Body.Close()
	cognitoCall(t, srv, "AdminSetUserPassword", map[string]any{
		"UserPoolId": poolID, "Username": "carol", "Password": "CarolPass1!", "Permanent": true,
	}).Body.Close()

	// When: InitiateAuth with incorrect SECRET_HASH
	resp := cognitoCall(t, srv, "InitiateAuth", map[string]any{
		"ClientId": clientID, "AuthFlow": "USER_PASSWORD_AUTH",
		"AuthParameters": map[string]string{
			"USERNAME":    "carol",
			"PASSWORD":    "CarolPass1!",
			"SECRET_HASH": "nottherealhash",
		},
	})
	defer resp.Body.Close()

	// Then: NotAuthorizedException
	helpers.AssertJSONError(t, resp, "NotAuthorizedException")
}

func TestInitiateAuth_noSecret_unexpectedHash(t *testing.T) {
	// Given: a client without a secret
	srv := helpers.NewTestServer(t)
	poolID := createPool(t, srv, "p")
	clientID := createClient(t, srv, poolID, "app")
	cognitoCall(t, srv, "AdminCreateUser", map[string]any{"UserPoolId": poolID, "Username": "dave"}).Body.Close()
	cognitoCall(t, srv, "AdminSetUserPassword", map[string]any{
		"UserPoolId": poolID, "Username": "dave", "Password": "DavePass1!", "Permanent": true,
	}).Body.Close()

	// When: InitiateAuth with a SECRET_HASH on a secret-less client
	resp := cognitoCall(t, srv, "InitiateAuth", map[string]any{
		"ClientId": clientID, "AuthFlow": "USER_PASSWORD_AUTH",
		"AuthParameters": map[string]string{
			"USERNAME":    "dave",
			"PASSWORD":    "DavePass1!",
			"SECRET_HASH": "shouldnotbehere",
		},
	})
	defer resp.Body.Close()

	// Then: InvalidParameterException
	helpers.AssertJSONError(t, resp, "InvalidParameterException")
}

// ─── AdminInitiateAuth SECRET_HASH ────────────────────────────────────────────

func TestAdminInitiateAuth_secretHash_missing(t *testing.T) {
	// Given: a confirmed user and a client with a secret
	srv := helpers.NewTestServer(t)
	poolID := createPool(t, srv, "p")
	clientID, _ := createClientWithSecret(t, srv, poolID, "app")
	cognitoCall(t, srv, "AdminCreateUser", map[string]any{"UserPoolId": poolID, "Username": "adminhash"}).Body.Close()
	cognitoCall(t, srv, "AdminSetUserPassword", map[string]any{
		"UserPoolId": poolID, "Username": "adminhash", "Password": "AdminHash1!", "Permanent": true,
	}).Body.Close()

	// When: AdminInitiateAuth omits SECRET_HASH for the secret client
	resp := cognitoCall(t, srv, "AdminInitiateAuth", map[string]any{
		"UserPoolId": poolID,
		"ClientId":   clientID,
		"AuthFlow":   "ADMIN_USER_PASSWORD_AUTH",
		"AuthParameters": map[string]string{
			"USERNAME": "adminhash",
			"PASSWORD": "AdminHash1!",
		},
	})
	defer resp.Body.Close()

	// Then: InvalidParameterException
	helpers.AssertJSONError(t, resp, "InvalidParameterException")
}

// ─── Challenge SECRET_HASH ────────────────────────────────────────────────────

func TestRespondToAuthChallenge_secretHash_missing(t *testing.T) {
	// Given: a secret client and a NEW_PASSWORD_REQUIRED challenge session
	srv := helpers.NewTestServer(t)
	poolID := createPool(t, srv, "p")
	clientID, clientSecret := createClientWithSecret(t, srv, poolID, "app")
	cognitoCall(t, srv, "AdminCreateUser", map[string]any{
		"UserPoolId": poolID, "Username": "challengehash", "TemporaryPassword": "TempHash1!",
	}).Body.Close()
	initResp := cognitoCall(t, srv, "InitiateAuth", map[string]any{
		"ClientId": clientID,
		"AuthFlow": "USER_PASSWORD_AUTH",
		"AuthParameters": map[string]string{
			"USERNAME":    "challengehash",
			"PASSWORD":    "TempHash1!",
			"SECRET_HASH": secretHash("challengehash", clientID, clientSecret),
		},
	})
	defer initResp.Body.Close()
	helpers.AssertStatus(t, initResp, http.StatusOK)
	var challenge struct {
		Session string `json:"Session"`
	}
	helpers.DecodeJSON(t, initResp, &challenge)

	// When: RespondToAuthChallenge omits SECRET_HASH for the secret client
	resp := cognitoCall(t, srv, "RespondToAuthChallenge", map[string]any{
		"ClientId":      clientID,
		"ChallengeName": "NEW_PASSWORD_REQUIRED",
		"Session":       challenge.Session,
		"ChallengeResponses": map[string]string{
			"USERNAME":     "challengehash",
			"NEW_PASSWORD": "FinalHash1!",
		},
	})
	defer resp.Body.Close()

	// Then: InvalidParameterException
	helpers.AssertJSONError(t, resp, "InvalidParameterException")
}

func TestAdminRespondToAuthChallenge_secretHash_missing(t *testing.T) {
	// Given: a secret client and a NEW_PASSWORD_REQUIRED admin challenge session
	srv := helpers.NewTestServer(t)
	poolID := createPool(t, srv, "p")
	clientID, clientSecret := createClientWithSecret(t, srv, poolID, "app")
	cognitoCall(t, srv, "AdminCreateUser", map[string]any{
		"UserPoolId": poolID, "Username": "adminchallenge", "TemporaryPassword": "TempHash1!",
	}).Body.Close()
	initResp := cognitoCall(t, srv, "AdminInitiateAuth", map[string]any{
		"UserPoolId": poolID,
		"ClientId":   clientID,
		"AuthFlow":   "ADMIN_USER_PASSWORD_AUTH",
		"AuthParameters": map[string]string{
			"USERNAME":    "adminchallenge",
			"PASSWORD":    "TempHash1!",
			"SECRET_HASH": secretHash("adminchallenge", clientID, clientSecret),
		},
	})
	defer initResp.Body.Close()
	helpers.AssertStatus(t, initResp, http.StatusOK)
	var challenge struct {
		Session string `json:"Session"`
	}
	helpers.DecodeJSON(t, initResp, &challenge)

	// When: AdminRespondToAuthChallenge omits SECRET_HASH for the secret client
	resp := cognitoCall(t, srv, "AdminRespondToAuthChallenge", map[string]any{
		"UserPoolId":    poolID,
		"ClientId":      clientID,
		"ChallengeName": "NEW_PASSWORD_REQUIRED",
		"Session":       challenge.Session,
		"ChallengeResponses": map[string]string{
			"USERNAME":     "adminchallenge",
			"NEW_PASSWORD": "FinalHash1!",
		},
	})
	defer resp.Body.Close()

	// Then: InvalidParameterException
	helpers.AssertJSONError(t, resp, "InvalidParameterException")
}

// ─── SignUp SECRET_HASH ────────────────────────────────────────────────────────

func TestSignUp_secretHash_valid(t *testing.T) {
	// Given: a pool client with a secret
	srv := helpers.NewTestServer(t)
	poolID := createPool(t, srv, "p")
	clientID, clientSecret := createClientWithSecret(t, srv, poolID, "app")

	// When: SignUp with correct SECRET_HASH
	resp := cognitoCall(t, srv, "SignUp", map[string]any{
		"ClientId":   clientID,
		"Username":   "newuser",
		"Password":   "NewUser1!",
		"SecretHash": secretHash("newuser", clientID, clientSecret),
	})
	defer resp.Body.Close()

	// Then: 200
	helpers.AssertStatus(t, resp, http.StatusOK)
}

func TestSignUp_secretHash_missing(t *testing.T) {
	// Given: a pool client with a secret
	srv := helpers.NewTestServer(t)
	poolID := createPool(t, srv, "p")
	clientID, _ := createClientWithSecret(t, srv, poolID, "app")

	// When: SignUp without SECRET_HASH
	resp := cognitoCall(t, srv, "SignUp", map[string]any{
		"ClientId": clientID, "Username": "newuser2", "Password": "NewUser1!",
	})
	defer resp.Body.Close()

	// Then: InvalidParameterException
	helpers.AssertJSONError(t, resp, "InvalidParameterException")
}

// ─── RefreshToken SECRET_HASH ─────────────────────────────────────────────────

func TestRefreshToken_secretHash_valid(t *testing.T) {
	// Given: a user already signed in with a client that has a secret
	srv := helpers.NewTestServer(t)
	poolID := createPool(t, srv, "p")
	clientID, clientSecret := createClientWithSecret(t, srv, poolID, "app")
	cognitoCall(t, srv, "AdminCreateUser", map[string]any{"UserPoolId": poolID, "Username": "eve"}).Body.Close()
	cognitoCall(t, srv, "AdminSetUserPassword", map[string]any{
		"UserPoolId": poolID, "Username": "eve", "Password": "EvePass1!", "Permanent": true,
	}).Body.Close()
	authResp := cognitoCall(t, srv, "InitiateAuth", map[string]any{
		"ClientId": clientID, "AuthFlow": "USER_PASSWORD_AUTH",
		"AuthParameters": map[string]string{
			"USERNAME":    "eve",
			"PASSWORD":    "EvePass1!",
			"SECRET_HASH": secretHash("eve", clientID, clientSecret),
		},
	})
	defer authResp.Body.Close()
	var authResult struct {
		AuthenticationResult struct {
			RefreshToken string `json:"RefreshToken"`
		} `json:"AuthenticationResult"`
	}
	helpers.DecodeJSON(t, authResp, &authResult)
	refreshToken := authResult.AuthenticationResult.RefreshToken

	// When: REFRESH_TOKEN_AUTH with correct SECRET_HASH
	resp := cognitoCall(t, srv, "InitiateAuth", map[string]any{
		"ClientId": clientID, "AuthFlow": "REFRESH_TOKEN_AUTH",
		"AuthParameters": map[string]string{
			"REFRESH_TOKEN": refreshToken,
			"SECRET_HASH":   secretHash("eve", clientID, clientSecret),
		},
	})
	defer resp.Body.Close()

	// Then: 200 with new tokens
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		AuthenticationResult struct {
			AccessToken string `json:"AccessToken"`
		} `json:"AuthenticationResult"`
	}
	helpers.DecodeJSON(t, resp, &result)
	if result.AuthenticationResult.AccessToken == "" {
		t.Error("expected AccessToken in refresh response")
	}
}

func TestRefreshToken_secretHash_missing(t *testing.T) {
	// Given: user signed in with a secret client
	srv := helpers.NewTestServer(t)
	poolID := createPool(t, srv, "p")
	clientID, clientSecret := createClientWithSecret(t, srv, poolID, "app")
	cognitoCall(t, srv, "AdminCreateUser", map[string]any{"UserPoolId": poolID, "Username": "frank"}).Body.Close()
	cognitoCall(t, srv, "AdminSetUserPassword", map[string]any{
		"UserPoolId": poolID, "Username": "frank", "Password": "FrankPass1!", "Permanent": true,
	}).Body.Close()
	authResp := cognitoCall(t, srv, "InitiateAuth", map[string]any{
		"ClientId": clientID, "AuthFlow": "USER_PASSWORD_AUTH",
		"AuthParameters": map[string]string{
			"USERNAME":    "frank",
			"PASSWORD":    "FrankPass1!",
			"SECRET_HASH": secretHash("frank", clientID, clientSecret),
		},
	})
	defer authResp.Body.Close()
	var authResult struct {
		AuthenticationResult struct {
			RefreshToken string `json:"RefreshToken"`
		} `json:"AuthenticationResult"`
	}
	helpers.DecodeJSON(t, authResp, &authResult)

	// When: REFRESH_TOKEN_AUTH without SECRET_HASH
	resp := cognitoCall(t, srv, "InitiateAuth", map[string]any{
		"ClientId": clientID, "AuthFlow": "REFRESH_TOKEN_AUTH",
		"AuthParameters": map[string]string{
			"REFRESH_TOKEN": authResult.AuthenticationResult.RefreshToken,
		},
	})
	defer resp.Body.Close()

	// Then: InvalidParameterException
	helpers.AssertJSONError(t, resp, "InvalidParameterException")
}

// ─── SECRET_HASH with username aliases ────────────────────────────────────────
//
// AWS computes SECRET_HASH over the literal value the caller sends as USERNAME,
// not over the user's internally resolved username or sub — see "Computing
// secret hash values" (https://docs.aws.amazon.com/cognito/latest/developerguide/signing-up-users-in-your-app.html#cognito-user-pools-computing-secret-hash)
// and the AWS re:Post guidance that an email/phone/preferred-username alias
// passed as USERNAME is the value that must be hashed. checkSecretHashTyped
// (internal/services/cognito/typed_logic.go) hashes req.AuthParameters["USERNAME"]
// exactly as received, before any alias/UsernameAttributes resolution happens.

func TestInitiateAuth_secretHash_usernameAttributeEmailLiteral(t *testing.T) {
	// Given: a UsernameAttributes:[email] pool, a secret client, and a user
	// admin-created with the email as Username (Cognito assigns a UUID as the
	// internal username for such pools).
	srv := helpers.NewTestServer(t)
	poolID := createPoolWithUsernameAttributes(t, srv, "email-secret-pool", []string{"email"})
	clientID, clientSecret := createClientWithSecret(t, srv, poolID, "app")
	internalUsername := createUserAndReturnSub(t, srv, poolID, "carol@example.com")
	cognitoCall(t, srv, "AdminSetUserPassword", map[string]any{
		"UserPoolId": poolID, "Username": "carol@example.com", "Password": "CarolPass1!", "Permanent": true,
	}).Body.Close()

	// When: InitiateAuth supplies the email as USERNAME and hashes SECRET_HASH
	// over that same literal email value
	resp := cognitoCall(t, srv, "InitiateAuth", map[string]any{
		"ClientId": clientID, "AuthFlow": "USER_PASSWORD_AUTH",
		"AuthParameters": map[string]string{
			"USERNAME":    "carol@example.com",
			"PASSWORD":    "CarolPass1!",
			"SECRET_HASH": secretHash("carol@example.com", clientID, clientSecret),
		},
	})
	defer resp.Body.Close()

	// Then: authentication succeeds
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		AuthenticationResult struct {
			AccessToken string `json:"AccessToken"`
		} `json:"AuthenticationResult"`
	}
	helpers.DecodeJSON(t, resp, &result)
	if result.AuthenticationResult.AccessToken == "" {
		t.Error("expected AccessToken, got none")
	}
	if internalUsername == "" {
		t.Fatal("expected a generated internal username/sub")
	}
}

func TestInitiateAuth_secretHash_usernameAttributeRejectsInternalUsernameHash(t *testing.T) {
	// Given: the same UsernameAttributes:[email] pool and user as above
	srv := helpers.NewTestServer(t)
	poolID := createPoolWithUsernameAttributes(t, srv, "email-secret-pool", []string{"email"})
	clientID, clientSecret := createClientWithSecret(t, srv, poolID, "app")
	internalUsername := createUserAndReturnSub(t, srv, poolID, "dana@example.com")
	cognitoCall(t, srv, "AdminSetUserPassword", map[string]any{
		"UserPoolId": poolID, "Username": "dana@example.com", "Password": "DanaPass1!", "Permanent": true,
	}).Body.Close()

	// When: InitiateAuth supplies the email as USERNAME but hashes SECRET_HASH
	// over the internally resolved UUID username instead of the literal email
	resp := cognitoCall(t, srv, "InitiateAuth", map[string]any{
		"ClientId": clientID, "AuthFlow": "USER_PASSWORD_AUTH",
		"AuthParameters": map[string]string{
			"USERNAME":    "dana@example.com",
			"PASSWORD":    "DanaPass1!",
			"SECRET_HASH": secretHash(internalUsername, clientID, clientSecret),
		},
	})
	defer resp.Body.Close()

	// Then: the hash doesn't match what AWS expects (hashed over the literal
	// USERNAME value), so authentication is rejected
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "NotAuthorizedException")
}

func TestInitiateAuth_secretHash_aliasAttributeEmailLiteral(t *testing.T) {
	// Given: an AliasAttributes:[email] pool where the user's fixed username is
	// "erin" and a verified email alias is registered separately.
	srv := helpers.NewTestServer(t)
	poolID := createPoolWithAliasAttributes(t, srv, "alias-secret-pool", []string{"email"})
	clientID, clientSecret := createClientWithSecret(t, srv, poolID, "app")
	cognitoCall(t, srv, "AdminCreateUser", map[string]any{
		"UserPoolId":    poolID,
		"Username":      "erin",
		"MessageAction": "SUPPRESS",
		"UserAttributes": []map[string]string{
			{"Name": "email", "Value": "erin@example.com"},
			{"Name": "email_verified", "Value": "true"},
		},
	}).Body.Close()
	cognitoCall(t, srv, "AdminSetUserPassword", map[string]any{
		"UserPoolId": poolID, "Username": "erin", "Password": "ErinPass1!", "Permanent": true,
	}).Body.Close()

	// When: InitiateAuth signs in with the email alias as USERNAME and hashes
	// SECRET_HASH over that same literal alias value, not the resolved "erin"
	resp := cognitoCall(t, srv, "InitiateAuth", map[string]any{
		"ClientId": clientID, "AuthFlow": "USER_PASSWORD_AUTH",
		"AuthParameters": map[string]string{
			"USERNAME":    "erin@example.com",
			"PASSWORD":    "ErinPass1!",
			"SECRET_HASH": secretHash("erin@example.com", clientID, clientSecret),
		},
	})
	defer resp.Body.Close()

	// Then: authentication succeeds
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		AuthenticationResult struct {
			AccessToken string `json:"AccessToken"`
		} `json:"AuthenticationResult"`
	}
	helpers.DecodeJSON(t, resp, &result)
	if result.AuthenticationResult.AccessToken == "" {
		t.Error("expected AccessToken, got none")
	}
}

func TestInitiateAuth_secretHash_aliasAttributeRejectsResolvedUsernameHash(t *testing.T) {
	// Given: the same AliasAttributes:[email] pool and user as above
	srv := helpers.NewTestServer(t)
	poolID := createPoolWithAliasAttributes(t, srv, "alias-secret-pool", []string{"email"})
	clientID, clientSecret := createClientWithSecret(t, srv, poolID, "app")
	cognitoCall(t, srv, "AdminCreateUser", map[string]any{
		"UserPoolId":    poolID,
		"Username":      "frank",
		"MessageAction": "SUPPRESS",
		"UserAttributes": []map[string]string{
			{"Name": "email", "Value": "frank@example.com"},
			{"Name": "email_verified", "Value": "true"},
		},
	}).Body.Close()
	cognitoCall(t, srv, "AdminSetUserPassword", map[string]any{
		"UserPoolId": poolID, "Username": "frank", "Password": "FrankPass1!", "Permanent": true,
	}).Body.Close()

	// When: InitiateAuth signs in with the email alias as USERNAME but hashes
	// SECRET_HASH over the resolved fixed username "frank" instead
	resp := cognitoCall(t, srv, "InitiateAuth", map[string]any{
		"ClientId": clientID, "AuthFlow": "USER_PASSWORD_AUTH",
		"AuthParameters": map[string]string{
			"USERNAME":    "frank@example.com",
			"PASSWORD":    "FrankPass1!",
			"SECRET_HASH": secretHash("frank", clientID, clientSecret),
		},
	})
	defer resp.Body.Close()

	// Then: the hash doesn't match what AWS expects (hashed over the literal
	// USERNAME value), so authentication is rejected
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "NotAuthorizedException")
}

// ─── RespondToAuthChallenge SECRET_HASH with username attributes ──────────────
//
// The same rule as InitiateAuth above applies to the challenge leg: AWS's
// "Computing secret hash values" guidance is to hash the username value the
// client sends, and RespondToAuthChallenge takes that value from
// ChallengeResponses.USERNAME. checkSecretHashTyped hashes
// req.ChallengeResponses["USERNAME"] exactly as received, before any
// UsernameAttributes resolution, so a hash computed over the pool's internally
// generated UUID username must be rejected.

// createEmailPoolUserWithTempPassword admin-creates a user in a
// UsernameAttributes:[email] pool with a temporary password and returns the
// internally generated username (which equals the user's sub).
func createEmailPoolUserWithTempPassword(t *testing.T, srv *helpers.TestServer, poolID, email, tempPassword string) string {
	t.Helper()
	resp := cognitoCall(t, srv, "AdminCreateUser", map[string]any{
		"UserPoolId":        poolID,
		"Username":          email,
		"TemporaryPassword": tempPassword,
		"MessageAction":     "SUPPRESS",
		"UserAttributes":    []map[string]string{{"Name": "email_verified", "Value": "true"}},
	})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		User struct {
			Username string `json:"Username"`
		} `json:"User"`
	}
	helpers.DecodeJSON(t, resp, &result)
	if result.User.Username == "" {
		t.Fatal("AdminCreateUser returned an empty Username")
	}
	return result.User.Username
}

// newPasswordChallengeSession signs in with a temporary password and returns the
// NEW_PASSWORD_REQUIRED session.
func newPasswordChallengeSession(t *testing.T, srv *helpers.TestServer, clientID, username, tempPassword, hash string) string {
	t.Helper()
	resp := cognitoCall(t, srv, "InitiateAuth", map[string]any{
		"ClientId": clientID,
		"AuthFlow": "USER_PASSWORD_AUTH",
		"AuthParameters": map[string]string{
			"USERNAME":    username,
			"PASSWORD":    tempPassword,
			"SECRET_HASH": hash,
		},
	})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	var challenge struct {
		ChallengeName string `json:"ChallengeName"`
		Session       string `json:"Session"`
	}
	helpers.DecodeJSON(t, resp, &challenge)
	if challenge.ChallengeName != "NEW_PASSWORD_REQUIRED" || challenge.Session == "" {
		t.Fatalf("expected a NEW_PASSWORD_REQUIRED session, got %#v", challenge)
	}
	return challenge.Session
}

func TestRespondToAuthChallenge_secretHash_usernameAttributeEmailLiteral(t *testing.T) {
	// Given: a UsernameAttributes:[email] pool, a secret client, and a user in
	// FORCE_CHANGE_PASSWORD reached by signing in with the email address
	srv := helpers.NewTestServer(t)
	poolID := createPoolWithUsernameAttributes(t, srv, "email-secret-challenge-pool", []string{"email"})
	clientID, clientSecret := createClientWithSecret(t, srv, poolID, "app")
	createEmailPoolUserWithTempPassword(t, srv, poolID, "gwen@example.com", "TempHash1!")
	session := newPasswordChallengeSession(t, srv, clientID, "gwen@example.com", "TempHash1!",
		secretHash("gwen@example.com", clientID, clientSecret))

	// When: RespondToAuthChallenge hashes SECRET_HASH over the same literal
	// email it puts in ChallengeResponses.USERNAME
	resp := cognitoCall(t, srv, "RespondToAuthChallenge", map[string]any{
		"ClientId":      clientID,
		"ChallengeName": "NEW_PASSWORD_REQUIRED",
		"Session":       session,
		"ChallengeResponses": map[string]string{
			"USERNAME":     "gwen@example.com",
			"NEW_PASSWORD": "FinalHash1!",
			"SECRET_HASH":  secretHash("gwen@example.com", clientID, clientSecret),
		},
	})
	defer resp.Body.Close()

	// Then: the challenge completes and returns tokens
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		AuthenticationResult struct {
			AccessToken string `json:"AccessToken"`
		} `json:"AuthenticationResult"`
	}
	helpers.DecodeJSON(t, resp, &result)
	if result.AuthenticationResult.AccessToken == "" {
		t.Error("expected AccessToken, got none")
	}
}

func TestRespondToAuthChallenge_secretHash_usernameAttributeRejectsInternalUsernameHash(t *testing.T) {
	// Given: the same UsernameAttributes:[email] pool, secret client, and
	// FORCE_CHANGE_PASSWORD user
	srv := helpers.NewTestServer(t)
	poolID := createPoolWithUsernameAttributes(t, srv, "email-secret-challenge-pool", []string{"email"})
	clientID, clientSecret := createClientWithSecret(t, srv, poolID, "app")
	internalUsername := createEmailPoolUserWithTempPassword(t, srv, poolID, "hugo@example.com", "TempHash1!")
	session := newPasswordChallengeSession(t, srv, clientID, "hugo@example.com", "TempHash1!",
		secretHash("hugo@example.com", clientID, clientSecret))

	// When: RespondToAuthChallenge sends the email as USERNAME but hashes
	// SECRET_HASH over the internally generated UUID username instead
	resp := cognitoCall(t, srv, "RespondToAuthChallenge", map[string]any{
		"ClientId":      clientID,
		"ChallengeName": "NEW_PASSWORD_REQUIRED",
		"Session":       session,
		"ChallengeResponses": map[string]string{
			"USERNAME":     "hugo@example.com",
			"NEW_PASSWORD": "FinalHash1!",
			"SECRET_HASH":  secretHash(internalUsername, clientID, clientSecret),
		},
	})
	defer resp.Body.Close()

	// Then: the hash doesn't match what AWS expects (hashed over the literal
	// USERNAME value), so the challenge is rejected
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "NotAuthorizedException")
}
