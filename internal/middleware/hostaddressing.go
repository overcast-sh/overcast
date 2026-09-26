package middleware

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"sync"
	"sync/atomic"

	"go.uber.org/zap"

	"github.com/overcast-sh/overcast/internal/serviceutil"
)

// hostaddressing.go decides, once per request, who owns a Host header: S3
// virtual-hosted addressing, a host-routed AWS service, or nobody (path-style,
// which falls through to S3 per AGENTS.md "Routing fallthrough is S3").
//
// This decision used to be split across two independently-registered
// middlewares — S3VirtualHostFor and HostDispatch — which both claimed the
// same request and each rewrote the path the other had already rewritten. Every
// host-routed invoke URL on a base Overcast can actually resolve was broken by
// it. See docs/plans/host-routing-precedence.md for the evidence.
//
// Precedence therefore derives from the host grammar, not from middleware
// registration order:
//
//	Tier A  {bucket}.s3.{...} / {bucket}.s3-{region}.{...}
//	          -> S3. Bucket is everything before the FIRST ".s3." / ".s3-".
//	Tier B  {bucket}.{base} for a recognised base
//	          -> S3, bucket is everything in front of the base — UNLESS that
//	             carries a reserved host label as a second-or-later segment,
//	             which is a host-routed address and defers to tier C.
//	Tier C  {id}.{label}[.{region}].{...} for a label in hostRouteLabels at
//	        index >= 1
//	          -> the owning service.
//	        anything else -> unclaimed.
//
// A must beat C so a dotted bucket addressed explicitly wins
// ("my.execute-api.s3.localhost" is a bucket, not an API Gateway invoke).
// B must beat C so an operator-configured OVERCAST_HOSTNAME wins over a
// generic label match inside it.
//
// Tier B is a local-only convenience — real AWS has no bare
// "{bucket}.{base}" form — but it is the form AWS SDKs actually emit against an
// endpoint override with virtual-hosted addressing, and the one CDK's asset
// publisher uses unconditionally (it ignores forcePathStyle). It therefore has
// to keep working, including for dotted bucket names.
//
// Disambiguating it by a reserved-label veto rather than by restricting the
// bucket to a single label is what lets both survive: LocalStack requires an
// "s3." label and floci takes only the first label, so neither can address a
// dotted bucket in this form. The residual cost here is narrower — only a
// bucket whose name carries a reserved label at segment index >= 1
// ("my.execute-api") is unreachable this way, and it stays reachable
// path-style and via tier A.
//
// ---- Performance ----
//
// This runs on every request. Classify is allocation-free: the base list is
// precomputed once in NewHostClassifier (it depends only on OVERCAST_HOSTNAME,
// fixed at startup), segments are walked by index rather than strings.Split,
// and port stripping avoids net.SplitHostPort because its error path allocates.
// See docs/plans/host-routing-precedence.md#7-performance for baseline numbers
// and the regression benchmarks in hostaddressing_bench_test.go.

// HostClaimKind identifies which addressing scheme owns a request.
type HostClaimKind uint8

const (
	// HostClaimNone means no scheme claimed the Host; the request keeps its
	// path and falls through to S3 path-style routing.
	HostClaimNone HostClaimKind = iota
	// HostClaimS3 means the Host carries an S3 bucket as a subdomain.
	HostClaimS3
	// HostClaimHostRoute means the Host matches a registered host-routed
	// service (execute-api, lambda-url, appsync-api).
	HostClaimHostRoute
)

// HostClaim is the single verdict for a request's Host header. Exactly one of
// Bucket / Route is meaningful, selected by Kind — the two can never both be
// set, which is the invariant the old two-middleware arrangement violated.
type HostClaim struct {
	Kind   HostClaimKind
	Bucket string         // set when Kind == HostClaimS3
	Route  HostRouteMatch // set when Kind == HostClaimHostRoute
}

// HostClassifier applies the precedence rule above. Construct it once (it
// precomputes the recognised base list) and call Classify per request.
type HostClassifier struct {
	// bases are the hostnames a bucket subdomain may sit in front of, sorted
	// longest-first so a configured parent domain never shadows a longer
	// default that also matches: with OVERCAST_HOSTNAME="overcast.sh",
	// "x.localhost.overcast.sh" is bucket "x", not "x.localhost". Sorting
	// happens once here, never per request.
	bases []string
}

// NewHostClassifier returns a classifier recognising the built-in wildcard-DNS
// bases plus configuredHostname (OVERCAST_HOSTNAME) when set. The configured
// hostname is additive, never a replacement.
func NewHostClassifier(configuredHostname string) *HostClassifier {
	bases := make([]string, 0, len(defaultVirtualHostBases)+1)
	bases = append(bases, defaultVirtualHostBases...)
	if configuredHostname != "" && !slices.Contains(bases, configuredHostname) {
		bases = append(bases, configuredHostname)
	}
	slices.SortStableFunc(bases, func(a, b string) int { return len(b) - len(a) })
	return &HostClassifier{bases: bases}
}

// Classify returns the single owner of host (which may include a port).
// Allocation-free.
func (c *HostClassifier) Classify(host string) HostClaim {
	hostname := foldHostname(hostWithoutPort(host))
	if hostname == "" || isIPLiteral(hostname) {
		return HostClaim{}
	}

	// Tier A — explicit S3 label. The bucket is everything before the first
	// separator, so bucket names containing dots stay addressable here.
	if i := indexS3Separator(hostname); i > 0 {
		return HostClaim{Kind: HostClaimS3, Bucket: hostname[:i]}
	}

	// Tier B — bare form. Everything in front of a recognised base is the
	// bucket, so dotted bucket names stay addressable, UNLESS that candidate
	// carries a reserved host label as a second-or-later segment — which is
	// exactly the shape of a host-routed address and must defer to tier C.
	//
	//	my.dotted.bucket.localhost          -> bucket "my.dotted.bucket"
	//	abc123.execute-api.us-east-1.localhost -> vetoed, tier C claims it
	//
	// The veto uses the same BucketNameReservedLabel predicate that backs the
	// CreateBucket diagnostic, so routing and the warning can never disagree
	// about which names are affected.
	for _, base := range c.bases {
		if len(hostname) <= len(base)+1 {
			continue
		}
		if !strings.EqualFold(hostname[len(hostname)-len(base):], base) ||
			hostname[len(hostname)-len(base)-1] != '.' {
			continue
		}
		candidate := hostname[:len(hostname)-len(base)-1]
		if isS3ServiceEndpoint(candidate) {
			// "s3.{base}" / "s3.{region}.{base}" are service endpoints.
			return HostClaim{}
		}
		if _, reserved := BucketNameReservedLabel(candidate); reserved {
			break // a host-routed address, not a bucket — fall through to tier C
		}
		return HostClaim{Kind: HostClaimS3, Bucket: candidate}
	}

	// Tier C — host-routed service.
	if m, ok := parseHostRouteName(hostname); ok {
		return HostClaim{Kind: HostClaimHostRoute, Route: m}
	}
	return HostClaim{}
}

// hostClaimContextKey is the context key for the per-request HostClaim.
type hostClaimContextKey struct{}

// HostClaimFromContext returns the claim stamped by HostAddressing. Host-routed
// and S3 virtual-hosted claims are stamped; an unclaimed (path-style) request
// is not, so ordinary path-style traffic allocates no context. The S3 claim
// matters where the rewritten path starts with a root the router shares with
// other services: a virtual-hosted bucket named "applications" is S3's, which
// its path alone no longer says (#2098).
func HostClaimFromContext(ctx context.Context) (HostClaim, bool) {
	c, ok := ctx.Value(hostClaimContextKey{}).(HostClaim)
	return c, ok
}

// HostAddressing returns the middleware that applies the precedence rule. It
// replaces the former S3VirtualHostFor + HostDispatch pair; because one
// classification drives both rewrites, they are mutually exclusive branches of
// one switch and can no longer both fire.
//
// rows is read through the pointer at request time, so callers can register
// this middleware early (chi requires all r.Use calls before any route
// registration) and populate the rows later, once the owning services exist.
func HostAddressing(configuredHostname string, rows *[]HostRouteRow, logger *zap.Logger) func(http.Handler) http.Handler {
	classifier := NewHostClassifier(configuredHostname)
	var warned sync.Map
	var warnedCount atomic.Int64

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch claim := classifier.Classify(r.Host); claim.Kind {
			case HostClaimS3:
				r.URL.Path = "/" + claim.Bucket + r.URL.Path
				if r.URL.RawPath != "" {
					r.URL.RawPath = "/" + claim.Bucket + r.URL.RawPath
				}
				// Stamped so a root the router shares with other services
				// can tell that this path starts with a bucket (AddressesS3).
				r = r.WithContext(context.WithValue(r.Context(), hostClaimContextKey{}, claim))
			case HostClaimHostRoute:
				for _, row := range *rows {
					if row.Label == claim.Route.Label {
						row.Rewrite(r, claim.Route)
						break
					}
				}
				// Stamp the claim so detectService labels this request by what
				// actually routed it, rather than re-deriving from a parallel
				// table that could disagree.
				r = r.WithContext(context.WithValue(r.Context(), hostClaimContextKey{}, claim))
			case HostClaimNone:
				// Nothing claimed the Host, so the request keeps its path and
				// falls through to S3 per AGENTS.md "Routing fallthrough is
				// S3". Warn only if it looked like it meant to be
				// virtual-hosted against a base we do not recognise.
				warnUnrecognisedBase(logger, &warned, &warnedCount, r.Host, configuredHostname)
			}
			next.ServeHTTP(w, r)
		})
	}
}

// ReservedHostLabels returns the host labels Overcast dispatches on, sorted for
// stable output. These are "reserved" only in the routing sense: a bucket whose
// name carries one as a second-or-later dot segment is not addressable in the
// bare virtual-hosted form (see BucketNameReservedLabel). Bucket creation is
// never refused — real AWS accepts such names, and both path-style and tier-A
// addressing keep working.
//
// This is deliberately NOT the set of all AWS endpoint prefixes. Reserving all
// of them would make names like "my.logs" or "my.events" collide for no
// benefit, since Overcast does not host-route those services.
func ReservedHostLabels() []string {
	labels := make([]string, 0, len(hostRouteLabels))
	for label := range hostRouteLabels {
		labels = append(labels, label)
	}
	slices.Sort(labels)
	return labels
}

// BucketNameReservedLabel reports whether bucket carries a reserved host label
// as a second-or-later dot segment, which is the only position that collides:
// in "{bucket}.{base}" the bucket's first segment lands at hostname index 0,
// and ParseHostRoute requires a label at index >= 1 because {id} must be
// non-empty in every real AWS host-routed shape.
//
//	"execute-api"            -> "", false  (index 0, safe)
//	"execute-api.thing"      -> "", false  (index 0, safe)
//	"my.execute-api"         -> "execute-api", true
//	"my.execute-api.thing"   -> "execute-api", true
func BucketNameReservedLabel(bucket string) (string, bool) {
	start := 0
	for segment := 0; ; segment++ {
		end := strings.IndexByte(bucket[start:], '.')
		var seg string
		if end < 0 {
			seg = bucket[start:]
		} else {
			seg = bucket[start : start+end]
		}
		if segment > 0 {
			if _, ok := hostRouteLabels[seg]; ok {
				return seg, true
			}
		}
		if end < 0 {
			return "", false
		}
		start += end + 1
	}
}

// indexS3Separator returns the index of the first ".s3." or ".s3-" separator,
// or -1. ".s3-" is the legacy dash dialect used by pre-2019 regions
// (mybucket.s3-us-west-2.amazonaws.com). A separator at index 0 is not a
// bucket — there would be no name in front of it.
func indexS3Separator(hostname string) int {
	if i := strings.Index(hostname, ".s3."); i > 0 {
		return i
	}
	if i := strings.Index(hostname, ".s3-"); i > 0 {
		return i
	}
	return -1
}

// parseHostRouteName finds a registered host-route label at segment index >= 1
// in a hostname that already has its port stripped and its case folded (see
// foldHostname — the label lookup is an exact map hit, so an unfolded hostname
// silently fails to match). It walks segments by index rather than
// strings.Split so it allocates nothing.
func parseHostRouteName(hostname string) (HostRouteMatch, bool) {
	start := 0
	for segment := 0; ; segment++ {
		end := strings.IndexByte(hostname[start:], '.')
		var seg string
		if end < 0 {
			seg = hostname[start:]
		} else {
			seg = hostname[start : start+end]
		}

		// The label can never be the first segment: {id} must be non-empty,
		// matching every real AWS host-routed shape.
		if segment > 0 {
			if _, ok := hostRouteLabels[seg]; ok {
				m := HostRouteMatch{Label: seg, ID: hostname[:start-1]}
				if end >= 0 {
					m.Region = regionSegment(hostname[start+end+1:])
				}
				return m, true
			}
		}
		if end < 0 {
			return HostRouteMatch{}, false
		}
		start += end + 1
	}
}

// regionSegment returns rest's first dot-separated segment when it looks like
// an AWS region, else "". A non-region segment means the base hostname starts
// straight after the label (e.g. "{id}.execute-api.localhost").
func regionSegment(rest string) string {
	seg := rest
	if i := strings.IndexByte(rest, '.'); i >= 0 {
		seg = rest[:i]
	}
	if isAWSRegionShaped(seg) {
		return seg
	}
	return ""
}

// hostWithoutPort strips a trailing :port and IPv6 brackets. It deliberately
// avoids net.SplitHostPort, whose error path allocates an *AddrError on every
// portless host — the common case for a Host header.
func hostWithoutPort(host string) string {
	if host == "" {
		return ""
	}
	if host[0] == '[' {
		if end := strings.IndexByte(host, ']'); end > 0 {
			return host[1:end]
		}
		return host
	}
	i := strings.LastIndexByte(host, ':')
	if i < 0 {
		return host
	}
	// More than one colon and no brackets: a bare IPv6 literal, not host:port.
	if strings.IndexByte(host, ':') != i {
		return host
	}
	if isAllDigits(host[i+1:]) {
		return host[:i]
	}
	return host
}

// foldHostname is serviceutil.FoldHostname, aliased so the hot path reads
// plainly and so this package documents WHY classification folds: casing must
// never decide which service answers a request. The implementation lives in
// serviceutil because minting needs the identical rule on the way out, and one
// implementation is the only way the two directions cannot drift.
func foldHostname(hostname string) string { return serviceutil.FoldHostname(hostname) }

// isIPLiteral reports whether hostname (port and brackets already removed) is
// an IP address rather than a DNS name. IP literals never carry a
// virtual-hosted or host-routed subdomain.
func isIPLiteral(hostname string) bool {
	if strings.IndexByte(hostname, ':') >= 0 {
		return true // IPv6, brackets already stripped
	}
	return isIPv4Shaped(hostname)
}

// isAllDigits reports whether s is non-empty and entirely ASCII digits.
func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// isIPv4Shaped reports whether s is non-empty and contains only ASCII digits
// and dots. A DNS name always has at least one non-digit label, so this is
// sufficient to separate "127.0.0.1" from "mybucket.localhost".
func isIPv4Shaped(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c != '.' && (c < '0' || c > '9') {
			return false
		}
	}
	return true
}
