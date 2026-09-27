package iam_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"

	"github.com/overcast-sh/overcast/tests/helpers"
)

// The tests in this file pin #2284: with IAM enforcement on, an S3 request is
// authorised as the operation S3 serves it as, decided by its method, path and
// sub-resource, under the IAM action AWS checks for that operation. The x-id
// query parameter the SDK adds names nothing.

// s3AdminPolicy allows every S3 action, for setting up and inspecting state.
const s3AdminPolicy = `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"s3:*","Resource":"*"}]}`

// s3Client is a path-style S3 client calling srv as accessKey.
func s3Client(srv *helpers.TestServer, accessKey string) *s3.Client {
	return s3.NewFromConfig(sdkConfigFor(srv, accessKey), func(o *s3.Options) { o.UsePathStyle = true })
}

// seedBucketWithObject creates bucket holding key, as an S3 administrator.
func seedBucketWithObject(t *testing.T, srv *helpers.TestServer, bucket, key string) *s3.Client {
	t.Helper()
	seedIAMPrincipal(t, srv, "s3-admin", s3AdminPolicy)
	admin := s3Client(srv, "s3-admin")
	ctx := context.Background()
	if _, err := admin.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(bucket)}); err != nil {
		t.Fatalf("CreateBucket: %v", err)
	}
	if _, err := admin.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(bucket), Key: aws.String(key), Body: strings.NewReader("v")}); err != nil {
		t.Fatalf("PutObject: %v", err)
	}
	return admin
}

// assertAccessDenied fails unless err is S3's AccessDenied naming action.
func assertAccessDenied(t *testing.T, err error, action string) {
	t.Helper()
	var apiErr smithy.APIError
	if !errors.As(err, &apiErr) || apiErr.ErrorCode() != "AccessDenied" || !strings.Contains(apiErr.ErrorMessage(), action) {
		t.Fatalf("err = %v, want AccessDenied for %s", err, action)
	}
}

func TestIAMEnforceS3Operations_deleteObjectsNeedsDeleteObject(t *testing.T) {
	// Given: an object, and a principal allowed to read it but not to delete
	srv := helpers.NewTestServer(t, helpers.WithEnforceIAM(true))
	admin := seedBucketWithObject(t, srv, "bulk", "keep")
	seedIAMPrincipal(t, srv, "reader", `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["s3:GetObject","s3:ListBucket"],"Resource":"*"}]}`)

	// When: it deletes the object in bulk
	_, err := s3Client(srv, "reader").DeleteObjects(context.Background(), &s3.DeleteObjectsInput{
		Bucket: aws.String("bulk"),
		Delete: &s3types.Delete{Objects: []s3types.ObjectIdentifier{{Key: aws.String("keep")}}},
	})

	// Then: it is authorised as s3:DeleteObject, denied, and the object stays
	assertAccessDenied(t, err, "s3:DeleteObject")
	if _, err := admin.HeadObject(context.Background(), &s3.HeadObjectInput{Bucket: aws.String("bulk"), Key: aws.String("keep")}); err != nil {
		t.Fatalf("HeadObject after a denied DeleteObjects: %v", err)
	}
}

func TestIAMEnforceS3Operations_createBucketDoesNotAllowPutBucketPolicy(t *testing.T) {
	// Given: a principal allowed s3:CreateBucket and nothing else, and its bucket
	srv := helpers.NewTestServer(t, helpers.WithEnforceIAM(true))
	seedIAMPrincipal(t, srv, "creator", `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"s3:CreateBucket","Resource":"*"}]}`)
	client := s3Client(srv, "creator")
	ctx := context.Background()
	if _, err := client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String("owned")}); err != nil {
		t.Fatalf("CreateBucket: %v", err)
	}

	// When: it sets a bucket policy, which is a PUT on the bucket too
	_, err := client.PutBucketPolicy(ctx, &s3.PutBucketPolicyInput{
		Bucket: aws.String("owned"),
		Policy: aws.String(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":"*","Action":"s3:GetObject","Resource":"arn:aws:s3:::owned/*"}]}`),
	})

	// Then: it is authorised as s3:PutBucketPolicy, and denied
	assertAccessDenied(t, err, "s3:PutBucketPolicy")
}

// scopedS3Policy is what a user writes from AWS's documentation to work with
// one bucket: bucket actions on the bucket, object actions on its objects.
const scopedS3Policy = `{"Version":"2012-10-17","Statement":[
	{"Effect":"Allow","Action":["s3:CreateBucket","s3:ListBucket","s3:ListBucketMultipartUploads","s3:PutBucketTagging","s3:GetBucketTagging"],"Resource":"arn:aws:s3:::work"},
	{"Effect":"Allow","Action":["s3:PutObject","s3:GetObject","s3:DeleteObject","s3:ListMultipartUploadParts","s3:AbortMultipartUpload"],"Resource":"arn:aws:s3:::work/*"}]}`

func TestIAMEnforceS3Operations_documentedActionsAllowTheirOperations(t *testing.T) {
	// Given: a principal holding the documented actions for one bucket
	srv := helpers.NewTestServer(t, helpers.WithEnforceIAM(true))
	seedIAMPrincipal(t, srv, "worker", scopedS3Policy)
	client := s3Client(srv, "worker")
	ctx := context.Background()
	bucket := aws.String("work")
	if _, err := client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: bucket}); err != nil {
		t.Fatalf("CreateBucket: %v", err)
	}
	if _, err := client.PutObject(ctx, &s3.PutObjectInput{Bucket: bucket, Key: aws.String("a"), Body: strings.NewReader("v")}); err != nil {
		t.Fatalf("PutObject: %v", err)
	}

	// When/Then: each operation is served under the action AWS checks for it
	for _, tc := range []struct {
		name string
		call func() error
	}{
		{"HeadBucket (s3:ListBucket)", func() error {
			_, err := client.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: bucket})
			return err
		}},
		{"ListObjectsV2 (s3:ListBucket)", func() error {
			_, err := client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{Bucket: bucket})
			return err
		}},
		{"ListObjects (s3:ListBucket)", func() error {
			_, err := client.ListObjects(ctx, &s3.ListObjectsInput{Bucket: bucket})
			return err
		}},
		{"HeadObject (s3:GetObject)", func() error {
			_, err := client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: bucket, Key: aws.String("a")})
			return err
		}},
		{"CopyObject (s3:GetObject on the source, s3:PutObject on the target)", func() error {
			_, err := client.CopyObject(ctx, &s3.CopyObjectInput{Bucket: bucket, Key: aws.String("b"), CopySource: aws.String("work/a")})
			return err
		}},
		{"PutBucketTagging and GetBucketTagging", func() error {
			if _, err := client.PutBucketTagging(ctx, &s3.PutBucketTaggingInput{Bucket: bucket, Tagging: &s3types.Tagging{TagSet: []s3types.Tag{{Key: aws.String("k"), Value: aws.String("v")}}}}); err != nil {
				return err
			}
			_, err := client.GetBucketTagging(ctx, &s3.GetBucketTaggingInput{Bucket: bucket})
			return err
		}},
		{"multipart upload (s3:PutObject, s3:ListMultipartUploadParts, s3:ListBucketMultipartUploads)", func() error {
			return multipartUpload(ctx, client, bucket, aws.String("mp"))
		}},
		{"AbortMultipartUpload (s3:AbortMultipartUpload)", func() error {
			created, err := client.CreateMultipartUpload(ctx, &s3.CreateMultipartUploadInput{Bucket: bucket, Key: aws.String("aborted")})
			if err != nil {
				return err
			}
			_, err = client.AbortMultipartUpload(ctx, &s3.AbortMultipartUploadInput{Bucket: bucket, Key: aws.String("aborted"), UploadId: created.UploadId})
			return err
		}},
		{"DeleteObjects (s3:DeleteObject on each object)", func() error {
			_, err := client.DeleteObjects(ctx, &s3.DeleteObjectsInput{Bucket: bucket, Delete: &s3types.Delete{Objects: []s3types.ObjectIdentifier{{Key: aws.String("a")}, {Key: aws.String("b")}}}})
			return err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.call(); err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}
		})
	}
}

// multipartUpload uploads key in one part, listing the upload and its parts
// on the way.
func multipartUpload(ctx context.Context, client *s3.Client, bucket, key *string) error {
	created, err := client.CreateMultipartUpload(ctx, &s3.CreateMultipartUploadInput{Bucket: bucket, Key: key})
	if err != nil {
		return err
	}
	part, err := client.UploadPart(ctx, &s3.UploadPartInput{Bucket: bucket, Key: key, UploadId: created.UploadId, PartNumber: aws.Int32(1), Body: strings.NewReader("part")})
	if err != nil {
		return err
	}
	if _, err := client.ListMultipartUploads(ctx, &s3.ListMultipartUploadsInput{Bucket: bucket}); err != nil {
		return err
	}
	if _, err := client.ListParts(ctx, &s3.ListPartsInput{Bucket: bucket, Key: key, UploadId: created.UploadId}); err != nil {
		return err
	}
	_, err = client.CompleteMultipartUpload(ctx, &s3.CompleteMultipartUploadInput{
		Bucket: bucket, Key: key, UploadId: created.UploadId,
		MultipartUpload: &s3types.CompletedMultipartUpload{Parts: []s3types.CompletedPart{{ETag: part.ETag, PartNumber: aws.Int32(1)}}},
	})
	return err
}

func TestIAMEnforceS3Operations_deleteObjectsAuthorisesEachKey(t *testing.T) {
	// Given: two objects, and a principal allowed to delete only one of them
	srv := helpers.NewTestServer(t, helpers.WithEnforceIAM(true))
	admin := seedBucketWithObject(t, srv, "mixed", "public/a")
	if _, err := admin.PutObject(context.Background(), &s3.PutObjectInput{Bucket: aws.String("mixed"), Key: aws.String("private/b"), Body: strings.NewReader("v")}); err != nil {
		t.Fatalf("PutObject: %v", err)
	}
	seedIAMPrincipal(t, srv, "pruner", `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"s3:DeleteObject","Resource":"arn:aws:s3:::mixed/public/*"}]}`)

	// When: it deletes both in one request
	_, err := s3Client(srv, "pruner").DeleteObjects(context.Background(), &s3.DeleteObjectsInput{
		Bucket: aws.String("mixed"),
		Delete: &s3types.Delete{Objects: []s3types.ObjectIdentifier{{Key: aws.String("public/a")}, {Key: aws.String("private/b")}}},
	})

	// Then: the key it may not delete refuses the request, and neither goes
	assertAccessDenied(t, err, "arn:aws:s3:::mixed/private/b")
	for _, key := range []string{"public/a", "private/b"} {
		if _, err := admin.HeadObject(context.Background(), &s3.HeadObjectInput{Bucket: aws.String("mixed"), Key: aws.String(key)}); err != nil {
			t.Fatalf("HeadObject %s after a denied DeleteObjects: %v", key, err)
		}
	}
}

func TestIAMEnforceS3Operations_rawSubResourceRequests(t *testing.T) {
	for _, tc := range []struct {
		name, method, path, action string
	}{
		// The SDK adds x-id to name the operation; S3 serves by method and
		// path alone, so a PUT to a key is PutObject whatever x-id says.
		{"x-id cannot rename a PutObject", http.MethodPut, "/raw/key?x-id=GetObject", "s3:PutObject"},
		{"x-id cannot rename a DeleteObject", http.MethodDelete, "/raw/key?x-id=GetObject", "s3:DeleteObject"},
		// DeleteObjects without the x-id the SDK adds.
		{"DeleteObjects without x-id", http.MethodPost, "/raw?delete", "s3:DeleteObject"},
		{"bucket policy", http.MethodPut, "/raw?policy", "s3:PutBucketPolicy"},
		{"bucket tagging", http.MethodPut, "/raw?tagging", "s3:PutBucketTagging"},
		{"bucket ACL", http.MethodPut, "/raw?acl", "s3:PutBucketAcl"},
		{"bucket CORS removal", http.MethodDelete, "/raw?cors", "s3:PutBucketCORS"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Given: a bucket, and a principal allowed only to read objects
			// and create buckets
			srv := helpers.NewTestServer(t, helpers.WithEnforceIAM(true))
			seedBucketWithObject(t, srv, "raw", "key")
			seedIAMPrincipal(t, srv, "limited", `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["s3:GetObject","s3:CreateBucket"],"Resource":"*"}]}`)

			// When: it sends the request, signed for s3
			req, err := http.NewRequest(tc.method, srv.URL+tc.path, strings.NewReader(`<Delete><Object><Key>key</Key></Object></Delete>`))
			if err != nil {
				t.Fatalf("build request: %v", err)
			}
			resp := doSigned(t, req, sigV4Auth("limited", "s3"))

			// Then: it is authorised as the operation S3 serves, and denied
			assertS3Denied(t, resp, tc.action)
		})
	}
}
