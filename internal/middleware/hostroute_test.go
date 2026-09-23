package middleware

import (
	"net/http/httptest"
	"regexp"
	"testing"
)

func TestPrefixPath(t *testing.T) {
	// Given: request targets with and without encoding Go keeps in RawPath
	tests := []struct {
		name, target, wantPath, wantEscaped string
	}{
		{"plain", "/prod/hello", "/p/prod/hello", "/p/prod/hello"},
		{"encoded slash kept", "/prod/@scope%2fpkg", "/p/prod/@scope/pkg", "/p/prod/@scope%2fpkg"},
		{"encoded slash beside a space", "/a%2Fb%20c", "/p/a/b c", "/p/a%2Fb%20c"},
		{"root", "/", "/p/", "/p/"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "http://example.test"+tt.target, nil)

			// When: the path is prefixed
			PrefixPath(r, "/p")

			// Then: Path is decoded and the wire form survives in EscapedPath
			if r.URL.Path != tt.wantPath {
				t.Errorf("Path = %q, want %q", r.URL.Path, tt.wantPath)
			}
			if got := r.URL.EscapedPath(); got != tt.wantEscaped {
				t.Errorf("EscapedPath() = %q, want %q", got, tt.wantEscaped)
			}
		})
	}
}

func TestPrefixPath_emptyPathGetsRootSlash(t *testing.T) {
	// Given: an absolute-form request line with no path leaves Path empty
	r := httptest.NewRequest("GET", "http://example.test/", nil)
	r.URL.Path = ""

	// When: the path is prefixed
	PrefixPath(r, "/p")

	// Then: it lands on the prefix's root, which the internal /* routes match
	if r.URL.Path != "/p/" {
		t.Errorf("Path = %q, want %q", r.URL.Path, "/p/")
	}
}

func TestParseHostRoute(t *testing.T) {
	// Given: a table of Host headers covering the grammar's edge cases
	tests := []struct {
		name       string
		host       string
		wantOK     bool
		wantLabel  string
		wantID     string
		wantRegion string
	}{
		{name: "execute-api with region", host: "abc123.execute-api.us-east-1.amazonaws.com", wantOK: true, wantLabel: "execute-api", wantID: "abc123", wantRegion: "us-east-1"},
		{name: "execute-api with port", host: "abc123.execute-api.us-east-1.amazonaws.com:4566", wantOK: true, wantLabel: "execute-api", wantID: "abc123", wantRegion: "us-east-1"},
		{name: "execute-api no region (bare localhost base)", host: "abc123.execute-api.localhost", wantOK: true, wantLabel: "execute-api", wantID: "abc123", wantRegion: ""},
		{name: "execute-api gov region", host: "abc123.execute-api.us-gov-west-1.amazonaws.com", wantOK: true, wantLabel: "execute-api", wantID: "abc123", wantRegion: "us-gov-west-1"},
		{name: "lambda-url", host: "deadbeefdeadbeefdeadbeefdeadbeef.lambda-url.us-east-1.on.aws", wantOK: true, wantLabel: "lambda-url", wantID: "deadbeefdeadbeefdeadbeefdeadbeef", wantRegion: "us-east-1"},
		{name: "appsync-api", host: "myapi456.appsync-api.ap-southeast-2.amazonaws.com", wantOK: true, wantLabel: "appsync-api", wantID: "myapi456", wantRegion: "ap-southeast-2"},
		{name: "dotted id before label", host: "foo.bar.execute-api.us-east-1.amazonaws.com", wantOK: true, wantLabel: "execute-api", wantID: "foo.bar", wantRegion: "us-east-1"},
		{name: "label with no id segment does not match", host: "execute-api.us-east-1.amazonaws.com", wantOK: false},
		{name: "unrecognised label", host: "abc123.made-up-service.us-east-1.amazonaws.com", wantOK: false},
		{name: "plain path-style host", host: "localhost:4566", wantOK: false},
		{name: "IP literal bypasses grammar", host: "127.0.0.1:4566", wantOK: false},
		{name: "IPv6 literal bypasses grammar", host: "[::1]:4566", wantOK: false},
		{name: "empty host", host: "", wantOK: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// When: the host is parsed
			m, ok := ParseHostRoute(tt.host)

			// Then: match result and fields are as expected
			if ok != tt.wantOK {
				t.Fatalf("ParseHostRoute(%q) ok = %v, want %v", tt.host, ok, tt.wantOK)
			}
			if !tt.wantOK {
				return
			}
			if m.Label != tt.wantLabel {
				t.Errorf("Label = %q, want %q", m.Label, tt.wantLabel)
			}
			if m.ID != tt.wantID {
				t.Errorf("ID = %q, want %q", m.ID, tt.wantID)
			}
			if m.Region != tt.wantRegion {
				t.Errorf("Region = %q, want %q", m.Region, tt.wantRegion)
			}
		})
	}
}

func TestHostRouteServiceFor(t *testing.T) {
	// Given: hosts that do and don't match the host-route grammar
	cases := []struct {
		host    string
		wantSvc string
		wantOK  bool
	}{
		{host: "abc123.execute-api.us-east-1.amazonaws.com", wantSvc: "apigateway", wantOK: true},
		{host: "xyz.lambda-url.us-east-1.on.aws", wantSvc: "lambda", wantOK: true},
		{host: "api1.appsync-api.us-east-1.amazonaws.com", wantSvc: "appsync", wantOK: true},
		{host: "mybucket.s3.us-east-1.amazonaws.com", wantOK: false}, // S3 is not a host-route label
		{host: "localhost:4566", wantOK: false},
	}

	for _, tc := range cases {
		t.Run(tc.host, func(t *testing.T) {
			// When: the grammar is parsed and its owner looked up
			m, matched := ParseHostRoute(tc.host)
			svc, ok := "", false
			if matched {
				svc, ok = HostRouteServiceFor(m)
			}

			// Then: it matches ParseHostRoute's positive/negative verdict
			if ok != tc.wantOK {
				t.Fatalf("HostRouteServiceFor(%q) ok = %v, want %v", tc.host, ok, tc.wantOK)
			}
			if ok && svc != tc.wantSvc {
				t.Errorf("HostRouteServiceFor(%q) = %q, want %q", tc.host, svc, tc.wantSvc)
			}
		})
	}
}

// The middleware that applies HostRouteRows is HostAddressing; its dispatch
// and pass-through behaviour is specified in hostaddressing_test.go, where it
// is tested together with S3 virtual-hosted addressing because the two share
// one precedence decision.

// TestIsAWSRegionShaped_matchesRegexpOracle pins the hand-rolled matcher to
// the regexp it replaced — `^[a-z]{2}(-gov|-iso[a-z]*)?-[a-z]+-\d$` — so the
// two can never drift. isAWSRegionShaped documents why it is hand-rolled (the
// per-request path is specified allocation-free; regexp matching is only
// amortized allocation-free).
func TestIsAWSRegionShaped_matchesRegexpOracle(t *testing.T) {
	oracle := regexp.MustCompile(`^[a-z]{2}(-gov|-iso[a-z]*)?-[a-z]+-\d$`)

	// Given: explicit shapes for every branch — real regions, the gov/iso
	// group, and near-misses that need the alternation resolved correctly
	// ("aa-gov-1" matches only via the group-less branch; "us-gove-bb-1"
	// matches via neither).
	corpus := []string{
		"us-east-1", "ap-southeast-2", "eu-west-3", "us-gov-west-1",
		"us-iso-east-1", "us-isob-east-1", "us-isof-south-1",
		"aa-gov-1", "aa-iso-1", "us-gove-1", "us-gove-bb-1", "us-govx-1",
		"us-east-12", "us-east-", "us-east", "us", "", "-us-east-1",
		"us-east-1-", "us--east-1", "us-east--1", "u-east-1", "usa-east-1",
		"US-east-1", "us-East-1", "us-east-A", "us.east-1", "us-ea5t-1",
		"aa-bb-cc-dd-1", "gov-gov-gov-1", "iso-iso-iso-1",
	}

	// And: a structural sweep — every 1..4-segment dash-join of parts chosen
	// to hit each alternative's segment-count and character-class edges. Four
	// segments is the longest join the pattern can accept, and the sweep
	// includes empty parts, so longer/degenerate shapes are covered too.
	parts := []string{"", "a", "aa", "aaa", "A", "gov", "gove", "iso", "isox", "isoX", "east", "1", "9", "12", "x1"}
	var sweep func(prefix string, depth int)
	sweep = func(prefix string, depth int) {
		corpus = append(corpus, prefix)
		if depth == 4 {
			return
		}
		for _, p := range parts {
			sweep(prefix+"-"+p, depth+1)
		}
	}
	for _, p := range parts {
		sweep(p, 1)
	}

	// When/Then: the hand-rolled matcher agrees with the oracle on all of it.
	for _, s := range corpus {
		if got, want := isAWSRegionShaped(s), oracle.MatchString(s); got != want {
			t.Errorf("isAWSRegionShaped(%q) = %v, oracle says %v", s, got, want)
		}
	}
}
