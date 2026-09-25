package athena

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// trino_fake_test.go — a stand-in for Trino's REST client protocol, so the
// client, the runner and the engine manager are tested without the engine.
//
// Each statement answers with its scripted pages in turn: the POST returns
// the first, and each nextUri the next. A page that is left without a
// nextUri ends the query. A statement with no script runs as "SELECT 1".

type fakeTrino struct {
	srv *httptest.Server

	mu        sync.Mutex
	scripts   map[string][]trinoResponse
	sessions  []trinoSession
	deleted   []string
	starting  bool
	statement []string
	// busy answers the next busy statement requests 503, as a loaded
	// coordinator does; status, when set, answers every one with it.
	busy   int
	status int
}

func newFakeTrino(t *testing.T) *fakeTrino {
	t.Helper()
	f := &fakeTrino{scripts: map[string][]trinoResponse{}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

// script sets the pages sql answers with.
func (f *fakeTrino) script(sql string, pages ...trinoResponse) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.scripts[sql] = pages
}

// selectOne is the pages of SELECT 1: queued, then the column and the row.
func selectOne() []trinoResponse {
	return []trinoResponse{
		{Stats: trinoStats{State: "QUEUED"}},
		{Columns: []trinoColumn{{Name: "_col0", Type: "integer", TypeSignature: trinoTypeSignature{RawType: "integer"}}},
			Data: [][]any{{json.Number("1")}}, Stats: trinoStats{State: "FINISHED", QueuedTimeMillis: 5, PlanningTimeMillis: 7, PhysicalInputBytes: 11, ProcessedRows: 1}},
	}
}

func (f *fakeTrino) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case strings.HasPrefix(r.URL.Path, "/v1/statement") && f.status != 0:
		w.WriteHeader(f.status)
	case strings.HasPrefix(r.URL.Path, "/v1/statement") && f.busy > 0:
		f.busy--
		w.WriteHeader(http.StatusServiceUnavailable)
	case r.URL.Path == "/v1/info":
		_ = json.NewEncoder(w).Encode(map[string]any{"starting": f.starting})
	case r.Method == http.MethodPost && r.URL.Path == "/v1/statement":
		body, _ := io.ReadAll(r.Body)
		sql := string(body)
		f.statement = append(f.statement, sql)
		f.sessions = append(f.sessions, trinoSession{Catalog: r.Header.Get("X-Trino-Catalog"), Schema: r.Header.Get("X-Trino-Schema")})
		f.page(w, len(f.statement)-1, 0)
	case r.Method == http.MethodDelete:
		f.deleted = append(f.deleted, r.URL.Path)
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/statement/executing/"):
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/v1/statement/executing/"), "/")
		q, _ := strconv.Atoi(parts[0])
		n, _ := strconv.Atoi(parts[1])
		f.page(w, q, n)
	default:
		http.NotFound(w, r)
	}
}

// page writes page n of query q; the caller holds mu.
func (f *fakeTrino) page(w http.ResponseWriter, q, n int) {
	pages, ok := f.scripts[f.statement[q]]
	if !ok {
		pages = selectOne()
	}
	page := pages[n]
	page.ID = "q" + strconv.Itoa(q)
	if n+1 < len(pages) {
		page.NextURI = f.srv.URL + "/v1/statement/executing/" + strconv.Itoa(q) + "/" + strconv.Itoa(n+1)
	}
	_ = json.NewEncoder(w).Encode(page)
}

func (f *fakeTrino) deletes() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.deleted...)
}
