package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// An AWS service answers to three names, and this package used to hold only
// one of them. Every test in this file is a case where the other two matter.
//
//   - Overcast's service key ("msk") routes the request, labels the log line,
//     and selects the resource resolver in iam_enforce.go.
//   - The SigV4 signing name ("kafka") is what a real SDK writes into the
//     Authorization credential scope, and therefore all detectService has to
//     go on for any path its prefix switch does not claim.
//   - The IAM action prefix ("kafka:") is what a policy author writes, because
//     it is what the AWS documentation says.
//
// Conflating the first two makes a signed request unclassifiable, which fails
// open through IAMEnforce's unnamed-action branch. Conflating the first and
// third makes a correctly written policy silently not match.

// signedFormRequest builds a Query-protocol request: a form-encoded Action in
// the body plus a credential scope, which is how the AWS SDKs call CloudWatch,
// ELBv2 and the other Query services.
func signedFormRequest(signingName, body string) (*http.Request, []byte) {
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("Authorization",
		"AWS4-HMAC-SHA256 Credential=AKID/20260811/us-east-1/"+signingName+
			"/aws4_request, SignedHeaders=host;x-amz-date, Signature=deadbeef")
	return r, []byte(body)
}

// signedTargetRequest builds a JSON-protocol request: an X-Amz-Target header
// plus a credential scope.
func signedTargetRequest(signingName, target string) *http.Request {
	r := signedRequest(http.MethodPost, "/", signingName)
	r.Header.Set("X-Amz-Target", target)
	return r
}

// TestDetectServiceMapsSigningNameToServiceKey covers the classification half.
//
// detectService's step 3b reads the credential scope, which carries the SigV4
// signing name, and returned it unchanged. For most services that is harmless
// because the two names are equal, but where they differ the answer was a name
// nothing downstream knows: the generated registry is keyed by Overcast's key,
// so restOperation could name no operation, and requestIAMResource's switch
// matched no arm.
//
// The "was" column records the behaviour on the commit this fixes.
func TestDetectServiceMapsSigningNameToServiceKey(t *testing.T) {
	tests := []struct {
		name    string
		signing string
		method  string
		path    string
		was     string
		want    string
	}{
		// MSK. Its v2 cluster API is claimed by the prefix switch at "/api", so
		// it was already right; its whole v1 surface is not, and every one of
		// these is a path a real SDK calls.
		{"msk list clusters", "kafka", http.MethodGet, "/v1/clusters", "kafka", "msk"},
		{"msk create cluster", "kafka", http.MethodPost, "/v1/clusters", "kafka", "msk"},
		{"msk list configurations", "kafka", http.MethodGet, "/v1/configurations", "kafka", "msk"},
		{"msk list kafka versions", "kafka", http.MethodGet, "/v1/kafka-versions", "kafka", "msk"},
		{"msk v2 clusters", "kafka", http.MethodGet, "/api/v2/clusters", "msk", "msk"},

		// AppRegistry. "/applications" is claimed by the prefix switch, its
		// sibling subtree is not — the same split as MSK's two surfaces.
		{"appregistry applications", "servicecatalog", http.MethodGet, "/applications", "appregistry", "appregistry"},
		{"appregistry attribute groups", "servicecatalog", http.MethodGet, "/attribute-groups", "servicecatalog", "appregistry"},

		// Query-protocol services, addressed with no body here, so step 3's
		// Action/Version lookup has nothing to read and the credential scope
		// decides them at step 3b. A Query request that *does* carry a body is
		// decided by its API version — see
		// TestSharedSigningNameIsSeparatedByTheQueryAPIVersion.
		{"cloudwatch", "monitoring", http.MethodPost, "/", "monitoring", "cloudwatch"},
		{"elbv2", "elasticloadbalancing", http.MethodPost, "/", "elasticloadbalancing", "elbv2"},

		// JSON-protocol services. X-Amz-Target claims these at step 1, so the
		// scope is reached only on a path outside the target protocol — a
		// latent case rather than a live one, asserted so the mapping stays
		// total rather than covering only what happens to be reachable today.
		{"stepfunctions", "states", http.MethodPost, "/", "states", "stepfunctions"},
		{"cognito", "cognito-idp", http.MethodPost, "/", "cognito-idp", "cognito"},
		{"waf", "wafv2", http.MethodPost, "/", "wafv2", "waf"},

		// EFS and OpenSearch bind their whole surface under a dated version
		// prefix the switch claims, so the scope never decided them. Asserted
		// because "the prefix switch happens to cover it" is a property of
		// today's models, not of this code.
		{"efs", "elasticfilesystem", http.MethodPost, "/", "elasticfilesystem", "efs"},
		{"opensearch", "es", http.MethodPost, "/", "es", "opensearch"},

		// Services whose signing name already is the key. Controls: the
		// mapping must not disturb them.
		{"eks", "eks", http.MethodGet, "/clusters", "eks", "eks"},
		{"lambda", "lambda", http.MethodGet, "/2015-03-31/functions", "lambda", "lambda"},
		{"logs", "logs", http.MethodPost, "/", "logs", "logs"},
		{"sqs", "sqs", http.MethodPost, "/", "sqs", "sqs"},

		// Two signing names are shared by an implemented pair, and the scope
		// alone cannot separate them. AppConfig Data signs as the AppConfig
		// control plane and DynamoDB Streams as DynamoDB; both are told apart
		// earlier — by path and by X-Amz-Target respectively — so the scope
		// must keep answering with the control-plane service rather than
		// guessing the narrower one.
		{"appconfig control plane", "appconfig", http.MethodPost, "/", "appconfig", "appconfig"},
		{"dynamodb", "dynamodb", http.MethodPost, "/", "dynamodb", "dynamodb"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Given: a request signed the way the AWS SDK for that service signs it.
			r := signedRequest(tt.method, tt.path, tt.signing)

			// When: it is classified.
			got := detectService(r)

			// Then: the answer is Overcast's service key, not the signing name.
			if got != tt.want {
				t.Errorf("detectService(%s %s, scope %q) = %q, want %q (was %q)",
					tt.method, tt.path, tt.signing, got, tt.want, tt.was)
			}
		})
	}
}

// TestSignedRESTRequestInfersAnIAMAction is the consequence of the mapping
// above, and the reason it is not cosmetic. IAMEnforce passes a signed request
// through untested when requestIAMAction returns "" — deliberately, because
// S3's sub-resource operations are not nameable. A service misclassified by its
// own signing name lands in that branch for its entire REST surface, so
// enforcement was off for every path below even with OVERCAST_ENFORCE_IAM=true.
func TestSignedRESTRequestInfersAnIAMAction(t *testing.T) {
	tests := []struct {
		signing string
		method  string
		path    string
		want    string
	}{
		{"kafka", http.MethodGet, "/v1/clusters", "kafka:ListClusters"},
		{"kafka", http.MethodPost, "/v1/clusters", "kafka:CreateCluster"},
		{"kafka", http.MethodGet, "/v1/configurations", "kafka:ListConfigurations"},
		{"kafka", http.MethodGet, "/v1/kafka-versions", "kafka:ListKafkaVersions"},
		{"servicecatalog", http.MethodGet, "/attribute-groups", "servicecatalog:ListAttributeGroups"},
		{"servicecatalog", http.MethodPost, "/attribute-groups", "servicecatalog:CreateAttributeGroup"},
	}

	for _, tt := range tests {
		t.Run(tt.method+" "+tt.path, func(t *testing.T) {
			// Given: a signed request to a modeled REST binding.
			r := signedRequest(tt.method, tt.path, tt.signing)

			// When/Then: an action is inferred, so enforcement has something
			// to evaluate rather than falling through the open branch.
			if got := classifyIAM(r).action; got != tt.want {
				t.Errorf("requestIAMAction(%s %s, scope %q) = %q, want %q",
					tt.method, tt.path, tt.signing, got, tt.want)
			}
		})
	}
}

// TestRequestIAMActionUsesTheAWSActionPrefix covers the authorization half.
//
// The action is built as "<service>:<operation>", and the service was
// Overcast's own key. Where AWS's IAM prefix differs, a policy written from the
// AWS documentation — the only place a user can read the right name — did not
// match the action Overcast evaluated, in either direction: an Allow did not
// grant, and a Deny did not deny.
//
// The prefix is not simply the signing name. CloudWatch signs as "monitoring"
// and authorizes as "cloudwatch:", which is why this is a second mapping and
// not the inverse of the first.
func TestRequestIAMActionUsesTheAWSActionPrefix(t *testing.T) {
	restTests := []struct {
		signing string
		method  string
		path    string
		was     string
		want    string
	}{
		{"kafka", http.MethodGet, "/api/v2/clusters", "msk:ListClustersV2", "kafka:ListClustersV2"},
		{"kafka", http.MethodPost, "/api/v2/clusters", "msk:CreateClusterV2", "kafka:CreateClusterV2"},
		{"servicecatalog", http.MethodGet, "/applications", "appregistry:ListApplications", "servicecatalog:ListApplications"},
		{"elasticfilesystem", http.MethodGet, "/2015-02-01/file-systems", "efs:DescribeFileSystems", "elasticfilesystem:DescribeFileSystems"},
		{"es", http.MethodGet, "/2021-01-01/domain", "opensearch:ListDomainNames", "es:ListDomainNames"},

		// AppConfig Data authorizes under AppConfig's own prefix — AWS has no
		// separate "appconfigdata:" IAM namespace, even though it is a
		// distinct signing name and a distinct Overcast service key.
		{"appconfig", http.MethodPost, "/configurationsessions", "appconfigdata:StartConfigurationSession", "appconfig:StartConfigurationSession"},

		// Controls: services whose key already is their AWS prefix.
		{"lambda", http.MethodGet, "/2015-03-31/functions", "lambda:ListFunctions", "lambda:ListFunctions"},
		{"apigateway", http.MethodGet, "/restapis", "apigateway:GetRestApis", "apigateway:GetRestApis"},
	}
	for _, tt := range restTests {
		t.Run(tt.method+" "+tt.path, func(t *testing.T) {
			r := signedRequest(tt.method, tt.path, tt.signing)
			if got := classifyIAM(r).action; got != tt.want {
				t.Errorf("requestIAMAction(%s %s) = %q, want %q (was %q)",
					tt.method, tt.path, got, tt.want, tt.was)
			}
		})
	}

	targetTests := []struct {
		signing string
		target  string
		was     string
		want    string
	}{
		{"states", "AWSStepFunctions.StartExecution", "stepfunctions:StartExecution", "states:StartExecution"},
		{"cognito-idp", "AWSCognitoIdentityProviderService.ListUserPools", "cognito:ListUserPools", "cognito-idp:ListUserPools"},
		{"wafv2", "AWSWAF_20190729.ListWebACLs", "waf:ListWebACLs", "wafv2:ListWebACLs"},

		// DynamoDB Streams authorizes under DynamoDB's own prefix — AWS has
		// no separate "dynamodbstreams:" IAM namespace for GetRecords,
		// DescribeStream, GetShardIterator or ListStreams, even though it is
		// a distinct Overcast service key with its own target prefix.
		{"dynamodb", "DynamoDBStreams_20120810.DescribeStream", "dynamodbstreams:DescribeStream", "dynamodb:DescribeStream"},

		// Controls.
		{"logs", "Logs_20140328.PutLogEvents", "logs:PutLogEvents", "logs:PutLogEvents"},
		{"dynamodb", "DynamoDB_20120810.ListTables", "dynamodb:ListTables", "dynamodb:ListTables"},
	}
	for _, tt := range targetTests {
		t.Run(tt.target, func(t *testing.T) {
			r := signedTargetRequest(tt.signing, tt.target)
			if got := classifyIAM(r).action; got != tt.want {
				t.Errorf("requestIAMAction(%s) = %q, want %q (was %q)", tt.target, got, tt.want, tt.was)
			}
		})
	}

	queryTests := []struct {
		signing string
		body    string
		was     string
		want    string
	}{
		// CloudWatch is the case that stops the two mappings being one: it
		// signs as "monitoring" but authorizes as "cloudwatch:".
		{"monitoring", "Action=PutMetricAlarm&Version=2010-08-01", "monitoring:PutMetricAlarm", "cloudwatch:PutMetricAlarm"},
		{"elasticloadbalancing", "Action=DescribeLoadBalancers&Version=2015-12-01",
			"elasticloadbalancing:DescribeLoadBalancers", "elasticloadbalancing:DescribeLoadBalancers"},

		// Control.
		{"sns", "Action=ListTopics&Version=2010-03-31", "sns:ListTopics", "sns:ListTopics"},
	}
	for _, tt := range queryTests {
		t.Run(tt.body, func(t *testing.T) {
			r, _ := signedFormRequest(tt.signing, tt.body)
			if got := classifyIAM(r).action; got != tt.want {
				t.Errorf("requestIAMAction(%s, scope %q) = %q, want %q (was %q)",
					tt.body, tt.signing, got, tt.want, tt.was)
			}
		})
	}
}

// TestIAMActionPrefixAgreesWithTheSigningNameMapping keeps the two mappings
// from drifting into disagreement. They are not inverses — CloudWatch is the
// standing counter-example — but where a service key has both a distinct
// signing name and a distinct action prefix, AWS gives it the same string for
// both, and a future entry that does not is far more likely to be a typo than
// a real AWS quirk. This is the assertion that makes it say so.
func TestIAMActionPrefixAgreesWithTheSigningNameMapping(t *testing.T) {
	// The keys AWS genuinely signs and authorizes under different names.
	// Listing them here rather than deriving them is the point: a new entry in
	// either mapping has to be added deliberately.
	exceptions := map[string]string{
		"cloudwatch": "monitoring",
	}

	for _, signingName := range []string{
		"kafka", "servicecatalog", "elasticfilesystem", "es",
		"elasticloadbalancing", "states", "cognito-idp", "wafv2",
	} {
		key := serviceKeyForSigningName(signingName)
		if key == signingName {
			t.Errorf("serviceKeyForSigningName(%q) = %q; expected a distinct Overcast key", signingName, key)
			continue
		}
		if want, isException := exceptions[key]; isException {
			t.Errorf("%q is recorded as an exception signing as %q; remove it from one list or the other", key, want)
			continue
		}
		if got := iamActionPrefix(key); got != signingName {
			t.Errorf("iamActionPrefix(%q) = %q, want %q — the signing name it maps back from",
				key, got, signingName)
		}
	}

	for key, signingName := range exceptions {
		if got := serviceKeyForSigningName(signingName); got != key {
			t.Errorf("serviceKeyForSigningName(%q) = %q, want %q", signingName, got, key)
		}
		if got := iamActionPrefix(key); got != key {
			t.Errorf("iamActionPrefix(%q) = %q, want %q — the exception is that it is *not* the signing name",
				key, got, key)
		}
	}
}
