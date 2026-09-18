package stepfunctions

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/overcast-sh/overcast/internal/clock"
	"github.com/overcast-sh/overcast/internal/config"
	"github.com/overcast-sh/overcast/internal/serviceutil"
	"github.com/overcast-sh/overcast/internal/state"
)

// newRedriveTestHandler builds a Handler over store on a mock clock wound to
// a fixed date — wound before the handler exists, as tests/AGENTS.md asks.
func newRedriveTestHandler(t *testing.T, store state.Store) (*Handler, *clock.Mock) {
	t.Helper()
	clk := clock.NewMock()
	clk.Set(time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC))
	cfg := &config.Config{Region: "us-east-1", AccountID: "000000000000"}
	log := serviceutil.NewServiceLogger(zap.NewNop(), "stepfunctions")
	return newHandler(cfg, newStore(store, cfg.Region), log, clk), clk
}

// failedExecution runs definition to its (unsuccessful) end synchronously.
func failedExecution(t *testing.T, h *Handler, name, definition, input string) (*Execution, *StateMachine) {
	t.Helper()
	ctx := context.Background()
	resp, aerr := h.createStateMachineTyped(ctx, &createStateMachineRequest{
		Name: name, Definition: definition, RoleArn: "arn:aws:iam::000000000000:role/r",
	})
	if aerr != nil {
		t.Fatalf("createStateMachineTyped: %+v", aerr)
	}
	exec, aerr := h.startExecution(ctx, resp.StateMachineArn, "run", input, 0, executionSync)
	if aerr != nil {
		t.Fatalf("startExecution: %+v", aerr)
	}
	if exec.Status == statusSucceeded || exec.Status == statusRunning {
		t.Fatalf("execution status = %s, want it unsuccessful", exec.Status)
	}
	sm, err := h.store.GetStateMachine(ctx, name)
	if err != nil || sm == nil {
		t.Fatalf("GetStateMachine: %v", err)
	}
	return exec, sm
}

const failingDefinition = `{"StartAt":"F","States":{"F":{"Type":"Fail","Error":"Boom"}}}`

func assertNotRedrivable(t *testing.T, h *Handler, exec *Execution, wantReason string) {
	t.Helper()
	ctx := context.Background()
	described, aerr := h.describeExecutionTyped(ctx, &describeExecutionRequest{ExecutionArn: exec.ExecutionArn})
	if aerr != nil {
		t.Fatalf("describeExecutionTyped: %+v", aerr)
	}
	if described.RedriveStatus != redriveStatusNotRedrivable || described.RedriveStatusReason != wantReason {
		t.Errorf("redriveStatus = %q (%q), want NOT_REDRIVABLE (%q)", described.RedriveStatus, described.RedriveStatusReason, wantReason)
	}
	_, aerr = h.redriveExecutionTyped(ctx, &redriveExecutionRequest{ExecutionArn: exec.ExecutionArn})
	if aerr == nil || aerr.Code != "ExecutionNotRedrivable" || !strings.Contains(aerr.Message, wantReason) {
		t.Errorf("RedriveExecution error = %+v, want ExecutionNotRedrivable naming %q", aerr, wantReason)
	}
}

func TestRedriveStatus_redrivablePeriodExceeded(t *testing.T) {
	// Given: an execution that failed more than 14 days ago
	h, clk := newRedriveTestHandler(t, state.NewMemoryStore())
	exec, _ := failedExecution(t, h, "window", failingDefinition, `{}`)
	clk.Add(redriveWindow + time.Second)

	// Then: it is no longer redrivable, with AWS's reason
	assertNotRedrivable(t, h, exec, reasonPeriodExceeded)
}

func TestRedriveStatus_withinTheWindowIsRedrivable(t *testing.T) {
	// Given: an execution that failed 13 days ago
	h, clk := newRedriveTestHandler(t, state.NewMemoryStore())
	exec, _ := failedExecution(t, h, "window-ok", failingDefinition, `{}`)
	clk.Add(13 * 24 * time.Hour)

	// When: we describe it
	described, aerr := h.describeExecutionTyped(context.Background(), &describeExecutionRequest{ExecutionArn: exec.ExecutionArn})

	// Then: it is redrivable
	if aerr != nil || described.RedriveStatus != redriveStatusRedrivable || described.RedriveStatusReason != "" {
		t.Errorf("describe = %+v, %+v", described, aerr)
	}
}

func TestRedriveStatus_maxExecutionTimeExceeded(t *testing.T) {
	// Given: a failed execution that started more than a year ago
	h, clk := newRedriveTestHandler(t, state.NewMemoryStore())
	exec, _ := failedExecution(t, h, "old", failingDefinition, `{}`)
	exec.StartDate = clk.Now().Add(-maxExecutionOpenTime - time.Hour)
	if err := h.store.PutExecution(context.Background(), exec); err != nil {
		t.Fatal(err)
	}

	// Then: it is not redrivable
	assertNotRedrivable(t, h, exec, reasonMaxExecutionTime)
}

func TestRedriveStatus_historyEventLimitExceeded(t *testing.T) {
	// Given: a failed execution whose history leaves no room for a redrive
	h, _ := newRedriveTestHandler(t, state.NewMemoryStore())
	exec, _ := failedExecution(t, h, "long", failingDefinition, `{}`)
	events := make([]HistoryEvent, maxRedriveHistoryEvents)
	for i := range events {
		events[i] = HistoryEvent{ID: int64(i + 1), Type: stateEnteredEventType(stateTypePass)}
	}
	if err := h.store.PutHistory(context.Background(), exec.ExecutionArn, events); err != nil {
		t.Fatal(err)
	}

	// Then: it is not redrivable
	assertNotRedrivable(t, h, exec, reasonHistoryLimit)
}

func TestRedriveMapRun_thousandthRedriveFailsWithStatesRuntime(t *testing.T) {
	// Given: a distributed Map whose map run failed and has already been
	// redriven the maximum number of times
	h, _ := newRedriveTestHandler(t, state.NewMemoryStore())
	ctx := context.Background()
	def := `{"StartAt":"Fan","States":{"Fan":{"Type":"Map","End":true,
	  "ItemProcessor":{"ProcessorConfig":{"Mode":"DISTRIBUTED","ExecutionType":"STANDARD"},
	    "StartAt":"F","States":{"F":{"Type":"Fail","Error":"Boom"}}}}}}`
	exec, sm := failedExecution(t, h, "capped", def, `[1]`)
	runs, err := h.store.listMapRuns(ctx, exec.ExecutionArn)
	if err != nil || len(runs) != 1 {
		t.Fatalf("listMapRuns = %v, %v", runs, err)
	}
	runs[0].RedriveCount = maxMapRunRedrives
	if err := h.store.putMapRun(ctx, runs[0]); err != nil {
		t.Fatal(err)
	}

	// When: the parent is redriven
	run, err := h.beginRedrive(ctx, exec)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.completeExecution(ctx, sm, exec, "us-east-1", 0, run); err != nil {
		t.Fatal(err)
	}

	// Then: the Map fails with States.Runtime and the map run is untouched
	if exec.Status != statusFailed || exec.Error != errRuntime || !strings.Contains(exec.Cause, "maximum") {
		t.Errorf("after redrive: %s %s: %s", exec.Status, exec.Error, exec.Cause)
	}
	after, err := h.store.getMapRun(ctx, runs[0].MapRunArn)
	if err != nil || after.RedriveCount != maxMapRunRedrives {
		t.Errorf("map run after = %+v, %v", after, err)
	}
}

// checkpointStoreContract exercises the checkpoint tree against one
// state.Store implementation: it round-trips, and a record that no longer
// decodes is isolated — the redrive falls back to the root point instead of
// failing.
func checkpointStoreContract(t *testing.T, store state.Store) {
	t.Helper()
	h, _ := newRedriveTestHandler(t, store)
	ctx := context.Background()
	const arn = "arn:aws:states:us-east-1:000000000000:execution:sm:run"
	tree := &redrivePoint{
		State: "Fan", Input: `{"a":1}`,
		Branches: []redriveChild{
			{Succeeded: true, Output: `"done"`},
			{Point: &redrivePoint{State: "Gate", Input: `{}`, Variables: `{"v":2}`}},
			{},
		},
	}

	// A tree round-trips.
	if err := h.store.putRedriveCheckpoint(ctx, arn, tree); err != nil {
		t.Fatalf("put: %v", err)
	}
	exec := &Execution{ExecutionArn: arn, RedriveState: "Fan", RedriveInput: `{"a":1}`}
	got := h.resumePoint(ctx, exec)
	if got == nil || len(got.Branches) != 3 || !got.Branches[0].Succeeded || got.Branches[1].Point.State != "Gate" || got.Branches[2].Point != nil {
		t.Fatalf("resumePoint = %+v", got)
	}

	// A tree belonging to another root is not applied.
	exec.RedriveState = "Other"
	if got := h.resumePoint(ctx, exec); got.hasContainer() {
		t.Errorf("tree applied to a different root: %+v", got)
	}

	// A malformed record is reported by the store and isolated by the
	// redrive, which keeps the root point.
	if err := store.Set(ctx, storeNS, serviceutil.RegionKey("us-east-1", redriveCheckpointKey(arn)), "{not json"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.store.getRedriveCheckpoint(ctx, arn); !errors.Is(err, errMalformedCheckpoint) {
		t.Errorf("getRedriveCheckpoint on malformed record = %v", err)
	}
	exec.RedriveState = "Fan"
	if got := h.resumePoint(ctx, exec); got == nil || got.State != "Fan" || got.hasContainer() {
		t.Errorf("resumePoint over a malformed tree = %+v", got)
	}

	// Deleting clears it.
	if err := h.store.deleteRedriveCheckpoint(ctx, arn); err != nil {
		t.Fatal(err)
	}
	if got, err := h.store.getRedriveCheckpoint(ctx, arn); got != nil || err != nil {
		t.Errorf("after delete = %+v, %v", got, err)
	}
}

func TestRedriveCheckpoint_memoryStore(t *testing.T) {
	checkpointStoreContract(t, state.NewMemoryStore())
}
