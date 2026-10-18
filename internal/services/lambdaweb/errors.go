package lambdaweb

// Data-plane errors: the lambda-web-error-type values and statuses of the
// "Web Function error types" table in the Lambda Developer Guide
// (web-functions-troubleshooting), plus the pre-routing statuses the HTTP
// page documents.

import "net/http"

// Error type values, as sent in the lambda-web-error-type response header.
const (
	errTypeMalformedRequest       = "Platform.MalformedRequest"
	errTypeMissingHostHeader      = "Platform.MissingHostHeader"
	errTypeInvalidEndpoint        = "Platform.InvalidEndpoint"
	errTypeHeaderTooLarge         = "Platform.HeaderTooLarge"
	errTypeDisallowedHeader       = "Platform.DisallowedHeader"
	errTypeUnsupportedHTTPVersion = "Platform.UnsupportedHttpVersion"
	errTypeCannotAssumeRole       = "Platform.CannotAssumeRole"
	errTypeKMSDecryption          = "Platform.KMSDecryptionError"
	errTypeInvalidNetworkConfig   = "Platform.InvalidNetworkConfiguration"
	errTypeRevisionNotActive      = "Platform.RevisionNotActive"
	errTypeEndpointNotActive      = "Platform.EndpointNotActive"
	errTypeEndpointDisabled       = "Platform.EndpointDisabled"
	errTypeAccountDisabled        = "Platform.AccountDisabled"
	errTypeAccountRequestThrottle = "Platform.AccountRequestThrottle"
	errTypeEndpointRequestThrot   = "Platform.EndpointRequestThrottle"
	errTypeAccountVCPUThrottle    = "Platform.AccountActiveVCPUArm64Throttle"
	errTypeEndpointMaxEnvThrottle = "Platform.EndpointMaxEnvThrottle"
	errTypeConcurrentInitThrottle = "Platform.EndpointConcurrentInitThrottle"

	errTypeRequestTimeout = "Runtime.RequestTimeout"
	errTypeOutOfMemory    = "Runtime.OutOfMemory"
	errTypeUnreachable    = "Runtime.Unreachable"
	errTypeExit           = "Runtime.Exit"
	errTypeExitError      = "Runtime.ExitError"
	errTypeInvalidResp    = "Runtime.InvalidResponse"
	errTypeOriginRequest  = "Runtime.OriginRequestError"
	errTypeImportCode     = "Runtime.ImportCodeError"
	errTypeInitTimeout    = "Runtime.InitTimeout"
	errTypeInitCode       = "Application.InitCodeError"
	errTypeTimeoutListen  = "Application.TimeoutListenError"
)

// platformError is a response Lambda itself produces for a data-plane request.
type platformError struct {
	// Status is the HTTP status.
	Status int
	// Type is the lambda-web-error-type value; empty omits the header, which
	// AWS does for internal errors and authorisation failures.
	Type string
	// Message is the response body, plain text.
	Message string
	// RetryAfter, when positive, is sent as Retry-After (seconds) on a throttle.
	RetryAfter int
}

func (e *platformError) Error() string { return e.Type + ": " + e.Message }

// Constructors for the errors the engine raises, so the status that goes with
// each type is written once.
func errMalformed(msg string) *platformError {
	return &platformError{Status: http.StatusBadRequest, Type: errTypeMalformedRequest, Message: msg}
}

func errNoHost() *platformError {
	return &platformError{Status: http.StatusBadRequest, Type: errTypeMissingHostHeader, Message: "The Host header is required."}
}

func errNoEndpoint() *platformError {
	return &platformError{Status: http.StatusNotFound, Type: errTypeInvalidEndpoint, Message: "No endpoint is served at this host."}
}

func errHeadersTooLarge() *platformError {
	return &platformError{Status: http.StatusRequestHeaderFieldsTooLarge, Type: errTypeHeaderTooLarge, Message: "The request headers exceed 128 KiB."}
}

func errHTTPVersion() *platformError {
	return &platformError{Status: http.StatusHTTPVersionNotSupported, Type: errTypeUnsupportedHTTPVersion, Message: "Web Function endpoints require HTTP/1.1."}
}

func errEndpointNotActive() *platformError {
	return &platformError{Status: http.StatusConflict, Type: errTypeEndpointNotActive, Message: "The endpoint is not active."}
}

func errRevisionNotActive() *platformError {
	return &platformError{Status: http.StatusConflict, Type: errTypeRevisionNotActive, Message: "The revision is not active."}
}

func errEndpointDisabled() *platformError {
	return &platformError{Status: http.StatusServiceUnavailable, Type: errTypeEndpointDisabled, Message: "The endpoint is disabled: its rate limit is 0."}
}

// errForbidden is an authorisation failure: 403 and, as AWS does, no error type.
func errForbidden() *platformError {
	return &platformError{Status: http.StatusForbidden, Message: "Forbidden"}
}

func errThrottle(errType, msg string) *platformError {
	return &platformError{Status: http.StatusTooManyRequests, Type: errType, Message: msg, RetryAfter: 1}
}

func errTimeout() *platformError {
	return &platformError{Status: http.StatusGatewayTimeout, Type: errTypeRequestTimeout, Message: "The function did not respond within its timeout."}
}
