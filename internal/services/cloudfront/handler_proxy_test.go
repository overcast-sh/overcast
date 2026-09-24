package cloudfront

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
)

// TestMatchPathPattern_followsAWSSemantics is the specification for cache
// behavior path patterns, taken from the CloudFront documentation for
// CacheBehavior.PathPattern:
//
//   - "*" matches 0 or more characters, and it matches across "/" — a pattern
//     is matched against the whole path, not segment by segment.
//   - "?" matches exactly 1 character.
//   - The leading "/" is OPTIONAL: "CloudFront behavior is the same with or
//     without the leading /". "images/*.jpg" and "/images/*.jpg" are one
//     pattern.
//
// The original matcher special-cased a trailing "*" and otherwise fell back to
// path.Match, whose "*" stops at "/". Two consequences, both of which route a
// request to the wrong origin rather than failing loudly:
//
//   - A pattern without a leading slash could never match anything, because the
//     request path always has one. A behavior written "jobs*" or "api/*" was
//     dead, and its requests silently fell through to the DEFAULT behavior.
//   - "*.jpg", the documentation's own example, matched nothing outside the
//     root, and a wildcard in the middle ("/api/*/detail") never matched.
func TestMatchPathPattern_followsAWSSemantics(t *testing.T) {
	cases := []struct {
		pattern string
		path    string
		want    bool
		why     string
	}{
		// ---- Everything ----------------------------------------------------
		{pattern: "*", path: "/anything/at/all", want: true},
		{pattern: "/*", path: "/anything/at/all", want: true},

		// ---- The leading slash is optional ---------------------------------
		{pattern: "jobs", path: "/jobs", want: true, why: "AWS: same behavior with or without the leading /"},
		{pattern: "/jobs", path: "/jobs", want: true},
		{pattern: "jobs*", path: "/jobs", want: true},
		{pattern: "jobs*", path: "/jobs/123", want: true},
		{pattern: "api/*", path: "/api/v1/things", want: true},
		{pattern: "/api/*", path: "/api/v1/things", want: true},
		{pattern: "images/*.jpg", path: "/images/cat.jpg", want: true},

		// ---- "*" matches across "/" ----------------------------------------
		{pattern: "*.jpg", path: "/images/deep/cat.jpg", want: true, why: "AWS's own example; path.Match's * stops at /"},
		{pattern: "/api/*/detail", path: "/api/v1/things/detail", want: true, why: "wildcard in the middle spans segments"},
		{pattern: "/assets/*", path: "/assets/js/app.min.js", want: true},

		// ---- "?" matches exactly one character -----------------------------
		{pattern: "/file?.txt", path: "/file1.txt", want: true},
		{pattern: "/file?.txt", path: "/file12.txt", want: false},
		{pattern: "/file?.txt", path: "/file.txt", want: false},

		// ---- Non-matches ----------------------------------------------------
		{pattern: "/api/*", path: "/jobs", want: false},
		{pattern: "jobs", path: "/jobs/123", want: false, why: "no wildcard: exact match only"},
		{pattern: "/images/*.jpg", path: "/images/cat.png", want: false},
		{pattern: "/jobs*", path: "/other", want: false},

		// ---- Literal regex metacharacters are literal ----------------------
		// A pattern is a glob, not a regexp: "." and "+" match themselves.
		{pattern: "/a.b", path: "/a.b", want: true},
		{pattern: "/a.b", path: "/axb", want: false, why: "'.' is a literal dot, not 'any character'"},
		{pattern: "/v1+/x", path: "/v1+/x", want: true},
	}

	for _, tc := range cases {
		name := tc.pattern + " vs " + tc.path
		t.Run(name, func(t *testing.T) {
			if got := matchPathPattern(tc.pattern, tc.path); got != tc.want {
				t.Errorf("matchPathPattern(%q, %q) = %v, want %v%s",
					tc.pattern, tc.path, got, tc.want, func() string {
						if tc.why != "" {
							return " — " + tc.why
						}
						return ""
					}())
			}
		})
	}
}

// TestViewerPath_isAlwaysTheEncodedForm: chi hands the proxy the wildcard
// encoded when Go set RawPath and decoded when it did not, so the proxy has to
// restore the viewer's encoding itself.
func TestViewerPath_isAlwaysTheEncodedForm(t *testing.T) {
	for _, target := range []string{
		"/100%25", "/a%20b", "/caf%C3%A9", "/caf%c3%a9", "/a%2Fb%25c", "/%7Euser", "/plain/path", "/",
		"//a/b", "//", "/a//b", "///a%2Fb", // repeated slashes are the viewer's too
	} {
		t.Run(target, func(t *testing.T) {
			// Given: a request routed the way the router would route it
			r := httptest.NewRequest(http.MethodGet, "/_overcast/cloudfront/distributions/EXAMPLE"+target, nil)
			rt := chi.NewRouter()
			var got string
			rt.Get("/_overcast/cloudfront/distributions/{distId}/*", func(_ http.ResponseWriter, r *http.Request) {
				got = viewerPath(r)
			})

			// When: the proxy reads its downstream path
			rt.ServeHTTP(httptest.NewRecorder(), r)

			// Then: it is exactly what the viewer sent
			if got != target {
				t.Errorf("viewerPath = %q, want %q", got, target)
			}
		})
	}
}

// TestNormalizePathForMatch_decodesOnlyUnreserved pins the RFC 3986 §6.2.2.2
// normalisation CloudFront applies before matching path patterns.
func TestNormalizePathForMatch_decodesOnlyUnreserved(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"/plain/path", "/plain/path"},
		{"/%7Euser", "/~user"},
		{"/%7euser", "/~user"},
		{"/%41%62%2D%2E%5F%30", "/Ab-._0"},
		{"/a%2Fb", "/a%2Fb"},   // reserved: stays encoded
		{"/a%40b", "/a%40b"},   // reserved: stays encoded
		{"/100%25", "/100%25"}, // "%" itself is not unreserved
		{"/caf%C3%A9", "/caf%C3%A9"},
		{"/bad%zz", "/bad%zz"}, // not an escape: left alone
		{"/trailing%7", "/trailing%7"},
		{"/end%", "/end%"},
		// Dot segments and repeated slashes (RFC 3986 section 5.2.4, and
		// AWS's "multiple slashes (//) or periods (..)").
		{"/a/b/..", "/a/"},
		{"/a/b/../", "/a/"},
		{"/a/b/.", "/a/b/"},
		{"/a/./b", "/a/b"},
		{"/a/b/../c", "/a/c"},
		{"/a/b/../../c", "/c"},
		{"/../a", "/a"},
		{"/..", "/"},
		{"/.", "/"},
		{"/", "/"},
		{"/a//b", "/a/b"},
		{"//a///b//", "/a/b/"},
		{"/a/b/%2E%2E", "/a/"},     // decoded to "..", then resolved
		{"/a/%2e/b", "/a/b"},       // decoded to ".", then resolved
		{"/a/..%2Fb", "/a/..%2Fb"}, // "%2F" is not a separator
		{"/a/.../b", "/a/.../b"},   // "..." is an ordinary segment
		{"/a/.b/..c", "/a/.b/..c"},
	} {
		if got := normalizePathForMatch(tc.in); got != tc.want {
			t.Errorf("normalizePathForMatch(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestEscapeURIPath_encodesOnlyWhatAPathCannotHold: a path already in encoded
// form is untouched, so a viewer's URI is never re-encoded, while a raw uri
// from a function becomes one an HTTP request line can carry.
func TestEscapeURIPath_encodesOnlyWhatAPathCannotHold(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"/plain/path-1_2.3~4", "/plain/path-1_2.3~4"},
		{"/sub:delims@!$&'()*+,;=", "/sub:delims@!$&'()*+,;="},
		{"/100%25", "/100%25"},
		{"/caf%c3%a9", "/caf%c3%a9"}, // valid escapes keep their case
		{"/café", "/caf%C3%A9"},
		{"/a b", "/a%20b"},
		{"/100%", "/100%25"},
		{"/50%-off", "/50%25-off"},
		{"/a?b#c", "/a%3Fb%23c"},
		{"/q\"<>\\^`{|}", "/q%22%3C%3E%5C%5E%60%7B%7C%7D"},
		{"/ctl\x00\x7f", "/ctl%00%7F"},
	} {
		if got := escapeURIPath(tc.in); got != tc.want {
			t.Errorf("escapeURIPath(%q) = %q, want %q", tc.in, got, tc.want)
		}
		if got := escapeURIPath(tc.want); got != tc.want {
			t.Errorf("escapeURIPath(%q) = %q, want it unchanged: output must be a fixed point", tc.want, got)
		}
	}
}

// TestPathHelpers_doNotAllocateOnPlainPaths: both helpers run on every proxied
// request, and the common path has nothing to decode or encode.
func TestPathHelpers_doNotAllocateOnPlainPaths(t *testing.T) {
	const p = "/assets/js/app.min.js"
	if n := testing.AllocsPerRun(100, func() { _ = normalizePathForMatch(p) }); n != 0 {
		t.Errorf("normalizePathForMatch allocated %v times, want 0", n)
	}
	if n := testing.AllocsPerRun(100, func() { _ = escapeURIPath(p) }); n != 0 {
		t.Errorf("escapeURIPath allocated %v times, want 0", n)
	}
	if n := testing.AllocsPerRun(100, func() { _ = logFieldEscape(p) }); n != 0 {
		t.Errorf("logFieldEscape allocated %v times, want 0", n)
	}
}

// TestLogFieldEscape_encodesTheDocumentedCharacters: standard log field values
// URL-encode ASCII 0-32, 127 and above, and the characters in the table in
// standard-logging-legacy-s3 ("Standard log file format"), "%" among them.
func TestLogFieldEscape_encodesTheDocumentedCharacters(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"/images/cat.jpg", "/images/cat.jpg"},
		{"/sub:delims@!$&()*+,;=/-._", "/sub:delims@!$&()*+,;=/-._"},
		{"/a%20b", "/a%2520b"},
		{"/~user", "/%7Euser"},
		{"a b", "a%20b"},
		{`<>"#%{}|\^~[]` + "`'", "%3C%3E%22%23%25%7B%7D%7C%5C%5E%7E%5B%5D%60%27"},
		{"\x00\t\x1f\x7f", "%00%09%1F%7F"},
		{"café", "caf%C3%A9"},
	} {
		if got := logFieldEscape(tc.in); got != tc.want {
			t.Errorf("logFieldEscape(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
