package s3_test

// s3_write_atomicity_test.go covers #2192 from a client's side: a write that
// does not complete never replaces what the key already holds. AWS documents
// PutObject as atomic — "Amazon S3 never adds partial objects; if you receive
// a success response, Amazon S3 added the entire object to the bucket"
// (https://docs.aws.amazon.com/AmazonS3/latest/API/API_PutObject.html) — and a
// failed upload leaves the existing object readable. CopyObject,
// UploadPart and CompleteMultipartUpload share the write path, so each is
// checked here too.
//
// A client disconnect is a transport event no SDK can be asked to produce, so
// those cases write the request by hand on a raw connection; everything else,
// and every read that checks the outcome, goes through the AWS SDK.

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"

	"github.com/overcast-sh/overcast/internal/state"
	"github.com/overcast-sh/overcast/tests/helpers"
)

// ---- Fixtures --------------------------------------------------------------

// commitFailStore refuses the next write to the s3:objects namespace — the
// record that makes a new body the key's current object — and passes every
// other call through.
type commitFailStore struct {
	state.Store
	mu    sync.Mutex
	armed bool
}

func (s *commitFailStore) failNextCommit() {
	s.mu.Lock()
	s.armed = true
	s.mu.Unlock()
}

func (s *commitFailStore) Set(ctx context.Context, namespace, key, value string) error {
	s.mu.Lock()
	fail := s.armed && namespace == "s3:objects"
	if fail {
		s.armed = false
	}
	s.mu.Unlock()
	if fail {
		return errors.New("state: injected commit failure")
	}
	return s.Store.Set(ctx, namespace, key, value)
}

// noRetryS3Client is s3Client without retries, so an injected one-shot
// failure reaches the test instead of being retried away by the SDK.
func noRetryS3Client(t *testing.T, srv *helpers.TestServer) *s3.Client {
	t.Helper()
	return s3.New(s3Client(t, srv).Options(), func(o *s3.Options) { o.RetryMaxAttempts = 1 })
}

func sdkPut(t *testing.T, client *s3.Client, bucket, key, body string) *s3.PutObjectOutput {
	t.Helper()
	out, err := client.PutObject(context.Background(), &s3.PutObjectInput{
		Bucket: aws.String(bucket), Key: aws.String(key), Body: strings.NewReader(body),
	})
	if err != nil {
		t.Fatalf("PutObject %s/%s: %v", bucket, key, err)
	}
	return out
}

func sdkCreateBucket(t *testing.T, client *s3.Client, bucket string) {
	t.Helper()
	if _, err := client.CreateBucket(context.Background(), &s3.CreateBucketInput{Bucket: aws.String(bucket)}); err != nil {
		t.Fatalf("CreateBucket %s: %v", bucket, err)
	}
}

// assertSDKObject reads the key's current version through the SDK and checks
// its body and ETag.
func assertSDKObject(t *testing.T, client *s3.Client, bucket, key, wantBody, wantETag string) {
	t.Helper()
	out, err := client.GetObject(context.Background(), &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)})
	if err != nil {
		t.Fatalf("GetObject %s/%s: %v", bucket, key, err)
	}
	defer out.Body.Close()
	body, err := io.ReadAll(out.Body)
	if err != nil {
		t.Fatalf("read %s/%s: %v", bucket, key, err)
	}
	if string(body) != wantBody || aws.ToString(out.ETag) != wantETag {
		t.Errorf("%s/%s = %q (ETag %s), want %q (ETag %s)", bucket, key, body, aws.ToString(out.ETag), wantBody, wantETag)
	}
}

// abandonedUpload sends a request whose declared Content-Length is longer than
// the body that follows, then half-closes the connection: the server sees the
// client go away part way through the body. It returns once the server has
// answered and closed its side, so the write's outcome is settled before the
// test looks at it, and it fails the test unless the server reports the write
// as failed — an answer given before the body was read would leave the test
// proving nothing.
func abandonedUpload(t *testing.T, srv *helpers.TestServer, method, path, partialBody string) {
	t.Helper()
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parse server URL: %v", err)
	}
	conn, err := net.Dial("tcp", u.Host)
	if err != nil {
		t.Fatalf("dial %s: %v", u.Host, err)
	}
	defer conn.Close()
	head := fmt.Sprintf("%s %s HTTP/1.1\r\nHost: %s\r\nContent-Length: %d\r\n\r\n", method, path, u.Host, len(partialBody)+1024)
	if _, err := io.WriteString(conn, head+partialBody); err != nil {
		t.Fatalf("write request: %v", err)
	}
	if err := conn.(*net.TCPConn).CloseWrite(); err != nil {
		t.Fatalf("half-close: %v", err)
	}
	reply, err := io.ReadAll(conn) // returns when the server closes the connection
	if err != nil {
		t.Fatalf("read reply: %v", err)
	}
	resp, err := http.ReadResponse(bufio.NewReader(bytes.NewReader(reply)), nil)
	if err != nil {
		t.Fatalf("parse reply %q: %v", reply, err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("abandoned upload answered %s, want the failed write's 500", resp.Status)
	}
}

// ---- PutObject -------------------------------------------------------------

func TestPutObject_clientDisconnectsOverAnExistingKey(t *testing.T) {
	for _, status := range []types.BucketVersioningStatus{"", types.BucketVersioningStatusEnabled, types.BucketVersioningStatusSuspended} {
		name := string(status)
		if name == "" {
			name = "Unversioned"
		}
		t.Run(name, func(t *testing.T) {
			// Given: a key holding an object
			srv := helpers.NewTestServer(t)
			client := s3Client(t, srv)
			sdkCreateBucket(t, client, "atomic")
			if status != "" {
				if _, err := client.PutBucketVersioning(context.Background(), &s3.PutBucketVersioningInput{
					Bucket:                  aws.String("atomic"),
					VersioningConfiguration: &types.VersioningConfiguration{Status: status},
				}); err != nil {
					t.Fatalf("PutBucketVersioning: %v", err)
				}
			}
			original := sdkPut(t, client, "atomic", "k", "original")

			// When: a client starts overwriting it and disconnects mid-body
			abandonedUpload(t, srv, "PUT", "/atomic/k", "new-")

			// Then: the original object is still the one served
			assertSDKObject(t, client, "atomic", "k", "original", aws.ToString(original.ETag))
		})
	}
}

// ---- UploadPart ------------------------------------------------------------

func TestUploadPart_clientDisconnectsOverAnUploadedPart(t *testing.T) {
	// Given: a multipart upload whose part 1 is uploaded
	srv := helpers.NewTestServer(t)
	client := s3Client(t, srv)
	ctx := context.Background()
	sdkCreateBucket(t, client, "atomic")
	mpu, err := client.CreateMultipartUpload(ctx, &s3.CreateMultipartUploadInput{Bucket: aws.String("atomic"), Key: aws.String("k")})
	if err != nil {
		t.Fatalf("CreateMultipartUpload: %v", err)
	}
	part, err := client.UploadPart(ctx, &s3.UploadPartInput{
		Bucket: aws.String("atomic"), Key: aws.String("k"), UploadId: mpu.UploadId,
		PartNumber: aws.Int32(1), Body: bytes.NewReader([]byte("part-one")),
	})
	if err != nil {
		t.Fatalf("UploadPart: %v", err)
	}

	// When: a re-upload of part 1 is abandoned mid-body
	abandonedUpload(t, srv, "PUT", "/atomic/k?partNumber=1&uploadId="+url.QueryEscape(aws.ToString(mpu.UploadId)), "part-t")

	// Then: the upload completes with the part that was uploaded in full
	if _, err := client.CompleteMultipartUpload(ctx, &s3.CompleteMultipartUploadInput{
		Bucket: aws.String("atomic"), Key: aws.String("k"), UploadId: mpu.UploadId,
		MultipartUpload: &types.CompletedMultipartUpload{Parts: []types.CompletedPart{{PartNumber: aws.Int32(1), ETag: part.ETag}}},
	}); err != nil {
		t.Fatalf("CompleteMultipartUpload: %v", err)
	}
	out, err := client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String("atomic"), Key: aws.String("k")})
	if err != nil {
		t.Fatalf("GetObject: %v", err)
	}
	defer out.Body.Close()
	if body, _ := io.ReadAll(out.Body); string(body) != "part-one" {
		t.Errorf("body = %q, want %q", body, "part-one")
	}
}

// ---- CompleteMultipartUpload -----------------------------------------------

func TestCompleteMultipartUpload_failedCommitKeepsTheCurrentObject(t *testing.T) {
	// Given: a key holding an object, and a finished multipart upload to it
	store := &commitFailStore{Store: state.NewMemoryStore()}
	srv := helpers.NewTestServer(t, helpers.WithStore(store))
	client := noRetryS3Client(t, srv)
	ctx := context.Background()
	sdkCreateBucket(t, client, "atomic")
	original := sdkPut(t, client, "atomic", "k", "original")
	mpu, err := client.CreateMultipartUpload(ctx, &s3.CreateMultipartUploadInput{Bucket: aws.String("atomic"), Key: aws.String("k")})
	if err != nil {
		t.Fatalf("CreateMultipartUpload: %v", err)
	}
	part, err := client.UploadPart(ctx, &s3.UploadPartInput{
		Bucket: aws.String("atomic"), Key: aws.String("k"), UploadId: mpu.UploadId,
		PartNumber: aws.Int32(1), Body: bytes.NewReader([]byte("assembled")),
	})
	if err != nil {
		t.Fatalf("UploadPart: %v", err)
	}

	// When: completing it fails to record the assembled object
	store.failNextCommit()
	_, err = client.CompleteMultipartUpload(ctx, &s3.CompleteMultipartUploadInput{
		Bucket: aws.String("atomic"), Key: aws.String("k"), UploadId: mpu.UploadId,
		MultipartUpload: &types.CompletedMultipartUpload{Parts: []types.CompletedPart{{PartNumber: aws.Int32(1), ETag: part.ETag}}},
	})

	// Then: the completion fails and the original object is still served
	if err == nil {
		t.Fatal("CompleteMultipartUpload succeeded despite the failed commit")
	}
	assertSDKObject(t, client, "atomic", "k", "original", aws.ToString(original.ETag))
}

// ---- CopyObject ------------------------------------------------------------

func TestCopyObject_failedCommitKeepsTheDestination(t *testing.T) {
	// Given: a source object and a destination key that already holds one
	store := &commitFailStore{Store: state.NewMemoryStore()}
	srv := helpers.NewTestServer(t, helpers.WithStore(store))
	client := noRetryS3Client(t, srv)
	sdkCreateBucket(t, client, "atomic")
	sdkPut(t, client, "atomic", "src", "source")
	original := sdkPut(t, client, "atomic", "dst", "original")

	// When: the copy fails to record its result
	store.failNextCommit()
	_, err := client.CopyObject(context.Background(), &s3.CopyObjectInput{
		Bucket: aws.String("atomic"), Key: aws.String("dst"), CopySource: aws.String("atomic/src"),
	})

	// Then: the copy fails and the destination still holds its own object
	if err == nil {
		t.Fatal("CopyObject succeeded despite the failed commit")
	}
	assertSDKObject(t, client, "atomic", "dst", "original", aws.ToString(original.ETag))
}

func TestCopyObject_ontoItselfKeepsTheBody(t *testing.T) {
	// Given: an object
	srv := helpers.NewTestServer(t)
	client := s3Client(t, srv)
	sdkCreateBucket(t, client, "atomic")
	original := sdkPut(t, client, "atomic", "k", "keep these bytes")

	// When: it is copied onto itself to replace its metadata — the documented
	// way to change an existing object's metadata
	if _, err := client.CopyObject(context.Background(), &s3.CopyObjectInput{
		Bucket: aws.String("atomic"), Key: aws.String("k"), CopySource: aws.String("atomic/k"),
		MetadataDirective: types.MetadataDirectiveReplace,
		Metadata:          map[string]string{"stage": "reviewed"},
	}); err != nil {
		t.Fatalf("CopyObject: %v", err)
	}

	// Then: the bytes, and so the ETag, are unchanged
	assertSDKObject(t, client, "atomic", "k", "keep these bytes", aws.ToString(original.ETag))
}
