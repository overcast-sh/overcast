package router

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/overcast-sh/overcast/internal/middleware"
)

// requestRouting is the router's dispatch as IAM enforcement reads it
// (middleware.RequestRouter): rootDispatch's resolution of AWS Query traffic,
// and pathDispatch's of the requests the REST fallback serves. Both are filled
// in after the middleware that reads them is built, and read at request time.
type requestRouting struct {
	*rootDispatch
	*pathDispatch
}

// pathDispatch holds the handlers the router registers itself that pick, per
// request, between a service and the REST fallback: the fallback's own "/*"
// and GET / routes, the shared roots (sharedRoots), and the Smithy RPC v2 URI,
// which is an S3 object path without a Smithy-Protocol header. Each such
// handler is a chooser, and RouteREST follows the same choices without
// serving the request, so IAM enforcement authorises the operation they serve
// rather than one named by the request's credential scope or query string
// (#2271). A route a service registered itself is not one of them: that
// service serves the request, and IAM classifies it from its content.
type pathDispatch struct {
	mux *chi.Mux
	// points are the handlers RouteREST resolves through, by the method and
	// pattern they are registered at; method "" is every method.
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
// and records it for RouteREST under each pattern chi's Find reports for a
// mount: the root, the root with a trailing slash, and the paths beneath it.
func (p *pathDispatch) mount(root string, h http.Handler) {
	p.mux.Route(root, func(m chi.Router) {
		m.Handle("/*", h)
	})
	for _, pattern := range []string{root, root + "/", root + "/*"} {
		p.points[routeKey{"", pattern}] = h
	}
}

// RouteREST reports how the REST fallback serves r, following the router's
// own choices from the route chi matches it to, and false when those choices
// end at a service's route instead.
func (p *pathDispatch) RouteREST(r *http.Request) (middleware.RESTRoute, bool) {
	rctx := chi.NewRouteContext()
	pattern := p.mux.Find(rctx, r.Method, routingPath(r))
	h, ok := p.points[routeKey{r.Method, pattern}]
	if !ok {
		h, ok = p.points[routeKey{"", pattern}]
	}
	for ok {
		switch next := h.(type) {
		case *restFallback:
			return restClaimFor(next.registry, r), true
		case s3Direct:
			return middleware.RESTRoute{Outcome: middleware.RESTServedByS3}, true
		case chooser:
			h = next.choose(r, rctx)
			ok = h != nil
		default:
			ok = false
		}
	}
	return middleware.RESTRoute{}, false
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

// delegatedRouter is a service's sub-router that a shared root dispatches to
// and that hands every request it does not match to the REST fallback
// (delegateUnmatched).
type delegatedRouter struct {
	chi.Router
	fallback http.Handler
}

// choose names the fallback for a request the sub-router matches no route
// for, which is what its NotFound and MethodNotAllowed handlers serve it with,
// and nil for one its own route serves. The sub-router routes on the path
// beneath the shared root it is mounted under, which is "/" for the root
// itself.
func (d delegatedRouter) choose(r *http.Request, rctx *chi.Context) http.Handler {
	path := rctx.RoutePath
	if path == "" {
		path = "/"
	}
	if d.Find(chi.NewRouteContext(), r.Method, path) == "" {
		return d.fallback
	}
	return nil
}
