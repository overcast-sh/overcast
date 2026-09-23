package router

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/overcast-sh/overcast/internal/state"
	"github.com/overcast-sh/overcast/internal/topology"
)

// buildTopology builds the map from the legacy contributor's view of byNS,
// alongside nodes standing in for the services that contribute their own.
func buildTopology(defaultRegion string, byNS map[string][]state.KV, regionFilter string, others ...topology.Node) topology.Response {
	legacy, migrated := &topology.Graph{}, &topology.Graph{}
	contributeLegacyTopology(defaultRegion, byNS, legacy)
	for _, n := range others {
		migrated.AddNode(n)
	}
	return topology.Build(regionFilter, migrated, legacy)
}

// otherNode is a node a migrated service's contributor would have added.
func otherNode(region, kind, name string) topology.Node {
	return topology.Node{ID: topology.NodeID(region, kind, name), Service: kind, Label: name, Region: region}
}

func TestBuildTopologyIncludesWAFWebACLs(t *testing.T) {
	// Given: a metadata-only WAFv2 Web ACL is stored in the service namespace.
	webACLPayload, _ := json.Marshal(map[string]any{
		"ID":    "acl-123",
		"Name":  "edge-acl",
		"Scope": "REGIONAL",
		"ARN":   "arn:aws:wafv2:us-west-2:000000000000:regional/webacl/edge-acl/acl-123",
		"Rules": []any{
			map[string]any{"Name": "block-bots"},
			map[string]any{"Name": "rate-limit"},
		},
	})

	// When: topology is built from the WAF namespace.
	resp := buildTopology("us-east-1", map[string][]state.KV{
		"waf:webacls": {{Key: "us-west-2/REGIONAL/acl-123", Value: string(webACLPayload)}},
	}, "")

	// Then: the Web ACL appears as a WAF node with useful metadata.
	if len(resp.Nodes) != 1 {
		t.Fatalf("expected one WAF node, got %#v", resp.Nodes)
	}
	node := resp.Nodes[0]
	if node.ID != "us-west-2::waf::acl-123" || node.Service != "waf" || node.Label != "edge-acl" {
		t.Fatalf("unexpected WAF node identity: %#v", node)
	}
	if node.Scope != "REGIONAL" || node.RuleCount == nil || *node.RuleCount != 2 {
		t.Fatalf("unexpected WAF node metadata: %#v", node)
	}
}

func TestBuildTopologyAddsECREdgesForECSConsumers(t *testing.T) {
	repoPayload, _ := json.Marshal(map[string]any{
		"repositoryArn":  "arn:aws:ecr:us-east-1:000000000000:repository/sample-app",
		"repositoryName": "sample-app",
		"repositoryUri":  "localhost:5000/000000000000/sample-app",
	})
	ecsServicePayload, _ := json.Marshal(map[string]any{
		"serviceName":    "web",
		"serviceArn":     "arn:aws:ecs:us-east-1:000000000000:service/demo/web",
		"clusterArn":     "arn:aws:ecs:us-east-1:000000000000:cluster/demo",
		"taskDefinition": "arn:aws:ecs:us-east-1:000000000000:task-definition/demo:3",
		"status":         "ACTIVE",
	})
	taskDefPayload, _ := json.Marshal(map[string]any{
		"taskDefinitionArn": "arn:aws:ecs:us-east-1:000000000000:task-definition/demo:3",
		"containerDefinitions": []map[string]any{{
			"name":  "app",
			"image": "localhost:5000/000000000000/sample-app:prod",
		}},
	})

	resp := buildTopology("us-east-1", map[string][]state.KV{
		tNsECRRepos:    {{Key: "us-east-1/sample-app", Value: string(repoPayload)}},
		tNsECSServices: {{Key: "us-east-1/web", Value: string(ecsServicePayload)}},
		tNsECSTaskDefs: {{Key: "us-east-1/demo:3", Value: string(taskDefPayload)}},
	}, "")

	nodes := map[string]topology.Node{}
	for _, node := range resp.Nodes {
		nodes[node.ID] = node
	}
	if _, ok := nodes["us-east-1::ecr::sample-app"]; !ok {
		t.Fatalf("expected ECR repository node, got %#v", resp.Nodes)
	}
	if got := nodes["us-east-1::ecr::sample-app"].RepositoryUri; got != "localhost:5000/000000000000/sample-app" {
		t.Fatalf("expected repositoryUri to propagate to topology node, got %q", got)
	}

	edges := map[string]topology.Edge{}
	for _, edge := range resp.Edges {
		edges[edge.ID] = edge
	}
	if _, ok := edges["ecr-ecs::us-east-1::ecr::sample-app→us-east-1::ecs-service::demo/web"]; !ok {
		t.Fatalf("expected ECR → ECS service edge, got %#v", resp.Edges)
	}
}

func TestBuildTopologyECSResourcesUseCurrentClusterOwnership(t *testing.T) {
	// Given: one cluster with a Fargate service, one running task, and one stopped task.
	clusterPayload, _ := json.Marshal(map[string]any{
		"clusterName": "demo",
		"clusterArn":  "arn:aws:ecs:us-east-1:000000000000:cluster/demo",
		"status":      "ACTIVE",
	})
	servicePayload, _ := json.Marshal(map[string]any{
		"serviceName":  "web",
		"serviceArn":   "arn:aws:ecs:us-east-1:000000000000:service/demo/web",
		"clusterArn":   "arn:aws:ecs:us-east-1:000000000000:cluster/demo",
		"status":       "ACTIVE",
		"desiredCount": 1,
		"runningCount": 1,
	})
	runningTaskPayload, _ := json.Marshal(map[string]any{
		"taskArn":    "arn:aws:ecs:us-east-1:000000000000:task/demo/running-task",
		"clusterArn": "arn:aws:ecs:us-east-1:000000000000:cluster/demo",
		"lastStatus": "RUNNING",
		"group":      "service:web",
	})
	stoppedTaskPayload, _ := json.Marshal(map[string]any{
		"taskArn":    "arn:aws:ecs:us-east-1:000000000000:task/demo/stopped-task",
		"clusterArn": "arn:aws:ecs:us-east-1:000000000000:cluster/demo",
		"lastStatus": "STOPPED",
		"group":      "service:web",
	})

	// When: the topology is built from the stored ECS records.
	resp := buildTopology("us-east-1", map[string][]state.KV{
		tNsClusters:    {{Key: "us-east-1/demo", Value: string(clusterPayload)}},
		tNsECSServices: {{Key: "us-east-1/demo/web", Value: string(servicePayload)}},
		tNsECSTasks: {
			{Key: "us-east-1/demo/running-task", Value: string(runningTaskPayload)},
			{Key: "us-east-1/demo/stopped-task", Value: string(stoppedTaskPayload)},
		},
	}, "")

	// Then: only the current task is shown, and every edge points at the real cluster/service nodes.
	nodes := map[string]topology.Node{}
	for _, node := range resp.Nodes {
		nodes[node.ID] = node
	}
	if _, ok := nodes["us-east-1::ecs::demo"]; !ok {
		t.Fatalf("expected real cluster node, got %#v", resp.Nodes)
	}
	serviceNode, ok := nodes["us-east-1::ecs-service::demo/web"]
	if !ok {
		t.Fatalf("expected service node owned by demo, got %#v", resp.Nodes)
	}
	if serviceNode.ClusterName != "demo" || serviceNode.ECSResourceType != "service" {
		t.Fatalf("unexpected service navigation metadata: %#v", serviceNode)
	}
	serviceJSON, _ := json.Marshal(serviceNode)
	if !strings.Contains(string(serviceJSON), `"runningCount":1`) || !strings.Contains(string(serviceJSON), `"desiredCount":1`) {
		t.Fatalf("service node should explain its running capacity: %s", serviceJSON)
	}
	taskNode, ok := nodes["us-east-1::ecs-task::demo/running-task"]
	if !ok {
		t.Fatalf("expected running task node, got %#v", resp.Nodes)
	}
	if taskNode.Label != "running-task" || taskNode.ClusterName != "demo" || taskNode.TaskID != "running-task" {
		t.Fatalf("unexpected task node: %#v", taskNode)
	}
	if _, ok := nodes["us-east-1::ecs-task::demo/stopped-task"]; ok {
		t.Fatalf("stopped task should not remain on the current-resource map: %#v", resp.Nodes)
	}

	edges := map[string]topology.Edge{}
	for _, edge := range resp.Edges {
		edges[edge.ID] = edge
	}
	if _, ok := edges["ecs-svc::us-east-1::ecs::demo→us-east-1::ecs-service::demo/web"]; !ok {
		t.Fatalf("expected cluster to service edge, got %#v", resp.Edges)
	}
	if _, ok := edges["ecs-task::us-east-1::ecs-service::demo/web→us-east-1::ecs-task::demo/running-task"]; !ok {
		t.Fatalf("expected service to running task edge, got %#v", resp.Edges)
	}
}

func TestBuildTopologyECSChildrenUseOwningClusterRegion(t *testing.T) {
	// Given: a canonical cluster record and stale child-resource ARNs from another region.
	clusterPayload, _ := json.Marshal(map[string]any{
		"clusterName": "demo",
		"clusterArn":  "arn:aws:ecs:ap-southeast-2:000000000000:cluster/demo",
	})
	servicePayload, _ := json.Marshal(map[string]any{
		"serviceName": "web",
		"serviceArn":  "arn:aws:ecs:us-east-1:000000000000:service/demo/web",
		"clusterArn":  "arn:aws:ecs:ap-southeast-2:000000000000:cluster/demo",
	})
	taskPayload, _ := json.Marshal(map[string]any{
		"taskArn":    "arn:aws:ecs:us-east-1:000000000000:task/demo/task-1",
		"clusterArn": "arn:aws:ecs:ap-southeast-2:000000000000:cluster/demo",
		"lastStatus": "RUNNING",
		"group":      "service:web",
	})

	// When: the topology is built.
	resp := buildTopology("us-east-1", map[string][]state.KV{
		tNsClusters:    {{Key: "ap-southeast-2/demo", Value: string(clusterPayload)}},
		tNsECSServices: {{Key: "ap-southeast-2/demo/web", Value: string(servicePayload)}},
		tNsECSTasks:    {{Key: "ap-southeast-2/demo/task-1", Value: string(taskPayload)}},
	}, "")

	// Then: service and task navigation stay in the owning cluster's region.
	nodes := map[string]topology.Node{}
	for _, node := range resp.Nodes {
		nodes[node.ID] = node
	}
	for _, id := range []string{
		"ap-southeast-2::ecs-service::demo/web",
		"ap-southeast-2::ecs-task::demo/task-1",
	} {
		if _, ok := nodes[id]; !ok {
			t.Fatalf("expected canonical ECS child node %q, got %#v", id, resp.Nodes)
		}
	}
}

func TestBuildTopologyMatchesECRConsumersWithNormalizedImageRefs(t *testing.T) {
	repoPayload, _ := json.Marshal(map[string]any{
		"repositoryArn":  "arn:aws:ecr:us-east-1:000000000000:repository/sample-app",
		"repositoryName": "sample-app",
		"repositoryUri":  "https://localhost:5000/000000000000/sample-app",
	})
	ecsServicePayload, _ := json.Marshal(map[string]any{
		"serviceName":    "web",
		"serviceArn":     "arn:aws:ecs:us-east-1:000000000000:service/demo/web",
		"clusterArn":     "arn:aws:ecs:us-east-1:000000000000:cluster/demo",
		"taskDefinition": "arn:aws:ecs:us-east-1:000000000000:task-definition/demo:3",
		"status":         "ACTIVE",
	})
	taskDefPayload, _ := json.Marshal(map[string]any{
		"taskDefinitionArn": "arn:aws:ecs:us-east-1:000000000000:task-definition/demo:3",
		"containerDefinitions": []map[string]any{{
			"name":  "app",
			"image": "https://localhost:5000/000000000000/sample-app:prod",
		}},
	})

	resp := buildTopology("us-east-1", map[string][]state.KV{
		tNsECRRepos:    {{Key: "us-east-1/sample-app", Value: string(repoPayload)}},
		tNsECSServices: {{Key: "us-east-1/web", Value: string(ecsServicePayload)}},
		tNsECSTaskDefs: {{Key: "us-east-1/demo:3", Value: string(taskDefPayload)}},
	}, "")

	edges := map[string]topology.Edge{}
	for _, edge := range resp.Edges {
		edges[edge.ID] = edge
	}
	if _, ok := edges["ecr-ecs::us-east-1::ecr::sample-app→us-east-1::ecs-service::demo/web"]; !ok {
		t.Fatalf("expected normalized ECR → ECS service edge, got %#v", resp.Edges)
	}
}

func TestBuildTopologyIncludesAllElastiCacheResourceTypes(t *testing.T) {
	clusterPayload, _ := json.Marshal(map[string]any{
		"CacheClusterId":     "session-cache",
		"CacheClusterStatus": "available",
		"Engine":             "redis",
	})
	serverlessPayload, _ := json.Marshal(map[string]any{
		"ServerlessCacheName": "api-cache",
		"Status":              "available",
		"Engine":              "redis",
	})
	replicationPayload, _ := json.Marshal(map[string]any{
		"ReplicationGroupId": "checkout-rg",
		"Status":             "available",
		"Engine":             "redis",
	})

	resp := buildTopology("us-east-1", map[string][]state.KV{
		tNsCacheClusters:          {{Key: "us-east-1/session-cache", Value: string(clusterPayload)}},
		tNsServerlessCaches:       {{Key: "us-west-2/api-cache", Value: string(serverlessPayload)}},
		tNsCacheReplicationGroups: {{Key: "eu-west-1/checkout-rg", Value: string(replicationPayload)}},
	}, "")

	nodes := map[string]topology.Node{}
	for _, node := range resp.Nodes {
		nodes[node.ID] = node
	}

	for _, want := range []struct {
		id     string
		label  string
		region string
	}{
		{id: "us-east-1::elasticache::session-cache", label: "session-cache", region: "us-east-1"},
		{id: "us-west-2::elasticache::api-cache", label: "api-cache", region: "us-west-2"},
		{id: "eu-west-1::elasticache::checkout-rg", label: "checkout-rg", region: "eu-west-1"},
	} {
		node, ok := nodes[want.id]
		if !ok {
			t.Fatalf("expected ElastiCache node %q, got nodes: %v", want.id, keys(nodes))
		}
		if node.Service != "elasticache" {
			t.Errorf("node %q service: got %q, want elasticache", want.id, node.Service)
		}
		if node.Label != want.label {
			t.Errorf("node %q label: got %q, want %q", want.id, node.Label, want.label)
		}
		if node.Region != want.region {
			t.Errorf("node %q region: got %q, want %q", want.id, node.Region, want.region)
		}
		if node.Status != "available" {
			t.Errorf("node %q status: got %q, want available", want.id, node.Status)
		}
	}
}

func TestCfnResourceRefMapsNonDefaultTypes(t *testing.T) {
	tests := []struct {
		resType    string
		physicalID string
		want       topology.Ref
	}{
		{"AWS::ApiGateway::RestApi", "abc123", topology.ID("us-east-1", "apigateway", "abc123")},
		{"AWS::ApiGatewayV2::Api", "def456", topology.ID("us-east-1", "apigateway", "def456")},
		{"AWS::Cognito::UserPool", "us-east-1_A1B2C3D4", topology.ID("us-east-1", "cognito", "us-east-1_A1B2C3D4")},
		{"AWS::AppSync::GraphQLApi", "abc123def456", topology.ID("us-east-1", "appsync", "abc123def456")},
		{"AWS::CloudFront::Distribution", "E1234567890ABC", topology.ID("us-east-1", "cloudfront", "E1234567890ABC")},
		{"AWS::WAFv2::WebACL", "REGIONAL/acl-123", topology.ID("us-east-1", "waf", "acl-123")},
		{"AWS::ElastiCache::CacheCluster", "cache-1", topology.ID("us-east-1", "elasticache", "cache-1")},
		{"AWS::ElastiCache::ServerlessCache", "cache-2", topology.ID("us-east-1", "elasticache", "cache-2")},
		{"AWS::ElastiCache::ReplicationGroup", "rg-1", topology.ID("us-east-1", "elasticache", "rg-1")},
		// Types whose service contributes its own topology, and types with no
		// node, resolve through the alias the service registers (if any).
		{"AWS::SQS::Queue", "arn:aws:sqs:us-east-1:000000000000:q", topology.CFN("us-east-1", "AWS::SQS::Queue", "arn:aws:sqs:us-east-1:000000000000:q")},
		{"AWS::Lambda::Function", "fn", topology.CFN("us-east-1", "AWS::Lambda::Function", "fn")},
		{"AWS::ApiGateway::Resource", "abc123/res1", topology.CFN("us-east-1", "AWS::ApiGateway::Resource", "abc123/res1")},
	}
	for _, tt := range tests {
		if got := cfnResourceRef(tCFNResource{Type: tt.resType, PhysicalID: tt.physicalID}, "us-east-1"); got != tt.want {
			t.Errorf("cfnResourceRef(%s, %s) = %+v, want %+v", tt.resType, tt.physicalID, got, tt.want)
		}
	}
}

func TestBuildTopologyCountsAPIGatewayRoutesWithRegionPrefixedKeys(t *testing.T) {
	restAPIPayload, _ := json.Marshal(map[string]any{
		"id":             "abc123",
		"name":           "my-api",
		"rootResourceId": "root1",
	})
	resourcePayload, _ := json.Marshal(map[string]any{
		"id":              "res1",
		"pathPart":        "items",
		"resourceMethods": map[string]any{},
	})
	resource2Payload, _ := json.Marshal(map[string]any{
		"id":              "res2",
		"pathPart":        "users",
		"resourceMethods": map[string]any{},
	})
	stagePayload, _ := json.Marshal(map[string]any{
		"stageName":    "prod",
		"deploymentId": "deploy1",
	})

	resp := buildTopology("ap-southeast-2", map[string][]state.KV{
		tNsRestAPIs: {{Key: "ap-southeast-2/abc123", Value: string(restAPIPayload)}},
		tNsAPIResources: {
			{Key: "ap-southeast-2/abc123/res1", Value: string(resourcePayload)},
			{Key: "ap-southeast-2/abc123/res2", Value: string(resource2Payload)},
		},
		tNsAPIStages: {{Key: "ap-southeast-2/abc123/prod", Value: string(stagePayload)}},
	}, "")

	nodes := map[string]topology.Node{}
	for _, node := range resp.Nodes {
		nodes[node.ID] = node
	}
	nodeID := "ap-southeast-2::apigateway::abc123"
	node, ok := nodes[nodeID]
	if !ok {
		t.Fatalf("expected API Gateway node %q, got nodes: %v", nodeID, keys(nodes))
	}
	if node.RouteCount == nil || *node.RouteCount != 2 {
		t.Errorf("expected 2 routes, got %v", node.RouteCount)
	}
	if node.StageCount == nil || *node.StageCount != 1 {
		t.Errorf("expected 1 stage, got %v", node.StageCount)
	}
	if node.Region != "ap-southeast-2" {
		t.Errorf("expected region ap-southeast-2, got %q", node.Region)
	}
}

func TestBuildTopologyAPIGatewayRegionDiffersFromDefault(t *testing.T) {
	// Config region is us-east-1, but API is stored in ap-southeast-2.
	// The node should appear in ap-southeast-2, not the default region.
	restAPIPayload, _ := json.Marshal(map[string]any{
		"id":             "68fea4a56c",
		"name":           "api-l-ase2-web-push-service",
		"rootResourceId": "root1",
	})
	resourcePayload, _ := json.Marshal(map[string]any{
		"id":              "eb7380",
		"pathPart":        "messages",
		"resourceMethods": map[string]any{},
	})
	stagePayload, _ := json.Marshal(map[string]any{
		"stageName":    "prod",
		"deploymentId": "deploy1",
	})

	resp := buildTopology("us-east-1", map[string][]state.KV{
		tNsRestAPIs:     {{Key: "ap-southeast-2/68fea4a56c", Value: string(restAPIPayload)}},
		tNsAPIResources: {{Key: "ap-southeast-2/68fea4a56c/eb7380", Value: string(resourcePayload)}},
		tNsAPIStages:    {{Key: "ap-southeast-2/68fea4a56c/prod", Value: string(stagePayload)}},
	}, "")

	nodes := map[string]topology.Node{}
	for _, node := range resp.Nodes {
		nodes[node.ID] = node
	}
	nodeID := "ap-southeast-2::apigateway::68fea4a56c"
	node, ok := nodes[nodeID]
	if !ok {
		t.Fatalf("expected API Gateway node %q, got nodes: %v", nodeID, keys(nodes))
	}
	if node.Region != "ap-southeast-2" {
		t.Errorf("expected region ap-southeast-2, got %q", node.Region)
	}
	if node.RouteCount == nil || *node.RouteCount != 1 {
		t.Errorf("expected 1 route, got %v", node.RouteCount)
	}
	if node.StageCount == nil || *node.StageCount != 1 {
		t.Errorf("expected 1 stage, got %v", node.StageCount)
	}
}

func keys[V any](m map[string]V) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	return ks
}

func TestBuildTopology_ec2InstanceDecodesStoreShape(t *testing.T) {
	// Given: a VPC and one instance persisted in the EC2 store's PascalCase
	// shape, both under region-scoped keys.
	vpcPayload, _ := json.Marshal(map[string]any{
		"VpcId":     "vpc-0abc",
		"CidrBlock": "10.0.0.0/16",
		"State":     "available",
	})
	instancePayload, _ := json.Marshal(map[string]any{
		"InstanceId":   "i-0123456789abcdef0",
		"ImageId":      "ami-12345678",
		"InstanceType": "t3.micro",
		"State":        map[string]any{"Code": 16, "Name": "running"},
		"SubnetId":     "subnet-0def",
		"VpcId":        "vpc-0abc",
	})

	// When: topology is built from the EC2 namespaces.
	resp := buildTopology("us-east-1", map[string][]state.KV{
		tNsVPCs:      {{Key: "us-west-2/vpc-0abc", Value: string(vpcPayload)}},
		tNsInstances: {{Key: "us-west-2/i-0123456789abcdef0", Value: string(instancePayload)}},
	}, "")

	// Then: the instance appears as an ec2 node grouped inside its VPC.
	nodes := map[string]topology.Node{}
	for _, n := range resp.Nodes {
		nodes[n.ID] = n
	}
	inst, ok := nodes["us-west-2::ec2::i-0123456789abcdef0"]
	if !ok {
		t.Fatalf("expected an ec2 node for the instance, got nodes: %v", keys(nodes))
	}
	if inst.Service != "ec2" || inst.Label != "i-0123456789abcdef0" || inst.Region != "us-west-2" {
		t.Fatalf("unexpected ec2 node identity: %#v", inst)
	}
	if inst.VpcID != "vpc-0abc" {
		t.Errorf("ec2 node vpcId: got %q, want %q", inst.VpcID, "vpc-0abc")
	}
	if inst.Status != "running" {
		t.Errorf("ec2 node status: got %q, want %q", inst.Status, "running")
	}

	// And: a vpc-member edge links the VPC to the instance in the same region.
	wantEdge := "vpc-member::us-west-2::vpc::vpc-0abc→us-west-2::ec2::i-0123456789abcdef0"
	found := false
	for _, e := range resp.Edges {
		if e.ID == wantEdge {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected vpc-member edge %q, got edges: %v", wantEdge, resp.Edges)
	}
}

// pipeEdges returns the edges of type "pipe" keyed by ID.
func pipeEdges(resp topology.Response) map[string]topology.Edge {
	out := map[string]topology.Edge{}
	for _, e := range resp.Edges {
		if e.Type == "pipe" {
			out[e.ID] = e
		}
	}
	return out
}

// assertPipeEdge checks that exactly one pipe edge exists and that it carries
// the expected endpoints, label and state.
func assertPipeEdge(t *testing.T, resp topology.Response, wantID, wantSrc, wantTgt, wantLabel, wantState string) {
	t.Helper()
	edges := pipeEdges(resp)
	if len(edges) != 1 {
		t.Fatalf("expected exactly one pipe edge, got %d: %v", len(edges), keys(edges))
	}
	edge, ok := edges[wantID]
	if !ok {
		t.Fatalf("expected pipe edge %q, got: %v", wantID, keys(edges))
	}
	if edge.Source != wantSrc || edge.Target != wantTgt {
		t.Errorf("pipe edge endpoints: got %s → %s, want %s → %s", edge.Source, edge.Target, wantSrc, wantTgt)
	}
	if edge.Type != "pipe" {
		t.Errorf("pipe edge type: got %q, want %q", edge.Type, "pipe")
	}
	if edge.Label != wantLabel {
		t.Errorf("pipe edge label: got %q, want %q", edge.Label, wantLabel)
	}
	if edge.State != wantState {
		t.Errorf("pipe edge state: got %q, want %q", edge.State, wantState)
	}
}

func TestBuildTopology_pipeSQSToLambda(t *testing.T) {
	// Given: an SQS queue, a Lambda function, and a RUNNING pipe whose
	// Source and Target are their ARNs (as AWS::Pipes::Pipe stores them).
	pipePayload, _ := json.Marshal(map[string]any{
		"Name":         "orders-to-lambda",
		"Source":       "arn:aws:sqs:us-east-1:000000000000:orders",
		"Target":       "arn:aws:lambda:us-east-1:000000000000:function:process-orders",
		"CurrentState": "RUNNING",
	})

	// When: topology is built.
	resp := buildTopology("us-east-1", map[string][]state.KV{
		tNsPipes: {{Key: "us-east-1/orders-to-lambda", Value: string(pipePayload)}},
	}, "", otherNode("us-east-1", "sqs", "orders"), otherNode("us-east-1", "lambda", "process-orders"))

	// Then: a pipe edge links the queue node to the function node.
	assertPipeEdge(t, resp,
		"pipe::us-east-1::orders-to-lambda",
		"us-east-1::sqs::orders",
		"us-east-1::lambda::process-orders",
		"orders-to-lambda", "RUNNING")
}

func TestBuildTopology_pipeDynamoDBStreamToSQS(t *testing.T) {
	// Given: a stream-enabled DynamoDB table, an SQS queue, and a pipe whose
	// Source is the table's stream ARN.
	tablePayload, _ := json.Marshal(map[string]any{
		"TableName": "events",
		"TableArn":  "arn:aws:dynamodb:us-east-1:000000000000:table/events",
		"StreamSpecification": map[string]any{
			"StreamEnabled":  true,
			"StreamViewType": "NEW_AND_OLD_IMAGES",
		},
	})
	pipePayload, _ := json.Marshal(map[string]any{
		"Name":         "events-to-queue",
		"Source":       "arn:aws:dynamodb:us-east-1:000000000000:table/events/stream/2026-01-01T00:00:00.000",
		"Target":       "arn:aws:sqs:us-east-1:000000000000:event-fanout",
		"CurrentState": "STOPPED",
	})

	// When: topology is built.
	resp := buildTopology("us-east-1", map[string][]state.KV{
		tNsTables: {{Key: "us-east-1/events", Value: string(tablePayload)}},
		tNsPipes:  {{Key: "us-east-1/events-to-queue", Value: string(pipePayload)}},
	}, "", otherNode("us-east-1", "sqs", "event-fanout"))

	// Then: a pipe edge links the table node to the queue node, carrying the
	// pipe's (non-RUNNING) state.
	assertPipeEdge(t, resp,
		"pipe::us-east-1::events-to-queue",
		"us-east-1::dynamodb::events",
		"us-east-1::sqs::event-fanout",
		"events-to-queue", "STOPPED")
}

func TestBuildTopology_pipeSQSToSNS(t *testing.T) {
	// Given: an SQS queue, an SNS topic, and a pipe from the queue to the topic.
	topicPayload, _ := json.Marshal(map[string]any{
		"name": "broadcast",
		"arn":  "arn:aws:sns:eu-west-1:000000000000:broadcast",
	})
	pipePayload, _ := json.Marshal(map[string]any{
		"Name":         "inbound-to-broadcast",
		"Source":       "arn:aws:sqs:eu-west-1:000000000000:inbound",
		"Target":       "arn:aws:sns:eu-west-1:000000000000:broadcast",
		"CurrentState": "RUNNING",
	})

	// When: topology is built with a default region that differs from the
	// resources' region, so endpoints must come from the ARNs.
	resp := buildTopology("us-east-1", map[string][]state.KV{
		tNsTopics: {{Key: "eu-west-1/broadcast", Value: string(topicPayload)}},
		tNsPipes:  {{Key: "eu-west-1/inbound-to-broadcast", Value: string(pipePayload)}},
	}, "", otherNode("eu-west-1", "sqs", "inbound"))

	// Then: a pipe edge links the queue node to the topic node in eu-west-1.
	assertPipeEdge(t, resp,
		"pipe::eu-west-1::inbound-to-broadcast",
		"eu-west-1::sqs::inbound",
		"eu-west-1::sns::broadcast",
		"inbound-to-broadcast", "RUNNING")
}

func TestBuildTopology_pipeLegacyRecordWithoutARNs(t *testing.T) {
	// Given: a DynamoDB table, an SQS queue, and a legacy pipe record that
	// carries only SourceName/TargetName and no ARNs.
	tablePayload, _ := json.Marshal(map[string]any{
		"TableName": "legacy-table",
		"TableArn":  "arn:aws:dynamodb:us-east-1:000000000000:table/legacy-table",
	})
	pipePayload, _ := json.Marshal(map[string]any{
		"Name":         "legacy-pipe",
		"SourceName":   "legacy-table",
		"TargetName":   "legacy-queue",
		"CurrentState": "RUNNING",
	})

	// When: topology is built.
	resp := buildTopology("us-east-1", map[string][]state.KV{
		tNsTables: {{Key: "us-east-1/legacy-table", Value: string(tablePayload)}},
		tNsPipes:  {{Key: "us-east-1/legacy-pipe", Value: string(pipePayload)}},
	}, "", otherNode("us-east-1", "sqs", "legacy-queue"))

	// Then: the DynamoDB → SQS fallback still produces the edge.
	assertPipeEdge(t, resp,
		"pipe::us-east-1::legacy-pipe",
		"us-east-1::dynamodb::legacy-table",
		"us-east-1::sqs::legacy-queue",
		"legacy-pipe", "RUNNING")
}

func TestBuildTopology_cloudFrontDistributionWithS3Origin(t *testing.T) {
	// Given: a bucket, and a distribution fronting it stored under the
	// region-scoped key the CloudFront store writes ("<region>/dist:<id>").
	bucketPayload, _ := json.Marshal(map[string]any{"name": "site", "region": "us-west-2"})
	distPayload, _ := json.Marshal(map[string]any{
		"id":          "E1234567890ABC",
		"status":      "Deployed",
		"domain_name": "d111111abcdef8.cloudfront.net",
		"distribution_config": map[string]any{
			"comment": "website",
			"origins": map[string]any{
				"quantity": 1,
				"items":    []map[string]any{{"id": "s3", "domain_name": "site.s3.amazonaws.com"}},
			},
		},
	})

	// When: topology is built.
	resp := buildTopology("us-east-1", map[string][]state.KV{
		tNsBuckets:         {{Key: "site", Value: string(bucketPayload)}},
		tNsCFDistributions: {{Key: "us-east-1/dist:E1234567890ABC", Value: string(distPayload)}},
	}, "")

	// Then: the distribution is a node, linked to the bucket in its own region.
	var dist *topology.Node
	for i := range resp.Nodes {
		if resp.Nodes[i].ID == "us-east-1::cloudfront::E1234567890ABC" {
			dist = &resp.Nodes[i]
		}
	}
	if dist == nil || dist.Label != "website" || dist.OriginCount == nil || *dist.OriginCount != 1 {
		t.Fatalf("expected a CloudFront node, got %+v", resp.Nodes)
	}
	if len(resp.Edges) != 1 || resp.Edges[0].Target != "us-west-2::s3::site" || resp.Edges[0].Type != "origin" {
		t.Fatalf("expected an S3 origin edge to the bucket, got %+v", resp.Edges)
	}
}
