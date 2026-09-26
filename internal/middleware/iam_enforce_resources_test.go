package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.uber.org/zap"

	"github.com/overcast-sh/overcast/internal/state"
)

func TestIAMEnforce_enabled_s3ResourcePolicyAllowsMatchingObject(t *testing.T) {
	st := state.NewMemoryStore()
	seedIAMUserWithPolicies(t, st, "test", []string{`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"s3:GetObject","Resource":"arn:aws:s3:::demo/*"}]}`}, nil)

	called := false
	h := IAMEnforce(true, st, zap.NewNop(), nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusNoContent)
	}))

	req := httptest.NewRequest(http.MethodGet, "/demo/path/object.txt", nil)
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=test/20260423/us-east-1/s3/aws4_request, SignedHeaders=host;x-amz-date, Signature=abc")
	req.Header.Set("X-Amz-Date", "20260423T000000Z")
	if got := classifyIAM(req).action; got != "s3:GetObject" {
		t.Fatalf("expected inferred action s3:GetObject, got %q", got)
	}
	if got := requestIAMResource(req, classifyIAM(req)); got != "arn:aws:s3:::demo/path/object.txt" {
		t.Fatalf("unexpected inferred resource: %q", got)
	}
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if !called {
		t.Fatal("expected next handler to be called")
	}
	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected status %d, got %d", http.StatusNoContent, rec.Code)
	}
}

func TestIAMEnforce_enabled_s3ResourcePolicyDeniesNonMatchingObject(t *testing.T) {
	st := state.NewMemoryStore()
	seedIAMUserWithPolicies(t, st, "test", []string{`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"s3:GetObject","Resource":"arn:aws:s3:::demo/allowed/*"}]}`}, nil)

	h := IAMEnforce(true, st, zap.NewNop(), nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	req := httptest.NewRequest(http.MethodGet, "/demo/other/object.txt", nil)
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=test/20260423/us-east-1/s3/aws4_request, SignedHeaders=host;x-amz-date, Signature=abc")
	req.Header.Set("X-Amz-Date", "20260423T000000Z")
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected status %d, got %d", http.StatusForbidden, rec.Code)
	}
}

func TestIAMEnforce_enabled_sqsResourcePolicyAllowsMatchingQueueFromCreateQueue(t *testing.T) {
	st := state.NewMemoryStore()
	seedIAMUserWithPolicies(t, st, "test", []string{`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"sqs:CreateQueue","Resource":"arn:aws:sqs:us-east-1:000000000000:allowed-queue"}]}`}, nil)

	called := false
	h := IAMEnforce(true, st, zap.NewNop(), nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusNoContent)
	}))

	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"QueueName":"allowed-queue"}`))
	req.Header.Set("Content-Type", "application/x-amz-json-1.0")
	req.Header.Set("X-Amz-Target", "AmazonSQS.CreateQueue")
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=test/20260423/us-east-1/sqs/aws4_request, SignedHeaders=host;x-amz-date, Signature=abc")
	req.Header.Set("X-Amz-Date", "20260423T000000Z")
	if got := requestIAMResource(req, classifyIAM(req)); got != "arn:aws:sqs:us-east-1:000000000000:allowed-queue" {
		t.Fatalf("unexpected inferred resource: %q", got)
	}
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if !called {
		t.Fatal("expected next handler to be called")
	}
	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected status %d, got %d", http.StatusNoContent, rec.Code)
	}
}

func TestIAMEnforce_enabled_sqsResourcePolicyDeniesNonMatchingQueueFromQueueURL(t *testing.T) {
	st := state.NewMemoryStore()
	seedIAMUserWithPolicies(t, st, "test", []string{`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"sqs:DeleteQueue","Resource":"arn:aws:sqs:us-east-1:000000000000:allowed-queue"}]}`}, nil)

	h := IAMEnforce(true, st, zap.NewNop(), nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"QueueUrl":"https://localhost/000000000000/other-queue"}`))
	req.Header.Set("Content-Type", "application/x-amz-json-1.0")
	req.Header.Set("X-Amz-Target", "AmazonSQS.DeleteQueue")
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=test/20260423/us-east-1/sqs/aws4_request, SignedHeaders=host;x-amz-date, Signature=abc")
	req.Header.Set("X-Amz-Date", "20260423T000000Z")
	if got := requestIAMResource(req, classifyIAM(req)); got != "arn:aws:sqs:us-east-1:000000000000:other-queue" {
		t.Fatalf("unexpected inferred resource: %q", got)
	}
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected status %d, got %d", http.StatusForbidden, rec.Code)
	}
}

func TestIAMEnforce_enabled_snsResourcePolicyAllowsMatchingCreateTopicName(t *testing.T) {
	st := state.NewMemoryStore()
	seedIAMUserWithPolicies(t, st, "test", []string{`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"sns:CreateTopic","Resource":"arn:aws:sns:us-east-1:000000000000:allowed-topic"}]}`}, nil)

	called := false
	h := IAMEnforce(true, st, zap.NewNop(), nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusNoContent)
	}))

	body := "Action=CreateTopic&Version=2010-03-31&Name=allowed-topic"
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=test/20260423/us-east-1/sns/aws4_request, SignedHeaders=host;x-amz-date, Signature=abc")
	req.Header.Set("X-Amz-Date", "20260423T000000Z")
	if got := requestIAMResource(req, classifyIAM(req)); got != "arn:aws:sns:us-east-1:000000000000:allowed-topic" {
		t.Fatalf("unexpected inferred resource: %q", got)
	}
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if !called {
		t.Fatal("expected next handler to be called")
	}
	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected status %d, got %d", http.StatusNoContent, rec.Code)
	}
}

func TestIAMEnforce_enabled_snsResourcePolicyDeniesNonMatchingCreateTopicName(t *testing.T) {
	st := state.NewMemoryStore()
	seedIAMUserWithPolicies(t, st, "test", []string{`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"sns:CreateTopic","Resource":"arn:aws:sns:us-east-1:000000000000:allowed-topic"}]}`}, nil)

	h := IAMEnforce(true, st, zap.NewNop(), nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	body := "Action=CreateTopic&Version=2010-03-31&Name=other-topic"
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=test/20260423/us-east-1/sns/aws4_request, SignedHeaders=host;x-amz-date, Signature=abc")
	req.Header.Set("X-Amz-Date", "20260423T000000Z")
	if got := requestIAMResource(req, classifyIAM(req)); got != "arn:aws:sns:us-east-1:000000000000:other-topic" {
		t.Fatalf("unexpected inferred resource: %q", got)
	}
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected status %d, got %d", http.StatusForbidden, rec.Code)
	}
}

func TestIAMEnforce_enabled_dynamodbResourcePolicyAllowsMatchingTable(t *testing.T) {
	st := state.NewMemoryStore()
	seedIAMUserWithPolicies(t, st, "test", []string{`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"dynamodb:PutItem","Resource":"arn:aws:dynamodb:us-east-1:000000000000:table/allowed-table"}]}`}, nil)

	called := false
	h := IAMEnforce(true, st, zap.NewNop(), nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusNoContent)
	}))

	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"TableName":"allowed-table","Item":{"id":{"S":"1"}}}`))
	req.Header.Set("Content-Type", "application/x-amz-json-1.0")
	req.Header.Set("X-Amz-Target", "DynamoDB_20120810.PutItem")
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=test/20260423/us-east-1/dynamodb/aws4_request, SignedHeaders=host;x-amz-date, Signature=abc")
	req.Header.Set("X-Amz-Date", "20260423T000000Z")
	if got := requestIAMResource(req, classifyIAM(req)); got != "arn:aws:dynamodb:us-east-1:000000000000:table/allowed-table" {
		t.Fatalf("unexpected inferred resource: %q", got)
	}
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if !called {
		t.Fatal("expected next handler to be called")
	}
	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected status %d, got %d", http.StatusNoContent, rec.Code)
	}
}

func TestIAMEnforce_enabled_dynamodbResourcePolicyDeniesNonMatchingTable(t *testing.T) {
	st := state.NewMemoryStore()
	seedIAMUserWithPolicies(t, st, "test", []string{`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"dynamodb:PutItem","Resource":"arn:aws:dynamodb:us-east-1:000000000000:table/allowed-table"}]}`}, nil)

	h := IAMEnforce(true, st, zap.NewNop(), nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"TableName":"other-table","Item":{"id":{"S":"1"}}}`))
	req.Header.Set("Content-Type", "application/x-amz-json-1.0")
	req.Header.Set("X-Amz-Target", "DynamoDB_20120810.PutItem")
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=test/20260423/us-east-1/dynamodb/aws4_request, SignedHeaders=host;x-amz-date, Signature=abc")
	req.Header.Set("X-Amz-Date", "20260423T000000Z")
	if got := requestIAMResource(req, classifyIAM(req)); got != "arn:aws:dynamodb:us-east-1:000000000000:table/other-table" {
		t.Fatalf("unexpected inferred resource: %q", got)
	}
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected status %d, got %d", http.StatusForbidden, rec.Code)
	}
}

func TestIAMEnforce_enabled_ssmResourcePolicyAllowsMatchingParameter(t *testing.T) {
	st := state.NewMemoryStore()
	seedIAMUserWithPolicies(t, st, "test", []string{`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"ssm:GetParameter","Resource":"arn:aws:ssm:us-east-1:000000000000:parameter/app/db/password"}]}`}, nil)

	called := false
	h := IAMEnforce(true, st, zap.NewNop(), nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusNoContent)
	}))

	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"Name":"/app/db/password"}`))
	req.Header.Set("Content-Type", "application/x-amz-json-1.1")
	req.Header.Set("X-Amz-Target", "AmazonSSM.GetParameter")
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=test/20260423/us-east-1/ssm/aws4_request, SignedHeaders=host;x-amz-date, Signature=abc")
	req.Header.Set("X-Amz-Date", "20260423T000000Z")
	if got := requestIAMResource(req, classifyIAM(req)); got != "arn:aws:ssm:us-east-1:000000000000:parameter/app/db/password" {
		t.Fatalf("unexpected inferred resource: %q", got)
	}
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if !called {
		t.Fatal("expected next handler to be called")
	}
	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected status %d, got %d", http.StatusNoContent, rec.Code)
	}
}

func TestIAMEnforce_enabled_ssmResourcePolicyDeniesNonMatchingParameter(t *testing.T) {
	st := state.NewMemoryStore()
	seedIAMUserWithPolicies(t, st, "test", []string{`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"ssm:GetParameter","Resource":"arn:aws:ssm:us-east-1:000000000000:parameter/app/db/password"}]}`}, nil)

	h := IAMEnforce(true, st, zap.NewNop(), nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"Name":"/app/db/other"}`))
	req.Header.Set("Content-Type", "application/x-amz-json-1.1")
	req.Header.Set("X-Amz-Target", "AmazonSSM.GetParameter")
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=test/20260423/us-east-1/ssm/aws4_request, SignedHeaders=host;x-amz-date, Signature=abc")
	req.Header.Set("X-Amz-Date", "20260423T000000Z")
	if got := requestIAMResource(req, classifyIAM(req)); got != "arn:aws:ssm:us-east-1:000000000000:parameter/app/db/other" {
		t.Fatalf("unexpected inferred resource: %q", got)
	}
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected status %d, got %d", http.StatusForbidden, rec.Code)
	}
}

func TestIAMEnforce_enabled_kmsResourcePolicyAllowsMatchingKeyID(t *testing.T) {
	st := state.NewMemoryStore()
	seedIAMUserWithPolicies(t, st, "test", []string{`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"kms:Encrypt","Resource":"arn:aws:kms:us-east-1:000000000000:key/1234abcd"}]}`}, nil)

	called := false
	h := IAMEnforce(true, st, zap.NewNop(), nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusNoContent)
	}))

	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"KeyId":"1234abcd","Plaintext":"AQ=="}`))
	req.Header.Set("Content-Type", "application/x-amz-json-1.1")
	req.Header.Set("X-Amz-Target", "TrentService.Encrypt")
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=test/20260423/us-east-1/kms/aws4_request, SignedHeaders=host;x-amz-date, Signature=abc")
	req.Header.Set("X-Amz-Date", "20260423T000000Z")
	if got := requestIAMResource(req, classifyIAM(req)); got != "arn:aws:kms:us-east-1:000000000000:key/1234abcd" {
		t.Fatalf("unexpected inferred resource: %q", got)
	}
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if !called {
		t.Fatal("expected next handler to be called")
	}
	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected status %d, got %d", http.StatusNoContent, rec.Code)
	}
}

func TestIAMEnforce_enabled_kmsResourcePolicyDeniesNonMatchingAlias(t *testing.T) {
	st := state.NewMemoryStore()
	seedIAMUserWithPolicies(t, st, "test", []string{`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"kms:Encrypt","Resource":"arn:aws:kms:us-east-1:000000000000:alias/app-key"}]}`}, nil)

	h := IAMEnforce(true, st, zap.NewNop(), nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"KeyId":"alias/other-key","Plaintext":"AQ=="}`))
	req.Header.Set("Content-Type", "application/x-amz-json-1.1")
	req.Header.Set("X-Amz-Target", "TrentService.Encrypt")
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=test/20260423/us-east-1/kms/aws4_request, SignedHeaders=host;x-amz-date, Signature=abc")
	req.Header.Set("X-Amz-Date", "20260423T000000Z")
	if got := requestIAMResource(req, classifyIAM(req)); got != "arn:aws:kms:us-east-1:000000000000:alias/other-key" {
		t.Fatalf("unexpected inferred resource: %q", got)
	}
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected status %d, got %d", http.StatusForbidden, rec.Code)
	}
}

func TestIAMEnforce_enabled_kinesisResourcePolicyAllowsMatchingStreamName(t *testing.T) {
	st := state.NewMemoryStore()
	seedIAMUserWithPolicies(t, st, "test", []string{`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"kinesis:DescribeStream","Resource":"arn:aws:kinesis:us-east-1:000000000000:stream/allowed-stream"}]}`}, nil)

	called := false
	h := IAMEnforce(true, st, zap.NewNop(), nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusNoContent)
	}))

	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"StreamName":"allowed-stream"}`))
	req.Header.Set("Content-Type", "application/x-amz-json-1.1")
	req.Header.Set("X-Amz-Target", "Kinesis_20131202.DescribeStream")
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=test/20260423/us-east-1/kinesis/aws4_request, SignedHeaders=host;x-amz-date, Signature=abc")
	req.Header.Set("X-Amz-Date", "20260423T000000Z")
	if got := requestIAMResource(req, classifyIAM(req)); got != "arn:aws:kinesis:us-east-1:000000000000:stream/allowed-stream" {
		t.Fatalf("unexpected inferred resource: %q", got)
	}
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if !called {
		t.Fatal("expected next handler to be called")
	}
	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected status %d, got %d", http.StatusNoContent, rec.Code)
	}
}

func TestIAMEnforce_enabled_kinesisResourcePolicyDeniesNonMatchingStreamARN(t *testing.T) {
	st := state.NewMemoryStore()
	seedIAMUserWithPolicies(t, st, "test", []string{`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"kinesis:DescribeStream","Resource":"arn:aws:kinesis:us-east-1:000000000000:stream/allowed-stream"}]}`}, nil)

	h := IAMEnforce(true, st, zap.NewNop(), nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"StreamARN":"arn:aws:kinesis:us-east-1:000000000000:stream/other-stream"}`))
	req.Header.Set("Content-Type", "application/x-amz-json-1.1")
	req.Header.Set("X-Amz-Target", "Kinesis_20131202.DescribeStream")
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=test/20260423/us-east-1/kinesis/aws4_request, SignedHeaders=host;x-amz-date, Signature=abc")
	req.Header.Set("X-Amz-Date", "20260423T000000Z")
	if got := requestIAMResource(req, classifyIAM(req)); got != "arn:aws:kinesis:us-east-1:000000000000:stream/other-stream" {
		t.Fatalf("unexpected inferred resource: %q", got)
	}
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected status %d, got %d", http.StatusForbidden, rec.Code)
	}
}

func TestIAMEnforce_enabled_firehoseResourcePolicyAllowsMatchingDeliveryStreamName(t *testing.T) {
	st := state.NewMemoryStore()
	seedIAMUserWithPolicies(t, st, "test", []string{`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"firehose:DescribeDeliveryStream","Resource":"arn:aws:firehose:us-east-1:000000000000:deliverystream/allowed-stream"}]}`}, nil)

	called := false
	h := IAMEnforce(true, st, zap.NewNop(), nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusNoContent)
	}))

	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"DeliveryStreamName":"allowed-stream"}`))
	req.Header.Set("Content-Type", "application/x-amz-json-1.1")
	req.Header.Set("X-Amz-Target", "Firehose_20150804.DescribeDeliveryStream")
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=test/20260423/us-east-1/firehose/aws4_request, SignedHeaders=host;x-amz-date, Signature=abc")
	req.Header.Set("X-Amz-Date", "20260423T000000Z")
	if got := requestIAMResource(req, classifyIAM(req)); got != "arn:aws:firehose:us-east-1:000000000000:deliverystream/allowed-stream" {
		t.Fatalf("unexpected inferred resource: %q", got)
	}
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if !called {
		t.Fatal("expected next handler to be called")
	}
	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected status %d, got %d", http.StatusNoContent, rec.Code)
	}
}

func TestIAMEnforce_enabled_firehoseResourcePolicyDeniesNonMatchingDeliveryStreamARN(t *testing.T) {
	st := state.NewMemoryStore()
	seedIAMUserWithPolicies(t, st, "test", []string{`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"firehose:DescribeDeliveryStream","Resource":"arn:aws:firehose:us-east-1:000000000000:deliverystream/allowed-stream"}]}`}, nil)

	h := IAMEnforce(true, st, zap.NewNop(), nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"DeliveryStreamARN":"arn:aws:firehose:us-east-1:000000000000:deliverystream/other-stream"}`))
	req.Header.Set("Content-Type", "application/x-amz-json-1.1")
	req.Header.Set("X-Amz-Target", "Firehose_20150804.DescribeDeliveryStream")
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=test/20260423/us-east-1/firehose/aws4_request, SignedHeaders=host;x-amz-date, Signature=abc")
	req.Header.Set("X-Amz-Date", "20260423T000000Z")
	if got := requestIAMResource(req, classifyIAM(req)); got != "arn:aws:firehose:us-east-1:000000000000:deliverystream/other-stream" {
		t.Fatalf("unexpected inferred resource: %q", got)
	}
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected status %d, got %d", http.StatusForbidden, rec.Code)
	}
}

func TestIAMEnforce_enabled_logsResourcePolicyAllowsMatchingLogStream(t *testing.T) {
	st := state.NewMemoryStore()
	seedIAMUserWithPolicies(t, st, "test", []string{`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"logs:PutLogEvents","Resource":"arn:aws:logs:us-east-1:000000000000:log-group:allowed-group:log-stream:allowed-stream"}]}`}, nil)

	called := false
	h := IAMEnforce(true, st, zap.NewNop(), nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusNoContent)
	}))

	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"logGroupName":"allowed-group","logStreamName":"allowed-stream","logEvents":[]}`))
	req.Header.Set("Content-Type", "application/x-amz-json-1.1")
	req.Header.Set("X-Amz-Target", "Logs_20140328.PutLogEvents")
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=test/20260423/us-east-1/logs/aws4_request, SignedHeaders=host;x-amz-date, Signature=abc")
	req.Header.Set("X-Amz-Date", "20260423T000000Z")
	if got := requestIAMResource(req, classifyIAM(req)); got != "arn:aws:logs:us-east-1:000000000000:log-group:allowed-group:log-stream:allowed-stream" {
		t.Fatalf("unexpected inferred resource: %q", got)
	}
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if !called {
		t.Fatal("expected next handler to be called")
	}
	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected status %d, got %d", http.StatusNoContent, rec.Code)
	}
}

func TestIAMEnforce_enabled_logsResourcePolicyDeniesNonMatchingLogGroup(t *testing.T) {
	st := state.NewMemoryStore()
	seedIAMUserWithPolicies(t, st, "test", []string{`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"logs:CreateLogStream","Resource":"arn:aws:logs:us-east-1:000000000000:log-group:allowed-group:*"}]}`}, nil)

	h := IAMEnforce(true, st, zap.NewNop(), nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"logGroupName":"other-group","logStreamName":"demo"}`))
	req.Header.Set("Content-Type", "application/x-amz-json-1.1")
	req.Header.Set("X-Amz-Target", "Logs_20140328.CreateLogStream")
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=test/20260423/us-east-1/logs/aws4_request, SignedHeaders=host;x-amz-date, Signature=abc")
	req.Header.Set("X-Amz-Date", "20260423T000000Z")
	if got := requestIAMResource(req, classifyIAM(req)); got != "arn:aws:logs:us-east-1:000000000000:log-group:other-group:log-stream:demo" {
		t.Fatalf("unexpected inferred resource: %q", got)
	}
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected status %d, got %d", http.StatusForbidden, rec.Code)
	}
}

func TestIAMEnforce_enabled_ecrResourcePolicyAllowsMatchingRepositoryName(t *testing.T) {
	st := state.NewMemoryStore()
	seedIAMUserWithPolicies(t, st, "test", []string{`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"ecr:CreateRepository","Resource":"arn:aws:ecr:us-east-1:000000000000:repository/allowed-repo"}]}`}, nil)

	called := false
	h := IAMEnforce(true, st, zap.NewNop(), nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusNoContent)
	}))

	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"repositoryName":"allowed-repo"}`))
	req.Header.Set("Content-Type", "application/x-amz-json-1.1")
	req.Header.Set("X-Amz-Target", "AmazonEC2ContainerRegistry_V20150921.CreateRepository")
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=test/20260423/us-east-1/ecr/aws4_request, SignedHeaders=host;x-amz-date, Signature=abc")
	req.Header.Set("X-Amz-Date", "20260423T000000Z")
	if got := requestIAMResource(req, classifyIAM(req)); got != "arn:aws:ecr:us-east-1:000000000000:repository/allowed-repo" {
		t.Fatalf("unexpected inferred resource: %q", got)
	}
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if !called {
		t.Fatal("expected next handler to be called")
	}
	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected status %d, got %d", http.StatusNoContent, rec.Code)
	}
}

func TestIAMEnforce_enabled_ecrResourcePolicyDeniesNonMatchingResourceArn(t *testing.T) {
	st := state.NewMemoryStore()
	seedIAMUserWithPolicies(t, st, "test", []string{`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"ecr:TagResource","Resource":"arn:aws:ecr:us-east-1:000000000000:repository/allowed-repo"}]}`}, nil)

	h := IAMEnforce(true, st, zap.NewNop(), nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"resourceArn":"arn:aws:ecr:us-east-1:000000000000:repository/other-repo","tags":[{"Key":"env","Value":"dev"}]}`))
	req.Header.Set("Content-Type", "application/x-amz-json-1.1")
	req.Header.Set("X-Amz-Target", "AmazonEC2ContainerRegistry_V20150921.TagResource")
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=test/20260423/us-east-1/ecr/aws4_request, SignedHeaders=host;x-amz-date, Signature=abc")
	req.Header.Set("X-Amz-Date", "20260423T000000Z")
	if got := requestIAMResource(req, classifyIAM(req)); got != "arn:aws:ecr:us-east-1:000000000000:repository/other-repo" {
		t.Fatalf("unexpected inferred resource: %q", got)
	}
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected status %d, got %d", http.StatusForbidden, rec.Code)
	}
}

func TestIAMEnforce_enabled_secretsManagerResourcePolicyAllowsMatchingName(t *testing.T) {
	st := state.NewMemoryStore()
	seedIAMUserWithPolicies(t, st, "test", []string{`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"secretsmanager:CreateSecret","Resource":"arn:aws:secretsmanager:us-east-1:000000000000:secret:allowed-secret"}]}`}, nil)

	called := false
	h := IAMEnforce(true, st, zap.NewNop(), nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusNoContent)
	}))

	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"Name":"allowed-secret","SecretString":"s3cr3t"}`))
	req.Header.Set("Content-Type", "application/x-amz-json-1.1")
	req.Header.Set("X-Amz-Target", "secretsmanager.CreateSecret")
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=test/20260423/us-east-1/secretsmanager/aws4_request, SignedHeaders=host;x-amz-date, Signature=abc")
	req.Header.Set("X-Amz-Date", "20260423T000000Z")
	if got := requestIAMResource(req, classifyIAM(req)); got != "arn:aws:secretsmanager:us-east-1:000000000000:secret:allowed-secret" {
		t.Fatalf("unexpected inferred resource: %q", got)
	}
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if !called {
		t.Fatal("expected next handler to be called")
	}
	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected status %d, got %d", http.StatusNoContent, rec.Code)
	}
}

func TestIAMEnforce_enabled_secretsManagerResourcePolicyDeniesNonMatchingSecretID(t *testing.T) {
	st := state.NewMemoryStore()
	seedIAMUserWithPolicies(t, st, "test", []string{`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"secretsmanager:GetSecretValue","Resource":"arn:aws:secretsmanager:us-east-1:000000000000:secret:allowed-secret"}]}`}, nil)

	h := IAMEnforce(true, st, zap.NewNop(), nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"SecretId":"other-secret"}`))
	req.Header.Set("Content-Type", "application/x-amz-json-1.1")
	req.Header.Set("X-Amz-Target", "secretsmanager.GetSecretValue")
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=test/20260423/us-east-1/secretsmanager/aws4_request, SignedHeaders=host;x-amz-date, Signature=abc")
	req.Header.Set("X-Amz-Date", "20260423T000000Z")
	if got := requestIAMResource(req, classifyIAM(req)); got != "arn:aws:secretsmanager:us-east-1:000000000000:secret:other-secret" {
		t.Fatalf("unexpected inferred resource: %q", got)
	}
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected status %d, got %d", http.StatusForbidden, rec.Code)
	}
}

func TestIAMEnforce_enabled_stepFunctionsResourcePolicyAllowsMatchingName(t *testing.T) {
	st := state.NewMemoryStore()
	seedIAMUserWithPolicies(t, st, "test", []string{`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"states:CreateStateMachine","Resource":"arn:aws:states:us-east-1:000000000000:stateMachine:allowed-sm"}]}`}, nil)

	called := false
	h := IAMEnforce(true, st, zap.NewNop(), nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusNoContent)
	}))

	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"name":"allowed-sm","definition":"{}","roleArn":"arn:aws:iam::000000000000:role/demo"}`))
	req.Header.Set("Content-Type", "application/x-amz-json-1.1")
	req.Header.Set("X-Amz-Target", "AWSStepFunctions.CreateStateMachine")
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=test/20260423/us-east-1/stepfunctions/aws4_request, SignedHeaders=host;x-amz-date, Signature=abc")
	req.Header.Set("X-Amz-Date", "20260423T000000Z")
	if got := requestIAMResource(req, classifyIAM(req)); got != "arn:aws:states:us-east-1:000000000000:stateMachine:allowed-sm" {
		t.Fatalf("unexpected inferred resource: %q", got)
	}
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if !called {
		t.Fatal("expected next handler to be called")
	}
	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected status %d, got %d", http.StatusNoContent, rec.Code)
	}
}

func TestIAMEnforce_enabled_stepFunctionsResourcePolicyDeniesNonMatchingStateMachineArn(t *testing.T) {
	st := state.NewMemoryStore()
	seedIAMUserWithPolicies(t, st, "test", []string{`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"states:StartExecution","Resource":"arn:aws:states:us-east-1:000000000000:stateMachine:allowed-sm"}]}`}, nil)

	h := IAMEnforce(true, st, zap.NewNop(), nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"stateMachineArn":"arn:aws:states:us-east-1:000000000000:stateMachine:other-sm","input":"{}"}`))
	req.Header.Set("Content-Type", "application/x-amz-json-1.1")
	req.Header.Set("X-Amz-Target", "AWSStepFunctions.StartExecution")
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=test/20260423/us-east-1/stepfunctions/aws4_request, SignedHeaders=host;x-amz-date, Signature=abc")
	req.Header.Set("X-Amz-Date", "20260423T000000Z")
	if got := requestIAMResource(req, classifyIAM(req)); got != "arn:aws:states:us-east-1:000000000000:stateMachine:other-sm" {
		t.Fatalf("unexpected inferred resource: %q", got)
	}
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected status %d, got %d", http.StatusForbidden, rec.Code)
	}
}

func TestIAMEnforce_enabled_cloudFormationResourcePolicyAllowsMatchingStackName(t *testing.T) {
	st := state.NewMemoryStore()
	seedIAMUserWithPolicies(t, st, "test", []string{`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"cloudformation:CreateStack","Resource":"arn:aws:cloudformation:us-east-1:000000000000:stack/allowed-stack/*"}]}`}, nil)

	called := false
	h := IAMEnforce(true, st, zap.NewNop(), nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusNoContent)
	}))

	body := "Action=CreateStack&Version=2010-05-15&StackName=allowed-stack"
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=test/20260423/us-east-1/cloudformation/aws4_request, SignedHeaders=host;x-amz-date, Signature=abc")
	req.Header.Set("X-Amz-Date", "20260423T000000Z")
	if got := requestIAMResource(req, classifyIAM(req)); got != "arn:aws:cloudformation:us-east-1:000000000000:stack/allowed-stack/*" {
		t.Fatalf("unexpected inferred resource: %q", got)
	}
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if !called {
		t.Fatal("expected next handler to be called")
	}
	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected status %d, got %d", http.StatusNoContent, rec.Code)
	}
}

func TestIAMEnforce_enabled_cloudFormationResourcePolicyDeniesNonMatchingStackName(t *testing.T) {
	st := state.NewMemoryStore()
	seedIAMUserWithPolicies(t, st, "test", []string{`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"cloudformation:CreateStack","Resource":"arn:aws:cloudformation:us-east-1:000000000000:stack/allowed-stack/*"}]}`}, nil)

	h := IAMEnforce(true, st, zap.NewNop(), nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	body := "Action=CreateStack&Version=2010-05-15&StackName=other-stack"
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=test/20260423/us-east-1/cloudformation/aws4_request, SignedHeaders=host;x-amz-date, Signature=abc")
	req.Header.Set("X-Amz-Date", "20260423T000000Z")
	if got := requestIAMResource(req, classifyIAM(req)); got != "arn:aws:cloudformation:us-east-1:000000000000:stack/other-stack/*" {
		t.Fatalf("unexpected inferred resource: %q", got)
	}
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected status %d, got %d", http.StatusForbidden, rec.Code)
	}
}

func TestIAMEnforce_enabled_ecsResourcePolicyAllowsMatchingCluster(t *testing.T) {
	st := state.NewMemoryStore()
	seedIAMUserWithPolicies(t, st, "test", []string{`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"ecs:DeleteCluster","Resource":"arn:aws:ecs:us-east-1:000000000000:cluster/allowed-cluster"}]}`}, nil)

	called := false
	h := IAMEnforce(true, st, zap.NewNop(), nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusNoContent)
	}))

	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"cluster":"allowed-cluster"}`))
	req.Header.Set("Content-Type", "application/x-amz-json-1.1")
	req.Header.Set("X-Amz-Target", "AmazonEC2ContainerServiceV20141113.DeleteCluster")
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=test/20260423/us-east-1/ecs/aws4_request, SignedHeaders=host;x-amz-date, Signature=abc")
	req.Header.Set("X-Amz-Date", "20260423T000000Z")
	if got := requestIAMResource(req, classifyIAM(req)); got != "arn:aws:ecs:us-east-1:000000000000:cluster/allowed-cluster" {
		t.Fatalf("unexpected inferred resource: %q", got)
	}
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if !called {
		t.Fatal("expected next handler to be called")
	}
	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected status %d, got %d", http.StatusNoContent, rec.Code)
	}
}

func TestIAMEnforce_enabled_ecsResourcePolicyDeniesNonMatchingCluster(t *testing.T) {
	st := state.NewMemoryStore()
	seedIAMUserWithPolicies(t, st, "test", []string{`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"ecs:DeleteCluster","Resource":"arn:aws:ecs:us-east-1:000000000000:cluster/allowed-cluster"}]}`}, nil)

	h := IAMEnforce(true, st, zap.NewNop(), nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"cluster":"other-cluster"}`))
	req.Header.Set("Content-Type", "application/x-amz-json-1.1")
	req.Header.Set("X-Amz-Target", "AmazonEC2ContainerServiceV20141113.DeleteCluster")
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=test/20260423/us-east-1/ecs/aws4_request, SignedHeaders=host;x-amz-date, Signature=abc")
	req.Header.Set("X-Amz-Date", "20260423T000000Z")
	if got := requestIAMResource(req, classifyIAM(req)); got != "arn:aws:ecs:us-east-1:000000000000:cluster/other-cluster" {
		t.Fatalf("unexpected inferred resource: %q", got)
	}
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected status %d, got %d", http.StatusForbidden, rec.Code)
	}
}

func TestIAMEnforce_enabled_lambdaResourcePolicyAllowsMatchingCreateFunctionName(t *testing.T) {
	st := state.NewMemoryStore()
	seedIAMUserWithPolicies(t, st, "test", []string{`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"lambda:CreateFunction","Resource":"arn:aws:lambda:us-east-1:000000000000:function:allowed-fn"}]}`}, nil)

	called := false
	h := IAMEnforce(true, st, zap.NewNop(), nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusNoContent)
	}))

	req := httptest.NewRequest(http.MethodPost, "/2015-03-31/functions", strings.NewReader(`{"FunctionName":"allowed-fn"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=test/20260423/us-east-1/lambda/aws4_request, SignedHeaders=host;x-amz-date, Signature=abc")
	req.Header.Set("X-Amz-Date", "20260423T000000Z")
	if got := classifyIAM(req).action; got != "lambda:CreateFunction" {
		t.Fatalf("unexpected inferred action: %q", got)
	}
	if got := requestIAMResource(req, classifyIAM(req)); got != "arn:aws:lambda:us-east-1:000000000000:function:allowed-fn" {
		t.Fatalf("unexpected inferred resource: %q", got)
	}
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if !called {
		t.Fatal("expected next handler to be called")
	}
	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected status %d, got %d", http.StatusNoContent, rec.Code)
	}
}

func TestIAMEnforce_enabled_lambdaResourcePolicyDeniesNonMatchingInvokePath(t *testing.T) {
	st := state.NewMemoryStore()
	seedIAMUserWithPolicies(t, st, "test", []string{`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"lambda:InvokeFunction","Resource":"arn:aws:lambda:us-east-1:000000000000:function:allowed-fn"}]}`}, nil)

	h := IAMEnforce(true, st, zap.NewNop(), nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	req := httptest.NewRequest(http.MethodPost, "/2015-03-31/functions/other-fn/invocations", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=test/20260423/us-east-1/lambda/aws4_request, SignedHeaders=host;x-amz-date, Signature=abc")
	req.Header.Set("X-Amz-Date", "20260423T000000Z")
	if got := classifyIAM(req).action; got != "lambda:InvokeFunction" {
		t.Fatalf("unexpected inferred action: %q", got)
	}
	if got := requestIAMResource(req, classifyIAM(req)); got != "arn:aws:lambda:us-east-1:000000000000:function:other-fn" {
		t.Fatalf("unexpected inferred resource: %q", got)
	}
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected status %d, got %d", http.StatusForbidden, rec.Code)
	}
}

func TestIAMEnforce_enabled_cloudWatchResourcePolicyAllowsMatchingAlarmName(t *testing.T) {
	st := state.NewMemoryStore()
	seedIAMUserWithPolicies(t, st, "test", []string{`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"cloudwatch:PutMetricAlarm","Resource":"arn:aws:cloudwatch:us-east-1:000000000000:alarm:allowed-alarm"}]}`}, nil)

	called := false
	h := IAMEnforce(true, st, zap.NewNop(), nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusNoContent)
	}))

	body := "Action=PutMetricAlarm&Version=2010-08-01&AlarmName=allowed-alarm"
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=test/20260423/us-east-1/cloudwatch/aws4_request, SignedHeaders=host;x-amz-date, Signature=abc")
	req.Header.Set("X-Amz-Date", "20260423T000000Z")
	if got := requestIAMResource(req, classifyIAM(req)); got != "arn:aws:cloudwatch:us-east-1:000000000000:alarm:allowed-alarm" {
		t.Fatalf("unexpected inferred resource: %q", got)
	}
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if !called {
		t.Fatal("expected next handler to be called")
	}
	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected status %d, got %d", http.StatusNoContent, rec.Code)
	}
}

func TestIAMEnforce_enabled_cloudWatchResourcePolicyDeniesNonMatchingAlarmName(t *testing.T) {
	st := state.NewMemoryStore()
	seedIAMUserWithPolicies(t, st, "test", []string{`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"cloudwatch:DeleteAlarms","Resource":"arn:aws:cloudwatch:us-east-1:000000000000:alarm:allowed-alarm"}]}`}, nil)

	h := IAMEnforce(true, st, zap.NewNop(), nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	body := "Action=DeleteAlarms&Version=2010-08-01&AlarmNames.member.1=other-alarm"
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=test/20260423/us-east-1/cloudwatch/aws4_request, SignedHeaders=host;x-amz-date, Signature=abc")
	req.Header.Set("X-Amz-Date", "20260423T000000Z")
	if got := requestIAMResource(req, classifyIAM(req)); got != "arn:aws:cloudwatch:us-east-1:000000000000:alarm:other-alarm" {
		t.Fatalf("unexpected inferred resource: %q", got)
	}
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected status %d, got %d", http.StatusForbidden, rec.Code)
	}
}

func TestIAMEnforce_enabled_lambdaAliasResourcePolicyAllowsMatchingFunction(t *testing.T) {
	st := state.NewMemoryStore()
	seedIAMUserWithPolicies(t, st, "test", []string{`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"lambda:CreateAlias","Resource":"arn:aws:lambda:us-east-1:000000000000:function:allowed-fn"}]}`}, nil)

	called := false
	h := IAMEnforce(true, st, zap.NewNop(), nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusNoContent)
	}))

	req := httptest.NewRequest(http.MethodPost, "/2015-03-31/functions/allowed-fn/aliases", strings.NewReader(`{"Name":"live","FunctionVersion":"1"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=test/20260423/us-east-1/lambda/aws4_request, SignedHeaders=host;x-amz-date, Signature=abc")
	req.Header.Set("X-Amz-Date", "20260423T000000Z")
	if got := classifyIAM(req).action; got != "lambda:CreateAlias" {
		t.Fatalf("unexpected inferred action: %q", got)
	}
	if got := requestIAMResource(req, classifyIAM(req)); got != "arn:aws:lambda:us-east-1:000000000000:function:allowed-fn" {
		t.Fatalf("unexpected inferred resource: %q", got)
	}
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if !called {
		t.Fatal("expected next handler to be called")
	}
	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected status %d, got %d", http.StatusNoContent, rec.Code)
	}
}

func TestIAMEnforce_enabled_lambdaAliasResourcePolicyDeniesNonMatchingFunction(t *testing.T) {
	st := state.NewMemoryStore()
	seedIAMUserWithPolicies(t, st, "test", []string{`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"lambda:CreateAlias","Resource":"arn:aws:lambda:us-east-1:000000000000:function:allowed-fn"}]}`}, nil)

	h := IAMEnforce(true, st, zap.NewNop(), nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	req := httptest.NewRequest(http.MethodPost, "/2015-03-31/functions/other-fn/aliases", strings.NewReader(`{"Name":"live","FunctionVersion":"1"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=test/20260423/us-east-1/lambda/aws4_request, SignedHeaders=host;x-amz-date, Signature=abc")
	req.Header.Set("X-Amz-Date", "20260423T000000Z")
	if got := classifyIAM(req).action; got != "lambda:CreateAlias" {
		t.Fatalf("unexpected inferred action: %q", got)
	}
	if got := requestIAMResource(req, classifyIAM(req)); got != "arn:aws:lambda:us-east-1:000000000000:function:other-fn" {
		t.Fatalf("unexpected inferred resource: %q", got)
	}
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected status %d, got %d", http.StatusForbidden, rec.Code)
	}
}

func TestIAMEnforce_enabled_lambdaResponseStreamingPathDeniesNonMatchingFunction(t *testing.T) {
	st := state.NewMemoryStore()
	seedIAMUserWithPolicies(t, st, "test", []string{`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"lambda:InvokeFunction","Resource":"arn:aws:lambda:us-east-1:000000000000:function:allowed-fn"}]}`}, nil)

	h := IAMEnforce(true, st, zap.NewNop(), nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	req := httptest.NewRequest(http.MethodPost, "/2021-11-15/functions/other-fn/response-streaming-invocations", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=test/20260423/us-east-1/lambda/aws4_request, SignedHeaders=host;x-amz-date, Signature=abc")
	req.Header.Set("X-Amz-Date", "20260423T000000Z")
	if got := classifyIAM(req).action; got != "lambda:InvokeFunction" {
		t.Fatalf("unexpected inferred action: %q", got)
	}
	if got := requestIAMResource(req, classifyIAM(req)); got != "arn:aws:lambda:us-east-1:000000000000:function:other-fn" {
		t.Fatalf("unexpected inferred resource: %q", got)
	}
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected status %d, got %d", http.StatusForbidden, rec.Code)
	}
}

func TestIAMEnforce_enabled_lambdaTestEventsPathDeniesNonMatchingFunction(t *testing.T) {
	st := state.NewMemoryStore()
	seedIAMUserWithPolicies(t, st, "test", []string{`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"lambda:PutTestEvent","Resource":"arn:aws:lambda:us-east-1:000000000000:function:allowed-fn"}]}`}, nil)

	h := IAMEnforce(true, st, zap.NewNop(), nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	req := httptest.NewRequest(http.MethodPut, "/_overcast/lambda/functions/other-fn/test-events/demo", strings.NewReader(`{"Body":"{}"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=test/20260423/us-east-1/lambda/aws4_request, SignedHeaders=host;x-amz-date, Signature=abc")
	req.Header.Set("X-Amz-Date", "20260423T000000Z")
	if got := classifyIAM(req).action; got != "lambda:PutTestEvent" {
		t.Fatalf("unexpected inferred action: %q", got)
	}
	if got := requestIAMResource(req, classifyIAM(req)); got != "arn:aws:lambda:us-east-1:000000000000:function:other-fn" {
		t.Fatalf("unexpected inferred resource: %q", got)
	}
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected status %d, got %d", http.StatusForbidden, rec.Code)
	}
}

func TestIAMEnforce_enabled_lambdaLayerVersionPathDeniesNonMatchingLayer(t *testing.T) {
	st := state.NewMemoryStore()
	seedIAMUserWithPolicies(t, st, "test", []string{`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"lambda:GetLayerVersion","Resource":"arn:aws:lambda:us-east-1:000000000000:layer:allowed-layer:1"}]}`}, nil)

	h := IAMEnforce(true, st, zap.NewNop(), nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	req := httptest.NewRequest(http.MethodGet, "/2018-10-31/layers/other-layer/versions/1", nil)
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=test/20260423/us-east-1/lambda/aws4_request, SignedHeaders=host;x-amz-date, Signature=abc")
	req.Header.Set("X-Amz-Date", "20260423T000000Z")
	if got := classifyIAM(req).action; got != "lambda:GetLayerVersion" {
		t.Fatalf("unexpected inferred action: %q", got)
	}
	if got := requestIAMResource(req, classifyIAM(req)); got != "arn:aws:lambda:us-east-1:000000000000:layer:other-layer:1" {
		t.Fatalf("unexpected inferred resource: %q", got)
	}
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected status %d, got %d", http.StatusForbidden, rec.Code)
	}
}

func TestIAMEnforce_enabled_pipesResourcePolicyDeniesNonMatchingPipe(t *testing.T) {
	st := state.NewMemoryStore()
	seedIAMUserWithPolicies(t, st, "test", []string{`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"pipes:DescribePipe","Resource":"arn:aws:pipes:us-east-1:000000000000:pipe/allowed-pipe"}]}`}, nil)

	h := IAMEnforce(true, st, zap.NewNop(), nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	req := httptest.NewRequest(http.MethodGet, "/v1/pipes/other-pipe", nil)
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=test/20260423/us-east-1/pipes/aws4_request, SignedHeaders=host;x-amz-date, Signature=abc")
	req.Header.Set("X-Amz-Date", "20260423T000000Z")
	if got := classifyIAM(req).action; got != "pipes:DescribePipe" {
		t.Fatalf("unexpected inferred action: %q", got)
	}
	if got := requestIAMResource(req, classifyIAM(req)); got != "arn:aws:pipes:us-east-1:000000000000:pipe/other-pipe" {
		t.Fatalf("unexpected inferred resource: %q", got)
	}
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected status %d, got %d", http.StatusForbidden, rec.Code)
	}
}
