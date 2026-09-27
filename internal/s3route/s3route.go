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

// subResource is a query parameter that selects an operation.
type subResource struct {
	param     string
	operation string
}

// subResources lists, per route, the sub-resources that select an operation,
// in the order S3 tries them: the first one present wins.
var subResources = map[route][]subResource{
	{levelService, http.MethodGet}: {
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
		{"analytics", "ListBucketAnalyticsConfigurations"},
		{"intelligent-tiering", "ListBucketIntelligentTieringConfigurations"},
		{"inventory", "ListBucketInventoryConfigurations"},
		{"metrics", "ListBucketMetricsConfigurations"},
		{"object-lock", "GetObjectLockConfiguration"},
		{"abac", "GetBucketAbac"},
		{"metadata", "GetBucketMetadataConfiguration"},
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
		{"metadata", "CreateBucketMetadataConfiguration"},
		{"metadataTable", "UpdateBucketMetadataTableConfiguration"},
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
		{"metadata", "DeleteBucketMetadataConfiguration"},
		{"metadataTable", "DeleteBucketMetadataTableConfiguration"},
	},
	{levelBucket, http.MethodPost}: {
		{"delete", "DeleteObjects"},
		{"metadataTable", "CreateBucketMetadataTableConfiguration"},
	},
	{levelObject, http.MethodGet}: {
		{"acl", "GetObjectAcl"},
		{"tagging", "GetObjectTagging"},
		{"attributes", "GetObjectAttributes"},
		{"legal-hold", "GetObjectLegalHold"},
		{"retention", "GetObjectRetention"},
		{"torrent", "GetObjectTorrent"},
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
		{"rename", "RenameObject"},
		{"encryption", "UpdateObjectEncryption"},
	},
	{levelObject, http.MethodDelete}: {
		{"tagging", "DeleteObjectTagging"},
		{"uploadId", "AbortMultipartUpload"},
	},
	{levelObject, http.MethodPost}: {
		{"uploads", "CreateMultipartUpload"},
		{"uploadId", "CompleteMultipartUpload"},
		{"restore", "RestoreObject"},
		{"select", "SelectObjectContent"},
		{"writeGetObjectResponse", "WriteGetObjectResponse"},
	},
}

// plain names the operation a route serves when no sub-resource selects one.
// A route absent here serves none: S3 answers a POST to a bucket or an object
// without a sub-resource with an error, not an operation.
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

// Operation names the operation S3 serves r as, or "" when S3 serves it none.
func Operation(r *http.Request) string {
	rt := route{levelOf(r.URL.EscapedPath()), r.Method}
	query := r.URL.Query()
	operation := selectOperation(rt, query)
	if copied, ok := copies[operation]; ok && rt.level == levelObject && r.Header.Get(CopySourceHeader) != "" {
		return copied
	}
	return operation
}

// selectOperation is the operation rt serves for query, before a copy source
// is considered.
func selectOperation(rt route, query url.Values) string {
	for _, sub := range subResources[rt] {
		if !query.Has(sub.param) {
			continue
		}
		// list-type=2 is ListObjectsV2. Any other list-type is the original
		// ListObjects, which is what S3 serves a bucket GET without one.
		if sub.operation == "ListObjectsV2" && query.Get(sub.param) != "2" {
			return "ListObjects"
		}
		return sub.operation
	}
	return plain[rt]
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
	slices.Sort(operations)
	return slices.Compact(operations)
}
