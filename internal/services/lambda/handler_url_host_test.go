package lambda

import (
	"net/http/httptest"
	"testing"

	"github.com/overcast-sh/overcast/internal/middleware"
)

func TestHostRouteRewrite_keepsEncodedSlashForRawPath(t *testing.T) {
	// Given: a function URL request whose path carries an encoded slash.
	req := httptest.NewRequest("GET", "http://abc.lambda-url.us-east-1.localhost/files/a%2Fb", nil)

	// When: the host rewrite moves it onto the internal url-invoke route.
	(&Service{}).HostRouteRewrite(req, middleware.HostRouteMatch{ID: "abc"})

	// Then: the encoded form survives, so the event's rawPath is the raw path
	// as payload format 2.0 defines it.
	if want := "/_overcast/lambda/url-invoke/abc/files/a%2Fb"; req.URL.EscapedPath() != want {
		t.Errorf("EscapedPath() = %q, want %q", req.URL.EscapedPath(), want)
	}
}
