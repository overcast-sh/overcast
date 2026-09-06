package scenario

import (
	"net/http"
	"reflect"
	"strings"

	"github.com/aws/smithy-go"
	smithyhttp "github.com/aws/smithy-go/transport/http"
	"github.com/overcast-sh/overcast-compat-go-sdk/internal/harness"
)

// This file's counterpart is the error-matching section of
// compat/suites/cli/internal/scenario/executor.go (matchesError, errorCodes,
// errorCodeSpellings) rather than a same-named file: the CLI suite has no
// separate errors.go because it reads codes out of stderr instead of a typed
// error chain. The two apply the same equality rule (below) over their own
// backend's error surfaces and are not byte-identical, but a change to the
// rule here usually needs a matching change there — change both or neither.

// Error matching (compat/model/README.md § Errors).
//
// A clause carries both the modeled shape and the wire code, because SDKs
// disagree about which of the two they surface — for SQS's not-found,
// QueueDoesNotExist and AWS.SimpleQueueService.NonExistentQueue — so either is
// accepted, but by **equality** against a code parsed out of a surface this
// SDK actually has, never by containment over the whole message. Containment
// cannot tell a code from a code that ends with it: a clause naming
// NotFoundException would be satisfied by a ResourceNotFoundException, which
// is a different error from a different branch of the service, and by the word
// appearing anywhere in the SDK's prose.
//
// The surfaces the AWS SDK for Go v2 gives us, and where each comes from:
//
//	smithy.APIError.ErrorCode()   the code the protocol deserializer resolved —
//	                              the AWS JSON protocols' __type, the REST JSON
//	                              body's `code`, and the Code inside an XML
//	                              error node: the Query protocol's
//	                              <ErrorResponse><Error><Code> and REST XML's
//	                              bare <Error><Code>, both resolved by the SDK's
//	                              own awsxml.GetErrorResponseComponents. This is
//	                              the bodyType and bodyCode carriers, and it is
//	                              why nothing in this file reads a body: the
//	                              deserializer has already found the code at
//	                              whichever depth its protocol states it.
//	the Go type name of the error the modeled exception type smithy-go minted for a
//	                              modeled error (*types.QueueDoesNotExist), read
//	                              off the error chain rather than named in an
//	                              errors.As per shape, so no generated file has
//	                              to import a service's types package to match
//	                              one. This is the exceptionName carrier.
//	x-amzn-query-error            the header an awsQueryCompatible service sends,
//	                              as <code>;<Sender|Receiver>, reached through
//	                              the transport error's HTTP response.
//
// When none of them states a code the clause does **not** match. There is no
// containment fallback, and the absence of one is the rule rather than an
// omission: an error with no code surface is no evidence that the service
// raised the named error, and matching it by containment would reinstate the
// near miss this equality exists to exclude, on exactly the inputs where
// nothing has checked the string's shape.

// matchesError reports whether a failed call carries the error a clause names.
func matchesError(err error, want *ErrorSpec) bool {
	if err == nil || want == nil {
		return false
	}
	for _, got := range errorCodes(err) {
		if (want.Shape != "" && got == want.Shape) || (want.Code != "" && got == want.Code) {
			return true
		}
	}
	return false
}

// errorCodes returns every code the error states, in every spelling a clause
// may name it by, or nil when it states none.
func errorCodes(err error) []string {
	var codes []string
	add := func(code string) {
		if code == "" {
			return
		}
		codes = append(codes, errorCodeSpellings(code)...)
	}
	for _, e := range chain(err) {
		// The chain is walked link by link, so a direct assertion on each link
		// covers exactly what errors.As over the whole chain would — without
		// counting one wrapped API error once per link that wraps it.
		if api, ok := e.(smithy.APIError); ok {
			add(api.ErrorCode())
		}
		add(modeledTypeName(e))
		if header, ok := httpHeader(e); ok {
			add(header.Get(queryErrorHeader))
		}
	}
	return codes
}

// queryErrorHeader is the header an awsQueryCompatible service returns
// alongside the JSON body, carrying the legacy Query code with a fault suffix.
const queryErrorHeader = "x-amzn-query-error"

// errorCodeSpellings returns one observed code in every spelling a clause may
// name it by, which is the list compat/model/README.md § Errors fixes: the
// value itself, what follows the last "#" of a Smithy id
// (com.amazonaws.sqs#QueueDoesNotExist states the same code as
// QueueDoesNotExist), and what precedes the first ";" of the <code>;<fault>
// form the x-amzn-query-error header uses.
//
// Splitting at those separators and nowhere else is what keeps the match an
// equality: no spelling of ResourceNotFoundException is NotFoundException.
func errorCodeSpellings(code string) []string {
	out := []string{code}
	if i := strings.LastIndex(code, "#"); i >= 0 {
		out = append(out, code[i+1:])
	}
	if i := strings.Index(code, ";"); i >= 0 {
		out = append(out, code[:i])
	}
	return out
}

// modeledTypeName is the exceptionName surface: the name of the Go type
// smithy-go generated for a modeled error shape, which is the shape name.
//
// It is read off the concrete type rather than through an errors.As against
// each candidate type, because the alternative is a generated file importing
// every service types package it might have to name — and because the clause
// names a shape, not a Go type, so the comparison is between two strings
// either way.
//
// Only a type from a generated SDK package counts. smithy-go's own
// *smithy.GenericAPIError is named after what it is rather than after a
// modeled shape, and isModeledErrorType's switch below excludes it by name.
// *smithy.OperationError never reaches that switch at all: it has no
// ErrorCode/ErrorMessage/ErrorFault, so it fails isModeledErrorType's own
// smithy.APIError assertion before the switch is reached, and "OperationError"
// can never satisfy a clause either way.
func modeledTypeName(err error) string {
	t := reflect.TypeOf(err)
	for t != nil && t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t == nil || t.Name() == "" {
		return ""
	}
	if !isModeledErrorType(err) {
		return ""
	}
	return t.Name()
}

// isModeledErrorType reports whether an error is one an SDK minted for a
// modeled error shape: it implements smithy.APIError in its own right (not
// through something it wraps) and is not smithy-go's own catch-all
// *smithy.GenericAPIError.
func isModeledErrorType(err error) bool {
	api, ok := err.(smithy.APIError)
	if !ok {
		return false
	}
	switch api.(type) {
	case *smithy.GenericAPIError:
		return false
	}
	return true
}

// httpHeader reaches the transport error's response headers, which is where
// the x-amzn-query-error header survives deserialization: the body is parsed
// away before the caller sees the error, and the header is not.
//
// The behaviour is named rather than the type, because two types carry it and
// they disagree about the return: smithy-go's transport/http.ResponseError
// answers with its own *Response wrapper (aws/transport/http.ResponseError
// embeds it and inherits the method), while a plainer transport error may
// answer with the standard one. Both are accepted so an SDK reshuffle changes
// nothing here.
func httpHeader(err error) (http.Header, bool) {
	// nolint:bodyclose — this is a response the SDK already read and closed on
	// its way to building the error; only its headers are looked at, and
	// closing a body the middleware stack owns would be wrong.
	switch h := err.(type) {
	case interface{ HTTPResponse() *smithyhttp.Response }:
		if resp := h.HTTPResponse(); resp != nil && resp.Response != nil { //nolint:bodyclose
			return resp.Header, true
		}
	case interface{ HTTPResponse() *http.Response }:
		if resp := h.HTTPResponse(); resp != nil { //nolint:bodyclose
			return resp.Header, true
		}
	}
	return nil, false
}

// isUnimplementedResponse reports whether the emulator answered 501.
//
// The status code is read from the transport error where there is one, which
// is exact. Only when there is no HTTP response at all — the SDK failed before
// or after the exchange — does this fall back to the harness's substring
// heuristic over the SDK's own text, which is what that heuristic is for.
func isUnimplementedResponse(err error) bool {
	for _, e := range chain(err) {
		if s, ok := e.(interface{ HTTPStatusCode() int }); ok {
			return s.HTTPStatusCode() == http.StatusNotImplemented
		}
	}
	return harness.LooksUnimplemented(err.Error())
}

// chain flattens an error chain, following both Unwrap forms. The AWS SDK
// wraps a modeled error in an *smithy.OperationError and a transport error, so
// the surfaces a clause reads are spread across three links.
func chain(err error) []error {
	var out []error
	var walk func(error)
	walk = func(e error) {
		for e != nil {
			out = append(out, e)
			switch u := e.(type) {
			case interface{ Unwrap() error }:
				e = u.Unwrap()
			case interface{ Unwrap() []error }:
				for _, child := range u.Unwrap() {
					walk(child)
				}
				return
			default:
				return
			}
		}
	}
	walk(err)
	return out
}
