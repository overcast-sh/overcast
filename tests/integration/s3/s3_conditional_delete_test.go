package s3_test

// s3_conditional_delete_test.go covers S3's conditional deletes — the
// If-Match header on DeleteObject and the ETag field on DeleteObjects (#2037).
//
// AWS reference for every expectation asserted here:
//   https://docs.aws.amazon.com/AmazonS3/latest/API/API_DeleteObject.html
//   https://docs.aws.amazon.com/AmazonS3/latest/API/API_DeleteObjects.html
//   https://docs.aws.amazon.com/AmazonS3/latest/userguide/WhatsNew.html
//     (entry dated September 16, 2025: "Amazon S3 now supports conditional
//     deletes in general purpose buckets")
//
// DeleteObject's If-Match documents a mismatch as 412 PreconditionFailed, a
// literal '*' as matching any ETag, and — by the same "no current version"
// rule the conditional-writes guide states for PUT's If-Match — a missing key
// (or a key whose current version is already a delete marker) as 404
// NoSuchKey rather than DeleteObject's usual idempotent 204. DeleteObjects
// carries the same condition per key, in the <Object> entry's <ETag>, and a
// mismatch on one key reports a per-key <Error> with code PreconditionFailed
// while the rest of the batch proceeds.
//
// x-amz-if-match-last-modified-time and x-amz-if-match-size (and their
// DeleteObjects counterparts, LastModifiedTime and Size on <Object>) are
// documented as directory-buckets-only; Overcast does not emulate directory
// buckets, so only If-Match/ETag is covered here.

import (
	"net/http"
	"strings"
	"testing"

	"github.com/overcast-sh/overcast/tests/helpers"
)

// ---- DeleteObject / If-Match, unversioned bucket ---------------------------

func TestDeleteObject_ifMatchMatchingEtagDeletes(t *testing.T) {
	// Given: an object with a known ETag
	srv := helpers.NewTestServer(t)
	createBucket(t, srv, "cond-del")
	putObject(t, srv, "cond-del", "a.txt", []byte("hello"), "text/plain")
	etag := objectETag(t, srv, "cond-del", "a.txt")

	// When: it is deleted with If-Match set to that ETag
	resp := deleteConditional(t, srv, "cond-del", "a.txt", map[string]string{"If-Match": etag})
	defer resp.Body.Close()

	// Then: the delete succeeds and the object is gone
	helpers.AssertStatus(t, resp, http.StatusNoContent)
	got, err := http.DefaultClient.Do(get(srv, "/cond-del/a.txt"))
	if err != nil {
		t.Fatal(err)
	}
	got.Body.Close()
	helpers.AssertStatus(t, got, http.StatusNotFound)
}

func TestDeleteObject_ifMatchMismatchedEtagIsPreconditionFailed(t *testing.T) {
	// Given: an object in a bucket
	srv := helpers.NewTestServer(t)
	createBucket(t, srv, "cond-del")
	putObject(t, srv, "cond-del", "a.txt", []byte("hello"), "text/plain")

	// When: it is deleted with an If-Match that does not name its ETag
	resp := deleteConditional(t, srv, "cond-del", "a.txt", map[string]string{"If-Match": `"not-the-etag"`})
	defer resp.Body.Close()

	// Then: 412 PreconditionFailed, and the object is untouched
	helpers.AssertStatus(t, resp, http.StatusPreconditionFailed)
	helpers.AssertXMLError(t, resp, "PreconditionFailed")
	assertObjectBody(t, srv, "cond-del", "a.txt", "hello")
}

func TestDeleteObject_ifMatchOnMissingKeyIsNoSuchKey(t *testing.T) {
	// Given: a bucket with no object at the key
	srv := helpers.NewTestServer(t)
	createBucket(t, srv, "cond-del")

	// When: a conditional delete names a key that was never there
	resp := deleteConditional(t, srv, "cond-del", "never-existed.txt", map[string]string{"If-Match": `"whatever"`})
	defer resp.Body.Close()

	// Then: 404 NoSuchKey — If-Match on a missing key is not the ordinary
	// idempotent 204 an unconditional DeleteObject answers, because there is
	// no current version to compare the ETag against.
	helpers.AssertStatus(t, resp, http.StatusNotFound)
	helpers.AssertXMLError(t, resp, "NoSuchKey")
}

func TestDeleteObject_ifMatchStarMatchesAnyEtag(t *testing.T) {
	// Given: an object in a bucket
	srv := helpers.NewTestServer(t)
	createBucket(t, srv, "cond-del")
	putObject(t, srv, "cond-del", "a.txt", []byte("hello"), "text/plain")

	// When: it is deleted with If-Match: * — AWS documents this as matching
	// any ETag, i.e. "delete it if it currently exists"
	resp := deleteConditional(t, srv, "cond-del", "a.txt", map[string]string{"If-Match": "*"})
	defer resp.Body.Close()

	// Then: the delete succeeds
	helpers.AssertStatus(t, resp, http.StatusNoContent)
}

// ---- DeleteObject / If-Match, versioned bucket -----------------------------

func TestDeleteObject_ifMatchOnVersionedBucketAppliesToCurrentVersion(t *testing.T) {
	// Given: a versioned key with one version
	srv := helpers.NewTestServer(t)
	createBucket(t, srv, "cond-del-ver")
	enableVersioning(t, srv, "cond-del-ver")
	putVersioned(t, srv, "cond-del-ver", "a.txt", []byte("hello"))
	etag := objectETag(t, srv, "cond-del-ver", "a.txt")

	// When: a mismatched conditional delete is sent with no version id
	mismatch := deleteConditional(t, srv, "cond-del-ver", "a.txt", map[string]string{"If-Match": `"not-it"`})
	defer mismatch.Body.Close()

	// Then: 412, and no delete marker was created — the object still reads
	helpers.AssertStatus(t, mismatch, http.StatusPreconditionFailed)
	still := getVersion(t, srv, "cond-del-ver", "a.txt", "")
	defer still.Body.Close()
	helpers.AssertStatus(t, still, http.StatusOK)

	// When: a matching conditional delete follows
	match := deleteConditional(t, srv, "cond-del-ver", "a.txt", map[string]string{"If-Match": etag})
	defer match.Body.Close()

	// Then: it succeeds and creates a delete marker, same as an unconditional
	// delete would
	helpers.AssertStatus(t, match, http.StatusNoContent)
	if match.Header.Get("x-amz-delete-marker") != "true" {
		t.Error("conditional delete on a versioned bucket did not report x-amz-delete-marker: true")
	}
}

func TestDeleteObject_ifMatchWithVersionIdAppliesToThatVersion(t *testing.T) {
	// Given: a versioned key with one version
	srv := helpers.NewTestServer(t)
	createBucket(t, srv, "cond-del-verid")
	enableVersioning(t, srv, "cond-del-verid")
	versionID := putVersioned(t, srv, "cond-del-verid", "a.txt", []byte("hello"))
	etag := objectETag(t, srv, "cond-del-verid", "a.txt")

	// When: that exact version is deleted with a mismatched If-Match
	mismatch := deleteConditionalVersion(t, srv, "cond-del-verid", "a.txt", versionID,
		map[string]string{"If-Match": `"not-it"`})
	defer mismatch.Body.Close()

	// Then: 412, and the version survives
	helpers.AssertStatus(t, mismatch, http.StatusPreconditionFailed)
	survives := getVersion(t, srv, "cond-del-verid", "a.txt", versionID)
	defer survives.Body.Close()
	helpers.AssertStatus(t, survives, http.StatusOK)

	// When: the same version is deleted with a matching If-Match
	match := deleteConditionalVersion(t, srv, "cond-del-verid", "a.txt", versionID,
		map[string]string{"If-Match": etag})
	defer match.Body.Close()

	// Then: it is permanently removed
	helpers.AssertStatus(t, match, http.StatusNoContent)
	gone := getVersion(t, srv, "cond-del-verid", "a.txt", versionID)
	defer gone.Body.Close()
	helpers.AssertStatus(t, gone, http.StatusNotFound)
}

// ---- DeleteObjects (batch) / per-key ETag ----------------------------------

func TestDeleteObjects_ifMatchPerKeyMismatchReportsErrorAndContinues(t *testing.T) {
	// Given: two objects in a bucket
	srv := helpers.NewTestServer(t)
	createBucket(t, srv, "cond-del-batch")
	putObject(t, srv, "cond-del-batch", "a.txt", []byte("a"), "text/plain")
	putObject(t, srv, "cond-del-batch", "b.txt", []byte("b"), "text/plain")
	etagA := objectETag(t, srv, "cond-del-batch", "a.txt")

	// When: a.txt is deleted with a matching ETag and b.txt with a mismatched
	// one, in the same batch
	body := `<Delete>` +
		`<Object><Key>a.txt</Key><ETag>` + etagA + `</ETag></Object>` +
		`<Object><Key>b.txt</Key><ETag>"not-the-etag"</ETag></Object>` +
		`</Delete>`
	resp := postDelete(t, srv, "cond-del-batch", body)
	defer resp.Body.Close()

	// Then: 200 overall, with a per-key PreconditionFailed error for b.txt
	helpers.AssertStatus(t, resp, http.StatusOK)
	respBody := helpers.ReadBody(t, resp)
	if !containsAll(respBody, "<Key>a.txt</Key>", "<Deleted>") {
		t.Errorf("expected a.txt reported as Deleted, got: %s", respBody)
	}
	if !containsAll(respBody, "<Key>b.txt</Key>", "PreconditionFailed") {
		t.Errorf("expected b.txt reported with a PreconditionFailed error, got: %s", respBody)
	}

	// And: a.txt is gone, b.txt survives
	gotA, err := http.DefaultClient.Do(get(srv, "/cond-del-batch/a.txt"))
	if err != nil {
		t.Fatal(err)
	}
	gotA.Body.Close()
	helpers.AssertStatus(t, gotA, http.StatusNotFound)

	assertObjectBody(t, srv, "cond-del-batch", "b.txt", "b")
}

// ---- Local helpers ----------------------------------------------------------

func deleteConditional(t *testing.T, srv *helpers.TestServer, bucket, key string, headers map[string]string) *http.Response {
	t.Helper()
	resp, err := http.DefaultClient.Do(mustReq(http.MethodDelete, srv.URL+"/"+bucket+"/"+key, nil, headers))
	if err != nil {
		t.Fatalf("DeleteObject %s/%s: %v", bucket, key, err)
	}
	return resp
}

func deleteConditionalVersion(t *testing.T, srv *helpers.TestServer, bucket, key, versionID string, headers map[string]string) *http.Response {
	t.Helper()
	path := srv.URL + "/" + bucket + "/" + key + "?versionId=" + versionID
	resp, err := http.DefaultClient.Do(mustReq(http.MethodDelete, path, nil, headers))
	if err != nil {
		t.Fatalf("DeleteObject %s/%s?versionId=%s: %v", bucket, key, versionID, err)
	}
	return resp
}

func postDelete(t *testing.T, srv *helpers.TestServer, bucket, xmlBody string) *http.Response {
	t.Helper()
	req := mustReq(http.MethodPost, srv.URL+"/"+bucket+"?delete", strings.NewReader(xmlBody),
		map[string]string{"Content-Type": "application/xml"})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("DeleteObjects %s: %v", bucket, err)
	}
	return resp
}

func containsAll(s string, subs ...string) bool {
	for _, sub := range subs {
		if !strings.Contains(s, sub) {
			return false
		}
	}
	return true
}
