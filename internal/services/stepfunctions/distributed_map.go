package stepfunctions

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

// Distributed Map: `ItemProcessor.ProcessorConfig.Mode: DISTRIBUTED`.
//
// Each item (or batch of items) runs as its own child execution with its own
// ARN and history, listed by ListExecutions with the run's mapRunArn, and the
// run as a whole is a map run that ListMapRuns / DescribeMapRun report and
// UpdateMapRun adjusts. The parent's history records MapRunStarted and
// MapRunSucceeded/MapRunFailed rather than the children's events — as on AWS,
// where a reader follows the mapRunArn to the children. Redriving the parent
// redrives the map run (redriveMapRun), which records MapRunRedriven instead.
//
// Items come from ItemsPath or from an ItemReader over S3 (a JSON, JSON Lines
// or CSV object, or a ListObjectsV2 listing); ItemBatcher groups them;
// ToleratedFailureCount/Percentage bound how many may fail; ResultWriter
// writes the results to S3 in AWS's manifest layout.

// maxDistributedConcurrency bounds a distributed Map whose MaxConcurrency is
// 0 (unbounded). AWS allows up to 10,000 child executions at once; a local
// emulator gains nothing from more goroutines than this.
const maxDistributedConcurrency = 1000

var errToleranceExceeded = errors.New("the Map Run's tolerated failure threshold was exceeded")

type itemReaderSpec struct {
	Resource     string          `json:"Resource"`
	Arguments    json.RawMessage `json:"Arguments"`
	ReaderConfig struct {
		InputType         string    `json:"InputType"`
		CSVHeaderLocation string    `json:"CSVHeaderLocation"`
		CSVHeaders        []string  `json:"CSVHeaders"`
		CSVDelimiter      string    `json:"CSVDelimiter"`
		MaxItems          aslNumber `json:"MaxItems"`
		MaxItemsPath      string    `json:"MaxItemsPath"`
		ItemsPointer      string    `json:"ItemsPointer"`
	} `json:"ReaderConfig"`
	Parameters json.RawMessage `json:"Parameters"`
}

type itemBatcherSpec struct {
	MaxItemsPerBatch          aslNumber       `json:"MaxItemsPerBatch"`
	MaxItemsPerBatchPath      string          `json:"MaxItemsPerBatchPath"`
	MaxInputBytesPerBatch     aslNumber       `json:"MaxInputBytesPerBatch"`
	MaxInputBytesPerBatchPath string          `json:"MaxInputBytesPerBatchPath"`
	BatchInput                json.RawMessage `json:"BatchInput"`
}

type resultWriterSpec struct {
	Resource     string          `json:"Resource"`
	Arguments    json.RawMessage `json:"Arguments"`
	Parameters   json.RawMessage `json:"Parameters"`
	WriterConfig struct {
		OutputType     string `json:"OutputType"`
		Transformation string `json:"Transformation"`
	} `json:"WriterConfig"`
}

// childResult is how one child execution ended.
type childResult struct {
	exec *Execution
	// output is the decoded output of a succeeded child.
	output any
	items  int
}

// childPlan is what a map run does with one input. A fresh run starts a new
// child for every input; a redrive keeps the children that succeeded (and
// those it cannot redrive), redrives or re-starts the rest, and starts the
// children that never ran.
type childPlan struct {
	// input is a new child's input.
	input any
	// existing is the child a redrive runs again: redriven from where it
	// stopped, or — restart set, for an EXPRESS child — started again from
	// the top under the same ARN.
	existing *Execution
	restart  bool
	// keep leaves the child's earlier outcome as it is.
	keep  bool
	items int
}

// runDistributedMap runs a Map in DISTRIBUTED mode. On the first attempt
// after a redrive whose checkpoint names the map run, the map run is
// redriven instead of a new one started.
func (in *interpreter) runDistributedMap(ctx context.Context, f *flow) (any, *stateError) {
	if resume := in.takeResume(); resume != nil && resume.MapRun != nil {
		if output, serr, redriven := in.redriveMapRun(ctx, f, resume.MapRun); redriven {
			return output, serr
		}
	}
	state := f.state
	items, serr := in.readItems(ctx, f)
	if serr != nil {
		return nil, serr
	}
	inputs, itemsPer, serr := in.childInputs(f, items)
	if serr != nil {
		return nil, serr
	}
	concurrency, _, serr := f.nonNegativeInt(state.MaxConcurrency, state.MaxConcurrencyPath, "MaxConcurrency")
	if serr != nil {
		return nil, serr
	}
	tolerance, serr := resolveTolerance(f)
	if serr != nil {
		return nil, serr
	}

	label := mapRunLabel(f)
	runID := uuid.NewString()
	record := MapRun{
		MapRunArn:                  mapRunARN(in.exec.ExecutionArn, label, runID),
		ExecutionArn:               in.exec.ExecutionArn,
		StateMachineArn:            in.sm.ARN + "/" + label,
		Status:                     statusRunning,
		StartDate:                  in.handler.clk.Now(),
		MaxConcurrency:             concurrency,
		ToleratedFailurePercentage: tolerance.percentage,
		ToleratedFailureCount:      tolerance.count,
		ItemCounts:                 mapRunCounts{Pending: int64(len(items)), Total: int64(len(items))},
		ExecutionCounts:            mapRunCounts{Pending: int64(len(inputs)), Total: int64(len(inputs))},
	}
	live := newLiveMapRun(record)
	live.countSet, live.percentageSet = tolerance.countSet, tolerance.percentageSet
	in.handler.registerMapRun(live)
	defer in.handler.releaseMapRun(record.MapRunArn)
	_ = in.handler.store.putMapRun(context.WithoutCancel(ctx), &record)

	in.record(HistoryEvent{
		Type:            evtMapStateStarted,
		MapStateStarted: &mapStateStartedDetails{Length: int64(len(items))},
	})
	in.record(HistoryEvent{Type: evtMapRunStarted, MapRunStarted: &mapRunStartedDetails{MapRunArn: record.MapRunArn}})

	plans := make([]childPlan, len(inputs))
	for i, input := range inputs {
		plans[i] = childPlan{input: input, items: itemsPer[i]}
	}
	return in.driveMapRun(ctx, f, live, runID, label, plans, make([]childResult, len(plans)))
}

// mapRunLabel is the Map's Label, or its state name.
func mapRunLabel(f *flow) string {
	if f.state.Label != "" {
		return f.state.Label
	}
	return f.name
}

// redriveMapRun redrives the map run a failed distributed Map left behind,
// as AWS does when its parent execution is redriven: the same map run (and
// ARN) runs again, its redrive count advanced and a MapRunRedriven event
// recorded where a fresh run records MapRunStarted. Children that succeeded
// keep their output; a STANDARD child that failed, timed out or was aborted
// is redriven under its own ARN, from the state it stopped in; an EXPRESS
// child is started again from the top under its old ARN; children that
// never started start with the input they would have had. A STANDARD child
// that can no longer be redriven keeps its failure and is counted in
// failuresNotRedrivable.
//
// redriven is false when the checkpoint cannot be followed — the map run
// record or a child is gone — and the caller then starts a new map run, which
// is also what AWS does for a map run that never started.
func (in *interpreter) redriveMapRun(ctx context.Context, f *flow, cp *redriveMapRun) (output any, serr *stateError, redriven bool) {
	h := in.handler
	persistCtx := context.WithoutCancel(ctx)
	record, err := h.store.getMapRun(persistCtx, cp.MapRunArn)
	if err != nil || record == nil || len(cp.Children) != len(cp.ItemsPer) {
		return nil, nil, false
	}
	if record.RedriveCount >= maxMapRunRedrives {
		return nil, newStateError(errRuntime, "the Map Run %s has been redriven %d times, the maximum", cp.MapRunArn, maxMapRunRedrives), true
	}
	express := strings.EqualFold(processorConfig(f.state.processor()).ExecutionType, "EXPRESS")

	now := h.clk.Now()
	record.Status = statusRunning
	record.StopDate = nil
	record.RedriveCount++
	record.RedriveDate = &now
	for _, counts := range []*mapRunCounts{&record.ExecutionCounts, &record.ItemCounts} {
		counts.PendingRedrive, counts.FailuresNotRedrivable, counts.ResultsWritten = 0, 0, 0
	}
	plans := make([]childPlan, len(cp.Children))
	results := make([]childResult, len(cp.Children))
	for i, arn := range cp.Children {
		items := cp.ItemsPer[i]
		plans[i].items = items
		if arn == "" {
			raw, ok := cp.Pending[i]
			if !ok {
				return nil, nil, false
			}
			input, err := decodeExecutionInput(raw)
			if err != nil {
				return nil, nil, false
			}
			plans[i].input = input
			continue
		}
		child, err := h.store.GetExecution(persistCtx, arn)
		if err != nil || child == nil {
			return nil, nil, false
		}
		switch {
		case child.Status == statusSucceeded:
			plans[i].keep = true
			results[i] = succeededChild(child, items)
			continue
		case express:
			plans[i].existing, plans[i].restart = child, true
		default:
			if status, _ := h.redriveStatus(persistCtx, child, nil); status != redriveStatusRedrivable {
				plans[i].keep = true
				results[i] = childResult{exec: child, items: items}
				record.ExecutionCounts.FailuresNotRedrivable++
				record.ItemCounts.FailuresNotRedrivable += int64(items)
				continue
			}
			plans[i].existing = child
		}
		// A child about to run again leaves its terminal count for
		// pendingRedrive until it is launched.
		*record.ExecutionCounts.bucket(child.Status)--
		*record.ItemCounts.bucket(child.Status) -= int64(items)
		record.ExecutionCounts.PendingRedrive++
		record.ItemCounts.PendingRedrive += int64(items)
	}

	tolerance, _ := resolveTolerance(f)
	live := newLiveMapRun(*record)
	live.countSet = tolerance.countSet || record.ToleratedFailureCount > 0
	live.percentageSet = tolerance.percentageSet || record.ToleratedFailurePercentage > 0
	h.registerMapRun(live)
	defer h.releaseMapRun(record.MapRunArn)
	_ = h.store.putMapRun(persistCtx, record)

	in.record(HistoryEvent{
		Type:            evtMapStateStarted,
		MapStateStarted: &mapStateStartedDetails{Length: record.ItemCounts.Total},
	})
	in.record(HistoryEvent{
		Type:           evtMapRunRedriven,
		MapRunRedriven: &mapRunRedrivenDetails{MapRunArn: record.MapRunArn, RedriveCount: record.RedriveCount},
	})
	runID := record.MapRunArn[strings.LastIndex(record.MapRunArn, ":")+1:]
	output, serr = in.driveMapRun(ctx, f, live, runID, mapRunLabel(f), plans, results)
	return output, serr, true
}

// succeededChild is the result of a child that already succeeded.
func succeededChild(exec *Execution, items int) childResult {
	result := childResult{exec: exec, items: items}
	if exec.Output != "" {
		_ = json.Unmarshal([]byte(exec.Output), &result.output)
	}
	return result
}

// bucket returns the count a child execution with this terminal status is
// counted in.
func (c *mapRunCounts) bucket(status string) *int64 {
	switch status {
	case statusSucceeded:
		return &c.Succeeded
	case statusTimedOut:
		return &c.TimedOut
	case statusAborted:
		return &c.Aborted
	default:
		return &c.Failed
	}
}

// driveMapRun runs a map run's children to the end, writes its results and
// records how it ended — the part a fresh run and a redrive share. It leaves
// the run's checkpoint on the frame, for a redrive should the Map fail.
func (in *interpreter) driveMapRun(ctx context.Context, f *flow, live *liveMapRun, runID, label string, plans []childPlan, results []childResult) (any, *stateError) {
	name, state := f.name, f.state
	persistCtx := context.WithoutCancel(ctx)

	exceeded := in.runChildren(ctx, live, state.processor(), label, plans, results, f.jsonata)

	final := live.update(func(run *MapRun) {
		stopped := in.handler.clk.Now()
		run.StopDate = &stopped
		switch {
		case ctx.Err() != nil:
			run.Status = statusAborted
		case exceeded:
			run.Status = statusFailed
		default:
			run.Status = statusSucceeded
		}
	})
	in.container = &redriveContainer{state: name, mapRun: mapRunCheckpoint(final.MapRunArn, plans, results)}

	var output any
	var writeErr *stateError
	if len(state.ResultWriter) > 0 {
		output, writeErr = in.writeResults(ctx, f, runID, &final, results)
		live.update(func(run *MapRun) { run.ItemCounts.ResultsWritten = final.ItemCounts.ResultsWritten })
		final.ItemCounts.ResultsWritten = live.recordCopy().ItemCounts.ResultsWritten
	} else {
		outputs := make([]any, len(results))
		for i, r := range results {
			outputs[i] = r.output
		}
		output = outputs
	}
	_ = in.handler.store.putMapRun(persistCtx, &final)

	if ctx.Err() != nil {
		return nil, in.unwindReason(ctx, name)
	}
	if exceeded || writeErr != nil {
		failure := newStateError(errExceedToleratedFailure, "The specified tolerated failure threshold was exceeded")
		if writeErr != nil && !exceeded {
			failure = writeErr
		}
		in.record(HistoryEvent{Type: evtMapRunFailed, MapRunFailed: &errorCauseDetails{Error: failure.name, Cause: failure.cause}})
		in.record(HistoryEvent{Type: evtMapStateFailed})
		return nil, failure
	}
	in.record(HistoryEvent{Type: evtMapRunSucceeded})
	in.record(HistoryEvent{Type: evtMapStateSucceeded})
	return output, nil
}

// mapRunCheckpoint records which child ran each input, and the inputs of the
// children that never started.
func mapRunCheckpoint(mapRunArn string, plans []childPlan, results []childResult) *redriveMapRun {
	cp := &redriveMapRun{MapRunArn: mapRunArn, Children: make([]string, len(plans)), ItemsPer: make([]int, len(plans))}
	for i, plan := range plans {
		cp.ItemsPer[i] = plan.items
		if exec := results[i].exec; exec != nil {
			cp.Children[i] = exec.ExecutionArn
			continue
		}
		encoded, err := encodeJSON(plan.input)
		if err != nil {
			continue
		}
		if cp.Pending == nil {
			cp.Pending = map[int]string{}
		}
		cp.Pending[i] = encoded
	}
	return cp
}

// recordCopy returns the current record.
func (l *liveMapRun) recordCopy() MapRun {
	record, _ := l.snapshot()
	return record
}

// runChildren runs the children plans call for under the run's live
// concurrency limit and failure tolerance, and waits for them all. results
// arrives holding the outcome of the children plans keep, and leaves holding
// every child's. It reports whether the tolerance was exceeded, which also
// stops the children still running and leaves the rest unstarted.
func (in *interpreter) runChildren(ctx context.Context, live *liveMapRun, processor *aslBranch, label string, plans []childPlan, results []childResult, jsonataMode bool) bool {
	childCtx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		exceeded bool
	)
	// Failures a redrive keeps can already be past the tolerance.
	if live.toleranceExceeded(live.recordCopy()) {
		exceeded = true
		cancel(errToleranceExceeded)
	}
launch:
	for i, plan := range plans {
		if plan.keep {
			continue
		}
		for {
			record, changed := live.snapshot()
			limit := record.MaxConcurrency
			if limit <= 0 || limit > maxDistributedConcurrency {
				limit = maxDistributedConcurrency
			}
			if record.ExecutionCounts.Running < int64(limit) {
				break
			}
			select {
			case <-changed:
			case <-childCtx.Done():
				break launch
			}
		}
		if childCtx.Err() != nil {
			break
		}
		n := int64(plan.items)
		live.update(func(run *MapRun) {
			if plan.existing != nil {
				run.ExecutionCounts.PendingRedrive--
				run.ItemCounts.PendingRedrive -= n
			} else {
				run.ExecutionCounts.Pending--
				run.ItemCounts.Pending -= n
			}
			run.ExecutionCounts.Running++
			run.ItemCounts.Running += n
		})
		wg.Add(1)
		go func(i int, plan childPlan) {
			defer wg.Done()
			result := in.runChildExecution(childCtx, processor, live.recordCopy().MapRunArn, label, plan, jsonataMode)
			result.items = plan.items
			results[i] = result
			record := live.update(func(run *MapRun) {
				run.ExecutionCounts.Running--
				run.ItemCounts.Running -= n
				*run.ExecutionCounts.bucket(result.exec.Status)++
				*run.ItemCounts.bucket(result.exec.Status) += n
			})
			if live.toleranceExceeded(record) {
				mu.Lock()
				exceeded = true
				mu.Unlock()
				cancel(errToleranceExceeded)
			}
		}(i, plan)
	}
	wg.Wait()
	// A child the redrive meant to run again but never launched keeps its
	// earlier outcome; pendingRedrive ends at zero, as on AWS.
	for i, plan := range plans {
		if plan.existing == nil || results[i].exec != nil {
			continue
		}
		results[i] = childResult{exec: plan.existing, items: plan.items}
		n := int64(plan.items)
		live.update(func(run *MapRun) {
			run.ExecutionCounts.PendingRedrive--
			run.ItemCounts.PendingRedrive -= n
			*run.ExecutionCounts.bucket(plan.existing.Status)++
			*run.ItemCounts.bucket(plan.existing.Status) += n
		})
	}
	return exceeded
}

// toleranceExceeded applies ToleratedFailureCount and ToleratedFailurePercentage
// to the run's current counts. With neither set no failure is tolerated; with
// either set the run fails as soon as either threshold is passed. Timed-out
// children count as failures.
func (l *liveMapRun) toleranceExceeded(record MapRun) bool {
	failed := record.ItemCounts.Failed + record.ItemCounts.TimedOut
	if failed == 0 {
		return false
	}
	if !l.countSet && !l.percentageSet {
		return true
	}
	if l.countSet && failed > record.ToleratedFailureCount {
		return true
	}
	if l.percentageSet && record.ItemCounts.Total > 0 &&
		float64(failed)*100/float64(record.ItemCounts.Total) > record.ToleratedFailurePercentage {
		return true
	}
	return false
}

// runChildExecution runs one child workflow execution of a map run to
// completion and persists it with its own history: a new child, or on a
// redrive an existing one — redriven (STANDARD), its history continuing after
// ExecutionRedriven, or started again from the top (EXPRESS) with a fresh
// history under the same ARN.
func (in *interpreter) runChildExecution(ctx context.Context, processor *aslBranch, mapRunArn, label string, plan childPlan, jsonataMode bool) childResult {
	h := in.handler
	persistCtx := context.WithoutCancel(ctx)
	childSM := *in.sm
	childSM.ARN = in.sm.ARN + "/" + label
	childSM.Name = in.sm.Name + "/" + label
	executionType := strings.ToUpper(processorConfig(processor).ExecutionType)
	if executionType == "" {
		executionType = "STANDARD"
	}

	var (
		exec *Execution
		run  *executionRun
	)
	switch {
	case plan.existing != nil && !plan.restart:
		redrive := *plan.existing
		exec = &redrive
		resumed, err := h.beginRedrive(persistCtx, exec)
		if err != nil {
			return childResult{exec: plan.existing}
		}
		run = resumed
	case plan.existing != nil:
		restart := *plan.existing
		exec = &restart
		exec.Status = statusRunning
		exec.StartDate = h.clk.Now()
		exec.StopDate = nil
		exec.Error, exec.Cause, exec.Output = "", "", ""
		exec.ExecutionType = executionType
		run = &executionRun{hist: newHistoryRecorder(maxHistoryEvents), restarted: true}
		run.hist.add(exec.StartDate, executionStartedEvent(&childSM, exec))
	default:
		name := uuid.NewString()
		inputJSON, _ := encodeJSON(plan.input)
		exec = &Execution{
			ExecutionArn:    mapRunChildARN(mapRunArn, name),
			StateMachineArn: childSM.ARN,
			Name:            name,
			Input:           inputJSON,
			Status:          statusRunning,
			StartDate:       h.clk.Now(),
			MapRunArn:       mapRunArn,
			ExecutionType:   executionType,
		}
		run = &executionRun{hist: newHistoryRecorder(maxHistoryEvents)}
		run.hist.add(exec.StartDate, executionStartedEvent(&childSM, exec))
	}
	_ = h.store.PutExecution(persistCtx, exec)

	runCtx, cancel := context.WithCancel(persistCtx)
	defer cancel()
	run.cancel = cancel
	if jsonataMode {
		run.queryLanguage = queryLanguageJSONata
	}
	// The parent stopping, timing out or exceeding its failure tolerance
	// aborts the child, exactly as a StopExecution on the child would.
	stopWatch := context.AfterFunc(ctx, func() {
		run.stop(h.clk.Now(), "States.Aborted", "the Map Run was stopped")
	})
	defer stopWatch()

	var outcome executionOutcome
	if h.reserveRun(exec.ExecutionArn, run, false) {
		outcome = h.runExecution(runCtx, &childSM, exec, processor, in.region, in.depth, run)
		defer h.releaseRun(exec.ExecutionArn)
	} else {
		serr := &stateError{name: errRuntime, cause: "Overcast Step Functions is shutting down", aborted: true}
		cursor := run.hist.lastID()
		outcome = (&interpreter{handler: h, hist: run.hist, cursor: &cursor}).finish(statusAborted, "", serr)
	}
	_ = h.persistOutcome(persistCtx, exec, run, outcome)

	result := childResult{exec: exec}
	if exec.Status == statusSucceeded && exec.Output != "" {
		_ = json.Unmarshal([]byte(exec.Output), &result.output)
	}
	return result
}

// ─── Items ────────────────────────────────────────────────────────────────────

// readItems returns the items a distributed Map runs over: from its
// ItemReader when it has one, otherwise from ItemsPath.
func (in *interpreter) readItems(ctx context.Context, f *flow) ([]any, *stateError) {
	state := f.state
	if len(state.ItemReader) == 0 {
		return f.mapItems()
	}
	var spec itemReaderSpec
	if err := json.Unmarshal(state.ItemReader, &spec); err != nil {
		return nil, newStateError(errRuntime, "ItemReader is malformed: %v", err)
	}
	params, serr := f.specArguments("ItemReader", spec.Parameters, spec.Arguments)
	if serr != nil {
		return nil, serr
	}
	bucket, _ := params["Bucket"].(string)
	readerFailed := func(serr *stateError) *stateError {
		return newStateError(errItemReaderFailed, "the ItemReader could not read s3://%s: %s: %s", bucket, serr.name, serr.cause)
	}

	var items []any
	switch {
	case strings.HasSuffix(spec.Resource, ":s3:listObjectsV2"):
		prefix, _ := params["Prefix"].(string)
		objects, serr := in.s3ListAll(ctx, bucket, prefix)
		if serr != nil {
			return nil, readerFailed(serr)
		}
		for _, obj := range objects {
			items = append(items, obj.toJSON())
		}
	case strings.HasSuffix(spec.Resource, ":s3:getObject"):
		switch strings.ToUpper(spec.ReaderConfig.InputType) {
		case "", "JSON", "JSONL", "CSV":
		default:
			return nil, unsupportedError("the ItemReader InputType %q — Overcast reads JSON, JSONL and CSV objects", spec.ReaderConfig.InputType)
		}
		key, _ := params["Key"].(string)
		body, _, serr := in.s3GetObject(ctx, bucket, key)
		if serr != nil {
			return nil, readerFailed(serr)
		}
		parsed, err := parseItemObject(body, &spec)
		if err != nil {
			return nil, newStateError(errItemReaderFailed, "the ItemReader could not parse s3://%s/%s: %v", bucket, key, err)
		}
		items = parsed
	default:
		return nil, unsupportedError("the ItemReader Resource %q — Overcast reads items with arn:aws:states:::s3:getObject and arn:aws:states:::s3:listObjectsV2", spec.Resource)
	}

	limit, _, serr := f.nonNegativeInt(spec.ReaderConfig.MaxItems, spec.ReaderConfig.MaxItemsPath, "MaxItems")
	if serr != nil {
		return nil, serr
	}
	if limit > 0 && len(items) > limit {
		items = items[:limit]
	}
	if items == nil {
		items = []any{}
	}
	return items, nil
}

// csvDelimiters maps ReaderConfig.CSVDelimiter to its character.
var csvDelimiters = map[string]rune{"": ',', "COMMA": ',', "PIPE": '|', "SEMICOLON": ';', "SPACE": ' ', "TAB": '\t'}

// parseItemObject parses an S3 object into items per ReaderConfig.InputType.
func parseItemObject(body []byte, spec *itemReaderSpec) ([]any, error) {
	switch strings.ToUpper(spec.ReaderConfig.InputType) {
	case "", "JSON":
		var doc any
		if err := json.Unmarshal(body, &doc); err != nil {
			return nil, err
		}
		if pointer := spec.ReaderConfig.ItemsPointer; pointer != "" {
			resolved, err := resolveJSONPointer(doc, pointer)
			if err != nil {
				return nil, err
			}
			doc = resolved
		}
		items, ok := doc.([]any)
		if !ok {
			return nil, errors.New("the object is not a JSON array")
		}
		return items, nil
	case "JSONL":
		var items []any
		for _, line := range bytes.Split(body, []byte("\n")) {
			line = bytes.TrimSpace(line)
			if len(line) == 0 {
				continue
			}
			var item any
			if err := json.Unmarshal(line, &item); err != nil {
				return nil, fmt.Errorf("a JSON Lines record is not valid JSON: %w", err)
			}
			items = append(items, item)
		}
		return items, nil
	}
	return parseCSVItems(body, spec)
}

// parseCSVItems turns CSV rows into objects keyed by the header row (or
// CSVHeaders when CSVHeaderLocation is GIVEN). Every value is a string, as
// on AWS.
func parseCSVItems(body []byte, spec *itemReaderSpec) ([]any, error) {
	delimiter, ok := csvDelimiters[strings.ToUpper(spec.ReaderConfig.CSVDelimiter)]
	if !ok {
		return nil, fmt.Errorf("CSVDelimiter %q is not one of COMMA, PIPE, SEMICOLON, SPACE, TAB", spec.ReaderConfig.CSVDelimiter)
	}
	reader := csv.NewReader(bytes.NewReader(body))
	reader.Comma = delimiter
	reader.FieldsPerRecord = -1
	rows, err := reader.ReadAll()
	if err != nil {
		return nil, err
	}
	headers := spec.ReaderConfig.CSVHeaders
	if !strings.EqualFold(spec.ReaderConfig.CSVHeaderLocation, "GIVEN") {
		if len(rows) == 0 {
			return []any{}, nil
		}
		headers, rows = rows[0], rows[1:]
	}
	items := make([]any, 0, len(rows))
	for _, row := range rows {
		item := make(map[string]any, len(headers))
		for i, header := range headers {
			if i < len(row) {
				item[header] = row[i]
			} else {
				item[header] = ""
			}
		}
		items = append(items, item)
	}
	return items, nil
}

// resolveJSONPointer resolves an RFC 6901 pointer such as /data/items.
func resolveJSONPointer(doc any, pointer string) (any, error) {
	if pointer == "" || pointer == "/" {
		return doc, nil
	}
	current := doc
	for _, token := range strings.Split(strings.TrimPrefix(pointer, "/"), "/") {
		token = strings.ReplaceAll(strings.ReplaceAll(token, "~1", "/"), "~0", "~")
		obj, ok := current.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("ItemsPointer %q does not resolve", pointer)
		}
		if current, ok = obj[token]; !ok {
			return nil, fmt.Errorf("ItemsPointer %q does not resolve", pointer)
		}
	}
	return current, nil
}

// childInputs applies ItemSelector to every item and ItemBatcher to the
// result, returning each child execution's input and how many items it holds.
func (in *interpreter) childInputs(f *flow, items []any) ([]any, []int, *stateError) {
	state := f.state
	selected := make([]any, len(items))
	for i, item := range items {
		frame := *in
		frame.mapItem = map[string]any{"Index": float64(i), "Value": item}
		value, serr := frame.iterationInput(f, item)
		if serr != nil {
			return nil, nil, serr
		}
		selected[i] = value
	}
	if len(state.ItemBatcher) == 0 {
		counts := make([]int, len(selected))
		for i := range counts {
			counts[i] = 1
		}
		return selected, counts, nil
	}

	var spec itemBatcherSpec
	if err := json.Unmarshal(state.ItemBatcher, &spec); err != nil {
		return nil, nil, newStateError(errRuntime, "ItemBatcher is malformed: %v", err)
	}
	maxItems, _, serr := f.nonNegativeInt(spec.MaxItemsPerBatch, spec.MaxItemsPerBatchPath, "MaxItemsPerBatch")
	if serr != nil {
		return nil, nil, serr
	}
	maxBytes, _, serr := f.nonNegativeInt(spec.MaxInputBytesPerBatch, spec.MaxInputBytesPerBatchPath, "MaxInputBytesPerBatch")
	if serr != nil {
		return nil, nil, serr
	}
	if maxItems <= 0 && maxBytes <= 0 {
		return nil, nil, newStateError(errRuntime, "ItemBatcher needs MaxItemsPerBatch or MaxInputBytesPerBatch")
	}
	batchInput, serr := f.render("BatchInput", spec.BatchInput, f.effective, nil)
	if serr != nil {
		return nil, nil, serr
	}

	var (
		inputs []any
		counts []int
		batch  []any
		size   int
	)
	flush := func() {
		if len(batch) == 0 {
			return
		}
		input := map[string]any{"Items": batch}
		if batchInput != nil {
			input["BatchInput"] = batchInput
		}
		inputs = append(inputs, input)
		counts = append(counts, len(batch))
		batch, size = nil, 0
	}
	for _, item := range selected {
		encoded, _ := encodeJSON(item)
		if len(batch) > 0 && ((maxItems > 0 && len(batch) >= maxItems) || (maxBytes > 0 && size+len(encoded) > maxBytes)) {
			flush()
		}
		batch = append(batch, item)
		size += len(encoded)
	}
	flush()
	return inputs, counts, nil
}

// mapTolerance is a Map's resolved failure tolerance.
type mapTolerance struct {
	count         int64
	countSet      bool
	percentage    float64
	percentageSet bool
}

func resolveTolerance(f *flow) (mapTolerance, *stateError) {
	var t mapTolerance
	state := f.state
	count, countSet, serr := f.nonNegativeInt(state.ToleratedFailureCount, state.ToleratedFailureCountPath, "ToleratedFailureCount")
	if serr != nil {
		return t, serr
	}
	t.count, t.countSet = int64(count), countSet
	percentage, percentageSet, serr := f.number(state.ToleratedFailurePercentage, state.ToleratedFailurePercentagePath, "ToleratedFailurePercentage")
	if serr != nil {
		return t, serr
	}
	if percentageSet && (percentage < 0 || percentage > 100) {
		return t, newStateError(errRuntime, "ToleratedFailurePercentage must be between 0 and 100")
	}
	t.percentage, t.percentageSet = percentage, percentageSet
	return t, nil
}

// specArguments evaluates an ItemReader's or ResultWriter's Parameters
// (JSONPath) or Arguments (JSONata) into an object.
func (f *flow) specArguments(field string, parameters, arguments json.RawMessage) (map[string]any, *stateError) {
	tmpl := parameters
	if f.jsonata {
		tmpl = arguments
	}
	rendered, serr := f.render(field, tmpl, f.effective, map[string]any{})
	if serr != nil {
		return nil, serr
	}
	params, _ := rendered.(map[string]any)
	if params == nil {
		params = map[string]any{}
	}
	return params, nil
}

// ─── ResultWriter ─────────────────────────────────────────────────────────────

// writeResults writes a map run's results where its ResultWriter says. With
// an S3 destination the results land in AWS's layout — SUCCEEDED_0.json,
// FAILED_0.json and PENDING_0.json beside a manifest.json, under
// <Prefix>/<map run id>/ — and the state's result points at the manifest.
// WriterConfig.Transformation COMPACT keeps only each child's output and
// FLATTEN also concatenates outputs that are arrays; with no Resource the
// transformed results are the state's result directly.
func (in *interpreter) writeResults(ctx context.Context, f *flow, runID string, run *MapRun, results []childResult) (any, *stateError) {
	state := f.state
	var spec resultWriterSpec
	if err := json.Unmarshal(state.ResultWriter, &spec); err != nil {
		return nil, newStateError(errRuntime, "ResultWriter is malformed: %v", err)
	}
	transformation := strings.ToUpper(spec.WriterConfig.Transformation)
	if transformation == "" {
		transformation = "NONE"
	}
	succeeded, failed, pending := []any{}, []any{}, []any{}
	for _, r := range results {
		if r.exec == nil {
			pending = append(pending, map[string]any{})
			continue
		}
		var entry any
		switch transformation {
		case "COMPACT", "FLATTEN":
			entry = r.output
		default:
			entry = childResultEntry(r.exec)
		}
		if r.exec.Status == statusSucceeded {
			succeeded = append(succeeded, entry)
		} else {
			failed = append(failed, childResultEntry(r.exec))
		}
	}
	if transformation == "FLATTEN" {
		var flat []any
		for _, out := range succeeded {
			if arr, ok := out.([]any); ok {
				flat = append(flat, arr...)
			} else {
				flat = append(flat, out)
			}
		}
		succeeded = flat
	}
	run.ItemCounts.ResultsWritten = int64(len(succeeded) + len(failed))

	if spec.Resource == "" {
		if transformation == "NONE" {
			return nil, newStateError(errRuntime, "a ResultWriter without a Resource needs a WriterConfig Transformation of COMPACT or FLATTEN")
		}
		return succeeded, nil
	}
	if !strings.HasSuffix(spec.Resource, ":s3:putObject") {
		return nil, unsupportedError("the ResultWriter Resource %q — Overcast writes results with arn:aws:states:::s3:putObject", spec.Resource)
	}
	params, serr := f.specArguments("ResultWriter", spec.Parameters, spec.Arguments)
	if serr != nil {
		return nil, serr
	}
	bucket, _ := params["Bucket"].(string)
	prefix, _ := params["Prefix"].(string)
	base := runID + "/"
	if prefix != "" {
		base = strings.TrimSuffix(prefix, "/") + "/" + base
	}
	jsonl := strings.EqualFold(spec.WriterConfig.OutputType, "JSONL")

	files := map[string][]any{}
	write := func(category string, entries []any) *stateError {
		if len(entries) == 0 {
			files[category] = []any{}
			return nil
		}
		body := encodeResultFile(entries, jsonl)
		key := base + category + "_0.json"
		if _, serr := in.s3PutObject(ctx, bucket, key, body, "application/json"); serr != nil {
			return newStateError(errResultWriterFailed, "the ResultWriter could not write s3://%s/%s: %s: %s", bucket, key, serr.name, serr.cause)
		}
		files[category] = []any{map[string]any{"Key": key, "Size": float64(len(body))}}
		return nil
	}
	for _, category := range []struct {
		name    string
		entries []any
	}{{"SUCCEEDED", succeeded}, {"FAILED", failed}, {"PENDING", pending}} {
		if serr := write(category.name, category.entries); serr != nil {
			return nil, serr
		}
	}
	manifest := map[string]any{
		"DestinationBucket": bucket,
		"MapRunArn":         run.MapRunArn,
		"ResultFiles":       files,
	}
	manifestJSON, _ := json.Marshal(manifest)
	manifestKey := base + "manifest.json"
	if _, serr := in.s3PutObject(ctx, bucket, manifestKey, manifestJSON, "application/json"); serr != nil {
		return nil, newStateError(errResultWriterFailed, "the ResultWriter could not write s3://%s/%s: %s: %s", bucket, manifestKey, serr.name, serr.cause)
	}
	return map[string]any{
		"MapRunArn":           run.MapRunArn,
		"ResultWriterDetails": map[string]any{"Bucket": bucket, "Key": manifestKey},
	}, nil
}

func encodeResultFile(entries []any, jsonl bool) []byte {
	if !jsonl {
		encoded, _ := json.Marshal(entries)
		return encoded
	}
	var buf bytes.Buffer
	for _, entry := range entries {
		line, _ := json.Marshal(entry)
		buf.Write(line)
		buf.WriteByte('\n')
	}
	return buf.Bytes()
}

// childResultEntry is one child execution as a ResultWriter file lists it.
func childResultEntry(exec *Execution) map[string]any {
	entry := map[string]any{
		"ExecutionArn":    exec.ExecutionArn,
		"Name":            exec.Name,
		"StateMachineArn": exec.StateMachineArn,
		"Status":          exec.Status,
		"Input":           exec.Input,
		"InputDetails":    map[string]any{"Included": true},
		"StartDate":       exec.StartDate.UTC().Format(time.RFC3339Nano),
		"RedriveCount":    float64(exec.RedriveCount),
		"RedriveStatus":   childRedriveStatus(exec),
	}
	if exec.RedriveDate != nil {
		entry["RedriveDate"] = exec.RedriveDate.UTC().Format(time.RFC3339Nano)
	}
	if exec.StopDate != nil {
		entry["StopDate"] = exec.StopDate.UTC().Format(time.RFC3339Nano)
	}
	if exec.Output != "" {
		entry["Output"] = exec.Output
		entry["OutputDetails"] = map[string]any{"Included": true}
	}
	if exec.Error != "" || exec.Status != statusSucceeded {
		entry["Error"] = exec.Error
		entry["Cause"] = exec.Cause
	}
	return entry
}

// childRedriveStatus is how a ResultWriter file reports whether a child can
// be re-run by redriving its map run.
func childRedriveStatus(exec *Execution) string {
	switch {
	case exec.Status == statusSucceeded || exec.Status == statusRunning:
		return redriveStatusNotRedrivable
	case strings.EqualFold(exec.ExecutionType, "EXPRESS"):
		return redriveStatusByMapRun
	}
	return redriveStatusRedrivable
}
