package middleware

import (
	"net/http"
	"strings"
)

// hostroute.go implements ONE general grammar + dispatch table for AWS
// services whose real invoke/control endpoints encode a resource ID (and
// usually a region) as a Host subdomain rather than in the request path:
//
//	{id}.{label}[.{region}].{base}[:port]
//
// Real examples:
//
//	myapi123.execute-api.us-east-1.amazonaws.com   (API Gateway invoke, v1+v2)
//	{url-id}.lambda-url.us-east-1.on.aws           (Lambda function URLs)
//	myapi456.appsync-api.us-east-1.amazonaws.com   (AppSync GraphQL)
//
// ParseHostRoute recognises this grammar against the fixed set of known
// `label` tokens in hostRouteLabels and returns the {id} (everything before
// the first recognised label — dot-joined, so IDs that themselves contain
// dots are supported) and {region} (the segment immediately after the label,
// only when it looks like an AWS region — otherwise region is "" and that
// segment is just the start of {base}).
//
// hostRouteLabels is the single source of truth mapping a label token to the
// AWS service that owns it. HostAddressing (hostaddressing.go) reads it to
// classify requests, and detectService (logger.go) resolves the log label from
// the claim that classification produced, so a request's log label can never
// drift from what it was actually dispatched to.
//
// ---- Adding a new host-routed service ----
//
//  1. Check the label cannot plausibly end a bucket name. See the guardrail
//     below — this is a correctness requirement, not style.
//  2. Add its "label" -> service-name entry to hostRouteLabels below.
//  3. In router.go, append one middleware.HostRouteRow{Label: "...", Rewrite: ...}
//     to the rows slice passed to middleware.HostAddressing. Keep Rewrite thin:
//     a PrefixPath call (and maybe a region context stamp) only, or a call
//     into one small exported method on the owning service (e.g.
//     apigwSvc.HostRouteRewrite) — never protocol/business logic here. Do not
//     assign r.URL.Path by hand: it loses the client's encoding (#2136).
//
// ---- Guardrail: labels must not be plausible bucket-name segments ----
//
// A bucket named "my.execute-api" is not addressable in the bare
// virtual-hosted form, because "my.execute-api.localhost" parses as an API
// Gateway invoke. That collision surface stays negligible only because every
// registered label is a hyphenated, AWS-specific data-plane token that nobody
// ends a bucket name with. Registering a bare common word ("logs", "events",
// "data") would widen it immediately.
//
// Prefer the AWS data-plane hostname verbatim — "appsync-realtime-api", not
// "realtime". TestReservedHostLabels_areNotPlausibleBucketSuffixes enforces
// the hyphenation rule; docs/plans/host-routing-precedence.md §6 tracks making
// this evidence-based against the generated AWS operation manifest, so the set
// can only grow when AWS itself adds a service or hostname.
//
// ---- Also reads this table ----
//
//   - internal/middleware/region.go's regionFromHost: extracts the region hint
//     from this same grammar for SigV4-less requests, which is the only region
//     evidence a host-routed invoke carries. It used to keep a third divergent
//     label list; H5 folded it onto parseHostRouteName, so registering a label
//     below is all a new host-routed service needs to resolve in the right
//     region. It keeps a small disjoint set for regional service endpoints
//     Overcast does NOT dispatch on ("s3" and friends), which must stay out of
//     this table — see regionalEndpointLabels.
//
// Host-route labels, exported so the services that MINT these URLs
// (serviceutil.HostRoutedURL callers) use the same string the router
// dispatches on. serviceutil cannot import this package — middleware imports
// serviceutil — so the label travels as a parameter; these constants are what
// stops the two sides drifting.
const (
	LabelExecuteAPI         = "execute-api"
	LabelLambdaURL          = "lambda-url"
	LabelAppSyncAPI         = "appsync-api"
	LabelAppSyncRealtimeAPI = "appsync-realtime-api"
	LabelCloudFront         = "cloudfront"
	LabelELB                = "elb"
)

var hostRouteLabels = map[string]string{
	LabelExecuteAPI:         "apigateway",
	LabelLambdaURL:          "lambda",
	LabelAppSyncAPI:         "appsync",
	LabelAppSyncRealtimeAPI: "appsync",
	LabelCloudFront:         "cloudfront",
	LabelELB:                "elbv2",
}

// nonHyphenatedLabelRationale documents why a label that is a single word is
// nonetheless safe, since the hyphenation heuristic below cannot judge it.
// Every entry must be an AWS brand term that no one would use as a segment of
// a bucket name. A generic word ("logs", "data", "cache") does not qualify and
// must never be added.
var nonHyphenatedLabelRationale = map[string]string{
	LabelCloudFront: "AWS brand term; {distributionId}.cloudfront.net is the real distribution address",
	LabelELB:        "AWS brand term; {name}-{id}.{region}.elb.amazonaws.com is the real load balancer address",
}

// isAWSRegionShaped matches AWS region shapes: us-east-1, ap-southeast-2,
// us-gov-west-1, us-iso-east-1, etc. Used to decide whether the host segment
// right after the label is a region or already the start of {base}.
//
// It is the pattern `^[a-z]{2}(-gov|-iso[a-z]*)?-[a-z]+-\d$` evaluated by
// hand rather than a package-level regexp because it sits on Classify's
// per-request path, which is specified allocation-free: regexp matching
// borrows its machine from an internal sync.Pool, so it is only *amortized*
// allocation-free — and under the race detector sync.Pool deliberately drops
// items at random, which made the allocation-free regression test flake on CI
// (-race) runs. Dash-splitting makes the pattern unambiguous (no segment may
// contain '-', so each alternative fixes the segment count), so no
// backtracking is needed. TestIsAWSRegionShaped_matchesRegexpOracle pins this
// function to the regexp it replaced.
func isAWSRegionShaped(s string) bool {
	// Split on '-' into at most four segments; more can never match.
	var segs [4]string
	n := 0
	start := 0
	for i := 0; i <= len(s); i++ {
		if i < len(s) && s[i] != '-' {
			continue
		}
		if n == len(segs) {
			return false
		}
		segs[n] = s[start:i]
		n++
		start = i + 1
	}
	if n < 3 {
		return false
	}
	// ^[a-z]{2}, ... -[a-z]+ ... , and -\d$ hold in every alternative.
	if len(segs[0]) != 2 || !isLowerAlpha(segs[0]) {
		return false
	}
	if last := segs[n-1]; len(last) != 1 || last[0] < '0' || last[0] > '9' {
		return false
	}
	if segs[n-2] == "" || !isLowerAlpha(segs[n-2]) {
		return false
	}
	if n == 3 {
		return true
	}
	// Four segments: the second must be the optional (-gov|-iso[a-z]*) group.
	return segs[1] == "gov" || (strings.HasPrefix(segs[1], "iso") && isLowerAlpha(segs[1][3:]))
}

// isLowerAlpha reports whether s is entirely a-z. The empty string is
// lower-alpha: it is the `[a-z]*` remainder of a bare "iso" group — callers
// needing `[a-z]+` check non-emptiness themselves.
func isLowerAlpha(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < 'a' || s[i] > 'z' {
			return false
		}
	}
	return true
}

// HostRouteMatch is a successfully parsed Host-based AWS endpoint address.
type HostRouteMatch struct {
	// Label is the recognised host segment, e.g. "execute-api". Always a key
	// of hostRouteLabels.
	Label string
	// ID is the subdomain segment(s) before Label, dot-joined.
	ID string
	// Region is the AWS region parsed from the segment after Label, or ""
	// if that segment doesn't look like a region (e.g. the base hostname
	// starts right after the label, as with a bare "localhost" base).
	Region string
}

// ParseHostRoute parses host (which may include a port) against the AWS
// `{id}.{label}[.{region}].{base}` grammar using the labels registered in
// hostRouteLabels. Returns ok=false for path-style requests, IP literals, or
// hosts that don't contain a registered label.
// It considers the grammar in isolation. Callers that must also account for S3
// virtual-hosted addressing — i.e. anything on the request path — should use
// HostClassifier.Classify instead, which applies the full precedence rule.
func ParseHostRoute(host string) (HostRouteMatch, bool) {
	hostname := foldHostname(hostWithoutPort(host))
	if hostname == "" || isIPLiteral(hostname) {
		return HostRouteMatch{}, false
	}
	return parseHostRouteName(hostname)
}

// HostRouteServiceFor reports the detectService() label owning a parsed
// host-route match — e.g. "apigateway" for an execute-api Host. detectService
// resolves it from the claim HostAddressing stamped on the request, so a
// request's log label is what actually routed it rather than a re-derivation
// that could disagree.
func HostRouteServiceFor(m HostRouteMatch) (service string, ok bool) {
	service, ok = hostRouteLabels[m.Label]
	return service, ok
}

// HostRouteRow binds one recognised label to the rewrite that adapts a
// Host-routed request into the owning service's existing path-style route.
// See the package doc above for the recipe to add a new row.
type HostRouteRow struct {
	// Label must be a key of hostRouteLabels.
	Label string
	// Rewrite mutates r (typically r.URL.Path/RawPath, and optionally the
	// request context, e.g. to stamp a region hint) in place so the request
	// matches a route already registered for the owning service. Called
	// once, synchronously, before chi's router dispatches on the (possibly
	// now-different) path. Rewrite should always mutate on a recognised ID
	// — even one that turns out not to exist — and let the owning service's
	// own handler produce the AWS-shaped not-found/forbidden error; only a
	// Host that doesn't match the grammar at all should fall through
	// untouched (see AGENTS.md "Routing fallthrough is S3").
	Rewrite func(r *http.Request, m HostRouteMatch)
}

// PrefixPath prepends prefix (a literal path with nothing to escape) to the
// request path, keeping r.URL.RawPath in step when it is set. RawPath holds
// the client's encoding whenever it differs from the default, most notably a
// %2F inside a segment, and chi routes on it; copying the decoded Path over
// it turns that %2F into a separator. API Gateway, Lambda function URLs, ALB
// and CloudFront all hand the path on still encoded, so the rewrite must too
// (#2136).
//
// An empty Path (an absolute-form request line with no path) becomes prefix
// + "/", which is what the internal /* routes need. S3 virtual-hosted
// addressing keeps its own inline prefixing because it must map that case to
// "/{bucket}", not "/{bucket}/".
func PrefixPath(r *http.Request, prefix string) {
	r.URL.Path = prefix + withLeadingSlash(r.URL.Path)
	if r.URL.RawPath != "" {
		r.URL.RawPath = prefix + withLeadingSlash(r.URL.RawPath)
	}
}

func withLeadingSlash(p string) string {
	if strings.HasPrefix(p, "/") {
		return p
	}
	return "/" + p
}

// The middleware that applies these rows is HostAddressing in
// hostaddressing.go. It is not in this file because dispatching a host-routed
// request cannot be decided independently of S3 virtual-hosted addressing —
// attempting that is what produced the double-claim bug documented in
// docs/plans/host-routing-precedence.md.
