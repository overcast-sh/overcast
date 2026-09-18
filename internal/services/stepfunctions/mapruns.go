package stepfunctions

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/overcast-sh/overcast/internal/protocol"
	"github.com/overcast-sh/overcast/internal/serviceutil"
)

// Map runs: the record a distributed Map keeps of one run over its items —
// status, the concurrency and failure tolerance it runs under, and counts of
// items and child executions by state. ListMapRuns, DescribeMapRun and
// UpdateMapRun read and adjust it; UpdateMapRun changes take effect on a run
// that is still in progress.

const mapRunPrefix = "maprun:"

// mapRunCounts is AWS's MapRunItemCounts / MapRunExecutionCounts.
type mapRunCounts struct {
	Pending               int64 `json:"pending" cbor:"pending"`
	Running               int64 `json:"running" cbor:"running"`
	Succeeded             int64 `json:"succeeded" cbor:"succeeded"`
	Failed                int64 `json:"failed" cbor:"failed"`
	TimedOut              int64 `json:"timedOut" cbor:"timedOut"`
	Aborted               int64 `json:"aborted" cbor:"aborted"`
	Total                 int64 `json:"total" cbor:"total"`
	ResultsWritten        int64 `json:"resultsWritten" cbor:"resultsWritten"`
	FailuresNotRedrivable int64 `json:"failuresNotRedrivable" cbor:"failuresNotRedrivable"`
	PendingRedrive        int64 `json:"pendingRedrive" cbor:"pendingRedrive"`
}

// MapRun is the persisted record of one distributed Map run.
type MapRun struct {
	MapRunArn                  string       `json:"MapRunArn"`
	ExecutionArn               string       `json:"ExecutionArn"`
	StateMachineArn            string       `json:"StateMachineArn"`
	Status                     string       `json:"Status"`
	StartDate                  time.Time    `json:"StartDate"`
	StopDate                   *time.Time   `json:"StopDate,omitempty"`
	MaxConcurrency             int          `json:"MaxConcurrency"`
	ToleratedFailurePercentage float64      `json:"ToleratedFailurePercentage"`
	ToleratedFailureCount      int64        `json:"ToleratedFailureCount"`
	ItemCounts                 mapRunCounts `json:"ItemCounts"`
	ExecutionCounts            mapRunCounts `json:"ExecutionCounts"`
}

// ─── Store ────────────────────────────────────────────────────────────────────

func (st *Store) putMapRun(ctx context.Context, run *MapRun) error {
	raw, err := json.Marshal(run)
	if err != nil {
		return fmt.Errorf("stepfunctions: marshal map run %q: %w", run.MapRunArn, err)
	}
	return st.s.Set(ctx, storeNS, serviceutil.RegionKey(st.region(ctx), mapRunPrefix+run.MapRunArn), string(raw))
}

func (st *Store) getMapRun(ctx context.Context, arn string) (*MapRun, error) {
	raw, found, err := st.s.Get(ctx, storeNS, serviceutil.RegionKey(st.region(ctx), mapRunPrefix+arn))
	if err != nil {
		return nil, fmt.Errorf("stepfunctions: get map run %q: %w", arn, err)
	}
	if !found {
		return nil, nil
	}
	var run MapRun
	if json.Unmarshal([]byte(raw), &run) != nil {
		return nil, nil
	}
	return &run, nil
}

// listMapRuns returns the map runs one execution started, oldest first.
// Undecodable records are skipped.
func (st *Store) listMapRuns(ctx context.Context, execARN string) ([]*MapRun, error) {
	pairs, err := st.s.Scan(ctx, storeNS, serviceutil.RegionKey(st.region(ctx), mapRunPrefix))
	if err != nil {
		return nil, fmt.Errorf("stepfunctions: scan map runs: %w", err)
	}
	var out []*MapRun
	for _, p := range pairs {
		var run MapRun
		if json.Unmarshal([]byte(p.Value), &run) != nil || run.ExecutionArn != execARN {
			continue
		}
		out = append(out, &run)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].StartDate.Before(out[j].StartDate) })
	return out, nil
}

// ─── ARNs ─────────────────────────────────────────────────────────────────────

// mapRunARN builds `arn:…:mapRun:<stateMachine>/<label>:<id>` for a run the
// execution execARN started.
func mapRunARN(execARN, label, id string) string {
	parts := strings.SplitN(execARN, ":", 8)
	// arn:aws:states:<region>:<account>:execution:<sm>:<name>
	if len(parts) < 7 {
		return "arn:aws:states:::mapRun:" + label + ":" + id
	}
	return strings.Join(parts[:5], ":") + ":mapRun:" + parts[6] + "/" + label + ":" + id
}

// mapRunChildARN is the execution ARN of one of a map run's children:
// `arn:…:execution:<stateMachine>/<label>:<name>`.
func mapRunChildARN(mapRunArn, name string) string {
	return mapRunExecutionPrefix(mapRunArn) + name
}

// mapRunExecutionPrefix is what every child execution ARN of a map run starts
// with. Runs sharing a label share it, so listings also filter on MapRunArn.
func mapRunExecutionPrefix(mapRunArn string) string {
	prefix := strings.Replace(mapRunArn, ":mapRun:", ":execution:", 1)
	if i := strings.LastIndex(prefix, ":"); i >= 0 {
		prefix = prefix[:i+1]
	}
	return prefix
}

// ─── Live runs ────────────────────────────────────────────────────────────────

// liveMapRun is a map run in progress. UpdateMapRun writes its limits and the
// running Map reads them before starting each child.
type liveMapRun struct {
	mu      sync.Mutex
	record  MapRun
	changed chan struct{}
	// countSet and percentageSet record which tolerances the Map declared (or
	// UpdateMapRun set); an unset one is not applied.
	countSet, percentageSet bool
}

func newLiveMapRun(record MapRun) *liveMapRun {
	return &liveMapRun{record: record, changed: make(chan struct{})}
}

// update applies fn under the lock and wakes anything waiting on a change.
func (l *liveMapRun) update(fn func(*MapRun)) MapRun {
	l.mu.Lock()
	defer l.mu.Unlock()
	fn(&l.record)
	close(l.changed)
	l.changed = make(chan struct{})
	return l.record
}

// snapshot returns the current record and a channel closed on its next change.
func (l *liveMapRun) snapshot() (MapRun, <-chan struct{}) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.record, l.changed
}

func (h *Handler) registerMapRun(run *liveMapRun) {
	h.mapRunsMu.Lock()
	defer h.mapRunsMu.Unlock()
	if h.mapRuns == nil {
		h.mapRuns = map[string]*liveMapRun{}
	}
	h.mapRuns[run.record.MapRunArn] = run
}

func (h *Handler) releaseMapRun(arn string) {
	h.mapRunsMu.Lock()
	defer h.mapRunsMu.Unlock()
	delete(h.mapRuns, arn)
}

func (h *Handler) liveMapRun(arn string) *liveMapRun {
	h.mapRunsMu.Lock()
	defer h.mapRunsMu.Unlock()
	return h.mapRuns[arn]
}

// getMapRun returns the current record of a map run, live or persisted.
func (h *Handler) getMapRun(ctx context.Context, arn string) (*MapRun, *protocol.AWSError) {
	if live := h.liveMapRun(arn); live != nil {
		record, _ := live.snapshot()
		return &record, nil
	}
	run, err := h.store.getMapRun(ctx, arn)
	if err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	if run == nil {
		return nil, &protocol.AWSError{
			Code:       "ResourceNotFound",
			Message:    fmt.Sprintf("Resource not found: '%s'", arn),
			HTTPStatus: http.StatusBadRequest,
		}
	}
	return run, nil
}

// ─── Operations ───────────────────────────────────────────────────────────────

type listMapRunsRequest struct {
	ExecutionArn string `json:"executionArn" cbor:"executionArn"`
	MaxResults   int    `json:"maxResults" cbor:"maxResults"`
	NextToken    string `json:"nextToken" cbor:"nextToken"`
}

type mapRunListItem struct {
	ExecutionArn    string  `json:"executionArn" cbor:"executionArn"`
	MapRunArn       string  `json:"mapRunArn" cbor:"mapRunArn"`
	StateMachineArn string  `json:"stateMachineArn" cbor:"stateMachineArn"`
	StartDate       float64 `json:"startDate" cbor:"startDate"`
	StopDate        float64 `json:"stopDate,omitempty" cbor:"stopDate,omitempty"`
}

type listMapRunsResponse struct {
	MapRuns   []mapRunListItem `json:"mapRuns" cbor:"mapRuns"`
	NextToken string           `json:"nextToken,omitempty" cbor:"nextToken,omitempty"`
}

func (h *Handler) listMapRunsTyped(ctx context.Context, req *listMapRunsRequest) (*listMapRunsResponse, *protocol.AWSError) {
	if _, aerr := h.getExecution(ctx, req.ExecutionArn); aerr != nil {
		return nil, aerr
	}
	runs, err := h.store.listMapRuns(ctx, req.ExecutionArn)
	if err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	page, pageErr := paginate(runs, req.MaxResults, req.NextToken)
	if pageErr != nil {
		return nil, pageErr
	}
	items := make([]mapRunListItem, 0, len(page.Items))
	for _, run := range page.Items {
		if current, aerr := h.getMapRun(ctx, run.MapRunArn); aerr == nil {
			run = current
		}
		item := mapRunListItem{
			ExecutionArn:    run.ExecutionArn,
			MapRunArn:       run.MapRunArn,
			StateMachineArn: run.StateMachineArn,
			StartDate:       epochSeconds(run.StartDate),
		}
		if run.StopDate != nil {
			item.StopDate = epochSeconds(*run.StopDate)
		}
		items = append(items, item)
	}
	return &listMapRunsResponse{MapRuns: items, NextToken: page.NextToken}, nil
}

type mapRunArnRequest struct {
	MapRunArn string `json:"mapRunArn" cbor:"mapRunArn"`
}

type describeMapRunResponse struct {
	MapRunArn                  string       `json:"mapRunArn" cbor:"mapRunArn"`
	ExecutionArn               string       `json:"executionArn" cbor:"executionArn"`
	Status                     string       `json:"status" cbor:"status"`
	StartDate                  float64      `json:"startDate" cbor:"startDate"`
	StopDate                   float64      `json:"stopDate,omitempty" cbor:"stopDate,omitempty"`
	MaxConcurrency             int          `json:"maxConcurrency" cbor:"maxConcurrency"`
	ToleratedFailurePercentage float64      `json:"toleratedFailurePercentage" cbor:"toleratedFailurePercentage"`
	ToleratedFailureCount      int64        `json:"toleratedFailureCount" cbor:"toleratedFailureCount"`
	ItemCounts                 mapRunCounts `json:"itemCounts" cbor:"itemCounts"`
	ExecutionCounts            mapRunCounts `json:"executionCounts" cbor:"executionCounts"`
	RedriveCount               int          `json:"redriveCount" cbor:"redriveCount"`
}

func (h *Handler) describeMapRunTyped(ctx context.Context, req *mapRunArnRequest) (*describeMapRunResponse, *protocol.AWSError) {
	run, aerr := h.getMapRun(ctx, req.MapRunArn)
	if aerr != nil {
		return nil, aerr
	}
	resp := &describeMapRunResponse{
		MapRunArn:                  run.MapRunArn,
		ExecutionArn:               run.ExecutionArn,
		Status:                     run.Status,
		StartDate:                  epochSeconds(run.StartDate),
		MaxConcurrency:             run.MaxConcurrency,
		ToleratedFailurePercentage: run.ToleratedFailurePercentage,
		ToleratedFailureCount:      run.ToleratedFailureCount,
		ItemCounts:                 run.ItemCounts,
		ExecutionCounts:            run.ExecutionCounts,
	}
	if run.StopDate != nil {
		resp.StopDate = epochSeconds(*run.StopDate)
	}
	return resp, nil
}

type updateMapRunRequest struct {
	MapRunArn                  string   `json:"mapRunArn" cbor:"mapRunArn"`
	MaxConcurrency             *int     `json:"maxConcurrency" cbor:"maxConcurrency"`
	ToleratedFailurePercentage *float64 `json:"toleratedFailurePercentage" cbor:"toleratedFailurePercentage"`
	ToleratedFailureCount      *int64   `json:"toleratedFailureCount" cbor:"toleratedFailureCount"`
}

func errInvalidMapRunUpdate(message string) *protocol.AWSError {
	return &protocol.AWSError{Code: "ValidationException", Message: message, HTTPStatus: http.StatusBadRequest}
}

// updateMapRunTyped implements UpdateMapRun. On a run still in progress the
// new limits apply to the children it has yet to start.
func (h *Handler) updateMapRunTyped(ctx context.Context, req *updateMapRunRequest) (*struct{}, *protocol.AWSError) {
	if req.MaxConcurrency == nil && req.ToleratedFailurePercentage == nil && req.ToleratedFailureCount == nil {
		return nil, errInvalidMapRunUpdate("at least one of maxConcurrency, toleratedFailurePercentage or toleratedFailureCount is required")
	}
	if req.MaxConcurrency != nil && *req.MaxConcurrency < 0 {
		return nil, errInvalidMapRunUpdate("maxConcurrency must not be negative")
	}
	if p := req.ToleratedFailurePercentage; p != nil && (*p < 0 || *p > 100) {
		return nil, errInvalidMapRunUpdate("toleratedFailurePercentage must be between 0 and 100")
	}
	if c := req.ToleratedFailureCount; c != nil && *c < 0 {
		return nil, errInvalidMapRunUpdate("toleratedFailureCount must not be negative")
	}
	apply := func(run *MapRun) {
		if req.MaxConcurrency != nil {
			run.MaxConcurrency = *req.MaxConcurrency
		}
		if req.ToleratedFailurePercentage != nil {
			run.ToleratedFailurePercentage = *req.ToleratedFailurePercentage
		}
		if req.ToleratedFailureCount != nil {
			run.ToleratedFailureCount = *req.ToleratedFailureCount
		}
	}
	if live := h.liveMapRun(req.MapRunArn); live != nil {
		live.mu.Lock()
		live.countSet = live.countSet || req.ToleratedFailureCount != nil
		live.percentageSet = live.percentageSet || req.ToleratedFailurePercentage != nil
		live.mu.Unlock()
		record := live.update(apply)
		if err := h.store.putMapRun(ctx, &record); err != nil {
			return nil, protocol.Wrap(protocol.ErrInternalError, err)
		}
		return &struct{}{}, nil
	}
	run, aerr := h.getMapRun(ctx, req.MapRunArn)
	if aerr != nil {
		return nil, aerr
	}
	apply(run)
	if err := h.store.putMapRun(ctx, run); err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	return &struct{}{}, nil
}
