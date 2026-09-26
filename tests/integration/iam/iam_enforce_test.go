package iam_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/overcast-sh/overcast/tests/helpers"
)

func sqsCallWithAuth(t *testing.T, srv *helpers.TestServer, target string, body map[string]any, auth string) *http.Response {
	t.Helper()
	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal sqs body: %v", err)
	}
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/", bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("build sqs request: %v", err)
	}
	req.Header.Set("Content-Type", "application/x-amz-json-1.0")
	req.Header.Set("X-Amz-Target", target)
	if auth != "" {
		req.Header.Set("Authorization", auth)
		req.Header.Set("X-Amz-Date", "20260423T000000Z")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do sqs request: %v", err)
	}
	return resp
}

func ddbCallWithAuth(t *testing.T, srv *helpers.TestServer, operation string, body map[string]any, auth string) *http.Response {
	t.Helper()
	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal ddb body: %v", err)
	}
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/", bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("build ddb request: %v", err)
	}
	req.Header.Set("Content-Type", "application/x-amz-json-1.0")
	req.Header.Set("X-Amz-Target", "DynamoDB_20120810."+operation)
	if auth != "" {
		req.Header.Set("Authorization", auth)
		req.Header.Set("X-Amz-Date", "20260423T000000Z")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do ddb request: %v", err)
	}
	return resp
}

func ssmCallWithAuth(t *testing.T, srv *helpers.TestServer, operation string, body map[string]any, auth string) *http.Response {
	t.Helper()
	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal ssm body: %v", err)
	}
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/", bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("build ssm request: %v", err)
	}
	req.Header.Set("Content-Type", "application/x-amz-json-1.1")
	req.Header.Set("X-Amz-Target", "AmazonSSM."+operation)
	if auth != "" {
		req.Header.Set("Authorization", auth)
		req.Header.Set("X-Amz-Date", "20260423T000000Z")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do ssm request: %v", err)
	}
	return resp
}

func kmsCallWithAuth(t *testing.T, srv *helpers.TestServer, operation string, body map[string]any, auth string) *http.Response {
	t.Helper()
	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal kms body: %v", err)
	}
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/", bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("build kms request: %v", err)
	}
	req.Header.Set("Content-Type", "application/x-amz-json-1.1")
	req.Header.Set("X-Amz-Target", "TrentService."+operation)
	if auth != "" {
		req.Header.Set("Authorization", auth)
		req.Header.Set("X-Amz-Date", "20260423T000000Z")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do kms request: %v", err)
	}
	return resp
}

func kinesisCallWithAuth(t *testing.T, srv *helpers.TestServer, operation string, body map[string]any, auth string) *http.Response {
	t.Helper()
	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal kinesis body: %v", err)
	}
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/", bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("build kinesis request: %v", err)
	}
	req.Header.Set("Content-Type", "application/x-amz-json-1.1")
	req.Header.Set("X-Amz-Target", "Kinesis_20131202."+operation)
	if auth != "" {
		req.Header.Set("Authorization", auth)
		req.Header.Set("X-Amz-Date", "20260423T000000Z")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do kinesis request: %v", err)
	}
	return resp
}

func firehoseCallWithAuth(t *testing.T, srv *helpers.TestServer, operation string, body map[string]any, auth string) *http.Response {
	t.Helper()
	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal firehose body: %v", err)
	}
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/", bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("build firehose request: %v", err)
	}
	req.Header.Set("Content-Type", "application/x-amz-json-1.1")
	req.Header.Set("X-Amz-Target", "Firehose_20150804."+operation)
	if auth != "" {
		req.Header.Set("Authorization", auth)
		req.Header.Set("X-Amz-Date", "20260423T000000Z")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do firehose request: %v", err)
	}
	return resp
}

func logsCallWithAuth(t *testing.T, srv *helpers.TestServer, operation string, body map[string]any, auth string) *http.Response {
	t.Helper()
	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal logs body: %v", err)
	}
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/", bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("build logs request: %v", err)
	}
	req.Header.Set("Content-Type", "application/x-amz-json-1.1")
	req.Header.Set("X-Amz-Target", "Logs_20140328."+operation)
	if auth != "" {
		req.Header.Set("Authorization", auth)
		req.Header.Set("X-Amz-Date", "20260423T000000Z")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do logs request: %v", err)
	}
	return resp
}

func ecrCallWithAuth(t *testing.T, srv *helpers.TestServer, operation string, body map[string]any, auth string) *http.Response {
	t.Helper()
	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal ecr body: %v", err)
	}
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/", bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("build ecr request: %v", err)
	}
	req.Header.Set("Content-Type", "application/x-amz-json-1.1")
	req.Header.Set("X-Amz-Target", "AmazonEC2ContainerRegistry_V20150921."+operation)
	if auth != "" {
		req.Header.Set("Authorization", auth)
		req.Header.Set("X-Amz-Date", "20260423T000000Z")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do ecr request: %v", err)
	}
	return resp
}

func secretsManagerCallWithAuth(t *testing.T, srv *helpers.TestServer, operation string, body map[string]any, auth string) *http.Response {
	t.Helper()
	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal secretsmanager body: %v", err)
	}
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/", bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("build secretsmanager request: %v", err)
	}
	req.Header.Set("Content-Type", "application/x-amz-json-1.1")
	req.Header.Set("X-Amz-Target", "secretsmanager."+operation)
	if auth != "" {
		req.Header.Set("Authorization", auth)
		req.Header.Set("X-Amz-Date", "20260423T000000Z")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do secretsmanager request: %v", err)
	}
	return resp
}

func stepFunctionsCallWithAuth(t *testing.T, srv *helpers.TestServer, operation string, body map[string]any, auth string) *http.Response {
	t.Helper()
	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal stepfunctions body: %v", err)
	}
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/", bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("build stepfunctions request: %v", err)
	}
	req.Header.Set("Content-Type", "application/x-amz-json-1.1")
	req.Header.Set("X-Amz-Target", "AWSStepFunctions."+operation)
	if auth != "" {
		req.Header.Set("Authorization", auth)
		req.Header.Set("X-Amz-Date", "20260423T000000Z")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do stepfunctions request: %v", err)
	}
	return resp
}

func cloudFormationCallWithAuth(t *testing.T, srv *helpers.TestServer, vals url.Values, auth string) *http.Response {
	t.Helper()
	if vals.Get("Version") == "" {
		vals.Set("Version", "2010-05-15")
	}
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/?"+vals.Encode(), strings.NewReader(vals.Encode()))
	if err != nil {
		t.Fatalf("build cloudformation request: %v", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if auth != "" {
		req.Header.Set("Authorization", auth)
		req.Header.Set("X-Amz-Date", "20260423T000000Z")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do cloudformation request: %v", err)
	}
	return resp
}

func ecsCallWithAuth(t *testing.T, srv *helpers.TestServer, operation string, body map[string]any, auth string) *http.Response {
	t.Helper()
	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal ecs body: %v", err)
	}
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/", bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("build ecs request: %v", err)
	}
	req.Header.Set("Content-Type", "application/x-amz-json-1.1")
	req.Header.Set("X-Amz-Target", "AmazonEC2ContainerServiceV20141113."+operation)
	if auth != "" {
		req.Header.Set("Authorization", auth)
		req.Header.Set("X-Amz-Date", "20260423T000000Z")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do ecs request: %v", err)
	}
	return resp
}

func lambdaCallWithAuth(t *testing.T, srv *helpers.TestServer, method, path string, body map[string]any, auth string) *http.Response {
	t.Helper()
	var payload []byte
	if body != nil {
		var err error
		payload, err = json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal lambda body: %v", err)
		}
	}
	req, err := http.NewRequest(method, srv.URL+path, bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("build lambda request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if auth != "" {
		req.Header.Set("Authorization", auth)
		req.Header.Set("X-Amz-Date", "20260423T000000Z")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do lambda request: %v", err)
	}
	return resp
}

func pipesCallWithAuth(t *testing.T, srv *helpers.TestServer, method, path string, body map[string]any, auth string) *http.Response {
	t.Helper()
	var payload []byte
	if body != nil {
		var err error
		payload, err = json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal pipes body: %v", err)
		}
	}
	req, err := http.NewRequest(method, srv.URL+path, bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("build pipes request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if auth != "" {
		req.Header.Set("Authorization", auth)
		req.Header.Set("X-Amz-Date", "20260423T000000Z")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do pipes request: %v", err)
	}
	return resp
}

func seedIAMPrincipal(t *testing.T, srv *helpers.TestServer, accessKey string, userDoc string) {
	t.Helper()
	helpers.SeedIAMUser(t, srv, accessKey, userDoc)
}

func seedIAMGroupPrincipal(t *testing.T, srv *helpers.TestServer, groupName string, members []string, inlineDoc string) {
	t.Helper()
	if srv.Store == nil {
		t.Fatal("test server store is nil")
	}
	group := map[string]any{
		"Members": members,
		"InlinePolicies": map[string]string{
			"inline-1": inlineDoc,
		},
	}
	b, err := json.Marshal(group)
	if err != nil {
		t.Fatalf("marshal group seed: %v", err)
	}
	if err := srv.Store.Set(context.Background(), "iam:groups", groupName, string(b)); err != nil {
		t.Fatalf("seed group: %v", err)
	}
}

func s3CallWithAuth(t *testing.T, srv *helpers.TestServer, method, path, auth string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, srv.URL+path, nil)
	if err != nil {
		t.Fatalf("build s3 request: %v", err)
	}
	if auth != "" {
		req.Header.Set("Authorization", auth)
		req.Header.Set("X-Amz-Date", "20260423T000000Z")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do s3 request: %v", err)
	}
	return resp
}

func queryCallWithAuth(
	t *testing.T,
	srv *helpers.TestServer,
	action string,
	version string,
	auth string,
) *http.Response {
	t.Helper()
	if version == "" {
		version = "2011-06-15"
	}
	form := url.Values{}
	form.Set("Action", action)
	form.Set("Version", version)
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/?"+form.Encode(), strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatalf("build query request: %v", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if auth != "" {
		req.Header.Set("Authorization", auth)
		req.Header.Set("X-Amz-Date", "20260423T000000Z")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do query request: %v", err)
	}
	return resp
}

func queryCallWithAuthValues(
	t *testing.T,
	srv *helpers.TestServer,
	vals url.Values,
	auth string,
) *http.Response {
	t.Helper()
	if vals.Get("Version") == "" {
		vals.Set("Version", "2011-06-15")
	}
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/", strings.NewReader(vals.Encode()))
	if err != nil {
		t.Fatalf("build query request: %v", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if auth != "" {
		req.Header.Set("Authorization", auth)
		req.Header.Set("X-Amz-Date", "20260423T000000Z")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do query request: %v", err)
	}
	return resp
}

func TestIAMEnforceIntegration_signedAllowOnSQS(t *testing.T) {
	srv := helpers.NewTestServer(t,
		helpers.WithEnforceIAM(true),
	)
	seedIAMPrincipal(t, srv, "test", `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"sqs:CreateQueue","Resource":"*"}]}`)

	resp := sqsCallWithAuth(
		t,
		srv,
		"AmazonSQS.CreateQueue",
		map[string]any{"QueueName": "iam-allow"},
		"AWS4-HMAC-SHA256 Credential=test/20260423/us-east-1/sqs/aws4_request, SignedHeaders=host;x-amz-date, Signature=abc",
	)
	defer resp.Body.Close()

	helpers.AssertStatus(t, resp, http.StatusOK)
}

func TestIAMEnforceIntegration_signedDenyWithoutPolicyOnSQS(t *testing.T) {
	srv := helpers.NewTestServer(t,
		helpers.WithEnforceIAM(true),
	)

	resp := sqsCallWithAuth(
		t,
		srv,
		"AmazonSQS.CreateQueue",
		map[string]any{"QueueName": "iam-deny"},
		"AWS4-HMAC-SHA256 Credential=test/20260423/us-east-1/sqs/aws4_request, SignedHeaders=host;x-amz-date, Signature=abc",
	)
	defer resp.Body.Close()

	helpers.AssertStatus(t, resp, http.StatusForbidden)
}

func TestIAMEnforceIntegration_signedAllowOnS3ListBuckets(t *testing.T) {
	srv := helpers.NewTestServer(t,
		helpers.WithEnforceIAM(true),
	)
	seedIAMPrincipal(t, srv, "test", `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"s3:ListBuckets","Resource":"*"}]}`)

	resp := s3CallWithAuth(
		t,
		srv,
		http.MethodGet,
		"/",
		"AWS4-HMAC-SHA256 Credential=test/20260423/us-east-1/s3/aws4_request, SignedHeaders=host;x-amz-date, Signature=abc",
	)
	defer resp.Body.Close()

	helpers.AssertStatus(t, resp, http.StatusOK)
}

func TestIAMEnforceIntegration_signedDenyOnS3ListBuckets(t *testing.T) {
	srv := helpers.NewTestServer(t,
		helpers.WithEnforceIAM(true),
	)
	seedIAMPrincipal(t, srv, "test", `{"Version":"2012-10-17","Statement":[{"Effect":"Deny","Action":"s3:ListBuckets","Resource":"*"}]}`)

	resp := s3CallWithAuth(
		t,
		srv,
		http.MethodGet,
		"/",
		"AWS4-HMAC-SHA256 Credential=test/20260423/us-east-1/s3/aws4_request, SignedHeaders=host;x-amz-date, Signature=abc",
	)
	defer resp.Body.Close()

	helpers.AssertStatus(t, resp, http.StatusForbidden)
}

func TestIAMEnforceIntegration_groupExplicitDenyOverridesUserAllowOnSQS(t *testing.T) {
	srv := helpers.NewTestServer(t,
		helpers.WithEnforceIAM(true),
	)
	seedIAMPrincipal(t, srv, "test", `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"sqs:CreateQueue","Resource":"*"}]}`)
	seedIAMGroupPrincipal(t, srv, "security", []string{"test"}, `{"Version":"2012-10-17","Statement":[{"Effect":"Deny","Action":"sqs:CreateQueue","Resource":"*"}]}`)

	resp := sqsCallWithAuth(
		t,
		srv,
		"AmazonSQS.CreateQueue",
		map[string]any{"QueueName": "iam-group-deny"},
		"AWS4-HMAC-SHA256 Credential=test/20260423/us-east-1/sqs/aws4_request, SignedHeaders=host;x-amz-date, Signature=abc",
	)
	defer resp.Body.Close()

	helpers.AssertStatus(t, resp, http.StatusForbidden)
}

func TestIAMEnforceIntegration_groupAllowWithoutUserInlineOnSQS(t *testing.T) {
	srv := helpers.NewTestServer(t,
		helpers.WithEnforceIAM(true),
	)
	seedIAMPrincipal(t, srv, "test", `{"Version":"2012-10-17","Statement":[]}`)
	seedIAMGroupPrincipal(t, srv, "devs", []string{"test"}, `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"sqs:CreateQueue","Resource":"*"}]}`)

	resp := sqsCallWithAuth(
		t,
		srv,
		"AmazonSQS.CreateQueue",
		map[string]any{"QueueName": "iam-group-allow"},
		"AWS4-HMAC-SHA256 Credential=test/20260423/us-east-1/sqs/aws4_request, SignedHeaders=host;x-amz-date, Signature=abc",
	)
	defer resp.Body.Close()

	helpers.AssertStatus(t, resp, http.StatusOK)
}

func TestIAMEnforceIntegration_signedAllowOnS3GetObjectResourceMatch(t *testing.T) {
	srv := helpers.NewTestServer(t,
		helpers.WithEnforceIAM(true),
	)
	seedIAMPrincipal(t, srv, "test", `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"s3:GetObject","Resource":"arn:aws:s3:::demo/*"}]}`)

	resp := s3CallWithAuth(
		t,
		srv,
		http.MethodGet,
		"/demo/object.txt",
		"AWS4-HMAC-SHA256 Credential=test/20260423/us-east-1/s3/aws4_request, SignedHeaders=host;x-amz-date, Signature=abc",
	)
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusForbidden {
		t.Fatalf("expected request to pass IAM enforcement, got status %d", resp.StatusCode)
	}
}

func TestIAMEnforceIntegration_signedDenyOnS3GetObjectResourceMismatch(t *testing.T) {
	srv := helpers.NewTestServer(t,
		helpers.WithEnforceIAM(true),
	)
	seedIAMPrincipal(t, srv, "test", `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"s3:GetObject","Resource":"arn:aws:s3:::demo/allowed/*"}]}`)

	resp := s3CallWithAuth(
		t,
		srv,
		http.MethodGet,
		"/demo/blocked/object.txt",
		"AWS4-HMAC-SHA256 Credential=test/20260423/us-east-1/s3/aws4_request, SignedHeaders=host;x-amz-date, Signature=abc",
	)
	defer resp.Body.Close()

	helpers.AssertStatus(t, resp, http.StatusForbidden)
}

func TestIAMEnforceIntegration_signedAllowOnSTSQuery(t *testing.T) {
	srv := helpers.NewTestServer(t,
		helpers.WithEnforceIAM(true),
	)
	seedIAMPrincipal(t, srv, "test", `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"sts:GetCallerIdentity","Resource":"*"}]}`)

	resp := queryCallWithAuth(
		t,
		srv,
		"GetCallerIdentity",
		"2011-06-15",
		"AWS4-HMAC-SHA256 Credential=test/20260423/us-east-1/sts/aws4_request, SignedHeaders=host;x-amz-date, Signature=abc",
	)
	defer resp.Body.Close()

	helpers.AssertStatus(t, resp, http.StatusOK)
}

func TestIAMEnforceIntegration_signedDenyOnSTSQueryWithoutAllow(t *testing.T) {
	srv := helpers.NewTestServer(t,
		helpers.WithEnforceIAM(true),
	)

	resp := queryCallWithAuth(
		t,
		srv,
		"GetCallerIdentity",
		"2011-06-15",
		"AWS4-HMAC-SHA256 Credential=test/20260423/us-east-1/sts/aws4_request, SignedHeaders=host;x-amz-date, Signature=abc",
	)
	defer resp.Body.Close()

	helpers.AssertStatus(t, resp, http.StatusForbidden)
}

func TestIAMEnforceIntegration_groupExplicitDenyOverridesUserAllowOnSTSQuery(t *testing.T) {
	srv := helpers.NewTestServer(t,
		helpers.WithEnforceIAM(true),
	)
	seedIAMPrincipal(t, srv, "test", `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"sts:GetCallerIdentity","Resource":"*"}]}`)
	seedIAMGroupPrincipal(t, srv, "security", []string{"test"}, `{"Version":"2012-10-17","Statement":[{"Effect":"Deny","Action":"sts:GetCallerIdentity","Resource":"*"}]}`)

	resp := queryCallWithAuth(
		t,
		srv,
		"GetCallerIdentity",
		"2011-06-15",
		"AWS4-HMAC-SHA256 Credential=test/20260423/us-east-1/sts/aws4_request, SignedHeaders=host;x-amz-date, Signature=abc",
	)
	defer resp.Body.Close()

	helpers.AssertStatus(t, resp, http.StatusForbidden)
}

// seedIAMRoleWithSession seeds an iam:sessions entry (mapping a temp access key
// to a role ARN) and an iam:roles entry with the given inline policy document.
func seedIAMRoleWithSession(t *testing.T, srv *helpers.TestServer, tempAccessKey, roleArn, roleName, inlineDoc string) {
	t.Helper()
	if srv.Store == nil {
		t.Fatal("test server store is nil")
	}
	ctx := context.Background()

	session := map[string]string{
		"RoleArn":  roleArn,
		"RoleName": roleName,
	}
	sb, err := json.Marshal(session)
	if err != nil {
		t.Fatalf("marshal role session: %v", err)
	}
	if err := srv.Store.Set(ctx, "iam:sessions", tempAccessKey, string(sb)); err != nil {
		t.Fatalf("seed role session: %v", err)
	}

	role := map[string]any{
		"RoleName": roleName,
		"InlinePolicies": map[string]string{
			"inline-1": inlineDoc,
		},
	}
	rb, err := json.Marshal(role)
	if err != nil {
		t.Fatalf("marshal role: %v", err)
	}
	if err := srv.Store.Set(ctx, "iam:roles", roleName, string(rb)); err != nil {
		t.Fatalf("seed role: %v", err)
	}
}

func TestIAMEnforceIntegration_roleSession_allowsMatchingAction(t *testing.T) {
	srv := helpers.NewTestServer(t,
		helpers.WithEnforceIAM(true),
	)
	seedIAMRoleWithSession(t, srv,
		"ASIA-role-key",
		"arn:aws:iam::123456789012:role/ci-role",
		"ci-role",
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"sqs:CreateQueue","Resource":"*"}]}`,
	)

	resp := sqsCallWithAuth(
		t,
		srv,
		"AmazonSQS.CreateQueue",
		map[string]any{"QueueName": "role-allowed"},
		"AWS4-HMAC-SHA256 Credential=ASIA-role-key/20260423/us-east-1/sqs/aws4_request, SignedHeaders=host;x-amz-date, Signature=abc",
	)
	defer resp.Body.Close()

	helpers.AssertStatus(t, resp, http.StatusOK)
}

func TestIAMEnforceIntegration_roleSession_deniesUnallowedAction(t *testing.T) {
	srv := helpers.NewTestServer(t,
		helpers.WithEnforceIAM(true),
	)
	seedIAMRoleWithSession(t, srv,
		"ASIA-role-key",
		"arn:aws:iam::123456789012:role/ci-role",
		"ci-role",
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"sqs:CreateQueue","Resource":"*"}]}`,
	)

	resp := sqsCallWithAuth(
		t,
		srv,
		"AmazonSQS.DeleteQueue",
		map[string]any{"QueueUrl": "https://localhost/000000000000/role-allowed"},
		"AWS4-HMAC-SHA256 Credential=ASIA-role-key/20260423/us-east-1/sqs/aws4_request, SignedHeaders=host;x-amz-date, Signature=abc",
	)
	defer resp.Body.Close()

	helpers.AssertStatus(t, resp, http.StatusForbidden)
}

func TestIAMEnforceIntegration_roleSession_explicitDenyBlocksAllowedAction(t *testing.T) {
	srv := helpers.NewTestServer(t,
		helpers.WithEnforceIAM(true),
	)
	seedIAMRoleWithSession(t, srv,
		"ASIA-role-key",
		"arn:aws:iam::123456789012:role/ci-role",
		"ci-role",
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"sqs:*","Resource":"*"},{"Effect":"Deny","Action":"sqs:DeleteQueue","Resource":"*"}]}`,
	)

	resp := sqsCallWithAuth(
		t,
		srv,
		"AmazonSQS.DeleteQueue",
		map[string]any{"QueueUrl": "https://localhost/000000000000/role-allowed"},
		"AWS4-HMAC-SHA256 Credential=ASIA-role-key/20260423/us-east-1/sqs/aws4_request, SignedHeaders=host;x-amz-date, Signature=abc",
	)
	defer resp.Body.Close()

	helpers.AssertStatus(t, resp, http.StatusForbidden)
}

// ─── Condition-block integration tests ───────────────────────────────────────

func TestIAMEnforceIntegration_condition_regionAllows(t *testing.T) {
	srv := helpers.NewTestServer(t,
		helpers.WithEnforceIAM(true),
	)
	// Allow SQS only in us-east-1.
	seedIAMPrincipal(t, srv, "test",
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"sqs:CreateQueue","Resource":"*","Condition":{"StringEquals":{"aws:RequestedRegion":"us-east-1"}}}]}`,
	)

	resp := sqsCallWithAuth(
		t,
		srv,
		"AmazonSQS.CreateQueue",
		map[string]any{"QueueName": "cond-allowed"},
		// Credential scope region: us-east-1 — matches condition.
		"AWS4-HMAC-SHA256 Credential=test/20260423/us-east-1/sqs/aws4_request, SignedHeaders=host;x-amz-date, Signature=abc",
	)
	defer resp.Body.Close()

	helpers.AssertStatus(t, resp, http.StatusOK)
}

func TestIAMEnforceIntegration_condition_regionBlocks(t *testing.T) {
	srv := helpers.NewTestServer(t,
		helpers.WithEnforceIAM(true),
	)
	// Allow SQS only in us-east-1.
	seedIAMPrincipal(t, srv, "test",
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"sqs:CreateQueue","Resource":"*","Condition":{"StringEquals":{"aws:RequestedRegion":"us-east-1"}}}]}`,
	)

	resp := sqsCallWithAuth(
		t,
		srv,
		"AmazonSQS.CreateQueue",
		map[string]any{"QueueName": "cond-blocked"},
		// Credential scope region: eu-west-1 — does NOT match condition.
		"AWS4-HMAC-SHA256 Credential=test/20260423/eu-west-1/sqs/aws4_request, SignedHeaders=host;x-amz-date, Signature=abc",
	)
	defer resp.Body.Close()

	helpers.AssertStatus(t, resp, http.StatusForbidden)
}

func TestIAMEnforceIntegration_condition_unknownOperator_denies(t *testing.T) {
	srv := helpers.NewTestServer(t,
		helpers.WithEnforceIAM(true),
	)
	// Unknown condition operator — must fail closed.
	seedIAMPrincipal(t, srv, "test",
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"sqs:CreateQueue","Resource":"*","Condition":{"WeirdOp":{"aws:RequestedRegion":"us-east-1"}}}]}`,
	)

	resp := sqsCallWithAuth(
		t,
		srv,
		"AmazonSQS.CreateQueue",
		map[string]any{"QueueName": "cond-unknown"},
		"AWS4-HMAC-SHA256 Credential=test/20260423/us-east-1/sqs/aws4_request, SignedHeaders=host;x-amz-date, Signature=abc",
	)
	defer resp.Body.Close()

	helpers.AssertStatus(t, resp, http.StatusForbidden)
}

func TestIAMEnforceIntegration_condition_denyInRegion_allowsOtherRegion(t *testing.T) {
	srv := helpers.NewTestServer(t,
		helpers.WithEnforceIAM(true),
	)
	// Allow all SQS; then deny SQS in eu-west-1 only.
	seedIAMPrincipal(t, srv, "test",
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"sqs:*","Resource":"*"},{"Effect":"Deny","Action":"sqs:*","Resource":"*","Condition":{"StringEquals":{"aws:RequestedRegion":"eu-west-1"}}}]}`,
	)

	resp := sqsCallWithAuth(
		t,
		srv,
		"AmazonSQS.CreateQueue",
		map[string]any{"QueueName": "cond-us"},
		// us-east-1 — deny condition does not match → request is allowed.
		"AWS4-HMAC-SHA256 Credential=test/20260423/us-east-1/sqs/aws4_request, SignedHeaders=host;x-amz-date, Signature=abc",
	)
	defer resp.Body.Close()

	helpers.AssertStatus(t, resp, http.StatusOK)
}

func TestIAMEnforceIntegration_condition_denyInRegion_blocksMatchingRegion(t *testing.T) {
	srv := helpers.NewTestServer(t,
		helpers.WithEnforceIAM(true),
	)
	// Allow all SQS; then deny SQS in eu-west-1 only.
	seedIAMPrincipal(t, srv, "test",
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"sqs:*","Resource":"*"},{"Effect":"Deny","Action":"sqs:*","Resource":"*","Condition":{"StringEquals":{"aws:RequestedRegion":"eu-west-1"}}}]}`,
	)

	resp := sqsCallWithAuth(
		t,
		srv,
		"AmazonSQS.CreateQueue",
		map[string]any{"QueueName": "cond-eu"},
		// eu-west-1 — deny condition matches → request is denied.
		"AWS4-HMAC-SHA256 Credential=test/20260423/eu-west-1/sqs/aws4_request, SignedHeaders=host;x-amz-date, Signature=abc",
	)
	defer resp.Body.Close()

	helpers.AssertStatus(t, resp, http.StatusForbidden)
}

func TestIAMEnforceIntegration_condition_principalArnAllows(t *testing.T) {
	srv := helpers.NewTestServer(t,
		helpers.WithEnforceIAM(true),
	)

	user := map[string]any{
		"UserName": "test-user",
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
	if err := srv.Store.Set(context.Background(), "iam:users", "test-user", string(b)); err != nil {
		t.Fatalf("seed user: %v", err)
	}

	resp := sqsCallWithAuth(
		t,
		srv,
		"AmazonSQS.CreateQueue",
		map[string]any{"QueueName": "cond-principal-arn"},
		"AWS4-HMAC-SHA256 Credential=test/20260423/us-east-1/sqs/aws4_request, SignedHeaders=host;x-amz-date, Signature=abc",
	)
	defer resp.Body.Close()

	helpers.AssertStatus(t, resp, http.StatusOK)
}

func TestIAMEnforceIntegration_condition_currentTimeAllows(t *testing.T) {
	srv := helpers.NewTestServer(t,
		helpers.WithEnforceIAM(true),
	)
	seedIAMPrincipal(t, srv, "test",
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"sqs:CreateQueue","Resource":"*","Condition":{"StringEquals":{"aws:CurrentTime":"2026-04-23T00:00:00Z"}}}]}`,
	)

	resp := sqsCallWithAuth(
		t,
		srv,
		"AmazonSQS.CreateQueue",
		map[string]any{"QueueName": "cond-current-time"},
		"AWS4-HMAC-SHA256 Credential=test/20260423/us-east-1/sqs/aws4_request, SignedHeaders=host;x-amz-date, Signature=abc",
	)
	defer resp.Body.Close()

	helpers.AssertStatus(t, resp, http.StatusOK)
}

func TestIAMEnforceIntegration_condition_dateLessThanAllows(t *testing.T) {
	srv := helpers.NewTestServer(t,
		helpers.WithEnforceIAM(true),
	)
	seedIAMPrincipal(t, srv, "test",
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"sqs:CreateQueue","Resource":"*","Condition":{"DateLessThan":{"aws:CurrentTime":"2026-04-24T00:00:00Z"}}}]}`,
	)

	resp := sqsCallWithAuth(
		t,
		srv,
		"AmazonSQS.CreateQueue",
		map[string]any{"QueueName": "cond-date-less-than"},
		"AWS4-HMAC-SHA256 Credential=test/20260423/us-east-1/sqs/aws4_request, SignedHeaders=host;x-amz-date, Signature=abc",
	)
	defer resp.Body.Close()

	helpers.AssertStatus(t, resp, http.StatusOK)
}

func TestIAMEnforceIntegration_condition_nullFalse_allowsWhenPrincipalArnPresent(t *testing.T) {
	srv := helpers.NewTestServer(t,
		helpers.WithEnforceIAM(true),
	)
	seedIAMPrincipal(t, srv, "test",
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"sqs:CreateQueue","Resource":"*","Condition":{"Null":{"aws:PrincipalArn":"false"}}}]}`,
	)

	resp := sqsCallWithAuth(
		t,
		srv,
		"AmazonSQS.CreateQueue",
		map[string]any{"QueueName": "cond-null-false"},
		"AWS4-HMAC-SHA256 Credential=test/20260423/us-east-1/sqs/aws4_request, SignedHeaders=host;x-amz-date, Signature=abc",
	)
	defer resp.Body.Close()

	helpers.AssertStatus(t, resp, http.StatusOK)
}

func TestIAMEnforceIntegration_condition_nullTrue_deniesWhenPrincipalArnPresent(t *testing.T) {
	srv := helpers.NewTestServer(t,
		helpers.WithEnforceIAM(true),
	)
	seedIAMPrincipal(t, srv, "test",
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"sqs:CreateQueue","Resource":"*","Condition":{"Null":{"aws:PrincipalArn":"true"}}}]}`,
	)

	resp := sqsCallWithAuth(
		t,
		srv,
		"AmazonSQS.CreateQueue",
		map[string]any{"QueueName": "cond-null-true"},
		"AWS4-HMAC-SHA256 Credential=test/20260423/us-east-1/sqs/aws4_request, SignedHeaders=host;x-amz-date, Signature=abc",
	)
	defer resp.Body.Close()

	helpers.AssertStatus(t, resp, http.StatusForbidden)
}

func TestIAMEnforceIntegration_condition_stringEqualsIfExists_allowsWhenKeyMissing(t *testing.T) {
	srv := helpers.NewTestServer(t,
		helpers.WithEnforceIAM(true),
	)
	seedIAMPrincipal(t, srv, "test",
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"sqs:CreateQueue","Resource":"*","Condition":{"StringEqualsIfExists":{"aws:PrincipalTag/team":"platform"}}}]}`,
	)

	resp := sqsCallWithAuth(
		t,
		srv,
		"AmazonSQS.CreateQueue",
		map[string]any{"QueueName": "cond-ifexists-missing"},
		"AWS4-HMAC-SHA256 Credential=test/20260423/us-east-1/sqs/aws4_request, SignedHeaders=host;x-amz-date, Signature=abc",
	)
	defer resp.Body.Close()

	helpers.AssertStatus(t, resp, http.StatusOK)
}

func TestIAMEnforceIntegration_sqsResourceScopedAllowsMatchingQueue(t *testing.T) {
	srv := helpers.NewTestServer(t,
		helpers.WithEnforceIAM(true),
	)
	seedIAMPrincipal(t, srv, "test",
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"sqs:CreateQueue","Resource":"arn:aws:sqs:us-east-1:000000000000:resource-allowed"}]}`,
	)

	resp := sqsCallWithAuth(
		t,
		srv,
		"AmazonSQS.CreateQueue",
		map[string]any{"QueueName": "resource-allowed"},
		"AWS4-HMAC-SHA256 Credential=test/20260423/us-east-1/sqs/aws4_request, SignedHeaders=host;x-amz-date, Signature=abc",
	)
	defer resp.Body.Close()

	helpers.AssertStatus(t, resp, http.StatusOK)
}

func TestIAMEnforceIntegration_sqsResourceScopedDeniesQueueURLMismatch(t *testing.T) {
	srv := helpers.NewTestServer(t,
		helpers.WithEnforceIAM(true),
	)
	seedIAMPrincipal(t, srv, "test",
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"sqs:DeleteQueue","Resource":"arn:aws:sqs:us-east-1:000000000000:resource-allowed"}]}`,
	)

	resp := sqsCallWithAuth(
		t,
		srv,
		"AmazonSQS.DeleteQueue",
		map[string]any{"QueueUrl": "https://localhost/000000000000/resource-other"},
		"AWS4-HMAC-SHA256 Credential=test/20260423/us-east-1/sqs/aws4_request, SignedHeaders=host;x-amz-date, Signature=abc",
	)
	defer resp.Body.Close()

	helpers.AssertStatus(t, resp, http.StatusForbidden)
}

func TestIAMEnforceIntegration_snsResourceScopedAllowsMatchingCreateTopicName(t *testing.T) {
	srv := helpers.NewTestServer(t,
		helpers.WithEnforceIAM(true),
	)
	seedIAMPrincipal(t, srv, "test",
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"sns:CreateTopic","Resource":"arn:aws:sns:us-east-1:000000000000:iam-topic-allowed"}]}`,
	)

	resp := queryCallWithAuthValues(t, srv, url.Values{
		"Action":  {"CreateTopic"},
		"Version": {"2010-03-31"},
		"Name":    {"iam-topic-allowed"},
	}, "AWS4-HMAC-SHA256 Credential=test/20260423/us-east-1/sns/aws4_request, SignedHeaders=host;x-amz-date, Signature=abc")
	defer resp.Body.Close()

	helpers.AssertStatus(t, resp, http.StatusOK)
}

func TestIAMEnforceIntegration_snsResourceScopedDeniesMismatchedCreateTopicName(t *testing.T) {
	srv := helpers.NewTestServer(t,
		helpers.WithEnforceIAM(true),
	)
	seedIAMPrincipal(t, srv, "test",
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"sns:CreateTopic","Resource":"arn:aws:sns:us-east-1:000000000000:iam-topic-allowed"}]}`,
	)

	resp := queryCallWithAuthValues(t, srv, url.Values{
		"Action":  {"CreateTopic"},
		"Version": {"2010-03-31"},
		"Name":    {"iam-topic-other"},
	}, "AWS4-HMAC-SHA256 Credential=test/20260423/us-east-1/sns/aws4_request, SignedHeaders=host;x-amz-date, Signature=abc")
	defer resp.Body.Close()

	helpers.AssertStatus(t, resp, http.StatusForbidden)
}

func TestIAMEnforceIntegration_dynamodbResourceScopedAllowsMatchingTable(t *testing.T) {
	srv := helpers.NewTestServer(t,
		helpers.WithEnforceIAM(true),
	)
	seedIAMPrincipal(t, srv, "test",
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"dynamodb:CreateTable","Resource":"*"},{"Effect":"Allow","Action":"dynamodb:PutItem","Resource":"arn:aws:dynamodb:us-east-1:000000000000:table/iam-allowed"}]}`,
	)

	createResp := ddbCallWithAuth(t, srv, "CreateTable", map[string]any{
		"TableName":            "iam-allowed",
		"AttributeDefinitions": []map[string]any{{"AttributeName": "id", "AttributeType": "S"}},
		"KeySchema":            []map[string]any{{"AttributeName": "id", "KeyType": "HASH"}},
		"BillingMode":          "PAY_PER_REQUEST",
	}, "AWS4-HMAC-SHA256 Credential=test/20260423/us-east-1/dynamodb/aws4_request, SignedHeaders=host;x-amz-date, Signature=abc")
	defer createResp.Body.Close()
	helpers.AssertStatus(t, createResp, http.StatusOK)

	putResp := ddbCallWithAuth(t, srv, "PutItem", map[string]any{
		"TableName": "iam-allowed",
		"Item": map[string]any{
			"id": map[string]any{"S": "1"},
		},
	}, "AWS4-HMAC-SHA256 Credential=test/20260423/us-east-1/dynamodb/aws4_request, SignedHeaders=host;x-amz-date, Signature=abc")
	defer putResp.Body.Close()

	helpers.AssertStatus(t, putResp, http.StatusOK)
}

func TestIAMEnforceIntegration_dynamodbResourceScopedDeniesMismatchedTable(t *testing.T) {
	srv := helpers.NewTestServer(t,
		helpers.WithEnforceIAM(true),
	)
	seedIAMPrincipal(t, srv, "test",
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"dynamodb:CreateTable","Resource":"*"},{"Effect":"Allow","Action":"dynamodb:PutItem","Resource":"arn:aws:dynamodb:us-east-1:000000000000:table/iam-allowed"}]}`,
	)

	createResp := ddbCallWithAuth(t, srv, "CreateTable", map[string]any{
		"TableName":            "iam-other",
		"AttributeDefinitions": []map[string]any{{"AttributeName": "id", "AttributeType": "S"}},
		"KeySchema":            []map[string]any{{"AttributeName": "id", "KeyType": "HASH"}},
		"BillingMode":          "PAY_PER_REQUEST",
	}, "AWS4-HMAC-SHA256 Credential=test/20260423/us-east-1/dynamodb/aws4_request, SignedHeaders=host;x-amz-date, Signature=abc")
	defer createResp.Body.Close()
	helpers.AssertStatus(t, createResp, http.StatusOK)

	putResp := ddbCallWithAuth(t, srv, "PutItem", map[string]any{
		"TableName": "iam-other",
		"Item": map[string]any{
			"id": map[string]any{"S": "1"},
		},
	}, "AWS4-HMAC-SHA256 Credential=test/20260423/us-east-1/dynamodb/aws4_request, SignedHeaders=host;x-amz-date, Signature=abc")
	defer putResp.Body.Close()

	helpers.AssertStatus(t, putResp, http.StatusForbidden)
}

func TestIAMEnforceIntegration_ssmResourceScopedAllowsMatchingParameter(t *testing.T) {
	srv := helpers.NewTestServer(t,
		helpers.WithEnforceIAM(true),
	)
	seedIAMPrincipal(t, srv, "test",
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"ssm:PutParameter","Resource":"arn:aws:ssm:us-east-1:000000000000:parameter/iam/allowed"}]}`,
	)

	resp := ssmCallWithAuth(t, srv, "PutParameter", map[string]any{
		"Name":      "/iam/allowed",
		"Value":     "ok",
		"Type":      "String",
		"Overwrite": true,
	}, "AWS4-HMAC-SHA256 Credential=test/20260423/us-east-1/ssm/aws4_request, SignedHeaders=host;x-amz-date, Signature=abc")
	defer resp.Body.Close()

	helpers.AssertStatus(t, resp, http.StatusOK)
}

func TestIAMEnforceIntegration_ssmResourceScopedDeniesMismatchedParameter(t *testing.T) {
	srv := helpers.NewTestServer(t,
		helpers.WithEnforceIAM(true),
	)
	seedIAMPrincipal(t, srv, "test",
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"ssm:GetParameter","Resource":"arn:aws:ssm:us-east-1:000000000000:parameter/iam/allowed"}]}`,
	)

	resp := ssmCallWithAuth(t, srv, "GetParameter", map[string]any{
		"Name": "/iam/other",
	}, "AWS4-HMAC-SHA256 Credential=test/20260423/us-east-1/ssm/aws4_request, SignedHeaders=host;x-amz-date, Signature=abc")
	defer resp.Body.Close()

	helpers.AssertStatus(t, resp, http.StatusForbidden)
}

func TestIAMEnforceIntegration_notActionAllow_allowsNonExcludedAction(t *testing.T) {
	srv := helpers.NewTestServer(t,
		helpers.WithEnforceIAM(true),
	)
	seedIAMPrincipal(t, srv, "test",
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","NotAction":"sqs:DeleteQueue","Resource":"*"}]}`,
	)

	resp := sqsCallWithAuth(
		t,
		srv,
		"AmazonSQS.CreateQueue",
		map[string]any{"QueueName": "not-action-allow"},
		"AWS4-HMAC-SHA256 Credential=test/20260423/us-east-1/sqs/aws4_request, SignedHeaders=host;x-amz-date, Signature=abc",
	)
	defer resp.Body.Close()

	helpers.AssertStatus(t, resp, http.StatusOK)
}

func TestIAMEnforceIntegration_notActionAllow_deniesExcludedAction(t *testing.T) {
	srv := helpers.NewTestServer(t,
		helpers.WithEnforceIAM(true),
	)
	seedIAMPrincipal(t, srv, "test",
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","NotAction":"sqs:DeleteQueue","Resource":"*"}]}`,
	)

	resp := sqsCallWithAuth(
		t,
		srv,
		"AmazonSQS.DeleteQueue",
		map[string]any{"QueueUrl": "https://localhost/000000000000/not-action-allow"},
		"AWS4-HMAC-SHA256 Credential=test/20260423/us-east-1/sqs/aws4_request, SignedHeaders=host;x-amz-date, Signature=abc",
	)
	defer resp.Body.Close()

	helpers.AssertStatus(t, resp, http.StatusForbidden)
}

func TestIAMEnforceIntegration_notResourceAllow_allowsOtherResource(t *testing.T) {
	srv := helpers.NewTestServer(t,
		helpers.WithEnforceIAM(true),
	)
	seedIAMPrincipal(t, srv, "test",
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"sqs:DeleteQueue","NotResource":"arn:aws:sqs:us-east-1:000000000000:blocked-queue"}]}`,
	)

	resp := sqsCallWithAuth(
		t,
		srv,
		"AmazonSQS.DeleteQueue",
		map[string]any{"QueueUrl": "https://localhost/000000000000/other-queue"},
		"AWS4-HMAC-SHA256 Credential=test/20260423/us-east-1/sqs/aws4_request, SignedHeaders=host;x-amz-date, Signature=abc",
	)
	defer resp.Body.Close()

	helpers.AssertStatus(t, resp, http.StatusOK)
}

func TestIAMEnforceIntegration_notResourceAllow_deniesExcludedResource(t *testing.T) {
	srv := helpers.NewTestServer(t,
		helpers.WithEnforceIAM(true),
	)
	seedIAMPrincipal(t, srv, "test",
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"sqs:DeleteQueue","NotResource":"arn:aws:sqs:us-east-1:000000000000:blocked-queue"}]}`,
	)

	resp := sqsCallWithAuth(
		t,
		srv,
		"AmazonSQS.DeleteQueue",
		map[string]any{"QueueUrl": "https://localhost/000000000000/blocked-queue"},
		"AWS4-HMAC-SHA256 Credential=test/20260423/us-east-1/sqs/aws4_request, SignedHeaders=host;x-amz-date, Signature=abc",
	)
	defer resp.Body.Close()

	helpers.AssertStatus(t, resp, http.StatusForbidden)
}

// TestIAMEnforceIntegration_condition_numericLessThan_allowsRequest verifies
// that a policy using NumericLessThan on aws:RequestedContentLength allows a
// request whose body is smaller than the policy threshold.
func TestIAMEnforceIntegration_condition_numericLessThan_allowsRequest(t *testing.T) {
	srv := helpers.NewTestServer(t,
		helpers.WithEnforceIAM(true),
	)
	// Allow requests with Content-Length < 10000 bytes.
	seedIAMPrincipal(t, srv, "test",
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"sqs:CreateQueue","Resource":"*","Condition":{"NumericLessThan":{"aws:RequestedContentLength":"10000"}}}]}`,
	)

	resp := sqsCallWithAuth(
		t,
		srv,
		"AmazonSQS.CreateQueue",
		map[string]any{"QueueName": "numeric-cond-test"},
		"AWS4-HMAC-SHA256 Credential=test/20260423/us-east-1/sqs/aws4_request, SignedHeaders=host;x-amz-date, Signature=abc",
	)
	defer resp.Body.Close()

	helpers.AssertStatus(t, resp, http.StatusOK)
}

// TestIAMEnforceIntegration_policyVariable_username_allowsOwnResource verifies
// that ${aws:username} in a Resource ARN is expanded to the calling user's name
// before matching, allowing access to their own resource.
func TestIAMEnforceIntegration_policyVariable_username_allowsOwnResource(t *testing.T) {
	srv := helpers.NewTestServer(t,
		helpers.WithEnforceIAM(true),
	)
	// Policy grants access to queues prefixed with the caller's username.
	seedIAMPrincipal(t, srv, "alice",
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"sqs:CreateQueue","Resource":"arn:aws:sqs:us-east-1:000000000000:${aws:username}-*"}]}`,
	)

	resp := sqsCallWithAuth(
		t,
		srv,
		"AmazonSQS.CreateQueue",
		map[string]any{"QueueName": "alice-myqueue"},
		"AWS4-HMAC-SHA256 Credential=alice/20260423/us-east-1/sqs/aws4_request, SignedHeaders=host;x-amz-date, Signature=abc",
	)
	defer resp.Body.Close()

	helpers.AssertStatus(t, resp, http.StatusOK)
}

// TestIAMEnforceIntegration_policyVariable_username_deniesOtherResource
// verifies that the ${aws:username} expansion prevents a caller from accessing
// another user's resource prefix.
func TestIAMEnforceIntegration_policyVariable_username_deniesOtherResource(t *testing.T) {
	srv := helpers.NewTestServer(t,
		helpers.WithEnforceIAM(true),
	)
	seedIAMPrincipal(t, srv, "alice",
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"sqs:CreateQueue","Resource":"arn:aws:sqs:us-east-1:000000000000:${aws:username}-*"}]}`,
	)

	resp := sqsCallWithAuth(
		t,
		srv,
		"AmazonSQS.CreateQueue",
		map[string]any{"QueueName": "bob-myqueue"},
		"AWS4-HMAC-SHA256 Credential=alice/20260423/us-east-1/sqs/aws4_request, SignedHeaders=host;x-amz-date, Signature=abc",
	)
	defer resp.Body.Close()

	helpers.AssertStatus(t, resp, http.StatusForbidden)
}

func TestIAMEnforceIntegration_kmsResourceScopedDeniesMismatchedKeyID(t *testing.T) {
	srv := helpers.NewTestServer(t,
		helpers.WithEnforceIAM(true),
	)
	seedIAMPrincipal(t, srv, "test",
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"kms:Encrypt","Resource":"arn:aws:kms:us-east-1:000000000000:key/allowed-key"}]}`,
	)

	resp := kmsCallWithAuth(
		t,
		srv,
		"Encrypt",
		map[string]any{"KeyId": "other-key", "Plaintext": "AQ=="},
		"AWS4-HMAC-SHA256 Credential=test/20260423/us-east-1/kms/aws4_request, SignedHeaders=host;x-amz-date, Signature=abc",
	)
	defer resp.Body.Close()

	helpers.AssertStatus(t, resp, http.StatusForbidden)
}

func TestIAMEnforceIntegration_kinesisResourceScopedDeniesMismatchedStreamName(t *testing.T) {
	srv := helpers.NewTestServer(t,
		helpers.WithEnforceIAM(true),
	)
	seedIAMPrincipal(t, srv, "test",
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"kinesis:DescribeStream","Resource":"arn:aws:kinesis:us-east-1:000000000000:stream/allowed-stream"}]}`,
	)

	resp := kinesisCallWithAuth(
		t,
		srv,
		"DescribeStream",
		map[string]any{"StreamName": "other-stream"},
		"AWS4-HMAC-SHA256 Credential=test/20260423/us-east-1/kinesis/aws4_request, SignedHeaders=host;x-amz-date, Signature=abc",
	)
	defer resp.Body.Close()

	helpers.AssertStatus(t, resp, http.StatusForbidden)
}

func TestIAMEnforceIntegration_firehoseResourceScopedDeniesMismatchedDeliveryStreamName(t *testing.T) {
	srv := helpers.NewTestServer(t,
		helpers.WithEnforceIAM(true),
	)
	seedIAMPrincipal(t, srv, "test",
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"firehose:DescribeDeliveryStream","Resource":"arn:aws:firehose:us-east-1:000000000000:deliverystream/allowed-stream"}]}`,
	)

	resp := firehoseCallWithAuth(
		t,
		srv,
		"DescribeDeliveryStream",
		map[string]any{"DeliveryStreamName": "other-stream"},
		"AWS4-HMAC-SHA256 Credential=test/20260423/us-east-1/firehose/aws4_request, SignedHeaders=host;x-amz-date, Signature=abc",
	)
	defer resp.Body.Close()

	helpers.AssertStatus(t, resp, http.StatusForbidden)
}

func TestIAMEnforceIntegration_logsResourceScopedDeniesMismatchedLogStream(t *testing.T) {
	srv := helpers.NewTestServer(t,
		helpers.WithEnforceIAM(true),
	)
	seedIAMPrincipal(t, srv, "test",
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"logs:PutLogEvents","Resource":"arn:aws:logs:us-east-1:000000000000:log-group:allowed-group:log-stream:allowed-stream"}]}`,
	)

	resp := logsCallWithAuth(
		t,
		srv,
		"PutLogEvents",
		map[string]any{"logGroupName": "allowed-group", "logStreamName": "other-stream", "logEvents": []any{}},
		"AWS4-HMAC-SHA256 Credential=test/20260423/us-east-1/logs/aws4_request, SignedHeaders=host;x-amz-date, Signature=abc",
	)
	defer resp.Body.Close()

	helpers.AssertStatus(t, resp, http.StatusForbidden)
}

func TestIAMEnforceIntegration_ecrResourceScopedDeniesMismatchedRepositoryName(t *testing.T) {
	srv := helpers.NewTestServer(t,
		helpers.WithEnforceIAM(true),
	)
	seedIAMPrincipal(t, srv, "test",
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"ecr:CreateRepository","Resource":"arn:aws:ecr:us-east-1:000000000000:repository/allowed-repo"}]}`,
	)

	resp := ecrCallWithAuth(
		t,
		srv,
		"CreateRepository",
		map[string]any{"repositoryName": "other-repo"},
		"AWS4-HMAC-SHA256 Credential=test/20260423/us-east-1/ecr/aws4_request, SignedHeaders=host;x-amz-date, Signature=abc",
	)
	defer resp.Body.Close()

	helpers.AssertStatus(t, resp, http.StatusForbidden)
}

func TestIAMEnforceIntegration_secretsManagerResourceScopedDeniesMismatchedSecretID(t *testing.T) {
	srv := helpers.NewTestServer(t,
		helpers.WithEnforceIAM(true),
	)
	seedIAMPrincipal(t, srv, "test",
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"secretsmanager:GetSecretValue","Resource":"arn:aws:secretsmanager:us-east-1:000000000000:secret:allowed-secret"}]}`,
	)

	resp := secretsManagerCallWithAuth(
		t,
		srv,
		"GetSecretValue",
		map[string]any{"SecretId": "other-secret"},
		"AWS4-HMAC-SHA256 Credential=test/20260423/us-east-1/secretsmanager/aws4_request, SignedHeaders=host;x-amz-date, Signature=abc",
	)
	defer resp.Body.Close()

	helpers.AssertStatus(t, resp, http.StatusForbidden)
}

func TestIAMEnforceIntegration_stepFunctionsResourceScopedDeniesMismatchedStateMachineArn(t *testing.T) {
	srv := helpers.NewTestServer(t,
		helpers.WithEnforceIAM(true),
	)
	seedIAMPrincipal(t, srv, "test",
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"stepfunctions:StartExecution","Resource":"arn:aws:states:us-east-1:000000000000:stateMachine:allowed-sm"}]}`,
	)

	resp := stepFunctionsCallWithAuth(
		t,
		srv,
		"StartExecution",
		map[string]any{"stateMachineArn": "arn:aws:states:us-east-1:000000000000:stateMachine:other-sm", "input": "{}"},
		"AWS4-HMAC-SHA256 Credential=test/20260423/us-east-1/stepfunctions/aws4_request, SignedHeaders=host;x-amz-date, Signature=abc",
	)
	defer resp.Body.Close()

	helpers.AssertStatus(t, resp, http.StatusForbidden)
}

func TestIAMEnforceIntegration_cloudFormationResourceScopedDeniesMismatchedStackName(t *testing.T) {
	srv := helpers.NewTestServer(t,
		helpers.WithEnforceIAM(true),
	)
	seedIAMPrincipal(t, srv, "test",
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"cloudformation:CreateStack","Resource":"arn:aws:cloudformation:us-east-1:000000000000:stack/allowed-stack/*"}]}`,
	)

	vals := url.Values{}
	vals.Set("Action", "CreateStack")
	vals.Set("StackName", "other-stack")
	vals.Set("TemplateBody", "{\"Resources\":{}}")

	resp := cloudFormationCallWithAuth(
		t,
		srv,
		vals,
		"AWS4-HMAC-SHA256 Credential=test/20260423/us-east-1/cloudformation/aws4_request, SignedHeaders=host;x-amz-date, Signature=abc",
	)
	defer resp.Body.Close()

	helpers.AssertStatus(t, resp, http.StatusForbidden)
}

func TestIAMEnforceIntegration_ecsResourceScopedDeniesMismatchedCluster(t *testing.T) {
	srv := helpers.NewTestServer(t,
		helpers.WithEnforceIAM(true),
	)
	seedIAMPrincipal(t, srv, "test",
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"ecs:DeleteCluster","Resource":"arn:aws:ecs:us-east-1:000000000000:cluster/allowed-cluster"}]}`,
	)

	resp := ecsCallWithAuth(
		t,
		srv,
		"DeleteCluster",
		map[string]any{"cluster": "other-cluster"},
		"AWS4-HMAC-SHA256 Credential=test/20260423/us-east-1/ecs/aws4_request, SignedHeaders=host;x-amz-date, Signature=abc",
	)
	defer resp.Body.Close()

	helpers.AssertStatus(t, resp, http.StatusForbidden)
}

func TestIAMEnforceIntegration_lambdaResourceScopedDeniesMismatchedInvokeFunction(t *testing.T) {
	srv := helpers.NewTestServer(t,
		helpers.WithEnforceIAM(true),
	)
	seedIAMPrincipal(t, srv, "test",
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"lambda:InvokeFunction","Resource":"arn:aws:lambda:us-east-1:000000000000:function:allowed-fn"}]}`,
	)

	resp := lambdaCallWithAuth(
		t,
		srv,
		http.MethodPost,
		"/2015-03-31/functions/other-fn/invocations",
		map[string]any{},
		"AWS4-HMAC-SHA256 Credential=test/20260423/us-east-1/lambda/aws4_request, SignedHeaders=host;x-amz-date, Signature=abc",
	)
	defer resp.Body.Close()

	helpers.AssertStatus(t, resp, http.StatusForbidden)
}

func TestIAMEnforceIntegration_cloudWatchResourceScopedDeniesMismatchedAlarmName(t *testing.T) {
	srv := helpers.NewTestServer(t,
		helpers.WithEnforceIAM(true),
	)
	seedIAMPrincipal(t, srv, "test",
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"cloudwatch:DeleteAlarms","Resource":"arn:aws:cloudwatch:us-east-1:000000000000:alarm:allowed-alarm"}]}`,
	)

	vals := url.Values{}
	vals.Set("Action", "DeleteAlarms")
	vals.Set("Version", "2010-08-01")
	vals.Set("AlarmNames.member.1", "other-alarm")

	resp := queryCallWithAuthValues(
		t,
		srv,
		vals,
		"AWS4-HMAC-SHA256 Credential=test/20260423/us-east-1/cloudwatch/aws4_request, SignedHeaders=host;x-amz-date, Signature=abc",
	)
	defer resp.Body.Close()

	helpers.AssertStatus(t, resp, http.StatusForbidden)
}

func TestIAMEnforceIntegration_lambdaAliasResourceScopedDeniesMismatchedFunction(t *testing.T) {
	srv := helpers.NewTestServer(t,
		helpers.WithEnforceIAM(true),
	)
	seedIAMPrincipal(t, srv, "test",
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"lambda:CreateAlias","Resource":"arn:aws:lambda:us-east-1:000000000000:function:allowed-fn"}]}`,
	)

	resp := lambdaCallWithAuth(
		t,
		srv,
		http.MethodPost,
		"/2015-03-31/functions/other-fn/aliases",
		map[string]any{"Name": "live", "FunctionVersion": "1"},
		"AWS4-HMAC-SHA256 Credential=test/20260423/us-east-1/lambda/aws4_request, SignedHeaders=host;x-amz-date, Signature=abc",
	)
	defer resp.Body.Close()

	helpers.AssertStatus(t, resp, http.StatusForbidden)
}

func TestIAMEnforceIntegration_lambdaResponseStreamingResourceScopedDeniesMismatchedFunction(t *testing.T) {
	srv := helpers.NewTestServer(t,
		helpers.WithEnforceIAM(true),
	)
	seedIAMPrincipal(t, srv, "test",
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"lambda:InvokeFunction","Resource":"arn:aws:lambda:us-east-1:000000000000:function:allowed-fn"}]}`,
	)

	resp := lambdaCallWithAuth(
		t,
		srv,
		http.MethodPost,
		"/2021-11-15/functions/other-fn/response-streaming-invocations",
		map[string]any{},
		"AWS4-HMAC-SHA256 Credential=test/20260423/us-east-1/lambda/aws4_request, SignedHeaders=host;x-amz-date, Signature=abc",
	)
	defer resp.Body.Close()

	helpers.AssertStatus(t, resp, http.StatusForbidden)
}

func TestIAMEnforceIntegration_lambdaTestEventResourceScopedDeniesMismatchedFunction(t *testing.T) {
	srv := helpers.NewTestServer(t,
		helpers.WithEnforceIAM(true),
	)
	seedIAMPrincipal(t, srv, "test",
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"lambda:PutTestEvent","Resource":"arn:aws:lambda:us-east-1:000000000000:function:allowed-fn"}]}`,
	)

	resp := lambdaCallWithAuth(
		t,
		srv,
		http.MethodPut,
		"/_overcast/lambda/functions/other-fn/test-events/demo",
		map[string]any{"Body": "{}"},
		"AWS4-HMAC-SHA256 Credential=test/20260423/us-east-1/lambda/aws4_request, SignedHeaders=host;x-amz-date, Signature=abc",
	)
	defer resp.Body.Close()

	helpers.AssertStatus(t, resp, http.StatusForbidden)
}

func TestIAMEnforceIntegration_lambdaLayerVersionResourceScopedDeniesMismatchedLayer(t *testing.T) {
	srv := helpers.NewTestServer(t,
		helpers.WithEnforceIAM(true),
	)
	seedIAMPrincipal(t, srv, "test",
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"lambda:GetLayerVersion","Resource":"arn:aws:lambda:us-east-1:000000000000:layer:allowed-layer:1"}]}`,
	)

	resp := lambdaCallWithAuth(
		t,
		srv,
		http.MethodGet,
		"/2018-10-31/layers/other-layer/versions/1",
		nil,
		"AWS4-HMAC-SHA256 Credential=test/20260423/us-east-1/lambda/aws4_request, SignedHeaders=host;x-amz-date, Signature=abc",
	)
	defer resp.Body.Close()

	helpers.AssertStatus(t, resp, http.StatusForbidden)
}

func TestIAMEnforceIntegration_pipesResourceScopedDeniesMismatchedPipe(t *testing.T) {
	srv := helpers.NewTestServer(t,
		helpers.WithEnforceIAM(true),
	)
	seedIAMPrincipal(t, srv, "test",
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"pipes:DescribePipe","Resource":"arn:aws:pipes:us-east-1:000000000000:pipe/allowed-pipe"}]}`,
	)

	resp := pipesCallWithAuth(
		t,
		srv,
		http.MethodGet,
		"/v1/pipes/other-pipe",
		nil,
		"AWS4-HMAC-SHA256 Credential=test/20260423/us-east-1/pipes/aws4_request, SignedHeaders=host;x-amz-date, Signature=abc",
	)
	defer resp.Body.Close()

	helpers.AssertStatus(t, resp, http.StatusForbidden)
}

// TestIAMEnforceIntegration_s3PutObjectKeepsFormEncodedBody covers the second
// place that read a request body before routing decided who owned it. IAM
// enforcement resolves a Query Action out of a form-encoded body, and it runs
// on every request when enabled — so with enforcement on, an S3 PutObject sent
// with the default content type of a bare HTTP client used to be authorized
// correctly and then stored as zero bytes.
func TestIAMEnforceIntegration_s3PutObjectKeepsFormEncodedBody(t *testing.T) {
	// Given: enforcement on, and a principal allowed to write to S3
	srv := helpers.NewTestServer(t,
		helpers.WithEnforceIAM(true),
	)
	seedIAMPrincipal(t, srv, "test",
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"s3:*","Resource":"*"}]}`,
	)
	const auth = "AWS4-HMAC-SHA256 Credential=test/20260423/us-east-1/s3/aws4_request, " +
		"SignedHeaders=host;x-amz-date, Signature=abc"

	createResp := s3CallWithAuth(t, srv, http.MethodPut, "/iam-bodycheck", auth)
	createResp.Body.Close()
	helpers.AssertStatus(t, createResp, http.StatusOK)

	// When: an object is written with the form content type
	body := "hello-body-check"
	req, err := http.NewRequest(http.MethodPut, srv.URL+"/iam-bodycheck/k1", strings.NewReader(body))
	if err != nil {
		t.Fatalf("build s3 put: %v", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Authorization", auth)
	req.Header.Set("X-Amz-Date", "20260423T000000Z")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do s3 put: %v", err)
	}
	resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)

	// Then: the bytes survived enforcement
	got := s3CallWithAuth(t, srv, http.MethodGet, "/iam-bodycheck/k1", auth)
	defer got.Body.Close()
	helpers.AssertStatus(t, got, http.StatusOK)
	stored, err := io.ReadAll(got.Body)
	if err != nil {
		t.Fatal(err)
	}
	if string(stored) != body {
		t.Errorf("stored body = %q, want %q", stored, body)
	}
}
