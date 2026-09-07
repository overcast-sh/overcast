package lambda

// handler_concurrency_test.go — reserved and provisioned concurrency handlers.

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/overcast-sh/overcast/internal/clock"
	"github.com/overcast-sh/overcast/internal/config"
	"github.com/overcast-sh/overcast/internal/serviceutil"
	"github.com/overcast-sh/overcast/internal/state"
)

// concurrencyTestHandler builds a handler over a real pool backed by a stub
// runtime, so provisioned concurrency actually allocates something.
func concurrencyTestHandler(t *testing.T) (*Handler, *InstancePool) {
	t.Helper()
	clk := clock.NewMock()
	ls := newLambdaStore(state.NewMemoryStore(), "us-east-1", clk)
	pool := NewInstancePool(&countingColdStartRuntime{}, zap.NewNop(), clk, PoolLimits{})
	t.Cleanup(pool.Stop)
	tracker := newInstanceTracker(clk, zap.NewNop())
	t.Cleanup(tracker.Stop)
	pool.observer = tracker
	h := &Handler{
		cfg:      &config.Config{Region: "us-east-1"},
		log:      serviceutil.NewServiceLogger(zap.NewNop(), "lambda"),
		clk:      clk,
		ls:       ls,
		tracker:  tracker,
		runtimes: newRuntimeRegistry([]Runtime{pool}),
	}
	fn := &Function{
		Name:       "pc-fn",
		ARN:        "arn:aws:lambda:us-east-1:000000000000:function:pc-fn",
		Runtime:    "nodejs22.x",
		Handler:    "index.handler",
		Timeout:    3,
		MemorySize: 128,
		State:      "Active",
	}
	if aerr := ls.putFunction(context.Background(), fn); aerr != nil {
		t.Fatalf("seed function: %s", aerr.Message)
	}
	return h, pool
}

func putProvisioned(t *testing.T, h *Handler, name, qualifier string, n int) *httptest.ResponseRecorder {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"ProvisionedConcurrentExecutions": n})
	req := httptest.NewRequest(http.MethodPut,
		"/2019-09-30/functions/"+name+"/provisioned-concurrency?Qualifier="+qualifier,
		bytes.NewReader(body))
	req = withFunctionNameParam(req, name)
	rec := httptest.NewRecorder()
	h.PutProvisionedConcurrencyConfig(rec, req)
	return rec
}

func decodeProvisioned(t *testing.T, rec *httptest.ResponseRecorder) provisionedConcurrencyConfigResponse {
	t.Helper()
	var out provisionedConcurrencyConfigResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode response: %v (body %s)", err, rec.Body.String())
	}
	return out
}

func TestPutProvisionedConcurrency_allocatesEnvironments(t *testing.T) {
	// Given: a function with no warm instances.
	h, pool := concurrencyTestHandler(t)

	// When: provisioned concurrency of 2 is configured.
	rec := putProvisioned(t, h, "pc-fn", "1", 2)

	// Then: the request is accepted and environments are actually created,
	// rather than the request being stored and forgotten.
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202: %s", rec.Code, rec.Body.String())
	}
	if got := waitForWarm(t, pool, "pc-fn", 2, 3*time.Second); got != 2 {
		t.Fatalf("allocated environments = %d, want 2", got)
	}
	requested, allocated, _, ok := pool.ProvisionedStatus("pc-fn")
	if !ok || requested != 2 || allocated != 2 {
		t.Fatalf("pool status = (%d,%d,%v), want (2,2,true)", requested, allocated, ok)
	}
}

func TestGetProvisionedConcurrency_reportsRealAllocation(t *testing.T) {
	// Given: a configured reservation whose environments are up.
	h, pool := concurrencyTestHandler(t)
	putProvisioned(t, h, "pc-fn", "1", 2)
	if got := waitForWarm(t, pool, "pc-fn", 2, 3*time.Second); got != 2 {
		t.Fatalf("allocated environments = %d, want 2", got)
	}

	// When: the config is read back.
	req := httptest.NewRequest(http.MethodGet,
		"/2019-09-30/functions/pc-fn/provisioned-concurrency?Qualifier=1", nil)
	req = withFunctionNameParam(req, "pc-fn")
	rec := httptest.NewRecorder()
	h.GetProvisionedConcurrencyConfig(rec, req)

	// Then: the counts come from the pool, not from echoing the request back.
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	got := decodeProvisioned(t, rec)
	if got.Status != "READY" {
		t.Fatalf("status = %q, want READY", got.Status)
	}
	if got.AllocatedProvisionedConcurrentExecutions != 2 || got.AvailableProvisionedConcurrentExecutions != 2 {
		t.Fatalf("allocated/available = %d/%d, want 2/2",
			got.AllocatedProvisionedConcurrentExecutions,
			got.AvailableProvisionedConcurrentExecutions)
	}
}

func TestGetProvisionedConcurrency_availableDropsWhileBusy(t *testing.T) {
	// Given: a reservation of 2 with both environments up.
	h, pool := concurrencyTestHandler(t)
	putProvisioned(t, h, "pc-fn", "1", 2)
	if got := waitForWarm(t, pool, "pc-fn", 2, 3*time.Second); got != 2 {
		t.Fatalf("allocated environments = %d, want 2", got)
	}

	// When: one is serving an invocation.
	fn, _ := h.ls.getFunction(context.Background(), "pc-fn")
	if _, err := pool.Acquire(context.Background(), fn); err != nil {
		t.Fatalf("acquire: %v", err)
	}

	// Then: it still counts as allocated but no longer as available.
	_, allocated, available, _ := pool.ProvisionedStatus("pc-fn")
	if allocated != 2 {
		t.Fatalf("allocated = %d, want 2", allocated)
	}
	if available != 1 {
		t.Fatalf("available = %d, want 1", available)
	}
}

func TestPutProvisionedConcurrency_rejectsZero(t *testing.T) {
	// Given: a function.
	h, _ := concurrencyTestHandler(t)

	// When: a reservation of 0 is requested — AWS rejects this rather than
	// treating it as "remove the reservation".
	rec := putProvisioned(t, h, "pc-fn", "1", 0)

	// Then: 400.
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body.String())
	}
}

func TestDeleteProvisionedConcurrency_releasesTheReservation(t *testing.T) {
	// Given: a configured reservation.
	h, pool := concurrencyTestHandler(t)
	putProvisioned(t, h, "pc-fn", "1", 2)
	if got := waitForWarm(t, pool, "pc-fn", 2, 3*time.Second); got != 2 {
		t.Fatalf("allocated environments = %d, want 2", got)
	}

	// When: it is deleted.
	req := httptest.NewRequest(http.MethodDelete,
		"/2019-09-30/functions/pc-fn/provisioned-concurrency?Qualifier=1", nil)
	req = withFunctionNameParam(req, "pc-fn")
	rec := httptest.NewRecorder()
	h.DeleteProvisionedConcurrencyConfig(rec, req)

	// Then: 204, and the pool no longer holds the environments open.
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204: %s", rec.Code, rec.Body.String())
	}
	if _, _, _, ok := pool.ProvisionedStatus("pc-fn"); ok {
		t.Fatal("reservation still held after delete")
	}

	// And a second delete reports not-found, as AWS does.
	rec = httptest.NewRecorder()
	h.DeleteProvisionedConcurrencyConfig(rec, withFunctionNameParam(
		httptest.NewRequest(http.MethodDelete,
			"/2019-09-30/functions/pc-fn/provisioned-concurrency?Qualifier=1", nil), "pc-fn"))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("second delete status = %d, want 404", rec.Code)
	}
}

func TestListProvisionedConcurrencyConfigs_returnsEveryQualifier(t *testing.T) {
	// Given: two reservations on different qualifiers.
	h, _ := concurrencyTestHandler(t)
	putProvisioned(t, h, "pc-fn", "1", 1)
	putProvisioned(t, h, "pc-fn", "prod", 2)

	// When: they are listed.
	req := httptest.NewRequest(http.MethodGet,
		"/2019-09-30/functions/pc-fn/provisioned-concurrency?List=ALL", nil)
	req = withFunctionNameParam(req, "pc-fn")
	rec := httptest.NewRecorder()
	h.GetOrListProvisionedConcurrency(rec, req)

	// Then: both come back, each carrying the qualified function ARN that AWS
	// puts on a list item (and only on a list item).
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var out listProvisionedConcurrencyConfigsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out.ProvisionedConcurrencyConfigs) != 2 {
		t.Fatalf("configs = %d, want 2", len(out.ProvisionedConcurrencyConfigs))
	}
	for _, item := range out.ProvisionedConcurrencyConfigs {
		if item.FunctionArn == "" {
			t.Fatal("list item missing FunctionArn")
		}
	}
}

func TestGetProvisionedConcurrency_omitsFunctionArn(t *testing.T) {
	// Given: a configured reservation.
	h, _ := concurrencyTestHandler(t)
	putProvisioned(t, h, "pc-fn", "1", 1)

	// When: it is read back.
	req := httptest.NewRequest(http.MethodGet,
		"/2019-09-30/functions/pc-fn/provisioned-concurrency?Qualifier=1", nil)
	req = withFunctionNameParam(req, "pc-fn")
	rec := httptest.NewRecorder()
	h.GetOrListProvisionedConcurrency(rec, req)

	// Then: no FunctionArn. AWS returns it only on ListProvisionedConcurrencyConfigs
	// items, and inventing response fields breaks SDK round-tripping.
	var raw map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, present := raw["FunctionArn"]; present {
		t.Fatalf("Get response carries FunctionArn, which AWS does not send: %s", rec.Body.String())
	}
}

func TestProvisionedConcurrency_reportsNotAllocatedWithoutDocker(t *testing.T) {
	// Given: a handler with no container runtime, as when Docker is absent.
	clk := clock.NewMock()
	ls := newLambdaStore(state.NewMemoryStore(), "us-east-1", clk)
	h := &Handler{
		cfg:      &config.Config{Region: "us-east-1"},
		log:      serviceutil.NewServiceLogger(zap.NewNop(), "lambda"),
		clk:      clk,
		ls:       ls,
		runtimes: newRuntimeRegistry([]Runtime{newNodeRuntime(clk, zap.NewNop())}),
	}
	fn := &Function{Name: "pc-fn", ARN: "arn:aws:lambda:us-east-1:000000000000:function:pc-fn", State: "Active"}
	if aerr := ls.putFunction(context.Background(), fn); aerr != nil {
		t.Fatalf("seed: %s", aerr.Message)
	}

	// When: provisioned concurrency is configured.
	rec := putProvisioned(t, h, "pc-fn", "1", 2)

	// Then: the config is accepted but honestly reports that nothing was
	// allocated, rather than claiming READY for capacity that does not exist.
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202: %s", rec.Code, rec.Body.String())
	}
	got := decodeProvisioned(t, rec)
	if got.Status != provisionedStatusFailed {
		t.Fatalf("status = %q, want %q with no container runtime", got.Status, provisionedStatusFailed)
	}
	if got.StatusReason == "" {
		t.Fatal("FAILED status with no StatusReason")
	}
	if got.AllocatedProvisionedConcurrentExecutions != 0 {
		t.Fatalf("allocated = %d, want 0", got.AllocatedProvisionedConcurrentExecutions)
	}
}

func TestProvisionedEnvironments_reportInitializationTypeToTheContainer(t *testing.T) {
	// Given: a container runtime.
	runtime := &ContainerRuntime{
		cfg:              &config.Config{Region: "us-east-1", AccountID: "000000000000"},
		overcastEndpoint: "http://172.18.0.1:4566",
	}
	fn := &Function{
		Name:       "pc-fn",
		ARN:        "arn:aws:lambda:us-east-1:000000000000:function:pc-fn",
		Runtime:    "nodejs22.x",
		MemorySize: 128,
	}

	// When/Then: an on-demand environment and a provisioned one report
	// different AWS_LAMBDA_INITIALIZATION_TYPE values, which is what
	// Powertools and friends read to classify a cold start.
	onDemand := envMap(runtime.buildEnv(fn, "stream", initTypeOnDemand, "172.18.0.1:41001", nil))
	if got := onDemand["AWS_LAMBDA_INITIALIZATION_TYPE"]; got != "on-demand" {
		t.Fatalf("on-demand init type = %q, want on-demand", got)
	}
	provisioned := envMap(runtime.buildEnv(fn, "stream", initTypeProvisioned, "172.18.0.1:41001", nil))
	if got := provisioned["AWS_LAMBDA_INITIALIZATION_TYPE"]; got != "provisioned-concurrency" {
		t.Fatalf("provisioned init type = %q, want provisioned-concurrency", got)
	}
}

func TestInvoke_reservedConcurrencyReturnsAwsThrottleResponse(t *testing.T) {
	// Given: a function throttled to zero concurrency.
	h, _ := concurrencyTestHandler(t)
	fn, _ := h.ls.getFunction(context.Background(), "pc-fn")
	fn.ReservedConcurrency = intPtr(0)
	if aerr := h.ls.putFunction(context.Background(), fn); aerr != nil {
		t.Fatalf("put function: %s", aerr.Message)
	}

	// When: it is invoked.
	req := httptest.NewRequest(http.MethodPost,
		"/2015-03-31/functions/pc-fn/invocations", bytes.NewReader([]byte(`{}`)))
	req = withFunctionNameParam(req, "pc-fn")
	rec := httptest.NewRecorder()
	h.InvokeFunction(rec, req)

	// Then: AWS's throttle response, not a 200 with a function error — SDK
	// retry policies and ESM back-off key on exactly this shape.
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429: %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("X-Amzn-Errortype"); got != "TooManyRequestsException" {
		t.Fatalf("X-Amzn-Errortype = %q, want TooManyRequestsException", got)
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Fatal("Retry-After header missing")
	}
	var body struct {
		Type              string `json:"Type"`
		Message           string `json:"message"`
		Reason            string `json:"Reason"`
		RetryAfterSeconds int    `json:"retryAfterSeconds"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v (%s)", err, rec.Body.String())
	}
	if body.Reason != reasonReservedConcurrency {
		t.Fatalf("Reason = %q, want %q", body.Reason, reasonReservedConcurrency)
	}
	if body.Type != "User" {
		t.Fatalf("Type = %q, want User", body.Type)
	}
	if body.Message != "Rate Exceeded." {
		t.Fatalf("message = %q, want %q", body.Message, "Rate Exceeded.")
	}
	if body.RetryAfterSeconds != throttleRetryAfterSeconds {
		t.Fatalf("retryAfterSeconds = %d, want %d", body.RetryAfterSeconds, throttleRetryAfterSeconds)
	}
}

func TestInvoke_reservedConcurrencyLeavesNoPhantomInstance(t *testing.T) {
	// Given: a function throttled to zero concurrency.
	h, _ := concurrencyTestHandler(t)
	fn, _ := h.ls.getFunction(context.Background(), "pc-fn")
	fn.ReservedConcurrency = intPtr(0)
	if aerr := h.ls.putFunction(context.Background(), fn); aerr != nil {
		t.Fatalf("put function: %s", aerr.Message)
	}

	// When: a throttled invocation is attempted.
	req := httptest.NewRequest(http.MethodPost,
		"/2015-03-31/functions/pc-fn/invocations", bytes.NewReader([]byte(`{}`)))
	req = withFunctionNameParam(req, "pc-fn")
	h.InvokeFunction(httptest.NewRecorder(), req)

	// Then: no execution environment is reported — it never got one.
	if got := len(h.tracker.Instances()); got != 0 {
		t.Fatalf("tracked instances = %d, want 0", got)
	}
}
