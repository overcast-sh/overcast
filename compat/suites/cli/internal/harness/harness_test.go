package harness

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// captureEvents runs fn with os.Stdout redirected to a file and returns the
// NDJSON events it emitted. emit writes to os.Stdout directly, which is what
// the Go runner reads, so a test that wants to assert on the run's report has
// to read it the same way.
func captureEvents(t *testing.T, fn func()) []map[string]any {
	t.Helper()
	path := filepath.Join(t.TempDir(), "events.ndjson")
	f, err := os.Create(path) //nolint:gosec // a path this test just made
	if err != nil {
		t.Fatal(err)
	}
	saved := os.Stdout
	os.Stdout = f
	func() {
		defer func() {
			os.Stdout = saved
			f.Close() //nolint:errcheck
		}()
		fn()
	}()

	raw, err := os.ReadFile(path) //nolint:gosec // as above
	if err != nil {
		t.Fatal(err)
	}
	var events []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var ev map[string]any
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatalf("emitted line is not JSON: %q: %v", line, err)
		}
		events = append(events, ev)
	}
	return events
}

func statuses(events []map[string]any) map[string]string {
	out := map[string]string{}
	for _, ev := range events {
		if ev["event"] == "test_result" {
			out[fmt.Sprint(ev["test"])] = fmt.Sprint(ev["status"])
		}
	}
	return out
}

// TestSetupFailureStillRunsTeardown is the IR's rule (compat/model/README.md
// § The scenario file): a group whose setup failed reports every test as skip
// and still runs teardown. The failure that made this a bug is a setup that got
// halfway — the queue is created, the DLQ redrive is not — where skipping
// teardown leaks everything the successful steps made, with no test left to
// clean it up.
func TestSetupFailureStillRunsTeardown(t *testing.T) {
	tornDown := false
	ran := false

	events := captureEvents(t, func() {
		counts := RunGroup(context.Background(), TestGroup{
			Suite: "cli", Service: "widgets", Name: "widgets-gen-thing",
			Setup: func(context.Context, *TestContext) error {
				return errors.New("CreateDep: AccessDenied")
			},
			Tests: []TestCase{
				{Name: "CreateThing", Fn: func(context.Context, *TestContext) error { ran = true; return nil }},
				{Name: "DeleteThing", Fn: func(context.Context, *TestContext) error { ran = true; return nil }},
			},
			Teardown: func(context.Context, *TestContext) error { tornDown = true; return nil },
		})
		if counts.Skipped != 2 || counts.Passed != 0 {
			t.Errorf("counts = %+v, want two skips and nothing run", counts)
		}
	})

	if !tornDown {
		t.Error("teardown must run after a failed setup — that is the run that leaks")
	}
	if ran {
		t.Error("no test may run when setup failed")
	}
	got := statuses(events)
	if got["CreateThing"] != "skip" || got["DeleteThing"] != "skip" {
		t.Errorf("statuses = %v, want every test skipped", got)
	}
	var sawSetupError bool
	for _, ev := range events {
		if ev["event"] == "group_setup_error" {
			sawSetupError = true
		}
		if ev["event"] == "test_result" && !strings.Contains(fmt.Sprint(ev["error"]), "setup failed: CreateDep: AccessDenied") {
			t.Errorf("skip reason = %q, want it to name the setup failure", ev["error"])
		}
	}
	if !sawSetupError {
		t.Error("a failed setup must emit group_setup_error")
	}
}

// TestTeardownRunsAfterTheTests keeps the ordinary path honest alongside the
// one above: teardown is not something only a failed setup triggers.
func TestTeardownRunsAfterTheTests(t *testing.T) {
	var order []string
	captureEvents(t, func() {
		counts := RunGroup(context.Background(), TestGroup{
			Suite: "cli", Name: "widgets-gen-thing",
			Setup: func(context.Context, *TestContext) error { order = append(order, "setup"); return nil },
			Tests: []TestCase{{Name: "CreateThing", Fn: func(context.Context, *TestContext) error {
				order = append(order, "test")
				return nil
			}}},
			Teardown: func(context.Context, *TestContext) error { order = append(order, "teardown"); return nil },
		})
		if counts.Passed != 1 {
			t.Errorf("counts = %+v, want one pass", counts)
		}
	})
	if strings.Join(order, ",") != "setup,test,teardown" {
		t.Errorf("order = %v", order)
	}
}

// ─── unimplemented classification ─────────────────────────────────────────────

// composedFailure stands in for the scenario interpreter's failure type: a
// message assembled out of scenario data, which the substring heuristic must
// never be pointed at.
type composedFailure struct{ msg string }

func (c composedFailure) Error() string  { return c.msg }
func (composedFailure) ComposedFailure() {}

type wrappedUnimplemented struct{ err error }

func (w wrappedUnimplemented) Error() string   { return w.err.Error() }
func (w wrappedUnimplemented) Unwrap() []error { return []error{w.err, ErrUnimplemented} }

// TestIsUnimplementedReadsTheSentinelNotTheProse is the bug this classification
// existed to have, twice over: the heuristic matches a bare "501", and both a
// composed failure message (which embeds the params JSON) and a plain CLI error
// (which quotes a request id, an ARN or a resource name) are enough to put one
// there. Neither is read as text now — a composed failure states its own
// classification, and a CLI error is classified from the code its banner
// states.
func TestIsUnimplementedReadsTheSentinelNotTheProse(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "a raw CLI 501",
			err:  errors.New(`aws widgets probe: exit status 254: An error occurred (501) when calling the Probe operation`),
			want: true,
		},
		{
			name: "a raw CLI error that is not a 501",
			err:  errors.New(`aws widgets get: exit status 254: An error occurred (AccessDenied) when calling the Get operation`),
			want: false,
		},
		{
			// #1924: the bug. The banner names a 400, and the "501" is in the
			// request id — the same shape that flipped the sibling go-sdk
			// suite's RotateSecretWithoutLambda row on CI run 34064243252.
			name: "a 400 whose request id contains 501",
			err: errors.New(`aws secretsmanager rotate-secret: exit status 254: An error occurred (InvalidRequestException) ` +
				`when calling the RotateSecret operation: No Lambda rotation function ARN is associated with this secret. ` +
				`(Request ID: 5f2c9501-0f3a-4c7d-9a11-6b1d0c2e4a77)`),
			want: false,
		},
		{
			name: "a 400 whose resource name contains 501",
			err: errors.New(`aws secretsmanager describe-secret: exit status 254: An error occurred (ResourceNotFoundException) ` +
				`when calling the DescribeSecret operation: Secrets Manager can't find the specified secret: oc-501abcde-rotate`),
			want: false,
		},
		{
			// A real 501: Overcast's body models cleanly, so the CLI states
			// the code rather than falling back to the status.
			name: "a real 501, stated as its code",
			err: errors.New(`aws scheduler create-schedule: exit status 254: An error occurred (NotImplemented) ` +
				`when calling the CreateSchedule operation: This operation is not implemented by the emulator`),
			want: true,
		},
		{
			// No banner at all: the CLI never reached the wire, or echoed a
			// body it could not model. The heuristic is all there is.
			name: "output stating no code at all",
			err: errors.New(`aws widgets probe: exit status 255: ` +
				`{"__type":"UnknownOperationException","message":"Unknown target: Widgets.Probe"}`),
			want: true,
		},
		{
			name: "a composed failure whose params happen to contain 501",
			err:  composedFailure{msg: `widgets-gen-thing/GetThing: GetThing params {"Id":"oc-501abcde-thing"}: responseField equals at $.Id: expected "x", actual "y" (compat/model/scenarios/widgets.json assert[0])`},
			want: false,
		},
		{
			name: "a composed failure whose port happens to contain 501",
			err:  composedFailure{msg: `widgets-gen-thing/GetThing: GetThing params {"Endpoint":"http://127.0.0.1:4501"}: readback nonEmpty at $.Id: expected a non-empty value, actual <missing> (f.json assert[0])`},
			want: false,
		},
		{
			name: "a composed failure carrying the sentinel",
			err:  wrappedUnimplemented{err: composedFailure{msg: `widgets-gen-probe/Probe: Probe params {}: call: expected the call to succeed, actual "An error occurred (501) …" (f.json call)`}},
			want: true,
		},
		{
			name: "the sentinel through fmt.Errorf's %w",
			err:  fmt.Errorf("eventually gave up after 3 attempt(s) 0ms apart; last failure: %w", wrappedUnimplemented{err: composedFailure{msg: "x"}}),
			want: true,
		},
		{
			name: "a composed failure through fmt.Errorf's %w",
			err:  fmt.Errorf("eventually gave up after 3 attempt(s) 0ms apart; last failure: %w", composedFailure{msg: `params {"Id":"oc-501abcde"}`}),
			want: false,
		},
		{name: "no error", err: nil, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsUnimplemented(tc.err); got != tc.want {
				t.Errorf("IsUnimplemented = %v, want %v for %v", got, tc.want, tc.err)
			}
		})
	}
}

// TestUnimplementedStatusIsReportedForTheWrappedSentinel walks the whole path a
// generated probe test takes, since the classification only matters where
// RunGroup applies it.
func TestUnimplementedStatusIsReportedForTheWrappedSentinel(t *testing.T) {
	events := captureEvents(t, func() {
		counts := RunGroup(context.Background(), TestGroup{
			Suite: "cli", Name: "widgets-gen-probe",
			Tests: []TestCase{
				{Name: "Probe", Fn: func(context.Context, *TestContext) error {
					return wrappedUnimplemented{err: composedFailure{msg: `probe: expected the call to succeed, actual "501"`}}
				}},
				{Name: "Get", Fn: func(context.Context, *TestContext) error {
					return composedFailure{msg: `params {"Id":"oc-501abcde"}: expected "a", actual "b"`}
				}},
			},
		})
		if counts.Unimplemented != 1 || counts.Failed != 1 {
			t.Errorf("counts = %+v, want one unimplemented and one fail", counts)
		}
	})
	got := statuses(events)
	if got["Probe"] != "unimplemented" {
		t.Errorf("Probe = %q, want unimplemented", got["Probe"])
	}
	if got["Get"] != "fail" {
		t.Errorf("Get = %q, want fail — a 501 in the params says nothing about the status", got["Get"])
	}
}

// ── Parallel groups (#1801) ──────────────────────────────────────────────────

// barrierTests builds n tests, each of which blocks until every one of them has
// started. Serially the group deadlocks and the barrier times out; run
// concurrently it clears immediately, so "did these actually overlap" is
// answered by the tests themselves rather than by a wall-clock threshold that
// a loaded CI machine can make lie.
func barrierTests(n int) []TestCase {
	started := make(chan struct{}, n)
	released := make(chan struct{})
	var tests []TestCase
	for i := 0; i < n; i++ {
		tests = append(tests, TestCase{
			Name: fmt.Sprintf("Probe%02d", i),
			Fn: func(ctx context.Context, _ *TestContext) error {
				started <- struct{}{}
				select {
				case <-released:
				case <-ctx.Done():
					return ctx.Err()
				}
				return nil
			},
		})
	}
	go func() {
		for i := 0; i < n; i++ {
			<-started
		}
		close(released)
	}()
	return tests
}

// TestParallelGroupRunsItsTestsConcurrently proves the flag does what it says:
// eight tests that each wait for all eight to have started can only finish if
// they overlap.
func TestParallelGroupRunsItsTestsConcurrently(t *testing.T) {
	t.Setenv("OVERCAST_COMPAT_PARALLEL_SLOTS", "8")
	tests := barrierTests(8)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var counts GroupCounts
	events := captureEvents(t, func() {
		counts = RunGroup(ctx, TestGroup{Suite: "cli", Service: "widgets", Name: "widgets-gen-probe", Parallel: true, Tests: tests})
	})
	if err := ctx.Err(); err != nil {
		t.Fatalf("the group did not finish: %v — the tests did not overlap", err)
	}
	if counts.Passed != 8 {
		t.Fatalf("counts = %+v, want 8 passed", counts)
	}
	if got := len(events); got != 8 {
		t.Fatalf("%d events, want 8", got)
	}
}

// TestParallelGroupEmitsInRegistryOrder pins the half of the contract that is
// not about speed: results come out in declaration order whatever order the
// tests finished in, so the NDJSON a parallel group produces is the stream the
// serial path produced.
func TestParallelGroupEmitsInRegistryOrder(t *testing.T) {
	t.Setenv("OVERCAST_COMPAT_PARALLEL_SLOTS", "8")
	// Test i sleeps (8-i) ms, so completion order is the reverse of
	// declaration order.
	var tests []TestCase
	for i := 0; i < 8; i++ {
		i := i
		tests = append(tests, TestCase{
			Name: fmt.Sprintf("Probe%02d", i),
			Fn: func(context.Context, *TestContext) error {
				time.Sleep(time.Duration(8-i) * time.Millisecond)
				if i%3 == 0 {
					return errors.New("boom")
				}
				return nil
			},
		})
	}

	events := captureEvents(t, func() {
		RunGroup(context.Background(), TestGroup{Suite: "cli", Service: "widgets", Name: "widgets-gen-probe", Parallel: true, Tests: tests})
	})

	var got []string
	for _, ev := range events {
		got = append(got, fmt.Sprint(ev["test"]))
	}
	want := []string{"Probe00", "Probe01", "Probe02", "Probe03", "Probe04", "Probe05", "Probe06", "Probe07"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("results emitted as %v, want declaration order %v", got, want)
	}
	if s := statuses(events); s["Probe00"] != "fail" || s["Probe01"] != "pass" {
		t.Errorf("statuses = %v", s)
	}
}

// TestGroupWithoutTheFlagRunsSerially is the other half of "a group without the
// flag runs exactly as today": the tests observe one another running, which
// only holds while nothing overlaps.
func TestGroupWithoutTheFlagRunsSerially(t *testing.T) {
	var mu sync.Mutex
	inFlight, maxInFlight := 0, 0
	var order []string

	var tests []TestCase
	for i := 0; i < 5; i++ {
		name := fmt.Sprintf("Probe%02d", i)
		tests = append(tests, TestCase{
			Name: name,
			Fn: func(context.Context, *TestContext) error {
				mu.Lock()
				inFlight++
				if inFlight > maxInFlight {
					maxInFlight = inFlight
				}
				order = append(order, name)
				mu.Unlock()
				time.Sleep(time.Millisecond)
				mu.Lock()
				inFlight--
				mu.Unlock()
				return nil
			},
		})
	}

	events := captureEvents(t, func() {
		RunGroup(context.Background(), TestGroup{Suite: "cli", Service: "widgets", Name: "widgets-gen-widget", Tests: tests})
	})
	if maxInFlight != 1 {
		t.Fatalf("%d tests ran at once in a group that did not ask for it", maxInFlight)
	}
	if strings.Join(order, ",") != "Probe00,Probe01,Probe02,Probe03,Probe04" {
		t.Errorf("ran in order %v", order)
	}
	if len(events) != 5 {
		t.Errorf("%d events, want 5", len(events))
	}
}

// TestParallelGroupWithDependenciesFallsBackToSerial: the concurrent path
// cannot express the dependency gate, so a group that declares one runs in
// order even where the registry says parallel. The IR never produces this
// combination — only probe groups are parallel, and a probe has no exports —
// but a corpus that did must not silently lose the cascade skip.
func TestParallelGroupWithDependenciesFallsBackToSerial(t *testing.T) {
	tests := []TestCase{
		{Name: "First", Fn: func(context.Context, *TestContext) error { return errors.New("boom") }},
		{Name: "Second", Depends: []string{"First"}, Fn: func(context.Context, *TestContext) error { return nil }},
	}
	var counts GroupCounts
	events := captureEvents(t, func() {
		counts = RunGroup(context.Background(), TestGroup{Suite: "cli", Service: "widgets", Name: "widgets-gen-probe", Parallel: true, Tests: tests})
	})
	if s := statuses(events); s["Second"] != "skip" {
		t.Fatalf("Second = %q, want skip: the dependency gate was lost", s["Second"])
	}
	if counts.Failed != 1 || counts.Skipped != 1 {
		t.Errorf("counts = %+v, want 1 failed and 1 skipped", counts)
	}
}

// TestMarkerOutranksTheDependencyGate pins the order the two checks have always
// been in. A test the suite marked na or skip never ran and never will, so it
// reports why it was marked — not "dependency failed", which would move an na
// into the skip counter and replace a skip's own reason with a cascade
// message.
func TestMarkerOutranksTheDependencyGate(t *testing.T) {
	tests := []TestCase{
		{Name: "First", Fn: func(context.Context, *TestContext) error { return errors.New("boom") }},
		{Name: "Marked", Depends: []string{"First"}, Skip: "requires docker", Fn: _noopTest},
		{Name: "Unavailable", Depends: []string{"First"}, NA: "the AWS CLI has no such command", Fn: _noopTest},
		{Name: "Ordinary", Depends: []string{"First"}, Fn: _noopTest},
	}
	var counts GroupCounts
	events := captureEvents(t, func() {
		counts = RunGroup(context.Background(), TestGroup{Suite: "cli", Service: "widgets", Name: "widgets-gen-widget", Tests: tests})
	})

	byTest := map[string]map[string]any{}
	for _, ev := range events {
		byTest[fmt.Sprint(ev["test"])] = ev
	}
	if got := byTest["Marked"]; got["status"] != "skip" || got["error"] != "requires docker" {
		t.Errorf("Marked = %v, want its own skip reason", got)
	}
	if got := byTest["Unavailable"]; got["status"] != "na" {
		t.Errorf("Unavailable = %v, want na", got)
	}
	if got := byTest["Ordinary"]; got["status"] != "skip" || got["error"] != "dependency failed: First" {
		t.Errorf("Ordinary = %v, want the cascade skip", got)
	}
	// na stays out of every counter; the marked skip and the cascade skip are
	// the two skips.
	if counts.Failed != 1 || counts.Skipped != 2 {
		t.Errorf("counts = %+v, want 1 failed and 2 skipped", counts)
	}
}

func _noopTest(context.Context, *TestContext) error { return nil }

// blockUntilDone is a test that waits for the group context to expire and
// returns its error, the way a real test does when the CLI it spawned is
// killed with the context.
func blockUntilDone(ctx context.Context, _ *TestContext) error {
	<-ctx.Done()
	return fmt.Errorf("DescribeKey: readback: %w", ctx.Err())
}

func passing(context.Context, *TestContext) error { return nil }

// reasons returns the error field of every test_result, keyed by test name.
func reasons(events []map[string]any) map[string]string {
	out := map[string]string{}
	for _, ev := range events {
		if ev["event"] == "test_result" {
			out[fmt.Sprint(ev["test"])] = fmt.Sprint(ev["error"])
		}
	}
	return out
}

func countEvents(events []map[string]any, kind string) int {
	n := 0
	for _, ev := range events {
		if ev["event"] == kind {
			n++
		}
	}
	return n
}

// TestGroupBudget_scalesWithTheTestCount pins the shape of the budget: a base
// for setup, teardown and a hung process, plus an allowance per test. The
// numbers are the ones the comment on the constants justifies; a change to
// either is a change to how much a loaded runner is allowed to slow down.
func TestGroupBudget_scalesWithTheTestCount(t *testing.T) {
	// Given: an empty group and the 33-test group that overran the flat cap
	empty := TestGroup{}
	kms := TestGroup{Tests: make([]TestCase, 33)}

	// When/Then: the base is the old flat cap, and the per-test allowance is
	// what lifts a large group clear of it
	if got := GroupBudget(empty); got != 5*time.Minute {
		t.Errorf("GroupBudget(empty) = %s, want the 5m base", got)
	}
	if got := GroupBudget(kms); got != 5*time.Minute+33*20*time.Second {
		t.Errorf("GroupBudget(33 tests) = %s, want 5m + 33x20s = 16m", got)
	}
	if GroupBudget(kms) <= 5*time.Minute {
		t.Error("a 33-test group must get more than the flat cap it overran")
	}
}

// TestGroupDeadline_reportsEveryUnrunTestAsACascadeSkip is the reporting half
// of #1966. cli/kms-gen-key ran out of its budget on DisableKey; the harness
// emitted `cancelled` for the four tests after it, an event the aggregate
// drops, so the parity gate saw four registry tests nobody had reported and
// failed the pull request for unrecorded debt. A deadline is this harness's
// own decision, so every test it left unrun gets a test_result naming that,
// worded as the cascade it is.
func TestGroupDeadline_reportsEveryUnrunTestAsACascadeSkip(t *testing.T) {
	// Given: a serial group whose second test outlives the group budget
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	group := TestGroup{
		Suite: "cli", Service: "kms", Name: "kms-gen-key",
		Tests: []TestCase{
			{Name: "CreateKey", Fn: passing},
			{Name: "DisableKey", Fn: blockUntilDone, Depends: []string{"CreateKey"}},
			{Name: "EnableKey", Fn: passing, Depends: []string{"CreateKey"}},
			{Name: "ScheduleKeyDeletion", Fn: passing, Depends: []string{"CreateKey"}},
			{Name: "GenerateRandomNA", Fn: passing, NA: "the CLI has no such command"},
		},
	}

	// When: the group runs
	var counts GroupCounts
	events := captureEvents(t, func() { counts = RunGroup(ctx, group) })

	// Then: one row per test, none of them a cancelled event
	if got := countEvents(events, "test_result"); got != len(group.Tests) {
		t.Fatalf("%d test_result events, want one per test (%d): %v", got, len(group.Tests), events)
	}
	if got := countEvents(events, "cancelled"); got != 0 {
		t.Errorf("%d cancelled events, want none: the aggregate never reads them", got)
	}
	if counts.Passed != 1 || counts.Failed != 1 || counts.Skipped != 2 || counts.Cancelled != 0 {
		t.Errorf("counts = %+v, want 1 pass, 1 fail, 2 skips, 0 cancelled", counts)
	}

	// And: the test that was running fails, saying the budget ran out under it
	got, why := statuses(events), reasons(events)
	if got["DisableKey"] != "fail" {
		t.Errorf("DisableKey = %s, want fail", got["DisableKey"])
	}
	if !strings.HasPrefix(why["DisableKey"], "group timed out after ") || !strings.Contains(why["DisableKey"], " during this test: DescribeKey: readback: context deadline exceeded") {
		t.Errorf("DisableKey reason = %q, want the budget named and the original error kept", why["DisableKey"])
	}

	// And: every test after it is a skip the parity checker reads as a cascade
	for _, name := range []string{"EnableKey", "ScheduleKeyDeletion"} {
		if got[name] != "skip" {
			t.Errorf("%s = %s, want skip", name, got[name])
		}
		if !strings.HasPrefix(why[name], "group timed out after ") || !strings.Contains(why[name], " before this test ran (last test to run: DisableKey)") {
			t.Errorf("%s reason = %q, want the cascade wording naming DisableKey", name, why[name])
		}
	}

	// And: a marker still outranks the deadline
	if got["GenerateRandomNA"] != "na" || why["GenerateRandomNA"] != "the CLI has no such command" {
		t.Errorf("GenerateRandomNA = %s %q, want its na marker", got["GenerateRandomNA"], why["GenerateRandomNA"])
	}
}

// TestGroupCancel_stillEmitsCancelledEvents keeps the dashboard protocol
// intact: a cancel is the orchestrator's doing and it consumes the
// acknowledgements, so only a deadline is turned into results.
func TestGroupCancel_stillEmitsCancelledEvents(t *testing.T) {
	// Given: a serial group whose first test cancels the run, as a dashboard
	// cancel would from outside
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	group := TestGroup{
		Suite: "cli", Service: "kms", Name: "kms-gen-key",
		Tests: []TestCase{
			{Name: "CreateKey", Fn: func(context.Context, *TestContext) error { cancel(); return nil }},
			{Name: "DisableKey", Fn: passing},
		},
	}

	// When: the group runs
	var counts GroupCounts
	events := captureEvents(t, func() { counts = RunGroup(ctx, group) })

	// Then: the unrun test is acknowledged as cancelled, not reported as a skip
	if got := countEvents(events, "cancelled"); got != 1 {
		t.Errorf("%d cancelled events, want 1", got)
	}
	if got := countEvents(events, "test_result"); got != 1 {
		t.Errorf("%d test_result events, want only CreateKey", got)
	}
	if counts.Cancelled != 1 || counts.Skipped != 0 {
		t.Errorf("counts = %+v, want 1 cancelled and no skips", counts)
	}
}

// TestParallelGroupDeadline_reportsUndispatchedTestsAsSkips is the concurrent
// path: a probe group whose budget is already gone reports every test rather
// than none.
func TestParallelGroupDeadline_reportsUndispatchedTestsAsSkips(t *testing.T) {
	// Given: an expired budget and a parallel group with an na marker in it
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	group := TestGroup{
		Suite: "cli", Service: "kms", Name: "kms-gen-probe", Parallel: true,
		Tests: []TestCase{
			{Name: "DescribeKey", Fn: passing},
			{Name: "ListKeys", Fn: passing},
			{Name: "Sign", Fn: passing, NA: "the CLI has no such command"},
		},
	}

	// When: the group runs
	var counts GroupCounts
	events := captureEvents(t, func() { counts = RunGroup(ctx, group) })

	// Then: three rows, two of them timed-out skips, no cancelled events
	if got := countEvents(events, "test_result"); got != 3 {
		t.Fatalf("%d test_result events, want 3: %v", got, events)
	}
	if got := countEvents(events, "cancelled"); got != 0 {
		t.Errorf("%d cancelled events, want none", got)
	}
	if counts.Skipped != 2 || counts.Cancelled != 0 {
		t.Errorf("counts = %+v, want 2 skips and nothing cancelled", counts)
	}
	got, why := statuses(events), reasons(events)
	for _, name := range []string{"DescribeKey", "ListKeys"} {
		if got[name] != "skip" || !strings.HasPrefix(why[name], "group timed out after ") {
			t.Errorf("%s = %s %q, want a group timed out skip", name, got[name], why[name])
		}
	}
	if got["Sign"] != "na" {
		t.Errorf("Sign = %s, want its na marker to outrank the deadline", got["Sign"])
	}
}
