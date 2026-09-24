// Package eventbridge_test — DescribeEventBus for a bus that does not exist,
// and ListEventBuses's NamePrefix, Limit and NextToken (#2110).
//
// DescribeEventBus used to answer a store miss with a 200 carrying the
// requested name and a freshly minted ARN, so an existence check took the
// "exists" branch on Overcast and the "missing" branch on AWS, whose model
// lists ResourceNotFoundException as the operation's error. ListEventBuses
// decoded no request at all: it ignored NamePrefix, Limit and NextToken and
// always answered every bus in the region.
package eventbridge_test

import (
	"net/http"
	"sort"
	"testing"

	"github.com/overcast-sh/overcast/tests/helpers"
)

// listEventBusesPage performs ListEventBuses and decodes one page.
func listEventBusesPage(t *testing.T, srv *helpers.TestServer, body map[string]any) (names []string, nextToken string) {
	t.Helper()
	resp := ebCall(t, srv, "ListEventBuses", body)
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	var out struct {
		EventBuses []struct {
			Name string `json:"Name"`
			Arn  string `json:"Arn"`
		} `json:"EventBuses"`
		NextToken string `json:"NextToken"`
	}
	helpers.DecodeJSON(t, resp, &out)
	for _, b := range out.EventBuses {
		if b.Arn == "" {
			t.Errorf("ListEventBuses returned bus %q with no Arn", b.Name)
		}
		names = append(names, b.Name)
	}
	return names, out.NextToken
}

func createBuses(t *testing.T, srv *helpers.TestServer, names ...string) {
	t.Helper()
	for _, name := range names {
		resp := ebCall(t, srv, "CreateEventBus", map[string]any{"Name": name})
		helpers.AssertStatus(t, resp, http.StatusOK)
		resp.Body.Close()
	}
}

// ─── DescribeEventBus ─────────────────────────────────────────────────────

func TestDescribeEventBus_unknownBus(t *testing.T) {
	// Given: no custom bus has been created.
	srv := helpers.NewTestServer(t)

	// When: DescribeEventBus names a bus that does not exist.
	resp := ebCall(t, srv, "DescribeEventBus", map[string]any{"Name": "never-created"})
	defer resp.Body.Close()

	// Then: ResourceNotFoundException, not a synthesized bus.
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "ResourceNotFoundException")
}

func TestDescribeEventBus_deletedBus(t *testing.T) {
	// Given: a bus that was created and then deleted.
	srv := helpers.NewTestServer(t)
	createBuses(t, srv, "short-lived")
	del := ebCall(t, srv, "DeleteEventBus", map[string]any{"Name": "short-lived"})
	helpers.AssertStatus(t, del, http.StatusOK)
	del.Body.Close()

	// When: DescribeEventBus names it.
	resp := ebCall(t, srv, "DescribeEventBus", map[string]any{"Name": "short-lived"})
	defer resp.Body.Close()

	// Then: it is gone.
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "ResourceNotFoundException")
}

func TestDescribeEventBus_unknownBusArn(t *testing.T) {
	// Given: no custom bus has been created.
	srv := helpers.NewTestServer(t)

	// When: DescribeEventBus names a missing bus by ARN, which the Name
	// member's pattern admits.
	resp := ebCall(t, srv, "DescribeEventBus", map[string]any{
		"Name": "arn:aws:events:us-east-1:000000000000:event-bus/never-created",
	})
	defer resp.Body.Close()

	// Then: ResourceNotFoundException.
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "ResourceNotFoundException")
}

func TestDescribeEventBus_existingBusByArn(t *testing.T) {
	// Given: a custom bus.
	srv := helpers.NewTestServer(t)
	resp := ebCall(t, srv, "CreateEventBus", map[string]any{"Name": "by-arn"})
	helpers.AssertStatus(t, resp, http.StatusOK)
	var created struct {
		EventBusArn string `json:"EventBusArn"`
	}
	helpers.DecodeJSON(t, resp, &created)
	resp.Body.Close()

	// When: DescribeEventBus names it by its ARN.
	described := describeEventBusBody(t, srv, created.EventBusArn)

	// Then: the bus is described under its name, not the ARN.
	if described["Name"] != "by-arn" || described["Arn"] != created.EventBusArn {
		t.Errorf("DescribeEventBus by ARN = Name %v Arn %v, want by-arn %s",
			described["Name"], described["Arn"], created.EventBusArn)
	}
}

func TestDescribeEventBus_defaultBusNeverCreated(t *testing.T) {
	// Given: an empty store — the default bus is never written to it.
	srv := helpers.NewTestServer(t)

	for _, name := range []string{"", "default"} {
		// When: DescribeEventBus names the default bus, explicitly or by
		// omitting Name.
		described := describeEventBusBody(t, srv, name)

		// Then: it always exists.
		if described["Name"] != "default" || described["Arn"] == nil || described["Arn"] == "" {
			t.Errorf("DescribeEventBus(%q) = %v, want the default bus", name, described)
		}
	}
}

// ─── ListEventBuses ───────────────────────────────────────────────────────

func TestListEventBuses_namePrefix(t *testing.T) {
	// Given: buses under two prefixes.
	srv := helpers.NewTestServer(t)
	createBuses(t, srv, "orders-a", "orders-b", "billing")

	// When: ListEventBuses is filtered by a prefix.
	names, next := listEventBusesPage(t, srv, map[string]any{"NamePrefix": "orders-"})

	// Then: only the matching buses — not default, not billing.
	sort.Strings(names)
	if len(names) != 2 || names[0] != "orders-a" || names[1] != "orders-b" || next != "" {
		t.Errorf("NamePrefix orders- = %v (next %q), want [orders-a orders-b]", names, next)
	}

	// And: a prefix of default matches the default bus too.
	names, _ = listEventBusesPage(t, srv, map[string]any{"NamePrefix": "def"})
	if len(names) != 1 || names[0] != "default" {
		t.Errorf("NamePrefix def = %v, want [default]", names)
	}
}

func TestListEventBuses_limitAndNextToken(t *testing.T) {
	// Given: three custom buses, four with default.
	srv := helpers.NewTestServer(t)
	createBuses(t, srv, "page-a", "page-b", "page-c")

	// When: the buses are listed two at a time.
	first, next := listEventBusesPage(t, srv, map[string]any{"Limit": 2})

	// Then: the first page is truncated and carries a NextToken.
	if len(first) != 2 || next == "" {
		t.Fatalf("Limit 2 page 1 = %v (next %q), want 2 buses and a NextToken", first, next)
	}

	// And: following the token returns the rest, with no token after it.
	second, last := listEventBusesPage(t, srv, map[string]any{"Limit": 2, "NextToken": next})
	if len(second) != 2 || last != "" {
		t.Fatalf("Limit 2 page 2 = %v (next %q), want 2 buses and no NextToken", second, last)
	}
	all := append(first, second...)
	sort.Strings(all)
	want := []string{"default", "page-a", "page-b", "page-c"}
	for i := range want {
		if all[i] != want[i] {
			t.Fatalf("pages together = %v, want %v", all, want)
		}
	}
}

func TestListEventBuses_invalidNextToken(t *testing.T) {
	// Given: a server with the default bus.
	srv := helpers.NewTestServer(t)

	// When: ListEventBuses is given a token it never issued.
	resp := ebCall(t, srv, "ListEventBuses", map[string]any{"NextToken": "not-a-token"})
	defer resp.Body.Close()

	// Then: the request is refused rather than restarting from page one.
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "ValidationException")
}

func TestListEventBuses_limitOutOfRange(t *testing.T) {
	// Given: a server with the default bus.
	srv := helpers.NewTestServer(t)

	for _, limit := range []int{0, 101} {
		// When: Limit falls outside the modeled 1..100 range.
		resp := ebCall(t, srv, "ListEventBuses", map[string]any{"Limit": limit})

		// Then: ValidationException.
		helpers.AssertStatus(t, resp, http.StatusBadRequest)
		helpers.AssertJSONError(t, resp, "ValidationException")
		resp.Body.Close()
	}
}
