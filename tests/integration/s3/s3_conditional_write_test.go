package s3_test

// s3_conditional_write_test.go covers S3's conditional writes — the If-Match
// and If-None-Match headers on PutObject and CompleteMultipartUpload (#1636).
//
// AWS reference for every expectation asserted here:
//   https://docs.aws.amazon.com/AmazonS3/latest/API/API_PutObject.html
//   https://docs.aws.amazon.com/AmazonS3/latest/API/API_CompleteMultipartUpload.html
//   https://docs.aws.amazon.com/AmazonS3/latest/userguide/conditional-writes.html
//
// The user-guide page's "Conditional write behavior" section is the normative
// one: If-None-Match succeeds only when there is no current object (a delete
// marker counts as none), If-Match succeeds only when the current object's
// ETag matches and answers 404 when there is no current version at all, and
// concurrent conditional writes resolve to exactly one winner.

import (
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/overcast-sh/overcast/tests/helpers"
)

// ---- PutObject / If-None-Match ---------------------------------------------

func TestPutObject_ifNoneMatchStarOnAbsentKey(t *testing.T) {
	// Given: a bucket with no object at the key
	srv := helpers.NewTestServer(t)
	createBucket(t, srv, "cond-bucket")

	// When: we write it guarded by If-None-Match: *
	resp := putConditional(t, srv, "cond-bucket", "lock", []byte("first"), map[string]string{
		"If-None-Match": "*",
	})
	defer resp.Body.Close()

	// Then: the write succeeds
	helpers.AssertStatus(t, resp, http.StatusOK)
	helpers.AssertRequestID(t, resp)
	assertObjectBody(t, srv, "cond-bucket", "lock", "first")
}

func TestPutObject_ifNoneMatchStarOnExistingKey(t *testing.T) {
	// Given: a bucket with an object already at the key
	srv := helpers.NewTestServer(t)
	createBucket(t, srv, "cond-bucket")
	putObject(t, srv, "cond-bucket", "lock", []byte("first"), "text/plain")

	// When: a second writer guards its write with If-None-Match: *
	resp := putConditional(t, srv, "cond-bucket", "lock", []byte("second"), map[string]string{
		"If-None-Match": "*",
	})
	defer resp.Body.Close()

	// Then: it is refused and the stored object is untouched
	helpers.AssertStatus(t, resp, http.StatusPreconditionFailed)
	helpers.AssertXMLError(t, resp, "PreconditionFailed")
	helpers.AssertRequestID(t, resp)
	assertObjectBody(t, srv, "cond-bucket", "lock", "first")
}

func TestPutObject_ifNoneMatchWithAnETagValue(t *testing.T) {
	// Given: a bucket with an object at the key
	srv := helpers.NewTestServer(t)
	createBucket(t, srv, "cond-bucket")
	putObject(t, srv, "cond-bucket", "lock", []byte("first"), "text/plain")
	etag := objectETag(t, srv, "cond-bucket", "lock")

	// When: a write supplies an ETag rather than the '*' AWS documents
	resp := putConditional(t, srv, "cond-bucket", "lock", []byte("second"), map[string]string{
		"If-None-Match": etag,
	})
	defer resp.Body.Close()

	// Then: the header is refused rather than ignored, and nothing is written
	helpers.AssertStatus(t, resp, http.StatusNotImplemented)
	helpers.AssertXMLError(t, resp, "NotImplemented")
	assertObjectBody(t, srv, "cond-bucket", "lock", "first")
}

// ---- PutObject / If-Match ---------------------------------------------------

func TestPutObject_ifMatchOnCurrentETag(t *testing.T) {
	// Given: a bucket with an object whose ETag the caller holds
	srv := helpers.NewTestServer(t)
	createBucket(t, srv, "cond-bucket")
	putObject(t, srv, "cond-bucket", "doc.txt", []byte("first"), "text/plain")
	etag := objectETag(t, srv, "cond-bucket", "doc.txt")

	// When: it writes guarded by that ETag
	resp := putConditional(t, srv, "cond-bucket", "doc.txt", []byte("second"), map[string]string{
		"If-Match": etag,
	})
	defer resp.Body.Close()

	// Then: the write succeeds
	helpers.AssertStatus(t, resp, http.StatusOK)
	assertObjectBody(t, srv, "cond-bucket", "doc.txt", "second")
}

func TestPutObject_ifMatchOnStaleETag(t *testing.T) {
	// Given: a bucket whose object has moved on since the caller read it
	srv := helpers.NewTestServer(t)
	createBucket(t, srv, "cond-bucket")
	putObject(t, srv, "cond-bucket", "doc.txt", []byte("first"), "text/plain")
	stale := objectETag(t, srv, "cond-bucket", "doc.txt")
	putObject(t, srv, "cond-bucket", "doc.txt", []byte("second"), "text/plain")

	// When: it writes guarded by the ETag it read earlier
	resp := putConditional(t, srv, "cond-bucket", "doc.txt", []byte("third"), map[string]string{
		"If-Match": stale,
	})
	defer resp.Body.Close()

	// Then: it is refused and the current object stands
	helpers.AssertStatus(t, resp, http.StatusPreconditionFailed)
	helpers.AssertXMLError(t, resp, "PreconditionFailed")
	assertObjectBody(t, srv, "cond-bucket", "doc.txt", "second")
}

func TestPutObject_ifMatchOnAbsentKey(t *testing.T) {
	// Given: a bucket with nothing at the key
	srv := helpers.NewTestServer(t)
	createBucket(t, srv, "cond-bucket")

	// When: a write is guarded by an ETag
	resp := putConditional(t, srv, "cond-bucket", "gone.txt", []byte("data"), map[string]string{
		"If-Match": `"d41d8cd98f00b204e9800998ecf8427e"`,
	})
	defer resp.Body.Close()

	// Then: AWS answers 404, not 412 — the object key no longer exists
	helpers.AssertStatus(t, resp, http.StatusNotFound)
	helpers.AssertXMLError(t, resp, "NoSuchKey")
}

func TestPutObject_ifMatchAndIfNoneMatchTogether(t *testing.T) {
	// Given: a bucket with an object whose current ETag the caller holds
	srv := helpers.NewTestServer(t)
	createBucket(t, srv, "cond-bucket")
	putObject(t, srv, "cond-bucket", "doc.txt", []byte("first"), "text/plain")
	etag := objectETag(t, srv, "cond-bucket", "doc.txt")

	// When: both conditions are supplied on one write
	resp := putConditional(t, srv, "cond-bucket", "doc.txt", []byte("second"), map[string]string{
		"If-Match":      etag,
		"If-None-Match": "*",
	})
	defer resp.Body.Close()

	// Then: both must hold, so the unsatisfiable pair is refused (RFC 7232
	// evaluates If-Match first; it passes, then If-None-Match: * fails)
	helpers.AssertStatus(t, resp, http.StatusPreconditionFailed)
	helpers.AssertXMLError(t, resp, "PreconditionFailed")
	assertObjectBody(t, srv, "cond-bucket", "doc.txt", "first")
}

// ---- PutObject / versioned buckets ------------------------------------------

func TestPutObject_ifNoneMatchStarOnVersionedBucketWithCurrentVersion(t *testing.T) {
	// Given: a versioning-enabled bucket holding a current version of the key
	srv := helpers.NewTestServer(t)
	createBucket(t, srv, "cond-versioned")
	enableVersioning(t, srv, "cond-versioned")
	putVersioned(t, srv, "cond-versioned", "lock", []byte("first"))

	// When: a conditional write targets the same key
	resp := putConditional(t, srv, "cond-versioned", "lock", []byte("second"), map[string]string{
		"If-None-Match": "*",
	})
	defer resp.Body.Close()

	// Then: the current version is what the condition sees, so it fails
	helpers.AssertStatus(t, resp, http.StatusPreconditionFailed)
	helpers.AssertXMLError(t, resp, "PreconditionFailed")
	assertObjectBody(t, srv, "cond-versioned", "lock", "first")
}

func TestPutObject_ifNoneMatchStarWhenCurrentVersionIsADeleteMarker(t *testing.T) {
	// Given: a versioned key whose current version is a delete marker
	srv := helpers.NewTestServer(t)
	createBucket(t, srv, "cond-versioned")
	enableVersioning(t, srv, "cond-versioned")
	putVersioned(t, srv, "cond-versioned", "lock", []byte("first"))
	marker := deleteVersion(t, srv, "cond-versioned", "lock", "")
	marker.Body.Close()
	helpers.AssertStatus(t, marker, http.StatusNoContent)

	// When: a conditional write targets the key
	resp := putConditional(t, srv, "cond-versioned", "lock", []byte("second"), map[string]string{
		"If-None-Match": "*",
	})
	defer resp.Body.Close()

	// Then: AWS counts a delete marker as "no current object", so it succeeds
	helpers.AssertStatus(t, resp, http.StatusOK)
	assertObjectBody(t, srv, "cond-versioned", "lock", "second")
}

func TestPutObject_ifMatchWhenCurrentVersionIsADeleteMarker(t *testing.T) {
	// Given: a versioned key whose current version is a delete marker
	srv := helpers.NewTestServer(t)
	createBucket(t, srv, "cond-versioned")
	enableVersioning(t, srv, "cond-versioned")
	putVersioned(t, srv, "cond-versioned", "doc.txt", []byte("first"))
	etag := objectETag(t, srv, "cond-versioned", "doc.txt")
	marker := deleteVersion(t, srv, "cond-versioned", "doc.txt", "")
	marker.Body.Close()
	helpers.AssertStatus(t, marker, http.StatusNoContent)

	// When: a write is guarded by the ETag the deleted version had
	resp := putConditional(t, srv, "cond-versioned", "doc.txt", []byte("second"), map[string]string{
		"If-Match": etag,
	})
	defer resp.Body.Close()

	// Then: AWS answers 404 — there is no current object version to match
	helpers.AssertStatus(t, resp, http.StatusNotFound)
	helpers.AssertXMLError(t, resp, "NoSuchKey")
}

// ---- PutObject / concurrency ------------------------------------------------

func TestPutObject_concurrentIfNoneMatchStarHasOneWinner(t *testing.T) {
	// Given: a bucket with nothing at the contended key
	srv := helpers.NewTestServer(t)
	createBucket(t, srv, "cond-race")

	// When: 16 writers race the same If-None-Match: * write
	const writers = 16
	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		statuses = make(map[int]int, 2)
		winner   string
	)
	wg.Add(writers)
	for i := 0; i < writers; i++ {
		go func(n int) {
			defer wg.Done()
			body := fmt.Sprintf("writer-%d", n)
			req := put(srv, "/cond-race/lock", []byte(body), map[string]string{
				"Content-Type":  "text/plain",
				"If-None-Match": "*",
			})
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Errorf("writer %d: %v", n, err)
				return
			}
			resp.Body.Close()
			mu.Lock()
			defer mu.Unlock()
			statuses[resp.StatusCode]++
			if resp.StatusCode == http.StatusOK {
				winner = body
			}
		}(i)
	}
	wg.Wait()

	// Then: exactly one wins, everyone else gets 412, and the stored object is
	// the winner's — mutual exclusion is the whole point of the primitive
	if statuses[http.StatusOK] != 1 {
		t.Fatalf("expected exactly 1 winner, got statuses %v", statuses)
	}
	if statuses[http.StatusPreconditionFailed] != writers-1 {
		t.Fatalf("expected %d losers with 412, got statuses %v", writers-1, statuses)
	}
	assertObjectBody(t, srv, "cond-race", "lock", winner)
}

// ---- CompleteMultipartUpload ------------------------------------------------

func TestCompleteMultipartUpload_ifNoneMatchStarOnAbsentKey(t *testing.T) {
	// Given: an upload whose parts are in place and no object at the key
	srv := helpers.NewTestServer(t)
	createBucket(t, srv, "cond-mp")
	uploadID, parts := stagedUpload(t, srv, "cond-mp", "big.bin", []byte("hello, world!"))

	// When: it completes guarded by If-None-Match: *
	resp := completeConditional(t, srv, "cond-mp", "big.bin", uploadID, parts, map[string]string{
		"If-None-Match": "*",
	})
	defer resp.Body.Close()

	// Then: the upload completes
	helpers.AssertStatus(t, resp, http.StatusOK)
	assertObjectBody(t, srv, "cond-mp", "big.bin", "hello, world!")
}

func TestCompleteMultipartUpload_ifNoneMatchStarOnExistingKey(t *testing.T) {
	// Given: an upload whose key was claimed by a plain PutObject meanwhile
	srv := helpers.NewTestServer(t)
	createBucket(t, srv, "cond-mp")
	uploadID, parts := stagedUpload(t, srv, "cond-mp", "big.bin", []byte("hello, world!"))
	putObject(t, srv, "cond-mp", "big.bin", []byte("claimed"), "text/plain")

	// When: it completes guarded by If-None-Match: *
	resp := completeConditional(t, srv, "cond-mp", "big.bin", uploadID, parts, map[string]string{
		"If-None-Match": "*",
	})
	defer resp.Body.Close()

	// Then: the completion is refused and the claiming object stands
	helpers.AssertStatus(t, resp, http.StatusPreconditionFailed)
	helpers.AssertXMLError(t, resp, "PreconditionFailed")
	assertObjectBody(t, srv, "cond-mp", "big.bin", "claimed")
}

func TestCompleteMultipartUpload_ifMatchOnStaleETag(t *testing.T) {
	// Given: an upload over a key whose object changed after the ETag was read
	srv := helpers.NewTestServer(t)
	createBucket(t, srv, "cond-mp")
	putObject(t, srv, "cond-mp", "big.bin", []byte("first"), "text/plain")
	stale := objectETag(t, srv, "cond-mp", "big.bin")
	uploadID, parts := stagedUpload(t, srv, "cond-mp", "big.bin", []byte("hello, world!"))
	putObject(t, srv, "cond-mp", "big.bin", []byte("second"), "text/plain")

	// When: it completes guarded by the stale ETag
	resp := completeConditional(t, srv, "cond-mp", "big.bin", uploadID, parts, map[string]string{
		"If-Match": stale,
	})
	defer resp.Body.Close()

	// Then: it is refused and the current object stands
	helpers.AssertStatus(t, resp, http.StatusPreconditionFailed)
	helpers.AssertXMLError(t, resp, "PreconditionFailed")
	assertObjectBody(t, srv, "cond-mp", "big.bin", "second")
}

func TestCompleteMultipartUpload_ifMatchOnAbsentKey(t *testing.T) {
	// Given: an upload over a key that holds no object
	srv := helpers.NewTestServer(t)
	createBucket(t, srv, "cond-mp")
	uploadID, parts := stagedUpload(t, srv, "cond-mp", "big.bin", []byte("hello, world!"))

	// When: it completes guarded by an ETag
	resp := completeConditional(t, srv, "cond-mp", "big.bin", uploadID, parts, map[string]string{
		"If-Match": `"d41d8cd98f00b204e9800998ecf8427e"`,
	})
	defer resp.Body.Close()

	// Then: AWS answers 404 — there is no current object version to match
	helpers.AssertStatus(t, resp, http.StatusNotFound)
	helpers.AssertXMLError(t, resp, "NoSuchKey")
}

func TestCompleteMultipartUpload_ifNoneMatchWithAnETagValue(t *testing.T) {
	// Given: a staged upload
	srv := helpers.NewTestServer(t)
	createBucket(t, srv, "cond-mp")
	uploadID, parts := stagedUpload(t, srv, "cond-mp", "big.bin", []byte("hello, world!"))

	// When: it completes with an ETag rather than the '*' AWS documents
	resp := completeConditional(t, srv, "cond-mp", "big.bin", uploadID, parts, map[string]string{
		"If-None-Match": `"d41d8cd98f00b204e9800998ecf8427e"`,
	})
	defer resp.Body.Close()

	// Then: the header is refused rather than ignored, and no object appears
	helpers.AssertStatus(t, resp, http.StatusNotImplemented)
	helpers.AssertXMLError(t, resp, "NotImplemented")

	missing, err := http.DefaultClient.Do(get(srv, "/cond-mp/big.bin"))
	if err != nil {
		t.Fatal(err)
	}
	defer missing.Body.Close()
	helpers.AssertStatus(t, missing, http.StatusNotFound)
}

// ---- Local helpers ----------------------------------------------------------

// putConditional writes an object with extra request headers and returns the
// raw response, so a test can assert on a refusal as well as a success.
func putConditional(t *testing.T, srv *helpers.TestServer, bucket, key string, body []byte, headers map[string]string) *http.Response {
	t.Helper()
	all := map[string]string{"Content-Type": "text/plain"}
	for k, v := range headers {
		all[k] = v
	}
	resp, err := http.DefaultClient.Do(put(srv, "/"+bucket+"/"+key, body, all))
	if err != nil {
		t.Fatalf("PutObject %s/%s: %v", bucket, key, err)
	}
	return resp
}

// objectETag returns the ETag HeadObject reports for a key.
func objectETag(t *testing.T, srv *helpers.TestServer, bucket, key string) string {
	t.Helper()
	resp, err := http.DefaultClient.Do(head(srv, "/"+bucket+"/"+key))
	if err != nil {
		t.Fatalf("HeadObject %s/%s: %v", bucket, key, err)
	}
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	etag := resp.Header.Get("ETag")
	if etag == "" {
		t.Fatalf("HeadObject %s/%s: expected an ETag", bucket, key)
	}
	return etag
}

// assertObjectBody reads the key's current version and compares it to want.
func assertObjectBody(t *testing.T, srv *helpers.TestServer, bucket, key, want string) {
	t.Helper()
	resp, err := http.DefaultClient.Do(get(srv, "/"+bucket+"/"+key))
	if err != nil {
		t.Fatalf("GetObject %s/%s: %v", bucket, key, err)
	}
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	assertBody(t, resp, want)
}

// completedPart mirrors one <Part> entry of a CompleteMultipartUpload body.
type completedPart struct {
	PartNumber int
	ETag       string
}

// stagedUpload initiates a multipart upload and uploads one part per body,
// returning the upload id and the parts list a completion needs.
func stagedUpload(t *testing.T, srv *helpers.TestServer, bucket, key string, bodies ...[]byte) (string, []completedPart) {
	t.Helper()
	uploadID := createMultipartUpload(t, srv, bucket, key)
	parts := make([]completedPart, 0, len(bodies))
	for i, body := range bodies {
		num := i + 1
		parts = append(parts, completedPart{PartNumber: num, ETag: uploadPart(t, srv, bucket, key, uploadID, num, body)})
	}
	return uploadID, parts
}

// completeConditional completes a multipart upload with extra request headers
// and returns the raw response.
func completeConditional(t *testing.T, srv *helpers.TestServer, bucket, key, uploadID string, parts []completedPart, headers map[string]string) *http.Response {
	t.Helper()
	var sb strings.Builder
	sb.WriteString("<CompleteMultipartUpload>")
	for _, p := range parts {
		fmt.Fprintf(&sb, "<Part><PartNumber>%d</PartNumber><ETag>%s</ETag></Part>", p.PartNumber, p.ETag)
	}
	sb.WriteString("</CompleteMultipartUpload>")

	all := map[string]string{"Content-Type": "application/xml"}
	for k, v := range headers {
		all[k] = v
	}
	path := fmt.Sprintf("%s/%s/%s?uploadId=%s", srv.URL, bucket, key, uploadID)
	resp, err := http.DefaultClient.Do(mustReq(http.MethodPost, path, strings.NewReader(sb.String()), all))
	if err != nil {
		t.Fatalf("CompleteMultipartUpload %s/%s: %v", bucket, key, err)
	}
	return resp
}
