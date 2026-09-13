package rds

// placement_test.go — which planes an instance's engine container joins.
//
// PubliclyAccessible is AWS's escape hatch from a VPC and the one
// docs/networking/vpcs.md tells people to use. It was stored and echoed but
// never reached the placement, so a "public" instance joined its VPC network
// alone and a function outside that VPC had the endpoint refused.

import (
	"context"
	"slices"
	"testing"

	"github.com/overcast-sh/overcast/internal/config"
	"github.com/overcast-sh/overcast/internal/dataplane"
	"github.com/overcast-sh/overcast/internal/middleware"
)

// placementResolver backs one VPC with one network.
type placementResolver struct{ vpcID, network string }

func (r placementResolver) VPCNetworkStatus(_ context.Context, vpcID string) string {
	if vpcID == r.vpcID {
		return "ok"
	}
	return ""
}

func (r placementResolver) DockerNetworkForVpc(_ context.Context, vpcID string) string {
	if vpcID == r.vpcID {
		return r.network
	}
	return ""
}

func (r placementResolver) VpcIDForSubnet(context.Context, string) string { return r.vpcID }

func TestInstancePlacement_publiclyAccessibleJoinsTheDefaultPlaneToo(t *testing.T) {
	tests := []struct {
		name         string
		public       *bool
		wantNetworks []string
	}{
		{"private stays on its VPC network only", boolPtr(false), []string{"net-vpc-0abc"}},
		{"public joins the default plane as well", boolPtr(true), []string{"net-vpc-0abc", "overcast"}},
		// A subnet-group instance that did not say is private, as on AWS.
		{"unstated with a subnet group is private", nil, []string{"net-vpc-0abc"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Given: placement enforced, an instance in a subnet group in a
			// backed VPC.
			cfg := &config.Config{Region: "us-east-1", Network: "overcast", DNSListening: true}
			h := testHandler(cfg)
			h.vpcResolver = placementResolver{vpcID: "vpc-0abc", network: "net-vpc-0abc"}
			ctx := middleware.ContextWithRegion(context.Background(), cfg.Region)
			inst := &DBInstance{
				DBInstanceIdentifier: "orders",
				Engine:               "postgres",
				DBSubnetGroupName:    "db-subnets",
				VpcID:                "vpc-0abc",
				PubliclyAccessible:   tc.public,
			}

			// When: its placement is built.
			placement, err := h.instancePlacement(ctx, inst, []string{"orders.us-east-1.rds.localhost"})
			if err != nil {
				t.Fatalf("instancePlacement: %v", err)
			}

			// Then: the planes it joins follow the flag.
			got := dataplane.DataNetworks(cfg, placement)
			if !slices.Equal(got, tc.wantNetworks) {
				t.Errorf("DataNetworks = %v, want %v", got, tc.wantNetworks)
			}
			if !slices.Contains(placement.Aliases, "orders.us-east-1.rds.localhost") {
				t.Errorf("aliases %v lost the endpoint name", placement.Aliases)
			}
		})
	}
}

// An instance with no subnet group is in the default VPC, which is the
// default plane; Public has nothing to add there.
func TestInstancePlacement_noSubnetGroupIsTheDefaultPlane(t *testing.T) {
	cfg := &config.Config{Region: "us-east-1", Network: "overcast", DNSListening: true}
	h := testHandler(cfg)
	h.vpcResolver = placementResolver{vpcID: "vpc-0abc", network: "net-vpc-0abc"}
	inst := &DBInstance{DBInstanceIdentifier: "orders", Engine: "mysql"}

	placement, err := h.instancePlacement(context.Background(), inst, nil)
	if err != nil {
		t.Fatalf("instancePlacement: %v", err)
	}
	if got := dataplane.DataNetworks(cfg, placement); !slices.Equal(got, []string{"overcast"}) {
		t.Errorf("DataNetworks = %v, want [overcast]", got)
	}
}
