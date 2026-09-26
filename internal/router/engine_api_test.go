package router

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/overcast-sh/overcast/internal/awsapi"
)

// serviceRecorder records which fake service a request reached, and whether
// the request chain ran before it.
type serviceRecorder struct {
	reached string
	chained bool
}

func (s *serviceRecorder) record(name string, r *http.Request) {
	s.reached, s.chained = name, r.Header.Get("X-Chained") != ""
}

// fakeTargetService is an enabled JSON-target service.
type fakeTargetService struct {
	name, prefix string
	rec          *serviceRecorder
}

func (f fakeTargetService) Name() string                                    { return f.name }
func (fakeTargetService) RegisterRoutes(chi.Router)                         {}
func (f fakeTargetService) TargetPrefix() string                            { return f.prefix }
func (f fakeTargetService) Dispatch(_ http.ResponseWriter, r *http.Request) { f.rec.record(f.name, r) }

// fakeQueryService is an enabled Query-protocol service.
type fakeQueryService struct {
	name string
	rec  *serviceRecorder
}

func (f fakeQueryService) Name() string            { return f.name }
func (fakeQueryService) RegisterRoutes(chi.Router) {}
func (f fakeQueryService) DispatchQuery(_ http.ResponseWriter, r *http.Request) {
	f.rec.record(f.name, r)
}

// fakeS3Tables is an enabled S3 Tables, serving one Iceberg catalog route.
type fakeS3Tables struct{ rec *serviceRecorder }

func (fakeS3Tables) Name() string              { return "s3tables" }
func (fakeS3Tables) RegisterRoutes(chi.Router) {}
func (f fakeS3Tables) IcebergRouter() chi.Router {
	r := chi.NewRouter()
	r.Get("/v1/config", func(_ http.ResponseWriter, r *http.Request) { f.rec.record("iceberg", r) })
	return r
}

// buildEngineAPI builds the engine API over services, with an S3 router that
// records, behind a chain that marks every request it runs for.
func buildEngineAPI(rec *serviceRecorder, services ...Service) (glue, s3, iceberg http.Handler) {
	return buildEngineAPIBehind(chi.Middlewares{func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			r.Header.Set("X-Chained", "1")
			next.ServeHTTP(w, r)
		})
	}}, rec, services...)
}

// buildEngineAPIBehind builds the engine API over services, with an S3 router
// that records, behind chain.
func buildEngineAPIBehind(chain chi.Middlewares, rec *serviceRecorder, services ...Service) (glue, s3, iceberg http.Handler) {
	byName := make(map[string]Service, len(services))
	for _, svc := range services {
		byName[svc.Name()] = svc
	}
	s3Router := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { rec.record("s3", r) })
	api := engineAPI(chain, byName, s3Router, awsapi.NewRegistry())
	return api.Glue, api.S3, api.Iceberg
}

func engineAPIRequest(method, target string, header map[string]string, body string) *http.Request {
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, target, reader)
	for k, v := range header {
		req.Header.Set(k, v)
	}
	return req
}

func TestEngineAPI_requestShapesPerSigningName(t *testing.T) {
	// Given: the engine API, with every service a request below names enabled
	rec := &serviceRecorder{}
	glue, s3, iceberg := buildEngineAPI(rec,
		fakeTargetService{name: "glue", prefix: "AWSGlue.", rec: rec},
		fakeTargetService{name: "athena", prefix: "AmazonAthena.", rec: rec},
		fakeTargetService{name: "dynamodb", prefix: "DynamoDB_20120810.", rec: rec},
		fakeQueryService{name: "iam", rec: rec},
		fakeTargetService{name: "s3", rec: rec},
		fakeS3Tables{rec: rec},
	)
	json11 := func(target string) map[string]string {
		return map[string]string{"X-Amz-Target": target, "Content-Type": "application/x-amz-json-1.1"}
	}
	form := map[string]string{"Content-Type": "application/x-www-form-urlencoded"}
	const listUsers = "Action=ListUsers&Version=2010-05-08"
	cases := []struct {
		name              string
		req               func() *http.Request
		glue, s3, iceberg string // the service each handler reaches; "" is none
	}{
		{"a Glue JSON 1.1 target", func() *http.Request { return engineAPIRequest(http.MethodPost, "/", json11("AWSGlue.GetTable"), "{}") }, "glue", "s3", ""},
		{"another service's JSON 1.1 target", func() *http.Request {
			return engineAPIRequest(http.MethodPost, "/", json11("AmazonAthena.ListWorkGroups"), "{}")
		}, "", "s3", ""},
		{"a JSON 1.0 target", func() *http.Request {
			return engineAPIRequest(http.MethodPost, "/", map[string]string{"X-Amz-Target": "DynamoDB_20120810.ListTables", "Content-Type": "application/x-amz-json-1.0"}, "{}")
		}, "", "s3", ""},
		{"a Query body", func() *http.Request { return engineAPIRequest(http.MethodPost, "/", form, listUsers) }, "", "s3", ""},
		{"a Query string", func() *http.Request { return engineAPIRequest(http.MethodGet, "/?"+listUsers, nil, "") }, "", "s3", ""},
		{"a REST-JSON path", func() *http.Request {
			return engineAPIRequest(http.MethodPost, "/2015-03-31/functions/f/invocations", nil, "{}")
		}, "", "s3", ""},
		{"a Glue target on a REST-JSON path", func() *http.Request {
			return engineAPIRequest(http.MethodPost, "/2015-03-31/functions/f/invocations", json11("AWSGlue.GetTable"), "{}")
		}, "", "s3", ""},
		{"a REST-XML path", func() *http.Request { return engineAPIRequest(http.MethodGet, "/2013-04-01/hostedzone", nil, "") }, "", "s3", ""},
		{"a Smithy RPC v2 call", func() *http.Request {
			return engineAPIRequest(http.MethodPost, "/service/ecs/operation/ListClusters", map[string]string{"Smithy-Protocol": "rpc-v2-cbor"}, "")
		}, "", "s3", ""},
		{"an S3 object", func() *http.Request { return engineAPIRequest(http.MethodGet, "/bucket/key", nil, "") }, "", "s3", ""},
		{"the Iceberg catalog", func() *http.Request {
			return engineAPIRequest(http.MethodGet, "/iceberg/v1/config?warehouse=arn", nil, "")
		}, "", "s3", "iceberg"},
		{"S3 Tables' control plane", func() *http.Request { return engineAPIRequest(http.MethodPut, "/buckets", nil, "") }, "", "s3", ""},
		{"the unsigned Iceberg mount", func() *http.Request {
			return engineAPIRequest(http.MethodGet, "/_overcast/s3tables/iceberg/v1/config", nil, "")
		}, "", "s3", ""},
	}
	for _, c := range cases {
		for _, h := range []struct {
			signingName string
			handler     http.Handler
			want        string
		}{{"glue", glue, c.glue}, {"s3", s3, c.s3}, {"s3tables", iceberg, c.iceberg}} {
			t.Run(h.signingName+"/"+c.name, func(t *testing.T) {
				// When: the request reaches the handler for one signing name
				*rec = serviceRecorder{}
				w := httptest.NewRecorder()
				h.handler.ServeHTTP(w, c.req())

				// Then: it reaches that signing name's own service, through the
				// chain, or no service at all
				if rec.reached != h.want || (h.want != "" && !rec.chained) {
					t.Errorf("reached %q (chained %v), want %q; status %d %s", rec.reached, rec.chained, h.want, w.Code, w.Body)
				}
			})
		}
	}
}

func TestEngineAPI_withNoEngineServiceEnabled(t *testing.T) {
	// Given: none of Glue, S3 or S3 Tables enabled
	rec := &serviceRecorder{}

	// When: the engine API is built
	glue, s3, iceberg := buildEngineAPI(rec, fakeQueryService{name: "iam", rec: rec})

	// Then: it has no handler, so the gateway refuses every call
	if glue != nil || s3 != nil || iceberg != nil {
		t.Errorf("glue %v, s3 %v, iceberg %v; want none", glue, s3, iceberg)
	}
}
