package ec2

// vpc_network_unbacked_test.go — a refused network create is reported the
// moment it happens, and the report goes when the VPC is backed or deleted.
//
// The strategies swallow a create failure on purpose: CreateVpc answers 200
// and the record is written `unbacked`, because a VPC is metadata AWS never
// refuses and the next reconcile retries the create. The failure that made
// this necessary to report was an address pool a stranded network still held:
// the create failed, the VPC was unbacked, and the first sign was an RDS
// instance refused three minutes later for a VPC "not launchable".

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"go.uber.org/zap"

	"github.com/overcast-sh/overcast/internal/clock"
	"github.com/overcast-sh/overcast/internal/config"
	"github.com/overcast-sh/overcast/internal/docker"
	"github.com/overcast-sh/overcast/internal/state"
)

// refusingDaemon is a fake daemon that refuses every network create while
// refuse is set, with the message Docker gives for an overlapping pool, and
// creates them otherwise. What it creates it can inspect and list, in the
// state it was asked for, so a reconcile that backs the VPC sees a network
// that matches its spec.
type refusingDaemon struct {
	server *httptest.Server
	mu     sync.Mutex
	refuse bool
	nets   map[string]docker.NetworkInspect
}

const poolOverlap = "invalid pool request: Pool overlaps with other one on this address space"

func newRefusingDaemon(t *testing.T) *refusingDaemon {
	t.Helper()
	d := &refusingDaemon{refuse: true, nets: map[string]docker.NetworkInspect{}}
	d.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d.mu.Lock()
		defer d.mu.Unlock()
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/networks/create"):
			if d.refuse {
				http.Error(w, `{"message":"`+poolOverlap+`"}`, http.StatusForbidden)
				return
			}
			var req docker.CreateNetworkOptions
			_ = json.NewDecoder(r.Body).Decode(&req)
			id := "net-" + req.Name
			d.nets[id] = docker.NetworkInspect{
				ID: id, Name: req.Name, Driver: docker.DefaultNetworkDriver, Scope: "local",
				Internal: req.Internal, Labels: req.Labels,
				IPAM: docker.NetworkIPAM{Config: []docker.NetworkIPAMConfig{{Subnet: req.Subnet}}},
			}
			_, _ = w.Write([]byte(`{"Id":"` + id + `"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/v1.45/networks":
			out := make([]docker.NetworkSummary, 0, len(d.nets))
			for _, n := range d.nets {
				out = append(out, docker.NetworkSummary{ID: n.ID, Name: n.Name, Labels: n.Labels, Internal: n.Internal, Driver: n.Driver, IPAM: n.IPAM})
			}
			_ = json.NewEncoder(w).Encode(out)
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1.45/networks/"):
			key := strings.TrimPrefix(r.URL.Path, "/v1.45/networks/")
			for _, n := range d.nets {
				if n.ID == key || n.Name == key {
					_ = json.NewEncoder(w).Encode(n)
					return
				}
			}
			http.Error(w, `{"message":"no such network"}`, http.StatusNotFound)
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	t.Cleanup(d.server.Close)
	return d
}

func (d *refusingDaemon) allowCreates() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.refuse = false
}

func refusingHandler(t *testing.T, d *refusingDaemon) *Handler {
	t.Helper()
	origInContainer := runningInContainer
	t.Cleanup(func() { runningInContainer = origInContainer })
	runningInContainer = func() bool { return false }

	cfg := &config.Config{
		Region: "us-east-1", AccountID: "000000000000",
		EC2VPCNetworkStrategy: "shared", Network: "overcast", DataDir: t.TempDir(),
	}
	h := New(cfg, state.NewMemoryStore(), zap.NewNop(), clock.NewMock()).handler
	h.docker = docker.NewClient(strings.Replace(d.server.URL, "http://", "tcp://", 1), zap.NewNop())
	h.dockerReady.Store(true)
	return h
}

func unbackedProblems(h *Handler) []string {
	var out []string
	for _, p := range h.networkProblems() {
		if p.Unbacked {
			out = append(out, p.VpcID+": "+p.Detail)
		}
	}
	return out
}

func TestEnsureNetwork_refusedCreateIsReportedUntilTheVPCIsBacked(t *testing.T) {
	daemon := newRefusingDaemon(t)
	h := refusingHandler(t, daemon)
	ctx := context.Background()
	vpc := &VPC{VpcID: "vpc-b7732331", CidrBlock: "10.42.0.0/16", State: "available"}

	// When: CreateVpc's network step runs against a daemon that refuses the
	// pool.
	if aerr := h.vpcStrategy.EnsureNetwork(ctx, vpc); aerr != nil {
		t.Fatalf("EnsureNetwork failed the call: %v — the VPC is metadata and the create is best-effort", aerr.Message)
	}

	// Then: the call still succeeds, the VPC is unbacked, and the failure is
	// on record for the advisories with Docker's reason, now rather than when
	// something is placed in the VPC.
	if vpc.NetworkStatus != vpcNetworkStatusUnbacked || vpc.DockerNetworkID != "" {
		t.Fatalf("VPC recorded as %q backed by %q, want unbacked with no network", vpc.NetworkStatus, vpc.DockerNetworkID)
	}
	got := unbackedProblems(h)
	if len(got) != 1 || !strings.HasPrefix(got[0], "vpc-b7732331: ") || !strings.Contains(got[0], poolOverlap) {
		t.Fatalf("unbacked problems = %v, want one for vpc-b7732331 quoting Docker's reason", got)
	}

	// When: the pool is freed and the next reconcile backs the VPC.
	if aerr := h.store.putVPC(ctx, vpc); aerr != nil {
		t.Fatalf("putVPC: %v", aerr.Message)
	}
	daemon.allowCreates()
	h.reconcileNetworks(ctx, nil)

	// Then: the record names a network and the problem is gone.
	backed, aerr := h.store.getVPC(ctx, vpc.VpcID)
	if aerr != nil {
		t.Fatalf("getVPC: %v", aerr.Message)
	}
	if backed.DockerNetworkID == "" || backed.NetworkStatus != vpcNetworkStatusOK {
		t.Fatalf("after reconcile the VPC is %q backed by %q, want ok with a network", backed.NetworkStatus, backed.DockerNetworkID)
	}
	if got := unbackedProblems(h); len(got) != 0 {
		t.Fatalf("unbacked problems = %v after the VPC was backed, want none", got)
	}
}

func TestEnsureNetwork_refusedCreateReportGoesWithTheVPC(t *testing.T) {
	daemon := newRefusingDaemon(t)
	h := refusingHandler(t, daemon)
	ctx := context.Background()
	vpc := &VPC{VpcID: "vpc-gone", CidrBlock: "10.43.0.0/16", State: "available"}

	// Given: a VPC the daemon refused a network for.
	if aerr := h.vpcStrategy.EnsureNetwork(ctx, vpc); aerr != nil {
		t.Fatalf("EnsureNetwork: %v", aerr.Message)
	}
	if got := unbackedProblems(h); len(got) != 1 {
		t.Fatalf("unbacked problems = %v, want one", got)
	}

	// When: the VPC is deleted.
	h.forgetVPCNetwork(ctx, vpc)

	// Then: a deleted VPC cannot keep an advisory alive.
	if got := unbackedProblems(h); len(got) != 0 {
		t.Fatalf("unbacked problems = %v after the VPC was deleted, want none", got)
	}
}

func TestVPCNetworkStatus_unbackedVPCIsRetriedBeforeALaunchIsRefused(t *testing.T) {
	daemon := newRefusingDaemon(t)
	h := refusingHandler(t, daemon)
	svc := &Service{handler: h}
	ctx := context.Background()
	h.reconciledAll.Store(true) // a full pass has already run: only a retry can heal

	// Given: a VPC whose network the daemon refused at CreateVpc.
	vpc := &VPC{VpcID: "vpc-retry", CidrBlock: "10.44.0.0/16", State: "available"}
	if aerr := h.vpcStrategy.EnsureNetwork(ctx, vpc); aerr != nil {
		t.Fatalf("EnsureNetwork: %v", aerr.Message)
	}
	if aerr := h.store.putVPC(ctx, vpc); aerr != nil {
		t.Fatalf("putVPC: %v", aerr.Message)
	}

	// When: the pool is freed and a launch asks whether the VPC is launchable.
	daemon.allowCreates()
	got := svc.VPCNetworkStatus(ctx, vpc.VpcID)

	// Then: the VPC was backed on the spot instead of refusing the launch.
	if got != vpcNetworkStatusOK {
		t.Fatalf("VPCNetworkStatus = %q, want %q after the daemon accepts the create", got, vpcNetworkStatusOK)
	}
	if p := unbackedProblems(h); len(p) != 0 {
		t.Fatalf("unbacked problems = %v after the retry backed the VPC, want none", p)
	}
}
