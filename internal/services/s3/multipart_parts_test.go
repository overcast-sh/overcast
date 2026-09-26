package s3

// multipart_parts_test.go pins where multipart part bodies live on disk
// (#2234) and the ETag of an object assembled from them (#2232).

import (
	"context"
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

// ---- multipartETag ---------------------------------------------------------

// The expected values were computed independently of this package, with
// Python's hashlib: md5(b"".join(md5(p).digest() for p in parts)).hexdigest(),
// suffixed "-<len(parts)>" — S3's documented formula.
func TestMultipartETag_knownAnswers(t *testing.T) {
	const (
		md5Hello = "5d41402abc4b2a76b9719d911017c592" // md5("hello")
		md5World = "7d793037a0760186574b0282f2f435e7" // md5("world")
	)
	cases := []struct {
		name  string
		parts []*Part
		want  string
	}{
		{
			name:  "one part",
			parts: []*Part{{PartNumber: 1, ETag: `"` + md5Hello + `"`}},
			want:  `"62109206880d38a4010a98e11243924a-1"`,
		},
		{
			name:  "two parts",
			parts: []*Part{{PartNumber: 1, ETag: `"` + md5Hello + `"`}, {PartNumber: 2, ETag: `"` + md5World + `"`}},
			want:  `"065947336a2f2a95ba8899f3675c3be6-2"`,
		},
		{
			// Not md5("helloworld") = fc5e038d38a57032085441e7fe7010b0, which
			// is what hashing the assembled bytes gave before #2232.
			name:  "part ETags stored without quotes",
			parts: []*Part{{PartNumber: 1, ETag: md5Hello}, {PartNumber: 2, ETag: md5World}},
			want:  `"065947336a2f2a95ba8899f3675c3be6-2"`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// When: the ETag of an object assembled from the parts is derived
			got, aerr := multipartETag(tc.parts)

			// Then: it is S3's digest of the parts' MD5s
			if aerr != nil {
				t.Fatalf("multipartETag: %v", aerr)
			}
			if got != tc.want {
				t.Errorf("ETag = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestMultipartETag_partWhoseStoredETagIsNotAnMD5(t *testing.T) {
	// Given: a persisted part record whose ETag cannot be an MD5
	parts := []*Part{
		{PartNumber: 1, ETag: `"5d41402abc4b2a76b9719d911017c592"`},
		{PartNumber: 2, ETag: `"not-hex"`},
	}

	// When: the ETag is derived
	_, aerr := multipartETag(parts)

	// Then: that one part is refused as S3 refuses an unusable part
	assertAWSError(t, aerr, "InvalidPart", http.StatusBadRequest)
	if aerr != nil && !strings.Contains(aerr.Message, "part 2") {
		t.Errorf("message %q does not name part 2", aerr.Message)
	}
}

// ---- Legacy part layout ----------------------------------------------------

func TestCompleteMultipartUpload_partsStoredUnderTheLegacyDirectory(t *testing.T) {
	// Given: an upload whose part an earlier release stored under
	// multipart/<uploadId>/, beside the body of an object in a bucket that
	// is itself named "multipart"
	f := newInProcessFixture(t)
	bodies := filepath.Join(f.dataDir, "s3-bodies")
	writeBodyFile(t, filepath.Join(bodies, "multipart", "legacy-upload", "1"), "legacy part")
	writeBodyFile(t, filepath.Join(bodies, bodyRel("multipart", "k", "")), "bucket body")
	f.seedLegacyUpload(t, "legacy-upload", "src", "obj", "legacy part")
	f.seedObject(t, "multipart", "k", "bucket body")

	// When: the upload is completed
	rec := f.serve(completeRequest("src", "obj", "legacy-upload", md5ETag("legacy part")))

	// Then: the object is assembled from the legacy part
	if rec.Code != http.StatusOK {
		t.Fatalf("CompleteMultipartUpload = %d: %s", rec.Code, rec.Body)
	}
	if got := f.read(t, "src", "obj", ""); got != "legacy part" {
		t.Errorf("assembled body = %q", got)
	}
	// And: the bucket named "multipart" keeps its object
	if got := f.read(t, "multipart", "k", ""); got != "bucket body" {
		t.Errorf("multipart/k body = %q", got)
	}
	// And: nothing is left in the legacy directory
	if _, err := os.Stat(filepath.Join(bodies, "multipart", "legacy-upload")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("legacy part directory still present: %v", err)
	}
}

// ---- Fixtures --------------------------------------------------------------

func writeBodyFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// seedBucket stores a bucket record without touching the body directory, so
// a test can lay out body files before this process first opens it.
func (f *inProcessFixture) seedBucket(t *testing.T, name string) {
	t.Helper()
	b := &Bucket{Name: name, Region: usEast1, CreationDate: f.clock.Now()}
	if aerr := f.svc.handler.store.putBucket(context.Background(), b); aerr != nil {
		t.Fatalf("put bucket %s: %v", name, aerr)
	}
}

// seedObject records an object whose body the test has written itself.
func (f *inProcessFixture) seedObject(t *testing.T, bucket, key, body string) {
	t.Helper()
	f.seedBucket(t, bucket)
	obj := &Object{Bucket: bucket, Key: key, ETag: md5ETag(body), ContentLength: int64(len(body)), LastModified: f.clock.Now()}
	if aerr := f.svc.handler.store.putObjectMeta(context.Background(), obj); aerr != nil {
		t.Fatalf("put object %s/%s: %v", bucket, key, aerr)
	}
}

// seedLegacyUpload records an upload of bucket/key with one part, whose body
// the test has written itself.
func (f *inProcessFixture) seedLegacyUpload(t *testing.T, uploadID, bucket, key, part string) {
	t.Helper()
	ctx := context.Background()
	store := f.svc.handler.store
	f.seedBucket(t, bucket)
	upload := &MultipartUpload{UploadID: uploadID, Bucket: bucket, Key: key, Initiated: f.clock.Now()}
	if aerr := store.createMultipartUpload(ctx, upload); aerr != nil {
		t.Fatalf("create upload: %v", aerr)
	}
	p := &Part{PartNumber: 1, ETag: md5ETag(part), Size: int64(len(part)), LastModified: f.clock.Now()}
	if aerr := store.savePart(ctx, uploadID, p); aerr != nil {
		t.Fatalf("save part: %v", aerr)
	}
}

// serve sends r through the service's own routes.
func (f *inProcessFixture) serve(r *http.Request) *httptest.ResponseRecorder {
	router := chi.NewRouter()
	f.svc.RegisterRoutes(router)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, r)
	return rec
}

// completeRequest completes uploadID with its single part 1.
func completeRequest(bucket, key, uploadID, partETag string) *http.Request {
	body := "<CompleteMultipartUpload><Part><PartNumber>1</PartNumber><ETag>" + partETag + "</ETag></Part></CompleteMultipartUpload>"
	return httptest.NewRequest(http.MethodPost, "/"+bucket+"/"+key+"?uploadId="+uploadID, strings.NewReader(body))
}
