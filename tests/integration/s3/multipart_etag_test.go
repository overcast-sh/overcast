package s3_test

import (
	"bytes"
	"context"
	"crypto/md5"
	"fmt"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/overcast-sh/overcast/tests/helpers"
)

// The ETag of an object assembled by CompleteMultipartUpload (#2232). S3
// derives it from the parts, not from the object's bytes: the hex MD5 of the
// concatenated binary MD5s of the parts, suffixed "-<part count>". A client
// that checks a multipart download against its ETag recomputes that formula
// from the part size, so any other digest reads as corruption.
//
// A CopyObject of such an object is a new object written in one piece, and
// "Objects created by the PUT Object, POST Object, or Copy operation ... have
// ETags that are an MD5 digest of their object data":
// https://docs.aws.amazon.com/AmazonS3/latest/API/API_Object.html

// awsMultipartETag computes S3's multipart ETag for parts, independently of
// the server's implementation.
func awsMultipartETag(parts ...[]byte) string {
	var digests []byte
	for _, p := range parts {
		sum := md5.Sum(p)
		digests = append(digests, sum[:]...)
	}
	return fmt.Sprintf(`"%x-%d"`, md5.Sum(digests), len(parts))
}

func TestSDKCompleteMultipartUpload_etagIsTheDigestOfThePartMD5s(t *testing.T) {
	// Given: a two-part upload
	srv := helpers.NewTestServer(t)
	client := s3Client(t, srv)
	ctx := context.Background()
	const bucket, key = "mp-digest", "two.bin"
	if _, err := client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(bucket)}); err != nil {
		t.Fatalf("CreateBucket: %v", err)
	}
	first, second := minSizedPart(), []byte("the last part")
	uploadID, parts := stagedSDKUpload(t, client, bucket, key, first, second)
	want := awsMultipartETag(first, second)

	// When: the upload is completed
	out, err := completeSDK(client, bucket, key, uploadID, parts)

	// Then: every operation that reports the object's ETag reports S3's
	if err != nil {
		t.Fatalf("CompleteMultipartUpload: %v", err)
	}
	for op, got := range objectETags(t, client, bucket, key) {
		if got != want {
			t.Errorf("%s ETag = %s, want %s", op, got, want)
		}
	}
	if got := aws.ToString(out.ETag); got != want {
		t.Errorf("CompleteMultipartUpload ETag = %s, want %s", got, want)
	}
}

func TestSDKCopyObject_ofAMultipartObjectHasTheMD5OfItsBytes(t *testing.T) {
	// Given: an object assembled from two parts
	srv := helpers.NewTestServer(t)
	client := s3Client(t, srv)
	ctx := context.Background()
	const bucket = "mp-copy"
	if _, err := client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(bucket)}); err != nil {
		t.Fatalf("CreateBucket: %v", err)
	}
	first, second := minSizedPart(), []byte("tail")
	uploadID, parts := stagedSDKUpload(t, client, bucket, "src.bin", first, second)
	if _, err := completeSDK(client, bucket, "src.bin", uploadID, parts); err != nil {
		t.Fatalf("CompleteMultipartUpload: %v", err)
	}

	// When: it is copied in one CopyObject
	out, err := client.CopyObject(ctx, &s3.CopyObjectInput{
		Bucket:     aws.String(bucket),
		Key:        aws.String("dst.bin"),
		CopySource: aws.String(bucket + "/src.bin"),
	})

	// Then: the copy's ETag is the plain MD5 of its bytes, with no part count
	if err != nil {
		t.Fatalf("CopyObject: %v", err)
	}
	want := fmt.Sprintf(`"%x"`, md5.Sum(bytes.Join([][]byte{first, second}, nil)))
	if got := aws.ToString(out.CopyObjectResult.ETag); got != want {
		t.Errorf("CopyObject ETag = %s, want %s", got, want)
	}
	for op, got := range objectETags(t, client, bucket, "dst.bin") {
		if got != want {
			t.Errorf("%s ETag of the copy = %s, want %s", op, got, want)
		}
	}
}

// objectETags returns the ETag each read operation reports for bucket/key,
// keyed by operation.
func objectETags(t *testing.T, client *s3.Client, bucket, key string) map[string]string {
	t.Helper()
	ctx := context.Background()
	head, err := client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)})
	if err != nil {
		t.Fatalf("HeadObject: %v", err)
	}
	get, err := client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)})
	if err != nil {
		t.Fatalf("GetObject: %v", err)
	}
	_ = get.Body.Close()
	etags := map[string]string{
		"HeadObject": aws.ToString(head.ETag),
		"GetObject":  aws.ToString(get.ETag),
	}
	v2, err := client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{Bucket: aws.String(bucket), Prefix: aws.String(key)})
	if err != nil {
		t.Fatalf("ListObjectsV2: %v", err)
	}
	if len(v2.Contents) != 1 {
		t.Fatalf("ListObjectsV2: %d objects, want 1", len(v2.Contents))
	}
	etags["ListObjectsV2"] = aws.ToString(v2.Contents[0].ETag)
	v1, err := client.ListObjects(ctx, &s3.ListObjectsInput{Bucket: aws.String(bucket), Prefix: aws.String(key)})
	if err != nil {
		t.Fatalf("ListObjects: %v", err)
	}
	if len(v1.Contents) != 1 {
		t.Fatalf("ListObjects: %d objects, want 1", len(v1.Contents))
	}
	etags["ListObjects"] = aws.ToString(v1.Contents[0].ETag)
	return etags
}
