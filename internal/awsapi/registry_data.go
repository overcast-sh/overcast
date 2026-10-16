package awsapi

// targetOperation, queryOperation, and the REST trie types are populated by
// awsmodelgen. They are intentionally private immutable generated data;
// Registry is the only runtime access point.
type targetOperation struct {
	Target       string
	ModelService string
	Operation    string
	Protocol     Protocol
	Ambiguous    bool
}

type queryOperation struct {
	Version      string
	Operation    string
	ModelService string
	Protocol     Protocol
	Ambiguous    bool
}

// restTrieNode stores indexes into the immutable literal-edge and operation
// tables. Parameter and greedy nodes use -1 when the corresponding edge is
// absent. Keeping only indexes here makes the generated trie compact and lets
// ClaimREST walk it without building maps during router startup.
type restTrieNode struct {
	LiteralStart   int
	LiteralEnd     int
	Parameter      int
	Greedy         int
	OperationStart int
	OperationEnd   int
}

type restTrieEdge struct {
	Segment string
	Node    int
}

type restOperation struct {
	Method       string
	Query        string
	ModelService string
	SigningName  string
	Operation    string
	Protocol     Protocol
	Ambiguous    bool
	// CandidateStart and CandidateEnd bound this binding's window in
	// restCandidates. They are set only when Ambiguous, because only then is
	// there more than one answer to retain.
	CandidateStart int
	CandidateEnd   int
}

// restCandidate is one modeled service's operation at a REST binding that
// several services declare. It is what lets an ambiguous binding stay
// unattributed without also being unanswerable: the shared entry in
// restOperations still names no owner, while a caller that has already
// classified the request's service intersects this set and gets that service's
// own operation — or nothing, when the service declares no binding here.
//
// Only REST bindings need it. A target, Query or RPC key contains the
// operation name, so every service colliding on one of those necessarily
// declares the same name and the retained name is already exact; a REST key is
// a method and URI shape, which distinct services routinely bind to
// differently named operations (GET /v2/apis is API Gateway v2's GetApis and
// AppSync's ListApis).
type restCandidate struct {
	ModelService string
	Operation    string
}

type rpcOperation struct {
	Protocol     Protocol
	ServiceShape string
	Operation    string
	ModelService string
	Ambiguous    bool
}

type serviceAlias struct {
	ModelService    string
	OvercastService string
}

// operationCollision is generated when distinct modeled services have the
// same wire identifier. Registry preserves the shared protocol/error profile
// but deliberately does not attribute the operation to either service.
//
// It is a diagnostic index keyed by a readable wire identifier, reported by
// the model inventory and asserted by tests. The runtime equivalent for REST
// bindings is restCandidates, which is reachable from a matched binding and
// carries each service's operation name.
type operationCollision struct {
	Key      string
	Services []string
}

// serviceAliases must stay strictly sorted by ModelService for binary search.
//
// "cognito-identity" (Cognito Federated Identities, identity pools) is
// deliberately absent: Overcast's cognito service implements only
// cognito-identity-provider (user pools), so identity-pool operations must
// keep their own identity rather than validate against the cognito key.
var serviceAliases = []serviceAlias{
	{ModelService: "api-gateway", OvercastService: "apigateway"},
	{ModelService: "apigatewayv2", OvercastService: "apigateway"},
	{ModelService: "auto-scaling", OvercastService: "autoscaling"},
	{ModelService: "bedrock-runtime", OvercastService: "bedrock"},
	// EventBridge under its former name. The models carry the same service
	// twice — "cloudwatch-events" declares 51 operations, every one of them
	// also declared by "eventbridge", and it binds no REST URI of its own — so
	// the two identities collide on all 51 X-Amz-Targets and awsmodelgen
	// blanks the service for each. Aliasing them together is what makes that a
	// non-collision: nothing has to be chosen between a service and itself.
	// Without it every EventBridge JSON call was classified as the S3
	// fallback. See ambiguity.go.
	{ModelService: "cloudwatch-events", OvercastService: "eventbridge"},
	{ModelService: "cognito-identity-provider", OvercastService: "cognito"},
	{ModelService: "dynamodb-streams", OvercastService: "dynamodbstreams"},
	{ModelService: "elastic-load-balancing-v2", OvercastService: "elbv2"},
	{ModelService: "kafka", OvercastService: "msk"},
	// Lambda Web Functions. Overcast keys the service "lambdaweb": the key is the
	// OVERCAST_STATE_<SERVICE> name, which cannot hold a hyphen.
	{ModelService: "lambda-web", OvercastService: "lambdaweb"},
	{ModelService: "route-53", OvercastService: "route53"},
	{ModelService: "secrets-manager", OvercastService: "secretsmanager"},
	{ModelService: "service-catalog-appregistry", OvercastService: "appregistry"},
	// Overcast's ses service implements both SES v1 (Query) and SES v2
	// (REST-JSON) operations under the single "ses" key.
	{ModelService: "sesv2", OvercastService: "ses"},
	{ModelService: "sfn", OvercastService: "stepfunctions"},
	// Overcast's waf service implements WAF v2 (AWSWAF_20190729), which the
	// model names "wafv2". The model's "waf" identity is WAF *Classic*
	// (AWSWAF_20150824) — unimplemented, and its operation names overlap with
	// v2 (CreateWebACL et al.), so it is diverted to the deliberately
	// unregistered "waf-classic" key to keep classic operations from silently
	// validating capability rows declared against the v2-implementing "waf".
	{ModelService: "waf", OvercastService: "waf-classic"},
	{ModelService: "wafv2", OvercastService: "waf"},
}
