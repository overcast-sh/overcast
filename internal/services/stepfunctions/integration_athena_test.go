package stepfunctions

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/overcast-sh/overcast/internal/clock"
	"github.com/overcast-sh/overcast/internal/state"
)

// fakeAthena stands in for Athena behind the router a Task dispatches
// through. Each GetQueryExecution answers with the next of states, repeating
// the last one; every request is recorded.
type fakeAthena struct {
	failWith string // an Athena error code every call answers with; set before use

	mu      sync.Mutex
	states  []string
	calls   []athenaCall
	stopped chan struct{}
}

type athenaCall struct {
	target string
	body   map[string]any
}

func newFakeAthena(states ...string) *fakeAthena {
	return &fakeAthena{states: states, stopped: make(chan struct{}, 1)}
}

func (f *fakeAthena) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	var body map[string]any
	_ = json.Unmarshal(raw, &body)
	target := r.Header.Get("X-Amz-Target")
	f.mu.Lock()
	f.calls = append(f.calls, athenaCall{target: target, body: body})
	f.mu.Unlock()

	w.Header().Set("Content-Type", "application/x-amz-json-1.1")
	if f.failWith != "" {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"__type":"`+f.failWith+`","Message":"no such workgroup"}`)
		return
	}
	switch target {
	case "AmazonAthena.StartQueryExecution":
		_, _ = io.WriteString(w, `{"QueryExecutionId":"q-1"}`)
	case "AmazonAthena.GetQueryExecution":
		queryState := f.nextState()
		_ = json.NewEncoder(w).Encode(map[string]any{"QueryExecution": map[string]any{
			"QueryExecutionId": body["QueryExecutionId"],
			"Query":            "SELECT 1",
			"Status":           map[string]any{"State": queryState, "StateChangeReason": "reason for " + queryState},
		}})
	case "AmazonAthena.StopQueryExecution":
		_, _ = io.WriteString(w, `{}`)
		select {
		case f.stopped <- struct{}{}:
		default:
		}
	case "AmazonAthena.GetQueryResults":
		_, _ = io.WriteString(w, `{"ResultSet":{"Rows":[{"Data":[{"VarCharValue":"1"}]}]},"UpdateCount":0}`)
	default:
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"__type":"UnknownOperationException"}`)
	}
}

func (f *fakeAthena) nextState() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	next := f.states[0]
	if len(f.states) > 1 {
		f.states = f.states[1:]
	}
	return next
}

// targets lists the operations called, in order.
func (f *fakeAthena) targets() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.calls))
	for i, c := range f.calls {
		out[i] = strings.TrimPrefix(c.target, "AmazonAthena.")
	}
	return out
}

// lastBody is the request body of the most recent call to operation.
func (f *fakeAthena) lastBody(operation string) map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := len(f.calls) - 1; i >= 0; i-- {
		if f.calls[i].target == "AmazonAthena."+operation {
			return f.calls[i].body
		}
	}
	return nil
}

// armingClock is a mock clock that reports each timer it arms, so a test can
// advance it in lockstep with a `.sync` Task's polls: the only timers these
// executions arm are the waits between them.
type armingClock struct {
	*clock.Mock
	armed chan time.Duration
}

func (c *armingClock) Timer(d time.Duration) *clock.Timer {
	t := c.Mock.Timer(d)
	c.armed <- d
	return t
}

// newAthenaTestHandler builds a Handler whose Tasks reach athena, with one
// state machine running the single Task state task.
func newAthenaTestHandler(t *testing.T, athena *fakeAthena, task string) (*Handler, *armingClock, string) {
	t.Helper()
	clk := &armingClock{Mock: clock.NewMock(), armed: make(chan time.Duration)}
	h := newTestHandler(state.NewMemoryStore(), clk)
	h.router = athena
	resp, aerr := h.createStateMachineTyped(context.Background(), &createStateMachineRequest{
		Name: "athena", Definition: `{"StartAt":"Q","States":{"Q":` + task + `}}`, RoleArn: "arn:aws:iam::000000000000:role/r",
	})
	if aerr != nil {
		t.Fatalf("createStateMachineTyped: %+v", aerr)
	}
	return h, clk, resp.StateMachineArn
}

// athenaTask is a Task state on resource with parameters.
func athenaTask(resource, parameters string) string {
	return `{"Type":"Task","Resource":"arn:aws:states:::athena:` + resource + `","Parameters":` + parameters + `,"End":true}`
}

// runAthenaExecution runs the state machine to its end, advancing the clock
// through each wait between polls as the Task arms it.
func runAthenaExecution(t *testing.T, h *Handler, clk *armingClock, smARN string) *Execution {
	t.Helper()
	finished := make(chan *Execution, 1)
	go func() {
		exec, aerr := h.startExecution(context.Background(), smARN, "run", `{}`, 0, executionSync)
		if aerr != nil {
			t.Errorf("startExecution: %+v", aerr)
		}
		finished <- exec
	}()
	for {
		select {
		case exec := <-finished:
			if exec == nil {
				t.FailNow()
			}
			return exec
		case d := <-clk.armed:
			clk.Add(d)
		case <-time.After(10 * time.Second):
			t.Fatal("the execution did not finish")
		}
	}
}

func decodeOutput(t *testing.T, exec *Execution) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal([]byte(exec.Output), &out); err != nil {
		t.Fatalf("output %q: %v", exec.Output, err)
	}
	return out
}

// historyTypes lists an execution's history event types, comma-separated.
func historyTypes(t *testing.T, h *Handler, execARN string) string {
	t.Helper()
	resp, aerr := h.getExecutionHistoryTyped(context.Background(), &getExecutionHistoryRequest{ExecutionArn: execARN})
	if aerr != nil {
		t.Fatalf("getExecutionHistoryTyped: %+v", aerr)
	}
	types := make([]string, len(resp.Events))
	for i, e := range resp.Events {
		types[i] = e.Type
	}
	return strings.Join(types, ",")
}

func TestAthenaStartQueryExecution_requestResponse(t *testing.T) {
	// Given: a request-response startQueryExecution Task
	athena := newFakeAthena("RUNNING")
	h, clk, smARN := newAthenaTestHandler(t, athena, athenaTask("startQueryExecution",
		`{"QueryString":"SELECT 1","WorkGroup":"primary","ResultConfiguration":{"OutputLocation":"s3://results/"}}`))

	// When: it runs
	exec := runAthenaExecution(t, h, clk, smARN)

	// Then: it starts the query with the Parameters as given, waits for
	// nothing, and returns the StartQueryExecution response
	if exec.Status != statusSucceeded {
		t.Fatalf("status = %s (%s: %s)", exec.Status, exec.Error, exec.Cause)
	}
	if got := athena.targets(); len(got) != 1 || got[0] != "StartQueryExecution" {
		t.Fatalf("calls = %v, want only StartQueryExecution", got)
	}
	if body := athena.lastBody("StartQueryExecution"); body["QueryString"] != "SELECT 1" || body["WorkGroup"] != "primary" {
		t.Errorf("request = %v, want the Parameters forwarded", body)
	}
	if out := decodeOutput(t, exec); out["QueryExecutionId"] != "q-1" || len(out) != 1 {
		t.Errorf("output = %s, want {\"QueryExecutionId\":\"q-1\"}", exec.Output)
	}
}

func TestAthenaStartQueryExecution_syncWaitsForSuccess(t *testing.T) {
	// Given: a .sync Task on a query that runs for two polls
	athena := newFakeAthena("QUEUED", "RUNNING", "SUCCEEDED")
	h, clk, smARN := newAthenaTestHandler(t, athena, athenaTask("startQueryExecution.sync", `{"QueryString":"SELECT 1"}`))

	// When: it runs
	exec := runAthenaExecution(t, h, clk, smARN)

	// Then: the start is recorded as TaskSubmitted, and the Task's result is
	// the final GetQueryExecution response
	if exec.Status != statusSucceeded {
		t.Fatalf("status = %s (%s: %s)", exec.Status, exec.Error, exec.Cause)
	}
	want := "StartQueryExecution,GetQueryExecution,GetQueryExecution,GetQueryExecution"
	if got := strings.Join(athena.targets(), ","); got != want {
		t.Errorf("calls = %s, want %s", got, want)
	}
	if types := historyTypes(t, h, exec.ExecutionArn); !strings.Contains(types, "TaskStarted,TaskSubmitted,TaskSucceeded") {
		t.Errorf("history = %s, want TaskStarted, TaskSubmitted, TaskSucceeded", types)
	}
	qe, _ := decodeOutput(t, exec)["QueryExecution"].(map[string]any)
	status, _ := qe["Status"].(map[string]any)
	if qe["QueryExecutionId"] != "q-1" || status["State"] != "SUCCEEDED" {
		t.Errorf("output = %s, want the SUCCEEDED QueryExecution", exec.Output)
	}
}

func TestAthenaStartQueryExecution_syncQueryEndsUnsuccessfully(t *testing.T) {
	for _, final := range []string{"FAILED", "CANCELLED"} {
		t.Run(final, func(t *testing.T) {
			// Given: a .sync Task on a query that ends final
			athena := newFakeAthena("RUNNING", final)
			h, clk, smARN := newAthenaTestHandler(t, athena, athenaTask("startQueryExecution.sync", `{"QueryString":"SELECT 1"}`))

			// When: it runs
			exec := runAthenaExecution(t, h, clk, smARN)

			// Then: the Task fails with States.TaskFailed, its cause the
			// GetQueryExecution response
			if exec.Status != statusFailed || exec.Error != errTaskFailed {
				t.Fatalf("status = %s, error = %q, want FAILED with %s", exec.Status, exec.Error, errTaskFailed)
			}
			var cause struct {
				QueryExecution struct {
					QueryExecutionId string
					Status           struct{ State, StateChangeReason string }
				}
			}
			if err := json.Unmarshal([]byte(exec.Cause), &cause); err != nil {
				t.Fatalf("cause %q is not the QueryExecution JSON: %v", exec.Cause, err)
			}
			if cause.QueryExecution.Status.State != final || cause.QueryExecution.Status.StateChangeReason != "reason for "+final {
				t.Errorf("cause = %s, want the %s QueryExecution", exec.Cause, final)
			}
		})
	}
}

func TestAthenaStartQueryExecution_syncUnrecognizedState(t *testing.T) {
	// Given: a .sync Task on a query reporting a state Athena does not model
	athena := newFakeAthena("BOGUS")
	h, clk, smARN := newAthenaTestHandler(t, athena, athenaTask("startQueryExecution.sync", `{"QueryString":"SELECT 1"}`))

	// When: it runs
	exec := runAthenaExecution(t, h, clk, smARN)

	// Then: it fails loudly at once rather than polling forever
	if exec.Error != errRuntime || !strings.Contains(exec.Cause, `"BOGUS"`) {
		t.Errorf("error = %q (%s), want %s naming the state", exec.Error, exec.Cause, errRuntime)
	}
}

func TestAthenaStartQueryExecution_stoppedExecutionStopsTheQuery(t *testing.T) {
	// Given: a .sync Task waiting between polls on a query that never finishes
	athena := newFakeAthena("RUNNING")
	h, clk, smARN := newAthenaTestHandler(t, athena, athenaTask("startQueryExecution.sync", `{"QueryString":"SELECT 1"}`))
	ctx := context.Background()
	exec, aerr := h.startExecution(ctx, smARN, "run", `{}`, 0, executionAsync)
	if aerr != nil {
		t.Fatalf("startExecution: %+v", aerr)
	}
	<-clk.armed

	// When: the execution is stopped
	if _, aerr := h.stopExecutionTyped(ctx, &stopExecutionRequest{ExecutionArn: exec.ExecutionArn}); aerr != nil {
		t.Fatalf("stopExecutionTyped: %+v", aerr)
	}

	// Then: the query it was waiting on is stopped too
	select {
	case <-athena.stopped:
	case <-time.After(10 * time.Second):
		t.Fatalf("StopQueryExecution was never called; calls = %v", athena.targets())
	}
	if body := athena.lastBody("StopQueryExecution"); body["QueryExecutionId"] != "q-1" {
		t.Errorf("StopQueryExecution request = %v, want QueryExecutionId q-1", body)
	}
}

func TestAthenaStartQueryExecution_syncTaskTimeoutStopsTheQuery(t *testing.T) {
	// Given: a .sync Task with a one-second budget on a query that never
	// finishes, on a clock nobody advances. A Task deadline is wall-clock,
	// so this test takes a second.
	athena := newFakeAthena("RUNNING")
	h, clk, smARN := newAthenaTestHandler(t, athena,
		`{"Type":"Task","Resource":"arn:aws:states:::athena:startQueryExecution.sync","TimeoutSeconds":1,
		  "Parameters":{"QueryString":"SELECT 1"},"End":true}`)
	go func() {
		for range clk.armed { // accept each wait, never end it
		}
	}()

	// When: it runs
	exec, aerr := h.startExecution(context.Background(), smARN, "run", `{}`, 0, executionSync)
	if aerr != nil {
		t.Fatalf("startExecution: %+v", aerr)
	}

	// Then: the Task times out with States.Timeout, and its query is stopped
	if exec.Error != errTimeout {
		t.Errorf("error = %q (%s), want %s", exec.Error, exec.Cause, errTimeout)
	}
	if types := historyTypes(t, h, exec.ExecutionArn); !strings.Contains(types, "TaskTimedOut") {
		t.Errorf("history = %s, want a TaskTimedOut event", types)
	}
	select {
	case <-athena.stopped:
	default:
		t.Errorf("StopQueryExecution was never called; calls = %v", athena.targets())
	}
}

func TestAthenaIntegrations_requestResponse(t *testing.T) {
	cases := []struct {
		action, target, wantKey string
	}{
		{"getQueryExecution", "GetQueryExecution", "QueryExecution"},
		{"getQueryResults", "GetQueryResults", "ResultSet"},
		{"stopQueryExecution", "StopQueryExecution", ""},
	}
	for _, tc := range cases {
		t.Run(tc.action, func(t *testing.T) {
			// Given: a Task on the optimized integration
			athena := newFakeAthena("SUCCEEDED")
			h, clk, smARN := newAthenaTestHandler(t, athena, athenaTask(tc.action, `{"QueryExecutionId":"q-1"}`))

			// When: it runs
			exec := runAthenaExecution(t, h, clk, smARN)

			// Then: it makes that one call and returns its response
			if exec.Status != statusSucceeded {
				t.Fatalf("status = %s (%s: %s)", exec.Status, exec.Error, exec.Cause)
			}
			if got := athena.targets(); len(got) != 1 || got[0] != tc.target {
				t.Errorf("calls = %v, want only %s", got, tc.target)
			}
			out := decodeOutput(t, exec)
			if tc.wantKey == "" && len(out) != 0 || tc.wantKey != "" && out[tc.wantKey] == nil {
				t.Errorf("output = %s, want the %s response", exec.Output, tc.target)
			}
		})
	}
}

func TestAthenaIntegrations_apiErrorIsPrefixed(t *testing.T) {
	// Given: Athena refuses the call
	athena := newFakeAthena("SUCCEEDED")
	athena.failWith = "InvalidRequestException"
	h, clk, smARN := newAthenaTestHandler(t, athena,
		athenaTask("startQueryExecution.sync", `{"QueryString":"SELECT 1","WorkGroup":"missing"}`))

	// When: it runs
	exec := runAthenaExecution(t, h, clk, smARN)

	// Then: the Task fails with the Athena-prefixed error name AWS uses
	if exec.Error != "Athena.InvalidRequestException" || exec.Cause != "no such workgroup" {
		t.Errorf("error = %q (%q), want Athena.InvalidRequestException", exec.Error, exec.Cause)
	}
}

func TestAthenaIntegrations_unofferedResourceIsNotRecognized(t *testing.T) {
	for _, resource := range []string{
		"arn:aws:states:::athena:getNamedQuery",
		"arn:aws:states:::athena:getQueryResults.sync",
		"arn:aws:states:::athena:startQueryExecution.waitForTaskToken",
	} {
		t.Run(resource, func(t *testing.T) {
			// Given: an athena: Resource AWS does not offer
			h := newTestHandler(state.NewMemoryStore(), clock.NewMock())
			def := `{"StartAt":"Q","States":{"Q":{"Type":"Task","Resource":"` + resource + `","End":true}}}`

			// When: a state machine is created with it
			_, aerr := h.createStateMachineTyped(context.Background(), &createStateMachineRequest{
				Name: "athena", Definition: def, RoleArn: "arn:aws:iam::000000000000:role/r",
			})

			// Then: CreateStateMachine refuses it, as AWS does
			if aerr == nil || aerr.Code != "InvalidDefinition" || !strings.Contains(aerr.Message, "is not recognized") {
				t.Errorf("error = %+v, want InvalidDefinition naming an unrecognized resource", aerr)
			}
		})
	}
}
