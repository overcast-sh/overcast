package middleware

import (
	"bufio"
	"bytes"
	"net"
	"net/http"
	"net/url"
	"strings"

	"go.uber.org/zap"

	"github.com/overcast-sh/overcast/internal/awsapi"
	"github.com/overcast-sh/overcast/internal/clock"
	"github.com/overcast-sh/overcast/internal/protocol"
	"github.com/overcast-sh/overcast/internal/serviceutil"
	"github.com/overcast-sh/overcast/internal/trace"
)

// detectService infers the AWS service from a request using the same signals
// the real AWS SDKs embed: X-Amz-Target prefix (JSON services), well-known URL
// prefixes (Lambda REST API), the Smithy RPC v2 URI and its Smithy-Protocol
// header, the Query-protocol Action and Version form parameters (IAM, STS, SNS,
// EC2 and the rest of the Query services), the Authorization Credential scope,
// and finally S3 as a fallback.
// An optional body parameter enables Query-protocol Action-param detection;
// without it a Query request can only be classified by its credential scope.
//
// What it answers is not only a log label. IAM enforcement builds the action it
// authorises from it (see requestIAMAction, which maps the answer to AWS's IAM
// action prefix), the resource resolvers below it branch on the same value, and
// region handling and the trace badges read it too. A changed answer here
// changes which IAM action a policy is evaluated against — treat it as
// behaviour, not presentation.
//
// Every one of those consumers wants an **Overcast service key**, which is one
// of the three names a service has. serviceidentity.go holds the other two and
// the mappings between them; the short version is that the answer here is never
// a SigV4 signing name, even though step 3b reads one.
//
// The step 2 prefix switch is hand-written and stays that way deliberately. The
// pinned Smithy models describe the same paths — every REST binding's URI, and
// for a shared binding the set of services declaring it — but step 2 cannot be
// derived from them:
//
//   - Scale, against S3. The corpus has roughly 1,950 root path segments owned
//     by exactly one modeled service, against the dozen claimed here, and
//     almost all of them are legal S3 bucket names. Step 2 runs ahead of the
//     credential scope, so a derived table would take "GET /clusters" away from
//     a bucket named clusters. The router's restFallback consults the same
//     model trie safely only because addressesNonS3 gates it on positive
//     non-S3 evidence from the credential scope — evidence that is by
//     definition absent for the unsigned traffic step 2 exists to serve.
//   - The credential scope cannot lead instead, for the reason in the bullet
//     above: it is absent from the unsigned traffic step 2 exists to serve.
//     What it *can* now do is answer correctly when it is present. The scope
//     carries the SigV4 signing name, which is not this function's key for
//     several implemented services — elasticfilesystem/efs, kafka/msk,
//     servicecatalog/appregistry, states/stepfunctions, monitoring/cloudwatch,
//     elasticloadbalancing/elbv2 — and step 3b used to return it raw, so a
//     signed request to any of their paths that step 2 does not claim was
//     labelled with a name nothing downstream knows. serviceKeyForSigningName
//     (serviceidentity.go) translates it; step 3b is why MSK's v1 surface no
//     longer escapes IAM enforcement. It cannot separate two services AWS
//     gives one signing name, which is why the Query wire is read at step 3
//     ahead of it — see #1884 and serviceKeyForSigningName's ELB arm.
//   - Three entries here are ambiguous in the models and would have to be given
//     up. "/applications" is declared by eight modeled services, "/v1/tags" by
//     twelve, "/2017-03-31/" by Lambda and Lambda MicroVMs. Answering "" for
//     them — the rule the rest of the model-derived work follows when several
//     services are candidates — is not neutral here, because "" falls through
//     to the S3 fallback. It would relabel three families that are correct now.
//     Two of the ambiguous entries — "/v2/apis" and "/applications" — are
//     shared by services Overcast actually implements, so they read the
//     credential scope first and fall back to the prefix's historical owner.
//     That is a narrower rule than deriving the switch: it costs nothing for
//     unsigned traffic and answers the signed case correctly.
//
// Three tests cover what that leaves open. Two walk the real router:
// TestDetectServiceClassifiesEveryRegisteredRouteFamily fails when a service
// registers a path family the switch has never been told about, classifying
// unsigned. TestDetectServiceClassifiesEverySignedRouteByItsSigningName signs
// each route the way the pinned models say that binding is signed, which is the
// case the switch does not decide and step 3b does.
//
// Both vary only the path, so neither can say anything about the protocols that
// name the service in the request *content* rather than its URL — the wires
// steps 1, 2d and 3 read. TestClassifiesEveryDeclaredOperationOnItsContentWire
// is that third axis: it sweeps every operation the capability registry
// declares against the shapes the models say a client addresses it with, it
// asserts the operation as well as the service, and it runs each case through
// the two middlewares that do the labelling rather than calling this function —
// because the failure it exists to catch is a caller that classifies with less
// than it has, which a direct call cannot reproduce.
func detectService(r *http.Request, body ...[]byte) string {
	// 1. X-Amz-Target — use the generated AWS operation registry for
	// accurate service mapping across all JSON-protocol services.
	if t := r.Header.Get("X-Amz-Target"); t != "" {
		if claim, ok := awsapi.NewRegistry().ClaimTarget(t); ok && claim.Service != "" {
			return middlewareServiceKey(claim.Service)
		}
	}

	// 2. Well-known URL prefixes — covers REST-protocol services.
	switch {
	case isLambdaAPIVersionPrefix(r.URL.Path):
		return "lambda"
	case strings.HasPrefix(r.URL.Path, "/2015-02-01/"):
		return "efs"
	case strings.HasPrefix(r.URL.Path, "/v1/pipes"):
		return "pipes"
	case strings.HasPrefix(r.URL.Path, "/v1/apis"),
		strings.HasPrefix(r.URL.Path, "/v1/tags"),
		strings.HasPrefix(r.URL.Path, "/v1/domainnames"),
		strings.HasPrefix(r.URL.Path, "/v1/mergedApis"),
		strings.HasPrefix(r.URL.Path, "/v1/sourceApis"),
		// The two API-independent evaluation endpoints (#860). No other
		// modeled service binds a /v1/dataplane- path, and without this
		// entry an unsigned POST there is labelled — and IAM-authorised —
		// as s3, because step 2 runs ahead of the credential scope.
		strings.HasPrefix(r.URL.Path, "/v1/dataplane-"):
		return "appsync"
	case strings.HasPrefix(r.URL.Path, "/api/v2/clusters"):
		// MSK's v2 cluster API. This is the only entry whose root segment
		// ("/api") no other implemented service uses, and the only reason it
		// needs claiming: without it an unsigned request to /api/v2/clusters
		// falls through to S3 and is read as the object v2/clusters in a
		// bucket named api. kafka is the only modeled service binding anything
		// under /api/v2, so there is nothing for the credential scope to
		// disambiguate.
		return "msk"
	case strings.HasPrefix(r.URL.Path, "/backup-vaults"),
		strings.HasPrefix(r.URL.Path, "/backup/plans"),
		strings.HasPrefix(r.URL.Path, "/backup-access-point"):
		// AWS Backup's three modeled subtrees. Each is claimed at its full
		// prefix rather than at a "/backup" root, because "backup" is a legal
		// S3 bucket name and this switch runs ahead of the credential scope:
		// claiming the root would take every object in a bucket called backup
		// away from S3. Claiming these three takes only the paths Backup's own
		// routes already answer, so the label agrees with what happened to the
		// request. No other modeled service binds a URI under any of them.
		//
		// /backup-access-point is the Backup Access Point operations' root
		// (#1467). Without it an unsigned request there is labelled — and IAM
		// authorised — as an s3 request to a bucket called
		// "backup-access-point".
		return "backup"
	case strings.HasPrefix(r.URL.Path, "/v2/email/"):
		return "ses"
	case strings.HasPrefix(r.URL.Path, "/2020-05-31/"):
		return "cloudfront"
	case strings.HasPrefix(r.URL.Path, "/2013-04-01/"):
		return "route53"
	case strings.HasPrefix(r.URL.Path, "/2021-01-01/"):
		// OpenSearch's whole surface, domains and tags alike. It is the only
		// modeled service that binds this version prefix, so there is nothing
		// for the credential scope to disambiguate — and leaving it to the
		// scope would mislabel every request anyway, because OpenSearch signs
		// as "es" while this function's key for it is "opensearch".
		return "opensearch"
	case strings.HasPrefix(r.URL.Path, "/v2/apis"):
		if svc := serviceFromAuthCredential(r); svc == "appsync" {
			return "appsync"
		}
		return "apigateway"
	case strings.HasPrefix(r.URL.Path, "/restapis"),
		strings.HasPrefix(r.URL.Path, "/apikeys"),
		strings.HasPrefix(r.URL.Path, "/usageplans"):
		return "apigateway"
	case r.URL.Path == "/applications", strings.HasPrefix(r.URL.Path, "/applications/"):
		// Shared with AppConfig, which models the same tree, so this is the
		// second entry that has to consult the credential scope before
		// answering — see /v2/apis above, and router.go's applicationsDispatch,
		// which resolves the route by the same signal. AppRegistry stays the
		// answer for its own "servicecatalog" scope and for unsigned callers,
		// which is what the web UI sends.
		//
		// A virtual-hosted or S3-signed request is S3's: "applications" is a
		// legal bucket name, and the router sends such a request there
		// (sharedRoots, #2098). The segment boundary above leaves a bucket
		// such as "applications-archive" to the S3 default.
		switch svc := serviceFromAuthCredential(r); {
		case AddressesS3(r, svc):
			return "s3"
		case svc == "appconfig":
			return "appconfig"
		}
		return "appregistry"
	case r.URL.Path == "/configurationsessions":
		// StartConfigurationSession, which only AppConfig Data models. Matched
		// exactly rather than by prefix: the model binds nothing beneath it,
		// and this step runs ahead of the credential scope, so a prefix would
		// take "/configurationsessions-backup" from a bucket of that name for
		// nothing.
		return "appconfigdata"
	case r.URL.Path == "/configuration":
		// GetLatestConfiguration. Three modeled services bind this path —
		// AppConfig Data, Omics and Service Catalog AppRegistry — so like
		// /v2/apis above it asks the credential scope before answering.
		// Overcast serves only AppConfig Data's binding here, but the label
		// picks the IAM action, and a caller that named another service (or a
		// genuine S3 request to a bucket called "configuration") must keep the
		// classification it had before this route existed. AppConfig Data signs
		// as "appconfig", which it shares with the AppConfig control plane;
		// only AppConfig Data binds this path.
		// Translated to a service key, not returned raw: this arm answers with
		// whatever the caller named, and every other branch of this function
		// returns a key.
		if svc := serviceKeyFromAuthCredential(r); svc != "" && svc != "appconfig" {
			return svc
		}
		return "appconfigdata"
	case strings.HasPrefix(r.URL.Path, "/schedules"),
		strings.HasPrefix(r.URL.Path, "/schedule-groups"):
		// EventBridge Scheduler's entire non-tag surface. DataBrew is the only
		// other modeled service binding these paths and Overcast does not
		// implement it, so there is no ambiguity for the credential scope to
		// resolve. Scheduler's tag operations stay unclaimed here: /tags is
		// shared with API Gateway, EKS and Pipes and is dispatched on the
		// resource ARN (see router.go).
		return "scheduler"
	case isBedrockRuntimeInferencePath(r.URL.Path):
		return "bedrock"
	case r.URL.Path == "/_overcast/events":
		return "events"
	case r.URL.Path == "/_overcast/metrics":
		return "metrics"
	}

	// 2b. Host-routed AWS-style addresses (execute-api / lambda-url /
	// appsync-api Host subdomains — see hostroute.go). Read from the claim
	// HostAddressing stamped on this request, so the label is what actually
	// routed it rather than a re-derivation that could disagree — notably
	// when OVERCAST_HOSTNAME itself contains a registered label, where S3
	// virtual-hosted addressing legitimately wins.
	if claim, ok := HostClaimFromContext(r.Context()); ok && claim.Kind == HostClaimHostRoute {
		if svc, found := HostRouteServiceFor(claim.Route); found {
			return svc
		}
	}

	// 2c. Emulator-internal /_-prefixed paths — S3 bucket names cannot start
	// with '_', so any /_* path is definitively not S3. Map known service
	// prefixes to their owner; everything else is "internal".
	if strings.HasPrefix(r.URL.Path, "/_") {
		return internalService(r.URL.Path)
	}

	// 2d. Smithy RPC v2 — /service/{serviceShape}/operation/{operation}, where
	// the path names both outright.
	//
	// Gated on the Smithy-Protocol header, and that gate is the whole content
	// of this step rather than a detail of it: without the header the router
	// hands this exact path to S3 (smithyRPCDispatch), where "service" is an
	// ordinary bucket name and the object key is the rest — so s3 is then the
	// correct answer, and claiming the prefix unconditionally would take a
	// genuine S3 request away from it.
	if claim, ok := smithyRPCClaim(r); ok && claim.Service != "" {
		return middlewareServiceKey(claim.Service)
	}

	// 3. Query-protocol Action and Version — the AWS Query wire names its own
	// service in the request content, as a pair the generated registry
	// attributes to exactly one modeled service.
	//
	// It leads the credential scope below because it is the stronger signal and
	// because it is the signal the *router* dispatches a Query request on
	// (targetDispatch's queryOwner): a classifier that read the scope instead
	// could label a request with a service that did not answer it. Elastic Load
	// Balancing is where the two disagree — Classic (2012-06-01) and ELBv2
	// (2015-12-01) are both Query on POST "/" and both sign as
	// "elasticloadbalancing", so the scope can only name one of them and named
	// ELBv2 for every Classic call (#1884).
	//
	// A request with no version, or one the models do not carry, falls through
	// to the scope, which is what classified it before this step moved.
	if len(body) > 0 && len(body[0]) > 0 && bytes.Contains(body[0][:min(len(body[0]), 256)], []byte("Action=")) {
		values, err := url.ParseQuery(string(body[0]))
		if err == nil {
			// claim.Service != "" for the same reason step 1 checks it: the
			// generated index blanks the service of an action several modeled
			// services declare rather than guess between them, and returning
			// that blank labelled the request with the empty string — which is
			// not a service key, reaches IAM enforcement as one, and is not
			// even the documented s3 fall-through. RDS is the live case, its
			// whole Query surface sharing an API version with DocumentDB and
			// Neptune.
			if claim, ok := awsapi.NewRegistry().ClaimQuery(values.Get("Version"), values.Get("Action")); ok && claim.Service != "" {
				return middlewareServiceKey(claim.Service)
			}
		}
	}

	// 3b. Authorization Credential scope — covers the Query-protocol services
	// whose request body this middleware never saw (see
	// TestDetectService_withoutTheBodyCannotClassifyQuery), and every REST
	// service whose path step 2 does not claim.
	// Format: AWS4-HMAC-SHA256 Credential=AKID/DATE/REGION/SERVICE/aws4_request
	if svc := serviceKeyFromAuthCredential(r); svc != "" && svc != "s3" {
		return svc
	}

	// 4. S3 is the final fallback: S3 uses plain HTTP verbs on path-style or
	// virtual-hosted URLs with no distinguishing header, so there is no
	// positive signal to match on.
	return "s3"
}

// smithyRPCClaim classifies a Smithy RPC v2 request from its URI and the
// Smithy-Protocol header, the same two signals the router's smithyRPCDispatch
// resolves the request with. Both have to read the wire the same way: the
// router deciding a request is RPC v2 while the classifier calls it S3 is how
// a whole protocol comes to be logged, traced and IAM-authorised as something
// it is not.
//
// The RPC v2 URI is fixed by the protocol at /service/{service}/operation/{op},
// so it is parsed here rather than read from chi's route parameters — this
// middleware runs before routing, and on requests chi never matches at all.
//
// Resolution follows the router's, in its order. The registry leads, because a
// modeled binding names the operation as the models spell it. When the models
// bind nothing — which is almost always, since they describe RPC v2 for one
// service Overcast implements — the label is tried as an Overcast service key,
// because that is the next thing smithyRPCServiceFor tries and it is a door
// that is genuinely open: `/service/ecs/operation/ListClusters` answers CBOR
// today, and ECS appears nowhere in the models' RPC v2 bindings. On that branch
// the router never reaches SupportedProtocols either, so following the models
// alone here left every such request labelled s3 with no operation — and IAM
// enforcement's unnamed-action branch lets those through.
//
// The operation is then taken from the URI as the caller wrote it, which is the
// same trust step 2 of detectOperationForService makes of `x-id`. It is not a
// licence to mislabel: the router dispatches on this very label, so what the
// classifier names and what answers the request are the same service by
// construction.
func smithyRPCClaim(r *http.Request) (awsapi.Claim, bool) {
	var wire awsapi.Protocol
	switch header := strings.TrimSpace(r.Header.Get("Smithy-Protocol")); {
	case strings.EqualFold(header, "rpc-v2-cbor"):
		wire = awsapi.ProtocolRPCV2CBOR
	case strings.EqualFold(header, "rpc-v2-json"):
		wire = awsapi.ProtocolRPCV2JSON
	default:
		return awsapi.Claim{}, false
	}
	rest, found := strings.CutPrefix(r.URL.Path, "/service/")
	if !found {
		return awsapi.Claim{}, false
	}
	serviceShape, operation, found := strings.Cut(rest, "/operation/")
	if !found || serviceShape == "" || operation == "" || strings.Contains(operation, "/") {
		return awsapi.Claim{}, false
	}
	// A modeled claim leads because it spells the operation as the models do,
	// but only when it names a service. An ambiguous one does not, and the
	// label below still does — the same reasoning smithyRPCServiceFor applies
	// by resolving the label against its dispatchers before it reads the claim
	// at all. The models bind no colliding RPC v2 operation today, so this is a
	// bridge for a model refresh rather than a live path; without it such a
	// refresh would quietly move a whole service back to the s3 fallback.
	if claim, ok := awsapi.NewRegistry().ClaimRPC(wire, serviceShape, operation); ok && claim.Service != "" {
		return claim, true
	}
	if key := serviceIdentityForRPCLabel(serviceShape); key != "" {
		return awsapi.Claim{
			Service:   key,
			Operation: operation,
			Protocol:  wire,
		}, true
	}
	return awsapi.Claim{}, false
}

// isLambdaAPIVersionPrefix reports whether a path is under one of Lambda's
// dated API version prefixes. Lambda is the one service that spreads its REST
// surface across many of them, and a prefix missing here sends that whole
// operation family into the S3 fallback's bucket/object shapes.
//
// The list is every version the pinned AWS models bind a Lambda operation to,
// not only the ones Overcast routes today: an unrouted Lambda path still gets
// a protocol-correct 501 from the generated registry, so labelling it lambda
// is what actually happened to it. TestDetectServiceCoversModeledLambdaVersions
// fails if a model refresh introduces one that is missing. No other modeled
// service shares any of these prefixes (only Lambda MicroVMs, which Overcast
// does not implement, shares /2017-03-31).
func isLambdaAPIVersionPrefix(path string) bool {
	switch {
	case strings.HasPrefix(path, "/2014-11-13/"),
		strings.HasPrefix(path, "/2015-03-31/"),
		strings.HasPrefix(path, "/2016-08-19/"),
		strings.HasPrefix(path, "/2017-03-31/"),
		strings.HasPrefix(path, "/2017-10-31/"),
		strings.HasPrefix(path, "/2018-10-31/"),
		strings.HasPrefix(path, "/2019-09-25/"),
		strings.HasPrefix(path, "/2019-09-30/"),
		strings.HasPrefix(path, "/2020-04-22/"),
		strings.HasPrefix(path, "/2020-06-30/"),
		strings.HasPrefix(path, "/2021-07-20/"),
		strings.HasPrefix(path, "/2021-10-31/"),
		strings.HasPrefix(path, "/2021-11-15/"),
		strings.HasPrefix(path, "/2024-08-31/"),
		strings.HasPrefix(path, "/2025-11-30/"),
		strings.HasPrefix(path, "/2025-12-01/"),
		strings.HasPrefix(path, "/2026-07-09/"):
		return true
	}
	return false
}

// isBedrockRuntimeInferencePath reports whether a path is one of Bedrock
// Runtime's per-model inference bindings, /model/{modelId}/<action>.
//
// This is the one entry in the switch above that matches a whole path shape
// rather than a prefix, and the reason is what its first segment is. "model" is
// an ordinary word and a perfectly legal S3 bucket name, and step 2 runs ahead
// of the credential scope, so a prefix claim would take every object in a
// bucket called "model" away from S3 with nothing left to correct it. Every
// modeled bedrock-runtime URI under /model ends in one of the actions below, so
// matching the last segment costs one comparison and collides only with an S3
// key that happens to spell one.
//
// modelId is a non-greedy label but its value may be an ARN, whose separators
// arrive percent-encoded on the wire and decoded in URL.Path — so the action is
// taken from the end of the path rather than from a fixed segment count.
//
// The list is every action the pinned models bind under /model/{modelId}, not
// only the two Overcast serves: an unrouted one still gets a protocol-correct
// 501 from the generated registry, so labelling it bedrock is what actually
// happened to it. Same reasoning as isLambdaAPIVersionPrefix.
func isBedrockRuntimeInferencePath(path string) bool {
	rest, ok := strings.CutPrefix(path, "/model/")
	if !ok {
		return false
	}
	separator := strings.LastIndexByte(rest, '/')
	if separator <= 0 {
		// No action segment, or an empty modelId before it.
		return false
	}
	switch rest[separator+1:] {
	case "invoke",
		"invoke-with-response-stream",
		"invoke-with-bidirectional-stream",
		"converse",
		"converse-stream",
		"count-tokens":
		return true
	}
	return false
}

// middlewareServiceKey translates the generated registry's service identity
// to the key middleware has always used where the two differ. CloudWatch Logs
// is modeled as "cloudwatch-logs" (its capability key), but its SigV4 signing
// name and real IAM action prefix are "logs" (logs:PutLogEvents), and every
// downstream switch — IAM enforcement, log labels, trace service badges —
// keys on "logs".
func middlewareServiceKey(s string) string {
	if s == "cloudwatch-logs" {
		return "logs"
	}
	return s
}

// middlewareServiceKeyOrigin is middlewareServiceKey backwards: it maps a key
// this package answers with to the identity the generated registry knows it by.
//
// Only one key is renamed, so this is one case — but it has to exist, because a
// Smithy RPC v2 label arrives already written in *this* package's namespace.
// "/service/logs/..." is what the router dispatches, and asking the registry
// whether "logs" is a service gets no for a name that plainly is one.
func middlewareServiceKeyOrigin(s string) string {
	if s == "logs" {
		return "cloudwatch-logs"
	}
	return s
}

// serviceIdentityForRPCLabel resolves a Smithy RPC v2 `{service}` URI label to
// the key this package classifies with, or "" when it names no service.
//
// The label can arrive in any of the three namespaces a service has, so each is
// tried in turn against the registry's notion of a real service. Order matters
// once: the label is treated as a key before it is treated as a modeled
// identity, because those two readings disagree for WAF — "waf" is Overcast's
// key for WAF v2, and is also the modeled identity of WAF *Classic*, which
// serviceAliases deliberately diverts to "waf-classic". Reading the label as a
// model identity first would answer with the unimplemented classic service for
// every v2 request.
func serviceIdentityForRPCLabel(label string) string {
	label = strings.ToLower(label)
	for _, candidate := range []string{middlewareServiceKeyOrigin(label), awsapi.ServiceKey(label)} {
		if awsapi.IsServiceKey(candidate) {
			return middlewareServiceKey(candidate)
		}
	}
	return ""
}

// internalService maps an emulator-internal /_-prefixed path to the service
// that owns it. S3 bucket names cannot start with '_', so any /_* path is
// definitively not an S3 request and must not fall through to the S3 fallback.
func internalService(path string) string {
	switch {
	// Every arm is now "/_overcast/" plus the owning service, because phase 5
	// was the last one that had a prefix outside the namespace to name.
	//
	// This is one step short of the collapse the plan promised — segment 2 of
	// the path *is* the answer here. It stays a switch because the namespace
	// holds more than services: /_overcast/health, /metrics, /debug, /events,
	// /info, /topology, /mcp, /init, /ca.pem and /domains all belong to the
	// emulator itself, and reading segment 2 blindly would invent "health" and
	// "debug" as service names. Collapsing it needs the set of real service
	// keys to check against, which phase 6 does along with the rest of the
	// predicate rework.
	case strings.HasPrefix(path, "/_overcast/cognito"):
		return "cognito"
	case strings.HasPrefix(path, "/_overcast/secretsmanager"):
		return "secretsmanager"
	case strings.HasPrefix(path, "/_overcast/ses"):
		// The mail inbox and the SES admin identity routes. Only the inbox was
		// named here before phase 4 moved it from /_overcast/inbox, so
		// /_overcast/ses/identities — already under this prefix — fell through
		// and was logged as "internal". Both are SES now.
		return "ses"
	case strings.HasPrefix(path, "/_overcast/ecs"):
		return "ecs"
	case strings.HasPrefix(path, "/_overcast/lambda"):
		return "lambda"
	case strings.HasPrefix(path, "/_overcast/rds"):
		return "rds"
	case strings.HasPrefix(path, "/_overcast/eks"):
		return "eks"
	case strings.HasPrefix(path, "/_overcast/appsync"):
		return "appsync"
	case strings.HasPrefix(path, "/_overcast/cloudfront"):
		return "cloudfront"
	case strings.HasPrefix(path, "/_overcast/athena"):
		return "athena"
	case strings.HasPrefix(path, "/_overcast/s3tables"):
		// The unsigned mount of S3 Tables' Iceberg REST catalog.
		return "s3tables"
	default:
		// Includes /_overcast/apigateway and /_overcast/elb, which were
		// "internal" before this phase too: host-routed traffic to either is
		// labelled from the HostClaim well before this function is reached, and
		// only direct-path access lands here.
		return "internal"
	}
}

// serviceFromAuthCredential extracts the SigV4 signing name from the
// Authorization header's Credential scope component.
//
// The signing name is not this package's service key — see serviceidentity.go.
// Callers that are classifying a request want serviceKeyFromAuthCredential;
// this one is for the two places that compare the scope against a specific
// service's own signing name, where the two happen to be equal and naming the
// signing name is what makes the comparison readable.
func serviceFromAuthCredential(r *http.Request) string {
	parts := credentialScope(r)
	if len(parts) >= 4 {
		return parts[3]
	}
	return ""
}

// serviceKeyFromAuthCredential reads the credential scope and translates it to
// an Overcast service key, which is what every consumer of detectService needs.
// Returning the raw signing name is what left MSK's v1 surface and
// AppRegistry's attribute groups unclassifiable, and with them unauthorized.
func serviceKeyFromAuthCredential(r *http.Request) string {
	return serviceKeyForSigningName(serviceFromAuthCredential(r))
}

// detectOperation infers the operation name from the request. It classifies
// the service first; callers that already have one should use
// detectOperationForService rather than paying for it twice.
func detectOperation(r *http.Request, body ...[]byte) string {
	return detectOperationForService(r, detectService(r, body...), body...)
}

// detectOperationForService infers the AWS operation name from a request that
// has already been classified as belonging to svc.
//
// Priority:
//  1. X-Amz-Target suffix  ("AmazonSQS.CreateQueue" → "CreateQueue")
//  2. x-id query param     ("?x-id=ListBuckets"     → "ListBuckets")
//  3. Query-protocol Action parameter (needs the body)
//  4. Smithy RPC v2 operation label in the URI
//  5. Method + path, resolved against svc
//
// Step 5 is where this used to go wrong. It ran one flat switch whose Lambda
// arm handled two methods under one path prefix and whose S3 arm was reachable
// by anything the arms above it failed to claim, so every other Lambda method
// and API version was labelled — and metered — as an S3 object operation.
// Resolution is now scoped to the classified service throughout: restOperation
// answers for a REST-routed service from the pinned Smithy models, the S3
// shape rules are reachable only when the request is S3's, and a path neither
// recognises yields "" instead of borrowing a name from whichever service's
// heuristics happened to sit lower in the switch.
func detectOperationForService(r *http.Request, svc string, body ...[]byte) string {
	// 1. Target-based (all JSON-protocol services).
	if t := r.Header.Get("X-Amz-Target"); t != "" {
		if claim, ok := awsapi.NewRegistry().ClaimTarget(t); ok && claim.Operation != "" {
			return claim.Operation
		}
	}

	// 2. x-id query param (S3 SDK sends this for several operations)
	if xid := rawQueryValue(r.URL.RawQuery, "x-id"); xid != "" {
		return xid
	}

	// 3. Query-protocol Action parameter.
	//
	// The scan covers the whole body, not a prefix of it. Form parameters
	// arrive in whatever order the client encoded them, so a request with a
	// large leading parameter pushes Action= past any fixed window: a
	// CreateStack whose TemplateBody was serialised first went unnamed, while
	// the same call with Action= first was recognised. bytes.Contains over a
	// body already bounded by the capture cap is cheap, and a false positive
	// — "Action=" appearing inside a value — costs one ParseQuery that
	// ClaimQuery then rejects.
	if len(body) > 0 && bytes.Contains(body[0], []byte("Action=")) {
		values, err := url.ParseQuery(string(body[0]))
		if err == nil {
			if claim, ok := awsapi.NewRegistry().ClaimQuery(values.Get("Version"), values.Get("Action")); ok {
				return claim.Operation
			}
		}
	}

	// 4. Smithy RPC v2 names the operation in the URI. Resolved through the
	// registry rather than taken from the path so the answer is a modeled
	// operation name and not whatever a caller put there.
	if claim, ok := smithyRPCClaim(r); ok && claim.Operation != "" {
		return claim.Operation
	}

	switch r.URL.Path {
	case "/_overcast/events":
		return "Subscribe"
	case "/_overcast/metrics":
		return "GetMetrics"
	}

	// Emulator-internal paths. No modeled AWS operation can describe one — S3
	// bucket names cannot begin with '_', so none of the model-derived steps
	// below can apply — but a few of them are named all the same, by the
	// hand-written table in restoperation.go. Ask it before giving up, because
	// this bail-out is what blanked the operation name on the emulator-only
	// Lambda endpoints the moment phase 6 moved them into the namespace.
	if strings.HasPrefix(r.URL.Path, InternalPrefix) {
		if op, ok := overcastRESTOperation(svc, r.Method, r.URL.Path); ok {
			return op
		}
		return ""
	}

	// Host-routed data-plane traffic (execute-api, lambda-url, appsync-api,
	// CloudFront, ELB). The path belongs to the customer's own API, not to an
	// AWS control-plane operation, so there is nothing to name — and without
	// this the request would be scored against whatever service the host claim
	// named it.
	if claim, ok := HostClaimFromContext(r.Context()); ok && claim.Kind == HostClaimHostRoute {
		return ""
	}

	// 5. Method + path, resolved against the classified service.
	if svc != "s3" {
		return restOperation(svc, r)
	}
	return s3ShapeOperation(r)
}

// s3ShapeOperation names an S3 request from its method, path depth and
// sub-resource query parameters. S3 alone needs shape rules rather than the
// generated model bindings: it has no distinguishing header or path prefix,
// and in the shared model trie its own `/{Bucket}/{Key+}` bindings sit behind
// other services' greedy bindings, so a lookup for "/my-bucket/key" answers
// with MediaStore Data's GetObject long before it reaches S3's.
//
// Reachable only for requests detectService classified as S3 — which is the
// same determination the router makes when it decides S3 keeps a path.
func s3ShapeOperation(r *http.Request) string {
	depth := pathDepth(r.URL.Path)
	query := r.URL.RawQuery

	switch {
	// Bucket-level
	case depth == 1 && r.Method == http.MethodGet && r.URL.Path == "/":
		return "ListBuckets"
	case depth == 1 && r.Method == http.MethodPut && rawQueryHas(query, "versioning"):
		return "PutBucketVersioning"
	case depth == 1 && r.Method == http.MethodGet && rawQueryHas(query, "location"):
		return "GetBucketLocation"
	case depth == 1 && r.Method == http.MethodGet && (rawQueryHas(query, "list-type") || rawQueryHas(query, "prefix")):
		return "ListObjectsV2"
	case depth == 1 && r.Method == http.MethodPut:
		return "CreateBucket"
	case depth == 1 && r.Method == http.MethodDelete:
		return "DeleteBucket"
	case depth == 1 && r.Method == http.MethodHead:
		return "HeadBucket"

	// Object-level
	case depth >= 2 && r.Method == http.MethodPut && r.Header.Get("X-Amz-Copy-Source") != "":
		return "CopyObject"
	case depth >= 2 && r.Method == http.MethodPut && rawQueryHas(query, "uploadId"):
		return "UploadPart"
	case depth >= 2 && r.Method == http.MethodPut:
		return "PutObject"
	case depth >= 2 && r.Method == http.MethodGet && rawQueryHas(query, "uploadId"):
		return "ListParts"
	case depth >= 2 && r.Method == http.MethodGet:
		return "GetObject"
	case depth >= 2 && r.Method == http.MethodHead:
		return "HeadObject"
	case depth >= 2 && r.Method == http.MethodDelete && rawQueryHas(query, "uploadId"):
		return "AbortMultipartUpload"
	case depth >= 2 && r.Method == http.MethodDelete:
		return "DeleteObject"
	case depth >= 2 && r.Method == http.MethodPost && rawQueryHas(query, "uploads"):
		return "CreateMultipartUpload"
	case depth >= 2 && r.Method == http.MethodPost && rawQueryHas(query, "delete"):
		return "DeleteObjects"
	}

	return ""
}

// responseWriter wraps http.ResponseWriter to capture the status code written
// by the handler. Go's standard ResponseWriter doesn't expose the status after
// the fact, so we intercept WriteHeader to record it.
//
// Flush is forwarded so that SSE handlers (/_overcast/events) can still call Flush
// through the middleware chain — without this the Flusher type assertion in
// eventsHandler would fail and the request would panic with a 500.
type responseWriter struct {
	http.ResponseWriter
	status          int
	awsErrorCode    string
	awsErrorMessage string
	awsErrorCause   error
	req             *http.Request
}

func (rw *responseWriter) WriteHeader(status int) {
	rw.status = status
	// Two of these wrappers nest in the chain (Logger's and RequestEvents'),
	// so this runs twice per response. CaptureStackOnce keeps the innermost
	// stack — the one closest to the handler — and makes the second call free.
	trace.RecorderFromContext(rw.req.Context()).CaptureStackOnce()
	rw.ResponseWriter.WriteHeader(status)
}

// RecordAWSError lets protocol writers attach AWS error details to the request
// log without exposing internal causes in client-facing response bodies.
//
// It forwards to a wrapped recorder as well as recording locally, because more
// than one of these can be in a single chain: RequestEvents is registered after
// Logger and builds its own responseWriter around Logger's, so a handler writes
// through the inner one while Logger reads the outer. Recording only locally
// left Logger's copy empty, and with it the trace's AWSErrorCode — the type
// assertion in protocol.recordAWSError succeeded every time, so nothing
// failed and nothing warned; the value just landed on the instance nobody
// read. Forwarding makes the depth of the chain stop mattering.
func (rw *responseWriter) RecordAWSError(aerr *protocol.AWSError) {
	if aerr == nil {
		return
	}
	rw.awsErrorCode = aerr.Code
	rw.awsErrorMessage = aerr.Message
	rw.awsErrorCause = protocol.Cause(aerr)
	if outer, ok := rw.ResponseWriter.(interface {
		RecordAWSError(*protocol.AWSError)
	}); ok {
		outer.RecordAWSError(aerr)
	}
}

// Flush forwards to the underlying ResponseWriter if it supports http.Flusher.
func (rw *responseWriter) Flush() {
	if f, ok := rw.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Unwrap lets middleware stacks (and http.ResponseController) reach the
// underlying ResponseWriter for interface detection (Hijacker, Pusher, etc.).
func (rw *responseWriter) Unwrap() http.ResponseWriter {
	return rw.ResponseWriter
}

// Hijack implements http.Hijacker by delegating to the underlying writer.
// Required for WebSocket upgrades (e.g. AppSync real-time subscriptions).
func (rw *responseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if hj, ok := rw.ResponseWriter.(http.Hijacker); ok {
		return hj.Hijack()
	}
	return nil, nil, http.ErrNotSupported
}

// isOperationalPollPath reports whether path belongs to an internal
// health/readiness probe or the /_overcast/debug/* namespace polled by container
// orchestrators (Docker HEALTHCHECK, Kubernetes probes) or the web UI's
// auto-refreshing debug views. These fire purely because time passed and
// infrastructure polled — never because of anything a real AWS client did —
// so per the trace-vs-debug policy (CONTRIBUTING.md § Log levels) they belong
// at TRACE: polling intervals of a few seconds would otherwise drown genuine
// request activity even at DEBUG.
func isOperationalPollPath(path string) bool {
	// The compatibility aliases are the same probe wearing an older URL,
	// polled on the same few-second interval and just as uninteresting.
	if path == LegacyHealthPath || path == LocalStackHealthPath {
		return true
	}
	return path == "/_overcast/health" || strings.HasPrefix(path, "/_overcast/debug/") || path == "/_overcast/debug"
}

// Logger logs every request with structured fields: real AWS API calls and
// other requests at INFO, internal health/readiness and /_overcast/debug/* polling at
// TRACE (see isOperationalPollPath). When stdout is a terminal, each line is
// prefixed with the service badge and (when known) an operation badge so log
// lines are easy to scan at a glance. Failed requests (5xx) are logged at
// ERROR level regardless of path.
func Logger(logger *zap.Logger, clk clock.Clock) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := clk.Now()
			// The request snapshot is only ever printed for a 5xx, so nothing
			// is read up front: DebugTrace's capture is reused when debug is
			// on — which also means the failure log gets that capture's larger
			// bound — and otherwise a lazy tee records only what the handler
			// itself reads. See bodycapture.go.
			capture := requestCaptureFromContext(r.Context())
			if capture == nil {
				capture = teeRequestBody(r, maxLoggedRequestBody)
			}
			rw := &responseWriter{ResponseWriter: w, status: http.StatusOK, req: r}

			next.ServeHTTP(rw, r)
			capture.seal()

			reqID := protocol.RequestIDFromContext(r.Context())
			duration := clk.Since(start)
			// Classified with the body the handler already read, not without
			// it. detectService takes it variadically, and omitting it here
			// silently disabled the step that classifies Query-protocol
			// requests by their Action — so every unsigned SQS, SNS or
			// CloudFormation call was logged under the s3 fallback, which is
			// what the field defaults to when nothing else claims the request.
			// captured() adds no read: see its doc comment.
			body := capture.captured()
			svc := detectService(r, body)
			op := detectOperationForService(r, svc, body)

			log := serviceutil.NewServiceLogger(logger, svc)
			if op != "" {
				log = log.WithOperation(op)
			}

			fields := []zap.Field{
				zap.String("request_id", reqID),
				zap.String("method", r.Method),
				zap.String("path", r.URL.Path),
				zap.String("query", r.URL.RawQuery),
				zap.Int("status", rw.status),
				zap.Duration("duration", duration),
				zap.String("remote_addr", r.RemoteAddr),
			}
			if t := r.Header.Get("X-Amz-Target"); t != "" {
				fields = append(fields, zap.String("target", t))
			}
			if rw.status >= 500 {
				requestBody := capture.body()
				fields = append(fields,
					zap.String("request_uri", r.RequestURI),
					zap.String("request_proto", r.Proto),
					zap.String("request_host", r.Host),
					zap.Int64("request_content_length", r.ContentLength),
					zap.Any("request_headers", capture.headers),
					zap.ByteString("request_body", requestBody),
				)
				if capture.truncated {
					fields = append(fields, zap.Bool("request_body_truncated", true))
				}
				if capture.err != nil {
					fields = append(fields, zap.Error(capture.err))
				}
			}
			if rw.awsErrorCode != "" {
				fields = append(fields,
					zap.String("aws_error_code", rw.awsErrorCode),
					zap.String("aws_error_message", rw.awsErrorMessage),
				)
				if rw.awsErrorCause != nil {
					fields = append(fields, zap.String("aws_error_cause", rw.awsErrorCause.Error()))
				}
			}

			if rec := trace.RecorderFromContext(r.Context()); rec != nil {
				rec.AddLog(trace.LogEntry{
					Level:     logLevel(rw.status),
					Message:   "request",
					Timestamp: start,
					Fields:    trace.ZapFieldsToMap(fields),
				})
				if rw.awsErrorCode != "" {
					rec.SetMeta(r.RemoteAddr, r.UserAgent(), r.Header.Get("Referer"), rw.awsErrorCode, rw.awsErrorMessage)
				}
			}

			switch {
			case rw.status >= 500:
				log.Error("request failed", fields...)
			case isOperationalPollPath(r.URL.Path):
				log.Trace("request", fields...)
			default:
				log.Info("request", fields...)
			}
		})
	}
}

func logLevel(status int) string {
	if status >= 500 {
		return "ERROR"
	}
	return "INFO"
}
