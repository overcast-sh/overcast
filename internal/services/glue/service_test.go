package glue

import (
	"context"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/overcast-sh/overcast/internal/clock"
	"github.com/overcast-sh/overcast/internal/config"
	"github.com/overcast-sh/overcast/internal/protocol"
	"github.com/overcast-sh/overcast/internal/state"
)

func newTestService(t *testing.T) (*Service, state.Store, *clock.Mock) {
	t.Helper()
	st := state.NewMemoryStore()
	clk := clock.NewMock()
	clk.Set(time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC))
	cfg := &config.Config{Region: "us-east-1", AccountID: "123456789012"}
	return New(cfg, st, zap.NewNop(), clk), st, clk
}

func mustOK(t *testing.T, what string, aerr *protocol.AWSError) {
	t.Helper()
	if aerr != nil {
		t.Fatalf("%s: %s: %s", what, aerr.Code, aerr.Message)
	}
}

func wantCode(t *testing.T, what string, aerr *protocol.AWSError, code string) {
	t.Helper()
	if aerr == nil {
		t.Fatalf("%s succeeded, want %s", what, code)
	}
	if aerr.Code != code {
		t.Fatalf("%s: code = %s (%s), want %s", what, aerr.Code, aerr.Message, code)
	}
	if code != protocol.ErrNotImplemented.Code && aerr.HTTPStatus != 400 {
		t.Fatalf("%s: HTTP status = %d, want 400", what, aerr.HTTPStatus)
	}
}

// seedTable creates database db and a table partitioned by (year int, month string).
func seedTable(t *testing.T, s *Service, db, table string) {
	t.Helper()
	ctx := context.Background()
	if _, found, _ := s.store.getDatabase(ctx, db); !found {
		_, aerr := s.createDatabaseTyped(ctx, &createDatabaseReq{DatabaseInput: &DatabaseInput{Name: db}})
		mustOK(t, "CreateDatabase", aerr)
	}
	_, aerr := s.createTableTyped(ctx, &createTableReq{DatabaseName: db, TableInput: &TableInput{
		Name:              table,
		StorageDescriptor: &StorageDescriptor{Location: "s3://bucket/" + table + "/", Columns: []Column{{Name: "id", Type: "bigint"}}},
		PartitionKeys:     []Column{{Name: "year", Type: "int"}, {Name: "month", Type: "string"}},
		Parameters:        map[string]string{"classification": "parquet"},
	}})
	mustOK(t, "CreateTable", aerr)
}

func TestUpdateTable_versionConcurrencyAndArchive(t *testing.T) {
	// Given: a table at version 0
	s, _, clk := newTestService(t)
	ctx := context.Background()
	seedTable(t, s, "db", "events")
	created, aerr := s.getTableTyped(ctx, &getTableReq{DatabaseName: "db", Name: "events"})
	mustOK(t, "GetTable", aerr)
	if created.Table.VersionId != "0" {
		t.Fatalf("new table VersionId = %q, want 0", created.Table.VersionId)
	}

	// When: it is updated against version 0
	clk.Add(time.Minute)
	in := &TableInput{Name: "events", Parameters: map[string]string{"metadata_location": "s3://b/m1.json"}}
	_, aerr = s.updateTableTyped(ctx, &updateTableReq{DatabaseName: "db", TableInput: in, VersionId: "0"})
	mustOK(t, "UpdateTable v0", aerr)

	// Then: the table is at version 1 with the new definition and its
	// original CreateTime, and version 0 is archived
	got, aerr := s.getTableTyped(ctx, &getTableReq{DatabaseName: "db", Name: "events"})
	mustOK(t, "GetTable", aerr)
	if got.Table.VersionId != "1" || got.Table.Parameters["metadata_location"] != "s3://b/m1.json" {
		t.Fatalf("after update: VersionId=%q Parameters=%v", got.Table.VersionId, got.Table.Parameters)
	}
	if got.Table.CreateTime != created.Table.CreateTime || got.Table.UpdateTime <= created.Table.UpdateTime {
		t.Fatalf("CreateTime %v→%v, UpdateTime %v→%v", created.Table.CreateTime, got.Table.CreateTime, created.Table.UpdateTime, got.Table.UpdateTime)
	}
	v0, aerr := s.getTableVersionTyped(ctx, &getTableVersionReq{DatabaseName: "db", TableName: "events", VersionId: "0"})
	mustOK(t, "GetTableVersion 0", aerr)
	if v0.TableVersion.Table.StorageDescriptor == nil || v0.TableVersion.Table.StorageDescriptor.Location != "s3://bucket/events/" {
		t.Fatalf("archived version lost its StorageDescriptor: %+v", v0.TableVersion.Table)
	}

	// When: a second writer commits against the stale version 0
	_, aerr = s.updateTableTyped(ctx, &updateTableReq{DatabaseName: "db", TableInput: in, VersionId: "0"})
	// Then: ConcurrentModificationException, and nothing changed
	wantCode(t, "UpdateTable stale", aerr, codeConcurrentModification)

	// When: updated with SkipArchive
	_, aerr = s.updateTableTyped(ctx, &updateTableReq{DatabaseName: "db", TableInput: in, VersionId: "1", SkipArchive: ptr(true)})
	mustOK(t, "UpdateTable skip archive", aerr)

	// Then: version 1 was not archived; versions are 2 (current) and 0
	versions, aerr := s.getTableVersionsTyped(ctx, &getTableVersionsReq{DatabaseName: "db", TableName: "events"})
	mustOK(t, "GetTableVersions", aerr)
	var ids []string
	for _, v := range versions.TableVersions {
		ids = append(ids, v.VersionId)
	}
	if len(ids) != 2 || ids[0] != "2" || ids[1] != "0" {
		t.Fatalf("versions = %v, want [2 0]", ids)
	}
}

func TestDeleteTableVersion_refusesTheCurrentVersion(t *testing.T) {
	// Given: a table updated once (version 1 current, version 0 archived)
	s, _, _ := newTestService(t)
	ctx := context.Background()
	seedTable(t, s, "db", "t")
	_, aerr := s.updateTableTyped(ctx, &updateTableReq{DatabaseName: "db", TableInput: &TableInput{Name: "t"}})
	mustOK(t, "UpdateTable", aerr)

	// When/Then: deleting the current version is refused
	_, aerr = s.deleteTableVersionTyped(ctx, &deleteTableVersionReq{DatabaseName: "db", TableName: "t", VersionId: "1"})
	wantCode(t, "DeleteTableVersion current", aerr, codeInvalidInput)

	// When: the archived version and a missing one are batch-deleted
	resp, aerr := s.batchDeleteTableVersionTyped(ctx, &batchDeleteTableVersionReq{DatabaseName: "db", TableName: "t", VersionIds: []string{"0", "7"}})
	mustOK(t, "BatchDeleteTableVersion", aerr)

	// Then: only the missing one is reported, and version 0 is gone
	if len(resp.Errors) != 1 || resp.Errors[0].VersionId != "7" || resp.Errors[0].ErrorDetail.ErrorCode != codeEntityNotFound {
		t.Fatalf("Errors = %+v", resp.Errors)
	}
	_, aerr = s.getTableVersionTyped(ctx, &getTableVersionReq{DatabaseName: "db", TableName: "t", VersionId: "0"})
	wantCode(t, "GetTableVersion deleted", aerr, codeEntityNotFound)
}

func TestCreate_duplicatesAndMissingParents(t *testing.T) {
	s, _, _ := newTestService(t)
	ctx := context.Background()
	seedTable(t, s, "db", "t")

	cases := []struct {
		name string
		call func() *protocol.AWSError
		code string
	}{
		{"duplicate database, differently cased", func() *protocol.AWSError {
			_, aerr := s.createDatabaseTyped(ctx, &createDatabaseReq{DatabaseInput: &DatabaseInput{Name: "DB"}})
			return aerr
		}, codeAlreadyExists},
		{"duplicate table", func() *protocol.AWSError {
			_, aerr := s.createTableTyped(ctx, &createTableReq{DatabaseName: "db", TableInput: &TableInput{Name: "T"}})
			return aerr
		}, codeAlreadyExists},
		{"table in missing database", func() *protocol.AWSError {
			_, aerr := s.createTableTyped(ctx, &createTableReq{DatabaseName: "nope", TableInput: &TableInput{Name: "t"}})
			return aerr
		}, codeEntityNotFound},
		{"duplicate partition", func() *protocol.AWSError {
			in := &PartitionInput{Values: []string{"2024", "01"}}
			if _, aerr := s.createPartitionTyped(ctx, &createPartitionReq{DatabaseName: "db", TableName: "t", PartitionInput: in}); aerr != nil {
				return aerr
			}
			_, aerr := s.createPartitionTyped(ctx, &createPartitionReq{DatabaseName: "db", TableName: "t", PartitionInput: in})
			return aerr
		}, codeAlreadyExists},
		{"partition value count mismatch", func() *protocol.AWSError {
			_, aerr := s.createPartitionTyped(ctx, &createPartitionReq{DatabaseName: "db", TableName: "t", PartitionInput: &PartitionInput{Values: []string{"2024"}}})
			return aerr
		}, codeInvalidInput},
		{"partition of missing table", func() *protocol.AWSError {
			_, aerr := s.createPartitionTyped(ctx, &createPartitionReq{DatabaseName: "db", TableName: "nope", PartitionInput: &PartitionInput{Values: []string{"1", "2"}}})
			return aerr
		}, codeEntityNotFound},
		{"unsupported partition expression", func() *protocol.AWSError {
			_, aerr := s.getPartitionsTyped(ctx, &getPartitionsReq{DatabaseName: "db", TableName: "t", Expression: "year = month"})
			return aerr
		}, codeInvalidInput},
		{"iceberg metadata update", func() *protocol.AWSError {
			_, aerr := s.updateTableTyped(ctx, &updateTableReq{DatabaseName: "db", Name: "t", UpdateOpenTableFormatInput: map[string]any{"UpdateIcebergInput": map[string]any{}}})
			return aerr
		}, protocol.ErrNotImplemented.Code},
		{"table rename", func() *protocol.AWSError {
			_, aerr := s.updateTableTyped(ctx, &updateTableReq{DatabaseName: "db", Name: "t", TableInput: &TableInput{Name: "other"}})
			return aerr
		}, codeInvalidInput},
		{"database rename", func() *protocol.AWSError {
			_, aerr := s.updateDatabaseTyped(ctx, &updateDatabaseReq{Name: "db", DatabaseInput: &DatabaseInput{Name: "other"}})
			return aerr
		}, codeInvalidInput},
		{"bad next token", func() *protocol.AWSError {
			_, aerr := s.getTablesTyped(ctx, &getTablesReq{DatabaseName: "db", NextToken: "garbage"})
			return aerr
		}, codeInvalidInput},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			wantCode(t, tc.name, tc.call(), tc.code)
		})
	}
}

func TestDeleteDatabase_cascadesToTablesPartitionsAndVersions(t *testing.T) {
	// Given: a database with a partitioned, versioned table, and a second
	// database whose name shares a prefix
	s, st, _ := newTestService(t)
	ctx := context.Background()
	seedTable(t, s, "db", "t")
	seedTable(t, s, "db2", "t")
	_, aerr := s.createPartitionTyped(ctx, &createPartitionReq{DatabaseName: "db", TableName: "t", PartitionInput: &PartitionInput{Values: []string{"2024", "01"}}})
	mustOK(t, "CreatePartition", aerr)
	_, aerr = s.updateTableTyped(ctx, &updateTableReq{DatabaseName: "db", TableInput: &TableInput{Name: "t", PartitionKeys: []Column{{Name: "year", Type: "int"}, {Name: "month"}}}})
	mustOK(t, "UpdateTable", aerr)

	// When: the database is deleted and recreated
	_, aerr = s.deleteDatabaseTyped(ctx, &deleteDatabaseReq{Name: "db"})
	mustOK(t, "DeleteDatabase", aerr)
	_, aerr = s.createDatabaseTyped(ctx, &createDatabaseReq{DatabaseInput: &DatabaseInput{Name: "db"}})
	mustOK(t, "CreateDatabase again", aerr)

	// Then: nothing of the old database survives, and db2 is untouched
	for _, ns := range []string{nsTables, nsPartitions, nsTableVersions} {
		pairs, err := st.Scan(ctx, ns, "db/")
		if err != nil {
			t.Fatal(err)
		}
		if len(pairs) != 0 {
			t.Errorf("%s still holds %d records under db/", ns, len(pairs))
		}
	}
	if _, aerr := s.getTableTyped(ctx, &getTableReq{DatabaseName: "db2", Name: "t"}); aerr != nil {
		t.Errorf("db2.t was removed with db: %s", aerr.Message)
	}
}

func TestDeleteTable_cascadesToPartitionsAndVersions(t *testing.T) {
	// Given: tables "t" and "t2" in one database, both with partitions
	s, st, _ := newTestService(t)
	ctx := context.Background()
	seedTable(t, s, "db", "t")
	seedTable(t, s, "db", "t2")
	for _, table := range []string{"t", "t2"} {
		_, aerr := s.createPartitionTyped(ctx, &createPartitionReq{DatabaseName: "db", TableName: table, PartitionInput: &PartitionInput{Values: []string{"2024", "a/b"}}})
		mustOK(t, "CreatePartition", aerr)
	}
	_, aerr := s.updateTableTyped(ctx, &updateTableReq{DatabaseName: "db", TableInput: &TableInput{Name: "t"}})
	mustOK(t, "UpdateTable", aerr)

	// When: t is deleted
	_, aerr = s.deleteTableTyped(ctx, &deleteTableReq{DatabaseName: "db", Name: "t"})
	mustOK(t, "DeleteTable", aerr)

	// Then: its partitions and versions are gone and t2's partition is not
	for _, ns := range []string{nsPartitions, nsTableVersions} {
		pairs, _ := st.Scan(ctx, ns, "db/t/")
		if len(pairs) != 0 {
			t.Errorf("%s still holds %d records for db/t", ns, len(pairs))
		}
	}
	resp, aerr := s.getPartitionTyped(ctx, &getPartitionReq{DatabaseName: "db", TableName: "t2", PartitionValues: []string{"2024", "a/b"}})
	mustOK(t, "GetPartition t2", aerr)
	if resp.Partition.Values[1] != "a/b" {
		t.Errorf("partition value with a slash round-tripped as %q", resp.Partition.Values[1])
	}
}

func TestMalformedRecords_areIsolated(t *testing.T) {
	// Given: one good record and one corrupt record in every namespace
	s, st, _ := newTestService(t)
	ctx := context.Background()
	seedTable(t, s, "db", "good")
	_, aerr := s.createPartitionTyped(ctx, &createPartitionReq{DatabaseName: "db", TableName: "good", PartitionInput: &PartitionInput{Values: []string{"2024", "01"}}})
	mustOK(t, "CreatePartition", aerr)
	_, aerr = s.updateTableTyped(ctx, &updateTableReq{DatabaseName: "db", TableInput: &TableInput{Name: "good", PartitionKeys: []Column{{Name: "year", Type: "int"}, {Name: "month"}}}})
	mustOK(t, "UpdateTable", aerr)
	for ns, key := range map[string]string{
		nsDatabases:     "broken",
		nsTables:        "db/broken",
		nsPartitions:    "db/good/9999/99",
		nsTableVersions: "db/good/5",
	} {
		if err := st.Set(ctx, ns, key, "{not json"); err != nil {
			t.Fatal(err)
		}
	}

	// When/Then: every list skips the corrupt record and returns the rest
	dbs, aerr := s.getDatabasesTyped(ctx, &getDatabasesReq{})
	mustOK(t, "GetDatabases", aerr)
	if len(dbs.DatabaseList) != 1 {
		t.Errorf("GetDatabases returned %d databases, want 1", len(dbs.DatabaseList))
	}
	tables, aerr := s.getTablesTyped(ctx, &getTablesReq{DatabaseName: "db"})
	mustOK(t, "GetTables", aerr)
	if len(tables.TableList) != 1 {
		t.Errorf("GetTables returned %d tables, want 1", len(tables.TableList))
	}
	parts, aerr := s.getPartitionsTyped(ctx, &getPartitionsReq{DatabaseName: "db", TableName: "good"})
	mustOK(t, "GetPartitions", aerr)
	if len(parts.Partitions) != 1 {
		t.Errorf("GetPartitions returned %d partitions, want 1", len(parts.Partitions))
	}
	versions, aerr := s.getTableVersionsTyped(ctx, &getTableVersionsReq{DatabaseName: "db", TableName: "good"})
	mustOK(t, "GetTableVersions", aerr)
	if len(versions.TableVersions) != 2 {
		t.Errorf("GetTableVersions returned %d versions, want 2", len(versions.TableVersions))
	}

	// And a named read of the corrupt record is a modeled not-found
	_, aerr = s.getTableTyped(ctx, &getTableReq{DatabaseName: "db", Name: "broken"})
	wantCode(t, "GetTable broken", aerr, codeEntityNotFound)

	// And deleting the database clears the corrupt children too
	_, aerr = s.deleteDatabaseTyped(ctx, &deleteDatabaseReq{Name: "db"})
	mustOK(t, "DeleteDatabase", aerr)
	for _, ns := range []string{nsTables, nsPartitions, nsTableVersions} {
		if pairs, _ := st.Scan(ctx, ns, "db/"); len(pairs) != 0 {
			t.Errorf("%s kept %d records after DeleteDatabase", ns, len(pairs))
		}
	}
}

func TestOldShapeTableRecord_decodesWithDefaults(t *testing.T) {
	// Given: a table stored by the pre-#2064 service, which kept five fields
	s, st, _ := newTestService(t)
	ctx := context.Background()
	_, aerr := s.createDatabaseTyped(ctx, &createDatabaseReq{DatabaseInput: &DatabaseInput{Name: "db"}})
	mustOK(t, "CreateDatabase", aerr)
	old := `{"Name":"legacy","DatabaseName":"db","Description":"d","TableType":"EXTERNAL_TABLE","CatalogId":"123456789012","overcastTags":{"k":"v"}}`
	if err := st.Set(ctx, nsTables, "db/legacy", old); err != nil {
		t.Fatal(err)
	}

	// When: it is read and then updated
	got, aerr := s.getTableTyped(ctx, &getTableReq{DatabaseName: "db", Name: "legacy"})
	mustOK(t, "GetTable", aerr)

	// Then: it reads as version 0 with empty PartitionKeys, and updates
	// against that version while keeping its tags
	if got.Table.VersionId != "0" || got.Table.PartitionKeys == nil || got.Table.Description != "d" {
		t.Fatalf("legacy table = %+v", got.Table)
	}
	_, aerr = s.updateTableTyped(ctx, &updateTableReq{DatabaseName: "db", VersionId: "0", TableInput: &TableInput{Name: "legacy"}})
	mustOK(t, "UpdateTable", aerr)
	tags, aerr := s.listTagsForResourceTyped(ctx, &glueListTagsForResourceReq{ResourceArn: "arn:aws:glue:us-east-1:123456789012:table/db/legacy"})
	mustOK(t, "GetTags", aerr)
	if tags.Tags["k"] != "v" {
		t.Errorf("tags after update = %v", tags.Tags)
	}
}

func TestUpdatePartition_movesToNewValues(t *testing.T) {
	// Given: partitions (2024, 01) and (2024, 02)
	s, _, _ := newTestService(t)
	ctx := context.Background()
	seedTable(t, s, "db", "t")
	resp, aerr := s.batchCreatePartitionTyped(ctx, &batchCreatePartitionReq{DatabaseName: "db", TableName: "t", PartitionInputList: []PartitionInput{
		{Values: []string{"2024", "01"}}, {Values: []string{"2024", "02"}}, {Values: []string{"2024", "01"}},
	}})
	mustOK(t, "BatchCreatePartition", aerr)
	if len(resp.Errors) != 1 || resp.Errors[0].ErrorDetail.ErrorCode != codeAlreadyExists {
		t.Fatalf("BatchCreatePartition Errors = %+v, want the one duplicate", resp.Errors)
	}

	// When: (2024, 01) is moved onto (2024, 02), then onto (2024, 03)
	_, aerr = s.updatePartitionTyped(ctx, &updatePartitionReq{DatabaseName: "db", TableName: "t", PartitionValueList: []string{"2024", "01"},
		PartitionInput: &PartitionInput{Values: []string{"2024", "02"}}})
	wantCode(t, "UpdatePartition onto existing", aerr, codeAlreadyExists)
	_, aerr = s.updatePartitionTyped(ctx, &updatePartitionReq{DatabaseName: "db", TableName: "t", PartitionValueList: []string{"2024", "01"},
		PartitionInput: &PartitionInput{Values: []string{"2024", "03"}, Parameters: map[string]string{"moved": "yes"}}})
	mustOK(t, "UpdatePartition", aerr)

	// Then: (2024, 01) is gone and (2024, 03) carries the new definition
	_, aerr = s.getPartitionTyped(ctx, &getPartitionReq{DatabaseName: "db", TableName: "t", PartitionValues: []string{"2024", "01"}})
	wantCode(t, "GetPartition old", aerr, codeEntityNotFound)
	got, aerr := s.getPartitionTyped(ctx, &getPartitionReq{DatabaseName: "db", TableName: "t", PartitionValues: []string{"2024", "03"}})
	mustOK(t, "GetPartition new", aerr)
	if got.Partition.Parameters["moved"] != "yes" || got.Partition.CreationTime == 0 {
		t.Errorf("moved partition = %+v", got.Partition)
	}
}

func TestGetPartitions_filtersPagesAndSegments(t *testing.T) {
	// Given: twelve monthly partitions for 2024
	s, _, _ := newTestService(t)
	ctx := context.Background()
	seedTable(t, s, "db", "t")
	var inputs []PartitionInput
	for _, m := range []string{"01", "02", "03", "04", "05", "06", "07", "08", "09", "10", "11", "12"} {
		inputs = append(inputs, PartitionInput{Values: []string{"2024", m}, StorageDescriptor: &StorageDescriptor{Columns: []Column{{Name: "id"}}, Location: "s3://b/" + m}})
	}
	_, aerr := s.batchCreatePartitionTyped(ctx, &batchCreatePartitionReq{DatabaseName: "db", TableName: "t", PartitionInputList: inputs})
	mustOK(t, "BatchCreatePartition", aerr)

	// When: a filtered query is paged two at a time
	var months []string
	token := ""
	for {
		page, aerr := s.getPartitionsTyped(ctx, &getPartitionsReq{DatabaseName: "db", TableName: "t",
			Expression: "year = 2024 AND month BETWEEN '03' AND '07'", MaxResults: 2, NextToken: token, ExcludeColumnSchema: ptr(true)})
		mustOK(t, "GetPartitions", aerr)
		for _, p := range page.Partitions {
			months = append(months, p.Values[1])
			if len(p.StorageDescriptor.Columns) != 0 || p.StorageDescriptor.Location == "" {
				t.Errorf("ExcludeColumnSchema: StorageDescriptor = %+v", p.StorageDescriptor)
			}
		}
		if token = page.NextToken; token == "" {
			break
		}
	}

	// Then: exactly March through July, in order
	if got := strings.Join(months, ","); got != "03,04,05,06,07" {
		t.Errorf("months = %s", got)
	}

	// And: three segments partition the whole table between them
	seen := 0
	for n := 0; n < 3; n++ {
		page, aerr := s.getPartitionsTyped(ctx, &getPartitionsReq{DatabaseName: "db", TableName: "t", Segment: &segment{SegmentNumber: n, TotalSegments: 3}})
		mustOK(t, "GetPartitions segment", aerr)
		seen += len(page.Partitions)
	}
	if seen != 12 {
		t.Errorf("segments returned %d partitions in total, want 12", seen)
	}
	// And the stored partition still has its columns
	p, aerr := s.getPartitionTyped(ctx, &getPartitionReq{DatabaseName: "db", TableName: "t", PartitionValues: []string{"2024", "03"}})
	mustOK(t, "GetPartition", aerr)
	if len(p.Partition.StorageDescriptor.Columns) != 1 {
		t.Error("ExcludeColumnSchema modified the stored partition")
	}
}

func TestCatalog_readsWhatTheAPIWrote(t *testing.T) {
	// Given: a partitioned table written through the API
	s, _, _ := newTestService(t)
	ctx := context.Background()
	seedTable(t, s, "db", "events")
	_, aerr := s.createPartitionTyped(ctx, &createPartitionReq{DatabaseName: "db", TableName: "events", PartitionInput: &PartitionInput{Values: []string{"2024", "01"}}})
	mustOK(t, "CreatePartition", aerr)

	// When: another service reads it through Catalog, with other casing
	c := s.Catalog()
	tbl, found, err := c.GetTable(ctx, "DB", "Events")

	// Then: it sees the full definition
	if err != nil || !found {
		t.Fatalf("GetTable found=%v err=%v", found, err)
	}
	if tbl.StorageDescriptor == nil || tbl.StorageDescriptor.Location != "s3://bucket/events/" || len(tbl.PartitionKeys) != 2 {
		t.Errorf("table = %+v", tbl)
	}
	dbs, _ := c.ListDatabases(ctx)
	tables, _ := c.ListTables(ctx, "db")
	parts, _ := c.ListPartitions(ctx, "db", "events")
	if len(dbs) != 1 || len(tables) != 1 || len(parts) != 1 {
		t.Errorf("databases=%d tables=%d partitions=%d", len(dbs), len(tables), len(parts))
	}
	if _, found, _ := c.GetDatabase(ctx, "missing"); found {
		t.Error("GetDatabase found a missing database")
	}
}

func ptr[T any](v T) *T { return &v }
