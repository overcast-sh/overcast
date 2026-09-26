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
	"github.com/overcast-sh/overcast/internal/events"
	"github.com/overcast-sh/overcast/internal/middleware"
	"github.com/overcast-sh/overcast/internal/state"
	"github.com/overcast-sh/overcast/internal/trace"
)

// engineKey stands in for the access key the Athena engine gateway mints:
// no IAM user or role session holds it.
const engineKey = "OVERCASTATHENA0123456789ABCDEF01234567"

// enforcingChain is the request chain of a router with IAM enforcement on.
func enforcingChain() requestChain {
	var bus *events.Bus
	var hostRoutes []middleware.HostRouteRow
	return requestChain{
		cfg:        &config.Config{EnforceIAM: true},
		store:      state.NewMemoryStore(),
		logger:     zap.NewNop(),
		clk:        clock.New(),
		traceBuf:   trace.NewBuffer(1),
		bus:        &bus,
		hostRoutes: &hostRoutes,
	}
}

// engineCall is a call the engine makes, signed with its key for
// signingName.
type engineCall struct {
	signingName, method, path string
	header                    map[string]string
}

// The calls the engine makes: one per service. The Iceberg REST catalog's
// names no IAM action, so enforcement gates it on no chain.
var (
	glueGetTable = engineCall{"glue", http.MethodPost, "/", map[string]string{"X-Amz-Target": "AWSGlue.GetTable", "Content-Type": "application/x-amz-json-1.1"}}
	s3GetObject  = engineCall{"s3", http.MethodGet, "/athena-data/people/part-1.csv", nil}
	icebergCfg   = engineCall{"s3tables", http.MethodGet, "/iceberg/v1/config", nil}
)

func (c engineCall) request() *http.Request {
	body := ""
	if c.method == http.MethodPost {
		body = "{}"
	}
	req := engineAPIRequest(c.method, c.path, c.header, body)
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential="+engineKey+
		"/20260927/us-east-1/"+c.signingName+"/aws4_request, SignedHeaders=host;x-amz-date, Signature=00")
	req.Header.Set("X-Amz-Date", "20260927T000000Z")
	return req
}

func TestRequestChain_engineCallsUnderIAMEnforcement(t *testing.T) {
	// Given: IAM enforcement on, and the engine API behind the engine chain
	rec := &serviceRecorder{}
	glue, s3, iceberg := buildEngineAPIBehind(enforcingChain().engine(), rec,
		fakeTargetService{name: "glue", prefix: "AWSGlue.", rec: rec},
		fakeTargetService{name: "s3", rec: rec},
		fakeS3Tables{rec: rec},
	)
	handlers := map[string]http.Handler{"glue": glue, "s3": s3, "s3tables": iceberg}
	want := map[string]string{"glue": "glue", "s3": "s3", "s3tables": "iceberg"}
	for _, c := range []engineCall{glueGetTable, s3GetObject, icebergCfg} {
		t.Run(c.signingName, func(t *testing.T) {
			// When: the engine calls the service, signed with its own key
			*rec = serviceRecorder{}
			w := httptest.NewRecorder()
			handlers[c.signingName].ServeHTTP(w, c.request())

			// Then: it reaches the service; the query that asked for it was
			// authorised when it was started
			if rec.reached != want[c.signingName] {
				t.Fatalf("reached %q, want %q; status %d %s", rec.reached, want[c.signingName], w.Code, w.Body)
			}
		})
	}
}

func TestRequestChain_engineKeyOnTheAPIUnderIAMEnforcement(t *testing.T) {
	// Given: IAM enforcement on, and a service behind the API's own chain
	rec := &serviceRecorder{}
	api := chi.NewRouter()
	api.Use(enforcingChain().api(nil)...)
	api.HandleFunc("/*", func(_ http.ResponseWriter, r *http.Request) { rec.record("api", r) })
	// Each denial is its protocol's: awsJson answers 400, S3 403.
	for _, tc := range []struct {
		call   engineCall
		status int
	}{{glueGetTable, http.StatusBadRequest}, {s3GetObject, http.StatusForbidden}} {
		t.Run(tc.call.signingName, func(t *testing.T) {
			// When: a caller signs with the engine's key on the API
			*rec = serviceRecorder{}
			w := httptest.NewRecorder()
			api.ServeHTTP(w, tc.call.request())

			// Then: it is denied: the key is no principal's, and admits a
			// call only at the gateway
			if rec.reached != "" || w.Code != tc.status || !strings.Contains(w.Body.String(), "AccessDenied") {
				t.Fatalf("reached %q; status %d %s, want %d AccessDenied", rec.reached, w.Code, w.Body, tc.status)
			}
		})
	}
}
