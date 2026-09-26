package athena

// recent_queries.go — what the system map shows of a workgroup's executions,
// kept up to date as each execution is stored (see athenaStore.putQuery), so
// a map fetch reads one small entry per workgroup instead of decoding every
// execution ever run. An entry holds the workgroup's newest runs and each
// catalog database or table bucket its queries read, with when one last did:
// both bounded, by topologyRecentRuns and by the distinct targets read within
// topologyQueryWindow of the newest query.

import (
	"cmp"
	"slices"
	"strings"
	"time"

	"github.com/overcast-sh/overcast/internal/topology"
)

// recentQueries is one workgroup's entry.
type recentQueries struct {
	WorkGroup string `json:"workGroup"`
	// Runs are its newest executions, newest first.
	Runs []recentRun `json:"runs"`
	// Reads are what its queries read, most recently read first.
	Reads []recentRead `json:"reads"`
}

// recentRun is an execution as a node row shows it, and where it wrote its
// results, which the workgroup's results edge follows.
type recentRun struct {
	topology.QueryRun
	OutputLocation string `json:"outputLocation,omitempty"`
}

// readTarget is a node a query reads: a Glue database or an S3 Tables table
// bucket, by map kind and name.
type readTarget struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
}

// recentRead is a target and the submission time (AWS epoch seconds) of the
// newest query that read it.
type recentRead struct {
	readTarget
	At float64 `json:"at"`
}

// record folds a stored execution — new, or a state change of one already
// seen — into the entry.
func (r *recentQueries) record(qe *QueryExecution) {
	r.recordRun(qe)
	r.recordReads(qe.Status.SubmissionDateTime, queryReads(qe))
}

// recordRun keeps the execution's latest state if it is among the newest.
// Submission times never change and executions are only deleted with their
// workgroup, so one that falls off the end never belongs back.
func (r *recentQueries) recordRun(qe *QueryExecution) {
	run := recentRun{QueryRun: queryRun(qe), OutputLocation: qe.ResultConfiguration.OutputLocation}
	runs := slices.DeleteFunc(r.Runs, func(x recentRun) bool { return x.ID == run.ID })
	runs = append(runs, run)
	slices.SortFunc(runs, func(a, b recentRun) int {
		return cmp.Or(cmp.Compare(b.SubmittedAt, a.SubmittedAt), strings.Compare(a.ID, b.ID))
	})
	r.Runs = runs[:min(len(runs), topologyRecentRuns)]
}

// recordReads notes that a query submitted at at read targets, then drops
// each read too old to be drawn by the time the newest one no longer is.
func (r *recentQueries) recordReads(at float64, targets []readTarget) {
	for _, t := range targets {
		i := slices.IndexFunc(r.Reads, func(x recentRead) bool { return x.readTarget == t })
		switch {
		case i < 0:
			r.Reads = append(r.Reads, recentRead{readTarget: t, At: at})
		case at > r.Reads[i].At:
			r.Reads[i].At = at
		}
	}
	if len(r.Reads) == 0 {
		return
	}
	slices.SortFunc(r.Reads, func(a, b recentRead) int {
		return cmp.Or(cmp.Compare(b.At, a.At), strings.Compare(a.Kind, b.Kind), strings.Compare(a.Name, b.Name))
	})
	oldest := r.Reads[0].At - topologyQueryWindow.Seconds()
	r.Reads = slices.DeleteFunc(r.Reads, func(x recentRead) bool { return x.At < oldest })
}

// queryRuns is the entry's runs as its node lists them.
func (r recentQueries) queryRuns() []topology.QueryRun {
	out := make([]topology.QueryRun, len(r.Runs))
	for i, run := range r.Runs {
		out[i] = run.QueryRun
	}
	return out
}

// lastOutputLocation is where the newest execution wrote its results.
func (r recentQueries) lastOutputLocation() string {
	if len(r.Runs) == 0 {
		return ""
	}
	return r.Runs[0].OutputLocation
}

// readSince is each node the workgroup's queries submitted since since read.
func (r recentQueries) readSince(region string, since time.Time) []topology.Ref {
	cutoff := float64(since.UnixMilli()) / 1000
	var out []topology.Ref
	for _, read := range r.Reads {
		if read.At >= cutoff {
			out = append(out, topology.ID(region, read.Kind, read.Name))
		}
	}
	return out
}

// recentQueriesOf builds every workgroup's entry from its executions.
func recentQueriesOf(queries []*QueryExecution) map[string]*recentQueries {
	out := make(map[string]*recentQueries)
	for _, qe := range queries {
		entry := out[qe.WorkGroup]
		if entry == nil {
			entry = &recentQueries{WorkGroup: qe.WorkGroup}
			out[qe.WorkGroup] = entry
		}
		entry.record(qe)
	}
	return out
}

func queryRun(qe *QueryExecution) topology.QueryRun {
	return topology.QueryRun{
		ID:          qe.QueryExecutionId,
		State:       qe.Status.State,
		Query:       sqlSnippet(qe.Query),
		SubmittedAt: unixMillis(qe.Status.SubmissionDateTime),
		CompletedAt: unixMillis(qe.Status.CompletionDateTime),
	}
}

// queryReads is each catalog database a query resolves against — its query
// context's, and any a DDL statement names — or, for a table bucket's
// catalog, the S3 Tables bucket. The tables a query the engine runs reads
// are not parsed out of its SQL, so they add nothing, and a catalog that is
// not on the map adds nothing either.
func queryReads(qe *QueryExecution) []readTarget {
	catalog := qe.QueryExecutionContext.Catalog
	if bucket, ok := tableBucketOf(catalog); ok {
		return []readTarget{{Kind: "s3tables", Name: bucket}}
	}
	if catalog != "" && !strings.EqualFold(catalog, awsDataCatalog) {
		return nil
	}
	database := orDefault(qe.QueryExecutionContext.Database, defaultDatabase)
	out := []readTarget{{Kind: "glue", Name: database}}
	for _, table := range statementTables(qe.Query, database) {
		db, _, _ := strings.Cut(table, ".")
		out = append(out, readTarget{Kind: "glue", Name: db})
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
