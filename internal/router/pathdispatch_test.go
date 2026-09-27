package router

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/overcast-sh/overcast/internal/awsapi"
	"github.com/overcast-sh/overcast/internal/middleware"
)

// recorder answers a request by recording who served it.
type recorder struct {
	name   string
	served *string
}

func (h recorder) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	*h.served = h.name
	w.WriteHeader(http.StatusOK)
}

// newPathRouter builds a router shaped like New's: a service route, the REST
// fallback on "/*" and GET /, the Smithy RPC v2 URI, and shared roots
// dispatched by signing name (to a delegated sub-router) and by resource ARN.
func newPathRouter(served *string) (*chi.Mux, *pathDispatch) {
	mux := chi.NewRouter()
	paths := newPathDispatch(mux)
	fallback := &restFallback{registry: awsapi.NewRegistry(), s3: recorder{"s3", served}}
	shared := newSharedRoots(paths, fallback)

	mux.Get("/clusters", recorder{"eks", served}.ServeHTTP)
	mux.Post("/", recorder{"target", served}.ServeHTTP)

	tables := chi.NewRouter()
	tables.Get("/", recorder{"s3tables", served}.ServeHTTP)
	shared.mount("/buckets", signingNameDispatch{signingName: "s3tables", owner: shared.delegate(tables), fallback: shared.s3})
	shared.mount("/tags", tagsDispatch{
		routers:  map[string]http.Handler{"eks": recorder{"eks-tags", served}},
		fallback: shared.s3,
	})

	paths.handle(http.MethodPost, "/service/{service}/operation/{operation}", smithyRPCRoute{
		rpc: recorder{"rpc", served},
		s3:  s3Direct{recorder{"s3", served}},
	})
	paths.handle(http.MethodGet, "/", fallback)
	paths.handle("", "/*", fallback)
	return mux, paths
}

// TestPathDispatch_routeRESTIsWhatIsServed pins the invariant IAM enforcement
// relies on (#2271): RouteREST reports the REST fallback exactly when the
// router serves a request through it, as S3 or as a modeled binding's 501,
// and names no fallback route when a service's route serves it.
func TestPathDispatch_routeRESTIsWhatIsServed(t *testing.T) {
	signed := func(r *http.Request, signingName string) *http.Request {
		r.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=AKID/20260101/us-east-1/"+signingName+"/aws4_request, SignedHeaders=host, Signature=x")
		return r
	}
	smithy := func(r *http.Request) *http.Request {
		r.Header.Set("Smithy-Protocol", "rpc-v2-cbor")
		return r
	}
	eksARN := url.PathEscape("arn:aws:eks:us-east-1:000000000000:cluster/c")
	for _, tc := range []struct {
		name       string
		req        *http.Request
		wantServed string
	}{
		{"bucket signed for another service", signed(httptest.NewRequest(http.MethodPut, "/some-bucket?Action=GetFederationToken", nil), "sts"), "s3"},
		{"object", httptest.NewRequest(http.MethodGet, "/some-bucket/key", nil), "s3"},
		{"list buckets", httptest.NewRequest(http.MethodGet, "/", nil), "s3"},
		{"head of the root", httptest.NewRequest(http.MethodHead, "/", nil), "s3"},
		{"root POST", httptest.NewRequest(http.MethodPost, "/", nil), "target"},
		{"service route signed for another service", signed(httptest.NewRequest(http.MethodGet, "/clusters?Action=GetCallerIdentity", nil), "sts"), "eks"},
		{"Smithy URI without the header", signed(httptest.NewRequest(http.MethodPost, "/service/a/operation/b", nil), "sts"), "s3"},
		{"Smithy RPC v2 call", smithy(httptest.NewRequest(http.MethodPost, "/service/a/operation/b", nil)), "rpc"},
		{"shared root's owner", signed(httptest.NewRequest(http.MethodGet, "/buckets", nil), "s3tables"), "s3tables"},
		{"shared root signed for another service", signed(httptest.NewRequest(http.MethodPut, "/buckets", nil), "sts"), "s3"},
		{"shared root signed for S3", signed(httptest.NewRequest(http.MethodGet, "/buckets", nil), "s3"), "s3"},
		{"shared root unsigned", httptest.NewRequest(http.MethodPut, "/buckets/key", nil), "s3"},
		{"shared root's owner, trailing slash", signed(httptest.NewRequest(http.MethodGet, "/buckets/", nil), "s3tables"), "s3tables"},
		{"delegated sub-router's unmatched method", signed(httptest.NewRequest(http.MethodPut, "/buckets", nil), "s3tables"), "501"},
		{"delegated sub-router's unmatched path", signed(httptest.NewRequest(http.MethodDelete, "/buckets/x/y", nil), "s3tables"), "501"},
		{"escaped ARN dispatch to its owner", httptest.NewRequest(http.MethodGet, "/tags/"+eksARN, nil), "eks-tags"},
		{"ARN dispatch with no owner", httptest.NewRequest(http.MethodPut, "/tags", nil), "s3"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Given: a router shaped like New's
			var served string
			mux, paths := newPathRouter(&served)

			// When: IAM enforcement asks how the request is served, then the
			// router serves it
			route, isFallback := paths.RouteREST(tc.req)
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, tc.req)

			// Then: the two agree
			if rec.Code == http.StatusNotImplemented {
				served = "501"
			}
			if served != tc.wantServed {
				t.Fatalf("served by %q, want %q", served, tc.wantServed)
			}
			want, wantFallback := map[string]middleware.RESTOutcome{
				"s3":  middleware.RESTServedByS3,
				"501": middleware.RESTNotImplemented,
			}[served]
			if isFallback != wantFallback || (isFallback && route.Outcome != want) {
				t.Errorf("RouteREST = %+v, %v; the router served it with %q", route, isFallback, served)
			}
		})
	}
}

func TestPathDispatch_routeRESTNamesTheFallbacksClaim(t *testing.T) {
	// Given: a router shaped like New's
	var served string
	_, paths := newPathRouter(&served)

	// When: a request signed for S3 Tables reaches the fallback on a binding
	// only S3 Tables models
	r := httptest.NewRequest(http.MethodGet, "/get-table?tableBucketARN=a", nil)
	r.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=AKID/20260101/us-east-1/s3tables/aws4_request, SignedHeaders=host, Signature=x")
	route, isFallback := paths.RouteREST(r)

	// Then: it is S3 Tables' GetTable, answered with a 501
	if !isFallback || route.Outcome != middleware.RESTNotImplemented || route.Claim.Service != "s3tables" || route.Claim.Operation != "GetTable" {
		t.Fatalf("RouteREST = %+v, %v; want S3 Tables' GetTable 501", route, isFallback)
	}
}
