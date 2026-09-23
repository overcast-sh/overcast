package glue

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"go.uber.org/zap"

	"github.com/overcast-sh/overcast/internal/serviceutil"
	"github.com/overcast-sh/overcast/internal/state"
)

// Namespaces. Keys are slash-delimited: a database by name, a table by
// "<db>/<table>", and a table's partitions and archived versions under
// "<db>/<table>/", so one prefix scan finds everything a table owns.
const (
	nsDatabases     = "glue:databases"
	nsTables        = "glue:tables"
	nsPartitions    = "glue:partitions"
	nsTableVersions = "glue:table-versions"
)

// databaseRecord is a Database as persisted: the wire shape plus its tags.
type databaseRecord struct {
	Database
	Tags map[string]string `json:"overcastTags,omitempty"`
}

func (d *databaseRecord) GetTags() map[string]string  { return d.Tags }
func (d *databaseRecord) SetTags(t map[string]string) { d.Tags = t }

// tableRecord is a Table as persisted: the wire shape plus its tags.
//
// Records written before #2064 carry only Name, DatabaseName, Description,
// TableType and CatalogId. They decode into this shape unchanged — every
// member added since is optional — and normalize gives them the version ID a
// table has always had on AWS.
type tableRecord struct {
	Table
	Tags map[string]string `json:"overcastTags,omitempty"`
}

func (t *tableRecord) GetTags() map[string]string     { return t.Tags }
func (t *tableRecord) SetTags(tags map[string]string) { t.Tags = tags }

// initialVersionID is the VersionId AWS gives a newly created table.
const initialVersionID = "0"

func (t *Table) normalize() {
	if t.VersionId == "" {
		t.VersionId = initialVersionID
	}
	if t.PartitionKeys == nil {
		t.PartitionKeys = []Column{}
	}
}

type glueStore struct {
	store state.Store
	log   *serviceutil.ServiceLogger
}

func newGlueStore(s state.Store, log *serviceutil.ServiceLogger) *glueStore {
	return &glueStore{store: s, log: log}
}

// normName folds a database or table name the way Glue does ("For Hive
// compatibility, this name is entirely lowercase").
func normName(name string) string { return strings.ToLower(name) }

func tableKey(dbName, tableName string) string { return dbName + "/" + tableName }

// tablePrefix is the key prefix under which a table's partitions and archived
// versions live.
func tablePrefix(dbName, tableName string) string { return tableKey(dbName, tableName) + "/" }

// partitionKey escapes each value, since a partition value may itself hold a
// slash and the key must stay unambiguous.
func partitionKey(dbName, tableName string, values []string) string {
	var b strings.Builder
	b.WriteString(tablePrefix(dbName, tableName))
	for i, v := range values {
		if i > 0 {
			b.WriteByte('/')
		}
		b.WriteString(url.PathEscape(v))
	}
	return b.String()
}

func versionKey(dbName, tableName, versionID string) string {
	return tablePrefix(dbName, tableName) + versionID
}

// put marshals v into ns/key.
func (s *glueStore) put(ctx context.Context, ns, key string, v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("glue: marshal %s %q: %w", ns, key, err)
	}
	if err := s.store.Set(ctx, ns, key, string(raw)); err != nil {
		return fmt.Errorf("glue: put %s %q: %w", ns, key, err)
	}
	return nil
}

// get reads ns/key into v. A record that cannot be decoded is reported as not
// found — a named read of one corrupt record answers like AWS does for a
// resource that is not there — and logged so the gap is visible.
func (s *glueStore) get(ctx context.Context, ns, key string, v any) (bool, error) {
	raw, found, err := s.store.Get(ctx, ns, key)
	if err != nil {
		return false, fmt.Errorf("glue: get %s %q: %w", ns, key, err)
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

// scan decodes every record under ns/prefix, skipping (and logging) any that
// fail, so one corrupt record never fails a list.
func scan[T any](ctx context.Context, s *glueStore, ns, prefix string) ([]*T, error) {
	pairs, err := s.store.Scan(ctx, ns, prefix)
	if err != nil {
		return nil, fmt.Errorf("glue: scan %s %q: %w", ns, prefix, err)
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

// deletePrefix removes every key under ns/prefix, decodable or not: a cascade
// must not leave a corrupt child behind for a later same-named parent.
func (s *glueStore) deletePrefix(ctx context.Context, ns, prefix string) error {
	pairs, err := s.store.Scan(ctx, ns, prefix)
	if err != nil {
		return fmt.Errorf("glue: scan %s %q: %w", ns, prefix, err)
	}
	for _, kv := range pairs {
		if err := s.store.Delete(ctx, ns, kv.Key); err != nil {
			return fmt.Errorf("glue: delete %s %q: %w", ns, kv.Key, err)
		}
	}
	return nil
}

// ─── Databases ─────────────────────────────────────────────────

func (s *glueStore) putDatabase(ctx context.Context, db *databaseRecord) error {
	return s.put(ctx, nsDatabases, db.Name, db)
}

func (s *glueStore) getDatabase(ctx context.Context, name string) (*databaseRecord, bool, error) {
	var db databaseRecord
	found, err := s.get(ctx, nsDatabases, name, &db)
	if !found || err != nil {
		return nil, false, err
	}
	return &db, true, nil
}

// listDatabases returns every decodable database, sorted by name so that
// pagination is stable.
func (s *glueStore) listDatabases(ctx context.Context) ([]*databaseRecord, error) {
	out, err := scan[databaseRecord](ctx, s, nsDatabases, "")
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// deleteDatabase removes the database and, as AWS does, everything in it:
// its tables and each table's partitions and archived versions.
func (s *glueStore) deleteDatabase(ctx context.Context, name string) error {
	prefix := name + "/"
	for _, ns := range []string{nsPartitions, nsTableVersions, nsTables} {
		if err := s.deletePrefix(ctx, ns, prefix); err != nil {
			return err
		}
	}
	if err := s.store.Delete(ctx, nsDatabases, name); err != nil {
		return fmt.Errorf("glue: delete database %q: %w", name, err)
	}
	return nil
}

// ─── Tables ────────────────────────────────────────────────────

func (s *glueStore) putTable(ctx context.Context, t *tableRecord) error {
	return s.put(ctx, nsTables, tableKey(t.DatabaseName, t.Name), t)
}

func (s *glueStore) getTable(ctx context.Context, dbName, tableName string) (*tableRecord, bool, error) {
	var t tableRecord
	found, err := s.get(ctx, nsTables, tableKey(dbName, tableName), &t)
	if !found || err != nil {
		return nil, false, err
	}
	t.normalize()
	return &t, true, nil
}

// listTables returns the database's decodable tables, sorted by name.
func (s *glueStore) listTables(ctx context.Context, dbName string) ([]*tableRecord, error) {
	out, err := scan[tableRecord](ctx, s, nsTables, dbName+"/")
	if err != nil {
		return nil, err
	}
	kept := out[:0]
	for _, t := range out {
		// A table key is exactly "<db>/<table>"; the prefix also matches
		// nothing else in this namespace, but check rather than assume.
		if t.DatabaseName != dbName {
			continue
		}
		t.normalize()
		kept = append(kept, t)
	}
	sort.Slice(kept, func(i, j int) bool { return kept[i].Name < kept[j].Name })
	return kept, nil
}

// deleteTable removes the table with its partitions and archived versions.
func (s *glueStore) deleteTable(ctx context.Context, dbName, tableName string) error {
	prefix := tablePrefix(dbName, tableName)
	for _, ns := range []string{nsPartitions, nsTableVersions} {
		if err := s.deletePrefix(ctx, ns, prefix); err != nil {
			return err
		}
	}
	if err := s.store.Delete(ctx, nsTables, tableKey(dbName, tableName)); err != nil {
		return fmt.Errorf("glue: delete table %q: %w", tableKey(dbName, tableName), err)
	}
	return nil
}

// ─── Table versions ────────────────────────────────────────────

func (s *glueStore) putTableVersion(ctx context.Context, t *Table) error {
	return s.put(ctx, nsTableVersions, versionKey(t.DatabaseName, t.Name, t.VersionId), t)
}

func (s *glueStore) getTableVersion(ctx context.Context, dbName, tableName, versionID string) (*Table, bool, error) {
	var t Table
	found, err := s.get(ctx, nsTableVersions, versionKey(dbName, tableName, versionID), &t)
	if !found || err != nil {
		return nil, false, err
	}
	t.normalize()
	return &t, true, nil
}

// listTableVersions returns the table's archived versions, newest first.
func (s *glueStore) listTableVersions(ctx context.Context, dbName, tableName string) ([]*Table, error) {
	out, err := scan[Table](ctx, s, nsTableVersions, tablePrefix(dbName, tableName))
	if err != nil {
		return nil, err
	}
	for _, t := range out {
		t.normalize()
	}
	sort.Slice(out, func(i, j int) bool { return versionLess(out[j].VersionId, out[i].VersionId) })
	return out, nil
}

func (s *glueStore) deleteTableVersion(ctx context.Context, dbName, tableName, versionID string) error {
	key := versionKey(dbName, tableName, versionID)
	if err := s.store.Delete(ctx, nsTableVersions, key); err != nil {
		return fmt.Errorf("glue: delete table version %q: %w", key, err)
	}
	return nil
}

// versionLess orders version IDs numerically. AWS's are decimal integers;
// anything else sorts after them, lexically.
func versionLess(a, b string) bool {
	ai, aerr := strconv.ParseInt(a, 10, 64)
	bi, berr := strconv.ParseInt(b, 10, 64)
	switch {
	case aerr == nil && berr == nil:
		return ai < bi
	case aerr == nil:
		return true
	case berr == nil:
		return false
	default:
		return a < b
	}
}

// ─── Partitions ────────────────────────────────────────────────

func (s *glueStore) putPartition(ctx context.Context, p *Partition) error {
	return s.put(ctx, nsPartitions, partitionKey(p.DatabaseName, p.TableName, p.Values), p)
}

func (s *glueStore) getPartition(ctx context.Context, dbName, tableName string, values []string) (*Partition, bool, error) {
	var p Partition
	found, err := s.get(ctx, nsPartitions, partitionKey(dbName, tableName, values), &p)
	if !found || err != nil {
		return nil, false, err
	}
	return &p, true, nil
}

// listPartitions returns the table's decodable partitions in key order,
// which keeps pagination stable.
func (s *glueStore) listPartitions(ctx context.Context, dbName, tableName string) ([]*Partition, error) {
	out, err := scan[Partition](ctx, s, nsPartitions, tablePrefix(dbName, tableName))
	if err != nil {
		return nil, err
	}
	sort.SliceStable(out, func(i, j int) bool {
		return partitionKey(dbName, tableName, out[i].Values) < partitionKey(dbName, tableName, out[j].Values)
	})
	return out, nil
}

func (s *glueStore) deletePartition(ctx context.Context, dbName, tableName string, values []string) error {
	key := partitionKey(dbName, tableName, values)
	if err := s.store.Delete(ctx, nsPartitions, key); err != nil {
		return fmt.Errorf("glue: delete partition %q: %w", key, err)
	}
	return nil
}
