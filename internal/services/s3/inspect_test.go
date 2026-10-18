package s3

// Tests for the metadata-only accessors in inspect.go.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
)

func TestBucketVersioning_reportsStatus(t *testing.T) {
	// Given: a bucket that was never versioned, one enabled and one suspended
	f := newInProcessFixture(t)
	for _, name := range []string{"plain", "enabled", "suspended"} {
		f.ensureBucket(t, name)
	}
	f.setVersioning(t, "enabled", versioningEnabled)
	f.setVersioning(t, "suspended", versioningSuspended)

	// When / Then: each reports its own status
	for bucket, want := range map[string]string{"plain": "", "enabled": "Enabled", "suspended": "Suspended"} {
		got, aerr := f.svc.BucketVersioning(context.Background(), bucket)
		if aerr != nil || got != want {
			t.Errorf("BucketVersioning(%s) = %q, %v; want %q", bucket, got, aerr, want)
		}
	}

	// And: a missing bucket is NoSuchBucket
	_, aerr := f.svc.BucketVersioning(context.Background(), "absent")
	assertAWSError(t, aerr, "NoSuchBucket", http.StatusNotFound)
}

func TestHeadObjectVersion_currentAndExplicitVersions(t *testing.T) {
	// Given: a versioned bucket holding two versions of a key
	f := newInProcessFixture(t)
	f.ensureBucket(t, "code")
	f.setVersioning(t, "code", versioningEnabled)
	v1 := f.put(t, "code", "app.zip", "one")
	v2 := f.put(t, "code", "app.zip", "three")

	// When: the current version is inspected
	cur, aerr := f.svc.HeadObjectVersion(context.Background(), "code", "app.zip", "")

	// Then: it is the latest, with its size, and no body was needed
	if aerr != nil || cur.VersionID != v2.VersionID || cur.Size != 5 || cur.DeleteMarker {
		t.Errorf("current = %+v, %v; want version %s size 5", cur, aerr, v2.VersionID)
	}

	// And: an older version can be addressed
	old, aerr := f.svc.HeadObjectVersion(context.Background(), "code", "app.zip", v1.VersionID)
	if aerr != nil || old.VersionID != v1.VersionID || old.Size != 3 {
		t.Errorf("old = %+v, %v", old, aerr)
	}

	// And: unknown keys and versions are the errors HeadObject answers with
	_, aerr = f.svc.HeadObjectVersion(context.Background(), "code", "nope.zip", "")
	assertAWSError(t, aerr, "NoSuchKey", http.StatusNotFound)
	_, aerr = f.svc.HeadObjectVersion(context.Background(), "code", "app.zip", "does-not-exist")
	assertAWSError(t, aerr, "NoSuchVersion", http.StatusNotFound)
	_, aerr = f.svc.HeadObjectVersion(context.Background(), "absent", "app.zip", "")
	assertAWSError(t, aerr, "NoSuchBucket", http.StatusNotFound)
}

func TestHeadObjectVersion_deleteMarker(t *testing.T) {
	// Given: a versioned object whose key was then deleted
	f := newInProcessFixture(t)
	f.ensureBucket(t, "code")
	f.setVersioning(t, "code", versioningEnabled)
	v1 := f.put(t, "code", "app.zip", "one")
	r := chi.NewRouter()
	f.svc.RegisterRoutes(r)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/code/app.zip", nil))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("DeleteObject status = %d", rec.Code)
	}

	// When: the current version is inspected
	cur, aerr := f.svc.HeadObjectVersion(context.Background(), "code", "app.zip", "")

	// Then: it is a delete marker, not an error
	if aerr != nil || !cur.DeleteMarker || cur.Size != 0 {
		t.Errorf("current = %+v, %v; want a delete marker", cur, aerr)
	}

	// And: the original version is still there
	old, aerr := f.svc.HeadObjectVersion(context.Background(), "code", "app.zip", v1.VersionID)
	if aerr != nil || old.DeleteMarker || old.Size != 3 {
		t.Errorf("old = %+v, %v", old, aerr)
	}
}
