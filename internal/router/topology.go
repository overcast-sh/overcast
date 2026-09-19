package router

// topology.go — internal topology API for the system map.
//
// GET /_overcast/topology — returns every resource and connection across all regions
// in a single, fast response. Reads directly from the state store with
// parallel Scan calls, avoiding the overhead of marshalling AWS SDK requests
// back into our own process.
//
// Optional query parameter:
//   ?region=us-east-1   — return only resources whose region matches.
//                         Omit to get all resources across all regions.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"github.com/overcast-sh/overcast/internal/config"
	"github.com/overcast-sh/overcast/internal/serviceutil"
	"github.com/overcast-sh/overcast/internal/state"
)

// ── Types ──────────────────────────────────────────────────────────────────

// topologyECSResourceType says which kind of ECS resource a topology node is:
// one of the constants below. The Map page navigates differently for each
// (a task node links to its task detail, a service node to its service), so
// the values are a contract — cmd/tsgen renders them as the TypeScript union.
type topologyECSResourceType = string

const (
	topologyECSCluster topologyECSResourceType = "cluster"
	topologyECSService topologyECSResourceType = "service"
	topologyECSTask    topologyECSResourceType = "task"
)

// topologyNode is one node of GET /_overcast/topology's graph. The fields
// after Region are per-service details, each populated only for the services
// named in its comment and omitted otherwise.
type topologyNode struct {
	ID      string `json:"id"`
	Service string `json:"service"`
	Label   string `json:"label"`
	Region  string `json:"region"`

	StreamEnabled                         *bool    `json:"streamEnabled,omitempty"`                         // DynamoDB only — whether the table has a stream
	ApproximateNumberOfMessages           *int     `json:"approximateNumberOfMessages,omitempty"`           // SQS only — visible messages waiting to be consumed
	ApproximateNumberOfMessagesNotVisible *int     `json:"approximateNumberOfMessagesNotVisible,omitempty"` // SQS only — messages in flight (received, not yet deleted/returned)
	StackName                             *string  `json:"stackName,omitempty"`                             // CloudFormation stack name this resource belongs to (1:1 ownership)
	VpcID                                 string   `json:"vpcId,omitempty"`                                 // VPC ID this resource belongs to (EC2 instances, RDS instances, …)
	Status                                string   `json:"status,omitempty"`                                // resource status string (e.g. RDS DBInstanceStatus: "available", "stopped")
	CidrBlock                             string   `json:"cidrBlock,omitempty"`                             // VPC only — CIDR block (e.g. "10.0.0.0/16")
	SubnetCount                           *int     `json:"subnetCount,omitempty"`                           // VPC only — number of subnets in this VPC
	HasInternetGateway                    *bool    `json:"hasInternetGateway,omitempty"`                    // VPC only — whether an internet gateway is attached
	AttachedVpcID                         string   `json:"attachedVpcId,omitempty"`                         // IGW only — the VPC ID attached to this internet gateway
	ProtocolType                          string   `json:"protocolType,omitempty"`                          // API Gateway only — protocol type (REST or HTTP)
	RouteCount                            *int     `json:"routeCount,omitempty"`                            // API Gateway only — number of routes or resources configured
	StageCount                            *int     `json:"stageCount,omitempty"`                            // API Gateway only — number of deployed stages
	DomainName                            string   `json:"domainName,omitempty"`                            // CloudFront only — the distribution's domain name
	OriginCount                           *int     `json:"originCount,omitempty"`                           // CloudFront only — number of origins
	AuthenticationType                    string   `json:"authenticationType,omitempty"`                    // AppSync only — authentication type (API_KEY, AWS_IAM, …)
	DataSourceCount                       *int     `json:"dataSourceCount,omitempty"`                       // AppSync only — number of data sources attached
	ResolverCount                         *int     `json:"resolverCount,omitempty"`                         // AppSync only — number of resolvers configured
	RepositoryUri                         string   `json:"repositoryUri,omitempty"`                         // ECR only — full push-ready repository URI (e.g. localhost:5000/my-repo)
	Scope                                 string   `json:"scope,omitempty"`                                 // WAF only — REGIONAL or CLOUDFRONT
	RuleCount                             *int     `json:"ruleCount,omitempty"`                             // WAF only — number of stored rules (rules are not enforced)
	ESMID                                 string   `json:"esmId,omitempty"`                                 // Lambda ESM filter node only — EventSourceMapping UUID
	FunctionName                          string   `json:"functionName,omitempty"`                          // Lambda ESM filter node only — target function name
	EventSource                           string   `json:"eventSource,omitempty"`                           // Lambda ESM filter node only — source queue/table name
	SourceType                            string   `json:"sourceType,omitempty"`                            // Lambda ESM filter node only — source type, e.g. dynamodb
	FilterPatterns                        []string `json:"filterPatterns,omitempty"`                        // Lambda ESM filter node only — raw FilterCriteria patterns

	ECSResourceType topologyECSResourceType `json:"ecsResourceType,omitempty"` // ECS only — whether this node is a cluster, service, or task
	ClusterName     string                  `json:"clusterName,omitempty"`     // ECS service/task owner, used for detail navigation
	TaskID          string                  `json:"taskId,omitempty"`          // ECS task only — task UUID used by the task detail route
	DesiredCount    *int                    `json:"desiredCount,omitempty"`    // ECS service only — configured task count
	RunningCount    *int                    `json:"runningCount,omitempty"`    // ECS service only — currently running task count
}

type topologyEdge struct {
	ID           string `json:"id"`
	Source       string `json:"source"`
	Target       string `json:"target"`
	Type         string `json:"type"`
	Label        string `json:"label,omitempty"`
	State        string `json:"state,omitempty"`
	SourceRegion string `json:"sourceRegion,omitempty"`
	TargetRegion string `json:"targetRegion,omitempty"`
}

type tFilterCriteria struct {
	Filters []struct {
		Pattern string `json:"Pattern"`
	} `json:"Filters"`
}

type topologyResponse struct {
	Regions []string       `json:"regions"`
	Nodes   []topologyNode `json:"nodes"`
	Edges   []topologyEdge `json:"edges"`
}

// ── State store namespaces (mirrored from service packages) ────────────────
// These are deliberately re-declared rather than imported so the topology
// handler has no compile-time coupling to individual service packages.

const (
	tNsBuckets       = "s3:buckets"
	tNsNotifications = "s3:notifications"
	tNsQueues        = "sqs:queues"
	tNsMessages      = "sqs:messages"
	tNsTopics        = "sns:topics"
	tNsSubscriptions = "sns:subscriptions"
	tNsTables        = "dynamodb:tables"
	tNsFunctions     = "lambda:functions"
	tNsESM           = "lambda:esm"
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

type tQueue struct {
	Name       string            `json:"name"`
	ARN        string            `json:"arn"`
	Attributes map[string]string `json:"attributes"`
}
type tRedrivePolicy struct {
	DeadLetterTargetArn string `json:"deadLetterTargetArn"`
	MaxReceiveCount     int    `json:"maxReceiveCount"`
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

type tFunction struct {
	Name     string `json:"name"`
	ARN      string `json:"arn"`
	LogGroup string `json:"log_group,omitempty"`
	ImageURI string `json:"image_uri,omitempty"`
	Package  string `json:"package_type,omitempty"`
}

type tESM struct {
	UUID           string           `json:"UUID"`
	FunctionArn    string           `json:"FunctionArn"`
	EventSourceArn string           `json:"EventSourceArn"`
	FilterCriteria *tFilterCriteria `json:"FilterCriteria,omitempty"`
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

// ── Handler ────────────────────────────────────────────────────────────────

func newTopologyHandler(cfg *config.Config, store state.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		regionFilter := r.URL.Query().Get("region") // "" = all regions

		// Scan all namespaces in parallel for minimum latency.
		type scanResult struct {
			ns   string
			data []state.KV
		}

		namespaces := []string{
			tNsBuckets, tNsNotifications, tNsQueues, tNsMessages,
			tNsTopics, tNsSubscriptions, tNsTables, tNsFunctions,
			tNsESM, tNsLogGroups, tNsPipes, tNsCFNStacks,
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

		results := make([]scanResult, len(namespaces))
		var wg sync.WaitGroup
		wg.Add(len(namespaces))
		for i, ns := range namespaces {
			go func(idx int, namespace string) {
				defer wg.Done()
				kvs, err := store.Scan(ctx, namespace, "")
				if err != nil {
					return // graceful degradation — namespace simply absent
				}
				results[idx] = scanResult{ns: namespace, data: kvs}
			}(i, ns)
		}
		wg.Wait()

		// Index results by namespace for easy lookup.
		byNS := make(map[string][]state.KV, len(namespaces))
		for _, sr := range results {
			if sr.data != nil {
				byNS[sr.ns] = sr.data
			}
		}

		// ── Build response ─────────────────────────────────────────────────
		resp := buildTopology(cfg, byNS, regionFilter)

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}
}

// buildTopology constructs the full topology graph from raw state store data.
// Extracted as a pure function to keep the handler thin and testable.
func buildTopology(cfg *config.Config, byNS map[string][]state.KV, regionFilter string) topologyResponse {
	defaultRegion := cfg.Region

	// ── Decode & collect nodes ─────────────────────────────────────────────
	regionSet := make(map[string]bool)
	nodeIndex := make(map[string]string) // node ID → region (for edge region tagging)

	var nodes []topologyNode

	addNode := func(n topologyNode) {
		if regionFilter != "" && n.Region != regionFilter {
			return
		}
		regionSet[n.Region] = true
		nodeIndex[n.ID] = n.Region
		nodes = append(nodes, n)
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
		addNode(topologyNode{
			ID:      b.Region + "::s3::" + b.Name,
			Service: "s3",
			Label:   b.Name,
			Region:  b.Region,
		})
	}

	// SQS queues (with message counts)
	msgCounts := countSQSMessages(byNS[tNsMessages])
	type queueMeta struct {
		name   string
		region string
	}
	queueIndex := make(map[string]queueMeta) // queue name → meta

	for _, kv := range byNS[tNsQueues] {
		var q tQueue
		if json.Unmarshal([]byte(kv.Value), &q) != nil {
			continue
		}
		region := regionFromARN(q.ARN, defaultRegion)
		queueIndex[q.Name] = queueMeta{name: q.Name, region: region}
		counts := msgCounts[q.Name]
		visible := counts.visible
		inFlight := counts.inFlight
		addNode(topologyNode{
			ID:                                    region + "::sqs::" + q.Name,
			Service:                               "sqs",
			Label:                                 q.Name,
			Region:                                region,
			ApproximateNumberOfMessages:           &visible,
			ApproximateNumberOfMessagesNotVisible: &inFlight,
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
		addNode(topologyNode{
			ID:      region + "::sns::" + t.Name,
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
		addNode(topologyNode{
			ID:            region + "::dynamodb::" + t.TableName,
			Service:       "dynamodb",
			Label:         t.TableName,
			Region:        region,
			StreamEnabled: &streamEnabled,
		})
	}

	// Lambda functions
	funcIndex := make(map[string]string) // function name → region
	type funcMeta struct {
		region   string
		logGroup string
		imageURI string
		pkgType  string
	}
	funcMetas := make(map[string]funcMeta) // function name → meta
	for _, kv := range byNS[tNsFunctions] {
		var fn tFunction
		if json.Unmarshal([]byte(kv.Value), &fn) != nil {
			continue
		}
		region := regionFromARN(fn.ARN, defaultRegion)
		funcIndex[fn.Name] = region
		funcMetas[fn.Name] = funcMeta{region: region, logGroup: fn.LogGroup, imageURI: fn.ImageURI, pkgType: fn.Package}
		addNode(topologyNode{
			ID:      region + "::lambda::" + fn.Name,
			Service: "lambda",
			Label:   fn.Name,
			Region:  region,
		})
	}

	// CloudWatch Logs groups
	// logGroupRegions maps group name → set of regions where the group exists.
	// A group can exist in multiple regions (e.g. created by CFN in one region
	// and accidentally duplicated in another); we track all so edges can
	// prefer the same-region copy.
	logGroupRegions := make(map[string][]string) // group name → regions
	for _, kv := range byNS[tNsLogGroups] {
		var lg tLogGroup
		if json.Unmarshal([]byte(kv.Value), &lg) != nil {
			continue
		}
		region := regionFromARN(lg.ARN, defaultRegion)
		logGroupRegions[lg.Name] = append(logGroupRegions[lg.Name], region)
		addNode(topologyNode{
			ID:      region + "::logs::" + lg.Name,
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
		region := defaultRegion
		if r, _ := serviceutil.SplitRegionKey(kv.Key); r != "" {
			region = r
		}
		addNode(topologyNode{
			ID:      region + "::ec2::" + inst.InstanceID,
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
		region := defaultRegion
		if i := strings.IndexByte(kv.Key, '/'); i > 0 {
			region = kv.Key[:i]
		}
		subnetCount := subnetCountByVpc[vpc.VpcID]
		_, hasIGW := igwAttachmentsByVpc[vpc.VpcID]
		addNode(topologyNode{
			ID:                 region + "::vpc::" + vpc.VpcID,
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
		region := defaultRegion
		if i := strings.IndexByte(kv.Key, '/'); i > 0 {
			region = kv.Key[:i]
		}
		// Find the attached VPC (if any).
		var attachedVpc string
		for _, att := range igw.Attachments {
			if att.VpcID != "" {
				attachedVpc = att.VpcID
				break
			}
		}
		addNode(topologyNode{
			ID:            region + "::igw::" + igw.InternetGatewayID,
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
		addNode(topologyNode{
			ID:              region + "::ecs::" + c.Name,
			Service:         "ecs",
			Label:           c.Name,
			Region:          region,
			Status:          c.Status,
			ECSResourceType: topologyECSCluster,
			ClusterName:     c.Name,
		})
	}

	// ECS services (nodes + edges to clusters)
	ecsServiceTaskDefs := make(map[string]string)
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
		ecsServiceTaskDefs[region+"::ecs-service::"+clusterName+"/"+svc.Name] = svc.TaskDefinition
		addNode(topologyNode{
			ID:              region + "::ecs-service::" + clusterName + "/" + svc.Name,
			Service:         "ecs",
			Label:           svc.Name,
			Region:          region,
			Status:          svc.Status,
			ECSResourceType: topologyECSService,
			ClusterName:     clusterName,
			DesiredCount:    &desiredCount,
			RunningCount:    &runningCount,
		})
	}

	// ECS tasks (nodes)
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
		addNode(topologyNode{
			ID:              region + "::ecs-task::" + clusterName + "/" + taskID,
			Service:         "ecs",
			Label:           taskID,
			Region:          region,
			Status:          task.LastStatus,
			ECSResourceType: topologyECSTask,
			ClusterName:     clusterName,
			TaskID:          taskID,
		})
	}

	// ECR repositories
	ecrRepoNodeByImageRef := make(map[string]string)
	for _, kv := range byNS[tNsECRRepos] {
		var repo tECRRepository
		if json.Unmarshal([]byte(kv.Value), &repo) != nil {
			continue
		}
		region := regionFromARN(repo.RepositoryArn, defaultRegion)
		if region == defaultRegion {
			if keyRegion, _ := splitRegionKey(kv.Key); keyRegion != "" {
				region = keyRegion
			}
		}
		nodeID := region + "::ecr::" + repo.RepositoryName
		addNode(topologyNode{
			ID:            nodeID,
			Service:       "ecr",
			Label:         repo.RepositoryName,
			Region:        region,
			RepositoryUri: repo.RepositoryURI,
		})
		if ref := normalizeContainerImageRef(repo.RepositoryURI); ref != "" {
			ecrRepoNodeByImageRef[ref] = nodeID
		}
	}

	// ECS task definitions keyed for repository-consumer edge derivation.
	ecsTaskDefImages := make(map[string][]string)
	for _, kv := range byNS[tNsECSTaskDefs] {
		var td tECSTaskDefinition
		if json.Unmarshal([]byte(kv.Value), &td) != nil {
			continue
		}
		images := make([]string, 0, len(td.ContainerDefinitions))
		for _, container := range td.ContainerDefinitions {
			if ref := normalizeContainerImageRef(container.Image); ref != "" {
				images = append(images, ref)
			}
		}
		if len(images) > 0 {
			ecsTaskDefImages[td.TaskDefinitionArn] = images
		}
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
		region := defaultRegion
		if i := strings.IndexByte(kv.Key, '/'); i > 0 {
			region = kv.Key[:i]
		}
		addNode(topologyNode{
			ID:      region + "::rds::" + db.ID,
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
		region := defaultRegion
		if i := strings.IndexByte(kv.Key, '/'); i > 0 {
			region = kv.Key[:i]
		}
		addNode(topologyNode{
			ID:      region + "::elasticache::" + c.ID,
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
		region := defaultRegion
		if i := strings.IndexByte(kv.Key, '/'); i > 0 {
			region = kv.Key[:i]
		}
		addNode(topologyNode{
			ID:      region + "::elasticache::" + rg.ID,
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
		region := defaultRegion
		if i := strings.IndexByte(kv.Key, '/'); i > 0 {
			region = kv.Key[:i]
		}
		addNode(topologyNode{
			ID:      region + "::elasticache::" + c.Name,
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
		region := defaultRegion
		if i := strings.IndexByte(kv.Key, '/'); i > 0 {
			region = kv.Key[:i]
		}
		label := c.ClusterName
		if label == "" {
			label = c.ClusterArn
		}
		addNode(topologyNode{
			ID:      region + "::msk::" + c.ClusterArn,
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
		region := defaultRegion
		if i := strings.IndexByte(kv.Key, '/'); i > 0 {
			region = kv.Key[:i]
		}
		addNode(topologyNode{
			ID:      region + "::efs::" + fs.ID,
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
		region := defaultRegion
		if i := strings.IndexByte(kv.Key, '/'); i > 0 {
			region = kv.Key[:i]
		}
		addNode(topologyNode{
			ID:      region + "::efs::" + ap.ID,
			Service: "efs",
			Label:   ap.ID,
			Region:  region,
			Status:  ap.Status,
		})
	}

	// API Gateway REST APIs (v1)
	// Count resources and stages per API for metadata.
	// Keys are region-scoped: "{region}/{apiID}/{resourceID}".
	v1ResourceCount := make(map[string]int)      // apiID → count
	v1LambdaTargets := make(map[string][]string) // apiID → []functionName
	for _, kv := range byNS[tNsAPIResources] {
		// Strip region prefix, then extract apiID from "apiID/resourceID".
		_, rest := splitRegionKey(kv.Key)
		apiID := rest
		if i := strings.IndexByte(rest, '/'); i > 0 {
			apiID = rest[:i]
		}
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
		// Strip region prefix, then extract apiID from "apiID/stageName".
		_, rest := splitRegionKey(kv.Key)
		apiID := rest
		if i := strings.IndexByte(rest, '/'); i > 0 {
			apiID = rest[:i]
		}
		v1StageCount[apiID]++
	}

	restAPIIndex := make(map[string]string) // apiID → region
	for _, kv := range byNS[tNsRestAPIs] {
		var api tRestAPI
		if json.Unmarshal([]byte(kv.Value), &api) != nil {
			continue
		}
		// Extract region from key: "{region}/{apiID}".
		keyRegion, _ := splitRegionKey(kv.Key)
		region := defaultRegion
		if keyRegion != "" {
			region = keyRegion
		}
		routes := v1ResourceCount[api.ID]
		stages := v1StageCount[api.ID]
		restAPIIndex[api.ID] = region
		addNode(topologyNode{
			ID:           region + "::apigateway::" + api.ID,
			Service:      "apigateway",
			Label:        api.Name,
			Region:       region,
			ProtocolType: "REST",
			RouteCount:   &routes,
			StageCount:   &stages,
		})
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
		_, rest := splitRegionKey(kv.Key)
		v2IntegIndex[rest] = &integ
	}

	for _, kv := range byNS[tNsV2Routes] {
		_, rest := splitRegionKey(kv.Key)
		apiID := rest
		if i := strings.IndexByte(rest, '/'); i > 0 {
			apiID = rest[:i]
		}
		v2RouteCount[apiID]++

		// Resolve route target → integration → Lambda.
		var route tRouteV2
		if json.Unmarshal([]byte(kv.Value), &route) != nil {
			continue
		}
		if strings.HasPrefix(route.Target, "integrations/") {
			integID := strings.TrimPrefix(route.Target, "integrations/")
			integKey := apiID + "/" + integID
			if integ, ok := v2IntegIndex[integKey]; ok {
				if (integ.IntegrationType == "AWS_PROXY") && integ.IntegrationURI != "" {
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
		_, rest := splitRegionKey(kv.Key)
		apiID := rest
		if i := strings.IndexByte(rest, '/'); i > 0 {
			apiID = rest[:i]
		}
		v2StageCount[apiID]++
	}

	v2APIIndex := make(map[string]string) // apiID → region
	for _, kv := range byNS[tNsV2APIs] {
		var api tAPIV2
		if json.Unmarshal([]byte(kv.Value), &api) != nil {
			continue
		}
		// Extract region from key: "{region}/{apiID}".
		keyRegion, _ := splitRegionKey(kv.Key)
		region := defaultRegion
		if keyRegion != "" {
			region = keyRegion
		}
		routes := v2RouteCount[api.ApiID]
		stages := v2StageCount[api.ApiID]
		v2APIIndex[api.ApiID] = region
		addNode(topologyNode{
			ID:           region + "::apigateway::" + api.ApiID,
			Service:      "apigateway",
			Label:        api.Name,
			Region:       region,
			ProtocolType: api.ProtocolType,
			RouteCount:   &routes,
			StageCount:   &stages,
		})
	}

	// CloudFront distributions
	// Index to track what S3 origins each distribution references.
	type cfDistInfo struct {
		nodeID    string
		region    string
		s3Origins []string // bucket names extracted from S3 origin domains
	}
	var cfDists []cfDistInfo

	for _, kv := range byNS[tNsCFDistributions] {
		// Only include distribution records (prefixed with dist:).
		if !strings.HasPrefix(kv.Key, "dist:") {
			continue
		}
		var dist tCFDistribution
		if json.Unmarshal([]byte(kv.Value), &dist) != nil {
			continue
		}
		if dist.ID == "" {
			continue
		}
		// Extract region from key (format: "us-east-1/dist:E1234567890ABC").
		region := defaultRegion
		if i := strings.IndexByte(kv.Key, '/'); i > 0 {
			region = kv.Key[:i]
		}
		origins := dist.Config.Origins.Quantity
		label := dist.ID
		if dist.Config.Comment != "" {
			label = dist.Config.Comment
		}
		nodeID := region + "::cloudfront::" + dist.ID
		addNode(topologyNode{
			ID:          nodeID,
			Service:     "cloudfront",
			Label:       label,
			Region:      region,
			Status:      dist.Status,
			DomainName:  dist.DomainName,
			OriginCount: &origins,
		})

		// Collect S3 origin bucket names for edge building.
		info := cfDistInfo{nodeID: nodeID, region: region}
		for _, o := range dist.Config.Origins.Items {
			dn := o.DomainName
			if strings.HasSuffix(dn, ".s3.amazonaws.com") || (strings.Contains(dn, ".s3.") && strings.HasSuffix(dn, ".amazonaws.com")) {
				dn = strings.TrimSuffix(dn, ".amazonaws.com")
				if idx := strings.Index(dn, ".s3"); idx > 0 {
					info.s3Origins = append(info.s3Origins, dn[:idx])
				}
			}
		}
		cfDists = append(cfDists, info)
	}

	// WAFv2 Web ACLs. These nodes represent stored control-plane metadata;
	// Overcast does not evaluate their rules against application traffic.
	for _, kv := range byNS[tNsWAFWebACLs] {
		var acl tWAFWebACL
		if json.Unmarshal([]byte(kv.Value), &acl) != nil || acl.ID == "" {
			continue
		}
		keyRegion, _ := splitRegionKey(kv.Key)
		region := regionFromARN(acl.ARN, keyRegion)
		if region == "" {
			region = defaultRegion
		}
		label := acl.Name
		if label == "" {
			label = acl.ID
		}
		ruleCount := len(acl.Rules)
		addNode(topologyNode{
			ID:        region + "::waf::" + acl.ID,
			Service:   "waf",
			Label:     label,
			Region:    region,
			Scope:     acl.Scope,
			RuleCount: &ruleCount,
		})
	}

	// AppSync GraphQL APIs
	// The "appsync" namespace stores all sub-resources with different key prefixes.
	// API records: "region/api:APIID" → JSON
	// DataSource records: "region/ds:APIID:NAME" → JSON
	type appsyncAPIInfo struct {
		nodeID string
		region string
		apiID  string
	}
	var appsyncAPIs []appsyncAPIInfo

	// Count data sources and resolvers per API for metadata display.
	appsyncDSCount := make(map[string]int)       // apiID → count
	appsyncResolverCount := make(map[string]int) // apiID → count
	// Collect data source records for edge building (Lambda/DynamoDB targets).
	type appsyncDSInfo struct {
		apiID  string
		region string
		ds     tAppSyncDataSource
	}
	var appsyncDataSources []appsyncDSInfo

	for _, kv := range byNS[tNsAppSync] {
		// Split region from key: "us-east-1/api:xxx"
		region, rest := splitRegionKey(kv.Key)
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
			nodeID := region + "::appsync::" + api.ApiId
			appsyncAPIs = append(appsyncAPIs, appsyncAPIInfo{
				nodeID: nodeID,
				region: region,
				apiID:  api.ApiId,
			})
			addNode(topologyNode{
				ID:                 nodeID,
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
			// Extract apiID from key format "ds:APIID:DSNAME"
			parts := strings.SplitN(strings.TrimPrefix(rest, "ds:"), ":", 2)
			if len(parts) < 2 {
				continue
			}
			apiID := parts[0]
			appsyncDSCount[apiID]++
			appsyncDataSources = append(appsyncDataSources, appsyncDSInfo{
				apiID:  apiID,
				region: region,
				ds:     ds,
			})
		case strings.HasPrefix(rest, "resolver:"):
			// Count resolvers per API. Key format: "resolver:APIID:TYPE:FIELD"
			parts := strings.SplitN(strings.TrimPrefix(rest, "resolver:"), ":", 2)
			if len(parts) >= 1 {
				appsyncResolverCount[parts[0]]++
			}
		}
	}
	// Backfill the counts into already-created nodes.
	for _, info := range appsyncAPIs {
		dsCount := appsyncDSCount[info.apiID]
		resolverCount := appsyncResolverCount[info.apiID]
		for i := range nodes {
			if nodes[i].ID == info.nodeID {
				nodes[i].DataSourceCount = &dsCount
				nodes[i].ResolverCount = &resolverCount
				break
			}
		}
	}

	// ── Cognito User Pools ─────────────────────────────────────────────────
	for _, kv := range byNS[tNsCognitoPools] {
		var p tCognitoPool
		if json.Unmarshal([]byte(kv.Value), &p) != nil {
			continue
		}
		region, _ := splitRegionKey(kv.Key)
		if region == "" {
			region = defaultRegion
		}
		addNode(topologyNode{
			ID:      region + "::cognito::" + p.ID,
			Service: "cognito",
			Label:   p.Name,
			Region:  region,
		})
	}

	// ── Build edges ────────────────────────────────────────────────────────
	var edges []topologyEdge

	// resolveNodeID returns a node ID present in nodeIndex.  It first tries
	// the candidate as-is; if that fails it scans for any node whose ID ends
	// with the "::service::name" suffix (i.e. same resource in a different
	// region).  Returns "" if no match is found.
	resolveNodeID := func(candidate string) string {
		if _, ok := nodeIndex[candidate]; ok {
			return candidate
		}
		// Extract "service::name" portion (everything after the first "::").
		if idx := strings.Index(candidate, "::"); idx >= 0 {
			suffix := candidate[idx:] // e.g. "::lambda::my-func"
			for nid := range nodeIndex {
				if strings.HasSuffix(nid, suffix) {
					return nid
				}
			}
		}
		return ""
	}

	addEdge := func(e topologyEdge) {
		// Only emit edges where both endpoints exist in the node set.
		_, srcOK := nodeIndex[e.Source]
		_, tgtOK := nodeIndex[e.Target]
		if srcOK && tgtOK {
			e.SourceRegion = nodeIndex[e.Source]
			e.TargetRegion = nodeIndex[e.Target]
			edges = append(edges, e)
		}
	}

	// S3 → SQS / Lambda notification edges
	for _, kv := range byNS[tNsNotifications] {
		bucketName := kv.Key
		var nc tNotificationConfig
		if json.Unmarshal([]byte(kv.Value), &nc) != nil {
			continue
		}
		// Find bucket's region
		bucketRegion := defaultRegion
		if _, ok := nodeIndex[defaultRegion+"::s3::"+bucketName]; ok {
			bucketRegion = defaultRegion
		} else {
			// Scan for any region prefix matching this bucket
			for nid, r := range nodeIndex {
				if strings.HasSuffix(nid, "::s3::"+bucketName) {
					bucketRegion = r
					break
				}
			}
		}
		srcID := bucketRegion + "::s3::" + bucketName
		for _, qc := range nc.QueueConfigurations {
			qName := nameFromARNSuffix(qc.ARN)
			qRegion := regionFromARN(qc.ARN, defaultRegion)
			tgtID := qRegion + "::sqs::" + qName
			addEdge(topologyEdge{
				ID:     "notif::" + srcID + "→" + tgtID,
				Source: srcID,
				Target: tgtID,
				Type:   "notification",
			})
		}
		for _, lc := range nc.LambdaConfigurations {
			fnName := lambdaNameFromARN(lc.ARN)
			fnRegion := regionFromARN(lc.ARN, defaultRegion)
			tgtID := fnRegion + "::lambda::" + fnName
			addEdge(topologyEdge{
				ID:     "notif::" + srcID + "→" + tgtID,
				Source: srcID,
				Target: tgtID,
				Type:   "notification",
			})
		}
	}

	// SQS → SQS DLQ edges (from RedrivePolicy)
	for _, kv := range byNS[tNsQueues] {
		var q tQueue
		if json.Unmarshal([]byte(kv.Value), &q) != nil {
			continue
		}
		rpRaw, ok := q.Attributes["RedrivePolicy"]
		if !ok || rpRaw == "" {
			continue
		}
		var rp tRedrivePolicy
		if json.Unmarshal([]byte(rpRaw), &rp) != nil {
			continue
		}
		srcRegion := regionFromARN(q.ARN, defaultRegion)
		dlqName := nameFromARNSuffix(rp.DeadLetterTargetArn)
		dlqRegion := regionFromARN(rp.DeadLetterTargetArn, defaultRegion)
		srcID := srcRegion + "::sqs::" + q.Name
		tgtID := dlqRegion + "::sqs::" + dlqName
		label := "DLQ"
		if rp.MaxReceiveCount > 0 {
			label = fmt.Sprintf("DLQ (max %d)", rp.MaxReceiveCount)
		}
		addEdge(topologyEdge{
			ID:     "dlq::" + srcID + "→" + tgtID,
			Source: srcID,
			Target: tgtID,
			Type:   "dlq",
			Label:  label,
		})
	}

	// Lambda → CloudWatch Logs edges
	for fnName, meta := range funcMetas {
		// Use the function's custom log group, or fall back to the AWS convention.
		logGroup := meta.logGroup
		if logGroup == "" {
			logGroup = "/aws/lambda/" + fnName
		}
		if regions, ok := logGroupRegions[logGroup]; ok && len(regions) > 0 {
			// Prefer the log group in the same region as the function.
			lgRegion := regions[0]
			for _, r := range regions {
				if r == meta.region {
					lgRegion = r
					break
				}
			}
			srcID := meta.region + "::lambda::" + fnName
			tgtID := lgRegion + "::logs::" + logGroup
			addEdge(topologyEdge{
				ID:     "logs::" + srcID + "→" + tgtID,
				Source: srcID,
				Target: tgtID,
				Type:   "logs",
			})
		}
	}

	// ECR → Lambda image-function edges.
	for fnName, meta := range funcMetas {
		if !strings.EqualFold(meta.pkgType, "Image") || strings.TrimSpace(meta.imageURI) == "" {
			continue
		}
		repoNodeID, ok := ecrRepoNodeByImageRef[normalizeContainerImageRef(meta.imageURI)]
		if !ok {
			continue
		}
		addEdge(topologyEdge{
			ID:     "ecr-lambda::" + repoNodeID + "→" + meta.region + "::lambda::" + fnName,
			Source: repoNodeID,
			Target: meta.region + "::lambda::" + fnName,
			Type:   "container-image",
		})
	}

	// ECR → ECS service edges via stored task definition container images.
	for serviceNodeID, taskDefArn := range ecsServiceTaskDefs {
		for _, imageRef := range ecsTaskDefImages[taskDefArn] {
			repoNodeID, ok := ecrRepoNodeByImageRef[imageRef]
			if !ok {
				continue
			}
			addEdge(topologyEdge{
				ID:     "ecr-ecs::" + repoNodeID + "→" + serviceNodeID,
				Source: repoNodeID,
				Target: serviceNodeID,
				Type:   "container-image",
			})
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
		srcID := topicRegion + "::sns::" + sub.TopicName
		switch strings.ToLower(sub.Protocol) {
		case "sqs":
			qName := sub.QueueName
			if qName == "" {
				qName = nameFromARNSuffix(sub.Endpoint)
			}
			qRegion := regionFromARN(sub.Endpoint, defaultRegion)
			tgtID := qRegion + "::sqs::" + qName
			addEdge(topologyEdge{
				ID:     "sub::" + srcID + "→" + tgtID,
				Source: srcID,
				Target: tgtID,
				Type:   "subscription",
			})
		case "lambda":
			fnName := lambdaNameFromARN(sub.Endpoint)
			fnRegion := regionFromARN(sub.Endpoint, defaultRegion)
			tgtID := fnRegion + "::lambda::" + fnName
			addEdge(topologyEdge{
				ID:     "sub::" + srcID + "→" + tgtID,
				Source: srcID,
				Target: tgtID,
				Type:   "subscription",
			})
		}
	}

	// Lambda ESM edges (SQS → Lambda, DynamoDB → Lambda)
	for _, kv := range byNS[tNsESM] {
		var esm tESM
		if json.Unmarshal([]byte(kv.Value), &esm) != nil {
			continue
		}
		fnName := lambdaNameFromARN(esm.FunctionArn)
		fnRegion := regionFromARN(esm.FunctionArn, defaultRegion)
		tgtID := resolveNodeID(fnRegion + "::lambda::" + fnName)
		if tgtID == "" {
			continue
		}
		srcArn := esm.EventSourceArn
		if strings.Contains(srcArn, ":sqs:") {
			qName := nameFromARNSuffix(srcArn)
			qRegion := regionFromARN(srcArn, defaultRegion)
			srcID := resolveNodeID(qRegion + "::sqs::" + qName)
			if srcID == "" {
				continue
			}
			addEdge(topologyEdge{
				ID:     "esm::" + srcID + "→" + tgtID,
				Source: srcID,
				Target: tgtID,
				Type:   "esm",
			})
		} else if strings.Contains(srcArn, ":dynamodb:") {
			tblName := tableNameFromStreamARN(srcArn)
			tblRegion := regionFromARN(srcArn, defaultRegion)
			srcID := resolveNodeID(tblRegion + "::dynamodb::" + tblName)
			if srcID == "" {
				continue
			}
			patterns := esmFilterPatterns(esm.FilterCriteria)
			if len(patterns) > 0 {
				filterID := tblRegion + "::esm-filter::" + esm.UUID
				addNode(topologyNode{
					ID:             filterID,
					Service:        "esm-filter",
					Label:          "ESM filter",
					Region:         tblRegion,
					ESMID:          esm.UUID,
					FunctionName:   fnName,
					EventSource:    tblName,
					SourceType:     "dynamodb",
					FilterPatterns: patterns,
				})
				addEdge(topologyEdge{
					ID:     "esm-filter-in::" + esm.UUID,
					Source: srcID,
					Target: filterID,
					Type:   "esm-filter",
					Label:  "filter",
				})
				addEdge(topologyEdge{
					ID:     "esm-filter-out::" + esm.UUID,
					Source: filterID,
					Target: tgtID,
					Type:   "esm",
					Label:  "matched",
				})
				continue
			}
			addEdge(topologyEdge{
				ID:     "esm::" + srcID + "→" + tgtID,
				Source: srcID,
				Target: tgtID,
				Type:   "esm",
			})
		}
	}

	// Pipes edges (DynamoDB → SQS)
	for _, kv := range byNS[tNsPipes] {
		var p tPipe
		if json.Unmarshal([]byte(kv.Value), &p) != nil {
			continue
		}
		srcRegion := regionFromARN(p.SourceArn, defaultRegion)
		tgtRegion := regionFromARN(p.TargetArn, defaultRegion)
		srcID := srcRegion + "::dynamodb::" + p.SourceName
		tgtID := tgtRegion + "::sqs::" + p.TargetName
		addEdge(topologyEdge{
			ID:     "pipe::" + srcRegion + "::" + p.Name,
			Source: srcID,
			Target: tgtID,
			Type:   "pipe",
			Label:  p.Name,
			State:  p.CurrentState,
		})
	}

	// ECS service → cluster edges
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
		srcID := region + "::ecs::" + clusterName
		tgtID := region + "::ecs-service::" + clusterName + "/" + svc.Name
		addEdge(topologyEdge{
			ID:     "ecs-svc::" + srcID + "→" + tgtID,
			Source: srcID,
			Target: tgtID,
			Type:   "ecs",
		})
	}

	// ECS task → cluster edges, task → service edges
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
		taskNodeID := region + "::ecs-task::" + clusterName + "/" + taskID

		// Task → service (if it belongs to one)
		if strings.HasPrefix(task.Group, "service:") {
			svcName := strings.TrimPrefix(task.Group, "service:")
			svcNodeID := region + "::ecs-service::" + clusterName + "/" + svcName
			addEdge(topologyEdge{
				ID:     "ecs-task::" + svcNodeID + "→" + taskNodeID,
				Source: svcNodeID,
				Target: taskNodeID,
				Type:   "ecs",
			})
		} else {
			// Orphan task → cluster directly
			clusterNodeID := region + "::ecs::" + clusterName
			addEdge(topologyEdge{
				ID:     "ecs-task::" + clusterNodeID + "→" + taskNodeID,
				Source: clusterNodeID,
				Target: taskNodeID,
				Type:   "ecs",
			})
		}
	}

	// IGW → VPC attachment edges
	for _, kv := range byNS[tNsInternetGateways] {
		var igw tIGW
		if json.Unmarshal([]byte(kv.Value), &igw) != nil {
			continue
		}
		region := defaultRegion
		if i := strings.IndexByte(kv.Key, '/'); i > 0 {
			region = kv.Key[:i]
		}
		for _, att := range igw.Attachments {
			if att.VpcID == "" {
				continue
			}
			srcID := region + "::igw::" + igw.InternetGatewayID
			tgtID := region + "::vpc::" + att.VpcID
			addEdge(topologyEdge{
				ID:     "igw-attach::" + srcID + "→" + tgtID,
				Source: srcID,
				Target: tgtID,
				Type:   "vpc-attachment",
			})
		}
	}

	// EC2 instance → VPC edges (instances that belong to a VPC)
	for _, kv := range byNS[tNsInstances] {
		var inst tInstance
		if json.Unmarshal([]byte(kv.Value), &inst) != nil || inst.VpcID == "" {
			continue
		}
		region := defaultRegion
		if r, _ := serviceutil.SplitRegionKey(kv.Key); r != "" {
			region = r
		}
		srcID := region + "::vpc::" + inst.VpcID
		tgtID := region + "::ec2::" + inst.InstanceID
		addEdge(topologyEdge{
			ID:     "vpc-member::" + srcID + "→" + tgtID,
			Source: srcID,
			Target: tgtID,
			Type:   "vpc-member",
		})
	}

	// EFS access point → file system edges.
	for _, kv := range byNS[tNsEFSAccessPoints] {
		var ap tEFSAccessPoint
		if json.Unmarshal([]byte(kv.Value), &ap) != nil || ap.ID == "" || ap.FileSystemID == "" {
			continue
		}
		region := defaultRegion
		if i := strings.IndexByte(kv.Key, '/'); i > 0 {
			region = kv.Key[:i]
		}
		srcID := region + "::efs::" + ap.ID
		tgtID := region + "::efs::" + ap.FileSystemID
		addEdge(topologyEdge{
			ID:     "efs-ap::" + srcID + "→" + tgtID,
			Source: srcID,
			Target: tgtID,
			Type:   "vpc-attachment",
			Label:  "access point",
		})
	}

	// CloudFormation stack ownership + intra-stack reference edges.
	// First pass: parse all stacks and index by ID for parent lookups.
	type parsedCFNStack struct {
		stack  tCFNStack
		region string
	}
	cfnStacksByID := make(map[string]parsedCFNStack)
	var cfnStacks []parsedCFNStack
	for _, kv := range byNS[tNsCFNStacks] {
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

	// Second pass: ownership tagging, reference edges, and nested-stack edges.
	for _, ps := range cfnStacks {
		stack := ps.stack
		stackRegion := ps.region

		// Build logical→physical node ID mapping for this stack.
		logicalToNodeID := make(map[string]string, len(stack.Resources))
		for _, res := range stack.Resources {
			if res.PhysicalID == "" {
				continue
			}
			nodeID := cfnResourceNodeID(res, stackRegion)
			if nodeID != "" {
				logicalToNodeID[res.LogicalID] = nodeID
			}
		}

		// Tag owned nodes with their stack name.
		for _, nodeID := range logicalToNodeID {
			for i := range nodes {
				if nodes[i].ID == nodeID {
					name := stack.StackName
					nodes[i].StackName = &name
					break
				}
			}
		}

		// Nested-stack edge: parent → child. Use the parent's physical
		// resource (child stack ARN) to find the child's stack name and
		// emit an edge between their stack group IDs.
		// Note: stack group IDs (stack::region::name) are phantom nodes
		// created by the frontend layout — they don't exist in the node
		// set, so we append directly to edges instead of using addEdge.
		if stack.ParentStackID != "" {
			if parent, ok := cfnStacksByID[stack.ParentStackID]; ok {
				parentRegion := parent.region
				srcID := "stack::" + parentRegion + "::" + parent.stack.StackName
				tgtID := "stack::" + stackRegion + "::" + stack.StackName
				edges = append(edges, topologyEdge{
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

	// API Gateway → Lambda edges (REST v1)
	for apiID, fnNames := range v1LambdaTargets {
		apiRegion := restAPIIndex[apiID]
		srcID := apiRegion + "::apigateway::" + apiID
		seen := make(map[string]bool)
		for _, fnName := range fnNames {
			if seen[fnName] {
				continue
			}
			seen[fnName] = true
			fnRegion, ok := funcIndex[fnName]
			if !ok {
				fnRegion = defaultRegion
			}
			tgtID := fnRegion + "::lambda::" + fnName
			addEdge(topologyEdge{
				ID:     "apigw::" + srcID + "→" + tgtID,
				Source: srcID,
				Target: tgtID,
				Type:   "apigw-integration",
			})
		}
	}

	// API Gateway → Lambda edges (HTTP v2)
	for apiID, fnNames := range v2LambdaTargets {
		apiRegion := v2APIIndex[apiID]
		srcID := apiRegion + "::apigateway::" + apiID
		seen := make(map[string]bool)
		for _, fnName := range fnNames {
			if seen[fnName] {
				continue
			}
			seen[fnName] = true
			fnRegion, ok := funcIndex[fnName]
			if !ok {
				fnRegion = defaultRegion
			}
			tgtID := fnRegion + "::lambda::" + fnName
			addEdge(topologyEdge{
				ID:     "apigw::" + srcID + "→" + tgtID,
				Source: srcID,
				Target: tgtID,
				Type:   "apigw-integration",
			})
		}
	}

	// Collect ordered region list
	regions := make([]string, 0, len(regionSet))
	for r := range regionSet {
		regions = append(regions, r)
	}
	// If region filter was requested but no resources exist, still include it
	if regionFilter != "" && !regionSet[regionFilter] {
		regions = append(regions, regionFilter)
	}

	// CloudFront → S3 origin edges
	for _, cf := range cfDists {
		for _, bucket := range cf.s3Origins {
			// Try to find the S3 bucket node in any region.
			targetID := ""
			for nid := range nodeIndex {
				if strings.HasSuffix(nid, "::s3::"+bucket) {
					targetID = nid
					break
				}
			}
			if targetID != "" {
				addEdge(topologyEdge{
					ID:     cf.nodeID + "->>" + targetID,
					Source: cf.nodeID,
					Target: targetID,
					Type:   "origin",
					Label:  "S3 origin",
				})
			}
		}
	}

	// AppSync → Lambda / DynamoDB data source edges
	for _, dsInfo := range appsyncDataSources {
		// Find the AppSync API node for this data source.
		srcID := dsInfo.region + "::appsync::" + dsInfo.apiID
		switch dsInfo.ds.Type {
		case "AWS_LAMBDA":
			var cfg struct {
				LambdaFunctionArn string `json:"lambdaFunctionArn"`
			}
			if json.Unmarshal(dsInfo.ds.LambdaConfig, &cfg) != nil || cfg.LambdaFunctionArn == "" {
				continue
			}
			fnName := lambdaNameFromARN(cfg.LambdaFunctionArn)
			fnRegion := regionFromARN(cfg.LambdaFunctionArn, dsInfo.region)
			tgtID := fnRegion + "::lambda::" + fnName
			addEdge(topologyEdge{
				ID:     "appsync-ds::" + srcID + "→" + tgtID,
				Source: srcID,
				Target: tgtID,
				Type:   "appsync-datasource",
				Label:  dsInfo.ds.Name,
			})
		case "AMAZON_DYNAMODB":
			var cfg struct {
				TableName string `json:"tableName"`
			}
			if json.Unmarshal(dsInfo.ds.DynamodbConfig, &cfg) != nil || cfg.TableName == "" {
				continue
			}
			tblRegion := dsInfo.region
			tgtID := tblRegion + "::dynamodb::" + cfg.TableName
			addEdge(topologyEdge{
				ID:     "appsync-ds::" + srcID + "→" + tgtID,
				Source: srcID,
				Target: tgtID,
				Type:   "appsync-datasource",
				Label:  dsInfo.ds.Name,
			})
		}
	}

	if nodes == nil {
		nodes = []topologyNode{}
	}
	if edges == nil {
		edges = []topologyEdge{}
	}

	return topologyResponse{
		Regions: regions,
		Nodes:   nodes,
		Edges:   edges,
	}
}

func esmFilterPatterns(fc *tFilterCriteria) []string {
	if fc == nil || len(fc.Filters) == 0 {
		return nil
	}
	out := make([]string, 0, len(fc.Filters))
	for _, f := range fc.Filters {
		if f.Pattern != "" {
			out = append(out, f.Pattern)
		}
	}
	return out
}

// ── ARN helpers ────────────────────────────────────────────────────────────

// regionFromARN extracts the region (segment 3) from a standard AWS ARN.
// Returns fallback if the ARN is malformed or the region segment is empty.
func regionFromARN(arn, fallback string) string {
	// arn:aws:service:REGION:account:resource
	parts := strings.SplitN(arn, ":", 6)
	if len(parts) >= 4 && parts[3] != "" {
		return parts[3]
	}
	return fallback
}

// splitRegionKey extracts the region prefix and remaining key from a
// region-scoped store key of the form "us-east-1/api:xxx".
// Returns ("", key) if no "/" separator is present.
func splitRegionKey(key string) (region, rest string) {
	if i := strings.IndexByte(key, '/'); i > 0 {
		return key[:i], key[i+1:]
	}
	return "", key
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

func normalizeContainerImageRef(image string) string {
	image = strings.TrimSpace(image)
	if image == "" {
		return ""
	}
	image = strings.TrimPrefix(strings.TrimPrefix(image, "https://"), "http://")
	if idx := strings.IndexByte(image, '@'); idx >= 0 {
		image = image[:idx]
	}
	lastSlash := strings.LastIndexByte(image, '/')
	lastColon := strings.LastIndexByte(image, ':')
	if lastColon > lastSlash {
		image = image[:lastColon]
	}
	return image
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

// cfnResourceNodeID maps a CloudFormation resource to the topology node ID
// that represents it. Returns "" for resource types not present in the graph.
func cfnResourceNodeID(res tCFNResource, defaultRegion string) string {
	switch {
	case strings.HasPrefix(res.Type, "AWS::S3::Bucket"):
		return defaultRegion + "::s3::" + res.PhysicalID
	case res.Type == "AWS::SQS::Queue":
		name := nameFromARNSuffix(res.PhysicalID)
		region := regionFromARN(res.PhysicalID, defaultRegion)
		return region + "::sqs::" + name
	case res.Type == "AWS::SNS::Topic":
		name := nameFromARNSuffix(res.PhysicalID)
		region := regionFromARN(res.PhysicalID, defaultRegion)
		return region + "::sns::" + name
	case res.Type == "AWS::DynamoDB::Table":
		name := res.PhysicalID
		if i := strings.LastIndex(name, "/"); i >= 0 {
			name = name[i+1:]
		}
		region := regionFromARN(res.PhysicalID, defaultRegion)
		return region + "::dynamodb::" + name
	case res.Type == "AWS::Lambda::Function":
		name := lambdaNameFromARN(res.PhysicalID)
		region := regionFromARN(res.PhysicalID, defaultRegion)
		return region + "::lambda::" + name
	case res.Type == "AWS::Logs::LogGroup":
		return defaultRegion + "::logs::" + res.PhysicalID
	case res.Type == "AWS::EC2::Instance":
		return defaultRegion + "::ec2::" + res.PhysicalID
	case res.Type == "AWS::ECS::Cluster":
		name := nameFromARNSuffix(res.PhysicalID)
		return defaultRegion + "::ecs::" + name
	case res.Type == "AWS::ECS::Service":
		// PhysicalID is the service ARN: arn:aws:ecs:region:acct:service/cluster/name
		parts := strings.SplitN(res.PhysicalID, "/", 3)
		if len(parts) == 3 {
			region := regionFromARN(res.PhysicalID, defaultRegion)
			return region + "::ecs-service::" + parts[1] + "/" + parts[2]
		}
		return ""
	case res.Type == "AWS::RDS::DBInstance":
		return defaultRegion + "::rds::" + res.PhysicalID
	case res.Type == "AWS::ElastiCache::CacheCluster" || res.Type == "AWS::ElastiCache::ServerlessCache" || res.Type == "AWS::ElastiCache::ReplicationGroup":
		return defaultRegion + "::elasticache::" + res.PhysicalID
	case res.Type == "AWS::ApiGateway::RestApi" || res.Type == "AWS::ApiGatewayV2::Api":
		return defaultRegion + "::apigateway::" + res.PhysicalID
	case res.Type == "AWS::Cognito::UserPool":
		return defaultRegion + "::cognito::" + res.PhysicalID
	case res.Type == "AWS::AppSync::GraphQLApi":
		return defaultRegion + "::appsync::" + res.PhysicalID
	case res.Type == "AWS::CloudFront::Distribution":
		return defaultRegion + "::cloudfront::" + res.PhysicalID
	case res.Type == "AWS::WAFv2::WebACL":
		id := res.PhysicalID
		if parts := strings.SplitN(id, "/", 2); len(parts) == 2 {
			id = parts[1]
		}
		return defaultRegion + "::waf::" + id
	default:
		return ""
	}
}

// ── SQS message counting ──────────────────────────────────────────────────

type sqsMessageCounts struct {
	visible  int
	inFlight int
}

// countSQSMessages scans the messages namespace and counts visible/in-flight
// messages per queue. Keys are "queueName/messageID".
func countSQSMessages(messageKVs []state.KV) map[string]sqsMessageCounts {
	counts := make(map[string]sqsMessageCounts)
	for _, kv := range messageKVs {
		// Keys are "region/queueName/messageID" (region-prefixed).
		parts := strings.SplitN(kv.Key, "/", 3)
		if len(parts) < 3 {
			continue
		}
		queueName := parts[1]
		c := counts[queueName]
		// We don't parse the full message just to count — check the
		// receipt_handle field to determine visibility.
		var msg struct {
			ReceiptHandle string `json:"receipt_handle"`
		}
		if err := json.Unmarshal([]byte(kv.Value), &msg); err != nil {
			continue
		}
		if msg.ReceiptHandle == "" {
			c.visible++
		} else {
			c.inFlight++
		}
		counts[queueName] = c
	}
	return counts
}
