package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// hostaddressing_test.go — the precedence contract between S3 virtual-hosted
// addressing and host-routed AWS services. See
// docs/plans/host-routing-precedence.md for the rule and the evidence.

// claimString renders a claim compactly so table failures are readable.
func claimString(c HostClaim) string {
	switch c.Kind {
	case HostClaimS3:
		return "s3:" + c.Bucket
	case HostClaimHostRoute:
		return "hostroute:" + c.Route.Label + " id=" + c.Route.ID + " region=" + c.Route.Region
	case HostClaimNone:
		return "none"
	}
	return "none"
}

// TestHostClassifier_precedenceMatrix is the specification for §4 of the plan:
// tier A (.s3. labelled) beats tier B (bare, exact-remainder base) beats
// tier C (registered host-route label), and anything unclaimed falls through
// to S3 path-style.
func TestHostClassifier_precedenceMatrix(t *testing.T) {
	// Given: hosts spanning every tier, both collision directions, and the
	// bases Overcast resolves on
	cases := []struct {
		name       string
		host       string
		configured string
		want       string
	}{
		// ---- Tier B: bare virtual-hosted, exact remainder match -----------
		{name: "bare bucket on localhost", host: "mybucket.localhost:4566", want: "s3:mybucket"},
		{name: "bare bucket on overcast.sh wildcard", host: "mybucket.localhost.overcast.sh:4566", want: "s3:mybucket"},
		{name: "bare bucket on localstack wildcard", host: "mybucket.localhost.localstack.cloud", want: "s3:mybucket"},
		{name: "CDK asset publisher bucket", host: "cdk-hnb659fds-assets-000000000000-ap-southeast-2.localhost.localstack.cloud:4566", want: "s3:cdk-hnb659fds-assets-000000000000-ap-southeast-2"},
		{name: "bare bucket on configured hostname", host: "mybucket.dev.example.test", configured: "dev.example.test", want: "s3:mybucket"},

		// ---- Tier A: labelled virtual-hosted ------------------------------
		{name: "labelled bucket", host: "mybucket.s3.localhost", want: "s3:mybucket"},
		{name: "labelled bucket with region", host: "mybucket.s3.us-east-1.localhost", want: "s3:mybucket"},
		{name: "labelled bucket on real AWS host", host: "mybucket.s3.us-west-2.amazonaws.com", want: "s3:mybucket"},
		{name: "legacy dash dialect", host: "legacy-bucket.s3-us-west-2.localhost", want: "s3:legacy-bucket"},
		{name: "dotted bucket labelled", host: "my.dotted.bucket.s3.localhost", want: "s3:my.dotted.bucket"},

		// ---- Tier C: host-routed services ---------------------------------
		{name: "apigw invoke on wildcard base with region", host: "abc123.execute-api.us-east-1.localhost.overcast.sh:4566", want: "hostroute:execute-api id=abc123 region=us-east-1"},
		{name: "apigw invoke on wildcard base no region", host: "abc123.execute-api.localhost.overcast.sh:4566", want: "hostroute:execute-api id=abc123 region="},
		{name: "apigw invoke on localhost with region", host: "abc123.execute-api.us-east-1.localhost:4566", want: "hostroute:execute-api id=abc123 region=us-east-1"},
		{name: "apigw invoke on localhost no region", host: "abc123.execute-api.localhost:4566", want: "hostroute:execute-api id=abc123 region="},
		{name: "apigw invoke on localstack base", host: "abc123.execute-api.us-east-1.localhost.localstack.cloud:4566", want: "hostroute:execute-api id=abc123 region=us-east-1"},
		{name: "apigw invoke on real AWS host", host: "abc123.execute-api.us-east-1.amazonaws.com", want: "hostroute:execute-api id=abc123 region=us-east-1"},
		{name: "lambda function url", host: "urlid.lambda-url.us-east-1.localhost.overcast.sh:4566", want: "hostroute:lambda-url id=urlid region=us-east-1"},
		{name: "appsync graphql", host: "myapi.appsync-api.us-east-1.localhost:4566", want: "hostroute:appsync-api id=myapi region=us-east-1"},
		{name: "dotted id before label", host: "foo.bar.execute-api.us-east-1.amazonaws.com", want: "hostroute:execute-api id=foo.bar region=us-east-1"},

		// ---- Precedence: A beats C ----------------------------------------
		// A dotted bucket whose last segment is a reserved label, addressed
		// the explicit way, is S3 — not API Gateway.
		{name: "reserved-suffix bucket labelled beats host route", host: "my.execute-api.s3.localhost", want: "s3:my.execute-api"},
		{name: "reserved-suffix bucket labelled on real AWS host", host: "my.execute-api.s3.us-east-1.amazonaws.com", want: "s3:my.execute-api"},

		// ---- Precedence: B beats C ----------------------------------------
		// An operator-configured base is stronger evidence than a generic
		// label match inside it.
		{name: "configured base containing a label", host: "mybucket.execute-api.mycorp.com", configured: "execute-api.mycorp.com", want: "s3:mybucket"},
		{name: "genuine invoke on that same configured base", host: "abc.execute-api.us-east-1.execute-api.mycorp.com", configured: "execute-api.mycorp.com", want: "hostroute:execute-api id=abc region=us-east-1"},

		// ---- Reserved labels at index 0 are NOT host routes ----------------
		// {id} must be non-empty in every real AWS host-routed shape, so a
		// bucket named exactly like a service is safe.
		{name: "bucket named execute-api", host: "execute-api.localhost:4566", want: "s3:execute-api"},
		{name: "bucket named execute-api on wildcard base", host: "execute-api.localhost.overcast.sh:4566", want: "s3:execute-api"},
		{name: "bucket named execute-api labelled", host: "execute-api.s3.localhost", want: "s3:execute-api"},
		{name: "bucket named lambda-url", host: "lambda-url.localhost:4566", want: "s3:lambda-url"},
		{name: "reserved label at index 0 with extra segment", host: "execute-api.thing.localhost", want: "s3:execute-api.thing"},

		// ---- Dotted bucket names stay addressable in the bare form ---------
		// The reserved-label veto, not a one-label restriction, is what keeps
		// tier B unambiguous — so a dotted name with no reserved segment is a
		// perfectly ordinary bucket.
		{name: "dotted bucket bare", host: "my.dotted.bucket.localhost", want: "s3:my.dotted.bucket"},
		{name: "dotted bucket bare on wildcard base", host: "my.dotted.bucket.localhost.overcast.sh", want: "s3:my.dotted.bucket"},
		{name: "dotted bucket bare on localstack base", host: "my.dotted.bucket.localhost.localstack.cloud:4566", want: "s3:my.dotted.bucket"},
		{name: "longer default base wins over configured parent", host: "mybucket.localhost.overcast.sh", configured: "overcast.sh", want: "s3:mybucket"},

		// ---- The documented limitation ------------------------------------
		// Dotted bucket with a reserved label at index >= 1, bare form: the
		// service wins. Escapes are path-style and the .s3. form above.
		{name: "dotted reserved-suffix bucket bare goes to service", host: "my.execute-api.localhost", want: "hostroute:execute-api id=my region="},
		{name: "reserved label mid-name bare goes to service", host: "my.execute-api.thing.localhost", want: "hostroute:execute-api id=my region="},

		// ---- Not buckets, not routes ---------------------------------------
		{name: "bare s3 service endpoint", host: "s3.localhost", want: "none"},
		{name: "s3 service endpoint with region", host: "s3.us-east-1.localhost", want: "none"},
		{name: "s3 service endpoint on wildcard base", host: "s3.localhost.overcast.sh", want: "none"},
		{name: "base domain itself", host: "localhost.overcast.sh:4566", want: "none"},
		{name: "plain path-style host", host: "localhost:4566", want: "none"},
		{name: "unrecognised base", host: "weird.s3x.host", want: "none"},
		{name: "unregistered label", host: "abc123.not-a-real-service.us-east-1.amazonaws.com", want: "none"},
		{name: "IPv4 literal", host: "127.0.0.1:4566", want: "none"},
		{name: "IPv6 literal", host: "[::1]:4566", want: "none"},
		{name: "empty host", host: "", want: "none"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// When: the host is classified
			got := NewHostClassifier(tc.configured).Classify(tc.host)

			// Then: exactly one tier claims it, with the expected fields
			if s := claimString(got); s != tc.want {
				t.Errorf("Classify(%q) with OVERCAST_HOSTNAME=%q\n got: %s\nwant: %s",
					tc.host, tc.configured, s, tc.want)
			}
		})
	}
}

// TestHostClassifier_claimsAreMutuallyExclusive is the regression guard for the
// original bug: S3 and the host-route table both claimed the same request,
// each rewriting the path the other had already rewritten.
func TestHostClassifier_claimsAreMutuallyExclusive(t *testing.T) {
	// Given: the hosts that previously produced a double claim
	hosts := []string{
		"abc123.execute-api.us-east-1.localhost.overcast.sh:4566",
		"abc123.execute-api.localhost:4566",
		"urlid.lambda-url.us-east-1.localhost.overcast.sh:4566",
		"myapi.appsync-api.us-east-1.localhost:4566",
		"my.execute-api.s3.localhost",
	}

	for _, host := range hosts {
		t.Run(host, func(t *testing.T) {
			// When: the host is classified
			claim := NewHostClassifier("").Classify(host)

			// Then: it carries exactly one kind — never both a bucket and a route
			if claim.Kind == HostClaimS3 && claim.Route.Label != "" {
				t.Errorf("Classify(%q) claimed S3 bucket %q AND host route %q",
					host, claim.Bucket, claim.Route.Label)
			}
			if claim.Kind == HostClaimHostRoute && claim.Bucket != "" {
				t.Errorf("Classify(%q) claimed host route %q AND S3 bucket %q",
					host, claim.Route.Label, claim.Bucket)
			}
			if claim.Kind == HostClaimNone {
				t.Errorf("Classify(%q) claimed nothing; expected a single owner", host)
			}
		})
	}
}

// TestHostClassifier_hostIsCaseInsensitive is the specification for DNS's own
// rule: a hostname is case-insensitive (RFC 4343; RFC 3986 §3.2.2 for the URI
// authority), so the SAME address in any case must reach the same resource.
//
// This is not hypothetical robustness. Browsers lowercase the Host before
// sending it, so every one of these forms is what a user actually gets by
// pasting an address Overcast minted into the address bar. Only the base was
// folded (strings.EqualFold in Classify) — the S3 label, the bucket, the
// service label and the region were all matched case-sensitively.
func TestHostClassifier_hostIsCaseInsensitive(t *testing.T) {
	// Given: every tier's address, in the case a client might actually send
	cases := []struct {
		name       string
		host       string
		configured string
		want       string
	}{
		// ---- Tier A / B: S3. Bucket names are lowercase-only by AWS rule, so
		// an upper-case Host segment can only mean a case-folded name.
		{name: "bare bucket, mixed case", host: "MyBucket.localhost:4566", want: "s3:mybucket"},
		{name: "bare bucket, upper base", host: "mybucket.LOCALHOST.OVERCAST.SH:4566", want: "s3:mybucket"},
		{name: "labelled bucket, upper s3 label", host: "mybucket.S3.localhost", want: "s3:mybucket"},
		{name: "labelled bucket, upper region", host: "MyBucket.s3.US-EAST-1.localhost", want: "s3:mybucket"},
		{name: "legacy dash dialect, mixed case", host: "Legacy-Bucket.S3-US-West-2.localhost", want: "s3:legacy-bucket"},
		{name: "dotted bucket, mixed case", host: "My.Dotted.Bucket.localhost", want: "s3:my.dotted.bucket"},
		{name: "s3 service endpoint, upper", host: "S3.localhost", want: "none"},

		// ---- Tier C: one row per registered label, since each is a separate
		// map lookup that was case-sensitive.
		{name: "apigw, upper label", host: "abc123.EXECUTE-API.us-east-1.localhost:4566", want: "hostroute:execute-api id=abc123 region=us-east-1"},
		{name: "apigw, upper region", host: "abc123.execute-api.US-EAST-1.localhost:4566", want: "hostroute:execute-api id=abc123 region=us-east-1"},
		{name: "apigw, fully upper", host: "ABC123.EXECUTE-API.US-EAST-1.LOCALHOST.OVERCAST.SH:4566", want: "hostroute:execute-api id=abc123 region=us-east-1"},
		{name: "lambda url, mixed case", host: "UrlId.Lambda-Url.US-East-1.localhost:4566", want: "hostroute:lambda-url id=urlid region=us-east-1"},
		{name: "appsync graphql, mixed case", host: "MyApi.AppSync-Api.US-East-1.localhost:4566", want: "hostroute:appsync-api id=myapi region=us-east-1"},
		{name: "appsync realtime, mixed case", host: "MyApi.AppSync-Realtime-Api.US-East-1.localhost:4566", want: "hostroute:appsync-realtime-api id=myapi region=us-east-1"},
		// CloudFront is the case that brought this in: its IDs are the only
		// uppercase ones Overcast mints, so pasting a minted DomainName into a
		// browser is exactly this row.
		{name: "cloudfront, browser-lowercased", host: "e1pqrs2t3u4v5w.cloudfront.localhost.overcast.sh:4566", want: "hostroute:cloudfront id=e1pqrs2t3u4v5w region="},
		{name: "cloudfront, as minted", host: "E1PQRS2T3U4V5W.cloudfront.localhost.overcast.sh:4566", want: "hostroute:cloudfront id=e1pqrs2t3u4v5w region="},
		{name: "cloudfront, upper label", host: "E1PQRS2T3U4V5W.CloudFront.net", want: "hostroute:cloudfront id=e1pqrs2t3u4v5w region="},

		// ---- Precedence survives folding, in both collision directions -----
		{name: "reserved-suffix bucket labelled, mixed case", host: "My.Execute-Api.S3.localhost", want: "s3:my.execute-api"},
		{name: "configured base containing a label, mixed case", host: "MyBucket.Execute-Api.MyCorp.com", configured: "execute-api.mycorp.com", want: "s3:mybucket"},
		{name: "bucket named like a service, upper", host: "EXECUTE-API.localhost:4566", want: "s3:execute-api"},

		// ---- Folding must not invent claims --------------------------------
		{name: "unregistered label, upper", host: "abc123.NOT-A-REAL-SERVICE.us-east-1.amazonaws.com", want: "none"},
		{name: "IPv4 literal is untouched", host: "127.0.0.1:4566", want: "none"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// When: the mixed-case host is classified
			got := NewHostClassifier(tc.configured).Classify(tc.host)

			// Then: it lands on the same owner, with fields in canonical case
			if s := claimString(got); s != tc.want {
				t.Errorf("Classify(%q) with OVERCAST_HOSTNAME=%q\n got: %s\nwant: %s",
					tc.host, tc.configured, s, tc.want)
			}
		})
	}
}

// TestHostClassifier_lowercaseHostStaysAllocationFree pins the cost of case
// folding to the requests that actually need it. Classify runs on every
// request and is specified allocation-free (§7 of the plan, and
// BenchmarkHostClassifier_Classify); an unconditional strings.ToLower would
// allocate on all of them. Real clients send lowercase, so folding must be
// pay-per-use: detect first, convert only when there is something to convert.
func TestHostClassifier_lowercaseHostStaysAllocationFree(t *testing.T) {
	// Given: the classifier, and one host per tier in the case clients send
	c := NewHostClassifier("localhost.overcast.sh")
	for _, host := range []string{
		"localhost:4566",
		"127.0.0.1:4566",
		"mybucket.localhost:4566",
		"mybucket.s3.us-east-1.localhost:4566",
		"abc123.execute-api.us-east-1.localhost.overcast.sh:4566",
		"e1pqrs2t3u4v5w.cloudfront.localhost.overcast.sh:4566",
	} {
		t.Run(host, func(t *testing.T) {
			// When: it is classified repeatedly
			got := testing.AllocsPerRun(100, func() { _ = c.Classify(host) })

			// Then: no allocation was needed to do it
			if got != 0 {
				t.Errorf("Classify(%q) allocated %v times per run; want 0", host, got)
			}
		})
	}
}

// TestBucketNameReservedLabel covers the reserved-label predicate backing the
// CreateBucket diagnostic: a reserved label is only a problem as the
// second-or-later dot segment, because that is where it lands at hostname
// index >= 1.
func TestBucketNameReservedLabel(t *testing.T) {
	// Given: bucket names with and without reserved labels in each position
	cases := []struct {
		bucket    string
		wantLabel string
	}{
		{bucket: "mybucket", wantLabel: ""},
		{bucket: "my.dotted.bucket", wantLabel: ""},
		{bucket: "execute-api", wantLabel: ""},       // index 0 — safe
		{bucket: "execute-api.thing", wantLabel: ""}, // index 0 — safe
		{bucket: "my.execute-api", wantLabel: "execute-api"},
		{bucket: "my.execute-api.thing", wantLabel: "execute-api"},
		{bucket: "my.lambda-url", wantLabel: "lambda-url"},
		{bucket: "my.appsync-api", wantLabel: "appsync-api"},
		{bucket: "prefix.my.execute-api", wantLabel: "execute-api"},
	}

	for _, tc := range cases {
		t.Run(tc.bucket, func(t *testing.T) {
			// When: the name is checked for a reserved label
			label, ok := BucketNameReservedLabel(tc.bucket)

			// Then: the label and its presence match expectation
			if (tc.wantLabel != "") != ok {
				t.Fatalf("BucketNameReservedLabel(%q) ok = %v, want %v", tc.bucket, ok, tc.wantLabel != "")
			}
			if label != tc.wantLabel {
				t.Errorf("BucketNameReservedLabel(%q) = %q, want %q", tc.bucket, label, tc.wantLabel)
			}
		})
	}
}

// TestReservedHostLabels_areNotPlausibleBucketSuffixes enforces the permanent
// guardrail in §11 of the plan: the collision surface stays small only because
// every dispatch label is a hyphenated, AWS-specific data-plane token. A label
// that is a bare common word would silently widen it.
func TestReservedHostLabels_areNotPlausibleBucketSuffixes(t *testing.T) {
	// Given: the registered dispatch labels
	labels := ReservedHostLabels()
	if len(labels) == 0 {
		t.Fatal("ReservedHostLabels() returned nothing; the dispatch table should never be empty")
	}

	for _, label := range labels {
		t.Run(label, func(t *testing.T) {
			// When/Then: each label must be a compound AWS token, not a word
			// someone would end a bucket name with. Hyphenation is the cheap
			// signal; a single-word label is allowed only with a written
			// rationale, which keeps the exception visible in review. See the
			// recipe in hostroute.go before relaxing this.
			hyphenated := false
			for i := 0; i < len(label); i++ {
				if label[i] == '-' {
					hyphenated = true
					break
				}
			}
			if hyphenated {
				return
			}
			if rationale := nonHyphenatedLabelRationale[label]; rationale == "" {
				t.Errorf("host-route label %q is a single bare word with no recorded rationale; "+
					"either use the hyphenated AWS data-plane hostname, or add an entry to "+
					"nonHyphenatedLabelRationale explaining why it cannot plausibly be a "+
					"segment of a bucket name", label)
			}
		})
	}
}

// TestHostAddressing_dispatchesExactlyOneRewrite proves the middleware applies
// the S3 rewrite or the host-route rewrite, never both, through the real
// http.Handler chain.
func TestHostAddressing_dispatchesExactlyOneRewrite(t *testing.T) {
	// Given: a middleware with a host-route row registered for execute-api
	cases := []struct {
		name     string
		host     string
		wantPath string
	}{
		{name: "host-routed invoke is not prefixed with a bucket", host: "abc123.execute-api.us-east-1.localhost.overcast.sh:4566", wantPath: "/_overcast/apigateway/abc123/test/hello"},
		{name: "bare bucket is prefixed once", host: "mybucket.localhost:4566", wantPath: "/mybucket/test/hello"},
		{name: "labelled bucket is prefixed once", host: "mybucket.s3.localhost", wantPath: "/mybucket/test/hello"},
		{name: "path-style host is untouched", host: "localhost:4566", wantPath: "/test/hello"},
		{name: "dotted bucket bare is prefixed once", host: "my.dotted.bucket.localhost", wantPath: "/my.dotted.bucket/test/hello"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rows := []HostRouteRow{
				{Label: "execute-api", Rewrite: func(r *http.Request, m HostRouteMatch) {
					r.URL.Path = "/_overcast/apigateway/" + m.ID + r.URL.Path
				}},
			}
			var gotPath string
			next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotPath = r.URL.Path
			})
			mw := HostAddressing("", &rows, nil)(next)

			// When: a request with that Host passes through
			req := httptest.NewRequest(http.MethodGet, "/test/hello", nil)
			req.Host = tc.host
			mw.ServeHTTP(httptest.NewRecorder(), req)

			// Then: the path was rewritten by exactly one owner
			if gotPath != tc.wantPath {
				t.Errorf("Host %q → path %q, want %q", tc.host, gotPath, tc.wantPath)
			}
		})
	}
}

// TestHostAddressing_stampsClaimForLogging proves the claim reaches the request
// context, so detectService labels a request by what actually routed it rather
// than re-deriving from a parallel table.
func TestHostAddressing_stampsClaimForLogging(t *testing.T) {
	// Given: a middleware with no rows registered at all
	var rows []HostRouteRow
	var got HostClaim
	var ok bool
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, ok = HostClaimFromContext(r.Context())
	})
	mw := HostAddressing("", &rows, nil)(next)

	// When: a host-routed request passes through
	req := httptest.NewRequest(http.MethodGet, "/graphql", nil)
	req.Host = "myapi.appsync-api.us-east-1.localhost:4566"
	mw.ServeHTTP(httptest.NewRecorder(), req)

	// Then: the claim is available downstream, even with no row to rewrite it
	if !ok {
		t.Fatal("no HostClaim stamped into the request context")
	}
	if got.Kind != HostClaimHostRoute || got.Route.Label != "appsync-api" {
		t.Errorf("stamped claim = %s, want hostroute:appsync-api", claimString(got))
	}
}

// TestHostAddressing_stampsS3Claim proves a virtual-hosted request carries its
// claim downstream, so a root the router shares with other services can tell
// that the rewritten path starts with a bucket (#2098).
func TestHostAddressing_stampsS3Claim(t *testing.T) {
	// Given: a middleware with no rows registered
	var rows []HostRouteRow
	var got HostClaim
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, _ = HostClaimFromContext(r.Context())
	})
	mw := HostAddressing("", &rows, nil)(next)

	// When: a request virtual-hosted to a bucket named "applications" passes through
	req := httptest.NewRequest(http.MethodGet, "/key.txt", nil)
	req.Host = "applications.s3.localhost:4566"
	mw.ServeHTTP(httptest.NewRecorder(), req)

	// Then: the S3 claim is available downstream
	if got.Kind != HostClaimS3 || got.Bucket != "applications" {
		t.Errorf("stamped claim = %s, want s3:applications", claimString(got))
	}
}

// TestFoldHostname_lowercaseInputIsNotCopied pins the pay-per-use property the
// allocation-free contract depends on: a host that needs no folding must be
// returned as-is, not copied. Without it, the fold would quietly put an
// allocation on every request — which is what the Classify benchmarks exist to
// prevent, and this states it directly for the one function responsible.
func TestFoldHostname_lowercaseInputIsNotCopied(t *testing.T) {
	const host = "abc123.execute-api.us-east-1.localhost.overcast.sh:4566"
	if got := testing.AllocsPerRun(100, func() { _ = foldHostname(host) }); got != 0 {
		t.Errorf("foldHostname(%q) allocated %v times per run; want 0", host, got)
	}
}
