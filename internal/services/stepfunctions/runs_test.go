package stepfunctions

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/overcast-sh/overcast/internal/clock"
	"github.com/overcast-sh/overcast/internal/config"
	"github.com/overcast-sh/overcast/internal/protocol"
	"github.com/overcast-sh/overcast/internal/serviceutil"
	"github.com/overcast-sh/overcast/internal/state"
)

// newRunsTestHandler builds a Handler wired to an in-memory store, with one
// STANDARD state machine ("race-sm": a single Pass state) registered — enough
// for startExecution to run an execution to completion near-instantly, so a
// hammering loop can launch many of them without the test itself taking long.
func newRunsTestHandler(t *testing.T) (*Handler, string) {
	t.Helper()
	cfg := &config.Config{Region: "us-east-1", AccountID: "000000000000"}
	log := serviceutil.NewServiceLogger(zap.NewNop(), "stepfunctions")
	h := newHandler(cfg, newStore(state.NewMemoryStore(), cfg.Region), log, clock.New())

	resp, aerr := h.createStateMachineTyped(context.Background(), &createStateMachineRequest{
		Name:       "race-sm",
		Definition: `{"StartAt":"P","States":{"P":{"Type":"Pass","End":true}}}`,
		RoleArn:    "arn:aws:iam::000000000000:role/race",
	})
	if aerr != nil {
		t.Fatalf("createStateMachineTyped: %+v", aerr)
	}
	return h, resp.StateMachineArn
}

// TestHandler_StopRacesStartExecution reproduces issue #1315: a
// StartExecution arriving while Handler.Stop is tearing down raced
// Stop.func1's wg.Wait (runs.go:103 at the time) against startExecution's
// wg.Add (execution_ops.go:136 at the time), with nothing synchronizing the
// two. CI's -race job caught this on PR #1308 (tests/integration/pipes,
// whose SFN target fired a live StartExecution during test-server teardown)
// — the third instance of the shutdown-vs-start class #1290 fixed in
// lifecycle.Scheduler and #1298 fixed in EKS. This hammers the same
// interleaving directly.
//
// Against the unfixed Handler (registerRun with no stopping check, plain
// h.wg.Add(1) in startExecution, Stop with no fencing before wg.Wait), this
// reliably reproduces under `go test -race`:
//
//	WARNING: DATA RACE
//	Write at 0x... by goroutine ...:
//	  internal/services/stepfunctions.(*Handler).Stop.func1()
//	      .../stepfunctions/runs.go:103 +0x...
//	Previous read at 0x... by goroutine ...:
//	  internal/services/stepfunctions.(*Handler).startExecution()
//	      .../stepfunctions/execution_ops.go:136 +0x...
//
// Fixed, reserveRun's stopping check and wg.Add happen under the same
// runsMu Stop uses to set stopping before it ever calls wg.Wait, so there is
// no unsynchronized access left for the race detector to catch, and Stop
// always returns well inside its deadline. Run with `go test -race -count=20`.
func TestHandler_StopRacesStartExecution(t *testing.T) {
	h, smARN := newRunsTestHandler(t)

	// Release a burst of concurrent StartExecution callers and call Stop at
	// the same instant, so as many of them as possible are actually
	// in-flight — registering their run and (pre-fix) doing a bare
	// h.wg.Add(1) — at the exact moment Stop's wg.Wait() begins. That
	// simultaneous-release shape, rather than a steady trickle, is what
	// makes the race detector's window land reliably instead of by luck.
	const callers = 64
	var ready sync.WaitGroup
	ready.Add(callers)
	start := make(chan struct{})
	var callersWG sync.WaitGroup
	callersWG.Add(callers)
	for i := 0; i < callers; i++ {
		go func(i int) {
			defer callersWG.Done()
			ready.Done()
			<-start
			_, _ = h.startExecutionTyped(context.Background(), &startExecutionRequest{
				StateMachineArn: smARN,
				Name:            fmt.Sprintf("c%d", i),
			})
		}(i)
	}
	ready.Wait()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	close(start)
	h.Stop(ctx)
	callersWG.Wait()

	if ctx.Err() != nil {
		t.Fatal("Stop() did not complete before its deadline — a StartExecution kept the wg open past shutdown")
	}
}

// TestHandler_StartExecutionRefusedAfterStop is the deterministic half of the
// #1315 fix: DoD requires that a StartExecution (or StartSyncExecution, or a
// nested states:startExecution — every launcher funnels through the same
// startExecution) arriving after Stop be a defined refusal, not a race that
// only sometimes gets caught. This nails that down directly, with no
// reliance on winning a race window: Stop runs first, then every launcher
// must get ServiceUnavailableException, register no run, and Add nothing to
// wg — and the RUNNING record startExecution had already persisted before
// the refusal must be unwound to ABORTED rather than left stuck forever.
func TestHandler_StartExecutionRefusedAfterStop(t *testing.T) {
	h, smARN := newRunsTestHandler(t)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	h.Stop(ctx)
	if ctx.Err() != nil {
		t.Fatal("Stop() on an idle Handler should return immediately")
	}

	t.Run("async", func(t *testing.T) {
		exec, aerr := h.startExecutionTyped(context.Background(), &startExecutionRequest{
			StateMachineArn: smARN,
			Name:            "after-stop-async",
		})
		if exec != nil {
			t.Fatalf("expected no execution response, got %+v", exec)
		}
		assertShutdownRefusal(t, aerr)

		execARN := protocol.ARN("us-east-1", "000000000000", "states", "execution:race-sm:after-stop-async")
		if run := h.lookupRun(execARN); run != nil {
			t.Fatal("a run was registered for an execution refused at shutdown — no goroutine was ever launched to release it")
		}
		stored, err := h.store.GetExecution(context.Background(), execARN)
		if err != nil {
			t.Fatalf("GetExecution: %v", err)
		}
		if stored == nil {
			t.Fatal("expected the already-persisted RUNNING record to still exist, unwound to a terminal state")
		}
		if stored.Status != statusAborted {
			t.Fatalf("stored execution status = %q, want %q — a shutdown refusal must not leave a RUNNING record stuck forever", stored.Status, statusAborted)
		}
	})

	t.Run("sync", func(t *testing.T) {
		// Exercises startExecution's executionSync branch directly — the
		// path both StartSyncExecution and a nested
		// states:startExecution.sync take — rather than going through
		// StartSyncExecution's own EXPRESS-only gate, which is an unrelated
		// concern.
		exec, aerr := h.startExecution(context.Background(), smARN, "after-stop-sync", "", 0, executionSync)
		if exec != nil {
			t.Fatalf("expected no execution response, got %+v", exec)
		}
		assertShutdownRefusal(t, aerr)
	})
}

func assertShutdownRefusal(t *testing.T, aerr *protocol.AWSError) {
	t.Helper()
	if aerr == nil {
		t.Fatal("expected a refusal after Stop, got nil error")
	}
	if aerr.HTTPStatus != http.StatusServiceUnavailable {
		t.Fatalf("HTTPStatus = %d, want %d", aerr.HTTPStatus, http.StatusServiceUnavailable)
	}
}

// ─── Terminal status vs the in-flight registry ────────────────────────────────

// atTerminalWrite wraps a state.Store and calls act once, the first time the
// execution record for arn is written with a terminal status — from inside
// that write, after the record is visible. That is exactly the instant a
// client polling DescribeExecution can first see the execution as FAILED, so
// it is the instant every contract about a terminal execution has to hold at.
type atTerminalWrite struct {
	state.Store
	arn  string
	act  func()
	once sync.Once
}

func (s *atTerminalWrite) Set(ctx context.Context, namespace, key, value string) error {
	if err := s.Store.Set(ctx, namespace, key, value); err != nil {
		return err
	}
	if strings.HasSuffix(key, execPrefix+s.arn) && strings.Contains(value, `"Status":"`+statusFailed+`"`) {
		s.once.Do(s.act)
	}
	return nil
}

// TestRedriveExecution_acceptedTheInstantTheExecutionReadsTerminal pins down
// issue #1973: RedriveExecution answered ExecutionNotRedrivable ("Execution
// is already being redriven") on CI for an execution the caller had just
// observed as FAILED.
//
// The in-flight registry (runs.go) and the store were updated in the wrong
// order: the run goroutine persisted the terminal record and only then
// released the run, so between the two an execution read as FAILED while
// lookupRun still returned its finished run — and redriveExecutionTyped
// reads that as a redrive already in flight. Nothing about the window is
// specific to a Map: any execution could be refused this way, and a client
// that polls DescribeExecution and acts on the answer is racing a window it
// cannot see.
//
// This drives the window deterministically instead of hoping to land in it:
// the store itself calls back from inside the terminal write, so the
// assertions run at the one instant that matters rather than at some later
// moment the scheduler chose.
func TestRedriveExecution_acceptedTheInstantTheExecutionReadsTerminal(t *testing.T) {
	cfg := &config.Config{Region: "us-east-1", AccountID: "000000000000"}
	log := serviceutil.NewServiceLogger(zap.NewNop(), "stepfunctions")
	execARN := protocol.ARN(cfg.Region, cfg.AccountID, "states", "execution:redrive-race:once")

	type observation struct {
		inFlight bool
		aerr     *protocol.AWSError
	}
	seen := make(chan observation, 1)
	backend := &atTerminalWrite{Store: state.NewMemoryStore(), arn: execARN}
	h := newHandler(cfg, newStore(backend, cfg.Region), log, clock.New())
	backend.act = func() {
		// Read the registry before redriving: the redrive registers a run of
		// its own, so asking afterwards could never tell the two apart.
		obs := observation{inFlight: h.lookupRun(execARN) != nil}
		_, obs.aerr = h.redriveExecutionTyped(context.Background(), &redriveExecutionRequest{ExecutionArn: execARN})
		seen <- obs
	}

	ctx := context.Background()
	sm, aerr := h.createStateMachineTyped(ctx, &createStateMachineRequest{
		Name: "redrive-race", Definition: failingDefinition, RoleArn: "arn:aws:iam::000000000000:role/r",
	})
	if aerr != nil {
		t.Fatalf("createStateMachineTyped: %+v", aerr)
	}

	// When: the execution fails, and a redrive arrives at the instant its
	// FAILED record lands
	if _, aerr := h.startExecutionTyped(ctx, &startExecutionRequest{StateMachineArn: sm.StateMachineArn, Name: "once"}); aerr != nil {
		t.Fatalf("startExecutionTyped: %+v", aerr)
	}
	var obs observation
	select {
	case obs = <-seen:
	case <-time.After(10 * time.Second):
		t.Fatal("the execution never wrote a FAILED record")
	}

	// Then: the finished run was no longer registered as in flight, and the
	// redrive was accepted
	if obs.inFlight {
		t.Error("the run was still registered as in flight when its terminal record was written — a redrive arriving here is refused as already in flight")
	}
	if obs.aerr != nil {
		t.Errorf("RedriveExecution at the terminal write: %+v", obs.aerr)
	}

	// And: the redrive's own run is the one left registered, and it releases
	// itself — the finished run's cleanup must not forget someone else's.
	deadline := time.Now().Add(10 * time.Second)
	for {
		stored, err := h.store.GetExecution(ctx, execARN)
		if err != nil {
			t.Fatalf("GetExecution: %v", err)
		}
		if stored != nil && stored.Status != statusRunning && h.lookupRun(execARN) == nil {
			if stored.RedriveCount != 1 {
				t.Errorf("redriveCount = %d, want 1", stored.RedriveCount)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the redriven execution never settled: stored=%+v inFlight=%v", stored, h.lookupRun(execARN) != nil)
		}
		time.Sleep(time.Millisecond)
	}
}
