package topology

import (
	"slices"
	"testing"
)

func node(region, kind, name string) Node {
	return Node{ID: NodeID(region, kind, name), Service: kind, Label: name, Region: region}
}

func edgeIDs(resp Response) []string {
	ids := make([]string, 0, len(resp.Edges))
	for _, e := range resp.Edges {
		ids = append(ids, e.ID)
	}
	return ids
}

func TestBuild_linkResolvesExactNodesAndDerivesID(t *testing.T) {
	// Given: a queue and a function contributed by different graphs, and a
	// link between them plus one to a node that does not exist.
	sqs, lambda := &Graph{}, &Graph{}
	sqs.AddNode(node("us-east-1", "sqs", "q"))
	lambda.AddNode(node("eu-west-1", "lambda", "fn"))
	lambda.AddLink(Link{Source: ID("us-east-1", "sqs", "q"), Target: ID("eu-west-1", "lambda", "fn"), IDPrefix: "esm", Type: "esm"})
	lambda.AddLink(Link{Source: ID("us-east-1", "sqs", "gone"), Target: ID("eu-west-1", "lambda", "fn"), IDPrefix: "esm", Type: "esm"})

	// When: the graphs are built.
	resp := Build("", sqs, lambda)

	// Then: only the resolvable link becomes an edge, named from its endpoints
	// and tagged with their regions.
	if len(resp.Edges) != 1 {
		t.Fatalf("edges: got %v, want one", edgeIDs(resp))
	}
	e := resp.Edges[0]
	if e.ID != "esm::us-east-1::sqs::q→eu-west-1::lambda::fn" {
		t.Errorf("edge ID: got %q", e.ID)
	}
	if e.SourceRegion != "us-east-1" || e.TargetRegion != "eu-west-1" {
		t.Errorf("edge regions: got %s → %s", e.SourceRegion, e.TargetRegion)
	}
	if !slices.Equal(resp.Regions, []string{"eu-west-1", "us-east-1"}) {
		t.Errorf("regions: got %v, want sorted", resp.Regions)
	}
}

func TestBuild_anyRegionPrefersExactThenLowestRegion(t *testing.T) {
	// Given: the same queue name in two regions other than the one a stale
	// ARN names, and in the region a second ARN names.
	g := &Graph{}
	g.AddNode(node("us-west-2", "sqs", "q"))
	g.AddNode(node("ap-southeast-2", "sqs", "q"))
	g.AddNode(node("us-east-1", "lambda", "fn"))
	g.AddLink(Link{Source: ID("eu-west-1", "sqs", "q").AnyRegion(), Target: ID("us-east-1", "lambda", "fn"), IDPrefix: "stale"})
	g.AddLink(Link{Source: ID("us-west-2", "sqs", "q").AnyRegion(), Target: ID("us-east-1", "lambda", "fn"), IDPrefix: "exact"})
	g.AddLink(Link{Source: ID("eu-west-1", "sqs", "q"), Target: ID("us-east-1", "lambda", "fn"), IDPrefix: "strict"})

	// When: the graph is built.
	resp := Build("", g)

	// Then: the stale ref falls back to the lowest region, the exact ref keeps
	// its own region, and a ref without AnyRegion does not fall back.
	want := []string{
		"stale::ap-southeast-2::sqs::q→us-east-1::lambda::fn",
		"exact::us-west-2::sqs::q→us-east-1::lambda::fn",
	}
	if got := edgeIDs(resp); !slices.Equal(got, want) {
		t.Errorf("edges: got %v, want %v", got, want)
	}
}

func TestBuild_aliasesResolveStacksAndImages(t *testing.T) {
	// Given: a function registered under its CloudFormation physical ID, a
	// repository registered under its image, and a stack and a consumer that
	// know only those aliases.
	lambda, ecr, cfn := &Graph{}, &Graph{}, &Graph{}
	lambda.AddNode(node("us-east-1", "lambda", "fn"), CFN("us-east-1", "AWS::Lambda::Function", "fn"))
	ecr.AddNode(node("us-east-1", "ecr", "app"), Image("localhost:5000/app"))
	lambda.AddLink(Link{Source: Image("https://localhost:5000/app@sha256:abc"), Target: ID("us-east-1", "lambda", "fn"), IDPrefix: "ecr-lambda"})
	cfn.SetStack(CFN("us-east-1", "AWS::Lambda::Function", "fn"), "app-stack")
	cfn.SetStack(CFN("eu-west-1", "AWS::Lambda::Function", "fn"), "other-region")

	// When: the graphs are built.
	resp := Build("", lambda, ecr, cfn)

	// Then: the digest-pinned image finds the repository, and only the stack
	// in the function's own region owns it.
	if got := edgeIDs(resp); !slices.Equal(got, []string{"ecr-lambda::us-east-1::ecr::app→us-east-1::lambda::fn"}) {
		t.Errorf("edges: got %v", got)
	}
	if s := resp.Nodes[0].StackName; s == nil || *s != "app-stack" {
		t.Errorf("stack name: got %v, want app-stack", s)
	}
}

func TestBuild_anchoredNodeNeedsEveryAnchor(t *testing.T) {
	// Given: two filter nodes, one between existing nodes and one whose
	// source table does not exist.
	g := &Graph{}
	g.AddNode(node("us-east-1", "dynamodb", "t"))
	g.AddNode(node("us-east-1", "lambda", "fn"))
	g.AddAnchoredNode(node("us-east-1", "esm-filter", "kept"), ID("us-east-1", "dynamodb", "t"), ID("us-east-1", "lambda", "fn"))
	g.AddAnchoredNode(node("us-east-1", "esm-filter", "dropped"), ID("us-east-1", "dynamodb", "gone"), ID("us-east-1", "lambda", "fn"))
	g.AddLink(Link{Source: ID("us-east-1", "esm-filter", "kept"), Target: ID("us-east-1", "lambda", "fn"), ID: "esm-filter-out::kept"})

	// When: the graph is built.
	resp := Build("", g)

	// Then: only the anchored node is kept, and it can be a link endpoint.
	if len(resp.Nodes) != 3 || resp.Nodes[2].Label != "kept" {
		t.Errorf("nodes: got %+v", resp.Nodes)
	}
	if got := edgeIDs(resp); !slices.Equal(got, []string{"esm-filter-out::kept"}) {
		t.Errorf("edges: got %v", got)
	}
}

func TestBuild_regionFilterKeepsOnlyThatRegion(t *testing.T) {
	// Given: nodes in two regions, an edge between them, and a phantom edge.
	g := &Graph{}
	g.AddNode(node("us-east-1", "sqs", "q"))
	g.AddNode(node("eu-west-1", "lambda", "fn"))
	g.AddLink(Link{Source: ID("us-east-1", "sqs", "q"), Target: ID("eu-west-1", "lambda", "fn"), IDPrefix: "esm"})
	g.AddEdge(Edge{ID: "nested-stack::a→b", Source: "stack::us-east-1::a", Target: "stack::us-east-1::b"})

	// When: the graph is built for a region, and for a region with no nodes.
	east := Build("us-east-1", g)
	empty := Build("sa-east-1", g)

	// Then: the cross-region edge is dropped with its far endpoint, phantom
	// edges pass through, and the filter region is always listed.
	if len(east.Nodes) != 1 || east.Nodes[0].Region != "us-east-1" {
		t.Errorf("filtered nodes: got %+v", east.Nodes)
	}
	if got := edgeIDs(east); !slices.Equal(got, []string{"nested-stack::a→b"}) {
		t.Errorf("filtered edges: got %v", got)
	}
	if len(empty.Nodes) != 0 || !slices.Equal(empty.Regions, []string{"sa-east-1"}) {
		t.Errorf("empty region: got nodes %+v, regions %v", empty.Nodes, empty.Regions)
	}
}

func TestBuild_absorbFoldsANodeIntoAnother(t *testing.T) {
	// Given: an S3 bucket that is a table bucket's warehouse, an edge to it
	// from another service (by exact ID and by a stale region), and an
	// absorption whose target does not exist.
	s3, tables, glue := &Graph{}, &Graph{}, &Graph{}
	s3.AddNode(node("us-east-1", "s3", "wh--table-s3"), CFN("us-east-1", "AWS::S3::Bucket", "wh--table-s3"))
	s3.AddNode(node("us-east-1", "s3", "orphan--table-s3"))
	tables.AddNode(node("us-east-1", "s3tables", "lake"))
	tables.Absorb(ID("us-east-1", "s3", "wh--table-s3"), ID("us-east-1", "s3tables", "lake"))
	tables.Absorb(ID("us-east-1", "s3", "orphan--table-s3"), ID("us-east-1", "s3tables", "gone"))
	glue.AddNode(node("us-east-1", "glue", "db"))
	glue.AddLink(Link{Source: ID("us-east-1", "glue", "db"), Target: ID("us-east-1", "s3", "wh--table-s3"), IDPrefix: "loc"})
	glue.AddLink(Link{Source: ID("us-east-1", "glue", "db"), Target: ID("eu-west-1", "s3", "wh--table-s3").AnyRegion(), ID: "stale"})
	glue.AddLink(Link{Source: ID("us-east-1", "glue", "db"), Target: CFN("us-east-1", "AWS::S3::Bucket", "wh--table-s3"), ID: "alias"})
	glue.SetStack(CFN("us-east-1", "AWS::S3::Bucket", "wh--table-s3"), "lake-stack")

	// When: the graphs are built.
	resp := Build("", s3, tables, glue)

	// Then: the warehouse is gone, and every edge and the stack that named
	// it land on the table bucket; the absorption with no target leaves its
	// node alone.
	var labels []string
	for _, n := range resp.Nodes {
		labels = append(labels, n.Label)
	}
	if !slices.Equal(labels, []string{"orphan--table-s3", "lake", "db"}) {
		t.Errorf("nodes: got %v", labels)
	}
	for _, e := range resp.Edges {
		if e.Target != "us-east-1::s3tables::lake" {
			t.Errorf("edge %s: target %s, want the table bucket", e.ID, e.Target)
		}
	}
	if len(resp.Edges) != 3 {
		t.Errorf("edges: got %v", edgeIDs(resp))
	}
	if lake := resp.Nodes[1]; lake.StackName == nil || *lake.StackName != "lake-stack" {
		t.Errorf("table bucket's stack: got %v", lake.StackName)
	}
	if resp.Nodes[0].StackName != nil {
		t.Errorf("stack landed on %s", resp.Nodes[0].ID)
	}
}
