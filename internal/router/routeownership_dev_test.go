//go:build dev

package router

import (
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/overcast-sh/overcast/internal/awsapi"
)

// This file is the axis #1227 files against #864's route-side gates.
//
// TestNoRouteIsRegisteredOutsideTheNamespace (pathnamespace_dev_test.go, #921)
// walks route -> model and asks whether every registered path is *some*
// modeled URI, with modeledURIIndex built from every service's operations at
// once. TestModeledBindings_areServedWhereAWSBindsThem (modelbinding_dev_test.go,
// #876) walks model -> route for the declaring service's own bindings: is a
// route registered where AWS binds an operation this service claims?
//
// Neither asks the question in between: does the service that registered this
// route model this path? A route registered by service X at a path only
// service Y models passes both existing gates, because modeledURIIndex there
// carries no service filter and the model -> route direction never looks past
// its own declaring service. That is #854's worst symptom restated as a
// routing-table fact: AppRegistry registered r.Route("/applications", …), a
// chi sub-router owns its whole subtree, and AppConfig's own requests answered
// from AppRegistry's router — four of them came back a Service Catalog
// resource with a 200.
//
// routeOwnerTracker (routeinventory_dev.go) attributes every pattern
// registered directly on the shared mux to the service whose RegisterRoutes
// call produced it, the other half of the attribution dispatchMount already
// keeps for the four runtime-dispatched sub-routers. This test asserts that
// attribution against the model, one service at a time.
func TestRegisteredRouteBelongsToAModelingOwner(t *testing.T) {
	// Given: every route the router registers, attributed to whichever
	// service registered it — a dispatched sub-router's Owner, or a direct
	// mux registration's DirectOwner — and the model bucketed per service so
	// a route can be checked against only its own registrant.
	routes := inventory(t)
	byService := newModeledURIIndexByService()

	var faults []ownershipFault
	judged := map[string]bool{}
	for _, route := range routes.routes {
		// A dispatch stub only chooses a sub-router at request time (the "/*"
		// and "/" chi entries under /v2/apis, /applications, /v1/tags and
		// /tags); the real routes are the mount's own, walked separately with
		// their Owner already set. Judging the stub itself would ask whether
		// e.g. "appconfig" models "/applications/*", which no service does by
		// design — the wildcard is the main router's, not a service's.
		if routes.dispatchStubs[route.Pattern] {
			continue
		}
		// A service's own /_overcast/<service>/... extension is real and
		// correctly attributed — eks really did register
		// /_overcast/eks/clusters/{name}/kubeconfig — but it is not an AWS
		// operation, so "does eks model this path" is the wrong question.
		// TestNoRouteIsRegisteredOutsideTheNamespace already holds the whole
		// namespace to its own rule; this gate is only about the manifest.
		if isInternalNamespace(route.Pattern) {
			continue
		}
		// nonManifestRoutes (pathnamespace_dev_test.go) is the existing,
		// reviewed list of paths no AWS model can ever describe — LocalStack's
		// execute-api compatibility shape, Cognito's OIDC discovery documents,
		// SQS's path-style queue URL. Each is still registered by a real
		// service (apigateway, cognito, sqs), so without this check every one
		// of them would report as that service registering a path it does not
		// model, which is true and not a fault: nothing modeled it because
		// nothing could. Reusing the list rather than a second one is the
		// point of extending the existing gate mechanism instead of growing a
		// parallel exemption table.
		if _, permanent := nonManifestRoutes[route.Pattern]; permanent {
			continue
		}

		owner := ownerOf(route)
		if owner == "" {
			// Not attributed to any service: router.go registers this pattern
			// itself (the protocol roots and /favicon.ico), which
			// TestNoRouteIsRegisteredOutsideTheNamespace already holds to the
			// manifest via nonManifestRoutes — or routeOwnerTracker failed to
			// attribute a route a service really did register, which is a gap
			// in the tracker rather than a legitimate exception and must fail
			// loudly rather than be silently skipped, exactly how #854 stayed
			// hidden.
			if unattributedRoutes[route.Pattern] {
				continue
			}
			faults = append(faults, ownershipFault{owner: "(unattributed)", method: route.Method, pattern: route.Pattern})
			continue
		}

		key := owner + " " + route.Method + " " + route.Pattern
		if judged[key] {
			continue
		}
		judged[key] = true

		index, ok := byService[owner]
		if !ok || !index.covers(route.Pattern) {
			faults = append(faults, ownershipFault{owner: owner, method: route.Method, pattern: route.Pattern})
		}
	}

	// Then: only the faults the ledger already accounts for are present, and
	// every ledger entry is still needed.
	assertRouteOwnershipLedger(t, faults)
}

// ownerOf resolves the single service a route is attributed to, preferring
// the dispatch-mount Owner (set for the four shared prefixes) and falling
// back to the direct-registration DirectOwner routeOwnerTracker records for
// everything else. The two are never both set for the same route: Owner
// covers a dispatched sub-router's own routes, and DirectOwner only exists for
// the main-mux pass walkRegisteredRoutes performs separately from those
// mounts.
func ownerOf(route registeredRoute) string {
	if route.Owner != "" {
		return route.Owner
	}
	return route.DirectOwner
}

// unattributedRoutes are the exact patterns router.go registers itself rather
// than any service — the protocol roots TestNoRouteIsRegisteredOutsideTheNamespace
// records in nonManifestRoutes as permanent exceptions to "every route is
// modeled". routeOwnerTracker has no service to credit them to because no
// Service.RegisterRoutes call produced them; that gate already holds them to
// the manifest (or explains why nothing can). This one only asks whether a
// route's registering SERVICE models the path, so a route with no registering
// service is out of its scope, not a pass.
var unattributedRoutes = map[string]bool{
	"/favicon.ico": true,
	"/":            true,
	"/*":           true,
	"/service/{service}/operation/{operation}": true,
	// S3 Tables' Iceberg REST catalog: router.go registers the root and
	// dispatches it on the signing name, like the S3 Tables roots, but no
	// model binds it, so there is no modeling owner to attribute it to.
	"/iceberg/*": true,
}

// isInternalNamespace reports whether pattern is one of router.go's own
// /_overcast/* endpoints, checked by prefix — like
// TestNoRouteIsRegisteredOutsideTheNamespace does — rather than enumerated, so
// a new endpoint under the reserved namespace needs no entry in either gate.
func isInternalNamespace(pattern string) bool { return strings.HasPrefix(pattern, internalPrefix) }

// newModeledURIIndexByService groups the modeled corpus per Overcast service
// key, reusing modeledURIIndex.add so a route's registrant is checked against
// exactly the same "does this URI cover that pattern" rule
// TestNoRouteIsRegisteredOutsideTheNamespace already uses — this gate adds a
// service filter to that rule, not a second definition of it.
func newModeledURIIndexByService() map[string]*modeledURIIndex {
	byService := map[string]*modeledURIIndex{}
	awsapi.WalkOperations(func(op awsapi.Operation) bool {
		if op.URI == "" {
			return true
		}
		key := awsapi.ServiceKey(op.Service)
		index, ok := byService[key]
		if !ok {
			index = &modeledURIIndex{byFirstSegment: map[string][][]string{}}
			byService[key] = index
		}
		index.add(op.URI)
		return true
	})
	return byService
}

// TestModeledURIIndexByService_isPartitionedPerService guards the failure
// mode this gate cannot notice about itself: if the per-service partition
// collapsed into one shared bucket — a wrong key, a map never reset between
// services, ServiceKey resolving everything to the same string — every route
// would appear to cover every service's index and
// TestRegisteredRouteBelongsToAModelingOwner would pass having proven
// nothing, exactly the risk TestModeledURIIndex_covers already guards for the
// unpartitioned index (pathnamespace_dev_test.go).
//
// The literal case is #1227's own motivating example: Lambda's Invoke binding
// is modeled for lambda and for no other enabled service, so eks's index must
// not cover it even though lambda's does.
func TestModeledURIIndexByService_isPartitionedPerService(t *testing.T) {
	byService := newModeledURIIndexByService()

	// Chosen from services that carry REST (@http-traited) bindings, unlike
	// DynamoDB, SQS or Cognito, whose operations dispatch on X-Amz-Target and
	// the manifest's URI field is empty for every one of them — they
	// legitimately have no bucket here, and asserting one for them would be
	// the wrong literal.
	for _, key := range []string{"lambda", "s3", "eks", "apigateway", "cloudfront"} {
		if _, ok := byService[key]; !ok {
			t.Errorf("newModeledURIIndexByService()[%q] missing — WalkOperations found no REST-bound operation for an established service key expected to have one", key)
		}
	}
	if _, ok := byService["nosuchservice"]; ok {
		t.Errorf(`newModeledURIIndexByService()["nosuchservice"] exists — ServiceKey resolved an invented name to a real bucket`)
	}

	const lambdaInvoke = "/2015-03-31/functions/{FunctionName}/invocations"
	if !byService["lambda"].covers(lambdaInvoke) {
		t.Errorf("byService[lambda].covers(%q) = false, want true — this is Lambda's own modeled Invoke binding", lambdaInvoke)
	}
	if byService["eks"].covers(lambdaInvoke) {
		t.Errorf("byService[eks].covers(%q) = true, want false — eks models no such path; the partition is not filtering by service", lambdaInvoke)
	}
	if byService["s3"].covers(lambdaInvoke) {
		t.Errorf("byService[s3].covers(%q) = true, want false — s3 models no such path; the partition is not filtering by service", lambdaInvoke)
	}
}

// ownershipFault is one route whose attributed owner does not model the path
// it registered — #854's fault class, named for the ledger and the report.
type ownershipFault struct {
	owner   string
	method  string
	pattern string
}

func (f ownershipFault) key() string { return f.owner + " " + f.method + " " + f.pattern }

func (f ownershipFault) String() string {
	if f.owner == "(unattributed)" {
		return fmt.Sprintf("%s %s: registered directly on the router with no attributable owner "+
			"(neither a dispatchMount nor routeOwnerTracker names one) — if this is a router-owned "+
			"endpoint rather than a service's, add it to unattributedRoutes", f.method, f.pattern)
	}
	return fmt.Sprintf("%s registered %s %s, which %[1]s does not model", f.owner, f.method, f.pattern)
}

// routeOwnershipViolations is the ratchet: every route whose registering
// service does not model the path, recorded so the gate can be enforced from
// the commit that introduces it rather than the commit that finishes fixing
// it. Same rule as unservedBindings (modelbinding_ledger_dev_test.go): an
// unrecorded fault fails the build, and so does a recorded one that has been
// fixed. Every entry names a filed issue.
var routeOwnershipViolations = map[string]string{}

// assertRouteOwnershipLedger holds the observed faults to
// routeOwnershipViolations in both directions.
func assertRouteOwnershipLedger(t *testing.T, faults []ownershipFault) {
	t.Helper()

	var unrecorded []string
	seen := map[string]bool{}
	for key := range routeOwnershipViolations {
		seen[key] = true
	}
	for _, fault := range faults {
		if _, recorded := routeOwnershipViolations[fault.key()]; recorded {
			delete(seen, fault.key())
			continue
		}
		unrecorded = append(unrecorded, fault.String())
	}
	sort.Strings(unrecorded)

	stale := make([]string, 0, len(seen))
	for key := range seen {
		stale = append(stale, key+" ("+routeOwnershipViolations[key]+")")
	}
	sort.Strings(stale)

	if len(unrecorded) > 0 {
		t.Errorf("%d route(s) registered by a service that does not model the path:\n\t%s\n\n"+
			"This is #854's fault class: a route answering for a service nobody asked. Move the route "+
			"to where its registering service actually models it, dispatch it through the right owner, "+
			"or — if the fault is pre-existing and owned by a filed issue — add it to "+
			"routeOwnershipViolations with that issue. Never widen an allow-list to make this pass.",
			len(unrecorded), strings.Join(unrecorded, "\n\t"))
	}
	if len(stale) > 0 {
		t.Errorf("%d routeOwnershipViolations entr(ies) no longer reproduce — delete them:\n\t%s",
			len(stale), strings.Join(stale, "\n\t"))
	}
}
