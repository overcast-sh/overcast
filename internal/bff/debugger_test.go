package bff

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// The two console routes for the compute debugger (docs/plans/compute-debugger.md
// § 6) are plain pass-throughs: the path and query reach the emulator intact,
// and its status comes back unchanged — the 404 for an unknown resource is what
// the Debug tab renders as "off", so it must not be rewritten into a BFF error.
func TestDebuggerRoutesProxyPathQueryAndStatus(t *testing.T) {
	type seen struct{ path, query string }
	var last seen
	_, restore := stubEmulatorForProxyTests(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		last = seen{path: r.URL.Path, query: r.URL.RawQuery}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/_overcast/debugger/targets":
			_, _ = w.Write([]byte(`{"targets":[]}`))
		case "/_overcast/debugger/targets/lambda/my fn":
			_, _ = w.Write([]byte(`{"id":"lambda/my fn","enabled":false,"reason":"not tagged"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":"no such resource"}`))
		}
	}))
	defer restore()
	handler := NewHandler(nil, nil, UIConfig{})

	get := func(target string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
		return rec
	}

	t.Run("list", func(t *testing.T) {
		rec := get("/api/debugger/targets")
		if rec.Code != http.StatusOK || last.path != "/_overcast/debugger/targets" {
			t.Fatalf("status %d, upstream path %q", rec.Code, last.path)
		}
	})

	t.Run("one target, escaped resource, container query", func(t *testing.T) {
		rec := get("/api/debugger/targets/lambda/my%20fn?container=app")
		if rec.Code != http.StatusOK {
			t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
		}
		if last.path != "/_overcast/debugger/targets/lambda/my fn" || last.query != "container=app" {
			t.Fatalf("upstream path %q query %q", last.path, last.query)
		}
		var body struct {
			Reason string `json:"reason"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body.Reason != "not tagged" {
			t.Fatalf("body %s (err %v)", rec.Body.String(), err)
		}
	})

	t.Run("unknown resource keeps the emulator's 404", func(t *testing.T) {
		rec := get("/api/debugger/targets/ecs/gone")
		if rec.Code != http.StatusNotFound {
			t.Fatalf("status %d, want 404", rec.Code)
		}
		if rec.Body.String() != `{"error":"no such resource"}` {
			t.Fatalf("body %s", rec.Body.String())
		}
	})
}
