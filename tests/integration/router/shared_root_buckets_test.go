// S3 buckets named after a root the main router dispatches (#2098).
//
// A handful of path roots are shared by more than one service and are owned by
// the main router, which picks a sub-router at request time: /tags by the
// resource ARN, /applications by the SigV4 signing name, and every S3 Tables
// root plus /iceberg by the signing name too. Each is also a legal S3 bucket
// name. (/v1/tags and /v2/apis are dispatched the same way, but "v1" and "v2"
// are too short to name a bucket, so no S3 request can reach them.)
//
// Two things used to go wrong for such a bucket. The dispatchers reach S3 from
// inside a chi mount, which had shifted chi's routing path past the root, so
// S3's absolute "/{bucket}/*" patterns read GET /tags/obj/key.txt as the object
// "key.txt" in a bucket named "obj". And the signing-name dispatchers sent an
// S3-signed request to their fallback *service*, so an SDK ListObjectsV2 on a
// bucket named "applications" was answered by AppRegistry's ListApplications.
//
// Run: go test ./tests/integration/router/...
package router_test

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"

	"github.com/overcast-sh/overcast/internal/services/s3tables"
	"github.com/overcast-sh/overcast/tests/helpers"
)

// sharedRoots lists every root the main router mounts a dispatcher on that is
// also a legal bucket name. A new one belongs here, so a bucket named after it
// is proven to keep working.
func sharedRoots() []string {
	return append([]string{"/tags", "/applications", s3tables.IcebergRoot}, s3tables.Roots...)
}

// sharedRootKey is the object every case stores: nested, so its path is not
// one the root's own services serve.
const sharedRootKey = "obj/key.txt"

func sharedRootS3Client(srv *helpers.TestServer) *s3.Client {
	return s3.New(s3.Options{
		Region:       "us-east-1",
		Credentials:  credentials.NewStaticCredentialsProvider("test", "test", ""),
		BaseEndpoint: aws.String(srv.URL),
		UsePathStyle: true,
		HTTPClient:   http.DefaultClient,
	})
}

// unsignedGet reads a path the way a browser or curl does, optionally against
// a virtual-hosted Host, and returns the status and body.
func unsignedGet(t *testing.T, srv *helpers.TestServer, host, path string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, srv.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if host != "" {
		req.Host = host
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body)
}

func TestS3BucketNamedLikeASharedRoot_roundTripsThroughTheSDK(t *testing.T) {
	core, logs := observer.New(zap.InfoLevel)
	srv := helpers.NewTestServer(t, helpers.WithLogger(zap.New(core)))
	c := sharedRootS3Client(srv)
	ctx := context.Background()

	for _, root := range sharedRoots() {
		bucket, key := strings.TrimPrefix(root, "/"), sharedRootKey
		t.Run(bucket, func(t *testing.T) {
			// Given: an S3 bucket named after the root, holding an object whose
			// path starts with the whole root
			if _, err := c.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(bucket)}); err != nil {
				t.Fatalf("CreateBucket: %v", err)
			}
			if _, err := c.PutObject(ctx, &s3.PutObjectInput{
				Bucket: aws.String(bucket), Key: aws.String(key), Body: bytes.NewReader([]byte("payload")),
			}); err != nil {
				t.Fatalf("PutObject: %v", err)
			}

			// When: the SDK reads, heads and lists it
			obj, err := c.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)})
			if err != nil {
				t.Fatalf("GetObject: %v", err)
			}
			body, _ := io.ReadAll(obj.Body)
			obj.Body.Close()
			if _, err := c.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)}); err != nil {
				t.Errorf("HeadObject: %v", err)
			}
			if _, err := c.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: aws.String(bucket)}); err != nil {
				t.Errorf("HeadBucket: %v", err)
			}
			list, err := c.ListObjectsV2(ctx, &s3.ListObjectsV2Input{Bucket: aws.String(bucket)})
			if err != nil {
				t.Fatalf("ListObjectsV2: %v", err)
			}

			// Then: S3 answers every call, on the bucket and key the caller named
			if string(body) != "payload" {
				t.Errorf("GetObject body = %q, want %q", body, "payload")
			}
			if len(list.Contents) != 1 || aws.ToString(list.Contents[0].Key) != key {
				t.Errorf("ListObjectsV2 = %d keys, want exactly %q", len(list.Contents), key)
			}
			if _, err := c.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)}); err != nil {
				t.Errorf("DeleteObject: %v", err)
			}
			if _, err := c.DeleteBucket(ctx, &s3.DeleteBucketInput{Bucket: aws.String(bucket)}); err != nil {
				t.Errorf("DeleteBucket: %v", err)
			}
		})
	}

	// And: every one of those requests was logged as S3's
	for _, entry := range logs.FilterMessage("request").All() {
		if svc, _ := entry.ContextMap()["service"].(string); svc != "s3" {
			t.Errorf("%s %s logged as service=%q, want s3", entry.ContextMap()["method"], entry.ContextMap()["path"], svc)
		}
	}
}

func TestS3BucketNamedLikeASharedRoot_answersUnsignedAndVirtualHostedReads(t *testing.T) {
	srv := helpers.NewTestServer(t)
	c := sharedRootS3Client(srv)
	ctx := context.Background()
	const plain = "plain-bucket"
	if _, err := c.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(plain)}); err != nil {
		t.Fatalf("CreateBucket %s: %v", plain, err)
	}

	for _, root := range sharedRoots() {
		bucket, key := strings.TrimPrefix(root, "/"), sharedRootKey
		t.Run(bucket, func(t *testing.T) {
			// Given: the nested object and a top-level one in a bucket named
			// after the root, and the nested key, root and all, in an ordinary
			// bucket
			if _, err := c.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(bucket)}); err != nil {
				t.Fatalf("CreateBucket: %v", err)
			}
			for _, b := range []struct{ bucket, key string }{{bucket, key}, {bucket, "top.txt"}, {plain, bucket + "/" + key}} {
				if _, err := c.PutObject(ctx, &s3.PutObjectInput{
					Bucket: aws.String(b.bucket), Key: aws.String(b.key), Body: bytes.NewReader([]byte("payload")),
				}); err != nil {
					t.Fatalf("PutObject %s/%s: %v", b.bucket, b.key, err)
				}
			}
			vhost := bucket + ".s3.localhost:4566"

			cases := []struct{ name, host, path string }{
				{name: "unsigned path-style", path: "/" + bucket + "/" + key},
				{name: "virtual-hosted nested key", host: vhost, path: "/" + key},
				{name: "virtual-hosted top-level key", host: vhost, path: "/top.txt"},
				{name: "virtual-hosted key under the root", host: plain + ".s3.localhost:4566", path: root + "/" + key},
			}
			for _, tc := range cases {
				// When: the object is read with no signature at all
				status, body := unsignedGet(t, srv, tc.host, tc.path)

				// Then: S3 serves it
				if status != http.StatusOK || body != "payload" {
					t.Errorf("%s GET %s = %d %q, want 200 %q", tc.name, tc.path, status, body, "payload")
				}
			}

			// And: the virtual-hosted bucket root lists the bucket
			if status, body := unsignedGet(t, srv, vhost, "/"); status != http.StatusOK || !strings.Contains(body, "<ListBucketResult") {
				t.Errorf("virtual-hosted GET / = %d %q, want 200 ListBucketResult", status, body)
			}
		})
	}
}

func TestSharedRoot_s3ControlSignedRequestIsNotTakenToS3(t *testing.T) {
	// Given: a request signed for "s3" that also names the account, which only
	// S3 Control sends — the two APIs share a signing name
	srv := helpers.NewTestServer(t)
	req, err := http.NewRequest(http.MethodGet, srv.URL+"/applications", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=test/20260926/us-east-1/s3/aws4_request, SignedHeaders=host, Signature=x")
	req.Header.Set("X-Amz-Account-Id", "000000000000")

	// When: it reaches the shared /applications root
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	// Then: the dispatcher answers it (AppRegistry's ListApplications), not S3
	helpers.AssertStatus(t, resp, http.StatusOK)
	if !strings.Contains(string(body), `"applications"`) {
		t.Errorf("body = %q, want AppRegistry's ListApplications", body)
	}
}
