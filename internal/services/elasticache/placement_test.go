package elasticache

// placement_test.go — which Docker network a cache container is attached to,
// as the daemon sees it.
//
// vpcForSubnetGroup answering the right VPC is necessary but not sufficient:
// the placement is only real once the daemon has been asked to connect the
// container to that VPC's network carrying every endpoint name. These tests
// drive the whole start path against the fake daemon and read the connect
// requests back.

import (
	"context"
	"slices"
	"testing"

	"github.com/overcast-sh/overcast/internal/docker"
)

// placementResolver is a VPCNetworkResolver for one VPC backed by one network.
type placementResolver struct {
	vpcID, network string
	subnets        []string
}

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

func (r placementResolver) VpcIDForSubnet(_ context.Context, subnetID string) string {
	if slices.Contains(r.subnets, subnetID) {
		return r.vpcID
	}
	return ""
}

func connectsTo(connects []networkConnect, network string) (networkConnect, bool) {
	for _, c := range connects {
		if c.Network == network {
			return c, true
		}
	}
	return networkConnect{}, false
}

func TestStartCacheContainer_namedSubnetGroupJoinsItsVPCNetwork(t *testing.T) {
	// Given: a VPC with a backed network, a cache subnet group naming its
	// subnets the way CloudFormation creates one (subnet IDs only, no VpcId),
	// and placement enforced, as it is wherever Overcast's resolver runs.
	fd := newFakeDockerDaemon(t)
	fd.release()
	h := newDockerTestHandler(t, fd)
	h.cfg.Network = "overcast"
	h.cfg.DNSListening = true
	h.vpcResolver = placementResolver{vpcID: "vpc-0abc", network: "net-vpc-0abc", subnets: []string{"subnet-1"}}
	ctx := testCtx()
	if aerr := h.store.putCacheSubnetGroup(ctx, &CacheSubnetGroup{
		CacheSubnetGroupName: "cache-subnets",
		SubnetIds:            []string{"subnet-1"},
	}); aerr != nil {
		t.Fatalf("putCacheSubnetGroup: %s", aerr.Message)
	}

	// When: a cluster in that subnet group is created.
	if _, aerr := h.createCacheClusterTyped(ctx, &ecCreateCacheClusterReq{
		CacheClusterId: "app-cache", Engine: "redis", CacheNodeType: "cache.t3.micro", NumCacheNodes: 1,
		CacheSubnetGroupName: "cache-subnets",
	}); aerr != nil {
		t.Fatalf("create: %v", aerr)
	}
	h.dockerWg.Wait()

	// Then: the container joined the VPC's network carrying the endpoint name
	// a GetAtt hands out, and stayed off the default plane.
	connects := fd.connections()
	vpc, ok := connectsTo(connects, "net-vpc-0abc")
	if !ok {
		t.Fatalf("container never joined the VPC network; connects = %+v", connects)
	}
	want := "app-cache.us-east-1.cfg.localhost.overcast.sh"
	if !slices.Contains(vpc.Aliases, want) {
		t.Errorf("VPC network aliases = %v, want to include %q", vpc.Aliases, want)
	}
	if _, onDefault := connectsTo(connects, "overcast"); onDefault {
		t.Errorf("container also joined the default plane; a VPC-placed cache gets its VPC network only: %+v", connects)
	}
}

func TestStartCacheContainer_withoutASubnetGroupJoinsTheDefaultPlane(t *testing.T) {
	fd := newFakeDockerDaemon(t)
	fd.release()
	h := newDockerTestHandler(t, fd)
	h.cfg.Network = "overcast"
	h.cfg.DNSListening = true
	h.vpcResolver = placementResolver{vpcID: "vpc-0abc", network: "net-vpc-0abc"}
	ctx := testCtx()

	if _, aerr := h.createCacheClusterTyped(ctx, &ecCreateCacheClusterReq{
		CacheClusterId: "bare-cache", Engine: "redis", CacheNodeType: "cache.t3.micro", NumCacheNodes: 1,
	}); aerr != nil {
		t.Fatalf("create: %v", aerr)
	}
	h.dockerWg.Wait()

	connects := fd.connections()
	def, ok := connectsTo(connects, "overcast")
	if !ok {
		t.Fatalf("container never joined the default plane; connects = %+v", connects)
	}
	want := "bare-cache.us-east-1.cfg.localhost.overcast.sh"
	if !slices.Contains(def.Aliases, want) {
		t.Errorf("default plane aliases = %v, want to include %q", def.Aliases, want)
	}
	_ = docker.EndpointAliases
}
