package s3_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	smithy "github.com/aws/smithy-go"

	"github.com/overcast-sh/overcast/tests/helpers"
)

// CompleteMultipartUpload validation (#1706). AWS refuses a completion whose
// parts list does not match what was uploaded, is out of order, or has a
// non-final part under 5 MiB, and every refusal leaves the upload in place so
// the client can retry or abort. Per the AWS API reference:
//
//   - InvalidPart (400): "One or more of the specified parts could not be
//     found. The part might not have been uploaded, or the specified ETag
//     might not have matched the uploaded part's ETag."
//   - InvalidPartOrder (400): "The list of parts was not in ascending order."
//   - EntityTooSmall (400): "Each part must be at least 5 MB in size, except
//     the last part."
//   - NoSuchUpload (404): "the multipart upload might have been aborted or
//     completed."
//   - An empty Parts list: "If you do not supply a valid Part with your
//     request, the service sends back an HTTP 400 response."
//
// https://docs.aws.amazon.com/AmazonS3/latest/API/API_CompleteMultipartUpload.html

// minPartSize is AWS's floor for every part except the last.
const minPartSize = 5 * 1024 * 1024

// minSizedPart returns a body that is exactly the minimum non-final part size.
func minSizedPart() []byte {
	return bytes.Repeat([]byte("a"), minPartSize)
}

// stagedSDKUpload initiates an upload through the SDK and uploads bodies as
// parts 1..n, returning the upload id and the CompletedPart list a correct
// completion would send.
func stagedSDKUpload(t *testing.T, client *s3.Client, bucket, key string, bodies ...[]byte) (string, []s3types.CompletedPart) {
	t.Helper()
	ctx := context.Background()
	created, err := client.CreateMultipartUpload(ctx, &s3.CreateMultipartUploadInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		t.Fatalf("CreateMultipartUpload: %v", err)
	}
	parts := make([]s3types.CompletedPart, 0, len(bodies))
	for i, body := range bodies {
		num := int32(i + 1)
		out, err := client.UploadPart(ctx, &s3.UploadPartInput{
			Bucket:     aws.String(bucket),
			Key:        aws.String(key),
			UploadId:   created.UploadId,
			PartNumber: aws.Int32(num),
			Body:       bytes.NewReader(body),
		})
		if err != nil {
			t.Fatalf("UploadPart %d: %v", num, err)
		}
		parts = append(parts, s3types.CompletedPart{PartNumber: aws.Int32(num), ETag: out.ETag})
	}
	return aws.ToString(created.UploadId), parts
}

// completeSDK sends CompleteMultipartUpload with the given parts list.
func completeSDK(client *s3.Client, bucket, key, uploadID string, parts []s3types.CompletedPart) (*s3.CompleteMultipartUploadOutput, error) {
	return client.CompleteMultipartUpload(context.Background(), &s3.CompleteMultipartUploadInput{
		Bucket:          aws.String(bucket),
		Key:             aws.String(key),
		UploadId:        aws.String(uploadID),
		MultipartUpload: &s3types.CompletedMultipartUpload{Parts: parts},
	})
}

// assertSDKError asserts err is a modelled API error with the given code and
// HTTP status.
func assertSDKError(t *testing.T, err error, wantCode string, wantStatus int) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected %s, got success", wantCode)
	}
	var apiErr smithy.APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("expected a modelled API error, got %T: %v", err, err)
	}
	if apiErr.ErrorCode() != wantCode {
		t.Errorf("expected error code %q, got %q (message: %s)", wantCode, apiErr.ErrorCode(), apiErr.ErrorMessage())
	}
	var respErr *awshttp.ResponseError
	if !errors.As(err, &respErr) {
		t.Fatalf("expected an HTTP response error, got %T: %v", err, err)
	}
	if respErr.HTTPStatusCode() != wantStatus {
		t.Errorf("expected HTTP %d, got %d", wantStatus, respErr.HTTPStatusCode())
	}
}

// assertNoObject asserts the key has no object behind it.
func assertNoObject(t *testing.T, client *s3.Client, bucket, key string) {
	t.Helper()
	_, err := client.HeadObject(context.Background(), &s3.HeadObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	})
	if err == nil {
		t.Fatalf("expected no object at %s/%s, but HeadObject succeeded", bucket, key)
	}
	var respErr *awshttp.ResponseError
	if !errors.As(err, &respErr) || respErr.HTTPStatusCode() != http.StatusNotFound {
		t.Fatalf("expected HeadObject 404 for %s/%s, got %v", bucket, key, err)
	}
}

// assertUploadIntact asserts the upload still exists with exactly wantParts
// parts, i.e. a failed completion discarded nothing.
func assertUploadIntact(t *testing.T, client *s3.Client, bucket, key, uploadID string, wantParts int) {
	t.Helper()
	out, err := client.ListParts(context.Background(), &s3.ListPartsInput{
		Bucket:   aws.String(bucket),
		Key:      aws.String(key),
		UploadId: aws.String(uploadID),
	})
	if err != nil {
		t.Fatalf("ListParts after a failed completion: %v", err)
	}
	if len(out.Parts) != wantParts {
		t.Errorf("expected %d parts still staged, got %d", wantParts, len(out.Parts))
	}
}

// ---- InvalidPart ------------------------------------------------------------

func TestSDKCompleteMultipartUpload_mismatchedPartETag(t *testing.T) {
	// Given: one full-size part, and a completion that names a fabricated ETag
	srv := helpers.NewTestServer(t)
	client := s3Client(t, srv)
	if _, err := client.CreateBucket(context.Background(), &s3.CreateBucketInput{Bucket: aws.String("mp-etag")}); err != nil {
		t.Fatalf("CreateBucket: %v", err)
	}
	uploadID, parts := stagedSDKUpload(t, client, "mp-etag", "big.bin", minSizedPart())
	wrong := []s3types.CompletedPart{
		{PartNumber: aws.Int32(1), ETag: aws.String(`"deadbeefdeadbeefdeadbeefdeadbeef"`)},
	}

	// When: the upload is completed with the wrong ETag
	_, err := completeSDK(client, "mp-etag", "big.bin", uploadID, wrong)

	// Then: InvalidPart, no object, and the upload is still there to retry
	assertSDKError(t, err, "InvalidPart", http.StatusBadRequest)
	assertNoObject(t, client, "mp-etag", "big.bin")
	assertUploadIntact(t, client, "mp-etag", "big.bin", uploadID, 1)

	// And: the retry with the real ETag succeeds
	if _, err := completeSDK(client, "mp-etag", "big.bin", uploadID, parts); err != nil {
		t.Fatalf("retry after InvalidPart: %v", err)
	}
}

func TestSDKCompleteMultipartUpload_unknownPartNumber(t *testing.T) {
	// Given: only part 1 was uploaded
	srv := helpers.NewTestServer(t)
	client := s3Client(t, srv)
	if _, err := client.CreateBucket(context.Background(), &s3.CreateBucketInput{Bucket: aws.String("mp-unknown")}); err != nil {
		t.Fatalf("CreateBucket: %v", err)
	}
	uploadID, parts := stagedSDKUpload(t, client, "mp-unknown", "big.bin", minSizedPart())
	withPhantom := append(parts, s3types.CompletedPart{PartNumber: aws.Int32(2), ETag: parts[0].ETag})

	// When: the completion also names a part 2 that was never uploaded
	_, err := completeSDK(client, "mp-unknown", "big.bin", uploadID, withPhantom)

	// Then: InvalidPart, and nothing was created or discarded
	assertSDKError(t, err, "InvalidPart", http.StatusBadRequest)
	assertNoObject(t, client, "mp-unknown", "big.bin")
	assertUploadIntact(t, client, "mp-unknown", "big.bin", uploadID, 1)
}

// ---- EntityTooSmall ---------------------------------------------------------

func TestSDKCompleteMultipartUpload_undersizedNonFinalPart(t *testing.T) {
	// Given: two parts whose first is four bytes
	srv := helpers.NewTestServer(t)
	client := s3Client(t, srv)
	if _, err := client.CreateBucket(context.Background(), &s3.CreateBucketInput{Bucket: aws.String("mp-small")}); err != nil {
		t.Fatalf("CreateBucket: %v", err)
	}
	uploadID, parts := stagedSDKUpload(t, client, "mp-small", "small.bin", []byte("tiny"), []byte("tail"))

	// When: the upload is completed with both parts, correctly listed
	_, err := completeSDK(client, "mp-small", "small.bin", uploadID, parts)

	// Then: EntityTooSmall, no object, and both parts are still staged
	assertSDKError(t, err, "EntityTooSmall", http.StatusBadRequest)
	assertNoObject(t, client, "mp-small", "small.bin")
	assertUploadIntact(t, client, "mp-small", "small.bin", uploadID, 2)
}

func TestSDKCompleteMultipartUpload_singleSmallPart(t *testing.T) {
	// Given: a single four-byte part — the only part is also the last part
	srv := helpers.NewTestServer(t)
	client := s3Client(t, srv)
	ctx := context.Background()
	if _, err := client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String("mp-one")}); err != nil {
		t.Fatalf("CreateBucket: %v", err)
	}
	uploadID, parts := stagedSDKUpload(t, client, "mp-one", "one.bin", []byte("tiny"))

	// When: the upload is completed
	out, err := completeSDK(client, "mp-one", "one.bin", uploadID, parts)

	// Then: it succeeds and the object carries the single part's bytes
	if err != nil {
		t.Fatalf("CompleteMultipartUpload with one small part: %v", err)
	}
	if !strings.HasSuffix(aws.ToString(out.ETag), `-1"`) {
		t.Errorf("expected a -1 multipart ETag, got %q", aws.ToString(out.ETag))
	}
	got, err := client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String("mp-one"), Key: aws.String("one.bin")})
	if err != nil {
		t.Fatalf("GetObject: %v", err)
	}
	defer got.Body.Close()
	body, _ := io.ReadAll(got.Body)
	if string(body) != "tiny" {
		t.Errorf("expected body %q, got %q", "tiny", body)
	}
}

func TestSDKCompleteMultipartUpload_lastPartMayBeSmall(t *testing.T) {
	// Given: a full-size first part and a four-byte last part
	srv := helpers.NewTestServer(t)
	client := s3Client(t, srv)
	ctx := context.Background()
	if _, err := client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String("mp-tail")}); err != nil {
		t.Fatalf("CreateBucket: %v", err)
	}
	uploadID, parts := stagedSDKUpload(t, client, "mp-tail", "tail.bin", minSizedPart(), []byte("tail"))

	// When: the upload is completed
	_, err := completeSDK(client, "mp-tail", "tail.bin", uploadID, parts)

	// Then: it succeeds and the object is both parts end to end
	if err != nil {
		t.Fatalf("CompleteMultipartUpload with a small last part: %v", err)
	}
	head, err := client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String("mp-tail"), Key: aws.String("tail.bin")})
	if err != nil {
		t.Fatalf("HeadObject: %v", err)
	}
	if want := int64(minPartSize + 4); aws.ToInt64(head.ContentLength) != want {
		t.Errorf("expected ContentLength %d, got %d", want, aws.ToInt64(head.ContentLength))
	}
}

// ---- InvalidPartOrder -------------------------------------------------------

func TestSDKCompleteMultipartUpload_partsOutOfOrder(t *testing.T) {
	// Given: two correctly uploaded parts
	srv := helpers.NewTestServer(t)
	client := s3Client(t, srv)
	if _, err := client.CreateBucket(context.Background(), &s3.CreateBucketInput{Bucket: aws.String("mp-order")}); err != nil {
		t.Fatalf("CreateBucket: %v", err)
	}
	uploadID, parts := stagedSDKUpload(t, client, "mp-order", "order.bin", minSizedPart(), []byte("tail"))
	reversed := []s3types.CompletedPart{parts[1], parts[0]}

	// When: the completion lists part 2 before part 1
	_, err := completeSDK(client, "mp-order", "order.bin", uploadID, reversed)

	// Then: InvalidPartOrder, and the upload is untouched
	assertSDKError(t, err, "InvalidPartOrder", http.StatusBadRequest)
	assertNoObject(t, client, "mp-order", "order.bin")
	assertUploadIntact(t, client, "mp-order", "order.bin", uploadID, 2)
}

func TestSDKCompleteMultipartUpload_duplicatePartNumber(t *testing.T) {
	// Given: one uploaded part
	srv := helpers.NewTestServer(t)
	client := s3Client(t, srv)
	if _, err := client.CreateBucket(context.Background(), &s3.CreateBucketInput{Bucket: aws.String("mp-dup")}); err != nil {
		t.Fatalf("CreateBucket: %v", err)
	}
	uploadID, parts := stagedSDKUpload(t, client, "mp-dup", "dup.bin", minSizedPart())
	twice := []s3types.CompletedPart{parts[0], parts[0]}

	// When: the completion lists the same part number twice
	_, err := completeSDK(client, "mp-dup", "dup.bin", uploadID, twice)

	// Then: the order is not strictly ascending, so InvalidPartOrder
	assertSDKError(t, err, "InvalidPartOrder", http.StatusBadRequest)
	assertNoObject(t, client, "mp-dup", "dup.bin")
}

// ---- NoSuchUpload after completion -----------------------------------------

func TestSDKCompleteMultipartUpload_completedUploadIdIsGone(t *testing.T) {
	// Given: an upload that has already been completed
	srv := helpers.NewTestServer(t)
	client := s3Client(t, srv)
	if _, err := client.CreateBucket(context.Background(), &s3.CreateBucketInput{Bucket: aws.String("mp-twice")}); err != nil {
		t.Fatalf("CreateBucket: %v", err)
	}
	uploadID, parts := stagedSDKUpload(t, client, "mp-twice", "twice.bin", []byte("once"))
	if _, err := completeSDK(client, "mp-twice", "twice.bin", uploadID, parts); err != nil {
		t.Fatalf("first CompleteMultipartUpload: %v", err)
	}

	// When: the same upload id is completed again
	_, err := completeSDK(client, "mp-twice", "twice.bin", uploadID, parts)

	// Then: NoSuchUpload — a completed upload id is no longer valid
	assertSDKError(t, err, "NoSuchUpload", http.StatusNotFound)
}

// ---- Wire-level cases ------------------------------------------------------

// completeRaw posts a literal CompleteMultipartUpload body and returns the
// response, for cases an SDK would not let a caller send.
func completeRaw(t *testing.T, srv *helpers.TestServer, bucket, key, uploadID, body string) *http.Response {
	t.Helper()
	path := fmt.Sprintf("/%s/%s?uploadId=%s", bucket, key, uploadID)
	req := mustReq(http.MethodPost, srv.URL+path, strings.NewReader(body), map[string]string{
		"Content-Type": "application/xml",
	})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("CompleteMultipartUpload: %v", err)
	}
	return resp
}

func TestCompleteMultipartUpload_emptyPartsList(t *testing.T) {
	// Given: an upload with one part staged
	srv := helpers.NewTestServer(t)
	createBucket(t, srv, "mp-empty")
	uploadID := createMultipartUpload(t, srv, "mp-empty", "empty.bin")
	uploadPart(t, srv, "mp-empty", "empty.bin", uploadID, 1, []byte("data"))

	// When: the completion carries no Part elements at all
	resp := completeRaw(t, srv, "mp-empty", "empty.bin", uploadID, "<CompleteMultipartUpload></CompleteMultipartUpload>")
	defer resp.Body.Close()

	// Then: 400 MalformedXML, and the upload is still listable
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertRequestID(t, resp)
	helpers.AssertXMLError(t, resp, "MalformedXML")

	list, err := http.DefaultClient.Do(get(srv, "/mp-empty/empty.bin?uploadId="+uploadID))
	if err != nil {
		t.Fatal(err)
	}
	defer list.Body.Close()
	helpers.AssertStatus(t, list, http.StatusOK)
}

func TestCompleteMultipartUpload_unquotedETagMatches(t *testing.T) {
	// Given: a staged part whose ETag was returned quoted
	srv := helpers.NewTestServer(t)
	createBucket(t, srv, "mp-quotes")
	uploadID := createMultipartUpload(t, srv, "mp-quotes", "q.bin")
	etag := uploadPart(t, srv, "mp-quotes", "q.bin", uploadID, 1, []byte("data"))
	if !strings.HasPrefix(etag, `"`) {
		t.Fatalf("expected UploadPart to return a quoted ETag, got %q", etag)
	}

	// When: the completion names the ETag without its quotes
	body := fmt.Sprintf("<CompleteMultipartUpload><Part><PartNumber>1</PartNumber><ETag>%s</ETag></Part></CompleteMultipartUpload>",
		strings.Trim(etag, `"`))
	resp := completeRaw(t, srv, "mp-quotes", "q.bin", uploadID, body)
	defer resp.Body.Close()

	// Then: the ETag still matches and the upload completes
	helpers.AssertStatus(t, resp, http.StatusOK)
}

func TestCompleteMultipartUpload_invalidPartErrorShape(t *testing.T) {
	// Given: a staged part
	srv := helpers.NewTestServer(t)
	createBucket(t, srv, "mp-shape")
	uploadID := createMultipartUpload(t, srv, "mp-shape", "s.bin")
	uploadPart(t, srv, "mp-shape", "s.bin", uploadID, 1, []byte("data"))

	// When: the completion names the wrong ETag
	body := `<CompleteMultipartUpload><Part><PartNumber>1</PartNumber><ETag>"00000000000000000000000000000000"</ETag></Part></CompleteMultipartUpload>`
	resp := completeRaw(t, srv, "mp-shape", "s.bin", uploadID, body)
	defer resp.Body.Close()

	// Then: the standard S3 XML error envelope carries InvalidPart
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertRequestID(t, resp)
	helpers.AssertXMLError(t, resp, "InvalidPart")
}

// ---- UploadPart part number range ------------------------------------------

func TestUploadPart_partNumberOutOfRange(t *testing.T) {
	// Given: an initiated upload
	srv := helpers.NewTestServer(t)
	createBucket(t, srv, "mp-range")
	uploadID := createMultipartUpload(t, srv, "mp-range", "r.bin")

	cases := []struct {
		name       string
		partNumber string
	}{
		{name: "zero", partNumber: "0"},
		{name: "negative", partNumber: "-1"},
		{name: "above ten thousand", partNumber: "10001"},
		{name: "not an integer", partNumber: "abc"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// When: a part is uploaded under that number
			path := fmt.Sprintf("/mp-range/r.bin?partNumber=%s&uploadId=%s", tc.partNumber, uploadID)
			resp, err := http.DefaultClient.Do(mustReq(http.MethodPut, srv.URL+path, bytes.NewReader([]byte("x")), nil))
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()

			// Then: 400 InvalidArgument
			helpers.AssertStatus(t, resp, http.StatusBadRequest)
			helpers.AssertXMLError(t, resp, "InvalidArgument")
		})
	}
}

func TestUploadPart_partNumberBoundsAreInclusive(t *testing.T) {
	// Given: an initiated upload
	srv := helpers.NewTestServer(t)
	createBucket(t, srv, "mp-bounds")
	uploadID := createMultipartUpload(t, srv, "mp-bounds", "b.bin")

	// When: parts 1 and 10000 are uploaded
	for _, num := range []int{1, 10000} {
		path := fmt.Sprintf("/mp-bounds/b.bin?partNumber=%d&uploadId=%s", num, uploadID)
		resp, err := http.DefaultClient.Do(mustReq(http.MethodPut, srv.URL+path, bytes.NewReader([]byte("x")), nil))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()

		// Then: both are accepted
		helpers.AssertStatus(t, resp, http.StatusOK)
	}
}
