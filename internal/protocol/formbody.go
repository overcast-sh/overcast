package protocol

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"strconv"
)

// MaxQueryFormBody bounds how much of a request body is ever read into memory
// in order to treat it as an AWS Query form — by ParseFormPreservingBody here,
// and by the Query codec's direct-body-read fallback. Real Query requests are
// far smaller: the largest inline payloads AWS accepts on this protocol are SNS
// Publish and SQS SendMessageBatch at 256 KiB and a CloudFormation inline
// TemplateBody at 51,200 bytes. This is generous headroom, not a realistic limit
// for legitimate traffic. Anything past it is not a Query request, and buffering
// it would mean holding an arbitrarily large S3 upload in memory.
const MaxQueryFormBody = 1 << 20 // 1 MiB

// ErrFormBodyTooLarge reports that a request body exceeded MaxQueryFormBody, so
// its form fields were not parsed. The body is left intact and readable.
//
// It is not a rejection. A request that trips it is re-parsed in full once
// routing has established that it really is Query traffic — see
// MaxQueryRequestBody — so a body between the two limits is dispatched
// normally. Callers must treat this as "not parsed here", never as "too large
// to serve": SQS accepts a 1 MiB MessageBody, which URL-encodes to more than
// MaxQueryFormBody on its own, and refusing that request would be wrong.
var ErrFormBodyTooLarge = errors.New("protocol: request body exceeds the AWS Query form parse limit")

// MaxQueryRequestBody bounds a complete AWS Query request body, as opposed to
// the speculative prefix MaxQueryFormBody buffers before routing. It is the
// point past which the emulator will not parse a form at all.
//
// The value matches net/http's own undocumented ParseForm ceiling, which is
// what enforced this bound before it was named. That is deliberate: making it
// explicit changes no request's outcome, only what an already-refused request
// is told. Legitimate Query traffic is far below it — the largest inline
// payload any Query service accepts is SQS's 1 MiB message body, at most 3 MiB
// once URL-encoded.
const MaxQueryRequestBody = 10 << 20 // 10 MiB

// QueryFormParseError converts a failed Query form parse into the error a
// client should see.
//
// The distinction it exists to draw is between an operation the emulator has
// not implemented and a request the emulator will not accept. Falling through
// to NotImplemented conflates them, and the 501 it produces carries
// x-emulator-unsupported — telling tooling that Overcast lacks CreateStack
// when the truth is that this particular body was too big to parse. See
// ErrMethodNotAllowed for the same line drawn against a method AWS refuses.
func QueryFormParseError(err error) *AWSError {
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		return &AWSError{
			Code: "RequestEntityTooLarge",
			Message: "Request body exceeds the maximum size of " +
				strconv.Itoa(MaxQueryRequestBody) + " bytes.",
			HTTPStatus: http.StatusRequestEntityTooLarge,
		}
	}
	return &AWSError{
		Code:       "MalformedQueryString",
		Message:    "The request body could not be parsed as an AWS Query form.",
		HTTPStatus: http.StatusBadRequest,
	}
}

// ParseFormPreservingBody populates r.Form and r.PostForm the way
// (*http.Request).ParseForm does, but leaves r.Body readable afterwards.
//
// This exists because Content-Type is not a routing decision. The standard
// library's ParseForm drains r.Body for any POST/PUT/PATCH labelled
// application/x-www-form-urlencoded, so shared code that sniffs a request for
// AWS Query fields — protocol detection, IAM action extraction — would silently
// swallow the payload of a request that turned out to belong to a REST service.
// S3 PutObject is the case that bites: `curl --data-binary` sends that content
// type by default, and AWS stores the body regardless.
//
// Callers that only ever see genuine Query traffic can keep using ParseForm;
// anything running ahead of routing must use this.
func ParseFormPreservingBody(r *http.Request) error {
	// ParseForm leaves PostForm non-nil on every path, so this both makes the
	// call idempotent and avoids re-buffering a body a previous call consumed.
	if r.PostForm != nil {
		return nil
	}
	if r.Body == nil || r.Body == http.NoBody {
		return r.ParseForm()
	}

	// One byte past the limit is enough to tell "at the limit" from "over" it.
	buffered, err := io.ReadAll(io.LimitReader(r.Body, MaxQueryFormBody+1))
	if err != nil {
		r.Body = &preservedBody{Reader: bytes.NewReader(buffered), closer: r.Body}
		return err
	}
	if len(buffered) > MaxQueryFormBody {
		r.Body = &preservedBody{
			Reader: io.MultiReader(bytes.NewReader(buffered), r.Body),
			closer: r.Body,
		}
		return ErrFormBodyTooLarge
	}

	original := r.Body
	r.Body = io.NopCloser(bytes.NewReader(buffered))
	parseErr := r.ParseForm()
	r.Body = &preservedBody{Reader: bytes.NewReader(buffered), closer: original}
	return parseErr
}

// ParseQueryForm parses the form of a request that routing has established is
// AWS Query traffic, reading up to MaxQueryRequestBody of it.
//
// A body within MaxQueryFormBody is parsed by ParseFormPreservingBody, which
// leaves it readable. A larger one is read in full, which consumes it: only a
// caller that knows the request is Query traffic may do that, because anything
// else would lose an S3 payload. QueryFormParseError says how to answer a
// failure.
func ParseQueryForm(w http.ResponseWriter, r *http.Request) error {
	if err := ParseFormPreservingBody(r); !errors.Is(err, ErrFormBodyTooLarge) {
		return err
	}
	r.Body = http.MaxBytesReader(w, r.Body, MaxQueryRequestBody)
	return r.ParseForm()
}

// preservedBody hands the handler a replayable body while keeping the original
// Close, so the server still releases the connection's read side.
type preservedBody struct {
	io.Reader
	closer io.Closer
}

func (b *preservedBody) Close() error { return b.closer.Close() }
