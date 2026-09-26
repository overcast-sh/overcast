// Package topology builds the graph behind GET /_overcast/topology, the web
// console's system map.
//
// Each service that puts resources on the map implements Contributor and
// writes its own nodes and edges into a Graph. An edge names its endpoints by
// Ref (a node ID, or an alias such as a CloudFormation physical ID), so a
// service never reads another service's state to draw a connection: Build
// merges every contributor's Graph and resolves the refs in one place.
//
// The package imports no service and nothing from the router, so services and
// the router can both depend on it. The wire types below are rendered into
// web/src/types/api.gen.ts by cmd/tsgen; run `make generate-ts` after changing
// them.
package topology

// ECSResourceType says which kind of ECS resource a topology node is:
// one of the constants below. The Map page navigates differently for each
// (a task node links to its task detail, a service node to its service), so
// the values are a contract — cmd/tsgen renders them as the TypeScript union.
type ECSResourceType = string

const (
	ECSCluster ECSResourceType = "cluster"
	ECSService ECSResourceType = "service"
	ECSTask    ECSResourceType = "task"
)

// Node is one node of GET /_overcast/topology's graph. The fields
// after Region are per-service details, each populated only for the services
// named in its comment and omitted otherwise.
type Node struct {
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

	ECSResourceType ECSResourceType `json:"ecsResourceType,omitempty"` // ECS only — whether this node is a cluster, service, or task
	ClusterName     string          `json:"clusterName,omitempty"`     // ECS service/task owner, used for detail navigation
	TaskID          string          `json:"taskId,omitempty"`          // ECS task only — task UUID used by the task detail route
	DesiredCount    *int            `json:"desiredCount,omitempty"`    // ECS service only — configured task count
	RunningCount    *int            `json:"runningCount,omitempty"`    // ECS service only — currently running task count

	RecentQueries    []QueryRun       `json:"recentQueries,omitempty"`    // Athena workgroup only — its latest executions, newest first
	EngineState      string           `json:"engineState,omitempty"`      // Athena workgroup only — the query engine's state while it cannot run a query yet
	GlueResourceType GlueResourceType `json:"glueResourceType,omitempty"` // Glue only — whether this node is a database or a federated catalog
	Tables           []DataTable      `json:"tables,omitempty"`           // Glue database and S3 Tables bucket only — the tables it holds
}

// GlueResourceType says which kind of Data Catalog resource a Glue node is:
// one of the constants below. The Map page draws and routes each differently.
type GlueResourceType = string

const (
	GlueDatabase GlueResourceType = "database"
	GlueCatalog  GlueResourceType = "catalog"
)

// QueryRun is one Athena query execution, as a workgroup node lists it.
type QueryRun struct {
	ID string `json:"id"`
	// State is the execution's state: QUEUED, RUNNING, SUCCEEDED, FAILED or
	// CANCELLED.
	State string `json:"state"`
	// Query is the start of the SQL, on one line.
	Query string `json:"query"`
	// SubmittedAt and CompletedAt are Unix milliseconds; CompletedAt is zero
	// until the execution finishes.
	SubmittedAt int64 `json:"submittedAt"`
	CompletedAt int64 `json:"completedAt,omitempty"`
}

// DataTable is one table inside a Glue database or S3 Tables bucket node.
type DataTable struct {
	Name string `json:"name"`
	// Namespace is the S3 Tables namespace the table is in.
	Namespace string `json:"namespace,omitempty"`
	// ID is the S3 Tables table id, which its console route names.
	ID string `json:"id,omitempty"`
	// Format is what a Glue table holds: ICEBERG, PARQUET, ORC, AVRO, JSON,
	// CSV or VIEW, and empty when nothing on the table says.
	Format string `json:"format,omitempty"`
	// Location is where the table's files are: a Glue table's storage
	// location, an S3 Tables table's warehouse.
	Location string `json:"location,omitempty"`
	// Partitions is how many partitions a Glue table has.
	Partitions *int `json:"partitions,omitempty"`
	// Snapshots is how many snapshots an Iceberg table's current metadata
	// keeps, when that metadata could be read.
	Snapshots *int `json:"snapshots,omitempty"`
	// LastCommit is the Iceberg table's current snapshot.
	LastCommit *TableCommit `json:"lastCommit,omitempty"`
}

// TableCommit is an Iceberg snapshot, as the Map's latest-commit peek shows it.
type TableCommit struct {
	// SnapshotID is decimal: Iceberg's 64-bit ids do not survive a
	// JavaScript number.
	SnapshotID string `json:"snapshotId"`
	Operation  string `json:"operation,omitempty"`
	// AddedRecords and DeletedRecords are from the snapshot summary, when it
	// records them.
	AddedRecords   *int64 `json:"addedRecords,omitempty"`
	DeletedRecords *int64 `json:"deletedRecords,omitempty"`
	// CommittedAt is the snapshot's timestamp, in Unix milliseconds.
	CommittedAt int64 `json:"committedAt"`
}

// Edge is one connection of GET /_overcast/topology's graph.
type Edge struct {
	ID           string `json:"id"`
	Source       string `json:"source"`
	Target       string `json:"target"`
	Type         string `json:"type"`
	Label        string `json:"label,omitempty"`
	State        string `json:"state,omitempty"`
	SourceRegion string `json:"sourceRegion,omitempty"`
	TargetRegion string `json:"targetRegion,omitempty"`
}

// Response is the body of GET /_overcast/topology.
type Response struct {
	Regions []string `json:"regions"`
	Nodes   []Node   `json:"nodes"`
	Edges   []Edge   `json:"edges"`
}
