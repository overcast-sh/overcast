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
//
// Executions are read through each workgroup's recent-queries entry (see
// recent_queries.go), not decoded one by one, so the map stays cheap however
// long the query history grows.
func (s *Service) ContributeTopology(ctx context.Context, g *topology.Graph) error {
	workGroups, err := s.store.listWorkGroups(ctx)
	if err != nil {
		return err
	}
	recent, err := s.store.recentQueries(ctx)
	if err != nil {
		return err
	}
	configs := make(map[string]*WorkGroupConfiguration, len(workGroups))
	for _, wg := range workGroups {
		configs[wg.Name] = wg.Configuration
	}

	region := s.cfg.Region
	engine := engineAttention(s.EngineStatus().State)
	since := s.clk.Now().Add(-topologyQueryWindow)
	for _, name := range drawnWorkGroups(configs, recent) {
		wgRecent := recent[name]
		node := topology.ID(region, serviceName, name)
		g.AddNode(topology.Node{
			ID:            topology.NodeID(region, serviceName, name),
			Service:       serviceName,
			Label:         name,
			Region:        region,
			RecentQueries: wgRecent.queryRuns(),
			EngineState:   engine,
		}, topology.CFN(region, "AWS::Athena::WorkGroup", name))
		if bucket, _, ok := serviceutil.SplitS3URI(resultLocation(configs[name], wgRecent.lastOutputLocation())); ok {
			g.AddLink(topology.Link{
				Source: node, Target: topology.ID(region, "s3", bucket).AnyRegion(),
				IDPrefix: "query-results", Type: "query-results",
			})
		}
		for _, target := range wgRecent.readSince(region, since) {
			g.AddLink(topology.Link{Source: node, Target: target, IDPrefix: "queries", Type: "queries"})
		}
	}
	return nil
}

// drawnWorkGroups is every workgroup the map shows, sorted: each one that
// exists, less primary while it has run nothing. primary is seeded lazily,
// so it can have executions before it has a record.
func drawnWorkGroups(configs map[string]*WorkGroupConfiguration, recent map[string]recentQueries) []string {
	names := make([]string, 0, len(configs)+1)
	for name := range configs {
		if name != primaryWorkGroup {
			names = append(names, name)
		}
	}
	if len(recent[primaryWorkGroup].Runs) > 0 {
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
func resultLocation(cfg *WorkGroupConfiguration, lastOutput string) string {
	var configured string
	if cfg != nil && cfg.ResultConfiguration != nil {
		configured = cfg.ResultConfiguration.OutputLocation
	}
	if cfg != nil && isTrue(cfg.EnforceWorkGroupConfiguration) && configured != "" {
		return configured
	}
	return cmp.Or(lastOutput, configured)
}
