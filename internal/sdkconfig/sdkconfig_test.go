package sdkconfig

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
)

// A handler that calls its own router in process must reach the route it
// asks for, not be routed again as itself.
func TestInProcess_nestedCallIsRoutedAfresh(t *testing.T) {
	// Given: a router whose /api/outer handler, on a mounted subrouter, calls
	// /inner/{name} in process
	r := chi.NewRouter()
	r.Get("/inner/{name}", func(w http.ResponseWriter, req *http.Request) {
		_, _ = io.WriteString(w, "inner "+chi.URLParam(req, "name"))
	})
	r.Route("/api", func(api chi.Router) {
		api.Get("/outer/{id}", func(w http.ResponseWriter, req *http.Request) {
			client := InProcess(r, "us-east-1").HTTPClient
			inner, err := http.NewRequestWithContext(req.Context(), http.MethodGet, inProcessEndpoint+"/inner/leaf", nil)
			if err != nil {
				t.Error(err)
				return
			}
			resp, err := client.Do(inner)
			if err != nil {
				t.Error(err)
				return
			}
			defer resp.Body.Close()
			body, _ := io.ReadAll(resp.Body)
			w.WriteHeader(resp.StatusCode)
			_, _ = w.Write(body)
		})
	})

	// When: /api/outer is served
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/outer/7", nil))

	// Then: the nested call reached /inner with its own parameters
	if rec.Code != http.StatusOK || rec.Body.String() != "inner leaf" {
		t.Fatalf("got %d %q, want 200 \"inner leaf\"", rec.Code, rec.Body.String())
	}
}
