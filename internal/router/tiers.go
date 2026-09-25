package router

// EmulationTier describes how completely a service is emulated.
//
//   - "full"        — P1+P2 operations implemented; real SDK clients can use it.
//   - "partial"     — P1 operations implemented; basic workflows work.
//   - "inert"       — Full CRUD works, resources exist, but no side-effects or enforcement.
//   - "stub"        — Registered with only a minimal discovery or control-plane subset;
//     unsupported endpoints return 501 Not Implemented.
//   - "unsupported" — Not registered in Overcast; no backend handler exists.
type EmulationTier = string

const (
	TierFull        EmulationTier = "full"
	TierPartial     EmulationTier = "partial"
	TierInert       EmulationTier = "inert"
	TierStub        EmulationTier = "stub"
	TierUnsupported EmulationTier = "unsupported"
)

// ServiceTiers maps the canonical service name (as returned by Service.Name())
// to its current emulation tier. Update this whenever a service graduates
// from stub → partial → full.
//
// Every registered service must appear here: New falls back to TierStub for
// anything missing, so an omission silently reports a working service as
// "stub" in /_overcast/health. TestServiceTiers_coverEveryRegisteredService enforces it.
var ServiceTiers = map[string]EmulationTier{
	// Full — P1+P2 complete
	"s3":     TierFull,
	"sqs":    TierFull,
	"sns":    TierFull,
	"lambda": TierFull,

	// Partial — P1 complete
	"dynamodb":        TierPartial,
	"dynamodbstreams": TierPartial,
	"ses":             TierPartial,
	"secretsmanager":  TierPartial,
	"kinesis":         TierPartial,
	"pipes":           TierPartial,
	"logs":            TierPartial,
	"sts":             TierPartial,
	"kms":             TierPartial,
	"ssm":             TierPartial,
	"ec2":             TierPartial,
	"ecs":             TierPartial,
	"ecr":             TierPartial,
	"eks":             TierPartial,
	"rds":             TierPartial,
	"cloudformation":  TierPartial,
	"apigateway":      TierPartial,
	"cognito":         TierPartial,
	"scheduler":       TierPartial,
	"appconfigdata":   TierPartial,
	// stepfunctions interprets the Amazon States Language for real: all eight
	// state types, the optimized Lambda/SQS/SNS/DynamoDB/Step Functions Task
	// integrations, Retry/Catch, and a real execution history. What it cannot
	// interpret fails the execution loudly rather than passing through.
	"stepfunctions": TierPartial,
	// autoscaling groups really converge: the reconciler launches and
	// terminates EC2 instances, runs the lifecycle state machine, and
	// executes simple and step scaling policies. Launch templates, mixed
	// instances policies and target-tracking policies are refused, not
	// emulated — see docs/services/autoscaling.md.
	"autoscaling": TierPartial,
	// eventbridge is not inert: PutEvents evaluates every rule on the bus and
	// delivers matches in-process to Lambda, SQS, SNS, Step Functions,
	// Kinesis, Firehose, ECS and event-bus targets, honouring RetryPolicy and
	// dead-lettering to the target's DeadLetterConfig queue. Archives,
	// replays, connections and API destinations still return 501.
	"eventbridge": TierPartial,
	// athena runs queries for real on a Trino container started on the first
	// query, writes their results to S3 and runs Hive DDL against the Glue
	// Data Catalog. Without Docker it is inert: queries succeed with no rows.
	"athena": TierPartial,

	// Inert — full CRUD, resources stored, but no enforcement / side-effects
	"iam":        TierInert,
	"appsync":    TierInert,
	"cloudfront": TierInert,

	// Stub — minimal metadata/probe responses that unblock SDK or IaC workflows;
	// unsupported operations return 501.
	"waf":    TierStub,
	"shield": TierStub,

	// organizations stores policies for real — create, describe, update,
	// delete, list and tag — so it is no longer a stub. DescribeOrganization
	// remains a single hardcoded org, and nothing about a policy is ever
	// attached or enforced (docs/plans/inert-tier-rollout.md §0).
	"organizations": TierInert,

	// Inert stubs — CRUD works for core resources, but no side-effects
	"cloudwatch":  TierInert,
	"acm":         TierInert,
	"opensearch":  TierInert,
	"appconfig":   TierInert,
	"glue":        TierInert,
	"firehose":    TierInert,
	"bedrock":     TierStub,
	"appregistry": TierInert,
	"elasticache": TierPartial,
	"msk":         TierPartial,
	"route53":     TierInert,
	"elbv2":       TierInert,
	"cloudtrail":  TierInert,
	"backup":      TierInert,
	"transfer":    TierInert,
	// efs is a full control-plane emulation with lifecycle states, but has no
	// NFS data plane — file systems are not mountable.
	"efs": TierPartial,
	// s3tables: the whole control plane plus the Iceberg REST catalog that
	// engines commit through; maintenance and replication are never run.
	"s3tables": TierPartial,
}

// ServiceGoalTiers maps each service to its aspirational emulation tier — the
// tier we intend to reach eventually. When ServiceTiers[svc] != ServiceGoalTiers[svc]
// the service is considered "work in progress" and the UI shows a WIP indicator.
//
// Services not listed here have an implicit goal of their current tier
// (i.e. no further improvement is planned or the service is already at its goal).
var ServiceGoalTiers = map[string]EmulationTier{
	// WIP — currently partial, goal is full
	"dynamodb":       TierFull,
	"secretsmanager": TierFull,

	// WIP — currently inert, goal is partial
	"iam": TierPartial,

	// WIP — currently partial, goal is full. The execution engine landed, so
	// Step Functions is no longer inert; `.waitForTaskToken`, activities and
	// distributed Map are what still stand between it and full.
	"stepfunctions": TierFull,
}
