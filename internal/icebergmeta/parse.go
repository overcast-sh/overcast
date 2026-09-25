package icebergmeta

import (
	"bytes"
	"encoding/json"
	"slices"
)

// StatisticsFile is one entry of statistics or partition-statistics. A commit
// only ever replaces or removes an entry by the snapshot it describes, so the
// rest of the entry is kept exactly as it was written.
type StatisticsFile struct {
	SnapshotID int64
	raw        json.RawMessage
}

// MarshalJSON writes the entry as it was read.
func (f StatisticsFile) MarshalJSON() ([]byte, error) {
	if len(f.raw) == 0 {
		return json.Marshal(map[string]int64{"snapshot-id": f.SnapshotID})
	}
	return f.raw, nil
}

// UnmarshalJSON reads an entry, which must name the snapshot it describes.
func (f *StatisticsFile) UnmarshalJSON(b []byte) error {
	var head struct {
		SnapshotID *int64 `json:"snapshot-id"`
	}
	if err := json.Unmarshal(b, &head); err != nil {
		return err
	}
	if head.SnapshotID == nil {
		return invalid("A statistics file must name its snapshot-id.")
	}
	f.SnapshotID = *head.SnapshotID
	f.raw = bytes.Clone(b)
	return nil
}

// Parse reads a table metadata file of format version 1 or 2. A version 1
// document's single schema and partition spec become the schema and spec
// lists version 2 keeps, and a table whose refs omit main gets the main
// branch the spec implies from current-snapshot-id — so a caller sees one
// model whichever version wrote the file. The file's own format version is
// kept; only an upgrade-format-version update changes it.
func Parse(raw []byte) (*Metadata, error) {
	m := &Metadata{CurrentSnapshotID: noSnapshot}
	if err := json.Unmarshal(raw, m); err != nil {
		return nil, invalid("The table metadata is not valid: %v", err)
	}
	if m.FormatVersion < minFormatVersion || m.FormatVersion > FormatVersion {
		return nil, invalid("Unsupported table format version %d: this catalog reads versions %d and %d.", m.FormatVersion, minFormatVersion, FormatVersion)
	}
	if m.Location == "" || (m.FormatVersion > minFormatVersion && m.TableUUID == "") {
		return nil, invalid("The table metadata has no table-uuid or location.")
	}
	m.upgradeVersionOneFields()
	m.fillEmpty()
	if m.CurrentSnapshotID != noSnapshot {
		if _, ok := m.Refs[MainBranch]; !ok {
			m.Refs[MainBranch] = SnapshotRef{SnapshotID: m.CurrentSnapshotID, Type: RefBranch}
		}
	}
	return m, m.validate()
}

// upgradeVersionOneFields reads the version 1 schema and partition-spec
// fields into the lists version 2 keeps, when the document has only the
// former. Partition fields written without ids get the ids version 1 implies:
// 1000 upwards, in order.
func (m *Metadata) upgradeVersionOneFields() {
	if len(m.Schemas) == 0 && m.Schema != nil {
		m.Schemas = []Schema{*m.Schema}
		m.CurrentSchemaID = m.Schema.SchemaID
	}
	if len(m.PartitionSpecs) == 0 && m.PartitionSpec != nil {
		fields := slices.Clone(*m.PartitionSpec)
		for i := range fields {
			if fields[i].FieldID == 0 {
				fields[i].FieldID = firstPartitionFieldID + i
			}
		}
		m.PartitionSpecs = []PartitionSpec{{SpecID: InitialSpecID, Fields: fields}}
		m.DefaultSpecID = InitialSpecID
	}
	// last-partition-id is optional in version 1; like the reference
	// implementation, take it as at least the highest id any spec uses, so
	// the next spec's fields cannot reuse one.
	m.LastPartitionID = max(m.LastPartitionID, firstPartitionFieldID-1)
	for _, spec := range m.PartitionSpecs {
		for _, f := range spec.Fields {
			m.LastPartitionID = max(m.LastPartitionID, f.FieldID)
		}
	}
	if len(m.SortOrders) == 0 {
		m.SortOrders = []SortOrder{unsortedOrder()}
		m.DefaultSortOrderID = UnsortedOrderID
	}
}

// validate checks that the document's pointers — current schema, default spec
// and sort order, current snapshot and every ref — name things it holds, and
// that main is the current snapshot.
func (m *Metadata) validate() error {
	if _, ok := m.schemaByID(m.CurrentSchemaID); !ok {
		return invalid("The current schema %d is not one of the table's schemas.", m.CurrentSchemaID)
	}
	if _, ok := m.specByID(m.DefaultSpecID); !ok {
		return invalid("The default partition spec %d is not one of the table's specs.", m.DefaultSpecID)
	}
	if !m.hasSortOrder(m.DefaultSortOrderID) {
		return invalid("The default sort order %d is not one of the table's sort orders.", m.DefaultSortOrderID)
	}
	if m.CurrentSnapshotID != noSnapshot && m.snapshotIndex(m.CurrentSnapshotID) < 0 {
		return invalid("The current snapshot %d is not one of the table's snapshots.", m.CurrentSnapshotID)
	}
	if main, ok := m.Refs[MainBranch]; ok && main.SnapshotID != m.CurrentSnapshotID {
		return invalid("Current snapshot ID does not match main branch (%d != %d)", m.CurrentSnapshotID, main.SnapshotID)
	}
	for name, ref := range m.Refs {
		if m.snapshotIndex(ref.SnapshotID) < 0 {
			return invalid("Ref %q points to unknown snapshot %d.", name, ref.SnapshotID)
		}
	}
	return nil
}
