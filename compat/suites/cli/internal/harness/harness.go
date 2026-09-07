// Package harness provides the core test framework for the Overcast compat CLI suite.
//
// Tests emit NDJSON events to stdout. Each test calls the AWS CLI via exec.Command
// and fails if the command exits non-zero or produces unexpected output.
package harness

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// TestFn is the signature for a single test.
type TestFn func(ctx context.Context, t *TestContext) error

// TestCase describes one test within a group.
type TestCase struct {
	Name string
	Fn   TestFn
	Op   string
	Skip string
	// NA, if non-empty, causes the test to be emitted with status "na".
	// Use this when the AWS CLI does not yet expose this operation.
	// NA results are excluded from pass-rate calculations.
	NA string
	// Depends lists test names in the same group that must pass before this
	// test runs.  If any dependency failed or was skipped, this test is
	// automatically skipped.
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
	fmt.Fprintln(os.Stderr, "[cli]", msg)
}

// Reset clears the state bag (called between groups).
func (t *TestContext) Reset() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.state = make(map[string]any)
}

// ErrUnimplemented marks an error the emulator answered with 501, for a caller
// that has already read the raw CLI text and classified it. Wrap it rather than
// returning it bare, so the reported message stays the caller's own.
var ErrUnimplemented = errors.New("unimplemented")

// Composed is implemented by an error whose message an interpreter assembled
// out of scenario data rather than one the AWS CLI produced — the params JSON
// that was sent, expected and actual values, the CLI's own text quoted inside
// it.
//
// LooksUnimplementedWithoutBanner must never be applied to such a message. It
// matches a bare "501", and a run id (`oc-` plus eight hex digits) or a port
// like 4501 in the params is enough to put one there, which would report every
// failure of that test as unimplemented. A composed error states the 501 by
// wrapping ErrUnimplemented instead.
type Composed interface{ ComposedFailure() }

// IsUnimplemented reports whether an error represents a 501 / not-implemented
// response.
//
// The sentinel is checked first, so a caller that has already classified the
// CLI's own text is believed, and a composed message is never read as text —
// see Composed. Everything else is decided from the **code the CLI states**,
// not from its prose: ClassifyBanner. The substring heuristic is the last
// resort and applies only to output stating no code at all.
//
// The prose was the whole rule until #1924, and a 400 was enough to defeat it:
// the sibling go-sdk suite reported its RotateSecretWithoutLambda test —
// which asserts an InvalidRequestException — as `unimplemented` on one CI run
// whose request id happened to contain "501", flipping a gated baseline row
// and failing an unrelated pull request. The CLI's output carries the same
// hazard in the same places.
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
	if unimplemented, decided := ClassifyBanner(err.Error()); decided {
		return unimplemented
	}
	return LooksUnimplementedWithoutBanner(err.Error())
}

// ClassifyBanner decides, from the error code the AWS CLI states, whether the
// emulator refused the operation as unimplemented. decided is false when the
// output states no code — the CLI died before it reached the wire, or printed a
// body it could not model — which is the one case a caller may fall back to the
// text.
//
// The CLI never prints an HTTP status of its own accord, but its banner carries
// what stands in for one, and carries it in a delimited position rather than
// loose in prose. Four spellings mean unimplemented, matched by **equality**
// against that position and nowhere else:
//
//   - NotImplemented, which Overcast answers with HTTP 501 and nothing else
//     (internal/protocol/errors.go), and UnsupportedOperation beside it.
//   - UnknownOperationException, which AWS — and Overcast — answer with HTTP
//     400 for a target naming no modeled operation, so a status alone would
//     miss it.
//   - "501" itself: botocore puts the HTTP status in the code position when it
//     cannot model the response body into an error code. That is a parsed
//     field of the response, not the digits appearing somewhere in a message.
func ClassifyBanner(text string) (unimplemented, decided bool) {
	codes := BannerCodes(text)
	if len(codes) == 0 {
		return false, false
	}
	for _, code := range codes {
		switch code {
		case "501", "NotImplemented", "UnknownOperationException", "UnsupportedOperation":
			return true, true
		}
	}
	return false, true
}

// BannerCodes returns every error code the AWS CLI's own banner states, in the
// order they were found, or nil when it states none. Retries print one banner
// each, so more than one is normal.
//
// This is the CLI's rendering of a modeled error, and the one place in its
// output where a code sits in a position rather than in prose. The scenario
// interpreter reads the same surface — see errorCodes in
// internal/scenario/executor.go, which adds the second surface (an error body
// the CLI echoed because it could not model it) that only error *matching*
// needs.
func BannerCodes(text string) []string {
	var codes []string
	for _, m := range errorBannerRe.FindAllStringSubmatch(text, -1) {
		codes = append(codes, m[1])
	}
	return codes
}

// errorBannerRe matches botocore's rendering of a service error:
// `An error occurred (<Code>) when calling the <Op> operation:`.
var errorBannerRe = regexp.MustCompile(`An error occurred \(([^()]+)\) when calling the `)

// LooksUnimplementedWithoutBanner is the substring heuristic, and it is for
// output that states **no error code** — the CLI died before it reached the
// wire, or echoed a body it could not model. Pass it what the CLI said and
// nothing else.
//
// It is never right for output that does state one: the banner names the error,
// and "501" appears in request ids, ARNs, resource names and port numbers.
// ClassifyBanner answers that case; this one only answers the case where there
// is no code to read.
func LooksUnimplementedWithoutBanner(s string) bool {
	return strings.Contains(s, "501") ||
		strings.Contains(s, "NotImplemented") ||
		strings.Contains(s, "UnknownOperationException") ||
		strings.Contains(s, "UnsupportedOperation") ||
		strings.Contains(s, "not implemented")
}

// ─── NDJSON events ────────────────────────────────────────────────────────────

// emitMu serialises writes to stdout so concurrent goroutines (parallel
// group execution) never interleave partial NDJSON lines.
var emitMu sync.Mutex

func emit(obj any) {
	b, _ := json.Marshal(obj)
	emitMu.Lock()
	os.Stdout.Write(b)
	os.Stdout.Write([]byte("\n"))
	emitMu.Unlock()
}

// GroupCounts holds the result totals from a single RunGroup call.
type GroupCounts struct {
	Passed, Failed, Skipped, Unimplemented, Cancelled int
}

// RunGroup executes a single TestGroup, emitting one test_result per test.
// It returns the aggregate counts for the caller to roll up into run_end.
func RunGroup(ctx context.Context, g TestGroup) GroupCounts {
	t := NewTestContext("", "", "")

	// Extract endpoint/region/runID from context values if present.
	if v, ok := ctx.Value(ctxEndpoint{}).(string); ok {
		t.Endpoint = v
	}
	if v, ok := ctx.Value(ctxRegion{}).(string); ok {
		t.Region = v
	}
	if v, ok := ctx.Value(ctxRunID{}).(string); ok {
		t.RunID = v
	}

	var counts GroupCounts

	if runSetup(ctx, g, t, &counts) {
		runTests(ctx, g, t, &counts)
	}

	// Teardown runs whether or not setup succeeded. A setup that failed halfway
	// has already created some of what it was going to create, and that is
	// exactly the run that leaks: the tests were all skipped, so nothing else
	// will ever delete it.
	if g.Teardown != nil {
		if err := g.Teardown(ctx, t); err != nil {
			emit(map[string]any{"event": "group_teardown_error", "suite": g.Suite, "group": g.Name, "error": err.Error()})
		}
	}
	return counts
}

// runSetup runs a group's setup, if it has one. It reports whether the tests
// should run: a setup failure emits one skip per test and returns false, and
// RunGroup still runs teardown — the IR's rule (compat/model/README.md § The
// scenario file).
func runSetup(ctx context.Context, g TestGroup, t *TestContext, counts *GroupCounts) bool {
	if g.Setup == nil {
		return true
	}
	err := g.Setup(ctx, t)
	if err == nil {
		return true
	}
	emit(map[string]any{"event": "group_setup_error", "suite": g.Suite, "group": g.Name, "error": err.Error()})
	for _, tc := range g.Tests {
		emit(map[string]any{
			"event": "test_result", "suite": g.Suite, "service": g.Service,
			"group": g.Name, "test": tc.Name, "status": "skip",
			"error": fmt.Sprintf("setup failed: %v", err), "duration_ms": 0,
		})
		counts.Skipped++
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
func runTests(ctx context.Context, g TestGroup, t *TestContext, counts *GroupCounts) {
	if g.Parallel && !hasDependencies(g.Tests) {
		runTestsConcurrently(ctx, g, t, counts)
		return
	}
	runTestsInOrder(ctx, g, t, counts)
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
func runTestsInOrder(ctx context.Context, g TestGroup, t *TestContext, counts *GroupCounts) {
	failedOrSkipped := map[string]bool{}

	for _, tc := range g.Tests {
		if ctx.Err() != nil {
			emit(cancelledEvent{Event: "cancelled", Suite: g.Suite, Group: g.Name, Test: tc.Name})
			counts.Cancelled++
			continue
		}
		// An na/skip marker outranks the dependency gate: a test the suite
		// never intended to run here reports why it was marked, not what
		// happened to something it does not depend on.
		res, done := marker(g, tc)
		if !done {
			// Dependency gate — skip if any declared dependency failed or was skipped.
			if gated, applies := dependencyGate(g, tc, failedOrSkipped); applies {
				res = gated
			} else {
				res = execute(ctx, g, t, tc)
			}
		}
		record(res, tc.Name, counts, failedOrSkipped)
		emit(res.event)
	}
}

// runTestsConcurrently runs the group's tests through a bounded worker pool and
// emits their results in declaration order once all of them are in.
//
// Emitting in order rather than as each finishes is what keeps this stream
// identical to the serial path's, test for test. The dashboard, the baseline
// and the flake detector all read it, and a result order that depended on
// which `aws` process answered first would be a new source of diff noise for
// no benefit.
func runTestsConcurrently(ctx context.Context, g TestGroup, t *TestContext, counts *GroupCounts) {
	results := make([]testResult, len(g.Tests))
	sem := make(chan struct{}, parallelSlots())
	var wg sync.WaitGroup
	for i, tc := range g.Tests {
		if ctx.Err() != nil {
			results[i] = testResult{
				event:  cancelledEvent{Event: "cancelled", Suite: g.Suite, Group: g.Name, Test: tc.Name},
				status: statusCancelled,
			}
			continue
		}
		wg.Add(1)
		go func(i int, tc TestCase) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			results[i] = runOne(ctx, g, t, tc)
		}(i, tc)
	}
	wg.Wait()

	// No dependency bookkeeping: this path is taken only when no test declares
	// one, so the set a serial run maintains would be read by nobody.
	for i, res := range results {
		record(res, g.Tests[i].Name, counts, nil)
		emit(res.event)
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

// resultEvent builds a test_result event. An empty message omits the "error"
// key entirely, which is what a pass and an unimplemented have always done.
func resultEvent(g TestGroup, name, status string, durMs int64, message string) map[string]any {
	ev := map[string]any{
		"event": "test_result", "suite": g.Suite, "service": g.Service,
		"group": g.Name, "test": name, "status": status, "duration_ms": durMs,
	}
	if message != "" {
		ev["error"] = message
	}
	return ev
}

// dependencyGate returns the skip result for a test whose declared
// dependencies did not all pass, and whether it applies. Consulted only after
// marker: a test that was never going to run has its own reason.
func dependencyGate(g TestGroup, tc TestCase, failedOrSkipped map[string]bool) (testResult, bool) {
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
	return testResult{event: resultEvent(g, tc.Name, statusSkip, 0, message), status: statusSkip}, true
}

// marker returns the result a test carries instead of running — na or skip —
// and whether it has one.
func marker(g TestGroup, tc TestCase) (testResult, bool) {
	if tc.NA != "" {
		return testResult{event: resultEvent(g, tc.Name, statusNA, 0, tc.NA), status: statusNA}, true
	}
	if tc.Skip != "" {
		return testResult{event: resultEvent(g, tc.Name, statusSkip, 0, tc.Skip), status: statusSkip}, true
	}
	return testResult{}, false
}

// runOne runs a single test, or reports its na/skip marker without running it.
// It touches no state shared with another test beyond the TestContext, whose
// bag is mutex guarded, so it is safe to call concurrently for the tests of one
// group — which is why the concurrent path calls this and the serial one calls
// marker and execute separately, with the dependency gate between them.
func runOne(ctx context.Context, g TestGroup, t *TestContext, tc TestCase) testResult {
	if res, done := marker(g, tc); done {
		return res
	}
	return execute(ctx, g, t, tc)
}

// execute runs the test function and classifies its outcome.
func execute(ctx context.Context, g TestGroup, t *TestContext, tc TestCase) testResult {
	start := time.Now()
	err := tc.Fn(ctx, t)
	durMs := time.Since(start).Milliseconds()

	switch {
	case err == nil:
		return testResult{event: resultEvent(g, tc.Name, statusPass, durMs, ""), status: statusPass}
	case IsUnimplemented(err):
		return testResult{event: resultEvent(g, tc.Name, statusUnimplemented, durMs, ""), status: statusUnimplemented}
	default:
		return testResult{event: resultEvent(g, tc.Name, statusFail, durMs, err.Error()), status: statusFail}
	}
}

// record folds one result into the group counters and, for a serial run, into
// the set the dependency gate reads. na is counted nowhere — it is excluded
// from pass-rate calculations, and it does not say a dependent's prerequisite
// is missing. failedOrSkipped may be nil when nothing will read it.
func record(res testResult, name string, counts *GroupCounts, failedOrSkipped map[string]bool) {
	switch res.status {
	case statusPass:
		counts.Passed++
		return
	case statusNA:
		return
	case statusCancelled:
		counts.Cancelled++
		return
	case statusSkip:
		counts.Skipped++
	case statusUnimplemented:
		counts.Unimplemented++
	case statusFail:
		counts.Failed++
	}
	if failedOrSkipped != nil {
		failedOrSkipped[name] = true
	}
}

// parallelSlots is how many things this suite may do at once — groups in
// RunSuite, and the tests of one parallel group in runTestsConcurrently.
// OVERCAST_COMPAT_PARALLEL_SLOTS is injected by the Go runner from the CPU
// count and the number of active suites; default 8.
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

// RunSuite executes all groups in parallel.
// RunGroup already creates its own TestContext per group, so no shared state
// is at risk from concurrent execution.
func RunSuite(suite string, groups []TestGroup, endpoint, region, runID string) {
	ctx := context.WithValue(context.Background(), ctxEndpoint{}, endpoint)
	ctx = context.WithValue(ctx, ctxRegion{}, region)
	ctx = context.WithValue(ctx, ctxRunID{}, runID)

	total := 0
	for _, g := range groups {
		total += len(g.Tests)
	}
	startedAt := time.Now()
	emit(map[string]any{
		"event": "run_start", "suite": suite, "run_id": runID,
		"total_tests": total, "endpoint": endpoint,
		"started_at": startedAt.UTC().Format(time.RFC3339),
	})

	// Limit concurrent group execution.
	sem := make(chan struct{}, parallelSlots())

	// Pre-allocate one slot per group so goroutines can write without locking.
	groupResults := make([]GroupCounts, len(groups))
	var wg sync.WaitGroup
	for i, g := range groups {
		wg.Add(1)
		go func(i int, g TestGroup) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			// Per-group timeout: prevents a single hung CLI call from
			// blocking this semaphore slot (and thus the whole suite) forever.
			groupCtx, groupCancel := context.WithTimeout(ctx, 5*time.Minute)
			defer groupCancel()
			groupResults[i] = RunGroup(groupCtx, g)
		}(i, g)
	}
	wg.Wait()

	var passed, failed, skipped, unimplemented int
	for _, c := range groupResults {
		passed += c.Passed
		failed += c.Failed
		skipped += c.Skipped
		unimplemented += c.Unimplemented
	}
	emit(map[string]any{
		"event": "run_end", "suite": suite, "run_id": runID,
		"passed": passed, "failed": failed, "skipped": skipped,
		"unimplemented": unimplemented,
		"duration_ms":   time.Since(startedAt).Milliseconds(),
	})
}

// context key types.
type ctxEndpoint struct{}
type ctxRegion struct{}
type ctxRunID struct{}

// NewRunContext returns a context with endpoint, region, and runID values
// that RunGroup will extract.
func NewRunContext(ctx context.Context, endpoint, region, runID string) context.Context {
	ctx = context.WithValue(ctx, ctxEndpoint{}, endpoint)
	ctx = context.WithValue(ctx, ctxRegion{}, region)
	ctx = context.WithValue(ctx, ctxRunID{}, runID)
	return ctx
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
func EmitBatchComplete(suite, batchID string, totals GroupCounts, durationMs int64) {
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
