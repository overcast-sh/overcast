package s3route

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/overcast-sh/overcast/internal/awsapi"
	"github.com/overcast-sh/overcast/internal/awsshapes"
)

// operationCase is one request and the operation S3 serves it as.
type operationCase struct {
	method, target, copySource, want string
}

// assertOperation fails t unless S3 serves tc's request as tc.want.
func assertOperation(t *testing.T, tc operationCase) {
	t.Helper()
	// Given: a path-style request
	r := httptest.NewRequest(tc.method, tc.target, nil)
	if tc.copySource != "" {
		r.Header.Set(CopySourceHeader, tc.copySource)
	}

	// When: it is named
	got := Operation(r)

	// Then: it is the operation S3 serves
	if got != tc.want {
		t.Fatalf("Operation(%s %s) = %q, want %q", tc.method, tc.target, got, tc.want)
	}
}

func TestOperation(t *testing.T) {
	for _, tc := range []operationCase{
		// The service root.
		{http.MethodGet, "/", "", "ListBuckets"},
		{http.MethodGet, "/?directory-buckets", "", "ListDirectoryBuckets"},
		{http.MethodPost, "/?x-id=ListBuckets", "", ""},

		// A bucket, with and without the trailing slash the SDKs send.
		{http.MethodPut, "/b", "", "CreateBucket"},
		{http.MethodPut, "/b/", "", "CreateBucket"},
		{http.MethodHead, "/b", "", "HeadBucket"},
		{http.MethodDelete, "/b", "", "DeleteBucket"},
		{http.MethodGet, "/b", "", "ListObjects"},
		{http.MethodGet, "/b?prefix=logs%2F", "", "ListObjects"},
		{http.MethodGet, "/b?list-type=2", "", "ListObjectsV2"},
		{http.MethodGet, "/b?list-type=1", "", "ListObjects"},
		{http.MethodPut, "/b/?policy=", "", "PutBucketPolicy"},
		{http.MethodPost, "/b?delete&x-id=DeleteObjects", "", "DeleteObjects"},
		{http.MethodPost, "/b", "", ""},
		// The first sub-resource in S3's order wins.
		{http.MethodGet, "/b?tagging&acl", "", "GetBucketAcl"},
		// A sub-resource S3 does not know is ignored, as S3 ignores it.
		{http.MethodPut, "/b?metadata", "", "CreateBucket"},

		// An object, whose key may hold slashes, or begin with one.
		{http.MethodGet, "/b/a/b/c.txt", "", "GetObject"},
		{http.MethodGet, "/b//k", "", "GetObject"},
		{http.MethodPut, "/b/k", "/src/k", "CopyObject"},
		{http.MethodPut, "/b/k?partNumber=1&uploadId=u", "/src/k", "UploadPartCopy"},
		{http.MethodPut, "/b/k?tagging", "/src/k", "PutObjectTagging"},
		{http.MethodPut, "/b/k?annotation", "/src/k", "PutObjectAnnotation"},
		{http.MethodGet, "/b/k?partNumber=1", "", "GetObject"},
		{http.MethodPost, "/b/k?delete", "", ""},
		{http.MethodPost, "/b/WriteGetObjectResponse", "", ""},

		// x-id names nothing S3 serves.
		{http.MethodPut, "/b/k?x-id=GetObject", "", "PutObject"},
		{http.MethodGet, "/b/k?x-id=DeleteObject", "", "GetObject"},
		{http.MethodPut, "/b?x-id=PutBucketPolicy", "", "CreateBucket"},
		{http.MethodGet, "/b?analytics&x-id=GetBucketAnalyticsConfiguration", "", "ListBucketAnalyticsConfigurations"},
	} {
		t.Run(tc.method+" "+tc.target, func(t *testing.T) { assertOperation(t, tc) })
	}
}

// modeledCases holds, for every operation the pinned model binds, the request
// an SDK sends for it and so the operation S3 must serve it as.
var modeledCases = []operationCase{
	// The service, and the one operation on a fixed path.
	{http.MethodGet, "/?x-id=ListBuckets", "", "ListBuckets"},
	{http.MethodGet, "/?directory-buckets", "", "ListDirectoryBuckets"},
	{http.MethodPost, "/WriteGetObjectResponse", "", "WriteGetObjectResponse"},

	// A bucket.
	{http.MethodPut, "/b", "", "CreateBucket"},
	{http.MethodHead, "/b", "", "HeadBucket"},
	{http.MethodDelete, "/b", "", "DeleteBucket"},
	{http.MethodGet, "/b", "", "ListObjects"},
	{http.MethodGet, "/b?list-type=2", "", "ListObjectsV2"},
	{http.MethodGet, "/b?versions", "", "ListObjectVersions"},
	{http.MethodGet, "/b?uploads", "", "ListMultipartUploads"},
	{http.MethodGet, "/b?location", "", "GetBucketLocation"},
	{http.MethodGet, "/b?session", "", "CreateSession"},
	{http.MethodPost, "/b?delete", "", "DeleteObjects"},
	{http.MethodGet, "/b?policyStatus", "", "GetBucketPolicyStatus"},
	{http.MethodGet, "/b?abac", "", "GetBucketAbac"},
	{http.MethodPut, "/b?abac", "", "PutBucketAbac"},
	{http.MethodGet, "/b?accelerate", "", "GetBucketAccelerateConfiguration"},
	{http.MethodPut, "/b?accelerate", "", "PutBucketAccelerateConfiguration"},
	{http.MethodGet, "/b?acl", "", "GetBucketAcl"},
	{http.MethodPut, "/b?acl", "", "PutBucketAcl"},
	{http.MethodGet, "/b?analytics&id=a&x-id=GetBucketAnalyticsConfiguration", "", "GetBucketAnalyticsConfiguration"},
	{http.MethodGet, "/b?analytics&x-id=ListBucketAnalyticsConfigurations", "", "ListBucketAnalyticsConfigurations"},
	{http.MethodPut, "/b?analytics&id=a", "", "PutBucketAnalyticsConfiguration"},
	{http.MethodDelete, "/b?analytics&id=a", "", "DeleteBucketAnalyticsConfiguration"},
	{http.MethodGet, "/b?cors", "", "GetBucketCors"},
	{http.MethodPut, "/b?cors", "", "PutBucketCors"},
	{http.MethodDelete, "/b?cors", "", "DeleteBucketCors"},
	{http.MethodGet, "/b?encryption", "", "GetBucketEncryption"},
	{http.MethodPut, "/b?encryption", "", "PutBucketEncryption"},
	{http.MethodDelete, "/b?encryption", "", "DeleteBucketEncryption"},
	{http.MethodGet, "/b?intelligent-tiering&id=a", "", "GetBucketIntelligentTieringConfiguration"},
	{http.MethodGet, "/b?intelligent-tiering", "", "ListBucketIntelligentTieringConfigurations"},
	{http.MethodPut, "/b?intelligent-tiering&id=a", "", "PutBucketIntelligentTieringConfiguration"},
	{http.MethodDelete, "/b?intelligent-tiering&id=a", "", "DeleteBucketIntelligentTieringConfiguration"},
	{http.MethodGet, "/b?inventory&id=a", "", "GetBucketInventoryConfiguration"},
	{http.MethodGet, "/b?inventory", "", "ListBucketInventoryConfigurations"},
	{http.MethodPut, "/b?inventory&id=a", "", "PutBucketInventoryConfiguration"},
	{http.MethodDelete, "/b?inventory&id=a", "", "DeleteBucketInventoryConfiguration"},
	{http.MethodGet, "/b?lifecycle", "", "GetBucketLifecycleConfiguration"},
	{http.MethodPut, "/b?lifecycle", "", "PutBucketLifecycleConfiguration"},
	{http.MethodDelete, "/b?lifecycle", "", "DeleteBucketLifecycle"},
	{http.MethodGet, "/b?logging", "", "GetBucketLogging"},
	{http.MethodPut, "/b?logging", "", "PutBucketLogging"},
	{http.MethodGet, "/b?metadataConfiguration", "", "GetBucketMetadataConfiguration"},
	{http.MethodPost, "/b?metadataConfiguration", "", "CreateBucketMetadataConfiguration"},
	{http.MethodDelete, "/b?metadataConfiguration", "", "DeleteBucketMetadataConfiguration"},
	{http.MethodGet, "/b?metadataTable", "", "GetBucketMetadataTableConfiguration"},
	{http.MethodPost, "/b?metadataTable", "", "CreateBucketMetadataTableConfiguration"},
	{http.MethodDelete, "/b?metadataTable", "", "DeleteBucketMetadataTableConfiguration"},
	{http.MethodPut, "/b?metadataInventoryTable", "", "UpdateBucketMetadataInventoryTableConfiguration"},
	{http.MethodPut, "/b?metadataJournalTable", "", "UpdateBucketMetadataJournalTableConfiguration"},
	{http.MethodPut, "/b?metadataAnnotationTable", "", "UpdateBucketMetadataAnnotationTableConfiguration"},
	{http.MethodGet, "/b?metrics&id=a", "", "GetBucketMetricsConfiguration"},
	{http.MethodGet, "/b?metrics", "", "ListBucketMetricsConfigurations"},
	{http.MethodPut, "/b?metrics&id=a", "", "PutBucketMetricsConfiguration"},
	{http.MethodDelete, "/b?metrics&id=a", "", "DeleteBucketMetricsConfiguration"},
	{http.MethodGet, "/b?notification", "", "GetBucketNotificationConfiguration"},
	{http.MethodPut, "/b?notification", "", "PutBucketNotificationConfiguration"},
	{http.MethodGet, "/b?object-lock", "", "GetObjectLockConfiguration"},
	{http.MethodPut, "/b?object-lock", "", "PutObjectLockConfiguration"},
	{http.MethodGet, "/b?ownershipControls", "", "GetBucketOwnershipControls"},
	{http.MethodPut, "/b?ownershipControls", "", "PutBucketOwnershipControls"},
	{http.MethodDelete, "/b?ownershipControls", "", "DeleteBucketOwnershipControls"},
	{http.MethodGet, "/b?policy", "", "GetBucketPolicy"},
	{http.MethodPut, "/b?policy", "", "PutBucketPolicy"},
	{http.MethodDelete, "/b?policy", "", "DeleteBucketPolicy"},
	{http.MethodGet, "/b?publicAccessBlock", "", "GetPublicAccessBlock"},
	{http.MethodPut, "/b?publicAccessBlock", "", "PutPublicAccessBlock"},
	{http.MethodDelete, "/b?publicAccessBlock", "", "DeletePublicAccessBlock"},
	{http.MethodGet, "/b?replication", "", "GetBucketReplication"},
	{http.MethodPut, "/b?replication", "", "PutBucketReplication"},
	{http.MethodDelete, "/b?replication", "", "DeleteBucketReplication"},
	{http.MethodGet, "/b?requestPayment", "", "GetBucketRequestPayment"},
	{http.MethodPut, "/b?requestPayment", "", "PutBucketRequestPayment"},
	{http.MethodGet, "/b?tagging", "", "GetBucketTagging"},
	{http.MethodPut, "/b?tagging", "", "PutBucketTagging"},
	{http.MethodDelete, "/b?tagging", "", "DeleteBucketTagging"},
	{http.MethodGet, "/b?versioning", "", "GetBucketVersioning"},
	{http.MethodPut, "/b?versioning", "", "PutBucketVersioning"},
	{http.MethodGet, "/b?website", "", "GetBucketWebsite"},
	{http.MethodPut, "/b?website", "", "PutBucketWebsite"},
	{http.MethodDelete, "/b?website", "", "DeleteBucketWebsite"},

	// An object.
	{http.MethodGet, "/b/k?x-id=GetObject", "", "GetObject"},
	{http.MethodHead, "/b/k", "", "HeadObject"},
	{http.MethodPut, "/b/k?x-id=PutObject", "", "PutObject"},
	{http.MethodPut, "/b/k?x-id=CopyObject", "/src/k", "CopyObject"},
	{http.MethodDelete, "/b/k?x-id=DeleteObject", "", "DeleteObject"},
	{http.MethodPost, "/b/k?uploads", "", "CreateMultipartUpload"},
	{http.MethodPut, "/b/k?partNumber=1&uploadId=u&x-id=UploadPart", "", "UploadPart"},
	{http.MethodPut, "/b/k?partNumber=1&uploadId=u&x-id=UploadPartCopy", "/src/k", "UploadPartCopy"},
	{http.MethodGet, "/b/k?uploadId=u&x-id=ListParts", "", "ListParts"},
	{http.MethodPost, "/b/k?uploadId=u", "", "CompleteMultipartUpload"},
	{http.MethodDelete, "/b/k?uploadId=u&x-id=AbortMultipartUpload", "", "AbortMultipartUpload"},
	{http.MethodGet, "/b/k?acl", "", "GetObjectAcl"},
	{http.MethodPut, "/b/k?acl", "", "PutObjectAcl"},
	{http.MethodGet, "/b/k?annotation&annotationName=n&x-id=GetObjectAnnotation", "", "GetObjectAnnotation"},
	{http.MethodGet, "/b/k?annotation&x-id=ListObjectAnnotations", "", "ListObjectAnnotations"},
	{http.MethodPut, "/b/k?annotation", "", "PutObjectAnnotation"},
	{http.MethodDelete, "/b/k?annotation&annotationName=n", "", "DeleteObjectAnnotation"},
	{http.MethodGet, "/b/k?attributes", "", "GetObjectAttributes"},
	{http.MethodPut, "/b/k?encryption", "", "UpdateObjectEncryption"},
	{http.MethodGet, "/b/k?legal-hold", "", "GetObjectLegalHold"},
	{http.MethodPut, "/b/k?legal-hold", "", "PutObjectLegalHold"},
	{http.MethodPut, "/b/k?renameObject", "", "RenameObject"},
	{http.MethodPost, "/b/k?restore", "", "RestoreObject"},
	{http.MethodGet, "/b/k?retention", "", "GetObjectRetention"},
	{http.MethodPut, "/b/k?retention", "", "PutObjectRetention"},
	{http.MethodPost, "/b/k?select&select-type=2", "", "SelectObjectContent"},
	{http.MethodGet, "/b/k?tagging", "", "GetObjectTagging"},
	{http.MethodPut, "/b/k?tagging", "", "PutObjectTagging"},
	{http.MethodDelete, "/b/k?tagging", "", "DeleteObjectTagging"},
	{http.MethodGet, "/b/k?torrent", "", "GetObjectTorrent"},
}

func TestOperation_servesEveryModeledOperation(t *testing.T) {
	for _, tc := range modeledCases {
		t.Run(tc.want, func(t *testing.T) { assertOperation(t, tc) })
	}
}

func TestModeledCases_coverTheModel(t *testing.T) {
	// Given: the operations the table above holds a request for
	covered := make(map[string]bool, len(modeledCases))
	for _, tc := range modeledCases {
		covered[tc.want] = true
	}

	// When/Then: every operation the model binds is one of them
	awsapi.WalkOperations(func(op awsapi.Operation) bool {
		if op.Service == "s3" && !covered[op.Name] {
			t.Errorf("modeledCases has no request for %s (%s %s)", op.Name, op.HTTPMethod, op.URI)
		}
		return true
	})
}

// modelLevel is the level a modeled S3 URI's path addresses.
func modelLevel(uri string) level {
	path, _, _ := strings.Cut(uri, "?")
	switch path {
	case "/":
		return levelService
	case "/{Bucket}":
		return levelBucket
	}
	return levelObject
}

// modelLiterals are a modeled S3 URI's query literal names, without x-id.
func modelLiterals(uri string) []string {
	_, query, _ := strings.Cut(uri, "?")
	var names []string
	for _, literal := range strings.Split(query, "&") {
		name, _, _ := strings.Cut(literal, "=")
		if name != "" && name != "x-id" {
			names = append(names, name)
		}
	}
	return names
}

// modeledS3 is the model's binding for an S3 operation.
func modeledS3(operation string) (awsapi.Operation, bool) {
	ops := awsapi.Operations("s3", operation)
	if len(ops) == 0 {
		return awsapi.Operation{}, false
	}
	return ops[0], true
}

// queryMembers are the query parameters an S3 operation's modeled input
// binds a member to.
func queryMembers(t *testing.T, operation string) []string {
	t.Helper()
	svc, ok, err := awsshapes.Lookup("s3")
	if err != nil || !ok {
		t.Fatalf("awsshapes.Lookup(s3) = %v, %v", ok, err)
	}
	shape, ok := svc.Operation(operation)
	if !ok {
		t.Fatalf("%s has no modeled shape", operation)
	}
	var names []string
	for _, member := range shape.Input.Members {
		if member.Query != "" {
			names = append(names, member.Query)
		}
	}
	return names
}

// divergences are the operations S3 selects by a parameter the model
// neither makes a query literal nor binds a member to, and why.
var divergences = map[string]string{
	"ListDirectoryBuckets": "selected by directory-buckets: AWS serves it on the s3express-control host, which Overcast does not route, and the model has only x-id",
}

func TestOperations_matchTheModel(t *testing.T) {
	check := func(t *testing.T, rt route, params, operation string) {
		t.Helper()
		if _, diverges := divergences[operation]; diverges {
			return
		}
		op, ok := modeledS3(operation)
		if !ok {
			t.Errorf("%s is not a modeled S3 operation", operation)
			return
		}
		if op.HTTPMethod != rt.method || modelLevel(op.URI) != rt.level {
			t.Errorf("%s is served on %s at level %d; the model binds %s %s", operation, rt.method, rt.level, op.HTTPMethod, op.URI)
			return
		}
		checkParams(t, operation, op.URI, params)
		// Refused treats a leading parameter as a sub-resource exactly
		// when the model makes it a query literal.
		if lead, _, _ := strings.Cut(params, "&"); lead != "" && parameters[lead] == slices.Contains(modelLiterals(op.URI), lead) {
			t.Errorf("%s: parameters[%q] = %v, but the model binds %s", operation, lead, parameters[lead], op.URI)
		}
	}
	for rt, subs := range subResources {
		for _, sub := range subs {
			check(t, rt, sub.params, sub.operation)
		}
	}
	for rt, operation := range plain {
		check(t, rt, "", operation)
	}
	for b, operation := range fixedPaths {
		op, ok := modeledS3(operation)
		if !ok || op.HTTPMethod != b.method || op.URI != b.path {
			t.Errorf("%s is served on %s %s; the model binds %s %s", operation, b.method, b.path, op.HTTPMethod, op.URI)
		}
	}
}

// checkParams fails t unless the parameters S3 selects operation by are
// ones its model binds on uri: a query literal, which one of them must be
// when the model has any, or a member of its input.
func checkParams(t *testing.T, operation, uri, params string) {
	t.Helper()
	literals := modelLiterals(uri)
	names := strings.Split(params, "&")
	if params == "" {
		names = nil
	}
	if len(literals) > 0 && !slices.ContainsFunc(names, func(name string) bool { return slices.Contains(literals, name) }) {
		t.Errorf("%s is served on %q; the model binds %s", operation, params, uri)
	}
	members := queryMembers(t, operation)
	for _, name := range names {
		if !slices.Contains(literals, name) && !slices.Contains(members, name) {
			t.Errorf("%s is selected by %s, which the model binds on neither %s nor its input", operation, name, uri)
		}
	}
}

func TestOperations_coverTheModel(t *testing.T) {
	// Given: the operations S3 routes
	routed := Operations()

	// When/Then: every operation the model binds is one of them, so none is
	// served as the plain operation of its route
	awsapi.WalkOperations(func(op awsapi.Operation) bool {
		if op.Service == "s3" && !slices.Contains(routed, op.Name) {
			t.Errorf("%s (%s %s) is modeled but not routed", op.Name, op.HTTPMethod, op.URI)
		}
		return true
	})
}

func TestSubResources_noneIsShadowed(t *testing.T) {
	// Given: each route's sub-resources, in the order S3 tries them
	for rt, subs := range subResources {
		for i, earlier := range subs {
			for _, later := range subs[i+1:] {
				// Then: none is tried after one whose parameters are a
				// subset of its own, which would always select first
				if !slices.ContainsFunc(earlier.names(), func(name string) bool { return !slices.Contains(later.names(), name) }) {
					t.Errorf("%s %d: %q selects before %q ever can", rt.method, rt.level, earlier.params, later.params)
				}
			}
		}
	}
}
