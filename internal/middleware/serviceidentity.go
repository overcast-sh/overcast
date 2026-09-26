package middleware

import (
	"net/http"
	"strings"
)

// An AWS service answers to three names, and middleware needs all three.
//
//   - The **Overcast service key** ("msk") is the one this package already had.
//     It routes the request, labels the log line and the trace badge, keys the
//     generated operation registry, and selects the resource resolver and the
//     error envelope in iam_enforce.go.
//   - The **SigV4 signing name** ("kafka") is the service component of the
//     Authorization credential scope. It is what a real SDK sends, and it is
//     therefore the only service signal detectService has for any path its
//     prefix switch does not claim.
//   - The **IAM action prefix** ("kafka:") is what a policy author writes,
//     because it is what the AWS documentation gives them.
//
// For most services all three are the same string, which is why one name was
// enough for a long time and why the places that assumed it read naturally.
// Where they diverge the assumption fails in two different ways, and this file
// holds the two mappings that stop it.
//
// The two are **not inverses of each other**, and CloudWatch is the standing
// proof: it signs as "monitoring" and authorizes as "cloudwatch:". Deriving
// either from the other would get one of them wrong.
//
// Both are switches rather than tables because detectService is on the request
// path for every request: a switch over string constants compiles to a length
// check and a comparison chain, allocates nothing, and needs no init.

// serviceKeyForSigningName maps a SigV4 credential scope's service component
// onto Overcast's service key. It is applied wherever the scope is read as a
// classification signal, because the raw signing name is a name nothing
// downstream of detectService knows: the generated registry is keyed by
// Overcast's key, so restOperation could name no operation for a scope-
// classified request, and requestIAMResource's switch matched no arm. Both
// failures are silent, and the first one lands in IAMEnforce's deliberate
// fail-open branch for unnamed actions — so a signed, unauthorized request to
// MSK's entire v1 surface was passed through with no policy evaluated.
//
// Everything not named here is returned unchanged, which is correct for the
// large majority of services and for every signing name Overcast does not
// serve. TestDetectServiceAlwaysAnswersWithAServiceKey walks the real routing
// table against every signing name the pinned models declare and fails if any
// of them still classifies as itself.
//
// Three signing names are deliberately absent because an implemented pair
// shares them and the scope alone cannot separate the two:
//
//   - "appconfig" is AppConfig's control plane and AppConfig Data. detectService
//     claims Data's two bindings by path before the scope is read.
//   - "dynamodb" is DynamoDB and DynamoDB Streams, told apart by X-Amz-Target
//     at step 1.
//   - "execute-api" is API Gateway's *data* plane. A request carrying it is
//     traffic to a customer's own API, not a management call, and mapping it to
//     "apigateway" would authorize it as one.
//
// In each case the broader service is the right answer for a scope that reaches
// step 3b, so the identity default already gives it.
func serviceKeyForSigningName(signingName string) string {
	switch signingName {
	case "kafka":
		return "msk"
	case "servicecatalog":
		// Service Catalog AppRegistry. Overcast does not implement Service
		// Catalog proper, which shares the name.
		return "appregistry"
	case "elasticfilesystem":
		return "efs"
	case "es":
		// OpenSearch. The models give the same signing name to the legacy
		// Elasticsearch Service identity, which Overcast does not implement.
		return "opensearch"
	case "monitoring":
		return "cloudwatch"
	case "elasticloadbalancing":
		// ELBv2. ELB *Classic* signs with the same name, so this arm can only
		// name one of the two — and the scope was never what separates them.
		// Both are AWS Query on POST "/", where the API version names the
		// service exactly (2012-06-01 Classic, 2015-12-01 v2), so
		// detectService reads that first and reaches this arm only for a
		// request carrying no version at all. ELBv2 is the right answer there:
		// it is the ELB service Overcast serves, and it is what such a request
		// reached before the version was consulted. See #1884.
		return "elbv2"
	case "states":
		return "stepfunctions"
	case "cognito-idp":
		// Cognito user pools. "cognito-identity" (federated identity pools) is
		// deliberately not mapped, for the reason awsapi's serviceAliases gives:
		// Overcast's cognito service implements user pools only.
		return "cognito"
	case "wafv2":
		// WAF v2, which Overcast's "waf" key implements. WAF Classic signs as
		// "waf" and is unimplemented, so it correctly falls through unchanged.
		return "waf"
	default:
		return signingName
	}
}

// iamActionPrefix maps Overcast's service key onto the IAM action prefix AWS
// authorizes that service under — the "kafka" in "kafka:ListClusters".
//
// requestIAMAction built the action from the service key, which is not a name
// AWS uses in a policy anywhere. Where the two differ, a policy written from
// the AWS documentation — the only place a user can read the right name — did
// not match the action Overcast evaluated, and the failure is silent in both
// directions: an Allow granted nothing and a Deny denied nothing.
//
// Everything not named here is returned unchanged. That is not an accident of
// the list being short: for most services the key was chosen to be AWS's name
// in the first place, including the four that would otherwise be surprising —
// EventBridge authorizes as "events:", CloudWatch Logs as "logs:", Bedrock
// Runtime as "bedrock:", and CloudWatch as "cloudwatch:" despite signing as
// "monitoring".
func iamActionPrefix(serviceKey string) string {
	switch serviceKey {
	case "msk":
		return "kafka"
	case "appregistry":
		return "servicecatalog"
	case "efs":
		return "elasticfilesystem"
	case "opensearch":
		return "es"
	case "elbv2":
		return "elasticloadbalancing"
	case "elastic-load-balancing":
		// ELB Classic, which detectService now names for a 2012-06-01 Query
		// request. AWS authorizes both ELB services under the one prefix, so
		// this key and "elbv2" deliberately answer with the same string; the
		// key is the model's identity for a service Overcast does not
		// implement, and without this arm a Classic call would be authorized
		// against a prefix no policy can name.
		return "elasticloadbalancing"
	case "stepfunctions":
		return "states"
	case "cognito":
		return "cognito-idp"
	case "waf":
		return "wafv2"
	case "dynamodbstreams":
		// Streams operations are authorized under DynamoDB's own prefix —
		// dynamodb:GetRecords, dynamodb:DescribeStream.
		return "dynamodb"
	case "appconfigdata":
		return "appconfig"
	default:
		return serviceKey
	}
}

// s3ControlAccountHeader is sent by S3 Control on every operation and never by
// S3 itself. It is what separates the two APIs, which share a signing name.
const s3ControlAccountHeader = "X-Amz-Account-Id"

// SignedForS3API reports whether a request whose credential scope names
// signingName is signed for the S3 object API: an S3-family signing name, and
// not S3 Control. It is the positive evidence of S3 the router reads where a
// path could belong to S3 or to another service.
func SignedForS3API(r *http.Request, signingName string) bool {
	return isS3APISigningName(signingName) && r.Header.Get(s3ControlAccountHeader) == ""
}

// AddressesS3 reports whether a request positively addresses the S3 object
// API: it was virtual-hosted to a bucket, or it is signed for S3
// (SignedForS3API) under signingName, its credential scope's service. Where a
// path could belong to S3 or to another service, either settles it for S3.
func AddressesS3(r *http.Request, signingName string) bool {
	if claim, ok := HostClaimFromContext(r.Context()); ok && claim.Kind == HostClaimS3 {
		return true
	}
	return SignedForS3API(r, signingName)
}

// isS3APISigningName reports whether a SigV4 credential scope belongs to a
// service that speaks the S3 object API itself, and whose traffic must
// therefore reach S3's routes. Object Lambda and S3 Express are S3 under
// another signing name.
//
// "s3-outposts" is deliberately absent. It signs the separate s3outposts
// control API, whose modeled paths cannot be mistaken for an object path.
func isS3APISigningName(signingName string) bool {
	switch strings.ToLower(signingName) {
	case "s3", "s3-object-lambda", "s3express":
		return true
	}
	return false
}
