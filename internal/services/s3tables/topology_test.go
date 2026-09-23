package s3tables

import (
	"context"
	"testing"

	"github.com/overcast-sh/overcast/internal/topology"
)

func TestContributeTopology_bucketsTablesAndStackAlias(t *testing.T) {
	// Given: a table bucket holding one table, in a non-default region
	s, st := newTestService(t)
	ctx := context.Background()
	b := &tableBucket{Name: "lake", Region: "eu-west-1", ARN: s.bucketARN("eu-west-1", "lake")}
	if aerr := s.saveBucket(ctx, b); aerr != nil {
		t.Fatalf("saveBucket: %v", aerr)
	}
	tbl := &tableRecord{Name: "orders", Namespace: "sales", Bucket: "lake", Region: "eu-west-1", TableID: "t-1", ARN: tableARN(b.ARN, "t-1")}
	if aerr := s.saveTable(ctx, tbl); aerr != nil {
		t.Fatalf("saveTable: %v", aerr)
	}
	_ = st.Set(ctx, nsTables, "eu-west-1/lake/sales/corrupt", "{not json")

	// When: S3 Tables contributes to the map, alongside a stack owning the table
	g, cfn := &topology.Graph{}, &topology.Graph{}
	if err := s.ContributeTopology(ctx, g); err != nil {
		t.Fatalf("ContributeTopology: %v", err)
	}
	cfn.SetStack(topology.CFN("eu-west-1", "AWS::S3Tables::Table", tbl.ARN), "lakehouse")
	resp := topology.Build("", g, cfn)

	// Then: both are nodes in the bucket's region, joined table → bucket, and
	// the stack owns the table by its physical ID; the corrupt record is skipped
	if len(resp.Nodes) != 2 || len(resp.Edges) != 1 {
		t.Fatalf("nodes %+v, edges %+v", resp.Nodes, resp.Edges)
	}
	for _, n := range resp.Nodes {
		if n.Service != serviceName || n.Region != "eu-west-1" {
			t.Errorf("unexpected node %+v", n)
		}
		if n.ID == "eu-west-1::s3tables::lake/t-1" && (n.Label != "sales.orders" || n.StackName == nil || *n.StackName != "lakehouse") {
			t.Errorf("table node %+v", n)
		}
	}
	if e := resp.Edges[0]; e.Source != "eu-west-1::s3tables::lake/t-1" || e.Target != "eu-west-1::s3tables::lake" || e.Type != "s3tables-table" {
		t.Errorf("unexpected edge %+v", e)
	}
}
