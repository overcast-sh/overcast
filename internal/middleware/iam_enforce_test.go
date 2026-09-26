package middleware

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"go.uber.org/zap"

	"github.com/overcast-sh/overcast/internal/state"
)

// classifyIAM is requestIAMOperation for a router that serves no AWS Query
// traffic, which is how every request is classified that does not reach the
// router's Query dispatch.
func classifyIAM(r *http.Request) iamOperation {
	op, _ := requestIAMOperation(nil, r, nil)
	return op
}

func TestIAMEnforce_disabled_passthroughUnsigned(t *testing.T) {
	called := false
	h := IAMEnforce(false, nil, zap.NewNop(), nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusNoContent)
	}))

	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"QueueName":"demo"}`))
	req.Header.Set("Content-Type", "application/x-amz-json-1.0")
	req.Header.Set("X-Amz-Target", "AmazonSQS.CreateQueue")
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if !called {
		t.Fatal("expected next handler to be called")
	}
	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected status %d, got %d", http.StatusNoContent, rec.Code)
	}
}

func TestIAMEnforce_enabled_deniesUnsignedJSON(t *testing.T) {
	h := IAMEnforce(true, nil, zap.NewNop(), nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"QueueName":"demo"}`))
	req.Header.Set("Content-Type", "application/x-amz-json-1.0")
	req.Header.Set("X-Amz-Target", "AmazonSQS.CreateQueue")
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected status %d, got %d", http.StatusForbidden, rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "AccessDeniedException") {
		t.Fatalf("expected AccessDeniedException, got body %q", rec.Body.String())
	}
}

func TestIAMEnforce_enabled_deniesUnsignedQueryXML(t *testing.T) {
	h := IAMEnforce(true, nil, zap.NewNop(), nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	req := httptest.NewRequest(http.MethodPost, "/?Action=GetCallerIdentity&Version=2011-06-15", nil)
	req.Header.Set("Host", "localhost:4566")
	req.Header.Set("Authorization", "")
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected status %d, got %d", http.StatusForbidden, rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "<Code>AccessDenied</Code>") {
		t.Fatalf("expected Query XML AccessDenied, got body %q", rec.Body.String())
	}
}

func TestIAMEnforce_enabled_deniesSignedRequestWithoutPrincipal(t *testing.T) {
	called := false
	h := IAMEnforce(true, state.NewMemoryStore(), zap.NewNop(), nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusNoContent)
	}))

	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"QueueName":"demo"}`))
	req.Header.Set("Content-Type", "application/x-amz-json-1.0")
	req.Header.Set("X-Amz-Target", "AmazonSQS.CreateQueue")
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=test/20260423/us-east-1/sqs/aws4_request, SignedHeaders=host;x-amz-date, Signature=abc")
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if called {
		t.Fatal("expected next handler not to be called")
	}
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected status %d, got %d", http.StatusForbidden, rec.Code)
	}
}

func TestIAMEnforce_enabled_bypassesInternalRoutes(t *testing.T) {
	called := false
	h := IAMEnforce(true, state.NewMemoryStore(), zap.NewNop(), nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusNoContent)
	}))

	req := httptest.NewRequest(http.MethodGet, "/_overcast/health", nil)
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if !called {
		t.Fatal("expected internal route to bypass IAM enforcement")
	}
	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected status %d, got %d", http.StatusNoContent, rec.Code)
	}
}

// TestIAMEnforce_enabled_deniesUnsignedRequestsUnderAPIPrefix pins the boundary
// the bypass is allowed to cover. shouldBypassIAM exempted "/api" and "/api/*"
// from the day the file was written, for a console BFF that has never been
// behind this middleware — IAMEnforce is wired once, at router.go's AWS mux,
// and the UI is a separate listener with its own handler (cmd_serve.go).
//
// Both subtests below are that one arm, and the order they are written in is
// the order the hole opened:
//
//   - "api" is a legal S3 bucket name (3 chars, lowercase), so from the first
//     release with IAM enforcement, every object request against a bucket of
//     that name reached the S3 fallback with no policy evaluated.
//   - #894 then bound MSK's v2 cluster API to /api/v2/clusters, the path the
//     pinned kafka model gives it, and every v2 operation joined it.
//
// The general lesson outlasts the fix: a prefix bypass written against path
// space nobody owned was already wrong, and got worse when AWS turned out to
// model a service there. "/_" is reserved because S3 bucket names cannot begin
// with an underscore. "/api" was reserved by nothing.
func TestIAMEnforce_enabled_deniesUnsignedRequestsUnderAPIPrefix(t *testing.T) {
	for _, tc := range []struct {
		name string
		path string
	}{
		{name: "s3 bucket named api", path: "/api/some-key"},
		{name: "msk v2 cluster api", path: "/api/v2/clusters"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			called := false
			h := IAMEnforce(true, state.NewMemoryStore(), zap.NewNop(), nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				called = true
				w.WriteHeader(http.StatusNoContent)
			}))

			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			rec := httptest.NewRecorder()

			h.ServeHTTP(rec, req)

			if called {
				t.Fatalf("unsigned request to %s reached the handler: /api/* still bypasses IAM enforcement", tc.path)
			}
			if rec.Code != http.StatusForbidden {
				t.Fatalf("expected status %d, got %d", http.StatusForbidden, rec.Code)
			}
		})
	}
}

func TestIAMEnforce_enabled_allowsSignedRequestWithMatchingPolicy(t *testing.T) {
	st := state.NewMemoryStore()
	seedIAMUserWithPolicies(t, st, "test", []string{`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"sqs:CreateQueue","Resource":"*"}]}`}, nil)

	called := false
	h := IAMEnforce(true, st, zap.NewNop(), nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusNoContent)
	}))

	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"QueueName":"demo"}`))
	req.Header.Set("Content-Type", "application/x-amz-json-1.0")
	req.Header.Set("X-Amz-Target", "AmazonSQS.CreateQueue")
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=test/20260423/us-east-1/sqs/aws4_request, SignedHeaders=host;x-amz-date, Signature=abc")
	req.Header.Set("X-Amz-Date", "20260423T000000Z")
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if !called {
		t.Fatal("expected next handler to be called")
	}
	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected status %d, got %d", http.StatusNoContent, rec.Code)
	}
}

func TestIAMEnforce_enabled_explicitDenyOverridesAllow(t *testing.T) {
	st := state.NewMemoryStore()
	seedIAMUserWithPolicies(t, st, "test", []string{`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"sqs:*","Resource":"*"},{"Effect":"Deny","Action":"sqs:DeleteQueue","Resource":"*"}]}`}, nil)

	h := IAMEnforce(true, st, zap.NewNop(), nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"QueueUrl":"https://localhost/000000000000/demo"}`))
	req.Header.Set("Content-Type", "application/x-amz-json-1.0")
	req.Header.Set("X-Amz-Target", "AmazonSQS.DeleteQueue")
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=test/20260423/us-east-1/sqs/aws4_request, SignedHeaders=host;x-amz-date, Signature=abc")
	req.Header.Set("X-Amz-Date", "20260423T000000Z")
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected status %d, got %d", http.StatusForbidden, rec.Code)
	}
}

func TestIAMEnforce_enabled_allowsSignedRequestWithGroupInlinePolicy(t *testing.T) {
	st := state.NewMemoryStore()
	seedIAMUserWithPolicies(t, st, "test", nil, nil)
	seedIAMGroupWithPolicies(t, st, "devs", []string{"test"}, []string{`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"sqs:CreateQueue","Resource":"*"}]}`}, nil)

	called := false
	h := IAMEnforce(true, st, zap.NewNop(), nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusNoContent)
	}))

	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"QueueName":"demo"}`))
	req.Header.Set("Content-Type", "application/x-amz-json-1.0")
	req.Header.Set("X-Amz-Target", "AmazonSQS.CreateQueue")
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=test/20260423/us-east-1/sqs/aws4_request, SignedHeaders=host;x-amz-date, Signature=abc")
	req.Header.Set("X-Amz-Date", "20260423T000000Z")
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if !called {
		t.Fatal("expected next handler to be called")
	}
	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected status %d, got %d", http.StatusNoContent, rec.Code)
	}
}

func TestIAMEnforce_enabled_groupExplicitDenyOverridesUserAllow(t *testing.T) {
	st := state.NewMemoryStore()
	seedIAMUserWithPolicies(t, st, "test", []string{`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"sqs:DeleteQueue","Resource":"*"}]}`}, nil)
	seedIAMGroupWithPolicies(t, st, "security", []string{"test"}, nil, map[string]string{
		"arn:aws:iam::000000000000:policy/deny-delete": `{"Version":"2012-10-17","Statement":[{"Effect":"Deny","Action":"sqs:DeleteQueue","Resource":"*"}]}`,
	})

	h := IAMEnforce(true, st, zap.NewNop(), nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"QueueUrl":"https://localhost/000000000000/demo"}`))
	req.Header.Set("Content-Type", "application/x-amz-json-1.0")
	req.Header.Set("X-Amz-Target", "AmazonSQS.DeleteQueue")
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=test/20260423/us-east-1/sqs/aws4_request, SignedHeaders=host;x-amz-date, Signature=abc")
	req.Header.Set("X-Amz-Date", "20260423T000000Z")
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected status %d, got %d", http.StatusForbidden, rec.Code)
	}
}

// TestIAMEnforce_enabled_arnLabelledPathExplicitDenyBlocks is the enforcement
// consequence of the decoded-path blind spot, and the reason it is not merely a
// labelling bug.
//
// requestIAMAction could name no action for a path carrying a percent-encoded
// ARN, so every such request took IAMEnforce's deliberately fail-open
// unnamed-action branch: a policy that explicitly denied the operation was
// never consulted. The denial the user wrote is the assertion here — an Allow
// passing proves only that nothing broke, while a Deny taking effect proves the
// action reached the evaluator at all.
func TestIAMEnforce_enabled_arnLabelledPathExplicitDenyBlocks(t *testing.T) {
	st := state.NewMemoryStore()
	seedIAMUserWithPolicies(t, st, "test", []string{`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"kafka:*","Resource":"*"},{"Effect":"Deny","Action":"kafka:DeleteCluster","Resource":"*"}]}`}, nil)

	h := IAMEnforce(true, st, zap.NewNop(), nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	req := httptest.NewRequest(http.MethodDelete, "/v1/clusters/"+escapedARN(testClusterARN), nil)
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=test/20260423/us-east-1/kafka/aws4_request, SignedHeaders=host;x-amz-date, Signature=abc")
	req.Header.Set("X-Amz-Date", "20260423T000000Z")
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected status %d, got %d", http.StatusForbidden, rec.Code)
	}
}

// TestIAMEnforce_enabled_arnLabelledPathAllowPasses is the other half: naming
// the action must not deny a caller whose policy permits it. Without this the
// test above is satisfied by denying everything.
func TestIAMEnforce_enabled_arnLabelledPathAllowPasses(t *testing.T) {
	st := state.NewMemoryStore()
	seedIAMUserWithPolicies(t, st, "test", []string{`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"kafka:DescribeCluster","Resource":"*"}]}`}, nil)

	called := false
	h := IAMEnforce(true, st, zap.NewNop(), nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusNoContent)
	}))

	req := httptest.NewRequest(http.MethodGet, "/v1/clusters/"+escapedARN(testClusterARN), nil)
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=test/20260423/us-east-1/kafka/aws4_request, SignedHeaders=host;x-amz-date, Signature=abc")
	req.Header.Set("X-Amz-Date", "20260423T000000Z")
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if !called {
		t.Fatal("expected next handler to be called")
	}
	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected status %d, got %d", http.StatusNoContent, rec.Code)
	}
}

func TestIAMEnforce_enabled_restFallbackS3ExplicitDenyBlocks(t *testing.T) {
	st := state.NewMemoryStore()
	seedIAMUserWithPolicies(t, st, "test", []string{`{"Version":"2012-10-17","Statement":[{"Effect":"Deny","Action":"s3:ListBuckets","Resource":"*"}]}`}, nil)

	h := IAMEnforce(true, st, zap.NewNop(), nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=test/20260423/us-east-1/s3/aws4_request, SignedHeaders=host;x-amz-date, Signature=abc")
	req.Header.Set("X-Amz-Date", "20260423T000000Z")
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected status %d, got %d", http.StatusForbidden, rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "<Code>AccessDenied</Code>") {
		t.Fatalf("expected XML AccessDenied body, got %q", rec.Body.String())
	}
}

func TestIAMEnforce_enabled_restFallbackS3AllowPasses(t *testing.T) {
	st := state.NewMemoryStore()
	seedIAMUserWithPolicies(t, st, "test", []string{`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"s3:ListBuckets","Resource":"*"}]}`}, nil)

	called := false
	h := IAMEnforce(true, st, zap.NewNop(), nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusNoContent)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=test/20260423/us-east-1/s3/aws4_request, SignedHeaders=host;x-amz-date, Signature=abc")
	req.Header.Set("X-Amz-Date", "20260423T000000Z")
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if !called {
		t.Fatal("expected next handler to be called")
	}
	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected status %d, got %d", http.StatusNoContent, rec.Code)
	}
}

func TestIAMEnforce_enabled_notActionAllow_allowsNonExcludedAction(t *testing.T) {
	st := state.NewMemoryStore()
	seedIAMUserWithPolicies(t, st, "test", []string{`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","NotAction":"sqs:DeleteQueue","Resource":"*"}]}`}, nil)

	called := false
	h := IAMEnforce(true, st, zap.NewNop(), nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusNoContent)
	}))

	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"QueueName":"demo"}`))
	req.Header.Set("Content-Type", "application/x-amz-json-1.0")
	req.Header.Set("X-Amz-Target", "AmazonSQS.CreateQueue")
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=test/20260423/us-east-1/sqs/aws4_request, SignedHeaders=host;x-amz-date, Signature=abc")
	req.Header.Set("X-Amz-Date", "20260423T000000Z")
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if !called {
		t.Fatal("expected next handler to be called")
	}
	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected status %d, got %d", http.StatusNoContent, rec.Code)
	}
}

func TestIAMEnforce_enabled_notActionAllow_deniesExcludedAction(t *testing.T) {
	st := state.NewMemoryStore()
	seedIAMUserWithPolicies(t, st, "test", []string{`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","NotAction":"sqs:DeleteQueue","Resource":"*"}]}`}, nil)

	h := IAMEnforce(true, st, zap.NewNop(), nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"QueueUrl":"https://localhost/000000000000/demo"}`))
	req.Header.Set("Content-Type", "application/x-amz-json-1.0")
	req.Header.Set("X-Amz-Target", "AmazonSQS.DeleteQueue")
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=test/20260423/us-east-1/sqs/aws4_request, SignedHeaders=host;x-amz-date, Signature=abc")
	req.Header.Set("X-Amz-Date", "20260423T000000Z")
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected status %d, got %d", http.StatusForbidden, rec.Code)
	}
}

func TestIAMEnforce_enabled_notResourceAllow_deniesExcludedResource(t *testing.T) {
	st := state.NewMemoryStore()
	seedIAMUserWithPolicies(t, st, "test", []string{`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"sqs:DeleteQueue","NotResource":"arn:aws:sqs:us-east-1:000000000000:blocked-queue"}]}`}, nil)

	h := IAMEnforce(true, st, zap.NewNop(), nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"QueueUrl":"https://localhost/000000000000/blocked-queue"}`))
	req.Header.Set("Content-Type", "application/x-amz-json-1.0")
	req.Header.Set("X-Amz-Target", "AmazonSQS.DeleteQueue")
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=test/20260423/us-east-1/sqs/aws4_request, SignedHeaders=host;x-amz-date, Signature=abc")
	req.Header.Set("X-Amz-Date", "20260423T000000Z")
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected status %d, got %d", http.StatusForbidden, rec.Code)
	}
}

func TestIAMEnforce_enabled_notResourceAllow_allowsOtherResource(t *testing.T) {
	st := state.NewMemoryStore()
	seedIAMUserWithPolicies(t, st, "test", []string{`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"sqs:DeleteQueue","NotResource":"arn:aws:sqs:us-east-1:000000000000:blocked-queue"}]}`}, nil)

	called := false
	h := IAMEnforce(true, st, zap.NewNop(), nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusNoContent)
	}))

	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"QueueUrl":"https://localhost/000000000000/other-queue"}`))
	req.Header.Set("Content-Type", "application/x-amz-json-1.0")
	req.Header.Set("X-Amz-Target", "AmazonSQS.DeleteQueue")
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=test/20260423/us-east-1/sqs/aws4_request, SignedHeaders=host;x-amz-date, Signature=abc")
	req.Header.Set("X-Amz-Date", "20260423T000000Z")
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if !called {
		t.Fatal("expected next handler to be called")
	}
	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected status %d, got %d", http.StatusNoContent, rec.Code)
	}
}

func TestIAMEnforce_enabled_roleSessionInlinePolicy_allows(t *testing.T) {
	st := state.NewMemoryStore()
	// No user record — the access key belongs to a role session.
	seedIAMRoleSession(t, st, "ASIA-role-key", "arn:aws:iam::123456789012:role/my-role", "my-role")
	seedIAMRoleWithPolicies(t, st, "my-role",
		[]string{`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"sqs:CreateQueue","Resource":"*"}]}`},
		nil,
	)

	called := false
	h := IAMEnforce(true, st, zap.NewNop(), nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusNoContent)
	}))

	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"QueueName":"demo"}`))
	req.Header.Set("Content-Type", "application/x-amz-json-1.0")
	req.Header.Set("X-Amz-Target", "AmazonSQS.CreateQueue")
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=ASIA-role-key/20260423/us-east-1/sqs/aws4_request, SignedHeaders=host;x-amz-date, Signature=abc")
	req.Header.Set("X-Amz-Date", "20260423T000000Z")
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if !called {
		t.Fatal("expected next handler to be called")
	}
	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected status %d, got %d", http.StatusNoContent, rec.Code)
	}
}

func TestIAMEnforce_enabled_roleSessionInlinePolicy_denies(t *testing.T) {
	st := state.NewMemoryStore()
	seedIAMRoleSession(t, st, "ASIA-role-key", "arn:aws:iam::123456789012:role/my-role", "my-role")
	seedIAMRoleWithPolicies(t, st, "my-role",
		[]string{`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"sqs:CreateQueue","Resource":"*"}]}`},
		nil,
	)

	h := IAMEnforce(true, st, zap.NewNop(), nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"QueueUrl":"https://localhost/000000000000/demo"}`))
	req.Header.Set("Content-Type", "application/x-amz-json-1.0")
	req.Header.Set("X-Amz-Target", "AmazonSQS.DeleteQueue")
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=ASIA-role-key/20260423/us-east-1/sqs/aws4_request, SignedHeaders=host;x-amz-date, Signature=abc")
	req.Header.Set("X-Amz-Date", "20260423T000000Z")
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected status %d, got %d", http.StatusForbidden, rec.Code)
	}
}

func TestIAMEnforce_enabled_roleSessionExplicitDeny_overridesAllow(t *testing.T) {
	st := state.NewMemoryStore()
	seedIAMRoleSession(t, st, "ASIA-role-key", "arn:aws:iam::123456789012:role/my-role", "my-role")
	seedIAMRoleWithPolicies(t, st, "my-role",
		[]string{`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"sqs:*","Resource":"*"},{"Effect":"Deny","Action":"sqs:DeleteQueue","Resource":"*"}]}`},
		nil,
	)

	h := IAMEnforce(true, st, zap.NewNop(), nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"QueueUrl":"https://localhost/000000000000/demo"}`))
	req.Header.Set("Content-Type", "application/x-amz-json-1.0")
	req.Header.Set("X-Amz-Target", "AmazonSQS.DeleteQueue")
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=ASIA-role-key/20260423/us-east-1/sqs/aws4_request, SignedHeaders=host;x-amz-date, Signature=abc")
	req.Header.Set("X-Amz-Date", "20260423T000000Z")
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected status %d, got %d", http.StatusForbidden, rec.Code)
	}
}

func TestIAMEnforce_enabled_roleSessionManagedPolicy_allows(t *testing.T) {
	st := state.NewMemoryStore()
	seedIAMRoleSession(t, st, "ASIA-role-key", "arn:aws:iam::123456789012:role/my-role", "my-role")
	seedIAMRoleWithPolicies(t, st, "my-role", nil, map[string]string{
		"arn:aws:iam::aws:policy/SQSFullAccess": `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"sqs:*","Resource":"*"}]}`,
	})

	called := false
	h := IAMEnforce(true, st, zap.NewNop(), nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusNoContent)
	}))

	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"QueueName":"demo"}`))
	req.Header.Set("Content-Type", "application/x-amz-json-1.0")
	req.Header.Set("X-Amz-Target", "AmazonSQS.CreateQueue")
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=ASIA-role-key/20260423/us-east-1/sqs/aws4_request, SignedHeaders=host;x-amz-date, Signature=abc")
	req.Header.Set("X-Amz-Date", "20260423T000000Z")
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if !called {
		t.Fatal("expected next handler to be called")
	}
	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected status %d, got %d", http.StatusNoContent, rec.Code)
	}
}

func TestIAMRequestFieldResolver_jsonFieldsAndArray_doNotConsumeBody(t *testing.T) {
	body := `{"TableName":"demo-table","SecretId":"demo-secret","clusters":["cluster-a"]}`
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-amz-json-1.1")

	fields := newIAMRequestFieldResolver()

	if got := fields.field(req, "TableName"); got != "demo-table" {
		t.Fatalf("unexpected table name: %q", got)
	}
	if got := fields.field(req, "SecretId"); got != "demo-secret" {
		t.Fatalf("unexpected secret id: %q", got)
	}
	if got := fields.firstJSONArrayStringField(req, "clusters"); got != "cluster-a" {
		t.Fatalf("unexpected first clusters value: %q", got)
	}

	raw, err := io.ReadAll(req.Body)
	if err != nil {
		t.Fatalf("read request body: %v", err)
	}
	if got := string(raw); got != body {
		t.Fatalf("expected request body to be preserved, got %q", got)
	}
}

func TestIAMRegionOrDefault_usesSigV4RegionWhenPresent(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=test/20260423/eu-west-1/sqs/aws4_request, SignedHeaders=host;x-amz-date, Signature=abc")

	if got := iamRegionOrDefault(req); got != "eu-west-1" {
		t.Fatalf("expected eu-west-1, got %q", got)
	}
}

func TestIAMRegionOrDefault_fallsBackToUSEast1(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/", nil)

	if got := iamRegionOrDefault(req); got != "us-east-1" {
		t.Fatalf("expected us-east-1 fallback, got %q", got)
	}
}

func TestIAMRequestFieldResolver_precedence(t *testing.T) {
	t.Run("query over json", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/?Name=query-value", strings.NewReader(`{"Name":"json-value"}`))
		req.Header.Set("Content-Type", "application/x-amz-json-1.1")

		fields := newIAMRequestFieldResolver()
		if got := fields.field(req, "Name"); got != "query-value" {
			t.Fatalf("expected query value, got %q", got)
		}
	})

	t.Run("form over missing query", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("Name=form-value"))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

		fields := newIAMRequestFieldResolver()
		if got := fields.field(req, "Name"); got != "form-value" {
			t.Fatalf("expected form value, got %q", got)
		}
	})

	t.Run("json fallback", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"Name":"json-value"}`))
		req.Header.Set("Content-Type", "application/x-amz-json-1.1")

		fields := newIAMRequestFieldResolver()
		if got := fields.field(req, "Name"); got != "json-value" {
			t.Fatalf("expected json value, got %q", got)
		}
	})
}

func seedIAMUserWithPolicies(t *testing.T, st state.Store, accessKey string, inlinePolicies []string, managedPolicies map[string]string) {
	t.Helper()

	ctx := context.Background()
	inline := map[string]string{}
	for i, doc := range inlinePolicies {
		inline["inline-"+strconv.Itoa(i+1)] = doc
	}

	attached := make([]map[string]string, 0, len(managedPolicies))
	for arn, doc := range managedPolicies {
		attached = append(attached, map[string]string{
			"PolicyArn": arn,
		})
		body, err := json.Marshal(map[string]any{"Document": doc})
		if err != nil {
			t.Fatalf("marshal managed policy: %v", err)
		}
		if err := st.Set(ctx, "iam:policies", arn, string(body)); err != nil {
			t.Fatalf("seed managed policy: %v", err)
		}
	}

	user := map[string]any{
		"UserName":         accessKey,
		"AccessKeys":       []map[string]string{{"AccessKeyId": accessKey}},
		"InlinePolicies":   inline,
		"AttachedPolicies": attached,
	}
	b, err := json.Marshal(user)
	if err != nil {
		t.Fatalf("marshal user seed: %v", err)
	}
	if err := st.Set(ctx, "iam:users", accessKey, string(b)); err != nil {
		t.Fatalf("seed user: %v", err)
	}
}

func seedIAMGroupWithPolicies(
	t *testing.T,
	st state.Store,
	groupName string,
	members []string,
	inlinePolicies []string,
	managedPolicies map[string]string,
) {
	t.Helper()

	ctx := context.Background()
	inline := map[string]string{}
	for i, doc := range inlinePolicies {
		inline["inline-"+strconv.Itoa(i+1)] = doc
	}

	attached := make([]map[string]string, 0, len(managedPolicies))
	for arn, doc := range managedPolicies {
		attached = append(attached, map[string]string{
			"PolicyArn": arn,
		})
		body, err := json.Marshal(map[string]any{"Document": doc})
		if err != nil {
			t.Fatalf("marshal managed policy: %v", err)
		}
		if err := st.Set(ctx, "iam:policies", arn, string(body)); err != nil {
			t.Fatalf("seed managed policy: %v", err)
		}
	}

	group := map[string]any{
		"Members":          members,
		"InlinePolicies":   inline,
		"AttachedPolicies": attached,
	}
	b, err := json.Marshal(group)
	if err != nil {
		t.Fatalf("marshal group seed: %v", err)
	}
	if err := st.Set(ctx, "iam:groups", groupName, string(b)); err != nil {
		t.Fatalf("seed group: %v", err)
	}
}

func seedIAMRoleSession(t *testing.T, st state.Store, accessKeyID, roleArn, roleName string) {
	t.Helper()
	session := map[string]string{
		"RoleArn":  roleArn,
		"RoleName": roleName,
	}
	b, err := json.Marshal(session)
	if err != nil {
		t.Fatalf("marshal role session: %v", err)
	}
	if err := st.Set(context.Background(), "iam:sessions", accessKeyID, string(b)); err != nil {
		t.Fatalf("seed role session: %v", err)
	}
}

func seedIAMRoleWithPolicies(t *testing.T, st state.Store, roleName string, inlinePolicies []string, managedPolicies map[string]string) {
	t.Helper()

	ctx := context.Background()
	inline := map[string]string{}
	for i, doc := range inlinePolicies {
		inline["inline-"+strconv.Itoa(i+1)] = doc
	}

	attached := make([]map[string]string, 0, len(managedPolicies))
	for arn, doc := range managedPolicies {
		attached = append(attached, map[string]string{
			"PolicyArn": arn,
		})
		body, err := json.Marshal(map[string]any{"Document": doc})
		if err != nil {
			t.Fatalf("marshal managed policy: %v", err)
		}
		if err := st.Set(ctx, "iam:policies", arn, string(body)); err != nil {
			t.Fatalf("seed managed policy: %v", err)
		}
	}

	role := map[string]any{
		"RoleName":         roleName,
		"InlinePolicies":   inline,
		"AttachedPolicies": attached,
	}
	b, err := json.Marshal(role)
	if err != nil {
		t.Fatalf("marshal role seed: %v", err)
	}
	if err := st.Set(ctx, "iam:roles", roleName, string(b)); err != nil {
		t.Fatalf("seed role: %v", err)
	}
}

// ─── Cost of the off path ────────────────────────────────────────────────────

// countingStore counts reads so a test can assert the evaluator does not touch
// the store at all.
type countingStore struct {
	state.Store
	reads int
}

func (c *countingStore) Get(ctx context.Context, namespace, key string) (string, bool, error) {
	c.reads++
	return c.Store.Get(ctx, namespace, key)
}

func (c *countingStore) Scan(ctx context.Context, namespace, prefix string) ([]state.KV, error) {
	c.reads++
	return c.Store.Scan(ctx, namespace, prefix)
}

func TestIAMEnforce_disabled_readsNothingFromTheStore(t *testing.T) {
	// Given: enforcement off (the default) over a store that counts reads
	st := &countingStore{Store: state.NewMemoryStore()}
	h := IAMEnforce(false, st, zap.NewNop(), nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	// When: a signed request goes through
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"QueueName":"demo"}`))
	req.Header.Set("Content-Type", "application/x-amz-json-1.0")
	req.Header.Set("X-Amz-Target", "AmazonSQS.CreateQueue")
	req.Header.Set("Authorization",
		"AWS4-HMAC-SHA256 Credential=test/20260101/us-east-1/sqs/aws4_request, SignedHeaders=host, Signature=deadbeef")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	// Then: the request is served and the evaluator cost nothing
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNoContent)
	}
	if st.reads != 0 {
		t.Fatalf("store reads = %d, want 0 when enforcement is off", st.reads)
	}
}

func TestIAMEnforce_enabled_compilesPrincipalPoliciesOncePerPrincipal(t *testing.T) {
	// Given: a user with a policy, and enforcement on
	st := &countingStore{Store: state.NewMemoryStore()}
	seedIAMUserWithPolicies(t, st, "AKIATEST", []string{`{"Statement":[{"Effect":"Allow","Action":"sqs:*","Resource":"*"}]}`}, nil)
	h := IAMEnforce(true, st, zap.NewNop(), nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	InvalidateIAMEnforceCache()

	send := func() {
		req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"QueueName":"demo"}`))
		req.Header.Set("Content-Type", "application/x-amz-json-1.0")
		req.Header.Set("X-Amz-Target", "AmazonSQS.CreateQueue")
		req.Header.Set("Authorization",
			"AWS4-HMAC-SHA256 Credential=AKIATEST/20260423/us-east-1/sqs/aws4_request, SignedHeaders=host;x-amz-date, Signature=abc")
		req.Header.Set("X-Amz-Date", "20260423T000000Z")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusNoContent {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusNoContent)
		}
	}

	// When: the same principal makes two requests
	send()
	afterFirst := st.reads
	send()

	// Then: the second is served from the compiled cache, with no further reads
	if afterFirst == 0 {
		t.Fatal("expected the first request to read the principal's policies")
	}
	if st.reads != afterFirst {
		t.Fatalf("store reads = %d after the second request, want %d — the compiled policy cache was not used",
			st.reads, afterFirst)
	}
}

func TestIAMEnforce_enabled_policyChangeReachesAWarmCache(t *testing.T) {
	// Given: a principal whose compiled policies are already cached by a
	// running middleware instance
	st := state.NewMemoryStore()
	seedIAMUserWithPolicies(t, st, "AKIAWARM", []string{`{"Statement":[{"Effect":"Allow","Action":"sqs:*","Resource":"*"}]}`}, nil)
	h := IAMEnforce(true, st, zap.NewNop(), nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	InvalidateIAMEnforceCache()

	send := func() int {
		req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"QueueName":"demo"}`))
		req.Header.Set("Content-Type", "application/x-amz-json-1.0")
		req.Header.Set("X-Amz-Target", "AmazonSQS.CreateQueue")
		req.Header.Set("Authorization",
			"AWS4-HMAC-SHA256 Credential=AKIAWARM/20260423/us-east-1/sqs/aws4_request, SignedHeaders=host;x-amz-date, Signature=abc")
		req.Header.Set("X-Amz-Date", "20260423T000000Z")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}
	if code := send(); code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d before the policy changed", code, http.StatusNoContent)
	}

	// When: the policy is replaced with one that does not allow the action,
	// and the IAM service reports the mutation
	seedIAMUserWithPolicies(t, st, "AKIAWARM", []string{`{"Statement":[{"Effect":"Allow","Action":"s3:*","Resource":"*"}]}`}, nil)
	InvalidateIAMEnforceCache()

	// Then: the warm cache is discarded and the new policy decides
	if code := send(); code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d — the cache survived an invalidation", code, http.StatusForbidden)
	}
}
