package s3_test

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	smithyhttp "github.com/aws/smithy-go/transport/http"

	"github.com/overcast-sh/overcast/tests/helpers"
)

// The tests in this file pin #2286: a sub-resource the model binds is served
// as its own operation, a 501 where Overcast does not implement it, and never
// as the plain operation of its route — CreateBucket, PutObject or
// DeleteBucket.

// assertNotImplemented fails t unless err is S3's 501 NotImplemented.
func assertNotImplemented(t *testing.T, operation string, err error) {
	t.Helper()
	var respErr *smithyhttp.ResponseError
	if !errors.As(err, &respErr) || respErr.HTTPStatusCode() != http.StatusNotImplemented {
		t.Fatalf("%s: err = %v, want a 501 NotImplemented", operation, err)
	}
}

func TestSDKUpdateBucketMetadataJournalTableConfiguration_createsNoBucket(t *testing.T) {
	// Given: no bucket named journal-bucket
	srv := helpers.NewTestServer(t)
	client := s3Client(t, srv)
	ctx := context.Background()

	// When: its journal table configuration is updated
	_, err := client.UpdateBucketMetadataJournalTableConfiguration(ctx, &s3.UpdateBucketMetadataJournalTableConfigurationInput{
		Bucket: aws.String("journal-bucket"),
		JournalTableConfiguration: &types.JournalTableConfigurationUpdates{
			RecordExpiration: &types.RecordExpiration{Expiration: types.ExpirationStateDisabled},
		},
	})

	// Then: S3 answers 501, and has not created the bucket
	assertNotImplemented(t, "UpdateBucketMetadataJournalTableConfiguration", err)
	if _, err := client.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: aws.String("journal-bucket")}); err == nil {
		t.Fatal("HeadBucket found journal-bucket: PUT ?metadataJournalTable created it")
	}
}

func TestSDKDeleteBucketMetadataConfiguration_keepsTheBucket(t *testing.T) {
	// Given: an empty bucket
	srv := helpers.NewTestServer(t)
	client := s3Client(t, srv)
	ctx := context.Background()
	if _, err := client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String("meta-bucket")}); err != nil {
		t.Fatalf("CreateBucket: %v", err)
	}

	// When: its metadata configuration is deleted
	_, err := client.DeleteBucketMetadataConfiguration(ctx, &s3.DeleteBucketMetadataConfigurationInput{
		Bucket: aws.String("meta-bucket"),
	})

	// Then: S3 answers 501, and the bucket is still there
	assertNotImplemented(t, "DeleteBucketMetadataConfiguration", err)
	if _, err := client.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: aws.String("meta-bucket")}); err != nil {
		t.Fatalf("HeadBucket: %v: DELETE ?metadataConfiguration deleted the bucket", err)
	}
}

func TestSDKRenameObject_writesNoObject(t *testing.T) {
	// Given: a bucket holding src.txt
	srv := helpers.NewTestServer(t)
	client := s3Client(t, srv)
	ctx := context.Background()
	if _, err := client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String("rename-bucket")}); err != nil {
		t.Fatalf("CreateBucket: %v", err)
	}
	putObject(t, srv, "rename-bucket", "src.txt", []byte("hello"), "text/plain")

	// When: it is renamed to dest.txt
	_, err := client.RenameObject(ctx, &s3.RenameObjectInput{
		Bucket:       aws.String("rename-bucket"),
		Key:          aws.String("dest.txt"),
		RenameSource: aws.String("rename-bucket/src.txt"),
	})

	// Then: S3 answers 501, and has written no dest.txt
	assertNotImplemented(t, "RenameObject", err)
	if _, err := client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String("rename-bucket"), Key: aws.String("dest.txt")}); err == nil {
		t.Fatal("HeadObject found dest.txt: PUT ?renameObject wrote it")
	}
}

// The SDK has no PutObjectAnnotation yet, so the request is sent by hand.
func TestPutObjectAnnotation_leavesTheObject(t *testing.T) {
	// Given: a bucket holding k.txt
	srv := helpers.NewTestServer(t)
	createBucket(t, srv, "annotated")
	putObject(t, srv, "annotated", "k.txt", []byte("original"), "text/plain")

	// When: an annotation is put on it
	resp := do(t, put(srv, "/annotated/k.txt?annotation&annotationName=note", []byte("an annotation"), nil))

	// Then: S3 answers 501, and the object is unchanged
	helpers.AssertStatus(t, resp, http.StatusNotImplemented)
	assertObjectBody(t, srv, "annotated", "k.txt", "original")
}

func TestSubResourceOnAnotherMethod_keepsTheBucket(t *testing.T) {
	// Given: an empty bucket
	srv := helpers.NewTestServer(t)
	createBucket(t, srv, "refused")

	// When: versioning is deleted, which S3 serves on no DELETE
	resp := do(t, del(srv, "/refused?versioning"))

	// Then: S3 refuses it, where it once served DeleteBucket
	helpers.AssertStatus(t, resp, http.StatusMethodNotAllowed)
	helpers.AssertStatus(t, do(t, head(srv, "/refused")), http.StatusOK)
}

func TestSubResourceOnAnotherMethod_keepsTheObject(t *testing.T) {
	for _, tc := range []struct {
		name string
		req  func(*helpers.TestServer) *http.Request
	}{
		// DELETE ?acl was a DeleteObject.
		{"delete", func(srv *helpers.TestServer) *http.Request { return del(srv, "/refused/k.txt?acl") }},
		// PUT ?attributes was a PutObject.
		{"write", func(srv *helpers.TestServer) *http.Request {
			return put(srv, "/refused/k.txt?attributes", []byte("overwritten"), nil)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Given: a bucket holding k.txt
			srv := helpers.NewTestServer(t)
			createBucket(t, srv, "refused")
			putObject(t, srv, "refused", "k.txt", []byte("original"), "text/plain")

			// When: a sub-resource is sent on a method S3 serves it on none
			resp := do(t, tc.req(srv))

			// Then: S3 refuses it, and the object is unchanged
			helpers.AssertStatus(t, resp, http.StatusMethodNotAllowed)
			assertObjectBody(t, srv, "refused", "k.txt", "original")
		})
	}
}

// do sends req and closes the response body.
func do(t *testing.T, req *http.Request) *http.Response {
	t.Helper()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp
}
