package middleware_test

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"go.uber.org/zap"

	"github.com/overcast-sh/overcast/internal/clock"
	"github.com/overcast-sh/overcast/internal/config"
	"github.com/overcast-sh/overcast/internal/middleware"
	"github.com/overcast-sh/overcast/internal/router"
	"github.com/overcast-sh/overcast/internal/state"
)

// registeredRouteClassification records how detectService labels an *unsigned*
// request to every family of route the router registers. It is the committed
// answer to "which paths does the prefix switch actually cover", and the whole
// point of writing it down is that it cannot be read off the switch: the switch
// says what it claims, this says what the routing table needs.
//
// Unsigned is the case under test because it is the only one the prefix switch
// decides. A request carrying a SigV4 credential scope is classified from that
// scope at step 3b for everything the switch does not claim, which is why most
// of the entries below read "s3" without anything being broken — see the
// per-group notes. Asserting the signed case as well would mostly assert that
// serviceFromAuthCredential echoes its input.
//
// A family is a route's first path segment, or its first two when that segment
// is a shared version prefix (/v1, /v2). Values are the classification, with
// "|" joining the set when one family produces more than one.
//
// **When this test fails, the fix is not automatically to edit this map.**
// A new entry means a service registered routes on a path nothing classified
// before. Decide which of these it is:
//
//   - The route is served, and unsigned callers reach it (a browser, a webhook,
//     an SDK with no credentials). Then "s3" here is a real defect: the request
//     is labelled s3 in logs, and IAM enforcement authorises it as an `s3:`
//     action. Add the prefix to detectService.
//   - The route is only ever reached by signed AWS SDK traffic. Then "s3" is
//     the fall-through and the credential scope does the work. Record it here
//     with a note saying so, and change nothing else — but check the scope
//     really does the work, which is the sibling assertion in
//     detectservice_signed_test.go rather than anything this table can say. It
//     only works when the service's signing name maps to its service key, and
//     for MSK and AppRegistry it did not: "s3" was recorded here as the benign
//     fall-through while every signed request to those paths went unclassified
//     and therefore unauthorized.
//
// Changing an existing entry is the louder signal: a path that used to be
// classified as one service and is now another has moved which IAM action a
// policy is evaluated against.
var registeredRouteClassification = map[string]string{
	// S3's catch-alls. Everything unclaimed lands here by design; "/" and "/*"
	// are the restFallback registrations, not S3 routes as such.
	"/":            "s3",
	"/*":           "s3",
	"/favicon.ico": "s3",

	// Dated API-version prefixes, all claimed explicitly by detectService.
	// TestDetectServiceCoversModeledLambdaVersions guards Lambda's list against
	// a model refresh adding one; the other three services bind a single
	// version each.
	"/2013-04-01": "route53",
	"/2015-02-01": "efs",
	"/2015-03-31": "lambda",
	"/2017-03-31": "lambda",
	"/2017-10-31": "lambda",
	"/2018-10-31": "lambda",
	"/2019-09-25": "lambda",
	"/2019-09-30": "lambda",
	"/2020-04-22": "lambda",
	"/2020-05-31": "cloudfront",
	"/2020-06-30": "lambda",
	"/2021-01-01": "opensearch",
	"/2021-10-31": "lambda",
	"/2021-11-15": "lambda",
	"/2026-07-09": "lambda",

	// Emulator-internal paths. S3 bucket names cannot begin with "_", so these
	// never reach the S3 fallback; internalService maps the known owners and
	// calls the rest "internal".
	//
	// **There is one, and this is it.** Sixteen internal families collapsed
	// into "/_overcast" over docs/plans/non-canonical-url-namespace.md's
	// phases 2-5: phase 2 retired "/_", "/_internal", "/_health", "/_metrics",
	// "/_topology" and "/_events"; phase 3 took "/_mcp"; phase 4 took "/_ecs"
	// and "/_rds"; phase 5 took "/_apigateway", "/_appsync", "/_cloudfront",
	// "/_cognito", "/_elb", "/_lambda" and "/@connections".
	//
	// The single guarantee that makes the prefix safe — S3 bucket names cannot
	// begin with "_" — is now spent once rather than sixteen times, and a new
	// entry in this section is a namespace violation rather than a new
	// convention. TestNoRouteIsRegisteredOutsideTheNamespace enforces that
	// directly; this map is where it would show up as drift.
	//
	// It is multi-valued because the prefix is a directory with several owners
	// under it, not because anything is ambiguous: internalService reads the
	// owner from the second segment.
	"/_overcast": "appsync|athena|cloudfront|cognito|ecs|eks|events|internal|lambda|metrics|rds|secretsmanager|ses",

	// The two compatibility roots, underscore-prefixed but deliberately not
	// under "/_overcast". Their whole job is to answer at a URL somebody else's
	// tooling already hard-codes: /_health is where Overcast's own health
	// endpoint lived before phase 2, and /_localstack is LocalStack's
	// operational namespace — what a compose healthcheck or a Testcontainers
	// wait strategy carried over from LocalStack polls.
	//
	// A URL already written into a healthcheck cannot be migrated by renaming
	// ours, and a 404 on one is read by an orchestrator as a dead container: it
	// restarts it, and with the default in-memory backend a restart is a wipe.
	// So these two are served, and recorded as permanent exceptions in
	// router.nonManifestRoutes with the same reasoning — an addition here is
	// still a namespace violation unless it earns an entry there too.
	//
	// Both classify as "internal": internalService names no owner for them,
	// which is right, because they belong to the emulator rather than to a
	// service.
	"/_health":     "internal",
	"/_localstack": "internal",
	// The third compatibility root, LocalStack's /_aws/ inspection namespace:
	// what a test suite carried over from LocalStack reads to assert on the
	// emails SES captured or the messages sitting in a queue. Served for the
	// same reason as /_localstack above — the URL is already written into
	// someone's assertions — and recorded in router.nonManifestRoutes
	// likewise. "internal" rather than ses/sqs: the paths belong to the
	// compatibility layer, which translates into the owning service's store
	// but is not that service's API.
	"/_aws": "internal",

	// Undated literals the prefix switch claims. /applications is shared by
	// AppConfig and AppRegistry; unsigned it resolves to AppRegistry, which
	// owned it outright before #854 and is what the web UI calls.
	// MSK's v2 cluster API is the whole of /api: no other implemented service
	// registers a route under that root segment.
	"/api":          "msk",
	"/apikeys":      "apigateway",
	"/applications": "appregistry",
	// AWS Backup's three modeled subtrees, /backup-vaults, /backup/plans and
	// /backup-access-point (#1467). The second's family is "/backup", but
	// detectService claims the longer prefixes rather than that root: "backup"
	// is a legal S3 bucket name.
	"/backup":              "backup",
	"/backup-vaults":       "backup",
	"/backup-access-point": "backup",
	// Backup's UntagResource (#1195), POST /untag/{ResourceArn} — its own path,
	// not another member of the shared "/tags" family below. Unsigned it falls
	// to "s3" for the same reason "/tags" does: "untag" is a legal S3 bucket
	// name, and only signed Backup traffic ever reaches this route.
	"/untag": "s3",
	// AppConfig Data's two modeled bindings. detectService claims both
	// prefixes explicitly: they are root-level paths with no version
	// segment, so nothing else identifies them before the credential scope.
	"/configuration":         "appconfigdata",
	"/configurationsessions": "appconfigdata",
	// Bedrock Runtime's inference bindings. Claimed by the whole path shape
	// rather than by the "/model" prefix, because that segment is also a legal
	// S3 bucket name — see isBedrockRuntimeInferencePath.
	"/model":    "bedrock",
	"/restapis": "apigateway",
	// EventBridge Scheduler serves both of these to unsigned callers as well as
	// signed ones, and its own tag operations are reached through the shared
	// /tags dispatcher rather than a prefix of its own.
	"/schedule-groups": "scheduler",
	"/schedules":       "scheduler",
	"/usageplans":      "apigateway",
	"/v1/apis":         "appsync",
	"/v1/domainnames":  "appsync",
	// AppSync's two API-independent evaluation endpoints (#860). They are
	// claimed by the prefix switch rather than left to the credential scope
	// because an unsigned POST to either is a plausible request — neither
	// needs an API or any account state — and the fall-through would label it,
	// and IAM-authorise it, as an s3 write to a bucket named v1.
	"/v1/dataplane-evaluatecode":     "appsync",
	"/v1/dataplane-evaluatetemplate": "appsync",
	"/v1/mergedApis":                 "appsync",
	"/v1/pipes":                      "pipes",
	"/v1/sourceApis":                 "appsync",
	"/v1/tags":                       "appsync",
	"/v2/email":                      "ses",

	// /v2/apis is shared by API Gateway v2 and AppSync, and is the one place the
	// switch already consults the credential scope before answering. Unsigned it
	// resolves to API Gateway.
	"/v2/apis": "apigateway",

	// Everything below is served by a real route but classified from the SigV4
	// credential scope, not from the path — "s3" is what an unsigned request
	// gets, and every AWS SDK signs. Adding prefixes for them would not be free:
	// each of these first segments is also a legal S3 bucket name, and the
	// prefix switch runs ahead of the credential scope, so claiming one takes
	// the path away from a genuine S3 request to a bucket of that name.
	//
	//   API Gateway: /account, /domainnames, /tags, /vpclinks, and their /v2
	//   forms. The WebSocket management API used to be listed here too, at
	//   /@connections; phase 5 moved it to
	//   /_overcast/apigateway/connections, so it is inside the namespace now
	//   and covered by the "/_overcast" family above rather than by a root
	//   segment of its own.
	"/account":        "s3",
	"/domainnames":    "s3",
	"/tags":           "s3",
	"/vpclinks":       "s3",
	"/v2/domainnames": "s3",
	"/v2/tags":        "s3",
	"/v2/vpclinks":    "s3",
	//   App Registry: /applications is claimed above, its sibling is not.
	"/attribute-groups": "s3",
	//   EKS, which is why the credential scope has to stay the primary signal:
	//   it is fully served with no prefix entry at all.
	"/access-policies":  "s3",
	"/addons":           "s3",
	"/cluster-versions": "s3",
	"/clusters":         "s3",
	//   MSK's v1 surface. Its v2 surface is claimed above, under "/api".
	"/v1/clusters":       "s3",
	"/v1/configurations": "s3",
	"/v1/kafka-versions": "s3",
	//   S3 Tables. The router itself dispatches these on the credential scope
	//   (router.go's signingNameDispatch): unsigned and S3-signed traffic
	//   reaches S3's bucket named for the root, which is what "s3" says.
	"/buckets":                            "s3",
	"/get-table":                          "s3",
	"/namespaces":                         "s3",
	"/replication-status":                 "s3",
	"/table-bucket-replication":           "s3",
	"/table-record-expiration":            "s3",
	"/table-record-expiration-job-status": "s3",
	"/table-replication":                  "s3",
	"/tables":                             "s3",
	"/tag":                                "s3",
	//   Smithy RPC v2 (/service/{service}/operation/{operation}); the dispatcher
	//   reads the service from the path itself and delegates to S3 without a
	//   Smithy-Protocol header, so the s3 label matches what happens.
	"/service": "s3",
	//   Label-rooted routes: SQS's path-style queue URLs and Cognito's per-pool
	//   OIDC discovery documents, the latter fetched unsigned by JWT libraries.
	//
	//   "s3" is the fall-through here and costs nothing, but for a sharper
	//   reason than elsewhere in this section — these two are not merely
	//   *undistinguished* from a bucket path, they are undistinguishable by
	//   this switch and distinguishable by their own shape. An account ID is
	//   all digits; a pool ID is "{region}_{suffix}", and an S3 bucket name can
	//   be neither, because it may not contain an underscore and a numeric
	//   bucket name would still be a legal bucket. The router relies on that
	//   disjointness rather than on precedence, and
	//   cognito.TestOIDCDiscoveryPathCannotCollideWithS3OrSQS pins it.
	"/{accountID:[0-9]+}": "s3",
	"/{poolId}":           "s3",
}

// buildTagGatedRouteFamilies are families registered only in some build
// configurations, so their absence is not drift.
//
// Empty since phase 3 of docs/plans/non-canonical-url-namespace.md. Its only
// entry was "/_mcp", a !slim route (mcp_routes.go / mcp_routes_slim.go) whose
// whole family vanished under -tags slim. MCP now lives at /_overcast/mcp, and
// "/_overcast" is registered in every configuration — init, ca.pem and the
// per-service admin routes are not build-tagged — so a slim build no longer
// loses a family, only some routes within one. The map stays for the next
// family that is genuinely build-gated.
var buildTagGatedRouteFamilies = map[string]bool{}

// TestDetectServiceClassifiesEveryRegisteredRouteFamily walks the real router
// and checks every route family against the table above.
//
// detectService's prefix switch is hand-written, and this is what stops that
// being silent. A service added with a new path root, or an existing service
// gaining an API version, changes the routing table without changing the
// switch — and nothing about that is visible until someone reads a log line
// labelled s3 for a request that plainly is not S3, or an IAM policy denies an
// `s3:` action nobody wrote. Enumerating the routes turns that into a failure
// at the commit that introduces it.
func TestDetectServiceClassifiesEveryRegisteredRouteFamily(t *testing.T) {
	// Given: every route family the router registers, classified unsigned.
	got := classifyRegisteredRouteFamilies(t)
	if len(got) == 0 {
		t.Fatal("walked no routes")
	}

	// When: each is compared with the committed classification.
	var unexpected, changed, missing []string
	for _, family := range sortedFamilies(got) {
		want, recorded := registeredRouteClassification[family]
		switch {
		case !recorded:
			unexpected = append(unexpected, family+" -> "+got[family])
		case got[family] != want:
			changed = append(changed, family+": was "+want+", now "+got[family])
		}
	}
	for family := range registeredRouteClassification {
		if _, found := got[family]; !found && !buildTagGatedRouteFamilies[family] {
			missing = append(missing, family)
		}
	}
	sort.Strings(missing)

	// Then: the switch and the routing table still agree about every path.
	if len(unexpected) > 0 {
		t.Errorf("routes registered on paths nothing classifies:\n\t%s\n"+
			"Read registeredRouteClassification's doc comment before adding these to it.",
			strings.Join(unexpected, "\n\t"))
	}
	if len(changed) > 0 {
		t.Errorf("classification moved, which moves the IAM action these paths are authorised as:\n\t%s",
			strings.Join(changed, "\n\t"))
	}
	if len(missing) > 0 {
		t.Errorf("recorded families no longer registered — delete them, or add them to buildTagGatedRouteFamilies:\n\t%s",
			strings.Join(missing, "\n\t"))
	}
}

// newTestRouterRoutes builds the real router and returns it as something
// chi.Walk can enumerate. Shared with the signed classification guard in
// detectservice_signed_test.go, which walks the same routing table.
func newTestRouterRoutes(t *testing.T) chi.Routes {
	t.Helper()
	cfg := &config.Config{
		Host:      "127.0.0.1",
		Port:      0,
		Region:    "us-east-1",
		AccountID: "000000000000",
		State:     config.StateBackendMemory,
		LogLevel:  "error",
	}
	handler, preShutdown, cleanup, _ := router.New(cfg, state.NewMemoryStore(), zap.NewNop(), clock.New())
	t.Cleanup(func() {
		preShutdown()
		cleanup(t.Context())
	})
	routes, ok := handler.(chi.Routes)
	if !ok {
		t.Fatal("router.New did not return a chi.Routes")
	}
	return routes
}

func classifyRegisteredRouteFamilies(t *testing.T) map[string]string {
	t.Helper()
	routes := newTestRouterRoutes(t)

	seen := map[string]map[string]bool{}
	if err := chi.Walk(routes, func(_, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		// detectService reads the path, not the method, so one probe per route
		// is enough and GET is as good as any.
		request := httptest.NewRequest(http.MethodGet, concreteRoutePath(route), nil)
		family := routeFamily(route)
		if seen[family] == nil {
			seen[family] = map[string]bool{}
		}
		seen[family][middleware.DetectServiceForTest(request)] = true
		return nil
	}); err != nil {
		t.Fatalf("walking the router: %v", err)
	}

	classification := make(map[string]string, len(seen))
	for family, services := range seen {
		names := make([]string, 0, len(services))
		for name := range services {
			names = append(names, name)
		}
		sort.Strings(names)
		classification[family] = strings.Join(names, "|")
	}
	return classification
}

func sortedFamilies(classification map[string]string) []string {
	families := make([]string, 0, len(classification))
	for family := range classification {
		families = append(families, family)
	}
	sort.Strings(families)
	return families
}

var routeLabel = regexp.MustCompile(`\{[^}]*\}`)

// concreteRoutePath turns a chi route pattern into a request path: labels and
// wildcards become an ordinary segment, which is all detectService needs since
// it matches prefixes rather than parsing the path.
func concreteRoutePath(route string) string {
	path := strings.ReplaceAll(routeLabel.ReplaceAllString(route, "x"), "*", "x")
	if path == "" {
		return "/"
	}
	return path
}

// routeFamily reduces a route to the unit detectService actually decides on:
// its first path segment, or its first two where the first is a version prefix
// several services share.
func routeFamily(route string) string {
	segments := strings.SplitN(strings.TrimPrefix(route, "/"), "/", 3)
	family := "/" + segments[0]
	if (family == "/v1" || family == "/v2") && len(segments) > 1 {
		family += "/" + segments[1]
	}
	return family
}
