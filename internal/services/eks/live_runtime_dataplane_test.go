package eks

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"go.uber.org/zap"

	"github.com/overcast-sh/overcast/internal/clock"
	"github.com/overcast-sh/overcast/internal/config"
	"github.com/overcast-sh/overcast/internal/dataplane"
	"github.com/overcast-sh/overcast/internal/middleware"
	"github.com/overcast-sh/overcast/internal/state"
)

// live_runtime_dataplane_test.go — how a k3s control plane sits on the data
// plane, and what a caller is told about reaching it.
//
// The endpoint DescribeCluster hands out is a *data-plane* address: it points
// at the k3s container, not at Overcast. Everything here follows the rule the
// other container-backed services keep (docs/dev/container-networking.md): the
// container joins its plane before it starts, carrying every name it could be
// handed out under; and the name it is handed out under depends on who asks.

// The control plane has to be on its data plane before it starts. k3s begins
// pulling images from the moment it runs, and under OVERCAST_VPC_EGRESS=routed
// its route out is the egress network the placement names — a container that
// starts first races its own first outbound connection.
func TestStartLiveCluster_joinsTheDataPlaneBeforeStarting(t *testing.T) {
	// Given: a daemon that will bring up one control plane.
	fd := newFakeK3sDaemon(t)
	fd.readyzPort = newK3sReadyzServer(t)
	svc := newFakeDaemonService(t, fd)
	cluster := putCreatingCluster(t, svc, "web")

	// When: the cluster is bootstrapped.
	svc.startLiveCluster(context.Background(), liveModeTestRegion, cluster)

	// Then: the data-plane connect for the container precedes its start.
	requests := fd.requestLog()
	connectAt := slices.Index(requests, "POST /v1.45/networks/overcast/connect")
	startAt := slices.Index(requests, "POST /v1.45/containers/"+fakeK3sContainerID+"/start")
	if connectAt < 0 || startAt < 0 {
		t.Fatalf("expected both a data-plane connect and a container start, got %v", requests)
	}
	if connectAt > startAt {
		t.Fatalf("container was started (request %d) before it joined the data plane (request %d): %v",
			startAt, connectAt, requests)
	}

	// And: the attachment advertises the endpoint name under every base an
	// endpoint could be minted on, so whichever one a caller holds resolves.
	connects := fd.networkConnects()
	if len(connects) != 1 {
		t.Fatalf("expected one data-plane connect, got %#v", connects)
	}
	if connects[0].container != fakeK3sContainerID {
		t.Fatalf("connected container = %q, want %q", connects[0].container, fakeK3sContainerID)
	}
	for _, want := range []string{
		"web.us-east-1.eks.localhost.overcast.sh",
		"web.us-east-1.eks.localhost.localstack.cloud",
		"web.us-east-1.eks.localhost.floci.io",
		"web.us-east-1.eks.localhost",
	} {
		if !slices.Contains(connects[0].aliases, want) {
			t.Errorf("aliases = %#v, missing %q", connects[0].aliases, want)
		}
	}
	if got := readCluster(t, svc, "web"); got.Status != "ACTIVE" {
		t.Fatalf("expected the cluster to reach ACTIVE, got %q", got.Status)
	}
}

// describeClusterAs fetches cluster over its route as a caller arriving from
// addr on origin, the way the ClientEndpoint middleware would have stamped it.
func describeClusterAs(t *testing.T, svc *Service, name, origin, addr string) map[string]any {
	t.Helper()

	r := chi.NewRouter()
	svc.RegisterRoutes(r)
	ctx := middleware.ContextWithClientAddr(
		middleware.ContextWithClientEndpoint(context.Background(), origin), addr)
	req := httptest.NewRequest(http.MethodGet, "/clusters/"+name, nil).WithContext(ctx)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("DescribeCluster %s: %d %s", name, rec.Code, rec.Body.String())
	}
	var body struct {
		Cluster map[string]any `json:"cluster"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode DescribeCluster response: %v", err)
	}
	return body.Cluster
}

// putReadyLiveCluster stores an ACTIVE live cluster whose endpoint is the
// canonical, host-side form pollK3sReady records.
func putReadyLiveCluster(t *testing.T, svc *Service, name, hostPort string) {
	t.Helper()

	if err := svc.putCluster(context.Background(), liveModeTestRegion, &Cluster{
		Name:                 name,
		Arn:                  svc.clusterARN(liveModeTestRegion, name),
		Status:               "ACTIVE",
		Version:              "1.31",
		Endpoint:             "https://" + svc.cfg.ExternalHostname() + ":" + hostPort,
		CertificateAuthority: map[string]any{"data": testCAData},
		CreatedAt:            svc.clk.Now(),
	}); err != nil {
		t.Fatalf("put ready live cluster: %v", err)
	}
}

// A sibling container cannot use the published host port: inside a Docker
// network the endpoint hostname resolves to Overcast, which serves no API on
// that port. It is handed the alias the container answers to, on the port k3s
// listens on inside the container — the name registered in
// TestStartLiveCluster_joinsTheDataPlaneBeforeStarting.
func TestDescribeCluster_siblingContainerGetsTheEndpointAliasOnTheAPIPort(t *testing.T) {
	cases := []struct {
		name     string
		hostname string
		origin   string
		want     string
	}{
		{
			// The configured OVERCAST_HOSTNAME is the operator asserting one
			// name resolves for every party, so it wins over the origin.
			name:     "configured hostname",
			hostname: "overcast.local",
			origin:   "http://172.18.0.2:4566",
			want:     "https://web.us-east-1.eks.overcast.local:6443",
		},
		{
			// Without one, the name is a subdomain of the host the caller
			// reached Overcast on.
			name:   "the caller's own hostname",
			origin: "http://localhost.overcast.sh:4566",
			want:   "https://web.us-east-1.eks.localhost.overcast.sh:6443",
		},
		{
			// A name cannot be a subdomain of an address.
			name:   "an IP-literal origin falls back to the external hostname",
			origin: "http://172.18.0.2:4566",
			want:   "https://web.us-east-1.eks.localhost:6443",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Given: a ready cluster, stored in its canonical form.
			svc := New(liveTestConfig(tc.hostname), state.NewMemoryStore(), zap.NewNop(), clock.New())
			putReadyLiveCluster(t, svc, "web", "16443")

			// When: a container on a Docker bridge describes it.
			cluster := describeClusterAs(t, svc, "web", tc.origin, "172.18.0.7")

			// Then: it is handed the alias and the in-container API port.
			if cluster["endpoint"] != tc.want {
				t.Fatalf("endpoint = %#v, want %q", cluster["endpoint"], tc.want)
			}
		})
	}
}

// The host reaches the container only through its published port, so a host
// caller keeps the answer it always had.
func TestDescribeCluster_hostCallerGetsThePublishedPort(t *testing.T) {
	svc := New(liveTestConfig("overcast.local"), state.NewMemoryStore(), zap.NewNop(), clock.New())
	putReadyLiveCluster(t, svc, "web", "16443")

	cluster := describeClusterAs(t, svc, "web", "http://overcast.local:4566", "127.0.0.1")

	if want := "https://overcast.local:16443"; cluster["endpoint"] != want {
		t.Fatalf("endpoint = %#v, want %q", cluster["endpoint"], want)
	}
}

// The record keeps the canonical form whoever asked: what is stored is
// re-minted per caller at read time, never overwritten with one caller's view.
func TestDescribeCluster_storeKeepsTheCanonicalEndpoint(t *testing.T) {
	svc := New(liveTestConfig("overcast.local"), state.NewMemoryStore(), zap.NewNop(), clock.New())
	putReadyLiveCluster(t, svc, "web", "16443")

	_ = describeClusterAs(t, svc, "web", "http://overcast.local:4566", "172.18.0.7")

	if got := readCluster(t, svc, "web").Endpoint; got != "https://overcast.local:16443" {
		t.Fatalf("stored endpoint = %q, want the canonical host form", got)
	}
}

// The kubeconfig a container fetches has to point where that container can
// reach: the same per-caller answer, in the file's `server:` line.
func TestUpdateKubeconfig_serverIsMintedForTheCaller(t *testing.T) {
	svc := New(liveTestConfig("overcast.local"), state.NewMemoryStore(), zap.NewNop(), clock.New())
	putReadyLiveCluster(t, svc, "web", "16443")

	r := chi.NewRouter()
	svc.RegisterRoutes(r)
	ctx := middleware.ContextWithClientAddr(context.Background(), "172.18.0.7")
	req := httptest.NewRequest(http.MethodPost, "/_overcast/eks/clusters/web/kubeconfig", nil).WithContext(ctx)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("UpdateKubeconfig: %d %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Kubeconfig string `json:"kubeconfig"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode kubeconfig response: %v", err)
	}
	if want := "server: https://web.us-east-1.eks.overcast.local:6443"; !strings.Contains(body.Kubeconfig, want) {
		t.Fatalf("kubeconfig = %q, want it to carry %q", body.Kubeconfig, want)
	}
}

// endpointPublicAccess is AWS's own switch for whether the API server is
// reachable from outside the VPC, and AWS defaults it to true.
func TestClusterEndpointPublicAccess_defaultsToTrue(t *testing.T) {
	cases := []struct {
		name      string
		vpcConfig map[string]any
		want      bool
	}{
		{name: "no resourcesVpcConfig", vpcConfig: nil, want: true},
		{name: "field absent", vpcConfig: map[string]any{"subnetIds": []any{"subnet-a"}}, want: true},
		{name: "true", vpcConfig: map[string]any{"endpointPublicAccess": true}, want: true},
		{name: "false", vpcConfig: map[string]any{"endpointPublicAccess": false}, want: false},
		{name: "not a bool", vpcConfig: map[string]any{"endpointPublicAccess": "false"}, want: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := clusterEndpointPublicAccess(tc.vpcConfig); got != tc.want {
				t.Fatalf("clusterEndpointPublicAccess(%v) = %v, want %v", tc.vpcConfig, got, tc.want)
			}
		})
	}
}

// A VPC-placed control plane is held to its VPC network — unless the cluster
// asked for a public endpoint, which on AWS is what lets a caller outside the
// VPC reach the API server, and here keeps it on the default plane too. The
// same escape hatch RDS's PubliclyAccessible and ECS's assignPublicIp are.
func TestClusterPlacement_endpointPublicAccessDecidesTheDefaultPlane(t *testing.T) {
	cases := []struct {
		name         string
		vpcConfig    map[string]any
		wantNetworks []string
	}{
		{
			name:         "public endpoint (the default) joins both",
			vpcConfig:    map[string]any{"vpcId": "vpc-abc"},
			wantNetworks: []string{"overcast-vpc-vpc-abc", "overcast"},
		},
		{
			name:         "private endpoint is held to the VPC",
			vpcConfig:    map[string]any{"vpcId": "vpc-abc", "endpointPublicAccess": false},
			wantNetworks: []string{"overcast-vpc-vpc-abc"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Given: a launchable VPC, on a host where placement is enforced.
			svc := New(
				&config.Config{Region: liveModeTestRegion, AccountID: "000000000000", Network: "overcast",
					EKSMode: config.EKSModeLive, DNSListening: true},
				state.NewMemoryStore(), zap.NewNop(), clock.New(),
			)
			svc.SetVPCResolver(fakePlacementResolver{
				status:  map[string]string{"vpc-abc": "ok"},
				network: map[string]string{"vpc-abc": "overcast-vpc-vpc-abc"},
			})
			cluster := &Cluster{Name: "web", ResourcesVPCConfig: tc.vpcConfig}

			// When: the control plane is placed.
			placement, err := svc.clusterPlacement(context.Background(), liveModeTestRegion, cluster)
			if err != nil {
				t.Fatalf("clusterPlacement: %v", err)
			}

			// Then: the planes it joins follow the endpoint's access.
			if got := dataplane.DataNetworks(svc.cfg, placement); !slices.Equal(got, tc.wantNetworks) {
				t.Fatalf("DataNetworks = %#v, want %#v", got, tc.wantNetworks)
			}
			if len(placement.Aliases) == 0 {
				t.Fatal("placement carries no aliases — the control plane would be unresolvable by name")
			}
		})
	}
}

// A control plane found running after a restart was never attached by this
// process, and one from an older version may not even be on the control
// plane. Adopting it means putting it where a fresh bootstrap would have —
// the same planes, the same aliases — not only recording its ID.
func TestDescribeCluster_adoptedControlPlaneJoinsItsPlanes(t *testing.T) {
	// Given: a persisted CREATING cluster whose k3s container is already up
	// from an earlier run, and a process that knows nothing of it.
	fd := newFakeK3sDaemon(t)
	fd.readyzPort = newK3sReadyzServer(t)
	fd.adoptable = "restarted"
	svc := newFakeDaemonService(t, fd)
	putCreatingCluster(t, svc, "restarted")
	svc.setLiveClusterRuntime(liveModeTestRegion, "restarted", &liveClusterRuntime{})

	// When: the cluster is described.
	cluster := describeClusterAs(t, svc, "restarted", "http://localhost:4566", "127.0.0.1")

	// Then: the container is adopted and reported ready…
	if cluster["status"] != "ACTIVE" {
		t.Fatalf("status = %#v, want ACTIVE", cluster["status"])
	}
	// …and it was joined to the control plane and its data plane, advertising
	// its endpoint names, exactly as a fresh bootstrap attaches it.
	assertAdoptedAttachments(t, fd.networkConnects(), "restarted")
}

// assertAdoptedAttachments checks the connect calls AttachAdopted makes for a
// control plane on the default data plane: the control plane with no aliases,
// then the data plane carrying the endpoint names.
func assertAdoptedAttachments(t *testing.T, connects []networkConnect, name string) {
	t.Helper()

	var control, data *networkConnect
	for i := range connects {
		switch connects[i].network {
		case "overcast_control":
			control = &connects[i]
		case "overcast":
			data = &connects[i]
		}
	}
	if control == nil || control.container != fakeK3sContainerID {
		t.Fatalf("adopted container was not joined to the control plane: %#v", connects)
	}
	if data == nil || data.container != fakeK3sContainerID {
		t.Fatalf("adopted container was not joined to the data plane: %#v", connects)
	}
	if want := name + ".us-east-1.eks.localhost"; !slices.Contains(data.aliases, want) {
		t.Fatalf("data-plane aliases = %#v, missing %q", data.aliases, want)
	}
}
