package s3

import (
	"net/http"
	"net/url"

	"github.com/go-chi/chi/v5"

	"github.com/overcast-sh/overcast/internal/clock"
	"github.com/overcast-sh/overcast/internal/config"
	"github.com/overcast-sh/overcast/internal/events"
	"github.com/overcast-sh/overcast/internal/protocol"
	"github.com/overcast-sh/overcast/internal/serviceutil"
	"github.com/overcast-sh/overcast/internal/state"
)

// objectKey extracts the object key from a chi wildcard route.
// chi's "*" param returns the path after /{bucket}/, so "/my-bucket/a/b/c"
// yields "a/b/c". This is needed because chi's {key:.+} doesn't match slashes.
//
// chi routes on r.URL.RawPath whenever that is set, and net/url sets it only
// when Go's canonical escaping of the decoded path differs from what the client
// sent. So the wildcard arrives already decoded for `%20` or multi-byte UTF-8 —
// Go re-escapes those identically — but still percent-encoded for characters Go
// leaves bare in a path, notably `+`, `=` and `&`. Unescaping exactly when
// RawPath is set is what keeps `a%2Bb.txt` from being stored under the literal
// key `a%2Bb.txt`, without double-decoding a key that really does contain a
// percent sign (`a%2520b` decodes to `a%20b` and stops there, because net/url
// re-escapes that back to what the client sent and so leaves RawPath empty).
func objectKey(r *http.Request) string {
	key := chi.URLParam(r, "*")
	if r.URL.RawPath == "" {
		return key
	}
	decoded, err := url.PathUnescape(key)
	if err != nil {
		return key
	}
	return decoded
}

// Handler holds the dependencies for S3 HTTP handlers.
// All handler methods hang off this struct — this is the standard Go pattern
// for grouping related handlers (equivalent to a TypeScript class with methods).
type Handler struct {
	cfg   *config.Config
	store *s3Store
	log   *serviceutil.ServiceLogger
	clk   clock.Clock
	bus   *events.Bus

	// lambdaAuth answers whether s3.amazonaws.com may invoke a Lambda
	// notification destination. Set by InitNotifications; nil when the server
	// was wired without Lambda.
	lambdaAuth events.FunctionInvokeAuthorizer

	// lifecycle caches every bucket's lifecycle configuration so the object
	// routes cost an atomic load rather than a store read. See lifecycle.go.
	lifecycle lifecycleIndex

	// objectLocks serialises a conditional write's check-then-commit against
	// other conditional writes to the same bucket+key, which is what makes
	// If-None-Match: * the mutual-exclusion primitive AWS documents. Only a
	// request carrying a condition takes one, so the unconditional PutObject
	// path is unchanged. See conditional_write.go.
	objectLocks serviceutil.RecordLocks

	// bucketLocks makes CreateBucket's existence check and store one step
	// against every other create of the same name (#2126), so concurrent
	// creators — clients, and in-process callers of EnsureBucket — produce one
	// bucket and one S3BucketCreated event. See createBucket.
	bucketLocks serviceutil.RecordLocks

	bucketGetRoutes    []s3Route
	bucketPutRoutes    []s3Route
	bucketDeleteRoutes []s3Route
	bucketPostRoutes   []s3Route
	objectGetRoutes    []s3Route
	objectPutRoutes    []s3Route
	objectDeleteRoutes []s3Route
	objectPostRoutes   []s3Route
}

// s3Route maps a query-parameter name to its handler.
// Order matters: the first matching param wins (mirrors AWS priority behaviour).
type s3Route struct {
	param string
	fn    http.HandlerFunc
}

// dispatchByQuery iterates routes in order and calls the first handler whose
// query param is present. Falls back to fallback if none match.
func dispatchByQuery(w http.ResponseWriter, r *http.Request, routes []s3Route, fallback http.HandlerFunc) {
	for _, rt := range routes {
		if serviceutil.HasQueryParam(r, rt.param) {
			rt.fn(w, r)
			return
		}
	}
	fallback(w, r)
}

func newHandler(cfg *config.Config, store state.Store, log *serviceutil.ServiceLogger, clk clock.Clock, bus *events.Bus) *Handler {
	h := &Handler{
		cfg:   cfg,
		store: newS3Store(store, cfg.DataDir),
		log:   log,
		clk:   clk,
		bus:   bus,
	}

	h.initBucketRoutes()
	h.initObjectRoutes()

	return h
}

// ---- Bucket dispatchers ---------------------------------------------------

// BucketGet dispatches GET /{bucket} by sub-resource query param.
// Guarded by x-amz-expected-bucket-owner (see expected_owner.go) — every
// sub-resource here, and the ListObjectsV1 fallback, targets an existing
// bucket.
func (h *Handler) BucketGet(w http.ResponseWriter, r *http.Request) {
	if !h.checkExpectedBucketOwner(w, r) {
		return
	}
	dispatchByQuery(w, r, h.bucketGetRoutes, h.ListObjectsV1)
}

// BucketPut dispatches PUT /{bucket} by sub-resource query param.
//
// CreateBucket — the fallback when no sub-resource query param matches — is
// deliberately NOT guarded by x-amz-expected-bucket-owner: AWS documents
// CreateBucket as one of the operations that ignores the header, since there
// is no existing bucket yet to compare an owner against. Every sub-resource
// PUT in bucketPutRoutes targets an existing bucket, so those are guarded
// like any other bucket/object operation. This is why BucketPut cannot use
// the same "guard once, then dispatchByQuery" shape as the other dispatchers
// below — the guard has to sit inside the loop, not in front of it.
func (h *Handler) BucketPut(w http.ResponseWriter, r *http.Request) {
	for _, rt := range h.bucketPutRoutes {
		if serviceutil.HasQueryParam(r, rt.param) {
			if !h.checkExpectedBucketOwner(w, r) {
				return
			}
			rt.fn(w, r)
			return
		}
	}
	h.CreateBucket(w, r)
}

// BucketDelete dispatches DELETE /{bucket} by sub-resource query param.
// Guarded by x-amz-expected-bucket-owner — the DeleteBucket fallback and
// every sub-resource here target an existing bucket.
func (h *Handler) BucketDelete(w http.ResponseWriter, r *http.Request) {
	if !h.checkExpectedBucketOwner(w, r) {
		return
	}
	dispatchByQuery(w, r, h.bucketDeleteRoutes, h.DeleteBucket)
}

// BucketPost dispatches POST /{bucket} by sub-resource query param.
// Guarded by x-amz-expected-bucket-owner.
func (h *Handler) BucketPost(w http.ResponseWriter, r *http.Request) {
	if !h.checkExpectedBucketOwner(w, r) {
		return
	}
	dispatchByQuery(w, r, h.bucketPostRoutes, protocol.NotImplementedXML)
}

// ListObjectsV2OrLocation dispatches GET /{bucket} based on query parameters:
// ?list-type=2        → ListObjectsV2
// ?location           → GetBucketLocation
// (no params)         → ListObjectsV2 (default)
//
// Deprecated: use BucketGet which handles all S3 bucket-level query params.
func (h *Handler) ListObjectsV2OrLocation(w http.ResponseWriter, r *http.Request) {
	h.BucketGet(w, r)
}

// ---- Object handlers -------------------------------------------------------

// ObjectGet dispatches GET /{bucket}/{key} by sub-resource query param.
// Guarded by x-amz-expected-bucket-owner.
func (h *Handler) ObjectGet(w http.ResponseWriter, r *http.Request) {
	if !h.checkExpectedBucketOwner(w, r) {
		return
	}
	dispatchByQuery(w, r, h.objectGetRoutes, h.GetObject)
}

// ObjectDelete dispatches DELETE /{bucket}/{key} by sub-resource query param.
// Guarded by x-amz-expected-bucket-owner.
func (h *Handler) ObjectDelete(w http.ResponseWriter, r *http.Request) {
	if !h.checkExpectedBucketOwner(w, r) {
		return
	}
	dispatchByQuery(w, r, h.objectDeleteRoutes, h.DeleteObject)
}

// ObjectPost dispatches POST /{bucket}/{key} by sub-resource query param.
// POST on an object is only an operation when a subresource selects one —
// ?uploads, ?uploadId=, ?restore, ?select. Without one there is no such AWS
// operation, so the fallback is MethodNotAllowed rather than NotImplemented:
// the latter would claim a gap in this emulator for a request real S3 refuses
// too, sending a caller after a workaround that does not exist.
// Guarded by x-amz-expected-bucket-owner.
func (h *Handler) ObjectPost(w http.ResponseWriter, r *http.Request) {
	if !h.checkExpectedBucketOwner(w, r) {
		return
	}
	dispatchByQuery(w, r, h.objectPostRoutes, protocol.MethodNotAllowedXML)
}

// PutObjectOrCopy dispatches PUT /{bucket}/{key}.
// partNumber is checked first because it requires a secondary header discriminant
// that the route table can't express. All other sub-resources use the table.
//
// Guarded by x-amz-expected-bucket-owner (destination bucket, always) and, on
// a copy (x-amz-copy-source present — CopyObject or UploadPartCopy), by
// x-amz-source-expected-bucket-owner (source bucket) as well. Both checks
// happen up front, before any branch below runs, so a denial never reaches
// PutObject/CopyObject/UploadPart/UploadPartCopy.
func (h *Handler) PutObjectOrCopy(w http.ResponseWriter, r *http.Request) {
	if !h.checkExpectedBucketOwner(w, r) {
		return
	}
	copySource := r.Header.Get("x-amz-copy-source")
	if copySource != "" && !h.checkExpectedSourceBucketOwner(w, r, copySource) {
		return
	}

	if serviceutil.HasQueryParam(r, "partNumber") {
		if copySource != "" {
			h.UploadPartCopy(w, r)
		} else {
			h.UploadPart(w, r)
		}
		return
	}
	dispatchByQuery(w, r, h.objectPutRoutes, func(w http.ResponseWriter, r *http.Request) {
		if copySource != "" {
			h.CopyObject(w, r)
		} else {
			h.PutObject(w, r)
		}
	})
}

// ---- Root-level dispatcher -------------------------------------------------

// RootGet dispatches GET / — either ListBuckets or ListDirectoryBuckets.
// Deliberately NOT guarded by x-amz-expected-bucket-owner: AWS documents
// ListBuckets as ignoring the header (there is no single target bucket to
// compare an owner against), and ListDirectoryBuckets is the same shape of
// operation. See expected_owner.go.
func (h *Handler) RootGet(w http.ResponseWriter, r *http.Request) {
	if serviceutil.HasQueryParam(r, "directory-buckets") {
		h.ListDirectoryBuckets(w, r)
		return
	}
	h.ListBuckets(w, r)
}
