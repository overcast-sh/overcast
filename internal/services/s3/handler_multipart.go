package s3

// handler_multipart.go contains fully-implemented multipart upload handlers.
// Stubs (NotImplementedXML) live in handler_stubs.go.
// Dispatchers (ObjectPost, ObjectDelete, PutObjectOrCopy, BucketGet) live in
// handler.go.

import (
	"encoding/xml"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/overcast-sh/overcast/internal/events"
	"github.com/overcast-sh/overcast/internal/protocol"
	"github.com/overcast-sh/overcast/internal/serviceutil"
)

// ---- XML types for multipart operations ------------------------------------

type xmlInitiateMultipartUploadResult struct {
	XMLName  xml.Name `xml:"InitiateMultipartUploadResult"`
	NS       string   `xml:"xmlns,attr"`
	Bucket   string   `xml:"Bucket"`
	Key      string   `xml:"Key"`
	UploadId string   `xml:"UploadId"`
}

type xmlCompleteMultipartUpload struct {
	Parts []xmlCompletePart `xml:"Part"`
}

type xmlCompletePart struct {
	PartNumber int    `xml:"PartNumber"`
	ETag       string `xml:"ETag"`
}

type xmlCompleteMultipartUploadResult struct {
	XMLName  xml.Name `xml:"CompleteMultipartUploadResult"`
	NS       string   `xml:"xmlns,attr"`
	Location string   `xml:"Location"`
	Bucket   string   `xml:"Bucket"`
	Key      string   `xml:"Key"`
	ETag     string   `xml:"ETag"`
}

// xmlListPartsResult is ListParts' response envelope.
// AWS docs: https://docs.aws.amazon.com/AmazonS3/latest/API/API_ListParts.html#API_ListParts_ResponseSyntax
// NextPartNumberMarker is typed Integer per the doc's XSD (Go SDKs decode it
// as an int) and, per the Response Elements section, is only meaningful
// "when a list is truncated" — omitempty is safe because valid part numbers
// start at 1, so a zero value only ever occurs on an untruncated response.
type xmlListPartsResult struct {
	XMLName              xml.Name  `xml:"ListPartsResult"`
	NS                   string    `xml:"xmlns,attr"`
	Bucket               string    `xml:"Bucket"`
	Key                  string    `xml:"Key"`
	UploadId             string    `xml:"UploadId"`
	PartNumberMarker     int       `xml:"PartNumberMarker"`
	NextPartNumberMarker int       `xml:"NextPartNumberMarker,omitempty"`
	MaxParts             int       `xml:"MaxParts"`
	IsTruncated          bool      `xml:"IsTruncated"`
	Parts                []xmlPart `xml:"Part"`
}

type xmlPart struct {
	PartNumber   int    `xml:"PartNumber"`
	ETag         string `xml:"ETag"`
	Size         int64  `xml:"Size"`
	LastModified string `xml:"LastModified"`
}

// xmlListMultipartUploadsResult is ListMultipartUploads' response envelope.
// AWS docs: https://docs.aws.amazon.com/AmazonS3/latest/API/API_ListMultipartUploads.html#API_ListMultipartUploads_ResponseSyntax
// Unlike ListParts' NextPartNumberMarker, AWS's own example responses show
// KeyMarker/UploadIdMarker/NextKeyMarker/NextUploadIdMarker present (as
// empty elements) even on an untruncated response with no markers supplied
// — so these are plain (non-omitempty) string fields, matching that shape.
type xmlListMultipartUploadsResult struct {
	XMLName            xml.Name    `xml:"ListMultipartUploadsResult"`
	NS                 string      `xml:"xmlns,attr"`
	Bucket             string      `xml:"Bucket"`
	KeyMarker          string      `xml:"KeyMarker"`
	UploadIdMarker     string      `xml:"UploadIdMarker"`
	NextKeyMarker      string      `xml:"NextKeyMarker"`
	NextUploadIdMarker string      `xml:"NextUploadIdMarker"`
	MaxUploads         int         `xml:"MaxUploads"`
	IsTruncated        bool        `xml:"IsTruncated"`
	Uploads            []xmlUpload `xml:"Upload"`
}

type xmlUpload struct {
	UploadId  string `xml:"UploadId"`
	Key       string `xml:"Key"`
	Initiated string `xml:"Initiated"`
}

const s3XMLNamespace = "http://s3.amazonaws.com/doc/2006-03-01/"

// ---- CreateMultipartUpload -------------------------------------------------

// CreateMultipartUpload handles POST /{bucket}/{key}?uploads
// AWS docs: https://docs.aws.amazon.com/AmazonS3/latest/API/API_CreateMultipartUpload.html
func (h *Handler) CreateMultipartUpload(w http.ResponseWriter, r *http.Request) {
	bucket := chi.URLParam(r, "bucket")
	key := objectKey(r)

	if aerr := h.requireBucket(r, bucket); aerr != nil {
		protocol.WriteXMLError(w, r, aerr)
		return
	}

	contentType := objectContentType(r.Header.Get("Content-Type"))

	upload := &MultipartUpload{
		UploadID:    uuid.New().String(),
		Bucket:      bucket,
		Key:         key,
		ContentType: contentType,
		Metadata:    serviceutil.HeaderPrefix(r, "X-Amz-Meta-"),
		Initiated:   h.clk.Now().UTC(),
	}

	if aerr := h.store.createMultipartUpload(r.Context(), upload); aerr != nil {
		protocol.WriteXMLError(w, r, aerr)
		return
	}

	protocol.WriteXML(w, r, http.StatusOK, &xmlInitiateMultipartUploadResult{
		NS:       s3XMLNamespace,
		Bucket:   bucket,
		Key:      key,
		UploadId: upload.UploadID,
	})
}

// ---- UploadPart ------------------------------------------------------------

// UploadPart handles PUT /{bucket}/{key}?partNumber=N&uploadId=xxx
// AWS docs: https://docs.aws.amazon.com/AmazonS3/latest/API/API_UploadPart.html
func (h *Handler) UploadPart(w http.ResponseWriter, r *http.Request) {
	bucket := chi.URLParam(r, "bucket")
	key := objectKey(r)
	uploadID := r.URL.Query().Get("uploadId")
	partNumberStr := r.URL.Query().Get("partNumber")

	partNumber, err := strconv.Atoi(partNumberStr)
	if err != nil || partNumber < 1 || partNumber > 10000 {
		protocol.WriteXMLError(w, r, &protocol.AWSError{
			Code:       "InvalidArgument",
			Message:    "Part number must be an integer between 1 and 10000",
			HTTPStatus: http.StatusBadRequest,
		})
		return
	}

	// Verify the upload exists and belongs to this bucket/key.
	upload, aerr := h.store.getMultipartUpload(r.Context(), uploadID)
	if aerr != nil {
		protocol.WriteXMLError(w, r, aerr)
		return
	}
	if upload.Bucket != bucket || upload.Key != key {
		protocol.WriteXMLError(w, r, errNoSuchUpload(uploadID))
		return
	}

	body, _ := maybeDecodeAWSChunked(r)
	part := &Part{PartNumber: partNumber, LastModified: h.clk.Now().UTC()}
	if aerr := h.store.storePart(r.Context(), uploadID, part, body); aerr != nil {
		protocol.WriteXMLError(w, r, aerr)
		return
	}

	w.Header().Set("ETag", part.ETag)
	protocol.WriteEmpty(w, r, http.StatusOK)
}

// ---- CompleteMultipartUpload -----------------------------------------------

// CompleteMultipartUpload handles POST /{bucket}/{key}?uploadId=xxx
// AWS docs: https://docs.aws.amazon.com/AmazonS3/latest/API/API_CompleteMultipartUpload.html
func (h *Handler) CompleteMultipartUpload(w http.ResponseWriter, r *http.Request) {
	bucket := chi.URLParam(r, "bucket")
	key := objectKey(r)
	uploadID := r.URL.Query().Get("uploadId")

	upload, aerr := h.store.getMultipartUpload(r.Context(), uploadID)
	if aerr != nil {
		protocol.WriteXMLError(w, r, aerr)
		return
	}
	if upload.Bucket != bucket || upload.Key != key {
		protocol.WriteXMLError(w, r, errNoSuchUpload(uploadID))
		return
	}

	cond, aerr := parseConditionalWrite(r)
	if aerr != nil {
		protocol.WriteXMLError(w, r, aerr)
		return
	}

	// Parse the list of parts from the request body.
	var req xmlCompleteMultipartUpload
	if decodeErr := xml.NewDecoder(r.Body).Decode(&req); decodeErr != nil {
		protocol.WriteXMLError(w, r, errMalformedMultipartXML())
		return
	}

	storedParts, aerr := h.store.listParts(r.Context(), uploadID)
	if aerr != nil {
		protocol.WriteXMLError(w, r, aerr)
		return
	}

	// Every validation runs before anything is assembled or deleted: a
	// refused completion must leave the upload and its parts exactly as they
	// were, so the client can correct its list and retry, or abort.
	orderedParts, aerr := resolveCompletedParts(req.Parts, storedParts)
	if aerr != nil {
		protocol.WriteXMLError(w, r, aerr)
		return
	}

	// The finished object is a new version of the key, so its identity has to
	// be settled before its body is assembled — the body path depends on it.
	b, aerr := h.store.getBucket(r.Context(), bucket)
	if aerr != nil {
		protocol.WriteXMLError(w, r, aerr)
		return
	}
	if cond.active() {
		// As in PutObject: held until the handler returns so the check and the
		// assembled object's commit are one step. AWS evaluates the condition
		// at completion, not at initiation — an in-progress upload is not yet
		// an object, so a PutObject that claims the key meanwhile is what a
		// conditional completion is meant to lose to.
		defer h.objectLocks.Lock(objectStoreKey(bucket, key))()
		if aerr := h.checkConditionalWrite(r.Context(), cond, bucket, key); aerr != nil {
			protocol.WriteXMLError(w, r, aerr)
			return
		}
	}
	now := h.clk.Now().UTC()
	obj := &Object{
		Bucket:       bucket,
		Key:          key,
		ContentType:  upload.ContentType,
		LastModified: now,
		Metadata:     upload.Metadata,
	}
	stamp, aerr := h.beginVersion(r.Context(), b, obj, now)
	if aerr != nil {
		protocol.WriteXMLError(w, r, aerr)
		return
	}

	// Stream the parts, in order, into the object's body, computing its MD5
	// on the way. A failure part way leaves the key as it was and the upload
	// intact, so the client can retry the completion.
	if aerr := h.store.storeObject(r.Context(), obj, h.store.partsOf(uploadID, orderedParts), multipartETag(len(orderedParts))); aerr != nil {
		protocol.WriteXMLError(w, r, aerr)
		return
	}
	if aerr := h.commitVersion(r.Context(), b, obj); aerr != nil {
		protocol.WriteXMLError(w, r, aerr)
		return
	}

	// Clean up: delete parts + upload record.
	_ = h.store.deleteAllParts(r.Context(), uploadID)        // best-effort cleanup
	_ = h.store.deleteMultipartUpload(r.Context(), uploadID) // best-effort cleanup

	// Emit S3ObjectCreated event.
	h.publishObjectEvent(r.Context(), events.S3ObjectCreated, obj, stamp, "ObjectCreated:CompleteMultipartUpload", obj.ContentLength, obj.ETag)

	setVersionIDHeader(w, obj)
	location := fmt.Sprintf("/%s/%s", bucket, key)
	protocol.WriteXML(w, r, http.StatusOK, &xmlCompleteMultipartUploadResult{
		NS:       s3XMLNamespace,
		Location: location,
		Bucket:   bucket,
		Key:      key,
		ETag:     obj.ETag,
	})
}

// multipartETag is the ETag of an object assembled from parts: the MD5 of its
// bytes, suffixed with the number of parts.
func multipartETag(parts int) func(bodyDigest) string {
	return func(d bodyDigest) string { return fmt.Sprintf(`"%x-%d"`, d.md5, parts) }
}

// minMultipartPartSize is AWS's floor for every part except the last: 5 MiB.
// It is enforced at completion, not at UploadPart — S3 accepts a small part
// at upload time and only refuses when the list is assembled.
const minMultipartPartSize = 5 * 1024 * 1024

// resolveCompletedParts checks the parts list a CompleteMultipartUpload names
// against the parts actually uploaded, and returns the stored parts in the
// requested order. The rules and their codes are the "Special errors" of
// https://docs.aws.amazon.com/AmazonS3/latest/API/API_CompleteMultipartUpload.html:
//
//   - the list must not be empty ("If you do not supply a valid Part with your
//     request, the service sends back an HTTP 400 response" — MalformedXML,
//     which is also what LocalStack and moto answer);
//   - part numbers must be strictly ascending (InvalidPartOrder);
//   - each PartNumber/ETag pair must name an uploaded part with that ETag
//     (InvalidPart) — ETags compare with their surrounding quotes stripped,
//     since S3 accepts either form and SDKs differ in what they send;
//   - every part but the last must be at least 5 MiB (EntityTooSmall).
//
// The checks run in that order: the whole list's ordering is settled before
// any ETag is looked at, and each part is matched before it is measured.
func resolveCompletedParts(requested []xmlCompletePart, stored []*Part) ([]*Part, *protocol.AWSError) {
	if len(requested) == 0 {
		return nil, errMalformedMultipartXML()
	}
	for i := 1; i < len(requested); i++ {
		if requested[i].PartNumber <= requested[i-1].PartNumber {
			return nil, errInvalidPartOrder()
		}
	}

	partByNum := make(map[int]*Part, len(stored))
	for _, p := range stored {
		partByNum[p.PartNumber] = p
	}

	ordered := make([]*Part, 0, len(requested))
	last := len(requested) - 1
	for i, rp := range requested {
		found, ok := partByNum[rp.PartNumber]
		if !ok || strings.Trim(found.ETag, `"`) != strings.Trim(rp.ETag, `"`) {
			return nil, errInvalidPart(rp.PartNumber)
		}
		if i != last && found.Size < minMultipartPartSize {
			return nil, errEntityTooSmall(found)
		}
		ordered = append(ordered, found)
	}
	return ordered, nil
}

func errMalformedMultipartXML() *protocol.AWSError {
	return &protocol.AWSError{
		Code:       "MalformedXML",
		Message:    "The XML you provided was not well-formed or did not validate against our published schema",
		HTTPStatus: http.StatusBadRequest,
	}
}

func errInvalidPartOrder() *protocol.AWSError {
	return &protocol.AWSError{
		Code:       "InvalidPartOrder",
		Message:    "The list of parts was not in ascending order. The parts list must be specified in order by part number.",
		HTTPStatus: http.StatusBadRequest,
	}
}

func errInvalidPart(partNumber int) *protocol.AWSError {
	return &protocol.AWSError{
		Code:       "InvalidPart",
		Message:    fmt.Sprintf("One or more of the specified parts could not be found. The part might not have been uploaded, or the specified ETag might not have matched the uploaded part's ETag: part %d", partNumber),
		HTTPStatus: http.StatusBadRequest,
	}
}

func errEntityTooSmall(p *Part) *protocol.AWSError {
	return &protocol.AWSError{
		Code:       "EntityTooSmall",
		Message:    fmt.Sprintf("Your proposed upload is smaller than the minimum allowed size: part %d is %d bytes, minimum is %d", p.PartNumber, p.Size, minMultipartPartSize),
		HTTPStatus: http.StatusBadRequest,
	}
}

// ---- AbortMultipartUpload --------------------------------------------------

// AbortMultipartUpload handles DELETE /{bucket}/{key}?uploadId=xxx
// AWS docs: https://docs.aws.amazon.com/AmazonS3/latest/API/API_AbortMultipartUpload.html
func (h *Handler) AbortMultipartUpload(w http.ResponseWriter, r *http.Request) {
	bucket := chi.URLParam(r, "bucket")
	key := objectKey(r)
	uploadID := r.URL.Query().Get("uploadId")

	upload, aerr := h.store.getMultipartUpload(r.Context(), uploadID)
	if aerr != nil {
		protocol.WriteXMLError(w, r, aerr)
		return
	}
	if upload.Bucket != bucket || upload.Key != key {
		protocol.WriteXMLError(w, r, errNoSuchUpload(uploadID))
		return
	}

	_ = h.store.deleteAllParts(r.Context(), uploadID) // best-effort cleanup
	if aerr := h.store.deleteMultipartUpload(r.Context(), uploadID); aerr != nil {
		protocol.WriteXMLError(w, r, aerr)
		return
	}

	protocol.WriteEmpty(w, r, http.StatusNoContent)
}

// ---- ListParts -------------------------------------------------------------

// listMax clamps a requested MaxParts/MaxUploads value to AWS's documented
// default-and-cap of 1000 for both ListParts and ListMultipartUploads (the
// same numeric limit also serves as the default when the client omits the
// parameter or supplies a non-positive value — Cognito's local pageBounds
// convention for a non-opaque, per-operation clamp).
//
// This is deliberately *not* handler_bucket.go's parseMaxKeys: max-parts and
// max-uploads have not been reviewed against AWS's validation behaviour yet,
// so they keep the older permissive reading (a malformed or negative value
// becomes the default) rather than inheriting max-keys' InvalidArgument
// rejections on the strength of a family resemblance.
func listMax(requested int) int {
	if requested <= 0 || requested > 1000 {
		return 1000
	}
	return requested
}

// errInvalidPartNumberMarker mirrors S3's InvalidArgument error shape (see
// UploadPart's partNumber validation above) for a part-number-marker that
// isn't a non-negative integer. AWS documents part-number-marker as a plain
// part number, not an opaque token (API_ListParts.html), so — unlike
// ListObjectsV2's ContinuationToken — there's no base64/JSON envelope to
// decode; validation is just "does this parse as an AWS-legal part number."
// A malformed value must error rather than silently resetting to 0 (i.e.
// "start from part 1"), the same invalid-token-silently-restarts-page-1
// divergence the pagination plan's H1/G3 fixed for other operations.
func errInvalidPartNumberMarker() *protocol.AWSError {
	return &protocol.AWSError{
		Code:       "InvalidArgument",
		Message:    "Part number marker must be a non-negative integer",
		HTTPStatus: http.StatusBadRequest,
	}
}

// ListParts handles GET /{bucket}/{key}?uploadId=xxx
// AWS docs: https://docs.aws.amazon.com/AmazonS3/latest/API/API_ListParts.html
//
// Pagination shape (pagination-plan.md G4): parts are bounded (AWS caps a
// single multipart upload at 10,000 parts), so this is exactly the
// boundedness rule's "simple in-memory slice pagination is correct" case —
// no ScanPage/storage change needed (store.listParts already Scans once and
// sorts). It deliberately does NOT use serviceutil.Paginate/H1: AWS types
// PartNumberMarker/NextPartNumberMarker as real integers on the wire (the
// part number itself, not an opaque cursor), so Paginate's base64(JSON)
// token would be the wrong shape here — a client (or another SDK) is
// entitled to jump to an arbitrary part-number-marker without having been
// handed a NextPartNumberMarker first, which an opaque token can't support.
func (h *Handler) ListParts(w http.ResponseWriter, r *http.Request) {
	bucket := chi.URLParam(r, "bucket")
	key := objectKey(r)
	uploadID := r.URL.Query().Get("uploadId")

	maxParts := listMax(serviceutil.QueryInt(r, "max-parts", 1000))
	partNumberMarker := 0
	if raw := serviceutil.QueryString(r, "part-number-marker", ""); raw != "" {
		v, err := strconv.Atoi(raw)
		if err != nil || v < 0 {
			protocol.WriteXMLError(w, r, errInvalidPartNumberMarker())
			return
		}
		partNumberMarker = v
	}

	upload, aerr := h.store.getMultipartUpload(r.Context(), uploadID)
	if aerr != nil {
		protocol.WriteXMLError(w, r, aerr)
		return
	}
	if upload.Bucket != bucket || upload.Key != key {
		protocol.WriteXMLError(w, r, errNoSuchUpload(uploadID))
		return
	}

	parts, aerr := h.store.listParts(r.Context(), uploadID)
	if aerr != nil {
		protocol.WriteXMLError(w, r, aerr)
		return
	}

	// AWS: "Only parts with higher part numbers will be listed." parts is
	// already sorted ascending by PartNumber (store.listParts).
	filtered := make([]*Part, 0, len(parts))
	for _, p := range parts {
		if p.PartNumber > partNumberMarker {
			filtered = append(filtered, p)
		}
	}

	truncated := len(filtered) > maxParts
	page := filtered
	if truncated {
		page = filtered[:maxParts]
	}

	var nextPartNumberMarker int
	if truncated {
		nextPartNumberMarker = page[len(page)-1].PartNumber
	}

	xmlParts := make([]xmlPart, 0, len(page))
	for _, p := range page {
		xmlParts = append(xmlParts, xmlPart{
			PartNumber:   p.PartNumber,
			ETag:         p.ETag,
			Size:         p.Size,
			LastModified: p.LastModified.Format(time.RFC3339),
		})
	}

	protocol.WriteXML(w, r, http.StatusOK, &xmlListPartsResult{
		NS:                   s3XMLNamespace,
		Bucket:               bucket,
		Key:                  key,
		UploadId:             uploadID,
		PartNumberMarker:     partNumberMarker,
		NextPartNumberMarker: nextPartNumberMarker,
		MaxParts:             maxParts,
		IsTruncated:          truncated,
		Parts:                xmlParts,
	})
}

// ---- ListMultipartUploads --------------------------------------------------

// ListMultipartUploads handles GET /{bucket}?uploads
// AWS docs: https://docs.aws.amazon.com/AmazonS3/latest/API/API_ListMultipartUploads.html
//
// Pagination shape (pagination-plan.md G4): in-progress uploads are bounded
// metadata (storage-access-plan.md's keep-as-is register already lists this
// op there) — store.listMultipartUploads keeps its existing single Scan, no
// storage change. Like ListParts, this deliberately does NOT use
// serviceutil.Paginate/H1: KeyMarker/UploadIdMarker are plain, AWS-documented
// values (a real object key and a real upload ID), and AWS's resume rule is
// a compound comparison over both — Key > KeyMarker, OR Key == KeyMarker AND
// UploadId > UploadIdMarker — not an opaque position a service alone can
// mint. Wrapping them in an opaque token would also break AWS's documented
// "any multipart uploads for a key equal to key-marker might also be
// included" jump-to behavior for a client that constructs its own markers
// rather than echoing back NextKeyMarker/NextUploadIdMarker.
func (h *Handler) ListMultipartUploads(w http.ResponseWriter, r *http.Request) {
	bucket := chi.URLParam(r, "bucket")

	if aerr := h.requireBucket(r, bucket); aerr != nil {
		protocol.WriteXMLError(w, r, aerr)
		return
	}

	maxUploads := listMax(serviceutil.QueryInt(r, "max-uploads", 1000))
	keyMarker := serviceutil.QueryString(r, "key-marker", "")
	uploadIDMarker := serviceutil.QueryString(r, "upload-id-marker", "")
	// AWS: "If key-marker is not specified, the upload-id-marker parameter
	// is ignored."
	if keyMarker == "" {
		uploadIDMarker = ""
	}

	uploads, aerr := h.store.listMultipartUploads(r.Context(), bucket)
	if aerr != nil {
		protocol.WriteXMLError(w, r, aerr)
		return
	}

	// AWS sorts uploads by key ascending, then by initiation time ascending
	// for uploads that share a key.
	sort.Slice(uploads, func(i, j int) bool {
		if uploads[i].Key != uploads[j].Key {
			return uploads[i].Key < uploads[j].Key
		}
		return uploads[i].Initiated.Before(uploads[j].Initiated)
	})

	filtered := make([]*MultipartUpload, 0, len(uploads))
	for _, u := range uploads {
		switch {
		case keyMarker == "":
			filtered = append(filtered, u)
		case u.Key > keyMarker:
			filtered = append(filtered, u)
		case u.Key == keyMarker && uploadIDMarker != "" && u.UploadID > uploadIDMarker:
			filtered = append(filtered, u)
		}
	}

	truncated := len(filtered) > maxUploads
	page := filtered
	if truncated {
		page = filtered[:maxUploads]
	}

	var nextKeyMarker, nextUploadIDMarker string
	if truncated {
		last := page[len(page)-1]
		nextKeyMarker = last.Key
		nextUploadIDMarker = last.UploadID
	}

	xmlUploads := make([]xmlUpload, 0, len(page))
	for _, u := range page {
		xmlUploads = append(xmlUploads, xmlUpload{
			UploadId:  u.UploadID,
			Key:       u.Key,
			Initiated: u.Initiated.Format(time.RFC3339),
		})
	}

	protocol.WriteXML(w, r, http.StatusOK, &xmlListMultipartUploadsResult{
		NS:                 s3XMLNamespace,
		Bucket:             bucket,
		KeyMarker:          keyMarker,
		UploadIdMarker:     uploadIDMarker,
		NextKeyMarker:      nextKeyMarker,
		NextUploadIdMarker: nextUploadIDMarker,
		MaxUploads:         maxUploads,
		IsTruncated:        truncated,
		Uploads:            xmlUploads,
	})
}

// ---- requireBucket (shared helper) -----------------------------------------

// requireBucket checks that bucket exists and returns an AWSError if not.
func (h *Handler) requireBucket(r *http.Request, bucket string) *protocol.AWSError {
	exists, aerr := h.store.bucketExists(r.Context(), bucket)
	if aerr != nil {
		return aerr
	}
	if !exists {
		return errNoSuchBucket(bucket)
	}
	return nil
}
