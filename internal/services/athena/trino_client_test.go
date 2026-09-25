package athena

import (
	"context"
	"errors"
	"testing"
)

func threePages() []trinoResponse {
	return []trinoResponse{
		{Stats: trinoStats{State: "QUEUED"}},
		{Stats: trinoStats{State: "RUNNING"}, Data: [][]any{{"a"}}},
		{Stats: trinoStats{State: "FINISHED"}, Data: [][]any{{"b"}}},
	}
}

func TestTrinoClient_followsNextURIToTheLastPage(t *testing.T) {
	// Given: a statement the engine answers in three pages
	f := newFakeTrino(t)
	f.script("SELECT x", threePages()...)

	// When: it is executed in a session
	var seen []string
	final, err := newTrinoClient().execute(context.Background(), f.srv.URL, "SELECT x", trinoSession{Catalog: "awsdatacatalog", Schema: "demo"},
		func(p *trinoResponse) error {
			seen = append(seen, p.Stats.State)
			return nil
		})

	// Then: every page was seen, in order, the last is returned, and the
	// session travelled in Trino's headers
	if err != nil || final == nil || final.Stats.State != "FINISHED" {
		t.Fatalf("final = %+v, err = %v", final, err)
	}
	if len(seen) != 3 || seen[0] != "QUEUED" || seen[2] != "FINISHED" {
		t.Fatalf("pages = %v", seen)
	}
	if s := f.sessions[0]; s.Catalog != "awsdatacatalog" || s.Schema != "demo" {
		t.Fatalf("session = %+v", s)
	}
}

func TestTrinoClient_returnsAFailedQuerysError(t *testing.T) {
	// Given: a statement the engine fails
	f := newFakeTrino(t)
	f.script("SELECT nope", trinoResponse{Error: &trinoError{Message: "line 1:8: Column 'nope' cannot be resolved",
		ErrorName: "COLUMN_NOT_FOUND", ErrorType: "USER_ERROR"}})

	// When: it is executed
	_, err := newTrinoClient().execute(context.Background(), f.srv.URL, "SELECT nope", trinoSession{}, func(*trinoResponse) error { return nil })

	// Then: the error is the engine's own
	var te *trinoError
	if !errors.As(err, &te) || te.ErrorName != "COLUMN_NOT_FOUND" {
		t.Fatalf("err = %v, want the engine's COLUMN_NOT_FOUND", err)
	}
}

func TestTrinoClient_cancelsWhenThePageHandlerStops(t *testing.T) {
	// Given: a three-page statement whose second page the caller refuses
	f := newFakeTrino(t)
	f.script("SELECT x", threePages()...)
	stop := errors.New("stop")

	// When: the handler returns an error on the RUNNING page
	_, err := newTrinoClient().execute(context.Background(), f.srv.URL, "SELECT x", trinoSession{}, func(p *trinoResponse) error {
		if p.Stats.State == "RUNNING" {
			return stop
		}
		return nil
	})

	// Then: the handler's error comes back and the query was cancelled at
	// the page it would have fetched next
	if !errors.Is(err, stop) {
		t.Fatalf("err = %v, want the handler's", err)
	}
	if d := f.deletes(); len(d) != 1 || d[0] != "/v1/statement/executing/0/2" {
		t.Fatalf("DELETEs = %v", d)
	}
}

func TestTrinoClient_cancelsWhenTheContextEnds(t *testing.T) {
	// Given: a three-page statement, and a context cancelled after the first
	f := newFakeTrino(t)
	f.script("SELECT x", threePages()...)
	ctx, cancel := context.WithCancel(context.Background())

	// When: it is executed
	_, err := newTrinoClient().execute(ctx, f.srv.URL, "SELECT x", trinoSession{}, func(*trinoResponse) error {
		cancel()
		return nil
	})

	// Then: it ends with the context and cancels the query on the engine
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if d := f.deletes(); len(d) != 1 {
		t.Fatalf("DELETEs = %v, want the query cancelled", d)
	}
}

func TestTrinoClient_unreachableEngine(t *testing.T) {
	// Given: an engine that is not there
	f := newFakeTrino(t)
	url := f.srv.URL
	f.srv.Close()

	// When: a statement is executed
	_, err := newTrinoClient().execute(context.Background(), url, "SELECT 1", trinoSession{}, func(*trinoResponse) error { return nil })

	// Then: the error says the engine could not be reached
	if !errors.Is(err, errTrinoUnreachable) {
		t.Fatalf("err = %v, want errTrinoUnreachable", err)
	}
}

func TestTrinoClient_info(t *testing.T) {
	f := newFakeTrino(t)
	f.starting = true
	if starting, err := newTrinoClient().info(context.Background(), f.srv.URL); err != nil || !starting {
		t.Fatalf("info = %v, %v; want starting", starting, err)
	}
}
