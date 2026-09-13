package router

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/overcast-sh/overcast/internal/config"
	"github.com/overcast-sh/overcast/internal/state"
)

type fakeDynamoDebugProvider struct{}

func (fakeDynamoDebugProvider) DebugNamespace() string { return "dynamodb:items" }

func (fakeDynamoDebugProvider) DebugStateKeys(context.Context) ([]string, error) {
	return []string{"Music/artist-1/song-1"}, nil
}

func (fakeDynamoDebugProvider) DebugStateValues(context.Context) (map[string]string, error) {
	return map[string]string{"Music/artist-1/song-1": `{"pk":{"S":"artist-1"}}`}, nil
}

func (fakeDynamoDebugProvider) DebugResetState(context.Context) error { return nil }

// fakeLogsDebugProvider mirrors fakeDynamoDebugProvider for the CloudWatch
// Logs events virtual namespace ("logs:events" — storage-plan.md 2.3).
type fakeLogsDebugProvider struct{}

func (fakeLogsDebugProvider) DebugNamespace() string { return "logs:events" }

func (fakeLogsDebugProvider) DebugStateKeys(context.Context) ([]string, error) {
	return []string{"us-east-1/my-group/my-stream/1700000000000/0"}, nil
}

func (fakeLogsDebugProvider) DebugStateValues(context.Context) (map[string]string, error) {
	return map[string]string{
		"us-east-1/my-group/my-stream/1700000000000/0": `{"timestamp":1700000000000,"message":"hello"}`,
	}, nil
}

func (fakeLogsDebugProvider) DebugResetState(context.Context) error { return nil }

func TestDebugState_includesAppSyncAndAPIGatewayNamespaces(t *testing.T) {
	// Given: raw state exists for services beyond the original debug allowlist.
	store := state.NewMemoryStore()
	ctx := context.Background()
	if err := store.Set(ctx, "appsync", "us-east-1:ds:api-id:NamespaceDS", `{"name":"NamespaceDS"}`); err != nil {
		t.Fatal(err)
	}
	if err := store.Set(ctx, "apigw:restapis", "us-east-1:api-id", `{"id":"api-id"}`); err != nil {
		t.Fatal(err)
	}

	// When: the raw state summary is requested.
	req := httptest.NewRequest(http.MethodGet, "/_overcast/debug/state", nil)
	rec := httptest.NewRecorder()
	debugState(store, nil).ServeHTTP(rec, req)

	// Then: AppSync and API Gateway namespaces are visible in the summary.
	body := rec.Body.String()
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, body)
	}
	if !containsDebugBody(body, `"appsync"`) {
		t.Fatalf("expected appsync namespace in debug state summary, got %s", body)
	}
	if !containsDebugBody(body, `"apigw:restapis"`) {
		t.Fatalf("expected apigw namespace in debug state summary, got %s", body)
	}
}

func TestDebugState_includesDynamoDBItemsVirtualNamespace(t *testing.T) {
	// Given: DynamoDB has item data in its dedicated item backend.
	store := state.NewMemoryStore()
	dynamo := fakeDynamoDebugProvider{}
	providers := []DebugStateProvider{dynamo}

	// When: the raw state summary is requested.
	req := httptest.NewRequest(http.MethodGet, "/_overcast/debug/state", nil)
	rec := httptest.NewRecorder()
	debugState(store, providers).ServeHTTP(rec, req)

	// Then: DynamoDB items are exposed as a virtual debug namespace.
	body := rec.Body.String()
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, body)
	}
	if !containsDebugBody(body, `"dynamodb:items"`) {
		t.Fatalf("expected dynamodb item namespace in debug state summary, got %s", body)
	}

	// And: fetching that namespace returns raw item JSON values.
	req = httptest.NewRequest(http.MethodGet, "/_overcast/debug/state/dynamodb:items", nil)
	rec = httptest.NewRecorder()
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("namespace", "dynamodb:items")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	debugStateNamespace(store, providers).ServeHTTP(rec, req)
	body = rec.Body.String()
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, body)
	}
	if !containsDebugBody(body, `Music/artist-1/song-1`) {
		t.Fatalf("expected dynamodb item key in namespace response, got %s", body)
	}
}

func TestDebugState_includesSQSMessagesNamespace(t *testing.T) {
	// Given: SQS message state exists in the shared store.
	store := state.NewMemoryStore()
	ctx := context.Background()
	if err := store.Set(ctx, "sqs:messages", "us-east-1/orders/msg-1", `{"body":"hello"}`); err != nil {
		t.Fatal(err)
	}

	// When: the raw state summary is requested.
	req := httptest.NewRequest(http.MethodGet, "/_overcast/debug/state", nil)
	rec := httptest.NewRecorder()
	debugState(store, nil).ServeHTTP(rec, req)

	// Then: SQS messages are visible through dynamic namespace discovery.
	body := rec.Body.String()
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, body)
	}
	if !containsDebugBody(body, `"sqs:messages"`) {
		t.Fatalf("expected sqs messages namespace in debug state summary, got %s", body)
	}
}

func TestDebugStateNamespace_truncatesLargeValues(t *testing.T) {
	// Given: a namespace has a very large raw string value such as a Lambda layer zip payload.
	store := state.NewMemoryStore()
	ctx := context.Background()
	largeValue := `{"layer_name":"deps","content":"` + strings.Repeat("A", debugStateValuePreviewBytes+1024) + `"}`
	if err := store.Set(ctx, "lambda:layers", "us-east-1/deps:0000000001", largeValue); err != nil {
		t.Fatal(err)
	}

	// When: the raw state namespace is requested.
	req := httptest.NewRequest(http.MethodGet, "/_overcast/debug/state/lambda:layers", nil)
	rec := httptest.NewRecorder()
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("namespace", "lambda:layers")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	debugStateNamespace(store, nil).ServeHTTP(rec, req)

	// Then: the value is capped for UI rendering and marked as truncated.
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var body debugStateNamespacePage
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	got := body.Values["us-east-1/deps:0000000001"]
	var layer map[string]string
	if err := json.Unmarshal([]byte(got), &layer); err != nil {
		t.Fatalf("expected valid JSON after truncation: %v; body=%q", err, got)
	}
	if layer["layer_name"] != "deps" {
		t.Fatalf("expected metadata to remain readable, got %q", layer["layer_name"])
	}
	if !strings.HasSuffix(layer["content"], debugStateTruncatedSuffix) {
		t.Fatalf("expected truncation marker, got suffix %q", layer["content"][len(layer["content"])-32:])
	}
	if len(layer["content"]) != debugStateValuePreviewBytes+len(debugStateTruncatedSuffix) {
		t.Fatalf("expected capped content length, got %d", len(layer["content"]))
	}
}

func TestDebugStateNamespace_truncatesLargePlainTextValues(t *testing.T) {
	// Given: a namespace has a large non-JSON raw string value.
	store := state.NewMemoryStore()
	ctx := context.Background()
	largeValue := strings.Repeat("A", debugStateValuePreviewBytes+1024)
	if err := store.Set(ctx, "debug:plain", "record", largeValue); err != nil {
		t.Fatal(err)
	}

	// When: the raw state namespace is requested.
	req := httptest.NewRequest(http.MethodGet, "/_overcast/debug/state/debug:plain", nil)
	rec := httptest.NewRecorder()
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("namespace", "debug:plain")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	debugStateNamespace(store, nil).ServeHTTP(rec, req)

	// Then: the plain value is capped for UI rendering and marked as truncated.
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var body debugStateNamespacePage
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	got := body.Values["record"]
	if !strings.HasSuffix(got, debugStateTruncatedSuffix) {
		t.Fatalf("expected truncation marker, got suffix %q", got[len(got)-32:])
	}
	if len(got) != debugStateValuePreviewBytes+len(debugStateTruncatedSuffix) {
		t.Fatalf("expected capped value length, got %d", len(got))
	}
}

func TestDebugStateNamespaceKey_returnsRawJSONValue(t *testing.T) {
	// Given: a selected raw state value is itself JSON and larger than the UI preview cap.
	store := state.NewMemoryStore()
	ctx := context.Background()
	key := "us-east-1/deps:0000000001"
	largeValue := `{"layer_name":"deps","content":"` + strings.Repeat("A", debugStateValuePreviewBytes+1024) + `"}`
	if err := store.Set(ctx, "lambda:layers", key, largeValue); err != nil {
		t.Fatal(err)
	}

	// When: the selected value is requested directly.
	req := httptest.NewRequest(http.MethodGet, "/_overcast/debug/state/lambda:layers?key="+key, nil)
	rec := httptest.NewRecorder()
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("namespace", "lambda:layers")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	debugStateNamespace(store, nil).ServeHTTP(rec, req)

	// Then: the full raw JSON value is returned without UI truncation.
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("expected JSON content-type, got %q", got)
	}
	if got := strings.TrimSpace(rec.Body.String()); got != largeValue {
		t.Fatalf("expected full raw value, got length %d want %d", len(got), len(largeValue))
	}
}

func TestDebugStateNamespaceKey_returnsRawTextValue(t *testing.T) {
	// Given: a selected raw state value is plain text.
	store := state.NewMemoryStore()
	ctx := context.Background()
	if err := store.Set(ctx, "debug:plain", "record", "not json"); err != nil {
		t.Fatal(err)
	}

	// When: the selected value is requested directly.
	req := httptest.NewRequest(http.MethodGet, "/_overcast/debug/state/debug:plain?key=record", nil)
	rec := httptest.NewRecorder()
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("namespace", "debug:plain")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	debugStateNamespace(store, nil).ServeHTTP(rec, req)

	// Then: the value is served as plain text for the browser to display or download.
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); got != "text/plain; charset=utf-8" {
		t.Fatalf("expected text content-type, got %q", got)
	}
	if got := rec.Body.String(); got != "not json" {
		t.Fatalf("unexpected body %q", got)
	}
}

// TestDebugState_includesLogsEventsVirtualNamespace is the CloudWatch Logs
// equivalent of TestDebugState_includesDynamoDBItemsVirtualNamespace —
// storage-plan.md 2.3 requires log events (now stored in a dedicated
// logs_events SQL table, not the generic kv store) to stay visible to
// /_overcast/debug/state via the same DebugStateProvider mechanism DynamoDB uses.
func TestDebugState_includesLogsEventsVirtualNamespace(t *testing.T) {
	// Given: CloudWatch Logs has event data in its dedicated event backend.
	store := state.NewMemoryStore()
	logsProvider := fakeLogsDebugProvider{}
	providers := []DebugStateProvider{logsProvider}

	// When: the raw state summary is requested.
	req := httptest.NewRequest(http.MethodGet, "/_overcast/debug/state", nil)
	rec := httptest.NewRecorder()
	debugState(store, providers).ServeHTTP(rec, req)

	// Then: log events are exposed as a virtual debug namespace.
	body := rec.Body.String()
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, body)
	}
	if !containsDebugBody(body, `"logs:events"`) {
		t.Fatalf("expected logs:events namespace in debug state summary, got %s", body)
	}

	// And: fetching that namespace returns raw event JSON values.
	req = httptest.NewRequest(http.MethodGet, "/_overcast/debug/state/logs:events", nil)
	rec = httptest.NewRecorder()
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("namespace", "logs:events")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	debugStateNamespace(store, providers).ServeHTTP(rec, req)
	body = rec.Body.String()
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, body)
	}
	if !containsDebugBody(body, `us-east-1/my-group/my-stream/1700000000000/0`) {
		t.Fatalf("expected log event key in namespace response, got %s", body)
	}
	if !containsDebugBody(body, `hello`) {
		t.Fatalf("expected log event message in namespace response, got %s", body)
	}
}

// ---- 3.13: /_overcast/debug/state/{namespace} pagination -----------------------------

func TestDebugStateNamespace_paginatesStoreBackedNamespace(t *testing.T) {
	// Given: a namespace with three keys.
	store := state.NewMemoryStore()
	ctx := context.Background()
	for _, k := range []string{"a", "b", "c"} {
		if err := store.Set(ctx, "test:ns", k, `"v-`+k+`"`); err != nil {
			t.Fatal(err)
		}
	}

	// When: the first page is requested with limit=2.
	page1 := fetchDebugStateNamespacePage(t, store, nil, "test:ns", "", "2")

	// Then: it returns the first two keys in order and a nextKey cursor.
	if len(page1.Values) != 2 {
		t.Fatalf("expected 2 values on page 1, got %d: %+v", len(page1.Values), page1.Values)
	}
	if _, ok := page1.Values["a"]; !ok {
		t.Errorf("expected key %q on page 1, got %+v", "a", page1.Values)
	}
	if _, ok := page1.Values["b"]; !ok {
		t.Errorf("expected key %q on page 1, got %+v", "b", page1.Values)
	}
	if page1.NextKey != "b" {
		t.Fatalf("expected nextKey %q, got %q", "b", page1.NextKey)
	}

	// When: the second page is requested using the first page's nextKey.
	page2 := fetchDebugStateNamespacePage(t, store, nil, "test:ns", page1.NextKey, "2")

	// Then: it returns exactly the remaining key, and signals no further page.
	if len(page2.Values) != 1 {
		t.Fatalf("expected 1 value on page 2, got %d: %+v", len(page2.Values), page2.Values)
	}
	if _, ok := page2.Values["c"]; !ok {
		t.Errorf("expected key %q on page 2, got %+v", "c", page2.Values)
	}
	if page2.NextKey != "" {
		t.Fatalf("expected empty nextKey on the last page, got %q", page2.NextKey)
	}
}

func TestDebugStateNamespace_defaultLimitAppliedWhenAbsent(t *testing.T) {
	// Given: a namespace with fewer keys than the default page limit.
	store := state.NewMemoryStore()
	ctx := context.Background()
	if err := store.Set(ctx, "test:ns", "only-key", `"v"`); err != nil {
		t.Fatal(err)
	}

	// When: the namespace is requested with no ?limit= at all.
	page := fetchDebugStateNamespacePage(t, store, nil, "test:ns", "", "")

	// Then: every key is returned in a single page (well under the default cap).
	if len(page.Values) != 1 {
		t.Fatalf("expected 1 value, got %d: %+v", len(page.Values), page.Values)
	}
	if page.NextKey != "" {
		t.Fatalf("expected empty nextKey, got %q", page.NextKey)
	}
}

func TestParseDebugStateLimit(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want int
	}{
		{"absent", "", debugStateDefaultPageLimit},
		{"non-numeric", "not-a-number", debugStateDefaultPageLimit},
		{"zero", "0", debugStateDefaultPageLimit},
		{"negative", "-5", debugStateDefaultPageLimit},
		{"valid", "10", 10},
		{"exceeds max", "999999", debugStateMaxPageLimit},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseDebugStateLimit(tc.raw); got != tc.want {
				t.Errorf("parseDebugStateLimit(%q) = %d, want %d", tc.raw, got, tc.want)
			}
		})
	}
}

// fakeMultiKeyDebugProvider is a DebugStateProvider with more than one key,
// used to exercise pagination over a provider-backed virtual namespace
// (which has no ScanPage of its own — see paginateDebugStateValues).
type fakeMultiKeyDebugProvider struct{}

func (fakeMultiKeyDebugProvider) DebugNamespace() string { return "fake:multi" }

func (fakeMultiKeyDebugProvider) DebugStateKeys(context.Context) ([]string, error) {
	return []string{"k1", "k2", "k3"}, nil
}

func (fakeMultiKeyDebugProvider) DebugStateValues(context.Context) (map[string]string, error) {
	return map[string]string{"k1": `"v1"`, "k2": `"v2"`, "k3": `"v3"`}, nil
}

func (fakeMultiKeyDebugProvider) DebugResetState(context.Context) error { return nil }

func TestDebugStateNamespace_paginatesProviderBackedNamespace(t *testing.T) {
	// Given: a virtual (provider-backed) namespace with three keys.
	store := state.NewMemoryStore()
	providers := []DebugStateProvider{fakeMultiKeyDebugProvider{}}

	// When: the first page is requested with limit=2.
	page1 := fetchDebugStateNamespacePage(t, store, providers, "fake:multi", "", "2")

	// Then: pagination applies the same way it does for a store-backed namespace.
	if len(page1.Values) != 2 {
		t.Fatalf("expected 2 values on page 1, got %d: %+v", len(page1.Values), page1.Values)
	}
	if page1.NextKey != "k2" {
		t.Fatalf("expected nextKey %q, got %q", "k2", page1.NextKey)
	}

	// When: the second page follows the cursor.
	page2 := fetchDebugStateNamespacePage(t, store, providers, "fake:multi", page1.NextKey, "2")

	// Then: the remaining key is returned and pagination terminates.
	if len(page2.Values) != 1 {
		t.Fatalf("expected 1 value on page 2, got %d: %+v", len(page2.Values), page2.Values)
	}
	if _, ok := page2.Values["k3"]; !ok {
		t.Errorf("expected key %q on page 2, got %+v", "k3", page2.Values)
	}
	if page2.NextKey != "" {
		t.Fatalf("expected empty nextKey on the last page, got %q", page2.NextKey)
	}
}

// fetchDebugStateNamespacePage issues a GET /_overcast/debug/state/{namespace} request
// with the given after/limit query parameters and decodes the paginated
// response.
func fetchDebugStateNamespacePage(t *testing.T, store state.Store, providers []DebugStateProvider, namespace, after, limit string) debugStateNamespacePage {
	t.Helper()
	url := "/_overcast/debug/state/" + namespace + "?"
	if after != "" {
		url += "after=" + after + "&"
	}
	if limit != "" {
		url += "limit=" + limit
	}
	req := httptest.NewRequest(http.MethodGet, url, nil)
	rec := httptest.NewRecorder()
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("namespace", namespace)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	debugStateNamespace(store, providers).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var page debugStateNamespacePage
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return page
}

// ---- 3.6: /_overcast/debug/metrics ----------------------------------------------------

// TestDebugMetrics_reportsCountersForMemoryStore proves MemoryStore — which
// has no async write path, background seed, or persistent backend — still
// shows up in the endpoint's store list, now that every Store implementation
// reports at least its storage-activity counters (see
// state.DebugMetricsReporter's doc comment). Every other DebugMetrics field
// stays at its zero value.
func TestDebugMetrics_reportsCountersForMemoryStore(t *testing.T) {
	// Given: a MemoryStore that has served one write and one read.
	store := state.NewMemoryStore()
	ctx := context.Background()
	if err := store.Set(ctx, "s3", "bucket-1", "v"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if _, _, err := store.Get(ctx, "s3", "bucket-1"); err != nil {
		t.Fatalf("Get: %v", err)
	}
	cfg := &config.Config{State: config.StateBackendMemory}

	// When: the metrics endpoint is requested.
	req := httptest.NewRequest(http.MethodGet, "/_overcast/debug/metrics", nil)
	rec := httptest.NewRecorder()
	debugMetrics(cfg, store, nil, nil, nil, nil).ServeHTTP(rec, req)

	// Then: it responds 200 with one "memory" mode entry reporting counters.
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp debugMetricsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(resp.Stores) != 1 {
		t.Fatalf("expected one reporting store for MemoryStore, got %+v", resp.Stores)
	}
	if resp.Stores[0].Mode != "memory" {
		t.Errorf("expected mode %q, got %q", "memory", resp.Stores[0].Mode)
	}
	if resp.Stores[0].Counters.Writes != 1 {
		t.Errorf("expected 1 write, got %d", resp.Stores[0].Counters.Writes)
	}
	if resp.Stores[0].Counters.Reads != 1 {
		t.Errorf("expected 1 read, got %d", resp.Stores[0].Counters.Reads)
	}
}

// TestDebugMetrics_advisoriesReflectMemoryModeAndAreNeverNull proves the
// endpoint's Advisories field is wired to the real config/store data (not
// just a hardcoded empty list) and stays a non-null (possibly empty) array —
// the same "never null" contract Stores already has, since the web UI's
// Metrics & Health page renders Advisories generically without a null check.
func TestDebugMetrics_advisoriesReflectMemoryModeAndAreNeverNull(t *testing.T) {
	// Given: OVERCAST_STATE=memory, which the memory-mode advisory rule
	// exists to surface (informational — see advisories.go).
	store := state.NewMemoryStore()
	cfg := &config.Config{State: config.StateBackendMemory}

	// When: the metrics endpoint is requested.
	req := httptest.NewRequest(http.MethodGet, "/_overcast/debug/metrics", nil)
	rec := httptest.NewRecorder()
	debugMetrics(cfg, store, nil, nil, nil, nil).ServeHTTP(rec, req)

	// Then: the response carries a non-null Advisories array including the
	// memory-mode advisory.
	var resp debugMetricsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Advisories == nil {
		t.Fatal("expected a non-nil (possibly empty) advisories list")
	}
	found := false
	for _, a := range resp.Advisories {
		if a.Code == advisoryCodeMemoryMode {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected the %q advisory for OVERCAST_STATE=memory, got %+v", advisoryCodeMemoryMode, resp.Advisories)
	}
}

func containsDebugBody(body, want string) bool {
	for i := 0; i+len(want) <= len(body); i++ {
		if body[i:i+len(want)] == want {
			return true
		}
	}
	return false
}
