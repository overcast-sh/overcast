package glue

// store_test.go — key design, legacy records, corrupt records and cascades
// under concurrency.

import (
	"context"
	"sync"
	"testing"

	"go.uber.org/zap"

	"github.com/overcast-sh/overcast/internal/clock"
	"github.com/overcast-sh/overcast/internal/config"
	"github.com/overcast-sh/overcast/internal/state"
)

func TestNamesWithSlashes_doNotCollide(t *testing.T) {
	// Given: table "b/t" in database "a", and table "t" in database "a/b" —
	// one key, "a/b/t", were the components not escaped — plus a table "t/x"
	// beside "t" in "a/b"
	s, _, _ := newTestService(t)
	ctx := context.Background()
	seedTable(t, s, "a", "b/t")
	seedTable(t, s, "a/b", "t")
	seedTable(t, s, "a/b", "t/x")
	for _, table := range []string{"t", "t/x"} {
		_, aerr := s.createPartitionTyped(ctx, &createPartitionReq{DatabaseName: "a/b", TableName: table,
			PartitionInput: &PartitionInput{Values: []string{"2024", table}}})
		mustOK(t, "CreatePartition", aerr)
	}

	// When/Then: each table list and partition list is its own
	for db, want := range map[string]int{"a": 1, "a/b": 2} {
		tables, aerr := s.getTablesTyped(ctx, &getTablesReq{DatabaseName: db})
		mustOK(t, "GetTables", aerr)
		if len(tables.TableList) != want {
			t.Errorf("GetTables(%q) = %d tables, want %d", db, len(tables.TableList), want)
		}
	}
	parts, aerr := s.getPartitionsTyped(ctx, &getPartitionsReq{DatabaseName: "a/b", TableName: "t"})
	mustOK(t, "GetPartitions", aerr)
	if len(parts.Partitions) != 1 || parts.Partitions[0].Values[1] != "t" {
		t.Errorf("GetPartitions(a/b.t) = %+v, want only its own partition", parts.Partitions)
	}

	// When: database "a" is deleted
	_, aerr = s.deleteDatabaseTyped(ctx, &deleteDatabaseReq{Name: "a"})
	mustOK(t, "DeleteDatabase a", aerr)

	// Then: database "a/b" and everything in it survive
	for _, table := range []string{"t", "t/x"} {
		if _, aerr := s.getTableTyped(ctx, &getTableReq{DatabaseName: "a/b", Name: table}); aerr != nil {
			t.Errorf("a/b.%s was deleted with database a: %s", table, aerr.Message)
		}
	}
	parts, aerr = s.getPartitionsTyped(ctx, &getPartitionsReq{DatabaseName: "a/b", TableName: "t/x"})
	mustOK(t, "GetPartitions", aerr)
	if len(parts.Partitions) != 1 {
		t.Errorf("a/b.t/x has %d partitions after deleting database a, want 1", len(parts.Partitions))
	}
}

// seedOrphanable gives db.t a partition and an archived version, then
// corrupts the table record itself.
func seedOrphanable(t *testing.T, s *Service, st state.Store) {
	t.Helper()
	ctx := context.Background()
	seedTable(t, s, "db", "t")
	_, aerr := s.createPartitionTyped(ctx, &createPartitionReq{DatabaseName: "db", TableName: "t", PartitionInput: &PartitionInput{Values: []string{"2024", "01"}}})
	mustOK(t, "CreatePartition", aerr)
	_, aerr = s.updateTableTyped(ctx, &updateTableReq{DatabaseName: "db", TableInput: &TableInput{Name: "t",
		PartitionKeys: []Column{{Name: "year", Type: "int"}, {Name: "month"}}}})
	mustOK(t, "UpdateTable", aerr)
	if err := st.Set(ctx, nsTables, tableKey("db", "t"), "{corrupt"); err != nil {
		t.Fatal(err)
	}
}

func TestCorruptTableRecord_isDeletableAndTakesItsChildren(t *testing.T) {
	// Given: a partitioned, versioned table whose record is corrupt
	s, st, _ := newTestService(t)
	ctx := context.Background()
	seedOrphanable(t, s, st)

	// When: it is deleted
	_, aerr := s.deleteTableTyped(ctx, &deleteTableReq{DatabaseName: "db", Name: "t"})

	// Then: the delete succeeds and removes its partitions and versions
	mustOK(t, "DeleteTable of a corrupt record", aerr)
	for _, ns := range []string{nsTables, nsPartitions, nsTableVersions} {
		if pairs, _ := st.Scan(ctx, ns, tableKey("db", "t")); len(pairs) != 0 {
			t.Errorf("%s kept %d records", ns, len(pairs))
		}
	}
}

func TestCreateTable_doesNotInheritLeftoverChildren(t *testing.T) {
	// Given: partitions and a version left under a table whose record is corrupt
	s, st, _ := newTestService(t)
	ctx := context.Background()
	seedOrphanable(t, s, st)

	// When: a table of that name is created
	seedTable(t, s, "db", "t")

	// Then: it starts with no partitions and a single version
	parts, aerr := s.getPartitionsTyped(ctx, &getPartitionsReq{DatabaseName: "db", TableName: "t"})
	mustOK(t, "GetPartitions", aerr)
	versions, aerr := s.getTableVersionsTyped(ctx, &getTableVersionsReq{DatabaseName: "db", TableName: "t"})
	mustOK(t, "GetTableVersions", aerr)
	if len(parts.Partitions) != 0 || len(versions.TableVersions) != 1 {
		t.Errorf("new table inherited %d partitions and %d versions", len(parts.Partitions), len(versions.TableVersions))
	}
}

func TestLegacyKeys_areMigratedToFoldedNames(t *testing.T) {
	// Given: a database and a table stored by the pre-#2064 service, which
	// kept names in the case they were written in
	st := state.NewMemoryStore()
	ctx := context.Background()
	if err := st.Set(ctx, nsDatabases, "MyDb", `{"Name":"MyDb","CatalogId":"123456789012","overcastTags":{"k":"v"}}`); err != nil {
		t.Fatal(err)
	}
	if err := st.Set(ctx, nsTables, "MyDb/MyTable", `{"Name":"MyTable","DatabaseName":"MyDb","TableType":"EXTERNAL_TABLE"}`); err != nil {
		t.Fatal(err)
	}
	s := New(&config.Config{Region: "us-east-1", AccountID: "123456789012"}, st, zap.NewNop(), clock.NewMock())

	// When: they are read under any casing, as the API now folds names
	db, aerr := s.getDatabaseTyped(ctx, &getDatabaseReq{Name: "MYDB"})
	mustOK(t, "GetDatabase", aerr)
	tbl, aerr := s.getTableTyped(ctx, &getTableReq{DatabaseName: "mydb", Name: "MyTable"})
	mustOK(t, "GetTable", aerr)

	// Then: both are found under folded names, tags survive, and the legacy
	// keys are gone
	if db.Database.Name != "mydb" || tbl.Table.Name != "mytable" || tbl.Table.DatabaseName != "mydb" {
		t.Errorf("database %q, table %q.%q", db.Database.Name, tbl.Table.DatabaseName, tbl.Table.Name)
	}
	tags, aerr := s.listTagsForResourceTyped(ctx, &glueListTagsForResourceReq{ResourceArn: "arn:aws:glue:us-east-1:123456789012:database/MyDb"})
	mustOK(t, "GetTags", aerr)
	if tags.Tags["k"] != "v" {
		t.Errorf("tags = %v", tags.Tags)
	}
	for ns, key := range map[string]string{nsDatabases: "MyDb", nsTables: "MyDb/MyTable"} {
		if _, found, _ := st.Get(ctx, ns, key); found {
			t.Errorf("legacy key %s %q still present", ns, key)
		}
	}
}

func TestCascadingDeletes_leaveNoOrphansUnderConcurrentWrites(t *testing.T) {
	// Given: many rounds of a table or its database being deleted while a
	// partition and a sibling table are created under them
	s, st, _ := newTestService(t)
	ctx := context.Background()
	for round := 0; round < 50; round++ {
		seedTable(t, s, "db", "t")
		var wg sync.WaitGroup
		wg.Add(3)
		go func() {
			defer wg.Done()
			_, _ = s.createPartitionTyped(ctx, &createPartitionReq{DatabaseName: "db", TableName: "t",
				PartitionInput: &PartitionInput{Values: []string{"2024", "01"}}})
		}()
		go func() {
			defer wg.Done()
			_, _ = s.createTableTyped(ctx, &createTableReq{DatabaseName: "db", TableInput: &TableInput{Name: "other"}})
		}()
		go func(round int) {
			defer wg.Done()
			if round%2 == 0 {
				_, _ = s.deleteTableTyped(ctx, &deleteTableReq{DatabaseName: "db", Name: "t"})
			} else {
				_, _ = s.deleteDatabaseTyped(ctx, &deleteDatabaseReq{Name: "db"})
			}
		}(round)
		wg.Wait()

		// Then: no partition outlives its table, no table its database
		if _, found, _ := s.store.getTable(ctx, "db", "t"); !found {
			if pairs, _ := st.Scan(ctx, nsPartitions, tablePrefix("db", "t")); len(pairs) != 0 {
				t.Fatalf("round %d: %d partitions outlived their table", round, len(pairs))
			}
		}
		if _, found, _ := s.store.getDatabase(ctx, "db"); !found {
			if pairs, _ := st.Scan(ctx, nsTables, databasePrefix("db")); len(pairs) != 0 {
				t.Fatalf("round %d: %d tables outlived their database", round, len(pairs))
			}
		}
		_, _ = s.deleteDatabaseTyped(ctx, &deleteDatabaseReq{Name: "db"})
	}
}
