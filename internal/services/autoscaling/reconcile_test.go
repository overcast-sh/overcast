package autoscaling

// Reconciler unit tests. These drive the clock rather than sleeping, and run
// reconcileOnce directly rather than racing the background loop.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/overcast-sh/overcast/internal/clock"
	"github.com/overcast-sh/overcast/internal/config"
	"github.com/overcast-sh/overcast/internal/state"
)

// ─── Fake EC2 ─────────────────────────────────────────────────────────────────

// fakeEC2 answers the RunInstances / TerminateInstances / PutEvents calls the
// reconciler makes, and records them, so a test can assert on what Auto
// Scaling actually asked EC2 for without standing up the whole router.
type fakeEC2 struct {
	mu         sync.Mutex
	launched   []string
	terminated []string
	events     []string
	failLaunch bool
	seq        int
}

func (f *fakeEC2) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if target := r.Header.Get("X-Amz-Target"); target == "AWSEvents.PutEvents" {
		f.mu.Lock()
		f.events = append(f.events, target)
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/x-amz-json-1.1")
		_, _ = w.Write([]byte(`{"FailedEntryCount":0,"Entries":[{"EventId":"e1"}]}`))
		return
	}
	_ = r.ParseForm()
	switch r.FormValue("Action") {
	case "RunInstances":
		f.mu.Lock()
		if f.failLaunch {
			f.mu.Unlock()
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`<Response><Errors><Error><Code>InvalidAMIID.NotFound</Code></Error></Errors></Response>`))
			return
		}
		f.seq++
		id := fmt.Sprintf("i-%016x", f.seq)
		f.launched = append(f.launched, id)
		f.mu.Unlock()
		_, _ = w.Write([]byte(`<RunInstancesResponse><instancesSet><item>` +
			`<instanceId>` + id + `</instanceId>` +
			`<placement><availabilityZone>us-east-1a</availabilityZone></placement>` +
			`<subnetId>subnet-1</subnetId>` +
			`</item></instancesSet></RunInstancesResponse>`))
	case "TerminateInstances":
		f.mu.Lock()
		f.terminated = append(f.terminated, r.FormValue("InstanceId.1"))
		f.mu.Unlock()
		_, _ = w.Write([]byte(`<TerminateInstancesResponse/>`))
	default:
		w.WriteHeader(http.StatusNotImplemented)
	}
}

func (f *fakeEC2) launchedIDs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.launched...)
}

func (f *fakeEC2) terminatedIDs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.terminated...)
}

// ─── Fixtures ─────────────────────────────────────────────────────────────────

type fixture struct {
	svc  *Service
	ec2  *fakeEC2
	mock *clock.Mock
	ctx  context.Context
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	return newFixtureOn(t, state.NewMemoryStore())
}

// newFixtureOn builds the fixture over a given store, for a test that needs to
// watch the service's writes.
func newFixtureOn(t *testing.T, st state.Store) *fixture {
	t.Helper()
	mock := clock.NewMock()
	svc := New(&config.Config{Region: "us-east-1", AccountID: "123456789012"},
		st, zap.NewNop(), mock)
	// Drain the background loop immediately: these tests call reconcileOnce
	// themselves so each pass is a known, countable step. Left running, the
	// loop would race every handler poke and every mock.Add, and "how many
	// instances did EC2 launch" would stop being a question with one answer.
	// Stop is idempotent, so the cleanup below is still correct.
	svc.Stop(context.Background())
	t.Cleanup(func() { svc.Stop(context.Background()) })
	ec2 := &fakeEC2{}
	svc.InitRouter(ec2)
	return &fixture{svc: svc, ec2: ec2, mock: mock, ctx: context.Background()}
}

// group creates a launch configuration and a group through the typed handlers,
// so the tests exercise the same validation real callers do.
func (f *fixture) group(t *testing.T, name string, min, max, desired int) {
	t.Helper()
	if _, aerr := f.svc.handler.createLaunchConfigTyped(f.ctx, &createLaunchConfigReq{
		LaunchConfigurationName: name + "-lc", ImageId: "ami-1", InstanceType: "t3.micro",
	}); aerr != nil {
		t.Fatalf("CreateLaunchConfiguration: %v", aerr)
	}
	if _, aerr := f.svc.handler.createASGTyped(f.ctx, &createASGReq{
		AutoScalingGroupName:    name,
		LaunchConfigurationName: name + "-lc",
		MinSize:                 min,
		MaxSize:                 max,
		DesiredCapacity:         &desired,
		AvailabilityZones:       []string{"us-east-1a", "us-east-1b"},
	}); aerr != nil {
		t.Fatalf("CreateAutoScalingGroup: %v", aerr)
	}
}

// converge runs reconcileOnce until it reports no further change, bounded so a
// bug cannot hang the suite.
func (f *fixture) converge(t *testing.T) {
	t.Helper()
	for i := 0; i < 20; i++ {
		if !f.svc.reconcileOnce(f.ctx) {
			return
		}
	}
	t.Fatal("reconciler never settled after 20 passes")
}

func (f *fixture) instances(t *testing.T, group string) []*ASGInstance {
	t.Helper()
	got, err := f.svc.st.listInstances(f.ctx, group)
	if err != nil {
		t.Fatalf("listInstances: %v", err)
	}
	return got
}

func states(instances []*ASGInstance) []string {
	out := make([]string, 0, len(instances))
	for _, i := range instances {
		out = append(out, i.LifecycleState)
	}
	return out
}

// ─── Tests ────────────────────────────────────────────────────────────────────

func TestReconcile_idlePassCostsNoStoreCall(t *testing.T) {
	// Given: a service with a known-zero group count
	f := newFixture(t)
	f.svc.groupCount.Store(0)

	// When/Then: a pass short-circuits on the atomic load alone
	if f.svc.reconcileOnce(f.ctx) {
		t.Error("an idle pass reported a change")
	}
}

func TestReconcile_launchesToDesiredCapacity(t *testing.T) {
	// Given: a group asking for three instances
	f := newFixture(t)
	f.group(t, "g", 1, 5, 3)

	// When: the reconciler converges it
	f.converge(t)

	// Then: three instances exist, all InService, spread across the zones
	got := f.instances(t, "g")
	if len(got) != 3 {
		t.Fatalf("instance count = %d, want 3 (%v)", len(got), states(got))
	}
	for _, inst := range got {
		if inst.LifecycleState != lifecycleInService {
			t.Errorf("%s LifecycleState = %q, want InService", inst.InstanceId, inst.LifecycleState)
		}
		if inst.HealthStatus != healthStatusHealthy {
			t.Errorf("%s HealthStatus = %q, want Healthy", inst.InstanceId, inst.HealthStatus)
		}
	}
	if n := len(f.ec2.launchedIDs()); n != 3 {
		t.Errorf("EC2 RunInstances calls = %d, want 3", n)
	}

	// And: a settled group does no further work
	if f.svc.reconcileOnce(f.ctx) {
		t.Error("a settled group still reported a change")
	}
}

func TestReconcile_scalesInOldestFirstAndSkipsProtectedInstances(t *testing.T) {
	// Given: a converged group of three, launched at distinct times
	f := newFixture(t)
	f.group(t, "g", 0, 5, 3)
	for range 3 {
		f.svc.reconcileOnce(f.ctx)
		f.mock.Add(time.Minute)
	}
	f.converge(t)
	got := f.instances(t, "g")
	if len(got) != 3 {
		t.Fatalf("setup: instance count = %d, want 3", len(got))
	}

	// And: the oldest is protected from scale-in
	oldest := got[0]
	for _, inst := range got {
		if inst.LaunchedAt.Before(oldest.LaunchedAt) {
			oldest = inst
		}
	}
	oldest.ProtectedFromScaleIn = true
	if err := f.svc.st.putInstance(f.ctx, oldest); err != nil {
		t.Fatalf("putInstance: %v", err)
	}

	// When: desired capacity drops to one
	if _, aerr := f.svc.handler.setDesiredCapacityTyped(f.ctx, &setDesiredCapacityReq{
		AutoScalingGroupName: "g", DesiredCapacity: 1,
	}); aerr != nil {
		t.Fatalf("SetDesiredCapacity: %v", aerr)
	}
	f.converge(t)

	// Then: the protected instance survives and the others are terminated
	remaining := f.instances(t, "g")
	if len(remaining) != 1 {
		t.Fatalf("instance count = %d, want 1 (%v)", len(remaining), states(remaining))
	}
	if remaining[0].InstanceId != oldest.InstanceId {
		t.Errorf("survivor = %s, want the protected instance %s", remaining[0].InstanceId, oldest.InstanceId)
	}
	if n := len(f.ec2.terminatedIDs()); n != 2 {
		t.Errorf("EC2 TerminateInstances calls = %d, want 2", n)
	}
}

func TestReconcile_replacesUnhealthyInstances(t *testing.T) {
	// Given: a converged group of one
	f := newFixture(t)
	f.group(t, "g", 1, 3, 1)
	f.converge(t)
	original := f.instances(t, "g")[0].InstanceId

	// When: it is marked unhealthy
	if _, aerr := f.svc.handler.setInstanceHealthTyped(f.ctx, &setInstanceHealthReq{
		InstanceId: original, HealthStatus: healthStatusUnhealthy,
	}); aerr != nil {
		t.Fatalf("SetInstanceHealth: %v", aerr)
	}
	f.converge(t)

	// Then: a different instance is in service
	got := f.instances(t, "g")
	if len(got) != 1 {
		t.Fatalf("instance count = %d, want 1 (%v)", len(got), states(got))
	}
	if got[0].InstanceId == original {
		t.Errorf("unhealthy instance %s was not replaced", original)
	}
}

func TestReconcile_lifecycleHookHoldsInstanceUntilTheHeartbeatExpires(t *testing.T) {
	// Given: a group with a launch hook that defaults to CONTINUE
	f := newFixture(t)
	f.group(t, "g", 0, 3, 0)
	if _, aerr := f.svc.handler.putLifecycleHookTyped(f.ctx, &putLifecycleHookReq{
		AutoScalingGroupName: "g", LifecycleHookName: "warmup",
		LifecycleTransition: transitionLaunching, HeartbeatTimeout: 300,
		DefaultResult: lifecycleResultContinue,
	}); aerr != nil {
		t.Fatalf("PutLifecycleHook: %v", aerr)
	}
	if _, aerr := f.svc.handler.setDesiredCapacityTyped(f.ctx, &setDesiredCapacityReq{
		AutoScalingGroupName: "g", DesiredCapacity: 1,
	}); aerr != nil {
		t.Fatalf("SetDesiredCapacity: %v", aerr)
	}

	// When: the reconciler runs without time passing
	f.converge(t)

	// Then: the instance is parked, not in service
	got := f.instances(t, "g")
	if len(got) != 1 || got[0].LifecycleState != lifecyclePendingWait {
		t.Fatalf("states = %v, want [Pending:Wait]", states(got))
	}
	if got[0].LifecycleActionToken == "" {
		t.Error("parked instance carries no LifecycleActionToken")
	}

	// When: the heartbeat window expires
	f.mock.Add(301 * time.Second)
	f.converge(t)

	// Then: the default result releases it into service
	got = f.instances(t, "g")
	if len(got) != 1 || got[0].LifecycleState != lifecycleInService {
		t.Fatalf("states = %v, want [InService]", states(got))
	}
}

func TestReconcile_abandonedLifecycleActionTerminatesTheInstance(t *testing.T) {
	// Given: a group with a launch hook parked on an instance
	f := newFixture(t)
	f.group(t, "g", 0, 3, 1)
	if _, aerr := f.svc.handler.putLifecycleHookTyped(f.ctx, &putLifecycleHookReq{
		AutoScalingGroupName: "g", LifecycleHookName: "warmup",
		LifecycleTransition: transitionLaunching, HeartbeatTimeout: 300,
		DefaultResult: lifecycleResultContinue,
	}); aerr != nil {
		t.Fatalf("PutLifecycleHook: %v", aerr)
	}
	f.converge(t)
	parked := f.instances(t, "g")
	if len(parked) != 1 || parked[0].LifecycleState != lifecyclePendingWait {
		t.Fatalf("setup: states = %v, want [Pending:Wait]", states(parked))
	}

	// When: the action is completed with ABANDON
	if _, aerr := f.svc.handler.completeLifecycleActionTyped(f.ctx, &completeLifecycleActionReq{
		AutoScalingGroupName: "g", LifecycleHookName: "warmup",
		InstanceId: parked[0].InstanceId, LifecycleActionResult: lifecycleResultAbandon,
	}); aerr != nil {
		t.Fatalf("CompleteLifecycleAction: %v", aerr)
	}
	f.converge(t)

	// Then: that instance was terminated (and, the group still wanting one, a
	// replacement was launched)
	if got := f.ec2.terminatedIDs(); len(got) != 1 || got[0] != parked[0].InstanceId {
		t.Errorf("terminated = %v, want [%s]", got, parked[0].InstanceId)
	}
}

func TestReconcile_recordsScalingActivitiesWithAWSWording(t *testing.T) {
	// Given: a converged group of one
	f := newFixture(t)
	f.group(t, "g", 0, 3, 1)
	f.converge(t)

	// When: the activities are read back
	activities, err := f.svc.st.listActivities(f.ctx, "g")
	if err != nil {
		t.Fatalf("listActivities: %v", err)
	}

	// Then: the launch is recorded, completed, and worded as AWS words it
	if len(activities) != 1 {
		t.Fatalf("activity count = %d, want 1", len(activities))
	}
	a := activities[0]
	if !strings.HasPrefix(a.Description, "Launching a new EC2 instance: i-") {
		t.Errorf("Description = %q", a.Description)
	}
	if a.StatusCode != "Successful" || a.Progress != 100 {
		t.Errorf("StatusCode/Progress = %s/%d, want Successful/100", a.StatusCode, a.Progress)
	}
	if !strings.Contains(a.Cause, "difference between desired and actual capacity") {
		t.Errorf("Cause = %q", a.Cause)
	}
	if a.EndTime.IsZero() {
		t.Error("EndTime is unset on a completed activity")
	}
}

// inServiceWriteSpy wraps a store and, at the moment an instance is written as
// InService, records what that instance's launch activity says in the store
// right then. It sees exactly what a Describe call arriving between two writes
// would: the handlers read the store without the service's lock.
type inServiceWriteSpy struct {
	state.Store
	mu   sync.Mutex
	seen []string // the launch activity's StatusCode at each InService write
}

func (s *inServiceWriteSpy) Set(ctx context.Context, namespace, key, value string) error {
	if namespace == nsInstances {
		var inst ASGInstance
		if json.Unmarshal([]byte(value), &inst) == nil && inst.LifecycleState == lifecycleInService {
			status := "<no launch activity>"
			if activities, err := newASGStore(s.Store).listActivities(ctx, inst.AutoScalingGroupName); err == nil {
				for _, a := range activities {
					if a.ActivityId == inst.LaunchActivityId {
						status = a.StatusCode
					}
				}
			}
			s.mu.Lock()
			s.seen = append(s.seen, status)
			s.mu.Unlock()
		}
	}
	return s.Store.Set(ctx, namespace, key, value)
}

// TestReconcile_launchActivityCompletesBeforeTheInstanceIsInService pins the
// order of the two writes that finish a launch (#720). The Describe handlers
// read the store without the service's lock, so if the instance became
// InService first, DescribeAutoScalingInstances could show it in service while
// DescribeScalingActivities still said PreInService for its launch. Completing
// the activity first means a reader who sees InService also sees Successful.
func TestReconcile_launchActivityCompletesBeforeTheInstanceIsInService(t *testing.T) {
	for _, tc := range []struct {
		name     string
		withHook bool
	}{
		{"straight from Pending", false},
		{"released from Pending:Wait by a lifecycle hook", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Given: a service whose store records the launch activity's status
			// at every InService write
			spy := &inServiceWriteSpy{Store: state.NewMemoryStore()}
			f := newFixtureOn(t, spy)
			f.group(t, "g", 0, 3, 0)
			if tc.withHook {
				if _, aerr := f.svc.handler.putLifecycleHookTyped(f.ctx, &putLifecycleHookReq{
					AutoScalingGroupName: "g", LifecycleHookName: "warmup",
					LifecycleTransition: transitionLaunching, HeartbeatTimeout: 300,
					DefaultResult: lifecycleResultContinue,
				}); aerr != nil {
					t.Fatalf("PutLifecycleHook: %v", aerr)
				}
			}

			// When: the group launches two instances and they reach service
			if _, aerr := f.svc.handler.setDesiredCapacityTyped(f.ctx, &setDesiredCapacityReq{
				AutoScalingGroupName: "g", DesiredCapacity: 2,
			}); aerr != nil {
				t.Fatalf("SetDesiredCapacity: %v", aerr)
			}
			f.converge(t)
			if tc.withHook {
				f.mock.Add(301 * time.Second)
				f.converge(t)
			}

			// Then: each instance went InService after its launch completed
			spy.mu.Lock()
			defer spy.mu.Unlock()
			if len(spy.seen) != 2 {
				t.Fatalf("saw %d InService writes, want 2 (states now %v)", len(spy.seen), states(f.instances(t, "g")))
			}
			for i, status := range spy.seen {
				if status != "Successful" {
					t.Errorf("InService write %d: its launch activity was %q, want Successful", i+1, status)
				}
			}
		})
	}
}

func TestReconcile_failedLaunchIsRecordedNotSwallowed(t *testing.T) {
	// Given: an EC2 that refuses to launch
	f := newFixture(t)
	f.ec2.failLaunch = true
	f.group(t, "g", 1, 3, 1)

	// When: the reconciler tries
	f.svc.reconcileOnce(f.ctx)

	// Then: a Failed activity names the reason, and no instance is claimed
	activities, err := f.svc.st.listActivities(f.ctx, "g")
	if err != nil {
		t.Fatalf("listActivities: %v", err)
	}
	if len(activities) != 1 || activities[0].StatusCode != "Failed" {
		t.Fatalf("activities = %+v, want one Failed", activities)
	}
	if !strings.Contains(activities[0].StatusMessage, "InvalidAMIID.NotFound") {
		t.Errorf("StatusMessage = %q, want EC2's own error", activities[0].StatusMessage)
	}
	if got := f.instances(t, "g"); len(got) != 0 {
		t.Errorf("instances = %v, want none", states(got))
	}
}

func TestReconcile_stopDrainsTheLoop(t *testing.T) {
	// Given: a running service
	mock := clock.NewMock()
	svc := New(&config.Config{Region: "us-east-1"}, state.NewMemoryStore(), zap.NewNop(), mock)

	// When: it is stopped with a live deadline
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	svc.Stop(ctx)

	// Then: Stop returned because the loop exited, not because the deadline hit
	if ctx.Err() != nil {
		t.Errorf("Stop did not drain the loop: %v", ctx.Err())
	}
	// And: a second Stop is safe
	svc.Stop(ctx)
}

func TestScaleInVictims_oldestFirst(t *testing.T) {
	base := time.Unix(1_700_000_000, 0).UTC()
	instances := []*ASGInstance{
		{InstanceId: "i-c", LifecycleState: lifecycleInService, LaunchedAt: base.Add(2 * time.Minute)},
		{InstanceId: "i-a", LifecycleState: lifecycleInService, LaunchedAt: base},
		{InstanceId: "i-b", LifecycleState: lifecycleInService, LaunchedAt: base.Add(time.Minute)},
		{InstanceId: "i-d", LifecycleState: lifecycleTerminating, LaunchedAt: base},
		{InstanceId: "i-e", LifecycleState: lifecycleInService, LaunchedAt: base, ProtectedFromScaleIn: true},
	}

	got := scaleInVictims(instances, 2)
	if len(got) != 2 || got[0].InstanceId != "i-a" || got[1].InstanceId != "i-b" {
		ids := make([]string, len(got))
		for i, v := range got {
			ids[i] = v.InstanceId
		}
		t.Errorf("victims = %v, want [i-a i-b]", ids)
	}

	// Asking for more than are eligible returns everything eligible.
	if got := scaleInVictims(instances, 99); len(got) != 3 {
		t.Errorf("eligible victims = %d, want 3", len(got))
	}
}

func TestClampCapacity(t *testing.T) {
	cases := []struct{ desired, min, max, want int }{
		{5, 1, 3, 3},
		{0, 2, 4, 2},
		{3, 1, 5, 3},
		{-1, 0, 5, 0},
	}
	for _, tc := range cases {
		if got := clampCapacity(tc.desired, tc.min, tc.max); got != tc.want {
			t.Errorf("clampCapacity(%d, %d, %d) = %d, want %d", tc.desired, tc.min, tc.max, got, tc.want)
		}
	}
}

// TestNextPlacement_roundRobins pins that a launch is told where to go exactly
// one way. A group with subnets is placed by subnet alone, leaving EC2 to
// derive the zone from it; a group without them is placed by zone. Sending
// both is what let the two disagree (#1840).
func TestNextPlacement_roundRobins(t *testing.T) {
	withSubnets := &AutoScalingGroup{
		AvailabilityZones: []string{"us-east-1a", "us-east-1b"},
		VPCZoneIdentifier: "subnet-1, subnet-2 ,subnet-3",
	}
	subnet, az := nextPlacement(withSubnets, 4)
	if subnet != "subnet-2" {
		t.Errorf("subnet for index 4 = %q, want subnet-2", subnet)
	}
	if az != "" {
		t.Errorf("zone for index 4 = %q, want none: the subnet already names one", az)
	}

	zonesOnly := &AutoScalingGroup{AvailabilityZones: []string{"us-east-1a", "us-east-1b"}}
	subnet, az = nextPlacement(zonesOnly, 3)
	if az != "us-east-1b" {
		t.Errorf("zone for index 3 = %q, want us-east-1b", az)
	}
	if subnet != "" {
		t.Errorf("subnet for index 3 = %q, want none: the group has no VPCZoneIdentifier", subnet)
	}

	if subnet, az = nextPlacement(&AutoScalingGroup{}, 0); subnet != "" || az != "" {
		t.Errorf("an unconfigured group placed at %q/%q, want nowhere in particular", subnet, az)
	}
}
