package glue

import (
	"context"
	"slices"
	"testing"

	"github.com/overcast-sh/overcast/internal/topology"
)

func contributeGlue(t *testing.T, s *Service) topology.Response {
	t.Helper()
	g := &topology.Graph{}
	if err := s.ContributeTopology(context.Background(), g); err != nil {
		t.Fatalf("ContributeTopology: %v", err)
	}
	// The endpoints of Glue's edges are other services' nodes.
	others := &topology.Graph{}
	for _, n := range []topology.Node{
		{ID: topology.NodeID("us-east-1", "s3", "bucket"), Service: "s3", Label: "bucket", Region: "us-east-1"},
		{ID: topology.NodeID("us-east-1", "s3", "lakehouse"), Service: "s3", Label: "lakehouse", Region: "us-east-1"},
		{ID: topology.NodeID("us-east-1", "s3tables", "lake"), Service: "s3tables", Label: "lake", Region: "us-east-1"},
	} {
		others.AddNode(n)
	}
	return topology.Build("", g, others)
}

func TestContributeTopology_databasesTablesAndEdges(t *testing.T) {
	// Given: a database with a partitioned Parquet table holding two
	// partitions, and an Iceberg table whose metadata is in another bucket
	ctx := context.Background()
	s, _, _ := newTestService(t)
	seedTable(t, s, "sales", "orders")
	for _, month := range []string{"08", "09"} {
		_, aerr := s.createPartitionTyped(ctx, &createPartitionReq{DatabaseName: "sales", TableName: "orders",
			PartitionInput: &PartitionInput{Values: []string{"2026", month}}})
		mustOK(t, "CreatePartition", aerr)
	}
	_, aerr := s.createTableTyped(ctx, &createTableReq{DatabaseName: "sales", TableInput: &TableInput{
		Name:              "events",
		StorageDescriptor: &StorageDescriptor{Location: "s3://lakehouse/events"},
		Parameters: map[string]string{
			"TABLE_TYPE":        "iceberg",
			"metadata_location": "s3://lakehouse/events/metadata/00001-a.metadata.json",
		},
	}})
	mustOK(t, "CreateTable", aerr)

	// When: Glue contributes to the map
	resp := contributeGlue(t, s)

	// Then: the database is a node with a row per table, each with its
	// format and partition count
	var db *topology.Node
	for i := range resp.Nodes {
		if resp.Nodes[i].Service == serviceName {
			db = &resp.Nodes[i]
		}
	}
	if db == nil || db.Label != "sales" || db.GlueResourceType != topology.GlueDatabase || len(db.Tables) != 2 {
		t.Fatalf("database node = %+v", db)
	}
	events, orders := db.Tables[0], db.Tables[1]
	if events.Name != "events" || events.Format != "ICEBERG" || *events.Partitions != 0 {
		t.Errorf("events row = %+v", events)
	}
	if orders.Name != "orders" || orders.Format != "PARQUET" || *orders.Partitions != 2 || orders.Location != "s3://bucket/orders/" {
		t.Errorf("orders row = %+v", orders)
	}
	// And: one edge per bucket the tables' data is in
	var targets []string
	for _, e := range resp.Edges {
		if e.Type != "table-location" || e.Source != db.ID {
			t.Errorf("unexpected edge %+v", e)
		}
		targets = append(targets, e.Target)
	}
	slices.Sort(targets)
	if want := []string{"us-east-1::s3::bucket", "us-east-1::s3::lakehouse"}; !slices.Equal(targets, want) {
		t.Errorf("table-location targets = %v, want %v", targets, want)
	}
}

func TestContributeTopology_s3tablescatalogFederatesEachTableBucket(t *testing.T) {
	// Given: Glue wired to S3 Tables, which has one table bucket
	s := newFederatedService(t)

	// When: Glue contributes to the map
	resp := contributeGlue(t, s)

	// Then: s3tablescatalog is a catalog node, federating the bucket
	var catalog *topology.Node
	for i := range resp.Nodes {
		if resp.Nodes[i].GlueResourceType == topology.GlueCatalog {
			catalog = &resp.Nodes[i]
		}
	}
	if catalog == nil || catalog.Label != S3TablesCatalogName || catalog.ID != "us-east-1::glue-catalog::s3tablescatalog" {
		t.Fatalf("catalog node = %+v", catalog)
	}
	if len(resp.Edges) != 1 || resp.Edges[0].Type != "federation" || resp.Edges[0].Target != "us-east-1::s3tables::lake" {
		t.Errorf("edges = %+v", resp.Edges)
	}
}

func TestContributeTopology_noCatalogWithoutTableBuckets(t *testing.T) {
	// Given: Glue wired to S3 Tables, which has no table bucket
	s := newFederatedService(t)
	s.s3tables.(*fakeS3Tables).buckets = nil

	// When: Glue contributes to the map
	resp := contributeGlue(t, s)

	// Then: there is nothing for s3tablescatalog to federate, so it is not drawn
	for _, n := range resp.Nodes {
		if n.Service == serviceName {
			t.Errorf("unexpected node %+v", n)
		}
	}
}

func TestTableFormat(t *testing.T) {
	sd := func(serde, input string) *StorageDescriptor {
		return &StorageDescriptor{SerdeInfo: &SerDeInfo{SerializationLibrary: serde}, InputFormat: input}
	}
	cases := []struct {
		name  string
		table Table
		want  string
	}{
		{"iceberg marker wins", Table{Parameters: map[string]string{"table_type": "ICEBERG"}, StorageDescriptor: sd("x.ParquetHiveSerDe", "")}, "ICEBERG"},
		{"view", Table{TableType: "VIRTUAL_VIEW"}, "VIEW"},
		{"serde", Table{StorageDescriptor: sd("org.apache.hadoop.hive.ql.io.orc.OrcSerde", "")}, "ORC"},
		{"input format", Table{StorageDescriptor: sd("", "org.apache.hadoop.hive.ql.io.avro.AvroContainerInputFormat")}, "AVRO"},
		{"text is csv", Table{StorageDescriptor: sd("org.apache.hadoop.hive.serde2.lazy.LazySimpleSerDe", "")}, "CSV"},
		{"classification", Table{Parameters: map[string]string{"classification": "json"}}, "JSON"},
		{"nothing says", Table{}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// Given: a table; When: its format is read; Then: it is what the table says first
			if got := tableFormat(&c.table); got != c.want {
				t.Errorf("tableFormat = %q, want %q", got, c.want)
			}
		})
	}
}
