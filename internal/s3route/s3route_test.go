package s3route

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/overcast-sh/overcast/internal/awsapi"
)

func TestOperation(t *testing.T) {
	for _, tc := range []struct {
		method, target, copySource, want string
	}{
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
		{http.MethodGet, "/b?location", "", "GetBucketLocation"},
		{http.MethodPut, "/b?policy", "", "PutBucketPolicy"},
		{http.MethodPut, "/b/?policy=", "", "PutBucketPolicy"},
		{http.MethodGet, "/b?policy", "", "GetBucketPolicy"},
		{http.MethodDelete, "/b?policy", "", "DeleteBucketPolicy"},
		{http.MethodPut, "/b?tagging", "", "PutBucketTagging"},
		{http.MethodPut, "/b?acl", "", "PutBucketAcl"},
		{http.MethodPut, "/b?versioning", "", "PutBucketVersioning"},
		{http.MethodDelete, "/b?cors", "", "DeleteBucketCors"},
		{http.MethodPost, "/b?delete", "", "DeleteObjects"},
		{http.MethodPost, "/b?delete&x-id=DeleteObjects", "", "DeleteObjects"},
		{http.MethodPost, "/b?metadataTable", "", "CreateBucketMetadataTableConfiguration"},
		{http.MethodPost, "/b", "", ""},
		// The first sub-resource in S3's order wins.
		{http.MethodGet, "/b?tagging&acl", "", "GetBucketAcl"},

		// An object, whose key may hold slashes, or begin with one.
		{http.MethodGet, "/b/k", "", "GetObject"},
		{http.MethodGet, "/b/a/b/c.txt", "", "GetObject"},
		{http.MethodGet, "/b//k", "", "GetObject"},
		{http.MethodHead, "/b/k", "", "HeadObject"},
		{http.MethodPut, "/b/k", "", "PutObject"},
		{http.MethodDelete, "/b/k", "", "DeleteObject"},
		{http.MethodPut, "/b/k", "/src/k", "CopyObject"},
		{http.MethodPut, "/b/k?partNumber=1&uploadId=u", "", "UploadPart"},
		{http.MethodPut, "/b/k?partNumber=1&uploadId=u", "/src/k", "UploadPartCopy"},
		{http.MethodPut, "/b/k?tagging", "/src/k", "PutObjectTagging"},
		{http.MethodGet, "/b/k?uploadId=u", "", "ListParts"},
		{http.MethodDelete, "/b/k?uploadId=u", "", "AbortMultipartUpload"},
		{http.MethodPost, "/b/k?uploads", "", "CreateMultipartUpload"},
		{http.MethodPost, "/b/k?uploadId=u", "", "CompleteMultipartUpload"},
		{http.MethodGet, "/b/k?attributes", "", "GetObjectAttributes"},
		{http.MethodPost, "/b/k?delete", "", ""},

		// x-id names nothing S3 serves.
		{http.MethodPut, "/b/k?x-id=GetObject", "", "PutObject"},
		{http.MethodGet, "/b/k?x-id=DeleteObject", "", "GetObject"},
		{http.MethodPut, "/b?x-id=PutBucketPolicy", "", "CreateBucket"},
	} {
		t.Run(tc.method+" "+tc.target, func(t *testing.T) {
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
		})
	}
}

// divergences are the operations S3 selects by something other than their
// modeled method, URI shape and query literal, and why.
var divergences = map[string]string{
	// The model tells these apart from their neighbours only by x-id.
	"UploadPart":              "selected by partNumber, a query member the model does not make a literal",
	"UploadPartCopy":          "an UploadPart with a copy source",
	"ListParts":               "selected by uploadId, a query member the model does not make a literal",
	"AbortMultipartUpload":    "selected by uploadId, a query member the model does not make a literal",
	"CompleteMultipartUpload": "selected by uploadId, a query member the model does not make a literal",
	"ListDirectoryBuckets":    "selected by directory-buckets; the model has only x-id",
	// Overcast's routing differs from the model's binding (follow-up to #2284).
	"GetBucketMetadataConfiguration":         "served on ?metadata; modeled on ?metadataConfiguration",
	"CreateBucketMetadataConfiguration":      "served on PUT ?metadata; modeled on POST ?metadataConfiguration",
	"DeleteBucketMetadataConfiguration":      "served on ?metadata; modeled on ?metadataConfiguration",
	"UpdateBucketMetadataTableConfiguration": "not a modeled operation",
	"RenameObject":                           "served on ?rename; modeled on ?renameObject",
	"WriteGetObjectResponse":                 "served on an object; modeled on POST /WriteGetObjectResponse",
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
func modeledS3(t *testing.T, operation string) (awsapi.Operation, bool) {
	t.Helper()
	ops := awsapi.Operations("s3", operation)
	if len(ops) == 0 {
		return awsapi.Operation{}, false
	}
	return ops[0], true
}

func TestOperations_matchTheModel(t *testing.T) {
	check := func(t *testing.T, rt route, param, operation string) {
		t.Helper()
		if _, diverges := divergences[operation]; diverges {
			return
		}
		op, ok := modeledS3(t, operation)
		if !ok {
			t.Errorf("%s is not a modeled S3 operation", operation)
			return
		}
		literals := modelLiterals(op.URI)
		switch {
		case op.HTTPMethod != rt.method || modelLevel(op.URI) != rt.level:
			t.Errorf("%s is served on %s at level %d; the model binds %s %s", operation, rt.method, rt.level, op.HTTPMethod, op.URI)
		case param == "" && len(literals) > 0:
			t.Errorf("%s is served without a sub-resource; the model binds %s", operation, op.URI)
		case param != "" && !slices.Contains(literals, param):
			t.Errorf("%s is served on ?%s; the model binds %s", operation, param, op.URI)
		}
	}
	for rt, subs := range subResources {
		for _, sub := range subs {
			check(t, rt, sub.param, sub.operation)
		}
	}
	for rt, operation := range plain {
		check(t, rt, "", operation)
	}
}

// unrouted are the modeled S3 operations selected by a sub-resource that
// Overcast's S3 does not route (follow-up to #2284). S3 serves each as the
// plain operation of its route, except the four Get*Configuration operations
// the model tells from their List* counterparts by an id query member: S3
// serves those as the List* operation, which AWS authorises by the same
// action.
var unrouted = []string{
	"GetBucketAnalyticsConfiguration", "GetBucketIntelligentTieringConfiguration",
	"GetBucketInventoryConfiguration", "GetBucketMetricsConfiguration",
	"CreateBucketMetadataConfiguration", "DeleteBucketMetadataConfiguration", "GetBucketMetadataConfiguration",
	"DeleteObjectAnnotation", "GetObjectAnnotation", "ListObjectAnnotations", "PutObjectAnnotation",
	"RenameObject",
	"UpdateBucketMetadataAnnotationTableConfiguration", "UpdateBucketMetadataInventoryTableConfiguration",
	"UpdateBucketMetadataJournalTableConfiguration",
}

func TestOperations_coverTheModeledSubResources(t *testing.T) {
	// Given: the operations S3 routes
	routed := Operations()

	// When: every modeled S3 operation a sub-resource selects is looked up
	awsapi.WalkOperations(func(op awsapi.Operation) bool {
		if op.Service != "s3" || len(modelLiterals(op.URI)) == 0 || slices.Contains(unrouted, op.Name) {
			return true
		}

		// Then: S3 routes it
		if !slices.Contains(routed, op.Name) {
			t.Errorf("%s (%s %s) is modeled but not routed", op.Name, op.HTTPMethod, op.URI)
		}
		return true
	})
}
