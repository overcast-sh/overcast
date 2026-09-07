// Package harness provides the core test framework for the Overcast compat Go SDK suite.
//
// Tests emit NDJSON events to stdout. Debug output goes to stderr via ctx.Log().
// Tests return an error to signal failure; nil means pass.
package harness

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aws/smithy-go"
	smithyhttp "github.com/aws/smithy-go/transport/http"
)

// TestFn is the signature for a single test.
type TestFn func(ctx context.Context, t *TestContext) error

// TestCase describes one test within a group.
type TestCase struct {
	Name string
	Fn   TestFn
	// Op is the AWS API operation name for doc links.
	// Empty string = use Name. "false" = suppress doc link.
	Op string
	// Skip, if non-empty, causes the test to be emitted as skipped.
	Skip string
	// NA, if non-empty, causes the test to be emitted with status "na".
	// Use this when the AWS SDK client does not yet expose this operation.
	// NA results are excluded from pass-rate calculations.
	NA string
	// Depends names tests in the SAME group that must have passed before this
	// one runs. RunGroup skips the test when any of them failed or was skipped.
	Depends []string
}

// TestGroup is a collection of related tests with optional setup/teardown.
type TestGroup struct {
	Suite   string
	Service string
	Name    string
	Tests   []TestCase
	// Parallel lets the group's tests run concurrently with one another,
	// bounded by the same slot count that bounds concurrent groups. Only a
	// generated probe group sets it (registry.generated.json's `parallel`):
	// its tests have no setup, no teardown, no exports and no Depends, so
	// nothing orders them and no test can observe another's outcome. Results
	// are still emitted in declaration order, so the only observable
	// difference is the wall clock.
	Parallel bool
	Setup    func(ctx context.Context, t *TestContext) error
	Teardown func(ctx context.Context, t *TestContext) error
}

// TestContext carries per-run state for tests.
type TestContext struct {
	Endpoint string
	Region   string
	RunID    string

	mu    sync.Mutex
	state map[string]any
}

// NewTestContext creates a fresh TestContext.
func NewTestContext(endpoint, region, runID string) *TestContext {
	return &TestContext{
		Endpoint: endpoint,
		Region:   region,
		RunID:    runID,
		state:    make(map[string]any),
	}
}

// Set stores a value in the context state bag.
func (t *TestContext) Set(key string, val any) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.state[key] = val
}

// Get retrieves a value from the context state bag.
func (t *TestContext) Get(key string) (any, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	v, ok := t.state[key]
	return v, ok
}

// LoadOrStore returns the value stored under key, or stores and returns what
// create() produces when there is none. The lookup and the store happen under
// one lock, which a Get-then-Set pair does not: the tests of a parallel group
// share one TestContext, and two of them racing to create the same lazily
// built value would each get a different one.
func (t *TestContext) LoadOrStore(key string, create func() any) any {
	t.mu.Lock()
	defer t.mu.Unlock()
	if v, ok := t.state[key]; ok {
		return v
	}
	v := create()
	t.state[key] = v
	return v
}

// GetString retrieves a string value from the state bag.
func (t *TestContext) GetString(key string) string {
	v, ok := t.Get(key)
	if !ok {
		return ""
	}
	s, _ := v.(string)
	return s
}

// Log writes a debug message to stderr.
func (t *TestContext) Log(msg string) {
	fmt.Fprintln(os.Stderr, "[go-sdk]", msg)
}

// Reset clears the state bag (called between groups).
func (t *TestContext) Reset() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.state = make(map[string]any)
}

// ─── NDJSON events ────────────────────────────────────────────────────────────

type runStartEvent struct {
	Event      string `json:"event"`
	Suite      string `json:"suite"`
	StartedAt  string `json:"started_at"`
	Endpoint   string `json:"endpoint"`
	Version    string `json:"version"`
	TotalTests int    `json:"total_tests,omitempty"`
}

type testResultEvent struct {
	Event      string `json:"event"`
	Suite      string `json:"suite"`
	Service    string `json:"service"`
	Group      string `json:"group"`
	Test       string `json:"test"`
	Status     string `json:"status"`
	DurationMs int64  `json:"duration_ms"`
	Error      string `json:"error,omitempty"`
}

type runEndEvent struct {
	Event         string `json:"event"`
	Suite         string `json:"suite"`
	Passed        int    `json:"passed"`
	Failed        int    `json:"failed"`
	Skipped       int    `json:"skipped"`
	Unimplemented int    `json:"unimplemented"`
	DurationMs    int64  `json:"duration_ms"`
}

// emitMu serialises writes to stdout so concurrent goroutines (parallel
// group execution) never interleave partial NDJSON lines.
var emitMu sync.Mutex

func emit(v any) {
	b, err := json.Marshal(v)
	if err != nil {
		fmt.Fprintln(os.Stderr, "harness: marshal error:", err)
		return
	}
	emitMu.Lock()
	fmt.Fprintln(os.Stdout, string(b))
	emitMu.Unlock()
}

// ─── Unimplemented detection ──────────────────────────────────────────────────

// ErrUnimplemented marks an error the emulator answered with 501, for a caller
// that has already read the raw SDK error and classified it. Wrap it rather
// than returning it bare, so the reported message stays the caller's own.
var ErrUnimplemented = errors.New("unimplemented")

// Composed is implemented by an error whose message was assembled out of
// scenario data rather than produced by the AWS SDK — the params JSON that was
// sent, expected and actual values, the SDK's own text quoted inside it.
//
// LooksUnimplementedWithoutResponse must never be applied to such a message. It
// matches a bare "501", and a run id or a port like 4501 in the params is
// enough to put one there, which would report every failure of that test as
// unimplemented. A composed error states the 501 by wrapping ErrUnimplemented
// instead. This is the treatment the cli suite's interpreter got in #1790, for
// the same reason and in the same shape.
type Composed interface{ ComposedFailure() }

// IsUnimplemented reports whether err signals a 501 / not-implemented response
// from the Overcast emulator.
//
// The sentinel is checked first, so a caller that has already classified the
// SDK's own error is believed, and a composed message is never read as text —
// see Composed. Everything else is decided from the **response the SDK's error
// carries**, not from its prose: ClassifyResponse. The substring heuristic is
// the last resort and applies only to an error that carries no response at all.
//
// The prose was the whole rule until #1924, and a 400 was enough to defeat it:
// go-sdk/secretsmanager-rotate/RotateSecretWithoutLambda asserts an
// InvalidRequestException, and one CI run whose request id happened to contain
// "501" reported it `unimplemented`, flipping a gated baseline row and failing
// an unrelated pull request.
func IsUnimplemented(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrUnimplemented) {
		return true
	}
	var composed Composed
	if errors.As(err, &composed) {
		return false
	}
	if unimplemented, decided := ClassifyResponse(err); decided {
		return unimplemented
	}
	return LooksUnimplementedWithoutResponse(err.Error())
}

// ClassifyResponse decides, from the HTTP response an AWS SDK error carries,
// whether the emulator refused the operation as unimplemented. decided is false
// when the error carries no response — the SDK failed before or after the
// exchange — which is the one case a caller may fall back to the text.
//
// Two things say "unimplemented", and both are facts of the response rather
// than of its wording:
//
//   - HTTP 501, with the x-emulator-unsupported header Overcast sets alongside
//     it on every one (internal/protocol/errors.go). Either is enough; the
//     header is read because a 501 whose body the SDK could not deserialise
//     still arrives with its headers intact.
//   - An error **code** of NotImplemented or UnknownOperationException, by
//     equality and never by containment. Overcast answers a target naming no
//     modeled operation with UnknownOperationException at HTTP 400
//     (internal/services/dynamodb/service.go), so the status alone would miss
//     it.
//
// Anything else the emulator answered is a failure of the operation, whatever
// its message contains.
func ClassifyResponse(err error) (unimplemented, decided bool) {
	status, header, ok := httpResponse(err)
	if !ok {
		return false, false
	}
	if status == http.StatusNotImplemented || header.Get("x-emulator-unsupported") == "true" {
		return true, true
	}
	return notRegisteredCode(err), true
}

// httpResponse returns the status and headers of the response err carries, and
// whether it carried one. The AWS SDK wraps a modeled error in an
// *smithy.OperationError and a transport error, so the response is reached
// through the chain rather than off the error itself; both smithy-go's
// *smithyhttp.ResponseError and the aws-sdk-go-v2 *awshttp.ResponseError that
// embeds it satisfy these interfaces.
func httpResponse(err error) (int, http.Header, bool) {
	var statuser interface{ HTTPStatusCode() int }
	if !errors.As(err, &statuser) {
		return 0, nil, false
	}
	return statuser.HTTPStatusCode(), responseHeader(err), true
}

// responseHeader returns the headers of the response err carries, or nil. The
// two shapes are the two spellings of HTTPResponse the SDK stack has.
func responseHeader(err error) http.Header {
	// nolint:bodyclose — this is a response the SDK already read and closed on
	// its way to building the error; only its headers are looked at, and
	// closing a body the middleware stack owns would be wrong.
	var smithyResp interface{ HTTPResponse() *smithyhttp.Response }
	if errors.As(err, &smithyResp) {
		if resp := smithyResp.HTTPResponse(); resp != nil && resp.Response != nil { //nolint:bodyclose
			return resp.Header
		}
	}
	var stdResp interface{ HTTPResponse() *http.Response }
	if errors.As(err, &stdResp) {
		if resp := stdResp.HTTPResponse(); resp != nil { //nolint:bodyclose
			return resp.Header
		}
	}
	return nil
}

// notRegisteredCode reports whether the API error in err's chain states one of
// the two codes Overcast uses for an operation it does not serve, by equality.
func notRegisteredCode(err error) bool {
	var apiErr smithy.APIError
	if !errors.As(err, &apiErr) {
		return false
	}
	switch apiErr.ErrorCode() {
	case "NotImplemented", "UnknownOperationException":
		return true
	}
	return false
}

// LooksUnimplementedWithoutResponse is the substring heuristic, and it is for
// an error that carries **no HTTP response** — a transport failure, or an SDK
// error raised before the exchange. Pass it what the SDK said and nothing else.
//
// It is never right for an error that reached the wire: the response states the
// status, and "501" appears in request ids, ARNs, resource names and port
// numbers. ClassifyResponse answers that case; this one only answers the case
// where there is nothing to read.
func LooksUnimplementedWithoutResponse(s string) bool {
	return strings.Contains(s, "501") ||
		strings.Contains(s, "NotImplemented") ||
		strings.Contains(s, "UnknownOperationException")
}

// ─── Group runner ─────────────────────────────────────────────────────────────

// GroupResult holds per-group counts.
type GroupResult struct {
	Passed        int
	Failed        int
	Skipped       int
	Unimplemented int
	Cancelled     int
}

// RunGroup executes one TestGroup and emits test_result events.
func RunGroup(ctx context.Context, group TestGroup, t *TestContext) GroupResult {
	var res GroupResult

	if runSetup(ctx, group, t, &res) {
		runTests(ctx, group, t, &res)
	}

	// Teardown phase (always runs). A setup that failed halfway has already
	// created some of what it was going to create, and that is exactly the
	// run that leaks: the tests were all skipped, so nothing else will ever
	// delete it.
	if group.Teardown != nil {
		if err := group.Teardown(ctx, t); err != nil {
			fmt.Fprintf(os.Stderr, "harness: teardown %q: %v\n", group.Name, err)
		}
	}

	return res
}

// runSetup runs a group's setup, if it has one. It reports whether the tests
// should run: a setup failure emits one skip per test and returns false, and
// RunGroup still runs teardown — the IR's rule (compat/model/README.md § The
// scenario file).
func runSetup(ctx context.Context, group TestGroup, t *TestContext, res *GroupResult) bool {
	if group.Setup == nil {
		return true
	}
	err := group.Setup(ctx, t)
	if err == nil {
		return true
	}
	reason := fmt.Sprintf("setup failed: %v", err)
	for _, tc := range group.Tests {
		emit(testResultEvent{
			Event:      "test_result",
			Suite:      group.Suite,
			Service:    group.Service,
			Group:      group.Name,
			Test:       tc.Name,
			Status:     "skip",
			DurationMs: 0,
			Error:      reason,
		})
		res.Skipped++
	}
	return false
}

// runTests runs every test in a group, honouring skips, na, cancellation and
// the dependency gate.
//
// A group marked Parallel whose tests declare no dependencies runs them
// concurrently; everything else runs in declaration order. Both halves of that
// condition are load-bearing. The concurrent path cannot express the
// dependency gate — it would have to decide what to skip from outcomes that
// have not happened yet — so a group declaring one is run serially even where
// the registry says parallel. The IR never produces that combination (only a
// probe group is parallel, and a probe has no exports for a Depends to
// consume), which is why this is a guard rather than a scheduler.
func runTests(ctx context.Context, group TestGroup, t *TestContext, res *GroupResult) {
	if group.Parallel && !hasDependencies(group.Tests) {
		runTestsConcurrently(ctx, group, t, res)
		return
	}
	runTestsInOrder(ctx, group, t, res)
}

// hasDependencies reports whether any test in the group declares one.
func hasDependencies(tests []TestCase) bool {
	for _, tc := range tests {
		if len(tc.Depends) > 0 {
			return true
		}
	}
	return false
}

// runTestsInOrder is the serial path: one test at a time, in declaration
// order, each result emitted as it completes.
func runTestsInOrder(ctx context.Context, group TestGroup, t *TestContext, res *GroupResult) {
	// Tests that did not pass, so a test declaring one of them as a dependency
	// is skipped rather than run against a prerequisite that never happened.
	// "na" and cancelled are deliberately absent: neither says the resource a
	// dependent needs is missing.
	failedOrSkipped := map[string]bool{}

	for _, tc := range group.Tests {
		if ctx.Err() != nil {
			emit(cancelledEvent{Event: "cancelled", Suite: group.Suite, Group: group.Name, Test: tc.Name})
			res.Cancelled++
			continue
		}
		// An na/skip marker outranks the dependency gate: a test the suite
		// never intended to run here reports why it was marked, not what
		// happened to something it does not depend on.
		out, done := marker(group, tc)
		if !done {
			// Dependency gate — skip if any declared dependency failed or was
			// skipped. Without it a single broken prerequisite reports as a
			// cascade of unrelated failures, and "dependency failed: X" is
			// what tells a reader the cause is elsewhere in the group.
			if gated, applies := dependencyGate(group, tc, failedOrSkipped); applies {
				out = gated
			} else {
				out = execute(ctx, group, t, tc)
			}
		}
		record(out, tc.Name, res, failedOrSkipped)
		emit(out.event)
	}
}

// runTestsConcurrently runs the group's tests through a bounded worker pool and
// emits their results in declaration order once all of them are in.
//
// Emitting in order rather than as each finishes is what keeps this stream
// identical to the serial path's, test for test. The dashboard, the baseline
// and the flake detector all read it, and a result order that depended on
// which call answered first would be a new source of diff noise for no
// benefit.
func runTestsConcurrently(ctx context.Context, group TestGroup, t *TestContext, res *GroupResult) {
	results := make([]testResult, len(group.Tests))
	sem := make(chan struct{}, parallelSlots())
	var wg sync.WaitGroup
	for i, tc := range group.Tests {
		if ctx.Err() != nil {
			results[i] = testResult{
				event:  cancelledEvent{Event: "cancelled", Suite: group.Suite, Group: group.Name, Test: tc.Name},
				status: statusCancelled,
			}
			continue
		}
		wg.Add(1)
		go func(i int, tc TestCase) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			results[i] = runOne(ctx, group, t, tc)
		}(i, tc)
	}
	wg.Wait()

	// No dependency bookkeeping: this path is taken only when no test declares
	// one, so the set a serial run maintains would be read by nobody.
	for i, out := range results {
		record(out, group.Tests[i].Name, res, nil)
		emit(out.event)
	}
}

// Result statuses, as they appear in the NDJSON stream.
const (
	statusPass          = "pass"
	statusFail          = "fail"
	statusSkip          = "skip"
	statusNA            = "na"
	statusUnimplemented = "unimplemented"
	statusCancelled     = "cancelled"
)

// testResult is one test's NDJSON event held rather than emitted, so a
// concurrent group can emit in declaration order.
type testResult struct {
	event  any
	status string
}

// resultEvent builds a test_result event. An empty message leaves the "error"
// key out, which is what a pass has always done.
func resultEvent(group TestGroup, name, status string, durMs int64, message string) testResultEvent {
	return testResultEvent{
		Event: "test_result", Suite: group.Suite, Service: group.Service,
		Group: group.Name, Test: name, Status: status, DurationMs: durMs, Error: message,
	}
}

// marker returns the result a test carries instead of running — na or skip —
// and whether it has one.
func marker(group TestGroup, tc TestCase) (testResult, bool) {
	if tc.NA != "" {
		return testResult{event: resultEvent(group, tc.Name, statusNA, 0, tc.NA), status: statusNA}, true
	}
	if tc.Skip != "" {
		return testResult{event: resultEvent(group, tc.Name, statusSkip, 0, tc.Skip), status: statusSkip}, true
	}
	return testResult{}, false
}

// dependencyGate returns the skip result for a test whose declared
// dependencies did not all pass, and whether it applies. Consulted only after
// marker: a test that was never going to run has its own reason.
func dependencyGate(group TestGroup, tc TestCase, failedOrSkipped map[string]bool) (testResult, bool) {
	var failedDeps []string
	for _, dep := range tc.Depends {
		if failedOrSkipped[dep] {
			failedDeps = append(failedDeps, dep)
		}
	}
	if len(failedDeps) == 0 {
		return testResult{}, false
	}
	message := fmt.Sprintf("dependency failed: %s", strings.Join(failedDeps, ", "))
	return testResult{event: resultEvent(group, tc.Name, statusSkip, 0, message), status: statusSkip}, true
}

// runOne runs a single test, or reports its na/skip marker without running it.
// It touches no state shared with another test beyond the TestContext, whose
// bag is mutex guarded, so it is safe to call concurrently for the tests of one
// group — which is why the concurrent path calls this and the serial one calls
// marker and execute separately, with the dependency gate between them.
func runOne(ctx context.Context, group TestGroup, t *TestContext, tc TestCase) testResult {
	if out, done := marker(group, tc); done {
		return out
	}
	return execute(ctx, group, t, tc)
}

// execute runs the test function and classifies its outcome.
func execute(ctx context.Context, group TestGroup, t *TestContext, tc TestCase) testResult {
	start := time.Now()
	err := tc.Fn(ctx, t)
	durMs := time.Since(start).Milliseconds()

	switch {
	case err == nil:
		return testResult{event: resultEvent(group, tc.Name, statusPass, durMs, ""), status: statusPass}
	case IsUnimplemented(err):
		return testResult{event: resultEvent(group, tc.Name, statusUnimplemented, durMs, err.Error()), status: statusUnimplemented}
	default:
		return testResult{event: resultEvent(group, tc.Name, statusFail, durMs, err.Error()), status: statusFail}
	}
}

// record folds one result into the group counters and, for a serial run, into
// the set the dependency gate reads. na is counted nowhere — it is excluded
// from pass-rate calculations, and it does not say a dependent's prerequisite
// is missing. failedOrSkipped may be nil when nothing will read it.
func record(out testResult, name string, res *GroupResult, failedOrSkipped map[string]bool) {
	switch out.status {
	case statusPass:
		res.Passed++
		return
	case statusNA:
		return
	case statusCancelled:
		res.Cancelled++
		return
	case statusSkip:
		res.Skipped++
	case statusUnimplemented:
		res.Unimplemented++
	case statusFail:
		res.Failed++
	}
	if failedOrSkipped != nil {
		failedOrSkipped[name] = true
	}
}

// parallelSlots is how many things this suite may do at once — groups in
// RunSuite and in the interactive loop, and the tests of one parallel group in
// runTestsConcurrently. OVERCAST_COMPAT_PARALLEL_SLOTS is injected by the Go
// runner; default 8.
//
// One number bounds both because it answers one question — how much load this
// machine should put on the emulator at once — and a second knob would only
// let the two drift apart.
func parallelSlots() int {
	if v := os.Getenv("OVERCAST_COMPAT_PARALLEL_SLOTS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return 8
}

// RunSuite executes all groups in parallel and emits run_start / run_end events.
// Each group receives its own independent TestContext so groups do not share
// state and can run concurrently without races.
func RunSuite(ctx context.Context, suite string, groups []TestGroup, endpoint, region, runID string) {
	start := time.Now()

	totalTests := 0
	for _, g := range groups {
		totalTests += len(g.Tests)
	}

	emit(runStartEvent{
		Event:      "run_start",
		Suite:      suite,
		StartedAt:  start.UTC().Format(time.RFC3339),
		Endpoint:   endpoint,
		Version:    "1",
		TotalTests: totalTests,
	})

	// Limit concurrent group execution. OVERCAST_COMPAT_PARALLEL_SLOTS is
	// injected by the Go runner; default 8.
	slots := 8
	if v := os.Getenv("OVERCAST_COMPAT_PARALLEL_SLOTS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			slots = n
		}
	}
	sem := make(chan struct{}, slots)

	results := make([]GroupResult, len(groups))
	var wg sync.WaitGroup
	for i, g := range groups {
		wg.Add(1)
		go func(i int, g TestGroup) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			// Per-group timeout: prevents a single hung HTTP call from
			// blocking this semaphore slot (and thus the whole suite) forever.
			groupCtx, groupCancel := context.WithTimeout(ctx, 5*time.Minute)
			defer groupCancel()
			t := NewTestContext(endpoint, region, runID)
			results[i] = RunGroup(groupCtx, g, t)
		}(i, g)
	}
	wg.Wait()

	var total GroupResult
	for _, r := range results {
		total.Passed += r.Passed
		total.Failed += r.Failed
		total.Skipped += r.Skipped
		total.Unimplemented += r.Unimplemented
	}

	elapsed := time.Since(start).Milliseconds()
	emit(runEndEvent{
		Event:         "run_end",
		Suite:         suite,
		Passed:        total.Passed,
		Failed:        total.Failed,
		Skipped:       total.Skipped,
		Unimplemented: total.Unimplemented,
		DurationMs:    elapsed,
	})
}

// ── Interactive mode types ────────────────────────────────────────────────

// StdinCommand represents a command received on stdin.
type StdinCommand struct {
	Command string    `json:"command"`
	BatchID string    `json:"batch_id,omitempty"`
	Tests   []TestRef `json:"tests,omitempty"`
	Group   string    `json:"group,omitempty"`
	Test    string    `json:"test,omitempty"`
}

// TestRef identifies a group and optional subset of tests.
type TestRef struct {
	Group string   `json:"group"`
	Tests []string `json:"tests,omitempty"`
}

type buildingEvent struct {
	Event   string `json:"event"`
	Suite   string `json:"suite"`
	Message string `json:"message"`
}

type readyEvent struct {
	Event      string `json:"event"`
	Suite      string `json:"suite"`
	TotalTests int    `json:"total_tests"`
}

type batchCompleteEvent struct {
	Event         string `json:"event"`
	Suite         string `json:"suite"`
	BatchID       string `json:"batch_id"`
	Passed        int    `json:"passed"`
	Failed        int    `json:"failed"`
	Skipped       int    `json:"skipped"`
	Unimplemented int    `json:"unimplemented"`
	Cancelled     int    `json:"cancelled"`
	DurationMs    int64  `json:"duration_ms"`
}

type cancelledEvent struct {
	Event   string `json:"event"`
	Suite   string `json:"suite"`
	BatchID string `json:"batch_id,omitempty"`
	Group   string `json:"group,omitempty"`
	Test    string `json:"test,omitempty"`
	Reason  string `json:"reason,omitempty"`
}

// EmitBuilding emits a building event during interactive setup.
func EmitBuilding(suite, message string) {
	emit(buildingEvent{Event: "building", Suite: suite, Message: message})
}

// EmitReady emits a ready event when the suite is prepared for commands.
func EmitReady(suite string, totalTests int) {
	emit(readyEvent{Event: "ready", Suite: suite, TotalTests: totalTests})
}

// EmitBatchComplete emits a batch_complete event after a batch finishes.
func EmitBatchComplete(suite, batchID string, totals GroupResult, durationMs int64) {
	emit(batchCompleteEvent{
		Event: "batch_complete", Suite: suite, BatchID: batchID,
		Passed: totals.Passed, Failed: totals.Failed,
		Skipped: totals.Skipped, Unimplemented: totals.Unimplemented,
		Cancelled: totals.Cancelled, DurationMs: durationMs,
	})
}

// EmitPong responds to an orchestrator ping with the currently executing test.
func EmitPong(suite, runningTest string) {
	emit(struct {
		Event       string `json:"event"`
		Suite       string `json:"suite"`
		RunningTest string `json:"running_test"`
	}{Event: "pong", Suite: suite, RunningTest: runningTest})
}

// ReadCommands reads NDJSON commands from stdin and sends them to the returned channel.
// It closes the channel when stdin is closed.
func ReadCommands() <-chan StdinCommand {
	ch := make(chan StdinCommand, 16)
	go func() {
		defer close(ch)
		scanner := bufio.NewScanner(os.Stdin)
		scanner.Buffer(make([]byte, 64*1024), 1024*1024)
		for scanner.Scan() {
			line := scanner.Text()
			if strings.TrimSpace(line) == "" {
				continue
			}
			var cmd StdinCommand
			if err := json.Unmarshal([]byte(line), &cmd); err != nil {
				fmt.Fprintf(os.Stderr, "[harness] invalid JSON on stdin: %s\n", line)
				continue
			}
			ch <- cmd
		}
	}()
	return ch
}
