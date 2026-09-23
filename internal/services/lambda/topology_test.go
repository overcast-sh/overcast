package lambda

import (
	"context"
	"encoding/json"
	"slices"
	"testing"

	"github.com/overcast-sh/overcast/internal/config"
	"github.com/overcast-sh/overcast/internal/state"
	"github.com/overcast-sh/overcast/internal/topology"
)

// topologyFixture is a Lambda service over a memory store, plus a graph
// standing in for the other services' contributions.
type topologyFixture struct {
	t      *testing.T
	svc    *Service
	others *topology.Graph
}

func newTopologyFixture(t *testing.T) *topologyFixture {
	return &topologyFixture{
		t:      t,
		svc:    &Service{cfg: &config.Config{Region: "us-east-1"}, store: state.NewMemoryStore()},
		others: &topology.Graph{},
	}
}

func (f *topologyFixture) seed(ns, key string, v any) {
	f.t.Helper()
	raw, _ := json.Marshal(v)
	if err := f.svc.store.Set(context.Background(), ns, key, string(raw)); err != nil {
		f.t.Fatalf("seed %s %s: %v", ns, key, err)
	}
}

func (f *topologyFixture) function(region, name string, fn Function) {
	fn.Name = name
	fn.ARN = "arn:aws:lambda:" + region + ":000000000000:function:" + name
	f.seed(nsFunctions, region+"/"+name, fn)
}

// other adds a node another service would have contributed.
func (f *topologyFixture) other(region, kind, name string, aliases ...topology.Ref) {
	f.others.AddNode(topology.Node{ID: topology.NodeID(region, kind, name), Service: kind, Label: name, Region: region}, aliases...)
}

func (f *topologyFixture) build() topology.Response {
	f.t.Helper()
	g := &topology.Graph{}
	if err := f.svc.ContributeTopology(context.Background(), g); err != nil {
		f.t.Fatalf("ContributeTopology: %v", err)
	}
	return topology.Build("", f.others, g)
}

func edgesByID(resp topology.Response) map[string]topology.Edge {
	out := make(map[string]topology.Edge, len(resp.Edges))
	for _, e := range resp.Edges {
		out[e.ID] = e
	}
	return out
}

func edgeIDList(resp topology.Response) []string {
	ids := make([]string, 0, len(resp.Edges))
	for _, e := range resp.Edges {
		ids = append(ids, e.ID)
	}
	return ids
}

func TestContributeTopology_sqsEventSourceMapping(t *testing.T) {
	// Given: a queue, a function, and an event source mapping linking them.
	f := newTopologyFixture(t)
	f.other("us-east-1", "sqs", "my-queue")
	f.function("us-east-1", "my-function", Function{})
	f.seed(nsESM, "us-east-1/uuid-1", EventSourceMapping{
		UUID:           "uuid-1",
		FunctionArn:    "arn:aws:lambda:us-east-1:000000000000:function:my-function",
		EventSourceArn: "arn:aws:sqs:us-east-1:000000000000:my-queue",
	})

	// When: the map is built.
	edges := edgesByID(f.build())

	// Then: an esm edge runs from the queue to the function.
	e, ok := edges["esm::us-east-1::sqs::my-queue→us-east-1::lambda::my-function"]
	if !ok || e.Type != "esm" {
		t.Fatalf("expected SQS → Lambda esm edge, got %v", edges)
	}
}

func TestContributeTopology_dynamoDBMappingWithFilterGetsFilterNode(t *testing.T) {
	// Given: a table, a function, and a filtered stream mapping between them.
	f := newTopologyFixture(t)
	f.other("us-east-1", "dynamodb", "items")
	f.function("us-east-1", "stream-fn", Function{})
	f.seed(nsESM, "us-east-1/esm-1", EventSourceMapping{
		UUID:           "esm-1",
		FunctionArn:    "arn:aws:lambda:us-east-1:000000000000:function:stream-fn",
		EventSourceArn: "arn:aws:dynamodb:us-east-1:000000000000:table/items/stream/2026-01-01T00:00:00.000",
		FilterCriteria: &FilterCriteria{Filters: []Filter{{Pattern: `{"eventName":["INSERT"]}`}}},
	})

	// When: the map is built.
	resp := f.build()

	// Then: the filter is a node between the table and the function.
	filterID := "us-east-1::esm-filter::esm-1"
	var filter *topology.Node
	for i := range resp.Nodes {
		if resp.Nodes[i].ID == filterID {
			filter = &resp.Nodes[i]
		}
	}
	if filter == nil || filter.Service != "esm-filter" || filter.ESMID != "esm-1" || len(filter.FilterPatterns) != 1 {
		t.Fatalf("expected ESM filter node %s, got %+v", filterID, resp.Nodes)
	}
	edges := edgesByID(resp)
	if e := edges["esm-filter-in::esm-1"]; e.Source != "us-east-1::dynamodb::items" || e.Target != filterID {
		t.Errorf("filter input edge: got %+v", e)
	}
	if e := edges["esm-filter-out::esm-1"]; e.Source != filterID || e.Target != "us-east-1::lambda::stream-fn" {
		t.Errorf("filter output edge: got %+v", e)
	}
}

func TestContributeTopology_mappingWithStaleRegionFallsBackByName(t *testing.T) {
	// Given: resources in ap-southeast-2, with mappings whose ARNs name
	// us-east-1 (stale data, or a cross-stack import from another region).
	f := newTopologyFixture(t)
	f.other("ap-southeast-2", "sqs", "my-queue")
	f.other("ap-southeast-2", "dynamodb", "my-table")
	f.function("ap-southeast-2", "my-function", Function{})
	for key, source := range map[string]string{
		"ap-southeast-2/esm-sqs": "arn:aws:sqs:us-east-1:000000000000:my-queue",
		"ap-southeast-2/esm-ddb": "arn:aws:dynamodb:us-east-1:000000000000:table/my-table/stream/2026-01-01T00:00:00.000",
	} {
		f.seed(nsESM, key, EventSourceMapping{
			FunctionArn:    "arn:aws:lambda:us-east-1:000000000000:function:my-function",
			EventSourceArn: source,
		})
	}

	// When: the map is built.
	edges := edgesByID(f.build())

	// Then: both mappings resolve to the ap-southeast-2 nodes.
	for _, id := range []string{
		"esm::ap-southeast-2::sqs::my-queue→ap-southeast-2::lambda::my-function",
		"esm::ap-southeast-2::dynamodb::my-table→ap-southeast-2::lambda::my-function",
	} {
		if _, ok := edges[id]; !ok {
			t.Errorf("missing esm edge %s, got %v", id, edges)
		}
	}
}

func TestContributeTopology_imageFunctionLinksToItsRepository(t *testing.T) {
	// Given: a repository registered under its URI, and an image function
	// pinned to a digest of it.
	f := newTopologyFixture(t)
	f.other("us-east-1", "ecr", "sample-app", topology.Image("https://localhost:5000/000000000000/sample-app"))
	f.function("us-east-1", "image-fn", Function{PackageType: "Image", ImageUri: "localhost:5000/000000000000/sample-app@sha256:deadbeef"})

	// When: the map is built.
	edges := edgesByID(f.build())

	// Then: the normalised image reference finds the repository.
	if _, ok := edges["ecr-lambda::us-east-1::ecr::sample-app→us-east-1::lambda::image-fn"]; !ok {
		t.Fatalf("expected ECR → Lambda edge, got %v", edges)
	}
}

func TestContributeTopology_logGroupPrefersFunctionRegion(t *testing.T) {
	// Given: a function with a custom log group that exists in its own region
	// and another, and a function whose default log group exists elsewhere only.
	f := newTopologyFixture(t)
	f.other("eu-west-1", "logs", "/custom/app")
	f.other("us-west-2", "logs", "/custom/app")
	f.other("eu-west-1", "logs", "/aws/lambda/other")
	f.function("us-west-2", "app", Function{LogGroup: "/custom/app"})
	f.function("us-west-2", "other", Function{})

	// When: the map is built.
	resp := f.build()

	// Then: the first links to its own region's group, the second falls back.
	want := []string{
		"logs::us-west-2::lambda::app→us-west-2::logs::/custom/app",
		"logs::us-west-2::lambda::other→eu-west-1::logs::/aws/lambda/other",
	}
	if got := edgeIDList(resp); !slices.Equal(got, want) {
		t.Errorf("log edges: got %v, want %v", got, want)
	}
}

func TestContributeTopology_stackOwnsFunctionByName(t *testing.T) {
	// Given: a function, and a stack that records it by the physical ID
	// CloudFormation gives a function: its name.
	f := newTopologyFixture(t)
	f.function("us-east-1", "api", Function{})
	f.others.SetStack(topology.CFN("us-east-1", "AWS::Lambda::Function", "api"), "app-stack")

	// When: the map is built.
	resp := f.build()

	// Then: the function node carries the stack's name.
	if len(resp.Nodes) != 1 || resp.Nodes[0].StackName == nil || *resp.Nodes[0].StackName != "app-stack" {
		t.Fatalf("expected the function to be owned by app-stack, got %+v", resp.Nodes)
	}
}
