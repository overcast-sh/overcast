package cognito_test

import (
	"net/http"
	"testing"

	"github.com/overcast-sh/overcast/tests/helpers"
)

func createCognitoUserWithEmail(t *testing.T, srv *helpers.TestServer, poolID, username, email string) {
	t.Helper()
	resp := cognitoCall(t, srv, "AdminCreateUser", map[string]any{
		"UserPoolId":    poolID,
		"Username":      username,
		"MessageAction": "SUPPRESS",
		"UserAttributes": []map[string]string{
			{"Name": "email", "Value": email},
		},
	})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
}

func TestListUsers_filterUsernameExact(t *testing.T) {
	// Given: a pool with multiple users
	srv := helpers.NewTestServer(t)
	poolID := createPool(t, srv, "p")
	createCognitoUserWithEmail(t, srv, poolID, "alice", "alice@example.com")
	createCognitoUserWithEmail(t, srv, poolID, "bob", "bob@example.com")

	// When: ListUsers filters by exact username
	resp := cognitoCall(t, srv, "ListUsers", map[string]any{
		"UserPoolId": poolID,
		"Filter":     `username = "alice"`,
	})
	defer resp.Body.Close()

	// Then: only the matching user is returned
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		Users []struct {
			Username string `json:"Username"`
		} `json:"Users"`
	}
	helpers.DecodeJSON(t, resp, &result)
	if len(result.Users) != 1 || result.Users[0].Username != "alice" {
		t.Fatalf("expected only alice, got %#v", result.Users)
	}
}

func TestListUsers_filterEmailPrefix(t *testing.T) {
	// Given: a pool with email attributes
	srv := helpers.NewTestServer(t)
	poolID := createPool(t, srv, "p")
	createCognitoUserWithEmail(t, srv, poolID, "alice", "team-alice@example.com")
	createCognitoUserWithEmail(t, srv, poolID, "bob", "other-bob@example.com")
	createCognitoUserWithEmail(t, srv, poolID, "carol", "team-carol@example.com")

	// When: ListUsers filters by email prefix
	resp := cognitoCall(t, srv, "ListUsers", map[string]any{
		"UserPoolId": poolID,
		"Filter":     `email ^= "team-"`,
	})
	defer resp.Body.Close()

	// Then: only users with matching email prefixes are returned
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		Users []struct {
			Username string `json:"Username"`
		} `json:"Users"`
	}
	helpers.DecodeJSON(t, resp, &result)
	if len(result.Users) != 2 {
		t.Fatalf("expected 2 matching users, got %#v", result.Users)
	}
	seen := map[string]bool{}
	for _, u := range result.Users {
		seen[u.Username] = true
	}
	if !seen["alice"] || !seen["carol"] || seen["bob"] {
		t.Fatalf("unexpected filtered users: %#v", result.Users)
	}
}

func TestListUsers_pagination(t *testing.T) {
	// Given: a pool with three users
	srv := helpers.NewTestServer(t)
	poolID := createPool(t, srv, "p")
	createCognitoUserWithEmail(t, srv, poolID, "alice", "alice@example.com")
	createCognitoUserWithEmail(t, srv, poolID, "bob", "bob@example.com")
	createCognitoUserWithEmail(t, srv, poolID, "carol", "carol@example.com")

	// When: ListUsers requests the first page
	resp := cognitoCall(t, srv, "ListUsers", map[string]any{
		"UserPoolId": poolID,
		"Limit":      2,
	})
	defer resp.Body.Close()

	// Then: the first page contains two users and a token for the next page
	helpers.AssertStatus(t, resp, http.StatusOK)
	var first struct {
		PaginationToken string `json:"PaginationToken"`
		Users           []struct {
			Username string `json:"Username"`
		} `json:"Users"`
	}
	helpers.DecodeJSON(t, resp, &first)
	if len(first.Users) != 2 || first.PaginationToken == "" {
		t.Fatalf("expected first page with 2 users and token, got %#v", first)
	}

	// When: ListUsers requests the next page with the returned token
	resp = cognitoCall(t, srv, "ListUsers", map[string]any{
		"UserPoolId":      poolID,
		"Limit":           2,
		"PaginationToken": first.PaginationToken,
	})
	defer resp.Body.Close()

	// Then: the final page returns the remaining user without another token
	helpers.AssertStatus(t, resp, http.StatusOK)
	var second struct {
		PaginationToken string `json:"PaginationToken"`
		Users           []struct {
			Username string `json:"Username"`
		} `json:"Users"`
	}
	helpers.DecodeJSON(t, resp, &second)
	if len(second.Users) != 1 || second.PaginationToken != "" {
		t.Fatalf("expected final page with 1 user and no token, got %#v", second)
	}
}

func TestListUsers_attributesToGet(t *testing.T) {
	// Given: a pool with a user that has multiple attributes
	srv := helpers.NewTestServer(t)
	poolID := createPool(t, srv, "p")
	resp := cognitoCall(t, srv, "AdminCreateUser", map[string]any{
		"UserPoolId":    poolID,
		"Username":      "alice",
		"MessageAction": "SUPPRESS",
		"UserAttributes": []map[string]string{
			{"Name": "email", "Value": "alice@example.com"},
			{"Name": "given_name", "Value": "Alice"},
		},
	})
	resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)

	// When: ListUsers requests a subset of attributes
	resp = cognitoCall(t, srv, "ListUsers", map[string]any{
		"UserPoolId":      poolID,
		"AttributesToGet": []string{"email", "sub"},
	})
	defer resp.Body.Close()

	// Then: only the requested attributes are returned for each user
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		Users []struct {
			Attributes []struct {
				Name string `json:"Name"`
			} `json:"Attributes"`
		} `json:"Users"`
	}
	helpers.DecodeJSON(t, resp, &result)
	if len(result.Users) != 1 || len(result.Users[0].Attributes) != 2 {
		t.Fatalf("expected one user with 2 attributes, got %#v", result.Users)
	}
	seen := map[string]bool{}
	for _, attr := range result.Users[0].Attributes {
		seen[attr.Name] = true
	}
	if !seen["email"] || !seen["sub"] || seen["given_name"] {
		t.Fatalf("unexpected attributes: %#v", result.Users[0].Attributes)
	}
}

// ─── Filter validation ─────────────────────────────────────────────────────
//
// AWS documents the Filter grammar as `"AttributeName Filter-Type "AttributeValue"`
// with Filter-Type limited to "=" and "^=", over a fixed set of searchable
// standard attributes (username, email, phone_number, name, given_name,
// family_name, preferred_username, cognito:user_status, status, sub) — see
// https://docs.aws.amazon.com/cognito-user-identity-pools/latest/APIReference/API_ListUsers.html.
// A filter outside that grammar answers InvalidParameterException; message
// wording parity with AWS is out of scope for these tests.

func TestListUsers_filterUnsupportedAttribute(t *testing.T) {
	// Given: a pool with a user
	srv := helpers.NewTestServer(t)
	poolID := createPool(t, srv, "p")
	createCognitoUserWithEmail(t, srv, poolID, "alice", "alice@example.com")

	// When: ListUsers filters on an attribute name AWS doesn't allow searching by
	resp := cognitoCall(t, srv, "ListUsers", map[string]any{
		"UserPoolId": poolID,
		"Filter":     `custom:role = "admin"`,
	})
	defer resp.Body.Close()

	// Then: Cognito rejects the unsupported attribute
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "InvalidParameterException")
}

func TestListUsers_filterMissingOperator(t *testing.T) {
	// Given: a pool with a user
	srv := helpers.NewTestServer(t)
	poolID := createPool(t, srv, "p")
	createCognitoUserWithEmail(t, srv, poolID, "alice", "alice@example.com")

	// When: ListUsers supplies a filter with no "=" or "^=" operator at all
	resp := cognitoCall(t, srv, "ListUsers", map[string]any{
		"UserPoolId": poolID,
		"Filter":     `username "alice"`,
	})
	defer resp.Body.Close()

	// Then: Cognito rejects the malformed filter
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "InvalidParameterException")
}

func TestListUsers_filterUnterminatedQuotedValue(t *testing.T) {
	// Given: a pool with a user
	srv := helpers.NewTestServer(t)
	poolID := createPool(t, srv, "p")
	createCognitoUserWithEmail(t, srv, poolID, "alice", "alice@example.com")

	// When: ListUsers supplies a filter whose quoted value is never closed
	resp := cognitoCall(t, srv, "ListUsers", map[string]any{
		"UserPoolId": poolID,
		"Filter":     `username = "alice`,
	})
	defer resp.Body.Close()

	// Then: Cognito rejects the malformed filter
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "InvalidParameterException")
}

func TestListUsers_filterUnsupportedOperator(t *testing.T) {
	// Given: a pool with a user
	srv := helpers.NewTestServer(t)
	poolID := createPool(t, srv, "p")
	createCognitoUserWithEmail(t, srv, poolID, "alice", "alice@example.com")

	// When: ListUsers supplies a filter with an operator other than "=" or "^="
	resp := cognitoCall(t, srv, "ListUsers", map[string]any{
		"UserPoolId": poolID,
		"Filter":     `username != "alice"`,
	})
	defer resp.Body.Close()

	// Then: Cognito rejects the unsupported operator
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "InvalidParameterException")
}

func TestListUsers_attributesToGetMissing(t *testing.T) {
	// Given: one returned user is missing the requested attribute
	srv := helpers.NewTestServer(t)
	poolID := createPool(t, srv, "p")
	createCognitoUserWithEmail(t, srv, poolID, "alice", "alice@example.com")
	resp := cognitoCall(t, srv, "AdminCreateUser", map[string]any{
		"UserPoolId":    poolID,
		"Username":      "bob",
		"MessageAction": "SUPPRESS",
	})
	resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)

	// When: ListUsers requests an attribute that isn't present on every returned user
	resp = cognitoCall(t, srv, "ListUsers", map[string]any{
		"UserPoolId":      poolID,
		"AttributesToGet": []string{"email"},
	})
	defer resp.Body.Close()

	// Then: AWS rejects the request
	helpers.AssertJSONError(t, resp, "InvalidParameterException")
}
