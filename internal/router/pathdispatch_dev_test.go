//go:build dev

package router

import (
	"net/http"
	"testing"

	"github.com/go-chi/chi/v5"
	"go.uber.org/zap"

	"github.com/overcast-sh/overcast/internal/clock"
	"github.com/overcast-sh/overcast/internal/config"
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
