package ecs

// service_scale_down_race_test.go — a scale-down survives the reconcile that
// was already running when it landed.
//
// UpdateService reads the whole service record, edits it and writes the whole
// record back, and so does every reconcile. A service's tasks each reconcile it
// as they reach RUNNING, on the scheduler's goroutines, so a caller scaling a
// service down is writing against a reconcile that has already read the record:
// its write lands second and restores the desired count the caller just
// lowered. UpdateService then reconciles a service that, as stored, still wants
// every task it had — the surplus is never retired, and the scale-down is
// silently a no-op.
//
// Nothing on the read path shows it, which is what makes it a flake rather than
// a bug report: DescribeServices recomputes counts from the tasks on its own
// copy, so the service reports the count it is running while the record says
// otherwise. The evidence is the stored record and the tasks left running.
//
// lockService is what holds this closed — the same lock, and the same lost
// update, as service_steady_state_race_test.go and deployment_failure_race_test.go
// cover from the reconcile side. This covers it from the API side: the handlers
// that mutate a service record are writers too.
//
// Real clock, for the reason service_steady_state_race_test.go gives: the race
// needs a transition's reconcile to still be running when the update lands, and
// the mock clock runs due timers one at a time.

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/overcast-sh/overcast/internal/clock"
	"github.com/overcast-sh/overcast/internal/config"
	"github.com/overcast-sh/overcast/internal/state"
)

// One service is enough to lose the update, but only on the interleaving where
// the update lands inside a reconcile; running many independent services makes
// hitting it reliable rather than a matter of luck. At these numbers at least
// one service loses its scale-down on every run with lockService taken out of
// the update path.
const (
	scaleDownServices = 48
	scaleDownTasks    = 3
)

func TestUpdateService_scaleDownConcurrentWithTaskTransitions(t *testing.T) {
	// Given: several services, each running several tasks
	svc := New(&config.Config{Region: "us-east-1", AccountID: "123456789012"}, state.NewMemoryStore(), zap.NewNop(), clock.New(), nil)
	h := svc.handler
	// Tasks only transition, and so only reconcile their service, when Docker
	// is ready — without it there are no transitions to race the update against.
	wireFakeDocker(t, h)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		h.scheduler.Stop(ctx)
	})
	ctx := context.Background()

	if w := postJSON(t, ctx, h.CreateCluster, map[string]any{"clusterName": "c1"}); w.Code != 200 {
		t.Fatalf("CreateCluster: HTTP %d: %s", w.Code, w.Body.String())
	}
	if w := postJSON(t, ctx, h.RegisterTaskDefinition, map[string]any{
		"family":               "f1",
		"containerDefinitions": []map[string]any{{"name": "app", "image": "nginx"}},
	}); w.Code != 200 {
		t.Fatalf("RegisterTaskDefinition: HTTP %d: %s", w.Code, w.Body.String())
	}
	for i := 0; i < scaleDownServices; i++ {
		name := fmt.Sprintf("s%d", i)
		if w := postJSON(t, ctx, h.CreateService, map[string]any{
			"cluster":        "c1",
			"serviceName":    name,
			"taskDefinition": "f1",
			"desiredCount":   scaleDownTasks,
		}); w.Code != 200 {
			t.Fatalf("CreateService %s: HTTP %d: %s", name, w.Code, w.Body.String())
		}
	}

	// When: each service is scaled to one task the moment one of its tasks
	// reports RUNNING — which is the moment the reconcile that transition
	// triggers is running, since the task is written before the service is
	var wg sync.WaitGroup
	for i := 0; i < scaleDownServices; i++ {
		name := fmt.Sprintf("s%d", i)
		wg.Add(1)
		go func() {
			defer wg.Done()
			if !awaitRunningTasks(ctx, h, "c1", name, 1) {
				// Reported rather than skipped: a scale-down that never ran is
				// not a scale-down that survived, and silently returning here
				// would make this test pass on a machine too loaded to reach
				// the window.
				t.Errorf("service %s: no task reached RUNNING before the deadline", name)
				return
			}
			postJSON(t, ctx, h.UpdateService, map[string]any{
				"cluster": "c1", "service": name, "desiredCount": 1,
			})
		}()
	}
	wg.Wait()
	h.scheduler.Settle()

	// Then: every service kept the count it was scaled to, and retired the
	// tasks that count no longer covers
	for i := 0; i < scaleDownServices; i++ {
		name := fmt.Sprintf("s%d", i)
		stored, aerr := h.store.getService(ctx, "c1", name)
		if aerr != nil || stored == nil {
			t.Fatalf("getService(%s): %v", name, aerr)
		}
		if stored.DesiredCount != 1 {
			t.Errorf("service %s: stored desiredCount = %d, want 1 (the scale-down was overwritten)",
				name, stored.DesiredCount)
		}
		if got := runningTaskCount(ctx, t, h, "c1", name); got != 1 {
			t.Errorf("service %s: %d tasks left running, want 1", name, got)
		}
	}
}

// awaitRunningTasks polls the store until a service has want tasks in RUNNING,
// reporting whether it got there before the deadline. It reads the tasks rather
// than the service so it returns as early as possible: a task is written before
// the reconcile it triggers reads the service.
//
// The deadline is generous because it is not what the test measures — it only
// has to outlast a loaded machine running the whole suite at once, where these
// 48 services and their transitions get a fraction of a CPU each.
func awaitRunningTasks(ctx context.Context, h *Handler, clusterName, serviceName string, want int) bool {
	deadline := time.Now().Add(60 * time.Second)
	group := serviceGroupPrefix + serviceName
	for {
		tasks, aerr := h.store.listTasks(ctx, clusterName)
		if aerr == nil {
			running := 0
			for _, task := range tasks {
				if task.Group == group && task.LastStatus == "RUNNING" {
					running++
				}
			}
			if running >= want {
				return true
			}
		}
		if time.Now().After(deadline) {
			return false
		}
	}
}

// runningTaskCount counts a service's tasks that have not stopped.
func runningTaskCount(ctx context.Context, t *testing.T, h *Handler, clusterName, serviceName string) int {
	t.Helper()
	tasks, aerr := h.store.listTasks(ctx, clusterName)
	if aerr != nil {
		t.Fatalf("listTasks: %v", aerr)
	}
	group := serviceGroupPrefix + serviceName
	live := 0
	for _, task := range tasks {
		if task.Group == group && task.LastStatus != "STOPPED" {
			live++
		}
	}
	return live
}
