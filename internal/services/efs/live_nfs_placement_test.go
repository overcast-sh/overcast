package efs

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/overcast-sh/overcast/internal/docker"
)

// A mount target is created in a subnet, which on AWS fixes the VPC its ENI
// lives in: `fs-….efs.<region>.amazonaws.com` resolves only from inside that
// VPC. Overcast placed every export on the default plane regardless, so a
// function or task placed in the VPC — held to the VPC's network once
// placement is enforced — could not resolve the mount target it was written
// against. These tests pin the subnet → VPC → network resolution that closes
// that, for a fresh export and for both paths that adopt a container after a
// restart.

// fakePlacementResolver answers from fixed tables, so a subnet or VPC absent
// from them stands in for one EC2 has no record of.
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

// backedVPC is a resolver that knows one subnet, in one VPC, whose Docker
// network exists.
func backedVPC() fakePlacementResolver {
	return fakePlacementResolver{
		subnetVPC: map[string]string{"subnet-in-vpc": "vpc-abc"},
		status:    map[string]string{"vpc-abc": "ok"},
		network:   map[string]string{"vpc-abc": "overcast-vpc-abc"},
	}
}

// newPlacementTestService is newNFSTestService with a named default plane, a
// VPC resolver, and placement enforced or not — enforced is what decides
// whether a VPC-placed export also keeps the default plane.
func newPlacementTestService(t *testing.T, fd *fakeNFSDaemon, resolver VPCNetworkResolver, enforced bool) *Service {
	t.Helper()
	svc := newNFSTestService(t, fd, true)
	svc.cfg.Network = "overcast"
	svc.cfg.DNSListening = enforced
	svc.SetVPCResolver(resolver)
	return svc
}

// connectionsTo returns the connects the daemon saw for one network.
func connectionsTo(fd *fakeNFSDaemon, network string) []networkConnect {
	var out []networkConnect
	for _, c := range fd.connections() {
		if c.Network == network {
			out = append(out, c)
		}
	}
	return out
}

// assertCarriesMountTargetAliases checks that a connect advertised the full
// alias set for the mount target — the file system's name on every base, not
// only the canonical one.
func assertCarriesMountTargetAliases(t *testing.T, svc *Service, c networkConnect, fsID, mtID string) {
	t.Helper()
	want := svc.mountTargetAliases("us-east-1", fsID, mtID)
	if len(want) == 0 {
		t.Fatalf("test setup: no aliases minted for %s", mtID)
	}
	if !slices.Equal(c.Aliases, want) {
		t.Fatalf("aliases on %s: want %v, got %v", c.Network, want, c.Aliases)
	}
	if !slices.ContainsFunc(c.Aliases, func(a string) bool { return strings.HasPrefix(a, fsID+".efs.us-east-1.") }) {
		t.Fatalf("aliases on %s must carry the file system's DNS name, got %v", c.Network, c.Aliases)
	}
}

func TestNFSExport_mountTargetJoinsItsSubnetsVPCNetwork(t *testing.T) {
	// Given: exports on, placement enforced, and a subnet EC2 owns in a VPC
	// whose Docker network exists.
	fd := newFakeNFSDaemon(t)
	svc := newPlacementTestService(t, fd, backedVPC(), true)
	ctx := context.Background()
	fsID := seedFileSystem(t, svc, ctx, "nfs-vpc")

	// When: a mount target is created in that subnet.
	mt, aerr := svc.createMountTargetTyped(ctx, &createMountTargetRequest{
		FileSystemId: fsID, SubnetId: "subnet-in-vpc",
	})
	if aerr != nil {
		t.Fatalf("CreateMountTarget: %v", aerr)
	}
	rec := awaitExport(t, svc, ctx, "us-east-1", mt.MountTargetId)

	// Then: the export joined the VPC's network carrying the mount target's
	// names, and nothing else — a VPC-placed resource is held to its VPC.
	vpc := connectionsTo(fd, "overcast-vpc-abc")
	if len(vpc) != 1 {
		t.Fatalf("expected one connect to the VPC network, got %+v (all: %+v)", vpc, fd.connections())
	}
	if vpc[0].Container != rec.NFSContainerId {
		t.Fatalf("VPC connect was for %q, want the export container %q", vpc[0].Container, rec.NFSContainerId)
	}
	assertCarriesMountTargetAliases(t, svc, vpc[0], fsID, mt.MountTargetId)
	if got := connectionsTo(fd, "overcast"); len(got) != 0 {
		t.Fatalf("a VPC-placed export must not also join the default plane, got %+v", got)
	}
}

func TestNFSExport_mountTargetOutsideAnyVPCStaysOnDefaultPlane(t *testing.T) {
	cases := []struct {
		name     string
		resolver VPCNetworkResolver
	}{
		{
			// Synthetic IDs a test or a hand-written call invented; EC2 has no
			// record of them, and inventing a VPC for them would be worse.
			name:     "subnet EC2 does not know",
			resolver: backedVPC(),
		},
		{
			// EC2 disabled: there is nobody to ask.
			name: "no resolver wired",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Given: a mount target Overcast cannot place in any VPC.
			fd := newFakeNFSDaemon(t)
			svc := newPlacementTestService(t, fd, tc.resolver, true)
			ctx := context.Background()
			fsID := seedFileSystem(t, svc, ctx, "nfs-novpc")

			// When: its export starts.
			mt, aerr := svc.createMountTargetTyped(ctx, &createMountTargetRequest{
				FileSystemId: fsID, SubnetId: "subnet-synthetic",
			})
			if aerr != nil {
				t.Fatalf("CreateMountTarget: %v", aerr)
			}
			awaitExport(t, svc, ctx, "us-east-1", mt.MountTargetId)

			// Then: it lands on the default plane with its names, as it always
			// has — "no VPC" is a working mount target here.
			def := connectionsTo(fd, "overcast")
			if len(def) != 1 {
				t.Fatalf("expected one connect to the default plane, got %+v (all: %+v)", def, fd.connections())
			}
			assertCarriesMountTargetAliases(t, svc, def[0], fsID, mt.MountTargetId)
			for _, c := range fd.connections() {
				if strings.HasPrefix(c.Network, "overcast-vpc-") {
					t.Fatalf("export joined a VPC network it was never placed in: %+v", c)
				}
			}
		})
	}
}

// The two adoption paths: an export container found by name when a mount
// target's export starts (a restart with the container still running), and
// one adopted from the daemon's container list by startup reconciliation.
// Both predate VPC placement — before this they sat on the default plane
// whatever subnet the mount target named — so both re-attach.

func TestNFSExport_reusedContainerRejoinsItsVPCNetwork(t *testing.T) {
	// Given: a mount target persisted across a restart, with its export
	// container still there under its name.
	fd := newFakeNFSDaemon(t)
	svc := newPlacementTestService(t, fd, backedVPC(), true)
	ctx := context.Background()
	const mtID, fsID = "fsmt-cccccccccccccccc1", "fs-cccccccccccccccc1"
	if err := svc.putMountTarget(ctx, "us-east-1", &mountTargetRecord{
		MountTargetId: mtID, FileSystemId: fsID, SubnetId: "subnet-in-vpc",
		LifeCycleState: stateAvailable,
	}); err != nil {
		t.Fatalf("seed mount target: %v", err)
	}
	fd.seedContainer(nfsContainerName(mtID), "ctr-reused", docker.ManagedLabels(serviceName, mtID))

	// When: the export starts and finds the container to reuse.
	svc.startExport(ctx, "us-east-1", mtID, fsID, "subnet-in-vpc")

	// Then: nothing was created, and the adopted container joined the control
	// plane and its VPC's network — with the mount target's names on the latter.
	if created := fd.createdContainers(); len(created) != 0 {
		t.Fatalf("reuse path created a container: %+v", created)
	}
	assertAdoptedPlacement(t, svc, fd, "ctr-reused", fsID, mtID)
}

func TestReconcileExports_adoptedContainerRejoinsItsVPCNetwork(t *testing.T) {
	// Given: a mount target persisted across a restart, and its container in
	// the daemon's list, running.
	fd := newFakeNFSDaemon(t)
	svc := newPlacementTestService(t, fd, backedVPC(), true)
	ctx := context.Background()
	const mtID, fsID = "fsmt-dddddddddddddddd1", "fs-dddddddddddddddd1"
	if err := svc.putMountTarget(ctx, "us-east-1", &mountTargetRecord{
		MountTargetId: mtID, FileSystemId: fsID, SubnetId: "subnet-in-vpc",
		LifeCycleState: stateAvailable, NFSContainerId: "ctr-adopted", NFSHostPort: 22060,
	}); err != nil {
		t.Fatalf("seed mount target: %v", err)
	}
	fd.mu.Lock()
	fd.listed = []docker.ContainerSummary{{
		ID: "ctr-adopted", Names: []string{"/" + nfsContainerName(mtID)}, State: "running",
		Labels: docker.ManagedLabels(serviceName, mtID),
		Ports: []struct {
			HostPort      int    `json:"PublicPort"`
			ContainerPort int    `json:"PrivatePort"`
			Type          string `json:"Type"`
		}{{HostPort: 22060, ContainerPort: 2049, Type: "tcp"}},
	}}
	fd.mu.Unlock()

	// When: reconciliation adopts it. The record already matches the
	// container, which is the common restart case and the one that used to
	// short-circuit before any attachment.
	svc.reconcileExports(ctx)

	// Then: the adopted container joined the control plane and its VPC's network.
	assertAdoptedPlacement(t, svc, fd, "ctr-adopted", fsID, mtID)
}

// assertAdoptedPlacement checks what AttachAdopted must have done for a
// container Overcast did not create in this run: joined the control plane
// (which a container from an older version may never have been on), joined
// the VPC network with every name, and kept off the default plane.
func assertAdoptedPlacement(t *testing.T, svc *Service, fd *fakeNFSDaemon, containerID, fsID, mtID string) {
	t.Helper()
	control := connectionsTo(fd, svc.cfg.ControlNetwork())
	if len(control) != 1 || control[0].Container != containerID {
		t.Fatalf("expected the adopted container %q on the control plane, got %+v (all: %+v)",
			containerID, control, fd.connections())
	}
	vpc := connectionsTo(fd, "overcast-vpc-abc")
	if len(vpc) != 1 || vpc[0].Container != containerID {
		t.Fatalf("expected the adopted container %q on the VPC network, got %+v (all: %+v)",
			containerID, vpc, fd.connections())
	}
	assertCarriesMountTargetAliases(t, svc, vpc[0], fsID, mtID)
	if got := connectionsTo(fd, "overcast"); len(got) != 0 {
		t.Fatalf("a VPC-placed export must not also join the default plane, got %+v", got)
	}
}

func TestCreateMountTarget_unlaunchableVPCIsRefused(t *testing.T) {
	// Given: exports on, and a subnet in a VPC with no Docker network behind
	// it — Docker was unavailable when it was created, or the create failed.
	unbacked := backedVPC()
	unbacked.status["vpc-abc"] = "unbacked"
	unbacked.network["vpc-abc"] = ""
	fd := newFakeNFSDaemon(t)
	svc := newPlacementTestService(t, fd, unbacked, true)
	ctx := context.Background()
	fsID := seedFileSystem(t, svc, ctx, "nfs-unbacked")

	// When: a mount target is requested there.
	_, aerr := svc.createMountTargetTyped(ctx, &createMountTargetRequest{
		FileSystemId: fsID, SubnetId: "subnet-in-vpc",
	})

	// Then: the create is refused with EFS's generic 400 naming the VPC and
	// why, rather than quietly minting an export nothing in the VPC can reach.
	if aerr == nil {
		t.Fatal("expected CreateMountTarget to be refused")
	}
	if aerr.Code != "BadRequest" || aerr.HTTPStatus != http.StatusBadRequest {
		t.Fatalf("want BadRequest/400, got %s/%d", aerr.Code, aerr.HTTPStatus)
	}
	if !strings.Contains(aerr.Message, "vpc-abc") || !strings.Contains(aerr.Message, "unbacked") {
		t.Fatalf("message must name the VPC and its network status, got %q", aerr.Message)
	}
	// Nothing was persisted and nothing was started.
	if mts, err := svc.listMountTargetsForFileSystem(ctx, "us-east-1", fsID); err != nil || len(mts) != 0 {
		t.Fatalf("refused mount target must not be persisted, got %+v (err=%v)", mts, err)
	}
	svc.nfsWg.Wait()
	if created := fd.createdContainers(); len(created) != 0 {
		t.Fatalf("refused mount target must not start an export, got %+v", created)
	}
}

func TestCreateMountTarget_unlaunchableVPCIsMetadataOnlyWithExportsOff(t *testing.T) {
	// Given: the same unbacked VPC, but no NFS data plane — the mount target is
	// metadata, and there is nothing to place. Refusing here would fail every
	// mock-mode and Docker-less CDK deploy for a network nobody needs.
	unbacked := backedVPC()
	unbacked.status["vpc-abc"] = "unbacked"
	fd := newFakeNFSDaemon(t)
	svc := newNFSTestService(t, fd, false)
	svc.SetVPCResolver(unbacked)
	ctx := context.Background()
	fsID := seedFileSystem(t, svc, ctx, "nfs-unbacked-off")

	// When/Then: the mount target is created as before.
	if _, aerr := svc.createMountTargetTyped(ctx, &createMountTargetRequest{
		FileSystemId: fsID, SubnetId: "subnet-in-vpc",
	}); aerr != nil {
		t.Fatalf("CreateMountTarget with exports off must not consult the VPC network: %v", aerr)
	}
}
