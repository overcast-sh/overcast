package s3

// create_bucket_race_test.go pins #2126: creating a bucket is one step against
// every other create of the same name, so N concurrent creators produce one
// bucket, one creation date and one S3BucketCreated event, and every loser
// gets the answer a create of an existing bucket gets.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/overcast-sh/overcast/internal/events"
	"github.com/overcast-sh/overcast/internal/middleware"
	"github.com/overcast-sh/overcast/internal/state"
)

// concurrentCreators is how many creators race for one bucket name.
const concurrentCreators = 8

// bucketCheckGate holds every bucket lookup, once it has read the store, until
// want of them are in flight together or wait passes. Without it the window
// between a creator's existence check and its store write is a few
// microseconds, and a race test would only fail sometimes; with it, every
// creator that is not serialised against the others is guaranteed to see the
// name as free. A creator that is serialised waits out wait alone, so the gate
// costs a correct implementation at most want × wait.
type bucketCheckGate struct {
	state.Store
	want int
	wait time.Duration

	mu      sync.Mutex
	arrived int
	all     chan struct{}
}

func newBucketCheckGate(want int) *bucketCheckGate {
	return &bucketCheckGate{Store: state.NewMemoryStore(), want: want, wait: 50 * time.Millisecond, all: make(chan struct{})}
}

func (g *bucketCheckGate) Get(ctx context.Context, namespace, key string) (string, bool, error) {
	value, found, err := g.Store.Get(ctx, namespace, key)
	if namespace == nsBuckets {
		g.arrive()
	}
	return value, found, err
}

func (g *bucketCheckGate) arrive() {
	g.mu.Lock()
	g.arrived++
	if g.arrived == g.want {
		close(g.all)
	}
	g.mu.Unlock()
	select {
	case <-g.all:
	case <-time.After(g.wait):
	}
}

// bucketCreatedEvents counts the S3BucketCreated events the bus has seen.
func (f *inProcessFixture) bucketCreatedEvents() int {
	history, cancel := f.bus.SnapshotAndSubscribeAll(func(context.Context, events.Event) {})
	cancel()
	n := 0
	for _, e := range history {
		if e.Type == events.S3BucketCreated {
			n++
		}
	}
	return n
}

// raceCreators runs create concurrentCreators times at once and waits for
// all of them.
func (f *inProcessFixture) raceCreators(create func(i int)) {
	var wg sync.WaitGroup
	for i := range concurrentCreators {
		wg.Go(func() { create(i) })
	}
	wg.Wait()
}

func TestEnsureBucket_concurrentCallersCreateOneBucket(t *testing.T) {
	// Given: callers that all see the name as free at the same moment
	f := newInProcessFixtureOn(t, newBucketCheckGate(concurrentCreators))

	// When: they all ensure the same bucket
	errs := make([]error, concurrentCreators)
	f.raceCreators(func(i int) {
		if aerr := f.svc.EnsureBucket(context.Background(), "shared", "eu-west-1"); aerr != nil {
			errs[i] = aerr
		}
	})

	// Then: every caller succeeds, and the bucket was created exactly once
	for i, err := range errs {
		if err != nil {
			t.Errorf("caller %d: %v", i, err)
		}
	}
	if n := f.bucketCreatedEvents(); n != 1 {
		t.Errorf("S3BucketCreated events = %d, want 1", n)
	}
}

func TestEnsureTableWarehouseBucket_concurrentCallersCreateOneBucket(t *testing.T) {
	// Given: callers that all see the warehouse name as free at the same moment
	f := newInProcessFixtureOn(t, newBucketCheckGate(concurrentCreators))
	const name = "63a8e430-6e0b-46f5-k833abtwr6s8tmtsycedn8s4yc3xhuse1b--table-s3"

	// When: they all ensure the same warehouse bucket
	f.raceCreators(func(i int) {
		if aerr := f.svc.EnsureTableWarehouseBucket(context.Background(), name, ""); aerr != nil {
			t.Errorf("caller %d: %v", i, aerr)
		}
	})

	// Then: it was created exactly once
	if n := f.bucketCreatedEvents(); n != 1 {
		t.Errorf("S3BucketCreated events = %d, want 1", n)
	}
}

// createBucketRequest is a CreateBucket for name as the router would hand it
// to the handler, in region.
func createBucketRequest(name, region string) *http.Request {
	r := httptest.NewRequest(http.MethodPut, "/"+name, nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("bucket", name)
	ctx := context.WithValue(r.Context(), chi.RouteCtxKey, rctx)
	return r.WithContext(middleware.ContextWithRegion(ctx, region))
}

func TestCreateBucket_concurrentRequestsHaveOneWinner(t *testing.T) {
	cases := []struct {
		region    string
		loserCode int
		loserBody string
	}{
		// Outside us-east-1 a re-create is BucketAlreadyOwnedByYou; in
		// us-east-1 it is the legacy 200 OK. Either way only one request
		// created the bucket.
		{"eu-west-1", http.StatusConflict, "<Code>BucketAlreadyOwnedByYou</Code>"},
		{usEast1, http.StatusOK, ""},
	}
	for _, tc := range cases {
		t.Run(tc.region, func(t *testing.T) {
			// Given: requests that all see the name as free at the same moment
			f := newInProcessFixtureOn(t, newBucketCheckGate(concurrentCreators))
			recorders := make([]*httptest.ResponseRecorder, concurrentCreators)

			// When: they all create the same bucket
			f.raceCreators(func(i int) {
				recorders[i] = httptest.NewRecorder()
				f.svc.handler.CreateBucket(recorders[i], createBucketRequest("shared", tc.region))
			})

			// Then: one request created it, every other one got the answer
			// for an existing bucket, and it was announced once
			winners := 0
			for i, rec := range recorders {
				switch {
				case rec.Code == http.StatusOK && rec.Body.Len() == 0 && tc.loserCode != http.StatusOK:
					winners++
				case rec.Code == tc.loserCode && strings.Contains(rec.Body.String(), tc.loserBody):
				default:
					t.Errorf("request %d: %d %s", i, rec.Code, rec.Body.String())
				}
			}
			if tc.loserCode != http.StatusOK && winners != 1 {
				t.Errorf("requests that created the bucket = %d, want 1", winners)
			}
			if n := f.bucketCreatedEvents(); n != 1 {
				t.Errorf("S3BucketCreated events = %d, want 1", n)
			}
			if b := f.bucket(t, "shared"); b.Region != tc.region {
				t.Errorf("Region = %q, want %q", b.Region, tc.region)
			}
		})
	}
}
