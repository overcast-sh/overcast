package s3tables

import (
	"context"
	"strings"
	"testing"

	"github.com/overcast-sh/overcast/internal/events"
	"github.com/overcast-sh/overcast/internal/events/eventstest"
	"github.com/overcast-sh/overcast/internal/icebergmeta"
	"github.com/overcast-sh/overcast/internal/protocol"
)

// newS3TablesService is a service with an in-memory S3 and the bucket "seed"
// with its namespace "ns"; it returns the bucket's ARN.
func newS3TablesService(t *testing.T) (*Service, *memoryS3, string) {
	t.Helper()
	s, _ := newTestService(t)
	s3 := withMemoryS3(s)
	return s, s3, seed(t, s)
}

// wireBus gives s a new event bus, so the events a test then sees are its own
// mutation's and not those of its setup.
func wireBus(t *testing.T, s *Service) *events.Bus {
	t.Helper()
	bus := events.NewBus()
	t.Cleanup(bus.Stop)
	s.InitBus(bus)
	return bus
}

// busEvents is every data-lake event type S3 Tables publishes, for asserting
// that nothing else was published.
var busEvents = []events.Type{events.S3TablesTableCreated, events.S3TablesTableDeleted, events.S3TablesTableCommitted}

// wantOnly asserts bus published exactly one event of each of the given
// types, and none of the others, and returns them by type.
func wantOnly(t *testing.T, bus *events.Bus, types ...events.Type) map[events.Type]events.Event {
	t.Helper()
	got := map[events.Type]events.Event{}
	for _, typ := range busEvents {
		published := eventstest.Published(bus, typ)
		want := 0
		for _, w := range types {
			if w == typ {
				want = 1
			}
		}
		if len(published) != want {
			t.Fatalf("%s published %d times, want %d: %+v", typ, len(published), want, published)
		}
		if want == 1 {
			got[typ] = published[0]
		}
	}
	return got
}

// wantTable asserts that e is about the table arn, called name, in namespace
// ns of bucket seed.
func wantTable(t *testing.T, e events.Event, arn, name string) {
	t.Helper()
	var p events.S3TablesTablePayload
	switch payload := e.Payload.(type) {
	case events.S3TablesTablePayload:
		p = payload
	case events.S3TablesCommitPayload:
		p = payload.S3TablesTablePayload
	default:
		t.Fatalf("%s payload is %T", e.Type, e.Payload)
	}
	want := events.S3TablesTablePayload{ARN: arn, Bucket: "seed", Namespace: "ns", Name: name}
	if p != want || e.ResourceARN != arn || e.Source != serviceName {
		t.Fatalf("%s = %+v (resource %q, source %q), want payload %+v", e.Type, p, e.ResourceARN, e.Source, want)
	}
}

func TestCreateTable_publishesTableCreatedOnce(t *testing.T) {
	s, _, bucketARN := newS3TablesService(t)
	bus := wireBus(t, s)
	out, aerr := s.createTableTyped(context.Background(), &createTableRequest{TableBucketARN: bucketARN, Namespace: "ns", Name: "t", Format: formatIceberg})
	if aerr != nil {
		t.Fatalf("createTable: %v", aerr)
	}
	wantTable(t, wantOnly(t, bus, events.S3TablesTableCreated)[events.S3TablesTableCreated], out.TableARN, "t")
}

func TestIcebergCreateTable_publishesTableCreatedOnce(t *testing.T) {
	s, _, bucketARN := newS3TablesService(t)
	bus := wireBus(t, s)
	icebergTable(t, s, bucketARN, "t")
	created := wantOnly(t, bus, events.S3TablesTableCreated)[events.S3TablesTableCreated]
	if !strings.HasPrefix(created.ResourceARN, bucketARN+"/table/") {
		t.Fatalf("created %s, want a table of %s", created.ResourceARN, bucketARN)
	}
	wantTable(t, created, created.ResourceARN, "t")
}

func TestDeletes_publishTableDeletedOnce(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name   string
		delete func(s *Service, bucketARN string) *protocol.AWSError
	}{
		{"DeleteTable", func(s *Service, bucketARN string) *protocol.AWSError {
			_, aerr := s.deleteTableTyped(ctx, &tableRequest{TableBucketARN: bucketARN, Namespace: "ns", Name: "t"})
			return aerr
		}},
		{"Iceberg REST drop", func(s *Service, bucketARN string) *protocol.AWSError {
			return s.icebergDropTableTyped(ctx, &icebergDropTableRequest{
				icebergPath: icebergPath{BucketARN: bucketARN, Namespace: "ns", Table: "t"}, PurgeRequested: true,
			})
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, _, bucketARN := newS3TablesService(t)
			out, aerr := s.createTableTyped(ctx, &createTableRequest{TableBucketARN: bucketARN, Namespace: "ns", Name: "t", Format: formatIceberg})
			if aerr != nil {
				t.Fatalf("createTable: %v", aerr)
			}
			bus := wireBus(t, s)

			if aerr := tt.delete(s, bucketARN); aerr != nil {
				t.Fatalf("delete: %v", aerr)
			}
			wantTable(t, wantOnly(t, bus, events.S3TablesTableDeleted)[events.S3TablesTableDeleted], out.TableARN, "t")
		})
	}
}

// bigSnapshotID is above 2^53, where a JavaScript number stops being exact.
const bigSnapshotID int64 = 9007199254740993

func TestIcebergCommit_publishesTableCommittedOnceWithTheSnapshot(t *testing.T) {
	// Given: an Iceberg table
	s, _, bucketARN := newS3TablesService(t)
	created := icebergTable(t, s, bucketARN, "t")
	bus := wireBus(t, s)

	// When: a commit appends a snapshot as main's head
	out, aerr := s.icebergCommitTableTyped(context.Background(), commitRequest(bucketARN, "t",
		icebergmeta.Update{Action: "add-snapshot", Snapshot: &icebergmeta.Snapshot{
			SnapshotID: bigSnapshotID, SequenceNumber: 1, TimestampMS: 1, ManifestList: "s3://m.avro",
			Summary: map[string]string{"operation": "append"},
		}},
		icebergmeta.Update{Action: "set-snapshot-ref", RefName: icebergmeta.MainBranch,
			SnapshotRef: icebergmeta.SnapshotRef{SnapshotID: bigSnapshotID, Type: icebergmeta.RefBranch}}))
	if aerr != nil {
		t.Fatalf("commit: %v", aerr)
	}

	// Then: one commit event says where the pointer moved and what the new
	// current snapshot did, its id exact as a string
	e := wantOnly(t, bus, events.S3TablesTableCommitted)[events.S3TablesTableCommitted]
	wantTable(t, e, e.ResourceARN, "t")
	p := e.Payload.(events.S3TablesCommitPayload)
	want := events.S3TablesCommitPayload{
		S3TablesTablePayload:     p.S3TablesTablePayload,
		PreviousMetadataLocation: created.MetadataLocation,
		MetadataLocation:         out.MetadataLocation,
		SnapshotID:               "9007199254740993",
		Operation:                "append",
	}
	if p != want {
		t.Fatalf("payload = %+v, want %+v", p, want)
	}
}

func TestUpdateTableMetadataLocation_publishesTableCommittedWithTheSnapshotReadBack(t *testing.T) {
	// Given: an Iceberg table, and a next metadata file the client wrote
	// itself, with a snapshot in it
	ctx := context.Background()
	s, s3, bucketARN := newS3TablesService(t)
	created := icebergTable(t, s, bucketARN, "t")
	next := *created.Metadata
	next.Snapshots = []icebergmeta.Snapshot{{SnapshotID: 7, TimestampMS: 1, Summary: map[string]string{"operation": "overwrite"}}}
	next.CurrentSnapshotID = 7
	raw, aerr := marshalMetadata(&next)
	if aerr != nil {
		t.Fatal(aerr)
	}
	nextLocation := strings.TrimSuffix(next.Location, "/") + "/metadata/00001-client.metadata.json"
	s3.objects[strings.TrimPrefix(nextLocation, s3Scheme)] = raw
	current, _ := s.getTableMetadataLocationTyped(ctx, &tableRequest{TableBucketARN: bucketARN, Namespace: "ns", Name: "t"})
	bus := wireBus(t, s)

	// When: the client points the table at it
	_, aerr = s.updateTableMetadataLocationTyped(ctx, &updateMetadataLocationRequest{
		TableBucketARN: bucketARN, Namespace: "ns", Name: "t", VersionToken: current.VersionToken, MetadataLocation: nextLocation,
	})
	if aerr != nil {
		t.Fatalf("updateTableMetadataLocation: %v", aerr)
	}

	// Then: one commit event, with the snapshot read back from the file
	p := wantOnly(t, bus, events.S3TablesTableCommitted)[events.S3TablesTableCommitted].Payload.(events.S3TablesCommitPayload)
	if p.PreviousMetadataLocation != created.MetadataLocation || p.MetadataLocation != nextLocation || p.SnapshotID != "7" || p.Operation != "overwrite" {
		t.Fatalf("payload = %+v", p)
	}
}

func TestCommitsThatChangeNothing_publishNothing(t *testing.T) {
	// Given: an Iceberg table
	ctx := context.Background()
	s, _, bucketARN := newS3TablesService(t)
	icebergTable(t, s, bucketARN, "t")
	bus := wireBus(t, s)

	// When: a commit changes nothing, and another is refused on a stale token
	_, aerr := s.icebergCommitTableTyped(ctx, commitRequest(bucketARN, "t",
		icebergmeta.Update{Action: "remove-properties", Removals: []string{"absent"}}))
	if aerr != nil {
		t.Fatalf("no-op commit: %v", aerr)
	}
	_, aerr = s.updateTableMetadataLocationTyped(ctx, &updateMetadataLocationRequest{
		TableBucketARN: bucketARN, Namespace: "ns", Name: "t", VersionToken: "stale", MetadataLocation: "s3://x/metadata/1.json",
	})
	if aerr == nil {
		t.Fatal("stale-token commit succeeded")
	}

	// Then: nothing is published
	wantOnly(t, bus)
}

func TestPublishSwap_thatCreatedTheTablePublishesItsCreationFirst(t *testing.T) {
	// Given: a swap that created a table with no snapshot yet
	s, _ := newTestService(t)
	bus := wireBus(t, s)
	arn := "arn:aws:s3tables:us-east-1:111122223333:bucket/seed/table/abc"
	swap := &metadataSwap{
		table:    tableRecord{ARN: arn, Bucket: "seed", Namespace: "ns", Name: "t", MetadataLocation: "s3://w--table-s3/metadata/00000-a.metadata.json"},
		created:  true,
		metadata: &icebergmeta.Metadata{CurrentSnapshotID: -1},
	}

	// When: it is published
	s.publishSwap(context.Background(), swap)

	// Then: the creation, then the commit, which names no snapshot
	got := wantOnly(t, bus, events.S3TablesTableCreated, events.S3TablesTableCommitted)
	created, committed := got[events.S3TablesTableCreated], got[events.S3TablesTableCommitted]
	wantTable(t, created, arn, "t")
	if created.Seq > committed.Seq {
		t.Fatal("the commit was published before the table's creation")
	}
	if p := committed.Payload.(events.S3TablesCommitPayload); p.SnapshotID != "" || p.Operation != "" || p.PreviousMetadataLocation != "" {
		t.Fatalf("committed %+v, want no snapshot and no previous file", p)
	}
}

func TestRenameTable_publishesTableRenamedOnceWithTheNewName(t *testing.T) {
	ctx := context.Background()
	s, _, bucketARN := newS3TablesService(t)
	out, aerr := s.createTableTyped(ctx, &createTableRequest{TableBucketARN: bucketARN, Namespace: "ns", Name: "t", Format: formatIceberg})
	if aerr != nil {
		t.Fatalf("createTable: %v", aerr)
	}
	bus := wireBus(t, s)

	if _, aerr := s.renameTableTyped(ctx, &renameTableRequest{TableBucketARN: bucketARN, Namespace: "ns", Name: "t", NewName: "orders"}); aerr != nil {
		t.Fatalf("renameTable: %v", aerr)
	}

	got := eventstest.Published(bus, events.S3TablesTableRenamed)
	if len(got) != 1 {
		t.Fatalf("TableRenamed published %d times, want once", len(got))
	}
	wantTable(t, got[0], out.TableARN, "orders")
	wantOnly(t, bus)
}
