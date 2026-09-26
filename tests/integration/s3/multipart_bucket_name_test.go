package s3_test

import (
	"bytes"
	"context"
	"io"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/overcast-sh/overcast/tests/helpers"
)

// A bucket's name must not decide the fate of another bucket's uploads
// (#2234). "multipart" is a valid bucket name, and part bodies used to live
// in a directory of that name, so deleting such a bucket deleted every
// in-progress upload's parts with it.
func TestSDKDeleteBucket_namedMultipartKeepsOtherBucketsUploads(t *testing.T) {
	// Given: a bucket named "multipart", and an upload in progress elsewhere
	srv := helpers.NewTestServer(t)
	client := s3Client(t, srv)
	ctx := context.Background()
	for _, name := range []string{"multipart", "mp-survivor"} {
		if _, err := client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(name)}); err != nil {
			t.Fatalf("CreateBucket %s: %v", name, err)
		}
	}
	part := []byte("a part that must outlive the other bucket")
	uploadID, parts := stagedSDKUpload(t, client, "mp-survivor", "k.bin", part)

	// When: the bucket named "multipart" is deleted
	if _, err := client.DeleteBucket(ctx, &s3.DeleteBucketInput{Bucket: aws.String("multipart")}); err != nil {
		t.Fatalf("DeleteBucket: %v", err)
	}

	// Then: the other bucket's upload still completes, with its bytes
	if _, err := completeSDK(client, "mp-survivor", "k.bin", uploadID, parts); err != nil {
		t.Fatalf("CompleteMultipartUpload after deleting bucket multipart: %v", err)
	}
	got, err := client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String("mp-survivor"), Key: aws.String("k.bin")})
	if err != nil {
		t.Fatalf("GetObject: %v", err)
	}
	defer got.Body.Close()
	body, _ := io.ReadAll(got.Body)
	if !bytes.Equal(body, part) {
		t.Errorf("body = %q, want %q", body, part)
	}
}
