package icebergmeta

// The table updates a commit applies, one method per action. Each follows the
// reference implementation's TableMetadata.Builder, including where it reuses
// an id rather than adding a duplicate schema, spec or sort order.

import (
	"maps"
	"slices"
)

// apply runs one update against the metadata being built.
func (b *builder) apply(u Update) error {
	switch u.Action {
	case "assign-uuid":
		return b.assignUUID(u)
	case "upgrade-format-version":
		return b.upgradeFormatVersion(u)
	case "add-schema":
		return b.addSchema(u)
	case "set-current-schema":
		return b.setCurrent(u.SchemaID, b.lastAddedSchema, "schema", func(id int) bool {
			_, ok := b.m.schemaByID(id)
			return ok
		}, &b.m.CurrentSchemaID)
	case "add-spec":
		return b.addSpec(u)
	case "set-default-spec":
		return b.setCurrent(u.SpecID, b.lastAddedSpec, "partition spec", func(id int) bool {
			_, ok := b.m.specByID(id)
			return ok
		}, &b.m.DefaultSpecID)
	case "add-sort-order":
		return b.addSortOrder(u)
	case "set-default-sort-order":
		return b.setCurrent(u.SortOrderID, b.lastAddedOrder, "sort order", b.m.hasSortOrder, &b.m.DefaultSortOrderID)
	case "add-snapshot":
		return b.addSnapshot(u)
	case "set-snapshot-ref":
		return b.setSnapshotRef(u)
	case "remove-snapshot-ref":
		b.removeRef(u.RefName)
		return nil
	case "remove-snapshots":
		b.removeSnapshots(u.SnapshotIDs)
		return nil
	case "set-location":
		if u.Location == "" {
			return invalid("set-location needs a location.")
		}
		b.m.Location = u.Location
		return nil
	case "set-properties":
		maps.Copy(b.m.Properties, u.Updates)
		return nil
	case "remove-properties":
		for _, k := range u.Removals {
			delete(b.m.Properties, k)
		}
		return nil
	case "set-statistics":
		return setStatistics(&b.m.Statistics, u.Statistics, u.Action)
	case "remove-statistics":
		b.m.Statistics = withoutStatistics(b.m.Statistics, u.SnapshotID)
		return nil
	case "set-partition-statistics":
		return setStatistics(&b.m.PartitionStatistics, u.PartitionStatistics, u.Action)
	case "remove-partition-statistics":
		b.m.PartitionStatistics = withoutStatistics(b.m.PartitionStatistics, u.SnapshotID)
		return nil
	case "remove-partition-specs":
		return b.removeSpecs(u.SpecIDs)
	case "remove-schemas":
		return b.removeSchemas(u.SchemaIDs)
	default:
		return invalid("Unsupported table update %q.", u.Action)
	}
}

// assignUUID sets the table's UUID. The reference implementations allow a
// reassignment; assert-table-uuid is what guards against one.
func (b *builder) assignUUID(u Update) error {
	if u.UUID == "" {
		return invalid("assign-uuid needs a uuid.")
	}
	b.m.TableUUID = u.UUID
	return nil
}

// upgradeFormatVersion only ever raises the version, and only as far as this
// package writes. For a table still being created every version is an
// upgrade, since the empty table has none.
func (b *builder) upgradeFormatVersion(u Update) error {
	switch v := u.FormatVersion; {
	case v > FormatVersion || v < minFormatVersion:
		return invalid("Cannot upgrade table to unsupported format version: v%d (supported: v%d)", v, FormatVersion)
	case v < b.m.FormatVersion:
		return invalid("Cannot downgrade v%d table to v%d", b.m.FormatVersion, v)
	default:
		b.m.FormatVersion = v
		return nil
	}
}

// addSchema adds a schema, or reuses the id of an identical one. The last
// column id only grows: it becomes the larger of its current value, the
// schema's highest id and the deprecated last-column-id member, which may not
// go backwards.
func (b *builder) addSchema(u Update) error {
	if u.Schema == nil {
		return invalid("add-schema needs a schema.")
	}
	schema, err := validateSchema(*u.Schema)
	if err != nil {
		return err
	}
	lastColumnID := max(b.m.LastColumnID, schema.highestFieldID())
	if u.LastColumnID != nil {
		if *u.LastColumnID < b.m.LastColumnID {
			return invalid("Invalid last column ID: %d < %d (previous last column ID)", *u.LastColumnID, b.m.LastColumnID)
		}
		lastColumnID = max(lastColumnID, *u.LastColumnID)
	}
	b.m.LastColumnID = lastColumnID
	id, reused := b.reuseSchemaID(schema)
	if !reused {
		schema.SchemaID = id
		b.m.Schemas = append(b.m.Schemas, schema)
	}
	b.lastAddedSchema = markAdded(b.addedSchemas, id, !reused)
	return nil
}

// markAdded records what an add resolved to and returns what -1 now names. A
// new id is this commit's and is "the last added"; so is an id an earlier add
// of this commit created. An id the table already had is not: as in the
// reference implementation, a following -1 then has nothing to name.
func markAdded(added map[int]bool, id int, isNew bool) *int {
	if isNew {
		added[id] = true
	}
	if !added[id] {
		return nil
	}
	return &id
}

func (b *builder) reuseSchemaID(schema Schema) (int, bool) {
	next := 0
	for _, s := range b.m.Schemas {
		if s.sameAs(schema) {
			return s.SchemaID, true
		}
		next = max(next, s.SchemaID+1)
	}
	return next, false
}

// setCurrent points a table at one of its schemas, specs or sort orders by
// id, where -1 means the one this commit last added.
func (b *builder) setCurrent(requested, added *int, what string, exists func(int) bool, target *int) error {
	if requested == nil {
		return invalid("The update needs a %s id.", what)
	}
	id := *requested
	if id == lastAdded {
		if added == nil {
			return invalid("Cannot set last added %s: no %s has been added", what, what)
		}
		id = *added
	}
	if !exists(id) {
		return invalid("Cannot set current %s to unknown %s: %d", what, what, id)
	}
	*target = id
	return nil
}

// currentSchema is the schema new specs and sort orders bind to.
func (b *builder) currentSchema() (Schema, error) {
	s, ok := b.m.schemaByID(b.m.CurrentSchemaID)
	if !ok {
		return Schema{}, invalid("The table has no current schema to bind to.")
	}
	return s, nil
}

// addSpec adds a partition spec, or reuses the id of a compatible one — the
// same fields in order, by source, transform and name. New fields without an
// id get one after the table's last assigned partition id.
func (b *builder) addSpec(u Update) error {
	if u.Spec == nil {
		return invalid("add-spec needs a spec.")
	}
	schema, err := b.currentSchema()
	if err != nil {
		return err
	}
	fields, lastID, err := bindPartitionFields(u.Spec.Fields, schema, b.m.LastPartitionID)
	if err != nil {
		return err
	}
	next := 0
	for _, s := range b.m.PartitionSpecs {
		if compatibleSpec(s.Fields, fields) {
			b.lastAddedSpec = markAdded(b.addedSpecs, s.SpecID, false)
			return nil
		}
		next = max(next, s.SpecID+1)
	}
	b.m.PartitionSpecs = append(b.m.PartitionSpecs, PartitionSpec{SpecID: next, Fields: fields})
	b.m.LastPartitionID = max(b.m.LastPartitionID, lastID)
	b.lastAddedSpec = markAdded(b.addedSpecs, next, true)
	return nil
}

func compatibleSpec(a, b []PartitionField) bool {
	return slices.EqualFunc(a, b, func(x, y PartitionField) bool {
		return x.SourceID == y.SourceID && x.Transform == y.Transform && x.Name == y.Name
	})
}

// addSortOrder adds a sort order, or reuses the id of an identical one. An
// order with no fields is the unsorted order, id 0.
func (b *builder) addSortOrder(u Update) error {
	if u.SortOrder == nil {
		return invalid("add-sort-order needs a sort-order.")
	}
	fields := nonNil(u.SortOrder.Fields)
	if len(fields) == 0 {
		isNew := !b.m.hasSortOrder(UnsortedOrderID)
		if isNew {
			b.m.SortOrders = append(b.m.SortOrders, unsortedOrder())
		}
		b.lastAddedOrder = markAdded(b.addedOrders, UnsortedOrderID, isNew)
		return nil
	}
	schema, err := b.currentSchema()
	if err != nil {
		return err
	}
	if err := validateSortFields(fields, schema); err != nil {
		return err
	}
	next := UnsortedOrderID + 1
	for _, o := range b.m.SortOrders {
		if slices.Equal(o.Fields, fields) {
			b.lastAddedOrder = markAdded(b.addedOrders, o.OrderID, false)
			return nil
		}
		next = max(next, o.OrderID+1)
	}
	b.m.SortOrders = append(b.m.SortOrders, SortOrder{OrderID: next, Fields: slices.Clone(fields)})
	b.lastAddedOrder = markAdded(b.addedOrders, next, true)
	return nil
}

// addSnapshot adds a snapshot. On a version 2 table a snapshot with a parent
// must have a sequence number above the table's last, which never goes down.
func (b *builder) addSnapshot(u Update) error {
	s := u.Snapshot
	if s == nil {
		return invalid("add-snapshot needs a snapshot.")
	}
	if b.m.snapshotIndex(s.SnapshotID) >= 0 {
		return invalid("Snapshot already exists for id: %d", s.SnapshotID)
	}
	if b.m.FormatVersion != minFormatVersion && s.ParentSnapshotID != nil && s.SequenceNumber <= b.m.LastSequenceNumber {
		return invalid("Cannot add snapshot with sequence number %d older than last sequence number %d", s.SequenceNumber, b.m.LastSequenceNumber)
	}
	b.m.Snapshots = append(b.m.Snapshots, *s)
	b.m.LastSequenceNumber = max(b.m.LastSequenceNumber, s.SequenceNumber)
	b.addedSnapshots[s.SnapshotID] = true
	b.lastUpdatedMS = s.TimestampMS
	return nil
}

// setSnapshotRef creates or moves a branch or tag. Moving main makes the
// snapshot current and logs it.
func (b *builder) setSnapshotRef(u Update) error {
	ref := u.SnapshotRef
	if u.RefName == "" {
		return invalid("set-snapshot-ref needs a ref-name.")
	}
	i := b.m.snapshotIndex(ref.SnapshotID)
	if i < 0 {
		return invalid("Cannot set %s to unknown snapshot: %d", u.RefName, ref.SnapshotID)
	}
	if err := validateRef(u.RefName, ref); err != nil {
		return err
	}
	if existing, ok := b.m.Refs[u.RefName]; ok && equalJSON(existing, ref) {
		return nil
	}
	b.m.Refs[u.RefName] = ref
	if b.addedSnapshots[ref.SnapshotID] {
		b.lastUpdatedMS = b.m.Snapshots[i].TimestampMS
	}
	if u.RefName != MainBranch {
		return nil
	}
	if b.lastUpdatedMS == 0 {
		b.lastUpdatedMS = b.now.UnixMilli()
	}
	b.m.CurrentSnapshotID = ref.SnapshotID
	b.m.SnapshotLog = append(b.m.SnapshotLog, SnapshotLogEntry{TimestampMS: b.lastUpdatedMS, SnapshotID: ref.SnapshotID})
	b.mainHistory = append(b.mainHistory, ref.SnapshotID)
	return nil
}

func validateRef(name string, ref SnapshotRef) error {
	switch {
	case ref.Type != RefBranch && ref.Type != RefTag:
		return invalid("Invalid snapshot ref type %q: must be branch or tag.", ref.Type)
	case name == MainBranch && ref.Type != RefBranch:
		return invalid("Ref main must be a branch.")
	case ref.Type == RefTag && (ref.MinSnapshotsToKeep != nil || ref.MaxSnapshotAgeMS != nil):
		return invalid("Tags do not support setting min-snapshots-to-keep or max-snapshot-age-ms.")
	}
	return nil
}

// removeRef drops a branch or tag; dropping main leaves no current snapshot.
func (b *builder) removeRef(name string) {
	if name == MainBranch {
		b.m.CurrentSnapshotID = noSnapshot
	}
	delete(b.m.Refs, name)
}

// removeSnapshots drops snapshots, their statistics, and every ref left
// pointing at one of them.
func (b *builder) removeSnapshots(ids []int64) {
	b.m.Snapshots = slices.DeleteFunc(b.m.Snapshots, func(s Snapshot) bool { return slices.Contains(ids, s.SnapshotID) })
	for _, id := range ids {
		b.m.Statistics = withoutStatistics(b.m.Statistics, id)
		b.m.PartitionStatistics = withoutStatistics(b.m.PartitionStatistics, id)
	}
	for name, ref := range b.m.Refs {
		if b.m.snapshotIndex(ref.SnapshotID) < 0 {
			b.removeRef(name)
		}
	}
}

// setStatistics replaces the entry for the file's snapshot, or adds one.
func setStatistics(list *[]StatisticsFile, f *StatisticsFile, action string) error {
	if f == nil {
		return invalid("%s needs a statistics file.", action)
	}
	*list = append(withoutStatistics(*list, f.SnapshotID), *f)
	return nil
}

func withoutStatistics(list []StatisticsFile, snapshotID int64) []StatisticsFile {
	return slices.DeleteFunc(list, func(f StatisticsFile) bool { return f.SnapshotID == snapshotID })
}

// removeSpecs drops partition specs other than the default one.
func (b *builder) removeSpecs(ids []int) error {
	if slices.Contains(ids, b.m.DefaultSpecID) {
		return invalid("Cannot remove the default partition spec")
	}
	b.m.PartitionSpecs = slices.DeleteFunc(b.m.PartitionSpecs, func(s PartitionSpec) bool { return slices.Contains(ids, s.SpecID) })
	return nil
}

// removeSchemas drops schemas other than the current one.
func (b *builder) removeSchemas(ids []int) error {
	if slices.Contains(ids, b.m.CurrentSchemaID) {
		return invalid("Cannot remove the current schema")
	}
	b.m.Schemas = slices.DeleteFunc(b.m.Schemas, func(s Schema) bool { return slices.Contains(ids, s.SchemaID) })
	return nil
}
