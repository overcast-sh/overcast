package glue

import (
	"context"
	"testing"

	"github.com/overcast-sh/overcast/internal/events"
	"github.com/overcast-sh/overcast/internal/events/eventstest"
	"github.com/overcast-sh/overcast/internal/protocol"
)

// newBusService is newTestService with the database sales and, unless
// withTable is false, its table orders (partitioned by year and month) and
// the partition 2026/09 created before the event bus is wired, so the events
// a test sees are its own mutation's alone.
func newBusService(t *testing.T, withTable bool) (*Service, *events.Bus) {
	t.Helper()
	s, _, _ := newTestService(t)
	ctx := context.Background()
	if withTable {
		seedTable(t, s, "sales", "orders")
		_, aerr := s.createPartitionTyped(ctx, &createPartitionReq{DatabaseName: "sales", TableName: "orders",
			PartitionInput: &PartitionInput{Values: []string{"2026", "09"}}})
		mustOK(t, "CreatePartition", aerr)
	} else {
		_, aerr := s.createDatabaseTyped(ctx, &createDatabaseReq{DatabaseInput: &DatabaseInput{Name: "sales"}})
		mustOK(t, "CreateDatabase", aerr)
	}
	bus := events.NewBus()
	t.Cleanup(bus.Stop)
	s.InitBus(bus)
	return s, bus
}

// wantOneGlueEvent asserts that bus published exactly one event of type typ,
// about sales.orders, with the given change.
func wantOneGlueEvent(t *testing.T, bus *events.Bus, typ events.Type, change string) {
	t.Helper()
	got := eventstest.Published(bus, typ)
	if len(got) != 1 {
		t.Fatalf("%s published %d times, want once: %+v", typ, len(got), got)
	}
	want := events.GlueTablePayload{
		Database: "sales", Table: "orders", Change: change,
		ARN: "arn:aws:glue:us-east-1:123456789012:table/sales/orders",
	}
	if p, ok := got[0].Payload.(events.GlueTablePayload); !ok || p != want {
		t.Fatalf("payload = %#v, want %#v", got[0].Payload, want)
	}
	if got[0].Source != serviceName || got[0].ResourceARN != want.ARN {
		t.Fatalf("source, resource ARN = %q, %q; want %q, %q", got[0].Source, got[0].ResourceARN, serviceName, want.ARN)
	}
}

func wantNoEvents(t *testing.T, bus *events.Bus) {
	t.Helper()
	for _, typ := range []events.Type{events.GlueTableChanged, events.GluePartitionsChanged} {
		if got := eventstest.Published(bus, typ); len(got) != 0 {
			t.Fatalf("%s published %d times, want none: %+v", typ, len(got), got)
		}
	}
}

var ordersInput = &TableInput{Name: "orders", StorageDescriptor: &StorageDescriptor{Location: "s3://bucket/orders/"}}

func TestTableMutations_publishTableChangedOnce(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name      string
		withTable bool
		change    string
		mutate    func(s *Service) *protocol.AWSError
	}{
		{"CreateTable", false, events.GlueTableCreated, func(s *Service) *protocol.AWSError {
			_, aerr := s.createTableTyped(ctx, &createTableReq{DatabaseName: "sales", TableInput: ordersInput})
			return aerr
		}},
		{"CreateTable through the catalog writer", false, events.GlueTableCreated, func(s *Service) *protocol.AWSError {
			return s.CatalogWriter().CreateTable(ctx, "sales", *ordersInput)
		}},
		{"UpdateTable", true, events.GlueTableUpdated, func(s *Service) *protocol.AWSError {
			_, aerr := s.updateTableTyped(ctx, &updateTableReq{DatabaseName: "sales", TableInput: ordersInput})
			return aerr
		}},
		{"DeleteTable", true, events.GlueTableDeleted, func(s *Service) *protocol.AWSError {
			_, aerr := s.deleteTableTyped(ctx, &deleteTableReq{DatabaseName: "sales", Name: "orders"})
			return aerr
		}},
		{"BatchDeleteTable", true, events.GlueTableDeleted, func(s *Service) *protocol.AWSError {
			_, aerr := s.batchDeleteTableTyped(ctx, &batchDeleteTableReq{DatabaseName: "sales", TablesToDelete: []string{"orders", "missing"}})
			return aerr
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, bus := newBusService(t, tt.withTable)
			mustOK(t, tt.name, tt.mutate(s))
			wantOneGlueEvent(t, bus, events.GlueTableChanged, tt.change)
		})
	}
}

func TestPartitionMutations_publishPartitionsChangedOnce(t *testing.T) {
	ctx := context.Background()
	values := func(v ...string) []string { return v }
	tests := []struct {
		name   string
		mutate func(s *Service) *protocol.AWSError
	}{
		{"CreatePartition", func(s *Service) *protocol.AWSError {
			_, aerr := s.createPartitionTyped(ctx, &createPartitionReq{DatabaseName: "sales", TableName: "orders",
				PartitionInput: &PartitionInput{Values: values("2026", "10")}})
			return aerr
		}},
		{"BatchCreatePartition", func(s *Service) *protocol.AWSError {
			_, aerr := s.batchCreatePartitionTyped(ctx, &batchCreatePartitionReq{DatabaseName: "sales", TableName: "orders",
				PartitionInputList: []PartitionInput{{Values: values("2026", "10")}, {Values: values("2026", "11")}, {Values: values("2026", "09")}}})
			return aerr
		}},
		{"UpdatePartition", func(s *Service) *protocol.AWSError {
			_, aerr := s.updatePartitionTyped(ctx, &updatePartitionReq{DatabaseName: "sales", TableName: "orders",
				PartitionValueList: values("2026", "09"), PartitionInput: &PartitionInput{Values: values("2026", "10")}})
			return aerr
		}},
		{"DeletePartition", func(s *Service) *protocol.AWSError {
			_, aerr := s.deletePartitionTyped(ctx, &deletePartitionReq{DatabaseName: "sales", TableName: "orders",
				PartitionValues: values("2026", "09")})
			return aerr
		}},
		{"BatchDeletePartition", func(s *Service) *protocol.AWSError {
			_, aerr := s.batchDeletePartitionTyped(ctx, &batchDeletePartitionReq{DatabaseName: "sales", TableName: "orders",
				PartitionsToDelete: []PartitionValueList{{Values: values("2026", "09")}, {Values: values("2026", "12")}}})
			return aerr
		}},
		{"CreatePartitions through the catalog writer", func(s *Service) *protocol.AWSError {
			_, aerr := s.CatalogWriter().CreatePartitions(ctx, "sales", "orders", []PartitionInput{{Values: values("2026", "10")}})
			return aerr
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, bus := newBusService(t, true)
			mustOK(t, tt.name, tt.mutate(s))
			wantOneGlueEvent(t, bus, events.GluePartitionsChanged, "")
		})
	}
}

// A request that changes nothing — refused outright, or a batch whose every
// item failed — publishes nothing.
func TestMutationsThatChangeNothing_publishNothing(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name   string
		mutate func(s *Service)
	}{
		{"CreateTable of an existing table", func(s *Service) {
			_, _ = s.createTableTyped(ctx, &createTableReq{DatabaseName: "sales", TableInput: ordersInput})
		}},
		{"DeleteTable of a missing table", func(s *Service) {
			_, _ = s.deleteTableTyped(ctx, &deleteTableReq{DatabaseName: "sales", Name: "missing"})
		}},
		{"BatchCreatePartition of existing partitions", func(s *Service) {
			_, _ = s.batchCreatePartitionTyped(ctx, &batchCreatePartitionReq{DatabaseName: "sales", TableName: "orders",
				PartitionInputList: []PartitionInput{{Values: []string{"2026", "09"}}}})
		}},
		{"BatchDeletePartition of missing partitions", func(s *Service) {
			_, _ = s.batchDeletePartitionTyped(ctx, &batchDeletePartitionReq{DatabaseName: "sales", TableName: "orders",
				PartitionsToDelete: []PartitionValueList{{Values: []string{"1999", "01"}}}})
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, bus := newBusService(t, true)
			tt.mutate(s)
			wantNoEvents(t, bus)
		})
	}
}

func TestDeleteDatabase_publishesADeleteForEachTable(t *testing.T) {
	s, bus := newBusService(t, true)
	_, aerr := s.deleteDatabaseTyped(context.Background(), &deleteDatabaseReq{Name: "sales"})
	mustOK(t, "DeleteDatabase", aerr)
	wantOneGlueEvent(t, bus, events.GlueTableChanged, events.GlueTableDeleted)
}
