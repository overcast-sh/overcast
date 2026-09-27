//go:build dev

package router

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"go.uber.org/zap"

	"github.com/overcast-sh/overcast/internal/clock"
	"github.com/overcast-sh/overcast/internal/config"
	"github.com/overcast-sh/overcast/internal/middleware"
	"github.com/overcast-sh/overcast/internal/state"
)

// reachesRESTFallback reports whether h can hand a request to the REST
// fallback or to S3's router, following the router's own dispatcher types.
func reachesRESTFallback(h http.Handler) bool {
	switch x := h.(type) {
	case *restFallback, s3Direct, delegatedRouter:
		// A delegated sub-router hands every path it does not match to the
		// fallback.
		return true
	case wholePath:
		return reachesRESTFallback(x.Handler)
	case s3First:
		return reachesRESTFallback(x.dispatch) || reachesRESTFallback(x.s3)
	case signingNameDispatch:
		return reachesRESTFallback(x.owner) || reachesRESTFallback(x.fallback)
	case tagsDispatch:
		return reachesRESTFallback(x.fallback)
	case smithyRPCRoute:
		return reachesRESTFallback(x.rpc) || reachesRESTFallback(x.s3)
	}
	return false
}

// TestPathDispatch_everyRouteReachingTheFallbackIsResolved guards #2271
// against drift. IAM enforcement names a request the REST fallback serves
// only if RouteREST can resolve it, and RouteREST resolves only the handlers
// registered through pathDispatch. A dispatcher that reaches the fallback but
// is registered on the mux directly would let a request's credential scope
// and query string name its IAM action again.
func TestPathDispatch_everyRouteReachingTheFallbackIsResolved(t *testing.T) {
	// Given: the router with every service registered
	mux := newInspectableRouter(t)

	// When: every route it serves is walked
	resolved := 0
	err := chi.Walk(mux.Mux, func(method, pattern string, h http.Handler, _ ...func(http.Handler) http.Handler) error {
		if !reachesRESTFallback(h) {
			return nil
		}
		// Then: each route that can reach the fallback is one RouteREST
		// resolves through
		_, forMethod := mux.paths.points[routeKey{method, pattern}]
		_, forAll := mux.paths.points[routeKey{"", pattern}]
		if !forMethod && !forAll {
			t.Errorf("%s %s reaches the REST fallback through %T, which pathDispatch does not record; register it with pathDispatch.handle or mount", method, pattern, h)
		}
		resolved++
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	if resolved == 0 {
		t.Fatal("walked no route that reaches the REST fallback; the walk or the dispatcher types changed")
	}
}

// newInspectableRouter is New with every service registered.
func newInspectableRouter(t *testing.T) *inspectableMux {
	t.Helper()
	cfg := &config.Config{
		Host:      "127.0.0.1",
		Region:    "us-east-1",
		AccountID: "000000000000",
		State:     config.StateBackendMemory,
		LogLevel:  "error",
		DataDir:   t.TempDir(),
	}
	handler, preShutdown, cleanup, _ := New(cfg, state.NewMemoryStore(), zap.NewNop(), clock.New())
	t.Cleanup(func() {
		preShutdown()
		cleanup(t.Context())
	})
	mux, ok := handler.(*inspectableMux)
	if !ok {
		t.Fatalf("New() returned %T, want *inspectableMux", handler)
	}
	return mux
}

// TestServiceRoutes_everyDirectRouteNamesItsService guards #2283 against
// drift. IAM enforcement authorises a request a service's own route serves as
// that service's, and RouteREST names the service only from what
// serviceRoutes recorded as RegisterRoutes ran. A registration that bypassed
// the record — a chi.Router method serviceRoutes does not override — would
// leave the request to be named by its credential scope again.
//
// routeOwnerTracker attributes the same routes by walking the mux, so the two
// must agree on every route a service registered directly.
func TestServiceRoutes_everyDirectRouteNamesItsService(t *testing.T) {
	// Given: the router with every service registered, and the routes each
	// service registered directly on it
	mux := newInspectableRouter(t)
	routes, err := walkRegisteredRoutes(mux)
	if err != nil {
		t.Fatalf("walk: %v", err)
	}

	checked := 0
	for _, route := range routes {
		if route.DirectOwner == "" {
			continue
		}
		// When: IAM enforcement asks who serves a request to the route
		path := examplePath(route.Pattern)
		got, routed := mux.paths.RouteREST(httptest.NewRequest(route.Method, path, nil))

		// Then: it is the service that registered it
		if !routed || got.Outcome != middleware.RESTServedByService || got.Service != route.DirectOwner {
			t.Errorf("%s %s (as %s): RouteREST = %+v, %v; want served by %s", route.Method, route.Pattern, path, got, routed, route.DirectOwner)
		}
		checked++
	}
	if checked == 0 {
		t.Fatal("no route was attributed to a service; the walk or routeOwnerTracker changed")
	}
}

// examplePath is a path chi matches to pattern: each parameter becomes a
// value its regular expression, if any, accepts, and a wildcard a segment.
func examplePath(pattern string) string {
	segments := strings.Split(pattern, "/")
	for i, segment := range segments {
		switch {
		case segment == "*":
			segments[i] = "x"
		case strings.HasPrefix(segment, "{") && strings.Contains(segment, "[0-9]"):
			segments[i] = "123456789012"
		case strings.HasPrefix(segment, "{"):
			segments[i] = "x"
		}
	}
	return strings.Join(segments, "/")
}
