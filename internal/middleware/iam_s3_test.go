package middleware

import (
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
)

// The tests in this file pin #2284: an S3 request is authorised as the
// operation S3 serves it as, under the actions AWS checks for that operation.

func TestS3IAMChecks(t *testing.T) {
	const bucket, object = "arn:aws:s3:::b", "arn:aws:s3:::b/k"
	for _, tc := range []struct {
		name, method, target, copySource, body string
		want                                   []iamCheck
	}{
		// Sub-resources are their own operations, not the plain one.
		{"bucket policy", http.MethodPut, "/b?policy", "", "", []iamCheck{{"s3:PutBucketPolicy", bucket}}},
		{"bucket tagging", http.MethodPut, "/b/?tagging", "", "", []iamCheck{{"s3:PutBucketTagging", bucket}}},
		{"bucket ACL", http.MethodPut, "/b?acl", "", "", []iamCheck{{"s3:PutBucketAcl", bucket}}},
		{"bucket versioning", http.MethodPut, "/b?versioning", "", "", []iamCheck{{"s3:PutBucketVersioning", bucket}}},
		{"bucket CORS removal", http.MethodDelete, "/b?cors", "", "", []iamCheck{{"s3:PutBucketCORS", bucket}}},
		{"bucket tagging removal", http.MethodDelete, "/b?tagging", "", "", []iamCheck{{"s3:PutBucketTagging", bucket}}},
		{"lifecycle", http.MethodGet, "/b?lifecycle", "", "", []iamCheck{{"s3:GetLifecycleConfiguration", bucket}}},
		{"notification", http.MethodPut, "/b?notification", "", "", []iamCheck{{"s3:PutBucketNotification", bucket}}},
		{"object lock", http.MethodPut, "/b?object-lock", "", "", []iamCheck{{"s3:PutBucketObjectLockConfiguration", bucket}}},
		{"metadata table", http.MethodPost, "/b?metadataTable", "", "", []iamCheck{{"s3:CreateBucketMetadataTableConfiguration", bucket}}},
		{"create bucket", http.MethodPut, "/b", "", "", []iamCheck{{"s3:CreateBucket", bucket}}},

		// Operations AWS authorises by another action.
		{"list buckets", http.MethodGet, "/", "", "", []iamCheck{{"s3:ListAllMyBuckets", "*"}}},
		{"head bucket", http.MethodHead, "/b", "", "", []iamCheck{{"s3:ListBucket", bucket}}},
		{"list objects", http.MethodGet, "/b", "", "", []iamCheck{{"s3:ListBucket", bucket}}},
		{"list objects v2", http.MethodGet, "/b?list-type=2", "", "", []iamCheck{{"s3:ListBucket", bucket}}},
		{"list versions", http.MethodGet, "/b?versions", "", "", []iamCheck{{"s3:ListBucketVersions", bucket}}},
		{"list uploads", http.MethodGet, "/b?uploads", "", "", []iamCheck{{"s3:ListBucketMultipartUploads", bucket}}},
		{"head object", http.MethodHead, "/b/k", "", "", []iamCheck{{"s3:GetObject", object}}},
		{"object attributes", http.MethodGet, "/b/k?attributes", "", "", []iamCheck{{"s3:GetObject", object}}},
		{"create upload", http.MethodPost, "/b/k?uploads", "", "", []iamCheck{{"s3:PutObject", object}}},
		{"upload part", http.MethodPut, "/b/k?partNumber=1&uploadId=u", "", "", []iamCheck{{"s3:PutObject", object}}},
		{"complete upload", http.MethodPost, "/b/k?uploadId=u", "", "", []iamCheck{{"s3:PutObject", object}}},
		{"list parts", http.MethodGet, "/b/k?uploadId=u", "", "", []iamCheck{{"s3:ListMultipartUploadParts", object}}},
		{"abort upload", http.MethodDelete, "/b/k?uploadId=u", "", "", []iamCheck{{"s3:AbortMultipartUpload", object}}},

		// A versionId selects the versioned action.
		{"get version", http.MethodGet, "/b/k?versionId=v", "", "", []iamCheck{{"s3:GetObjectVersion", object}}},
		{"delete version", http.MethodDelete, "/b/k?versionId=v", "", "", []iamCheck{{"s3:DeleteObjectVersion", object}}},
		{"version tagging", http.MethodPut, "/b/k?tagging&versionId=v", "", "", []iamCheck{{"s3:PutObjectVersionTagging", object}}},
		{"head version", http.MethodHead, "/b/k?versionId=v", "", "", []iamCheck{{"s3:GetObjectVersion", object}}},

		// A copy reads its source too.
		{"copy", http.MethodPut, "/b/k", "src/a%20b", "", []iamCheck{{"s3:PutObject", object}, {"s3:GetObject", "arn:aws:s3:::src/a b"}}},
		{"copy of a version", http.MethodPut, "/b/k", "/src/a?versionId=v", "", []iamCheck{{"s3:PutObject", object}, {"s3:GetObjectVersion", "arn:aws:s3:::src/a"}}},
		{"part copy", http.MethodPut, "/b/k?partNumber=1&uploadId=u", "src/a", "", []iamCheck{{"s3:PutObject", object}, {"s3:GetObject", "arn:aws:s3:::src/a"}}},

		// DeleteObjects deletes each key it names.
		{"delete objects", http.MethodPost, "/b?delete", "", `<Delete><Object><Key>x</Key></Object><Object><Key>y/z</Key><VersionId>v</VersionId></Object></Delete>`,
			[]iamCheck{{"s3:DeleteObject", "arn:aws:s3:::b/x"}, {"s3:DeleteObjectVersion", "arn:aws:s3:::b/y/z"}}},
		{"delete objects, body unreadable", http.MethodPost, "/b?delete", "", `not xml`, []iamCheck{{"s3:DeleteObject", bucket}}},
		{"delete objects, key repeated", http.MethodPost, "/b?delete", "", `<Delete><Object><Key>x</Key></Object><Object><Key>x</Key></Object></Delete>`, []iamCheck{{"s3:DeleteObject", "arn:aws:s3:::b/x"}}},

		// x-id names nothing S3 serves.
		{"x-id on a put", http.MethodPut, "/b/k?x-id=GetObject", "", "", []iamCheck{{"s3:PutObject", object}}},
		{"x-id on a bucket put", http.MethodPut, "/b?x-id=PutBucketPolicy", "", "", []iamCheck{{"s3:CreateBucket", bucket}}},
		{"x-id on delete objects", http.MethodPost, "/b?delete&x-id=DeleteObjects", "", `<Delete><Object><Key>x</Key></Object></Delete>`, []iamCheck{{"s3:DeleteObject", "arn:aws:s3:::b/x"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Given: an S3 request
			r := signedRequest(tc.method, tc.target, "s3")
			if tc.body != "" {
				r.Body = io.NopCloser(strings.NewReader(tc.body))
			}
			if tc.copySource != "" {
				r.Header.Set("X-Amz-Copy-Source", tc.copySource)
			}

			// When: enforcement names what it must be allowed
			op := classifyIAM(r)
			got := requestIAMChecks(r, op)

			// Then: it is what AWS checks for the operation S3 serves
			if op.action == "" || !slices.Equal(got, tc.want) {
				t.Fatalf("%s %s: action %q, checks %v; want %v", tc.method, tc.target, op.action, got, tc.want)
			}
		})
	}
}

func TestS3IAMChecks_deleteObjectsBodyStillServed(t *testing.T) {
	// Given: a DeleteObjects request
	const body = `<Delete><Object><Key>x</Key></Object></Delete>`
	r := httptest.NewRequest(http.MethodPost, "/b?delete", strings.NewReader(body))

	// When: enforcement reads the keys it deletes
	requestIAMChecks(r, iamOperation{service: "s3", action: "s3:DeleteObject"})

	// Then: the handler still reads the whole body
	if got, err := io.ReadAll(r.Body); err != nil || string(got) != body {
		t.Fatalf("body after checks = %q, %v; want %q", got, err, body)
	}
}

func TestIAMAction_isNamedAfterTheOperationUnlessTheTableSaysOtherwise(t *testing.T) {
	for _, tc := range []struct{ svc, op, want string }{
		{"s3", "PutBucketPolicy", "s3:PutBucketPolicy"},
		{"s3", "ListObjectsV2", "s3:ListBucket"},
		{"s3", "ListDirectoryBuckets", "s3express:ListAllMyDirectoryBuckets"},
		{"lambda", "Invoke", "lambda:InvokeFunction"},
		{"lambda", "GetFunction", "lambda:GetFunction"},
		{"stepfunctions", "StartExecution", "states:StartExecution"},
		{"s3", "", ""},
		{"internal", "Anything", ""},
	} {
		t.Run(tc.svc+" "+tc.op, func(t *testing.T) {
			// Given: an operation of a service
			// When: its IAM action is named
			got := iamAction(tc.svc, tc.op)

			// Then: it is the action AWS documents for it
			if got != tc.want {
				t.Fatalf("iamAction(%q, %q) = %q, want %q", tc.svc, tc.op, got, tc.want)
			}
		})
	}
}
