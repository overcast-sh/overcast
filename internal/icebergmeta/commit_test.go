package icebergmeta

import (
	"encoding/json"
	"errors"
	"slices"
	"testing"
	"time"
)

const baseLocation = "s3://w--table-s3/metadata/00003-abc.metadata.json"

func ptr[T any](v T) *T { return &v }

// baseTable is a two-column v2 table with no snapshots, last updated at
// testNow.
func baseTable(t *testing.T) *Metadata {
	t.Helper()
	m, err := New(CreateSpec{
		TableUUID: "5f1a8f36-8b3a-4bcb-9b3a-1c2d3e4f5a6b",
		Location:  "s3://w--table-s3",
		Fields: []Field{
			{ID: 1, Name: "id", Type: PrimitiveType("long"), Required: true},
			{ID: 2, Name: "name", Type: PrimitiveType("string")},
		},
	}, testNow)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func snapshot(id int64, parent *int64, seq, ts int64) *Snapshot {
	return &Snapshot{SnapshotID: id, ParentSnapshotID: parent, SequenceNumber: seq, TimestampMS: ts,
		ManifestList: "s3://w--table-s3/metadata/snap.avro", Summary: map[string]string{"operation": "append"}}
}

func addSnapshot(s *Snapshot) Update { return Update{Action: "add-snapshot", Snapshot: s} }

func setMain(id int64) Update {
	return Update{Action: "set-snapshot-ref", RefName: MainBranch, SnapshotRef: SnapshotRef{SnapshotID: id, Type: RefBranch}}
}

func mustCommit(t *testing.T, base *Metadata, location string, now time.Time, updates ...Update) *Metadata {
	t.Helper()
	m, err := Commit(base, location, CommitRequest{Updates: updates}, now)
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}
	return m
}

func TestCommit_lastAddedResolvesToWhatTheCommitAdded(t *testing.T) {
	// Given: a table with schema 0
	base := baseTable(t)
	newSchema := Schema{Fields: append(base.Schemas[0].Fields, Field{ID: 3, Name: "note", Type: PrimitiveType("string")})}

	// When: a commit adds a schema and makes "the last added" current
	m := mustCommit(t, base, baseLocation, testNow,
		Update{Action: "add-schema", Schema: &newSchema},
		Update{Action: "set-current-schema", SchemaID: ptr(lastAdded)})

	// Then: the new schema is id 1, current, and the last column id grew
	if m.CurrentSchemaID != 1 || len(m.Schemas) != 2 || m.LastColumnID != 3 {
		t.Errorf("current = %d, schemas = %d, last column = %d", m.CurrentSchemaID, len(m.Schemas), m.LastColumnID)
	}
}

func TestCommit_reusesTheIDOfAnIdenticalSchemaSpecAndOrder(t *testing.T) {
	// Given: a table, and a schema, spec and order identical to ones it has
	base := baseTable(t)
	same := base.Schemas[0]
	same.SchemaID = 7
	adds := []Update{
		{Action: "add-schema", Schema: &same},
		{Action: "add-spec", Spec: &PartitionSpec{SpecID: 3}},
		{Action: "add-sort-order", SortOrder: &SortOrder{OrderID: 4}},
	}

	// When: a commit adds them again
	m := mustCommit(t, base, baseLocation, testNow, adds...)

	// Then: nothing was added, so the commit changes nothing
	if m != base {
		t.Errorf("Commit returned new metadata %+v, want base unchanged", m)
	}

	// And: -1 names nothing, because no add of this commit created anything
	for _, set := range []Update{
		{Action: "set-current-schema", SchemaID: ptr(lastAdded)},
		{Action: "set-default-spec", SpecID: ptr(lastAdded)},
		{Action: "set-default-sort-order", SortOrderID: ptr(lastAdded)},
	} {
		updates := append(slices.Clone(adds), set)
		if _, err := Commit(base, baseLocation, CommitRequest{Updates: updates}, testNow); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s -1 after re-adding: err = %v, want ErrInvalid", set.Action, err)
		}
	}
}

func TestCommit_lastAddedSurvivesReaddingWithinTheCommit(t *testing.T) {
	// Given: a commit that adds a new schema, then adds it again
	base := baseTable(t)
	next := Schema{Fields: append(base.Schemas[0].Fields, Field{ID: 3, Name: "note", Type: PrimitiveType("string")})}

	// When: -1 follows the second add
	m := mustCommit(t, base, baseLocation, testNow,
		Update{Action: "add-schema", Schema: &next},
		Update{Action: "add-schema", Schema: &next},
		Update{Action: "set-current-schema", SchemaID: ptr(lastAdded)})

	// Then: it names the schema the first add created
	if m.CurrentSchemaID != 1 || len(m.Schemas) != 2 {
		t.Errorf("current = %d, schemas = %d", m.CurrentSchemaID, len(m.Schemas))
	}
}

func TestCommit_partitionFieldIDsContinueFromTheLastAssigned(t *testing.T) {
	// Given: a table whose spec 1 already used partition field 1000
	base := baseTable(t)
	base = mustCommit(t, base, baseLocation, testNow,
		Update{Action: "add-spec", Spec: &PartitionSpec{Fields: []PartitionField{{SourceID: 1, Name: "id_b", Transform: "bucket[4]"}}}})

	// When: another spec is added with fields that carry no ids
	m := mustCommit(t, base, baseLocation, testNow,
		Update{Action: "add-spec", Spec: &PartitionSpec{Fields: []PartitionField{
			{SourceID: 1, Name: "id_b", Transform: "bucket[4]"},
			{SourceID: 2, Name: "name", Transform: "identity"},
		}}},
		Update{Action: "set-default-spec", SpecID: ptr(lastAdded)})

	// Then: the first spec got 1000, the new one gets ids after it
	if got := base.PartitionSpecs[1].Fields[0].FieldID; got != 1000 {
		t.Fatalf("first spec field id = %d, want 1000", got)
	}
	spec := m.PartitionSpecs[2]
	if m.DefaultSpecID != 2 || spec.Fields[0].FieldID != 1001 || spec.Fields[1].FieldID != 1002 || m.LastPartitionID != 1002 {
		t.Errorf("default = %d, spec = %+v, last = %d", m.DefaultSpecID, spec, m.LastPartitionID)
	}
}

func TestCommit_snapshotRules(t *testing.T) {
	later := testNow.Add(time.Minute)
	first := snapshot(1, nil, 1, testNow.UnixMilli()+10)
	cases := map[string]struct {
		updates []Update
		wantErr error
		check   func(*testing.T, *Metadata)
	}{
		"an appended snapshot becomes current and stamps the commit": {
			updates: []Update{addSnapshot(first), setMain(1)},
			check: func(t *testing.T, m *Metadata) {
				if m.CurrentSnapshotID != 1 || m.LastSequenceNumber != 1 || m.LastUpdatedMS != first.TimestampMS {
					t.Errorf("current = %d, seq = %d, updated = %d", m.CurrentSnapshotID, m.LastSequenceNumber, m.LastUpdatedMS)
				}
				if len(m.SnapshotLog) != 1 || m.SnapshotLog[0] != (SnapshotLogEntry{TimestampMS: first.TimestampMS, SnapshotID: 1}) {
					t.Errorf("snapshot log = %+v", m.SnapshotLog)
				}
			},
		},
		"a snapshot replaced on main within the commit leaves no log entry": {
			updates: []Update{addSnapshot(first), setMain(1), addSnapshot(snapshot(2, ptr[int64](1), 2, first.TimestampMS+1)), setMain(2)},
			check: func(t *testing.T, m *Metadata) {
				if len(m.SnapshotLog) != 1 || m.SnapshotLog[0].SnapshotID != 2 {
					t.Errorf("snapshot log = %+v, want only snapshot 2", m.SnapshotLog)
				}
			},
		},
		"a child snapshot must advance the sequence number": {
			updates: []Update{addSnapshot(first), addSnapshot(snapshot(2, ptr[int64](1), 1, first.TimestampMS))},
			wantErr: ErrInvalid,
		},
		"a snapshot id cannot be added twice": {
			updates: []Update{addSnapshot(first), addSnapshot(first)},
			wantErr: ErrInvalid,
		},
		"a ref must point at a snapshot the table has": {
			updates: []Update{setMain(42)},
			wantErr: ErrInvalid,
		},
		"main must be a branch": {
			updates: []Update{addSnapshot(first), {Action: "set-snapshot-ref", RefName: MainBranch, SnapshotRef: SnapshotRef{SnapshotID: 1, Type: RefTag}}},
			wantErr: ErrInvalid,
		},
		"an old snapshot timestamp never moves last-updated-ms backwards": {
			updates: []Update{addSnapshot(snapshot(1, nil, 1, testNow.UnixMilli()-1000)), setMain(1)},
			check: func(t *testing.T, m *Metadata) {
				if m.LastUpdatedMS != testNow.UnixMilli() {
					t.Errorf("last-updated-ms = %d, want the base's %d", m.LastUpdatedMS, testNow.UnixMilli())
				}
			},
		},
		"a commit without a snapshot is stamped now": {
			updates: []Update{{Action: "set-properties", Updates: map[string]string{"k": "v"}}},
			check: func(t *testing.T, m *Metadata) {
				if m.LastUpdatedMS != later.UnixMilli() {
					t.Errorf("last-updated-ms = %d, want %d", m.LastUpdatedMS, later.UnixMilli())
				}
			},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			// Given: a table with no snapshots
			base := baseTable(t)

			// When: the commit is applied
			m, err := Commit(base, baseLocation, CommitRequest{Updates: tc.updates}, later)

			// Then: it fails as the spec says, or produces the expected table
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("err = %v, want %v", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Commit: %v", err)
			}
			tc.check(t, m)
		})
	}
}

func TestCommit_removeSnapshotsPrunesRefsStatisticsAndHistory(t *testing.T) {
	// Given: snapshots 1 → 2 → 3, each made current in turn, a tag on 2 and
	// statistics for 2
	base := baseTable(t)
	ts := testNow.UnixMilli()
	base = mustCommit(t, base, baseLocation, testNow, addSnapshot(snapshot(1, nil, 1, ts+1)), setMain(1))
	base = mustCommit(t, base, baseLocation, testNow, addSnapshot(snapshot(2, ptr[int64](1), 2, ts+2)), setMain(2))
	base = mustCommit(t, base, baseLocation, testNow, addSnapshot(snapshot(3, ptr[int64](2), 3, ts+3)), setMain(3))
	var stats StatisticsFile
	if err := json.Unmarshal([]byte(`{"snapshot-id":2,"statistics-path":"s3://w--table-s3/stats.puffin"}`), &stats); err != nil {
		t.Fatal(err)
	}
	base = mustCommit(t, base, baseLocation, testNow,
		Update{Action: "set-snapshot-ref", RefName: "v2", SnapshotRef: SnapshotRef{SnapshotID: 2, Type: RefTag}},
		Update{Action: "set-statistics", Statistics: &stats})

	// When: snapshot 2 is removed
	m := mustCommit(t, base, baseLocation, testNow, Update{Action: "remove-snapshots", SnapshotIDs: []int64{2}})

	// Then: its tag and statistics go, and so does the history up to and
	// including it — keeping snapshot 1's entry would claim 1 was current
	// while 2 was
	if _, ok := m.Refs["v2"]; ok || len(m.Statistics) != 0 {
		t.Errorf("refs = %v, statistics = %v", m.Refs, m.Statistics)
	}
	if len(m.SnapshotLog) != 1 || m.SnapshotLog[0].SnapshotID != 3 {
		t.Errorf("snapshot log = %+v, want only snapshot 3", m.SnapshotLog)
	}
	if m.CurrentSnapshotID != 3 || len(m.Snapshots) != 2 {
		t.Errorf("current = %d, snapshots = %d", m.CurrentSnapshotID, len(m.Snapshots))
	}
}

func TestCommit_removingMainLeavesNoCurrentSnapshot(t *testing.T) {
	// Given: a table whose main branch points at snapshot 1
	base := mustCommit(t, baseTable(t), baseLocation, testNow, addSnapshot(snapshot(1, nil, 1, testNow.UnixMilli())), setMain(1))

	// When: main is removed
	m := mustCommit(t, base, baseLocation, testNow, Update{Action: "remove-snapshot-ref", RefName: MainBranch})

	// Then: there is no current snapshot
	if m.CurrentSnapshotID != noSnapshot {
		t.Errorf("current snapshot = %d", m.CurrentSnapshotID)
	}
}

func TestCommit_metadataLogKeepsPreviousVersionsMax(t *testing.T) {
	// Given: a table that keeps two previous metadata files
	m := baseTable(t)
	m.Properties[propPreviousVersionsMax] = "2"

	// When: three commits follow, each from the file the previous one wrote
	for i, loc := range []string{"f0", "f1", "f2"} {
		m = mustCommit(t, m, loc, testNow.Add(time.Duration(i+1)*time.Second),
			Update{Action: "set-properties", Updates: map[string]string{"n": loc}})
	}

	// Then: the log names the last two, oldest first
	if len(m.MetadataLog) != 2 || m.MetadataLog[0].MetadataFile != "f1" || m.MetadataLog[1].MetadataFile != "f2" {
		t.Errorf("metadata log = %+v", m.MetadataLog)
	}
	if m.MetadataLog[1].TimestampMS != testNow.Add(2*time.Second).UnixMilli() {
		t.Errorf("an entry carries the timestamp of the metadata it names: %+v", m.MetadataLog[1])
	}
}

func TestCommit_formatVersionOnlyUpgrades(t *testing.T) {
	cases := map[string]struct {
		from, to int
		wantErr  bool
	}{
		"v1 to v2":        {from: 1, to: 2},
		"same version":    {from: 2, to: 2},
		"downgrade":       {from: 2, to: 1, wantErr: true},
		"unsupported v3":  {from: 2, to: 3, wantErr: true},
		"invalid version": {from: 1, to: 0, wantErr: true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			// Given: a table at version from
			base := baseTable(t)
			base.FormatVersion = tc.from

			// When: it is asked to become version to
			m, err := Commit(base, baseLocation, CommitRequest{Updates: []Update{{Action: "upgrade-format-version", FormatVersion: tc.to}}}, testNow)

			// Then: only a supported upgrade (or a no-op) succeeds
			if tc.wantErr {
				if !errors.Is(err, ErrInvalid) {
					t.Fatalf("err = %v, want ErrInvalid", err)
				}
				return
			}
			if err != nil || m.FormatVersion != tc.to {
				t.Fatalf("m = %v, err = %v", m, err)
			}
		})
	}
}

func TestCommit_requirements(t *testing.T) {
	base := baseTable(t)
	cases := map[string]struct {
		base *Metadata
		req  Requirement
		fail bool
	}{
		"assert-create on a new table":            {req: Requirement{Type: AssertCreate}},
		"assert-create on an existing table":      {base: base, req: Requirement{Type: AssertCreate}, fail: true},
		"assert-table-uuid matches":               {base: base, req: Requirement{Type: AssertTableUUID, UUID: base.TableUUID}},
		"assert-table-uuid differs":               {base: base, req: Requirement{Type: AssertTableUUID, UUID: "other"}, fail: true},
		"assert-ref-snapshot-id absent as null":   {base: base, req: Requirement{Type: AssertRefSnapshotID, Ref: MainBranch}},
		"assert-ref-snapshot-id absent but named": {base: base, req: Requirement{Type: AssertRefSnapshotID, Ref: MainBranch, SnapshotID: ptr[int64](1)}, fail: true},
		"assert-last-assigned-field-id":           {base: base, req: Requirement{Type: AssertLastAssignedFieldID, LastAssignedFieldID: ptr(2)}},
		"assert-last-assigned-field-id stale":     {base: base, req: Requirement{Type: AssertLastAssignedFieldID, LastAssignedFieldID: ptr(1)}, fail: true},
		"assert-current-schema-id stale":          {base: base, req: Requirement{Type: AssertCurrentSchemaID, CurrentSchemaID: ptr(1)}, fail: true},
		"assert-last-assigned-partition-id":       {base: base, req: Requirement{Type: AssertLastAssignedPartitionID, LastAssignedPartitionID: ptr(999)}},
		"assert-default-spec-id stale":            {base: base, req: Requirement{Type: AssertDefaultSpecID, DefaultSpecID: ptr(1)}, fail: true},
		"assert-default-sort-order-id":            {base: base, req: Requirement{Type: AssertDefaultSortOrderID, DefaultSortOrderID: ptr(0)}},
		"a table requirement on a missing table":  {req: Requirement{Type: AssertCurrentSchemaID, CurrentSchemaID: ptr(0)}, fail: true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			// Given: the table (or none) and one requirement
			// When: a commit carrying only that requirement is checked
			err := tc.req.check(tc.base)

			// Then: it fails as a requirement, not as an invalid request
			if tc.fail != errors.Is(err, ErrRequirementFailed) || (!tc.fail && err != nil) {
				t.Errorf("err = %v, want failure = %v", err, tc.fail)
			}
		})
	}
}

func TestCommit_failedRequirementAppliesNothing(t *testing.T) {
	// Given: a commit whose requirement the table does not meet
	base := baseTable(t)
	req := CommitRequest{
		Requirements: []Requirement{{Type: AssertCurrentSchemaID, CurrentSchemaID: ptr(9)}},
		Updates:      []Update{{Action: "set-properties", Updates: map[string]string{"k": "v"}}},
	}

	// When: it is applied
	_, err := Commit(base, baseLocation, req, testNow)

	// Then: it fails as a conflict and the table is untouched
	if !errors.Is(err, ErrRequirementFailed) || len(base.Properties) != 0 {
		t.Errorf("err = %v, properties = %v", err, base.Properties)
	}
}

func TestCommit_refusesWhatTheSpecForbids(t *testing.T) {
	cases := map[string]Update{
		"an unknown action":               {Action: "enable-row-lineage"},
		"set-current-schema -1 with none": {Action: "set-current-schema", SchemaID: ptr(lastAdded)},
		"an unknown schema":               {Action: "set-current-schema", SchemaID: ptr(5)},
		"a schema with a bad type":        {Action: "add-schema", Schema: &Schema{Fields: []Field{{ID: 1, Name: "a", Type: PrimitiveType("varchar")}}}},
		"a spec on an unknown column":     {Action: "add-spec", Spec: &PartitionSpec{Fields: []PartitionField{{SourceID: 9, Name: "p", Transform: "identity"}}}},
		"a last column id going back":     {Action: "add-schema", Schema: &Schema{Fields: []Field{{ID: 1, Name: "a", Type: PrimitiveType("int")}}}, LastColumnID: ptr(1)},
		"removing the current schema":     {Action: "remove-schemas", SchemaIDs: []int{0}},
		"removing the default spec":       {Action: "remove-partition-specs", SpecIDs: []int{0}},
		"an empty location":               {Action: "set-location"},
		"an optional identifier field": {Action: "add-schema", Schema: &Schema{
			Fields: []Field{{ID: 1, Name: "id", Type: PrimitiveType("long")}}, IdentifierFieldIDs: []int{1},
		}},
		"a double identifier field": {Action: "add-schema", Schema: &Schema{
			Fields: []Field{{ID: 1, Name: "x", Type: PrimitiveType("double"), Required: true}}, IdentifierFieldIDs: []int{1},
		}},
	}
	for name, u := range cases {
		t.Run(name, func(t *testing.T) {
			// Given: a table, and one update the spec does not allow
			// When: it is applied
			_, err := Commit(baseTable(t), baseLocation, CommitRequest{Updates: []Update{u}}, testNow)

			// Then: it is refused as invalid
			if !errors.Is(err, ErrInvalid) {
				t.Errorf("err = %v, want ErrInvalid", err)
			}
		})
	}
}

func TestCommit_holdsNestedReferencesToTheSpecsRules(t *testing.T) {
	// Given: a table whose current schema adds a list of strings, tags (id 3,
	// element 4)
	withList := &Schema{SchemaID: 1, Fields: []Field{
		{ID: 1, Name: "id", Type: PrimitiveType("long"), Required: true},
		{ID: 2, Name: "name", Type: PrimitiveType("string")},
		{ID: 3, Name: "tags", Type: Type{List: &ListType{ElementID: 4, Element: PrimitiveType("string")}}},
	}}
	base := mustCommit(t, baseTable(t), baseLocation, testNow,
		Update{Action: "add-schema", Schema: withList},
		Update{Action: "set-current-schema", SchemaID: ptr(lastAdded)})

	// When: a partition spec on the list element is added
	_, err := Commit(base, baseLocation, CommitRequest{Updates: []Update{
		{Action: "add-spec", Spec: &PartitionSpec{Fields: []PartitionField{{SourceID: 4, Name: "tag", Transform: "identity"}}}},
	}}, testNow)

	// Then: it is refused, as the spec forbids a partition source in a list
	if !errors.Is(err, ErrInvalid) {
		t.Errorf("add-spec err = %v, want ErrInvalid", err)
	}

	// And: a sort order on the same element is accepted, as the reference
	// implementation accepts it
	m := mustCommit(t, base, baseLocation, testNow, Update{Action: "add-sort-order", SortOrder: &SortOrder{OrderID: 1, Fields: []SortField{
		{SourceID: 4, Transform: "identity", Direction: "asc", NullOrder: "nulls-first"},
	}}})
	if !m.hasSortOrder(1) {
		t.Errorf("sort orders = %+v", m.SortOrders)
	}
}

func TestCommit_aCreateMustAssignUUIDAndLocation(t *testing.T) {
	// Given: a create commit that never assigns a location
	req := CommitRequest{
		Requirements: []Requirement{{Type: AssertCreate}},
		Updates: []Update{
			{Action: "assign-uuid", UUID: "u"},
			{Action: "add-schema", Schema: &Schema{Fields: []Field{{ID: 1, Name: "a", Type: PrimitiveType("int")}}}},
			{Action: "set-current-schema", SchemaID: ptr(lastAdded)},
			{Action: "add-spec", Spec: &PartitionSpec{}},
			{Action: "set-default-spec", SpecID: ptr(lastAdded)},
			{Action: "add-sort-order", SortOrder: &SortOrder{}},
			{Action: "set-default-sort-order", SortOrderID: ptr(lastAdded)},
		},
	}

	// When: it is applied to no table
	_, err := Commit(nil, "", req, testNow)

	// Then: the result is not a whole table
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("err = %v, want ErrInvalid", err)
	}

	// And: with a location it is a version 2 table
	req.Updates = append(req.Updates, Update{Action: "set-location", Location: "s3://w--table-s3"})
	m, err := Commit(nil, "", req, testNow)
	if err != nil || m.FormatVersion != FormatVersion || len(m.MetadataLog) != 0 {
		t.Fatalf("m = %+v, err = %v", m, err)
	}
}

func TestNextMetadataVersion(t *testing.T) {
	cases := map[string]int{
		"": 0,
		"s3://w/metadata/00007-uuid.metadata.json": 8,
		"s3://w/metadata/v1.metadata.json":         0,
		"s3://w/metadata/12345-a-b.metadata.json":  12346,
	}
	for location, want := range cases {
		// Given/When: the version after the file at location
		// Then: it is one more than the file's own, or 0
		if got := NextMetadataVersion(location); got != want {
			t.Errorf("NextMetadataVersion(%q) = %d, want %d", location, got, want)
		}
	}
}
