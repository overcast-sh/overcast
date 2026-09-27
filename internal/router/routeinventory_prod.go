//go:build !dev

package router

import (
	"net/http"

	"github.com/go-chi/chi/v5"
)

// withDispatchMounts returns the mux unchanged in production builds, so a
// released binary serves requests through the bare chi mux exactly as before.
//
// New still records the mounts either way rather than branching on a build
// tag: it is eight appends once at startup, and one code path is worth more
// than that. Only the wrapper is dev-only.
func withDispatchMounts(r *chi.Mux, _ []dispatchMount, _ map[string]string, _ *pathDispatch) http.Handler {
	return r
}

// routeOwnerTracker is a no-op outside dev builds. A released binary calls
// attribute() once per enabled service exactly as the dev build does — New
// does not branch on the build tag to decide whether to call it, for the same
// reason withDispatchMounts above does not — but here that call does nothing:
// no chi.Walk, no map writes. Production routing pays literally nothing for
// an attribution no production code path ever reads. See routeinventory_dev.go
// for the dev build's chi.Walk-based diff.
type routeOwnerTracker struct{}

func newRouteOwnerTracker() *routeOwnerTracker { return &routeOwnerTracker{} }

func (t *routeOwnerTracker) attribute(*chi.Mux, string) {}

func (t *routeOwnerTracker) ownersByKey() map[string]string { return nil }
