package middleware

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/overcast-sh/overcast/internal/state"
)

// These tests pin #2273: under enforcement, AssumeRoleWithWebIdentity is
// authorised by the assumed role's trust policy, not by a caller's identity
// policies, and an SDK's unsigned call is therefore not refused as unsigned.

const (
	webIdentityRoleARN = "arn:aws:iam::000000000000:role/ci"
	webIdentityIssuer  = "https://oidc.example.com"
	oidcProviderARN    = "arn:aws:iam::000000000000:oidc-provider/oidc.example.com"
)

// trustFor is a trust policy admitting principal's web identities that meet
// condition, a Condition block's JSON.
func trustFor(principal, condition string) string {
	return `{"Version":"2012-10-17","Statement":[{"Effect":"Allow",
		"Principal":{"Federated":"` + principal + `"},"Action":"sts:AssumeRoleWithWebIdentity",
		"Condition":` + condition + `}]}`
}

// ciTrust trusts oidc.example.com tokens for the ci subject and the STS audience.
var ciTrust = trustFor(oidcProviderARN, `{"StringEquals":{"oidc.example.com:sub":"ci","oidc.example.com:aud":"sts.amazonaws.com"}}`)

// unsignedJWT is an ID token carrying claims, with a signature nothing checks.
func unsignedJWT(t *testing.T, claims map[string]any) string {
	t.Helper()
	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	enc := base64.RawURLEncoding
	return enc.EncodeToString([]byte(`{"alg":"RS256"}`)) + "." + enc.EncodeToString(payload) + ".sig"
}

// ciToken is an oidc.example.com ID token for subject sub.
func ciToken(t *testing.T, sub string) string {
	t.Helper()
	return unsignedJWT(t, map[string]any{"iss": webIdentityIssuer, "sub": sub, "aud": "sts.amazonaws.com"})
}

// storeWithRole is a store holding the role roleARN with trust as its trust
// policy.
func storeWithRole(t *testing.T, roleARN, trust string) state.Store {
	t.Helper()
	st := state.NewMemoryStore()
	name := roleARN[strings.LastIndex(roleARN, "/")+1:]
	b, _ := json.Marshal(map[string]string{"RoleName": name, "Arn": roleARN, "AssumeRolePolicyDocument": trust})
	if err := st.Set(context.Background(), iamRolesNamespace, name, string(b)); err != nil {
		t.Fatal(err)
	}
	return st
}

// webIdentityForm is the form of an AssumeRoleWithWebIdentity for the ci role
// presenting token, with overrides applied; an empty override removes the
// parameter.
func webIdentityForm(token string, overrides map[string]string) url.Values {
	form := url.Values{
		"Action":           {"AssumeRoleWithWebIdentity"},
		"Version":          {"2011-06-15"},
		"RoleArn":          {webIdentityRoleARN},
		"RoleSessionName":  {"build"},
		"WebIdentityToken": {token},
	}
	for k, v := range overrides {
		if v == "" {
			form.Del(k)
			continue
		}
		form.Set(k, v)
	}
	return form
}

// parsedQueryRequest is an unsigned, parsed Query POST of form.
func parsedQueryRequest(t *testing.T, form url.Values) *http.Request {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if err := r.ParseForm(); err != nil {
		t.Fatal(err)
	}
	return r
}

// servedAsWebIdentity is the router serving a request as STS
// AssumeRoleWithWebIdentity.
var servedAsWebIdentity = stubQueryRouter{route: QueryRoute{Service: "sts", Action: "AssumeRoleWithWebIdentity"}, isQuery: true}

func TestIAMEnforce_webIdentity_unsignedCallTheTrustPolicyAdmits(t *testing.T) {
	cases := []struct {
		name    string
		roleARN string
		trust   string
		claims  map[string]any
		form    map[string]string
	}{
		{"oidc-provider ARN principal", webIdentityRoleARN, ciTrust,
			map[string]any{"iss": webIdentityIssuer, "sub": "ci", "aud": "sts.amazonaws.com"}, nil},
		{"bare issuer principal, issuer with a trailing slash", webIdentityRoleARN,
			trustFor("accounts.google.com", `{"StringEquals":{"accounts.google.com:sub":"ci"}}`),
			map[string]any{"iss": "https://accounts.google.com/", "sub": "ci"}, nil},
		{"aud from azp", webIdentityRoleARN, ciTrust,
			map[string]any{"iss": webIdentityIssuer, "sub": "ci", "aud": "other", "azp": "sts.amazonaws.com"}, nil},
		{"aud given as a list", webIdentityRoleARN, ciTrust,
			map[string]any{"iss": webIdentityIssuer, "sub": "ci", "aud": []string{"sts.amazonaws.com", "other"}}, nil},
		{"role with a path", "arn:aws:iam::000000000000:role/svc/ci", ciTrust,
			map[string]any{"iss": webIdentityIssuer, "sub": "ci", "aud": "sts.amazonaws.com"},
			map[string]string{"RoleArn": "arn:aws:iam::000000000000:role/svc/ci"}},
		{"session name condition", webIdentityRoleARN,
			trustFor(oidcProviderARN, `{"StringEquals":{"sts:RoleSessionName":"build"}}`),
			map[string]any{"iss": webIdentityIssuer, "sub": "ci"}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Given: a role whose trust policy admits the token
			st := storeWithRole(t, tc.roleARN, tc.trust)
			r := parsedQueryRequest(t, webIdentityForm(unsignedJWT(t, tc.claims), tc.form))

			// When: an unsigned call presents it
			rec, served := enforceSigned(t, st, servedAsWebIdentity, r)

			// Then: it is served
			if !served {
				t.Fatalf("status %d, body %q; want the web identity served", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestIAMEnforce_webIdentity_callTheTrustPolicyRefuses(t *testing.T) {
	unimplemented := trustFor("oidc.example.com", `{"ForAnyValue:StringLike":{"oidc.example.com:amr":"authenticated"}}`)
	cases := []struct {
		name  string
		st    state.Store
		token string
	}{
		{"another subject", storeWithRole(t, webIdentityRoleARN, ciTrust), ciToken(t, "dev")},
		{"another provider", storeWithRole(t, webIdentityRoleARN, ciTrust),
			unsignedJWT(t, map[string]any{"iss": "https://other.example.com", "sub": "ci", "aud": "sts.amazonaws.com"})},
		{"a role that does not exist", state.NewMemoryStore(), ciToken(t, "ci")},
		{"a role of that name in another account", storeWithRole(t, "arn:aws:iam::111111111111:role/ci", ciTrust), ciToken(t, "ci")},
		{"a construct the evaluator does not implement", storeWithRole(t, webIdentityRoleARN, unimplemented), ciToken(t, "ci")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Given: a trust policy that does not admit the caller
			r := parsedQueryRequest(t, webIdentityForm(tc.token, nil))

			// When: the unsigned call arrives
			rec, served := enforceSigned(t, tc.st, servedAsWebIdentity, r)

			// Then: AWS's AccessDenied for the call, in Query XML
			if served || rec.Code != http.StatusForbidden ||
				!strings.Contains(rec.Body.String(), "<Code>AccessDenied</Code>") ||
				!strings.Contains(rec.Body.String(), "Not authorized to perform sts:AssumeRoleWithWebIdentity") {
				t.Fatalf("served=%v status=%d body=%q; want AccessDenied", served, rec.Code, rec.Body.String())
			}
		})
	}
}

func TestIAMEnforce_webIdentity_invalidRequestRefusedBeforeTheTrustPolicy(t *testing.T) {
	cases := []struct {
		name  string
		token string
		form  map[string]string
		code  string
	}{
		{"no RoleArn", "a.b.c", map[string]string{"RoleArn": ""}, "MissingParameter"},
		{"no RoleSessionName", "a.b.c", map[string]string{"RoleSessionName": ""}, "MissingParameter"},
		{"no WebIdentityToken", "", map[string]string{"ProviderId": "graph.facebook.com"}, "MissingParameter"},
		{"a token that is not a JWT", "not-a-jwt", nil, "InvalidIdentityToken"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Given: a role trusting every web identity of its provider
			st := storeWithRole(t, webIdentityRoleARN, trustFor("oidc.example.com", `{}`))
			r := parsedQueryRequest(t, webIdentityForm(tc.token, tc.form))

			// When: the call is malformed
			rec, served := enforceSigned(t, st, servedAsWebIdentity, r)

			// Then: STS refuses the request itself, with a 400
			if served || rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "<Code>"+tc.code+"</Code>") {
				t.Fatalf("served=%v status=%d body=%q; want 400 %s", served, rec.Code, rec.Body.String(), tc.code)
			}
		})
	}
}

func TestIAMEnforce_webIdentity_oauthProviderNamedByProviderId(t *testing.T) {
	// Given: a role trusting Facebook's OAuth 2.0 tokens
	st := storeWithRole(t, webIdentityRoleARN, trustFor("graph.facebook.com", `{}`))
	r := parsedQueryRequest(t, webIdentityForm("opaque-access-token", map[string]string{"ProviderId": "graph.facebook.com"}))

	// When: the call presents an opaque access token and names its provider
	rec, served := enforceSigned(t, st, servedAsWebIdentity, r)

	// Then: the provider named is the one the trust policy is matched against
	if !served {
		t.Fatalf("status %d, body %q; want the web identity served", rec.Code, rec.Body.String())
	}
}

func TestIAMEnforce_webIdentity_signedCallDecidedByTheTrustPolicy(t *testing.T) {
	// Given: a signer whose only policy denies everything, and a role trusting
	// the ci subject
	st := storeWithRole(t, webIdentityRoleARN, ciTrust)
	seedIAMUserWithPolicies(t, st, callerAccessKey, []string{denyAllPolicy}, nil)
	r := signAsCaller(parsedQueryRequest(t, webIdentityForm(ciToken(t, "ci"), nil)), "sts")

	// When: the call is signed as well
	rec, served := enforceSigned(t, st, servedAsWebIdentity, r)

	// Then: the signer's policies are not what decides it
	if !served {
		t.Fatalf("status %d, body %q; want the web identity served", rec.Code, rec.Body.String())
	}
}

// failingStore is a store whose reads fail.
type failingStore struct{ state.Store }

func (failingStore) Get(context.Context, string, string) (string, bool, error) {
	return "", false, errors.New("store unavailable")
}

func TestIAMEnforce_webIdentity_unreadableStoreIsAnInternalError(t *testing.T) {
	// Given: a store that cannot be read
	r := parsedQueryRequest(t, webIdentityForm(ciToken(t, "ci"), nil))

	// When: the call arrives
	rec, served := enforceSigned(t, failingStore{state.NewMemoryStore()}, servedAsWebIdentity, r)

	// Then: the failure is Overcast's, not a trust decision
	if served || rec.Code != http.StatusInternalServerError {
		t.Fatalf("served=%v status=%d body=%q; want a 500", served, rec.Code, rec.Body.String())
	}
}

func TestIAMEnforce_webIdentity_unsignedCallsOtherwiseRoutedAreRefused(t *testing.T) {
	st := storeWithRole(t, webIdentityRoleARN, `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":"*","Action":"sts:*"}]}`)
	token := ciToken(t, "ci")
	cases := map[string]func() (*http.Request, QueryRouter){
		"STS AssumeRole": func() (*http.Request, QueryRouter) {
			r := parsedQueryRequest(t, webIdentityForm(token, map[string]string{"Action": "AssumeRole"}))
			return r, stubQueryRouter{route: QueryRoute{Service: "sts", Action: "AssumeRole"}, isQuery: true}
		},
		"a Query call the router serves as IAM": func() (*http.Request, QueryRouter) {
			r := parsedQueryRequest(t, webIdentityForm(token, map[string]string{"Version": "2010-05-08"}))
			return r, stubQueryRouter{route: QueryRoute{Service: "iam", Action: "AssumeRoleWithWebIdentity"}, isQuery: true}
		},
		"a path-routed request naming AssumeRoleWithWebIdentity": func() (*http.Request, QueryRouter) {
			q := webIdentityForm(token, nil)
			return httptest.NewRequest(http.MethodPut, "/bucket?"+q.Encode(), nil), stubQueryRouter{}
		},
	}
	for name, request := range cases {
		t.Run(name, func(t *testing.T) {
			// Given: an unsigned request the router does not serve as STS
			// AssumeRoleWithWebIdentity
			r, queries := request()

			// When: it reaches enforcement
			rec, served := enforceSigned(t, st, queries, r)

			// Then: it is refused as unsigned
			if served || rec.Code != http.StatusForbidden {
				t.Fatalf("served=%v status=%d; want it refused", served, rec.Code)
			}
		})
	}
}
