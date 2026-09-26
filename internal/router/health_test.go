package router

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/overcast-sh/overcast/internal/config"
	"github.com/overcast-sh/overcast/internal/listenstatus"
	"github.com/overcast-sh/overcast/internal/services/athena"
	"github.com/overcast-sh/overcast/internal/state"
)

func TestInfoHandlerIncludesDebugFlag(t *testing.T) {
	handler := newInfoHandler(&config.Config{
		Region:    "ap-southeast-2",
		AccountID: "123456789012",
		Version:   "test-version",
		Debug:     true,
	})
	req := httptest.NewRequest(http.MethodGet, "/_overcast/info", nil)
	rec := httptest.NewRecorder()

	handler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	var got infoResponse
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !got.Debug {
		t.Fatalf("debug = false, want true")
	}
}

// TestInfoHandler_reportsIAMEnforcement covers the flag the web UI reads to
// tell an emulator-issued AccessDenied apart from an application bug. It is
// off unless the operator turned it on.
func TestInfoHandler_reportsIAMEnforcement(t *testing.T) {
	// Given: the default configuration
	handler := newInfoHandler(&config.Config{})
	req := httptest.NewRequest(http.MethodGet, "/_overcast/info", nil)
	rec := httptest.NewRecorder()

	// When: the info endpoint is called
	handler(rec, req)

	// Then: enforcement reads as off
	var got infoResponse
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got.IAMEnforce {
		t.Fatal("iam_enforce = true by default, want false")
	}

	// Given: enforcement switched on
	handler = newInfoHandler(&config.Config{EnforceIAM: true})
	rec = httptest.NewRecorder()

	// When/Then: the endpoint says so
	handler(rec, httptest.NewRequest(http.MethodGet, "/_overcast/info", nil))
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !got.IAMEnforce {
		t.Fatal("iam_enforce = false with OVERCAST_ENFORCE_IAM set, want true")
	}
}

// TestHealthHandler_reportsAutoStateProvenance verifies that /_overcast/health's
// storage.configured field distinguishes "what was configured" from
// storage.default's "what backend is actually in effect" — the case that
// matters is Default: memory, Configured: auto, which means the
// OVERCAST_STATE=auto resolver picked memory because it found no evidence of
// persistence intent, not that anyone explicitly set OVERCAST_STATE=memory.
func TestHealthHandler_reportsAutoStateProvenance(t *testing.T) {
	cfg := &config.Config{
		Region:          "us-east-1",
		AccountID:       "000000000000",
		State:           config.StateBackendMemory,
		StateConfigured: "auto",
		StateSource:     config.StateSourceAuto,
	}
	handler := newHealthHandler(cfg, state.NewMemoryStore(), nil, nil, nil, healthSources{})
	req := httptest.NewRequest(http.MethodGet, "/_overcast/health", nil)
	rec := httptest.NewRecorder()

	handler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	var got healthResponse
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got.Storage.Default != "memory" {
		t.Errorf("storage.default = %q, want %q", got.Storage.Default, "memory")
	}
	if got.Storage.Configured != "auto" {
		t.Errorf("storage.configured = %q, want %q", got.Storage.Configured, "auto")
	}
}

// TestHealthHandler_omitsConfiguredWhenNotPopulated verifies the additive
// nature of storage.configured: a Config built directly (not via
// config.Load()) leaves StateConfigured at its zero value, and the field
// must be omitted from the JSON response rather than emitted as "" — this is
// the shape every pre-existing caller of /_overcast/health (and every test built
// against tests/helpers.NewTestServer, which constructs Config directly)
// already expects, so it must not regress when this field was added.
func TestHealthHandler_omitsConfiguredWhenNotPopulated(t *testing.T) {
	cfg := &config.Config{
		Region:    "us-east-1",
		AccountID: "000000000000",
		State:     config.StateBackendMemory,
	}
	handler := newHealthHandler(cfg, state.NewMemoryStore(), nil, nil, nil, healthSources{})
	req := httptest.NewRequest(http.MethodGet, "/_overcast/health", nil)
	rec := httptest.NewRecorder()

	handler(rec, req)

	if !responseOmitsConfiguredKey(rec.Body.Bytes()) {
		t.Errorf("expected storage.configured to be omitted from the response, got %s", rec.Body.String())
	}
}

// responseOmitsConfiguredKey reports whether the raw JSON response's
// "storage" object has no "configured" key at all (as opposed to
// "configured":""), confirming the omitempty tag actually omits it.
func responseOmitsConfiguredKey(body []byte) bool {
	var raw struct {
		Storage map[string]json.RawMessage `json:"storage"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return false
	}
	_, present := raw.Storage["configured"]
	return !present
}

// TestHealthHandler_reportsListenersAndDegradesOnABindFailure covers the
// listeners section: the SMTP capture server bound after falling back from a
// busy default, and the Lambda Runtime API failed to bind — the second Overcast
// on one host shape. Anyone polling health sees the failure, the reason and
// the fix, and the overall status turns degraded.
func TestHealthHandler_reportsListenersAndDegradesOnABindFailure(t *testing.T) {
	// Given: one listener fell back and one failed
	reported := map[string]listenstatus.Status{
		listenstatus.SMTP:             {State: listenstatus.Listening, Addr: "127.0.0.1:49152", FellBack: true},
		listenstatus.LambdaRuntimeAPI: {State: listenstatus.Failed, Error: "address already in use", Fix: "set LAMBDA_RUNTIME_API_PORT to a free port, or 0 for an ephemeral one"},
	}
	cfg := &config.Config{Region: "us-east-1", AccountID: "000000000000", State: config.StateBackendMemory}
	handler := newHealthHandler(cfg, state.NewMemoryStore(), nil, nil, nil, healthSources{Listeners: func() map[string]listenstatus.Status { return reported }})
	rec := httptest.NewRecorder()

	// When: health is polled
	handler(rec, httptest.NewRequest(http.MethodGet, "/_overcast/health", nil))

	// Then: still 200 — the API is serving — but degraded, with both outcomes echoed
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	var got healthResponse
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got.Status != "degraded" {
		t.Errorf("status = %q, want %q", got.Status, "degraded")
	}
	if l := got.Listeners[listenstatus.SMTP]; l.State != listenstatus.Listening || !l.FellBack || l.Addr != "127.0.0.1:49152" {
		t.Errorf("listeners.smtp = %+v, want listening on the fallback port", l)
	}
	if l := got.Listeners[listenstatus.LambdaRuntimeAPI]; l.State != listenstatus.Failed || l.Error == "" || l.Fix == "" {
		t.Errorf("listeners.lambdaRuntimeApi = %+v, want failed with a reason and a fix", l)
	}
}

// TestHealthHandler_omitsListenersUntilOneReports verifies the additive shape:
// with nothing reported the key is absent, not an empty object, and a fallback
// alone (the listener works, just elsewhere) does not degrade the status.
func TestHealthHandler_omitsListenersUntilOneReports(t *testing.T) {
	// Given: no listener has reported
	cfg := &config.Config{Region: "us-east-1", AccountID: "000000000000", State: config.StateBackendMemory}
	handler := newHealthHandler(cfg, state.NewMemoryStore(), nil, nil, nil, healthSources{Listeners: func() map[string]listenstatus.Status { return nil }})
	rec := httptest.NewRecorder()

	// When: health is polled
	handler(rec, httptest.NewRequest(http.MethodGet, "/_overcast/health", nil))

	// Then: the key is absent and the status is ok
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if _, present := raw["listeners"]; present {
		t.Errorf("expected listeners to be omitted, got %s", raw["listeners"])
	}
	var status string
	if err := json.Unmarshal(raw["status"], &status); err != nil || status != "ok" {
		t.Errorf("status = %s, want ok", raw["status"])
	}

	// Given: a listener bound on a fallback port
	fellBack := map[string]listenstatus.Status{listenstatus.SMTP: {State: listenstatus.Listening, Addr: "127.0.0.1:49152", FellBack: true}}
	handler = newHealthHandler(cfg, state.NewMemoryStore(), nil, nil, nil, healthSources{Listeners: func() map[string]listenstatus.Status { return fellBack }})
	rec = httptest.NewRecorder()

	// When/Then: that is reported, but it is not a degradation
	handler(rec, httptest.NewRequest(http.MethodGet, "/_overcast/health", nil))
	var got healthResponse
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got.Status != "ok" {
		t.Errorf("status = %q after a fallback, want %q", got.Status, "ok")
	}
	if !got.Listeners[listenstatus.SMTP].FellBack {
		t.Errorf("listeners.smtp = %+v, want fellBack", got.Listeners[listenstatus.SMTP])
	}
}

// A starting engine is the first query's cold start, not a fault: health
// reports it, and stays ok.
func TestHealthHandler_athenaEngineStartingStaysOK(t *testing.T) {
	// Given: an Athena engine pulling its image
	cfg := &config.Config{Region: "us-east-1", AccountID: "000000000000", State: config.StateBackendMemory}
	engine := func() athena.EngineStatus {
		return athena.EngineStatus{Engine: config.AthenaEngineTrino, State: athena.EnginePulling, MemoryBytes: 2 << 30}
	}
	handler := newHealthHandler(cfg, state.NewMemoryStore(), nil, nil, nil, healthSources{AthenaEngine: engine})
	rec := httptest.NewRecorder()

	// When: health is asked
	handler(rec, httptest.NewRequest(http.MethodGet, "/_overcast/health", nil))

	// Then: it carries the engine's status and is still ok
	var got healthResponse
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got.Status != "ok" || got.AthenaEngine == nil || got.AthenaEngine.State != athena.EnginePulling || got.AthenaEngine.MemoryBytes != 2<<30 {
		t.Fatalf("health = %+v, engine = %+v", got, got.AthenaEngine)
	}
}

func TestHealthHandler_omitsAthenaEngineWhenAthenaIsOff(t *testing.T) {
	cfg := &config.Config{Region: "us-east-1", AccountID: "000000000000", State: config.StateBackendMemory}
	handler := newHealthHandler(cfg, state.NewMemoryStore(), nil, nil, nil, healthSources{})
	rec := httptest.NewRecorder()

	handler(rec, httptest.NewRequest(http.MethodGet, "/_overcast/health", nil))

	var got map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if _, ok := got["athenaEngine"]; ok {
		t.Fatalf("athenaEngine present without Athena: %v", got)
	}
}
