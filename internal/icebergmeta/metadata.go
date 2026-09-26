// Package icebergmeta builds and evolves Apache Iceberg table metadata — the
// metadata.json document an Iceberg catalog points a table at.
//
// Overcast writes that document itself where AWS does: S3 Tables' CreateTable
// with a schema, and the Iceberg REST catalog's create and commit (and, later,
// Glue's OpenTableFormatInput). It hand-rolls the format-version 1 and 2 model
// rather than depending on apache/iceberg-go, which would pull arrow-go into
// every binary. Only metadata is handled here; data and manifest files are
// always the engine's or the client's.
//
// The API is Decision 4's in docs/plans/athena-s3tables-iceberg.md: New builds
// a table's first metadata, Parse reads a metadata file, and Commit applies an
// Iceberg REST commit — its requirements, then its updates — to it. The
// package does no I/O and reads no clock: callers pass now.
//
// Spec: https://iceberg.apache.org/spec/#table-metadata-fields
package icebergmeta

import (
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strconv"
	"time"
)

// FormatVersion is the newest Iceberg table format this package writes, and
// the one a new table gets unless its properties ask for version 1.
const FormatVersion = 2

// minFormatVersion is the oldest format this package reads and writes.
const minFormatVersion = 1

// Identifiers the spec gives an initial table.
const (
	// InitialSchemaID is the id of a new table's only schema.
	InitialSchemaID = 0
	// InitialSpecID is the id of a new table's partition spec.
	InitialSpecID = 0
	// UnsortedOrderID is the id the spec reserves for the unsorted order.
	UnsortedOrderID = 0
	// firstPartitionFieldID is where partition field ids start; the reference
	// implementation reports 999 as the last id of a spec with no fields.
	firstPartitionFieldID = 1000
	// noSnapshot is current-snapshot-id for a table without one, as the
	// reference implementation writes it.
	noSnapshot = -1
)

// Table properties this package reads.
const (
	// propFormatVersion chooses a new table's format version. It is reserved:
	// consumed at creation and never stored.
	propFormatVersion = "format-version"
	// propPreviousVersionsMax caps the metadata log; the spec's default is 100.
	propPreviousVersionsMax    = "write.metadata.previous-versions-max"
	defaultPreviousVersionsMax = 100
)

// Schema is an Iceberg struct schema.
type Schema struct {
	Type               string  `json:"type"`
	SchemaID           int     `json:"schema-id"`
	IdentifierFieldIDs []int   `json:"identifier-field-ids,omitempty"`
	Fields             []Field `json:"fields"`
}

// PartitionField is one field of a partition spec.
type PartitionField struct {
	SourceID  int    `json:"source-id"`
	FieldID   int    `json:"field-id"`
	Name      string `json:"name"`
	Transform string `json:"transform"`
}

// PartitionSpec is an Iceberg partition spec.
type PartitionSpec struct {
	SpecID int              `json:"spec-id"`
	Fields []PartitionField `json:"fields"`
}

// SortField is one field of a sort order.
type SortField struct {
	SourceID  int    `json:"source-id"`
	Transform string `json:"transform"`
	Direction string `json:"direction"`
	NullOrder string `json:"null-order"`
}

// SortOrder is an Iceberg sort order.
type SortOrder struct {
	OrderID int         `json:"order-id"`
	Fields  []SortField `json:"fields"`
}

// Snapshot is one snapshot of a table. A version 1 snapshot may list its
// manifests inline instead of naming a manifest list, and has no sequence
// number; the reference implementation writes sequence-number only when it is
// above zero, and so does this package.
type Snapshot struct {
	SnapshotID       int64             `json:"snapshot-id"`
	ParentSnapshotID *int64            `json:"parent-snapshot-id,omitempty"`
	SequenceNumber   int64             `json:"sequence-number,omitempty"`
	TimestampMS      int64             `json:"timestamp-ms"`
	ManifestList     string            `json:"manifest-list,omitempty"`
	Manifests        []string          `json:"manifests,omitempty"`
	Summary          map[string]string `json:"summary,omitempty"`
	SchemaID         *int              `json:"schema-id,omitempty"`
}

// Ref types and the branch every table's current snapshot is on.
const (
	RefBranch  = "branch"
	RefTag     = "tag"
	MainBranch = "main"
)

// SnapshotRef is a named branch or tag.
type SnapshotRef struct {
	SnapshotID         int64  `json:"snapshot-id"`
	Type               string `json:"type"`
	MinSnapshotsToKeep *int   `json:"min-snapshots-to-keep,omitempty"`
	MaxSnapshotAgeMS   *int64 `json:"max-snapshot-age-ms,omitempty"`
	MaxRefAgeMS        *int64 `json:"max-ref-age-ms,omitempty"`
}

// SnapshotLogEntry records when a snapshot became the table's current one.
type SnapshotLogEntry struct {
	TimestampMS int64 `json:"timestamp-ms"`
	SnapshotID  int64 `json:"snapshot-id"`
}

// MetadataLogEntry records a previous metadata file of the table.
type MetadataLogEntry struct {
	TimestampMS  int64  `json:"timestamp-ms"`
	MetadataFile string `json:"metadata-file"`
}

// Metadata is a table metadata document. Field order follows the spec's table
// so the written JSON reads the way the spec does. Schema and PartitionSpec
// are the version 1 fields, written only for a version 1 table.
// CurrentSnapshotID is -1 for a table with no current snapshot, as the
// reference implementation writes it.
type Metadata struct {
	FormatVersion       int                    `json:"format-version"`
	TableUUID           string                 `json:"table-uuid"`
	Location            string                 `json:"location"`
	LastSequenceNumber  int64                  `json:"last-sequence-number"`
	LastUpdatedMS       int64                  `json:"last-updated-ms"`
	LastColumnID        int                    `json:"last-column-id"`
	Schema              *Schema                `json:"schema,omitempty"`
	CurrentSchemaID     int                    `json:"current-schema-id"`
	Schemas             []Schema               `json:"schemas"`
	PartitionSpec       *[]PartitionField      `json:"partition-spec,omitempty"`
	DefaultSpecID       int                    `json:"default-spec-id"`
	PartitionSpecs      []PartitionSpec        `json:"partition-specs"`
	LastPartitionID     int                    `json:"last-partition-id"`
	DefaultSortOrderID  int                    `json:"default-sort-order-id"`
	SortOrders          []SortOrder            `json:"sort-orders"`
	Properties          map[string]string      `json:"properties"`
	CurrentSnapshotID   int64                  `json:"current-snapshot-id"`
	Snapshots           []Snapshot             `json:"snapshots"`
	SnapshotLog         []SnapshotLogEntry     `json:"snapshot-log"`
	MetadataLog         []MetadataLogEntry     `json:"metadata-log"`
	Refs                map[string]SnapshotRef `json:"refs"`
	Statistics          []StatisticsFile       `json:"statistics"`
	PartitionStatistics []StatisticsFile       `json:"partition-statistics"`
}

// CreateSpec describes a new table in Iceberg's own terms. Fields carry the
// caller's field ids, which partition fields, sort fields and
// IdentifierFieldIDs refer to columns by; New then assigns fresh ids from 1,
// as Iceberg does for every new table whatever the caller proposed, and
// rewrites those references to match. A partition field's FieldID is optional
// (zero means "assign one").
type CreateSpec struct {
	TableUUID          string
	Location           string
	Fields             []Field
	IdentifierFieldIDs []int
	PartitionFields    []PartitionField
	SortOrderID        int
	SortFields         []SortField
	Properties         map[string]string
}

// ErrInvalid is matched (errors.Is) by every error for a table definition or
// an update the spec does not allow. The error's message is written for the
// API caller.
var ErrInvalid = errors.New("icebergmeta: invalid table definition")

// ErrRequirementFailed is matched (errors.Is) by the error Commit returns when
// the table no longer meets one of the commit's requirements — another writer
// committed first. The message is written for the API caller.
var ErrRequirementFailed = errors.New("icebergmeta: commit requirement failed")

type metaError struct {
	reason string
	kind   error
}

func (e metaError) Error() string        { return e.reason }
func (e metaError) Is(target error) bool { return target == e.kind }

func invalid(format string, args ...any) error {
	return metaError{reason: fmt.Sprintf(format, args...), kind: ErrInvalid}
}

func requirementFailed(format string, args ...any) error {
	return metaError{reason: fmt.Sprintf(format, args...), kind: ErrRequirementFailed}
}

// New builds the metadata of a table that has no snapshots yet, last updated
// at now: one schema (id 0) with fresh field ids, the given partition spec as
// spec 0 (unpartitioned when empty) and the given sort order (unsorted, id 0,
// when empty). The reserved format-version property picks version 1 or 2 and
// is not stored.
func New(spec CreateSpec, now time.Time) (*Metadata, error) {
	if spec.TableUUID == "" || spec.Location == "" {
		return nil, invalid("a table needs a UUID and a location")
	}
	props := maps.Clone(spec.Properties)
	if props == nil {
		props = map[string]string{}
	}
	version, err := createFormatVersion(props)
	if err != nil {
		return nil, err
	}
	schema, mapping, err := newSchema(spec.Fields, spec.IdentifierFieldIDs)
	if err != nil {
		return nil, err
	}
	partitionFields, lastPartitionID, err := bindPartitionFields(remapSources(spec.PartitionFields, mapping), schema, firstPartitionFieldID-1)
	if err != nil {
		return nil, err
	}
	orders, defaultOrder, err := newSortOrders(spec.SortOrderID, remapSortSources(spec.SortFields, mapping), schema)
	if err != nil {
		return nil, err
	}
	m := &Metadata{
		FormatVersion:      version,
		TableUUID:          spec.TableUUID,
		Location:           spec.Location,
		LastUpdatedMS:      now.UnixMilli(),
		LastColumnID:       schema.highestFieldID(),
		CurrentSchemaID:    InitialSchemaID,
		Schemas:            []Schema{schema},
		DefaultSpecID:      InitialSpecID,
		PartitionSpecs:     []PartitionSpec{{SpecID: InitialSpecID, Fields: partitionFields}},
		LastPartitionID:    lastPartitionID,
		DefaultSortOrderID: defaultOrder,
		SortOrders:         orders,
		Properties:         props,
		CurrentSnapshotID:  noSnapshot,
	}
	m.fillEmpty()
	m.writeVersionFields()
	return m, nil
}

// createFormatVersion consumes the reserved format-version property.
func createFormatVersion(props map[string]string) (int, error) {
	raw, ok := props[propFormatVersion]
	if !ok {
		return FormatVersion, nil
	}
	delete(props, propFormatVersion)
	v, err := strconv.Atoi(raw)
	if err != nil || v < minFormatVersion || v > FormatVersion {
		return 0, invalid("Unsupported format version %q: this catalog writes versions %d and %d.", raw, minFormatVersion, FormatVersion)
	}
	return v, nil
}

// newSchema is the initial schema: the caller's fields validated and
// renumbered from 1, with the old-to-new id mapping.
func newSchema(fields []Field, identifierIDs []int) (Schema, map[int]int, error) {
	checked, err := validateSchema(Schema{Fields: fields, IdentifierFieldIDs: identifierIDs})
	if err != nil {
		return Schema{}, nil, err
	}
	fresh, mapping := freshIDs(checked.Fields)
	schema := Schema{Type: "struct", SchemaID: InitialSchemaID, Fields: fresh}
	for _, id := range identifierIDs {
		schema.IdentifierFieldIDs = append(schema.IdentifierFieldIDs, mapping[id])
	}
	return schema, mapping, nil
}

// remapSources rewrites partition source ids through the fresh-id mapping. An
// id the caller's schema never declared becomes -1, which validation refuses.
func remapSources(in []PartitionField, mapping map[int]int) []PartitionField {
	out := slices.Clone(in)
	for i := range out {
		out[i].SourceID = remapped(mapping, out[i].SourceID)
	}
	return out
}

func remapSortSources(in []SortField, mapping map[int]int) []SortField {
	out := slices.Clone(in)
	for i := range out {
		out[i].SourceID = remapped(mapping, out[i].SourceID)
	}
	return out
}

func remapped(mapping map[int]int, id int) int {
	if v, ok := mapping[id]; ok {
		return v
	}
	return -1
}

// bindPartitionFields validates a spec's fields against schema and assigns an
// id to each field that has none, above both lastAssigned and the highest
// explicit id, so the two never collide. It returns the fields and the new
// last assigned id.
func bindPartitionFields(in []PartitionField, schema Schema, lastAssigned int) ([]PartitionField, int, error) {
	lastID := lastAssigned
	for _, p := range in {
		lastID = max(lastID, p.FieldID)
	}
	out := make([]PartitionField, 0, len(in))
	ids := map[int]bool{}
	names := map[string]bool{}
	for _, p := range in {
		if !schema.hasFieldID(p.SourceID) {
			return nil, 0, invalid("Partition field %q refers to unknown source id %d.", p.Name, p.SourceID)
		}
		if p.Name == "" || p.Transform == "" {
			return nil, 0, invalid("A partition field needs a name and a transform.")
		}
		if p.FieldID == 0 {
			lastID++
			p.FieldID = lastID
		}
		if p.FieldID < firstPartitionFieldID || ids[p.FieldID] {
			return nil, 0, invalid("Partition field id %d is invalid or used more than once.", p.FieldID)
		}
		if names[p.Name] {
			return nil, 0, invalid("Partition field name %q is used more than once.", p.Name)
		}
		ids[p.FieldID], names[p.Name] = true, true
		out = append(out, p)
	}
	return out, lastID, nil
}

// newSortOrders is the unsorted order plus, when fields are given, the order
// orderID over them, with the id of the default order.
func newSortOrders(orderID int, in []SortField, schema Schema) ([]SortOrder, int, error) {
	orders := []SortOrder{unsortedOrder()}
	if len(in) == 0 {
		return orders, UnsortedOrderID, nil
	}
	if orderID == UnsortedOrderID {
		return nil, 0, invalid("Sort order id 0 is reserved for the unsorted order.")
	}
	if err := validateSortFields(in, schema); err != nil {
		return nil, 0, err
	}
	return append(orders, SortOrder{OrderID: orderID, Fields: slices.Clone(in)}), orderID, nil
}

func unsortedOrder() SortOrder { return SortOrder{OrderID: UnsortedOrderID, Fields: []SortField{}} }

func validateSortFields(in []SortField, schema Schema) error {
	for _, f := range in {
		if !schema.hasFieldID(f.SourceID) {
			return invalid("Sort field refers to unknown source id %d.", f.SourceID)
		}
		if f.Direction != "asc" && f.Direction != "desc" {
			return invalid("Sort direction %q must be asc or desc.", f.Direction)
		}
		if f.NullOrder != "nulls-first" && f.NullOrder != "nulls-last" {
			return invalid("Sort null-order %q must be nulls-first or nulls-last.", f.NullOrder)
		}
		if f.Transform == "" {
			return invalid("A sort field needs a transform.")
		}
	}
	return nil
}

// fillEmpty replaces nil collections with empty ones, so the document always
// writes [] and {} rather than null.
func (m *Metadata) fillEmpty() {
	m.Schemas = nonNil(m.Schemas)
	m.PartitionSpecs = nonNil(m.PartitionSpecs)
	m.SortOrders = nonNil(m.SortOrders)
	m.Snapshots = nonNil(m.Snapshots)
	m.SnapshotLog = nonNil(m.SnapshotLog)
	m.MetadataLog = nonNil(m.MetadataLog)
	m.Statistics = nonNil(m.Statistics)
	m.PartitionStatistics = nonNil(m.PartitionStatistics)
	if m.Properties == nil {
		m.Properties = map[string]string{}
	}
	if m.Refs == nil {
		m.Refs = map[string]SnapshotRef{}
	}
	for i := range m.PartitionSpecs {
		m.PartitionSpecs[i].Fields = nonNil(m.PartitionSpecs[i].Fields)
	}
	for i := range m.SortOrders {
		m.SortOrders[i].Fields = nonNil(m.SortOrders[i].Fields)
	}
}

// writeVersionFields sets the version 1 fields a version 1 writer must still
// write — the current schema and the default spec's fields — and clears them
// for version 2.
func (m *Metadata) writeVersionFields() {
	m.Schema, m.PartitionSpec = nil, nil
	if m.FormatVersion != minFormatVersion {
		return
	}
	if s, ok := m.schemaByID(m.CurrentSchemaID); ok {
		m.Schema = &s
	}
	if spec, ok := m.specByID(m.DefaultSpecID); ok {
		fields := spec.Fields
		m.PartitionSpec = &fields
	}
}

func (m *Metadata) schemaByID(id int) (Schema, bool) {
	i := slices.IndexFunc(m.Schemas, func(s Schema) bool { return s.SchemaID == id })
	if i < 0 {
		return Schema{}, false
	}
	return m.Schemas[i], true
}

func (m *Metadata) specByID(id int) (PartitionSpec, bool) {
	i := slices.IndexFunc(m.PartitionSpecs, func(s PartitionSpec) bool { return s.SpecID == id })
	if i < 0 {
		return PartitionSpec{}, false
	}
	return m.PartitionSpecs[i], true
}

func (m *Metadata) hasSortOrder(id int) bool {
	return slices.ContainsFunc(m.SortOrders, func(o SortOrder) bool { return o.OrderID == id })
}

// CurrentSnapshot is the table's current snapshot; ok is false for a table
// with none.
func (m *Metadata) CurrentSnapshot() (Snapshot, bool) {
	i := m.snapshotIndex(m.CurrentSnapshotID)
	if i < 0 {
		return Snapshot{}, false
	}
	return m.Snapshots[i], true
}

func (m *Metadata) snapshotIndex(id int64) int {
	return slices.IndexFunc(m.Snapshots, func(s Snapshot) bool { return s.SnapshotID == id })
}

// metadataFileName is the reference implementation's metadata file name,
// "<version>-<uuid>.metadata.json".
var metadataFileName = regexp.MustCompile(`(?:^|/)(\d+)-[^/]*\.metadata\.json$`)

// MetadataPath is where the version-th metadata file of a table lives,
// relative to the table's location, in the "metadata/<version>-<uuid>.metadata.json"
// form the reference implementation writes.
func MetadataPath(version int, fileUUID string) string {
	return fmt.Sprintf("metadata/%05d-%s.metadata.json", version, fileUUID)
}

// NextMetadataVersion is the version of the metadata file that follows the
// one at location: one more than the version its name carries, or 0 when
// there is no current file or its name carries none.
func NextMetadataVersion(location string) int {
	m := metadataFileName.FindStringSubmatch(location)
	if m == nil {
		return 0
	}
	v, err := strconv.Atoi(m[1])
	if err != nil {
		return 0
	}
	return v + 1
}
