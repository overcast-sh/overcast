package athena

import (
	"context"
	"slices"
	"testing"

	"github.com/overcast-sh/overcast/internal/events"
	"github.com/overcast-sh/overcast/internal/events/eventstest"
	"github.com/overcast-sh/overcast/internal/protocol"
)

func newBusServiceWithExecutor(t *testing.T) (*Service, *fakeExecutor, *events.Bus) {
	t.Helper()
	s, f := newServiceWithExecutor(t)
	bus := events.NewBus()
	t.Cleanup(bus.Stop)
	s.InitBus(bus)
	return s, f, bus
}

// publishedStates is the State of every athena:QueryStateChanged event bus
// has published, in order.
func publishedStates(bus *events.Bus) []string {
	var states []string
	for _, e := range eventstest.Published(bus, events.AthenaQueryStateChanged) {
		states = append(states, e.Payload.(events.AthenaQueryStatePayload).State)
	}
	return states
}

func TestQueryStateChanged_publishedOnceForEachStateReached(t *testing.T) {
	// Given: a query started and reported through to success, with a
	// repeated report and one that arrives after it finished
	ctx := context.Background()
	s, f, bus := newBusServiceWithExecutor(t)
	out, aerr := s.startQueryExecutionTyped(ctx, startReq("SELECT 1"))
	mustOK(t, "StartQueryExecution", aerr)
	id := out.QueryExecutionId
	f.report(ctx, id, queryTransition{State: stateRunning})
	f.report(ctx, id, queryTransition{State: stateRunning})
	f.report(ctx, id, queryTransition{State: stateSucceeded})
	f.report(ctx, id, queryTransition{State: stateFailed})

	// Then: each state the query reached was published once, in order
	if got, want := publishedStates(bus), []string{stateQueued, stateRunning, stateSucceeded}; !slices.Equal(got, want) {
		t.Fatalf("states = %v, want %v", got, want)
	}
	// And: each event names the query, its workgroup and its database
	for _, e := range eventstest.Published(bus, events.AthenaQueryStateChanged) {
		p := e.Payload.(events.AthenaQueryStatePayload)
		if p.QueryExecutionID != id || p.WorkGroup != primaryWorkGroup || p.Database != defaultDatabase || p.Tables != nil || e.Source != serviceName {
			t.Fatalf("event = %+v", e)
		}
	}
}

func TestQueryStateChanged_stopPublishesCancelledOnce(t *testing.T) {
	// Given: a running query that is stopped, then reported successful
	ctx := context.Background()
	s, f, bus := newBusServiceWithExecutor(t)
	out, aerr := s.startQueryExecutionTyped(ctx, startReq("SELECT 1"))
	mustOK(t, "StartQueryExecution", aerr)
	f.report(ctx, out.QueryExecutionId, queryTransition{State: stateRunning})
	_, aerr = s.stopQueryExecutionTyped(ctx, &queryIDReq{QueryExecutionId: out.QueryExecutionId})
	mustOK(t, "StopQueryExecution", aerr)
	f.report(ctx, out.QueryExecutionId, queryTransition{State: stateSucceeded})

	// Then: CANCELLED is the last state published, and success never is
	if got, want := publishedStates(bus), []string{stateQueued, stateRunning, stateCancelled}; !slices.Equal(got, want) {
		t.Fatalf("states = %v, want %v", got, want)
	}
}

func TestQueryStateChanged_idempotentRetryPublishesNothingMore(t *testing.T) {
	// Given: a start retried with the same client request token
	ctx := context.Background()
	s, _, bus := newBusServiceWithExecutor(t)
	req := startReq("SELECT 1")
	req.ClientRequestToken = "retry-token-0123456789abcdef0123"
	for range 2 {
		_, aerr := s.startQueryExecutionTyped(ctx, req)
		mustOK(t, "StartQueryExecution", aerr)
	}

	// Then: the one execution it created was published QUEUED once
	if got := publishedStates(bus); !slices.Equal(got, []string{stateQueued}) {
		t.Fatalf("states = %v, want [QUEUED]", got)
	}
}

func TestQueryStateChanged_carriesTheDDLTableInItsDatabase(t *testing.T) {
	// Given: DDL naming an unqualified table, run in the database Sales
	ctx := context.Background()
	s, _, bus := newBusServiceWithExecutor(t)
	req := startReq("ALTER TABLE Orders ADD PARTITION (dt = '2026-09-25')")
	req.QueryExecutionContext = &QueryExecutionContext{Database: "Sales"}
	_, aerr := s.startQueryExecutionTyped(ctx, req)
	mustOK(t, "StartQueryExecution", aerr)

	// Then: the event names the database and the table in it, as the catalog
	// folds them
	got := eventstest.Published(bus, events.AthenaQueryStateChanged)
	if len(got) != 1 {
		t.Fatalf("published %d events, want 1", len(got))
	}
	p := got[0].Payload.(events.AthenaQueryStatePayload)
	if p.Database != "sales" || !slices.Equal(p.Tables, []string{"sales.orders"}) {
		t.Fatalf("database, tables = %q, %v; want sales, [sales.orders]", p.Database, p.Tables)
	}
}

func TestStatementTables(t *testing.T) {
	tests := []struct {
		query string
		want  []string
	}{
		{"CREATE EXTERNAL TABLE logs.events (id bigint) LOCATION 's3://b/e/'", []string{"logs.events"}},
		{"DROP TABLE IF EXISTS orders", []string{"sales.orders"}},
		{"MSCK REPAIR TABLE orders", []string{"sales.orders"}},
		{"DESCRIBE orders", []string{"sales.orders"}},
		{"SHOW PARTITIONS logs.events", []string{"logs.events"}},
		{"CREATE DATABASE analytics", nil},
		{"SHOW TABLES", nil},
		{"SELECT * FROM orders", nil},
		{"CREATE EXTERNAL TABLE (", nil},
	}
	for _, tt := range tests {
		if got := statementTables(tt.query, "sales"); !slices.Equal(got, tt.want) {
			t.Errorf("statementTables(%q) = %v, want %v", tt.query, got, tt.want)
		}
	}
}

func TestQueryStateChanged_reapedQueriesCarryNoRequestID(t *testing.T) {
	// Given: an execution a previous process left RUNNING
	s, _, bus := newBusServiceWithExecutor(t)
	ctx := context.Background()
	left := &QueryExecution{QueryExecutionId: "left-running", WorkGroup: primaryWorkGroup,
		Status: QueryExecutionStatus{State: stateRunning}}
	if err := s.store.putQuery(ctx, left); err != nil {
		t.Fatal(err)
	}

	// When: this process's first request reaps it
	s.reapInterrupted(protocol.ContextWithRequestID(ctx, "unrelated-request"))

	// Then: its FAILED event is published, but not as part of that request
	got := eventstest.Published(bus, events.AthenaQueryStateChanged)
	if len(got) != 1 || got[0].Payload.(events.AthenaQueryStatePayload).State != stateFailed {
		t.Fatalf("published %+v, want one FAILED", got)
	}
	if got[0].RequestID != "" {
		t.Fatalf("request ID = %q, want none", got[0].RequestID)
	}
}
