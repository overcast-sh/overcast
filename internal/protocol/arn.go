package protocol

import (
	"fmt"
	"strings"
)

// ARN builds an AWS ARN string.
// Format: arn:aws:<service>:<region>:<accountID>:<resource>
//
// Examples:
//
//	ARN("us-east-1", "000000000000", "s3", "my-bucket")
//	  → "arn:aws:s3:::my-bucket"  (S3 omits region and account)
//
//	ARN("us-east-1", "000000000000", "sqs", "my-queue")
//	  → "arn:aws:sqs:us-east-1:000000000000:my-queue"
func ARN(region, accountID, service, resource string) string {
	// S3 ARNs omit region and account ID — this is an AWS quirk.
	if service == "s3" {
		return fmt.Sprintf("arn:aws:s3:::%s", resource)
	}
	return fmt.Sprintf("arn:aws:%s:%s:%s:%s", service, region, accountID, resource)
}

// ServiceFromARN extracts the service name (e.g. "appsync", "kafka") from an
// ARN's third colon-delimited segment ("arn:partition:service:..."). Returns
// "" if arn is not a well-formed ARN string.
//
// This is the authoritative way to identify which service owns a resource
// when a path is shared between services (e.g. /v1/tags/{resourceArn}) —
// the ARN is self-describing, unlike the request's SigV4 credential scope,
// which callers may not always set to match (hand-built requests, or
// resource-group-style tagging tools that sign generically).
func ServiceFromARN(arn string) string {
	parts := strings.SplitN(arn, ":", 4)
	if len(parts) < 3 || parts[0] != "arn" {
		return ""
	}
	return parts[2]
}

// QueueARN builds an SQS queue ARN from its components.
func QueueARN(region, accountID, queueName string) string {
	return ARN(region, accountID, "sqs", queueName)
}

// TopicARN builds an SNS topic ARN.
func TopicARN(region, accountID, topicName string) string {
	return ARN(region, accountID, "sns", topicName)
}

// LambdaARN builds a Lambda function ARN.
func LambdaARN(region, accountID, functionName string) string {
	return ARN(region, accountID, "lambda", "function:"+functionName)
}

// LambdaVersionARN builds a Lambda function ARN with a numeric version qualifier.
// Format: arn:aws:lambda:{region}:{account}:function:{name}:{version}.
func LambdaVersionARN(region, accountID, functionName string, version int) string {
	return fmt.Sprintf("arn:aws:lambda:%s:%s:function:%s:%d", region, accountID, functionName, version)
}

// LambdaCodeSigningConfigARN builds a Lambda code signing configuration ARN.
// Format: arn:aws:lambda:{region}:{account}:code-signing-config:{cscID}.
func LambdaCodeSigningConfigARN(region, accountID, cscID string) string {
	return ARN(region, accountID, "lambda", "code-signing-config:"+cscID)
}

// LayerVersionARN builds a Lambda layer version ARN.
// Format: arn:aws:lambda:{region}:{account}:layer:{name}:{version}.
func LayerVersionARN(region, accountID, layerName string, version int) string {
	return fmt.Sprintf("arn:aws:lambda:%s:%s:layer:%s:%d", region, accountID, layerName, version)
}

// LayerARN builds the unversioned Lambda layer ARN (no version suffix).
// Format: arn:aws:lambda:{region}:{account}:layer:{name}.
func LayerARN(region, accountID, layerName string) string {
	return fmt.Sprintf("arn:aws:lambda:%s:%s:layer:%s", region, accountID, layerName)
}

// TableARN builds a DynamoDB table ARN.
func TableARN(region, accountID, tableName string) string {
	return ARN(region, accountID, "dynamodb", "table/"+tableName)
}

// LogGroupARN builds a CloudWatch Logs log group ARN.
func LogGroupARN(region, accountID, groupName string) string {
	return fmt.Sprintf("arn:aws:logs:%s:%s:log-group:%s:*", region, accountID, groupName)
}

// LogStreamARN builds a CloudWatch Logs log stream ARN.
func LogStreamARN(region, accountID, groupName, streamName string) string {
	return fmt.Sprintf("arn:aws:logs:%s:%s:log-group:%s:log-stream:%s", region, accountID, groupName, streamName)
}

// DistributionARN builds a CloudFront distribution ARN.
// CloudFront ARNs omit the region — this is an AWS quirk.
// Format: arn:aws:cloudfront::{accountID}:distribution/{distributionID}.
func DistributionARN(accountID, distributionID string) string {
	return fmt.Sprintf("arn:aws:cloudfront::%s:distribution/%s", accountID, distributionID)
}

// RoleNameFromARN returns the role name an IAM role ARN
// (arn:aws:iam::<account>:role/<RoleName>, or role/<path>/<RoleName>) names:
// its last "/"-delimited segment. An input with no "/" is returned unchanged.
func RoleNameFromARN(roleArn string) string {
	if idx := strings.LastIndex(roleArn, "/"); idx >= 0 {
		return roleArn[idx+1:]
	}
	return roleArn
}

// AssumedRoleARN builds the ARN AWS names an STS role session by:
// arn:aws:sts::<account>:assumed-role/<RoleName>/<SessionName>. RoleName is
// read from roleArn, and the role's path is not part of it — see
// https://docs.aws.amazon.com/IAM/latest/UserGuide/reference_identifiers.html#identifiers-arns.
func AssumedRoleARN(accountID, roleArn, sessionName string) string {
	return fmt.Sprintf("arn:aws:sts::%s:assumed-role/%s/%s", accountID, RoleNameFromARN(roleArn), sessionName)
}
