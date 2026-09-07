package ecs

// launch_transition_order_test.go — a task's PROVISIONING → RUNNING transition
// is scheduled only once the task record it reads exists.
//
// The transition callback reads the task back out of the store and gives up if
// it is not there:
//
//	got, aerr := h.store.getTask(ctx, cluster, taskID)
//	if aerr != nil || got == nil { return }
//
// so scheduling it before the record is persisted leaves a window in which the
// callback can fire against nothing. It is a 200 ms window and the write that
// closes it is the next statement, which is why this never showed up on an idle
// machine — but the transition is a one-shot. When it loses the race it is
// consumed, and the task that lands a moment later sits at PROVISIONING for the
// lifetime of the emulator: no second transition is ever scheduled, so the
// service it belongs to never reaches its running count and never announces a
// steady state.
//
// Rather than try to lose that race on a real clock, this holds the write open:
// the store blocks on the way into the task's Set, which is exactly the instant
// the transition must not yet be pending.

import (
	"context"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/overcast-sh/overcast/internal/clock"
	"github.com/overcast-sh/overcast/internal/config"
	"github.com/overcast-sh/overcast/internal/state"
)

// gatedTaskStore blocks the first write of a task record until it is released,
// so a test can inspect the world at the moment the task does not yet exist.
type gatedTaskStore struct {
	state.Store
	once    sync.Once
	entered chan struct{}
	release chan struct{}
}

func (s *gatedTaskStore) Set(ctx context.Context, namespace, key, value string) error {
	if namespace == nsTasks {
		s.once.Do(func() {
			close(s.entered)
			<-s.release
		})
	}
	return s.Store.Set(ctx, namespace, key, value)
}

func TestRunTask_runningTransitionScheduledAfterTheRecordLands(t *testing.T) {
	// Given: a handler whose first task write can be held open
	gate := &gatedTaskStore{
		Store:   state.NewMemoryStore(),
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	clk := clock.NewMock()
	svc := New(&config.Config{Region: "us-east-1", AccountID: "123456789012"}, gate, zap.NewNop(), clk, nil)
	h := svc.handler
	// The transition being ordered here is scheduled only when Docker is ready,
	// so the race this test is about does not exist on a metadata-only handler.
	// The containers start before the gated write, which is the point the
	// assertion below inspects.
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

	// When: RunTask is held at the point of writing its task record
	done := make(chan struct{})
	go func() {
		defer close(done)
		if w := postJSON(t, ctx, h.RunTask, map[string]any{
			"cluster":        "c1",
			"taskDefinition": "f1",
		}); w.Code != 200 {
			t.Errorf("RunTask: HTTP %d: %s", w.Code, w.Body.String())
		}
	}()
	select {
	case <-gate.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("RunTask never reached the task write")
	}

	// Then: no transition is pending yet. One scheduled here would read the
	// task back before this write lands, find nothing, and be spent — leaving
	// the task at PROVISIONING with nothing left to move it.
	if n := h.scheduler.PendingCount(); n != 0 {
		t.Errorf("%d transition(s) pending before the task record exists, want 0 — "+
			"a transition firing now would find no task and be consumed", n)
	}

	close(gate.release)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("RunTask did not return")
	}

	// And: the transition is scheduled once the record is there, so the task
	// still reaches RUNNING.
	h.scheduler.AdvanceAndSettle(clk, time.Second)

	tasks, aerr := h.store.listTasks(ctx, "c1")
	if aerr != nil {
		t.Fatalf("listTasks: %s", aerr.Message)
	}
	if len(tasks) != 1 {
		t.Fatalf("listTasks: got %d tasks, want 1", len(tasks))
	}
	if tasks[0].LastStatus != "RUNNING" {
		t.Errorf("task status = %q, want %q", tasks[0].LastStatus, "RUNNING")
	}
}
