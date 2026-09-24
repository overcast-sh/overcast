package athena

import (
	"context"
	"encoding/json"
	"fmt"

	"go.uber.org/zap"

	"github.com/overcast-sh/overcast/internal/serviceutil"
	"github.com/overcast-sh/overcast/internal/state"
)

// Namespaces. Workgroups, data catalogs and named queries are keyed by their
// name or ID; a prepared statement by "<workgroup>/<name>", which is
// unambiguous because a workgroup name cannot hold a slash. Idempotency
// tokens are keyed "<operation>/<token>", since a token only has to be
// unique per operation.
const (
	nsWorkGroups         = "athena:workgroups"
	nsQueries            = "athena:queries"
	nsNamedQueries       = "athena:named-queries"
	nsPreparedStatements = "athena:prepared-statements"
	nsDataCatalogs       = "athena:data-catalogs"
	nsIdempotency        = "athena:idempotency"
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
	if err := json.Unmarshal([]byte(raw), v); err != nil {
		s.log.Warn("skipping malformed record", zap.String("namespace", ns), zap.String("key", key), zap.Error(err))
		return false, nil
	}
	return true, nil
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
	pairs, err := s.store.Scan(ctx, ns, prefix)
	if err != nil {
		return nil, fmt.Errorf("athena: scan %s %q: %w", ns, prefix, err)
	}
	out := make([]*T, 0, len(pairs))
	for _, kv := range pairs {
		var v T
		if err := json.Unmarshal([]byte(kv.Value), &v); err != nil {
			s.log.Warn("skipping malformed record", zap.String("namespace", ns), zap.String("key", kv.Key), zap.Error(err))
			continue
		}
		out = append(out, &v)
	}
	return out, nil
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

func (s *athenaStore) putQuery(ctx context.Context, qe *QueryExecution) error {
	return s.put(ctx, nsQueries, qe.QueryExecutionId, qe)
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
