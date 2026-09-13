package eks

import (
	"context"
	"slices"
	"testing"

	"go.uber.org/zap"

	"github.com/overcast-sh/overcast/internal/clock"
	"github.com/overcast-sh/overcast/internal/config"
	"github.com/overcast-sh/overcast/internal/state"
)

// A k3s control plane created with resourcesVpcConfig.subnetIds belongs in the
// VPC those subnets name, the same as an RDS instance or a cache node. Overcast
// placed it on the default plane regardless, which only worked while a
// VPC-placed resource also kept the default plane; these tests pin the
// resolution that survives the enforcement change.

type fakePlacementResolver struct {
	subnetVPC map[string]string
	status    map[string]string
	network   map[string]string
}

func (f fakePlacementResolver) VpcIDForSubnet(_ context.Context, subnetID string) string {
	return f.subnetVPC[subnetID]
}

func (f fakePlacementResolver) VPCNetworkStatus(_ context.Context, vpcID string) string {
	return f.status[vpcID]
}

func (f fakePlacementResolver) DockerNetworkForVpc(_ context.Context, vpcID string) string {
	return f.network[vpcID]
}

func newPlacementService(resolver VPCNetworkResolver) *Service {
	svc := New(
		&config.Config{Region: liveModeTestRegion, AccountID: "000000000000", Network: "overcast", EKSMode: config.EKSModeLive},
		state.NewMemoryStore(), zap.NewNop(), clock.New(),
	)
	svc.SetVPCResolver(resolver)
	return svc
}

func TestVPCForCluster_resolvesTheVPCTheSubnetsNamed(t *testing.T) {
	// Given: a cluster created with subnets EC2 owns. resourcesVpcConfig is
	// stored as the free-form map the API supplied, so the list is []any after
	// a JSON round-trip and []string when a caller built the record in process.
	svc := newPlacementService(fakePlacementResolver{
		subnetVPC: map[string]string{"subnet-a": "vpc-abc"},
	})

	cases := []struct {
		name    string
		subnets any
	}{
		{name: "decoded from JSON", subnets: []any{"subnet-a", "subnet-b"}},
		{name: "built in process", subnets: []string{"subnet-a", "subnet-b"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cluster := &Cluster{
				Name:               "web",
				ResourcesVPCConfig: map[string]any{"subnetIds": tc.subnets},
			}

			// When/Then: the control plane belongs in that VPC.
			if got := svc.vpcForCluster(context.Background(), cluster); got != "vpc-abc" {
				t.Fatalf("vpcForCluster = %q, want %q", got, "vpc-abc")
			}
		})
	}
}

// AWS only *returns* vpcId in resourcesVpcConfig, but a caller that sends it is
// naming the answer the subnets would have resolved to.
func TestVPCForCluster_honoursAnExplicitVpcID(t *testing.T) {
	svc := newPlacementService(fakePlacementResolver{})
	cluster := &Cluster{
		Name:               "web",
		ResourcesVPCConfig: map[string]any{"vpcId": "vpc-explicit"},
	}

	if got := svc.vpcForCluster(context.Background(), cluster); got != "vpc-explicit" {
		t.Fatalf("vpcForCluster = %q, want %q", got, "vpc-explicit")
	}
}

func TestVPCForCluster_fallsBackToTheDefaultPlane(t *testing.T) {
	cases := []struct {
		name     string
		cluster  *Cluster
		resolver VPCNetworkResolver
	}{
		{
			name:     "no resourcesVpcConfig at all",
			cluster:  &Cluster{Name: "web"},
			resolver: fakePlacementResolver{subnetVPC: map[string]string{"subnet-a": "vpc-abc"}},
		},
		{
			name:     "resourcesVpcConfig without subnetIds",
			cluster:  &Cluster{Name: "web", ResourcesVPCConfig: map[string]any{"endpointPublicAccess": true}},
			resolver: fakePlacementResolver{subnetVPC: map[string]string{"subnet-a": "vpc-abc"}},
		},
		{
			// Synthetic IDs EC2 has no record of. Inventing a VPC for them
			// would be worse than the default plane.
			name:     "subnets EC2 does not know",
			cluster:  &Cluster{Name: "web", ResourcesVPCConfig: map[string]any{"subnetIds": []any{"subnet-synthetic"}}},
			resolver: fakePlacementResolver{subnetVPC: map[string]string{"subnet-a": "vpc-abc"}},
		},
		{
			// subnetIds arrived as something that is not a list of strings.
			name:     "subnetIds of an unexpected shape",
			cluster:  &Cluster{Name: "web", ResourcesVPCConfig: map[string]any{"subnetIds": "subnet-a"}},
			resolver: fakePlacementResolver{subnetVPC: map[string]string{"subnet-a": "vpc-abc"}},
		},
		{
			name:    "no resolver wired",
			cluster: &Cluster{Name: "web", ResourcesVPCConfig: map[string]any{"subnetIds": []any{"subnet-a"}}},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Given: a cluster Overcast cannot place in any VPC.
			svc := newPlacementService(tc.resolver)

			// When/Then: it keeps landing on the default plane rather than
			// erroring — "no VPC" is a working cluster here.
			if got := svc.vpcForCluster(context.Background(), tc.cluster); got != "" {
				t.Fatalf("vpcForCluster = %q, want the default plane", got)
			}
		})
	}
}

func TestPlacementFor_usesTheVPCNetwork(t *testing.T) {
	// Given: a launchable VPC with a Docker network behind it.
	svc := newPlacementService(fakePlacementResolver{
		status:  map[string]string{"vpc-abc": "ok"},
		network: map[string]string{"vpc-abc": "overcast-vpc-vpc-abc"},
	})
	aliases := svc.clusterEndpointAliases(liveModeTestRegion, "web")
	if len(aliases) == 0 {
		t.Fatal("clusterEndpointAliases returned nothing to advertise")
	}

	// When: the control-plane container is placed.
	placement, err := svc.placementFor(context.Background(), "vpc-abc", nil, aliases, true)
	if err != nil {
		t.Fatalf("placementFor: %v", err)
	}

	// Then: it joins the VPC's network, still answering to its endpoint names.
	if placement.VPCNetwork != "overcast-vpc-vpc-abc" {
		t.Fatalf("VPCNetwork = %q, want the VPC's network", placement.VPCNetwork)
	}
	if !slices.Equal(placement.Aliases, aliases) {
		t.Fatalf("Aliases = %#v, want %#v", placement.Aliases, aliases)
	}
}

func TestPlacementFor_unlaunchableVPCIsAnError(t *testing.T) {
	// Given: a VPC whose Docker network could not be created.
	svc := newPlacementService(fakePlacementResolver{
		status: map[string]string{"vpc-abc": "unbacked"},
	})

	// When/Then: placing into it fails rather than quietly starting a control
	// plane nothing in the VPC can reach.
	if _, err := svc.placementFor(context.Background(), "vpc-abc", nil, nil, true); err == nil {
		t.Fatal("placementFor into an unbacked VPC: want an error, got nil")
	}
}

func TestPlacementFor_noVPCKeepsTheDefaultPlane(t *testing.T) {
	svc := newPlacementService(fakePlacementResolver{})
	aliases := svc.clusterEndpointAliases(liveModeTestRegion, "web")

	placement, err := svc.placementFor(context.Background(), "", nil, aliases, true)
	if err != nil {
		t.Fatalf("placementFor: %v", err)
	}
	if placement.VPCNetwork != "" {
		t.Fatalf("VPCNetwork = %q, want the default plane", placement.VPCNetwork)
	}
	if !slices.Equal(placement.Aliases, aliases) {
		t.Fatalf("Aliases = %#v, want %#v", placement.Aliases, aliases)
	}
}

// A nil resolver stored in the interface field must not be mistaken for a wired
// one: dataplane.PlaceInVPC only skips resolution when the interface itself is
// nil, and a typed nil would panic on the first call.
func TestPlacementFor_withoutAResolver(t *testing.T) {
	svc := newPlacementService(nil)

	placement, err := svc.placementFor(context.Background(), "vpc-abc", nil, nil, true)
	if err != nil {
		t.Fatalf("placementFor: %v", err)
	}
	if placement.VPCNetwork != "" {
		t.Fatalf("VPCNetwork = %q, want the default plane", placement.VPCNetwork)
	}
}
