package harness

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	"github.com/aws/smithy-go"
	smithyhttp "github.com/aws/smithy-go/transport/http"
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
		ctx := NewTestContext("http://127.0.0.1:4566", "us-east-1", "run")
		res := RunGroup(context.Background(), TestGroup{
			Suite: "go-sdk", Service: "widgets", Name: "widgets-gen-thing",
			Setup: func(context.Context, *TestContext) error {
				return errors.New("CreateDep: AccessDenied")
			},
			Tests: []TestCase{
				{Name: "CreateThing", Fn: func(context.Context, *TestContext) error { ran = true; return nil }},
				{Name: "DeleteThing", Fn: func(context.Context, *TestContext) error { ran = true; return nil }},
			},
			Teardown: func(context.Context, *TestContext) error { tornDown = true; return nil },
		}, ctx)
		if res.Skipped != 2 || res.Passed != 0 {
			t.Errorf("res = %+v, want two skips and nothing run", res)
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
	for _, ev := range events {
		if ev["event"] == "test_result" && !strings.Contains(fmt.Sprint(ev["error"]), "setup failed: CreateDep: AccessDenied") {
			t.Errorf("skip reason = %q, want it to name the setup failure", ev["error"])
		}
	}
}

// TestTeardownRunsAfterTheTests keeps the ordinary path honest alongside the
// one above: teardown is not something only a failed setup triggers.
func TestTeardownRunsAfterTheTests(t *testing.T) {
	var order []string
	captureEvents(t, func() {
		ctx := NewTestContext("http://127.0.0.1:4566", "us-east-1", "run")
		res := RunGroup(context.Background(), TestGroup{
			Suite: "go-sdk", Name: "widgets-gen-thing",
			Setup: func(context.Context, *TestContext) error { order = append(order, "setup"); return nil },
			Tests: []TestCase{{Name: "CreateThing", Fn: func(context.Context, *TestContext) error {
				order = append(order, "test")
				return nil
			}}},
			Teardown: func(context.Context, *TestContext) error { order = append(order, "teardown"); return nil },
		}, ctx)
		if res.Passed != 1 {
			t.Errorf("res = %+v, want one pass", res)
		}
	})
	if strings.Join(order, ",") != "setup,test,teardown" {
		t.Errorf("order = %v", order)
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

func probeContext() *TestContext {
	return NewTestContext("http://127.0.0.1:4566", "us-east-1", "run")
}

// TestParallelGroupRunsItsTestsConcurrently proves the flag does what it says:
// eight tests that each wait for all eight to have started can only finish if
// they overlap.
func TestParallelGroupRunsItsTestsConcurrently(t *testing.T) {
	t.Setenv("OVERCAST_COMPAT_PARALLEL_SLOTS", "8")
	tests := barrierTests(8)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var res GroupResult
	events := captureEvents(t, func() {
		res = RunGroup(ctx, TestGroup{Suite: "go-sdk", Service: "widgets", Name: "widgets-gen-probe", Parallel: true, Tests: tests}, probeContext())
	})
	if err := ctx.Err(); err != nil {
		t.Fatalf("the group did not finish: %v — the tests did not overlap", err)
	}
	if res.Passed != 8 {
		t.Fatalf("res = %+v, want 8 passed", res)
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
		RunGroup(context.Background(), TestGroup{Suite: "go-sdk", Service: "widgets", Name: "widgets-gen-probe", Parallel: true, Tests: tests}, probeContext())
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
		RunGroup(context.Background(), TestGroup{Suite: "go-sdk", Service: "widgets", Name: "widgets-gen-widget", Tests: tests}, probeContext())
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
	var res GroupResult
	events := captureEvents(t, func() {
		res = RunGroup(context.Background(), TestGroup{Suite: "go-sdk", Service: "widgets", Name: "widgets-gen-probe", Parallel: true, Tests: tests}, probeContext())
	})
	if s := statuses(events); s["Second"] != "skip" {
		t.Fatalf("Second = %q, want skip: the dependency gate was lost", s["Second"])
	}
	if res.Failed != 1 || res.Skipped != 1 {
		t.Errorf("res = %+v, want 1 failed and 1 skipped", res)
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
		{Name: "Unavailable", Depends: []string{"First"}, NA: "not yet supported by the AWS Go SDK v2", Fn: _noopTest},
		{Name: "Ordinary", Depends: []string{"First"}, Fn: _noopTest},
	}
	var res GroupResult
	events := captureEvents(t, func() {
		res = RunGroup(context.Background(), TestGroup{Suite: "go-sdk", Service: "widgets", Name: "widgets-gen-widget", Tests: tests}, probeContext())
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
	if res.Failed != 1 || res.Skipped != 2 {
		t.Errorf("res = %+v, want 1 failed and 2 skipped", res)
	}
}

func _noopTest(context.Context, *TestContext) error { return nil }

// ── unimplemented classification (#1924) ─────────────────────────────────────

// composedFailure stands in for the scenario interpreter's failure type: a
// message assembled out of scenario data, which the substring heuristic must
// never be pointed at.
type composedFailure struct{ msg string }

func (c composedFailure) Error() string  { return c.msg }
func (composedFailure) ComposedFailure() {}

type wrappedUnimplemented struct{ err error }

func (w wrappedUnimplemented) Error() string   { return w.err.Error() }
func (w wrappedUnimplemented) Unwrap() []error { return []error{w.err, ErrUnimplemented} }

// sdkError builds the error shape the AWS Go SDK v2 hands a caller: the modeled
// API error inside a transport error carrying the response, inside the
// operation error that names the call. Nothing here is a stand-in — these are
// the SDK's own types, and IsUnimplemented reads them the way it reads a real
// one.
func sdkError(status int, code, message, requestID string, header http.Header) error {
	resp := &smithyhttp.Response{Response: &http.Response{
		StatusCode: status,
		Header:     header,
	}}
	if resp.Header == nil {
		resp.Header = http.Header{}
	}
	return &smithy.OperationError{
		ServiceID:     "Secrets Manager",
		OperationName: "RotateSecret",
		Err: &awshttp.ResponseError{
			ResponseError: &smithyhttp.ResponseError{
				Response: resp,
				Err:      &smithy.GenericAPIError{Code: code, Message: message},
			},
			RequestID: requestID,
		},
	}
}

// TestIsUnimplementedReadsTheResponseNotTheProse is the bug this classification
// exists to have (#1924). The heuristic matched a bare "501", and a request id
// is enough to put one in a 400's text: on CI run 34064243252
// go-sdk/secretsmanager-rotate/RotateSecretWithoutLambda — a test that asserts
// an InvalidRequestException — was reported `unimplemented`, which flipped a
// gated baseline row and failed an unrelated pull request.
func TestIsUnimplementedReadsTheResponseNotTheProse(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "a 400 whose request id contains 501",
			err: sdkError(http.StatusBadRequest, "InvalidRequestException",
				"No Lambda rotation function ARN is associated with this secret.",
				"5f2c9501-0f3a-4c7d-9a11-6b1d0c2e4a77", nil),
			want: false,
		},
		{
			name: "a 400 whose message contains 501",
			err: sdkError(http.StatusBadRequest, "InvalidRequestException",
				"Secret oc-501abcde-rotate is not versioned", "req-1", nil),
			want: false,
		},
		{
			name: "a real 501",
			err: sdkError(http.StatusNotImplemented, "NotImplemented",
				"This operation is not implemented by the emulator", "req-2",
				http.Header{"X-Emulator-Unsupported": []string{"true"}}),
			want: true,
		},
		{
			name: "a 501 whose body the SDK could not model, named only by the header",
			err: sdkError(http.StatusNotImplemented, "", "", "req-3",
				http.Header{"X-Emulator-Unsupported": []string{"true"}}),
			want: true,
		},
		{
			name: "an unknown operation, which AWS answers 400",
			err:  sdkError(http.StatusBadRequest, "UnknownOperationException", "Unknown operation: Frobnicate", "req-4", nil),
			want: true,
		},
		{
			name: "a transport failure carrying no response",
			err:  fmt.Errorf("operation error Secrets Manager: RotateSecret: %w", errors.New("dial tcp 127.0.0.1:4501: connect: connection refused")),
			want: true, // no response to read: the heuristic is all there is
		},
		{
			name: "a composed failure whose params happen to contain 501",
			err:  composedFailure{msg: `widgets-gen-thing/GetThing: GetThing params {"Id":"oc-501abcde-thing"}: responseField equals at $.Id: expected "x", actual "y" (f.json assert[0])`},
			want: false,
		},
		{
			name: "a composed failure carrying the sentinel",
			err:  wrappedUnimplemented{err: composedFailure{msg: `widgets-gen-probe/Probe: Probe params {}: call: expected the call to succeed, actual "501 …" (f.json call)`}},
			want: true,
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
