// Package groups assembles all service group implementations for the CLI suite.
package groups

import (
	"context"

	"github.com/overcast-sh/overcast-compat-cli/internal/harness"
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

// All returns all service groups.
func All() []ServiceGroup {
	return []ServiceGroup{
		S3().named("s3"),
		SQS().named("sqs"),
		DynamoDB().named("dynamodb"),
		SNS().named("sns"),
		Lambda().named("lambda"),
		CloudWatchLogs().named("cloudwatch-logs"),
		SES().named("ses"),
		IAM().named("iam"),
		STS().named("sts"),
		SecretsManager().named("secretsmanager"),
		KMS().named("kms"),
		SSM().named("ssm"),
		Kinesis().named("kinesis"),
		EventBridge().named("eventbridge"),
		CloudFormation().named("cloudformation"),
		EC2().named("ec2"),
		ECS().named("ecs"),
		ECR().named("ecr"),
		Cognito().named("cognito"),
		AppSync().named("appsync"),
		APIGateway().named("apigateway"),
		CloudFront().named("cloudfront"),
		RDS().named("rds"),
		StepFunctions().named("stepfunctions"),
		Pipes().named("pipes"),
		WAF().named("waf"),
		Shield().named("shield"),
		Glue().named("glue"),
		ElastiCache().named("elasticache"),
		EFS().named("efs"),
		AppConfigData().named("appconfigdata"),
		OpenSearch().named("opensearch"),
		AppConfig().named("appconfig"),
		Backup().named("backup"),
		MSK().named("msk"),
		EKS().named("eks"),
	}
}
