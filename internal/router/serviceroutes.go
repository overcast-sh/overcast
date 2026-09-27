package router

import (
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
)

// serviceRoutes is the router a service's RegisterRoutes registers on. Every
// route goes to the mux exactly as the mux would register it. serviceRoutes
// also records, for pathDispatch, the service that registered each top-level
// pattern: the route's own pattern, or the point a sub-router is mounted at.
// With that record, RouteREST names the service a request is served by,
// whatever service its credential scope names (#2283).
//
// It records as routes are added because walking the mux after each service,
// as the dev build's routeOwnerTracker does, adds tens of milliseconds to
// startup. Recording costs one map write per route.
//
// Only the methods that register a route are overridden; the rest are the
// mux's. TestServiceRoutes_everyDirectRouteNamesItsService (dev build) checks
// the record against routeOwnerTracker's walk, so a registration method this
// type does not override is caught there.
type serviceRoutes struct {
	chi.Router
	paths   *pathDispatch
	service string
}

// routesFor is the router service's RegisterRoutes registers on.
func (p *pathDispatch) routesFor(service string) chi.Router {
	return serviceRoutes{Router: p.mux, paths: p, service: service}
}

// record notes h, registered at pattern for method ("" for every method), as
// the service's.
func (s serviceRoutes) record(method, pattern string, h http.Handler) {
	s.paths.points[routeKey{strings.ToUpper(method), pattern}] = servedBy{service: s.service, Handler: h}
}

func (s serviceRoutes) With(middlewares ...func(http.Handler) http.Handler) chi.Router {
	return serviceRoutes{Router: s.Router.With(middlewares...), paths: s.paths, service: s.service}
}

func (s serviceRoutes) Group(fn func(r chi.Router)) chi.Router {
	im := s.With()
	if fn != nil {
		fn(im)
	}
	return im
}

// Route mounts a new sub-router at pattern, as the mux's Route does. The
// sub-router's own routes need no record: RouteREST resolves a request by the
// mount point chi matches first.
func (s serviceRoutes) Route(pattern string, fn func(r chi.Router)) chi.Router {
	sub := chi.NewRouter()
	fn(sub)
	s.Mount(pattern, sub)
	return sub
}

func (s serviceRoutes) Mount(pattern string, h http.Handler) {
	s.Router.Mount(pattern, h)
	s.paths.recordMount(pattern, servedBy{service: s.service, Handler: h})
}

func (s serviceRoutes) Handle(pattern string, h http.Handler) {
	s.Router.Handle(pattern, h)
	// The mux reads "METHOD /pattern" as a route for that method alone.
	if i := strings.IndexAny(pattern, " \t"); i >= 0 {
		s.record(pattern[:i], strings.TrimLeft(pattern[i+1:], " \t"), h)
		return
	}
	s.record("", pattern, h)
}

func (s serviceRoutes) HandleFunc(pattern string, h http.HandlerFunc) { s.Handle(pattern, h) }

func (s serviceRoutes) Method(method, pattern string, h http.Handler) {
	s.Router.Method(method, pattern, h)
	s.record(method, pattern, h)
}

func (s serviceRoutes) MethodFunc(method, pattern string, h http.HandlerFunc) {
	s.Method(method, pattern, h)
}

func (s serviceRoutes) Connect(pattern string, h http.HandlerFunc) {
	s.Method(http.MethodConnect, pattern, h)
}

func (s serviceRoutes) Delete(pattern string, h http.HandlerFunc) {
	s.Method(http.MethodDelete, pattern, h)
}

func (s serviceRoutes) Get(pattern string, h http.HandlerFunc) { s.Method(http.MethodGet, pattern, h) }

func (s serviceRoutes) Head(pattern string, h http.HandlerFunc) {
	s.Method(http.MethodHead, pattern, h)
}

func (s serviceRoutes) Options(pattern string, h http.HandlerFunc) {
	s.Method(http.MethodOptions, pattern, h)
}

func (s serviceRoutes) Patch(pattern string, h http.HandlerFunc) {
	s.Method(http.MethodPatch, pattern, h)
}

func (s serviceRoutes) Post(pattern string, h http.HandlerFunc) {
	s.Method(http.MethodPost, pattern, h)
}

func (s serviceRoutes) Put(pattern string, h http.HandlerFunc) { s.Method(http.MethodPut, pattern, h) }

func (s serviceRoutes) Query(pattern string, h http.HandlerFunc) { s.Method("QUERY", pattern, h) }

func (s serviceRoutes) Trace(pattern string, h http.HandlerFunc) {
	s.Method(http.MethodTrace, pattern, h)
}
