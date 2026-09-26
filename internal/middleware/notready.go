package middleware

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/overcast-sh/overcast/internal/protocol"
	"github.com/overcast-sh/overcast/internal/state"
)

// notReadyRetryAfterSeconds is the Retry-After hint sent with a 503 response
// while the storage backend is still migrating. Deliberately short — the
// migration itself is what determines actual wait time, this is just a
// reasonable poll interval for a well-behaved client that honours the
// header rather than relying solely on its SDK's own retry/backoff policy.
const notReadyRetryAfterSeconds = 2

// InternalPrefix is the one path prefix reserved for endpoints Overcast serves
// on its own behalf rather than on AWS's — health, metrics, debug state, the
// event stream, per-service admin APIs, and the data plane of emulated
// workloads.
//
// It is reserved by an S3 naming rule rather than by convention: a bucket name
// cannot begin with an underscore, so no request AWS models can ever collide
// with it. That guarantee is worth spending on exactly one prefix, which is
// the whole argument for having one. Sixteen roots collapsed into it over
// docs/plans/non-canonical-url-namespace.md's phases 2-6.
//
// It lives in middleware rather than router because the dependency runs one
// way: router imports middleware, so this is the package both can share.
//
// Every path Overcast serves is either a binding the pinned manifest models or
// starts here, enforced by
// router.TestNoRouteIsRegisteredOutsideTheNamespace.
const InternalPrefix = "/_overcast/"

// LegacyHealthPath and LocalStackPrefix are the two compatibility roots that
// deliberately sit outside InternalPrefix, because answering at a URL someone
// else's tooling already hard-codes is the entire point of them.
//
// LegacyHealthPath is where Overcast's own health endpoint lived until
// docs/plans/non-canonical-url-namespace.md phase 2 moved it; LocalStackPrefix
// is the namespace LocalStack serves its operational endpoints under, and so
// what a compose healthcheck or a Testcontainers wait strategy carried over
// from LocalStack polls. A 404 on either is indistinguishable from a dead
// container to an orchestrator, which restarts it — and with the default
// in-memory state backend, a restart mid-deploy takes every stack the deploy
// had created with it.
//
// Both are covered by the same S3 naming rule InternalPrefix relies on: a
// bucket name cannot begin with an underscore, so neither can shadow a request
// AWS models. See router.nonManifestRoutes for the routing-side record.
const (
	LegacyHealthPath = "/_health"
	LocalStackPrefix = "/_localstack/"
)

// LocalStackHealthPath is the one path inside LocalStackPrefix Overcast
// answers rather than points elsewhere.
const LocalStackHealthPath = LocalStackPrefix + "health"

// AWSCompatPrefix is the third compatibility root, and the other half of
// LocalStack's namespace. /_localstack/ is what a healthcheck or an
// init-script poll hits; /_aws/ is what a *test suite* hits, to read back the
// side effects the AWS API itself gives no way to observe — the emails SES
// "sent", the messages sitting in a queue. `curl localhost:4566/_aws/ses` is
// written into one assertion per test that sends an email, which is why it is
// served rather than documented. Same underscore guarantee as the two roots
// above; see router/aws_compat.go for what is served under it.
const AWSCompatPrefix = "/_aws/"

// NotReady rejects a request with a 503 while the storage backend is still
// completing a one-time startup migration (see internal/state/migrate.go),
// instead of letting the request observe whatever the store would otherwise
// do during that window: persistent mode blocks the request indefinitely
// inside ensureReady, and hybrid mode's TierHot reads silently return "not
// found" for data that exists once migration finishes, because the
// post-migration seed hasn't populated memory yet (see
// state.NotReadyReporter and HybridStore.NotReady for the precise window
// this covers).
//
// Internal Overcast endpoints — any path starting with "/_" — are exempt, so
// operators can still check status, inspect debug state, or poll init-hook
// progress while a migration is in flight. No real AWS API request path starts
// with "/_", because an S3 bucket name cannot.
//
// store is checked once per request via a non-blocking type assertion to
// state.NotReadyReporter — stores that don't implement it (MemoryStore,
// WALStore) are always treated as ready, the same convention
// state.ReadyAwaiter already uses.
func NotReady(store state.Store) func(http.Handler) http.Handler {
	reporter, ok := store.(state.NotReadyReporter)
	if !ok {
		// This store type never has a "still starting up" window (e.g.
		// MemoryStore) — skip the per-request type assertion entirely
		// rather than repeating a check that can never succeed.
		return func(next http.Handler) http.Handler { return next }
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if isInternalPath(r.URL.Path) || !reporter.NotReady() {
				next.ServeHTTP(w, r)
				return
			}
			w.Header().Set("Retry-After", strconv.Itoa(notReadyRetryAfterSeconds))
			writeUnroutedError(w, r, protocol.ErrStorageMigrating)
		})
	}
}

// isInternalPath reports whether path is one of Overcast's own operational
// endpoints rather than an AWS API request, so operators can still check
// status and poll init-hook progress while a migration is in flight.
//
// It was the broad "/_" test through phases 2-5, because narrowing it to
// InternalPrefix while routes were still outside the namespace would have
// started gating them mid-migration. Phase 6 moved the last of them, so the
// namespace and the set are the same thing again — plus the two compatibility
// roots, which are health probes wearing an older URL and have to be exempt
// for the same reason /_overcast/health is: an orchestrator that reads 503 as
// "unhealthy" restarts the container, and restarting it during a migration is
// the one thing that turns a slow start into a lost one.
func isInternalPath(path string) bool {
	return strings.HasPrefix(path, InternalPrefix) ||
		path == LegacyHealthPath ||
		strings.HasPrefix(path, LocalStackPrefix) ||
		strings.HasPrefix(path, AWSCompatPrefix)
}
