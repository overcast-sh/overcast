package ecs

// Regression test: ECS tasks are stored under region-scoped keys, but the
// PROVISIONING → RUNNING transition runs on a scheduler callback outside any
// request context. Before the region-scoped scheduler API the callback
// resolved the DEFAULT region and tasks run elsewhere never left
// PROVISIONING. Also covers the per-region seeding of builtin capacity
// providers, which used to seed only once globally (into the default region).

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
	"github.com/overcast-sh/overcast/internal/middleware"
	"github.com/overcast-sh/overcast/internal/serviceutil"
	"github.com/overcast-sh/overcast/internal/state"
)

func newECSRegionTestHandler(t *testing.T) (*Handler, *clock.Mock) {
	t.Helper()
	clk := clock.NewMock()
	svc := New(&config.Config{Region: "us-east-1", AccountID: "123456789012", Network: "overcast"}, state.NewMemoryStore(), zap.NewNop(), clk, nil)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		svc.handler.scheduler.Stop(ctx)
	})
	return svc.handler, clk
}

func postJSON(t *testing.T, ctx context.Context, fn http.HandlerFunc, body any) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/x-amz-json-1.1")
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()
	fn(w, req)
	return w
}

func TestTaskTransition_nonDefaultRegion(t *testing.T) {
	// Docker is wired to a fake daemon rather than skipped for want of a real
	// one: the PROVISIONING → RUNNING transition this test is about is only
	// scheduled when Docker is ready, so a metadata-only handler never reaches
	// the subject. See docker_fake_test.go.
	h, clk, fd := newECSDockerTestHandler(t)
	ctx := middleware.ContextWithRegion(context.Background(), "eu-west-1")

	if w := postJSON(t, ctx, h.CreateCluster, map[string]any{"clusterName": "c1"}); w.Code != 200 {
		t.Fatalf("CreateCluster: HTTP %d: %s", w.Code, w.Body.String())
	}
	if w := postJSON(t, ctx, h.RegisterTaskDefinition, map[string]any{
		"family":               "f1",
		"containerDefinitions": []map[string]any{{"name": "app", "image": "nginx"}},
	}); w.Code != 200 {
		t.Fatalf("RegisterTaskDefinition: HTTP %d: %s", w.Code, w.Body.String())
	}
	if w := postJSON(t, ctx, h.RunTask, map[string]any{"cluster": "c1", "taskDefinition": "f1"}); w.Code != 200 {
		t.Fatalf("RunTask: HTTP %d: %s", w.Code, w.Body.String())
	}

	// Locate the task (and confirm its stored region) via a cross-region scan.
	tasks, err := serviceutil.ScanRegions[Task](context.Background(), h.store.store, nsTasks, "us-east-1")
	if err != nil || len(tasks) != 1 {
		t.Fatalf("task scan = %d tasks (err %v), want 1", len(tasks), err)
	}
	if tasks[0].Region != "eu-west-1" {
		t.Fatalf("task stored under region %q, want eu-west-1", tasks[0].Region)
	}
	taskID := extractTaskID(tasks[0].Value.TaskArn)

	// Fire the 200ms PROVISIONING → RUNNING transition and wait for it: the
	// mock clock runs the callback on a goroutine of its own, so advancing the
	// clock alone leaves the read below racing it.
	h.scheduler.AdvanceAndSettle(clk, time.Second)

	got, aerr := h.store.getTask(ctx, "c1", taskID)
	if aerr != nil || got == nil {
		t.Fatalf("getTask(eu-west-1): %v", aerr)
	}
	if got.LastStatus != "RUNNING" {
		t.Fatalf("task status = %q, want %q (PROVISIONING → RUNNING no-oped)", got.LastStatus, "RUNNING")
	}

	// A task reporting RUNNING must have a container behind it — otherwise the
	// handler was metadata-only and the transition proved nothing about the
	// region-scoped callback.
	if n := fd.startedCount(); n != 1 {
		t.Fatalf("containers started = %d, want 1: the task reported RUNNING without one behind it", n)
	}
}

func TestEnsureBuiltinProviders_perRegion(t *testing.T) {
	h, _ := newECSRegionTestHandler(t)
	euCtx := middleware.ContextWithRegion(context.Background(), "eu-west-1")
	defCtx := context.Background()

	h.ensureBuiltinProviders(euCtx)
	if cp, _ := h.store.getCapacityProvider(euCtx, "FARGATE"); cp == nil {
		t.Fatal("FARGATE not seeded in eu-west-1")
	}
	if cp, _ := h.store.getCapacityProvider(defCtx, "FARGATE"); cp != nil {
		t.Fatal("eu-west-1 seed leaked into the default region")
	}

	// A later default-region request must still get its own seed — the old
	// sync.Once guard would have skipped it after the first region.
	h.ensureBuiltinProviders(defCtx)
	if cp, _ := h.store.getCapacityProvider(defCtx, "FARGATE"); cp == nil {
		t.Fatal("FARGATE not seeded in the default region after a prior region's seed")
	}
}
