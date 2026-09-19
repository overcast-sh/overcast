package stepfunctions

import (
	"context"
	"encoding/json"
	"math"
	"math/rand/v2"
	"strings"
	"sync"
	"time"
)

// Per-state-type execution, and the Retry/Catch machinery that wraps the three
// state types that can fail. The driver that sequences states and the ASL
// error model live in interpreter.go; how a state reads its input and shapes
// its output in each query language lives in dataflow.go.

// ─── Individual state types ───────────────────────────────────────────────────

// runFail raises the Fail state's error. Error and Cause are both optional in
// ASL; an absent Error leaves the error name empty, as on AWS — it must not
// turn into States.Runtime, which no Catch can handle.
func (in *interpreter) runFail(f *flow) *stateError {
	errName, serr := f.text(f.state.Error, f.state.ErrorPath, "Error")
	if serr != nil {
		return serr
	}
	cause, serr := f.text(f.state.Cause, f.state.CausePath, "Cause")
	if serr != nil {
		return serr
	}
	return &stateError{name: errName, cause: cause}
}

func (in *interpreter) runSucceed(f *flow) (any, string, bool, *stateError) {
	output, assigned, serr := f.finishPassthrough(f.state.Assign, f.state.Output)
	if serr != nil {
		return nil, "", false, serr
	}
	in.exitState(f, output, assigned)
	return output, "", true, nil
}

func (in *interpreter) runChoice(f *flow) (any, string, bool, *stateError) {
	for _, rule := range f.state.Choices {
		matched, serr := in.choiceMatches(f, rule)
		if serr != nil {
			return nil, "", false, serr
		}
		if matched {
			return in.leaveChoice(f, rule.Next, rule.Assign, rule.Output)
		}
	}
	if f.state.Default == "" {
		return nil, "", false, newStateError(errNoChoiceMatched, "no choice rule matched and the state has no Default transition")
	}
	return in.leaveChoice(f, f.state.Default, f.state.Assign, f.state.Output)
}

// choiceMatches evaluates one top-level Choice rule: a JSONPath comparison
// tree, or a JSONata Condition.
func (in *interpreter) choiceMatches(f *flow, rule *aslChoiceRule) (bool, *stateError) {
	if !f.jsonata {
		matched, err := evaluateChoiceRule(rule, f.effective, f.ctxObj)
		if err != nil {
			return false, newStateError(errRuntime, "%s", err.Error())
		}
		return matched, nil
	}
	expr, _ := isJSONataExpression(rule.Condition)
	value, defined, serr := in.evalJSONata(expr, f.scope("Condition"))
	if serr != nil {
		return false, serr
	}
	matched, ok := value.(bool)
	if !defined || !ok {
		return false, in.queryError(f.scope("Condition"), "a Choice Condition must evaluate to a boolean")
	}
	return matched, nil
}

func (in *interpreter) leaveChoice(f *flow, next string, assign, output json.RawMessage) (any, string, bool, *stateError) {
	out, assigned, serr := f.finishPassthrough(assign, output)
	if serr != nil {
		return nil, "", false, serr
	}
	in.exitState(f, out, assigned)
	return out, next, false, nil
}

func (in *interpreter) runWait(ctx context.Context, f *flow) (any, string, bool, *stateError) {
	now := in.handler.clk.Now()
	var (
		delay time.Duration
		park  *parkHandle
	)
	if cp := in.takeResumed(parkWait); cp != nil && cp.WaitUntil != nil {
		// Resumed after a restart: only what is left of the wait remains.
		delay = max(cp.WaitUntil.Sub(now), 0)
		park = in.resumePark(cp)
	} else {
		var serr *stateError
		if delay, serr = f.waitDuration(now); serr != nil {
			return nil, "", false, serr
		}
		if delay >= durableWaitThreshold {
			until := now.Add(delay)
			park = in.park(ctx, f, &executionCheckpoint{Kind: parkWait, WaitUntil: &until})
		}
	}
	elapsed := in.pause(ctx, delay)
	in.unpark(ctx, park, !elapsed)
	if !elapsed {
		return nil, "", false, in.unwindReason(ctx, f.name)
	}
	output, assigned, serr := f.finishPassthrough(f.state.Assign, f.state.Output)
	if serr != nil {
		return nil, "", false, serr
	}
	in.exitState(f, output, assigned)
	return output, f.state.Next, f.state.End, nil
}

// pause blocks for d on the injected clock, or until the context is done. It
// reports whether the full pause elapsed. The timer is always stopped, so a
// wait cut short leaves nothing pending.
func (in *interpreter) pause(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}
	timer := in.handler.clk.Timer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	}
}

func maxDuration(d, floor time.Duration) time.Duration {
	if d < floor {
		return floor
	}
	return d
}

// runPass produces its result without doing any work: Result, else
// Parameters, else the effective input (JSONPath); the input (JSONata), which
// Output then reshapes.
func (in *interpreter) runPass(f *flow) (any, string, bool, *stateError) {
	var (
		output   any
		assigned map[string]any
		serr     *stateError
	)
	if f.jsonata {
		output, assigned, serr = f.jsonataOutput(f.state.Output, f.state.Assign, f.scope("Output"), f.raw)
	} else {
		result, renderErr := f.arguments()
		if renderErr != nil {
			return nil, "", false, renderErr
		}
		if len(f.state.Result) > 0 {
			var decoded any
			if err := json.Unmarshal(f.state.Result, &decoded); err != nil {
				return nil, "", false, newStateError(errRuntime, "Result is not valid JSON: %v", err)
			}
			result = decoded
		}
		output, assigned, serr = f.finishResult(result)
	}
	if serr != nil {
		return nil, "", false, serr
	}
	in.exitState(f, output, assigned)
	return output, f.state.Next, f.state.End, nil
}

// exitState applies a state's variable assignments and records its
// `<Type>StateExited` event.
func (in *interpreter) exitState(f *flow, output any, assigned map[string]any) {
	if len(assigned) > 0 {
		in.vars.assign(assigned)
	}
	in.recordExit(f.name, f.state.Type, output, assigned)
}

// ─── Retry / Catch ────────────────────────────────────────────────────────────

// runRetryable executes a Task, Parallel or Map state under its Retry and
// Catch policies. States.Runtime is never retried or caught — AWS documents it
// that way, and it is also what keeps an Overcast "not supported" failure from
// being swallowed by a `Catch` on States.ALL. An unwind (stop, budget, failed
// sibling) is never retried or caught either.
func (in *interpreter) runRetryable(ctx context.Context, f *flow) (any, string, bool, *stateError) {
	state := f.state
	attempts := make([]int, len(state.Retry))
	retryCount := 0
	// A Task resumed after a restart carries on from the Retry position it
	// was parked at.
	if cp := in.parkResumed; cp != nil && len(cp.RetryAttempts) == len(attempts) {
		copy(attempts, cp.RetryAttempts)
		retryCount = cp.RetryCount
	}

	for {
		attempt := f.withContext(in.stateContext(f.name, in.handler.clk.Now(), retryCount))
		in.retryAttempts, in.retryCount = attempts, retryCount

		result, serr := in.runRetryableOnce(ctx, attempt)
		if serr == nil {
			// Shaping the result can fail too (a ResultPath that does not
			// match, a JSONata Output that errors); that is the state failing,
			// so Retry and Catch see it like any other error.
			var (
				output   any
				assigned map[string]any
			)
			output, assigned, serr = attempt.finishResult(result)
			if serr == nil {
				in.exitState(attempt, output, assigned)
				return output, state.Next, state.End, nil
			}
		}
		if serr.unwound || ctx.Err() != nil {
			if !serr.unwound {
				serr = in.unwindReason(ctx, f.name)
			}
			return nil, "", false, serr
		}
		if serr.name == errRuntime {
			return nil, "", false, serr
		}

		idx := matchRetrier(state.Retry, attempts, serr)
		if idx >= 0 && in.testState != nil && in.topLevel {
			// TestState runs one attempt and reports that a retrier would
			// have taken over.
			in.testState.status = testStateRetriable
			return nil, "", false, serr
		}
		if idx >= 0 {
			delay := retryDelay(state.Retry[idx], attempts[idx])
			attempts[idx]++
			retryCount++
			if !in.pause(ctx, delay) {
				return nil, "", false, in.unwindReason(ctx, f.name)
			}
			continue
		}

		catcher := matchCatcher(state.Catch, serr)
		if catcher == nil {
			return nil, "", false, serr
		}
		errorOutput := map[string]any{"Error": serr.name, "Cause": serr.cause}
		output, assigned, catchErr := attempt.finishCatch(catcher, errorOutput)
		if catchErr != nil {
			return nil, "", false, catchErr
		}
		if in.testState != nil && in.topLevel {
			in.testState.status = testStateCaughtError
		}
		in.exitState(attempt, output, assigned)
		return output, catcher.Next, false, nil
	}
}

// runRetryableOnce runs one attempt of a Task, Parallel or Map state.
func (in *interpreter) runRetryableOnce(ctx context.Context, f *flow) (any, *stateError) {
	switch f.state.Type {
	case stateTypeTask:
		return in.runTask(ctx, f)
	case stateTypeParallel:
		return in.runParallel(ctx, f)
	case stateTypeMap:
		return in.runMap(ctx, f)
	}
	return nil, unsupportedError("state type %q", f.state.Type)
}

// matchRetrier returns the index of the first retrier that matches the error
// and still has attempts left, or -1.
func matchRetrier(retriers []aslRetrier, attempts []int, serr *stateError) int {
	for i, retrier := range retriers {
		if !errorMatches(retrier.ErrorEquals, serr) {
			continue
		}
		if attempts[i] >= retrier.maxAttempts() {
			return -1
		}
		return i
	}
	return -1
}

func matchCatcher(catchers []aslCatcher, serr *stateError) *aslCatcher {
	for i := range catchers {
		if errorMatches(catchers[i].ErrorEquals, serr) {
			return &catchers[i]
		}
	}
	return nil
}

// wildcardErrorEquals are the two ErrorEquals entries AWS treats as wildcards
// rather than as literal error names.
//
// States.ALL is the language's own wildcard. States.TaskFailed is documented
// as one too — "when used in a retry or catch, States.TaskFailed acts as a
// wildcard that matches any known error name except for States.Runtime" — and
// it is the matcher real state machines reach for first, because it is what
// catches a Lambda function error whose name the workflow does not know in
// advance.
//
// Every other predefined name matches literally, with one documented
// exception: States.Timeout also matches States.HeartbeatTimeout, because a
// missed heartbeat is a kind of task timeout.
var wildcardErrorEquals = map[string]bool{errAll: true, errTaskFailed: true}

// errorMatches implements ASL's ErrorEquals matching. Neither wildcard reaches
// States.Runtime, which AWS documents as neither retriable nor catchable and
// which is also how an Overcast "not supported" failure stays loud; naming it
// explicitly still matches.
func errorMatches(errorEquals []string, serr *stateError) bool {
	for _, want := range errorEquals {
		if want == serr.name {
			return true
		}
		if want == errTimeout && serr.name == errHeartbeatTimeout {
			return true
		}
		if wildcardErrorEquals[want] && serr.name != errRuntime {
			return true
		}
	}
	return false
}

// retryJitter returns a multiplier in [0, 1) for JitterStrategy FULL. It is a
// variable so tests can pin it.
var retryJitter = rand.Float64

// retryDelay computes the backoff for the next attempt of a retrier:
// IntervalSeconds × BackoffRate^attempt, capped by MaxDelaySeconds, then —
// with JitterStrategy FULL — scaled by a random factor in [0, 1), which is
// AWS's "full jitter".
func retryDelay(retrier aslRetrier, priorAttempts int) time.Duration {
	seconds := retrier.interval() * math.Pow(retrier.backoffRate(), float64(priorAttempts))
	if retrier.MaxDelaySeconds != nil && *retrier.MaxDelaySeconds > 0 && seconds > *retrier.MaxDelaySeconds {
		seconds = *retrier.MaxDelaySeconds
	}
	if strings.EqualFold(retrier.JitterStrategy, "FULL") {
		seconds *= retryJitter()
	}
	if seconds <= 0 {
		return 0
	}
	return time.Duration(seconds * float64(time.Second))
}

// ─── Parallel ─────────────────────────────────────────────────────────────────

// runParallel runs every branch concurrently against the same input and
// returns their outputs as an array, in branch order.
//
// History follows AWS's causal linkage: each branch's first event links to
// ParallelStateStarted and later events chain through that branch only, so a
// reader can attribute every event to its branch by walking previousEventId.
//
// The first branch to fail fails the whole state with the branch's own error
// and cause — a Catch on the Parallel matches the name the branch raised — and
// the other branches are stopped, recording their `<Type>StateAborted` events.
//
// On the first attempt after a redrive only the branches that did not
// succeed run — each from the state it stopped in — and the others contribute
// the output they recorded, as on AWS; they record no events.
func (in *interpreter) runParallel(ctx context.Context, f *flow) (any, *stateError) {
	resume := in.takeResume()
	if resume != nil && len(resume.Branches) != len(f.state.Branches) {
		resume = nil
	}
	input, serr := f.arguments()
	if serr != nil {
		return nil, serr
	}
	startedID := in.record(HistoryEvent{Type: evtParallelStateStarted})

	branchCtx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)

	results := make([]any, len(f.state.Branches))
	outcomes := make([]redriveChild, len(f.state.Branches))
	var (
		mu       sync.Mutex
		firstErr *stateError
		wg       sync.WaitGroup
	)
	for i, branch := range f.state.Branches {
		if output, ok := resume.succeededOutput(i); ok {
			results[i], outcomes[i] = output, resume.Branches[i]
			continue
		}
		child := in.forkFor(f, startedID)
		start, branchInput, resumed := child.resumeAt(resume.childPoint(i))
		if !resumed {
			start, branchInput = branch.StartAt, cloneJSON(input)
		}
		wg.Add(1)
		go func(i int, branch *aslBranch) {
			defer wg.Done()
			output, serr := child.runChild(func() (any, *stateError) {
				return child.runBranchFrom(branchCtx, branch, start, branchInput)
			})
			outcomes[i] = childOutcome(child, output, serr)
			if serr != nil {
				mu.Lock()
				if firstErr == nil && !serr.unwound {
					firstErr = serr
					cancel(errSiblingFailed)
				}
				mu.Unlock()
				return
			}
			results[i] = output
		}(i, branch)
	}
	wg.Wait()
	in.rejoin()
	in.container = &redriveContainer{state: f.name, branches: outcomes}

	if ctx.Err() != nil {
		return nil, in.unwindReason(ctx, f.name)
	}
	if firstErr != nil {
		in.record(HistoryEvent{Type: evtParallelStateFailed})
		return nil, firstErr
	}
	in.record(HistoryEvent{Type: evtParallelStateSucceeded})
	return results, nil
}

// forkFor is fork for the children of the state f runs: they default to that
// state's query language and read its variables through a scope of their own.
func (in *interpreter) forkFor(f *flow, after int64) *interpreter {
	child := in.fork(after)
	if f.jsonata {
		child.queryLanguage = queryLanguageJSONata
	}
	return child
}

// runChild runs fn and turns a panic into a States.Runtime failure. The
// recovery middleware covers neither a request's own goroutines nor these, and
// a panic in one branch must not take the process down.
func (in *interpreter) runChild(fn func() (any, *stateError)) (output any, serr *stateError) {
	defer func() {
		if r := recover(); r != nil {
			output = nil
			serr = newStateError(errRuntime, "a Parallel branch or Map iteration panicked inside Overcast's interpreter: %v", r)
		}
	}()
	return fn()
}

// ─── Map ──────────────────────────────────────────────────────────────────────

// defaultInlineMapConcurrency is how many inline Map iterations run at once
// when MaxConcurrency is 0 (unbounded). AWS documents an inline Map as running
// up to 40 iterations concurrently.
const defaultInlineMapConcurrency = 40

// runMap runs a Map state: inline iteration here, distributed mode in
// distributed_map.go.
func (in *interpreter) runMap(ctx context.Context, f *flow) (any, *stateError) {
	state := f.state
	processor := state.processor()
	if mode := processorMode(processor); strings.EqualFold(mode, "DISTRIBUTED") {
		return in.runDistributedMap(ctx, f)
	} else if mode != "" && !strings.EqualFold(mode, "INLINE") {
		return nil, newStateError(errRuntime, "unknown Map ProcessorConfig.Mode %q", mode)
	}
	if len(state.ItemReader) > 0 || len(state.ItemBatcher) > 0 || len(state.ResultWriter) > 0 {
		return nil, newStateError(errRuntime, "ItemReader, ItemBatcher and ResultWriter are only valid on a distributed Map (state %q)", f.name)
	}

	items, serr := f.mapItems()
	if serr != nil {
		return nil, serr
	}
	concurrency, _, serr := f.nonNegativeInt(state.MaxConcurrency, state.MaxConcurrencyPath, "MaxConcurrency")
	if serr != nil {
		return nil, serr
	}
	if concurrency == 0 || concurrency > defaultInlineMapConcurrency {
		concurrency = defaultInlineMapConcurrency
	}

	resume := in.takeResume()
	if resume != nil && len(resume.Branches) != len(items) {
		resume = nil
	}

	startedID := in.record(HistoryEvent{
		Type:            evtMapStateStarted,
		MapStateStarted: &mapStateStartedDetails{Length: int64(len(items))},
	})

	results, outcomes, firstErr := in.iterate(ctx, f, items, concurrency, startedID, resume)
	in.rejoin()
	in.container = &redriveContainer{state: f.name, branches: outcomes}

	if ctx.Err() != nil {
		return nil, in.unwindReason(ctx, f.name)
	}
	if firstErr != nil {
		in.record(HistoryEvent{Type: evtMapStateFailed})
		return nil, firstErr
	}
	in.record(HistoryEvent{Type: evtMapStateSucceeded})
	return results, nil
}

// iterate runs the Map's processor over items, at most concurrency at a time,
// and returns the outputs in item order. The first iteration to fail stops
// the rest: iterations already running record MapIterationAborted and
// iterations not yet started never start.
//
// After a redrive (resume set) an iteration that succeeded contributes its
// recorded output without running or recording anything, and the others
// resume at the state they stopped in, or start afresh if they never entered
// one. It also returns each iteration's outcome, for the checkpoint.
func (in *interpreter) iterate(ctx context.Context, f *flow, items []any, concurrency int, startedID int64, resume *redrivePoint) ([]any, []redriveChild, *stateError) {
	iterCtx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)

	results := make([]any, len(items))
	outcomes := make([]redriveChild, len(items))
	var (
		mu       sync.Mutex
		firstErr *stateError
		wg       sync.WaitGroup
	)
	// Iterations that already succeeded are settled up front, so a failure
	// that stops the launches below cannot drop them from the checkpoint.
	done := make([]bool, len(items))
	for index := range items {
		if output, ok := resume.succeededOutput(index); ok {
			results[index], outcomes[index], done[index] = output, resume.Branches[index], true
		}
	}
	slots := make(chan struct{}, concurrency)
	for index, item := range items {
		if done[index] {
			continue
		}
		select {
		case slots <- struct{}{}:
		case <-iterCtx.Done():
		}
		if iterCtx.Err() != nil {
			break
		}
		child := in.forkFor(f, startedID)
		child.mapItem = map[string]any{"Index": float64(index), "Value": item}
		point := resume.childPoint(index)
		wg.Add(1)
		go func(index int, item any) {
			defer wg.Done()
			defer func() { <-slots }()
			output, serr := child.runChild(func() (any, *stateError) {
				return child.runIteration(iterCtx, f, index, item, point)
			})
			outcomes[index] = childOutcome(child, output, serr)
			if serr != nil {
				mu.Lock()
				if firstErr == nil && !serr.unwound {
					firstErr = serr
					cancel(errSiblingFailed)
				}
				mu.Unlock()
				return
			}
			results[index] = output
		}(index, item)
	}
	wg.Wait()
	return results, outcomes, firstErr
}

// runIteration runs one inline Map iteration on its own frame, recording the
// MapIteration* events that attribute everything inside it to its index. A
// redriven iteration (point set) resumes at the state it stopped in.
func (in *interpreter) runIteration(ctx context.Context, f *flow, index int, item any, point *redrivePoint) (any, *stateError) {
	details := func() *mapIterationDetails { return &mapIterationDetails{Name: f.name, Index: int64(index)} }
	in.record(HistoryEvent{Type: evtMapIterationStarted, MapIterationStarted: details()})

	processor := f.state.processor()
	start, input, resumed := in.resumeAt(point)
	var serr *stateError
	if !resumed {
		start = processor.StartAt
		input, serr = in.iterationInput(f, item)
	}
	if serr == nil {
		var output any
		output, serr = in.runBranchFrom(ctx, processor, start, input)
		if serr == nil {
			in.record(HistoryEvent{Type: evtMapIterationSucceeded, MapIterationSucceeded: details()})
			return output, nil
		}
	}
	if serr.unwound || ctx.Err() != nil {
		in.record(HistoryEvent{Type: evtMapIterationAborted, MapIterationAborted: details()})
		if !serr.unwound {
			serr = in.unwindReason(ctx, f.name)
		}
		return nil, serr
	}
	in.record(HistoryEvent{Type: evtMapIterationFailed, MapIterationFailed: details()})
	return nil, serr
}

// iterationInput builds one item's input: the item itself, or the
// ItemSelector (legacy: Parameters) evaluated with $$.Map.Item set. In an
// ItemSelector `$` (JSONPath) and $states.input (JSONata) are the Map state's
// own input, not the item. in must be the item's frame (mapItem set).
func (in *interpreter) iterationInput(f *flow, item any) (any, *stateError) {
	selector := mapItemSelector(f.state)
	if len(selector) == 0 {
		return cloneJSON(item), nil
	}
	scoped := f.withContext(in.stateContext(f.name, in.handler.clk.Now(), 0))
	scoped.in = in
	return scoped.render("ItemSelector", selector, f.effective, nil)
}

// mapItems selects the array a Map iterates: ItemsPath (default `$`) over the
// effective input in JSONPath, Items (default the input) in JSONata.
func (f *flow) mapItems() ([]any, *stateError) {
	var source any
	if f.jsonata {
		value, serr := f.render("Items", f.state.Items, nil, f.raw)
		if serr != nil {
			return nil, serr
		}
		source = value
	} else {
		source = f.effective
		if f.state.ItemsPath != "" {
			selected, err := selectPath(f.effective, f.ctxObj, f.state.ItemsPath)
			if err != nil {
				return nil, newStateError(errRuntime, "%s", err.Error())
			}
			source = selected
		}
	}
	items, ok := source.([]any)
	if !ok {
		if f.jsonata {
			return nil, f.in.queryError(f.scope("Items"), "the Map state's Items did not evaluate to an array")
		}
		path := f.state.ItemsPath
		if path == "" {
			path = "$"
		}
		return nil, newStateError(errRuntime, "the Map state's items (ItemsPath %q) are not an array", path)
	}
	return items, nil
}

// mapItemSelector returns the per-item payload template: ItemSelector, or the
// legacy Parameters spelling that pre-ItemProcessor Map states used.
func mapItemSelector(state *aslState) json.RawMessage {
	if len(state.ItemSelector) > 0 {
		return state.ItemSelector
	}
	if state.ItemProcessor == nil && len(state.Parameters) > 0 {
		return state.Parameters
	}
	return nil
}

// processorMode reads ProcessorConfig.Mode from a Map's ItemProcessor.
func processorMode(processor *aslBranch) string {
	return processorConfig(processor).Mode
}

// mapProcessorConfig is a Map ItemProcessor's ProcessorConfig.
type mapProcessorConfig struct {
	Mode          string `json:"Mode"`
	ExecutionType string `json:"ExecutionType"`
}

func processorConfig(processor *aslBranch) mapProcessorConfig {
	var config mapProcessorConfig
	if processor == nil || len(processor.ProcessorConfig) == 0 {
		return config
	}
	_ = json.Unmarshal(processor.ProcessorConfig, &config)
	return config
}
