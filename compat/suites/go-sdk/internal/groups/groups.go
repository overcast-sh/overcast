// Package groups assembles all service group implementations for the Go SDK suite.
package groups

import (
	"context"

	"github.com/overcast-sh/overcast-compat-go-sdk/internal/clients"
	"github.com/overcast-sh/overcast-compat-go-sdk/internal/harness"
)

// ServiceGroup bundles the impls, setup, and teardown maps for one service.
type ServiceGroup struct {
	// Name identifies the service file these registrations came from. It is
	// what a duplicate-key error names, so a collision points at the two files
	// to look in rather than just the key they disagree about.
	Name     string
	Impls    map[string]harness.TestFn
	Setup    map[string]func(context.Context, *harness.TestContext) error
	Teardown map[string]func(context.Context, *harness.TestContext) error
}

// named labels a service group with its source. Applied here rather than in
// each constructor so the names sit next to the registration order they
// describe, in one table.
func (g ServiceGroup) named(name string) ServiceGroup {
	g.Name = name
	return g
}

// All returns all service groups: the hand-written ones below, then the
// generated ones from scenarios_gen.go.
//
// A generated group is a registry group cmd/compatgen emitted from the
// scenario IR (compat/model/scenarios/<service>.json). Its constructor names
// the source file it came from rather than a service file here, so a duplicate
// impl key can say which of the two registered it.
func All(c *clients.Clients) []ServiceGroup {
	return append(handWritten(c), scenarioGroups(c)...)
}

func handWritten(c *clients.Clients) []ServiceGroup {
	return []ServiceGroup{
		S3(c).named("s3"),
		SQS(c).named("sqs"),
		DynamoDB(c).named("dynamodb"),
		SNS(c).named("sns"),
		Lambda(c).named("lambda"),
		SES(c).named("ses"),
		IAM(c).named("iam"),
		STS(c).named("sts"),
		SecretsManager(c).named("secretsmanager"),
		KMS(c).named("kms"),
		SSM(c).named("ssm"),
		EventBridge(c).named("eventbridge"),
		CloudFormation(c).named("cloudformation"),
		EC2(c).named("ec2"),
		ECS(c).named("ecs"),
		Cognito(c).named("cognito"),
		AppSync(c).named("appsync"),
		APIGateway(c).named("apigateway"),
		CloudFront(c).named("cloudfront"),
		RDS(c).named("rds"),
		StepFunctions(c).named("stepfunctions"),
		Pipes(c).named("pipes"),
		WAF(c).named("waf"),
		Shield(c).named("shield"),
		Glue(c).named("glue"),
		GlueS3Tables(c).named("glue_s3tables"),
		Athena(c).named("athena"),
		AthenaEngine(c).named("athena_engine"),
		ElastiCache(c).named("elasticache"),
		EFS(c).named("efs"),
		S3Tables(c).named("s3tables"),
	}
}
