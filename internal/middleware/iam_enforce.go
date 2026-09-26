package middleware

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"go.uber.org/zap"

	"github.com/overcast-sh/overcast/internal/iampolicy"
	"github.com/overcast-sh/overcast/internal/protocol"
	"github.com/overcast-sh/overcast/internal/state"
)

// iamEnforceCacheEntry is one principal's compiled policy set. Compiling is
// the expensive half of an evaluation (JSON parse plus wildcard compilation),
// so it happens once per principal and is reused until an IAM mutation calls
// InvalidateIAMEnforceCache. A principal with no policies — and an access key
// that resolves to no principal at all — is cached too, so a denied request
// does not rescan the IAM namespaces on every retry.
type iamEnforceCacheEntry struct {
	statements   []iampolicy.Statement
	principalCtx map[string]string
	// boundary is the principal's compiled permissions boundary, nil when it
	// has none, and boundaryErr says why it could not be read when it could
	// not. Both are cached alongside the identity policies because attaching,
	// replacing or removing a boundary is an IAM mutation like any other and
	// therefore already invalidates this cache.
	boundary    *iampolicy.Boundary
	boundaryErr error
	// compileErr is set when the principal's policies could not be parsed.
	// Enforcement fails closed on it rather than evaluating a partial set.
	compileErr error
}

// iamEnforceGeneration is bumped by every IAM mutation. Each middleware
// instance stamps its cache with the generation it was built at and discards
// the cache when the two diverge, which is how a policy change reaches an
// already-warm cache.
//
// A counter rather than a registry of invalidation callbacks: a registry has to
// be added to when a router is built and removed from when one goes away, and
// the removal half is easy to forget — a test binary builds hundreds of routers
// and would accumulate one live callback (and its cache) per router, with every
// IAM mutation walking all of them. A generation is O(1) to invalidate however
// many routers exist, needs no registration or teardown, and lets a dead
// router's cache be collected with the router.
var iamEnforceGeneration atomic.Uint64

// InvalidateIAMEnforceCache marks every compiled-policy cache stale. Called by
// the IAM service after any operation that changes what a principal is allowed
// to do.
func InvalidateIAMEnforceCache() {
	iamEnforceGeneration.Add(1)
}

// iamEnforceCache holds one middleware instance's compiled policies, valid only
// for the generation it was built at.
type iamEnforceCache struct {
	mu         sync.RWMutex
	generation uint64
	entries    map[string]*iamEnforceCacheEntry
}

// load returns the cached entry for accessKeyID, and the generation the caller
// must still be on for a later store to be accepted. A cache built before the
// last invalidation is dropped here rather than consulted.
func (c *iamEnforceCache) load(accessKeyID string) (*iamEnforceCacheEntry, uint64) {
	generation := iamEnforceGeneration.Load()

	c.mu.RLock()
	if c.generation == generation {
		entry := c.entries[accessKeyID]
		c.mu.RUnlock()
		return entry, generation
	}
	c.mu.RUnlock()

	c.mu.Lock()
	if c.generation != generation {
		c.entries = nil
		c.generation = generation
	}
	c.mu.Unlock()
	return nil, generation
}

// store records an entry compiled at generation, and drops it if an IAM
// mutation landed while it was being compiled — the entry may already describe
// policies that no longer exist, and recompiling on the next request is
// cheaper than reasoning about which half is stale.
func (c *iamEnforceCache) store(generation uint64, accessKeyID string, entry *iamEnforceCacheEntry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if iamEnforceGeneration.Load() != generation {
		return
	}
	if c.generation != generation || c.entries == nil {
		c.entries = make(map[string]*iamEnforceCacheEntry)
		c.generation = generation
	}
	c.entries[accessKeyID] = entry
}

// IAMEnforce enforces opt-in IAM authorization.
//
// queries is the router's AWS Query dispatch, which names the operation a
// Query request is served as. A nil queries is a router that serves no Query
// traffic, and every request is then classified from its own content.
func IAMEnforce(enabled bool, st state.Store, logger *zap.Logger, queries QueryRouter) func(http.Handler) http.Handler {
	cache := &iamEnforceCache{}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !enabled || shouldBypassIAM(r) {
				next.ServeHTTP(w, r)
				return
			}

			op, err := requestIAMOperation(w, r, queries)
			if err != nil {
				// The router refuses a Query form it cannot parse with this
				// error, and a form parsed here cannot be parsed there again.
				protocol.WriteQueryXMLError(w, r, protocol.QueryFormParseError(err))
				return
			}

			if !isSignedIAMRequest(r) {
				denyIAMRequest(w, r, op, logger, "unsigned request")
				return
			}

			if st == nil {
				next.ServeHTTP(w, r)
				return
			}

			parts, signed, sigErr := extractSigV4Parts(r)
			if sigErr != nil || !signed || strings.TrimSpace(parts.AccessKey) == "" {
				denyIAMRequest(w, r, op, logger, "missing or malformed SigV4 access key")
				return
			}

			if op.action == "" {
				// No action could be inferred, so there is nothing to evaluate
				// a policy against and the request is not gated.
				//
				// This is deliberately fail-open, and it is the one place in
				// this middleware that is: iamDenialReason fails closed on
				// everything it can reason about, down to policy constructs the
				// evaluator does not implement. Failing closed here instead
				// would deny far more than it protected: S3 reaches this branch
				// routinely, because its sub-resource operations (?tagging,
				// ?restore, ?legal-hold, …) are named by query parameters the
				// shape rules do not enumerate. A closed default would break
				// ordinary S3 traffic the moment IAM enforcement was switched
				// on, to guard paths that mostly do not resolve to a handler in
				// the first place.
				//
				// What must never happen is the third option: inferring the
				// *wrong* action. That is not a safe default in either
				// direction — it authorised Lambda AddPermission as
				// lambda:InvokeFunction, letting an invoke-only principal grant
				// invoke rights to others, and it denied RemovePermission
				// against a policy that allowed it. requestIAMAction is now
				// scoped to the classified service throughout so a path one
				// service does not model can no longer borrow another's name;
				// see restOperation.
				//
				// Logged so the gap is observable rather than silent.
				if logger != nil {
					logger.Debug("iam enforcement could not infer an action; request not gated",
						zap.String("method", r.Method),
						zap.String("path", r.URL.Path),
						zap.String("service", op.service),
					)
				}
				next.ServeHTTP(w, r)
				return
			}

			resource := requestIAMResource(r, op)
			result := evaluateIAMDecision(r, st, parts.AccessKey, op, resource, cache)
			if reason, denied := iamDenialReason(result); denied {
				// A deny the evaluator could not reason about is a gap in
				// Overcast, not a decision the user asked for: say so at warn
				// level rather than burying it in debug output.
				if logger != nil && (result.compileErr != nil || result.boundaryErr != nil || len(result.Unsupported) > 0) {
					logger.Warn("iam enforcement denied a request it could not evaluate",
						zap.String("service", op.service),
						zap.String("action", op.action),
						zap.String("reason", reason),
					)
				}
				denyIAMRequest(w, r, op, logger, reason)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

const (
	iamUsersNamespace    = "iam:users"
	iamPoliciesNamespace = "iam:policies"
	iamGroupsNamespace   = "iam:groups"
	iamRolesNamespace    = "iam:roles"
	iamSessionsNamespace = "iam:sessions"
	defaultIAMAccountID  = "000000000000"
)

// iamDenialReason maps an evaluation onto the enforcement decision, and
// explains it for the debug log. Enforcement is deliberately fail-closed: a
// policy construct the evaluator does not implement denies rather than
// silently granting access it cannot reason about (the simulator surfaces the
// same information as AWS's PolicyEvaluation error).
func iamDenialReason(result iamEnforceResult) (string, bool) {
	if result.compileErr != nil {
		return "principal policy could not be parsed: " + result.compileErr.Error(), true
	}
	if result.boundaryErr != nil {
		return "permissions boundary could not be read: " + result.boundaryErr.Error(), true
	}
	if len(result.Unsupported) > 0 {
		return "policy uses a construct the evaluator does not implement: " + strings.Join(result.Unsupported, "; "), true
	}
	switch result.Decision {
	case iampolicy.DecisionAllowed:
		return "", false
	case iampolicy.DecisionExplicitDeny:
		return "explicit deny policy matched", true
	case iampolicy.DecisionImplicitDeny:
		fallthrough
	default:
		reason := "policy did not allow action"
		if result.BoundaryApplied && !result.AllowedByBoundary {
			// Distinguishable because the fix is different: the boundary caps
			// what the identity policies can grant, so widening those changes
			// nothing until the boundary is widened too.
			reason = "permissions boundary did not allow action"
		}
		if len(result.MissingContextKeys) > 0 {
			reason += " (missing condition context keys: " + strings.Join(result.MissingContextKeys, ", ") + ")"
		}
		return reason, true
	}
}

type iamUserRecord struct {
	UserName   string `json:"UserName"`
	UserID     string `json:"UserId"`
	Arn        string `json:"Arn"`
	AccessKeys []struct {
		AccessKeyID     string `json:"AccessKeyId"`
		SecretAccessKey string `json:"SecretAccessKey"`
	} `json:"AccessKeys"`
	InlinePolicies   map[string]string `json:"InlinePolicies"`
	AttachedPolicies []struct {
		PolicyArn string `json:"PolicyArn"`
	} `json:"AttachedPolicies"`
	PermissionsBoundary string `json:"PermissionsBoundary"`
}

type iamGroupRecord struct {
	Members          []string          `json:"Members"`
	InlinePolicies   map[string]string `json:"InlinePolicies"`
	AttachedPolicies []struct {
		PolicyArn string `json:"PolicyArn"`
	} `json:"AttachedPolicies"`
}

// iamRoleSessionRecord is the record stored in iam:sessions when a role is assumed via STS.
// Keyed by the temporary access key ID.
type iamRoleSessionRecord struct {
	RoleArn         string `json:"RoleArn"`
	RoleName        string `json:"RoleName"`
	SecretAccessKey string `json:"SecretAccessKey"`
}

type iamRoleRecord struct {
	RoleName         string            `json:"RoleName"`
	Arn              string            `json:"Arn"`
	InlinePolicies   map[string]string `json:"InlinePolicies"`
	AttachedPolicies []struct {
		PolicyArn string `json:"PolicyArn"`
	} `json:"AttachedPolicies"`
	PermissionsBoundary string `json:"PermissionsBoundary"`
}

type iamManagedPolicyRecord struct {
	Document string `json:"Document"`
}

func denyIAMRequest(w http.ResponseWriter, r *http.Request, op iamOperation, logger *zap.Logger, reason string) {
	if logger != nil {
		logger.Debug("iam enforcement denied request",
			zap.String("method", r.Method),
			zap.String("path", r.URL.Path),
			zap.String("service", op.service),
			zap.String("reason", reason),
		)
	}
	writeIAMAccessDenied(w, r, op)
}

// iamOperation is the operation IAM enforcement authorises a request as.
type iamOperation struct {
	// service is the key of the service that serves the request.
	service string
	// action is "<prefix>:<Op>", or "" when no operation can be named.
	action string
	// query is set when the router serves the request as AWS Query traffic,
	// whose clients read an error only in the Query XML envelope.
	query bool
}

// requestIAMOperation names the operation r is served as.
//
// A request the router dispatches as AWS Query is named by queries, from the
// same Version and Action resolution the router serves it by, and never by its
// credential scope: the two name different services whenever a caller signs
// for one service and calls another's Action (#2229). Everything else is
// classified from its own content by detectService.
func requestIAMOperation(w http.ResponseWriter, r *http.Request, queries QueryRouter) (iamOperation, error) {
	if queries != nil {
		route, isQuery, err := queries.RouteQuery(w, r)
		if err != nil || isQuery {
			return queryIAMOperation(route), err
		}
	}
	svc := detectService(r)
	return iamOperation{service: svc, action: requestIAMAction(r, svc)}, nil
}

// queryIAMOperation names the operation a routed Query request is served as.
// A route no service owns serves no operation, so it names none.
func queryIAMOperation(route QueryRoute) iamOperation {
	op := iamOperation{service: route.Service, query: true}
	if route.Service != "" && route.Action != "" {
		op.action = iamActionPrefix(route.Service) + ":" + route.Action
	}
	return op
}

// requestIAMAction names the IAM action a request to svc invokes, as
// "<prefix>:<Op>".
//
// The prefix is AWS's IAM action prefix for the classified service, which is
// not always Overcast's key for it: MSK is keyed "msk" and authorizes as
// "kafka:", Step Functions as "states:", OpenSearch as "es:". The action is
// evaluated against a policy the user wrote from the AWS documentation, so it
// has to be the name that documentation gives — see iamActionPrefix.
func requestIAMAction(r *http.Request, svc string) string {
	if svc == "" || svc == "internal" || svc == "metrics" || svc == "events" {
		return ""
	}
	prefix := iamActionPrefix(svc)
	if svc == "lambda" {
		if op := requestLambdaIAMOperation(r); op != "" {
			return prefix + ":" + op
		}
	}

	if action := strings.TrimSpace(r.URL.Query().Get("Action")); action != "" {
		return prefix + ":" + action
	}
	if strings.Contains(strings.ToLower(r.Header.Get("Content-Type")), "application/x-www-form-urlencoded") {
		// Preserving the body matters here: enforcement runs ahead of routing
		// and this content type is what a bare HTTP client sends by default,
		// so a plain ParseForm would eat an S3 PutObject payload whenever IAM
		// enforcement is switched on.
		_ = protocol.ParseFormPreservingBody(r)
		if action := strings.TrimSpace(r.Form.Get("Action")); action != "" {
			return prefix + ":" + action
		}
	}

	if target := strings.TrimSpace(r.Header.Get("X-Amz-Target")); target != "" {
		op := target
		if idx := strings.LastIndex(op, "."); idx >= 0 && idx+1 < len(op) {
			op = op[idx+1:]
		}
		if op != "" {
			return prefix + ":" + op
		}
	}

	if op := strings.TrimSpace(detectOperationForService(r, svc)); op != "" {
		return prefix + ":" + op
	}

	return ""
}

// requestIAMResource names the resource op acts on, as an ARN or "*".
func requestIAMResource(r *http.Request, op iamOperation) string {
	fields := newIAMRequestFieldResolver()
	switch op.service {
	case "s3":
		return requestS3IAMResource(r)
	case "sqs":
		return requestSQSIAMResource(r, op.action, fields)
	case "sns":
		return requestSNSIAMResource(r, op.action, fields)
	case "dynamodb":
		return requestDynamoDBIAMResource(r, fields)
	case "cloudformation":
		return requestCloudFormationIAMResource(r, fields)
	case "ecs":
		return requestECSIAMResource(r, fields)
	case "logs":
		return requestLogsIAMResource(r, fields)
	case "ecr":
		return requestECRIAMResource(r, fields)
	case "secretsmanager":
		return requestSecretsManagerIAMResource(r, fields)
	case "stepfunctions":
		return requestStepFunctionsIAMResource(r, fields)
	case "kinesis":
		return requestKinesisIAMResource(r, fields)
	case "firehose":
		return requestFirehoseIAMResource(r, fields)
	case "kms":
		return requestKMSIAMResource(r, fields)
	case "ssm":
		return requestSSMIAMResource(r, fields)
	case "lambda":
		return requestLambdaIAMResource(r, fields)
	case "cloudwatch":
		return requestCloudWatchIAMResource(r, fields)
	case "pipes":
		return requestPipesIAMResource(r)
	case "ec2":
		return requestEC2IAMResource(r, fields)
	case "events":
		return requestEventBridgeIAMResource(r, fields)
	case "rds":
		return requestRDSIAMResource(r, fields)
	case "cognito":
		return requestCognitoIAMResource(r, fields)
	case "apigateway":
		return requestAPIGatewayIAMResource(r)
	case "route53":
		return requestRoute53IAMResource(r, fields)
	case "elbv2":
		return requestELBv2IAMResource(r, fields)
	default:
		return "*"
	}
}

// requestLambdaIAMOperation returns the IAM action suffix for a Lambda REST
// request — "InvokeFunction" in "lambda:InvokeFunction".
//
// It shares one path mapping with the request logger (restOperation, which
// reads the pinned Smithy `@http` bindings) and adds only what authorization
// needs on top: the translation from an API operation name to the IAM action
// name where AWS makes them differ. Keeping a second hand-written copy of the
// paths here is what let the two drift — the logger's copy mislabelled most of
// Lambda as S3 while this one did not, and every path this one missed fell
// through to the logger's copy and produced actions like "lambda:PutObject".
func requestLambdaIAMOperation(r *http.Request) string {
	operation := restOperation("lambda", r)
	if operation == "" {
		return ""
	}
	return lambdaIAMAction(operation)
}

// lambdaIAMAction maps a Lambda API operation name onto the IAM action name
// that authorizes it. AWS authorizes all three invoke operations with the
// single action lambda:InvokeFunction — there is no lambda:Invoke — and every
// other Lambda operation's action is named after the operation itself.
func lambdaIAMAction(operation string) string {
	switch operation {
	case "Invoke", "InvokeAsync", "InvokeWithResponseStream":
		return "InvokeFunction"
	default:
		return operation
	}
}

func requestS3IAMResource(r *http.Request) string {
	trimmed := strings.Trim(strings.TrimSpace(r.URL.Path), "/")
	if trimmed == "" {
		return "*"
	}

	parts := strings.SplitN(trimmed, "/", 2)
	bucket := strings.TrimSpace(parts[0])
	if bucket == "" {
		return "*"
	}
	if len(parts) == 1 {
		return fmt.Sprintf("arn:aws:s3:::%s", bucket)
	}
	objectKey := strings.TrimSpace(parts[1])
	if objectKey == "" {
		return fmt.Sprintf("arn:aws:s3:::%s", bucket)
	}
	return fmt.Sprintf("arn:aws:s3:::%s/%s", bucket, objectKey)
}

func requestSQSIAMResource(r *http.Request, action string, fields *iamRequestFieldResolver) string {
	region := iamRegionOrDefault(r)

	if strings.EqualFold(action, "sqs:CreateQueue") {
		queueName := fields.field(r, "QueueName")
		if queueName == "" {
			return "*"
		}
		return fmt.Sprintf("arn:aws:sqs:%s:%s:%s", region, defaultIAMAccountID, queueName)
	}

	queueURL := fields.field(r, "QueueUrl")
	if queueURL == "" {
		return "*"
	}
	accountID, queueName := parseSQSQueueURL(queueURL)
	if queueName == "" {
		return "*"
	}
	if accountID == "" {
		accountID = defaultIAMAccountID
	}

	return fmt.Sprintf("arn:aws:sqs:%s:%s:%s", region, accountID, queueName)
}

func requestSNSIAMResource(r *http.Request, action string, fields *iamRequestFieldResolver) string {
	region := iamRegionOrDefault(r)

	if strings.EqualFold(action, "sns:CreateTopic") {
		topicName := fields.field(r, "Name")
		if topicName == "" {
			return "*"
		}
		return fmt.Sprintf("arn:aws:sns:%s:%s:%s", region, defaultIAMAccountID, topicName)
	}

	if topicArn := fields.field(r, "TopicArn"); topicArn != "" {
		return topicArn
	}
	if targetArn := fields.field(r, "TargetArn"); targetArn != "" {
		return targetArn
	}

	return "*"
}

func requestDynamoDBIAMResource(r *http.Request, fields *iamRequestFieldResolver) string {
	region := iamRegionOrDefault(r)

	tableName := fields.field(r, "TableName")
	if tableName == "" {
		return "*"
	}

	return fmt.Sprintf("arn:aws:dynamodb:%s:%s:table/%s", region, defaultIAMAccountID, tableName)
}

func requestCloudFormationIAMResource(r *http.Request, fields *iamRequestFieldResolver) string {
	region := iamRegionOrDefault(r)

	stackID := fields.field(r, "StackId")
	if stackID != "" {
		if strings.HasPrefix(stackID, "arn:aws:cloudformation:") {
			return stackID
		}
		stackID = strings.TrimPrefix(stackID, "stack/")
		if i := strings.Index(stackID, "/"); i >= 0 {
			stackID = stackID[:i]
		}
		if stackID != "" {
			return fmt.Sprintf("arn:aws:cloudformation:%s:%s:stack/%s/*", region, defaultIAMAccountID, stackID)
		}
	}

	stackName := fields.field(r, "StackName")
	if stackName == "" {
		return "*"
	}

	return fmt.Sprintf("arn:aws:cloudformation:%s:%s:stack/%s/*", region, defaultIAMAccountID, stackName)
}

func requestECSIAMResource(r *http.Request, fields *iamRequestFieldResolver) string {
	region := iamRegionOrDefault(r)

	cluster := fields.field(r, "cluster")
	if cluster == "" {
		cluster = fields.firstJSONArrayStringField(r, "clusters")
	}
	if cluster == "" {
		cluster = fields.field(r, "clusterName")
	}
	if cluster == "" {
		return "*"
	}

	if strings.HasPrefix(cluster, "arn:aws:ecs:") {
		return cluster
	}

	cluster = strings.TrimPrefix(cluster, "cluster/")
	if i := strings.LastIndex(cluster, "/"); i >= 0 && i+1 < len(cluster) {
		cluster = cluster[i+1:]
	}
	if cluster == "" {
		return "*"
	}

	return fmt.Sprintf("arn:aws:ecs:%s:%s:cluster/%s", region, defaultIAMAccountID, cluster)
}

func requestLogsIAMResource(r *http.Request, fields *iamRequestFieldResolver) string {
	region := iamRegionOrDefault(r)

	logGroupName := fields.field(r, "logGroupName")
	if logGroupName == "" {
		return "*"
	}

	logStreamName := fields.field(r, "logStreamName")
	if logStreamName == "" {
		return fmt.Sprintf("arn:aws:logs:%s:%s:log-group:%s:*", region, defaultIAMAccountID, logGroupName)
	}

	return fmt.Sprintf("arn:aws:logs:%s:%s:log-group:%s:log-stream:%s", region, defaultIAMAccountID, logGroupName, logStreamName)
}

func requestECRIAMResource(r *http.Request, fields *iamRequestFieldResolver) string {
	region := iamRegionOrDefault(r)

	if resourceArn := fields.field(r, "resourceArn"); resourceArn != "" {
		return resourceArn
	}

	repositoryName := fields.field(r, "repositoryName")
	if repositoryName == "" {
		repositoryName = fields.firstJSONArrayStringField(r, "repositoryNames")
	}
	if repositoryName == "" {
		return "*"
	}

	return fmt.Sprintf("arn:aws:ecr:%s:%s:repository/%s", region, defaultIAMAccountID, repositoryName)
}

func requestSecretsManagerIAMResource(r *http.Request, fields *iamRequestFieldResolver) string {
	region := iamRegionOrDefault(r)

	secretID := fields.field(r, "SecretId")
	if secretID == "" {
		secretID = fields.field(r, "Name")
	}
	if secretID == "" {
		return "*"
	}

	if strings.HasPrefix(secretID, "arn:aws:secretsmanager:") {
		return secretID
	}

	return fmt.Sprintf("arn:aws:secretsmanager:%s:%s:secret:%s", region, defaultIAMAccountID, secretID)
}

func requestStepFunctionsIAMResource(r *http.Request, fields *iamRequestFieldResolver) string {
	region := iamRegionOrDefault(r)

	stateMachineArn := fields.field(r, "stateMachineArn")
	if stateMachineArn != "" {
		if strings.HasPrefix(stateMachineArn, "arn:aws:states:") {
			return stateMachineArn
		}
		return fmt.Sprintf("arn:aws:states:%s:%s:stateMachine:%s", region, defaultIAMAccountID, stateMachineArn)
	}

	name := fields.field(r, "name")
	if name == "" {
		return "*"
	}

	return fmt.Sprintf("arn:aws:states:%s:%s:stateMachine:%s", region, defaultIAMAccountID, name)
}

func requestKinesisIAMResource(r *http.Request, fields *iamRequestFieldResolver) string {
	region := iamRegionOrDefault(r)

	if streamARN := fields.field(r, "StreamARN"); streamARN != "" {
		return streamARN
	}

	streamName := fields.field(r, "StreamName")
	if streamName == "" {
		return "*"
	}

	return fmt.Sprintf("arn:aws:kinesis:%s:%s:stream/%s", region, defaultIAMAccountID, streamName)
}

func requestFirehoseIAMResource(r *http.Request, fields *iamRequestFieldResolver) string {
	region := iamRegionOrDefault(r)

	if streamARN := fields.field(r, "DeliveryStreamARN"); streamARN != "" {
		return streamARN
	}

	streamName := fields.field(r, "DeliveryStreamName")
	if streamName == "" {
		return "*"
	}

	return fmt.Sprintf("arn:aws:firehose:%s:%s:deliverystream/%s", region, defaultIAMAccountID, streamName)
}

func requestSSMIAMResource(r *http.Request, fields *iamRequestFieldResolver) string {
	region := iamRegionOrDefault(r)

	name := fields.field(r, "Name")
	if name == "" {
		return "*"
	}
	name = strings.TrimPrefix(name, "/")
	if name == "" {
		return "*"
	}

	return fmt.Sprintf("arn:aws:ssm:%s:%s:parameter/%s", region, defaultIAMAccountID, name)
}

func requestKMSIAMResource(r *http.Request, fields *iamRequestFieldResolver) string {
	region := iamRegionOrDefault(r)

	keyID := fields.field(r, "KeyId")
	if keyID == "" {
		return "*"
	}

	if strings.HasPrefix(keyID, "arn:aws:kms:") {
		return keyID
	}

	if strings.HasPrefix(keyID, "alias/") {
		return fmt.Sprintf("arn:aws:kms:%s:%s:%s", region, defaultIAMAccountID, keyID)
	}

	keyID = strings.TrimPrefix(keyID, "key/")
	if keyID == "" {
		return "*"
	}

	return fmt.Sprintf("arn:aws:kms:%s:%s:key/%s", region, defaultIAMAccountID, keyID)
}

func requestLambdaIAMResource(r *http.Request, fields *iamRequestFieldResolver) string {
	region := iamRegionOrDefault(r)
	trimmedPath := strings.Trim(strings.TrimSpace(r.URL.Path), "/")
	parts := strings.Split(trimmedPath, "/")

	// Both shapes reach here, and they differ by exactly one leading segment:
	//
	//	2015-03-31/functions/{name}/…        — AWS's, led by an API version
	//	_overcast/lambda/functions/{name}/…  — Overcast's emulator-only ones
	//
	// Dropping "_overcast" leaves "lambda" standing where the version segment
	// stands, so every index below keeps meaning what it has always meant.
	//
	// Without it the name is read from the wrong segment and the ARN degrades
	// to "*", which does not fail a request — it widens what a policy matches,
	// so a grant on one function would authorise every function. Phase 6 of
	// docs/plans/non-canonical-url-namespace.md moved these paths, and
	// TestIAMEnforce_enabled_lambdaTestEventsPathDeniesNonMatchingFunction is
	// what caught it.
	if len(parts) >= 2 && "/"+parts[0]+"/" == InternalPrefix && parts[1] == "lambda" {
		parts = parts[1:]
	}

	if len(parts) >= 3 && parts[1] == "layers" {
		layerName, err := url.PathUnescape(parts[2])
		if err != nil {
			layerName = parts[2]
		}
		if layerName == "" {
			return "*"
		}
		if len(parts) >= 5 && parts[3] == "versions" {
			version := strings.TrimSpace(parts[4])
			if version != "" {
				return fmt.Sprintf("arn:aws:lambda:%s:%s:layer:%s:%s", region, defaultIAMAccountID, layerName, version)
			}
		}
		return fmt.Sprintf("arn:aws:lambda:%s:%s:layer:%s", region, defaultIAMAccountID, layerName)
	}

	functionName := fields.field(r, "FunctionName")
	if functionName == "" {
		if len(parts) >= 3 && parts[1] == "functions" {
			if decoded, err := url.PathUnescape(parts[2]); err == nil {
				functionName = decoded
			} else {
				functionName = parts[2]
			}
		}
	}
	if functionName == "" {
		return "*"
	}

	if strings.HasPrefix(functionName, "arn:aws:lambda:") {
		return functionName
	}

	functionName = strings.TrimPrefix(functionName, "function:")
	if functionName == "" {
		return "*"
	}

	return fmt.Sprintf("arn:aws:lambda:%s:%s:function:%s", region, defaultIAMAccountID, functionName)
}

func requestCloudWatchIAMResource(r *http.Request, fields *iamRequestFieldResolver) string {
	region := iamRegionOrDefault(r)

	if resourceARN := fields.field(r, "ResourceARN"); resourceARN != "" {
		return resourceARN
	}

	alarmName := fields.field(r, "AlarmName")
	if alarmName == "" {
		alarmName = fields.field(r, "AlarmNames.member.1")
	}
	if alarmName == "" {
		return "*"
	}

	return fmt.Sprintf("arn:aws:cloudwatch:%s:%s:alarm:%s", region, defaultIAMAccountID, alarmName)
}

func requestPipesIAMResource(r *http.Request) string {
	region := iamRegionOrDefault(r)
	trimmedPath := strings.Trim(strings.TrimSpace(r.URL.Path), "/")
	parts := strings.Split(trimmedPath, "/")
	if len(parts) < 3 || parts[0] != "v1" || parts[1] != "pipes" {
		return "*"
	}
	name, err := url.PathUnescape(parts[2])
	if err != nil {
		name = parts[2]
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return "*"
	}
	return fmt.Sprintf("arn:aws:pipes:%s:%s:pipe/%s", region, defaultIAMAccountID, name)
}

func requestEC2IAMResource(r *http.Request, fields *iamRequestFieldResolver) string {
	region := iamRegionOrDefault(r)

	if vpcID := fields.field(r, "VpcId"); vpcID != "" {
		return fmt.Sprintf("arn:aws:ec2:%s:%s:vpc/%s", region, defaultIAMAccountID, vpcID)
	}
	if instanceID := fields.field(r, "InstanceId"); instanceID != "" {
		return fmt.Sprintf("arn:aws:ec2:%s:%s:instance/%s", region, defaultIAMAccountID, instanceID)
	}
	if instanceIDs := fields.firstJSONArrayStringField(r, "InstanceIds"); instanceIDs != "" {
		return fmt.Sprintf("arn:aws:ec2:%s:%s:instance/%s", region, defaultIAMAccountID, instanceIDs)
	}
	if groupID := fields.field(r, "GroupId"); groupID != "" {
		return fmt.Sprintf("arn:aws:ec2:%s:%s:security-group/%s", region, defaultIAMAccountID, groupID)
	}
	if niID := fields.field(r, "NetworkInterfaceId"); niID != "" {
		return fmt.Sprintf("arn:aws:ec2:%s:%s:network-interface/%s", region, defaultIAMAccountID, niID)
	}
	if keyName := fields.field(r, "KeyName"); keyName != "" {
		return fmt.Sprintf("arn:aws:ec2:%s:%s:key-pair/%s", region, defaultIAMAccountID, keyName)
	}
	if allocationID := fields.field(r, "AllocationId"); allocationID != "" {
		return fmt.Sprintf("arn:aws:ec2:%s:%s:elastic-ip/%s", region, defaultIAMAccountID, allocationID)
	}

	return "*"
}

func requestEventBridgeIAMResource(r *http.Request, fields *iamRequestFieldResolver) string {
	region := iamRegionOrDefault(r)

	if eventBusName := fields.field(r, "EventBusName"); eventBusName != "" {
		return fmt.Sprintf("arn:aws:events:%s:%s:event-bus/%s", region, defaultIAMAccountID, eventBusName)
	}
	if name := fields.field(r, "Name"); name != "" {
		return fmt.Sprintf("arn:aws:events:%s:%s:rule/%s/%s", region, defaultIAMAccountID, "default", name)
	}
	if rule := fields.field(r, "Rule"); rule != "" {
		if eventBusName := fields.field(r, "EventBusName"); eventBusName != "" {
			return fmt.Sprintf("arn:aws:events:%s:%s:rule/%s/%s", region, defaultIAMAccountID, eventBusName, rule)
		}
		return fmt.Sprintf("arn:aws:events:%s:%s:rule/%s/%s", region, defaultIAMAccountID, "default", rule)
	}

	return "*"
}

func requestRDSIAMResource(r *http.Request, fields *iamRequestFieldResolver) string {
	region := iamRegionOrDefault(r)

	if dbInstanceID := fields.field(r, "DBInstanceIdentifier"); dbInstanceID != "" {
		return fmt.Sprintf("arn:aws:rds:%s:%s:db:%s", region, defaultIAMAccountID, dbInstanceID)
	}
	if dbClusterID := fields.field(r, "DBClusterIdentifier"); dbClusterID != "" {
		return fmt.Sprintf("arn:aws:rds:%s:%s:cluster:%s", region, defaultIAMAccountID, dbClusterID)
	}

	return "*"
}

func requestCognitoIAMResource(r *http.Request, fields *iamRequestFieldResolver) string {
	region := iamRegionOrDefault(r)

	if userPoolID := fields.field(r, "UserPoolId"); userPoolID != "" {
		return fmt.Sprintf("arn:aws:cognito-idp:%s:%s:userpool/%s", region, defaultIAMAccountID, userPoolID)
	}
	if identityPoolID := fields.field(r, "IdentityPoolId"); identityPoolID != "" {
		return fmt.Sprintf("arn:aws:cognito-identity:%s:%s:identitypool/%s", region, defaultIAMAccountID, identityPoolID)
	}

	return "*"
}

func requestAPIGatewayIAMResource(r *http.Request) string {
	region := iamRegionOrDefault(r)
	trimmedPath := strings.Trim(strings.TrimSpace(r.URL.Path), "/")
	parts := strings.Split(trimmedPath, "/")

	if len(parts) >= 2 && parts[0] == "restapis" {
		restAPIID := parts[1]
		if restAPIID != "" {
			return fmt.Sprintf("arn:aws:apigateway:%s:%s:/restapis/%s", region, defaultIAMAccountID, restAPIID)
		}
	}

	if len(parts) >= 3 && parts[0] == "v2" && parts[1] == "apis" {
		apiID := parts[2]
		if apiID != "" {
			return fmt.Sprintf("arn:aws:apigateway:%s:%s:/apis/%s", region, defaultIAMAccountID, apiID)
		}
	}

	return "*"
}

func requestRoute53IAMResource(r *http.Request, fields *iamRequestFieldResolver) string {
	if hostedZoneID := fields.field(r, "HostedZoneId"); hostedZoneID != "" {
		id := strings.TrimPrefix(hostedZoneID, "/hostedzone/")
		return fmt.Sprintf("arn:aws:route53:::hostedzone/%s", id)
	}
	if id := fields.field(r, "Id"); id != "" {
		id = strings.TrimPrefix(id, "/hostedzone/")
		if id != "" {
			return fmt.Sprintf("arn:aws:route53:::hostedzone/%s", id)
		}
	}
	return "*"
}

func requestELBv2IAMResource(r *http.Request, fields *iamRequestFieldResolver) string {
	region := iamRegionOrDefault(r)

	if lbARN := fields.field(r, "LoadBalancerArn"); lbARN != "" {
		if strings.HasPrefix(lbARN, "arn:aws:elasticloadbalancing:") {
			return lbARN
		}
		return fmt.Sprintf("arn:aws:elasticloadbalancing:%s:%s:loadbalancer/%s", region, defaultIAMAccountID, lbARN)
	}
	if tgARN := fields.field(r, "TargetGroupArn"); tgARN != "" {
		if strings.HasPrefix(tgARN, "arn:aws:elasticloadbalancing:") {
			return tgARN
		}
		return fmt.Sprintf("arn:aws:elasticloadbalancing:%s:%s:targetgroup/%s", region, defaultIAMAccountID, tgARN)
	}
	if resourceARN := fields.field(r, "ResourceArns.member.1"); resourceARN != "" {
		return resourceARN
	}

	return "*"
}

func requestedRegionFromSigV4(r *http.Request) string {
	if parts := credentialScope(r); len(parts) >= 3 {
		if region := strings.TrimSpace(parts[2]); region != "" {
			return strings.ToLower(region)
		}
	}
	return ""
}

func iamRegionOrDefault(r *http.Request) string {
	region := requestedRegionFromSigV4(r)
	if region == "" {
		return "us-east-1"
	}
	return region
}

type iamRequestFieldResolver struct {
	formParsed bool
	jsonLoaded bool
	jsonBody   map[string]any
}

func newIAMRequestFieldResolver() *iamRequestFieldResolver {
	return &iamRequestFieldResolver{}
}

func (f *iamRequestFieldResolver) field(r *http.Request, key string) string {
	if v := strings.TrimSpace(r.URL.Query().Get(key)); v != "" {
		return v
	}

	if strings.Contains(strings.ToLower(r.Header.Get("Content-Type")), "application/x-www-form-urlencoded") {
		if !f.formParsed {
			// Body-preserving for the same reason as requestIAMAction above.
			_ = protocol.ParseFormPreservingBody(r)
			f.formParsed = true
		}
		if v := strings.TrimSpace(r.Form.Get(key)); v != "" {
			return v
		}
	}

	payload := f.loadJSONBody(r)
	if payload == nil {
		return ""
	}
	v, ok := payload[key]
	if !ok {
		return ""
	}
	s, ok := v.(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(s)
}

func (f *iamRequestFieldResolver) firstJSONArrayStringField(r *http.Request, key string) string {
	payload := f.loadJSONBody(r)
	if payload == nil {
		return ""
	}
	v, ok := payload[key]
	if !ok {
		return ""
	}
	a, ok := v.([]any)
	if !ok || len(a) == 0 {
		return ""
	}
	s, ok := a[0].(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(s)
}

func (f *iamRequestFieldResolver) loadJSONBody(r *http.Request) map[string]any {
	if f.jsonLoaded {
		return f.jsonBody
	}
	f.jsonLoaded = true

	if r.Body == nil {
		return nil
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return nil
	}
	_ = r.Body.Close()
	r.Body = io.NopCloser(bytes.NewReader(body))

	if len(bytes.TrimSpace(body)) == 0 {
		return nil
	}

	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil
	}
	f.jsonBody = payload
	return f.jsonBody
}

func parseSQSQueueURL(queueURL string) (string, string) {
	u, err := url.Parse(strings.TrimSpace(queueURL))
	if err != nil {
		return "", ""
	}
	segments := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(segments) >= 2 {
		return strings.TrimSpace(segments[len(segments)-2]), strings.TrimSpace(segments[len(segments)-1])
	}
	if len(segments) == 1 {
		return "", strings.TrimSpace(segments[0])
	}
	return "", ""
}

// iamEnforceResult is an evaluation plus whatever stopped it being one.
type iamEnforceResult struct {
	iampolicy.Result
	compileErr  error
	boundaryErr error
}

func evaluateIAMDecision(r *http.Request, st state.Store, accessKeyID string, op iamOperation, resource string, cache *iamEnforceCache) iamEnforceResult {
	cached, generation := cache.load(accessKeyID)
	if cached == nil {
		principal := collectPrincipalPolicies(r.Context(), st, accessKeyID)
		statements, err := iampolicy.Compile(principal.docs)
		boundary, boundaryErr := resolveIAMPermissionsBoundary(r.Context(), st, principal.boundaryArn)
		cached = &iamEnforceCacheEntry{
			statements:   statements,
			principalCtx: principal.context,
			boundary:     boundary,
			boundaryErr:  boundaryErr,
			compileErr:   err,
		}
		cache.store(generation, accessKeyID, cached)
	}
	if cached.compileErr != nil {
		return iamEnforceResult{compileErr: cached.compileErr}
	}
	if cached.boundaryErr != nil {
		return iamEnforceResult{boundaryErr: cached.boundaryErr}
	}

	reqCtx := buildIAMRequestContext(r, op.service)
	for k, v := range cached.principalCtx {
		reqCtx[k] = v
	}

	return iamEnforceResult{Result: iampolicy.Evaluate(iampolicy.Input{
		Request: iampolicy.Request{
			Action:           op.action,
			Resource:         resource,
			Context:          reqCtx,
			PrincipalARN:     cached.principalCtx["aws:principalarn"],
			PrincipalAccount: cached.principalCtx["aws:principalaccount"],
		},
		Identity: cached.statements,
		Boundary: cached.boundary,
	})}
}

// iamPrincipalPolicies is the policy material an access key resolves to: the
// identity policy documents, the condition-key context derived from the
// principal itself, and the ARN of its permissions boundary if it has one.
type iamPrincipalPolicies struct {
	docs        []iampolicy.SourcedDocument
	context     map[string]string
	boundaryArn string
}

// resolveIAMPermissionsBoundary compiles the managed policy a principal's
// boundary ARN names, or returns nil when it has no boundary.
//
// A boundary that is attached but cannot be read — the policy record is gone,
// does not decode, or its document is not a valid policy — yields a boundary
// with no statements, which allows nothing, plus the reason. Enforcement is
// fail-closed throughout, and reading an unreadable boundary as absent would
// grant exactly the permissions it was attached to withhold; the reason is what
// stops that deny looking like an ordinary missing permission.
func resolveIAMPermissionsBoundary(ctx context.Context, st state.Store, arn string) (*iampolicy.Boundary, error) {
	if strings.TrimSpace(arn) == "" {
		return nil, nil
	}
	unreadable := fmt.Errorf("permissions boundary policy %s could not be read", arn)
	raw, found, err := st.Get(ctx, iamPoliciesNamespace, arn)
	if err != nil || !found {
		return &iampolicy.Boundary{}, unreadable
	}
	var managed iamManagedPolicyRecord
	if err := json.Unmarshal([]byte(raw), &managed); err != nil || strings.TrimSpace(managed.Document) == "" {
		return &iampolicy.Boundary{}, unreadable
	}
	return iampolicy.NewBoundary(managed.Document,
		iampolicy.SourceRef{ID: arn, Type: iampolicy.SourceTypeIAMPolicy})
}

func collectPrincipalPolicies(ctx context.Context, st state.Store, accessKeyID string) iamPrincipalPolicies {
	users, err := st.Scan(ctx, iamUsersNamespace, "")
	if err != nil {
		return iamPrincipalPolicies{}
	}

	for _, kv := range users {
		var user iamUserRecord
		if err := json.Unmarshal([]byte(kv.Value), &user); err != nil {
			continue
		}
		if !userHasAccessKey(user, accessKeyID) {
			continue
		}
		if strings.TrimSpace(user.UserName) == "" {
			user.UserName = kv.Key
		}

		docs := make([]iampolicy.SourcedDocument, 0, len(user.InlinePolicies)+len(user.AttachedPolicies))
		docs = appendInlineAndAttachedPolicyDocs(ctx, st, docs, iampolicy.SourceTypeUser, user.InlinePolicies, user.AttachedPolicies)
		docs = appendGroupPolicyDocuments(ctx, st, docs, user.UserName)

		principalArn := strings.TrimSpace(user.Arn)
		if principalArn == "" {
			principalArn = fmt.Sprintf("arn:aws:iam::%s:user/%s", defaultIAMAccountID, user.UserName)
		}
		principalAccount := accountFromARN(principalArn)
		if principalAccount == "" {
			principalAccount = defaultIAMAccountID
		}

		userID := strings.TrimSpace(user.UserID)
		if userID == "" {
			userID = accessKeyID
		}

		principalCtx := map[string]string{
			"aws:principalarn":     principalArn,
			"aws:principalaccount": principalAccount,
			"aws:userid":           userID,
			"aws:username":         user.UserName,
			"aws:principaltype":    "User",
		}

		return iamPrincipalPolicies{
			docs:        docs,
			context:     principalCtx,
			boundaryArn: user.PermissionsBoundary,
		}
	}

	return collectRoleSessionPolicies(ctx, st, accessKeyID)
}

// PrincipalIdentity is the caller identity resolved from an IAM or STS access
// key. It intentionally contains no authorization decision.
type PrincipalIdentity struct {
	ARN     string
	Account string
}

// ResolvePrincipalIdentity resolves the principal represented by an access
// key using the same IAM and STS records as request-time enforcement.
func ResolvePrincipalIdentity(ctx context.Context, st state.Store, accessKeyID string) (PrincipalIdentity, bool) {
	if st == nil || strings.TrimSpace(accessKeyID) == "" {
		return PrincipalIdentity{}, false
	}
	principal := collectPrincipalPolicies(ctx, st, accessKeyID)
	arn := strings.TrimSpace(principal.context["aws:principalarn"])
	account := strings.TrimSpace(principal.context["aws:principalaccount"])
	if arn == "" {
		return PrincipalIdentity{}, false
	}
	return PrincipalIdentity{ARN: arn, Account: account}, true
}

// LocalIAMPrincipalExists reports whether an IAM user or role ARN resolves to
// a record in the local account. Callers remain responsible for deciding how
// to handle account roots, STS sessions, service principals, and remote
// accounts, none of which this local IAM store can authoritatively validate.
func LocalIAMPrincipalExists(ctx context.Context, st state.Store, principalARN string) (bool, error) {
	if st == nil {
		return false, nil
	}
	parts := strings.SplitN(strings.TrimSpace(principalARN), ":", 6)
	if len(parts) != 6 || parts[0] != "arn" || parts[2] != "iam" {
		return false, nil
	}
	resource := parts[5]
	var namespace, name string
	switch {
	case strings.HasPrefix(resource, "user/"):
		namespace = iamUsersNamespace
		name = resource[strings.LastIndex(resource, "/")+1:]
	case strings.HasPrefix(resource, "role/"):
		namespace = iamRolesNamespace
		name = resource[strings.LastIndex(resource, "/")+1:]
	default:
		return false, nil
	}
	raw, found, err := st.Get(ctx, namespace, name)
	if err != nil || !found {
		return false, err
	}
	var record struct {
		ARN string `json:"Arn"`
	}
	if err := json.Unmarshal([]byte(raw), &record); err != nil {
		return false, nil
	}
	return strings.TrimSpace(record.ARN) == strings.TrimSpace(principalARN), nil
}

func appendGroupPolicyDocuments(ctx context.Context, st state.Store, docs []iampolicy.SourcedDocument, userName string) []iampolicy.SourcedDocument {
	if strings.TrimSpace(userName) == "" {
		return docs
	}

	groups, err := st.Scan(ctx, iamGroupsNamespace, "")
	if err != nil {
		return docs
	}

	for _, kv := range groups {
		var group iamGroupRecord
		if err := json.Unmarshal([]byte(kv.Value), &group); err != nil {
			continue
		}
		if !groupHasMember(group, userName) {
			continue
		}
		docs = appendInlineAndAttachedPolicyDocs(ctx, st, docs, iampolicy.SourceTypeGroup, group.InlinePolicies, group.AttachedPolicies)
	}

	return docs
}

func groupHasMember(group iamGroupRecord, userName string) bool {
	for _, member := range group.Members {
		if member == userName {
			return true
		}
	}
	return false
}

// collectRoleSessionPolicies resolves a temporary access key issued by STS
// AssumeRole to its originating role, then collects that role's inline and
// attached managed policy documents plus its permissions boundary. Returns the
// zero value when the key is not found in iam:sessions.
func collectRoleSessionPolicies(ctx context.Context, st state.Store, accessKeyID string) iamPrincipalPolicies {
	sessionRaw, found, err := st.Get(ctx, iamSessionsNamespace, accessKeyID)
	if err != nil || !found {
		return iamPrincipalPolicies{}
	}

	var session iamRoleSessionRecord
	if err := json.Unmarshal([]byte(sessionRaw), &session); err != nil {
		return iamPrincipalPolicies{}
	}

	roleName := strings.TrimSpace(session.RoleName)
	if roleName == "" {
		// Try to derive the role name from the ARN as fallback.
		if idx := strings.LastIndex(session.RoleArn, "/"); idx >= 0 {
			roleName = session.RoleArn[idx+1:]
		}
	}
	if roleName == "" {
		return iamPrincipalPolicies{}
	}

	roleRaw, found, err := st.Get(ctx, iamRolesNamespace, roleName)
	if err != nil || !found {
		return iamPrincipalPolicies{}
	}

	var role iamRoleRecord
	if err := json.Unmarshal([]byte(roleRaw), &role); err != nil {
		return iamPrincipalPolicies{}
	}

	docs := make([]iampolicy.SourcedDocument, 0, len(role.InlinePolicies)+len(role.AttachedPolicies))
	docs = appendInlineAndAttachedPolicyDocs(ctx, st, docs, iampolicy.SourceTypeRole, role.InlinePolicies, role.AttachedPolicies)

	principalArn := strings.TrimSpace(session.RoleArn)
	if principalArn == "" {
		principalArn = strings.TrimSpace(role.Arn)
	}
	if principalArn == "" {
		principalArn = fmt.Sprintf("arn:aws:iam::%s:role/%s", defaultIAMAccountID, roleName)
	}
	principalAccount := accountFromARN(principalArn)
	if principalAccount == "" {
		principalAccount = defaultIAMAccountID
	}

	principalCtx := map[string]string{
		"aws:principalarn":     principalArn,
		"aws:principalaccount": principalAccount,
		"aws:userid":           accessKeyID,
		"aws:username":         roleName,
		"aws:principaltype":    "AssumedRole",
	}

	return iamPrincipalPolicies{
		docs:        docs,
		context:     principalCtx,
		boundaryArn: role.PermissionsBoundary,
	}
}

func accountFromARN(arn string) string {
	parts := strings.Split(arn, ":")
	if len(parts) < 6 {
		return ""
	}
	return strings.TrimSpace(parts[4])
}

func appendInlineAndAttachedPolicyDocs(
	ctx context.Context,
	st state.Store,
	docs []iampolicy.SourcedDocument,
	inlineSourceType string,
	inline map[string]string,
	attached []struct {
		PolicyArn string `json:"PolicyArn"`
	},
) []iampolicy.SourcedDocument {
	for name, doc := range inline {
		docs = append(docs, iampolicy.SourcedDocument{
			Source:   iampolicy.SourceRef{ID: name, Type: inlineSourceType},
			Document: doc,
		})
	}
	for _, policy := range attached {
		if strings.TrimSpace(policy.PolicyArn) == "" {
			continue
		}
		managedRaw, found, getErr := st.Get(ctx, iamPoliciesNamespace, policy.PolicyArn)
		if getErr != nil || !found {
			continue
		}
		var managed iamManagedPolicyRecord
		if err := json.Unmarshal([]byte(managedRaw), &managed); err != nil {
			continue
		}
		if strings.TrimSpace(managed.Document) != "" {
			docs = append(docs, iampolicy.SourcedDocument{
				Source:   iampolicy.SourceRef{ID: policy.PolicyArn, Type: iampolicy.SourceTypeIAMPolicy},
				Document: managed.Document,
			})
		}
	}
	return docs
}

func userHasAccessKey(user iamUserRecord, accessKeyID string) bool {
	for _, key := range user.AccessKeys {
		if key.AccessKeyID == accessKeyID {
			return true
		}
	}
	return false
}

// shouldBypassIAM reports whether a request is exempt from IAM enforcement.
//
// Only two things are: a CORS preflight, which carries no credentials to
// evaluate, and Overcast's own internal endpoints. The latter are recognised
// by the "/_" prefix and nothing else — S3 bucket names cannot begin with an
// underscore, so that prefix is reserved against every service AWS has modeled
// and every service it may model later.
//
// It is the only prefix with that property, which is why there is no third
// arm here. "/api" used to be one, added for a console BFF that has never
// passed through this middleware — IAMEnforce is wired once, on the AWS mux at
// router.go, and the UI is a separate listener. It exempted two real things:
// an S3 bucket named "api", which is a legal name, and later every operation
// of MSK's v2 cluster API, once #894 bound it to /api/v2/clusters — the path
// the pinned kafka model gives it.
//
// So adding a prefix here asserts two things, not one: that AWS models no
// service under it, and that it is not a name S3 will accept.
//
// # Internal does not mean unauthenticated
//
// The namespace answers "who invented this path", which is not the same
// question as "may anyone call it". Most internal endpoints carry no
// authorization of their own and the prefix test is the whole answer for them.
// A few do: the emulator-only Lambda endpoints read and write a function's
// source and its saved test events, authorized per function, and
// TestIAMEnforce_enabled_lambdaTestEventsPathDeniesNonMatchingFunction pins
// that a policy naming one function must not authorize another.
//
// Those two meanings rode on the same "/_" test until phase 6 of
// docs/plans/non-canonical-url-namespace.md, which is why moving the routes
// into the namespace deleted their authorization and had to be backed out of
// phase 4. They are separated by asking overcastRESTOperation — the table that
// already enumerates exactly the internal routes carrying an operation name,
// for the logger and for IAM alike. An internal path it can name is a path
// with something to authorize, so it is not exempt.
//
// A route marker would be the obvious alternative and is not available:
// IAMEnforce is registered with r.Use, so it runs before chi resolves which
// route matched and there is no route to read a marker from.
func shouldBypassIAM(r *http.Request) bool {
	if r.Method == http.MethodOptions {
		return true
	}
	if !strings.HasPrefix(r.URL.Path, InternalPrefix) {
		return false
	}
	_, named := overcastRESTOperation(detectService(r), r.Method, r.URL.Path)
	return !named
}

func isSignedIAMRequest(r *http.Request) bool {
	if strings.TrimSpace(r.Header.Get("Authorization")) != "" {
		return true
	}
	q := r.URL.Query()
	if q.Get("X-Amz-Signature") != "" {
		return true
	}
	if q.Get("X-Amz-Algorithm") != "" {
		return true
	}
	return false
}

// writeIAMAccessDenied answers a denied request in the error envelope its
// client reads: a routed Query request's is Query XML whichever service serves
// it, and everything else's is its service's.
func writeIAMAccessDenied(w http.ResponseWriter, r *http.Request, op iamOperation) {
	if op.query {
		writeQueryAccessDenied(w, r)
		return
	}
	switch op.service {
	case "s3", "cloudfront":
		protocol.WriteXMLError(w, r, &protocol.AWSError{
			Code:       "AccessDenied",
			Message:    "Access Denied",
			HTTPStatus: http.StatusForbidden,
		})
	// MSK is deliberately absent: it is a REST-JSON service and its handlers
	// write JSON errors (internal/services/msk/handler.go), so a denial in the
	// Query XML envelope is a shape no MSK client can parse. It was listed here
	// and unreachable for most of MSK's surface, because a signed request was
	// classified by its "kafka" signing name and fell to the default; naming
	// the service correctly is what would have made the wrong envelope real.
	case "sns", "iam", "sts", "ec2", "cloudformation", "rds", "ses", "cloudwatch", "acm", "kinesis", "kms", "ssm", "stepfunctions", "ecs", "ecr", "glue", "firehose", "athena", "elasticache", "waf", "shield", "autoscaling", "route53", "elbv2", "organizations":
		writeQueryAccessDenied(w, r)
	default:
		protocol.WriteJSONError(w, r, &protocol.AWSError{
			Code:       "AccessDeniedException",
			Message:    "User is not authorized to perform this action",
			HTTPStatus: http.StatusForbidden,
		})
	}
}

func writeQueryAccessDenied(w http.ResponseWriter, r *http.Request) {
	protocol.WriteQueryXMLError(w, r, &protocol.AWSError{
		Code:       "AccessDenied",
		Message:    "User is not authorized to perform this action",
		HTTPStatus: http.StatusForbidden,
	})
}

// buildIAMRequestContext constructs the set of IAM condition context keys that
// are derivable from the HTTP request alone (no store access needed).
// Supported keys: aws:RequestedRegion, aws:SourceIp, aws:CurrentTime.
func buildIAMRequestContext(r *http.Request, svc string) map[string]string {
	ctx := make(map[string]string, 3)

	// aws:RequestedRegion — extracted from the SigV4 credential scope.
	if region := requestedRegionFromSigV4(r); region != "" {
		ctx["aws:requestedregion"] = region
	}

	// aws:SourceIp — client IP with port stripped.
	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		ip = r.RemoteAddr
	}
	if ip = strings.TrimSpace(ip); ip != "" {
		ctx["aws:sourceip"] = ip
	}

	// aws:CurrentTime — derived from SigV4 timestamp when available.
	dateRaw := strings.TrimSpace(r.Header.Get("X-Amz-Date"))
	if dateRaw == "" {
		dateRaw = strings.TrimSpace(r.URL.Query().Get("X-Amz-Date"))
	}
	if dateRaw != "" {
		if ts, err := time.Parse("20060102T150405Z", dateRaw); err == nil {
			ctx["aws:currenttime"] = ts.UTC().Format(time.RFC3339)
		}
	}

	// aws:RequestedContentLength — Content-Length of the request body, when set.
	if cl := r.ContentLength; cl >= 0 {
		ctx["aws:requestedcontentlength"] = strconv.FormatInt(cl, 10)
	}

	if populate, ok := serviceConditionKeyPopulators[svc]; ok {
		for k, v := range populate(r) {
			ctx[k] = v
		}
	}

	return ctx
}

// serviceConditionKeyPopulators is the extension point for condition-key
// context that only makes sense for one service, keyed by detectService's
// name for it. buildIAMRequestContext dispatches through this map rather than
// branching on service name itself, so the next service-specific key
// (an sqs:* key, another s3:* key, …) is a new map entry, not a growing
// if/switch in the request-context builder.
var serviceConditionKeyPopulators = map[string]func(*http.Request) map[string]string{
	"s3": s3ConditionKeys,
}

// s3ConditionKeys populates s3:x-amz-bucket-namespace from the
// X-Amz-Bucket-Namespace request header (see s3.CreateBucket), but only when
// the header is present — a header-less request leaves the key genuinely
// absent from the IAM condition context, matching how AWS scopes a
// service-specific condition key to the requests that actually carry it
// (compare aws:SourceArn, documented the same way: "The aws:SourceArn key is
// present in the request context only if a resource triggers a service to
// call another service...").
//
// This key previously defaulted a missing header to "global" (CreateBucket's
// own default) purely to compensate for a since-fixed bug: the condition
// evaluator treated a missing context key as "not matched" for every
// operator, including negated ones, so AWS's own published guard —
// Deny+StringNotEquals{"s3:x-amz-bucket-namespace":"account-regional"} —
// never matched a header-less request and the Deny silently failed to apply
// (#1475). Per "IAM JSON policy elements: Condition operators", a negated
// operator like StringNotEquals evaluates to *matched* when its key is
// absent:
// https://docs.aws.amazon.com/IAM/latest/UserGuide/reference_policies_elements_condition_operators.html
// so with evaluateConditions fixed (internal/iampolicy/condition.go), the
// Deny pattern denies a header-less request without any default here.
//
// The default also produced its own divergence from AWS: with the key
// always present, StringEquals{"s3:x-amz-bucket-namespace":"global"} matched
// every header-less S3 request in Overcast, where on AWS a StringEquals
// against a key the request never carried does not match at all. Not
// populating the key when the header is absent removes that divergence too.
func s3ConditionKeys(r *http.Request) map[string]string {
	// Deliberately a local literal, not an import of internal/services/s3:
	// this is the middleware layer, imported by every service, so it cannot
	// import down into one service's package without inverting that
	// dependency. The header name itself (which AWS defines) is a wire
	// constant, not an implementation detail — s3.CreateBucket carries the
	// same literal for the same reason.
	const bucketNamespaceHeader = "X-Amz-Bucket-Namespace"

	namespace := strings.TrimSpace(r.Header.Get(bucketNamespaceHeader))
	if namespace == "" {
		return nil
	}
	return map[string]string{
		"s3:x-amz-bucket-namespace": namespace,
	}
}
