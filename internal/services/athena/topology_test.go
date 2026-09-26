package athena

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/overcast-sh/overcast/internal/topology"
)

// putExecution stores an execution submitted ago before the service's clock.
func putExecution(t *testing.T, s *Service, qe QueryExecution, ago time.Duration) {
	t.Helper()
	qe.Status.SubmissionDateTime = float64(s.clk.Now().Add(-ago).UnixMilli()) / 1000
	if err := s.store.putQuery(context.Background(), &qe); err != nil {
		t.Fatalf("putQuery: %v", err)
	}
}

func contributeAthena(t *testing.T, s *Service) topology.Response {
	t.Helper()
	g := &topology.Graph{}
	if err := s.ContributeTopology(context.Background(), g); err != nil {
		t.Fatalf("ContributeTopology: %v", err)
	}
	// The endpoints of Athena's edges are other services' nodes.
	targets := &topology.Graph{}
	for _, id := range []string{"s3::results", "glue::sales", "glue::audit", "s3tables::lake"} {
		kind, name, _ := strings.Cut(id, "::")
		targets.AddNode(topology.Node{ID: topology.NodeID("us-east-1", kind, name), Service: kind, Label: name, Region: "us-east-1"})
	}
	return topology.Build("", g, targets)
}

func TestContributeTopology_workgroupsRunsAndEdges(t *testing.T) {
	// Given: a workgroup with a configured result location and four
	// executions — two recent ones that read a Glue database (one a DDL
	// statement naming a table in another) and a table bucket, and older ones
	// — plus a second workgroup that has run nothing
	s, _ := newTestService(t)
	ctx := context.Background()
	for _, name := range []string{"analytics", "idle"} {
		wg := &workGroupRecord{WorkGroup: WorkGroup{Name: name, State: workGroupEnabled}}
		if name == "analytics" {
			wg.Configuration = &WorkGroupConfiguration{ResultConfiguration: &ResultConfiguration{OutputLocation: "s3://configured/"}}
		}
		if err := s.store.putWorkGroup(ctx, wg); err != nil {
			t.Fatalf("putWorkGroup: %v", err)
		}
	}
	putExecution(t, s, QueryExecution{
		QueryExecutionId: "q-old", WorkGroup: "analytics", Query: "SELECT 0",
		QueryExecutionContext: QueryExecutionContext{Database: "old"},
		Status:                QueryExecutionStatus{State: stateSucceeded},
	}, time.Hour)
	putExecution(t, s, QueryExecution{
		QueryExecutionId: "q-ddl", WorkGroup: "analytics", Query: "ALTER TABLE audit.events ADD PARTITION (dt='1')",
		QueryExecutionContext: QueryExecutionContext{Database: "sales"},
		Status:                QueryExecutionStatus{State: stateSucceeded},
	}, 3*time.Minute)
	putExecution(t, s, QueryExecution{
		QueryExecutionId: "q-lake", WorkGroup: "analytics", Query: "SELECT * FROM orders",
		QueryExecutionContext: QueryExecutionContext{Catalog: "s3tablescatalog/lake", Database: "shop"},
		Status:                QueryExecutionStatus{State: stateFailed},
	}, 2*time.Minute)
	putExecution(t, s, QueryExecution{
		QueryExecutionId: "q-new", WorkGroup: "analytics", Query: "SELECT  region,\n\tcount(*)\nFROM sales GROUP BY 1",
		QueryExecutionContext: QueryExecutionContext{Database: "Sales"},
		ResultConfiguration:   ResultConfiguration{OutputLocation: "s3://results/athena/q-new.csv"},
		Status:                QueryExecutionStatus{State: stateRunning},
	}, time.Minute)

	// When: Athena contributes to the map
	resp := contributeAthena(t, s)

	// Then: both stored workgroups are nodes, and primary, which has run
	// nothing, is not
	var wgs []topology.Node
	for _, n := range resp.Nodes {
		if n.Service == serviceName {
			wgs = append(wgs, n)
		}
	}
	if len(wgs) != 2 || wgs[0].Label != "analytics" || wgs[1].Label != "idle" {
		t.Fatalf("workgroup nodes = %+v", wgs)
	}
	// And: analytics lists its three latest runs, newest first, on one line
	var ids []string
	for _, r := range wgs[0].RecentQueries {
		ids = append(ids, r.ID)
	}
	if !slices.Equal(ids, []string{"q-new", "q-lake", "q-ddl"}) {
		t.Errorf("recent runs = %v", ids)
	}
	if got := wgs[0].RecentQueries[0]; got.Query != "SELECT region, count(*) FROM sales GROUP BY 1" || got.State != stateRunning || got.SubmittedAt != s.clk.Now().Add(-time.Minute).UnixMilli() {
		t.Errorf("newest run = %+v", got)
	}
	// And: the engine chip says the engine is off (the service has none)
	if wgs[0].EngineState != EngineOff {
		t.Errorf("engine state = %q", wgs[0].EngineState)
	}
	// And: results go where the newest query wrote them, and the recent
	// queries read sales, audit and the table bucket — not the old database
	var edges []string
	for _, e := range resp.Edges {
		edges = append(edges, e.Type+" "+e.Target)
	}
	slices.Sort(edges)
	want := []string{
		"queries us-east-1::glue::audit",
		"queries us-east-1::glue::sales",
		"queries us-east-1::s3tables::lake",
		"query-results us-east-1::s3::results",
	}
	if !slices.Equal(edges, want) {
		t.Errorf("edges = %v, want %v", edges, want)
	}
}

func TestContributeTopology_primaryOnceItHasRun(t *testing.T) {
	// Given: primary has never been seeded, but a query ran in it
	s, _ := newTestService(t)
	putExecution(t, s, QueryExecution{QueryExecutionId: "q-1", WorkGroup: primaryWorkGroup, Query: "SELECT 1",
		Status: QueryExecutionStatus{State: stateSucceeded}}, time.Hour)

	// When: Athena contributes to the map
	resp := contributeAthena(t, s)

	// Then: primary is drawn, and with no result location nothing is linked
	var labels []string
	for _, n := range resp.Nodes {
		if n.Service == serviceName {
			labels = append(labels, n.Label)
		}
	}
	if !slices.Equal(labels, []string{primaryWorkGroup}) || len(resp.Edges) != 0 {
		t.Errorf("workgroups %v, edges %+v", labels, resp.Edges)
	}
}

func TestSQLSnippet_cutsLongQueries(t *testing.T) {
	// Given: a query longer than a node row
	long := "SELECT " + strings.Repeat("col, ", 40) + "x FROM t"

	// When: it is cut to a snippet
	got := sqlSnippet(long)

	// Then: it keeps the row's length and ends in an ellipsis
	if n := len([]rune(got)); n != topologySnippetRunes || !strings.HasSuffix(got, "…") {
		t.Errorf("snippet (%d runes) = %q", n, got)
	}
}

func TestEngineAttention(t *testing.T) {
	for state, want := range map[string]string{
		EngineReady: "", EngineStopped: "", EngineOff: EngineOff, EngineStarting: EngineStarting,
		EnginePulling: EnginePulling, EngineFailed: EngineFailed, EngineProbing: EngineProbing,
	} {
		t.Run(state, func(t *testing.T) {
			// Given: an engine state; When: a node asks whether to show it;
			// Then: only a state that keeps a query off the engine is shown
			if got := engineAttention(state); got != want {
				t.Errorf("engineAttention(%q) = %q, want %q", state, got, want)
			}
		})
	}
}

func TestResultLocation(t *testing.T) {
	enforced, open := true, false
	cfg := func(enforce *bool) *WorkGroupConfiguration {
		return &WorkGroupConfiguration{EnforceWorkGroupConfiguration: enforce,
			ResultConfiguration: &ResultConfiguration{OutputLocation: "s3://workgroup/"}}
	}
	const ran = "s3://client/q.csv"
	cases := []struct {
		name string
		cfg  *WorkGroupConfiguration
		last string
		want string
	}{
		{"enforced wins over the last query", cfg(&enforced), ran, "s3://workgroup/"},
		{"otherwise the last query's", cfg(&open), ran, "s3://client/q.csv"},
		{"before any query, the configured one", cfg(&open), "", "s3://workgroup/"},
		{"none at all", nil, "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// Given: a workgroup and where its last query wrote; When: its
			// results bucket is read; Then: it is where a query's results go
			if got := resultLocation(c.cfg, c.last); got != c.want {
				t.Errorf("resultLocation = %q, want %q", got, c.want)
			}
		})
	}
}
