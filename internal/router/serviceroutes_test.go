package router

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/overcast-sh/overcast/internal/middleware"
	"github.com/overcast-sh/overcast/internal/services/s3tables"
)

// TestServiceRoutes_namesTheServiceForEveryRegistrationShape pins #2283 for
// each way a service's RegisterRoutes can add a route: RouteREST names the
// service for a request the route serves.
func TestServiceRoutes_namesTheServiceForEveryRegistrationShape(t *testing.T) {
	for _, tc := range []struct {
		name     string
		register func(r chi.Router, h http.HandlerFunc)
		method   string
		path     string
	}{
		{"Get", func(r chi.Router, h http.HandlerFunc) { r.Get("/a", h) }, http.MethodGet, "/a"},
		{"Method", func(r chi.Router, h http.HandlerFunc) { r.Method("patch", "/a", h) }, http.MethodPatch, "/a"},
		{"HandleFunc", func(r chi.Router, h http.HandlerFunc) { r.HandleFunc("/a", h) }, http.MethodDelete, "/a"},
		{"Handle with a method", func(r chi.Router, h http.HandlerFunc) { r.Handle("POST /a", h) }, http.MethodPost, "/a"},
		{"Route", func(r chi.Router, h http.HandlerFunc) { r.Route("/a", func(r chi.Router) { r.Get("/{id}", h) }) }, http.MethodGet, "/a/1"},
		{"Mount of a handler", func(r chi.Router, h http.HandlerFunc) { r.Mount("/a", h) }, http.MethodPut, "/a/b/c"},
		{"Mount with a trailing slash", func(r chi.Router, h http.HandlerFunc) { r.Mount("/a/", h) }, http.MethodPut, "/a/b"},
		{"With", func(r chi.Router, h http.HandlerFunc) { r.With(passThrough).Get("/a", h) }, http.MethodGet, "/a"},
		{"Group", func(r chi.Router, h http.HandlerFunc) { r.Group(func(r chi.Router) { r.Post("/a", h) }) }, http.MethodPost, "/a"},
		{"Route inside With", func(r chi.Router, h http.HandlerFunc) {
			r.With(passThrough).Route("/a", func(r chi.Router) { r.Get("/", h) })
		}, http.MethodGet, "/a/"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Given: a route a service registered in this shape
			mux := chi.NewRouter()
			paths := newPathDispatch(mux)
			served := false
			tc.register(paths.routesFor("svc"), func(http.ResponseWriter, *http.Request) { served = true })

			// When: a request to it is resolved, then served
			req := httptest.NewRequest(tc.method, tc.path, nil)
			route, routed := paths.RouteREST(req)
			mux.ServeHTTP(httptest.NewRecorder(), req)

			// Then: the route serves it, and RouteREST names the service
			if !served {
				t.Fatalf("%s %s was not served by the registered route", tc.method, tc.path)
			}
			if !routed || route.Outcome != middleware.RESTServedByService || route.Service != "svc" {
				t.Fatalf("RouteREST = %+v, %v; want served by svc", route, routed)
			}
		})
	}
}

func passThrough(next http.Handler) http.Handler { return next }

// TestServiceRoutes_unmatchedPathBeneathAServiceMountIsNotServed pins that a
// path a service's sub-router does not match reaches no route: the router's
// 404 serves no operation of the service's.
func TestServiceRoutes_unmatchedPathBeneathAServiceMountIsNotServed(t *testing.T) {
	// Given: a service's sub-router serving one path
	mux := chi.NewRouter()
	paths := newPathDispatch(mux)
	paths.routesFor("svc").Route("/a", func(r chi.Router) {
		r.Get("/known", func(http.ResponseWriter, *http.Request) {})
	})

	// When: a request to another path beneath it is resolved
	route, routed := paths.RouteREST(httptest.NewRequest(http.MethodGet, "/a/unknown", nil))

	// Then: no route serves it
	if routed {
		t.Fatalf("RouteREST = %+v, true; want no route", route)
	}
}

// TestIcebergRoot_isWhereS3TablesServesIt holds middleware's copy of the
// Iceberg REST catalog's root, which names its IAM actions, to S3 Tables'.
func TestIcebergRoot_isWhereS3TablesServesIt(t *testing.T) {
	if middleware.IcebergRoot != s3tables.IcebergRoot {
		t.Fatalf("middleware.IcebergRoot = %q, s3tables.IcebergRoot = %q", middleware.IcebergRoot, s3tables.IcebergRoot)
	}
}
