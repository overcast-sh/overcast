package middleware

import (
	"context"
	"encoding/base64"
	"encoding/json"
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
)

// webIdentityTrust trusts oidc.example.com tokens whose subject is sub.
func webIdentityTrust(sub string) string {
	return `{"Version":"2012-10-17","Statement":[{"Effect":"Allow",
		"Principal":{"Federated":"arn:aws:iam::000000000000:oidc-provider/oidc.example.com"},
		"Action":"sts:AssumeRoleWithWebIdentity",
		"Condition":{"StringEquals":{"oidc.example.com:sub":"` + sub + `","oidc.example.com:aud":"sts.amazonaws.com"}}}]}`
}

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
	return unsignedJWT(t, map[string]any{"iss": webIdentityIssuer, "sub": sub, "aud": "sts.amazonaws.com"})
}

// storeWithRole is a store holding the ci role with trust as its trust policy.
func storeWithRole(t *testing.T, trust string) state.Store {
	t.Helper()
	st := state.NewMemoryStore()
	b, _ := json.Marshal(map[string]string{"RoleName": "ci", "Arn": webIdentityRoleARN, "AssumeRolePolicyDocument": trust})
	if err := st.Set(context.Background(), iamRolesNamespace, "ci", string(b)); err != nil {
		t.Fatal(err)
	}
	return st
}

// webIdentityCall is an unsigned, parsed AssumeRoleWithWebIdentity Query form
// carrying extra, and the route the router serves it as.
func webIdentityCall(t *testing.T, extra url.Values) (*http.Request, QueryRouter) {
	t.Helper()
	form := url.Values{
		"Action":          {"AssumeRoleWithWebIdentity"},
		"Version":         {"2011-06-15"},
		"RoleArn":         {webIdentityRoleARN},
		"RoleSessionName": {"build"},
	}
	for k, v := range extra {
		form[k] = v
	}
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if err := r.ParseForm(); err != nil {
		t.Fatal(err)
	}
	return r, stubQueryRouter{route: QueryRoute{Service: "sts", Action: "AssumeRoleWithWebIdentity"}, isQuery: true}
}

func TestIAMEnforce_webIdentityTheTrustPolicyAllowsIsServedUnsigned(t *testing.T) {
	// Given: a role trusting the ci subject of oidc.example.com
	st := storeWithRole(t, webIdentityTrust("ci"))
	r, queries := webIdentityCall(t, url.Values{"WebIdentityToken": {ciToken(t, "ci")}})

	// When: an unsigned call presents a token for that subject
	rec, served := enforceSigned(t, st, queries, r)

	// Then: the trust policy admits it, and it is served
	if !served {
		t.Fatalf("status %d, body %q; want the web identity served", rec.Code, rec.Body.String())
	}
}

func TestIAMEnforce_webIdentityTheTrustPolicyRefusesIsAccessDenied(t *testing.T) {
	cases := map[string]struct {
		st    state.Store
		extra url.Values
	}{
		"another subject": {storeWithRole(t, webIdentityTrust("ci")), url.Values{"WebIdentityToken": {ciToken(t, "dev")}}},
		"another provider": {storeWithRole(t, webIdentityTrust("ci")), url.Values{"WebIdentityToken": {
			unsignedJWT(t, map[string]any{"iss": "https://other.example.com", "sub": "ci", "aud": "sts.amazonaws.com"}),
		}}},
		"a role that does not exist": {state.NewMemoryStore(), url.Values{"WebIdentityToken": {ciToken(t, "ci")}}},
		"a construct the evaluator does not implement": {storeWithRole(t, `{"Version":"2012-10-17","Statement":[{"Effect":"Allow",
			"Principal":{"Federated":"oidc.example.com"},"Action":"sts:AssumeRoleWithWebIdentity",
			"Condition":{"ForAnyValue:StringLike":{"oidc.example.com:amr":"authenticated"}}}]}`), url.Values{"WebIdentityToken": {ciToken(t, "ci")}}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			// Given: a trust policy that does not admit the caller
			r, queries := webIdentityCall(t, tc.extra)

			// When: the unsigned call arrives
			rec, served := enforceSigned(t, tc.st, queries, r)

			// Then: AWS's AccessDenied for the call, in Query XML
			if served || rec.Code != http.StatusForbidden ||
				!strings.Contains(rec.Body.String(), "<Code>AccessDenied</Code>") ||
				!strings.Contains(rec.Body.String(), "Not authorized to perform sts:AssumeRoleWithWebIdentity") {
				t.Fatalf("served=%v status=%d body=%q; want AccessDenied", served, rec.Code, rec.Body.String())
			}
		})
	}
}

func TestIAMEnforce_webIdentityWithATokenThatIsNotAJWTIsInvalidIdentityToken(t *testing.T) {
	// Given: a role trusting the ci subject
	st := storeWithRole(t, webIdentityTrust("ci"))
	r, queries := webIdentityCall(t, url.Values{"WebIdentityToken": {"not-a-jwt"}})

	// When: the call presents a token that is not an ID token
	rec, served := enforceSigned(t, st, queries, r)

	// Then: STS refuses the token itself
	if served || rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "<Code>InvalidIdentityToken</Code>") {
		t.Fatalf("served=%v status=%d body=%q; want InvalidIdentityToken", served, rec.Code, rec.Body.String())
	}
}

func TestIAMEnforce_webIdentityWithAnOAuthProviderIsMatchedByProviderId(t *testing.T) {
	// Given: a role trusting Facebook's OAuth 2.0 tokens
	st := storeWithRole(t, `{"Version":"2012-10-17","Statement":[{"Effect":"Allow",
		"Principal":{"Federated":"graph.facebook.com"},"Action":"sts:AssumeRoleWithWebIdentity"}]}`)
	r, queries := webIdentityCall(t, url.Values{"WebIdentityToken": {"opaque-access-token"}, "ProviderId": {"graph.facebook.com"}})

	// When: the call presents an opaque access token and names its provider
	rec, served := enforceSigned(t, st, queries, r)

	// Then: the provider named is the one the trust policy is matched against
	if !served {
		t.Fatalf("status %d, body %q; want the web identity served", rec.Code, rec.Body.String())
	}
}

func TestIAMEnforce_signedWebIdentityIsDecidedByTheTrustPolicy(t *testing.T) {
	// Given: a signer whose only policy denies everything, and a role trusting
	// the ci subject
	st := storeWithRole(t, webIdentityTrust("ci"))
	seedIAMUserWithPolicies(t, st, callerAccessKey, []string{denyAllPolicy}, nil)
	r, queries := webIdentityCall(t, url.Values{"WebIdentityToken": {ciToken(t, "ci")}})
	signAsCaller(r, "sts")

	// When: the call is signed as well
	rec, served := enforceSigned(t, st, queries, r)

	// Then: the signer's policies are not what decides it
	if !served {
		t.Fatalf("status %d, body %q; want the web identity served", rec.Code, rec.Body.String())
	}
}

func TestIAMEnforce_unsignedCallsTheTrustPolicyDoesNotDecideAreRefused(t *testing.T) {
	st := storeWithRole(t, `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":"*","Action":"sts:*"}]}`)
	cases := map[string]func() (*http.Request, QueryRouter){
		"AssumeRole": func() (*http.Request, QueryRouter) {
			r, queries := queryCall(t, "sts", "AssumeRole", "2011-06-15")
			r.Header.Del("Authorization")
			return r, queries
		},
		"a path-routed request naming AssumeRoleWithWebIdentity": func() (*http.Request, QueryRouter) {
			r := httptest.NewRequest(http.MethodPut, "/bucket?Action=AssumeRoleWithWebIdentity&RoleArn="+url.QueryEscape(webIdentityRoleARN), nil)
			return r, stubQueryRouter{}
		},
	}
	for name, request := range cases {
		t.Run(name, func(t *testing.T) {
			// Given: an unsigned request the router does not serve as
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
