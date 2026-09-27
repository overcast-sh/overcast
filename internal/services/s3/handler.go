package s3

import (
	"net/http"
	"net/url"

	"github.com/go-chi/chi/v5"

	"github.com/overcast-sh/overcast/internal/clock"
	"github.com/overcast-sh/overcast/internal/config"
	"github.com/overcast-sh/overcast/internal/events"
	"github.com/overcast-sh/overcast/internal/protocol"
	"github.com/overcast-sh/overcast/internal/s3route"
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

	// operations maps each operation s3route can name onto its handler.
	operations operationHandlers
}

// operationHandlers maps S3 operation names onto their handlers. It is a named
// type rather than a map[string]http.HandlerFunc literal because capgen reads
// such a literal as a service's whole dispatch and requires a capability row
// for every key, and most of S3's 501 stubs have none yet (#2287).
type operationHandlers map[string]http.HandlerFunc

func newHandler(cfg *config.Config, store state.Store, log *serviceutil.ServiceLogger, clk clock.Clock, bus *events.Bus) *Handler {
	h := &Handler{
		cfg:   cfg,
		store: newS3Store(store, cfg.DataDir),
		log:   log,
		clk:   clk,
		bus:   bus,
	}

	h.operations = h.bucketOperations()
	for operation, serve := range h.objectOperations() {
		h.operations[operation] = serve
	}
	return h
}

// dispatch serves r as the operation s3route names it, which is the name IAM
// enforcement authorises and the request log records: S3 has no other
// dispatch, so what is authorised, logged and served cannot differ (#2284).
// A request s3route refuses, one naming a sub-resource on a method S3 serves
// it on none, is MethodNotAllowed. unserved answers any other request
// s3route names no operation: a POST to a bucket or an object without a
// sub-resource that selects one.
//
// Every operation is guarded by x-amz-expected-bucket-owner except the ones AWS
// documents as ignoring it (see expected_owner.go), and a copy is guarded by
// x-amz-source-expected-bucket-owner as well. Both checks run before the
// operation's handler, so a denial never reaches it.
func (h *Handler) dispatch(unserved http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		operation := s3route.Operation(r)
		if !ignoresExpectedBucketOwner[operation] && !h.checkExpectedBucketOwner(w, r) {
			return
		}
		if copiesObject[operation] && !h.checkExpectedSourceBucketOwner(w, r, r.Header.Get(s3route.CopySourceHeader)) {
			return
		}
		if serve, ok := h.operations[operation]; ok {
			serve(w, r)
			return
		}
		if s3route.Refused(r) {
			protocol.MethodNotAllowedXML(w, r)
			return
		}
		unserved(w, r)
	}
}
