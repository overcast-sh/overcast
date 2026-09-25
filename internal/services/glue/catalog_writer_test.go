package glue

import (
	"context"
	"strconv"
	"testing"
)

func TestCatalogWriter_runsGluesOwnOperations(t *testing.T) {
	// Given: the in-process writer
	ctx := context.Background()
	s, _, _ := newTestService(t)
	w := s.CatalogWriter()

	// When: a database and a partitioned table are created through it
	mustOK(t, "CreateDatabase", w.CreateDatabase(ctx, DatabaseInput{Name: "Sales"}))
	mustOK(t, "CreateTable", w.CreateTable(ctx, "sales", TableInput{Name: "Orders", PartitionKeys: []Column{{Name: "n", Type: "int"}}}))

	// Then: they fail as the operations do
	wantCode(t, "CreateDatabase twice", w.CreateDatabase(ctx, DatabaseInput{Name: "sales"}), codeAlreadyExists)
	wantCode(t, "CreateTable in a missing database", w.CreateTable(ctx, "nope", TableInput{Name: "t"}), codeEntityNotFound)

	// When: more partitions are created than one batch holds, one twice
	inputs := make([]PartitionInput, maxBatchCreatePartitions+5)
	for i := range inputs {
		inputs[i] = PartitionInput{Values: []string{strconv.Itoa(i)}}
	}
	inputs = append(inputs, PartitionInput{Values: []string{"0"}})
	errs, aerr := w.CreatePartitions(ctx, "sales", "orders", inputs)
	mustOK(t, "CreatePartitions", aerr)

	// Then: every one was created across the batches, and the duplicate is
	// reported rather than failing the lot
	if len(errs) != 1 || errs[0].ErrorDetail.ErrorCode != codeAlreadyExists {
		t.Fatalf("errors = %+v", errs)
	}
	parts, _ := s.Catalog().ListPartitions(ctx, "sales", "orders")
	if len(parts) != maxBatchCreatePartitions+5 {
		t.Fatalf("created %d partitions", len(parts))
	}

	// When: they are deleted, more than a batch at once, one of them missing
	values := make([][]string, 0, len(parts)+1)
	for _, p := range parts {
		values = append(values, p.Values)
	}
	errs, aerr = w.DeletePartitions(ctx, "sales", "orders", append(values, []string{"missing"}))
	mustOK(t, "DeletePartitions", aerr)
	if len(errs) != 1 || errs[0].ErrorDetail.ErrorCode != codeEntityNotFound {
		t.Fatalf("errors = %+v", errs)
	}

	// And: the table and database go
	mustOK(t, "DeleteTable", w.DeleteTable(ctx, "sales", "orders"))
	mustOK(t, "DeleteDatabase", w.DeleteDatabase(ctx, "sales"))
	if _, found, _ := s.Catalog().GetDatabase(ctx, "sales"); found {
		t.Fatal("database survived")
	}
}
