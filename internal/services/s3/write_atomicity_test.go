package s3

// write_atomicity_test.go pins the object write contract (#2192): a write that
// does not complete — its body stream fails part way, or the metadata record
// that would make it visible cannot be stored — leaves the key exactly as it
// was. The current object's bytes, its s3:objects record and its version
// history stay byte-identical, and no body file the failed write created is
// left behind.
//
// The contract cases run against every state backend this build provides,
// because the guarantee lives in s3Store's write ordering, not in a backend.

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/overcast-sh/overcast/internal/config"
	"github.com/overcast-sh/overcast/internal/events"
	"github.com/overcast-sh/overcast/internal/protocol"
	"github.com/overcast-sh/overcast/internal/state"
)

// ---- Fixtures --------------------------------------------------------------

var errInjectedSet = errors.New("state: injected write failure")

// faultStore fails the next write to one namespace and passes everything else
// through, which is the partial failure a write has to survive: the body is
// fully received and staged, and the record that would publish it is refused.
type faultStore struct {
	state.Store
	mu     sync.Mutex
	failNS string
}

func (s *faultStore) failNextSet(namespace string) {
	s.mu.Lock()
	s.failNS = namespace
	s.mu.Unlock()
}

func (s *faultStore) Set(ctx context.Context, namespace, key, value string) error {
	s.mu.Lock()
	fail := s.failNS != "" && s.failNS == namespace
	if fail {
		s.failNS = ""
	}
	s.mu.Unlock()
	if fail {
		return errInjectedSet
	}
	return s.Store.Set(ctx, namespace, key, value)
}

// newBackendStores returns a constructor per state backend this build
// provides. Under -tags nosqlite it degrades to memory only rather than the
// file being tagged out — see tests/AGENTS.md § Parity tests.
func newBackendStores() map[string]func(t *testing.T) state.Store {
	stores := map[string]func(t *testing.T) state.Store{
		"memory": func(*testing.T) state.Store { return state.NewMemoryStore() },
	}
	if !config.SQLiteSupported() {
		return stores
	}
	stores["sqlite"] = func(t *testing.T) state.Store {
		t.Helper()
		s, err := state.NewSQLiteStore(t.TempDir())
		if err != nil {
			t.Fatalf("state.NewSQLiteStore: %v", err)
		}
		t.Cleanup(func() { _ = s.Close() })
		return s
	}
	return stores
}

// failingReader yields prefix and then fails, as a client that disconnects
// part way through its upload does.
func failingReader(prefix string) io.Reader {
	return io.MultiReader(strings.NewReader(prefix), errReader{})
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("client went away") }

// keySnapshot is everything a failed write must leave untouched.
type keySnapshot struct {
	body      string
	current   string
	versions  []state.KV
	bodyFiles []string
	created   int
}

func (f *inProcessFixture) snapshot(t *testing.T, store state.Store, bucket, key string) keySnapshot {
	t.Helper()
	ctx := context.Background()
	current, _, err := store.Get(ctx, nsObjects, objectStoreKey(bucket, key))
	if err != nil {
		t.Fatalf("get current record: %v", err)
	}
	versions, err := store.Scan(ctx, nsVersions, bucket+"/"+key+versionSep)
	if err != nil {
		t.Fatalf("scan versions: %v", err)
	}
	return keySnapshot{
		body:      f.read(t, bucket, key, ""),
		current:   current,
		versions:  versions,
		bodyFiles: f.bodyFiles(t),
		created:   f.createdEvents(),
	}
}

// bodyFiles lists every file under the body directory, staged ones included,
// so a leftover from a failed write shows up however it was named.
func (f *inProcessFixture) bodyFiles(t *testing.T) []string {
	t.Helper()
	var files []string
	root := filepath.Join(f.dataDir, "s3-bodies")
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			rel, _ := filepath.Rel(root, path)
			files = append(files, rel)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk body dir: %v", err)
	}
	return files
}

// ---- The write contract ----------------------------------------------------

func TestPutObjectStream_failedWriteKeepsTheCurrentObject(t *testing.T) {
	failures := []struct {
		name  string
		write func(f *inProcessFixture, store *faultStore) *protocol.AWSError
	}{
		{"body stream fails", func(f *inProcessFixture, _ *faultStore) *protocol.AWSError {
			_, aerr := f.svc.PutObjectStream(context.Background(), "bkt", "k", failingReader("new-"), events.S3PutObjectOptions{})
			return aerr
		}},
		{"metadata write fails", func(f *inProcessFixture, store *faultStore) *protocol.AWSError {
			store.failNextSet(nsObjects)
			_, aerr := f.svc.PutObjectBytes(context.Background(), "bkt", "k", []byte("replacement"), events.S3PutObjectOptions{})
			return aerr
		}},
	}
	statuses := []string{"", versioningEnabled, versioningSuspended}

	for backend, newStore := range newBackendStores() {
		for _, status := range statuses {
			for _, failure := range failures {
				name := backend + "/" + status + "/" + failure.name
				if status == "" {
					name = backend + "/Unversioned/" + failure.name
				}
				t.Run(name, func(t *testing.T) {
					// Given: a key holding an object
					store := &faultStore{Store: newStore(t)}
					f := newInProcessFixtureOn(t, store)
					f.ensureBucket(t, "bkt")
					if status != "" {
						f.setVersioning(t, "bkt", status)
					}
					f.put(t, "bkt", "k", "original")
					before := f.snapshot(t, store, "bkt", "k")

					// When: a write to the same key fails
					aerr := failure.write(f, store)

					// Then: the write reports the failure, and the key — its
					// bytes, records, history and body files — is exactly as it
					// was, with nothing announced
					if aerr == nil {
						t.Fatal("failed write reported success")
					}
					if after := f.snapshot(t, store, "bkt", "k"); !reflect.DeepEqual(before, after) {
						t.Errorf("key changed by a failed write:\nbefore %+v\nafter  %+v", before, after)
					}
				})
			}
		}
	}
}

func TestPutObjectStream_failedWriteOfANewKeyLeavesNothing(t *testing.T) {
	for backend, newStore := range newBackendStores() {
		t.Run(backend, func(t *testing.T) {
			// Given: an empty bucket
			store := &faultStore{Store: newStore(t)}
			f := newInProcessFixtureOn(t, store)
			f.ensureBucket(t, "bkt")

			// When: the first write of a key has its metadata write refused
			store.failNextSet(nsObjects)
			_, aerr := f.svc.PutObjectBytes(context.Background(), "bkt", "k", []byte("body"), events.S3PutObjectOptions{})

			// Then: the key does not exist and no body file was left behind
			if aerr == nil {
				t.Fatal("failed write reported success")
			}
			if _, aerr := f.svc.GetObjectBytes(context.Background(), "bkt", "k", ""); aerr == nil || aerr.Code != "NoSuchKey" {
				t.Errorf("GetObject = %v, want NoSuchKey", aerr)
			}
			if files := f.bodyFiles(t); len(files) != 0 {
				t.Errorf("body files left behind: %v", files)
			}
		})
	}
}

func TestPutObjectStream_suspendedBucketReplacesTheNullVersion(t *testing.T) {
	// Given: a suspended bucket whose key has a null version beneath an
	// enabled-era version
	f := newInProcessFixture(t)
	f.ensureBucket(t, "bkt")
	f.put(t, "bkt", "k", "null-1")
	f.setVersioning(t, "bkt", versioningEnabled)
	v1 := f.put(t, "bkt", "k", "versioned")
	f.setVersioning(t, "bkt", versioningSuspended)

	// When: the key is written again
	f.put(t, "bkt", "k", "null-2")

	// Then: the new null version replaced the old one, and the enabled-era
	// version is untouched
	if got := f.read(t, "bkt", "k", nullVersionID); got != "null-2" {
		t.Errorf("null version = %q", got)
	}
	if got := f.read(t, "bkt", "k", v1.VersionID); got != "versioned" {
		t.Errorf("version %s = %q", v1.VersionID, got)
	}
	versions, aerr := f.svc.handler.store.listKeyVersions(context.Background(), "bkt", "k")
	if aerr != nil {
		t.Fatalf("list versions: %v", aerr)
	}
	if len(versions) != 2 {
		t.Errorf("versions = %d, want 2 (one null, one enabled-era)", len(versions))
	}
}

func TestBodyFiles_discardsWhatAPreviousProcessLeftStaged(t *testing.T) {
	// Given: a staged body a previous process never finished writing
	f := newInProcessFixture(t)
	leftover := filepath.Join(f.dataDir, "s3-bodies", stagingDir, "interrupted")
	if err := os.MkdirAll(filepath.Dir(leftover), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(leftover, []byte("half a body"), 0o644); err != nil {
		t.Fatal(err)
	}

	// When: this process first touches a body
	f.ensureBucket(t, "bkt")
	f.put(t, "bkt", "k", "body")

	// Then: the leftover is gone and the new object is intact
	if _, err := os.Stat(leftover); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("leftover staged body still present: %v", err)
	}
	if got := f.read(t, "bkt", "k", ""); got != "body" {
		t.Errorf("body = %q", got)
	}
}

func TestPutObjectStream_overwriteWhileTheBodyIsBeingRead(t *testing.T) {
	// Given: a reader part way through an object's body
	f := newInProcessFixture(t)
	f.ensureBucket(t, "bkt")
	f.put(t, "bkt", "k", "original")
	reader, aerr := f.svc.handler.store.openBody(f.meta(t, "bkt", "k"))
	if aerr != nil {
		t.Fatalf("open body: %v", aerr)
	}
	defer reader.Close()
	head := make([]byte, 4)
	if _, err := io.ReadFull(reader, head); err != nil {
		t.Fatalf("read: %v", err)
	}

	// When: the key is overwritten, and then deleted
	f.put(t, "bkt", "k", "replacement")
	replaced := f.read(t, "bkt", "k", "")
	if aerr := f.svc.handler.store.deleteObject(context.Background(), "bkt", "k"); aerr != nil {
		t.Fatalf("delete while a reader is open: %v", aerr)
	}

	// Then: both succeed — on Windows as elsewhere — and the reader still
	// finishes the bytes it started on
	rest, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read rest: %v", err)
	}
	if got := string(head) + string(rest); got != "original" {
		t.Errorf("open reader saw %q, want %q", got, "original")
	}
	if replaced != "replacement" {
		t.Errorf("overwritten body = %q", replaced)
	}
}

func TestPutObjectStream_concurrentOverwritesStayConsistent(t *testing.T) {
	// Given: a key that many writers overwrite at once
	f := newInProcessFixture(t)
	f.ensureBucket(t, "bkt")
	bodies := []string{"alpha", "bravo-bravo", "charlie-charlie-charlie", "delta"}

	// When: they all write concurrently
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		body := bodies[i%len(bodies)]
		wg.Go(func() {
			if _, aerr := f.svc.PutObjectBytes(context.Background(), "bkt", "k", []byte(body), events.S3PutObjectOptions{}); aerr != nil {
				t.Errorf("put %q: %v", body, aerr)
			}
		})
	}
	wg.Wait()

	// Then: the stored body is one writer's whole body, described by that
	// same writer's metadata
	got := f.read(t, "bkt", "k", "")
	meta := f.meta(t, "bkt", "k")
	if meta.ETag != md5ETag(got) || meta.ContentLength != int64(len(got)) {
		t.Errorf("body %q does not match its metadata (ETag %s, length %d)", got, meta.ETag, meta.ContentLength)
	}
}

// slowVersionScans pauses a random few milliseconds either side of reading a
// key's version history, so suspended-bucket null-version bookkeeping that is
// not serialised against other writers of the key interleaves with theirs.
type slowVersionScans struct {
	state.Store
}

func (s slowVersionScans) Scan(ctx context.Context, namespace, prefix string) ([]state.KV, error) {
	if namespace == nsVersions {
		time.Sleep(time.Duration(rand.IntN(4000)) * time.Microsecond)
	}
	kvs, err := s.Store.Scan(ctx, namespace, prefix)
	if namespace == nsVersions {
		time.Sleep(time.Duration(rand.IntN(4000)) * time.Microsecond)
	}
	return kvs, err
}

// assertOneNullVersion checks a suspended key's invariant: exactly one null
// version in its history, and it is the current object, readable in full.
func (f *inProcessFixture) assertOneNullVersion(t *testing.T, bucket, key string) {
	t.Helper()
	versions, aerr := f.svc.handler.store.listKeyVersions(context.Background(), bucket, key)
	if aerr != nil {
		t.Fatalf("list versions: %v", aerr)
	}
	var nulls []*Object
	for _, v := range versions {
		if v.isNullVersion() {
			nulls = append(nulls, v)
		}
	}
	current := f.meta(t, bucket, key)
	if len(nulls) != 1 || nulls[0].Seq != current.Seq {
		t.Fatalf("null versions = %d, want exactly the current one (%s)", len(nulls), current.Seq)
	}
	if current.DeleteMarker {
		return
	}
	if got := f.read(t, bucket, key, ""); current.ETag != md5ETag(got) {
		t.Errorf("current body %q does not match its ETag %s", got, current.ETag)
	}
}

func TestPutObjectStream_concurrentWritesToASuspendedKey(t *testing.T) {
	for range 10 {
		// Given: a version-suspended bucket, where every write of a key
		// replaces its one null version
		f := newInProcessFixtureOn(t, slowVersionScans{state.NewMemoryStore()})
		f.ensureBucket(t, "bkt")
		f.setVersioning(t, "bkt", versioningSuspended)
		f.put(t, "bkt", "k", "first")

		// When: writers overwrite the key at once
		var wg sync.WaitGroup
		for i := range 8 {
			wg.Go(func() {
				body := strings.Repeat("x", i+1)
				if _, aerr := f.svc.PutObjectBytes(context.Background(), "bkt", "k", []byte(body), events.S3PutObjectOptions{}); aerr != nil {
					t.Errorf("put %q: %v", body, aerr)
				}
			})
		}
		wg.Wait()

		// Then: the key still has exactly one null version, and it is readable
		f.assertOneNullVersion(t, "bkt", "k")
	}
}

func TestCreateDeleteMarker_racingAWriteToASuspendedKey(t *testing.T) {
	for range 10 {
		// Given: a version-suspended key
		f := newInProcessFixtureOn(t, slowVersionScans{state.NewMemoryStore()})
		f.ensureBucket(t, "bkt")
		f.setVersioning(t, "bkt", versioningSuspended)
		f.put(t, "bkt", "k", "first")
		b := f.bucket(t, "bkt")

		// When: a delete and a write of the key race
		var wg sync.WaitGroup
		wg.Go(func() {
			if _, aerr := f.svc.handler.createDeleteMarker(httptest.NewRequest(http.MethodDelete, "/bkt/k", nil), b, "k"); aerr != nil {
				t.Errorf("delete: %v", aerr)
			}
		})
		wg.Go(func() {
			if _, aerr := f.svc.PutObjectBytes(context.Background(), "bkt", "k", []byte("second"), events.S3PutObjectOptions{}); aerr != nil {
				t.Errorf("put: %v", aerr)
			}
		})
		wg.Wait()

		// Then: whichever landed last is the key's one null version, and a
		// current object still has its body
		f.assertOneNullVersion(t, "bkt", "k")
	}
}
