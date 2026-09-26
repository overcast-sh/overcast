package s3

// handler_bucket.go contains all fully-implemented bucket-level S3 handlers
// and the route tables that map sub-resource query params to those handlers.
// Stubs (NotImplementedXML) live in handler_stubs.go.
// Dispatchers (BucketGet, BucketPut, …) live in handler.go.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"encoding/xml"
	"errors"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"go.uber.org/zap"

	"github.com/overcast-sh/overcast/internal/events"
	"github.com/overcast-sh/overcast/internal/middleware"
	"github.com/overcast-sh/overcast/internal/protocol"
	"github.com/overcast-sh/overcast/internal/serviceutil"
)

// initBucketRoutes populates the four bucket-level dispatch tables.
// Called once by newHandler.
func (h *Handler) initBucketRoutes() {
	h.bucketGetRoutes = []s3Route{
		{"list-type", h.listTypeDispatch},
		{"location", h.GetBucketLocation},
		{"acl", h.GetBucketAcl},
		{"cors", h.GetBucketCors},
		{"policy", h.GetBucketPolicy},
		{"policyStatus", h.GetBucketPolicyStatus},
		{"lifecycle", h.GetBucketLifecycleConfiguration},
		{"versioning", h.GetBucketVersioning},
		{"notification", h.GetBucketNotificationConfiguration},
		{"tagging", h.GetBucketTagging},
		{"website", h.GetBucketWebsite},
		{"logging", h.GetBucketLogging},
		{"replication", h.GetBucketReplication},
		{"encryption", h.GetBucketEncryption},
		{"accelerate", h.GetBucketAccelerateConfiguration},
		{"requestPayment", h.GetBucketRequestPayment},
		{"ownershipControls", h.GetBucketOwnershipControls},
		{"publicAccessBlock", h.GetPublicAccessBlock},
		{"uploads", h.ListMultipartUploads},
		{"versions", h.ListObjectVersions},
		{"analytics", h.ListBucketAnalyticsConfigurations},
		{"intelligent-tiering", h.ListBucketIntelligentTieringConfigurations},
		{"inventory", h.ListBucketInventoryConfigurations},
		{"metrics", h.ListBucketMetricsConfigurations},
		{"object-lock", h.GetObjectLockConfiguration},
		{"abac", h.GetBucketAbac},
		{"metadata", h.GetBucketMetadataConfiguration},
		{"metadataTable", h.GetBucketMetadataTableConfiguration},
		{"session", h.CreateSession},
	}

	h.bucketPutRoutes = []s3Route{
		{"acl", h.PutBucketAcl},
		{"cors", h.PutBucketCors},
		{"policy", h.PutBucketPolicy},
		{"lifecycle", h.PutBucketLifecycleConfiguration},
		{"versioning", h.PutBucketVersioning},
		{"notification", h.PutBucketNotificationConfiguration},
		{"tagging", h.PutBucketTagging},
		{"website", h.PutBucketWebsite},
		{"logging", h.PutBucketLogging},
		{"replication", h.PutBucketReplication},
		{"encryption", h.PutBucketEncryption},
		{"accelerate", h.PutBucketAccelerateConfiguration},
		{"requestPayment", h.PutBucketRequestPayment},
		{"ownershipControls", h.PutBucketOwnershipControls},
		{"publicAccessBlock", h.PutPublicAccessBlock},
		{"analytics", h.PutBucketAnalyticsConfiguration},
		{"intelligent-tiering", h.PutBucketIntelligentTieringConfiguration},
		{"inventory", h.PutBucketInventoryConfiguration},
		{"metrics", h.PutBucketMetricsConfiguration},
		{"object-lock", h.PutObjectLockConfiguration},
		{"abac", h.PutBucketAbac},
		{"metadata", h.CreateBucketMetadataConfiguration},
		{"metadataTable", h.UpdateBucketMetadataTableConfiguration},
	}

	h.bucketDeleteRoutes = []s3Route{
		{"cors", h.DeleteBucketCors},
		{"policy", h.DeleteBucketPolicy},
		{"lifecycle", h.DeleteBucketLifecycle},
		{"tagging", h.DeleteBucketTagging},
		{"website", h.DeleteBucketWebsite},
		{"replication", h.DeleteBucketReplication},
		{"encryption", h.DeleteBucketEncryption},
		{"analytics", h.DeleteBucketAnalyticsConfiguration},
		{"intelligent-tiering", h.DeleteBucketIntelligentTieringConfiguration},
		{"inventory", h.DeleteBucketInventoryConfiguration},
		{"metrics", h.DeleteBucketMetricsConfiguration},
		{"ownershipControls", h.DeleteBucketOwnershipControls},
		{"publicAccessBlock", h.DeletePublicAccessBlock},
		{"metadata", h.DeleteBucketMetadataConfiguration},
		{"metadataTable", h.DeleteBucketMetadataTableConfiguration},
	}

	h.bucketPostRoutes = []s3Route{
		{"delete", h.DeleteObjects},
		{"metadataTable", h.CreateBucketMetadataTableConfiguration},
	}
}

// CreateBucket handles PUT /{bucket}
// AWS docs: https://docs.aws.amazon.com/AmazonS3/latest/API/API_CreateBucket.html
// usEast1 is the one region where re-creating a bucket you already own is not
// an error — see CreateBucket.
const usEast1 = "us-east-1"

// bucketNamespaceHeader is the AWS request header naming which S3 namespace
// CreateBucket should create the bucket in.
// https://docs.aws.amazon.com/AmazonS3/latest/API/API_CreateBucket.html
const bucketNamespaceHeader = "X-Amz-Bucket-Namespace"

// bucketNamespaceGlobal and bucketNamespaceAccountRegional are the two
// x-amz-bucket-namespace values AWS documents. bucketNamespaceGlobal also
// names Bucket.Namespace's zero value (see store.go) — a bucket created in
// the global namespace persists no namespace at all.
const (
	bucketNamespaceGlobal          = "global"
	bucketNamespaceAccountRegional = "account-regional"
)

func (h *Handler) CreateBucket(w http.ResponseWriter, r *http.Request) {
	bucket := chi.URLParam(r, "bucket")

	namespace := strings.TrimSpace(r.Header.Get(bucketNamespaceHeader))
	switch namespace {
	case "":
		namespace = bucketNamespaceGlobal
	case bucketNamespaceGlobal, bucketNamespaceAccountRegional:
		// recognised values
	default:
		// Unverified against real AWS: the exact error code for an
		// unrecognised x-amz-bucket-namespace value. This one stays
		// InvalidArgument while the name checks below moved to
		// InvalidBucketName, because the two reject different things: the
		// bucket name may be perfectly valid here and the bad input is a
		// request header. S3's documented error list gives InvalidArgument
		// ("Invalid Argument", 400) for exactly that, and reserves
		// InvalidBucketName for "the specified bucket is not valid".
		protocol.WriteXMLError(w, r, protocol.ErrInvalidArgument(
			"Invalid namespace type: "+namespace+". The value of x-amz-bucket-namespace must be global or account-regional."))
		return
	}

	region := middleware.RegionFromContext(r.Context(), h.cfg.Region)

	if aerr := h.validateNewBucketName(bucket, namespace, region); aerr != nil {
		protocol.WriteXMLError(w, r, aerr)
		return
	}

	b := &Bucket{Name: bucket, Region: region}
	if namespace == bucketNamespaceAccountRegional {
		b.Namespace = bucketNamespaceAccountRegional
	}
	created, aerr := h.createBucket(r.Context(), b)
	if aerr != nil {
		protocol.WriteXMLError(w, r, aerr)
		return
	}
	if !created {
		// Account-regional re-create is documented as a 409
		// BucketAlreadyOwnedByYou "in every region including us-east-1" — the
		// legacy 200-OK re-create behaviour below applies only to the global
		// namespace, so this branches before it rather than joining it.
		if namespace == bucketNamespaceAccountRegional {
			protocol.WriteXMLError(w, r, errBucketAlreadyExists(bucket))
			return
		}
		// S3 documents BucketAlreadyOwnedByYou as returned "in all AWS Regions
		// except in the North Virginia Region. For legacy compatibility, if you
		// re-create an existing bucket that you already own in the North
		// Virginia Region, Amazon S3 returns 200 OK". us-east-1 is the default
		// region, so any client that creates its bucket idempotently — which
		// the AWS SDKs' own examples do — depends on this.
		if region != usEast1 {
			protocol.WriteXMLError(w, r, errBucketAlreadyExists(bucket))
			return
		}
		w.Header().Set("Location", "/"+bucket)
		w.WriteHeader(http.StatusOK)
		return
	}

	w.Header().Set("Location", "/"+bucket)
	protocol.WriteEmpty(w, r, http.StatusOK)
}

// validateNewBucketName applies CreateBucket's naming rules for a bucket
// created in namespace. It is a separate step from createBucket so that an
// internal caller entitled to a name S3 reserves for AWS's own use — S3
// Tables' "--table-s3" warehouse buckets (#2067) — can apply its own rule
// instead, without a second copy of the creation path.
//
// A name that breaks the naming rules is InvalidBucketName — "The specified
// bucket is not valid.", 400 — per S3's documented error list (see
// protocol.ErrInvalidBucketName). serviceutil already assigns that code, so
// its error is returned unchanged. CreateBucket used to rewrite every one of
// them to InvalidArgument "to preserve the code expected by existing tests",
// which made the tests the wire contract instead of AWS; the swap was
// recorded as a deferred follow-up in docs/plans/host-routing-precedence.md
// §13 and is what this closes.
func (h *Handler) validateNewBucketName(bucket, namespace, region string) *protocol.AWSError {
	if namespace == bucketNamespaceAccountRegional {
		return serviceutil.ValidateAccountRegionalBucketName(bucket, h.cfg.AccountID, region)
	}
	if aerr := serviceutil.BucketName(bucket); aerr != nil {
		return aerr
	}
	if serviceutil.HasAccountRegionalBucketSuffix(bucket) {
		// AWS's 2026 naming-rules update reserves the "-an" suffix for
		// account regional namespace buckets. Unverified against real AWS:
		// the exact error code for a global-namespace CreateBucket that
		// carries it. It is a bucket-name rejection like the other
		// reserved-suffix rules — which live in serviceutil.BucketName and
		// answer InvalidBucketName — so it follows them rather than staying
		// behind on InvalidArgument.
		return protocol.ErrInvalidBucketName(
			"The specified bucket name is not valid. Bucket names must not end with the suffix -an unless created in your account regional namespace.")
	}
	return nil
}

// createBucket stores b, whose name has already been validated, unless a
// bucket of that name exists — in which case it reports created=false and
// leaves the existing bucket untouched, for the caller to answer the way its
// operation requires. It stamps the creation date and announces the new
// bucket on the event bus.
func (h *Handler) createBucket(ctx context.Context, b *Bucket) (created bool, aerr *protocol.AWSError) {
	if created, aerr = h.claimBucketName(ctx, b); !created {
		return false, aerr
	}

	// A bucket whose name carries a host-route label as a second-or-later dot
	// segment cannot be reached by bare virtual-hosted addressing —
	// "my.execute-api.localhost" parses as an API Gateway invoke. AWS accepts
	// the name and so does Overcast, and the AWS wire format has no field for
	// a warning, so a log line is the only channel available. Creation is
	// naturally once per name, so no dedup is needed.
	if label, reserved := middleware.BucketNameReservedLabel(b.Name); reserved {
		h.log.Warn("bucket name contains a reserved host label; it is not addressable in bare "+
			"virtual-hosted form — use path-style or {bucket}.s3.{host} addressing",
			zap.String("bucket", b.Name),
			zap.String("reserved_label", label),
		)
	}

	if h.bus != nil {
		h.bus.Publish(ctx, events.Event{
			Type:    events.S3BucketCreated,
			Time:    h.clk.Now(),
			Source:  "s3",
			Payload: events.ResourcePayload{Name: b.Name, ARN: protocol.ARN("", "", "s3", b.Name)},
		})
	}
	return true, nil
}

// claimBucketName stores b unless a bucket of its name exists, holding the
// name's lock across the check and the store, so of any number of concurrent
// creates exactly one sees the name free and every other one sees the bucket
// it created (#2126). Bucket names are global, not per region, so the name
// alone is the key.
func (h *Handler) claimBucketName(ctx context.Context, b *Bucket) (bool, *protocol.AWSError) {
	defer h.bucketLocks.Lock(b.Name)()
	exists, aerr := h.store.bucketExists(ctx, b.Name)
	if aerr != nil || exists {
		return false, aerr
	}
	b.CreationDate = h.clk.Now().UTC()
	if aerr := h.store.putBucket(ctx, b); aerr != nil {
		return false, aerr
	}
	return true, nil
}

// HeadBucket handles HEAD /{bucket}
// Returns 200 if the bucket exists, 404 if not.
// Guarded by x-amz-expected-bucket-owner (see expected_owner.go) — HeadBucket
// is registered directly on the chi router rather than reached through
// BucketGet's dispatch table, so it carries its own guard call.
func (h *Handler) HeadBucket(w http.ResponseWriter, r *http.Request) {
	if !h.checkExpectedBucketOwner(w, r) {
		return
	}
	bucket := chi.URLParam(r, "bucket")

	exists, aerr := h.store.bucketExists(r.Context(), bucket)
	if aerr != nil {
		protocol.WriteXMLError(w, r, aerr)
		return
	}
	if !exists {
		protocol.WriteXMLError(w, r, errNoSuchBucket(bucket))
		return
	}

	protocol.WriteEmpty(w, r, http.StatusOK)
}

// DeleteBucket handles DELETE /{bucket}
// AWS requires the bucket to be empty before deletion.
func (h *Handler) DeleteBucket(w http.ResponseWriter, r *http.Request) {
	bucket := chi.URLParam(r, "bucket")

	exists, aerr := h.store.bucketExists(r.Context(), bucket)
	if aerr != nil {
		protocol.WriteXMLError(w, r, aerr)
		return
	}
	if !exists {
		protocol.WriteXMLError(w, r, errNoSuchBucket(bucket))
		return
	}

	// Enforce AWS behaviour: bucket must be empty.
	objects, aerr := h.store.listObjects(r.Context(), bucket, "")
	if aerr != nil {
		protocol.WriteXMLError(w, r, aerr)
		return
	}
	if len(objects) > 0 {
		protocol.WriteXMLError(w, r, errBucketNotEmpty(bucket))
		return
	}

	if aerr := h.store.deleteBucket(r.Context(), bucket); aerr != nil {
		protocol.WriteXMLError(w, r, aerr)
		return
	}
	// deleteBucket drops the bucket's lifecycle record too; republish the
	// snapshot so the sweeper and the object paths stop seeing it at once.
	h.invalidateLifecycleIndex(r)

	if h.bus != nil {
		h.bus.Publish(r.Context(), events.Event{
			Type:    events.S3BucketDeleted,
			Time:    h.clk.Now(),
			Source:  "s3",
			Payload: events.ResourcePayload{Name: bucket, ARN: protocol.ARN("", "", "s3", bucket)},
		})
	}
	protocol.WriteEmpty(w, r, http.StatusNoContent)
}

// ListBuckets handles GET / — list all buckets owned by the account.
// AWS docs: https://docs.aws.amazon.com/AmazonS3/latest/API/API_ListBuckets.html
func (h *Handler) ListBuckets(w http.ResponseWriter, r *http.Request) {
	buckets, aerr := h.store.listBuckets(r.Context())
	if aerr != nil {
		protocol.WriteXMLError(w, r, aerr)
		return
	}

	type bucketEntry struct {
		Name         string    `xml:"Name"`
		CreationDate time.Time `xml:"CreationDate"`
	}
	type listBucketsResponse struct {
		XMLName xml.Name `xml:"ListAllMyBucketsResult"`
		Xmlns   string   `xml:"xmlns,attr"`
		Owner   struct {
			ID          string `xml:"ID"`
			DisplayName string `xml:"DisplayName"`
		} `xml:"Owner"`
		Buckets struct {
			Bucket []bucketEntry `xml:"Bucket"`
		} `xml:"Buckets"`
	}

	resp := listBucketsResponse{
		Xmlns: "http://s3.amazonaws.com/doc/2006-03-01/",
	}
	resp.Owner.ID = h.cfg.AccountID
	resp.Owner.DisplayName = "overcast"
	resp.Buckets.Bucket = make([]bucketEntry, len(buckets))
	for i, b := range buckets {
		resp.Buckets.Bucket[i] = bucketEntry{Name: b.Name, CreationDate: b.CreationDate}
	}
	protocol.WriteXML(w, r, http.StatusOK, resp)
}

// maxListPageSize is the largest page any S3 bucket listing returns, and the
// value used when max-keys is omitted. ListObjectsV2 documents it as
// returning "some or all (up to 1,000) of the objects in a bucket with each
// request", with a response that "might contain fewer keys but will never
// contain more".
const maxListPageSize = 1000

// errInvalidMaxKeys is S3's rejection of a max-keys value it cannot use.
// The API reference's Errors section lists only NoSuchBucket, but the two
// InvalidArgument messages below are what real S3 returns and what the
// AWS-verified conformance suites assert: s3-tests'
// test_bucket_list_maxkeys_invalid expects 400/InvalidArgument for
// "max-keys=blah", and both Ceph's RGW and OpenStack Swift's s3api — each
// validated against real S3 — carry these exact strings. The value is not
// silently replaced by the default, which is what Overcast did before: a
// client that mistyped max-keys got a full-size page and no indication that
// its limit had been ignored.
func errInvalidMaxKeys(message string) *protocol.AWSError {
	return &protocol.AWSError{
		Code:       "InvalidArgument",
		Message:    message,
		HTTPStatus: http.StatusBadRequest,
	}
}

// parseMaxKeys reads and validates the max-keys query parameter shared by
// ListObjects, ListObjectsV2 and ListObjectVersions, returning the effective
// page size.
//
// AWS's rules, in the order it applies them:
//
//   - omitted → the 1,000 default.
//   - not an integer, or outside the signed 32-bit range → InvalidArgument.
//   - negative → InvalidArgument (a distinct message naming the range).
//   - zero → a legal, empty, *untruncated* page. Some clients send
//     max-keys=0 to test whether a bucket is empty without listing it, so
//     this must not be treated as "unset"; s3-tests'
//     test_bucket_listv2_maxkeys_zero asserts IsTruncated false with no keys.
//   - above 1,000 → clamped, not rejected. The response echoes the *clamped*
//     value: Versity's AWS conformance test ListObjectsV2_exceeding_max_keys
//     sends 233453333 and asserts MaxKeys == 1000, and Ceph's RGW likewise
//     emits the bounded value rather than the requested one.
//
// An explicitly empty value (max-keys=) is treated as omitted rather than as
// a malformed integer. The references disagree on that one — Swift rejects
// it, Ceph defaults it — and no AWS SDK emits it, so Overcast keeps the
// permissive reading its other query helpers use.
func parseMaxKeys(r *http.Request) (int, *protocol.AWSError) {
	raw := r.URL.Query().Get("max-keys")
	if raw == "" {
		return maxListPageSize, nil
	}

	v, err := strconv.ParseInt(raw, 10, 64)
	if err != nil && !errors.Is(err, strconv.ErrRange) {
		return 0, errInvalidMaxKeys("Provided max-keys not an integer or within integer range")
	}
	// On ErrRange ParseInt still returns the saturated bound, so a value
	// too negative for int64 lands here rather than in the branch below.
	if v < 0 {
		return 0, errInvalidMaxKeys("Argument max-keys must be an integer between 0 and 2147483647")
	}
	if err != nil || v > math.MaxInt32 {
		return 0, errInvalidMaxKeys("Provided max-keys not an integer or within integer range")
	}
	if v > maxListPageSize {
		return maxListPageSize, nil
	}
	return int(v), nil
}

// encodingTypeURL is the only value AWS accepts for encoding-type.
const encodingTypeURL = "url"

// parseEncodingType reads and validates the encoding-type query parameter.
// The API reference gives "Valid Values: url"; anything else is rejected
// with InvalidArgument and the message real S3 uses, which Ceph's RGW,
// OpenStack Swift's s3api and Scality's CloudServer all reproduce verbatim.
// The empty string means "not requested", so no encoding is applied and the
// EncodingType response element is omitted.
func parseEncodingType(r *http.Request) (string, *protocol.AWSError) {
	raw := serviceutil.QueryString(r, "encoding-type", "")
	if raw == "" {
		return "", nil
	}
	if raw != encodingTypeURL {
		return "", &protocol.AWSError{
			Code:       "InvalidArgument",
			Message:    "Invalid Encoding Method specified in Request",
			HTTPStatus: http.StatusBadRequest,
		}
	}
	return encodingTypeURL, nil
}

// encodeListValue applies the requested encoding-type to one response
// element. ListObjectsV2 names the elements this covers exactly — "returns
// encoded key name values in the following response elements: Delimiter,
// Prefix, Key, and StartAfter" — and the AWS SDKs decode CommonPrefixes'
// Prefix alongside them (botocore's decode_list_object_v2 lists
// ('CommonPrefixes', 'Prefix') as a nested key), so it is encoded too.
// ContinuationToken and NextContinuationToken are not: they are opaque and
// the SDKs pass them straight back.
func encodeListValue(encodingType, v string) string {
	if encodingType != encodingTypeURL {
		return v
	}
	return s3URLEncode(v)
}

const upperhex = "0123456789ABCDEF"

// s3URLEncode percent-encodes v the way S3's encoding-type=url does: every
// byte outside the RFC 3986 unreserved set is written as %XX over the UTF-8
// bytes, except "/", which stays literal so a delimiter or folder-shaped
// prefix reads normally.
//
// s3-tests' test_bucket_listv2_encoding_basic pins the exact expectations
// against real S3: the keys foo+1/bar, foo/bar/xyzzy, quux ab/thud and
// asdf+b listed with delimiter=/ and encoding-type=url come back as key
// "asdf%2Bb" and prefixes "foo%2B1/", "foo/", "quux%20ab/", with Delimiter
// still "/". Note "+" becomes %2B and a space becomes %20 — this is path
// encoding, not form encoding, because the SDKs decode it with a plain
// percent-decoder that would leave a "+" alone and corrupt a key containing
// a space.
func s3URLEncode(v string) string {
	escapes := 0
	for i := 0; i < len(v); i++ {
		if !isS3URLSafe(v[i]) {
			escapes++
		}
	}
	if escapes == 0 {
		return v
	}

	var b strings.Builder
	b.Grow(len(v) + 2*escapes)
	for i := 0; i < len(v); i++ {
		c := v[i]
		if isS3URLSafe(c) {
			b.WriteByte(c)
			continue
		}
		b.WriteByte('%')
		b.WriteByte(upperhex[c>>4])
		b.WriteByte(upperhex[c&0x0f])
	}
	return b.String()
}

// isS3URLSafe reports whether c survives encoding-type=url unescaped.
func isS3URLSafe(c byte) bool {
	switch {
	case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9':
		return true
	case c == '-', c == '.', c == '_', c == '~', c == '/':
		return true
	}
	return false
}

// listObjectsV1Response is the XML envelope for ListObjects (v1).
// Differs from v2: uses Marker/NextMarker, no KeyCount, no ContinuationToken.
type listObjectsV1Response struct {
	XMLName        xml.Name        `xml:"ListBucketResult"`
	Xmlns          string          `xml:"xmlns,attr"`
	Name           string          `xml:"Name"`
	Prefix         string          `xml:"Prefix"`
	Delimiter      string          `xml:"Delimiter,omitempty"`
	Marker         string          `xml:"Marker"`
	NextMarker     string          `xml:"NextMarker,omitempty"`
	MaxKeys        int             `xml:"MaxKeys"`
	EncodingType   string          `xml:"EncodingType,omitempty"`
	IsTruncated    bool            `xml:"IsTruncated"`
	Contents       []objectSummary `xml:"Contents"`
	CommonPrefixes []commonPrefix  `xml:"CommonPrefixes"`
}

// ListObjectsV1 handles GET /{bucket} (no list-type) and GET /{bucket}?list-type=1.
// AWS docs: https://docs.aws.amazon.com/AmazonS3/latest/API/API_ListObjects.html
func (h *Handler) ListObjectsV1(w http.ResponseWriter, r *http.Request) {
	bucket := chi.URLParam(r, "bucket")
	prefix := serviceutil.QueryString(r, "prefix", "")
	delimiter := serviceutil.QueryString(r, "delimiter", "")
	maxKeys, aerr := parseMaxKeys(r)
	if aerr != nil {
		protocol.WriteXMLError(w, r, aerr)
		return
	}
	encodingType, aerr := parseEncodingType(r)
	if aerr != nil {
		protocol.WriteXMLError(w, r, aerr)
		return
	}
	marker := serviceutil.QueryString(r, "marker", "")
	// AWS: "Marker can be any key in the bucket" — a real (possibly
	// nonexistent) key, not an opaque token — see
	// https://docs.aws.amazon.com/AmazonS3/latest/API/API_ListObjects.html#API_ListObjects_RequestParameters.
	// Unlike V2's ContinuationToken there is nothing to validate: a
	// marker with no matching successor key is exactly what real AWS
	// returns an empty/short result for, not an error (the Errors section
	// of that doc page lists only NoSuchBucket).

	exists, aerr := h.store.bucketExists(r.Context(), bucket)
	if aerr != nil {
		protocol.WriteXMLError(w, r, aerr)
		return
	}
	if !exists {
		protocol.WriteXMLError(w, r, errNoSuchBucket(bucket))
		return
	}

	entries, aerr := h.buildListPage(r.Context(), bucket, prefix, delimiter, marker, maxKeys)
	if aerr != nil {
		protocol.WriteXMLError(w, r, aerr)
		return
	}

	entries, truncated := trimListPage(entries, maxKeys)

	var nextMarker string
	if truncated {
		nextMarker = entries[len(entries)-1].key
	}

	var contents []objectSummary
	var commonPrefixes []commonPrefix
	for _, e := range entries {
		if e.isPrefix {
			commonPrefixes = append(commonPrefixes, commonPrefix{Prefix: encodeListValue(encodingType, e.key)})
		} else {
			contents = append(contents, objectSummary{
				Key:          encodeListValue(encodingType, e.obj.Key),
				LastModified: e.obj.LastModified,
				ETag:         e.obj.ETag,
				Size:         e.obj.ContentLength,
				StorageClass: e.obj.effectiveStorageClass(),
			})
		}
	}

	resp := &listObjectsV1Response{
		Xmlns:          "http://s3.amazonaws.com/doc/2006-03-01/",
		Name:           bucket,
		Prefix:         encodeListValue(encodingType, prefix),
		Delimiter:      encodeListValue(encodingType, delimiter),
		Marker:         encodeListValue(encodingType, marker),
		NextMarker:     encodeListValue(encodingType, nextMarker),
		MaxKeys:        maxKeys,
		EncodingType:   encodingType,
		IsTruncated:    truncated,
		Contents:       contents,
		CommonPrefixes: commonPrefixes,
	}

	protocol.WriteXML(w, r, http.StatusOK, resp)
}

// trimListPage cuts a page built by buildListPage (or buildVersionsPage) down
// to maxKeys and reports whether more entries follow. Those builders collect
// one entry past maxKeys as their truncation signal, so the extra entry is
// dropped here.
//
// max-keys=0 is the case worth spelling out: the page trims to empty and
// IsTruncated stays false, because AWS treats "give me no keys" as a
// complete answer rather than the first page of a walk (s3-tests'
// test_bucket_listv2_maxkeys_zero). Returning true would hand a paginating
// client a NextContinuationToken and an infinite loop of empty pages.
func trimListPage[T any](entries []T, maxKeys int) ([]T, bool) {
	if len(entries) <= maxKeys {
		return entries, false
	}
	return entries[:maxKeys], maxKeys > 0
}

// listTypeDispatch routes list-type=2 to ListObjectsV2 and all other values
// (including blank/1) to ListObjectsV1.
func (h *Handler) listTypeDispatch(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("list-type") == "2" {
		h.ListObjectsV2(w, r)
	} else {
		h.ListObjectsV1(w, r)
	}
}

// listObjectsV2Response is the XML envelope for ListObjectsV2.
type listObjectsV2Response struct {
	XMLName               xml.Name        `xml:"ListBucketResult"`
	Xmlns                 string          `xml:"xmlns,attr"`
	Name                  string          `xml:"Name"`
	Prefix                string          `xml:"Prefix"`
	Delimiter             string          `xml:"Delimiter,omitempty"`
	EncodingType          string          `xml:"EncodingType,omitempty"`
	KeyCount              int             `xml:"KeyCount"`
	MaxKeys               int             `xml:"MaxKeys"`
	IsTruncated           bool            `xml:"IsTruncated"`
	ContinuationToken     string          `xml:"ContinuationToken,omitempty"`
	NextContinuationToken string          `xml:"NextContinuationToken,omitempty"`
	StartAfter            string          `xml:"StartAfter,omitempty"`
	Contents              []objectSummary `xml:"Contents"`
	CommonPrefixes        []commonPrefix  `xml:"CommonPrefixes"`
}

type objectSummary struct {
	Key          string    `xml:"Key"`
	LastModified time.Time `xml:"LastModified"`
	ETag         string    `xml:"ETag"`
	Size         int64     `xml:"Size"`
	StorageClass string    `xml:"StorageClass"`
	// Owner is populated only when ListObjectsV2 is called with
	// fetch-owner=true: "The owner field is not present in ListObjectsV2 by
	// default." A nil pointer omits the element entirely, which is also
	// what ListObjects (v1) wants here — it has no fetch-owner parameter,
	// and Overcast does not yet return per-object owners for it.
	Owner *ownerXML `xml:"Owner,omitempty"`
}

type commonPrefix struct {
	Prefix string `xml:"Prefix"`
}

// pageEntry is one "effective" entry in a ListObjects{V1,V2} response —
// either a leaf object or a delimiter-collapsed common prefix. key is the
// "effective key": the object's own key for leaves, or the collapsed prefix
// string (e.g. "photos/") for CommonPrefixes.
type pageEntry struct {
	isPrefix bool
	key      string
	obj      *Object
}

// internalListPageChunk is the internal ScanPage fetch size buildListPage
// uses while looping to fill one client-facing page. It is deliberately
// independent of the client's requested MaxKeys: with a delimiter, many
// consecutive keys can collapse into a single CommonPrefix (e.g. thousands
// of objects under one "folder"), so a small MaxKeys does not bound how many
// raw keys must be scanned before the next *effective* entry appears —
// storage-access-plan.md A6's explicit shoehorn warning. A chunk size fixed
// independently of MaxKeys keeps the number of internal round trips
// reasonable regardless of how the client paginates.
const internalListPageChunk = 1000

// buildListPage incrementally scans a bucket's objects — via repeated
// s3Store.listObjectsPage calls (ScanPage-backed), never a full-bucket Scan
// — and returns up to maxKeys+1 effective entries (objects + collapsed
// common prefixes) with keys greater than startAfter. The caller must trim
// the result to maxKeys and treat a maxKeys+1-th entry as the truncation
// signal (mirrors the pre-A6 single-Scan implementation's own convention).
//
// This is NOT a 1:1 ScanPage swap for the old full-bucket Scan: because a
// delimiter can collapse many consecutive keys into one CommonPrefix before
// a page fills, the loop below keeps pulling internal ScanPage pages —
// carrying the storage cursor forward across them via listObjectsPage's
// nextKey — until it has accumulated enough effective entries or the bucket
// is exhausted. The delimiter/common-prefix collapse rules themselves, and
// the startAfter/marker skip semantics, are unchanged from before A6; only
// the source of raw keys becomes incremental instead of fully materialized
// up front. Memory is now bounded by internalListPageChunk instead of the
// bucket size.
func (h *Handler) buildListPage(ctx context.Context, bucket, prefix, delimiter, startAfter string, maxKeys int) ([]pageEntry, *protocol.AWSError) {
	var entries []pageEntry
	seenPrefixes := map[string]bool{}
	cursor := startAfter

	for {
		objs, nextCursor, aerr := h.store.listObjectsPage(ctx, bucket, prefix, cursor, internalListPageChunk)
		if aerr != nil {
			return nil, aerr
		}

		for _, obj := range objs {
			if obj.DeleteMarker {
				// A key whose current version is a delete marker is absent as
				// far as ListObjects is concerned: AWS surfaces markers only
				// through ListObjectVersions.
				continue
			}
			effectiveKey := obj.Key
			isPrefix := false
			cpKey := ""

			if delimiter != "" {
				remainder := obj.Key[len(prefix):]
				if idx := strings.Index(remainder, delimiter); idx >= 0 {
					cpKey = prefix + remainder[:idx+len(delimiter)]
					effectiveKey = cpKey
					isPrefix = true
				}
			}

			// Skip entries already returned on a previous page. Compared
			// against the ORIGINAL startAfter (not the advancing internal
			// cursor): because all objects inside a common prefix have
			// actual keys lexicographically greater than the prefix string
			// itself (e.g. "photos/" < "photos/img.jpg"), ScanPage's raw-key
			// cursor alone would let already-collapsed objects back in
			// (a later object with a raw key past the cursor can still
			// collapse into a prefix effective key <= startAfter) — this
			// check is what re-excludes them.
			if startAfter != "" && effectiveKey <= startAfter {
				continue
			}

			if isPrefix {
				if !seenPrefixes[cpKey] {
					seenPrefixes[cpKey] = true
					entries = append(entries, pageEntry{isPrefix: true, key: cpKey})
				}
			} else {
				entries = append(entries, pageEntry{key: obj.Key, obj: obj})
			}

			// Collect one extra entry to detect truncation without scanning
			// everything.
			if len(entries) > maxKeys {
				return entries, nil
			}
		}

		if nextCursor == "" {
			// Reached the end of the bucket/prefix range.
			return entries, nil
		}
		cursor = nextCursor
	}
}

// errInvalidContinuationToken mirrors real S3's rejection of a
// garbled/foreign ListObjectsV2 ContinuationToken. AWS's API reference page
// documents only NoSuchBucket in its Errors section, but the documented
// InvalidArgument code is well established as the observed behavior for an
// unparseable continuation token ("The continuation token provided is
// incorrect") — see e.g.
// https://github.com/aws/aws-sdk-js-v3/issues/1636. Before this fix, a
// garbled token silently fell back to "start from the beginning"
// (pagination-plan.md G4 / storage-access-plan.md A6's invalid-token gap).
func errInvalidContinuationToken() *protocol.AWSError {
	return &protocol.AWSError{
		Code:       "InvalidArgument",
		Message:    "The continuation token provided is incorrect",
		HTTPStatus: http.StatusBadRequest,
	}
}

// listObjectsV2Page resolves one ListObjectsV2 page: the entries after the
// continuation token (or, on a first page, after startAfter), trimmed to
// maxKeys, and the token for the page after them — empty when this page is the
// last. The HTTP handler and the in-process Service.ListObjects both page
// through it, so they answer the same keys and the same errors.
func (h *Handler) listObjectsV2Page(ctx context.Context, bucket, prefix, delimiter, contToken, startAfter string, maxKeys int) ([]pageEntry, string, *protocol.AWSError) {
	// Decode the opaque continuation token to a "start-after" key. The
	// token is base64(lastEffectiveKeyOnPreviousPage) — P3's cursor-in-token
	// pattern, just a raw string cursor rather than M1's integer-index
	// codec (the wrong shape here: S3's cursor is a key, not an index).
	// AWS rejects an invalid/garbled token with InvalidArgument rather than
	// silently restarting from page 1 (pagination-plan.md G4).
	if contToken != "" {
		decoded, err := base64.StdEncoding.DecodeString(contToken)
		if err != nil {
			return nil, "", errInvalidContinuationToken()
		}
		startAfter = string(decoded)
	}

	exists, aerr := h.store.bucketExists(ctx, bucket)
	if aerr != nil {
		return nil, "", aerr
	}
	if !exists {
		return nil, "", errNoSuchBucket(bucket)
	}

	entries, aerr := h.buildListPage(ctx, bucket, prefix, delimiter, startAfter, maxKeys)
	if aerr != nil {
		return nil, "", aerr
	}

	entries, truncated := trimListPage(entries, maxKeys)
	if !truncated {
		return entries, "", nil
	}
	return entries, base64.StdEncoding.EncodeToString([]byte(entries[len(entries)-1].key)), nil
}

// ListObjectsV2 handles GET /{bucket}?list-type=2.
// AWS docs: https://docs.aws.amazon.com/AmazonS3/latest/API/API_ListObjectsV2.html
func (h *Handler) ListObjectsV2(w http.ResponseWriter, r *http.Request) {
	bucket := chi.URLParam(r, "bucket")
	prefix := serviceutil.QueryString(r, "prefix", "")
	delimiter := serviceutil.QueryString(r, "delimiter", "")
	maxKeys, aerr := parseMaxKeys(r)
	if aerr != nil {
		protocol.WriteXMLError(w, r, aerr)
		return
	}
	encodingType, aerr := parseEncodingType(r)
	if aerr != nil {
		protocol.WriteXMLError(w, r, aerr)
		return
	}
	// AWS's FetchOwner is a plain boolean with no documented error for a
	// non-boolean value, so anything that is not "true" reads as false —
	// the default the doc describes ("The owner field is not present in
	// ListObjectsV2 by default").
	fetchOwner := strings.EqualFold(serviceutil.QueryString(r, "fetch-owner", ""), "true")
	contToken := serviceutil.QueryString(r, "continuation-token", "")
	requestedStartAfter := serviceutil.QueryString(r, "start-after", "")

	entries, nextToken, aerr := h.listObjectsV2Page(r.Context(), bucket, prefix, delimiter, contToken, requestedStartAfter, maxKeys)
	if aerr != nil {
		protocol.WriteXMLError(w, r, aerr)
		return
	}
	truncated := nextToken != ""

	// fetch-owner returns the bucket owner for every key. Overcast has a
	// single account, so that is the same owner ListObjectVersions and
	// ListBuckets already report.
	var owner *ownerXML
	if fetchOwner {
		owner = &ownerXML{ID: h.cfg.AccountID, DisplayName: "overcast"}
	}

	// Build typed response slices.
	var contents []objectSummary
	var commonPrefixes []commonPrefix
	for _, e := range entries {
		if e.isPrefix {
			commonPrefixes = append(commonPrefixes, commonPrefix{Prefix: encodeListValue(encodingType, e.key)})
		} else {
			contents = append(contents, objectSummary{
				Key:          encodeListValue(encodingType, e.obj.Key),
				LastModified: e.obj.LastModified,
				ETag:         e.obj.ETag,
				Size:         e.obj.ContentLength,
				StorageClass: e.obj.effectiveStorageClass(),
				Owner:        owner,
			})
		}
	}

	resp := &listObjectsV2Response{
		Xmlns:                 "http://s3.amazonaws.com/doc/2006-03-01/",
		Name:                  bucket,
		Prefix:                encodeListValue(encodingType, prefix),
		Delimiter:             encodeListValue(encodingType, delimiter),
		EncodingType:          encodingType,
		KeyCount:              len(entries),
		MaxKeys:               maxKeys,
		IsTruncated:           truncated,
		ContinuationToken:     contToken,
		NextContinuationToken: nextToken,
		StartAfter:            encodeListValue(encodingType, requestedStartAfter),
		Contents:              contents,
		CommonPrefixes:        commonPrefixes,
	}

	protocol.WriteXML(w, r, http.StatusOK, resp)
}

// getBucketLocationResponse is the XML for GetBucketLocation.
type getBucketLocationResponse struct {
	XMLName            xml.Name `xml:"LocationConstraint"`
	Xmlns              string   `xml:"xmlns,attr"`
	LocationConstraint string   `xml:",chardata"`
}

// GetBucketLocation handles GET /{bucket}?location.
// AWS docs: https://docs.aws.amazon.com/AmazonS3/latest/API/API_GetBucketLocation.html
func (h *Handler) GetBucketLocation(w http.ResponseWriter, r *http.Request) {
	bucket := chi.URLParam(r, "bucket")

	b, aerr := h.store.getBucket(r.Context(), bucket)
	if aerr != nil {
		protocol.WriteXMLError(w, r, aerr)
		return
	}

	resp := &getBucketLocationResponse{
		Xmlns:              "http://s3.amazonaws.com/doc/2006-03-01/",
		LocationConstraint: b.Region,
	}
	protocol.WriteXML(w, r, http.StatusOK, resp)
}

// ---- Versioning ------------------------------------------------------------

type versioningConfigurationXML struct {
	XMLName xml.Name `xml:"VersioningConfiguration"`
	Xmlns   string   `xml:"xmlns,attr,omitempty"`
	Status  string   `xml:"Status,omitempty"`
}

// GetBucketVersioning handles GET /{bucket}?versioning.
// AWS docs: https://docs.aws.amazon.com/AmazonS3/latest/API/API_GetBucketVersioning.html
func (h *Handler) GetBucketVersioning(w http.ResponseWriter, r *http.Request) {
	bucket := chi.URLParam(r, "bucket")
	b, aerr := h.store.getBucket(r.Context(), bucket)
	if aerr != nil {
		protocol.WriteXMLError(w, r, aerr)
		return
	}
	resp := &versioningConfigurationXML{
		Xmlns:  "http://s3.amazonaws.com/doc/2006-03-01/",
		Status: b.VersioningStatus,
	}
	protocol.WriteXML(w, r, http.StatusOK, resp)
}

// PutBucketVersioning handles PUT /{bucket}?versioning.
// AWS docs: https://docs.aws.amazon.com/AmazonS3/latest/API/API_PutBucketVersioning.html
//
// The three states are not a simple on/off. A bucket starts with no status at
// all and has never had versions; Enabled mints a version id for every write;
// Suspended keeps the versions already recorded and writes new objects as the
// null version, which is a different thing from returning to the unversioned
// state — that transition does not exist on AWS and does not exist here.
func (h *Handler) PutBucketVersioning(w http.ResponseWriter, r *http.Request) {
	bucket := chi.URLParam(r, "bucket")
	var req versioningConfigurationXML
	if err := xml.NewDecoder(r.Body).Decode(&req); err != nil {
		protocol.WriteXMLError(w, r, protocol.ErrInvalidArgument("malformed XML"))
		return
	}
	if req.Status != versioningEnabled && req.Status != versioningSuspended {
		protocol.WriteXMLError(w, r, &protocol.AWSError{
			Code:       "MalformedXML",
			Message:    "The XML you provided was not well-formed or did not validate against our published schema",
			HTTPStatus: http.StatusBadRequest,
		})
		return
	}
	b, aerr := h.store.getBucket(r.Context(), bucket)
	if aerr != nil {
		protocol.WriteXMLError(w, r, aerr)
		return
	}
	b.VersioningStatus = req.Status
	if aerr := h.store.putBucket(r.Context(), b); aerr != nil {
		protocol.WriteXMLError(w, r, aerr)
		return
	}
	// Objects the bucket already held are the null versions of their keys, as
	// on AWS. Giving them their place in the history here means the very next
	// ListObjectVersions sees one ordered history rather than two half-worlds.
	if aerr := h.ensureVersionHistory(r.Context(), b); aerr != nil {
		protocol.WriteXMLError(w, r, aerr)
		return
	}
	protocol.WriteEmpty(w, r, http.StatusOK)
}

// ─── CORS configuration ────────────────────────────────────────────────────────

type corsConfigurationXML struct {
	XMLName   xml.Name      `xml:"CORSConfiguration"`
	Xmlns     string        `xml:"xmlns,attr,omitempty"`
	CORSRules []corsRuleXML `xml:"CORSRule"`
}

type corsRuleXML struct {
	AllowedHeader []string `xml:"AllowedHeader"`
	AllowedMethod []string `xml:"AllowedMethod"`
	AllowedOrigin []string `xml:"AllowedOrigin"`
	ExposeHeader  []string `xml:"ExposeHeader"`
	MaxAgeSeconds int      `xml:"MaxAgeSeconds,omitempty"`
}

// putBucketCors handles PUT /{bucket}?cors.
func (h *Handler) putBucketCors(w http.ResponseWriter, r *http.Request) {
	bucket := chi.URLParam(r, "bucket")
	var req corsConfigurationXML
	if err := xml.NewDecoder(r.Body).Decode(&req); err != nil {
		protocol.WriteXMLError(w, r, protocol.ErrInvalidArgument("malformed XML"))
		return
	}
	b, aerr := h.store.getBucket(r.Context(), bucket)
	if aerr != nil {
		protocol.WriteXMLError(w, r, aerr)
		return
	}
	rules := make([]CORSRule, 0, len(req.CORSRules))
	for _, rx := range req.CORSRules {
		rules = append(rules, CORSRule{
			AllowedHeaders: rx.AllowedHeader,
			AllowedMethods: rx.AllowedMethod,
			AllowedOrigins: rx.AllowedOrigin,
			ExposeHeaders:  rx.ExposeHeader,
			MaxAgeSeconds:  rx.MaxAgeSeconds,
		})
	}
	b.CORSRules = rules
	if aerr := h.store.putBucket(r.Context(), b); aerr != nil {
		protocol.WriteXMLError(w, r, aerr)
		return
	}
	protocol.WriteEmpty(w, r, http.StatusOK)
}

// getBucketCors handles GET /{bucket}?cors.
func (h *Handler) getBucketCors(w http.ResponseWriter, r *http.Request) {
	bucket := chi.URLParam(r, "bucket")
	b, aerr := h.store.getBucket(r.Context(), bucket)
	if aerr != nil {
		protocol.WriteXMLError(w, r, aerr)
		return
	}
	if len(b.CORSRules) == 0 {
		protocol.WriteXMLError(w, r, &protocol.AWSError{
			Code:       "NoSuchCORSConfiguration",
			Message:    "The CORS configuration does not exist",
			HTTPStatus: http.StatusNotFound,
		})
		return
	}
	protocol.WriteXML(w, r, http.StatusOK, &corsConfigurationXML{Xmlns: s3XMLNamespace, CORSRules: corsRulesToXML(b.CORSRules)})
}

func corsRulesToXML(rules []CORSRule) []corsRuleXML {
	out := make([]corsRuleXML, 0, len(rules))
	for _, rule := range rules {
		out = append(out, corsRuleXML{
			AllowedHeader: rule.AllowedHeaders,
			AllowedMethod: rule.AllowedMethods,
			AllowedOrigin: rule.AllowedOrigins,
			ExposeHeader:  rule.ExposeHeaders,
			MaxAgeSeconds: rule.MaxAgeSeconds,
		})
	}
	return out
}

// deleteBucketCors handles DELETE /{bucket}?cors.
func (h *Handler) deleteBucketCors(w http.ResponseWriter, r *http.Request) {
	bucket := chi.URLParam(r, "bucket")
	b, aerr := h.store.getBucket(r.Context(), bucket)
	if aerr != nil {
		protocol.WriteXMLError(w, r, aerr)
		return
	}
	b.CORSRules = nil
	if aerr := h.store.putBucket(r.Context(), b); aerr != nil {
		protocol.WriteXMLError(w, r, aerr)
		return
	}
	protocol.WriteEmpty(w, r, http.StatusNoContent)
}

// PutBucketPolicy handles PUT /{bucket}?policy
// AWS docs: https://docs.aws.amazon.com/AmazonS3/latest/API/API_PutBucketPolicy.html
func (h *Handler) PutBucketPolicy(w http.ResponseWriter, r *http.Request) {
	bucket := chi.URLParam(r, "bucket")
	body, err := io.ReadAll(io.LimitReader(r.Body, 20*1024+1))
	if err != nil || len(body) == 0 {
		protocol.WriteXMLError(w, r, &protocol.AWSError{
			Code:       "MalformedPolicy",
			Message:    "Missing or invalid policy document",
			HTTPStatus: http.StatusBadRequest,
		})
		return
	}
	if len(body) > 20*1024 {
		protocol.WriteXMLError(w, r, &protocol.AWSError{
			Code:       "MalformedPolicy",
			Message:    "Policy document must not exceed 20 KB",
			HTTPStatus: http.StatusBadRequest,
		})
		return
	}
	if !json.Valid(body) {
		protocol.WriteXMLError(w, r, &protocol.AWSError{
			Code:       "MalformedPolicy",
			Message:    "Policy is not valid JSON",
			HTTPStatus: http.StatusBadRequest,
		})
		return
	}
	b, aerr := h.store.getBucket(r.Context(), bucket)
	if aerr != nil {
		protocol.WriteXMLError(w, r, aerr)
		return
	}
	b.Policy = string(body)
	if aerr := h.store.putBucket(r.Context(), b); aerr != nil {
		protocol.WriteXMLError(w, r, aerr)
		return
	}
	protocol.WriteEmpty(w, r, http.StatusOK)
}

// GetBucketPolicy handles GET /{bucket}?policy
// AWS docs: https://docs.aws.amazon.com/AmazonS3/latest/API/API_GetBucketPolicy.html
func (h *Handler) GetBucketPolicy(w http.ResponseWriter, r *http.Request) {
	bucket := chi.URLParam(r, "bucket")
	b, aerr := h.store.getBucket(r.Context(), bucket)
	if aerr != nil {
		protocol.WriteXMLError(w, r, aerr)
		return
	}
	if b.Policy == "" {
		protocol.WriteXMLError(w, r, &protocol.AWSError{
			Code:       "NoSuchBucketPolicy",
			Message:    "The bucket policy does not exist",
			HTTPStatus: http.StatusNotFound,
		})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("x-amz-request-id", protocol.RequestIDFromContext(r.Context()))
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(b.Policy)) //nolint:errcheck
}

// GetBucketPolicyStatus handles GET /{bucket}?policyStatus
// AWS docs: https://docs.aws.amazon.com/AmazonS3/latest/API/API_GetBucketPolicyStatus.html
func (h *Handler) GetBucketPolicyStatus(w http.ResponseWriter, r *http.Request) {
	protocol.NotImplementedXML(w, r)
}

// DeleteBucketPolicy handles DELETE /{bucket}?policy
// AWS docs: https://docs.aws.amazon.com/AmazonS3/latest/API/API_DeleteBucketPolicy.html
func (h *Handler) DeleteBucketPolicy(w http.ResponseWriter, r *http.Request) {
	bucket := chi.URLParam(r, "bucket")
	b, aerr := h.store.getBucket(r.Context(), bucket)
	if aerr != nil {
		protocol.WriteXMLError(w, r, aerr)
		return
	}
	b.Policy = ""
	if aerr := h.store.putBucket(r.Context(), b); aerr != nil {
		protocol.WriteXMLError(w, r, aerr)
		return
	}
	protocol.WriteEmpty(w, r, http.StatusNoContent)
}
