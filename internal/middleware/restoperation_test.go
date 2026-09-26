package middleware

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/overcast-sh/overcast/internal/awsapi"
)

// signedRequest builds a request carrying a SigV4 Authorization header scoped
// to signingName, which is how a real SDK identifies the service it is calling
// on a path that carries no service-specific prefix.
func signedRequest(method, target, signingName string) *http.Request {
	r := httptest.NewRequest(method, target, nil)
	r.Header.Set("Authorization",
		"AWS4-HMAC-SHA256 Credential=AKID/20260809/us-east-1/"+signingName+
			"/aws4_request, SignedHeaders=host;x-amz-date, Signature=deadbeef")
	return r
}

// TestDetectOperationLambdaRESTSurface pins the label for the Lambda REST
// surface. Before the shared model-backed mapping, every method and API
// version outside the two cases the old switch handled fell through to the S3
// object shapes below it, so Lambda writes were labelled — and metered —
// as PutObject/DeleteObject against S3.
//
// The "was" column records the behaviour on the commit this fixes.
func TestDetectOperationLambdaRESTSurface(t *testing.T) {
	tests := []struct {
		method string
		path   string
		was    string
		want   string
	}{
		// The regression rows, as measured before the fix.
		{http.MethodPost, "/2015-03-31/functions", "InvokeFunction", "CreateFunction"},
		{http.MethodPost, "/2015-03-31/functions/fn/invocations", "InvokeFunction", "Invoke"},
		{http.MethodPost, "/2015-03-31/functions/fn/versions", "InvokeFunction", "PublishVersion"},
		{http.MethodPost, "/2015-03-31/functions/fn/aliases", "InvokeFunction", "CreateAlias"},
		{http.MethodPost, "/2015-03-31/functions/fn/policy", "InvokeFunction", "AddPermission"},
		{http.MethodGet, "/2015-03-31/functions/fn/configuration", "GetFunction", "GetFunctionConfiguration"},
		{http.MethodGet, "/2015-03-31/functions/fn/aliases", "GetFunction", "ListAliases"},
		{http.MethodPut, "/2015-03-31/functions/fn/configuration", "PutObject", "UpdateFunctionConfiguration"},
		{http.MethodPut, "/2015-03-31/functions/fn/code", "PutObject", "UpdateFunctionCode"},
		{http.MethodDelete, "/2015-03-31/functions/fn", "DeleteObject", "DeleteFunction"},
		{http.MethodGet, "/2015-03-31/event-source-mappings/uuid", "GetObject", "GetEventSourceMapping"},
		{http.MethodPut, "/2015-03-31/event-source-mappings/uuid", "PutObject", "UpdateEventSourceMapping"},
		{http.MethodDelete, "/2015-03-31/event-source-mappings/uuid", "DeleteObject", "DeleteEventSourceMapping"},
		{http.MethodPut, "/2019-09-25/functions/fn/event-invoke-config", "PutObject", "PutFunctionEventInvokeConfig"},
		{http.MethodGet, "/2016-08-19/account-settings/", "GetObject", "GetAccountSettings"},
		{http.MethodPost, "/2018-10-31/layers/lay/versions", "", "PublishLayerVersion"},

		// Three of the measured rows named an API version AWS does not serve
		// that operation on, so the models describe no such operation and the
		// honest answer is no label at all. The version AWS (and Overcast)
		// really uses is asserted alongside each.
		{http.MethodPut, "/2015-03-31/functions/fn/concurrency", "PutObject", ""},
		{http.MethodPut, "/2017-10-31/functions/fn/concurrency", "PutObject", "PutFunctionConcurrency"},
		{http.MethodPut, "/2020-06-30/functions/fn/url", "PutObject", ""},
		{http.MethodPut, "/2021-10-31/functions/fn/url", "PutObject", "UpdateFunctionUrlConfig"},
		{http.MethodPut, "/2017-03-31/tags/arn%3Aaws%3Alambda%3Aus-east-1%3A000000000000%3Afunction%3Afn", "PutObject", ""},
		{http.MethodPost, "/2017-03-31/tags/arn%3Aaws%3Alambda%3Aus-east-1%3A000000000000%3Afunction%3Afn", "InvokeFunction", "TagResource"},

		// The rest of the routes internal/services/lambda/service.go registers.
		{http.MethodGet, "/2015-03-31/functions", "ListFunctions", "ListFunctions"},
		{http.MethodGet, "/2015-03-31/functions/fn", "GetFunction", "GetFunction"},
		{http.MethodPost, "/2015-03-31/event-source-mappings", "InvokeFunction", "CreateEventSourceMapping"},
		{http.MethodGet, "/2015-03-31/event-source-mappings", "ListEventSourceMappings", "ListEventSourceMappings"},
		{http.MethodGet, "/2015-03-31/functions/fn/policy", "GetFunction", "GetPolicy"},
		{http.MethodDelete, "/2015-03-31/functions/fn/policy/stmt-1", "DeleteObject", "RemovePermission"},
		{http.MethodGet, "/2015-03-31/functions/fn/versions", "GetFunction", "ListVersionsByFunction"},
		{http.MethodGet, "/2015-03-31/functions/fn/aliases/live", "GetFunction", "GetAlias"},
		{http.MethodPut, "/2015-03-31/functions/fn/aliases/live", "PutObject", "UpdateAlias"},
		{http.MethodDelete, "/2015-03-31/functions/fn/aliases/live", "DeleteObject", "DeleteAlias"},
		{http.MethodDelete, "/2017-10-31/functions/fn/concurrency", "DeleteObject", "DeleteFunctionConcurrency"},
		{http.MethodGet, "/2019-09-30/functions/fn/concurrency", "GetFunction", "GetFunctionConcurrency"},
		{http.MethodPut, "/2019-09-30/functions/fn/provisioned-concurrency", "PutObject", "PutProvisionedConcurrencyConfig"},
		{http.MethodGet, "/2019-09-30/functions/fn/provisioned-concurrency", "GetFunction", "GetProvisionedConcurrencyConfig"},
		{http.MethodDelete, "/2019-09-30/functions/fn/provisioned-concurrency", "DeleteObject", "DeleteProvisionedConcurrencyConfig"},
		{http.MethodGet, "/2020-06-30/functions/fn/code-signing-config", "GetObject", "GetFunctionCodeSigningConfig"},
		{http.MethodPut, "/2020-06-30/functions/fn/code-signing-config", "PutObject", "PutFunctionCodeSigningConfig"},
		{http.MethodDelete, "/2020-06-30/functions/fn/code-signing-config", "DeleteObject", "DeleteFunctionCodeSigningConfig"},
		{http.MethodPost, "/2020-04-22/code-signing-configs", "", "CreateCodeSigningConfig"},
		{http.MethodGet, "/2020-04-22/code-signing-configs", "GetObject", "ListCodeSigningConfigs"},
		{http.MethodGet, "/2020-04-22/code-signing-configs/csc-1", "GetObject", "GetCodeSigningConfig"},
		{http.MethodPut, "/2020-04-22/code-signing-configs/csc-1", "PutObject", "UpdateCodeSigningConfig"},
		{http.MethodDelete, "/2020-04-22/code-signing-configs/csc-1", "DeleteObject", "DeleteCodeSigningConfig"},
		{http.MethodGet, "/2020-04-22/code-signing-configs/csc-1/functions", "GetObject", "ListFunctionsByCodeSigningConfig"},
		{http.MethodPost, "/2021-11-15/functions/fn/response-streaming-invocations", "", "InvokeWithResponseStream"},
		{http.MethodGet, "/2018-10-31/layers", "GetObject", "ListLayers"},
		{http.MethodGet, "/2018-10-31/layers/lay/versions", "GetObject", "ListLayerVersions"},
		{http.MethodGet, "/2018-10-31/layers/lay/versions/3", "GetObject", "GetLayerVersion"},
		{http.MethodDelete, "/2018-10-31/layers/lay/versions/3", "DeleteObject", "DeleteLayerVersion"},
		{http.MethodPost, "/2021-10-31/functions/fn/url", "", "CreateFunctionUrlConfig"},
		{http.MethodGet, "/2021-10-31/functions/fn/url", "GetObject", "GetFunctionUrlConfig"},
		{http.MethodDelete, "/2021-10-31/functions/fn/url", "DeleteObject", "DeleteFunctionUrlConfig"},
		{http.MethodGet, "/2021-10-31/functions/fn/urls", "GetObject", "ListFunctionUrlConfigs"},
		{http.MethodGet, "/2017-03-31/tags/arn%3Aaws%3Alambda%3A%3A%3Afunction%3Afn", "GetObject", "ListTags"},
		{http.MethodDelete, "/2017-03-31/tags/arn%3Aaws%3Alambda%3A%3A%3Afunction%3Afn", "DeleteObject", "UntagResource"},

		// Emulator-only routes: no AWS model describes them, so they come from
		// the one hand-written table in restoperation.go.
		{http.MethodGet, "/_overcast/lambda/functions/fn/source", "GetFunction", "GetFunctionSource"},
		{http.MethodPut, "/_overcast/lambda/functions/fn/source", "PutObject", "PutFunctionSource"},
		{http.MethodGet, "/_overcast/lambda/functions/fn/test-events", "GetFunction", "ListTestEvents"},
		{http.MethodPut, "/_overcast/lambda/functions/fn/test-events/ev", "PutObject", "PutTestEvent"},
		{http.MethodDelete, "/_overcast/lambda/functions/fn/test-events/ev", "DeleteObject", "DeleteTestEvent"},
		{http.MethodPost, "/_overcast/lambda/functions/fn/invoke-with-progress", "InvokeFunction", "Invoke"},
	}

	for _, tt := range tests {
		t.Run(tt.method+" "+tt.path, func(t *testing.T) {
			r := httptest.NewRequest(tt.method, tt.path, nil)
			if got := detectService(r); got != "lambda" {
				t.Fatalf("detectService(%s) = %q, want lambda", tt.path, got)
			}
			if got := detectOperation(r); got != tt.want {
				t.Errorf("detectOperation(%s %s) = %q, want %q (was %q before the fix)",
					tt.method, tt.path, got, tt.want, tt.was)
			}
		})
	}
}

// TestRequestIAMActionLambdaRESTSurface pins the IAM action for every Lambda
// route. The action fed to the policy evaluator used to fall through to the
// same broken S3 heuristics whenever the Lambda mapper had no answer, which
// authorised AddPermission as lambda:InvokeFunction (an invoke-only principal
// could grant invoke rights to others) and denied RemovePermission by asking
// about lambda:DeleteObject.
func TestRequestIAMActionLambdaRESTSurface(t *testing.T) {
	tests := []struct {
		method string
		path   string
		want   string
	}{
		{http.MethodPost, "/2015-03-31/functions", "lambda:CreateFunction"},
		{http.MethodGet, "/2015-03-31/functions", "lambda:ListFunctions"},
		{http.MethodGet, "/2015-03-31/functions/fn", "lambda:GetFunction"},
		{http.MethodDelete, "/2015-03-31/functions/fn", "lambda:DeleteFunction"},
		{http.MethodPut, "/2015-03-31/functions/fn/code", "lambda:UpdateFunctionCode"},
		{http.MethodGet, "/2015-03-31/functions/fn/configuration", "lambda:GetFunctionConfiguration"},
		{http.MethodPut, "/2015-03-31/functions/fn/configuration", "lambda:UpdateFunctionConfiguration"},

		// The privilege-escalation row: never lambda:InvokeFunction.
		{http.MethodPost, "/2015-03-31/functions/fn/policy", "lambda:AddPermission"},
		{http.MethodGet, "/2015-03-31/functions/fn/policy", "lambda:GetPolicy"},
		{http.MethodDelete, "/2015-03-31/functions/fn/policy/stmt-1", "lambda:RemovePermission"},

		// All three invoke operations authorise as lambda:InvokeFunction; AWS
		// has no lambda:Invoke action.
		{http.MethodPost, "/2015-03-31/functions/fn/invocations", "lambda:InvokeFunction"},
		{http.MethodPost, "/2021-11-15/functions/fn/response-streaming-invocations", "lambda:InvokeFunction"},
		{http.MethodPost, "/_overcast/lambda/functions/fn/invoke-with-progress", "lambda:InvokeFunction"},

		{http.MethodPost, "/2015-03-31/functions/fn/versions", "lambda:PublishVersion"},
		{http.MethodGet, "/2015-03-31/functions/fn/versions", "lambda:ListVersionsByFunction"},
		{http.MethodPost, "/2015-03-31/functions/fn/aliases", "lambda:CreateAlias"},
		{http.MethodGet, "/2015-03-31/functions/fn/aliases", "lambda:ListAliases"},
		{http.MethodGet, "/2015-03-31/functions/fn/aliases/live", "lambda:GetAlias"},
		{http.MethodPut, "/2015-03-31/functions/fn/aliases/live", "lambda:UpdateAlias"},
		{http.MethodDelete, "/2015-03-31/functions/fn/aliases/live", "lambda:DeleteAlias"},
		{http.MethodPost, "/2015-03-31/event-source-mappings", "lambda:CreateEventSourceMapping"},
		{http.MethodGet, "/2015-03-31/event-source-mappings", "lambda:ListEventSourceMappings"},
		{http.MethodGet, "/2015-03-31/event-source-mappings/uuid", "lambda:GetEventSourceMapping"},
		{http.MethodPut, "/2015-03-31/event-source-mappings/uuid", "lambda:UpdateEventSourceMapping"},
		{http.MethodDelete, "/2015-03-31/event-source-mappings/uuid", "lambda:DeleteEventSourceMapping"},
		{http.MethodPut, "/2017-10-31/functions/fn/concurrency", "lambda:PutFunctionConcurrency"},
		{http.MethodDelete, "/2017-10-31/functions/fn/concurrency", "lambda:DeleteFunctionConcurrency"},
		{http.MethodGet, "/2019-09-30/functions/fn/concurrency", "lambda:GetFunctionConcurrency"},
		{http.MethodPut, "/2019-09-30/functions/fn/provisioned-concurrency", "lambda:PutProvisionedConcurrencyConfig"},
		{http.MethodGet, "/2019-09-30/functions/fn/provisioned-concurrency", "lambda:GetProvisionedConcurrencyConfig"},
		{http.MethodDelete, "/2019-09-30/functions/fn/provisioned-concurrency", "lambda:DeleteProvisionedConcurrencyConfig"},
		{http.MethodGet, "/2020-06-30/functions/fn/code-signing-config", "lambda:GetFunctionCodeSigningConfig"},
		{http.MethodPut, "/2020-06-30/functions/fn/code-signing-config", "lambda:PutFunctionCodeSigningConfig"},
		{http.MethodDelete, "/2020-06-30/functions/fn/code-signing-config", "lambda:DeleteFunctionCodeSigningConfig"},
		{http.MethodPost, "/2020-04-22/code-signing-configs", "lambda:CreateCodeSigningConfig"},
		{http.MethodGet, "/2020-04-22/code-signing-configs", "lambda:ListCodeSigningConfigs"},
		{http.MethodGet, "/2020-04-22/code-signing-configs/csc-1", "lambda:GetCodeSigningConfig"},
		{http.MethodPut, "/2020-04-22/code-signing-configs/csc-1", "lambda:UpdateCodeSigningConfig"},
		{http.MethodDelete, "/2020-04-22/code-signing-configs/csc-1", "lambda:DeleteCodeSigningConfig"},
		{http.MethodGet, "/2020-04-22/code-signing-configs/csc-1/functions", "lambda:ListFunctionsByCodeSigningConfig"},
		{http.MethodGet, "/2018-10-31/layers", "lambda:ListLayers"},
		{http.MethodPost, "/2018-10-31/layers/lay/versions", "lambda:PublishLayerVersion"},
		{http.MethodGet, "/2018-10-31/layers/lay/versions", "lambda:ListLayerVersions"},
		{http.MethodGet, "/2018-10-31/layers/lay/versions/3", "lambda:GetLayerVersion"},
		{http.MethodDelete, "/2018-10-31/layers/lay/versions/3", "lambda:DeleteLayerVersion"},
		{http.MethodPost, "/2021-10-31/functions/fn/url", "lambda:CreateFunctionUrlConfig"},
		{http.MethodGet, "/2021-10-31/functions/fn/url", "lambda:GetFunctionUrlConfig"},
		{http.MethodPut, "/2021-10-31/functions/fn/url", "lambda:UpdateFunctionUrlConfig"},
		{http.MethodDelete, "/2021-10-31/functions/fn/url", "lambda:DeleteFunctionUrlConfig"},
		{http.MethodGet, "/2021-10-31/functions/fn/urls", "lambda:ListFunctionUrlConfigs"},
		{http.MethodPost, "/2017-03-31/tags/arn%3Aaws%3Alambda%3A%3A%3Afunction%3Afn", "lambda:TagResource"},
		{http.MethodGet, "/2017-03-31/tags/arn%3Aaws%3Alambda%3A%3A%3Afunction%3Afn", "lambda:ListTags"},
		{http.MethodDelete, "/2017-03-31/tags/arn%3Aaws%3Alambda%3A%3A%3Afunction%3Afn", "lambda:UntagResource"},
		{http.MethodGet, "/_overcast/lambda/functions/fn/source", "lambda:GetFunctionSource"},
		{http.MethodPut, "/_overcast/lambda/functions/fn/source", "lambda:PutFunctionSource"},
		{http.MethodGet, "/_overcast/lambda/functions/fn/test-events", "lambda:ListTestEvents"},
		{http.MethodPut, "/_overcast/lambda/functions/fn/test-events/ev", "lambda:PutTestEvent"},
		{http.MethodDelete, "/_overcast/lambda/functions/fn/test-events/ev", "lambda:DeleteTestEvent"},
	}

	for _, tt := range tests {
		t.Run(tt.method+" "+tt.path, func(t *testing.T) {
			r := signedRequest(tt.method, tt.path, "lambda")
			if got := classifyIAM(r).action; got != tt.want {
				t.Errorf("requestIAMAction(%s %s) = %q, want %q", tt.method, tt.path, got, tt.want)
			}
		})
	}
}

// TestRequestIAMActionNeverBorrowsAnotherServicesOperation is the fence the
// privilege-escalation bug needed. No Lambda path may authorise as an S3
// operation, and no path may authorise as an invoke unless it really is one.
func TestRequestIAMActionNeverBorrowsAnotherServicesOperation(t *testing.T) {
	s3Operations := map[string]bool{
		"GetObject": true, "PutObject": true, "DeleteObject": true, "HeadObject": true,
		"CopyObject": true, "ListBuckets": true, "CreateBucket": true, "DeleteBucket": true,
		"HeadBucket": true, "ListObjectsV2": true,
	}
	invokePaths := map[string]bool{
		"/2015-03-31/functions/fn/invocations":                    true,
		"/_overcast/lambda/functions/fn/invoke-with-progress":     true,
		"/2021-11-15/functions/fn/response-streaming-invocations": true,
	}

	for _, method := range []string{
		http.MethodGet, http.MethodPut, http.MethodPost, http.MethodDelete, http.MethodHead, http.MethodPatch,
	} {
		for _, path := range lambdaRegisteredPaths() {
			r := signedRequest(method, path, "lambda")
			action := classifyIAM(r).action
			operation := strings.TrimPrefix(action, "lambda:")
			if s3Operations[operation] {
				t.Errorf("%s %s authorises as %q — an S3 operation", method, path, action)
			}
			if operation == "InvokeFunction" && !invokePaths[path] {
				t.Errorf("%s %s authorises as %q but is not an invoke", method, path, action)
			}
		}
	}
}

// lambdaRegisteredPaths lists concrete forms of the routes
// internal/services/lambda/service.go registers.
func lambdaRegisteredPaths() []string {
	return []string{
		"/2015-03-31/functions",
		"/2015-03-31/functions/fn",
		"/2015-03-31/functions/fn/code",
		"/2015-03-31/functions/fn/configuration",
		"/2015-03-31/functions/fn/policy",
		"/2015-03-31/functions/fn/policy/stmt-1",
		"/2015-03-31/functions/fn/invocations",
		"/_overcast/lambda/functions/fn/invoke-with-progress",
		"/2015-03-31/functions/fn/versions",
		"/2015-03-31/functions/fn/aliases",
		"/2015-03-31/functions/fn/aliases/live",
		"/_overcast/lambda/functions/fn/source",
		"/_overcast/lambda/functions/fn/test-events",
		"/_overcast/lambda/functions/fn/test-events/ev",
		"/2015-03-31/event-source-mappings",
		"/2015-03-31/event-source-mappings/uuid",
		"/2017-03-31/tags/arn%3Aaws%3Alambda%3A%3A%3Afunction%3Afn",
		"/2017-10-31/functions/fn/concurrency",
		"/2018-10-31/layers",
		"/2018-10-31/layers/lay/versions",
		"/2018-10-31/layers/lay/versions/3",
		"/2019-09-30/functions/fn/concurrency",
		"/2019-09-30/functions/fn/provisioned-concurrency",
		"/2020-04-22/code-signing-configs",
		"/2020-04-22/code-signing-configs/csc-1",
		"/2020-04-22/code-signing-configs/csc-1/functions",
		"/2020-06-30/functions/fn/code-signing-config",
		"/2021-10-31/functions/fn/url",
		"/2021-10-31/functions/fn/urls",
		"/2021-11-15/functions/fn/response-streaming-invocations",
	}
}

// TestDetectOperationLoggerAndIAMAgree pins the two consumers of the shared
// mapping against each other. They must name the same operation everywhere
// except the invoke family, where AWS's IAM action name deliberately differs
// from the API operation name.
func TestDetectOperationLoggerAndIAMAgree(t *testing.T) {
	for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodPost, http.MethodDelete} {
		for _, path := range lambdaRegisteredPaths() {
			r := signedRequest(method, path, "lambda")
			label := detectOperation(r)
			action := classifyIAM(r).action
			if label == "" {
				if action != "" {
					t.Errorf("%s %s: no log label but IAM action %q", method, path, action)
				}
				continue
			}
			want := "lambda:" + lambdaIAMAction(label)
			if action != want {
				t.Errorf("%s %s: label %q implies action %q, got %q", method, path, label, want, action)
			}
		}
	}
}

// TestDetectOperationOtherRESTServices covers the rest of the REST-routed
// surface, which the old switch left unlabelled (AppSync, CloudFront, API
// Gateway and EFS were explicitly given up on) or mislabelled as S3.
func TestDetectOperationOtherRESTServices(t *testing.T) {
	tests := []struct {
		name        string
		method      string
		path        string
		signingName string
		wantService string
		want        string
	}{
		{"efs list", http.MethodGet, "/2015-02-01/file-systems", "", "efs", "DescribeFileSystems"},
		{"efs create", http.MethodPost, "/2015-02-01/file-systems", "", "efs", "CreateFileSystem"},
		{"efs delete", http.MethodDelete, "/2015-02-01/file-systems/fs-1", "", "efs", "DeleteFileSystem"},
		{"efs mount targets", http.MethodGet, "/2015-02-01/mount-targets", "", "efs", "DescribeMountTargets"},

		{"pipes list", http.MethodGet, "/v1/pipes", "", "pipes", "ListPipes"},
		{"pipes create", http.MethodPost, "/v1/pipes/p1", "", "pipes", "CreatePipe"},
		{"pipes describe", http.MethodGet, "/v1/pipes/p1", "", "pipes", "DescribePipe"},
		{"pipes update", http.MethodPut, "/v1/pipes/p1", "", "pipes", "UpdatePipe"},
		{"pipes delete", http.MethodDelete, "/v1/pipes/p1", "", "pipes", "DeletePipe"},

		{"appsync list apis", http.MethodGet, "/v1/apis", "", "appsync", "ListGraphqlApis"},
		{"appsync create api", http.MethodPost, "/v1/apis", "", "appsync", "CreateGraphqlApi"},
		{"appsync get api", http.MethodGet, "/v1/apis/api-1", "", "appsync", "GetGraphqlApi"},
		{"appsync delete api", http.MethodDelete, "/v1/apis/api-1", "", "appsync", "DeleteGraphqlApi"},

		{"cloudfront list", http.MethodGet, "/2020-05-31/distribution", "", "cloudfront", "ListDistributions"},
		{"cloudfront create", http.MethodPost, "/2020-05-31/distribution", "", "cloudfront", "CreateDistribution"},
		{"cloudfront get", http.MethodGet, "/2020-05-31/distribution/d1", "", "cloudfront", "GetDistribution"},

		{"route53 list zones", http.MethodGet, "/2013-04-01/hostedzone", "", "route53", "ListHostedZones"},
		{"route53 create zone", http.MethodPost, "/2013-04-01/hostedzone", "", "route53", "CreateHostedZone"},

		{"apigw list rest apis", http.MethodGet, "/restapis", "", "apigateway", "GetRestApis"},
		{"apigw create rest api", http.MethodPost, "/restapis", "", "apigateway", "CreateRestApi"},
		{"apigw resources", http.MethodGet, "/restapis/api-1/resources", "", "apigateway", "GetResources"},
		{"apigw api keys", http.MethodGet, "/apikeys", "", "apigateway", "GetApiKeys"},
		{"apigw usage plans", http.MethodGet, "/usageplans", "", "apigateway", "GetUsagePlans"},

		{"ses v2 send", http.MethodPost, "/v2/email/outbound-emails", "", "ses", "SendEmail"},
		{"ses v2 identities", http.MethodGet, "/v2/email/identities", "", "ses", "ListEmailIdentities"},

		// /v2/apis is shared: the SigV4 scope decides the service, and the
		// models name the operation differently for each, so the label must
		// follow the caller and not the first service the trie happens to hold.
		{"apigw v2 get apis", http.MethodGet, "/v2/apis", "apigateway", "apigateway", "GetApis"},
		{"apigw v2 create api", http.MethodPost, "/v2/apis", "apigateway", "apigateway", "CreateApi"},
		{"appsync events list apis", http.MethodGet, "/v2/apis", "appsync", "appsync", "ListApis"},

		// The other shared bindings Overcast serves on both sides. Each names
		// its operation differently per service, so a single retained name
		// could only ever answer one of them.
		{"appregistry get configuration", http.MethodGet, "/configuration", "appregistry", "appregistry", "GetConfiguration"},
		{"apigw get tags", http.MethodGet, "/tags/arn%3Aaws%3Aapigateway%3A%3A%3Arestapis-a1", "apigateway", "apigateway", "GetTags"},
		{"backup list tags", http.MethodGet, "/tags/arn%3Aaws%3Abackup%3A%3A%3Avault%3Av1", "backup", "backup", "ListTags"},
		{"eks list tags for resource", http.MethodGet, "/tags/arn%3Aaws%3Aeks%3A%3A%3Acluster%3Ac1", "eks", "eks", "ListTagsForResource"},
		{"scheduler get schedule", http.MethodGet, "/schedules/s1", "scheduler", "scheduler", "GetSchedule"},

		// A REST service with no path prefix of its own, identified purely by
		// the SigV4 credential scope. Before the fix these were S3 shapes.
		{"eks describe cluster", http.MethodGet, "/clusters/c1", "eks", "eks", "DescribeCluster"},
		{"eks create cluster", http.MethodPost, "/clusters", "eks", "eks", "CreateCluster"},
		{"eks delete cluster", http.MethodDelete, "/clusters/c1", "eks", "eks", "DeleteCluster"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var r *http.Request
			if tt.signingName == "" {
				r = httptest.NewRequest(tt.method, tt.path, nil)
			} else {
				r = signedRequest(tt.method, tt.path, tt.signingName)
			}
			if got := detectService(r); got != tt.wantService {
				t.Fatalf("detectService(%s) = %q, want %q", tt.path, got, tt.wantService)
			}
			if got := detectOperation(r); got != tt.want {
				t.Errorf("detectOperation(%s %s) = %q, want %q", tt.method, tt.path, got, tt.want)
			}
		})
	}
}

// TestDetectOperationS3TablesLegacyGetTable pins the label and IAM action for
// GetTable's pre-2025-06-06 binding, GET /tables/{tableBucketARN}/{namespace}/{name}
// (#2264). The pinned model binds GetTable only at /get-table, so without an
// explicit mapping the legacy request carries no operation. IAMEnforce treats
// an unnamed request as its one fail-open case, so a policy denying
// s3tables:GetTable would not have covered the SDKs that still send the path
// form.
func TestDetectOperationS3TablesLegacyGetTable(t *testing.T) {
	const table = "/tables/arn%3Aaws%3As3tables%3Aus-east-1%3A000000000000%3Abucket%2Fb1/ns/orders"
	tests := []struct {
		name   string
		method string
		path   string
		want   string
	}{
		{"current binding", http.MethodGet, "/get-table?tableBucketARN=a&namespace=ns&name=orders", "GetTable"},
		{"legacy binding", http.MethodGet, table, "GetTable"},
		{"legacy binding, unescaped ARN", http.MethodGet, "/tables/arn:aws:s3tables:us-east-1:000000000000:bucket%2Fb1/ns/orders", "GetTable"},
		{"delete on the same path", http.MethodDelete, table, "DeleteTable"},
		{"sub-resource", http.MethodGet, table + "/policy", "GetTablePolicy"},
		{"no fourth segment", http.MethodPut, table, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Given: a request signed for S3 Tables
			r := signedRequest(tt.method, tt.path, "s3tables")

			// When: the logger and IAM classify it
			label := detectOperation(r)
			action := classifyIAM(r).action

			// Then: both name the operation S3 Tables serves there
			if label != tt.want {
				t.Errorf("detectOperation(%s %s) = %q, want %q", tt.method, tt.path, label, tt.want)
			}
			wantAction := ""
			if tt.want != "" {
				wantAction = "s3tables:" + tt.want
			}
			if action != wantAction {
				t.Errorf("IAM action(%s %s) = %q, want %q", tt.method, tt.path, action, wantAction)
			}
		})
	}
}

// TestDetectOperationS3Unchanged is the counterweight to the fix: it must not
// have been achieved by labelling less. Every S3 shape the old switch knew
// still resolves.
func TestDetectOperationS3Unchanged(t *testing.T) {
	tests := []struct {
		name   string
		method string
		path   string
		header map[string]string
		want   string
	}{
		{"list buckets", http.MethodGet, "/", nil, "ListBuckets"},
		{"create bucket", http.MethodPut, "/my-bucket", nil, "CreateBucket"},
		{"delete bucket", http.MethodDelete, "/my-bucket", nil, "DeleteBucket"},
		{"head bucket", http.MethodHead, "/my-bucket", nil, "HeadBucket"},
		{"put bucket versioning", http.MethodPut, "/my-bucket?versioning", nil, "PutBucketVersioning"},
		{"get bucket location", http.MethodGet, "/my-bucket?location", nil, "GetBucketLocation"},
		{"list objects v2", http.MethodGet, "/my-bucket?list-type=2", nil, "ListObjectsV2"},
		{"list objects by prefix", http.MethodGet, "/my-bucket?prefix=logs%2F", nil, "ListObjectsV2"},
		{"get object", http.MethodGet, "/my-bucket/key", nil, "GetObject"},
		{"get nested object", http.MethodGet, "/my-bucket/a/b/c.txt", nil, "GetObject"},
		{"put object", http.MethodPut, "/my-bucket/key", nil, "PutObject"},
		{"head object", http.MethodHead, "/my-bucket/key", nil, "HeadObject"},
		{"delete object", http.MethodDelete, "/my-bucket/key", nil, "DeleteObject"},
		{"copy object", http.MethodPut, "/my-bucket/key", map[string]string{"X-Amz-Copy-Source": "/src/obj"}, "CopyObject"},
		{"create multipart upload", http.MethodPost, "/my-bucket/key?uploads", nil, "CreateMultipartUpload"},
		{"upload part", http.MethodPut, "/my-bucket/key?partNumber=1&uploadId=abc", nil, "UploadPart"},
		{"list parts", http.MethodGet, "/my-bucket/key?uploadId=abc", nil, "ListParts"},
		{"abort multipart upload", http.MethodDelete, "/my-bucket/key?uploadId=abc", nil, "AbortMultipartUpload"},
		{"delete objects", http.MethodPost, "/my-bucket?delete", nil, ""},
		{"delete objects on key", http.MethodPost, "/my-bucket/key?delete", nil, "DeleteObjects"},
		{"x-id wins over shape", http.MethodGet, "/bucket/key?x-id=GetObject", nil, "GetObject"},
		{"x-id names a bucket operation", http.MethodPost, "/?x-id=ListBuckets", nil, "ListBuckets"},
		{"x-id percent decoded", http.MethodGet, "/bucket/key?x-id=Get%4Fbject", nil, "GetObject"},
		{"x-id among other params", http.MethodPut, "/bucket/key?partNumber=1&x-id=UploadPart", nil, "UploadPart"},
		// An S3-signed request must not pick up another service's binding even
		// when the bucket name happens to match one of its literal segments.
		{"bucket named like another service", http.MethodGet, "/analyzer/key", nil, "GetObject"},
		{"bucket named clusters", http.MethodGet, "/clusters/key", nil, "GetObject"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest(tt.method, tt.path, nil)
			for k, v := range tt.header {
				r.Header.Set(k, v)
			}
			if got := detectService(r); got != "s3" {
				t.Fatalf("detectService(%s) = %q, want s3", tt.path, got)
			}
			if got := detectOperation(r); got != tt.want {
				t.Errorf("detectOperation(%s %s) = %q, want %q", tt.method, tt.path, got, tt.want)
			}
		})
	}
}

// TestDetectOperationUnknownPathHasNoOperation covers the invariant that
// failed: a path no service claims must yield no operation rather than
// borrowing one from whichever heuristics sat lowest in the switch.
func TestDetectOperationUnknownPathHasNoOperation(t *testing.T) {
	tests := []struct {
		name        string
		method      string
		path        string
		signingName string
	}{
		{"unmodeled lambda version", http.MethodGet, "/2099-01-01/functions/fn", "lambda"},
		{"unmodeled lambda subresource", http.MethodPut, "/2015-03-31/functions/fn/not-a-thing", "lambda"},
		{"wrong method on a lambda route", http.MethodPatch, "/2015-03-31/functions/fn/configuration", "lambda"},
		{"unknown path on a rest service", http.MethodGet, "/no/such/thing", "lambda"},
		{"unknown path signed for eks", http.MethodGet, "/no/such/thing", "eks"},
		{"unknown path signed for appsync", http.MethodPost, "/v1/not-an-appsync-route", "appsync"},
		{"cloudfront unknown resource", http.MethodPut, "/2020-05-31/not-a-resource", ""},
		{"efs unknown resource", http.MethodPost, "/2015-02-01/not-a-resource", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var r *http.Request
			if tt.signingName == "" {
				r = httptest.NewRequest(tt.method, tt.path, nil)
			} else {
				r = signedRequest(tt.method, tt.path, tt.signingName)
			}
			if got := detectOperation(r); got != "" {
				t.Errorf("detectOperation(%s %s) = %q, want \"\"", tt.method, tt.path, got)
			}
		})
	}
}

// TestDetectServiceCoversModeledLambdaVersions guards isLambdaAPIVersionPrefix
// against a model refresh adding an API version. A missing prefix drops that
// whole operation family into the S3 fallback, which is the shape of the bug
// this file exists to fix.
func TestDetectServiceCoversModeledLambdaVersions(t *testing.T) {
	seen := map[string]bool{}
	awsapi.WalkOperations(func(op awsapi.Operation) bool {
		if awsapi.ServiceKey(op.Service) != "lambda" || !strings.HasPrefix(op.URI, "/2") {
			return true
		}
		i := strings.IndexByte(op.URI[1:], '/')
		if i < 0 {
			return true
		}
		seen[op.URI[:i+2]] = true
		return true
	})
	if len(seen) == 0 {
		t.Fatal("no modeled Lambda API version prefixes found")
	}
	for prefix := range seen {
		if !isLambdaAPIVersionPrefix(prefix) {
			t.Errorf("isLambdaAPIVersionPrefix(%q) = false; add it to detectService", prefix)
		}
	}
}

// TestRestOperationNeverNamesAnotherService walks every modeled operation of
// the REST-routed services and asserts the scoped lookup either names that
// service's own operation or names nothing. It is the drift guard: if a model
// refresh reshapes a binding, this fails rather than a service quietly
// inheriting another's label.
func TestRestOperationNeverNamesAnotherService(t *testing.T) {
	checked := 0
	awsapi.WalkOperations(func(op awsapi.Operation) bool {
		svc := awsapi.ServiceKey(op.Service)
		if !restRoutedServices[svc] || op.URI == "" || op.HTTPMethod == "" {
			return true
		}
		path, query := concreteRESTPath(op.URI)
		got := restOperation(svc, restRequest(op.HTTPMethod, path, query))
		checked++
		if got != "" && got != op.Name {
			t.Errorf("restOperation(%s, %s %s) = %q, want %q or \"\"",
				svc, op.HTTPMethod, op.URI, got, op.Name)
		}
		return true
	})
	if checked == 0 {
		t.Fatal("walked no modeled REST operations")
	}
}

// TestRestOperationNamesEveryARNLabelledBinding is the generic form of the MSK
// cases above, and the guard that keeps them from being the only ones covered.
// It walks every modeled binding of the REST-routed services whose URI carries
// an ARN label, encodes that label the way an SDK does, and asserts the lookup
// still names an operation.
//
// Before the escaped-path walk every one of these answered "" — the blind spot
// was never MSK's, only most visible there. A model refresh that reshapes a
// binding, or a service added to restRoutedServices whose ARNs are handled some
// other way, fails here rather than silently going unlabelled and ungated.
func TestRestOperationNamesEveryARNLabelledBinding(t *testing.T) {
	checked := 0
	awsapi.WalkOperations(func(op awsapi.Operation) bool {
		svc := awsapi.ServiceKey(op.Service)
		if !restRoutedServices[svc] || op.URI == "" || op.HTTPMethod == "" {
			return true
		}
		path, query, ok := arnLabelledRESTPath(op.URI)
		if !ok {
			return true
		}
		checked++
		r := restRequest(op.HTTPMethod, path, query)
		if r.URL.EscapedPath() == r.URL.Path {
			t.Fatalf("%s %s: built target %q carries no escaped path", svc, op.Name, path)
		}
		got := restOperation(svc, r)
		if got == "" {
			t.Errorf("restOperation(%s, %s %s) = \"\", want %q (ARN label unmatched)",
				svc, op.HTTPMethod, op.URI, op.Name)
			return true
		}

		// The escaped walk takes precedence, so it must not displace an answer
		// the decoded walk was already giving. Decoding an escaped ARN splits
		// one segment into several, and a deeper binding of the right shape
		// could in principle match that longer path — this asserts that no
		// pinned binding does, rather than assuming it.
		if decoded := awsapi.NewRegistry().RESTOperation(
			awsapiServiceKey(svc), op.HTTPMethod, r.URL.Path, r.URL.RawQuery,
		); decoded != "" && decoded != got {
			t.Errorf("restOperation(%s, %s %s) = %q, but the decoded path named %q; the escaped walk displaced an existing answer",
				svc, op.HTTPMethod, op.URI, got, decoded)
		}
		return true
	})
	if checked == 0 {
		t.Fatal("walked no modeled REST operations carrying an ARN label")
	}
}

// restRoutedServices is the set of services whose surface reaches restOperation
// by method and path rather than by X-Amz-Target or a Query Action.
var restRoutedServices = map[string]bool{
	"lambda": true, "efs": true, "pipes": true, "appsync": true, "ses": true,
	"cloudfront": true, "route53": true, "apigateway": true, "appregistry": true,
	"msk": true,
}

// restRequest builds the request restOperation reads. It takes the path and
// query separately because the caller has them separately; the target is
// reassembled so url.Parse sets RawPath for any path that needs one.
func restRequest(method, path, query string) *http.Request {
	target := path
	if query != "" {
		target += "?" + query
	}
	return httptest.NewRequest(method, target, nil)
}

// arnLabelledRESTPath substitutes a Smithy URI template's labels the way
// concreteRESTPath does, except that a label naming an ARN gets a percent-
// encoded ARN — the single segment an SDK actually sends. It reports false for
// a URI with no ARN label, and for a greedy ARN label, which binds the
// remaining path and so was never affected.
func arnLabelledRESTPath(uri string) (path, query string, ok bool) {
	path = uri
	if i := strings.IndexByte(uri, '?'); i >= 0 {
		path, query = uri[:i], uri[i+1:]
	}
	segments := strings.Split(path, "/")
	for i, segment := range segments {
		if !strings.HasPrefix(segment, "{") || !strings.HasSuffix(segment, "}") {
			continue
		}
		if strings.HasSuffix(segment, "+}") {
			segments[i] = "greedy/value"
			continue
		}
		label := strings.TrimSuffix(strings.TrimPrefix(segment, "{"), "}")
		if strings.HasSuffix(strings.ToLower(label), "arn") {
			segments[i] = escapedARN(
				fmt.Sprintf("arn:aws:svc:us-east-1:000000000000:resource/demo/v%d", i))
			ok = true
			continue
		}
		segments[i] = fmt.Sprintf("v%d", i)
	}
	return strings.Join(segments, "/"), query, ok
}

// escapedARN percent-encodes an ARN the way an AWS SDK does when it binds one
// to a non-greedy httpLabel: the whole value becomes a single URI segment, so
// its slashes and colons are escaped rather than left to divide segments.
// smithy-go, botocore and the JS SDK all agree on this.
func escapedARN(arn string) string {
	return strings.NewReplacer("/", "%2F", ":", "%3A").Replace(arn)
}

const testClusterARN = "arn:aws:kafka:us-east-1:000000000000:cluster/demo/e8b4c1a2-1111-2222-3333-444455556666-7"

// TestDetectOperationNamesARNLabelledBindings covers the decoded-path blind
// spot. url.URL.Path is the request target *decoded*, so the single segment an
// SDK sent for an ARN label reappears as three — "cluster", "demo", the UUID —
// and the generated trie matches no binding of that shape. Every operation
// whose URI carries an ARN therefore went unnamed: no label on the log line, no
// operation on the metric, and no action for IAM to evaluate.
//
// This is the same mechanism PR #1000 fixed for the router's 501 fallback, on
// the second of the two paths that walk the trie. It is not MSK-specific — MSK
// is simply the service that puts an ARN in a URI most.
func TestDetectOperationNamesARNLabelledBindings(t *testing.T) {
	escaped := escapedARN(testClusterARN)
	operationARN := escapedARN("arn:aws:kafka:us-east-1:000000000000:cluster-operation/demo/7e1f")

	tests := []struct {
		name    string
		signing string
		method  string
		path    string
		want    string
	}{
		{"describe cluster", "kafka", http.MethodGet, "/v1/clusters/" + escaped, "DescribeCluster"},
		{"delete cluster", "kafka", http.MethodDelete, "/v1/clusters/" + escaped, "DeleteCluster"},
		{"list cluster operations", "kafka", http.MethodGet, "/v1/clusters/" + escaped + "/operations", "ListClusterOperations"},
		{"get bootstrap brokers", "kafka", http.MethodGet, "/v1/clusters/" + escaped + "/bootstrap-brokers", "GetBootstrapBrokers"},
		{"list nodes", "kafka", http.MethodGet, "/v1/clusters/" + escaped + "/nodes", "ListNodes"},
		{"update broker count", "kafka", http.MethodPut, "/v1/clusters/" + escaped + "/nodes/count", "UpdateBrokerCount"},
		{"describe cluster v2", "kafka", http.MethodGet, "/api/v2/clusters/" + escaped, "DescribeClusterV2"},
		{"list cluster operations v2", "kafka", http.MethodGet, "/api/v2/clusters/" + escaped + "/operations", "ListClusterOperationsV2"},
		{"describe cluster operation v2", "kafka", http.MethodGet, "/api/v2/operations/" + operationARN, "DescribeClusterOperationV2"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Given: a request encoded the way the AWS SDK encodes an ARN label.
			r := signedRequest(tt.method, tt.path, tt.signing)
			if r.URL.EscapedPath() == r.URL.Path {
				t.Fatalf("test target %q did not survive parsing as an escaped path", tt.path)
			}

			// When/Then: the modeled operation is named.
			if got := detectOperation(r); got != tt.want {
				t.Errorf("detectOperation(%s %s) = %q, want %q",
					tt.method, tt.path, got, tt.want)
			}
		})
	}
}

// TestDetectOperationKeepsDecodedPathMatches pins what the escaped-first walk
// must not cost. Trying the escaped form first can only add matches, never lose
// one, because the decoded form is still tried second — so every path named
// before the change is still named after it.
//
// The last row is the one that makes the fallback load-bearing rather than
// merely defensive: percent-encoding a *literal* segment ("%6Eodes" for
// "nodes") makes the escaped walk miss where the decoded walk still matches,
// because a literal is compared byte for byte while a label accepts whatever
// segment it is given. Without the second walk that request would lose an
// operation it is named today.
func TestDetectOperationKeepsDecodedPathMatches(t *testing.T) {
	tests := []struct {
		name    string
		signing string
		method  string
		path    string
		want    string
	}{
		{"plain collection", "kafka", http.MethodGet, "/v1/clusters", "ListClusters"},
		{"plain sub-resource", "kafka", http.MethodGet, "/v1/clusters/demo-cluster/nodes", "ListNodes"},
		// Escaped and decoded both match here — a label takes either spelling —
		// so this pins that a RawPath alone does not change the answer.
		{"escaped label", "kafka", http.MethodGet, "/v1/clusters/demo%2Dcluster/nodes", "ListNodes"},
		// Escaped misses, decoded matches: only the fallback can answer this.
		{"escaped literal segment", "kafka", http.MethodGet, "/v1/clusters/demo/%6Eodes", "ListNodes"},
		{"lambda control", "lambda", http.MethodGet, "/2015-03-31/functions", "ListFunctions"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := signedRequest(tt.method, tt.path, tt.signing)
			if got := detectOperation(r); got != tt.want {
				t.Errorf("detectOperation(%s %s) = %q, want %q",
					tt.method, tt.path, got, tt.want)
			}
		})
	}
}

// concreteRESTPath substitutes a Smithy URI template's labels for concrete
// segments so it can be fed back through the matcher.
func concreteRESTPath(uri string) (path, query string) {
	path = uri
	if i := strings.IndexByte(uri, '?'); i >= 0 {
		path, query = uri[:i], uri[i+1:]
	}
	segments := strings.Split(path, "/")
	for i, segment := range segments {
		if !strings.HasPrefix(segment, "{") || !strings.HasSuffix(segment, "}") {
			continue
		}
		if strings.HasSuffix(segment, "+}") {
			segments[i] = "greedy/value"
			continue
		}
		segments[i] = fmt.Sprintf("v%d", i)
	}
	return strings.Join(segments, "/"), query
}

func TestRawQueryHas(t *testing.T) {
	tests := []struct {
		rawQuery string
		key      string
		want     bool
	}{
		{"", "uploads", false},
		{"uploads", "uploads", true},
		{"uploads=", "uploads", true},
		{"uploads=1", "uploads", true},
		{"partNumber=1&uploadId=abc", "uploadId", true},
		{"partNumber=1&uploadId=abc", "partNumber", true},
		{"uploadIdentifier=abc", "uploadId", false},
		{"xuploads", "uploads", false},
		{"a=1&uploads&b=2", "uploads", true},
	}
	for _, tt := range tests {
		if got := rawQueryHas(tt.rawQuery, tt.key); got != tt.want {
			t.Errorf("rawQueryHas(%q, %q) = %v, want %v", tt.rawQuery, tt.key, got, tt.want)
		}
	}
}

func TestRawQueryValue(t *testing.T) {
	tests := []struct {
		rawQuery string
		key      string
		want     string
	}{
		{"", "x-id", ""},
		{"x-id", "x-id", ""},
		{"x-id=", "x-id", ""},
		{"x-id=GetObject", "x-id", "GetObject"},
		{"a=1&x-id=PutObject&b=2", "x-id", "PutObject"},
		{"prefix-x-id=Nope", "x-id", ""},
		{"x-id=Get%4Fbject", "x-id", "GetObject"},
		{"x-id=a+b", "x-id", "a b"},
	}
	for _, tt := range tests {
		if got := rawQueryValue(tt.rawQuery, tt.key); got != tt.want {
			t.Errorf("rawQueryValue(%q, %q) = %q, want %q", tt.rawQuery, tt.key, got, tt.want)
		}
	}
}

func TestPathDepth(t *testing.T) {
	tests := []struct {
		path string
		want int
	}{
		{"/", 1},
		{"/bucket", 1},
		{"/bucket/", 1},
		{"/bucket/key", 2},
		{"/bucket/a/b/c", 4},
		{"/bucket//key", 3},
	}
	for _, tt := range tests {
		if got := pathDepth(tt.path); got != tt.want {
			t.Errorf("pathDepth(%q) = %d, want %d", tt.path, got, tt.want)
		}
	}
}

func BenchmarkDetectOperationS3Object(b *testing.B) {
	r := httptest.NewRequest(http.MethodPut, "/my-bucket/some/key.txt", nil)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if detectOperation(r) != "PutObject" {
			b.Fatal("unexpected operation")
		}
	}
}

func BenchmarkDetectOperationS3MultipartQuery(b *testing.B) {
	r := httptest.NewRequest(http.MethodPut, "/my-bucket/key?partNumber=3&uploadId=abc123", nil)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if detectOperation(r) != "UploadPart" {
			b.Fatal("unexpected operation")
		}
	}
}

func BenchmarkDetectOperationLambdaInvoke(b *testing.B) {
	r := httptest.NewRequest(http.MethodPost, "/2015-03-31/functions/my-function/invocations", nil)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if detectOperation(r) != "Invoke" {
			b.Fatal("unexpected operation")
		}
	}
}

func BenchmarkDetectOperationAmbiguousBinding(b *testing.B) {
	r := httptest.NewRequest(http.MethodGet, "/2017-03-31/tags/arn%3Aaws%3Alambda%3A%3A%3Afunction%3Afn", nil)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if detectOperation(r) != "ListTags" {
			b.Fatal("unexpected operation")
		}
	}
}
