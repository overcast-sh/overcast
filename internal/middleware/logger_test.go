package middleware

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/overcast-sh/overcast/internal/clock"
	"github.com/overcast-sh/overcast/internal/protocol"
	"github.com/overcast-sh/overcast/internal/serviceutil"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

// s3SignedAuth is an Authorization header signed for S3.
const s3SignedAuth = "AWS4-HMAC-SHA256 Credential=AKID/20260623/us-east-1/s3/aws4_request, SignedHeaders=host, Signature=abc"

func TestDetectService(t *testing.T) {
	tests := []struct {
		name   string
		method string
		path   string
		header map[string]string // optional headers
		host   string            // optional Host header (host-routed cases)
		want   string
	}{
		// X-Amz-Target — JSON-protocol services
		{name: "sqs target", method: "POST", path: "/", header: map[string]string{"X-Amz-Target": "AmazonSQS.CreateQueue"}, want: "sqs"},
		{name: "dynamodb target", method: "POST", path: "/", header: map[string]string{"X-Amz-Target": "DynamoDB_20120810.PutItem"}, want: "dynamodb"},
		{name: "cognito target", method: "POST", path: "/", header: map[string]string{"X-Amz-Target": "AWSCognitoIdentityProviderService.ListUsers"}, want: "cognito"},

		// Well-known URL prefixes — REST-protocol services
		{name: "lambda path", method: "GET", path: "/2015-03-31/functions", want: "lambda"},
		{name: "pipes path", method: "GET", path: "/v1/pipes", want: "pipes"},
		{name: "appsync apis", method: "GET", path: "/v1/apis", want: "appsync"},
		{name: "appsync graphql", method: "POST", path: "/_overcast/appsync/apis/api-id/graphql", want: "appsync"},
		{name: "ses path", method: "GET", path: "/v2/email/identities", want: "ses"},
		{name: "cloudfront path", method: "GET", path: "/2020-05-31/distribution", want: "cloudfront"},
		{name: "apigateway restapis", method: "GET", path: "/restapis", want: "apigateway"},
		// AWS Backup's three subtrees, all unsigned: the two the switch has
		// always claimed and /backup-access-point (#1467), whose six
		// operations would otherwise be labelled — and IAM-authorised — as an
		// s3 request to a bucket called "backup-access-point".
		{name: "backup vaults", method: "GET", path: "/backup-vaults", want: "backup"},
		{name: "backup plans", method: "GET", path: "/backup/plans", want: "backup"},
		{name: "backup access point list", method: "GET", path: "/backup-access-point", want: "backup"},
		{name: "backup access point create", method: "PUT", path: "/backup-access-point/create", want: "backup"},
		{name: "backup access point describe", method: "GET", path: "/backup-access-point/arn%3Aaws%3Abackup%3Aus-east-1%3A000000000000%3Aaccesspoint%2Fap-one", want: "backup"},
		{name: "apigateway v2", method: "GET", path: "/v2/apis", want: "apigateway"},
		{name: "appsync events v2", method: "GET", path: "/v2/apis", header: map[string]string{"Authorization": "AWS4-HMAC-SHA256 Credential=AKID/20260623/us-east-1/appsync/aws4_request, SignedHeaders=host;x-amz-date, Signature=abc"}, want: "appsync"},
		// /applications is S3's when S3-signed or virtual-hosted: the router
		// sends such a request to a bucket named "applications" (#2098). S3
		// Control shares the signing name but not the routing.
		{name: "s3-signed applications", method: "GET", path: "/applications", header: map[string]string{"Authorization": s3SignedAuth}, want: "s3"},
		{name: "s3express-signed applications", method: "GET", path: "/applications/key.txt", header: map[string]string{"Authorization": "AWS4-HMAC-SHA256 Credential=AKID/20260623/us-east-1/s3express/aws4_request, SignedHeaders=host, Signature=abc"}, want: "s3"},
		{name: "virtual-hosted bucket named applications", method: "GET", path: "/key.txt", host: "applications.s3.localhost:4566", want: "s3"},
		{name: "bucket merely prefixed applications", method: "GET", path: "/applications-archive/key.txt", want: "s3"},
		{name: "s3 control-signed applications", method: "GET", path: "/applications", header: map[string]string{"Authorization": s3SignedAuth, s3ControlAccountHeader: "000000000000"}, want: "appregistry"},

		// Internal /_overcast/events and /_overcast/metrics
		{name: "events", method: "GET", path: "/_overcast/events", want: "events"},
		{name: "metrics", method: "GET", path: "/_overcast/metrics", want: "metrics"},

		// Emulator-internal /_-prefixed paths — must NOT fall through to S3
		{name: "health", method: "GET", path: "/_overcast/health", want: "internal"},
		{name: "topology", method: "GET", path: "/_overcast/topology", want: "internal"},
		{name: "info", method: "GET", path: "/_overcast/info", want: "internal"},
		{name: "debug", method: "GET", path: "/_overcast/debug/store", want: "internal"},
		{name: "cognito oauth", method: "GET", path: "/_overcast/cognito/user-pools/us-east-1_ABC/oauth2/authorize", want: "cognito"},
		{name: "cognito login", method: "POST", path: "/_overcast/cognito/user-pools/us-east-1_ABC/login", want: "cognito"},
		{name: "ecs tasks", method: "GET", path: "/_overcast/ecs/clusters/default/tasks", want: "ecs"},
		{name: "lambda instances", method: "GET", path: "/_overcast/lambda/instances", want: "lambda"},
		{name: "cloudfront proxy", method: "GET", path: "/_overcast/cloudfront/distributions/EDIST123/index.html", want: "cloudfront"},
		{name: "secretsmanager internal", method: "GET", path: "/_overcast/secretsmanager/secrets", want: "secretsmanager"},
		{name: "mail internal", method: "GET", path: "/_overcast/ses/inbox/messages", want: "ses"},

		// Host-routed AWS-style addresses (execute-api / lambda-url /
		// appsync-api Host subdomains) — see hostroute.go. Labelled from the
		// claim HostAddressing stamped on the request, regardless of what
		// internal path convention the (already-applied) rewrite used.
		{name: "host-routed execute-api", method: "GET", path: "/_overcast/apigateway/execute-api/abc123/us-east-1/prod/pets", host: "abc123.execute-api.us-east-1.amazonaws.com", want: "apigateway"},
		{name: "host-routed execute-api no region", method: "GET", path: "/_overcast/apigateway/execute-api/abc123/-/prod/pets", host: "abc123.execute-api.localhost", want: "apigateway"},
		{name: "host-routed lambda-url", method: "POST", path: "/_overcast/lambda/url-invoke/deadbeef/", host: "deadbeefdeadbeefdeadbeefdeadbeef.lambda-url.us-east-1.amazonaws.com", want: "lambda"},
		{name: "host-routed appsync-api", method: "POST", path: "/_overcast/appsync/apis/api-id/graphql", host: "api-id.appsync-api.us-east-1.amazonaws.com", want: "appsync"},

		// Unrecognised Host label — must NOT be claimed by the host-route
		// table; falls through to whatever the path/other signals say
		// (here, plain S3 fallback per AGENTS.md "Routing fallthrough is S3").
		{name: "unrecognised host label falls through to s3", method: "GET", path: "/my-bucket/key", host: "something.made-up-label.us-east-1.example.com", want: "s3"},

		// S3 fallback — plain paths without distinguishing signals
		{name: "s3 list buckets", method: "GET", path: "/", want: "s3"},
		{name: "s3 get object", method: "GET", path: "/my-bucket/key", want: "s3"},
		{name: "s3 put object", method: "PUT", path: "/my-bucket/key", want: "s3"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest(tt.method, tt.path, nil)
			for k, v := range tt.header {
				r.Header.Set(k, v)
			}
			if tt.host != "" {
				r.Host = tt.host
			}
			// detectService reads the claim HostAddressing stamps, so the
			// request must pass through it exactly as it does in the real
			// chain (router.go registers HostAddressing before Logger). Rows
			// are empty: we want the classification and its stamp, not a path
			// rewrite — these fixtures already carry post-rewrite paths.
			var noRows []HostRouteRow
			got := "s3"
			HostAddressing("", &noRows, nil)(http.HandlerFunc(func(_ http.ResponseWriter, req *http.Request) {
				got = detectService(req)
			})).ServeHTTP(httptest.NewRecorder(), r)
			if got != tt.want {
				t.Errorf("detectService(%s %s) = %q, want %q", tt.method, tt.path, got, tt.want)
			}
		})
	}
}

// TestDetectService_virtualHostedS3StillLabelsS3 verifies that a
// virtual-hosted-style S3 request (bucket in the Host header) is still
// labelled "s3" by detectService after HostAddressing has rewritten the
// URL path. detectService itself never looks at r.Host — it relies on
// HostAddressing running earlier in the middleware chain (see router.go)
// to turn "/key" with Host "mybucket.s3.localhost" into path-style
// "/mybucket/key" before Logger (and therefore detectService) ever sees the
// request. This test exercises that ordering explicitly so a future
// middleware-chain reorder cannot silently mislabel virtual-hosted S3
// traffic as some other service.
func TestDetectService_virtualHostedS3StillLabelsS3(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/key.txt", nil)
	r.Host = "mybucket.s3.localhost"

	// Run the real HostAddressing middleware to perform the rewrite, then
	// inspect the (possibly mutated) request that reaches the next handler —
	// exactly as Logger would.
	var rewritten *http.Request
	var noRows []HostRouteRow
	handler := HostAddressing("", &noRows, nil)(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		rewritten = req
	}))
	handler.ServeHTTP(httptest.NewRecorder(), r)

	if rewritten == nil {
		t.Fatal("expected inner handler to be invoked")
	}
	if rewritten.URL.Path != "/mybucket/key.txt" {
		t.Fatalf("expected rewritten path %q, got %q", "/mybucket/key.txt", rewritten.URL.Path)
	}
	if got := detectService(rewritten); got != "s3" {
		t.Errorf("detectService after virtual-host rewrite = %q, want %q", got, "s3")
	}
}

func TestDetectOperation(t *testing.T) {
	tests := []struct {
		name   string
		method string
		path   string
		header map[string]string
		want   string
	}{
		// X-Amz-Target
		{name: "sqs target", method: "POST", path: "/", header: map[string]string{"X-Amz-Target": "AmazonSQS.CreateQueue"}, want: "CreateQueue"},

		// x-id query param
		{name: "x-id param", method: "GET", path: "/bucket/key?x-id=GetObject", want: "GetObject"},

		// Internal endpoints with known operations
		{name: "events", method: "GET", path: "/_overcast/events", want: "Subscribe"},
		{name: "metrics", method: "GET", path: "/_overcast/metrics", want: "GetMetrics"},

		// Emulator-internal /_-prefixed paths — must return "" (no operation)
		{name: "health", method: "GET", path: "/_overcast/health", want: ""},
		{name: "topology", method: "GET", path: "/_overcast/topology", want: ""},
		{name: "info", method: "GET", path: "/_overcast/info", want: ""},
		{name: "cognito oauth", method: "GET", path: "/_overcast/cognito/user-pools/us-east-1_ABC/oauth2/authorize", want: ""},
		{name: "cognito debug token", method: "GET", path: "/_overcast/cognito/user-pools/us-east-1_ABC/debug/token", want: ""},
		{name: "lambda instances", method: "GET", path: "/_overcast/lambda/instances", want: ""},
		{name: "debug store", method: "GET", path: "/_overcast/debug/store/s3", want: ""},

		// S3 heuristics — should still work for real S3 paths
		{name: "s3 list buckets", method: "GET", path: "/", want: "ListBuckets"},
		{name: "s3 create bucket", method: "PUT", path: "/my-bucket", want: "CreateBucket"},
		{name: "s3 get object", method: "GET", path: "/my-bucket/key", want: "GetObject"},
		{name: "s3 put object", method: "PUT", path: "/my-bucket/key", want: "PutObject"},
		{name: "s3 delete object", method: "DELETE", path: "/my-bucket/key", want: "DeleteObject"},
		{name: "s3 head object", method: "HEAD", path: "/my-bucket/key", want: "HeadObject"},
		{name: "s3 head bucket", method: "HEAD", path: "/my-bucket", want: "HeadBucket"},
		{name: "s3 delete bucket", method: "DELETE", path: "/my-bucket", want: "DeleteBucket"},
		{name: "s3 list objects v2", method: "GET", path: "/my-bucket?list-type=2", want: "ListObjectsV2"},
		{name: "s3 get bucket location", method: "GET", path: "/my-bucket?location", want: "GetBucketLocation"},
		{name: "s3 copy object", method: "PUT", path: "/my-bucket/key", header: map[string]string{"X-Amz-Copy-Source": "/src/obj"}, want: "CopyObject"},
		{name: "s3 create multipart", method: "POST", path: "/my-bucket/key?uploads", want: "CreateMultipartUpload"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest(tt.method, tt.path, nil)
			for k, v := range tt.header {
				r.Header.Set(k, v)
			}
			got := detectOperation(r)
			if got != tt.want {
				t.Errorf("detectOperation(%s %s) = %q, want %q", tt.method, tt.path, got, tt.want)
			}
		})
	}
}

// Query-protocol form parameters arrive in whatever order the client encoded
// them. Detection used to scan only the first 256 bytes of the body for
// Action=, so a request with a large leading parameter went unnamed — a
// CreateStack carrying a real template being the case that surfaced it, since
// the trace list rendered it with a blank operation while DeleteStack, whose
// body is short, resolved fine.
func TestDetectOperationForService_actionPositionInTheBody(t *testing.T) {
	bulky := url.QueryEscape(`{"Description":"` + strings.Repeat("x", 400) + `","Resources":{}}`)

	tests := []struct {
		name string
		body string
		want string
	}{
		{
			name: "action first",
			body: "Action=CreateStack&Version=2010-05-15&StackName=s&TemplateBody=" + bulky,
			want: "CreateStack",
		},
		{
			name: "action after a large leading parameter",
			body: "TemplateBody=" + bulky + "&Version=2010-05-15&StackName=s&Action=CreateStack",
			want: "CreateStack",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Given: a Query-protocol POST with the parameters in this order
			r := httptest.NewRequest("POST", "/", strings.NewReader(tt.body))
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")

			// When: the operation is detected from the captured body
			got := detectOperationForService(r, "cloudformation", []byte(tt.body))

			// Then: the operation is named regardless of where Action= sits
			if got != tt.want {
				t.Errorf("detectOperationForService = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestInternalService(t *testing.T) {
	tests := []struct {
		path string
		want string
	}{
		{"/_overcast/cognito/user-pools/pool/oauth2/authorize", "cognito"},
		{"/_overcast/cognito/user-pools/pool/login", "cognito"},
		{"/_overcast/ecs/clusters/default/tasks", "ecs"},
		{"/_overcast/ecs/tasks/arn/logs/main", "ecs"},
		{"/_overcast/lambda/instances", "lambda"},
		{"/_overcast/lambda/runtimes", "lambda"},
		{"/_overcast/appsync/apis/api-id/graphql", "appsync"},
		{"/_overcast/cloudfront/distributions/EDIST/index.html", "cloudfront"},
		{"/_overcast/secretsmanager/secrets", "secretsmanager"},
		{"/_overcast/secretsmanager/secrets/id/value", "secretsmanager"},
		{"/_overcast/ses/inbox/messages", "ses"},
		{"/_overcast/health", "internal"},
		{"/_overcast/topology", "internal"},
		{"/_overcast/info", "internal"},
		{"/_overcast/debug/store", "internal"},
		{"/_overcast/metrics", "internal"}, // still "internal" via this helper; detectService handles the exact match earlier
		{"/_unknown/path", "internal"},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			got := internalService(tt.path)
			if got != tt.want {
				t.Errorf("internalService(%q) = %q, want %q", tt.path, got, tt.want)
			}
		})
	}
}

// TestIsOperationalPollPath pins which paths are demoted to TRACE. It is the
// logging half of the same judgement trace.isInternalPath makes for the trace
// ring buffer, and the two are meant to agree — trace.go's doc comment says so
// explicitly — so they are pinned in the same terms.
//
// The set is deliberately narrower than "starts with /_": a path is demoted
// only when the request fires because time passed and infrastructure polled,
// never because a client did something. Everything Overcast serves under "/_"
// on behalf of an emulated workload is the second kind and must stay at INFO,
// which is what the user cases below are for.
//
// These were written down before docs/plans/non-canonical-url-namespace.md
// moved the paths, because the distinction used to survive on an accident: the
// data-plane routes sat on first segments of their own that nobody had thought
// to demote. Phase 5 put them under /_overcast/ alongside the polling
// endpoints, so the accident is gone and the allowlist is now the only thing
// keeping them apart. Getting it wrong would not fail anything — it would just
// make a client's request quietly log a level lower than it should.
func TestIsOperationalPollPath(t *testing.T) {
	polled := []string{
		"/_overcast/health",
		"/_overcast/debug",
		"/_overcast/debug/state",
		"/_overcast/debug/traces",
		"/_overcast/debug/traces/search",
	}
	for _, p := range polled {
		if !isOperationalPollPath(p) {
			t.Errorf("isOperationalPollPath(%q) = false, want true", p)
		}
	}

	client := []string{
		"/",
		"/my-bucket/key",
		"/2015-03-31/functions",
		"/_overcast/debugfoo",
		"/_overcast/init",

		// Data plane: a real client's request, however it is spelled. Same
		// set as trace.TestIsInternalPathSeparatesPollingFromClientTraffic —
		// the two predicates are meant to agree, so they are given the same
		// paths to agree about.
		"/_overcast/appsync/apis/abc123/graphql",
		"/_overcast/appsync/apis/abc123/realtime",
		"/_overcast/apigateway/execute-api/abc123/us-east-1/test/hello",
		"/_overcast/lambda/url-invoke/abc123/",
		"/_overcast/cloudfront/distributions/E123456789/index.html",
		"/_overcast/elb/healthz",
		"/_overcast/cognito/user-pools/us-east-1_abc123/login",
		"/_overcast/cognito/user-pools/us-east-1_abc123/oauth2/token",
	}
	for _, p := range client {
		if isOperationalPollPath(p) {
			t.Errorf("isOperationalPollPath(%q) = true, want false", p)
		}
	}
}

func TestLogger_healthCheckLogsAtTrace(t *testing.T) {
	// Given: a /_overcast/health request (polled every few seconds by Docker/K8s
	// healthchecks) and a real AWS API call (SQS CreateQueue) through the
	// same middleware.
	core, logs := observer.New(serviceutil.TraceLevel)
	logger := zap.New(core)
	handler := Logger(logger, clock.New())(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// When: a health-check request is served.
	healthReq := httptest.NewRequest(http.MethodGet, "/_overcast/health", nil)
	handler.ServeHTTP(httptest.NewRecorder(), healthReq)

	// And: a debug-namespace request is served.
	debugReq := httptest.NewRequest(http.MethodGet, "/_overcast/debug/state", nil)
	handler.ServeHTTP(httptest.NewRecorder(), debugReq)

	// And: a real AWS API request is served.
	awsReq := httptest.NewRequest(http.MethodPost, "/", nil)
	awsReq.Header.Set("X-Amz-Target", "AmazonSQS.CreateQueue")
	handler.ServeHTTP(httptest.NewRecorder(), awsReq)

	// Then: the health-check and debug-namespace requests log at TRACE...
	for _, path := range []string{"/_overcast/health", "/_overcast/debug/state"} {
		found := false
		for _, e := range logs.FilterMessage("request").All() {
			if e.ContextMap()["path"] == path {
				found = true
				if e.Level != serviceutil.TraceLevel {
					t.Errorf("path %s logged at %s, want trace", path, e.Level)
				}
			}
		}
		if !found {
			t.Fatalf("no 'request' log entry found for path %s", path)
		}
	}

	// ...and the real AWS API request still logs at INFO.
	awsFound := false
	for _, e := range logs.FilterMessage("request").All() {
		if e.ContextMap()["path"] == "/" {
			awsFound = true
			if e.Level != zapcore.InfoLevel {
				t.Errorf("AWS API request logged at %s, want info", e.Level)
			}
		}
	}
	if !awsFound {
		t.Fatal("no 'request' log entry found for the AWS API call")
	}
}

func TestLogger_awsInternalError(t *testing.T) {
	// Given: a handler writes a wrapped AWS InternalError.
	core, logs := observer.New(zapcore.DebugLevel)
	logger := zap.New(core)
	handler := Logger(logger, clock.New())(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read body in handler: %v", err)
		}
		if string(body) != `{"QueueUrl":"http://localhost:4566/000000000000/q"}` {
			t.Fatalf("handler body = %q", string(body))
		}
		protocol.WriteJSONError(w, r, protocol.Wrap(protocol.ErrInternalError, errors.New("state scan: database is locked")))
	}))
	req := httptest.NewRequest(http.MethodPost, "/?trace=1", strings.NewReader(`{"QueueUrl":"http://localhost:4566/000000000000/q"}`))
	req.Header.Set("X-Amz-Target", "AmazonSQS.ReceiveMessage")
	req.Header.Set("Content-Type", "application/x-amz-json-1.0")
	rec := httptest.NewRecorder()

	// When: the request fails with a 500.
	handler.ServeHTTP(rec, req)

	// Then: the failure log includes the AWS error and its internal cause.
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	entries := logs.FilterMessage("request failed").All()
	if len(entries) != 1 {
		t.Fatalf("request failed log entries = %d, want 1", len(entries))
	}
	ctx := entries[0].ContextMap()
	if got := ctx["aws_error_code"]; got != "InternalError" {
		t.Fatalf("aws_error_code = %v, want InternalError", got)
	}
	if got := ctx["aws_error_cause"]; got != "state scan: database is locked" {
		t.Fatalf("aws_error_cause = %v, want state scan: database is locked", got)
	}
	if got := ctx["request_uri"]; got != "/?trace=1" {
		t.Fatalf("request_uri = %v, want /?trace=1", got)
	}
	if got := ctx["request_body"]; got != `{"QueueUrl":"http://localhost:4566/000000000000/q"}` {
		t.Fatalf("request_body = %v", got)
	}
	headers, ok := ctx["request_headers"].(http.Header)
	if !ok {
		t.Fatalf("request_headers type = %T, want http.Header", ctx["request_headers"])
	}
	if got := headers.Get("X-Amz-Target"); got != "AmazonSQS.ReceiveMessage" {
		t.Fatalf("X-Amz-Target header = %q", got)
	}
}
