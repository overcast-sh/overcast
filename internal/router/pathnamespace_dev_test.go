//go:build dev

package router

import (
	"sort"
	"strings"
	"testing"

	"github.com/overcast-sh/overcast/internal/awsapi"
	"github.com/overcast-sh/overcast/internal/config"
	"github.com/overcast-sh/overcast/internal/middleware"
)

// internalPrefix is middleware.InternalPrefix, aliased so this file reads the
// same as it did when the constant lived here. It moved to middleware in phase
// 6 because both packages need it and the dependency only runs one way:
// router imports middleware, never the reverse.
const internalPrefix = middleware.InternalPrefix

// nonManifestRoutes are the paths Overcast serves that are neither a modeled
// AWS binding nor under internalPrefix, and are expected to stay that way.
//
// Each entry carries its reason as a value rather than a comment, so a failure
// reads as an argument rather than a list. Adding one is legal and reviewable;
// the review question is always "why is this not under /_overcast/?"
var nonManifestRoutes = map[string]string{
	"/favicon.ico": "browser-chosen path; a 204 to keep auto-requests out of the S3 fallback. We do not control where a browser asks for this.",

	"/":  "protocol root: awsQuery and awsJson dispatch on X-Amz-Target or the Action parameter, never on the path, so no modeled URI describes it.",
	"/*": "protocol root: the S3 fallback. S3's bucket/object space is deliberately unbounded, which is what makes every unclaimed prefix S3's.",
	"/service/{service}/operation/{operation}": "protocol root: Smithy RPC v2 names the service and operation in the path itself. The manifest records each operation's own URI, never this dispatch shape.",

	"/iceberg/*": "S3 Tables' Iceberg REST catalog, at the path AWS serves it on the S3 Tables endpoint (https://s3tables.<region>.amazonaws.com/iceberg). It is a real AWS surface, but the Iceberg REST spec defines it rather than any AWS model, so no manifest row can cover it. Dispatched on the SigV4 signing name like the S3 Tables roots, because \"iceberg\" is a legal bucket name; unsigned clients use the /_overcast/s3tables/iceberg mount instead.",

	"/{accountID:[0-9]+}/{queueName}": "SQS path-style queue URL. A real AWS shape, but the manifest models it as an endpoint rather than an operation URI, so no row can cover it.",

	"/_health":            "Overcast's own health endpoint before phase 2 moved it into the namespace, kept as an alias. A URL already written into someone's compose healthcheck or Testcontainers wait strategy is not a URL we can migrate for them, and a 404 there reads as a dead container — so it is served rather than left to the S3 catch-all. Reserved by the same underscore rule as internalPrefix. See internal/router/health_compat.go.",
	"/_localstack/health": "LocalStack's health endpoint — what a compose healthcheck or wait strategy carried over from LocalStack polls — served in LocalStack's own response shape. Same reason and same underscore guarantee as /_health above.",
	"/_localstack/*":      "The remainder of LocalStack's operational namespace, answered with a 404 naming the Overcast endpoint instead of S3's NoSuchBucket. One route for the whole prefix, so the namespace is accounted for rather than only the paths that happen to be mapped today.",

	// The three paths under that prefix Overcast answers rather than points
	// elsewhere. Each is registered onto the same handler value as its
	// /_overcast/ original, so there is no second implementation to drift —
	// see internal/router/localstack_compat.go. Same underscore guarantee as
	// /_localstack/health above.
	"/_localstack/init":         "LocalStack's init-hook status endpoint, on the same handler as /_overcast/init. Overcast's status body was built to LocalStack's contract — same stage names, same script states — so this needs no translation.",
	"/_localstack/init/{stage}": "The per-stage form of the above, on the same handler as /_overcast/init/{stage}.",
	"/_localstack/state/reset":  "LocalStack's state-reset endpoint, on the same handler as /_overcast/reset. A test suite that resets between cases hard-codes this URL, and a 404 leaves the next case running against the previous one's resources.",

	// LocalStack's /_aws/ inspection namespace — the other half of the
	// namespace above, and the half a test suite hits rather than a
	// healthcheck: `curl localhost:4566/_aws/ses` is written into one
	// assertion per test that sends an email. Each served path translates a
	// store the /_overcast/ API already exposes into LocalStack's wire shape;
	// see internal/router/aws_compat.go. Same underscore guarantee as
	// /_localstack/health above.
	"/_aws/ses":          "LocalStack's captured-email endpoint (GET and DELETE), over the same inbox store as /_overcast/ses/inbox/messages, rendered in LocalStack's {\"messages\": [...]} shape.",
	"/_aws/sqs/messages": "LocalStack's queue peek, over the same read as GET /{accountID}/{queueName}, rendered as LocalStack's ReceiveMessageResponse in XML or JSON per Accept.",
	"/_aws/sqs/messages/{region}/{accountID}/{queueName}": "The path form of the above, which LocalStack documents beside the ?QueueUrl= form.",
	"/_aws/*": "The remainder of LocalStack's inspection namespace, answered with a 404 naming the Overcast endpoint instead of S3's NoSuchBucket — one route for the whole prefix, as for /_localstack/* above.",

	"/restapis/{restApiId}/{stageName}/_user_request_/*": "LocalStack URL compatibility for API Gateway execute-api. The underscore mid-path is deliberate and must not be tidied into the namespace: the point is byte-identical compatibility with a URL LocalStack documents, and host-addressed callers already have the namespaced route. See docs/plans/non-canonical-url-namespace.md section 3.",
	"/restapis/{restApiId}/{stageName}/_user_request_":   "The stage root of the route above — LocalStack's URL with an empty path. Same reason, registered separately because chi's wildcard does not match the empty remainder.",

	"/{poolId}/.well-known/jwks.json":            "Cognito OIDC discovery, at the path portion of AWS's own issuer. AWS models no SDK operation for discovery, so no manifest row can ever cover it whatever shape it takes — this is a permanent exception, not a migration that stalled. Safe at the top level because a pool ID contains \"_\" and an S3 bucket name cannot; TestOIDCDiscoveryPathCannotCollideWithS3OrSQS pins that.",
	"/{poolId}/.well-known/openid-configuration": "As above. OIDC Discovery 1.0 section 4.3 requires the issuer to be byte-identical to the URL this document was fetched from, which is why the route and issuerFor have to agree and neither can move alone.",
}

// unmigratedRoutes is the ratchet: every path that breaks the rule today,
// recorded so the gate can be enforced from the commit that introduces it
// rather than from the commit that finishes the migration. The value names the
// phase of docs/plans/non-canonical-url-namespace.md that retires the entry.
//
// It only ever shrinks. Each phase deletes the lines it moved, and this gate
// fails on an entry that is no longer registered — so a phase cannot quietly
// leave the ledger behind.
//
// **Do not add to this map.** A new internal endpoint goes under
// internalPrefix; a new AWS operation goes where the manifest binds it. An
// entry here is a debt already owed, not a way to take on more.
var unmigratedRoutes = map[string]string{
	// Phase 2 is done: the router-owned roots now live under internalPrefix.

	// Phase 3 is done: /_overcast/debug/* and /_overcast/mcp are namespaced.

	// Phase 5 is done: the data plane is namespaced.

	// **Empty.** Every path Overcast serves is now either a binding the pinned
	// manifest models or lives under InternalPrefix, with the handful of standing
	// exceptions recorded in nonManifestRoutes above and nowhere else.
	//
	// Keeping the map rather than deleting it is deliberate: the next migration
	// that has to move routes in stages needs somewhere to put the debt, and the
	// gate's staleness check only works against a map that exists.
}

// buildTagGatedRoutes are ledger entries that some build configurations do not
// register, so their absence is not a debt someone paid without saying so.
//
// Empty since phase 3: its only entry was /_mcp/*, which mcp_routes_slim.go
// does not register, and that route is now inside the namespace and out of the
// ratchet. The mechanism stays because the next build-tag-gated route to enter
// either ledger needs it, and rediscovering why a slim run reported a phantom
// stale entry is the expensive way to learn it. Mirrors
// buildTagGatedRouteFamilies in
// internal/middleware/detectservice_routes_test.go.
var buildTagGatedRoutes = map[string]bool{}

// TestNoRouteIsRegisteredOutsideTheNamespace is the route-to-model direction of
// the manifest gates, and the enforceable form of the rule in
// docs/plans/non-canonical-url-namespace.md:
//
//	Every path Overcast serves on the AWS API listener is either a binding the
//	pinned AWS manifest models, or it lives under /_overcast/.
//
// TestModeledBindings_areServedWhereAWSBindsThem walks model to route and asks
// whether everything AWS models is reachable. This walks route to model and
// asks the opposite: whether everything reachable is something AWS models.
// Neither implies the other, and only this direction catches a path the
// emulator invented — the failure behind #793, #815 and #854-#860, which a
// name-only check cannot see.
func TestNoRouteIsRegisteredOutsideTheNamespace(t *testing.T) {
	// Given: every route the router registers, and every URI the models bind.
	// Debug is on because the /_overcast/debug/* namespace is registered behind it, and
	// a namespace gate that cannot see a whole namespace is not a gate.
	routes := inventoryWith(t, func(cfg *config.Config) { cfg.Debug = true })
	modeled := newModeledURIIndex()

	var invented, misnamespaced []string
	seenRatchet := map[string]bool{}
	seenPermanent := map[string]bool{}
	// A pattern registered for several methods is one path, and the rule is
	// about paths. Judging it once also keeps a nine-method proxy route from
	// filling the failure report with nine copies of itself.
	judged := map[string]bool{}

	for _, route := range routes.routes {
		pattern := route.Pattern
		if _, permanent := nonManifestRoutes[pattern]; permanent {
			seenPermanent[pattern] = true
			continue
		}
		if _, unmigrated := unmigratedRoutes[pattern]; unmigrated {
			seenRatchet[pattern] = true
			continue
		}
		if judged[pattern] {
			continue
		}
		judged[pattern] = true

		// When: each route is classified.
		switch {
		case strings.HasPrefix(pattern, internalPrefix):
			// Namespaced. Nothing further to prove: the prefix is reserved
			// because S3 bucket names cannot begin with an underscore.
		case strings.HasPrefix(pattern, "/_"):
			misnamespaced = append(misnamespaced, pattern)
		case !modeled.covers(pattern):
			invented = append(invented, pattern)
		}
	}

	// Then: nothing is served on a path that is neither modeled nor namespaced.
	sort.Strings(misnamespaced)
	if len(misnamespaced) > 0 {
		t.Errorf("routes under \"/_\" but outside %[1]q:\n\t%s\n"+
			"Internal endpoints live under %[1]s — one reserved prefix, not sixteen. "+
			"See docs/plans/non-canonical-url-namespace.md.",
			internalPrefix, strings.Join(misnamespaced, "\n\t"))
	}

	sort.Strings(invented)
	if len(invented) > 0 {
		t.Errorf("routes on paths no AWS model binds:\n\t%s\n"+
			"Either serve the operation where AWS binds it (check the URI in the pinned "+
			"manifest), move the endpoint under %s, or add it to nonManifestRoutes with "+
			"the reason it is neither.",
			strings.Join(invented, "\n\t"), internalPrefix)
	}

	// And: neither ledger outlives the routes it describes. An entry for a
	// path nothing registers is worse than clutter — it silently pre-approves
	// whatever gets registered there next.
	reportStaleLedger(t, "unmigratedRoutes", unmigratedRoutes, seenRatchet,
		"only shrinks; a phase that moved these owes the deletion")
	reportStaleLedger(t, "nonManifestRoutes", nonManifestRoutes, seenPermanent,
		"records exceptions to the rule; an exception with no route is not one")
}

// reportStaleLedger fails when a ledger names a path the router no longer
// registers, quoting the recorded reason so the report says what is being
// deleted and why it was ever allowed.
func reportStaleLedger(t *testing.T, name string, ledger map[string]string, seen map[string]bool, rule string) {
	t.Helper()

	var stale []string
	for pattern, reason := range ledger {
		if !seen[pattern] && !buildTagGatedRoutes[pattern] {
			stale = append(stale, pattern+"\n\t\trecorded reason: "+reason)
		}
	}
	sort.Strings(stale)
	if len(stale) > 0 {
		t.Errorf("%[1]s entries no longer registered — delete them:\n\t%[2]s\n%[1]s %[3]s.",
			name, strings.Join(stale, "\n\t"), rule)
	}
}

// TestModeledURIIndex_covers guards the half of the gate that can fail open.
//
// If the index over-matches, every route looks modeled and
// TestNoRouteIsRegisteredOutsideTheNamespace passes while proving nothing —
// the failure mode a gate is least likely to notice about itself, because a
// green test is what it looks like. So the invented cases below matter more
// than the modeled ones, and they are drawn from what the gate really found on
// its first run.
func TestModeledURIIndex_covers(t *testing.T) {
	index := newModeledURIIndex()

	modeled := []string{
		"/2015-03-31/functions/{name}/invocations",               // Lambda Invoke
		"/api/v2/clusters",                                       // MSK ListClustersV2
		"/v2/email/identities",                                   // SES v2
		"/2020-05-31/distributions/{id}/monitoring-subscription", // CloudFront, AWS's plural
		"/restapis/{restApiId}",                                  // API Gateway
	}
	for _, pattern := range modeled {
		if !index.covers(pattern) {
			t.Errorf("covers(%q) = false, want true — the manifest binds this path", pattern)
		}
	}

	invented := []string{
		"/_overcast/lambda/functions/{name}/source",             // web UI function editor
		"/_overcast/lambda/functions/{name}/test-events",        // web UI Test tab
		"/2020-05-31/distribution/{id}/monitoring-subscription", // singular: the redundant alias
		"/_overcast/eks/clusters/{name}/kubeconfig",             // EKS emulator extension
		"/{region}/{poolId}/.well-known/jwks.json",              // region-prefixed OIDC discovery
		"/nothing/aws/ever/modeled",
	}
	for _, pattern := range invented {
		if index.covers(pattern) {
			t.Errorf("covers(%q) = true, want false — no modeled URI binds this path", pattern)
		}
	}
}

// modeledURIIndex is every modeled URI, pre-split and bucketed by first path
// segment.
//
// The bucketing is what makes the gate affordable: the corpus is ~18,850
// operations and the router registers hundreds of routes, so the naive cross
// product is millions of segment comparisons. Keying on the first segment
// reduces almost every lookup to a handful. Routes whose own first segment is
// a label — SQS's queue URLs, Cognito's per-pool discovery — can match a URI
// under any key, so they get the full scan they need.
type modeledURIIndex struct {
	byFirstSegment map[string][][]string
	// parameterised holds URIs whose own first segment is a model label. No
	// literal key can index them, because they match any first segment.
	parameterised [][]string
	all           [][]string
}

func newModeledURIIndex() *modeledURIIndex {
	index := &modeledURIIndex{byFirstSegment: map[string][][]string{}}
	awsapi.WalkOperations(func(op awsapi.Operation) bool {
		if op.URI != "" {
			index.add(op.URI)
		}
		return true
	})
	return index
}

// add buckets one modeled URI the same way newModeledURIIndex does for the
// whole corpus. Factored out so newModeledURIIndexByService (routeownership_dev_test.go,
// #1227) builds one index per Overcast service key from the identical rule —
// a route-ownership gate that graded coverage by a second, slightly different
// definition of "modeled" would be worse than not having it.
func (m *modeledURIIndex) add(uri string) {
	segments := uriSegments(uri)
	m.all = append(m.all, segments)
	switch {
	case len(segments) < 2:
		m.parameterised = append(m.parameterised, segments)
	case isLabelSegment(segments[1]):
		m.parameterised = append(m.parameterised, segments)
	default:
		if m.byFirstSegment == nil {
			m.byFirstSegment = map[string][][]string{}
		}
		m.byFirstSegment[segments[1]] = append(m.byFirstSegment[segments[1]], segments)
	}
}

// covers reports whether any modeled URI is served by this route pattern.
func (m *modeledURIIndex) covers(pattern string) bool {
	segments := patternSegments(pattern)
	if len(segments) < 2 || isLabelSegment(segments[1]) {
		return anyCovered(segments, m.all)
	}
	return anyCovered(segments, m.byFirstSegment[segments[1]]) ||
		anyCovered(segments, m.parameterised)
}

func anyCovered(pattern []string, uris [][]string) bool {
	for _, uri := range uris {
		if patternCovers(pattern, uri) != coverageNone {
			return true
		}
	}
	return false
}

// isLabelSegment reports whether a path segment matches values rather than
// being one, in either notation: chi's "{name}" and "*", the models' "{Name}"
// and "{Name+}".
func isLabelSegment(s string) bool {
	return s == "*" || strings.HasPrefix(s, "{")
}
