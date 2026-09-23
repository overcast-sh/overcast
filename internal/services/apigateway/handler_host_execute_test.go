package apigateway

import (
	"net/http/httptest"
	"testing"

	"github.com/overcast-sh/overcast/internal/middleware"
)

func TestHostRouteRewrite_preservesEncodedSlash(t *testing.T) {
	// Given: a host-style invoke whose single path segment carries an encoded
	// slash, as npm sends a scoped package name (@scope/pkg -> @scope%2fpkg).
	req := httptest.NewRequest("PUT", "http://abc123.execute-api.eu-west-1.localhost/prod/@scope%2fpkg", nil)
	s := &Service{}

	// When: the host rewrite moves it onto the internal execute-api route.
	s.HostRouteRewrite(req, middleware.HostRouteMatch{ID: "abc123", Region: "eu-west-1"})

	// Then: the encoded form survives byte-for-byte, so the router and the
	// resource matcher still see one segment, as AWS does.
	const wantEscaped = "/_overcast/apigateway/execute-api/abc123/eu-west-1/prod/@scope%2fpkg"
	if got := req.URL.EscapedPath(); got != wantEscaped {
		t.Errorf("EscapedPath() = %q, want %q", got, wantEscaped)
	}
	const wantPath = "/_overcast/apigateway/execute-api/abc123/eu-west-1/prod/@scope/pkg"
	if req.URL.Path != wantPath {
		t.Errorf("Path = %q, want %q", req.URL.Path, wantPath)
	}
}

func TestHostRouteRewrite_plainPathLeavesRawPathEmpty(t *testing.T) {
	// Given: a host-style invoke with nothing percent-encoded.
	req := httptest.NewRequest("GET", "http://abc123.execute-api.localhost/prod/hello", nil)
	s := &Service{}

	// When: the host rewrite runs with no region in the Host.
	s.HostRouteRewrite(req, middleware.HostRouteMatch{ID: "abc123"})

	// Then: the region placeholder is used and RawPath stays unset.
	if want := "/_overcast/apigateway/execute-api/abc123/-/prod/hello"; req.URL.Path != want {
		t.Errorf("Path = %q, want %q", req.URL.Path, want)
	}
	if req.URL.RawPath != "" {
		t.Errorf("RawPath = %q, want empty", req.URL.RawPath)
	}
}
