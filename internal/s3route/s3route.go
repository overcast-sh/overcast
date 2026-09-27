// Package s3route names the operation S3 serves a request as.
//
// S3 has no operation header and no path prefix of its own. It serves a
// request by its method, by whether its path addresses the service, a bucket
// or an object, and by the sub-resource named in its query string: PUT
// /bucket is CreateBucket, and PUT /bucket?policy is PutBucketPolicy. This
// package is that decision, and the only copy of it. S3's handler dispatches
// on it, and IAM enforcement and the request log name the request by it, so
// what is authorised, what is logged and what is served are the same
// operation by construction (#2284).
//
// The request must be path-style: middleware.HostAddressing rewrites a
// virtual-hosted request to its path-style form before anything reads it.
//
// The query parameter x-id plays no part. The SDKs add it to name the
// operation they meant, but S3 never reads it, so PUT /bucket/key?x-id=GetObject
// is PutObject.
//
// The pinned Smithy model describes most of these bindings too, as the query
// literals of S3's @http traits, but it cannot make this decision alone: it
// tells PutObject, UploadPart, CopyObject and UploadPartCopy apart only by
// the x-id literal S3 ignores, and gives no order to try sub-resources in.
// TestOperations_matchTheModel holds each operation here to its modeled
// method and sub-resource instead.
package s3route

import (
	"net/http"
	"net/url"
	"slices"
	"strings"
)

// level is the part of S3's namespace a path addresses.
type level int

const (
	levelService level = iota // "/"
	levelBucket               // "/bucket" or "/bucket/"
	levelObject               // "/bucket/key..."
)

// levelOf reads a path the way S3's routes match it: "/" is the service,
// "/{bucket}" and "/{bucket}/" are the bucket, and anything longer is an
// object, whose key may itself begin with a slash.
func levelOf(path string) level {
	rest := strings.TrimPrefix(path, "/")
	if rest == "" {
		return levelService
	}
	if _, key, found := strings.Cut(rest, "/"); !found || key == "" {
		return levelBucket
	}
	return levelObject
}

// route is one method at one level.
type route struct {
	level  level
	method string
}

// subResource is the query that selects an operation: one parameter, or
// several joined by "&" that must all be present, as in "analytics&id".
type subResource struct {
	params    string
	operation string
}

// names lists sub's parameters, the sub-resource first.
func (sub subResource) names() []string {
	return strings.Split(sub.params, "&")
}

// present reports whether query carries every one of sub's parameters. It
// walks them in place rather than through names: it runs on every request.
func (sub subResource) present(query url.Values) bool {
	for params := sub.params; params != ""; {
		var param string
		param, params, _ = strings.Cut(params, "&")
		if !query.Has(param) {
			return false
		}
	}
	return true
}

// subResources lists, per route, the sub-resources that select an operation,
// in the order S3 tries them: the first one present wins.
//
// The model tells a Get from the List on the same sub-resource only by the
// x-id literal S3 ignores: it binds GetBucketAnalyticsConfiguration and
// ListBucketAnalyticsConfigurations both to GET ?analytics. S3 tells them
// apart by the query member only the Get carries, id or annotationName, so
// the Get, which names both, comes first.
var subResources = map[route][]subResource{
	{levelService, http.MethodGet}: {
		// An Overcast selector: AWS serves ListDirectoryBuckets on the
		// s3express-control host, which Overcast does not route.
		{"directory-buckets", "ListDirectoryBuckets"},
	},
	{levelBucket, http.MethodGet}: {
		{"list-type", "ListObjectsV2"},
		{"location", "GetBucketLocation"},
		{"acl", "GetBucketAcl"},
		{"cors", "GetBucketCors"},
		{"policy", "GetBucketPolicy"},
		{"policyStatus", "GetBucketPolicyStatus"},
		{"lifecycle", "GetBucketLifecycleConfiguration"},
		{"versioning", "GetBucketVersioning"},
		{"notification", "GetBucketNotificationConfiguration"},
		{"tagging", "GetBucketTagging"},
		{"website", "GetBucketWebsite"},
		{"logging", "GetBucketLogging"},
		{"replication", "GetBucketReplication"},
		{"encryption", "GetBucketEncryption"},
		{"accelerate", "GetBucketAccelerateConfiguration"},
		{"requestPayment", "GetBucketRequestPayment"},
		{"ownershipControls", "GetBucketOwnershipControls"},
		{"publicAccessBlock", "GetPublicAccessBlock"},
		{"uploads", "ListMultipartUploads"},
		{"versions", "ListObjectVersions"},
		{"analytics&id", "GetBucketAnalyticsConfiguration"},
		{"analytics", "ListBucketAnalyticsConfigurations"},
		{"intelligent-tiering&id", "GetBucketIntelligentTieringConfiguration"},
		{"intelligent-tiering", "ListBucketIntelligentTieringConfigurations"},
		{"inventory&id", "GetBucketInventoryConfiguration"},
		{"inventory", "ListBucketInventoryConfigurations"},
		{"metrics&id", "GetBucketMetricsConfiguration"},
		{"metrics", "ListBucketMetricsConfigurations"},
		{"object-lock", "GetObjectLockConfiguration"},
		{"abac", "GetBucketAbac"},
		{"metadataConfiguration", "GetBucketMetadataConfiguration"},
		{"metadataTable", "GetBucketMetadataTableConfiguration"},
		{"session", "CreateSession"},
	},
	{levelBucket, http.MethodPut}: {
		{"acl", "PutBucketAcl"},
		{"cors", "PutBucketCors"},
		{"policy", "PutBucketPolicy"},
		{"lifecycle", "PutBucketLifecycleConfiguration"},
		{"versioning", "PutBucketVersioning"},
		{"notification", "PutBucketNotificationConfiguration"},
		{"tagging", "PutBucketTagging"},
		{"website", "PutBucketWebsite"},
		{"logging", "PutBucketLogging"},
		{"replication", "PutBucketReplication"},
		{"encryption", "PutBucketEncryption"},
		{"accelerate", "PutBucketAccelerateConfiguration"},
		{"requestPayment", "PutBucketRequestPayment"},
		{"ownershipControls", "PutBucketOwnershipControls"},
		{"publicAccessBlock", "PutPublicAccessBlock"},
		{"analytics", "PutBucketAnalyticsConfiguration"},
		{"intelligent-tiering", "PutBucketIntelligentTieringConfiguration"},
		{"inventory", "PutBucketInventoryConfiguration"},
		{"metrics", "PutBucketMetricsConfiguration"},
		{"object-lock", "PutObjectLockConfiguration"},
		{"abac", "PutBucketAbac"},
		{"metadataInventoryTable", "UpdateBucketMetadataInventoryTableConfiguration"},
		{"metadataJournalTable", "UpdateBucketMetadataJournalTableConfiguration"},
		{"metadataAnnotationTable", "UpdateBucketMetadataAnnotationTableConfiguration"},
	},
	{levelBucket, http.MethodDelete}: {
		{"cors", "DeleteBucketCors"},
		{"policy", "DeleteBucketPolicy"},
		{"lifecycle", "DeleteBucketLifecycle"},
		{"tagging", "DeleteBucketTagging"},
		{"website", "DeleteBucketWebsite"},
		{"replication", "DeleteBucketReplication"},
		{"encryption", "DeleteBucketEncryption"},
		{"analytics", "DeleteBucketAnalyticsConfiguration"},
		{"intelligent-tiering", "DeleteBucketIntelligentTieringConfiguration"},
		{"inventory", "DeleteBucketInventoryConfiguration"},
		{"metrics", "DeleteBucketMetricsConfiguration"},
		{"ownershipControls", "DeleteBucketOwnershipControls"},
		{"publicAccessBlock", "DeletePublicAccessBlock"},
		{"metadataConfiguration", "DeleteBucketMetadataConfiguration"},
		{"metadataTable", "DeleteBucketMetadataTableConfiguration"},
	},
	{levelBucket, http.MethodPost}: {
		{"delete", "DeleteObjects"},
		{"metadataConfiguration", "CreateBucketMetadataConfiguration"},
		{"metadataTable", "CreateBucketMetadataTableConfiguration"},
	},
	{levelObject, http.MethodGet}: {
		{"acl", "GetObjectAcl"},
		{"tagging", "GetObjectTagging"},
		{"attributes", "GetObjectAttributes"},
		{"legal-hold", "GetObjectLegalHold"},
		{"retention", "GetObjectRetention"},
		{"torrent", "GetObjectTorrent"},
		{"annotation&annotationName", "GetObjectAnnotation"},
		{"annotation", "ListObjectAnnotations"},
		{"uploadId", "ListParts"},
	},
	{levelObject, http.MethodPut}: {
		// partNumber leads: a part upload carries uploadId too, and no
		// sub-resource below applies to one.
		{"partNumber", "UploadPart"},
		{"acl", "PutObjectAcl"},
		{"tagging", "PutObjectTagging"},
		{"legal-hold", "PutObjectLegalHold"},
		{"retention", "PutObjectRetention"},
		{"renameObject", "RenameObject"},
		{"encryption", "UpdateObjectEncryption"},
		{"annotation", "PutObjectAnnotation"},
	},
	{levelObject, http.MethodDelete}: {
		{"tagging", "DeleteObjectTagging"},
		{"annotation", "DeleteObjectAnnotation"},
		{"uploadId", "AbortMultipartUpload"},
	},
	{levelObject, http.MethodPost}: {
		{"uploads", "CreateMultipartUpload"},
		{"uploadId", "CompleteMultipartUpload"},
		{"restore", "RestoreObject"},
		{"select", "SelectObjectContent"},
	},
}

// binding is a method on a fixed path.
type binding struct {
	method string
	path   string
}

// fixedPaths names the operations S3 binds to a path of their own rather
// than to a bucket or an object. No bucket can be named
// WriteGetObjectResponse, since bucket names are lowercase, so the path
// addresses none. AWS serves it on a host prefixed with the request route,
// which Overcast does not route: a client reaches it path-style.
var fixedPaths = map[binding]string{
	{http.MethodPost, "/WriteGetObjectResponse"}: "WriteGetObjectResponse",
}

// plain names the operation a route serves when no sub-resource selects one.
// A route absent here serves none: S3 answers a POST to a bucket or an object
// without a sub-resource with an error, not an operation.
//
// A query parameter S3 does not know is ignored, so a request carrying one
// is served as the plain operation. A sub-resource it does know never is:
// served on another method it is refused (Refused), and one the model binds
// but subResources lacked would be destructive, as PUT ?annotation once
// overwrote the object (#2286). TestOperations_coverTheModel fails on any
// modeled operation missing from subResources.
var plain = map[route]string{
	{levelService, http.MethodGet}:   "ListBuckets",
	{levelBucket, http.MethodGet}:    "ListObjects",
	{levelBucket, http.MethodPut}:    "CreateBucket",
	{levelBucket, http.MethodHead}:   "HeadBucket",
	{levelBucket, http.MethodDelete}: "DeleteBucket",
	{levelObject, http.MethodGet}:    "GetObject",
	{levelObject, http.MethodPut}:    "PutObject",
	{levelObject, http.MethodHead}:   "HeadObject",
	{levelObject, http.MethodDelete}: "DeleteObject",
}

// copies names the operation a PUT to an object is instead when it carries
// an x-amz-copy-source header.
var copies = map[string]string{
	"PutObject":  "CopyObject",
	"UploadPart": "UploadPartCopy",
}

// CopySourceHeader names the object a copy reads from.
const CopySourceHeader = "X-Amz-Copy-Source"

// MaxDeleteObjectsBody is how much of a DeleteObjects body S3 reads. S3
// accepts at most 1,000 keys of at most 1,024 bytes each, so a valid body fits
// well inside it. S3's handler and IAM enforcement both read exactly this
// prefix, so the keys enforcement authorises are the keys S3 deletes.
const MaxDeleteObjectsBody = 4 << 20

// parameters are the leading parameters in subResources that are query
// members rather than sub-resources. Each selects an operation on the methods
// that list it, and is an ordinary parameter on any other: GET
// /bucket/key?partNumber=1 is a GetObject.
var parameters = map[string]bool{
	"partNumber": true,
	"uploadId":   true,
}

// subResourcesAt lists, per level, the sub-resources S3 serves an operation
// on there, on any method.
var subResourcesAt = func() map[level]map[string]bool {
	at := map[level]map[string]bool{}
	for rt, subs := range subResources {
		if at[rt.level] == nil {
			at[rt.level] = map[string]bool{}
		}
		for _, sub := range subs {
			if param := sub.names()[0]; !parameters[param] {
				at[rt.level][param] = true
			}
		}
	}
	return at
}()

// Operation names the operation S3 serves r as, or "" when S3 serves it none.
func Operation(r *http.Request) string {
	operation, _ := resolve(r)
	return operation
}

// Refused reports whether S3 refuses r with MethodNotAllowed: r's query
// names a sub-resource S3 serves at r's level, but not on r's method. S3
// answers that 405 rather than serving the plain operation of the route,
// which for DELETE /bucket?versioning would be DeleteBucket. Operation names
// a refused request no operation.
func Refused(r *http.Request) bool {
	_, refused := resolve(r)
	return refused
}

// resolve names the operation S3 serves r as, or reports that S3 refuses it.
func resolve(r *http.Request) (operation string, refused bool) {
	if operation, ok := fixedPaths[binding{r.Method, r.URL.EscapedPath()}]; ok {
		return operation, false
	}
	rt := route{levelOf(r.URL.EscapedPath()), r.Method}
	operation, refused = selectOperation(rt, r.URL.Query())
	if copied, ok := copies[operation]; ok && rt.level == levelObject && r.Header.Get(CopySourceHeader) != "" {
		return copied, false
	}
	return operation, refused
}

// selectOperation is the operation rt serves for query, before a copy source
// is considered, or refused when query names a sub-resource that rt serves
// none for.
func selectOperation(rt route, query url.Values) (operation string, refused bool) {
	for _, sub := range subResources[rt] {
		if !sub.present(query) {
			continue
		}
		// list-type=2 is ListObjectsV2. Any other list-type is the original
		// ListObjects, which is what S3 serves a bucket GET without one.
		if sub.operation == "ListObjectsV2" && query.Get("list-type") != "2" {
			return "ListObjects", false
		}
		return sub.operation, false
	}
	for param := range query {
		if subResourcesAt[rt.level][param] {
			return "", true
		}
	}
	return plain[rt], false
}

// Operations lists every operation Operation can name.
func Operations() []string {
	var operations []string
	for _, subs := range subResources {
		for _, sub := range subs {
			operations = append(operations, sub.operation)
		}
	}
	for _, operation := range plain {
		operations = append(operations, operation)
	}
	for _, operation := range copies {
		operations = append(operations, operation)
	}
	for _, operation := range fixedPaths {
		operations = append(operations, operation)
	}
	slices.Sort(operations)
	return slices.Compact(operations)
}
