package athena

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"go.uber.org/zap"

	"github.com/overcast-sh/overcast/internal/serviceutil"
	"github.com/overcast-sh/overcast/internal/state"
)

// Namespaces. Workgroups, data catalogs and named queries are keyed by their
// name or ID; a prepared statement by "<workgroup>/<name>", which is
// unambiguous because a workgroup name cannot hold a slash. Idempotency
// tokens are keyed "<operation>/<token>", since a token only has to be
// unique per operation. Each workgroup's recent-queries entry (see
// recent_queries.go) is keyed by the workgroup's name.
const (
	nsWorkGroups         = "athena:workgroups"
	nsQueries            = "athena:queries"
	nsNamedQueries       = "athena:named-queries"
	nsPreparedStatements = "athena:prepared-statements"
	nsDataCatalogs       = "athena:data-catalogs"
	nsIdempotency        = "athena:idempotency"
	nsRecentQueries      = "athena:recent-queries"
)

// workGroupRecord is a WorkGroup as persisted: the wire shape plus its tags.
// Tags are kept off GetWorkGroup and ListWorkGroups (the model keeps them off
// WorkGroup) and exposed only through the tag operations.
type workGroupRecord struct {
	WorkGroup
	Tags map[string]string `json:"overcastTags,omitempty"`
}

func (wg *workGroupRecord) GetTags() map[string]string  { return wg.Tags }
func (wg *workGroupRecord) SetTags(t map[string]string) { wg.Tags = t }

// dataCatalogRecord is a DataCatalog as persisted, with its tags.
type dataCatalogRecord struct {
	DataCatalog
	Tags map[string]string `json:"overcastTags,omitempty"`
}

func (c *dataCatalogRecord) GetTags() map[string]string  { return c.Tags }
func (c *dataCatalogRecord) SetTags(t map[string]string) { c.Tags = t }

// idempotencyRecord remembers what a client request token created, and a
// fingerprint of the request that created it, so a retry returns the same
// resource and a reuse with different parameters is refused.
type idempotencyRecord struct {
	ResourceID  string `json:"resourceId"`
	Fingerprint string `json:"fingerprint"`
}

type athenaStore struct {
	store state.Store
	log   *serviceutil.ServiceLogger

	// recentMu serialises each read-modify-write of a recent-queries entry,
	// and a rebuild of them all against any of those.
	recentMu sync.Mutex
	// recentFresh is whether the recent-queries entries account for every
	// stored execution. It is false until this process first rebuilds them —
	// a previous one may have stopped between storing an execution and its
	// entry, or predate the entries — and again once one cannot be read.
	// Guarded by recentMu.
	recentFresh bool
}

func newAthenaStore(s state.Store, log *serviceutil.ServiceLogger) *athenaStore {
	return &athenaStore{store: s, log: log}
}

func preparedStatementKey(workGroup, name string) string { return workGroup + "/" + name }

func idempotencyKey(operation, token string) string { return operation + "/" + token }

// put marshals v into ns/key.
func (s *athenaStore) put(ctx context.Context, ns, key string, v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("athena: marshal %s %q: %w", ns, key, err)
	}
	if err := s.store.Set(ctx, ns, key, string(raw)); err != nil {
		return fmt.Errorf("athena: put %s %q: %w", ns, key, err)
	}
	return nil
}

// get reads ns/key into v. A record that cannot be decoded reads as not
// found — a named read of one corrupt record answers as AWS does for a
// resource that is not there — and is logged so the gap is visible.
func (s *athenaStore) get(ctx context.Context, ns, key string, v any) (bool, error) {
	raw, found, err := s.store.Get(ctx, ns, key)
	if err != nil {
		return false, fmt.Errorf("athena: get %s %q: %w", ns, key, err)
	}
	if !found {
		return false, nil
	}
	return s.decode(ns, key, raw, v), nil
}

// decode unmarshals the record at ns/key into v, logging and reporting false
// when it cannot be decoded.
func (s *athenaStore) decode(ns, key, raw string, v any) bool {
	if err := json.Unmarshal([]byte(raw), v); err != nil {
		s.log.Warn("skipping malformed record", zap.String("namespace", ns), zap.String("key", key), zap.Error(err))
		return false
	}
	return true
}

func (s *athenaStore) delete(ctx context.Context, ns, key string) error {
	if err := s.store.Delete(ctx, ns, key); err != nil {
		return fmt.Errorf("athena: delete %s %q: %w", ns, key, err)
	}
	return nil
}

// scan decodes every record under ns/prefix, skipping (and logging) any that
// fail, so one corrupt record never fails a list.
func scan[T any](ctx context.Context, s *athenaStore, ns, prefix string) ([]*T, error) {
	out, _, err := scanCounting[T](ctx, s, ns, prefix)
	return out, err
}

// scanCounting is scan, also reporting how many records it skipped.
func scanCounting[T any](ctx context.Context, s *athenaStore, ns, prefix string) ([]*T, int, error) {
	pairs, err := s.store.Scan(ctx, ns, prefix)
	if err != nil {
		return nil, 0, fmt.Errorf("athena: scan %s %q: %w", ns, prefix, err)
	}
	out := make([]*T, 0, len(pairs))
	for _, kv := range pairs {
		var v T
		if s.decode(ns, kv.Key, kv.Value, &v) {
			out = append(out, &v)
		}
	}
	return out, len(pairs) - len(out), nil
}

// getRecord is get for a typed record, returning nil when it is absent.
func getRecord[T any](ctx context.Context, s *athenaStore, ns, key string) (*T, error) {
	var v T
	found, err := s.get(ctx, ns, key, &v)
	if !found || err != nil {
		return nil, err
	}
	return &v, nil
}

// ─── Typed accessors ──────────────────────────────────────────

func (s *athenaStore) putWorkGroup(ctx context.Context, wg *workGroupRecord) error {
	return s.put(ctx, nsWorkGroups, wg.Name, wg)
}

// getWorkGroup reads one workgroup, normalized; see WorkGroup.normalize.
func (s *athenaStore) getWorkGroup(ctx context.Context, name string) (*workGroupRecord, error) {
	wg, err := getRecord[workGroupRecord](ctx, s, nsWorkGroups, name)
	if wg != nil {
		wg.normalize()
	}
	return wg, err
}

func (s *athenaStore) listWorkGroups(ctx context.Context) ([]*workGroupRecord, error) {
	out, err := scan[workGroupRecord](ctx, s, nsWorkGroups, "")
	for _, wg := range out {
		wg.normalize()
	}
	return out, err
}

// putQuery stores an execution, then folds it into its workgroup's
// recent-queries entry. The entry follows the record and is derived from it,
// so only the record's write can fail the call: an entry that could not be
// kept up to date is rebuilt on the next map fetch instead.
func (s *athenaStore) putQuery(ctx context.Context, qe *QueryExecution) error {
	if err := s.put(ctx, nsQueries, qe.QueryExecutionId, qe); err != nil {
		return err
	}
	s.recordRecentQuery(ctx, qe)
	return nil
}

// getQuery reads one execution. Records written before workgroups were
// tracked carry no WorkGroup; they ran in primary, as every query without
// one does.
func (s *athenaStore) getQuery(ctx context.Context, id string) (*QueryExecution, error) {
	qe, err := getRecord[QueryExecution](ctx, s, nsQueries, id)
	if qe != nil {
		qe.normalize()
	}
	return qe, err
}

func (s *athenaStore) listQueries(ctx context.Context) ([]*QueryExecution, error) {
	out, err := scan[QueryExecution](ctx, s, nsQueries, "")
	for _, qe := range out {
		qe.normalize()
	}
	return out, err
}

// recordRecentQuery folds a stored execution into its workgroup's
// recent-queries entry. An entry that cannot be read, or written, is left
// for the next rebuild rather than replaced by one that knows only this
// execution.
func (s *athenaStore) recordRecentQuery(ctx context.Context, qe *QueryExecution) {
	s.recentMu.Lock()
	defer s.recentMu.Unlock()
	workGroup := orPrimary(qe.WorkGroup)
	entry := &recentQueries{WorkGroup: workGroup}
	raw, found, err := s.store.Get(ctx, nsRecentQueries, workGroup)
	switch {
	case err != nil:
		s.recentStale(workGroup, err)
	case found && !s.decode(nsRecentQueries, workGroup, raw, entry):
		s.recentFresh = false
	default:
		entry.record(qe)
		if err := s.put(ctx, nsRecentQueries, workGroup, entry); err != nil {
			s.recentStale(workGroup, err)
		}
	}
}

// recentStale marks the recent-queries entries for a rebuild after a store
// error left workGroup's behind. Its caller holds recentMu.
func (s *athenaStore) recentStale(workGroup string, err error) {
	s.log.Warn("recent-queries entry left for a rebuild", zap.String("workGroup", workGroup), zap.Error(err))
	s.recentFresh = false
}

// recentQueries is every workgroup's recent-queries entry, by workgroup. The
// entries are rebuilt from the executions the first time this process reads
// them, and whenever one of them cannot be read.
func (s *athenaStore) recentQueries(ctx context.Context) (map[string]recentQueries, error) {
	s.recentMu.Lock()
	defer s.recentMu.Unlock()
	if !s.recentFresh {
		if err := s.rebuildRecentQueries(ctx); err != nil {
			return nil, err
		}
	}
	entries, skipped, err := scanCounting[recentQueries](ctx, s, nsRecentQueries, "")
	if err != nil {
		return nil, err
	}
	if skipped > 0 {
		if err := s.rebuildRecentQueries(ctx); err != nil {
			return nil, err
		}
		if entries, err = scan[recentQueries](ctx, s, nsRecentQueries, ""); err != nil {
			return nil, err
		}
	}
	out := make(map[string]recentQueries, len(entries))
	for _, e := range entries {
		out[e.WorkGroup] = *e
	}
	return out, nil
}

// rebuildRecentQueries replaces every recent-queries entry with one built
// from the stored executions — the one read of them all. Its caller holds
// recentMu.
func (s *athenaStore) rebuildRecentQueries(ctx context.Context) error {
	queries, err := s.listQueries(ctx)
	if err != nil {
		return err
	}
	entries := recentQueriesOf(queries)
	stale, err := s.store.List(ctx, nsRecentQueries, "")
	if err != nil {
		return fmt.Errorf("athena: list %s: %w", nsRecentQueries, err)
	}
	for _, key := range stale {
		if entries[key] == nil {
			if err := s.delete(ctx, nsRecentQueries, key); err != nil {
				return err
			}
		}
	}
	for key, entry := range entries {
		if err := s.put(ctx, nsRecentQueries, key, entry); err != nil {
			return err
		}
	}
	s.recentFresh = true
	return nil
}

// forgetRecentQueries drops a workgroup's recent-queries entry once its
// executions have been deleted with it. The entries are rebuilt on the next
// map fetch too: a state change that raced the delete may have stored an
// execution again, and the executions are the truth.
func (s *athenaStore) forgetRecentQueries(ctx context.Context, workGroup string) {
	s.recentMu.Lock()
	defer s.recentMu.Unlock()
	if err := s.delete(ctx, nsRecentQueries, workGroup); err != nil {
		s.log.Warn("recent-queries entry left for a rebuild", zap.String("workGroup", workGroup), zap.Error(err))
	}
	s.recentFresh = false
}

func (s *athenaStore) putNamedQuery(ctx context.Context, nq *NamedQuery) error {
	return s.put(ctx, nsNamedQueries, nq.NamedQueryId, nq)
}

func (s *athenaStore) getNamedQuery(ctx context.Context, id string) (*NamedQuery, error) {
	return getRecord[NamedQuery](ctx, s, nsNamedQueries, id)
}

func (s *athenaStore) putPreparedStatement(ctx context.Context, ps *PreparedStatement) error {
	return s.put(ctx, nsPreparedStatements, preparedStatementKey(ps.WorkGroupName, ps.StatementName), ps)
}

func (s *athenaStore) getPreparedStatement(ctx context.Context, workGroup, name string) (*PreparedStatement, error) {
	return getRecord[PreparedStatement](ctx, s, nsPreparedStatements, preparedStatementKey(workGroup, name))
}

func (s *athenaStore) putDataCatalog(ctx context.Context, c *dataCatalogRecord) error {
	return s.put(ctx, nsDataCatalogs, c.Name, c)
}

func (s *athenaStore) getDataCatalog(ctx context.Context, name string) (*dataCatalogRecord, error) {
	return getRecord[dataCatalogRecord](ctx, s, nsDataCatalogs, name)
}

func (s *athenaStore) getIdempotency(ctx context.Context, operation, token string) (*idempotencyRecord, error) {
	return getRecord[idempotencyRecord](ctx, s, nsIdempotency, idempotencyKey(operation, token))
}

func (s *athenaStore) putIdempotency(ctx context.Context, operation, token string, rec *idempotencyRecord) error {
	return s.put(ctx, nsIdempotency, idempotencyKey(operation, token), rec)
}
