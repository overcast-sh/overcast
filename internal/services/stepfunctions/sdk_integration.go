package stepfunctions

import (
	"context"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"github.com/overcast-sh/overcast/internal/awsapi"
)

// AWS SDK service integrations: `arn:aws:states:::aws-sdk:<service>:<action>`.
//
// Overcast interprets them for every service that speaks AWS JSON 1.0 or 1.1
// (DynamoDB, SQS, Step Functions, EventBridge, Secrets Manager, SSM, KMS,
// CloudWatch Logs, Kinesis, ECS, …) by dispatching the action to that
// service's X-Amz-Target through the router, and for the common S3 object
// actions (s3io.go). Other protocols — Query, REST-JSON, REST-XML beyond S3 —
// fail the execution loudly naming the integration.
//
// Two conventions from AWS's SDK integrations apply: results use PascalCase
// member names whatever the service's own wire casing, and errors are named
// `<Service>.<ErrorCode>` (for example DynamoDb.ResourceNotFoundException).

// sdkErrorPrefixes are the error-name prefixes AWS uses for SDK integrations
// whose prefix is not simply the service name with a capital first letter.
var sdkErrorPrefixes = map[string]string{
	"dynamodb":       "DynamoDb",
	"secretsmanager": "SecretsManager",
	"eventbridge":    "EventBridge",
	"cloudwatchlogs": "CloudWatchLogs",
	"cloudwatch":     "CloudWatch",
}

func sdkErrorPrefix(service string) string {
	if prefix, ok := sdkErrorPrefixes[service]; ok {
		return prefix
	}
	return capitalize(service)
}

// sdkJSONContentTypes are the protocols the generic SDK integration speaks.
var sdkJSONContentTypes = map[awsapi.Protocol]string{
	awsapi.ProtocolAWSJSON10: "application/x-amz-json-1.0",
	awsapi.ProtocolAWSJSON11: "application/x-amz-json-1.1",
}

// sdkOperations caches the model lookup for each service/action pair a
// workflow has used, so the corpus is scanned once per pair.
var sdkOperations sync.Map

// findSDKOperation resolves an aws-sdk service name and camelCase action to
// its modeled operation. The service name is the SDK's own identifier with
// spaces removed and lower-cased — `sfn`, `dynamodb`, `secretsmanager`,
// `cloudwatchlogs` — which is how AWS spells it in the Resource ARN.
func findSDKOperation(service, action string) (awsapi.Operation, bool) {
	key := service + ":" + action
	if cached, ok := sdkOperations.Load(key); ok {
		op, found := cached.(awsapi.Operation)
		return op, found
	}
	name := capitalize(action)
	var match awsapi.Operation
	found := false
	awsapi.WalkOperations(func(op awsapi.Operation) bool {
		if op.Name == name && normalizeSDKID(op.SDKID) == service {
			match, found = op, true
			return false
		}
		return true
	})
	if found {
		sdkOperations.Store(key, match)
	} else {
		sdkOperations.Store(key, false)
	}
	return match, found
}

func normalizeSDKID(id string) string {
	return strings.ToLower(strings.NewReplacer(" ", "", "-", "").Replace(id))
}

// invokeAWSSDK runs one aws-sdk integration.
func (in *interpreter) invokeAWSSDK(ctx context.Context, service, action string, payload any) (any, *stateError) {
	params, ok := payload.(map[string]any)
	if !ok {
		if payload != nil {
			return nil, newStateError(errParameterPathFailure, "aws-sdk:%s:%s requires Parameters that are a JSON object", service, action)
		}
		params = map[string]any{}
	}
	if service == "s3" {
		return in.invokeS3SDK(ctx, action, params)
	}
	op, found := findSDKOperation(service, action)
	if !found {
		return nil, newStateError(errRuntime, "aws-sdk:%s:%s does not name an AWS API action", service, action)
	}
	contentType, ok := sdkJSONContentTypes[op.Protocol]
	if !ok {
		return nil, unsupportedError("the aws-sdk:%s:%s integration — %s speaks the %s protocol, and Overcast interprets SDK integrations for AWS JSON services and S3 only", service, action, op.SDKID, op.Protocol)
	}
	result, serr := in.invokeTargetAs(ctx, op.TargetPrefix+op.Name, contentType, params, sdkErrorPrefix(service))
	if serr != nil {
		return nil, serr
	}
	return pascalCaseMembers(result), nil
}

// pascalCaseMembers gives a response the PascalCase member names AWS SDK
// integrations use. Services whose JSON is already PascalCase (DynamoDB, SQS,
// Kinesis, …) are left exactly as they are — their nested maps hold user data
// such as item attribute names that must not be rewritten. Only a response
// whose top-level members are camelCase (Step Functions, ECS, …) is converted.
func pascalCaseMembers(v any) any {
	obj, ok := v.(map[string]any)
	if !ok {
		return v
	}
	for key := range obj {
		if r, _ := utf8.DecodeRuneInString(key); unicode.IsLower(r) {
			return capitalizeKeys(v)
		}
	}
	return v
}

func capitalizeKeys(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for key, value := range x {
			out[capitalize(key)] = capitalizeKeys(value)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, value := range x {
			out[i] = capitalizeKeys(value)
		}
		return out
	}
	return v
}

func capitalize(s string) string {
	r, size := utf8.DecodeRuneInString(s)
	if size == 0 {
		return s
	}
	return string(unicode.ToUpper(r)) + s[size:]
}
