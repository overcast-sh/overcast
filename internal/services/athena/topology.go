package athena

// topology.go — Athena's part of the system map: a node per workgroup with
// its latest executions, an edge to the bucket its results go to and, for a
// while after a query, an edge to each catalog database it read.

import (
	"cmp"
	"context"
	"math"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/overcast-sh/overcast/internal/serviceutil"
	"github.com/overcast-sh/overcast/internal/topology"
)

const (
	// topologyRecentRuns is how many executions a workgroup node lists.
	topologyRecentRuns = 3
	// topologyQueryWindow is how long a query keeps its workgroup's edge to
	// what it read. The edge records what ran, not a configured connection,
	// which the map's legend says ("recent").
	topologyQueryWindow = 15 * time.Minute
	// topologySnippetRunes is how much of a query's SQL a node row shows.
	topologySnippetRunes = 120
)

// ContributeTopology implements topology.Contributor. Workgroups are not
// partitioned by region here, so every node is in the default region. The
// built-in primary workgroup is drawn only once it has run something: every
// account has it, and an unused one says nothing about the stack.
func (s *Service) ContributeTopology(ctx context.Context, g *topology.Graph) error {
	workGroups, err := s.store.listWorkGroups(ctx)
	if err != nil {
		return err
	}
	// TODO(priority:P2): this decodes every stored execution on each map
	// fetch, and a query's state changes each refetch the map. Nothing prunes
	// executions yet; read only the newest per workgroup once they are
	// indexed by submission time.
	queries, err := s.store.listQueries(ctx)
	if err != nil {
		return err
	}
	runs := executionsByWorkGroup(queries)
	configs := make(map[string]*WorkGroupConfiguration, len(workGroups))
	for _, wg := range workGroups {
		configs[wg.Name] = wg.Configuration
	}

	region := s.cfg.Region
	engine := engineAttention(s.EngineStatus().State)
	since := s.clk.Now().Add(-topologyQueryWindow)
	for _, name := range drawnWorkGroups(configs, runs) {
		wgRuns := runs[name]
		node := topology.ID(region, serviceName, name)
		g.AddNode(topology.Node{
			ID:            topology.NodeID(region, serviceName, name),
			Service:       serviceName,
			Label:         name,
			Region:        region,
			RecentQueries: queryRuns(wgRuns[:min(len(wgRuns), topologyRecentRuns)]),
			EngineState:   engine,
		}, topology.CFN(region, "AWS::Athena::WorkGroup", name))
		if bucket, _, ok := serviceutil.SplitS3URI(resultLocation(configs[name], wgRuns)); ok {
			g.AddLink(topology.Link{
				Source: node, Target: topology.ID(region, "s3", bucket).AnyRegion(),
				IDPrefix: "query-results", Type: "query-results",
			})
		}
		for _, target := range readTargets(region, wgRuns, since) {
			g.AddLink(topology.Link{Source: node, Target: target, IDPrefix: "queries", Type: "queries"})
		}
	}
	return nil
}

// executionsByWorkGroup groups executions by workgroup, newest first.
func executionsByWorkGroup(queries []*QueryExecution) map[string][]*QueryExecution {
	out := make(map[string][]*QueryExecution)
	for _, qe := range queries {
		out[qe.WorkGroup] = append(out[qe.WorkGroup], qe)
	}
	for _, list := range out {
		slices.SortFunc(list, func(a, b *QueryExecution) int {
			return cmp.Compare(b.Status.SubmissionDateTime, a.Status.SubmissionDateTime)
		})
	}
	return out
}

// drawnWorkGroups is every workgroup the map shows, sorted: each one that
// exists, less primary while it has run nothing. primary is seeded lazily,
// so it can have executions before it has a record.
func drawnWorkGroups(configs map[string]*WorkGroupConfiguration, runs map[string][]*QueryExecution) []string {
	names := make([]string, 0, len(configs)+1)
	for name := range configs {
		if name != primaryWorkGroup {
			names = append(names, name)
		}
	}
	if len(runs[primaryWorkGroup]) > 0 {
		names = append(names, primaryWorkGroup)
	}
	slices.Sort(names)
	return names
}

// engineAttention is the engine state a workgroup node shows as a chip: any
// state in which a query cannot run on the engine straight away. A stopped
// engine is not one — the next query starts it, which is normal.
func engineAttention(state string) string {
	if state == EngineReady || state == EngineStopped {
		return ""
	}
	return state
}

func queryRuns(executions []*QueryExecution) []topology.QueryRun {
	out := make([]topology.QueryRun, len(executions))
	for i, qe := range executions {
		out[i] = topology.QueryRun{
			ID:          qe.QueryExecutionId,
			State:       qe.Status.State,
			Query:       sqlSnippet(qe.Query),
			SubmittedAt: unixMillis(qe.Status.SubmissionDateTime),
			CompletedAt: unixMillis(qe.Status.CompletionDateTime),
		}
	}
	return out
}

// sqlSnippet is the start of a query on one line: runs of whitespace,
// newlines included, collapse to a space.
func sqlSnippet(query string) string {
	s := strings.Join(strings.Fields(query), " ")
	if utf8.RuneCountInString(s) <= topologySnippetRunes {
		return s
	}
	return string([]rune(s)[:topologySnippetRunes-1]) + "…"
}

// unixMillis converts an AWS epoch-seconds timestamp to Unix milliseconds.
func unixMillis(seconds float64) int64 {
	return int64(math.Round(seconds * 1000))
}

// resultLocation is where the workgroup's results go: its own location when
// it enforces it, else where the latest query wrote them, else — before any
// query has run — the location it is configured with.
func resultLocation(cfg *WorkGroupConfiguration, newestFirst []*QueryExecution) string {
	var configured string
	if cfg != nil && cfg.ResultConfiguration != nil {
		configured = cfg.ResultConfiguration.OutputLocation
	}
	if cfg != nil && isTrue(cfg.EnforceWorkGroupConfiguration) && configured != "" {
		return configured
	}
	if len(newestFirst) > 0 {
		if loc := newestFirst[0].ResultConfiguration.OutputLocation; loc != "" {
			return loc
		}
	}
	return configured
}

// readTargets is each catalog database the workgroup's queries submitted
// since since resolve against — the query context's, and any a DDL statement
// names — as the node it is on the map: a Glue database, or for a table
// bucket's catalog, the S3 Tables bucket. The tables a query the engine runs
// reads are not parsed out of its SQL, so they add nothing.
func readTargets(region string, newestFirst []*QueryExecution, since time.Time) []topology.Ref {
	var out []topology.Ref
	seen := make(map[topology.Ref]bool)
	add := func(r topology.Ref) {
		if !seen[r] {
			seen[r] = true
			out = append(out, r)
		}
	}
	cutoff := float64(since.UnixMilli()) / 1000
	for _, qe := range newestFirst {
		if qe.Status.SubmissionDateTime < cutoff {
			break
		}
		catalog := qe.QueryExecutionContext.Catalog
		if bucket, ok := tableBucketOf(catalog); ok {
			add(topology.ID(region, "s3tables", bucket))
			continue
		}
		if catalog != "" && !strings.EqualFold(catalog, awsDataCatalog) {
			continue // another catalog, which is not on the map
		}
		database := orDefault(qe.QueryExecutionContext.Database, defaultDatabase)
		add(topology.ID(region, "glue", database))
		for _, table := range statementTables(qe.Query, database) {
			db, _, _ := strings.Cut(table, ".")
			add(topology.ID(region, "glue", db))
		}
	}
	return out
}

// tableBucketOf is the table bucket a "s3tablescatalog/<bucket>" catalog
// name names.
func tableBucketOf(catalog string) (string, bool) {
	if !isS3TablesCatalog(catalog) {
		return "", false
	}
	_, bucket, _ := strings.Cut(catalog, "/")
	return bucket, true
}
