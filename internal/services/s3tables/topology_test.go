package s3tables

import (
	"context"
	"testing"

	"github.com/overcast-sh/overcast/internal/icebergmeta"
	"github.com/overcast-sh/overcast/internal/serviceutil"
	"github.com/overcast-sh/overcast/internal/topology"
)

func TestContributeTopology_bucketNodeListsTablesAndFoldsInWarehouses(t *testing.T) {
	// Given: a table bucket with two Iceberg tables, one of them committed to
	// with a snapshot that appended records, and a corrupt table record
	ctx := context.Background()
	s, _, bucketARN := newS3TablesService(t)
	events := icebergTable(t, s, bucketARN, "events")
	icebergTable(t, s, bucketARN, "accounts")
	if _, aerr := s.icebergCommitTableTyped(ctx, commitRequest(bucketARN, "events",
		icebergmeta.Update{Action: "add-snapshot", Snapshot: &icebergmeta.Snapshot{
			SnapshotID: 11, SequenceNumber: 1, TimestampMS: 1_700_000_000_000, ManifestList: "s3://m.avro",
			Summary: map[string]string{"operation": "append", "added-records": "250"},
		}},
		icebergmeta.Update{Action: "set-snapshot-ref", RefName: icebergmeta.MainBranch,
			SnapshotRef: icebergmeta.SnapshotRef{SnapshotID: 11, Type: icebergmeta.RefBranch}})); aerr != nil {
		t.Fatalf("commit: %v", aerr)
	}
	_ = s.store.Set(ctx, nsTables, "us-east-1/seed/ns/corrupt", "{not json")

	// When: S3 Tables contributes to the map, beside S3 (which has the
	// events table's warehouse bucket) and a stack that owns the table bucket
	g, s3, cfn := &topology.Graph{}, &topology.Graph{}, &topology.Graph{}
	if err := s.ContributeTopology(ctx, g); err != nil {
		t.Fatalf("ContributeTopology: %v", err)
	}
	warehouse, _, _ := serviceutil.SplitS3URI(events.Metadata.Location)
	s3.AddNode(topology.Node{ID: topology.NodeID("us-east-1", "s3", warehouse), Service: "s3", Label: warehouse, Region: "us-east-1"})
	cfn.SetStack(topology.CFN("us-east-1", "AWS::S3Tables::TableBucket", bucketARN), "lakehouse")
	resp := topology.Build("", g, s3, cfn)

	// Then: the table bucket is the only node — the warehouse bucket is
	// folded into it — and it belongs to the stack
	if len(resp.Nodes) != 1 {
		t.Fatalf("nodes = %+v", resp.Nodes)
	}
	bucket := resp.Nodes[0]
	if bucket.ID != "us-east-1::s3tables::seed" || bucket.StackName == nil || *bucket.StackName != "lakehouse" {
		t.Fatalf("bucket node = %+v", bucket)
	}
	// And: it lists both tables by namespace and name, the corrupt one skipped
	if len(bucket.Tables) != 2 || bucket.Tables[0].Name != "accounts" || bucket.Tables[1].Name != "events" {
		t.Fatalf("tables = %+v", bucket.Tables)
	}
	// And: the committed table has its snapshot count and latest commit
	row := bucket.Tables[1]
	if row.Namespace != "ns" || row.ID == "" || row.Location != events.Metadata.Location || row.Snapshots == nil || *row.Snapshots != 1 {
		t.Errorf("events row = %+v", row)
	}
	c := row.LastCommit
	if c == nil || c.SnapshotID != "11" || c.Operation != "append" || c.AddedRecords == nil || *c.AddedRecords != 250 ||
		c.DeletedRecords != nil || c.CommittedAt != 1_700_000_000_000 {
		t.Errorf("last commit = %+v", c)
	}
	// And: the table that has not been committed to has no snapshot yet
	if acc := bucket.Tables[0]; acc.Snapshots == nil || *acc.Snapshots != 0 || acc.LastCommit != nil {
		t.Errorf("accounts row = %+v", acc)
	}
}

func TestSnapshotCache_readsEachMetadataFileOnce(t *testing.T) {
	// Given: a table whose metadata has been summarised once
	ctx := context.Background()
	s, mem, bucketARN := newS3TablesService(t)
	table := icebergTable(t, s, bucketARN, "t")
	if _, ok := s.snapshotSummary(ctx, table.MetadataLocation); !ok {
		t.Fatal("first read failed")
	}

	// When: the file disappears and the map asks again
	clear(mem.objects)
	_, ok := s.snapshotSummary(ctx, table.MetadataLocation)

	// Then: the summary is remembered, since a location's file does not change
	if !ok {
		t.Error("summary was not remembered")
	}
}
