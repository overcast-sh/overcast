package s3

// Unit tests for the in-process accessor other services use to write, list and
// create buckets without an HTTP round trip (service.go). The accessor shares
// PutObject's, ListObjectsV2's and CreateBucket's own code paths, so these
// tests pin what an internal caller observes: the same stored object, the same
// version identity, the same notifications and the same AWS errors.

import (
	"context"
	"crypto/md5"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/overcast-sh/overcast/internal/clock"
	"github.com/overcast-sh/overcast/internal/config"
	"github.com/overcast-sh/overcast/internal/events"
	"github.com/overcast-sh/overcast/internal/protocol"
	"github.com/overcast-sh/overcast/internal/state"
)

// The accessor's methods are what router.go hands to consuming services, so
// their signatures must stay assignable to the shared func types.
var (
	_ events.S3PutObjectFunc    = (*Service)(nil).PutObjectBytes
	_ events.S3ListObjectsFunc  = (*Service)(nil).ListObjects
	_ events.S3EnsureBucketFunc = (*Service)(nil).EnsureBucket
	_ events.S3EnsureBucketFunc = (*Service)(nil).EnsureTableWarehouseBucket
)

// ---- Fixtures --------------------------------------------------------------

type inProcessFixture struct {
	svc   *Service
	bus   *events.Bus
	clock *clock.Mock
}

func newInProcessFixture(t *testing.T) *inProcessFixture {
	t.Helper()
	cfg := &config.Config{Region: "us-east-1", AccountID: "000000000000", DataDir: t.TempDir()}
	mock := clock.NewMock()
	mock.Set(time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC))
	bus := events.NewBus()
	svc := New(cfg, state.NewMemoryStore(), zap.NewNop(), mock, bus)
	t.Cleanup(func() {
		svc.Stop(context.Background())
		bus.Stop()
	})
	return &inProcessFixture{svc: svc, bus: bus, clock: mock}
}

func (f *inProcessFixture) ensureBucket(t *testing.T, name string) {
	t.Helper()
	if aerr := f.svc.EnsureBucket(context.Background(), name, ""); aerr != nil {
		t.Fatalf("ensure bucket %s: %v", name, aerr)
	}
}

func (f *inProcessFixture) setVersioning(t *testing.T, name, status string) {
	t.Helper()
	ctx := context.Background()
	b, aerr := f.svc.handler.store.getBucket(ctx, name)
	if aerr != nil {
		t.Fatalf("get bucket %s: %v", name, aerr)
	}
	b.VersioningStatus = status
	if aerr := f.svc.handler.store.putBucket(ctx, b); aerr != nil {
		t.Fatalf("put bucket %s: %v", name, aerr)
	}
}

func (f *inProcessFixture) put(t *testing.T, bucket, key, body string) events.S3PutObjectResult {
	t.Helper()
	res, aerr := f.svc.PutObjectBytes(context.Background(), bucket, key, []byte(body), events.S3PutObjectOptions{})
	if aerr != nil {
		t.Fatalf("put %s/%s: %v", bucket, key, aerr)
	}
	return res
}

func (f *inProcessFixture) read(t *testing.T, bucket, key, versionID string) string {
	t.Helper()
	body, aerr := f.svc.GetObjectBytes(context.Background(), bucket, key, versionID)
	if aerr != nil {
		t.Fatalf("get %s/%s@%q: %v", bucket, key, versionID, aerr)
	}
	return string(body)
}

func md5ETag(body string) string {
	return fmt.Sprintf(`"%x"`, md5.Sum([]byte(body)))
}

func assertAWSError(t *testing.T, aerr *protocol.AWSError, code string, status int) {
	t.Helper()
	if aerr == nil {
		t.Fatalf("expected %s, got no error", code)
	}
	if aerr.Code != code || aerr.HTTPStatus != status {
		t.Fatalf("expected %s (%d), got %s (%d): %s", code, status, aerr.Code, aerr.HTTPStatus, aerr.Message)
	}
}

// recordingEnqueuer is an events.MessageEnqueuer that hands every delivery to
// the test on a channel.
type recordingEnqueuer struct {
	delivered chan string
}

func (e *recordingEnqueuer) EnqueueRaw(_ context.Context, queueName, body string) error {
	e.delivered <- queueName + "|" + body
	return nil
}

// ---- PutObjectBytes --------------------------------------------------------

func TestPutObjectBytes_unversionedBucket(t *testing.T) {
	// Given: an unversioned bucket
	f := newInProcessFixture(t)
	f.ensureBucket(t, "results")

	// When: an internal caller writes an object with a content type and metadata
	res, aerr := f.svc.PutObjectBytes(context.Background(), "results", "q/1.csv", []byte("a,b\n1,2\n"),
		events.S3PutObjectOptions{ContentType: "text/csv", Metadata: map[string]string{"Query-Id": "q1"}})

	// Then: it is stored exactly as an HTTP PutObject would store it
	if aerr != nil {
		t.Fatalf("put: %v", aerr)
	}
	if res.ETag != md5ETag("a,b\n1,2\n") {
		t.Errorf("ETag = %s, want %s", res.ETag, md5ETag("a,b\n1,2\n"))
	}
	if res.VersionID != "" {
		t.Errorf("VersionID = %q, want none for an unversioned bucket", res.VersionID)
	}
	if got := f.read(t, "results", "q/1.csv", ""); got != "a,b\n1,2\n" {
		t.Errorf("body = %q", got)
	}
	obj, aerr := f.svc.handler.store.getObjectMeta(context.Background(), "results", "q/1.csv")
	if aerr != nil {
		t.Fatalf("meta: %v", aerr)
	}
	if obj.ContentType != "text/csv" {
		t.Errorf("ContentType = %q", obj.ContentType)
	}
	if obj.Metadata["query-id"] != "q1" {
		t.Errorf("Metadata = %v, want the key lower-cased as S3 stores it", obj.Metadata)
	}
	if !obj.LastModified.Equal(f.clock.Now()) {
		t.Errorf("LastModified = %v, want the injected clock's %v", obj.LastModified, f.clock.Now())
	}
}

func TestPutObjectBytes_defaultContentType(t *testing.T) {
	// Given: a bucket
	f := newInProcessFixture(t)
	f.ensureBucket(t, "results")

	// When: an object is written with no content type
	f.put(t, "results", "k", "x")

	// Then: it reads back as S3's default binary type
	obj, aerr := f.svc.handler.store.getObjectMeta(context.Background(), "results", "k")
	if aerr != nil {
		t.Fatalf("meta: %v", aerr)
	}
	if obj.ContentType != "application/octet-stream" {
		t.Errorf("ContentType = %q", obj.ContentType)
	}
}

func TestPutObjectBytes_overwriteUnversioned(t *testing.T) {
	// Given: an unversioned bucket holding a key
	f := newInProcessFixture(t)
	f.ensureBucket(t, "results")
	first := f.put(t, "results", "k", "first")

	// When: the key is written again
	second := f.put(t, "results", "k", "second")

	// Then: the new body replaces the old one
	if first.ETag == second.ETag {
		t.Errorf("ETag unchanged across overwrite: %s", second.ETag)
	}
	if got := f.read(t, "results", "k", ""); got != "second" {
		t.Errorf("body = %q, want second", got)
	}
}

func TestPutObjectBytes_versionedBucket(t *testing.T) {
	// Given: a bucket with versioning enabled
	f := newInProcessFixture(t)
	f.ensureBucket(t, "warehouse")
	f.setVersioning(t, "warehouse", versioningEnabled)

	// When: the same key is written twice
	v1 := f.put(t, "warehouse", "metadata.json", "v1")
	v2 := f.put(t, "warehouse", "metadata.json", "v2")

	// Then: each write gets its own version, and both remain readable
	if v1.VersionID == "" || v2.VersionID == "" || v1.VersionID == v2.VersionID {
		t.Fatalf("version ids = %q, %q; want two distinct ids", v1.VersionID, v2.VersionID)
	}
	if got := f.read(t, "warehouse", "metadata.json", ""); got != "v2" {
		t.Errorf("current body = %q, want v2", got)
	}
	if got := f.read(t, "warehouse", "metadata.json", v1.VersionID); got != "v1" {
		t.Errorf("first version body = %q, want v1", got)
	}
}

func TestPutObjectBytes_suspendedBucket(t *testing.T) {
	// Given: a bucket whose versioning is suspended
	f := newInProcessFixture(t)
	f.ensureBucket(t, "warehouse")
	f.setVersioning(t, "warehouse", versioningSuspended)

	// When: an object is written
	res := f.put(t, "warehouse", "k", "body")

	// Then: it is stored as the null version, which is what S3 reports for it
	if res.VersionID != nullVersionID {
		t.Errorf("VersionID = %q, want %q", res.VersionID, nullVersionID)
	}
}

func TestPutObjectBytes_missingBucket(t *testing.T) {
	// Given: no bucket
	f := newInProcessFixture(t)

	// When: an object is written to it
	_, aerr := f.svc.PutObjectBytes(context.Background(), "absent", "k", []byte("x"), events.S3PutObjectOptions{})

	// Then: the caller gets PutObject's own NoSuchBucket
	assertAWSError(t, aerr, "NoSuchBucket", http.StatusNotFound)
}

func TestPutObjectBytes_emptyKey(t *testing.T) {
	// Given: a bucket
	f := newInProcessFixture(t)
	f.ensureBucket(t, "results")

	// When: a write names no key
	_, aerr := f.svc.PutObjectBytes(context.Background(), "results", "", []byte("x"), events.S3PutObjectOptions{})

	// Then: it is refused rather than written over the bucket's body directory
	assertAWSError(t, aerr, "InvalidArgument", http.StatusBadRequest)
}

func TestPutObjectBytes_firesNotification(t *testing.T) {
	// Given: a bucket that notifies a queue of every created object
	f := newInProcessFixture(t)
	enq := &recordingEnqueuer{delivered: make(chan string, 1)}
	f.svc.InitNotifications(enq, nil, nil, nil, nil, f.bus, zap.NewNop())
	f.ensureBucket(t, "results")
	cfg := &NotificationConfig{QueueConfigurations: []QueueNotificationConfig{{
		ID:     "all-creates",
		ARN:    "arn:aws:sqs:us-east-1:000000000000:results-events",
		Events: []string{"s3:ObjectCreated:*"},
	}}}
	if aerr := f.svc.handler.store.putNotificationConfig(context.Background(), "results", cfg); aerr != nil {
		t.Fatalf("put notification config: %v", aerr)
	}

	// When: an internal caller writes an object
	f.put(t, "results", "q/1.csv", "a,b\n")

	// Then: the queue receives the same ObjectCreated:Put record an HTTP put produces
	select {
	case got := <-enq.delivered:
		queue, body, _ := strings.Cut(got, "|")
		if queue != "results-events" {
			t.Errorf("queue = %q", queue)
		}
		for _, want := range []string{`"eventName":"ObjectCreated:Put"`, `"key":"q/1.csv"`, `"name":"results"`} {
			if !strings.Contains(body, want) {
				t.Errorf("notification body missing %s: %s", want, body)
			}
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no notification delivered for PutObjectBytes")
	}
}

// ---- ListObjects -----------------------------------------------------------

func TestListObjects_prefixAndContinuation(t *testing.T) {
	// Given: a bucket with five keys under one prefix and two under another
	f := newInProcessFixture(t)
	f.ensureBucket(t, "results")
	want := []string{"a/1", "a/2", "a/3", "a/4", "a/5"}
	for _, k := range want {
		f.put(t, "results", k, "body-"+k)
	}
	f.put(t, "results", "b/1", "x")
	f.put(t, "results", "b/2", "x")

	// When: the prefix is listed two keys at a time
	var got []string
	var pages int
	token := ""
	for {
		page, aerr := f.svc.ListObjects(context.Background(), "results", "a/", token, 2)
		if aerr != nil {
			t.Fatalf("list page %d: %v", pages, aerr)
		}
		pages++
		for _, o := range page.Objects {
			got = append(got, o.Key)
			if o.Size != int64(len("body-"+o.Key)) || o.ETag != md5ETag("body-"+o.Key) {
				t.Errorf("summary for %s = size %d etag %s", o.Key, o.Size, o.ETag)
			}
			if !o.LastModified.Equal(f.clock.Now()) {
				t.Errorf("LastModified for %s = %v", o.Key, o.LastModified)
			}
		}
		if page.NextContinuationToken == "" {
			break
		}
		token = page.NextContinuationToken
	}

	// Then: every key under the prefix comes back once, in order, over three pages
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("keys = %v, want %v", got, want)
	}
	if pages != 3 {
		t.Errorf("pages = %d, want 3", pages)
	}
}

func TestListObjects_defaultPageSize(t *testing.T) {
	// Given: a bucket with a few keys
	f := newInProcessFixture(t)
	f.ensureBucket(t, "results")
	f.put(t, "results", "a", "x")
	f.put(t, "results", "b", "x")

	// When: it is listed with no page size
	page, aerr := f.svc.ListObjects(context.Background(), "results", "", "", 0)

	// Then: one untruncated page holds everything
	if aerr != nil {
		t.Fatalf("list: %v", aerr)
	}
	if len(page.Objects) != 2 || page.NextContinuationToken != "" {
		t.Errorf("page = %+v", page)
	}
}

func TestListObjects_errors(t *testing.T) {
	f := newInProcessFixture(t)
	f.ensureBucket(t, "results")

	cases := []struct {
		name, bucket, token, code string
		status                    int
	}{
		{"missing bucket", "absent", "", "NoSuchBucket", http.StatusNotFound},
		{"garbled continuation token", "results", "%%%not-base64", "InvalidArgument", http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// When: the listing is refused
			_, aerr := f.svc.ListObjects(context.Background(), tc.bucket, "", tc.token, 10)

			// Then: with the error ListObjectsV2 answers over HTTP
			assertAWSError(t, aerr, tc.code, tc.status)
		})
	}
}

// ---- EnsureBucket ----------------------------------------------------------

func TestEnsureBucket_idempotent(t *testing.T) {
	// Given: a service outside us-east-1's legacy re-create rule
	f := newInProcessFixture(t)
	ctx := context.Background()

	// When: the same bucket is ensured twice
	first := f.svc.EnsureBucket(ctx, "warehouse", "eu-west-1")
	second := f.svc.EnsureBucket(ctx, "warehouse", "eu-west-1")

	// Then: both succeed and the bucket lives in the requested region
	if first != nil || second != nil {
		t.Fatalf("EnsureBucket = %v, %v; want nil both times", first, second)
	}
	b, aerr := f.svc.handler.store.getBucket(ctx, "warehouse")
	if aerr != nil {
		t.Fatalf("get bucket: %v", aerr)
	}
	if b.Region != "eu-west-1" {
		t.Errorf("Region = %q", b.Region)
	}
}

func TestEnsureBucket_keepsExistingContents(t *testing.T) {
	// Given: a bucket holding an object
	f := newInProcessFixture(t)
	f.ensureBucket(t, "warehouse")
	f.put(t, "warehouse", "k", "kept")

	// When: it is ensured again
	f.ensureBucket(t, "warehouse")

	// Then: nothing in it was disturbed
	if got := f.read(t, "warehouse", "k", ""); got != "kept" {
		t.Errorf("body = %q", got)
	}
}

func TestEnsureBucket_defaultRegion(t *testing.T) {
	// Given: a service configured for us-east-1
	f := newInProcessFixture(t)

	// When: a bucket is ensured without a region
	f.ensureBucket(t, "results")

	// Then: it is created in the service's region
	b, aerr := f.svc.handler.store.getBucket(context.Background(), "results")
	if aerr != nil {
		t.Fatalf("get bucket: %v", aerr)
	}
	if b.Region != "us-east-1" {
		t.Errorf("Region = %q", b.Region)
	}
}

func TestEnsureBucket_invalidName(t *testing.T) {
	cases := []struct{ name, bucket string }{
		{"too short", "ab"},
		{"upper case", "Results"},
		// Real S3 reserves the suffix for S3 Tables' own buckets, so the
		// general accessor refuses it; EnsureTableWarehouseBucket is the one
		// entry point that may create it.
		{"reserved S3 Tables suffix", "warehouse--table-s3"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Given: a service
			f := newInProcessFixture(t)

			// When: a bucket is ensured under a name CreateBucket refuses
			aerr := f.svc.EnsureBucket(context.Background(), tc.bucket, "")

			// Then: it is refused with CreateBucket's own error
			assertAWSError(t, aerr, "InvalidBucketName", http.StatusBadRequest)
		})
	}
}

func TestEnsureTableWarehouseBucket_createsTheReservedSuffixOnly(t *testing.T) {
	// Given: a service
	f := newInProcessFixture(t)
	ctx := context.Background()

	// When: S3 Tables ensures a warehouse bucket, twice
	name := "63a8e430-6e0b-46f5-k833abtwr6s8tmtsycedn8s4yc3xhuse1b--table-s3"
	first := f.svc.EnsureTableWarehouseBucket(ctx, name, "eu-west-1")
	second := f.svc.EnsureTableWarehouseBucket(ctx, name, "eu-west-1")

	// Then: it exists, in the requested region, and is writable like any bucket
	if first != nil || second != nil {
		t.Fatalf("EnsureTableWarehouseBucket = %v, %v; want nil both times", first, second)
	}
	b, aerr := f.svc.handler.store.getBucket(ctx, name)
	if aerr != nil {
		t.Fatalf("get bucket: %v", aerr)
	}
	if b.Region != "eu-west-1" {
		t.Errorf("Region = %q", b.Region)
	}
	f.put(t, name, "metadata/00000.metadata.json", "{}")

	// And: a name without the suffix is not a warehouse bucket
	aerr = f.svc.EnsureTableWarehouseBucket(ctx, "plain-bucket", "")
	assertAWSError(t, aerr, "InvalidBucketName", http.StatusBadRequest)
}

func TestEnsureBucket_announcesCreationOnce(t *testing.T) {
	// Given: a subscriber to bucket creation
	f := newInProcessFixture(t)
	created := make(chan string, 2)
	cancel := f.bus.Subscribe(events.S3BucketCreated, func(_ context.Context, e events.Event) {
		created <- e.Payload.(events.ResourcePayload).Name
	})
	t.Cleanup(cancel)

	// When: a bucket is ensured twice
	f.ensureBucket(t, "results")
	f.ensureBucket(t, "results")

	// Then: exactly one creation event is published
	select {
	case name := <-created:
		if name != "results" {
			t.Errorf("created %q", name)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no S3BucketCreated event")
	}
	select {
	case name := <-created:
		t.Errorf("second creation event for %q", name)
	case <-time.After(100 * time.Millisecond):
	}
}
