//go:build dev

package router

import (
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
)

// inspectableMux is the router plus the sub-routers it dispatches to at
// request time. It embeds *chi.Mux, so it serves and routes exactly as the
// bare mux does and still satisfies chi.Routes for callers that walk it.
type inspectableMux struct {
	*chi.Mux
	mounts []dispatchMount
	// owners is routeOwnerTracker's output: "METHOD pattern" -> the service
	// whose RegisterRoutes call registered it directly on this mux. See
	// routeOwnerTracker in this file for why dispatched sub-routers do not
	// need it (dispatchMount.Owner already names them).
	owners map[string]string
	// paths is the dispatch RouteREST resolves requests through, so a test
	// can check it against the routes the mux actually serves.
	paths *pathDispatch
}

// withDispatchMounts attaches the recorded mounts, direct-registration
// ownership and path dispatch in dev builds so walkRegisteredRoutes can see
// the whole served surface and who registered each part of it.
func withDispatchMounts(r *chi.Mux, mounts []dispatchMount, owners map[string]string, paths *pathDispatch) http.Handler {
	return &inspectableMux{Mux: r, mounts: mounts, owners: owners, paths: paths}
}

// registeredRoute is one method/pattern pair the router serves, attributed to
// the sub-router that registered it where the main router owns a shared path
// space on a service's behalf.
type registeredRoute struct {
	Method  string
	Pattern string
	// Owner names the service package for a route registered on a dispatched
	// sub-router, and is empty for a route registered directly on the mux.
	//
	// This field is read by the gates that predate #1227
	// (modelbinding_dev_test.go, pathnamespace_dev_test.go) and its meaning is
	// unchanged by DirectOwner below: leaving it alone is what keeps those
	// gates' pass/fail behavior identical to before this file existed.
	Owner string
	// Fallback is set when the sub-router answers for any service the
	// dispatcher cannot attribute, not only for Owner.
	Fallback bool
	// DirectOwner names the service whose RegisterRoutes(r) call registered
	// this pattern directly on the shared mux — routeOwnerTracker's
	// attribution, populated only for the main-mux pass in
	// walkRegisteredRoutes. It is empty for a route reached through a
	// dispatchMount (Owner already answers the question there) and for a
	// pattern router.go registers itself rather than a service (the protocol
	// roots and /_overcast/* endpoints).
	//
	// #1227's route-ownership gate is the only reader. It is a separate field
	// rather than a second use of Owner so that gate cannot change what
	// Owner means to the gates that already read it.
	DirectOwner string
}

// walkRegisteredRoutes visits every route the handler serves: the main mux,
// then each dispatched sub-router with its mount prefix applied.
//
// A route reached only through a dispatcher is walked once per owner, because
// two services really do register different operations at the same pattern
// under /v2/apis and /tags — reporting the pattern alone would lose which
// service answers it.
func walkRegisteredRoutes(h http.Handler) ([]registeredRoute, error) {
	mux, ok := h.(*inspectableMux)
	if !ok {
		return nil, errNotInspectable
	}

	var out []registeredRoute
	collect := func(mount dispatchMount, direct bool) error {
		return chi.Walk(mount.Routes, func(method, pattern string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
			route := registeredRoute{
				Method:   method,
				Pattern:  joinRoutePattern(mount.Prefix, pattern),
				Owner:    mount.Owner,
				Fallback: mount.Fallback,
			}
			if direct {
				route.DirectOwner = mux.owners[method+" "+pattern]
			}
			out = append(out, route)
			return nil
		})
	}
	if err := collect(dispatchMount{Routes: mux.Mux}, true); err != nil {
		return nil, err
	}
	for _, mount := range mux.mounts {
		if err := collect(mount, false); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// joinRoutePattern prefixes a sub-router pattern with its mount path, keeping
// the result in the same shape chi.Walk reports for a directly registered
// route so both can be compared against a modeled URI without a second rule.
func joinRoutePattern(prefix, pattern string) string {
	joined := prefix + pattern
	if joined == "" {
		return "/"
	}
	if joined != "/" {
		joined = strings.TrimSuffix(joined, "/")
	}
	return joined
}

// errNotInspectable reports a handler that did not come from New, so the
// caller says "build it with New" rather than reading an empty inventory as a
// router that serves nothing.
var errNotInspectable = errors.New("router handler was not built by New; the dispatched sub-routers are not recorded")

// routeOwnerTracker attributes each pattern registered directly on the shared
// mux to the service whose RegisterRoutes call produced it — #1227's half of
// the attribution dispatchMount already provides for the four sub-routers a
// runtime dispatcher reaches. New calls attribute once per service,
// immediately after that service's RegisterRoutes(r), and a pattern is
// credited to the first service that registers it.
//
// Crediting the first registrant rather than erroring on a second one is
// deliberate: two services racing for the same direct-mux pattern, with the
// second silently winning, is a real shape in this router (chi resolves it by
// registration order) and the route-ownership gate this feeds is exactly what
// should report that as a fault — recording only the first owner is what
// makes the second registration invisible to chi.Walk and therefore visible
// to the gate as "nobody registered this for chi's actual owner".
type routeOwnerTracker struct {
	seen   map[string]bool
	owners map[string]string
}

func newRouteOwnerTracker() *routeOwnerTracker {
	return &routeOwnerTracker{seen: map[string]bool{}, owners: map[string]string{}}
}

// attribute walks r's currently-registered routes and credits every pattern
// not already seen to owner. Called once per service per New, so its cost is
// bounded by (service count) x (routes registered so far) — a few hundred
// routes and a few dozen services, not a number that has ever needed
// measuring against the rest of New's startup work.
func (t *routeOwnerTracker) attribute(r *chi.Mux, owner string) {
	_ = chi.Walk(r, func(method, pattern string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		key := method + " " + pattern
		if t.seen[key] {
			return nil
		}
		t.seen[key] = true
		t.owners[key] = owner
		return nil
	})
}

// ownersByKey returns the accumulated "METHOD pattern" -> owner map for
// withDispatchMounts to attach to the inspectableMux.
func (t *routeOwnerTracker) ownersByKey() map[string]string { return t.owners }
