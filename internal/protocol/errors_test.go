package protocol_test

import (
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/overcast-sh/overcast/internal/protocol"
	"github.com/overcast-sh/overcast/internal/state"
)

// TestWriteXMLError_structuredResponse verifies the XML error envelope is correct.
func TestWriteXMLError_structuredResponse(t *testing.T) {
	// Given: a handler that writes an XML error
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		protocol.WriteXMLError(w, r, &protocol.AWSError{
			Code:       "NoSuchBucket",
			Message:    "The bucket does not exist",
			HTTPStatus: http.StatusNotFound,
		})
	})

	// When: we make a request
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	ctx := protocol.ContextWithRequestID(req.Context(), "test-request-id")
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	resp := w.Result()

	// Then: status, Content-Type, request ID, and body are correct
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status: expected 404, got %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/xml" {
		t.Errorf("Content-Type: expected application/xml, got %q", ct)
	}
	if rid := resp.Header.Get("x-amz-request-id"); rid != "test-request-id" {
		t.Errorf("x-amz-request-id: expected test-request-id, got %q", rid)
	}

	body, _ := io.ReadAll(resp.Body)
	var errResp struct {
		Code    string `xml:"Code"`
		Message string `xml:"Message"`
	}
	if err := xml.Unmarshal(body, &errResp); err != nil {
		t.Fatalf("failed to parse XML error: %v\nbody: %s", err, body)
	}
	if errResp.Code != "NoSuchBucket" {
		t.Errorf("Code: expected NoSuchBucket, got %q", errResp.Code)
	}
}

// TestWriteJSONError_structuredResponse verifies the JSON error envelope is correct.
func TestWriteJSONError_structuredResponse(t *testing.T) {
	// Given: a handler that writes a JSON error
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		protocol.WriteJSONError(w, r, &protocol.AWSError{
			Code:       "QueueDoesNotExist",
			Message:    "The queue does not exist",
			HTTPStatus: http.StatusBadRequest,
		})
	})

	// When: we make a request
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	ctx := protocol.ContextWithRequestID(req.Context(), "json-request-id")
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	resp := w.Result()

	// Then: the JSON body has __type and message fields
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status: expected 400, got %d", resp.StatusCode)
	}
	if rid := resp.Header.Get("x-amzn-requestid"); rid != "json-request-id" {
		t.Errorf("x-amzn-requestid: expected json-request-id, got %q", rid)
	}

	body, _ := io.ReadAll(resp.Body)
	var errResp struct {
		Type    string `json:"__type"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(body, &errResp); err != nil {
		t.Fatalf("failed to parse JSON error: %v\nbody: %s", err, body)
	}
	if errResp.Type != "QueueDoesNotExist" {
		t.Errorf("__type: expected QueueDoesNotExist, got %q", errResp.Type)
	}
}

// TestWriteJSONError_queryErrorCodeHeader is the writer-level counterpart of
// #1810: WriteJSONError renders x-amzn-query-error only when AWSError.
// QueryErrorCode is set (the field every service but SQS leaves empty), and
// picks "Sender" vs "Receiver" from HTTPStatus the way the
// aws.protocols#awsQueryCompatible protocol does.
func TestWriteJSONError_queryErrorCodeHeader(t *testing.T) {
	cases := []struct {
		name string
		aerr *protocol.AWSError
		want string // "" means the header must be absent entirely
	}{
		{
			name: "unset QueryErrorCode omits the header",
			aerr: &protocol.AWSError{Code: "InternalError", Message: "boom", HTTPStatus: http.StatusInternalServerError},
			want: "",
		},
		{
			name: "4xx is Sender",
			aerr: &protocol.AWSError{Code: "X", Message: "boom", HTTPStatus: http.StatusBadRequest, QueryErrorCode: "AWS.SimpleQueueService.NonExistentQueue"},
			want: "AWS.SimpleQueueService.NonExistentQueue;Sender",
		},
		{
			name: "5xx is Receiver",
			aerr: &protocol.AWSError{Code: "InternalError", Message: "boom", HTTPStatus: http.StatusInternalServerError, QueryErrorCode: "InternalError"},
			want: "InternalError;Receiver",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/", nil)
			protocol.WriteJSONError(w, req, tc.aerr)

			got := w.Result().Header.Get("x-amzn-query-error")
			if tc.want == "" {
				if got != "" {
					t.Errorf("x-amzn-query-error = %q, want absent", got)
				}
				return
			}
			if got != tc.want {
				t.Errorf("x-amzn-query-error = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestWrap_preservesQueryErrorCode confirms Wrap carries QueryErrorCode from
// its template like every other field, so a wrapped SQS sentinel keeps
// rendering the header.
func TestWrap_preservesQueryErrorCode(t *testing.T) {
	template := &protocol.AWSError{
		Code:           "AWS.SimpleQueueService.NonExistentQueue",
		Message:        "boom",
		HTTPStatus:     http.StatusBadRequest,
		QueryErrorCode: "AWS.SimpleQueueService.NonExistentQueue",
	}
	wrapped := protocol.Wrap(template, errors.New("cause"))

	if wrapped.QueryErrorCode != template.QueryErrorCode {
		t.Errorf("QueryErrorCode = %q, want %q", wrapped.QueryErrorCode, template.QueryErrorCode)
	}
}

// TestWriteJSONError_reasonField is the writer-level counterpart of
// Organizations' InvalidInputException.Reason: WriteJSONError renders a
// "Reason" member only when AWSError.Reason is set (the field every service
// without a modeled Reason-shaped member leaves empty), and omits it
// entirely otherwise rather than sending an empty string AWS never sends.
func TestWriteJSONError_reasonField(t *testing.T) {
	cases := []struct {
		name string
		aerr *protocol.AWSError
		want string // "" means the field must be absent from the body entirely
	}{
		{
			name: "unset Reason omits the field",
			aerr: &protocol.AWSError{Code: "InternalError", Message: "boom", HTTPStatus: http.StatusInternalServerError},
			want: "",
		},
		{
			name: "set Reason is rendered verbatim",
			aerr: &protocol.AWSError{Code: "InvalidInputException", Message: "boom", HTTPStatus: http.StatusBadRequest, Reason: "MAX_VALUE_EXCEEDED"},
			want: "MAX_VALUE_EXCEEDED",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/", nil)
			protocol.WriteJSONError(w, req, tc.aerr)

			body, _ := io.ReadAll(w.Result().Body)
			var raw map[string]any
			if err := json.Unmarshal(body, &raw); err != nil {
				t.Fatalf("decoding body %s: %v", body, err)
			}
			if tc.want == "" {
				if _, present := raw["Reason"]; present {
					t.Errorf("body %s carries a Reason field, want it absent", body)
				}
				return
			}
			if got, _ := raw["Reason"].(string); got != tc.want {
				t.Errorf("Reason = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestWrap_preservesReason confirms Wrap carries Reason from its template
// like every other field, so a wrapped Organizations InvalidInputException
// keeps its modeled Reason.
func TestWrap_preservesReason(t *testing.T) {
	template := &protocol.AWSError{
		Code:       "InvalidInputException",
		Message:    "boom",
		HTTPStatus: http.StatusBadRequest,
		Reason:     "MIN_VALUE_EXCEEDED",
	}
	wrapped := protocol.Wrap(template, errors.New("cause"))

	if wrapped.Reason != template.Reason {
		t.Errorf("Reason = %q, want %q", wrapped.Reason, template.Reason)
	}
}

// TestNotImplemented_setsUnsupportedHeader verifies the 501 sentinel header.
func TestNotImplemented_setsUnsupportedHeader(t *testing.T) {
	// Given: a handler that returns NotImplemented
	handler := http.HandlerFunc(protocol.NotImplementedJSON)

	// When: we call it
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req = req.WithContext(protocol.ContextWithRequestID(req.Context(), "req-1"))
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	resp := w.Result()

	// Then: status is 501 and x-emulator-unsupported is true
	if resp.StatusCode != http.StatusNotImplemented {
		t.Errorf("expected 501, got %d", resp.StatusCode)
	}
	if got := resp.Header.Get("x-emulator-unsupported"); got != "true" {
		t.Errorf("expected x-emulator-unsupported: true, got %q", got)
	}
}

// ---- Storage-pressure -> AWS throttling remap (storage-pressure-handling item 1) ----

// pressureAWSError builds the error shape a service store layer actually
// produces under storage pressure: protocol.Wrap(protocol.ErrInternalError,
// cause) where cause carries state.ErrStorePressure in its chain (exactly
// what HybridStore.wrapStorePressure attaches — see internal/state/hybrid.go).
func pressureAWSError() *protocol.AWSError {
	cause := fmt.Errorf("hybrid sqlite read retry exhausted: %w: %w", state.ErrStorePressure, errors.New("context deadline exceeded"))
	return protocol.Wrap(protocol.ErrInternalError, cause)
}

// plainAWSError builds an ordinary InternalError with a cause that is NOT
// storage pressure — the negative case every family must leave untouched.
func plainAWSError() *protocol.AWSError {
	return protocol.Wrap(protocol.ErrInternalError, errors.New("disk I/O error"))
}

func TestWriteXMLError_storagePressureRemapsToSlowDown(t *testing.T) {
	// Given: a storage-pressure error reaching the S3/XML error writer
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		protocol.WriteXMLError(w, r, pressureAWSError())
	})
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req = req.WithContext(protocol.ContextWithRequestID(req.Context(), "req-1"))
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	resp := w.Result()

	// Then: the client sees S3's real throttling shape, not InternalError
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("status: expected 503, got %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	var errResp struct {
		Code string `xml:"Code"`
	}
	if err := xml.Unmarshal(body, &errResp); err != nil {
		t.Fatalf("failed to parse XML error: %v\nbody: %s", err, body)
	}
	if errResp.Code != "SlowDown" {
		t.Errorf("Code: expected SlowDown, got %q", errResp.Code)
	}
}

func TestWriteXMLError_plainErrorStaysInternalError(t *testing.T) {
	// Given: a plain (non-pressure) internal error
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		protocol.WriteXMLError(w, r, plainAWSError())
	})
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req = req.WithContext(protocol.ContextWithRequestID(req.Context(), "req-1"))
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	resp := w.Result()

	// Then: it must NOT be remapped to a throttling shape
	if resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("status: expected 500, got %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	var errResp struct {
		Code string `xml:"Code"`
	}
	if err := xml.Unmarshal(body, &errResp); err != nil {
		t.Fatalf("failed to parse XML error: %v\nbody: %s", err, body)
	}
	if errResp.Code != "InternalError" {
		t.Errorf("Code: expected InternalError, got %q", errResp.Code)
	}
}

func TestWriteJSONError_storagePressureRemapsToThrottlingException(t *testing.T) {
	// Given: a storage-pressure error reaching a JSON-protocol error writer
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		protocol.WriteJSONError(w, r, pressureAWSError())
	})
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req = req.WithContext(protocol.ContextWithRequestID(req.Context(), "req-1"))
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	resp := w.Result()

	// Then: the client sees the standard JSON-protocol throttling shape
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status: expected 400, got %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	var errResp struct {
		Type string `json:"__type"`
	}
	if err := json.Unmarshal(body, &errResp); err != nil {
		t.Fatalf("failed to parse JSON error: %v\nbody: %s", err, body)
	}
	if errResp.Type != "ThrottlingException" {
		t.Errorf("__type: expected ThrottlingException, got %q", errResp.Type)
	}
}

func TestWriteJSONError_plainErrorStaysInternalError(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		protocol.WriteJSONError(w, r, plainAWSError())
	})
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req = req.WithContext(protocol.ContextWithRequestID(req.Context(), "req-1"))
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	resp := w.Result()

	if resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("status: expected 500, got %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	var errResp struct {
		Type string `json:"__type"`
	}
	if err := json.Unmarshal(body, &errResp); err != nil {
		t.Fatalf("failed to parse JSON error: %v\nbody: %s", err, body)
	}
	if errResp.Type != "InternalError" {
		t.Errorf("__type: expected InternalError, got %q", errResp.Type)
	}
}

func TestWriteQueryXMLError_storagePressureRemapsToThrottling(t *testing.T) {
	// Given: a storage-pressure error reaching the Query-protocol error writer
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		protocol.WriteQueryXMLError(w, r, pressureAWSError())
	})
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req = req.WithContext(protocol.ContextWithRequestID(req.Context(), "req-1"))
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	resp := w.Result()

	// Then: the client sees AWS's general Query-protocol throttling shape
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status: expected 400, got %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	var errResp struct {
		Error struct {
			Code string `xml:"Code"`
		} `xml:"Error"`
	}
	if err := xml.Unmarshal(body, &errResp); err != nil {
		t.Fatalf("failed to parse Query XML error: %v\nbody: %s", err, body)
	}
	if errResp.Error.Code != "Throttling" {
		t.Errorf("Code: expected Throttling, got %q", errResp.Error.Code)
	}
}

func TestWriteQueryXMLError_plainErrorStaysInternalError(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		protocol.WriteQueryXMLError(w, r, plainAWSError())
	})
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req = req.WithContext(protocol.ContextWithRequestID(req.Context(), "req-1"))
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	resp := w.Result()

	if resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("status: expected 500, got %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	var errResp struct {
		Error struct {
			Code string `xml:"Code"`
		} `xml:"Error"`
	}
	if err := xml.Unmarshal(body, &errResp); err != nil {
		t.Fatalf("failed to parse Query XML error: %v\nbody: %s", err, body)
	}
	if errResp.Error.Code != "InternalError" {
		t.Errorf("Code: expected InternalError, got %q", errResp.Error.Code)
	}
}

func TestWriteEC2QueryXMLError_storagePressureRemapsToThrottling(t *testing.T) {
	// Given: a storage-pressure error reaching the EC2 Query-protocol error writer
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		protocol.WriteEC2QueryXMLError(w, r, pressureAWSError())
	})
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req = req.WithContext(protocol.ContextWithRequestID(req.Context(), "req-1"))
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	resp := w.Result()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status: expected 400, got %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	var errResp struct {
		Errors []struct {
			Code string `xml:"Code"`
		} `xml:"Errors>Error"`
	}
	if err := xml.Unmarshal(body, &errResp); err != nil {
		t.Fatalf("failed to parse EC2 Query XML error: %v\nbody: %s", err, body)
	}
	if len(errResp.Errors) != 1 || errResp.Errors[0].Code != "Throttling" {
		t.Errorf("Code: expected [Throttling], got %+v", errResp.Errors)
	}
}

// TestWriteEC2QueryXMLError_structuredResponse verifies the EC2 Query error envelope.
func TestWriteEC2QueryXMLError_structuredResponse(t *testing.T) {
	// Given: a handler that writes an EC2 Query error
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		protocol.WriteEC2QueryXMLError(w, r, &protocol.AWSError{
			Code:       "InvalidInstanceID.NotFound",
			Message:    "The instance ID 'i-1a2b3c4d' does not exist",
			HTTPStatus: http.StatusBadRequest,
		})
	})

	// When: we make a request
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("Action=DescribeInstances"))
	req = req.WithContext(protocol.ContextWithRequestID(req.Context(), "ec2-request-id"))
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	resp := w.Result()

	// Then: the body matches EC2's documented <Response><Errors><Error> shape
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status: expected 400, got %d", resp.StatusCode)
	}
	if rid := resp.Header.Get("x-amzn-requestid"); rid != "ec2-request-id" {
		t.Errorf("x-amzn-requestid: expected ec2-request-id, got %q", rid)
	}

	body, _ := io.ReadAll(resp.Body)
	var errResp struct {
		XMLName xml.Name `xml:"Response"`
		Errors  []struct {
			Code    string `xml:"Code"`
			Message string `xml:"Message"`
		} `xml:"Errors>Error"`
		RequestID string `xml:"RequestID"`
	}
	if err := xml.Unmarshal(body, &errResp); err != nil {
		t.Fatalf("failed to parse EC2 XML error: %v\nbody: %s", err, body)
	}
	if errResp.XMLName.Local != "Response" {
		t.Errorf("root: expected Response, got %q", errResp.XMLName.Local)
	}
	if len(errResp.Errors) != 1 {
		t.Fatalf("expected one error, got %d; body: %s", len(errResp.Errors), body)
	}
	if errResp.Errors[0].Code != "InvalidInstanceID.NotFound" {
		t.Errorf("Code: expected InvalidInstanceID.NotFound, got %q", errResp.Errors[0].Code)
	}
	if errResp.RequestID != "ec2-request-id" {
		t.Errorf("RequestID: expected ec2-request-id, got %q", errResp.RequestID)
	}
}

// TestRequestID_generatesUniqueIDs verifies NewRequestID produces unique values.
func TestRequestID_generatesUniqueIDs(t *testing.T) {
	// Given / When: we generate multiple request IDs
	ids := make(map[string]bool)
	for i := 0; i < 100; i++ {
		id := protocol.NewRequestID()
		if id == "" {
			t.Fatal("NewRequestID returned empty string")
		}
		if ids[id] {
			t.Fatalf("NewRequestID generated duplicate ID: %q", id)
		}
		ids[id] = true
	}
	// Then: all 100 IDs are unique (verified by the map above)
}

// TestRequestIDContext_roundtrip verifies storing and retrieving request IDs.
func TestRequestIDContext_roundtrip(t *testing.T) {
	// Given: a context with a request ID
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	ctx := protocol.ContextWithRequestID(req.Context(), "my-id-123")

	// When: we retrieve it
	got := protocol.RequestIDFromContext(ctx)

	// Then: we get the same ID back
	if got != "my-id-123" {
		t.Errorf("expected my-id-123, got %q", got)
	}
}

// TestRequestIDContext_missingGeneratesNew verifies fallback behaviour.
func TestRequestIDContext_missingGeneratesNew(t *testing.T) {
	// Given: a context with no request ID
	req := httptest.NewRequest(http.MethodGet, "/", nil)

	// When: we retrieve the request ID
	got := protocol.RequestIDFromContext(req.Context())

	// Then: a non-empty ID is generated (not an error, not empty)
	if got == "" {
		t.Error("expected a generated request ID, got empty string")
	}
}

// TestARN_s3OmitsRegionAndAccount verifies S3 ARN format.
func TestARN_s3OmitsRegionAndAccount(t *testing.T) {
	// Given / When: we build an S3 ARN
	arn := protocol.ARN("us-east-1", "000000000000", "s3", "my-bucket")

	// Then: region and account are omitted (AWS spec)
	expected := "arn:aws:s3:::my-bucket"
	if arn != expected {
		t.Errorf("S3 ARN: expected %q, got %q", expected, arn)
	}
}

// TestARN_sqsIncludesRegionAndAccount verifies SQS ARN format.
func TestARN_sqsIncludesRegionAndAccount(t *testing.T) {
	// Given / When: we build an SQS ARN
	arn := protocol.QueueARN("us-east-1", "123456789012", "my-queue")

	// Then: region and account are included
	expected := "arn:aws:sqs:us-east-1:123456789012:my-queue"
	if arn != expected {
		t.Errorf("SQS ARN: expected %q, got %q", expected, arn)
	}
}

// ---- Error wrapping (cause) ------------------------------------------------

// TestWrap_preservesCauseInChain verifies that Wrap() attaches the cause and
// that the standard errors.Is / errors.As functions can inspect it.
func TestWrap_preservesCauseInChain(t *testing.T) {
	// Given: an underlying storage error and a template AWSError
	underlyingErr := errors.New("sqlite: disk I/O error")

	// When: we wrap the storage error with an AWSError
	wrapped := protocol.Wrap(protocol.ErrInternalError, underlyingErr)

	// Then: the AWS error code is preserved
	if wrapped.Code != "InternalError" {
		t.Errorf("expected Code InternalError, got %q", wrapped.Code)
	}

	// And: errors.Is traverses the chain to find the underlying error
	if !errors.Is(wrapped, underlyingErr) {
		t.Error("errors.Is should find the underlying error through the chain")
	}

	// And: errors.As can extract *AWSError from the chain
	var aerr *protocol.AWSError
	if !errors.As(wrapped, &aerr) {
		t.Error("errors.As should find *AWSError in the chain")
	}
	if aerr.Code != "InternalError" {
		t.Errorf("expected Code InternalError via errors.As, got %q", aerr.Code)
	}
}

// TestWrap_doesNotMutateTemplate verifies that Wrap() never modifies the
// sentinel template — this would be a concurrency bug if templates are shared.
func TestWrap_doesNotMutateTemplate(t *testing.T) {
	// Given: the sentinel ErrInternalError template
	originalMessage := protocol.ErrInternalError.Message

	// When: we wrap it with a cause
	cause := errors.New("some cause")
	wrapped := protocol.Wrap(protocol.ErrInternalError, cause)

	// Then: the template is unchanged
	if protocol.ErrInternalError.Message != originalMessage {
		t.Error("Wrap() must not mutate the template AWSError")
	}
	if protocol.Cause(protocol.ErrInternalError) != nil {
		t.Error("Wrap() must not set cause on the template")
	}

	// And: the wrapped copy has the cause
	if protocol.Cause(wrapped) != cause {
		t.Error("wrapped error should have the cause set")
	}
}

// TestWrap_causeNotLeakedToClient verifies that the underlying cause is never
// sent to the HTTP client — it is server-side only.
func TestWrap_causeNotLeakedToClient(t *testing.T) {
	// Given: an AWSError wrapping a sensitive internal error
	sensitiveErr := errors.New("internal db password: hunter2")
	aerr := protocol.Wrap(protocol.ErrInternalError, sensitiveErr)

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		protocol.WriteJSONError(w, r, aerr)
	})

	// When: the error is written to an HTTP response
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req = req.WithContext(protocol.ContextWithRequestID(req.Context(), "req-safe"))
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	// Then: the response body must not contain the sensitive cause text
	body := w.Body.String()
	if strings.Contains(body, "hunter2") {
		t.Errorf("cause must not be leaked to the client: body = %s", body)
	}
	if strings.Contains(body, "internal db password") {
		t.Errorf("cause must not be leaked to the client: body = %s", body)
	}
}

// TestAsAWSError_extractsFromChain verifies AsAWSError traverses nested wrapping.
func TestAsAWSError_extractsFromChain(t *testing.T) {
	// Given: an AWSError wrapped in a plain fmt.Errorf chain
	aerr := protocol.Wrap(protocol.ErrInternalError, errors.New("root cause"))
	outerErr := fmt.Errorf("service layer: %w", aerr)

	// When: we use AsAWSError
	got := protocol.AsAWSError(outerErr)

	// Then: we get the AWSError back
	if got == nil {
		t.Fatal("AsAWSError should find *AWSError in the chain")
	}
	if got.Code != "InternalError" {
		t.Errorf("expected InternalError, got %q", got.Code)
	}
}

// TestAsAWSError_returnsNilForNonAWSError verifies nil is returned cleanly.
func TestAsAWSError_returnsNilForNonAWSError(t *testing.T) {
	// Given: a plain error with no AWSError in its chain
	plainErr := fmt.Errorf("some plain error: %w", errors.New("root"))

	// When + Then: AsAWSError returns nil
	if got := protocol.AsAWSError(plainErr); got != nil {
		t.Errorf("expected nil for non-AWSError chain, got %v", got)
	}
}

// TestWriteXMLError_detailsAreOptionalAndOrdered pins both halves of the
// XMLDetails contract added in #1864: an error that names no extra members
// renders exactly the envelope it always did — byte for byte, because this
// field sits in the document every REST-XML service shares — and one that does
// renders them between Message and RequestId, which is where AWS puts them.
func TestWriteXMLError_detailsAreOptionalAndOrdered(t *testing.T) {
	write := func(aerr *protocol.AWSError) string {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req = req.WithContext(protocol.ContextWithRequestID(req.Context(), "rid"))
		w := httptest.NewRecorder()
		protocol.WriteXMLError(w, req, aerr)
		body, _ := io.ReadAll(w.Result().Body)
		return string(body)
	}

	// Given: an error with no details — the shape every other service sends
	plain := write(&protocol.AWSError{
		Code:       "NoSuchBucket",
		Message:    "The bucket does not exist",
		HTTPStatus: http.StatusNotFound,
	})

	// Then: it is unchanged by the field's existence
	want := xml.Header + "<Error><Code>NoSuchBucket</Code>" +
		"<Message>The bucket does not exist</Message><RequestId>rid</RequestId></Error>"
	if plain != want {
		t.Errorf("an error with no XMLDetails must be byte-identical to the old envelope\n got: %s\nwant: %s", plain, want)
	}

	// And: one that names details renders them in order, after Message
	detailed := write(&protocol.AWSError{
		Code:       "InvalidRange",
		Message:    "The requested range is not satisfiable",
		HTTPStatus: http.StatusRequestedRangeNotSatisfiable,
		XMLDetails: []protocol.XMLDetail{
			{Name: "RangeRequested", Value: "bytes=500-600"},
			{Name: "ActualObjectSize", Value: "136"},
		},
	})
	wantDetailed := xml.Header + "<Error><Code>InvalidRange</Code>" +
		"<Message>The requested range is not satisfiable</Message>" +
		"<RangeRequested>bytes=500-600</RangeRequested>" +
		"<ActualObjectSize>136</ActualObjectSize><RequestId>rid</RequestId></Error>"
	if detailed != wantDetailed {
		t.Errorf("details must render in order between Message and RequestId\n got: %s\nwant: %s", detailed, wantDetailed)
	}
}

// ---- rest-xml wrapped errors (CloudFront) ---------------------------------

// captureResponse runs write against a recorder carrying request ID "rid" and
// returns the response together with its body.
func captureResponse(t *testing.T, write func(w http.ResponseWriter, r *http.Request)) (*http.Response, string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req = req.WithContext(protocol.ContextWithRequestID(req.Context(), "rid"))
	w := httptest.NewRecorder()
	write(w, req)
	resp := w.Result()
	body, _ := io.ReadAll(resp.Body)
	return resp, string(body)
}

// TestWriteQueryXMLError_declaresNoNamespace holds the Query-protocol
// envelope byte-identical. WriteRESTXMLError shares its response struct, and
// the xmlns attribute it adds is omitempty precisely so that Route 53, SNS,
// STS and IAM keep sending exactly what they always have.
func TestWriteQueryXMLError_declaresNoNamespace(t *testing.T) {
	// Given: an error written through the Query-protocol writer
	_, body := captureResponse(t, func(w http.ResponseWriter, r *http.Request) {
		protocol.WriteQueryXMLError(w, r, &protocol.AWSError{
			Code:       "NoSuchHostedZone",
			Message:    "No hosted zone found with ID: Z1",
			HTTPStatus: http.StatusNotFound,
		})
	})

	// Then: the envelope is unchanged, and carries no xmlns
	want := xml.Header + "<ErrorResponse><Error><Type>Sender</Type>" +
		"<Code>NoSuchHostedZone</Code>" +
		"<Message>No hosted zone found with ID: Z1</Message></Error>" +
		"<RequestId>rid</RequestId></ErrorResponse>"
	if body != want {
		t.Errorf("Query error envelope changed\n got: %s\nwant: %s", body, want)
	}
}

func TestWriteRESTXMLError_wrapsWithServiceNamespace(t *testing.T) {
	// Given: an error written through the rest-xml writer with a namespace
	const ns = "http://cloudfront.amazonaws.com/doc/2020-05-31/"
	resp, body := captureResponse(t, func(w http.ResponseWriter, r *http.Request) {
		protocol.WriteRESTXMLError(w, r, ns, &protocol.AWSError{
			Code:       "NoSuchDistribution",
			Message:    "The specified distribution does not exist: E1",
			HTTPStatus: http.StatusNotFound,
		})
	})

	// Then: the wrapped envelope carries the namespace on its root, and the
	// headers are the ones the service's success responses use
	want := xml.Header + `<ErrorResponse xmlns="` + ns + `"><Error><Type>Sender</Type>` +
		"<Code>NoSuchDistribution</Code>" +
		"<Message>The specified distribution does not exist: E1</Message></Error>" +
		"<RequestId>rid</RequestId></ErrorResponse>"
	if body != want {
		t.Errorf("rest-xml error envelope\n got: %s\nwant: %s", body, want)
	}
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status: expected 404, got %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/xml" {
		t.Errorf("Content-Type: expected application/xml, got %q", ct)
	}
	if rid := resp.Header.Get("x-amz-request-id"); rid != "rid" {
		t.Errorf("x-amz-request-id: expected rid, got %q", rid)
	}
}

// TestWriteRESTXMLError_serverErrorIsReceiver covers the other value of Type:
// AWS blames the receiver for a 5xx and the sender for everything below it.
func TestWriteRESTXMLError_serverErrorIsReceiver(t *testing.T) {
	// Given: a server-side failure written through the rest-xml writer
	_, body := captureResponse(t, func(w http.ResponseWriter, r *http.Request) {
		protocol.WriteRESTXMLError(w, r, "urn:test", protocol.ErrInternalError)
	})

	// Then: Type reports Receiver
	if !strings.Contains(body, "<Type>Receiver</Type>") {
		t.Errorf("expected <Type>Receiver</Type> for a 5xx, got: %s", body)
	}
}

// TestWriteXMLNS_stampsTheRootOnly covers the success half: the namespace is
// a property of the response, so it lands on the root element and nowhere
// else, however deeply the payload nests.
func TestWriteXMLNS_stampsTheRootOnly(t *testing.T) {
	type inner struct {
		XMLName xml.Name `xml:"Inner"`
		Value   string   `xml:"Value"`
	}
	type outer struct {
		XMLName xml.Name `xml:"Outer"`
		ID      string   `xml:"Id"`
		Inner   inner    `xml:"Inner"`
	}
	payload := &outer{ID: "E1", Inner: inner{Value: "v & w"}}
	const ns = "http://cloudfront.amazonaws.com/doc/2020-05-31/"

	// Given: a nested payload written with a namespace
	_, namespaced := captureResponse(t, func(w http.ResponseWriter, r *http.Request) {
		protocol.WriteXMLNS(w, r, http.StatusOK, ns, payload)
	})

	// Then: only the root declares it, and nothing else about the document
	// changed — escaping included
	want := xml.Header + `<Outer xmlns="` + ns + `"><Id>E1</Id>` +
		"<Inner><Value>v &amp; w</Value></Inner></Outer>"
	if namespaced != want {
		t.Errorf("namespaced response\n got: %s\nwant: %s", namespaced, want)
	}

	// And: the same payload without a namespace is what WriteXML writes
	_, plain := captureResponse(t, func(w http.ResponseWriter, r *http.Request) {
		protocol.WriteXML(w, r, http.StatusOK, payload)
	})
	if wantPlain := strings.Replace(want, ` xmlns="`+ns+`"`, "", 1); plain != wantPlain {
		t.Errorf("un-namespaced response changed\n got: %s\nwant: %s", plain, wantPlain)
	}
}
