// Package s3_test contains integration tests for the S3 service emulator.
//
// TDD contract: every test in this file must pass before the corresponding
// handler code in internal/services/s3/ is considered complete.
// New handlers MUST have a failing test written here first.
//
// Run: go test ./tests/integration/s3/...
// Run with race detector: go test -race ./tests/integration/s3/...
package s3_test

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/overcast-sh/overcast/tests/helpers"
)

// ---- CreateBucket ----------------------------------------------------------

func TestCreateBucket_success(t *testing.T) {
	srv := helpers.NewTestServer(t)

	resp, err := http.DefaultClient.Do(put(srv, "/test-bucket", nil, nil))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	helpers.AssertStatus(t, resp, http.StatusOK)
	helpers.AssertRequestID(t, resp)

	if loc := resp.Header.Get("Location"); loc != "/test-bucket" {
		t.Errorf("expected Location: /test-bucket, got %q", loc)
	}
}

func TestCreateBucket_alreadyExistsInUSEast1IsOK(t *testing.T) {
	// Given: a bucket this account already owns, in us-east-1
	srv := helpers.NewTestServer(t)
	createBucket(t, srv, "my-bucket")

	// When: it is created again
	resp, err := http.DefaultClient.Do(put(srv, "/my-bucket", nil, nil))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	// Then: 200, not a conflict. S3 documents BucketAlreadyOwnedByYou as
	// returned "in all AWS Regions except in the North Virginia Region. For
	// legacy compatibility, if you re-create an existing bucket that you
	// already own in the North Virginia Region, Amazon S3 returns 200 OK".
	// us-east-1 is the default region, so a suite that creates its bucket
	// idempotently — as the dotnet and rust compat suites do — hits this on
	// every run.
	helpers.AssertStatus(t, resp, http.StatusOK)
	if loc := resp.Header.Get("Location"); loc != "/my-bucket" {
		t.Errorf("Location = %q, want /my-bucket", loc)
	}
}

// sigV4For builds an Authorization header that targets a region, which is how
// the request region reaches the handler in these tests.
func sigV4For(region string) string {
	return "AWS4-HMAC-SHA256 Credential=test/20250101/" + region +
		"/s3/aws4_request, SignedHeaders=host, Signature=fake"
}

func TestCreateBucket_alreadyExistsOutsideUSEast1Conflicts(t *testing.T) {
	// Given: a bucket this account already owns, in a region that is not
	// North Virginia
	srv := helpers.NewTestServer(t)
	region := "eu-west-1"
	req := put(srv, "/my-bucket", nil, nil)
	req.Header.Set("Authorization", sigV4For(region))
	first, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	first.Body.Close()

	// When: it is created again in that same region
	req = put(srv, "/my-bucket", nil, nil)
	req.Header.Set("Authorization", sigV4For(region))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	// Then: the documented conflict, which every region but us-east-1 returns
	helpers.AssertStatus(t, resp, http.StatusConflict)
	helpers.AssertXMLError(t, resp, "BucketAlreadyOwnedByYou")
}

func TestCreateBucket_nameTooShort(t *testing.T) {
	// Given: a two-character name, one under S3's three-character minimum
	srv := helpers.NewTestServer(t)

	// When: it is created
	resp, err := http.DefaultClient.Do(put(srv, "/ab", nil, nil))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	// Then: the code S3's documented error list gives for a name that breaks
	// the naming rules. CreateBucket used to answer InvalidArgument here, and
	// this assertion was the reason the handler overrode serviceutil's code.
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertXMLError(t, resp, "InvalidBucketName")
}

// TestCreateBucket_invalidName_returnsInvalidBucketName covers every branch of
// serviceutil.BucketName over the wire, not just the length one the suite used
// to reach: each has its own message, and each has to arrive with the code S3
// documents rather than the InvalidArgument this handler once rewrote them to.
// https://docs.aws.amazon.com/AmazonS3/latest/userguide/bucketnamingrules.html
func TestCreateBucket_invalidName_returnsInvalidBucketName(t *testing.T) {
	srv := helpers.NewTestServer(t)

	for _, tc := range []struct {
		name string
		why  string
	}{
		{name: "ab", why: "shorter than the 3-character minimum"},
		{name: strings.Repeat("a", 64), why: "longer than the 63-character maximum"},
		{name: "MyBucket", why: "uppercase is outside the permitted charset"},
		{name: "my_bucket", why: "underscore is outside the permitted charset"},
		{name: "-my-bucket", why: "must begin with a letter or number"},
		{name: "my-bucket-", why: "must end with a letter or number"},
		{name: "example..com", why: "two adjacent periods"},
		{name: "192.168.5.4", why: "formatted as an IP address"},
		{name: "xn--my-bucket", why: "reserved prefix xn--"},
		{name: "sthree-my-bucket", why: "reserved prefix sthree-"},
		{name: "amzn-s3-demo-my-bucket", why: "reserved prefix amzn-s3-demo-"},
		{name: "my-bucket-s3alias", why: "reserved suffix -s3alias"},
		{name: "my-bucket--ol-s3", why: "reserved suffix --ol-s3"},
		{name: "my-bucket.mrap", why: "reserved suffix .mrap"},
		{name: "my-bucket--x-s3", why: "reserved suffix --x-s3"},
		{name: "my-bucket--table-s3", why: "reserved suffix --table-s3"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Given: a bucket name AWS rejects (tc.why)
			// When: it is created in the default global namespace
			resp, err := http.DefaultClient.Do(put(srv, "/"+tc.name, nil, nil))
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()

			// Then: 400 InvalidBucketName
			helpers.AssertStatus(t, resp, http.StatusBadRequest)
			helpers.AssertXMLError(t, resp, "InvalidBucketName")
		})
	}
}

// ---- CreateBucket: account regional namespaces (issue #1471) --------------
//
// Default test server account is 000000000000, default region us-east-1
// (see tests/helpers/server.go defaultTestConfig), so a well-formed
// account-regional name here always ends "-000000000000-us-east-1-an".

func TestCreateBucket_accountRegional_success(t *testing.T) {
	srv := helpers.NewTestServer(t)

	resp, err := http.DefaultClient.Do(put(srv, "/amzn-app-000000000000-us-east-1-an", nil, map[string]string{
		"X-Amz-Bucket-Namespace": "account-regional",
	}))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	helpers.AssertStatus(t, resp, http.StatusOK)
	if loc := resp.Header.Get("Location"); loc != "/amzn-app-000000000000-us-east-1-an" {
		t.Errorf("Location = %q, want /amzn-app-000000000000-us-east-1-an", loc)
	}
}

func TestCreateBucket_accountRegional_reCreateConflictsEvenInUSEast1(t *testing.T) {
	// Given: an account-regional bucket already created in us-east-1 — the
	// region where a *global*-namespace re-create would be a legacy 200 OK.
	srv := helpers.NewTestServer(t)
	const name = "amzn-app-000000000000-us-east-1-an"
	first, err := http.DefaultClient.Do(put(srv, "/"+name, nil, map[string]string{
		"X-Amz-Bucket-Namespace": "account-regional",
	}))
	if err != nil {
		t.Fatal(err)
	}
	first.Body.Close()
	helpers.AssertStatus(t, first, http.StatusOK)

	// When: it is created again, still account-regional, still us-east-1
	resp, err := http.DefaultClient.Do(put(srv, "/"+name, nil, map[string]string{
		"X-Amz-Bucket-Namespace": "account-regional",
	}))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	// Then: 409 BucketAlreadyOwnedByYou, unlike the global-namespace legacy
	// us-east-1 200-OK re-create.
	helpers.AssertStatus(t, resp, http.StatusConflict)
	helpers.AssertXMLError(t, resp, "BucketAlreadyOwnedByYou")
}

func TestCreateBucket_global_reservedANSuffixRejected(t *testing.T) {
	// Given: no x-amz-bucket-namespace header (global namespace, the default)
	srv := helpers.NewTestServer(t)

	// When: the name carries the suffix AWS reserves for the account regional
	// namespace
	resp, err := http.DefaultClient.Do(put(srv, "/some-bucket-000000000000-us-east-1-an", nil, nil))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	// Then: rejected, the same way as the other reserved suffixes — which is
	// also why it carries their code. The exact code AWS uses for this rule is
	// unverified; see the CreateBucket comment.
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertXMLError(t, resp, "InvalidBucketName")
}

func TestCreateBucket_global_explicitHeaderSameAsAbsent(t *testing.T) {
	// x-amz-bucket-namespace: global must behave identically to omitting it —
	// unverified against real AWS, see the header switch in CreateBucket.
	srv := helpers.NewTestServer(t)

	resp, err := http.DefaultClient.Do(put(srv, "/some-bucket-000000000000-us-east-1-an", nil, map[string]string{
		"X-Amz-Bucket-Namespace": "global",
	}))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertXMLError(t, resp, "InvalidBucketName")
}

func TestCreateBucket_accountRegional_wrongAccountRejected(t *testing.T) {
	srv := helpers.NewTestServer(t)

	// A suffix naming an account that is not cfg.AccountID emulates "another
	// account used my suffix".
	resp, err := http.DefaultClient.Do(put(srv, "/amzn-app-111122223333-us-east-1-an", nil, map[string]string{
		"X-Amz-Bucket-Namespace": "account-regional",
	}))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	// The code for a mismatched account or region is unverified against real
	// AWS (see ValidateAccountRegionalBucketName); it follows the base naming
	// rules' InvalidBucketName rather than being given one of its own.
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertXMLError(t, resp, "InvalidBucketName")
}

func TestCreateBucket_accountRegional_wrongRegionRejected(t *testing.T) {
	srv := helpers.NewTestServer(t)

	// Suffix names eu-west-1 while the request itself targets us-east-1
	// (the test server's default, since no region-scoped Authorization
	// header is sent).
	resp, err := http.DefaultClient.Do(put(srv, "/amzn-app-000000000000-eu-west-1-an", nil, map[string]string{
		"X-Amz-Bucket-Namespace": "account-regional",
	}))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertXMLError(t, resp, "InvalidBucketName")
}

func TestCreateBucket_namespaceHeader_invalidValueRejected(t *testing.T) {
	// Exact error code is unverified against real AWS — see CreateBucket.
	// This one stays InvalidArgument while the name checks answer
	// InvalidBucketName: the name here is well-formed and the bad input is a
	// request header, which is what S3 documents InvalidArgument for.
	srv := helpers.NewTestServer(t)

	resp, err := http.DefaultClient.Do(put(srv, "/some-bucket", nil, map[string]string{
		"X-Amz-Bucket-Namespace": "regional",
	}))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertXMLError(t, resp, "InvalidArgument")
}

// ---- HeadBucket ------------------------------------------------------------

func TestHeadBucket_exists(t *testing.T) {
	srv := helpers.NewTestServer(t)
	createBucket(t, srv, "my-bucket")

	resp, err := http.DefaultClient.Do(head(srv, "/my-bucket"))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	helpers.AssertStatus(t, resp, http.StatusOK)
}

func TestHeadBucket_notFound(t *testing.T) {
	srv := helpers.NewTestServer(t)

	resp, err := http.DefaultClient.Do(head(srv, "/no-such-bucket"))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	helpers.AssertStatus(t, resp, http.StatusNotFound)
}

// ---- DeleteBucket ----------------------------------------------------------

func TestDeleteBucket_empty(t *testing.T) {
	srv := helpers.NewTestServer(t)
	createBucket(t, srv, "my-bucket")

	resp, err := http.DefaultClient.Do(del(srv, "/my-bucket"))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	helpers.AssertStatus(t, resp, http.StatusNoContent)
}

func TestDeleteBucket_notEmpty(t *testing.T) {
	srv := helpers.NewTestServer(t)
	createBucket(t, srv, "my-bucket")
	putObject(t, srv, "my-bucket", "key1", []byte("data"), "text/plain")

	resp, err := http.DefaultClient.Do(del(srv, "/my-bucket"))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	helpers.AssertStatus(t, resp, http.StatusConflict)
	helpers.AssertXMLError(t, resp, "BucketNotEmpty")
}

func TestDeleteBucket_notFound(t *testing.T) {
	srv := helpers.NewTestServer(t)

	resp, err := http.DefaultClient.Do(del(srv, "/no-such-bucket"))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	helpers.AssertStatus(t, resp, http.StatusNotFound)
	helpers.AssertXMLError(t, resp, "NoSuchBucket")
}

// ---- PutObject -------------------------------------------------------------

func TestPutObject_success(t *testing.T) {
	srv := helpers.NewTestServer(t)
	createBucket(t, srv, "my-bucket")

	body := []byte("hello world")
	resp, err := http.DefaultClient.Do(put(srv, "/my-bucket/hello.txt", body, map[string]string{
		"Content-Type": "text/plain",
	}))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	helpers.AssertStatus(t, resp, http.StatusOK)
	if etag := resp.Header.Get("ETag"); etag == "" {
		t.Error("expected ETag header to be set")
	}
}

func TestPutObject_noBucket(t *testing.T) {
	srv := helpers.NewTestServer(t)

	resp, err := http.DefaultClient.Do(put(srv, "/no-bucket/key.txt", []byte("data"), nil))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	helpers.AssertStatus(t, resp, http.StatusNotFound)
	helpers.AssertXMLError(t, resp, "NoSuchBucket")
}

func TestPutObject_withMetadata(t *testing.T) {
	srv := helpers.NewTestServer(t)
	createBucket(t, srv, "my-bucket")

	resp, err := http.DefaultClient.Do(put(srv, "/my-bucket/obj.txt", []byte("data"), map[string]string{
		"Content-Type":       "text/plain",
		"X-Amz-Meta-Author":  "test",
		"X-Amz-Meta-Version": "1.0",
	}))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)

	// Verify metadata round-trips via GetObject.
	got, err := http.DefaultClient.Do(get(srv, "/my-bucket/obj.txt"))
	if err != nil {
		t.Fatal(err)
	}
	defer got.Body.Close()
	helpers.AssertStatus(t, got, http.StatusOK)

	if got.Header.Get("X-Amz-Meta-Author") != "test" {
		t.Errorf("expected X-Amz-Meta-Author: test, got %q", got.Header.Get("X-Amz-Meta-Author"))
	}
}

// ---- GetObject -------------------------------------------------------------

func TestGetObject_success(t *testing.T) {
	srv := helpers.NewTestServer(t)
	createBucket(t, srv, "my-bucket")

	content := []byte("the quick brown fox")
	putObject(t, srv, "my-bucket", "fox.txt", content, "text/plain")

	resp, err := http.DefaultClient.Do(get(srv, "/my-bucket/fox.txt"))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	helpers.AssertStatus(t, resp, http.StatusOK)
	if ct := resp.Header.Get("Content-Type"); ct != "text/plain" {
		t.Errorf("expected Content-Type text/plain, got %q", ct)
	}

	body, _ := io.ReadAll(resp.Body)
	if !bytes.Equal(body, content) {
		t.Errorf("expected body %q, got %q", content, body)
	}
}

func TestGetObject_notFound(t *testing.T) {
	srv := helpers.NewTestServer(t)
	createBucket(t, srv, "my-bucket")

	resp, err := http.DefaultClient.Do(get(srv, "/my-bucket/no-such-key.txt"))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	helpers.AssertStatus(t, resp, http.StatusNotFound)
	helpers.AssertXMLError(t, resp, "NoSuchKey")
}

// TestGetObject_missingBucketReturnsNoSuchBucket is the regression test for
// #1635: a GET of any key in a bucket that does not exist must answer
// NoSuchBucket, not NoSuchKey — the unversioned path in readTarget used to
// fall straight through to a composite-key lookup that cannot tell "no such
// bucket" apart from "no such key", so both collapsed to NoSuchKey.
func TestGetObject_missingBucketReturnsNoSuchBucket(t *testing.T) {
	srv := helpers.NewTestServer(t)
	// Given: no bucket named "no-such-bucket" exists at all

	// When: we GET any key from it
	resp, err := http.DefaultClient.Do(get(srv, "/no-such-bucket/key.txt"))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	// Then: NoSuchBucket, not NoSuchKey
	helpers.AssertStatus(t, resp, http.StatusNotFound)
	helpers.AssertXMLError(t, resp, "NoSuchBucket")
}

func TestGetObject_rangeBytes(t *testing.T) {
	srv := helpers.NewTestServer(t)
	createBucket(t, srv, "my-bucket")
	content := []byte("abcdefghijklmnopqrstuvwxyz") // 26 bytes
	putObject(t, srv, "my-bucket", "alpha.txt", content, "text/plain")

	req := get(srv, "/my-bucket/alpha.txt")
	req.Header.Set("Range", "bytes=0-4") // first 5 bytes: "abcde"
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	helpers.AssertStatus(t, resp, http.StatusPartialContent)
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "abcde" {
		t.Errorf("expected 'abcde', got %q", body)
	}
	if cr := resp.Header.Get("Content-Range"); cr != "bytes 0-4/26" {
		t.Errorf("expected Content-Range: bytes 0-4/26, got %q", cr)
	}
	if cl := resp.Header.Get("Content-Length"); cl != "5" {
		t.Errorf("expected Content-Length: 5, got %q", cl)
	}
}

func TestGetObject_rangeSuffix(t *testing.T) {
	srv := helpers.NewTestServer(t)
	createBucket(t, srv, "my-bucket")
	content := []byte("hello world")
	putObject(t, srv, "my-bucket", "msg.txt", content, "text/plain")

	req := get(srv, "/my-bucket/msg.txt")
	req.Header.Set("Range", "bytes=-5") // last 5 bytes: "world"
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	helpers.AssertStatus(t, resp, http.StatusPartialContent)
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "world" {
		t.Errorf("expected 'world', got %q", body)
	}
}

func TestGetObject_rangeOpenEnd(t *testing.T) {
	srv := helpers.NewTestServer(t)
	createBucket(t, srv, "my-bucket")
	content := []byte("hello world")
	putObject(t, srv, "my-bucket", "msg.txt", content, "text/plain")

	req := get(srv, "/my-bucket/msg.txt")
	req.Header.Set("Range", "bytes=6-") // from offset 6 to end: "world"
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	helpers.AssertStatus(t, resp, http.StatusPartialContent)
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "world" {
		t.Errorf("expected 'world', got %q", body)
	}
}

func TestGetObject_rangeUnsatisfiable(t *testing.T) {
	srv := helpers.NewTestServer(t)
	createBucket(t, srv, "my-bucket")
	putObject(t, srv, "my-bucket", "tiny.txt", []byte("hi"), "text/plain")

	req := get(srv, "/my-bucket/tiny.txt")
	req.Header.Set("Range", "bytes=100-200") // beyond end of 2-byte file
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	helpers.AssertStatus(t, resp, http.StatusRequestedRangeNotSatisfiable)
}

func TestGetObject_ifMatch_matches(t *testing.T) {
	srv := helpers.NewTestServer(t)
	createBucket(t, srv, "my-bucket")
	putObject(t, srv, "my-bucket", "doc.txt", []byte("hello"), "text/plain")

	// Get ETag from HeadObject.
	hr, err := http.DefaultClient.Do(head(srv, "/my-bucket/doc.txt"))
	if err != nil {
		t.Fatal(err)
	}
	defer hr.Body.Close()
	etag := hr.Header.Get("ETag")

	req := get(srv, "/my-bucket/doc.txt")
	req.Header.Set("If-Match", etag)
	resp, err2 := http.DefaultClient.Do(req)
	if err2 != nil {
		t.Fatal(err2)
	}
	defer resp.Body.Close()

	helpers.AssertStatus(t, resp, http.StatusOK)
}

func TestGetObject_ifMatch_mismatch(t *testing.T) {
	srv := helpers.NewTestServer(t)
	createBucket(t, srv, "my-bucket")
	putObject(t, srv, "my-bucket", "doc.txt", []byte("hello"), "text/plain")

	req := get(srv, "/my-bucket/doc.txt")
	req.Header.Set("If-Match", `"wrongetag"`)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	helpers.AssertStatus(t, resp, http.StatusPreconditionFailed)
}

func TestGetObject_ifNoneMatch_matches(t *testing.T) {
	srv := helpers.NewTestServer(t)
	createBucket(t, srv, "my-bucket")
	putObject(t, srv, "my-bucket", "doc.txt", []byte("hello"), "text/plain")

	hr, err := http.DefaultClient.Do(head(srv, "/my-bucket/doc.txt"))
	if err != nil {
		t.Fatal(err)
	}
	defer hr.Body.Close()
	etag := hr.Header.Get("ETag")

	// If-None-Match with the current ETag → 304 Not Modified.
	req := get(srv, "/my-bucket/doc.txt")
	req.Header.Set("If-None-Match", etag)
	resp, err2 := http.DefaultClient.Do(req)
	if err2 != nil {
		t.Fatal(err2)
	}
	defer resp.Body.Close()

	helpers.AssertStatus(t, resp, http.StatusNotModified)
}

func TestGetObject_ifNoneMatch_mismatch(t *testing.T) {
	srv := helpers.NewTestServer(t)
	createBucket(t, srv, "my-bucket")
	putObject(t, srv, "my-bucket", "doc.txt", []byte("hello"), "text/plain")

	req := get(srv, "/my-bucket/doc.txt")
	req.Header.Set("If-None-Match", `"different"`)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	helpers.AssertStatus(t, resp, http.StatusOK)
}

// ---- HeadObject ------------------------------------------------------------

func TestHeadObject_success(t *testing.T) {
	srv := helpers.NewTestServer(t)
	createBucket(t, srv, "my-bucket")
	putObject(t, srv, "my-bucket", "doc.pdf", []byte("pdf-data"), "application/pdf")

	resp, err := http.DefaultClient.Do(head(srv, "/my-bucket/doc.pdf"))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	helpers.AssertStatus(t, resp, http.StatusOK)
	if ct := resp.Header.Get("Content-Type"); ct != "application/pdf" {
		t.Errorf("expected Content-Type application/pdf, got %q", ct)
	}
	// HEAD must not return a body.
	body, _ := io.ReadAll(resp.Body)
	if len(body) > 0 {
		t.Errorf("HEAD response must have no body, got %d bytes", len(body))
	}
}

// ---- DeleteObject ----------------------------------------------------------

func TestDeleteObject_success(t *testing.T) {
	srv := helpers.NewTestServer(t)
	createBucket(t, srv, "my-bucket")
	putObject(t, srv, "my-bucket", "gone.txt", []byte("bye"), "text/plain")

	resp, err := http.DefaultClient.Do(del(srv, "/my-bucket/gone.txt"))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusNoContent)

	// Verify object is gone.
	got, err := http.DefaultClient.Do(get(srv, "/my-bucket/gone.txt"))
	if err != nil {
		t.Fatal(err)
	}
	defer got.Body.Close()
	helpers.AssertStatus(t, got, http.StatusNotFound)
}

func TestDeleteObject_nonExistentIsIdempotent(t *testing.T) {
	srv := helpers.NewTestServer(t)
	createBucket(t, srv, "my-bucket")

	// AWS DeleteObject is idempotent — deleting a non-existent key returns 204.
	resp, err := http.DefaultClient.Do(del(srv, "/my-bucket/does-not-exist.txt"))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusNoContent)
}

// ---- DeleteObjects (batch) -------------------------------------------------

func TestDeleteObjects_batchDelete(t *testing.T) {
	// Given three objects in a bucket
	srv := helpers.NewTestServer(t)
	createBucket(t, srv, "batch-bucket")
	putObject(t, srv, "batch-bucket", "a.txt", []byte("a"), "text/plain")
	putObject(t, srv, "batch-bucket", "b.txt", []byte("b"), "text/plain")
	putObject(t, srv, "batch-bucket", "c.txt", []byte("c"), "text/plain")

	// When we batch-delete a.txt and b.txt
	body := `<Delete><Object><Key>a.txt</Key></Object><Object><Key>b.txt</Key></Object></Delete>`
	req := mustReq(http.MethodPost, srv.URL+"/batch-bucket?delete", strings.NewReader(body), map[string]string{
		"Content-Type": "application/xml",
	})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	// Then we get 200 with deleted keys in response
	helpers.AssertStatus(t, resp, http.StatusOK)

	// And a.txt and b.txt are gone
	for _, key := range []string{"a.txt", "b.txt"} {
		got, err := http.DefaultClient.Do(get(srv, "/batch-bucket/"+key))
		if err != nil {
			t.Fatal(err)
		}
		got.Body.Close()
		helpers.AssertStatus(t, got, http.StatusNotFound)
	}

	// But c.txt still exists
	got, err := http.DefaultClient.Do(get(srv, "/batch-bucket/c.txt"))
	if err != nil {
		t.Fatal(err)
	}
	got.Body.Close()
	helpers.AssertStatus(t, got, http.StatusOK)
}

func TestDeleteObjects_quiet(t *testing.T) {
	// Given an object in a bucket
	srv := helpers.NewTestServer(t)
	createBucket(t, srv, "quiet-bucket")
	putObject(t, srv, "quiet-bucket", "x.txt", []byte("x"), "text/plain")

	// When we batch-delete with Quiet=true
	body := `<Delete><Quiet>true</Quiet><Object><Key>x.txt</Key></Object></Delete>`
	req := mustReq(http.MethodPost, srv.URL+"/quiet-bucket?delete", strings.NewReader(body), map[string]string{
		"Content-Type": "application/xml",
	})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	// Then we get 200 and the object is gone
	helpers.AssertStatus(t, resp, http.StatusOK)

	got, err := http.DefaultClient.Do(get(srv, "/quiet-bucket/x.txt"))
	if err != nil {
		t.Fatal(err)
	}
	got.Body.Close()
	helpers.AssertStatus(t, got, http.StatusNotFound)
}

func TestDeleteObjects_bucketNotFound(t *testing.T) {
	srv := helpers.NewTestServer(t)

	body := `<Delete><Object><Key>x.txt</Key></Object></Delete>`
	req := mustReq(http.MethodPost, srv.URL+"/no-such-bucket?delete", strings.NewReader(body), map[string]string{
		"Content-Type": "application/xml",
	})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusNotFound)
}

// ---- ListObjectsV2 ---------------------------------------------------------

func TestListObjectsV2_empty(t *testing.T) {
	srv := helpers.NewTestServer(t)
	createBucket(t, srv, "my-bucket")

	resp, err := http.DefaultClient.Do(get(srv, "/my-bucket?list-type=2"))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		KeyCount int `xml:"KeyCount"`
	}
	helpers.DecodeXML(t, resp, &result)
	if result.KeyCount != 0 {
		t.Errorf("expected KeyCount 0, got %d", result.KeyCount)
	}
}

func TestListObjectsV2_withObjects(t *testing.T) {
	srv := helpers.NewTestServer(t)
	createBucket(t, srv, "my-bucket")

	keys := []string{"a.txt", "b.txt", "c.txt"}
	for _, k := range keys {
		putObject(t, srv, "my-bucket", k, []byte("data"), "text/plain")
	}

	resp, err := http.DefaultClient.Do(get(srv, "/my-bucket?list-type=2"))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		KeyCount int `xml:"KeyCount"`
		Contents []struct {
			Key string `xml:"Key"`
		} `xml:"Contents"`
	}
	helpers.DecodeXML(t, resp, &result)

	if result.KeyCount != len(keys) {
		t.Errorf("expected KeyCount %d, got %d", len(keys), result.KeyCount)
	}
}

func TestListObjectsV2_withPrefix(t *testing.T) {
	srv := helpers.NewTestServer(t)
	createBucket(t, srv, "my-bucket")

	putObject(t, srv, "my-bucket", "logs/2024/jan.log", []byte("data"), "text/plain")
	putObject(t, srv, "my-bucket", "logs/2024/feb.log", []byte("data"), "text/plain")
	putObject(t, srv, "my-bucket", "images/photo.jpg", []byte("data"), "image/jpeg")

	resp, err := http.DefaultClient.Do(get(srv, "/my-bucket?list-type=2&prefix=logs/"))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		KeyCount int `xml:"KeyCount"`
	}
	helpers.DecodeXML(t, resp, &result)

	if result.KeyCount != 2 {
		t.Errorf("expected 2 objects with prefix 'logs/', got %d", result.KeyCount)
	}
}

// ---- CopyObject ------------------------------------------------------------

func TestCopyObject_success(t *testing.T) {
	srv := helpers.NewTestServer(t)
	createBucket(t, srv, "src-bucket")
	createBucket(t, srv, "dst-bucket")
	putObject(t, srv, "src-bucket", "original.txt", []byte("original content"), "text/plain")

	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/dst-bucket/copy.txt", nil)
	req.Header.Set("x-amz-copy-source", "/src-bucket/original.txt")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)

	// Verify the copy is readable.
	got, err := http.DefaultClient.Do(get(srv, "/dst-bucket/copy.txt"))
	if err != nil {
		t.Fatal(err)
	}
	defer got.Body.Close()
	helpers.AssertStatus(t, got, http.StatusOK)
	body, _ := io.ReadAll(got.Body)
	if string(body) != "original content" {
		t.Errorf("expected 'original content', got %q", string(body))
	}
}

func TestCopyObject_urlEncodedSource(t *testing.T) {
	// Given: a source object whose key needs encoding, copied with a
	// URL-encoded x-amz-copy-source. AWS requires that header to be
	// URL-encoded and decodes it; the AWS SDK for .NET encodes the whole
	// value, separator included, which the emulator used to reject as
	// "Invalid copy source" — it split on "/" before decoding, and an encoded
	// separator leaves nothing to split on.
	srv := helpers.NewTestServer(t)
	createBucket(t, srv, "src-bucket")
	createBucket(t, srv, "dst-bucket")
	putObject(t, srv, "src-bucket", "my file.txt", []byte("encoded content"), "text/plain")

	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/dst-bucket/copy.txt", nil)
	req.Header.Set("x-amz-copy-source", "%2Fsrc-bucket%2Fmy%20file.txt")

	// When: the copy is requested
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	// Then: it succeeds and the body came across
	helpers.AssertStatus(t, resp, http.StatusOK)
	got, err := http.DefaultClient.Do(get(srv, "/dst-bucket/copy.txt"))
	if err != nil {
		t.Fatal(err)
	}
	defer got.Body.Close()
	helpers.AssertStatus(t, got, http.StatusOK)
	body, _ := io.ReadAll(got.Body)
	if string(body) != "encoded content" {
		t.Errorf("body = %q, want %q", string(body), "encoded content")
	}
}

func TestCopyObject_encodedKeyKeepsItsSlashes(t *testing.T) {
	// Given: a key containing a slash — the bucket/key separator is the first
	// unencoded one, so a nested key must survive intact
	srv := helpers.NewTestServer(t)
	createBucket(t, srv, "src-bucket")
	createBucket(t, srv, "dst-bucket")
	putObject(t, srv, "src-bucket", "dir/nested.txt", []byte("nested"), "text/plain")

	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/dst-bucket/copy.txt", nil)
	req.Header.Set("x-amz-copy-source", "/src-bucket/dir/nested.txt")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
}

// ---- GetBucketLocation -----------------------------------------------------

func TestGetBucketLocation_success(t *testing.T) {
	srv := helpers.NewTestServer(t, helpers.WithRegion("eu-west-1"))
	createBucket(t, srv, "my-bucket")

	resp, err := http.DefaultClient.Do(get(srv, "/my-bucket?location"))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		LocationConstraint string `xml:",chardata"`
	}
	helpers.DecodeXML(t, resp, &result)
	if result.LocationConstraint != "eu-west-1" {
		t.Errorf("expected location eu-west-1, got %q", result.LocationConstraint)
	}
}

func TestGetBucketLocation_notFound(t *testing.T) {
	srv := helpers.NewTestServer(t)

	resp, err := http.DefaultClient.Do(get(srv, "/no-such-bucket?location"))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	helpers.AssertStatus(t, resp, http.StatusNotFound)
	helpers.AssertXMLError(t, resp, "NoSuchBucket")
}

// ---- Bucket encryption -----------------------------------------------------

func TestGetBucketEncryption_defaultSSES3(t *testing.T) {
	// Given: an existing bucket.
	srv := helpers.NewTestServer(t)
	createBucket(t, srv, "encrypted-bucket")

	// When: CDK/S3 asks for the bucket encryption configuration.
	resp, err := http.DefaultClient.Do(get(srv, "/encrypted-bucket?encryption"))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	// Then: it gets a supported AWS-shaped SSE-S3 configuration instead of a 501.
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		Rules []struct {
			ApplyServerSideEncryptionByDefault struct {
				SSEAlgorithm string `xml:"SSEAlgorithm"`
			} `xml:"ApplyServerSideEncryptionByDefault"`
		} `xml:"Rule"`
	}
	helpers.DecodeXML(t, resp, &result)
	if len(result.Rules) != 1 {
		t.Fatalf("expected 1 encryption rule, got %d", len(result.Rules))
	}
	if got := result.Rules[0].ApplyServerSideEncryptionByDefault.SSEAlgorithm; got != "AES256" {
		t.Fatalf("expected SSEAlgorithm AES256, got %q", got)
	}
}

func TestPutBucketEncryption_roundTrip(t *testing.T) {
	// Given: an existing bucket.
	srv := helpers.NewTestServer(t)
	createBucket(t, srv, "roundtrip-encryption-bucket")

	// When: a bucket encryption configuration is stored.
	body := `<ServerSideEncryptionConfiguration xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><Rule><ApplyServerSideEncryptionByDefault><SSEAlgorithm>aws:kms</SSEAlgorithm><KMSMasterKeyID>alias/test</KMSMasterKeyID></ApplyServerSideEncryptionByDefault><BucketKeyEnabled>true</BucketKeyEnabled></Rule></ServerSideEncryptionConfiguration>`
	req := mustReq(http.MethodPut, srv.URL+"/roundtrip-encryption-bucket?encryption", strings.NewReader(body), map[string]string{"Content-Type": "application/xml"})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)

	// Then: GET returns the stored configuration.
	resp, err = http.DefaultClient.Do(get(srv, "/roundtrip-encryption-bucket?encryption"))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		Rules []struct {
			ApplyServerSideEncryptionByDefault struct {
				SSEAlgorithm   string `xml:"SSEAlgorithm"`
				KMSMasterKeyID string `xml:"KMSMasterKeyID"`
			} `xml:"ApplyServerSideEncryptionByDefault"`
			BucketKeyEnabled bool `xml:"BucketKeyEnabled"`
		} `xml:"Rule"`
	}
	helpers.DecodeXML(t, resp, &result)
	if len(result.Rules) != 1 {
		t.Fatalf("expected 1 encryption rule, got %d", len(result.Rules))
	}
	if got := result.Rules[0].ApplyServerSideEncryptionByDefault.SSEAlgorithm; got != "aws:kms" {
		t.Fatalf("expected SSEAlgorithm aws:kms, got %q", got)
	}
	if got := result.Rules[0].ApplyServerSideEncryptionByDefault.KMSMasterKeyID; got != "alias/test" {
		t.Fatalf("expected KMSMasterKeyID alias/test, got %q", got)
	}
	if !result.Rules[0].BucketKeyEnabled {
		t.Fatal("expected BucketKeyEnabled true")
	}
}

// ---- CopyObject (additional error paths) -----------------------------------

func TestCopyObject_missingSourceKey(t *testing.T) {
	srv := helpers.NewTestServer(t)
	createBucket(t, srv, "src-bucket")
	createBucket(t, srv, "dst-bucket")

	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/dst-bucket/copy.txt", nil)
	req.Header.Set("x-amz-copy-source", "/src-bucket/no-such-key")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusNotFound)
	helpers.AssertXMLError(t, resp, "NoSuchKey")
}

// TestCopyObject_missingSourceBucket is the CopyObject half of the #1635
// regression: the source lookup in CopyObject calls getObjectMeta directly
// (handler_object.go, "Load source metadata only"), with no check that
// srcBucket — which is independent of the already-resolved destination
// bucket — actually exists. A copy-source naming a bucket that was never
// created used to answer NoSuchKey instead of NoSuchBucket.
func TestCopyObject_missingSourceBucket(t *testing.T) {
	srv := helpers.NewTestServer(t)
	createBucket(t, srv, "dst-bucket")
	// Given: "no-such-src-bucket" was never created

	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/dst-bucket/copy.txt", nil)
	req.Header.Set("x-amz-copy-source", "/no-such-src-bucket/key.txt")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusNotFound)
	helpers.AssertXMLError(t, resp, "NoSuchBucket")
}

func TestCopyObject_invalidCopySource(t *testing.T) {
	srv := helpers.NewTestServer(t)
	createBucket(t, srv, "dst-bucket")

	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/dst-bucket/copy.txt", nil)
	req.Header.Set("x-amz-copy-source", "no-slash-at-all") // no bucket/key separator

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertXMLError(t, resp, "InvalidArgument")
}

// ---- HeadObject (bucket not found path) ------------------------------------

// HeadObject shares readTarget's unversioned lookup with GetObject (#1635),
// but AWS documents that a HEAD error is always a bodyless generic status
// code — "It's not possible to retrieve the exact exception of these error
// codes" (HeadObject API reference) — and this codebase sets no
// x-amz-error-code or similar header to work around that. So a missing
// bucket and a missing key in an existing bucket are wire-identical for HEAD
// (404, no body) both before and after the #1635 fix; these two tests pin
// that pair down so the fix's internal bucket-check cannot regress either
// case even though neither response distinguishes the reason.
func TestHeadObject_bucketNotFound(t *testing.T) {
	srv := helpers.NewTestServer(t)

	resp, err := http.DefaultClient.Do(head(srv, "/no-such-bucket/key.txt"))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	helpers.AssertStatus(t, resp, http.StatusNotFound)
}

func TestHeadObject_existingBucketMissingKeyReturnsNotFound(t *testing.T) {
	srv := helpers.NewTestServer(t)
	createBucket(t, srv, "my-bucket")

	resp, err := http.DefaultClient.Do(head(srv, "/my-bucket/no-such-key.txt"))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	helpers.AssertStatus(t, resp, http.StatusNotFound)
}

// ---- DeleteObject (bucket not found path) ----------------------------------

func TestDeleteObject_bucketNotFound(t *testing.T) {
	srv := helpers.NewTestServer(t)

	resp, err := http.DefaultClient.Do(del(srv, "/no-such-bucket/key.txt"))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	helpers.AssertStatus(t, resp, http.StatusNotFound)
	helpers.AssertXMLError(t, resp, "NoSuchBucket")
}

// ---- ListObjectsV2 with delimiter (folder simulation) ----------------------

func TestListObjectsV2_withDelimiter_collapsesPrefixes(t *testing.T) {
	// Given: a bucket with objects at different "folder" depths
	srv := helpers.NewTestServer(t)
	createBucket(t, srv, "my-bucket")
	putObject(t, srv, "my-bucket", "photos/2024/jan.jpg", []byte("data"), "image/jpeg")
	putObject(t, srv, "my-bucket", "photos/2024/feb.jpg", []byte("data"), "image/jpeg")
	putObject(t, srv, "my-bucket", "photos/2025/mar.jpg", []byte("data"), "image/jpeg")
	putObject(t, srv, "my-bucket", "docs/readme.txt", []byte("data"), "text/plain")
	putObject(t, srv, "my-bucket", "root.txt", []byte("data"), "text/plain")

	// When: we list with delimiter=/ and no prefix (root level)
	resp, err := http.DefaultClient.Do(get(srv, "/my-bucket?list-type=2&delimiter=%2F"))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	// Then: we get root.txt as a Content, and photos/ + docs/ as CommonPrefixes
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		Contents []struct {
			Key string `xml:"Key"`
		} `xml:"Contents"`
		CommonPrefixes []struct {
			Prefix string `xml:"Prefix"`
		} `xml:"CommonPrefixes"`
		Delimiter string `xml:"Delimiter"`
	}
	helpers.DecodeXML(t, resp, &result)

	if len(result.Contents) != 1 {
		t.Errorf("expected 1 Content at root, got %d: %v", len(result.Contents), result.Contents)
	}
	if result.Contents[0].Key != "root.txt" {
		t.Errorf("expected root.txt, got %q", result.Contents[0].Key)
	}

	if len(result.CommonPrefixes) != 2 {
		t.Errorf("expected 2 CommonPrefixes (docs/, photos/), got %d: %v", len(result.CommonPrefixes), result.CommonPrefixes)
	}

	prefixes := map[string]bool{}
	for _, cp := range result.CommonPrefixes {
		prefixes[cp.Prefix] = true
	}
	if !prefixes["docs/"] {
		t.Error("expected CommonPrefix docs/")
	}
	if !prefixes["photos/"] {
		t.Error("expected CommonPrefix photos/")
	}

	if result.Delimiter != "/" {
		t.Errorf("expected Delimiter '/', got %q", result.Delimiter)
	}
}

func TestListObjectsV2_withDelimiterAndPrefix_drillsIntoFolder(t *testing.T) {
	// Given: a bucket with objects nested under photos/
	srv := helpers.NewTestServer(t)
	createBucket(t, srv, "my-bucket")
	putObject(t, srv, "my-bucket", "photos/2024/jan.jpg", []byte("data"), "image/jpeg")
	putObject(t, srv, "my-bucket", "photos/2024/feb.jpg", []byte("data"), "image/jpeg")
	putObject(t, srv, "my-bucket", "photos/2025/mar.jpg", []byte("data"), "image/jpeg")
	putObject(t, srv, "my-bucket", "docs/readme.txt", []byte("data"), "text/plain")

	// When: we list with prefix=photos/ and delimiter=/
	resp, err := http.DefaultClient.Do(get(srv, "/my-bucket?list-type=2&prefix=photos%2F&delimiter=%2F"))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	// Then: we get photos/2024/ and photos/2025/ as CommonPrefixes, no Contents
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		Contents []struct {
			Key string `xml:"Key"`
		} `xml:"Contents"`
		CommonPrefixes []struct {
			Prefix string `xml:"Prefix"`
		} `xml:"CommonPrefixes"`
		Prefix string `xml:"Prefix"`
	}
	helpers.DecodeXML(t, resp, &result)

	if len(result.Contents) != 0 {
		t.Errorf("expected 0 Contents, got %d", len(result.Contents))
	}
	if len(result.CommonPrefixes) != 2 {
		t.Errorf("expected 2 CommonPrefixes (photos/2024/, photos/2025/), got %d: %v",
			len(result.CommonPrefixes), result.CommonPrefixes)
	}
	if result.Prefix != "photos/" {
		t.Errorf("expected Prefix 'photos/', got %q", result.Prefix)
	}
}

func TestListObjectsV2_withDelimiterAndPrefix_listsLeafObjects(t *testing.T) {
	// Given: objects inside photos/2024/
	srv := helpers.NewTestServer(t)
	createBucket(t, srv, "my-bucket")
	putObject(t, srv, "my-bucket", "photos/2024/jan.jpg", []byte("data"), "image/jpeg")
	putObject(t, srv, "my-bucket", "photos/2024/feb.jpg", []byte("data"), "image/jpeg")
	putObject(t, srv, "my-bucket", "photos/2025/mar.jpg", []byte("data"), "image/jpeg")

	// When: we list with prefix=photos/2024/ and delimiter=/
	resp, err := http.DefaultClient.Do(get(srv, "/my-bucket?list-type=2&prefix=photos%2F2024%2F&delimiter=%2F"))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	// Then: we get jan.jpg and feb.jpg as Contents, no CommonPrefixes
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		Contents []struct {
			Key string `xml:"Key"`
		} `xml:"Contents"`
		CommonPrefixes []struct {
			Prefix string `xml:"Prefix"`
		} `xml:"CommonPrefixes"`
		KeyCount int `xml:"KeyCount"`
	}
	helpers.DecodeXML(t, resp, &result)

	if len(result.Contents) != 2 {
		t.Errorf("expected 2 Contents (jan.jpg, feb.jpg), got %d", len(result.Contents))
	}
	if len(result.CommonPrefixes) != 0 {
		t.Errorf("expected 0 CommonPrefixes, got %d", len(result.CommonPrefixes))
	}
	if result.KeyCount != 2 {
		t.Errorf("expected KeyCount 2, got %d", result.KeyCount)
	}
}

// ---- ListObjectsV2 bucket not found ----------------------------------------

func TestListObjectsV2_bucketNotFound(t *testing.T) {
	srv := helpers.NewTestServer(t)

	resp, err := http.DefaultClient.Do(get(srv, "/no-such-bucket?list-type=2"))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	helpers.AssertStatus(t, resp, http.StatusNotFound)
	helpers.AssertXMLError(t, resp, "NoSuchBucket")
}

// ---- ListObjectsV2 pagination with continuation tokens ---------------------

func TestListObjectsV2_maxKeys_truncatesResult(t *testing.T) {
	// Given: a bucket with 5 objects
	srv := helpers.NewTestServer(t)
	createBucket(t, srv, "paging-bucket")
	for _, key := range []string{"a.txt", "b.txt", "c.txt", "d.txt", "e.txt"} {
		putObject(t, srv, "paging-bucket", key, []byte("x"), "text/plain")
	}

	// When: we list with max-keys=2
	resp, err := http.DefaultClient.Do(get(srv, "/paging-bucket?list-type=2&max-keys=2"))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	// Then: we get exactly 2 objects, IsTruncated=true, and a NextContinuationToken
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		Contents []struct {
			Key string `xml:"Key"`
		} `xml:"Contents"`
		IsTruncated           bool   `xml:"IsTruncated"`
		NextContinuationToken string `xml:"NextContinuationToken"`
		KeyCount              int    `xml:"KeyCount"`
	}
	helpers.DecodeXML(t, resp, &result)

	if len(result.Contents) != 2 {
		t.Errorf("expected 2 Contents, got %d: %v", len(result.Contents), result.Contents)
	}
	if result.Contents[0].Key != "a.txt" {
		t.Errorf("expected first key a.txt, got %q", result.Contents[0].Key)
	}
	if result.Contents[1].Key != "b.txt" {
		t.Errorf("expected second key b.txt, got %q", result.Contents[1].Key)
	}
	if !result.IsTruncated {
		t.Error("expected IsTruncated=true")
	}
	if result.NextContinuationToken == "" {
		t.Error("expected non-empty NextContinuationToken")
	}
	if result.KeyCount != 2 {
		t.Errorf("expected KeyCount=2, got %d", result.KeyCount)
	}
}

func TestListObjectsV2_continuationToken_resumesFromCorrectKey(t *testing.T) {
	// Given: a bucket with 5 objects
	srv := helpers.NewTestServer(t)
	createBucket(t, srv, "paging-bucket")
	for _, key := range []string{"a.txt", "b.txt", "c.txt", "d.txt", "e.txt"} {
		putObject(t, srv, "paging-bucket", key, []byte("x"), "text/plain")
	}

	// When: we fetch page 1 (max-keys=2),  then page 2 using the returned token
	resp1, err := http.DefaultClient.Do(get(srv, "/paging-bucket?list-type=2&max-keys=2"))
	if err != nil {
		t.Fatal(err)
	}
	defer resp1.Body.Close()

	var page1 struct {
		Contents []struct {
			Key string `xml:"Key"`
		} `xml:"Contents"`
		NextContinuationToken string `xml:"NextContinuationToken"`
	}
	helpers.DecodeXML(t, resp1, &page1)

	token := page1.NextContinuationToken
	if token == "" {
		t.Fatal("expected a NextContinuationToken from page 1")
	}

	resp2, err := http.DefaultClient.Do(get(srv, "/paging-bucket?list-type=2&max-keys=2&continuation-token="+token))
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()

	// Then: page 2 starts from c.txt
	helpers.AssertStatus(t, resp2, http.StatusOK)
	var page2 struct {
		Contents []struct {
			Key string `xml:"Key"`
		} `xml:"Contents"`
		IsTruncated           bool   `xml:"IsTruncated"`
		NextContinuationToken string `xml:"NextContinuationToken"`
	}
	helpers.DecodeXML(t, resp2, &page2)

	if len(page2.Contents) != 2 {
		t.Errorf("expected 2 Contents on page 2, got %d: %v", len(page2.Contents), page2.Contents)
	}
	if page2.Contents[0].Key != "c.txt" {
		t.Errorf("expected first key c.txt on page 2, got %q", page2.Contents[0].Key)
	}
	if page2.Contents[1].Key != "d.txt" {
		t.Errorf("expected second key d.txt on page 2, got %q", page2.Contents[1].Key)
	}
	if !page2.IsTruncated {
		t.Error("expected IsTruncated=true for page 2 (e.txt remains)")
	}
}

func TestListObjectsV2_continuationToken_lastPage_isNotTruncated(t *testing.T) {
	// Given: a bucket with 3 objects
	srv := helpers.NewTestServer(t)
	createBucket(t, srv, "paging-bucket")
	for _, key := range []string{"a.txt", "b.txt", "c.txt"} {
		putObject(t, srv, "paging-bucket", key, []byte("x"), "text/plain")
	}

	// When: we page through with max-keys=2 until the last page
	resp1, err := http.DefaultClient.Do(get(srv, "/paging-bucket?list-type=2&max-keys=2"))
	if err != nil {
		t.Fatal(err)
	}
	defer resp1.Body.Close()
	var page1 struct {
		NextContinuationToken string `xml:"NextContinuationToken"`
	}
	helpers.DecodeXML(t, resp1, &page1)

	resp2, err := http.DefaultClient.Do(get(srv, "/paging-bucket?list-type=2&max-keys=2&continuation-token="+page1.NextContinuationToken))
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()

	// Then: last page has 1 item, IsTruncated=false, no NextContinuationToken
	helpers.AssertStatus(t, resp2, http.StatusOK)
	var page2 struct {
		Contents []struct {
			Key string `xml:"Key"`
		} `xml:"Contents"`
		IsTruncated           bool   `xml:"IsTruncated"`
		NextContinuationToken string `xml:"NextContinuationToken"`
	}
	helpers.DecodeXML(t, resp2, &page2)

	if len(page2.Contents) != 1 {
		t.Errorf("expected 1 Content on last page, got %d: %v", len(page2.Contents), page2.Contents)
	}
	if page2.Contents[0].Key != "c.txt" {
		t.Errorf("expected c.txt on last page, got %q", page2.Contents[0].Key)
	}
	if page2.IsTruncated {
		t.Error("expected IsTruncated=false on last page")
	}
	if page2.NextContinuationToken != "" {
		t.Errorf("expected no NextContinuationToken on last page, got %q", page2.NextContinuationToken)
	}
}

func TestListObjectsV2_startAfter(t *testing.T) {
	// Given: a bucket with lexicographically ordered objects
	srv := helpers.NewTestServer(t)
	createBucket(t, srv, "paging-bucket")
	for _, key := range []string{"a.txt", "b.txt", "c.txt"} {
		putObject(t, srv, "paging-bucket", key, []byte("x"), "text/plain")
	}

	// When: we list after b.txt
	resp, err := http.DefaultClient.Do(get(srv, "/paging-bucket?list-type=2&start-after=b.txt"))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	// Then: AWS returns only later keys and echoes StartAfter.
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		Contents []struct {
			Key string `xml:"Key"`
		} `xml:"Contents"`
		StartAfter string `xml:"StartAfter"`
		KeyCount   int    `xml:"KeyCount"`
	}
	helpers.DecodeXML(t, resp, &result)

	if result.StartAfter != "b.txt" {
		t.Errorf("expected StartAfter b.txt, got %q", result.StartAfter)
	}
	if result.KeyCount != 1 {
		t.Errorf("expected KeyCount=1, got %d", result.KeyCount)
	}
	if len(result.Contents) != 1 || result.Contents[0].Key != "c.txt" {
		t.Fatalf("expected only c.txt after b.txt, got %#v", result.Contents)
	}
}

// TestListObjectsV2_invalidContinuationToken_returnsInvalidArgument pins the
// pagination-plan.md G4 / storage-access-plan.md A6 invalid-token fix: a
// garbled ContinuationToken must be rejected with S3's InvalidArgument
// error, not silently treated as "start from page 1" (which would look like
// a legitimate empty/first page to a paginating client and cause duplicate
// delivery). See https://docs.aws.amazon.com/AmazonS3/latest/API/API_ListObjectsV2.html.
func TestListObjectsV2_invalidContinuationToken_returnsInvalidArgument(t *testing.T) {
	// Given: a bucket with at least one object
	srv := helpers.NewTestServer(t)
	createBucket(t, srv, "bad-token-bucket")
	putObject(t, srv, "bad-token-bucket", "a.txt", []byte("x"), "text/plain")

	// When: we list with a continuation-token that isn't valid base64
	resp, err := http.DefaultClient.Do(get(srv, "/bad-token-bucket?list-type=2&continuation-token=not-valid-base64!!!"))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	// Then: 400 InvalidArgument, not a silent restart from page 1
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertXMLError(t, resp, "InvalidArgument")
}

// TestListObjectsV2_delimiterHeavyBucket_paginationWalkMatchesFullListing is
// storage-access-plan.md A6's equivalence test: buildListPage must loop
// internal ScanPage fetches until it accumulates enough *effective* entries
// (not just enough raw keys), because with a delimiter many keys collapse
// into a single CommonPrefix before a small MaxKeys page fills. This seeds
// far more objects per "folder" than MaxKeys, forcing several internal
// round trips per client-facing page, and asserts that walking small pages
// to completion yields exactly the same CommonPrefixes and Contents as one
// large, untruncated request (the oracle) — same set, no duplicates, no
// gaps.
func TestListObjectsV2_delimiterHeavyBucket_paginationWalkMatchesFullListing(t *testing.T) {
	// Given: 12 "folders" of 30 objects each (360 objects), all collapsing
	// into CommonPrefixes under a root-level delimiter listing — far more
	// objects per folder than the MaxKeys=3 page size used below.
	srv := helpers.NewTestServer(t)
	createBucket(t, srv, "delim-heavy-bucket")
	const numFolders = 12
	const objsPerFolder = 30
	for f := 0; f < numFolders; f++ {
		for o := 0; o < objsPerFolder; o++ {
			key := fmt.Sprintf("folder-%02d/obj-%03d.txt", f, o)
			putObject(t, srv, "delim-heavy-bucket", key, []byte("x"), "text/plain")
		}
	}
	// Also a couple of root-level leaf objects (not collapsed).
	putObject(t, srv, "delim-heavy-bucket", "root-a.txt", []byte("x"), "text/plain")
	putObject(t, srv, "delim-heavy-bucket", "root-b.txt", []byte("x"), "text/plain")

	// Oracle: one request with MaxKeys large enough to return everything
	// in a single, untruncated page.
	oracleResp, err := http.DefaultClient.Do(get(srv, "/delim-heavy-bucket?list-type=2&delimiter=%2F&max-keys=10000"))
	if err != nil {
		t.Fatal(err)
	}
	defer oracleResp.Body.Close()
	helpers.AssertStatus(t, oracleResp, http.StatusOK)

	var oracle struct {
		Contents []struct {
			Key string `xml:"Key"`
		} `xml:"Contents"`
		CommonPrefixes []struct {
			Prefix string `xml:"Prefix"`
		} `xml:"CommonPrefixes"`
		IsTruncated bool `xml:"IsTruncated"`
	}
	helpers.DecodeXML(t, oracleResp, &oracle)
	if oracle.IsTruncated {
		t.Fatal("oracle request should not be truncated (max-keys=10000 over 362 effective entries)")
	}
	wantPrefixes := make([]string, 0, len(oracle.CommonPrefixes))
	for _, cp := range oracle.CommonPrefixes {
		wantPrefixes = append(wantPrefixes, cp.Prefix)
	}
	wantContents := make([]string, 0, len(oracle.Contents))
	for _, c := range oracle.Contents {
		wantContents = append(wantContents, c.Key)
	}
	sort.Strings(wantPrefixes)
	sort.Strings(wantContents)

	if len(wantPrefixes) != numFolders {
		t.Fatalf("oracle: expected %d CommonPrefixes, got %d: %v", numFolders, len(wantPrefixes), wantPrefixes)
	}
	if len(wantContents) != 2 {
		t.Fatalf("oracle: expected 2 root-level Contents, got %d: %v", len(wantContents), wantContents)
	}

	// When: walking with MaxKeys=3 (much smaller than objsPerFolder), so
	// buildListPage must internally loop several ScanPage fetches to
	// collapse each folder's 30 objects into a single effective
	// CommonPrefix entry before a client-facing page can fill.
	var gotPrefixes, gotContents []string
	token := ""
	for pages := 0; ; pages++ {
		if pages > 200 {
			t.Fatal("pagination walk did not terminate within 200 pages")
		}
		reqURL := "/delim-heavy-bucket?list-type=2&delimiter=%2F&max-keys=3"
		if token != "" {
			reqURL += "&continuation-token=" + url.QueryEscape(token)
		}
		resp, err := http.DefaultClient.Do(get(srv, reqURL))
		if err != nil {
			t.Fatal(err)
		}
		var page struct {
			Contents []struct {
				Key string `xml:"Key"`
			} `xml:"Contents"`
			CommonPrefixes []struct {
				Prefix string `xml:"Prefix"`
			} `xml:"CommonPrefixes"`
			IsTruncated           bool   `xml:"IsTruncated"`
			NextContinuationToken string `xml:"NextContinuationToken"`
		}
		helpers.DecodeXML(t, resp, &page)
		resp.Body.Close()

		for _, cp := range page.CommonPrefixes {
			gotPrefixes = append(gotPrefixes, cp.Prefix)
		}
		for _, c := range page.Contents {
			gotContents = append(gotContents, c.Key)
		}

		if !page.IsTruncated {
			break
		}
		if page.NextContinuationToken == "" {
			t.Fatal("IsTruncated=true but NextContinuationToken is empty")
		}
		token = page.NextContinuationToken
	}

	// Then: the paginated walk's union of effective entries equals the
	// oracle's, with no duplicates (each CommonPrefix/Content must appear
	// on exactly one page — seenPrefixes is per-buildListPage-call, i.e.
	// per HTTP request, so a prefix spanning an internal-loop boundary
	// must still be collapsed exactly once per page and never repeated
	// across pages).
	sortedGotPrefixes := append([]string(nil), gotPrefixes...)
	sort.Strings(sortedGotPrefixes)
	sortedGotContents := append([]string(nil), gotContents...)
	sort.Strings(sortedGotContents)

	if !slicesEqual(sortedGotPrefixes, wantPrefixes) {
		t.Fatalf("paginated CommonPrefixes walk mismatch:\n got:  %v\n want: %v", sortedGotPrefixes, wantPrefixes)
	}
	if !slicesEqual(sortedGotContents, wantContents) {
		t.Fatalf("paginated Contents walk mismatch:\n got:  %v\n want: %v", sortedGotContents, wantContents)
	}
}

func slicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// ---- ListObjects v1 --------------------------------------------------------

func TestListObjects_v1_returnsObjects(t *testing.T) {
	// Given: a bucket with three objects
	srv := helpers.NewTestServer(t)
	createBucket(t, srv, "v1-bucket")
	for _, k := range []string{"a.txt", "b.txt", "c.txt"} {
		putObject(t, srv, "v1-bucket", k, []byte("data"), "text/plain")
	}

	// When: ListObjects v1 (no list-type param)
	resp, err := http.DefaultClient.Do(get(srv, "/v1-bucket"))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	// Then: 200 with Contents in v1 XML format (no KeyCount)
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		Name        string `xml:"Name"`
		MaxKeys     int    `xml:"MaxKeys"`
		IsTruncated bool   `xml:"IsTruncated"`
		Contents    []struct {
			Key string `xml:"Key"`
		} `xml:"Contents"`
		// v1 must NOT have KeyCount
		KeyCount *int `xml:"KeyCount"`
	}
	helpers.DecodeXML(t, resp, &result)
	if len(result.Contents) != 3 {
		t.Errorf("expected 3 contents, got %d", len(result.Contents))
	}
	if result.KeyCount != nil {
		t.Errorf("v1 response must not include KeyCount, got %d", *result.KeyCount)
	}
	if result.Name != "v1-bucket" {
		t.Errorf("expected Name=v1-bucket, got %q", result.Name)
	}
}

func TestListObjects_v1_paginatesWithMarker(t *testing.T) {
	// Given: a bucket with 3 objects and max-keys=2
	srv := helpers.NewTestServer(t)
	createBucket(t, srv, "v1-paged")
	for _, k := range []string{"a.txt", "b.txt", "c.txt"} {
		putObject(t, srv, "v1-paged", k, []byte("body"), "text/plain")
	}

	// When: first page
	resp1, err := http.DefaultClient.Do(get(srv, "/v1-paged?max-keys=2"))
	if err != nil {
		t.Fatal(err)
	}
	defer resp1.Body.Close()
	helpers.AssertStatus(t, resp1, http.StatusOK)

	var page1 struct {
		IsTruncated bool   `xml:"IsTruncated"`
		NextMarker  string `xml:"NextMarker"`
		Contents    []struct {
			Key string `xml:"Key"`
		} `xml:"Contents"`
	}
	helpers.DecodeXML(t, resp1, &page1)

	if !page1.IsTruncated {
		t.Fatal("expected IsTruncated=true on first page")
	}
	if len(page1.Contents) != 2 {
		t.Errorf("expected 2 contents on first page, got %d", len(page1.Contents))
	}
	if page1.NextMarker == "" {
		t.Fatal("expected NextMarker on first page")
	}

	// When: second page using NextMarker
	resp2, err := http.DefaultClient.Do(get(srv, "/v1-paged?max-keys=2&marker="+page1.NextMarker))
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	helpers.AssertStatus(t, resp2, http.StatusOK)

	var page2 struct {
		IsTruncated bool `xml:"IsTruncated"`
		Contents    []struct {
			Key string `xml:"Key"`
		} `xml:"Contents"`
	}
	helpers.DecodeXML(t, resp2, &page2)

	if len(page2.Contents) != 1 {
		t.Errorf("expected 1 content on second page, got %d", len(page2.Contents))
	}
	if page2.Contents[0].Key != "c.txt" {
		t.Errorf("expected c.txt, got %q", page2.Contents[0].Key)
	}
	if page2.IsTruncated {
		t.Error("expected IsTruncated=false on last page")
	}
}

func TestListObjects_v1_listType1_alsoUsesV1Format(t *testing.T) {
	// Given: a bucket with one object
	srv := helpers.NewTestServer(t)
	createBucket(t, srv, "lt1-bucket")
	putObject(t, srv, "lt1-bucket", "file.txt", []byte("x"), "text/plain")

	// When: explicit list-type=1
	resp, err := http.DefaultClient.Do(get(srv, "/lt1-bucket?list-type=1"))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	// Then: v1 format (no KeyCount)
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		Contents []struct {
			Key string `xml:"Key"`
		} `xml:"Contents"`
		KeyCount *int `xml:"KeyCount"`
	}
	helpers.DecodeXML(t, resp, &result)
	if len(result.Contents) != 1 {
		t.Errorf("expected 1 content, got %d", len(result.Contents))
	}
	if result.KeyCount != nil {
		t.Errorf("v1 response must not include KeyCount, got %d", *result.KeyCount)
	}
}

// ---- Unimplemented operations return 501, not 404 --------------------------

// ---- Multipart upload ------------------------------------------------------

// createMultipartUpload initiates a multipart upload and returns the uploadId.
func createMultipartUpload(t *testing.T, srv *helpers.TestServer, bucket, key string) string {
	t.Helper()
	req := mustReq(http.MethodPost, srv.URL+"/"+bucket+"/"+key+"?uploads", nil, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("createMultipartUpload: %v", err)
	}
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)

	var result struct {
		UploadId string `xml:"UploadId"`
	}
	helpers.DecodeXML(t, resp, &result)
	if result.UploadId == "" {
		t.Fatal("createMultipartUpload: expected non-empty UploadId")
	}
	return result.UploadId
}

// uploadPart uploads a single part and returns the ETag header.
func uploadPart(t *testing.T, srv *helpers.TestServer, bucket, key, uploadID string, partNum int, body []byte) string {
	t.Helper()
	path := fmt.Sprintf("/%s/%s?partNumber=%d&uploadId=%s", bucket, key, partNum, uploadID)
	req := mustReq(http.MethodPut, srv.URL+path, bytes.NewReader(body), nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("uploadPart %d: %v", partNum, err)
	}
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)

	etag := resp.Header.Get("ETag")
	if etag == "" {
		t.Fatalf("uploadPart %d: expected ETag header", partNum)
	}
	return etag
}

// completeMultipartUpload completes the upload using the given part ETags.
func completeMultipartUpload(t *testing.T, srv *helpers.TestServer, bucket, key, uploadID string, parts []struct {
	PartNumber int
	ETag       string
}) {
	t.Helper()
	var sb strings.Builder
	sb.WriteString("<CompleteMultipartUpload>")
	for _, p := range parts {
		fmt.Fprintf(&sb, "<Part><PartNumber>%d</PartNumber><ETag>%s</ETag></Part>", p.PartNumber, p.ETag)
	}
	sb.WriteString("</CompleteMultipartUpload>")
	path := fmt.Sprintf("/%s/%s?uploadId=%s", bucket, key, uploadID)
	req := mustReq(http.MethodPost, srv.URL+path, strings.NewReader(sb.String()), map[string]string{
		"Content-Type": "application/xml",
	})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("completeMultipartUpload: %v", err)
	}
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
}

func TestMultipartUpload_fullCycle(t *testing.T) {
	// Given a bucket
	srv := helpers.NewTestServer(t)
	createBucket(t, srv, "mp-bucket")

	// Every part but the last must be at least 5 MiB (#1706), so part 1 is
	// exactly that and part 2 is the short tail.
	part1 := bytes.Repeat([]byte("h"), 5*1024*1024)
	part2 := []byte("world!")

	// When: create, upload two parts, complete
	uploadID := createMultipartUpload(t, srv, "mp-bucket", "combined.txt")

	// Verify request ID is present on initiate response
	helpers.AssertRequestID(t, func() *http.Response {
		req := mustReq(http.MethodPost, srv.URL+"/mp-bucket/combined.txt?uploads", nil, nil)
		resp, _ := http.DefaultClient.Do(req)
		resp.Body.Close() // drain second initiation - we just want the header check
		return resp
	}())

	etag1 := uploadPart(t, srv, "mp-bucket", "combined.txt", uploadID, 1, part1)
	etag2 := uploadPart(t, srv, "mp-bucket", "combined.txt", uploadID, 2, part2)

	completeMultipartUpload(t, srv, "mp-bucket", "combined.txt", uploadID, []struct {
		PartNumber int
		ETag       string
	}{
		{1, etag1},
		{2, etag2},
	})

	// Then: GetObject returns concatenated body
	resp, err := http.DefaultClient.Do(get(srv, "/mp-bucket/combined.txt"))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)

	body, _ := io.ReadAll(resp.Body)
	want := append(part1, part2...)
	if !bytes.Equal(body, want) {
		t.Errorf("expected body %q, got %q", want, body)
	}

	// And: the ETag has the multipart suffix -<n>
	etag := resp.Header.Get("ETag")
	if !strings.Contains(etag, "-2") {
		t.Errorf("expected multipart ETag with -2 suffix, got %q", etag)
	}
}

func TestMultipartUpload_createReturnsCorrectXML(t *testing.T) {
	// Given a bucket
	srv := helpers.NewTestServer(t)
	createBucket(t, srv, "xml-bucket")

	// When: create multipart upload
	req := mustReq(http.MethodPost, srv.URL+"/xml-bucket/my/key.bin?uploads", nil, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	helpers.AssertStatus(t, resp, http.StatusOK)

	// Then: response XML has correct Bucket and Key
	var result struct {
		XMLName  interface{} `xml:"InitiateMultipartUploadResult"`
		Bucket   string      `xml:"Bucket"`
		Key      string      `xml:"Key"`
		UploadId string      `xml:"UploadId"`
	}
	helpers.DecodeXML(t, resp, &result)

	if result.Bucket != "xml-bucket" {
		t.Errorf("expected Bucket=xml-bucket, got %q", result.Bucket)
	}
	if result.Key != "my/key.bin" {
		t.Errorf("expected Key=my/key.bin, got %q", result.Key)
	}
	if result.UploadId == "" {
		t.Error("expected non-empty UploadId")
	}
}

func TestMultipartUpload_uploadPartReturnsETag(t *testing.T) {
	// Given an initiated multipart upload
	srv := helpers.NewTestServer(t)
	createBucket(t, srv, "etag-bucket")
	uploadID := createMultipartUpload(t, srv, "etag-bucket", "obj.bin")

	// When: upload part 1
	data := bytes.Repeat([]byte("x"), 512)
	etag := uploadPart(t, srv, "etag-bucket", "obj.bin", uploadID, 1, data)

	// Then: ETag is quoted hex MD5
	if etag == "" {
		t.Error("expected non-empty ETag")
	}
	if !strings.HasPrefix(etag, `"`) || !strings.HasSuffix(etag, `"`) {
		t.Errorf("expected quoted ETag, got %q", etag)
	}
}

func TestMultipartUpload_listParts(t *testing.T) {
	// Given an initiated upload with two parts
	srv := helpers.NewTestServer(t)
	createBucket(t, srv, "lp-bucket")
	uploadID := createMultipartUpload(t, srv, "lp-bucket", "file.bin")
	etag1 := uploadPart(t, srv, "lp-bucket", "file.bin", uploadID, 1, []byte("part-one"))
	etag2 := uploadPart(t, srv, "lp-bucket", "file.bin", uploadID, 2, []byte("part-two"))

	// When: list parts
	path := fmt.Sprintf("/lp-bucket/file.bin?uploadId=%s", uploadID)
	resp, err := http.DefaultClient.Do(get(srv, path))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	// Then: both parts are listed with correct ETag and size
	helpers.AssertStatus(t, resp, http.StatusOK)
	helpers.AssertRequestID(t, resp)

	var result struct {
		Bucket   string `xml:"Bucket"`
		Key      string `xml:"Key"`
		UploadId string `xml:"UploadId"`
		Parts    []struct {
			PartNumber   int    `xml:"PartNumber"`
			ETag         string `xml:"ETag"`
			Size         int64  `xml:"Size"`
			LastModified string `xml:"LastModified"`
		} `xml:"Part"`
	}
	helpers.DecodeXML(t, resp, &result)

	if result.Bucket != "lp-bucket" {
		t.Errorf("expected Bucket lp-bucket, got %q", result.Bucket)
	}
	if result.Key != "file.bin" {
		t.Errorf("expected Key file.bin, got %q", result.Key)
	}
	if result.UploadId != uploadID {
		t.Errorf("expected UploadId %q, got %q", uploadID, result.UploadId)
	}
	if len(result.Parts) != 2 {
		t.Fatalf("expected 2 parts, got %d", len(result.Parts))
	}
	if result.Parts[0].PartNumber != 1 || result.Parts[0].ETag != etag1 {
		t.Errorf("part 1: expected PartNumber=1, ETag=%q; got PartNumber=%d, ETag=%q",
			etag1, result.Parts[0].PartNumber, result.Parts[0].ETag)
	}
	if result.Parts[1].PartNumber != 2 || result.Parts[1].ETag != etag2 {
		t.Errorf("part 2: expected PartNumber=2, ETag=%q; got PartNumber=%d, ETag=%q",
			etag2, result.Parts[1].PartNumber, result.Parts[1].ETag)
	}
	if result.Parts[0].Size != int64(len("part-one")) {
		t.Errorf("part 1: expected size %d, got %d", len("part-one"), result.Parts[0].Size)
	}
}

func TestMultipartUpload_listMultipartUploads(t *testing.T) {
	// Given a bucket with two initiated uploads
	srv := helpers.NewTestServer(t)
	createBucket(t, srv, "lmu-bucket")
	id1 := createMultipartUpload(t, srv, "lmu-bucket", "file-a.bin")
	id2 := createMultipartUpload(t, srv, "lmu-bucket", "file-b.bin")

	// When: list multipart uploads
	resp, err := http.DefaultClient.Do(get(srv, "/lmu-bucket?uploads"))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	// Then: both uploads appear
	helpers.AssertStatus(t, resp, http.StatusOK)
	helpers.AssertRequestID(t, resp)

	var result struct {
		Bucket  string `xml:"Bucket"`
		Uploads []struct {
			UploadId string `xml:"UploadId"`
			Key      string `xml:"Key"`
		} `xml:"Upload"`
	}
	helpers.DecodeXML(t, resp, &result)

	if result.Bucket != "lmu-bucket" {
		t.Errorf("expected Bucket lmu-bucket, got %q", result.Bucket)
	}
	if len(result.Uploads) != 2 {
		t.Fatalf("expected 2 uploads, got %d", len(result.Uploads))
	}

	ids := map[string]string{}
	for _, u := range result.Uploads {
		ids[u.UploadId] = u.Key
	}
	if ids[id1] != "file-a.bin" {
		t.Errorf("expected upload %q key=file-a.bin, got %q", id1, ids[id1])
	}
	if ids[id2] != "file-b.bin" {
		t.Errorf("expected upload %q key=file-b.bin, got %q", id2, ids[id2])
	}
}

// ---- ListParts / ListMultipartUploads pagination (pagination-plan.md G4) --

// TestListParts_paginationContract pins pagination-plan.md G4's ListParts
// fix: MaxParts/PartNumberMarker are now honored (previously ignored
// entirely, always returning every part on one page with no IsTruncated/
// NextPartNumberMarker fields in the XML schema at all), and a malformed
// part-number-marker is rejected with InvalidArgument instead of silently
// defaulting to part 1 (the same invalid-token-restarts-page-1 divergence
// H1/G3 fixed elsewhere). AWS docs:
// https://docs.aws.amazon.com/AmazonS3/latest/API/API_ListParts.html.
func TestListParts_paginationContract(t *testing.T) {
	// Given: an upload with 5 parts — more than the MaxParts=2 page size
	// used below, so the walk must take at least 3 pages.
	srv := helpers.NewTestServer(t)
	createBucket(t, srv, "parts-paging-bucket")
	uploadID := createMultipartUpload(t, srv, "parts-paging-bucket", "big-file.bin")
	wantIDs := make([]string, 0, 5)
	for i := 1; i <= 5; i++ {
		uploadPart(t, srv, "parts-paging-bucket", "big-file.bin", uploadID, i, []byte(fmt.Sprintf("part-%d", i)))
		wantIDs = append(wantIDs, strconv.Itoa(i))
	}

	fetch := func(t *testing.T, token string) ([]string, string) {
		t.Helper()
		path := fmt.Sprintf("/parts-paging-bucket/big-file.bin?uploadId=%s&max-parts=2", uploadID)
		if token != "" {
			path += "&part-number-marker=" + token
		}
		resp, err := http.DefaultClient.Do(get(srv, path))
		if err != nil {
			t.Fatalf("ListParts: %v", err)
		}
		defer resp.Body.Close()
		helpers.AssertStatus(t, resp, http.StatusOK)

		var page struct {
			Parts []struct {
				PartNumber int `xml:"PartNumber"`
			} `xml:"Part"`
			IsTruncated          bool `xml:"IsTruncated"`
			NextPartNumberMarker int  `xml:"NextPartNumberMarker"`
		}
		helpers.DecodeXML(t, resp, &page)

		ids := make([]string, len(page.Parts))
		for i, p := range page.Parts {
			ids[i] = strconv.Itoa(p.PartNumber)
		}
		next := ""
		if page.IsTruncated {
			next = strconv.Itoa(page.NextPartNumberMarker)
		}
		return ids, next
	}

	probe := func(t *testing.T) (int, string) {
		t.Helper()
		path := fmt.Sprintf("/parts-paging-bucket/big-file.bin?uploadId=%s&part-number-marker=not-an-integer", uploadID)
		resp, err := http.DefaultClient.Do(get(srv, path))
		if err != nil {
			t.Fatalf("ListParts probe: %v", err)
		}
		defer resp.Body.Close()
		var errResp struct {
			Code string `xml:"Code"`
		}
		helpers.DecodeXML(t, resp, &errResp)
		return resp.StatusCode, errResp.Code
	}

	helpers.PaginationContractTest(t, wantIDs, func(id string) string { return id }, fetch, probe,
		helpers.PaginationContractOptions{
			WantInvalidTokenStatus:    http.StatusBadRequest,
			WantInvalidTokenErrorCode: "InvalidArgument",
		})
}

// TestListParts_maxPartsDefaultAndCap pins AWS's documented MaxParts
// default and cap of 1000 (both the same value) —
// https://docs.aws.amazon.com/AmazonS3/latest/API/API_ListParts.html.
func TestListParts_maxPartsDefaultAndCap(t *testing.T) {
	srv := helpers.NewTestServer(t)
	createBucket(t, srv, "parts-cap-bucket")
	uploadID := createMultipartUpload(t, srv, "parts-cap-bucket", "file.bin")
	uploadPart(t, srv, "parts-cap-bucket", "file.bin", uploadID, 1, []byte("x"))

	cases := []struct {
		name    string
		query   string
		wantMax int
	}{
		{"omitted", "", 1000},
		{"over cap clamps to 1000", "&max-parts=5000", 1000},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := fmt.Sprintf("/parts-cap-bucket/file.bin?uploadId=%s%s", uploadID, tc.query)
			resp, err := http.DefaultClient.Do(get(srv, path))
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			helpers.AssertStatus(t, resp, http.StatusOK)
			var result struct {
				MaxParts int `xml:"MaxParts"`
			}
			helpers.DecodeXML(t, resp, &result)
			if result.MaxParts != tc.wantMax {
				t.Errorf("expected MaxParts=%d, got %d", tc.wantMax, result.MaxParts)
			}
		})
	}
}

// TestListMultipartUploads_paginationContract pins pagination-plan.md G4's
// ListMultipartUploads fix: MaxUploads/KeyMarker/UploadIdMarker are now
// honored (previously ignored entirely — no IsTruncated/NextKeyMarker/
// NextUploadIdMarker in the XML schema at all). AWS docs:
// https://docs.aws.amazon.com/AmazonS3/latest/API/API_ListMultipartUploads.html.
func TestListMultipartUploads_paginationContract(t *testing.T) {
	// Given: 5 uploads on 5 distinct keys — more than the MaxUploads=2 page
	// size used below, so the walk must take at least 3 pages. AWS sorts
	// ListMultipartUploads by key ascending, so name the keys so sorted
	// order is predictable.
	srv := helpers.NewTestServer(t)
	createBucket(t, srv, "uploads-paging-bucket")
	wantIDs := make([]string, 0, 5)
	for _, key := range []string{"a.bin", "b.bin", "c.bin", "d.bin", "e.bin"} {
		createMultipartUpload(t, srv, "uploads-paging-bucket", key)
		wantIDs = append(wantIDs, key)
	}

	fetch := func(t *testing.T, token string) ([]string, string) {
		t.Helper()
		path := "/uploads-paging-bucket?uploads&max-uploads=2"
		if token != "" {
			path += "&key-marker=" + token
		}
		resp, err := http.DefaultClient.Do(get(srv, path))
		if err != nil {
			t.Fatalf("ListMultipartUploads: %v", err)
		}
		defer resp.Body.Close()
		helpers.AssertStatus(t, resp, http.StatusOK)

		var page struct {
			Uploads []struct {
				Key string `xml:"Key"`
			} `xml:"Upload"`
			IsTruncated   bool   `xml:"IsTruncated"`
			NextKeyMarker string `xml:"NextKeyMarker"`
		}
		helpers.DecodeXML(t, resp, &page)

		ids := make([]string, len(page.Uploads))
		for i, u := range page.Uploads {
			ids[i] = u.Key
		}
		next := ""
		if page.IsTruncated {
			next = page.NextKeyMarker
		}
		return ids, next
	}

	// AWS documents KeyMarker/UploadIdMarker as plain (non-opaque) values,
	// not tokens with a rejectable "invalid" shape — any string is a
	// syntactically legal key-marker (see ListObjects' Marker for the same
	// convention) — so there is no invalid-marker probe here, unlike
	// ListObjectsV2's ContinuationToken or ListParts' PartNumberMarker.
	helpers.PaginationContractTest(t, wantIDs, func(key string) string { return key }, fetch, nil,
		helpers.PaginationContractOptions{})
}

// TestListMultipartUploads_sameKeyCompoundMarker pins AWS's compound
// KeyMarker+UploadIdMarker resume rule for two uploads that share a key:
// "any multipart uploads for a key equal to the key-marker might also be
// included, provided those multipart uploads have upload IDs
// lexicographically greater than the specified upload-id-marker."
func TestListMultipartUploads_sameKeyCompoundMarker(t *testing.T) {
	// Given: two uploads initiated for the same key
	srv := helpers.NewTestServer(t)
	createBucket(t, srv, "same-key-bucket")
	id1 := createMultipartUpload(t, srv, "same-key-bucket", "shared.bin")
	id2 := createMultipartUpload(t, srv, "same-key-bucket", "shared.bin")
	lo, hi := id1, id2
	if hi < lo {
		lo, hi = hi, lo
	}

	// When: key-marker=shared.bin with no upload-id-marker
	resp1, err := http.DefaultClient.Do(get(srv, "/same-key-bucket?uploads&key-marker=shared.bin"))
	if err != nil {
		t.Fatal(err)
	}
	defer resp1.Body.Close()
	helpers.AssertStatus(t, resp1, http.StatusOK)
	var page1 struct {
		Uploads []struct {
			UploadId string `xml:"UploadId"`
		} `xml:"Upload"`
	}
	helpers.DecodeXML(t, resp1, &page1)

	// Then: neither upload for "shared.bin" is included — AWS excludes
	// keys equal to key-marker entirely when upload-id-marker is absent.
	if len(page1.Uploads) != 0 {
		t.Errorf("expected 0 uploads with key-marker=shared.bin and no upload-id-marker, got %d", len(page1.Uploads))
	}

	// When: key-marker=shared.bin AND upload-id-marker=lo (the
	// lexicographically smaller of the two upload IDs)
	resp2, err := http.DefaultClient.Do(get(srv, "/same-key-bucket?uploads&key-marker=shared.bin&upload-id-marker="+lo))
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	helpers.AssertStatus(t, resp2, http.StatusOK)
	var page2 struct {
		Uploads []struct {
			UploadId string `xml:"UploadId"`
		} `xml:"Upload"`
	}
	helpers.DecodeXML(t, resp2, &page2)

	// Then: only the upload with the lexicographically greater ID (hi) is
	// included.
	if len(page2.Uploads) != 1 || page2.Uploads[0].UploadId != hi {
		t.Fatalf("expected exactly the upload with ID %q, got %#v", hi, page2.Uploads)
	}
}

func TestMultipartUpload_abort(t *testing.T) {
	// Given an initiated upload with one part
	srv := helpers.NewTestServer(t)
	createBucket(t, srv, "abort-bucket")
	uploadID := createMultipartUpload(t, srv, "abort-bucket", "gone.bin")
	uploadPart(t, srv, "abort-bucket", "gone.bin", uploadID, 1, []byte("some data"))

	// When: abort the upload
	path := fmt.Sprintf("/abort-bucket/gone.bin?uploadId=%s", uploadID)
	resp, err := http.DefaultClient.Do(del(srv, path))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusNoContent)

	// Then: the upload no longer appears in ListMultipartUploads
	lresp, err := http.DefaultClient.Do(get(srv, "/abort-bucket?uploads"))
	if err != nil {
		t.Fatal(err)
	}
	defer lresp.Body.Close()
	helpers.AssertStatus(t, lresp, http.StatusOK)

	var result struct {
		Uploads []struct {
			UploadId string `xml:"UploadId"`
		} `xml:"Upload"`
	}
	helpers.DecodeXML(t, lresp, &result)
	for _, u := range result.Uploads {
		if u.UploadId == uploadID {
			t.Errorf("aborted upload %q still appears in ListMultipartUploads", uploadID)
		}
	}
}

func TestMultipartUpload_completeRemovesUploadFromList(t *testing.T) {
	// Given a completed multipart upload
	srv := helpers.NewTestServer(t)
	createBucket(t, srv, "done-bucket")
	uploadID := createMultipartUpload(t, srv, "done-bucket", "done.bin")
	etag := uploadPart(t, srv, "done-bucket", "done.bin", uploadID, 1, []byte("content"))
	completeMultipartUpload(t, srv, "done-bucket", "done.bin", uploadID, []struct {
		PartNumber int
		ETag       string
	}{{1, etag}})

	// When: list multipart uploads
	resp, err := http.DefaultClient.Do(get(srv, "/done-bucket?uploads"))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)

	// Then: completed upload is not listed
	var result struct {
		Uploads []struct {
			UploadId string `xml:"UploadId"`
		} `xml:"Upload"`
	}
	helpers.DecodeXML(t, resp, &result)
	for _, u := range result.Uploads {
		if u.UploadId == uploadID {
			t.Errorf("completed upload %q should not appear in ListMultipartUploads", uploadID)
		}
	}
}

func TestMultipartUpload_unknownUploadID_uploadPart_returns404(t *testing.T) {
	// Given a bucket with no active uploads
	srv := helpers.NewTestServer(t)
	createBucket(t, srv, "unknown-bucket")

	// When: upload a part with an unknown uploadId
	path := "/unknown-bucket/key.bin?partNumber=1&uploadId=no-such-upload"
	req := mustReq(http.MethodPut, srv.URL+path, bytes.NewReader([]byte("data")), nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	// Then: 404 NoSuchUpload
	helpers.AssertStatus(t, resp, http.StatusNotFound)
	helpers.AssertXMLError(t, resp, "NoSuchUpload")
}

func TestMultipartUpload_unknownUploadID_complete_returns404(t *testing.T) {
	srv := helpers.NewTestServer(t)
	createBucket(t, srv, "unknown-bucket")

	body := `<CompleteMultipartUpload><Part><PartNumber>1</PartNumber><ETag>"abc"</ETag></Part></CompleteMultipartUpload>`
	path := "/unknown-bucket/key.bin?uploadId=no-such-upload"
	req := mustReq(http.MethodPost, srv.URL+path, strings.NewReader(body), map[string]string{"Content-Type": "application/xml"})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	helpers.AssertStatus(t, resp, http.StatusNotFound)
	helpers.AssertXMLError(t, resp, "NoSuchUpload")
}

func TestMultipartUpload_unknownUploadID_abort_returns404(t *testing.T) {
	srv := helpers.NewTestServer(t)
	createBucket(t, srv, "unknown-bucket")

	path := "/unknown-bucket/key.bin?uploadId=no-such-upload"
	resp, err := http.DefaultClient.Do(del(srv, path))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	helpers.AssertStatus(t, resp, http.StatusNotFound)
	helpers.AssertXMLError(t, resp, "NoSuchUpload")
}

func TestMultipartUpload_unknownUploadID_listParts_returns404(t *testing.T) {
	srv := helpers.NewTestServer(t)
	createBucket(t, srv, "unknown-bucket")

	resp, err := http.DefaultClient.Do(get(srv, "/unknown-bucket/key.bin?uploadId=no-such-upload"))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	helpers.AssertStatus(t, resp, http.StatusNotFound)
	helpers.AssertXMLError(t, resp, "NoSuchUpload")
}

func TestMultipartUpload_listMultipartUploads_bucketNotFound(t *testing.T) {
	srv := helpers.NewTestServer(t)

	resp, err := http.DefaultClient.Do(get(srv, "/no-bucket?uploads"))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	helpers.AssertStatus(t, resp, http.StatusNotFound)
	helpers.AssertXMLError(t, resp, "NoSuchBucket")
}

func TestMultipartUpload_bodyStoredOnDisk(t *testing.T) {
	// Given a test server with a known data directory
	dataDir := t.TempDir()
	srv := helpers.NewTestServer(t, helpers.WithDataDir(dataDir))
	createBucket(t, srv, "disk-bucket")

	// When: complete a multipart upload
	uploadID := createMultipartUpload(t, srv, "disk-bucket", "disk-obj.bin")
	etag := uploadPart(t, srv, "disk-bucket", "disk-obj.bin", uploadID, 1, []byte("hello"))
	completeMultipartUpload(t, srv, "disk-bucket", "disk-obj.bin", uploadID, []struct {
		PartNumber int
		ETag       string
	}{{1, etag}})

	// Then: a body file exists
	if files := findFiles(t, dataDir); len(files) == 0 {
		t.Fatal("expected body file on disk after multipart complete")
	}
}

// TestS3_UnimplementedOperations_return501 verifies that every S3 route that
// is not yet implemented returns HTTP 501 with x-emulator-unsupported: true.
// Each sub-test name identifies the AWS S3 operation being tested.
//
// The test pre-creates bucket "stub-bucket" and object "stub-bucket/stub-key"
// so that routes under /{bucket} and /{bucket}/{key} are not rejected with a
// 404 before reaching the stub handler.
func TestS3_UnimplementedOperations_return501(t *testing.T) {
	srv := helpers.NewTestServer(t)
	createBucket(t, srv, "stub-bucket")
	putObject(t, srv, "stub-bucket", "stub-key", []byte("data"), "text/plain")

	type tc struct {
		name   string
		method string
		path   string // path + query string, no host
	}

	cases := []tc{
		// ---- Root -------------------------------------------------------
		{"ListDirectoryBuckets", http.MethodGet, "/?directory-buckets"},

		// ---- Bucket GET sub-resources -----------------------------------
		{"GetBucketAcl", http.MethodGet, "/stub-bucket?acl"},
		{"GetBucketPolicyStatus", http.MethodGet, "/stub-bucket?policyStatus"},
		{"GetBucketLogging", http.MethodGet, "/stub-bucket?logging"},
		{"GetBucketReplication", http.MethodGet, "/stub-bucket?replication"},
		{"GetBucketAccelerateConfiguration", http.MethodGet, "/stub-bucket?accelerate"},
		{"GetBucketRequestPayment", http.MethodGet, "/stub-bucket?requestPayment"},
		{"GetBucketOwnershipControls", http.MethodGet, "/stub-bucket?ownershipControls"},
		{"GetPublicAccessBlock", http.MethodGet, "/stub-bucket?publicAccessBlock"},
		{"ListBucketAnalyticsConfigurations", http.MethodGet, "/stub-bucket?analytics"},
		{"ListBucketIntelligentTieringConfigurations", http.MethodGet, "/stub-bucket?intelligent-tiering"},
		{"ListBucketInventoryConfigurations", http.MethodGet, "/stub-bucket?inventory"},
		{"ListBucketMetricsConfigurations", http.MethodGet, "/stub-bucket?metrics"},
		{"GetObjectLockConfiguration", http.MethodGet, "/stub-bucket?object-lock"},
		{"GetBucketAbac", http.MethodGet, "/stub-bucket?abac"},
		{"GetBucketMetadataConfiguration", http.MethodGet, "/stub-bucket?metadata"},
		{"GetBucketMetadataTableConfiguration", http.MethodGet, "/stub-bucket?metadataTable"},
		{"CreateSession", http.MethodGet, "/stub-bucket?session"},

		// ---- Bucket PUT sub-resources -----------------------------------
		{"PutBucketAcl", http.MethodPut, "/stub-bucket?acl"},
		{"PutBucketLogging", http.MethodPut, "/stub-bucket?logging"},
		{"PutBucketReplication", http.MethodPut, "/stub-bucket?replication"},
		{"PutBucketAccelerateConfiguration", http.MethodPut, "/stub-bucket?accelerate"},
		{"PutBucketRequestPayment", http.MethodPut, "/stub-bucket?requestPayment"},
		{"PutBucketOwnershipControls", http.MethodPut, "/stub-bucket?ownershipControls"},
		{"PutPublicAccessBlock", http.MethodPut, "/stub-bucket?publicAccessBlock"},
		{"PutBucketAnalyticsConfiguration", http.MethodPut, "/stub-bucket?analytics"},
		{"PutBucketIntelligentTieringConfiguration", http.MethodPut, "/stub-bucket?intelligent-tiering"},
		{"PutBucketInventoryConfiguration", http.MethodPut, "/stub-bucket?inventory"},
		{"PutBucketMetricsConfiguration", http.MethodPut, "/stub-bucket?metrics"},
		{"PutObjectLockConfiguration", http.MethodPut, "/stub-bucket?object-lock"},
		{"PutBucketAbac", http.MethodPut, "/stub-bucket?abac"},
		{"CreateBucketMetadataConfiguration", http.MethodPut, "/stub-bucket?metadata"},
		{"UpdateBucketMetadataTableConfiguration", http.MethodPut, "/stub-bucket?metadataTable"},

		// ---- Bucket DELETE sub-resources --------------------------------
		{"DeleteBucketReplication", http.MethodDelete, "/stub-bucket?replication"},
		{"DeleteBucketAnalyticsConfiguration", http.MethodDelete, "/stub-bucket?analytics"},
		{"DeleteBucketIntelligentTieringConfiguration", http.MethodDelete, "/stub-bucket?intelligent-tiering"},
		{"DeleteBucketInventoryConfiguration", http.MethodDelete, "/stub-bucket?inventory"},
		{"DeleteBucketMetricsConfiguration", http.MethodDelete, "/stub-bucket?metrics"},
		{"DeleteBucketOwnershipControls", http.MethodDelete, "/stub-bucket?ownershipControls"},
		{"DeletePublicAccessBlock", http.MethodDelete, "/stub-bucket?publicAccessBlock"},
		{"DeleteBucketMetadataConfiguration", http.MethodDelete, "/stub-bucket?metadata"},
		{"DeleteBucketMetadataTableConfiguration", http.MethodDelete, "/stub-bucket?metadataTable"},

		// ---- Bucket POST sub-resources ----------------------------------
		{"CreateBucketMetadataTableConfiguration", http.MethodPost, "/stub-bucket?metadataTable"},

		// ---- Object GET sub-resources -----------------------------------
		{"GetObjectAcl", http.MethodGet, "/stub-bucket/stub-key?acl"},
		{"GetObjectAttributes", http.MethodGet, "/stub-bucket/stub-key?attributes"},
		{"GetObjectLegalHold", http.MethodGet, "/stub-bucket/stub-key?legal-hold"},
		{"GetObjectRetention", http.MethodGet, "/stub-bucket/stub-key?retention"},
		{"GetObjectTorrent", http.MethodGet, "/stub-bucket/stub-key?torrent"},

		// ---- Object PUT sub-resources -----------------------------------
		{"PutObjectAcl", http.MethodPut, "/stub-bucket/stub-key?acl"},
		{"PutObjectLegalHold", http.MethodPut, "/stub-bucket/stub-key?legal-hold"},
		{"PutObjectRetention", http.MethodPut, "/stub-bucket/stub-key?retention"},
		{"RenameObject", http.MethodPut, "/stub-bucket/stub-key?rename"},
		{"UpdateObjectEncryption", http.MethodPut, "/stub-bucket/stub-key?encryption"},
		{"UploadPartCopy", http.MethodPut, "/stub-bucket/stub-key?partNumber=1"},

		// ---- Object DELETE sub-resources --------------------------------
		// ---- Object POST sub-resources ----------------------------------
		{"RestoreObject", http.MethodPost, "/stub-bucket/stub-key?restore"},
		{"SelectObjectContent", http.MethodPost, "/stub-bucket/stub-key?select"},
		{"WriteGetObjectResponse", http.MethodPost, "/stub-bucket/stub-key?writeGetObjectResponse"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req, err := http.NewRequest(c.method, srv.URL+c.path, nil)
			if err != nil {
				t.Fatalf("build request: %v", err)
			}
			// UploadPartCopy needs the copy-source header to route correctly.
			if c.name == "UploadPartCopy" {
				req.Header.Set("x-amz-copy-source", "/stub-bucket/stub-key")
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("do request: %v", err)
			}
			resp.Body.Close()

			helpers.AssertStatus(t, resp, http.StatusNotImplemented)
			if got := resp.Header.Get("x-emulator-unsupported"); got != "true" {
				t.Errorf("expected x-emulator-unsupported: true, got %q", got)
			}
		})
	}
}

// ---- ListBuckets -----------------------------------------------------------

func TestListBuckets_empty(t *testing.T) {
	srv := helpers.NewTestServer(t)

	resp, err := http.DefaultClient.Do(get(srv, "/"))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	helpers.AssertStatus(t, resp, http.StatusOK)
	helpers.AssertRequestID(t, resp)

	var result struct {
		Buckets struct {
			Bucket []struct{ Name string }
		}
	}
	if err := xml.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode ListBucketsResult: %v", err)
	}
	if len(result.Buckets.Bucket) != 0 {
		t.Errorf("expected 0 buckets, got %d", len(result.Buckets.Bucket))
	}
}

func TestListBuckets_withBuckets(t *testing.T) {
	srv := helpers.NewTestServer(t)
	createBucket(t, srv, "alpha")
	createBucket(t, srv, "beta")
	createBucket(t, srv, "gamma")

	resp, err := http.DefaultClient.Do(get(srv, "/"))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	helpers.AssertStatus(t, resp, http.StatusOK)

	var result struct {
		Buckets struct {
			Bucket []struct{ Name string }
		}
	}
	if err := xml.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode ListBucketsResult: %v", err)
	}
	if got := len(result.Buckets.Bucket); got != 3 {
		t.Errorf("expected 3 buckets, got %d", got)
	}
	names := make(map[string]bool, 3)
	for _, b := range result.Buckets.Bucket {
		names[b.Name] = true
	}
	for _, want := range []string{"alpha", "beta", "gamma"} {
		if !names[want] {
			t.Errorf("bucket %q missing from ListBuckets response", want)
		}
	}
}

// ---- Request ID is present on every response --------------------------------

func TestEveryResponse_hasRequestID(t *testing.T) {
	srv := helpers.NewTestServer(t)
	createBucket(t, srv, "my-bucket")

	cases := []*http.Request{
		mustReq(http.MethodPut, srv.URL+"/test-bucket2", nil, nil),
		mustReq(http.MethodHead, srv.URL+"/my-bucket", nil, nil),
		mustReq(http.MethodGet, srv.URL+"/my-bucket?list-type=2", nil, nil),
		mustReq(http.MethodGet, srv.URL+"/my-bucket/missing", nil, nil), // 404
	}

	for _, req := range cases {
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		helpers.AssertRequestID(t, resp)
	}
}

// ---- Disk-backed body storage ----------------------------------------------

func TestPutObject_bodyStoredOnDisk(t *testing.T) {
	// Given a test server with a known data directory
	dataDir := t.TempDir()
	srv := helpers.NewTestServer(t, helpers.WithDataDir(dataDir))
	createBucket(t, srv, "my-bucket")

	// When we put an object with a known body
	content := []byte("disk-backed-body-content")
	putObject(t, srv, "my-bucket", "disk-test.txt", content, "text/plain")

	// Then GetObject returns the correct body
	resp, err := http.DefaultClient.Do(get(srv, "/my-bucket/disk-test.txt"))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)

	body, _ := io.ReadAll(resp.Body)
	if !bytes.Equal(body, content) {
		t.Errorf("expected body %q, got %q", content, body)
	}

	// And a body file exists on disk under the data directory
	bodyFiles := findFiles(t, dataDir)
	if len(bodyFiles) == 0 {
		t.Fatal("expected at least one body file on disk under DataDir, found none")
	}
}

func TestDeleteObject_removesBodyFromDisk(t *testing.T) {
	// Given a test server with a known data directory
	dataDir := t.TempDir()
	srv := helpers.NewTestServer(t, helpers.WithDataDir(dataDir))
	createBucket(t, srv, "my-bucket")
	putObject(t, srv, "my-bucket", "to-delete.txt", []byte("ephemeral"), "text/plain")

	// Verify body file exists
	before := findFiles(t, dataDir)
	if len(before) == 0 {
		t.Fatal("expected body file on disk after put")
	}

	// When we delete the object
	resp, err := http.DefaultClient.Do(del(srv, "/my-bucket/to-delete.txt"))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusNoContent)

	// Then the body file is removed from disk
	after := findFiles(t, dataDir)
	if len(after) != 0 {
		t.Errorf("expected 0 body files after delete, found %d: %v", len(after), after)
	}
}

// findFiles returns all regular files under dir (recursively).
func findFiles(t *testing.T, dir string) []string {
	t.Helper()
	var files []string
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("findFiles: %v", err)
	}
	return files
}

// ---- S3 Virtual-Hosted-Style Addressing ------------------------------------

func TestS3VirtualHostedStyle_PutAndGetObject(t *testing.T) {
	// Given a bucket created via path-style
	srv := helpers.NewTestServer(t)
	createBucket(t, srv, "vhost-bucket")

	// When uploading an object via virtual-hosted-style (bucket in Host header)
	body := []byte("virtual hosted content")
	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/greeting.txt", bytes.NewReader(body))
	req.Host = "vhost-bucket.localhost"
	req.Header.Set("Content-Type", "text/plain")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	// Then the upload succeeds
	helpers.AssertStatus(t, resp, http.StatusOK)

	// And the object can be retrieved via path-style
	getResp, err := http.DefaultClient.Do(get(srv, "/vhost-bucket/greeting.txt"))
	if err != nil {
		t.Fatal(err)
	}
	defer getResp.Body.Close()

	helpers.AssertStatus(t, getResp, http.StatusOK)
	got, _ := io.ReadAll(getResp.Body)
	if string(got) != "virtual hosted content" {
		t.Errorf("expected %q, got %q", "virtual hosted content", got)
	}
}

func TestS3VirtualHostedStyle_HeadBucket(t *testing.T) {
	srv := helpers.NewTestServer(t)
	createBucket(t, srv, "vhost-head-bucket")

	// HEAD / with virtual-hosted Host header
	req, _ := http.NewRequest(http.MethodHead, srv.URL+"/", nil)
	req.Host = "vhost-head-bucket.localhost"

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	helpers.AssertStatus(t, resp, http.StatusOK)
}

func TestS3VirtualHostedStyle_ListObjectsV2(t *testing.T) {
	srv := helpers.NewTestServer(t)
	createBucket(t, srv, "vhost-list-bucket")
	putObject(t, srv, "vhost-list-bucket", "file1.txt", []byte("hello"), "text/plain")

	// GET /?list-type=2 with virtual-hosted Host header
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/?list-type=2", nil)
	req.Host = "vhost-list-bucket.localhost"

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	helpers.AssertStatus(t, resp, http.StatusOK)
	respBody, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(respBody), "file1.txt") {
		t.Errorf("expected listing to contain file1.txt, got %s", respBody)
	}
}

func TestS3VirtualHostedStyle_S3Subdomain(t *testing.T) {
	// {bucket}.s3.localhost pattern (standard AWS virtual-hosted-style)
	srv := helpers.NewTestServer(t)
	createBucket(t, srv, "s3style-bucket")

	body := []byte("s3 subdomain content")
	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/my-key.json", bytes.NewReader(body))
	req.Host = "s3style-bucket.s3.localhost"

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	helpers.AssertStatus(t, resp, http.StatusOK)

	// Verify via path-style
	getResp, err := http.DefaultClient.Do(get(srv, "/s3style-bucket/my-key.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer getResp.Body.Close()

	helpers.AssertStatus(t, getResp, http.StatusOK)
	got, _ := io.ReadAll(getResp.Body)
	if string(got) != "s3 subdomain content" {
		t.Errorf("expected %q, got %q", "s3 subdomain content", got)
	}
}

func TestS3VirtualHostedStyle_CreateBucket(t *testing.T) {
	// CDK bootstrap creates buckets; the SDK may use virtual-hosted-style.
	srv := helpers.NewTestServer(t)

	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/", nil)
	req.Host = "vhost-created-bucket.localhost"

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	helpers.AssertStatus(t, resp, http.StatusOK)

	// Verify bucket exists via path-style HeadBucket
	headResp, err := http.DefaultClient.Do(head(srv, "/vhost-created-bucket"))
	if err != nil {
		t.Fatal(err)
	}
	headResp.Body.Close()

	helpers.AssertStatus(t, headResp, http.StatusOK)
}

// ---- S3 Virtual-Hosted-Style with configured wildcard DNS hostname ----------
//
// These tests cover the CDK asset-publisher path on Windows/Mac where
// *.localhost does not resolve.  Setting OVERCAST_HOSTNAME to a wildcard-DNS
// name (e.g. "localhost.localstack.cloud") makes CDK-generated bucket
// hostnames resolvable without any hosts-file changes:
//
//	cdk-hnb659fds-assets-<account>-<region>.localhost.localstack.cloud
//
// The middleware rewrites the Host subdomain to a path-style bucket prefix,
// so the request reaches the correct S3 handler.

func TestS3VirtualHostedStyle_WildcardDNS_PutAndGetObject(t *testing.T) {
	// Given: Overcast configured with a wildcard-DNS hostname
	const wildcardBase = "localhost.localstack.cloud"
	srv := helpers.NewTestServer(t, helpers.WithHostname(wildcardBase))
	createBucket(t, srv, "wc-bucket")
	putObject(t, srv, "wc-bucket", "hello.txt", []byte("hello"), "text/plain")

	// When: GET via virtual-hosted-style Host with the wildcard base
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/hello.txt", nil)
	req.Host = "wc-bucket." + wildcardBase

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	// Then: the object is returned correctly
	helpers.AssertStatus(t, resp, http.StatusOK)
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "hello" {
		t.Errorf("expected body %q, got %q", "hello", string(body))
	}
}

func TestS3VirtualHostedStyle_WildcardDNS_CDKBootstrapBucketName(t *testing.T) {
	// Given: a CDK bootstrap bucket name with account and region
	// (the exact pattern CDK's asset publisher uses on Windows/Mac)
	const wildcardBase = "localhost.localstack.cloud"
	const cdkBucket = "cdk-hnb659fds-assets-000000000000-ap-southeast-2"

	srv := helpers.NewTestServer(t, helpers.WithHostname(wildcardBase))
	createBucket(t, srv, cdkBucket)

	// When: HEAD via virtual-hosted Host — mimics CDK asset publisher probe
	req, _ := http.NewRequest(http.MethodHead, srv.URL+"/", nil)
	req.Host = cdkBucket + "." + wildcardBase + ":4566"

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	// Then: bucket is found (200)
	helpers.AssertStatus(t, resp, http.StatusOK)
}

func TestS3VirtualHostedStyle_WildcardDNS_S3SubdomainStyle(t *testing.T) {
	// {bucket}.s3.{base} style with wildcard base
	const wildcardBase = "localhost.localstack.cloud"
	srv := helpers.NewTestServer(t, helpers.WithHostname(wildcardBase))
	createBucket(t, srv, "s3sub-bucket")
	putObject(t, srv, "s3sub-bucket", "obj.txt", []byte("data"), "text/plain")

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/obj.txt", nil)
	req.Host = "s3sub-bucket.s3." + wildcardBase

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	helpers.AssertStatus(t, resp, http.StatusOK)
}

// ---- S3 Virtual-Hosted-Style: plain ".s3.localhost" suffix ----------------
//
// Browsers and OS resolvers (systemd-resolved on Linux, Windows since
// version 20348) resolve "*.localhost" to loopback natively, with no
// OVERCAST_HOSTNAME configuration and no /etc/hosts entry required. This is
// therefore the most common real-world way users will reach the emulator via
// virtual-hosted-style addressing (e.g. "mybucket.s3.localhost:4566"). These
// tests prove the ".s3." match rule is suffix-agnostic — it fires the same
// way whether or not a wildcard-DNS OVERCAST_HOSTNAME is configured.

func TestS3VirtualHostedStyle_PlainDotLocalhostSuffix_PutAndGetObject(t *testing.T) {
	// Given: a server with no OVERCAST_HOSTNAME configured
	srv := helpers.NewTestServer(t)
	createBucket(t, srv, "my-bucket")

	// When: PUT via Host "my-bucket.s3.localhost" (no port-independent config needed)
	body := []byte("hello from plain .s3.localhost")
	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/greeting.txt", bytes.NewReader(body))
	req.Host = "my-bucket.s3.localhost"
	req.Header.Set("Content-Type", "text/plain")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)

	// Then: GET via the same Host returns the object
	getReq, _ := http.NewRequest(http.MethodGet, srv.URL+"/greeting.txt", nil)
	getReq.Host = "my-bucket.s3.localhost"
	getResp, err := http.DefaultClient.Do(getReq)
	if err != nil {
		t.Fatal(err)
	}
	defer getResp.Body.Close()

	helpers.AssertStatus(t, getResp, http.StatusOK)
	got, _ := io.ReadAll(getResp.Body)
	if string(got) != string(body) {
		t.Errorf("expected %q, got %q", body, got)
	}
}

// ---- S3 Virtual-Hosted-Style: error fidelity, dotted buckets, sub-resources -

func TestS3VirtualHostedStyle_NonexistentBucketReturnsModeledXMLError(t *testing.T) {
	// Given: no bucket has been created
	srv := helpers.NewTestServer(t)

	// When: GET an object via virtual-hosted-style Host for a bucket that
	// was never created
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/some-key.txt", nil)
	req.Host = "nosuchbucket.s3.localhost"

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	// Then: we get a modeled AWS XML error, not a bare 404 — NoSuchBucket,
	// matching real AWS (#1635; GetObject used to answer NoSuchKey for a
	// missing bucket regardless of addressing style, because the unversioned
	// read path never checked the bucket at all). This test's job is to pin
	// that virtual-hosted-style routing reaches the real S3 error path
	// instead of falling through to chi's bare 404, with the addressing
	// style contributing no divergence of its own from the path-style case
	// covered by TestGetObject_missingBucketReturnsNoSuchBucket.
	helpers.AssertStatus(t, resp, http.StatusNotFound)
	helpers.AssertXMLError(t, resp, "NoSuchBucket")
	helpers.AssertRequestID(t, resp)
}

func TestS3VirtualHostedStyle_ExistingBucketNonexistentKeyReturnsNoSuchKey(t *testing.T) {
	// Given: a bucket exists but the requested key does not
	srv := helpers.NewTestServer(t)
	createBucket(t, srv, "vhost-nsk-bucket")

	// When: GET a nonexistent key via virtual-hosted-style Host
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/missing.txt", nil)
	req.Host = "vhost-nsk-bucket.s3.localhost"

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	// Then: we get a modeled AWS NoSuchKey XML error
	helpers.AssertStatus(t, resp, http.StatusNotFound)
	helpers.AssertXMLError(t, resp, "NoSuchKey")
	helpers.AssertRequestID(t, resp)
}

func TestS3VirtualHostedStyle_DottedBucketNameHost_SplitsOnFirstDotS3(t *testing.T) {
	// Given: real AWS bucket names may contain dots, but Overcast's own
	// CreateBucket validation deliberately restricts bucket names to
	// lowercase letters, numbers, and hyphens (see serviceutil.BucketName) —
	// so a dotted bucket name can never actually exist here, and a full
	// PUT+GET round trip is not achievable through Overcast's own API.
	// This test instead pins the Host-header SPLIT behavior directly: the
	// bucket must be everything before the FIRST ".s3." separator (matching
	// extractS3BucketFromHost's unit-tested contract), not e.g. just the
	// first label. We prove this by observing which "bucket" name PutObject
	// reports as missing.
	srv := helpers.NewTestServer(t)

	// When: PUT via a Host whose bucket portion itself contains dots
	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/key.txt", bytes.NewReader([]byte("x")))
	req.Host = "my.dotted.bucket.s3.localhost"
	req.Header.Set("Content-Type", "text/plain")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	// Then: PutObject's bucketExists check reaches the S3 handler with the
	// FULL "my.dotted.bucket" string as the bucket name (a modeled
	// NoSuchBucket XML error naming it), proving the split landed on the
	// first ".s3." and did not truncate at the first dot.
	helpers.AssertStatus(t, resp, http.StatusNotFound)
	body, _ := io.ReadAll(resp.Body)
	var errResp struct {
		Code    string `xml:"Code"`
		Message string `xml:"Message"`
	}
	if err := xml.Unmarshal(body, &errResp); err != nil {
		t.Fatalf("failed to decode XML error body %q: %v", body, err)
	}
	if errResp.Code != "NoSuchBucket" {
		t.Errorf("expected error code %q, got %q (message: %s)", "NoSuchBucket", errResp.Code, errResp.Message)
	}
	if !strings.Contains(errResp.Message, "my.dotted.bucket") {
		t.Errorf("expected error message to reference bucket %q, got %q", "my.dotted.bucket", errResp.Message)
	}
}

func TestS3VirtualHostedStyle_SubResourceLocation(t *testing.T) {
	// Given: a bucket exists
	srv := helpers.NewTestServer(t)
	createBucket(t, srv, "vhost-location-bucket")

	// When: GET ?location via virtual-hosted-style Host
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/?location", nil)
	req.Host = "vhost-location-bucket.s3.localhost"

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	// Then: the sub-resource handler responds normally (bucket resolved from Host)
	helpers.AssertStatus(t, resp, http.StatusOK)
	respBody, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(respBody), "LocationConstraint") {
		t.Errorf("expected LocationConstraint in response, got %s", respBody)
	}
}

func TestS3VirtualHostedStyle_BareS3HostStaysPathStyle(t *testing.T) {
	// Given: a bucket created via path-style
	srv := helpers.NewTestServer(t)
	createBucket(t, srv, "pathstyle-bucket")
	putObject(t, srv, "pathstyle-bucket", "key.txt", []byte("path style"), "text/plain")

	// When: GET with Host "s3.localhost" (the bare S3 service endpoint, not
	// a bucket subdomain) and the bucket already in the path
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/pathstyle-bucket/key.txt", nil)
	req.Host = "s3.localhost"

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	// Then: no bucket prefix is added (no double-rewrite) — request succeeds as path-style
	helpers.AssertStatus(t, resp, http.StatusOK)
	got, _ := io.ReadAll(resp.Body)
	if string(got) != "path style" {
		t.Errorf("expected %q, got %q", "path style", got)
	}
}

func TestS3VirtualHostedStyle_NonS3HostDotNotInLabelPositionStaysPathStyle(t *testing.T) {
	// Given: a bucket created via path-style
	srv := helpers.NewTestServer(t)
	createBucket(t, srv, "pathstyle-bucket2")
	putObject(t, srv, "pathstyle-bucket2", "key.txt", []byte("path style 2"), "text/plain")

	// When: GET with a Host containing ".s3x." — NOT the ".s3." separator in
	// label position — and the bucket already in the path
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/pathstyle-bucket2/key.txt", nil)
	req.Host = "weird.s3x.host"

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	// Then: the Host is not mistaken for virtual-hosted-style — request
	// resolves as path-style
	helpers.AssertStatus(t, resp, http.StatusOK)
	got, _ := io.ReadAll(resp.Body)
	if string(got) != "path style 2" {
		t.Errorf("expected %q, got %q", "path style 2", got)
	}
}

func TestS3VirtualHostedStyle_LegacyDashRegionDialect_PutAndGetObject(t *testing.T) {
	// Given: a bucket exists
	srv := helpers.NewTestServer(t)
	createBucket(t, srv, "legacy-dash-bucket")

	// When: PUT via the legacy dash-region dialect
	// ({bucket}.s3-{region}.{base}, e.g. mybucket.s3-us-west-2.amazonaws.com)
	body := []byte("legacy dash dialect content")
	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/obj.txt", bytes.NewReader(body))
	req.Host = "legacy-dash-bucket.s3-us-west-2.localhost"
	req.Header.Set("Content-Type", "text/plain")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)

	// Then: the object can be retrieved via path-style
	getResp, err := http.DefaultClient.Do(get(srv, "/legacy-dash-bucket/obj.txt"))
	if err != nil {
		t.Fatal(err)
	}
	defer getResp.Body.Close()

	helpers.AssertStatus(t, getResp, http.StatusOK)
	got, _ := io.ReadAll(getResp.Body)
	if string(got) != string(body) {
		t.Errorf("expected %q, got %q", body, got)
	}
}

// ---- Test helpers ----------------------------------------------------------
// These are local to this package, not exported to other test packages.

func createBucket(t *testing.T, srv *helpers.TestServer, name string) {
	t.Helper()
	resp, err := http.DefaultClient.Do(put(srv, "/"+name, nil, nil))
	if err != nil {
		t.Fatalf("createBucket %q: %v", name, err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("createBucket %q: unexpected status %d", name, resp.StatusCode)
	}
}

func putObject(t *testing.T, srv *helpers.TestServer, bucket, key string, body []byte, contentType string) {
	t.Helper()
	resp, err := http.DefaultClient.Do(put(srv, "/"+bucket+"/"+key, body, map[string]string{
		"Content-Type": contentType,
	}))
	if err != nil {
		t.Fatalf("putObject %q/%q: %v", bucket, key, err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("putObject %q/%q: unexpected status %d", bucket, key, resp.StatusCode)
	}
}

// ---- BucketNotificationConfiguration --------------------------------------

// notifXML is the AWS-compatible XML body for PutBucketNotificationConfiguration.
// It registers one SQS queue for all ObjectCreated events on the "uploads/" prefix.
const notifXML = `<?xml version="1.0" encoding="UTF-8"?>
<NotificationConfiguration xmlns="http://s3.amazonaws.com/doc/2006-03-01/">
  <QueueConfiguration>
    <Id>test-queue-config</Id>
    <Queue>arn:aws:sqs:us-east-1:000000000000:my-queue</Queue>
    <Event>s3:ObjectCreated:*</Event>
    <Filter>
      <S3Key>
        <FilterRule>
          <Name>prefix</Name>
          <Value>uploads/</Value>
        </FilterRule>
      </S3Key>
    </Filter>
  </QueueConfiguration>
</NotificationConfiguration>`

func TestPutBucketNotificationConfiguration_success(t *testing.T) {
	// Given a bucket exists
	srv := helpers.NewTestServer(t)
	createBucket(t, srv, "notify-bucket")

	// When we configure notifications
	resp, err := http.DefaultClient.Do(
		put(srv, "/notify-bucket?notification", []byte(notifXML), map[string]string{
			"Content-Type": "application/xml",
		}),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	// Then the response is 200 with no body
	helpers.AssertStatus(t, resp, http.StatusOK)
	helpers.AssertRequestID(t, resp)
}

func TestPutBucketNotificationConfiguration_bucketNotFound(t *testing.T) {
	// Given no bucket exists
	srv := helpers.NewTestServer(t)

	// When we configure notifications on a missing bucket
	resp, err := http.DefaultClient.Do(
		put(srv, "/missing-bucket?notification", []byte(notifXML), map[string]string{
			"Content-Type": "application/xml",
		}),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	// Then we get 404 NoSuchBucket
	helpers.AssertStatus(t, resp, http.StatusNotFound)
	helpers.AssertXMLError(t, resp, "NoSuchBucket")
}

func TestGetBucketNotificationConfiguration_empty(t *testing.T) {
	// Given a bucket with no notification config
	srv := helpers.NewTestServer(t)
	createBucket(t, srv, "quiet-bucket")

	// When we get the notification config
	resp, err := http.DefaultClient.Do(get(srv, "/quiet-bucket?notification"))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	// Then we get an empty NotificationConfiguration
	helpers.AssertStatus(t, resp, http.StatusOK)
	helpers.AssertRequestID(t, resp)

	var cfg struct {
		XMLName xml.Name `xml:"NotificationConfiguration"`
	}
	body, _ := io.ReadAll(resp.Body)
	if err := xml.Unmarshal(body, &cfg); err != nil {
		t.Fatalf("expected valid XML, got %q: %v", body, err)
	}
}

func TestGetBucketNotificationConfiguration_roundtrip(t *testing.T) {
	// Given a bucket with a saved notification config
	srv := helpers.NewTestServer(t)
	createBucket(t, srv, "roundtrip-bucket")

	putResp, err := http.DefaultClient.Do(
		put(srv, "/roundtrip-bucket?notification", []byte(notifXML), map[string]string{
			"Content-Type": "application/xml",
		}),
	)
	if err != nil {
		t.Fatal(err)
	}
	putResp.Body.Close()
	helpers.AssertStatus(t, putResp, http.StatusOK)

	// When we get the config back
	resp, err := http.DefaultClient.Do(get(srv, "/roundtrip-bucket?notification"))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	// Then the queue ARN and event type are preserved
	helpers.AssertStatus(t, resp, http.StatusOK)

	type FilterRule struct {
		Name  string `xml:"Name"`
		Value string `xml:"Value"`
	}
	type QueueConfig struct {
		ID     string       `xml:"Id"`
		Queue  string       `xml:"Queue"`
		Events []string     `xml:"Event"`
		Rules  []FilterRule `xml:"Filter>S3Key>FilterRule"`
	}
	var cfg struct {
		XMLName      xml.Name      `xml:"NotificationConfiguration"`
		QueueConfigs []QueueConfig `xml:"QueueConfiguration"`
	}
	body, _ := io.ReadAll(resp.Body)
	if err := xml.Unmarshal(body, &cfg); err != nil {
		t.Fatalf("expected valid XML, got %q: %v", body, err)
	}

	if len(cfg.QueueConfigs) != 1 {
		t.Fatalf("expected 1 QueueConfiguration, got %d", len(cfg.QueueConfigs))
	}
	qc := cfg.QueueConfigs[0]
	if qc.ID != "test-queue-config" {
		t.Errorf("expected Id %q, got %q", "test-queue-config", qc.ID)
	}
	if qc.Queue != "arn:aws:sqs:us-east-1:000000000000:my-queue" {
		t.Errorf("unexpected Queue ARN: %q", qc.Queue)
	}
	if len(qc.Events) != 1 || qc.Events[0] != "s3:ObjectCreated:*" {
		t.Errorf("unexpected Events: %v", qc.Events)
	}
	if len(qc.Rules) != 1 || qc.Rules[0].Name != "prefix" || qc.Rules[0].Value != "uploads/" {
		t.Errorf("unexpected filter rules: %v", qc.Rules)
	}
}

func TestGetBucketNotificationConfiguration_bucketNotFound(t *testing.T) {
	// Given no bucket exists
	srv := helpers.NewTestServer(t)

	// When we get notifications for a missing bucket
	resp, err := http.DefaultClient.Do(get(srv, "/missing-bucket?notification"))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	// Then we get 404 NoSuchBucket
	helpers.AssertStatus(t, resp, http.StatusNotFound)
	helpers.AssertXMLError(t, resp, "NoSuchBucket")
}

func TestPutBucketNotificationConfiguration_crossRegionLambda(t *testing.T) {
	// Given: a bucket in the test server's default region (us-east-1)
	srv := helpers.NewTestServer(t)
	createBucket(t, srv, "cross-region-notify-bucket")

	// When: a notification config points to a Lambda function in a different region
	crossRegionXML := `<?xml version="1.0" encoding="UTF-8"?>
<NotificationConfiguration xmlns="http://s3.amazonaws.com/doc/2006-03-01/">
  <CloudFunctionConfiguration>
    <Id>cross-region-fn</Id>
    <CloudFunction>arn:aws:lambda:eu-west-1:000000000000:function:my-fn</CloudFunction>
    <Event>s3:ObjectCreated:*</Event>
  </CloudFunctionConfiguration>
</NotificationConfiguration>`

	resp, err := http.DefaultClient.Do(
		put(srv, "/cross-region-notify-bucket?notification", []byte(crossRegionXML), map[string]string{
			"Content-Type": "application/xml",
		}),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	// Then: real AWS rejects Lambda destinations in a different region from the bucket
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertXMLError(t, resp, "InvalidArgument")
}

func TestPutBucketNotificationConfiguration_replace(t *testing.T) {
	// Given a bucket with an existing notification config
	srv := helpers.NewTestServer(t)
	createBucket(t, srv, "replace-bucket")

	firstPut, _ := http.DefaultClient.Do(
		put(srv, "/replace-bucket?notification", []byte(notifXML), map[string]string{
			"Content-Type": "application/xml",
		}),
	)
	firstPut.Body.Close()

	// When we PUT an empty NotificationConfiguration (clear all)
	emptyXML := `<NotificationConfiguration xmlns="http://s3.amazonaws.com/doc/2006-03-01/"></NotificationConfiguration>`
	clearResp, err := http.DefaultClient.Do(
		put(srv, "/replace-bucket?notification", []byte(emptyXML), map[string]string{
			"Content-Type": "application/xml",
		}),
	)
	if err != nil {
		t.Fatal(err)
	}
	clearResp.Body.Close()
	helpers.AssertStatus(t, clearResp, http.StatusOK)

	// Then getting the config returns empty
	getResp, err := http.DefaultClient.Do(get(srv, "/replace-bucket?notification"))
	if err != nil {
		t.Fatal(err)
	}
	defer getResp.Body.Close()

	var cfg struct {
		XMLName      xml.Name `xml:"NotificationConfiguration"`
		QueueConfigs []struct {
			ID string `xml:"Id"`
		} `xml:"QueueConfiguration"`
	}
	body, _ := io.ReadAll(getResp.Body)
	if err := xml.Unmarshal(body, &cfg); err != nil {
		t.Fatalf("expected valid XML after clear, got %q: %v", body, err)
	}
	if len(cfg.QueueConfigs) != 0 {
		t.Errorf("expected 0 QueueConfigurations after clear, got %d", len(cfg.QueueConfigs))
	}
}

func put(srv *helpers.TestServer, path string, body []byte, headers map[string]string) *http.Request {
	var bodyReader io.Reader
	if body != nil {
		bodyReader = bytes.NewReader(body)
	}
	req, _ := http.NewRequest(http.MethodPut, srv.URL+path, bodyReader)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	return req
}

func get(srv *helpers.TestServer, path string) *http.Request {
	req, _ := http.NewRequest(http.MethodGet, srv.URL+path, nil)
	return req
}

func head(srv *helpers.TestServer, path string) *http.Request {
	req, _ := http.NewRequest(http.MethodHead, srv.URL+path, nil)
	return req
}

func del(srv *helpers.TestServer, path string) *http.Request {
	req, _ := http.NewRequest(http.MethodDelete, srv.URL+path, nil)
	return req
}

func mustReq(method, url string, body io.Reader, headers map[string]string) *http.Request {
	req, err := http.NewRequest(method, url, body)
	if err != nil {
		panic(fmt.Sprintf("mustReq: %v", err))
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	return req
}

// Ensure xml package is used (for helpers.DecodeXML result types above).
var _ = xml.Unmarshal
var _ = strings.Contains

// ---- S3 Event Notifications → SQS ------------------------------------------

// sqsCall is a minimal SQS JSON helper for cross-service notification tests.
func sqsCall(t *testing.T, srv *helpers.TestServer, action string, body map[string]interface{}) *http.Response {
	t.Helper()
	raw, _ := json.Marshal(body)
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/x-amz-json-1.0")
	req.Header.Set("X-Amz-Target", "AmazonSQS."+action)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("sqsCall %s: %v", action, err)
	}
	return resp
}

func TestPutObject_sendsNotificationToSQS(t *testing.T) {
	// Given an SQS queue and an S3 bucket with notifications pointing to it
	srv := helpers.NewTestServer(t)
	createBucket(t, srv, "event-bucket")

	// Create the SQS queue
	createResp := sqsCall(t, srv, "CreateQueue", map[string]interface{}{
		"QueueName": "event-queue",
	})
	defer createResp.Body.Close()
	helpers.AssertStatus(t, createResp, http.StatusOK)
	var qResult struct {
		QueueUrl string `json:"QueueUrl"`
	}
	helpers.DecodeJSON(t, createResp, &qResult)

	// Configure notifications: ObjectCreated → SQS queue
	notifXML := `<?xml version="1.0" encoding="UTF-8"?>
<NotificationConfiguration xmlns="http://s3.amazonaws.com/doc/2006-03-01/">
  <QueueConfiguration>
    <Id>put-to-queue</Id>
    <Queue>arn:aws:sqs:us-east-1:000000000000:event-queue</Queue>
    <Event>s3:ObjectCreated:*</Event>
  </QueueConfiguration>
</NotificationConfiguration>`

	putNCResp, _ := http.DefaultClient.Do(
		put(srv, "/event-bucket?notification", []byte(notifXML), map[string]string{"Content-Type": "application/xml"}),
	)
	putNCResp.Body.Close()
	helpers.AssertStatus(t, putNCResp, http.StatusOK)

	// When we upload an object
	putObjResp, _ := http.DefaultClient.Do(
		put(srv, "/event-bucket/docs/hello.txt", []byte("hello world"), map[string]string{"Content-Type": "text/plain"}),
	)
	putObjResp.Body.Close()
	helpers.AssertStatus(t, putObjResp, http.StatusOK)

	// Then a notification message appears in the SQS queue (async — small retry window)
	var msgBody string
	for i := 0; i < 20; i++ {
		recvResp := sqsCall(t, srv, "ReceiveMessage", map[string]interface{}{
			"QueueUrl":            qResult.QueueUrl,
			"MaxNumberOfMessages": 1,
		})
		var recvResult struct {
			Messages []struct {
				Body string `json:"Body"`
			} `json:"Messages"`
		}
		helpers.DecodeJSON(t, recvResp, &recvResult)
		recvResp.Body.Close()
		if len(recvResult.Messages) > 0 {
			msgBody = recvResult.Messages[0].Body
			break
		}
		// Bus is async — give it a moment
		time.Sleep(5 * time.Millisecond)
	}

	if msgBody == "" {
		t.Fatal("expected a notification message in SQS, got none after retries")
	}

	// The message body should be S3 notification event JSON
	var eventEnvelope struct {
		Records []struct {
			EventName string `json:"eventName"`
			S3        struct {
				Bucket struct {
					Name string `json:"name"`
				} `json:"bucket"`
				Object struct {
					Key  string `json:"key"`
					Size int64  `json:"size"`
				} `json:"object"`
			} `json:"s3"`
		} `json:"Records"`
	}
	if err := json.Unmarshal([]byte(msgBody), &eventEnvelope); err != nil {
		t.Fatalf("expected valid JSON event, got %q: %v", msgBody, err)
	}
	if len(eventEnvelope.Records) != 1 {
		t.Fatalf("expected 1 record, got %d", len(eventEnvelope.Records))
	}
	rec := eventEnvelope.Records[0]
	if rec.EventName != "ObjectCreated:Put" {
		t.Errorf("expected eventName ObjectCreated:Put, got %q", rec.EventName)
	}
	if rec.S3.Bucket.Name != "event-bucket" {
		t.Errorf("expected bucket event-bucket, got %q", rec.S3.Bucket.Name)
	}
	if rec.S3.Object.Key != "docs/hello.txt" {
		t.Errorf("expected key docs/hello.txt, got %q", rec.S3.Object.Key)
	}
	if rec.S3.Object.Size != 11 {
		t.Errorf("expected size 11, got %d", rec.S3.Object.Size)
	}
}

func TestDeleteObject_sendsNotificationToSQS(t *testing.T) {
	// Given an SQS queue and an S3 bucket with ObjectRemoved notifications
	srv := helpers.NewTestServer(t)
	createBucket(t, srv, "del-bucket")

	createResp := sqsCall(t, srv, "CreateQueue", map[string]interface{}{
		"QueueName": "del-queue",
	})
	defer createResp.Body.Close()
	var qResult struct {
		QueueUrl string `json:"QueueUrl"`
	}
	helpers.DecodeJSON(t, createResp, &qResult)

	notifXML := `<?xml version="1.0" encoding="UTF-8"?>
<NotificationConfiguration xmlns="http://s3.amazonaws.com/doc/2006-03-01/">
  <QueueConfiguration>
    <Id>del-config</Id>
    <Queue>arn:aws:sqs:us-east-1:000000000000:del-queue</Queue>
    <Event>s3:ObjectRemoved:*</Event>
  </QueueConfiguration>
</NotificationConfiguration>`

	putNCResp, _ := http.DefaultClient.Do(
		put(srv, "/del-bucket?notification", []byte(notifXML), map[string]string{"Content-Type": "application/xml"}),
	)
	putNCResp.Body.Close()

	// Put then delete an object
	putObjResp, _ := http.DefaultClient.Do(
		put(srv, "/del-bucket/remove-me.txt", []byte("bye"), map[string]string{"Content-Type": "text/plain"}),
	)
	putObjResp.Body.Close()

	// Drain any ObjectCreated messages (not subscribed but just in case)
	time.Sleep(10 * time.Millisecond)

	// When we delete the object
	delResp, _ := http.DefaultClient.Do(del(srv, "/del-bucket/remove-me.txt"))
	delResp.Body.Close()
	helpers.AssertStatus(t, delResp, http.StatusNoContent)

	// Then a notification appears
	var msgBody string
	for i := 0; i < 20; i++ {
		recvResp := sqsCall(t, srv, "ReceiveMessage", map[string]interface{}{
			"QueueUrl":            qResult.QueueUrl,
			"MaxNumberOfMessages": 1,
		})
		var recvResult struct {
			Messages []struct {
				Body string `json:"Body"`
			} `json:"Messages"`
		}
		helpers.DecodeJSON(t, recvResp, &recvResult)
		recvResp.Body.Close()
		if len(recvResult.Messages) > 0 {
			msgBody = recvResult.Messages[0].Body
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	if msgBody == "" {
		t.Fatal("expected a delete notification message in SQS, got none")
	}

	var eventEnvelope struct {
		Records []struct {
			EventName string `json:"eventName"`
			S3        struct {
				Object struct {
					Key string `json:"key"`
				} `json:"object"`
			} `json:"s3"`
		} `json:"Records"`
	}
	if err := json.Unmarshal([]byte(msgBody), &eventEnvelope); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if len(eventEnvelope.Records) != 1 {
		t.Fatalf("expected 1 record, got %d", len(eventEnvelope.Records))
	}
	if eventEnvelope.Records[0].EventName != "ObjectRemoved:Delete" {
		t.Errorf("expected ObjectRemoved:Delete, got %q", eventEnvelope.Records[0].EventName)
	}
}

func TestPutObject_notificationFilteredByPrefix(t *testing.T) {
	// Given a notification config with prefix filter "uploads/"
	srv := helpers.NewTestServer(t)
	createBucket(t, srv, "filter-bucket")

	createResp := sqsCall(t, srv, "CreateQueue", map[string]interface{}{
		"QueueName": "filter-queue",
	})
	defer createResp.Body.Close()
	var qResult struct {
		QueueUrl string `json:"QueueUrl"`
	}
	helpers.DecodeJSON(t, createResp, &qResult)

	notifXML := `<?xml version="1.0" encoding="UTF-8"?>
<NotificationConfiguration xmlns="http://s3.amazonaws.com/doc/2006-03-01/">
  <QueueConfiguration>
    <Id>filter-config</Id>
    <Queue>arn:aws:sqs:us-east-1:000000000000:filter-queue</Queue>
    <Event>s3:ObjectCreated:*</Event>
    <Filter>
      <S3Key>
        <FilterRule><Name>prefix</Name><Value>uploads/</Value></FilterRule>
      </S3Key>
    </Filter>
  </QueueConfiguration>
</NotificationConfiguration>`

	putNCResp, _ := http.DefaultClient.Do(
		put(srv, "/filter-bucket?notification", []byte(notifXML), map[string]string{"Content-Type": "application/xml"}),
	)
	putNCResp.Body.Close()

	// When we upload an object that does NOT match the prefix
	notMatchResp, _ := http.DefaultClient.Do(
		put(srv, "/filter-bucket/other/file.txt", []byte("nope"), map[string]string{"Content-Type": "text/plain"}),
	)
	notMatchResp.Body.Close()

	time.Sleep(20 * time.Millisecond)

	// Then no message in queue
	recvResp := sqsCall(t, srv, "ReceiveMessage", map[string]interface{}{
		"QueueUrl":            qResult.QueueUrl,
		"MaxNumberOfMessages": 1,
	})
	var recvResult struct {
		Messages []struct{ Body string } `json:"Messages"`
	}
	helpers.DecodeJSON(t, recvResp, &recvResult)
	recvResp.Body.Close()
	if len(recvResult.Messages) != 0 {
		t.Errorf("expected no messages for non-matching prefix, got %d", len(recvResult.Messages))
	}

	// When we upload an object that DOES match the prefix
	matchResp, _ := http.DefaultClient.Do(
		put(srv, "/filter-bucket/uploads/doc.txt", []byte("yes"), map[string]string{"Content-Type": "text/plain"}),
	)
	matchResp.Body.Close()

	var msgBody string
	for i := 0; i < 20; i++ {
		recvResp := sqsCall(t, srv, "ReceiveMessage", map[string]interface{}{
			"QueueUrl":            qResult.QueueUrl,
			"MaxNumberOfMessages": 1,
		})
		var r2 struct {
			Messages []struct{ Body string } `json:"Messages"`
		}
		helpers.DecodeJSON(t, recvResp, &r2)
		recvResp.Body.Close()
		if len(r2.Messages) > 0 {
			msgBody = r2.Messages[0].Body
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	if msgBody == "" {
		t.Fatal("expected notification for uploads/doc.txt, got none")
	}
}

// ---- S3 Event Notifications → Lambda ----------------------------------------

func TestPutObject_sendsNotificationToLambda(t *testing.T) {
	// Given an S3 bucket with a Lambda notification config and a pre-seeded function
	srv := helpers.NewTestServer(t)
	createBucket(t, srv, "lambda-notify-bucket")

	// Pre-seed a Lambda function directly in the state store
	// (bypassing HTTP since Lambda CRUD is not yet implemented)
	fn := map[string]interface{}{
		"name":        "s3-handler",
		"arn":         "arn:aws:lambda:us-east-1:000000000000:function:s3-handler",
		"runtime":     "nodejs20.x",
		"handler":     "index.handler",
		"state":       "Active",
		"timeout":     30,
		"memory_size": 128,
	}
	fnJSON, _ := json.Marshal(fn)
	if err := srv.Store.Set(context.Background(), "lambda:functions", "us-east-1/s3-handler", string(fnJSON)); err != nil {
		t.Fatalf("pre-seeding function: %v", err)
	}

	// Configure S3 → Lambda notification
	notifXML := `<?xml version="1.0" encoding="UTF-8"?>
<NotificationConfiguration xmlns="http://s3.amazonaws.com/doc/2006-03-01/">
  <CloudFunctionConfiguration>
    <Id>invoke-on-put</Id>
    <CloudFunction>arn:aws:lambda:us-east-1:000000000000:function:s3-handler</CloudFunction>
    <Event>s3:ObjectCreated:*</Event>
  </CloudFunctionConfiguration>
</NotificationConfiguration>`

	putNCResp, _ := http.DefaultClient.Do(
		put(srv, "/lambda-notify-bucket?notification", []byte(notifXML), map[string]string{"Content-Type": "application/xml"}),
	)
	putNCResp.Body.Close()
	helpers.AssertStatus(t, putNCResp, http.StatusOK)

	// When we upload an object
	putObjResp, _ := http.DefaultClient.Do(
		put(srv, "/lambda-notify-bucket/trigger.txt", []byte("hello"), map[string]string{"Content-Type": "text/plain"}),
	)
	putObjResp.Body.Close()
	helpers.AssertStatus(t, putObjResp, http.StatusOK)

	// Then an invocation record appears in lambda:invocations (async — small retry window)
	var gotInvocation bool
	for i := 0; i < 40; i++ {
		kvs, err := srv.Store.Scan(context.Background(), "lambda:invocations", "us-east-1/s3-handler:")
		if err == nil && len(kvs) > 0 {
			gotInvocation = true
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	if !gotInvocation {
		t.Fatal("expected Lambda invocation record in state store, got none after retries")
	}
}

func TestDeleteObject_sendsNotificationToLambda(t *testing.T) {
	// Given a bucket with ObjectRemoved → Lambda notification and a pre-seeded function
	srv := helpers.NewTestServer(t)
	createBucket(t, srv, "lambda-del-bucket")

	fn := map[string]interface{}{
		"name":        "del-handler",
		"arn":         "arn:aws:lambda:us-east-1:000000000000:function:del-handler",
		"runtime":     "nodejs20.x",
		"handler":     "index.handler",
		"state":       "Active",
		"timeout":     30,
		"memory_size": 128,
	}
	fnJSON, _ := json.Marshal(fn)
	if err := srv.Store.Set(context.Background(), "lambda:functions", "us-east-1/del-handler", string(fnJSON)); err != nil {
		t.Fatalf("pre-seeding function: %v", err)
	}

	notifXML := `<?xml version="1.0" encoding="UTF-8"?>
<NotificationConfiguration xmlns="http://s3.amazonaws.com/doc/2006-03-01/">
  <CloudFunctionConfiguration>
    <Id>invoke-on-delete</Id>
    <CloudFunction>arn:aws:lambda:us-east-1:000000000000:function:del-handler</CloudFunction>
    <Event>s3:ObjectRemoved:*</Event>
  </CloudFunctionConfiguration>
</NotificationConfiguration>`

	putNCResp, _ := http.DefaultClient.Do(
		put(srv, "/lambda-del-bucket?notification", []byte(notifXML), map[string]string{"Content-Type": "application/xml"}),
	)
	putNCResp.Body.Close()
	helpers.AssertStatus(t, putNCResp, http.StatusOK)

	// Upload then delete an object
	putObjResp, _ := http.DefaultClient.Do(
		put(srv, "/lambda-del-bucket/item.txt", []byte("data"), map[string]string{"Content-Type": "text/plain"}),
	)
	putObjResp.Body.Close()

	delResp, _ := http.DefaultClient.Do(del(srv, "/lambda-del-bucket/item.txt"))
	delResp.Body.Close()
	helpers.AssertStatus(t, delResp, http.StatusNoContent)

	// Then an invocation record appears
	var gotInvocation bool
	for i := 0; i < 40; i++ {
		kvs, err := srv.Store.Scan(context.Background(), "lambda:invocations", "us-east-1/del-handler:")
		if err == nil && len(kvs) > 0 {
			gotInvocation = true
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	if !gotInvocation {
		t.Fatal("expected Lambda invocation record for delete event, got none after retries")
	}
}

func TestPutObject_lambdaNotification_functionNotFound(t *testing.T) {
	// Given a bucket configured to send to a Lambda that doesn't exist
	srv := helpers.NewTestServer(t)
	createBucket(t, srv, "missing-fn-bucket")

	notifXML := `<?xml version="1.0" encoding="UTF-8"?>
<NotificationConfiguration xmlns="http://s3.amazonaws.com/doc/2006-03-01/">
  <CloudFunctionConfiguration>
    <Id>invoke-missing</Id>
    <CloudFunction>arn:aws:lambda:us-east-1:000000000000:function:does-not-exist</CloudFunction>
    <Event>s3:ObjectCreated:*</Event>
  </CloudFunctionConfiguration>
</NotificationConfiguration>`

	putNCResp, _ := http.DefaultClient.Do(
		put(srv, "/missing-fn-bucket?notification", []byte(notifXML), map[string]string{"Content-Type": "application/xml"}),
	)
	putNCResp.Body.Close()
	helpers.AssertStatus(t, putNCResp, http.StatusOK)

	// When we upload an object — dispatcher should log a warning but not panic
	putObjResp, _ := http.DefaultClient.Do(
		put(srv, "/missing-fn-bucket/test.txt", []byte("hi"), map[string]string{"Content-Type": "text/plain"}),
	)
	putObjResp.Body.Close()

	// Then the upload itself succeeds (graceful degradation)
	helpers.AssertStatus(t, putObjResp, http.StatusOK)

	// And no invocation record is written for the missing function
	time.Sleep(20 * time.Millisecond)
	kvs, err := srv.Store.Scan(context.Background(), "lambda:invocations", "us-east-1/does-not-exist:")
	if err != nil {
		t.Fatalf("scan error: %v", err)
	}
	if len(kvs) != 0 {
		t.Errorf("expected no invocation records for missing function, got %d", len(kvs))
	}
}

// ---- Bucket Policy ---------------------------------------------------------

func TestPutBucketPolicy_success(t *testing.T) {
	// Given: a bucket exists
	srv := helpers.NewTestServer(t)
	createBucket(t, srv, "policy-bucket")

	// When: PutBucketPolicy is called with a valid JSON policy
	policy := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":"*","Action":"s3:GetObject","Resource":"arn:aws:s3:::policy-bucket/*"}]}`
	resp, err := http.DefaultClient.Do(put(srv, "/policy-bucket?policy", []byte(policy), nil))
	if err != nil {
		t.Fatalf("PutBucketPolicy: %v", err)
	}
	defer resp.Body.Close()

	// Then: 200 OK
	helpers.AssertStatus(t, resp, http.StatusOK)
}

func TestPutBucketPolicy_noSuchBucket(t *testing.T) {
	// Given: no bucket exists
	srv := helpers.NewTestServer(t)

	// When: PutBucketPolicy is called on a non-existent bucket
	policy := `{"Version":"2012-10-17","Statement":[]}`
	resp, err := http.DefaultClient.Do(put(srv, "/no-such-bucket?policy", []byte(policy), nil))
	if err != nil {
		t.Fatalf("PutBucketPolicy: %v", err)
	}
	defer resp.Body.Close()

	// Then: 404 NoSuchBucket
	helpers.AssertStatus(t, resp, http.StatusNotFound)
	helpers.AssertXMLError(t, resp, "NoSuchBucket")
}

func TestPutBucketPolicy_emptyBody(t *testing.T) {
	// Given: a bucket exists
	srv := helpers.NewTestServer(t)
	createBucket(t, srv, "policy-bucket")

	// When: PutBucketPolicy is called with an empty body
	resp, err := http.DefaultClient.Do(put(srv, "/policy-bucket?policy", nil, nil))
	if err != nil {
		t.Fatalf("PutBucketPolicy: %v", err)
	}
	defer resp.Body.Close()

	// Then: 400 MalformedPolicy
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
}

func TestGetBucketPolicy_success(t *testing.T) {
	// Given: a bucket with a policy
	srv := helpers.NewTestServer(t)
	createBucket(t, srv, "policy-bucket")
	policy := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":"*","Action":"s3:GetObject","Resource":"arn:aws:s3:::policy-bucket/*"}]}`
	resp, err := http.DefaultClient.Do(put(srv, "/policy-bucket?policy", []byte(policy), nil))
	if err != nil {
		t.Fatalf("PutBucketPolicy: %v", err)
	}
	resp.Body.Close()

	// When: GetBucketPolicy is called
	resp, err = http.DefaultClient.Do(get(srv, "/policy-bucket?policy"))
	if err != nil {
		t.Fatalf("GetBucketPolicy: %v", err)
	}
	defer resp.Body.Close()

	// Then: 200 OK with the policy JSON
	helpers.AssertStatus(t, resp, http.StatusOK)
	body, _ := io.ReadAll(resp.Body)
	if string(body) != policy {
		t.Errorf("expected policy %q, got %q", policy, string(body))
	}
}

func TestGetBucketPolicy_noPolicySet(t *testing.T) {
	// Given: a bucket with no policy
	srv := helpers.NewTestServer(t)
	createBucket(t, srv, "policy-bucket")

	// When: GetBucketPolicy is called
	resp, err := http.DefaultClient.Do(get(srv, "/policy-bucket?policy"))
	if err != nil {
		t.Fatalf("GetBucketPolicy: %v", err)
	}
	defer resp.Body.Close()

	// Then: 404 NoSuchBucketPolicy
	helpers.AssertStatus(t, resp, http.StatusNotFound)
	helpers.AssertXMLError(t, resp, "NoSuchBucketPolicy")
}

func TestGetBucketPolicy_noSuchBucket(t *testing.T) {
	// Given: no bucket exists
	srv := helpers.NewTestServer(t)

	// When: GetBucketPolicy is called
	resp, err := http.DefaultClient.Do(get(srv, "/no-such-bucket?policy"))
	if err != nil {
		t.Fatalf("GetBucketPolicy: %v", err)
	}
	defer resp.Body.Close()

	// Then: 404 NoSuchBucket
	helpers.AssertStatus(t, resp, http.StatusNotFound)
	helpers.AssertXMLError(t, resp, "NoSuchBucket")
}

func TestDeleteBucketPolicy_success(t *testing.T) {
	// Given: a bucket with a policy
	srv := helpers.NewTestServer(t)
	createBucket(t, srv, "policy-bucket")
	policy := `{"Version":"2012-10-17","Statement":[]}`
	resp, err := http.DefaultClient.Do(put(srv, "/policy-bucket?policy", []byte(policy), nil))
	if err != nil {
		t.Fatalf("PutBucketPolicy: %v", err)
	}
	resp.Body.Close()

	// When: DeleteBucketPolicy is called
	resp, err = http.DefaultClient.Do(del(srv, "/policy-bucket?policy"))
	if err != nil {
		t.Fatalf("DeleteBucketPolicy: %v", err)
	}
	defer resp.Body.Close()

	// Then: 204 No Content
	helpers.AssertStatus(t, resp, http.StatusNoContent)

	// And: GetBucketPolicy returns 404
	resp2, err := http.DefaultClient.Do(get(srv, "/policy-bucket?policy"))
	if err != nil {
		t.Fatalf("GetBucketPolicy after delete: %v", err)
	}
	defer resp2.Body.Close()
	helpers.AssertStatus(t, resp2, http.StatusNotFound)
}

func TestDeleteBucketPolicy_noPolicySet(t *testing.T) {
	// Given: a bucket with no policy
	srv := helpers.NewTestServer(t)
	createBucket(t, srv, "policy-bucket")

	// When: DeleteBucketPolicy is called
	resp, err := http.DefaultClient.Do(del(srv, "/policy-bucket?policy"))
	if err != nil {
		t.Fatalf("DeleteBucketPolicy: %v", err)
	}
	defer resp.Body.Close()

	// Then: 204 No Content (idempotent)
	helpers.AssertStatus(t, resp, http.StatusNoContent)
}

// ---- Object Tagging --------------------------------------------------------

func TestPutObjectTagging_success(t *testing.T) {
	// Given: a bucket and object exist
	srv := helpers.NewTestServer(t)
	createBucket(t, srv, "tag-bucket")
	putObject(t, srv, "tag-bucket", "tagged.txt", []byte("hello"), "text/plain")

	// When: PutObjectTagging is called with two tags
	taggingXML := `<Tagging><TagSet><Tag><Key>env</Key><Value>prod</Value></Tag><Tag><Key>team</Key><Value>platform</Value></Tag></TagSet></Tagging>`
	resp, err := http.DefaultClient.Do(put(srv, "/tag-bucket/tagged.txt?tagging", []byte(taggingXML), map[string]string{"Content-Type": "application/xml"}))
	if err != nil {
		t.Fatalf("PutObjectTagging: %v", err)
	}
	defer resp.Body.Close()

	// Then: 200 OK
	helpers.AssertStatus(t, resp, http.StatusOK)
}

func TestGetObjectTagging_success(t *testing.T) {
	// Given: an object exists with tags
	srv := helpers.NewTestServer(t)
	createBucket(t, srv, "tag-bucket")
	putObject(t, srv, "tag-bucket", "tagged.txt", []byte("hello"), "text/plain")
	taggingXML := `<Tagging><TagSet><Tag><Key>env</Key><Value>prod</Value></Tag></TagSet></Tagging>`
	putResp, err := http.DefaultClient.Do(put(srv, "/tag-bucket/tagged.txt?tagging", []byte(taggingXML), map[string]string{"Content-Type": "application/xml"}))
	if err != nil {
		t.Fatalf("PutObjectTagging setup: %v", err)
	}
	putResp.Body.Close()

	// When: GetObjectTagging is called
	resp, err := http.DefaultClient.Do(get(srv, "/tag-bucket/tagged.txt?tagging"))
	if err != nil {
		t.Fatalf("GetObjectTagging: %v", err)
	}
	defer resp.Body.Close()

	// Then: 200 with the tag in the response
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		XMLName xml.Name `xml:"Tagging"`
		TagSet  struct {
			Tags []struct {
				Key   string `xml:"Key"`
				Value string `xml:"Value"`
			} `xml:"Tag"`
		} `xml:"TagSet"`
	}
	body, _ := io.ReadAll(resp.Body)
	if err := xml.Unmarshal(body, &result); err != nil {
		t.Fatalf("unmarshal tagging response: %v", err)
	}
	if len(result.TagSet.Tags) != 1 || result.TagSet.Tags[0].Key != "env" || result.TagSet.Tags[0].Value != "prod" {
		t.Errorf("unexpected tags: %+v", result.TagSet.Tags)
	}
}

// TestGetObjectTagging_missingBucketReturnsNoSuchBucket is the
// GetObjectTagging half of the #1635 regression: it reaches getObjectMeta
// through currentObjectForTagging without ever resolving the bucket first,
// so a nonexistent bucket used to answer NoSuchKey instead of NoSuchBucket.
func TestGetObjectTagging_missingBucketReturnsNoSuchBucket(t *testing.T) {
	// Given: no bucket named "no-such-bucket" exists at all
	srv := helpers.NewTestServer(t)

	// When: GetObjectTagging is called against a key in it
	resp, err := http.DefaultClient.Do(get(srv, "/no-such-bucket/key.txt?tagging"))
	if err != nil {
		t.Fatalf("GetObjectTagging: %v", err)
	}
	defer resp.Body.Close()

	// Then: NoSuchBucket, not NoSuchKey
	helpers.AssertStatus(t, resp, http.StatusNotFound)
	helpers.AssertXMLError(t, resp, "NoSuchBucket")
}

// TestGetObjectTagging_missingKeyInExistingBucketReturnsNoSuchKey is the
// regression guard: a missing key in a bucket that does exist must still
// answer NoSuchKey, so the bucket-existence fix cannot collapse the two
// cases in the other direction.
func TestGetObjectTagging_missingKeyInExistingBucketReturnsNoSuchKey(t *testing.T) {
	// Given: a bucket that exists, but no object in it
	srv := helpers.NewTestServer(t)
	createBucket(t, srv, "tag-bucket")

	// When: GetObjectTagging is called against a key that was never created
	resp, err := http.DefaultClient.Do(get(srv, "/tag-bucket/no-such-key.txt?tagging"))
	if err != nil {
		t.Fatalf("GetObjectTagging: %v", err)
	}
	defer resp.Body.Close()

	// Then: NoSuchKey, unchanged
	helpers.AssertStatus(t, resp, http.StatusNotFound)
	helpers.AssertXMLError(t, resp, "NoSuchKey")
}

func TestGetObjectTagging_emptyTagSet(t *testing.T) {
	// Given: an object exists with no tags set
	srv := helpers.NewTestServer(t)
	createBucket(t, srv, "tag-bucket")
	putObject(t, srv, "tag-bucket", "untagged.txt", []byte("hello"), "text/plain")

	// When: GetObjectTagging is called
	resp, err := http.DefaultClient.Do(get(srv, "/tag-bucket/untagged.txt?tagging"))
	if err != nil {
		t.Fatalf("GetObjectTagging: %v", err)
	}
	defer resp.Body.Close()

	// Then: 200 with empty TagSet
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		XMLName xml.Name `xml:"Tagging"`
		TagSet  struct {
			Tags []struct {
				Key string `xml:"Key"`
			} `xml:"Tag"`
		} `xml:"TagSet"`
	}
	body, _ := io.ReadAll(resp.Body)
	if err := xml.Unmarshal(body, &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(result.TagSet.Tags) != 0 {
		t.Errorf("expected empty TagSet, got %+v", result.TagSet.Tags)
	}
}

func TestDeleteObjectTagging_success(t *testing.T) {
	// Given: an object exists with tags
	srv := helpers.NewTestServer(t)
	createBucket(t, srv, "tag-bucket")
	putObject(t, srv, "tag-bucket", "tagged.txt", []byte("hello"), "text/plain")
	taggingXML := `<Tagging><TagSet><Tag><Key>env</Key><Value>prod</Value></Tag></TagSet></Tagging>`
	putResp, err := http.DefaultClient.Do(put(srv, "/tag-bucket/tagged.txt?tagging", []byte(taggingXML), map[string]string{"Content-Type": "application/xml"}))
	if err != nil {
		t.Fatalf("PutObjectTagging setup: %v", err)
	}
	putResp.Body.Close()

	// When: DeleteObjectTagging is called
	resp, err := http.DefaultClient.Do(del(srv, "/tag-bucket/tagged.txt?tagging"))
	if err != nil {
		t.Fatalf("DeleteObjectTagging: %v", err)
	}
	defer resp.Body.Close()

	// Then: 204 No Content
	helpers.AssertStatus(t, resp, http.StatusNoContent)

	// And: GetObjectTagging returns empty TagSet
	getResp, err := http.DefaultClient.Do(get(srv, "/tag-bucket/tagged.txt?tagging"))
	if err != nil {
		t.Fatalf("GetObjectTagging: %v", err)
	}
	defer getResp.Body.Close()
	var result struct {
		TagSet struct {
			Tags []struct {
				Key string `xml:"Key"`
			} `xml:"Tag"`
		} `xml:"TagSet"`
	}
	body, _ := io.ReadAll(getResp.Body)
	xml.Unmarshal(body, &result) //nolint:errcheck
	if len(result.TagSet.Tags) != 0 {
		t.Errorf("expected empty tags after delete, got %+v", result.TagSet.Tags)
	}
}

func TestPutObjectTagging_objectNotFound(t *testing.T) {
	// Given: bucket exists but object does not
	srv := helpers.NewTestServer(t)
	createBucket(t, srv, "tag-bucket")

	// When: PutObjectTagging is called for missing key
	taggingXML := `<Tagging><TagSet></TagSet></Tagging>`
	resp, err := http.DefaultClient.Do(put(srv, "/tag-bucket/no-such-key?tagging", []byte(taggingXML), map[string]string{"Content-Type": "application/xml"}))
	if err != nil {
		t.Fatalf("PutObjectTagging: %v", err)
	}
	defer resp.Body.Close()

	// Then: 404 NoSuchKey
	helpers.AssertStatus(t, resp, http.StatusNotFound)
}

// ---- Bucket Tagging --------------------------------------------------------

func TestPutBucketTagging_success(t *testing.T) {
	// Given: a bucket exists
	srv := helpers.NewTestServer(t)
	createBucket(t, srv, "btag-bucket")

	// When: PutBucketTagging is called
	taggingXML := `<Tagging><TagSet><Tag><Key>project</Key><Value>overcast</Value></Tag></TagSet></Tagging>`
	resp, err := http.DefaultClient.Do(put(srv, "/btag-bucket?tagging", []byte(taggingXML), map[string]string{"Content-Type": "application/xml"}))
	if err != nil {
		t.Fatalf("PutBucketTagging: %v", err)
	}
	defer resp.Body.Close()

	// Then: 204 No Content
	helpers.AssertStatus(t, resp, http.StatusNoContent)
}

func TestGetBucketTagging_success(t *testing.T) {
	// Given: a bucket exists with a tag
	srv := helpers.NewTestServer(t)
	createBucket(t, srv, "btag-bucket")
	taggingXML := `<Tagging><TagSet><Tag><Key>project</Key><Value>overcast</Value></Tag></TagSet></Tagging>`
	putResp, err := http.DefaultClient.Do(put(srv, "/btag-bucket?tagging", []byte(taggingXML), map[string]string{"Content-Type": "application/xml"}))
	if err != nil {
		t.Fatalf("PutBucketTagging setup: %v", err)
	}
	putResp.Body.Close()

	// When: GetBucketTagging is called
	resp, err := http.DefaultClient.Do(get(srv, "/btag-bucket?tagging"))
	if err != nil {
		t.Fatalf("GetBucketTagging: %v", err)
	}
	defer resp.Body.Close()

	// Then: 200 with tag set
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		XMLName xml.Name `xml:"Tagging"`
		TagSet  struct {
			Tags []struct {
				Key   string `xml:"Key"`
				Value string `xml:"Value"`
			} `xml:"Tag"`
		} `xml:"TagSet"`
	}
	body, _ := io.ReadAll(resp.Body)
	if err := xml.Unmarshal(body, &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(result.TagSet.Tags) != 1 || result.TagSet.Tags[0].Key != "project" {
		t.Errorf("unexpected tags: %+v", result.TagSet.Tags)
	}
}

func TestDeleteBucketTagging_success(t *testing.T) {
	// Given: a bucket with a tag
	srv := helpers.NewTestServer(t)
	createBucket(t, srv, "btag-bucket")
	taggingXML := `<Tagging><TagSet><Tag><Key>k</Key><Value>v</Value></Tag></TagSet></Tagging>`
	putResp, err := http.DefaultClient.Do(put(srv, "/btag-bucket?tagging", []byte(taggingXML), map[string]string{"Content-Type": "application/xml"}))
	if err != nil {
		t.Fatalf("PutBucketTagging setup: %v", err)
	}
	putResp.Body.Close()

	// When: DeleteBucketTagging is called
	resp, err := http.DefaultClient.Do(del(srv, "/btag-bucket?tagging"))
	if err != nil {
		t.Fatalf("DeleteBucketTagging: %v", err)
	}
	defer resp.Body.Close()

	// Then: 204 No Content
	helpers.AssertStatus(t, resp, http.StatusNoContent)
}

func TestGetBucketTagging_noTags(t *testing.T) {
	// Given: a bucket with no tags
	srv := helpers.NewTestServer(t)
	createBucket(t, srv, "btag-bucket")

	// When: GetBucketTagging is called
	resp, err := http.DefaultClient.Do(get(srv, "/btag-bucket?tagging"))
	if err != nil {
		t.Fatalf("GetBucketTagging: %v", err)
	}
	defer resp.Body.Close()

	// Then: AWS reports that no bucket tag set exists.
	helpers.AssertStatus(t, resp, http.StatusNotFound)
	assertS3ErrorCode(t, resp, "NoSuchTagSet")
}

// ---- Bucket Versioning -----------------------------------------------------

func TestPutBucketVersioning_enable(t *testing.T) {
	// Given: a bucket exists
	srv := helpers.NewTestServer(t)
	createBucket(t, srv, "ver-bucket")

	// When: PutBucketVersioning enables versioning
	versioningXML := `<VersioningConfiguration><Status>Enabled</Status></VersioningConfiguration>`
	resp, err := http.DefaultClient.Do(put(srv, "/ver-bucket?versioning", []byte(versioningXML), map[string]string{"Content-Type": "application/xml"}))
	if err != nil {
		t.Fatalf("PutBucketVersioning: %v", err)
	}
	defer resp.Body.Close()

	// Then: 200 OK
	helpers.AssertStatus(t, resp, http.StatusOK)
}

func TestGetBucketVersioning_enabled(t *testing.T) {
	// Given: a bucket with versioning enabled
	srv := helpers.NewTestServer(t)
	createBucket(t, srv, "ver-bucket")
	versioningXML := `<VersioningConfiguration><Status>Enabled</Status></VersioningConfiguration>`
	putResp, err := http.DefaultClient.Do(put(srv, "/ver-bucket?versioning", []byte(versioningXML), map[string]string{"Content-Type": "application/xml"}))
	if err != nil {
		t.Fatalf("PutBucketVersioning setup: %v", err)
	}
	putResp.Body.Close()

	// When: GetBucketVersioning is called
	resp, err := http.DefaultClient.Do(get(srv, "/ver-bucket?versioning"))
	if err != nil {
		t.Fatalf("GetBucketVersioning: %v", err)
	}
	defer resp.Body.Close()

	// Then: 200 with Status=Enabled
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		XMLName xml.Name `xml:"VersioningConfiguration"`
		Status  string   `xml:"Status"`
	}
	body, _ := io.ReadAll(resp.Body)
	if err := xml.Unmarshal(body, &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if result.Status != "Enabled" {
		t.Errorf("expected Status=Enabled, got %q", result.Status)
	}
}

func TestGetBucketVersioning_notConfigured(t *testing.T) {
	// Given: a bucket with no versioning config
	srv := helpers.NewTestServer(t)
	createBucket(t, srv, "ver-bucket")

	// When: GetBucketVersioning is called
	resp, err := http.DefaultClient.Do(get(srv, "/ver-bucket?versioning"))
	if err != nil {
		t.Fatalf("GetBucketVersioning: %v", err)
	}
	defer resp.Body.Close()

	// Then: 200 with empty Status (AWS returns empty VersioningConfiguration)
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		XMLName xml.Name `xml:"VersioningConfiguration"`
		Status  string   `xml:"Status"`
	}
	body, _ := io.ReadAll(resp.Body)
	xml.Unmarshal(body, &result) //nolint:errcheck
	if result.Status != "" {
		t.Errorf("expected empty Status for unconfigured bucket, got %q", result.Status)
	}
}

func TestPutBucketVersioning_suspend(t *testing.T) {
	// Given: a bucket with versioning enabled
	srv := helpers.NewTestServer(t)
	createBucket(t, srv, "ver-bucket")
	vEnable := `<VersioningConfiguration><Status>Enabled</Status></VersioningConfiguration>`
	r, err := http.DefaultClient.Do(put(srv, "/ver-bucket?versioning", []byte(vEnable), map[string]string{"Content-Type": "application/xml"}))
	if err != nil {
		t.Fatalf("PutBucketVersioning enable: %v", err)
	}
	r.Body.Close()

	// When: PutBucketVersioning suspends versioning
	vSuspend := `<VersioningConfiguration><Status>Suspended</Status></VersioningConfiguration>`
	resp, err := http.DefaultClient.Do(put(srv, "/ver-bucket?versioning", []byte(vSuspend), map[string]string{"Content-Type": "application/xml"}))
	if err != nil {
		t.Fatalf("PutBucketVersioning suspend: %v", err)
	}
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)

	// And: GetBucketVersioning reflects Suspended
	getResp, err := http.DefaultClient.Do(get(srv, "/ver-bucket?versioning"))
	if err != nil {
		t.Fatalf("GetBucketVersioning: %v", err)
	}
	defer getResp.Body.Close()
	var result struct {
		Status string `xml:"Status"`
	}
	body, _ := io.ReadAll(getResp.Body)
	xml.Unmarshal(body, &result) //nolint:errcheck
	if result.Status != "Suspended" {
		t.Errorf("expected Status=Suspended, got %q", result.Status)
	}
}

// ---- ListObjectVersions ---------------------------------------------------

// TestListObjectVersions_nonVersionedBucket verifies that objects in a bucket
// without versioning are returned as Version entries with VersionId="null".
func TestListObjectVersions_nonVersionedBucket(t *testing.T) {
	// Given: a non-versioned bucket with two objects
	srv := helpers.NewTestServer(t)
	createBucket(t, srv, "ver-list-bucket")
	putObject(t, srv, "ver-list-bucket", "a.txt", []byte("aaa"), "text/plain")
	putObject(t, srv, "ver-list-bucket", "b.txt", []byte("bbb"), "text/plain")

	// When: caller lists object versions
	result := listVersions(t, srv, "ver-list-bucket", "")

	// Then: both objects appear as Version entries with VersionId="null", IsLatest=true
	if result.Name != "ver-list-bucket" {
		t.Errorf("Name: want %q, got %q", "ver-list-bucket", result.Name)
	}
	if len(result.Versions) != 2 {
		t.Fatalf("want 2 versions, got %d", len(result.Versions))
	}
	if result.IsTruncated {
		t.Error("IsTruncated should be false")
	}
	for _, v := range result.Versions {
		if v.VersionId != "null" {
			t.Errorf("key %q: want VersionId=%q, got %q", v.Key, "null", v.VersionId)
		}
		if !v.IsLatest {
			t.Errorf("key %q: want IsLatest=true", v.Key)
		}
		if v.StorageClass != "STANDARD" {
			t.Errorf("key %q: want StorageClass=STANDARD, got %q", v.Key, v.StorageClass)
		}
	}
	// Versions should be sorted by key
	if result.Versions[0].Key != "a.txt" || result.Versions[1].Key != "b.txt" {
		t.Errorf("want keys [a.txt b.txt], got [%s %s]", result.Versions[0].Key, result.Versions[1].Key)
	}
}

// TestListObjectVersions_noSuchBucket verifies that a 404 is returned for missing buckets.
func TestListObjectVersions_noSuchBucket(t *testing.T) {
	// Given: no bucket
	srv := helpers.NewTestServer(t)

	// When: caller lists object versions on a non-existent bucket
	resp, err := http.DefaultClient.Do(get(srv, "/no-such-bucket?versions"))
	if err != nil {
		t.Fatalf("ListObjectVersions: %v", err)
	}
	defer resp.Body.Close()

	// Then: 404 NoSuchBucket
	helpers.AssertStatus(t, resp, http.StatusNotFound)
	helpers.AssertXMLError(t, resp, "NoSuchBucket")
}

// TestListObjectVersions_prefix verifies prefix filtering.
func TestListObjectVersions_prefix(t *testing.T) {
	// Given: a bucket with objects under different prefixes
	srv := helpers.NewTestServer(t)
	createBucket(t, srv, "ver-prefix-bucket")
	putObject(t, srv, "ver-prefix-bucket", "docs/readme.txt", []byte("r"), "text/plain")
	putObject(t, srv, "ver-prefix-bucket", "docs/guide.txt", []byte("g"), "text/plain")
	putObject(t, srv, "ver-prefix-bucket", "src/main.go", []byte("m"), "text/plain")

	// When: listing with prefix="docs/"
	result := listVersions(t, srv, "ver-prefix-bucket", "prefix=docs/")

	// Then: only docs/ objects are returned
	if len(result.Versions) != 2 {
		t.Fatalf("want 2 versions, got %d: %v", len(result.Versions), result.Versions)
	}
	for _, v := range result.Versions {
		if !strings.HasPrefix(v.Key, "docs/") {
			t.Errorf("unexpected key %q (expected docs/ prefix)", v.Key)
		}
	}
}

// TestListObjectVersions_maxKeys verifies pagination via max-keys.
func TestListObjectVersions_maxKeys(t *testing.T) {
	// Given: a bucket with 3 objects
	srv := helpers.NewTestServer(t)
	createBucket(t, srv, "ver-page-bucket")
	putObject(t, srv, "ver-page-bucket", "k1.txt", []byte("1"), "text/plain")
	putObject(t, srv, "ver-page-bucket", "k2.txt", []byte("2"), "text/plain")
	putObject(t, srv, "ver-page-bucket", "k3.txt", []byte("3"), "text/plain")

	// When: listing with max-keys=2
	result := listVersions(t, srv, "ver-page-bucket", "max-keys=2")

	// Then: first page has 2 versions, IsTruncated=true, NextKeyMarker set
	if len(result.Versions) != 2 {
		t.Fatalf("want 2 versions on first page, got %d", len(result.Versions))
	}
	if !result.IsTruncated {
		t.Error("IsTruncated should be true")
	}
	if result.NextKeyMarker == "" {
		t.Error("NextKeyMarker should be set when truncated")
	}
	if result.MaxKeys != 2 {
		t.Errorf("MaxKeys: want 2, got %d", result.MaxKeys)
	}

	// When: fetching next page using key-marker
	result2 := listVersions(t, srv, "ver-page-bucket", "max-keys=2&key-marker="+result.NextKeyMarker)

	// Then: second page has the remaining version
	if len(result2.Versions) != 1 {
		t.Fatalf("want 1 version on second page, got %d", len(result2.Versions))
	}
	if result2.IsTruncated {
		t.Error("IsTruncated should be false on last page")
	}
	if result2.Versions[0].Key != "k3.txt" {
		t.Errorf("want key k3.txt, got %q", result2.Versions[0].Key)
	}
}

// TestListObjectVersions_emptyBucket verifies that an empty bucket returns no versions.
func TestListObjectVersions_emptyBucket(t *testing.T) {
	// Given: an empty bucket
	srv := helpers.NewTestServer(t)
	createBucket(t, srv, "ver-empty-bucket")

	// When: listing object versions
	result := listVersions(t, srv, "ver-empty-bucket", "")

	// Then: no versions returned, not truncated
	if len(result.Versions) != 0 {
		t.Fatalf("want 0 versions, got %d", len(result.Versions))
	}
	if result.IsTruncated {
		t.Error("IsTruncated should be false for empty bucket")
	}
}

// ---- Presigned URL ----------------------------------------------------------

func TestPresignedURL_getObject(t *testing.T) {
	// Given: a server with SigV4 validation, IAM user with access key, bucket, and object
	srv := helpers.NewTestServer(t, helpers.WithSigV4Validate(true))

	// Create IAM user
	iamCall(t, srv, "CreateUser", url.Values{"UserName": {"presign-user"}})

	// Create access key and extract credentials
	akResp := iamCall(t, srv, "CreateAccessKey", url.Values{"UserName": {"presign-user"}})
	defer akResp.Body.Close()
	akBody := helpers.ReadBody(t, akResp)
	helpers.AssertStatus(t, akResp, http.StatusOK)

	accessKeyID := extractXMLTag(akBody, "AccessKeyId")
	secretAccessKey := extractXMLTag(akBody, "SecretAccessKey")
	if accessKeyID == "" || secretAccessKey == "" {
		t.Fatalf("failed to parse access key from response: %s", akBody)
	}

	// Create bucket and put an object
	createBucket(t, srv, "presign-bucket")
	putObject(t, srv, "presign-bucket", "hello.txt", []byte("presigned content"), "text/plain")

	// When: we generate a presigned GET URL and access it
	presignedURL := buildPresignedGetURL(t, srv.URL, accessKeyID, secretAccessKey,
		"us-east-1", "presign-bucket", "hello.txt", 300)

	resp, err := http.Get(presignedURL)
	if err != nil {
		t.Fatalf("GET presigned URL: %v", err)
	}
	defer resp.Body.Close()

	// Then: the object is returned correctly
	helpers.AssertStatus(t, resp, http.StatusOK)
	body := helpers.ReadBody(t, resp)
	if body != "presigned content" {
		t.Errorf("expected body %q, got %q", "presigned content", body)
	}
}

func TestPresignedURL_getObject_wrongSignature_returns403(t *testing.T) {
	// Given: a server with SigV4 validation, IAM user, bucket, object
	srv := helpers.NewTestServer(t, helpers.WithSigV4Validate(true))

	iamCall(t, srv, "CreateUser", url.Values{"UserName": {"presign-user2"}})
	akResp := iamCall(t, srv, "CreateAccessKey", url.Values{"UserName": {"presign-user2"}})
	defer akResp.Body.Close()
	akBody := helpers.ReadBody(t, akResp)
	accessKeyID := extractXMLTag(akBody, "AccessKeyId")
	secretAccessKey := extractXMLTag(akBody, "SecretAccessKey")

	createBucket(t, srv, "presign-bucket2")
	putObject(t, srv, "presign-bucket2", "hello.txt", []byte("data"), "text/plain")

	// Generate a valid presigned URL, then corrupt the signature
	presignedURL := buildPresignedGetURL(t, srv.URL, accessKeyID, secretAccessKey,
		"us-east-1", "presign-bucket2", "hello.txt", 300)
	corruptedURL := presignedURL + "ff"

	// When: we access the corrupted presigned URL
	resp, err := http.Get(corruptedURL)
	if err != nil {
		t.Fatalf("GET corrupted presigned URL: %v", err)
	}
	defer resp.Body.Close()

	// Then: 403 is returned because signature validation fails
	helpers.AssertStatus(t, resp, http.StatusForbidden)
}

func TestPresignedURL_getObject_expired_returns403(t *testing.T) {
	// Given: a server with SigV4 validation + mock clock, IAM user, bucket, object
	srv := helpers.NewTestServer(t, helpers.WithSigV4Validate(true), helpers.WithMockClock())

	iamCall(t, srv, "CreateUser", url.Values{"UserName": {"presign-user3"}})
	akResp := iamCall(t, srv, "CreateAccessKey", url.Values{"UserName": {"presign-user3"}})
	defer akResp.Body.Close()
	akBody := helpers.ReadBody(t, akResp)
	accessKeyID := extractXMLTag(akBody, "AccessKeyId")
	secretAccessKey := extractXMLTag(akBody, "SecretAccessKey")

	createBucket(t, srv, "presign-bucket3")
	putObject(t, srv, "presign-bucket3", "hello.txt", []byte("data"), "text/plain")

	// Generate a presigned URL valid for 1 second
	presignedURL := buildPresignedGetURL(t, srv.URL, accessKeyID, secretAccessKey,
		"us-east-1", "presign-bucket3", "hello.txt", 1)

	// Advance the mock clock past the expiry
	srv.Clock.Add(2 * time.Second)

	// When: we access the expired presigned URL
	resp, err := http.Get(presignedURL)
	if err != nil {
		t.Fatalf("GET expired presigned URL: %v", err)
	}
	defer resp.Body.Close()

	// Then: 403 is returned because the presigned URL has expired
	helpers.AssertStatus(t, resp, http.StatusForbidden)
}

func TestPresignedURL_getObject_stsSessionCredentials(t *testing.T) {
	// Given: SigV4 validation, IAM role, STS session credentials, bucket, object
	srv := helpers.NewTestServer(t, helpers.WithSigV4Validate(true))

	// Call STS AssumeRole to get temporary credentials
	stsResp := stsCall(t, srv, "AssumeRole", url.Values{
		"RoleArn":         {"arn:aws:iam::000000000000:role/PresignRole"},
		"RoleSessionName": {"presign-session"},
	})
	defer stsResp.Body.Close()
	helpers.AssertStatus(t, stsResp, http.StatusOK)
	stsBody := helpers.ReadBody(t, stsResp)

	tempAccessKeyID := extractXMLTag(stsBody, "AccessKeyId")
	tempSecretKey := extractXMLTag(stsBody, "SecretAccessKey")
	if tempAccessKeyID == "" || tempSecretKey == "" {
		t.Fatalf("failed to extract STS credentials: %s", stsBody)
	}

	// Create bucket and object
	createBucket(t, srv, "presign-sts-bucket")
	putObject(t, srv, "presign-sts-bucket", "sts.txt", []byte("sts presigned content"), "text/plain")

	// When: generate a presigned URL with STS session credentials and GET it
	presignedURL := buildPresignedGetURL(t, srv.URL, tempAccessKeyID, tempSecretKey,
		"us-east-1", "presign-sts-bucket", "sts.txt", 300)

	resp, err := http.Get(presignedURL)
	if err != nil {
		t.Fatalf("GET STS presigned URL: %v", err)
	}
	defer resp.Body.Close()

	// Then: the object is returned
	helpers.AssertStatus(t, resp, http.StatusOK)
	body := helpers.ReadBody(t, resp)
	if body != "sts presigned content" {
		t.Errorf("expected body %q, got %q", "sts presigned content", body)
	}
}

// ---- Presigned URL helpers --------------------------------------------------

// iamCall performs an IAM Query-protocol request.
func iamCall(t *testing.T, srv *helpers.TestServer, action string, params url.Values) *http.Response {
	t.Helper()
	if params == nil {
		params = url.Values{}
	}
	params.Set("Action", action)
	params.Set("Version", "2010-05-08")
	body := strings.NewReader(params.Encode())
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/", body)
	if err != nil {
		t.Fatalf("iamCall: new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("iamCall: do: %v", err)
	}
	return resp
}

// stsCall performs an STS Query-protocol request.
func stsCall(t *testing.T, srv *helpers.TestServer, action string, params url.Values) *http.Response {
	t.Helper()
	if params == nil {
		params = url.Values{}
	}
	params.Set("Action", action)
	params.Set("Version", "2011-06-15")
	body := strings.NewReader(params.Encode())
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/", body)
	if err != nil {
		t.Fatalf("stsCall: new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("stsCall: do: %v", err)
	}
	return resp
}

func extractXMLTag(xmlStr, tag string) string {
	start := strings.Index(xmlStr, "<"+tag+">")
	if start == -1 {
		return ""
	}
	start += len("<" + tag + ">")
	end := strings.Index(xmlStr[start:], "</"+tag+">")
	if end == -1 {
		return ""
	}
	return xmlStr[start : start+end]
}

// buildPresignedGetURL constructs a SigV4-presigned GET URL for an S3 object.
// This replicates what the AWS SDK does client-side.
func buildPresignedGetURL(t *testing.T, baseURL, accessKey, secret, region, bucket, key string, expires int) string {
	t.Helper()
	now := time.Now().UTC()
	date := now.Format("20060102")
	amzDate := now.Format("20060102T150405Z")
	scope := date + "/" + region + "/s3/aws4_request"

	u, err := url.Parse(baseURL + "/" + bucket + "/" + key)
	if err != nil {
		t.Fatalf("parse base URL: %v", err)
	}

	q := u.Query()
	q.Set("X-Amz-Algorithm", "AWS4-HMAC-SHA256")
	q.Set("X-Amz-Credential", accessKey+"/"+scope)
	q.Set("X-Amz-Date", amzDate)
	q.Set("X-Amz-Expires", strconv.Itoa(expires))
	q.Set("X-Amz-SignedHeaders", "host")
	u.RawQuery = q.Encode()

	canonicalRequest := buildCanonicalRequest("GET", u.Path, q, "host:"+u.Host, "UNSIGNED-PAYLOAD")
	stringToSign := "AWS4-HMAC-SHA256\n" + amzDate + "\n" + scope + "\n" + sha256Hex([]byte(canonicalRequest))
	signature := computeSigV4(secret, date, region, "s3", stringToSign)

	q.Set("X-Amz-Signature", signature)
	u.RawQuery = q.Encode()
	return u.String()
}

func buildCanonicalRequest(method, path string, query url.Values, canonicalHeaders, payloadHash string) string {
	type pair struct{ k, v string }
	pairs := make([]pair, 0, len(query))
	for k, vs := range query {
		if strings.EqualFold(k, "X-Amz-Signature") {
			continue
		}
		sort.Strings(vs)
		for _, v := range vs {
			pairs = append(pairs, pair{sigV4Escape(k), sigV4Escape(v)})
		}
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].k == pairs[j].k {
			return pairs[i].v < pairs[j].v
		}
		return pairs[i].k < pairs[j].k
	})
	canonicalParts := make([]string, len(pairs))
	for i, p := range pairs {
		canonicalParts[i] = p.k + "=" + p.v
	}
	return method + "\n" + path + "\n" + strings.Join(canonicalParts, "&") + "\n" + canonicalHeaders + "\n\nhost\n" + payloadHash
}

func sigV4Escape(s string) string {
	e := url.QueryEscape(s)
	e = strings.ReplaceAll(e, "+", "%20")
	e = strings.ReplaceAll(e, "*", "%2A")
	e = strings.ReplaceAll(e, "%7E", "~")
	return e
}

func computeSigV4(secret, date, region, service, stringToSign string) string {
	kDate := hmacSHA256([]byte("AWS4"+secret), date)
	kRegion := hmacSHA256(kDate, region)
	kService := hmacSHA256(kRegion, service)
	kSigning := hmacSHA256(kService, "aws4_request")
	return hex.EncodeToString(hmacSHA256(kSigning, stringToSign))
}

func hmacSHA256(key []byte, msg string) []byte {
	h := hmac.New(sha256.New, key)
	h.Write([]byte(msg))
	return h.Sum(nil)
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// TestS3VirtualHostedStyle_hostIsCaseInsensitive proves a bucket is reachable
// virtual-hosted style whatever the case of the Host, in both the bare and the
// labelled form.
//
// A hostname is case-insensitive (RFC 4343), and a bucket name is
// lowercase-only by AWS rule, so an upper-case segment in a Host can only ever
// mean a case-folded bucket name — never a different bucket. Matching it
// case-sensitively made the bucket unreachable: the extracted name kept its
// case and could match nothing, and an upper-case ".S3." label was not even
// recognised as the S3 separator, so the whole "{bucket}.s3" was taken as the
// bucket name.
func TestS3VirtualHostedStyle_hostIsCaseInsensitive(t *testing.T) {
	// Given: an ordinary bucket holding one object
	srv := helpers.NewTestServer(t)
	createBucket(t, srv, "case-fold-bucket")
	putResp, err := http.DefaultClient.Do(put(srv, "/case-fold-bucket/greeting.txt", []byte("hello"), nil))
	if err != nil {
		t.Fatalf("seed object: %v", err)
	}
	putResp.Body.Close()

	hosts := []struct{ name, host string }{
		{"bare, mixed case", "Case-Fold-Bucket.localhost:4566"},
		{"bare, upper base", "case-fold-bucket.LOCALHOST.OVERCAST.SH:4566"},
		{"labelled, upper s3 label", "case-fold-bucket.S3.localhost:4566"},
		{"labelled, mixed case with region", "Case-Fold-Bucket.s3.US-East-1.localhost:4566"},
	}

	for _, h := range hosts {
		t.Run(h.name, func(t *testing.T) {
			// When: the object is fetched with the bucket in the Host header
			req, _ := http.NewRequest(http.MethodGet, srv.URL+"/greeting.txt", nil)
			req.Host = h.host
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("request: %v", err)
			}
			defer resp.Body.Close()

			// Then: the same object the path-style URL serves
			helpers.AssertStatus(t, resp, http.StatusOK)
			got, _ := io.ReadAll(resp.Body)
			if string(got) != "hello" {
				t.Errorf("Host %q: got body %q, want %q", h.host, got, "hello")
			}
		})
	}
}

// TestObjectPost_unsupportedSubresourceIsMethodNotAllowed covers POST on an
// object path that names no subresource.
//
// POST /{bucket}/{key} is only an operation when a subresource selects one —
// ?uploads, ?uploadId=, ?restore, ?select. Without one it is not an AWS
// operation at all, and S3 answers 405 MethodNotAllowed: "The specified method
// is not allowed against this resource."
//
// ObjectPost fell back to NotImplemented, which says something different and
// untrue — that this is an operation Overcast has not got to yet. A client
// seeing 501 reasonably concludes the emulator is incomplete and looks for a
// workaround, when in fact real AWS rejects the same request. That was the
// response a CloudFront distribution surfaced when a POST was routed to an S3
// origin: the misleading error sent the investigation after a missing feature
// instead of a misrouted request.
func TestObjectPost_unsupportedSubresourceIsMethodNotAllowed(t *testing.T) {
	srv := helpers.NewTestServer(t)
	createBucket(t, srv, "post-method-bucket")

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/post-method-bucket/some/key",
		strings.NewReader(`{"anything":true}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "MethodNotAllowed") {
		t.Errorf("expected a MethodNotAllowed error code, got: %s", body)
	}
	// Not an "unimplemented" answer: this operation does not exist in AWS
	// either, so the emulator must not advertise it as a gap in its coverage.
	if got := resp.Header.Get("x-emulator-unsupported"); got != "" {
		t.Errorf("x-emulator-unsupported = %q; a method AWS also rejects is not an emulator gap", got)
	}
}

// TestObjectPost_validSubresourceStillDispatches guards the other direction:
// narrowing the fallback must not shadow the real POST operations.
func TestObjectPost_validSubresourceStillDispatches(t *testing.T) {
	srv := helpers.NewTestServer(t)
	createBucket(t, srv, "post-subresource-bucket")

	req, _ := http.NewRequest(http.MethodPost,
		srv.URL+"/post-subresource-bucket/multipart/key?uploads", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()

	helpers.AssertStatus(t, resp, http.StatusOK)
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "InitiateMultipartUploadResult") {
		t.Errorf("expected CreateMultipartUpload to still dispatch, got: %s", body)
	}
}
