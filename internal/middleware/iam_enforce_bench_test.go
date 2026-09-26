package middleware

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/overcast-sh/overcast/internal/state"
)

func BenchmarkRequestIAMResource_SQSCreateQueue(b *testing.B) {
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"QueueName":"bench-queue"}`))
	req.Header.Set("Content-Type", "application/x-amz-json-1.0")
	req.Header.Set("X-Amz-Target", "AmazonSQS.CreateQueue")
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=test/20260423/us-east-1/sqs/aws4_request, SignedHeaders=host;x-amz-date, Signature=abc")
	req.Header.Set("X-Amz-Date", "20260423T000000Z")

	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = requestIAMResource(req, classifyIAM(req))
	}
}

func BenchmarkRequestIAMResource_ECSDeleteCluster(b *testing.B) {
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"cluster":"bench-cluster"}`))
	req.Header.Set("Content-Type", "application/x-amz-json-1.1")
	req.Header.Set("X-Amz-Target", "AmazonEC2ContainerServiceV20141113.DeleteCluster")
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=test/20260423/us-east-1/ecs/aws4_request, SignedHeaders=host;x-amz-date, Signature=abc")
	req.Header.Set("X-Amz-Date", "20260423T000000Z")

	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = requestIAMResource(req, classifyIAM(req))
	}
}

func BenchmarkEvaluateIAMDecision_SQSAllow(b *testing.B) {
	st := state.NewMemoryStore()
	seedIAMUserForBench(b, st, "test", `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"sqs:CreateQueue","Resource":"*"}]}`)

	req := benchIAMRequest()

	b.ReportAllocs()
	cache := &iamEnforceCache{}
	for i := 0; i < b.N; i++ {
		_ = evaluateIAMDecision(req, st, "test", benchCreateQueue, benchQueueARN, cache)
	}
}

// ─── Cache effect: cold vs warm ──────────────────────────────────────────────
//
// The four benchmarks below are read in pairs. "Warm" reuses one cache across
// iterations, which is what a running emulator does between IAM mutations.
// "Cold" calls InvalidateIAMEnforceCache in the loop — the same path a real
// PutUserPolicy/AttachUserPolicy takes — so every iteration pays for the store
// reads, the JSON parse and the wildcard compilation again. The gap between a
// pair is what the cache is worth; allocs/op is the figure to trust, since
// timings taken over a bind-mounted repo in Docker are noisy.
//
// The seeded policy is deliberately not a single literal statement: two
// statements with wildcard actions and resources, so compilation is real work
// rather than something the compiler can trivially fold away.

const benchPolicyDoc = `{"Version":"2012-10-17","Statement":[
	{"Effect":"Allow","Action":["sqs:*Queue*","sqs:SendMessage*"],"Resource":["arn:aws:sqs:*:*:bench-*","arn:aws:sqs:us-east-1:000000000000:*"]},
	{"Effect":"Deny","Action":"sqs:DeleteQueue","Resource":"arn:aws:sqs:*:*:protected-*"}
]}`

// benchIAMRequest is the request every evaluation benchmark below runs against.
func benchIAMRequest() *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"QueueName":"bench-queue"}`))
	req.Header.Set("Content-Type", "application/x-amz-json-1.0")
	req.Header.Set("X-Amz-Target", "AmazonSQS.CreateQueue")
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=test/20260423/us-east-1/sqs/aws4_request, SignedHeaders=host;x-amz-date, Signature=abc")
	req.Header.Set("X-Amz-Date", "20260423T000000Z")
	return req
}

const benchQueueARN = "arn:aws:sqs:us-east-1:000000000000:bench-queue"

var benchCreateQueue = iamOperation{service: "sqs", action: "sqs:CreateQueue"}

func BenchmarkEvaluateIAMDecision_WarmCache(b *testing.B) {
	st := state.NewMemoryStore()
	seedIAMUserForBench(b, st, "test", benchPolicyDoc)
	req := benchIAMRequest()
	cache := &iamEnforceCache{}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = evaluateIAMDecision(req, st, "test", benchCreateQueue, benchQueueARN, cache)
	}
}

func BenchmarkEvaluateIAMDecision_ColdCache(b *testing.B) {
	st := state.NewMemoryStore()
	seedIAMUserForBench(b, st, "test", benchPolicyDoc)
	req := benchIAMRequest()
	cache := &iamEnforceCache{}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// What an IAM mutation does — not a fresh struct, so the benchmark
		// exercises the real invalidation path.
		InvalidateIAMEnforceCache()
		_ = evaluateIAMDecision(req, st, "test", benchCreateQueue, benchQueueARN, cache)
	}
}

// The negative-lookup pair: an access key that resolves to no principal at all.
// Uncached, each of these scans the whole iam:users namespace and then misses
// iam:sessions, which is what an unauthorised caller retrying would cost.

func BenchmarkEvaluateIAMDecision_UnknownPrincipal_WarmCache(b *testing.B) {
	st := state.NewMemoryStore()
	seedIAMUserForBench(b, st, "test", benchPolicyDoc)
	req := benchIAMRequest()
	cache := &iamEnforceCache{}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = evaluateIAMDecision(req, st, "no-such-key", benchCreateQueue, benchQueueARN, cache)
	}
}

func BenchmarkEvaluateIAMDecision_UnknownPrincipal_ColdCache(b *testing.B) {
	st := state.NewMemoryStore()
	seedIAMUserForBench(b, st, "test", benchPolicyDoc)
	req := benchIAMRequest()
	cache := &iamEnforceCache{}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		InvalidateIAMEnforceCache()
		_ = evaluateIAMDecision(req, st, "no-such-key", benchCreateQueue, benchQueueARN, cache)
	}
}

func seedIAMUserForBench(b *testing.B, st state.Store, accessKey, userDoc string) {
	b.Helper()
	ctx := context.Background()
	user := map[string]any{
		"UserName":       accessKey,
		"AccessKeys":     []map[string]string{{"AccessKeyId": accessKey}},
		"InlinePolicies": map[string]string{"inline-1": userDoc},
	}
	raw, err := json.Marshal(user)
	if err != nil {
		b.Fatalf("marshal user seed: %v", err)
	}
	if err := st.Set(ctx, "iam:users", accessKey, string(raw)); err != nil {
		b.Fatalf("seed user: %v", err)
	}
}
