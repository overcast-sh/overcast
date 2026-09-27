// Package s3_test — tests for the x-amz-expected-bucket-owner (bucket owner
// condition) guard. See https://docs.aws.amazon.com/AmazonS3/latest/userguide/bucket-owner-condition.html
// and issue #1472.
package s3_test

import (
	"net/http"
	"testing"

	"github.com/overcast-sh/overcast/tests/helpers"
)

const wrongAccountID = "111111111111"

// ---- Mismatch on bucket operations -----------------------------------------

func TestExpectedBucketOwner_mismatch_bucketGetSubresource_denied(t *testing.T) {
	srv := helpers.NewTestServer(t)
	createBucket(t, srv, "my-bucket")

	req := get(srv, "/my-bucket?location")
	req.Header.Set("x-amz-expected-bucket-owner", wrongAccountID)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	helpers.AssertStatus(t, resp, http.StatusForbidden)
	helpers.AssertXMLError(t, resp, "AccessDenied")
}

func TestExpectedBucketOwner_mismatch_listObjectsFallback_denied(t *testing.T) {
	srv := helpers.NewTestServer(t)
	createBucket(t, srv, "my-bucket")

	req := get(srv, "/my-bucket")
	req.Header.Set("x-amz-expected-bucket-owner", wrongAccountID)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	helpers.AssertStatus(t, resp, http.StatusForbidden)
	helpers.AssertXMLError(t, resp, "AccessDenied")
}

func TestExpectedBucketOwner_mismatch_headBucket_denied(t *testing.T) {
	srv := helpers.NewTestServer(t)
	createBucket(t, srv, "my-bucket")

	req := head(srv, "/my-bucket")
	req.Header.Set("x-amz-expected-bucket-owner", wrongAccountID)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	// HeadBucket never has a body (AWS returns headers only), so there is no
	// XML error to decode — just the status and the header AWS also sets.
	helpers.AssertStatus(t, resp, http.StatusForbidden)
}

func TestExpectedBucketOwner_mismatch_deleteBucket_denied(t *testing.T) {
	srv := helpers.NewTestServer(t)
	createBucket(t, srv, "my-bucket")

	req := del(srv, "/my-bucket")
	req.Header.Set("x-amz-expected-bucket-owner", wrongAccountID)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	helpers.AssertStatus(t, resp, http.StatusForbidden)
	helpers.AssertXMLError(t, resp, "AccessDenied")

	// And: the bucket must still exist — the delete must not have happened.
	headResp, err := http.DefaultClient.Do(head(srv, "/my-bucket"))
	if err != nil {
		t.Fatal(err)
	}
	defer headResp.Body.Close()
	helpers.AssertStatus(t, headResp, http.StatusOK)
}

func TestExpectedBucketOwner_mismatch_bucketPostDeleteObjects_denied(t *testing.T) {
	srv := helpers.NewTestServer(t)
	createBucket(t, srv, "my-bucket")
	putObject(t, srv, "my-bucket", "keep-me.txt", []byte("hi"), "text/plain")

	body := []byte(`<?xml version="1.0" encoding="UTF-8"?>
<Delete><Object><Key>keep-me.txt</Key></Object></Delete>`)
	req := put(srv, "/my-bucket?delete", body, map[string]string{"Content-Type": "application/xml"})
	req.Method = http.MethodPost
	req.Header.Set("x-amz-expected-bucket-owner", wrongAccountID)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	helpers.AssertStatus(t, resp, http.StatusForbidden)
	helpers.AssertXMLError(t, resp, "AccessDenied")

	// And: the object must still exist — the bulk delete must not have happened.
	getResp, err := http.DefaultClient.Do(get(srv, "/my-bucket/keep-me.txt"))
	if err != nil {
		t.Fatal(err)
	}
	defer getResp.Body.Close()
	helpers.AssertStatus(t, getResp, http.StatusOK)
}

// ---- Mismatch on object operations -----------------------------------------

func TestExpectedBucketOwner_mismatch_putObject_denied(t *testing.T) {
	srv := helpers.NewTestServer(t)
	createBucket(t, srv, "my-bucket")

	req := put(srv, "/my-bucket/key.txt", []byte("hello"), map[string]string{
		"Content-Type":                "text/plain",
		"x-amz-expected-bucket-owner": wrongAccountID,
	})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	helpers.AssertStatus(t, resp, http.StatusForbidden)
	helpers.AssertXMLError(t, resp, "AccessDenied")

	// And: no object must have been written.
	getResp, err := http.DefaultClient.Do(get(srv, "/my-bucket/key.txt"))
	if err != nil {
		t.Fatal(err)
	}
	defer getResp.Body.Close()
	helpers.AssertStatus(t, getResp, http.StatusNotFound)
}

func TestExpectedBucketOwner_mismatch_getObject_denied(t *testing.T) {
	srv := helpers.NewTestServer(t)
	createBucket(t, srv, "my-bucket")
	putObject(t, srv, "my-bucket", "key.txt", []byte("hello"), "text/plain")

	req := get(srv, "/my-bucket/key.txt")
	req.Header.Set("x-amz-expected-bucket-owner", wrongAccountID)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	helpers.AssertStatus(t, resp, http.StatusForbidden)
	helpers.AssertXMLError(t, resp, "AccessDenied")
}

func TestExpectedBucketOwner_mismatch_headObject_denied(t *testing.T) {
	srv := helpers.NewTestServer(t)
	createBucket(t, srv, "my-bucket")
	putObject(t, srv, "my-bucket", "key.txt", []byte("hello"), "text/plain")

	req := head(srv, "/my-bucket/key.txt")
	req.Header.Set("x-amz-expected-bucket-owner", wrongAccountID)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	helpers.AssertStatus(t, resp, http.StatusForbidden)
}

func TestExpectedBucketOwner_mismatch_deleteObject_denied(t *testing.T) {
	srv := helpers.NewTestServer(t)
	createBucket(t, srv, "my-bucket")
	putObject(t, srv, "my-bucket", "key.txt", []byte("hello"), "text/plain")

	req := del(srv, "/my-bucket/key.txt")
	req.Header.Set("x-amz-expected-bucket-owner", wrongAccountID)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	helpers.AssertStatus(t, resp, http.StatusForbidden)
	helpers.AssertXMLError(t, resp, "AccessDenied")

	// And: the object must still exist.
	getResp, err := http.DefaultClient.Do(get(srv, "/my-bucket/key.txt"))
	if err != nil {
		t.Fatal(err)
	}
	defer getResp.Body.Close()
	helpers.AssertStatus(t, getResp, http.StatusOK)
}

func TestExpectedBucketOwner_mismatch_objectSubresourceTagging_denied(t *testing.T) {
	srv := helpers.NewTestServer(t)
	createBucket(t, srv, "my-bucket")
	putObject(t, srv, "my-bucket", "key.txt", []byte("hello"), "text/plain")

	body := []byte(`<?xml version="1.0" encoding="UTF-8"?>
<Tagging><TagSet><Tag><Key>a</Key><Value>b</Value></Tag></TagSet></Tagging>`)
	req := put(srv, "/my-bucket/key.txt?tagging", body, map[string]string{
		"Content-Type":                "application/xml",
		"x-amz-expected-bucket-owner": wrongAccountID,
	})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	helpers.AssertStatus(t, resp, http.StatusForbidden)
	helpers.AssertXMLError(t, resp, "AccessDenied")
}

// ---- Matching / absent header change nothing (regression) -----------------

func TestExpectedBucketOwner_match_proceeds(t *testing.T) {
	srv := helpers.NewTestServer(t)
	createBucket(t, srv, "my-bucket")
	putObject(t, srv, "my-bucket", "key.txt", []byte("hello"), "text/plain")

	req := get(srv, "/my-bucket/key.txt")
	req.Header.Set("x-amz-expected-bucket-owner", srv.Config.AccountID)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	helpers.AssertStatus(t, resp, http.StatusOK)
}

func TestExpectedBucketOwner_absent_proceeds(t *testing.T) {
	srv := helpers.NewTestServer(t)
	createBucket(t, srv, "my-bucket")
	putObject(t, srv, "my-bucket", "key.txt", []byte("hello"), "text/plain")

	resp, err := http.DefaultClient.Do(get(srv, "/my-bucket/key.txt"))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	helpers.AssertStatus(t, resp, http.StatusOK)
}

// ---- Exclusions: CreateBucket and ListBuckets ignore the header ------------

func TestExpectedBucketOwner_createBucket_ignoresMismatch(t *testing.T) {
	srv := helpers.NewTestServer(t)

	req := put(srv, "/brand-new-bucket", nil, nil)
	req.Header.Set("x-amz-expected-bucket-owner", wrongAccountID)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	helpers.AssertStatus(t, resp, http.StatusOK)
}

func TestExpectedBucketOwner_listBuckets_ignoresMismatch(t *testing.T) {
	srv := helpers.NewTestServer(t)
	createBucket(t, srv, "my-bucket")

	req := get(srv, "/")
	req.Header.Set("x-amz-expected-bucket-owner", wrongAccountID)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	helpers.AssertStatus(t, resp, http.StatusOK)
}

// ---- Ordering vs NoSuchBucket (unverified against real AWS; see guard) -----

func TestExpectedBucketOwner_missingBucket_returnsNoSuchBucketNotAccessDenied(t *testing.T) {
	srv := helpers.NewTestServer(t)

	req := get(srv, "/does-not-exist?location")
	req.Header.Set("x-amz-expected-bucket-owner", wrongAccountID)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	helpers.AssertStatus(t, resp, http.StatusNotFound)
	helpers.AssertXMLError(t, resp, "NoSuchBucket")
}

// ---- Malformed header value (unverified against real AWS; see guard) ------

func TestExpectedBucketOwner_malformedValue_deniedAsAccessDenied(t *testing.T) {
	srv := helpers.NewTestServer(t)
	createBucket(t, srv, "my-bucket")

	req := get(srv, "/my-bucket?location")
	req.Header.Set("x-amz-expected-bucket-owner", "not-an-account-id")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	helpers.AssertStatus(t, resp, http.StatusForbidden)
	helpers.AssertXMLError(t, resp, "AccessDenied")
}

// ---- x-amz-source-expected-bucket-owner (CopyObject) -----------------------

func TestExpectedSourceBucketOwner_mismatch_copyObject_denied(t *testing.T) {
	srv := helpers.NewTestServer(t)
	createBucket(t, srv, "src-bucket")
	createBucket(t, srv, "dst-bucket")
	putObject(t, srv, "src-bucket", "key.txt", []byte("hello"), "text/plain")

	req := put(srv, "/dst-bucket/copy.txt", nil, map[string]string{
		"x-amz-copy-source":                  "/src-bucket/key.txt",
		"x-amz-source-expected-bucket-owner": wrongAccountID,
	})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	helpers.AssertStatus(t, resp, http.StatusForbidden)
	helpers.AssertXMLError(t, resp, "AccessDenied")

	// And: no copy must have landed in the destination bucket.
	getResp, err := http.DefaultClient.Do(get(srv, "/dst-bucket/copy.txt"))
	if err != nil {
		t.Fatal(err)
	}
	defer getResp.Body.Close()
	helpers.AssertStatus(t, getResp, http.StatusNotFound)
}

func TestExpectedSourceBucketOwner_match_copyObjectProceeds(t *testing.T) {
	srv := helpers.NewTestServer(t)
	createBucket(t, srv, "src-bucket")
	createBucket(t, srv, "dst-bucket")
	putObject(t, srv, "src-bucket", "key.txt", []byte("hello"), "text/plain")

	req := put(srv, "/dst-bucket/copy.txt", nil, map[string]string{
		"x-amz-copy-source":                  "/src-bucket/key.txt",
		"x-amz-source-expected-bucket-owner": srv.Config.AccountID,
	})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	helpers.AssertStatus(t, resp, http.StatusOK)
}

func TestExpectedSourceBucketOwner_ignoredOffACopy(t *testing.T) {
	// Given: an object
	srv := helpers.NewTestServer(t)
	createBucket(t, srv, "dst-bucket")
	putObject(t, srv, "dst-bucket", "key.txt", []byte("hello"), "text/plain")

	// When: its tags are set by a request that also carries a copy source and
	// a mismatched source owner, neither of which PutObjectTagging reads
	req := put(srv, "/dst-bucket/key.txt?tagging", []byte(`<Tagging><TagSet><Tag><Key>k</Key><Value>v</Value></Tag></TagSet></Tagging>`), map[string]string{
		"x-amz-copy-source":                  "/src-bucket/key.txt",
		"x-amz-source-expected-bucket-owner": wrongAccountID,
	})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	// Then: the source owner is checked only by a copy, so the tags are set
	helpers.AssertStatus(t, resp, http.StatusOK)
}

// TestExpectedSourceBucketOwner_destinationMismatch_deniedBeforeSourceRead
// verifies the destination-bucket expected-owner header (which the shared
// dispatch-entry guard, Handler.dispatch, also enforces) still applies to
// CopyObject requests, independent of the source-owner header.
func TestExpectedBucketOwner_destinationMismatch_copyObjectDenied(t *testing.T) {
	srv := helpers.NewTestServer(t)
	createBucket(t, srv, "src-bucket")
	createBucket(t, srv, "dst-bucket")
	putObject(t, srv, "src-bucket", "key.txt", []byte("hello"), "text/plain")

	req := put(srv, "/dst-bucket/copy.txt", nil, map[string]string{
		"x-amz-copy-source":           "/src-bucket/key.txt",
		"x-amz-expected-bucket-owner": wrongAccountID,
	})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	helpers.AssertStatus(t, resp, http.StatusForbidden)
	helpers.AssertXMLError(t, resp, "AccessDenied")
}
