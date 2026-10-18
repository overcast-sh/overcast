package lambdaweb

import (
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math/big"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"

	"github.com/overcast-sh/overcast/internal/clock"
	"github.com/overcast-sh/overcast/internal/iampolicy"
	"github.com/overcast-sh/overcast/internal/middleware"
	"github.com/overcast-sh/overcast/internal/state"
)

const (
	testAccount   = "111122223333"
	testOtherAcct = "999988887777"
	testRegion    = "us-east-1"
	testHost      = "abc123.s1.lambda-web.us-east-1.localhost:4566"
	testAccessKey = "AKIAALICEEXAMPLE"
	testSecret    = "alice-secret-key"
	testPrefix    = "/_overcast/lambdaweb/invoke/abc123.s1/us-east-1"
	testEndpoint  = "arn:aws:lambda:us-east-1:" + testAccount + ":web-function/shop/endpoint/prod"
	invokeActionN = "lambda:InvokeWebFunctionEndpoint"
)

var testNow = time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)

func testAuthContext(policy string) authContext {
	c := authContext{
		Function:    &webFunction{Name: "shop", ARN: "arn:aws:lambda:us-east-1:" + testAccount + ":web-function/shop"},
		Endpoint:    &endpoint{Name: "prod", Type: endpointHomeRegion, AuthType: authIam},
		EndpointARN: testEndpoint,
		Region:      testRegion,
	}
	if policy != "" {
		c.Policy = &policyRecord{Policy: policy, RevisionID: "rev-" + policy[:min(len(policy), 12)]}
	}
	return c
}

// signedRequest returns a request to path signed with the SDK's SigV4 signer
// for the given scope, then rewritten the way HostAddressing rewrites it.
func signedRequest(t *testing.T, path, region, service, secret string) *http.Request {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, "http://"+testHost+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set(headerContentSHA, "UNSIGNED-PAYLOAD")
	creds := aws.Credentials{AccessKeyID: testAccessKey, SecretAccessKey: secret}
	if err := v4.NewSigner().SignHTTP(context.Background(), creds, req, "UNSIGNED-PAYLOAD", service, region, testNow); err != nil {
		t.Fatal(err)
	}
	middleware.PrefixPath(req, testPrefix)
	return req
}

func newAuthorizer(opts AuthorizerOptions) *Authorizer {
	if opts.Clock == nil {
		mock := clock.NewMock()
		mock.Set(testNow)
		opts.Clock = mock
	}
	return NewAuthorizer(opts)
}

func TestParseSignature_sigV4(t *testing.T) {
	// Given: a SigV4 Authorization header with the headers AWS requires
	r, _ := http.NewRequest(http.MethodGet, "http://x/", nil)
	r.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=AKIA1/20260301/us-east-1/lambda/aws4_request, SignedHeaders=host;x-amz-date, Signature=abcd")
	r.Header.Set(headerAmzDate, "20260301T120000Z")
	r.Header.Set(headerContentSHA, "UNSIGNED-PAYLOAD")

	// When: it is parsed
	s, reason := parseSignature(r)

	// Then: every field is read and the request is well formed
	if reason != "" {
		t.Fatalf("reason = %q", reason)
	}
	if s.v4a || s.accessKey != "AKIA1" || s.date != "20260301" || s.region != "us-east-1" || s.service != "lambda" ||
		s.signedHeaders != "host;x-amz-date" || s.sig != "abcd" {
		t.Fatalf("parsed = %+v", s)
	}
}

func TestParseSignature_sigV4a(t *testing.T) {
	// Given: a SigV4a header, whose scope has no Region, and a Region set header
	r, _ := http.NewRequest(http.MethodGet, "http://x/", nil)
	r.Header.Set("Authorization", "AWS4-ECDSA-P256-SHA256 Credential=AKIA1/20260301/lambda/aws4_request, SignedHeaders=host;x-amz-date;x-amz-region-set, Signature=3045")
	r.Header.Set(headerAmzDate, "20260301T120000Z")
	r.Header.Set(headerContentSHA, "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855")
	r.Header.Set(headerRegionSet, "*")

	// When: it is parsed
	s, reason := parseSignature(r)

	// Then: it is SigV4a with the set
	if reason != "" || !s.v4a || s.service != "lambda" || s.regionSet != "*" {
		t.Fatalf("parsed = %+v, reason %q", s, reason)
	}
}

func TestParseSignature_malformed(t *testing.T) {
	good := map[string]string{
		"Authorization":    "AWS4-HMAC-SHA256 Credential=AKIA1/20260301/us-east-1/lambda/aws4_request, SignedHeaders=host;x-amz-date, Signature=abcd",
		headerAmzDate:      "20260301T120000Z",
		headerContentSHA:   "UNSIGNED-PAYLOAD",
		"X-Amz-Unrelated":  "x",
		"X-Amz-Region-Set": "",
	}
	cases := []struct {
		name   string
		mutate func(h map[string]string)
		want   string
	}{
		{"no authorization", func(h map[string]string) { delete(h, "Authorization") }, "no Authorization"},
		{"bearer token", func(h map[string]string) { h["Authorization"] = "Bearer abc" }, "unsupported"},
		{"no signature", func(h map[string]string) {
			h["Authorization"] = "AWS4-HMAC-SHA256 Credential=AKIA1/20260301/us-east-1/lambda/aws4_request, SignedHeaders=host"
		}, "lacks"},
		{"short scope", func(h map[string]string) {
			h["Authorization"] = "AWS4-HMAC-SHA256 Credential=AKIA1/20260301/lambda/aws4_request, SignedHeaders=host, Signature=ab"
		}, "malformed credential scope"},
		{"missing date", func(h map[string]string) { delete(h, headerAmzDate) }, "X-Amz-Date"},
		{"date outside scope day", func(h map[string]string) { h[headerAmzDate] = "20260302T120000Z" }, "X-Amz-Date"},
		{"missing content hash", func(h map[string]string) { delete(h, headerContentSHA) }, "X-Amz-Content-Sha256"},
		{"chunked signing", func(h map[string]string) { h[headerContentSHA] = "STREAMING-AWS4-HMAC-SHA256-PAYLOAD" }, "chunked"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Given: a well formed request with one defect
			h := map[string]string{}
			for k, v := range good {
				h[k] = v
			}
			tc.mutate(h)
			r, _ := http.NewRequest(http.MethodGet, "http://x/", nil)
			for k, v := range h {
				if v != "" {
					r.Header.Set(k, v)
				}
			}

			// When: it is parsed
			_, reason := parseSignature(r)

			// Then: it is refused for that defect
			if !strings.Contains(reason, tc.want) {
				t.Fatalf("reason = %q, want it to contain %q", reason, tc.want)
			}
		})
	}
}

func TestAuthorize_scope(t *testing.T) {
	multi := testAuthContext("")
	multi.Endpoint.Type = endpointMultiRegion
	multi.Endpoint.Regions = []string{"us-east-1", "eu-west-1"}
	multi.Region = ""

	cases := []struct {
		name string
		ctx  authContext
		req  func(t *testing.T) *http.Request
		ok   bool
	}{
		{"matching scope", testAuthContext(""), func(t *testing.T) *http.Request {
			return signedRequest(t, "/x", testRegion, "lambda", testSecret)
		}, true},
		{"wrong service", testAuthContext(""), func(t *testing.T) *http.Request {
			return signedRequest(t, "/x", testRegion, "s3", testSecret)
		}, false},
		{"wrong region", testAuthContext(""), func(t *testing.T) *http.Request {
			return signedRequest(t, "/x", "eu-west-1", "lambda", testSecret)
		}, false},
		{"multi-region endpoint refuses SigV4", multi, func(t *testing.T) *http.Request {
			return signedRequest(t, "/x", testRegion, "lambda", testSecret)
		}, false},
		{"missing content hash", testAuthContext(""), func(t *testing.T) *http.Request {
			r := signedRequest(t, "/x", testRegion, "lambda", testSecret)
			r.Header.Del(headerContentSHA)
			return r
		}, false},
		{"unsigned request", testAuthContext(""), func(t *testing.T) *http.Request {
			r, _ := http.NewRequest(http.MethodGet, "http://"+testHost+"/x", nil)
			return r
		}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Given: an authorizer with validation and enforcement off, so only the
			// always-on checks apply
			a := newAuthorizer(AuthorizerOptions{})

			// When: the request is authorised
			perr := a.Authorize(context.Background(), tc.req(t), tc.ctx)

			// Then: only a well scoped request passes; a refusal is a 403 with no error type
			if tc.ok != (perr == nil) {
				t.Fatalf("Authorize = %v, want ok=%v", perr, tc.ok)
			}
			if perr != nil && (perr.Status != http.StatusForbidden || perr.Type != "") {
				t.Fatalf("refusal = %+v, want 403 with no error type", perr)
			}
		})
	}
}

func TestAuthorize_sigV4Validation(t *testing.T) {
	cases := []struct {
		name     string
		path     string
		secret   string
		mutate   func(r *http.Request)
		validate bool
		ok       bool
	}{
		{"valid signature", "/orders/1?x=1&y=2", testSecret, nil, true, true},
		{"path needing escapes is signed once more", "/a b/c%2Fd", testSecret, nil, true, true},
		{"wrong secret", "/orders", "other-secret", nil, true, false},
		{"wrong secret passes with validation off", "/orders", "other-secret", nil, false, true},
		{"tampered path", "/orders", testSecret, func(r *http.Request) { r.URL.Path += "x" }, true, false},
		{"tampered query", "/orders?a=1", testSecret, func(r *http.Request) { r.URL.RawQuery = "a=2" }, true, false},
		{"tampered host", "/orders", testSecret, func(r *http.Request) { r.Host = "evil." + r.Host }, true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Given: a request signed with the SDK signer for the original path, then
			// rewritten under the invoke prefix, and a store whose secret for the key is testSecret
			a := newAuthorizer(AuthorizerOptions{ValidateSignatures: tc.validate, Secrets: aliceSecrets(t)})
			r := signedRequest(t, tc.path, testRegion, "lambda", tc.secret)
			if tc.mutate != nil {
				tc.mutate(r)
			}

			// When: it is authorised
			perr := a.Authorize(context.Background(), r, testAuthContext(""))

			// Then: the signature is checked against the ORIGINAL path and Host
			if tc.ok != (perr == nil) {
				t.Fatalf("Authorize = %v, want ok=%v", perr, tc.ok)
			}
		})
	}
}

func TestAuthorize_sigV4Validation_expired(t *testing.T) {
	// Given: a request signed ten minutes before the authorizer's clock
	a := newAuthorizer(AuthorizerOptions{ValidateSignatures: true, Secrets: aliceSecrets(t)})
	r := signedRequest(t, "/x", testRegion, "lambda", testSecret)
	mock := clock.NewMock()
	mock.Set(testNow.Add(10 * time.Minute))
	a.opts.Clock = mock

	// When: it is authorised
	perr := a.Authorize(context.Background(), r, testAuthContext(""))

	// Then: the clock skew refuses it
	if perr == nil {
		t.Fatal("an expired signature was admitted")
	}
}

func TestStripInvokePrefix(t *testing.T) {
	cases := map[string]string{
		testPrefix + "/orders/1":             "/orders/1",
		testPrefix + "/":                     "/",
		testPrefix + "/a%2Fb":                "/a%2Fb",
		"/_overcast/lambdaweb/invoke/abc//x": "/x", // a global endpoint: empty Region
		"/orders":                            "/orders",
	}
	for in, want := range cases {
		// Given an escaped path / When the invoke prefix is stripped / Then the client's path remains
		if got := StripInvokePrefix(in); got != want {
			t.Errorf("StripInvokePrefix(%q) = %q, want %q", in, got, want)
		}
	}
}

// ─── SigV4a ──────────────────────────────────────────────────────────────────

func TestDeriveSigV4aKey_matchesSDKKnownAnswer(t *testing.T) {
	// Given: the access key pair and public point from the AWS SDK's own
	// TestDeriveECDSAKeyPairFromSecret
	const (
		accessKey = "AKISORANDOMAASORANDOM"
		secretKey = "q+jcrXGc+0zWN6uzclKVhvMmUsIfRPa4rlRandom"
	)
	wantX, _ := new(big.Int).SetString("15D242CEEBF8D8169FD6A8B5A746C41140414C3B07579038DA06AF89190FFFCB", 16)
	wantY, _ := new(big.Int).SetString("515242CEDD82E94799482E4C0514B505AFCCF2C0C98D6A553BF539F424C5EC0", 16)

	// When: the key is derived
	key := deriveSigV4aKey(accessKey, secretKey)

	// Then: it is the SDK's key
	if key.X.Cmp(wantX) != 0 || key.Y.Cmp(wantY) != 0 {
		t.Fatalf("derived (%x, %x), want (%x, %x)", key.X, key.Y, wantX, wantY)
	}
}

// signV4a signs r with SigV4a as a client would, then rewrites its path.
func signV4a(t *testing.T, path, regionSet, secret string) *http.Request {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, "http://"+testHost+path, strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set(headerContentSHA, "UNSIGNED-PAYLOAD")
	req.Header.Set(headerAmzDate, testNow.Format("20060102T150405Z"))
	req.Header.Set(headerRegionSet, regionSet)

	signed := "host;x-amz-content-sha256;x-amz-date;x-amz-region-set"
	canonical := strings.Join([]string{
		req.Method,
		escapeSignedPath(req.URL.EscapedPath()),
		req.URL.RawQuery,
		"host:" + req.Host + "\nx-amz-content-sha256:UNSIGNED-PAYLOAD\nx-amz-date:" + req.Header.Get(headerAmzDate) +
			"\nx-amz-region-set:" + regionSet + "\n",
		signed,
		"UNSIGNED-PAYLOAD",
	}, "\n")
	date := testNow.Format("20060102")
	h := sha256.Sum256([]byte(canonical))
	sts := authAlgSigV4a + "\n" + req.Header.Get(headerAmzDate) + "\n" + date + "/lambda/aws4_request\n" + hex.EncodeToString(h[:])
	digest := sha256.Sum256([]byte(sts))
	der, err := ecdsa.SignASN1(rand.Reader, deriveSigV4aKey(testAccessKey, secret), digest[:])
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", authAlgSigV4a+" Credential="+testAccessKey+"/"+date+"/lambda/aws4_request, SignedHeaders="+
		signed+", Signature="+hex.EncodeToString(der))
	middleware.PrefixPath(req, testPrefix)
	return req
}

func multiRegionContext() authContext {
	c := testAuthContext("")
	c.Endpoint.Type = endpointMultiRegion
	c.Endpoint.Regions = []string{"us-east-1", "eu-west-1"}
	c.Region = ""
	return c
}

func TestAuthorize_sigV4a(t *testing.T) {
	cases := []struct {
		name      string
		ctx       authContext
		regionSet string
		secret    string
		mutate    func(r *http.Request)
		ok        bool
	}{
		{"multi-region endpoint, region set *", multiRegionContext(), "*", testSecret, nil, true},
		{"multi-region endpoint, set covering its regions", multiRegionContext(), "us-east-1,eu-west-1", testSecret, nil, true},
		{"multi-region endpoint, set missing a region", multiRegionContext(), "us-east-1", testSecret, nil, false},
		{"regional endpoint accepts a set containing its region", testAuthContext(""), "us-*", testSecret, nil, true},
		{"regional endpoint refuses a set without its region", testAuthContext(""), "eu-west-1", testSecret, nil, false},
		{"wrong secret", multiRegionContext(), "*", "other-secret", nil, false},
		{"tampered body hash", multiRegionContext(), "*", testSecret, func(r *http.Request) { r.Header.Set(headerContentSHA, "e3b0") }, false},
		{"tampered path", multiRegionContext(), "*", testSecret, func(r *http.Request) { r.URL.Path += "x" }, false},
		{"swapped region set", multiRegionContext(), "*", testSecret, func(r *http.Request) { r.Header.Set(headerRegionSet, "us-east-1,eu-west-1") }, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Given: a SigV4a request for a path with an escape, validation on, and a
			// store holding the key's secret
			a := newAuthorizer(AuthorizerOptions{ValidateSignatures: true, Secrets: aliceSecrets(t)})
			r := signV4a(t, "/v1/a b?q=1", tc.regionSet, tc.secret)
			if tc.mutate != nil {
				tc.mutate(r)
			}

			// When: it is authorised
			perr := a.Authorize(context.Background(), r, tc.ctx)

			// Then: the signature verifies against the derived key, or does not
			if tc.ok != (perr == nil) {
				t.Fatalf("Authorize = %v, want ok=%v", perr, tc.ok)
			}
		})
	}
}

func TestAuthorize_sigV4a_unsignedRegionSetIsRefused(t *testing.T) {
	// Given: a SigV4a request whose X-Amz-Region-Set is not among the signed headers
	r := signV4a(t, "/x", "*", testSecret)
	r.Header.Set("Authorization", strings.Replace(r.Header.Get("Authorization"), ";x-amz-region-set", "", 1))
	a := newAuthorizer(AuthorizerOptions{ValidateSignatures: true, Secrets: aliceSecrets(t)})

	// When / Then: it is refused
	if perr := a.Authorize(context.Background(), r, multiRegionContext()); perr == nil {
		t.Fatal("an unsigned Region set was admitted")
	}
}

// ─── IAM enforcement ─────────────────────────────────────────────────────────

type iamUser struct {
	name, account, key string
	policies           []string
}

func iamStore(t *testing.T, users ...iamUser) state.Store {
	t.Helper()
	st := state.NewMemoryStore()
	for _, u := range users {
		inline := map[string]string{}
		for i, p := range u.policies {
			inline["p"+string(rune('a'+i))] = p
		}
		rec, _ := json.Marshal(map[string]any{
			"UserName":       u.name,
			"Arn":            "arn:aws:iam::" + u.account + ":user/" + u.name,
			"AccessKeys":     []map[string]string{{"AccessKeyId": u.key, "SecretAccessKey": testSecret}},
			"InlinePolicies": inline,
		})
		if err := st.Set(context.Background(), "iam:users", u.name, string(rec)); err != nil {
			t.Fatal(err)
		}
	}
	return st
}

func aliceSecrets(t *testing.T) middleware.SecretResolver {
	return middleware.NewSecretResolver(iamStore(t, iamUser{name: "alice", account: testAccount, key: testAccessKey}))
}

func allowInvoke(resource string) string {
	return `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"` + invokeActionN + `","Resource":"` + resource + `"}]}`
}

const denyInvoke = `{"Version":"2012-10-17","Statement":[{"Effect":"Deny","Action":"lambda:*","Resource":"*"}]}`

func resourcePolicy(principal, condition string) string {
	if condition != "" {
		condition = `,"Condition":` + condition
	}
	return `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":` + principal + `,"Action":"` +
		invokeActionN + `","Resource":"*"` + condition + `}]}`
}

func TestAuthorize_iamMatrix(t *testing.T) {
	aliceARN := `{"AWS":"arn:aws:iam::` + testAccount + `:user/alice"}`
	cases := []struct {
		name     string
		user     iamUser
		resource string // function resource policy
		ok       bool
	}{
		{"same account, identity policy only",
			iamUser{name: "alice", account: testAccount, policies: []string{allowInvoke(testEndpoint)}}, "", true},
		{"same account, resource policy only",
			iamUser{name: "alice", account: testAccount}, resourcePolicy(aliceARN, ""), true},
		{"same account, no policy at all",
			iamUser{name: "alice", account: testAccount}, "", false},
		{"same account, wildcard endpoint ARN in identity policy",
			iamUser{name: "alice", account: testAccount, policies: []string{allowInvoke("arn:aws:lambda:us-east-1:" + testAccount + ":web-function/shop/endpoint/*")}}, "", true},
		{"same account, identity policy for a different endpoint",
			iamUser{name: "alice", account: testAccount, policies: []string{allowInvoke("arn:aws:lambda:us-east-1:" + testAccount + ":web-function/shop/endpoint/staging")}}, "", false},
		{"explicit deny in the identity policy beats a resource allow",
			iamUser{name: "alice", account: testAccount, policies: []string{denyInvoke}}, resourcePolicy(aliceARN, ""), false},
		{"explicit deny in the resource policy beats an identity allow",
			iamUser{name: "alice", account: testAccount, policies: []string{allowInvoke("*")}},
			`{"Version":"2012-10-17","Statement":[{"Effect":"Deny","Principal":"*","Action":"lambda:*","Resource":"*"}]}`, false},
		{"cross account, identity policy alone",
			iamUser{name: "alice", account: testOtherAcct, policies: []string{allowInvoke("*")}}, "", false},
		{"cross account, resource policy alone",
			iamUser{name: "alice", account: testOtherAcct}, resourcePolicy(`{"AWS":"arn:aws:iam::`+testOtherAcct+`:user/alice"}`, ""), false},
		{"cross account, both",
			iamUser{name: "alice", account: testOtherAcct, policies: []string{allowInvoke("*")}},
			resourcePolicy(`{"AWS":"arn:aws:iam::`+testOtherAcct+`:root"}`, ""), true},
		{"resource policy naming the account",
			iamUser{name: "alice", account: testAccount}, resourcePolicy(`{"AWS":"`+testAccount+`"}`, ""), true},
		{"resource policy with a wildcard principal and a matching org id",
			iamUser{name: "alice", account: testAccount}, resourcePolicy(`"*"`, `{"StringEquals":{"aws:PrincipalOrgID":"o-abc123"}}`), true},
		{"resource policy with a wildcard principal and another org id",
			iamUser{name: "alice", account: testAccount}, resourcePolicy(`"*"`, `{"StringEquals":{"aws:PrincipalOrgID":"o-zzzzzz"}}`), false},
		{"identity policy conditioned on the function's tag",
			iamUser{name: "alice", account: testAccount, policies: []string{
				`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"lambda:InvokeWebFunctionEndpoint","Resource":"*","Condition":{"StringEquals":{"aws:ResourceTag/Env":"prod"}}}]}`}}, "", true},
		{"identity policy conditioned on a tag the function lacks",
			iamUser{name: "alice", account: testAccount, policies: []string{
				`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"lambda:InvokeWebFunctionEndpoint","Resource":"*","Condition":{"StringEquals":{"aws:ResourceTag/Env":"dev"}}}]}`}}, "", false},
		{"identity policy conditioned on the endpoint's auth type",
			iamUser{name: "alice", account: testAccount, policies: []string{
				`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"lambda:InvokeWebFunctionEndpoint","Resource":"*","Condition":{"StringEquals":{"lambda:Resource/WebFunctionAuthType":"IamAuth"}}}]}`}}, "", true},
		{"resource policy condition on the caller's account",
			iamUser{name: "alice", account: testAccount}, resourcePolicy(`"*"`, `{"StringEquals":{"aws:PrincipalAccount":"`+testAccount+`"}}`), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Given: IAM enforcement on, a user in the store, and the function's tags and org
			st := iamStore(t, withKey(tc.user))
			a := newAuthorizer(AuthorizerOptions{
				EnforceIAM:   true,
				Principals:   middleware.NewIAMPrincipalResolver(st),
				ResourceTags: func(context.Context, string) map[string]string { return map[string]string{"Env": "prod"} },
				OrgID:        func(context.Context, string) string { return "o-abc123" },
			})
			r := signedRequest(t, "/x", testRegion, "lambda", testSecret)

			// When: the request is authorised for lambda:InvokeWebFunctionEndpoint
			ctx, slot := WithPrincipalSlot(context.Background())
			perr := a.Authorize(ctx, r, testAuthContext(tc.resource))

			// Then: the AWS combination rule decides, and the principal is reported on success
			if tc.ok != (perr == nil) {
				t.Fatalf("Authorize = %v, want ok=%v", perr, tc.ok)
			}
			if tc.ok && slot.Principal().ARN != "arn:aws:iam::"+tc.user.account+":user/alice" {
				t.Fatalf("principal = %+v", slot.Principal())
			}
		})
	}
}

func withKey(u iamUser) iamUser { u.key = testAccessKey; return u }

func TestAuthorize_iamEnforcement_unknownAccessKey(t *testing.T) {
	// Given: IAM enforcement on and a key no IAM user owns, even though the
	// resource policy allows every principal in the org
	a := newAuthorizer(AuthorizerOptions{EnforceIAM: true, Principals: middleware.NewIAMPrincipalResolver(iamStore(t))})
	r := signedRequest(t, "/x", testRegion, "lambda", testSecret)

	// When / Then: it is refused, as IAMEnforce refuses an unknown principal
	if perr := a.Authorize(context.Background(), r, testAuthContext(allowInvoke("*"))); perr == nil {
		t.Fatal("an unknown principal was admitted")
	}
}

func TestAuthorize_iamEnforcementOff_admitsAnyWellFormedRequest(t *testing.T) {
	// Given: enforcement off and a resource policy that would deny everyone
	a := newAuthorizer(AuthorizerOptions{})
	r := signedRequest(t, "/x", testRegion, "lambda", testSecret)

	// When: the request is authorised
	perr := a.Authorize(context.Background(), r, testAuthContext(`{"Version":"2012-10-17","Statement":[{"Effect":"Deny","Principal":"*","Action":"*","Resource":"*"}]}`))

	// Then: Overcast is not a security boundary, so it is admitted
	if perr != nil {
		t.Fatalf("Authorize = %v", perr)
	}
}

func TestAuthorize_unparsableResourcePolicy_failsClosedWhenEnforcing(t *testing.T) {
	// Given: enforcement on and a stored policy that is not JSON
	st := iamStore(t, withKey(iamUser{name: "alice", account: testAccount, policies: []string{allowInvoke("*")}}))
	a := newAuthorizer(AuthorizerOptions{EnforceIAM: true, Principals: middleware.NewIAMPrincipalResolver(st)})
	r := signedRequest(t, "/x", testRegion, "lambda", testSecret)

	// When / Then: the request is refused rather than evaluated on half a policy
	if perr := a.Authorize(context.Background(), r, testAuthContext("not json")); perr == nil {
		t.Fatal("an unreadable policy was ignored")
	}
}

// ─── CloudFront / service principals ─────────────────────────────────────────

const cloudFrontARN = "arn:aws:cloudfront::" + testAccount + ":distribution/E123ABC"

func cloudFrontPolicy(sourceARN string) string {
	return resourcePolicy(`{"Service":"cloudfront.amazonaws.com"}`,
		`{"ArnLike":{"aws:SourceArn":"`+sourceARN+`"}}`)
}

func cloudFrontRequest() *http.Request {
	r, _ := http.NewRequest(http.MethodGet, "http://"+testHost+"/x", nil)
	return r
}

func TestAuthorize_cloudFrontServicePrincipal(t *testing.T) {
	cases := []struct {
		name   string
		policy string
		trust  bool // whether the engine verified the distribution
		ok     bool
	}{
		{"policy names the distribution", cloudFrontPolicy(cloudFrontARN), true, true},
		{"policy uses a wildcard SourceArn", cloudFrontPolicy("arn:aws:cloudfront::" + testAccount + ":distribution/*"), true, true},
		{"policy names another distribution", cloudFrontPolicy("arn:aws:cloudfront::" + testAccount + ":distribution/EOTHER"), true, false},
		{"no policy", "", true, false},
		{"identity-style policy for a user does not admit a service", resourcePolicy(`{"AWS":"`+testAccount+`"}`, ""), true, false},
		{"no verified distribution means an unsigned request", cloudFrontPolicy(cloudFrontARN), false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Given: enforcement on and a request carrying CloudFront's service context
			// (and no signature of a user's)
			a := newAuthorizer(AuthorizerOptions{
				EnforceIAM: true,
				Principals: middleware.NewIAMPrincipalResolver(iamStore(t)),
			})

			// When: it is authorised
			ctx, slot := WithPrincipalSlot(context.Background())
			ac := testAuthContext(tc.policy)
			if tc.trust {
				ac.SourceARN = cloudFrontARN
			}
			perr := a.Authorize(ctx, cloudFrontRequest(), ac)

			// Then: only the resource policy can admit the service
			if tc.ok != (perr == nil) {
				t.Fatalf("Authorize = %v, want ok=%v", perr, tc.ok)
			}
			if tc.ok && slot.Principal().Service != "cloudfront.amazonaws.com" {
				t.Fatalf("principal = %+v", slot.Principal())
			}
		})
	}
}

func TestAuthorize_serviceCallerFromContext(t *testing.T) {
	// Given: an in-process service caller, which no client can forge, and the
	// header trust left off
	a := newAuthorizer(AuthorizerOptions{EnforceIAM: true, Principals: middleware.NewIAMPrincipalResolver(iamStore(t))})
	ctx := WithServiceCaller(context.Background(), ServiceCaller{Principal: "cloudfront.amazonaws.com", SourceARN: cloudFrontARN})
	r, _ := http.NewRequest(http.MethodGet, "http://"+testHost+"/x", nil)

	// When / Then: the context value is honoured and the policy decides
	if perr := a.Authorize(ctx, r, testAuthContext(cloudFrontPolicy(cloudFrontARN))); perr != nil {
		t.Fatalf("Authorize = %v", perr)
	}
}

// ─── Decision table ──────────────────────────────────────────────────────────

func TestDecideInvoke_table(t *testing.T) {
	// The decision is pure: statements in, verdict out.
	parse := func(doc string) []iampolicy.Statement {
		stmts, err := parseTestPolicy(doc)
		if err != nil {
			t.Fatal(err)
		}
		return stmts
	}
	allow := parse(allowInvoke("*"))
	deny := parse(denyInvoke)
	resAllow := parse(resourcePolicy(`"*"`, `{"StringEquals":{"aws:PrincipalAccount":"`+testAccount+`"}}`))
	resDeny := parse(`{"Version":"2012-10-17","Statement":[{"Effect":"Deny","Principal":"*","Action":"lambda:*","Resource":"*"}]}`)
	same := callerIdentity{ARN: "arn:aws:iam::" + testAccount + ":user/a", Account: testAccount, Known: true}
	other := callerIdentity{ARN: "arn:aws:iam::" + testOtherAcct + ":user/a", Account: testOtherAcct, Known: true}

	cases := []struct {
		name     string
		caller   callerIdentity
		identity []iampolicy.Statement
		resource []iampolicy.Statement
		want     bool
	}{
		{"same account: identity only", same, allow, nil, true},
		{"same account: resource only", same, nil, resAllow, true},
		{"same account: neither", same, nil, nil, false},
		{"same account: identity allow, resource deny", same, allow, resDeny, false},
		{"same account: identity deny, resource allow", same, deny, resAllow, false},
		{"cross account: identity only", other, allow, nil, false},
		{"cross account: resource only", other, nil, parse(resourcePolicy(`"*"`, "")), false},
		{"cross account: both", other, allow, parse(resourcePolicy(`"*"`, "")), true},
		{"cross account: both but explicit deny", other, allow, append(parse(resourcePolicy(`"*"`, "")), resDeny...), false},
		{"unknown principal counts as cross account", callerIdentity{}, nil, parse(resourcePolicy(`"*"`, "")), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Given: a caller, its identity statements and the function's resource policy
			in := decisionInput{
				Action: invokeActionN, Resource: testEndpoint, EndpointAccount: testAccount,
				Caller: tc.caller, Identity: tc.identity, ResourcePolicy: tc.resource,
				Context: map[string]string{"aws:principalaccount": tc.caller.Account},
			}

			// When: the decision is made
			got := decideInvoke(in)

			// Then: it follows the same-account / cross-account rule
			if got.Allowed != tc.want {
				t.Fatalf("decideInvoke = %+v, want allowed=%v", got, tc.want)
			}
			if !got.Allowed && got.Reason == "" {
				t.Fatal("a refusal gave no reason")
			}
		})
	}
}

func TestPolicyCache_evictsLeastRecentlyUsed(t *testing.T) {
	// Given: a cache of two entries holding a and b, with a read last
	c := newPolicyCache(2)
	c.put("a", policyEntry{})
	c.put("b", policyEntry{})
	c.get("a")

	// When: a third entry is added
	c.put("c", policyEntry{})

	// Then: b, the least recently used, is gone
	if _, ok := c.get("b"); ok {
		t.Fatal("b survived")
	}
	if _, ok := c.get("a"); !ok {
		t.Fatal("a was evicted")
	}
}

func TestAuthorizer_cachesResourcePolicyByRevisionID(t *testing.T) {
	// Given: an authorizer and a policy record
	a := newAuthorizer(AuthorizerOptions{})
	rec := &policyRecord{Policy: allowInvoke("*"), RevisionID: "r1"}
	first, err := a.policyStatements("fn", rec)
	if err != nil {
		t.Fatal(err)
	}

	// When: the same revision is asked for again with a different body (a lie a
	// real store never tells), and then a new revision arrives
	again, _ := a.policyStatements("fn", &policyRecord{Policy: denyInvoke, RevisionID: "r1"})
	next, _ := a.policyStatements("fn", &policyRecord{Policy: denyInvoke, RevisionID: "r2"})

	// Then: the revision id keys the cache
	if again[0].Effect != first[0].Effect || next[0].Effect == first[0].Effect {
		t.Fatalf("effects: first %s again %s next %s", first[0].Effect, again[0].Effect, next[0].Effect)
	}
}

func TestNewAuthorizer_isPureAssignment(t *testing.T) {
	// Given / When: an authorizer is built
	a := NewAuthorizer(AuthorizerOptions{})

	// Then: no cache or other state exists until a request needs it
	if a.policies != nil {
		t.Fatal("NewAuthorizer allocated its policy cache")
	}
}

func parseTestPolicy(doc string) ([]iampolicy.Statement, error) {
	return iampolicy.ParseDocument(doc, iampolicy.SourceRef{ID: "test", Type: iampolicy.SourceTypeResourcePolicy})
}
