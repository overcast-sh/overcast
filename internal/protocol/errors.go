// Package protocol provides shared AWS protocol helpers used by all service
// handlers: error serialisation, request IDs, ARN construction, and response
// writing. Nothing in this package is service-specific.
package protocol

import (
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"

	"github.com/overcast-sh/overcast/internal/state"
)

// ---- Error types -----------------------------------------------------------

// AWSError represents a structured AWS API error that maps to an HTTP response.
//
// It implements the standard error interface and supports Go's error wrapping
// convention, equivalent to JavaScript's `new Error("msg", { cause: err })`.
//
// Wrapping pattern — use Wrap() to attach an underlying cause while presenting
// a clean AWS error to callers:
//
//	// Service code:
//	raw, found, err := s.store.Get(ctx, ns, key)
//	if err != nil {
//	    return protocol.Wrap(protocol.ErrInternalError, err)
//	}
//
//	// The HTTP layer sees ErrInternalError (clean AWS error code + message).
//	// The original err is preserved in the chain for logging and debugging:
//	logger.Error("state read failed", zap.Error(aerr))
//	// → logs both "InternalError" and the underlying storage error
//
// Inspection pattern — anywhere in the call chain:
//
//	var aerr *protocol.AWSError
//	if errors.As(err, &aerr) {
//	    // aerr.Code, aerr.HTTPStatus available
//	}
type AWSError struct {
	// Code is the AWS error code string, e.g. "NoSuchBucket", "QueueDoesNotExist".
	Code string
	// Message is the human-readable description sent to the client.
	Message string
	// HTTPStatus is the HTTP status code to send, e.g. 404, 400, 500.
	HTTPStatus int
	// QueryErrorCode, when set, is the legacy AWS Query-protocol error code
	// for this error. WriteJSONError renders it as the x-amzn-query-error
	// response header (format "<code>;Sender" or "<code>;Receiver", chosen
	// from HTTPStatus) — the header AWS sends on every JSON-protocol error
	// response for a service marked aws.protocols#awsQueryCompatible, so
	// that clients still speaking the legacy Query error codes keep working.
	// Left empty (the default, and the only value any service but SQS ever
	// sets) it renders no header at all, so this field is a no-op for every
	// service that never sets it. See internal/services/sqs's query error
	// table for the one service that does, and why.
	QueryErrorCode string
	// Reason, when set, is rendered as the JSON error body's "Reason"
	// member. Several AWS JSON-protocol services model an enum-valued
	// Reason member on one of their exception shapes that AWS always
	// populates alongside Code/Message — Organizations'
	// InvalidInputException.Reason (the InvalidInputExceptionReason enum) is
	// the first Overcast caller. Left empty (the default, and the only
	// value any service without such a member ever sets) it renders no
	// field at all, so this is a no-op for every service that never sets
	// it — the same shape as QueryErrorCode above.
	Reason string
	// XMLDetails, when set, are extra members rendered into the REST-XML
	// error body between Message and RequestId, in the order given. AWS's
	// error documents are not one fixed shape: some errors carry members
	// naming what the caller asked for and what the resource actually is,
	// which is the whole diagnostic value of the error. S3's InvalidRange
	// is the first Overcast caller — it names RangeRequested and
	// ActualObjectSize, the two fields a ranged-download client compares to
	// find its own off-by-one. Left nil (the default, and the only value an
	// error without such members ever sets) nothing extra is rendered, so
	// this is a no-op for every error that never sets it — the same shape
	// as QueryErrorCode and Reason above.
	XMLDetails []XMLDetail
	// cause is the underlying error that triggered this AWSError.
	// It is not sent to clients — it is for internal logging and error chain
	// inspection only. Equivalent to JavaScript's Error.cause.
	cause error
}

type errorRecorder interface {
	RecordAWSError(*AWSError)
}

func recordAWSError(w http.ResponseWriter, aerr *AWSError) {
	if rec, ok := w.(errorRecorder); ok {
		rec.RecordAWSError(aerr)
	}
}

// RecordError attaches aerr, cause included, to the request's log line and
// trace, as every Write*Error helper here does. It is for a handler that puts
// an error on the wire in a model of its own — a non-AWS protocol such as the
// Iceberg REST catalog's — so its failures are logged like any other.
func RecordError(w http.ResponseWriter, aerr *AWSError) {
	recordAWSError(w, aerr)
}

// Error implements the error interface.
// Returns the AWS error code and message; the cause is accessible via Unwrap.
func (e *AWSError) Error() string {
	if e.cause != nil {
		return fmt.Sprintf("%s: %s (cause: %v)", e.Code, e.Message, e.cause)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

// Unwrap returns the underlying cause, enabling errors.Is / errors.As to
// traverse the full error chain. This is the Go equivalent of error.cause
// in JavaScript.
//
//	// Check if a specific underlying error occurred:
//	if errors.Is(aerr, sql.ErrNoRows) { ... }
//
//	// Extract a specific error type from anywhere in the chain:
//	var pgErr *pgconn.PgError
//	if errors.As(aerr, &pgErr) { ... }
func (e *AWSError) Unwrap() error {
	return e.cause
}

// Wrap returns a new *AWSError with the same Code, Message, and HTTPStatus as
// template, but with cause attached as the underlying error.
//
// This is the primary way to preserve error context in service code:
//
//	func (s *s3Store) getBucket(ctx context.Context, name string) (*Bucket, *AWSError) {
//	    raw, found, err := s.store.Get(ctx, nsBuckets, name)
//	    if err != nil {
//	        // ErrInternalError is shown to the client.
//	        // err (e.g. "sqlite: no such table") is preserved for logging.
//	        return nil, protocol.Wrap(ErrInternalError, err)
//	    }
//	    ...
//	}
//
// Wrap never modifies the template — it always returns a new AWSError value.
func Wrap(template *AWSError, cause error) *AWSError {
	return &AWSError{
		Code:           template.Code,
		Message:        template.Message,
		HTTPStatus:     template.HTTPStatus,
		QueryErrorCode: template.QueryErrorCode,
		Reason:         template.Reason,
		cause:          cause,
	}
}

// Cause returns the underlying cause of aerr, or nil if none was set.
// Prefer errors.Is / errors.As over Cause for most use cases — they traverse
// arbitrarily deep chains. Use Cause only when you need the immediate cause.
func Cause(aerr *AWSError) error {
	return aerr.cause
}

// ---- Sentinel errors -------------------------------------------------------
// These are templates — never mutate them. Use Wrap() to attach a cause.

var (
	// ErrNotImplemented is returned for endpoints that exist in the routing
	// table but have not yet been implemented. The x-emulator-unsupported
	// header is added automatically by WriteXMLError / WriteJSONError.
	ErrNotImplemented = &AWSError{
		Code:       "NotImplemented",
		Message:    "This operation is not yet emulated. Check docs/services/ for the support matrix.",
		HTTPStatus: http.StatusNotImplemented,
	}

	// ErrMethodNotAllowed is returned for a method AWS itself rejects on a
	// resource — not for something Overcast has yet to implement. The two are
	// different claims: NotImplemented tells a client the emulator is
	// incomplete and invites a workaround, when real AWS would refuse the same
	// request. S3's wording, verbatim.
	ErrMethodNotAllowed = &AWSError{
		Code:       "MethodNotAllowed",
		Message:    "The specified method is not allowed against this resource.",
		HTTPStatus: http.StatusMethodNotAllowed,
	}

	// ErrInternalError is returned when the emulator itself encounters an
	// unexpected failure (state backend error, serialisation failure, etc.).
	// Always Wrap() this with the underlying error for log context.
	ErrInternalError = &AWSError{
		Code:       "InternalError",
		Message:    "An internal error occurred.",
		HTTPStatus: http.StatusInternalServerError,
	}

	// ErrStorageMigrating is returned by middleware.NotReady while the
	// storage backend is still completing a one-time schema migration on
	// startup (see internal/state/migrate.go) and cannot yet serve requests
	// reliably — without this check, a request in this window would either
	// block indefinitely (persistent mode) or silently observe incomplete
	// state as if it simply didn't exist (hybrid mode's TierHot reads,
	// before the post-migration seed has populated memory). "ServiceUnavailable"
	// is a real AWS error code multiple services return for their own
	// transient unavailability, so AWS SDKs already retry it automatically
	// per their standard retry policy — a client normally needs no special
	// handling for this.
	ErrStorageMigrating = &AWSError{
		Code:       "ServiceUnavailable",
		Message:    "Overcast is completing a one-time database migration and is not yet ready to serve requests. This happens after an upgrade or first startup against existing data and should resolve within moments — retry the request.",
		HTTPStatus: http.StatusServiceUnavailable,
	}
)

// ErrInvalidArgument returns a 400 error for malformed input.
func ErrInvalidArgument(msg string) *AWSError {
	return &AWSError{Code: "InvalidArgument", Message: msg, HTTPStatus: http.StatusBadRequest}
}

// ErrInvalidBucketName returns the 400 error S3 documents for a bucket name
// that breaks the naming rules: code InvalidBucketName, "The specified bucket
// is not valid.", HTTP 400 Bad Request. Evidenced by the error-responses list
// AWS carries as documentation on the S3 model's Error$Code member — S3's
// error codes are not modeled as Smithy error shapes, so that list is the
// authority. https://docs.aws.amazon.com/AmazonS3/latest/API/ErrorResponses.html
//
// msg carries the reason the name was rejected rather than AWS's one-line
// description; see the note above serviceutil.BucketName for why the emulator
// keeps the longer wording.
func ErrInvalidBucketName(msg string) *AWSError {
	return &AWSError{Code: "InvalidBucketName", Message: msg, HTTPStatus: http.StatusBadRequest}
}

// ErrSerialization returns the 400 error real AWS JSON-protocol services
// use for a request body that cannot be parsed at all — DynamoDB and other
// coral-framework services return __type
// com.amazon.coral.service#SerializationException, and Smithy's
// malformed-request protocol tests pin 400 + x-amzn-errortype:
// SerializationException. Use this for parse-level failures only; semantic
// validation of a successfully parsed request stays ValidationException /
// InvalidArgument / service-specific codes.
func ErrSerialization(msg string) *AWSError {
	return &AWSError{Code: "SerializationException", Message: msg, HTTPStatus: http.StatusBadRequest}
}

// ErrMissingParameter returns a 400 error for a missing required parameter.
func ErrMissingParameter(param string) *AWSError {
	return &AWSError{
		Code:       "MissingParameter",
		Message:    fmt.Sprintf("The request must contain the parameter %s.", param),
		HTTPStatus: http.StatusBadRequest,
	}
}

// AsAWSError extracts the first *AWSError from the error chain.
// Returns nil if no *AWSError is found.
//
// This is a convenience wrapper around errors.As for the common case of
// checking whether an error from a helper function is an AWSError:
//
//	aerr := protocol.AsAWSError(err)
//	if aerr != nil {
//	    protocol.WriteJSONError(w, r, aerr)
//	    return
//	}
func AsAWSError(err error) *AWSError {
	var aerr *AWSError
	if errors.As(err, &aerr) {
		return aerr
	}
	return nil
}

// ---- Storage-pressure -> AWS throttling remap ------------------------------
//
// Real AWS signals storage/service overload with a throttling error that SDK
// retry policies retry automatically (exponential backoff, no application
// code required). Overcast's HybridStore instead used to surface a plain
// "context deadline exceeded", wrapped as InternalError by every service's
// store layer via the standard protocol.Wrap(protocol.ErrInternalError, err)
// pattern, when SQLite read contention outlasted both busy_timeout and the
// read-retry window (see internal/state/hybrid.go's
// shouldRetryHybridSQLiteRead / hybridSQLiteReadRetryTimeout and
// HybridStore.wrapStorePressure, which tags the underlying error with
// state.ErrStorePressure before it ever reaches a service). A generic 500
// causes most SDKs to give up immediately instead of retrying.
//
// remapStorePressure is the single place that knows both halves of this
// translation: "is this error actually storage pressure" (errors.Is against
// the sentinel, which traverses AWSError.Unwrap same as any other error
// chain) and "what does AWS's throttling error look like on this wire
// family" (the throttle template passed by each Write*Error caller below,
// since only those call sites know which family they're serializing for).
// Keeping the sentinel check itself in one function — rather than
// duplicating an errors.Is call in all four Write*Error bodies — is what
// makes this "one place": no service code anywhere needs to special-case
// pressure errors; every service already goes through one of these four
// functions to write any error response.
//
// AWS error codes/statuses per wire family (verified against AWS docs,
// 2026-07-25):
//   - S3 REST-XML (WriteXMLError): "SlowDown", HTTP 503 — S3's documented
//     write/request-rate throttling error.
//     https://docs.aws.amazon.com/AmazonS3/latest/API/ErrorResponses.html
//   - AWS JSON 1.0/1.1 (WriteJSONError — SQS, SNS's JSON path, DynamoDB,
//     Lambda, etc.): "ThrottlingException", HTTP 400 — the standard modeled
//     throttling error shared by JSON-protocol services.
//   - Query/REST-XML (WriteQueryXMLError, WriteEC2QueryXMLError — SNS's
//     Query path, EC2, and other Query-protocol services): "Throttling",
//     HTTP 400 — AWS's general Query-protocol throttling error ("Rate
//     exceeded").
var (
	errSlowDown = &AWSError{
		Code:       "SlowDown",
		Message:    "Please reduce your request rate.",
		HTTPStatus: http.StatusServiceUnavailable,
	}
	errThrottlingException = &AWSError{
		Code:       "ThrottlingException",
		Message:    "The request was denied due to request throttling.",
		HTTPStatus: http.StatusBadRequest,
	}
	errThrottling = &AWSError{
		Code:       "Throttling",
		Message:    "Rate exceeded",
		HTTPStatus: http.StatusBadRequest,
	}
)

// remapStorePressure returns throttle (via Wrap, preserving aerr's cause
// chain) when aerr's chain carries state.ErrStorePressure; otherwise it
// returns aerr unchanged. See the package-level doc comment above.
func remapStorePressure(aerr *AWSError, throttle *AWSError) *AWSError {
	if aerr == nil || !errors.Is(aerr, state.ErrStorePressure) {
		return aerr
	}
	return Wrap(throttle, Cause(aerr))
}

// ---- XML error format (S3 and other REST-XML services) ---------------------

// XMLDetail is one operation-specific member of a REST-XML error document.
// See AWSError.XMLDetails for when an error carries any.
type XMLDetail struct {
	Name  string
	Value string
}

// xmlErrorResponse is the wire envelope S3 uses for errors. Field order is
// element order, so Details sits where AWS puts an error's own members:
// after Message, before RequestId.
type xmlErrorResponse struct {
	XMLName   xml.Name `xml:"Error"`
	Code      string   `xml:"Code"`
	Message   string   `xml:"Message"`
	Details   []xmlErrorDetail
	RequestID string `xml:"RequestId"`
}

// xmlErrorDetail renders one XMLDetail under its own element name.
type xmlErrorDetail struct {
	XMLName xml.Name
	Value   string `xml:",chardata"`
}

// WriteXMLError writes an AWS REST-XML error response (S3 format).
// The cause, if any, is NOT included in the response — it stays server-side
// for logging. The x-emulator-unsupported header is set automatically for 501.
func WriteXMLError(w http.ResponseWriter, r *http.Request, aerr *AWSError) {
	aerr = remapStorePressure(aerr, errSlowDown)
	recordAWSError(w, aerr)
	reqID := RequestIDFromContext(r.Context())

	var details []xmlErrorDetail
	for _, d := range aerr.XMLDetails {
		details = append(details, xmlErrorDetail{XMLName: xml.Name{Local: d.Name}, Value: d.Value})
	}
	body, _ := xml.Marshal(&xmlErrorResponse{
		Code:      aerr.Code,
		Message:   aerr.Message,
		Details:   details,
		RequestID: reqID,
	})

	// Drain the request body so the HTTP/1.1 connection can be reused by the
	// SDK client. Without this, the client logs "failed to close HTTP response
	// body, this may affect connection reuse".
	if r.Body != nil {
		io.Copy(io.Discard, r.Body) //nolint:errcheck
		r.Body.Close()              //nolint:errcheck
	}

	full := append([]byte(xml.Header), body...)
	w.Header().Set("Content-Type", "application/xml")
	w.Header().Set("Content-Length", strconv.Itoa(len(full)))
	w.Header().Set("x-amz-request-id", reqID)
	if aerr.HTTPStatus == http.StatusNotImplemented {
		w.Header().Set("x-emulator-unsupported", "true")
	}
	w.WriteHeader(aerr.HTTPStatus)
	w.Write(full)
}

// ---- JSON error format (SQS, SNS, DynamoDB, Lambda) -----------------------

// jsonErrorResponse is the wire envelope used by JSON-protocol services.
type jsonErrorResponse struct {
	Type    string `json:"__type"`
	Message string `json:"message"`
	// Reason mirrors AWSError.Reason — omitted entirely for the services
	// that never set it (see AWSError.Reason's doc comment).
	Reason string `json:"Reason,omitempty"`
}

// WriteJSONError writes an AWS JSON-protocol error response.
// The cause, if any, is NOT included in the response body.
func WriteJSONError(w http.ResponseWriter, r *http.Request, aerr *AWSError) {
	aerr = remapStorePressure(aerr, errThrottlingException)
	recordAWSError(w, aerr)
	reqID := RequestIDFromContext(r.Context())

	body, _ := json.Marshal(&jsonErrorResponse{
		Type:    aerr.Code,
		Message: aerr.Message,
		Reason:  aerr.Reason,
	})

	// Drain the request body so the HTTP/1.1 connection can be reused by the
	// SDK client.
	if r.Body != nil {
		io.Copy(io.Discard, r.Body) //nolint:errcheck
		r.Body.Close()              //nolint:errcheck
	}

	w.Header().Set("Content-Type", "application/x-amz-json-1.0")
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.Header().Set("x-amzn-requestid", reqID)
	if aerr.QueryErrorCode != "" {
		w.Header().Set("x-amzn-query-error", aerr.QueryErrorCode+";"+querySenderOrReceiver(aerr.HTTPStatus))
	}
	if aerr.HTTPStatus == http.StatusNotImplemented {
		w.Header().Set("x-emulator-unsupported", "true")
	}
	w.WriteHeader(aerr.HTTPStatus)
	w.Write(body)
}

// querySenderOrReceiver classifies an HTTP status the way the
// awsQueryCompatible protocol's x-amzn-query-error header does: a 5xx is the
// service's own fault ("Receiver"), anything else is the caller's
// ("Sender") — matching the smithy.api#error "client"/"server" trait value
// AWS's error shapes carry, without needing to model that trait here too.
func querySenderOrReceiver(httpStatus int) string {
	if httpStatus >= http.StatusInternalServerError {
		return "Receiver"
	}
	return "Sender"
}

// NotImplementedXML is a convenience handler for unimplemented S3 endpoints.
func NotImplementedXML(w http.ResponseWriter, r *http.Request) {
	WriteXMLError(w, r, ErrNotImplemented)
}

// MethodNotAllowedXML is a convenience handler for a method AWS rejects on a
// resource, for use as a dispatch fallback where no subresource selects an
// operation.
func MethodNotAllowedXML(w http.ResponseWriter, r *http.Request) {
	WriteXMLError(w, r, ErrMethodNotAllowed)
}

// NotImplementedJSON is a convenience handler for unimplemented JSON endpoints.
func NotImplementedJSON(w http.ResponseWriter, r *http.Request) {
	WriteJSONError(w, r, ErrNotImplemented)
}

// ---- Query-protocol XML error format (SNS) ---------------------------------

// queryXMLErrorResponse is the wire envelope used by AWS Query-protocol
// services (SNS), and by the rest-xml services that do not set
// noErrorWrapping (CloudFront) — the two protocols wrap errors identically.
//
// Xmlns is omitted when empty, so a caller that passes no namespace writes
// the same bytes this envelope has always produced. WriteQueryXMLError never
// sets it; TestWriteQueryXMLError_declaresNoNamespace holds that.
type queryXMLErrorResponse struct {
	XMLName   xml.Name      `xml:"ErrorResponse"`
	Xmlns     string        `xml:"xmlns,attr,omitempty"`
	Error     queryXMLError `xml:"Error"`
	RequestID string        `xml:"RequestId"`
}

type queryXMLError struct {
	Type    string `xml:"Type"`
	Code    string `xml:"Code"`
	Message string `xml:"Message"`
}

// ec2QueryXMLErrorResponse is EC2's documented Query-protocol error envelope.
// Verified against AWS docs (2026-07-12):
// https://docs.aws.amazon.com/AWSEC2/latest/APIReference/errors-overview.html#api-error-response
type ec2QueryXMLErrorResponse struct {
	XMLName   xml.Name           `xml:"Response"`
	Errors    []ec2QueryXMLError `xml:"Errors>Error"`
	RequestID string             `xml:"RequestID"`
}

type ec2QueryXMLError struct {
	Code    string `xml:"Code"`
	Message string `xml:"Message"`
}

// WriteQueryXMLError writes an AWS Query-protocol XML error response (SNS format).
func WriteQueryXMLError(w http.ResponseWriter, r *http.Request, aerr *AWSError) {
	aerr = remapStorePressure(aerr, errThrottling)
	recordAWSError(w, aerr)
	reqID := RequestIDFromContext(r.Context())
	body, _ := xml.Marshal(&queryXMLErrorResponse{
		Error: queryXMLError{
			Type:    "Sender",
			Code:    aerr.Code,
			Message: aerr.Message,
		},
		RequestID: reqID,
	})
	if r.Body != nil {
		io.Copy(io.Discard, r.Body) //nolint:errcheck
		r.Body.Close()              //nolint:errcheck
	}
	full := append([]byte(xml.Header), body...)
	w.Header().Set("Content-Type", "text/xml")
	w.Header().Set("Content-Length", strconv.Itoa(len(full)))
	w.Header().Set("x-amzn-requestid", reqID)
	if aerr.HTTPStatus == http.StatusNotImplemented {
		w.Header().Set("x-emulator-unsupported", "true")
	}
	w.WriteHeader(aerr.HTTPStatus)
	w.Write(full)
}

// WriteRESTXMLError writes an error in the envelope the rest-xml protocol
// wraps errors in when the service's protocol trait does not set
// noErrorWrapping:
//
//	<ErrorResponse xmlns="…">
//	  <Error><Type>Sender</Type><Code>…</Code><Message>…</Message></Error>
//	  <RequestId>…</RequestId>
//	</ErrorResponse>
//
// That is WriteQueryXMLError's envelope plus the service's xmlNamespace on
// the root. The headers are WriteXMLError's rather than WriteQueryXMLError's
// (application/xml, x-amz-request-id), so an error and a 200 from the same
// REST-XML operation agree on both; only the body shape changes.
//
// Type says who the model blames. AWS answers Sender for a client error and
// Receiver for a server one, from the error shape's smithy.api#error trait;
// AWSError records the HTTP status rather than the trait, which stands in
// for it exactly on every modeled error (4xx client, 5xx server).
//
// Store pressure maps to SlowDown, as it does for every other REST-XML
// service — these callers came from WriteXMLError and this is not a change
// of behaviour under load.
func WriteRESTXMLError(w http.ResponseWriter, r *http.Request, namespace string, aerr *AWSError) {
	aerr = remapStorePressure(aerr, errSlowDown)
	recordAWSError(w, aerr)
	reqID := RequestIDFromContext(r.Context())

	errType := "Sender"
	if aerr.HTTPStatus >= http.StatusInternalServerError {
		errType = "Receiver"
	}
	body, _ := xml.Marshal(&queryXMLErrorResponse{
		Xmlns: namespace,
		Error: queryXMLError{
			Type:    errType,
			Code:    aerr.Code,
			Message: aerr.Message,
		},
		RequestID: reqID,
	})

	// Drain the request body so the HTTP/1.1 connection can be reused by the
	// SDK client.
	if r.Body != nil {
		io.Copy(io.Discard, r.Body) //nolint:errcheck
		r.Body.Close()              //nolint:errcheck
	}

	full := append([]byte(xml.Header), body...)
	w.Header().Set("Content-Type", "application/xml")
	w.Header().Set("Content-Length", strconv.Itoa(len(full)))
	w.Header().Set("x-amz-request-id", reqID)
	if aerr.HTTPStatus == http.StatusNotImplemented {
		w.Header().Set("x-emulator-unsupported", "true")
	}
	w.WriteHeader(aerr.HTTPStatus)
	w.Write(full)
}

// WriteEC2QueryXMLError writes an EC2 Query-protocol XML error response.
func WriteEC2QueryXMLError(w http.ResponseWriter, r *http.Request, aerr *AWSError) {
	aerr = remapStorePressure(aerr, errThrottling)
	recordAWSError(w, aerr)
	reqID := RequestIDFromContext(r.Context())
	body, _ := xml.Marshal(&ec2QueryXMLErrorResponse{
		Errors: []ec2QueryXMLError{{
			Code:    aerr.Code,
			Message: aerr.Message,
		}},
		RequestID: reqID,
	})
	if r.Body != nil {
		io.Copy(io.Discard, r.Body) //nolint:errcheck
		r.Body.Close()              //nolint:errcheck
	}
	full := append([]byte(xml.Header), body...)
	w.Header().Set("Content-Type", "text/xml")
	w.Header().Set("Content-Length", strconv.Itoa(len(full)))
	w.Header().Set("x-amzn-requestid", reqID)
	if aerr.HTTPStatus == http.StatusNotImplemented {
		w.Header().Set("x-emulator-unsupported", "true")
	}
	w.WriteHeader(aerr.HTTPStatus)
	w.Write(full)
}

// NotImplementedQueryXML writes a 501 for unimplemented Query-protocol operations.
func NotImplementedQueryXML(w http.ResponseWriter, r *http.Request) {
	WriteQueryXMLError(w, r, ErrNotImplemented)
}

// NotImplementedEC2QueryXML writes a 501 for unimplemented EC2 Query operations.
func NotImplementedEC2QueryXML(w http.ResponseWriter, r *http.Request) {
	WriteEC2QueryXMLError(w, r, ErrNotImplemented)
}
