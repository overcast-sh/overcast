package router

import (
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/overcast-sh/overcast/internal/middleware"
)

// requestRouting is the router's dispatch as IAM enforcement reads it
// (middleware.RequestRouter): rootDispatch's resolution of AWS Query traffic,
// and pathDispatch's of the requests the router serves by their path. Both are
// filled in after the middleware that reads them is built, and read at
// request time.
type requestRouting struct {
	*rootDispatch
	*pathDispatch
}

// pathDispatch records, by the pattern chi matches a request to on the mux,
// the handler registered there, so RouteREST can say who serves a request
// without serving it.
//
// Two kinds of handler are recorded. A service's own route is recorded as
// served by that service (servedBy), by serviceRoutes as RegisterRoutes adds
// it. The handlers the router registers itself that pick, per request, between
// services and the REST fallback — the fallback's own "/*" and GET / routes,
// the shared roots (sharedRoots), the ARN- and signing-name-dispatched roots,
// and the Smithy RPC v2 URI, which is an S3 object path without a
// Smithy-Protocol header — are choosers, and RouteREST follows the same
// choices. IAM enforcement then authorises the operation the router serves,
// rather than one named by the request's credential scope or query string
// (#2271, #2283).
type pathDispatch struct {
	mux *chi.Mux
	// points are the handlers RouteREST resolves through, by the method and
	// the top-level pattern they are registered at: a route's own pattern, or
	// the mount point a sub-router is reached through. Method "" is every
	// method.
	points map[routeKey]http.Handler
}

type routeKey struct{ method, pattern string }

// chooser is a router-owned handler that serves a request by handing it to
// the handler choose names, nil for none. rctx is the request's routing
// context: the router's own when it serves the request, and the one chi's Find
// fills in when RouteREST resolves it.
type chooser interface {
	choose(r *http.Request, rctx *chi.Context) http.Handler
}

// serveChoice serves r with the handler c chooses for it, and answers 404 when
// it chooses none.
func serveChoice(w http.ResponseWriter, r *http.Request, c chooser) {
	h := c.choose(r, chi.RouteContext(r.Context()))
	if h == nil {
		http.NotFound(w, r)
		return
	}
	h.ServeHTTP(w, r)
}

func newPathDispatch(mux *chi.Mux) *pathDispatch {
	return &pathDispatch{mux: mux, points: map[routeKey]http.Handler{}}
}

// handle registers h on the mux at pattern, for method or for every method
// when method is "", and records it for RouteREST.
func (p *pathDispatch) handle(method, pattern string, h http.Handler) {
	if method == "" {
		p.mux.Handle(pattern, h)
	} else {
		p.mux.Method(method, pattern, h)
	}
	p.points[routeKey{method, pattern}] = h
}

// mount serves root, and every path beneath it, through h for every method,
// and records it for RouteREST.
func (p *pathDispatch) mount(root string, h http.Handler) {
	p.mux.Route(root, func(m chi.Router) {
		m.Handle("/*", h)
	})
	p.recordMount(root, h)
}

// recordMount records h at every top-level pattern chi's Mount registers for
// root: the root and the root with a trailing slash, unless it already ends
// in one, and the paths beneath it.
func (p *pathDispatch) recordMount(root string, h http.Handler) {
	if !strings.HasSuffix(root, "/") {
		p.points[routeKey{"", root}] = h
		root += "/"
		p.points[routeKey{"", root}] = h
	}
	p.points[routeKey{"", root + "*"}] = h
}

// RouteREST reports how the router serves r by its path, following the
// router's own choices from the route chi matches it to: through the REST
// fallback, or by a service's route. It reports false when r reaches neither:
// a route the router answers itself, or no route at all, which includes a
// path beneath a service's sub-router that the sub-router does not match.
func (p *pathDispatch) RouteREST(r *http.Request) (middleware.RESTRoute, bool) {
	rctx := chi.NewRouteContext()
	if p.mux.Find(rctx, r.Method, routingPath(r)) == "" {
		return middleware.RESTRoute{}, false
	}
	h, ok := p.registered(r.Method, rctx)
	for ok {
		switch next := h.(type) {
		case *restFallback:
			return restClaimFor(next.registry, r), true
		case s3Direct:
			return middleware.RESTRoute{Outcome: middleware.RESTServedByS3}, true
		case servedBy:
			return middleware.RESTRoute{Outcome: middleware.RESTServedByService, Service: next.service}, true
		case chooser:
			h = next.choose(r, rctx)
			ok = h != nil
		default:
			ok = false
		}
	}
	return middleware.RESTRoute{}, false
}

// registered is the handler recorded at the top-level pattern Find matched,
// for method or for every method.
func (p *pathDispatch) registered(method string, rctx *chi.Context) (http.Handler, bool) {
	if len(rctx.RoutePatterns) == 0 {
		return nil, false
	}
	pattern := rctx.RoutePatterns[0]
	if h, ok := p.points[routeKey{method, pattern}]; ok {
		return h, true
	}
	h, ok := p.points[routeKey{"", pattern}]
	return h, ok
}

// routingPath is the path chi routes r on: its raw path when it has one.
func routingPath(r *http.Request) string {
	switch {
	case r.URL.RawPath != "":
		return r.URL.RawPath
	case r.URL.Path != "":
		return r.URL.Path
	}
	return "/"
}

// s3Direct is S3's own router, reached without the REST fallback: the Smithy
// RPC v2 URI hands S3 a request carrying no Smithy-Protocol header outright.
type s3Direct struct{ http.Handler }

// servedBy is a handler of service's: a route it registered itself, or a
// sub-router of its that a router-owned dispatcher hands requests to.
type servedBy struct {
	service string
	http.Handler
}

// servedByService marks h as service's, or is nil when h is: a dispatcher
// reads a nil handler as a service that is not registered.
func servedByService(service string, h http.Handler) http.Handler {
	if h == nil {
		return nil
	}
	return servedBy{service: service, Handler: h}
}

// delegatedRouter is service's sub-router that a shared root dispatches to and
// that hands every request it does not match to the REST fallback
// (delegateUnmatched).
type delegatedRouter struct {
	chi.Router
	service  string
	fallback http.Handler
}

// choose names the fallback for a request the sub-router matches no route
// for, which is what its NotFound and MethodNotAllowed handlers serve it with,
// and the service for one its own route serves. The sub-router routes on the
// path beneath the shared root it is mounted under, which is "/" for the root
// itself.
func (d delegatedRouter) choose(r *http.Request, rctx *chi.Context) http.Handler {
	path := rctx.RoutePath
	if path == "" {
		path = "/"
	}
	if d.Find(chi.NewRouteContext(), r.Method, path) == "" {
		return d.fallback
	}
	return servedBy{service: d.service, Handler: d.Router}
}
