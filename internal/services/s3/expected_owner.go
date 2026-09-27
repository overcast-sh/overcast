package s3

// expected_owner.go implements S3's "bucket owner condition" —
// x-amz-expected-bucket-owner (ExpectedBucketOwner / --expected-bucket-owner
// in the SDKs/CLI) and its CopyObject/UploadPartCopy sibling,
// x-amz-source-expected-bucket-owner (--expected-source-bucket-owner). AWS
// docs: https://docs.aws.amazon.com/AmazonS3/latest/userguide/bucket-owner-condition.html
//
// AWS accepts the header on nearly every S3 bucket and object operation and
// fails closed with 403 AccessDenied when the 12-digit account ID it carries
// does not match the bucket's real owner — the guard against an application
// operating on a wrong-but-existing bucket name in the global namespace.
// CreateBucket, ListBuckets and S3 Control operations are documented
// exceptions where AWS *ignores* the header instead of enforcing it; Overcast
// has no S3 Control operations, so only the first two need excluding.
//
// This file holds the one shared guard (checkExpectedBucketOwner /
// checkExpectedSourceBucketOwner) and the operations it skips. Handler.dispatch
// calls it before every operation's handler, so it is never duplicated inside
// the ~100 individual operation handlers.

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/overcast-sh/overcast/internal/protocol"
)

// ignoresExpectedBucketOwner are the operations AWS documents as ignoring
// x-amz-expected-bucket-owner: CreateBucket has no bucket yet to compare an
// owner against, and ListBuckets no single bucket. ListDirectoryBuckets is the
// same shape of operation, and WriteGetObjectResponse addresses no bucket at
// all: its model has no ExpectedBucketOwner member.
var ignoresExpectedBucketOwner = map[string]bool{
	"CreateBucket":           true,
	"ListBuckets":            true,
	"ListDirectoryBuckets":   true,
	"WriteGetObjectResponse": true,
}

// copiesObject are the operations that read the object x-amz-copy-source
// names, whose bucket x-amz-source-expected-bucket-owner guards.
var copiesObject = map[string]bool{
	"CopyObject":     true,
	"UploadPartCopy": true,
}

const (
	expectedBucketOwnerHeader       = "x-amz-expected-bucket-owner"
	expectedSourceBucketOwnerHeader = "x-amz-source-expected-bucket-owner"
)

// checkExpectedBucketOwner enforces x-amz-expected-bucket-owner against the
// {bucket} the current route targets. Returns true if the caller should
// proceed; false if a terminal response has already been written (403
// AccessDenied, or an InternalError on a store failure) and the caller must
// return immediately without doing any further work.
func (h *Handler) checkExpectedBucketOwner(w http.ResponseWriter, r *http.Request) bool {
	return h.checkExpectedOwner(w, r, expectedBucketOwnerHeader, chi.URLParam(r, "bucket"))
}

// checkExpectedSourceBucketOwner enforces x-amz-source-expected-bucket-owner
// against the source bucket named in an x-amz-copy-source header (CopyObject,
// UploadPartCopy). copySource is the raw x-amz-copy-source header value. If
// it does not parse as "<bucket>/<key>", the check is skipped so the
// operation's own copy-source validation — which runs afterwards and already
// knows how to report "Invalid copy source" — is what the caller sees,
// rather than this guard failing on a bucket name it was never able to
// determine.
func (h *Handler) checkExpectedSourceBucketOwner(w http.ResponseWriter, r *http.Request, copySource string) bool {
	if r.Header.Get(expectedSourceBucketOwnerHeader) == "" {
		return true // zero-cost when absent: no parse, no store read.
	}
	srcBucket, _, ok := parseCopySource(copySource)
	if !ok {
		return true
	}
	return h.checkExpectedOwner(w, r, expectedSourceBucketOwnerHeader, srcBucket)
}

// checkExpectedOwner is the shared implementation behind both headers above.
// Overcast is single-account, so "the expected owner" of any bucket that
// exists is always h.cfg.AccountID — there is no per-bucket owner to look up
// beyond that.
func (h *Handler) checkExpectedOwner(w http.ResponseWriter, r *http.Request, headerName, bucket string) bool {
	owner := r.Header.Get(headerName)
	if owner == "" {
		return true // header absent: no-op, and the common case — checked first.
	}

	// UNVERIFIED AGAINST REAL AWS: ordering vs a missing bucket. AWS's docs do
	// not say whether the owner check or NoSuchBucket fires first for a
	// bucket that does not exist. Modeled here as NoSuchBucket winning: the
	// owner check inherently needs an existing bucket to compare an owner
	// against, so skip the guard and let the handler's own NoSuchBucket path
	// fire, rather than this guard reporting AccessDenied for a bucket name
	// that was never going to be reachable anyway.
	exists, aerr := h.store.bucketExists(r.Context(), bucket)
	if aerr != nil {
		protocol.WriteXMLError(w, r, aerr)
		return false
	}
	if !exists {
		return true
	}

	// UNVERIFIED AGAINST REAL AWS: the response to a malformed (non-12-digit)
	// header value is undocumented. Modeled as 403 AccessDenied — the same
	// response a well-formed but wrong account ID gets — since a malformed
	// value can never equal the real owner's account ID either. A plain
	// inequality check covers both cases without a separate format
	// validation step.
	if owner != h.cfg.AccountID {
		protocol.WriteXMLError(w, r, errAccessDeniedBucketOwner())
		return false
	}
	return true
}

func errAccessDeniedBucketOwner() *protocol.AWSError {
	return &protocol.AWSError{
		Code:       "AccessDenied",
		Message:    "Access Denied",
		HTTPStatus: http.StatusForbidden,
	}
}
