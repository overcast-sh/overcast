// Package stepfunctions_test — state-type semantics and the history shape a
// flow diagram is drawn from: error propagation out of Parallel and Map,
// concurrency, causal previousEventId linkage and iteration attribution.
//
// Run: go test ./tests/integration/stepfunctions/...
package stepfunctions_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/overcast-sh/overcast/tests/helpers"
)

// rawEvent is a history event decoded generically, so a test can assert on any
// detail block without widening the shared historyEvent struct.
type rawEvent map[string]any

func (e rawEvent) typ() string { s, _ := e["type"].(string); return s }
func (e rawEvent) id() int64   { f, _ := e["id"].(float64); return int64(f) }
func (e rawEvent) prev() int64 { f, _ := e["previousEventId"].(float64); return int64(f) }

// detail returns the named details block, or nil.
func (e rawEvent) detail(key string) map[string]any {
	d, _ := e[key].(map[string]any)
	return d
}

// stateName returns the state a StateEntered/StateExited event names.
func (e rawEvent) stateName() string {
	for _, key := range []string{"stateEnteredEventDetails", "stateExitedEventDetails"} {
		if d := e.detail(key); d != nil {
			s, _ := d["name"].(string)
			return s
		}
	}
	return ""
}

// rawHistory returns an execution's whole history, following nextToken:
// GetExecutionHistory pages at 100 events by default, and a history that
// grows past that — a redriven one, or one with a busy branch — would
// otherwise be silently cut off at the page boundary.
func rawHistory(t *testing.T, srv *helpers.TestServer, execARN string) []rawEvent {
	t.Helper()
	return historyPages[rawEvent](t, srv, execARN)
}

// historyPages reads every page of an execution's history.
func historyPages[E any](t *testing.T, srv *helpers.TestServer, execARN string) []E {
	t.Helper()
	var events []E
	token := ""
	for {
		body := map[string]any{"executionArn": execARN, "maxResults": 1000}
		if token != "" {
			body["nextToken"] = token
		}
		resp := sfnCall(t, srv, "GetExecutionHistory", body)
		helpers.AssertStatus(t, resp, http.StatusOK)
		var out struct {
			Events    []E    `json:"events"`
			NextToken string `json:"nextToken"`
		}
		helpers.DecodeJSON(t, resp, &out) // closes the body
		events = append(events, out.Events...)
		if out.NextToken == "" {
			return events
		}
		token = out.NextToken
	}
}

func findEvents(events []rawEvent, typ string) []rawEvent {
	var out []rawEvent
	for _, e := range events {
		if e.typ() == typ {
			out = append(out, e)
		}
	}
	return out
}

func runToEnd(t *testing.T, srv *helpers.TestServer, name, def, input string) (describeExecutionResult, string) {
	t.Helper()
	smARN := createSM(t, srv, name, def)
	execARN := startExec(t, srv, smARN, input)
	return waitForTerminal(t, srv, execARN), execARN
}

// ─── Error propagation ────────────────────────────────────────────────────────

func TestStartExecution_parallelBranchErrorPropagatesUnchanged(t *testing.T) {
	// Given: a Parallel whose branch fails with a custom error, caught by name
	srv := helpers.NewTestServer(t)
	def := `{
	  "StartAt": "P",
	  "States": {
	    "P": {
	      "Type": "Parallel",
	      "Branches": [
	        {"StartAt": "Ok", "States": {"Ok": {"Type": "Pass", "End": true}}},
	        {"StartAt": "Boom", "States": {"Boom": {"Type": "Fail", "Error": "Custom.Error", "Cause": "branch said no"}}}
	      ],
	      "Catch": [{"ErrorEquals": ["Custom.Error"], "ResultPath": "$.err", "Next": "Caught"}],
	      "End": true
	    },
	    "Caught": {"Type": "Pass", "End": true}
	  }
	}`

	// When: it runs
	got, _ := runToEnd(t, srv, "par-err", def, `{}`)

	// Then: the Catch saw the branch's own error name and cause
	if got.Status != "SUCCEEDED" {
		t.Fatalf("status = %q (error=%q cause=%q)", got.Status, got.Error, got.Cause)
	}
	var out struct {
		Err struct{ Error, Cause string } `json:"err"`
	}
	if err := json.Unmarshal([]byte(got.Output), &out); err != nil {
		t.Fatalf("output %q: %v", got.Output, err)
	}
	if out.Err.Error != "Custom.Error" || out.Err.Cause != "branch said no" {
		t.Errorf("caught = %+v, want Custom.Error / branch said no", out.Err)
	}
}

func TestStartExecution_mapIterationErrorPropagatesUnchanged(t *testing.T) {
	// Given: a Map whose second iteration fails with a custom error and no Catch
	srv := helpers.NewTestServer(t)
	def := `{
	  "StartAt": "M",
	  "States": {
	    "M": {
	      "Type": "Map",
	      "ItemProcessor": {
	        "StartAt": "Check",
	        "States": {
	          "Check": {"Type": "Choice", "Choices": [{"Variable": "$", "NumericEquals": 2, "Next": "Bad"}], "Default": "Good"},
	          "Bad": {"Type": "Fail", "Error": "Item.Bad", "Cause": "two is bad"},
	          "Good": {"Type": "Pass", "End": true}
	        }
	      },
	      "End": true
	    }
	  }
	}`

	// When: it runs over [1,2,3]
	got, _ := runToEnd(t, srv, "map-err", def, `[1,2,3]`)

	// Then: the execution fails with the iteration's own error
	if got.Status != "FAILED" {
		t.Fatalf("status = %q, want FAILED", got.Status)
	}
	if got.Error != "Item.Bad" || got.Cause != "two is bad" {
		t.Errorf("error/cause = %q/%q, want Item.Bad/two is bad", got.Error, got.Cause)
	}
}

func TestStartExecution_failStateWithoutErrorIsCatchableByStatesAll(t *testing.T) {
	// Given: a Parallel branch ending in a Fail state with no Error field
	srv := helpers.NewTestServer(t)
	def := `{
	  "StartAt": "P",
	  "States": {
	    "P": {
	      "Type": "Parallel",
	      "Branches": [{"StartAt": "F", "States": {"F": {"Type": "Fail"}}}],
	      "Catch": [{"ErrorEquals": ["States.ALL"], "Next": "Caught"}],
	      "End": true
	    },
	    "Caught": {"Type": "Succeed"}
	  }
	}`

	// When: it runs
	got, _ := runToEnd(t, srv, "fail-noerr", def, `{}`)

	// Then: States.ALL caught it — an absent Error is not States.Runtime
	if got.Status != "SUCCEEDED" {
		t.Fatalf("status = %q (error=%q cause=%q), want SUCCEEDED", got.Status, got.Error, got.Cause)
	}
}

// ─── Choice ───────────────────────────────────────────────────────────────────

func TestStartExecution_choiceTypeMismatchEvaluatesFalse(t *testing.T) {
	// Given: a StringEquals rule applied to a number
	srv := helpers.NewTestServer(t)
	def := `{
	  "StartAt": "C",
	  "States": {
	    "C": {"Type": "Choice", "Choices": [{"Variable": "$.v", "StringEquals": "1", "Next": "Str"}], "Default": "Other"},
	    "Str": {"Type": "Pass", "Result": "str", "End": true},
	    "Other": {"Type": "Pass", "Result": "other", "End": true}
	  }
	}`

	// When: the variable is a number
	got, _ := runToEnd(t, srv, "choice-mismatch", def, `{"v":1}`)

	// Then: the rule does not match (the spec says false, not an error)
	if got.Status != "SUCCEEDED" || got.Output != `"other"` {
		t.Fatalf("status=%q output=%q error=%q, want SUCCEEDED \"other\"", got.Status, got.Output, got.Error)
	}
}

// ─── Validation ───────────────────────────────────────────────────────────────

func TestCreateStateMachine_duplicateStateNameAcrossBranchesRejected(t *testing.T) {
	// Given: two Parallel branches that both declare a state named Work
	srv := helpers.NewTestServer(t)
	def := `{
	  "StartAt": "P",
	  "States": {
	    "P": {
	      "Type": "Parallel",
	      "Branches": [
	        {"StartAt": "Work", "States": {"Work": {"Type": "Pass", "End": true}}},
	        {"StartAt": "Work", "States": {"Work": {"Type": "Pass", "End": true}}}
	      ],
	      "End": true
	    }
	  }
	}`

	// When: we create it
	resp := sfnCall(t, srv, "CreateStateMachine", map[string]any{
		"name": "dup-names", "definition": def, "roleArn": "arn:aws:iam::000000000000:role/r",
	})
	defer resp.Body.Close()

	// Then: AWS's InvalidDefinition
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	body := helpers.ReadBody(t, resp)
	if !strings.Contains(body, "InvalidDefinition") || !strings.Contains(body, "Work") {
		t.Errorf("body = %s, want InvalidDefinition naming Work", body)
	}
}

func TestCreateStateMachine_statesAllNotLastRetrierRejected(t *testing.T) {
	// Given: a Retry whose States.ALL retrier is not the last one
	srv := helpers.NewTestServer(t)
	def := `{"StartAt":"T","States":{"T":{"Type":"Task","Resource":"arn:aws:lambda:us-east-1:000000000000:function:f",
	  "Retry":[{"ErrorEquals":["States.ALL"]},{"ErrorEquals":["X"]}],"End":true}}}`

	// When: we create it
	resp := sfnCall(t, srv, "CreateStateMachine", map[string]any{
		"name": "all-not-last", "definition": def, "roleArn": "arn:aws:iam::000000000000:role/r",
	})
	defer resp.Body.Close()

	// Then: rejected, as the spec requires States.ALL alone and last
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
}

// ─── Concurrency ──────────────────────────────────────────────────────────────

func TestStartExecution_parallelBranchesRunConcurrently(t *testing.T) {
	// Given: three branches that each Wait one second
	srv := helpers.NewTestServer(t)
	branch := func(n string) string {
		return `{"StartAt":"W` + n + `","States":{"W` + n + `":{"Type":"Wait","Seconds":1,"End":true}}}`
	}
	def := `{"StartAt":"P","States":{"P":{"Type":"Parallel","Branches":[` +
		branch("1") + `,` + branch("2") + `,` + branch("3") + `],"End":true}}}`

	// When: it runs
	started := time.Now()
	got, _ := runToEnd(t, srv, "par-concurrent", def, `{}`)
	elapsed := time.Since(started)

	// Then: the branches overlapped rather than running back to back
	if got.Status != "SUCCEEDED" {
		t.Fatalf("status = %q (%s)", got.Status, got.Cause)
	}
	if elapsed > 2500*time.Millisecond {
		t.Errorf("elapsed %s — branches ran sequentially", elapsed)
	}
}

func TestStartExecution_parallelFailureAbortsSiblingBranches(t *testing.T) {
	// Given: one branch that fails at once and one that would wait a minute
	srv := helpers.NewTestServer(t)
	def := `{
	  "StartAt": "P",
	  "States": {
	    "P": {
	      "Type": "Parallel",
	      "Branches": [
	        {"StartAt": "Slow", "States": {"Slow": {"Type": "Wait", "Seconds": 60, "End": true}}},
	        {"StartAt": "Pause", "States": {"Pause": {"Type": "Wait", "Seconds": 1, "Next": "Boom"}, "Boom": {"Type": "Fail", "Error": "E"}}}
	      ],
	      "End": true
	    }
	  }
	}`

	// When: it runs
	started := time.Now()
	got, execARN := runToEnd(t, srv, "par-abort", def, `{}`)

	// Then: the execution fails promptly and the sibling's Wait is aborted
	if got.Status != "FAILED" || got.Error != "E" {
		t.Fatalf("status=%q error=%q, want FAILED E", got.Status, got.Error)
	}
	if time.Since(started) > 10*time.Second {
		t.Errorf("the failing branch did not stop its sibling")
	}
	events := rawHistory(t, srv, execARN)
	if len(findEvents(events, "WaitStateAborted")) != 1 {
		t.Errorf("want one WaitStateAborted, got types %v", rawTypes(events))
	}
	if len(findEvents(events, "ParallelStateFailed")) != 1 {
		t.Errorf("want one ParallelStateFailed, got types %v", rawTypes(events))
	}
}

func rawTypes(events []rawEvent) []string {
	out := make([]string, 0, len(events))
	for _, e := range events {
		out = append(out, e.typ())
	}
	return out
}

// ─── History shape for flow diagrams ──────────────────────────────────────────

// chainOf returns the ids reachable by walking previousEventId back from id.
func chainOf(events []rawEvent, id int64) map[int64]bool {
	byID := map[int64]rawEvent{}
	for _, e := range events {
		byID[e.id()] = e
	}
	seen := map[int64]bool{}
	for id != 0 && !seen[id] {
		seen[id] = true
		e, ok := byID[id]
		if !ok {
			break
		}
		id = e.prev()
	}
	return seen
}

func TestGetExecutionHistory_parallelBranchEventsLinkToTheirBranch(t *testing.T) {
	// Given: a Parallel with two two-state branches
	srv := helpers.NewTestServer(t)
	def := `{
	  "StartAt": "P",
	  "States": {
	    "P": {
	      "Type": "Parallel",
	      "Branches": [
	        {"StartAt": "A1", "States": {"A1": {"Type": "Pass", "Next": "A2"}, "A2": {"Type": "Pass", "End": true}}},
	        {"StartAt": "B1", "States": {"B1": {"Type": "Pass", "Next": "B2"}, "B2": {"Type": "Pass", "End": true}}}
	      ],
	      "Next": "After"
	    },
	    "After": {"Type": "Pass", "End": true}
	  }
	}`

	// When: it runs
	got, execARN := runToEnd(t, srv, "par-links", def, `{}`)
	if got.Status != "SUCCEEDED" {
		t.Fatalf("status = %q", got.Status)
	}
	events := rawHistory(t, srv, execARN)

	// Then: each branch's first state links to ParallelStateStarted, and each
	// branch's later events chain back through that branch only
	started := findEvents(events, "ParallelStateStarted")
	if len(started) != 1 {
		t.Fatalf("ParallelStateStarted count = %d; types %v", len(started), rawTypes(events))
	}
	byName := map[string][]rawEvent{}
	for _, e := range events {
		if n := e.stateName(); n != "" {
			byName[n] = append(byName[n], e)
		}
	}
	for _, first := range []string{"A1", "B1"} {
		if byName[first][0].prev() != started[0].id() {
			t.Errorf("%s entered prev = %d, want ParallelStateStarted %d", first, byName[first][0].prev(), started[0].id())
		}
	}
	a2 := chainOf(events, byName["A2"][1].id())
	for _, e := range byName["B1"] {
		if a2[e.id()] {
			t.Errorf("branch A's chain reaches branch B's event %d", e.id())
		}
	}
	// And the Parallel's own events are named
	for _, typ := range []string{"ParallelStateEntered", "ParallelStateExited"} {
		evs := findEvents(events, typ)
		if len(evs) != 1 || evs[0].stateName() != "P" {
			t.Errorf("%s = %v, want one naming P", typ, evs)
		}
	}
}

func TestGetExecutionHistory_mapIterationEventsCarryNameAndIndex(t *testing.T) {
	// Given: an inline Map over three items
	srv := helpers.NewTestServer(t)
	def := `{
	  "StartAt": "Each",
	  "States": {
	    "Each": {
	      "Type": "Map",
	      "ItemProcessor": {"StartAt": "Inner", "States": {"Inner": {"Type": "Pass", "End": true}}},
	      "End": true
	    }
	  }
	}`

	// When: it runs
	got, execARN := runToEnd(t, srv, "map-links", def, `["a","b","c"]`)
	if got.Status != "SUCCEEDED" {
		t.Fatalf("status = %q", got.Status)
	}
	events := rawHistory(t, srv, execARN)

	// Then: every iteration event names the Map state and its index, and the
	// iteration's inner events chain back to its own MapIterationStarted
	mapStarted := findEvents(events, "MapStateStarted")
	if len(mapStarted) != 1 {
		t.Fatalf("MapStateStarted count = %d", len(mapStarted))
	}
	if l, _ := mapStarted[0].detail("mapStateStartedEventDetails")["length"].(float64); l != 3 {
		t.Errorf("length = %v, want 3", l)
	}
	iterStarted := map[int64]int64{} // index → event id
	for _, e := range findEvents(events, "MapIterationStarted") {
		d := e.detail("mapIterationStartedEventDetails")
		if d["name"] != "Each" {
			t.Errorf("mapIterationStartedEventDetails.name = %v, want Each", d["name"])
		}
		if e.prev() != mapStarted[0].id() {
			t.Errorf("MapIterationStarted prev = %d, want MapStateStarted %d", e.prev(), mapStarted[0].id())
		}
		idx, _ := d["index"].(float64)
		iterStarted[int64(idx)] = e.id()
	}
	if len(iterStarted) != 3 {
		t.Fatalf("iteration indexes = %v, want 0..2", iterStarted)
	}
	succeeded := findEvents(events, "MapIterationSucceeded")
	if len(succeeded) != 3 {
		t.Fatalf("MapIterationSucceeded count = %d", len(succeeded))
	}
	for _, e := range succeeded {
		d := e.detail("mapIterationSucceededEventDetails")
		if d["name"] != "Each" {
			t.Errorf("mapIterationSucceededEventDetails.name = %v", d["name"])
		}
		idx, _ := d["index"].(float64)
		chain := chainOf(events, e.id())
		if !chain[iterStarted[int64(idx)]] {
			t.Errorf("iteration %v's MapIterationSucceeded does not chain to its MapIterationStarted", idx)
		}
		for other, id := range iterStarted {
			if other != int64(idx) && chain[id] {
				t.Errorf("iteration %v chains into iteration %d", idx, other)
			}
		}
	}
}

func TestStopExecution_duringWaitRecordsWaitStateAborted(t *testing.T) {
	// Given: an execution parked in a long Wait
	srv := helpers.NewTestServer(t)
	smARN := createSM(t, srv, "stop-wait", `{"StartAt":"W","States":{"W":{"Type":"Wait","Seconds":60,"End":true}}}`)
	execARN := startExec(t, srv, smARN, `{}`)
	deadline := time.Now().Add(5 * time.Second)
	for len(findEvents(rawHistory(t, srv, execARN), "WaitStateEntered")) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("never entered the Wait")
		}
		time.Sleep(5 * time.Millisecond)
	}

	// When: it is stopped
	resp := sfnCall(t, srv, "StopExecution", map[string]any{"executionArn": execARN, "error": "Stop", "cause": "user"})
	resp.Body.Close()

	// Then: the history records the aborted Wait before ExecutionAborted
	got := waitForTerminal(t, srv, execARN)
	if got.Status != "ABORTED" {
		t.Fatalf("status = %q", got.Status)
	}
	types := rawTypes(rawHistory(t, srv, execARN))
	if strings.Join(types, ",") != "ExecutionStarted,WaitStateEntered,WaitStateAborted,ExecutionAborted" {
		t.Errorf("types = %v", types)
	}
}
