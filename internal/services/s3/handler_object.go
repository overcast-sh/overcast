package s3

// handler_object.go contains all fully-implemented object-level S3 handlers
// and the route tables that map sub-resource query params to those handlers.
// Stubs (NotImplementedXML) live in handler_stubs.go.
// Dispatchers (ObjectGet, ObjectDelete, …, PutObjectOrCopy) live in handler.go.

import (
	"context"
	"encoding/xml"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/overcast-sh/overcast/internal/events"
	"github.com/overcast-sh/overcast/internal/protocol"
	"github.com/overcast-sh/overcast/internal/serviceutil"
)

// initObjectRoutes populates the four object-level dispatch tables.
// Called once by newHandler.
func (h *Handler) initObjectRoutes() {
	h.objectGetRoutes = []s3Route{
		{"acl", h.GetObjectAcl},
		{"tagging", h.GetObjectTagging},
		{"attributes", h.GetObjectAttributes},
		{"legal-hold", h.GetObjectLegalHold},
		{"retention", h.GetObjectRetention},
		{"torrent", h.GetObjectTorrent},
		{"uploadId", h.ListParts},
	}

	h.objectPutRoutes = []s3Route{
		{"acl", h.PutObjectAcl},
		{"tagging", h.PutObjectTagging},
		{"legal-hold", h.PutObjectLegalHold},
		{"retention", h.PutObjectRetention},
		{"rename", h.RenameObject},
		{"encryption", h.UpdateObjectEncryption},
	}

	h.objectDeleteRoutes = []s3Route{
		{"tagging", h.DeleteObjectTagging},
		{"uploadId", h.AbortMultipartUpload},
	}

	h.objectPostRoutes = []s3Route{
		{"uploads", h.CreateMultipartUpload},
		{"uploadId", h.CompleteMultipartUpload},
		{"restore", h.RestoreObject},
		{"select", h.SelectObjectContent},
		{"writeGetObjectResponse", h.WriteGetObjectResponse},
	}
}

// defaultObjectContentType is the Content-Type S3 records for an object
// written without one.
const defaultObjectContentType = "application/octet-stream"

// objectContentType is the Content-Type S3 records for an object written with
// contentType, which may be empty.
func objectContentType(contentType string) string {
	if contentType == "" {
		return defaultObjectContentType
	}
	return contentType
}

// storageClassStandard is S3's default object storage class. AWS omits the
// x-amz-storage-class header on responses for objects in it.
const storageClassStandard = "STANDARD"

// objectStorageClasses are the classes S3 accepts on the x-amz-storage-class
// request header. Overcast records the class and stores every object the same
// way — there are no per-class durability or retrieval semantics
// (docs/plans/full-emulation-priority.md §7).
var objectStorageClasses = map[string]bool{
	storageClassStandard:  true,
	"REDUCED_REDUNDANCY":  true,
	"STANDARD_IA":         true,
	"ONEZONE_IA":          true,
	"INTELLIGENT_TIERING": true,
	"GLACIER":             true,
	"GLACIER_IR":          true,
	"DEEP_ARCHIVE":        true,
	"OUTPOSTS":            true,
	"SNOW":                true,
	"EXPRESS_ONEZONE":     true,
}

// requestedStorageClass reads and validates the x-amz-storage-class header.
// An absent header (or an explicit STANDARD) stores nothing, so the object
// reads back as STANDARD; an unrecognised class is refused the way AWS
// refuses it rather than being silently dropped.
func requestedStorageClass(r *http.Request) (string, *protocol.AWSError) {
	raw := r.Header.Get("x-amz-storage-class")
	if raw == "" || raw == storageClassStandard {
		return "", nil
	}
	if !objectStorageClasses[raw] {
		return "", &protocol.AWSError{
			Code:       "InvalidStorageClass",
			Message:    "The storage class you specified is not valid: " + raw,
			HTTPStatus: http.StatusBadRequest,
		}
	}
	return raw, nil
}

// writeObjectStorageClass sets x-amz-storage-class, which AWS sends only when
// the object is not in STANDARD.
func writeObjectStorageClass(w http.ResponseWriter, obj *Object) {
	if class := obj.effectiveStorageClass(); class != storageClassStandard {
		w.Header().Set("x-amz-storage-class", class)
	}
}

// PutObject handles PUT /{bucket}/{key}.
// AWS docs: https://docs.aws.amazon.com/AmazonS3/latest/API/API_PutObject.html
func (h *Handler) PutObject(w http.ResponseWriter, r *http.Request) {
	bucket := chi.URLParam(r, "bucket")
	key := objectKey(r)

	b, aerr := h.store.getBucket(r.Context(), bucket)
	if aerr != nil {
		protocol.WriteXMLError(w, r, aerr)
		return
	}

	storageClass, aerr := requestedStorageClass(r)
	if aerr != nil {
		protocol.WriteXMLError(w, r, aerr)
		return
	}

	cond, aerr := parseConditionalWrite(r)
	if aerr != nil {
		protocol.WriteXMLError(w, r, aerr)
		return
	}
	if cond.active() {
		// Held until the handler returns, so the condition and the write it
		// guards are one step against any other conditional writer for this
		// key: a check that released before the write committed would let a
		// loser overwrite the winner's object.
		defer h.objectLocks.Lock(objectStoreKey(bucket, key))()
		if aerr := h.checkConditionalWrite(r.Context(), cond, bucket, key); aerr != nil {
			protocol.WriteXMLError(w, r, aerr)
			return
		}
	}

	// Extract x-amz-meta-* headers into the metadata map.
	meta := serviceutil.HeaderPrefix(r, "X-Amz-Meta-")

	obj := &Object{
		Bucket:             bucket,
		Key:                key,
		StorageClass:       storageClass,
		ContentType:        objectContentType(r.Header.Get("Content-Type")),
		LastModified:       h.clk.Now().UTC(),
		Metadata:           meta,
		ContentDisposition: r.Header.Get("Content-Disposition"),
		ContentEncoding:    stripAWSChunkedEncoding(r.Header.Get("Content-Encoding")),
		ContentLanguage:    r.Header.Get("Content-Language"),
		CacheControl:       r.Header.Get("Cache-Control"),
		Expires:            r.Header.Get("Expires"),
	}

	// Decode aws-chunked streaming uploads (SDK for .NET v4, Rust, newer
	// Java) transparently so we store the raw object bytes, not the chunk
	// framing. See aws_chunked.go.
	body, _ := maybeDecodeAWSChunked(r)

	etag, aerr := h.writeObject(r.Context(), b, obj, body)
	if aerr != nil {
		protocol.WriteXMLError(w, r, aerr)
		return
	}

	w.Header().Set("ETag", etag)
	setVersionIDHeader(w, obj)
	// AWS tells the caller, on the write itself, when a lifecycle rule will
	// expire the object it just stored.
	h.setExpirationHeader(r.Context(), w, obj)
	protocol.WriteEmpty(w, r, http.StatusOK)
}

// writeObject is PutObject's write path once the request has been parsed into
// obj: it gives obj the key's next version identity, streams body to disk
// while computing the MD5 ETag in one pass (the body is never fully
// buffered), records the version, and publishes the ObjectCreated:Put event
// the bucket's notifications are delivered from. A body that fails part way
// leaves the key as it was. The HTTP handler and the in-process
// Service.PutObjectStream both call it, so an internal write is
// indistinguishable from a client's.
func (h *Handler) writeObject(ctx context.Context, b *Bucket, obj *Object, body io.Reader) (string, *protocol.AWSError) {
	stamp, aerr := h.beginVersion(ctx, b, obj, obj.LastModified)
	if aerr != nil {
		return "", aerr
	}
	commit := func() *protocol.AWSError { return h.commitObject(ctx, b, obj) }
	if aerr := h.store.storeObject(obj, copyFrom(body), bodyDigest.etag, commit); aerr != nil {
		return "", aerr
	}
	h.publishObjectEvent(ctx, events.S3ObjectCreated, obj, stamp, "ObjectCreated:Put", obj.ContentLength, obj.ETag)
	return obj.ETag, nil
}

// publishObjectEvent emits one object mutation onto the bus with the version
// identity S3 attaches to a notification: the version id (only for a versioned
// bucket) and the sequencer, which consumers compare to order events for a key.
func (h *Handler) publishObjectEvent(ctx context.Context, typ events.Type, obj *Object, stamp versionStamp, eventName string, size int64, etag string) {
	payload := events.S3ObjectPayload{
		Bucket:    obj.Bucket,
		Key:       obj.Key,
		Size:      size,
		ETag:      etag,
		EventName: eventName,
		Sequencer: stamp.sequencer(),
		VersionID: obj.headerVersionID(),
	}
	h.bus.Publish(ctx, events.Event{
		Type:    typ,
		Time:    obj.LastModified,
		Source:  "s3",
		Payload: payload,
	})
}

// GetObject handles GET /{bucket}/{key}.
// AWS docs: https://docs.aws.amazon.com/AmazonS3/latest/API/API_GetObject.html
func (h *Handler) GetObject(w http.ResponseWriter, r *http.Request) {
	bucket := chi.URLParam(r, "bucket")
	key := objectKey(r)

	// Load metadata only — body is streamed separately.
	obj, ok := h.readTarget(w, r, bucket, key)
	if !ok {
		return
	}

	// Open the body file for streaming.
	f, aerr := h.store.openBody(obj)
	if aerr != nil {
		protocol.WriteXMLError(w, r, aerr)
		return
	}
	defer f.Close()

	h.writeObjectReadHeaders(w, r, obj)

	// ETag conditional requests (RFC 7232).
	// If-Match: return 412 if ETags don't match.
	if ifMatch := r.Header.Get("If-Match"); ifMatch != "" && ifMatch != "*" {
		if !etagMatches(obj.ETag, ifMatch) {
			w.WriteHeader(http.StatusPreconditionFailed)
			return
		}
	}
	// If-None-Match: return 304 if ETags match.
	if ifNoneMatch := r.Header.Get("If-None-Match"); ifNoneMatch != "" {
		if ifNoneMatch == "*" || etagMatches(obj.ETag, ifNoneMatch) {
			w.WriteHeader(http.StatusNotModified)
			return
		}
	}

	rng, ok := applyObjectRange(w, r, obj)
	if !ok {
		return
	}
	if rng.partial {
		if _, err := f.Seek(rng.start, io.SeekStart); err != nil {
			protocol.WriteXMLError(w, r, protocol.Wrap(protocol.ErrInternalError, err))
			return
		}
		w.WriteHeader(http.StatusPartialContent)
		_, _ = io.Copy(w, io.LimitReader(f, rng.length()))
		return
	}

	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, f)
}

// writeObjectReadHeaders writes the headers every successful object read
// carries, so GetObject and HeadObject cannot answer the same object
// differently. RFC 9110 §9.3.2 says a HEAD response's header fields SHOULD be
// the ones a GET would have sent, and AWS honours that; HeadObject kept its own
// copy of this list and the copy was missing the x-amz-meta-* loop, so
// head_object read back no user metadata at all (#1704).
func (h *Handler) writeObjectReadHeaders(w http.ResponseWriter, r *http.Request, obj *Object) {
	w.Header().Set("Content-Type", obj.ContentType)
	w.Header().Set("Content-Length", strconv.FormatInt(obj.ContentLength, 10))
	w.Header().Set("ETag", obj.ETag)
	w.Header().Set("Last-Modified", obj.LastModified.UTC().Format(http.TimeFormat))
	w.Header().Set("x-amz-request-id", protocol.RequestIDFromContext(r.Context()))
	setVersionIDHeader(w, obj)
	writeObjectStorageClass(w, obj)
	h.setExpirationHeader(r.Context(), w, obj)

	// Response headers the write supplied, each omitted when it did not.
	setIfNotEmpty(w, "Content-Disposition", obj.ContentDisposition)
	setIfNotEmpty(w, "Content-Encoding", obj.ContentEncoding)
	setIfNotEmpty(w, "Content-Language", obj.ContentLanguage)
	setIfNotEmpty(w, "Cache-Control", obj.CacheControl)
	setIfNotEmpty(w, "Expires", obj.Expires)

	// User metadata. Keys are stored lower-cased, as AWS stores them, and
	// Header.Set canonicalises the name on the way out — both ends are
	// case-insensitive, and every SDK lower-cases them again on the way in.
	for name, value := range obj.Metadata {
		w.Header().Set("x-amz-meta-"+name, value)
	}
}

// setIfNotEmpty sets a response header only when the object recorded a value
// for it — AWS sends no header at all otherwise.
func setIfNotEmpty(w http.ResponseWriter, header, value string) {
	if value != "" {
		w.Header().Set(header, value)
	}
}

// readObjectMeta is the read-side counterpart to getObjectMeta that checks the
// bucket exists first, so a missing bucket reports NoSuchBucket rather than
// NoSuchKey — the same distinction resolveVersion already makes for versioned
// reads. Write paths (PutObject, DeleteObject) resolve the bucket themselves
// before ever reaching the object store and then call lookupObjectMeta/
// getObjectMeta directly, so they are unaffected and stay at one lookup; every
// unversioned read path shares this one extra getBucket call instead of
// duplicating the check. See #1635.
func (h *Handler) readObjectMeta(ctx context.Context, bucket, key string) (*Object, *protocol.AWSError) {
	if _, aerr := h.store.getBucket(ctx, bucket); aerr != nil {
		return nil, aerr
	}
	return h.store.getObjectMeta(ctx, bucket, key)
}

// readTarget resolves the version a GET or HEAD addresses — the key's current
// version, or the one named by ?versionId= — and writes the AWS response itself
// when there is nothing to read, reporting false.
//
// The two "nothing to read" answers are different operations on AWS's side and
// both matter to clients:
//
//   - the current version is a delete marker: 404 NoSuchKey, plus
//     x-amz-delete-marker so a caller can tell a deleted object from one that
//     never existed;
//   - the request names a delete marker's own version id: 405 MethodNotAllowed
//     with Allow: DELETE, because that version exists and simply cannot be read.
func (h *Handler) readTarget(w http.ResponseWriter, r *http.Request, bucket, key string) (*Object, bool) {
	versionID := serviceutil.QueryString(r, "versionId", "")

	if versionID == "" {
		obj, aerr := h.readObjectMeta(r.Context(), bucket, key)
		if aerr != nil {
			protocol.WriteXMLError(w, r, aerr)
			return nil, false
		}
		if obj.DeleteMarker {
			w.Header().Set("x-amz-delete-marker", "true")
			setVersionIDHeader(w, obj)
			protocol.WriteXMLError(w, r, errNoSuchKey(key))
			return nil, false
		}
		return obj, true
	}

	obj, aerr := h.resolveVersion(r.Context(), bucket, key, versionID)
	if aerr != nil {
		protocol.WriteXMLError(w, r, aerr)
		return nil, false
	}
	if obj.DeleteMarker {
		w.Header().Set("x-amz-delete-marker", "true")
		w.Header().Set("Allow", "DELETE")
		setVersionIDHeader(w, obj)
		protocol.WriteXMLError(w, r, errMethodNotAllowedOnDeleteMarker())
		return nil, false
	}
	return obj, true
}

// resolveVersion returns the named version of a key.
func (h *Handler) resolveVersion(ctx context.Context, bucket, key, versionID string) (*Object, *protocol.AWSError) {
	b, aerr := h.store.getBucket(ctx, bucket)
	if aerr != nil {
		return nil, aerr
	}
	if !b.versioned() {
		// A bucket that has never been versioned holds exactly one version of
		// each key, and AWS calls it "null". Any other id names a version that
		// cannot exist there.
		if versionID != nullVersionID {
			return nil, errNoSuchVersion()
		}
		return h.store.getObjectMeta(ctx, bucket, key)
	}
	if aerr := h.ensureVersionHistory(ctx, b); aerr != nil {
		return nil, aerr
	}
	obj, found, aerr := h.store.findVersion(ctx, bucket, key, versionID)
	if aerr != nil {
		return nil, aerr
	}
	if !found {
		return nil, errNoSuchVersion()
	}
	return obj, nil
}

// objectRange is a Range header resolved against one object's body.
type objectRange struct {
	// partial is true when the answer must be a 206 covering start..end.
	partial    bool
	start, end int64 // inclusive, meaningful only when partial
}

// length is the number of bytes the range covers.
func (rng objectRange) length() int64 { return rng.end - rng.start + 1 }

// rangeOutcome classifies a Range header against an object's size. The three
// answers are three different responses; collapsing rangeIgnored into
// rangeUnsatisfiable, as a single "not ok" did, is what made a Range S3 cannot
// parse a 416 instead of the whole object.
type rangeOutcome int

const (
	// rangeIgnored is a Range header the server must answer as if it were
	// absent: a range unit it does not understand (RFC 9110 §14.2 requires
	// that one to be ignored) or a syntactically invalid byte-range-spec.
	// AWS answers those with the whole object and a 200.
	//
	// Answering 200 to a Range nobody could satisfy reads as permissiveness,
	// and floci and ministack both return 416 here, so this looks like the
	// bug rather than the fix. It is not: confirmed against real S3 on
	// 2026-09-06 — `bytes=abc`, `bytes=1-0`, `bytes=15-1` and `items=1-2`
	// all return the full body. Transcript, and the command to re-run it, in
	// docs/dev/compatibility/looks-like-a-bug.md.
	rangeIgnored rangeOutcome = iota
	// rangeSatisfiable is a valid range overlapping the object: a 206.
	rangeSatisfiable
	// rangeUnsatisfiable is a valid range that covers none of the object,
	// which RFC 9110 §15.5.17 answers with 416 and S3 spells InvalidRange.
	rangeUnsatisfiable
)

// applyObjectRange resolves the request's Range header against obj, writes the
// framing headers the answer needs, and reports whether the caller should carry
// on producing a response.
//
// It reports false having already written the whole response for an
// unsatisfiable range. That answer goes through protocol.WriteXMLError, which
// is the fix for #1705 twice over: the caller gets the InvalidRange document an
// SDK can raise a modelled error from, and the Content-Length follows from the
// bytes actually written rather than announcing the object's length and then
// sending nothing — a mismatch that leaves the connection desynchronised for
// whatever request reuses it next.
//
// No Content-Range accompanies the 416. RFC 9110 §15.5.17 says a 416 SHOULD
// carry one, and real S3 does not send it (#1864) — the object's size reaches
// the caller as the error document's ActualObjectSize instead. AWS wins over
// the RFC here; docs/dev/compatibility/looks-like-a-bug.md holds the transcript.
func applyObjectRange(w http.ResponseWriter, r *http.Request, obj *Object) (objectRange, bool) {
	header := r.Header.Get("Range")
	if header == "" {
		return objectRange{}, true
	}

	start, end, outcome := parseByteRange(header, obj.ContentLength)
	if outcome == rangeIgnored {
		return objectRange{}, true
	}
	if outcome == rangeUnsatisfiable {
		protocol.WriteXMLError(w, r, errInvalidRange(header, obj.ContentLength))
		return objectRange{}, false
	}

	rng := objectRange{partial: true, start: start, end: end}
	w.Header().Set("Content-Range", "bytes "+
		strconv.FormatInt(start, 10)+"-"+
		strconv.FormatInt(end, 10)+"/"+
		strconv.FormatInt(obj.ContentLength, 10))
	w.Header().Set("Content-Length", strconv.FormatInt(rng.length(), 10))
	return rng, true
}

// parseByteRange parses a "Range: bytes=X-Y" header value against a body of
// totalSize bytes. When the outcome is rangeSatisfiable it returns the resolved
// (start, end) byte positions, both inclusive; the positions are meaningless
// otherwise.
//
// The distinction the outcome carries is RFC 9110's: §14.1.1 calls a
// byte-range-spec whose last-byte-pos precedes its first-byte-pos *invalid*,
// and one whose first-byte-pos is past the end of the representation
// *unsatisfiable*. Only the second is a 416 — an invalid one is ignored, and
// the client gets the whole object. Real S3 agrees; see rangeIgnored above
// and docs/dev/compatibility/looks-like-a-bug.md before making one stricter.
func parseByteRange(header string, totalSize int64) (start, end int64, outcome rangeOutcome) {
	header = strings.TrimSpace(header)
	if !strings.HasPrefix(header, "bytes=") {
		return 0, 0, rangeIgnored
	}
	spec := strings.TrimPrefix(header, "bytes=")
	// Only the first range specifier is handled: AWS does not support
	// retrieving multiple ranges of data per GET request.
	first := strings.TrimSpace(strings.SplitN(spec, ",", 2)[0])
	dashIdx := strings.Index(first, "-")
	if dashIdx < 0 {
		return 0, 0, rangeIgnored
	}
	lhs, rhs := first[:dashIdx], first[dashIdx+1:]

	switch {
	case lhs == "" && rhs != "":
		// bytes=-N  →  the last N bytes.
		n, err := strconv.ParseInt(rhs, 10, 64)
		if err != nil || n < 0 {
			return 0, 0, rangeIgnored
		}
		if n == 0 || totalSize == 0 {
			// Zero bytes asked for, or an empty object: nothing to overlap.
			return 0, 0, rangeUnsatisfiable
		}
		if n > totalSize {
			// A suffix longer than the object is the whole object, not an
			// error (RFC 9110 §14.1.1).
			n = totalSize
		}
		return totalSize - n, totalSize - 1, rangeSatisfiable
	case lhs != "" && rhs == "":
		// bytes=N-  →  from N to the end.
		s, err := strconv.ParseInt(lhs, 10, 64)
		if err != nil || s < 0 {
			return 0, 0, rangeIgnored
		}
		if s >= totalSize {
			return 0, 0, rangeUnsatisfiable
		}
		return s, totalSize - 1, rangeSatisfiable
	case lhs != "" && rhs != "":
		s, startErr := strconv.ParseInt(lhs, 10, 64)
		e, endErr := strconv.ParseInt(rhs, 10, 64)
		if startErr != nil || endErr != nil || s < 0 || e < s {
			return 0, 0, rangeIgnored
		}
		if s >= totalSize {
			return 0, 0, rangeUnsatisfiable
		}
		if e >= totalSize {
			// A range starting inside the object truncates to its end.
			e = totalSize - 1
		}
		return s, e, rangeSatisfiable
	default:
		// A bare "bytes=-" names neither a position nor a suffix length.
		return 0, 0, rangeIgnored
	}
}

// etagMatches reports whether objectETag matches any ETag in headerVal.
// headerVal is a comma-separated list as sent in If-Match / If-None-Match.
// Comparison strips surrounding quotes for robustness.
func etagMatches(objectETag, headerVal string) bool {
	bare := strings.Trim(objectETag, `"`)
	for _, tok := range strings.Split(headerVal, ",") {
		tok = strings.TrimSpace(tok)
		if tok == "*" || strings.Trim(tok, `"`) == bare {
			return true
		}
	}
	return false
}

// HeadObject handles HEAD /{bucket}/{key}
// Returns the same headers as GetObject but no body.
// AWS docs: https://docs.aws.amazon.com/AmazonS3/latest/API/API_HeadObject.html
// Guarded by x-amz-expected-bucket-owner (see expected_owner.go) — HeadObject
// is registered directly on the chi router rather than reached through
// ObjectGet's dispatch table, so it carries its own guard call.
func (h *Handler) HeadObject(w http.ResponseWriter, r *http.Request) {
	if !h.checkExpectedBucketOwner(w, r) {
		return
	}
	bucket := chi.URLParam(r, "bucket")
	key := objectKey(r)

	// Metadata only — no body read from disk.
	obj, ok := h.readTarget(w, r, bucket, key)
	if !ok {
		return
	}

	h.writeObjectReadHeaders(w, r, obj)

	// HeadObject accepts Range too, and answers it with the same status and
	// framing GetObject does — only the body is left out, which net/http does
	// for every HEAD response.
	rng, ok := applyObjectRange(w, r, obj)
	if !ok {
		return
	}
	if rng.partial {
		w.WriteHeader(http.StatusPartialContent)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// ---- Batch delete -----------------------------------------------------------

// deleteObjectsRequest is the XML body for POST /{bucket}?delete.
type deleteObjectsRequest struct {
	XMLName xml.Name            `xml:"Delete"`
	Quiet   bool                `xml:"Quiet"`
	Objects []deleteObjectEntry `xml:"Object"`
}

type deleteObjectEntry struct {
	Key       string `xml:"Key"`
	VersionId string `xml:"VersionId,omitempty"`
	// ETag is DeleteObjects' per-key counterpart to DeleteObject's If-Match
	// header (com.amazonaws.s3#ObjectIdentifier$ETag): the entry is deleted
	// only if it matches the key's current ETag. LastModifiedTime and Size,
	// the shape's other two conditional fields, are documented as supported
	// only for directory buckets, which Overcast does not emulate, so they
	// are not modeled here.
	ETag string `xml:"ETag,omitempty"`
}

// deleteObjectsResponse is the XML response for DeleteObjects.
type deleteObjectsResponse struct {
	XMLName xml.Name            `xml:"DeleteResult"`
	XMLNS   string              `xml:"xmlns,attr"`
	Deleted []deletedObject     `xml:"Deleted,omitempty"`
	Errors  []deleteObjectError `xml:"Error,omitempty"`
}

// deletedObject mirrors com.amazonaws.s3#DeletedObject. DeleteMarker and
// DeleteMarkerVersionId only appear when the delete created a marker (or
// removed one); VersionId only for a bucket with version history, which is why
// all three are omitempty.
type deletedObject struct {
	Key                   string `xml:"Key"`
	VersionId             string `xml:"VersionId,omitempty"`
	DeleteMarker          bool   `xml:"DeleteMarker,omitempty"`
	DeleteMarkerVersionId string `xml:"DeleteMarkerVersionId,omitempty"`
}

type deleteObjectError struct {
	Key     string `xml:"Key"`
	Code    string `xml:"Code"`
	Message string `xml:"Message"`
}

// DeleteObjects handles POST /{bucket}?delete — batch delete up to 1000 keys.
// AWS docs: https://docs.aws.amazon.com/AmazonS3/latest/API/API_DeleteObjects.html
func (h *Handler) DeleteObjects(w http.ResponseWriter, r *http.Request) {
	bucket := chi.URLParam(r, "bucket")

	b, aerr := h.store.getBucket(r.Context(), bucket)
	if aerr != nil {
		protocol.WriteXMLError(w, r, aerr)
		return
	}

	var req deleteObjectsRequest
	if err := xml.NewDecoder(r.Body).Decode(&req); err != nil {
		protocol.WriteXMLError(w, r, &protocol.AWSError{Code: "MalformedXML", HTTPStatus: http.StatusBadRequest, Message: "The XML you provided was not well-formed"})
		return
	}

	if len(req.Objects) > 1000 {
		protocol.WriteXMLError(w, r, &protocol.AWSError{Code: "MalformedXML", HTTPStatus: http.StatusBadRequest, Message: "The XML you provided had more than 1000 objects"})
		return
	}

	resp := deleteObjectsResponse{
		XMLNS: "http://s3.amazonaws.com/doc/2006-03-01/",
	}

	for _, entry := range req.Objects {
		// DeleteObjects has no If-None-Match counterpart, so the entry's ETag
		// is the whole condition — unlike parseConditionalWrite, which also
		// reads If-None-Match off request headers that do not apply here.
		c := conditionalWrite{ifMatch: strings.TrimSpace(entry.ETag)}
		outcome, delErr := h.deleteOne(r, b, entry.Key, entry.VersionId, c)
		if delErr != nil {
			resp.Errors = append(resp.Errors, deleteObjectError{
				Key:     entry.Key,
				Code:    delErr.Code,
				Message: delErr.Message,
			})
			continue
		}
		if req.Quiet {
			continue
		}
		// AWS reports a created delete marker under DeleteMarkerVersionId and
		// a permanently removed version under VersionId. A version-targeted
		// delete that removed a marker sets both flags, which is why the
		// outcome carries the distinction rather than the request shape.
		deleted := deletedObject{Key: entry.Key, DeleteMarker: outcome.deleteMarker}
		if entry.VersionId != "" {
			deleted.VersionId = outcome.versionID
			if outcome.deleteMarker {
				deleted.DeleteMarkerVersionId = outcome.versionID
			}
		} else {
			deleted.DeleteMarkerVersionId = outcome.versionID
		}
		resp.Deleted = append(resp.Deleted, deleted)
	}

	protocol.WriteXML(w, r, http.StatusOK, resp)
}

// DeleteObject handles DELETE /{bucket}/{key}.
// AWS docs: https://docs.aws.amazon.com/AmazonS3/latest/API/API_DeleteObject.html
func (h *Handler) DeleteObject(w http.ResponseWriter, r *http.Request) {
	bucket := chi.URLParam(r, "bucket")
	key := objectKey(r)

	// Verify bucket exists first (AWS returns NoSuchBucket, not NoSuchKey).
	b, aerr := h.store.getBucket(r.Context(), bucket)
	if aerr != nil {
		protocol.WriteXMLError(w, r, aerr)
		return
	}

	// DeleteObject only documents If-Match, not If-None-Match — read the
	// header directly rather than through parseConditionalWrite, which also
	// parses (and would refuse) an If-None-Match this operation does not
	// define.
	c := conditionalWrite{ifMatch: strings.TrimSpace(r.Header.Get("If-Match"))}

	outcome, aerr := h.deleteOne(r, b, key, serviceutil.QueryString(r, "versionId", ""), c)
	if aerr != nil {
		protocol.WriteXMLError(w, r, aerr)
		return
	}
	if outcome.deleteMarker {
		w.Header().Set("x-amz-delete-marker", "true")
	}
	if outcome.versionID != "" {
		w.Header().Set("x-amz-version-id", outcome.versionID)
	}
	protocol.WriteEmpty(w, r, http.StatusNoContent)
}

// deleteOutcome is what a single delete produced, so DeleteObject can set the
// response headers and DeleteObjects can fill in one Deleted entry.
type deleteOutcome struct {
	// deleteMarker is true when the delete created a delete marker, and when a
	// version-targeted delete removed one. AWS uses the same flag for both.
	deleteMarker bool
	// versionID is the delete marker's version id, or the version id that was
	// permanently removed. Empty for a bucket with no version history, where
	// AWS sends no version id at all.
	versionID string
}

// deleteOne performs one DeleteObject, in whichever of AWS's three shapes the
// bucket's versioning state and the request select. c is the caller's
// If-Match condition (the zero value for an unconditional delete).
//
// Unconditionally, all three shapes are idempotent, which is why none of them
// reports a missing key: AWS answers 204 for a key that was never there, and —
// in a versioning-enabled bucket — still creates a delete marker for one,
// because a later PUT would otherwise be un-deletable in the caller's mental
// model. A condition changes that: AWS's conditional-writes guide documents
// If-Match on a key with no current version (or whose current version is a
// delete marker) as a 404 NoSuchKey rather than a silent no-op, because there
// is no ETag to compare against. deleteOne applies that same rule to deletes.
func (h *Handler) deleteOne(r *http.Request, b *Bucket, key, versionID string, c conditionalWrite) (deleteOutcome, *protocol.AWSError) {
	ctx := r.Context()

	if !b.versioned() {
		// A bucket that has never been versioned only has null versions, so a
		// versionId is either that or a version that cannot exist.
		if versionID != "" && versionID != nullVersionID {
			return deleteOutcome{}, errInvalidVersionID()
		}
		obj, aerr := h.store.lookupObjectMeta(ctx, b.Name, key)
		if aerr != nil {
			return deleteOutcome{}, aerr
		}
		if c.active() {
			if aerr := c.evaluate(key, obj); aerr != nil {
				return deleteOutcome{}, aerr
			}
		}
		if aerr := h.store.deleteObject(ctx, b.Name, key); aerr != nil {
			return deleteOutcome{}, aerr
		}
		h.publishDelete(r, b.Name, key, obj, newVersionStamp(h.clk.Now().UTC()), "ObjectRemoved:Delete")
		return deleteOutcome{}, nil
	}

	if aerr := h.ensureVersionHistory(ctx, b); aerr != nil {
		return deleteOutcome{}, aerr
	}
	if versionID == "" {
		// The condition applies to the current version, exactly as it would
		// for a write — creating a delete marker on a mismatched or absent
		// current version is what checkConditionalWrite already refuses.
		if aerr := h.checkConditionalWrite(ctx, c, b.Name, key); aerr != nil {
			return deleteOutcome{}, aerr
		}
		return h.createDeleteMarker(r, b, key)
	}
	return h.deleteVersionPermanently(r, b, key, versionID, c)
}

// createDeleteMarker is a delete with no version id against a versioned bucket:
// nothing is removed, a new version with no body becomes current, and the
// versions beneath it stay exactly where they are.
func (h *Handler) createDeleteMarker(r *http.Request, b *Bucket, key string) (deleteOutcome, *protocol.AWSError) {
	ctx := r.Context()
	now := h.clk.Now().UTC()

	marker := &Object{
		Bucket:       b.Name,
		Key:          key,
		LastModified: now,
		DeleteMarker: true,
	}
	stamp, aerr := h.beginVersion(ctx, b, marker, now)
	if aerr != nil {
		return deleteOutcome{}, aerr
	}
	if aerr := h.commitMarker(ctx, b, marker); aerr != nil {
		return deleteOutcome{}, aerr
	}

	h.publishDelete(r, b.Name, key, marker, stamp, "ObjectRemoved:DeleteMarkerCreated")
	return deleteOutcome{deleteMarker: true, versionID: marker.wireVersionID()}, nil
}

// deleteVersionPermanently is a delete that names a version id: that one
// version really goes, and if it was the current one the newest version left
// takes its place. c's condition, when present, applies to that named
// version's own ETag rather than to whichever version is current — the
// caller asked to remove a specific version, so that is the one it must
// match.
func (h *Handler) deleteVersionPermanently(r *http.Request, b *Bucket, key, versionID string, c conditionalWrite) (deleteOutcome, *protocol.AWSError) {
	ctx := r.Context()

	target, found, aerr := h.store.findVersion(ctx, b.Name, key, versionID)
	if aerr != nil {
		return deleteOutcome{}, aerr
	}
	if !found {
		// Idempotent, as everywhere else in DeleteObject.
		return deleteOutcome{versionID: versionID}, nil
	}
	if c.active() {
		if aerr := c.evaluate(key, target); aerr != nil {
			return deleteOutcome{}, aerr
		}
	}
	if aerr := h.store.deleteVersion(ctx, target); aerr != nil {
		return deleteOutcome{}, aerr
	}

	current, aerr := h.store.lookupObjectMeta(ctx, b.Name, key)
	if aerr != nil {
		return deleteOutcome{}, aerr
	}
	if current != nil && current.Seq == target.Seq {
		if aerr := h.promoteNewestVersion(ctx, b.Name, key); aerr != nil {
			return deleteOutcome{}, aerr
		}
	}

	h.publishDelete(r, b.Name, key, target, newVersionStamp(h.clk.Now().UTC()), "ObjectRemoved:Delete")
	return deleteOutcome{deleteMarker: target.DeleteMarker, versionID: target.wireVersionID()}, nil
}

// publishDelete emits one removal event, timestamped when the delete happened
// rather than when the version it removed was written. removed may be nil when
// the key was not there — AWS still answers 204, and the notification then
// carries no version id because there is none to report.
func (h *Handler) publishDelete(r *http.Request, bucket, key string, removed *Object, stamp versionStamp, eventName string) {
	subject := &Object{Bucket: bucket, Key: key, LastModified: h.clk.Now().UTC()}
	if removed != nil {
		subject.Seq, subject.VersionID = removed.Seq, removed.VersionID
	}
	h.publishObjectEvent(r.Context(), events.S3ObjectRemoved, subject, stamp, eventName, 0, "")
}

// copyObjectResponse is the XML for a successful CopyObject.
type copyObjectResponse struct {
	XMLName      xml.Name  `xml:"CopyObjectResult"`
	LastModified time.Time `xml:"LastModified"`
	ETag         string    `xml:"ETag"`
}

// copySourceVersion is the ?versionId= a copy source may carry.
func copySourceVersion(key string) (bareKey, versionID string) {
	idx := strings.Index(key, "?")
	if idx < 0 {
		return key, ""
	}
	values, err := url.ParseQuery(key[idx+1:])
	if err != nil {
		return key, ""
	}
	return key[:idx], values.Get("versionId")
}

// CopyObject handles PUT /{bucket}/{key} with x-amz-copy-source header.
// AWS docs: https://docs.aws.amazon.com/AmazonS3/latest/API/API_CopyObject.html
func (h *Handler) CopyObject(w http.ResponseWriter, r *http.Request) {
	destBucket := chi.URLParam(r, "bucket")
	destKey := objectKey(r)

	srcBucket, srcKey, ok := parseCopySource(r.Header.Get("x-amz-copy-source"))
	if !ok {
		protocol.WriteXMLError(w, r, protocol.ErrInvalidArgument("Invalid copy source"))
		return
	}
	srcKey, srcVersionID := copySourceVersion(srcKey)

	destB, aerr := h.store.getBucket(r.Context(), destBucket)
	if aerr != nil {
		protocol.WriteXMLError(w, r, aerr)
		return
	}

	// Load source metadata only — the body is streamed by storeObject.
	var src *Object
	if srcVersionID == "" {
		src, aerr = h.readObjectMeta(r.Context(), srcBucket, srcKey)
		if aerr == nil && src.DeleteMarker {
			// The source key's current version is a delete marker, so there is
			// nothing to copy — the same answer a GET of that key gives.
			aerr = errNoSuchKey(srcKey)
		}
	} else {
		src, aerr = h.resolveVersion(r.Context(), srcBucket, srcKey, srcVersionID)
		if aerr == nil && src.DeleteMarker {
			aerr = errMethodNotAllowedOnDeleteMarker()
		}
	}
	if aerr != nil {
		protocol.WriteXMLError(w, r, aerr)
		return
	}

	// A copy takes the requested storage class, or STANDARD — it does not
	// inherit the source's, which is what AWS does too.
	storageClass, aerr := requestedStorageClass(r)
	if aerr != nil {
		protocol.WriteXMLError(w, r, aerr)
		return
	}

	now := h.clk.Now().UTC()
	dest := &Object{
		Bucket:       destBucket,
		Key:          destKey,
		StorageClass: storageClass,
		ContentType:  src.ContentType,
		LastModified: now,
		Metadata:     src.Metadata,
	}
	stamp, aerr := h.beginVersion(r.Context(), destB, dest, now)
	if aerr != nil {
		protocol.WriteXMLError(w, r, aerr)
		return
	}

	// Stream the source body to the destination, computing its MD5 on the
	// way. The source is read in full before the destination changes, which
	// is what lets a copy onto itself (the documented way to replace an
	// object's metadata) keep its bytes.
	commit := func() *protocol.AWSError { return h.commitObject(r.Context(), destB, dest) }
	if aerr := h.store.storeObject(dest, h.store.bodyOf(src), bodyDigest.etag, commit); aerr != nil {
		protocol.WriteXMLError(w, r, aerr)
		return
	}

	h.publishObjectEvent(r.Context(), events.S3ObjectCreated, dest, stamp, "ObjectCreated:Copy", dest.ContentLength, dest.ETag)

	setVersionIDHeader(w, dest)
	if src.Seq != "" {
		w.Header().Set("x-amz-copy-source-version-id", src.wireVersionID())
	}
	h.setExpirationHeader(r.Context(), w, dest)

	protocol.WriteXML(w, r, http.StatusOK, &copyObjectResponse{
		LastModified: now,
		ETag:         dest.ETag,
	})
}

// parseCopySource splits an x-amz-copy-source header into bucket and key.
//
// AWS documents the value as "the name of the source bucket and key name of
// the source object, separated by a slash (/)" and requires it URL-encoded,
// which it decodes. Clients differ in how much they encode: most send an
// unencoded separator with an encoded key, while the AWS SDK for .NET encodes
// the whole value — separator included. Splitting before decoding therefore
// rejected a request AWS accepts ("Invalid copy source"), which is what the
// dotnet compat suite's CopyObject hit.
//
// The separator is the first unencoded slash, so the split comes first and the
// parts are decoded afterwards, keeping slashes inside a key intact. When
// there is no unencoded slash the whole value is decoded and split again,
// which is the fully-encoded form.
func parseCopySource(header string) (bucket, key string, ok bool) {
	split := func(s string) (string, string, bool) {
		s = strings.TrimPrefix(s, "/")
		parts := strings.SplitN(s, "/", 2)
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			return "", "", false
		}
		return parts[0], parts[1], true
	}

	if b, k, found := split(header); found {
		return decodeCopyPart(b), decodeCopyPart(k), true
	}
	decoded, err := url.QueryUnescape(header)
	if err != nil {
		return "", "", false
	}
	return split(decoded)
}

// decodeCopyPart URL-decodes one component, leaving it unchanged when it is not
// valid encoding — a key may legitimately contain a bare '%'.
func decodeCopyPart(s string) string {
	if decoded, err := url.QueryUnescape(s); err == nil {
		return decoded
	}
	return s
}
