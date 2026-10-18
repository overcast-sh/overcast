package cloudfront

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"go.uber.org/zap"

	"github.com/overcast-sh/overcast/internal/clock"
	"github.com/overcast-sh/overcast/internal/config"
	"github.com/overcast-sh/overcast/internal/serviceutil"
	"github.com/overcast-sh/overcast/internal/state"
)

const (
	testWebDomain = "abc123.def456.lambda-web.eu-west-1.localhost"
	testDistARN   = "arn:aws:cloudfront::000000000000:distribution/EDIST1"
)

// seen is what the stub Web Function endpoint recorded.
type seen struct {
	host   string
	header http.Header
	body   string
	arn    string
	arnOK  bool
}

// newOriginFixture returns a Handler whose emulator loopback port is a stub
// lambda-web endpoint, plus a function reading what the stub last received.
// The stub stands in for the lambda-web host route, which is served by the
// emulator itself once the data plane lands.
func newOriginFixture(t *testing.T) (*Handler, func() seen) {
	t.Helper()
	var last seen
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		last = seen{host: r.Host, header: r.Header.Clone(), body: string(b)}
		last.arn, last.arnOK = serviceutil.TakeCloudFrontSource(r)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(stub.Close)
	_, portStr, _ := net.SplitHostPort(strings.TrimPrefix(stub.URL, "http://"))
	port, _ := strconv.Atoi(portStr)

	cfg := &config.Config{Hostname: "localhost", Port: port, Region: "eu-west-1", AccountID: "000000000000"}
	h := newHandler(cfg, newStore(state.NewMemoryStore(), "eu-west-1"),
		serviceutil.NewServiceLogger(zap.NewNop(), serviceName), clock.NewMock())
	return h, func() seen { return last }
}

func putOAC(t *testing.T, h *Handler, id, originType, behavior string) {
	t.Helper()
	err := h.store.PutOAC(context.Background(), &OriginAccessControl{ID: id, Version: 1,
		OriginAccessControlConfig: OriginAccessControlConfig{
			Name: id, SigningProtocol: "sigv4", SigningBehavior: behavior, OriginAccessControlOriginType: originType,
		}})
	if err != nil {
		t.Fatal(err)
	}
}

func dispatch(t *testing.T, h *Handler, method, oacID string, mut func(*http.Request), body string) {
	t.Helper()
	r := httptest.NewRequest(method, "http://localhost/x", strings.NewReader(body))
	r.Host = "localhost:" + strconv.Itoa(h.cfg.Port)
	if mut != nil {
		mut(r)
	}
	origin := &Origin{ID: "o", DomainName: testWebDomain, OriginAccessControlId: oacID}
	resp, _, err := h.dispatchToOrigins(context.Background(), r, "EDIST1", "/x", []*Origin{origin}, nil)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	resp.Body.Close()
}

func TestLambdaWebOrigin_lambdaOACSignsAndStampsIdentity(t *testing.T) {
	// Given: a distribution origin on a Web Function endpoint with a lambda OAC
	h, got := newOriginFixture(t)
	putOAC(t, h, "OAC1", "lambda", "always-amz-auth")

	// When: a viewer GET without Authorization is proxied
	dispatch(t, h, http.MethodGet, "OAC1", nil, "")

	// Then: the stub is reached with the lambda-web domain as Host
	s := got()
	if s.host != testWebDomain {
		t.Fatalf("Host = %q, want %q", s.host, testWebDomain)
	}
	// And: SigV4 for service lambda in the endpoint Region, over the identity header
	auth := s.header.Get("Authorization")
	if !strings.HasPrefix(auth, "AWS4-HMAC-SHA256 Credential=") || !strings.Contains(auth, "/eu-west-1/lambda/aws4_request") {
		t.Fatalf("Authorization = %q, want SigV4 scoped to eu-west-1/lambda", auth)
	}
	for _, signed := range []string{"host", "x-amz-content-sha256", "x-amz-date", "x-overcast-source-arn"} {
		if !strings.Contains(auth, signed) {
			t.Errorf("SignedHeaders missing %q in %q", signed, auth)
		}
	}
	if s.header.Get("X-Amz-Content-Sha256") != emptyBodySHA256 {
		t.Errorf("X-Amz-Content-Sha256 = %q", s.header.Get("X-Amz-Content-Sha256"))
	}
	if s.header.Get(cfOriginAuthHeader) != auth {
		t.Errorf("dedicated signature header = %q, want %q", s.header.Get(cfOriginAuthHeader), auth)
	}
	// And: the trusted identity names the distribution
	if !s.arnOK || s.arn != testDistARN {
		t.Errorf("source identity = (%q, %v), want (%q, true)", s.arn, s.arnOK, testDistARN)
	}
}

func TestLambdaWebOrigin_bodyRequestUsesUnsignedPayload(t *testing.T) {
	// Given: a lambda OAC origin
	h, got := newOriginFixture(t)
	putOAC(t, h, "OAC1", "lambda", "always-amz-auth")

	// When: a viewer POSTs a body
	dispatch(t, h, http.MethodPost, "OAC1", nil, "payload")

	// Then: the body arrives intact and is not covered by the signature
	s := got()
	if s.body != "payload" {
		t.Errorf("body = %q", s.body)
	}
	if s.header.Get("X-Amz-Content-Sha256") != "UNSIGNED-PAYLOAD" {
		t.Errorf("X-Amz-Content-Sha256 = %q, want UNSIGNED-PAYLOAD", s.header.Get("X-Amz-Content-Sha256"))
	}
}

func TestLambdaWebOrigin_viewerAuthorizationPassesThrough(t *testing.T) {
	// Given: a lambda OAC origin and a viewer carrying an application token
	h, got := newOriginFixture(t)
	putOAC(t, h, "OAC1", "lambda", "always-amz-auth")

	// When: the request is proxied
	dispatch(t, h, http.MethodGet, "OAC1", func(r *http.Request) { r.Header.Set("Authorization", "Bearer app-jwt") }, "")

	// Then: the viewer Authorization is preserved and the signature rides in the dedicated header
	s := got()
	if s.header.Get("Authorization") != "Bearer app-jwt" {
		t.Errorf("Authorization = %q, want the viewer's", s.header.Get("Authorization"))
	}
	if !strings.HasPrefix(s.header.Get(cfOriginAuthHeader), "AWS4-HMAC-SHA256 ") {
		t.Errorf("dedicated header = %q, want a SigV4 signature", s.header.Get(cfOriginAuthHeader))
	}
}

func TestLambdaWebOrigin_viewerAWSSignatureIsRemoved(t *testing.T) {
	// Given: a viewer whose Authorization is itself an AWS signature
	h, got := newOriginFixture(t)
	putOAC(t, h, "OAC1", "lambda", "always-amz-auth")
	viewer := "AWS4-HMAC-SHA256 Credential=AKIAVIEWER/20260101/us-east-1/s3/aws4_request, SignedHeaders=host, Signature=00"

	// When: the request is proxied
	dispatch(t, h, http.MethodGet, "OAC1", func(r *http.Request) { r.Header.Set("Authorization", viewer) }, "")

	// Then: the viewer's signature never reaches the endpoint
	s := got()
	if strings.Contains(s.header.Get("Authorization"), "AKIAVIEWER") || strings.Contains(s.header.Get(cfOriginAuthHeader), "AKIAVIEWER") {
		t.Errorf("viewer AWS signature leaked: %v", s.header)
	}
	if !strings.Contains(s.header.Get("Authorization"), "/lambda/aws4_request") {
		t.Errorf("Authorization = %q, want CloudFront's own signature", s.header.Get("Authorization"))
	}
}

func TestLambdaWebOrigin_nonLambdaOACIsNotSigned(t *testing.T) {
	// Given: the same origin with an s3-type OAC (stored, never applied)
	h, got := newOriginFixture(t)
	putOAC(t, h, "OAC1", "s3", "always")

	// When: the request is proxied
	dispatch(t, h, http.MethodGet, "OAC1", nil, "")

	// Then: the origin sees no signature and no CloudFront identity
	s := got()
	if s.host != testWebDomain {
		t.Errorf("Host = %q", s.host)
	}
	for _, name := range []string{"Authorization", cfOriginAuthHeader, "X-Amz-Date", serviceutil.CloudFrontSourceARNHeader, serviceutil.InternalTrustHeader} {
		if v := s.header.Get(name); v != "" {
			t.Errorf("%s = %q, want absent", name, v)
		}
	}
	if s.arnOK {
		t.Error("identity trusted without an OAC")
	}
}

func TestLambdaWebOrigin_noOACIsNotSigned(t *testing.T) {
	// Given: a Web Function origin with no OAC (an ApplicationManaged endpoint)
	h, got := newOriginFixture(t)

	// When: a viewer with a token is proxied
	dispatch(t, h, http.MethodGet, "", func(r *http.Request) { r.Header.Set("Authorization", "Bearer t") }, "")

	// Then: the request is plain: viewer Authorization only, no identity
	s := got()
	if s.header.Get("Authorization") != "Bearer t" || s.header.Get(cfOriginAuthHeader) != "" || s.arnOK {
		t.Errorf("unexpected signing: %v arnOK=%v", s.header, s.arnOK)
	}
}

func TestLambdaWebOrigin_spoofedIdentityFromViewerIsIgnored(t *testing.T) {
	// Given: an unsigned origin and a viewer that sends the identity headers itself
	h, got := newOriginFixture(t)
	putOAC(t, h, "OAC1", "s3", "always")

	// When: the request is proxied
	dispatch(t, h, http.MethodGet, "OAC1", func(r *http.Request) {
		r.Header.Set(serviceutil.CloudFrontSourceARNHeader, testDistARN)
		r.Header.Set(serviceutil.InternalTrustHeader, "guess")
	}, "")

	// Then: the headers are not forwarded, so no identity is trusted
	if s := got(); s.arnOK || s.header.Get(serviceutil.CloudFrontSourceARNHeader) != "" {
		t.Errorf("viewer-supplied identity forwarded: %v", s.header)
	}
}

func TestValidateOACConfig(t *testing.T) {
	cases := []struct {
		name                        string
		protocol, behavior, oriType string
		wantErr                     bool
	}{
		{"s3 always", "sigv4", "always", "s3", false},
		{"lambda always-amz-auth", "sigv4", "always-amz-auth", "lambda", false},
		{"lambda global sigv4a", "sigv4a", "always-amz-auth", "lambda", false},
		{"lambda function url keeps always", "sigv4", "always", "lambda", false},
		{"always-amz-auth on s3 rejected", "sigv4", "always-amz-auth", "s3", true},
		{"unknown origin type", "sigv4", "always", "ec2", true},
		{"unknown protocol", "sigv9", "always", "s3", true},
		{"unknown behavior", "sigv4", "sometimes", "s3", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Given: a config / When: validated / Then: accepted or InvalidArgument
			err := validateOACConfig(&OriginAccessControlConfig{
				Name: "n", SigningProtocol: tc.protocol, SigningBehavior: tc.behavior, OriginAccessControlOriginType: tc.oriType,
			})
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if err != nil && err.Code != "InvalidArgument" {
				t.Errorf("code = %q", err.Code)
			}
		})
	}
}

func TestLambdaWebRegion(t *testing.T) {
	cases := map[string]struct {
		region string
		ok     bool
	}{
		"a.b.lambda-web.us-east-1.on.aws":                 {"us-east-1", true},
		"A.B.Lambda-Web.global.on.aws":                    {"global", true},
		"abc.def.lambda-web.eu-west-1.localhost:4566":     {"eu-west-1", true},
		"abc.lambda-url.us-east-1.on.aws":                 {"", false},
		"lambda-web.us-east-1.example.com":                {"", false},
		"bucket.s3.amazonaws.com":                         {"", false},
		"x.lambda-web":                                    {"", false},
		"id.shard.lambda-web.ap-southeast-2.example.test": {"ap-southeast-2", true},
	}
	for domain, want := range cases {
		if region, ok := lambdaWebRegion(domain); region != want.region || ok != want.ok {
			t.Errorf("lambdaWebRegion(%q) = (%q, %v), want (%q, %v)", domain, region, ok, want.region, want.ok)
		}
	}
}

func TestValidateOriginAccessControls_lambdaOACNeedsWebFunctionDomain(t *testing.T) {
	// Given: a lambda OAC
	h, _ := newOriginFixture(t)
	putOAC(t, h, "OACL", "lambda", "always-amz-auth")
	cfg := func(domain string) *DistributionConfig {
		c := &DistributionConfig{}
		c.Origins.Items = []Origin{{ID: "o", DomainName: domain, OriginAccessControlId: "OACL"}}
		return c
	}

	// When / Then: a Web Function domain is accepted
	if err := h.validateOriginAccessControls(context.Background(), cfg(testWebDomain)); err != nil {
		t.Errorf("web function domain rejected: %v", err)
	}
	// And: any other domain is InvalidDomainNameForOriginAccessControl
	err := h.validateOriginAccessControls(context.Background(), cfg("bucket.s3.amazonaws.com"))
	if err == nil || err.Code != "InvalidDomainNameForOriginAccessControl" {
		t.Errorf("err = %v, want InvalidDomainNameForOriginAccessControl", err)
	}
}
