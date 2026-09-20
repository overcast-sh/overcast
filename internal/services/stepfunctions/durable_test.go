package stepfunctions

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/overcast-sh/overcast/internal/state"
)

// The end-to-end restart behaviour — parked tokens, activities and Waits
// resuming over a reopened SQLite store — is covered in
// tests/integration/stepfunctions/durable_restart_test.go. These cover what
// that cannot reach: a crash (no shutdown ran), a malformed checkpoint, and
// the checkpoint store itself.

// parkCheckpointStoreContract exercises the checkpoint records against one
// state.Store implementation.
func parkCheckpointStoreContract(t *testing.T, backend state.Store) {
	t.Helper()
	ctx := context.Background()
	st := newStore(backend, "us-east-1")

	// Given: two checkpoints in different regions and one malformed record
	for _, cp := range []*executionCheckpoint{
		{ExecutionArn: "arn:aws:states:us-east-1:000000000000:execution:sm:a", Region: "us-east-1", Point: redrivePoint{State: "Ask", Input: "{}"}, Kind: parkCallback, Token: "t-a"},
		{ExecutionArn: "arn:aws:states:eu-west-1:000000000000:execution:sm:b", Region: "eu-west-1", Point: redrivePoint{State: "Hold", Input: "{}"}, Kind: parkWait},
	} {
		if err := st.PutCheckpoint(ctx, cp); err != nil {
			t.Fatalf("PutCheckpoint: %v", err)
		}
	}
	if err := backend.Set(ctx, storeNS, parkPrefix+"arn:aws:states:us-east-1:000000000000:execution:sm:bad", "{not json"); err != nil {
		t.Fatalf("seed malformed checkpoint: %v", err)
	}

	// When: every checkpoint is listed
	checkpoints, malformed, err := st.ListCheckpoints(ctx)

	// Then: both regions are found in one scan and the bad record is isolated
	if err != nil {
		t.Fatalf("ListCheckpoints: %v", err)
	}
	if len(checkpoints) != 2 || len(malformed) != 1 {
		t.Fatalf("checkpoints=%d malformed=%v, want 2 and 1", len(checkpoints), malformed)
	}

	// When: one is deleted
	if err := st.DeleteCheckpoint(ctx, checkpoints[0].ExecutionArn); err != nil {
		t.Fatalf("DeleteCheckpoint: %v", err)
	}
	checkpoints, _, _ = st.ListCheckpoints(ctx)

	// Then: only the other remains
	if len(checkpoints) != 1 {
		t.Fatalf("after delete: %d checkpoints, want 1", len(checkpoints))
	}
}

func TestParkCheckpoint_memoryStore(t *testing.T) {
	parkCheckpointStoreContract(t, state.NewMemoryStore())
}

// runningRecordFromAnotherProcess writes a RUNNING execution as a process
// that has since died would have left it, and returns its ARN.
func runningRecordFromAnotherProcess(t *testing.T, backend state.Store, smARN string) string {
	t.Helper()
	ctx := context.Background()
	previous := newStore(backend, "us-east-1")
	exec := &Execution{
		ExecutionArn:    executionARNPrefix(smARN) + "crashed",
		StateMachineArn: smARN,
		Name:            "crashed",
		Input:           `{"k":1}`,
		Status:          statusRunning,
		StartDate:       time.Date(2026, 3, 1, 11, 0, 0, 0, time.UTC),
	}
	if err := previous.PutExecution(ctx, exec); err != nil {
		t.Fatalf("PutExecution: %v", err)
	}
	return exec.ExecutionArn
}

func TestReapIfOrphaned_crashedExecutionEndsFailed(t *testing.T) {
	// Given: a RUNNING execution left by a process that crashed before it
	// parked anywhere, and a new process over the same store
	backend := state.NewMemoryStore()
	h, _ := newRedriveTestHandler(t, backend)
	ctx := context.Background()
	created, aerr := h.createStateMachineTyped(ctx, &createStateMachineRequest{
		Name: "crashy", Definition: `{"StartAt":"P","States":{"P":{"Type":"Pass","End":true}}}`, RoleArn: "arn:aws:iam::000000000000:role/r",
	})
	if aerr != nil {
		t.Fatalf("createStateMachineTyped: %+v", aerr)
	}
	execARN := runningRecordFromAnotherProcess(t, backend, created.StateMachineArn)

	// When: the execution is first read after the restart
	h.ensureRehydrated()
	described, aerr := h.describeExecutionTyped(ctx, &describeExecutionRequest{ExecutionArn: execARN})
	if aerr != nil {
		t.Fatalf("describeExecutionTyped: %+v", aerr)
	}

	// Then: it has ended FAILED with a States.Runtime cause naming the
	// restart, and its history is ExecutionStarted then ExecutionFailed
	if described.Status != statusFailed || described.Error != errRuntime || !strings.Contains(described.Cause, "restart") {
		t.Fatalf("status=%s error=%s cause=%q", described.Status, described.Error, described.Cause)
	}
	events, err := h.store.GetHistory(ctx, execARN)
	if err != nil || len(events) != 2 || events[0].Type != evtExecutionStarted || events[1].Type != evtExecutionFailed || events[1].PreviousEventID != 1 {
		t.Fatalf("history = %+v (%v)", events, err)
	}
	if events[0].ExecutionStarted.Input != `{"k":1}` || events[0].ExecutionStarted.RoleArn == "" {
		t.Errorf("ExecutionStarted = %+v", events[0].ExecutionStarted)
	}
}

func TestReapIfOrphaned_ownRunningExecutionIsLeftAlone(t *testing.T) {
	// Given: a RUNNING record this process wrote
	backend := state.NewMemoryStore()
	h, _ := newRedriveTestHandler(t, backend)
	ctx := context.Background()
	exec := &Execution{ExecutionArn: "arn:aws:states:us-east-1:000000000000:execution:sm:mine", Status: statusRunning}
	if err := h.store.PutExecution(ctx, exec); err != nil {
		t.Fatalf("PutExecution: %v", err)
	}
	stored, _ := h.store.GetExecution(ctx, exec.ExecutionArn)
	h.ensureRehydrated()

	// When / Then: it is not mistaken for one a crash left behind
	if got := h.reapIfOrphaned(ctx, stored); got.Status != statusRunning {
		t.Fatalf("status = %s, want RUNNING", got.Status)
	}
}

func TestRehydrate_malformedCheckpointIsIsolated(t *testing.T) {
	// Given: a crashed execution whose checkpoint cannot be decoded
	backend := state.NewMemoryStore()
	h, _ := newRedriveTestHandler(t, backend)
	ctx := context.Background()
	execARN := runningRecordFromAnotherProcess(t, backend, "arn:aws:states:us-east-1:000000000000:stateMachine:gone")
	if err := backend.Set(ctx, storeNS, parkPrefix+execARN, "{not json"); err != nil {
		t.Fatalf("seed malformed checkpoint: %v", err)
	}

	// When: the new process rehydrates and the execution is read
	if err := h.rehydrate(ctx); err != nil {
		t.Fatalf("rehydrate: %v", err)
	}
	h.ensureRehydrated()
	exec, aerr := h.getExecution(ctx, execARN)

	// Then: the bad record fails only its own execution, and is cleaned up
	if aerr != nil || exec.Status != statusFailed {
		t.Fatalf("exec=%+v aerr=%+v", exec, aerr)
	}
	if _, found, _ := backend.Get(ctx, storeNS, parkPrefix+execARN); found {
		t.Error("the malformed checkpoint was not removed")
	}
}

func TestRehydrate_checkpointOfFinishedExecutionIsDropped(t *testing.T) {
	// Given: a checkpoint left for an execution that is no longer RUNNING
	backend := state.NewMemoryStore()
	h, _ := newRedriveTestHandler(t, backend)
	ctx := context.Background()
	exec := &Execution{ExecutionArn: "arn:aws:states:us-east-1:000000000000:execution:sm:done", Status: statusSucceeded}
	if err := h.store.PutExecution(ctx, exec); err != nil {
		t.Fatalf("PutExecution: %v", err)
	}
	cp := &executionCheckpoint{ExecutionArn: exec.ExecutionArn, Region: "us-east-1", Point: redrivePoint{State: "Ask", Input: "{}"}, Kind: parkCallback, Token: "stale"}
	if err := h.store.PutCheckpoint(ctx, cp); err != nil {
		t.Fatalf("PutCheckpoint: %v", err)
	}

	// When: the process rehydrates
	if err := h.rehydrate(ctx); err != nil {
		t.Fatalf("rehydrate: %v", err)
	}

	// Then: the stale token names nothing and the checkpoint is gone
	if h.tasks.lookup("stale") != nil {
		t.Error("a finished execution's token was registered")
	}
	if _, found, _ := backend.Get(ctx, storeNS, parkPrefix+exec.ExecutionArn); found {
		t.Error("the stale checkpoint was not removed")
	}
}

// pausingStore holds open the first Set that matches, so a test can look at
// what a concurrent request sees in the middle of one store write.
type pausingStore struct {
	state.Store
	match   func(key, value string) bool
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (p *pausingStore) Set(ctx context.Context, namespace, key, value string) error {
	if p.match(key, value) {
		p.once.Do(func() {
			close(p.entered)
			<-p.release
		})
	}
	return p.Store.Set(ctx, namespace, key, value)
}

// parkedActivityExecution leaves one execution parked on an activity task the
// way a process that has since shut down would have, and returns the
// execution and the activity it is waiting on.
func parkedActivityExecution(t *testing.T, backend state.Store, name string) (*Execution, string) {
	t.Helper()
	ctx := context.Background()
	h, _ := newRedriveTestHandler(t, backend)
	activity, aerr := h.createActivityTyped(ctx, &createActivityRequest{Name: name})
	if aerr != nil {
		t.Fatalf("createActivityTyped: %+v", aerr)
	}
	created, aerr := h.createStateMachineTyped(ctx, &createStateMachineRequest{
		Name:       name,
		Definition: `{"StartAt":"Approve","States":{"Approve":{"Type":"Task","Resource":"` + activity.ActivityArn + `","End":true}}}`,
		RoleArn:    "arn:aws:iam::000000000000:role/r",
	})
	if aerr != nil {
		t.Fatalf("createStateMachineTyped: %+v", aerr)
	}
	exec, aerr := h.startExecution(ctx, created.StateMachineArn, "run", `{"doc":"a"}`, 0, executionAsync)
	if aerr != nil {
		t.Fatalf("startExecution: %+v", aerr)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		checkpoints, _, err := h.store.ListCheckpoints(ctx)
		if err != nil {
			t.Fatalf("ListCheckpoints: %v", err)
		}
		if len(checkpoints) == 1 && checkpoints[0].ExecutionArn == exec.ExecutionArn {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the execution never parked on its activity task")
		}
		time.Sleep(time.Millisecond)
	}
	stopCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	h.Stop(stopCtx)
	return exec, activity.ActivityArn
}

func TestReapIfOrphaned_resumedExecutionSurvivesItsOwnTerminalWrite(t *testing.T) {
	// Given: an execution parked on an activity task by a process that has
	// since gone away, and a new process whose terminal write for it can be
	// held open
	backend := state.NewMemoryStore()
	ctx := context.Background()
	exec, activityARN := parkedActivityExecution(t, backend, "resumed-activity")
	paused := &pausingStore{
		Store:   backend,
		entered: make(chan struct{}),
		release: make(chan struct{}),
		match: func(key, value string) bool {
			return strings.Contains(key, exec.ExecutionArn) && strings.Contains(value, statusSucceeded)
		},
	}
	second, _ := newRedriveTestHandler(t, paused)

	// When: it is resumed, a worker answers it, and it is described in the
	// window persistOutcome opens between releasing the run and writing the
	// record that says it has finished
	second.ensureRehydrated()
	task, aerr := second.getActivityTaskTyped(ctx, &getActivityTaskRequest{ActivityArn: activityARN, WorkerName: "after-restart"})
	if aerr != nil || task.TaskToken == "" || task.Input != `{"doc":"a"}` {
		t.Fatalf("getActivityTaskTyped = %+v (%+v)", task, aerr)
	}
	if _, aerr := second.sendTaskSuccessTyped(ctx, &sendTaskSuccessRequest{TaskToken: task.TaskToken, Output: `{"ok":1}`}); aerr != nil {
		t.Fatalf("sendTaskSuccessTyped: %+v", aerr)
	}
	select {
	case <-paused.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("the resumed execution never reached its terminal write")
	}
	described, aerr := second.describeExecutionTyped(ctx, &describeExecutionRequest{ExecutionArn: exec.ExecutionArn})
	close(paused.release)

	// Then: the execution this process is running is not mistaken for one the
	// previous process left orphaned, and it lands SUCCEEDED
	if aerr != nil {
		t.Fatalf("describeExecutionTyped: %+v", aerr)
	}
	if described.Status != statusRunning {
		t.Fatalf("mid-write status=%s error=%s cause=%q, want RUNNING", described.Status, described.Error, described.Cause)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		final, aerr := second.describeExecutionTyped(ctx, &describeExecutionRequest{ExecutionArn: exec.ExecutionArn})
		if aerr != nil {
			t.Fatalf("describeExecutionTyped: %+v", aerr)
		}
		if final.Status != statusRunning {
			if final.Status != statusSucceeded || final.Output != `{"ok":1}` {
				t.Fatalf("final status=%s output=%q (%s: %s)", final.Status, final.Output, final.Error, final.Cause)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the resumed execution never finished")
		}
		time.Sleep(time.Millisecond)
	}
}
