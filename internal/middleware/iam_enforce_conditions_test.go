package middleware

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.uber.org/zap"

	"github.com/overcast-sh/overcast/internal/state"
)

// ─── End-to-end middleware tests with Condition blocks ────────────────────────

func TestIAMEnforce_condition_regionAllows(t *testing.T) {
	st := state.NewMemoryStore()
	// Policy allows SQS only in us-east-1.
	seedIAMUserWithPolicies(t, st, "test", []string{
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"sqs:CreateQueue","Resource":"*","Condition":{"StringEquals":{"aws:RequestedRegion":"us-east-1"}}}]}`,
	}, nil)

	called := false
	h := IAMEnforce(true, st, zap.NewNop(), nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusNoContent)
	}))

	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"QueueName":"demo"}`))
	req.Header.Set("Content-Type", "application/x-amz-json-1.0")
	req.Header.Set("X-Amz-Target", "AmazonSQS.CreateQueue")
	// Credential scope has region us-east-1.
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=test/20260423/us-east-1/sqs/aws4_request, SignedHeaders=host;x-amz-date, Signature=abc")
	req.Header.Set("X-Amz-Date", "20260423T000000Z")
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if !called {
		t.Fatal("expected next handler to be called")
	}
	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected %d, got %d", http.StatusNoContent, rec.Code)
	}
}

func TestIAMEnforce_condition_regionBlocks(t *testing.T) {
	st := state.NewMemoryStore()
	// Policy allows SQS only in us-east-1, but request comes from eu-west-1.
	seedIAMUserWithPolicies(t, st, "test", []string{
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"sqs:CreateQueue","Resource":"*","Condition":{"StringEquals":{"aws:RequestedRegion":"us-east-1"}}}]}`,
	}, nil)

	h := IAMEnforce(true, st, zap.NewNop(), nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"QueueName":"demo"}`))
	req.Header.Set("Content-Type", "application/x-amz-json-1.0")
	req.Header.Set("X-Amz-Target", "AmazonSQS.CreateQueue")
	// Credential scope has region eu-west-1.
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=test/20260423/eu-west-1/sqs/aws4_request, SignedHeaders=host;x-amz-date, Signature=abc")
	req.Header.Set("X-Amz-Date", "20260423T000000Z")
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	// Condition not met → Allow statement doesn't apply → NoMatch → deny.
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected %d, got %d", http.StatusBadRequest, rec.Code)
	}
}

func TestIAMEnforce_condition_unknownOperator_denies(t *testing.T) {
	st := state.NewMemoryStore()
	// Policy has an unknown condition operator — must fail closed.
	seedIAMUserWithPolicies(t, st, "test", []string{
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"sqs:CreateQueue","Resource":"*","Condition":{"WeirdUnknownOp":{"aws:RequestedRegion":"us-east-1"}}}]}`,
	}, nil)

	h := IAMEnforce(true, st, zap.NewNop(), nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"QueueName":"demo"}`))
	req.Header.Set("Content-Type", "application/x-amz-json-1.0")
	req.Header.Set("X-Amz-Target", "AmazonSQS.CreateQueue")
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=test/20260423/us-east-1/sqs/aws4_request, SignedHeaders=host;x-amz-date, Signature=abc")
	req.Header.Set("X-Amz-Date", "20260423T000000Z")
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected %d (fail closed), got %d", http.StatusBadRequest, rec.Code)
	}
}

func TestIAMEnforce_condition_denyWithRegion_blocksMatchingRegion(t *testing.T) {
	st := state.NewMemoryStore()
	// Allow everything, then deny SQS in eu-west-1.
	seedIAMUserWithPolicies(t, st, "test", []string{
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"sqs:*","Resource":"*"},{"Effect":"Deny","Action":"sqs:*","Resource":"*","Condition":{"StringEquals":{"aws:RequestedRegion":"eu-west-1"}}}]}`,
	}, nil)

	h := IAMEnforce(true, st, zap.NewNop(), nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"QueueName":"demo"}`))
	req.Header.Set("Content-Type", "application/x-amz-json-1.0")
	req.Header.Set("X-Amz-Target", "AmazonSQS.CreateQueue")
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=test/20260423/eu-west-1/sqs/aws4_request, SignedHeaders=host;x-amz-date, Signature=abc")
	req.Header.Set("X-Amz-Date", "20260423T000000Z")
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected %d, got %d", http.StatusBadRequest, rec.Code)
	}
}

func TestIAMEnforce_condition_denyWithRegion_allowsOtherRegion(t *testing.T) {
	st := state.NewMemoryStore()
	// Allow everything, then deny SQS in eu-west-1 only.
	seedIAMUserWithPolicies(t, st, "test", []string{
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"sqs:*","Resource":"*"},{"Effect":"Deny","Action":"sqs:*","Resource":"*","Condition":{"StringEquals":{"aws:RequestedRegion":"eu-west-1"}}}]}`,
	}, nil)

	called := false
	h := IAMEnforce(true, st, zap.NewNop(), nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusNoContent)
	}))

	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"QueueName":"demo"}`))
	req.Header.Set("Content-Type", "application/x-amz-json-1.0")
	req.Header.Set("X-Amz-Target", "AmazonSQS.CreateQueue")
	// us-east-1 is not blocked by the deny condition.
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=test/20260423/us-east-1/sqs/aws4_request, SignedHeaders=host;x-amz-date, Signature=abc")
	req.Header.Set("X-Amz-Date", "20260423T000000Z")
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if !called {
		t.Fatal("expected next handler to be called")
	}
	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected %d, got %d", http.StatusNoContent, rec.Code)
	}
}

// ─── s3:x-amz-bucket-namespace condition key (issues #1471, #1475) ────────
//
// AWS's published enforcement pattern for account regional namespaces is a
// Deny with StringNotEquals against s3:x-amz-bucket-namespace, which denies
// both an absent header and any value other than "account-regional". The
// first two tests are that pattern's two branches, driven through the same
// PUT-a-bucket-name shape restoperation_test.go pins to CreateBucket.
//
// Until #1475, s3ConditionKeys always populated the key — defaulting an
// absent header to "global" — solely to compensate for the condition
// evaluator treating a missing key as "not matched" for every operator,
// including negated ones. With evaluateConditions fixed to match AWS's
// documented negated-operator-on-missing-key semantics
// (internal/iampolicy/condition.go), the Deny below denies a header-less
// request because the key is genuinely absent and StringNotEquals matches on
// absence — not because a default value stood in for it. The third test
// below pins the divergence that default used to cause: a StringEquals
// against a header-less request must NOT match "global", because on AWS the
// key isn't present in the context at all when the request never carried
// the header.

func denyNonAccountRegionalBucketNamespacePolicy() string {
	return `{"Version":"2012-10-17","Statement":[` +
		`{"Effect":"Allow","Action":"s3:CreateBucket","Resource":"*"},` +
		`{"Effect":"Deny","Action":"s3:CreateBucket","Resource":"*","Condition":{"StringNotEquals":{"s3:x-amz-bucket-namespace":"account-regional"}}}` +
		`]}`
}

func TestIAMEnforce_condition_bucketNamespace_absentHeaderDenied(t *testing.T) {
	st := state.NewMemoryStore()
	seedIAMUserWithPolicies(t, st, "test", []string{denyNonAccountRegionalBucketNamespacePolicy()}, nil)

	h := IAMEnforce(true, st, zap.NewNop(), nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// No X-Amz-Bucket-Namespace header at all, so s3:x-amz-bucket-namespace
	// is genuinely absent from the condition context — StringNotEquals
	// matches on a missing key (AWS's negated-operator rule), so AWS's
	// published pattern denies this.
	req := httptest.NewRequest(http.MethodPut, "/my-bucket", nil)
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=test/20260423/us-east-1/s3/aws4_request, SignedHeaders=host;x-amz-date, Signature=abc")
	req.Header.Set("X-Amz-Date", "20260423T000000Z")
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected %d, got %d", http.StatusForbidden, rec.Code)
	}
}

func TestIAMEnforce_condition_bucketNamespace_accountRegionalAllowed(t *testing.T) {
	st := state.NewMemoryStore()
	seedIAMUserWithPolicies(t, st, "test", []string{denyNonAccountRegionalBucketNamespacePolicy()}, nil)

	called := false
	h := IAMEnforce(true, st, zap.NewNop(), nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodPut, "/amzn-app-000000000000-us-east-1-an", nil)
	req.Header.Set("X-Amz-Bucket-Namespace", "account-regional")
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=test/20260423/us-east-1/s3/aws4_request, SignedHeaders=host;x-amz-date, Signature=abc")
	req.Header.Set("X-Amz-Date", "20260423T000000Z")
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if !called {
		t.Fatal("expected next handler to be called")
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("expected %d, got %d", http.StatusOK, rec.Code)
	}
}

func TestIAMEnforce_condition_bucketNamespace_absentHeader_stringEqualsGlobalDoesNotMatch(t *testing.T) {
	st := state.NewMemoryStore()
	// Allow only when the namespace is explicitly "global" — the value
	// s3ConditionKeys used to default a missing header to.
	seedIAMUserWithPolicies(t, st, "test", []string{
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"s3:CreateBucket","Resource":"*","Condition":{"StringEquals":{"s3:x-amz-bucket-namespace":"global"}}}]}`,
	}, nil)

	h := IAMEnforce(true, st, zap.NewNop(), nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// No X-Amz-Bucket-Namespace header — the key is absent from the context,
	// so StringEquals must not match "global" (positive operators keep
	// "missing key → not matched"). Before #1475 this request was allowed,
	// because the key was always defaulted to "global".
	req := httptest.NewRequest(http.MethodPut, "/my-bucket", nil)
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=test/20260423/us-east-1/s3/aws4_request, SignedHeaders=host;x-amz-date, Signature=abc")
	req.Header.Set("X-Amz-Date", "20260423T000000Z")
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected %d, got %d", http.StatusForbidden, rec.Code)
	}
}

func TestIAMEnforce_condition_principalArn_allowsMatchingUserArn(t *testing.T) {
	st := state.NewMemoryStore()

	user := map[string]any{
		"UserName": "test-user",
		"UserId":   "AIDAEXAMPLE123456",
		"Arn":      "arn:aws:iam::123456789012:user/test-user",
		"AccessKeys": []map[string]string{
			{"AccessKeyId": "test"},
		},
		"InlinePolicies": map[string]string{
			"inline-1": `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"sqs:CreateQueue","Resource":"*","Condition":{"StringEquals":{"aws:PrincipalArn":"arn:aws:iam::123456789012:user/test-user"}}}]}`,
		},
	}
	b, err := json.Marshal(user)
	if err != nil {
		t.Fatalf("marshal user: %v", err)
	}
	if err := st.Set(context.Background(), "iam:users", "test-user", string(b)); err != nil {
		t.Fatalf("seed user: %v", err)
	}

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
		t.Fatalf("expected %d, got %d", http.StatusNoContent, rec.Code)
	}
}

func TestIAMEnforce_condition_principalAccount_blocksMismatch(t *testing.T) {
	st := state.NewMemoryStore()

	user := map[string]any{
		"UserName": "test-user",
		"Arn":      "arn:aws:iam::123456789012:user/test-user",
		"AccessKeys": []map[string]string{
			{"AccessKeyId": "test"},
		},
		"InlinePolicies": map[string]string{
			"inline-1": `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"sqs:CreateQueue","Resource":"*","Condition":{"StringEquals":{"aws:PrincipalAccount":"000000000000"}}}]}`,
		},
	}
	b, err := json.Marshal(user)
	if err != nil {
		t.Fatalf("marshal user: %v", err)
	}
	if err := st.Set(context.Background(), "iam:users", "test-user", string(b)); err != nil {
		t.Fatalf("seed user: %v", err)
	}

	h := IAMEnforce(true, st, zap.NewNop(), nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"QueueName":"demo"}`))
	req.Header.Set("Content-Type", "application/x-amz-json-1.0")
	req.Header.Set("X-Amz-Target", "AmazonSQS.CreateQueue")
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=test/20260423/us-east-1/sqs/aws4_request, SignedHeaders=host;x-amz-date, Signature=abc")
	req.Header.Set("X-Amz-Date", "20260423T000000Z")
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected %d, got %d", http.StatusBadRequest, rec.Code)
	}
}

func TestIAMEnforce_condition_userID_allowsMatchingUserID(t *testing.T) {
	st := state.NewMemoryStore()

	user := map[string]any{
		"UserName": "test-user",
		"UserId":   "AIDAUSERIDMATCH",
		"AccessKeys": []map[string]string{
			{"AccessKeyId": "test"},
		},
		"InlinePolicies": map[string]string{
			"inline-1": `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"sqs:CreateQueue","Resource":"*","Condition":{"StringEquals":{"aws:userid":"AIDAUSERIDMATCH"}}}]}`,
		},
	}
	b, err := json.Marshal(user)
	if err != nil {
		t.Fatalf("marshal user: %v", err)
	}
	if err := st.Set(context.Background(), "iam:users", "test-user", string(b)); err != nil {
		t.Fatalf("seed user: %v", err)
	}

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
		t.Fatalf("expected %d, got %d", http.StatusNoContent, rec.Code)
	}
}

func TestIAMEnforce_condition_currentTime_allowsMatchingSigV4Time(t *testing.T) {
	st := state.NewMemoryStore()
	seedIAMUserWithPolicies(t, st, "test", []string{
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"sqs:CreateQueue","Resource":"*","Condition":{"StringEquals":{"aws:CurrentTime":"2026-04-23T00:00:00Z"}}}]}`,
	}, nil)

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
		t.Fatalf("expected %d, got %d", http.StatusNoContent, rec.Code)
	}
}

func TestIAMEnforce_condition_dateLessThan_allowsEarlierRequestTime(t *testing.T) {
	st := state.NewMemoryStore()
	seedIAMUserWithPolicies(t, st, "test", []string{
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"sqs:CreateQueue","Resource":"*","Condition":{"DateLessThan":{"aws:CurrentTime":"2026-04-24T00:00:00Z"}}}]}`,
	}, nil)

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
		t.Fatalf("expected %d, got %d", http.StatusNoContent, rec.Code)
	}
}

func TestIAMEnforce_condition_dateGreaterThan_deniesWhenNotSatisfied(t *testing.T) {
	st := state.NewMemoryStore()
	seedIAMUserWithPolicies(t, st, "test", []string{
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"sqs:CreateQueue","Resource":"*","Condition":{"DateGreaterThan":{"aws:CurrentTime":"2026-04-24T00:00:00Z"}}}]}`,
	}, nil)

	h := IAMEnforce(true, st, zap.NewNop(), nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"QueueName":"demo"}`))
	req.Header.Set("Content-Type", "application/x-amz-json-1.0")
	req.Header.Set("X-Amz-Target", "AmazonSQS.CreateQueue")
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=test/20260423/us-east-1/sqs/aws4_request, SignedHeaders=host;x-amz-date, Signature=abc")
	req.Header.Set("X-Amz-Date", "20260423T000000Z")
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected %d, got %d", http.StatusBadRequest, rec.Code)
	}
}

func TestIAMEnforce_condition_nullFalse_allowsWhenPrincipalArnPresent(t *testing.T) {
	st := state.NewMemoryStore()
	seedIAMUserWithPolicies(t, st, "test", []string{
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"sqs:CreateQueue","Resource":"*","Condition":{"Null":{"aws:PrincipalArn":"false"}}}]}`,
	}, nil)

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
		t.Fatalf("expected %d, got %d", http.StatusNoContent, rec.Code)
	}
}

func TestIAMEnforce_condition_nullTrue_deniesWhenPrincipalArnPresent(t *testing.T) {
	st := state.NewMemoryStore()
	seedIAMUserWithPolicies(t, st, "test", []string{
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"sqs:CreateQueue","Resource":"*","Condition":{"Null":{"aws:PrincipalArn":"true"}}}]}`,
	}, nil)

	h := IAMEnforce(true, st, zap.NewNop(), nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"QueueName":"demo"}`))
	req.Header.Set("Content-Type", "application/x-amz-json-1.0")
	req.Header.Set("X-Amz-Target", "AmazonSQS.CreateQueue")
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=test/20260423/us-east-1/sqs/aws4_request, SignedHeaders=host;x-amz-date, Signature=abc")
	req.Header.Set("X-Amz-Date", "20260423T000000Z")
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected %d, got %d", http.StatusBadRequest, rec.Code)
	}
}

func TestIAMEnforce_condition_stringEqualsIfExists_allowsWhenKeyMissing(t *testing.T) {
	st := state.NewMemoryStore()
	seedIAMUserWithPolicies(t, st, "test", []string{
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"sqs:CreateQueue","Resource":"*","Condition":{"StringEqualsIfExists":{"aws:PrincipalTag/team":"platform"}}}]}`,
	}, nil)

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
		t.Fatalf("expected %d, got %d", http.StatusNoContent, rec.Code)
	}
}

// ─── Numeric condition operator middleware tests ───────────────────────────────

// TestIAMEnforce_condition_numericLessThan_allowsSmallBody verifies that a
// policy with NumericLessThan on aws:RequestedContentLength allows requests
// whose Content-Length is below the threshold.
func TestIAMEnforce_condition_numericLessThan_allowsSmallBody(t *testing.T) {
	st := state.NewMemoryStore()
	// Allow only if request body is smaller than 1000 bytes.
	seedIAMUserWithPolicies(t, st, "test", []string{
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"sqs:CreateQueue","Resource":"*","Condition":{"NumericLessThan":{"aws:RequestedContentLength":"1000"}}}]}`,
	}, nil)

	called := false
	h := IAMEnforce(true, st, zap.NewNop(), nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusNoContent)
	}))

	body := `{"QueueName":"demo"}`
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.ContentLength = int64(len(body))
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
		t.Fatalf("expected %d, got %d", http.StatusNoContent, rec.Code)
	}
}

// TestIAMEnforce_condition_numericLessThan_deniesLargeBody verifies that the
// same NumericLessThan policy blocks a request whose Content-Length exceeds
// the threshold.
func TestIAMEnforce_condition_numericLessThan_deniesLargeBody(t *testing.T) {
	st := state.NewMemoryStore()
	seedIAMUserWithPolicies(t, st, "test", []string{
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"sqs:CreateQueue","Resource":"*","Condition":{"NumericLessThan":{"aws:RequestedContentLength":"10"}}}]}`,
	}, nil)

	h := IAMEnforce(true, st, zap.NewNop(), nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	body := `{"QueueName":"demo"}` // 20 bytes — exceeds threshold of 10
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.ContentLength = int64(len(body))
	req.Header.Set("Content-Type", "application/x-amz-json-1.0")
	req.Header.Set("X-Amz-Target", "AmazonSQS.CreateQueue")
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=test/20260423/us-east-1/sqs/aws4_request, SignedHeaders=host;x-amz-date, Signature=abc")
	req.Header.Set("X-Amz-Date", "20260423T000000Z")
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected %d, got %d", http.StatusBadRequest, rec.Code)
	}
}

// ─── Policy variable substitution middleware tests ────────────────────────────

// TestIAMEnforce_policyVariable_username_allowsMatchingUser verifies that
// ${aws:username} in a Resource pattern is expanded to the calling user's name
// before matching, allowing requests that match the expanded ARN.
func TestIAMEnforce_policyVariable_username_allowsMatchingUser(t *testing.T) {
	st := state.NewMemoryStore()
	// Policy allows queues whose name starts with the caller's username.
	seedIAMUserWithPolicies(t, st, "alice", []string{
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"sqs:CreateQueue","Resource":"arn:aws:sqs:us-east-1:000000000000:${aws:username}-*"}]}`,
	}, nil)

	called := false
	h := IAMEnforce(true, st, zap.NewNop(), nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusNoContent)
	}))

	// Queue name "alice-myqueue" should match arn:aws:sqs:...:alice-*
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"QueueName":"alice-myqueue"}`))
	req.Header.Set("Content-Type", "application/x-amz-json-1.0")
	req.Header.Set("X-Amz-Target", "AmazonSQS.CreateQueue")
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=alice/20260423/us-east-1/sqs/aws4_request, SignedHeaders=host;x-amz-date, Signature=abc")
	req.Header.Set("X-Amz-Date", "20260423T000000Z")
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if !called {
		t.Fatal("expected next handler to be called")
	}
	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected %d, got %d", http.StatusNoContent, rec.Code)
	}
}

// TestIAMEnforce_policyVariable_username_deniesOtherUser verifies that
// ${aws:username} expansion causes a deny when the resource belongs to a
// different user than the caller.
func TestIAMEnforce_policyVariable_username_deniesOtherUser(t *testing.T) {
	st := state.NewMemoryStore()
	seedIAMUserWithPolicies(t, st, "alice", []string{
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"sqs:CreateQueue","Resource":"arn:aws:sqs:us-east-1:000000000000:${aws:username}-*"}]}`,
	}, nil)

	h := IAMEnforce(true, st, zap.NewNop(), nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	// Queue name "bob-myqueue" should NOT match arn:aws:sqs:...:alice-*
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"QueueName":"bob-myqueue"}`))
	req.Header.Set("Content-Type", "application/x-amz-json-1.0")
	req.Header.Set("X-Amz-Target", "AmazonSQS.CreateQueue")
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=alice/20260423/us-east-1/sqs/aws4_request, SignedHeaders=host;x-amz-date, Signature=abc")
	req.Header.Set("X-Amz-Date", "20260423T000000Z")
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected %d, got %d", http.StatusBadRequest, rec.Code)
	}
}
