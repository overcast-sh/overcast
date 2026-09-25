package glue

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"

	"go.uber.org/zap"

	"github.com/overcast-sh/overcast/internal/serviceutil"
	"github.com/overcast-sh/overcast/internal/state"
)

// Namespaces. A database is keyed by its name; a table by "<db>/<table>", and
// a table's partitions and archived versions under "<db>/<table>/", so one
// prefix scan finds everything a table owns. Glue's NameString pattern allows
// "/" in a name, so every component is path-escaped: unescaped, table "b/t"
// in database "a" and table "t" in database "a/b" would share one key.
const (
	nsDatabases     = "glue:databases"
	nsTables        = "glue:tables"
	nsPartitions    = "glue:partitions"
	nsTableVersions = "glue:table-versions"
	// nsColumnStatistics holds column statistics under their table's prefix:
	// "<table prefix>t/<column>" for the table's own, and
	// "<table prefix>p/<values>/<column>" for a partition's, the values
	// escaped a second time so they form one segment.
	nsColumnStatistics = "glue:column-statistics"
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
// table has always had on AWS. Their keys used the names as written, in their
// original case and unescaped; migrateLegacyKeys moves them to the keys a
// lookup now computes.
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
	store    state.Store
	log      *serviceutil.ServiceLogger
	migrated serviceutil.LazyInit
}

func newGlueStore(s state.Store, log *serviceutil.ServiceLogger) *glueStore {
	return &glueStore{store: s, log: log}
}

// normName folds a database or table name the way Glue does ("For Hive
// compatibility, this name is entirely lowercase").
func normName(name string) string { return strings.ToLower(name) }

func esc(name string) string { return url.PathEscape(name) }

// databaseKey is a database's key: its name alone, unescaped, since it is the
// whole key in its own namespace and cannot be confused with another.
func databaseKey(name string) string { return name }

// databasePrefix is the key prefix under which a database's tables, and their
// partitions and versions, live.
func databasePrefix(dbName string) string { return esc(dbName) + "/" }

func tableKey(dbName, tableName string) string { return databasePrefix(dbName) + esc(tableName) }

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
		b.WriteString(esc(v))
	}
	return b.String()
}

func versionKey(dbName, tableName, versionID string) string {
	return tablePrefix(dbName, tableName) + esc(versionID)
}

// ready runs the one-time legacy key migration before the first access. It
// is lazy rather than done in New, which must not touch the store.
func (s *glueStore) ready(ctx context.Context) error {
	return s.migrated.Do(func() error { return s.migrateLegacyKeys(ctx) })
}

// migrateLegacyKeys moves records written before #2064 to the keys a lookup
// computes now: names folded to lowercase and path-escaped. Only databases
// and tables existed then. A record whose canonical key is already taken is
// left where it is and logged, never overwritten.
func (s *glueStore) migrateLegacyKeys(ctx context.Context) error {
	dbs, err := s.store.Scan(ctx, nsDatabases, "")
	if err != nil {
		return fmt.Errorf("glue: migrate databases: %w", err)
	}
	for _, kv := range dbs {
		var db databaseRecord
		if json.Unmarshal([]byte(kv.Value), &db) != nil {
			continue
		}
		db.Name = normName(db.Name)
		if err := s.moveRecord(ctx, nsDatabases, kv.Key, databaseKey(db.Name), &db); err != nil {
			return err
		}
	}
	tables, err := s.store.Scan(ctx, nsTables, "")
	if err != nil {
		return fmt.Errorf("glue: migrate tables: %w", err)
	}
	for _, kv := range tables {
		var t tableRecord
		if json.Unmarshal([]byte(kv.Value), &t) != nil {
			continue
		}
		t.DatabaseName, t.Name = normName(t.DatabaseName), normName(t.Name)
		if err := s.moveRecord(ctx, nsTables, kv.Key, tableKey(t.DatabaseName, t.Name), &t); err != nil {
			return err
		}
	}
	return nil
}

func (s *glueStore) moveRecord(ctx context.Context, ns, from, to string, v any) error {
	if from == to {
		return nil
	}
	if _, taken, err := s.store.Get(ctx, ns, to); err != nil {
		return fmt.Errorf("glue: migrate %s %q: %w", ns, from, err)
	} else if taken {
		s.log.Warn("legacy record not migrated: its canonical key is taken", zap.String("namespace", ns), zap.String("key", from), zap.String("canonical", to))
		return nil
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("glue: migrate %s %q: %w", ns, from, err)
	}
	if err := s.store.Set(ctx, ns, to, string(raw)); err != nil {
		return fmt.Errorf("glue: migrate %s %q: %w", ns, from, err)
	}
	if err := s.store.Delete(ctx, ns, from); err != nil {
		return fmt.Errorf("glue: migrate %s %q: %w", ns, from, err)
	}
	return nil
}

// put marshals v into ns/key.
func (s *glueStore) put(ctx context.Context, ns, key string, v any) error {
	if err := s.ready(ctx); err != nil {
		return err
	}
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
	if err := s.ready(ctx); err != nil {
		return false, err
	}
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
	if err := s.ready(ctx); err != nil {
		return nil, err
	}
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
	if err := s.ready(ctx); err != nil {
		return err
	}
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
	return s.put(ctx, nsDatabases, databaseKey(db.Name), db)
}

func (s *glueStore) getDatabase(ctx context.Context, name string) (*databaseRecord, bool, error) {
	var db databaseRecord
	found, err := s.get(ctx, nsDatabases, databaseKey(name), &db)
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
	prefix := databasePrefix(name)
	for _, ns := range []string{nsPartitions, nsTableVersions, nsColumnStatistics, nsTables} {
		if err := s.deletePrefix(ctx, ns, prefix); err != nil {
			return err
		}
	}
	if err := s.store.Delete(ctx, nsDatabases, databaseKey(name)); err != nil {
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
	out, err := scan[tableRecord](ctx, s, nsTables, databasePrefix(dbName))
	if err != nil {
		return nil, err
	}
	kept := out[:0]
	for _, t := range out {
		// With escaped components the prefix matches only this database's
		// tables; a record whose own name disagrees with its key is skipped.
		if t.DatabaseName != dbName {
			continue
		}
		t.normalize()
		kept = append(kept, t)
	}
	sort.Slice(kept, func(i, j int) bool { return kept[i].Name < kept[j].Name })
	return kept, nil
}

// tableExists reports whether any record is stored under the table's key,
// decodable or not.
func (s *glueStore) tableExists(ctx context.Context, dbName, tableName string) (bool, error) {
	if err := s.ready(ctx); err != nil {
		return false, err
	}
	_, found, err := s.store.Get(ctx, nsTables, tableKey(dbName, tableName))
	if err != nil {
		return false, fmt.Errorf("glue: get table %q: %w", tableKey(dbName, tableName), err)
	}
	return found, nil
}

// deleteTableChildren removes the table's partitions, archived versions and
// column statistics.
func (s *glueStore) deleteTableChildren(ctx context.Context, dbName, tableName string) error {
	prefix := tablePrefix(dbName, tableName)
	for _, ns := range []string{nsPartitions, nsTableVersions, nsColumnStatistics} {
		if err := s.deletePrefix(ctx, ns, prefix); err != nil {
			return err
		}
	}
	return nil
}

// deleteTable removes the table with its partitions and archived versions.
func (s *glueStore) deleteTable(ctx context.Context, dbName, tableName string) error {
	if err := s.deleteTableChildren(ctx, dbName, tableName); err != nil {
		return err
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
	type keyed struct {
		key string
		p   *Partition
	}
	byKey := make([]keyed, len(out))
	for i, p := range out {
		byKey[i] = keyed{partitionKey(dbName, tableName, p.Values), p}
	}
	slices.SortStableFunc(byKey, func(a, b keyed) int { return strings.Compare(a.key, b.key) })
	for i, k := range byKey {
		out[i] = k.p
	}
	return out, nil
}

func (s *glueStore) deletePartition(ctx context.Context, dbName, tableName string, values []string) error {
	key := partitionKey(dbName, tableName, values)
	if err := s.store.Delete(ctx, nsPartitions, key); err != nil {
		return fmt.Errorf("glue: delete partition %q: %w", key, err)
	}
	return s.deletePrefix(ctx, nsColumnStatistics, partitionStatisticsPrefix(dbName, tableName, values))
}
