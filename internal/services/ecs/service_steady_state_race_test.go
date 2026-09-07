package ecs

// service_steady_state_race_test.go — a service announces its steady state
// whichever order its tasks happen to start in.
//
// A service is reconciled from each of its tasks: every task's PROVISIONING →
// RUNNING transition runs on its own scheduler goroutine and calls reconcile,
// which reads the whole service record, recounts, and writes the whole record
// back. Two tasks of one service starting together therefore both read before
// either writes, and the write that lands last discards the other's edit.
//
// What that loses is the steady-state event. It is emitted on the edge into
// steady state and never again, so a stale write that puts the service back
// short of its desired count leaves it running at that count having never said
// so — and a caller waiting on the event waiting forever. The persisted running
// count goes stale with it; only the read path hides that, because
// DescribeServices recomputes counts on its own copy.
//
// lockService is what holds this closed. deployment_failure_race_test.go covers
// the same lock from the failure-count side; this covers the steady-state edge,
// which is a separate victim of the same lost update — remove the lock and this
// test fails while that one still needs its own reconcile loop to trip.
//
// This is the one ECS test that runs on a real clock, so it pays real time for
// both waits a steady state now takes: the 200ms a task takes to start, and the
// settle window it then has to stay up for. The race needs two of a service's
// transitions to overlap, and the mock clock will not produce that dependably:
// it fires due timers one at a time and sleeps a millisecond after each, which
// on an idle machine is long enough for one callback to finish before the next
// begins.
//
// NOTE — its sensitivity to lockService is not currently demonstrated. The
// header above claims the steady-state event is lost on every run with the lock
// taken out; that was measured before #686 made the transition conditional on
// Docker, and it no longer reproduces. Taking lockService out now leaves this
// test passing (checked at 48 and at 200 services), most likely because placing
// a task does real work against the daemon, so a service's transitions come due
// staggered rather than together and the last writer sees the whole picture.
//
// It is kept, and kept running, because what it asserts is still true and worth
// asserting: every service reaches its desired count and records having done so.
// But it is not today a regression test for the lock. TestTaskDeathsCounted-
// WhileReconciling covers that same lock from the failure-count side and does
// still fail without it — verified — so the lock is not unguarded. Restoring
// this one's sensitivity to the steady-state edge is worth a follow-up.

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/overcast-sh/overcast/internal/clock"
	"github.com/overcast-sh/overcast/internal/config"
	"github.com/overcast-sh/overcast/internal/state"
)

// The test starts steadyStateServices services of steadyStateTasks tasks each,
// all at once. One service is enough to lose the event, but only on the
// interleaving where the callback that saw every task running is not the one
// that wrote last; running many independent services makes hitting that
// interleaving reliable rather than a matter of luck. At these numbers the
// event is lost on every run with lockService taken out.
const (
	steadyStateServices = 48
	steadyStateTasks    = 3
)

func TestServiceSteadyState_concurrentTaskTransitions(t *testing.T) {
	// Given: several services, each wanting several tasks
	svc := New(&config.Config{Region: "us-east-1", AccountID: "123456789012"}, state.NewMemoryStore(), zap.NewNop(), clock.New(), nil)
	h := svc.handler
	// Tasks only transition, and so only reconcile their service, when Docker
	// is ready — without it no service ever reaches a steady state to race.
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
	for i := 0; i < steadyStateServices; i++ {
		name := fmt.Sprintf("s%d", i)
		if w := postJSON(t, ctx, h.CreateService, map[string]any{
			"cluster":        "c1",
			"serviceName":    name,
			"taskDefinition": "f1",
			"desiredCount":   steadyStateTasks,
		}); w.Code != 200 {
			t.Fatalf("CreateService %s: HTTP %d: %s", name, w.Code, w.Body.String())
		}
	}

	// When: every task reaches RUNNING, all transitions coming due together
	h.scheduler.Settle()
	// ...and then stays up long enough to be credited. Settling twice is not
	// belt and braces: a deployment reaches its steady state a settle window
	// after its tasks run (see settleWindow), and the check that notices is
	// scheduled by the reconcile each RUNNING transition performs — so it does
	// not exist yet when the first Settle decides what to wait for, and
	// settling is defined over the transitions outstanding when it was called.
	// This is where the test spends its time; on a real clock the second wait
	// is the window itself.
	h.scheduler.Settle()

	// Then: each service recorded reaching its desired count. Read the stored
	// record rather than DescribeServices: the read path recomputes counts and
	// rollout state on its own copy, so it reports a steady state the service
	// never persisted — the event is the only durable evidence.
	for i := 0; i < steadyStateServices; i++ {
		name := fmt.Sprintf("s%d", i)
		stored, aerr := h.store.getService(ctx, "c1", name)
		if aerr != nil || stored == nil {
			t.Fatalf("getService(%s): %v", name, aerr)
		}
		if stored.RunningCount != steadyStateTasks {
			t.Errorf("service %s: runningCount = %d, want %d", name, stored.RunningCount, steadyStateTasks)
		}
		var messages strings.Builder
		var steady bool
		for _, e := range stored.Events {
			messages.WriteString("  " + e.Message + "\n")
			if strings.Contains(e.Message, "has reached a steady state") {
				steady = true
			}
		}
		if !steady {
			t.Errorf("service %s recorded no steady-state event, events:\n%s", name, messages.String())
		}
	}
}
