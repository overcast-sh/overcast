package lambdaweb

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/overcast-sh/overcast/internal/clock"
	"github.com/overcast-sh/overcast/internal/config"
	"github.com/overcast-sh/overcast/internal/protocol"
	"github.com/overcast-sh/overcast/internal/state"
)

// ─── Fixtures ────────────────────────────────────────────────────────────────

func makeZip(t *testing.T, entries map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range entries {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("zip create %q: %v", name, err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatalf("zip write %q: %v", name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zip close: %v", err)
	}
	return buf.Bytes()
}

// fakeCode is a codeSource over an in-memory map of "bucket/key" to package
// bytes. Every object has the single version "v1". When gate is set, Fetch
// blocks on it, so a test decides when the worker may proceed.
type fakeCode struct {
	mu      sync.Mutex
	objects map[string][]byte
	gate    chan struct{}
	fetches int
}

func newFakeCode() *fakeCode { return &fakeCode{objects: map[string][]byte{}} }

func (c *fakeCode) put(bucket, key string, body []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.objects[bucket+"/"+key] = body
}

func (c *fakeCode) remove(bucket, key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.objects, bucket+"/"+key)
}

func (c *fakeCode) Resolve(_ context.Context, ref codeRef) (codeRef, int64, *protocol.AWSError) {
	c.mu.Lock()
	defer c.mu.Unlock()
	body, ok := c.objects[ref.Bucket+"/"+ref.Key]
	if !ok {
		return ref, 0, validationError("The S3 object s3://%s/%s does not exist.", ref.Bucket, ref.Key)
	}
	ref.VersionID = "v1"
	return ref, int64(len(body)), nil
}

func (c *fakeCode) Fetch(ctx context.Context, ref codeRef) ([]byte, *protocol.AWSError) {
	if c.gate != nil {
		select {
		case <-c.gate:
		case <-ctx.Done():
			return nil, validationError("canceled")
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.fetches++
	body, ok := c.objects[ref.Bucket+"/"+ref.Key]
	if !ok {
		return nil, validationError("The S3 object s3://%s/%s does not exist.", ref.Bucket, ref.Key)
	}
	return body, nil
}

type lifecycleFixture struct {
	t     *testing.T
	s     *Service
	code  *fakeCode
	store state.Store

	mu        sync.Mutex
	hookCalls int
	// atHook is the stored endpoint "dev" (state, updateStatus) each time the
	// change hook ran, so a test can see what the data plane would have seen.
	atHook []string
	hookFn func() error
}

func newLifecycleFixture(t *testing.T) *lifecycleFixture {
	t.Helper()
	f := &lifecycleFixture{t: t, code: newFakeCode(), store: state.NewMemoryStore()}
	f.s = New(&config.Config{Region: "us-east-1", AccountID: "111122223333"}, f.store, zap.NewNop(), clock.NewMock())
	f.s.InitCodeSource(f.code)
	f.s.InitChangeHookErr(func(region, function string) error {
		f.mu.Lock()
		f.hookCalls++
		var err error
		if f.hookFn != nil {
			err = f.hookFn()
		}
		f.mu.Unlock()
		if ep, found, _ := f.s.loadEndpoint(context.Background(), region, function, "dev"); found {
			f.mu.Lock()
			f.atHook = append(f.atHook, epState(ep)+"/"+epUpdateStatus(ep))
			f.mu.Unlock()
		}
		return err
	})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		f.s.StopRevisionWorker(ctx)
	})
	return f
}

func (f *lifecycleFixture) hooks() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.hookCalls
}

func (f *lifecycleFixture) createRevision(fn, key string) *revisionView {
	f.t.Helper()
	out, aerr := f.s.createWebFunctionRevisionTyped(context.Background(), &createRevisionRequest{FunctionName: fn, revisionConfigIn: *testRevisionConfig("code", key)})
	if aerr != nil {
		f.t.Fatalf("createWebFunctionRevision(%s): %s", key, aerr.Message)
	}
	return out
}

func (f *lifecycleFixture) revision(fn, id string) *revisionView {
	f.t.Helper()
	out, aerr := f.s.getWebFunctionRevisionTyped(context.Background(), &revisionRequest{FunctionName: fn, RevisionID: id})
	if aerr != nil {
		f.t.Fatalf("getWebFunctionRevision: %s", aerr.Message)
	}
	return out
}

func (f *lifecycleFixture) endpoint(fn, name string) *endpointView {
	f.t.Helper()
	out, aerr := f.s.getWebFunctionEndpointTyped(context.Background(), &endpointRequest{FunctionName: fn, EndpointName: name})
	if aerr != nil {
		f.t.Fatalf("getWebFunctionEndpoint: %s", aerr.Message)
	}
	return out
}

func (f *lifecycleFixture) createApp(name string) *createWebFunctionResponse {
	f.t.Helper()
	out, aerr := f.s.createWebFunctionTyped(context.Background(), &createWebFunctionRequest{
		FunctionName:   name,
		RevisionConfig: testRevisionConfig("code", "v1.zip"),
		EndpointConfig: &endpointConfigIn{EndpointName: "dev", EndpointType: endpointHomeRegion, AuthType: authApplicationManaged, AutoDeploymentMode: deployLatestRevision},
	})
	if aerr != nil {
		f.t.Fatalf("createWebFunction: %s", aerr.Message)
	}
	return out
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func goodPackage(t *testing.T) []byte { return makeZip(t, map[string]string{"index.js": "// app"}) }

// ─── Revision lifecycle ──────────────────────────────────────────────────────

func TestRevisionLifecycle_pendingUntilThePackageIsChecked(t *testing.T) {
	// Given: a function whose first package is still being fetched
	f := newLifecycleFixture(t)
	f.code.put("code", "v1.zip", goodPackage(t))
	f.code.gate = make(chan struct{})
	out := f.createApp("shop")
	first := out.Revision.RevisionID

	// Then: the revision reads Pending and the LatestRevision endpoint has nothing to serve yet
	if got := f.revision("shop", first); got.State != statePending {
		t.Fatalf("State = %s, want Pending", got.State)
	}
	if ep := f.endpoint("shop", "dev"); len(ep.RevisionWeights) != 0 || ep.State != stateActive {
		t.Errorf("endpoint = %s with weights %+v, want Active with none", ep.State, ep.RevisionWeights)
	}

	// When: the package is fetched
	close(f.code.gate)

	// Then: the revision becomes Active, pinned to its S3 version, and the endpoint serves it
	eventually(t, "the revision to become Active", func() bool { return f.revision("shop", first).State == stateActive })
	got := f.revision("shop", first)
	if got.BuildConfig.CodeConfig.S3Object.VersionID != "v1" || len(got.Errors) != 0 {
		t.Errorf("revision = %+v", got)
	}
	// the endpoint follows the revision a moment after it turns Active
	eventually(t, "the endpoint to serve it", func() bool {
		ep := f.endpoint("shop", "dev")
		return len(ep.RevisionWeights) == 1 && ep.UpdateStatus == updateSuccessful
	})
	ep := f.endpoint("shop", "dev")
	if len(ep.RevisionWeights) != 1 || ep.RevisionWeights[0].RevisionID != first || ep.UpdateStatus != updateSuccessful {
		t.Errorf("endpoint weights = %+v, updateStatus = %s", ep.RevisionWeights, ep.UpdateStatus)
	}
}

func TestRevisionLifecycle_corruptPackageFailsPermanently(t *testing.T) {
	// Given: a function and a package that is not a zip
	f := newLifecycleFixture(t)
	f.code.put("code", "v1.zip", goodPackage(t))
	f.createApp("shop")
	eventually(t, "the first revision", func() bool { return len(mustList(t, f, "shop")) == 1 && mustList(t, f, "shop")[0].State == stateActive })
	f.code.put("code", "bad.zip", []byte("this is not a zip"))

	// When: a revision is created from it
	rev := f.createRevision("shop", "bad.zip")

	// Then: it fails with the reason in errors[]
	eventually(t, "the revision to fail", func() bool { return f.revision("shop", rev.RevisionID).State == stateFailed })
	got := f.revision("shop", rev.RevisionID)
	if len(got.Errors) != 1 || got.Errors[0].Attribute != "buildConfig.codeConfig.s3Object" || got.Errors[0].ErrorCode != "InvalidCodePackage" ||
		got.StateReason == "" || got.StateReason != got.Errors[0].ErrorMessage {
		t.Errorf("revision = state %s reason %q errors %+v", got.State, got.StateReason, got.Errors)
	}

	// And: the endpoint keeps serving the earlier revision
	if ep := f.endpoint("shop", "dev"); len(ep.RevisionWeights) != 1 || ep.RevisionWeights[0].RevisionID == rev.RevisionID {
		t.Errorf("endpoint weights = %+v", ep.RevisionWeights)
	}

	// And: it cannot be put behind an endpoint
	_, aerr := f.s.updateWebFunctionEndpointTyped(context.Background(), &updateEndpointRequest{
		FunctionName: "shop", EndpointName: "dev", AutoDeploymentMode: deployDisabled, RevisionWeights: []revisionWt{{RevisionID: rev.RevisionID, Weight: 100}},
	})
	if aerr == nil || aerr.Code != "ValidationException" {
		t.Errorf("weights on a Failed revision: %+v", aerr)
	}

	// And: replacing the object does not bring it back
	f.code.put("code", "bad.zip", goodPackage(t))
	time.Sleep(20 * time.Millisecond)
	if got := f.revision("shop", rev.RevisionID); got.State != stateFailed {
		t.Errorf("State = %s after the object was fixed, want Failed", got.State)
	}
}

func mustList(t *testing.T, f *lifecycleFixture, fn string) []revisionSummary {
	t.Helper()
	out, aerr := f.s.listWebFunctionRevisionsTyped(context.Background(), &listRequest{FunctionName: fn})
	if aerr != nil {
		t.Fatalf("list revisions: %s", aerr.Message)
	}
	return out.Revisions
}

func TestRevisionLifecycle_unsafePackagePathFails(t *testing.T) {
	// Given: a package with an entry that escapes the package root
	f := newLifecycleFixture(t)
	f.code.put("code", "v1.zip", makeZip(t, map[string]string{"index.js": "x", "../../etc/passwd": "x"}))

	// When: a function is created from it
	rev := f.createApp("shop").Revision

	// Then: the revision fails
	eventually(t, "the revision to fail", func() bool { return f.revision("shop", rev.RevisionID).State == stateFailed })
}

func TestRevisionLifecycle_objectGoneBeforeTheFetchFails(t *testing.T) {
	// Given: a revision waiting for its package, whose object is then deleted
	f := newLifecycleFixture(t)
	f.code.put("code", "v1.zip", goodPackage(t))
	f.code.gate = make(chan struct{})
	rev := f.createApp("shop").Revision
	f.code.remove("code", "v1.zip")

	// When: the worker goes to fetch it
	close(f.code.gate)

	// Then: the revision fails as unreadable
	eventually(t, "the revision to fail", func() bool { return f.revision("shop", rev.RevisionID).State == stateFailed })
	if got := f.revision("shop", rev.RevisionID); len(got.Errors) != 1 || got.Errors[0].ErrorCode != "CodeUnavailable" {
		t.Errorf("errors = %+v", got.Errors)
	}
}

func TestCreateRevision_missingObjectIsRefusedUpFront(t *testing.T) {
	// Given: a function and no object at the key
	f := newLifecycleFixture(t)
	f.code.put("code", "v1.zip", goodPackage(t))
	f.createApp("shop")

	// When: a revision names a key that does not exist
	_, aerr := f.s.createWebFunctionRevisionTyped(context.Background(), &createRevisionRequest{FunctionName: "shop", revisionConfigIn: *testRevisionConfig("code", "missing.zip")})

	// Then: the request fails and no revision is stored
	if aerr == nil || aerr.Code != "ValidationException" {
		t.Fatalf("error = %+v", aerr)
	}
	eventually(t, "the first revision", func() bool { return len(mustList(t, f, "shop")) == 1 })
}

func TestRevisionLifecycle_listsNewestFirst(t *testing.T) {
	// Given: three revisions created in turn
	f := newLifecycleFixture(t)
	f.code.put("code", "v1.zip", goodPackage(t))
	first := f.createApp("shop").Revision.RevisionID
	second := f.createRevision("shop", "v1.zip").RevisionID
	third := f.createRevision("shop", "v1.zip").RevisionID

	// When: they are listed
	got := mustList(t, f, "shop")

	// Then: the newest is first
	if len(got) != 3 || got[0].RevisionID != third || got[1].RevisionID != second || got[2].RevisionID != first {
		t.Errorf("order = %v", got)
	}
}

func TestRevisionLifecycle_pendingRevisionCannotTakeTraffic(t *testing.T) {
	// Given: an endpoint on a Disabled-mode function with a Pending second revision
	f := newLifecycleFixture(t)
	f.code.put("code", "v1.zip", goodPackage(t))
	first := f.createApp("shop").Revision.RevisionID
	eventually(t, "the first revision", func() bool { return f.revision("shop", first).State == stateActive })
	f.code.gate = make(chan struct{})
	second := f.createRevision("shop", "v1.zip").RevisionID

	// When: weights name the Pending revision
	_, aerr := f.s.updateWebFunctionEndpointTyped(context.Background(), &updateEndpointRequest{
		FunctionName: "shop", EndpointName: "dev", AutoDeploymentMode: deployDisabled, RevisionWeights: []revisionWt{{RevisionID: second, Weight: 100}},
	})

	// Then: it is refused
	if aerr == nil || aerr.Code != "ValidationException" {
		t.Errorf("weights on a Pending revision: %+v", aerr)
	}

	// And: with the endpoint Disabled, switching to LatestRevision conflicts while the latest is Pending
	if _, aerr := f.s.updateWebFunctionEndpointTyped(context.Background(), &updateEndpointRequest{FunctionName: "shop", EndpointName: "dev", AutoDeploymentMode: deployDisabled}); aerr != nil {
		t.Fatalf("switch to Disabled: %s", aerr.Message)
	}
	_, aerr = f.s.updateWebFunctionEndpointTyped(context.Background(), &updateEndpointRequest{FunctionName: "shop", EndpointName: "dev", AutoDeploymentMode: deployLatestRevision})
	if aerr == nil || aerr.Code != "ConflictException" {
		t.Errorf("switch with a Pending latest revision: %+v", aerr)
	}

	// And: once it is Active the switch works and serves it
	close(f.code.gate)
	eventually(t, "the second revision", func() bool { return f.revision("shop", second).State == stateActive })
	ep, aerr := f.s.updateWebFunctionEndpointTyped(context.Background(), &updateEndpointRequest{FunctionName: "shop", EndpointName: "dev", AutoDeploymentMode: deployLatestRevision})
	if aerr != nil || len(ep.RevisionWeights) != 1 || ep.RevisionWeights[0].RevisionID != second {
		t.Errorf("switch = %+v, %+v", ep, aerr)
	}
}

func TestRevisionLifecycle_cleanupIgnoresPendingRevisions(t *testing.T) {
	// Given: a function holding 50 Active revisions and one Pending one
	f := newLifecycleFixture(t)
	ctx := context.Background()
	fn := &webFunction{Name: "shop", Region: "us-east-1", ARN: f.s.functionARN("us-east-1", "shop")}
	seed := func(seq int, st string) string {
		id := fmt.Sprintf("rev-%05d", seq)
		rev := &revision{Function: "shop", Region: "us-east-1", ID: id, Seq: seq, State: st, CreatedAt: f.s.now()}
		if aerr := putRecord(ctx, f.s, nsRevisions, childKey("us-east-1", "shop", id), rev); aerr != nil {
			t.Fatalf("seed: %s", aerr.Message)
		}
		return id
	}
	for i := 1; i <= 50; i++ {
		seed(i, stateActive)
	}
	seed(51, statePending)

	// When: cleanup runs
	if aerr := f.s.cleanupRevisions(ctx, fn, "rev-00050"); aerr != nil {
		t.Fatalf("cleanup: %s", aerr.Message)
	}

	// Then: nothing is deleted, because only 50 revisions are settled
	revs, _ := f.s.listRevisions(ctx, "us-east-1", "shop")
	if len(revs) != 51 {
		t.Fatalf("revisions = %d, want 51", len(revs))
	}

	// And: once a 51st settles, only the oldest settled revision goes
	seed(52, stateActive)
	if aerr := f.s.cleanupRevisions(ctx, fn, "rev-00052"); aerr != nil {
		t.Fatalf("cleanup: %s", aerr.Message)
	}
	revs, _ = f.s.listRevisions(ctx, "us-east-1", "shop")
	if len(revs) != 51 || revs[0].ID != "rev-00002" {
		t.Errorf("revisions = %d, oldest %s; want 51 starting at rev-00002", len(revs), revs[0].ID)
	}
	if p := revs[len(revs)-2]; p.ID != "rev-00051" || p.State != statePending {
		t.Errorf("the Pending revision was disturbed: %+v", p)
	}
}

func TestRevisionLifecycle_resumesPendingRevisionsAtStart(t *testing.T) {
	// Given: a store holding a revision left Pending by an earlier process
	f := newLifecycleFixture(t)
	f.code.put("code", "v1.zip", goodPackage(t))
	f.code.gate = make(chan struct{})
	rev := f.createApp("shop").Revision
	stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	f.s.StopRevisionWorker(stopCtx)
	if got := f.revision("shop", rev.RevisionID); got.State != statePending {
		t.Fatalf("State after Stop = %s, want Pending", got.State)
	}

	// When: a new service starts on the same store and is used for the first time
	code := newFakeCode()
	code.put("code", "v1.zip", goodPackage(t))
	s2 := New(&config.Config{Region: "us-east-1", AccountID: "111122223333"}, f.store, zap.NewNop(), clock.NewMock())
	s2.InitCodeSource(code)
	t.Cleanup(func() { s2.StopRevisionWorker(context.Background()) })
	_, aerr := s2.listWebFunctionRevisionsTyped(context.Background(), &listRequest{FunctionName: "shop"})
	if aerr != nil {
		t.Fatalf("list: %s", aerr.Message)
	}

	// Then: the revision finishes
	eventually(t, "the revision to resume", func() bool {
		got, _ := s2.getWebFunctionRevisionTyped(context.Background(), &revisionRequest{FunctionName: "shop", RevisionID: rev.RevisionID})
		return got != nil && got.State == stateActive
	})
}

func TestMarkRevisionFailed_recordsTheCauseAndNotifiesTheDataPlane(t *testing.T) {
	// Given: an Active revision
	f := newLifecycleFixture(t)
	f.code.put("code", "v1.zip", goodPackage(t))
	rev := f.createApp("shop").Revision
	eventually(t, "the revision", func() bool { return f.revision("shop", rev.RevisionID).State == stateActive })
	before := f.hooks()

	// When: an environment reports it cannot fetch the code
	f.s.markRevisionFailed(context.Background(), "us-east-1", "shop", rev.RevisionID, "CodeUnavailable", "The S3 object is gone.")

	// Then: the revision is Failed with the cause, and the hook ran
	got := f.revision("shop", rev.RevisionID)
	if got.State != stateFailed || got.StateReason != "The S3 object is gone." || len(got.Errors) != 1 || got.Errors[0].ErrorCode != "CodeUnavailable" {
		t.Errorf("revision = %+v", got)
	}
	if f.hooks() <= before {
		t.Error("the change hook did not run")
	}

	// And: Failed is permanent, even if the data plane reports again
	f.s.markRevisionFailed(context.Background(), "us-east-1", "shop", rev.RevisionID, "Other", "later")
	if got := f.revision("shop", rev.RevisionID); got.Errors[0].ErrorCode != "CodeUnavailable" {
		t.Errorf("errors = %+v", got.Errors)
	}
}

func TestStopLifecycle_leavesInterruptedRevisionsPending(t *testing.T) {
	// Given: a revision whose package fetch is blocked
	f := newLifecycleFixture(t)
	f.code.put("code", "v1.zip", goodPackage(t))
	f.code.gate = make(chan struct{})
	rev := f.createApp("shop").Revision

	// When: the service stops
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan struct{})
	go func() { f.s.StopRevisionWorker(ctx); close(done) }()

	// Then: Stop returns, and the revision is still Pending, not Failed
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("StopRevisionWorker did not return")
	}
	if got := f.revision("shop", rev.RevisionID); got.State != statePending {
		t.Errorf("State = %s, want Pending", got.State)
	}
}

// ─── Endpoint lifecycle ──────────────────────────────────────────────────────

func TestEndpointLifecycle_pendingUntilRegistered(t *testing.T) {
	// Given: a service with a change hook
	f := newLifecycleFixture(t)
	f.code.put("code", "v1.zip", goodPackage(t))

	// When: a function and endpoint are created
	out := f.createApp("shop")

	// Then: the create response says Pending, the data plane saw it Pending, and it reads Active after
	if out.Endpoint.State != statePending {
		t.Errorf("create State = %s", out.Endpoint.State)
	}
	if len(f.atHook) == 0 || f.atHook[0] != "Pending/" {
		t.Errorf("state at the first hook = %v", f.atHook)
	}
	if ep := f.endpoint("shop", "dev"); ep.State != stateActive || ep.UpdateStatus != updateSuccessful {
		t.Errorf("endpoint = %s / %s", ep.State, ep.UpdateStatus)
	}
}

func TestEndpointLifecycle_updateIsInProgressWhileTheDataPlaneApplies(t *testing.T) {
	// Given: an Active endpoint
	f := newLifecycleFixture(t)
	f.code.put("code", "v1.zip", goodPackage(t))
	f.createApp("shop")
	eventually(t, "routing", func() bool { return len(f.endpoint("shop", "dev").RevisionWeights) == 1 })
	f.mu.Lock()
	f.atHook = nil
	f.mu.Unlock()

	// When: its auth type changes
	out, aerr := f.s.updateWebFunctionEndpointTyped(context.Background(), &updateEndpointRequest{FunctionName: "shop", EndpointName: "dev", AuthType: authIam})
	if aerr != nil {
		t.Fatalf("update: %s", aerr.Message)
	}

	// Then: the response and the data plane see InProgress with the state still Active, and a read finds Successful
	if out.UpdateStatus != updateInProgress || out.State != stateActive {
		t.Errorf("response = %s / %s", out.State, out.UpdateStatus)
	}
	if len(f.atHook) != 1 || f.atHook[0] != "Active/InProgress" {
		t.Errorf("state at the hook = %v", f.atHook)
	}
	if ep := f.endpoint("shop", "dev"); ep.UpdateStatus != updateSuccessful || ep.RegionalEndpoints["us-east-1"].UpdateStatus != updateSuccessful {
		t.Errorf("endpoint = %s, regional = %+v", ep.UpdateStatus, ep.RegionalEndpoints["us-east-1"])
	}
}

func TestEndpointLifecycle_hookFailureFailsTheUpdate(t *testing.T) {
	// Given: an endpoint whose next change the data plane refuses
	f := newLifecycleFixture(t)
	f.code.put("code", "v1.zip", goodPackage(t))
	f.createApp("shop")
	f.mu.Lock()
	f.hookFn = func() error { return errors.New("routing table rebuild failed") }
	f.mu.Unlock()

	// When: it is updated
	if _, aerr := f.s.updateWebFunctionEndpointTyped(context.Background(), &updateEndpointRequest{FunctionName: "shop", EndpointName: "dev", AuthType: authIam}); aerr != nil {
		t.Fatalf("update: %s", aerr.Message)
	}

	// Then: updateStatus is Failed with the reason, and the endpoint itself stays Active
	ep := f.endpoint("shop", "dev")
	if ep.State != stateActive || ep.UpdateStatus != updateFailed || ep.UpdateStatusReason == "" {
		t.Errorf("endpoint = %s / %s / %q", ep.State, ep.UpdateStatus, ep.UpdateStatusReason)
	}
}

func TestEndpointLifecycle_registrationFailureFailsTheEndpoint(t *testing.T) {
	// Given: a data plane that refuses new endpoints
	f := newLifecycleFixture(t)
	f.code.put("code", "v1.zip", goodPackage(t))
	f.hookFn = func() error { return errors.New("no capacity") }

	// When: an endpoint is created
	f.createApp("shop")

	// Then: it is Failed with a reason
	ep := f.endpoint("shop", "dev")
	if ep.State != stateFailed || ep.StateReason == "" {
		t.Errorf("endpoint = %s / %q", ep.State, ep.StateReason)
	}
}

func TestEndpointLifecycle_deleteDrainsBeforeRemoving(t *testing.T) {
	// Given: an Active endpoint
	f := newLifecycleFixture(t)
	f.code.put("code", "v1.zip", goodPackage(t))
	out := f.createApp("shop")
	// the revision becomes Active in the background and the endpoint follows;
	// let that settle so its hook calls are not mistaken for the delete's
	eventually(t, "the endpoint to follow the Active revision", func() bool {
		ep := f.endpoint("shop", "dev")
		return f.revision("shop", out.Revision.RevisionID).State == stateActive &&
			len(ep.RevisionWeights) == 1 && ep.UpdateStatus == updateSuccessful
	})
	f.mu.Lock()
	f.atHook = nil
	f.mu.Unlock()

	// When: it is deleted
	if _, aerr := f.s.deleteWebFunctionEndpointTyped(context.Background(), &endpointRequest{FunctionName: "shop", EndpointName: "dev"}); aerr != nil {
		t.Fatalf("delete: %s", aerr.Message)
	}

	// Then: the data plane was told while it was Deleting, and then once more after it was gone
	f.mu.Lock()
	seen := append([]string(nil), f.atHook...)
	f.mu.Unlock()
	if len(seen) != 1 || seen[0] != "Deleting/Successful" {
		t.Errorf("state at the hook = %v", seen)
	}
	if f.hooks() < 2 {
		t.Errorf("hook calls = %d", f.hooks())
	}
	if _, aerr := f.s.getWebFunctionEndpointTyped(context.Background(), &endpointRequest{FunctionName: "shop", EndpointName: "dev"}); aerr == nil || aerr.Code != "ResourceNotFoundException" {
		t.Errorf("endpoint survived: %+v", aerr)
	}
}

func TestEndpointLifecycle_oldRecordsReadActive(t *testing.T) {
	// Given: an endpoint and a revision stored before the data plane, with no state
	ep := &endpoint{}
	rev := &revision{}

	// Then: they read Active and Successful
	if epState(ep) != stateActive || epUpdateStatus(ep) != updateSuccessful || revState(rev) != stateActive {
		t.Errorf("state = %s / %s / %s", epState(ep), epUpdateStatus(ep), revState(rev))
	}
}
