package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCORS_answersPreflightsItself(t *testing.T) {
	// Given: an ordinary request host
	called := false
	h := CORS(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))

	// When: a preflight arrives
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodOptions, "http://localhost:4566/", nil)
	h.ServeHTTP(rec, req)

	// Then: the middleware answers and the handler is not reached
	if rec.Code != http.StatusNoContent || called || rec.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Errorf("code=%d called=%v origin=%q", rec.Code, called, rec.Header().Get("Access-Control-Allow-Origin"))
	}
}

func TestCORS_leavesLambdaWebEndpointsToTheApplication(t *testing.T) {
	// Given: a Lambda Web Function endpoint's own host
	var sawOptions bool
	h := CORS(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawOptions = r.Method == http.MethodOptions
		w.WriteHeader(http.StatusNoContent)
	}))

	// When: a preflight arrives
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodOptions, "http://abc123.def456.lambda-web.us-east-1.localhost:4566/", nil)
	h.ServeHTTP(rec, req)

	// Then: the application sees it and no CORS header was added on its behalf
	if !sawOptions {
		t.Error("the preflight did not reach the application")
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("Access-Control-Allow-Origin = %q, want none", got)
	}
}
