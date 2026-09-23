package router

// topology.go — internal topology API for the system map.
//
// GET /_overcast/topology — returns every resource and connection across all regions
// in a single, fast response.
//
// Optional query parameter:
//   ?region=us-east-1   — return only resources whose region matches.
//                         Omit to get all resources across all regions.
//
// Services that implement topology.Contributor put their own resources on the
// map (see internal/topology). legacyTopology covers the services not yet
// migrated to a contributor, reading their state directly from the store; it
// shrinks as each service moves (#2090).

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"

	"go.uber.org/zap"

	"github.com/overcast-sh/overcast/internal/config"
	"github.com/overcast-sh/overcast/internal/serviceutil"
	"github.com/overcast-sh/overcast/internal/state"
	"github.com/overcast-sh/overcast/internal/topology"
)

// ── Handler ────────────────────────────────────────────────────────────────

// newTopologyHandler runs every contributor concurrently, each into its own
// graph, and merges the graphs in contributor order. A contributor that fails
// is logged and left off the map rather than failing the whole response.
func newTopologyHandler(cfg *config.Config, store state.Store, contributors []topology.Contributor, logger *zap.Logger) http.HandlerFunc {
	contributors = append([]topology.Contributor{legacyTopology{cfg: cfg, store: store}}, contributors...)
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		graphs := make([]*topology.Graph, len(contributors))
		var wg sync.WaitGroup
		for i, c := range contributors {
			wg.Go(func() {
				g := &topology.Graph{}
				if err := c.ContributeTopology(ctx, g); err != nil {
					logger.Warn("topology contributor failed", zap.String("contributor", contributorName(c)), zap.Error(err))
					return
				}
				graphs[i] = g
			})
		}
		wg.Wait()

		kept := graphs[:0]
		for _, g := range graphs {
			if g != nil {
				kept = append(kept, g)
			}
		}
		resp := topology.Build(r.URL.Query().Get("region"), kept...) // "" = all regions

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}
}

func contributorName(c topology.Contributor) string {
	if svc, ok := c.(Service); ok {
		return svc.Name()
	}
	return "legacy"
}

// ── Legacy contributor ─────────────────────────────────────────────────────

// legacyTopology contributes the services that do not yet implement
// topology.Contributor. It scans their namespaces in parallel and decodes
// them into the lightweight structs below.
type legacyTopology struct {
	cfg   *config.Config
	store state.Store
}

// State store namespaces, mirrored from the service packages not yet
// migrated. Each is deleted when its service implements topology.Contributor
// and reads its own namespace.
const (
	tNsBuckets       = "s3:buckets"
	tNsNotifications = "s3:notifications"
	tNsTopics        = "sns:topics"
	tNsSubscriptions = "sns:subscriptions"
	tNsTables        = "dynamodb:tables"
	tNsLogGroups     = "logs:groups"
	tNsPipes         = "pipes:pipes"
	tNsCFNStacks     = "cfn:stacks"

	// EC2 resource tracking.
	tNsInstances        = "ec2:instances"
	tNsVPCs             = "ec2:vpcs"
	tNsSubnets          = "ec2:subnets"
	tNsInternetGateways = "ec2:internet-gateways"

	// ECS resource tracking.
	tNsClusters    = "ecs:clusters"
	tNsECSTaskDefs = "ecs:task-definitions"
	tNsECSTasks    = "ecs:tasks"
	tNsECSServices = "ecs:services"

	// ECR resource tracking.
	tNsECRRepos = "ecr:repositories"

	// RDS resource tracking.
	tNsDBInstances = "rds:instances"

	// ElastiCache resource tracking.
	tNsCacheClusters          = "elasticache:clusters"
	tNsCacheReplicationGroups = "elasticache:replication-groups"
	tNsServerlessCaches       = "elasticache:serverless-caches"

	// EFS resource tracking.
	tNsEFSFileSystems  = "efs:filesystems"
	tNsEFSAccessPoints = "efs:accesspoints"

	// MSK resource tracking.
	tNsMSKClusters = "msk:clusters"

	// API Gateway resource tracking.
	tNsRestAPIs     = "apigw:restapis"
	tNsAPIResources = "apigw:resources"
	tNsAPIStages    = "apigw:stages"
	tNsV2APIs       = "apigw:v2apis"
	tNsV2Routes     = "apigw:v2routes"
	tNsV2Integ      = "apigw:v2integrations"
	tNsV2Stages     = "apigw:v2stages"

	// CloudFront resource tracking.
	tNsCFDistributions = "cloudfront"

	// WAFv2 resource tracking.
	tNsWAFWebACLs = "waf:webacls"

	// AppSync resource tracking.
	tNsAppSync = "appsync"

	// Cognito resource tracking.
	tNsCognitoPools = "cognito:pools"
)

var legacyTopologyNamespaces = []string{
	tNsBuckets, tNsNotifications,
	tNsTopics, tNsSubscriptions, tNsTables,
	tNsLogGroups, tNsPipes, tNsCFNStacks,
	tNsInstances, tNsVPCs, tNsSubnets, tNsInternetGateways,
	tNsClusters, tNsECSTaskDefs, tNsECSTasks, tNsECSServices,
	tNsECRRepos,
	tNsDBInstances,
	tNsCacheClusters, tNsCacheReplicationGroups, tNsServerlessCaches,
	tNsRestAPIs, tNsAPIResources, tNsAPIStages,
	tNsV2APIs, tNsV2Routes, tNsV2Integ, tNsV2Stages,
	tNsCFDistributions,
	tNsWAFWebACLs,
	tNsAppSync,
	tNsCognitoPools,
	tNsMSKClusters,
	tNsEFSFileSystems, tNsEFSAccessPoints,
}

// ContributeTopology implements topology.Contributor. A namespace whose scan
// fails is treated as empty.
func (l legacyTopology) ContributeTopology(ctx context.Context, g *topology.Graph) error {
	results := make([][]state.KV, len(legacyTopologyNamespaces))
	var wg sync.WaitGroup
	for i, ns := range legacyTopologyNamespaces {
		wg.Go(func() {
			if kvs, err := l.store.Scan(ctx, ns, ""); err == nil {
				results[i] = kvs
			}
		})
	}
	wg.Wait()

	byNS := make(map[string][]state.KV, len(legacyTopologyNamespaces))
	for i, ns := range legacyTopologyNamespaces {
		if results[i] != nil {
			byNS[ns] = results[i]
		}
	}
	contributeLegacyTopology(l.cfg.Region, byNS, g)
	return nil
}

// ── Lightweight decode structs ─────────────────────────────────────────────
// Only the fields the topology needs — keeps allocation small and decoupled
// from the full domain types.

type tBucket struct {
	Name   string `json:"name"`
	Region string `json:"region"`
}

type tNotificationConfig struct {
	QueueConfigurations  []tNotifQueue  `json:"queue_configurations,omitempty"`
	LambdaConfigurations []tNotifLambda `json:"lambda_configurations,omitempty"`
	// TODO(priority:P3): TopicConfigurations for S3 → SNS notification edges.
}
type tNotifQueue struct {
	ARN string `json:"arn"`
}
type tNotifLambda struct {
	ARN string `json:"arn"`
}

type tTopic struct {
	Name string `json:"name"`
	ARN  string `json:"arn"`
}
type tSubscription struct {
	TopicName string `json:"topic_name"`
	Protocol  string `json:"protocol"`
	Endpoint  string `json:"endpoint"`
	QueueName string `json:"queue_name,omitempty"`
}

type tWAFWebACL struct {
	ID    string `json:"Id"`
	Name  string `json:"Name"`
	Scope string `json:"Scope"`
	ARN   string `json:"ARN"`
	Rules []any  `json:"Rules"`
}

type tTable struct {
	TableName           string       `json:"TableName"`
	TableARN            string       `json:"TableArn"`
	StreamSpecification *tStreamSpec `json:"StreamSpecification,omitempty"`
}
type tStreamSpec struct {
	StreamEnabled bool `json:"StreamEnabled"`
}

type tLogGroup struct {
	Name string `json:"name"`
	ARN  string `json:"arn"`
}

type tPipe struct {
	Name         string `json:"Name"`
	SourceArn    string `json:"Source"`
	TargetArn    string `json:"Target"`
	SourceName   string `json:"SourceName"`
	TargetName   string `json:"TargetName"`
	CurrentState string `json:"CurrentState"`
}

// EC2 instances. Mirrors the PascalCase shape the EC2 store persists
// (ec2.Instance); the region is not in the record but in the region-scoped
// key ("us-east-1/i-abc").
type tInstance struct {
	InstanceID   string `json:"InstanceId"`
	InstanceType string `json:"InstanceType"`
	State        struct {
		Name string `json:"Name"` // pending, running, stopped, terminated, etc.
	} `json:"State"`
	VpcID    string `json:"VpcId"`
	SubnetID string `json:"SubnetId"`
}

// EC2 VPCs.
type tVPC struct {
	VpcID     string `json:"VpcId"`
	CidrBlock string `json:"CidrBlock"`
	State     string `json:"State"`
}

// EC2 Subnets.
type tSubnet struct {
	SubnetID string `json:"SubnetId"`
	VpcID    string `json:"VpcId"`
}

// EC2 Internet Gateways.
type tIGW struct {
	InternetGatewayID string           `json:"InternetGatewayId"`
	Attachments       []tIGWAttachment `json:"Attachments,omitempty"`
}
type tIGWAttachment struct {
	VpcID string `json:"VpcId"`
	State string `json:"State"`
}

// ECS resources.
type tCluster struct {
	Name   string `json:"clusterName"`
	ARN    string `json:"clusterArn"`
	Status string `json:"status"`
}
type tECSService struct {
	Name           string `json:"serviceName"`
	ARN            string `json:"serviceArn"`
	ClusterARN     string `json:"clusterArn"`
	TaskDefinition string `json:"taskDefinition"` // ARN of the task definition
	DesiredCount   int    `json:"desiredCount"`
	RunningCount   int    `json:"runningCount"`
	Status         string `json:"status"`
}
type tECSTaskDefinition struct {
	TaskDefinitionArn    string `json:"taskDefinitionArn"`
	ContainerDefinitions []struct {
		Image string `json:"image"`
	} `json:"containerDefinitions"`
}
type tECSTask struct {
	TaskARN        string `json:"taskArn"`
	ClusterARN     string `json:"clusterArn"`
	TaskDefinition string `json:"taskDefinitionArn"` // ARN of the task definition
	LastStatus     string `json:"lastStatus"`
	DesiredStatus  string `json:"desiredStatus"`
	Group          string `json:"group"` // "service:<name>" for service tasks
}

// RDS resources.
type tDBInstance struct {
	ID     string `json:"DBInstanceIdentifier"`
	Engine string `json:"Engine"`
	Status string `json:"DBInstanceStatus"`
}

// ElastiCache resources.
type tCacheCluster struct {
	ID     string `json:"CacheClusterId"`
	Engine string `json:"Engine"`
	Status string `json:"CacheClusterStatus"`
}
type tCacheReplicationGroup struct {
	ID     string `json:"ReplicationGroupId"`
	Engine string `json:"Engine"`
	Status string `json:"Status"`
}
type tServerlessCache struct {
	Name   string `json:"ServerlessCacheName"`
	Engine string `json:"Engine"`
	Status string `json:"Status"`
}

// EFS resources.
type tEFSFileSystem struct {
	ID     string `json:"FileSystemId"`
	Status string `json:"LifeCycleState"`
}
type tEFSAccessPoint struct {
	ID           string `json:"AccessPointId"`
	FileSystemID string `json:"FileSystemId"`
	Status       string `json:"LifeCycleState"`
}

// MSK resources.
type tMSKCluster struct {
	ClusterArn  string `json:"clusterArn"`
	ClusterName string `json:"clusterName"`
	State       string `json:"state"`
}

// API Gateway REST v1.
type tRestAPI struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}
type tAPIResource struct {
	ID              string                   `json:"id"`
	ResourceMethods map[string]*tMethodBrief `json:"resourceMethods,omitempty"`
}
type tMethodBrief struct {
	MethodIntegration *tIntegrationBrief `json:"methodIntegration,omitempty"`
}
type tIntegrationBrief struct {
	Type string `json:"type"` // AWS_PROXY, HTTP_PROXY, MOCK, AWS, HTTP
	URI  string `json:"uri,omitempty"`
}

// API Gateway HTTP v2.
type tAPIV2 struct {
	ApiID        string `json:"apiId"`
	Name         string `json:"name"`
	ProtocolType string `json:"protocolType"` // HTTP, WEBSOCKET
}

// CloudFront distribution.
type tCFDistribution struct {
	ID         string `json:"id"`
	Status     string `json:"status"`
	DomainName string `json:"domain_name"`
	Config     struct {
		Comment string `json:"comment"`
		Enabled bool   `json:"enabled"`
		Origins struct {
			Quantity int `json:"quantity"`
			Items    []struct {
				ID         string `json:"id"`
				DomainName string `json:"domain_name"`
			} `json:"items"`
		} `json:"origins"`
	} `json:"distribution_config"`
}
type tRouteV2 struct {
	RouteID  string `json:"routeId"`
	RouteKey string `json:"routeKey"`
	Target   string `json:"target,omitempty"`
}
type tIntegrationV2 struct {
	IntegrationID   string `json:"integrationId"`
	IntegrationType string `json:"integrationType"`
	IntegrationURI  string `json:"integrationUri,omitempty"`
}
type tCFNStack struct {
	StackName     string         `json:"StackName"`
	StackID       string         `json:"StackId"`
	ParentStackID string         `json:"ParentId,omitempty"`
	Status        string         `json:"StackStatus"`
	Region        string         `json:"Region,omitempty"`
	TemplateBody  string         `json:"TemplateBody,omitempty"`
	Resources     []tCFNResource `json:"Resources,omitempty"`
	Outputs       []tCFNOutput   `json:"Outputs,omitempty"`
}
type tCFNResource struct {
	LogicalID  string `json:"LogicalResourceId"`
	PhysicalID string `json:"PhysicalResourceId,omitempty"`
	Type       string `json:"ResourceType"`
}
type tCFNOutput struct {
	Key        string `json:"OutputKey"`
	Value      string `json:"OutputValue"`
	ExportName string `json:"ExportName,omitempty"`
}
type tAppSyncAPI struct {
	ApiId              string `json:"apiId"`
	Name               string `json:"name"`
	AuthenticationType string `json:"authenticationType"`
}

// Cognito user pool.
type tCognitoPool struct {
	ID   string `json:"Id"`
	Name string `json:"Name"`
	ARN  string `json:"Arn"`
}
type tECRRepository struct {
	RepositoryArn  string `json:"repositoryArn"`
	RepositoryName string `json:"repositoryName"`
	RepositoryURI  string `json:"repositoryUri"`
}
type tAppSyncDataSource struct {
	Name           string          `json:"name"`
	Type           string          `json:"type"`
	LambdaConfig   json.RawMessage `json:"lambdaConfig,omitempty"`
	DynamodbConfig json.RawMessage `json:"dynamodbConfig,omitempty"`
}

// contributeLegacyTopology writes the unmigrated services' nodes and edges
// from raw state store data. A pure function over byNS, so tests can seed it
// directly.
func contributeLegacyTopology(defaultRegion string, byNS map[string][]state.KV, g *topology.Graph) {
	addNode := func(n topology.Node, aliases ...topology.Ref) { g.AddNode(n, aliases...) }
	addEdge := func(src, tgt topology.Ref, idPrefix, typ string) {
		g.AddLink(topology.Link{Source: src, Target: tgt, IDPrefix: idPrefix, Type: typ})
	}

	// S3 buckets
	for _, kv := range byNS[tNsBuckets] {
		var b tBucket
		if json.Unmarshal([]byte(kv.Value), &b) != nil {
			continue
		}
		if b.Region == "" {
			b.Region = defaultRegion
		}
		addNode(topology.Node{
			ID:      topology.NodeID(b.Region, "s3", b.Name),
			Service: "s3",
			Label:   b.Name,
			Region:  b.Region,
		})
	}

	// SNS topics
	topicIndex := make(map[string]string) // topic name → region
	for _, kv := range byNS[tNsTopics] {
		var t tTopic
		if json.Unmarshal([]byte(kv.Value), &t) != nil {
			continue
		}
		region := regionFromARN(t.ARN, defaultRegion)
		topicIndex[t.Name] = region
		addNode(topology.Node{
			ID:      topology.NodeID(region, "sns", t.Name),
			Service: "sns",
			Label:   t.Name,
			Region:  region,
		})
	}

	// DynamoDB tables
	for _, kv := range byNS[tNsTables] {
		var t tTable
		if json.Unmarshal([]byte(kv.Value), &t) != nil {
			continue
		}
		region := regionFromARN(t.TableARN, defaultRegion)
		streamEnabled := t.StreamSpecification != nil && t.StreamSpecification.StreamEnabled
		addNode(topology.Node{
			ID:            topology.NodeID(region, "dynamodb", t.TableName),
			Service:       "dynamodb",
			Label:         t.TableName,
			Region:        region,
			StreamEnabled: &streamEnabled,
		})
	}

	// CloudWatch Logs groups
	for _, kv := range byNS[tNsLogGroups] {
		var lg tLogGroup
		if json.Unmarshal([]byte(kv.Value), &lg) != nil {
			continue
		}
		region := regionFromARN(lg.ARN, defaultRegion)
		addNode(topology.Node{
			ID:      topology.NodeID(region, "logs", lg.Name),
			Service: "logs",
			Label:   lg.Name,
			Region:  region,
		})
	}

	// EC2 instances
	for _, kv := range byNS[tNsInstances] {
		var inst tInstance
		if json.Unmarshal([]byte(kv.Value), &inst) != nil {
			continue
		}
		region := regionFromKey(kv.Key, defaultRegion)
		addNode(topology.Node{
			ID:      topology.NodeID(region, "ec2", inst.InstanceID),
			Service: "ec2",
			Label:   inst.InstanceID,
			Region:  region,
			VpcID:   inst.VpcID,
			Status:  inst.State.Name,
		})
	}

	// EC2 VPCs
	// First count subnets per VPC for the node metadata.
	subnetCountByVpc := make(map[string]int)
	for _, kv := range byNS[tNsSubnets] {
		var sub tSubnet
		if json.Unmarshal([]byte(kv.Value), &sub) != nil {
			continue
		}
		subnetCountByVpc[sub.VpcID]++
	}

	// Collect IGW attachments so we can tag VPCs that have an IGW.
	igwAttachmentsByVpc := make(map[string]string) // vpcId → igwId
	for _, kv := range byNS[tNsInternetGateways] {
		var igw tIGW
		if json.Unmarshal([]byte(kv.Value), &igw) != nil {
			continue
		}
		for _, att := range igw.Attachments {
			if att.VpcID != "" {
				igwAttachmentsByVpc[att.VpcID] = igw.InternetGatewayID
			}
		}
	}

	for _, kv := range byNS[tNsVPCs] {
		var vpc tVPC
		if json.Unmarshal([]byte(kv.Value), &vpc) != nil {
			continue
		}
		// Region is stored in the region-scoped key (e.g. "us-east-1/vpc-abc").
		region := regionFromKey(kv.Key, defaultRegion)
		subnetCount := subnetCountByVpc[vpc.VpcID]
		_, hasIGW := igwAttachmentsByVpc[vpc.VpcID]
		addNode(topology.Node{
			ID:                 topology.NodeID(region, "vpc", vpc.VpcID),
			Service:            "vpc",
			Label:              vpc.VpcID,
			Region:             region,
			Status:             vpc.State,
			CidrBlock:          vpc.CidrBlock,
			SubnetCount:        &subnetCount,
			HasInternetGateway: &hasIGW,
		})
	}

	// EC2 Internet Gateways
	for _, kv := range byNS[tNsInternetGateways] {
		var igw tIGW
		if json.Unmarshal([]byte(kv.Value), &igw) != nil {
			continue
		}
		region := regionFromKey(kv.Key, defaultRegion)
		// Find the attached VPC (if any).
		var attachedVpc string
		for _, att := range igw.Attachments {
			if att.VpcID != "" {
				attachedVpc = att.VpcID
				break
			}
		}
		addNode(topology.Node{
			ID:            topology.NodeID(region, "igw", igw.InternetGatewayID),
			Service:       "igw",
			Label:         igw.InternetGatewayID,
			Region:        region,
			AttachedVpcID: attachedVpc,
		})
	}

	// ECS clusters
	type ecsClusterIdentity struct {
		name   string
		region string
	}
	clusterIndex := make(map[string]ecsClusterIdentity)
	for _, kv := range byNS[tNsClusters] {
		var c tCluster
		if json.Unmarshal([]byte(kv.Value), &c) != nil {
			continue
		}
		region := regionFromARN(c.ARN, defaultRegion)
		clusterIndex[c.ARN] = ecsClusterIdentity{name: c.Name, region: region}
		addNode(topology.Node{
			ID:              topology.NodeID(region, "ecs", c.Name),
			Service:         "ecs",
			Label:           c.Name,
			Region:          region,
			Status:          c.Status,
			ECSResourceType: topology.ECSCluster,
			ClusterName:     c.Name,
		})
	}

	// ECS task definitions' container images, for repository → service edges.
	ecsTaskDefImages := make(map[string][]string)
	for _, kv := range byNS[tNsECSTaskDefs] {
		var td tECSTaskDefinition
		if json.Unmarshal([]byte(kv.Value), &td) != nil {
			continue
		}
		for _, container := range td.ContainerDefinitions {
			ecsTaskDefImages[td.TaskDefinitionArn] = append(ecsTaskDefImages[td.TaskDefinitionArn], container.Image)
		}
	}

	// ECS services (nodes, cluster edges and repository edges)
	for _, kv := range byNS[tNsECSServices] {
		var svc tECSService
		if json.Unmarshal([]byte(kv.Value), &svc) != nil {
			continue
		}
		region := regionFromARN(svc.ARN, defaultRegion)
		clusterName := nameFromSlashSuffix(svc.ClusterARN)
		if cluster, ok := clusterIndex[svc.ClusterARN]; ok {
			region = cluster.region
			clusterName = cluster.name
		}
		desiredCount, runningCount := svc.DesiredCount, svc.RunningCount
		serviceRef := topology.ID(region, "ecs-service", clusterName+"/"+svc.Name)
		addNode(topology.Node{
			ID:              topology.NodeID(region, "ecs-service", clusterName+"/"+svc.Name),
			Service:         "ecs",
			Label:           svc.Name,
			Region:          region,
			Status:          svc.Status,
			ECSResourceType: topology.ECSService,
			ClusterName:     clusterName,
			DesiredCount:    &desiredCount,
			RunningCount:    &runningCount,
		})
		addEdge(topology.ID(region, "ecs", clusterName), serviceRef, "ecs-svc", "ecs")
		for _, image := range ecsTaskDefImages[svc.TaskDefinition] {
			addEdge(topology.Image(image), serviceRef, "ecr-ecs", "container-image")
		}
	}

	// ECS tasks (nodes, and edges to their service or cluster)
	for _, kv := range byNS[tNsECSTasks] {
		var task tECSTask
		if json.Unmarshal([]byte(kv.Value), &task) != nil {
			continue
		}
		if task.LastStatus == "STOPPED" {
			continue
		}
		region := regionFromARN(task.TaskARN, defaultRegion)
		taskID := nameFromSlashSuffix(task.TaskARN)
		clusterName := nameFromSlashSuffix(task.ClusterARN)
		if cluster, ok := clusterIndex[task.ClusterARN]; ok {
			region = cluster.region
			clusterName = cluster.name
		}
		addNode(topology.Node{
			ID:              topology.NodeID(region, "ecs-task", clusterName+"/"+taskID),
			Service:         "ecs",
			Label:           taskID,
			Region:          region,
			Status:          task.LastStatus,
			ECSResourceType: topology.ECSTask,
			ClusterName:     clusterName,
			TaskID:          taskID,
		})
		taskRef := topology.ID(region, "ecs-task", clusterName+"/"+taskID)
		if svcName, ok := strings.CutPrefix(task.Group, "service:"); ok {
			addEdge(topology.ID(region, "ecs-service", clusterName+"/"+svcName), taskRef, "ecs-task", "ecs")
		} else {
			// Orphan task → cluster directly
			addEdge(topology.ID(region, "ecs", clusterName), taskRef, "ecs-task", "ecs")
		}
	}

	// ECR repositories, registered under their URI so image consumers find them.
	for _, kv := range byNS[tNsECRRepos] {
		var repo tECRRepository
		if json.Unmarshal([]byte(kv.Value), &repo) != nil {
			continue
		}
		region := regionFromARN(repo.RepositoryArn, defaultRegion)
		if region == defaultRegion {
			region = regionFromKey(kv.Key, defaultRegion)
		}
		addNode(topology.Node{
			ID:            topology.NodeID(region, "ecr", repo.RepositoryName),
			Service:       "ecr",
			Label:         repo.RepositoryName,
			Region:        region,
			RepositoryUri: repo.RepositoryURI,
		}, topology.Image(repo.RepositoryURI))
	}

	// RDS DB instances
	for _, kv := range byNS[tNsDBInstances] {
		var db tDBInstance
		if json.Unmarshal([]byte(kv.Value), &db) != nil {
			continue
		}
		if db.ID == "" {
			continue
		}
		// Region is not stored in the instance JSON — extract from the region-scoped key.
		region := regionFromKey(kv.Key, defaultRegion)
		addNode(topology.Node{
			ID:      topology.NodeID(region, "rds", db.ID),
			Service: "rds",
			Label:   db.ID,
			Region:  region,
			Status:  db.Status,
		})
	}

	// ElastiCache clusters
	for _, kv := range byNS[tNsCacheClusters] {
		var c tCacheCluster
		if json.Unmarshal([]byte(kv.Value), &c) != nil {
			continue
		}
		if c.ID == "" {
			continue
		}
		region := regionFromKey(kv.Key, defaultRegion)
		addNode(topology.Node{
			ID:      topology.NodeID(region, "elasticache", c.ID),
			Service: "elasticache",
			Label:   c.ID,
			Region:  region,
			Status:  c.Status,
		})
	}
	for _, kv := range byNS[tNsCacheReplicationGroups] {
		var rg tCacheReplicationGroup
		if json.Unmarshal([]byte(kv.Value), &rg) != nil {
			continue
		}
		if rg.ID == "" {
			continue
		}
		region := regionFromKey(kv.Key, defaultRegion)
		addNode(topology.Node{
			ID:      topology.NodeID(region, "elasticache", rg.ID),
			Service: "elasticache",
			Label:   rg.ID,
			Region:  region,
			Status:  rg.Status,
		})
	}
	for _, kv := range byNS[tNsServerlessCaches] {
		var c tServerlessCache
		if json.Unmarshal([]byte(kv.Value), &c) != nil {
			continue
		}
		if c.Name == "" {
			continue
		}
		region := regionFromKey(kv.Key, defaultRegion)
		addNode(topology.Node{
			ID:      topology.NodeID(region, "elasticache", c.Name),
			Service: "elasticache",
			Label:   c.Name,
			Region:  region,
			Status:  c.Status,
		})
	}

	// MSK clusters
	for _, kv := range byNS[tNsMSKClusters] {
		var c tMSKCluster
		if json.Unmarshal([]byte(kv.Value), &c) != nil {
			continue
		}
		if c.ClusterArn == "" {
			continue
		}
		region := regionFromKey(kv.Key, defaultRegion)
		label := c.ClusterName
		if label == "" {
			label = c.ClusterArn
		}
		addNode(topology.Node{
			ID:      topology.NodeID(region, "msk", c.ClusterArn),
			Service: "msk",
			Label:   label,
			Region:  region,
			Status:  c.State,
		})
	}

	// EFS file systems and access points. Mount targets are metadata-only and
	// have no node of their own (subnets are not topology nodes).
	for _, kv := range byNS[tNsEFSFileSystems] {
		var fs tEFSFileSystem
		if json.Unmarshal([]byte(kv.Value), &fs) != nil || fs.ID == "" {
			continue
		}
		region := regionFromKey(kv.Key, defaultRegion)
		addNode(topology.Node{
			ID:      topology.NodeID(region, "efs", fs.ID),
			Service: "efs",
			Label:   fs.ID,
			Region:  region,
			Status:  fs.Status,
		})
	}
	for _, kv := range byNS[tNsEFSAccessPoints] {
		var ap tEFSAccessPoint
		if json.Unmarshal([]byte(kv.Value), &ap) != nil || ap.ID == "" {
			continue
		}
		region := regionFromKey(kv.Key, defaultRegion)
		addNode(topology.Node{
			ID:      topology.NodeID(region, "efs", ap.ID),
			Service: "efs",
			Label:   ap.ID,
			Region:  region,
			Status:  ap.Status,
		})
		if ap.FileSystemID != "" {
			g.AddLink(topology.Link{
				Source:   topology.ID(region, "efs", ap.ID),
				Target:   topology.ID(region, "efs", ap.FileSystemID),
				IDPrefix: "efs-ap",
				Type:     "vpc-attachment",
				Label:    "access point",
			})
		}
	}

	// API Gateway REST APIs (v1)
	// Count resources and stages per API for metadata.
	// Keys are region-scoped: "{region}/{apiID}/{resourceID}".
	v1ResourceCount := make(map[string]int)      // apiID → count
	v1LambdaTargets := make(map[string][]string) // apiID → []functionName
	for _, kv := range byNS[tNsAPIResources] {
		apiID := apiIDFromKey(kv.Key)
		v1ResourceCount[apiID]++

		// Extract Lambda targets from integrations.
		var res tAPIResource
		if json.Unmarshal([]byte(kv.Value), &res) != nil {
			continue
		}
		for _, m := range res.ResourceMethods {
			if m.MethodIntegration != nil &&
				(m.MethodIntegration.Type == "AWS_PROXY" || m.MethodIntegration.Type == "AWS") &&
				m.MethodIntegration.URI != "" {
				fnName := lambdaNameFromARN(m.MethodIntegration.URI)
				if fnName != "" {
					v1LambdaTargets[apiID] = append(v1LambdaTargets[apiID], fnName)
				}
			}
		}
	}
	v1StageCount := make(map[string]int) // apiID → count
	for _, kv := range byNS[tNsAPIStages] {
		v1StageCount[apiIDFromKey(kv.Key)]++
	}

	for _, kv := range byNS[tNsRestAPIs] {
		var api tRestAPI
		if json.Unmarshal([]byte(kv.Value), &api) != nil {
			continue
		}
		region := regionFromKey(kv.Key, defaultRegion)
		routes := v1ResourceCount[api.ID]
		stages := v1StageCount[api.ID]
		addNode(topology.Node{
			ID:           topology.NodeID(region, "apigateway", api.ID),
			Service:      "apigateway",
			Label:        api.Name,
			Region:       region,
			ProtocolType: "REST",
			RouteCount:   &routes,
			StageCount:   &stages,
		})
		addAPIGatewayLambdaEdges(g, region, api.ID, v1LambdaTargets[api.ID])
	}

	// API Gateway HTTP APIs (v2)
	// Keys are region-scoped: "{region}/{apiID}/{routeID|stageID|integrationID}".
	v2RouteCount := make(map[string]int)         // apiID → count
	v2LambdaTargets := make(map[string][]string) // apiID → []functionName

	// Build integration index for v2 target resolution.
	// Strip region prefix so the index key is "apiID/integrationID".
	v2IntegIndex := make(map[string]*tIntegrationV2) // "apiID/integrationID" → integration
	for _, kv := range byNS[tNsV2Integ] {
		var integ tIntegrationV2
		if json.Unmarshal([]byte(kv.Value), &integ) != nil {
			continue
		}
		_, rest := serviceutil.SplitRegionKey(kv.Key)
		v2IntegIndex[rest] = &integ
	}

	for _, kv := range byNS[tNsV2Routes] {
		apiID := apiIDFromKey(kv.Key)
		v2RouteCount[apiID]++

		// Resolve route target → integration → Lambda.
		var route tRouteV2
		if json.Unmarshal([]byte(kv.Value), &route) != nil {
			continue
		}
		if integID, ok := strings.CutPrefix(route.Target, "integrations/"); ok {
			if integ, ok := v2IntegIndex[apiID+"/"+integID]; ok {
				if integ.IntegrationType == "AWS_PROXY" && integ.IntegrationURI != "" {
					fnName := lambdaNameFromARN(integ.IntegrationURI)
					if fnName != "" {
						v2LambdaTargets[apiID] = append(v2LambdaTargets[apiID], fnName)
					}
				}
			}
		}
	}
	v2StageCount := make(map[string]int) // apiID → count
	for _, kv := range byNS[tNsV2Stages] {
		v2StageCount[apiIDFromKey(kv.Key)]++
	}

	for _, kv := range byNS[tNsV2APIs] {
		var api tAPIV2
		if json.Unmarshal([]byte(kv.Value), &api) != nil {
			continue
		}
		region := regionFromKey(kv.Key, defaultRegion)
		routes := v2RouteCount[api.ApiID]
		stages := v2StageCount[api.ApiID]
		addNode(topology.Node{
			ID:           topology.NodeID(region, "apigateway", api.ApiID),
			Service:      "apigateway",
			Label:        api.Name,
			Region:       region,
			ProtocolType: api.ProtocolType,
			RouteCount:   &routes,
			StageCount:   &stages,
		})
		addAPIGatewayLambdaEdges(g, region, api.ApiID, v2LambdaTargets[api.ApiID])
	}

	// CloudFront distributions, with edges to the S3 buckets they front.
	for _, kv := range byNS[tNsCFDistributions] {
		region, rest := serviceutil.SplitRegionKey(kv.Key)
		if region == "" {
			region = defaultRegion
		}
		// Only include distribution records (keys "us-east-1/dist:E1234567890ABC").
		if !strings.HasPrefix(rest, "dist:") {
			continue
		}
		var dist tCFDistribution
		if json.Unmarshal([]byte(kv.Value), &dist) != nil {
			continue
		}
		if dist.ID == "" {
			continue
		}
		origins := dist.Config.Origins.Quantity
		label := dist.ID
		if dist.Config.Comment != "" {
			label = dist.Config.Comment
		}
		addNode(topology.Node{
			ID:          topology.NodeID(region, "cloudfront", dist.ID),
			Service:     "cloudfront",
			Label:       label,
			Region:      region,
			Status:      dist.Status,
			DomainName:  dist.DomainName,
			OriginCount: &origins,
		})
		for _, o := range dist.Config.Origins.Items {
			dn := o.DomainName
			if strings.HasSuffix(dn, ".s3.amazonaws.com") || (strings.Contains(dn, ".s3.") && strings.HasSuffix(dn, ".amazonaws.com")) {
				dn = strings.TrimSuffix(dn, ".amazonaws.com")
				if idx := strings.Index(dn, ".s3"); idx > 0 {
					g.AddLink(topology.Link{
						Source:   topology.ID(region, "cloudfront", dist.ID),
						Target:   topology.ID(region, "s3", dn[:idx]).AnyRegion(),
						IDPrefix: "origin",
						Type:     "origin",
						Label:    "S3 origin",
					})
				}
			}
		}
	}

	// WAFv2 Web ACLs. These nodes represent stored control-plane metadata;
	// Overcast does not evaluate their rules against application traffic.
	for _, kv := range byNS[tNsWAFWebACLs] {
		var acl tWAFWebACL
		if json.Unmarshal([]byte(kv.Value), &acl) != nil || acl.ID == "" {
			continue
		}
		region := regionFromARN(acl.ARN, regionFromKey(kv.Key, defaultRegion))
		label := acl.Name
		if label == "" {
			label = acl.ID
		}
		ruleCount := len(acl.Rules)
		addNode(topology.Node{
			ID:        topology.NodeID(region, "waf", acl.ID),
			Service:   "waf",
			Label:     label,
			Region:    region,
			Scope:     acl.Scope,
			RuleCount: &ruleCount,
		})
	}

	// AppSync GraphQL APIs
	// The "appsync" namespace stores all sub-resources with different key prefixes:
	// "region/api:APIID", "region/ds:APIID:NAME" and "region/resolver:APIID:TYPE:FIELD".
	// Count data sources and resolvers first, so API nodes carry the totals.
	appsyncDSCount := make(map[string]int)       // apiID → count
	appsyncResolverCount := make(map[string]int) // apiID → count
	for _, kv := range byNS[tNsAppSync] {
		_, rest := serviceutil.SplitRegionKey(kv.Key)
		if ds, ok := strings.CutPrefix(rest, "ds:"); ok {
			if apiID, _, ok := strings.Cut(ds, ":"); ok {
				appsyncDSCount[apiID]++
			}
		} else if resolver, ok := strings.CutPrefix(rest, "resolver:"); ok {
			apiID, _, _ := strings.Cut(resolver, ":")
			appsyncResolverCount[apiID]++
		}
	}
	for _, kv := range byNS[tNsAppSync] {
		region, rest := serviceutil.SplitRegionKey(kv.Key)
		if region == "" {
			region = defaultRegion
		}
		switch {
		case strings.HasPrefix(rest, "api:"):
			var api tAppSyncAPI
			if json.Unmarshal([]byte(kv.Value), &api) != nil {
				continue
			}
			dsCount := appsyncDSCount[api.ApiId]
			resolverCount := appsyncResolverCount[api.ApiId]
			addNode(topology.Node{
				ID:                 topology.NodeID(region, "appsync", api.ApiId),
				Service:            "appsync",
				Label:              api.Name,
				Region:             region,
				AuthenticationType: api.AuthenticationType,
				DataSourceCount:    &dsCount,
				ResolverCount:      &resolverCount,
			})
		case strings.HasPrefix(rest, "ds:"):
			var ds tAppSyncDataSource
			if json.Unmarshal([]byte(kv.Value), &ds) != nil {
				continue
			}
			apiID, _, ok := strings.Cut(strings.TrimPrefix(rest, "ds:"), ":")
			if !ok {
				continue
			}
			addAppSyncDataSourceEdge(g, region, apiID, ds)
		}
	}

	// ── Cognito User Pools ─────────────────────────────────────────────────
	for _, kv := range byNS[tNsCognitoPools] {
		var p tCognitoPool
		if json.Unmarshal([]byte(kv.Value), &p) != nil {
			continue
		}
		region := regionFromKey(kv.Key, defaultRegion)
		addNode(topology.Node{
			ID:      topology.NodeID(region, "cognito", p.ID),
			Service: "cognito",
			Label:   p.Name,
			Region:  region,
		})
	}

	// ── Edges ──────────────────────────────────────────────────────────────

	// S3 → SQS / Lambda notification edges
	for _, kv := range byNS[tNsNotifications] {
		bucketName := kv.Key
		var nc tNotificationConfig
		if json.Unmarshal([]byte(kv.Value), &nc) != nil {
			continue
		}
		bucket := topology.ID(defaultRegion, "s3", bucketName).AnyRegion()
		for _, qc := range nc.QueueConfigurations {
			addEdge(bucket, topology.ID(regionFromARN(qc.ARN, defaultRegion), "sqs", nameFromARNSuffix(qc.ARN)), "notif", "notification")
		}
		for _, lc := range nc.LambdaConfigurations {
			addEdge(bucket, topology.ID(regionFromARN(lc.ARN, defaultRegion), "lambda", lambdaNameFromARN(lc.ARN)), "notif", "notification")
		}
	}

	// SNS → SQS / Lambda subscription edges
	for _, kv := range byNS[tNsSubscriptions] {
		var sub tSubscription
		if json.Unmarshal([]byte(kv.Value), &sub) != nil {
			continue
		}
		topicRegion, ok := topicIndex[sub.TopicName]
		if !ok {
			continue
		}
		topic := topology.ID(topicRegion, "sns", sub.TopicName)
		switch strings.ToLower(sub.Protocol) {
		case "sqs":
			qName := sub.QueueName
			if qName == "" {
				qName = nameFromARNSuffix(sub.Endpoint)
			}
			addEdge(topic, topology.ID(regionFromARN(sub.Endpoint, defaultRegion), "sqs", qName), "sub", "subscription")
		case "lambda":
			addEdge(topic, topology.ID(regionFromARN(sub.Endpoint, defaultRegion), "lambda", lambdaNameFromARN(sub.Endpoint)), "sub", "subscription")
		}
	}

	// Pipes edges. Both endpoints are resolved from the pipe's Source and
	// Target ARNs (SQS, SNS, DynamoDB stream, Lambda, ...). Legacy records
	// that carry only SourceName/TargetName keep the historical
	// DynamoDB → SQS interpretation.
	for _, kv := range byNS[tNsPipes] {
		var p tPipe
		if json.Unmarshal([]byte(kv.Value), &p) != nil {
			continue
		}
		srcRegion := regionFromARN(p.SourceArn, defaultRegion)
		src := pipeEndpointRef(p.SourceArn, defaultRegion)
		if p.SourceArn == "" && p.SourceName != "" {
			src = topology.ID(srcRegion, "dynamodb", p.SourceName).AnyRegion()
		}
		tgt := pipeEndpointRef(p.TargetArn, defaultRegion)
		if p.TargetArn == "" && p.TargetName != "" {
			tgt = topology.ID(regionFromARN(p.TargetArn, defaultRegion), "sqs", p.TargetName).AnyRegion()
		}
		g.AddLink(topology.Link{
			Source: src,
			Target: tgt,
			ID:     "pipe::" + srcRegion + "::" + p.Name,
			Type:   "pipe",
			Label:  p.Name,
			State:  p.CurrentState,
		})
	}

	// IGW → VPC attachment edges
	for _, kv := range byNS[tNsInternetGateways] {
		var igw tIGW
		if json.Unmarshal([]byte(kv.Value), &igw) != nil {
			continue
		}
		region := regionFromKey(kv.Key, defaultRegion)
		for _, att := range igw.Attachments {
			if att.VpcID != "" {
				addEdge(topology.ID(region, "igw", igw.InternetGatewayID), topology.ID(region, "vpc", att.VpcID), "igw-attach", "vpc-attachment")
			}
		}
	}

	// EC2 instance → VPC edges (instances that belong to a VPC)
	for _, kv := range byNS[tNsInstances] {
		var inst tInstance
		if json.Unmarshal([]byte(kv.Value), &inst) != nil || inst.VpcID == "" {
			continue
		}
		region := regionFromKey(kv.Key, defaultRegion)
		addEdge(topology.ID(region, "vpc", inst.VpcID), topology.ID(region, "ec2", inst.InstanceID), "vpc-member", "vpc-member")
	}

	contributeCloudFormation(defaultRegion, byNS[tNsCFNStacks], g)
}

// contributeCloudFormation tags each stack's resources with the stack's name
// and links nested stacks to their parents.
func contributeCloudFormation(defaultRegion string, kvs []state.KV, g *topology.Graph) {
	// First pass: parse all stacks and index by ID for parent lookups.
	type parsedCFNStack struct {
		stack  tCFNStack
		region string
	}
	cfnStacksByID := make(map[string]parsedCFNStack)
	var cfnStacks []parsedCFNStack
	for _, kv := range kvs {
		var stack tCFNStack
		if json.Unmarshal([]byte(kv.Value), &stack) != nil {
			continue
		}
		if stack.Status == "DELETE_COMPLETE" {
			continue
		}
		stackRegion := stack.Region
		if stackRegion == "" {
			stackRegion = defaultRegion
		}
		parsed := parsedCFNStack{stack: stack, region: stackRegion}
		cfnStacks = append(cfnStacks, parsed)
		if stack.StackID != "" {
			cfnStacksByID[stack.StackID] = parsed
		}
	}

	// Second pass: ownership tagging and nested-stack edges.
	for _, ps := range cfnStacks {
		stack := ps.stack
		stackRegion := ps.region

		for _, res := range stack.Resources {
			if res.PhysicalID != "" {
				g.SetStack(cfnResourceRef(res, stackRegion), stack.StackName)
			}
		}

		// Nested-stack edge: parent → child, between their stack group IDs.
		// Stack group IDs (stack::region::name) are phantom nodes created by
		// the frontend layout — they don't exist in the node set, so the edge
		// is added verbatim rather than as a link.
		if stack.ParentStackID != "" {
			if parent, ok := cfnStacksByID[stack.ParentStackID]; ok {
				parentRegion := parent.region
				srcID := "stack::" + parentRegion + "::" + parent.stack.StackName
				tgtID := "stack::" + stackRegion + "::" + stack.StackName
				g.AddEdge(topology.Edge{
					ID:           "nested-stack::" + srcID + "→" + tgtID,
					Source:       srcID,
					Target:       tgtID,
					Type:         "nested-stack",
					SourceRegion: parentRegion,
					TargetRegion: stackRegion,
				})
			}
		}
	}
}

// addAPIGatewayLambdaEdges links an API to each distinct function its
// integrations invoke. Integration URIs are resolved by name, preferring the
// API's own region.
func addAPIGatewayLambdaEdges(g *topology.Graph, region, apiID string, fnNames []string) {
	seen := make(map[string]bool)
	for _, fnName := range fnNames {
		if seen[fnName] {
			continue
		}
		seen[fnName] = true
		g.AddLink(topology.Link{
			Source:   topology.ID(region, "apigateway", apiID),
			Target:   topology.ID(region, "lambda", fnName).AnyRegion(),
			IDPrefix: "apigw",
			Type:     "apigw-integration",
		})
	}
}

// addAppSyncDataSourceEdge links an AppSync API to a Lambda or DynamoDB data
// source.
func addAppSyncDataSourceEdge(g *topology.Graph, region, apiID string, ds tAppSyncDataSource) {
	var target topology.Ref
	switch ds.Type {
	case "AWS_LAMBDA":
		var cfg struct {
			LambdaFunctionArn string `json:"lambdaFunctionArn"`
		}
		if json.Unmarshal(ds.LambdaConfig, &cfg) != nil || cfg.LambdaFunctionArn == "" {
			return
		}
		target = topology.ID(regionFromARN(cfg.LambdaFunctionArn, region), "lambda", lambdaNameFromARN(cfg.LambdaFunctionArn))
	case "AMAZON_DYNAMODB":
		var cfg struct {
			TableName string `json:"tableName"`
		}
		if json.Unmarshal(ds.DynamodbConfig, &cfg) != nil || cfg.TableName == "" {
			return
		}
		target = topology.ID(region, "dynamodb", cfg.TableName)
	default:
		return
	}
	g.AddLink(topology.Link{
		Source:   topology.ID(region, "appsync", apiID),
		Target:   target,
		IDPrefix: "appsync-ds",
		Type:     "appsync-datasource",
		Label:    ds.Name,
	})
}

// ── Key and ARN helpers ────────────────────────────────────────────────────

// regionFromARN extracts the region from a standard AWS ARN, or returns
// fallback when the ARN has none.
func regionFromARN(arn, fallback string) string {
	if r := serviceutil.ARNRegion(arn); r != "" {
		return r
	}
	return fallback
}

// regionFromKey extracts the region from a region-scoped store key
// ("us-east-1/vpc-abc"), or returns fallback when the key has none.
func regionFromKey(key, fallback string) string {
	if r, _ := serviceutil.SplitRegionKey(key); r != "" {
		return r
	}
	return fallback
}

// apiIDFromKey extracts the API ID from an API Gateway sub-resource key,
// "{region}/{apiID}/{resourceID|stageName|…}".
func apiIDFromKey(key string) string {
	_, rest := serviceutil.SplitRegionKey(key)
	apiID, _, _ := strings.Cut(rest, "/")
	return apiID
}

// nameFromARNSuffix returns the last colon-separated segment of an ARN.
func nameFromARNSuffix(arn string) string {
	if i := strings.LastIndex(arn, ":"); i >= 0 {
		return arn[i+1:]
	}
	return arn
}

func nameFromSlashSuffix(arn string) string {
	if i := strings.LastIndexByte(arn, '/'); i >= 0 {
		return arn[i+1:]
	}
	return nameFromARNSuffix(arn)
}

// lambdaNameFromARN extracts the function name from a Lambda ARN or
// API Gateway integration URI.
// arn:aws:lambda:us-east-1:000000000000:function:my-func → "my-func"
// arn:aws:lambda:us-east-1:000000000000:function:my-func:3 → "my-func"
// arn:aws:apigateway:...:lambda:path/.../functions/<lambda-arn>/invocations → "my-func"
func lambdaNameFromARN(arn string) string {
	// Handle API Gateway integration URI: extract the Lambda ARN from the
	// /functions/<arn>/invocations pattern.
	if idx := strings.Index(arn, "/functions/"); idx >= 0 {
		remainder := arn[idx+len("/functions/"):]
		remainder = strings.TrimSuffix(remainder, "/invocations")
		arn = remainder
	}

	name := nameFromARNSuffix(arn)
	// If the last segment is a numeric version qualifier, go one more level up
	if isNumeric(name) {
		// Remove ":version" suffix and try again
		trimmed := arn[:strings.LastIndex(arn, ":")]
		name = nameFromARNSuffix(trimmed)
	}
	// Handle "function:name" format
	if strings.HasPrefix(name, "function:") {
		return strings.TrimPrefix(name, "function:")
	}
	return name
}

// tableNameFromStreamARN extracts the table name from a DynamoDB stream ARN.
// arn:aws:dynamodb:us-east-1:000000000000:table/MyTable/stream/2024-01-01T00:00:00.000
func tableNameFromStreamARN(arn string) string {
	// Look for table/<name>/stream or table/<name>
	idx := strings.Index(arn, "table/")
	if idx < 0 {
		return nameFromARNSuffix(arn)
	}
	rest := arn[idx+len("table/"):]
	if i := strings.Index(rest, "/"); i >= 0 {
		return rest[:i]
	}
	return rest
}

func isNumeric(s string) bool {
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return len(s) > 0
}

// pipeEndpointRef maps an EventBridge Pipes source or target ARN to the
// node that represents it, keyed on the ARN's service segment and falling
// back to the same name in another region. Returns the zero Ref for an empty
// ARN or a service the graph has no node type for.
func pipeEndpointRef(arn, defaultRegion string) topology.Ref {
	parts := strings.SplitN(arn, ":", 6)
	if len(parts) < 6 || parts[0] != "arn" {
		return topology.Ref{}
	}
	region := regionFromARN(arn, defaultRegion)
	var kind, name string
	switch parts[2] {
	case "sqs", "sns":
		kind, name = parts[2], nameFromARNSuffix(arn)
	case "dynamodb":
		// Either a table ARN or a stream ARN; both map to the table node.
		kind, name = "dynamodb", tableNameFromStreamARN(arn)
	case "lambda":
		kind, name = "lambda", lambdaNameFromARN(arn)
	case "kinesis":
		// arn:aws:kinesis:region:acct:stream/<name>
		kind, name = "kinesis", nameFromSlashSuffix(arn)
	case "states":
		// arn:aws:states:region:acct:stateMachine:<name>
		kind, name = "states", nameFromARNSuffix(arn)
	case "events":
		// arn:aws:events:region:acct:event-bus/<name>
		kind, name = "events", nameFromSlashSuffix(arn)
	default:
		return topology.Ref{}
	}
	return topology.ID(region, kind, name).AnyRegion()
}

// cfnResourceRef maps a CloudFormation resource to the node that represents
// it. A type whose service contributes its own topology registers its nodes
// under topology.CFN, so anything not listed here resolves that way.
func cfnResourceRef(res tCFNResource, defaultRegion string) topology.Ref {
	switch {
	case strings.HasPrefix(res.Type, "AWS::S3::Bucket"):
		return topology.ID(defaultRegion, "s3", res.PhysicalID)
	case res.Type == "AWS::SNS::Topic":
		return topology.ID(regionFromARN(res.PhysicalID, defaultRegion), "sns", nameFromARNSuffix(res.PhysicalID))
	case res.Type == "AWS::DynamoDB::Table":
		name := res.PhysicalID
		if i := strings.LastIndex(name, "/"); i >= 0 {
			name = name[i+1:]
		}
		return topology.ID(regionFromARN(res.PhysicalID, defaultRegion), "dynamodb", name)
	case res.Type == "AWS::Logs::LogGroup":
		return topology.ID(defaultRegion, "logs", res.PhysicalID)
	case res.Type == "AWS::EC2::Instance":
		return topology.ID(defaultRegion, "ec2", res.PhysicalID)
	case res.Type == "AWS::ECS::Cluster":
		return topology.ID(defaultRegion, "ecs", nameFromARNSuffix(res.PhysicalID))
	case res.Type == "AWS::ECS::Service":
		// PhysicalID is the service ARN: arn:aws:ecs:region:acct:service/cluster/name
		if parts := strings.SplitN(res.PhysicalID, "/", 3); len(parts) == 3 {
			return topology.ID(regionFromARN(res.PhysicalID, defaultRegion), "ecs-service", parts[1]+"/"+parts[2])
		}
		return topology.Ref{}
	case res.Type == "AWS::RDS::DBInstance":
		return topology.ID(defaultRegion, "rds", res.PhysicalID)
	case res.Type == "AWS::ElastiCache::CacheCluster" || res.Type == "AWS::ElastiCache::ServerlessCache" || res.Type == "AWS::ElastiCache::ReplicationGroup":
		return topology.ID(defaultRegion, "elasticache", res.PhysicalID)
	case res.Type == "AWS::ApiGateway::RestApi" || res.Type == "AWS::ApiGatewayV2::Api":
		return topology.ID(defaultRegion, "apigateway", res.PhysicalID)
	case res.Type == "AWS::Cognito::UserPool":
		return topology.ID(defaultRegion, "cognito", res.PhysicalID)
	case res.Type == "AWS::AppSync::GraphQLApi":
		return topology.ID(defaultRegion, "appsync", res.PhysicalID)
	case res.Type == "AWS::CloudFront::Distribution":
		return topology.ID(defaultRegion, "cloudfront", res.PhysicalID)
	case res.Type == "AWS::WAFv2::WebACL":
		id := res.PhysicalID
		if parts := strings.SplitN(id, "/", 2); len(parts) == 2 {
			id = parts[1]
		}
		return topology.ID(defaultRegion, "waf", id)
	default:
		return topology.CFN(defaultRegion, res.Type, res.PhysicalID)
	}
}
