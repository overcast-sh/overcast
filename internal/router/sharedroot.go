package router

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/overcast-sh/overcast/internal/awsapi"
	"github.com/overcast-sh/overcast/internal/middleware"
)

// sharedRoots mounts the path roots the main router owns on behalf of the
// services that share them and that are also legal S3 bucket names — /tags,
// /applications, the S3 Tables roots and /iceberg — each dispatched at request
// time by resource ARN or SigV4 signing name. (/v1/tags and /v2/apis are
// dispatched the same way but need none of this: "v1" and "v2" are too short
// to name a bucket.)
//
// A request the dispatcher does not give to a service must reach S3 exactly as
// it would had the root never been mounted (#2098). Two things stood in the
// way, and this type fixes both for every such root at once rather than per
// dispatcher:
//
//   - chi's mount shifts the routing path past the root, and S3's router
//     matches absolute patterns, so handed the shifted path it read
//     GET /tags/obj/key.txt as the object "key.txt" in a bucket named "obj".
//     s3 is the REST fallback on the whole request path.
//   - a signing-name dispatcher sent an S3-signed request to its fallback
//     service, so an SDK ListObjectsV2 on a bucket named "applications" was
//     answered by AppRegistry's ListApplications. mount sends every S3-signed
//     request to s3 before the dispatcher sees it: no SDK signs for S3 a
//     request meant for another service, which is the rule addressesNonS3
//     applies to the REST fallback itself.
type sharedRoots struct {
	mux chi.Router
	// s3 is the router's REST fallback, run on the whole request path.
	s3 http.HandlerFunc
}

func newSharedRoots(mux chi.Router, operationRegistry *awsapi.Registry, s3Router http.Handler) sharedRoots {
	return sharedRoots{mux: mux, s3: wholePath(restFallback(operationRegistry, s3Router))}
}

// mount serves root through dispatch, except that an S3-signed request goes
// straight to S3. The single "/*" pattern also matches the bare root.
func (s sharedRoots) mount(root string, dispatch http.Handler) {
	h := s3SignedFirst(dispatch, s.s3)
	s.mux.Route(root, func(m chi.Router) {
		m.HandleFunc("/*", h)
	})
}

// delegate makes a dispatched sub-router hand what it does not serve to S3's
// REST fallback (see delegateUnmatched), and returns it for recording.
func (s sharedRoots) delegate(sub chi.Router) chi.Router {
	delegateUnmatched(sub, s.s3)
	return sub
}

// s3SignedFirst sends a request signed for the S3 object API to s3 and every
// other request to dispatch.
func s3SignedFirst(dispatch, s3 http.Handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if middleware.SignedForS3API(r, middleware.ServiceFromCredential(r)) {
			s3.ServeHTTP(w, r)
			return
		}
		dispatch.ServeHTTP(w, r)
	}
}

// wholePath makes a handler reached from inside a chi mount route on the full
// request path again. A mount shifts chi's routing path past its prefix, which
// the next router then matches against; S3's router has absolute patterns, so
// handed the shifted path it would read "/tables/key" as the object "key" in a
// bucket that does not exist. Clearing RoutePath makes chi fall back to the
// request URL, exactly as it does for the unmounted "/*" route.
func wholePath(h http.Handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if rctx := chi.RouteContext(r.Context()); rctx != nil {
			rctx.RoutePath = ""
		}
		h.ServeHTTP(w, r)
	}
}

// delegateUnmatched makes a dispatched sub-router hand requests it does not
// serve back to the main router's REST fallback, instead of answering chi's
// own bare 404 or 405.
//
// A chi sub-router owns its whole subtree: a path it does not match hits *its*
// NotFound and never reaches the parent's "/*". That is why the modeled
// AppConfig operations Overcast does not implement answered a bodiless 404
// under AppRegistry's /applications rather than the generated registry's
// protocol-correct 501 (docs/plans/manifest-enforcement.md records the fault).
// MethodNotAllowed matters as much as NotFound: the model binds several
// unimplemented operations to a method on a path that *is* registered, and chi
// answers those 405.
func delegateUnmatched(sub chi.Router, fallback http.HandlerFunc) {
	sub.NotFound(fallback)
	sub.MethodNotAllowed(fallback)
}
