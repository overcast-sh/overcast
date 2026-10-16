package lambdaweb

import (
	"context"
	"testing"

	"go.uber.org/zap"

	"github.com/overcast-sh/overcast/internal/clock"
	"github.com/overcast-sh/overcast/internal/config"
	"github.com/overcast-sh/overcast/internal/state"
	"github.com/overcast-sh/overcast/internal/topology"
)

func newTestService(t *testing.T) *Service {
	t.Helper()
	cfg := &config.Config{Region: "us-east-1", AccountID: "111122223333"}
	return New(cfg, state.NewMemoryStore(), zap.NewNop(), clock.NewMock())
}

func testRevisionConfig(bucket, key string) *revisionConfigIn {
	return &revisionConfigIn{
		BuildConfig: &buildConfig{
			CodeConfig:    codeConfig{S3Object: s3Object{Bucket: bucket, Key: key}},
			RuntimeConfig: runtimeConfig{Runtime: "nodejs24.x"},
		},
		Service: &serviceConfig{ExecutionRoleARN: "arn:aws:iam::111122223333:role/web"},
	}
}

func TestContributeTopology_nodeListsEndpointsAndLinksToCodeBucket(t *testing.T) {
	// Given: a web function with a revision, a LatestRevision endpoint, and a
	// stack that owns the function
	ctx := context.Background()
	s := newTestService(t)
	out, aerr := s.createWebFunctionTyped(ctx, &createWebFunctionRequest{
		FunctionName:   "shop",
		RevisionConfig: testRevisionConfig("code-bucket", "shop.zip"),
		EndpointConfig: &endpointConfigIn{
			EndpointName: "prod", EndpointType: endpointHomeRegion, AuthType: authIam, AutoDeploymentMode: deployLatestRevision,
		},
	})
	if aerr != nil {
		t.Fatalf("create: %v", aerr)
	}

	// When: it contributes to the map beside S3, which holds the code bucket
	g, s3, cfn := &topology.Graph{}, &topology.Graph{}, &topology.Graph{}
	if err := s.ContributeTopology(ctx, g); err != nil {
		t.Fatalf("ContributeTopology: %v", err)
	}
	s3.AddNode(topology.Node{ID: topology.NodeID("us-east-1", "s3", "code-bucket"), Service: "s3", Label: "code-bucket", Region: "us-east-1"})
	cfn.SetStack(topology.CFN("us-east-1", "AWS::Lambda::WebFunctionEndpoint", out.Endpoint.EndpointARN), "storefront")
	resp := topology.Build("", g, s3, cfn)

	// Then: the function is one node, owned by the stack through its endpoint
	var node *topology.Node
	for i := range resp.Nodes {
		if resp.Nodes[i].Service == serviceName {
			node = &resp.Nodes[i]
		}
	}
	if node == nil || node.ID != "us-east-1::lambdaweb::shop" || node.StackName == nil || *node.StackName != "storefront" {
		t.Fatalf("node = %+v", node)
	}

	// And: it lists the endpoint with the revision it serves
	if node.WebRevisions == nil || *node.WebRevisions != 1 || len(node.WebEndpoints) != 1 {
		t.Fatalf("node = %+v", node)
	}
	ep := node.WebEndpoints[0]
	if ep.Name != "prod" || ep.AuthType != authIam || len(ep.Serves) != 1 ||
		ep.Serves[0].RevisionID != out.Revision.RevisionID || ep.Serves[0].Weight != 100 || ep.DomainName == "" {
		t.Errorf("endpoint = %+v", ep)
	}

	// And: an edge runs to the bucket the code is stored in
	if len(resp.Edges) != 1 || resp.Edges[0].Type != "code" || resp.Edges[0].Target != "us-east-1::s3::code-bucket" {
		t.Errorf("edges = %+v", resp.Edges)
	}
}

func TestContributeTopology_skipsUndecodableRecords(t *testing.T) {
	// Given: a corrupt function record beside a good one
	ctx := context.Background()
	s := newTestService(t)
	if _, aerr := s.createWebFunctionTyped(ctx, &createWebFunctionRequest{FunctionName: "good"}); aerr != nil {
		t.Fatalf("create: %v", aerr)
	}
	_ = s.store.Set(ctx, nsFunctions, "us-east-1/corrupt", "{not json")

	// When: it contributes
	g := &topology.Graph{}
	if err := s.ContributeTopology(ctx, g); err != nil {
		t.Fatalf("ContributeTopology: %v", err)
	}

	// Then: only the good function is on the map
	resp := topology.Build("", g)
	if len(resp.Nodes) != 1 || resp.Nodes[0].Label != "good" {
		t.Errorf("nodes = %+v", resp.Nodes)
	}
}
