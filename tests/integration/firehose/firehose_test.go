// Package firehose_test contains integration tests for the Kinesis Data Firehose emulator.
//
// Run: go test ./tests/integration/firehose/...
package firehose_test

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/overcast-sh/overcast/tests/helpers"
)

// fhCall performs a Firehose JSON 1.1 dispatch request.
func fhCall(t *testing.T, srv *helpers.TestServer, operation string, body map[string]any) *http.Response {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal %s body: %v", operation, err)
	}
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/x-amz-json-1.1")
	req.Header.Set("X-Amz-Target", "Firehose_20150804."+operation)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("fhCall %s: %v", operation, err)
	}
	return resp
}

func createStream(t *testing.T, srv *helpers.TestServer, name string) string {
	t.Helper()
	resp := fhCall(t, srv, "CreateDeliveryStream", map[string]any{
		"DeliveryStreamName": name,
	})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		DeliveryStreamARN string `json:"DeliveryStreamARN"`
	}
	helpers.DecodeJSON(t, resp, &result)
	if result.DeliveryStreamARN == "" {
		t.Fatal("expected DeliveryStreamARN to be set")
	}
	return result.DeliveryStreamARN
}

// ─── CreateDeliveryStream ─────────────────────────────────────────────────────

func TestCreateDeliveryStream_success(t *testing.T) {
	// Given: an empty store
	srv := helpers.NewTestServer(t)

	// When: CreateDeliveryStream is called
	arn := createStream(t, srv, "test-stream")

	// Then: a valid ARN is returned
	if arn == "" {
		t.Error("expected DeliveryStreamARN to be non-empty")
	}
}

// ─── DescribeDeliveryStream ───────────────────────────────────────────────────

func TestDescribeDeliveryStream_success(t *testing.T) {
	// Given: a delivery stream exists
	srv := helpers.NewTestServer(t)
	createStream(t, srv, "test-stream")

	// When: DescribeDeliveryStream is called
	resp := fhCall(t, srv, "DescribeDeliveryStream", map[string]any{
		"DeliveryStreamName": "test-stream",
	})
	defer resp.Body.Close()

	// Then: 200 with stream description
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		DeliveryStreamDescription struct {
			DeliveryStreamName string `json:"DeliveryStreamName"`
		} `json:"DeliveryStreamDescription"`
	}
	helpers.DecodeJSON(t, resp, &result)
	if result.DeliveryStreamDescription.DeliveryStreamName != "test-stream" {
		t.Errorf("expected DeliveryStreamName=test-stream, got %q",
			result.DeliveryStreamDescription.DeliveryStreamName)
	}
}

// ─── ListDeliveryStreams ──────────────────────────────────────────────────────

func TestListDeliveryStreams_success(t *testing.T) {
	// Given: a delivery stream exists
	srv := helpers.NewTestServer(t)
	createStream(t, srv, "test-stream")

	// When: ListDeliveryStreams is called
	resp := fhCall(t, srv, "ListDeliveryStreams", map[string]any{})
	defer resp.Body.Close()

	// Then: 200 with at least 1 stream
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		DeliveryStreamNames []string `json:"DeliveryStreamNames"`
	}
	helpers.DecodeJSON(t, resp, &result)
	if len(result.DeliveryStreamNames) < 1 {
		t.Error("expected at least 1 delivery stream")
	}
}

// ─── DeleteDeliveryStream ─────────────────────────────────────────────────────

func TestDeleteDeliveryStream_success(t *testing.T) {
	// Given: a delivery stream exists
	srv := helpers.NewTestServer(t)
	createStream(t, srv, "test-stream")

	// When: DeleteDeliveryStream is called
	del := fhCall(t, srv, "DeleteDeliveryStream", map[string]any{
		"DeliveryStreamName": "test-stream",
	})
	defer del.Body.Close()
	helpers.AssertStatus(t, del, http.StatusOK)

	// Then: DescribeDeliveryStream reports ResourceNotFoundException (HTTP 400)
	resp := fhCall(t, srv, "DescribeDeliveryStream", map[string]any{
		"DeliveryStreamName": "test-stream",
	})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
}

// ─── PutRecord ────────────────────────────────────────────────────────────────

func TestPutRecord_success(t *testing.T) {
	// Given: a delivery stream exists
	srv := helpers.NewTestServer(t)
	createStream(t, srv, "test-stream")

	// When: PutRecord is called
	resp := fhCall(t, srv, "PutRecord", map[string]any{
		"DeliveryStreamName": "test-stream",
		"Record": map[string]any{
			"Data": base64.StdEncoding.EncodeToString([]byte("hello world")),
		},
	})
	defer resp.Body.Close()

	// Then: 200 with RecordId
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		RecordId string `json:"RecordId"`
	}
	helpers.DecodeJSON(t, resp, &result)
	if result.RecordId == "" {
		t.Error("expected RecordId to be set")
	}
}
