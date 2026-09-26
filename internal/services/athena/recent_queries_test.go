package athena

import (
	"context"
	"fmt"
	"math/rand/v2"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/overcast-sh/overcast/internal/state"
	"github.com/overcast-sh/overcast/internal/topology"
)

// readCountingStore counts the reads of each namespace, and the records
// scans return from it: the executions a map fetch decodes.
type readCountingStore struct {
	state.Store
	reads   map[string]*atomic.Int64
	scanned map[string]*atomic.Int64
}

func newReadCountingStore() *readCountingStore {
	c := &readCountingStore{Store: state.NewMemoryStore(), reads: map[string]*atomic.Int64{}, scanned: map[string]*atomic.Int64{}}
	for _, ns := range []string{nsQueries, nsRecentQueries} {
		c.reads[ns], c.scanned[ns] = new(atomic.Int64), new(atomic.Int64)
	}
	return c
}

func (c *readCountingStore) count(ns string, records int) {
	if n := c.reads[ns]; n != nil {
		n.Add(1)
		c.scanned[ns].Add(int64(records))
	}
}

func (c *readCountingStore) Get(ctx context.Context, ns, key string) (string, bool, error) {
	c.count(ns, 1)
	return c.Store.Get(ctx, ns, key)
}

func (c *readCountingStore) Scan(ctx context.Context, ns, prefix string) ([]state.KV, error) {
	kvs, err := c.Store.Scan(ctx, ns, prefix)
	c.count(ns, len(kvs))
	return kvs, err
}

func (c *readCountingStore) resetCounts() {
	for ns := range c.reads {
		c.reads[ns].Store(0)
		c.scanned[ns].Store(0)
	}
}

// longHistory is n executions spread over six hours across three
// workgroups, each reading one of several Glue databases or table buckets —
// some through a DDL statement naming another database — in shuffled order.
func longHistory(now time.Time, n int) []QueryExecution {
	rng := rand.New(rand.NewPCG(2222, 1))
	workGroups := []string{"analytics", "etl", primaryWorkGroup}
	out := make([]QueryExecution, n)
	for i := range out {
		qe := QueryExecution{
			QueryExecutionId:      fmt.Sprintf("q-%05d", i),
			WorkGroup:             workGroups[i%len(workGroups)],
			Query:                 "SELECT * FROM events",
			QueryExecutionContext: QueryExecutionContext{Database: fmt.Sprintf("db%d", i%7)},
			ResultConfiguration:   ResultConfiguration{OutputLocation: fmt.Sprintf("s3://results-%d/q-%05d.csv", i%4, i)},
			Status: QueryExecutionStatus{
				State:              stateSucceeded,
				SubmissionDateTime: float64(now.Add(-time.Duration(i)*5*time.Second).UnixMilli()) / 1000,
			},
		}
		switch i % 5 {
		case 0:
			qe.QueryExecutionContext = QueryExecutionContext{Catalog: fmt.Sprintf("s3tablescatalog/lake%d", i%2), Database: "shop"}
		case 1:
			qe.Query = fmt.Sprintf("ALTER TABLE audit%d.events ADD PARTITION (dt='1')", i%3)
		}
		out[i] = qe
	}
	rng.Shuffle(len(out), func(i, j int) { out[i], out[j] = out[j], out[i] })
	return out
}

// putHistoryWorkGroups stores the workgroups a history runs in, less
// primary, which is drawn once it has run something.
func putHistoryWorkGroups(t *testing.T, s *Service) {
	t.Helper()
	for _, name := range []string{"analytics", "etl"} {
		if err := s.store.putWorkGroup(context.Background(), &workGroupRecord{WorkGroup: WorkGroup{Name: name, State: workGroupEnabled}}); err != nil {
			t.Fatalf("putWorkGroup: %v", err)
		}
	}
}

// storeHistory stores each execution as a query's life does: submitted
// RUNNING, then finished.
func storeHistory(t *testing.T, s *Service, history []QueryExecution) {
	t.Helper()
	ctx := context.Background()
	for _, qe := range history {
		final := qe.Status.State
		qe.Status.State = stateRunning
		if err := s.store.putQuery(ctx, &qe); err != nil {
			t.Fatalf("putQuery: %v", err)
		}
		qe.Status.State, qe.Status.CompletionDateTime = final, qe.Status.SubmissionDateTime+2
		if err := s.store.putQuery(ctx, &qe); err != nil {
			t.Fatalf("putQuery: %v", err)
		}
	}
}

// contributed is what Athena's contribution says, in a form a test compares:
// each workgroup node's runs, and every edge.
type contributed struct {
	runs  map[string][]topology.QueryRun
	edges []string
}

// contributeAll contributes Athena to a map holding a node for every bucket,
// database and table bucket a history can name.
func contributeAll(t *testing.T, s *Service) contributed {
	t.Helper()
	g := &topology.Graph{}
	if err := s.ContributeTopology(context.Background(), g); err != nil {
		t.Fatalf("ContributeTopology: %v", err)
	}
	targets := &topology.Graph{}
	add := func(kind, name string) {
		targets.AddNode(topology.Node{ID: topology.NodeID("us-east-1", kind, name), Service: kind, Label: name, Region: "us-east-1"})
	}
	for i := range 7 {
		add("glue", fmt.Sprintf("db%d", i))
		add("glue", fmt.Sprintf("audit%d", i))
		add("s3", fmt.Sprintf("results-%d", i))
		add("s3tables", fmt.Sprintf("lake%d", i))
	}
	resp := topology.Build("", g, targets)
	out := contributed{runs: map[string][]topology.QueryRun{}}
	for _, n := range resp.Nodes {
		if n.Service == serviceName {
			out.runs[n.Label] = n.RecentQueries
		}
	}
	for _, e := range resp.Edges {
		out.edges = append(out.edges, e.Source+" "+e.Type+" "+e.Target)
	}
	slices.Sort(out.edges)
	return out
}

// expectedContribution is what the map shows of history, worked out from
// every execution in it: each workgroup's three newest runs, its newest
// run's results bucket, and what the last fifteen minutes' queries read.
func expectedContribution(now time.Time, history []QueryExecution) contributed {
	byWorkGroup := map[string][]QueryExecution{}
	for _, qe := range history {
		byWorkGroup[qe.WorkGroup] = append(byWorkGroup[qe.WorkGroup], qe)
	}
	cutoff := float64(now.Add(-topologyQueryWindow).UnixMilli()) / 1000
	out := contributed{runs: map[string][]topology.QueryRun{}}
	edges := map[string]bool{}
	for wg, runs := range byWorkGroup {
		slices.SortFunc(runs, func(a, b QueryExecution) int {
			return int(b.Status.SubmissionDateTime*1000 - a.Status.SubmissionDateTime*1000)
		})
		node := topology.NodeID("us-east-1", serviceName, wg)
		for _, qe := range runs[:topologyRecentRuns] {
			qe.Status.CompletionDateTime = qe.Status.SubmissionDateTime + 2
			out.runs[wg] = append(out.runs[wg], queryRun(&qe))
		}
		bucket, _, _ := strings.Cut(strings.TrimPrefix(runs[0].ResultConfiguration.OutputLocation, "s3://"), "/")
		edges[node+" query-results "+topology.NodeID("us-east-1", "s3", bucket)] = true
		for _, qe := range runs {
			if qe.Status.SubmissionDateTime < cutoff {
				continue
			}
			for _, r := range queryReads(&qe) {
				edges[node+" queries "+topology.NodeID("us-east-1", r.Kind, r.Name)] = true
			}
		}
	}
	for e := range edges {
		out.edges = append(out.edges, e)
	}
	slices.Sort(out.edges)
	return out
}

func assertContribution(t *testing.T, got, want contributed) {
	t.Helper()
	if len(got.runs) != len(want.runs) {
		t.Fatalf("workgroups = %d, want %d", len(got.runs), len(want.runs))
	}
	for wg, runs := range want.runs {
		if !slices.Equal(got.runs[wg], runs) {
			t.Errorf("%s runs = %+v, want %+v", wg, got.runs[wg], runs)
		}
	}
	if !slices.Equal(got.edges, want.edges) {
		t.Errorf("edges = %v\nwant %v", got.edges, want.edges)
	}
}

func TestContributeTopology_longHistoryIsNotDecodedPerFetch(t *testing.T) {
	// Given: a service that has drawn the map once, and then stores 4,000
	// executions, each submitted and then finished
	st := newReadCountingStore()
	s := newTestServiceOn(t, st)
	putHistoryWorkGroups(t, s)
	contributeAll(t, s)
	history := longHistory(s.clk.Now(), 4000)
	storeHistory(t, s, history)
	want := expectedContribution(s.clk.Now(), history)
	st.resetCounts()

	// When: Athena contributes to the map again
	got := contributeAll(t, s)

	// Then: it read no execution at all — only one entry per workgroup
	if n := st.reads[nsQueries].Load(); n != 0 {
		t.Errorf("execution reads = %d (%d records), want none", n, st.scanned[nsQueries].Load())
	}
	if n := st.scanned[nsRecentQueries].Load(); n != 3 {
		t.Errorf("recent-queries entries read = %d, want one per workgroup (3)", n)
	}
	// And: it shows what a read of the whole history shows
	assertContribution(t, got, want)

	// And: each entry stays small: three runs, and only reads within the
	// window of its newest query
	recent, err := s.store.recentQueries(context.Background())
	if err != nil {
		t.Fatalf("recentQueries: %v", err)
	}
	for wg, entry := range recent {
		if len(entry.Runs) != topologyRecentRuns || len(entry.Reads) > 7+3+2 {
			t.Errorf("%s entry holds %d runs and %d reads", wg, len(entry.Runs), len(entry.Reads))
		}
	}
}

func TestContributeTopology_rebuildsOnceAfterARestart(t *testing.T) {
	// Given: 600 stored executions whose recent-queries entries are gone, as
	// in data written before they existed
	st := newReadCountingStore()
	before := newTestServiceOn(t, st)
	putHistoryWorkGroups(t, before)
	history := longHistory(before.clk.Now(), 600)
	storeHistory(t, before, history)
	keys, _ := st.List(context.Background(), nsRecentQueries, "")
	for _, k := range keys {
		_ = st.Delete(context.Background(), nsRecentQueries, k)
	}

	// When: a new process draws the map, twice
	s := newTestServiceOn(t, st)
	st.resetCounts()
	first := contributeAll(t, s)
	firstScanned := st.scanned[nsQueries].Load()
	st.resetCounts()
	second := contributeAll(t, s)

	// Then: the first fetch rebuilt the entries from the executions, once,
	// and the second read none
	if firstScanned != 600 || st.reads[nsQueries].Load() != 0 {
		t.Errorf("executions decoded: first fetch %d, second %d reads", firstScanned, st.reads[nsQueries].Load())
	}
	// And: both show the whole history's answer
	want := expectedContribution(s.clk.Now(), history)
	assertContribution(t, first, want)
	assertContribution(t, second, want)
}

func TestContributeTopology_unreadableEntryIsRebuilt(t *testing.T) {
	// Given: a drawn map over 30 executions, whose analytics entry is then
	// corrupted
	st := newReadCountingStore()
	s := newTestServiceOn(t, st)
	ctx := context.Background()
	putHistoryWorkGroups(t, s)
	history := longHistory(s.clk.Now().Add(-time.Minute), 30)
	storeHistory(t, s, history)
	contributeAll(t, s)
	corrupt := func() {
		if err := st.Set(ctx, nsRecentQueries, "analytics", "{not json"); err != nil {
			t.Fatalf("Set: %v", err)
		}
	}
	corrupt()

	// When: Athena contributes to the map
	got := contributeAll(t, s)

	// Then: the entry was rebuilt from the executions, and the map is whole
	assertContribution(t, got, expectedContribution(s.clk.Now(), history))

	// Given: the entry corrupted again, and then a query run in analytics
	corrupt()
	later := QueryExecution{QueryExecutionId: "q-later", WorkGroup: "analytics", Query: "SELECT 1",
		QueryExecutionContext: QueryExecutionContext{Database: "db0"},
		ResultConfiguration:   ResultConfiguration{OutputLocation: "s3://results-0/q-later.csv"},
		Status:                QueryExecutionStatus{State: stateSucceeded, SubmissionDateTime: float64(s.clk.Now().UnixMilli()) / 1000}}
	history = append(history, later)
	storeHistory(t, s, []QueryExecution{later})

	// When: Athena contributes to the map
	got = contributeAll(t, s)

	// Then: the entry was not replaced by one knowing only the new query,
	// but rebuilt, and the map is whole again
	assertContribution(t, got, expectedContribution(s.clk.Now(), history))
}

func TestDeleteWorkGroup_recursiveDropsItsRecentQueries(t *testing.T) {
	// Given: a workgroup that has run a query, drawn on the map
	s, _ := newTestService(t)
	ctx := context.Background()
	wg := &workGroupRecord{WorkGroup: WorkGroup{Name: "scratch", State: workGroupEnabled}}
	if err := s.store.putWorkGroup(ctx, wg); err != nil {
		t.Fatalf("putWorkGroup: %v", err)
	}
	putExecution(t, s, QueryExecution{QueryExecutionId: "q-1", WorkGroup: "scratch", Query: "SELECT 1",
		Status: QueryExecutionStatus{State: stateSucceeded}}, time.Minute)
	contributeAthena(t, s)

	// When: it is deleted with its contents, and made again
	recursive := true
	_, aerr := s.deleteWorkGroupTyped(ctx, &deleteWorkGroupReq{WorkGroup: "scratch", RecursiveDeleteOption: &recursive})
	mustOK(t, "DeleteWorkGroup", aerr)
	if err := s.store.putWorkGroup(ctx, wg); err != nil {
		t.Fatalf("putWorkGroup: %v", err)
	}

	// Then: the new workgroup's node lists none of the old one's runs
	resp := contributeAthena(t, s)
	for _, n := range resp.Nodes {
		if n.Label == "scratch" && len(n.RecentQueries) != 0 {
			t.Errorf("recent runs = %+v, want none", n.RecentQueries)
		}
	}
}

// indexWriteFailingStore fails every write of a recent-queries entry while
// failing is set.
type indexWriteFailingStore struct {
	state.Store
	failing atomic.Bool
}

func (f *indexWriteFailingStore) Set(ctx context.Context, ns, key, value string) error {
	if ns == nsRecentQueries && f.failing.Load() {
		return fmt.Errorf("disk full")
	}
	return f.Store.Set(ctx, ns, key, value)
}

func TestPutQuery_indexWriteFailureIsRebuiltNotReturned(t *testing.T) {
	// Given: a drawn map, then a store that cannot write recent-queries entries
	st := &indexWriteFailingStore{Store: state.NewMemoryStore()}
	s := newTestServiceOn(t, st)
	putHistoryWorkGroups(t, s)
	contributeAll(t, s)
	st.failing.Store(true)

	// When: executions are stored
	history := longHistory(s.clk.Now(), 30)
	storeHistory(t, s, history) // fails the test if putQuery returns an error

	// Then: once the store recovers, the map shows them all
	st.failing.Store(false)
	assertContribution(t, contributeAll(t, s), expectedContribution(s.clk.Now(), history))
}
