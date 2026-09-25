package icebergmeta

// Commit: the Iceberg REST catalog's CommitTableRequest applied to a table's
// metadata, with the reference implementation's (TableMetadata.Builder)
// semantics wherever the spec leaves a choice open.
//
// Spec: https://github.com/apache/iceberg/blob/main/open-api/rest-catalog-open-api.yaml
// (TableRequirement and TableUpdate).

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"
)

// CommitRequest is the body of an Iceberg REST commit: requirements the table
// must meet, then updates to apply in order.
type CommitRequest struct {
	Requirements []Requirement `json:"requirements"`
	Updates      []Update      `json:"updates"`
}

// Requirement is one TableRequirement. Type selects which of the other
// members it reads.
type Requirement struct {
	Type                    string `json:"type"`
	UUID                    string `json:"uuid,omitempty"`
	Ref                     string `json:"ref,omitempty"`
	SnapshotID              *int64 `json:"snapshot-id,omitempty"`
	LastAssignedFieldID     *int   `json:"last-assigned-field-id,omitempty"`
	CurrentSchemaID         *int   `json:"current-schema-id,omitempty"`
	LastAssignedPartitionID *int   `json:"last-assigned-partition-id,omitempty"`
	DefaultSpecID           *int   `json:"default-spec-id,omitempty"`
	DefaultSortOrderID      *int   `json:"default-sort-order-id,omitempty"`
}

// Requirement types.
const (
	AssertCreate                  = "assert-create"
	AssertTableUUID               = "assert-table-uuid"
	AssertRefSnapshotID           = "assert-ref-snapshot-id"
	AssertLastAssignedFieldID     = "assert-last-assigned-field-id"
	AssertCurrentSchemaID         = "assert-current-schema-id"
	AssertLastAssignedPartitionID = "assert-last-assigned-partition-id"
	AssertDefaultSpecID           = "assert-default-spec-id"
	AssertDefaultSortOrderID      = "assert-default-sort-order-id"
)

// Update is one TableUpdate. Action selects which of the other members it
// reads. The embedded SnapshotRef carries set-snapshot-ref's members, and its
// SnapshotID also serves remove-statistics and remove-partition-statistics.
type Update struct {
	Action string `json:"action"`
	SnapshotRef
	UUID                string            `json:"uuid,omitempty"`
	FormatVersion       int               `json:"format-version,omitempty"`
	Schema              *Schema           `json:"schema,omitempty"`
	LastColumnID        *int              `json:"last-column-id,omitempty"`
	SchemaID            *int              `json:"schema-id,omitempty"`
	Spec                *PartitionSpec    `json:"spec,omitempty"`
	SpecID              *int              `json:"spec-id,omitempty"`
	SortOrder           *SortOrder        `json:"sort-order,omitempty"`
	SortOrderID         *int              `json:"sort-order-id,omitempty"`
	Snapshot            *Snapshot         `json:"snapshot,omitempty"`
	RefName             string            `json:"ref-name,omitempty"`
	SnapshotIDs         []int64           `json:"snapshot-ids,omitempty"`
	Location            string            `json:"location,omitempty"`
	Updates             map[string]string `json:"updates,omitempty"`
	Removals            []string          `json:"removals,omitempty"`
	Statistics          *StatisticsFile   `json:"statistics,omitempty"`
	PartitionStatistics *StatisticsFile   `json:"partition-statistics,omitempty"`
	SpecIDs             []int             `json:"spec-ids,omitempty"`
	SchemaIDs           []int             `json:"schema-ids,omitempty"`
}

// lastAdded is the id -1 stands for in set-current-schema, set-default-spec
// and set-default-sort-order: whatever the latest add of its kind in the same
// commit resolved to, when that add created it in this commit.
const lastAdded = -1

// Commit applies req to base, the table's current metadata read from
// currentLocation, and returns the metadata to write next. base is nil for a
// table that does not exist yet — a staged or brand-new create, which must
// carry assert-create — and currentLocation is then empty.
//
// Requirements are checked against base first: one that fails returns an
// error matching ErrRequirementFailed and nothing is applied. Updates then
// apply in order; one the spec forbids returns an error matching ErrInvalid.
// When the updates change nothing, Commit returns base itself, and the caller
// has nothing to write.
//
// On the new metadata, last-updated-ms is the timestamp of the snapshot the
// commit added, or now, and never earlier than base's; base's file joins the
// metadata log, which keeps at most write.metadata.previous-versions-max
// entries; and the snapshot log loses the entries of snapshots that no longer
// exist, with everything before them.
func Commit(base *Metadata, currentLocation string, req CommitRequest, now time.Time) (*Metadata, error) {
	for _, r := range req.Requirements {
		if err := r.check(base); err != nil {
			return nil, err
		}
	}
	b, err := newBuilder(base, now)
	if err != nil {
		return nil, err
	}
	for _, u := range req.Updates {
		if err := b.apply(u); err != nil {
			return nil, err
		}
	}
	if base != nil && equalJSON(base, b.m) {
		return base, nil
	}
	return b.build(currentLocation)
}

// check tests one requirement against base, which is nil for a table that
// does not exist. Messages are the reference implementation's.
func (r Requirement) check(base *Metadata) error {
	if r.Type == AssertCreate {
		if base != nil {
			return requirementFailed("Requirement failed: table already exists")
		}
		return nil
	}
	if base == nil {
		return requirementFailed("Requirement failed: %s: the table does not exist", r.Type)
	}
	switch r.Type {
	case AssertTableUUID:
		if !strings.EqualFold(r.UUID, base.TableUUID) {
			return requirementFailed("Requirement failed: UUID does not match: expected %s != %s", base.TableUUID, r.UUID)
		}
		return nil
	case AssertRefSnapshotID:
		return r.checkRef(base)
	case AssertLastAssignedFieldID:
		return checkID(r.LastAssignedFieldID, base.LastColumnID, r.Type, "last assigned field id changed")
	case AssertCurrentSchemaID:
		return checkID(r.CurrentSchemaID, base.CurrentSchemaID, r.Type, "current schema changed")
	case AssertLastAssignedPartitionID:
		return checkID(r.LastAssignedPartitionID, base.LastPartitionID, r.Type, "last assigned partition id changed")
	case AssertDefaultSpecID:
		return checkID(r.DefaultSpecID, base.DefaultSpecID, r.Type, "default partition spec changed")
	case AssertDefaultSortOrderID:
		return checkID(r.DefaultSortOrderID, base.DefaultSortOrderID, r.Type, "default sort order changed")
	default:
		return invalid("Unsupported requirement type %q.", r.Type)
	}
}

func checkID(want *int, got int, typ, what string) error {
	if want == nil {
		return invalid("Requirement %s is missing its id.", typ)
	}
	if *want != got {
		return requirementFailed("Requirement failed: %s: expected id %d != %d", what, *want, got)
	}
	return nil
}

// checkRef is assert-ref-snapshot-id: the ref must point at snapshot-id, or
// not exist when snapshot-id is null.
func (r Requirement) checkRef(base *Metadata) error {
	ref, exists := base.Refs[r.Ref]
	switch {
	case exists && r.SnapshotID == nil:
		return requirementFailed("Requirement failed: %s %s was created concurrently", ref.Type, r.Ref)
	case exists && *r.SnapshotID != ref.SnapshotID:
		return requirementFailed("Requirement failed: %s %s has changed: expected id %d != %d", ref.Type, r.Ref, *r.SnapshotID, ref.SnapshotID)
	case !exists && r.SnapshotID != nil:
		return requirementFailed("Requirement failed: branch or tag %s is missing, expected %d", r.Ref, *r.SnapshotID)
	}
	return nil
}

// builder holds the metadata a commit is building and what the commit has
// done so far: the ids its adds resolved to, the snapshots it added, and the
// snapshots it made current on main in turn.
type builder struct {
	m   *Metadata
	now time.Time

	lastAddedSchema, lastAddedSpec, lastAddedOrder *int
	// The ids this commit's adds created, by kind: only those can be "the
	// last added".
	addedSchemas, addedSpecs, addedOrders map[int]bool
	addedSnapshots                        map[int64]bool
	mainHistory                           []int64
	// lastUpdatedMS is the timestamp the commit has settled on so far: an
	// added snapshot's, or now once main moved to an older snapshot. Zero
	// until one of those happens.
	lastUpdatedMS int64
}

// newBuilder starts from a copy of base, or from an empty table whose format
// version the commit's upgrade-format-version (if any) sets.
func newBuilder(base *Metadata, now time.Time) (*builder, error) {
	b := &builder{
		now: now, addedSnapshots: map[int64]bool{},
		addedSchemas: map[int]bool{}, addedSpecs: map[int]bool{}, addedOrders: map[int]bool{},
	}
	if base == nil {
		b.m = &Metadata{
			CurrentSchemaID: lastAdded, DefaultSpecID: lastAdded, DefaultSortOrderID: lastAdded,
			LastPartitionID: firstPartitionFieldID - 1, CurrentSnapshotID: noSnapshot,
		}
		b.m.fillEmpty()
		return b, nil
	}
	raw, err := json.Marshal(base)
	if err != nil {
		return nil, err
	}
	b.m = &Metadata{}
	if err := json.Unmarshal(raw, b.m); err != nil {
		return nil, err
	}
	return b, nil
}

// build finishes the new metadata: format version, timestamps, the logs and
// the version 1 fields, then checks it is a whole table.
func (b *builder) build(currentLocation string) (*Metadata, error) {
	m := b.m
	if m.FormatVersion == 0 {
		m.FormatVersion = FormatVersion
	}
	baseUpdated := m.LastUpdatedMS
	if b.lastUpdatedMS == 0 {
		b.lastUpdatedMS = b.now.UnixMilli()
	}
	m.LastUpdatedMS = max(b.lastUpdatedMS, baseUpdated)
	if currentLocation != "" {
		m.MetadataLog = append(m.MetadataLog, MetadataLogEntry{TimestampMS: baseUpdated, MetadataFile: currentLocation})
		if limit := previousVersionsMax(m.Properties); len(m.MetadataLog) > limit {
			m.MetadataLog = m.MetadataLog[len(m.MetadataLog)-limit:]
		}
	}
	m.SnapshotLog = b.prunedSnapshotLog()
	m.writeVersionFields()
	if m.TableUUID == "" || m.Location == "" {
		return nil, invalid("A table needs a UUID and a location: the commit must assign both.")
	}
	return m, m.validate()
}

// previousVersionsMax is write.metadata.previous-versions-max, at least 1. An
// unparseable value falls back to the default rather than failing the
// commit: the property only trims history, and refusing every later commit
// over it would leave the table unwritable.
func previousVersionsMax(props map[string]string) int {
	if v, err := strconv.Atoi(props[propPreviousVersionsMax]); err == nil {
		return max(1, v)
	}
	return defaultPreviousVersionsMax
}

// prunedSnapshotLog drops two kinds of entry, as the reference implementation
// does. A snapshot this commit made current on main only to replace it within
// the same commit was never visible, so its entry goes. And an entry for a
// snapshot that no longer exists takes every entry before it too, since
// keeping them would leave a gap in which the log names the wrong snapshot as
// current.
func (b *builder) prunedSnapshotLog() []SnapshotLogEntry {
	intermediate := map[int64]bool{}
	for _, id := range b.mainHistory {
		if b.addedSnapshots[id] && id != b.m.CurrentSnapshotID {
			intermediate[id] = true
		}
	}
	out := []SnapshotLogEntry{}
	for _, e := range b.m.SnapshotLog {
		switch {
		case b.m.snapshotIndex(e.SnapshotID) < 0:
			out = out[:0]
		case !intermediate[e.SnapshotID]:
			out = append(out, e)
		}
	}
	return out
}
