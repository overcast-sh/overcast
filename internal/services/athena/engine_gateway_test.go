package athena

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const gatewayTestKey = "OVERCASTATHENAKEY"

// gatewayRequest is a request to the gateway, signed with key for service
// when service is not empty.
type gatewayRequest struct {
	method, path, key, service string
	header                     map[string]string
	body                       string
}

func (c gatewayRequest) build() *http.Request {
	var body io.Reader
	if c.body != "" {
		body = strings.NewReader(c.body)
	}
	req := httptest.NewRequest(c.method, "http://host.docker.internal:9"+c.path, body)
	if c.service != "" {
		req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential="+c.key+"/20260925/us-east-1/"+c.service+"/aws4_request, SignedHeaders=host, Signature=00")
	}
	for k, v := range c.header {
		req.Header.Set(k, v)
	}
	return req
}

// recordingAPI is an EngineAPI whose handlers record which of them answered,
// and the Host each was handed.
type recordingAPI struct {
	reached, host string
}

func (a *recordingAPI) api() EngineAPI {
	handler := func(name string) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { a.reached, a.host = name, r.Host })
	}
	return EngineAPI{Glue: handler("glue"), S3: handler("s3"), Iceberg: handler("iceberg")}
}

func TestEngineOnly_signedRequests(t *testing.T) {
	// Given: the gateway in front of an API that records which handler answered
	rec := &recordingAPI{}
	h := engineOnly(gatewayTestKey, rec.api())
	cases := []struct {
		name string
		req  gatewayRequest
		want string // the handler reached; "" is refused
	}{
		{"signed for Glue", gatewayRequest{method: http.MethodPost, path: "/", key: gatewayTestKey, service: "glue", header: map[string]string{"X-Amz-Target": "AWSGlue.GetTable"}}, "glue"},
		{"signed for S3", gatewayRequest{method: http.MethodGet, path: "/bucket/key", key: gatewayTestKey, service: "s3"}, "s3"},
		{"signed for S3 Tables", gatewayRequest{method: http.MethodGet, path: "/iceberg/v1/config", key: gatewayTestKey, service: "s3tables"}, "iceberg"},
		// #2214: the signing name alone picks the handler, whatever else in
		// the request names another service.
		{"a Query body signed for S3", gatewayRequest{method: http.MethodPost, path: "/", key: gatewayTestKey, service: "s3", header: map[string]string{"Content-Type": "application/x-www-form-urlencoded"}, body: "Action=ListUsers&Version=2010-05-08"}, "s3"},
		{"a Glue target on Lambda's path, signed for Glue", gatewayRequest{method: http.MethodPost, path: "/2015-03-31/functions/f/invocations", key: gatewayTestKey, service: "glue", header: map[string]string{"X-Amz-Target": "AWSGlue.GetTable"}}, "glue"},

		{"signed with another key", gatewayRequest{method: http.MethodPost, path: "/", key: "AKIAOTHER", service: "glue", header: map[string]string{"X-Amz-Target": "AWSGlue.GetTable"}}, ""},
		{"signed with another key for S3 Tables", gatewayRequest{method: http.MethodGet, path: "/iceberg/v1/config", key: "AKIAOTHER", service: "s3tables"}, ""},
		{"unsigned", gatewayRequest{method: http.MethodGet, path: "/bucket/key"}, ""},
		{"signed for another service", gatewayRequest{method: http.MethodPost, path: "/", key: gatewayTestKey, service: "iam", header: map[string]string{"Content-Type": "application/x-www-form-urlencoded"}, body: "Action=ListUsers&Version=2010-05-08"}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// When: the request reaches the gateway
			*rec = recordingAPI{}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, c.req.build())

			// Then: it reaches the handler for its signing name, addressed to
			// localhost, or is refused
			switch {
			case rec.reached != c.want:
				t.Errorf("reached %q, want %q", rec.reached, c.want)
			case c.want == "" && w.Code != http.StatusForbidden:
				t.Errorf("status %d, want 403", w.Code)
			case c.want != "" && rec.host != "localhost":
				t.Errorf("served as addressed to %q, want localhost", rec.host)
			}
		})
	}
}

func TestEngineOnly_withS3TablesDisabled(t *testing.T) {
	// Given: an API without S3 Tables
	rec := &recordingAPI{}
	api := rec.api()
	api.Iceberg = nil
	h := engineOnly(gatewayTestKey, api)

	// When: the engine's Iceberg catalog calls it
	w := httptest.NewRecorder()
	h.ServeHTTP(w, gatewayRequest{method: http.MethodGet, path: "/iceberg/v1/config", key: gatewayTestKey, service: "s3tables"}.build())

	// Then: the call is refused rather than handed to another service
	if w.Code != http.StatusForbidden || rec.reached != "" {
		t.Errorf("status %d, reached %q; want 403 reaching nothing", w.Code, rec.reached)
	}
}
