package router

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/overcast-sh/overcast/internal/config"
	"github.com/overcast-sh/overcast/internal/state"
)

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
	resp := buildTopology(&config.Config{Region: "us-east-1"}, map[string][]state.KV{
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

func TestBuildTopologyESMEdgesFromLambdaESMNamespace(t *testing.T) {
	// Given: an SQS queue, a Lambda function, and an ESM linking them.
	queuePayload, _ := json.Marshal(map[string]any{
		"name": "my-queue",
		"arn":  "arn:aws:sqs:us-east-1:000000000000:my-queue",
	})
	functionPayload, _ := json.Marshal(map[string]any{
		"name": "my-function",
		"arn":  "arn:aws:lambda:us-east-1:000000000000:function:my-function",
	})
	esmPayload, _ := json.Marshal(map[string]any{
		"FunctionArn":    "arn:aws:lambda:us-east-1:000000000000:function:my-function",
		"EventSourceArn": "arn:aws:sqs:us-east-1:000000000000:my-queue",
	})

	resp := buildTopology(&config.Config{Region: "us-east-1"}, map[string][]state.KV{
		tNsQueues:    {{Key: "us-east-1/my-queue", Value: string(queuePayload)}},
		tNsFunctions: {{Key: "us-east-1/my-function", Value: string(functionPayload)}},
		tNsESM:       {{Key: "us-east-1/uuid-1", Value: string(esmPayload)}},
	}, "")

	edges := map[string]topologyEdge{}
	for _, edge := range resp.Edges {
		edges[edge.ID] = edge
	}

	wantID := "esm::us-east-1::sqs::my-queue→us-east-1::lambda::my-function"
	if _, ok := edges[wantID]; !ok {
		t.Fatalf("expected SQS → Lambda ESM edge (%s), got edges: %v", wantID, resp.Edges)
	}
	if got := edges[wantID].Type; got != "esm" {
		t.Errorf("edge type: got %q, want %q", got, "esm")
	}
}

func TestBuildTopologyDynamoDBESMFilterNode(t *testing.T) {
	// Given: a stream-enabled DynamoDB table, a Lambda function, and a filtered ESM.
	tablePayload, _ := json.Marshal(map[string]any{
		"TableName": "items",
		"TableArn":  "arn:aws:dynamodb:us-east-1:000000000000:table/items",
		"StreamSpecification": map[string]any{
			"StreamEnabled": true,
		},
	})
	functionPayload, _ := json.Marshal(map[string]any{
		"name": "stream-fn",
		"arn":  "arn:aws:lambda:us-east-1:000000000000:function:stream-fn",
	})
	esmPayload, _ := json.Marshal(map[string]any{
		"UUID":           "esm-1",
		"FunctionArn":    "arn:aws:lambda:us-east-1:000000000000:function:stream-fn",
		"EventSourceArn": "arn:aws:dynamodb:us-east-1:000000000000:table/items/stream/2026-01-01T00:00:00.000",
		"FilterCriteria": map[string]any{
			"Filters": []map[string]any{{"Pattern": `{"eventName":["INSERT"]}`}},
		},
	})

	// When: topology is built.
	resp := buildTopology(&config.Config{Region: "us-east-1"}, map[string][]state.KV{
		tNsTables:    {{Key: "us-east-1/items", Value: string(tablePayload)}},
		tNsFunctions: {{Key: "us-east-1/stream-fn", Value: string(functionPayload)}},
		tNsESM:       {{Key: "us-east-1/esm-1", Value: string(esmPayload)}},
	}, "")

	// Then: the filter is visible as a node between DynamoDB and Lambda.
	nodes := map[string]topologyNode{}
	for _, node := range resp.Nodes {
		nodes[node.ID] = node
	}
	filterID := "us-east-1::esm-filter::esm-1"
	filter, ok := nodes[filterID]
	if !ok {
		t.Fatalf("expected ESM filter node %s, got nodes: %v", filterID, resp.Nodes)
	}
	if filter.Service != "esm-filter" || filter.ESMID != "esm-1" || len(filter.FilterPatterns) != 1 {
		t.Fatalf("unexpected filter node: %#v", filter)
	}

	edges := map[string]topologyEdge{}
	for _, edge := range resp.Edges {
		edges[edge.ID] = edge
	}
	if edge := edges["esm-filter-in::esm-1"]; edge.Source != "us-east-1::dynamodb::items" || edge.Target != filterID {
		t.Fatalf("unexpected filter input edge: %#v", edge)
	}
	if edge := edges["esm-filter-out::esm-1"]; edge.Source != filterID || edge.Target != "us-east-1::lambda::stream-fn" {
		t.Fatalf("unexpected filter output edge: %#v", edge)
	}
}

func TestBuildTopologyAddsECREdgesForLambdaAndECSConsumers(t *testing.T) {
	repoPayload, _ := json.Marshal(map[string]any{
		"repositoryArn":  "arn:aws:ecr:us-east-1:000000000000:repository/sample-app",
		"repositoryName": "sample-app",
		"repositoryUri":  "localhost:5000/000000000000/sample-app",
	})
	lambdaPayload, _ := json.Marshal(map[string]any{
		"name":         "image-fn",
		"arn":          "arn:aws:lambda:us-east-1:000000000000:function:image-fn",
		"package_type": "Image",
		"image_uri":    "localhost:5000/000000000000/sample-app:latest",
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

	resp := buildTopology(&config.Config{Region: "us-east-1"}, map[string][]state.KV{
		tNsECRRepos:    {{Key: "us-east-1/sample-app", Value: string(repoPayload)}},
		tNsFunctions:   {{Key: "us-east-1/image-fn", Value: string(lambdaPayload)}},
		tNsECSServices: {{Key: "us-east-1/web", Value: string(ecsServicePayload)}},
		tNsECSTaskDefs: {{Key: "us-east-1/demo:3", Value: string(taskDefPayload)}},
	}, "")

	nodes := map[string]topologyNode{}
	for _, node := range resp.Nodes {
		nodes[node.ID] = node
	}
	if _, ok := nodes["us-east-1::ecr::sample-app"]; !ok {
		t.Fatalf("expected ECR repository node, got %#v", resp.Nodes)
	}
	if got := nodes["us-east-1::ecr::sample-app"].RepositoryUri; got != "localhost:5000/000000000000/sample-app" {
		t.Fatalf("expected repositoryUri to propagate to topology node, got %q", got)
	}

	edges := map[string]topologyEdge{}
	for _, edge := range resp.Edges {
		edges[edge.ID] = edge
	}
	if _, ok := edges["ecr-lambda::us-east-1::ecr::sample-app→us-east-1::lambda::image-fn"]; !ok {
		t.Fatalf("expected ECR → Lambda edge, got %#v", resp.Edges)
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
	resp := buildTopology(&config.Config{Region: "us-east-1"}, map[string][]state.KV{
		tNsClusters:    {{Key: "us-east-1/demo", Value: string(clusterPayload)}},
		tNsECSServices: {{Key: "us-east-1/demo/web", Value: string(servicePayload)}},
		tNsECSTasks: {
			{Key: "us-east-1/demo/running-task", Value: string(runningTaskPayload)},
			{Key: "us-east-1/demo/stopped-task", Value: string(stoppedTaskPayload)},
		},
	}, "")

	// Then: only the current task is shown, and every edge points at the real cluster/service nodes.
	nodes := map[string]topologyNode{}
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

	edges := map[string]topologyEdge{}
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
	resp := buildTopology(&config.Config{Region: "us-east-1"}, map[string][]state.KV{
		tNsClusters:    {{Key: "ap-southeast-2/demo", Value: string(clusterPayload)}},
		tNsECSServices: {{Key: "ap-southeast-2/demo/web", Value: string(servicePayload)}},
		tNsECSTasks:    {{Key: "ap-southeast-2/demo/task-1", Value: string(taskPayload)}},
	}, "")

	// Then: service and task navigation stay in the owning cluster's region.
	nodes := map[string]topologyNode{}
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
	lambdaPayload, _ := json.Marshal(map[string]any{
		"name":         "image-fn",
		"arn":          "arn:aws:lambda:us-east-1:000000000000:function:image-fn",
		"package_type": "Image",
		"image_uri":    "localhost:5000/000000000000/sample-app@sha256:deadbeef",
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

	resp := buildTopology(&config.Config{Region: "us-east-1"}, map[string][]state.KV{
		tNsECRRepos:    {{Key: "us-east-1/sample-app", Value: string(repoPayload)}},
		tNsFunctions:   {{Key: "us-east-1/image-fn", Value: string(lambdaPayload)}},
		tNsECSServices: {{Key: "us-east-1/web", Value: string(ecsServicePayload)}},
		tNsECSTaskDefs: {{Key: "us-east-1/demo:3", Value: string(taskDefPayload)}},
	}, "")

	edges := map[string]topologyEdge{}
	for _, edge := range resp.Edges {
		edges[edge.ID] = edge
	}
	if _, ok := edges["ecr-lambda::us-east-1::ecr::sample-app→us-east-1::lambda::image-fn"]; !ok {
		t.Fatalf("expected normalized ECR → Lambda edge, got %#v", resp.Edges)
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

	resp := buildTopology(&config.Config{Region: "us-east-1"}, map[string][]state.KV{
		tNsCacheClusters:          {{Key: "us-east-1/session-cache", Value: string(clusterPayload)}},
		tNsServerlessCaches:       {{Key: "us-west-2/api-cache", Value: string(serverlessPayload)}},
		tNsCacheReplicationGroups: {{Key: "eu-west-1/checkout-rg", Value: string(replicationPayload)}},
	}, "")

	nodes := map[string]topologyNode{}
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

func TestCfnResourceNodeIDMapsNonDefaultTypes(t *testing.T) {
	tests := []struct {
		resType    string
		physicalID string
		want       string
	}{
		{"AWS::ApiGateway::RestApi", "abc123", "us-east-1::apigateway::abc123"},
		{"AWS::ApiGatewayV2::Api", "def456", "us-east-1::apigateway::def456"},
		{"AWS::ApiGateway::Resource", "abc123/res1", ""},
		{"AWS::ApiGateway::Method", "abc123/res1/GET", ""},
		{"AWS::Cognito::UserPool", "us-east-1_A1B2C3D4", "us-east-1::cognito::us-east-1_A1B2C3D4"},
		{"AWS::AppSync::GraphQLApi", "abc123def456", "us-east-1::appsync::abc123def456"},
		{"AWS::CloudFront::Distribution", "E1234567890ABC", "us-east-1::cloudfront::E1234567890ABC"},
		{"AWS::WAFv2::WebACL", "REGIONAL/acl-123", "us-east-1::waf::acl-123"},
		{"AWS::ElastiCache::CacheCluster", "cache-1", "us-east-1::elasticache::cache-1"},
		{"AWS::ElastiCache::ServerlessCache", "cache-2", "us-east-1::elasticache::cache-2"},
		{"AWS::ElastiCache::ReplicationGroup", "rg-1", "us-east-1::elasticache::rg-1"},
	}
	for _, tt := range tests {
		got := cfnResourceNodeID(tCFNResource{Type: tt.resType, PhysicalID: tt.physicalID}, "us-east-1")
		if got != tt.want {
			t.Errorf("cfnResourceNodeID(%s, %s) = %q, want %q", tt.resType, tt.physicalID, got, tt.want)
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

	resp := buildTopology(&config.Config{Region: "ap-southeast-2"}, map[string][]state.KV{
		tNsRestAPIs: {{Key: "ap-southeast-2/abc123", Value: string(restAPIPayload)}},
		tNsAPIResources: {
			{Key: "ap-southeast-2/abc123/res1", Value: string(resourcePayload)},
			{Key: "ap-southeast-2/abc123/res2", Value: string(resource2Payload)},
		},
		tNsAPIStages: {{Key: "ap-southeast-2/abc123/prod", Value: string(stagePayload)}},
	}, "")

	nodes := map[string]topologyNode{}
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

	resp := buildTopology(&config.Config{Region: "us-east-1"}, map[string][]state.KV{
		tNsRestAPIs:     {{Key: "ap-southeast-2/68fea4a56c", Value: string(restAPIPayload)}},
		tNsAPIResources: {{Key: "ap-southeast-2/68fea4a56c/eb7380", Value: string(resourcePayload)}},
		tNsAPIStages:    {{Key: "ap-southeast-2/68fea4a56c/prod", Value: string(stagePayload)}},
	}, "")

	nodes := map[string]topologyNode{}
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

func TestBuildTopologyESMEdgeRegionMismatch(t *testing.T) {
	// Given: nodes in ap-southeast-2, but ESM has ARNs with us-east-1 (stale
	// data or cross-stack import from a different region).  The topology
	// should still create the edges by falling back to name-based matching.

	queuePayload, _ := json.Marshal(map[string]any{
		"name": "my-queue",
		"arn":  "arn:aws:sqs:ap-southeast-2:000000000000:my-queue",
	})
	functionPayload, _ := json.Marshal(map[string]any{
		"name": "my-function",
		"arn":  "arn:aws:lambda:ap-southeast-2:000000000000:function:my-function",
	})
	tablePayload, _ := json.Marshal(map[string]any{
		"TableName": "my-table",
		"TableArn":  "arn:aws:dynamodb:ap-southeast-2:000000000000:table/my-table",
		"StreamSpecification": map[string]any{
			"StreamEnabled":  true,
			"StreamViewType": "NEW_AND_OLD_IMAGES",
		},
		"LatestStreamArn": "arn:aws:dynamodb:ap-southeast-2:000000000000:table/my-table/stream/2026-01-01T00:00:00.000",
	})
	// ESM ARNs reference us-east-1 (wrong region)
	sqsESM, _ := json.Marshal(map[string]any{
		"FunctionArn":    "arn:aws:lambda:us-east-1:000000000000:function:my-function",
		"EventSourceArn": "arn:aws:sqs:us-east-1:000000000000:my-queue",
	})
	ddbESM, _ := json.Marshal(map[string]any{
		"FunctionArn":    "arn:aws:lambda:us-east-1:000000000000:function:my-function",
		"EventSourceArn": "arn:aws:dynamodb:us-east-1:000000000000:table/my-table/stream/2026-01-01T00:00:00.000",
	})

	resp := buildTopology(&config.Config{Region: "us-east-1"}, map[string][]state.KV{
		tNsQueues:    {{Key: "ap-southeast-2/my-queue", Value: string(queuePayload)}},
		tNsFunctions: {{Key: "ap-southeast-2/my-function", Value: string(functionPayload)}},
		tNsTables:    {{Key: "ap-southeast-2/my-table", Value: string(tablePayload)}},
		tNsESM: {
			{Key: "ap-southeast-2/esm-sqs", Value: string(sqsESM)},
			{Key: "ap-southeast-2/esm-ddb", Value: string(ddbESM)},
		},
	}, "")

	esmEdges := map[string]topologyEdge{}
	for _, e := range resp.Edges {
		if e.Type == "esm" {
			esmEdges[e.ID] = e
		}
	}

	if len(esmEdges) != 2 {
		t.Fatalf("expected 2 ESM edges, got %d: %v", len(esmEdges), esmEdges)
	}

	// Verify SQS → Lambda ESM edge (resolved to ap-southeast-2 nodes)
	sqsEdgeID := "esm::ap-southeast-2::sqs::my-queue→ap-southeast-2::lambda::my-function"
	if _, ok := esmEdges[sqsEdgeID]; !ok {
		t.Errorf("missing SQS ESM edge; want %s, got: %v", sqsEdgeID, keys(esmEdges))
	}

	// Verify DynamoDB → Lambda ESM edge (resolved to ap-southeast-2 nodes)
	ddbEdgeID := "esm::ap-southeast-2::dynamodb::my-table→ap-southeast-2::lambda::my-function"
	if _, ok := esmEdges[ddbEdgeID]; !ok {
		t.Errorf("missing DynamoDB ESM edge; want %s, got: %v", ddbEdgeID, keys(esmEdges))
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
	resp := buildTopology(&config.Config{Region: "us-east-1"}, map[string][]state.KV{
		tNsVPCs:      {{Key: "us-west-2/vpc-0abc", Value: string(vpcPayload)}},
		tNsInstances: {{Key: "us-west-2/i-0123456789abcdef0", Value: string(instancePayload)}},
	}, "")

	// Then: the instance appears as an ec2 node grouped inside its VPC.
	nodes := map[string]topologyNode{}
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
